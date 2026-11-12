//! What the four handlers say and decide between their calls (`supabase/functions/slack-in/index.ts`,
//! `slack-out/index.ts`, `mail-in/index.ts` and `mail-out/index.ts`).
//!
//! A handler is orchestration: it takes a request or a message from the gateway's inbox, calls Slack or a
//! mail provider, sends into the team and writes the thread table. What it tells a person, a session and
//! its own caller on the way, and each small decision between two calls, is here, so that a handler holds
//! the calls and their order and nothing else. The TypeScript has no tests of its handlers; the tests of
//! this module are read off the handlers' own strings.
//!
//! - [`slack`] is `slack-in` and `slack-out`.
//! - [`mail`] is `mail-in` and `mail-out`.
//! - What the two gateways say and decide alike is in this module itself.

use serde::Serialize;

use crate::connector::{Answer, stringify};
use crate::resolve::{Direction, ThreadRow};

pub mod mail;
pub mod slack;

/// How many messages one turn takes from a gateway's inbox.
pub const INBOX_BATCH: usize = 50;

/// A Brigade message for a gateway's session to send: a person's words to a session, or a note to a
/// session whose message was not delivered.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Sending {
    /// The session the message is for.
    pub recipient_session_id: String,
    /// The body.
    pub body: String,
    /// The summary.
    pub summary: String,
    /// The Brigade message this one answers. It is what the person typed after `reply-to:` when they
    /// typed one, so it can be the empty string or no message's id at all, and the engine is asked with it
    /// as it is: the TypeScript's send was refused then, and the person told so.
    pub reply_to: Option<String>,
    /// What makes a second send of the same thing the first one.
    pub idempotency_key: String,
}

impl Sending {
    /// Whether a send the engine refused with `code` is sent once more as [`Sending::unthreaded`]: it was
    /// refused as `not_found` and it names a message it answers. That message is gone (retention); the
    /// text still reaches the session.
    #[must_use]
    pub fn sends_again_unthreaded(&self, code: &str) -> bool {
        code == "not_found" && self.reply_to.as_deref().is_some_and(|id| !id.is_empty())
    }

    /// The same message, answering none.
    #[must_use]
    pub fn unthreaded(&self) -> Self {
        Self {
            reply_to: None,
            ..self.clone()
        }
    }
}

/// Whether a message from the gateway's inbox was carried out on an earlier turn that ended before the
/// acknowledgement: its thread row is there and says `out`. It is acknowledged and never sent twice.
#[must_use]
pub fn already_carried(row: Option<&ThreadRow>) -> bool {
    row.is_some_and(|row| row.direction == Direction::Out)
}

/// The idempotency key of the note that tells a session its message was not delivered.
#[must_use]
pub fn not_delivered_key(message_id: &str) -> String {
    format!("gw-err:{message_id}")
}

/// What a person is told when the engine refused their message. `code` is the refusal's code and `detail`
/// what it adds, empty when it adds nothing.
#[must_use]
pub fn refused(code: &str, detail: &str) -> String {
    let detail = if detail.is_empty() {
        String::new()
    } else {
        format!(": {detail}")
    };
    format!("Brigade refused the message ({code}{detail}). Try again later.")
}

/// The answer to a request that is not a POST.
#[must_use]
pub fn answer_method_not_allowed() -> Answer {
    failure(405, "method not allowed")
}

/// The answer to a request that was received before: a provider and Slack both deliver again.
#[must_use]
pub fn answer_duplicate() -> Answer {
    #[derive(Serialize)]
    struct Duplicate {
        duplicate: bool,
    }
    json(200, &Duplicate { duplicate: true })
}

/// The answer to a request the handler failed on. `error` is what went wrong.
#[must_use]
pub fn answer_failed(error: &str) -> Answer {
    failure(500, error)
}

/// An answer whose body is one of this module's shapes.
fn json(status: u16, shape: &impl Serialize) -> Answer {
    Answer {
        status,
        body: stringify(shape),
    }
}

/// `{"error": …}`.
fn failure(status: u16, error: &str) -> Answer {
    #[derive(Serialize)]
    struct Failure<'a> {
        error: &'a str,
    }
    json(status, &Failure { error })
}

/// `{"ignored": …}`, answered with 200.
fn ignored(reason: &str) -> Answer {
    #[derive(Serialize)]
    struct Ignored<'a> {
        ignored: &'a str,
    }
    json(200, &Ignored { ignored: reason })
}
