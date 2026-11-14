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

/// The version of the snapshot format this build writes.
///
/// Format 2 is format 1 with what a team and a member have had since server 0.4.0: a team's count of join
/// secrets, and the version a member joined with and whether it is banned. A build before that reads a
/// state without them and would drop them without a word, lifting every ban. It reads format 1 only, so a
/// snapshot that may hold them names a format that build passes over, and it then refuses the log's record
/// of a rotation or a revocation instead of starting on half of what the directory holds.
///
/// Format 3 is format 2 with what a team has had since server 0.5.0: the ticket of its move, while one is
/// open. A build before that would read the state without it, and a team whose move is open would be a
/// team with none: the ticket would stop admitting without a record that says so, and that build's own
/// snapshot would then say the same to this one. It reads formats 1 and 2, so it passes a format 3
/// snapshot over, and it refuses the log's record of a move instead.
///
/// What a principal's bounds count, since server 0.6.0, is in the state without a format of its own: the
/// times of the teams it created and the sessions it opened in the last hour. A build before that drops
/// them, and it has no bound to hold against them. A snapshot without them reads as nobody having created
/// a team or opened a session in that hour.
const FORMAT: u32 = 3;

/// The oldest snapshot format this build reads. A snapshot of format 1 holds no secret version and no ban,
/// and reads as the state it was: every team has its first secret, and nobody is banned. Neither format 1
/// nor format 2 holds a move, and both read as the state they were: no team is moving.
const OLDEST_FORMAT: u32 = 1;

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
        sha256: checksum(FORMAT, position, &body),
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
/// format or of the position is caught like a damaged byte of the state.
fn checksum(format: u32, position: Position, body: &[u8]) -> String {
    let mut covered = format!("{format}\n{position}\n").into_bytes();
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
    if !(OLDEST_FORMAT..=FORMAT).contains(&header.format) {
        return Err(format!(
            "it is of format {}, and this build reads formats {OLDEST_FORMAT} to {FORMAT}",
            header.format
        ));
    }
    let body = &bytes[end_of_header + 1..];
    if checksum(header.format, header.position, body) != header.sha256 {
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

    use brigade_core::hash::sha256_hex_of;
    use brigade_core::state::{MoveTicket, Principal, State};
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
                    ..Principal::default()
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
    fn a_snapshot_of_the_format_before_this_one_is_read_and_this_builds_names_the_newer() {
        let dir = scratch("formats");
        // A snapshot as server 0.3.0 wrote it: format 1, its checksum over that format, and a state whose
        // team has no secret version and whose member has neither the version it joined with nor a ban.
        let body = concat!(
            r#"{"principals":{"p1":{"created_at":5}},"credentials":{"h1":"p1"},"#,
            r#""teams":{"t":{"team_ref":"t","team_name":"ops","join_secret_hash":"s","created_by":"p1","#,
            r#""created_at":5,"members":{"p1":{"human_label":"alice","active":true,"joined_at":5,"#,
            r#""sent_at":[]}},"sessions":{}}},"session_teams":{},"swept_at":0,"latest_at":5,"entries":2}"#
        );
        let sha256 = sha256_hex_of(format!("1\n70\n{body}").as_bytes());
        let earlier = format!("{{\"format\":1,\"position\":70,\"sha256\":\"{sha256}\"}}\n{body}");
        std::fs::write(dir.join(name(70)), earlier).unwrap();
        let (position, state) = newest(&dir, &mut |_, why| panic!("passed over: {why}"))
            .unwrap()
            .unwrap();
        assert_eq!((position, state.entries), (70, 2));
        // It holds nothing a principal's bounds count, and reads as nobody having used any of them.
        let principal = &state.principals["p1"];
        assert!(principal.teams_created_at.is_empty() && principal.sessions_opened_at.is_empty());
        let team = &state.teams["t"];
        assert_eq!((team.secret_version, team.owner()), (1, Some("p1")));
        let member = &team.members["p1"];
        assert_eq!(
            (member.active, member.joined_secret_version, member.banned),
            (true, 1, false)
        );

        // What this build writes of that state names format 3, which the earlier builds pass over: they
        // would read the state without what a team and a member have now. It reads back whole here.
        write(&dir, 80, &state).unwrap();
        let written = std::fs::read_to_string(dir.join(name(80))).unwrap();
        assert!(written.starts_with(r#"{"format":3,"position":80,"#), "{written}");
        let found = newest(&dir, &mut |_, why| panic!("passed over: {why}"))
            .unwrap()
            .unwrap();
        assert_eq!(found, (80, state));
        // The format is inside what the checksum covers: a header that names another does not read.
        let renamed = written.replacen(r#""format":3"#, r#""format":1"#, 1);
        std::fs::write(dir.join(name(80)), renamed).unwrap();
        let mut reasons = Vec::new();
        let found = newest(&dir, &mut |_, why| reasons.push(why.to_owned()))
            .unwrap()
            .unwrap();
        assert_eq!(
            (found.0, reasons.as_slice()),
            (70, &["it fails its checksum".to_owned()][..])
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_snapshot_of_format_2_is_read_with_no_team_moving_and_a_move_is_written_as_format_3() {
        let dir = scratch("moves");
        // A snapshot as server 0.4.1 wrote it: format 2, its checksum over that format, a team that
        // counts its secrets and a member that is banned, and no move.
        let body = concat!(
            r#"{"principals":{"p1":{"created_at":5}},"credentials":{"h1":"p1"},"#,
            r#""teams":{"t":{"team_ref":"t","team_name":"ops","join_secret_hash":"s","created_by":"p1","#,
            r#""created_at":5,"secret_version":2,"members":{"p1":{"human_label":"alice","active":false,"#,
            r#""joined_at":5,"joined_secret_version":2,"banned":true,"sent_at":[]}},"sessions":{}}},"#,
            r#""session_teams":{},"swept_at":0,"latest_at":5,"entries":2}"#
        );
        let sha256 = sha256_hex_of(format!("2\n70\n{body}").as_bytes());
        let earlier = format!("{{\"format\":2,\"position\":70,\"sha256\":\"{sha256}\"}}\n{body}");
        std::fs::write(dir.join(name(70)), earlier).unwrap();
        let (position, mut state) = newest(&dir, &mut |_, why| panic!("passed over: {why}"))
            .unwrap()
            .unwrap();
        assert_eq!(position, 70);
        let team = &state.teams["t"];
        assert_eq!((team.secret_version, team.move_ticket.as_ref()), (2, None));
        assert!(team.members["p1"].banned);
        // A state with no move is, but for its header, the bytes that build wrote: nothing is added to a
        // team that is not moving.
        write(&dir, 80, &state).unwrap();
        let written = std::fs::read_to_string(dir.join(name(80))).unwrap();
        assert_eq!(written.split_once('\n').unwrap().1, body);
        assert!(written.starts_with(r#"{"format":3,"position":80,"#), "{written}");

        // A team whose move is open is written with its ticket's hash and end, and read back whole.
        let ticket = MoveTicket {
            hash: "hash-of-the-ticket".to_owned(),
            ends_at: Timestamp(9),
        };
        state.teams.get_mut("t").unwrap().move_ticket = Some(ticket.clone());
        write(&dir, 90, &state).unwrap();
        let written = std::fs::read_to_string(dir.join(name(90))).unwrap();
        assert!(
            written.contains(r#""move_ticket":{"hash":"hash-of-the-ticket","ends_at":9}"#),
            "{written}"
        );
        let (position, read) = newest(&dir, &mut |_, why| panic!("passed over: {why}"))
            .unwrap()
            .unwrap();
        assert_eq!((position, &read), (90, &state));
        assert_eq!(read.teams["t"].move_ticket, Some(ticket));
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
