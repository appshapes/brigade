//! `supabase/functions/_shared/gateway_test.ts`, case for case: the same inputs and the same expected
//! outputs.

use brigade_gateway::block::Span;
use brigade_gateway::resolve::{
    addressed_to_inbox, description, gateway_for_mail, reply_tag, sender_allowed, tag_of,
};
use brigade_gateway::{
    Direction, Envelope, How, Inbound, InboundMail, MAX_BODY_BYTES, Outbound, Recipient, RenderContext,
    Sender, SessionRecord, Source, TeamRecipient, TeamSessions, ThreadLookup, ThreadRow, brigade_body,
    decision_view, find_blocks, last_block, render_outbound, resolve_inbound, resolve_outbound,
    resolve_recipient, resolve_recipient_across, roster_text,
};

const INBOX: &str = "a1b2c3d4@example.resend.app";
const S1: &str = "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2";
const S2: &str = "7a7a7a7a-7a7a-4a7a-8a7a-7a7a7a7a7b1c4";
const GW: &str = "9c9c9c9c-9c9c-4c9c-8c9c-9c9c9c9c9c9c9";
const M1: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";
const M2: &str = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";

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

fn sessions() -> Vec<SessionRecord> {
    vec![
        SessionRecord {
            session_description: Some("reviewing the login fix".to_owned()),
            ..session(S1, "frank-reviewer", "p1", Some("sam@example.com"), "idle")
        },
        session(S2, "rjae-brigade", "p2", None, "active"),
        SessionRecord {
            harness: Some("gateway-email".to_owned()),
            ..session(GW, "mail-gateway", "p3", Some("mail gateway"), "idle")
        },
        SessionRecord {
            harness: Some("gateway-slack".to_owned()),
            ..session(
                "aaaaaaaa-0000-4000-8000-0000000000aa",
                "slack-gateway",
                "p4",
                Some("slack gateway"),
                "idle",
            )
        },
    ]
}

struct Threads(Vec<ThreadRow>);

impl ThreadLookup for Threads {
    fn by_brigade_id(&self, id: &str) -> Option<ThreadRow> {
        self.0.iter().find(|row| row.brigade_message_id == id).cloned()
    }

    fn by_mail_ids(&self, ids: &[String]) -> Option<ThreadRow> {
        self.0
            .iter()
            .find(|row| row.mail_message_id.as_ref().is_some_and(|id| ids.contains(id)))
            .cloned()
    }
}

fn threads(rows: &[ThreadRow]) -> Threads {
    Threads(rows.to_vec())
}

fn mail() -> InboundMail {
    InboundMail {
        provider_id: "re_1".to_owned(),
        from: "alice@example.com".to_owned(),
        from_name: Some("Alice".to_owned()),
        to: vec![INBOX.to_owned()],
        subject: "Login page broken on Safari".to_owned(),
        message_id: Some("<abc@mail.example.com>".to_owned()),
        in_reply_to: None,
        references: Vec::new(),
        text: "Steps: open /login, submit, blank page.".to_owned(),
        received_at: "2026-10-03T16:00:00Z".to_owned(),
    }
}

fn mail_with(text: &str) -> InboundMail {
    InboundMail {
        text: text.to_owned(),
        ..mail()
    }
}

fn err_of<T>(result: Result<T, String>) -> String {
    result.err().unwrap_or_default()
}

/// The id of the session found, how it was found and the ids of the others.
fn pick(result: Result<Recipient<'_>, String>) -> (String, How, Vec<String>) {
    let recipient = result.unwrap();
    (
        recipient.session.session_id.clone(),
        recipient.how,
        recipient.others.iter().map(|s| s.session_id.clone()).collect(),
    )
}

fn ids(ids: &[&str]) -> Vec<String> {
    ids.iter().map(|id| (*id).to_owned()).collect()
}

#[test]
fn resolve_recipient_full_id_unique_tail_ambiguous_tail_too_short_none() {
    let sessions = sessions();
    assert_eq!(
        pick(resolve_recipient(S1, &sessions)),
        (S1.to_owned(), How::Id, ids(&[]))
    );
    assert_eq!(
        pick(resolve_recipient("3F9A2", &sessions)),
        (S1.to_owned(), How::Tail, ids(&[]))
    );
    let twin = SessionRecord {
        session_id: "aaaaaaaa-0000-4000-8000-00000009c9c9".to_owned(),
        ..sessions[2].clone()
    };
    let ambiguous = resolve_recipient("9c9c9", [&sessions[2], &twin]);
    assert!(err_of(ambiguous).contains("2 session ids"));
    assert!(err_of(resolve_recipient("3f9", &sessions)).contains("too short"));
    assert!(err_of(resolve_recipient("3f9", &sessions)).contains("no member has that label"));
    assert!(err_of(resolve_recipient("zzzzz", &sessions)).contains("no session ends"));
    assert!(err_of(resolve_recipient("", &sessions)).contains("no session or member"));
}

/// One of Sam's sessions; each test changes what it needs.
fn sam() -> SessionRecord {
    SessionRecord {
        last_seen_at: Some("2026-10-15T10:00:00Z".to_owned()),
        ..session(
            "cccccccc-0000-4000-8000-0000000000c1",
            "sam-one",
            "9f3c1a20-aaaa-4aaa-8aaa-aaaaaaaaaaaa",
            Some("Sam@Example.com"),
            "idle",
        )
    }
}

fn sam_as(id: &str, name: &str, state: &str) -> SessionRecord {
    SessionRecord {
        session_id: id.to_owned(),
        session_name: name.to_owned(),
        state: state.to_owned(),
        ..sam()
    }
}

#[test]
fn resolve_recipient_a_members_label_picks_their_best_session_active_before_idle_before_offline() {
    let sessions = sessions();
    let idle = sam();
    let active = sam_as("cccccccc-0000-4000-8000-0000000000c2", "sam-two", "active");
    let offline = sam_as("cccccccc-0000-4000-8000-0000000000c3", "sam-old", "offline");
    // sessions[0] carries the same label under another principal; the rest are the unlabelled one and the
    // gateways.
    let roster: Vec<&SessionRecord> = [&offline, &idle, &active]
        .into_iter()
        .chain(&sessions[1..])
        .collect();
    assert_eq!(
        pick(resolve_recipient("sam@example.com", roster)),
        (
            active.session_id.clone(),
            How::Label,
            ids(&[&idle.session_id, &offline.session_id])
        )
    );
    // Among equals, the most recently seen.
    let older = SessionRecord {
        session_id: "cccccccc-0000-4000-8000-0000000000c4".to_owned(),
        last_seen_at: Some("2026-10-15T09:00:00Z".to_owned()),
        ..sam()
    };
    assert_eq!(
        pick(resolve_recipient("sam@example.com", [&older, &idle])).0,
        idle.session_id
    );
    // Every session offline: the most recently seen one, and the message waits.
    let older_offline = SessionRecord {
        state: "offline".to_owned(),
        ..older.clone()
    };
    let waiting = resolve_recipient("sam@example.com", [&offline, &older_offline]).unwrap();
    assert_eq!(waiting.session.session_id, offline.session_id);
    assert_eq!(waiting.who.as_deref(), Some("sam@example.com"));
}

#[test]
fn resolve_recipient_refusing_sessions_and_gateways_are_never_picked_a_shared_label_is_refused() {
    let sessions = sessions();
    let refusing = SessionRecord {
        inbound: Some("refuse".to_owned()),
        state: "active".to_owned(),
        ..sam()
    };
    let holding = SessionRecord {
        session_id: "cccccccc-0000-4000-8000-0000000000c5".to_owned(),
        inbound: Some("hold".to_owned()),
        ..sam()
    };
    assert_eq!(
        pick(resolve_recipient("sam@example.com", [&refusing, &holding])).0,
        holding.session_id
    );
    assert!(err_of(resolve_recipient("sam@example.com", [&refusing])).contains("can receive"));
    assert!(err_of(resolve_recipient("mail gateway", &sessions)).contains("can receive"));
    // A webhook gateway run as one of sam's own sessions is active and the freshest, and still never the
    // pick.
    let webhook = SessionRecord {
        session_id: "eeeeeeee-0000-4000-8000-0000000000e1".to_owned(),
        state: "active".to_owned(),
        harness: Some("gateway-webhook".to_owned()),
        ..sam()
    };
    let idle = SessionRecord {
        session_id: "eeeeeeee-0000-4000-8000-0000000000e2".to_owned(),
        state: "idle".to_owned(),
        ..sam()
    };
    assert_eq!(
        pick(resolve_recipient("sam@example.com", [&webhook, &idle])).0,
        idle.session_id
    );
    let impostor = SessionRecord {
        session_id: "dddddddd-0000-4000-8000-0000000000d1".to_owned(),
        principal_ref: "0b0b0b0b-bbbb-4bbb-8bbb-bbbbbbbbbbbb".to_owned(),
        state: "active".to_owned(),
        ..sam()
    };
    let shared = err_of(resolve_recipient("sam@example.com", [&sam(), &impostor]));
    assert!(shared.contains("2 members"));
    assert!(shared.contains("9f3c1a20, 0b0b0b0b"));
}

#[test]
fn resolve_recipient_eight_or_more_characters_of_a_principal_reference_name_the_member() {
    let sessions = sessions();
    let one = sam();
    let two = SessionRecord {
        session_id: "cccccccc-0000-4000-8000-0000000000c2".to_owned(),
        state: "active".to_owned(),
        human_label: None,
        ..sam()
    };
    let roster: Vec<&SessionRecord> = [&one, &two].into_iter().chain(&sessions).collect();
    assert_eq!(
        pick(resolve_recipient("9F3C1A20", roster)),
        (two.session_id.clone(), How::Principal, ids(&[&one.session_id]))
    );
    assert!(err_of(resolve_recipient("9f3c1a2", [&one])).contains("no session ends"));
    // A session's name is never an address.
    assert!(err_of(resolve_recipient("frank-reviewer", &sessions)).contains("no member has that label"));
}

// Several teams on one gateway. Alpha carries the shared fixture's two people; Beta carries Sam under the
// second principal, as a person on two teams is (one credential per team, so one principal per team).

fn team<'a>(id: &'a str, name: &'a str, sessions: &[&'a SessionRecord]) -> TeamSessions<'a> {
    TeamSessions {
        team_id: id,
        team_name: name,
        sessions: sessions.to_vec(),
    }
}

fn bob() -> SessionRecord {
    SessionRecord {
        human_label: Some("bob@example.com".to_owned()),
        principal_ref: "b0b0b0b0-bbbb-4bbb-8bbb-bbbbbbbbbbbb".to_owned(),
        ..sam()
    }
}

/// The id of the session found, how it was found, its team and the ids of the others.
fn pick_across(result: Result<TeamRecipient<'_>, String>) -> (String, How, String, Vec<String>) {
    let found = result.unwrap();
    (
        found.recipient.session.session_id.clone(),
        found.recipient.how,
        found.team_name.to_owned(),
        found
            .recipient
            .others
            .iter()
            .map(|s| s.session_id.clone())
            .collect(),
    )
}

#[test]
fn resolve_recipient_across_an_id_or_a_tail_is_found_on_whichever_team_has_it_a_label_on_one_team_is_that_teams()
 {
    let sessions = sessions();
    let bob = bob();
    let alpha = team("t-alpha", "Alpha", &[&sessions[0], &sessions[1]]);
    let beta = team("t-beta", "Beta", &[&bob]);
    let teams = [alpha.clone(), beta.clone()];
    assert_eq!(
        pick_across(resolve_recipient_across(S1, &teams)),
        (S1.to_owned(), How::Id, "Alpha".to_owned(), ids(&[]))
    );
    assert_eq!(
        pick_across(resolve_recipient_across("3F9A2", &teams)),
        (S1.to_owned(), How::Tail, "Alpha".to_owned(), ids(&[]))
    );
    assert_eq!(
        pick_across(resolve_recipient_across("000c1", &teams)),
        (bob.session_id.clone(), How::Tail, "Beta".to_owned(), ids(&[]))
    );
    assert_eq!(
        pick_across(resolve_recipient_across("Bob@Example.com", &teams)),
        (bob.session_id.clone(), How::Label, "Beta".to_owned(), ids(&[]))
    );
    assert_eq!(
        pick_across(resolve_recipient_across("b0b0b0b0", &teams)),
        (
            bob.session_id.clone(),
            How::Principal,
            "Beta".to_owned(),
            ids(&[])
        )
    );
    // A tail that ends an id on each team is refused with both ids, as inside one team.
    let twin = SessionRecord {
        session_id: "aaaaaaaa-0000-4000-8000-00000009c9c9".to_owned(),
        ..sessions[2].clone()
    };
    let ambiguous = err_of(resolve_recipient_across(
        "9c9c9",
        &[
            team("t-alpha", "Alpha", &[&sessions[2]]),
            team("t-beta", "Beta", &[&twin]),
        ],
    ));
    assert!(ambiguous.contains("2 session ids"));
    assert!(ambiguous.contains(&twin.session_id));
    // Nobody of that name, anywhere.
    assert!(err_of(resolve_recipient_across("zzzzz", &teams)).contains("no session ends"));
    assert!(err_of(resolve_recipient_across("3f9", &teams)).contains("too short"));
    assert!(err_of(resolve_recipient_across("", &teams)).contains("no session or member"));
    assert!(err_of(resolve_recipient_across("frank-reviewer", &teams)).contains("no member has that label"));
}

#[test]
fn resolve_recipient_across_a_label_on_two_teams_is_answered_with_each_teams_best_sessions_by_tail_inside_one_team_the_rules_are_unchanged()
 {
    let sessions = sessions();
    let alpha = team("t-alpha", "Alpha", &[&sessions[0], &sessions[1]]);
    let beta_sessions = [
        SessionRecord {
            state: "active".to_owned(),
            ..sam()
        },
        sam_as("cccccccc-0000-4000-8000-0000000000c2", "sam-two", "offline"),
        sam_as("cccccccc-0000-4000-8000-0000000000c3", "sam-three", "idle"),
        sam_as("cccccccc-0000-4000-8000-0000000000c4", "sam-four", "offline"),
        SessionRecord {
            inbound: Some("refuse".to_owned()),
            ..sam_as("cccccccc-0000-4000-8000-0000000000c5", "sam-quiet", "idle")
        },
    ];
    let beta = team("t-beta", "Beta", &beta_sessions.iter().collect::<Vec<_>>());
    // Alpha's frank-reviewer carries the label under principal p1; Beta's Sam under another: two teams, the
    // person picks by tail. The best three of a team are listed, state first; a refusing session is not a
    // candidate.
    let two = err_of(resolve_recipient_across(
        "sam@example.com",
        &[beta.clone(), alpha.clone()],
    ));
    assert!(two.contains("\"sam@example.com\" is a member of 2 teams"));
    assert!(two.contains("Alpha: 3f9a2 frank-reviewer (idle)"));
    assert!(two.contains("Beta: 000c1 sam-one (active), 000c3 sam-three (idle), 000c2 sam-two (offline)"));
    assert!(!two.contains("sam-four"));
    assert!(!two.contains("sam-quiet"));
    // A team whose member cannot receive is still named, so the person knows where the label lives.
    let quiet = err_of(resolve_recipient_across(
        "sam@example.com",
        &[alpha.clone(), team("t-beta", "Beta", &[&beta_sessions[4]])],
    ));
    assert!(quiet.contains("Beta: no session can receive a message"));
    // On one team, the member's best session with the others, and the team carried.
    let delta = team("t-delta", "Delta", &[&sessions[1]]);
    assert_eq!(
        pick_across(resolve_recipient_across(
            "sam@example.com",
            &[beta.clone(), delta.clone()]
        )),
        (
            beta_sessions[0].session_id.clone(),
            How::Label,
            "Beta".to_owned(),
            ids(&[
                &beta_sessions[2].session_id,
                &beta_sessions[1].session_id,
                &beta_sessions[3].session_id
            ])
        )
    );
    // Two principals sharing a label inside one team: refused with the principal prefixes, as before.
    let sam = sam();
    let gamma = team("t-gamma", "Gamma", &[&sessions[0], &sam]);
    let shared = err_of(resolve_recipient_across("sam@example.com", &[gamma, delta]));
    assert!(shared.contains("2 members"));
    assert!(shared.contains("p1, 9f3c1a20"));
}

#[test]
fn addressed_to_inbox_accepts_the_inbox_and_its_tags_refuses_another_teams_inbox_on_the_same_account() {
    assert!(addressed_to_inbox(&[INBOX], INBOX));
    assert!(addressed_to_inbox(&["A1B2C3D4@EXAMPLE.RESEND.APP"], INBOX));
    assert!(addressed_to_inbox(&[reply_tag(INBOX, M1)], INBOX));
    assert!(addressed_to_inbox(&["brigade@appshapes.com", INBOX], INBOX));
    assert!(!addressed_to_inbox(&["7c7c7c7c@example.resend.app"], INBOX));
    assert!(!addressed_to_inbox(&["a1b2c3d4x@example.resend.app"], INBOX));
    assert!(!addressed_to_inbox(&[] as &[&str], INBOX));
}

#[test]
fn reply_tag_and_tag_of_round_trip_the_message_id_through_the_inbox_address() {
    let tagged = reply_tag(INBOX, M1);
    assert_eq!(tagged, format!("a1b2c3d4+{M1}@example.resend.app"));
    assert_eq!(
        tag_of(&["someone@else.com".to_owned(), tagged.to_uppercase()], INBOX).as_deref(),
        Some(M1)
    );
    assert_eq!(tag_of(&[INBOX], INBOX), None);
    assert_eq!(tag_of(&["a1b2c3d4+notauuid@example.resend.app"], INBOX), None);
}

#[test]
fn inbound_an_explicit_block_wins_and_carries_summary_and_reply_to() {
    let resolved = resolve_inbound(
        &mail_with(&format!(
            "[brigade]\nto: 3f9a2\nreply-to: {M1}\nsummary: Safari login\n[/brigade]\nSteps here."
        )),
        INBOX,
        &threads(&[]),
    );
    assert_eq!(
        resolved,
        Inbound::Message {
            target: "3f9a2".to_owned(),
            reply_to: Some(M1.to_owned()),
            summary: Some("Safari login".to_owned()),
            text: "Steps here.".to_owned(),
            source: Source::Block,
        }
    );
}

#[test]
fn inbound_the_to_shorthand() {
    let resolved = resolve_inbound(
        &mail_with("to: 7b1c4\n\nPlease rerun the suite."),
        INBOX,
        &threads(&[]),
    );
    let Inbound::Message { target, text, .. } = resolved else {
        panic!("expected a message, got {resolved:?}");
    };
    assert_eq!(target, "7b1c4");
    assert_eq!(text, "Please rerun the suite.");
}

#[test]
fn inbound_a_reply_resolves_through_the_reply_to_tag_then_the_headers_then_the_quoted_block() {
    let out = ThreadRow {
        brigade_message_id: M1.to_owned(),
        direction: Direction::Out,
        session_id: S1.to_owned(),
        address: "alice@example.com".to_owned(),
        mail_message_id: Some(format!("<brigade.{M1}@appshapes.com>")),
        provider_mail_id: Some("x".to_owned()),
        subject: Some("s".to_owned()),
        kind: None,
        actor: None,
    };
    let table = threads(&[out]);
    let reply = format!(
        "Ship it.\n\nOn Fri, Oct 3, 2026 at 9:00 AM Brigade <brigade@appshapes.com> wrote:\n> frank-reviewer wrote:\n> body\n> [brigade]\n> to: {S1}\n> reply-to: {M1}\n> [/brigade]"
    );

    let via_tag = resolve_inbound(
        &InboundMail {
            to: vec![reply_tag(INBOX, M1)],
            ..mail_with(&reply)
        },
        INBOX,
        &table,
    );
    assert_eq!(
        via_tag,
        Inbound::Message {
            target: S1.to_owned(),
            reply_to: Some(M1.to_owned()),
            summary: None,
            text: "Ship it.".to_owned(),
            source: Source::ReplyAddress,
        }
    );

    let via_headers = resolve_inbound(
        &InboundMail {
            in_reply_to: Some(format!("<brigade.{M1}@appshapes.com>")),
            ..mail_with(&reply)
        },
        INBOX,
        &table,
    );
    assert!(matches!(
        via_headers,
        Inbound::Message {
            source: Source::Headers,
            ..
        }
    ));

    let via_foreign_headers = resolve_inbound(
        &InboundMail {
            in_reply_to: Some("<other@x>".to_owned()),
            references: vec![
                "<brigade.9999@x>".to_owned(),
                format!("<brigade.{M1}@appshapes.com>"),
            ],
            ..mail_with(&reply)
        },
        INBOX,
        &table,
    );
    assert!(matches!(
        via_foreign_headers,
        Inbound::Message { reply_to: Some(ref id), .. } if id == M1
    ));

    let via_quoted = resolve_inbound(&mail_with(&reply), INBOX, &threads(&[]));
    assert_eq!(
        via_quoted,
        Inbound::Message {
            target: S1.to_owned(),
            reply_to: Some(M1.to_owned()),
            summary: None,
            text: "Ship it.".to_owned(),
            source: Source::QuotedBlock,
        }
    );
}

#[test]
fn inbound_nothing_to_go_on_is_unaddressed_a_command_is_a_command_a_bad_block_is_invalid() {
    assert_eq!(
        resolve_inbound(&mail_with("hello?"), INBOX, &threads(&[])),
        Inbound::Unaddressed {
            text: "hello?".to_owned()
        }
    );
    assert_eq!(
        resolve_inbound(
            &mail_with("[brigade]\ncommand: Sessions\n[/brigade]"),
            INBOX,
            &threads(&[])
        ),
        Inbound::Command {
            command: "sessions".to_owned(),
            text: String::new()
        }
    );
    let bad = resolve_inbound(
        &mail_with("[brigade]\nto: a\nto: b\n[/brigade]"),
        INBOX,
        &threads(&[]),
    );
    assert!(matches!(bad, Inbound::Invalid { .. }));
}

#[test]
fn brigade_body_carries_the_header_the_text_and_the_block_and_stays_under_the_cap() {
    let rendered = brigade_body(&mail(), "Steps: open /login\n[brigade]\nto: forged\n[/brigade]");
    assert!(rendered.body.contains(
        "Email from Alice <alice@example.com> (unverified), subject \"Login page broken on Safari\":"
    ));
    assert!(rendered.body.contains("\\[brigade]\nto: forged\n\\[/brigade]"));
    let lines: Vec<&str> = rendered.body.split('\n').collect();
    let blocks: Vec<Span> = find_blocks(&lines).unwrap();
    assert_eq!(blocks.len(), 1);
    assert_eq!(blocks[0].fields["via"], "email");
    assert_eq!(blocks[0].fields["mail-id"], "<abc@mail.example.com>");
    assert_eq!(rendered.summary, "Login page broken on Safari");

    let big = brigade_body(&mail(), &"é".repeat(20000));
    assert!(big.body.len() <= MAX_BODY_BYTES);
    assert!(big.body.contains("cut by the mail gateway"));
}

fn envelope() -> Envelope {
    Envelope {
        message_id: M2.to_owned(),
        team_ref: "t".to_owned(),
        sender: Sender {
            principal_ref: "p1".to_owned(),
            human_label: Some("sam@example.com".to_owned()),
            session_id: S1.to_owned(),
            session_name: "frank-reviewer".to_owned(),
        },
        recipient_session_id: GW.to_owned(),
        summary: Some("Fix merged".to_owned()),
        body: "to: alice@example.com\nThe fix is on master; please retest Safari.".to_owned(),
        reply_to: None,
        hop_count: 0,
        created_at: "2026-10-03T17:00:00Z".to_owned(),
    }
}

fn context() -> RenderContext {
    RenderContext {
        team_name: "brigade".to_owned(),
        from: "Brigade <brigade@appshapes.com>".to_owned(),
        inbox: INBOX.to_owned(),
        public_address: "brigade-team@appshapes.com".to_owned(),
    }
}

#[test]
fn outbound_a_to_line_addresses_a_new_mail_the_subject_comes_from_the_summary() {
    let resolved = resolve_outbound(&envelope(), &threads(&[]), "brigade");
    assert_eq!(
        resolved,
        Ok(Outbound {
            address: "alice@example.com".to_owned(),
            subject: "[brigade/brigade] Fix merged".to_owned(),
            in_reply_to: None,
            text: "The fix is on master; please retest Safari.".to_owned(),
        })
    );
}

#[test]
fn outbound_a_reply_to_an_inbound_mail_threads_back_to_its_sender() {
    let in_row = ThreadRow {
        brigade_message_id: M1.to_owned(),
        direction: Direction::In,
        session_id: S1.to_owned(),
        address: "alice@example.com".to_owned(),
        mail_message_id: Some("<abc@mail.example.com>".to_owned()),
        provider_mail_id: Some("re_1".to_owned()),
        subject: Some("Login page broken on Safari".to_owned()),
        kind: None,
        actor: None,
    };
    let resolved = resolve_outbound(
        &Envelope {
            body: "Retested, passes.".to_owned(),
            reply_to: Some(M1.to_owned()),
            ..envelope()
        },
        &threads(&[in_row]),
        "brigade",
    );
    assert_eq!(
        resolved,
        Ok(Outbound {
            address: "alice@example.com".to_owned(),
            subject: "Re: Login page broken on Safari".to_owned(),
            in_reply_to: Some("<abc@mail.example.com>".to_owned()),
            text: "Retested, passes.".to_owned(),
        })
    );
}

#[test]
fn outbound_no_address_and_no_thread_is_a_reason_as_is_a_to_that_is_not_an_address() {
    let none = resolve_outbound(
        &Envelope {
            body: "hello".to_owned(),
            ..envelope()
        },
        &threads(&[]),
        "brigade",
    );
    assert!(none.is_err());
    let bad = resolve_outbound(
        &Envelope {
            body: "to: 3f9a2\nhello".to_owned(),
            ..envelope()
        },
        &threads(&[]),
        "brigade",
    );
    assert_eq!(bad, Err("\"to: 3f9a2\" is not an email address".to_owned()));
}

#[test]
fn render_outbound_writes_the_mail_with_the_block_the_reply_tag_and_the_auto_submitted_header() {
    let resolved = resolve_outbound(&envelope(), &threads(&[]), "brigade").expect("expected ok");
    let rendered = render_outbound(&envelope(), &resolved, &context());
    assert_eq!(rendered.to, "alice@example.com");
    assert_eq!(rendered.reply_to, Some(reply_tag(INBOX, M2)));
    assert!(
        rendered
            .text
            .contains("send a new email to brigade-team@appshapes.com")
    );
    let header = |name: &str| {
        rendered
            .headers
            .iter()
            .find(|(key, _)| key == name)
            .map(|(_, value)| value.as_str())
    };
    assert_eq!(header("Message-ID"), None);
    assert_eq!(header("In-Reply-To"), None);
    assert_eq!(header("Auto-Submitted"), Some("auto-generated"));
    // The sender line is the session and its label, nothing else, then the text.
    assert!(
        rendered.text.starts_with(
            "frank-reviewer · sam@example.com\n\nThe fix is on master; please retest Safari.\n\n"
        )
    );
    assert!(
        !rendered.text.contains("unverified"),
        "a mail a person reads shows the label alone"
    );
    assert!(
        !rendered.text.contains("on Brigade team"),
        "the sender line names no team"
    );
    let mut unlabelled = envelope();
    unlabelled.sender.human_label = None;
    let unlabelled = render_outbound(&unlabelled, &resolved, &context());
    assert!(
        unlabelled
            .text
            .starts_with("frank-reviewer\n\nThe fix is on master")
    );
    let block = last_block(&rendered.text).unwrap();
    assert_eq!(block["to"], S1);
    assert_eq!(block["reply-to"], M2);
    assert_eq!(block["hops"], "0");
    assert_eq!(block["from-principal"], "p1");
}

const ASK: &str = "Ship 0.43.0 with the digest, or wait for the Stop hook?\n\n- ship now\n- [default] wait\n\nAnswer by 2026-10-31.";

#[test]
fn outbound_a_decision_ask_is_mailed_as_the_command_writes_it_and_its_subject_is_the_question() {
    let env = Envelope {
        summary: Some("Ship 0.43.0 with the digest, or wait for the Stop hook?".to_owned()),
        ..envelope()
    };
    let resolved = resolve_outbound(
        &Envelope {
            body: format!("to: alice@example.com\n\n{ASK}"),
            ..env.clone()
        },
        &threads(&[]),
        "brigade",
    )
    .expect("expected ok");
    assert_eq!(resolved.text, ASK);
    assert_eq!(
        resolved.subject,
        "[brigade/brigade] Ship 0.43.0 with the digest, or wait for the Stop hook?"
    );
    let rendered = render_outbound(&env, &resolved, &context());
    assert!(
        rendered
            .text
            .starts_with(&format!("frank-reviewer · sam@example.com\n\n{ASK}\n\n"))
    );
}

#[test]
fn outbound_an_older_plugins_labelled_ask_is_mailed_in_the_same_shape_an_unknown_shape_stays_intact() {
    let labelled = [
        "Brigade decision for Alice from team brigade. Answer however you wish; the session acts on your answer and closes the decision.",
        "Decision d-20261029-a1b2c3: Ship 0.43.0 with the digest, or wait for the Stop hook?",
        "  for Alice, asked 2026-10-29 by sam@example.com (session frank-reviewer), repository brigade, card 162, answer by 2026-10-31",
        "  options: ship now | wait",
        "  default: wait",
    ]
    .join("\n");
    let env = Envelope {
        summary: Some(
            "Decision d-20261029-a1b2c3: Ship 0.43.0 with the digest, or wait for the Stop hook?".to_owned(),
        ),
        ..envelope()
    };
    let resolved = resolve_outbound(
        &Envelope {
            body: format!("to: alice@example.com\n\n{labelled}"),
            ..env.clone()
        },
        &threads(&[]),
        "brigade",
    )
    .expect("expected ok");
    assert_eq!(resolved.text, ASK);
    assert_eq!(
        resolved.subject,
        "[brigade/brigade] Ship 0.43.0 with the digest, or wait for the Stop hook?"
    );
    assert_eq!(
        decision_view(&labelled, "brigade", str::to_owned).as_deref(),
        Some(ASK)
    );
    // Another team's name, a line this view does not know, a default that is none of the options: shown as
    // sent.
    for other in [
        labelled.replace("from team brigade.", "from team other."),
        format!("{labelled}\n  note: one more line"),
        labelled.replace("  default: wait", "  default: neither"),
        ASK.to_owned(),
    ] {
        assert_eq!(decision_view(&other, "brigade", str::to_owned), None);
        let kept = resolve_outbound(
            &Envelope {
                body: format!("to: alice@example.com\n\n{other}"),
                ..env.clone()
            },
            &threads(&[]),
            "brigade",
        );
        assert_eq!(kept.map(|kept| kept.text), Ok(other));
    }
}

#[test]
fn roster_text_shows_the_sessions_online_now_both_gateways_and_offline_sessions_left_out() {
    let sessions = sessions();
    let offline = session(
        "bbbbbbbb-0000-4000-8000-0000000000bb",
        "gone-home",
        "p5",
        Some("pat@example.com"),
        "offline",
    );
    let text = roster_text("brigade", sessions.iter().chain([&offline]), GW, INBOX);
    assert!(text.contains("or a member's label"));
    assert!(text.contains("  3f9a2  frank-reviewer  idle  sam@example.com\n"));
    assert!(!text.contains("unverified"));
    assert!(text.contains("↳ reviewing the login fix"));
    assert!(!text.contains("gateway"));
    assert!(!text.contains("gone-home"));
    assert!(roster_text("brigade", [&offline], GW, INBOX).contains("(none)"));
}

#[test]
fn the_gateway_description_fits_the_protocols_cap() {
    assert!(description(INBOX).chars().count() <= 256);
}

#[test]
fn gateway_for_mail_picks_the_gateway_whose_inbox_the_mail_reached_tags_included() {
    struct Gateway {
        inbox: &'static str,
        team: &'static str,
    }
    let gateways = [
        Gateway {
            inbox: INBOX,
            team: "brigade",
        },
        Gateway {
            inbox: "other@example.resend.app",
            team: "telder",
        },
    ];
    let team =
        |to: &[String]| gateway_for_mail(&gateways, to, |gateway| gateway.inbox).map(|gateway| gateway.team);
    assert_eq!(
        team(&[
            "someone@example.com".to_owned(),
            "other@example.resend.app".to_owned()
        ]),
        Some("telder")
    );
    assert_eq!(team(&[reply_tag(INBOX, M1)]), Some("brigade"));
    assert_eq!(team(&["nobody@example.com".to_owned()]), None);
    assert!(gateway_for_mail(&[] as &[Gateway], &[INBOX], |gateway| gateway.inbox).is_none());
}

#[test]
fn a_mail_that_says_only_sessions_in_its_text_or_its_subject_is_the_sessions_command() {
    let table = threads(&[]);
    let resolve = |text: &str, subject: &str| {
        resolve_inbound(
            &InboundMail {
                subject: subject.to_owned(),
                ..mail_with(text)
            },
            INBOX,
            &table,
        )
    };
    let command = |resolved: Inbound| match resolved {
        Inbound::Command { command, .. } => Some(command),
        _ => None,
    };
    assert_eq!(
        command(resolve("sessions\n", "anything")).as_deref(),
        Some("sessions")
    );
    assert_eq!(
        command(resolve("Brigade sessions", "x")).as_deref(),
        Some("sessions")
    );
    assert_eq!(command(resolve("", "help")).as_deref(), Some("help"));
    let quoted = resolve("sessions\n\nOn Sat, Oct 4, 2026 Brigade wrote:\n> hi", "Re: x");
    assert!(
        matches!(quoted, Inbound::Command { .. }),
        "the quoted part does not count"
    );
    let prose = resolve("sessions are slow today", "sessions");
    assert!(matches!(prose, Inbound::Unaddressed { .. }));
}

#[test]
fn header_lines_address_a_mail_and_carry_a_summary() {
    let resolved = resolve_inbound(
        &InboundMail {
            subject: "x".to_owned(),
            ..mail_with("to: 3f9a2\nsummary: Login blank on Safari 18\n\nSteps: open /login, sign in.\n")
        },
        INBOX,
        &threads(&[]),
    );
    let Inbound::Message {
        target,
        summary,
        text,
        ..
    } = resolved
    else {
        panic!("expected a message, got {resolved:?}");
    };
    assert_eq!(target, "3f9a2");
    assert_eq!(summary.as_deref(), Some("Login blank on Safari 18"));
    assert_eq!(text, "Steps: open /login, sign in.");
}

#[test]
fn sender_allowed_an_empty_list_is_open_a_listed_address_or_domain_gets_in_nobody_else_does() {
    let entries = |entries: &[&str]| {
        entries
            .iter()
            .map(|entry| (*entry).to_owned())
            .collect::<Vec<_>>()
    };
    assert!(sender_allowed("anyone@stranger.example", Some(&[])));
    assert!(sender_allowed("anyone@stranger.example", None));
    assert!(sender_allowed(
        "anyone@stranger.example",
        Some(&entries(&[" ", ""]))
    ));
    let list = entries(&["QA@Example.com", "@AppShapes.com", ""]);
    let list = Some(list.as_slice());
    assert!(sender_allowed("qa@example.com", list));
    assert!(sender_allowed("Rjae@AppShapes.com", list));
    assert!(
        !sender_allowed("dev@example.com", list),
        "the address was listed, not its domain"
    );
    assert!(
        !sender_allowed("rjae@mail.appshapes.com", list),
        "the domain exactly, not a subdomain"
    );
    assert!(!sender_allowed("x@appshapes.com.evil.example", list));
    assert!(
        !sender_allowed("@appshapes.com", list),
        "no local part is no address"
    );
    assert!(!sender_allowed("", list));
}
