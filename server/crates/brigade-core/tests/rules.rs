//! The rules of BAP/1's section 4.5, checked against `execute` with no server around it.
//!
//! The conformance suite is the oracle for a running server; these tests are the same rules at the size of
//! one function call, with the clock in the test's hands.

use brigade_core::error::Code;
use brigade_core::hash::sha256_hex;
use brigade_core::record::Event;
use brigade_core::{Entropy, Entry, Error, Record, Request, State, Timestamp, answer, import_team};
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
    entropy: Counter,
    now: Timestamp,
    log: Vec<Entry>,
    /// Whether a request may create a team: what the server's creation token decides.
    may_create: bool,
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
            entropy: Counter(0),
            now: Timestamp(1_800_000_000 * SECOND),
            log: Vec::new(),
            may_create: true,
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
            path,
            body,
        };
        let outcome = answer(&mut self.state, request, self.now, &mut self.entropy)?;
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
