//! A gateway acting as the member it is.
//!
//! Everything a gateway does in a team goes through the engine's request path, the one every HTTP request
//! takes, with the credential the server issued the gateway when it joined. So a gateway can do nothing a
//! member cannot, and the limits, the leases and the rules for a `reply_to` apply to it unchanged. The
//! requests are the contract's own (`docs/backend-rest-v1.md`, section 2), built here and never sent over a
//! socket.

use brigade_core::hash::sha256_hex;
use brigade_core::{Code, Error};
use hyper::body::Bytes;
use serde_json::{Value, json};

use crate::engine::Client;

/// What a gateway's session says of itself when it registers and at every heartbeat.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Session<'a> {
    /// `mail-gateway` or `slack-gateway`.
    pub name: &'a str,
    /// The roster's line under it: how a person and a session write to it.
    pub description: &'a str,
    /// `gateway-email` or `gateway-slack`: what members and other gateways recognise it by.
    pub harness: &'a str,
    /// The gateway's own version.
    pub harness_version: &'a str,
}

/// The lease a gateway's session asks for, as the Edge Functions do: ten minutes, kept by the loop.
const LEASE_SECONDS: i64 = 600;

/// What a join answered. It holds the credential the server issued, so it has no `Debug`.
#[derive(Clone, PartialEq, Eq)]
pub struct Joined {
    /// The team.
    pub team_ref: String,
    /// Its name.
    pub team_name: String,
    /// The gateway's principal in it.
    pub principal_ref: String,
    /// The credential the server issued; empty when the join was made with one the server knew.
    pub credential: String,
}

/// Reads a text member of a result, empty when it is not there.
fn text(result: &Value, name: &str) -> String {
    result[name].as_str().unwrap_or_default().to_owned()
}

/// Joins a team with its secret, as a new principal when `credential` is none.
///
/// # Errors
/// What the join was refused with: a secret that is not the team's is `unauthorized`.
pub async fn join(
    engine: &Client,
    credential: Option<&str>,
    join_secret: &str,
    human_label: &str,
) -> Result<Joined, Error> {
    let body = json!({"join_secret": join_secret, "human_label": human_label});
    let caller = credential.map(sha256_hex);
    let result = engine
        .ask(caller, false, "/v1/team/join", Bytes::from(body.to_string()))
        .await?;
    Ok(Joined {
        team_ref: text(&result, "team_ref"),
        team_name: text(&result, "team_name"),
        principal_ref: text(&result, "principal_ref"),
        credential: text(&result, "credential"),
    })
}

/// A gateway's way to the engine, as one member.
#[derive(Clone)]
pub struct Member {
    engine: Client,
    /// The hash of the gateway's credential: what the engine knows a caller by.
    caller: String,
}

impl Member {
    /// The member a credential names.
    #[must_use]
    pub fn new(engine: Client, credential: &str) -> Self {
        Self {
            engine,
            caller: sha256_hex(credential),
        }
    }

    async fn ask(&self, path: &str, body: &Value) -> Result<Value, Error> {
        self.engine
            .ask(
                Some(self.caller.clone()),
                false,
                path,
                Bytes::from(body.to_string()),
            )
            .await
    }

    /// Registers the gateway's session, resuming `resume` when it names one, and answers its id.
    ///
    /// # Errors
    /// What the registration was refused with.
    pub async fn register(
        &self,
        team_ref: &str,
        session: &Session<'_>,
        resume: Option<&str>,
    ) -> Result<String, Error> {
        let mut body = json!({
            "team_ref": team_ref,
            "session_name": session.name,
            "session_description": session.description,
            "activity": "idle",
            "inbound": "accept",
            "harness": session.harness,
            "harness_version": session.harness_version,
            "lease_seconds": LEASE_SECONDS,
        });
        if let Some(session_id) = resume {
            body["resume"] = json!({"session_id": session_id});
        }
        let result = self.ask("/v1/session/register", &body).await?;
        Ok(text(&result, "session_id"))
    }

    /// Keeps the gateway's session open and its description current. A session the server no longer has,
    /// or one whose lease ran out, is registered again, resuming the old id where the server still holds
    /// it, and the id answered is the one to use from then on.
    ///
    /// # Errors
    /// What the heartbeat, or the registration that followed it, was refused with.
    pub async fn keep(
        &self,
        team_ref: &str,
        session_id: &str,
        session: &Session<'_>,
    ) -> Result<String, Error> {
        if session_id.is_empty() {
            return self.register(team_ref, session, None).await;
        }
        let heartbeat = json!({
            "session_id": session_id,
            "heartbeat": {
                "activity": "idle",
                "session_description": session.description,
                "inbound": "accept",
                "lease_seconds": LEASE_SECONDS,
            },
        });
        match self.ask("/v1/session/heartbeat", &heartbeat).await {
            Ok(_) => Ok(session_id.to_owned()),
            Err(error) if error.code == Code::NotFound => self.register(team_ref, session, None).await,
            Err(error) if error.code == Code::Conflict => {
                self.register(team_ref, session, Some(session_id)).await
            }
            Err(error) => Err(error),
        }
    }

    /// The team's sessions, the offline ones too when asked.
    ///
    /// # Errors
    /// What the listing was refused with.
    pub async fn sessions(&self, team_ref: &str, include_offline: bool) -> Result<Vec<Value>, Error> {
        let body = json!({"team_ref": team_ref, "include_offline": include_offline});
        let mut result = self.ask("/v1/session/list", &body).await?;
        Ok(result["sessions"].take().as_array().cloned().unwrap_or_default())
    }

    /// The messages waiting for the gateway's session, oldest first, without waiting for one.
    ///
    /// # Errors
    /// What the receive was refused with.
    pub async fn receive(&self, session_id: &str, limit: usize) -> Result<Vec<Value>, Error> {
        let body = json!({"session_id": session_id, "limit": limit, "wait_seconds": 0});
        let mut result = self.ask("/v1/message/receive", &body).await?;
        Ok(result["messages"].take().as_array().cloned().unwrap_or_default())
    }

    /// Sends a message from the gateway's session and answers the send's result.
    ///
    /// # Errors
    /// What the send was refused with: a recipient that is gone, a limit, a `reply_to` the session did not
    /// receive.
    pub async fn send(&self, message: &Outgoing<'_>) -> Result<Value, Error> {
        let mut body = json!({
            "sender_session_id": message.sender_session_id,
            "recipient_session_id": message.recipient_session_id,
            "body": message.body,
            "idempotency_key": message.idempotency_key,
        });
        if !message.summary.is_empty() {
            body["summary"] = json!(message.summary);
        }
        if let Some(reply_to) = message.reply_to {
            body["reply_to"] = json!(reply_to);
        }
        self.ask("/v1/message/send", &body).await
    }

    /// Acknowledges messages the gateway's session received.
    ///
    /// # Errors
    /// What the acknowledgement was refused with.
    pub async fn acknowledge(&self, session_id: &str, message_ids: &[&str]) -> Result<(), Error> {
        let body = json!({"session_id": session_id, "message_ids": message_ids});
        self.ask("/v1/message/ack", &body).await.map(|_| ())
    }
}

/// A message a gateway sends into a team.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Outgoing<'a> {
    /// The gateway's session.
    pub sender_session_id: &'a str,
    /// The session it is for.
    pub recipient_session_id: &'a str,
    /// The text.
    pub body: &'a str,
    /// The one line that goes with it; empty for none.
    pub summary: &'a str,
    /// The message it answers, when it answers one the gateway's session received.
    pub reply_to: Option<&'a str>,
    /// What makes a second send of the same thing the first one: the provider's id of what was carried.
    pub idempotency_key: &'a str,
}
