//! The state of a server: what its records add up to.
//!
//! The state is held in memory and written whole in a snapshot. [`State::apply`] is the only thing that
//! changes it. It cannot fail, reads no clock and draws nothing, so a restart and a standby that apply the
//! same records in the same order hold the same state.
//!
//! Retention (BAP/1 4.5.9) and the send budgets (4.5.12) are functions of record times, so the sweep is
//! part of applying a record and needs no record of its own.

use std::collections::{BTreeMap, VecDeque};

use serde::{Deserialize, Serialize};
use serde_json::{Map, Value, json};

use crate::error::PROTOCOL_VERSION;
use crate::limits;
use crate::record::{AcceptedMessage, Entry, Event, Record, SessionChanges, SessionFacts};
use crate::time::Timestamp;

/// How often applying records sweeps what retention has let go, in seconds of record time.
const SWEEP_INTERVAL_SECONDS: i64 = 60;

/// The longest window a send budget counts over, in seconds.
const LONGEST_RATE_WINDOW_SECONDS: i64 = 3600;

/// Everything a server knows.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct State {
    /// Every principal the server has created, by its reference.
    pub principals: BTreeMap<String, Principal>,
    /// The principal each known credential names, by the credential's SHA-256.
    pub credentials: BTreeMap<String, String>,
    /// Every team, by its reference.
    pub teams: BTreeMap<String, Team>,
    /// The team each session is in, by the session's id.
    pub session_teams: BTreeMap<String, String>,
    /// The record time of the last sweep.
    pub swept_at: Timestamp,
    /// The latest time any record carried. The next record is never stamped earlier, whatever the machine's
    /// clock did while the server was down.
    pub latest_at: Timestamp,
    /// The number of the last entry of the log that was applied. Entries are numbered from one with no gap,
    /// so a log that skipped or repeated one is refused instead of believed.
    pub entries: u64,
}

/// The server's record of one member's installation.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Principal {
    /// When its first credential was issued.
    pub created_at: Timestamp,
}

/// One team.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Team {
    /// The team's reference.
    pub team_ref: String,
    /// The team's display name.
    pub team_name: String,
    /// The SHA-256 of the whole join secret. The secret itself is never kept.
    pub join_secret_hash: String,
    /// The principal that created the team.
    pub created_by: String,
    /// When the team was created.
    pub created_at: Timestamp,
    /// Every membership the team has had, by principal.
    pub members: BTreeMap<String, Member>,
    /// Every session the team still holds, by id.
    pub sessions: BTreeMap<String, Session>,
}

/// One principal's membership of one team.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Member {
    /// The member's label: unverified text. Empty when it has none.
    pub human_label: String,
    /// Whether the membership is active. One that was left is kept, revoked, so a rejoin is the same member.
    pub active: bool,
    /// When the principal first joined.
    pub joined_at: Timestamp,
    /// When each of the principal's sessions sent, within the longest budget window.
    pub sent_at: VecDeque<Timestamp>,
}

/// One session.
#[derive(Clone, Debug, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Session {
    /// The session's id.
    pub session_id: String,
    /// The principal that owns it.
    pub principal_ref: String,
    /// What its harness last reported.
    pub facts: SessionFacts,
    /// When its lease runs out.
    pub lease_until: Timestamp,
    /// When it last registered, renewed its lease or sent.
    pub last_seen_at: Timestamp,
    /// When it was first registered.
    pub created_at: Timestamp,
    /// When it was closed, if it is.
    pub closed_at: Option<Timestamp>,
    /// The number of the last message accepted for it. A cursor is this number (the contract's section 4).
    pub last_sequence: u64,
    /// Its unacknowledged messages, by the order they were accepted in.
    pub pending: BTreeMap<u64, Message>,
    /// Its acknowledged messages, until retention lets them go.
    pub acked: BTreeMap<u64, Message>,
    /// The number of each message it still holds, by message id.
    pub sequences: BTreeMap<String, u64>,
    /// What each idempotency key it used was first used for.
    pub idempotency: BTreeMap<String, IdempotencyEntry>,
    /// When it sent, within the longest budget window.
    pub sent_at: VecDeque<Timestamp>,
}

/// One message a session received.
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct Message {
    /// The message as it was accepted.
    pub accepted: AcceptedMessage,
    /// When it was accepted.
    pub created_at: Timestamp,
    /// When its recipient acknowledged it, if it has.
    pub acked_at: Option<Timestamp>,
}

/// What an idempotency key was first used for, with the whole answer of that send: a repeated send is
/// answered with the original response, and the message it names may be gone by then (BAP/1 4.5.4).
#[derive(Clone, Debug, PartialEq, Eq, Serialize, Deserialize)]
pub struct IdempotencyEntry {
    /// The hash of the recipient, the body, the summary and the message answered.
    pub fingerprint: String,
    /// The message the first send made.
    pub message_id: String,
    /// Its recipient.
    pub recipient_session_id: String,
    /// Its hop count.
    pub hop_count: u32,
    /// When it was accepted.
    pub created_at: Timestamp,
}

impl Member {
    fn new(human_label: String, joined_at: Timestamp) -> Self {
        Self {
            human_label,
            active: true,
            joined_at,
            sent_at: VecDeque::new(),
        }
    }
}

impl Session {
    /// The state of BAP/1 4.5.8, computed when it is read: `offline` when the session is closed or its
    /// lease has run out, `active` when the lease is valid and the harness last reported `busy`, `idle`
    /// otherwise. Nothing stores it, so it cannot go stale.
    #[must_use]
    pub fn state(&self, now: Timestamp) -> &'static str {
        if self.offline(now) {
            "offline"
        } else if self.facts.activity == "busy" {
            "active"
        } else {
            "idle"
        }
    }

    /// Whether the session is closed or its lease has run out.
    #[must_use]
    pub fn offline(&self, now: Timestamp) -> bool {
        self.closed_at.is_some() || now > self.lease_until
    }

    /// Whether retention still holds the session at `now` (BAP/1 4.5.9): it has not been offline for
    /// longer than a closed session is kept. The sweep lets go of the rest when a record next runs it; a
    /// request is judged as if it already had.
    #[must_use]
    pub fn retained(&self, now: Timestamp) -> bool {
        let since = self.closed_at.unwrap_or(self.lease_until);
        !(self.offline(now) && since < now.minus_seconds(limits::RETENTION_CLOSED_SESSION_SECONDS))
    }

    /// A message the session received and still holds, pending or acknowledged.
    #[must_use]
    pub fn received(&self, message_id: &str) -> Option<&Message> {
        let sequence = self.sequences.get(message_id)?;
        self.pending.get(sequence).or_else(|| self.acked.get(sequence))
    }

    fn renew(&mut self, at: Timestamp) {
        self.last_seen_at = at;
        self.lease_until = at.plus_seconds(self.facts.lease_seconds);
    }

    fn change(&mut self, changes: &SessionChanges) {
        let facts = &mut self.facts;
        if let Some(activity) = &changes.activity {
            facts.activity.clone_from(activity);
        }
        if let Some(name) = &changes.session_name {
            facts.session_name.clone_from(name);
        }
        if let Some(description) = &changes.session_description {
            facts.session_description = Some(description.clone());
        }
        if let Some(inbound) = &changes.inbound {
            facts.inbound.clone_from(inbound);
        }
        if let Some(seconds) = changes.lease_seconds {
            facts.lease_seconds = seconds;
        }
        // A harness never clears these four: it leaves them out and what is stored stands (BAP/1 4.4.4).
        if let Some(model) = &changes.model {
            facts.model = Some(model.clone());
        }
        if let Some(tokens) = changes.context_used_tokens {
            facts.context_used_tokens = Some(tokens);
        }
        if let Some(version) = &changes.brigade_version {
            facts.brigade_version = Some(version.clone());
        }
        if let Some(peer) = &changes.sync_peer {
            facts.sync_peer = Some(peer.clone());
        }
    }
}

impl Message {
    /// The message as the envelope of BAP/1 4.4.5.
    #[must_use]
    pub fn envelope(&self) -> Value {
        let accepted = &self.accepted;
        let mut sender = Map::new();
        sender.insert("principal_ref".into(), json!(accepted.sender_principal_ref));
        if !accepted.sender_human_label.is_empty() {
            sender.insert("human_label".into(), json!(accepted.sender_human_label));
        }
        sender.insert("session_id".into(), json!(accepted.sender_session_id));
        sender.insert("session_name".into(), json!(accepted.sender_session_name));
        let mut envelope = Map::new();
        envelope.insert("protocol_version".into(), json!(PROTOCOL_VERSION));
        envelope.insert("kind".into(), json!("text"));
        envelope.insert("message_id".into(), json!(accepted.message_id));
        envelope.insert("team_ref".into(), json!(accepted.team_ref));
        envelope.insert("sender".into(), Value::Object(sender));
        envelope.insert(
            "recipient_session_id".into(),
            json!(accepted.recipient_session_id),
        );
        if !accepted.summary.is_empty() {
            envelope.insert("summary".into(), json!(accepted.summary));
        }
        envelope.insert("body".into(), json!(accepted.body));
        envelope.insert("reply_to".into(), json!(accepted.reply_to));
        envelope.insert("hop_count".into(), json!(accepted.hop_count));
        envelope.insert("created_at".into(), json!(self.created_at.rfc3339()));
        envelope.insert("delivery_state".into(), json!("accepted"));
        Value::Object(envelope)
    }
}

impl Team {
    /// Whether the principal is an active member.
    #[must_use]
    pub fn active(&self, principal_ref: &str) -> bool {
        self.members
            .get(principal_ref)
            .is_some_and(|member| member.active)
    }

    /// The label of a principal's membership; empty when it has none.
    #[must_use]
    pub fn label(&self, principal_ref: &str) -> &str {
        self.members
            .get(principal_ref)
            .map_or("", |member| member.human_label.as_str())
    }

    /// A session as the record of BAP/1 4.4.3. The label is the membership's when it is read, so a label
    /// that changed shows everywhere at once.
    #[must_use]
    pub fn session_record(&self, session: &Session, now: Timestamp, is_self: bool) -> Map<String, Value> {
        let facts = &session.facts;
        let mut record = Map::new();
        record.insert("session_id".into(), json!(session.session_id));
        record.insert("session_name".into(), json!(facts.session_name));
        record.insert("session_description".into(), json!(facts.session_description));
        record.insert("principal_ref".into(), json!(session.principal_ref));
        let label = self.label(&session.principal_ref);
        if !label.is_empty() {
            record.insert("human_label".into(), json!(label));
        }
        record.insert("state".into(), json!(session.state(now)));
        record.insert("activity".into(), json!(facts.activity));
        record.insert("inbound".into(), json!(facts.inbound));
        record.insert("last_seen_at".into(), json!(session.last_seen_at.rfc3339()));
        record.insert("lease_until".into(), json!(session.lease_until.rfc3339()));
        record.insert("harness".into(), json!(facts.harness));
        record.insert("harness_version".into(), json!(facts.harness_version));
        record.insert("workspace_label".into(), json!(facts.workspace_label));
        // The four facts a harness may never have reported are left out until it does (BAP/1 4.4.3).
        if let Some(model) = &facts.model {
            record.insert("model".into(), json!(model));
        }
        if let Some(tokens) = facts.context_used_tokens {
            record.insert("context_used_tokens".into(), json!(tokens));
        }
        if let Some(version) = &facts.brigade_version {
            record.insert("brigade_version".into(), json!(version));
        }
        if let Some(peer) = &facts.sync_peer {
            record.insert("sync_peer".into(), json!(peer));
        }
        record.insert("created_at".into(), json!(session.created_at.rfc3339()));
        record.insert("is_self".into(), json!(is_self));
        record
    }

    fn adopt_label(&mut self, principal_ref: &str, offered: Option<&String>) {
        // The registration's label is the harness's default. It fills a membership that has none and never
        // replaces one that is there, whatever a later registration says (BAP/1 4.4.2).
        if let (Some(member), Some(offered)) = (self.members.get_mut(principal_ref), offered)
            && member.human_label.is_empty()
        {
            member.human_label.clone_from(offered);
        }
    }
}

impl State {
    /// The principal a credential's hash names, when the server knows the credential.
    #[must_use]
    pub fn principal_of(&self, credential_hash: &str) -> Option<&str> {
        self.credentials.get(credential_hash).map(String::as_str)
    }

    /// The team a session is in, and the session.
    #[must_use]
    pub fn session(&self, session_id: &str) -> Option<(&Team, &Session)> {
        let team = self.teams.get(self.session_teams.get(session_id)?)?;
        Some((team, team.sessions.get(session_id)?))
    }

    fn session_mut(&mut self, team_ref: &str, session_id: &str) -> Option<&mut Session> {
        self.teams.get_mut(team_ref)?.sessions.get_mut(session_id)
    }

    /// Applies the next entry of the log: the records one request made.
    ///
    /// # Errors
    /// When the entry is not the one after the last applied. The log was read wrongly, or is not this
    /// state's log, and nothing of the entry is applied.
    pub fn replay(&mut self, entry: &Entry) -> Result<(), String> {
        if entry.sequence != self.entries + 1 {
            return Err(format!(
                "entry {} of the log follows entry {}: an entry was skipped or repeated",
                entry.sequence, self.entries
            ));
        }
        for record in &entry.records {
            self.apply(record);
        }
        self.entries = entry.sequence;
        Ok(())
    }

    /// Applies one record. A record that names something the state does not hold changes nothing: the
    /// records of one log always agree with the state they were made against.
    pub fn apply(&mut self, record: &Record) {
        let at = record.at;
        self.latest_at = self.latest_at.max(at);
        match &record.event {
            Event::CredentialIssued {
                principal_ref,
                credential_hash,
            } => {
                self.principals
                    .entry(principal_ref.clone())
                    .or_insert(Principal { created_at: at });
                self.credentials
                    .insert(credential_hash.clone(), principal_ref.clone());
            }
            Event::CredentialRevoked { credential_hash } => {
                self.credentials.remove(credential_hash);
            }
            Event::TeamCreated {
                team_ref,
                team_name,
                join_secret_hash,
                created_by,
            } => {
                self.teams.entry(team_ref.clone()).or_insert_with(|| Team {
                    team_ref: team_ref.clone(),
                    team_name: team_name.clone(),
                    join_secret_hash: join_secret_hash.clone(),
                    created_by: created_by.clone(),
                    created_at: at,
                    members: BTreeMap::new(),
                    sessions: BTreeMap::new(),
                });
            }
            Event::MemberJoined {
                team_ref,
                principal_ref,
                human_label,
            } => {
                if let Some(team) = self.teams.get_mut(team_ref) {
                    match team.members.get_mut(principal_ref) {
                        Some(member) => {
                            member.active = true;
                            if !human_label.is_empty() {
                                member.human_label.clone_from(human_label);
                            }
                        }
                        None => {
                            team.members
                                .insert(principal_ref.clone(), Member::new(human_label.clone(), at));
                        }
                    }
                }
            }
            Event::MemberLeft {
                team_ref,
                principal_ref,
            } => {
                if let Some(team) = self.teams.get_mut(team_ref) {
                    if let Some(member) = team.members.get_mut(principal_ref) {
                        member.active = false;
                    }
                    for session in team.sessions.values_mut() {
                        if session.principal_ref == *principal_ref && session.closed_at.is_none() {
                            session.closed_at = Some(at);
                        }
                    }
                }
            }
            Event::SessionOpened {
                team_ref,
                session_id,
                principal_ref,
                facts,
                human_label,
            } => {
                if let Some(team) = self.teams.get_mut(team_ref) {
                    let mut session = Session {
                        session_id: session_id.clone(),
                        principal_ref: principal_ref.clone(),
                        facts: facts.clone(),
                        created_at: at,
                        ..Session::default()
                    };
                    session.renew(at);
                    team.sessions.insert(session_id.clone(), session);
                    team.adopt_label(principal_ref, human_label.as_ref());
                    self.session_teams.insert(session_id.clone(), team_ref.clone());
                }
            }
            Event::SessionResumed {
                team_ref,
                session_id,
                facts,
                human_label,
            } => {
                if let Some(team) = self.teams.get_mut(team_ref)
                    && let Some(session) = team.sessions.get_mut(session_id)
                {
                    session.closed_at = None;
                    session.facts = facts.clone();
                    session.renew(at);
                    let principal_ref = session.principal_ref.clone();
                    team.adopt_label(&principal_ref, human_label.as_ref());
                }
            }
            Event::SessionHeartbeat {
                team_ref,
                session_id,
                changes,
            } => {
                if let Some(session) = self.session_mut(team_ref, session_id) {
                    session.change(changes);
                    session.renew(at);
                }
            }
            Event::SessionClosed { team_ref, session_id } => {
                if let Some(session) = self.session_mut(team_ref, session_id)
                    && session.closed_at.is_none()
                {
                    session.closed_at = Some(at);
                }
            }
            Event::MessageAccepted { message } => self.accept(message, at),
            Event::MessageAcked {
                team_ref,
                session_id,
                message_ids,
            } => {
                if let Some(session) = self.session_mut(team_ref, session_id) {
                    for message_id in message_ids {
                        if let Some(sequence) = session.sequences.get(message_id)
                            && let Some(mut message) = session.pending.remove(sequence)
                        {
                            message.acked_at = Some(at);
                            session.acked.insert(*sequence, message);
                        }
                    }
                }
            }
        }
        if self.swept_at.seconds_until(at) >= SWEEP_INTERVAL_SECONDS {
            self.sweep(at);
        }
    }

    fn accept(&mut self, message: &AcceptedMessage, at: Timestamp) {
        let Some(team) = self.teams.get_mut(&message.team_ref) else {
            return;
        };
        if let Some(recipient) = team.sessions.get_mut(&message.recipient_session_id) {
            recipient.last_sequence += 1;
            let sequence = recipient.last_sequence;
            recipient.sequences.insert(message.message_id.clone(), sequence);
            recipient.pending.insert(
                sequence,
                Message {
                    accepted: message.clone(),
                    created_at: at,
                    acked_at: None,
                },
            );
        }
        if let Some(sender) = team.sessions.get_mut(&message.sender_session_id) {
            sender.last_seen_at = at;
            sender.sent_at.push_back(at);
            if let Some(idempotency) = &message.idempotency {
                sender.idempotency.insert(
                    idempotency.key.clone(),
                    IdempotencyEntry {
                        fingerprint: idempotency.fingerprint.clone(),
                        message_id: message.message_id.clone(),
                        recipient_session_id: message.recipient_session_id.clone(),
                        hop_count: message.hop_count,
                        created_at: at,
                    },
                );
            }
        }
        if let Some(member) = team.members.get_mut(&message.sender_principal_ref) {
            member.sent_at.push_back(at);
        }
    }

    /// Lets go of what retention no longer holds, as of the record time `at` (BAP/1 4.5.9): a session that
    /// has been offline for longer than a closed session is kept, with its messages; an acknowledged message
    /// older than acknowledged messages are kept; an unacknowledged one older than the floor.
    fn sweep(&mut self, at: Timestamp) {
        self.swept_at = at;
        let acked_horizon = at.minus_seconds(limits::RETENTION_ACKED_MESSAGE_SECONDS);
        let unacked_horizon = at.minus_seconds(limits::RETENTION_UNACKED_MESSAGE_SECONDS);
        // A key is kept at least as long as its message can be (4.5.4): unacknowledged for a week, then
        // acknowledged for a day.
        let key_horizon = unacked_horizon.minus_seconds(limits::RETENTION_ACKED_MESSAGE_SECONDS);
        let rate_horizon = at.minus_seconds(LONGEST_RATE_WINDOW_SECONDS);
        for team in self.teams.values_mut() {
            let session_teams = &mut self.session_teams;
            team.sessions.retain(|session_id, session| {
                let keep = session.retained(at);
                if !keep {
                    session_teams.remove(session_id);
                }
                keep
            });
            for session in team.sessions.values_mut() {
                session
                    .acked
                    .retain(|_, message| message.acked_at.is_none_or(|acked_at| acked_at >= acked_horizon));
                session
                    .pending
                    .retain(|_, message| message.created_at >= unacked_horizon);
                let (pending, acked) = (&session.pending, &session.acked);
                session
                    .sequences
                    .retain(|_, sequence| pending.contains_key(sequence) || acked.contains_key(sequence));
                session
                    .idempotency
                    .retain(|_, entry| entry.created_at >= key_horizon);
                trim(&mut session.sent_at, rate_horizon);
            }
            for member in team.members.values_mut() {
                trim(&mut member.sent_at, rate_horizon);
            }
        }
    }
}

/// Drops the times at or before `horizon` from the front of a list that is in time order.
fn trim(times: &mut VecDeque<Timestamp>, horizon: Timestamp) {
    while times.front().is_some_and(|at| *at <= horizon) {
        times.pop_front();
    }
}
