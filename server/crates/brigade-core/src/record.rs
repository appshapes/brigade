//! The records of the log: everything the state is made of.
//!
//! A request that changes something becomes one or more records. The records are appended to the log and
//! synced before the request is answered, and the state is what [`crate::state::State::apply`] makes of them
//! in order. A record carries the time it was made at and every new identifier, so applying it reads no clock
//! and draws nothing: any instance that reads the same records reaches the same state.

use serde::{Deserialize, Serialize};

use crate::time::Timestamp;

/// The version of the record format. A reader refuses a record of a version it does not know.
pub const RECORD_VERSION: u32 = 1;

/// The longest an entry is as the bytes the log carries, before its seal. The longest is an accepted
/// message whose every character is one JSON writes as six: a body of 16,384 bytes becomes 98,304, and its
/// summary, names, label and key a few thousand more. The log must take a record this long, and the server
/// refuses to start on one that does not. A test builds the record and holds it under this number.
pub const LONGEST_ENCODED_BYTES: usize = 104 * 1024;

/// One entry of the log: the records one request made, with its number. The records of a request are one
/// entry so that the log holds all of them or none, and entries are numbered so that a reader knows when
/// it was handed one twice or was not handed one at all. The number is inside what the seal covers.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Entry {
    /// The entry's number: one more than the entry before it, from one.
    pub sequence: u64,
    /// The records, in the order they were applied.
    pub records: Vec<Record>,
}

impl Entry {
    /// The entry as the bytes the log carries, before its seal.
    ///
    /// # Panics
    /// Never: an entry holds only strings, numbers and lists of them.
    #[must_use]
    pub fn encode(&self) -> Vec<u8> {
        serde_json::to_vec(self).expect("an entry is always JSON")
    }

    /// Reads an entry back from the log.
    ///
    /// # Errors
    /// A message that names what is wrong, when the bytes are not an entry of records this build knows.
    pub fn decode(bytes: &[u8]) -> Result<Self, String> {
        let entry: Self = serde_json::from_slice(bytes).map_err(|error| format!("not an entry: {error}"))?;
        if let Some(record) = entry
            .records
            .iter()
            .find(|record| record.version != RECORD_VERSION)
        {
            return Err(format!(
                "a record of version {}, and this build reads version {RECORD_VERSION}",
                record.version
            ));
        }
        Ok(entry)
    }
}

/// One record of the log.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Record {
    /// The record format's version.
    pub version: u32,
    /// When the request that made the record was answered, by the clock of the instance that took it.
    pub at: Timestamp,
    /// What happened.
    #[serde(flatten)]
    pub event: Event,
}

impl Record {
    /// A record of this version.
    #[must_use]
    pub fn new(at: Timestamp, event: Event) -> Self {
        Self {
            version: RECORD_VERSION,
            at,
            event,
        }
    }

    /// The record as the bytes the log carries.
    ///
    /// # Panics
    /// Never: a record holds only strings, numbers and lists of them.
    #[must_use]
    pub fn encode(&self) -> Vec<u8> {
        serde_json::to_vec(self).expect("a record is always JSON")
    }

    /// Reads a record back from the log.
    ///
    /// # Errors
    /// A message that names what is wrong, when the bytes are not a record of a version this build knows.
    pub fn decode(bytes: &[u8]) -> Result<Self, String> {
        let record: Self = serde_json::from_slice(bytes).map_err(|error| format!("not a record: {error}"))?;
        if record.version != RECORD_VERSION {
            return Err(format!(
                "a record of version {}, and this build reads version {RECORD_VERSION}",
                record.version
            ));
        }
        Ok(record)
    }
}

/// A session's facts as a registration reported them.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionFacts {
    /// The session's display name.
    pub session_name: String,
    /// The session's own claim about its work.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_description: Option<String>,
    /// `busy` or `idle`.
    pub activity: String,
    /// `accept`, `hold` or `refuse`.
    pub inbound: String,
    /// The lease granted, in seconds.
    pub lease_seconds: i64,
    /// The registering harness.
    pub harness: String,
    /// The harness's version.
    pub harness_version: String,
    /// The workspace's label.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub workspace_label: Option<String>,
    /// The model identity the harness reported.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    /// The harness's count of the tokens its context holds.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_used_tokens: Option<i64>,
    /// The version of the Brigade harness.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub brigade_version: Option<String>,
    /// The descriptor of the session's folder-sync adapter.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sync_peer: Option<String>,
}

/// What a heartbeat changed. A member that is absent was left as it stood.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct SessionChanges {
    /// `busy` or `idle`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub activity: Option<String>,
    /// A new display name.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_name: Option<String>,
    /// A new description.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub session_description: Option<String>,
    /// `accept`, `hold` or `refuse`.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub inbound: Option<String>,
    /// A new lease, in seconds.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub lease_seconds: Option<i64>,
    /// The model identity.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub model: Option<String>,
    /// The context's token count.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub context_used_tokens: Option<i64>,
    /// The Brigade harness's version.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub brigade_version: Option<String>,
    /// The sync adapter's descriptor.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sync_peer: Option<String>,
}

/// A message as it was accepted, with every member the server stamped (BAP/1 4.5.5).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct AcceptedMessage {
    /// The message's id.
    pub message_id: String,
    /// The team both sessions are in.
    pub team_ref: String,
    /// The sender's principal.
    pub sender_principal_ref: String,
    /// The sender's label when the message was accepted; empty when it had none.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub sender_human_label: String,
    /// The sending session.
    pub sender_session_id: String,
    /// The sending session's name when the message was accepted.
    pub sender_session_name: String,
    /// The receiving session.
    pub recipient_session_id: String,
    /// The one-line summary; empty when the sender gave none.
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub summary: String,
    /// The text.
    pub body: String,
    /// The message this one answers.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub reply_to: Option<String>,
    /// The hop count the server computed (BAP/1 4.5.12).
    pub hop_count: u32,
    /// The sender's idempotency key, and the fingerprint of what it was first used for.
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub idempotency: Option<Idempotency>,
}

/// An idempotency key and the fingerprint of the request it was first used for (BAP/1 4.5.4).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Idempotency {
    /// The sender's key.
    pub key: String,
    /// The hash of the recipient, the body, the summary and the message answered.
    pub fingerprint: String,
}

/// The ticket of a move as the server a team leaves keeps it, in the record of where the team went.
///
/// It is the one secret a server keeps as it is and not as a hash: a member's machine has to present it
/// at the backend the team went to, so the server that hands it out has to hold it (BAP/1 4.4.10, the
/// contract's section 3c). It is written whole to the log and to a snapshot, which is why the type is its
/// own: it prints as nothing of itself, so that no line and no debug rendering of a record or a state
/// holds it, and the one way to its text says that it is being exposed.
#[derive(Clone, PartialEq, Eq, Serialize, Deserialize)]
#[serde(transparent)]
pub struct KeptTicket(String);

impl KeptTicket {
    /// A ticket to keep.
    #[must_use]
    pub fn new(ticket: &str) -> Self {
        Self(ticket.to_owned())
    }

    /// The whole ticket, for the one use it has: the answer to an active member that asks for it.
    #[must_use]
    pub fn expose(&self) -> &str {
        &self.0
    }
}

impl std::fmt::Debug for KeptTicket {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("[redacted]")
    }
}

/// What a record says happened.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
#[serde(tag = "kind")]
pub enum Event {
    /// A credential was issued to a principal, which this creates when it is new. The log holds the
    /// credential's hash and never the credential.
    #[serde(rename = "credential.issued")]
    CredentialIssued {
        /// The principal the credential names.
        principal_ref: String,
        /// The SHA-256 of the credential.
        credential_hash: String,
    },
    /// A credential became unknown to the server. Its principal's memberships are as they were.
    #[serde(rename = "credential.revoked")]
    CredentialRevoked {
        /// The SHA-256 of the credential.
        credential_hash: String,
    },
    /// A team was created.
    #[serde(rename = "team.created")]
    TeamCreated {
        /// The team's reference.
        team_ref: String,
        /// The team's display name.
        team_name: String,
        /// The SHA-256 of the whole join secret.
        join_secret_hash: String,
        /// The principal that created it, or that took it in from another backend by its own request,
        /// which is its first owner. Empty for a team the server's operator took in, which nobody here
        /// created: such a team has no owner until a `team.owner_changed` names one.
        created_by: String,
        /// Whether the team exists on another backend and was taken in by a request, with the join
        /// secret it has there. It is written only when it was, and the team is the same either way: it
        /// is what lets the server say how a team came to be here.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        taken_in: bool,
    },
    /// A team was given a new join secret. The secret before it no longer joins, and every member is as it
    /// was.
    #[serde(rename = "team.secret_rotated")]
    TeamSecretRotated {
        /// The team.
        team_ref: String,
        /// The SHA-256 of the whole new join secret.
        join_secret_hash: String,
        /// The team's count of secrets, this one included.
        secret_version: u32,
    },
    /// A team has another owner: the one its owner transferred it to, the first member to join a team that
    /// has none, or the one the server's operator named. Or it has none any more, because the operator
    /// took the team back.
    #[serde(rename = "team.owner_changed")]
    TeamOwnerChanged {
        /// The team.
        team_ref: String,
        /// The principal that owns it from now on. Empty when the operator took the team back: it has no
        /// owner until the first member joins it over HTTP.
        principal_ref: String,
    },
    /// A team's move from another backend was opened, by the server's operator or by the team's owner:
    /// until `ends_at`, or until the move is closed, a join is let in with the move ticket as it is with
    /// the join secret, except that it never makes an owner. The log holds the ticket's hash and never the
    /// ticket. A team has one move at most: a move that is opened takes the place of the one the team had,
    /// whose ticket admits nobody from then on.
    #[serde(rename = "team.move_opened")]
    TeamMoveOpened {
        /// The team.
        team_ref: String,
        /// The SHA-256 of the whole move ticket.
        ticket_hash: String,
        /// When the ticket stops admitting.
        ends_at: Timestamp,
        /// Whether the team's owner opened the move, by a request made with its credential (the
        /// contract's section 3b), and not the server's operator from the command line. It is written
        /// only when it is true, so the operator's record is the record it has always been. The owner's
        /// own request ends the retry key the team keeps for that owner; the operator's does not.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        by_owner: bool,
    },
    /// A team's move was closed, and its ticket admits nobody from now on: by the server's operator, by
    /// the team's owner, or by a rotation of the team's join secret, in the entry of the rotation.
    #[serde(rename = "team.move_closed")]
    TeamMoveClosed {
        /// The team.
        team_ref: String,
        /// Whether the team's owner closed the move, by a request made with its credential, and neither
        /// the server's operator nor a rotation. It is written only when it is true, and is read as
        /// [`Event::TeamMoveOpened`]'s is.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        by_owner: bool,
    },
    /// The team's owner recorded, on this server, that the team went to another backend: where, and the
    /// move ticket that admits a member there. It is the record a member's session follows its team on
    /// (BAP/1 4.4.10, the contract's section 3c), and it changes nothing else about the team: its members,
    /// its sessions and its join secret are as they were. A team has one such record at most: a second
    /// takes the place of the first. The log holds the ticket itself, the one secret it holds whole,
    /// because an active member is handed it.
    #[serde(rename = "team.move_recorded")]
    TeamMoveRecorded {
        /// The team.
        team_ref: String,
        /// The adapter of the backend the team went to, as its team file names it.
        adapter: String,
        /// The url of that backend.
        url: String,
        /// The pin of that backend's certificate, when its team file carries one. It is written only
        /// then.
        #[serde(default, skip_serializing_if = "String::is_empty")]
        certificate: String,
        /// The move ticket: a join secret of the team at the backend it went to.
        ticket: KeptTicket,
    },
    /// The record of where a team went was cleared: by the team's owner, in an entry of its own, or by the
    /// server's operator that takes the team back, in the entry of the take-back. The server says nothing
    /// of a move from now on.
    #[serde(rename = "team.move_cleared")]
    TeamMoveCleared {
        /// The team.
        team_ref: String,
    },
    /// The server's operator removed a team, with everything the server held of it: its name, the hash of
    /// its join secret, its owner, every membership it ever had, every session with the messages it held,
    /// the ticket of its move, the record of where it went and the retry key it kept. The server holds no
    /// team of that reference from now on, so the reference is free: a team is taken in under it as under
    /// one the server never held. The principals that were its members stay, with their credentials: they
    /// are the server's, and a principal may be a member of other teams. It is one record however much
    /// the team held, because an entry has a longest size: what goes is what the state holds of the team
    /// when the record is applied. No request makes it.
    #[serde(rename = "team.removed")]
    TeamRemoved {
        /// The team.
        team_ref: String,
    },
    /// A principal became an active member of a team: a new membership, or one that was revoked and is
    /// active again with the day it first joined. It joined with the secret the team has now, whose
    /// version the membership keeps.
    #[serde(rename = "member.joined")]
    MemberJoined {
        /// The team.
        team_ref: String,
        /// The principal.
        principal_ref: String,
        /// The label the request carried. Empty keeps the label a returning member had.
        #[serde(default, skip_serializing_if = "String::is_empty")]
        human_label: String,
        /// Whether the join was let in with the team's move ticket and not with its join secret. It is
        /// written only when it was, and the membership is the same either way: it is what lets the server
        /// say how a member came in.
        #[serde(default, skip_serializing_if = "std::ops::Not::not")]
        by_move_ticket: bool,
        /// The SHA-256 of the retry key of the take-in that made this member, when that request carried
        /// one, made the principal and made it the team's owner: the log holds the key's hash and never
        /// the key. It is what lets the machine that took the team in make the request again, when its
        /// answer was lost, and be answered as this principal (the contract's section 3b). The record of
        /// a take-in made again carries it too: that entry makes the principal's credential unknown,
        /// which ends a key, and this record keeps it for the answer that may be lost in its turn. It is
        /// written only then, so every other join is the record it has always been, and a build that
        /// does not know the member reads the record without it. A join without it, by a principal a
        /// team keeps a key for, is that principal's own, made with its credential: it ends the key.
        #[serde(default, skip_serializing_if = "String::is_empty")]
        retry_key_hash: String,
    },
    /// A principal left a team: its membership is revoked and its open sessions there are closed.
    #[serde(rename = "member.left")]
    MemberLeft {
        /// The team.
        team_ref: String,
        /// The principal.
        principal_ref: String,
    },
    /// The team's owner ended a principal's membership, or lifted its ban: the membership is revoked and
    /// is banned or not as the record says, and the principal's open sessions in the team are closed as
    /// they are when it leaves.
    #[serde(rename = "member.revoked")]
    MemberRevoked {
        /// The team.
        team_ref: String,
        /// The principal.
        principal_ref: String,
        /// Whether the principal's later join is refused.
        banned: bool,
    },
    /// The team's owner, or the operator that takes the team back, ended the membership of every active
    /// member that joined with a secret of `max_version` or a lower one, other than `except`, and their
    /// open sessions in the team are closed.
    /// It is one record however many members it covers, because an entry has a longest size: who they are
    /// is what the state says when the record is applied. Nobody is banned by it, and a banned member is
    /// not active, so it stays banned.
    #[serde(rename = "members.revoked_by_version")]
    MembersRevokedByVersion {
        /// The team.
        team_ref: String,
        /// The highest secret version whose members are revoked.
        max_version: u32,
        /// The principal that is never revoked by it: the owner that asked. Empty when the operator took
        /// the team back, which spares nobody.
        except: String,
    },
    /// A session was registered.
    #[serde(rename = "session.opened")]
    SessionOpened {
        /// The team.
        team_ref: String,
        /// The new session's id.
        session_id: String,
        /// The principal that owns it.
        principal_ref: String,
        /// What the registration reported.
        facts: SessionFacts,
        /// The harness's default label, which fills a membership that has none (BAP/1 4.4.2).
        #[serde(default, skip_serializing_if = "Option::is_none")]
        human_label: Option<String>,
    },
    /// A closed or expired session was re-opened in place.
    #[serde(rename = "session.resumed")]
    SessionResumed {
        /// The team.
        team_ref: String,
        /// The session.
        session_id: String,
        /// What the registration reported.
        facts: SessionFacts,
        /// The harness's default label, which fills a membership that has none.
        #[serde(default, skip_serializing_if = "Option::is_none")]
        human_label: Option<String>,
    },
    /// A session's lease was renewed. It is a record so that a standby has the leases.
    #[serde(rename = "session.heartbeat")]
    SessionHeartbeat {
        /// The team.
        team_ref: String,
        /// The session.
        session_id: String,
        /// What the heartbeat changed.
        #[serde(default)]
        changes: SessionChanges,
    },
    /// A session was closed.
    #[serde(rename = "session.closed")]
    SessionClosed {
        /// The team.
        team_ref: String,
        /// The session.
        session_id: String,
    },
    /// A message was accepted.
    #[serde(rename = "message.accepted")]
    MessageAccepted {
        /// The message.
        message: AcceptedMessage,
    },
    /// A session acknowledged messages.
    #[serde(rename = "message.acked")]
    MessageAcked {
        /// The team.
        team_ref: String,
        /// The session that acknowledged.
        session_id: String,
        /// The messages that were pending and are acknowledged now.
        message_ids: Vec<String>,
    },
}

#[cfg(test)]
mod tests {
    use super::{
        AcceptedMessage, Entry, Event, Idempotency, KeptTicket, LONGEST_ENCODED_BYTES, Record, SessionChanges,
    };
    use crate::time::Timestamp;

    #[test]
    fn a_record_names_its_kind_beside_its_members() {
        let record = Record::new(
            Timestamp(5),
            Event::MemberLeft {
                team_ref: "t".into(),
                principal_ref: "p".into(),
            },
        );
        assert_eq!(
            String::from_utf8(record.encode()).unwrap(),
            r#"{"version":1,"at":5,"kind":"member.left","team_ref":"t","principal_ref":"p"}"#
        );
    }

    #[test]
    fn every_kind_reads_back_as_it_was_written() {
        let message = AcceptedMessage {
            message_id: "m".into(),
            team_ref: "t".into(),
            sender_principal_ref: "p".into(),
            sender_human_label: String::new(),
            sender_session_id: "a".into(),
            sender_session_name: "alpha".into(),
            recipient_session_id: "b".into(),
            summary: String::new(),
            body: "line\nbreak \u{2028} and \u{0000}".into(),
            reply_to: Some("earlier".into()),
            hop_count: 3,
            idempotency: Some(Idempotency {
                key: "k".into(),
                fingerprint: "f".into(),
            }),
        };
        let events = [
            Event::CredentialIssued {
                principal_ref: "p".into(),
                credential_hash: "h".into(),
            },
            Event::CredentialRevoked {
                credential_hash: "h".into(),
            },
            Event::TeamCreated {
                team_ref: "t".into(),
                team_name: "ops".into(),
                join_secret_hash: "s".into(),
                created_by: "p".into(),
                taken_in: false,
            },
            Event::TeamCreated {
                team_ref: "t".into(),
                team_name: "ops".into(),
                join_secret_hash: "s".into(),
                created_by: "p".into(),
                taken_in: true,
            },
            Event::MemberJoined {
                team_ref: "t".into(),
                principal_ref: "p".into(),
                human_label: String::new(),
                by_move_ticket: false,
                retry_key_hash: String::new(),
            },
            Event::MemberJoined {
                team_ref: "t".into(),
                principal_ref: "p".into(),
                human_label: "alice".into(),
                by_move_ticket: true,
                retry_key_hash: String::new(),
            },
            Event::MemberJoined {
                team_ref: "t".into(),
                principal_ref: "p".into(),
                human_label: "alice".into(),
                by_move_ticket: false,
                retry_key_hash: "r".into(),
            },
            Event::TeamMoveOpened {
                team_ref: "t".into(),
                ticket_hash: "k".into(),
                ends_at: Timestamp(9),
                by_owner: false,
            },
            Event::TeamMoveOpened {
                team_ref: "t".into(),
                ticket_hash: "k".into(),
                ends_at: Timestamp(9),
                by_owner: true,
            },
            Event::TeamMoveClosed {
                team_ref: "t".into(),
                by_owner: false,
            },
            Event::TeamMoveClosed {
                team_ref: "t".into(),
                by_owner: true,
            },
            Event::TeamMoveRecorded {
                team_ref: "t".into(),
                adapter: "rest".into(),
                url: "https://brigade.example.com".into(),
                certificate: String::new(),
                ticket: KeptTicket::new("brg1.t.ticket"),
            },
            Event::TeamMoveRecorded {
                team_ref: "t".into(),
                adapter: "rest".into(),
                url: "https://brigade.example.com".into(),
                certificate: "sha256:00".into(),
                ticket: KeptTicket::new("brg1.t.ticket"),
            },
            Event::TeamMoveCleared { team_ref: "t".into() },
            Event::TeamRemoved { team_ref: "t".into() },
            Event::TeamSecretRotated {
                team_ref: "t".into(),
                join_secret_hash: "s2".into(),
                secret_version: 2,
            },
            Event::TeamOwnerChanged {
                team_ref: "t".into(),
                principal_ref: "q".into(),
            },
            Event::MemberRevoked {
                team_ref: "t".into(),
                principal_ref: "p".into(),
                banned: true,
            },
            Event::MembersRevokedByVersion {
                team_ref: "t".into(),
                max_version: 1,
                except: "q".into(),
            },
            Event::SessionHeartbeat {
                team_ref: "t".into(),
                session_id: "a".into(),
                changes: SessionChanges {
                    session_description: Some(String::new()),
                    ..SessionChanges::default()
                },
            },
            Event::SessionClosed {
                team_ref: "t".into(),
                session_id: "a".into(),
            },
            Event::MessageAccepted { message },
            Event::MessageAcked {
                team_ref: "t".into(),
                session_id: "b".into(),
                message_ids: vec!["m".into()],
            },
        ];
        for event in events {
            let record = Record::new(Timestamp(1_700_000_000_000_000_000), event);
            assert_eq!(Record::decode(&record.encode()).unwrap(), record);
        }
    }

    #[test]
    fn a_record_of_another_version_is_refused_by_name() {
        let error = Record::decode(
            br#"{"version":2,"at":5,"kind":"member.left","team_ref":"t","principal_ref":"p"}"#,
        )
        .unwrap_err();
        assert!(error.contains("version 2"), "{error}");
        assert!(Record::decode(b"not json").is_err());
        assert!(Record::decode(br#"{"version":1,"at":5,"kind":"no.such.kind"}"#).is_err());
    }

    #[test]
    fn the_administration_records_are_named_as_the_log_keeps_them() {
        // A build that does not know these four kinds refuses the entry that holds one, as it refuses any
        // kind it does not know: the names are what it reads, so they are held here.
        let written = |event: Event| String::from_utf8(Record::new(Timestamp(5), event).encode()).unwrap();
        assert_eq!(
            written(Event::TeamSecretRotated {
                team_ref: "t".into(),
                join_secret_hash: "s2".into(),
                secret_version: 2,
            }),
            r#"{"version":1,"at":5,"kind":"team.secret_rotated","team_ref":"t","join_secret_hash":"s2","secret_version":2}"#
        );
        assert_eq!(
            written(Event::TeamOwnerChanged {
                team_ref: "t".into(),
                principal_ref: "q".into(),
            }),
            r#"{"version":1,"at":5,"kind":"team.owner_changed","team_ref":"t","principal_ref":"q"}"#
        );
        assert_eq!(
            written(Event::MemberRevoked {
                team_ref: "t".into(),
                principal_ref: "p".into(),
                banned: false,
            }),
            r#"{"version":1,"at":5,"kind":"member.revoked","team_ref":"t","principal_ref":"p","banned":false}"#
        );
        assert_eq!(
            written(Event::MembersRevokedByVersion {
                team_ref: "t".into(),
                max_version: 1,
                except: "q".into(),
            }),
            r#"{"version":1,"at":5,"kind":"members.revoked_by_version","team_ref":"t","max_version":1,"except":"q"}"#
        );
    }

    #[test]
    fn the_records_of_a_move_are_named_as_the_log_keeps_them() {
        // A build that does not know these two kinds refuses the entry that holds one, so the names are
        // held here, as the four of the administration are.
        let written = |event: Event| String::from_utf8(Record::new(Timestamp(5), event).encode()).unwrap();
        let opened = |by_owner: bool| Event::TeamMoveOpened {
            team_ref: "t".into(),
            ticket_hash: "k".into(),
            ends_at: Timestamp(9),
            by_owner,
        };
        let closed = |by_owner: bool| Event::TeamMoveClosed {
            team_ref: "t".into(),
            by_owner,
        };
        let operators =
            r#"{"version":1,"at":5,"kind":"team.move_opened","team_ref":"t","ticket_hash":"k","ends_at":9}"#;
        assert_eq!(written(opened(false)), operators);
        assert_eq!(
            written(closed(false)),
            r#"{"version":1,"at":5,"kind":"team.move_closed","team_ref":"t"}"#
        );
        // The operator's records are the records they have always been, byte for byte, and one written
        // before an owner could open a move reads as the operator's. The owner's carry one member more,
        // which a build that does not know it reads past.
        assert_eq!(Record::decode(operators.as_bytes()).unwrap().event, opened(false));
        let owners = r#"{"version":1,"at":5,"kind":"team.move_opened","team_ref":"t","ticket_hash":"k","ends_at":9,"by_owner":true}"#;
        assert_eq!(written(opened(true)), owners);
        assert_eq!(Record::decode(owners.as_bytes()).unwrap().event, opened(true));
        let owners = r#"{"version":1,"at":5,"kind":"team.move_closed","team_ref":"t","by_owner":true}"#;
        assert_eq!(written(closed(true)), owners);
        assert_eq!(Record::decode(owners.as_bytes()).unwrap().event, closed(true));
    }

    #[test]
    fn the_records_of_where_a_team_went_are_named_as_the_log_keeps_them() {
        // A build that does not know these two kinds refuses the entry that holds one, so the names and
        // the members are held here: a server before 0.8.0 does not start on a log that holds either.
        let written = |event: Event| String::from_utf8(Record::new(Timestamp(5), event).encode()).unwrap();
        let recorded = |certificate: &str| Event::TeamMoveRecorded {
            team_ref: "t".into(),
            adapter: "rest".into(),
            url: "https://brigade.example.com".into(),
            certificate: certificate.into(),
            ticket: KeptTicket::new("brg1.t.ticket"),
        };
        let without = r#"{"version":1,"at":5,"kind":"team.move_recorded","team_ref":"t","adapter":"rest","url":"https://brigade.example.com","ticket":"brg1.t.ticket"}"#;
        assert_eq!(written(recorded("")), without);
        assert_eq!(Record::decode(without.as_bytes()).unwrap().event, recorded(""));
        let with = r#"{"version":1,"at":5,"kind":"team.move_recorded","team_ref":"t","adapter":"rest","url":"https://brigade.example.com","certificate":"sha256:00","ticket":"brg1.t.ticket"}"#;
        assert_eq!(written(recorded("sha256:00")), with);
        assert_eq!(
            Record::decode(with.as_bytes()).unwrap().event,
            recorded("sha256:00")
        );
        let cleared = r#"{"version":1,"at":5,"kind":"team.move_cleared","team_ref":"t"}"#;
        assert_eq!(written(Event::TeamMoveCleared { team_ref: "t".into() }), cleared);
        assert_eq!(
            Record::decode(cleared.as_bytes()).unwrap().event,
            Event::TeamMoveCleared { team_ref: "t".into() }
        );
    }

    #[test]
    fn the_record_of_a_removed_team_is_named_as_the_log_keeps_it() {
        // A build that does not know this kind refuses the entry that holds one, so the name and the one
        // member are held here: a server before 0.9.0 does not read a log's entry that removes a team. The
        // record holds the reference and nothing else of the team: what goes is what the state holds.
        let removed = Event::TeamRemoved { team_ref: "t".into() };
        let written = r#"{"version":1,"at":5,"kind":"team.removed","team_ref":"t"}"#;
        assert_eq!(
            String::from_utf8(Record::new(Timestamp(5), removed.clone()).encode()).unwrap(),
            written
        );
        assert_eq!(Record::decode(written.as_bytes()).unwrap().event, removed);
        let entry = Entry {
            sequence: 7,
            records: vec![Record::new(Timestamp(5), removed)],
        };
        assert_eq!(
            String::from_utf8(entry.encode()).unwrap(),
            format!(r#"{{"sequence":7,"records":[{written}]}}"#)
        );
        assert_eq!(Entry::decode(&entry.encode()).unwrap(), entry);
    }

    #[test]
    fn a_move_ticket_is_written_whole_and_printed_as_nothing_of_itself() {
        // The log and a snapshot hold the ticket, because a member is handed it. Nothing that prints a
        // record for a person does: the debug rendering of the ticket, of the record and of the entry
        // holds no part of it.
        // The ticket is put together here: no file of the repository holds the shape of one.
        let ticket = concat!("brg1", ".team-reference.", "the-secret-part-of-the-ticket");
        let record = Record::new(
            Timestamp(5),
            Event::TeamMoveRecorded {
                team_ref: "team-reference".into(),
                adapter: "rest".into(),
                url: "https://brigade.example.com".into(),
                certificate: String::new(),
                ticket: KeptTicket::new(ticket),
            },
        );
        assert!(String::from_utf8(record.encode()).unwrap().contains(ticket));
        let entry = Entry {
            sequence: 1,
            records: vec![record.clone()],
        };
        for printed in [
            format!("{:?}", KeptTicket::new(ticket)),
            format!("{record:?}"),
            format!("{entry:?}"),
            format!("{record:#?}"),
        ] {
            assert!(!printed.contains("the-secret-part"), "{printed}");
            assert!(printed.contains("[redacted]"), "{printed}");
        }
        assert_eq!(KeptTicket::new(ticket).expose(), ticket);
    }

    #[test]
    fn the_longest_record_of_a_move_fits_the_longest_entry() {
        // Every character of the destination is one JSON writes as six bytes, and the ticket is at its
        // cap: the record is far inside what the log takes.
        let control = |count: usize| "\u{1}".repeat(count);
        let record = Record::new(
            Timestamp(i64::MAX),
            Event::TeamMoveRecorded {
                team_ref: "t".repeat(crate::limits::MAX_TAKEN_IN_TEAM_REF_CHARS),
                adapter: control(crate::limits::MAX_MOVE_ADAPTER_CHARS),
                url: control(crate::limits::MAX_MOVE_URL_CHARS),
                certificate: control(crate::limits::MAX_MOVE_CERTIFICATE_CHARS),
                ticket: KeptTicket::new(&"\u{10FFFF}".repeat(crate::limits::MAX_MOVE_TICKET_CHARS)),
            },
        );
        let entry = Entry {
            sequence: u64::MAX,
            records: vec![record],
        };
        assert!(entry.encode().len() < LONGEST_ENCODED_BYTES / 4);
    }

    #[test]
    fn a_team_says_that_it_was_taken_in_by_a_request_only_when_it_was() {
        let created = |created_by: &str, taken_in: bool| Event::TeamCreated {
            team_ref: "t".into(),
            team_name: "ops".into(),
            join_secret_hash: "s".into(),
            created_by: created_by.into(),
            taken_in,
        };
        let written = |event: Event| String::from_utf8(Record::new(Timestamp(5), event).encode()).unwrap();
        // A team that was created here, and one the operator took in, are the records they have always
        // been, byte for byte, and a record written before a request could take a team in reads as one.
        let created_here = r#"{"version":1,"at":5,"kind":"team.created","team_ref":"t","team_name":"ops","join_secret_hash":"s","created_by":"p"}"#;
        assert_eq!(written(created("p", false)), created_here);
        assert_eq!(
            Record::decode(created_here.as_bytes()).unwrap().event,
            created("p", false)
        );
        assert_eq!(
            written(created("", false)),
            r#"{"version":1,"at":5,"kind":"team.created","team_ref":"t","team_name":"ops","join_secret_hash":"s","created_by":""}"#
        );
        assert_eq!(
            written(created("p", true)),
            r#"{"version":1,"at":5,"kind":"team.created","team_ref":"t","team_name":"ops","join_secret_hash":"s","created_by":"p","taken_in":true}"#
        );
    }

    #[test]
    fn a_join_says_that_it_came_in_with_the_move_ticket_only_when_it_did() {
        let joined = |by_move_ticket: bool| Event::MemberJoined {
            team_ref: "t".into(),
            principal_ref: "p".into(),
            human_label: String::new(),
            by_move_ticket,
            retry_key_hash: String::new(),
        };
        let written = |event: Event| String::from_utf8(Record::new(Timestamp(5), event).encode()).unwrap();
        // A join with the join secret is the record it has always been, byte for byte, and a record
        // written before there were tickets reads as one.
        let with_the_secret =
            r#"{"version":1,"at":5,"kind":"member.joined","team_ref":"t","principal_ref":"p"}"#;
        assert_eq!(written(joined(false)), with_the_secret);
        assert_eq!(
            Record::decode(with_the_secret.as_bytes()).unwrap().event,
            joined(false)
        );
        assert_eq!(
            written(joined(true)),
            r#"{"version":1,"at":5,"kind":"member.joined","team_ref":"t","principal_ref":"p","by_move_ticket":true}"#
        );
    }

    #[test]
    fn a_join_keeps_the_hash_of_a_retry_key_only_when_a_take_in_gave_it_one() {
        let joined = |retry_key_hash: &str| Event::MemberJoined {
            team_ref: "t".into(),
            principal_ref: "p".into(),
            human_label: String::new(),
            by_move_ticket: false,
            retry_key_hash: retry_key_hash.into(),
        };
        let written = |event: Event| String::from_utf8(Record::new(Timestamp(5), event).encode()).unwrap();
        // A join with no key is the record it has always been, byte for byte, and a record written before
        // there were retry keys reads as one: a server 0.6.0 and a server 0.7.0 write and read the same.
        let without = r#"{"version":1,"at":5,"kind":"member.joined","team_ref":"t","principal_ref":"p"}"#;
        assert_eq!(written(joined("")), without);
        assert_eq!(Record::decode(without.as_bytes()).unwrap().event, joined(""));
        // The member is named as the log keeps it: a build that does not know it reads past it, and one
        // that does reads the hash back.
        let with = r#"{"version":1,"at":5,"kind":"member.joined","team_ref":"t","principal_ref":"p","retry_key_hash":"h"}"#;
        assert_eq!(written(joined("h")), with);
        assert_eq!(Record::decode(with.as_bytes()).unwrap().event, joined("h"));
    }
}
