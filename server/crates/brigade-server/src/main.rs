//! `brigade-server`: a Brigade backend that keeps a team on the file system.
//!
//! The server implements the backend REST contract (`docs/backend-rest-v1.md`), which the `rest` adapter
//! bundled in the `brigade` binary speaks. It is one process: the rules of `brigade-core`, the ordered log
//! of `brigade-log`, and this crate's HTTP layer, snapshots and wiring. The plan is
//! `.context/plans/brigade-server.md` (work item 89).

mod engine;
mod gateway;
mod http;
mod note;
mod options;
mod said;
mod snapshot;
mod tls;

use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::process::ExitCode;
use std::sync::Arc;
use std::time::Duration;

use tokio::net::TcpListener;
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::watch;

use crate::note::note;
use crate::options::{Asked, CloseMove, Import, LogKind, OpenMove, Options, SetOwner, TakeBack, Tls};

/// How long the requests that are being answered are given once the server is told to stop.
const DRAIN: Duration = Duration::from_secs(5);

fn main() -> ExitCode {
    match options::parse(std::env::args().skip(1)) {
        Ok(Asked::Help) => {
            print!("{}", options::USAGE);
            ExitCode::SUCCESS
        }
        Ok(Asked::Version) => {
            println!("{} {}", env!("CARGO_PKG_NAME"), env!("CARGO_PKG_VERSION"));
            ExitCode::SUCCESS
        }
        Ok(Asked::Run(options)) => ended(run(&options)),
        Ok(Asked::Import(import)) => ended(take_in(&import)),
        Ok(Asked::SetOwner(named)) => ended(name_owner(&named)),
        Ok(Asked::TakeBack(taken)) => ended(take_back(&taken)),
        Ok(Asked::OpenMove(opened)) => ended(open_move(&opened)),
        Ok(Asked::CloseMove(closed)) => ended(close_move(&closed)),
        Err(why) => {
            eprintln!("brigade-server: {why}\n\n{}", options::USAGE);
            ExitCode::from(2)
        }
    }
}

/// The status the process ends with, after the sentence that says why when it failed.
fn ended(result: Result<(), String>) -> ExitCode {
    match result {
        Ok(()) => ExitCode::SUCCESS,
        Err(why) => {
            note(&why);
            ExitCode::FAILURE
        }
    }
}

/// The token a file holds: its one line, without the line's end.
fn create_token(options: &Options) -> Result<Option<String>, String> {
    let Some(path) = &options.create_token_file else {
        return Ok(None);
    };
    let text =
        fs::read_to_string(path).map_err(|error| format!("{} could not be read: {error}", path.display()))?;
    let token = text.trim();
    if token.is_empty() {
        return Err(format!("{} holds no creation token", path.display()));
    }
    Ok(Some(token.to_owned()))
}

/// The join secret a file holds: its text, without the white space around it.
///
/// The sentences name the option, and never the path or anything the file held: a secret that was typed
/// where the path belongs is not repeated.
fn join_secret(path: &Path) -> Result<String, String> {
    let text = fs::read_to_string(path)
        .map_err(|error| format!("the file --join-secret-file names could not be read: {error}"))?;
    let secret = text.trim();
    if secret.is_empty() {
        return Err("the file --join-secret-file names holds no join secret".to_owned());
    }
    Ok(secret.to_owned())
}

/// Makes the data directory when it is not there, and locks it for as long as the lock is held.
///
/// One server on one directory: a second would write a second order into the same log, and so would a team
/// taken in or taken back, an owner named, or a move opened or closed, beside a server that runs.
fn lock(options: &Options) -> Result<File, String> {
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&options.data)
        .map_err(|error| format!("{} could not be created: {error}", options.data.display()))?;
    let lock = File::create(options.data.join("lock"))
        .map_err(|error| format!("{} could not be locked: {error}", options.data.display()))?;
    lock.try_lock()
        .map_err(|_| format!("another server is running on {}", options.data.display()))?;
    Ok(lock)
}

/// Starts the engine on the data directory: its log opened, its newest snapshot read and the records after
/// it applied.
fn open(options: &Options) -> Result<engine::Started, String> {
    if options.log == LogKind::File {
        note(
            "the log is one file, which is for tests: a server that holds a team's messages runs on the Cavalry feed",
        );
    }
    let started = engine::start(engine::Settings {
        data: options.data.clone(),
        log: options.log,
        feed_control: options.feed_control.clone(),
        feed_self: options.feed_self.clone(),
        driver_dir: options.driver_dir.clone(),
        snapshot_every: options.snapshot_every,
        feed_roll_bytes: options.feed_roll_bytes,
    })?;
    if started.replayed > 0 {
        note(&format!(
            "read {} records after the newest snapshot",
            started.replayed
        ));
    }
    Ok(started)
}

/// Runs one of the operator's commands that change the data directory and end.
///
/// Nothing is served: no address is bound and no gateway started. The directory is locked as a start locks
/// it, so the command is refused while a server runs there, and what it asks goes through the engine, which
/// appends and syncs its record before it answers. `ask` has the engine do it and says in one sentence what
/// came of it; the sentence is the server's own line when the command was not refused.
fn alone(
    options: &Options,
    ask: impl FnOnce(&tokio::runtime::Runtime, &engine::Client) -> Result<String, String>,
) -> Result<(), String> {
    let _lock = lock(options)?;
    let started = open(options)?;
    // One question and its answer need no more than this thread.
    let said = tokio::runtime::Builder::new_current_thread()
        .build()
        .map_err(|error| format!("the runtime could not be started: {error}"))
        .and_then(|runtime| ask(&runtime, &started.client));
    if let Ok(sentence) = &said {
        note(sentence);
    }
    // The engine ends when the way to it has gone, and writes its snapshot as it does at every stop.
    drop(started.client);
    let _ = started.thread.join();
    said.map(|_| ())
}

/// Takes in a team that exists elsewhere, keeping its join secret, and ends.
fn take_in(import: &Import) -> Result<(), String> {
    // The secret is read before anything is made: a file that is missing or empty ends the command with the
    // directory as it was.
    let join_secret = join_secret(&import.join_secret_file)?;
    alone(&import.options, |runtime, engine| {
        // A refusal is the rules' own fixed text: it names what is wrong and holds nothing of the secret.
        let result = runtime
            .block_on(engine.import_team(&import.team_name, &join_secret))
            .map_err(|error| format!("the team was not taken in: {error}"))?;
        let team_ref = result["team_ref"].as_str().unwrap_or_default();
        let team_name = result["team_name"].as_str().unwrap_or_default();
        // The name is written as a quoted string, so that one that holds the end of a line writes no second
        // line.
        Ok(if result["imported"].as_bool() == Some(true) {
            format!("took in team {team_name:?} ({team_ref})")
        } else {
            format!("team {team_name:?} ({team_ref}) is already here under this join secret: nothing changed")
        })
    })
}

/// Names the owner of a team, the member that may rotate its join secret, revoke a member and transfer it,
/// and ends.
///
/// It is for whoever can run the server on its data directory, and no request can do it: over the network
/// only a team's owner names another. The engine refuses a team the server does not hold and a principal
/// that is not an active member of it.
fn name_owner(named: &SetOwner) -> Result<(), String> {
    alone(&named.options, |runtime, engine| {
        // A refusal is the rules' own fixed text, and repeats nothing the command line held.
        let result = runtime
            .block_on(engine.set_owner(&named.team_ref, &named.principal_ref))
            .map_err(|error| format!("the owner was not named: {error}"))?;
        let text = |member: &str| result[member].as_str().unwrap_or_default();
        let (team_ref, team_name, principal_ref) =
            (text("team_ref"), text("team_name"), text("principal_ref"));
        // The references are the server's own, read back from the team it found. The name and the label are
        // a member's words, and are written as quoted strings so that neither writes a second line.
        let owner = match text("human_label") {
            "" => principal_ref.to_owned(),
            human_label => format!("{principal_ref} ({human_label:?})"),
        };
        Ok(if result["changed"].as_bool() == Some(true) {
            format!("team {team_name:?} ({team_ref}) is now owned by {owner}")
        } else {
            format!("team {team_name:?} ({team_ref}) is already owned by {owner}: nothing changed")
        })
    })
}

/// Which of the operator's commands holds a place for a secret, in the words its refusals are told with.
#[derive(Clone, Copy)]
struct Asking {
    /// The option that names the file.
    option: &'static str,
    /// What was not done, when the place cannot be held.
    not_done: &'static str,
}

/// The new join secret of a team that is taken back.
const NEW_SECRET: Asking = Asking {
    option: "--secret-file",
    not_done: "the team was not taken back",
};

/// The ticket of a team's move.
const MOVE_TICKET: Asking = Asking {
    option: "--ticket-file",
    not_done: "the move was not opened",
};

/// The place of a secret that does not exist yet: the file the operator named for the new join secret of
/// a team that is taken back, or for the ticket of a team's move. It is held before the team is given the
/// secret, so that the secret is not made and then found to have nowhere to go.
///
/// What is held is a file beside the one that was named, which only its owner can read. The secret is
/// written to it, and it is moved to the name that was asked for once it is whole. A place that is let go
/// of without a secret leaves nothing behind.
struct SecretPlace {
    /// The file the operator named.
    named: PathBuf,
    /// The file beside it that the secret is written to first.
    unfinished: PathBuf,
    file: File,
}

impl SecretPlace {
    /// Holds the place for the file at `named`: an absolute path of a file that is not there, in a
    /// directory that is. Nothing that is there is ever written over.
    ///
    /// The sentences name the option and never the path: what was typed where a path belongs is not
    /// repeated.
    fn hold(named: &Path, asking: Asking) -> Result<Self, String> {
        let Asking { option, not_done } = asking;
        let refused = |why: &str| format!("{not_done}: {why}");
        if !named.is_absolute() {
            return Err(refused(&format!("{option} takes an absolute path")));
        }
        let (Some(directory), Some(name)) = (named.parent(), named.file_name()) else {
            return Err(refused(&format!("{option} names no file")));
        };
        if !directory.is_dir() {
            return Err(refused(&format!(
                "the directory of the file {option} names is not there"
            )));
        }
        // A link that leads nowhere is there as well: the secret would be written to where it points.
        if fs::symlink_metadata(named).is_ok() {
            return Err(refused(&format!(
                "the file {option} names is there already, and nothing is written over: name a file that \
                 is not there"
            )));
        }
        let unfinished = directory.join(format!(".{}.unfinished", name.to_string_lossy()));
        // What an earlier run that was cut short left is not a file anybody asked for.
        let _ = fs::remove_file(&unfinished);
        let file = OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&unfinished)
            .map_err(|error| {
                refused(&format!(
                    "no file can be made in the directory of the file {option} names: {}",
                    error.kind()
                ))
            })?;
        Ok(Self {
            named: named.to_path_buf(),
            unfinished,
            file,
        })
    }

    /// Writes the secret, and gives the file the name that was asked for once it is whole and on the disk.
    fn keep(mut self, join_secret: &str) -> std::io::Result<()> {
        self.file.write_all(format!("{join_secret}\n").as_bytes())?;
        self.file.sync_all()?;
        fs::rename(&self.unfinished, &self.named)?;
        // The rename is durable once the directory is.
        match self.named.parent() {
            Some(directory) => File::open(directory)?.sync_all(),
            None => Ok(()),
        }
    }
}

impl Drop for SecretPlace {
    fn drop(&mut self) {
        // After the file has its name there is nothing left to remove, and this fails without harm.
        let _ = fs::remove_file(&self.unfinished);
    }
}

/// Takes a team back from whoever holds it, and ends.
///
/// The team gets a new join secret, every membership ends and the team has no owner, in one entry of the
/// log. The secret goes to the file the operator named and to nothing else: no line of the server's holds
/// it. The first member to join with it over HTTP owns the team, so the operator hands the file to the
/// team's admin and to nobody before.
fn take_back(taken: &TakeBack) -> Result<(), String> {
    // The file's place is held before anything is changed: a path that cannot be written to ends the
    // command with the team as it was.
    let place = SecretPlace::hold(&taken.secret_file, NEW_SECRET)?;
    alone(&taken.options, |runtime, engine| {
        // A refusal is the rules' own fixed text, and repeats nothing the command line held.
        let result = runtime
            .block_on(engine.take_back(&taken.team_ref))
            .map_err(|error| format!("the team was not taken back: {error}"))?;
        let text = |member: &str| result[member].as_str().unwrap_or_default();
        let (team_ref, team_name) = (text("team_ref"), text("team_name"));
        // From here on the team has the new secret and no member. A secret that does not reach its file
        // is lost with this process: the hash is all the server keeps, so the command is run again.
        place.keep(text("join_secret")).map_err(|error| {
            format!(
                "team {team_name:?} ({team_ref}) was taken back, but its new join secret could not be \
                 written to the file --secret-file names ({}): the secret is lost, so run the command again",
                error.kind()
            )
        })?;
        let ended = match result["revoked"].as_u64().unwrap_or_default() {
            1 => "1 membership".to_owned(),
            count => format!("{count} memberships"),
        };
        // The name and the path are written as quoted strings, so that neither writes a second line.
        Ok(format!(
            "took back team {team_name:?} ({team_ref}): ended {ended}, and its new join secret is in {:?}",
            taken.secret_file
        ))
    })
}

/// Opens the move of a team's members from another backend, and ends.
///
/// The team gets a move ticket: a second join secret that lets a member in until it ends, and never makes
/// the team's owner. The ticket goes to the file the operator named and to nothing else: no line of the
/// server's holds it. The operator hands the file to the team's admin, who records the ticket on the
/// backend the team leaves; a member's session reads it there and joins this server with it.
fn open_move(opened: &OpenMove) -> Result<(), String> {
    // The file's place is held before anything is changed: a path that cannot be written to ends the
    // command with the team as it was.
    let place = SecretPlace::hold(&opened.ticket_file, MOVE_TICKET)?;
    alone(&opened.options, |runtime, engine| {
        // A refusal is the rules' own fixed text, and repeats nothing the command line held.
        let result = runtime
            .block_on(engine.open_move(&opened.team_ref, opened.days))
            .map_err(|error| format!("the move was not opened: {error}"))?;
        let text = |member: &str| result[member].as_str().unwrap_or_default();
        let (team_ref, team_name, ends_at) = (text("team_ref"), text("team_name"), text("ends_at"));
        // From here on the team has the ticket. One that does not reach its file is lost with this
        // process: the hash is all the server keeps, so the command is run again, and the ticket it then
        // makes takes the place of the lost one.
        place.keep(text("ticket")).map_err(|error| {
            format!(
                "the move of team {team_name:?} ({team_ref}) was opened, but its ticket could not be written \
                 to the file --ticket-file names ({}): the ticket is lost, so run the command again",
                error.kind()
            )
        })?;
        let before = if result["replaced"].as_bool() == Some(true) {
            ", and the ticket of the move it had admits nobody from now on"
        } else {
            ""
        };
        // The name and the path are written as quoted strings, so that neither writes a second line. The
        // end is the server's own text, in UTC.
        Ok(format!(
            "opened the move of team {team_name:?} ({team_ref}): its ticket is in {:?} and admits until \
             {ends_at}{before}",
            opened.ticket_file
        ))
    })
}

/// Closes the move of a team, and ends: its ticket admits nobody from then on, and every member that came
/// in with it stays one.
fn close_move(closed: &CloseMove) -> Result<(), String> {
    alone(&closed.options, |runtime, engine| {
        // A refusal is the rules' own fixed text, and repeats nothing the command line held.
        let result = runtime
            .block_on(engine.close_move(&closed.team_ref))
            .map_err(|error| format!("the move was not closed: {error}"))?;
        let text = |member: &str| result[member].as_str().unwrap_or_default();
        let (team_ref, team_name) = (text("team_ref"), text("team_name"));
        Ok(if result["closed"].as_bool() == Some(true) {
            format!(
                "closed the move of team {team_name:?} ({team_ref}): its ticket admits nobody from now on"
            )
        } else {
            format!("team {team_name:?} ({team_ref}) has no move: nothing changed")
        })
    })
}

fn run(options: &Options) -> Result<(), String> {
    let _lock = lock(options)?;

    let create_token = create_token(options)?;
    // The gateways are read before the log is opened: a configuration that does not read ends the start.
    let gateways = match &options.gateway_config {
        Some(path) => gateway::ready(gateway::config::read(path)?, &options.data)?,
        None => None,
    };
    let tls = match &options.tls {
        Some(Tls::Files(certificate, key)) => Some(tls::serving(certificate, key)?),
        Some(Tls::SelfSigned) => {
            let (certificate, key, made) = tls::own_certificate(&options.data)?;
            if made {
                note(&format!(
                    "made the server's own certificate, {}",
                    certificate.display()
                ));
            }
            Some(tls::serving(&certificate, &key)?)
        }
        None => None,
    };
    let started = open(options)?;

    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(|error| format!("the runtime could not be started: {error}"))?;
    let (stop, stopping) = watch::channel(false);
    let gateways = gateways.map(|ready| Arc::new(ready.on(started.client.clone())));
    let shared = Arc::new(http::Shared {
        engine: started.client,
        create_token,
        stopping,
        gateways,
    });
    let served = runtime.block_on(async {
        // Every gateway is a member with a session before anything can be asked of it: a request that came
        // before that could not be carried.
        let turns = match &shared.gateways {
            Some(gateways) => {
                let mut turns = gateway::turns::Turns::new(gateways);
                turns.take_places(gateways).await;
                Some(turns)
            }
            None => None,
        };
        let listener = TcpListener::bind(options.listen)
            .await
            .map_err(|error| format!("{} could not be listened on: {error}", options.listen))?;
        let address = listener
            .local_addr()
            .map_err(|error| format!("the listening address is not known: {error}"))?;
        let scheme = if tls.is_some() { "https" } else { "http" };
        let base = format!("{scheme}://{address}");
        if let Some(path) = &options.address_file {
            fs::write(path, format!("{base}\n"))
                .map_err(|error| format!("{} could not be written: {error}", path.display()))?;
        }
        let how = match (&tls, options.tls_in_front) {
            // What a team file pins when no authority signed the certificate.
            (Some(tls), _) => format!(" (certificate {})", tls.fingerprint),
            (None, true) => " (plain HTTP: TLS ends in front of the server)".to_owned(),
            (None, false) => " (plain HTTP on loopback)".to_owned(),
        };
        note(&format!("listening on {base}{how}"));

        let acceptor = tls.map(|tls| tls.acceptor);
        let serving = tokio::spawn(http::serve(listener, acceptor, Arc::clone(&shared), DRAIN));
        // The gateways' turns begin with the listener: each goes to work on what waits for it.
        let turning = shared.gateways.as_ref().zip(turns).map(|(gateways, turns)| {
            tokio::spawn(gateway::turns::run(
                Arc::clone(gateways),
                turns,
                shared.stopping.clone(),
            ))
        });
        let mut terminate =
            signal(SignalKind::terminate()).map_err(|error| format!("signals cannot be heard: {error}"))?;
        let mut interrupt =
            signal(SignalKind::interrupt()).map_err(|error| format!("signals cannot be heard: {error}"))?;
        tokio::select! {
            _ = terminate.recv() => {}
            _ = interrupt.recv() => {}
        }
        note("stopping");
        // No new connection is taken; the requests that are being answered are answered, a receive that
        // waits at once, and then the listener's task ends.
        let _ = stop.send(true);
        let _ = serving.await;
        // A gateway's turn ends after the message it is carrying; one that Slack or a mail provider holds
        // up for longer is dropped with the runtime, and what it had not acknowledged waits in its inbox.
        if let Some(turning) = turning {
            let _ = tokio::time::timeout(DRAIN, turning).await;
        }
        Ok::<(), String>(())
    });
    // The engine ends when the last holder of a way to it has gone: what is still running is dropped, and
    // the engine writes its last snapshot.
    drop(shared);
    runtime.shutdown_timeout(Duration::from_secs(1));
    let _ = started.thread.join();
    served
}

#[cfg(test)]
mod tests {
    use std::os::unix::fs::PermissionsExt;
    use std::path::{Path, PathBuf};

    use super::{Asking, MOVE_TICKET, NEW_SECRET, SecretPlace};

    fn scratch(test: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("brigade-secret-place-{test}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        dir
    }

    fn mode(path: &Path) -> u32 {
        std::fs::metadata(path).unwrap().permissions().mode() & 0o777
    }

    /// Why a place was not held for a team's new join secret, without the words every refusal begins
    /// with.
    fn refused(path: &Path) -> String {
        refused_for(path, NEW_SECRET)
    }

    /// Why a place was not held for `asking`, without the words every refusal of its command begins with.
    fn refused_for(path: &Path, asking: Asking) -> String {
        let Err(why) = SecretPlace::hold(path, asking) else {
            panic!("held a place at {}", path.display());
        };
        why.strip_prefix(&format!("{}: ", asking.not_done))
            .unwrap_or_else(|| panic!("{why}"))
            .to_owned()
    }

    #[test]
    fn a_secret_is_written_whole_to_a_file_of_its_owners_alone_and_over_nothing() {
        let dir = scratch("kept");
        let (named, unfinished) = (dir.join("ops.secret"), dir.join(".ops.secret.unfinished"));
        // What an earlier run that was cut short left behind is not in the way.
        std::fs::write(&unfinished, "half of something").unwrap();
        let place = SecretPlace::hold(&named, NEW_SECRET).unwrap();
        // The place is held beside the name, by a file only its owner reads, and the name is not there
        // until the secret is whole.
        assert!(!named.exists());
        assert_eq!(mode(&unfinished), 0o600);
        assert_eq!(std::fs::read_to_string(&unfinished).unwrap(), "");
        place.keep("the-secret-of-the-test").unwrap();
        assert_eq!(
            std::fs::read_to_string(&named).unwrap(),
            "the-secret-of-the-test\n"
        );
        assert_eq!(mode(&named), 0o600);
        assert!(!unfinished.exists());

        // A file that is there is never written over: not the one just written, and not a link that
        // leads nowhere, which a write would follow.
        let there = "the file --secret-file names is there already, and nothing is written over: name a file \
                     that is not there";
        assert_eq!(refused(&named), there);
        let link = dir.join("link");
        std::os::unix::fs::symlink(dir.join("nowhere"), &link).unwrap();
        assert_eq!(refused(&link), there);
        assert!(!dir.join("nowhere").exists());
        assert_eq!(
            std::fs::read_to_string(&named).unwrap(),
            "the-secret-of-the-test\n"
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_place_that_cannot_be_held_is_refused_by_the_options_name_and_never_by_the_path() {
        let dir = scratch("refused");
        for (path, why) in [
            (
                PathBuf::from("PATH-MUST-NOT-LEAK/ops.secret"),
                "--secret-file takes an absolute path",
            ),
            (
                dir.join("PATH-MUST-NOT-LEAK").join("ops.secret"),
                "the directory of the file --secret-file names is not there",
            ),
            (PathBuf::from("/"), "--secret-file names no file"),
        ] {
            let refusal = refused(&path);
            assert_eq!(refusal, why);
            assert!(!refusal.contains("MUST-NOT-LEAK"));
        }
        // A directory where the file is to be is there already, like any file.
        assert!(refused(&dir).contains("is there already"));
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn the_place_of_a_move_ticket_is_held_and_refused_in_its_own_commands_words() {
        let dir = scratch("ticket");
        let named = dir.join("ops.move-ticket");
        SecretPlace::hold(&named, MOVE_TICKET)
            .unwrap()
            .keep("the-ticket-of-the-test")
            .unwrap();
        assert_eq!(
            std::fs::read_to_string(&named).unwrap(),
            "the-ticket-of-the-test\n"
        );
        assert_eq!(mode(&named), 0o600);
        for (path, why) in [
            (
                PathBuf::from("PATH-MUST-NOT-LEAK/ops.move-ticket"),
                "--ticket-file takes an absolute path",
            ),
            (
                dir.join("PATH-MUST-NOT-LEAK").join("ops.move-ticket"),
                "the directory of the file --ticket-file names is not there",
            ),
            (PathBuf::from("/"), "--ticket-file names no file"),
            (
                named.clone(),
                "the file --ticket-file names is there already, and nothing is written over: name a file \
                 that is not there",
            ),
        ] {
            // The words are the command's own: a refusal of one command never names the other's option.
            let refusal = refused_for(&path, MOVE_TICKET);
            assert_eq!(refusal, why);
            assert!(!refusal.contains("MUST-NOT-LEAK") && !refusal.contains("--secret-file"));
        }
        assert_eq!(
            std::fs::read_to_string(&named).unwrap(),
            "the-ticket-of-the-test\n"
        );
        std::fs::remove_dir_all(dir).unwrap();
    }

    #[test]
    fn a_place_let_go_of_or_one_that_cannot_be_kept_leaves_nothing_behind() {
        let dir = scratch("let-go");
        let (named, unfinished) = (dir.join("ops.secret"), dir.join(".ops.secret.unfinished"));
        // The command was refused after the place was held: nothing was made.
        drop(SecretPlace::hold(&named, NEW_SECRET).unwrap());
        assert!(!named.exists() && !unfinished.exists());

        // The secret could not be given its name, here because a directory took the name meanwhile: the
        // failure is told, and the file that held the secret is gone.
        let place = SecretPlace::hold(&named, NEW_SECRET).unwrap();
        std::fs::create_dir(&named).unwrap();
        assert!(place.keep("the-secret-of-the-test").is_err());
        assert!(!unfinished.exists());
        assert!(named.is_dir());
        std::fs::remove_dir_all(dir).unwrap();
    }
}
