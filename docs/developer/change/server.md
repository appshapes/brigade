# Change the server

`server/` is the Brigade server: a backend for [the backend REST contract](../../backend-rest-v1.md) that keeps a
team on the file system and on a Cavalry feed, with no Supabase project (work item 89; the plan is
`.context/plans/brigade-server.md`). It runs one instance today. A team is created on it with `brigade team create
--adapter rest`, and its team file names the server's url and, for a certificate no authority signed, that
certificate's fingerprint. It has no release yet.

## Three rules

1. **No shared code.** `server/` is a Rust workspace of its own. It touches the rest of the repository through the
   contract page and through the conformance suite run with the bundled `rest` adapter, and nothing else.
2. **Outside the acceptance gate.** `make test` and `make push` run no Rust. The server has its own targets and its
   own CI job, `server`, which the `master` ruleset does not require until the first server release.
3. **Its own version and dependency list.** `server/VERSION` (a test holds `Cargo.toml` to it) and
   `server/deny.toml`, which names every crate the server links. A new crate is added there in the change that
   adds it. The HTTP and TLS stack is hyper, tokio and tokio-rustls on ring (owner ruling, 2026-11-12).

## The crates

| Crate | What |
| --- | --- |
| `brigade-core` | The rules, with no I/O and no clock. `answer` takes a request's path and body, checks it in the stand-in server's order, answers it against the `State` and makes its `Entry` of records; `State::replay` is what a restart and a standby run over the same entries. |
| `brigade-log` | The ordered log behind one trait, `Log`: `append` returns after the sync, `read_from` plays the entries after a position, `release` says what the kept snapshots cover. `FileLog` is one file, for tests; `CavalryLog` (feature `cavalry`) is a recorded Cavalry publication. `Sealer` seals each entry with AES-256-GCM. |
| `brigade-server` | The binary: the options, the one thread that owns the state and the log (`engine.rs`), snapshots, HTTP and TLS. |

## Build, check and test

```sh
make server-build        # server/target/release/brigade-server
make server-check        # rustfmt, and clippy with warnings denied, with and without the Cavalry feed
make server-test         # the server's tests, the conformance suite against a running server, and a team on one
make server-deps-check   # cargo-deny against server/deny.toml; `cargo install cargo-deny --locked` once
```

The toolchain is the one `server/rust-toolchain.toml` pins; rustup installs it on the first `cargo` run inside
`server/`. Cavalry builds on Linux and macOS only, and cargo fetches it from GitHub at the pinned commit.

`make server-test` builds the Go binaries first, because the oracle is the conformance suite through
`bin/brigade`'s `rest` adapter (the contract's section 6). It runs the suite twice: on the file log, and with
`--slow` on the Cavalry feed. Beside it, `crates/brigade-core/tests/rules.rs` holds the same rules at the size of a
function call with the clock in the test's hands, and `crates/brigade-server/tests/server.rs` holds what only a
process shows: a kill and a start, a receive that waits, TLS, a process held still past Cavalry's timeouts.

The member's side has one test of its own, `TestATeamOnAServerThroughTheRealBinary` in `internal/harness/e2e`: the
built `brigade` creates a team with `--adapter rest`, a second member joins from inside a session, both sessions
start, and a message one sends reaches the other's inbox. `make test` runs it against the stand-in server; `make
server-test` runs it against `brigade-server` over TLS, with a certificate made for no address, so that only its
fingerprint in the team file can make a member trust it.

## Run one

```sh
server/target/release/brigade-server --data /tmp/brigade-data
bin/brigade-conformance --env BRIGADE_REST_URL=http://127.0.0.1:8743 --adapter bin/brigade -- adapter rest
```

`brigade-server --help` lists the options. The data directory holds `lock` (one server per directory),
`record.key` (the key entries are sealed under, made on the first start, mode 0600), `snapshots/` and the log:
`feed/` for Cavalry, `log/` for `--log file`. A directory that holds a log and no `record.key` has lost it, and
the server refuses it: a new key would open nothing that is there. A snapshot is written every
`--snapshot-every` entries, after a start that read entries and at a stop; the newest three are kept, and the
log is shortened only to the oldest of them, once two exist, so a damaged snapshot always has one behind it and
every entry since. A stop takes no new connection, answers what is being answered (a receive that waits at
once, the rest within five seconds) and then writes its snapshot.

The listener follows the owner's ruling of 2026-11-12. With `--tls-cert` and `--tls-key` the server serves HTTPS
itself, and its start line names the certificate's fingerprint (`certificate sha256:…`): what `team create
--certificate` takes and the team file pins, when no authority signed the certificate. Without them it serves plain HTTP, on a loopback address only, unless `--tls-in-front` says that TLS ends
in front of it; a start that would put plain HTTP on any other address is refused before the directory is touched.
What stands in front must pass the `Authorization` and `Brigade-Create-Token` headers, allow a body of 1 MiB, hold
a quiet request open for more than 30 seconds (the receive that waits) and never send a `POST` again by itself.

Anyone who can reach a server may create a team on it (owner ruling, 2026-11-12). An operator who wants otherwise
starts it with `--create-token-file`: creating a team then needs that file's token in the `Brigade-Create-Token`
header, and everything else is unchanged.

## What stays true

- A request that changes something becomes one entry of records: the log holds all of a request's records or
  none. The entry is appended and synced before the request is answered, and a failed append ends the process:
  the state is never ahead of the log.
- Entries are numbered from one, inside what the seal covers, and a start refuses an entry that is not the one
  after the last it applied. A log that was read wrongly, with an entry skipped or handed over twice, stops the
  start; it is never believed.
- A request is checked in the stand-in server's order (`internal/adapters/rest/stub`): the path, the body's size,
  that the body is one JSON object, the credential, the creation token, the membership of the team it names,
  then its shape and the rules. Nothing of a body is read beyond its members' names before the credential is
  known, and a member no rule reads is never read at all.
- `State::apply` cannot fail, reads no clock and draws nothing. Time and every new identifier are in the record.
  `the_same_records_make_the_same_state` in `rules.rs` holds this; a rule that needs the time takes it from the
  record, and retention's sweep is part of applying one.
- A record and a snapshot each name the version of their format, and a reader refuses a version it does not know.
  A snapshot's checksum covers its position, and its position must be the one in its name.
- No entry is longer than `record::LONGEST_ENCODED_BYTES`. The server refuses to start on a log that takes less,
  and a member BAP/1 gives no cap (`harness`, `harness_version`) has one of the server's own.
- A change to the contract lands in one commit with the adapter, the stand-in and a green conformance run.

## Cavalry

- The pin is one commit, named twice in `crates/brigade-log/Cargo.toml` (`cavalry` and `cavalry-log`). Nothing in
  Cavalry is frozen before its 0.1.0: a new pin may need the feed started again from a snapshot.
- `Shape` in `crates/brigade-log/src/feed.rs` is the one switch between pins. At the current pin a publication
  with no receiver refuses every append and a channel names no term length, so the server subscribes to its own
  feed and a log is three terms of 16 MiB. Cavalry's cards 271 and 272 change both; the comment on `Shape` says
  what to set when a pin has them.
- Every page of a log is memory from the moment it exists, and a driver that cannot create one starts all the
  same and refuses every append. So the server checks the room of the driver directory's file system first and
  refuses to start with the number: 201 MiB at the current pin. A container's default `/dev/shm` is 64 MiB;
  `docker run --shm-size=256m` or `--driver-dir` on a disk. The default driver directory is a name on
  `/dev/shm`, where any user of the host can make a file, so the server makes it private itself and refuses one
  that exists and is not a directory of its own user's.
- Every run is a new recording, on one line of positions. A restart reads the earlier recordings back through a
  replay over `cavalry:ipc`. A process held still for more than ten seconds comes back with its Cavalry client
  closed: the next append connects again and offers only the records the feed does not hold.
- A recording that is live cannot be let go of. So when a snapshot is taken and the live recording holds
  `--feed-roll-bytes` or more, the feed is rolled into a new recording at the same position, and the recordings
  that end before the oldest snapshot the server keeps are purged. The newest stopped recording always stays:
  the next start reads where the feed begins from it.

## Not built yet

There is no release yet: the image `server/Dockerfile` builds is not published anywhere (the plan's phase 3). There
is no standby instance (phase 4), and a team on a server has no Slack or email gateway: both run inside a Supabase
project today (work item 156).

## Known limits

Found by two reviews of the code on 2026-11-12 and left for a decision or a measurement:

- Nothing bounds the sessions a member registers, the principals a join secret makes or the teams a server
  holds. BAP/1 names `register_session` and `join_attempts` as reasons for `rate_limited`; the numbers are a
  product decision, and they weigh more now that anyone may create a team.
- An entry that fails its seal refuses the start, wherever it is in the log. Whether a power loss can leave such
  an entry at the feed's tail depends on Cavalry's recovery of a recording, which is not tested here.
- The seal has one key and no rotation. Nonces are random, which is sound for about four thousand million
  entries under one key: decades at a team's pace, and a number to revisit before then.
- A request nested more than 128 levels deep is refused, where the reference reads 10,000.
- An idempotency key is kept for eight days after its use, as long as its message can be, and not for the
  session's life as the filesystem adapter keeps it.
- Every entry appended wakes every receive that waits, and each asks the engine again.
