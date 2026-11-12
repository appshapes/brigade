//! `brigade-server`: a Brigade backend that keeps a team on the file system.
//!
//! The server implements the backend REST contract (`docs/backend-rest-v1.md`), which the `rest` adapter
//! bundled in the `brigade` binary speaks. It is one process: the rules of `brigade-core`, the ordered log
//! of `brigade-log`, and this crate's HTTP layer, snapshots and wiring. The plan is
//! `.context/plans/brigade-server.md` (work item 89).

mod engine;
mod http;
mod note;
mod options;
mod snapshot;
mod tls;

use std::fs::{self, DirBuilder, File};
use std::os::unix::fs::DirBuilderExt;
use std::process::ExitCode;
use std::sync::Arc;
use std::time::Duration;

use tokio::net::TcpListener;
use tokio::signal::unix::{SignalKind, signal};
use tokio::sync::watch;

use crate::note::note;
use crate::options::{Asked, LogKind, Options};

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
        Ok(Asked::Run(options)) => match run(&options) {
            Ok(()) => ExitCode::SUCCESS,
            Err(why) => {
                note(&why);
                ExitCode::FAILURE
            }
        },
        Err(why) => {
            eprintln!("brigade-server: {why}\n\n{}", options::USAGE);
            ExitCode::from(2)
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

fn run(options: &Options) -> Result<(), String> {
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(&options.data)
        .map_err(|error| format!("{} could not be created: {error}", options.data.display()))?;
    // One server on one directory: a second would write a second order into the same log.
    let lock = File::create(options.data.join("lock"))
        .map_err(|error| format!("{} could not be locked: {error}", options.data.display()))?;
    lock.try_lock()
        .map_err(|_| format!("another server is running on {}", options.data.display()))?;

    let create_token = create_token(options)?;
    let tls = match &options.tls {
        Some((certificate, key)) => Some(tls::acceptor(certificate, key)?),
        None => None,
    };
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

    let runtime = tokio::runtime::Builder::new_multi_thread()
        .enable_all()
        .build()
        .map_err(|error| format!("the runtime could not be started: {error}"))?;
    let (stop, stopping) = watch::channel(false);
    let shared = Arc::new(http::Shared {
        engine: started.client,
        create_token,
        stopping,
    });
    let served = runtime.block_on(async {
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
            (Some(_), _) => "",
            (None, true) => " (plain HTTP: TLS ends in front of the server)",
            (None, false) => " (plain HTTP on loopback)",
        };
        note(&format!("listening on {base}{how}"));

        let serving = tokio::spawn(http::serve(listener, tls, Arc::clone(&shared), DRAIN));
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
        Ok::<(), String>(())
    });
    // The engine ends when the last holder of a way to it has gone: what is still running is dropped, and
    // the engine writes its last snapshot.
    drop(shared);
    runtime.shutdown_timeout(Duration::from_secs(1));
    let _ = started.thread.join();
    served
}
