//! A gateway's place in a team: its membership and its one session.
//!
//! A gateway becomes a member the way anyone does: with the team's join secret, which the operator hands the
//! server in a file. The server issues it a credential, which is kept in the gateways' state, and from then
//! on the gateway is known by that credential alone. Its session is registered once and kept open by a
//! heartbeat a minute from the loop, as the Edge Functions kept theirs by a tick a minute.

use brigade_core::wire::JoinSecret;
use brigade_core::{Code, Error};
use tokio::sync::Mutex;

use super::member::{self, Member, Session};
use super::state::{Kind, Membership, Store};
use crate::engine::Client;

/// The label a gateway joins a team under, as the installers gave it.
#[must_use]
pub fn label(kind: Kind) -> &'static str {
    match kind {
        Kind::Email => "mail gateway",
        Kind::Slack => "slack gateway",
    }
}

/// What went wrong with a gateway's place in a team, or with a turn of it: a refusal of the server's own
/// rules, or a failure to write the gateways' state.
#[derive(Debug)]
pub enum Failure {
    /// The engine refused a request.
    Refused(Error),
    /// The state could not be written, or the secret does not read as one.
    Broken(String),
}

impl std::fmt::Display for Failure {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            // The code and the message are the contract's own and carry nothing a request held.
            Self::Refused(error) => write!(formatter, "{}: {}", error.code.as_str(), error.message),
            Self::Broken(why) => formatter.write_str(why),
        }
    }
}

impl From<Error> for Failure {
    fn from(error: Error) -> Self {
        Self::Refused(error)
    }
}

/// The gateway's membership of the team `join_secret` names, joining it when the gateway is not a member
/// yet. A gateway that is a member already is not joined again: its credential is the one it has.
///
/// The store is locked to read it and to write it, and not while the engine is asked.
///
/// # Errors
/// The join was refused, or the state could not be written.
pub async fn member_of(
    engine: &Client,
    store: &Mutex<Store>,
    kind: Kind,
    join_secret: &str,
) -> Result<Membership, Failure> {
    let team_ref = JoinSecret::parse(join_secret)
        .map_err(|_| Failure::Broken("a join secret file does not hold a join secret".to_owned()))?
        .team_ref()
        .to_owned();
    let known = store.lock().await.membership(&team_ref, kind).cloned();
    match known {
        Some(membership) => Ok(membership),
        None => join(engine, store, kind, join_secret).await,
    }
}

/// Joins the team anew, as a new principal, and records the membership in place of any earlier one. This is
/// what follows a credential the server no longer knows.
///
/// # Errors
/// The join was refused, or the state could not be written.
pub async fn join(
    engine: &Client,
    store: &Mutex<Store>,
    kind: Kind,
    join_secret: &str,
) -> Result<Membership, Failure> {
    let joined = member::join(engine, None, join_secret, label(kind)).await?;
    let membership = Membership {
        team_ref: joined.team_ref,
        kind,
        team_name: joined.team_name,
        credential: joined.credential,
        principal_ref: joined.principal_ref,
        session_id: String::new(),
    };
    store
        .lock()
        .await
        .set_membership(membership.clone())
        .map_err(Failure::Broken)?;
    Ok(membership)
}

/// Whether a refusal means the gateway is no longer the member it was: its credential is unknown, or its
/// membership has ended. Joining again with the secret is what answers both.
#[must_use]
pub fn lost(error: &Error) -> bool {
    matches!(error.code, Code::Unauthenticated | Code::Unauthorized)
}

/// Keeps the gateway's session open and answers its id, registering one when there is none or the server
/// no longer has it. The id is recorded when it changes.
///
/// # Errors
/// The heartbeat or the registration was refused, or the state could not be written.
pub async fn session(
    engine: &Client,
    store: &Mutex<Store>,
    membership: &Membership,
    session: &Session<'_>,
) -> Result<String, Failure> {
    let member = Member::new(engine.clone(), &membership.credential);
    let current = store
        .lock()
        .await
        .membership(&membership.team_ref, membership.kind)
        .map_or("", |stored| stored.session_id.as_str())
        .to_owned();
    let session_id = member.keep(&membership.team_ref, &current, session).await?;
    store
        .lock()
        .await
        .set_session(&membership.team_ref, membership.kind, &session_id)
        .map_err(Failure::Broken)?;
    Ok(session_id)
}

#[cfg(test)]
mod tests {
    use hyper::body::Bytes;
    use serde_json::{Value, json};

    use super::*;
    use crate::gateway::member::Outgoing;
    use crate::gateway::testing::{engine_on, scratch};

    const SLACK: Session<'static> = Session {
        name: "slack-gateway",
        description: "slack gateway for example.slack.com",
        harness: "gateway-slack",
        harness_version: "0.1.0",
    };

    #[tokio::test]
    async fn a_gateway_joins_with_the_secret_registers_once_and_is_one_more_member() {
        let data = scratch("presence-member");
        let engine = engine_on(&data);
        let store = Mutex::new(Store::open(&data).unwrap());

        // An admin creates the team and has a session of their own.
        let created = engine
            .ask(
                None,
                true,
                "/v1/team/create",
                Bytes::from(json!({"team_name": "ops", "human_label": "alice@example.com"}).to_string()),
            )
            .await
            .unwrap();
        let (team_ref, secret) = (
            created["team_ref"].as_str().unwrap().to_owned(),
            created["join_secret"].as_str().unwrap().to_owned(),
        );
        let alice = Member::new(engine.clone(), created["credential"].as_str().unwrap());
        let coding = Session {
            name: "payments-api",
            description: "",
            harness: "claude-code",
            harness_version: "2.1.0",
        };
        let alice_session = alice.register(&team_ref, &coding, None).await.unwrap();

        // The gateway joins with the secret and registers its session.
        let membership = member_of(&engine, &store, Kind::Slack, &secret).await.unwrap();
        assert_eq!(membership.team_ref, team_ref);
        assert_eq!(membership.team_name, "ops");
        assert!(!membership.credential.is_empty());
        let session_id = session(&engine, &store, &membership, &SLACK).await.unwrap();
        assert!(!session_id.is_empty());

        // It is on the roster as the team sees it, recognisable by its harness, under the label it joined by.
        let roster = alice.sessions(&team_ref, false).await.unwrap();
        let gateway: &Value = roster
            .iter()
            .find(|row| row["session_id"] == json!(session_id))
            .expect("the gateway's session on the roster");
        assert_eq!(gateway["harness"], json!("gateway-slack"));
        assert_eq!(gateway["session_name"], json!("slack-gateway"));
        assert_eq!(gateway["human_label"], json!("slack gateway"));
        assert_eq!(gateway["inbound"], json!("accept"));

        // A second turn joins nothing and registers nothing: the same member, the same session.
        let again = member_of(&engine, &store, Kind::Slack, &secret).await.unwrap();
        assert_eq!(again.credential, membership.credential);
        assert_eq!(
            session(&engine, &store, &again, &SLACK).await.unwrap(),
            session_id
        );
        assert_eq!(alice.sessions(&team_ref, true).await.unwrap().len(), 2);

        // A session's message reaches it like any member's, and is gone once acknowledged.
        let sent = alice
            .send(&Outgoing {
                sender_session_id: &alice_session,
                recipient_session_id: &session_id,
                body: "to: #general\n\nthe migration has landed",
                summary: "migration",
                reply_to: None,
                idempotency_key: "k1",
            })
            .await
            .unwrap();
        let gateway = Member::new(engine.clone(), &membership.credential);
        let inbox = gateway.receive(&session_id, 50).await.unwrap();
        assert_eq!(inbox.len(), 1);
        assert_eq!(inbox[0]["message_id"], sent["message_id"]);
        assert_eq!(inbox[0]["sender"]["session_id"], json!(alice_session));
        gateway
            .acknowledge(&session_id, &[sent["message_id"].as_str().unwrap()])
            .await
            .unwrap();
        assert!(gateway.receive(&session_id, 50).await.unwrap().is_empty());

        // The other kind is another member of the same team, with a session of its own.
        let mail = member_of(&engine, &store, Kind::Email, &secret).await.unwrap();
        assert_ne!(mail.principal_ref, membership.principal_ref);

        // What it was is there after a restart of the store.
        let reopened = Store::open(&data).unwrap();
        assert_eq!(
            reopened.membership(&team_ref, Kind::Slack).unwrap().session_id,
            session_id
        );
        let _ = std::fs::remove_dir_all(&data);
    }

    #[tokio::test]
    async fn a_secret_that_is_not_the_teams_is_refused_and_nothing_is_kept() {
        let data = scratch("presence-refused");
        let engine = engine_on(&data);
        let store = Mutex::new(Store::open(&data).unwrap());
        let created = engine
            .ask(
                None,
                true,
                "/v1/team/create",
                Bytes::from(json!({"team_name": "ops"}).to_string()),
            )
            .await
            .unwrap();
        let team_ref = created["team_ref"].as_str().unwrap();
        let wrong = format!("brg1.{team_ref}.{}", "0".repeat(32));
        let refused = member_of(&engine, &store, Kind::Slack, &wrong)
            .await
            .err()
            .unwrap();
        assert!(
            matches!(&refused, Failure::Refused(error) if lost(error)),
            "{refused}"
        );
        assert!(store.lock().await.membership(team_ref, Kind::Slack).is_none());
        assert!(matches!(
            member_of(&engine, &store, Kind::Slack, "not a secret").await,
            Err(Failure::Broken(_))
        ));
        let _ = std::fs::remove_dir_all(&data);
    }
}
