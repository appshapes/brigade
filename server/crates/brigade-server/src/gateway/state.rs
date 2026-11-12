//! The gateways' own bookkeeping, in `<data>/gateway/state.json`.
//!
//! A gateway is an ordinary member of a team (the plan's section 4.6), so everything a team knows about it is
//! in the ordered log like any member's. What is kept here is what only the gateway knows: the credential it
//! was given when it joined and the session it registered, the rows that make a person's reply routable (which
//! Brigade message a Slack thread or a mail belongs to), and the ids of the deliveries of a provider that the
//! gateway is done with, so that a delivery made twice is carried once.
//!
//! It is one file, written whole beside its name and moved into place after every change. A team's gateway
//! carries a few messages a minute at most, and the rows are swept after ninety days, so the file stays small.
//! It holds a credential, so it is private to the server's user, like the key beside the log.

use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::Write as _;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};

use brigade_core::Timestamp;
use serde::{Deserialize, Serialize};

/// The format this build writes and the only one it reads.
const FORMAT: u32 = 1;

/// How long a thread row and a received id are kept: ninety days, as the Edge Functions keep them.
const KEPT_SECONDS: i64 = 90 * 24 * 60 * 60;

/// Which gateway a row belongs to.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Kind {
    /// The mail gateway.
    Email,
    /// The Slack gateway.
    Slack,
}

/// Which way a carried message went.
#[derive(Clone, Copy, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Direction {
    /// From a person to a session.
    In,
    /// From a session to a person.
    Out,
}

/// A gateway's membership of one team. It holds the gateway's credential, so what it prints of itself
/// leaves the credential out.
#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct Membership {
    /// The team.
    pub team_ref: String,
    /// Which gateway.
    pub kind: Kind,
    /// The team's name, as the join answered it.
    pub team_name: String,
    /// The credential the server issued to the gateway when it joined.
    pub credential: String,
    /// The gateway's principal in the team.
    pub principal_ref: String,
    /// The session it registered last; empty until it has registered one.
    pub session_id: String,
}

impl std::fmt::Debug for Membership {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("Membership")
            .field("team_ref", &self.team_ref)
            .field("kind", &self.kind)
            .field("team_name", &self.team_name)
            .field("credential", &"[redacted]")
            .field("principal_ref", &self.principal_ref)
            .field("session_id", &self.session_id)
            .finish()
    }
}

/// One carried message: what makes a reply to it routable.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Thread {
    /// The Brigade message the row describes.
    pub brigade_message_id: String,
    /// The team it was carried for.
    pub team_ref: String,
    /// Which gateway carried it.
    pub kind: Kind,
    /// Which way it went.
    pub direction: Direction,
    /// The session: the recipient of a message that came in, the sender of one that went out.
    pub session_id: String,
    /// The person's side: a mail address, or a Slack channel's id.
    pub address: String,
    /// For mail, the person's Message-ID on a mail that came in. For Slack, the thread's root timestamp.
    pub thread_id: Option<String>,
    /// The provider's own id of the carried message: a mail provider's id, a Slack event id or a posted
    /// message's timestamp.
    pub provider_id: Option<String>,
    /// For mail, the subject a reply repeats. For Slack, an excerpt.
    pub subject: Option<String>,
    /// For Slack, the person's user id, which a reply into their thread mentions.
    pub actor: Option<String>,
    /// When the row was written.
    pub created_at: Timestamp,
}

/// What a provider delivered already.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
struct Received {
    id: String,
    at: Timestamp,
}

/// The file's content.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
struct Content {
    format: u32,
    memberships: Vec<Membership>,
    threads: Vec<Thread>,
    received: Vec<Received>,
}

/// The gateways' bookkeeping, held in memory and written through on every change.
pub struct Store {
    path: PathBuf,
    content: Content,
}

impl Store {
    /// Opens the store under `data`, empty when there is none yet.
    ///
    /// # Errors
    /// A sentence for the operator. A file that does not read, or that another format wrote, is refused and
    /// never replaced: it holds the credential a gateway is a member by.
    pub fn open(data: &Path) -> Result<Self, String> {
        let dir = data.join("gateway");
        DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(&dir)
            .map_err(|error| format!("{} could not be created: {error}", dir.display()))?;
        let path = dir.join("state.json");
        let content = match fs::read(&path) {
            Ok(bytes) => {
                let content: Content = serde_json::from_slice(&bytes).map_err(|error| {
                    format!("{} does not read as the gateways' state: {error}", path.display())
                })?;
                if content.format != FORMAT {
                    return Err(format!(
                        "{} was written in a format this server does not read",
                        path.display()
                    ));
                }
                content
            }
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => Content {
                format: FORMAT,
                ..Content::default()
            },
            Err(error) => return Err(format!("{} could not be read: {error}", path.display())),
        };
        Ok(Self { path, content })
    }

    /// Writes the content beside its name and moves it into place. The directory is synced after the
    /// move, as the file was before it: a power cut then leaves the new content or the old, and never a
    /// name that points at neither.
    fn save(&self) -> Result<(), String> {
        let refused =
            |error: std::io::Error| format!("{} could not be written: {error}", self.path.display());
        let text = serde_json::to_vec(&self.content).map_err(|error| refused(error.into()))?;
        let beside = self.path.with_extension("json.new");
        let _ = fs::remove_file(&beside);
        OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&beside)
            .and_then(|mut file| {
                file.write_all(&text)?;
                file.sync_all()
            })
            .and_then(|()| fs::rename(&beside, &self.path))
            .and_then(|()| match self.path.parent() {
                Some(directory) => File::open(directory).and_then(|directory| directory.sync_all()),
                None => Ok(()),
            })
            .map_err(refused)
    }

    /// The gateway's membership of a team, when it has joined.
    #[must_use]
    pub fn membership(&self, team_ref: &str, kind: Kind) -> Option<&Membership> {
        self.content
            .memberships
            .iter()
            .find(|member| member.team_ref == team_ref && member.kind == kind)
    }

    /// Every team a gateway of this kind is a member of, in the order it joined them.
    pub fn memberships(&self, kind: Kind) -> impl Iterator<Item = &Membership> {
        self.content
            .memberships
            .iter()
            .filter(move |member| member.kind == kind)
    }

    /// Records a membership, in place of an earlier one of the same team and kind.
    ///
    /// # Errors
    /// The file could not be written.
    pub fn set_membership(&mut self, membership: Membership) -> Result<(), String> {
        self.content
            .memberships
            .retain(|member| !(member.team_ref == membership.team_ref && member.kind == membership.kind));
        self.content.memberships.push(membership);
        self.save()
    }

    /// Records the session a gateway registered.
    ///
    /// # Errors
    /// The file could not be written.
    pub fn set_session(&mut self, team_ref: &str, kind: Kind, session_id: &str) -> Result<(), String> {
        let Some(member) = self
            .content
            .memberships
            .iter_mut()
            .find(|member| member.team_ref == team_ref && member.kind == kind)
        else {
            return Ok(());
        };
        if member.session_id == session_id {
            return Ok(());
        }
        session_id.clone_into(&mut member.session_id);
        self.save()
    }

    /// Whether a provider's delivery `id` is one the gateway is done with: it was carried, or the person
    /// was given their answer. A delivery of it that comes again is a duplicate.
    #[must_use]
    pub fn delivered(&self, id: &str) -> bool {
        self.content.received.iter().any(|received| received.id == id)
    }

    /// Writes down that the gateway is done with a provider's delivery `id`. A delivery that was dropped
    /// or ignored is never written down: anyone can cause one, and the file would grow with them.
    ///
    /// # Errors
    /// The file could not be written. The delivery is then not remembered as done: the provider is
    /// answered a failure, and the delivery it makes again is gone through in full, which the engine's
    /// idempotency key for it makes safe.
    pub fn record_delivery(&mut self, id: &str, now: Timestamp) -> Result<(), String> {
        if self.delivered(id) {
            return Ok(());
        }
        self.content.received.push(Received {
            id: id.to_owned(),
            at: now,
        });
        self.save().inspect_err(|_| {
            self.content.received.pop();
        })
    }

    /// The Brigade message a provider's delivery became, when a gateway of `kind` carried it in: the row
    /// whose provider id is the delivery's. It is how a delivery that comes again finds the message the
    /// engine already holds for it, when the engine will not say which it is.
    #[must_use]
    pub fn carried_in(&self, kind: Kind, provider_id: &str) -> Option<&Thread> {
        self.newest(|thread| {
            thread.kind == kind
                && thread.direction == Direction::In
                && thread.provider_id.as_deref() == Some(provider_id)
        })
    }

    /// Records a carried message. A row for the same Brigade message is kept as it is.
    ///
    /// # Errors
    /// The file could not be written. The row is still held, and is written with the next change that is:
    /// the message was carried, and a turn that forgot it would carry it again two seconds later.
    pub fn record(&mut self, thread: Thread) -> Result<(), String> {
        if self.thread(&thread.brigade_message_id).is_some() {
            return Ok(());
        }
        self.content.threads.push(thread);
        self.save()
    }

    /// The row of a Brigade message.
    #[must_use]
    pub fn thread(&self, brigade_message_id: &str) -> Option<&Thread> {
        self.content
            .threads
            .iter()
            .find(|thread| thread.brigade_message_id == brigade_message_id)
    }

    /// The newest mail row whose Message-ID is one of `ids`.
    #[must_use]
    pub fn mail_thread(&self, ids: &[String]) -> Option<&Thread> {
        self.newest(|thread| {
            thread.kind == Kind::Email && thread.thread_id.as_ref().is_some_and(|id| ids.contains(id))
        })
    }

    /// The newest Slack row of a thread in a channel.
    #[must_use]
    pub fn slack_thread(&self, channel: &str, root: &str) -> Option<&Thread> {
        self.newest(|thread| {
            thread.kind == Kind::Slack
                && thread.address == channel
                && thread.thread_id.as_deref() == Some(root)
        })
    }

    /// The newest row that `wanted` accepts: the last one written among those with the latest time.
    fn newest(&self, wanted: impl Fn(&Thread) -> bool) -> Option<&Thread> {
        self.content
            .threads
            .iter()
            .filter(|thread| wanted(thread))
            .max_by_key(|thread| thread.created_at)
    }

    /// Lets go of the rows and the received ids that are older than ninety days.
    ///
    /// # Errors
    /// The file could not be written.
    pub fn sweep(&mut self, now: Timestamp) -> Result<(), String> {
        let oldest = now.minus_seconds(KEPT_SECONDS);
        let before = (self.content.threads.len(), self.content.received.len());
        self.content.threads.retain(|thread| thread.created_at >= oldest);
        self.content.received.retain(|received| received.at >= oldest);
        if before == (self.content.threads.len(), self.content.received.len()) {
            return Ok(());
        }
        self.save()
    }
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;

    use super::*;

    fn scratch(name: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("brigade-gateway-state-{name}-{}", std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn at(seconds: i64) -> Timestamp {
        Timestamp(seconds * 1_000_000_000)
    }

    fn thread(
        message: &str,
        kind: Kind,
        direction: Direction,
        address: &str,
        root: Option<&str>,
        when: i64,
    ) -> Thread {
        Thread {
            brigade_message_id: message.to_owned(),
            team_ref: "team".to_owned(),
            kind,
            direction,
            session_id: "session".to_owned(),
            address: address.to_owned(),
            thread_id: root.map(str::to_owned),
            provider_id: None,
            subject: None,
            actor: None,
            created_at: at(when),
        }
    }

    #[test]
    fn what_is_recorded_is_there_after_a_restart_and_private() {
        let data = scratch("restart");
        let mut store = Store::open(&data).unwrap();
        store
            .set_membership(Membership {
                team_ref: "team".to_owned(),
                kind: Kind::Slack,
                team_name: "ops".to_owned(),
                credential: "c".repeat(32),
                principal_ref: "p".to_owned(),
                session_id: String::new(),
            })
            .unwrap();
        store.set_session("team", Kind::Slack, "s1").unwrap();
        store
            .record(thread("m1", Kind::Slack, Direction::Out, "C1", Some("10.1"), 100))
            .unwrap();
        store.record_delivery("slack:E1", at(100)).unwrap();

        let again = Store::open(&data).unwrap();
        assert_eq!(again.membership("team", Kind::Slack).unwrap().session_id, "s1");
        assert!(again.membership("team", Kind::Email).is_none());
        assert_eq!(again.memberships(Kind::Slack).count(), 1);
        assert_eq!(again.thread("m1").unwrap().address, "C1");
        assert!(again.delivered("slack:E1"));
        assert!(!again.delivered("slack:E2"));
        // What a membership prints of itself is everything but the credential.
        let printed = format!("{:?}", again.membership("team", Kind::Slack).unwrap());
        assert!(
            printed.contains("ops") && printed.contains("[redacted]"),
            "{printed}"
        );
        assert!(!printed.contains(&"c".repeat(32)), "{printed}");

        let file = data.join("gateway").join("state.json");
        assert_eq!(fs::metadata(&file).unwrap().permissions().mode() & 0o777, 0o600);
        assert_eq!(
            fs::metadata(data.join("gateway")).unwrap().permissions().mode() & 0o777,
            0o700
        );
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn the_newest_row_of_a_thread_is_the_one_found() {
        let data = scratch("newest");
        let mut store = Store::open(&data).unwrap();
        store
            .record(thread(
                "out",
                Kind::Slack,
                Direction::Out,
                "C1",
                Some("10.1"),
                100,
            ))
            .unwrap();
        store
            .record(thread("in", Kind::Slack, Direction::In, "C1", Some("10.1"), 200))
            .unwrap();
        store
            .record(thread(
                "other",
                Kind::Slack,
                Direction::Out,
                "C2",
                Some("10.1"),
                300,
            ))
            .unwrap();
        store
            .record(thread(
                "mail",
                Kind::Email,
                Direction::In,
                "a@example.com",
                Some("<1@example.com>"),
                400,
            ))
            .unwrap();
        assert_eq!(store.slack_thread("C1", "10.1").unwrap().brigade_message_id, "in");
        assert!(store.slack_thread("C1", "10.2").is_none());
        // A Slack row is never the answer to a mail's Message-ID, and the other way round.
        assert_eq!(
            store
                .mail_thread(&["<1@example.com>".to_owned()])
                .unwrap()
                .brigade_message_id,
            "mail"
        );
        assert!(store.mail_thread(&["10.1".to_owned()]).is_none());
        // A second row for one Brigade message changes nothing.
        store
            .record(thread(
                "out",
                Kind::Slack,
                Direction::Out,
                "C9",
                Some("99.9"),
                500,
            ))
            .unwrap();
        assert_eq!(store.thread("out").unwrap().address, "C1");
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn rows_older_than_ninety_days_are_let_go_of() {
        let data = scratch("sweep");
        let mut store = Store::open(&data).unwrap();
        store
            .record(thread(
                "old",
                Kind::Email,
                Direction::Out,
                "a@example.com",
                None,
                0,
            ))
            .unwrap();
        store
            .record(thread(
                "new",
                Kind::Email,
                Direction::Out,
                "a@example.com",
                None,
                KEPT_SECONDS,
            ))
            .unwrap();
        store.record_delivery("old", at(0)).unwrap();
        store.record_delivery("new", at(KEPT_SECONDS)).unwrap();
        store.sweep(at(KEPT_SECONDS + 1)).unwrap();
        assert!(store.thread("old").is_none());
        assert!(store.thread("new").is_some());
        assert!(!store.delivered("old"));
        assert!(store.delivered("new"));
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn a_delivery_that_could_not_be_written_down_is_not_done_with_and_a_carried_message_stays_carried() {
        let data = scratch("unwritten");
        let mut store = Store::open(&data).unwrap();
        // Something that is not a file stands where the store writes beside its name, whoever runs the test.
        let beside = data.join("gateway").join("state.json.new");
        fs::create_dir_all(beside.join("in-the-way")).unwrap();
        assert!(
            store
                .record_delivery("slack:E1", at(100))
                .unwrap_err()
                .contains("could not be written")
        );
        assert!(!store.delivered("slack:E1"));
        let posted = thread("m1", Kind::Slack, Direction::Out, "C1", Some("10.1"), 100);
        assert!(store.record(posted).is_err());
        // What was posted is known to be posted, though the file does not say so yet.
        assert!(store.thread("m1").is_some());
        assert!(Store::open(&data).unwrap().thread("m1").is_none());

        fs::remove_dir_all(&beside).unwrap();
        // The provider delivers again, the delivery is done with this time, and the row is written with it.
        store.record_delivery("slack:E1", at(101)).unwrap();
        // Writing down what is written down already is nothing.
        store.record_delivery("slack:E1", at(102)).unwrap();
        let reopened = Store::open(&data).unwrap();
        assert!(reopened.delivered("slack:E1") && reopened.thread("m1").is_some());
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn what_a_delivery_became_is_found_by_the_providers_id_for_it() {
        let data = scratch("became");
        let mut store = Store::open(&data).unwrap();
        // The row a gateway of that kind wrote for a message that came in, and no other.
        let mut carried = thread("m1", Kind::Slack, Direction::In, "D1", Some("10.1"), 100);
        carried.provider_id = Some("E1".to_owned());
        let mut posted = thread("m2", Kind::Slack, Direction::Out, "D1", Some("10.1"), 200);
        posted.provider_id = Some("E1".to_owned());
        store.record(carried).unwrap();
        store.record(posted).unwrap();
        assert_eq!(
            store.carried_in(Kind::Slack, "E1").unwrap().brigade_message_id,
            "m1"
        );
        assert!(store.carried_in(Kind::Email, "E1").is_none());
        assert!(store.carried_in(Kind::Slack, "E2").is_none());
        let _ = fs::remove_dir_all(&data);
    }

    #[test]
    fn a_file_that_does_not_read_is_refused_and_kept() {
        let data = scratch("refused");
        fs::create_dir_all(data.join("gateway")).unwrap();
        let file = data.join("gateway").join("state.json");
        fs::write(&file, b"not json").unwrap();
        assert!(Store::open(&data).err().unwrap().contains("does not read"));
        fs::write(
            &file,
            br#"{"format":2,"memberships":[],"threads":[],"received":[]}"#,
        )
        .unwrap();
        assert!(Store::open(&data).err().unwrap().contains("format"));
        assert!(fs::read(&file).unwrap().starts_with(b"{\"format\":2"));
        let _ = fs::remove_dir_all(&data);
    }
}
