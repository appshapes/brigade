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
//!
//! Five changes are not requests, and are the operator's alone: [`answer`] has no path to any of them.
//! [`import_team`] takes in a team that exists on another backend, with the join secret it has there,
//! [`set_owner`] names the owner of a team, [`take_back`] takes a team from whoever holds it, and
//! [`open_move`] and [`close_move`] open and close the move of a team's members from another backend.
//! Each makes its entry as a request does, and the caller appends and syncs that entry in the same way.

use std::collections::BTreeMap;

use serde_json::{Map, Value, json};

use crate::attempts::JoinFailures;
use crate::error::Error;
use crate::hash::{Entropy, new_credential, new_ref, same, sha256_hex};
use crate::json::Document;
use crate::limits;
use crate::record::{AcceptedMessage, Entry, Event, Idempotency, Record, SessionChanges, SessionFacts};
use crate::state::{Session, State, Team};
use crate::time::Timestamp;
use crate::wire::{
    Ack, Command, Heartbeat, JOIN_SECRET_PREFIX, JoinSecret, Receive, Registration, Revocation, Send,
    SessionList, TeamCreate, TeamJoin, TeamRevoke, TeamTransfer, check_team_name,
};

/// One request as it arrived.
#[derive(Clone, Copy, Debug)]
pub struct Request<'a> {
    /// The SHA-256 of the credential the request presented, when it presented one.
    pub caller: Option<&'a str>,
    /// Whether the caller may create a team: the server asks for no creation token, or the request
    /// presented the right one (the contract's section 3).
    pub may_create: bool,
    /// Whether the caller's join may make it the owner of a team that has none, one that was taken in
    /// from another backend (the contract's section 3a). A member that joins over HTTP may; a gateway,
    /// which joins inside the server, never does.
    pub may_own: bool,
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
/// names or, for an administration request, that the caller owns it, and then its shape and the rules.
/// Nothing of the body is read beyond its members' names until the credential is known, so a request
/// costs the server its bytes and no more, whoever sent it.
///
/// `now` is the caller's one reading of the clock for this request, and `entropy` the source of every new
/// identifier.
///
/// # Errors
/// The failure to answer with, as BAP/1's section 4.6 and the contract's sections 1 and 3 name it. The
/// state is unchanged.
pub fn answer(
    state: &mut State,
    failures: &mut JoinFailures,
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
    if Command::administration_path(request.path) {
        let team_ref = document.named_required_text("team_ref")?;
        owned_team(state, &team_ref, principal_ref.unwrap_or_default())?;
    }
    let command = Command::read(request.path, &document)?;
    execute(
        state,
        failures,
        request.caller,
        request.may_own,
        &command,
        now,
        entropy,
    )
}

/// Answers one request that was already read. `may_own` is the request's own
/// ([`Request::may_own`]), and only a join reads it.
///
/// # Errors
/// The failure to answer with. The state is unchanged.
pub fn execute(
    state: &mut State,
    failures: &mut JoinFailures,
    caller: Option<&str>,
    may_own: bool,
    command: &Command,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    match command {
        Command::TeamCreate(request) => team_create(state, caller, request, now, entropy),
        Command::TeamJoin(request) => team_join(state, failures, caller, may_own, request, now, entropy),
        Command::TeamLeave(team_ref) => team_leave(state, caller, team_ref, now),
        Command::TeamMembers(team_ref) => team_members(state, caller, team_ref, now),
        Command::TeamRotateSecret(team_ref) => team_rotate_secret(state, caller, team_ref, now, entropy),
        Command::TeamRevokeMember(request) => team_revoke_member(state, caller, request, now),
        Command::TeamTransfer(request) => team_transfer(state, caller, request, now),
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

/// Takes in a team that already exists elsewhere, under `team_name`, keeping its join secret.
///
/// A join checks the hash of the whole secret against the team the secret names, so the team is here once
/// one `team.created` record carries the reference inside the secret, the name and that hash. Its members
/// then join with the secret they already have, as they would join a team that was created here. The team
/// is taken in with no member and no principal: nobody here created it, so the record names no creator.
///
/// Taking in a team that is here under the same secret changes nothing and makes no entry; the answer then
/// carries the name the team has. The answer is `team_ref`, `team_name` and `imported`, and neither it nor
/// a refusal holds any part of the secret.
///
/// # Errors
/// `invalid_input` when `join_secret` is not a join secret or `team_name` breaks the rule a created team's
/// name is held to; `conflict` when a team of that reference is here under another join secret. The state is
/// unchanged.
pub fn import_team(
    state: &mut State,
    team_name: &str,
    join_secret: &str,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let join_secret = JoinSecret::parse(join_secret)?;
    check_team_name(team_name)?;
    let team_ref = join_secret.team_ref();
    let join_secret_hash = sha256_hex(join_secret.expose());
    if let Some(team) = state.teams.get(team_ref) {
        if !same(&team.join_secret_hash, &join_secret_hash) {
            return Err(Error::conflict(
                "a team of that reference is here under another join secret",
                "other_join_secret",
            ));
        }
        return Ok(Outcome::read(
            json!({"team_ref": team_ref, "team_name": team.team_name, "imported": false}),
        ));
    }
    let created = Event::TeamCreated {
        team_ref: team_ref.to_owned(),
        team_name: team_name.to_owned(),
        join_secret_hash,
        created_by: String::new(),
    };
    let entry = commit(state, now, vec![created]);
    Ok(Outcome {
        result: json!({"team_ref": team_ref, "team_name": team_name, "imported": true}),
        entry,
    })
}

/// Names the owner of a team, for the server's operator: the principal becomes the team's owner whoever
/// owned it before, and whether or not anyone did.
///
/// It is how a team that was taken in gets the owner its admin should be when someone else joined first,
/// and how a team whose owner lost its credential gets another. The principal must be an active member of
/// the team, as the one a transfer names must be. Naming the principal that already owns the team changes
/// nothing and makes no entry. The answer is `team_ref`, `team_name`, `principal_ref`, the member's
/// `human_label` and `changed`.
///
/// # Errors
/// `not_found` when the server holds no team of that reference; `invalid_input` naming `principal_ref`
/// when the principal is not an active member of the team. The state is unchanged.
pub fn set_owner(
    state: &mut State,
    team_ref: &str,
    principal_ref: &str,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let team = state.teams.get(team_ref).ok_or_else(Error::no_such_team)?;
    if !team.active(principal_ref) {
        return Err(not_an_active_member());
    }
    let changed = team.owner() != Some(principal_ref);
    let result = json!({
        "team_ref": team.team_ref,
        "team_name": team.team_name,
        "principal_ref": principal_ref,
        "human_label": team.label(principal_ref),
        "changed": changed,
    });
    let entry = if changed {
        let owned = Event::TeamOwnerChanged {
            team_ref: team_ref.to_owned(),
            principal_ref: principal_ref.to_owned(),
        };
        commit(state, now, vec![owned])
    } else {
        None
    };
    Ok(Outcome { result, entry })
}

/// Takes a team back from whoever holds it, for the server's operator.
///
/// The first member to join a team that has no owner owns it, and naming another owner takes an active
/// member. So whoever held a leaked join secret, joined first, rotated the secret and revoked everyone
/// else would leave the operator nothing to name and the team's admin no way in. This is the way back,
/// and it is one entry, so the log holds all of it or none:
///
/// - the team has a new join secret, minted as a rotation mints one, and its count of secrets is one more,
///   and a move of the team that is open is closed, as a rotation closes it;
/// - every active membership ends and its open sessions are closed, with nobody spared, while a banned
///   member stays banned and a member that left stays as it was;
/// - the team has no owner, so the first member to join it with the new secret over HTTP owns it, as on a
///   team that was just taken in.
///
/// The answer is `team_ref`, `team_name`, `secret_version`, `join_secret`, `revoked` and
/// `sessions_closed`. Its `join_secret` is the one place the new secret is: the caller writes it to the
/// operator's file and nowhere else.
///
/// # Errors
/// `not_found` when the server holds no team of that reference. The state is unchanged.
pub fn take_back(
    state: &mut State,
    team_ref: &str,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    let team = state.teams.get(team_ref).ok_or_else(Error::no_such_team)?;
    let join_secret = new_join_secret(team, entropy);
    let secret_version = team.secret_version.saturating_add(1);
    // Every version a member can have joined with, and no principal left out.
    let everyone = team.joined_up_to(u32::MAX, "");
    let revoked = everyone.len();
    let sessions_closed = team.open_sessions(|member| everyone.contains(member), now);
    let owned = team.owner().is_some();
    let moving = team.move_ticket.is_some();
    let team_name = team.team_name.clone();
    let mut events = vec![Event::TeamSecretRotated {
        team_ref: team_ref.to_owned(),
        join_secret_hash: sha256_hex(&join_secret),
        secret_version,
    }];
    // A team that is taken back opens to its new secret and to nothing else: the ticket of a move could
    // be in the hands that the team is taken from.
    if moving {
        events.push(Event::TeamMoveClosed {
            team_ref: team_ref.to_owned(),
        });
    }
    if revoked > 0 {
        events.push(Event::MembersRevokedByVersion {
            team_ref: team_ref.to_owned(),
            max_version: u32::MAX,
            except: String::new(),
        });
    }
    if owned {
        events.push(Event::TeamOwnerChanged {
            team_ref: team_ref.to_owned(),
            principal_ref: String::new(),
        });
    }
    let entry = commit(state, now, events);
    Ok(Outcome {
        result: json!({
            "team_ref": team_ref,
            "team_name": team_name,
            "secret_version": secret_version,
            "join_secret": join_secret,
            "revoked": revoked,
            "sessions_closed": sessions_closed,
        }),
        entry,
    })
}

/// How many days a move ticket admits for when the operator names no number.
pub const MOVE_TICKET_DEFAULT_DAYS: u32 = 30;

/// The most days a move ticket admits for.
pub const MOVE_TICKET_MAX_DAYS: u32 = 365;

/// Opens the move of a team's members from another backend, for the server's operator.
///
/// A team that was taken in keeps its join secret, and no member's machine keeps that secret: a member
/// that is to follow the team here with no command of its own needs something else to join with. That is
/// the move ticket, a second join secret of the team for `days` days. It is minted as a join secret is, so
/// a join carries it as it carries the secret, and the server keeps its hash and its end. A join it lets
/// in is a join in everything but one: it never makes the team's owner, so the team's admin joins with
/// the secret itself, first.
///
/// A team has one move at most. Opening one for a team that has one, ended or not, puts the new ticket
/// in its place, and the ticket before it admits nobody from then on: it is how a ticket whose file was
/// lost is replaced. The answer is `team_ref`, `team_name`, `ticket`, `ends_at` and `replaced`. Its
/// `ticket` is the one place the ticket is: the caller writes it to the operator's file and nowhere else.
///
/// # Errors
/// `not_found` when the server holds no team of that reference; `invalid_input` when `days` is not from
/// one to [`MOVE_TICKET_MAX_DAYS`]. The state is unchanged.
pub fn open_move(
    state: &mut State,
    team_ref: &str,
    days: u32,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    let team = state.teams.get(team_ref).ok_or_else(Error::no_such_team)?;
    if !(1..=MOVE_TICKET_MAX_DAYS).contains(&days) {
        return Err(Error::out_of_range(
            "ticket_days",
            1,
            i64::from(MOVE_TICKET_MAX_DAYS),
        ));
    }
    let ticket = new_join_secret(team, entropy);
    // The end is a whole second, so that the time the operator is told is the time the record holds.
    let ends_at = now.plus_seconds(i64::from(days) * 86_400);
    let ends_at = Timestamp(ends_at.0 - ends_at.0.rem_euclid(1_000_000_000));
    let replaced = team.move_ticket.is_some();
    let team_name = team.team_name.clone();
    let opened = Event::TeamMoveOpened {
        team_ref: team_ref.to_owned(),
        ticket_hash: sha256_hex(&ticket),
        ends_at,
    };
    let entry = commit(state, now, vec![opened]);
    Ok(Outcome {
        result: json!({
            "team_ref": team_ref,
            "team_name": team_name,
            "ticket": ticket,
            "ends_at": ends_at.rfc3339(),
            "replaced": replaced,
        }),
        entry,
    })
}

/// Closes the move of a team, for the server's operator: its ticket admits nobody from now on, whether
/// or not it had ended. Every member that came in with it stays a member.
///
/// A team with no move is left as it is and makes no entry. The answer is `team_ref`, `team_name` and
/// `closed`.
///
/// # Errors
/// `not_found` when the server holds no team of that reference. The state is unchanged.
pub fn close_move(state: &mut State, team_ref: &str, now: Timestamp) -> Result<Outcome, Error> {
    let team = state.teams.get(team_ref).ok_or_else(Error::no_such_team)?;
    let closed = team.move_ticket.is_some();
    let result = json!({"team_ref": team.team_ref, "team_name": team.team_name, "closed": closed});
    let entry = if closed {
        let closed = Event::TeamMoveClosed {
            team_ref: team_ref.to_owned(),
        };
        commit(state, now, vec![closed])
    } else {
        None
    };
    Ok(Outcome { result, entry })
}

/// Whether `presented`, the SHA-256 of what a join carried, is the hash of the team's move ticket, and the
/// ticket has not ended at `now`.
///
/// The hashes are compared whether or not the ticket has ended, so a ticket past its end costs what one
/// that admits costs, and is told what any wrong secret is told.
fn ticket_admits(team: &Team, presented: &str, now: Timestamp) -> bool {
    team.move_ticket
        .as_ref()
        .is_some_and(|ticket| same(&ticket.hash, presented) & (now < ticket.ends_at))
}

/// A new join secret for a team that has one already: minted as a created team's is, around the reference
/// the team has. The reference is what a member's files and the team file key by, and it never changes.
fn new_join_secret(team: &Team, entropy: &mut dyn Entropy) -> String {
    format!("{JOIN_SECRET_PREFIX}{}.{}", team.team_ref, new_ref(entropy))
}

/// The refusal of a principal that is to own a team and is not an active member of it. It is one answer
/// for a membership that was revoked and for a principal the team never had (BAP/1 4.4.10).
fn not_an_active_member() -> Error {
    Error::refused_value("principal_ref", "an active member of this team")
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

/// The team an administration request names, when the caller is its owner and an active member of it.
///
/// The refusal is the one answer [`member_team`] gives, for a team that does not exist, a caller that is
/// not an active member, an active member that is not the owner, an owner that left, and a team that was
/// taken in and has no owner yet: nothing in it says whether the team exists or who owns it (the
/// contract's section 3a).
fn owned_team<'a>(state: &'a State, team_ref: &str, principal_ref: &str) -> Result<&'a Team, Error> {
    member_team(state, team_ref, principal_ref)
        .ok()
        .filter(|team| team.owner() == Some(principal_ref))
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
) -> Result<Outcome, Error> {
    // A principal the server knows creates a bounded number of teams in an hour. A caller with no
    // credential it knows is a new principal, which has created none: what holds a stranger is a limit by
    // address, and that is the proxy's in front of the server.
    if let Some(principal) = caller
        .and_then(|hash| state.principal_of(hash))
        .and_then(|principal_ref| state.principals.get(principal_ref))
    {
        within_budget(
            principal.teams_created_at.iter(),
            now,
            limits::PRINCIPAL_RATE_WINDOW_SECONDS,
            limits::TEAMS_CREATED_PER_HOUR,
            "team_create",
        )?;
    }
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
        by_move_ticket: false,
    });
    let entry = commit(state, now, events);
    let mut result = Map::new();
    result.insert("team_ref".into(), json!(team_ref));
    result.insert("team_name".into(), json!(request.team_name));
    result.insert("join_secret".into(), json!(join_secret));
    result.insert("principal_ref".into(), json!(principal_ref));
    Ok(Outcome {
        result: with_credential(result, credential),
        entry,
    })
}

fn team_join(
    state: &mut State,
    failures: &mut JoinFailures,
    caller: Option<&str>,
    may_own: bool,
    request: &TeamJoin,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    // The joins a principal fails are bounded: after as many as it may fail in the window, its next is
    // refused here, before the secret is looked at, the right one included. What is counted is a member's
    // join by a principal the server knows. A caller with no credential it knows has no principal until its
    // join succeeds. And a gateway's join, which is made inside the server and is the one that may not own,
    // is not counted: a gateway that was put out tries again each minute with the file its operator keeps,
    // and is let in the minute that file is right.
    let counted = may_own
        .then(|| caller.and_then(|hash| state.principal_of(hash)))
        .flatten()
        .map(str::to_owned);
    if let Some(principal_ref) = &counted {
        failures.admit(principal_ref, now)?;
    }
    // The hash is taken before the team is looked up, so a wrong secret and an unknown team cost the same.
    // A secret the team had before a rotation is a wrong secret: only the hash of the one it has is kept.
    let presented = sha256_hex(request.join_secret.expose());
    // A team opens to its join secret, and while it is moving here from another backend to its move
    // ticket, until the ticket ends. What was presented is held against both, whichever it is, and a
    // ticket that has ended, one whose move was closed, one that another took the place of and one of
    // another team are each a wrong secret, with the answer a wrong secret has.
    let admitted = state.teams.get(request.join_secret.team_ref()).and_then(|team| {
        let by_secret = same(&team.join_secret_hash, &presented);
        let by_ticket = ticket_admits(team, &presented, now);
        (by_secret | by_ticket).then_some((team, !by_secret))
    });
    let Some((team, by_move_ticket)) = admitted else {
        return Err(failures.rejected(counted.as_deref(), now));
    };
    // A banned principal is told what a wrong secret is told, although its secret is right, and nothing
    // is made for it. A caller with no credential the server knows is a new principal, which nobody
    // banned: the ban is on the principal, and the secret is what keeps a stranger out.
    let banned = caller
        .and_then(|hash| state.principal_of(hash))
        .and_then(|principal_ref| team.members.get(principal_ref))
        .is_some_and(|member| member.banned);
    if banned {
        return Err(failures.rejected(counted.as_deref(), now));
    }
    // The join is let in, and what its principal failed before it is forgotten.
    if let Some(principal_ref) = &counted {
        failures.succeeded(principal_ref);
    }
    let (team_ref, team_name) = (team.team_ref.clone(), team.team_name.clone());
    let ownerless = team.owner().is_none();
    let mut events = Vec::new();
    let (principal_ref, credential) = principal_or_new(state, caller, entropy, &mut events);
    let rejoined = state.teams[&team_ref].members.contains_key(&principal_ref);
    events.push(Event::MemberJoined {
        team_ref: team_ref.clone(),
        principal_ref: principal_ref.clone(),
        human_label: request.human_label.clone(),
        by_move_ticket,
    });
    // A team that was taken in has no owner, because nobody here created it. The first member whose join
    // may own it does, in the entry that makes it a member: there is no moment at which the team has that
    // member and still no owner. A join by the move ticket never owns, as a gateway's never does: the
    // ticket is read by every member of the team on the backend it leaves, and the team's admin joins
    // with the secret itself.
    if ownerless && may_own && !by_move_ticket {
        events.push(Event::TeamOwnerChanged {
            team_ref: team_ref.clone(),
            principal_ref: principal_ref.clone(),
        });
    }
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
                "joined_secret_version".into(),
                json!(member.joined_secret_version),
            );
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

fn team_rotate_secret(
    state: &mut State,
    caller: Option<&str>,
    team_ref: &str,
    now: Timestamp,
    entropy: &mut dyn Entropy,
) -> Result<Outcome, Error> {
    let principal_ref = authenticate(state, caller)?;
    let team = owned_team(state, team_ref, &principal_ref)?;
    let join_secret = new_join_secret(team, entropy);
    let secret_version = team.secret_version.saturating_add(1);
    let team_name = team.team_name.clone();
    let moving = team.move_ticket.is_some();
    // No member is touched: the version a member joined with is read only by a revocation that names it.
    let mut events = vec![Event::TeamSecretRotated {
        team_ref: team_ref.to_owned(),
        join_secret_hash: sha256_hex(&join_secret),
        secret_version,
    }];
    // A rotation is how an owner ends everything that let anyone in before it, and a move ticket is one
    // of those things: the move is closed in the rotation's own entry. The operator opens another when
    // the move is to go on.
    if moving {
        events.push(Event::TeamMoveClosed {
            team_ref: team_ref.to_owned(),
        });
    }
    let entry = commit(state, now, events);
    Ok(Outcome {
        result: json!({
            "team_ref": team_ref,
            "team_name": team_name,
            "secret_version": secret_version,
            "rotated_at": now.rfc3339(),
            "join_secret": join_secret,
        }),
        entry,
    })
}

fn team_revoke_member(
    state: &mut State,
    caller: Option<&str>,
    request: &TeamRevoke,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let owner = authenticate(state, caller)?;
    let team = owned_team(state, &request.team_ref, &owner)?;
    let team_ref = request.team_ref.clone();
    match &request.whom {
        Revocation::Principal { principal_ref, ban } => {
            // The owner cannot revoke itself: a ban of its own it could never lift, because lifting one
            // takes an owner that is an active member. Leaving is its way out.
            if *principal_ref == owner {
                return Err(Error::refused_value(
                    "principal_ref",
                    "a member other than the caller",
                ));
            }
            // The membership moves unless it is already what was asked for: revoked for a revocation,
            // banned for a ban. A revocation of a banned member is what lifts the ban. A principal the
            // team never had gets no membership made for it, and is no error.
            let changed = team
                .members
                .get(principal_ref)
                .is_some_and(|member| member.active || member.banned != *ban);
            // Only a membership that moves makes a record, and only a record closes a session.
            let (sessions_closed, entry) = if changed {
                let open = team.open_sessions(|member| member == principal_ref, now);
                let revoked = Event::MemberRevoked {
                    team_ref: team_ref.clone(),
                    principal_ref: principal_ref.clone(),
                    banned: *ban,
                };
                (open, commit(state, now, vec![revoked]))
            } else {
                (0, None)
            };
            Ok(Outcome {
                result: json!({
                    "team_ref": team_ref,
                    "principal_ref": principal_ref,
                    "status": if *ban { "banned" } else { "revoked" },
                    "changed": changed,
                    "sessions_closed": sessions_closed,
                }),
                entry,
            })
        }
        Revocation::UpToVersion(max_version) => {
            // The owner is left out whatever version it joined with: after one rotation the natural
            // request names version one, which is the owner's own.
            let revoked = team.joined_up_to(*max_version, &owner);
            let count = revoked.len();
            let sessions_closed = team.open_sessions(|member| revoked.contains(member), now);
            let entry = if count == 0 {
                None
            } else {
                let swept = Event::MembersRevokedByVersion {
                    team_ref: team_ref.clone(),
                    max_version: *max_version,
                    except: owner,
                };
                commit(state, now, vec![swept])
            };
            Ok(Outcome {
                result: json!({
                    "team_ref": team_ref,
                    "max_version": max_version,
                    "revoked": count,
                    "sessions_closed": sessions_closed,
                }),
                entry,
            })
        }
    }
}

fn team_transfer(
    state: &mut State,
    caller: Option<&str>,
    request: &TeamTransfer,
    now: Timestamp,
) -> Result<Outcome, Error> {
    let owner = authenticate(state, caller)?;
    let team = owned_team(state, &request.team_ref, &owner)?;
    if !team.active(&request.principal_ref) {
        return Err(not_an_active_member());
    }
    // The owner naming itself is accepted and changes nothing. The owner it was stays a member, and its
    // next administration request is refused as any member's is.
    let entry = if request.principal_ref == owner {
        None
    } else {
        let owned = Event::TeamOwnerChanged {
            team_ref: request.team_ref.clone(),
            principal_ref: request.principal_ref.clone(),
        };
        commit(state, now, vec![owned])
    };
    Ok(Outcome {
        result: json!({
            "team_ref": request.team_ref,
            "principal_ref": request.principal_ref,
            "transferred": true,
        }),
        entry,
    })
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
            // A principal opens a bounded number of sessions in an hour, over all its teams. A resume
            // opens none, so a session that comes back is never refused for it.
            if let Some(principal) = state.principals.get(&principal_ref) {
                within_budget(
                    principal.sessions_opened_at.iter(),
                    now,
                    limits::PRINCIPAL_RATE_WINDOW_SECONDS,
                    limits::SESSIONS_OPENED_PER_HOUR,
                    "register_session",
                )?;
            }
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

/// One budget: at most `limit` of what `times` are the times of, in the `window_seconds` before `now`. A
/// send budget counts sends; a principal's counts the teams it created, the sessions it opened or the joins
/// it failed.
pub(crate) fn within_budget<'a>(
    times: impl Iterator<Item = &'a Timestamp>,
    now: Timestamp,
    window_seconds: i64,
    limit: usize,
    reason: &'static str,
) -> Result<(), Error> {
    let window_start = now.minus_seconds(window_seconds);
    let mut count = 0;
    let mut oldest: Option<Timestamp> = None;
    for at in times.filter(|at| **at > window_start) {
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
