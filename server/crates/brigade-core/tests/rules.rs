//! The rules of BAP/1's section 4.5, checked against `execute` with no server around it.
//!
//! The conformance suite is the oracle for a running server; these tests are the same rules at the size of
//! one function call, with the clock in the test's hands.

use brigade_core::error::Code;
use brigade_core::hash::sha256_hex;
use brigade_core::record::Event;
use brigade_core::state::TakeInRetry;
use brigade_core::{
    Command, Entropy, Entry, Error, JoinFailures, Record, Request, State, Timestamp, answer, close_move,
    envelope_error, execute, import_team, limits, open_move, set_owner, take_back,
};
use serde_json::{Value, json};

/// A source of bytes that never repeats and is the same in every run.
struct Counter(u64);

impl Entropy for Counter {
    fn fill(&mut self, buffer: &mut [u8]) {
        for chunk in buffer.chunks_mut(8) {
            self.0 += 1;
            let bytes = self.0.to_be_bytes();
            chunk.copy_from_slice(&bytes[..chunk.len()]);
        }
    }
}

const SECOND: i64 = 1_000_000_000;

/// A state, a clock and every entry of the log made so far.
struct Rig {
    state: State,
    /// The joins each principal failed, which the server counts beside the state.
    failures: JoinFailures,
    entropy: Counter,
    now: Timestamp,
    log: Vec<Entry>,
    /// Whether a request may create a team: what the server's creation token decides.
    may_create: bool,
    /// Whether a join may own a team that has no owner: true over HTTP, false for a gateway.
    may_own: bool,
}

/// One member's installation: its credential and what it joined.
struct Member {
    credential: String,
    principal_ref: String,
    team_ref: String,
}

impl Rig {
    fn new() -> Self {
        Self {
            state: State::default(),
            failures: JoinFailures::new(),
            entropy: Counter(0),
            now: Timestamp(1_800_000_000 * SECOND),
            log: Vec::new(),
            may_create: true,
            may_own: true,
        }
    }

    fn advance(&mut self, seconds: i64) {
        self.now = self.now.plus_seconds(seconds);
    }

    fn call(&mut self, credential: Option<&str>, path: &str, body: Value) -> Result<Value, Error> {
        self.raw(credential, path, body.to_string().as_bytes())
    }

    fn raw(&mut self, credential: Option<&str>, path: &str, body: &[u8]) -> Result<Value, Error> {
        let hash = credential.map(sha256_hex);
        let request = Request {
            caller: hash.as_deref(),
            may_create: self.may_create,
            may_own: self.may_own,
            path,
            body,
        };
        let outcome = answer(
            &mut self.state,
            &mut self.failures,
            request,
            self.now,
            &mut self.entropy,
        )?;
        self.log.extend(outcome.entry);
        Ok(outcome.result)
    }

    fn ok(&mut self, member: &Member, path: &str, body: Value) -> Value {
        self.call(Some(&member.credential), path, body)
            .unwrap_or_else(|error| panic!("{path}: {error}"))
    }

    fn err(&mut self, member: &Member, path: &str, body: Value) -> Error {
        self.call(Some(&member.credential), path, body).expect_err(path)
    }

    /// Creates a team as a new principal; returns the creator and the join secret.
    fn create(&mut self, label: &str) -> (Member, String) {
        let result = self
            .call(
                None,
                "/v1/team/create",
                json!({"team_name": "ops", "human_label": label}),
            )
            .unwrap();
        let member = Member {
            credential: text(&result, "credential"),
            principal_ref: text(&result, "principal_ref"),
            team_ref: text(&result, "team_ref"),
        };
        (member, text(&result, "join_secret"))
    }

    /// Takes in a team that exists elsewhere, as the operator's command does: no request and no credential.
    fn import(&mut self, team_name: &str, secret: &str) -> Result<Value, Error> {
        let outcome = import_team(&mut self.state, team_name, secret, self.now)?;
        self.log.extend(outcome.entry);
        Ok(outcome.result)
    }

    /// Names a team's owner, as the operator's command does: no request and no credential.
    fn set_owner(&mut self, team_ref: &str, principal_ref: &str) -> Result<Value, Error> {
        let outcome = set_owner(&mut self.state, team_ref, principal_ref, self.now)?;
        self.log.extend(outcome.entry);
        Ok(outcome.result)
    }

    /// Takes a team back, as the operator's command does: no request and no credential.
    fn take_back(&mut self, team_ref: &str) -> Result<Value, Error> {
        let outcome = take_back(&mut self.state, team_ref, self.now, &mut self.entropy)?;
        self.log.extend(outcome.entry);
        Ok(outcome.result)
    }

    /// Opens a team's move for `days` days, as the operator's command does: no request and no credential.
    fn open_move(&mut self, team_ref: &str, days: u32) -> Result<Value, Error> {
        let outcome = open_move(&mut self.state, team_ref, days, self.now, &mut self.entropy)?;
        self.log.extend(outcome.entry);
        Ok(outcome.result)
    }

    /// Closes a team's move, as the operator's command does: no request and no credential.
    fn close_move(&mut self, team_ref: &str) -> Result<Value, Error> {
        let outcome = close_move(&mut self.state, team_ref, self.now)?;
        self.log.extend(outcome.entry);
        Ok(outcome.result)
    }

    /// Joins a team as a new principal.
    fn join(&mut self, secret: &str, label: &str) -> Member {
        let result = self
            .call(
                None,
                "/v1/team/join",
                json!({"join_secret": secret, "human_label": label}),
            )
            .unwrap();
        assert_eq!(result["rejoined"], json!(false));
        Member {
            credential: text(&result, "credential"),
            principal_ref: text(&result, "principal_ref"),
            team_ref: text(&result, "team_ref"),
        }
    }

    /// Registers a session and returns its id.
    fn register(&mut self, member: &Member, name: &str) -> String {
        let result = self.ok(member, "/v1/session/register", registration(member, name));
        text(&result, "session_id")
    }

    fn send(&mut self, member: &Member, from: &str, to: &str, body: &str) -> Result<Value, Error> {
        self.call(
            Some(&member.credential),
            "/v1/message/send",
            json!({"sender_session_id": from, "recipient_session_id": to, "body": body}),
        )
    }

    fn receive(&mut self, member: &Member, session: &str) -> Vec<Value> {
        let result = self.ok(member, "/v1/message/receive", json!({"session_id": session}));
        result["messages"].as_array().unwrap().clone()
    }

    fn ack(&mut self, member: &Member, session: &str, ids: &[&str]) -> Value {
        self.ok(
            member,
            "/v1/message/ack",
            json!({"session_id": session, "message_ids": ids}),
        )
    }

    fn list(&mut self, member: &Member, include_offline: bool) -> Vec<Value> {
        let body = json!({"team_ref": member.team_ref, "include_offline": include_offline});
        self.ok(member, "/v1/session/list", body)["sessions"]
            .as_array()
            .unwrap()
            .clone()
    }
}

fn text(value: &Value, member: &str) -> String {
    value[member]
        .as_str()
        .unwrap_or_else(|| panic!("no {member} in {value}"))
        .to_owned()
}

fn registration(member: &Member, name: &str) -> Value {
    json!({
        "team_ref": member.team_ref, "harness": "claude-code", "harness_version": "2.1.251",
        "session_name": name, "activity": "busy", "inbound": "accept",
    })
}

fn reason(error: &Error) -> &str {
    error.details.get("reason").map_or("", String::as_str)
}

/// Two members of one team, each with one session.
fn pair(rig: &mut Rig) -> (Member, String, Member, String) {
    let (alice, secret) = rig.create("alice@example.com");
    let bob = rig.join(&secret, "bob@example.com");
    let a = rig.register(&alice, "alpha");
    let b = rig.register(&bob, "beta");
    (alice, a, bob, b)
}

#[test]
fn a_team_is_created_with_a_secret_shown_once_and_a_credential() {
    let mut rig = Rig::new();
    let result = rig
        .call(None, "/v1/team/create", json!({"team_name": "ops"}))
        .unwrap();
    let secret = text(&result, "join_secret");
    assert_eq!(
        secret,
        format!(
            "brg1.{}.{}",
            text(&result, "team_ref"),
            secret.rsplit('.').next().unwrap()
        )
    );
    let credential = text(&result, "credential");
    assert!((16..=512).contains(&credential.len()));
    // The state keeps the hashes and never the secret or the credential.
    let stored = serde_json::to_string(&rig.state).unwrap();
    assert!(!stored.contains(&secret));
    assert!(!stored.contains(&credential));
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!logged.contains(&secret));
    assert!(!logged.contains(&credential));
}

#[test]
fn a_known_credential_acts_for_its_principal_and_gets_no_second_credential() {
    let mut rig = Rig::new();
    let (alice, _) = rig.create("alice");
    let again = rig.ok(&alice, "/v1/team/create", json!({"team_name": "second"}));
    assert_eq!(text(&again, "principal_ref"), alice.principal_ref);
    assert!(again.get("credential").is_none());
    assert_ne!(text(&again, "team_ref"), alice.team_ref);
}

#[test]
fn a_rejected_join_is_one_answer_and_creates_nothing() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let before = rig.state.clone();
    let wrong = rig
        .call(
            None,
            "/v1/team/join",
            json!({"join_secret": format!("{secret}x")}),
        )
        .unwrap_err();
    let unknown = rig
        .call(
            None,
            "/v1/team/join",
            json!({"join_secret": "brg1.nosuchteam.secret"}),
        )
        .unwrap_err();
    assert_eq!(wrong, unknown);
    assert_eq!(wrong.code, Code::Unauthorized);
    assert!(wrong.details.is_empty());
    assert_eq!(rig.state, before);
    // A malformed secret is the request's fault, and says nothing of what it held.
    let malformed = rig
        .call(None, "/v1/team/join", json!({"join_secret": "nope"}))
        .unwrap_err();
    assert_eq!(malformed.code, Code::InvalidInput);
    assert_eq!(rig.list(&alice, true).len(), 0);
}

/// The reference of a team on a backend that names its teams with UUIDs.
const ELSEWHERE: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";

/// The join secret a team has on another backend. It is put together when the test runs, because the
/// repository's secret scan refuses a file that holds a whole one.
fn secret_of(team_ref: &str, random: &str) -> String {
    format!("brg1.{team_ref}.{random}")
}

#[test]
fn a_team_taken_in_is_joined_with_the_secret_it_has_elsewhere() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let taken = rig.import("ops", &secret).unwrap();
    assert_eq!(
        taken,
        json!({"team_ref": ELSEWHERE, "team_name": "ops", "imported": true})
    );
    // One entry of one record: the team and its secret's hash, with no creator, no member and no principal.
    let created = Event::TeamCreated {
        team_ref: ELSEWHERE.into(),
        team_name: "ops".into(),
        join_secret_hash: sha256_hex(&secret),
        created_by: String::new(),
        taken_in: false,
    };
    assert_eq!(rig.log.len(), 1);
    assert_eq!(rig.log[0].records, [Record::new(rig.now, created)]);
    assert!(rig.state.teams[ELSEWHERE].members.is_empty());
    assert!(rig.state.principals.is_empty() && rig.state.credentials.is_empty());
    // The state and the log keep the hash and never the secret.
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!stored.contains("kept-from-the-other-backend"));
    assert!(!logged.contains("kept-from-the-other-backend"));

    // Its members join with the secret they have, and are a team exactly as on one that was created here.
    rig.advance(5);
    let alice = rig.join(&secret, "alice@example.com");
    let bob = rig.join(&secret, "bob@example.com");
    assert_eq!(
        (alice.team_ref.as_str(), bob.team_ref.as_str()),
        (ELSEWHERE, ELSEWHERE)
    );
    assert_ne!(alice.principal_ref, bob.principal_ref);
    let a = rig.register(&alice, "alpha");
    let b = rig.register(&bob, "beta");
    assert_eq!(rig.list(&bob, false).len(), 2);
    let sent = rig.send(&alice, &a, &b, "the first message here").unwrap();
    let received = rig.receive(&bob, &b);
    assert_eq!(received.len(), 1);
    assert_eq!(received[0]["body"], json!("the first message here"));
    assert_eq!(received[0]["team_ref"], json!(ELSEWHERE));
    assert_eq!(received[0]["sender"]["human_label"], json!("alice@example.com"));
    rig.ack(&bob, &b, &[&text(&sent, "message_id")]);
    rig.send(&bob, &b, &a, "and its answer").unwrap();
    assert_eq!(rig.receive(&alice, &a)[0]["body"], json!("and its answer"));
    let roster = rig.ok(&alice, "/v1/team/members", json!({"team_ref": ELSEWHERE}));
    assert_eq!(roster["team_name"], json!("ops"));
    assert_eq!(roster["members"].as_array().unwrap().len(), 2);

    // A wrong secret for the team's reference is refused as any wrong secret is, and creates nothing.
    let before = rig.state.clone();
    let wrong = secret_of(ELSEWHERE, "not-what-the-team-has");
    let wrong = rig
        .call(None, "/v1/team/join", json!({"join_secret": wrong}))
        .unwrap_err();
    let unknown = secret_of("no-such-team", "not-what-the-team-has");
    let unknown = rig
        .call(None, "/v1/team/join", json!({"join_secret": unknown}))
        .unwrap_err();
    assert_eq!(wrong, unknown);
    assert_eq!(wrong.code, Code::Unauthorized);
    assert_eq!(rig.state, before);

    // A restart: every entry, read back from its bytes, makes the same state, the team that was taken in
    // and everything its members did.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    // A snapshot: the state written and read back, a team with no creator in it.
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    assert_eq!(serde_json::from_slice::<State>(&snapshot).unwrap(), rig.state);
}

#[test]
fn a_team_is_taken_in_once_and_never_under_a_second_secret() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("ops", &secret).unwrap();
    let alice = rig.join(&secret, "alice");
    rig.register(&alice, "alpha");
    let (before, entries) = (rig.state.clone(), rig.log.len());

    // Again, under whatever name: nothing changes, no entry is made, and the answer names the team as it is.
    rig.advance(60);
    let again = rig.import("renamed", &secret).unwrap();
    assert_eq!(
        again,
        json!({"team_ref": ELSEWHERE, "team_name": "ops", "imported": false})
    );
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    // Another secret for the same reference is a conflict, told without a word of either secret.
    let other = secret_of(ELSEWHERE, "another-secret-for-the-reference");
    let conflict = rig.import("ops", &other).unwrap_err();
    assert_eq!(conflict.code, Code::Conflict);
    assert_eq!(reason(&conflict), "other_join_secret");
    assert_eq!(
        conflict.message,
        "a team of that reference is here under another join secret"
    );
    let told = format!("{conflict:?}");
    assert!(!told.contains("another-secret-for-the-reference"));
    assert!(!told.contains("kept-from-the-other-backend"));
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);
    // The team opens to the secret it was taken in with, and to no other.
    assert_eq!(
        rig.call(None, "/v1/team/join", json!({"join_secret": other}))
            .unwrap_err()
            .code,
        Code::Unauthorized
    );
    assert_eq!(
        rig.ok(&alice, "/v1/team/join", json!({"join_secret": secret}))["rejoined"],
        json!(true)
    );

    // A team that was created here is held to the same: its own secret changes nothing, another is refused.
    let (carol, created) = rig.create("carol");
    let entries = rig.log.len();
    let here = rig.import("renamed", &created).unwrap();
    assert_eq!(
        here,
        json!({"team_ref": carol.team_ref, "team_name": "ops", "imported": false})
    );
    let forged = secret_of(&carol.team_ref, "another-secret-for-the-reference");
    assert_eq!(rig.import("ops", &forged).unwrap_err().code, Code::Conflict);
    assert_eq!(rig.log.len(), entries);
}

#[test]
fn a_team_is_not_taken_in_without_a_join_secret_and_a_name_that_keeps_the_rule() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    // A text that is not a join secret is the refusal a join gives one, and holds nothing of the text.
    let malformed = rig
        .call(None, "/v1/team/join", json!({"join_secret": "nope"}))
        .unwrap_err();
    assert_eq!(malformed.code, Code::InvalidInput);
    for not_a_secret in [
        String::new(),
        "not-a-join-secret".to_owned(),
        ELSEWHERE.to_owned(),
        secret.replacen("brg1.", "brg2.", 1),
        secret_of(ELSEWHERE, "with a space"),
        format!("{secret}\n"),
    ] {
        let refused = rig.import("ops", &not_a_secret).unwrap_err();
        assert_eq!(refused, malformed, "{not_a_secret:?}");
    }
    // A name is held to the rule a created team's name is held to, in the same words.
    for name in [String::new(), "é".repeat(65)] {
        let created = rig
            .call(None, "/v1/team/create", json!({"team_name": name}))
            .unwrap_err();
        let refused = rig.import(&name, &secret).unwrap_err();
        assert_eq!(refused, created, "{name:?}");
        assert_eq!(refused.details["field"], "team_name");
    }
    // Nothing of all that changed the state or made an entry, and a name at the cap is taken.
    assert_eq!(rig.state, State::default());
    assert!(rig.log.is_empty());
    let at_cap = "é".repeat(64);
    assert_eq!(rig.import(&at_cap, &secret).unwrap()["team_name"], json!(at_cap));
}

#[test]
fn leaving_revokes_closes_and_a_rejoin_is_the_same_member() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let bob = rig.join(&secret, "bob");
    let b = rig.register(&bob, "beta");
    assert_eq!(rig.list(&alice, false).len(), 1);

    let left = rig.ok(&bob, "/v1/team/leave", json!({"team_ref": bob.team_ref}));
    assert_eq!(
        left,
        json!({"team_ref": bob.team_ref, "principal_ref": bob.principal_ref, "left": true})
    );
    // The member's sessions leave the roster with it, offline ones included.
    assert_eq!(rig.list(&alice, true).len(), 0);
    // Every verb that touches the team is refused the member who left, in the words a stranger gets.
    let refused = rig.err(&bob, "/v1/session/list", json!({"team_ref": bob.team_ref}));
    let stranger = rig.err(&bob, "/v1/session/list", json!({"team_ref": "no-such-team"}));
    assert_eq!(refused, stranger);
    assert_eq!(refused.code, Code::Unauthorized);
    assert_eq!(
        rig.err(&bob, "/v1/message/receive", json!({"session_id": b}))
            .code,
        Code::Unauthorized
    );
    assert_eq!(
        rig.err(&bob, "/v1/session/register", registration(&bob, "again"))
            .code,
        Code::Unauthorized
    );
    // Leaving again is the success it always is, and makes no record.
    let records = rig.log.len();
    rig.ok(&bob, "/v1/team/leave", json!({"team_ref": bob.team_ref}));
    rig.ok(&bob, "/v1/team/leave", json!({"team_ref": "no-such-team"}));
    assert_eq!(rig.log.len(), records);

    rig.advance(5);
    let rejoined = rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(rejoined["rejoined"], json!(true));
    assert_eq!(text(&rejoined, "principal_ref"), bob.principal_ref);
    assert!(rejoined.get("credential").is_none());
    // The label and the day it first joined are kept; the session it had stays closed until it resumes.
    let members = rig.ok(&alice, "/v1/team/members", json!({"team_ref": alice.team_ref}));
    let entry = members["members"]
        .as_array()
        .unwrap()
        .iter()
        .find(|m| m["principal_ref"] == json!(bob.principal_ref));
    assert_eq!(entry.unwrap()["human_label"], json!("bob"));
    assert_eq!(rig.list(&alice, false).len(), 0);
    let mut resume = registration(&bob, "beta");
    resume["resume"] = json!({"session_id": b});
    assert_eq!(
        rig.ok(&bob, "/v1/session/register", resume)["resumed"],
        json!(true)
    );
    assert_eq!(rig.list(&alice, false).len(), 1);
}

#[test]
fn a_roster_lists_active_members_with_what_their_sessions_show() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let bob = rig.join(&secret, "");
    rig.register(&alice, "alpha");
    let roster = rig.ok(&bob, "/v1/team/members", json!({"team_ref": bob.team_ref}));
    let members = roster["members"].as_array().unwrap();
    assert_eq!(members.len(), 2);
    let of = |principal: &str| {
        members
            .iter()
            .find(|m| m["principal_ref"] == json!(principal))
            .unwrap()
            .clone()
    };
    assert_eq!(of(&alice.principal_ref)["session_count"], json!(1));
    assert_eq!(of(&alice.principal_ref)["status"], json!("active"));
    assert_eq!(of(&bob.principal_ref)["session_count"], json!(0));
    assert_eq!(of(&bob.principal_ref)["last_seen_at"], Value::Null);
    assert!(of(&bob.principal_ref).get("human_label").is_none());
    // A caller with no membership is told what a stranger to a missing team is told.
    let (carol, _) = rig.create("carol");
    let foreign = rig.err(&carol, "/v1/team/members", json!({"team_ref": alice.team_ref}));
    let missing = rig.err(&carol, "/v1/team/members", json!({"team_ref": "0123"}));
    assert_eq!(foreign, missing);
}

#[test]
fn a_registration_fills_an_empty_label_and_never_replaces_one() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("");
    let bob = rig.join(&secret, "bob");
    let mut offer = registration(&alice, "alpha");
    offer["human_label"] = json!("alice@default");
    assert_eq!(
        rig.ok(&alice, "/v1/session/register", offer.clone())["human_label"],
        json!("alice@default")
    );
    offer["human_label"] = json!("someone else");
    assert_eq!(
        rig.ok(&alice, "/v1/session/register", offer)["human_label"],
        json!("alice@default")
    );
    let mut offer = registration(&bob, "beta");
    offer["human_label"] = json!("not bob");
    assert_eq!(
        rig.ok(&bob, "/v1/session/register", offer)["human_label"],
        json!("bob")
    );
}

#[test]
fn a_session_is_active_idle_or_offline_by_the_servers_clock() {
    let mut rig = Rig::new();
    let (alice, _) = rig.create("alice");
    let mut request = registration(&alice, "alpha");
    request["lease_seconds"] = json!(30);
    let registered = rig.ok(&alice, "/v1/session/register", request);
    let id = text(&registered, "session_id");
    assert_eq!(registered["state"], json!("active"));
    assert_eq!(registered["lease_seconds"], json!(30));
    assert_eq!(registered["resumed"], json!(false));
    assert_eq!(registered["is_self"], json!(false));

    let beat = rig.ok(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": id, "heartbeat": {"activity": "idle"}}),
    );
    assert_eq!(beat["state"], json!("idle"));

    // Expiry is authoritative: a lease that ran out is offline with no close.
    rig.advance(31);
    assert_eq!(rig.list(&alice, false).len(), 0);
    let listed = rig.list(&alice, true);
    assert_eq!(listed[0]["state"], json!("offline"));
    // A heartbeat on a session that expired and was not closed renews it.
    let beat = rig.ok(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": id, "heartbeat": {}}),
    );
    assert_eq!(beat["state"], json!("idle"));
    assert_eq!(rig.list(&alice, false).len(), 1);
}

#[test]
fn a_heartbeat_changes_what_it_names_and_keeps_the_rest() {
    let mut rig = Rig::new();
    let (alice, _) = rig.create("alice");
    let mut request = registration(&alice, "alpha");
    request["model"] = json!("claude-fable-5-2");
    request["context_used_tokens"] = json!(100);
    request["session_description"] = json!("first");
    let id = text(&rig.ok(&alice, "/v1/session/register", request), "session_id");
    let changes = json!({"session_name": "renamed", "session_description": "", "inbound": "hold", "sync_peer": "syncthing:ABC"});
    rig.ok(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": id, "heartbeat": changes}),
    );
    let listed = rig.ok(
        &alice,
        "/v1/session/list",
        json!({"team_ref": alice.team_ref, "session_id": id}),
    );
    let session = &listed["sessions"][0];
    assert_eq!(session["session_name"], json!("renamed"));
    assert_eq!(session["session_description"], json!(""));
    assert_eq!(session["inbound"], json!("hold"));
    assert_eq!(session["sync_peer"], json!("syncthing:ABC"));
    assert_eq!(session["model"], json!("claude-fable-5-2"));
    assert_eq!(session["context_used_tokens"], json!(100));
    assert_eq!(session["is_self"], json!(true));
    assert!(session.get("brigade_version").is_none());
}

#[test]
fn close_is_idempotent_and_a_closed_session_neither_beats_nor_sends() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let closed = rig.ok(&alice, "/v1/session/close", json!({"session_id": a}));
    assert_eq!(closed, json!({"session_id": a, "state": "offline"}));
    let records = rig.log.len();
    rig.ok(&alice, "/v1/session/close", json!({"session_id": a}));
    assert_eq!(rig.log.len(), records);

    let beat = rig.err(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": a, "heartbeat": {}}),
    );
    assert_eq!((beat.code, reason(&beat)), (Code::Conflict, "session_closed"));
    let sent = rig.send(&alice, &a, &b, "from a closed session").unwrap_err();
    assert_eq!((sent.code, reason(&sent)), (Code::Conflict, "session_closed"));
    // Sending to a closed recipient is accepted: the message waits for a resume.
    rig.send(&bob, &b, &a, "for later").unwrap();
    let mut resume = registration(&alice, "alpha");
    resume["resume"] = json!({"session_id": a});
    let resumed = rig.ok(&alice, "/v1/session/register", resume);
    assert_eq!(
        (resumed["resumed"].clone(), text(&resumed, "session_id")),
        (json!(true), a.clone())
    );
    assert_eq!(rig.receive(&alice, &a).len(), 1);
}

#[test]
fn a_resume_needs_an_owned_session_that_is_not_live() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let resume_of = |member: &Member, id: &str| {
        let mut request = registration(member, "again");
        request["resume"] = json!({"session_id": id});
        request
    };
    let live = rig.err(&alice, "/v1/session/register", resume_of(&alice, &a));
    assert_eq!((live.code, reason(&live)), (Code::Conflict, "session_live"));
    // Another member's session and one that never existed are the same answer.
    let foreign = rig.err(&alice, "/v1/session/register", resume_of(&alice, &b));
    let unknown = rig.err(&alice, "/v1/session/register", resume_of(&alice, "never"));
    assert_eq!(foreign, unknown);
    assert_eq!(foreign.code, Code::NotFound);
    assert!(foreign.details.is_empty());
    // An expired lease can be resumed with no close.
    rig.advance(91);
    assert_eq!(
        rig.ok(&bob, "/v1/session/register", resume_of(&bob, &b))["resumed"],
        json!(true)
    );
}

#[test]
fn a_message_is_stamped_by_the_server_and_received_until_acknowledged() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let sent = rig.send(&alice, &a, &b, "The migration has landed.").unwrap();
    assert_eq!(sent["status"], json!("accepted"));
    assert_eq!(sent["duplicate"], json!(false));
    assert_eq!(sent["hop_count"], json!(0));
    let id = text(&sent, "message_id");

    let received = rig.receive(&bob, &b);
    assert_eq!(received.len(), 1);
    let message = &received[0];
    assert_eq!(message["message_id"], json!(id));
    assert_eq!(message["kind"], json!("text"));
    assert_eq!(message["team_ref"], json!(alice.team_ref));
    assert_eq!(message["sender"]["principal_ref"], json!(alice.principal_ref));
    assert_eq!(message["sender"]["human_label"], json!("alice@example.com"));
    assert_eq!(message["sender"]["session_id"], json!(a));
    assert_eq!(message["sender"]["session_name"], json!("alpha"));
    assert_eq!(message["recipient_session_id"], json!(b));
    assert_eq!(message["body"], json!("The migration has landed."));
    assert_eq!(message["created_at"], sent["created_at"]);
    assert_eq!(message["delivery_state"], json!("accepted"));
    assert_eq!(message["reply_to"], Value::Null);

    // Receiving never acknowledges.
    assert_eq!(rig.receive(&bob, &b).len(), 1);
    let acked = rig.ack(&bob, &b, &[&id, "no-such-message"]);
    assert_eq!(acked, json!({"acked": [id], "unknown": ["no-such-message"]}));
    assert_eq!(rig.receive(&bob, &b).len(), 0);
    // An id acknowledged before is acknowledged again, with no record; the sender cannot acknowledge it.
    let records = rig.log.len();
    assert_eq!(rig.ack(&bob, &b, &[&id]), json!({"acked": [id], "unknown": []}));
    assert_eq!(rig.ack(&alice, &a, &[&id]), json!({"acked": [], "unknown": [id]}));
    assert_eq!(rig.log.len(), records);
}

#[test]
fn nobody_sends_receives_or_closes_for_a_session_it_does_not_own() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let unknown = rig.err(&alice, "/v1/message/receive", json!({"session_id": "never"}));
    for (path, body) in [
        ("/v1/message/receive", json!({"session_id": b})),
        ("/v1/message/ack", json!({"session_id": b, "message_ids": ["m"]})),
        ("/v1/session/close", json!({"session_id": b})),
        ("/v1/session/heartbeat", json!({"session_id": b, "heartbeat": {}})),
        (
            "/v1/message/send",
            json!({"sender_session_id": b, "recipient_session_id": a, "body": "as bob"}),
        ),
    ] {
        assert_eq!(rig.err(&alice, path, body), unknown, "{path}");
    }
    assert_eq!(unknown.code, Code::NotFound);
    drop(bob);
}

#[test]
fn one_team_never_sees_or_reaches_another() {
    let mut rig = Rig::new();
    let (alice, a, _bob, _b) = pair(&mut rig);
    let (mallory, _) = rig.create("mallory");
    let m = rig.register(&mallory, "other");
    // The other team's session is not listed, and messaging it is the answer for an id that never existed.
    assert!(
        rig.list(&alice, true)
            .iter()
            .all(|session| session["session_id"] != json!(m))
    );
    let foreign = rig.send(&alice, &a, &m, "across teams").unwrap_err();
    let unknown = rig.send(&alice, &a, "never", "to nobody").unwrap_err();
    assert_eq!(foreign, unknown);
    assert_eq!(foreign.code, Code::NotFound);
    // A session of the caller's own cannot be registered into a team it is not a member of.
    let mut wrong_team = registration(&mallory, "x");
    wrong_team["team_ref"] = json!(alice.team_ref);
    assert_eq!(
        rig.err(&mallory, "/v1/session/register", wrong_team).code,
        Code::Unauthorized
    );
}

#[test]
fn an_idempotency_key_makes_one_message_and_refuses_another_payload() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let send = |body: &str, key: &str| json!({"sender_session_id": a, "recipient_session_id": b, "body": body, "idempotency_key": key});
    let first = rig.ok(&alice, "/v1/message/send", send("once", "k1"));
    rig.advance(1);
    let second = rig.ok(&alice, "/v1/message/send", send("once", "k1"));
    assert_eq!(second["duplicate"], json!(true));
    assert_eq!(second["message_id"], first["message_id"]);
    assert_eq!(second["created_at"], first["created_at"]);
    assert_eq!(rig.receive(&bob, &b).len(), 1);
    let other = rig.err(&alice, "/v1/message/send", send("twice", "k1"));
    assert_eq!(
        (other.code, reason(&other)),
        (Code::Conflict, "idempotency_key_reused")
    );
    // The key answers with the first response after the message is acknowledged, too.
    let id = text(&first, "message_id");
    rig.ack(&bob, &b, &[&id]);
    assert_eq!(
        rig.ok(&alice, "/v1/message/send", send("once", "k1"))["message_id"],
        json!(id)
    );
    // A key belongs to its sender session: the same key from another session is another message.
    let a2 = rig.register(&alice, "alpha-2");
    let from_other = rig.ok(
        &alice,
        "/v1/message/send",
        json!({"sender_session_id": a2, "recipient_session_id": b, "body": "once", "idempotency_key": "k1"}),
    );
    assert_eq!(from_other["duplicate"], json!(false));
}

#[test]
fn a_reply_counts_one_more_hop_labelled_or_not() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let first = text(&rig.send(&alice, &a, &b, "ping").unwrap(), "message_id");
    // A labelled answer.
    let reply = rig.ok(
        &bob,
        "/v1/message/send",
        json!({"sender_session_id": b, "recipient_session_id": a, "body": "pong", "reply_to": first}),
    );
    assert_eq!(reply["hop_count"], json!(1));
    // An unlabelled answer within the window is still an answer.
    assert_eq!(
        rig.send(&alice, &a, &b, "ping again").unwrap()["hop_count"],
        json!(2)
    );
    // `reply_to` must name a message the sender received: its own sent message is not one.
    let not_received = rig.err(
        &alice,
        "/v1/message/send",
        json!({"sender_session_id": a, "recipient_session_id": b, "body": "x", "reply_to": first}),
    );
    assert_eq!(not_received.code, Code::NotFound);
    // Outside the window an unlabelled message starts again at zero.
    rig.advance(601);
    assert_eq!(
        rig.send(&bob, &b, &a, "much later").unwrap()["hop_count"],
        json!(0)
    );
}

#[test]
fn a_reply_chain_ends_at_the_hop_cap() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let mut last = text(&rig.send(&alice, &a, &b, "0").unwrap(), "message_id");
    let mut turn = 0;
    loop {
        // Acknowledge as the chain goes, and pace it, so neither a cap nor a budget is what ends it.
        let (member, from, to, other, inbox) = if turn % 2 == 0 {
            (&bob, &b, &a, &bob, &b)
        } else {
            (&alice, &a, &b, &alice, &a)
        };
        rig.ack(other, inbox, &[&last]);
        rig.advance(4);
        let body =
            json!({"sender_session_id": from, "recipient_session_id": to, "body": "next", "reply_to": last});
        match rig.call(Some(&member.credential), "/v1/message/send", body) {
            Ok(sent) => {
                turn += 1;
                assert_eq!(sent["hop_count"], json!(turn));
                last = text(&sent, "message_id");
            }
            Err(error) => {
                assert_eq!(error.code, Code::LoopDetected);
                assert_eq!(turn, 32);
                break;
            }
        }
    }
}

#[test]
fn a_session_and_a_principal_each_have_a_send_budget() {
    let mut rig = Rig::new();
    let (alice, a, bob, _b) = pair(&mut rig);
    // Twenty recipients, so no inbox cap is in the way of the session's minute.
    let recipients: Vec<String> = (0..20).map(|n| rig.register(&bob, &format!("r{n}"))).collect();
    for recipient in &recipients {
        rig.send(&alice, &a, recipient, "one of twenty").unwrap();
    }
    let limited = rig
        .send(&alice, &a, &recipients[0], "the twenty-first")
        .unwrap_err();
    assert_eq!(
        (limited.code, reason(&limited)),
        (Code::RateLimited, "send_per_minute")
    );
    assert_eq!(limited.retry_after_ms, Some(60_000));
    assert_eq!(
        limited.message,
        "the send rate limit for this session is exhausted"
    );
    // The principal's minute is summed over its sessions: two more sessions spend forty more.
    for name in ["a2", "a3"] {
        let session = rig.register(&alice, name);
        for recipient in &recipients {
            rig.send(&alice, &session, recipient, "more").unwrap();
        }
    }
    let fourth = rig.register(&alice, "a4");
    let limited = rig
        .send(&alice, &fourth, &recipients[0], "the sixty-first")
        .unwrap_err();
    assert_eq!(reason(&limited), "principal_per_minute");
    assert_eq!(
        limited.message,
        "the send rate limit for this principal is exhausted"
    );
    // A minute later the window has moved on.
    rig.advance(61);
    rig.send(&alice, &fourth, &recipients[0], "after the minute")
        .unwrap();
}

/// One more team for a principal the server knows.
fn another_team(rig: &mut Rig, member: &Member) -> Result<Value, Error> {
    rig.call(
        Some(&member.credential),
        "/v1/team/create",
        json!({"team_name": "more", "human_label": "alice"}),
    )
}

#[test]
fn a_principal_creates_a_bounded_number_of_teams_in_an_hour() {
    // The numbers are the reference backend's (ADR 0004): a team meets the same on either.
    assert_eq!(
        (
            limits::TEAMS_CREATED_PER_HOUR,
            limits::SESSIONS_OPENED_PER_HOUR,
            limits::PRINCIPAL_RATE_WINDOW_SECONDS,
            limits::FAILED_JOINS_PER_WINDOW,
            limits::FAILED_JOIN_WINDOW_SECONDS,
            limits::FAILED_JOIN_RETRY_FLOOR_MS,
        ),
        (5, 120, 3600, 5, 900, 60_000)
    );
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    for _ in 1..limits::TEAMS_CREATED_PER_HOUR {
        rig.advance(60);
        another_team(&mut rig, &alice).unwrap();
    }
    rig.advance(60);
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let limited = another_team(&mut rig, &alice).unwrap_err();
    assert_eq!(
        (limited.code, reason(&limited)),
        (Code::RateLimited, "team_create")
    );
    // The bound frees when the first team's hour is over, and that team was made five minutes ago.
    assert_eq!(limited.retry_after_ms, Some((3600 - 300) * 1000));
    assert_eq!(
        limited.message,
        "this principal has created as many teams as one may in an hour"
    );
    // The refusal made nothing: no team, no record.
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // A caller with no credential the server knows is a new principal, which has created none.
    rig.create("bob");
    // A team counts against the principal that created it, whoever owns it afterwards.
    let carol = rig.join(&secret, "carol");
    rig.ok(
        &alice,
        "/v1/team/transfer",
        json!({"team_ref": alice.team_ref, "principal_ref": carol.principal_ref}),
    );
    assert_eq!(
        reason(&another_team(&mut rig, &alice).unwrap_err()),
        "team_create"
    );
    // Once the first team's hour is over there is room for one more, and for one only.
    rig.advance(3600 - 300 + 1);
    another_team(&mut rig, &alice).unwrap();
    assert_eq!(
        reason(&another_team(&mut rig, &alice).unwrap_err()),
        "team_create"
    );
    // A restart counts what the log holds, and a snapshot carries the count.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    rig.state = serde_json::from_slice(&snapshot).unwrap();
    assert_eq!(
        reason(&another_team(&mut rig, &alice).unwrap_err()),
        "team_create"
    );
}

#[test]
fn a_principal_opens_a_bounded_number_of_sessions_in_an_hour_and_a_resume_is_not_one() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let first = rig.register(&alice, "s0");
    for n in 1..limits::SESSIONS_OPENED_PER_HOUR {
        rig.register(&alice, &format!("s{n}"));
    }
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let limited = rig.err(
        &alice,
        "/v1/session/register",
        registration(&alice, "one too many"),
    );
    assert_eq!(
        (limited.code, reason(&limited)),
        (Code::RateLimited, "register_session")
    );
    assert_eq!(limited.retry_after_ms, Some(3_600_000));
    assert_eq!(
        limited.message,
        "this principal has opened as many sessions as one may in an hour"
    );
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // The sessions of a principal are counted over all its teams.
    let elsewhere = another_team(&mut rig, &alice).unwrap();
    let mut other = registration(&alice, "in the other team");
    other["team_ref"] = elsewhere["team_ref"].clone();
    assert_eq!(
        reason(&rig.err(&alice, "/v1/session/register", other)),
        "register_session"
    );
    // Another member of the team has a count of its own.
    let bob = rig.join(&secret, "bob");
    rig.register(&bob, "b0");
    // A resume opens no session: one that comes back is not refused, and is not counted.
    rig.ok(&alice, "/v1/session/close", json!({"session_id": first}));
    let mut resume = registration(&alice, "s0");
    resume["resume"] = json!({"session_id": first});
    assert_eq!(
        rig.ok(&alice, "/v1/session/register", resume)["resumed"],
        json!(true)
    );
    let opened = |rig: &Rig| {
        rig.state.principals[&alice.principal_ref]
            .sessions_opened_at
            .len()
    };
    assert_eq!(opened(&rig), limits::SESSIONS_OPENED_PER_HOUR);
    // An hour after the sessions were opened there is room again, and their times are let go of.
    rig.advance(3601);
    rig.register(&alice, "after the hour");
    assert_eq!(opened(&rig), 1);
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
}

#[test]
fn a_principals_failed_joins_are_bounded_and_a_join_that_succeeds_clears_them() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    // Bob is a principal the server knows: he has a team of his own.
    let (bob, _) = rig.create("bob");
    let wrong = secret_of(&alice.team_ref, "not-the-secret-of-the-team");
    let unknown = secret_of("no-such-team", "whatever-it-is");
    let fail = |rig: &mut Rig, secret: &str| rig.err(&bob, "/v1/team/join", json!({"join_secret": secret}));
    let rejected = wire(&Error::secret_rejected());
    // A wrong secret and an unknown team each count, over all teams.
    for n in 0..limits::FAILED_JOINS_PER_WINDOW {
        rig.advance(60);
        let secret = if n % 2 == 0 { &wrong } else { &unknown };
        assert_eq!(wire(&fail(&mut rig, secret)), rejected);
    }
    // The next join is refused unheard, the right secret included, and nothing is made.
    rig.advance(60);
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let limited = fail(&mut rig, &secret);
    assert_eq!(
        (limited.code, reason(&limited)),
        (Code::RateLimited, "join_attempts")
    );
    // The first failure was five minutes ago, and leaves the window ten minutes from now.
    assert_eq!(limited.retry_after_ms, Some(600_000));
    assert_eq!(
        limited.message,
        "this principal has failed too many joins, and its next is not heard yet"
    );
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // A request that cannot be read is told so, held or not, as the reference backend tells it first.
    let unread = rig.err(&bob, "/v1/team/join", json!({"join_secret": 7}));
    assert_eq!(unread.code, Code::InvalidInput);
    // A refusal for the bound is not a failed join: it does not push the wait out.
    rig.advance(60);
    assert_eq!(fail(&mut rig, &secret).retry_after_ms, Some(540_000));
    // The wait is never told shorter than a minute.
    rig.advance(539);
    assert_eq!(fail(&mut rig, &secret).retry_after_ms, Some(60_000));
    // A request that cannot be read is no attempt on a secret.
    rig.advance(2);
    let unread = rig.err(&bob, "/v1/team/join", json!({"join_secret": 7}));
    assert_eq!(unread.code, Code::InvalidInput);
    // With the first failure out of the window the join is heard, and a join that succeeds clears the rest.
    let joined = rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(joined["rejoined"], json!(false));
    for _ in 1..limits::FAILED_JOINS_PER_WINDOW {
        assert_eq!(wire(&fail(&mut rig, &wrong)), rejected);
    }
    rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    // A caller with no credential the server knows has no principal to count against.
    for _ in 0..=limits::FAILED_JOINS_PER_WINDOW {
        let stranger = rig
            .call(None, "/v1/team/join", json!({"join_secret": wrong}))
            .unwrap_err();
        assert_eq!(wire(&stranger), rejected);
    }
    // One principal's failures hold nobody else.
    for _ in 0..limits::FAILED_JOINS_PER_WINDOW {
        fail(&mut rig, &wrong);
    }
    assert_eq!(reason(&fail(&mut rig, &secret)), "join_attempts");
    rig.ok(&alice, "/v1/team/join", json!({"join_secret": secret}));
    // The count is the process's own: a server that starts again has none.
    rig.failures = JoinFailures::new();
    rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));

    // A gateway's join is made inside the server and may not own. It tries again each minute with the file
    // its operator keeps, so its failures are not counted, and it is let in the minute the file is right.
    rig.may_own = false;
    for _ in 0..=limits::FAILED_JOINS_PER_WINDOW {
        assert_eq!(wire(&fail(&mut rig, &wrong)), rejected);
    }
    rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    rig.may_own = true;

    // A principal the team banned is told what a wrong secret is told, and its joins are counted as one.
    let carol = rig.join(&secret, "carol");
    revoke(&mut rig, &alice, &carol.principal_ref, Some(true)).unwrap();
    let right = json!({"join_secret": secret});
    for _ in 0..limits::FAILED_JOINS_PER_WINDOW {
        assert_eq!(wire(&rig.err(&carol, "/v1/team/join", right.clone())), rejected);
    }
    assert_eq!(reason(&rig.err(&carol, "/v1/team/join", right)), "join_attempts");

    // No record holds these times, so the clock they were read from may be set back. A failure that then
    // lies ahead of the clock is let go of, and does not hold its principal for as long as the clock went.
    for _ in 0..limits::FAILED_JOINS_PER_WINDOW {
        fail(&mut rig, &wrong);
    }
    assert_eq!(reason(&fail(&mut rig, &secret)), "join_attempts");
    rig.now = rig.now.minus_seconds(3600);
    rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
}

#[test]
fn the_pair_cap_is_told_before_the_inbox_cap() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let target = rig.register(&alice, "target");
    // Four senders fill the inbox to sixty, fifteen each.
    let mut senders = Vec::new();
    for n in 0..4 {
        let member = rig.join(&secret, &format!("m{n}"));
        let session = rig.register(&member, &format!("s{n}"));
        for _ in 0..15 {
            rig.send(&member, &session, &target, "fill").unwrap();
        }
        senders.push((member, session));
    }
    // A sender at its own quota is told that, although the inbox is full as well.
    let (member, session) = &senders[0];
    let own = rig.send(member, session, &target, "one more").unwrap_err();
    assert_eq!(
        (own.code, reason(&own)),
        (Code::RateLimited, "sender_quota_for_recipient")
    );
    assert_eq!(own.retry_after_ms, Some(5000));
    // A sender with room of its own is told the inbox is full.
    let fresh = rig.join(&secret, "fresh");
    let fresh_session = rig.register(&fresh, "fresh");
    let full = rig.send(&fresh, &fresh_session, &target, "no room").unwrap_err();
    assert_eq!(reason(&full), "recipient_inbox_full");
    // Acknowledging makes room.
    let pending = rig.receive(&alice, &target);
    let first = text(&pending[0], "message_id");
    rig.ack(&alice, &target, &[&first]);
    rig.send(&fresh, &fresh_session, &target, "room now").unwrap();
}

#[test]
fn a_cursor_returns_only_what_was_accepted_after_it() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    rig.send(&alice, &a, &b, "one").unwrap();
    rig.send(&alice, &a, &b, "two").unwrap();
    let first = rig.ok(&bob, "/v1/message/receive", json!({"session_id": b, "limit": 1}));
    assert_eq!(first["messages"].as_array().unwrap().len(), 1);
    assert_eq!(first["messages"][0]["body"], json!("one"));
    let cursor = text(&first, "cursor");
    assert!(cursor.len() <= 256);

    // Nothing was accepted after the cursor, although two messages are still unacknowledged.
    let after = rig.ok(
        &bob,
        "/v1/message/receive",
        json!({"session_id": b, "after": cursor}),
    );
    assert_eq!(after["messages"], json!([]));
    rig.send(&alice, &a, &b, "three").unwrap();
    let after = rig.ok(
        &bob,
        "/v1/message/receive",
        json!({"session_id": b, "after": cursor}),
    );
    assert_eq!(after["messages"].as_array().unwrap().len(), 1);
    assert_eq!(after["messages"][0]["body"], json!("three"));
    // Without a cursor, or with one this session never got, the answer is every unacknowledged message.
    for strange in [
        json!(null),
        json!("nonsense"),
        json!("c1.000000000000.1"),
        json!(format!("{cursor}9999")),
    ] {
        let all = rig.ok(
            &bob,
            "/v1/message/receive",
            json!({"session_id": b, "after": strange}),
        );
        assert_eq!(all["messages"].as_array().unwrap().len(), 3, "{strange}");
    }
    // Another session's cursor is not recognised either.
    let other = rig.register(&bob, "other");
    let foreign = rig.ok(
        &bob,
        "/v1/message/receive",
        json!({"session_id": other, "after": cursor}),
    );
    assert_eq!(foreign["messages"], json!([]));
    assert_ne!(text(&foreign, "cursor"), cursor);
}

#[test]
fn a_revoked_credential_is_unknown_and_its_membership_stays() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let a = rig.register(&alice, "alpha");
    assert_eq!(
        rig.ok(&alice, "/v1/credential/revoke", json!({})),
        json!({"revoked": true})
    );
    for (path, body) in [
        ("/v1/session/list", json!({"team_ref": alice.team_ref})),
        ("/v1/message/receive", json!({"session_id": a})),
        ("/v1/credential/revoke", json!({})),
        ("/v1/team/leave", json!({"team_ref": alice.team_ref})),
    ] {
        assert_eq!(rig.err(&alice, path, body).code, Code::Unauthenticated, "{path}");
    }
    assert_eq!(
        rig.call(None, "/v1/session/list", json!({"team_ref": alice.team_ref}))
            .unwrap_err()
            .code,
        Code::Unauthenticated
    );
    // The membership is as it was: the roster still shows it. A join with the old credential is a new principal.
    let bob = rig.join(&secret, "bob");
    assert_eq!(
        rig.ok(&bob, "/v1/team/members", json!({"team_ref": bob.team_ref}))["members"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    let again = rig.ok(&alice, "/v1/team/join", json!({"join_secret": secret}));
    assert_ne!(text(&again, "principal_ref"), alice.principal_ref);
    assert_eq!(again["rejoined"], json!(false));
    assert!(again.get("credential").is_some());
}

#[test]
fn retention_lets_go_of_old_messages_and_long_closed_sessions() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let old = text(
        &rig.send(&alice, &a, &b, "acknowledged soon").unwrap(),
        "message_id",
    );
    rig.send(&alice, &a, &b, "never acknowledged").unwrap();
    rig.ack(&bob, &b, &[&old]);
    let day = 86_400;
    let beat = |rig: &mut Rig| {
        // Something has to be written for the sweep to run: it is part of applying a record.
        rig.ok(
            &alice,
            "/v1/session/heartbeat",
            json!({"session_id": a, "heartbeat": {}}),
        );
        rig.ok(
            &bob,
            "/v1/session/heartbeat",
            json!({"session_id": b, "heartbeat": {}}),
        );
    };
    // Six days on, the unacknowledged message is kept and the acknowledged one is gone.
    for _ in 0..6 {
        rig.advance(day);
        beat(&mut rig);
    }
    assert_eq!(rig.receive(&bob, &b).len(), 1);
    assert_eq!(rig.ack(&bob, &b, &[&old]), json!({"acked": [], "unknown": [old]}));
    // Past a week it is gone too.
    rig.advance(day + 3600);
    beat(&mut rig);
    assert_eq!(rig.receive(&bob, &b).len(), 0);
    // A session closed for more than a week is forgotten with its inbox; one that keeps its lease is not.
    rig.ok(&bob, "/v1/session/close", json!({"session_id": b}));
    for _ in 0..8 {
        rig.advance(day);
        rig.ok(
            &alice,
            "/v1/session/heartbeat",
            json!({"session_id": a, "heartbeat": {}}),
        );
    }
    assert_eq!(
        rig.err(&bob, "/v1/message/receive", json!({"session_id": b}))
            .code,
        Code::NotFound
    );
    assert_eq!(rig.list(&bob, true).len(), 1);
}

#[test]
fn the_same_records_make_the_same_state() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    for n in 0..5 {
        rig.advance(7);
        let sent = rig.send(&alice, &a, &b, &format!("message {n}")).unwrap();
        if n % 2 == 0 {
            rig.ack(&bob, &b, &[&text(&sent, "message_id")]);
        }
    }
    rig.ok(
        &bob,
        "/v1/session/heartbeat",
        json!({"session_id": b, "heartbeat": {"activity": "idle"}}),
    );
    rig.advance(120);
    rig.ok(&alice, "/v1/session/close", json!({"session_id": a}));
    rig.ok(&bob, "/v1/team/leave", json!({"team_ref": bob.team_ref}));
    rig.ok(&alice, "/v1/credential/revoke", json!({}));

    // A restart: every entry, read back from its bytes, applied to an empty state.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    // An entry handed over twice, and one that was skipped, are refused and change nothing.
    let last = rig.log.last().unwrap();
    assert!(replayed.replay(last).unwrap_err().contains("skipped or repeated"));
    let mut gapped = State::default();
    gapped.replay(&rig.log[0]).unwrap();
    assert!(gapped.replay(&rig.log[2]).is_err());
    assert_eq!(gapped.entries, 1);
    // A snapshot: the state written and read back.
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    assert_eq!(serde_json::from_slice::<State>(&snapshot).unwrap(), rig.state);
}

#[test]
fn a_refused_request_changes_nothing_and_makes_no_record() {
    let mut rig = Rig::new();
    let (alice, a, _bob, b) = pair(&mut rig);
    let (before, records) = (rig.state.clone(), rig.log.len());
    assert!(rig.send(&alice, &b, &a, "not mine").is_err());
    assert!(
        rig.call(None, "/v1/session/close", json!({"session_id": a}))
            .is_err()
    );
    assert!(
        rig.call(
            Some("not-a-credential"),
            "/v1/team/leave",
            json!({"team_ref": alice.team_ref})
        )
        .is_err()
    );
    assert_eq!(rig.receive(&alice, &a).len(), 0);
    assert_eq!(rig.list(&alice, true).len(), 2);
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), records);
}

#[test]
fn no_record_is_longer_than_the_log_is_asked_to_take() {
    use brigade_core::record::LONGEST_ENCODED_BYTES;
    // The character JSON writes longest: a control character is six bytes on the wire for one in memory.
    let worst = |count: usize| "\u{1}".repeat(count);
    let mut rig = Rig::new();
    let created = rig
        .call(
            None,
            "/v1/team/create",
            json!({"team_name": worst(64), "human_label": worst(128)}),
        )
        .unwrap();
    let member = Member {
        credential: text(&created, "credential"),
        principal_ref: text(&created, "principal_ref"),
        team_ref: text(&created, "team_ref"),
    };
    let registration = json!({
        "team_ref": member.team_ref, "harness": worst(256), "harness_version": worst(256),
        "session_name": worst(64), "session_description": worst(256), "activity": "busy", "inbound": "accept",
        "workspace_label": worst(128), "human_label": worst(128), "model": worst(128),
        "context_used_tokens": 9_007_199_254_740_991_i64, "brigade_version": worst(64), "sync_peer": worst(256),
    });
    let a = text(
        &rig.ok(&member, "/v1/session/register", registration.clone()),
        "session_id",
    );
    let b = text(
        &rig.ok(&member, "/v1/session/register", registration),
        "session_id",
    );
    let first = rig.send(&member, &a, &b, "to answer").unwrap();
    rig.ok(
        &member,
        "/v1/message/send",
        json!({
            "sender_session_id": b, "recipient_session_id": a, "body": worst(16_384), "summary": worst(200),
            "reply_to": text(&first, "message_id"), "idempotency_key": worst(128),
        }),
    );
    let heartbeat = json!({
        "session_name": worst(64), "session_description": worst(256), "model": worst(128),
        "brigade_version": worst(64), "sync_peer": worst(256), "lease_seconds": 600,
    });
    rig.ok(
        &member,
        "/v1/session/heartbeat",
        json!({"session_id": a, "heartbeat": heartbeat}),
    );

    let longest = rig.log.iter().map(|entry| entry.encode().len()).max().unwrap();
    assert!(
        longest > 98_304,
        "the test did not build the long record: {longest}"
    );
    assert!(longest <= LONGEST_ENCODED_BYTES, "{longest} bytes");
    // A harness name longer than any harness has is refused, so a record cannot be as long as a request.
    let mut long = registration_of(&member);
    long["harness_version"] = json!("v".repeat(257));
    let refused = rig.err(&member, "/v1/session/register", long);
    assert_eq!(refused.details["field"], "harness_version");
}

fn registration_of(member: &Member) -> Value {
    registration(member, "plain")
}

#[test]
fn a_request_is_checked_in_the_stand_ins_order() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    let (stranger, _) = rig.create("stranger");

    // What is not one JSON object is told before the credential is looked at; the credential before the shape.
    let error = rig.raw(None, "/v1/session/heartbeat", b"[").unwrap_err();
    assert_eq!(
        (error.code, reason(&error)),
        (Code::InvalidInput, "malformed_json")
    );
    let error = rig.call(None, "/v1/session/heartbeat", json!({})).unwrap_err();
    assert_eq!(error.code, Code::Unauthenticated);
    let error = rig
        .call(Some("not-a-credential"), "/v1/message/send", json!({"sender": 1}))
        .unwrap_err();
    assert_eq!(error.code, Code::Unauthenticated);
    assert_eq!(
        rig.raw(None, "/v1/nowhere", b"[").unwrap_err().code,
        Code::NotFound
    );

    // The membership of the team a request names is told before the request's shape.
    let wrong = json!({"team_ref": alice.team_ref, "activity": "asleep"});
    assert_eq!(
        rig.err(&stranger, "/v1/session/register", wrong.clone()).code,
        Code::Unauthorized
    );
    let error = rig.err(&alice, "/v1/session/register", wrong);
    assert_eq!(
        (error.code, error.details["field"].as_str()),
        (Code::InvalidInput, "harness")
    );
    let list = json!({"team_ref": alice.team_ref, "include_offline": "yes"});
    assert_eq!(
        rig.err(&stranger, "/v1/session/list", list.clone()).code,
        Code::Unauthorized
    );
    assert_eq!(rig.err(&alice, "/v1/session/list", list).code, Code::InvalidInput);

    // A server that asks for a creation token refuses the creation before it reads the request.
    rig.may_create = false;
    let error = rig.call(None, "/v1/team/create", json!({})).unwrap_err();
    assert_eq!(error.code, Code::Unauthorized);
    assert_eq!(
        error.message,
        "this server does not let this caller create a team"
    );
    // The token gates creating a team and nothing else.
    assert_eq!(rig.list(&alice, true).len(), 2);
    rig.may_create = true;

    // A caller that is a member of no team is told so for every session, its own and anyone's and none.
    rig.ok(&bob, "/v1/team/leave", json!({"team_ref": bob.team_ref}));
    for id in [b.as_str(), a.as_str(), "never"] {
        let error = rig.err(&bob, "/v1/session/close", json!({"session_id": id}));
        assert_eq!(error.code, Code::Unauthorized, "{id}");
        assert!(error.details.is_empty());
    }
    // A member of some team is told the uniform not_found for any id that is not its own.
    assert_eq!(
        rig.err(&alice, "/v1/session/close", json!({"session_id": b}))
            .code,
        Code::NotFound
    );
    assert_eq!(
        rig.err(&stranger, "/v1/session/close", json!({"session_id": a}))
            .code,
        Code::NotFound
    );
}

#[test]
fn a_request_is_judged_as_if_retention_had_already_swept() {
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    rig.ok(&bob, "/v1/session/close", json!({"session_id": b}));
    // Just over a week later nothing has been written, so nothing has swept; the closed session is gone
    // all the same, to a send, a list, a roster, a resume and its owner.
    rig.advance(7 * 86_400 + 1);
    let mut again = registration(&alice, "alpha");
    again["resume"] = json!({"session_id": a});
    rig.ok(&alice, "/v1/session/register", again);
    let records = rig.log.len();
    assert_eq!(
        rig.send(&alice, &a, &b, "to a session that is gone")
            .unwrap_err()
            .code,
        Code::NotFound
    );
    assert_eq!(rig.log.len(), records);
    assert!(
        rig.list(&alice, true)
            .iter()
            .all(|session| session["session_id"] != json!(b))
    );
    let roster = rig.ok(&alice, "/v1/team/members", json!({"team_ref": alice.team_ref}));
    let of_bob = roster["members"]
        .as_array()
        .unwrap()
        .iter()
        .find(|m| m["principal_ref"] == json!(bob.principal_ref));
    assert_eq!(of_bob.unwrap()["last_seen_at"], Value::Null);
    assert_eq!(
        rig.err(&bob, "/v1/message/receive", json!({"session_id": b}))
            .code,
        Code::NotFound
    );
    let mut resume = registration(&bob, "beta");
    resume["resume"] = json!({"session_id": b});
    assert_eq!(rig.err(&bob, "/v1/session/register", resume).code, Code::NotFound);
}

#[test]
fn a_record_is_never_stamped_before_the_one_before_it_is_known() {
    let mut rig = Rig::new();
    let (alice, a, _bob, _b) = pair(&mut rig);
    rig.advance(30);
    rig.ok(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": a, "heartbeat": {}}),
    );
    // The state remembers the latest time a record carried, so a server that starts from a snapshot knows
    // it without the log.
    assert_eq!(rig.state.latest_at, rig.now);
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    assert_eq!(
        serde_json::from_slice::<State>(&snapshot).unwrap().latest_at,
        rig.now
    );
    assert_eq!(rig.state.entries, rig.log.len() as u64);
}

#[test]
fn a_request_carries_what_it_likes_beside_what_is_read() {
    let mut rig = Rig::new();
    let (alice, a, _bob, b) = pair(&mut rig);
    // Members no rule reads, however odd, do not get a request refused (BAP/1 JSON convention 2).
    let body = format!(
        r#"{{"sender_session_id":"{a}","recipient_session_id":"{b}","body":"hi","big":1e999,"deep":{},"twice":{{"k":1,"k":2}}}}"#,
        "[".repeat(60) + &"]".repeat(60)
    );
    let sent = rig
        .raw(Some(&alice.credential), "/v1/message/send", body.as_bytes())
        .unwrap();
    assert_eq!(sent["status"], json!("accepted"));
    // A roster of many members with many sessions is one pass.
    let roster = rig.ok(&alice, "/v1/team/members", json!({"team_ref": alice.team_ref}));
    assert_eq!(roster["members"].as_array().unwrap().len(), 2);
}

/// A refusal as the bytes a caller reads.
fn wire(error: &Error) -> String {
    envelope_error(error).to_string()
}

/// The administration requests, each with a body the team's owner would be answered for.
fn administration(team_ref: &str, target: &str) -> [(&'static str, Value); 4] {
    [
        ("/v1/team/rotate-secret", json!({"team_ref": team_ref})),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": team_ref, "principal_ref": target}),
        ),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": team_ref, "joined_secret_version_lte": 1}),
        ),
        (
            "/v1/team/transfer",
            json!({"team_ref": team_ref, "principal_ref": target}),
        ),
    ]
}

/// The version of the secret an active member joined with, as the roster shows it to `asker`.
fn joined_version(rig: &mut Rig, asker: &Member, principal_ref: &str) -> Value {
    let roster = rig.ok(asker, "/v1/team/members", json!({"team_ref": asker.team_ref}));
    roster["members"]
        .as_array()
        .unwrap()
        .iter()
        .find(|member| member["principal_ref"] == json!(principal_ref))
        .unwrap_or_else(|| panic!("{principal_ref} is not on the roster"))["joined_secret_version"]
        .clone()
}

/// The principals of the active members, as the roster shows them to `asker`.
fn active(rig: &mut Rig, asker: &Member) -> Vec<String> {
    let roster = rig.ok(asker, "/v1/team/members", json!({"team_ref": asker.team_ref}));
    roster["members"]
        .as_array()
        .unwrap()
        .iter()
        .map(|member| text(member, "principal_ref"))
        .collect()
}

fn revoke(rig: &mut Rig, owner: &Member, principal_ref: &str, ban: Option<bool>) -> Result<Value, Error> {
    let mut body = json!({"team_ref": owner.team_ref, "principal_ref": principal_ref});
    if let Some(ban) = ban {
        body["ban"] = json!(ban);
    }
    rig.call(Some(&owner.credential), "/v1/team/revoke-member", body)
}

fn revoke_up_to(rig: &mut Rig, owner: &Member, max_version: i64) -> Result<Value, Error> {
    let body = json!({"team_ref": owner.team_ref, "joined_secret_version_lte": max_version});
    rig.call(Some(&owner.credential), "/v1/team/revoke-member", body)
}

fn rotate(rig: &mut Rig, owner: &Member) -> Value {
    rig.ok(
        owner,
        "/v1/team/rotate-secret",
        json!({"team_ref": owner.team_ref}),
    )
}

#[test]
fn only_the_owner_administers_and_every_other_caller_reads_one_refusal() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let bob = rig.join(&secret, "bob");
    let carol = rig.join(&secret, "carol");
    let (stranger, _) = rig.create("stranger");
    rig.ok(&carol, "/v1/team/leave", json!({"team_ref": carol.team_ref}));
    // What a stranger is told about a team's roster is what every administration request is refused with.
    let uniform = wire(&rig.err(&stranger, "/v1/team/members", json!({"team_ref": alice.team_ref})));
    assert_eq!(uniform, wire(&Error::not_member()));
    let (before, entries) = (rig.state.clone(), rig.log.len());

    for (path, body) in administration(&alice.team_ref, &bob.principal_ref) {
        // An active member that is not the owner, a member that left, and a member of another team.
        for caller in [&bob, &carol, &stranger] {
            let refused = rig.err(caller, path, body.clone());
            assert_eq!(refused.code, Code::Unauthorized, "{path}");
            assert_eq!(wire(&refused), uniform, "{path}");
        }
        // A team that does not exist, whoever asks.
        let mut nowhere = body.clone();
        nowhere["team_ref"] = json!("no-such-team");
        for caller in [&alice, &bob] {
            assert_eq!(wire(&rig.err(caller, path, nowhere.clone())), uniform, "{path}");
        }
        // No credential, and one the server does not know, are told so before anything else.
        for credential in [None, Some("not-a-credential")] {
            let refused = rig.call(credential, path, body.clone()).unwrap_err();
            assert_eq!(refused.code, Code::Unauthenticated, "{path}");
        }
    }

    // The owner is checked before the request's shape: a caller that is not the owner is told the same
    // whatever else is wrong with what it sent, where the owner is told what is wrong.
    for (path, body) in [
        ("/v1/team/revoke-member", json!({"team_ref": alice.team_ref})),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "principal_ref": alice.principal_ref}),
        ),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "principal_ref": bob.principal_ref, "joined_secret_version_lte": 1}),
        ),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "joined_secret_version_lte": -1}),
        ),
        ("/v1/team/transfer", json!({"team_ref": alice.team_ref})),
        (
            "/v1/team/transfer",
            json!({"team_ref": alice.team_ref, "principal_ref": "nobody"}),
        ),
    ] {
        for caller in [&bob, &carol, &stranger] {
            assert_eq!(wire(&rig.err(caller, path, body.clone())), uniform, "{path}");
        }
        assert_eq!(rig.err(&alice, path, body).code, Code::InvalidInput, "{path}");
    }
    // The team is named first, as on every request that names one.
    let unnamed = rig.err(&bob, "/v1/team/rotate-secret", json!({}));
    assert_eq!(
        (unnamed.code, unnamed.details["field"].as_str()),
        (Code::InvalidInput, "team_ref")
    );
    // Nothing of all that changed anything or made an entry.
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    // An owner that left is no active member and is refused like anyone. It is still the owner, so it
    // administers again once it has joined again.
    rig.ok(&alice, "/v1/team/leave", json!({"team_ref": alice.team_ref}));
    for (path, body) in administration(&alice.team_ref, &bob.principal_ref) {
        assert_eq!(wire(&rig.err(&alice, path, body)), uniform, "{path}");
    }
    rig.ok(&alice, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(rotate(&mut rig, &alice)["secret_version"], json!(2));
}

#[test]
fn a_rotation_gives_the_team_a_new_secret_and_the_old_one_no_longer_joins() {
    let mut rig = Rig::new();
    let (alice, first) = rig.create("alice");
    let bob = rig.join(&first, "bob");
    let b = rig.register(&bob, "beta");
    rig.advance(30);
    let entries = rig.log.len();

    let rotated = rotate(&mut rig, &alice);
    let second = text(&rotated, "join_secret");
    // The five members of the contract's result, and no other.
    assert_eq!(
        rotated,
        json!({
            "team_ref": alice.team_ref, "team_name": "ops", "secret_version": 2,
            "rotated_at": rig.now.rfc3339(), "join_secret": second,
        })
    );
    // The secret names the team by the reference it has, and its secret part is 128 new random bits.
    let random = second
        .strip_prefix(&format!("brg1.{}.", alice.team_ref))
        .expect("a secret for the same team_ref");
    assert_eq!(random.len(), 32);
    assert!(random.bytes().all(|byte| byte.is_ascii_hexdigit()));
    assert_ne!(second, first);
    // One record, of the new secret's hash: the state and the log keep the hash and never the secret.
    let recorded = Event::TeamSecretRotated {
        team_ref: alice.team_ref.clone(),
        join_secret_hash: sha256_hex(&second),
        secret_version: 2,
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, recorded)]);
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!stored.contains(random) && !logged.contains(random));

    // The secret the team had is refused as a secret it never had and as an unknown team's are, to a
    // stranger and to a member alike, and makes nothing.
    let before = rig.state.clone();
    let join = |rig: &mut Rig, credential: Option<&str>, secret: &str| {
        rig.call(credential, "/v1/team/join", json!({"join_secret": secret}))
            .unwrap_err()
    };
    let never = secret_of(&alice.team_ref, "never-a-secret-of-the-team");
    let garbage = join(&mut rig, None, &never);
    assert_eq!(garbage.code, Code::Unauthorized);
    assert_eq!(wire(&garbage), wire(&Error::secret_rejected()));
    assert_eq!(wire(&join(&mut rig, None, &first)), wire(&garbage));
    assert_eq!(
        wire(&join(&mut rig, Some(&bob.credential), &first)),
        wire(&garbage)
    );
    let nowhere = secret_of("no-such-team", "never-a-secret-of-the-team");
    assert_eq!(wire(&join(&mut rig, None, &nowhere)), wire(&garbage));
    assert_eq!(rig.state, before);

    // No membership changed: bob is a member with his session, at the version he joined with.
    assert_eq!(rig.list(&alice, false).len(), 1);
    rig.ok(
        &bob,
        "/v1/session/heartbeat",
        json!({"session_id": b, "heartbeat": {}}),
    );
    assert_eq!(joined_version(&mut rig, &bob, &bob.principal_ref), json!(1));
    assert_eq!(joined_version(&mut rig, &bob, &alice.principal_ref), json!(1));
    // A member that joins with the new secret is of its version, and so is one that joins again with it.
    let carol = rig.join(&second, "carol");
    assert_eq!(joined_version(&mut rig, &alice, &carol.principal_ref), json!(2));
    let again = rig.ok(&bob, "/v1/team/join", json!({"join_secret": second}));
    assert_eq!(again["rejoined"], json!(true));
    assert_eq!(joined_version(&mut rig, &alice, &bob.principal_ref), json!(2));
    assert_eq!(rig.list(&alice, false).len(), 1);

    // Every rotation counts one more, and ends the secret before it.
    rig.advance(1);
    let third = rotate(&mut rig, &alice);
    assert_eq!(third["secret_version"], json!(3));
    assert_eq!(third["rotated_at"], json!(rig.now.rfc3339()));
    assert_eq!(wire(&join(&mut rig, None, &second)), wire(&garbage));
    rig.join(&text(&third, "join_secret"), "dave");
}

#[test]
fn a_revoked_member_is_out_until_it_joins_again_and_a_banned_one_stays_out() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let a = rig.register(&alice, "alpha");
    let bob = rig.join(&secret, "bob");
    let b = rig.register(&bob, "beta");
    let closed = rig.register(&bob, "beta-2");
    rig.ok(&bob, "/v1/session/close", json!({"session_id": closed}));
    let answer = |principal_ref: &str, status: &str, changed: bool, sessions_closed: u32| {
        json!({
            "team_ref": alice.team_ref, "principal_ref": principal_ref, "status": status,
            "changed": changed, "sessions_closed": sessions_closed,
        })
    };

    // The owner cannot name itself, with a ban or without one.
    for ban in [None, Some(true)] {
        let own = revoke(&mut rig, &alice, &alice.principal_ref, ban).unwrap_err();
        assert_eq!(
            (own.code, own.details["field"].as_str(), reason(&own)),
            (Code::InvalidInput, "principal_ref", "invalid_value")
        );
    }
    // A principal the team never had is no error, and no membership is made for it.
    let (stranger, _) = rig.create("stranger");
    let entries = rig.log.len();
    for principal_ref in [stranger.principal_ref.as_str(), "never-a-principal"] {
        assert_eq!(
            revoke(&mut rig, &alice, principal_ref, Some(true)).unwrap(),
            answer(principal_ref, "banned", false, 0)
        );
    }
    assert_eq!(rig.log.len(), entries);
    assert_eq!(rig.state.teams[&alice.team_ref].members.len(), 2);

    // A revocation ends the membership and closes the member's open sessions: one of bob's two was closed.
    rig.advance(5);
    assert_eq!(
        revoke(&mut rig, &alice, &bob.principal_ref, None).unwrap(),
        answer(&bob.principal_ref, "revoked", true, 1)
    );
    let recorded = Event::MemberRevoked {
        team_ref: alice.team_ref.clone(),
        principal_ref: bob.principal_ref.clone(),
        banned: false,
    };
    assert_eq!(rig.log.last().unwrap().records, [Record::new(rig.now, recorded)]);
    // The member is refused as one that left: one answer on everything that touches the team.
    let uniform = wire(&Error::not_member());
    for (path, body) in [
        ("/v1/session/list", json!({"team_ref": bob.team_ref})),
        ("/v1/team/members", json!({"team_ref": bob.team_ref})),
        ("/v1/message/receive", json!({"session_id": b})),
        ("/v1/session/heartbeat", json!({"session_id": b, "heartbeat": {}})),
        ("/v1/session/register", registration(&bob, "again")),
        (
            "/v1/message/send",
            json!({"sender_session_id": b, "recipient_session_id": a, "body": "still here"}),
        ),
    ] {
        assert_eq!(wire(&rig.err(&bob, path, body)), uniform, "{path}");
    }
    assert_eq!(rig.list(&alice, true).len(), 1);
    assert_eq!(active(&mut rig, &alice), [alice.principal_ref.as_str()]);
    // Revoked again, nothing moves and nothing is recorded.
    let entries = rig.log.len();
    assert_eq!(
        revoke(&mut rig, &alice, &bob.principal_ref, Some(false)).unwrap(),
        answer(&bob.principal_ref, "revoked", false, 0)
    );
    assert_eq!(rig.log.len(), entries);

    // It comes back with the secret as the member it was, as after a leave, and resumes its session.
    let back = rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(
        (back["rejoined"].clone(), text(&back, "principal_ref")),
        (json!(true), bob.principal_ref.clone())
    );
    let mut resume = registration(&bob, "beta");
    resume["resume"] = json!({"session_id": b});
    rig.ok(&bob, "/v1/session/register", resume);

    // A ban ends the membership too, and the principal's join is then refused as a wrong secret is: the
    // same bytes, with the right secret, and nothing made.
    assert_eq!(
        revoke(&mut rig, &alice, &bob.principal_ref, Some(true)).unwrap(),
        answer(&bob.principal_ref, "banned", true, 1)
    );
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let wrong = secret_of(&alice.team_ref, "not-the-secret-of-the-team");
    let rejected = rig
        .call(None, "/v1/team/join", json!({"join_secret": wrong}))
        .unwrap_err();
    assert_eq!(wire(&rejected), wire(&Error::secret_rejected()));
    let banned = rig.err(&bob, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(banned.code, Code::Unauthorized);
    assert_eq!(wire(&banned), wire(&rejected));
    assert_eq!(
        wire(&rig.err(&bob, "/v1/team/join", json!({"join_secret": wrong}))),
        wire(&rejected)
    );
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);
    // Banned again, nothing moves.
    assert_eq!(
        revoke(&mut rig, &alice, &bob.principal_ref, Some(true)).unwrap(),
        answer(&bob.principal_ref, "banned", false, 0)
    );
    assert_eq!(rig.log.len(), entries);

    // A member that was revoked can still be banned: the status moves, and no session is left to close.
    let carol = rig.join(&secret, "carol");
    revoke(&mut rig, &alice, &carol.principal_ref, None).unwrap();
    assert_eq!(
        revoke(&mut rig, &alice, &carol.principal_ref, Some(true)).unwrap(),
        answer(&carol.principal_ref, "banned", true, 0)
    );
    assert!(rig.state.teams[&alice.team_ref].members[&carol.principal_ref].banned);
    // The ban is on the principal. A caller with no credential is a new principal, which nobody banned:
    // the secret is what keeps a stranger out.
    let fresh = rig
        .call(None, "/v1/team/join", json!({"join_secret": secret}))
        .unwrap();
    assert_eq!(fresh["rejoined"], json!(false));
    assert_ne!(text(&fresh, "principal_ref"), carol.principal_ref);

    // A revocation without a ban lifts one: the status moves back, and the principal joins as the member
    // it was.
    assert_eq!(
        revoke(&mut rig, &alice, &bob.principal_ref, None).unwrap(),
        answer(&bob.principal_ref, "revoked", true, 0)
    );
    assert!(!rig.state.teams[&alice.team_ref].members[&bob.principal_ref].banned);
    let back = rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(
        (back["rejoined"].clone(), text(&back, "principal_ref")),
        (json!(true), bob.principal_ref.clone())
    );
    assert!(active(&mut rig, &alice).contains(&bob.principal_ref));
    assert_eq!(
        wire(&rig.err(&carol, "/v1/team/join", json!({"join_secret": secret}))),
        wire(&rejected)
    );
}

#[test]
fn a_revocation_by_version_sends_everyone_but_the_owner_back_through_the_new_secret() {
    let mut rig = Rig::new();
    let (alice, first) = rig.create("alice");
    rig.register(&alice, "alpha");
    let bob = rig.join(&first, "bob");
    let carol = rig.join(&first, "carol");
    let erin = rig.join(&first, "erin");
    rig.register(&bob, "beta");
    rig.register(&bob, "beta-2");
    rig.register(&carol, "gamma");
    revoke(&mut rig, &alice, &erin.principal_ref, Some(true)).unwrap();
    let second = text(&rotate(&mut rig, &alice), "join_secret");
    let dave = rig.join(&second, "dave");
    rig.register(&dave, "delta");
    let answer = |max_version: i64, revoked: u32, sessions_closed: u32| {
        json!({
            "team_ref": alice.team_ref, "max_version": max_version, "revoked": revoked,
            "sessions_closed": sessions_closed,
        })
    };

    // Nobody joined with a version below the first: nothing happens, and nothing is recorded.
    let entries = rig.log.len();
    assert_eq!(revoke_up_to(&mut rig, &alice, 0).unwrap(), answer(0, 0, 0));
    assert_eq!(rig.log.len(), entries);

    // The step after a rotation: everyone that joined with the first secret, except the owner, whose own
    // version is the first. One record, however many members it covers.
    rig.advance(5);
    assert_eq!(revoke_up_to(&mut rig, &alice, 1).unwrap(), answer(1, 2, 3));
    let recorded = Event::MembersRevokedByVersion {
        team_ref: alice.team_ref.clone(),
        max_version: 1,
        except: alice.principal_ref.clone(),
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, recorded)]);
    // The owner and the member that joined with the new secret are as they were, sessions and all.
    assert_eq!(
        active(&mut rig, &alice),
        [alice.principal_ref.as_str(), dave.principal_ref.as_str()]
    );
    assert_eq!(joined_version(&mut rig, &alice, &alice.principal_ref), json!(1));
    let names: Vec<Value> = rig
        .list(&alice, false)
        .iter()
        .map(|session| session["session_name"].clone())
        .collect();
    assert_eq!(names, [json!("alpha"), json!("delta")]);
    // The revoked are refused as members that left.
    let uniform = wire(&Error::not_member());
    for member in [&bob, &carol] {
        let refused = rig.err(member, "/v1/session/list", json!({"team_ref": member.team_ref}));
        assert_eq!(wire(&refused), uniform);
    }
    // It never bans, and it leaves a banned member banned: the new secret does not let her in.
    let members = &rig.state.teams[&alice.team_ref].members;
    assert!(members[&erin.principal_ref].banned);
    assert!(!members[&bob.principal_ref].banned && !members[&carol.principal_ref].banned);
    assert_eq!(
        wire(&rig.err(&erin, "/v1/team/join", json!({"join_secret": second}))),
        wire(&Error::secret_rejected())
    );
    // Asked again, there is nobody left of that version.
    let entries = rig.log.len();
    assert_eq!(revoke_up_to(&mut rig, &alice, 1).unwrap(), answer(1, 0, 0));
    assert_eq!(rig.log.len(), entries);

    // Everyone comes back through the new secret, as the member they were and of its version.
    let back = rig.ok(&bob, "/v1/team/join", json!({"join_secret": second}));
    assert_eq!(
        (back["rejoined"].clone(), text(&back, "principal_ref")),
        (json!(true), bob.principal_ref.clone())
    );
    assert_eq!(joined_version(&mut rig, &alice, &bob.principal_ref), json!(2));
    // The owner is spared whatever version is named, the highest a request may name included.
    assert_eq!(
        revoke_up_to(&mut rig, &alice, 2_147_483_647).unwrap(),
        answer(2_147_483_647, 2, 1)
    );
    assert_eq!(active(&mut rig, &alice), [alice.principal_ref.as_str()]);
    assert!(rig.state.teams[&alice.team_ref].members[&erin.principal_ref].banned);
}

#[test]
fn a_transfer_makes_an_active_member_the_owner_and_the_owner_it_was_a_member() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice");
    let a = rig.register(&alice, "alpha");
    let bob = rig.join(&secret, "bob");
    let carol = rig.join(&secret, "carol");
    let (stranger, _) = rig.create("stranger");
    rig.ok(&carol, "/v1/team/leave", json!({"team_ref": carol.team_ref}));
    let transfer = |rig: &mut Rig, owner: &Member, principal_ref: &str| {
        let body = json!({"team_ref": owner.team_ref, "principal_ref": principal_ref});
        rig.call(Some(&owner.credential), "/v1/team/transfer", body)
    };

    // The new owner must be an active member of the team: a member that left, a member of another team
    // and a principal that never was are one refusal, and change nothing.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let mut refusals = Vec::new();
    for principal_ref in [
        carol.principal_ref.as_str(),
        stranger.principal_ref.as_str(),
        "never-a-principal",
    ] {
        let refused = transfer(&mut rig, &alice, principal_ref).unwrap_err();
        assert_eq!(
            (refused.code, refused.details["field"].as_str(), reason(&refused)),
            (Code::InvalidInput, "principal_ref", "invalid_value")
        );
        refusals.push(wire(&refused));
    }
    assert!(refusals.iter().all(|refusal| *refusal == refusals[0]));
    // The owner naming itself is accepted, changes nothing and makes no entry.
    assert_eq!(
        transfer(&mut rig, &alice, &alice.principal_ref).unwrap(),
        json!({"team_ref": alice.team_ref, "principal_ref": alice.principal_ref, "transferred": true})
    );
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    rig.advance(5);
    assert_eq!(
        transfer(&mut rig, &alice, &bob.principal_ref).unwrap(),
        json!({"team_ref": alice.team_ref, "principal_ref": bob.principal_ref, "transferred": true})
    );
    let recorded = Event::TeamOwnerChanged {
        team_ref: alice.team_ref.clone(),
        principal_ref: bob.principal_ref.clone(),
    };
    assert_eq!(rig.log.last().unwrap().records, [Record::new(rig.now, recorded)]);
    // The owner it was stays a member with its session, and its next administration request is refused
    // as any member's is.
    assert_eq!(rig.list(&alice, false).len(), 1);
    rig.ok(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": a, "heartbeat": {}}),
    );
    let uniform = wire(&Error::not_member());
    for (path, body) in administration(&alice.team_ref, &bob.principal_ref) {
        assert_eq!(wire(&rig.err(&alice, path, body)), uniform, "{path}");
    }
    // The new owner administers: it may even revoke the owner it replaced, and hand the team back.
    assert_eq!(rotate(&mut rig, &bob)["secret_version"], json!(2));
    assert_eq!(
        transfer(&mut rig, &bob, &alice.principal_ref).unwrap()["transferred"],
        json!(true)
    );
    assert_eq!(
        revoke(&mut rig, &alice, &bob.principal_ref, None).unwrap()["changed"],
        json!(true)
    );
}

#[test]
fn the_first_member_to_join_a_team_that_was_taken_in_owns_it_and_a_gateway_never_does() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("ops", &secret).unwrap();
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    let uniform = wire(&Error::not_member());

    // A gateway joins inside the server, and its join may not own: the team still has no owner, and
    // until it has one every administration request is refused, the gateway's too.
    rig.may_own = false;
    let gateway = rig.join(&secret, "slack gateway");
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    assert_eq!(
        rig.log.last().unwrap().records.len(),
        2,
        "a credential and a membership, and no owner"
    );
    for (path, body) in administration(ELSEWHERE, &gateway.principal_ref) {
        assert_eq!(wire(&rig.err(&gateway, path, body)), uniform, "{path}");
    }

    // The first member to join over HTTP owns the team, in the entry that makes it a member.
    rig.may_own = true;
    rig.advance(5);
    let alice = rig.join(&secret, "alice");
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    let owned = Event::TeamOwnerChanged {
        team_ref: ELSEWHERE.into(),
        principal_ref: alice.principal_ref.clone(),
    };
    let entry = rig.log.last().unwrap();
    assert_eq!(entry.records.len(), 3);
    assert!(matches!(entry.records[1].event, Event::MemberJoined { .. }));
    assert_eq!(entry.records[2], Record::new(rig.now, owned));
    // Nobody that joins after it does, and neither does the gateway by joining again.
    let bob = rig.join(&secret, "bob");
    rig.ok(&gateway, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    for member in [&bob, &gateway] {
        let refused = rig.err(member, "/v1/team/rotate-secret", json!({"team_ref": ELSEWHERE}));
        assert_eq!(wire(&refused), uniform);
    }

    // The owner administers the team as the owner of one that was created here does. A rotation keeps
    // the reference the team came with, which is not one this server would have made.
    let rotated = rotate(&mut rig, &alice);
    let second = text(&rotated, "join_secret");
    assert_eq!(rotated["team_ref"], json!(ELSEWHERE));
    assert!(second.starts_with(&format!("brg1.{ELSEWHERE}.")));
    assert_eq!(rig.join(&second, "carol").team_ref, ELSEWHERE);
    assert_eq!(revoke_up_to(&mut rig, &alice, 1).unwrap()["revoked"], json!(2));

    // On a team that already has members and no owner, as one taken in by an earlier server has, the
    // first join that may own is a member's that joins again.
    let mut rig = Rig::new();
    rig.import("ops", &secret).unwrap();
    rig.may_own = false;
    let early = rig.join(&secret, "early");
    rig.may_own = true;
    let again = rig.ok(&early, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(again["rejoined"], json!(true));
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(early.principal_ref.as_str())
    );

    // A team that was created here has its creator for an owner, and no join changes that.
    let (creator, created) = rig.create("creator");
    rig.join(&created, "joiner");
    assert_eq!(
        rig.state.teams[&creator.team_ref].owner(),
        Some(creator.principal_ref.as_str())
    );
}

#[test]
fn the_operator_names_the_owner_of_a_team_among_its_active_members() {
    let mut rig = Rig::new();
    let (alice, secret) = rig.create("alice@example.com");
    let bob = rig.join(&secret, "bob@example.com");
    let carol = rig.join(&secret, "");
    rig.ok(&carol, "/v1/team/leave", json!({"team_ref": carol.team_ref}));
    let (stranger, _) = rig.create("stranger");
    let (before, entries) = (rig.state.clone(), rig.log.len());

    // A team the server does not hold, and a principal that is not an active member of the team, are
    // refused, and neither refusal repeats what was named.
    let nowhere = rig.set_owner("no-such-team", &bob.principal_ref).unwrap_err();
    assert_eq!(nowhere.code, Code::NotFound);
    assert_eq!(nowhere.message, "the server holds no team of that reference");
    for principal_ref in [
        carol.principal_ref.as_str(),
        stranger.principal_ref.as_str(),
        "never-a-principal",
    ] {
        let refused = rig.set_owner(&alice.team_ref, principal_ref).unwrap_err();
        assert_eq!(
            (refused.code, refused.details["field"].as_str()),
            (Code::InvalidInput, "principal_ref")
        );
        assert_eq!(
            refused.message,
            "principal_ref must be an active member of this team"
        );
    }
    // The principal that already owns the team changes nothing and makes no entry.
    assert_eq!(
        rig.set_owner(&alice.team_ref, &alice.principal_ref).unwrap(),
        json!({
            "team_ref": alice.team_ref, "team_name": "ops", "principal_ref": alice.principal_ref,
            "human_label": "alice@example.com", "changed": false,
        })
    );
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    // Another member is the owner from the record on, whatever the owner before it wanted.
    rig.advance(5);
    assert_eq!(
        rig.set_owner(&alice.team_ref, &bob.principal_ref).unwrap(),
        json!({
            "team_ref": alice.team_ref, "team_name": "ops", "principal_ref": bob.principal_ref,
            "human_label": "bob@example.com", "changed": true,
        })
    );
    let recorded = Event::TeamOwnerChanged {
        team_ref: alice.team_ref.clone(),
        principal_ref: bob.principal_ref.clone(),
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, recorded)]);
    assert_eq!(rotate(&mut rig, &bob)["secret_version"], json!(2));
    let refused = rig.err(
        &alice,
        "/v1/team/rotate-secret",
        json!({"team_ref": alice.team_ref}),
    );
    assert_eq!(wire(&refused), wire(&Error::not_member()));

    // A team that was taken in and that only a gateway has joined gets its owner this way too.
    let moved = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("moved", &moved).unwrap();
    rig.may_own = false;
    let member = rig.join(&moved, "");
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    assert_eq!(
        rig.set_owner(ELSEWHERE, &member.principal_ref).unwrap(),
        json!({
            "team_ref": ELSEWHERE, "team_name": "moved", "principal_ref": member.principal_ref,
            "human_label": "", "changed": true,
        })
    );
    assert_eq!(rotate(&mut rig, &member)["secret_version"], json!(2));
}

#[test]
fn the_administration_records_make_the_same_state_when_they_are_read_again() {
    let mut rig = Rig::new();
    let (alice, first) = rig.create("alice");
    let bob = rig.join(&first, "bob");
    let carol = rig.join(&first, "carol");
    let erin = rig.join(&first, "erin");
    let a = rig.register(&alice, "alpha");
    let b = rig.register(&bob, "beta");
    rig.register(&carol, "gamma");
    rig.send(&alice, &a, &b, "before any of it").unwrap();
    rig.advance(7);
    revoke(&mut rig, &alice, &erin.principal_ref, Some(true)).unwrap();
    let second = text(&rotate(&mut rig, &alice), "join_secret");
    let dave = rig.join(&second, "dave");
    rig.register(&dave, "delta");
    rig.advance(7);
    revoke_up_to(&mut rig, &alice, 1).unwrap();
    rig.ok(&bob, "/v1/team/join", json!({"join_secret": second}));
    revoke(&mut rig, &alice, &dave.principal_ref, None).unwrap();
    revoke(&mut rig, &alice, &erin.principal_ref, None).unwrap();
    rig.advance(7);
    rig.ok(
        &alice,
        "/v1/team/transfer",
        json!({"team_ref": alice.team_ref, "principal_ref": bob.principal_ref}),
    );
    rotate(&mut rig, &bob);
    rig.set_owner(&alice.team_ref, &alice.principal_ref).unwrap();
    let moved = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("moved", &moved).unwrap();
    rig.join(&moved, "first");
    rig.join(&moved, "second");

    // A restart: every entry, read back from its bytes, applied to an empty state.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    // The state says what the requests were answered with.
    let team = &replayed.teams[&alice.team_ref];
    assert_eq!(team.secret_version, 3);
    assert_eq!(team.owner(), Some(alice.principal_ref.as_str()));
    let of = |member: &Member| &team.members[&member.principal_ref];
    assert!(of(&alice).active && of(&bob).active);
    assert!(!of(&carol).active && !of(&dave).active && !of(&erin).active);
    assert!(!of(&erin).banned);
    assert_eq!(
        (
            of(&alice).joined_secret_version,
            of(&bob).joined_secret_version,
            of(&dave).joined_secret_version
        ),
        (1, 2, 2)
    );
    assert!(replayed.teams[ELSEWHERE].owner().is_some());
    // A snapshot: the state written and read back, versions, bans and owners in it.
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    assert_eq!(serde_json::from_slice::<State>(&snapshot).unwrap(), rig.state);
}

/// Takes out of a written state what a team and a member have had since teams counted their secrets.
fn as_written_before(state: &mut Value) {
    match state {
        Value::Object(members) => {
            for newer in ["secret_version", "joined_secret_version", "banned"] {
                members.remove(newer);
            }
            members.values_mut().for_each(as_written_before);
        }
        Value::Array(items) => items.iter_mut().for_each(as_written_before),
        _ => {}
    }
}

#[test]
fn a_snapshot_written_before_teams_counted_their_secrets_still_reads() {
    // The state as server 0.3.0 wrote it: a team has no secret version, and a member neither the version
    // it joined with nor a ban. One team was created there and one was taken in.
    let written = r#"{
        "principals": {"p1": {"created_at": 5}, "p2": {"created_at": 6}},
        "credentials": {"h1": "p1", "h2": "p2"},
        "teams": {
            "t": {
                "team_ref": "t", "team_name": "ops", "join_secret_hash": "s", "created_by": "p1",
                "created_at": 5,
                "members": {
                    "p1": {"human_label": "alice", "active": true, "joined_at": 5, "sent_at": [7]},
                    "p2": {"human_label": "", "active": false, "joined_at": 6, "sent_at": []}
                },
                "sessions": {}
            },
            "elsewhere": {
                "team_ref": "elsewhere", "team_name": "moved", "join_secret_hash": "s2", "created_by": "",
                "created_at": 7, "members": {}, "sessions": {}
            }
        },
        "session_teams": {}, "swept_at": 0, "latest_at": 7, "entries": 4
    }"#;
    let state: State = serde_json::from_str(written).unwrap();
    // Every team had its first secret then, and every member had joined with it; nobody was banned.
    let team = &state.teams["t"];
    assert_eq!((team.secret_version, team.owner()), (1, Some("p1")));
    for (principal_ref, active) in [("p1", true), ("p2", false)] {
        let member = &team.members[principal_ref];
        assert_eq!(
            (member.active, member.joined_secret_version, member.banned),
            (active, 1, false)
        );
    }
    assert_eq!(team.members["p1"].sent_at.len(), 1);
    let moved = &state.teams["elsewhere"];
    assert_eq!((moved.secret_version, moved.owner()), (1, None));
    assert_eq!((state.entries, state.latest_at), (4, Timestamp(7)));
    // Written by this build and read back it is the same state, with what a team and a member have now.
    let again = serde_json::to_string(&state).unwrap();
    assert!(again.contains(r#""secret_version":1"#) && again.contains(r#""joined_secret_version":1"#));
    assert_eq!(serde_json::from_str::<State>(&again).unwrap(), state);

    // The same for a whole state with sessions and messages in it: what this build writes, less the three
    // members that are new, is what the earlier build wrote, and it reads back as the state it was.
    let mut rig = Rig::new();
    let (alice, a, bob, b) = pair(&mut rig);
    rig.send(&alice, &a, &b, "kept across the upgrade").unwrap();
    rig.ok(&bob, "/v1/team/leave", json!({"team_ref": bob.team_ref}));
    rig.import("moved", &secret_of(ELSEWHERE, "kept-from-the-other-backend"))
        .unwrap();
    let mut earlier = serde_json::to_value(&rig.state).unwrap();
    as_written_before(&mut earlier);
    let earlier = earlier.to_string();
    assert!(!earlier.contains("secret_version") && !earlier.contains("banned"));
    assert_eq!(serde_json::from_str::<State>(&earlier).unwrap(), rig.state);
}

#[test]
fn the_operator_takes_a_team_back_from_whoever_holds_it_in_one_entry() {
    let mut rig = Rig::new();
    let first = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("ops", &first).unwrap();
    // The secret leaked. Whoever had it joined first, and so owns the team; the team's admin and a
    // colleague joined after, and a gateway inside the server.
    let mallory = rig.join(&first, "mallory");
    let alice = rig.join(&first, "alice");
    let bob = rig.join(&first, "bob");
    rig.may_own = false;
    let gateway = rig.join(&first, "slack gateway");
    rig.may_own = true;
    rig.register(&alice, "alpha");
    rig.register(&gateway, "slack-gateway");
    let m = rig.register(&mallory, "mu");
    // The owner bans one member, rotates the secret and revokes everyone who joined with the old one:
    // nobody but the owner is a member, and nobody else holds the secret the team has now.
    revoke(&mut rig, &mallory, &bob.principal_ref, Some(true)).unwrap();
    let second = text(&rotate(&mut rig, &mallory), "join_secret");
    assert_eq!(revoke_up_to(&mut rig, &mallory, 1).unwrap()["revoked"], json!(2));
    assert_eq!(active(&mut rig, &mallory), [mallory.principal_ref.as_str()]);
    // Naming an owner does not help: nobody the operator could name is an active member.
    let stuck = rig.set_owner(ELSEWHERE, &alice.principal_ref).unwrap_err();
    assert_eq!(stuck.details["field"], "principal_ref");

    // A team the server does not hold is refused, and nothing is made.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let nowhere = rig.take_back("no-such-team").unwrap_err();
    assert_eq!(wire(&nowhere), wire(&Error::no_such_team()));
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    rig.advance(5);
    let taken = rig.take_back(ELSEWHERE).unwrap();
    let third = text(&taken, "join_secret");
    assert_eq!(
        taken,
        json!({
            "team_ref": ELSEWHERE, "team_name": "ops", "secret_version": 3, "join_secret": third,
            "revoked": 1, "sessions_closed": 1,
        })
    );
    // The secret is minted as a rotation mints one: for the same reference, 128 new random bits.
    let random = third
        .strip_prefix(&format!("brg1.{ELSEWHERE}."))
        .expect("a secret for the same team_ref");
    assert_eq!(random.len(), 32);
    assert!(random.bytes().all(|byte| byte.is_ascii_hexdigit()));
    assert!(third != second && third != first);
    // One entry of three records: the log holds the new secret's hash, every membership ended and no
    // owner, or none of it. It holds the hash and never the secret, and so does the state.
    let records = [
        Event::TeamSecretRotated {
            team_ref: ELSEWHERE.into(),
            join_secret_hash: sha256_hex(&third),
            secret_version: 3,
        },
        Event::MembersRevokedByVersion {
            team_ref: ELSEWHERE.into(),
            max_version: u32::MAX,
            except: String::new(),
        },
        Event::TeamOwnerChanged {
            team_ref: ELSEWHERE.into(),
            principal_ref: String::new(),
        },
    ];
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(
        rig.log[entries].records,
        records.map(|event| Record::new(rig.now, event))
    );
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!stored.contains(random) && !logged.contains(random));

    // Its three effects. The team has no owner and no active member, and the session that was open is
    // closed; the member that was banned is banned still.
    let team = &rig.state.teams[ELSEWHERE];
    assert_eq!((team.owner(), team.secret_version), (None, 3));
    assert!(team.members.values().all(|member| !member.active));
    assert!(team.members[&bob.principal_ref].banned);
    assert!(!team.members[&mallory.principal_ref].banned);
    assert_eq!(team.sessions[&m].closed_at, Some(rig.now));
    // Every secret the team had before is refused as a secret it never had, whoever presents it.
    let rejected = wire(&Error::secret_rejected());
    for old in [&first, &second] {
        for credential in [None, Some(mallory.credential.as_str())] {
            let refused = rig
                .call(credential, "/v1/team/join", json!({"join_secret": old}))
                .unwrap_err();
            assert_eq!(wire(&refused), rejected);
        }
    }
    // The owner it had cannot administer it, and reads what any stranger reads.
    let uniform = wire(&Error::not_member());
    for (path, body) in administration(ELSEWHERE, &alice.principal_ref) {
        assert_eq!(wire(&rig.err(&mallory, path, body)), uniform, "{path}");
    }

    // A gateway that joins with the new secret does not own the team, however early it joins. The first
    // member to join with it over HTTP does, in the entry that makes it a member again.
    rig.may_own = false;
    let back = rig.ok(&gateway, "/v1/team/join", json!({"join_secret": third}));
    assert_eq!(back["rejoined"], json!(true));
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    rig.may_own = true;
    rig.advance(5);
    let back = rig.ok(&alice, "/v1/team/join", json!({"join_secret": third}));
    assert_eq!(
        (back["rejoined"].clone(), text(&back, "principal_ref")),
        (json!(true), alice.principal_ref.clone())
    );
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    assert_eq!(joined_version(&mut rig, &alice, &alice.principal_ref), json!(3));
    assert_eq!(rotate(&mut rig, &alice)["secret_version"], json!(4));
    // The member that was banned is refused with the right secret, as before the team was taken back.
    assert_eq!(
        wire(&rig.err(&bob, "/v1/team/join", json!({"join_secret": third}))),
        rejected
    );

    // Taken back again, a team with one member and its owner is as it was after the first time, with one
    // more secret. A team that has no member and no owner makes the one record of its secret.
    let again = rig.take_back(ELSEWHERE).unwrap();
    assert_eq!(
        (&again["secret_version"], &again["revoked"]),
        (&json!(5), &json!(2))
    );
    let entries = rig.log.len();
    let empty = rig.take_back(ELSEWHERE).unwrap();
    assert_eq!(
        (
            &empty["secret_version"],
            &empty["revoked"],
            &empty["sessions_closed"]
        ),
        (&json!(6), &json!(0), &json!(0))
    );
    assert_eq!(rig.log[entries].records.len(), 1);

    // A restart: every entry, read back from its bytes, makes the same state.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    assert_eq!(serde_json::from_slice::<State>(&snapshot).unwrap(), rig.state);
}

#[test]
fn a_membership_that_ends_does_not_bring_back_a_session_retention_let_go_of() {
    let week = 7 * 86_400;
    // A member is revoked, and another leaves, long after the lease of a session of theirs ran out.
    for leaves in [false, true] {
        let mut rig = Rig::new();
        let (alice, secret) = rig.create("alice");
        let bob = rig.join(&secret, "bob");
        let stale = rig.register(&bob, "long-gone");
        let mut longer = registration(&bob, "still-held");
        longer["lease_seconds"] = json!(600);
        let live = text(&rig.ok(&bob, "/v1/session/register", longer), "session_id");
        // Just over a week after the shorter lease ran out, with nothing written in between: no sweep has
        // run, and both sessions are in the state with no close. Retention has let go of the one and
        // still holds the other, whose lease ran out less than a week ago.
        rig.advance(90 + week + 1);
        assert!(rig.state.session_teams.contains_key(&stale));
        assert_eq!(rig.list(&alice, true).len(), 1);
        let ended = if leaves {
            rig.ok(&bob, "/v1/team/leave", json!({"team_ref": bob.team_ref}));
            None
        } else {
            Some(revoke(&mut rig, &alice, &bob.principal_ref, None).unwrap())
        };
        // The session retention holds is closed, and is the one that is counted. The other is not
        // closed, which would have kept it for another week: the sweep the record runs lets go of it.
        if let Some(revoked) = ended {
            assert_eq!(revoked["sessions_closed"], json!(1), "{revoked}");
        }
        let team = &rig.state.teams[&alice.team_ref];
        assert_eq!(team.sessions[&live].closed_at, Some(rig.now));
        assert!(!team.sessions.contains_key(&stale), "leaves: {leaves}");
        assert!(!rig.state.session_teams.contains_key(&stale));
        // The member comes back and finds the one session to resume, and the other gone.
        rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
        for (session, found) in [(&live, true), (&stale, false)] {
            let mut resume = registration(&bob, "again");
            resume["resume"] = json!({"session_id": session});
            let resumed = rig.call(Some(&bob.credential), "/v1/session/register", resume);
            assert_eq!(resumed.is_ok(), found, "leaves: {leaves}");
        }
        // A restart reaches the same state.
        let mut replayed = State::default();
        for entry in &rig.log {
            replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
        }
        assert_eq!(replayed, rig.state);
    }
}

/// A day, in seconds.
const DAY: i64 = 86_400;

/// What a join with `secret` is refused with, by a caller with no credential.
fn join_refused(rig: &mut Rig, secret: &str) -> String {
    let refused = rig
        .call(None, "/v1/team/join", json!({"join_secret": secret}))
        .expect_err("the join was let in");
    wire(&refused)
}

/// The names of the members of a JSON object, in the order of the alphabet.
fn members_of(value: &Value) -> Vec<&str> {
    value.as_object().unwrap().keys().map(String::as_str).collect()
}

#[test]
fn a_move_ticket_lets_a_member_in_until_it_ends_and_never_makes_an_owner() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("ops", &secret).unwrap();
    let rejected = wire(&Error::secret_rejected());

    // A team the server does not hold, and a number of days no ticket is given, are refused, and nothing
    // is made.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let nowhere = rig.open_move("no-such-team", 30).unwrap_err();
    assert_eq!(wire(&nowhere), wire(&Error::no_such_team()));
    for days in [0, 366, u32::MAX] {
        let refused = rig.open_move(ELSEWHERE, days).unwrap_err();
        assert_eq!(
            (refused.code, refused.details["field"].as_str(), reason(&refused)),
            (Code::InvalidInput, "ticket_days", "out_of_range"),
            "{days}"
        );
    }
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    // The move is opened: the ticket is a second join secret of the same team, minted as one is, and it
    // ends a whole number of days later. The answer is the one place it is: the log and the state hold
    // its hash.
    let opened = rig.open_move(ELSEWHERE, 30).unwrap();
    let ticket = text(&opened, "ticket");
    let ends_at = rig.now.plus_seconds(30 * DAY);
    assert_eq!(
        opened,
        json!({
            "team_ref": ELSEWHERE, "team_name": "ops", "ticket": ticket, "ends_at": ends_at.rfc3339(),
            "replaced": false,
        })
    );
    let random = ticket
        .strip_prefix(&format!("brg1.{ELSEWHERE}."))
        .expect("a ticket for the same team_ref");
    assert_eq!(random.len(), 32);
    assert!(random.bytes().all(|byte| byte.is_ascii_hexdigit()));
    assert!(ticket != secret);
    let recorded = Event::TeamMoveOpened {
        team_ref: ELSEWHERE.into(),
        ticket_hash: sha256_hex(&ticket),
        ends_at,
        by_owner: false,
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, recorded)]);
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!stored.contains(random) && !logged.contains(random));

    // A member comes in with the ticket over HTTP, on a team that has no owner. It is a member like any
    // other, joined under the secret the team has, and it does not own the team: the entry is a
    // credential and a membership that says how it came in, and no owner.
    rig.advance(5);
    let bob = rig.join(&ticket, "bob");
    assert_eq!(bob.team_ref, ELSEWHERE);
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    let entry = rig.log.last().unwrap();
    assert_eq!(
        entry.records.len(),
        2,
        "a credential and a membership, and no owner"
    );
    let followed = Event::MemberJoined {
        team_ref: ELSEWHERE.into(),
        principal_ref: bob.principal_ref.clone(),
        human_label: "bob".into(),
        by_move_ticket: true,
        retry_key_hash: String::new(),
    };
    assert_eq!(entry.records[1], Record::new(rig.now, followed));
    assert_eq!(active(&mut rig, &bob), [bob.principal_ref.as_str()]);
    assert_eq!(joined_version(&mut rig, &bob, &bob.principal_ref), json!(1));
    let uniform = wire(&Error::not_member());
    for (path, body) in administration(ELSEWHERE, &bob.principal_ref) {
        assert_eq!(wire(&rig.err(&bob, path, body)), uniform, "{path}");
    }
    // Joining again with the ticket is the same member, and still no owner.
    let again = rig.ok(&bob, "/v1/team/join", json!({"join_secret": ticket}));
    assert_eq!(again["rejoined"], json!(true));
    assert_eq!(
        members_of(&again),
        ["principal_ref", "rejoined", "team_name", "team_ref"]
    );
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);

    // The join secret admits as it always did, and makes the first member that joins with it the owner,
    // in the entry that makes it a member.
    rig.advance(5);
    let alice = rig.join(&secret, "alice");
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    let entry = rig.log.last().unwrap();
    assert_eq!(entry.records.len(), 3);
    let joined = Event::MemberJoined {
        team_ref: ELSEWHERE.into(),
        principal_ref: alice.principal_ref.clone(),
        human_label: "alice".into(),
        by_move_ticket: false,
        retry_key_hash: String::new(),
    };
    assert_eq!(entry.records[1], Record::new(rig.now, joined));
    assert!(matches!(entry.records[2].event, Event::TeamOwnerChanged { .. }));
    // The member the ticket let in joins again with the secret, which it may hold as well: it is the
    // member it was, and the team keeps the owner it has.
    let with_secret = rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    assert_eq!(with_secret["rejoined"], json!(true));
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );

    // A ticket is one team's. The ticket of another team's move joins that team, and its random half
    // under this team's reference is a wrong secret.
    let (creator, _) = rig.create("creator");
    let other = text(&rig.open_move(&creator.team_ref, 30).unwrap(), "ticket");
    assert_eq!(rig.join(&other, "dave").team_ref, creator.team_ref);
    let other_random = other.rsplit('.').next().unwrap();
    let (before, entries) = (rig.state.clone(), rig.log.len());
    assert_eq!(
        join_refused(&mut rig, &secret_of(ELSEWHERE, other_random)),
        rejected
    );
    // So is this team's ticket under the other team's reference, and a ticket nobody made.
    assert_eq!(
        join_refused(&mut rig, &secret_of(&creator.team_ref, random)),
        rejected
    );
    assert_eq!(
        join_refused(&mut rig, &secret_of(ELSEWHERE, "never-a-ticket-of-any-move")),
        rejected
    );
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    // A member the owner banned is told what a wrong secret is told, with the ticket as with the secret;
    // a member that was only revoked comes back with the ticket.
    let carol = rig.join(&ticket, "carol");
    revoke(&mut rig, &alice, &bob.principal_ref, Some(true)).unwrap();
    revoke(&mut rig, &alice, &carol.principal_ref, None).unwrap();
    let (before, entries) = (rig.state.clone(), rig.log.len());
    for held in [&ticket, &secret] {
        assert_eq!(
            wire(&rig.err(&bob, "/v1/team/join", json!({"join_secret": held}))),
            rejected
        );
    }
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);
    let back = rig.ok(&carol, "/v1/team/join", json!({"join_secret": ticket}));
    assert_eq!(back["rejoined"], json!(true));

    // The ticket admits until its end and not at it. One second before, a member comes in; at the end
    // and after it the ticket is a wrong secret, with the answer one has and nothing made.
    rig.now = ends_at.minus_seconds(1);
    rig.join(&ticket, "erin");
    for later in [1, 1, 30 * DAY] {
        rig.advance(later);
        let (before, entries) = (rig.state.clone(), rig.log.len());
        assert_eq!(join_refused(&mut rig, &ticket), rejected, "{later}");
        assert_eq!(
            wire(&rig.err(&carol, "/v1/team/join", json!({"join_secret": ticket}))),
            rejected
        );
        assert_eq!(rig.state, before);
        assert_eq!(rig.log.len(), entries);
    }
    // Every member the ticket let in is still one, and the join secret still admits.
    assert!(rig.state.teams[ELSEWHERE].active(&carol.principal_ref));
    rig.join(&secret, "frank");

    // A restart: every entry, read back from its bytes, makes the same state, the move in it.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    assert_eq!(
        replayed.teams[ELSEWHERE]
            .move_ticket
            .as_ref()
            .map(|ticket| ticket.ends_at),
        Some(ends_at)
    );
}

#[test]
fn a_move_is_replaced_by_another_and_closed_by_the_operator_a_rotation_and_a_take_back() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    rig.import("ops", &secret).unwrap();
    let alice = rig.join(&secret, "alice");
    let rejected = wire(&Error::secret_rejected());

    // A team that is not moving has nothing to close: nothing changes and no entry is made. A team the
    // server does not hold is refused.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    assert_eq!(
        rig.close_move(ELSEWHERE).unwrap(),
        json!({"team_ref": ELSEWHERE, "team_name": "ops", "closed": false})
    );
    let nowhere = rig.close_move("no-such-team").unwrap_err();
    assert_eq!(wire(&nowhere), wire(&Error::no_such_team()));
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);

    // A second move takes the place of the first: the first ticket admits nobody from then on, although
    // it has not ended, and the answer says a ticket was replaced.
    let first = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    let bob = rig.join(&first, "bob");
    rig.advance(5);
    // The clock is between two seconds, as a server's is: the ticket ends on the whole second before, so
    // the time the operator is told is the time the record holds.
    let whole = rig.now;
    rig.now = Timestamp(whole.0 + SECOND / 2);
    let opened = rig.open_move(ELSEWHERE, 7).unwrap();
    let second = text(&opened, "ticket");
    let ends_at = whole.plus_seconds(7 * DAY);
    assert_eq!(
        (&opened["replaced"], text(&opened, "ends_at")),
        (&json!(true), ends_at.rfc3339())
    );
    assert!(matches!(
        &rig.log.last().unwrap().records[..],
        [Record { event: Event::TeamMoveOpened { ends_at: recorded, .. }, .. }] if *recorded == ends_at
    ));
    assert!(second != first);
    assert_eq!(join_refused(&mut rig, &first), rejected);
    rig.join(&second, "carol");
    // A move whose ticket has ended is replaced as well, and the answer says so.
    rig.advance(8 * DAY);
    assert_eq!(join_refused(&mut rig, &second), rejected);
    let opened = rig.open_move(ELSEWHERE, 1).unwrap();
    let third = text(&opened, "ticket");
    assert_eq!(opened["replaced"], json!(true));
    rig.join(&third, "dave");

    // The operator closes the move, in one record: its ticket admits nobody, and every member that came
    // in with any of the tickets is still one. Closing it again changes nothing.
    rig.advance(5);
    let entries = rig.log.len();
    assert_eq!(
        rig.close_move(ELSEWHERE).unwrap(),
        json!({"team_ref": ELSEWHERE, "team_name": "ops", "closed": true})
    );
    let closed = Event::TeamMoveClosed {
        team_ref: ELSEWHERE.into(),
        by_owner: false,
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, closed.clone())]);
    assert_eq!(rig.state.teams[ELSEWHERE].move_ticket, None);
    for ticket in [&first, &second, &third] {
        assert_eq!(join_refused(&mut rig, ticket), rejected);
    }
    assert_eq!(active(&mut rig, &alice).len(), 4);
    assert!(rig.state.teams[ELSEWHERE].active(&bob.principal_ref));
    let (before, entries) = (rig.state.clone(), rig.log.len());
    assert_eq!(rig.close_move(ELSEWHERE).unwrap()["closed"], json!(false));
    assert_eq!(rig.state, before);
    assert_eq!(rig.log.len(), entries);
    // A move whose ticket has ended is still there to close.
    rig.open_move(ELSEWHERE, 1).unwrap();
    rig.advance(2 * DAY);
    assert_eq!(rig.close_move(ELSEWHERE).unwrap()["closed"], json!(true));

    // A rotation ends everything that let anyone in before it, the move with it, in the rotation's own
    // entry. Its answer is the one it always was.
    let ticket = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    rig.advance(5);
    let entries = rig.log.len();
    let rotated = rotate(&mut rig, &alice);
    assert_eq!(
        members_of(&rotated),
        [
            "join_secret",
            "rotated_at",
            "secret_version",
            "team_name",
            "team_ref"
        ]
    );
    let new_secret = text(&rotated, "join_secret");
    let records = [
        Event::TeamSecretRotated {
            team_ref: ELSEWHERE.into(),
            join_secret_hash: sha256_hex(&new_secret),
            secret_version: 2,
        },
        closed.clone(),
    ];
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(
        rig.log[entries].records,
        records.map(|event| Record::new(rig.now, event))
    );
    assert_eq!(rig.state.teams[ELSEWHERE].move_ticket, None);
    assert_eq!(join_refused(&mut rig, &ticket), rejected);
    assert_eq!(join_refused(&mut rig, &secret), rejected);
    rig.join(&new_secret, "erin");
    // A rotation of a team that is not moving is the one record it always was.
    let entries = rig.log.len();
    let rotated = rotate(&mut rig, &alice);
    assert_eq!(rig.log[entries].records.len(), 1);
    assert_eq!(rotated["secret_version"], json!(3));

    // A team that is taken back opens to its new secret alone: the move is closed in the entry that
    // takes the team back, and the answer is the one it always was.
    let ticket = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    let frank = rig.join(&ticket, "frank");
    rig.advance(5);
    let entries = rig.log.len();
    let taken = rig.take_back(ELSEWHERE).unwrap();
    assert_eq!(
        members_of(&taken),
        [
            "join_secret",
            "revoked",
            "secret_version",
            "sessions_closed",
            "team_name",
            "team_ref"
        ]
    );
    let kinds: Vec<&Event> = rig.log[entries]
        .records
        .iter()
        .map(|record| &record.event)
        .collect();
    assert_eq!(kinds.len(), 4);
    assert!(matches!(kinds[0], Event::TeamSecretRotated { .. }));
    assert_eq!(kinds[1], &closed);
    assert!(matches!(kinds[2], Event::MembersRevokedByVersion { .. }));
    assert!(matches!(kinds[3], Event::TeamOwnerChanged { .. }));
    assert_eq!(rig.state.teams[ELSEWHERE].move_ticket, None);
    assert_eq!(join_refused(&mut rig, &ticket), rejected);
    assert_eq!(
        wire(&rig.err(&frank, "/v1/team/join", json!({"join_secret": ticket}))),
        rejected
    );
    // The team has no owner again, and a ticket of a new move does not make one: the secret from the
    // operator's file does.
    let ticket = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    rig.ok(&frank, "/v1/team/join", json!({"join_secret": ticket}));
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    rig.ok(
        &alice,
        "/v1/team/join",
        json!({"join_secret": text(&taken, "join_secret")}),
    );
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );

    // A restart: every entry, read back from its bytes, makes the same state. And a snapshot: the state
    // written and read back, with the move that is open in it.
    let mut replayed = State::default();
    for entry in &rig.log {
        replayed.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    assert_eq!(replayed, rig.state);
    assert!(rig.state.teams[ELSEWHERE].move_ticket.is_some());
    let snapshot = serde_json::to_string(&rig.state).unwrap();
    assert!(snapshot.contains(r#""move_ticket":{"hash":""#), "{snapshot}");
    assert_eq!(serde_json::from_str::<State>(&snapshot).unwrap(), rig.state);
    // A team that is not moving is written as it was before there were moves, and such a state reads as
    // one in which no team is moving.
    rig.close_move(ELSEWHERE).unwrap();
    let snapshot = serde_json::to_string(&rig.state).unwrap();
    assert!(!snapshot.contains("move_ticket"), "{snapshot}");
    assert_eq!(serde_json::from_str::<State>(&snapshot).unwrap(), rig.state);
}

/// A take-in as a caller asks for it: the join secret the team has elsewhere, a name and a label.
fn take_in(rig: &mut Rig, credential: Option<&str>, secret: &str, label: &str) -> Result<Value, Error> {
    rig.call(
        credential,
        "/v1/team/take-in",
        json!({"join_secret": secret, "team_name": "ops", "human_label": label}),
    )
}

/// The member a take-in or a join by a new principal made.
fn member_of(result: &Value) -> Member {
    Member {
        credential: text(result, "credential"),
        principal_ref: text(result, "principal_ref"),
        team_ref: text(result, "team_ref"),
    }
}

/// Every entry of the log, read back from its bytes and applied to an empty state.
fn replayed(rig: &Rig) -> State {
    let mut state = State::default();
    for entry in &rig.log {
        state.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
    }
    state
}

#[test]
fn a_team_taken_in_by_request_is_owned_by_its_taker_in_the_entry_that_takes_it_in() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let taken = take_in(&mut rig, None, &secret, "alice").unwrap();
    // The answer: the team under the reference inside the secret, the caller's principal and its
    // credential, that the team was taken in now, and that the caller owns it.
    assert_eq!(
        members_of(&taken),
        [
            "credential",
            "owner",
            "principal_ref",
            "taken_in",
            "team_name",
            "team_ref"
        ]
    );
    assert_eq!(
        (
            &taken["team_ref"],
            &taken["team_name"],
            &taken["taken_in"],
            &taken["owner"]
        ),
        (&json!(ELSEWHERE), &json!("ops"), &json!(true), &json!(true))
    );
    let alice = member_of(&taken);
    // One entry holds all of it: the principal, the team with its owner and the secret's hash, and the
    // membership. There is no entry after which the team is here and has no owner.
    assert_eq!(rig.log.len(), 1);
    let records = &rig.log[0].records;
    assert_eq!(records.len(), 3);
    assert!(matches!(
        &records[0].event,
        Event::CredentialIssued { principal_ref, .. } if *principal_ref == alice.principal_ref
    ));
    let created = Event::TeamCreated {
        team_ref: ELSEWHERE.into(),
        team_name: "ops".into(),
        join_secret_hash: sha256_hex(&secret),
        created_by: alice.principal_ref.clone(),
        taken_in: true,
    };
    assert_eq!(records[1], Record::new(rig.now, created));
    // A request that carries no retry key writes the record a take-in always wrote, and the team keeps
    // nothing for a request made again.
    let joined = Event::MemberJoined {
        team_ref: ELSEWHERE.into(),
        principal_ref: alice.principal_ref.clone(),
        human_label: "alice".into(),
        by_move_ticket: false,
        retry_key_hash: String::new(),
    };
    assert_eq!(records[2], Record::new(rig.now, joined));
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None);
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    assert_eq!(active(&mut rig, &alice), [alice.principal_ref.as_str()]);
    // The state and the log keep the hash and never the secret.
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!stored.contains("kept-from-the-other-backend"));
    assert!(!logged.contains("kept-from-the-other-backend"));

    // The team's members join with the secret they have, as members, and the taker administers the team
    // as the owner of one that was created here does.
    rig.advance(5);
    let bob = rig.join(&secret, "bob");
    assert_eq!(bob.team_ref, ELSEWHERE);
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    let uniform = wire(&Error::not_member());
    for (path, body) in administration(ELSEWHERE, &alice.principal_ref) {
        assert_eq!(wire(&rig.err(&bob, path, body)), uniform, "{path}");
    }
    let rotated = rotate(&mut rig, &alice);
    assert_eq!(rotated["team_ref"], json!(ELSEWHERE));
    assert_eq!(rotated["secret_version"], json!(2));

    // A caller the server knows takes a team in as the principal it is, and is issued no second
    // credential. The team counts against it as one it created does.
    let other = secret_of("1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e", "of-a-second-team");
    let second = take_in(&mut rig, Some(&alice.credential), &other, "alice").unwrap();
    assert_eq!(
        members_of(&second),
        ["owner", "principal_ref", "taken_in", "team_name", "team_ref"]
    );
    assert_eq!(second["principal_ref"], json!(alice.principal_ref));
    assert_eq!(rig.log.last().unwrap().records.len(), 2);
    assert_eq!(
        rig.state.principals[&alice.principal_ref].teams_created_at.len(),
        2
    );

    // A restart and a snapshot: the team that was taken in is there, and is owned.
    assert_eq!(replayed(&rig), rig.state);
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    let read: State = serde_json::from_slice(&snapshot).unwrap();
    assert_eq!(read, rig.state);
    assert_eq!(read.teams[ELSEWHERE].owner(), Some(alice.principal_ref.as_str()));
}

#[test]
fn a_take_in_of_a_team_that_is_here_is_a_join_by_the_rules_a_join_has() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let alice = member_of(&take_in(&mut rig, None, &secret, "alice").unwrap());

    // The same request again, by the caller that took the team in: the team is here, so nothing is taken
    // in, and the caller is the member and the owner it was. The name the request carries is not read.
    rig.advance(5);
    let entries = rig.log.len();
    let again = rig
        .call(
            Some(&alice.credential),
            "/v1/team/take-in",
            json!({"join_secret": secret, "team_name": "renamed"}),
        )
        .unwrap();
    assert_eq!(
        again,
        json!({
            "team_ref": ELSEWHERE, "team_name": "ops", "principal_ref": alice.principal_ref,
            "taken_in": false, "owner": true,
        })
    );
    // It is what a join of an active member is: one record, the membership again.
    assert_eq!(rig.log.len(), entries + 1);
    assert!(matches!(
        &rig.log[entries].records[..],
        [Record {
            event: Event::MemberJoined {
                by_move_ticket: false,
                ..
            },
            ..
        }]
    ));
    assert_eq!(rig.state.teams[ELSEWHERE].team_name, "ops");
    assert_eq!(active(&mut rig, &alice), [alice.principal_ref.as_str()]);

    // A caller with no credential the server knows, as one whose first answer was lost is, is a new
    // principal: it joins as a member, and the team keeps the owner it has.
    let lost = take_in(&mut rig, None, &secret, "alice again").unwrap();
    assert_eq!(
        (&lost["taken_in"], &lost["owner"]),
        (&json!(false), &json!(false))
    );
    assert_ne!(lost["principal_ref"], json!(alice.principal_ref));
    assert!(lost["credential"].is_string());
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    let bob = member_of(&lost);
    let refused = rig.err(&bob, "/v1/team/close-move", json!({"team_ref": ELSEWHERE}));
    assert_eq!(wire(&refused), wire(&Error::not_member()));

    // On a team the operator took in, which has no owner, the rule of the first join holds: the first
    // caller whose take-in may own it does, in the entry that makes it a member, and the next does not.
    let imported = "2d2d2d2d-2d2d-4d2d-8d2d-2d2d2d2d2d2d";
    let held = secret_of(imported, "of-the-team-the-operator-took-in");
    rig.import("legacy", &held).unwrap();
    let first = take_in(&mut rig, None, &held, "carol").unwrap();
    assert_eq!(
        (&first["taken_in"], &first["owner"], &first["team_name"]),
        (&json!(false), &json!(true), &json!("legacy"))
    );
    let carol = member_of(&first);
    assert!(matches!(
        &rig.log.last().unwrap().records[..],
        [_, _, Record { event: Event::TeamOwnerChanged { principal_ref, .. }, .. }]
            if *principal_ref == carol.principal_ref
    ));
    let next = take_in(&mut rig, None, &held, "dave").unwrap();
    assert_eq!(
        (&next["taken_in"], &next["owner"]),
        (&json!(false), &json!(false))
    );

    // A team's move ticket is let in as a join lets it in, and never owns: on a team with no owner the
    // team still has none after it.
    let ownerless = "3c3c3c3c-3c3c-4c3c-8c3c-3c3c3c3c3c3c";
    let kept = secret_of(ownerless, "of-a-team-with-no-owner-yet");
    rig.import("moving", &kept).unwrap();
    let ticket = text(&rig.open_move(ownerless, 30).unwrap(), "ticket");
    let followed = take_in(&mut rig, None, &ticket, "erin").unwrap();
    assert_eq!(
        (&followed["taken_in"], &followed["owner"]),
        (&json!(false), &json!(false))
    );
    assert_eq!(rig.state.teams[ownerless].owner(), None);
    assert!(matches!(
        &rig.log.last().unwrap().records[..],
        [
            _,
            Record {
                event: Event::MemberJoined {
                    by_move_ticket: true,
                    ..
                },
                ..
            }
        ]
    ));

    // A team that was created here is held to the same: its own secret is a join.
    let (creator, created) = rig.create("creator");
    let joined = take_in(&mut rig, None, &created, "frank").unwrap();
    assert_eq!(
        (&joined["team_ref"], &joined["taken_in"], &joined["owner"]),
        (&json!(creator.team_ref), &json!(false), &json!(false))
    );
    assert_eq!(replayed(&rig), rig.state);
}

#[test]
fn a_take_in_under_another_secret_is_a_conflict_and_changes_nothing() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let alice = member_of(&take_in(&mut rig, None, &secret, "alice").unwrap());
    let bob = rig.join(&secret, "bob");
    let (before, entries) = (rig.state.clone(), rig.log.len());

    // Another secret for the reference is refused in the words the operator's command is refused in,
    // without a word of either secret, and nothing is made: no principal, no member, no entry.
    let other = secret_of(ELSEWHERE, "another-secret-for-the-reference");
    let conflict = take_in(&mut rig, None, &other, "mallory").unwrap_err();
    assert_eq!(
        (conflict.code, reason(&conflict)),
        (Code::Conflict, "other_join_secret")
    );
    assert_eq!(
        conflict.message,
        "a team of that reference is here under another join secret"
    );
    assert_eq!(wire(&conflict), wire(&rig.import("ops", &other).unwrap_err()));
    let told = wire(&conflict);
    assert!(!told.contains("another-secret-for-the-reference"));
    assert!(!told.contains("kept-from-the-other-backend"));
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // A member of the team, and its owner, are told the same for another secret.
    for member in [&alice, &bob] {
        let refused = take_in(&mut rig, Some(&member.credential), &other, "").unwrap_err();
        assert_eq!(wire(&refused), told);
    }
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // The team opens to the secret it was taken in with, and to no other.
    assert_eq!(join_refused(&mut rig, &other), wire(&Error::secret_rejected()));

    // A principal the owner banned is told what another secret is told, although its secret is right:
    // the answer does not say that it is banned.
    revoke(&mut rig, &alice, &bob.principal_ref, Some(true)).unwrap();
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let banned = take_in(&mut rig, Some(&bob.credential), &secret, "bob").unwrap_err();
    assert_eq!(wire(&banned), told);
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));

    // A take-in that is refused for its secret is a failed join of a principal the server knows, and is
    // counted as one: after as many as a principal may fail, its next is not heard, the right secret
    // included. A caller with no credential is held by none of it.
    let (carol, _) = rig.create("carol");
    for _ in 0..limits::FAILED_JOINS_PER_WINDOW {
        rig.advance(60);
        let refused = take_in(&mut rig, Some(&carol.credential), &other, "carol").unwrap_err();
        assert_eq!(wire(&refused), told);
    }
    rig.advance(60);
    let limited = take_in(&mut rig, Some(&carol.credential), &secret, "carol").unwrap_err();
    assert_eq!(
        (limited.code, reason(&limited)),
        (Code::RateLimited, "join_attempts")
    );
    assert_eq!(
        take_in(&mut rig, None, &other, "stranger").unwrap_err().code,
        Code::Conflict
    );
    // A team that is not here is taken in by that principal all the same: nothing of a secret is tried.
    let elsewhere = secret_of(
        "4b4b4b4b-4b4b-4b4b-8b4b-4b4b4b4b4b4b",
        "of-a-team-that-is-not-here",
    );
    let taken = take_in(&mut rig, Some(&carol.credential), &elsewhere, "carol").unwrap();
    assert_eq!(
        (&taken["taken_in"], &taken["owner"]),
        (&json!(true), &json!(true))
    );
}

#[test]
fn a_take_in_is_held_to_the_creation_rule_and_to_the_shape_of_its_request() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let (stranger, created) = rig.create("stranger");

    // A server that asks for a creation token refuses a take-in as it refuses a creation, before it reads
    // any member of the request: the bytes are the same for a request that reads, for one that does not,
    // for a team that is not here and for one that is, under the right secret or another.
    rig.may_create = false;
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let refused = wire(&rig.call(None, "/v1/team/create", json!({})).unwrap_err());
    assert_eq!(refused, wire(&Error::create_refused()));
    for body in [
        json!({}),
        json!({"join_secret": secret, "team_name": "ops"}),
        json!({"join_secret": created, "team_name": "ops"}),
        json!({"join_secret": secret_of(&stranger.team_ref, "not-its-secret"), "team_name": "ops"}),
    ] {
        for credential in [None, Some(stranger.credential.as_str())] {
            let error = rig
                .call(credential, "/v1/team/take-in", body.clone())
                .unwrap_err();
            assert_eq!(wire(&error), refused, "{body}");
        }
    }
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // The token gates putting a team on the server and nothing else: a join needs the team's secret alone.
    rig.join(&created, "joiner");
    rig.may_create = true;

    // The shape is a join's with a creation's name: the secret is there, the name keeps the rule a
    // created team's name is held to, the label is within its cap, and then the secret is one.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let field = |error: &Error| error.details.get("field").cloned().unwrap_or_default();
    for (body, named, why) in [
        (json!({"team_name": "ops"}), "join_secret", "required"),
        (
            json!({"join_secret": "", "team_name": "ops"}),
            "join_secret",
            "required",
        ),
        (json!({"join_secret": secret}), "team_name", "required"),
        (
            json!({"join_secret": secret, "team_name": ""}),
            "team_name",
            "required",
        ),
        (
            json!({"join_secret": secret, "team_name": "é".repeat(65)}),
            "team_name",
            "too_long",
        ),
        (
            json!({"join_secret": secret, "team_name": "ops", "human_label": "l".repeat(129)}),
            "human_label",
            "too_long",
        ),
        (
            json!({"join_secret": "not-a-join-secret", "team_name": "ops"}),
            "join_secret",
            "invalid_value",
        ),
        // Two things wrong: the name is told before the secret's format.
        (
            json!({"join_secret": "not-a-join-secret"}),
            "team_name",
            "required",
        ),
    ] {
        let error = rig.call(None, "/v1/team/take-in", body.clone()).unwrap_err();
        assert_eq!(error.code, Code::InvalidInput, "{body}");
        assert_eq!((field(&error).as_str(), reason(&error)), (named, why), "{body}");
        assert!(!wire(&error).contains("not-a-join-secret"), "{body}");
    }
    // A text that is not a join secret is the refusal a join gives one.
    let malformed = rig
        .call(None, "/v1/team/join", json!({"join_secret": "nope"}))
        .unwrap_err();
    let taken = rig
        .call(
            None,
            "/v1/team/take-in",
            json!({"join_secret": "nope", "team_name": "ops"}),
        )
        .unwrap_err();
    assert_eq!(taken, malformed);
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // A name at the cap is taken, and a member the request carries beside what is read is not read.
    let at_cap = "é".repeat(64);
    let taken = rig
        .call(
            None,
            "/v1/team/take-in",
            json!({"join_secret": secret, "team_name": at_cap, "backend": {"url": "x"}, "more": 1}),
        )
        .unwrap();
    assert_eq!(taken["team_name"], json!(at_cap));

    // The reference inside the secret is the caller's choice, and the server keeps it: in its state, in
    // every record of the team and in every snapshot. It is held to a cap of the server's own, so a caller
    // the server does not know cannot have it keep a megabyte with one request. A reference at the cap is
    // taken in, counted in code points; one over it is refused in fixed words, and nothing is made.
    let longest = "é".repeat(limits::MAX_TAKEN_IN_TEAM_REF_CHARS);
    let taken = take_in(&mut rig, None, &secret_of(&longest, "s"), "at the cap").unwrap();
    assert_eq!(
        (&taken["team_ref"], &taken["taken_in"]),
        (&json!(longest), &json!(true))
    );
    let (before, entries) = (rig.state.clone(), rig.log.len());
    for over in [
        "r".repeat(limits::MAX_TAKEN_IN_TEAM_REF_CHARS + 1),
        "r".repeat(900_000),
    ] {
        let refused = take_in(&mut rig, None, &secret_of(&over, "s"), "over").unwrap_err();
        assert_eq!(
            (refused.code, field(&refused).as_str(), reason(&refused)),
            (Code::InvalidInput, "join_secret", "invalid_value"),
            "{} code points",
            over.len()
        );
        assert!(wire(&refused).len() < 400, "the refusal repeats the reference");
        assert!(!wire(&refused).contains("rrrr"));
    }
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // The cap is a take-in's. A join keeps nothing of what it presents, and is answered as it always was.
    let over = "r".repeat(limits::MAX_TAKEN_IN_TEAM_REF_CHARS + 1);
    assert_eq!(
        join_refused(&mut rig, &secret_of(&over, "s")),
        wire(&Error::secret_rejected())
    );

    // A principal adds a bounded number of teams in an hour, created or taken in: the take-in is refused
    // where a creation would be, and a team that was taken in counts as one that was created.
    let mut rig = Rig::new();
    let (alice, _) = rig.create("alice");
    for n in 1..limits::TEAMS_CREATED_PER_HOUR {
        rig.advance(60);
        let held = secret_of(&format!("team-{n}"), "kept-from-the-other-backend");
        let taken = take_in(&mut rig, Some(&alice.credential), &held, "alice").unwrap();
        assert_eq!(taken["taken_in"], json!(true));
    }
    rig.advance(60);
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let one_more = secret_of("team-too-many", "kept-from-the-other-backend");
    let limited = take_in(&mut rig, Some(&alice.credential), &one_more, "alice").unwrap_err();
    assert_eq!(
        (limited.code, reason(&limited)),
        (Code::RateLimited, "team_create")
    );
    assert_eq!(
        reason(&another_team(&mut rig, &alice).unwrap_err()),
        "team_create"
    );
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // A take-in of a team that is here is a join, and no bound on teams holds it.
    let here = secret_of("team-1", "kept-from-the-other-backend");
    assert_eq!(
        take_in(&mut rig, Some(&alice.credential), &here, "alice").unwrap()["taken_in"],
        json!(false)
    );

    // A caller that may not own, a gateway inside the server, takes a team in with no owner, as the
    // operator does, and the first member whose join may own it does.
    let mut rig = Rig::new();
    rig.may_own = false;
    let taken = take_in(&mut rig, None, &secret, "gateway").unwrap();
    assert_eq!(
        (&taken["taken_in"], &taken["owner"]),
        (&json!(true), &json!(false))
    );
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    assert!(matches!(
        &rig.log[0].records[1].event,
        Event::TeamCreated { created_by, taken_in: true, .. } if created_by.is_empty()
    ));
    // Such a caller's take-in of a team that is here is a join as well, and is held to the secret as any
    // is: another secret lets nobody in and makes nothing, and the right one makes a member and no owner.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let other = secret_of(ELSEWHERE, "another-secret-for-the-reference");
    let refused = take_in(&mut rig, None, &other, "gateway").unwrap_err();
    assert_eq!(
        (refused.code, reason(&refused)),
        (Code::Conflict, "other_join_secret")
    );
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    let again = take_in(&mut rig, None, &secret, "gateway").unwrap();
    assert_eq!(
        (&again["taken_in"], &again["owner"]),
        (&json!(false), &json!(false))
    );
    assert_eq!(rig.state.teams[ELSEWHERE].owner(), None);
    assert!(matches!(
        &rig.log.last().unwrap().records[..],
        [
            _,
            Record {
                event: Event::MemberJoined { .. },
                ..
            }
        ]
    ));
    rig.may_own = true;
    let owner = rig.join(&secret, "alice");
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(owner.principal_ref.as_str())
    );
}

/// The two requests of a team's move, each with a body the team's owner would be answered for.
fn move_requests(team_ref: &str) -> [(&'static str, Value); 3] {
    [
        ("/v1/team/open-move", json!({"team_ref": team_ref})),
        (
            "/v1/team/open-move",
            json!({"team_ref": team_ref, "ticket_days": 7}),
        ),
        ("/v1/team/close-move", json!({"team_ref": team_ref})),
    ]
}

#[test]
fn only_the_owner_opens_and_closes_a_move_by_request() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let alice = member_of(&take_in(&mut rig, None, &secret, "alice").unwrap());
    let bob = rig.join(&secret, "bob");
    let carol = rig.join(&secret, "carol");
    let (stranger, _) = rig.create("stranger");
    rig.ok(&carol, "/v1/team/leave", json!({"team_ref": ELSEWHERE}));
    let uniform = wire(&Error::not_member());
    let rejected = wire(&Error::secret_rejected());
    let (before, entries) = (rig.state.clone(), rig.log.len());

    for (path, body) in move_requests(ELSEWHERE) {
        // An active member that is not the owner, a member that left, and a member of another team.
        for caller in [&bob, &carol, &stranger] {
            let refused = rig.err(caller, path, body.clone());
            assert_eq!(refused.code, Code::Unauthorized, "{path}");
            assert_eq!(wire(&refused), uniform, "{path}");
        }
        // A team that does not exist, whoever asks.
        let mut nowhere = body.clone();
        nowhere["team_ref"] = json!("no-such-team");
        for caller in [&alice, &bob] {
            assert_eq!(wire(&rig.err(caller, path, nowhere.clone())), uniform, "{path}");
        }
        // No credential, and one the server does not know, are told so before anything else.
        for credential in [None, Some("not-a-credential")] {
            let refused = rig.call(credential, path, body.clone()).unwrap_err();
            assert_eq!(refused.code, Code::Unauthenticated, "{path}");
        }
    }
    // The owner is checked before the request's shape: a caller that is not the owner is told the same
    // whatever number of days it asked for, where the owner is told what is wrong with the number.
    for days in [json!(0), json!(366), json!(-1), json!("thirty"), json!(1.5)] {
        let body = json!({"team_ref": ELSEWHERE, "ticket_days": days});
        for caller in [&bob, &carol, &stranger] {
            assert_eq!(
                wire(&rig.err(caller, "/v1/team/open-move", body.clone())),
                uniform
            );
        }
        let refused = rig.err(&alice, "/v1/team/open-move", body);
        assert_eq!(
            (
                refused.code,
                refused.details["field"].as_str(),
                reason(&refused),
                refused.details["min"].as_str(),
                refused.details["max"].as_str(),
            ),
            (Code::InvalidInput, "ticket_days", "out_of_range", "1", "365"),
            "{days}"
        );
    }
    // The team is named first, as on every request that names one.
    for path in ["/v1/team/open-move", "/v1/team/close-move"] {
        let unnamed = rig.err(&bob, path, json!({}));
        assert_eq!(
            (unnamed.code, unnamed.details["field"].as_str()),
            (Code::InvalidInput, "team_ref"),
            "{path}"
        );
    }
    // Nothing of all that changed anything or made an entry, and the owner that closes a move the team
    // does not have changes nothing either.
    let none = rig.ok(&alice, "/v1/team/close-move", json!({"team_ref": ELSEWHERE}));
    assert_eq!(
        none,
        json!({"team_ref": ELSEWHERE, "team_name": "ops", "closed": false})
    );
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));

    // The owner opens the move: the answer is the operator's, the one place the ticket is, and the log
    // and the state hold its hash. Thirty days when the request names no number, and a null is none.
    rig.advance(5);
    let opened = rig.ok(
        &alice,
        "/v1/team/open-move",
        json!({"team_ref": ELSEWHERE, "ticket_days": null}),
    );
    let ticket = text(&opened, "ticket");
    let ends_at = rig.now.plus_seconds(30 * DAY);
    assert_eq!(
        opened,
        json!({
            "team_ref": ELSEWHERE, "team_name": "ops", "ticket": ticket, "ends_at": ends_at.rfc3339(),
            "replaced": false,
        })
    );
    let random = ticket
        .strip_prefix(&format!("brg1.{ELSEWHERE}."))
        .expect("a ticket for the same team_ref");
    assert_eq!(random.len(), 32);
    let recorded = Event::TeamMoveOpened {
        team_ref: ELSEWHERE.into(),
        ticket_hash: sha256_hex(&ticket),
        ends_at,
        by_owner: true,
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, recorded)]);
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    assert!(!stored.contains(random) && !logged.contains(random));

    // The ticket admits a join, which makes a member like any other and never an owner: the team keeps
    // the owner it has, and the member the ticket let in administers nothing and moves nothing.
    rig.advance(5);
    let dave = rig.join(&ticket, "dave");
    assert!(matches!(
        &rig.log.last().unwrap().records[..],
        [
            _,
            Record {
                event: Event::MemberJoined {
                    by_move_ticket: true,
                    ..
                },
                ..
            }
        ]
    ));
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );
    for (path, body) in move_requests(ELSEWHERE)
        .into_iter()
        .chain(administration(ELSEWHERE, &dave.principal_ref))
    {
        assert_eq!(wire(&rig.err(&dave, path, body)), uniform, "{path}");
    }

    // A second open, for seven days, takes the place of the first and says so: the first ticket admits
    // nobody from then on, although it has not ended.
    rig.advance(5);
    let again = rig.ok(
        &alice,
        "/v1/team/open-move",
        json!({"team_ref": ELSEWHERE, "ticket_days": 7}),
    );
    let second = text(&again, "ticket");
    assert_eq!(
        (&again["replaced"], text(&again, "ends_at")),
        (&json!(true), rig.now.plus_seconds(7 * DAY).rfc3339())
    );
    assert!(second != ticket);
    assert_eq!(join_refused(&mut rig, &ticket), rejected);
    rig.join(&second, "erin");
    // The ends of the range are taken.
    for days in [1, 365] {
        let opened = rig.ok(
            &alice,
            "/v1/team/open-move",
            json!({"team_ref": ELSEWHERE, "ticket_days": days}),
        );
        assert_eq!(
            text(&opened, "ends_at"),
            rig.now.plus_seconds(days * DAY).rfc3339()
        );
    }
    let last = text(
        &rig.ok(&alice, "/v1/team/open-move", json!({"team_ref": ELSEWHERE})),
        "ticket",
    );

    // The owner closes the move, in one record: its ticket admits nobody, and every member that came in
    // with a ticket is still one. Closing it again changes nothing and makes no entry.
    rig.advance(5);
    let entries = rig.log.len();
    let closed = rig.ok(&alice, "/v1/team/close-move", json!({"team_ref": ELSEWHERE}));
    assert_eq!(
        closed,
        json!({"team_ref": ELSEWHERE, "team_name": "ops", "closed": true})
    );
    let record = Event::TeamMoveClosed {
        team_ref: ELSEWHERE.into(),
        by_owner: true,
    };
    assert_eq!(rig.log.len(), entries + 1);
    assert_eq!(rig.log[entries].records, [Record::new(rig.now, record)]);
    assert_eq!(rig.state.teams[ELSEWHERE].move_ticket, None);
    for held in [&ticket, &second, &last] {
        assert_eq!(join_refused(&mut rig, held), rejected);
    }
    assert!(rig.state.teams[ELSEWHERE].active(&dave.principal_ref));
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let again = rig.ok(&alice, "/v1/team/close-move", json!({"team_ref": ELSEWHERE}));
    assert_eq!(again["closed"], json!(false));
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));

    // The operator's commands and the owner's requests are one move: what one opens the other closes,
    // and what one opened the other replaces.
    let by_operator = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    let replaced = rig.ok(&alice, "/v1/team/open-move", json!({"team_ref": ELSEWHERE}));
    assert_eq!(replaced["replaced"], json!(true));
    assert_eq!(join_refused(&mut rig, &by_operator), rejected);
    assert_eq!(rig.close_move(ELSEWHERE).unwrap()["closed"], json!(true));
    assert_eq!(join_refused(&mut rig, &text(&replaced, "ticket")), rejected);
    rig.open_move(ELSEWHERE, 30).unwrap();
    let closed = rig.ok(&alice, "/v1/team/close-move", json!({"team_ref": ELSEWHERE}));
    assert_eq!(closed["closed"], json!(true));

    // An owner that left is no active member and is refused like anyone, and a team the operator took
    // in, which has no owner yet, has nobody its move answers.
    rig.ok(&alice, "/v1/team/leave", json!({"team_ref": ELSEWHERE}));
    for (path, body) in move_requests(ELSEWHERE) {
        assert_eq!(wire(&rig.err(&alice, path, body)), uniform, "{path}");
    }
    let ownerless = "3c3c3c3c-3c3c-4c3c-8c3c-3c3c3c3c3c3c";
    let kept = secret_of(ownerless, "of-a-team-with-no-owner-yet");
    rig.import("moving", &kept).unwrap();
    rig.may_own = false;
    let gateway = rig.join(&kept, "gateway");
    rig.may_own = true;
    for (path, body) in move_requests(ownerless) {
        assert_eq!(wire(&rig.err(&gateway, path, body)), uniform, "{path}");
    }

    // A restart: every entry, read back from its bytes, makes the same state.
    assert_eq!(replayed(&rig), rig.state);
}

#[test]
fn a_move_request_that_was_already_read_is_still_its_owners_alone() {
    // `execute` answers a request that was read already. It checks the owner itself, as each of the three
    // administration requests does, so a caller that reached it past `answer` moves nothing.
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let alice = member_of(&take_in(&mut rig, None, &secret, "alice").unwrap());
    let bob = rig.join(&secret, "bob");
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let body = json!({"team_ref": ELSEWHERE}).to_string();
    for path in ["/v1/team/open-move", "/v1/team/close-move"] {
        let command = Command::parse(path, body.as_bytes()).unwrap();
        for caller in [Some(sha256_hex(&bob.credential)), None] {
            let refused = execute(
                &mut rig.state,
                &mut rig.failures,
                caller.as_deref(),
                true,
                &command,
                rig.now,
                &mut rig.entropy,
            )
            .unwrap_err();
            let expected = if caller.is_some() {
                Error::not_member()
            } else {
                Error::unknown_credential()
            };
            assert_eq!(wire(&refused), wire(&expected), "{path}");
        }
    }
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // The owner is answered by it as by a request.
    let command = Command::parse("/v1/team/open-move", body.as_bytes()).unwrap();
    let opened = execute(
        &mut rig.state,
        &mut rig.failures,
        Some(&sha256_hex(&alice.credential)),
        true,
        &command,
        rig.now,
        &mut rig.entropy,
    )
    .unwrap();
    assert_eq!(opened.result["replaced"], json!(false));
    assert!(opened.entry.is_some());
}

/// A retry key as a client mints one: 32 random bytes in base64url, here the same in every run. No file
/// of the repository holds a real one.
const KEY: &str = "Zm9yLXRoZS10YWtlLWluLW9mLXRoZS10ZXN0LW9ubHk";
/// Another key of the same form.
const OTHER_KEY: &str = "YW5vdGhlci1rZXktdGhhdC1ub2JvZHktbWludGVkLTE";

/// A take-in that carries a retry key, as a client that can make it again asks for it.
fn take_in_keyed(rig: &mut Rig, credential: Option<&str>, secret: &str, key: &str) -> Result<Value, Error> {
    rig.call(
        credential,
        "/v1/team/take-in",
        json!({"join_secret": secret, "team_name": "ops", "human_label": "alice", "retry_key": key}),
    )
}

/// Whether the server knows the credential. A request that needs one, about a team that is not there,
/// is refused either way: as a credential nobody has, or as a principal that is no member of that team.
fn known(rig: &mut Rig, credential: &str) -> bool {
    let refused = rig
        .call(
            Some(credential),
            "/v1/team/members",
            json!({"team_ref": "no-such-team"}),
        )
        .unwrap_err();
    if wire(&refused) == wire(&Error::unknown_credential()) {
        return false;
    }
    assert_eq!(wire(&refused), wire(&Error::not_member()));
    true
}

#[test]
fn a_take_in_made_again_with_its_key_is_answered_for_the_principal_the_first_one_made() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let team = json!({"team_ref": ELSEWHERE});

    // The first request, with a key: the answer a take-in always had, and the hash of the key in the
    // record that makes the owner a member. The team keeps the hash and the principal.
    let taken = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        (&taken["taken_in"], &taken["owner"]),
        (&json!(true), &json!(true))
    );
    let alice = member_of(&taken);
    let joined = Event::MemberJoined {
        team_ref: ELSEWHERE.into(),
        principal_ref: alice.principal_ref.clone(),
        human_label: "alice".into(),
        by_move_ticket: false,
        retry_key_hash: sha256_hex(KEY),
    };
    assert_eq!(rig.log[0].records.len(), 3);
    assert_eq!(rig.log[0].records[2], Record::new(rig.now, joined));
    let kept = rig.state.teams[ELSEWHERE].take_in_retry.clone().unwrap();
    assert_eq!(
        (kept.key_hash.as_str(), kept.principal_ref.as_str()),
        (sha256_hex(KEY).as_str(), alice.principal_ref.as_str())
    );
    // The state, a snapshot of it and the log hold the hash, and never the key.
    let stored = serde_json::to_string(&rig.state).unwrap();
    let logged = String::from_utf8(rig.log.iter().flat_map(Entry::encode).collect()).unwrap();
    for written in [&stored, &logged] {
        assert!(written.contains(&sha256_hex(KEY)), "the hash is what is kept");
        assert!(!written.contains(KEY), "the key is kept in clear");
    }
    // Positive control: the credential the first answer carried is one the server knows.
    assert!(known(&mut rig, &alice.credential));
    let bob = rig.join(&secret, "bob");
    let principals = rig.state.principals.len();

    // The answer was lost: the same request again, with the secret, the key and no credential. It is
    // answered for the principal the first one made, which owns the team, with a new credential. Nothing
    // is taken in, and nobody new is made.
    rig.advance(5);
    let entries = rig.log.len();
    let again = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        members_of(&again),
        [
            "credential",
            "owner",
            "principal_ref",
            "taken_in",
            "team_name",
            "team_ref"
        ]
    );
    assert_eq!(
        (
            &again["principal_ref"],
            &again["taken_in"],
            &again["owner"],
            &again["team_name"]
        ),
        (
            &json!(alice.principal_ref),
            &json!(false),
            &json!(true),
            &json!("ops")
        )
    );
    let second = text(&again, "credential");
    assert_ne!(second, alice.credential);
    assert_eq!(rig.state.principals.len(), principals);
    // One entry: the lost credential is unknown, the new one is issued, and the member is the member
    // it was, with the key's hash once more. A credential made unknown ends a key, and the join keeps
    // it for the answer that may be lost in its turn: the team keeps what it kept.
    assert_eq!(rig.log.len(), entries + 1);
    let records: Vec<&Event> = rig.log[entries]
        .records
        .iter()
        .map(|record| &record.event)
        .collect();
    assert_eq!(
        records,
        [
            &Event::CredentialRevoked {
                credential_hash: sha256_hex(&alice.credential),
            },
            &Event::CredentialIssued {
                principal_ref: alice.principal_ref.clone(),
                credential_hash: sha256_hex(&second),
            },
            &Event::MemberJoined {
                team_ref: ELSEWHERE.into(),
                principal_ref: alice.principal_ref.clone(),
                human_label: "alice".into(),
                by_move_ticket: false,
                retry_key_hash: sha256_hex(KEY),
            },
        ]
    );
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, Some(kept.clone()));
    assert_eq!(rig.state.principals[&alice.principal_ref].retry_key_in, ELSEWHERE);
    // The credential the lost answer carried admits nobody from then on, and the new one is the owner's:
    // the server knows it for the principal that owns the team. Nothing is asked with it here that
    // leaves a record: the first such request ends the key, which a test of its own holds.
    let refused = rig
        .call(Some(&alice.credential), "/v1/team/open-move", team.clone())
        .unwrap_err();
    assert_eq!(wire(&refused), wire(&Error::unknown_credential()));
    assert!(
        !known(&mut rig, &alice.credential),
        "the lost credential is known"
    );
    let owner = Member {
        credential: second.clone(),
        principal_ref: alice.principal_ref.clone(),
        team_ref: ELSEWHERE.into(),
    };
    assert_eq!(
        rig.state.principal_of(&sha256_hex(&second)),
        rig.state.teams[ELSEWHERE].owner()
    );
    assert_eq!(
        wire(&rig.err(&bob, "/v1/team/open-move", team.clone())),
        wire(&Error::not_member())
    );
    assert_eq!(
        active(&mut rig, &owner),
        [alice.principal_ref.clone(), bob.principal_ref.clone()]
    );
    // The operator opens the team's move from the command line. That is nobody's request, and the
    // team keeps its key.
    let ticket = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, Some(kept.clone()));

    // The second answer may be lost as well: the key is honoured again, and the credential before it
    // is unknown in its turn. The request carries the credential of the first answer, as a client
    // that still had it in its file would: a credential the server does not know is no credential.
    let third = text(
        &take_in_keyed(&mut rig, Some(&alice.credential), &secret, KEY).unwrap(),
        "credential",
    );
    assert_ne!(third, second);
    assert!(known(&mut rig, &third));
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, Some(kept.clone()));
    let refused = rig
        .call(Some(&second), "/v1/team/members", team.clone())
        .unwrap_err();
    assert_eq!(wire(&refused), wire(&Error::unknown_credential()));

    // Another key, and no key, with the right secret: the join of a new principal, as it always was.
    // The two answers have the same members and say the same, and neither says that the team keeps a
    // key. The owner's credential is the one it was.
    let (before, entries) = (rig.state.teams[ELSEWHERE].take_in_retry.clone(), rig.log.len());
    let wrong = take_in_keyed(&mut rig, None, &secret, OTHER_KEY).unwrap();
    let none = take_in(&mut rig, None, &secret, "alice").unwrap();
    for stranger in [&wrong, &none] {
        assert_eq!(members_of(stranger), members_of(&again));
        assert_eq!(
            (&stranger["taken_in"], &stranger["owner"]),
            (&json!(false), &json!(false))
        );
        assert_ne!(stranger["principal_ref"], json!(alice.principal_ref));
        assert!(matches!(
            &rig.log[entries].records[..],
            [
                Record {
                    event: Event::CredentialIssued { .. },
                    ..
                },
                Record {
                    event: Event::MemberJoined { retry_key_hash, .. },
                    ..
                }
            ] if retry_key_hash.is_empty()
        ));
    }
    assert_ne!(wrong["principal_ref"], none["principal_ref"]);
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, before);
    assert!(known(&mut rig, &third));
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(alice.principal_ref.as_str())
    );

    // The key with another secret is the conflict another secret always was, byte for byte, and makes
    // nothing: the key opens nothing without the team's secret.
    let other = secret_of(ELSEWHERE, "another-secret-for-the-reference");
    let (before, entries) = (rig.state.clone(), rig.log.len());
    let conflict = take_in_keyed(&mut rig, None, &other, KEY).unwrap_err();
    assert_eq!(
        (conflict.code, reason(&conflict)),
        (Code::Conflict, "other_join_secret")
    );
    assert_eq!(
        wire(&conflict),
        wire(&take_in(&mut rig, None, &other, "alice").unwrap_err())
    );
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));

    // The ticket of the team's move is not its join secret: with the key it is the join a ticket always
    // was, of a new principal that does not own.
    let followed = take_in_keyed(&mut rig, None, &ticket, KEY).unwrap();
    assert_eq!(
        (&followed["taken_in"], &followed["owner"]),
        (&json!(false), &json!(false))
    );
    assert_ne!(followed["principal_ref"], json!(alice.principal_ref));
    assert!(known(&mut rig, &third));

    // A caller the server knows is the principal its credential names, whatever key it carries: the key
    // gives a member nothing of the owner's.
    let entries = rig.log.len();
    let member = take_in_keyed(&mut rig, Some(&bob.credential), &secret, KEY).unwrap();
    assert_eq!(
        member,
        json!({
            "team_ref": ELSEWHERE, "team_name": "ops", "principal_ref": bob.principal_ref,
            "taken_in": false, "owner": false,
        })
    );
    assert_eq!(rig.log[entries].records.len(), 1);
    assert!(known(&mut rig, &third));

    // A restart and a snapshot: the key's hash is where it was, and the request made again is answered
    // as it was before either.
    assert_eq!(replayed(&rig), rig.state);
    let snapshot = serde_json::to_vec(&rig.state).unwrap();
    let read: State = serde_json::from_slice(&snapshot).unwrap();
    assert_eq!(read, rig.state);
    assert_eq!(read.teams[ELSEWHERE].take_in_retry, Some(kept));
    rig.state = replayed(&rig);
    let after = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        (&after["principal_ref"], &after["owner"]),
        (&json!(alice.principal_ref), &json!(true))
    );
    assert_eq!(replayed(&rig), rig.state);
}

/// One request of the owner a take-in made, asked with its credential: what it does on the rig, with
/// the member beside the owner and the team's join secret at hand, and the join secret the team has
/// after it.
type OwnersRequest = fn(&mut Rig, &Member, &Member, &str) -> String;

#[test]
fn a_retry_key_ends_with_the_first_record_that_its_owners_own_request_makes() {
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    // Each leaves a record, and each is the owner's own: from that record on the owner holds a
    // credential, and the key has nothing left to stand in for.
    let requests: [(&str, OwnersRequest); 12] = [
        ("registers a session", |rig, owner, _, secret| {
            rig.register(owner, "alpha");
            secret.to_owned()
        }),
        ("makes its credential unknown", |rig, owner, _, secret| {
            rig.ok(owner, "/v1/credential/revoke", json!({}));
            secret.to_owned()
        }),
        ("opens the team's move", |rig, owner, _, secret| {
            rig.ok(owner, "/v1/team/open-move", json!({"team_ref": ELSEWHERE}));
            secret.to_owned()
        }),
        ("closes the move the operator opened", |rig, owner, _, secret| {
            // The operator's command is nobody's request: the team keeps its key through it.
            rig.open_move(ELSEWHERE, 30).unwrap();
            assert!(rig.state.teams[ELSEWHERE].take_in_retry.is_some());
            rig.ok(owner, "/v1/team/close-move", json!({"team_ref": ELSEWHERE}));
            secret.to_owned()
        }),
        ("revokes a member", |rig, owner, member, secret| {
            revoke(rig, owner, &member.principal_ref, None).unwrap();
            secret.to_owned()
        }),
        ("revokes the members of a secret", |rig, owner, _, secret| {
            assert_eq!(revoke_up_to(rig, owner, 1).unwrap()["revoked"], json!(1));
            secret.to_owned()
        }),
        ("transfers the team", |rig, owner, member, secret| {
            rig.ok(
                owner,
                "/v1/team/transfer",
                json!({"team_ref": ELSEWHERE, "principal_ref": member.principal_ref}),
            );
            secret.to_owned()
        }),
        ("leaves the team", |rig, owner, _, secret| {
            rig.ok(owner, "/v1/team/leave", json!({"team_ref": ELSEWHERE}));
            secret.to_owned()
        }),
        (
            "joins the team again with its credential",
            |rig, owner, _, secret| {
                rig.ok(owner, "/v1/team/join", json!({"join_secret": secret}));
                secret.to_owned()
            },
        ),
        (
            "takes the team in again with its credential",
            |rig, owner, _, secret| {
                take_in_keyed(rig, Some(&owner.credential), secret, KEY).unwrap();
                secret.to_owned()
            },
        ),
        ("creates a team", |rig, owner, _, secret| {
            rig.ok(owner, "/v1/team/create", json!({"team_name": "second"}));
            secret.to_owned()
        }),
        ("rotates the team's secret", |rig, owner, _, _| {
            text(&rotate(rig, owner), "join_secret")
        }),
    ];
    for (what, request) in requests {
        let mut rig = Rig::new();
        let first = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
        let member = rig.join(&secret, "bob");
        // The positive control: until the owner asks anything, the take-in is made again. The owner
        // holds the credential of that answer.
        let again = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
        assert_eq!(again["principal_ref"], json!(first.principal_ref), "{what}");
        let owner = Member {
            credential: text(&again, "credential"),
            ..first
        };
        assert!(rig.state.teams[ELSEWHERE].take_in_retry.is_some(), "{what}");
        assert_eq!(
            rig.state.principals[&owner.principal_ref].retry_key_in, ELSEWHERE,
            "{what}"
        );

        let entries = rig.log.len();
        let secret_now = request(&mut rig, &owner, &member, &secret);
        assert!(rig.log.len() > entries, "{what}: the request left no record");
        assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None, "{what}");
        assert_eq!(
            rig.state.principals[&owner.principal_ref].retry_key_in, "",
            "{what}"
        );
        // What a snapshot keeps then names no key, and holds nothing of one.
        let stored = serde_json::to_string(&rig.state).unwrap();
        assert!(
            !stored.contains("take_in_retry")
                && !stored.contains("retry_key_in")
                && !stored.contains(&sha256_hex(KEY)),
            "{what}: {stored}"
        );

        // The same request again, with the key and the secret the team has, is the join of a new
        // principal, in the two records a stranger's join has. It keeps nothing, and it touches
        // nothing of the owner's.
        let (principals, entries) = (rig.state.principals.len(), rig.log.len());
        let stranger = take_in_keyed(&mut rig, None, &secret_now, KEY).unwrap();
        assert_eq!(
            (&stranger["taken_in"], &stranger["owner"]),
            (&json!(false), &json!(false)),
            "{what}"
        );
        assert_ne!(stranger["principal_ref"], json!(owner.principal_ref), "{what}");
        assert_eq!(rig.state.principals.len(), principals + 1, "{what}");
        assert!(
            matches!(
                &rig.log[entries].records[..],
                [
                    Record {
                        event: Event::CredentialIssued { .. },
                        ..
                    },
                    Record {
                        event: Event::MemberJoined { retry_key_hash, .. },
                        ..
                    }
                ] if retry_key_hash.is_empty()
            ),
            "{what}"
        );
        assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None, "{what}");
        assert_eq!(
            known(&mut rig, &owner.credential),
            what != "makes its credential unknown",
            "{what}"
        );
        assert_eq!(replayed(&rig), rig.state, "{what}");
    }
}

#[test]
fn a_retry_key_outlives_every_request_that_is_not_its_owners_own_or_leaves_no_record() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let team = json!({"team_ref": ELSEWHERE});
    let first = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
    let kept = rig.state.teams[ELSEWHERE].take_in_retry.clone();
    assert!(kept.is_some());
    let still = |rig: &Rig, what: &str| {
        assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, kept, "{what}");
        assert_eq!(
            rig.state.principals[&first.principal_ref].retry_key_in, ELSEWHERE,
            "{what}"
        );
    };

    // The take-in made again, and made again: an answer that is lost twice is still made again.
    take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    still(&rig, "the take-in made again");
    let again = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    still(&rig, "the take-in made again a second time");
    let owner = Member {
        credential: text(&again, "credential"),
        principal_ref: first.principal_ref.clone(),
        team_ref: ELSEWHERE.into(),
    };

    // Another principal's requests, with its own credential, whatever they are.
    let bob = rig.join(&secret, "bob");
    still(&rig, "a stranger joins");
    let session = rig.register(&bob, "beta");
    still(&rig, "a member registers a session");
    rig.ok(
        &bob,
        "/v1/session/heartbeat",
        json!({"session_id": session, "heartbeat": {"activity": "idle"}}),
    );
    rig.ok(&bob, "/v1/session/close", json!({"session_id": session}));
    still(&rig, "a member's session beats and closes");
    for key in [KEY, OTHER_KEY] {
        take_in_keyed(&mut rig, Some(&bob.credential), &secret, key).unwrap();
        still(&rig, "a member takes the team in with a key, as the member it is");
    }
    rig.ok(&bob, "/v1/team/leave", team.clone());
    still(&rig, "a member leaves");
    rig.ok(&bob, "/v1/team/join", json!({"join_secret": secret}));
    still(&rig, "a member joins again with its credential");
    rig.ok(&bob, "/v1/team/create", json!({"team_name": "bobs"}));
    still(&rig, "a member creates a team");
    rig.ok(&bob, "/v1/credential/revoke", json!({}));
    still(&rig, "a member makes its credential unknown");
    take_in_keyed(&mut rig, None, &secret, OTHER_KEY).unwrap();
    take_in(&mut rig, None, &secret, "").unwrap();
    still(&rig, "strangers take the team in with another key and with none");
    take_in_keyed(
        &mut rig,
        None,
        &secret_of(ELSEWHERE, "another-secret-for-the-reference"),
        KEY,
    )
    .unwrap_err();
    still(&rig, "the key with another secret");

    // The operator's commands are nobody's request.
    let ticket = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    still(&rig, "the operator opens the team's move");
    take_in_keyed(&mut rig, None, &ticket, KEY).unwrap();
    still(&rig, "a stranger follows with the ticket and the key");
    rig.close_move(ELSEWHERE).unwrap();
    still(&rig, "the operator closes the move");
    assert_eq!(
        rig.set_owner(ELSEWHERE, &first.principal_ref).unwrap()["changed"],
        json!(false)
    );
    rig.import("ops", &secret).unwrap();
    still(
        &rig,
        "the operator names the owner the team has, and takes the team in again",
    );

    // The owner's own requests that leave no record: what it reads, what it is refused, and what
    // changes nothing.
    let entries = rig.log.len();
    assert_eq!(active(&mut rig, &owner).len(), 5);
    rig.list(&owner, true);
    rig.err(
        &owner,
        "/v1/team/open-move",
        json!({"team_ref": ELSEWHERE, "ticket_days": 0}),
    );
    assert_eq!(
        rig.ok(&owner, "/v1/team/close-move", team.clone())["closed"],
        json!(false)
    );
    rig.ok(
        &owner,
        "/v1/team/transfer",
        json!({"team_ref": ELSEWHERE, "principal_ref": owner.principal_ref}),
    );
    assert_eq!(rig.log.len(), entries, "one of them left a record");
    still(&rig, "the owner's requests that leave no record");

    // So the take-in is made again still, for the owner the first one made.
    let honoured = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        (&honoured["principal_ref"], &honoured["owner"]),
        (&json!(first.principal_ref), &json!(true))
    );
    assert!(!known(&mut rig, &owner.credential));
    still(&rig, "the take-in made again after all of it");
    assert_eq!(replayed(&rig), rig.state);
}

/// The requests the server reads, one name for each, and [`Asks::Count`] after the last. [`asks`] has no
/// arm for anything else, so a request that is added to the server does not build here until it has a
/// name, and the test below fails until the owner of a kept key has asked it.
#[derive(Clone, Copy, Debug)]
enum Asks {
    TeamCreate,
    TeamJoin,
    TeamLeave,
    TeamMembers,
    TeamRotateSecret,
    TeamRevokeMember,
    TeamTransfer,
    TeamTakeIn,
    TeamOpenMove,
    TeamCloseMove,
    SessionRegister,
    SessionHeartbeat,
    SessionList,
    SessionClose,
    MessageSend,
    MessageReceive,
    MessageAck,
    CredentialRevoke,
    Count,
}

/// The name of a request as the server read it.
fn asks(command: &Command) -> Asks {
    match command {
        Command::TeamCreate(_) => Asks::TeamCreate,
        Command::TeamJoin(_) => Asks::TeamJoin,
        Command::TeamLeave(_) => Asks::TeamLeave,
        Command::TeamMembers(_) => Asks::TeamMembers,
        Command::TeamRotateSecret(_) => Asks::TeamRotateSecret,
        Command::TeamRevokeMember(_) => Asks::TeamRevokeMember,
        Command::TeamTransfer(_) => Asks::TeamTransfer,
        Command::TeamTakeIn(_) => Asks::TeamTakeIn,
        Command::TeamOpenMove(_) => Asks::TeamOpenMove,
        Command::TeamCloseMove(_) => Asks::TeamCloseMove,
        Command::SessionRegister(_) => Asks::SessionRegister,
        Command::SessionHeartbeat(_) => Asks::SessionHeartbeat,
        Command::SessionList(_) => Asks::SessionList,
        Command::SessionClose(_) => Asks::SessionClose,
        Command::MessageSend(_) => Asks::MessageSend,
        Command::MessageReceive(_) => Asks::MessageReceive,
        Command::MessageAck(_) => Asks::MessageAck,
        Command::CredentialRevoke => Asks::CredentialRevoke,
    }
}

/// What a request left behind it.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Leaves {
    /// It was answered, and the log has one entry more.
    ARecord,
    /// It was answered, and the log is what it was: a read, or a request that found nothing to change.
    Nothing,
    /// It was refused.
    ARefusal,
}

/// What the owner of a kept key asks: the path, the body, what it must leave, and whether the operator
/// opened the team's move before it. The body is made from the owner, the member beside it, that
/// member's session and the team's join secret.
type OwnersAsk = (
    &'static str,
    fn(&Member, &Member, &str, &str) -> Value,
    Leaves,
    bool,
);

#[test]
fn every_request_the_server_reads_ends_the_key_of_the_owner_that_asks_it_or_leaves_no_record() {
    // The rule is as whole as the list of requests it is held to, so this is the whole list: every
    // request of the contract's section 2, asked by the principal a team keeps a key for, with its own
    // credential. One that leaves a record ends the key, and one that leaves none ends nothing. And
    // no answer and no record of any of them tells that the team kept a key: a server that is the same
    // in everything but that, the twin, answers each in the same bytes and writes the same entry.
    //
    // What a session asks leaves a record that the rule does not read: a heartbeat, a close, a resume,
    // a message sent and one acknowledged. None of them is an owner's first, because a principal that a
    // team keeps a key for has no session: the request that would give it one is its own, and ends the
    // key. So each is asked here about the session of another member, which is the only kind there is,
    // and is refused.
    use Leaves::{ARecord, ARefusal, Nothing};
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let requests: [OwnersAsk; 28] = [
        (
            "/v1/team/create",
            |_, _, _, _| json!({"team_name": "second"}),
            ARecord,
            false,
        ),
        (
            "/v1/team/join",
            |_, _, _, secret| json!({"join_secret": secret}),
            ARecord,
            false,
        ),
        (
            "/v1/team/join",
            |_, _, _, _| json!({"join_secret": secret_of(ELSEWHERE, "not-the-secret-of-the-team")}),
            ARefusal,
            false,
        ),
        (
            "/v1/team/leave",
            |_, _, _, _| json!({"team_ref": ELSEWHERE}),
            ARecord,
            false,
        ),
        (
            "/v1/team/leave",
            |_, _, _, _| json!({"team_ref": "a-team-the-owner-is-not-in"}),
            Nothing,
            false,
        ),
        (
            "/v1/team/members",
            |_, _, _, _| json!({"team_ref": ELSEWHERE}),
            Nothing,
            false,
        ),
        (
            "/v1/team/rotate-secret",
            |_, _, _, _| json!({"team_ref": ELSEWHERE}),
            ARecord,
            false,
        ),
        (
            "/v1/team/revoke-member",
            |_, member, _, _| json!({"team_ref": ELSEWHERE, "principal_ref": member.principal_ref}),
            ARecord,
            false,
        ),
        (
            "/v1/team/revoke-member",
            |_, _, _, _| json!({"team_ref": ELSEWHERE, "joined_secret_version_lte": 1}),
            ARecord,
            false,
        ),
        (
            "/v1/team/revoke-member",
            |_, _, _, _| json!({"team_ref": ELSEWHERE, "principal_ref": "a-principal-the-team-never-had"}),
            Nothing,
            false,
        ),
        (
            "/v1/team/revoke-member",
            |owner, _, _, _| json!({"team_ref": ELSEWHERE, "principal_ref": owner.principal_ref}),
            ARefusal,
            false,
        ),
        (
            "/v1/team/transfer",
            |_, member, _, _| json!({"team_ref": ELSEWHERE, "principal_ref": member.principal_ref}),
            ARecord,
            false,
        ),
        (
            "/v1/team/transfer",
            |owner, _, _, _| json!({"team_ref": ELSEWHERE, "principal_ref": owner.principal_ref}),
            Nothing,
            false,
        ),
        (
            "/v1/team/take-in",
            |_, _, _, secret| json!({"join_secret": secret, "team_name": "ops", "retry_key": KEY}),
            ARecord,
            false,
        ),
        (
            "/v1/team/take-in",
            |_, _, _, _| {
                let other = secret_of(ELSEWHERE, "another-secret-for-the-reference");
                json!({"join_secret": other, "team_name": "ops", "retry_key": KEY})
            },
            ARefusal,
            false,
        ),
        (
            "/v1/team/open-move",
            |_, _, _, _| json!({"team_ref": ELSEWHERE}),
            ARecord,
            false,
        ),
        (
            "/v1/team/open-move",
            |_, _, _, _| json!({"team_ref": ELSEWHERE, "ticket_days": 0}),
            ARefusal,
            false,
        ),
        (
            "/v1/team/close-move",
            |_, _, _, _| json!({"team_ref": ELSEWHERE}),
            ARecord,
            true,
        ),
        (
            "/v1/team/close-move",
            |_, _, _, _| json!({"team_ref": ELSEWHERE}),
            Nothing,
            false,
        ),
        (
            "/v1/session/register",
            |owner, _, _, _| registration(owner, "alpha"),
            ARecord,
            false,
        ),
        (
            "/v1/session/register",
            |owner, _, session, _| {
                let mut resumed = registration(owner, "alpha");
                resumed["resume"] = json!({"session_id": session});
                resumed
            },
            ARefusal,
            false,
        ),
        (
            "/v1/session/heartbeat",
            |_, _, session, _| json!({"session_id": session, "heartbeat": {"activity": "idle"}}),
            ARefusal,
            false,
        ),
        (
            "/v1/session/list",
            |_, _, _, _| json!({"team_ref": ELSEWHERE, "include_offline": true}),
            Nothing,
            false,
        ),
        (
            "/v1/session/close",
            |_, _, session, _| json!({"session_id": session}),
            ARefusal,
            false,
        ),
        (
            "/v1/message/send",
            |_, _, session, _| json!({"sender_session_id": session, "recipient_session_id": session, "body": "x"}),
            ARefusal,
            false,
        ),
        (
            "/v1/message/receive",
            |_, _, session, _| json!({"session_id": session}),
            ARefusal,
            false,
        ),
        (
            "/v1/message/ack",
            |_, _, session, _| json!({"session_id": session, "message_ids": ["a-message"]}),
            ARefusal,
            false,
        ),
        ("/v1/credential/revoke", |_, _, _, _| json!({}), ARecord, false),
    ];
    let mut asked = [false; Asks::Count as usize];
    for (path, body, leaves, moving) in requests {
        let (mut rig, mut twin) = (Rig::new(), Rig::new());
        let owner = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
        let unkeyed = member_of(&take_in(&mut twin, None, &secret, "alice").unwrap());
        assert_eq!(owner.credential, unkeyed.credential);
        let member = rig.join(&secret, "bob");
        assert_eq!(member.credential, twin.join(&secret, "bob").credential);
        // A session of the team's, and a message that waits in it: what a session's request would name.
        let session = rig.register(&member, "beta");
        assert_eq!(session, twin.register(&member, "beta"));
        for server in [&mut rig, &mut twin] {
            server.send(&member, &session, &session, "to itself").unwrap();
            if moving {
                // The operator's command is nobody's request: the team keeps its key through it.
                server.open_move(ELSEWHERE, 30).unwrap();
            }
        }
        assert!(rig.state.teams[ELSEWHERE].take_in_retry.is_some(), "{path}");
        assert!(twin.state.teams[ELSEWHERE].take_in_retry.is_none(), "{path}");
        let body = body(&owner, &member, &session, &secret);
        let what = format!("{path} {body}");
        // A body that breaks its request's shape is refused before it is read as one, and names none.
        if let Ok(read) = Command::parse(path, body.to_string().as_bytes()) {
            asked[asks(&read) as usize] = true;
        }

        let (entries, twins) = (rig.log.len(), twin.log.len());
        let answer = rig.call(Some(&owner.credential), path, body.clone());
        let answered_the_twin = twin.call(Some(&owner.credential), path, body);
        assert_eq!(
            told(&answer),
            told(&answered_the_twin),
            "{what}: the answer tells whether the team keeps a key"
        );
        assert_eq!(
            rig.log[entries..],
            twin.log[twins..],
            "{what}: the entry tells whether the team keeps a key"
        );
        let left = match (&answer, rig.log.len() - entries) {
            (Ok(_), 0) => Nothing,
            (Ok(_), 1) => ARecord,
            (Err(_), 0) => ARefusal,
            (_, entries) => panic!("{what} made {entries} entries and was answered {answer:?}"),
        };
        assert_eq!(left, leaves, "{what}: {answer:?}");
        // The rule: the key is gone exactly when the owner's own request left a record.
        let ended = left == ARecord;
        assert_eq!(
            rig.state.teams[ELSEWHERE].take_in_retry.is_none(),
            ended,
            "{what}"
        );
        assert_eq!(
            rig.state.principals[&owner.principal_ref].retry_key_in.is_empty(),
            ended,
            "{what}"
        );
        // Once the key has ended the two servers hold the same state: nothing is left of it.
        assert_eq!(rig.state == twin.state, ended, "{what}");
        // And what the rule is for: the take-in made again is the owner's until then, and a stranger's
        // join from then on, which takes nothing from the owner.
        let secret_now = match (&answer, path) {
            (Ok(rotated), "/v1/team/rotate-secret") => text(rotated, "join_secret"),
            _ => secret.clone(),
        };
        let again = take_in_keyed(&mut rig, None, &secret_now, KEY).unwrap();
        assert_eq!(
            (
                again["principal_ref"] == json!(owner.principal_ref),
                &again["owner"]
            ),
            (!ended, &json!(!ended)),
            "{what}"
        );
        assert_eq!(
            known(&mut rig, &owner.credential),
            ended && path != "/v1/credential/revoke",
            "{what}"
        );
        assert_eq!(replayed(&rig), rig.state, "{what}");
    }
    assert_eq!(
        asked,
        [true; Asks::Count as usize],
        "a request the server reads was not asked by the owner of a kept key"
    );
}

#[test]
fn a_team_that_gets_another_owner_or_another_secret_keeps_no_retry_key() {
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");

    // The operator names another owner. The key was kept for the owner a take-in made: the team keeps
    // none, and the same request is a stranger's join. The owner it was holds what it held.
    let mut rig = Rig::new();
    let alice = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
    let bob = rig.join(&secret, "bob");
    assert_eq!(
        rig.set_owner(ELSEWHERE, &bob.principal_ref).unwrap()["changed"],
        json!(true)
    );
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None);
    assert_eq!(rig.state.principals[&alice.principal_ref].retry_key_in, "");
    let stranger = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(stranger["owner"], json!(false));
    assert_ne!(stranger["principal_ref"], json!(alice.principal_ref));
    assert!(known(&mut rig, &alice.credential));
    assert_eq!(
        rig.state.teams[ELSEWHERE].owner(),
        Some(bob.principal_ref.as_str())
    );
    assert_eq!(replayed(&rig), rig.state);

    // A rotation ends the key, whoever asks for it: here the owner, whose own request it is. The
    // secret the team was taken in with is another secret then, and the new one with the key is the
    // join of a new principal.
    let mut rig = Rig::new();
    let first = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
    let rotated = text(&rotate(&mut rig, &first), "join_secret");
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None);
    let old = take_in_keyed(&mut rig, None, &secret, KEY).unwrap_err();
    assert_eq!((old.code, reason(&old)), (Code::Conflict, "other_join_secret"));
    let stranger = take_in_keyed(&mut rig, None, &rotated, KEY).unwrap();
    assert_eq!(
        (&stranger["taken_in"], &stranger["owner"]),
        (&json!(false), &json!(false))
    );
    assert_ne!(stranger["principal_ref"], json!(first.principal_ref));
    // The owner's credential is the one it held: a key that ended replaces nothing.
    assert!(known(&mut rig, &first.credential));
    assert_eq!(replayed(&rig), rig.state);

    // The operator's take-back gives the team a new secret, and ends the key with the old one.
    let mut rig = Rig::new();
    let taken_from = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
    assert!(rig.state.teams[ELSEWHERE].take_in_retry.is_some());
    let new_secret = text(&rig.take_back(ELSEWHERE).unwrap(), "join_secret");
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None);
    assert_eq!(rig.state.principals[&taken_from.principal_ref].retry_key_in, "");
    assert_eq!(replayed(&rig), rig.state);
    // The team has no owner then, and the first take-in with its new secret owns it by the join, as on
    // a team the operator took in: with a key of its own, that key is the one the team keeps from then
    // on, and the key the team was taken from answers for nobody.
    let owner = take_in_keyed(&mut rig, None, &new_secret, OTHER_KEY).unwrap();
    assert_eq!(
        (&owner["taken_in"], &owner["owner"]),
        (&json!(false), &json!(true))
    );
    assert_ne!(owner["principal_ref"], json!(taken_from.principal_ref));
    assert_eq!(
        rig.state.teams[ELSEWHERE]
            .take_in_retry
            .as_ref()
            .map(|kept| (kept.key_hash.clone(), kept.principal_ref.clone())),
        Some((sha256_hex(OTHER_KEY), text(&owner, "principal_ref")))
    );
    assert_eq!(
        rig.state.principals[&text(&owner, "principal_ref")].retry_key_in,
        ELSEWHERE
    );
    let stranger = take_in_keyed(&mut rig, None, &new_secret, KEY).unwrap();
    assert_eq!(stranger["owner"], json!(false));
    assert_ne!(stranger["principal_ref"], json!(taken_from.principal_ref));
    let again = take_in_keyed(&mut rig, None, &new_secret, OTHER_KEY).unwrap();
    assert_eq!(
        (&again["principal_ref"], &again["owner"]),
        (&owner["principal_ref"], &json!(true))
    );
    assert_eq!(replayed(&rig), rig.state);
}

#[test]
fn a_retry_key_is_kept_only_where_the_take_in_made_the_teams_owner() {
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let kept = |rig: &Rig| rig.state.teams[ELSEWHERE].take_in_retry.clone();
    let no_hash = |rig: &Rig| {
        rig.log
            .iter()
            .flat_map(|entry| &entry.records)
            .all(|record| !matches!(&record.event, Event::MemberJoined { retry_key_hash, .. } if !retry_key_hash.is_empty()))
    };

    // A caller the server knows takes a team in as the principal it is, and holds its credential still:
    // no credential rides the answer, and no key is kept. The same key with no credential is then the
    // join of a new principal.
    let mut rig = Rig::new();
    let (carol, _) = rig.create("carol");
    let taken = take_in_keyed(&mut rig, Some(&carol.credential), &secret, KEY).unwrap();
    assert_eq!(
        members_of(&taken),
        ["owner", "principal_ref", "taken_in", "team_name", "team_ref"]
    );
    assert_eq!((kept(&rig), no_hash(&rig)), (None, true));
    let stranger = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(stranger["owner"], json!(false));
    assert_ne!(stranger["principal_ref"], json!(carol.principal_ref));
    assert!(known(&mut rig, &carol.credential));

    // A caller that may not own, a gateway inside the server, makes no owner, and no key is kept.
    let mut rig = Rig::new();
    rig.may_own = false;
    let taken = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(taken["owner"], json!(false));
    assert_eq!((kept(&rig), no_hash(&rig)), (None, true));

    // A team the operator took in has no owner, and the first caller whose take-in may own it does, by
    // the join. With a key, that join keeps the key as a take-in of a team that was not here does: the
    // owner it made is the owner whose credential a lost answer would leave with nobody.
    let mut rig = Rig::new();
    rig.import("legacy", &secret).unwrap();
    let first = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        (&first["taken_in"], &first["owner"]),
        (&json!(false), &json!(true))
    );
    let dave = member_of(&first);
    assert_eq!(
        kept(&rig).map(|kept| (kept.key_hash, kept.principal_ref)),
        Some((sha256_hex(KEY), dave.principal_ref.clone()))
    );
    let again = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        (&again["principal_ref"], &again["owner"]),
        (&json!(dave.principal_ref), &json!(true))
    );
    assert!(!known(&mut rig, &dave.credential), "the lost credential is known");

    // On a team that has its owner, a take-in with a key of its own is a join that keeps nothing, and
    // the owner's key is the team's still: whoever holds the join secret cannot put a key in the place
    // of the owner's.
    let erin = take_in_keyed(&mut rig, None, &secret, OTHER_KEY).unwrap();
    assert_eq!(erin["owner"], json!(false));
    assert_eq!(
        kept(&rig).map(|kept| kept.key_hash),
        Some(sha256_hex(KEY)),
        "a member's key took the place of the owner's"
    );
    let owner = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(
        (&owner["principal_ref"], &owner["owner"]),
        (&json!(dave.principal_ref), &json!(true))
    );
    // And the member's key answers for nobody: it is another key, and its caller is new again.
    let erin_again = take_in_keyed(&mut rig, None, &secret, OTHER_KEY).unwrap();
    assert_ne!(erin_again["principal_ref"], erin["principal_ref"]);
    assert_eq!(replayed(&rig), rig.state);

    // A caller the server knows that is the first to join a team with no owner owns it, and holds its
    // credential still: no key is kept for it.
    let mut rig = Rig::new();
    let (frank, _) = rig.create("frank");
    rig.import("legacy", &secret).unwrap();
    let owned = take_in_keyed(&mut rig, Some(&frank.credential), &secret, KEY).unwrap();
    assert_eq!(
        owned,
        json!({
            "team_ref": ELSEWHERE, "team_name": "legacy", "principal_ref": frank.principal_ref,
            "taken_in": false, "owner": true,
        })
    );
    assert_eq!((kept(&rig), no_hash(&rig)), (None, true));

    // A join by the ticket of a move never owns, so on a team with no owner it keeps no key either.
    let mut rig = Rig::new();
    rig.import("moving", &secret).unwrap();
    let ticket = text(&rig.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    let followed = take_in_keyed(&mut rig, None, &ticket, KEY).unwrap();
    assert_eq!(followed["owner"], json!(false));
    assert_eq!((kept(&rig), no_hash(&rig)), (None, true));
}

#[test]
fn a_retry_key_is_held_to_its_form_and_changes_nothing_else_of_a_take_in() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let field = |error: &Error| error.details.get("field").cloned().unwrap_or_default();
    let keyed = |key: Value| json!({"join_secret": secret, "team_name": "ops", "retry_key": key});

    // A key is 22 to 128 characters of a credential's alphabet. One that is not is refused in fixed
    // words that hold nothing of it, whatever the server holds, and nothing is made.
    let (before, entries) = (rig.state.clone(), rig.log.len());
    for key in [
        json!(""),
        json!("k".repeat(21)),
        json!("k".repeat(129)),
        json!("a key with spaces in it, long enough"),
        json!("a-key-with-a-quote-\"-in-it-long-enough"),
        json!("é".repeat(30)),
        json!(22),
        json!(["a-key-in-a-list-that-is-long-enough"]),
    ] {
        let error = rig
            .call(None, "/v1/team/take-in", keyed(key.clone()))
            .unwrap_err();
        assert_eq!(
            (error.code, field(&error).as_str(), reason(&error)),
            (Code::InvalidInput, "retry_key", "invalid_value"),
            "{key}"
        );
        let told = wire(&error);
        assert!(
            !told.contains("spaces in it") && !told.contains("kkkk") && !told.contains("quote"),
            "the refusal repeats the key: {told}"
        );
    }
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    // It is read last: a request with something else wrong is told that, as it always was.
    for (body, named) in [
        (json!({"team_name": "ops", "retry_key": "short"}), "join_secret"),
        (json!({"join_secret": secret, "retry_key": "short"}), "team_name"),
        (
            json!({"join_secret": "not-a-join-secret", "team_name": "ops", "retry_key": "short"}),
            "join_secret",
        ),
    ] {
        let error = rig.call(None, "/v1/team/take-in", body.clone()).unwrap_err();
        assert_eq!(field(&error), named, "{body}");
    }
    // The creation token is asked for where it always was, before anything of the body is read: a
    // request with a key, well formed or not, is refused in a creation's bytes.
    rig.may_create = false;
    for key in [json!(KEY), json!("short")] {
        let error = rig.call(None, "/v1/team/take-in", keyed(key)).unwrap_err();
        assert_eq!(wire(&error), wire(&Error::create_refused()));
    }
    rig.may_create = true;
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));

    // A key at either end of its length is taken, and `null` is no key: the request it always was.
    for (team_ref, key) in [
        ("at-the-least", json!("k".repeat(22))),
        (
            "at-the-most",
            json!("A-Za-z0-9._~+/=".repeat(9)[..128].to_owned()),
        ),
    ] {
        let body = json!({"join_secret": secret_of(team_ref, "s"), "team_name": "ops", "retry_key": key});
        let taken = rig.call(None, "/v1/team/take-in", body).unwrap();
        assert_eq!(taken["taken_in"], json!(true), "{team_ref}");
        assert!(rig.state.teams[team_ref].take_in_retry.is_some(), "{team_ref}");
    }
    let taken = rig.call(None, "/v1/team/take-in", keyed(Value::Null)).unwrap();
    assert_eq!(taken["taken_in"], json!(true));
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None);

    // A join is asked for no key and reads none: the member is one it does not know.
    let (creator, created) = rig.create("creator");
    let entries = rig.log.len();
    let joined = rig
        .call(
            None,
            "/v1/team/join",
            json!({"join_secret": created, "retry_key": KEY}),
        )
        .unwrap();
    assert_eq!(joined["team_ref"], json!(creator.team_ref));
    assert!(matches!(
        &rig.log[entries].records[1].event,
        Event::MemberJoined { retry_key_hash, .. } if retry_key_hash.is_empty()
    ));
    assert_eq!(rig.state.teams[&creator.team_ref].take_in_retry, None);
}

/// What a request was answered: the result, or the refusal as it goes on the wire.
fn told(answer: &Result<Value, Error>) -> String {
    match answer {
        Ok(result) => result.to_string(),
        Err(error) => wire(error),
    }
}

/// Two servers that are the same in everything but one: the first took the team in with a retry key,
/// the second with none. Their entropy is the same, so whatever each makes next has the same
/// references, and an answer of the one can be held against the other's byte for byte. With them: the
/// answer of the take-in, a member that each server knows, and the ticket of a move the operator
/// opened, the same on both.
fn with_and_without_a_key(secret: &str) -> (Rig, Rig, Value, Member, String) {
    let (mut keeps, mut keeps_none) = (Rig::new(), Rig::new());
    let first = take_in_keyed(&mut keeps, None, secret, KEY).unwrap();
    assert_eq!(first, take_in(&mut keeps_none, None, secret, "alice").unwrap());
    assert!(keeps.state.teams[ELSEWHERE].take_in_retry.is_some());
    assert!(keeps_none.state.teams[ELSEWHERE].take_in_retry.is_none());
    let member = keeps.join(secret, "bob");
    assert_eq!(member.credential, keeps_none.join(secret, "bob").credential);
    let ticket = text(&keeps.open_move(ELSEWHERE, 30).unwrap(), "ticket");
    assert_eq!(
        ticket,
        text(&keeps_none.open_move(ELSEWHERE, 30).unwrap(), "ticket")
    );
    (keeps, keeps_none, first, member, ticket)
}

/// Asks both servers the same take-ins, and holds each answer and each entry of the one against the
/// other's: no key, another key, a key that is no key, the kept key with another secret, with the
/// ticket, and from a caller the server knows. `the_kept_key_too` adds the one request that a team which
/// keeps the key answers differently: the team's secret, that key and no credential the server knows.
fn answered_the_same(
    keeps: &mut Rig,
    keeps_none: &mut Rig,
    (secret, ticket, member): (&str, &str, &Member),
    the_kept_key_too: bool,
) -> usize {
    let other = secret_of(ELSEWHERE, "another-secret-for-the-reference");
    let body = |secret: &str, key: Value| {
        let mut body = json!({"join_secret": secret, "team_name": "ops", "human_label": "eve"});
        if !key.is_null() {
            body["retry_key"] = key;
        }
        body
    };
    let known_caller = Some(member.credential.as_str());
    let never_issued = Some("a-credential-the-server-never-issued");
    let mut requests = vec![
        (None, body(secret, Value::Null)),
        (None, body(secret, json!(OTHER_KEY))),
        (None, body(secret, json!("no-key"))),
        (None, body(secret, json!(7))),
        (None, body(&other, Value::Null)),
        (None, body(&other, json!(OTHER_KEY))),
        (None, body(&other, json!(KEY))),
        (None, body(&other, json!("no-key"))),
        (None, body(ticket, json!(KEY))),
        (None, body(ticket, json!(OTHER_KEY))),
        (never_issued, body(&other, json!(KEY))),
        (known_caller, body(secret, json!(KEY))),
        (known_caller, body(secret, json!(OTHER_KEY))),
        (known_caller, body(&other, json!(KEY))),
        (known_caller, body(&other, Value::Null)),
        (known_caller, body(ticket, json!(KEY))),
    ];
    if the_kept_key_too {
        requests.push((None, body(secret, json!(KEY))));
        requests.push((never_issued, body(secret, json!(KEY))));
    }
    for (credential, request) in &requests {
        let (entries, entries_none) = (keeps.log.len(), keeps_none.log.len());
        let answer = keeps.call(*credential, "/v1/team/take-in", request.clone());
        let answer_none = keeps_none.call(*credential, "/v1/team/take-in", request.clone());
        assert_eq!(told(&answer), told(&answer_none), "{request}");
        // What each wrote is the same as well: nothing a request leaves says which team keeps a key.
        assert_eq!(keeps.log[entries..], keeps_none.log[entries_none..], "{request}");
    }
    requests.len()
}

#[test]
fn no_answer_and_no_record_tells_whether_a_team_keeps_a_retry_key() {
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");

    // A team that keeps a key, against one that never kept one: every request but the one that carries
    // the team's secret, the kept key and no credential the server knows.
    let (mut keeps, mut keeps_none, first, member, ticket) = with_and_without_a_key(&secret);
    assert_eq!(
        answered_the_same(&mut keeps, &mut keeps_none, (&secret, &ticket, &member), false),
        16
    );
    assert!(keeps.state.teams[ELSEWHERE].take_in_retry.is_some());
    // The positive control: the one request that is left is answered for the owner where the key is
    // kept and for a new member where none is, and even then with the same members.
    let honoured = take_in_keyed(&mut keeps, None, &secret, KEY).unwrap();
    let stranger = take_in_keyed(&mut keeps_none, None, &secret, KEY).unwrap();
    assert_eq!(
        (&honoured["principal_ref"], &honoured["owner"]),
        (&first["principal_ref"], &json!(true))
    );
    assert_ne!(stranger["principal_ref"], first["principal_ref"]);
    assert_eq!(stranger["owner"], json!(false));
    assert_eq!(members_of(&honoured), members_of(&stranger));

    // A team whose key has ended, against one that never kept one. The owner registers a session on
    // both, which ends the key where there was one: from there the two servers hold the same state,
    // and every request is answered in the same bytes, the one with the kept key included. It is the
    // join of a new principal on both.
    let (mut kept, mut never, first, member, ticket) = with_and_without_a_key(&secret);
    let owner = member_of(&first);
    assert_eq!(kept.register(&owner, "alpha"), never.register(&owner, "alpha"));
    assert_eq!(kept.state.teams[ELSEWHERE].take_in_retry, None);
    assert_eq!(kept.state, never.state);
    assert_eq!(
        answered_the_same(&mut kept, &mut never, (&secret, &ticket, &member), true),
        18
    );
    assert_eq!(kept.state, never.state);
    let stranger = take_in_keyed(&mut kept, None, &secret, KEY).unwrap();
    assert_eq!(stranger, take_in_keyed(&mut never, None, &secret, KEY).unwrap());
    assert_eq!(stranger["owner"], json!(false));
    assert_ne!(stranger["principal_ref"], first["principal_ref"]);
    assert!(known(&mut kept, &owner.credential));
}

#[test]
fn whatever_the_records_say_a_team_keeps_one_key_and_a_principal_has_one_team_that_keeps_its_key() {
    // No request makes these records: a team keeps a key for the owner a take-in made, and that owner is
    // made once. Applying a record still leaves the state whole whatever the record is, because a log
    // is read as it is written: the way from a principal to the team that keeps its key and the key the
    // team keeps always name each other.
    let at = Timestamp(1_800_000_000 * SECOND);
    let mut state = State::default();
    let apply = |state: &mut State, event: Event| state.apply(&Record::new(at, event));
    let joined = |team_ref: &str, principal_ref: &str, retry_key_hash: &str| Event::MemberJoined {
        team_ref: team_ref.into(),
        principal_ref: principal_ref.into(),
        human_label: String::new(),
        by_move_ticket: false,
        retry_key_hash: retry_key_hash.into(),
    };
    for principal_ref in ["p-one", "p-two"] {
        apply(
            &mut state,
            Event::CredentialIssued {
                principal_ref: principal_ref.into(),
                credential_hash: format!("the-credential-of-{principal_ref}"),
            },
        );
    }
    for team_ref in ["t-north", "t-south"] {
        apply(
            &mut state,
            Event::TeamCreated {
                team_ref: team_ref.into(),
                team_name: "ops".into(),
                join_secret_hash: "the-hash-of-a-secret".into(),
                created_by: String::new(),
                taken_in: true,
            },
        );
    }
    let kept = |state: &State, team_ref: &str| {
        state.teams[team_ref]
            .take_in_retry
            .as_ref()
            .map(|kept| (kept.key_hash.clone(), kept.principal_ref.clone()))
    };
    let way = |state: &State, principal_ref: &str| state.principals[principal_ref].retry_key_in.clone();

    apply(&mut state, joined("t-north", "p-one", "the-first-hash"));
    assert_eq!(
        kept(&state, "t-north"),
        Some(("the-first-hash".into(), "p-one".into()))
    );
    assert_eq!(way(&state, "p-one"), "t-north");
    // The team keeps a key for another principal: the first has no way back to a key that is not its own.
    apply(&mut state, joined("t-north", "p-two", "the-second-hash"));
    assert_eq!(
        kept(&state, "t-north"),
        Some(("the-second-hash".into(), "p-two".into()))
    );
    assert_eq!(
        (way(&state, "p-one"), way(&state, "p-two")),
        (String::new(), "t-north".into())
    );
    // Another team keeps a key for that principal: the team that kept one for it keeps none.
    apply(&mut state, joined("t-south", "p-two", "the-third-hash"));
    assert_eq!(kept(&state, "t-north"), None);
    assert_eq!(
        kept(&state, "t-south"),
        Some(("the-third-hash".into(), "p-two".into()))
    );
    assert_eq!(way(&state, "p-two"), "t-south");
    // A record that names a principal no team keeps a key for ends nothing, and one that names the
    // principal a team does keep one for ends that key and no other.
    apply(&mut state, joined("t-north", "p-one", "the-fourth-hash"));
    apply(
        &mut state,
        Event::MemberLeft {
            team_ref: "t-north".into(),
            principal_ref: "p-two".into(),
        },
    );
    assert_eq!(kept(&state, "t-south"), None);
    assert_eq!(way(&state, "p-two"), "");
    assert_eq!(
        kept(&state, "t-north"),
        Some(("the-fourth-hash".into(), "p-one".into()))
    );
    assert_eq!(way(&state, "p-one"), "t-north");
    // A way back that points at a team which keeps another principal's key, as no record leaves it and
    // a state from elsewhere might hold it: a record of that principal's own ends its way, and leaves
    // the other's key where it is.
    state.principals.get_mut("p-two").unwrap().retry_key_in = "t-north".into();
    apply(
        &mut state,
        Event::MemberLeft {
            team_ref: "t-south".into(),
            principal_ref: "p-two".into(),
        },
    );
    assert_eq!(
        kept(&state, "t-north"),
        Some(("the-fourth-hash".into(), "p-one".into()))
    );
    assert_eq!(way(&state, "p-two"), "");
    // A record of a team the state does not hold changes nothing.
    let before = state.clone();
    apply(&mut state, joined("no-such-team", "p-one", ""));
    apply(&mut state, joined("no-such-team", "p-two", "the-fifth-hash"));
    assert_eq!(state.teams, before.teams);
    assert_eq!(state.principals, before.principals);
}

#[test]
fn a_log_written_before_a_retry_key_had_an_end_replays_to_one_state() {
    // What a 0.7.0 of before a key had an end wrote, in the records it wrote them in: the take-in with a
    // key; a move its owner opened by request, with no word of whose request it was; the take-in made
    // again, whose join carried no hash; and the take-in made again once more, which that build
    // honoured for as long as the team had its secret.
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let owner = "the-owner-the-take-in-made";
    let at = |nanoseconds: i64| Timestamp(1_800_000_000 * SECOND + nanoseconds);
    let issued = |credential_hash: &str| Event::CredentialIssued {
        principal_ref: owner.into(),
        credential_hash: credential_hash.into(),
    };
    let joined = |retry_key_hash: String| Event::MemberJoined {
        team_ref: ELSEWHERE.into(),
        principal_ref: owner.into(),
        human_label: "alice".into(),
        by_move_ticket: false,
        retry_key_hash,
    };
    let again = |lost: &str, credential_hash: &str| {
        vec![
            Event::CredentialRevoked {
                credential_hash: lost.into(),
            },
            issued(credential_hash),
            joined(String::new()),
        ]
    };
    let entries: Vec<Vec<Event>> = vec![
        vec![
            issued("the-first-credential"),
            Event::TeamCreated {
                team_ref: ELSEWHERE.into(),
                team_name: "ops".into(),
                join_secret_hash: sha256_hex(&secret),
                created_by: owner.into(),
                taken_in: true,
            },
            joined(sha256_hex(KEY)),
        ],
        vec![Event::TeamMoveOpened {
            team_ref: ELSEWHERE.into(),
            ticket_hash: "the-hash-of-a-ticket".into(),
            ends_at: at(86_400 * SECOND),
            by_owner: false,
        }],
        again("the-first-credential", "the-second-credential"),
        again("the-second-credential", "the-third-credential"),
    ];
    let written: Vec<Vec<u8>> = entries
        .into_iter()
        .zip(1..)
        .map(|(events, sequence)| {
            let records = events
                .into_iter()
                .map(|event| Record::new(at(sequence), event))
                .collect();
            Entry {
                sequence: sequence.unsigned_abs(),
                records,
            }
            .encode()
        })
        .collect();
    // The bytes are the bytes of that build: no member it did not know, and no hash in the join of a
    // take-in made again.
    for (bytes, held) in written.iter().zip([1, 0, 0, 0]) {
        let text = String::from_utf8(bytes.clone()).unwrap();
        assert!(!text.contains("by_owner"), "{text}");
        assert_eq!(text.matches("retry_key_hash").count(), held, "{text}");
    }
    let replay = |through: usize| {
        let mut state = State::default();
        for bytes in &written[..through] {
            state.replay(&Entry::decode(bytes).unwrap()).unwrap();
        }
        state
    };
    // It reads to one state, however often it is read, and a snapshot of that state reads back to it.
    let (once, twice) = (replay(4), replay(4));
    assert_eq!(once, twice);
    assert_eq!(
        serde_json::from_slice::<State>(&serde_json::to_vec(&once).unwrap()).unwrap(),
        once
    );
    // The move that was opened is read as the operator's, which ends nothing: the team keeps its key
    // past it, and the way back from the owner to the team is there.
    let opened = replay(2);
    assert_eq!(
        opened.teams[ELSEWHERE]
            .take_in_retry
            .as_ref()
            .map(|kept| kept.principal_ref.as_str()),
        Some(owner)
    );
    assert_eq!(opened.principals[owner].retry_key_in, ELSEWHERE);
    // The take-in made again ends it, as the rule is now: its entry makes a credential unknown, and
    // its join, as that build wrote it, keeps none. The rest is what the records say: the owner, the
    // one credential it has, the move.
    for state in [replay(3), once.clone()] {
        assert_eq!(state.teams[ELSEWHERE].take_in_retry, None);
        assert_eq!(state.principals[owner].retry_key_in, "");
        assert_eq!(state.teams[ELSEWHERE].owner(), Some(owner));
    }
    assert_eq!(
        once.credentials.iter().collect::<Vec<_>>(),
        [(&"the-third-credential".to_owned(), &owner.to_owned())]
    );
    assert!(once.teams[ELSEWHERE].move_ticket.is_some());
    // A server that goes on from either reading answers the request with the key in the same bytes:
    // the join of a new principal.
    let answers: Vec<Value> = [once, twice]
        .into_iter()
        .map(|state| {
            let mut rig = Rig::new();
            rig.state = state;
            rig.advance(60);
            take_in_keyed(&mut rig, None, &secret, KEY).unwrap()
        })
        .collect();
    assert_eq!(answers[0], answers[1]);
    assert_eq!(answers[0]["owner"], json!(false));
    assert_ne!(answers[0]["principal_ref"], json!(owner));
}

#[test]
fn a_retry_key_buys_no_guess_at_a_join_secret_that_the_bound_on_failed_joins_does_not_count() {
    let mut rig = Rig::new();
    let secret = secret_of(ELSEWHERE, "kept-from-the-other-backend");
    let wrong = secret_of(ELSEWHERE, "not-the-secret-of-the-team");
    let alice = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
    // Mallory is a principal the server knows: it has a team of its own.
    let (mallory, _) = rig.create("mallory");
    let caller = Some(mallory.credential.as_str());
    let conflict = wire(&take_in(&mut rig, None, &wrong, "").unwrap_err());
    // Its guesses at the team's secret are counted with the key the team keeps, with another and with
    // none: as many as a principal may fail, and no more.
    for n in 0..limits::FAILED_JOINS_PER_WINDOW {
        rig.advance(1);
        let refused = match n % 3 {
            0 => take_in_keyed(&mut rig, caller, &wrong, KEY),
            1 => take_in_keyed(&mut rig, caller, &wrong, OTHER_KEY),
            _ => take_in(&mut rig, caller, &wrong, ""),
        }
        .unwrap_err();
        assert_eq!(wire(&refused), conflict, "guess {n}");
    }
    // The next is refused unheard, whatever it carries: the right secret, the right secret with the
    // kept key, and one more guess with it. Nothing is made, and the owner's credential is untouched.
    rig.advance(1);
    let (before, entries) = (rig.state.clone(), rig.log.len());
    for attempt in [
        take_in(&mut rig, caller, &secret, ""),
        take_in_keyed(&mut rig, caller, &secret, KEY),
        take_in_keyed(&mut rig, caller, &wrong, KEY),
    ] {
        let limited = attempt.unwrap_err();
        assert_eq!(
            (limited.code, reason(&limited)),
            (Code::RateLimited, "join_attempts")
        );
    }
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    assert!(known(&mut rig, &alice.credential));
    // A take-in made again is not that principal's: it carries no credential the server knows, so it
    // is counted against nobody and held by nobody's failures, as a stranger's join always was.
    let again = take_in_keyed(&mut rig, None, &secret, KEY).unwrap();
    assert_eq!(again["principal_ref"], json!(alice.principal_ref));
    // And it clears nothing of Mallory's: what Mallory failed still holds Mallory.
    let held = take_in(&mut rig, caller, &secret, "").unwrap_err();
    assert_eq!(reason(&held), "join_attempts");

    // A principal the team banned is refused with the key as without it. A team keeps a key for its
    // owner alone, and nobody bans the owner, so no request leads here: what does is a state that a
    // build of before the key had an end wrote, which can hold a key for a principal the team has
    // since transferred from and banned. Asking again with the key and no credential, that principal
    // is refused and counted against nobody: its request showed the secret and the key, and guessed
    // at neither. With its credential it is counted, as a banned principal's join always was.
    let mut rig = Rig::new();
    let alice = member_of(&take_in_keyed(&mut rig, None, &secret, KEY).unwrap());
    let bob = rig.join(&secret, "bob");
    rig.ok(
        &alice,
        "/v1/team/transfer",
        json!({"team_ref": ELSEWHERE, "principal_ref": bob.principal_ref}),
    );
    revoke(&mut rig, &bob, &alice.principal_ref, Some(true)).unwrap();
    assert_eq!(rig.state.teams[ELSEWHERE].take_in_retry, None);
    rig.state.teams.get_mut(ELSEWHERE).unwrap().take_in_retry = Some(TakeInRetry {
        key_hash: sha256_hex(KEY),
        principal_ref: alice.principal_ref.clone(),
    });
    let (before, entries) = (rig.state.clone(), rig.log.len());
    for _ in 0..=limits::FAILED_JOINS_PER_WINDOW {
        let refused = take_in_keyed(&mut rig, None, &secret, KEY).unwrap_err();
        assert_eq!(wire(&refused), conflict);
    }
    for _ in 0..limits::FAILED_JOINS_PER_WINDOW {
        let refused = take_in_keyed(&mut rig, Some(&alice.credential), &secret, KEY).unwrap_err();
        assert_eq!(wire(&refused), conflict);
    }
    let limited = take_in_keyed(&mut rig, Some(&alice.credential), &secret, KEY).unwrap_err();
    assert_eq!(reason(&limited), "join_attempts");
    // Nothing was made for any of them: no credential in the place of the one the principal holds.
    assert_eq!((&rig.state, rig.log.len()), (&before, entries));
    assert!(known(&mut rig, &alice.credential));
}

#[test]
fn a_retry_key_answers_for_the_team_that_keeps_it_and_for_no_other() {
    let mut rig = Rig::new();
    let (north, south) = ("north-team", "south-team");
    let (north_secret, south_secret) = (secret_of(north, "n"), secret_of(south, "s"));
    let body = |secret: &str, key: &str| json!({"join_secret": secret, "team_name": "ops", "retry_key": key});
    let alice = member_of(
        &rig.call(None, "/v1/team/take-in", body(&north_secret, KEY))
            .unwrap(),
    );
    let bob = member_of(
        &rig.call(None, "/v1/team/take-in", body(&south_secret, OTHER_KEY))
            .unwrap(),
    );
    // The key of one team with the secret of the other is another key: a new member of that team.
    for (secret, key, owner) in [(&south_secret, KEY, &bob), (&north_secret, OTHER_KEY, &alice)] {
        let crossed = rig.call(None, "/v1/team/take-in", body(secret, key)).unwrap();
        assert_eq!(crossed["owner"], json!(false));
        assert_ne!(crossed["principal_ref"], json!(alice.principal_ref));
        assert_ne!(crossed["principal_ref"], json!(bob.principal_ref));
        assert!(known(&mut rig, &owner.credential));
    }
    assert!(known(&mut rig, &alice.credential) && known(&mut rig, &bob.credential));
    // One key that a client used for two teams answers on each for the principal that team keeps.
    let mut rig = Rig::new();
    let alice = member_of(
        &rig.call(None, "/v1/team/take-in", body(&north_secret, KEY))
            .unwrap(),
    );
    let bob = member_of(
        &rig.call(None, "/v1/team/take-in", body(&south_secret, KEY))
            .unwrap(),
    );
    assert_ne!(alice.principal_ref, bob.principal_ref);
    let again = rig
        .call(None, "/v1/team/take-in", body(&south_secret, KEY))
        .unwrap();
    assert_eq!(again["principal_ref"], json!(bob.principal_ref));
    // Only that principal's credential was made unknown.
    assert!(known(&mut rig, &alice.credential));
    assert!(!known(&mut rig, &bob.credential));
}

/// A third key of the same form as [`KEY`].
const THIRD_KEY: &str = "YS10aGlyZC1rZXktZm9yLXRoZS1ydWxlcy1vbmx5LTAz";

/// Numbers that are the same in every run: what the walk below chooses by.
struct Choices(u64);

impl Choices {
    fn below(&mut self, bound: usize) -> usize {
        // xorshift64*: enough to ask the same teams something else at every step.
        self.0 ^= self.0 >> 12;
        self.0 ^= self.0 << 25;
        self.0 ^= self.0 >> 27;
        (self.0.wrapping_mul(0x2545_F491_4F6C_DD1D) >> 33) as usize % bound
    }

    fn one_of<'a, T>(&mut self, of: &'a [T]) -> Option<&'a T> {
        if of.is_empty() {
            None
        } else {
            Some(&of[self.below(of.len())])
        }
    }
}

/// What a step of the walk asked, as far as the rules of a retry key read it.
#[derive(Debug)]
enum Asked {
    /// `/v1/team/take-in`, with what it presented.
    TakeIn {
        team: &'static str,
        secret: String,
        key: Option<&'static str>,
    },
    /// `/v1/credential/revoke`, with the credential it presented.
    RevokeOwn(String),
    /// Anything else: a join, an administration request, one of the operator's commands.
    Other,
}

/// What a team was before a step, as far as the rules of a retry key read it.
#[derive(Clone)]
struct TeamBefore {
    owner: Option<String>,
    join_secret_hash: String,
    /// The hash of the key the team kept, and the principal it kept it for.
    keeps: Option<(String, String)>,
}

/// What the server held before a step: the teams of the walk, and the principal whose credential the
/// step's request carries, when the server knows that credential. The operator's commands carry none.
/// The walk keeps the rest itself, because a copy of the whole state at every step is the whole walk
/// squared.
struct Before {
    teams: std::collections::BTreeMap<&'static str, TeamBefore>,
    asks: Option<String>,
}

/// What the walk has seen the server make: every principal, and whom every credential ever named.
#[derive(Default)]
struct Seen {
    principals: std::collections::BTreeSet<String>,
    named: std::collections::BTreeMap<String, String>,
}

/// What one step did to the retry keys: how many it kept for an owner a take-in made, how many take-ins
/// it made again, how many keys an owner's own request ended, and the kinds of the records that ended
/// them.
#[derive(Default)]
struct Counted {
    kept: usize,
    repeated: usize,
    ended_by_their_owner: usize,
    ended_with: std::collections::BTreeSet<&'static str>,
}

/// The kinds of record that end a retry key, as the log names them.
const ENDS_A_KEY: [&str; 10] = [
    "credential.revoked",
    "member.joined",
    "member.left",
    "member.revoked",
    "members.revoked_by_version",
    "session.opened",
    "team.move_closed",
    "team.move_opened",
    "team.owner_changed",
    "team.secret_rotated",
];

/// The kind of a record, when it is one that ends the key `team_ref` keeps for `kept_for` in an entry
/// which that principal's own request made: the rule of [`State::apply`], said a second time. The match
/// has no arm for anything else, so a kind of record that is added does not build here until it is
/// said whether it ends a key.
fn ends_the_key(event: &Event, team_ref: &str, kept_for: &str) -> Option<&'static str> {
    match event {
        // A record that names the principal as the one who acted.
        Event::SessionOpened { principal_ref, .. } => (principal_ref == kept_for).then_some("session.opened"),
        Event::MemberLeft { principal_ref, .. } => (principal_ref == kept_for).then_some("member.left"),
        Event::MemberJoined {
            principal_ref,
            retry_key_hash,
            ..
        } => (principal_ref == kept_for && retry_key_hash.is_empty()).then_some("member.joined"),
        // The credential is the caller's own: the walk holds that for every such record.
        Event::CredentialRevoked { .. } => Some("credential.revoked"),
        // A record that only the team's owner is answered for.
        Event::MemberRevoked { team_ref: of, .. } => (of == team_ref).then_some("member.revoked"),
        Event::MembersRevokedByVersion { team_ref: of, .. } => {
            (of == team_ref).then_some("members.revoked_by_version")
        }
        Event::TeamMoveOpened {
            team_ref: of,
            by_owner,
            ..
        } => (of == team_ref && *by_owner).then_some("team.move_opened"),
        Event::TeamMoveClosed {
            team_ref: of,
            by_owner,
        } => (of == team_ref && *by_owner).then_some("team.move_closed"),
        // Another owner, and another secret.
        Event::TeamOwnerChanged {
            team_ref: of,
            principal_ref,
        } => (of == team_ref && principal_ref != kept_for).then_some("team.owner_changed"),
        Event::TeamSecretRotated { team_ref: of, .. } => (of == team_ref).then_some("team.secret_rotated"),
        // A credential issued and a team created are in an entry with the join that ends the key. The
        // five below them are records of a session, which the rule does not read: a principal that a
        // team keeps a key for has no session, which the walk holds at every step, so none of them is
        // its first.
        Event::CredentialIssued { .. }
        | Event::TeamCreated { .. }
        | Event::SessionResumed { .. }
        | Event::SessionHeartbeat { .. }
        | Event::SessionClosed { .. }
        | Event::MessageAccepted { .. }
        | Event::MessageAcked { .. } => None,
    }
}

/// Holds one step against the rules of a retry key: what the server held before it, what it holds now,
/// the entry the step made and what was asked.
fn held_to_the_rules_of_a_retry_key(
    before: &Before,
    seen: &Seen,
    after: &State,
    entry: &Entry,
    asked: &Asked,
) -> Counted {
    let events: Vec<&Event> = entry.records.iter().map(|record| &record.event).collect();
    let issues_for = |principal: &str| {
        events.iter().any(
            |event| matches!(event, Event::CredentialIssued { principal_ref, .. } if principal_ref == principal),
        )
    };
    let mut counted = Counted::default();
    for event in &events {
        match event {
            // A join carries a key's hash in two entries. One is the take-in, with that key, of a
            // caller the server did not know: for the principal the entry made, which owns the team
            // from this entry on, on a team that had no owner or was not there, with the team's own
            // secret and never with the ticket of a move. The other is that take-in made again, which
            // keeps what the team kept: it is held to its own rule below.
            Event::MemberJoined {
                team_ref,
                principal_ref,
                retry_key_hash,
                by_move_ticket,
                ..
            } if !retry_key_hash.is_empty() => {
                let Asked::TakeIn {
                    team,
                    secret,
                    key: Some(key),
                } = asked
                else {
                    panic!("a key was kept for {asked:?}");
                };
                assert_eq!((team_ref.as_str(), retry_key_hash), (*team, &sha256_hex(key)));
                assert_eq!(before.asks, None, "a key was kept for a caller the server knew");
                assert!(
                    issues_for(principal_ref),
                    "a key was kept and no credential issued"
                );
                assert_eq!(after.teams[*team].owner(), Some(principal_ref.as_str()));
                assert_eq!(after.teams[*team].join_secret_hash, sha256_hex(secret));
                assert!(!by_move_ticket, "a key was kept for a join by the ticket");
                if seen.principals.contains(principal_ref) {
                    assert_eq!(
                        before.teams[team].keeps,
                        Some((retry_key_hash.clone(), principal_ref.clone())),
                        "a key was kept for a principal that was there, and it is not the one the team kept"
                    );
                } else {
                    assert!(
                        before.teams.get(team).is_none_or(|team| team.owner.is_none()),
                        "a key was kept on a team that had its owner"
                    );
                    counted.kept += 1;
                }
            }
            // A credential is issued for a principal that was there by one request alone: a take-in
            // with the team's secret and the key the team keeps for that principal, from a caller the
            // server does not know. Its entry keeps the key again, and the credential it issues is the
            // one credential the principal has from then on.
            Event::CredentialIssued { principal_ref, .. } if seen.principals.contains(principal_ref) => {
                let Asked::TakeIn {
                    team,
                    secret,
                    key: Some(key),
                } = asked
                else {
                    panic!("a credential was issued for a principal that was there, by {asked:?}");
                };
                let held = &before.teams[team];
                assert_eq!(
                    held.keeps,
                    Some((sha256_hex(key), principal_ref.clone())),
                    "the team kept no such key"
                );
                assert_eq!(held.join_secret_hash, sha256_hex(secret));
                assert_eq!(
                    before.asks, None,
                    "a caller the server knew was answered for another"
                );
                assert!(
                    events.iter().any(|event| {
                        matches!(event, Event::MemberJoined { principal_ref: joined, retry_key_hash, .. }
                            if joined == principal_ref && *retry_key_hash == sha256_hex(key))
                    }),
                    "a take-in made again did not keep its key for the next"
                );
                assert_eq!(
                    after
                        .credentials
                        .values()
                        .filter(|named| *named == principal_ref)
                        .count(),
                    1,
                    "a take-in made again left a credential before it known"
                );
                counted.repeated += 1;
            }
            // A credential is made unknown by its own holder, or by the take-in made again of the
            // principal it names, and by nothing else.
            Event::CredentialRevoked { credential_hash } => match asked {
                Asked::RevokeOwn(credential) => assert_eq!(&sha256_hex(credential), credential_hash),
                Asked::TakeIn { .. } => assert!(
                    issues_for(&seen.named[credential_hash]),
                    "a take-in made the credential of another principal unknown"
                ),
                Asked::Other => panic!("a credential was made unknown by a request that is neither"),
            },
            _ => {}
        }
    }
    for (team_ref, team) in &after.teams {
        let was = before.teams.get(team_ref.as_str());
        let then = was.and_then(|team| team.keeps.clone());
        let now = team
            .take_in_retry
            .as_ref()
            .map(|kept| (kept.key_hash.clone(), kept.principal_ref.clone()));
        let has = |of: &dyn Fn(&Event) -> bool| events.iter().any(|event| of(event));
        let rotated =
            has(&|event| matches!(event, Event::TeamSecretRotated { team_ref: of, .. } if of == team_ref));
        if let Some((_, kept_for)) = &then {
            // The key ends with the first record that the request of the principal it is kept for
            // makes, with its credential, whatever that request is and whichever team it is about.
            let its_owners_own = before.asks.as_ref() == Some(kept_for);
            let another_owner = has(
                &|event| matches!(event, Event::TeamOwnerChanged { team_ref: of, principal_ref } if of == team_ref && principal_ref != kept_for),
            );
            if its_owners_own || rotated || another_owner {
                assert_eq!(
                    now, None,
                    "{team_ref} keeps a key past {asked:?} (its owner's own: {its_owners_own}, a rotation: \
                     {rotated}, another owner: {another_owner})"
                );
                if its_owners_own {
                    counted.ended_by_their_owner += 1;
                    // The entry holds a record that the rule reads for it.
                    let with: Vec<&str> = events
                        .iter()
                        .filter_map(|event| ends_the_key(event, team_ref, kept_for))
                        .collect();
                    assert!(
                        !with.is_empty(),
                        "the key of {team_ref} ended by {asked:?}, with no record that ends one: {events:?}"
                    );
                    counted.ended_with.extend(with);
                }
            } else {
                // And with nothing else: not with another principal's request, not with the operator's
                // command that names no owner, and not with the take-in made again, which keeps it.
                assert_eq!(now, then, "what {team_ref} keeps changed by {asked:?}");
            }
        } else if now.is_some() {
            // A team that kept none keeps one by the join of a take-in that carries it.
            assert!(
                has(
                    &|event| matches!(event, Event::MemberJoined { team_ref: of, retry_key_hash, .. } if of == team_ref && !retry_key_hash.is_empty())
                ),
                "{team_ref} keeps a key that no join wrote, by {asked:?}"
            );
        }
        // The principal a team keeps a key for owns the team, and knows the way back to it.
        if let Some(kept) = &team.take_in_retry {
            assert_eq!(team.owner(), Some(kept.principal_ref.as_str()));
            assert_eq!(after.principals[&kept.principal_ref].retry_key_in, *team_ref);
        }
        // A take-in never takes a team from the owner it has.
        if let (Asked::TakeIn { .. }, Some(owner)) = (asked, was.and_then(|team| team.owner.as_deref())) {
            assert_eq!(
                team.owner(),
                Some(owner),
                "a take-in changed the owner of {team_ref}"
            );
        }
    }
    counted
}

/// The rules of a retry key over a walk of twenty thousand requests on two dozen teams, chosen by numbers
/// that are the same in every run: take-ins and joins with every pairing of the team's secret, an old
/// one, an invented one and a ticket with three keys and none, from callers the server knows, knew and
/// never knew, and between them every request that changes who owns a team, who is in it and what
/// opens it, the operator's included. Whatever is asked, a key is kept only for the owner a take-in
/// made; it ends with the first record that owner's own request makes, with a rotation and with another
/// owner, and with nothing else; a credential is issued for a principal that was there only to the
/// holder of that key and the team's secret, a credential is made unknown only by its holder or by that
/// take-in, and a take-in never changes the owner a team has. And the other way: every take-in that
/// carries the key a team keeps and the team's secret, from a caller the server does not know, is
/// answered for the principal the first one made, and every other one from such a caller for a
/// principal that is new.
#[test]
fn whatever_is_asked_a_retry_key_is_kept_for_the_owner_a_take_in_made_and_answers_for_nobody_else() {
    use std::collections::BTreeMap;
    const TEAMS: [&str; 24] = [
        "walk-00", "walk-01", "walk-02", "walk-03", "walk-04", "walk-05", "walk-06", "walk-07", "walk-08",
        "walk-09", "walk-10", "walk-11", "walk-12", "walk-13", "walk-14", "walk-15", "walk-16", "walk-17",
        "walk-18", "walk-19", "walk-20", "walk-21", "walk-22", "walk-23",
    ];
    const KEYS: [Option<&str>; 4] = [None, Some(KEY), Some(OTHER_KEY), Some(THIRD_KEY)];
    let mut rig = Rig::new();
    let mut choices = Choices(0x0328_0011_2026_1114);
    // What the walk knows: every secret and ticket a team ever had, the last the one it has; every
    // credential an answer ever carried, known to the server still or not; the last one it was given
    // for each principal; and who ever joined each team.
    let mut secrets: BTreeMap<&str, Vec<String>> = BTreeMap::new();
    let mut tickets: BTreeMap<&str, Vec<String>> = BTreeMap::new();
    let mut credentials: Vec<String> = Vec::new();
    let mut holds: BTreeMap<String, String> = BTreeMap::new();
    let mut joined: BTreeMap<&str, Vec<String>> = BTreeMap::new();
    let mut seen = Seen::default();
    let mut counted = Counted::default();
    let (mut honoured, mut refused, mut took_in) = (0, 0, 0);
    // The state the last restart read back from its snapshot, with the number of entries the log had
    // then; how many restarts were held against the log, and what they had of retry keys.
    let mut restart: Option<(State, usize)> = None;
    let (mut restarts, mut keys_in_a_snapshot, mut keys_changed_after_a_snapshot) = (0, 0, 0);
    for step in 0..20_000 {
        // Twenty minutes a step: the bound on failed joins holds nobody for the whole walk.
        rig.advance(1200);
        // A team more comes into the walk every eight hundred steps, so that each is taken in, or
        // imported, by a request of another kind.
        let team = TEAMS[choices.below((1 + step / 800).min(TEAMS.len()))];
        // One caller in forty is a principal some team keeps a key for, with the credential the walk
        // was given for it: its request ends the key whichever team it is about. Three times in four
        // it is about the team that keeps the key, the one team that principal is in: so the walk has
        // such an owner register a session, leave, transfer and revoke there, and not only join.
        let kept_for: Vec<(&'static str, &String)> = TEAMS
            .iter()
            .filter_map(|team| {
                let kept = rig.state.teams.get(*team)?.take_in_retry.as_ref()?;
                Some((*team, holds.get(&kept.principal_ref)?))
            })
            .collect();
        let (team, caller) = match choices.below(40) {
            0..=15 => (team, None),
            16..=17 => (team, Some("a-credential-the-server-never-issued".to_owned())),
            18 => match choices.one_of(&kept_for) {
                Some((its_team, credential)) => {
                    let credential = (*credential).clone();
                    let about = if choices.below(4) == 0 { team } else { *its_team };
                    (about, Some(credential))
                }
                None => (team, None),
            },
            _ => (team, choices.one_of(&credentials).cloned()),
        };
        let teams: BTreeMap<&str, TeamBefore> = TEAMS
            .iter()
            .filter_map(|team| {
                let held = rig.state.teams.get(*team)?;
                let team_before = TeamBefore {
                    owner: held.owner().map(str::to_owned),
                    join_secret_hash: held.join_secret_hash.clone(),
                    keeps: held
                        .take_in_retry
                        .as_ref()
                        .map(|kept| (kept.key_hash.clone(), kept.principal_ref.clone())),
                };
                Some((*team, team_before))
            })
            .collect();
        // A full copy of the state while it is small, for what a refused request leaves.
        let whole = (step < 1_500).then(|| rig.state.clone());
        let entries = rig.log.len();
        // The credential of the team's owner, when the walk holds one, and somebody who joined the team.
        let owner = teams
            .get(team)
            .and_then(|held| holds.get(held.owner.as_ref()?).cloned());
        let member = choices
            .one_of(joined.get(team).map_or(&[][..], Vec::as_slice))
            .cloned();
        let secret = {
            let known = secrets.get(team).map_or(&[][..], Vec::as_slice);
            match choices.below(20) {
                0..=11 if !known.is_empty() => known[known.len() - 1].clone(),
                12..=14 if known.len() > 1 => known[choices.below(known.len() - 1)].clone(),
                15..=17 => tickets
                    .get(team)
                    .and_then(|of_team| of_team.last())
                    .cloned()
                    .unwrap_or_else(|| secret_of(team, "never-a-ticket-of-the-team")),
                _ if teams.contains_key(team) => secret_of(team, &format!("invented-{step}")),
                _ => secret_of(team, &format!("first-{step}")),
            }
        };
        let mut asked = Asked::Other;
        let team_body = json!({"team_ref": team});
        // Whose credential the request carries: the caller's, the owner's, or nobody's for the
        // operator's commands.
        let chosen = choices.below(44);
        let carries = match chosen {
            0..=21 | 24..=25 | 30 | 40..=43 => caller.clone(),
            22..=23 | 26..=29 | 31..=33 => owner.clone(),
            _ => None,
        };
        let before = Before {
            teams,
            asks: carries
                .as_ref()
                .and_then(|credential| rig.state.principal_of(&sha256_hex(credential)))
                .map(str::to_owned),
        };
        let carried = carries.as_deref();
        let answer = match chosen {
            0..=17 => {
                // Every other take-in of a team that keeps a key carries that key: the request that is
                // made again is the one the walk is about.
                let kept = before.teams.get(team).and_then(|held| held.keeps.as_ref());
                let key = match (kept, choices.below(2)) {
                    (Some((key_hash, _)), 0) => KEYS
                        .into_iter()
                        .find(|key| key.is_some_and(|key| sha256_hex(key) == *key_hash))
                        .flatten(),
                    _ => KEYS[choices.below(KEYS.len())],
                };
                let mut body = json!({"join_secret": secret, "team_name": "ops"});
                if let Some(key) = key {
                    body["retry_key"] = json!(key);
                }
                asked = Asked::TakeIn {
                    team,
                    secret: secret.clone(),
                    key,
                };
                // One take-in in twelve is one that may not own, as a gateway's request inside the
                // server is.
                rig.may_own = choices.below(12) != 0;
                let answer = rig.call(carried, "/v1/team/take-in", body);
                rig.may_own = true;
                answer
            }
            18..=21 => rig.call(carried, "/v1/team/join", json!({"join_secret": secret})),
            22..=23 => rig.call(
                carried,
                "/v1/team/transfer",
                json!({"team_ref": team, "principal_ref": member}),
            ),
            24..=25 => rig.call(carried, "/v1/team/leave", team_body),
            26..=28 => {
                // A member revoked, banned or let back in, or every member of a secret revoked.
                let mut body = json!({"team_ref": team, "principal_ref": member});
                match choices.below(4) {
                    0 => {}
                    1 => body["ban"] = json!(true),
                    2 => body["ban"] = json!(false),
                    _ => body = json!({"team_ref": team, "joined_secret_version_lte": 1 + choices.below(3)}),
                }
                rig.call(carried, "/v1/team/revoke-member", body)
            }
            29 => rig.call(carried, "/v1/team/rotate-secret", team_body),
            30 => {
                asked = carries.clone().map_or(Asked::Other, Asked::RevokeOwn);
                rig.call(carried, "/v1/credential/revoke", json!({}))
            }
            31..=32 => rig.call(carried, "/v1/team/open-move", team_body),
            33 => rig.call(carried, "/v1/team/close-move", team_body),
            34 => rig.import("ops", &secret),
            35 => rig.take_back(team),
            36..=37 => rig.set_owner(team, member.as_deref().unwrap_or("nobody")),
            38 => rig.open_move(team, 1 + choices.below(40) as u32),
            39 => rig.close_move(team),
            40..=42 => rig.call(
                carried,
                "/v1/session/register",
                json!({
                    "team_ref": team, "harness": "claude-code", "harness_version": "2.1.251",
                    "session_name": format!("s{step}"), "activity": "busy", "inbound": "accept",
                }),
            ),
            _ => rig.call(carried, "/v1/team/create", json!({"team_name": "of-its-own"})),
        };
        // The principal a take-in must be answered for: the one the team keeps the key of, when the
        // request carries that key and the team's secret, from a caller the server does not know.
        let must_answer_for = match &asked {
            Asked::TakeIn {
                secret,
                key: Some(key),
                ..
            } if before.asks.is_none() => before.teams.get(team).and_then(|held| {
                let (key_hash, kept_for) = held.keeps.as_ref()?;
                (*key_hash == sha256_hex(key) && held.join_secret_hash == sha256_hex(secret))
                    .then(|| kept_for.clone())
            }),
            _ => None,
        };
        let entry = rig.log.get(entries);
        match entry {
            Some(entry) => {
                let step_counted =
                    held_to_the_rules_of_a_retry_key(&before, &seen, &rig.state, entry, &asked);
                counted.kept += step_counted.kept;
                counted.repeated += step_counted.repeated;
                counted.ended_by_their_owner += step_counted.ended_by_their_owner;
                counted.ended_with.extend(step_counted.ended_with);
            }
            None => {
                if let Some(whole) = whole {
                    assert_eq!(
                        whole, rig.state,
                        "step {step} made no entry and changed the state"
                    );
                }
            }
        }
        match answer {
            Ok(result) => {
                // What the walk learns of an answer: a credential, a secret, a ticket.
                if let Some(credential) = result.get("credential").and_then(Value::as_str) {
                    credentials.push(credential.to_owned());
                    holds.insert(text(&result, "principal_ref"), credential.to_owned());
                }
                if let (Some(new_secret), Some(of_team)) = (
                    result.get("join_secret").and_then(Value::as_str),
                    result
                        .get("team_ref")
                        .and_then(Value::as_str)
                        .and_then(|of| TEAMS.iter().find(|team| **team == of)),
                ) {
                    secrets.entry(of_team).or_default().push(new_secret.to_owned());
                }
                if let Some(ticket) = result.get("ticket").and_then(Value::as_str) {
                    tickets.entry(team).or_default().push(ticket.to_owned());
                }
                if result.get("taken_in") == Some(&json!(true))
                    || result.get("imported") == Some(&json!(true))
                {
                    secrets.entry(team).or_default().push(secret.clone());
                    took_in += 1;
                }
                // An answer says `owner` truly, and a caller the server knew is answered for itself,
                // with no credential.
                if let Asked::TakeIn { .. } = &asked {
                    let answered = text(&result, "principal_ref");
                    assert_eq!(
                        result["owner"],
                        json!(rig.state.teams[team].owner() == Some(answered.as_str())),
                        "step {step}"
                    );
                    match (&before.asks, &must_answer_for) {
                        (Some(known), _) => {
                            assert_eq!(&answered, known, "step {step}");
                            assert!(result.get("credential").is_none(), "step {step}");
                        }
                        // The take-in made again is answered for the principal the first one made,
                        // which owns the team, with a credential.
                        (None, Some(kept_for)) => {
                            assert_eq!(&answered, kept_for, "step {step}");
                            assert_eq!(result["owner"], json!(true), "step {step}");
                            assert!(result.get("credential").is_some(), "step {step}");
                            honoured += 1;
                        }
                        // And any other, from a caller the server does not know, is a new principal's.
                        (None, None) => assert!(
                            !seen.principals.contains(&answered),
                            "step {step}: a stranger was answered for a principal that was there"
                        ),
                    }
                }
            }
            Err(_) => {
                assert_eq!(
                    rig.log.len(),
                    entries,
                    "step {step} was refused and made an entry"
                );
                assert_eq!(
                    must_answer_for, None,
                    "step {step}: a take-in made again was refused"
                );
                refused += 1;
            }
        }
        // What the walk has seen the server make, for the steps after this one.
        for record in entry.map_or(&[][..], |entry| &entry.records[..]) {
            match &record.event {
                Event::CredentialIssued {
                    principal_ref,
                    credential_hash,
                } => {
                    seen.principals.insert(principal_ref.clone());
                    seen.named.insert(credential_hash.clone(), principal_ref.clone());
                }
                Event::MemberJoined {
                    team_ref,
                    principal_ref,
                    ..
                } => {
                    if let Some(of_team) = TEAMS.iter().find(|team| *team == team_ref) {
                        joined.entry(of_team).or_default().push(principal_ref.clone());
                    }
                }
                _ => {}
            }
        }
        // Now and then, the way from every principal back to the team that keeps its key: there are
        // as many of them as there are keys.
        if step % 500 == 499 {
            let ways = rig
                .state
                .principals
                .values()
                .filter(|principal| !principal.retry_key_in.is_empty())
                .count();
            let keys = rig
                .state
                .teams
                .values()
                .filter(|team| team.take_in_retry.is_some())
                .count();
            assert_eq!(ways, keys, "step {step}");
        }
        // What lets the rule read so few kinds of record: a principal that a team keeps a key for is an
        // active member of that team, is in no other, and has no session anywhere. So no heartbeat, no
        // close, no resume and no message can be its first record: its first session ends the key.
        for (team_ref, team) in &rig.state.teams {
            let Some(kept) = &team.take_in_retry else {
                continue;
            };
            assert!(team.active(&kept.principal_ref), "step {step}: {team_ref}");
            for (other_ref, other) in &rig.state.teams {
                assert!(
                    other_ref == team_ref || !other.members.contains_key(&kept.principal_ref),
                    "step {step}: the principal {team_ref} keeps a key for is a member of {other_ref}"
                );
                assert!(
                    other
                        .sessions
                        .values()
                        .all(|session| session.principal_ref != kept.principal_ref),
                    "step {step}: the principal {team_ref} keeps a key for has a session in {other_ref}"
                );
            }
        }
        // A restart, every 199 steps: the snapshot of the restart before it, with the entries the log has
        // taken since, is the state the server holds, the keys and the ways back to them included. The
        // walk then goes on from that snapshot read back, as a server does that was stopped and started.
        if step % 199 == 198 {
            if let Some((mut read, entries)) = restart.take() {
                let keys = |state: &State| -> BTreeMap<String, TakeInRetry> {
                    state
                        .teams
                        .iter()
                        .filter_map(|(team_ref, team)| Some((team_ref.clone(), team.take_in_retry.clone()?)))
                        .collect()
                };
                let then = keys(&read);
                for entry in &rig.log[entries..] {
                    read.replay(&Entry::decode(&entry.encode()).unwrap()).unwrap();
                }
                assert_eq!(
                    read, rig.state,
                    "step {step}: a snapshot and the log after it make another state"
                );
                restarts += 1;
                // A restart proves the most where the snapshot held a key, and where the log after it
                // kept a key or ended one.
                let now = keys(&read);
                keys_in_a_snapshot += then.len();
                keys_changed_after_a_snapshot += then
                    .keys()
                    .chain(now.keys())
                    .collect::<std::collections::BTreeSet<_>>()
                    .into_iter()
                    .filter(|team_ref| then.get(*team_ref) != now.get(*team_ref))
                    .count();
            }
            let snapshot = serde_json::to_vec(&rig.state).unwrap();
            rig.state = serde_json::from_slice(&snapshot).unwrap();
            restart = Some((rig.state.clone(), rig.log.len()));
        }
    }
    assert!(
        restarts >= 90 && keys_in_a_snapshot >= 20 && keys_changed_after_a_snapshot >= 40,
        "the walk restarted {restarts} times: {keys_in_a_snapshot} keys were in a snapshot, and the log \
         after a snapshot kept or ended {keys_changed_after_a_snapshot}"
    );
    // The walk went where the rules are: keys kept, take-ins made again, keys their owner's own request
    // ended, requests refused, and a log that makes the same state when it is read again.
    let Counted {
        kept,
        repeated,
        ended_by_their_owner,
        ended_with,
    } = counted;
    // Every kind of record that ends a key ended one, in an entry its owner's own request made.
    assert_eq!(ended_with.into_iter().collect::<Vec<_>>(), ENDS_A_KEY);
    assert!(
        kept >= 50
            && repeated >= 20
            && honoured == repeated
            && ended_by_their_owner >= 50
            && refused >= 2_000
            && took_in >= 20,
        "the walk kept {kept} keys, made {repeated} take-ins again and expected {honoured}, saw \
         {ended_by_their_owner} keys ended by their owner's own request, was refused {refused} times and \
         took {took_in} teams in"
    );
    assert_eq!(replayed(&rig), rig.state);
}
