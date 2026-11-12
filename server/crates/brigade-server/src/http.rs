//! The contract's section 1: one `POST` per command, one result envelope per answer.
//!
//! This layer is thin on purpose. It reads the path, the credential and the body, hands the command to the
//! engine and writes the envelope back with the status the contract pairs with it. The one thing it does of
//! its own is hold a receive that asked to wait (the contract's section 4).
//!
//! The gateways' endpoints are on the same listener and outside the contract: a request under `/gateway/`
//! that a configured gateway takes is handed to it, and answered with the status and the JSON it gives.

use std::convert::Infallible;
use std::sync::Arc;
use std::time::Duration;

use brigade_core::hash::{same, sha256_hex};
use brigade_core::limits::MAX_REQUEST_BYTES;
use brigade_core::{Command, Error, envelope_error, envelope_ok};
use http_body_util::{BodyExt, Full};
use hyper::body::{Bytes, Incoming};
use hyper::header::{AUTHORIZATION, CONNECTION, CONTENT_LENGTH, CONTENT_TYPE, HeaderMap};
use hyper::server::conn::http1;
use hyper::service::service_fn;
use hyper::{Method, Request, Response, StatusCode};
use hyper_util::rt::{TokioIo, TokioTimer};
use serde_json::{Value, json};
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::net::TcpListener;
use tokio::sync::{Semaphore, watch};
use tokio::time::Instant;
use tokio_rustls::TlsAcceptor;

use crate::engine::Client;
use crate::gateway::Gateways;
use crate::gateway::endpoint::{self, Received, Route};
use crate::note::note;

/// The header that carries the creation token (the contract's section 3).
const CREATE_TOKEN_HEADER: &str = "brigade-create-token";

/// The path of a receive, the one request this layer may hold (the contract's section 4).
const RECEIVE_PATH: &str = "/v1/message/receive";

/// The path that says what the server is. It needs no credential and says nothing about any team.
const INFO_PATH: &str = "/v1/info";

/// How long a client has to send its request's head, and then its body.
const READ_TIMEOUT: Duration = Duration::from_secs(15);

/// How many times the most a reader takes is read of a body that is too long, before the request is refused.
const DRAINED_TIMES: usize = 4;

/// How long a TLS handshake may take.
const HANDSHAKE_TIMEOUT: Duration = Duration::from_secs(10);

/// How long one connection lasts at most: its request read, a receive that waits its thirty seconds, and
/// its answer written, with room to spare.
const CONNECTION_TIMEOUT: Duration = Duration::from_secs(90);

/// How many connections are served at once. A team is tens of sessions, each with one receive that waits.
const CONNECTIONS: usize = 1024;

/// What every request handler shares.
pub struct Shared {
    /// The way to the engine.
    pub engine: Client,
    /// The token `/v1/team/create` requires, when the operator set one.
    pub create_token: Option<String>,
    /// Becomes true when the server is told to stop.
    pub stopping: watch::Receiver<bool>,
    /// The Slack and mail gateways, when the operator configured any.
    pub gateways: Option<Arc<Gateways>>,
}

/// The result of `/v1/info`.
fn info() -> Value {
    json!({
        "contract_version": "1",
        "server": {"name": env!("CARGO_PKG_NAME"), "version": env!("CARGO_PKG_VERSION")},
    })
}

/// The SHA-256 of the credential a request presents, when it presents one of the contract's shape
/// (section 1): sixteen to 512 characters of its alphabet. Anything else is no credential.
fn caller(headers: &HeaderMap) -> Option<String> {
    let credential = headers
        .get(AUTHORIZATION)?
        .to_str()
        .ok()?
        .strip_prefix("Bearer ")?;
    let fits = (16..=512).contains(&credential.len())
        && credential
            .bytes()
            .all(|byte| byte.is_ascii_alphanumeric() || b"._~+/=-".contains(&byte));
    fits.then(|| sha256_hex(credential))
}

/// Why a request's body was not read.
enum Unread {
    /// The connection broke, or what came is not a body.
    Broken,
    /// The body did not arrive in time.
    Late,
    /// The body is longer than its reader takes.
    TooLong,
}

/// Reads a request's body, up to `most` bytes.
///
/// A body that is too long is read on and thrown away, up to four times as much, before it is refused: a
/// server that answers and closes while its client is still sending has its answer lost with the
/// connection, and the client never learns why.
async fn body_within(request: Request<Incoming>, most: usize) -> Result<Bytes, Unread> {
    let declared = request
        .headers()
        .get(CONTENT_LENGTH)
        .and_then(|length| length.to_str().ok()?.parse::<usize>().ok());
    let mut too_long = declared.is_some_and(|length| length > most);
    let drained = most.saturating_mul(DRAINED_TIMES);
    let deadline = Instant::now() + READ_TIMEOUT;
    let mut incoming = request.into_body();
    let (mut body, mut read) = (Vec::new(), 0_usize);
    loop {
        let frame = match tokio::time::timeout_at(deadline, incoming.frame()).await {
            Ok(Some(Ok(frame))) => frame,
            Ok(Some(Err(_))) => return Err(Unread::Broken),
            Ok(None) => break,
            Err(_) => return Err(Unread::Late),
        };
        let Ok(data) = frame.into_data() else { continue };
        read += data.len();
        if read > drained {
            return Err(Unread::TooLong);
        }
        if !too_long && read > most {
            too_long = true;
            body = Vec::new();
        }
        if !too_long {
            body.extend_from_slice(&data);
        }
    }
    if too_long {
        Err(Unread::TooLong)
    } else {
        Ok(Bytes::from(body))
    }
}

/// Reads a request's body, up to the contract's megabyte.
async fn body_of(request: Request<Incoming>) -> Result<Bytes, Error> {
    body_within(request, MAX_REQUEST_BYTES)
        .await
        .map_err(|unread| match unread {
            Unread::Broken => Error::malformed_json(),
            Unread::Late => Error::unavailable("the request's body did not arrive in time"),
            Unread::TooLong => Error::body_too_long(),
        })
}

/// Whether a receive's answer holds no message.
fn empty(result: &Value) -> bool {
    result["messages"].as_array().is_none_or(Vec::is_empty)
}

/// Answers one request with the `result` of its envelope, or the failure.
///
/// The body goes to the engine as it arrived. The engine looks at the credential before it reads anything
/// of the body beyond its members' names, so this layer reads nothing of it either.
async fn answer(shared: &Shared, request: Request<Incoming>) -> Result<Value, Error> {
    let path = request.uri().path().to_owned();
    if path != INFO_PATH && !Command::known_path(&path) {
        return Err(Error::no_such_endpoint());
    }
    if request.method() != Method::POST {
        return Err(Error::not_a_post());
    }
    let caller = caller(request.headers());
    let token = request
        .headers()
        .get(CREATE_TOKEN_HEADER)
        .and_then(|token| token.to_str().ok());
    let may_create = match &shared.create_token {
        None => true,
        Some(required) => token.is_some_and(|presented| same(presented, required)),
    };
    let body = body_of(request).await?;
    if path == INFO_PATH {
        return Ok(info());
    }
    let ask = || shared.engine.ask(caller.clone(), may_create, &path, body.clone());
    let mut changes = shared.engine.changes();
    // What has changed up to here is seen by the question below; only a later change is waited for.
    changes.borrow_and_update();
    let mut result = ask().await?;
    if path != RECEIVE_PATH || !empty(&result) {
        return Ok(result);
    }
    // A receive that asked to wait is held while its answer would be empty. It is asked again every time
    // the state changes, so it answers at once when a message is accepted for its session, and at once
    // with the refusal when its caller's membership ends or its credential is revoked. The engine answered
    // it once already, so the body reads as a receive.
    let wait = match Command::parse(&path, &body) {
        Ok(Command::MessageReceive(receive)) => receive.wait_seconds.unsigned_abs(),
        _ => 0,
    };
    let deadline = Instant::now() + Duration::from_secs(wait);
    let mut stopping = shared.stopping.clone();
    loop {
        // A server that is stopping answers what it has: the caller asks again of the one that starts.
        if !empty(&result) || Instant::now() >= deadline || *stopping.borrow() {
            return Ok(result);
        }
        tokio::select! {
            changed = changes.changed() => {
                if changed.is_err() {
                    return Err(Error::unavailable("the server is stopping"));
                }
            }
            () = tokio::time::sleep_until(deadline) => {}
            _ = stopping.changed() => {}
        }
        changes.borrow_and_update();
        result = ask().await?;
    }
}

/// The endpoint of a configured gateway that a request is for, when it is for one.
fn gateway_route(shared: &Shared, request: &Request<Incoming>) -> Option<(Arc<Gateways>, Route)> {
    let gateways = shared.gateways.as_ref()?;
    let route = Route::of(gateways, request.uri().path())?;
    Some((Arc::clone(gateways), route))
}

/// Answers one request to a gateway's endpoint with the status and the JSON the gateway gives.
///
/// The body is read only when what came before it does not already decide the answer, and then up to what
/// the endpoint takes: anyone can send a request here.
///
/// The gateway answers in a task of its own. Slack gives a request three seconds and then closes the
/// connection, and a request dropped with its connection would be carried half way and begun again at
/// Slack's next delivery. In its own task the first delivery is carried to its end, whoever is still
/// listening for the answer.
///
/// Such a task outlives its connection, so the connections' places do not bound them. The gateways have
/// places of their own: a request that finds none is told at once to come again, which Slack and a mail
/// provider do.
async fn gateway_answer(
    gateways: Arc<Gateways>,
    route: Route,
    request: Request<Incoming>,
) -> Response<Full<Bytes>> {
    let method = request.method().as_str().to_owned();
    let path = request.uri().path().to_owned();
    // Each byte of a header's value is one character, as the Fetch standard reads a header.
    let headers = request
        .headers()
        .iter()
        .map(|(name, value)| {
            (
                name.as_str().to_owned(),
                value.as_bytes().iter().copied().map(char::from).collect(),
            )
        })
        .collect();
    let mut received = Received {
        method,
        path,
        headers,
        body: String::new(),
    };
    // What came before the body may already decide the answer, and then none of the body is read: a
    // request anyone can send is not kept in memory before it is known whose it is.
    if let Some(answer) = endpoint::before_body(&gateways, route, &received) {
        return response(answer.status, answer.body);
    }
    // With every place taken there is nothing to read the body for.
    if gateways.full() {
        let answer = endpoint::answer_busy();
        return response(answer.status, answer.body);
    }
    received.body = match body_within(request, route.most(&gateways)).await {
        Ok(bytes) => {
            let text = String::from_utf8_lossy(&bytes);
            text.strip_prefix('\u{feff}').unwrap_or(&text).to_owned()
        }
        Err(unread) => {
            let answer = match unread {
                Unread::Broken => endpoint::answer_broken(),
                Unread::Late => endpoint::answer_late(),
                Unread::TooLong => endpoint::answer_too_long(),
            };
            return response(answer.status, answer.body);
        }
    };
    // The place is taken once the body is read, and not before: a caller that sends its body slowly holds
    // a connection's place, and none of the gateways'.
    let Some(place) = gateways.place() else {
        let answer = endpoint::answer_busy();
        return response(answer.status, answer.body);
    };
    let answering = tokio::spawn(async move {
        let _place = place;
        endpoint::answer(&gateways, route, &received).await
    });
    let answer = answering.await.unwrap_or_else(|_| {
        note("a gateway's endpoint ended before it answered");
        endpoint::answer_failed()
    });
    response(answer.status, answer.body)
}

/// Writes an envelope with the status the contract's section 1 pairs with it.
fn respond(outcome: Result<Value, Error>) -> Response<Full<Bytes>> {
    let (status, envelope) = match outcome {
        Ok(result) => (200, envelope_ok(result)),
        Err(error) => (error.code.http_status(), envelope_error(&error)),
    };
    let mut body = envelope.to_string();
    body.push('\n');
    response(status, body)
}

/// One answer: a status and a JSON body, on a connection that closes after it.
fn response(status: u16, body: String) -> Response<Full<Bytes>> {
    let mut response = Response::new(Full::new(Bytes::from(body)));
    *response.status_mut() = StatusCode::from_u16(status).unwrap_or(StatusCode::INTERNAL_SERVER_ERROR);
    response.headers_mut().insert(
        CONTENT_TYPE,
        hyper::header::HeaderValue::from_static("application/json"),
    );
    // One request per connection. A connection the server closes while a client is about to reuse it is a
    // request that fails for no reason the client can see; closing after every answer leaves no such moment.
    response
        .headers_mut()
        .insert(CONNECTION, hyper::header::HeaderValue::from_static("close"));
    response
}

async fn serve_connection<Io>(io: Io, shared: Arc<Shared>)
where
    Io: AsyncRead + AsyncWrite + Unpin,
{
    let service = service_fn(move |request| {
        let shared = Arc::clone(&shared);
        async move {
            let response = match gateway_route(&shared, &request) {
                Some((gateways, route)) => gateway_answer(gateways, route, request).await,
                None => respond(answer(&shared, request).await),
            };
            Ok::<_, Infallible>(response)
        }
    });
    // A connection that ends badly is the client's affair: a request that was answered was answered.
    let _ = http1::Builder::new()
        .timer(TokioTimer::new())
        .header_read_timeout(READ_TIMEOUT)
        .keep_alive(false)
        .serve_connection(TokioIo::new(io), service)
        .await;
}

/// Serves requests on `listener` until the server is told to stop, and then gives the requests that are
/// being answered `drain` to be answered: one whose entry is in the log should get its answer, and one a
/// gateway is carrying should be carried.
pub async fn serve(listener: TcpListener, tls: Option<TlsAcceptor>, shared: Arc<Shared>, drain: Duration) {
    let room = Arc::new(Semaphore::new(CONNECTIONS));
    let mut stop = shared.stopping.clone();
    loop {
        let accepted = tokio::select! {
            accepted = listener.accept() => accepted,
            _ = stop.changed() => break,
        };
        let stream = match accepted {
            Ok((stream, _)) => stream,
            Err(error) => {
                note(&format!("a connection could not be accepted: {error}"));
                // Out of file descriptors, most likely: give the ones in use a moment to close.
                tokio::time::sleep(Duration::from_millis(50)).await;
                continue;
            }
        };
        // With every place taken the connection is dropped: the client sees a refused request and retries.
        let Ok(place) = Arc::clone(&room).try_acquire_owned() else {
            continue;
        };
        let (shared, tls) = (Arc::clone(&shared), tls.clone());
        tokio::spawn(async move {
            let _place = place;
            let _ = stream.set_nodelay(true);
            // A connection that outlives its one request, as one whose client never reads the answer
            // does, is dropped: it would hold its place for as long as the client liked.
            let _ = tokio::time::timeout(CONNECTION_TIMEOUT, async {
                match tls {
                    None => serve_connection(stream, shared).await,
                    Some(tls) => {
                        let handshake = tokio::time::timeout(HANDSHAKE_TIMEOUT, tls.accept(stream)).await;
                        if let Ok(Ok(stream)) = handshake {
                            serve_connection(stream, shared).await;
                        }
                    }
                }
            })
            .await;
        });
    }
    // Every connection holds a place while it is served: with all of them free again, every request that
    // was being answered has its answer. A receive that was waiting was answered at the stop.
    let deadline = Instant::now() + drain;
    let places = u32::try_from(CONNECTIONS).unwrap_or(u32::MAX);
    let _ = tokio::time::timeout_at(deadline, room.acquire_many(places)).await;
    // A gateway's request whose caller hung up is still being carried, and has the rest of the same time to
    // be. What a stop cuts short all the same is carried at the provider's next delivery: nothing says a
    // delivery came until the gateway is done with it.
    if let Some(gateways) = &shared.gateways {
        let _ = tokio::time::timeout_at(deadline, gateways.idle()).await;
    }
}

#[cfg(test)]
mod tests {
    use hyper::header::{AUTHORIZATION, HeaderMap, HeaderValue};

    use super::caller;

    fn with(authorization: &str) -> HeaderMap {
        let mut headers = HeaderMap::new();
        headers.insert(AUTHORIZATION, HeaderValue::from_str(authorization).unwrap());
        headers
    }

    #[test]
    fn a_credential_is_a_bearer_token_of_the_contracts_shape() {
        let hash = caller(&with("Bearer 0123456789abcdef")).unwrap();
        assert_eq!(hash.len(), 64);
        assert_ne!(caller(&with("Bearer 0123456789abcdeg")).unwrap(), hash);
        assert!(caller(&with("Bearer A-Z_a.z~0+9/=-xyz")).is_some());
        for not_one in [
            "",
            "Bearer ",
            "Bearer short",
            "Basic 0123456789abcdef",
            "bearer 0123456789abcdef",
            "Bearer 0123456789 abcdef",
            "Bearer 0123456789abcde\u{e9}",
        ] {
            let Ok(value) = HeaderValue::from_str(not_one) else {
                continue;
            };
            let mut headers = HeaderMap::new();
            headers.insert(AUTHORIZATION, value);
            assert!(caller(&headers).is_none(), "{not_one:?}");
        }
        assert!(caller(&HeaderMap::new()).is_none());
        assert!(caller(&with(&format!("Bearer {}", "a".repeat(513)))).is_none());
        assert!(caller(&with(&format!("Bearer {}", "a".repeat(512)))).is_some());
    }
}
