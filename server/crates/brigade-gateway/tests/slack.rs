//! `supabase/functions/_shared/slack_test.ts`, case for case: the same inputs and the same expected
//! outputs.

use brigade_core::Timestamp;
use brigade_gateway::slack::{SIGNATURE_TOLERANCE_SECONDS, sign_slack};
use brigade_gateway::{
    Direction, Envelope, InboundSlack, Mentions, MessageSource, ParsedRequest, Request, Sender,
    SessionRecord, SlackContext, SlackInbound, SlackOutbound, SlackRenderContext, SlackTarget, SlackThreads,
    TargetSource, TeamSessions, ThreadKind, ThreadLookup, ThreadRow, escape_mrkdwn, find_blocks,
    is_slack_user_id, link_mentions, mention_names, mentions_user, parse_command_request,
    parse_events_request, parse_slack_target, pass_mention_markup, render_slack_text, resolve_slack_inbound,
    resolve_slack_outbound, slack_body, slack_description, slack_roster, strip_mention,
    verify_slack_signature,
};
use serde_json::{Value, json};

const BOT: &str = "U0BOT0000";
const S1: &str = "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2";
const GW: &str = "9c9c9c9c-9c9c-4c9c-8c9c-9c9c9c9c9c9c9";
const M1: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";
const M2: &str = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";

fn at(seconds: i64) -> Timestamp {
    Timestamp(seconds * 1_000_000_000)
}

fn some(text: &str) -> Option<String> {
    Some(text.to_owned())
}

#[test]
fn the_slack_signature_verifies_and_a_tampered_body_or_stale_timestamp_does_not() {
    let secret = "8f742231b10e8888abcd99yyyzzz85a5";
    let body = r#"{"type":"event_callback"}"#;
    let ts = "1700000000";
    let signature = sign_slack(secret, ts, body).unwrap();
    let headers = [
        ("x-slack-request-timestamp", ts),
        ("x-slack-signature", signature.as_str()),
    ];
    let request = Request {
        path: "/",
        headers: &headers,
    };
    let verify = |secret: &str, body: &str, now: i64| {
        verify_slack_signature(secret, &request, body, at(now), SIGNATURE_TOLERANCE_SECONDS)
    };
    assert!(verify(secret, body, 1_700_000_100));
    assert!(!verify(secret, &format!("{body} "), 1_700_000_100));
    assert!(!verify(secret, body, 1_700_009_000));
    assert!(!verify("other", body, 1_700_000_100));
    assert!(!verify("", body, 1_700_000_100));
}

/// An Events API body around one event.
fn callback(event_id: &str, event: &Value) -> String {
    json!({ "type": "event_callback", "event_id": event_id, "event": event }).to_string()
}

#[test]
fn parse_events_request_challenge_a_dm_a_mention_and_the_things_to_ignore() {
    assert_eq!(
        parse_events_request(r#"{"type":"url_verification","challenge":"abc"}"#, Some(BOT)),
        ParsedRequest::UrlVerification {
            challenge: "abc".to_owned()
        }
    );
    let dm = parse_events_request(
        &callback(
            "Ev1",
            &json!({
                "type": "message",
                "channel_type": "im",
                "user": "U1",
                "channel": "D1",
                "ts": "1.1",
                "text": "to: 3f9a2\nhello &amp; bye",
            }),
        ),
        Some(BOT),
    );
    assert_eq!(
        dm,
        ParsedRequest::Event {
            message: InboundSlack {
                event_id: "Ev1".to_owned(),
                source: MessageSource::Message,
                user: "U1".to_owned(),
                channel: "D1".to_owned(),
                channel_type: some("im"),
                ts: some("1.1"),
                thread_ts: None,
                text: "to: 3f9a2\nhello & bye".to_owned(),
                implicit: false,
            }
        }
    );
    let mention = parse_events_request(
        &callback(
            "Ev2",
            &json!({
                "type": "app_mention",
                "user": "U1",
                "channel": "C1",
                "ts": "2.2",
                "thread_ts": "2.0",
                "text": format!("<@{BOT}> to: 3f9a2 please look"),
            }),
        ),
        Some(BOT),
    );
    let ParsedRequest::Event { message } = mention else {
        panic!("expected an event, got {mention:?}");
    };
    assert_eq!(message.text, "to: 3f9a2 please look");
    assert_eq!(message.thread_ts.as_deref(), Some("2.0"));
    assert!(!message.implicit);
    for event in [
        json!({ "type": "message", "channel_type": "im", "user": BOT, "channel": "D1", "ts": "1", "text": "me" }),
        json!({ "type": "message", "channel_type": "im", "bot_id": "B1", "channel": "D1", "ts": "1", "text": "a bot" }),
        json!({
            "type": "message",
            "channel_type": "im",
            "user": "U1",
            "subtype": "message_changed",
            "channel": "D1",
            "ts": "1",
            "text": "edit",
        }),
        json!({ "type": "message", "channel_type": "channel", "user": "U1", "channel": "C1", "ts": "1", "text": "no mention" }),
        json!({ "type": "reaction_added", "user": "U1" }),
    ] {
        let parsed = parse_events_request(&callback("E", &event), Some(BOT));
        assert!(matches!(parsed, ParsedRequest::Ignore { .. }), "{event}");
    }
    assert!(matches!(
        parse_events_request("not json", Some(BOT)),
        ParsedRequest::Ignore { .. }
    ));
}

#[test]
fn parse_events_request_a_channel_reply_without_a_mention_is_implicit_the_rest_of_a_channel_is_ignored() {
    let event = |event: Value, bot: Option<&str>| parse_events_request(&callback("Ev3", &event), bot);
    let reply = event(
        json!({
            "type": "message",
            "channel_type": "channel",
            "user": "U1",
            "channel": "C1",
            "ts": "3.3",
            "thread_ts": "3.0",
            "text": "it passes &amp; ships",
        }),
        Some(BOT),
    );
    assert_eq!(
        reply,
        ParsedRequest::Event {
            message: InboundSlack {
                event_id: "Ev3".to_owned(),
                source: MessageSource::Message,
                user: "U1".to_owned(),
                channel: "C1".to_owned(),
                channel_type: some("channel"),
                ts: some("3.3"),
                thread_ts: some("3.0"),
                text: "it passes & ships".to_owned(),
                implicit: true,
            }
        }
    );
    // A private channel is a channel too; a reply the person also sent to the channel is still their reply.
    let implicit = |parsed: ParsedRequest| match parsed {
        ParsedRequest::Event { message } => message.implicit,
        other => panic!("expected an event, got {other:?}"),
    };
    let in_group = event(
        json!({
            "type": "message",
            "channel_type": "group",
            "user": "U1",
            "channel": "G1",
            "ts": "3.4",
            "thread_ts": "3.0",
            "text": "ok",
        }),
        Some(BOT),
    );
    assert!(implicit(in_group));
    let broadcast = event(
        json!({
            "type": "message",
            "subtype": "thread_broadcast",
            "channel_type": "channel",
            "user": "U1",
            "channel": "C1",
            "ts": "3.5",
            "thread_ts": "3.0",
            "text": "also to the channel",
        }),
        Some(BOT),
    );
    assert!(implicit(broadcast));
    // Not carried: a reply that mentions the bot (app_mention carries it), a top-level message, a thread
    // root, a channel message before the bot's own id is known, and an edit.
    let ignored = |fields: Value, bot: Option<&str>| match event(fields, bot) {
        ParsedRequest::Ignore { reason } => reason,
        other => panic!("expected an ignore, got {other:?}"),
    };
    assert_eq!(
        ignored(
            json!({
                "type": "message",
                "channel_type": "channel",
                "user": "U1",
                "channel": "C1",
                "ts": "3.6",
                "thread_ts": "3.0",
                "text": format!("<@{BOT}> ship it"),
            }),
            Some(BOT)
        ),
        "carried by the mention event"
    );
    assert_eq!(
        ignored(
            json!({
                "type": "message",
                "channel_type": "channel",
                "user": "U1",
                "channel": "C1",
                "ts": "3.7",
                "text": "hello",
            }),
            Some(BOT)
        ),
        "a channel message without a mention"
    );
    assert_eq!(
        ignored(
            json!({
                "type": "message",
                "channel_type": "channel",
                "user": "U1",
                "channel": "C1",
                "ts": "3.0",
                "thread_ts": "3.0",
                "text": "root",
            }),
            Some(BOT)
        ),
        "a channel message without a mention"
    );
    assert_eq!(
        ignored(
            json!({
                "type": "message",
                "channel_type": "channel",
                "user": "U1",
                "channel": "C1",
                "ts": "3.8",
                "thread_ts": "3.0",
                "text": "ok",
            }),
            None
        ),
        "a channel message before the bot's identity is known"
    );
    assert_eq!(
        ignored(
            json!({
                "type": "message",
                "subtype": "message_changed",
                "channel_type": "channel",
                "user": "U1",
                "channel": "C1",
                "ts": "3.9",
                "thread_ts": "3.0",
                "text": "edit",
            }),
            Some(BOT)
        ),
        "not a person's message"
    );
    assert!(mentions_user(&format!("hi <@{BOT}|brigade> there"), BOT));
    assert!(!mentions_user("hi <@U0OTHER00> there", BOT));
}

#[test]
fn parse_command_request_reads_a_slash_commands_form_body() {
    let parsed = parse_command_request(
        "command=%2Fbrigade&text=send+3f9a2+please+retest&user_id=U1&channel_id=C1&trigger_id=T1&response_url=https%3A%2F%2Fhooks",
        || panic!("the command names its trigger"),
    );
    let ParsedRequest::Command { message, command, .. } = parsed else {
        panic!("expected a command, got {parsed:?}");
    };
    assert_eq!(command, "brigade");
    assert_eq!(
        message,
        InboundSlack {
            event_id: "cmd:T1".to_owned(),
            source: MessageSource::Command,
            user: "U1".to_owned(),
            channel: "C1".to_owned(),
            channel_type: None,
            ts: None,
            thread_ts: None,
            text: "send 3f9a2 please retest".to_owned(),
            implicit: false,
        }
    );
    assert!(matches!(
        parse_command_request("x=1", || panic!("nothing to name")),
        ParsedRequest::Ignore { .. }
    ));
}

#[test]
fn strip_mention_and_escape_mrkdwn() {
    assert_eq!(
        strip_mention(&format!("  <@{BOT}|brigade> hi &lt;b&gt; "), Some(BOT)),
        "hi <b>"
    );
    assert_eq!(escape_mrkdwn("a < b & c > d"), "a &lt; b &amp; c &gt; d");
}

#[test]
fn parse_slack_target_reads_every_address_form() {
    let user = |id: &str| Some(SlackTarget::User { id: id.to_owned() });
    let channel = |id: &str| Some(SlackTarget::Channel { id: id.to_owned() });
    assert_eq!(
        parse_slack_target("@alice"),
        Some(SlackTarget::UserName {
            name: "alice".to_owned()
        })
    );
    assert_eq!(
        parse_slack_target("#qa"),
        Some(SlackTarget::ChannelName {
            name: "qa".to_owned()
        })
    );
    assert_eq!(
        parse_slack_target("alice@example.com"),
        Some(SlackTarget::UserEmail {
            email: "alice@example.com".to_owned()
        })
    );
    assert_eq!(parse_slack_target("U0123ABCD"), user("U0123ABCD"));
    assert_eq!(parse_slack_target("C0123ABCD"), channel("C0123ABCD"));
    assert_eq!(parse_slack_target("<@U0123ABCD|alice>"), user("U0123ABCD"));
    assert_eq!(parse_slack_target("<#C0123ABCD|qa>"), channel("C0123ABCD"));
    assert_eq!(parse_slack_target("3f9a2"), None);
    assert_eq!(parse_slack_target("alice"), None);
}

/// A direct message from U1; each test changes what it needs.
fn msg() -> InboundSlack {
    InboundSlack {
        event_id: "Ev1".to_owned(),
        source: MessageSource::Message,
        user: "U1".to_owned(),
        channel: "D1".to_owned(),
        channel_type: some("im"),
        ts: some("10.1"),
        thread_ts: None,
        text: String::new(),
        implicit: false,
    }
}

fn msg_with(text: &str) -> InboundSlack {
    InboundSlack {
        text: text.to_owned(),
        ..msg()
    }
}

struct Threads(Vec<ThreadRow>);

impl SlackThreads for Threads {
    fn by_thread(&self, channel: &str, thread_ts: &str) -> Option<ThreadRow> {
        self.0
            .iter()
            .find(|row| row.address == channel && row.mail_message_id.as_deref() == Some(thread_ts))
            .cloned()
    }
}

impl ThreadLookup for Threads {
    fn by_brigade_id(&self, id: &str) -> Option<ThreadRow> {
        self.0.iter().find(|row| row.brigade_message_id == id).cloned()
    }

    fn by_mail_ids(&self, _ids: &[String]) -> Option<ThreadRow> {
        None
    }
}

fn threads(rows: &[ThreadRow]) -> Threads {
    Threads(rows.to_vec())
}

/// A Slack row for a message in the thread `5.5` of channel C1.
fn row(brigade_message_id: &str, direction: Direction) -> ThreadRow {
    ThreadRow {
        brigade_message_id: brigade_message_id.to_owned(),
        direction,
        session_id: S1.to_owned(),
        address: "C1".to_owned(),
        mail_message_id: some("5.5"),
        provider_mail_id: some("5.5"),
        subject: None,
        kind: Some(ThreadKind::Slack),
        actor: None,
    }
}

fn message(target: &str, reply_to: Option<&str>, text: &str, source: TargetSource) -> SlackInbound {
    SlackInbound::Message {
        target: target.to_owned(),
        reply_to: reply_to.map(str::to_owned),
        summary: None,
        text: text.to_owned(),
        source,
    }
}

#[test]
fn inbound_a_to_line_a_thread_reply_to_a_sessions_post_a_second_message_in_the_persons_own_thread_and_a_command()
 {
    assert_eq!(
        resolve_slack_inbound(&msg_with("to: 3f9a2\nLogin is blank on Safari"), &threads(&[])),
        message("3f9a2", None, "Login is blank on Safari", TargetSource::Block)
    );
    let out_row = row(M1, Direction::Out);
    assert_eq!(
        resolve_slack_inbound(
            &InboundSlack {
                channel: "C1".to_owned(),
                channel_type: some("channel"),
                source: MessageSource::AppMention,
                thread_ts: some("5.5"),
                ..msg_with("ship it")
            },
            &threads(&[out_row]),
        ),
        message(S1, Some(M1), "ship it", TargetSource::Thread)
    );
    let in_row = ThreadRow {
        address: "D1".to_owned(),
        mail_message_id: some("7.7"),
        provider_mail_id: some("Ev0"),
        actor: some("U1"),
        ..row(M2, Direction::In)
    };
    assert_eq!(
        resolve_slack_inbound(
            &InboundSlack {
                thread_ts: some("7.7"),
                ..msg_with("and the console log says 500")
            },
            &threads(&[in_row]),
        ),
        message(S1, None, "and the console log says 500", TargetSource::Thread)
    );
    assert_eq!(
        resolve_slack_inbound(&msg_with("sessions"), &threads(&[])),
        SlackInbound::Command {
            command: "sessions".to_owned(),
            args: String::new(),
            text: "sessions".to_owned(),
        }
    );
    assert_eq!(
        resolve_slack_inbound(&msg_with("hello?"), &threads(&[])),
        SlackInbound::Unaddressed {
            text: "hello?".to_owned()
        }
    );
    assert!(matches!(
        resolve_slack_inbound(&msg_with("[brigade]\nto: a\nto: b\n[/brigade]"), &threads(&[])),
        SlackInbound::Invalid { .. }
    ));
}

#[test]
fn inbound_an_untagged_channel_reply_is_carried_inside_a_brigade_thread_and_ignored_anywhere_else() {
    let out_row = row(M1, Direction::Out);
    let reply = InboundSlack {
        channel: "C1".to_owned(),
        channel_type: some("channel"),
        thread_ts: some("5.5"),
        implicit: true,
        ..msg_with("ship it")
    };
    // Under a session's post: the reply answers that message, exactly as a tagged reply does.
    assert_eq!(
        resolve_slack_inbound(&reply, &threads(std::slice::from_ref(&out_row))),
        message(S1, Some(M1), "ship it", TargetSource::Thread)
    );
    // Under a person's earlier message to a session: a second message to the same session.
    let in_row = ThreadRow {
        actor: some("U1"),
        ..row(M2, Direction::In)
    };
    assert_eq!(
        resolve_slack_inbound(&reply, &threads(&[in_row])),
        message(S1, None, "ship it", TargetSource::Thread)
    );
    // A to: line inside a Brigade thread still wins, as it does for a tagged reply.
    let with_text = |text: &str| InboundSlack {
        text: text.to_owned(),
        ..reply.clone()
    };
    let redirected = resolve_slack_inbound(
        &with_text("to: 3f9a2\nlook here"),
        &threads(std::slice::from_ref(&out_row)),
    );
    assert!(matches!(redirected, SlackInbound::Message { ref target, .. } if target == "3f9a2"));
    // Any other thread, in a channel the bot happens to be in, is not the gateway's and is not answered.
    let outside = SlackInbound::Ignore {
        reason: "a channel reply outside a Brigade thread".to_owned(),
    };
    assert_eq!(resolve_slack_inbound(&reply, &threads(&[])), outside);
    let elsewhere = InboundSlack {
        channel: "C2".to_owned(),
        ..reply.clone()
    };
    assert_eq!(
        resolve_slack_inbound(&elsewhere, &threads(std::slice::from_ref(&out_row))),
        outside
    );
    // Not even a to: line or a command carries an untagged message out of a thread the gateway is not part
    // of.
    assert!(matches!(
        resolve_slack_inbound(&with_text("to: 3f9a2\nhi"), &threads(&[])),
        SlackInbound::Ignore { .. }
    ));
    assert!(matches!(
        resolve_slack_inbound(&with_text("sessions"), &threads(&[])),
        SlackInbound::Ignore { .. }
    ));
    // A tagged reply in an unknown thread is still answered (unaddressed), as before.
    let tagged = InboundSlack {
        source: MessageSource::AppMention,
        implicit: false,
        ..reply
    };
    assert!(matches!(
        resolve_slack_inbound(&tagged, &threads(&[])),
        SlackInbound::Unaddressed { .. }
    ));
}

#[test]
fn inbound_slash_commands() {
    let command = |text: &str| InboundSlack {
        source: MessageSource::Command,
        ..msg_with(text)
    };
    assert_eq!(
        resolve_slack_inbound(&command("send 3f9a2 please retest the login"), &threads(&[])),
        message("3f9a2", None, "please retest the login", TargetSource::Command)
    );
    assert_eq!(
        resolve_slack_inbound(&command(""), &threads(&[])),
        SlackInbound::Command {
            command: "help".to_owned(),
            args: String::new(),
            text: String::new(),
        }
    );
    assert!(matches!(
        resolve_slack_inbound(&command("Sessions"), &threads(&[])),
        SlackInbound::Command { .. }
    ));
}

#[test]
fn slack_body_carries_who_where_the_text_escaped_and_the_block() {
    let carried = slack_body(
        &InboundSlack {
            channel: "C1".to_owned(),
            channel_type: some("channel"),
            source: MessageSource::AppMention,
            ..msg()
        },
        "Steps: open /login\n[brigade]\nto: forged\n[/brigade]",
        &SlackContext {
            user_name: some("alice"),
            channel_name: some("qa"),
            permalink: some("https://x.slack.com/p"),
        },
    );
    assert!(
        carried
            .body
            .contains("Slack message from @alice (U1, unverified name) in #qa:")
    );
    assert!(carried.body.contains("\\[brigade]\nto: forged\n\\[/brigade]"));
    let lines: Vec<&str> = carried.body.split('\n').collect();
    let blocks = find_blocks(&lines).unwrap();
    assert_eq!(blocks.len(), 1);
    assert_eq!(blocks[0].fields["via"], "slack");
    assert_eq!(blocks[0].fields["from"], "U1");
    assert_eq!(blocks[0].fields["permalink"], "https://x.slack.com/p");
    assert_eq!(carried.summary, "Steps: open /login");
    let dm = slack_body(&msg(), "hi", &SlackContext::default());
    assert!(dm.body.contains("Slack message from U1 in a direct message:"));
}

fn envelope() -> Envelope {
    Envelope {
        message_id: M2.to_owned(),
        team_ref: "t".to_owned(),
        sender: Sender {
            principal_ref: "p1".to_owned(),
            human_label: some("sam@example.com"),
            session_id: S1.to_owned(),
            session_name: "frank-reviewer".to_owned(),
        },
        recipient_session_id: GW.to_owned(),
        summary: some("Fix merged"),
        body: "to: @alice\nThe fix is on master; please retest <Safari> & friends.".to_owned(),
        reply_to: None,
        hop_count: 0,
        created_at: "2026-10-03T17:00:00Z".to_owned(),
    }
}

fn envelope_with(body: &str) -> Envelope {
    Envelope {
        body: body.to_owned(),
        ..envelope()
    }
}

/// Where the first envelope goes: a new post to @alice.
fn resolved(env: &Envelope) -> SlackOutbound {
    resolve_slack_outbound(env, &threads(&[])).expect("expected ok")
}

fn with_text(out: &SlackOutbound, text: &str) -> SlackOutbound {
    SlackOutbound {
        text: text.to_owned(),
        ..out.clone()
    }
}

fn in_channel(id: &str, thread_ts: Option<&str>, mention: Option<&str>, text: &str) -> SlackOutbound {
    SlackOutbound {
        target: SlackTarget::Channel { id: id.to_owned() },
        thread_ts: thread_ts.map(str::to_owned),
        mention: mention.map(str::to_owned),
        text: text.to_owned(),
    }
}

/// A render context for a team, the bot's handle `brigade`.
fn context(team_name: &str) -> SlackRenderContext {
    SlackRenderContext {
        team_name: team_name.to_owned(),
        bot_handle: "brigade".to_owned(),
        ..SlackRenderContext::default()
    }
}

#[test]
fn outbound_to_forms_a_reply_into_the_persons_thread_and_no_address() {
    assert_eq!(
        resolve_slack_outbound(&envelope(), &threads(&[])),
        Ok(SlackOutbound {
            target: SlackTarget::UserName {
                name: "alice".to_owned()
            },
            thread_ts: None,
            mention: None,
            text: "The fix is on master; please retest <Safari> & friends.".to_owned(),
        })
    );
    let in_row = ThreadRow {
        provider_mail_id: some("Ev9"),
        subject: some("s"),
        actor: some("U1"),
        ..row(M1, Direction::In)
    };
    let reply = |body: &str| Envelope {
        reply_to: some(M1),
        ..envelope_with(body)
    };
    assert_eq!(
        resolve_slack_outbound(
            &reply("Retested, passes."),
            &threads(std::slice::from_ref(&in_row))
        ),
        Ok(in_channel("C1", Some("5.5"), Some("U1"), "Retested, passes."))
    );
    let mail_row = ThreadRow {
        kind: Some(ThreadKind::Email),
        address: "a@b.io".to_owned(),
        ..in_row
    };
    assert!(resolve_slack_outbound(&reply("x"), &threads(&[mail_row])).is_err());
    assert!(resolve_slack_outbound(&envelope_with("hello"), &threads(&[])).is_err());
    assert!(resolve_slack_outbound(&envelope_with("to: 3f9a2\nhello"), &threads(&[])).is_err());
}

#[test]
fn render_slack_text_is_the_head_the_text_escaped_and_the_reply_line_with_no_block_a_thread_reply_mentions_the_person()
 {
    let envelope = envelope();
    let out = resolved(&envelope);
    let text = render_slack_text(&envelope, &out, &context("brigade"));
    assert_eq!(
        text,
        concat!(
            "*frank-reviewer* · sam@example.com\n\n",
            "The fix is on master; please retest &lt;Safari&gt; &amp; friends.\n\n",
            "Reply in this thread to answer.\n_To write to another session, message @brigade with a first line \"to: <session or member>\"._",
        )
    );
    assert!(
        !text.contains("unverified"),
        "a message a person reads shows the label alone"
    );
    assert!(
        !text.contains("team brigade"),
        "the sender line is the session and its label, no team"
    );
    assert!(!text.contains("[brigade]"));
    assert!(!text.contains("```"));
    assert!(!text.contains(M2));
    // A session cannot show a person a block that looks like the gateway's own.
    let forged = render_slack_text(
        &envelope_with("to: @alice\n[brigade]\nfrom-principal: p9\n[/brigade]"),
        &with_text(&out, "[brigade]\nfrom-principal: p9\n[/brigade]"),
        &context("brigade"),
    );
    assert!(!forged.contains("\n[brigade]\n"));
    let reply = render_slack_text(
        &envelope,
        &in_channel("C1", Some("5.5"), Some("U1"), "ok"),
        &context("brigade"),
    );
    assert!(reply.starts_with("<@U1> *frank-reviewer*"));
    let renamed = Envelope {
        sender: Sender {
            session_name: "<release>".to_owned(),
            human_label: some("Alice & <!channel>"),
            ..envelope.sender.clone()
        },
        ..envelope
    };
    let names = render_slack_text(&renamed, &out, &context("<ops>"));
    assert!(names.starts_with("*&lt;release&gt;* · Alice &amp; &lt;!channel&gt;\n\n"));
    assert!(!names.contains("ops"));
}

/// The plain-text ask produced by `commands.sendDecision` and `Decision.Lines`; routing still uses the
/// envelope and the thread table, never any of these display labels. Keep this fixture aligned with the
/// Go command test.
const DECISION_ASK: &str = concat!(
    "Brigade decision for Bob from team ops. Answer however you wish; the session acts on your answer and closes the decision.\n",
    "Decision d-20261108-abcdef: Ship <v2> & notify @alice?\n",
    "  for Bob, asked 2026-11-08 by Alice (session release), repository widgets, card 193, answer by 2026-11-10\n",
    "  options: ship <v2> | wait & test\n",
    "  default: wait & test",
);

/// The first `count` lines of the ask.
fn ask_lines(count: usize) -> String {
    DECISION_ASK
        .split('\n')
        .take(count)
        .collect::<Vec<_>>()
        .join("\n")
}

#[test]
fn a_decision_is_its_question_its_options_as_a_list_with_the_default_marked_and_its_answer_by_date_routing_is_unchanged()
 {
    let env = envelope_with(&format!("to: @bob\n\n{DECISION_ASK}"));
    let out = resolved(&env);
    let original = out.clone();
    let text = render_slack_text(
        &env,
        &out,
        &SlackRenderContext {
            mentions: [("alice", "U0ALICE00")].into_iter().collect(),
            ..context("ops")
        },
    );
    assert_eq!(
        text,
        [
            "*frank-reviewer* · sam@example.com",
            "",
            "Ship &lt;v2&gt; &amp; notify <@U0ALICE00>?",
            "",
            "- ship &lt;v2&gt;",
            "- [default] wait &amp; test",
            "",
            "Answer by 2026-11-10.",
            "",
            "Reply in this thread to answer.",
            "_To write to another session, message @brigade with a first line \"to: <session or member>\"._",
        ]
        .join("\n")
    );
    assert!(
        !text.contains("unverified"),
        "a decision shows its sender's label alone"
    );
    assert!(!text.contains("recommend"));
    // Owner ruling 2026-11-09 (card 193): the preamble, the decision id and the for/asked line are noise
    // to the person.
    for gone in [
        "Brigade decision for",
        "d-20261108-abcdef",
        "asked 2026-11-08",
        "repository widgets",
        "card 193",
    ] {
        assert!(!text.contains(gone), "{gone}");
    }
    assert_eq!(
        out, original,
        "presentation must not mutate the resolved route or source text"
    );
    assert_eq!(env.body, format!("to: @bob\n\n{DECISION_ASK}"));
    // A reply still uses the stored thread and actor, even if the message looks like a decision.
    let reply = render_slack_text(
        &env,
        &SlackOutbound {
            target: SlackTarget::Channel { id: "C1".to_owned() },
            thread_ts: some("5.5"),
            mention: some("U1"),
            ..out
        },
        &context("ops"),
    );
    assert!(reply.starts_with("<@U1> *frank-reviewer*"));
    assert!(reply.contains("\n\nReply in this thread and mention @brigade to answer.\n"));
}

#[test]
fn minimal_decisions_omit_absent_fields_unfamiliar_asks_fall_back_intact() {
    let envelope = envelope();
    let out = resolved(&envelope);
    let minimal = ask_lines(3);
    let render_for =
        |text: &str, team: &str| render_slack_text(&envelope, &with_text(&out, text), &context(team));
    let render = |text: &str| render_for(text, "ops");
    let text = render(&minimal);
    assert!(text.contains(
        "\n\nShip &lt;v2&gt; &amp; notify @alice?\n\nAnswer by 2026-11-10.\n\nReply in this thread"
    ));
    assert!(!text.contains("\n- "));
    assert!(!text.contains("[default]"));
    // No answer-by date, no line for one: the question stands alone above the reply line.
    let undated = render(&minimal.replace(", answer by 2026-11-10", ""));
    assert!(undated.contains("\n\nShip &lt;v2&gt; &amp; notify @alice?\n\nReply in this thread"));
    assert!(!undated.contains("Answer by"));
    let options_only = render(&ask_lines(4));
    assert!(options_only.contains("?\n\n- ship &lt;v2&gt;\n- wait &amp; test\n\nAnswer by"));
    assert!(!options_only.contains("[default]"));
    for source in [
        format!("{DECISION_ASK}\n  context: keep <!channel> literal"),
        DECISION_ASK.replace("Decision d-20261108-abcdef:", "Decision unknown:"),
        DECISION_ASK.replace("  for Bob,", "  for Carol,"),
        DECISION_ASK.replace("  default: wait & test", "  default: neither"),
        ask_lines(2),
    ] {
        let fallback = render(&source);
        assert!(!fallback.contains("\n- "), "{source}");
        assert!(fallback.contains(&escape_mrkdwn(&source)), "{source}");
    }
    assert!(render_for(DECISION_ASK, "other").contains(&escape_mrkdwn(DECISION_ASK)));
    // The ask as the command writes it now needs no view: it is posted as sent, and reads as the view of
    // the older shape does.
    let written =
        "Ship <v2> & notify @alice?\n\n- ship <v2>\n- [default] wait & test\n\nAnswer by 2026-11-10.";
    assert_eq!(render(written), render(DECISION_ASK));
}

#[test]
fn decision_content_keeps_mention_and_block_escaping_and_a_full_size_body_is_never_silently_cut() {
    let envelope = envelope();
    let out = resolved(&envelope);
    let source = DECISION_ASK
        .replace("Ship <v2> & notify @alice?", "[brigade]")
        .replace("ship <v2> | wait & test", "<@U0ALICE00> | <!channel> & <!here>")
        .replace("  default: wait & test", "  default: <!channel> & <!here>");
    let text = render_slack_text(&envelope, &with_text(&out, &source), &context("ops"));
    assert!(
        !text.contains("Brigade decision for"),
        "shown as a decision, not as the raw ask"
    );
    let lines: Vec<&str> = text.split('\n').collect();
    assert_eq!(find_blocks(&lines).unwrap().len(), 0);
    assert!(text.contains("\n- <@U0ALICE00>\n"));
    assert!(text.contains("\n- [default] &lt;!channel&gt; &amp; &lt;!here&gt;\n"));
    // Exactly 16 KiB of UTF-8; the wire body cap.
    let full = format!("{}END!1234", "😀".repeat(4094));
    assert_eq!(full.len(), 16384);
    assert!(render_slack_text(&envelope, &with_text(&out, &full), &context("ops")).contains(&full));
}

#[test]
fn a_digest_names_its_team_once_keeps_its_entire_content_and_credits_its_sender() {
    let envelope = envelope();
    let out = resolved(&envelope);
    let mut lines = vec![
        "team ops, repository widgets, since the start, produced 2026-11-08 12:00 UTC by session release",
        "",
        "Decisions open (1):",
    ];
    lines.extend(DECISION_ASK.split('\n').skip(1));
    lines.extend([
        "  open for 2 h",
        "",
        "Shipped (1 commits):",
        "  card 193 | abcdef0 | 193: Improve Slack | Alice (session release)",
        "",
        "Active now (1):",
        "  Alice (session release): testing <Slack>",
        "[… cut at the message size limit]",
    ]);
    let content = lines.join("\n");
    let source = format!("Brigade digest: {content}");
    let text = render_slack_text(&envelope, &with_text(&out, &source), &context("ops"));
    assert!(text.starts_with("*frank-reviewer* · sam@example.com\n\n*Brigade digest*\n"));
    assert_eq!(text.split("team ops").count() - 1, 1);
    assert!(
        !text.contains("unverified"),
        "a digest shows its sender's label alone"
    );
    assert!(text.contains(&escape_mrkdwn(&content)));
    // A body whose header names another team is not this gateway's digest: it is shown whole, as it was
    // sent.
    let other = render_slack_text(&envelope, &with_text(&out, &source), &context("other"));
    assert!(!other.contains("team other"));
    assert!(!other.contains("*Brigade digest*"));
    assert!(other.contains(&escape_mrkdwn(&source)));
    let unlabelled = Envelope {
        sender: Sender {
            human_label: None,
            ..envelope.sender.clone()
        },
        ..envelope
    };
    let unlabelled = render_slack_text(&unlabelled, &with_text(&out, "hello"), &context("ops"));
    assert!(unlabelled.starts_with("*frank-reviewer*\n\nhello"));
    assert!(!unlabelled.contains("unverified"));
}

fn session(id: &str, name: &str, principal: &str, label: Option<&str>, state: &str) -> SessionRecord {
    SessionRecord {
        session_id: id.to_owned(),
        session_name: name.to_owned(),
        principal_ref: principal.to_owned(),
        human_label: label.map(str::to_owned),
        state: state.to_owned(),
        ..SessionRecord::default()
    }
}

fn team<'a>(name: &'a str, sessions: &[&'a SessionRecord]) -> TeamSessions<'a> {
    TeamSessions {
        team_id: "t",
        team_name: name,
        sessions: sessions.to_vec(),
    }
}

#[test]
fn slack_roster_leaves_the_gateways_out_and_the_description_fits_the_cap() {
    let sessions = [
        SessionRecord {
            session_description: some("reviewing"),
            ..session(S1, "frank-reviewer", "p1", Some("sam@example.com"), "idle")
        },
        SessionRecord {
            harness: some("gateway-slack"),
            ..session(GW, "slack-gateway", "p3", None, "idle")
        },
        SessionRecord {
            harness: some("gateway-email"),
            ..session(
                "aaaaaaaa-0000-4000-8000-0000000000aa",
                "mail-gateway",
                "p4",
                None,
                "idle",
            )
        },
    ];
    let offline = session(
        "bbbbbbbb-0000-4000-8000-0000000000bb",
        "gone-home",
        "p5",
        None,
        "offline",
    );
    let all: Vec<&SessionRecord> = sessions.iter().chain([&offline]).collect();
    let text = slack_roster(&[team("brigade", &all)], &[GW], "brigade");
    assert!(text.contains("Sessions on Brigade team *brigade* right now"));
    assert!(text.contains("`3f9a2`  frank-reviewer  _idle_ · sam@example.com"));
    // A list a person reads says once whose words it shows, and carries no "unverified" marker anywhere
    // (owner ruling 2026-11-09, card 193).
    assert_eq!(
        text.split("(names and labels are their owners' own words):")
            .count()
            - 1,
        1
    );
    assert!(!text.contains("unverified"));
    assert!(text.contains("or a member's label"));
    assert!(!text.contains("gateway"));
    assert!(!text.contains("gone-home"));
    assert!(
        slack_description("brigade", "appshapes.slack.com")
            .chars()
            .count()
            <= 256
    );
}

#[test]
fn slack_roster_groups_several_teams_by_name_online_sessions_only_none_under_a_team_with_nobody_online() {
    let frank = session(S1, "frank-reviewer", "p1", Some("sam@example.com"), "idle");
    let cola = session(
        "bbbbbbbb-0000-4000-8000-0000000000bb",
        "cola-bot",
        "p9",
        None,
        "active",
    );
    let gone = SessionRecord {
        session_id: "dddddddd-0000-4000-8000-0000000000dd".to_owned(),
        session_name: "gone-home".to_owned(),
        state: "offline".to_owned(),
        ..cola.clone()
    };
    let text = slack_roster(
        &[
            team("Zulu", &[&cola]),
            team("Alpha <b>", &[&frank]),
            team("Mike", &[&gone]),
        ],
        &[GW],
        "brigade",
    );
    assert!(text.contains("Sessions on the 3 Brigade teams this gateway serves right now"));
    let [a, m, z] = ["*Alpha &lt;b&gt;*", "*Mike*", "*Zulu*"].map(|heading| text.find(heading));
    assert!(a.is_some() && a < m && m < z, "teams in name order");
    assert!(text.contains("*Alpha &lt;b&gt;*\n• `3f9a2`  frank-reviewer  _idle_ · sam@example.com"));
    assert!(text.contains("*Mike*\n  (none)"));
    assert!(text.contains("*Zulu*\n• `000bb`  cola-bot  _active_"));
    // Across several teams too: one note in the header, no "unverified" marker anywhere.
    assert!(text.contains("(names and labels are their owners' own words):"));
    assert!(!text.contains("unverified"));
    assert!(!text.contains("gone-home"));
    assert!(text.contains("To write to one: message @brigade"));
}

#[test]
fn a_mention_or_dm_that_says_only_sessions_with_or_without_brigade_in_front_is_the_command() {
    let none = threads(&[]);
    let base = |text: &str| InboundSlack {
        event_id: "Ev1".to_owned(),
        source: MessageSource::AppMention,
        user: "U1".to_owned(),
        channel: "C1".to_owned(),
        channel_type: some("channel"),
        ts: some("1.1"),
        thread_ts: None,
        text: text.to_owned(),
        implicit: false,
    };
    let in_channel = resolve_slack_inbound(&base("sessions"), &none);
    assert!(matches!(in_channel, SlackInbound::Command { ref command, .. } if command == "sessions"));
    let prefixed = resolve_slack_inbound(&base("Brigade help"), &none);
    assert!(matches!(prefixed, SlackInbound::Command { ref command, .. } if command == "help"));
    let prose = resolve_slack_inbound(&base("sessions are down?"), &none);
    assert!(matches!(prose, SlackInbound::Unaddressed { .. }));
}

/// An envelope with no summary, as two of the TypeScript's cases build theirs.
fn bare_envelope(label: Option<&str>, created_at: &str) -> Envelope {
    Envelope {
        message_id: M2.to_owned(),
        team_ref: "t".to_owned(),
        sender: Sender {
            principal_ref: "p1".to_owned(),
            human_label: label.map(str::to_owned),
            session_id: S1.to_owned(),
            session_name: "frank-reviewer".to_owned(),
        },
        recipient_session_id: GW.to_owned(),
        summary: None,
        body: "ok".to_owned(),
        reply_to: None,
        hop_count: 0,
        created_at: created_at.to_owned(),
    }
}

#[test]
fn the_footer_tells_a_channel_reader_to_mention_the_bot_and_a_direct_message_reader_not_to() {
    let env = bare_envelope(Some("sam@example.com"), "2026-10-04T12:00:00Z");
    let context = context("brigade");
    let channel = render_slack_text(&env, &in_channel("C123", None, None, "ok"), &context);
    assert!(channel.contains("mention @brigade to answer"));
    let dm = render_slack_text(&env, &in_channel("D123", Some("1.2"), Some("U1"), "ok"), &context);
    assert!(!dm.contains("mention @brigade to answer"));
    assert!(dm.contains("Reply in this thread to answer."));
    let by_name = render_slack_text(
        &env,
        &SlackOutbound {
            target: SlackTarget::UserName {
                name: "alice".to_owned(),
            },
            thread_ts: None,
            mention: None,
            text: "ok".to_owned(),
        },
        &context,
    );
    assert!(by_name.contains("Reply in this thread to answer."));
    // Once the installed app hears channel threads, a channel reader is not asked to mention the bot
    // either.
    let heard = render_slack_text(
        &env,
        &in_channel("C123", None, None, "ok"),
        &SlackRenderContext {
            hears_channel_threads: true,
            ..context
        },
    );
    assert!(!heard.contains("mention @brigade to answer"));
    assert!(heard.contains("Reply in this thread to answer."));
}

#[test]
fn a_session_tags_a_person_slacks_own_markup_passes_handles_the_gateway_resolved_become_mentions_broadcasts_do_not()
 {
    let env = Envelope {
        body: "x".to_owned(),
        ..bare_envelope(None, "2026-10-16T12:00:00Z")
    };
    let render = |text: &str, mentions: &[(&str, &str)]| {
        render_slack_text(
            &env,
            &in_channel("C1", None, None, text),
            &SlackRenderContext {
                mentions: mentions.iter().copied().collect(),
                ..context("brigade")
            },
        )
    };
    // 1. Slack's markup with the member id, with or without a label, and a channel; a stray < is still
    // escaped.
    let markup = render(
        "<@UGLP0XL9RD> and <@W012345678|alicia>: see <#C0123ABCD|qa>; 1 < 2",
        &[],
    );
    assert!(markup.contains("<@UGLP0XL9RD> and <@W012345678|alicia>: see <#C0123ABCD|qa>; 1 &lt; 2"));
    // 2. A handle the gateway resolved, however punctuated, as written; another spelling is another
    // handle.
    let linked = render(
        "@alicia, @Alicia. and @alicia.smith: @alicia-x @alicia",
        &[("alicia", "U0ALICIA00")],
    );
    assert!(linked.contains("<@U0ALICIA00>, @Alicia. and @alicia.smith: @alicia-x <@U0ALICIA00>"));
    // 3. The broadcast forms, user groups and links stay literal; so does an email address and a bare
    // name.
    let literal = render(
        "<!channel> <!here> <!subteam^S123> <https://x.io|x> mail sam@example.com, @nobody",
        &[],
    );
    assert!(literal.contains(
        "&lt;!channel&gt; &lt;!here&gt; &lt;!subteam^S123&gt; &lt;https://x.io|x&gt; mail sam@example.com, @nobody"
    ));
    // The footer's own @brigade is never linked.
    assert!(!render("hi", &[("brigade", "U0BOT00000")]).contains("<@U0BOT00000>"));
}

#[test]
fn mention_names_is_slack_user_id_pass_mention_markup_and_link_mentions() {
    assert_eq!(
        mention_names(
            "@alicia, then @Bob.Smith. ask @UGLP0XL9RD; not sam@example.com nor <@U0BOT0000> or @ or @-x"
        ),
        ["alicia", "Bob.Smith", "UGLP0XL9RD"]
    );
    assert_eq!(mention_names("to: @alice\n@alice again"), ["alice"]);
    assert!(is_slack_user_id("UGLP0XL9RD"));
    assert!(is_slack_user_id("W012345678"));
    assert!(!is_slack_user_id("alicia"));
    assert!(!is_slack_user_id("U1"));
    assert_eq!(
        pass_mention_markup("&lt;@U1234567&gt; &lt;#C1234567|qa&gt; &lt;!here&gt; &lt;@bad&gt;"),
        "<@U1234567> <#C1234567|qa> &lt;!here&gt; &lt;@bad&gt;"
    );
    let ids = |pairs: &[(&str, &str)]| -> Mentions { pairs.iter().copied().collect() };
    assert_eq!(
        link_mentions(
            "@a.b and @a",
            &ids(&[("a.b", "U0000000A"), ("a", "U0000000B"), ("bad", "not-an-id")])
        ),
        "<@U0000000A> and <@U0000000B>"
    );
    assert_eq!(
        link_mentions(
            "x@a and <@U0000000B>",
            &ids(&[("a", "U0000000A"), ("U0000000B", "U0000000B")])
        ),
        "x@a and <@U0000000B>"
    );
}
