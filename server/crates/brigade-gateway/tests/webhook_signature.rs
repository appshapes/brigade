//! `supabase/functions/_shared/svix_test.ts`, case for case: the same inputs and the same expected outputs.

use brigade_core::Timestamp;
use brigade_gateway::Request;
use brigade_gateway::webhook_signature::{TOLERANCE_SECONDS, sign, verify};

/// `"whsec_" + btoa("0123456789abcdef0123456789abcdef")`.
const SECRET: &str = "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=";
/// `"whsec_" + btoa("another secret value here 1234")`.
const ANOTHER_SECRET: &str = "whsec_YW5vdGhlciBzZWNyZXQgdmFsdWUgaGVyZSAxMjM0";

fn at(seconds: i64) -> Timestamp {
    Timestamp(seconds * 1_000_000_000)
}

#[test]
fn a_signature_made_with_the_secret_verifies_a_tampered_body_does_not() {
    let body = r#"{"type":"email.received"}"#;
    let timestamp = "1700000000";
    let signature = sign(SECRET, "msg_1", timestamp, body).unwrap();
    let signatures = format!("v2,junk {signature}");
    let headers = [
        ("svix-id", "msg_1"),
        ("svix-timestamp", timestamp),
        ("svix-signature", signatures.as_str()),
    ];
    let request = Request {
        path: "/",
        headers: &headers,
    };
    assert!(verify(
        SECRET,
        &request,
        body,
        at(1_700_000_010),
        TOLERANCE_SECONDS
    ));
    assert!(!verify(
        SECRET,
        &request,
        &format!("{body} "),
        at(1_700_000_010),
        TOLERANCE_SECONDS
    ));
    assert!(!verify(
        ANOTHER_SECRET,
        &request,
        body,
        at(1_700_000_010),
        TOLERANCE_SECONDS
    ));
}

#[test]
fn a_stale_timestamp_or_a_missing_header_fails() {
    let body = "{}";
    let signature = sign(SECRET, "m", "1700000000", body).unwrap();
    let headers = [
        ("svix-id", "m"),
        ("svix-timestamp", "1700000000"),
        ("svix-signature", signature.as_str()),
    ];
    let request = Request {
        path: "/",
        headers: &headers,
    };
    assert!(!verify(
        SECRET,
        &request,
        body,
        at(1_700_001_000),
        TOLERANCE_SECONDS
    ));
    let only_id = Request {
        path: "/",
        headers: &[("svix-id", "m")],
    };
    assert!(!verify(
        SECRET,
        &only_id,
        body,
        at(1_700_000_000),
        TOLERANCE_SECONDS
    ));
}
