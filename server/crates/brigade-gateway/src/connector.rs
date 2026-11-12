//! The mail connector contract (`docs/mail-connectors.md`; `supabase/functions/_shared/connector.ts` and
//! the decisions of `connector_serve.ts`).
//!
//! A connector stands between one mail provider and the gateway core, and the two exchange exactly two JSON
//! shapes over HTTPS: a received mail, connector to core, and a mail to send, core to connector. One secret
//! authenticates both directions: a bearer header, a basic-auth password, or the path segment after the
//! endpoint's name, for a caller that can only be given a URL.
//!
//! Nothing here speaks HTTP. A request to make is an [`HttpRequest`], what a peer answered is a status and
//! a body, and what to answer is an [`Answer`]; the caller carries them. A body is the text the Fetch
//! standard's `text()` gives: UTF-8, lossily decoded, without a byte order mark.

use std::fmt;
use std::sync::LazyLock;

use brigade_core::Timestamp;
use regex::Regex;
use serde::Serialize;
use serde::ser::Serializer;

use crate::js;
use crate::json::{Json, filled};
use crate::mail::{InboundMail, OutboundMail, is_address};
use crate::time;

/// The version of the contract, and the value of `brigade_mail` in both shapes.
pub const CONTRACT_VERSION: u8 = 1;

/// `/^Bearer\s+(\S+)\s*$/i`.
static BEARER: LazyLock<Regex> = LazyLock::new(|| credentials_pattern("Bearer"));

/// `/^Basic\s+(\S+)\s*$/i`.
static BASIC: LazyLock<Regex> = LazyLock::new(|| credentials_pattern("Basic"));

fn credentials_pattern(scheme: &str) -> Regex {
    js::regex(&format!(r"^(?i-u:{scheme})[{s}]+([^{s}]+)[{s}]*$", s = js::SPACE))
}

/// A failed send. `retryable` says whether a later attempt can succeed.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct SendError {
    /// The HTTP status the send was answered with; 0 when it was not answered at all.
    pub status: u16,
    /// What went wrong, as the sending session is told.
    pub message: String,
    /// Whether a later attempt can succeed.
    pub retryable: bool,
}

impl SendError {
    /// A failed send. When `retryable` is not stated it follows the status: a 429, a 5xx or no answer at
    /// all (status 0) can succeed later, and anything else is this mail's fault.
    #[must_use]
    pub fn new(status: u16, message: impl Into<String>, retryable: Option<bool>) -> Self {
        Self {
            status,
            message: message.into(),
            retryable: retryable.unwrap_or(status == 0 || status == 429 || status >= 500),
        }
    }
}

impl fmt::Display for SendError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for SendError {}

/// A provider webhook that is not authentic.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct WebhookRejected {
    /// Why, as the provider is answered.
    pub message: String,
    /// What the connector answers the provider: 401 by default, 403 for a provider that stops retrying on
    /// it (Postmark).
    pub status: u16,
}

impl WebhookRejected {
    /// A rejection answered with 401.
    #[must_use]
    pub fn new(message: impl Into<String>) -> Self {
        Self::with_status(message, 401)
    }

    /// A rejection answered with `status`.
    #[must_use]
    pub fn with_status(message: impl Into<String>, status: u16) -> Self {
        Self {
            message: message.into(),
            status,
        }
    }
}

impl fmt::Display for WebhookRejected {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        formatter.write_str(&self.message)
    }
}

impl std::error::Error for WebhookRejected {}

/// What a rule reads of an HTTP request: its path and its headers.
#[derive(Clone, Copy)]
pub struct Request<'a> {
    /// The path of the request's URL, without the query, as the HTTP layer has it. The TypeScript read
    /// `new URL(req.url).pathname`, which has dot segments removed; a caller whose HTTP layer does not
    /// remove them passes the path as it came.
    pub path: &'a str,
    /// The headers, name and value, in the order they came. A value is taken as it is given: the HTTP layer
    /// has already removed the white space around it, as the Fetch standard's `Headers` does.
    pub headers: &'a [(&'a str, &'a str)],
}

impl Request<'_> {
    /// The value of a header, its name in any case. A header that came more than once reads as its values
    /// joined by `, `, which is how the Fetch standard's `Headers.get` reads one.
    #[must_use]
    pub fn header(&self, name: &str) -> Option<String> {
        let values: Vec<&str> = self
            .headers
            .iter()
            .filter(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| *value)
            .collect();
        (!values.is_empty()).then(|| values.join(", "))
    }

    /// The segments of the path that are not empty.
    fn segments(&self) -> Vec<&str> {
        self.path
            .split('/')
            .filter(|segment| !segment.is_empty())
            .collect()
    }
}

/// An HTTP request for the caller to make. It has no `Debug`: its headers carry a secret.
#[derive(Clone, PartialEq, Eq)]
pub struct HttpRequest {
    /// The method.
    pub method: &'static str,
    /// The URL.
    pub url: String,
    /// The headers, name and value.
    pub headers: Vec<(&'static str, String)>,
    /// The body, JSON, for a request that has one.
    pub body: Option<String>,
}

impl HttpRequest {
    /// The value of a header, its name in any case.
    #[must_use]
    pub fn header(&self, name: &str) -> Option<&str> {
        self.headers
            .iter()
            .find(|(key, _)| key.eq_ignore_ascii_case(name))
            .map(|(_, value)| value.as_str())
    }
}

/// What a webhook carried: the provider's id for the mail and, when the webhook carried the mail itself,
/// the mail. With no mail the caller fetches it by the id.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Hook {
    /// The provider's id for the received mail.
    pub provider_id: String,
    /// The mail, when the webhook carried it.
    pub mail: Option<InboundMail>,
}

/// What a shipped connector wraps, as the decisions on either side of each HTTP exchange: the provider's
/// webhook, a fetch of a received mail by the provider's id (for a webhook that carries only the id), and a
/// send.
pub trait Provider {
    /// The provider's name.
    fn name(&self) -> &'static str;

    /// Reads a provider's webhook request. `None` is an event that is not a received mail.
    ///
    /// # Errors
    /// The request is not authentic.
    fn webhook(
        &self,
        request: &Request<'_>,
        raw_body: &str,
        now: Timestamp,
    ) -> Result<Option<Hook>, WebhookRejected>;

    /// The request that fetches a received mail.
    fn fetch_request(&self, provider_id: &str) -> HttpRequest;

    /// Reads the answer to [`Provider::fetch_request`] as a mail.
    ///
    /// # Errors
    /// Why the mail could not be read, for the connector's own log; the provider is answered 502.
    fn fetched(
        &self,
        provider_id: &str,
        status: u16,
        body: &str,
        now: Timestamp,
    ) -> Result<InboundMail, String>;

    /// The request that sends a mail.
    fn send_request(&self, mail: &OutboundMail) -> HttpRequest;

    /// Reads the answer to [`Provider::send_request`] as the provider's id for the sent mail.
    ///
    /// # Errors
    /// The provider refused the mail, or its answer could not be read.
    fn sent(&self, status: u16, body: &str) -> Result<String, SendError>;
}

/// Whether a status is one the Fetch standard calls ok: 200 to 299.
pub(crate) fn is_ok(status: u16) -> bool {
    (200..=299).contains(&status)
}

// ---- Authentication ---------------------------------------------------------------------------------------

/// Reads `Authorization: Bearer <token>`.
#[must_use]
pub fn bearer_of(request: &Request<'_>) -> Option<String> {
    let header = request.header("authorization").unwrap_or_default();
    Some(BEARER.captures(&header)?[1].to_owned())
}

/// Reads the password of `Authorization: Basic <base64 user:password>`; the user is not checked. Each byte
/// of the decoded credentials is one character, as `atob` has it, so a password that is not ASCII matches a
/// secret only where the secret's characters are those bytes.
#[must_use]
pub fn basic_password_of(request: &Request<'_>) -> Option<String> {
    let header = request.header("authorization").unwrap_or_default();
    let decoded = js::binary_string(&js::atob(&BASIC.captures(&header)?[1])?);
    decoded.split_once(':').map(|(_, password)| password.to_owned())
}

/// Reads the path segment after the last `anchor` (`…/mail-in/<secret>`), its percent escapes decoded.
#[must_use]
pub fn path_secret_of(request: &Request<'_>, anchor: &str) -> Option<String> {
    let segments = request.segments();
    let at = segments.iter().rposition(|segment| *segment == anchor)?;
    js::decode_uri_component(segments.get(at + 1)?)
}

/// Whether a request carries `secret` as a bearer, a basic-auth password or a path segment after `anchor`.
/// An empty secret authorizes nothing.
#[must_use]
pub fn authorized(request: &Request<'_>, anchor: &str, secret: &str) -> bool {
    if secret.is_empty() {
        return false;
    }
    [
        bearer_of(request),
        basic_password_of(request),
        path_secret_of(request, anchor),
    ]
    .iter()
    .flatten()
    .any(|got| constant_time_equal(got, secret))
}

/// Whether two strings are equal, in time that depends on their lengths and not on where they differ.
#[must_use]
pub fn constant_time_equal(left: &str, right: &str) -> bool {
    brigade_core::hash::same(left, right)
}

// ---- The wire shapes --------------------------------------------------------------------------------------

/// A received mail as a connector POSTs it to the core.
#[derive(Serialize)]
struct InboundWire<'a> {
    brigade_mail: u8,
    /// The provider's id for the mail: the core's idempotency key.
    id: &'a str,
    from: &'a str,
    from_name: Option<&'a str>,
    /// Every address the mail was delivered to, plus tags; the core keeps only mail addressed to its inbox.
    to: &'a [String],
    subject: &'a str,
    message_id: Option<&'a str>,
    in_reply_to: Option<&'a str>,
    references: &'a [String],
    text: &'a str,
    received_at: &'a str,
}

/// A mail to send as the core POSTs it to a connector's send endpoint.
#[derive(Serialize)]
struct OutboundWire<'a> {
    brigade_mail: u8,
    from: &'a str,
    to: &'a str,
    reply_to: Option<&'a str>,
    subject: &'a str,
    text: &'a str,
    headers: HeadersObject<'a>,
}

/// A mail's headers as a JSON object, in their own order.
pub(crate) struct HeadersObject<'a>(pub(crate) &'a [(String, String)]);

impl Serialize for HeadersObject<'_> {
    fn serialize<S: Serializer>(&self, serializer: S) -> Result<S::Ok, S::Error> {
        serializer.collect_map(self.0.iter().map(|(name, value)| (name, value)))
    }
}

/// `JSON.stringify` of one of this crate's shapes: no white space, the members in the order they are
/// declared.
pub(crate) fn stringify(shape: &impl Serialize) -> String {
    serde_json::to_string(shape).expect("a shape of this crate is always JSON")
}

/// The inbound shape of a mail, as JSON.
#[must_use]
pub fn to_inbound_wire(mail: &InboundMail) -> String {
    stringify(&InboundWire {
        brigade_mail: CONTRACT_VERSION,
        id: &mail.provider_id,
        from: &mail.from,
        from_name: mail.from_name.as_deref(),
        to: &mail.to,
        subject: &mail.subject,
        message_id: mail.message_id.as_deref(),
        in_reply_to: mail.in_reply_to.as_deref(),
        references: &mail.references,
        text: &mail.text,
        received_at: &mail.received_at,
    })
}

/// Reads and checks the inbound shape. The sender is lowercased; the delivery addresses are trimmed,
/// lowercased and each kept once; a mail that states no time of receipt was received at `now`.
///
/// # Errors
/// The first rule the body breaks, as the connector is answered.
pub fn from_inbound_wire(raw: &str, now: Timestamp) -> Result<InboundMail, String> {
    let wire = parse_object(raw)?;
    check_version(&wire)?;
    let id = filled(wire.get("id")).ok_or("id is required")?;
    let from = filled(wire.get("from")).unwrap_or("").to_lowercase();
    if !is_address(&from) {
        return Err("from must be an email address".to_owned());
    }
    let delivered = strings(wire.get("to")).ok_or("to must be a list of addresses")?;
    let mut to: Vec<String> = Vec::new();
    for address in delivered {
        let address = js::trim(address).to_lowercase();
        if !address.is_empty() && !to.contains(&address) {
            to.push(address);
        }
    }
    let references = match wire.get("references") {
        Some(references) => strings(Some(references)).ok_or("references must be a list of strings")?,
        None => Vec::new(),
    };
    for name in [
        "from_name",
        "subject",
        "message_id",
        "in_reply_to",
        "text",
        "received_at",
    ] {
        if wire.get(name).is_some_and(|value| value.as_str().is_none()) {
            return Err(format!("{name} must be a string"));
        }
    }
    let text = |name: &str| filled(wire.get(name)).map(str::to_owned);
    Ok(InboundMail {
        provider_id: id.to_owned(),
        from,
        from_name: text("from_name"),
        to,
        subject: text("subject").unwrap_or_default(),
        message_id: text("message_id"),
        in_reply_to: text("in_reply_to"),
        references: references.into_iter().map(str::to_owned).collect(),
        text: text("text").unwrap_or_default(),
        received_at: text("received_at").unwrap_or_else(|| time::now_iso(now)),
    })
}

/// The outbound shape of a mail, as JSON.
#[must_use]
pub fn to_outbound_wire(mail: &OutboundMail) -> String {
    stringify(&OutboundWire {
        brigade_mail: CONTRACT_VERSION,
        from: &mail.from,
        to: &mail.to,
        reply_to: mail.reply_to.as_deref(),
        subject: &mail.subject,
        text: &mail.text,
        headers: HeadersObject(&mail.headers),
    })
}

/// Reads and checks the outbound shape. The recipient is lowercased.
///
/// # Errors
/// The first rule the body breaks, as the core is answered.
pub fn from_outbound_wire(raw: &str) -> Result<OutboundMail, String> {
    let wire = parse_object(raw)?;
    check_version(&wire)?;
    let from = filled(wire.get("from")).ok_or("from is required")?;
    let to = filled(wire.get("to")).unwrap_or("").to_lowercase();
    if !is_address(&to) {
        return Err("to must be one email address".to_owned());
    }
    let subject = wire
        .get("subject")
        .and_then(Json::as_str)
        .ok_or("subject must be a string")?;
    let text = wire
        .get("text")
        .and_then(Json::as_str)
        .ok_or("text must be a string")?;
    if wire.get("reply_to").is_some_and(|value| value.as_str().is_none()) {
        return Err("reply_to must be a string".to_owned());
    }
    let mut headers = Vec::new();
    if let Some(object) = wire.get("headers") {
        if !matches!(object, Json::Object(_)) {
            return Err("headers must be an object".to_owned());
        }
        for (name, value) in object.entries() {
            let value = value
                .as_str()
                .ok_or_else(|| format!("header {name} must be a string"))?;
            headers.push((name.clone(), value.to_owned()));
        }
    }
    Ok(OutboundMail {
        from: from.to_owned(),
        to,
        reply_to: filled(wire.get("reply_to")).map(str::to_owned),
        subject: subject.to_owned(),
        text: text.to_owned(),
        headers,
    })
}

fn parse_object(raw: &str) -> Result<Json, String> {
    match Json::parse(raw) {
        None => Err("the body is not JSON".to_owned()),
        Some(object @ Json::Object(_)) => Ok(object),
        Some(_) => Err("the body is not a JSON object".to_owned()),
    }
}

fn check_version(wire: &Json) -> Result<(), String> {
    match wire.get("brigade_mail") {
        Some(Json::Number(version)) if *version == f64::from(CONTRACT_VERSION) => Ok(()),
        _ => Err(format!("brigade_mail must be {CONTRACT_VERSION}")),
    }
}

/// The elements of a list whose every element is a string.
fn strings(value: Option<&Json>) -> Option<Vec<&str>> {
    match value? {
        Json::List(items) => items.iter().map(Json::as_str).collect(),
        _ => None,
    }
}

// ---- The core's send client -------------------------------------------------------------------------------

/// How the core sends a mail: one POST to the connector's send URL with the secret as a bearer.
///
/// # Errors
/// The team has no send URL, which no later attempt mends.
pub fn send_request(send_url: &str, secret: &str, mail: &OutboundMail) -> Result<HttpRequest, SendError> {
    if send_url.is_empty() {
        return Err(SendError::new(
            0,
            "BRIGADE_MAIL_SEND_URL is not set: run the installer",
            Some(false),
        ));
    }
    Ok(post_json(send_url, secret, to_outbound_wire(mail)))
}

/// The failure of a send that got no answer. `why` is what the HTTP layer says of it.
#[must_use]
pub fn send_unanswered(why: &str) -> SendError {
    SendError::new(0, format!("connector: {why}"), Some(true))
}

/// Reads a connector's answer to a send as the provider's id for the mail. A 2xx with an `id` is success.
///
/// # Errors
/// Anything else: `retryable` is what the connector states in its JSON answer, and otherwise follows the
/// status. A 2xx without an id can be tried again.
pub fn send_answer(status: u16, body: &str) -> Result<String, SendError> {
    let answer = Json::parse(body).unwrap_or(Json::Null);
    if is_ok(status) {
        return filled(answer.get("id"))
            .map(str::to_owned)
            .ok_or_else(|| SendError::new(502, "connector: the send answer carries no id", Some(true)));
    }
    let message = match filled(answer.get("error")) {
        Some(error) => error.to_owned(),
        None => format!("connector: send answered {status}"),
    };
    let retryable = match answer.get("retryable") {
        Some(Json::Bool(retryable)) => Some(*retryable),
        _ => None,
    };
    Err(SendError::new(status, message, retryable))
}

fn post_json(url: &str, secret: &str, body: String) -> HttpRequest {
    HttpRequest {
        method: "POST",
        url: url.to_owned(),
        headers: vec![
            ("Authorization", format!("Bearer {secret}")),
            ("Content-Type", "application/json".to_owned()),
        ],
        body: Some(body),
    }
}

// ---- A shipped connector's answers ------------------------------------------------------------------------

/// The endpoint of a shipped connector a request is for.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Route {
    /// `…/send`: the core's sends, authenticated by the connector secret.
    Send,
    /// `…/webhook`: the provider's webhook.
    Webhook,
}

/// What a shipped connector answers: a status and a JSON body.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Answer {
    /// The HTTP status.
    pub status: u16,
    /// The body, JSON.
    pub body: String,
}

#[derive(Serialize)]
struct ErrorBody<'a> {
    error: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    retryable: Option<bool>,
}

#[derive(Serialize)]
struct IgnoredBody {
    ignored: bool,
}

#[derive(Serialize)]
struct DroppedBody {
    dropped: u16,
}

#[derive(Serialize)]
struct SentBody<'a> {
    id: &'a str,
}

fn error(status: u16, message: &str) -> Answer {
    Answer {
        status,
        body: stringify(&ErrorBody {
            error: message,
            retryable: None,
        }),
    }
}

/// Names the endpoint a path is for: it holds `send` or `webhook` as a segment, after whatever prefix the
/// platform leaves on it.
#[must_use]
pub fn route_of(path: &str) -> Option<Route> {
    let request = Request { path, headers: &[] };
    let segments = request.segments();
    if segments.contains(&"send") {
        Some(Route::Send)
    } else if segments.contains(&"webhook") {
        Some(Route::Webhook)
    } else {
        None
    }
}

/// The endpoint a request is for.
///
/// # Errors
/// The answer to a request that is for none: 405 to any method but POST, 404 to any other path.
pub fn route(method: &str, path: &str) -> Result<Route, Answer> {
    if method != "POST" {
        return Err(error(405, "method not allowed"));
    }
    route_of(path).ok_or_else(|| error(404, "no such endpoint: POST …/webhook or …/send"))
}

/// The request that hands a received mail to the gateway core: the inbound shape, with the connector secret
/// as a bearer.
#[must_use]
pub fn forward_request(inbound_url: &str, secret: &str, mail: &InboundMail) -> HttpRequest {
    post_json(inbound_url, secret, to_inbound_wire(mail))
}

/// The answer to a webhook that is not authentic: the status the provider's rule chose.
#[must_use]
pub fn answer_rejected(rejected: &WebhookRejected) -> Answer {
    error(rejected.status, &rejected.message)
}

/// The answer to a webhook that is authentic and is not a received mail.
#[must_use]
pub fn answer_not_a_mail() -> Answer {
    Answer {
        status: 200,
        body: stringify(&IgnoredBody { ignored: true }),
    }
}

/// The answer to a webhook whose mail could not be fetched from the provider. The provider tries again.
#[must_use]
pub fn answer_unreadable() -> Answer {
    error(502, "the mail could not be read from the provider")
}

/// The answer to the provider once the mail was handed to the core. `core` is the core's status and body,
/// `None` when it did not answer. The core's 2xx passes through as a 200. A 401 or a 5xx, a wrong secret or
/// a core outage, becomes a 502, so that the provider tries again and the failure shows in its dashboard.
/// Any other status is a mail the core refused for good: it is dropped with a 200 and not tried again.
#[must_use]
pub fn answer_forwarded(core: Option<(u16, &str)>) -> Answer {
    match core {
        None => error(502, "the gateway core did not answer"),
        Some((status, body)) if is_ok(status) => Answer {
            status: 200,
            body: body.to_owned(),
        },
        Some((status, _)) if status == 401 || status >= 500 => {
            error(502, &format!("the gateway core answered {status}"))
        }
        Some((status, _)) => Answer {
            status: 200,
            body: stringify(&DroppedBody { dropped: status }),
        },
    }
}

/// Reads a request to the send endpoint as the mail to send.
///
/// # Errors
/// The answer to a request that is not one: 401 without the connector secret, 400 for a body that is not
/// the outbound shape.
pub fn read_send(request: &Request<'_>, secret: &str, raw_body: &str) -> Result<OutboundMail, Answer> {
    if !authorized(request, "send", secret) {
        return Err(error(401, "the request does not carry this connector's secret"));
    }
    from_outbound_wire(raw_body).map_err(|reason| error(400, &reason))
}

/// The answer to the core once the provider answered a send: the id, or the failure with its own status
/// (502 when that is no status of an error) and whether a later attempt can succeed.
#[must_use]
pub fn answer_sent(sent: &Result<String, SendError>) -> Answer {
    match sent {
        Ok(provider_id) => Answer {
            status: 200,
            body: stringify(&SentBody { id: provider_id }),
        },
        Err(failure) => Answer {
            status: if (400..=599).contains(&failure.status) {
                failure.status
            } else {
                502
            },
            body: stringify(&ErrorBody {
                error: &failure.message,
                retryable: Some(failure.retryable),
            }),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::{Request, basic_password_of, bearer_of, path_secret_of};
    use crate::js;

    fn with_authorization(value: &str, read: impl Fn(&Request<'_>) -> Option<String>) -> Option<String> {
        read(&Request {
            path: "/",
            headers: &[("Authorization", value)],
        })
    }

    #[test]
    fn a_scheme_reads_in_any_ascii_case_and_a_token_has_no_white_space() {
        let bearer = |value: &str| with_authorization(value, bearer_of);
        assert_eq!(bearer("bearer abc").as_deref(), Some("abc"));
        assert_eq!(bearer("BEARER\t\u{A0}abc \u{FEFF}").as_deref(), Some("abc"));
        assert_eq!(bearer("Bearer abc def"), None);
        assert_eq!(bearer("Bearerabc"), None);
        assert_eq!(bearer(" Bearer abc"), None);
        assert_eq!(bearer("Bearer "), None);
        assert_eq!(bearer("Basic abc"), None);
        // U+0085 is no white space to JavaScript, so it is part of the token.
        assert_eq!(bearer("Bearer a\u{85}b").as_deref(), Some("a\u{85}b"));
        assert_eq!(
            bearer_of(&Request {
                path: "/",
                headers: &[]
            }),
            None
        );
    }

    #[test]
    fn a_header_that_came_twice_is_its_values_joined() {
        let request = Request {
            path: "/",
            headers: &[("authorization", "Bearer one"), ("AUTHORIZATION", "Bearer two")],
        };
        assert_eq!(
            request.header("Authorization").as_deref(),
            Some("Bearer one, Bearer two")
        );
        assert_eq!(bearer_of(&request), None);
    }

    #[test]
    fn a_basic_password_is_what_follows_the_first_colon_one_character_a_byte() {
        let basic = |credentials: &[u8]| {
            with_authorization(&format!("Basic {}", js::btoa(credentials)), basic_password_of)
        };
        assert_eq!(basic(b"brigade:secret").as_deref(), Some("secret"));
        assert_eq!(basic(b":se:cret").as_deref(), Some("se:cret"));
        assert_eq!(basic(b"user:").as_deref(), Some(""));
        assert_eq!(basic(b"no colon"), None);
        // The UTF-8 of `é` is two bytes, and each is a character of the password.
        assert_eq!(basic("u:é".as_bytes()).as_deref(), Some("\u{C3}\u{A9}"));
        assert_eq!(basic(&[b'u', b':', 0xE9]).as_deref(), Some("é"));
        assert_eq!(with_authorization("Basic not-base64!", basic_password_of), None);
        // The padding may be missing.
        assert_eq!(
            with_authorization("basic dTpw", basic_password_of).as_deref(),
            Some("p")
        );
        assert_eq!(
            with_authorization("Basic dTpwYQ", basic_password_of).as_deref(),
            Some("pa")
        );
    }

    #[test]
    fn a_path_secret_follows_the_last_anchor_and_is_decoded() {
        let secret = |path: &str| path_secret_of(&Request { path, headers: &[] }, "mail-in");
        assert_eq!(secret("/functions/v1/mail-in/abc").as_deref(), Some("abc"));
        assert_eq!(secret("//mail-in//abc//").as_deref(), Some("abc"));
        assert_eq!(secret("/mail-in/x/mail-in/abc/def").as_deref(), Some("abc"));
        assert_eq!(secret("/mail-in/a%20b%2Fc").as_deref(), Some("a b/c"));
        assert_eq!(secret("/mail-in/%zz"), None);
        assert_eq!(secret("/mail-in/"), None);
        assert_eq!(secret("/mail-in"), None);
        assert_eq!(secret("/MAIL-IN/abc"), None);
        assert_eq!(secret("/mail-inx/abc"), None);
    }
}
