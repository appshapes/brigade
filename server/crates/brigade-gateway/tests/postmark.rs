//! `supabase/functions/_shared/providers/postmark_test.ts`, case for case: the same inputs and the same
//! expected outputs. Where the TypeScript handed the provider a stand-in `fetch`, the test reads the request
//! the provider would make and hands it the answer the stand-in gave.

use brigade_core::Timestamp;
use brigade_gateway::providers::Postmark;
use brigade_gateway::providers::postmark::{inbound, with_hash};
use brigade_gateway::{Hook, OutboundMail, Provider, Request, WebhookRejected};
use serde_json::{Value, json};

const INBOX: &str = "0123456789abcdef0123456789abcdef01234567@inbound.postmarkapp.com";
const TAGGED_INBOX: &str =
    "0123456789abcdef0123456789abcdef01234567+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@inbound.postmarkapp.com";
const TAG: &str = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";
const SECRET: &str = "fedcba9876543210fedcba9876543210fedcba9876543210";
const HOOK: &str = "/functions/v1/mail-connector-postmark/webhook";
/// `"Basic " + btoa("brigade:" + SECRET)`.
const BASIC: &str = "Basic YnJpZ2FkZTpmZWRjYmE5ODc2NTQzMjEwZmVkY2JhOTg3NjU0MzIxMGZlZGNiYTk4NzY1NDMyMTA=";
/// `"Basic " + btoa("brigade:nope")`.
const WRONG_BASIC: &str = "Basic YnJpZ2FkZTpub3Bl";

/// 2026-10-04T12:00:00.000Z.
const NOW: Timestamp = Timestamp(1_791_115_200_000_000_000);

/// The shape of Postmark's documented inbound webhook example, with placeholder values, and some members
/// replaced. A member replaced by `null` is one the TypeScript set to `undefined`: it is left out.
fn payload(over: Value) -> String {
    let mut payload = json!({
        "FromName": "Alice Example",
        "MessageStream": "inbound",
        "From": "alice@example.com",
        "FromFull": { "Email": "alice@example.com", "Name": "Alice Example", "MailboxHash": "" },
        "To": format!("\"Brigade\" <{TAGGED_INBOX}>"),
        "ToFull": [{ "Email": INBOX, "Name": "Brigade", "MailboxHash": TAG }],
        "Cc": "",
        "CcFull": [],
        "Bcc": "",
        "BccFull": [],
        "OriginalRecipient": INBOX,
        "Subject": "Re: [brigade/team] Please retest",
        "MessageID": "73e6d360-66eb-11e1-8e72-a8904824019b",
        "ReplyTo": "",
        "MailboxHash": TAG,
        "Date": "Sat, 4 Oct 2026 10:00:00 -0400",
        "TextBody": "Retested, works.\n\nOn Sat, Oct 4, 2026 Brigade wrote:\n> please retest",
        "HtmlBody": "<p>Retested, works.</p>",
        "StrippedTextReply": "Retested, works.",
        "Tag": "",
        "Headers": [
            { "Name": "Message-ID", "Value": "<CA+abc@mail.example.com>" },
            { "Name": "In-Reply-To", "Value": "<0f0f0f0f@mtasv.net>" },
            { "Name": "References", "Value": "<0f0f0f0f@mtasv.net>" },
            { "Name": "X-Spam-Status", "Value": "No" },
        ],
        "Attachments": [],
    });
    let members = payload.as_object_mut().unwrap();
    for (name, value) in over.as_object().unwrap() {
        if value.is_null() {
            members.remove(name);
        } else {
            members.insert(name.clone(), value.clone());
        }
    }
    payload.to_string()
}

fn provider() -> Postmark {
    Postmark::new("t", Some(SECRET.to_owned()))
}

#[test]
fn with_hash_puts_a_mailbox_hash_back_as_the_tag_and_lowercases() {
    assert_eq!(
        with_hash("Hash@Inbound.Postmarkapp.com", "ABC"),
        "hash+abc@inbound.postmarkapp.com"
    );
    assert_eq!(
        with_hash("hash+abc@inbound.postmarkapp.com", "abc"),
        "hash+abc@inbound.postmarkapp.com"
    );
    assert_eq!(
        with_hash("hash@inbound.postmarkapp.com", ""),
        "hash@inbound.postmarkapp.com"
    );
    assert_eq!(with_hash("", "abc"), "");
}

#[test]
fn postmark_inbound_maps_the_webhook_payload_to_the_gateways_inbound_mail() {
    let mail = inbound(&payload(json!({})), NOW).unwrap();
    assert_eq!(mail.provider_id, "73e6d360-66eb-11e1-8e72-a8904824019b");
    assert_eq!(mail.from, "alice@example.com");
    assert_eq!(mail.from_name.as_deref(), Some("Alice Example"));
    assert_eq!(mail.to, [TAGGED_INBOX]);
    assert_eq!(mail.subject, "Re: [brigade/team] Please retest");
    assert_eq!(mail.message_id.as_deref(), Some("<CA+abc@mail.example.com>"));
    assert_eq!(mail.in_reply_to.as_deref(), Some("<0f0f0f0f@mtasv.net>"));
    assert_eq!(mail.references, ["<0f0f0f0f@mtasv.net>"]);
    assert!(mail.text.contains("Retested, works."));
    assert_eq!(mail.received_at, "2026-10-04T14:00:00.000Z");
}

#[test]
fn postmark_inbound_falls_back_to_the_html_body_to_from_and_to_now() {
    let mail = inbound(
        &payload(json!({
            "TextBody": "",
            "FromFull": null,
            "From": "Bob <bob@example.com>",
            "FromName": "",
            "Date": "not a date",
            "ToFull": [{ "Email": "cc@example.com", "Name": "", "MailboxHash": "" }],
            "CcFull": [{ "Email": INBOX, "Name": "", "MailboxHash": "" }],
            "OriginalRecipient": "",
        })),
        NOW,
    )
    .unwrap();
    assert_eq!(mail.text, "Retested, works.");
    assert_eq!(mail.from, "bob@example.com");
    assert_eq!(mail.from_name.as_deref(), Some("Bob"));
    assert_eq!(mail.to, ["cc@example.com", INBOX]);
    // The TypeScript asserts that the time is a date at all; here it is the time that was handed in.
    assert_eq!(mail.received_at, "2026-10-04T12:00:00.000Z");
}

/// The provider's reading of a webhook that came with `headers` to `path`.
fn hook(
    provider: &Postmark,
    headers: &[(&str, &str)],
    path: &str,
    raw: &str,
) -> Result<Option<Hook>, WebhookRejected> {
    provider.webhook(&Request { path, headers }, raw, NOW)
}

#[test]
fn the_webhook_is_authenticated_by_the_basic_auth_password_or_the_path_and_refused_with_403() {
    let provider = provider();
    let raw = payload(json!({}));
    let basic = [("Authorization", BASIC)];
    let got = hook(&provider, &basic, HOOK, &raw).unwrap().unwrap();
    assert_eq!(got.provider_id, "73e6d360-66eb-11e1-8e72-a8904824019b");
    assert_eq!(
        got.mail.as_ref().map(|mail| mail.from.as_str()),
        Some("alice@example.com")
    );
    let by_path = hook(&provider, &[], &format!("{HOOK}/{SECRET}"), &raw)
        .unwrap()
        .unwrap();
    assert_eq!(by_path.provider_id, got.provider_id);
    assert_eq!(hook(&provider, &[], HOOK, &raw).unwrap_err().status, 403);
    let wrong = [("Authorization", WRONG_BASIC)];
    assert_eq!(hook(&provider, &wrong, HOOK, &raw).unwrap_err().status, 403);
    let unset = Postmark::new("t", None);
    assert_eq!(hook(&unset, &basic, HOOK, &raw).unwrap_err().status, 403);
    assert_eq!(hook(&provider, &basic, HOOK, "not json"), Ok(None));
    let bounce = json!({ "RecordType": "Bounce", "MessageID": "x", "MessageStream": "outbound" }).to_string();
    assert_eq!(hook(&provider, &basic, HOOK, &bounce), Ok(None));
}

fn outbound() -> OutboundMail {
    OutboundMail {
        from: "Brigade <brigade@example.com>".to_owned(),
        to: "alice@example.com".to_owned(),
        reply_to: Some(TAGGED_INBOX.to_owned()),
        subject: "[brigade/team] hello".to_owned(),
        text: "hello".to_owned(),
        headers: vec![
            ("Auto-Submitted".to_owned(), "auto-generated".to_owned()),
            ("In-Reply-To".to_owned(), "<x@example.com>".to_owned()),
        ],
    }
}

#[test]
fn send_posts_postmarks_shape_with_the_server_token_and_maps_its_answers() {
    let provider = Postmark::new("server-token", Some(SECRET.to_owned()));
    let request = provider.send_request(&outbound());
    assert_eq!(request.method, "POST");
    assert_eq!(request.url, "https://api.postmarkapp.com/email");
    assert_eq!(request.header("x-postmark-server-token"), Some("server-token"));
    assert_eq!(request.header("accept"), Some("application/json"));
    assert_eq!(request.header("content-type"), Some("application/json"));
    let body: Value = serde_json::from_str(request.body.as_deref().unwrap()).unwrap();
    assert_eq!(body["From"], outbound().from);
    assert_eq!(body["To"], outbound().to);
    assert_eq!(body["ReplyTo"], TAGGED_INBOX);
    assert_eq!(body["TextBody"], "hello");
    assert_eq!(body["MessageStream"], "outbound");
    assert_eq!(
        body["Headers"],
        json!([
            { "Name": "Auto-Submitted", "Value": "auto-generated" },
            { "Name": "In-Reply-To", "Value": "<x@example.com>" },
        ])
    );
    assert!(body.get("HtmlBody").is_none());
    assert_eq!(
        provider.sent(200, r#"{"ErrorCode":0,"Message":"OK","MessageID":"pm-9"}"#),
        Ok("pm-9".to_owned())
    );

    let inactive = provider
        .sent(422, r#"{"ErrorCode":406,"Message":"inactive recipient"}"#)
        .unwrap_err();
    assert!(!inactive.retryable);
    assert!(inactive.message.contains("(error 406): inactive recipient"));
    assert_eq!(
        inactive.message,
        "postmark: send answered 422 (error 406): inactive recipient"
    );

    assert!(provider.sent(429, "").unwrap_err().retryable);

    let odd_ok = provider
        .sent(200, r#"{"ErrorCode":300,"Message":"invalid"}"#)
        .unwrap_err();
    assert!(!odd_ok.retryable);
}

#[test]
fn fetch_reads_an_inbound_messages_details_through_the_same_mapping() {
    let provider = provider();
    let request = provider.fetch_request("abc");
    assert_eq!(request.method, "GET");
    assert!(request.url.contains("/messages/inbound/abc/details"));
    assert_eq!(request.header("x-postmark-server-token"), Some("t"));
    let mail = provider
        .fetched("abc", 200, &payload(json!({ "MessageID": null })), NOW)
        .unwrap();
    assert_eq!(mail.provider_id, "abc");
    assert_eq!(mail.from, "alice@example.com");
}

#[test]
fn the_send_body_is_written_as_json_stringify_writes_it() {
    let provider = Postmark::new("server-token", None);
    assert_eq!(
        provider.send_request(&outbound()).body.as_deref(),
        Some(
            r#"{"From":"Brigade <brigade@example.com>","To":"alice@example.com","Subject":"[brigade/team] hello","TextBody":"hello","MessageStream":"outbound","Headers":[{"Name":"Auto-Submitted","Value":"auto-generated"},{"Name":"In-Reply-To","Value":"<x@example.com>"}],"ReplyTo":"0123456789abcdef0123456789abcdef01234567+1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e@inbound.postmarkapp.com"}"#
        )
    );
    let bare = OutboundMail {
        reply_to: None,
        headers: Vec::new(),
        ..outbound()
    };
    assert_eq!(
        provider.send_request(&bare).body.as_deref(),
        Some(
            r#"{"From":"Brigade <brigade@example.com>","To":"alice@example.com","Subject":"[brigade/team] hello","TextBody":"hello","MessageStream":"outbound","Headers":[]}"#
        )
    );
}

#[test]
fn a_recipient_is_read_from_every_list_once_with_its_hash() {
    let mail = inbound(
        &payload(json!({
            "ToFull": [
                { "Email": " A@Example.com ", "MailboxHash": "" },
                { "Email": "a@example.com" },
                { "Email": "" },
                null,
                "text",
            ],
            "CcFull": [{ "Email": "c@example.com", "MailboxHash": " Tag " }],
            "BccFull": [{ "Email": "b+x@example.com", "MailboxHash": "tag" }],
            "OriginalRecipient": "O@Example.com",
            "MailboxHash": "h",
        })),
        NOW,
    )
    .unwrap();
    assert_eq!(
        mail.to,
        [
            "a@example.com",
            "c+tag@example.com",
            "b+x@example.com",
            "o+h@example.com"
        ]
    );
    // An original recipient that is only white space is added as the empty address it becomes.
    let blank = inbound(&payload(json!({ "ToFull": [], "OriginalRecipient": " " })), NOW).unwrap();
    assert_eq!(blank.to, [""]);
}

#[test]
fn a_header_is_read_by_its_first_spelling_and_a_payload_of_other_types_is_text() {
    let mail = inbound(
        &payload(json!({
            "Headers": [
                { "Name": "message-id", "Value": "<first@x>" },
                { "Name": "Message-ID", "Value": "<second@x>" },
                { "Name": "message-id", "Value": "<third@x>" },
                { "Name": 5, "Value": "<not@x>" },
                { "Name": "References", "Value": null },
                { "Name": "In-Reply-To" },
            ],
            "Subject": 5,
            "TextBody": " \n",
            "HtmlBody": "<p>from the <b>HTML</b></p>",
            "FromFull": { "Email": "Carol <CAROL@Example.com>", "Name": " " },
            "FromName": "ignored: FromFull has a name, blank as it is",
        })),
        NOW,
    )
    .unwrap();
    assert_eq!(mail.message_id.as_deref(), Some("<first@x>"));
    assert_eq!(mail.in_reply_to, None);
    assert_eq!(mail.references, Vec::<String>::new());
    assert_eq!(mail.subject, "5");
    assert_eq!(mail.text, "from the HTML");
    assert_eq!(mail.from, "carol@example.com");
    assert_eq!(mail.from_name.as_deref(), Some("Carol"));
    assert!(inbound("[]", NOW).is_err());
    assert!(inbound("not json", NOW).is_err());
}

#[test]
fn what_is_a_mail_and_what_is_not() {
    let provider = provider();
    let basic = [("Authorization", BASIC)];
    let carried = |payload: Value| {
        hook(&provider, &basic, HOOK, &payload.to_string())
            .unwrap()
            .map(|hook| hook.provider_id)
    };
    assert_eq!(
        carried(json!({ "MessageID": "m", "From": "a@b.c" })).as_deref(),
        Some("m")
    );
    assert_eq!(
        carried(json!({ "MessageID": "m", "FromFull": {}, "MessageStream": "inbound" })).as_deref(),
        Some("m")
    );
    assert_eq!(
        carried(json!({ "MessageID": "m", "From": "a@b.c", "MessageStream": 5 })).as_deref(),
        Some("m")
    );
    for not_a_mail in [
        json!({ "MessageID": "m" }),
        json!({ "MessageID": "m", "From": "" }),
        json!({ "MessageID": "", "From": "a@b.c" }),
        json!({ "MessageID": 5, "From": "a@b.c" }),
        json!({ "From": "a@b.c" }),
        json!({ "MessageID": "m", "From": "a@b.c", "MessageStream": "outbound" }),
        json!({ "MessageID": "m", "From": "a@b.c", "MessageStream": "" }),
        json!(["MessageID"]),
        json!(null),
    ] {
        assert_eq!(carried(not_a_mail.clone()), None, "{not_a_mail}");
    }
    // An empty secret is no secret.
    let empty = Postmark::new("t", Some(String::new()));
    let rejected = hook(&empty, &basic, HOOK, "{}").unwrap_err();
    assert_eq!(
        (rejected.status, rejected.message.as_str()),
        (403, "the webhook secret is not configured")
    );
    let unauthorized = hook(&provider, &[], HOOK, "{}").unwrap_err();
    assert_eq!(
        unauthorized.message,
        "the request does not carry this connector's webhook secret"
    );
}

#[test]
fn a_send_answer_is_an_id_or_an_error_with_its_code_and_message() {
    let provider = provider();
    let failure = |status: u16, body: &str| {
        let error = provider.sent(status, body).unwrap_err();
        (error.status, error.message, error.retryable)
    };
    assert_eq!(
        failure(429, ""),
        (429, "postmark: send answered 429".to_owned(), true)
    );
    assert_eq!(
        failure(500, "<html>"),
        (500, "postmark: send answered 500".to_owned(), true)
    );
    assert_eq!(
        failure(401, r#"{"ErrorCode":10,"Message":"Bad token"}"#),
        (
            401,
            "postmark: send answered 401 (error 10): Bad token".to_owned(),
            false
        )
    );
    assert_eq!(
        failure(422, r#"{"ErrorCode":"406","Message":["a","b"]}"#),
        (422, "postmark: send answered 422: a,b".to_owned(), false)
    );
    assert_eq!(
        failure(200, r#"{"ErrorCode":300.5,"Message":""}"#),
        (200, "postmark: send answered 200 (error 300.5)".to_owned(), false)
    );
    assert!(failure(503, r#"{"ErrorCode":0}"#).2);
    // Accepted: the id as text, empty when the answer names none.
    assert_eq!(
        provider.sent(200, r#"{"MessageID":"pm-1"}"#),
        Ok("pm-1".to_owned())
    );
    assert_eq!(provider.sent(200, r#"{"MessageID":7}"#), Ok("7".to_owned()));
    assert_eq!(provider.sent(201, "{}"), Ok(String::new()));
    assert_eq!(provider.sent(200, "not json"), Ok(String::new()));
    assert_eq!(provider.sent(200, "[]"), Ok(String::new()));
}

#[test]
fn a_fetch_that_was_refused_or_is_not_json_is_an_error() {
    let provider = provider();
    assert_eq!(
        provider.fetched("abc", 404, "{}", NOW),
        Err("postmark: inbound message details answered 404".to_owned())
    );
    assert!(provider.fetched("abc", 200, "<html>", NOW).is_err());
    assert_eq!(
        provider.fetch_request("a/b c").url,
        "https://api.postmarkapp.com/messages/inbound/a%2Fb%20c/details"
    );
}
