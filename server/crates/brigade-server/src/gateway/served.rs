//! One team's gateway, as a handler acts for it.
//!
//! This is the row the Edge Functions read from their `gateways` table: the team, the gateway's session in
//! it and, for mail, the team's addresses. Everything it does in the team goes through [`Member`], so it does
//! what a member may and nothing else, and what the engine answers is read here into the shapes the rules of
//! `brigade-gateway` take.

use brigade_core::{Code, Error};
use brigade_gateway::notes::INBOX_BATCH;
use brigade_gateway::{Envelope, RenderContext, Sender, Sending, SessionRecord};
use serde_json::Value;

use super::Team;
use super::member::{Member, Outgoing};
use super::state::Membership;
use crate::engine::Client;

/// What the engine answered a send with.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Sent {
    /// The message's id.
    pub message_id: String,
    /// Whether the message had been sent before under the same idempotency key.
    pub duplicate: bool,
}

/// One team's gateway.
#[derive(Clone)]
pub struct Served {
    /// The team.
    pub team_ref: String,
    /// The team's name, as the join answered it.
    pub team_name: String,
    /// The gateway's session in the team.
    pub session_id: String,
    /// For mail, the address the provider receives the team's mail at; empty for Slack, like the three
    /// members below.
    pub inbox: String,
    /// The address people are told to write to.
    pub public_address: String,
    /// The From of every mail the gateway sends for the team.
    pub from: String,
    /// Who may write in by mail; empty admits anyone.
    pub allowed_senders: Vec<String>,
    member: Member,
}

impl Served {
    /// The gateway of `team`, as the member `membership` records, acting from `session_id`.
    #[must_use]
    pub fn new(engine: &Client, membership: &Membership, session_id: &str, team: &Team) -> Self {
        Self {
            team_ref: membership.team_ref.clone(),
            team_name: membership.team_name.clone(),
            session_id: session_id.to_owned(),
            inbox: team.inbox.clone(),
            public_address: team.public_address.clone(),
            from: team.from.clone(),
            allowed_senders: team.allowed_senders.clone(),
            member: Member::new(engine.clone(), &membership.credential),
        }
    }

    /// What a mail of the team's is rendered with.
    #[must_use]
    pub fn render_context(&self) -> RenderContext {
        RenderContext {
            team_name: self.team_name.clone(),
            from: self.from.clone(),
            inbox: self.inbox.clone(),
            public_address: self.public_address.clone(),
        }
    }

    /// The team's roster, the offline sessions too, the gateway's own among them.
    ///
    /// # Errors
    /// What the listing was refused with.
    pub async fn sessions(&self) -> Result<Vec<SessionRecord>, Error> {
        let rows = self.member.sessions(&self.team_ref, true).await?;
        Ok(rows.iter().map(session_record).collect())
    }

    /// The messages waiting for the gateway's session, oldest first: one turn's batch at most.
    ///
    /// # Errors
    /// What the receive was refused with.
    pub async fn inbox(&self) -> Result<Vec<Envelope>, Error> {
        let rows = self.member.receive(&self.session_id, INBOX_BATCH).await?;
        Ok(rows.iter().map(envelope).collect())
    }

    /// Sends a message from the gateway's session. What it answers is asked of the engine as the rules
    /// wrote it, an empty `reply_to` included.
    ///
    /// # Errors
    /// What the send was refused with.
    pub async fn send(&self, message: &Sending) -> Result<Sent, Error> {
        let sent = self
            .member
            .send(&Outgoing {
                sender_session_id: &self.session_id,
                recipient_session_id: &message.recipient_session_id,
                body: &message.body,
                summary: &message.summary,
                reply_to: message.reply_to.as_deref(),
                idempotency_key: &message.idempotency_key,
            })
            .await?;
        Ok(Sent {
            message_id: text(&sent, "message_id"),
            duplicate: sent["duplicate"].as_bool().unwrap_or(false),
        })
    }

    /// Acknowledges one message the gateway's session received.
    ///
    /// # Errors
    /// What the acknowledgement was refused with.
    pub async fn acknowledge(&self, message_id: &str) -> Result<(), Error> {
        self.member.acknowledge(&self.session_id, &[message_id]).await
    }
}

/// What a refusal adds to its code: the reason the engine names, empty when it names none. It is the
/// `detail` of the Edge Functions' `brigade:<code>:<detail>`.
#[must_use]
pub fn detail(error: &Error) -> &str {
    error.details.get("reason").map_or("", String::as_str)
}

/// Whether a send was refused because the engine already holds a message under its idempotency key, sent
/// with other words. A key is one event's or one mail's, so that message is this one, carried by an earlier
/// delivery of it: only what Slack or the provider said of it in between can have changed.
#[must_use]
pub fn carried_before(refusal: &Error) -> bool {
    refusal.code == Code::Conflict && detail(refusal) == "idempotency_key_reused"
}

/// A text member, empty when it is not there.
fn text(value: &Value, name: &str) -> String {
    value[name].as_str().unwrap_or_default().to_owned()
}

/// A text member that may be missing.
fn optional(value: &Value, name: &str) -> Option<String> {
    value[name].as_str().map(str::to_owned)
}

/// A row of the engine's roster, as the rules read one.
fn session_record(row: &Value) -> SessionRecord {
    SessionRecord {
        session_id: text(row, "session_id"),
        session_name: text(row, "session_name"),
        session_description: optional(row, "session_description"),
        principal_ref: text(row, "principal_ref"),
        human_label: optional(row, "human_label"),
        state: text(row, "state"),
        inbound: optional(row, "inbound"),
        harness: optional(row, "harness"),
        last_seen_at: optional(row, "last_seen_at"),
    }
}

/// A message of the engine's, as the rules read one.
fn envelope(message: &Value) -> Envelope {
    let sender = &message["sender"];
    Envelope {
        message_id: text(message, "message_id"),
        team_ref: text(message, "team_ref"),
        sender: Sender {
            principal_ref: text(sender, "principal_ref"),
            human_label: optional(sender, "human_label"),
            session_id: text(sender, "session_id"),
            session_name: text(sender, "session_name"),
        },
        recipient_session_id: text(message, "recipient_session_id"),
        summary: optional(message, "summary"),
        body: text(message, "body"),
        reply_to: optional(message, "reply_to"),
        hop_count: message["hop_count"]
            .as_u64()
            .and_then(|hops| u32::try_from(hops).ok())
            .unwrap_or(0),
        created_at: text(message, "created_at"),
    }
}

#[cfg(test)]
mod tests {
    use brigade_gateway::Sending;

    use super::*;
    use crate::gateway::member::Session;
    use crate::gateway::state::Kind;
    use crate::gateway::testing::{engine_on, scratch, team};

    #[tokio::test]
    async fn what_the_engine_answers_is_read_as_the_rules_take_it() {
        let data = scratch("served");
        let engine = engine_on(&data);
        let ops = team(&engine).await;
        // A second member is the gateway: joined by hand, with a session of its own.
        let joined = crate::gateway::member::join(&engine, None, &ops.join_secret, "slack gateway")
            .await
            .unwrap();
        let membership = Membership {
            team_ref: joined.team_ref,
            kind: Kind::Slack,
            team_name: joined.team_name,
            credential: joined.credential,
            principal_ref: joined.principal_ref,
            session_id: String::new(),
        };
        let session = Session {
            name: "slack-gateway",
            description: "slack gateway for example.slack.com",
            harness: "gateway-slack",
            harness_version: "0.1.0",
        };
        let session_id = Member::new(engine.clone(), &membership.credential)
            .register(&ops.team_ref, &session, None)
            .await
            .unwrap();
        let served = Served::new(&engine, &membership, &session_id, &ops.slack_team());
        assert_eq!(served.team_name, "ops");

        // The roster: every member of a row the rules read, the label left out where a member has none.
        let roster = served.sessions().await.unwrap();
        let alice = roster.iter().find(|row| row.session_id == ops.session).unwrap();
        assert_eq!(alice.session_name, "payments-api");
        assert_eq!(alice.human_label.as_deref(), Some("alice@example.com"));
        assert_eq!(alice.principal_ref, ops.principal_ref);
        assert_eq!(
            (
                alice.state.as_str(),
                alice.inbound.as_deref(),
                alice.harness.as_deref()
            ),
            ("idle", Some("accept"), Some("claude-code"))
        );
        assert!(alice.last_seen_at.as_deref().is_some_and(|at| at.ends_with('Z')));
        let own = roster.iter().find(|row| row.session_id == session_id).unwrap();
        assert_eq!(own.harness.as_deref(), Some("gateway-slack"));
        assert_eq!(
            own.session_description.as_deref(),
            Some("slack gateway for example.slack.com")
        );

        // A send, and the same send again.
        let note = Sending {
            recipient_session_id: ops.session.clone(),
            body: "Slack message from U0PERSON01 in a direct message:\n\nhello".to_owned(),
            summary: "hello".to_owned(),
            reply_to: None,
            idempotency_key: "slack:Ev1".to_owned(),
        };
        let first = served.send(&note).await.unwrap();
        assert!(!first.duplicate);
        let again = served.send(&note).await.unwrap();
        assert_eq!(
            (again.message_id.as_str(), again.duplicate),
            (first.message_id.as_str(), true)
        );

        // An answer to it, as the gateway's session receives it.
        let answer = ops
            .send(&session_id, "on it", "re: hello", Some(&first.message_id))
            .await;
        let inbox = served.inbox().await.unwrap();
        assert_eq!(inbox.len(), 1);
        let got = &inbox[0];
        assert_eq!(got.message_id, answer);
        assert_eq!(got.team_ref, ops.team_ref);
        assert_eq!(got.recipient_session_id, session_id);
        assert_eq!(got.sender.session_id, ops.session);
        assert_eq!(got.sender.session_name, "payments-api");
        assert_eq!(got.sender.human_label.as_deref(), Some("alice@example.com"));
        assert_eq!(got.summary.as_deref(), Some("re: hello"));
        assert_eq!(got.body, "on it");
        assert_eq!(got.reply_to.as_deref(), Some(first.message_id.as_str()));
        assert_eq!(got.hop_count, 1);
        assert!(got.created_at.ends_with('Z'));
        served.acknowledge(&got.message_id).await.unwrap();
        assert!(served.inbox().await.unwrap().is_empty());

        // A refusal keeps its code, and its reason is the detail.
        let mut too_long = note.clone();
        too_long.summary = "x".repeat(201);
        too_long.idempotency_key = "slack:Ev2".to_owned();
        let refused = served.send(&too_long).await.unwrap_err();
        assert_eq!(
            (refused.code.as_str(), detail(&refused)),
            ("invalid_input", "too_long")
        );
        let mut gone = note.clone();
        gone.reply_to = Some("no-such-message".to_owned());
        gone.idempotency_key = "slack:Ev3".to_owned();
        let refused = served.send(&gone).await.unwrap_err();
        assert_eq!((refused.code.as_str(), detail(&refused)), ("not_found", ""));
        assert!(gone.sends_again_unthreaded(refused.code.as_str()));
        let _ = std::fs::remove_dir_all(&data);
    }
}
