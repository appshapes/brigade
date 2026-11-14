//! The server as a process: started, asked over HTTP, killed and started again.
//!
//! The rules themselves are `brigade-core`'s tests and the conformance suite's. These tests are what only a
//! running process can show: that what was answered is still there after a kill, that a receive that waits
//! is answered when it should be, and that the listener does what the operator told it.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde_json::{Value, json};

mod common;

use common::{Scratch, Server, log_arguments, parse, request, text};

/// A member of a team on a server: its credential, its team and one session.
struct Member {
    credential: String,
    team_ref: String,
    session: String,
}

fn register(server: &Server, credential: &str, team_ref: &str, name: &str) -> String {
    let registration = json!({
        "team_ref": team_ref, "harness": "test", "harness_version": "1", "session_name": name,
        "activity": "busy", "inbound": "accept",
    });
    text(
        &server.ok(credential, "/v1/session/register", &registration),
        "session_id",
    )
}

/// Creates a team with two members, each with one session.
fn team_of_two(server: &Server) -> (Member, Member, String) {
    let (status, created) = server.post(
        None,
        "/v1/team/create",
        &json!({"team_name": "ops", "human_label": "alice"}),
    );
    assert_eq!(status, 200, "{created}");
    let created = &created["result"];
    let (secret, team_ref) = (text(created, "join_secret"), text(created, "team_ref"));
    let alice = text(created, "credential");
    let (status, joined) = server.post(
        None,
        "/v1/team/join",
        &json!({"join_secret": secret, "human_label": "bob"}),
    );
    assert_eq!(status, 200, "{joined}");
    let bob = text(&joined["result"], "credential");
    let a = register(server, &alice, &team_ref, "alpha");
    let b = register(server, &bob, &team_ref, "beta");
    (
        Member {
            credential: alice,
            team_ref: team_ref.clone(),
            session: a,
        },
        Member {
            credential: bob,
            team_ref,
            session: b,
        },
        secret,
    )
}

fn send(server: &Server, from: &Member, to: &Member, body: &str, key: &str) -> Value {
    let request = json!({
        "sender_session_id": from.session, "recipient_session_id": to.session, "body": body, "idempotency_key": key,
    });
    server.ok(&from.credential, "/v1/message/send", &request)
}

fn bodies(result: &Value) -> Vec<String> {
    result["messages"]
        .as_array()
        .unwrap()
        .iter()
        .map(|message| text(message, "body"))
        .collect()
}

/// What was answered is still there after the process is killed, and after it is stopped and started.
fn everything_answered_survives(log: &str) {
    let scratch = Scratch::new(&format!("survives-{log}"));
    let arguments = log_arguments(log, &scratch);
    let server = Server::start(&scratch, &arguments);
    let (alice, bob, secret) = team_of_two(&server);
    let first = send(&server, &alice, &bob, "acknowledged before the kill", "k1");
    let second = send(&server, &alice, &bob, "still unacknowledged", "k2");
    send(&server, &alice, &bob, "and this one", "k3");
    let acked = json!({"session_id": bob.session, "message_ids": [first["message_id"]]});
    server.ok(&bob.credential, "/v1/message/ack", &acked);
    let before = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session, "limit": 1}),
    );
    let cursor = text(&before, "cursor");
    server.kill();

    let server = Server::start(&scratch, &arguments);
    // Every unacknowledged message is there, in order, with the id it had; the acknowledged one is not.
    let after = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!(bodies(&after), ["still unacknowledged", "and this one"]);
    assert_eq!(after["messages"][0]["message_id"], second["message_id"]);
    assert_eq!(text(&after, "cursor"), cursor);
    // The cursor from before the kill still stands for what it stood for.
    send(&server, &alice, &bob, "after the restart", "k4");
    let since = json!({"session_id": bob.session, "after": cursor});
    assert_eq!(
        bodies(&server.ok(&bob.credential, "/v1/message/receive", &since)),
        ["after the restart"]
    );
    // An idempotency key from before the kill answers with the first send, and an acknowledgement is kept.
    let again = send(&server, &alice, &bob, "acknowledged before the kill", "k1");
    assert_eq!(
        (again["duplicate"].clone(), again["message_id"].clone()),
        (json!(true), first["message_id"].clone())
    );
    assert_eq!(
        server.ok(&bob.credential, "/v1/message/ack", &acked),
        json!({"acked": [first["message_id"]], "unknown": []})
    );
    // The team, its members, their sessions and the join secret are as they were.
    let listed = server.ok(
        &alice.credential,
        "/v1/session/list",
        &json!({"team_ref": alice.team_ref}),
    );
    assert_eq!(listed["sessions"].as_array().unwrap().len(), 2);
    let (status, _) = server.post(None, "/v1/team/join", &json!({"join_secret": secret}));
    assert_eq!(status, 200);

    // A stop writes a snapshot, and a start from it holds the same.
    server.stop();
    let snapshots = std::fs::read_dir(scratch.path("data").join("snapshots"))
        .unwrap()
        .count();
    assert!(snapshots >= 1);
    let server = Server::start(&scratch, &arguments);
    let after = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!(
        bodies(&after),
        ["still unacknowledged", "and this one", "after the restart"]
    );
    let members = server.ok(
        &alice.credential,
        "/v1/team/members",
        &json!({"team_ref": alice.team_ref}),
    );
    assert_eq!(members["members"].as_array().unwrap().len(), 3);
    server.stop();
}

#[test]
fn everything_answered_survives_a_kill_on_the_file_log() {
    everything_answered_survives("file");
}

#[cfg(feature = "cavalry")]
#[test]
fn everything_answered_survives_a_kill_on_the_cavalry_feed() {
    everything_answered_survives("cavalry");
}

/// A process held still for longer than Cavalry's liveness timeouts, as a virtual machine is while it is
/// moved, comes back with its Cavalry client closed. The next append connects again and loses nothing.
#[cfg(feature = "cavalry")]
#[test]
fn a_server_that_was_held_still_goes_on_where_it_stopped() {
    let scratch = Scratch::new("held-still");
    let arguments = log_arguments("cavalry", &scratch);
    let server = Server::start(&scratch, &arguments);
    let (alice, bob, _) = team_of_two(&server);
    send(&server, &alice, &bob, "before the pause", "p1");

    let signal = |name: &str| {
        let status = Command::new("kill")
            .arg(name)
            .arg(server.child.id().to_string())
            .status()
            .unwrap();
        assert!(status.success());
    };
    signal("-STOP");
    std::thread::sleep(Duration::from_secs(12));
    signal("-CONT");

    send(&server, &alice, &bob, "after the pause", "p2");
    send(&server, &alice, &bob, "and one more", "p3");
    let pending = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!(
        bodies(&pending),
        ["before the pause", "after the pause", "and one more"]
    );
    // The pause was long enough to close the client, so this is the path that was taken.
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(said.contains("connected again"), "{said}");
    server.kill();

    // What the feed holds after all that is each record once, in order.
    let server = Server::start(&scratch, &arguments);
    let pending = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!(
        bodies(&pending),
        ["before the pause", "after the pause", "and one more"]
    );
    server.stop();
}

/// A feed is rolled when a snapshot is taken, and the recordings every kept snapshot stands for are let go
/// of. With a snapshot every few records and a roll at every one, a short run does both many times; what was
/// answered is all there after a kill, and the feed's directory does not grow with every recording.
#[cfg(feature = "cavalry")]
#[test]
fn the_feed_is_rolled_and_let_go_of_as_snapshots_cover_it() {
    let scratch = Scratch::new("rolled");
    let mut arguments = log_arguments("cavalry", &scratch);
    arguments.extend(["--snapshot-every".to_owned(), "4".to_owned()]);
    arguments.extend(["--feed-roll-bytes".to_owned(), "1".to_owned()]);
    let server = Server::start(&scratch, &arguments);
    let (alice, bob, _) = team_of_two(&server);
    let mut kept = Vec::new();
    // Eighteen sends stay inside a session's minute; the heartbeats between them are records too.
    for number in 0..18 {
        let sent = send(
            &server,
            &alice,
            &bob,
            &format!("message {number}"),
            &format!("r{number}"),
        );
        if number % 8 == 0 {
            kept.push(format!("message {number}"));
        } else {
            let acked = json!({"session_id": bob.session, "message_ids": [sent["message_id"]]});
            server.ok(&bob.credential, "/v1/message/ack", &acked);
        }
        for member in [&alice, &bob] {
            let beat = json!({"session_id": member.session, "heartbeat": {}});
            server.ok(&member.credential, "/v1/session/heartbeat", &beat);
        }
    }
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    let rolls = said.matches("was rolled at position").count();
    assert!(rolls >= 10, "{rolls} rolls:\n{said}");
    assert!(said.contains("let go of"), "{said}");
    assert!(!said.contains("was not let go of"), "{said}");
    // Each recording has a file of its own. With none let go of there would be one for every roll.
    let files = std::fs::read_dir(scratch.path("data").join("feed"))
        .unwrap()
        .count();
    assert!(
        files < rolls,
        "{files} files in the feed's directory after {rolls} rolls"
    );
    server.kill();

    let server = Server::start(&scratch, &arguments);
    let pending = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!(bodies(&pending), kept);
    // A key from the first messages still answers with the first send: the state came through the snapshots.
    let again = send(&server, &alice, &bob, "message 0", "r0");
    assert_eq!(again["duplicate"], json!(true));
    send(&server, &alice, &bob, "after the restart", "r-after");
    server.stop();

    let server = Server::start(&scratch, &arguments);
    let pending = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    kept.push("after the restart".to_owned());
    assert_eq!(bodies(&pending), kept);
    server.stop();
}

#[test]
fn a_receive_that_waits_is_answered_when_it_should_be() {
    let scratch = Scratch::new("waits");
    let server = Arc::new(Server::start(&scratch, &log_arguments("file", &scratch)));
    let (alice, bob, _) = team_of_two(&server);
    let waiting = |member: &Member| {
        let (server, credential) = (Arc::clone(&server), member.credential.clone());
        let request = json!({"session_id": member.session, "wait_seconds": 20});
        std::thread::spawn(move || {
            let started = Instant::now();
            let (status, envelope) = server.post(Some(&credential), "/v1/message/receive", &request);
            (status, envelope, started.elapsed())
        })
    };

    // With nothing to wait for, and with no wait asked, the answer is at once.
    let (status, envelope) = server.post(
        Some(&bob.credential),
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!((status, envelope["result"]["messages"].clone()), (200, json!([])));

    // A message accepted while the request waits is its answer.
    let held = waiting(&bob);
    std::thread::sleep(Duration::from_millis(400));
    send(&server, &alice, &bob, "while you waited", "w1");
    let (status, envelope, took) = held.join().unwrap();
    assert_eq!(status, 200);
    assert_eq!(bodies(&envelope["result"]), ["while you waited"]);
    assert!(took < Duration::from_secs(5), "answered after {took:?}");

    // A wait that sees nothing ends when its time has passed, with an empty answer and a cursor.
    let request = json!({"session_id": alice.session, "wait_seconds": 1});
    let started = Instant::now();
    let (status, envelope) = server.post(Some(&alice.credential), "/v1/message/receive", &request);
    assert_eq!((status, envelope["result"]["messages"].clone()), (200, json!([])));
    assert!(envelope["result"]["cursor"].is_string());
    assert!(
        started.elapsed() >= Duration::from_millis(900),
        "{:?}",
        started.elapsed()
    );

    // A member that leaves is refused within the second, although its request had long to wait.
    let held = waiting(&alice);
    std::thread::sleep(Duration::from_millis(400));
    server.ok(
        &alice.credential,
        "/v1/team/leave",
        &json!({"team_ref": alice.team_ref}),
    );
    let (status, envelope, took) = held.join().unwrap();
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (403, "unauthorized")
    );
    assert!(took < Duration::from_secs(5), "answered after {took:?}");

    // So is one whose credential is revoked. The message in bob's inbox is acknowledged first, so that the
    // request has nothing to answer with and waits.
    let pending = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    let acked = json!({"session_id": bob.session, "message_ids": [pending["messages"][0]["message_id"]]});
    server.ok(&bob.credential, "/v1/message/ack", &acked);
    let held = waiting(&bob);
    std::thread::sleep(Duration::from_millis(400));
    server.ok(&bob.credential, "/v1/credential/revoke", &json!({}));
    let (status, envelope, took) = held.join().unwrap();
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (401, "unauthenticated")
    );
    assert!(took < Duration::from_secs(5), "answered after {took:?}");
}

#[test]
fn the_transport_is_the_contracts() {
    let scratch = Scratch::new("transport");
    let server = Server::start(&scratch, &log_arguments("file", &scratch));
    let authority = server.url.strip_prefix("http://").unwrap().to_owned();
    let raw = |bytes: &[u8]| {
        let mut stream = TcpStream::connect(&authority).unwrap();
        stream.set_read_timeout(Some(Duration::from_secs(30))).unwrap();
        stream.write_all(bytes).unwrap();
        let mut response = Vec::new();
        stream.read_to_end(&mut response).unwrap();
        parse(&response)
    };

    // What the server is, with no credential: the contract, its own name and version, and that it carries
    // the administration endpoints, which the adapter asks before it sends an administration command.
    let (status, info) = server.post(None, "/v1/info", &json!({}));
    assert_eq!(status, 200);
    assert_eq!(info["result"]["contract_version"], json!("1"));
    assert_eq!(info["result"]["server"]["name"], json!("brigade-server"));
    assert_eq!(
        info["result"]["server"]["version"],
        json!(env!("CARGO_PKG_VERSION"))
    );
    assert_eq!(info["result"]["capabilities"], json!(["team.admin"]));

    // An unknown path is 404 whatever the method; a known one that is not a POST is refused.
    let (status, envelope) = server.post(None, "/v1/team/delete", &json!({}));
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (404, "not_found")
    );
    let (status, _) = raw(format!("GET /nowhere HTTP/1.1\r\nHost: {authority}\r\n\r\n").as_bytes());
    assert_eq!(status, 404);
    let (status, envelope) = raw(format!("GET /v1/info HTTP/1.1\r\nHost: {authority}\r\n\r\n").as_bytes());
    assert_eq!(
        (status, text(&envelope["error"]["details"], "field").as_str()),
        (400, "method")
    );

    // No credential, and one the server does not know, are 401 on everything but create and join.
    for credential in [None, Some("0123456789abcdef0123456789abcdef")] {
        let (status, envelope) = server.post(credential, "/v1/session/list", &json!({"team_ref": "t"}));
        assert_eq!(
            (status, text(&envelope["error"], "code").as_str()),
            (401, "unauthenticated")
        );
        assert_eq!(envelope["error"]["retryable"], json!(false));
    }

    // The credential is looked at before the request's shape: a request with both wrong is told the first.
    let (status, envelope) = server.post(None, "/v1/session/heartbeat", &json!({}));
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (401, "unauthenticated")
    );
    let forged = json!({"sender": {"principal_ref": "p"}});
    let (status, _) = server.post(
        Some("0123456789abcdef0123456789abcdef"),
        "/v1/message/send",
        &forged,
    );
    assert_eq!(status, 401);

    // A body that is not one JSON object, and one over the megabyte, are refused as the request's fault.
    let (status, envelope) = raw(&request(&authority, None, "/v1/team/create", "[1, 2", &[]));
    assert_eq!(
        (status, text(&envelope["error"]["details"], "reason").as_str()),
        (400, "malformed_json")
    );
    let long = format!("{{\"team_name\":\"{}\"}}", "x".repeat(1 << 20));
    let (status, envelope) = raw(&request(&authority, None, "/v1/team/create", &long, &[]));
    assert_eq!(
        (status, text(&envelope["error"]["details"], "reason").as_str()),
        (400, "too_long")
    );
    // A request sent in pieces with no length named is read like any other.
    let chunked = format!(
        "POST /v1/team/create HTTP/1.1\r\nHost: {authority}\r\nTransfer-Encoding: chunked\r\n\r\n\
         13\r\n{{\"team_name\":\"ops\"}}\r\n0\r\n\r\n"
    );
    let (status, envelope) = raw(chunked.as_bytes());
    assert_eq!(status, 200, "{envelope}");
    assert!(envelope["result"]["credential"].is_string());

    // The statuses the contract pairs with the codes that need a team to show.
    let (alice, bob, _) = team_of_two(&server);
    let cases = [
        (
            "/v1/session/list",
            json!({"team_ref": "no-such-team"}),
            403,
            "unauthorized",
        ),
        (
            "/v1/session/close",
            json!({"session_id": bob.session}),
            404,
            "not_found",
        ),
        (
            "/v1/session/register",
            json!({"team_ref": alice.team_ref}),
            400,
            "invalid_input",
        ),
    ];
    for (path, body, expected, code) in cases {
        let (status, envelope) = server.post(Some(&alice.credential), path, &body);
        assert_eq!(
            (status, text(&envelope["error"], "code").as_str()),
            (expected, code),
            "{path}"
        );
    }
    server.ok(
        &alice.credential,
        "/v1/session/close",
        &json!({"session_id": alice.session}),
    );
    let beat = json!({"session_id": alice.session, "heartbeat": {}});
    let (status, envelope) = server.post(Some(&alice.credential), "/v1/session/heartbeat", &beat);
    assert_eq!(
        (status, text(&envelope["error"]["details"], "reason").as_str()),
        (409, "session_closed")
    );
}

#[test]
fn a_creation_token_is_needed_where_the_operator_set_one() {
    let scratch = Scratch::new("token");
    let token_file = scratch.path("create-token");
    std::fs::write(&token_file, "let-me-make-a-team\n").unwrap();
    let mut arguments = log_arguments("file", &scratch);
    arguments.extend(["--create-token-file".to_owned(), token_file.display().to_string()]);
    let server = Server::start(&scratch, &arguments);
    let authority = server.url.strip_prefix("http://").unwrap().to_owned();
    let create = |headers: &[(&str, &str)]| {
        let mut stream = TcpStream::connect(&authority).unwrap();
        stream
            .write_all(&request(
                &authority,
                None,
                "/v1/team/create",
                r#"{"team_name":"ops"}"#,
                headers,
            ))
            .unwrap();
        let mut response = Vec::new();
        stream.read_to_end(&mut response).unwrap();
        parse(&response)
    };
    let (status, envelope) = create(&[]);
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (403, "unauthorized")
    );
    let (status, _) = create(&[("Brigade-Create-Token", "not-the-token")]);
    assert_eq!(status, 403);
    let (status, envelope) = create(&[("Brigade-Create-Token", "let-me-make-a-team")]);
    assert_eq!(status, 200, "{envelope}");
    // The token gates creating a team and nothing else: joining one needs the team's secret alone.
    let secret = text(&envelope["result"], "join_secret");
    let (status, _) = server.post(None, "/v1/team/join", &json!({"join_secret": secret}));
    assert_eq!(status, 200);
    // Nothing the server wrote about itself holds the token.
    assert!(
        !std::fs::read_to_string(scratch.path("stderr"))
            .unwrap()
            .contains("let-me-make-a-team")
    );
}

#[test]
fn a_stop_answers_what_is_being_answered_and_a_lost_key_is_never_replaced() {
    let scratch = Scratch::new("stop");
    let arguments = log_arguments("file", &scratch);
    let server = Arc::new(Server::start(&scratch, &arguments));
    let (alice, bob, _) = team_of_two(&server);
    send(&server, &alice, &bob, "kept across the stop", "s1");

    // A receive that has long to wait is answered at the stop, with the empty answer it had, and the
    // server ends cleanly.
    let held = {
        let (server, credential) = (Arc::clone(&server), alice.credential.clone());
        let request = json!({"session_id": alice.session, "wait_seconds": 30});
        std::thread::spawn(move || {
            let started = Instant::now();
            let (status, envelope) = server.post(Some(&credential), "/v1/message/receive", &request);
            (status, envelope, started.elapsed())
        })
    };
    std::thread::sleep(Duration::from_millis(400));
    let pid = server.child.id().to_string();
    assert!(
        Command::new("kill")
            .arg("-TERM")
            .arg(&pid)
            .status()
            .unwrap()
            .success()
    );
    let (status, envelope, took) = held.join().unwrap();
    assert_eq!(
        (status, envelope["result"]["messages"].clone()),
        (200, json!([])),
        "{envelope}"
    );
    assert!(took < Duration::from_secs(10), "answered after {took:?}");
    let mut server = Arc::try_unwrap(server)
        .ok()
        .expect("the only holder of the server");
    assert!(server.child.wait().unwrap().success());

    // The key is what opens the log. A directory that holds a log and no key is refused, not given a new
    // key that opens nothing.
    let key = scratch.path("data").join("record.key");
    let kept = std::fs::read(&key).unwrap();
    std::fs::remove_file(&key).unwrap();
    let refused = Server::try_start(&scratch, &arguments)
        .err()
        .expect("a start with no key");
    assert!(refused.contains("no record.key"), "{refused}");
    assert!(!key.exists());
    std::fs::write(&key, kept).unwrap();
    let server = Server::start(&scratch, &arguments);
    let pending = server.ok(
        &bob.credential,
        "/v1/message/receive",
        &json!({"session_id": bob.session}),
    );
    assert_eq!(bodies(&pending), ["kept across the stop"]);
    server.stop();
}

#[test]
fn one_directory_has_one_server() {
    let scratch = Scratch::new("one");
    let arguments = log_arguments("file", &scratch);
    let _first = Server::start(&scratch, &arguments);
    let second = Server::try_start(&scratch, &arguments)
        .err()
        .expect("a second server on one directory");
    assert!(second.contains("another server is running"), "{second}");
}

/// A reference as a backend that names its teams with UUIDs makes one.
const ELSEWHERE: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";

/// Writes the join secret of a team that lives elsewhere to a file of the test's own, as a member's secret
/// file holds it, and returns the file and the secret. The secret is put together here, because the
/// repository's secret scan refuses a file that holds a whole one.
fn secret_file(scratch: &Scratch, team_ref: &str, random: &str) -> (PathBuf, String) {
    let secret = format!("brg1.{team_ref}.{random}");
    let file = scratch.path(&format!("{random}.join-secret"));
    std::fs::write(&file, format!("{secret}\n")).unwrap();
    (file, secret)
}

/// Runs the command that takes a team in on `scratch`'s data directory, and returns its status and
/// everything it wrote.
fn take_in(scratch: &Scratch, log: &[String], name: &str, secret_file: &Path) -> (Option<i32>, String) {
    let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
        .arg("--data")
        .arg(scratch.path("data"))
        .args(log)
        .args(["--import-team", name, "--join-secret-file"])
        .arg(secret_file)
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let wrote = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    (output.status.code(), wrote)
}

#[test]
fn a_team_that_lives_elsewhere_is_taken_in_and_joined_with_the_secret_it_has() {
    let scratch = Scratch::new("import");
    let arguments = log_arguments("file", &scratch);
    let (file, secret) = secret_file(&scratch, ELSEWHERE, "kept-from-the-other-backend");

    // On a directory that holds nothing yet, the command takes the team in, says so and ends. It listens on
    // nothing, and the engine leaves its snapshot behind as it does at every stop.
    let (status, wrote) = take_in(&scratch, &arguments, "ops", &file);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!("took in team \"ops\" ({ELSEWHERE})")),
        "{wrote}"
    );
    assert!(!wrote.contains("listening on"), "{wrote}");
    assert!(!wrote.contains("kept-from-the-other-backend"), "{wrote}");
    assert!(!scratch.path("address").exists());
    let snapshots = std::fs::read_dir(scratch.path("data").join("snapshots"))
        .unwrap()
        .count();
    assert_eq!(snapshots, 1);

    // A server on that directory has the team: a member joins with the secret it already has, and its
    // session is registered and listed as on a team that was created here.
    let server = Server::start(&scratch, &arguments);
    let (status, joined) = server.post(
        None,
        "/v1/team/join",
        &json!({"join_secret": secret, "human_label": "alice"}),
    );
    assert_eq!(status, 200, "{joined}");
    let joined = &joined["result"];
    assert_eq!(
        (
            text(joined, "team_ref").as_str(),
            text(joined, "team_name").as_str()
        ),
        (ELSEWHERE, "ops")
    );
    let credential = text(joined, "credential");
    let session = register(&server, &credential, ELSEWHERE, "alpha");
    let listed = server.ok(&credential, "/v1/session/list", &json!({"team_ref": ELSEWHERE}));
    assert_eq!(listed["team_name"], json!("ops"));
    let sessions = listed["sessions"].as_array().unwrap();
    assert_eq!(sessions.len(), 1);
    assert_eq!(sessions[0]["session_id"], json!(session));

    // While the server runs on the directory nothing is taken in beside it: the command is refused in the
    // words a second server is, and the team it named is not there.
    let (other_file, other) = secret_file(
        &scratch,
        "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e",
        "never-taken-in-anywhere",
    );
    let (status, wrote) = take_in(&scratch, &arguments, "other", &other_file);
    assert_eq!(status, Some(1), "{wrote}");
    assert!(wrote.contains("another server is running"), "{wrote}");
    assert!(!wrote.contains("took in team"), "{wrote}");
    assert!(!wrote.contains("never-taken-in-anywhere"), "{wrote}");
    let (status, envelope) = server.post(None, "/v1/team/join", &json!({"join_secret": other}));
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (403, "unauthorized")
    );
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(!said.contains("kept-from-the-other-backend"), "{said}");
    assert!(!said.contains("never-taken-in-anywhere"), "{said}");
    server.stop();

    // The same secret again, under whatever name: the team is already here, and it is left as it is.
    let (status, wrote) = take_in(&scratch, &arguments, "renamed", &file);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!("team \"ops\" ({ELSEWHERE}) is already here")),
        "{wrote}"
    );
    assert!(!wrote.contains("took in team"), "{wrote}");
    assert!(!wrote.contains("kept-from-the-other-backend"), "{wrote}");

    let server = Server::start(&scratch, &arguments);
    let members = server.ok(&credential, "/v1/team/members", &json!({"team_ref": ELSEWHERE}));
    assert_eq!(members["team_name"], json!("ops"));
    assert_eq!(members["members"].as_array().unwrap().len(), 1);
    // The command that was refused while the server ran left nothing behind either.
    let (status, _) = server.post(None, "/v1/team/join", &json!({"join_secret": other}));
    assert_eq!(status, 403);
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(!said.contains("kept-from-the-other-backend"), "{said}");
    server.stop();
}

#[test]
fn a_team_is_not_taken_in_from_a_file_that_holds_no_join_secret_for_it() {
    let scratch = Scratch::new("import-refused");
    let arguments = log_arguments("file", &scratch);
    let refused = |file: &Path, why: &str| {
        let (status, wrote) = take_in(&scratch, &arguments, "ops", file);
        assert_eq!(status, Some(1), "{wrote}");
        assert!(wrote.contains(why), "{wrote}");
        assert!(!wrote.contains("took in team"), "{wrote}");
        wrote
    };

    // A file that is not there, and one that holds nothing but the end of a line.
    refused(&scratch.path("no-such-file"), "could not be read");
    let empty = scratch.path("empty");
    std::fs::write(&empty, " \n").unwrap();
    refused(&empty, "holds no join secret");
    // A text that is no join secret is refused, and no sentence repeats what the file held.
    let not_one = scratch.path("not-one");
    std::fs::write(&not_one, "what-the-file-held is no join secret\n").unwrap();
    let wrote = refused(
        &not_one,
        "the team was not taken in: invalid_input: join_secret is malformed",
    );
    assert!(!wrote.contains("what-the-file-held"), "{wrote}");

    // A team that is here under one secret is not taken in under another, and neither secret is repeated.
    let (file, secret) = secret_file(&scratch, ELSEWHERE, "kept-from-the-other-backend");
    let (status, wrote) = take_in(&scratch, &arguments, "ops", &file);
    assert_eq!(status, Some(0), "{wrote}");
    let (other_file, other) = secret_file(&scratch, ELSEWHERE, "another-secret-for-the-reference");
    let wrote = refused(
        &other_file,
        "conflict: a team of that reference is here under another join secret",
    );
    assert!(!wrote.contains("another-secret-for-the-reference"), "{wrote}");
    assert!(!wrote.contains("kept-from-the-other-backend"), "{wrote}");

    // The command serves nothing, so it is refused with what serving takes, before it touches a directory.
    let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
        .arg("--data")
        .arg(scratch.path("untouched"))
        .args([
            "--import-team",
            "ops",
            "--listen",
            "127.0.0.1:0",
            "--join-secret-file",
        ])
        .arg(&file)
        .output()
        .unwrap();
    assert_eq!(output.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&output.stderr).contains("does not go with --listen"));
    assert!(!scratch.path("untouched").exists());

    // After all of it the team opens to the secret it was taken in with, and to no other.
    let server = Server::start(&scratch, &arguments);
    let (status, _) = server.post(None, "/v1/team/join", &json!({"join_secret": other}));
    assert_eq!(status, 403);
    let (status, joined) = server.post(None, "/v1/team/join", &json!({"join_secret": secret}));
    assert_eq!(status, 200, "{joined}");
    assert_eq!(text(&joined["result"], "team_ref"), ELSEWHERE);
    server.stop();
}

/// A member as the roster shows it to `credential`: the one whose label is `human_label`.
fn member_labelled(server: &Server, credential: &str, team_ref: &str, human_label: &str) -> Value {
    let roster = server.ok(credential, "/v1/team/members", &json!({"team_ref": team_ref}));
    roster["members"]
        .as_array()
        .unwrap()
        .iter()
        .find(|member| member["human_label"] == json!(human_label))
        .unwrap_or_else(|| panic!("{human_label} is not on the roster: {roster}"))
        .clone()
}

/// Joins a team as a new principal and returns its credential and its principal.
fn join(server: &Server, secret: &str, human_label: &str) -> (String, String) {
    let (status, joined) = server.post(
        None,
        "/v1/team/join",
        &json!({"join_secret": secret, "human_label": human_label}),
    );
    assert_eq!(status, 200, "{joined}");
    (
        text(&joined["result"], "credential"),
        text(&joined["result"], "principal_ref"),
    )
}

/// The status and the `error` of a request that is refused.
fn refused(server: &Server, credential: Option<&str>, path: &str, body: &Value) -> (u16, Value) {
    let (status, envelope) = server.post(credential, path, body);
    assert_eq!(envelope["ok"], json!(false), "{path}: {envelope}");
    (status, envelope["error"].clone())
}

#[test]
fn the_owner_rotates_revokes_bans_and_transfers_and_all_of_it_survives_a_kill() {
    let scratch = Scratch::new("administration");
    let arguments = log_arguments("file", &scratch);
    let server = Server::start(&scratch, &arguments);
    let (alice, bob, first) = team_of_two(&server);
    let team = json!({"team_ref": alice.team_ref});
    let bob_ref = text(
        &member_labelled(&server, &alice.credential, &alice.team_ref, "bob"),
        "principal_ref",
    );
    let alice_ref = text(
        &member_labelled(&server, &alice.credential, &alice.team_ref, "alice"),
        "principal_ref",
    );

    // Only the owner is answered. A member that is not the owner reads what a stranger to a team that
    // does not exist reads, byte for byte, and a request with no credential is told that first.
    let uniform = refused(
        &server,
        Some(&bob.credential),
        "/v1/team/members",
        &json!({"team_ref": "no-such-team"}),
    );
    assert_eq!(uniform.0, 403);
    assert_eq!(text(&uniform.1, "code"), "unauthorized");
    for (path, body) in [
        ("/v1/team/rotate-secret", team.clone()),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "principal_ref": alice_ref}),
        ),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "joined_secret_version_lte": 1}),
        ),
        (
            "/v1/team/transfer",
            json!({"team_ref": alice.team_ref, "principal_ref": bob_ref}),
        ),
    ] {
        assert_eq!(
            refused(&server, Some(&bob.credential), path, &body),
            uniform,
            "{path}"
        );
        let mut nowhere = body.clone();
        nowhere["team_ref"] = json!("no-such-team");
        assert_eq!(
            refused(&server, Some(&alice.credential), path, &nowhere),
            uniform,
            "{path}"
        );
        let (status, error) = refused(&server, None, path, &body);
        assert_eq!(
            (status, text(&error, "code").as_str()),
            (401, "unauthenticated"),
            "{path}"
        );
    }

    // A rotation: the five members of the contract's result, a secret for the same team, version two.
    let rotated = server.ok(&alice.credential, "/v1/team/rotate-secret", &team);
    let second = text(&rotated, "join_secret");
    let mut members: Vec<&String> = rotated.as_object().unwrap().keys().collect();
    members.sort();
    assert_eq!(
        members,
        [
            "join_secret",
            "rotated_at",
            "secret_version",
            "team_name",
            "team_ref"
        ]
    );
    assert_eq!(
        (
            &rotated["team_ref"],
            &rotated["team_name"],
            &rotated["secret_version"]
        ),
        (&json!(alice.team_ref), &json!("ops"), &json!(2))
    );
    assert!(text(&rotated, "rotated_at").ends_with('Z'));
    assert!(second.starts_with(&format!("brg1.{}.", alice.team_ref)));
    assert_ne!(second, first);
    // The secret the team had no longer joins, and is refused as one it never had is.
    let never = format!("brg1.{}.{}", alice.team_ref, "0".repeat(32));
    let rejected = refused(&server, None, "/v1/team/join", &json!({"join_secret": never}));
    assert_eq!(
        (rejected.0, text(&rejected.1, "code").as_str()),
        (403, "unauthorized")
    );
    for credential in [None, Some(bob.credential.as_str())] {
        assert_eq!(
            refused(
                &server,
                credential,
                "/v1/team/join",
                &json!({"join_secret": first})
            ),
            rejected
        );
    }
    // No member was touched: both are on the roster, at the version they joined with, and a member that
    // joins with the new secret is of its version.
    let (carol, carol_ref) = join(&server, &second, "carol");
    register(&server, &carol, &alice.team_ref, "gamma");
    let version = |label: &str| {
        member_labelled(&server, &alice.credential, &alice.team_ref, label)["joined_secret_version"].clone()
    };
    assert_eq!(
        (version("alice"), version("bob"), version("carol")),
        (json!(1), json!(1), json!(2))
    );

    // The step after a rotation: everyone that joined with the old secret, except the owner.
    let swept = server.ok(
        &alice.credential,
        "/v1/team/revoke-member",
        &json!({"team_ref": alice.team_ref, "joined_secret_version_lte": 1}),
    );
    assert_eq!(
        swept,
        json!({"team_ref": alice.team_ref, "max_version": 1, "revoked": 1, "sessions_closed": 1})
    );
    let list = json!({"team_ref": alice.team_ref});
    assert_eq!(
        refused(&server, Some(&bob.credential), "/v1/session/list", &list),
        uniform
    );
    // He comes back through the new secret as the member he was.
    let back = server.ok(&bob.credential, "/v1/team/join", &json!({"join_secret": second}));
    assert_eq!(
        (&back["rejoined"], &back["principal_ref"]),
        (&json!(true), &json!(bob_ref))
    );
    assert_eq!(version("bob"), json!(2));

    // One member revoked, banned, and the ban lifted.
    let revoke = |ban: Option<bool>| {
        let mut body = json!({"team_ref": alice.team_ref, "principal_ref": carol_ref});
        if let Some(ban) = ban {
            body["ban"] = json!(ban);
        }
        server.ok(&alice.credential, "/v1/team/revoke-member", &body)
    };
    let answer = |status: &str, changed: bool, sessions_closed: u32| {
        json!({
            "team_ref": alice.team_ref, "principal_ref": carol_ref, "status": status,
            "changed": changed, "sessions_closed": sessions_closed,
        })
    };
    assert_eq!(revoke(None), answer("revoked", true, 1));
    assert_eq!(revoke(None), answer("revoked", false, 0));
    assert_eq!(refused(&server, Some(&carol), "/v1/session/list", &list), uniform);
    let rejoin = json!({"join_secret": second});
    assert_eq!(
        server.ok(&carol, "/v1/team/join", &rejoin)["rejoined"],
        json!(true)
    );
    assert_eq!(revoke(Some(true)), answer("banned", true, 0));
    // A banned principal's join is the rejected join: the bytes a wrong secret gets, with the right one.
    assert_eq!(refused(&server, Some(&carol), "/v1/team/join", &rejoin), rejected);
    assert_eq!(revoke(Some(false)), answer("revoked", true, 0));
    assert_eq!(
        server.ok(&carol, "/v1/team/join", &rejoin)["rejoined"],
        json!(true)
    );
    assert_eq!(revoke(Some(true)), answer("banned", true, 0));

    // What the owner may not ask is the request's fault, and names the member.
    for (path, body) in [
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "principal_ref": alice_ref}),
        ),
        (
            "/v1/team/revoke-member",
            json!({"team_ref": alice.team_ref, "principal_ref": bob_ref, "joined_secret_version_lte": 1}),
        ),
        (
            "/v1/team/transfer",
            json!({"team_ref": alice.team_ref, "principal_ref": carol_ref}),
        ),
    ] {
        let (status, error) = refused(&server, Some(&alice.credential), path, &body);
        assert_eq!(
            (
                status,
                text(&error, "code").as_str(),
                text(&error["details"], "field").as_str()
            ),
            (400, "invalid_input", "principal_ref"),
            "{path}"
        );
    }

    // A transfer: bob owns the team, and alice is a member whose next administration request is refused.
    let to_bob = json!({"team_ref": alice.team_ref, "principal_ref": bob_ref});
    assert_eq!(
        server.ok(&alice.credential, "/v1/team/transfer", &to_bob),
        json!({"team_ref": alice.team_ref, "principal_ref": bob_ref, "transferred": true})
    );
    assert_eq!(
        refused(&server, Some(&alice.credential), "/v1/team/rotate-secret", &team),
        uniform
    );
    server.ok(&alice.credential, "/v1/session/list", &list);
    server.kill();

    // After a kill the records are all there: the owner, the secret, the versions and the ban.
    let server = Server::start(&scratch, &arguments);
    assert_eq!(
        refused(&server, Some(&alice.credential), "/v1/team/rotate-secret", &team),
        uniform
    );
    assert_eq!(refused(&server, Some(&carol), "/v1/team/join", &rejoin), rejected);
    assert_eq!(
        refused(&server, None, "/v1/team/join", &json!({"join_secret": first})),
        rejected
    );
    assert_eq!(
        (
            member_labelled(&server, &bob.credential, &alice.team_ref, "alice")["joined_secret_version"]
                .clone(),
            member_labelled(&server, &bob.credential, &alice.team_ref, "bob")["joined_secret_version"]
                .clone()
        ),
        (json!(1), json!(2))
    );
    let third = server.ok(&bob.credential, "/v1/team/rotate-secret", &team);
    assert_eq!(third["secret_version"], json!(3));
    // A stop writes a snapshot, and a start from it holds the same.
    server.stop();
    let server = Server::start(&scratch, &arguments);
    assert_eq!(refused(&server, Some(&carol), "/v1/team/join", &rejoin), rejected);
    assert_eq!(
        server.ok(&bob.credential, "/v1/team/rotate-secret", &team)["secret_version"],
        json!(4)
    );
    // Nothing the server wrote about itself holds a join secret.
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    for secret in [&first, &second, &text(&third, "join_secret")] {
        assert!(!said.contains(secret.rsplit('.').next().unwrap()), "{said}");
    }
    server.stop();
}

#[test]
fn a_receive_that_waits_is_refused_within_a_second_of_its_members_revocation() {
    let scratch = Scratch::new("waits-revoked");
    let server = Arc::new(Server::start(&scratch, &log_arguments("file", &scratch)));
    let (alice, bob, secret) = team_of_two(&server);
    let (carol, _) = join(&server, &secret, "carol");
    let carol_session = register(&server, &carol, &alice.team_ref, "gamma");
    let bob_ref = text(
        &member_labelled(&server, &alice.credential, &alice.team_ref, "bob"),
        "principal_ref",
    );
    // A receive with long to wait, and the moment its answer came.
    let waiting = |credential: &str, session: &str| {
        let (server, credential) = (Arc::clone(&server), credential.to_owned());
        let request = json!({"session_id": session, "wait_seconds": 25});
        std::thread::spawn(move || {
            let (status, envelope) = server.post(Some(&credential), "/v1/message/receive", &request);
            (status, envelope, Instant::now())
        })
    };

    // The owner revokes one member by its principal.
    let held = waiting(&bob.credential, &bob.session);
    std::thread::sleep(Duration::from_millis(400));
    let asked = Instant::now();
    server.ok(
        &alice.credential,
        "/v1/team/revoke-member",
        &json!({"team_ref": alice.team_ref, "principal_ref": bob_ref}),
    );
    let (status, envelope, answered) = held.join().unwrap();
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (403, "unauthorized")
    );
    let took = answered.saturating_duration_since(asked);
    assert!(took < Duration::from_secs(1), "answered after {took:?}");

    // The owner revokes every member that joined with the first secret.
    let held = waiting(&carol, &carol_session);
    std::thread::sleep(Duration::from_millis(400));
    let asked = Instant::now();
    let swept = server.ok(
        &alice.credential,
        "/v1/team/revoke-member",
        &json!({"team_ref": alice.team_ref, "joined_secret_version_lte": 1}),
    );
    assert_eq!(swept["revoked"], json!(1));
    let (status, envelope, answered) = held.join().unwrap();
    assert_eq!(
        (status, text(&envelope["error"], "code").as_str()),
        (403, "unauthorized")
    );
    let took = answered.saturating_duration_since(asked);
    assert!(took < Duration::from_secs(1), "answered after {took:?}");

    // The owner's own receive is not disturbed by either: it waits its time out and is answered empty.
    let request = json!({"session_id": alice.session, "wait_seconds": 1});
    let (status, envelope) = server.post(Some(&alice.credential), "/v1/message/receive", &request);
    assert_eq!((status, envelope["result"]["messages"].clone()), (200, json!([])));
}

/// Runs the command that names a team's owner on `scratch`'s data directory, and returns its status and
/// everything it wrote.
fn set_owner(
    scratch: &Scratch,
    log: &[String],
    team_ref: &str,
    principal_ref: &str,
) -> (Option<i32>, String) {
    let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
        .arg("--data")
        .arg(scratch.path("data"))
        .args(log)
        .args(["--set-owner", team_ref, "--principal", principal_ref])
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let wrote = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    (output.status.code(), wrote)
}

#[test]
fn the_first_to_join_a_team_that_was_taken_in_owns_it_and_the_operator_can_name_another() {
    let scratch = Scratch::new("owner");
    let arguments = log_arguments("file", &scratch);
    let (file, secret) = secret_file(&scratch, ELSEWHERE, "kept-from-the-other-backend");
    let (status, wrote) = take_in(&scratch, &arguments, "ops", &file);
    assert_eq!(status, Some(0), "{wrote}");
    let team = json!({"team_ref": ELSEWHERE});

    // The first member to join over HTTP owns the team: she rotates its secret, which keeps the reference
    // the team came with, and the member that joins after her does not.
    let server = Server::start(&scratch, &arguments);
    let (alice, alice_ref) = join(&server, &secret, "alice");
    let rotated = server.ok(&alice, "/v1/team/rotate-secret", &team);
    let second = text(&rotated, "join_secret");
    assert_eq!(
        (&rotated["team_ref"], &rotated["secret_version"]),
        (&json!(ELSEWHERE), &json!(2))
    );
    assert!(second.starts_with(&format!("brg1.{ELSEWHERE}.")));
    let (bob, bob_ref) = join(&server, &second, "bob");
    let (carol, carol_ref) = join(&server, &second, "carol");
    server.ok(&carol, "/v1/team/leave", &team);
    let (status, error) = refused(&server, Some(&bob), "/v1/team/rotate-secret", &team);
    assert_eq!((status, text(&error, "code").as_str()), (403, "unauthorized"));

    // While the server runs on the directory no owner is named beside it.
    let (status, wrote) = set_owner(&scratch, &arguments, ELSEWHERE, &bob_ref);
    assert_eq!(status, Some(1), "{wrote}");
    assert!(wrote.contains("another server is running"), "{wrote}");
    assert_eq!(
        refused(&server, Some(&bob), "/v1/team/rotate-secret", &team).0,
        403
    );
    server.stop();

    // A team the server does not hold, a principal that never was a member of the team and one that
    // left it are refused in one line each, which repeats nothing that was typed.
    let not_named = |team_ref: &str, principal_ref: &str, why: &str| {
        let (status, wrote) = set_owner(&scratch, &arguments, team_ref, principal_ref);
        assert_eq!(status, Some(1), "{wrote}");
        let refusal: Vec<&str> = wrote
            .lines()
            .filter(|line| line.contains("the owner was not named"))
            .collect();
        assert_eq!(refusal.len(), 1, "{wrote}");
        assert!(refusal[0].ends_with(why), "{wrote}");
        assert!(!wrote.contains("owned by"), "{wrote}");
        assert!(
            !wrote.contains(team_ref) && !wrote.contains(principal_ref),
            "{wrote}"
        );
    };
    not_named(
        "typed-no-such-team",
        &bob_ref,
        "the owner was not named: not_found: the server holds no team of that reference",
    );
    let not_a_member =
        "the owner was not named: invalid_input: principal_ref must be an active member of this team";
    not_named(
        ELSEWHERE.to_uppercase().as_str(),
        &bob_ref,
        "the server holds no team of that reference",
    );
    for principal_ref in ["typed-no-such-principal", carol_ref.as_str()] {
        let (status, wrote) = set_owner(&scratch, &arguments, ELSEWHERE, principal_ref);
        assert_eq!(status, Some(1), "{wrote}");
        assert!(wrote.contains(not_a_member), "{wrote}");
        assert!(
            !wrote.contains(principal_ref) && !wrote.contains("owned by"),
            "{wrote}"
        );
    }
    // The member that already owns the team: the command says so and changes nothing.
    let (status, wrote) = set_owner(&scratch, &arguments, ELSEWHERE, &alice_ref);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!(
            "team \"ops\" ({ELSEWHERE}) is already owned by {alice_ref} (\"alice\"): nothing changed"
        )),
        "{wrote}"
    );
    // Another member: one line that names the team and its owner, and nothing is served.
    let (status, wrote) = set_owner(&scratch, &arguments, ELSEWHERE, &bob_ref);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!(
            "team \"ops\" ({ELSEWHERE}) is now owned by {bob_ref} (\"bob\")"
        )),
        "{wrote}"
    );
    assert!(!wrote.contains("listening on"), "{wrote}");
    // The command serves nothing, so it is refused with what serving takes, and with the other command.
    for others in [
        &["--listen", "127.0.0.1:0"][..],
        &["--import-team", "ops", "--join-secret-file", "f"][..],
    ] {
        let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
            .arg("--data")
            .arg(scratch.path("untouched"))
            .args(["--set-owner", ELSEWHERE, "--principal", &alice_ref])
            .args(others)
            .output()
            .unwrap();
        assert_eq!(output.status.code(), Some(2), "{others:?}");
        assert!(!scratch.path("untouched").exists());
    }

    // The server that starts again serves the owner the operator named, and the owner before him is a
    // member like any other.
    let server = Server::start(&scratch, &arguments);
    assert_eq!(
        server.ok(&bob, "/v1/team/rotate-secret", &team)["secret_version"],
        json!(3)
    );
    let (status, error) = refused(&server, Some(&alice), "/v1/team/rotate-secret", &team);
    assert_eq!((status, text(&error, "code").as_str()), (403, "unauthorized"));
    assert_eq!(
        server.ok(&alice, "/v1/team/members", &team)["members"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    server.stop();
}

/// Runs the command that takes a team back on `scratch`'s data directory, and returns its status and
/// everything it wrote.
fn take_back(scratch: &Scratch, log: &[String], team_ref: &str, secret_file: &Path) -> (Option<i32>, String) {
    let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
        .arg("--data")
        .arg(scratch.path("data"))
        .args(log)
        .args(["--take-back", team_ref, "--secret-file"])
        .arg(secret_file)
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let wrote = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    (output.status.code(), wrote)
}

#[test]
fn the_operator_takes_back_a_team_whose_first_joiner_put_everyone_else_out() {
    use std::os::unix::fs::PermissionsExt;

    let scratch = Scratch::new("take-back");
    let arguments = log_arguments("file", &scratch);
    let (file, first) = secret_file(&scratch, ELSEWHERE, "kept-from-the-other-backend");
    let (status, wrote) = take_in(&scratch, &arguments, "ops", &file);
    assert_eq!(status, Some(0), "{wrote}");
    let team = json!({"team_ref": ELSEWHERE});

    // The secret leaked. Whoever had it joined first and so owns the team; the team's admin and a colleague
    // joined after. The owner rotates the secret and revokes everyone who joined with the old one: the
    // admin is out, holds no secret that joins, and nobody the operator could name is a member.
    let server = Server::start(&scratch, &arguments);
    let (mallory, _) = join(&server, &first, "mallory");
    let (alice, alice_ref) = join(&server, &first, "alice");
    let (bob, _) = join(&server, &first, "bob");
    register(&server, &alice, ELSEWHERE, "alpha");
    let second = text(
        &server.ok(&mallory, "/v1/team/rotate-secret", &team),
        "join_secret",
    );
    let swept = server.ok(
        &mallory,
        "/v1/team/revoke-member",
        &json!({"team_ref": ELSEWHERE, "joined_secret_version_lte": 1}),
    );
    assert_eq!(swept["revoked"], json!(2));
    let rejected = refused(
        &server,
        Some(&alice),
        "/v1/team/join",
        &json!({"join_secret": first}),
    );
    assert_eq!(rejected.0, 403);

    // While the server runs on the directory no team is taken back beside it, and nothing is left where
    // the secret was to go.
    let new_secret = scratch.path("ops.new-secret");
    let unfinished = scratch.path(".ops.new-secret.unfinished");
    let (status, wrote) = take_back(&scratch, &arguments, ELSEWHERE, &new_secret);
    assert_eq!(status, Some(1), "{wrote}");
    assert!(wrote.contains("another server is running"), "{wrote}");
    assert!(!new_secret.exists() && !unfinished.exists());
    server.stop();
    let (status, wrote) = set_owner(&scratch, &arguments, ELSEWHERE, &alice_ref);
    assert_eq!(status, Some(1), "{wrote}");

    // The place of the secret is checked before anything is changed: a path that is not absolute, a
    // directory that is not there and a file that is are refused in one line each, which does not repeat
    // the path. So is a team the server does not hold, and it leaves no file behind.
    let not_taken = |team_ref: &str, path: &Path, why: &str| {
        let (status, wrote) = take_back(&scratch, &arguments, team_ref, path);
        assert_eq!(status, Some(1), "{wrote}");
        let refusal: Vec<&str> = wrote
            .lines()
            .filter(|line| line.contains("the team was not taken back: "))
            .collect();
        assert_eq!(refusal.len(), 1, "{wrote}");
        assert!(refusal[0].ends_with(why), "{wrote}");
        assert!(!wrote.contains("took back team"), "{wrote}");
        assert!(!wrote.contains(&path.display().to_string()), "{wrote}");
        assert!(!unfinished.exists());
    };
    not_taken(
        ELSEWHERE,
        Path::new("typed/ops.new-secret"),
        "--secret-file takes an absolute path",
    );
    not_taken(
        ELSEWHERE,
        &scratch.path("no-such-directory/ops.new-secret"),
        "the directory of the file --secret-file names is not there",
    );
    not_taken(
        ELSEWHERE,
        &file,
        "the file --secret-file names is there already, and nothing is written over: name a file that is not \
         there",
    );
    assert_eq!(std::fs::read_to_string(&file).unwrap(), format!("{first}\n"));
    not_taken(
        "typed-no-such-team",
        &new_secret,
        "not_found: the server holds no team of that reference",
    );
    assert!(!new_secret.exists());

    // The team is taken back: one line that names the team, how many memberships ended and the file, and
    // nothing of the secret. The file is its owner's alone and holds a join secret for the same team.
    let (status, wrote) = take_back(&scratch, &arguments, ELSEWHERE, &new_secret);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!(
            "took back team \"ops\" ({ELSEWHERE}): ended 1 membership, and its new join secret is in {new_secret:?}"
        )),
        "{wrote}"
    );
    assert!(!wrote.contains("listening on"), "{wrote}");
    assert_eq!(
        std::fs::metadata(&new_secret).unwrap().permissions().mode() & 0o777,
        0o600
    );
    assert!(!unfinished.exists());
    let held = std::fs::read_to_string(&new_secret).unwrap();
    let third = held.strip_suffix('\n').expect("one line");
    let random = third
        .strip_prefix(&format!("brg1.{ELSEWHERE}."))
        .expect("a join secret for the same team");
    assert_eq!(random.len(), 32);
    assert!(random.bytes().all(|byte| byte.is_ascii_hexdigit()));
    assert!(third != first && third != second);
    assert!(!wrote.contains(random), "{wrote}");
    // Asked again with the same file it writes over nothing, and the team is as it was.
    not_taken(
        ELSEWHERE,
        &new_secret,
        "the file --secret-file names is there already, and nothing is written over: name a file that is not \
         there",
    );
    assert_eq!(std::fs::read_to_string(&new_secret).unwrap(), held);

    // The server that starts again holds a team with no member and no owner. Neither the secret that
    // leaked nor the one its owner rotated to joins, whoever presents it, and the owner it had is refused
    // as any stranger is.
    let server = Server::start(&scratch, &arguments);
    for old in [&first, &second] {
        for credential in [None, Some(mallory.as_str()), Some(alice.as_str())] {
            assert_eq!(
                refused(&server, credential, "/v1/team/join", &json!({"join_secret": old})),
                rejected
            );
        }
    }
    for (path, body) in [
        ("/v1/team/rotate-secret", team.clone()),
        ("/v1/team/members", team.clone()),
        (
            "/v1/team/transfer",
            json!({"team_ref": ELSEWHERE, "principal_ref": alice_ref}),
        ),
    ] {
        let (status, error) = refused(&server, Some(&mallory), path, &body);
        assert_eq!(
            (status, text(&error, "code").as_str()),
            (403, "unauthorized"),
            "{path}"
        );
    }
    // The admin joins with the secret from the file, as the member she was, and is the team's owner: she
    // is its one member, and she administers it.
    let back = server.ok(&alice, "/v1/team/join", &json!({"join_secret": third}));
    assert_eq!(
        (&back["rejoined"], &back["principal_ref"]),
        (&json!(true), &json!(alice_ref))
    );
    let members = server.ok(&alice, "/v1/team/members", &team);
    assert_eq!(members["members"].as_array().unwrap().len(), 1, "{members}");
    assert_eq!(members["members"][0]["joined_secret_version"], json!(3));
    // A member that joins after her is a member, and does not own the team.
    let back = server.ok(&bob, "/v1/team/join", &json!({"join_secret": third}));
    assert_eq!(back["rejoined"], json!(true));
    assert_eq!(
        refused(&server, Some(&bob), "/v1/team/rotate-secret", &team).0,
        403
    );
    assert_eq!(
        server.ok(&alice, "/v1/team/rotate-secret", &team)["secret_version"],
        json!(4)
    );
    // Nothing the server wrote about itself holds a join secret.
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    for secret in [&first, &second, third] {
        assert!(!said.contains(secret.rsplit('.').next().unwrap()), "{said}");
    }
    server.stop();
}

/// Runs the command that opens a team's move on `scratch`'s data directory, and returns its status and
/// everything it wrote.
fn open_move(
    scratch: &Scratch,
    log: &[String],
    team_ref: &str,
    ticket_file: &Path,
    days: Option<&str>,
) -> (Option<i32>, String) {
    let mut command = Command::new(env!("CARGO_BIN_EXE_brigade-server"));
    command
        .arg("--data")
        .arg(scratch.path("data"))
        .args(log)
        .args(["--open-move", team_ref, "--ticket-file"])
        .arg(ticket_file);
    if let Some(days) = days {
        command.args(["--ticket-days", days]);
    }
    let output = command.stdin(Stdio::null()).output().unwrap();
    let wrote = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    (output.status.code(), wrote)
}

/// Runs the command that closes a team's move on `scratch`'s data directory, and returns its status and
/// everything it wrote.
fn close_move(scratch: &Scratch, log: &[String], team_ref: &str) -> (Option<i32>, String) {
    let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
        .arg("--data")
        .arg(scratch.path("data"))
        .args(log)
        .args(["--close-move", team_ref])
        .stdin(Stdio::null())
        .output()
        .unwrap();
    let wrote = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    (output.status.code(), wrote)
}

/// The ticket a file holds, and its random half: one line, a join secret of `team_ref` minted as the
/// server mints one, in a file of its owner's alone.
fn ticket_in(file: &Path, team_ref: &str) -> (String, String) {
    use std::os::unix::fs::PermissionsExt;

    assert_eq!(
        std::fs::metadata(file).unwrap().permissions().mode() & 0o777,
        0o600
    );
    let held = std::fs::read_to_string(file).unwrap();
    let ticket = held.strip_suffix('\n').expect("one line");
    let random = ticket
        .strip_prefix(&format!("brg1.{team_ref}."))
        .expect("a ticket for the same team");
    assert_eq!(random.len(), 32);
    assert!(random.bytes().all(|byte| byte.is_ascii_hexdigit()));
    (ticket.to_owned(), random.to_owned())
}

#[test]
fn a_team_that_moves_is_joined_with_its_ticket_until_the_move_is_replaced_or_closed() {
    let scratch = Scratch::new("move");
    let arguments = log_arguments("file", &scratch);
    let (file, secret) = secret_file(&scratch, ELSEWHERE, "kept-from-the-other-backend");
    let (status, wrote) = take_in(&scratch, &arguments, "ops", &file);
    assert_eq!(status, Some(0), "{wrote}");
    let team = json!({"team_ref": ELSEWHERE});
    let first_file = scratch.path("ops.move-ticket");
    let unfinished = scratch.path(".ops.move-ticket.unfinished");

    // The place of the ticket is checked before anything is changed: a path that is not absolute, a
    // directory that is not there and a file that is are refused in one line each, which does not repeat
    // the path. So is a team the server does not hold, and it leaves no file behind.
    let not_opened = |team_ref: &str, path: &Path, why: &str| {
        let (status, wrote) = open_move(&scratch, &arguments, team_ref, path, None);
        assert_eq!(status, Some(1), "{wrote}");
        let refusal: Vec<&str> = wrote
            .lines()
            .filter(|line| line.contains("the move was not opened: "))
            .collect();
        assert_eq!(refusal.len(), 1, "{wrote}");
        assert!(refusal[0].ends_with(why), "{wrote}");
        assert!(!wrote.contains("opened the move of"), "{wrote}");
        assert!(!wrote.contains(&path.display().to_string()), "{wrote}");
        assert!(!unfinished.exists());
    };
    not_opened(
        ELSEWHERE,
        Path::new("typed/ops.move-ticket"),
        "--ticket-file takes an absolute path",
    );
    not_opened(
        ELSEWHERE,
        &scratch.path("no-such-directory/ops.move-ticket"),
        "the directory of the file --ticket-file names is not there",
    );
    not_opened(
        ELSEWHERE,
        &file,
        "the file --ticket-file names is there already, and nothing is written over: name a file that is not \
         there",
    );
    assert_eq!(std::fs::read_to_string(&file).unwrap(), format!("{secret}\n"));
    not_opened(
        "typed-no-such-team",
        &first_file,
        "not_found: the server holds no team of that reference",
    );
    assert!(!first_file.exists());
    // A number of days no ticket is given is refused by the command line, before a directory is touched.
    for days in ["0", "366"] {
        let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
            .arg("--data")
            .arg(scratch.path("untouched"))
            .args(["--open-move", ELSEWHERE, "--ticket-days", days, "--ticket-file"])
            .arg(&first_file)
            .output()
            .unwrap();
        assert_eq!(output.status.code(), Some(2), "{days}");
        assert!(
            String::from_utf8_lossy(&output.stderr)
                .contains("--ticket-days takes a number of days from 1 to 365")
        );
        assert!(!scratch.path("untouched").exists() && !first_file.exists());
    }

    // The move is opened: one line that names the team, the file and when the ticket ends, in UTC and to
    // the second, and nothing of the ticket. The file is its owner's alone and holds a join secret for
    // the same team that is not the team's own. Nothing is served.
    let (status, wrote) = open_move(&scratch, &arguments, ELSEWHERE, &first_file, None);
    assert_eq!(status, Some(0), "{wrote}");
    let said = format!(
        "opened the move of team \"ops\" ({ELSEWHERE}): its ticket is in {first_file:?} and admits until "
    );
    let line = wrote
        .lines()
        .find(|line| line.contains(&said))
        .unwrap_or_else(|| panic!("{wrote}"));
    let ends_at = line.rsplit(' ').next().unwrap();
    assert_eq!(ends_at.len(), "2026-12-14T03:12:45Z".len(), "{line}");
    assert!(ends_at.ends_with('Z') && ends_at.as_bytes()[10] == b'T', "{line}");
    assert!(!wrote.contains("admits nobody"), "{wrote}");
    assert!(!wrote.contains("listening on"), "{wrote}");
    assert!(!unfinished.exists());
    let (first, first_random) = ticket_in(&first_file, ELSEWHERE);
    assert!(first != secret);
    assert!(!wrote.contains(&first_random), "{wrote}");
    // The command's own stop left a snapshot of the new format, with the ticket's hash and never the
    // ticket: it is what the server that starts next reads the move from.
    let newest = std::fs::read_dir(scratch.path("data").join("snapshots"))
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .max()
        .unwrap();
    let snapshot = std::fs::read_to_string(newest).unwrap();
    assert!(snapshot.starts_with(r#"{"format":3,"#), "{snapshot}");
    assert!(snapshot.contains(r#""move_ticket":{"hash":""#), "{snapshot}");
    assert!(!snapshot.contains(&first_random), "{snapshot}");

    // A member comes in with the ticket before anyone has joined with the secret. It is a member, and
    // the team still has no owner: the member the secret lets in next is the owner.
    let server = Server::start(&scratch, &arguments);
    let (bob, bob_ref) = join(&server, &first, "bob");
    let (status, error) = refused(&server, Some(&bob), "/v1/team/rotate-secret", &team);
    assert_eq!((status, text(&error, "code").as_str()), (403, "unauthorized"));
    let (alice, alice_ref) = join(&server, &secret, "alice");
    assert_eq!(
        server.ok(
            &alice,
            "/v1/team/transfer",
            &json!({"team_ref": ELSEWHERE, "principal_ref": alice_ref})
        )["transferred"],
        json!(true)
    );
    assert_eq!(
        member_labelled(&server, &alice, ELSEWHERE, "bob")["joined_secret_version"],
        json!(1)
    );
    // The server says who came in and how, and that the member the secret let in owns the team.
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    for line in [
        format!("member {bob_ref} joined team ops ({ELSEWHERE}) with its move ticket, labelled \"bob\""),
        format!("member {alice_ref} joined team ops ({ELSEWHERE}), labelled \"alice\""),
        format!("member {alice_ref} owns team ops ({ELSEWHERE}) from now on"),
    ] {
        assert!(said.contains(&line), "{line}: {said}");
    }
    assert!(!said.contains(&format!("member {bob_ref} owns")), "{said}");

    // While the server runs on the directory no move is opened or closed beside it, and nothing is left
    // where a ticket was to go.
    let second_file = scratch.path("ops.second-ticket");
    let (status, wrote) = open_move(&scratch, &arguments, ELSEWHERE, &second_file, Some("1"));
    assert_eq!(status, Some(1), "{wrote}");
    assert!(wrote.contains("another server is running"), "{wrote}");
    assert!(!second_file.exists() && !scratch.path(".ops.second-ticket.unfinished").exists());
    let (status, wrote) = close_move(&scratch, &arguments, ELSEWHERE);
    assert_eq!(status, Some(1), "{wrote}");
    assert!(wrote.contains("another server is running"), "{wrote}");

    // The move is in the log: a server that was killed and starts again still opens to the ticket.
    server.kill();
    let server = Server::start(&scratch, &arguments);
    let (carol, _) = join(&server, &first, "carol");
    let rejected = refused(
        &server,
        None,
        "/v1/team/join",
        &json!({"join_secret": format!("brg1.{ELSEWHERE}.never-a-ticket-of-any-move")}),
    );
    assert_eq!(rejected.0, 403);
    server.stop();

    // A second move takes the place of the first, and the line says what became of the first ticket.
    let (status, wrote) = open_move(&scratch, &arguments, ELSEWHERE, &second_file, Some("1"));
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!(
            "opened the move of team \"ops\" ({ELSEWHERE}): its ticket is in {second_file:?}"
        )),
        "{wrote}"
    );
    assert!(
        wrote.contains("Z, and the ticket of the move it had admits nobody from now on"),
        "{wrote}"
    );
    let (second, second_random) = ticket_in(&second_file, ELSEWHERE);
    assert!(second != first);
    // The file of the first ticket is not written over by asking again for it.
    not_opened(
        ELSEWHERE,
        &first_file,
        "the file --ticket-file names is there already, and nothing is written over: name a file that is not \
         there",
    );
    assert_eq!(
        std::fs::read_to_string(&first_file).unwrap(),
        format!("{first}\n")
    );
    let server = Server::start(&scratch, &arguments);
    assert_eq!(
        refused(&server, None, "/v1/team/join", &json!({"join_secret": first})),
        rejected
    );
    let (dave, _) = join(&server, &second, "dave");
    server.stop();

    // The operator closes the move: one line, and the ticket is a wrong secret from then on, whoever
    // presents it. Every member that came in with a ticket is still one. Closing again changes nothing.
    let (status, wrote) = close_move(&scratch, &arguments, ELSEWHERE);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!(
            "closed the move of team \"ops\" ({ELSEWHERE}): its ticket admits nobody from now on"
        )),
        "{wrote}"
    );
    assert!(!wrote.contains("listening on"), "{wrote}");
    let (status, wrote) = close_move(&scratch, &arguments, ELSEWHERE);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(
        wrote.contains(&format!(
            "team \"ops\" ({ELSEWHERE}) has no move: nothing changed"
        )),
        "{wrote}"
    );
    let (status, wrote) = close_move(&scratch, &arguments, "typed-no-such-team");
    assert_eq!(status, Some(1), "{wrote}");
    assert!(
        wrote.contains("the move was not closed: not_found: the server holds no team of that reference"),
        "{wrote}"
    );
    assert!(!wrote.contains("typed-no-such-team"), "{wrote}");
    let server = Server::start(&scratch, &arguments);
    for ticket in [&first, &second] {
        for credential in [None, Some(dave.as_str())] {
            assert_eq!(
                refused(
                    &server,
                    credential,
                    "/v1/team/join",
                    &json!({"join_secret": ticket})
                ),
                rejected
            );
        }
    }
    let roster = server.ok(&alice, "/v1/team/members", &team);
    assert_eq!(roster["members"].as_array().unwrap().len(), 4, "{roster}");
    server.ok(&carol, "/v1/team/members", &team);
    server.stop();

    // A rotation of the team's join secret closes a move that is open, and the server says so.
    let third_file = scratch.path("ops.third-ticket");
    let (status, wrote) = open_move(&scratch, &arguments, ELSEWHERE, &third_file, Some("365"));
    assert_eq!(status, Some(0), "{wrote}");
    assert!(!wrote.contains("admits nobody"), "{wrote}");
    let (third, third_random) = ticket_in(&third_file, ELSEWHERE);
    let server = Server::start(&scratch, &arguments);
    let (erin, _) = join(&server, &third, "erin");
    let rotated = server.ok(&alice, "/v1/team/rotate-secret", &team);
    assert_eq!(rotated["secret_version"], json!(2));
    assert_eq!(
        refused(
            &server,
            Some(&erin),
            "/v1/team/join",
            &json!({"join_secret": third})
        ),
        rejected
    );
    join(&server, &text(&rotated, "join_secret"), "frank");
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(
        said.contains(&format!(
            "the move of team ops ({ELSEWHERE}) is closed by the rotation of its join secret"
        )),
        "{said}"
    );
    // Nothing the server wrote about itself holds a ticket or a join secret.
    for random in [&first_random, &second_random, &third_random] {
        assert!(!said.contains(random.as_str()), "{said}");
    }
    assert!(!said.contains("kept-from-the-other-backend"), "{said}");
    server.stop();
    // The rotation closed it: there is nothing left for the operator to close.
    let (status, wrote) = close_move(&scratch, &arguments, ELSEWHERE);
    assert_eq!(status, Some(0), "{wrote}");
    assert!(wrote.contains("has no move: nothing changed"), "{wrote}");
}

#[test]
fn a_gateway_that_joins_a_team_that_was_taken_in_first_does_not_own_it() {
    let scratch = Scratch::new("gateway-first");
    let mut arguments = log_arguments("file", &scratch);
    let (file, secret) = secret_file(&scratch, ELSEWHERE, "kept-from-the-other-backend");
    let (status, wrote) = take_in(&scratch, &arguments, "ops", &file);
    assert_eq!(status, Some(0), "{wrote}");

    // A Slack gateway for the team. Every secret is an obvious fake, and its Slack is an address nothing
    // listens on: a gateway with nothing to carry asks Slack nothing.
    let put = |name: &str, held: &str| {
        let path = scratch.path(name);
        std::fs::write(&path, format!("{held}\n")).unwrap();
        path.display().to_string()
    };
    let config = json!({"slack": {
        "bot_token_file": put("slack-bot-token", "xoxb-FAKE-BOT-TOKEN-MUST-NOT-LEAK"),
        "signing_secret_file": put("slack-signing-secret", "fake-signing-secret-MUST-NOT-LEAK"),
        "api": "http://127.0.0.1:1/api",
        "teams": [{"join_secret_file": file.display().to_string()}],
    }});
    let gateways = scratch.path("gateways.json");
    std::fs::write(&gateways, config.to_string()).unwrap();
    arguments.extend(["--gateway-config".to_owned(), gateways.display().to_string()]);

    // The gateway takes its place before the server listens, so it is the team's first member. It joined
    // inside the server, and such a join never owns: the team still has no owner.
    let server = Server::start(&scratch, &arguments);
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(said.contains("the slack gateway joined team ops"), "{said}");
    let state: Value =
        serde_json::from_slice(&std::fs::read(scratch.path("data/gateway/state.json")).unwrap()).unwrap();
    let gateway = text(&state["memberships"][0], "credential");
    let team = json!({"team_ref": ELSEWHERE});
    let roster = server.ok(&gateway, "/v1/team/members", &team);
    assert_eq!(roster["members"].as_array().unwrap().len(), 1, "{roster}");
    let (status, error) = refused(&server, Some(&gateway), "/v1/team/rotate-secret", &team);
    assert_eq!((status, text(&error, "code").as_str()), (403, "unauthorized"));

    // The first member to join over HTTP is the second member of the team, and its owner.
    let (alice, alice_ref) = join(&server, &secret, "alice");
    assert_eq!(
        server.ok(&alice, "/v1/team/members", &team)["members"]
            .as_array()
            .unwrap()
            .len(),
        2
    );
    assert_eq!(
        server.ok(&alice, "/v1/team/rotate-secret", &team)["secret_version"],
        json!(2)
    );
    assert_eq!(
        server.ok(
            &alice,
            "/v1/team/transfer",
            &json!({"team_ref": ELSEWHERE, "principal_ref": alice_ref})
        )["transferred"],
        json!(true)
    );
    assert_eq!(
        refused(&server, Some(&gateway), "/v1/team/rotate-secret", &team).0,
        403
    );
    server.stop();
}

#[test]
fn the_listener_refuses_plain_http_off_loopback_before_it_touches_the_directory() {
    let scratch = Scratch::new("listen");
    let output = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
        .arg("--data")
        .arg(scratch.path("data"))
        .args(["--listen", "0.0.0.0:0"])
        .output()
        .unwrap();
    assert_eq!(output.status.code(), Some(2));
    assert!(String::from_utf8_lossy(&output.stderr).contains("loopback"));
    assert!(!scratch.path("data").exists());
}

/// A certificate for `localhost` and its key, made by the `openssl` command; `None` where there is none.
fn self_signed(scratch: &Scratch) -> Option<(PathBuf, PathBuf)> {
    let (certificate, key) = (scratch.path("certificate.pem"), scratch.path("key.pem"));
    let made = Command::new("openssl")
        .args([
            "req",
            "-x509",
            "-newkey",
            "rsa:2048",
            "-nodes",
            "-days",
            "2",
            "-subj",
            "/CN=localhost",
        ])
        .args(["-addext", "subjectAltName=DNS:localhost,IP:127.0.0.1"])
        .args(["-addext", "basicConstraints=critical,CA:FALSE"])
        .arg("-keyout")
        .arg(&key)
        .arg("-out")
        .arg(&certificate)
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .status()
        .ok()?;
    made.success().then_some((certificate, key))
}

/// One `POST` over TLS to a server named `localhost`, trusting `certificate` alone.
fn post_over_tls(url: &str, certificate: &Path, path: &str, body: &str) -> (u16, Value) {
    post_over_tls_as(url, certificate, "localhost", path, body)
}

/// One `POST` over TLS, trusting `certificate` alone, to a server that certificate calls `name`.
fn post_over_tls_as(
    url: &str,
    certificate: &Path,
    name: &'static str,
    path: &str,
    body: &str,
) -> (u16, Value) {
    use rustls::pki_types::pem::PemObject;
    use rustls::pki_types::{CertificateDer, ServerName};

    let authority = url.strip_prefix("https://").expect("an HTTPS server");
    let mut roots = rustls::RootCertStore::empty();
    roots
        .add(CertificateDer::from_pem_file(certificate).unwrap())
        .unwrap();
    let config =
        rustls::ClientConfig::builder_with_provider(Arc::new(rustls::crypto::ring::default_provider()))
            .with_safe_default_protocol_versions()
            .unwrap()
            .with_root_certificates(roots)
            .with_no_client_auth();
    let name = ServerName::try_from(name).unwrap();
    let mut connection = rustls::ClientConnection::new(Arc::new(config), name).unwrap();
    let mut stream = TcpStream::connect(authority).unwrap();
    stream.set_read_timeout(Some(Duration::from_secs(30))).unwrap();
    let mut tls = rustls::Stream::new(&mut connection, &mut stream);
    tls.write_all(&request(authority, None, path, body, &[])).unwrap();
    let mut response = Vec::new();
    // The server closes after its answer; an end without TLS's goodbye is still the whole answer here.
    let _ = tls.read_to_end(&mut response);
    parse(&response)
}

#[test]
fn the_server_serves_tls_itself_when_it_is_given_a_certificate() {
    let scratch = Scratch::new("tls");
    let Some((certificate, key)) = self_signed(&scratch) else {
        eprintln!("skipped: this machine has no openssl command to make a certificate with");
        return;
    };
    let mut arguments = log_arguments("file", &scratch);
    arguments.extend(["--tls-cert".to_owned(), certificate.display().to_string()]);
    arguments.extend(["--tls-key".to_owned(), key.display().to_string()]);
    let server = Server::start(&scratch, &arguments);
    assert!(server.url.starts_with("https://"), "{}", server.url);

    let (status, envelope) = post_over_tls(
        &server.url,
        &certificate,
        "/v1/team/create",
        r#"{"team_name":"ops"}"#,
    );
    assert_eq!(status, 200, "{envelope}");
    assert!(envelope["result"]["credential"].is_string());

    // The start line names the certificate's fingerprint as `openssl` prints it, which is what a team file pins.
    let printed = Command::new("openssl")
        .args(["x509", "-noout", "-fingerprint", "-sha256", "-in"])
        .arg(&certificate)
        .output()
        .unwrap();
    let printed = String::from_utf8_lossy(&printed.stdout);
    let hex = printed
        .trim()
        .rsplit('=')
        .next()
        .unwrap()
        .replace(':', "")
        .to_lowercase();
    assert_eq!(hex.len(), 64, "{printed}");
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    assert!(said.contains(&format!("(certificate sha256:{hex})")), "{said}");

    // The same port does not answer plain HTTP.
    let authority = server.url.strip_prefix("https://").unwrap();
    let mut stream = TcpStream::connect(authority).unwrap();
    stream.set_read_timeout(Some(Duration::from_secs(15))).unwrap();
    stream
        .write_all(&request(authority, None, "/v1/info", "{}", &[]))
        .unwrap();
    let mut response = Vec::new();
    let _ = stream.read_to_end(&mut response);
    assert!(!String::from_utf8_lossy(&response).contains("contract_version"));
}

/// The fingerprint in the server's start line, as `sha256:<hex>`. The server writes where it listens before
/// it says so, and a start is known by the first, so the line is waited for: read at once, it is sometimes
/// not there yet on a loaded machine.
fn said_fingerprint(scratch: &Scratch) -> String {
    let deadline = Instant::now() + Duration::from_secs(15);
    loop {
        let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
        if let Some(at) = said.rfind("(certificate sha256:")
            && said.len() >= at + 13 + 71
        {
            return said[at + 13..at + 13 + 71].to_owned();
        }
        assert!(
            Instant::now() < deadline,
            "no start line that names the certificate: {said}"
        );
        std::thread::sleep(Duration::from_millis(20));
    }
}

#[test]
fn the_server_makes_its_own_certificate_once_and_keeps_it() {
    use std::os::unix::fs::PermissionsExt;

    let scratch = Scratch::new("own-certificate");
    let mut arguments = log_arguments("file", &scratch);
    arguments.push("--tls-self-signed".to_owned());
    let tls = scratch.path("data").join("tls");
    let (certificate, key) = (tls.join("certificate.pem"), tls.join("key.pem"));

    let first = {
        let server = Server::start(&scratch, &arguments);
        assert!(server.url.starts_with("https://"), "{}", server.url);
        // The certificate it made is the one it serves, and a client that trusts it alone is answered.
        let (status, envelope) = post_over_tls_as(
            &server.url,
            &certificate,
            "brigade-server",
            "/v1/team/create",
            r#"{"team_name":"ops"}"#,
        );
        assert_eq!(status, 200, "{envelope}");
        said_fingerprint(&scratch)
    };
    assert_eq!(first.len(), 71, "{first}");
    assert!(
        std::fs::read_to_string(scratch.path("stderr"))
            .unwrap()
            .contains("made the server's own certificate")
    );
    assert_eq!(
        std::fs::metadata(&key).unwrap().permissions().mode() & 0o777,
        0o600
    );
    assert_eq!(
        std::fs::metadata(&tls).unwrap().permissions().mode() & 0o777,
        0o700
    );

    // A second start serves the same certificate: a new one would be one no team file pins.
    let pem = std::fs::read(&certificate).unwrap();
    {
        let _server = Server::start(&scratch, &arguments);
        assert_eq!(said_fingerprint(&scratch), first);
    }
    assert_eq!(std::fs::read(&certificate).unwrap(), pem);

    // Half of the pair is never replaced: the start is refused, by name.
    std::fs::remove_file(&key).unwrap();
    let refused = Server::try_start(&scratch, &arguments)
        .err()
        .expect("a refused start");
    assert!(refused.contains("not both"), "{refused}");
    assert_eq!(std::fs::read(&certificate).unwrap(), pem);
}

/// The conformance suite through the bundled `rest` adapter: the contract's section 6.
///
/// It needs the Go side's binaries, so the two tests below are marked ignored and `make server-test` runs
/// them, with `BRIGADE_BIN_DIR` naming the directory that holds `brigade` and `brigade-conformance`.
fn conformance(log: &str, slow: bool) {
    let bin = std::env::var_os("BRIGADE_BIN_DIR").map(PathBuf::from).expect(
        "BRIGADE_BIN_DIR names the directory of brigade and brigade-conformance; make server-test sets it",
    );
    let scratch = Scratch::new(&format!("conformance-{log}"));
    let server = Server::start(&scratch, &log_arguments(log, &scratch));
    let mut suite = Command::new(bin.join("brigade-conformance"));
    if slow {
        suite.arg("--slow");
    }
    let output = suite
        .arg("--env")
        .arg(format!("BRIGADE_REST_URL={}", server.url))
        .arg("--adapter")
        .arg(bin.join("brigade"))
        .args(["--", "adapter", "rest"])
        .output()
        .unwrap();
    // The suite writes its report to stderr.
    let report = format!(
        "{}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
    println!("{report}");
    assert!(output.status.success(), "{report}");
    // Two cases are skipped on either log: C-51 and C-52, the record of where a team went, which the `rest`
    // adapter does not offer, because a record is kept by the backend a team leaves. The third is the slow
    // one.
    let skipped = if slow {
        " 0 failed, 2 skipped"
    } else {
        " 0 failed, 3 skipped"
    };
    assert!(report.contains(skipped), "{report}");
    server.stop();
}

/// On the file log, without the case that waits for a lease to run out: the rules are the same on either
/// log, and the slow case is run where it counts.
#[test]
#[ignore = "needs the Go side's binaries: make server-test"]
fn the_conformance_suite_is_green_on_the_file_log() {
    conformance("file", false);
}

/// On the Cavalry feed, `--slow` included: what the contract calls a correct server.
#[cfg(feature = "cavalry")]
#[test]
#[ignore = "needs the Go side's binaries: make server-test"]
fn the_conformance_suite_is_green_on_the_cavalry_feed() {
    conformance("cavalry", true);
}
