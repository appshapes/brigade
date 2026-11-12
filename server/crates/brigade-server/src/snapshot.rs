//! Snapshots: the state, written whole with the position of the log it stands for.
//!
//! A start reads the newest snapshot and applies the log's records after its position, so the log is read
//! from its beginning only once. The format is the server's own and names its version: a snapshot outlives a
//! change of the log's format, and it is what a new instance will be started from.
//!
//! A snapshot file is one header line, `{"format", "position", "sha256"}`, and then the state as JSON. It is
//! written beside its final name and renamed into place, so a file with a snapshot's name is always whole.

use std::fs::{self, File, OpenOptions};
use std::io::Write;
use std::os::unix::fs::OpenOptionsExt;
use std::path::{Path, PathBuf};

use brigade_core::State;
use brigade_core::hash::sha256_hex_of;
use brigade_log::Position;
use serde::{Deserialize, Serialize};

/// The version of the snapshot format.
const FORMAT: u32 = 1;

/// What the name of a snapshot ends with while it is being written.
const UNFINISHED: &str = ".unfinished";

/// How many snapshots are kept. More than one, so that a newest file that turns out damaged has one behind
/// it.
const KEPT: usize = 3;

#[derive(Serialize, Deserialize)]
struct Header {
    format: u32,
    position: Position,
    sha256: String,
}

fn name(position: Position) -> String {
    format!("snapshot-{position:020}.json")
}

/// The snapshots in `dir`, newest first.
fn listed(dir: &Path) -> Result<Vec<(Position, PathBuf)>, String> {
    let entries = fs::read_dir(dir).map_err(|error| format!("reading the snapshot directory: {error}"))?;
    let mut found = Vec::new();
    for entry in entries {
        let path = entry
            .map_err(|error| format!("reading the snapshot directory: {error}"))?
            .path();
        let position = path
            .file_name()
            .and_then(|name| name.to_str())
            .and_then(|name| name.strip_prefix("snapshot-"))
            .and_then(|name| name.strip_suffix(".json"))
            .and_then(|digits| digits.parse::<Position>().ok());
        if let Some(position) = position {
            found.push((position, path));
        }
    }
    found.sort_by_key(|(position, _)| std::cmp::Reverse(*position));
    Ok(found)
}

/// Writes the state as the snapshot of `position`, and lets go of the snapshots older than the few kept.
///
/// # Errors
/// A sentence that says what could not be written.
pub fn write(dir: &Path, position: Position, state: &State) -> Result<(), String> {
    let failed = |error: std::io::Error| format!("writing a snapshot: {error}");
    let body = serde_json::to_vec(state).map_err(|error| format!("writing a snapshot: {error}"))?;
    let header = Header {
        format: FORMAT,
        position,
        sha256: checksum(position, &body),
    };
    let mut bytes = serde_json::to_vec(&header).map_err(|error| format!("writing a snapshot: {error}"))?;
    bytes.push(b'\n');
    bytes.extend_from_slice(&body);

    let unfinished = dir.join(format!(".{}{UNFINISHED}", name(position)));
    let written = OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(&unfinished)
        .and_then(|mut file| {
            file.write_all(&bytes)?;
            file.sync_all()
        })
        .and_then(|()| fs::rename(&unfinished, dir.join(name(position))))
        // The rename is durable once the directory is.
        .and_then(|()| File::open(dir)?.sync_all());
    if let Err(error) = written {
        // A write that did not finish leaves nothing behind: on a disk with no room, what it left would
        // be the next thing in the way.
        let _ = fs::remove_file(&unfinished);
        return Err(failed(error));
    }

    for (_, old) in listed(dir)?.into_iter().skip(KEPT) {
        // A snapshot that cannot be removed costs disk and nothing else.
        let _ = fs::remove_file(old);
    }
    Ok(())
}

/// The position the log may be shortened to: that of the oldest snapshot that is kept, once at least two
/// are. The log lets go of what comes before it and of nothing after, so a start that passes over a damaged
/// snapshot still finds the one behind it and every record since.
///
/// # Errors
/// When the directory cannot be read.
pub fn oldest(dir: &Path) -> Result<Option<Position>, String> {
    let kept: Vec<Position> = listed(dir)?
        .into_iter()
        .take(KEPT)
        .map(|(position, _)| position)
        .collect();
    Ok(if kept.len() < 2 {
        None
    } else {
        kept.into_iter().min()
    })
}

/// The checksum of a snapshot: over the format, the position and the state, so that a damaged digit of the
/// position is caught like a damaged byte of the state.
fn checksum(position: Position, body: &[u8]) -> String {
    let mut covered = format!("{FORMAT}\n{position}\n").into_bytes();
    covered.extend_from_slice(body);
    sha256_hex_of(&covered)
}

/// Removes what a write that did not finish left behind.
pub fn tidy(dir: &Path) {
    let Ok(entries) = fs::read_dir(dir) else {
        return;
    };
    for path in entries.flatten().map(|entry| entry.path()) {
        if path.to_str().is_some_and(|name| name.ends_with(UNFINISHED)) {
            let _ = fs::remove_file(path);
        }
    }
}

/// Reads one snapshot file whole.
fn read(path: &Path, named: Position) -> Result<(Position, State), String> {
    let bytes = fs::read(path).map_err(|error| error.to_string())?;
    let end_of_header = bytes
        .iter()
        .position(|byte| *byte == b'\n')
        .ok_or("it has no header line")?;
    let header: Header = serde_json::from_slice(&bytes[..end_of_header])
        .map_err(|error| format!("its header does not read: {error}"))?;
    if header.format != FORMAT {
        return Err(format!(
            "it is of format {}, and this build reads format {FORMAT}",
            header.format
        ));
    }
    let body = &bytes[end_of_header + 1..];
    if checksum(header.position, body) != header.sha256 {
        return Err("it fails its checksum".to_owned());
    }
    // The position is read in two places, the name when the log is shortened and the header at a start:
    // they must be one number.
    if header.position != named {
        return Err(format!(
            "its header names position {} and its name {named}",
            header.position
        ));
    }
    let state = serde_json::from_slice(body).map_err(|error| format!("its state does not read: {error}"))?;
    Ok((header.position, state))
}

/// The newest snapshot that reads back whole, with its position. A newer file that is damaged is passed
/// over, and `skipped` is told why.
///
/// # Errors
/// When the directory cannot be read.
pub fn newest(dir: &Path, skipped: &mut dyn FnMut(&Path, &str)) -> Result<Option<(Position, State)>, String> {
    for (named, path) in listed(dir)? {
        match read(&path, named) {
            Ok(found) => return Ok(Some(found)),
            Err(why) => skipped(&path, &why),
        }
    }
    Ok(None)
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use brigade_core::state::{Principal, State};
    use brigade_core::time::Timestamp;

    use super::{KEPT, name, newest, oldest, tidy, write};

    fn scratch(test: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("brigade-snapshot-{test}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn state_with(principals: usize) -> State {
        let mut state = State::default();
        for index in 0..principals {
            state.principals.insert(
                format!("p{index}"),
                Principal {
                    created_at: Timestamp(index as i64),
                },
            );
        }
        state
    }

    #[test]
    fn the_newest_snapshot_is_the_one_read_and_old_ones_are_let_go() {
        let dir = scratch("newest");
        assert!(
            newest(&dir, &mut |_, _| panic!("nothing to skip"))
                .unwrap()
                .is_none()
        );
        // The log is shortened to nothing while fewer than two snapshots stand behind it.
        assert_eq!(oldest(&dir).unwrap(), None);
        write(&dir, 50, &state_with(1)).unwrap();
        assert_eq!(oldest(&dir).unwrap(), None);
        for position in 1..=5 {
            write(&dir, position * 100, &state_with(position as usize)).unwrap();
        }
        assert_eq!(oldest(&dir).unwrap(), Some(300));
        let (position, state) = newest(&dir, &mut |_, _| panic!("nothing to skip"))
            .unwrap()
            .unwrap();
        assert_eq!((position, state), (500, state_with(5)));
        let mut files: Vec<String> = std::fs::read_dir(&dir)
            .unwrap()
            .map(|entry| entry.unwrap().file_name().into_string().unwrap())
            .collect();
        files.sort();
        assert_eq!(files, vec![name(300), name(400), name(500)]);
        assert_eq!(files.len(), KEPT);
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_damaged_snapshot_is_passed_over_for_the_one_behind_it() {
        let dir = scratch("damaged");
        write(&dir, 10, &state_with(1)).unwrap();
        write(&dir, 20, &state_with(2)).unwrap();
        let newest_file = dir.join(name(20));
        let mut bytes = std::fs::read(&newest_file).unwrap();
        let last = bytes.len() - 2;
        bytes[last] ^= 1;
        std::fs::write(&newest_file, bytes).unwrap();

        let mut reasons = Vec::new();
        let found = newest(&dir, &mut |_, why| reasons.push(why.to_owned()))
            .unwrap()
            .unwrap();
        assert_eq!(found, (10, state_with(1)));
        assert_eq!(reasons, vec!["it fails its checksum".to_owned()]);

        // A file cut short, and one of a format this build does not read, are passed over the same way.
        std::fs::write(dir.join(name(30)), b"{\"format\":1").unwrap();
        std::fs::write(
            dir.join(name(40)),
            b"{\"format\":9,\"position\":40,\"sha256\":\"\"}\n{}",
        )
        .unwrap();
        let mut reasons = Vec::new();
        let found = newest(&dir, &mut |_, why| reasons.push(why.to_owned()))
            .unwrap()
            .unwrap();
        assert_eq!(found.0, 10);
        assert_eq!(reasons.len(), 3);
        assert!(reasons[0].contains("format 9"), "{reasons:?}");
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn the_position_is_covered_by_the_checksum_and_agrees_with_the_name() {
        let dir = scratch("position");
        write(&dir, 10, &state_with(1)).unwrap();
        write(&dir, 20, &state_with(2)).unwrap();
        // A digit of the header's position changed: the checksum covers it.
        let file = dir.join(name(20));
        let text = std::fs::read_to_string(&file).unwrap();
        std::fs::write(&file, text.replacen("\"position\":20", "\"position\":21", 1)).unwrap();
        let mut reasons = Vec::new();
        let found = newest(&dir, &mut |_, why| reasons.push(why.to_owned()))
            .unwrap()
            .unwrap();
        assert_eq!(
            (found.0, reasons.as_slice()),
            (10, &["it fails its checksum".to_owned()][..])
        );
        // A whole snapshot under another position's name is not that position's snapshot.
        std::fs::copy(dir.join(name(10)), dir.join(name(30))).unwrap();
        let mut reasons = Vec::new();
        let found = newest(&dir, &mut |_, why| reasons.push(why.to_owned()))
            .unwrap()
            .unwrap();
        assert_eq!(found.0, 10);
        assert!(reasons[0].contains("its name 30"), "{reasons:?}");
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_write_that_did_not_finish_leaves_nothing_to_be_read() {
        let dir = scratch("unfinished");
        write(&dir, 10, &state_with(1)).unwrap();
        let left = dir.join(format!(".{}.unfinished", name(20)));
        std::fs::write(&left, b"half a snapshot").unwrap();
        assert_eq!(
            newest(&dir, &mut |_, _| panic!("nothing to skip"))
                .unwrap()
                .unwrap()
                .0,
            10
        );
        tidy(&dir);
        assert!(!left.exists());
        assert!(dir.join(name(10)).exists());
        // A directory that cannot be written to is an error and not a panic.
        assert!(write(&dir.join("not-there"), 30, &state_with(1)).is_err());
        std::fs::remove_dir_all(dir).unwrap();
    }
}
