//! What `mail-in` and `mail-out` say and decide between their calls
//! (`supabase/functions/mail-in/index.ts`, `supabase/functions/mail-out/index.ts`). The TypeScript has no
//! tests of its handlers; these are the strings and the decisions the two handlers hold, read off their
//! source.

use brigade_gateway::notes::Sending;
use brigade_gateway::notes::mail::{
    NOT_DELIVERED_SUMMARY, Undelivered, answer_carried, answer_command, answer_malformed, answer_not_ours,
    answer_sender_not_allowed, answer_unauthorized, bounce, command_reply, dropped, is_final, not_mailed,
    received_key, send, send_key, thread_row_in, thread_row_out, undeliverable,
};
use brigade_gateway::{
    Answer, BrigadeBody, Direction, Envelope, InboundMail, Outbound, OutboundMail, RenderContext, SendError,
    Sender, Source, ThreadKind, ThreadRow,
};

const INBOX: &str = "a1b2c3d4@example.resend.app";
const S1: &str = "6f6f6f6f-6f6f-4f6f-8f6f-6f6f6f6f3f9a2";
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

fn mail() -> InboundMail {
    InboundMail {
        provider_id: "re_1".to_owned(),
        from: "alice@example.com".to_owned(),
        from_name: some("Alice"),
        to: vec![INBOX.to_owned()],
        subject: "Login page broken on Safari".to_owned(),
        message_id: some("<abc@mail.example.com>"),
        in_reply_to: None,
        references: Vec::new(),
        text: "Steps: open /login, submit, blank page.".to_owned(),
        received_at: "2026-10-03T16:00:00Z".to_owned(),
    }
}

fn context() -> RenderContext {
    RenderContext {
        team_name: "ops".to_owned(),
        from: "Brigade <brigade@appshapes.com>".to_owned(),
        inbox: INBOX.to_owned(),
        public_address: "brigade@appshapes.com".to_owned(),
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
        body: "to: alice@example.com\nThe fix is on master.".to_owned(),
        reply_to: None,
        hop_count: 0,
        created_at: "2026-10-03T17:00:00Z".to_owned(),
    }
}

fn header(name: &str, value: &str) -> (String, String) {
    (name.to_owned(), value.to_owned())
}

// ---- mail-in: the request ----------------------------------------------------------------------------------

#[test]
fn a_mail_is_recognised_by_its_providers_id_and_sent_on_under_a_key_made_of_it() {
    assert_eq!(received_key(&mail()), "re_1");
    assert_eq!(send_key(&mail()), "mail:re_1");
}

#[test]
fn the_answers_a_connector_is_given() {
    assert_eq!(
        answer_unauthorized(),
        answer(
            401,
            r#"{"error":"the request does not carry this gateway's connector secret"}"#
        )
    );
    assert_eq!(
        answer_malformed("from must be an email address"),
        answer(400, r#"{"error":"from must be an email address"}"#)
    );
    assert_eq!(
        answer_not_ours(),
        answer(
            200,
            r#"{"ignored":"not addressed to a gateway inbox of this project"}"#
        )
    );
    assert_eq!(
        answer_sender_not_allowed(),
        answer(
            200,
            r#"{"ignored":"the sender is not on the team's allowed-senders list"}"#
        )
    );
    assert_eq!(
        answer_command("sessions"),
        answer(200, r#"{"command":"sessions"}"#)
    );
    assert_eq!(
        answer_carried(M1, S1, Source::ReplyAddress, true),
        answer(
            200,
            &format!(r#"{{"message_id":"{M1}","to":"{S1}","source":"reply-address","duplicate":true}}"#)
        )
    );
}

#[test]
fn a_dropped_mail_is_logged_by_its_senders_domain_alone() {
    assert_eq!(
        dropped(&mail(), "ops"),
        "mail-in: dropped a mail from @example.com: not on team ops's allowed senders"
    );
    let from = |from: &str| InboundMail {
        from: from.to_owned(),
        ..mail()
    };
    assert_eq!(
        dropped(&from("Mallory@Evil.Test"), "Dev <2>"),
        "mail-in: dropped a mail from @evil.test: not on team Dev <2>'s allowed senders"
    );
    assert_eq!(
        dropped(&from("noat"), "ops"),
        "mail-in: dropped a mail from @?: not on team ops's allowed senders"
    );
}

// ---- mail-in: a mail that is not delivered -----------------------------------------------------------------

#[test]
fn the_four_reasons_a_mail_is_not_delivered_and_what_the_connector_is_answered() {
    let invalid = Undelivered::Invalid {
        reason: "the line \"to:\" appears twice at the top of the text",
    };
    assert_eq!(
        invalid.reason(),
        "The lines at the top of your email could not be read: the line \"to:\" appears twice at the top of the text."
    );
    assert_eq!(invalid.answer(), answer(200, r#"{"bounced":"invalid"}"#));
    assert_eq!(
        Undelivered::Unaddressed.reason(),
        "No session was named. Start your email with a line \"to: <session>\" (see the list below), or reply to an email a session sent you."
    );
    assert_eq!(
        Undelivered::Unaddressed.answer(),
        answer(200, r#"{"bounced":"unaddressed"}"#)
    );
    let not_sent = Undelivered::NotSent {
        error: "no session ends with \"zzzzz\", and no member has that label",
    };
    assert_eq!(
        not_sent.reason(),
        "The message was not sent: no session ends with \"zzzzz\", and no member has that label."
    );
    assert_eq!(not_sent.answer(), answer(200, r#"{"bounced":"not_found"}"#));
    let refused = Undelivered::Refused {
        code: "rate_limited",
        detail: "slow down",
    };
    assert_eq!(
        refused.reason(),
        "Brigade refused the message (rate_limited: slow down). Try again later."
    );
    assert_eq!(refused.answer(), answer(200, r#"{"bounced":"rate_limited"}"#));
    let bare = Undelivered::Refused {
        code: "forbidden",
        detail: "",
    };
    assert_eq!(
        bare.reason(),
        "Brigade refused the message (forbidden). Try again later."
    );
}

#[test]
fn a_bounce_answers_the_mail_with_the_reason_and_the_roster() {
    let bounced = bounce(&mail(), &context(), &Undelivered::Unaddressed, "ROSTER").unwrap();
    assert_eq!(
        bounced,
        OutboundMail {
            from: "Brigade <brigade@appshapes.com>".to_owned(),
            to: "alice@example.com".to_owned(),
            reply_to: some(INBOX),
            subject: "Re: Login page broken on Safari".to_owned(),
            text: "Your email was not delivered to a Brigade session.\n\nNo session was named. Start your email with a line \"to: <session>\" (see the list below), or reply to an email a session sent you.\n\nROSTER".to_owned(),
            headers: vec![
                header("Auto-Submitted", "auto-replied"),
                header("In-Reply-To", "<abc@mail.example.com>"),
                header("References", "<abc@mail.example.com>"),
            ],
        }
    );
    // A mail with no subject and no Message-ID: a subject of the gateway's, and nothing to thread under.
    let bare = InboundMail {
        subject: String::new(),
        message_id: None,
        ..mail()
    };
    let bounced = bounce(&bare, &context(), &Undelivered::Unaddressed, "ROSTER").unwrap();
    assert_eq!(bounced.subject, "[brigade] not delivered");
    assert_eq!(bounced.headers, [header("Auto-Submitted", "auto-replied")]);
    let empty_id = InboundMail {
        message_id: some(""),
        ..mail()
    };
    assert_eq!(
        bounce(&empty_id, &context(), &Undelivered::Unaddressed, "R")
            .unwrap()
            .headers
            .len(),
        1
    );
    // The subject is repeated as the mail has it.
    let two_lines = InboundMail {
        subject: "Re: two\nlines".to_owned(),
        ..mail()
    };
    assert_eq!(
        bounce(&two_lines, &context(), &Undelivered::Unaddressed, "R")
            .unwrap()
            .subject,
        "Re: Re: two\nlines"
    );
}

#[test]
fn a_bounce_never_answers_the_gateways_own_inbox_or_nobody() {
    let from = |from: &str| InboundMail {
        from: from.to_owned(),
        ..mail()
    };
    let bounced = |mail: &InboundMail| bounce(mail, &context(), &Undelivered::Unaddressed, "R");
    assert_eq!(bounced(&from(INBOX)), None);
    assert_eq!(bounced(&from("A1B2C3D4@example.resend.app")), None);
    assert_eq!(bounced(&from(&format!("a1b2c3d4+{M1}@example.resend.app"))), None);
    assert_eq!(bounced(&from("")), None);
    // The address people are told to write to is not the inbox: a mail from it is answered.
    assert!(bounced(&from("brigade@appshapes.com")).is_some());
    assert!(bounced(&from("a1b2c3d4@other.example")).is_some());
}

#[test]
fn a_command_is_answered_with_the_roster_whoever_sent_it() {
    assert_eq!(
        command_reply(&mail(), &context(), "sessions", "ROSTER"),
        OutboundMail {
            from: "Brigade <brigade@appshapes.com>".to_owned(),
            to: "alice@example.com".to_owned(),
            reply_to: some(INBOX),
            subject: "[brigade/ops] sessions".to_owned(),
            text: "ROSTER".to_owned(),
            headers: vec![header("Auto-Submitted", "auto-replied")],
        }
    );
    assert_eq!(
        command_reply(&mail(), &context(), "help", "ROSTER").text,
        "ROSTER"
    );
    let unknown = command_reply(&mail(), &context(), "bogus", "ROSTER");
    assert_eq!(unknown.subject, "[brigade/ops] bogus");
    assert_eq!(
        unknown.text,
        "Unknown command \"bogus\". Commands: sessions, help.\n\nROSTER"
    );
    // No guard here, as there is none in the TypeScript: a command from the inbox itself is answered to it.
    let own = InboundMail {
        from: INBOX.to_owned(),
        ..mail()
    };
    assert_eq!(command_reply(&own, &context(), "help", "R").to, INBOX);
}

// ---- mail-in: a mail that is sent on -----------------------------------------------------------------------

#[test]
fn the_message_sent_on_takes_the_persons_summary_before_the_subject() {
    let carried = BrigadeBody {
        body: "the body".to_owned(),
        summary: "Login page broken on Safari".to_owned(),
    };
    assert_eq!(
        send(&mail(), S1, &carried, None, Some(M1)),
        Sending {
            recipient_session_id: S1.to_owned(),
            body: "the body".to_owned(),
            summary: "Login page broken on Safari".to_owned(),
            reply_to: some(M1),
            idempotency_key: "mail:re_1".to_owned(),
        }
    );
    let given = send(&mail(), S1, &carried, Some("My sum"), None);
    assert_eq!((given.summary.as_str(), given.reply_to), ("My sum", None));
}

#[test]
fn the_row_of_a_mail_keeps_the_address_the_message_id_and_the_subject() {
    assert_eq!(
        thread_row_in(M1, S1, &mail()),
        ThreadRow {
            brigade_message_id: M1.to_owned(),
            direction: Direction::In,
            session_id: S1.to_owned(),
            address: "alice@example.com".to_owned(),
            mail_message_id: some("<abc@mail.example.com>"),
            provider_mail_id: some("re_1"),
            subject: some("Login page broken on Safari"),
            kind: Some(ThreadKind::Email),
            actor: None,
        }
    );
    let bare = InboundMail {
        subject: String::new(),
        message_id: None,
        ..mail()
    };
    let row = thread_row_in(M1, S1, &bare);
    assert_eq!((row.subject, row.mail_message_id), (None, None));
}

// ---- mail-out ----------------------------------------------------------------------------------------------

#[test]
fn a_session_is_told_why_its_message_was_not_mailed() {
    let env = envelope();
    let note = |body: String| Sending {
        recipient_session_id: S1.to_owned(),
        body,
        summary: "mail gateway: not delivered".to_owned(),
        reply_to: some(M2),
        idempotency_key: format!("gw-err:{M2}"),
    };
    assert_eq!(
        undeliverable(&env, "\"to: 3f9a2\" is not an email address"),
        note(format!(
            "The mail gateway could not deliver your message {M2}: \"to: 3f9a2\" is not an email address"
        ))
    );
    assert_eq!(
        not_mailed(&env, "alice@example.com", "bad address"),
        note(format!(
            "The mail gateway could not deliver your message {M2} to alice@example.com: bad address"
        ))
    );
    assert_eq!(NOT_DELIVERED_SUMMARY, "mail gateway: not delivered");
}

#[test]
fn a_failed_send_is_final_only_when_it_says_no_later_attempt_can_succeed() {
    assert!(is_final(&SendError::new(422, "bad address", None)));
    assert!(is_final(&SendError::new(500, "refused for good", Some(false))));
    assert!(is_final(&SendError::new(
        0,
        "BRIGADE_MAIL_SEND_URL is not set: run the installer",
        Some(false)
    )));
    assert!(!is_final(&SendError::new(503, "later", None)));
    assert!(!is_final(&SendError::new(429, "slow down", None)));
    assert!(!is_final(&SendError::new(400, "try again", Some(true))));
    // A send that got no answer is tried again.
    assert!(!is_final(&SendError::new(0, "connector: refused", Some(true))));
}

#[test]
fn the_row_of_a_sent_mail_has_no_message_id_and_the_subject_it_was_sent_with() {
    let out = Outbound {
        address: "alice@example.com".to_owned(),
        subject: "[brigade/ops] Fix merged".to_owned(),
        in_reply_to: some("<abc@mail.example.com>"),
        text: "The fix is on master.".to_owned(),
    };
    let sent = OutboundMail {
        from: "Brigade <brigade@appshapes.com>".to_owned(),
        to: "alice@example.com".to_owned(),
        reply_to: None,
        subject: "[brigade/ops] Fix merged".to_owned(),
        text: "the rendered text".to_owned(),
        headers: Vec::new(),
    };
    assert_eq!(
        thread_row_out(&envelope(), &out, "sent-1", &sent),
        ThreadRow {
            brigade_message_id: M2.to_owned(),
            direction: Direction::Out,
            session_id: S1.to_owned(),
            address: "alice@example.com".to_owned(),
            mail_message_id: None,
            provider_mail_id: some("sent-1"),
            subject: some("[brigade/ops] Fix merged"),
            kind: Some(ThreadKind::Email),
            actor: None,
        }
    );
}
