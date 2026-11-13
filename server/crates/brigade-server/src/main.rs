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
mod snapshot;
mod tls;

use std::fs::{self, DirBuilder, File};
use std::os::unix::fs::DirBuilderExt;
use std::path::Path;
use std::process::ExitCode;
use std::sync::Arc;
use std::time::Duration;

use tokio::net::TcpListener;
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::watch;

use crate::note::note;
use crate::options::{Asked, Import, LogKind, Options, Tls};

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
/// taken in beside a server that runs.
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

/// Takes in a team that exists elsewhere, keeping its join secret, and ends.
///
/// Nothing is served: no address is bound and no gateway started. The directory is locked as a start locks
/// it, so the command is refused while a server runs there, and the team goes through the engine, which
/// appends and syncs its record before it answers.
fn take_in(import: &Import) -> Result<(), String> {
    // The secret is read before anything is made: a file that is missing or empty ends the command with the
    // directory as it was.
    let join_secret = join_secret(&import.join_secret_file)?;
    let _lock = lock(&import.options)?;
    let started = open(&import.options)?;
    let taken = ask_to_take_in(&started.client, import, &join_secret);
    if let Ok(sentence) = &taken {
        note(sentence);
    }
    // The engine ends when the way to it has gone, and writes its snapshot as it does at every stop.
    drop(started.client);
    let _ = started.thread.join();
    taken.map(|_| ())
}

/// Has the engine take the team in, and says in one sentence what came of it.
fn ask_to_take_in(engine: &engine::Client, import: &Import, join_secret: &str) -> Result<String, String> {
    // One question and its answer need no more than this thread.
    let runtime = tokio::runtime::Builder::new_current_thread()
        .build()
        .map_err(|error| format!("the runtime could not be started: {error}"))?;
    // A refusal is the rules' own fixed text: it names what is wrong and holds nothing of the secret.
    let result = runtime
        .block_on(engine.import_team(&import.team_name, join_secret))
        .map_err(|error| format!("the team was not taken in: {error}"))?;
    let team_ref = result["team_ref"].as_str().unwrap_or_default();
    let team_name = result["team_name"].as_str().unwrap_or_default();
    // The name is written as a quoted string, so that one that holds the end of a line writes no second line.
    Ok(if result["imported"].as_bool() == Some(true) {
        format!("took in team {team_name:?} ({team_ref})")
    } else {
        format!("team {team_name:?} ({team_ref}) is already here under this join secret: nothing changed")
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
