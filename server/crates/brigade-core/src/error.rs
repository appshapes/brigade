//! The failures a server answers, and the envelope every answer travels in.
//!
//! Every message here is fixed text. An error message reaches a model (BAP/1 4.3), and a value in a hostile
//! document is the attacker's, so nothing a caller supplied is ever echoed: not a member's value, not an id
//! and above all not a join secret or a credential.

use std::collections::BTreeMap;

use serde_json::{Map, Value, json};

/// The protocol major this server speaks.
pub const PROTOCOL_VERSION: &str = "1";

/// The error codes a server answers. `usage`, `config` and `protocol_mismatch` are the adapter's own
/// (the contract's section 1), so they are not here.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Code {
    /// The request fails its shape, or a member is over its cap.
    InvalidInput,
    /// The request carries no credential this server knows.
    Unauthenticated,
    /// The caller is not an active member of the team, or the join secret was rejected.
    Unauthorized,
    /// An unknown, foreign or not-owned session or message id, or an unknown path.
    NotFound,
    /// The request disagrees with the state: a closed session, a live resume, a reused idempotency key.
    Conflict,
    /// The reply chain reached the hop-count cap.
    LoopDetected,
    /// A send budget, an inbox cap or a principal's bound is exhausted: its teams created or its sessions
    /// opened in an hour, or the joins it failed.
    RateLimited,
    /// A failure of the server itself.
    Internal,
    /// The server cannot answer now.
    Unavailable,
}

impl Code {
    /// The code as the wire spells it.
    #[must_use]
    pub fn as_str(self) -> &'static str {
        match self {
            Self::InvalidInput => "invalid_input",
            Self::Unauthenticated => "unauthenticated",
            Self::Unauthorized => "unauthorized",
            Self::NotFound => "not_found",
            Self::Conflict => "conflict",
            Self::LoopDetected => "loop_detected",
            Self::RateLimited => "rate_limited",
            Self::Internal => "internal",
            Self::Unavailable => "unavailable",
        }
    }

    /// Whether a retry of the same request can succeed (BAP/1 4.6).
    #[must_use]
    pub fn retryable(self) -> bool {
        matches!(self, Self::RateLimited | Self::Unavailable)
    }

    /// The HTTP status the contract's section 1 pairs with the code.
    #[must_use]
    pub fn http_status(self) -> u16 {
        match self {
            Self::InvalidInput => 400,
            Self::Unauthenticated => 401,
            Self::Unauthorized => 403,
            Self::NotFound => 404,
            Self::Conflict => 409,
            Self::LoopDetected => 422,
            Self::RateLimited => 429,
            Self::Internal => 500,
            Self::Unavailable => 503,
        }
    }
}

/// One failure: a code, a short message that is safe to show a model, and machine-readable details.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Error {
    /// The code, which also selects the HTTP status.
    pub code: Code,
    /// Short, fixed text.
    pub message: String,
    /// How long to wait before a retry; present on `rate_limited`.
    pub retry_after_ms: Option<u64>,
    /// Machine-readable detail: `field`, `reason` and their companions (BAP/1 4.3.1).
    pub details: BTreeMap<&'static str, String>,
}

impl Error {
    fn new(code: Code, message: impl Into<String>) -> Self {
        Self {
            code,
            message: message.into(),
            retry_after_ms: None,
            details: BTreeMap::new(),
        }
    }

    fn with(mut self, key: &'static str, value: impl Into<String>) -> Self {
        self.details.insert(key, value.into());
        self
    }

    /// The uniform `not_found` of BAP/1 4.5.6 and 4.5.7. It carries no details, so the answer for an unknown
    /// id, a foreign team's id and a not-owned id is the same bytes and nothing tells them apart.
    #[must_use]
    pub fn not_found() -> Self {
        Self::new(Code::NotFound, "session or message not found")
    }

    /// An unknown path (the contract's section 1).
    #[must_use]
    pub fn no_such_endpoint() -> Self {
        Self::new(Code::NotFound, "no such endpoint")
    }

    /// The uniform `unauthorized` of BAP/1 4.5.7: a team that does not exist, a membership that never was
    /// and one that was revoked are the same bytes. An administration request is refused with it too, so a
    /// member that is not the team's owner reads the same bytes as a stranger (the contract's section 3a).
    #[must_use]
    pub fn not_member() -> Self {
        Self::new(Code::Unauthorized, "not an active member of this team")
    }

    /// The uniform `unauthorized` of `team join`: a wrong secret, a secret that was rotated away, an unknown
    /// team and a banned principal are the same bytes.
    #[must_use]
    pub fn secret_rejected() -> Self {
        Self::new(Code::Unauthorized, "join secret rejected")
    }

    /// A team creation this server does not allow the caller (the contract's section 3).
    #[must_use]
    pub fn create_refused() -> Self {
        Self::new(
            Code::Unauthorized,
            "this server does not let this caller create a team",
        )
    }

    /// A request with no credential this server knows.
    #[must_use]
    pub fn unknown_credential() -> Self {
        Self::new(
            Code::Unauthenticated,
            "the credential is not known to this server",
        )
    }

    /// A conflict with its reason token (BAP/1 4.6).
    #[must_use]
    pub fn conflict(message: &str, reason: &str) -> Self {
        Self::new(Code::Conflict, message).with("reason", reason)
    }

    /// A send budget, an inbox cap or a principal's bound that is exhausted, with the reason token of BAP/1
    /// 4.6 and a wait that is always above zero.
    #[must_use]
    pub fn rate_limited(reason: &'static str, retry_after_ms: u64) -> Self {
        let send_limit = |subject: &str| format!("the send rate limit for this {subject} is exhausted");
        let message = match reason {
            "team_create" => "this principal has created as many teams as one may in an hour".to_owned(),
            "register_session" => {
                "this principal has opened as many sessions as one may in an hour".to_owned()
            }
            "join_attempts" => {
                "this principal has failed too many joins, and its next is not heard yet".to_owned()
            }
            "principal_per_minute" | "principal_per_hour" => send_limit("principal"),
            "sender_quota_for_recipient" | "recipient_inbox_full" => send_limit("recipient inbox"),
            _ => send_limit("session"),
        };
        let mut error = Self::new(Code::RateLimited, message).with("reason", reason);
        error.retry_after_ms = Some(retry_after_ms.max(1));
        error
    }

    /// A reply chain at the hop-count cap.
    #[must_use]
    pub fn loop_detected() -> Self {
        Self::new(
            Code::LoopDetected,
            "the reply chain has reached the hop-count cap",
        )
        .with("reason", "max_hop_count")
    }

    /// A failure of the server itself.
    #[must_use]
    pub fn internal(message: &str) -> Self {
        Self::new(Code::Internal, message)
    }

    /// A request the server cannot answer now.
    #[must_use]
    pub fn unavailable(message: &str) -> Self {
        Self::new(Code::Unavailable, message)
    }

    /// A body that is not one JSON object. No member can be named, so there is no `field`.
    #[must_use]
    pub fn malformed_json() -> Self {
        Self::new(Code::InvalidInput, "the request body is not one JSON object")
            .with("reason", "malformed_json")
    }

    /// A request that is not a `POST` (the contract's section 1).
    #[must_use]
    pub fn not_a_post() -> Self {
        Self::new(Code::InvalidInput, "every request of this contract is a POST")
            .with("field", "method")
            .with("reason", "invalid_value")
    }

    /// A body over the contract's cap.
    #[must_use]
    pub fn body_too_long() -> Self {
        Self::new(Code::InvalidInput, "the request body exceeds 1 MiB")
            .with("field", "body")
            .with("reason", "too_long")
    }

    /// A missing or empty required member.
    #[must_use]
    pub fn required(field: &str) -> Self {
        Self::new(
            Code::InvalidInput,
            format!("required member {field} is missing or empty"),
        )
        .with("field", field)
        .with("reason", "required")
    }

    /// A member over its cap. `unit` is `bytes` or `codepoints`: the two caps measure different things.
    #[must_use]
    pub fn too_long(field: &str, limit: usize, actual: usize, unit: &str) -> Self {
        Self::new(
            Code::InvalidInput,
            format!("{field} exceeds the protocol cap of {limit} {unit}"),
        )
        .with("field", field)
        .with("reason", "too_long")
        .with("limit", limit.to_string())
        .with("actual", actual.to_string())
        .with("unit", unit)
    }

    /// A member outside its fixed values. `allowed` is the specification's own list, so it is safe to print.
    #[must_use]
    pub fn invalid_value(field: &str, allowed: &str) -> Self {
        Self::new(Code::InvalidInput, format!("{field} must be one of: {allowed}"))
            .with("field", field)
            .with("reason", "invalid_value")
            .with("allowed", allowed)
    }

    /// A member of the wrong JSON type. `kind` names the type it must be.
    #[must_use]
    pub fn wrong_type(field: &str, kind: &str) -> Self {
        Self::new(Code::InvalidInput, format!("{field} must be {kind}"))
            .with("field", field)
            .with("reason", "invalid_value")
    }

    /// A member the server stamps and a caller must not supply (BAP/1 4.4.6).
    #[must_use]
    pub fn forbidden_member(field: &str) -> Self {
        Self::new(
            Code::InvalidInput,
            format!("{field} is stamped by the adapter and must not be supplied by the caller"),
        )
        .with("field", field)
        .with("reason", "forbidden_member")
    }

    /// A number outside its bounds.
    #[must_use]
    pub fn out_of_range(field: &str, minimum: i64, maximum: i64) -> Self {
        Self::new(
            Code::InvalidInput,
            format!("{field} must be between {minimum} and {maximum}"),
        )
        .with("field", field)
        .with("reason", "out_of_range")
        .with("min", minimum.to_string())
        .with("max", maximum.to_string())
    }

    /// A member whose value a rule refuses although its type is right: the owner naming itself, a principal
    /// that is not an active member of the team, one form of a request given beside the other. `must_be`
    /// is the server's own fixed text, so nothing a caller sent is in the answer.
    #[must_use]
    pub fn refused_value(field: &str, must_be: &str) -> Self {
        Self::new(Code::InvalidInput, format!("{field} must be {must_be}"))
            .with("field", field)
            .with("reason", "invalid_value")
    }

    /// A team the operator names on the command line that the server does not hold. No request is ever told
    /// this: a request for a team that does not exist is told the uniform `unauthorized`.
    #[must_use]
    pub fn no_such_team() -> Self {
        Self::new(Code::NotFound, "the server holds no team of that reference")
    }

    /// A join secret that is not `brg1.<team_ref>.<secret>`. No part of the input is in the answer.
    #[must_use]
    pub fn malformed_join_secret() -> Self {
        Self::new(
            Code::InvalidInput,
            "join_secret is malformed; expected brg1.<team_ref>.<secret>",
        )
        .with("field", "join_secret")
        .with("reason", "invalid_value")
    }

    /// The failure as the envelope's `error` member.
    #[must_use]
    pub fn object(&self) -> Value {
        let mut object = Map::new();
        object.insert("code".into(), json!(self.code.as_str()));
        object.insert("message".into(), json!(self.message));
        object.insert("retryable".into(), json!(self.code.retryable()));
        if let Some(wait) = self.retry_after_ms {
            object.insert("retry_after_ms".into(), json!(wait));
        }
        if !self.details.is_empty() {
            object.insert("details".into(), json!(self.details));
        }
        Value::Object(object)
    }
}

impl std::fmt::Display for Error {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        write!(formatter, "{}: {}", self.code.as_str(), self.message)
    }
}

impl std::error::Error for Error {}

/// The envelope of a result (BAP/1 4.3).
#[must_use]
pub fn envelope_ok(result: Value) -> Value {
    json!({"ok": true, "protocol_version": PROTOCOL_VERSION, "result": result})
}

/// The envelope of a failure (BAP/1 4.3).
#[must_use]
pub fn envelope_error(error: &Error) -> Value {
    json!({"ok": false, "protocol_version": PROTOCOL_VERSION, "error": error.object()})
}

#[cfg(test)]
mod tests {
    use super::{Code, Error, envelope_error, envelope_ok};
    use serde_json::json;

    #[test]
    fn only_two_codes_are_retryable() {
        for code in [
            Code::InvalidInput,
            Code::Unauthenticated,
            Code::Unauthorized,
            Code::NotFound,
            Code::Conflict,
            Code::LoopDetected,
            Code::Internal,
        ] {
            assert!(!code.retryable(), "{}", code.as_str());
        }
        assert!(Code::RateLimited.retryable());
        assert!(Code::Unavailable.retryable());
    }

    #[test]
    fn the_uniform_refusals_carry_no_details() {
        assert_eq!(
            envelope_error(&Error::not_found()),
            json!({"ok": false, "protocol_version": "1", "error": {
                "code": "not_found", "message": "session or message not found", "retryable": false}})
        );
        assert!(Error::not_member().details.is_empty());
        assert!(Error::secret_rejected().details.is_empty());
    }

    #[test]
    fn a_rate_limit_always_names_a_wait_above_zero() {
        let error = Error::rate_limited("send_per_minute", 0);
        assert_eq!(error.retry_after_ms, Some(1));
        let object = error.object();
        assert_eq!(object["retryable"], json!(true));
        assert_eq!(object["retry_after_ms"], json!(1));
        assert_eq!(object["details"]["reason"], json!("send_per_minute"));
    }

    #[test]
    fn a_cap_violation_names_its_unit() {
        let error = Error::too_long("body", 16_384, 16_385, "bytes");
        assert_eq!(error.code.http_status(), 400);
        assert_eq!(error.details["unit"], "bytes");
        assert_eq!(error.details["limit"], "16384");
        assert_eq!(error.details["actual"], "16385");
    }

    #[test]
    fn a_result_travels_in_an_ok_envelope() {
        assert_eq!(
            envelope_ok(json!({"revoked": true})),
            json!({"ok": true, "protocol_version": "1", "result": {"revoked": true}})
        );
    }
}
