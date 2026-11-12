//! The server as a process: started, asked over HTTP, killed and started again.
//!
//! The rules themselves are `brigade-core`'s tests and the conformance suite's. These tests are what only a
//! running process can show: that what was answered is still there after a kill, that a receive that waits
//! is answered when it should be, and that the listener does what the operator told it.

use std::io::{Read, Write};
use std::net::TcpStream;
use std::path::{Path, PathBuf};
use std::process::{Child, Command, Stdio};
use std::sync::Arc;
use std::time::{Duration, Instant};

use serde_json::{Value, json};

/// A directory of the test's own, removed when the test ends.
struct Scratch(PathBuf);

impl Scratch {
    fn new(test: &str) -> Self {
        let dir = std::env::temp_dir().join(format!("brigade-server-{test}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        Self(dir)
    }

    fn path(&self, name: &str) -> PathBuf {
        self.0.join(name)
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

/// A UDP port nothing is bound to now, and that no other test of this run was given: the tests run side by
/// side, and the system hands a port out again as soon as it is free.
#[cfg(feature = "cavalry")]
fn free_udp() -> String {
    static GIVEN: std::sync::Mutex<std::collections::BTreeSet<u16>> =
        std::sync::Mutex::new(std::collections::BTreeSet::new());
    loop {
        let socket = std::net::UdpSocket::bind("127.0.0.1:0").unwrap();
        let address = socket.local_addr().unwrap();
        if GIVEN.lock().unwrap().insert(address.port()) {
            return address.to_string();
        }
    }
}

/// The same arguments with the feed on two new UDP ports.
fn on_new_ports(arguments: &[String]) -> Vec<String> {
    #[cfg(feature = "cavalry")]
    {
        let mut moved = arguments.to_vec();
        for index in 1..moved.len() {
            if moved[index - 1] == "--feed-control" || moved[index - 1] == "--feed-self" {
                moved[index] = free_udp();
            }
        }
        moved
    }
    #[cfg(not(feature = "cavalry"))]
    arguments.to_vec()
}

/// The arguments that choose the log a test runs on.
fn log_arguments(log: &str, scratch: &Scratch) -> Vec<String> {
    let arguments = vec!["--log".to_owned(), log.to_owned()];
    #[cfg(feature = "cavalry")]
    let arguments = if log == "cavalry" {
        let feed = [
            "--feed-control".to_owned(),
            free_udp(),
            "--feed-self".to_owned(),
            free_udp(),
        ];
        let driver = [
            "--driver-dir".to_owned(),
            scratch.path("driver").display().to_string(),
        ];
        arguments.into_iter().chain(feed).chain(driver).collect()
    } else {
        arguments
    };
    let _ = scratch;
    arguments
}

/// One running server.
struct Server {
    child: Child,
    url: String,
}

impl Server {
    /// Starts a server on `scratch`'s data directory and waits until it listens.
    ///
    /// A port that was free when the test chose it can be taken by the time the server binds it, by
    /// anything on the machine. Where the feed's ports are is nothing a test is about, so such a start is
    /// made again on new ones.
    fn start(scratch: &Scratch, arguments: &[String]) -> Self {
        let mut arguments = arguments.to_vec();
        for _ in 0..3 {
            match Self::try_start(scratch, &arguments) {
                Ok(server) => return server,
                Err(log) if log.contains("Address already in use") => arguments = on_new_ports(&arguments),
                Err(log) => panic!("the server did not start:\n{log}"),
            }
        }
        panic!("the server found its feed's ports taken three times in a row");
    }

    /// Starts a server; when it ends before it listens, the error is what it wrote to stderr.
    fn try_start(scratch: &Scratch, arguments: &[String]) -> Result<Self, String> {
        let address = scratch.path("address");
        let _ = std::fs::remove_file(&address);
        let stderr = std::fs::File::create(scratch.path("stderr")).unwrap();
        let mut child = Command::new(env!("CARGO_BIN_EXE_brigade-server"))
            .arg("--data")
            .arg(scratch.path("data"))
            .args(["--listen", "127.0.0.1:0", "--address-file"])
            .arg(&address)
            .args(arguments)
            .stdin(Stdio::null())
            .stdout(Stdio::null())
            .stderr(stderr)
            .spawn()
            .unwrap();
        let deadline = Instant::now() + Duration::from_secs(30);
        loop {
            if let Ok(url) = std::fs::read_to_string(&address)
                && url.ends_with('\n')
            {
                return Ok(Self {
                    child,
                    url: url.trim().to_owned(),
                });
            }
            if child.try_wait().unwrap().is_some() || Instant::now() > deadline {
                let _ = child.kill();
                let _ = child.wait();
                return Err(std::fs::read_to_string(scratch.path("stderr")).unwrap_or_default());
            }
            std::thread::sleep(Duration::from_millis(20));
        }
    }

    /// Ends the server as a power cut would, as far as a process can: no signal it can handle.
    fn kill(mut self) {
        self.child.kill().unwrap();
        self.child.wait().unwrap();
    }

    /// Asks the server to stop and waits until it has.
    fn stop(mut self) {
        let status = Command::new("kill")
            .arg("-TERM")
            .arg(self.child.id().to_string())
            .status()
            .unwrap();
        assert!(status.success());
        let ended = self.child.wait().unwrap();
        assert!(ended.success(), "the server ended with {ended}");
    }

    fn post(&self, credential: Option<&str>, path: &str, body: &Value) -> (u16, Value) {
        let authority = self.url.strip_prefix("http://").expect("a plain HTTP server");
        let mut stream = TcpStream::connect(authority).unwrap();
        stream.set_read_timeout(Some(Duration::from_secs(60))).unwrap();
        stream
            .write_all(&request(authority, credential, path, &body.to_string(), &[]))
            .unwrap();
        let mut response = Vec::new();
        stream.read_to_end(&mut response).unwrap();
        parse(&response)
    }

    /// Asks and expects a result.
    fn ok(&self, credential: &str, path: &str, body: &Value) -> Value {
        let (status, envelope) = self.post(Some(credential), path, body);
        assert_eq!(status, 200, "{path}: {envelope}");
        envelope["result"].clone()
    }
}

impl Drop for Server {
    fn drop(&mut self) {
        let _ = self.child.kill();
        let _ = self.child.wait();
    }
}

/// One `POST` as the bytes of HTTP/1.1.
fn request(
    authority: &str,
    credential: Option<&str>,
    path: &str,
    body: &str,
    headers: &[(&str, &str)],
) -> Vec<u8> {
    let mut head = format!(
        "POST {path} HTTP/1.1\r\nHost: {authority}\r\nContent-Type: application/json\r\nContent-Length: {}\r\n",
        body.len()
    );
    if let Some(credential) = credential {
        head.push_str(&format!("Authorization: Bearer {credential}\r\n"));
    }
    for (name, value) in headers {
        head.push_str(&format!("{name}: {value}\r\n"));
    }
    head.push_str("\r\n");
    let mut bytes = head.into_bytes();
    bytes.extend_from_slice(body.as_bytes());
    bytes
}

/// The status and the envelope of a response.
fn parse(response: &[u8]) -> (u16, Value) {
    let text = String::from_utf8_lossy(response);
    let (head, body) = text
        .split_once("\r\n\r\n")
        .unwrap_or_else(|| panic!("not a response: {text}"));
    let status = head
        .split_whitespace()
        .nth(1)
        .and_then(|status| status.parse().ok())
        .expect("a status line");
    assert!(
        head.to_ascii_lowercase()
            .contains("content-type: application/json"),
        "{head}"
    );
    (
        status,
        serde_json::from_str(body).unwrap_or_else(|_| panic!("not an envelope: {body}")),
    )
}

fn text(value: &Value, member: &str) -> String {
    value[member]
        .as_str()
        .unwrap_or_else(|| panic!("no {member} in {value}"))
        .to_owned()
}

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

    // What the server is, with no credential.
    let (status, info) = server.post(None, "/v1/info", &json!({}));
    assert_eq!(status, 200);
    assert_eq!(info["result"]["contract_version"], json!("1"));
    assert_eq!(info["result"]["server"]["name"], json!("brigade-server"));
    assert_eq!(
        info["result"]["server"]["version"],
        json!(env!("CARGO_PKG_VERSION"))
    );

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

/// The fingerprint in the server's start line, as `sha256:<hex>`.
fn said_fingerprint(scratch: &Scratch) -> String {
    let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
    let from = said
        .rfind("(certificate sha256:")
        .expect("a start line that names the certificate")
        + 13;
    said[from..from + 71].to_owned()
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
    let skipped = if slow {
        " 0 failed, 0 skipped"
    } else {
        " 0 failed, 1 skipped"
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
