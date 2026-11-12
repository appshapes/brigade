//! The log as a Cavalry feed: one recorded publication, in a driver the server embeds.
//!
//! A record is one message of the feed. An append offers the records, waits until the recorder has written
//! them, asks for a sync and returns when the sync has: what the server then answers is on the disk. A
//! position is the feed's own, the byte after a record's last frame.
//!
//! Every run of the server is a new recording, because Cavalry extends none. The feed is still one line of
//! positions: each run's publication is created where the last recording stopped, under initial term id 0.
//! A restart reads the earlier recordings back in order, each through a replay over `cavalry:ipc`.
//!
//! The pattern is the one the Cavalry session measured on 2026-11-11 and 2026-11-12
//! (`../cavalry/.ignored/cavalry-adapter/answers.md`, sections C, F and G).

use std::os::unix::fs::{DirBuilderExt, MetadataExt};
use std::path::{Path, PathBuf};
use std::time::{Duration, Instant};

use cavalry::{
    Cavalry, Driver, Flags, Image, NULL_LENGTH, Offer, Publication, Recording, ReplayStopReason, Subscription,
};
use cavalry_log::catalog::CatalogReader;
use cavalry_log::layout::State;
use ring::digest::{SHA256, digest};

use crate::{Each, Log, LogError, Position};

/// The stream the feed is published on.
const FEED_STREAM: i32 = 10;

/// The stream a restart replays the feed's recordings on.
const REPLAY_STREAM: i32 = 11;

/// The channel a restart replays on: inside the one driver, with no socket.
const REPLAY_CHANNEL: &str = "cavalry:ipc";

/// What a driver directory holds before any log: its counters, its command ring and its broadcast buffer.
const DRIVER_FILES_BYTES: u64 = 8 << 20;

/// How long an append, a connection or a quiet replay is waited for.
const PATIENCE: Duration = Duration::from_secs(15);

/// How the feed is shaped by the Cavalry commit this crate is pinned to.
///
/// At `620a2e2` a publication with no connected receiver refuses every offer, a recording does not count as
/// one, and a channel names no term length. So the server subscribes to its own feed, and a log is three
/// terms of 16 MiB. Cavalry's cards 271 and 272 change both (its ADRs 0098 and 0099, built on branches on
/// 2026-11-12): with a pin that has them, `channel_keys` becomes `|ssc=true|term-length=1m`, `term_length`
/// 1 MiB and `own_subscription` false, and nothing else in this file changes. A channel that names either
/// key is refused by a driver without it, so the switch is made here and not at run time.
struct Shape {
    /// What the feed's channel adds after its control address.
    channel_keys: &'static str,
    /// The term length the position keys count in, in bytes.
    term_length: u64,
    /// Whether the server reads its own feed so that the feed takes appends.
    own_subscription: bool,
}

const SHAPE: Shape = Shape {
    channel_keys: "",
    term_length: 16 << 20,
    own_subscription: true,
};

/// What the feed is opened with.
pub struct FeedOptions {
    /// Cavalry's log directory: the recordings and their catalog. On the host's disk.
    pub log_dir: PathBuf,
    /// Cavalry's driver directory, when the operator chose one.
    pub driver_dir: Option<PathBuf>,
    /// The server's data directory, which names the driver directory when the operator chose none.
    pub data_dir: PathBuf,
    /// The UDP address of the feed: what a standby will be told to follow.
    pub control: String,
    /// The UDP address the server reads its own feed on, while the feed needs a reader to take appends.
    pub own_endpoint: String,
    /// How many bytes a recording holds before the feed is rolled into a new one, when a snapshot is taken.
    /// A recording that is live cannot be let go of, so a feed that is never rolled is never shorter.
    pub roll_bytes: u64,
}

/// One recording of the feed, from the catalog.
struct Recorded {
    id: i64,
    start: i64,
    /// Where it stopped; `None` while it is live.
    stop: Option<i64>,
}

/// The feed's publication, its recording and what keeps it connected. All of it belongs to one client and
/// goes when that client does.
struct Feed {
    publication: Publication,
    recording: Recording,
    own: Option<Subscription>,
}

/// The log as a Cavalry feed.
pub struct CavalryLog {
    // Dropped in this order: the feed before its client, the client before its driver.
    feed: Option<Feed>,
    client: Option<Cavalry>,
    driver: Option<Driver>,
    log_dir: PathBuf,
    control: String,
    own_endpoint: String,
    /// Where this run's recording began: the end of everything the earlier recordings hold.
    begin: i64,
    /// The position after the last record appended.
    end: i64,
    max_record_bytes: usize,
    roll_bytes: u64,
    /// What the operator should be told, until the caller asks.
    notes: Vec<String>,
}

fn failed(doing: &str, error: impl std::fmt::Display) -> LogError {
    LogError::new(format!("{doing}: {error}"))
}

/// The driver directory for a data directory the operator gave none for: on `/dev/shm` where there is one,
/// which is memory and what Cavalry is built for, else beside the data. Its name carries a hash of the data
/// directory, so two servers on one host do not meet.
fn default_driver_dir(data_dir: &Path) -> PathBuf {
    let shared_memory = Path::new("/dev/shm");
    if !shared_memory.is_dir() {
        return data_dir.join("driver");
    }
    let absolute = std::fs::canonicalize(data_dir).unwrap_or_else(|_| data_dir.to_path_buf());
    let hash = digest(&SHA256, absolute.as_os_str().as_encoded_bytes());
    let name: String = hash.as_ref()[..6]
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect();
    shared_memory.join(format!("brigade-server-{name}"))
}

/// Makes the driver directory this user's own, or refuses one that is not.
///
/// The default is a name on `/dev/shm`, where any user of the host can make a file. A directory or a link
/// someone else put under that name would have the driver write where they chose, so a name that exists must
/// be a real directory that this user owns and only this user can write to, and a name that does not is made
/// here, private, before the driver sees it.
fn claim(driver_dir: &Path) -> Result<(), LogError> {
    let private = |recursive: bool, path: &Path| {
        std::fs::DirBuilder::new()
            .recursive(recursive)
            .mode(0o700)
            .create(path)
    };
    if let Some(parent) = driver_dir.parent() {
        private(true, parent).map_err(|error| failed("making the driver directory's parent", error))?;
    }
    // Made, not looked for and then made: a name that someone put there in between is found below, not
    // accepted. Making a directory never follows a link at its own name.
    match private(false, driver_dir) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => {
            let found = std::fs::symlink_metadata(driver_dir)
                .map_err(|error| failed("looking at the driver directory", error))?;
            let own = found.file_type().is_dir()
                && found.uid() == nix::unistd::Uid::effective().as_raw()
                && found.mode() & 0o022 == 0;
            if own {
                Ok(())
            } else {
                Err(LogError::new(format!(
                    "{} is not a directory that this user owns and only it can write to: remove it, or name \
                     another with --driver-dir",
                    driver_dir.display()
                )))
            }
        }
        Err(error) => Err(failed("making the driver directory", error)),
    }
}

/// The bytes the files under `dir` take on their file system; zero for a directory that is not there.
fn taken(dir: &Path) -> u64 {
    let Ok(entries) = std::fs::read_dir(dir) else {
        return 0;
    };
    entries
        .flatten()
        .map(|entry| match entry.metadata() {
            Ok(metadata) if metadata.is_dir() => taken(&entry.path()),
            Ok(metadata) => metadata.blocks() * 512,
            Err(_) => 0,
        })
        .sum()
}

/// Refuses a driver directory whose file system has no room for the feed's logs.
///
/// Every page of a log is real memory from the moment the log is created, and a driver that cannot create
/// one starts all the same: the feed is added and every append is refused `not-connected`, with nothing in
/// the client's answer to say why. A container's default `/dev/shm` of 64 MiB is such a place. So the room
/// is checked here and the refusal names the number.
// The two fields are 64 bits wide on Linux and narrower on macOS, so the conversions are needed on one and
// idle on the other.
#[allow(clippy::useless_conversion)]
fn check_room(driver_dir: &Path) -> Result<(), LogError> {
    // A log is three terms and a page of metadata. The feed holds its publication and, while it reads
    // itself, an image; a restart's replay holds two more for a moment; twice that leaves room for the ones
    // that linger while they are replaced (the Cavalry session's rule, its section F4).
    let needed = DRIVER_FILES_BYTES + 4 * (3 * SHAPE.term_length + 4096);
    let mut existing = driver_dir;
    while !existing.exists() {
        existing = existing
            .parent()
            .ok_or_else(|| LogError::new("the driver directory has no parent that exists"))?;
    }
    let stat = nix::sys::statvfs::statvfs(existing)
        .map_err(|error| failed("reading the room of the driver directory's file system", error))?;
    // What an earlier run left in the directory is replaced, so it counts as room.
    let room = u64::from(stat.blocks_available()) * u64::from(stat.fragment_size()) + taken(driver_dir);
    if room < needed {
        return Err(LogError::new(format!(
            "the file system of the driver directory {} has room for {} MiB and the feed needs {} MiB: \
             give the container more shared memory (docker run --shm-size, or an emptyDir with medium Memory \
             mounted at /dev/shm), or name another directory with --driver-dir",
            driver_dir.display(),
            room >> 20,
            needed.div_ceil(1 << 20),
        )));
    }
    Ok(())
}

/// The recordings of the feed the catalog holds as valid, in the order of their positions.
fn recordings(log_dir: &Path) -> Result<Vec<Recorded>, LogError> {
    let doing = "reading the feed's catalog";
    let catalog = CatalogReader::open(log_dir).map_err(|error| failed(doing, error))?;
    let slots = catalog.slots().map_err(|error| failed(doing, error))?;
    let mut found = Vec::new();
    for id in 0..i64::try_from(slots).unwrap_or(i64::MAX) {
        if let Some(descriptor) = catalog.get(id).map_err(|error| failed(doing, error))?
            && descriptor.state() == State::Valid
            && descriptor.stream_id() == FEED_STREAM
        {
            let stop = (!descriptor.is_live()).then_some(descriptor.stop_position());
            found.push(Recorded {
                id,
                start: descriptor.start_position(),
                stop,
            });
        }
    }
    found.sort_by_key(|recorded| (recorded.start, recorded.id));
    Ok(found)
}

/// Reads the server's own subscription to its feed, and throws what it reads away. A receiver that never
/// reads holds its sender at the window.
fn drain(feed: &mut Feed) {
    if let Some(own) = &mut feed.own {
        while matches!(own.poll(|_, _| {}, 64), Ok(read) if read > 0) {}
    }
}

impl CavalryLog {
    /// Starts the driver, connects to it and adds the feed where its last recording stopped.
    ///
    /// # Errors
    /// A sentence for the operator, when the driver cannot start on these directories or the feed does not
    /// connect.
    pub fn open(options: &FeedOptions) -> Result<Self, LogError> {
        let driver_dir = options
            .driver_dir
            .clone()
            .unwrap_or_else(|| default_driver_dir(&options.data_dir));
        claim(&driver_dir)?;
        check_room(&driver_dir)?;
        let driver = Driver::start(&driver_dir, &options.log_dir)
            .map_err(|error| failed("starting the Cavalry driver", error))?;
        let mut client = Cavalry::connect_to(&driver_dir)
            .map_err(|error| failed("connecting to the Cavalry driver", error))?;
        let (feed, begin) = add_feed(
            &mut client,
            &options.log_dir,
            &options.control,
            &options.own_endpoint,
        )?;
        let max_record_bytes = feed.publication.max_message_length();
        Ok(Self {
            feed: Some(feed),
            client: Some(client),
            driver: Some(driver),
            log_dir: options.log_dir.clone(),
            control: options.control.clone(),
            own_endpoint: options.own_endpoint.clone(),
            begin,
            end: begin,
            max_record_bytes,
            roll_bytes: options.roll_bytes,
            notes: Vec::new(),
        })
    }

    /// A new client of the driver this process still runs, and the feed again where it stopped.
    ///
    /// A process that was held still for longer than Cavalry's liveness timeouts, as a virtual machine is
    /// while it is moved, comes back with its client closed. The driver goes on; everything the client held
    /// is gone with it. Returns where the feed stopped.
    fn reconnect(&mut self) -> Result<i64, LogError> {
        self.feed = None;
        self.client = None;
        let driver_dir = self.driver.as_ref().map(|driver| driver.dir().to_path_buf());
        let driver_dir = driver_dir.ok_or_else(|| LogError::new("the Cavalry driver is gone"))?;
        let mut client = Cavalry::connect_to(&driver_dir)
            .map_err(|error| failed("connecting to the Cavalry driver again", error))?;
        let (feed, stopped_at) = add_feed(&mut client, &self.log_dir, &self.control, &self.own_endpoint)?;
        self.feed = Some(feed);
        self.client = Some(client);
        Ok(stopped_at)
    }

    /// Ends the feed's recording and begins the next one where it stopped, with no restart: stop the
    /// recording, close the publication, and add the feed again at the stop position. The order matters. A
    /// stop cuts the recording at what the recorder has written, and every append waited for the recorder,
    /// so that is everything published.
    fn roll(&mut self) -> Result<(), LogError> {
        let doing = "rolling the feed's recording";
        let (Some(feed), Some(client)) = (self.feed.take(), &mut self.client) else {
            return Err(LogError::new("the feed is closed"));
        };
        feed.recording
            .stop(client)
            .map_err(|error| failed(doing, error))?;
        let deadline = Instant::now() + PATIENCE;
        while feed.recording.stop_position().is_none() {
            if Instant::now() > deadline {
                return Err(LogError::new(
                    "the feed's recording did not report its stop in time",
                ));
            }
            std::thread::sleep(Duration::from_micros(200));
        }
        if let Some(own) = feed.own {
            // The destination belongs to the send endpoint, which outlives the publication for a moment:
            // taken off here, it is added again with the new publication like the first time.
            let endpoint = format!("cavalry:udp?endpoint={}", self.own_endpoint);
            let _ = client.remove_destination(feed.publication.registration_id(), &endpoint);
            let _ = own.close();
        }
        feed.publication.close().map_err(|error| failed(doing, error))?;
        let (feed, begin) = add_feed(client, &self.log_dir, &self.control, &self.own_endpoint)?;
        self.feed = Some(feed);
        if begin != self.end {
            return Err(LogError::new(format!(
                "the feed's recording stopped at {begin} and its last record ends at {}",
                self.end
            )));
        }
        self.begin = begin;
        Ok(())
    }

    /// Offers the records from `next` on and waits until they are synced. Returns the position after each
    /// record that was offered, so that after a reconnect the caller can tell which of them the feed holds.
    fn offer_and_sync(&mut self, records: &[Vec<u8>], offered: &mut Vec<i64>) -> Result<(), LogError> {
        let (Some(feed), Some(client)) = (&mut self.feed, &mut self.client) else {
            return Err(LogError::new("the feed is closed"));
        };
        let deadline = Instant::now() + PATIENCE;
        for record in &records[offered.len()..] {
            loop {
                match feed.publication.offer(record) {
                    Ok(after) => {
                        offered.push(after);
                        break;
                    }
                    // Not connected for the moment a recording takes to go live, back-pressured while the
                    // recorder has a window to write, or the end of a term: each passes.
                    Err(Offer::NotConnected | Offer::BackPressured | Offer::AdminAction)
                        if Instant::now() < deadline =>
                    {
                        drain(feed);
                        std::thread::sleep(Duration::from_micros(200));
                    }
                    Err(refusal) => return Err(failed("the feed refused a record", refusal.reason())),
                }
            }
            drain(feed);
        }
        let end = offered.last().copied().unwrap_or(self.end);
        // The recorder is a thread of the driver's: once it has written up to the last record, one sync
        // covers the batch.
        while feed.recording.position() < end {
            if client.is_closed() {
                return Err(LogError::new("closed"));
            }
            if Instant::now() > deadline {
                return Err(LogError::new(
                    "the feed's recorder did not reach the records in time",
                ));
            }
            drain(feed);
            std::thread::sleep(Duration::from_micros(50));
        }
        feed.recording
            .sync(client)
            .map_err(|error| failed("syncing the feed's recording", error))?;
        if feed.recording.synced_position() < end {
            return Err(LogError::new(
                "the feed's recording was synced short of the records",
            ));
        }
        drain(feed);
        self.end = end;
        Ok(())
    }

    /// Plays one recording from `from` to its stop into `each`, and returns the position it ended at.
    fn play(
        client: &mut Cavalry,
        subscription: &mut Subscription,
        recorded: &Recorded,
        from: i64,
        stop: i64,
        each: &mut Each<'_>,
    ) -> Result<i64, LogError> {
        let doing = "reading the feed back";
        let replay = client
            .start_replay(recorded.id, from, NULL_LENGTH, REPLAY_STREAM, REPLAY_CHANNEL)
            .map_err(|error| failed(doing, error))?;
        let session = replay.session_id();
        let (mut record, mut whole) = (Vec::new(), false);
        let mut refused: Option<LogError> = None;
        let mut arithmetic = None;
        let mut heard = Instant::now();
        // The position after the last frame this reader was handed.
        let mut delivered = from;
        loop {
            // The images first, so that the replay's position arithmetic is in hand before its frames.
            subscription
                .poll(|_, _| {}, 0)
                .map_err(|error| failed(doing, error))?;
            let image = subscription
                .images()
                .iter()
                .find(|image| image.session_id() == session);
            if arithmetic.is_none() {
                arithmetic = image.map(Image::positions);
            }
            let (reached, in_sight) = (image.map(Image::position), image.is_some());
            let mut read = 0;
            if let Some(positions) = &arithmetic {
                read = subscription
                    .poll(
                        |body, header| {
                            if header.session_id() != session || refused.is_some() {
                                return;
                            }
                            // From the image's arithmetic and not a sum of lengths: a padding frame at the
                            // end of a term moves the position and reaches no handler.
                            let offset = u32::try_from(header.term_offset()).unwrap_or(0);
                            let after = positions.position(header.term_id(), offset)
                                + i64::try_from(header.aligned_length()).unwrap_or(0);
                            delivered = after;
                            // The frames of one record are consecutive: the first says so, and the last.
                            // A record whose last frame never came, as a kill in the middle of an offer
                            // leaves, is dropped when the next one begins.
                            if header.flags().contains(Flags::BEGIN) {
                                record.clear();
                                whole = true;
                            }
                            if !whole {
                                return;
                            }
                            record.extend_from_slice(body);
                            if header.flags().contains(Flags::END) {
                                whole = false;
                                if let Err(error) = each(&record, Position::try_from(after).unwrap_or(0)) {
                                    refused = Some(error);
                                }
                            }
                        },
                        64,
                    )
                    .map_err(|error| failed(doing, error))?;
            }
            if let Some(error) = refused {
                return Err(error);
            }
            if read > 0 {
                heard = Instant::now();
                continue;
            }
            if let Some((end, reason)) = replay.ended() {
                // The replay is read to its end when the last frame handed over ends there, or the image
                // stands there: the image's own position counts the padding that reaches no handler.
                // Nothing else is the end. An image that is not in sight yet has delivered nothing, and
                // one that has gone may have gone unread: the driver lets go of a replay's image a few
                // seconds after the replay stops, whether or not its subscriber read it.
                if delivered >= end || reached.is_some_and(|reached| reached >= end) {
                    return match reason {
                        Some(ReplayStopReason::Ended) | None if end >= stop => Ok(end),
                        _ => Err(LogError::new(format!(
                            "the replay of recording {} ended at position {end}, before the recording's stop at {stop}",
                            recorded.id
                        ))),
                    };
                }
                if arithmetic.is_some() && !in_sight {
                    return Err(LogError::new(format!(
                        "the replay of recording {} was let go of at position {delivered}, before it was read to \
                         its end at {end}: start the server again",
                        recorded.id
                    )));
                }
            }
            if heard.elapsed() > PATIENCE {
                return Err(LogError::new(format!(
                    "the replay of recording {} stopped arriving",
                    recorded.id
                )));
            }
            std::thread::sleep(Duration::from_micros(200));
        }
    }
}

/// Adds the feed's publication where its last recording stopped, records it, and connects it. Returns the
/// feed and the position it begins at.
fn add_feed(
    client: &mut Cavalry,
    log_dir: &Path,
    control: &str,
    own_endpoint: &str,
) -> Result<(Feed, i64), LogError> {
    let doing = "adding the feed";
    // A recording that is still live is an earlier client's, which the driver has not timed out yet. It is
    // stopped first: read before that, the stop is too low, and the new publication would repeat positions
    // the old recording holds.
    for recorded in recordings(log_dir)? {
        if recorded.stop.is_none() {
            client
                .stop_recording(recorded.id)
                .map_err(|error| failed("stopping an earlier recording of the feed", error))?;
        }
    }
    let begin = recordings(log_dir)?
        .iter()
        .filter_map(|recorded| recorded.stop)
        .max()
        .unwrap_or(0);
    let term = i64::try_from(SHAPE.term_length).unwrap_or(i64::MAX);
    let channel = format!(
        "cavalry:udp?control={control}|control-mode=manual{}|init-term-id=0|term-id={}|term-offset={}",
        SHAPE.channel_keys,
        begin / term,
        begin % term
    );
    let publication = client
        .add_exclusive_publication(&channel, FEED_STREAM)
        .map_err(|error| failed(doing, error))?;
    let recording = client
        .start_recording(&channel, FEED_STREAM)
        .map_err(|error| failed(doing, error))?;
    let own = if SHAPE.own_subscription {
        let endpoint = format!("cavalry:udp?endpoint={own_endpoint}");
        let own = client
            .add_subscription(&endpoint, FEED_STREAM)
            .map_err(|error| failed(doing, error))?;
        client
            .add_destination(publication.registration_id(), &endpoint)
            .map_err(|error| failed(doing, error))?;
        Some(own)
    } else {
        None
    };
    let mut feed = Feed {
        publication,
        recording,
        own,
    };
    let deadline = Instant::now() + PATIENCE;
    while !feed.publication.is_connected() {
        if Instant::now() > deadline {
            return Err(LogError::new(format!(
                "the feed on {control} did not connect: check that nothing else uses that address or {own_endpoint}, \
                 and that the driver directory's file system has room (cavalry doctor names the cause)"
            )));
        }
        drain(&mut feed);
        std::thread::sleep(Duration::from_millis(1));
    }
    Ok((feed, begin))
}

impl Log for CavalryLog {
    fn append(&mut self, records: &[Vec<u8>]) -> Result<Position, LogError> {
        let mut offered = Vec::with_capacity(records.len());
        let mut attempts = 0;
        // Everything up to here was synced and answered.
        let answered = self.end;
        loop {
            match self.offer_and_sync(records, &mut offered) {
                Ok(()) => return Ok(Position::try_from(self.end).unwrap_or(0)),
                Err(error) => {
                    let closed = self.client.as_ref().is_none_or(Cavalry::is_closed);
                    attempts += 1;
                    if !closed || attempts > 3 {
                        return Err(error);
                    }
                    // The client was closed under the append. The feed stopped where its recording did:
                    // the records offered at or before that are in it, and the rest are offered again.
                    let stopped_at = self.reconnect()?;
                    if stopped_at < answered {
                        // The catalog holds no stop for the recording, as when its own stop could not be
                        // written: a feed that went on from here would use positions of answered records.
                        return Err(LogError::new(format!(
                            "the feed stopped at position {stopped_at}, before records that were answered up to \
                             {answered}"
                        )));
                    }
                    offered.retain(|after| *after <= stopped_at);
                    self.end = stopped_at;
                    self.notes.push(format!(
                        "the Cavalry client was closed under an append; connected again, and the feed goes on \
                         at position {stopped_at}"
                    ));
                }
            }
        }
    }

    fn read_from(&mut self, from: Position, each: &mut Each<'_>) -> Result<Position, LogError> {
        let mut position =
            i64::try_from(from).map_err(|_| LogError::new("a position past the feed's range"))?;
        if position > self.begin {
            return Err(LogError::new(format!(
                "position {position} is past the feed's end, {}: the snapshot is of another feed",
                self.begin
            )));
        }
        let Some(client) = &mut self.client else {
            return Err(LogError::new("the feed is closed"));
        };
        if position < self.begin {
            let doing = "reading the feed back";
            let mut subscription = client
                .add_subscription(REPLAY_CHANNEL, REPLAY_STREAM)
                .map_err(|error| failed(doing, error))?;
            for recorded in recordings(&self.log_dir)? {
                let Some(stop) = recorded
                    .stop
                    .filter(|stop| *stop > position && *stop > recorded.start)
                else {
                    continue;
                };
                if recorded.start > position {
                    return Err(LogError::new(format!(
                        "the feed no longer holds position {position}: its earliest recording after it begins at {}",
                        recorded.start
                    )));
                }
                position = Self::play(client, &mut subscription, &recorded, position, stop, each)?;
                if let Some(feed) = &mut self.feed {
                    drain(feed);
                }
            }
            let _ = subscription.close();
        }
        if position != self.begin {
            return Err(LogError::new(format!(
                "the feed's recordings end at {position} and the feed begins at {}",
                self.begin
            )));
        }
        Ok(Position::try_from(self.begin).unwrap_or(0))
    }

    fn max_record_bytes(&self) -> usize {
        self.max_record_bytes
    }

    fn release(&mut self, before: Position) -> Result<(), LogError> {
        let before = i64::try_from(before).unwrap_or(i64::MAX);
        let held = u64::try_from(self.end - self.begin).unwrap_or(0);
        if held >= self.roll_bytes && held > 0 {
            self.roll()?;
            self.notes.push(format!(
                "the feed's recording was rolled at position {}",
                self.begin
            ));
        }
        let mut stopped: Vec<(i64, i64)> = recordings(&self.log_dir)?
            .iter()
            .filter_map(|recorded| recorded.stop.map(|stop| (stop, recorded.id)))
            .collect();
        stopped.sort_unstable();
        // The newest stopped recording is kept whatever the snapshots cover: where the feed begins at the
        // next start is read from it.
        stopped.pop();
        let Some(client) = &mut self.client else {
            return Err(LogError::new("the feed is closed"));
        };
        let mut gone = 0;
        for (stop, id) in stopped {
            if stop > before {
                continue;
            }
            // A purge that is refused, as it is while a replay still reads the recording, is sent again
            // after the next snapshot.
            match client.purge_recording(id) {
                Ok(()) => gone += 1,
                Err(error) => self
                    .notes
                    .push(format!("recording {id} of the feed was not let go of: {error}")),
            }
        }
        if gone > 0 {
            self.notes.push(format!(
                "let go of {gone} recordings of the feed, which the snapshots cover up to position {before}"
            ));
        }
        Ok(())
    }

    fn notes(&mut self) -> Vec<String> {
        std::mem::take(&mut self.notes)
    }
}

impl Drop for CavalryLog {
    fn drop(&mut self) {
        // Every append was synced before it returned, so a stop cuts the recording at what was answered.
        if let (Some(feed), Some(client)) = (self.feed.take(), &mut self.client) {
            let _ = feed.recording.stop(client);
            if let Some(own) = feed.own {
                let _ = own.close();
            }
            let _ = feed.publication.close();
        }
        if let Some(client) = self.client.take() {
            let _ = client.close();
        }
        if let Some(driver) = self.driver.take() {
            let _ = driver.close();
        }
    }
}
