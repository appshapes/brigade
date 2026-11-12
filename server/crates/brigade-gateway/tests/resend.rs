//! The Resend provider (`supabase/functions/_shared/providers/resend.ts`). The TypeScript has no test file
//! for it; these are the rules that file states, read off its source.

use brigade_core::Timestamp;
use brigade_gateway::providers::Resend;
use brigade_gateway::webhook_signature::sign;
use brigade_gateway::{OutboundMail, Provider, Request};
use serde_json::json;

/// `"whsec_" + btoa("0123456789abcdef0123456789abcdef")`.
const SECRET: &str = "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=";
const HOOK: &str = "/functions/v1/mail-connector-resend/webhook";

/// 2026-10-04T12:00:00.000Z.
const NOW: Timestamp = Timestamp(1_791_115_200_000_000_000);
const NOW_SECONDS: &str = "1791115200";

fn provider() -> Resend {
    Resend::new("re_key", Some(SECRET.to_owned()))
}

/// The provider's reading of a webhook signed with `secret` at `NOW`.
fn signed(provider: &Resend, secret: &str, body: &str) -> Result<Option<String>, (u16, String)> {
    let signature = sign(secret, "msg_1", NOW_SECONDS, body).unwrap();
    let headers = [
        ("svix-id", "msg_1"),
        ("svix-timestamp", NOW_SECONDS),
        ("svix-signature", signature.as_str()),
    ];
    let request = Request {
        path: HOOK,
        headers: &headers,
    };
    match provider.webhook(&request, body, NOW) {
        Ok(hook) => Ok(hook.map(|hook| {
            assert_eq!(hook.mail, None, "Resend's webhook carries only the id");
            hook.provider_id
        })),
        Err(rejected) => Err((rejected.status, rejected.message)),
    }
}

#[test]
fn a_signed_received_event_is_carried_by_its_id_and_every_other_event_is_not_a_mail() {
    let provider = provider();
    let carried = |event: serde_json::Value| signed(&provider, SECRET, &event.to_string());
    assert_eq!(
        carried(json!({ "type": "email.received", "data": { "email_id": "re_1" } })),
        Ok(Some("re_1".to_owned()))
    );
    for not_a_mail in [
        json!({ "type": "email.sent", "data": { "email_id": "re_1" } }),
        json!({ "type": "email.bounced", "data": { "email_id": "re_1" } }),
        json!({ "type": "email.received" }),
        json!({ "type": "email.received", "data": null }),
        json!({ "type": "email.received", "data": {} }),
        json!({ "type": "email.received", "data": { "email_id": "" } }),
        json!({ "type": "email.received", "data": { "email_id": 5 } }),
        json!({ "type": "email.received", "data": "re_1" }),
        json!({ "data": { "email_id": "re_1" } }),
        json!(["email.received"]),
        json!("email.received"),
        json!(null),
    ] {
        assert_eq!(carried(not_a_mail.clone()), Ok(None), "{not_a_mail}");
    }
}

#[test]
fn a_webhook_that_is_not_authentic_is_rejected_with_401() {
    let provider = provider();
    let event = r#"{"type":"email.received","data":{"email_id":"re_1"}}"#;
    let other = "whsec_YW5vdGhlciBzZWNyZXQgdmFsdWUgaGVyZSAxMjM0";
    assert_eq!(
        signed(&provider, other, event),
        Err((401, "the webhook signature does not verify".to_owned()))
    );
    let unsigned = Request {
        path: HOOK,
        headers: &[],
    };
    assert_eq!(
        provider.webhook(&unsigned, event, NOW).unwrap_err().message,
        "the webhook signature does not verify"
    );
    for unset in [None, Some(String::new())] {
        let provider = Resend::new("re_key", unset);
        assert_eq!(
            signed(&provider, SECRET, event),
            Err((401, "the webhook signing secret is not configured".to_owned()))
        );
    }
    // The signature is checked before the body is read.
    assert_eq!(
        signed(&provider, SECRET, "not json"),
        Err((401, "the webhook body is not JSON".to_owned()))
    );
    assert_eq!(
        signed(&provider, other, "not json"),
        Err((401, "the webhook signature does not verify".to_owned()))
    );
}

#[test]
fn a_webhook_signed_too_long_ago_is_rejected() {
    let provider = provider();
    let event = r#"{"type":"email.received","data":{"email_id":"re_1"}}"#;
    let signature = sign(SECRET, "msg_1", "1791114899", event).unwrap();
    let headers = [
        ("svix-id", "msg_1"),
        ("svix-timestamp", "1791114899"),
        ("svix-signature", signature.as_str()),
    ];
    let request = Request {
        path: HOOK,
        headers: &headers,
    };
    assert_eq!(provider.webhook(&request, event, NOW).unwrap_err().status, 401);
}

#[test]
fn the_fetch_is_one_get_with_the_key_and_its_answer_is_the_mail() {
    let provider = provider();
    let request = provider.fetch_request("re 1/x");
    assert_eq!(request.method, "GET");
    assert_eq!(request.url, "https://api.resend.com/emails/receiving/re%201%2Fx");
    assert_eq!(request.headers, [("Authorization", "Bearer re_key".to_owned())]);
    assert_eq!(request.body, None);

    let answer = json!({
        "id": "re_1",
        "from": "Alice Example <Alice@Example.com>",
        "to": ["Brigade <A1B2C3D4@example.resend.app>", "other@example.com"],
        "received_for": ["a1b2c3d4@example.resend.app", "a1b2c3d4+tag@example.resend.app"],
        "subject": "Login page broken",
        "message_id": "<abc@mail.example.com>",
        "headers": {
            "in-reply-to": "<m0@x>",
            "References": "<m0@x> <m1@x>",
            "message-id": "<from-the-headers@x>",
        },
        "text": "Steps here.",
        "html": "<p>ignored</p>",
        "created_at": "2026-10-03 16:00:00.000000+00",
    });
    let mail = provider.fetched("re_1", 200, &answer.to_string(), NOW).unwrap();
    assert_eq!(mail.provider_id, "re_1");
    assert_eq!(mail.from, "alice@example.com");
    assert_eq!(mail.from_name.as_deref(), Some("Alice Example"));
    assert_eq!(
        mail.to,
        [
            "a1b2c3d4@example.resend.app",
            "other@example.com",
            "a1b2c3d4+tag@example.resend.app"
        ]
    );
    assert_eq!(mail.subject, "Login page broken");
    assert_eq!(mail.message_id.as_deref(), Some("<abc@mail.example.com>"));
    assert_eq!(mail.in_reply_to.as_deref(), Some("<m0@x>"));
    assert_eq!(mail.references, ["<m0@x>", "<m1@x>"]);
    assert_eq!(mail.text, "Steps here.");
    assert_eq!(mail.received_at, "2026-10-03 16:00:00.000000+00");
}

#[test]
fn a_fetched_mail_falls_back_to_its_headers_its_html_and_now() {
    let provider = provider();
    let answer = json!({
        "from": "bob@example.com",
        "to": "a1b2c3d4@example.resend.app",
        "headers": { "Message-ID": ["<list@x>", "<second@x>"], "references": 5 },
        "text": "  \n",
        "html": "<div>Hello<br>there</div>",
    });
    let mail = provider.fetched("re_2", 200, &answer.to_string(), NOW).unwrap();
    assert_eq!(mail.from_name, None);
    assert_eq!(mail.to, ["a1b2c3d4@example.resend.app"]);
    assert_eq!(mail.subject, "");
    assert_eq!(mail.message_id.as_deref(), Some("<list@x>"));
    assert_eq!(mail.in_reply_to, None);
    assert_eq!(mail.references, Vec::<String>::new());
    assert_eq!(mail.text, "Hello\nthere");
    assert_eq!(mail.received_at, "2026-10-04T12:00:00.000Z");
    // Nothing at all is still a mail, from nobody to nobody.
    let empty = provider.fetched("re_3", 200, "{}", NOW).unwrap();
    assert_eq!(
        (empty.from.as_str(), empty.to.len(), empty.text.as_str()),
        ("", 0, "")
    );
    assert_eq!(
        provider.fetched("re_1", 404, "{}", NOW),
        Err("resend: retrieve received email answered 404".to_owned())
    );
    assert!(provider.fetched("re_1", 200, "<html>", NOW).is_err());
}

fn outbound() -> OutboundMail {
    OutboundMail {
        from: "Brigade <brigade@example.com>".to_owned(),
        to: "alice@example.com".to_owned(),
        reply_to: Some("a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app".to_owned()),
        subject: "[brigade/team] hello".to_owned(),
        text: "hello".to_owned(),
        headers: vec![
            ("Auto-Submitted".to_owned(), "auto-generated".to_owned()),
            ("In-Reply-To".to_owned(), "<x@example.com>".to_owned()),
        ],
    }
}

#[test]
fn the_send_is_one_post_of_resends_shape_with_the_key() {
    let provider = provider();
    let request = provider.send_request(&outbound());
    assert_eq!(request.method, "POST");
    assert_eq!(request.url, "https://api.resend.com/emails");
    assert_eq!(
        request.headers,
        [
            ("Authorization", "Bearer re_key".to_owned()),
            ("Content-Type", "application/json".to_owned())
        ]
    );
    assert_eq!(
        request.body.as_deref(),
        Some(
            r#"{"from":"Brigade <brigade@example.com>","to":["alice@example.com"],"subject":"[brigade/team] hello","text":"hello","headers":{"Auto-Submitted":"auto-generated","In-Reply-To":"<x@example.com>"},"reply_to":["a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app"]}"#
        )
    );
    for no_reply_to in [None, Some(String::new())] {
        let bare = OutboundMail {
            reply_to: no_reply_to,
            headers: Vec::new(),
            ..outbound()
        };
        assert_eq!(
            provider.send_request(&bare).body.as_deref(),
            Some(
                r#"{"from":"Brigade <brigade@example.com>","to":["alice@example.com"],"subject":"[brigade/team] hello","text":"hello","headers":{}}"#
            )
        );
    }
    let stand_in = Resend {
        base_url: "http://127.0.0.1:9".to_owned(),
        ..provider
    };
    assert_eq!(
        stand_in.send_request(&outbound()).url,
        "http://127.0.0.1:9/emails"
    );
}

#[test]
fn a_send_answer_is_an_id_or_an_error_that_follows_the_status() {
    let provider = provider();
    assert_eq!(provider.sent(200, r#"{"id":"re_9"}"#), Ok("re_9".to_owned()));
    let failure = |status: u16, body: &str| {
        let error = provider.sent(status, body).unwrap_err();
        (error.status, error.message, error.retryable)
    };
    assert_eq!(
        failure(
            422,
            r#"{"statusCode":422,"name":"validation_error","message":"Invalid `to` field."}"#
        ),
        (
            422,
            "resend: send answered 422: Invalid `to` field.".to_owned(),
            false
        )
    );
    assert_eq!(
        failure(403, r#"{"name":"invalid_api_key"}"#),
        (
            403,
            "resend: send answered 403: invalid_api_key".to_owned(),
            false
        )
    );
    assert_eq!(
        failure(429, r#"{"message":null,"name":"rate_limit_exceeded"}"#),
        (
            429,
            "resend: send answered 429: rate_limit_exceeded".to_owned(),
            true
        )
    );
    assert_eq!(
        failure(500, ""),
        (500, "resend: send answered 500".to_owned(), true)
    );
    assert_eq!(
        failure(503, "<html>"),
        (503, "resend: send answered 503".to_owned(), true)
    );
    assert_eq!(
        failure(400, r#"{"message":""}"#),
        (400, "resend: send answered 400".to_owned(), false)
    );
    assert_eq!(
        failure(400, "null"),
        (400, "resend: send answered 400".to_owned(), false)
    );
    // An accepted send that names no id has the id the TypeScript's `String(undefined)` gave it.
    assert_eq!(provider.sent(200, "{}"), Ok("undefined".to_owned()));
    assert_eq!(provider.sent(200, r#"{"id":null}"#), Ok("null".to_owned()));
    assert_eq!(provider.sent(200, r#"{"id":7}"#), Ok("7".to_owned()));
    // One whose answer cannot be read at all is a failure that can be tried again.
    for unreadable in ["", "<html>", "null"] {
        assert_eq!(
            failure(200, unreadable),
            (500, "resend: the send answer could not be read".to_owned(), true),
            "{unreadable}"
        );
    }
}
