//! `supabase/functions/_shared/connector_test.ts`, case for case: the same inputs and the same expected
//! outputs.
//!
//! The TypeScript's last four cases drive the connector's HTTP handler with a stand-in provider and a
//! stand-in `fetch`. Here each is a test of the decisions that handler makes between its HTTP exchanges:
//! the request it would send and the answer it would give for what came back.

use brigade_core::Timestamp;
use brigade_gateway::connector::{
    Route, answer_forwarded, answer_not_a_mail, answer_rejected, answer_sent, answer_unreadable,
    basic_password_of, bearer_of, forward_request, path_secret_of, read_send, route, route_of, send_answer,
    send_request, send_unanswered,
};
use brigade_gateway::providers::Resend;
use brigade_gateway::webhook_signature::sign;
use brigade_gateway::{
    Answer, InboundMail, OutboundMail, Provider, Request, SendError, WebhookRejected, authorized,
    from_inbound_wire, from_outbound_wire, to_inbound_wire, to_outbound_wire,
};
use serde_json::{Value, json};

const SECRET: &str = "0123456789abcdef0123456789abcdef0123456789abcdef";
const CORE: &str = "https://example.supabase.co/functions/v1/mail-in";
const CORE_PATH: &str = "/functions/v1/mail-in";
const CONNECTOR_PATH: &str = "/functions/v1/mail-connector-fake";
/// `btoa("brigade:" + SECRET)`.
const BASIC: &str = "Basic YnJpZ2FkZTowMTIzNDU2Nzg5YWJjZGVmMDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=";

/// 2026-10-04T12:00:00.000Z.
const NOW: Timestamp = Timestamp(1_791_115_200_000_000_000);

fn inbound() -> InboundMail {
    InboundMail {
        provider_id: "pm-1".to_owned(),
        from: "alice@example.com".to_owned(),
        from_name: Some("Alice".to_owned()),
        to: vec!["a1b2c3d4+0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f@example.resend.app".to_owned()],
        subject: "Re: hello".to_owned(),
        message_id: Some("<m1@example.com>".to_owned()),
        in_reply_to: Some("<m0@example.com>".to_owned()),
        references: vec!["<m0@example.com>".to_owned()],
        text: "yes please".to_owned(),
        received_at: "2026-10-04T12:00:00.000Z".to_owned(),
    }
}

fn outbound() -> OutboundMail {
    OutboundMail {
        from: "Brigade <brigade@example.com>".to_owned(),
        to: "alice@example.com".to_owned(),
        reply_to: Some("a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app".to_owned()),
        subject: "[brigade/team] hello".to_owned(),
        text: "hello".to_owned(),
        headers: vec![("Auto-Submitted".to_owned(), "auto-generated".to_owned())],
    }
}

/// A shape with some members replaced, as the TypeScript's `{ ...wire, member: value }`.
fn with(wire: &str, members: Value) -> String {
    let mut wire: Value = serde_json::from_str(wire).unwrap();
    for (name, value) in members.as_object().unwrap() {
        wire[name] = value.clone();
    }
    wire.to_string()
}

fn request<'a>(path: &'a str, headers: &'a [(&'a str, &'a str)]) -> Request<'a> {
    Request { path, headers }
}

fn value(json: &str) -> Value {
    serde_json::from_str(json).unwrap()
}

#[test]
fn the_inbound_shape_round_trips_and_is_validated() {
    let wire = to_inbound_wire(&inbound());
    assert_eq!(from_inbound_wire(&wire, NOW), Ok(inbound()));
    let bad = |body: &str| {
        from_inbound_wire(body, NOW)
            .err()
            .unwrap_or_else(|| "accepted".to_owned())
    };
    assert!(bad("nope").contains("not JSON"));
    assert!(bad("[]").contains("not a JSON object"));
    assert!(bad(&with(&wire, json!({ "brigade_mail": 2 }))).contains("brigade_mail must be 1"));
    assert!(bad(&with(&wire, json!({ "id": "" }))).contains("id is required"));
    assert!(bad(&with(&wire, json!({ "from": "alice" }))).contains("from must be an email address"));
    assert!(bad(&with(&wire, json!({ "to": "x@example.com" }))).contains("to must be a list"));
    assert!(bad(&with(&wire, json!({ "text": 5 }))).contains("text must be a string"));
    let minimal = from_inbound_wire(
        &json!({
            "brigade_mail": 1,
            "id": "x",
            "from": "ALICE@Example.com",
            "to": ["B@Example.COM ", "b@example.com"],
        })
        .to_string(),
        NOW,
    )
    .unwrap();
    assert_eq!(minimal.from, "alice@example.com");
    assert_eq!(minimal.to, ["b@example.com"]);
    assert_eq!(minimal.subject, "");
    assert_eq!(minimal.text, "");
    assert_eq!(minimal.references, Vec::<String>::new());
    assert_eq!(minimal.from_name, None);
    // The TypeScript stamps a mail that states no time of receipt with the clock; here the time is handed in.
    assert_eq!(minimal.received_at, "2026-10-04T12:00:00.000Z");
}

#[test]
fn the_outbound_shape_round_trips_and_is_validated() {
    let wire = to_outbound_wire(&outbound());
    assert_eq!(from_outbound_wire(&wire), Ok(outbound()));
    let bad = |body: &str| {
        from_outbound_wire(body)
            .err()
            .unwrap_or_else(|| "accepted".to_owned())
    };
    assert!(bad(&with(&wire, json!({ "to": "nobody" }))).contains("to must be one email address"));
    assert!(bad(&with(&wire, json!({ "from": "" }))).contains("from is required"));
    assert!(bad(&with(&wire, json!({ "headers": { "a": 1 } }))).contains("header a must be a string"));
    assert!(bad(&with(&wire, json!({ "subject": null }))).contains("subject must be a string"));
    // `headers: undefined` is a member `JSON.stringify` leaves out.
    let mut no_headers = value(&wire);
    no_headers.as_object_mut().unwrap().remove("headers");
    assert_eq!(
        from_outbound_wire(&no_headers.to_string()).map(|mail| mail.headers),
        Ok(Vec::new())
    );
}

#[test]
fn the_secret_is_read_as_a_bearer_a_basic_auth_password_or_a_path_segment_in_constant_time() {
    let bearer_value = format!("Bearer {SECRET}");
    let bearer_headers = [("Authorization", bearer_value.as_str())];
    let bearer = request(CORE_PATH, &bearer_headers);
    let basic_headers = [("Authorization", BASIC)];
    let basic = request(CORE_PATH, &basic_headers);
    let secret_path = format!("{CORE_PATH}/{SECRET}");
    let path = request(&secret_path, &[]);
    let none = request(CORE_PATH, &[]);
    assert_eq!(bearer_of(&bearer).as_deref(), Some(SECRET));
    assert_eq!(basic_password_of(&basic).as_deref(), Some(SECRET));
    assert_eq!(path_secret_of(&path, "mail-in").as_deref(), Some(SECRET));
    assert_eq!(path_secret_of(&none, "mail-in"), None);
    for carries in [&bearer, &basic, &path] {
        assert!(authorized(carries, "mail-in", SECRET));
    }
    assert!(!authorized(&none, "mail-in", SECRET));
    assert!(!authorized(&bearer, "mail-in", &format!("{SECRET}x")));
    assert!(!authorized(&bearer, "mail-in", ""));
    let not_base64 = [("Authorization", "Basic not-base64!")];
    assert!(!authorized(&request(CORE_PATH, &not_base64), "mail-in", SECRET));
}

#[test]
fn route_of_names_the_endpoint_whatever_prefix_the_platform_leaves_on_the_path() {
    assert_eq!(
        route_of("/functions/v1/mail-connector-resend/send"),
        Some(Route::Send)
    );
    assert_eq!(route_of("/mail-connector-postmark/webhook"), Some(Route::Webhook));
    assert_eq!(
        route_of(&format!("/mail-connector-postmark/webhook/{SECRET}")),
        Some(Route::Webhook)
    );
    assert_eq!(route_of("/mail-connector-postmark"), None);
}

/// `sendClient POSTs the outbound shape with the bearer and maps the answers`: the request the client
/// makes, then each answer the TypeScript's stand-in `fetch` gave.
#[test]
fn the_send_client_posts_the_outbound_shape_with_the_bearer_and_maps_the_answers() {
    let sent = send_request("https://c.example/send", SECRET, &outbound()).unwrap();
    assert_eq!(sent.method, "POST");
    assert_eq!(sent.url, "https://c.example/send");
    assert_eq!(
        sent.header("authorization"),
        Some(format!("Bearer {SECRET}").as_str())
    );
    assert_eq!(sent.header("content-type"), Some("application/json"));
    assert_eq!(sent.body.as_deref(), Some(to_outbound_wire(&outbound()).as_str()));
    assert_eq!(send_answer(200, r#"{"id":"re_9"}"#), Ok("re_9".to_owned()));

    let final_error = send_answer(422, r#"{"error":"inactive recipient","retryable":false}"#).unwrap_err();
    assert!(!final_error.retryable);
    assert!(final_error.message.contains("inactive recipient"));

    assert!(send_answer(429, "").unwrap_err().retryable);

    let down = send_unanswered("connection refused");
    assert!(down.retryable);
    assert_eq!(down.status, 0);
    assert_eq!(down.message, "connector: connection refused");

    let no_id = send_answer(200, "{}").unwrap_err();
    assert!(no_id.retryable);
    assert_eq!(
        (no_id.status, no_id.message.as_str()),
        (502, "connector: the send answer carries no id")
    );

    let unset = send_request("", SECRET, &outbound()).err().unwrap();
    assert!(!unset.retryable);
    assert_eq!(
        unset.message,
        "BRIGADE_MAIL_SEND_URL is not set: run the installer"
    );

    assert!(
        send_answer(400, r#"{"error":"busy","retryable":true}"#)
            .unwrap_err()
            .retryable
    );
}

/// `the connector forwards a webhook's mail to the core as the inbound shape`.
#[test]
fn the_connector_forwards_a_webhooks_mail_to_the_core_as_the_inbound_shape() {
    let forwarded = forward_request(CORE, SECRET, &inbound());
    assert_eq!(forwarded.method, "POST");
    assert_eq!(forwarded.url, CORE);
    assert_eq!(
        forwarded.header("authorization"),
        Some(format!("Bearer {SECRET}").as_str())
    );
    assert_eq!(
        forwarded.body.as_deref(),
        Some(to_inbound_wire(&inbound()).as_str())
    );
    let answer = answer_forwarded(Some((200, r#"{"message_id":"m1"}"#)));
    assert_eq!(answer.status, 200);
    assert_eq!(value(&answer.body), json!({ "message_id": "m1" }));
}

/// `the connector fetches the mail when the webhook carried only the id`. What makes the handler fetch is
/// a hook with no mail in it, and Resend's webhook is the one that carries only an id: the hook has none,
/// the fetch is one request by that id, and what it answers is what goes to the core.
#[test]
fn the_connector_fetches_the_mail_when_the_webhook_carried_only_the_id() {
    let secret = "whsec_MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY=";
    let provider = Resend::new("key", Some(secret.to_owned()));
    let event = r#"{"type":"email.received","data":{"email_id":"re_1"}}"#;
    let signature = sign(secret, "msg_1", "1791115200", event).unwrap();
    let headers = [
        ("svix-id", "msg_1"),
        ("svix-timestamp", "1791115200"),
        ("svix-signature", signature.as_str()),
    ];
    let hook = provider
        .webhook(
            &request("/functions/v1/mail-connector-resend/webhook", &headers),
            event,
            NOW,
        )
        .unwrap()
        .unwrap();
    assert_eq!(hook.provider_id, "re_1");
    assert_eq!(hook.mail, None);
    let fetch = provider.fetch_request(&hook.provider_id);
    assert_eq!(
        (fetch.method, fetch.url.as_str()),
        ("GET", "https://api.resend.com/emails/receiving/re_1")
    );
    let fetched = provider
        .fetched(
            &hook.provider_id,
            200,
            r#"{"from":"alice@example.com","to":["a1b2c3d4@example.resend.app"],"text":"yes please"}"#,
            NOW,
        )
        .unwrap();
    assert_eq!(fetched.provider_id, "re_1");
    assert_eq!(
        forward_request(CORE, SECRET, &fetched).body.as_deref(),
        Some(to_inbound_wire(&fetched).as_str())
    );
    assert_eq!(answer_forwarded(Some((200, "{}"))).status, 200);
}

/// `the connector answers the provider by what the core and the webhook said`.
#[test]
fn the_connector_answers_the_provider_by_what_the_core_and_the_webhook_said() {
    let core = |status: u16| answer_forwarded(Some((status, r#"{"error":"x"}"#)));
    assert_eq!(core(401).status, 502, "a wrong secret: let the provider retry");
    assert_eq!(core(401).body, r#"{"error":"the gateway core answered 401"}"#);
    assert_eq!(core(503).status, 502, "a core outage: retry");
    let dropped = core(400);
    assert_eq!(
        dropped.status, 200,
        "a mail the core refused for good is not retried"
    );
    assert_eq!(value(&dropped.body), json!({ "dropped": 400 }));
    let rejected = answer_rejected(&WebhookRejected::with_status("not signed", 403));
    assert_eq!(rejected.status, 403);
    assert_eq!(rejected.body, r#"{"error":"not signed"}"#);
    let ignored = answer_not_a_mail();
    assert_eq!(ignored.status, 200);
    assert_eq!(value(&ignored.body), json!({ "ignored": true }));
    let down = answer_forwarded(None);
    assert_eq!(down.status, 502);
    assert_eq!(down.body, r#"{"error":"the gateway core did not answer"}"#);
    assert_eq!(
        answer_unreadable(),
        Answer {
            status: 502,
            body: r#"{"error":"the mail could not be read from the provider"}"#.to_owned()
        }
    );
    let webhook = format!("{CONNECTOR_PATH}/webhook");
    assert_eq!(route("POST", &webhook), Ok(Route::Webhook));
    assert_eq!(route("GET", &webhook).unwrap_err().status, 405);
    assert_eq!(
        route("GET", &webhook).unwrap_err().body,
        r#"{"error":"method not allowed"}"#
    );
    let other = route("POST", &format!("{CONNECTOR_PATH}/other")).unwrap_err();
    assert_eq!(other.status, 404);
    assert_eq!(
        other.body,
        r#"{"error":"no such endpoint: POST …/webhook or …/send"}"#
    );
}

/// `the connector's send endpoint checks the secret, validates the body and maps send errors`.
#[test]
fn the_connectors_send_endpoint_checks_the_secret_validates_the_body_and_maps_send_errors() {
    let send_path = format!("{CONNECTOR_PATH}/send");
    let wire = to_outbound_wire(&outbound());
    let bearer_value = format!("Bearer {SECRET}");
    let bearer = [("Authorization", bearer_value.as_str())];
    assert_eq!(route("POST", &send_path), Ok(Route::Send));

    let unauthorized = read_send(&request(&send_path, &[]), SECRET, &wire).unwrap_err();
    assert_eq!(unauthorized.status, 401);
    assert_eq!(
        unauthorized.body,
        r#"{"error":"the request does not carry this connector's secret"}"#
    );
    let bad = read_send(&request(&send_path, &bearer), SECRET, r#"{"brigade_mail":1}"#).unwrap_err();
    assert_eq!(bad.status, 400);
    assert_eq!(bad.body, r#"{"error":"from is required"}"#);

    // The provider is handed the mail the core sent, and its id is the answer.
    assert_eq!(
        read_send(&request(&send_path, &bearer), SECRET, &wire),
        Ok(outbound())
    );
    let ok = answer_sent(&Ok("sent-1".to_owned()));
    assert_eq!(ok.status, 200);
    assert_eq!(value(&ok.body), json!({ "id": "sent-1" }));
    let by_path = format!("{send_path}/{SECRET}");
    assert_eq!(read_send(&request(&by_path, &[]), SECRET, &wire), Ok(outbound()));

    let failing = answer_sent(&Err(SendError::new(422, "inactive recipient", Some(false))));
    assert_eq!(failing.status, 422);
    assert_eq!(
        value(&failing.body),
        json!({ "error": "inactive recipient", "retryable": false })
    );
    assert_eq!(
        failing.body,
        r#"{"error":"inactive recipient","retryable":false}"#
    );

    let no_answer = answer_sent(&Err(SendError::new(0, "provider unreachable", None)));
    assert_eq!(no_answer.status, 502);
    assert_eq!(value(&no_answer.body)["retryable"], json!(true));
}

#[test]
fn the_shapes_are_written_as_json_stringify_writes_them() {
    assert_eq!(
        to_inbound_wire(&inbound()),
        r#"{"brigade_mail":1,"id":"pm-1","from":"alice@example.com","from_name":"Alice","to":["a1b2c3d4+0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f@example.resend.app"],"subject":"Re: hello","message_id":"<m1@example.com>","in_reply_to":"<m0@example.com>","references":["<m0@example.com>"],"text":"yes please","received_at":"2026-10-04T12:00:00.000Z"}"#
    );
    assert_eq!(
        to_outbound_wire(&outbound()),
        r#"{"brigade_mail":1,"from":"Brigade <brigade@example.com>","to":"alice@example.com","reply_to":"a1b2c3d4+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@example.resend.app","subject":"[brigade/team] hello","text":"hello","headers":{"Auto-Submitted":"auto-generated"}}"#
    );
    // What is missing is `null`, and a text is escaped as JSON escapes it: the quote, the backslash and the
    // control characters, and nothing else.
    let bare = OutboundMail {
        reply_to: None,
        text: "a \"b\"\\\n\t\u{1}\u{7f}é\u{2028}🙂".to_owned(),
        headers: vec![
            ("References".to_owned(), "<b@x>".to_owned()),
            ("In-Reply-To".to_owned(), "<a@x>".to_owned()),
        ],
        ..outbound()
    };
    assert_eq!(
        to_outbound_wire(&bare),
        "{\"brigade_mail\":1,\"from\":\"Brigade <brigade@example.com>\",\"to\":\"alice@example.com\",\"reply_to\":null,\"subject\":\"[brigade/team] hello\",\"text\":\"a \\\"b\\\"\\\\\\n\\t\\u0001\u{7f}é\u{2028}🙂\",\"headers\":{\"References\":\"<b@x>\",\"In-Reply-To\":\"<a@x>\"}}"
    );
    // The headers come back in the order they were written, not in the order of their names.
    assert_eq!(from_outbound_wire(&to_outbound_wire(&bare)), Ok(bare));
}

#[test]
fn each_rule_of_a_shape_is_checked_in_the_typescripts_order() {
    let inbound_error = |body: Value| from_inbound_wire(&body.to_string(), NOW).unwrap_err();
    assert_eq!(inbound_error(json!({})), "brigade_mail must be 1");
    assert_eq!(
        inbound_error(json!({ "brigade_mail": "1" })),
        "brigade_mail must be 1"
    );
    assert_eq!(
        inbound_error(json!({ "brigade_mail": 1, "id": 5 })),
        "id is required"
    );
    assert_eq!(
        inbound_error(json!({ "brigade_mail": 1, "id": "x", "from": " a@b.c" })),
        "from must be an email address"
    );
    let valid = |members: Value| {
        let mut body = json!({ "brigade_mail": 1.0, "id": "x", "from": "a@b.c", "to": [] });
        for (name, value) in members.as_object().unwrap() {
            body[name] = value.clone();
        }
        body
    };
    assert_eq!(
        inbound_error(valid(json!({ "to": ["a@b.c", 5] }))),
        "to must be a list of addresses"
    );
    assert_eq!(
        inbound_error(valid(json!({ "references": "<a@x>", "text": 5 }))),
        "references must be a list of strings"
    );
    assert_eq!(
        inbound_error(valid(json!({ "references": [null] }))),
        "references must be a list of strings"
    );
    assert_eq!(
        inbound_error(valid(json!({ "received_at": 5, "subject": 5 }))),
        "subject must be a string"
    );
    assert_eq!(
        inbound_error(valid(json!({ "received_at": false }))),
        "received_at must be a string"
    );
    // `null` is a member that is not there, and an empty string is no value.
    let sparse = from_inbound_wire(
        &valid(json!({ "references": null, "from_name": null, "subject": null, "message_id": "", "received_at": "" }))
            .to_string(),
        NOW,
    )
    .unwrap();
    assert_eq!(sparse.to, Vec::<String>::new());
    assert_eq!((sparse.message_id, sparse.from_name), (None, None));
    assert_eq!(sparse.received_at, "2026-10-04T12:00:00.000Z");

    let outbound_error = |body: Value| from_outbound_wire(&body.to_string()).unwrap_err();
    assert_eq!(outbound_error(json!("text")), "the body is not a JSON object");
    assert_eq!(outbound_error(json!({ "brigade_mail": 1 })), "from is required");
    let valid = |members: Value| {
        let mut body =
            json!({ "brigade_mail": 1, "from": "a@b.c", "to": "C@D.E", "subject": "", "text": "" });
        for (name, value) in members.as_object().unwrap() {
            body[name] = value.clone();
        }
        body
    };
    assert_eq!(
        from_outbound_wire(&valid(json!({})).to_string()).unwrap().to,
        "c@d.e"
    );
    assert_eq!(
        outbound_error(valid(json!({ "to": ["c@d.e"] }))),
        "to must be one email address"
    );
    assert_eq!(
        outbound_error(valid(json!({ "text": null }))),
        "text must be a string"
    );
    assert_eq!(
        outbound_error(valid(json!({ "reply_to": 5, "headers": [] }))),
        "reply_to must be a string"
    );
    assert_eq!(
        outbound_error(valid(json!({ "headers": [] }))),
        "headers must be an object"
    );
    assert_eq!(
        outbound_error(valid(json!({ "headers": "x" }))),
        "headers must be an object"
    );
    assert_eq!(
        from_outbound_wire(&valid(json!({ "reply_to": "", "headers": null })).to_string())
            .map(|mail| (mail.reply_to, mail.headers)),
        Ok((None, Vec::new()))
    );
}

#[test]
fn a_send_answer_is_success_a_retryable_error_or_a_final_one() {
    let failure = |status: u16, body: &str| {
        let error = send_answer(status, body).unwrap_err();
        (error.status, error.message, error.retryable)
    };
    // The status decides when the answer does not.
    assert_eq!(
        failure(500, "oops"),
        (500, "connector: send answered 500".to_owned(), true)
    );
    assert_eq!(
        failure(404, "{}"),
        (404, "connector: send answered 404".to_owned(), false)
    );
    assert_eq!(
        failure(429, r#"{"error":""}"#),
        (429, "connector: send answered 429".to_owned(), true)
    );
    // The answer's own word overrides the status, when it is a boolean.
    assert_eq!(
        failure(503, r#"{"error":"gone for good","retryable":false}"#),
        (503, "gone for good".to_owned(), false)
    );
    assert_eq!(
        failure(503, r#"{"error":5,"retryable":"false"}"#),
        (503, "connector: send answered 503".to_owned(), true)
    );
    // A 2xx is success only with an id that is a string and not empty.
    assert_eq!(
        send_answer(201, r#"{"id":"x","error":"ignored"}"#),
        Ok("x".to_owned())
    );
    for body in [r#"{"id":""}"#, r#"{"id":5}"#, "[]", "null", "", "not json"] {
        assert_eq!(
            failure(200, body),
            (502, "connector: the send answer carries no id".to_owned(), true),
            "{body}"
        );
    }
    assert_eq!(
        failure(300, "{}"),
        (300, "connector: send answered 300".to_owned(), false)
    );
}

#[test]
fn a_send_error_keeps_its_status_as_the_answer_only_when_it_is_an_errors() {
    let status = |status: u16| answer_sent(&Err(SendError::new(status, "x", None))).status;
    assert_eq!(status(400), 400);
    assert_eq!(status(599), 599);
    assert_eq!(status(200), 502);
    assert_eq!(status(399), 502);
    assert_eq!(status(600), 502);
    assert_eq!(status(0), 502);
}
