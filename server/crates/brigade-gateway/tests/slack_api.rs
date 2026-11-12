//! The Slack Web API (`supabase/functions/_shared/providers/slack.ts`). The TypeScript has no test file for
//! it; these are the rules that file states, read off its source.

use brigade_gateway::HttpRequest;
use brigade_gateway::providers::Slack;
use brigade_gateway::providers::slack::{
    API, AuthTest, ChannelSearch, MAX_PAGES, Metadata, NO_ANSWER, PostEphemeral, PostMessage, Posted,
    SlackChannel, SlackError, SlackUser, UNREADABLE_ANSWER, UserSearch, granted_scopes,
};
use serde_json::{Value, json};

fn slack() -> Slack {
    Slack::new("xoxb-token")
}

/// A request as its method, its URL without the API's base, its headers and its body.
fn parts(request: &HttpRequest) -> (&str, &str, Vec<(&str, &str)>, Option<&str>) {
    (
        request.method,
        request.url.strip_prefix(API).expect("the request goes to Slack"),
        request
            .headers
            .iter()
            .map(|(name, value)| (*name, value.as_str()))
            .collect(),
        request.body.as_deref(),
    )
}

const BEARER: (&str, &str) = ("Authorization", "Bearer xoxb-token");
const JSON: (&str, &str) = ("Content-Type", "application/json; charset=utf-8");

fn error(code: &str, message: &str) -> SlackError {
    SlackError {
        code: code.to_owned(),
        message: message.to_owned(),
    }
}

fn ok(members: Value) -> String {
    let mut answer = json!({ "ok": true });
    for (name, value) in members.as_object().expect("an object of members") {
        answer[name] = value.clone();
    }
    answer.to_string()
}

#[test]
fn a_call_with_a_body_is_a_post_of_json_with_the_bot_token_as_a_bearer() {
    let slack = slack();
    assert_eq!(
        parts(&slack.auth_test_request()),
        ("POST", "/auth.test", vec![BEARER, JSON], Some("{}"))
    );
    assert_eq!(
        parts(&slack.open_dm_request("U1")),
        (
            "POST",
            "/conversations.open",
            vec![BEARER, JSON],
            Some(r#"{"users":"U1"}"#)
        )
    );
    assert_eq!(
        parts(&slack.join_channel_request("C1")),
        (
            "POST",
            "/conversations.join",
            vec![BEARER, JSON],
            Some(r#"{"channel":"C1"}"#)
        )
    );
    assert_eq!(
        parts(&slack.add_reaction_request("C1", "1.1", "eyes")),
        (
            "POST",
            "/reactions.add",
            vec![BEARER, JSON],
            Some(r#"{"channel":"C1","timestamp":"1.1","name":"eyes"}"#)
        )
    );
    // A stand-in's base is where the requests go.
    let stand_in = Slack {
        bot_token: "t".to_owned(),
        base_url: "http://127.0.0.1:9/api".to_owned(),
    };
    assert_eq!(
        stand_in.auth_test_request().url,
        "http://127.0.0.1:9/api/auth.test"
    );
}

#[test]
fn a_post_never_unfurls_and_carries_a_thread_and_metadata_only_when_it_has_them() {
    let slack = slack();
    let post = |thread_ts: Option<&str>, metadata: Option<Metadata>| {
        slack
            .post_message_request(&PostMessage {
                channel: "C1".to_owned(),
                text: "hi \"there\"\n<@U1> é".to_owned(),
                thread_ts: thread_ts.map(str::to_owned),
                metadata,
            })
            .body
            .unwrap()
    };
    let plain =
        r#"{"channel":"C1","text":"hi \"there\"\n<@U1> é","unfurl_links":false,"unfurl_media":false}"#;
    assert_eq!(post(None, None), plain);
    // An empty thread is no thread.
    assert_eq!(post(Some(""), None), plain);
    assert_eq!(
        post(Some("5.5"), None),
        r#"{"channel":"C1","text":"hi \"there\"\n<@U1> é","unfurl_links":false,"unfurl_media":false,"thread_ts":"5.5"}"#
    );
    let metadata = Metadata {
        event_type: "brigade_message".to_owned(),
        event_payload: vec![
            ("message_id".to_owned(), "m1".to_owned()),
            ("from_session".to_owned(), "s1".to_owned()),
            ("team".to_owned(), "o\"ps".to_owned()),
        ],
    };
    // The payload's members are sent in the order they are given.
    assert_eq!(
        post(Some("5.5"), Some(metadata)),
        r#"{"channel":"C1","text":"hi \"there\"\n<@U1> é","unfurl_links":false,"unfurl_media":false,"thread_ts":"5.5","metadata":{"event_type":"brigade_message","event_payload":{"message_id":"m1","from_session":"s1","team":"o\"ps"}}}"#
    );
    let request = slack.post_message_request(&PostMessage {
        channel: "C1".to_owned(),
        text: "t".to_owned(),
        thread_ts: None,
        metadata: None,
    });
    let (method, path, headers, _) = parts(&request);
    assert_eq!((method, path), ("POST", "/chat.postMessage"));
    assert_eq!(headers, [BEARER, JSON]);
}

#[test]
fn an_ephemeral_post_names_who_sees_it() {
    let slack = slack();
    let post = |thread_ts: Option<&str>| {
        slack.post_ephemeral_request(&PostEphemeral {
            channel: "C1".to_owned(),
            user: "U1".to_owned(),
            text: "psst".to_owned(),
            thread_ts: thread_ts.map(str::to_owned),
        })
    };
    assert_eq!(
        parts(&post(None)),
        (
            "POST",
            "/chat.postEphemeral",
            vec![BEARER, JSON],
            Some(r#"{"channel":"C1","user":"U1","text":"psst"}"#)
        )
    );
    assert_eq!(post(Some("")).body, post(None).body);
    assert_eq!(
        post(Some("5.5")).body.as_deref(),
        Some(r#"{"channel":"C1","user":"U1","text":"psst","thread_ts":"5.5"}"#)
    );
}

#[test]
fn a_lookup_is_a_get_with_its_argument_escaped_in_the_query() {
    let slack = slack();
    assert_eq!(
        parts(&slack.user_info_request("U1")),
        ("GET", "/users.info?user=U1", vec![BEARER], None)
    );
    assert_eq!(
        slack.user_info_request("U 1&x=é/?").url,
        format!("{API}/users.info?user=U%201%26x%3D%C3%A9%2F%3F")
    );
    assert_eq!(
        parts(&slack.lookup_by_email_request("alice+x@example.com")),
        (
            "GET",
            "/users.lookupByEmail?email=alice%2Bx%40example.com",
            vec![BEARER],
            None
        )
    );
    assert_eq!(
        parts(&slack.channel_info_request("C 1/é")),
        (
            "GET",
            "/conversations.info?channel=C%201%2F%C3%A9",
            vec![BEARER],
            None
        )
    );
    assert_eq!(
        parts(&slack.permalink_request("C&1", "1 .1")),
        (
            "GET",
            "/chat.getPermalink?channel=C%261&message_ts=1%20.1",
            vec![BEARER],
            None
        )
    );
}

#[test]
fn an_answer_that_is_not_a_2xx_or_not_ok_is_an_error_with_slacks_code() {
    assert_eq!(
        Slack::post_message_answer(500, ""),
        Err(error("http_500", "slack: chat.postMessage answered 500"))
    );
    // The status is read before the body: a 429 is `http_429`, whatever the body says.
    assert_eq!(
        Slack::post_message_answer(429, r#"{"ok":false,"error":"ratelimited"}"#),
        Err(error("http_429", "slack: chat.postMessage answered 429"))
    );
    assert_eq!(
        Slack::post_message_answer(200, r#"{"ok":false,"error":"channel_not_found"}"#),
        Err(error(
            "channel_not_found",
            "slack: chat.postMessage: channel_not_found"
        ))
    );
    for unnamed in [
        r#"{"ok":false}"#,
        r#"{"ok":"true"}"#,
        "{}",
        "[]",
        "5",
        r#"{"error":null}"#,
    ] {
        assert_eq!(
            Slack::join_channel_answer(200, unnamed),
            Err(error("unknown_error", "slack: conversations.join: unknown_error")),
            "{unnamed}"
        );
    }
    // An error that is no string is its text, as `String()` makes it.
    assert_eq!(
        Slack::join_channel_answer(200, r#"{"ok":false,"error":["a","b"]}"#),
        Err(error("a,b", "slack: conversations.join: a,b"))
    );
    // The failure of a lookup names the method with its query.
    assert_eq!(
        Slack::user_info_answer("U 1", 503, ""),
        Err(error("http_503", "slack: users.info?user=U%201 answered 503"))
    );
    assert_eq!(
        Slack::lookup_by_email_answer("a@b.io", 200, r#"{"ok":false,"error":"missing_scope"}"#),
        Err(error(
            "missing_scope",
            "slack: users.lookupByEmail?email=a%40b.io: missing_scope"
        ))
    );
    assert_eq!(
        Slack::post_message_answer(299, r#"{"ok":true,"ts":"1","channel":"C"}"#).map(|posted| posted.ts),
        Ok("1".to_owned())
    );
}

#[test]
fn slack_asking_for_a_pause_and_the_platform_failing_are_retryable_and_nothing_else_is() {
    for code in [
        "ratelimited",
        "internal_error",
        "service_unavailable",
        "fatal_error",
        "http_500",
        "http_503",
        "http_599",
    ] {
        assert!(SlackError::new(code, None).retryable(), "{code}");
    }
    for code in [
        "http_429",
        "http_404",
        "http_400",
        "channel_not_found",
        "not_in_channel",
        "unknown_error",
        "RATELIMITED",
        "",
        UNREADABLE_ANSWER,
        NO_ANSWER,
    ] {
        assert!(!SlackError::new(code, None).retryable(), "{code}");
    }
    // A failure with no text of its own is told by its code.
    let failure = SlackError::new("ratelimited", None);
    assert_eq!(failure.message, "slack: ratelimited");
    assert_eq!(failure.to_string(), "slack: ratelimited");
    // No answer at all is the caller's to say, in the caller's words, and is not retryable.
    let unanswered = SlackError::unanswered("connection refused");
    assert_eq!(unanswered, error(NO_ANSWER, "connection refused"));
    assert!(!unanswered.retryable());
}

#[test]
fn an_answer_that_cannot_be_read_is_an_error_of_its_own_and_is_not_retryable() {
    let unreadable = |method: &str| {
        error(
            UNREADABLE_ANSWER,
            &format!("slack: {method}: the answer could not be read"),
        )
    };
    for body in ["", "nope", "null", "<html>"] {
        assert_eq!(
            Slack::auth_test_answer(200, body),
            Err(unreadable("auth.test")),
            "{body}"
        );
    }
    assert_eq!(
        Slack::open_dm_answer(200, r#"{"ok":true}"#),
        Err(unreadable("conversations.open"))
    );
    assert_eq!(
        Slack::user_info_answer("U1", 200, r#"{"ok":true,"user":null}"#),
        Err(unreadable("users.info?user=U1"))
    );
    assert_eq!(
        Slack::channel_info_answer("C1", 200, r#"{"ok":true}"#),
        Err(unreadable("conversations.info?channel=C1"))
    );
    assert!(!unreadable("auth.test").retryable());
}

#[test]
fn auth_test_reads_the_bot_and_the_workspaces_host() {
    let read = |answer: Value| Slack::auth_test_answer(200, &ok(answer)).unwrap();
    assert_eq!(
        read(json!({
            "user_id": "U0BOT",
            "bot_id": "B1",
            "team": "AppShapes",
            "url": "https://appshapes.slack.com/",
        })),
        AuthTest {
            user_id: "U0BOT".to_owned(),
            bot_id: Some("B1".to_owned()),
            team: "AppShapes".to_owned(),
            team_domain: "appshapes.slack.com".to_owned(),
            url: "https://appshapes.slack.com/".to_owned(),
        }
    );
    // What is missing is `undefined` for the user, as `String()` writes it, and empty for the rest.
    assert_eq!(
        read(json!({})),
        AuthTest {
            user_id: "undefined".to_owned(),
            bot_id: None,
            team: String::new(),
            team_domain: String::new(),
            url: String::new(),
        }
    );
    // One scheme and one slash are taken off the URL, and only a scheme in small letters.
    assert_eq!(
        read(json!({ "url": "http://x.slack.com" })).team_domain,
        "x.slack.com"
    );
    assert_eq!(
        read(json!({ "url": "https://x.slack.com//" })).team_domain,
        "x.slack.com/"
    );
    assert_eq!(read(json!({ "url": "HTTPS://x/" })).team_domain, "HTTPS://x");
    assert_eq!(read(json!({ "bot_id": 5 })).bot_id, None);
}

#[test]
fn a_post_answers_its_timestamp_and_channel_and_an_open_the_channels_id() {
    assert_eq!(
        Slack::post_message_answer(200, &ok(json!({ "ts": "9.9", "channel": "C1" }))),
        Ok(Posted {
            ts: "9.9".to_owned(),
            channel: "C1".to_owned(),
        })
    );
    assert_eq!(
        Slack::post_message_answer(200, &ok(json!({ "ts": 9.5 }))),
        Ok(Posted {
            ts: "9.5".to_owned(),
            channel: "undefined".to_owned(),
        })
    );
    assert_eq!(
        Slack::post_ephemeral_answer(200, &ok(json!({ "message_ts": "1" }))),
        Ok(())
    );
    assert_eq!(
        Slack::post_ephemeral_answer(200, r#"{"ok":false,"error":"user_not_in_channel"}"#),
        Err(error(
            "user_not_in_channel",
            "slack: chat.postEphemeral: user_not_in_channel"
        ))
    );
    assert_eq!(
        Slack::open_dm_answer(200, &ok(json!({ "channel": { "id": "D1" } }))),
        Ok("D1".to_owned())
    );
    assert_eq!(
        Slack::open_dm_answer(200, &ok(json!({ "channel": {} }))),
        Ok("undefined".to_owned())
    );
    assert_eq!(
        Slack::open_dm_answer(200, r#"{"ok":false,"error":"user_not_found"}"#),
        Err(error(
            "user_not_found",
            "slack: conversations.open: user_not_found"
        ))
    );
    assert_eq!(Slack::join_channel_answer(200, r#"{"ok":true}"#), Ok(()));
}

fn user(id: &str, name: Option<&str>, email: Option<&str>) -> SlackUser {
    SlackUser {
        id: id.to_owned(),
        name: name.map(str::to_owned),
        email: email.map(str::to_owned),
        is_bot: false,
        deleted: false,
    }
}

#[test]
fn a_user_is_named_by_the_first_of_four_names_that_is_there() {
    let read = |fields: Value| Slack::user_info_answer("U1", 200, &ok(json!({ "user": fields }))).unwrap();
    assert_eq!(
        read(json!({
            "id": "U1",
            "name": "alice",
            "real_name": "Alice A",
            "profile": { "display_name": "Ali", "real_name": "Alice R", "email": "A@X.io" },
        })),
        user("U1", Some("Ali"), Some("a@x.io"))
    );
    assert_eq!(
        read(json!({ "id": "U1", "name": "alice", "real_name": "Alice A", "profile": { "display_name": "", "real_name": "Alice R" } })).name.as_deref(),
        Some("Alice R")
    );
    assert_eq!(
        read(json!({ "id": "U1", "name": "alice", "real_name": "Alice A", "profile": {} }))
            .name
            .as_deref(),
        Some("Alice A")
    );
    assert_eq!(
        read(json!({ "id": "U1", "name": "alice", "profile": null }))
            .name
            .as_deref(),
        Some("alice")
    );
    assert_eq!(read(json!({ "id": "U1", "name": "" })), user("U1", None, None));
    // An empty list is true to JavaScript and empty as text: it is chosen, and it is no name.
    assert_eq!(
        read(json!({ "id": "U1", "name": "alice", "profile": { "display_name": [] } })).name,
        None
    );
    // Who is a bot and who is gone is what JavaScript takes for true.
    let flagged = read(json!({ "id": 5, "is_bot": 1, "deleted": "x", "profile": { "email": 5 } }));
    assert_eq!(
        flagged,
        SlackUser {
            is_bot: true,
            deleted: true,
            ..user("5", None, None)
        }
    );
    assert_eq!(
        read(json!({ "is_bot": 0, "deleted": "" })),
        user("undefined", None, None)
    );
}

#[test]
fn a_lookup_by_email_that_finds_nobody_is_none_and_any_other_refusal_an_error() {
    let found = ok(json!({ "user": { "id": "U1", "name": "alice" } }));
    assert_eq!(
        Slack::lookup_by_email_answer("a@b.io", 200, &found),
        Ok(Some(user("U1", Some("alice"), None)))
    );
    assert_eq!(
        Slack::lookup_by_email_answer("a@b.io", 200, r#"{"ok":false,"error":"users_not_found"}"#),
        Ok(None)
    );
    assert!(Slack::lookup_by_email_answer("a@b.io", 200, r#"{"ok":false,"error":"ratelimited"}"#).is_err());
    assert!(
        Slack::lookup_by_email_answer("a@b.io", 404, r#"{"ok":false,"error":"users_not_found"}"#).is_err()
    );
}

#[test]
fn a_channel_slack_does_not_know_or_may_not_read_is_none() {
    let found = ok(json!({ "channel": { "id": "C1", "name": "qa", "is_private": true, "is_member": 1 } }));
    assert_eq!(
        Slack::channel_info_answer("C1", 200, &found),
        Ok(Some(SlackChannel {
            id: "C1".to_owned(),
            name: Some("qa".to_owned()),
            is_im: false,
            is_private: true,
            is_member: true,
        }))
    );
    for code in ["channel_not_found", "missing_scope"] {
        let refused = json!({ "ok": false, "error": code }).to_string();
        assert_eq!(
            Slack::channel_info_answer("C1", 200, &refused),
            Ok(None),
            "{code}"
        );
    }
    assert!(Slack::channel_info_answer("C1", 200, r#"{"ok":false,"error":"ratelimited"}"#).is_err());
    let unnamed = ok(json!({ "channel": { "id": "D1", "name": 5, "is_im": true } }));
    assert_eq!(
        Slack::channel_info_answer("D1", 200, &unnamed)
            .unwrap()
            .unwrap()
            .name,
        None
    );
}

#[test]
fn a_reaction_that_is_there_already_or_may_not_be_added_is_no_failure() {
    assert_eq!(Slack::add_reaction_answer(200, r#"{"ok":true}"#), Ok(()));
    for code in ["already_reacted", "missing_scope"] {
        let refused = json!({ "ok": false, "error": code }).to_string();
        assert_eq!(Slack::add_reaction_answer(200, &refused), Ok(()), "{code}");
    }
    assert_eq!(
        Slack::add_reaction_answer(200, r#"{"ok":false,"error":"invalid_name"}"#),
        Err(error("invalid_name", "slack: reactions.add: invalid_name"))
    );
    assert!(Slack::add_reaction_answer(500, "").is_err());
}

#[test]
fn a_permalink_is_there_or_it_is_not() {
    assert_eq!(
        Slack::permalink_answer(200, &ok(json!({ "permalink": "https://x.slack.com/p1" }))).as_deref(),
        Some("https://x.slack.com/p1")
    );
    for (status, body) in [
        (200, ok(json!({}))),
        (200, ok(json!({ "permalink": 5 }))),
        (200, r#"{"ok":false,"error":"message_not_found"}"#.to_owned()),
        (500, ok(json!({ "permalink": "https://x.slack.com/p1" }))),
        (200, "nope".to_owned()),
    ] {
        assert_eq!(Slack::permalink_answer(status, &body), None, "{status} {body}");
    }
}

/// A page of `users.list`.
fn users(members: Value, next_cursor: &str) -> String {
    ok(json!({ "members": members, "response_metadata": { "next_cursor": next_cursor } }))
}

/// Drives a search over canned pages; the last page repeats. It answers the URLs asked for, without the
/// API's base, and what the search found.
fn drive_users(name: &str, pages: &[(u16, String)]) -> (Vec<String>, Result<Option<SlackUser>, SlackError>) {
    let slack = slack();
    let mut search = UserSearch::new(name);
    let mut asked = Vec::new();
    while let Some(request) = search.request(&slack) {
        assert_eq!(request.method, "GET");
        assert_eq!(parts(&request).2, [BEARER]);
        let (status, body) = &pages[asked.len().min(pages.len() - 1)];
        asked.push(request.url.strip_prefix(API).unwrap().to_owned());
        match search.answer(*status, body) {
            Ok(None) => {}
            found => return (asked, found),
        }
    }
    (asked, Ok(None))
}

#[test]
fn a_user_search_reads_a_page_at_a_time_until_a_name_matches_or_the_pages_end() {
    let alice = json!({ "id": "U0ALICE00", "name": "alice", "profile": { "display_name": "Ali", "real_name": "Alice A", "real_name_normalized": "ALICE NORMAL" } });
    let bob = json!({ "id": "U0BOB0000", "name": "bob" });
    let pages = [
        (200, users(json!([bob]), "c=2 é")),
        (200, users(json!([alice]), "")),
    ];
    // The second page is asked for with the first one's cursor, escaped.
    let (asked, found) = drive_users("Alice", &pages);
    assert_eq!(
        asked,
        [
            "/users.list?limit=200",
            "/users.list?limit=200&cursor=c%3D2%20%C3%A9"
        ]
    );
    assert_eq!(found, Ok(Some(user("U0ALICE00", Some("Ali"), None))));
    // Any of the five names, in any case, the whole of it.
    for name in ["ALICE", "ali", "alice a", "Alice Normal"] {
        assert!(drive_users(name, &pages).1.unwrap().is_some(), "{name}");
    }
    for name in ["alic", "Alice A.", ""] {
        assert_eq!(drive_users(name, &pages), (asked.clone(), Ok(None)), "{name}");
    }
    // A match on the first page ends the search there.
    assert_eq!(drive_users("bob", &pages).0, ["/users.list?limit=200"]);
    // A page without a cursor is the last.
    assert_eq!(
        drive_users("alice", &[(200, ok(json!({ "members": [bob] })))]),
        (vec!["/users.list?limit=200".to_owned()], Ok(None))
    );
}

#[test]
fn a_user_search_passes_over_bots_and_deactivated_users() {
    let page = users(
        json!([
            { "id": "U0BOT0000", "name": "alice", "is_bot": true },
            { "id": "U0GONE000", "name": "alice", "deleted": true },
            { "id": "U0ALICE00", "name": "Alice" },
        ]),
        "",
    );
    assert_eq!(
        drive_users("alice", &[(200, page)]).1,
        Ok(Some(user("U0ALICE00", Some("Alice"), None)))
    );
}

#[test]
fn a_search_gives_up_after_twenty_pages_and_ends_at_the_first_failure() {
    let more = (200, users(json!([]), "more"));
    let (asked, found) = drive_users("nobody", std::slice::from_ref(&more));
    assert_eq!(asked.len(), MAX_PAGES);
    assert_eq!(asked[1], "/users.list?limit=200&cursor=more");
    assert_eq!(found, Ok(None));
    // A failure is the search's, and it names the page that failed.
    let (asked, found) = drive_users(
        "nobody",
        &[more, (200, r#"{"ok":false,"error":"ratelimited"}"#.to_owned())],
    );
    assert_eq!(asked.len(), 2);
    assert_eq!(
        found,
        Err(error(
            "ratelimited",
            "slack: users.list?limit=200&cursor=more: ratelimited"
        ))
    );
    // A search that failed asks for nothing more.
    let mut search = UserSearch::new("nobody");
    assert!(search.answer(500, "").is_err());
    assert!(search.request(&slack()).is_none());
    // A page whose members cannot be walked is not an answer.
    let (_, found) = drive_users("nobody", &[(200, ok(json!({ "members": [null] })))]);
    assert_eq!(found.unwrap_err().code, UNREADABLE_ANSWER);
}

#[test]
fn a_channel_search_matches_the_name_in_any_case() {
    let slack = slack();
    let list = "/conversations.list?limit=200&types=public_channel,private_channel&exclude_archived=true";
    let mut search = ChannelSearch::new("QA");
    let first = search.request(&slack).unwrap();
    assert_eq!(parts(&first), ("GET", list, vec![BEARER], None));
    let page = ok(json!({
        "channels": [{ "id": "C8", "name": "other" }, { "name": 5 }],
        "response_metadata": { "next_cursor": "n2" },
    }));
    assert_eq!(search.answer(200, &page), Ok(None));
    let second = search.request(&slack).unwrap();
    assert_eq!(parts(&second).1, format!("{list}&cursor=n2"));
    let page = ok(json!({ "channels": [{ "id": "C7", "name": "qa", "is_member": true }] }));
    assert_eq!(
        search.answer(200, &page),
        Ok(Some(SlackChannel {
            id: "C7".to_owned(),
            name: Some("qa".to_owned()),
            is_im: false,
            is_private: false,
            is_member: true,
        }))
    );
    assert!(search.request(&slack).is_none());
    // No such channel: the search ends at the page with no cursor.
    let mut search = ChannelSearch::new("nowhere");
    assert_eq!(search.answer(200, &ok(json!({ "channels": [] }))), Ok(None));
    assert!(search.request(&slack).is_none());
    let mut search = ChannelSearch::new("qa");
    assert_eq!(
        search.answer(200, r#"{"ok":false,"error":"missing_scope"}"#),
        Err(error(
            "missing_scope",
            &format!("slack: {}: missing_scope", &list[1..])
        ))
    );
}

#[test]
fn the_scopes_header_is_a_list_between_commas() {
    let scopes = |header: &str| granted_scopes(Some(header)).unwrap();
    assert_eq!(
        scopes("chat:write,channels:history"),
        ["chat:write", "channels:history"]
    );
    assert_eq!(scopes(" a , b ,, c "), ["a", "b", "c"]);
    assert_eq!(scopes(""), Vec::<String>::new());
    assert_eq!(granted_scopes(None), None);
}
