//! What a released server left on the disk, read by this build (work item 385).
//!
//! `server/testdata/stored/<version>/data` is a data directory as the released server of that version left
//! it, and `entries.jsonl` beside it is its log opened, one entry to a line. Every operator's directory is
//! one of these. A member added to a record or to the state without a default makes this build refuse such
//! a directory, and a test here or in `brigade-core`'s `tests/stored.rs` then says which member.
//!
//! The directories are written by [`write_the_directory_a_released_server_leaves`], which is not a test of
//! this build: `server/testdata/stored/README.md` says how.

use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};

use brigade_log::{FileLog, KEY_BYTES, Log, Sealer};
use serde_json::{Value, json};

// This file uses part of what the tests of the running server share.
#[allow(dead_code)]
mod common;

use common::{Scratch, Server, text};

/// The first release whose directory is kept. It is never taken away: a directory that release wrote is
/// somebody's until they say otherwise.
const FIRST_KEPT: &str = "0.9.0";

/// Where the directories that releases left are kept.
fn kept_in() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/stored")
}

/// Where the directory a release left is kept.
fn kept(release: &str) -> PathBuf {
    kept_in().join(release)
}

/// The released versions whose directories this build is held to: every one kept.
fn releases() -> Vec<String> {
    let mut releases: Vec<String> = std::fs::read_dir(kept_in())
        .unwrap()
        .map(|entry| entry.unwrap())
        .filter(|entry| entry.path().is_dir())
        .map(|entry| entry.file_name().into_string().unwrap())
        .collect();
    releases.sort();
    assert!(
        releases.iter().any(|release| release == FIRST_KEPT),
        "{releases:?}"
    );
    releases
}

/// A version as three numbers, so that 0.10.0 comes after 0.9.0.
fn numbers(version: &str) -> Vec<u32> {
    version.split('.').map(|part| part.parse().unwrap()).collect()
}

/// A release leaves its directory here once it is out. While the changelog has work under Unreleased,
/// every version it names is out, and each from the first kept on has a directory. In the commit that
/// names a new version and has nothing under Unreleased, that version is being released: its binary does
/// not exist yet, and it is asked for by the first change that lands after it.
#[test]
fn every_release_that_is_out_left_its_directory() {
    let changelog = std::fs::read_to_string(kept_in().join("../../CHANGELOG.md")).unwrap();
    let mut released: Vec<&str> = changelog
        .lines()
        .filter_map(|line| line.strip_prefix("## ["))
        .filter_map(|line| line.split_once(']'))
        .map(|(version, _)| version)
        .filter(|version| version.split('.').all(|part| part.parse::<u32>().is_ok()))
        .collect();
    released.sort_by_key(|version| numbers(version));
    assert!(released.contains(&FIRST_KEPT), "{released:?}");
    if !changelog.lines().any(|line| line == "## [Unreleased]") {
        released.pop();
    }
    let kept = releases();
    for version in released {
        assert!(
            numbers(version) < numbers(FIRST_KEPT) || kept.iter().any(|release| release == version),
            "server {version} is released and the directory it leaves is not kept: \
             server/testdata/stored/README.md says how to write it"
        );
    }
}

/// The key every kept directory is sealed under: the writer gives it to the released server, as an
/// operator gives a second instance a copy of the first one's key. It is these 32 bytes, in the open for
/// everyone to read, so no kept directory holds a key that was ever anybody's secret.
const KEY: &[u8; KEY_BYTES] = b"brigade-stored-form-test-key-385";

/// The key as a server keeps it in `record.key`.
fn key_file() -> String {
    let digits: String = KEY.iter().map(|byte| format!("{byte:02x}")).collect();
    format!("{digits}\n")
}

/// Every kept directory is sealed under the test's own key and under no other. A directory whose key is
/// anything else was not made by the writer here, and its key may be somebody's.
#[test]
fn every_kept_directory_is_sealed_under_the_tests_own_key() {
    for release in &releases() {
        for data in ["data", "feed-data"] {
            assert_eq!(
                std::fs::read_to_string(kept(release).join(data).join("record.key")).unwrap(),
                key_file(),
                "server {release}, {data}"
            );
        }
    }
}

/// The log of a data directory opened: every entry as the bytes the server sealed, in order.
fn entries_of(data: &Path) -> Vec<Vec<u8>> {
    let hex = std::fs::read_to_string(data.join("record.key")).unwrap();
    let mut key = [0_u8; KEY_BYTES];
    for (byte, pair) in key.iter_mut().zip(hex.trim().as_bytes().chunks(2)) {
        *byte = u8::from_str_radix(std::str::from_utf8(pair).unwrap(), 16).unwrap();
    }
    let sealer = Sealer::new(&key).unwrap();
    let mut log = FileLog::open(&data.join("log/records.log")).unwrap();
    let mut entries = Vec::new();
    log.read_from(0, &mut |sealed, _| {
        entries.push(sealer.open(sealed)?);
        Ok(())
    })
    .unwrap();
    entries
}

/// One member of a team on the server that is writing: its credential, its principal, its team.
struct Member {
    credential: String,
    principal_ref: String,
    team_ref: String,
}

/// What the scenario asks of the released server while it runs.
struct Asking<'a>(&'a Server);

impl Asking<'_> {
    fn member(result: &Value) -> Member {
        Member {
            credential: text(result, "credential"),
            principal_ref: text(result, "principal_ref"),
            team_ref: text(result, "team_ref"),
        }
    }

    fn create(&self, team_name: &str, human_label: &str) -> (Member, String) {
        let (status, created) = self.0.post(
            None,
            "/v1/team/create",
            &json!({"team_name": team_name, "human_label": human_label}),
        );
        assert_eq!(status, 200, "{created}");
        (
            Self::member(&created["result"]),
            text(&created["result"], "join_secret"),
        )
    }

    fn join(&self, secret: &str, human_label: &str) -> Member {
        let (status, joined) = self.0.post(
            None,
            "/v1/team/join",
            &json!({"join_secret": secret, "human_label": human_label}),
        );
        assert_eq!(status, 200, "{joined}");
        Self::member(&joined["result"])
    }

    fn ask(&self, member: &Member, path: &str, body: Value) -> Value {
        self.0.ok(&member.credential, path, &body)
    }

    fn register(&self, member: &Member, facts: Value) -> String {
        let mut registration = json!({
            "team_ref": member.team_ref, "harness": "claude-code", "harness_version": "2.1.318",
            "activity": "busy", "inbound": "accept",
        });
        for (name, value) in facts.as_object().unwrap() {
            registration[name] = value.clone();
        }
        text(
            &self.ask(member, "/v1/session/register", registration),
            "session_id",
        )
    }

    fn send(&self, member: &Member, from: &str, to: &str, more: Value) -> String {
        let mut message = json!({"sender_session_id": from, "recipient_session_id": to});
        for (name, value) in more.as_object().unwrap() {
            message[name] = value.clone();
        }
        text(&self.ask(member, "/v1/message/send", message), "message_id")
    }
}

/// Runs one of the operator's commands of `binary` on the directory, which no server is running on, with
/// the options `log` of the log the directory holds.
fn operator(binary: &Path, scratch: &Scratch, log: &[String], arguments: &[&str]) {
    let output = Command::new(binary)
        .arg("--data")
        .arg(scratch.path("data"))
        .args(log)
        .args(arguments)
        .stdin(Stdio::null())
        .output()
        .unwrap();
    assert!(
        output.status.success(),
        "{arguments:?}: {}{}",
        String::from_utf8_lossy(&output.stdout),
        String::from_utf8_lossy(&output.stderr)
    );
}

/// The references of two teams that live on another backend, as their join secrets there name them.
const ELSEWHERE: &str = "0f0f0f0f-0f0f-4f0f-8f0f-0f0f0f0f0f0f";
const TAKEN_IN: &str = "1e1e1e1e-1e1e-4e1e-8e1e-1e1e1e1e1e1e";

/// What the first run hands to the second.
struct FirstRun {
    alice: Member,
    bob: Member,
    alpha: String,
    beta: String,
    third: String,
    doomed: String,
}

/// What a server is asked in its first run: a team whose sessions hold pending and acknowledged messages,
/// and a team the operator removes afterwards.
fn first_run(server: &Server) -> FirstRun {
    let asking = Asking(server);
    let (alice, secret) = asking.create("ops", "alice");
    let bob = asking.join(&secret, "bob");
    let carol = asking.join(&secret, "carol");
    // A session with every fact a registration can carry, and two with none of the optional ones.
    let alpha = asking.register(
        &alice,
        json!({
            "session_name": "alpha", "session_description": "reviewing the ledger migration",
            "lease_seconds": 300, "workspace_label": "ledger", "human_label": "alice@example.com",
            "model": "claude-fable-5-2", "context_used_tokens": 181_000, "brigade_version": "0.57.0",
            "sync_peer": "syncthing:ABCDEFG-HIJKLMN",
        }),
    );
    let beta = asking.register(&bob, json!({"session_name": "beta"}));
    let gamma = asking.register(&carol, json!({"session_name": "gamma"}));
    // A heartbeat that changes everything a heartbeat can, and one that changes nothing.
    asking.ask(
        &alice,
        "/v1/session/heartbeat",
        json!({"session_id": alpha, "heartbeat": {
            "activity": "idle", "session_name": "alpha-2", "session_description": "",
            "inbound": "hold", "lease_seconds": 600, "model": "claude-opus-5-5",
            "context_used_tokens": 182_500, "brigade_version": "0.57.1", "sync_peer": "",
        }}),
    );
    asking.ask(
        &bob,
        "/v1/session/heartbeat",
        json!({"session_id": beta, "heartbeat": {}}),
    );
    // Three messages to beta: two it acknowledges and one it does not. The second has no summary and no
    // key, and a body with what JSON escapes.
    let first = asking.send(
        &alice,
        &alpha,
        &beta,
        json!({"body": "The migration is merged.", "summary": "Migration landed", "idempotency_key": "key-1"}),
    );
    let second = asking.send(
        &alice,
        &alpha,
        &beta,
        json!({"body": "two lines\nand \"quotes\", a tab\t, \u{2028} and \u{e9}"}),
    );
    let third = asking.send(
        &alice,
        &alpha,
        &beta,
        json!({"body": "Still waiting when the server stopped.", "idempotency_key": "key-3"}),
    );
    let received = asking.ask(
        &bob,
        "/v1/message/receive",
        json!({"session_id": beta, "limit": 10}),
    );
    assert_eq!(received["messages"].as_array().unwrap().len(), 3, "{received}");
    asking.ask(
        &bob,
        "/v1/message/ack",
        json!({"session_id": beta, "message_ids": [first, second]}),
    );
    // An answer, which carries the message it answers and a hop count of its own.
    asking.send(
        &bob,
        &beta,
        &alpha,
        json!({"body": "Seen, thanks.", "reply_to": first, "idempotency_key": "key-4"}),
    );
    // A session that is closed and opened again in place.
    asking.ask(&carol, "/v1/session/close", json!({"session_id": gamma}));
    asking.register(
        &carol,
        json!({"session_name": "gamma", "resume": {"session_id": gamma}}),
    );
    // A team the operator removes while the server is down, with sessions and a message of its own.
    let (kate, doomed_secret) = asking.create("doomed", "kate");
    let leo = asking.join(&doomed_secret, "leo");
    let kappa = asking.register(&kate, json!({"session_name": "kappa"}));
    let lambda = asking.register(&leo, json!({"session_name": "lambda"}));
    asking.send(&kate, &kappa, &lambda, json!({"body": "Gone with the team."}));
    FirstRun {
        doomed: kate.team_ref.clone(),
        alice,
        bob,
        alpha,
        beta,
        third,
    }
}

/// What the operator does between the two runs: takes a team in, opens and closes its move, and removes a
/// team.
fn between_the_runs(binary: &Path, scratch: &Scratch, log: &[String], first: &FirstRun) -> String {
    let secret = format!("brg1.{ELSEWHERE}.kept-from-the-other-backend");
    let secret_file = scratch.path("elsewhere.join-secret");
    std::fs::write(&secret_file, format!("{secret}\n")).unwrap();
    let (secret_file, ticket_file) = (
        secret_file.display().to_string(),
        scratch.path("elsewhere.move-ticket").display().to_string(),
    );
    let commands: [&[&str]; 4] = [
        &["--import-team", "elsewhere", "--join-secret-file", &secret_file],
        &["--open-move", ELSEWHERE, "--ticket-file", &ticket_file],
        &["--close-move", ELSEWHERE],
        &["--remove-team", &first.doomed],
    ];
    for command in commands {
        operator(binary, scratch, log, command);
    }
    secret
}

/// What the server is asked in its second run: the first team goes on, and a second one has every record
/// of a team's administration.
fn second_run(server: &Server, first: &FirstRun, elsewhere: &str) {
    let asking = Asking(server);
    let FirstRun {
        alice,
        bob,
        alpha,
        beta,
        third,
        ..
    } = first;
    asking.send(
        alice,
        alpha,
        beta,
        json!({"body": "Sent after the restart.", "idempotency_key": "key-5"}),
    );
    asking.ask(
        bob,
        "/v1/message/ack",
        json!({"session_id": beta, "message_ids": [third]}),
    );
    let delta = asking.register(bob, json!({"session_name": "delta"}));
    asking.ask(bob, "/v1/session/close", json!({"session_id": delta}));
    asking.ask(
        alice,
        "/v1/session/heartbeat",
        json!({"session_id": alpha, "heartbeat": {"activity": "busy"}}),
    );

    let (erin, lab_secret) = asking.create("lab", "erin");
    let team = json!({"team_ref": erin.team_ref});
    let frank = asking.join(&lab_secret, "frank");
    asking.join(&lab_secret, "grace");
    let rotated = text(
        &asking.ask(&erin, "/v1/team/rotate-secret", team.clone()),
        "join_secret",
    );
    let heidi = asking.join(&rotated, "heidi");
    asking.ask(
        &erin,
        "/v1/team/revoke-member",
        json!({"team_ref": erin.team_ref, "principal_ref": frank.principal_ref, "ban": true}),
    );
    asking.ask(
        &erin,
        "/v1/team/revoke-member",
        json!({"team_ref": erin.team_ref, "joined_secret_version_lte": 1}),
    );
    asking.ask(
        &erin,
        "/v1/team/transfer",
        json!({"team_ref": erin.team_ref, "principal_ref": heidi.principal_ref}),
    );
    asking.ask(&heidi, "/v1/team/open-move", team.clone());
    asking.ask(&heidi, "/v1/team/close-move", team.clone());
    let ivan = asking.join(&rotated, "ivan");
    asking.ask(&ivan, "/v1/team/leave", team);
    let judy = asking.join(&rotated, "judy");
    asking.ask(&judy, "/v1/credential/revoke", json!({}));

    // A team taken in by a request that carries a retry key, and the first member of the team the operator
    // took in, who owns it from then on.
    let (status, taken) = server.post(
        None,
        "/v1/team/take-in",
        &json!({
            "join_secret": format!("brg1.{TAKEN_IN}.kept-from-the-other-backend"),
            "team_name": "taken", "human_label": "mallory", "retry_key": RETRY_KEY,
        }),
    );
    assert_eq!(status, 200, "{taken}");
    asking.join(elsewhere, "nina");
}

/// A retry key as a client mints one.
const RETRY_KEY: &str = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";

/// The files a server keeps its state in, under its data directory: the key, the snapshots, and the log,
/// which is one file or the directory of a Cavalry feed. Its lock and its certificate are not among them.
fn kept_files(data: &Path) -> Vec<PathBuf> {
    let mut files = vec![PathBuf::from("record.key")];
    for dir in ["snapshots", "log", "feed"] {
        let Ok(entries) = std::fs::read_dir(data.join(dir)) else {
            continue;
        };
        files.extend(entries.map(|entry| Path::new(dir).join(entry.unwrap().file_name())));
    }
    files.sort();
    files
}

/// Copies what a server keeps of `from` into `to`, as private as a server makes it. A checkout keeps none
/// of the modes.
fn copy_the_directory(from: &Path, to: &Path) {
    use std::os::unix::fs::{DirBuilderExt, PermissionsExt};

    for file in kept_files(from) {
        std::fs::DirBuilder::new()
            .recursive(true)
            .mode(0o700)
            .create(to.join(&file).parent().unwrap())
            .unwrap();
        std::fs::copy(from.join(&file), to.join(&file)).unwrap();
        std::fs::set_permissions(to.join(&file), std::fs::Permissions::from_mode(0o600)).unwrap();
    }
}

/// Copies a feed's data directory into the place it is kept, and returns the lengths that were cut. A
/// recording's file is 128 MiB long whatever it holds, and all but its first kilobytes are zeros that the
/// file system never stored: it is kept up to its last byte that is not zero, with its length on a line of
/// the list this returns, `<length> <file>`. Every other file is kept as it is.
#[cfg(feature = "cavalry")]
fn keep_the_feed(from: &Path, to: &Path) -> String {
    let mut lengths = String::new();
    for file in kept_files(from) {
        std::fs::create_dir_all(to.join(&file).parent().unwrap()).unwrap();
        let mut bytes = std::fs::read(from.join(&file)).unwrap();
        if file.extension().is_some_and(|extension| extension == "rec") {
            lengths.push_str(&format!("{} {}\n", bytes.len(), file.display()));
            let held = bytes
                .iter()
                .rposition(|byte| *byte != 0)
                .map_or(0, |last| last + 1);
            bytes.truncate(held);
        }
        std::fs::write(to.join(&file), bytes).unwrap();
    }
    lengths
}

/// Puts a feed's data directory back as its server left it: a copy of what is kept, with every file that
/// was cut given its length again. The zeros come back as a hole, which costs nothing on the disk.
#[cfg(feature = "cavalry")]
fn the_feed_a_release_left(release: &str, scratch: &Scratch) {
    copy_the_directory(&kept(release).join("feed-data"), &scratch.path("data"));
    let lengths = std::fs::read_to_string(kept(release).join("feed-lengths")).unwrap();
    for line in lengths.lines() {
        let (length, file) = line.split_once(' ').expect("a length and a file");
        std::fs::OpenOptions::new()
            .write(true)
            .open(scratch.path("data").join(file))
            .unwrap()
            .set_len(length.parse().unwrap())
            .unwrap();
    }
}

/// Makes the data directory with the test's key in it, before the server's first start, so that the server
/// makes none.
fn with_the_tests_key(scratch: &Scratch) {
    use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};

    std::fs::DirBuilder::new()
        .mode(0o700)
        .create(scratch.path("data"))
        .unwrap();
    std::io::Write::write_all(
        &mut std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(scratch.path("data").join("record.key"))
            .unwrap(),
        key_file().as_bytes(),
    )
    .unwrap();
}

/// Has the released server leave a directory on the file log, stopped, and puts it and its log opened into
/// `whole`.
fn leave_a_directory(binary: &Path, whole: &Path) {
    let scratch = Scratch::new("stored-writer");
    let log = ["--log".to_owned(), "file".to_owned()];
    with_the_tests_key(&scratch);
    let start = || Server::try_start_the(binary, &scratch, &log).unwrap();

    let server = start();
    let first = first_run(&server);
    server.stop();
    let elsewhere = between_the_runs(binary, &scratch, &log, &first);
    let server = start();
    second_run(&server, &first, &elsewhere);
    server.stop();

    copy_the_directory(&scratch.path("data"), &whole.join("data"));
    let opened: Vec<u8> = entries_of(&whole.join("data"))
        .into_iter()
        .flat_map(|entry| [entry, b"\n".to_vec()].concat())
        .collect();
    std::fs::write(whole.join("entries.jsonl"), opened).unwrap();
}

/// Has the released server leave a directory on the Cavalry feed, killed, and puts it into `whole` with
/// what that server itself makes of it. The feed is rolled at every snapshot and a snapshot is taken every
/// eight entries, so the directory holds what a server's does after months: recordings one after another on
/// one line of positions, the earliest purged, and a last one that the kill left live with entries in it
/// that no snapshot covers.
#[cfg(feature = "cavalry")]
fn leave_a_feed(binary: &Path, whole: &Path) {
    let scratch = Scratch::new("stored-writer-feed");
    let roll = ["--feed-roll-bytes".to_owned(), "1".to_owned()];
    let log = [common::log_arguments("cavalry", &scratch), roll.to_vec()].concat();
    let running = [log.clone(), vec!["--snapshot-every".to_owned(), "8".to_owned()]].concat();
    with_the_tests_key(&scratch);
    let start = || Server::try_start_the(binary, &scratch, &running).unwrap();

    let server = start();
    let first = first_run(&server);
    server.stop();
    let elsewhere = between_the_runs(binary, &scratch, &log, &first);
    let server = start();
    second_run(&server, &first, &elsewhere);
    // Killed while nothing is being answered: everything the directory holds was answered.
    server.kill();
    let lengths = keep_the_feed(&scratch.path("data"), &whole.join("feed-data"));
    std::fs::write(whole.join("feed-lengths"), lengths).unwrap();

    // What the release itself makes of what it left: started again, it reads the entries after its newest
    // snapshot and writes the state at the feed's end.
    start().stop();
    let (_, end) = snapshots_of(&scratch.path("data")).pop().unwrap();
    std::fs::copy(end, whole.join("feed-end.json")).unwrap();
}

/// Copies a directory and everything in it.
#[cfg(feature = "cavalry")]
fn copy_everything(from: &Path, to: &Path) {
    std::fs::create_dir_all(to).unwrap();
    for entry in std::fs::read_dir(from).unwrap() {
        let (path, name) = (entry.as_ref().unwrap().path(), entry.unwrap().file_name());
        if path.is_dir() {
            copy_everything(&path, &to.join(name));
        } else {
            std::fs::copy(&path, to.join(name)).unwrap();
        }
    }
}

/// Not a test of this build: it writes the directories a released server leaves, for the tests here and in
/// `brigade-core` to read. It runs only when it is asked by name with the released binary and the place to
/// write to in its environment, and `server/testdata/stored/README.md` has the command.
#[test]
#[ignore = "writes a fixture with a released server's binary"]
fn write_the_directory_a_released_server_leaves() {
    let (binary, out) = match (
        std::env::var("BRIGADE_RELEASED_SERVER"),
        std::env::var("BRIGADE_STORED_OUT"),
    ) {
        (Ok(binary), Ok(out)) => (PathBuf::from(binary), PathBuf::from(out)),
        // Asked for with the other tests that are ignored, and not to write anything.
        (Err(_), Err(_)) => return,
        _ => panic!("BRIGADE_RELEASED_SERVER and BRIGADE_STORED_OUT go together, and one of them is not set"),
    };
    assert!(!out.exists(), "{} is there already", out.display());
    // Put together in the test's own directory first: what can fail, the reading of the log among it, has
    // failed before anything is at the place asked for, so a directory with a release's name is whole.
    let whole = Scratch::new("stored-whole");
    leave_a_directory(&binary, &whole.path("0"));
    // A kept directory has both, and the tests ask every one for both.
    #[cfg(not(feature = "cavalry"))]
    panic!("the writer leaves a directory on the Cavalry feed too: run it with the default features");
    #[cfg(feature = "cavalry")]
    leave_a_feed(&binary, &whole.path("0"));
    #[cfg(feature = "cavalry")]
    copy_everything(&whole.path("0"), &out);
}

/// The snapshots of a data directory, the oldest first: each one's position and its path.
fn snapshots_of(data: &Path) -> Vec<(u64, PathBuf)> {
    let mut snapshots: Vec<(u64, PathBuf)> = std::fs::read_dir(data.join("snapshots"))
        .unwrap()
        .map(|entry| entry.unwrap().path())
        .map(|path| {
            let name = path.file_name().unwrap().to_str().unwrap();
            let position = name
                .strip_prefix("snapshot-")
                .and_then(|name| name.strip_suffix(".json"))
                .expect("a snapshot's name");
            (position.parse().unwrap(), path)
        })
        .collect();
    snapshots.sort();
    snapshots
}

/// The state a snapshot file holds, as JSON: what comes after its header line.
fn state_in(snapshot: &Path) -> Value {
    let file = std::fs::read_to_string(snapshot).unwrap();
    serde_json::from_str(file.split_once('\n').expect("a header line").1).unwrap()
}

/// The lines kept beside a release's directory are what its sealed log holds: opened with the directory's
/// own key, entry for entry and byte for byte. So what `brigade-core` reads as the release's entries is
/// what the release wrote, and nobody's edit of it.
#[test]
fn the_log_a_release_left_opens_to_the_entries_kept_beside_it() {
    for release in &releases() {
        let scratch = Scratch::new(&format!("stored-log-{release}"));
        copy_the_directory(&kept(release).join("data"), &scratch.path("data"));
        let opened: Vec<String> = entries_of(&scratch.path("data"))
            .into_iter()
            .map(|entry| String::from_utf8(entry).unwrap())
            .collect();
        let lines = std::fs::read_to_string(kept(release).join("entries.jsonl")).unwrap();
        assert_eq!(opened, lines.lines().collect::<Vec<_>>(), "server {release}");
    }
}

/// This build starts on the directory a release left. From the release's newest snapshot it has nothing
/// to read after it. From the one before it, it reads the entries the release wrote after that snapshot,
/// and it reaches the state the release itself had: what it writes as its own snapshot there holds what the
/// release's snapshot holds, member for member. No snapshot is passed over either time: a snapshot this
/// build could not read would be, and the start would go on from an older one without a word of failure.
#[test]
fn this_build_starts_on_the_directory_a_release_left_and_reaches_the_state_the_release_had() {
    for release in &releases() {
        let arguments = ["--log".to_owned(), "file".to_owned()];
        let left = snapshots_of(&kept(release).join("data"));
        let [.., (before, _), (newest, newest_file)] = &left[..] else {
            panic!("server {release} left fewer than two snapshots");
        };
        assert!(before < newest);
        // Starts this build on the directory, stops it, and returns what it said.
        let started_clean = |scratch: &Scratch| {
            Server::start(scratch, &arguments).stop();
            let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
            assert!(!said.contains("passed over"), "server {release}: {said}");
            said
        };

        // The directory as the release left it.
        let scratch = Scratch::new(&format!("stored-start-{release}"));
        copy_the_directory(&kept(release).join("data"), &scratch.path("data"));
        let said = started_clean(&scratch);
        assert!(!said.contains("records after the newest snapshot"), "{said}");
        assert_eq!(
            snapshots_of(&scratch.path("data")).len(),
            left.len(),
            "a start that read nothing after the newest snapshot wrote none"
        );

        // The same directory without its newest snapshot, as after a kill: the entries after the one
        // before it are read again.
        let scratch = Scratch::new(&format!("stored-replay-{release}"));
        copy_the_directory(&kept(release).join("data"), &scratch.path("data"));
        std::fs::remove_file(
            scratch
                .path("data")
                .join("snapshots")
                .join(newest_file.file_name().unwrap()),
        )
        .unwrap();
        let said = started_clean(&scratch);
        // The positive control that it read them: the release counted its entries in each snapshot.
        let entries = |snapshot: &Path| state_in(snapshot)["entries"].as_u64().unwrap();
        let after = entries(newest_file) - entries(&left[left.len() - 2].1);
        assert!(
            after > 0 && said.contains(&format!("read {after} records after the newest snapshot")),
            "{after} entries: {said}"
        );
        let written = snapshots_of(&scratch.path("data"));
        let (position, file) = written.last().unwrap();
        assert_eq!(position, newest, "this build's snapshot is of the log's end");
        assert_eq!(
            state_in(file),
            state_in(newest_file),
            "server {release} held one state at the end of its log, and this build reaches another from \
             its entries"
        );
    }
}

/// This build starts on the feed a release left when it was killed, and the feed goes on (work item 387).
/// The directory holds what a server's does after months on the Cavalry of that release: recordings in that
/// Cavalry's format and with its terms, the earliest purged, the last still live in the catalog.
///
/// The first start reads the entries after the release's newest snapshot out of the release's recordings
/// and reaches the state the release itself reached when it was started again on what it had left: the same
/// position, and the same state member for member. Then a team is created, which the feed takes in a
/// recording of this build's own shape. And a start from the release's snapshot once more, as after a kill
/// with this build's snapshots lost, reads the release's recordings and this build's in one read, which
/// crosses the two term lengths.
#[cfg(feature = "cavalry")]
#[test]
fn this_build_starts_on_the_feed_a_release_left_killed_and_the_feed_goes_on() {
    for release in &releases() {
        let scratch = Scratch::new(&format!("stored-feed-{release}"));
        the_feed_a_release_left(release, &scratch);
        let arguments = common::log_arguments("cavalry", &scratch);
        let entries = |snapshot: &Path| state_in(snapshot)["entries"].as_u64().unwrap();
        let left = snapshots_of(&scratch.path("data"));
        let (newest, newest_file) = left.last().unwrap().clone();
        // What the release reached: its own snapshot of the feed's end, with its position in its header.
        let reached = kept(release).join("feed-end.json");
        let header = std::fs::read_to_string(&reached).unwrap();
        let header: Value = serde_json::from_str(header.split_once('\n').unwrap().0).unwrap();
        let end = header["position"].as_u64().unwrap();
        let after = entries(&reached) - entries(&newest_file);
        assert!(
            newest < end && after > 0,
            "server {release}: {newest} {end} {after}"
        );
        let said_after_stopping = |server: Server| {
            server.stop();
            let said = std::fs::read_to_string(scratch.path("stderr")).unwrap();
            assert!(!said.contains("passed over"), "server {release}: {said}");
            said
        };

        // The first start on the release's directory.
        let said = said_after_stopping(Server::start(&scratch, &arguments));
        assert!(
            said.contains(&format!("read {after} records after the newest snapshot")),
            "{after} entries: {said}"
        );
        let written = snapshots_of(&scratch.path("data"));
        let (position, file) = written.last().unwrap();
        assert_eq!(*position, end, "this build's feed ends where the release's did");
        assert_eq!(
            state_in(file),
            state_in(&reached),
            "server {release} reached one state on the feed it had left, and this build reaches another"
        );

        // The feed goes on: a team is created, in a recording of this build's own.
        let server = Server::start(&scratch, &arguments);
        let (status, created) = server.post(
            None,
            "/v1/team/create",
            &json!({"team_name": "after-the-move", "human_label": "zoe"}),
        );
        assert_eq!(status, 200, "{created}");
        said_after_stopping(server);

        // From the release's snapshot once more: its recordings and this build's, in one read. The snapshot
        // is still there, and what it needs of the feed is, because a server keeps three snapshots and lets
        // go of nothing after the oldest of them, and this test has started it twice.
        assert!(
            snapshots_of(&scratch.path("data"))
                .iter()
                .any(|(position, _)| *position == newest),
            "the release's newest snapshot is gone after two starts: the server keeps fewer than three now, \
             or this test starts it more often before this stage"
        );
        for (position, file) in snapshots_of(&scratch.path("data")) {
            if position > newest {
                std::fs::remove_file(file).unwrap();
            }
        }
        let said = said_after_stopping(Server::start(&scratch, &arguments));
        assert!(
            said.contains(&format!("read {} records after the newest snapshot", after + 1)),
            "{} entries: {said}",
            after + 1
        );
        let (_, file) = snapshots_of(&scratch.path("data")).pop().unwrap();
        let state = state_in(&file);
        assert_eq!(state["entries"].as_u64(), Some(entries(&reached) + 1));
        assert!(
            state["teams"]
                .as_object()
                .unwrap()
                .values()
                .any(|team| team["team_name"] == json!("after-the-move")),
            "the team created after the move is in the state"
        );
    }
}
