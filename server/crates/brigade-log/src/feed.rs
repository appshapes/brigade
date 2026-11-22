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
    Cavalry, Driver, Flags, Image, NULL_LENGTH, Offer, Publication, Recording, Replay, ReplayStopReason,
    Subscription,
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
/// At `d265af8` a channel names its term length, and a recording connects a publication whose channel names
/// `ssc` (Cavalry's ADRs 0098 and 0099, its cards 271 and 272). So the feed's terms are 1 MiB, a log is three
/// of them, and the server reads nothing of its own feed to keep it taking appends.
///
/// At `620a2e2`, the pin of server 0.9.0 and of every release before it, a channel named neither: a term of
/// `cavalry:udp` was 16 MiB, a publication with no connected receiver refused every offer and a recording
/// did not count as one, so the server subscribed to its own feed (`channel_keys` empty, `term_length`
/// 16 MiB, `own_subscription` true). A directory such a server wrote holds recordings of that shape, and a
/// start reads them back beside its own: one read of the feed crosses both term lengths, which is why the
/// reader takes a replay's position arithmetic from its image and never from this constant. A driver refuses
/// a channel key it does not know, so the switch is made here and not at run time.
struct Shape {
    /// What the feed's channel adds after its control address.
    channel_keys: &'static str,
    /// The term length the position keys count in, in bytes.
    term_length: u64,
    /// Whether the server reads its own feed so that the feed takes appends.
    own_subscription: bool,
}

const SHAPE: Shape = Shape {
    channel_keys: "|ssc=true|term-length=1m",
    term_length: 1 << 20,
    own_subscription: false,
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
    /// The term length it was recorded with, which is the shape of the feed that wrote it.
    term_length: u64,
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
    /// How a read back learns that a replay ended.
    ended: Ended,
}

/// How a reader learns that a replay ended: the replay's own word, asked with whether the replay's image is
/// still in sight. The server asks the replay and nothing else. A test asks late, as a driver can make a
/// client learn it: `replay stopped` may reach the client after the driver has withdrawn the image.
type Ended = fn(&Replay, bool) -> Option<(i64, Option<ReplayStopReason>)>;

/// The replay's own word, whenever it is asked.
fn as_the_replay_says(replay: &Replay, _in_sight: bool) -> Option<(i64, Option<ReplayStopReason>)> {
    replay.ended()
}

/// The image of one session on a subscription, while the subscription holds it.
fn image_of(subscription: &Subscription, session: i32) -> Option<&Image> {
    subscription
        .images()
        .iter()
        .find(|image| image.session_id() == session)
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

/// How many bytes the driver directory's file system needs for logs whose terms are `term_length` long.
///
/// A log is three terms and a page of metadata. The feed holds its publication and, while it reads itself,
/// an image; a restart's replay holds two more for a moment; twice that leaves room for the ones that linger
/// while they are replaced (the Cavalry session's rule, its section F4).
fn room_for(term_length: u64) -> u64 {
    DRIVER_FILES_BYTES + 4 * (3 * term_length + 4096)
}

/// Refuses a driver directory whose file system has no room for the feed's logs.
///
/// Every page of a log is real memory from the moment the log is created, and a driver that cannot create
/// one starts all the same: the feed is added and every append is refused `not-connected`, with nothing in
/// the client's answer to say why. A container's default `/dev/shm` of 64 MiB is such a place. So the room
/// is checked here and the refusal names the number.
///
/// It is asked for logs with terms of `term_length`: the feed's own, or the longest a recording of the feed
/// has when that is longer. A replay's log has the terms of its recording, so a directory that a server of
/// an earlier shape wrote needs that shape's room until its recordings are let go of.
// The two fields are 64 bits wide on Linux and narrower on macOS, so the conversions are needed on one and
// idle on the other.
#[allow(clippy::useless_conversion)]
fn check_room(driver_dir: &Path, term_length: u64) -> Result<(), LogError> {
    let needed = room_for(term_length);
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
            "the file system of the driver directory {} has room for {} MiB and the feed needs {} MiB{}: \
             give the container more shared memory (docker run --shm-size, or an emptyDir with medium Memory \
             mounted at /dev/shm), or name another directory with --driver-dir",
            driver_dir.display(),
            room >> 20,
            needed.div_ceil(1 << 20),
            if term_length > SHAPE.term_length {
                ", because it still holds recordings that an earlier server wrote with longer terms"
            } else {
                ""
            },
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
                term_length: u64::try_from(descriptor.term_length()).unwrap_or(0),
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
        // The room is asked for before the driver opens the log directory, because a driver changes a
        // directory as it opens it: a start that is refused for the room has touched nothing. A recording
        // of longer terms than the feed's own is replayed into a log of its own terms, and the catalog
        // says how long they are, also the catalog of a directory an earlier driver wrote. A directory with
        // no catalog yet holds no recording.
        let longest = recordings(&options.log_dir).map_or(0, |held| {
            held.iter()
                .map(|recorded| recorded.term_length)
                .max()
                .unwrap_or(0)
        });
        check_room(&driver_dir, SHAPE.term_length.max(longest))?;
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
            ended: as_the_replay_says,
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
        ended: Ended,
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
        // The furthest the replay's image has stood in any pass of the loop below. The image's own position
        // counts the padding at a term's end, which reaches no handler, so for a recording that ends in
        // padding it is the only thing that says the end was read. It is kept across the passes because the
        // two things a reader waits for can come apart: the driver withdraws a replay's image once it has
        // been read to the end of stream, and `replay stopped` can reach this client after that. Until work
        // item 375 the reader looked for the image and the end in one pass, found neither in that order,
        // and failed the start on a recording that was whole.
        let mut stood = from;
        loop {
            // The images first, so that the replay's position arithmetic is in hand before its frames.
            subscription
                .poll(|_, _| {}, 0)
                .map_err(|error| failed(doing, error))?;
            if arithmetic.is_none() {
                arithmetic = image_of(subscription, session).map(Image::positions);
            }
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
            // Where the image stands is read here, after the frames and before any other poll. The poll
            // above is what takes the image past a padding frame, and an image leaves a subscription only
            // inside a poll, so an image the driver withdraws next is still seen here at its last position.
            // Read before the frames instead, a withdrawal that lands between that poll and the next pass
            // would hide the end. The test holds that the position is kept; no test can force that moment,
            // so this comment is all that holds the line's place.
            let image = image_of(subscription, session);
            let in_sight = image.is_some();
            if let Some(image) = image {
                stood = stood.max(image.position());
            }
            if let Some(error) = refused {
                return Err(error);
            }
            if read > 0 {
                heard = Instant::now();
                continue;
            }
            if let Some((end, reason)) = ended(&replay, in_sight) {
                // The replay is read to its end when the last frame handed over ends there, or the image
                // has stood there in this pass or an earlier one. Nothing else is the end. An image that is
                // not in sight yet has delivered nothing, and one that has gone may have gone unread: the
                // driver lets go of a replay's image a few seconds after the replay stops, whether or not
                // its subscriber read it.
                if delivered >= end || stood >= end {
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
                        "the replay of recording {} was let go of at position {}, before it was read to its \
                         end at {end}: start the server again",
                        recorded.id,
                        delivered.max(stood)
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
        let ended = self.ended;
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
                position = Self::play(client, &mut subscription, &recorded, position, stop, each, ended)?;
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

#[cfg(test)]
mod tests {
    use std::io::Write as _;
    use std::process::{Child, Command, Stdio};

    use super::*;

    /// A directory of the test's own, removed when the test ends, however it ends. The driver directory is
    /// in it, as in the tests of the running server, so the temporary directory needs the room `check_room`
    /// asks for.
    struct Scratch(PathBuf);

    impl Scratch {
        fn new(test: &str) -> Self {
            let dir = std::env::temp_dir().join(format!("brigade-log-{test}-{}", std::process::id()));
            let _ = std::fs::remove_dir_all(&dir);
            std::fs::create_dir_all(&dir).unwrap();
            Self(dir)
        }
    }

    impl Drop for Scratch {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.0);
        }
    }

    /// A UDP port nothing is bound to now, and that no other call of this run was given: the system hands a
    /// port out again as soon as it is free.
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

    fn options(scratch: &Scratch) -> FeedOptions {
        FeedOptions {
            log_dir: scratch.0.join("log"),
            driver_dir: Some(scratch.0.join("driver")),
            data_dir: scratch.0.join("data"),
            control: free_udp(),
            own_endpoint: free_udp(),
            roll_bytes: u64::MAX,
        }
    }

    /// The same options with the feed on two new ports.
    fn on_new_ports(options: &FeedOptions) -> FeedOptions {
        FeedOptions {
            log_dir: options.log_dir.clone(),
            driver_dir: options.driver_dir.clone(),
            data_dir: options.data_dir.clone(),
            control: free_udp(),
            own_endpoint: free_udp(),
            roll_bytes: options.roll_bytes,
        }
    }

    /// Whether a failure is a port that was free when it was chosen and taken before the driver bound it,
    /// which any process of the machine can do.
    fn port_taken(failure: &str) -> bool {
        failure.contains("Address already in use")
    }

    /// Opens the feed as a start does, and on a port that was taken meanwhile opens it on two new ones, as
    /// the tests of the running server start a server again.
    fn opened(options: &FeedOptions) -> CavalryLog {
        let (mut moved, mut tries) = (None, 1);
        loop {
            match CavalryLog::open(moved.as_ref().unwrap_or(options)) {
                Ok(log) => return log,
                Err(failure) => {
                    assert!(
                        port_taken(&failure.to_string()) && tries < 4,
                        "the feed did not open at try {tries} of 4: {failure}"
                    );
                    moved = Some(on_new_ports(options));
                    tries += 1;
                }
            }
        }
    }

    /// The feed's recordings that hold something or are live, from the catalog: where each begins and where
    /// it stopped. One that an open left empty says nothing: an open that fails on its second port leaves one.
    fn held(options: &FeedOptions) -> Vec<(i64, Option<i64>)> {
        recordings(&options.log_dir)
            .unwrap()
            .iter()
            .filter(|recorded| recorded.stop != Some(recorded.start))
            .map(|recorded| (recorded.start, recorded.stop))
            .collect()
    }

    /// `replay stopped` as it can reach a client: only after the driver has withdrawn the replay's image.
    fn told_after_the_image_went(replay: &Replay, in_sight: bool) -> Option<(i64, Option<ReplayStopReason>)> {
        if in_sight { None } else { replay.ended() }
    }

    /// Appends up to a term's last kilobytes and then writes the term's padding, and returns the length of
    /// each record with the position after it. It leaves the feed in the moment after an offer met the end
    /// of a term and before the offer was made again: the feed's last frame is padding, which no reader's
    /// handler is given.
    fn appended_up_to_a_terms_padding(log: &mut CavalryLog) -> Vec<(usize, i64)> {
        let term = i64::try_from(SHAPE.term_length).unwrap();
        let mut appended = Vec::new();
        // Large records up to the last megabytes of the term, then small ones up to its last kilobytes. The
        // sizes are within what the feed takes as one record at any term length, the 1 MiB it is to move to
        // among them.
        let max = log.max_record_bytes();
        for (length, room) in [((1 << 20).min(max), 3 << 20), (16 << 10, 64 << 10)] {
            let record = vec![7_u8; length];
            while term - log.end > room {
                let after = log.append(std::slice::from_ref(&record)).unwrap();
                appended.push((length, i64::try_from(after).unwrap()));
            }
        }
        // One offer more, of the largest record the feed takes, which the term has no room for: it writes
        // the padding to the term's end and takes nothing. It is not made again. Until the feed has room to
        // look at the offer it says what an append waits out, and that is waited out here too.
        let (feed, client) = (log.feed.as_mut().unwrap(), log.client.as_mut().unwrap());
        let (too_long, deadline) = (vec![7_u8; max], Instant::now() + PATIENCE);
        loop {
            match feed.publication.offer(&too_long) {
                Err(Offer::AdminAction) => break,
                Err(Offer::NotConnected | Offer::BackPressured) if Instant::now() < deadline => {
                    drain(feed);
                    std::thread::sleep(Duration::from_micros(200));
                }
                answer => panic!("the offer at the term's end was answered {answer:?}"),
            }
        }
        while feed.recording.position() < term {
            assert!(
                !client.is_closed() && Instant::now() < deadline,
                "the recorder did not reach the padding: it stands at {}",
                feed.recording.position()
            );
            drain(feed);
            std::thread::sleep(Duration::from_micros(200));
        }
        assert!(
            appended.last().is_some_and(|(_, after)| *after < term),
            "the last record ends at the term's end: {:?}",
            appended.last()
        );
        appended
    }

    /// Writes a recording that stops right after a term's padding, by a feed that is closed in that moment.
    fn a_recording_that_ends_in_padding(options: &FeedOptions) -> Vec<(usize, i64)> {
        let term = i64::try_from(SHAPE.term_length).unwrap();
        let mut log = opened(options);
        let appended = appended_up_to_a_terms_padding(&mut log);
        drop(log);
        assert_eq!(
            held(options),
            [(0, Some(term))],
            "the recording does not stop at the term's end"
        );
        appended
    }

    /// Opens the directory as a start does and reads every record back: each one's length and the position
    /// after it, and where the read ended.
    fn read_back(options: &FeedOptions, ended: Ended) -> Result<(Vec<(usize, i64)>, Position), LogError> {
        let mut log = opened(options);
        log.ended = ended;
        let mut read = Vec::new();
        let end = log.read_from(0, &mut |record, after| {
            read.push((record.len(), i64::try_from(after).unwrap()));
            Ok(())
        })?;
        Ok((read, end))
    }

    /// A recording that stops right after a term's padding is read to its end, whichever of the driver's two
    /// messages reaches the reader first (work item 375).
    #[test]
    fn a_recording_that_ends_in_padding_is_read_to_its_end_when_its_image_goes_before_the_replays_end_is_told()
     {
        let scratch = Scratch::new("ends-in-padding");
        let options = options(&scratch);
        let appended = a_recording_that_ends_in_padding(&options);
        let term = Position::try_from(SHAPE.term_length).unwrap();

        // The order that failed: the image is withdrawn, and only then is the replay's end told.
        let (read, end) = read_back(&options, told_after_the_image_went).unwrap();
        assert_eq!((read, end), (appended.clone(), term));
        // And asked as the server asks, in whichever order the driver's two threads make of it. That is not
        // the order that holds on its own: on the reader as it was, 9 of 12 such reads of this recording
        // failed on the machine the fix was written on.
        let (read, end) = read_back(&options, as_the_replay_says).unwrap();
        assert_eq!((read, end), (appended, term));
    }

    // From here on, a process that appends to the feed and is killed, and what the next start reads back
    // (work item 384). Before it, nothing in the server's own gate read back a record of several frames, a
    // feed across a term, or a feed that was rolled and purged.

    /// What makes a run of this test program the process that appends: the directory it works in.
    const APPENDS_IN: &str = "BRIGADE_LOG_TEST_APPENDS_IN";
    /// The process id of the test program that started it.
    const STARTED_BY: &str = "BRIGADE_LOG_TEST_STARTED_BY";
    /// The feed's two addresses, for that process.
    const CONTROL: &str = "BRIGADE_LOG_TEST_CONTROL";
    const OWN: &str = "BRIGADE_LOG_TEST_OWN";
    /// How many bytes a recording holds before a release rolls it, for a process that takes snapshots.
    const ROLL: &str = "BRIGADE_LOG_TEST_ROLL_BYTES";
    /// Set for a process that writes a term's padding and then waits to be killed.
    const PADS: &str = "BRIGADE_LOG_TEST_PADS";

    /// After how many appends the process that takes snapshots takes one.
    const SNAPSHOT_EVERY: usize = 12;

    /// Record `index` of the process: its index, then bytes in a pattern that repeats every 251, which no
    /// frame's length divides, so a frame that is dropped, doubled or out of its place shows. The lengths go
    /// from less than one frame to more frames than one poll of the reader takes, within what the feed takes
    /// as one record.
    fn record(index: u64, max: usize) -> Vec<u8> {
        const LENGTHS: [usize; 9] = [8, 200, 1_400, 1_500, 4_000, 100_000, 100_000, 100_000, 300_000];
        let seed = usize::try_from(index).unwrap();
        let length = LENGTHS[seed % LENGTHS.len()].min(max);
        let mut bytes = index.to_le_bytes().to_vec();
        bytes.extend((8..length).map(|at| u8::try_from((seed * 31 + at) % 251).unwrap()));
        bytes
    }

    /// Not a test of its own: the process that the tests below start and kill. Started with a directory in
    /// its environment, it opens the feed there and appends for as long as it lives. In the file `answered`
    /// it says what a server would have answered or kept:
    ///
    /// - `A <first index> <count> <position>` once an append has returned.
    /// - `S <position> <bytes held>` when a snapshot at a position would be on the disk, with how many bytes
    ///   the live recording holds. Like the server it keeps two snapshots and then releases what lies before
    ///   the older one: `D <position>` once that release has returned, and `N <note>` for what the log tells
    ///   the operator about it.
    /// - `L <length> <position>` for each record and then `P`, for a process that writes a term's padding.
    #[test]
    fn the_process_that_appends_until_it_is_killed() {
        let Ok(dir) = std::env::var(APPENDS_IN) else {
            return;
        };
        let dir = PathBuf::from(dir);
        let roll_bytes: Option<u64> = std::env::var(ROLL).ok().map(|bytes| bytes.parse().unwrap());
        let options = FeedOptions {
            log_dir: dir.join("log"),
            driver_dir: Some(dir.join("driver")),
            data_dir: dir.join("data"),
            control: std::env::var(CONTROL).unwrap(),
            own_endpoint: std::env::var(OWN).unwrap(),
            roll_bytes: roll_bytes.unwrap_or(u64::MAX),
        };
        let mut answered = std::fs::OpenOptions::new()
            .create(true)
            .append(true)
            .open(dir.join("answered"))
            .unwrap();
        // One write for a line, so that a kill leaves whole lines and at most one that is cut short.
        let mut say = |line: String| answered.write_all(line.as_bytes()).unwrap();
        let mut log = CavalryLog::open(&options).unwrap();
        // A process that nobody kills ends on its own: at once when the test program that started it has
        // gone without killing it, as one that is itself killed does, after two minutes, and after four times
        // what a test lets it append. Its parent is compared with the process id it was given and not with
        // the one it finds, which is already another's when the test program went first.
        let ends = Instant::now() + Duration::from_secs(120);
        let started_by: u32 = std::env::var(STARTED_BY).unwrap().parse().unwrap();
        let wanted = move || Instant::now() < ends && std::os::unix::process::parent_id() == started_by;
        if std::env::var(PADS).is_ok() {
            for (length, after) in appended_up_to_a_terms_padding(&mut log) {
                say(format!("L {length} {after}\n"));
            }
            say("P\n".to_owned());
            while wanted() {
                std::thread::sleep(Duration::from_millis(50));
            }
            return;
        }
        let max = log.max_record_bytes();
        let records_of = |batch: usize, next: u64| -> Vec<Vec<u8>> {
            let count = [1, 3, 2][batch % 3];
            (next..next + count).map(|index| record(index, max)).collect()
        };
        let (mut next, mut batch, mut snapshot) = (0_u64, 0_usize, None);
        let mut records = records_of(batch, next);
        while wanted() && log.end < 4 * more_than_a_term() {
            let after = log.append(&records).unwrap();
            let count = u64::try_from(records.len()).unwrap();
            // The next records are made before this append is said to have returned, so that the process
            // goes from saying so straight into the next append, and a kill that follows the line finds it
            // there: with frames offered that the recorder has not written, or has written in part.
            records = records_of(batch + 1, next + count);
            say(format!("A {next} {count} {after}\n"));
            next += count;
            batch += 1;
            if roll_bytes.is_some() && batch % SNAPSHOT_EVERY == 0 {
                say(format!("S {after} {}\n", log.end - log.begin));
                if let Some(older) = snapshot.replace(after) {
                    log.release(older).unwrap();
                    for note in log.notes() {
                        say(format!("N {note}\n"));
                    }
                    say(format!("D {after}\n"));
                }
            }
        }
    }

    /// What the process said before it was killed.
    #[derive(Default)]
    struct Answered {
        /// The appends that returned: the first record's index, how many records, the position after the last.
        batches: Vec<(u64, u64, i64)>,
        /// The snapshots: the position of each, and how many bytes the live recording held then.
        snapshots: Vec<(i64, u64)>,
        /// How many releases returned.
        released: usize,
        /// What its log told the operator.
        notes: Vec<String>,
        /// The records of a process that wrote a term's padding, and whether it had written the padding.
        before_the_padding: Vec<(usize, i64)>,
        padded: bool,
    }

    impl Answered {
        fn read(scratch: &Scratch) -> Self {
            let text = std::fs::read_to_string(scratch.0.join("answered")).unwrap_or_default();
            // A last line that the kill cut short says nothing.
            let whole = text.rfind('\n').map_or("", |end| &text[..end + 1]);
            let mut answered = Self::default();
            for line in whole.lines() {
                let (kind, rest) = line.split_once(' ').unwrap_or((line, ""));
                let numbers: Vec<&str> = rest.split(' ').collect();
                let number = |at: usize| numbers[at].parse::<i64>().unwrap();
                match kind {
                    "A" => answered.batches.push((
                        numbers[0].parse().unwrap(),
                        numbers[1].parse().unwrap(),
                        number(2),
                    )),
                    "S" => answered.snapshots.push((number(0), numbers[1].parse().unwrap())),
                    "D" => answered.released += 1,
                    "L" => answered
                        .before_the_padding
                        .push((numbers[0].parse().unwrap(), number(1))),
                    "P" => answered.padded = true,
                    _ => answered.notes.push(rest.to_owned()),
                }
            }
            answered
        }

        /// The position after the last record whose append returned.
        fn end(&self) -> i64 {
            self.batches.last().map_or(0, |(_, _, after)| *after)
        }

        /// Whether the process had got as far as an append.
        fn said_nothing(&self) -> bool {
            self.batches.is_empty() && self.before_the_padding.is_empty()
        }

        /// How many of its notes say `what`.
        fn told(&self, what: &str) -> usize {
            self.notes.iter().filter(|note| note.contains(what)).count()
        }

        /// How many of its releases let go of recordings. A purge that was refused is told in other words,
        /// which end in these.
        fn purges(&self) -> usize {
            self.notes
                .iter()
                .filter(|note| note.starts_with("let go of "))
                .count()
        }

        /// The newest snapshot's position and the one's before it, once there are two.
        fn last_two_snapshots(&self) -> Option<(i64, i64)> {
            match self.snapshots[..] {
                [.., (older, _), (newest, _)] => Some((older, newest)),
                _ => None,
            }
        }
    }

    /// The process, killed when the test lets go of it, however the test ends.
    struct Appender(Child);

    impl Drop for Appender {
        fn drop(&mut self) {
            let _ = self.0.kill();
            let _ = self.0.wait();
        }
    }

    /// Starts the process on the feed of `options`, lets it go on until `enough` says so and for `longer`
    /// after that, and kills it with the signal no process can handle, as a power cut is to a process.
    /// Returns what it said.
    fn appended_and_killed(
        scratch: &Scratch,
        options: &FeedOptions,
        pads: bool,
        longer: Duration,
        enough: impl Fn(&Answered) -> bool,
    ) -> Answered {
        let test = format!(
            "{}::the_process_that_appends_until_it_is_killed",
            module_path!().split_once("::").unwrap().1
        );
        let start = |options: &FeedOptions| {
            let output = std::fs::File::create(scratch.0.join("output")).unwrap();
            let mut command = Command::new(std::env::current_exe().unwrap());
            command
                .args(["--exact", &test, "--nocapture"])
                .env(APPENDS_IN, &scratch.0)
                .env(STARTED_BY, std::process::id().to_string())
                .env(CONTROL, &options.control)
                .env(OWN, &options.own_endpoint)
                .stdin(Stdio::null())
                .stdout(output.try_clone().unwrap())
                .stderr(output);
            if options.roll_bytes != u64::MAX {
                command.env(ROLL, options.roll_bytes.to_string());
            }
            if pads {
                command.env(PADS, "1");
            }
            Appender(command.spawn().unwrap())
        };
        let mut appender = start(options);
        let (deadline, mut starts) = (Instant::now() + Duration::from_secs(90), 1);
        loop {
            let answered = Answered::read(scratch);
            if enough(&answered) {
                break;
            }
            let output = || std::fs::read_to_string(scratch.0.join("output")).unwrap_or_default();
            if let Some(ended) = appender.0.try_wait().unwrap() {
                // The process ended on its own. One that could not open the feed because a port was taken
                // before its driver bound it has said nothing, and is started again on two new ports.
                assert!(
                    port_taken(&output()) && answered.said_nothing() && starts < 4,
                    "the process that appends ended on its own ({ended}); it said:\n{}",
                    output()
                );
                appender = start(&on_new_ports(options));
                starts += 1;
            }
            assert!(
                Instant::now() < deadline,
                "the process that appends was too slow; it said:\n{}",
                output()
            );
            std::thread::sleep(Duration::from_millis(2));
        }
        std::thread::sleep(longer);
        appender.0.kill().unwrap();
        appender.0.wait().unwrap();
        Answered::read(scratch)
    }

    /// Opens the directory as the next start does and reads the feed back from `from`. Every record read is
    /// the process's next one, whole, and every record whose append returned is among them, at the position
    /// the append returned. After them come at most the records of the one append the kill was in.
    fn every_record_is_read_back(options: &FeedOptions, answered: &Answered, from: i64, ended: Ended) {
        let first = match from {
            0 => 0,
            _ => answered
                .batches
                .iter()
                .find(|(_, _, after)| *after == from)
                .map(|(first, count, _)| first + count)
                .expect("the read begins where an append ended"),
        };
        let mut log = opened(options);
        log.ended = ended;
        let max = log.max_record_bytes();
        let (mut read, mut wrong) = (Vec::new(), None);
        let end = log
            .read_from(Position::try_from(from).unwrap(), &mut |bytes, after| {
                let index = first + u64::try_from(read.len()).unwrap();
                let appended = record(index, max);
                if wrong.is_none() && bytes != appended {
                    let at = bytes.iter().zip(&appended).position(|(found, expected)| found != expected);
                    wrong = Some(format!(
                        "record {index} was appended with {} bytes and read back with {}; the first byte that \
                         differs is at {at:?}",
                        appended.len(),
                        bytes.len()
                    ));
                }
                read.push(i64::try_from(after).unwrap());
                Ok(())
            })
            .unwrap();
        assert_eq!(wrong, None);
        let position_of = |index: u64| {
            usize::try_from(index - first)
                .ok()
                .and_then(|at| read.get(at).copied())
        };
        for (first_of_batch, count, after) in &answered.batches {
            if *after > from {
                let last = first_of_batch + count - 1;
                assert_eq!(
                    position_of(last),
                    Some(*after),
                    "record {last}, whose append returned, is read back at its position"
                );
            }
        }
        let returned = answered
            .batches
            .last()
            .map_or(0, |(first, count, _)| first + count);
        let read_to = first + u64::try_from(read.len()).unwrap();
        assert!(
            (returned..=returned + 3).contains(&read_to),
            "{returned} records were appended and answered, and the read ended after {read_to}"
        );
        let end = i64::try_from(end).unwrap();
        assert!(
            answered.end() <= end && read.last().is_none_or(|last| *last <= end),
            "the feed ends at {end}, the last append that returned at {}, the last record read at {:?}",
            answered.end(),
            read.last()
        );
    }

    /// As much as the process appends before it is killed: more than a term, so that the feed crosses one,
    /// and 20 MiB at the least.
    fn more_than_a_term() -> i64 {
        let term = i64::try_from(SHAPE.term_length).unwrap();
        (term + term / 4).max(20 << 20)
    }

    /// A process appends records of one frame and of many across a term's end and is killed. The next start
    /// reads back every record it had answered, in both orders in which a replay's end can reach the reader.
    #[test]
    fn a_feed_across_a_term_is_read_back_whole_after_its_process_was_killed() {
        let scratch = Scratch::new("killed");
        let options = options(&scratch);
        let answered = appended_and_killed(&scratch, &options, false, Duration::ZERO, |answered| {
            answered.end() >= more_than_a_term()
        });
        assert!(answered.end() > i64::try_from(SHAPE.term_length).unwrap());

        every_record_is_read_back(&options, &answered, 0, as_the_replay_says);
        every_record_is_read_back(&options, &answered, 0, told_after_the_image_went);
    }

    /// A feed that takes snapshots as the server does: it rolls at every second one, since a recording
    /// holds less than this after one and more after two, and it lets go of what lies before the older of
    /// the two snapshots it keeps.
    fn with_snapshots(scratch: &Scratch) -> FeedOptions {
        FeedOptions {
            roll_bytes: 5 << 19,
            ..options(scratch)
        }
    }

    /// What every read back after a process that took snapshots holds: the feed was rolled, recordings were
    /// let go of, and what they held is gone, so a read from the feed's first position is refused. Then the
    /// next start reads back from the newest snapshot, as a start does, and from the one before it, as a
    /// start does that passes over a damaged snapshot. Returns the recordings the feed held when it was killed.
    fn read_back_from_both_snapshots(options: &FeedOptions, answered: &Answered) -> Vec<(i64, Option<i64>)> {
        assert!(
            answered.told("was rolled") >= 2
                && answered.purges() >= 2
                && answered.told("was not let go of") == 0,
            "{:?}",
            answered.notes
        );
        let (older, newest) = answered.last_two_snapshots().unwrap();
        let held = held(options);
        let earliest = held.iter().map(|(start, _)| *start).min().unwrap();
        assert!(
            0 < earliest && earliest <= older,
            "the feed holds {held:?} and the older snapshot is at {older}"
        );
        let refused = opened(options).read_from(0, &mut |_, _| Ok(())).unwrap_err();
        assert!(
            refused.to_string().contains("no longer holds position 0"),
            "{refused}"
        );

        every_record_is_read_back(options, answered, newest, as_the_replay_says);
        every_record_is_read_back(options, answered, older, as_the_replay_says);
        every_record_is_read_back(options, answered, older, told_after_the_image_went);
        held
    }

    /// The same through rolls and purges. The process is killed while it appends, after a snapshot that
    /// rolled the feed: the read from the snapshot before it begins in the middle of a recording and goes on
    /// into the next one.
    #[test]
    fn a_feed_that_was_rolled_and_purged_is_read_back_from_its_snapshots_after_its_process_was_killed() {
        let scratch = Scratch::new("rolled");
        let options = with_snapshots(&scratch);
        let answered = appended_and_killed(&scratch, &options, false, Duration::ZERO, |answered| {
            let Some((_, newest)) = answered.last_two_snapshots() else {
                return false;
            };
            answered.end() >= more_than_a_term()
                && answered.purges() >= 2
                && answered.released + 1 == answered.snapshots.len()
                && answered.told(&format!("was rolled at position {newest}")) == 1
                && answered.end() - newest >= 256 << 10
        });
        let (older, newest) = answered.last_two_snapshots().unwrap();

        let held = read_back_from_both_snapshots(&options, &answered);
        // The older snapshot lies inside a recording that stopped at the newest, where the killed one began.
        assert!(
            held.iter()
                .any(|(start, stop)| *start < older && *stop == Some(newest))
                && held.iter().any(|(start, _)| *start == newest),
            "the feed holds {held:?}; the snapshots are at {older} and {newest}"
        );
    }

    /// And a process killed in the release that rolls the feed, at four moments of the roll: the snapshot is
    /// on the disk, and the feed is somewhere between its old recording and its new one. On the machine this
    /// was written on, idle, a kill up to 6 ms into the release found the old recording still live, one from
    /// 8 ms to 16 ms found it stopped and no new one, one from 20 ms on found the new one live in a release
    /// that had not returned, and a roll was over after about 130 ms. Where the moments fall elsewhere, each
    /// is still a kill that the next start has to read back from; a kill after the roll finds the process
    /// appending again, which the test above holds.
    #[test]
    fn a_feed_whose_process_was_killed_while_it_rolled_is_read_back_from_its_snapshots() {
        for (case, longer) in [0, 12, 60, 120].into_iter().enumerate() {
            let scratch = Scratch::new(&format!("rolling-{case}"));
            let options = with_snapshots(&scratch);
            let longer = Duration::from_millis(longer);
            let answered = appended_and_killed(&scratch, &options, false, longer, |answered| {
                answered.released + 1 < answered.snapshots.len()
                    && answered
                        .snapshots
                        .last()
                        .is_some_and(|(_, held)| *held >= options.roll_bytes)
            });
            let (older, newest) = answered.last_two_snapshots().unwrap();

            every_record_is_read_back(&options, &answered, newest, as_the_replay_says);
            every_record_is_read_back(&options, &answered, older, as_the_replay_says);
            every_record_is_read_back(&options, &answered, older, told_after_the_image_went);
        }
    }

    /// A process killed in the moment after an offer met a term's end. The kill leaves the recording live.
    ///
    /// Under the Cavalry of server 0.9.0 the next start stopped it at the term's end, after the padding, and
    /// this was work item 375 from a real kill: the reader as it was failed on it. The driver this build is
    /// pinned to cuts a recording that a crash left live to the last position a sync covered (Cavalry's ADR
    /// 0144), and the feed syncs after every append and not after the padding. So the padding the kill came
    /// after is gone: the feed ends at the last record that was answered, and it is read back whole in both
    /// orders in which a replay's end can reach the reader. A feed that is closed in that moment still ends
    /// in padding, which the first test of this file holds.
    #[test]
    fn a_feed_whose_process_was_killed_right_after_a_terms_padding_ends_at_its_last_record() {
        let scratch = Scratch::new("padded");
        let options = options(&scratch);
        let answered = appended_and_killed(&scratch, &options, true, Duration::ZERO, |answered| {
            answered.padded
        });
        let last = answered.before_the_padding.last().unwrap().1;
        assert!(last < i64::try_from(SHAPE.term_length).unwrap());
        assert_eq!(held(&options), [(0, None)], "the kill leaves the recording live");

        let read = read_back(&options, told_after_the_image_went).unwrap();
        assert_eq!(
            read,
            (
                answered.before_the_padding.clone(),
                Position::try_from(last).unwrap()
            )
        );
        assert_eq!(
            held(&options),
            [(0, Some(last))],
            "the start cuts the recording at the last position a sync covered"
        );
        let read = read_back(&options, as_the_replay_says).unwrap();
        assert_eq!(
            read,
            (answered.before_the_padding, Position::try_from(last).unwrap())
        );
    }

    /// The room the server asks of its driver directory is for the longest term a log there can have. The
    /// feed's own terms are 1 MiB. A recording that server 0.9.0 wrote has terms of 16 MiB, the catalog says
    /// so, and a replay's log has the terms of its recording: a directory that still holds one needs what
    /// that server needed (work item 387). The catalog here is the one the released 0.9.0 left, as it left it.
    #[test]
    fn the_room_asked_for_is_that_of_the_longest_term_a_recording_has() {
        let left = Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/stored/0.9.0/feed-data/feed");
        let longest = recordings(&left)
            .unwrap()
            .iter()
            .map(|recorded| recorded.term_length)
            .max();
        assert_eq!(longest, Some(16 << 20));
        assert_eq!(room_for(16 << 20).div_ceil(1 << 20), 201);
        assert_eq!(
            (SHAPE.term_length, room_for(SHAPE.term_length).div_ceil(1 << 20)),
            (1 << 20, 21)
        );
    }
}
