//! The stored form of every record and of the state, as a released server wrote it (work item 385).
//!
//! `server/testdata/stored/<version>/entries.jsonl` is the log of a released server opened, one entry to a
//! line, and `data/snapshots` beside it holds its snapshots: the state as JSON under one header line. Every
//! data directory an operator has is made of these. A member added to a record or to the state without a
//! default makes this build refuse them, and here that is a test that fails with the member's name, where it
//! would otherwise be a server that does not start. `brigade-server`'s `tests/stored.rs` holds that the
//! lines are what the release's sealed log holds, and starts this build on the directory.

use std::collections::BTreeSet;
use std::path::{Path, PathBuf};

use brigade_core::State;
use brigade_core::record::{Entry, Event};
use serde_json::Value;

/// The first release whose stored form is kept. It is never taken away: a directory that release wrote is
/// somebody's until they say otherwise.
const FIRST_KEPT: &str = "0.9.0";

/// The released versions whose stored form this build is held to: every directory kept, which a release
/// adds its own to.
fn releases() -> Vec<String> {
    let kept = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/stored");
    let mut releases: Vec<String> = std::fs::read_dir(kept)
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

/// The kinds of record no kept release's log holds, each with why. A kind that is new since the last
/// release is named here until a release has written it; the two of a recorded move hold a move ticket
/// whole, which is the shape of a join secret, and no file of the repository holds one. `record.rs` has
/// their bytes, written by hand.
const HELD_BY_NO_RELEASE: [&str; 2] = ["team.move_cleared", "team.move_recorded"];

fn kept(release: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/stored")
        .join(release)
}

/// Every kind of record this build knows, by the name the log gives it. The list is the build's own and
/// nothing here keeps it in step: it is what the reader says it expects when it meets a kind it does not
/// know, so a kind added to [`Event`] is in it from the moment it compiles.
fn every_kind() -> BTreeSet<String> {
    let refusal = serde_json::from_str::<Event>(r#"{"kind":"no.such.kind"}"#)
        .expect_err("no record is of that kind")
        .to_string();
    let (_, expected) = refusal
        .split_once("expected one of ")
        .expect("the refusal of an unknown kind names the kinds there are");
    let kinds: BTreeSet<String> = expected
        .split('`')
        .skip(1)
        .step_by(2)
        .map(str::to_owned)
        .collect();
    assert!(kinds.len() >= 20 && kinds.contains("session.opened"), "{refusal}");
    kinds
}

/// The kinds a line of a log names, read as JSON and not as records: what the line holds, whether or not
/// this build reads it.
fn kinds_in(line: &str) -> Vec<String> {
    let entry: Value = serde_json::from_str(line).expect("a line of a kept log is JSON");
    entry["records"]
        .as_array()
        .expect("an entry has records")
        .iter()
        .map(|record| {
            record["kind"]
                .as_str()
                .expect("a record names its kind")
                .to_owned()
        })
        .collect()
}

/// Every entry a release wrote is read by this build, and this build writes it as the release did, byte
/// for byte: no member is missing that this build asks for, and none is added that it always writes.
#[test]
fn every_entry_a_release_wrote_is_read_and_is_written_the_same() {
    let mut held = BTreeSet::new();
    for release in &releases() {
        let log = std::fs::read_to_string(kept(release).join("entries.jsonl")).unwrap();
        for (number, line) in log.lines().enumerate() {
            let entry = Entry::decode(line.as_bytes()).unwrap_or_else(|why| {
                panic!(
                    "server {release} wrote entry {} of {:?}, and this build does not read it: {why}",
                    number + 1,
                    kinds_in(line)
                )
            });
            assert_eq!(
                String::from_utf8(entry.encode()).unwrap(),
                line,
                "this build writes entry {} of server {release} with other bytes than that server did",
                number + 1
            );
            held.extend(kinds_in(line));
        }
    }
    // Every kind there is has its stored form held by a release's own bytes, or is named with the reason it
    // is not. A kind that is new is neither until it is put in one place or the other.
    let (kinds, named): (_, BTreeSet<String>) = (
        every_kind(),
        HELD_BY_NO_RELEASE.into_iter().map(str::to_owned).collect(),
    );
    let nowhere: Vec<&String> = kinds
        .difference(&held)
        .filter(|kind| !named.contains(*kind))
        .collect();
    assert!(
        nowhere.is_empty(),
        "no kept release's log holds a record of {nowhere:?}, and HELD_BY_NO_RELEASE does not name it: name \
         it there with the reason until a release has written one (server/testdata/stored/README.md)"
    );
    let wrongly: Vec<&String> = named
        .iter()
        .filter(|kind| held.contains(*kind) || !kinds.contains(*kind))
        .collect();
    assert!(
        wrongly.is_empty(),
        "HELD_BY_NO_RELEASE names {wrongly:?}, which a kept log holds or which is no kind of record"
    );
}

/// The state a snapshot file holds: what comes after its header line.
fn state_in(snapshot: &Path) -> String {
    let file = std::fs::read_to_string(snapshot).unwrap();
    file.split_once('\n').expect("a header line").1.to_owned()
}

/// Every snapshot a release wrote is read as the state by this build, and this build writes that state
/// with the members the release wrote and no other. The snapshots hold what a server's state is made of:
/// sessions with pending and acknowledged messages among it.
#[test]
fn every_snapshot_a_release_wrote_is_read_and_is_written_the_same() {
    for release in &releases() {
        // The snapshots of the directory on the file log, those of the one on the Cavalry feed, and the
        // one the release wrote when it was started again on the feed it had left.
        let mut snapshots = vec![kept(release).join("feed-end.json")];
        for data in ["data", "feed-data"] {
            let held = std::fs::read_dir(kept(release).join(data).join("snapshots")).unwrap();
            snapshots.extend(held.map(|entry| entry.unwrap().path()));
        }
        snapshots.sort();
        assert!(!snapshots.is_empty(), "server {release} left no snapshot");
        let (mut sessions, mut pending, mut acked, mut keys) = (0, 0, 0, 0);
        for snapshot in &snapshots {
            let written = state_in(snapshot);
            let state: State = serde_json::from_str(&written).unwrap_or_else(|why| {
                panic!(
                    "server {release} wrote {}, and this build does not read its state: {why}",
                    snapshot.display()
                )
            });
            assert_eq!(
                serde_json::to_value(&state).unwrap(),
                serde_json::from_str::<Value>(&written).unwrap(),
                "this build writes the state of {} with other members than server {release} did",
                snapshot.display()
            );
            for session in state.teams.values().flat_map(|team| team.sessions.values()) {
                sessions += 1;
                pending += session.pending.len();
                acked += session.acked.len();
                keys += session.idempotency.len();
            }
        }
        // The positive control: the state read is a state with everything in it, and not an empty one.
        assert!(
            sessions > 0 && pending > 0 && acked > 0 && keys > 0,
            "the snapshots of server {release} hold {sessions} sessions, {pending} pending and {acked} \
             acknowledged messages and {keys} idempotency keys"
        );
    }
}
