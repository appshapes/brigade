//! The gateways' way out: one HTTPS request to Slack or to a mail provider, and its answer.
//!
//! `brigade-gateway` builds every request and reads every answer; this is the one place a connection is
//! made. A request gets a connection of its own, as the Edge Functions' `fetch` gave each one, so there is no
//! pool to go stale between two ticks. The server is checked against Mozilla's list of authorities, which is
//! data in the binary, so the gateways need nothing of the host's.
//!
//! Plain HTTP is spoken to the server's own machine and to nothing else: that is how a test puts a stand-in
//! for Slack beside the server, and no token ever crosses a network in clear.

use std::sync::Arc;
use std::time::Duration;

use brigade_gateway::HttpRequest;
use http_body_util::{BodyExt, Full, Limited};
use hyper::body::Bytes;
use hyper::header::{CONNECTION, HOST, USER_AGENT};
use hyper::{Request, Uri};
use hyper_util::rt::TokioIo;
use rustls::pki_types::ServerName;
use rustls::{ClientConfig, RootCertStore};
use tokio::io::{AsyncRead, AsyncWrite};
use tokio::net::TcpStream;
use tokio_rustls::TlsConnector;

/// How long a request may take from its first byte to its answer's last: the budget a tick's call had.
const REQUEST_TIMEOUT: Duration = Duration::from_secs(20);

/// How long a connection may take to open.
const CONNECT_TIMEOUT: Duration = Duration::from_secs(10);

/// The most of an answer that is read. A page of a workspace's users is the largest answer a gateway asks
/// for, and it is far below this.
const MAX_ANSWER_BYTES: usize = 8 << 20;

/// An answer to a request.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Reply {
    /// The HTTP status.
    pub status: u16,
    /// The headers, names in lower case.
    pub headers: Vec<(String, String)>,
    /// The body as text: UTF-8, read leniently, without a byte order mark.
    pub body: String,
}

impl Reply {
    /// The value of a header, its name in any case.
    #[must_use]
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(header, _)| header.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

/// What makes the gateways' requests.
#[derive(Clone)]
pub struct Outbound {
    tls: TlsConnector,
}

impl Outbound {
    /// A client that trusts the public authorities.
    #[must_use]
    pub fn new() -> Self {
        let mut roots = RootCertStore::empty();
        roots.extend(webpki_roots::TLS_SERVER_ROOTS.iter().cloned());
        Self::trusting(roots)
    }

    /// A client that trusts `roots` and nothing else.
    #[must_use]
    pub fn trusting(roots: RootCertStore) -> Self {
        let config = ClientConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_safe_default_protocol_versions()
            .expect("ring's provider supports the default protocol versions")
            .with_root_certificates(roots)
            .with_no_client_auth();
        Self {
            tls: TlsConnector::from(Arc::new(config)),
        }
    }

    /// Makes one request and reads its answer.
    ///
    /// # Errors
    /// A sentence that says what failed and never what the request carried: no header, no body and no more
    /// of the URL than its host.
    pub async fn request(&self, request: &HttpRequest) -> Result<Reply, String> {
        let uri: Uri = request
            .url
            .parse()
            .map_err(|_| "a gateway's request has a URL that does not read".to_owned())?;
        let host = uri
            .host()
            .ok_or("a gateway's request has a URL with no host")?
            .to_owned();
        let secure = match uri.scheme_str() {
            Some("https") => true,
            Some("http") if is_loopback(&host) => false,
            _ => return Err(format!("a gateway's request to {host} is not HTTPS")),
        };
        // An IPv6 address is written between brackets in a URL and without them everywhere else: the
        // brackets are the URL's, and neither a connection nor a certificate's name takes them.
        let address = host
            .strip_prefix('[')
            .and_then(|rest| rest.strip_suffix(']'))
            .unwrap_or(&host)
            .to_owned();
        let exchange = async {
            let port = uri.port_u16().unwrap_or(if secure { 443 } else { 80 });
            let stream = tokio::time::timeout(CONNECT_TIMEOUT, TcpStream::connect((address.as_str(), port)))
                .await
                .map_err(|_| format!("{host} did not answer a connection in time"))?
                .map_err(|error| format!("{host} could not be connected to: {error}"))?;
            if secure {
                let name = ServerName::try_from(address.clone())
                    .map_err(|_| format!("{host} is not a name a certificate can be checked for"))?;
                let stream = self
                    .tls
                    .connect(name, stream)
                    .await
                    .map_err(|error| format!("{host} could not be trusted: {error}"))?;
                exchange(stream, &uri, &host, request).await
            } else {
                exchange(stream, &uri, &host, request).await
            }
        };
        tokio::time::timeout(REQUEST_TIMEOUT, exchange)
            .await
            .map_err(|_| format!("{host} did not answer in time"))?
    }
}

/// Whether `host` is this machine: the only host plain HTTP is spoken to.
fn is_loopback(host: &str) -> bool {
    host.eq_ignore_ascii_case("localhost")
        || host
            .trim_start_matches('[')
            .trim_end_matches(']')
            .parse::<std::net::IpAddr>()
            .is_ok_and(|address| address.is_loopback())
}

/// Sends `request` over an open connection and reads the whole answer.
async fn exchange<Io>(io: Io, uri: &Uri, host: &str, request: &HttpRequest) -> Result<Reply, String>
where
    Io: AsyncRead + AsyncWrite + Unpin + Send + 'static,
{
    let failed = |error: &dyn std::fmt::Display| format!("the request to {host} failed: {error}");
    let (mut sender, connection) = hyper::client::conn::http1::handshake(TokioIo::new(io))
        .await
        .map_err(|error| failed(&error))?;
    // The connection is driven beside the request and ends with it.
    let driving = tokio::spawn(connection);
    let target = uri.path_and_query().map_or("/", |target| target.as_str());
    let authority = uri.authority().map_or(host, |authority| authority.as_str());
    let mut builder = Request::builder()
        .method(request.method)
        .uri(target)
        .header(HOST, authority)
        .header(USER_AGENT, concat!("brigade-server/", env!("CARGO_PKG_VERSION")))
        .header(CONNECTION, "close");
    for (name, value) in &request.headers {
        builder = builder.header(*name, value.as_str());
    }
    let body = request.body.clone().unwrap_or_default();
    let outgoing = builder
        .body(Full::new(Bytes::from(body)))
        .map_err(|_| format!("a gateway's request to {host} has a header that does not read"))?;
    let answer = sender
        .send_request(outgoing)
        .await
        .map_err(|error| failed(&error))?;
    let status = answer.status().as_u16();
    let headers = answer
        .headers()
        .iter()
        .map(|(name, value)| {
            (
                name.as_str().to_owned(),
                String::from_utf8_lossy(value.as_bytes()).into_owned(),
            )
        })
        .collect();
    let bytes = Limited::new(answer.into_body(), MAX_ANSWER_BYTES)
        .collect()
        .await
        .map_err(|_| format!("the answer of {host} could not be read, or is longer than a gateway reads"))?
        .to_bytes();
    driving.abort();
    let text = String::from_utf8_lossy(&bytes);
    let body = text.strip_prefix('\u{feff}').unwrap_or(&text).to_owned();
    Ok(Reply {
        status,
        headers,
        body,
    })
}

#[cfg(test)]
mod tests {
    use std::io::{Read, Write};
    use std::net::TcpListener;

    use rustls::pki_types::{CertificateDer, PrivateKeyDer, PrivatePkcs8KeyDer};
    use tokio::io::{AsyncReadExt, AsyncWriteExt};

    use super::*;

    fn post(url: String) -> HttpRequest {
        HttpRequest {
            method: "POST",
            url,
            headers: vec![
                ("Authorization", "Bearer token-MUST-NOT-LEAK".to_owned()),
                ("Content-Type", "application/json".to_owned()),
            ],
            body: Some(r#"{"channel":"C1"}"#.to_owned()),
        }
    }

    /// Reads one request from `stream` up to the end of a body of `Content-Length` bytes.
    fn read_request(stream: &mut impl Read) -> String {
        let mut seen = Vec::new();
        let mut buffer = [0_u8; 1024];
        loop {
            let read = stream.read(&mut buffer).unwrap();
            seen.extend_from_slice(&buffer[..read]);
            let text = String::from_utf8_lossy(&seen).into_owned();
            if let Some(head) = text.find("\r\n\r\n") {
                let length = text
                    .lines()
                    .find_map(|line| {
                        line.to_ascii_lowercase()
                            .strip_prefix("content-length: ")?
                            .parse::<usize>()
                            .ok()
                    })
                    .unwrap_or(0);
                if text.len() >= head + 4 + length || read == 0 {
                    return text;
                }
            }
            if read == 0 {
                return text;
            }
        }
    }

    #[tokio::test]
    async fn a_request_goes_out_whole_and_its_answer_comes_back() {
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        let served = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let request = read_request(&mut stream);
            let body = "\u{feff}{\"ok\":true}";
            write!(
                stream,
                "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nX-OAuth-Scopes: chat:write\r\nContent-Length: {}\r\n\r\n{body}",
                body.len()
            )
            .unwrap();
            request
        });
        let reply = Outbound::new()
            .request(&post(format!("http://127.0.0.1:{port}/api/chat.postMessage?x=1")))
            .await
            .unwrap();
        assert_eq!(reply.status, 200);
        assert_eq!(reply.body, r#"{"ok":true}"#);
        assert_eq!(reply.header("x-oauth-scopes"), Some("chat:write"));
        let request = served.join().unwrap();
        assert!(
            request.starts_with("POST /api/chat.postMessage?x=1 HTTP/1.1\r\n"),
            "{request}"
        );
        let lower = request.to_ascii_lowercase();
        assert!(
            lower.contains(&format!("host: 127.0.0.1:{port}\r\n")),
            "{request}"
        );
        assert!(lower.contains("authorization: bearer token-must-not-leak\r\n"));
        assert!(lower.contains("content-length: 16\r\n"));
        assert!(request.ends_with(r#"{"channel":"C1"}"#));
    }

    #[tokio::test]
    async fn this_machine_is_reached_by_its_ipv6_address_too() {
        // Where the machine has no IPv6 loopback there is nothing to show.
        let Ok(listener) = TcpListener::bind("[::1]:0") else {
            return;
        };
        let port = listener.local_addr().unwrap().port();
        let served = std::thread::spawn(move || {
            let (mut stream, _) = listener.accept().unwrap();
            let request = read_request(&mut stream);
            write!(stream, "HTTP/1.1 200 OK\r\nContent-Length: 2\r\n\r\n{{}}").unwrap();
            request
        });
        let reply = Outbound::new()
            .request(&post(format!("http://[::1]:{port}/api/chat.postMessage")))
            .await
            .unwrap();
        assert_eq!((reply.status, reply.body.as_str()), (200, "{}"));
        // The brackets are where a URL has them, and so the Host header keeps them.
        let request = served.join().unwrap().to_ascii_lowercase();
        assert!(request.contains(&format!("host: [::1]:{port}\r\n")), "{request}");
    }

    #[tokio::test]
    async fn plain_http_is_spoken_to_this_machine_only_and_a_failure_says_nothing_of_the_request() {
        let refused = Outbound::new()
            .request(&post("http://slack.example.com/api/chat.postMessage".to_owned()))
            .await
            .unwrap_err();
        assert!(
            refused.contains("slack.example.com") && refused.contains("not HTTPS"),
            "{refused}"
        );
        // Nothing listens here: the failure names the host and never the token or the body.
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = listener.local_addr().unwrap().port();
        drop(listener);
        let failed = Outbound::new()
            .request(&post(format!("http://127.0.0.1:{port}/api/x")))
            .await
            .unwrap_err();
        assert!(failed.contains("127.0.0.1"), "{failed}");
        assert!(
            !failed.contains("MUST-NOT-LEAK") && !failed.contains("C1"),
            "{failed}"
        );
    }

    #[tokio::test]
    async fn https_trusts_the_authorities_it_was_given_and_no_other() {
        let key = rcgen::KeyPair::generate().unwrap();
        let made = rcgen::CertificateParams::new(vec!["localhost".to_owned()])
            .unwrap()
            .self_signed(&key)
            .unwrap();
        let certificate = CertificateDer::from(made.der().to_vec());
        let config =
            rustls::ServerConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
                .with_safe_default_protocol_versions()
                .unwrap()
                .with_no_client_auth()
                .with_single_cert(
                    vec![certificate.clone()],
                    PrivateKeyDer::Pkcs8(PrivatePkcs8KeyDer::from(key.serialize_der())),
                )
                .unwrap();
        let acceptor = tokio_rustls::TlsAcceptor::from(Arc::new(config));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let port = listener.local_addr().unwrap().port();
        tokio::spawn(async move {
            // Two connections: one that is trusted and answered, one that is not.
            for _ in 0..2 {
                let (stream, _) = listener.accept().await.unwrap();
                let acceptor = acceptor.clone();
                tokio::spawn(async move {
                    let Ok(mut stream) = acceptor.accept(stream).await else {
                        return;
                    };
                    let mut buffer = [0_u8; 2048];
                    let _ = stream.read(&mut buffer).await;
                    let _ = stream
                        .write_all(b"HTTP/1.1 201 Created\r\nContent-Length: 2\r\n\r\n{}")
                        .await;
                    let _ = stream.shutdown().await;
                });
            }
        });
        let url = format!("https://localhost:{port}/emails");
        let mut roots = RootCertStore::empty();
        roots.add(certificate).unwrap();
        let reply = Outbound::trusting(roots)
            .request(&post(url.clone()))
            .await
            .unwrap();
        assert_eq!((reply.status, reply.body.as_str()), (201, "{}"));
        let refused = Outbound::new().request(&post(url)).await.unwrap_err();
        assert!(refused.contains("could not be trusted"), "{refused}");
    }
}
