//! The one thread that owns the state and the log.
//!
//! Every request is a job on this thread's queue. The thread answers the jobs in the order they arrive:
//! it runs each against the state, appends the records the batch made to the log, waits for the sync, and
//! only then sends the answers. So there is one order, the thread's own, and an answer is never ahead of the
//! disk. The jobs that are waiting when the thread looks share one sync.
//!
//! A team the operator takes in from another backend is a job too, of a kind of its own, and so are the
//! owner the operator names for a team and the team the operator takes back: each changes the state through
//! the same queue, the same append and the same sync, and is nothing a request can ask for.
//!
//! A failed append ends the process: the state already holds what the log does not, and nothing may be
//! answered from it.

use std::fs::{self, DirBuilder, File, OpenOptions};
use std::io::Write;
use std::os::unix::fs::{DirBuilderExt, OpenOptionsExt};
use std::path::{Path, PathBuf};
use std::thread::JoinHandle;

use brigade_core::hash::hex;
use brigade_core::record::LONGEST_ENCODED_BYTES;
use brigade_core::{
    Entropy, Entry, Error, Request, State, Timestamp, answer, import_team, set_owner, take_back,
};
use brigade_log::{FileLog, KEY_BYTES, Log, LogError, Position, Sealer};
use hyper::body::Bytes;
use ring::rand::{SecureRandom, SystemRandom};
use serde_json::Value;
use tokio::sync::{mpsc, oneshot, watch};

use crate::note::{self, note};
use crate::options::LogKind;
use crate::said;
use crate::snapshot;

/// How many jobs wait for the engine at most before a request is made to wait for room.
const QUEUE: usize = 1024;

/// How many jobs share one sync at most.
const BATCH: usize = 256;

/// What the engine is started with.
// A build without the Cavalry feed reads none of the feed's settings.
#[cfg_attr(not(feature = "cavalry"), allow(dead_code))]
pub struct Settings {
    /// The directory everything is kept in.
    pub data: PathBuf,
    /// The ordered log.
    pub log: LogKind,
    /// The UDP address of the Cavalry feed.
    pub feed_control: String,
    /// The UDP address the server reads its own feed on.
    pub feed_self: String,
    /// Cavalry's driver directory, when the operator chose one.
    pub driver_dir: Option<PathBuf>,
    /// Records between two snapshots.
    pub snapshot_every: u64,
    /// How many bytes a recording of the feed holds before it is rolled.
    pub feed_roll_bytes: u64,
}

/// What the engine is asked to do.
enum Work {
    /// To answer one request of the contract, as it arrived.
    Request {
        /// The SHA-256 of the credential the request presented, when it presented one.
        caller: Option<String>,
        /// Whether the request may create a team.
        may_create: bool,
        /// Whether the request's join may own a team that has no owner.
        may_own: bool,
        path: String,
        body: Bytes,
    },
    /// To take in a team that exists elsewhere, with the join secret it has there. It is a kind of its own
    /// and not a request, so no path of the contract reaches it: only the operator's command line asks it.
    Import {
        /// The name the team gets here.
        team_name: String,
        /// The team's whole join secret, as its file held it.
        join_secret: String,
    },
    /// To name the owner of a team. Like the import it is the operator's alone, and no path reaches it:
    /// over the network only a team's owner names another, by a transfer.
    SetOwner {
        /// The team.
        team_ref: String,
        /// The active member that is to own it.
        principal_ref: String,
    },
    /// To take a team back from whoever holds it: a new join secret, no member and no owner. It is the
    /// operator's alone, as the two before it are.
    TakeBack {
        /// The team.
        team_ref: String,
    },
}

/// One piece of work for the engine, and where its answer goes.
struct Job {
    work: Work,
    reply: oneshot::Sender<Result<Value, Error>>,
}

/// The way to the engine: from the HTTP side, from the gateways, and from the operator's commands.
#[derive(Clone)]
pub struct Client {
    jobs: mpsc::Sender<Job>,
    changes: watch::Receiver<u64>,
}

impl Client {
    /// Has the engine answer one request. The body goes to the engine as it arrived: nothing of it is read
    /// before the engine has looked at the credential.
    ///
    /// `may_create` says whether the request may create a team, and `may_own` whether its join may make
    /// the caller the owner of a team that has none. A request that came over HTTP may own; one a gateway
    /// makes inside the server never does.
    ///
    /// # Errors
    /// The failure the request is answered with; `unavailable` when the server is stopping.
    pub async fn ask(
        &self,
        caller: Option<String>,
        may_create: bool,
        may_own: bool,
        path: &str,
        body: Bytes,
    ) -> Result<Value, Error> {
        let request = Work::Request {
            caller,
            may_create,
            may_own,
            path: path.to_owned(),
            body,
        };
        self.hand(request).await
    }

    /// Has the engine take in a team that exists elsewhere, under `team_name`, keeping `join_secret`. The
    /// record it makes is appended and synced before the answer comes, as a request's is.
    ///
    /// # Errors
    /// The failure the rules refuse it with, which holds nothing of the secret; `unavailable` when the
    /// server is stopping.
    pub async fn import_team(&self, team_name: &str, join_secret: &str) -> Result<Value, Error> {
        let import = Work::Import {
            team_name: team_name.to_owned(),
            join_secret: join_secret.to_owned(),
        };
        self.hand(import).await
    }

    /// Has the engine name `principal_ref` the owner of the team `team_ref`. The record it makes is
    /// appended and synced before the answer comes, as a request's is.
    ///
    /// # Errors
    /// The failure the rules refuse it with: a team the server does not hold, or a principal that is not an
    /// active member of it; `unavailable` when the server is stopping.
    pub async fn set_owner(&self, team_ref: &str, principal_ref: &str) -> Result<Value, Error> {
        let named = Work::SetOwner {
            team_ref: team_ref.to_owned(),
            principal_ref: principal_ref.to_owned(),
        };
        self.hand(named).await
    }

    /// Has the engine take the team `team_ref` back: it gets a new join secret, every membership ends and
    /// it has no owner. The entry it makes is appended and synced before the answer comes. The answer's
    /// `join_secret` is the one place the new secret is, so the caller writes it where the operator said
    /// and nowhere else.
    ///
    /// # Errors
    /// The failure the rules refuse it with, a team the server does not hold; `unavailable` when the
    /// server is stopping.
    pub async fn take_back(&self, team_ref: &str) -> Result<Value, Error> {
        let taken = Work::TakeBack {
            team_ref: team_ref.to_owned(),
        };
        self.hand(taken).await
    }

    /// Puts one piece of work on the engine's queue and waits for its answer.
    async fn hand(&self, work: Work) -> Result<Value, Error> {
        let stopping = || Error::unavailable("the server is stopping");
        let (reply, answered) = oneshot::channel();
        self.jobs
            .send(Job { work, reply })
            .await
            .map_err(|_| stopping())?;
        answered.await.map_err(|_| stopping())?
    }

    /// A signal that moves every time the state has changed and the change is on the disk. A receive that
    /// waits listens to it.
    #[must_use]
    pub fn changes(&self) -> watch::Receiver<u64> {
        self.changes.clone()
    }
}

/// A running engine.
pub struct Started {
    /// The way to it.
    pub client: Client,
    /// Its thread, which ends once every client is gone.
    pub thread: JoinHandle<()>,
    /// How many records the start applied after the snapshot.
    pub replayed: u64,
}

/// Opens the log, reads the newest snapshot and the records after it, and starts the engine's thread.
///
/// # Errors
/// A sentence for the operator, when the server cannot start on this directory.
pub fn start(settings: Settings) -> Result<Started, String> {
    let (jobs, queue) = mpsc::channel(QUEUE);
    let (moved, changes) = watch::channel(0_u64);
    let (ready, opened) = std::sync::mpsc::channel();
    // The log is opened on the thread that will own it: a Cavalry client stays on the thread it was made on.
    let thread = std::thread::Builder::new()
        .name("engine".to_owned())
        .spawn(move || match Engine::open(&settings) {
            Ok(mut engine) => {
                let _ = ready.send(Ok(engine.replayed));
                engine.run(queue, &moved);
                engine.close();
            }
            Err(why) => {
                let _ = ready.send(Err(why));
            }
        })
        .map_err(|error| format!("the engine's thread could not be started: {error}"))?;
    let replayed = opened
        .recv()
        .map_err(|_| "the engine ended before it was ready".to_owned())??;
    Ok(Started {
        client: Client { jobs, changes },
        thread,
        replayed,
    })
}

/// The operating system's randomness, for every new identifier, credential and join secret.
struct SystemEntropy(SystemRandom);

impl Entropy for SystemEntropy {
    fn fill(&mut self, buffer: &mut [u8]) {
        if self.0.fill(buffer).is_err() {
            // An identifier that is not random would be guessable; there is nothing to answer with.
            note("the operating system gave no randomness; stopping");
            std::process::exit(1);
        }
    }
}

struct Engine {
    state: State,
    log: Box<dyn Log>,
    sealer: Sealer,
    entropy: SystemEntropy,
    snapshots: PathBuf,
    snapshot_every: u64,
    /// The position after the last record applied.
    position: Position,
    /// Records appended since the last snapshot.
    since_snapshot: u64,
    /// Entries the start applied after the snapshot.
    replayed: u64,
}

/// A directory only its owner can enter.
fn private_dir(path: &Path) -> Result<(), String> {
    DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
        .map_err(|error| format!("{} could not be created: {error}", path.display()))
}

/// The key every entry is sealed under: read from `record.key` in the data directory, and made there on
/// the first start. A second instance is given a copy of the file.
///
/// A key is made only in a directory that holds nothing of a server's yet. A directory with a log and no
/// key has lost it, and a new key would open nothing that is there.
fn record_key(data: &Path) -> Result<[u8; KEY_BYTES], String> {
    let path = data.join("record.key");
    let malformed = || {
        format!(
            "{} is not a record key: it holds {} hexadecimal digits",
            path.display(),
            KEY_BYTES * 2
        )
    };
    match fs::read_to_string(&path) {
        Ok(text) => {
            let digits = text.trim().as_bytes();
            if digits.len() != KEY_BYTES * 2 {
                return Err(malformed());
            }
            let mut key = [0_u8; KEY_BYTES];
            for (byte, pair) in key.iter_mut().zip(digits.chunks(2)) {
                let pair = std::str::from_utf8(pair).map_err(|_| malformed())?;
                *byte = u8::from_str_radix(pair, 16).map_err(|_| malformed())?;
            }
            Ok(key)
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            if ["snapshots", "feed", "log"]
                .iter()
                .any(|name| data.join(name).exists())
            {
                return Err(format!(
                    "{} holds a server's log and no record.key: put the key back, because nothing in the log \
                     can be read without it",
                    data.display()
                ));
            }
            let mut key = [0_u8; KEY_BYTES];
            SystemRandom::new()
                .fill(&mut key)
                .map_err(|_| "the operating system gave no randomness for the record key")?;
            // Written beside its name and moved into place, so a file with the key's name is always whole.
            let unfinished = data.join("record.key.unfinished");
            let failed = |error: std::io::Error| format!("{} could not be written: {error}", path.display());
            let mut file = OpenOptions::new()
                .write(true)
                .create(true)
                .truncate(true)
                .mode(0o600)
                .open(&unfinished)
                .map_err(failed)?;
            file.write_all(format!("{}\n", hex(&key)).as_bytes())
                .map_err(failed)?;
            file.sync_all().map_err(failed)?;
            drop(file);
            fs::rename(&unfinished, &path).map_err(failed)?;
            File::open(data)
                .and_then(|directory| directory.sync_all())
                .map_err(failed)?;
            Ok(key)
        }
        Err(error) => Err(format!("{} could not be read: {error}", path.display())),
    }
}

fn open_log(settings: &Settings) -> Result<Box<dyn Log>, String> {
    match settings.log {
        LogKind::File => {
            let dir = settings.data.join("log");
            private_dir(&dir)?;
            Ok(Box::new(
                FileLog::open(&dir.join("records.log")).map_err(|error| error.to_string())?,
            ))
        }
        LogKind::Cavalry => open_feed(settings),
    }
}

#[cfg(feature = "cavalry")]
fn open_feed(settings: &Settings) -> Result<Box<dyn Log>, String> {
    let log_dir = settings.data.join("feed");
    private_dir(&log_dir)?;
    let options = brigade_log::FeedOptions {
        log_dir,
        driver_dir: settings.driver_dir.clone(),
        data_dir: settings.data.clone(),
        control: settings.feed_control.clone(),
        own_endpoint: settings.feed_self.clone(),
        roll_bytes: settings.feed_roll_bytes,
    };
    Ok(Box::new(
        brigade_log::CavalryLog::open(&options).map_err(|error| error.to_string())?,
    ))
}

#[cfg(not(feature = "cavalry"))]
fn open_feed(_: &Settings) -> Result<Box<dyn Log>, String> {
    Err("this build has no Cavalry feed".to_owned())
}

impl Engine {
    fn open(settings: &Settings) -> Result<Self, String> {
        // The key first: it is what says whether this directory already holds a server's log.
        let key = record_key(&settings.data)?;
        let sealer = Sealer::new(&key).map_err(|error| error.to_string())?;
        let snapshots = settings.data.join("snapshots");
        private_dir(&snapshots)?;
        snapshot::tidy(&snapshots);
        let mut log = open_log(settings)?;
        // The arithmetic is checked on the running thing: an entry the log cannot take would end the process
        // in the middle of a request.
        let longest = LONGEST_ENCODED_BYTES + Sealer::overhead();
        if log.max_record_bytes() < longest {
            return Err(format!(
                "the log takes entries of {} bytes and the longest entry is {longest}",
                log.max_record_bytes()
            ));
        }
        let found = snapshot::newest(&snapshots, &mut |path, why| {
            note(&format!("the snapshot {} is passed over: {why}", path.display()));
        })?;
        let (from, mut state) = found.unwrap_or_default();
        let mut replayed = 0_u64;
        let end = log
            .read_from(from, &mut |sealed, _| {
                let entry = Entry::decode(&sealer.open(sealed)?).map_err(LogError::new)?;
                // An entry that is not the next one is a log read wrongly: refused here, never believed.
                state.replay(&entry).map_err(LogError::new)?;
                replayed += 1;
                Ok(())
            })
            .map_err(|error| {
                format!("the log could not be read from the snapshot's position, {from}: {error}")
            })?;
        let mut engine = Self {
            state,
            log,
            sealer,
            entropy: SystemEntropy(SystemRandom::new()),
            snapshots,
            snapshot_every: settings.snapshot_every,
            position: end,
            since_snapshot: replayed,
            replayed,
        };
        // The next start then reads only what comes after this one.
        if replayed > 0 {
            engine.snapshot();
            engine.release();
        }
        Ok(engine)
    }

    fn snapshot(&mut self) {
        // Whether or not it was written, the next one is due after as many entries again: a disk with no
        // room is not helped by a snapshot after every request.
        self.since_snapshot = 0;
        if let Err(why) = snapshot::write(&self.snapshots, self.position, &self.state) {
            // The log is the record of truth; a snapshot that was not written costs the next start time.
            note(&why);
        }
    }

    /// Lets the log go of what every snapshot the server keeps already stands for.
    fn release(&mut self) {
        let before = match snapshot::oldest(&self.snapshots) {
            Ok(Some(before)) => before,
            Ok(None) => return,
            Err(why) => {
                // Nothing is let go of this time; the next snapshot asks again.
                note(&why);
                return;
            }
        };
        if let Err(error) = self.log.release(before) {
            // The log could not go on after it began to let go, and nothing more can be appended to it.
            note(&format!(
                "the log could not be shortened, and the server stops: {error}"
            ));
            std::process::exit(1);
        }
        for sentence in self.log.notes() {
            note(&sentence);
        }
    }

    /// The time to stamp a request with: the machine's clock, and never earlier than a record before it,
    /// whatever the clock did while the server ran or was down.
    fn now(&self) -> Timestamp {
        self.state.latest_at.max(note::now())
    }

    fn run(&mut self, mut queue: mpsc::Receiver<Job>, moved: &watch::Sender<u64>) {
        while let Some(first) = queue.blocking_recv() {
            let mut batch = vec![first];
            while batch.len() < BATCH
                && let Ok(job) = queue.try_recv()
            {
                batch.push(job);
            }
            let mut sealed = Vec::new();
            let mut lines = Vec::new();
            let mut answers = Vec::with_capacity(batch.len());
            for job in &batch {
                let now = self.now();
                let outcome = match &job.work {
                    Work::Request {
                        caller,
                        may_create,
                        may_own,
                        path,
                        body,
                    } => {
                        let request = Request {
                            caller: caller.as_deref(),
                            may_create: *may_create,
                            may_own: *may_own,
                            path,
                            body,
                        };
                        answer(&mut self.state, request, now, &mut self.entropy)
                    }
                    Work::Import {
                        team_name,
                        join_secret,
                    } => import_team(&mut self.state, team_name, join_secret, now),
                    Work::SetOwner {
                        team_ref,
                        principal_ref,
                    } => set_owner(&mut self.state, team_ref, principal_ref, now),
                    Work::TakeBack { team_ref } => {
                        take_back(&mut self.state, team_ref, now, &mut self.entropy)
                    }
                };
                // A request that may make its caller an owner came over HTTP and is a member's own. What it
                // changed about a team is said once the log holds it. A gateway's turns say what the
                // gateway did, and an operator's command prints its own line.
                let members_own = matches!(&job.work, Work::Request { may_own: true, .. });
                answers.push(outcome.map(|outcome| {
                    // The records of one request are one entry: the log holds all of them or none.
                    if let Some(entry) = &outcome.entry {
                        sealed.push(self.sealer.seal(&entry.encode()));
                        if members_own {
                            lines.extend(said::of_entry(&self.state, entry));
                        }
                    }
                    outcome.result
                }));
            }
            if !sealed.is_empty() {
                let appended = sealed
                    .into_iter()
                    .collect::<Result<Vec<_>, _>>()
                    .and_then(|sealed| {
                        let count = sealed.len() as u64;
                        self.log.append(&sealed).map(|position| (position, count))
                    });
                match appended {
                    Ok((position, count)) => {
                        self.position = position;
                        self.since_snapshot += count;
                    }
                    Err(error) => {
                        // The state holds what the log does not. Nothing may be answered from it.
                        note(&format!("an append failed, and the server stops: {error}"));
                        std::process::exit(1);
                    }
                }
                for sentence in self.log.notes() {
                    note(&sentence);
                }
                for line in &lines {
                    note(line);
                }
                moved.send_modify(|version| *version += 1);
            }
            for (job, answer) in batch.into_iter().zip(answers) {
                // A caller that has gone has no use for the answer.
                let _ = job.reply.send(answer);
            }
            if self.since_snapshot >= self.snapshot_every {
                self.snapshot();
                self.release();
            }
        }
    }

    fn close(mut self) {
        if self.since_snapshot > 0 {
            self.snapshot();
        }
        note(&format!("stopped at position {}", self.position));
    }
}
