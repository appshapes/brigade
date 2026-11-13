//! What `slack-in` and `slack-out` say and decide between their calls
//! (`supabase/functions/slack-in/index.ts`, `supabase/functions/slack-out/index.ts`). The TypeScript has no
//! tests of its handlers; these are the strings and the decisions the two handlers hold, read off their
//! source.

use brigade_gateway::notes::slack::{
    CHANNEL_HISTORY_SCOPE, DEFAULT_BOT_HANDLE, DEFAULT_WORKSPACE, NOT_DELIVERED_SUMMARY, NOT_INSTALLED,
    REACTION, Tell, answer_carried, answer_challenge, answer_command, answer_for, answer_ignored,
    answer_told, answer_unsigned, challenge, command, confirmation, confirmation_among, describe_target,
    hears_channel_threads, invalid, is_final, joins_first, mention_key, no_gateway, no_match, not_connected,
    not_posted, not_sent, parse_request, post, received_key, roster_of_connected, send, sent_note, tell,
    thread_row_in, thread_row_out, unaddressed, undeliverable, wants_channel_name,
};
use brigade_gateway::notes::{
    INBOX_BATCH, Sending, already_carried, answer_duplicate, answer_failed, answer_method_not_allowed,
    not_delivered_key, refused,
};
use brigade_gateway::providers::slack::{Metadata, PostEphemeral, PostMessage, Posted, SlackError};
use brigade_gateway::{
    Answer, BrigadeBody, Direction, Envelope, InboundSlack, MessageSource, ParsedRequest, Sender,
    SessionRecord, SlackInbound, SlackOutbound, SlackTarget, TargetSource, TeamSessions, ThreadKind,
    ThreadRow, resolve_recipient_across,
};

const S1: &str = "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2";
const S2: &str = "7a7a7a7a-7a7a-4a7a-8a7a-7a7a7a7a7b1c4";
const S3: &str = "cccccccc-0000-4000-8000-0000000000c3";
const GW: &str = "9c9c9c9c-9c9c-4c9c-8c9c-9c9c9c9c9c9c9";
const M1: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";
const M2: &str = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";

fn some(text: &str) -> Option<String> {
    Some(text.to_owned())
}

fn answer(status: u16, body: &str) -> Answer {
    Answer {
        status,
        body: body.to_owned(),
    }
}

/// A mention of the bot in the thread `5.5` of channel C1.
fn mention() -> InboundSlack {
    InboundSlack {
        event_id: "Ev1".to_owned(),
        source: MessageSource::AppMention,
        user: "U1".to_owned(),
        channel: "C1".to_owned(),
        channel_type: None,
        ts: some("6.6"),
        thread_ts: some("5.5"),
        text: "ship it".to_owned(),
        implicit: false,
    }
}

/// A direct message, in no thread.
fn direct() -> InboundSlack {
    InboundSlack {
        channel: "D1".to_owned(),
        channel_type: some("im"),
        source: MessageSource::Message,
        ts: some("10.1"),
        thread_ts: None,
        ..mention()
    }
}

fn slash() -> InboundSlack {
    InboundSlack {
        event_id: "cmd:T1".to_owned(),
        source: MessageSource::Command,
        ts: None,
        thread_ts: None,
        ..mention()
    }
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
        body: "to: @alice\nThe fix is on master.".to_owned(),
        reply_to: None,
        hop_count: 0,
        created_at: "2026-10-03T17:00:00Z".to_owned(),
    }
}

// ---- what the two gateways say alike -----------------------------------------------------------------------

#[test]
fn a_refusal_names_its_code_and_what_the_code_adds() {
    assert_eq!(
        refused("rate_limited", "slow down"),
        "Brigade refused the message (rate_limited: slow down). Try again later."
    );
    assert_eq!(
        refused("forbidden", ""),
        "Brigade refused the message (forbidden). Try again later."
    );
}

#[test]
fn a_send_is_tried_once_more_unthreaded_only_when_the_message_it_answers_is_gone() {
    let threaded = Sending {
        recipient_session_id: S1.to_owned(),
        body: "b".to_owned(),
        summary: "s".to_owned(),
        reply_to: some(M1),
        idempotency_key: "slack:Ev1".to_owned(),
    };
    assert!(threaded.sends_again_unthreaded("not_found"));
    assert!(!threaded.sends_again_unthreaded("rate_limited"));
    assert!(!threaded.sends_again_unthreaded("internal"));
    let again = threaded.unthreaded();
    assert_eq!(
        again,
        Sending {
            reply_to: None,
            ..threaded.clone()
        }
    );
    // A message that answers none, or names the empty string, has nothing to drop.
    assert!(!again.sends_again_unthreaded("not_found"));
    let empty = Sending {
        reply_to: some(""),
        ..threaded
    };
    assert!(!empty.sends_again_unthreaded("not_found"));
}

#[test]
fn a_message_whose_row_says_out_was_carried_already() {
    let row = |direction| ThreadRow {
        brigade_message_id: M1.to_owned(),
        direction,
        session_id: S1.to_owned(),
        address: "C1".to_owned(),
        mail_message_id: None,
        provider_mail_id: None,
        subject: None,
        kind: Some(ThreadKind::Slack),
        actor: None,
    };
    assert!(already_carried(Some(&row(Direction::Out))));
    assert!(!already_carried(Some(&row(Direction::In))));
    assert!(!already_carried(None));
    assert_eq!(not_delivered_key(M1), format!("gw-err:{M1}"));
    assert_eq!(INBOX_BATCH, 50);
}

#[test]
fn the_answers_both_endpoints_give() {
    assert_eq!(
        answer_method_not_allowed(),
        answer(405, r#"{"error":"method not allowed"}"#)
    );
    assert_eq!(answer_duplicate(), answer(200, r#"{"duplicate":true}"#));
    assert_eq!(
        answer_failed("db \"down\""),
        answer(500, r#"{"error":"db \"down\""}"#)
    );
}

// ---- slack-in: the request ---------------------------------------------------------------------------------

#[test]
fn the_challenge_is_read_before_the_signature_and_only_from_json() {
    let body = r#"{"type":"url_verification","challenge":"abc123"}"#;
    assert_eq!(challenge("application/json", body).as_deref(), Some("abc123"));
    assert_eq!(
        challenge("application/json; charset=utf-8", body).as_deref(),
        Some("abc123")
    );
    assert_eq!(challenge("text/plain", body), None);
    assert_eq!(challenge("", body), None);
    for not_one in [
        r#"{"type":"url_verification","challenge":5}"#,
        r#"{"type":"url_verification"}"#,
        r#"{"type":"event_callback","challenge":"abc"}"#,
        "null",
        "not json",
        "",
    ] {
        assert_eq!(challenge("application/json", not_one), None, "{not_one}");
    }
    assert_eq!(
        answer_challenge("abc123"),
        answer(200, r#"{"challenge":"abc123"}"#)
    );
}

#[test]
fn a_form_is_a_slash_command_and_anything_else_an_event() {
    let form = "command=%2Fbrigade&text=sessions&user_id=U1&channel_id=C1";
    let parsed = parse_request(
        "application/x-www-form-urlencoded; charset=utf-8",
        form,
        Some("U0BOT0000"),
        || "drawn".to_owned(),
    );
    let ParsedRequest::Command { message, command, .. } = parsed else {
        panic!("expected a command, got {parsed:?}");
    };
    assert_eq!(command, "brigade");
    // Slack named no trigger, so the caller's stands in for it.
    assert_eq!(message.event_id, "cmd:drawn");
    // The same body without the form's content type is read as an event, and is not JSON.
    assert_eq!(
        parse_request("application/json", form, Some("U0BOT0000"), || panic!(
            "not asked"
        )),
        ParsedRequest::Ignore {
            reason: "not json".to_owned()
        }
    );
    assert_eq!(
        parse_request("", r#"{"type":"other"}"#, None, || panic!("not asked")),
        ParsedRequest::Ignore {
            reason: "type other".to_owned()
        }
    );
}

#[test]
fn the_answers_slack_is_given() {
    assert_eq!(received_key("Ev1"), "slack:Ev1");
    assert_eq!(received_key("cmd:T1"), "slack:cmd:T1");
    assert_eq!(
        answer_unsigned(),
        answer(
            401,
            r#"{"error":"the request is not signed by this gateway's Slack app"}"#
        )
    );
    assert_eq!(
        answer_ignored("a channel reply outside a Brigade thread"),
        answer(200, r#"{"ignored":"a channel reply outside a Brigade thread"}"#)
    );
    assert_eq!(answer_told(), answer(200, r#"{"told":true}"#));
    assert_eq!(
        answer_carried(M1, S1, "ops", TargetSource::Thread, false),
        answer(
            200,
            &format!(
                r#"{{"message_id":"{M1}","to":"{S1}","team":"ops","source":"thread","duplicate":false}}"#
            )
        )
    );
    assert_eq!(
        answer_command("the *roster*"),
        answer(200, r#"{"response_type":"ephemeral","text":"the *roster*"}"#)
    );
    assert_eq!(
        NOT_INSTALLED,
        "the slack gateway is not installed for any team of this project: run its install target"
    );
    assert_eq!(no_gateway("ops"), "team ops has no slack gateway row");
    assert_eq!((DEFAULT_BOT_HANDLE, DEFAULT_WORKSPACE), ("brigade", "slack"));
}

// ---- slack-in: what a person is told -----------------------------------------------------------------------

#[test]
fn what_a_person_is_told_about_a_message_that_is_not_sent_on() {
    assert_eq!(
        invalid("the [brigade] block names \"to\" twice"),
        "The lines at the top of your message could not be read: the [brigade] block names \"to\" twice."
    );
    assert_eq!(
        unaddressed("ROSTER"),
        "No session was named. Start your message with a line `to: <session or member>`, or reply in the thread under a session's message.\n\nROSTER"
    );
    assert_eq!(command("sessions", "ROSTER"), "ROSTER");
    assert_eq!(command("help", "ROSTER"), "ROSTER");
    assert_eq!(
        command("bogus", "ROSTER"),
        "Unknown command `bogus`. Try `sessions`, `help`, or `send <session or member> <text>`.\n\nROSTER"
    );
    // The refusal may name sessions and teams, so it is escaped; the roster is the gateway's own text.
    assert_eq!(
        not_sent("\"<b>\" is a member of 2 teams", "*ROSTER*"),
        "The message was not sent: \"&lt;b&gt;\" is a member of 2 teams.\n\n*ROSTER*"
    );
}

#[test]
fn a_roster_that_leaves_teams_out_says_so_without_naming_them() {
    // With every team listed the roster is what it always was, and so is every answer that shows it.
    assert_eq!(not_connected(0), None);
    assert_eq!(roster_of_connected("ROSTER", 0), "ROSTER");
    // One team whose gateway is out, and several: a sentence under the roster, which names no team.
    let one = "One team's gateway is not connected at the moment, so its sessions cannot be reached.";
    assert_eq!(not_connected(1).as_deref(), Some(one));
    assert_eq!(roster_of_connected("ROSTER", 1), format!("ROSTER\n\n{one}"));
    assert_eq!(
        roster_of_connected("ROSTER", 3),
        "ROSTER\n\n3 teams' gateways are not connected at the moment, so their sessions cannot be reached."
    );
    // The answers that show a roster show this one: a name nobody has, no name at all, and the roster asked
    // for by its command.
    let roster = roster_of_connected("ROSTER", 1);
    assert_eq!(
        not_sent(
            "no session ends with \"3f9a2\", and no member has that label",
            &roster
        ),
        format!(
            "The message was not sent: no session ends with \"3f9a2\", and no member has that label.\n\nROSTER\n\n{one}"
        )
    );
    assert!(unaddressed(&roster).ends_with(&format!("\n\nROSTER\n\n{one}")));
    assert_eq!(command("sessions", &roster), format!("ROSTER\n\n{one}"));
}

#[test]
fn an_answer_is_for_what_is_not_sent_on_and_not_for_what_is_ignored() {
    let text = || "t".to_owned();
    assert_eq!(
        answer_for(
            &SlackInbound::Invalid {
                reason: "why".to_owned()
            },
            "R"
        )
        .as_deref(),
        Some("The lines at the top of your message could not be read: why.")
    );
    assert_eq!(
        answer_for(&SlackInbound::Unaddressed { text: text() }, "R"),
        Some(unaddressed("R"))
    );
    let word = |word: &str| SlackInbound::Command {
        command: word.to_owned(),
        args: String::new(),
        text: text(),
    };
    assert_eq!(answer_for(&word("sessions"), "R").as_deref(), Some("R"));
    assert_eq!(answer_for(&word("Sessions"), "R"), Some(command("Sessions", "R")));
    assert_eq!(
        answer_for(
            &SlackInbound::Message {
                target: "3f9a2".to_owned(),
                reply_to: None,
                summary: None,
                text: text(),
                source: TargetSource::Block,
            },
            "R"
        ),
        None
    );
    assert_eq!(
        answer_for(
            &SlackInbound::Ignore {
                reason: "why".to_owned()
            },
            "R"
        ),
        None
    );
}

#[test]
fn a_person_is_answered_where_they_are() {
    // A slash command: in its own response, and nothing is posted.
    assert_eq!(
        tell(&slash(), "hello"),
        Tell::Response(answer(200, r#"{"response_type":"ephemeral","text":"hello"}"#))
    );
    // A direct message: a post under the person's message, or in the thread they wrote in.
    let post = |thread_ts: Option<&str>| {
        Tell::Post(PostMessage {
            channel: "D1".to_owned(),
            text: "hello".to_owned(),
            thread_ts: thread_ts.map(str::to_owned),
            metadata: None,
        })
    };
    assert_eq!(tell(&direct(), "hello"), post(Some("10.1")));
    let in_thread = InboundSlack {
        thread_ts: some("7.7"),
        ..direct()
    };
    assert_eq!(tell(&in_thread, "hello"), post(Some("7.7")));
    // A thread that is the empty string is still the thread, and the post then goes into none.
    let empty_thread = InboundSlack {
        thread_ts: some(""),
        ..direct()
    };
    assert_eq!(tell(&empty_thread, "hello"), post(Some("")));
    // A channel: to the person alone, in the thread they wrote in or in the channel.
    let ephemeral = |thread_ts: Option<&str>| {
        Tell::Ephemeral(PostEphemeral {
            channel: "C1".to_owned(),
            user: "U1".to_owned(),
            text: "hello".to_owned(),
            thread_ts: thread_ts.map(str::to_owned),
        })
    };
    assert_eq!(tell(&mention(), "hello"), ephemeral(Some("5.5")));
    let top_level = InboundSlack {
        thread_ts: None,
        ..mention()
    };
    assert_eq!(tell(&top_level, "hello"), ephemeral(None));
    // A group message is no direct message.
    let group = InboundSlack {
        channel_type: some("mpim"),
        ..top_level
    };
    assert_eq!(tell(&group, "hello"), ephemeral(None));
}

// ---- slack-in: a message that is sent on -------------------------------------------------------------------

#[test]
fn the_channels_name_is_asked_for_everywhere_but_in_a_direct_message() {
    assert!(wants_channel_name(&mention()));
    assert!(!wants_channel_name(&direct()));
    assert_eq!(mention().posted_ts(), Some("6.6"));
    assert_eq!(slash().posted_ts(), None);
    let empty = InboundSlack {
        ts: some(""),
        ..mention()
    };
    assert_eq!(empty.posted_ts(), None);
    assert_eq!(REACTION, "eyes");
}

#[test]
fn the_message_sent_on_takes_the_persons_summary_before_the_bodys_own() {
    let carried = BrigadeBody {
        body: "the body".to_owned(),
        summary: "first line".to_owned(),
    };
    assert_eq!(
        send(&mention(), S1, &carried, None, Some(M1)),
        Sending {
            recipient_session_id: S1.to_owned(),
            body: "the body".to_owned(),
            summary: "first line".to_owned(),
            reply_to: some(M1),
            idempotency_key: "slack:Ev1".to_owned(),
        }
    );
    let given = send(&slash(), S1, &carried, Some("My sum"), None);
    assert_eq!(given.summary, "My sum");
    assert_eq!(given.reply_to, None);
    assert_eq!(given.idempotency_key, "slack:cmd:T1");
    // A summary and a reply-to the person left empty are still what they gave.
    let empty = send(&mention(), S1, &carried, Some(""), Some(""));
    assert_eq!(
        (empty.summary.as_str(), empty.reply_to.as_deref()),
        ("", Some(""))
    );
}

#[test]
fn the_row_of_a_persons_message_is_keyed_by_the_thread_it_is_in_or_starts() {
    assert_eq!(
        thread_row_in(M1, S1, &mention(), "first line"),
        ThreadRow {
            brigade_message_id: M1.to_owned(),
            direction: Direction::In,
            session_id: S1.to_owned(),
            address: "C1".to_owned(),
            mail_message_id: some("5.5"),
            provider_mail_id: some("Ev1"),
            subject: some("first line"),
            kind: Some(ThreadKind::Slack),
            actor: some("U1"),
        }
    );
    // A message in no thread starts one; a slash command has neither.
    assert_eq!(
        thread_row_in(M1, S1, &direct(), "s").mail_message_id,
        some("10.1")
    );
    assert_eq!(thread_row_in(M1, S1, &slash(), "s").mail_message_id, None);
    let empty_thread = InboundSlack {
        thread_ts: some(""),
        ..direct()
    };
    assert_eq!(
        thread_row_in(M1, S1, &empty_thread, "s").mail_message_id,
        some("")
    );
}

fn session(id: &str, name: &str, principal: &str, label: Option<&str>, state: &str) -> SessionRecord {
    SessionRecord {
        session_id: id.to_owned(),
        session_name: name.to_owned(),
        principal_ref: principal.to_owned(),
        human_label: label.map(str::to_owned),
        state: state.to_owned(),
        last_seen_at: some("2026-10-15T10:00:00Z"),
        ..SessionRecord::default()
    }
}

const ACCEPTED: &str = "Accepted means stored for that session, not read; its reply will come to you here.";

#[test]
fn the_confirmation_says_which_session_and_for_a_member_why_that_one() {
    let frank = session(S1, "frank-<reviewer>", "p1", Some("Sam & co"), "idle");
    let active = session(S2, "sam-two", "p1", Some("Sam & co"), "active");
    let offline = session(S3, "sam-old", "p1", Some("Sam & co"), "offline");
    let teams = [TeamSessions {
        team_id: "t1",
        team_name: "Dev <2>",
        sessions: vec![&frank, &active, &offline],
    }];
    // By the id's tail: the session alone.
    let by_tail = resolve_recipient_across("3f9a2", &teams).unwrap();
    assert_eq!(
        sent_note(&by_tail, 1),
        format!("Sent to *frank-&lt;reviewer&gt;* (`…3f9a2`). {ACCEPTED}")
    );
    // With several teams served, the team too.
    assert_eq!(
        sent_note(&by_tail, 2),
        format!("Sent to *frank-&lt;reviewer&gt;* (`…3f9a2`) on team *Dev &lt;2&gt;*. {ACCEPTED}")
    );
    // By a member's label: why that session, and the other one that is online.
    let by_label = resolve_recipient_across("sam & co", &teams).unwrap();
    assert_eq!(
        sent_note(&by_label, 1),
        format!(
            "Sent to *sam-two* (`…7b1c4`): sam &amp; co's the active session of theirs seen most recently. 1 other session of theirs is online: *frank-&lt;reviewer&gt;* (`…3f9a2`); send to one of those to choose it. {ACCEPTED}"
        )
    );
    // Two others online are listed, best first.
    let idle = session(
        "dddddddd-0000-4000-8000-0000000000d4",
        "sam-four",
        "p1",
        Some("Sam & co"),
        "idle",
    );
    let more = [TeamSessions {
        team_id: "t1",
        team_name: "ops",
        sessions: vec![&frank, &active, &offline, &idle],
    }];
    let by_label = resolve_recipient_across("Sam & co", &more).unwrap();
    assert_eq!(
        sent_note(&by_label, 2),
        format!(
            "Sent to *sam-two* (`…7b1c4`) on team *ops*: Sam &amp; co's the active session of theirs seen most recently. 2 other sessions of theirs are online: *frank-&lt;reviewer&gt;* (`…3f9a2`), *sam-four* (`…000d4`); send to one of those to choose it. {ACCEPTED}"
        )
    );
    // Every session offline: the message waits.
    let waiting = [TeamSessions {
        team_id: "t1",
        team_name: "ops",
        sessions: vec![&offline],
    }];
    let by_label = resolve_recipient_across("Sam & co", &waiting).unwrap();
    assert_eq!(
        sent_note(&by_label, 1),
        format!("Sent to *sam-old* (`…000c3`): Sam &amp; co's offline; the message waits for it. {ACCEPTED}")
    );
}

#[test]
fn a_person_is_told_of_the_send_when_they_used_a_slash_command_or_named_a_member() {
    let frank = session(S1, "frank", "p1", Some("sam"), "idle");
    let teams = [TeamSessions {
        team_id: "t1",
        team_name: "ops",
        sessions: vec![&frank],
    }];
    let by_tail = resolve_recipient_across("3f9a2", &teams).unwrap();
    let by_label = resolve_recipient_across("sam", &teams).unwrap();
    // A session named by its id: the reaction is the whole confirmation.
    assert_eq!(confirmation(&mention(), &by_tail, 1), None);
    assert_eq!(confirmation(&direct(), &by_tail, 1), None);
    // A slash command is always answered, in its response.
    assert_eq!(
        confirmation(&slash(), &by_tail, 1),
        Some(Tell::Response(answer_command(&sent_note(&by_tail, 1))))
    );
    // A member named: told where they are.
    assert_eq!(
        confirmation(&mention(), &by_label, 2),
        Some(tell(&mention(), &sent_note(&by_label, 2)))
    );
    assert!(matches!(
        confirmation(&direct(), &by_label, 1),
        Some(Tell::Post(_))
    ));

    // With a team left out of the request the note says so under where the message went, and names the
    // team it went to: the name may have been one a session of the other team answers to as well.
    let one_out = "One team's gateway is not connected at the moment, so its sessions cannot be reached.";
    assert_eq!(
        confirmation_among(&slash(), &by_label, 1, 1),
        Some(Tell::Response(answer_command(&format!(
            "{}\n\n{one_out}",
            sent_note(&by_label, 2)
        ))))
    );
    assert_eq!(
        confirmation_among(&mention(), &by_label, 2, 1),
        Some(tell(
            &mention(),
            &format!("{}\n\n{one_out}", sent_note(&by_label, 3))
        ))
    );
    assert!(sent_note(&by_label, 2).contains(" on team *ops*"));
    // A session named by the end of its id is told as well while a team is left out, in a direct message
    // and in a channel: the characters may end the id of a session of that team too, and the reaction
    // alone would not say which session the message went to.
    let by_tail_among = format!("{}\n\n{one_out}", sent_note(&by_tail, 2));
    assert_eq!(
        by_tail_among,
        format!("Sent to *frank* (`…3f9a2`) on team *ops*. {ACCEPTED}\n\n{one_out}")
    );
    for message in [direct(), mention()] {
        assert_eq!(
            confirmation_among(&message, &by_tail, 1, 1),
            Some(tell(&message, &by_tail_among))
        );
    }
    // With no team left out the confirmation is what it always was, and that of a session named by its
    // id in a message is the reaction alone.
    for message in [slash(), mention(), direct()] {
        assert_eq!(
            confirmation_among(&message, &by_label, 2, 0),
            confirmation(&message, &by_label, 2)
        );
        assert_eq!(
            confirmation_among(&message, &by_tail, 2, 0),
            confirmation(&message, &by_tail, 2)
        );
    }
    assert_eq!(confirmation_among(&direct(), &by_tail, 2, 0), None);
    assert_eq!(confirmation_among(&mention(), &by_tail, 2, 0), None);
}

// ---- slack-out ---------------------------------------------------------------------------------------------

#[test]
fn channel_threads_are_heard_when_auth_test_succeeds_and_lists_the_scope() {
    let fine: Result<(), SlackError> = Ok(());
    assert!(hears_channel_threads(&fine, Some("chat:write, channels:history")));
    assert!(!hears_channel_threads(&fine, Some("chat:write,groups:history")));
    assert!(!hears_channel_threads(&fine, Some("channels:historyx")));
    assert!(!hears_channel_threads(&fine, Some("")));
    assert!(!hears_channel_threads(&fine, None));
    // A call that failed says nothing, whatever its header lists.
    let failed: Result<(), SlackError> = Err(SlackError::new("invalid_auth", None));
    assert!(!hears_channel_threads(&failed, Some("channels:history")));
    assert_eq!(CHANNEL_HISTORY_SCOPE, "channels:history");
}

#[test]
fn a_handle_is_looked_up_once_whatever_its_case() {
    assert_eq!(mention_key("Alice"), "alice");
    assert_eq!(mention_key("Bob.Smith"), "bob.smith");
}

#[test]
fn a_target_is_described_as_it_was_named() {
    let named = |name: &str| name.to_owned();
    assert_eq!(
        describe_target(&SlackTarget::User {
            id: named("U0123ABCD")
        }),
        "U0123ABCD"
    );
    assert_eq!(
        describe_target(&SlackTarget::Channel {
            id: named("C0123ABCD")
        }),
        "C0123ABCD"
    );
    assert_eq!(
        describe_target(&SlackTarget::UserName { name: named("alice") }),
        "@alice"
    );
    assert_eq!(
        describe_target(&SlackTarget::UserEmail {
            email: named("alice@example.com")
        }),
        "alice@example.com"
    );
    assert_eq!(
        describe_target(&SlackTarget::ChannelName { name: named("qa") }),
        "#qa"
    );
}

/// The note a session is sent, with this body.
fn note(body: String) -> Sending {
    Sending {
        recipient_session_id: S1.to_owned(),
        body,
        summary: "slack gateway: not delivered".to_owned(),
        reply_to: some(M2),
        idempotency_key: format!("gw-err:{M2}"),
    }
}

#[test]
fn a_session_is_told_why_its_message_was_not_delivered() {
    let env = envelope();
    assert_eq!(
        undeliverable(
            &env,
            "\"to: 3f9a2\" is not @name, #channel, an email address or a Slack id"
        ),
        note(format!(
            "The Slack gateway could not deliver your message {M2}: \"to: 3f9a2\" is not @name, #channel, an email address or a Slack id"
        ))
    );
    assert_eq!(
        no_match(
            &env,
            &SlackTarget::UserName {
                name: "nobody".to_owned()
            }
        ),
        note(format!(
            "The Slack gateway could not deliver your message {M2}: no Slack user or channel matches \"@nobody\""
        ))
    );
    assert_eq!(
        not_posted(&env, "C7", "slack: chat.postMessage: is_archived"),
        note(format!(
            "The Slack gateway could not post your message {M2} to C7: slack: chat.postMessage: is_archived"
        ))
    );
    assert_eq!(NOT_DELIVERED_SUMMARY, "slack gateway: not delivered");
}

#[test]
fn a_failure_is_final_unless_slack_says_a_later_attempt_can_mend_it() {
    for retried in [
        "ratelimited",
        "internal_error",
        "service_unavailable",
        "fatal_error",
        "http_502",
    ] {
        assert!(!is_final(&SlackError::new(retried, None)), "{retried}");
    }
    for ended in [
        "channel_not_found",
        "is_archived",
        "http_429",
        "http_404",
        "not_in_channel",
    ] {
        assert!(is_final(&SlackError::new(ended, None)), "{ended}");
    }
    // No answer at all is final too: only Slack's own word makes a failure one that passes.
    assert!(is_final(&SlackError::unanswered("connection refused")));
    assert!(joins_first(&SlackError::new("not_in_channel", None)));
    assert!(!joins_first(&SlackError::new("channel_not_found", None)));
}

#[test]
fn a_post_carries_the_message_its_sender_and_the_team_beside_its_text() {
    let env = envelope();
    let reply = SlackOutbound {
        target: SlackTarget::Channel { id: "C1".to_owned() },
        thread_ts: some("5.5"),
        mention: some("U1"),
        text: "ok".to_owned(),
    };
    assert_eq!(
        post(&env, &reply, "C1", "the rendered text", "ops"),
        PostMessage {
            channel: "C1".to_owned(),
            text: "the rendered text".to_owned(),
            thread_ts: some("5.5"),
            metadata: Some(Metadata {
                event_type: "brigade_message".to_owned(),
                event_payload: vec![
                    ("message_id".to_owned(), M2.to_owned()),
                    ("from_session".to_owned(), S1.to_owned()),
                    ("team".to_owned(), "ops".to_owned()),
                ],
            }),
        }
    );
    let fresh = SlackOutbound {
        thread_ts: None,
        mention: None,
        ..reply
    };
    assert_eq!(post(&env, &fresh, "D9", "t", "ops").thread_ts, None);
}

#[test]
fn the_row_of_a_sessions_post_is_keyed_by_the_thread_it_went_into_or_starts() {
    let env = envelope();
    let posted = Posted {
        ts: "20.1".to_owned(),
        channel: "C7".to_owned(),
    };
    let fresh = SlackOutbound {
        target: SlackTarget::UserName {
            name: "alice".to_owned(),
        },
        thread_ts: None,
        mention: None,
        text: "\n  The fix is on master.  \nmore".to_owned(),
    };
    assert_eq!(
        thread_row_out(&env, &fresh, &posted),
        ThreadRow {
            brigade_message_id: M2.to_owned(),
            direction: Direction::Out,
            session_id: S1.to_owned(),
            // Where Slack says the post is, not what the target named.
            address: "C7".to_owned(),
            mail_message_id: some("20.1"),
            provider_mail_id: some("20.1"),
            subject: some("Fix merged"),
            kind: Some(ThreadKind::Slack),
            actor: None,
        }
    );
    // A reply stays in its thread and keeps who it mentions.
    let reply = SlackOutbound {
        thread_ts: some("5.5"),
        mention: some("U1"),
        ..fresh.clone()
    };
    let row = thread_row_out(&env, &reply, &posted);
    assert_eq!(
        (row.mail_message_id, row.provider_mail_id, row.actor),
        (some("5.5"), some("20.1"), some("U1"))
    );
    // Without a summary the subject is the text's first line, and at most 120 characters of it.
    let unsummarised = Envelope {
        summary: some(""),
        ..env.clone()
    };
    assert_eq!(
        thread_row_out(&unsummarised, &fresh, &posted).subject,
        some("The fix is on master.")
    );
    let long = SlackOutbound {
        text: "x".repeat(200),
        ..fresh
    };
    let unsummarised = Envelope { summary: None, ..env };
    assert_eq!(
        thread_row_out(&unsummarised, &long, &posted).subject,
        Some(format!("{}…", "x".repeat(119)))
    );
}
