//! What `mail-in` and `mail-out` say and decide between their calls
//! (`supabase/functions/mail-in/index.ts`, `supabase/functions/mail-out/index.ts`).
//!
//! Inbound, a mail that cannot be routed is answered by mail, with the reason and the roster, and its
//! caller is answered 200 once the request is authentic and well-formed: a second delivery would not
//! change the outcome. A mail from a sender the team does not list is dropped unanswered (card 55).
//! Outbound, a session whose message could not be mailed is told by a Brigade message, and a failure a
//! later attempt can mend leaves the message in the inbox for the next turn.

use serde::Serialize;

use super::{Sending, failure, ignored, json, not_delivered_key, refused};
use crate::connector::{Answer, SendError};
use crate::mail::{InboundMail, OutboundMail, domain_of};
use crate::resolve::{
    BrigadeBody, Direction, Envelope, Outbound, RenderContext, Source, ThreadKind, ThreadRow,
    addressed_to_inbox,
};

/// The summary of the note that tells a session its message was not delivered.
pub const NOT_DELIVERED_SUMMARY: &str = "mail gateway: not delivered";

// ---- mail-in: the request ---------------------------------------------------------------------------------

/// The key a mail is recognised by when its provider delivers it again: the provider's own id for it, as
/// it is.
#[must_use]
pub fn received_key(mail: &InboundMail) -> &str {
    &mail.provider_id
}

/// The idempotency key of the Brigade message a mail becomes.
#[must_use]
pub fn send_key(mail: &InboundMail) -> String {
    format!("mail:{}", mail.provider_id)
}

/// The answer to a request that does not carry the connector secret.
#[must_use]
pub fn answer_unauthorized() -> Answer {
    failure(401, "the request does not carry this gateway's connector secret")
}

/// The answer to a request whose body is not the inbound shape. `reason` is
/// [`crate::from_inbound_wire`]'s.
#[must_use]
pub fn answer_malformed(reason: &str) -> Answer {
    failure(400, reason)
}

/// The answer to a mail that was delivered to no inbox of the project's gateways.
#[must_use]
pub fn answer_not_ours() -> Answer {
    ignored("not addressed to a gateway inbox of this project")
}

/// The answer to a mail from a sender the team does not list. The mail is dropped: no roster and no
/// bounce, because an answer to a forged From would make the gateway a source of backscatter and confirm
/// that the address is live.
#[must_use]
pub fn answer_sender_not_allowed() -> Answer {
    ignored("the sender is not on the team's allowed-senders list")
}

/// The line the handler logs for a mail it dropped because its sender is not listed: the sender's domain
/// alone, never the address.
#[must_use]
pub fn dropped(mail: &InboundMail, team_name: &str) -> String {
    let domain = match domain_of(&mail.from) {
        domain if domain.is_empty() => "?".to_owned(),
        domain => domain,
    };
    format!("mail-in: dropped a mail from @{domain}: not on team {team_name}'s allowed senders")
}

/// The answer to a mail that was a command for the gateway.
#[must_use]
pub fn answer_command(command: &str) -> Answer {
    #[derive(Serialize)]
    struct Command<'a> {
        command: &'a str,
    }
    json(200, &Command { command })
}

/// The answer to a mail that was sent on as a Brigade message.
#[must_use]
pub fn answer_carried(
    message_id: &str,
    recipient_session_id: &str,
    source: Source,
    duplicate: bool,
) -> Answer {
    #[derive(Serialize)]
    struct Carried<'a> {
        message_id: &'a str,
        to: &'a str,
        source: &'a str,
        duplicate: bool,
    }
    json(
        200,
        &Carried {
            message_id,
            to: recipient_session_id,
            source: source.as_str(),
            duplicate,
        },
    )
}

// ---- mail-in: a mail that is not delivered ----------------------------------------------------------------

/// Why a mail was not delivered to a session.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Undelivered<'a> {
    /// The directives at the top of the mail could not be read.
    Invalid {
        /// Why, as [`crate::Inbound::Invalid`] says it.
        reason: &'a str,
    },
    /// The mail names no session and answers none.
    Unaddressed,
    /// The mail names no session it can go to.
    NotSent {
        /// Why, as [`crate::resolve_recipient`] says it.
        error: &'a str,
    },
    /// The engine refused the message.
    Refused {
        /// The refusal's code.
        code: &'a str,
        /// What the refusal adds to its code; empty when it adds nothing.
        detail: &'a str,
    },
}

impl Undelivered<'_> {
    /// The sentence the person is told.
    #[must_use]
    pub fn reason(&self) -> String {
        match *self {
            Self::Invalid { reason } => {
                format!("The lines at the top of your email could not be read: {reason}.")
            }
            Self::Unaddressed => {
                "No session was named. Start your email with a line \"to: <session>\" (see the list below), or reply to an email a session sent you."
                    .to_owned()
            }
            Self::NotSent { error } => format!("The message was not sent: {error}."),
            Self::Refused { code, detail } => refused(code, detail),
        }
    }

    /// What the endpoint's caller is answered: `{"bounced": …}` with the kind of the failure, which for a
    /// refusal is its code.
    #[must_use]
    pub fn answer(&self) -> Answer {
        #[derive(Serialize)]
        struct Bounced<'a> {
            bounced: &'a str,
        }
        let bounced = match *self {
            Self::Invalid { .. } => "invalid",
            Self::Unaddressed => "unaddressed",
            Self::NotSent { .. } => "not_found",
            Self::Refused { code, .. } => code,
        };
        json(200, &Bounced { bounced })
    }
}

/// The mail that tells a person their mail was not delivered: the reason, then the roster
/// ([`crate::roster_text`]'s). `None` when there is nobody to tell, or when the mail came from the
/// gateway's own inbox or a reply address of it: an answer there would be a loop.
///
/// The subject repeats the mail's own, as the mail has it; nothing makes it one line.
#[must_use]
pub fn bounce(
    mail: &InboundMail,
    context: &RenderContext,
    why: &Undelivered<'_>,
    roster: &str,
) -> Option<OutboundMail> {
    if mail.from.is_empty() || addressed_to_inbox(&[&mail.from], &context.inbox) {
        return None;
    }
    let mut headers = vec![("Auto-Submitted".to_owned(), "auto-replied".to_owned())];
    if let Some(message_id) = mail.message_id.as_deref().filter(|id| !id.is_empty()) {
        headers.push(("In-Reply-To".to_owned(), message_id.to_owned()));
        headers.push(("References".to_owned(), message_id.to_owned()));
    }
    Some(OutboundMail {
        from: context.from.clone(),
        to: mail.from.clone(),
        reply_to: Some(context.inbox.clone()),
        subject: if mail.subject.is_empty() {
            "[brigade] not delivered".to_owned()
        } else {
            format!("Re: {}", mail.subject)
        },
        text: format!(
            "Your email was not delivered to a Brigade session.\n\n{}\n\n{roster}",
            why.reason()
        ),
        headers,
    })
}

/// The mail that answers a command: the roster for `sessions` and `help`, and for any other word that it
/// is none, then the roster.
///
/// Unlike [`bounce`], it is written whoever the mail came from: the TypeScript guarded only the bounce
/// against the gateway's own inbox.
#[must_use]
pub fn command_reply(
    mail: &InboundMail,
    context: &RenderContext,
    command: &str,
    roster: &str,
) -> OutboundMail {
    let text = if command == "sessions" || command == "help" {
        roster.to_owned()
    } else {
        format!("Unknown command \"{command}\". Commands: sessions, help.\n\n{roster}")
    };
    OutboundMail {
        from: context.from.clone(),
        to: mail.from.clone(),
        reply_to: Some(context.inbox.clone()),
        subject: format!("[brigade/{}] {command}", context.team_name),
        text,
        headers: vec![("Auto-Submitted".to_owned(), "auto-replied".to_owned())],
    }
}

// ---- mail-in: a mail that is sent on ----------------------------------------------------------------------

/// The Brigade message a mail becomes. `carried` is [`crate::brigade_body`]'s, and `summary` and
/// `reply_to` are the resolution's: a summary the person gave is the message's, and otherwise the body's
/// own is.
#[must_use]
pub fn send(
    mail: &InboundMail,
    recipient_session_id: &str,
    carried: &BrigadeBody,
    summary: Option<&str>,
    reply_to: Option<&str>,
) -> Sending {
    Sending {
        recipient_session_id: recipient_session_id.to_owned(),
        body: carried.body.clone(),
        summary: summary.unwrap_or(&carried.summary).to_owned(),
        reply_to: reply_to.map(str::to_owned),
        idempotency_key: send_key(mail),
    }
}

/// The thread row of a mail that was sent on, so that the session's answer finds the person and their
/// thread. `message_id` is the id the engine gave the Brigade message.
#[must_use]
pub fn thread_row_in(message_id: &str, recipient_session_id: &str, mail: &InboundMail) -> ThreadRow {
    ThreadRow {
        brigade_message_id: message_id.to_owned(),
        direction: Direction::In,
        session_id: recipient_session_id.to_owned(),
        address: mail.from.clone(),
        mail_message_id: mail.message_id.clone(),
        provider_mail_id: Some(mail.provider_id.clone()),
        subject: Some(mail.subject.clone()).filter(|subject| !subject.is_empty()),
        kind: Some(ThreadKind::Email),
        actor: None,
    }
}

// ---- mail-out ---------------------------------------------------------------------------------------------

fn note(env: &Envelope, body: String) -> Sending {
    Sending {
        recipient_session_id: env.sender.session_id.clone(),
        body,
        summary: NOT_DELIVERED_SUMMARY.to_owned(),
        reply_to: Some(env.message_id.clone()),
        idempotency_key: not_delivered_key(&env.message_id),
    }
}

/// The note to a session whose message names no address. `reason` is [`crate::resolve_outbound`]'s.
#[must_use]
pub fn undeliverable(env: &Envelope, reason: &str) -> Sending {
    note(
        env,
        format!(
            "The mail gateway could not deliver your message {}: {reason}",
            env.message_id
        ),
    )
}

/// The note to a session whose message the provider refused for good. `reason` is the failure's text.
#[must_use]
pub fn not_mailed(env: &Envelope, address: &str, reason: &str) -> Sending {
    note(
        env,
        format!(
            "The mail gateway could not deliver your message {} to {address}: {reason}",
            env.message_id
        ),
    )
}

/// Whether a failed send ends a message's delivery: the session is told and the message acknowledged. A
/// failure that does not leaves the message in the inbox for the next turn.
///
/// Mail is the other way round from Slack here: only a failure that says no later attempt can succeed is
/// final, so a send that got no answer is tried again.
#[must_use]
pub fn is_final(error: &SendError) -> bool {
    !error.retryable
}

/// The thread row of a session's message that was mailed, so that a person's reply finds the session.
/// `provider_id` is the provider's id for the sent mail and `sent` the mail as it was rendered. The row
/// has no Message-ID: a provider's relay assigns it, and a reply is found by the Reply-To tag.
#[must_use]
pub fn thread_row_out(env: &Envelope, out: &Outbound, provider_id: &str, sent: &OutboundMail) -> ThreadRow {
    ThreadRow {
        brigade_message_id: env.message_id.clone(),
        direction: Direction::Out,
        session_id: env.sender.session_id.clone(),
        address: out.address.clone(),
        mail_message_id: None,
        provider_mail_id: Some(provider_id.to_owned()),
        subject: Some(sent.subject.clone()),
        kind: Some(ThreadKind::Email),
        actor: None,
    }
}
