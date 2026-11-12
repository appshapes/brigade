//! `answer`: one request against the state.
//!
//! A request is checked against the state as it stands at `now`. When it is accepted, the records it makes
//! are applied to the state and handed back with the answer as one entry of the log; the caller appends
//! the entry and syncs it before it sends the answer, and ends the process if the append fails, so the
//! state is never ahead of the log. A request that is refused, or that changes nothing, makes no entry.
//!
//! Every rule here is one of BAP/1's section 4.5, which the contract hands to the server. The filesystem
//! adapter (`internal/adapters/fs`) is the worked example they were written against, and the stand-in
//! server (`internal/adapters/rest/stub`) the order a request is checked in.

use std::collections::BTreeMap;

use serde_json::{Map, Value, json};

use crate::error::Error;
use crate::hash::{Entropy, new_credential, new_ref, same, sha256_hex};
use crate::json::Document;
use crate::limits;
use crate::record::{AcceptedMessage, Entry, Event, Idempotency, Record, SessionChanges, SessionFacts};
use crate::state::{Session, State, Team};
use crate::time::Timestamp;
use crate::wire::{
    Ack, Command, Heartbeat, JOIN_SECRET_PREFIX, Receive, Registration, Send, SessionList, TeamCreate,
    TeamJoin,
};

/// One request as it arrived.
#[derive(Clone, Copy, Debug)]
pub struct Request<'a> {
    /// The SHA-256 of the credential the request presented, when it presented one.
    pub caller: Option<&'a str>,
    /// Whether the caller may create a team: the server asks for no creation token, or the request
    /// presented the right one (the contract's section 3).
    pub may_create: bool,
    /// The path of the request.
    pub path: &'a str,
    /// The body of the request.
    pub body: &'a [u8],
}

/// The answer to a request and the entry it made.
#[derive(Clone, Debug, PartialEq)]
pub struct Outcome {
    /// The `result` of the answer's envelope.
    pub result: Value,
    /// The entry to append and sync before the answer is sent, when the request changed something. Its
    /// records are already applied to the state.
    pub entry: Option<Entry>,
}

impl Outcome {
    fn read(result: Value) -> Self {
        Self { result, entry: None }
    }
}

/// Answers one request, from its path and its body.
///
/// The request is checked in the order the stand-in server checks one: the path, the body's size, that the
/// body is one JSON object, the credential, the creation token, the membership of the team the request
/// names, and then its shape and the rules. Nothing of the body is read beyond its members' names until
/// the credential is known, so a request costs the server its bytes and no more, whoever sent it.
///
/// `now` is the caller's one reading of the clock for this request, and `entropy` the source of every new
/// identifier.
///
/// # Errors
/// The failure to answer with, as BAP/1's section 4.6 and the contract's sections 1 and 3 name it. The
/// state is unchanged.
pub fn answer(
    state: &mut State,
    request: Request<'_>,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    if !Command::known_path(request.path) {
        return Err(Error::no_such_endpoint());
    }
    if request.body.len() > limits::MAX_REQUEST_BYTES {
        return Err(Error::body_too_long());
    }
    let document = Document::parse(request.body)?;
    let principal_ref = request.caller.and_then(|hash| state.principal_of(hash));
    if principal_ref.is_none() && !Command::open_path(request.path) {
        return Err(Error::unknown_credential());
    }
    if request.path == "/v1/team/create" && !request.may_create {
        return Err(Error::create_refused());
    }
    if Command::team_scoped_path(request.path) {
        let team_ref = document.named_required_text("team_ref")?;
        member_team(state, &team_ref, principal_ref.unwrap_or_default())?;
    }
    let command = Command::read(request.path, &document)?;
    execute(state, request.caller, &command, now, entropy)
}

/// Answers one request that was already read.
///
/// # Errors
/// The failure to answer with. The state is unchanged.
pub fn execute(
    state: &mut State,
    caller: Option<&str>,
    command: &Command,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    match command {
        Command::TeamCreate(request) => Ok(team_create(state, caller, request, now, entropy)),
        Command::TeamJoin(request) => team_join(state, caller, request, now, entropy),
        Command::TeamLeave(team_ref) => team_leave(state, caller, team_ref, now),
        Command::TeamMembers(team_ref) => team_members(state, caller, team_ref, now),
        Command::SessionRegister(request) => session_register(state, caller, request, now, entropy),
        Command::SessionHeartbeat(request) => session_heartbeat(state, caller, request, now),
        Command::SessionList(request) => session_list(state, caller, request, now),
        Command::SessionClose(session_id) => session_close(state, caller, session_id, now),
        Command::MessageSend(request) => message_send(state, caller, request, now, entropy),
        Command::MessageReceive(request) => message_receive(state, caller, request, now),
        Command::MessageAck(request) => message_ack(state, caller, request, now),
        Command::CredentialRevoke => credential_revoke(state, caller, now),
    }
}

/// Makes the records of one request, applies them, and numbers them as the log's next entry.
fn commit(state: &mut State, now: Timestamp, events: Vec<Event>) -> Option<Entry> {
    if events.is_empty() {
        return None;
    }
    let records: Vec<Record> = events.into_iter().map(|event| Record::new(now, event)).collect();
    for record in &records {
        state.apply(record);
    }
    state.entries += 1;
    Some(Entry {
        sequence: state.entries,
        records,
    })
}

/// The principal a request acts for: the one its credential names.
fn authenticate(state: &State, caller: Option<&str>) -> Result<String, Error> {
    caller
        .and_then(|hash| state.principal_of(hash))
        .map(str::to_owned)
        .ok_or_else(Error::unknown_credential)
}

/// The principal a create or a join acts for: the one a known credential names, else a new one whose
/// credential rides the result (the contract's section 3).
fn principal_or_new(
    state: &State,
    caller: Option<&str>,
    entropy: &mut dyn Entropy,
    events: &mut Vec<Event>,
) -> (String, Option<String>) {
    if let Some(principal_ref) = caller.and_then(|hash| state.principal_of(hash)) {
        return (principal_ref.to_owned(), None);
    }
    let principal_ref = new_ref(entropy);
    let credential = new_credential(entropy);
    events.push(Event::CredentialIssued {
        principal_ref: principal_ref.clone(),
        credential_hash: sha256_hex(&credential),
    });
    (principal_ref, Some(credential))
}

/// The team a request names, when the caller is an active member of it. The refusal is one answer for a
/// team that does not exist, a membership that never was and one that was revoked (BAP/1 4.5.7).
fn member_team<'a>(state: &'a State, team_ref: &str, principal_ref: &str) -> Result<&'a Team, Error> {
    state
        .teams
        .get(team_ref)
        .filter(|team| team.active(principal_ref))
        .ok_or_else(Error::not_member)
}

/// The session a request names, when the caller owns it and is still an active member of its team.
///
/// The owner of a session whose membership has ended is `unauthorized`: that is what ends a running watch
/// of a member who left (the contract's section 4). For any other id, an unknown one, a foreign team's and
/// a not-owned one alike, a caller that is an active member of some team is told the uniform `not_found`
/// (BAP/1 4.5.6, 4.5.7), and a caller that is a member of none is told `unauthorized`, as it is told for
/// everything else: the reference refuses a caller with no team before it looks at the id.
fn owned_session<'a>(
    state: &'a State,
    principal_ref: &str,
    session_id: &str,
    now: Timestamp,
) -> Result<(&'a Team, &'a Session), Error> {
    let owned = state
        .session(session_id)
        .filter(|(_, session)| session.principal_ref == principal_ref && session.retained(now));
    match owned {
        Some((team, session)) if team.active(principal_ref) => Ok((team, session)),
        Some(_) => Err(Error::not_member()),
        None if state.teams.values().any(|team| team.active(principal_ref)) => Err(Error::not_found()),
        None => Err(Error::not_member()),
    }
}

fn with_credential(mut result: Map<String, Value>, credential: Option<String>) -> Value {
    if let Some(credential) = credential {
        result.insert("credential".into(), json!(credential));
    }
    Value::Object(result)
}

fn team_create(
    state: &mut State,
    caller: Option<&str>,
    request: &TeamCreate,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Outcome {
    let mut events = Vec::new();
    let (principal_ref, credential) = principal_or_new(state, caller, entropy, &mut events);
    let team_ref = new_ref(entropy);
    let join_secret = format!("{JOIN_SECRET_PREFIX}{team_ref}.{}", new_ref(entropy));
    events.push(Event::TeamCreated {
        team_ref: team_ref.clone(),
        team_name: request.team_name.clone(),
        join_secret_hash: sha256_hex(&join_secret),
        created_by: principal_ref.clone(),
    });
    events.push(Event::MemberJoined {
        team_ref: team_ref.clone(),
        principal_ref: principal_ref.clone(),
        human_label: request.human_label.clone(),
    });
    let entry = commit(state, now, events);
    let mut result = Map::new();
    result.insert("team_ref".into(), json!(team_ref));
    result.insert("team_name".into(), json!(request.team_name));
    result.insert("join_secret".into(), json!(join_secret));
    result.insert("principal_ref".into(), json!(principal_ref));
    Outcome {
        result: with_credential(result, credential),
        entry,
    }
}

fn team_join(
    state: &mut State,
    caller: Option<&str>,
    request: &TeamJoin,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    // The hash is taken before the team is looked up, so a wrong secret and an unknown team cost the same.
    let presented = sha256_hex(request.join_secret.expose());
    let team = state
        .teams
        .get(request.join_secret.team_ref())
        .filter(|team| same(&team.join_secret_hash, &presented))
        .ok_or_else(Error::secret_rejected)?;
    let (team_ref, team_name) = (team.team_ref.clone(), team.team_name.clone());
    let mut events = Vec::new();
    let (principal_ref, credential) = principal_or_new(state, caller, entropy, &mut events);
    let rejoined = state.teams[&team_ref].members.contains_key(&principal_ref);
    events.push(Event::MemberJoined {
        team_ref: team_ref.clone(),
        principal_ref: principal_ref.clone(),
        human_label: request.human_label.clone(),
    });
    let entry = commit(state, now, events);
    let mut result = Map::new();
    result.insert("team_ref".into(), json!(team_ref));
    result.insert("team_name".into(), json!(team_name));
    result.insert("principal_ref".into(), json!(principal_ref));
    result.insert("rejoined".into(), json!(rejoined));
    Ok(Outcome {
        result: with_credential(result, credential),
        entry,
    })
}

fn team_leave(
    state: &mut State,
    caller: Option<&str>,
    team_ref: &str,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    // Leaving a team the caller is not an active member of is the success it always is, and changes
    // nothing (BAP/1 4.4.10: idempotent).
    let entry = if state
        .teams
        .get(team_ref)
        .is_some_and(|team| team.active(&principal_ref))
    {
        let left = Event::MemberLeft {
            team_ref: team_ref.to_owned(),
            principal_ref: principal_ref.clone(),
        };
        commit(state, now, vec![left])
    } else {
        None
    };
    Ok(Outcome {
        result: json!({"team_ref": team_ref, "principal_ref": principal_ref, "left": true}),
        entry,
    })
}

fn team_members(
    state: &State,
    caller: Option<&str>,
    team_ref: &str,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let team = member_team(state, team_ref, &principal_ref)?;
    // The last time any of a member's sessions was seen, in any state, and how many of them are not offline:
    // one pass over the team's sessions, whatever the team's size.
    let mut seen: BTreeMap<&str, (Timestamp, usize)> = BTreeMap::new();
    for session in team.sessions.values().filter(|session| session.retained(now)) {
        let of_member = seen
            .entry(session.principal_ref.as_str())
            .or_insert((session.last_seen_at, 0));
        of_member.0 = of_member.0.max(session.last_seen_at);
        of_member.1 += usize::from(!session.offline(now));
    }
    let members: Vec<Value> = team
        .members
        .iter()
        .filter(|(_, member)| member.active)
        .map(|(member_ref, member)| {
            let of_member = seen.get(member_ref.as_str());
            let mut entry = Map::new();
            entry.insert("principal_ref".into(), json!(member_ref));
            if !member.human_label.is_empty() {
                entry.insert("human_label".into(), json!(member.human_label));
            }
            entry.insert("status".into(), json!("active"));
            entry.insert("joined_at".into(), json!(member.joined_at.rfc3339()));
            entry.insert(
                "last_seen_at".into(),
                json!(of_member.map(|(last_seen, _)| last_seen.rfc3339())),
            );
            entry.insert(
                "session_count".into(),
                json!(of_member.map_or(0, |(_, count)| *count)),
            );
            Value::Object(entry)
        })
        .collect();
    Ok(Outcome::read(json!({
        "team_ref": team.team_ref,
        "team_name": team.team_name,
        "server_time": now.rfc3339(),
        "members": members,
    })))
}

fn session_register(
    state: &mut State,
    caller: Option<&str>,
    request: &Registration,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let team = member_team(state, &request.team_ref, &principal_ref)?;
    let lease_seconds = request.lease_seconds.unwrap_or(limits::LEASE_DEFAULT_SECONDS);
    let facts = SessionFacts {
        session_name: request.session_name.clone(),
        session_description: request.session_description.clone(),
        activity: request.activity.clone(),
        inbound: request.inbound.clone(),
        lease_seconds,
        harness: request.harness.clone(),
        harness_version: request.harness_version.clone(),
        workspace_label: request.workspace_label.clone(),
        model: request.model.clone(),
        context_used_tokens: request.context_used_tokens,
        brigade_version: request.brigade_version.clone(),
        sync_peer: request.sync_peer.clone(),
    };
    let team_ref = request.team_ref.clone();
    let human_label = request.human_label.clone().filter(|label| !label.is_empty());
    let (session_id, event) = match &request.resume {
        Some(session_id) => {
            // A resume re-opens a closed or expired session the caller owns, in place. One that is open
            // with a valid lease is refused and left as it is: two processes must never drain one inbox
            // (BAP/1 4.5.8).
            let session = team
                .sessions
                .get(session_id)
                .filter(|session| session.principal_ref == principal_ref && session.retained(now))
                .ok_or_else(Error::not_found)?;
            if !session.offline(now) {
                return Err(Error::conflict(
                    "that session is open with a valid lease",
                    "session_live",
                ));
            }
            (
                session_id.clone(),
                Event::SessionResumed {
                    team_ref,
                    session_id: session_id.clone(),
                    facts,
                    human_label,
                },
            )
        }
        None => {
            let session_id = new_ref(entropy);
            let opened = Event::SessionOpened {
                team_ref,
                session_id: session_id.clone(),
                principal_ref: principal_ref.clone(),
                facts,
                human_label,
            };
            (session_id, opened)
        }
    };
    let entry = commit(state, now, vec![event]);
    let team = &state.teams[&request.team_ref];
    let mut result = team.session_record(&team.sessions[&session_id], now, false);
    result.insert("resumed".into(), json!(request.resume.is_some()));
    result.insert("lease_seconds".into(), json!(lease_seconds));
    result.insert("server_time".into(), json!(now.rfc3339()));
    Ok(Outcome {
        result: Value::Object(result),
        entry,
    })
}

fn session_heartbeat(
    state: &mut State,
    caller: Option<&str>,
    request: &Heartbeat,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let (team, session) = owned_session(state, &principal_ref, &request.session_id, now)?;
    if session.closed_at.is_some() {
        return Err(Error::conflict("that session is closed", "session_closed"));
    }
    let changes = &request.changes;
    let event = Event::SessionHeartbeat {
        team_ref: team.team_ref.clone(),
        session_id: request.session_id.clone(),
        changes: SessionChanges {
            activity: changes.activity.clone(),
            session_name: changes.session_name.clone(),
            session_description: changes.session_description.clone(),
            inbound: changes.inbound.clone(),
            lease_seconds: changes.lease_seconds,
            model: changes.model.clone(),
            context_used_tokens: changes.context_used_tokens,
            brigade_version: changes.brigade_version.clone(),
            sync_peer: changes.sync_peer.clone(),
        },
    };
    let entry = commit(state, now, vec![event]);
    // The heartbeat just renewed the session, so it is there: nothing after the entry is made may fail,
    // because the state already holds it.
    let (_, session) = state
        .session(&request.session_id)
        .expect("the session a heartbeat just renewed");
    Ok(Outcome {
        result: json!({
            "session_id": session.session_id,
            "state": session.state(now),
            "lease_until": session.lease_until.rfc3339(),
            "server_time": now.rfc3339(),
        }),
        entry,
    })
}

fn session_list(
    state: &State,
    caller: Option<&str>,
    request: &SessionList,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let team = member_team(state, &request.team_ref, &principal_ref)?;
    // Only this team's sessions, and only those whose owner is still an active member: a member who left
    // takes its sessions out of the roster with it.
    let sessions: Vec<Value> = team
        .sessions
        .values()
        .filter(|session| team.active(&session.principal_ref) && session.retained(now))
        .filter(|session| request.include_offline || !session.offline(now))
        .map(|session| {
            let is_self = request.session_id.as_deref() == Some(session.session_id.as_str());
            Value::Object(team.session_record(session, now, is_self))
        })
        .collect();
    Ok(Outcome::read(json!({
        "team_ref": team.team_ref,
        "team_name": team.team_name,
        "server_time": now.rfc3339(),
        "sessions": sessions,
        "truncated": false,
    })))
}

fn session_close(
    state: &mut State,
    caller: Option<&str>,
    session_id: &str,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let (team, session) = owned_session(state, &principal_ref, session_id, now)?;
    // Closing a session that is closed changes nothing and is not an error (BAP/1 4.5.8).
    let entry = if session.closed_at.is_none() {
        let closed = Event::SessionClosed {
            team_ref: team.team_ref.clone(),
            session_id: session_id.to_owned(),
        };
        commit(state, now, vec![closed])
    } else {
        None
    };
    Ok(Outcome {
        result: json!({"session_id": session_id, "state": "offline"}),
        entry,
    })
}

/// The hop count of a send (BAP/1 4.5.12): one more than the message it answers, which must be one the
/// sender received; or, with no `reply_to`, one more than the most recent message the recipient sent the
/// sender within the implicit reply window, because an unlabelled answer is still an answer; or zero.
fn hop_count(sender: &Session, request: &Send, now: Timestamp) -> Result<u32, Error> {
    let hop = if request.reply_to.is_empty() {
        let window_start = now.minus_seconds(limits::IMPLICIT_REPLY_WINDOW_SECONDS);
        // The highest number is the most recent, which stays exact when two messages share a time.
        sender
            .pending
            .iter()
            .chain(sender.acked.iter())
            .filter(|(_, message)| {
                message.accepted.sender_session_id == request.recipient_session_id
                    && message.created_at > window_start
            })
            .max_by_key(|(sequence, _)| **sequence)
            .map_or(0, |(_, message)| message.accepted.hop_count + 1)
    } else {
        sender
            .received(&request.reply_to)
            .ok_or_else(Error::not_found)?
            .accepted
            .hop_count
            + 1
    };
    if hop > limits::MAX_HOP_COUNT {
        return Err(Error::loop_detected());
    }
    Ok(hop)
}

/// One send budget: at most `limit` sends in the `window_seconds` before `now`.
fn within_budget<'a>(
    sent_at: impl Iterator<Item = &'a Timestamp>,
    now: Timestamp,
    window_seconds: i64,
    limit: usize,
    reason: &'static str,
) -> Result<(), Error> {
    let window_start = now.minus_seconds(window_seconds);
    let mut count = 0;
    let mut oldest: Option<Timestamp> = None;
    for at in sent_at.filter(|at| **at > window_start) {
        count += 1;
        oldest = Some(oldest.map_or(*at, |oldest| oldest.min(*at)));
    }
    match oldest {
        Some(oldest) if count >= limit => {
            // The budget frees when its oldest send leaves the window.
            let wait = now.millis_until(oldest.plus_seconds(window_seconds));
            Err(Error::rate_limited(reason, u64::try_from(wait).unwrap_or(0)))
        }
        _ => Ok(()),
    }
}

/// The budgets and caps of BAP/1 4.5.12, in the order it fixes: the sender session's, the principal's,
/// then the pair's cap before the recipient's, so that one sender cannot make a recipient read as full for
/// everybody else.
fn within_limits(team: &Team, sender: &Session, recipient: &Session, now: Timestamp) -> Result<(), Error> {
    within_budget(
        sender.sent_at.iter(),
        now,
        60,
        limits::SEND_RATE_PER_MINUTE,
        "send_per_minute",
    )?;
    within_budget(
        sender.sent_at.iter(),
        now,
        3600,
        limits::SEND_RATE_PER_HOUR,
        "send_per_hour",
    )?;
    if let Some(member) = team.members.get(&sender.principal_ref) {
        within_budget(
            member.sent_at.iter(),
            now,
            60,
            limits::PRINCIPAL_SEND_RATE_PER_MINUTE,
            "principal_per_minute",
        )?;
        within_budget(
            member.sent_at.iter(),
            now,
            3600,
            limits::PRINCIPAL_SEND_RATE_PER_HOUR,
            "principal_per_hour",
        )?;
    }
    let from_sender = recipient
        .pending
        .values()
        .filter(|message| message.accepted.sender_session_id == sender.session_id)
        .count();
    if from_sender >= limits::MAX_UNACKED_PER_SENDER_RECIPIENT {
        return Err(Error::rate_limited(
            "sender_quota_for_recipient",
            limits::CAP_RETRY_AFTER_MS,
        ));
    }
    if recipient.pending.len() >= limits::MAX_UNACKED_PER_RECIPIENT {
        return Err(Error::rate_limited(
            "recipient_inbox_full",
            limits::CAP_RETRY_AFTER_MS,
        ));
    }
    Ok(())
}

fn send_response(
    message_id: &str,
    recipient: &str,
    created_at: Timestamp,
    duplicate: bool,
    hop_count: u32,
) -> Value {
    json!({
        "status": "accepted",
        "message_id": message_id,
        "recipient_session_id": recipient,
        "created_at": created_at.rfc3339(),
        "duplicate": duplicate,
        "hop_count": hop_count,
    })
}

fn message_send(
    state: &mut State,
    caller: Option<&str>,
    request: &Send,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let (team, sender) = owned_session(state, &principal_ref, &request.sender_session_id, now)?;
    if sender.closed_at.is_some() {
        return Err(Error::conflict("the sending session is closed", "session_closed"));
    }
    // The recipient is a session of the sender's team whose owner is still an active member. Sending to a
    // closed one is accepted: the message waits for retention or a resume (BAP/1 4.5.8).
    let recipient = team
        .sessions
        .get(&request.recipient_session_id)
        .filter(|recipient| team.active(&recipient.principal_ref) && recipient.retained(now))
        .ok_or_else(Error::not_found)?;
    let fingerprint = sha256_hex(&format!(
        "{}\0{}\0{}\0{}",
        request.recipient_session_id, request.body, request.summary, request.reply_to
    ));
    if let Some(first) = sender
        .idempotency
        .get(&request.idempotency_key)
        .filter(|_| !request.idempotency_key.is_empty())
    {
        // The same key with the same payload is the first send's answer again, and no second message; with
        // another payload it is a conflict (BAP/1 4.5.4).
        if first.fingerprint != fingerprint {
            return Err(Error::conflict(
                "this idempotency key was used for a different message",
                "idempotency_key_reused",
            ));
        }
        let answer = send_response(
            &first.message_id,
            &first.recipient_session_id,
            first.created_at,
            true,
            first.hop_count,
        );
        return Ok(Outcome::read(answer));
    }
    let hop_count = hop_count(sender, request, now)?;
    within_limits(team, sender, recipient, now)?;
    // Every identity member is stamped from the sender's session and membership, never from the request.
    let message = AcceptedMessage {
        message_id: new_ref(entropy),
        team_ref: team.team_ref.clone(),
        sender_principal_ref: sender.principal_ref.clone(),
        sender_human_label: team.label(&sender.principal_ref).to_owned(),
        sender_session_id: sender.session_id.clone(),
        sender_session_name: sender.facts.session_name.clone(),
        recipient_session_id: request.recipient_session_id.clone(),
        summary: request.summary.clone(),
        body: request.body.clone(),
        reply_to: Some(request.reply_to.clone()).filter(|reply_to| !reply_to.is_empty()),
        hop_count,
        idempotency: Some(Idempotency {
            key: request.idempotency_key.clone(),
            fingerprint,
        })
        .filter(|idempotency| !idempotency.key.is_empty()),
    };
    let answer = send_response(
        &message.message_id,
        &message.recipient_session_id,
        now,
        false,
        hop_count,
    );
    let entry = commit(state, now, vec![Event::MessageAccepted { message }]);
    Ok(Outcome {
        result: answer,
        entry,
    })
}

/// The cursor of a session whose last accepted message has the number `sequence`: "everything accepted for
/// this session up to now" (the contract's section 4). It names its session by a hash, so one session's
/// cursor is not recognised for another and nothing in it reads back as an id.
fn cursor(session_id: &str, sequence: u64) -> String {
    format!("c1.{}.{sequence}", &sha256_hex(session_id)[..12])
}

/// The number a cursor stands for. One this server did not issue for this session, or one that names a
/// number the session has not reached, is not recognised and is treated as absent.
fn cursor_floor(session: &Session, after: Option<&str>) -> u64 {
    after
        .and_then(|after| after.strip_prefix(&cursor(&session.session_id, 0)[..16]))
        .and_then(|number| number.parse::<u64>().ok())
        .filter(|number| *number <= session.last_sequence)
        .unwrap_or(0)
}

fn message_receive(
    state: &State,
    caller: Option<&str>,
    request: &Receive,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let (_, session) = owned_session(state, &principal_ref, &request.session_id, now)?;
    let floor = cursor_floor(session, request.after.as_deref());
    // Oldest first, and never an acknowledgement: receiving changes nothing.
    let messages: Vec<Value> = session
        .pending
        .range(floor + 1..)
        .take(request.limit)
        .map(|(_, message)| message.envelope())
        .collect();
    Ok(Outcome::read(
        json!({"messages": messages, "cursor": cursor(&session.session_id, session.last_sequence)}),
    ))
}

fn message_ack(
    state: &mut State,
    caller: Option<&str>,
    request: &Ack,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let (team, session) = owned_session(state, &principal_ref, &request.session_id, now)?;
    // A pending id is acknowledged, an id acknowledged before counts as acknowledged again, and anything
    // else, unknown or another session's, is `unknown` and never an error (BAP/1 4.5.3).
    let (mut acked, mut unknown, mut newly) = (Vec::new(), Vec::new(), Vec::new());
    for message_id in &request.message_ids {
        match session.sequences.get(message_id) {
            Some(sequence) => {
                if session.pending.contains_key(sequence) && !newly.contains(message_id) {
                    newly.push(message_id.clone());
                }
                acked.push(message_id.clone());
            }
            None => unknown.push(message_id.clone()),
        }
    }
    let entry = if newly.is_empty() {
        None
    } else {
        let event = Event::MessageAcked {
            team_ref: team.team_ref.clone(),
            session_id: request.session_id.clone(),
            message_ids: newly,
        };
        commit(state, now, vec![event])
    };
    Ok(Outcome {
        result: json!({"acked": acked, "unknown": unknown}),
        entry,
    })
}

fn credential_revoke(state: &mut State, caller: Option<&str>, now: Timestamp) -> Result<Outcome, Error> {
    authenticate(state, caller)?;
    // The presented credential becomes unknown. The principal's memberships are as they were.
    let credential_hash = caller.unwrap_or_default().to_owned();
    let entry = commit(state, now, vec![Event::CredentialRevoked { credential_hash }]);
    Ok(Outcome {
        result: json!({"revoked": true}),
        entry,
    })
}
