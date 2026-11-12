//! The thread table, as the rules of `brigade-gateway` read it.
//!
//! The rules name a row's members as the Edge Functions' table did, where a mail's Message-ID and a Slack
//! thread's root share one column; the store names them for what they are. This is the one place the two
//! meet. The lookups are the three the functions made, and like theirs none of them asks which team a row
//! belongs to: a row is found by what it is about.

use brigade_core::Timestamp;
use brigade_gateway::{Direction as Way, SlackThreads, ThreadKind, ThreadLookup, ThreadRow};

use super::state::{Direction, Kind, Store, Thread};

/// A row of the store, as the rules read it.
#[must_use]
pub fn row(thread: &Thread) -> ThreadRow {
    ThreadRow {
        brigade_message_id: thread.brigade_message_id.clone(),
        direction: match thread.direction {
            Direction::In => Way::In,
            Direction::Out => Way::Out,
        },
        session_id: thread.session_id.clone(),
        address: thread.address.clone(),
        mail_message_id: thread.thread_id.clone(),
        provider_mail_id: thread.provider_id.clone(),
        subject: thread.subject.clone(),
        kind: Some(match thread.kind {
            Kind::Email => ThreadKind::Email,
            Kind::Slack => ThreadKind::Slack,
        }),
        actor: thread.actor.clone(),
    }
}

/// A row the rules wrote for a message carried for `team_ref` at `now`, as the store keeps it. A row that
/// names no gateway is mail's, as it was in the functions' table.
#[must_use]
pub fn thread(team_ref: &str, row: &ThreadRow, now: Timestamp) -> Thread {
    Thread {
        brigade_message_id: row.brigade_message_id.clone(),
        team_ref: team_ref.to_owned(),
        kind: match row.kind {
            Some(ThreadKind::Slack) => Kind::Slack,
            Some(ThreadKind::Email) | None => Kind::Email,
        },
        direction: match row.direction {
            Way::In => Direction::In,
            Way::Out => Direction::Out,
        },
        session_id: row.session_id.clone(),
        address: row.address.clone(),
        thread_id: row.mail_message_id.clone(),
        provider_id: row.provider_mail_id.clone(),
        subject: row.subject.clone(),
        actor: row.actor.clone(),
        created_at: now,
    }
}

impl ThreadLookup for Store {
    fn by_brigade_id(&self, id: &str) -> Option<ThreadRow> {
        self.thread(id).map(row)
    }

    fn by_mail_ids(&self, ids: &[String]) -> Option<ThreadRow> {
        self.mail_thread(ids).map(row)
    }
}

impl SlackThreads for Store {
    fn by_thread(&self, channel: &str, thread_ts: &str) -> Option<ThreadRow> {
        self.slack_thread(channel, thread_ts).map(row)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::gateway::testing::scratch;

    fn written(message: &str, kind: Option<ThreadKind>, direction: Way) -> ThreadRow {
        ThreadRow {
            brigade_message_id: message.to_owned(),
            direction,
            session_id: "session".to_owned(),
            address: "C0GENERAL1".to_owned(),
            mail_message_id: Some("1700000000.000100".to_owned()),
            provider_mail_id: Some("1700000000.000200".to_owned()),
            subject: Some("the migration".to_owned()),
            kind,
            actor: Some("U0PERSON01".to_owned()),
        }
    }

    #[test]
    fn a_row_the_rules_wrote_is_read_back_as_it_was_written() {
        let data = scratch("threads-round");
        let mut store = Store::open(&data).unwrap();
        let slack = written("m-slack", Some(ThreadKind::Slack), Way::Out);
        store
            .record(thread("team", &slack, Timestamp(1_000_000_000)))
            .unwrap();
        assert_eq!(store.by_brigade_id("m-slack"), Some(slack.clone()));
        assert_eq!(store.thread("m-slack").unwrap().team_ref, "team");
        assert_eq!(store.by_thread("C0GENERAL1", "1700000000.000100"), Some(slack));
        assert_eq!(store.by_thread("C0GENERAL1", "1700000000.000200"), None);
        assert_eq!(store.by_brigade_id("m-other"), None);
        let _ = std::fs::remove_dir_all(&data);
    }

    #[test]
    fn a_row_that_names_no_gateway_is_mails_and_a_slack_row_answers_no_message_id() {
        let data = scratch("threads-mail");
        let mut store = Store::open(&data).unwrap();
        let mut mail = written("m-mail", None, Way::In);
        mail.address = "pat@example.com".to_owned();
        mail.mail_message_id = Some("<1@example.com>".to_owned());
        mail.actor = None;
        store
            .record(thread("team", &mail, Timestamp(1_000_000_000)))
            .unwrap();
        let mut slack = written("m-slack", Some(ThreadKind::Slack), Way::In);
        slack.mail_message_id = Some("<2@example.com>".to_owned());
        store
            .record(thread("team", &slack, Timestamp(2_000_000_000)))
            .unwrap();
        // What comes back says which gateway it is, where what went in left it out.
        mail.kind = Some(ThreadKind::Email);
        let ids = |ids: &[&str]| ids.iter().map(|id| (*id).to_owned()).collect::<Vec<_>>();
        assert_eq!(
            store.by_mail_ids(&ids(&["<0@example.com>", "<1@example.com>"])),
            Some(mail)
        );
        assert_eq!(store.by_mail_ids(&ids(&["<2@example.com>"])), None);
        assert_eq!(store.by_mail_ids(&[]), None);
        let _ = std::fs::remove_dir_all(&data);
    }
}
