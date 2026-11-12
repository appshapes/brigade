//! The Standard Webhooks (Svix) signature check (`supabase/functions/_shared/svix.ts`).
//!
//! A signature is the HMAC-SHA256 of `<id>.<timestamp>.<body>` under the base64-decoded secret (its `whsec_`
//! prefix removed), written in base64 and compared in constant time against each `v1,<signature>` entry of
//! the space-separated signature header. Resend signs its webhooks this way.

use brigade_core::Timestamp;
use ring::hmac;

use crate::connector::{Request, constant_time_equal};
use crate::{js, time};

/// How far a webhook's timestamp may be from now, either way, in seconds.
pub const TOLERANCE_SECONDS: i64 = 300;

/// Whether a webhook carries a signature made with `secret` over its id, its timestamp and `body`, and its
/// timestamp is within `tolerance_seconds` of `now`. The three values are read from the `svix-` headers,
/// and from the `webhook-` headers where a `svix-` one is missing.
///
/// The comparison is of the two signatures as text, so a signature whose base64 is spelled another way is
/// not the signature. The timestamp is read as JavaScript's `Number` reads a string.
#[must_use]
pub fn verify(
    secret: &str,
    request: &Request<'_>,
    body: &str,
    now: Timestamp,
    tolerance_seconds: i64,
) -> bool {
    let header = |svix: &str, standard: &str| {
        request
            .header(svix)
            .or_else(|| request.header(standard))
            .filter(|value| !value.is_empty())
    };
    let (Some(id), Some(timestamp), Some(signatures)) = (
        header("svix-id", "webhook-id"),
        header("svix-timestamp", "webhook-timestamp"),
        header("svix-signature", "webhook-signature"),
    ) else {
        return false;
    };
    let at = js::number(&timestamp);
    // Both are whole seconds far below 2^53, where a double holds every integer.
    let (now, tolerance) = (time::now_seconds(now) as f64, tolerance_seconds as f64);
    if !at.is_finite() || (now - at).abs() > tolerance {
        return false;
    }
    let Some(expected) = signature(secret, &id, &timestamp, body) else {
        return false;
    };
    signatures
        .split(' ')
        .filter_map(|part| part.split_once(','))
        .any(|(version, signature)| version == "v1" && constant_time_equal(signature, &expected))
}

/// The header value a sender would write, for tests and for a provider that needs one. `None` when the
/// secret is not base64 or is empty, where the TypeScript throws.
#[must_use]
pub fn sign(secret: &str, id: &str, timestamp: &str, body: &str) -> Option<String> {
    signature(secret, id, timestamp, body).map(|signature| format!("v1,{signature}"))
}

/// The HMAC-SHA256 of `<id>.<timestamp>.<body>` under the secret, in base64.
fn signature(secret: &str, id: &str, timestamp: &str, body: &str) -> Option<String> {
    let key = js::atob(secret.strip_prefix("whsec_").unwrap_or(secret))?;
    // WebCrypto refuses to import a key of no bytes, so a secret that decodes to nothing signs nothing.
    if key.is_empty() {
        return None;
    }
    let key = hmac::Key::new(hmac::HMAC_SHA256, &key);
    let tag = hmac::sign(&key, format!("{id}.{timestamp}.{body}").as_bytes());
    Some(js::btoa(tag.as_ref()))
}

#[cfg(test)]
mod tests {
    use brigade_core::Timestamp;

    use super::{TOLERANCE_SECONDS, sign, verify};
    use crate::connector::Request;
    use crate::js;

    fn secret() -> String {
        format!("whsec_{}", js::btoa(b"0123456789abcdef0123456789abcdef"))
    }

    fn at(seconds: i64) -> Timestamp {
        Timestamp(seconds * 1_000_000_000)
    }

    /// The vector of the Standard Webhooks specification's reference libraries.
    #[test]
    fn the_signature_is_the_one_the_standard_makes() {
        let secret = "whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw";
        let body = r#"{"test": 2432232314}"#;
        assert_eq!(
            sign(secret, "msg_p5jXN8AQM9LWM0D4loKWxJek", "1614265330", body).as_deref(),
            Some("v1,g0hM9SsE+OTPJTGt/tmIKtSyZlE3uFJELVlNIOLJ1OE=")
        );
    }

    #[test]
    fn the_webhook_headers_stand_in_for_the_svix_ones() {
        let signature = sign(&secret(), "m", "1700000000", "{}").unwrap();
        let headers = [
            ("Webhook-Id", "m"),
            ("webhook-timestamp", "1700000000"),
            ("WEBHOOK-SIGNATURE", signature.as_str()),
        ];
        let request = Request {
            path: "/",
            headers: &headers,
        };
        assert!(verify(
            &secret(),
            &request,
            "{}",
            at(1_700_000_000),
            TOLERANCE_SECONDS
        ));
        // A `svix-` header that is there and empty is not replaced by the other.
        let headers = [
            ("svix-id", ""),
            ("webhook-id", "m"),
            ("webhook-timestamp", "1700000000"),
            ("webhook-signature", signature.as_str()),
        ];
        let request = Request {
            path: "/",
            headers: &headers,
        };
        assert!(!verify(
            &secret(),
            &request,
            "{}",
            at(1_700_000_000),
            TOLERANCE_SECONDS
        ));
    }

    #[test]
    fn the_timestamp_is_within_the_tolerance_either_way_and_is_a_number() {
        let verified = |timestamp: &str, now: i64| {
            let signature = sign(&secret(), "m", timestamp, "{}").unwrap();
            let headers = [
                ("svix-id", "m"),
                ("svix-timestamp", timestamp),
                ("svix-signature", signature.as_str()),
            ];
            let request = Request {
                path: "/",
                headers: &headers,
            };
            verify(&secret(), &request, "{}", at(now), TOLERANCE_SECONDS)
        };
        assert!(verified("1700000000", 1_700_000_300));
        assert!(!verified("1700000000", 1_700_000_301));
        assert!(verified("1700000000", 1_699_999_700));
        assert!(!verified("1700000000", 1_699_999_699));
        assert!(verified("1.7e9", 1_700_000_000));
        assert!(verified(" 1700000000 ", 1_700_000_000));
        assert!(!verified("1700000000s", 1_700_000_000));
        assert!(!verified("Infinity", 1_700_000_000));
    }

    #[test]
    fn a_signature_is_compared_as_text_and_only_under_v1() {
        let signature = sign(&secret(), "m", "1700000000", "{}").unwrap();
        let verified = |signatures: &str| {
            let headers = [
                ("svix-id", "m"),
                ("svix-timestamp", "1700000000"),
                ("svix-signature", signatures),
            ];
            let request = Request {
                path: "/",
                headers: &headers,
            };
            verify(&secret(), &request, "{}", at(1_700_000_000), TOLERANCE_SECONDS)
        };
        assert!(verified(&signature));
        assert!(verified(&format!("v1,wrong  nocomma {signature}")));
        assert!(!verified(&signature.replace("v1,", "v2,")));
        assert!(!verified(signature.trim_end_matches('=')));
        assert!(!verified(&format!("{signature},")));
        assert!(!verified(&format!("\t{signature}")));
    }

    #[test]
    fn a_secret_that_is_no_key_signs_nothing() {
        assert_eq!(sign("whsec_", "m", "1", "{}"), None);
        assert_eq!(sign("whsec_not base64!", "m", "1", "{}"), None);
        assert!(sign("MDEyMw==", "m", "1", "{}").is_some());
    }
}
