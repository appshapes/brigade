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
        /// The principal that created it.
        created_by: String,
    },
    /// A principal became an active member of a team: a new membership, or one that was revoked and is
    /// active again with the day it first joined.
    #[serde(rename = "member.joined")]
    MemberJoined {
        /// The team.
        team_ref: String,
        /// The principal.
        principal_ref: String,
        /// The label the request carried. Empty keeps the label a returning member had.
        #[serde(default, skip_serializing_if = "String::is_empty")]
        human_label: String,
    },
    /// A principal left a team: its membership is revoked and its open sessions there are closed.
    #[serde(rename = "member.left")]
    MemberLeft {
        /// The team.
        team_ref: String,
        /// The principal.
        principal_ref: String,
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
    use super::{AcceptedMessage, Event, Idempotency, Record, SessionChanges};
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
            },
            Event::MemberJoined {
                team_ref: "t".into(),
                principal_ref: "p".into(),
                human_label: String::new(),
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
}
