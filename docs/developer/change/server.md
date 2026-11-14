# Change the server

`server/` is the Brigade server: a backend for [the backend REST contract](../../backend-rest-v1.md) that keeps a
team on the file system and on a Cavalry feed, with no Supabase project (work item 89; the plan is
`.context/plans/brigade-server.md`). It runs one instance today. A team is created on it with `brigade team create
--adapter rest`, and its team file names the server's url and, for a certificate no authority signed, that
certificate's fingerprint. Its first release is 0.1.0; the operator's pages are [Project Owner ›
Server](../../project-owner/server/README.md).

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
| `brigade-gateway` | What the mail and Slack gateways decide and what they write, with no I/O: the directives at the top of a text, which session an argument means, the Brigade body of a mail or a Slack message and the mail or the post of a session's message, the roster's text, the signature checks, and for Slack's Web API, Resend and Postmark the request to make and the reading of its answer. It is the Edge Functions' shared TypeScript ported text for text, and its tests are theirs. |
| `brigade-server` | The binary: the options, the one thread that owns the state and the log (`engine.rs`), snapshots, HTTP and TLS, and under `gateway/` the gateways' I/O: their configuration and state, the HTTPS client, the loop and the two endpoints. |

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
--certificate` takes and the team file pins, when no authority signed the certificate. With `--tls-self-signed`
the server makes that certificate itself on its first start (`tls.rs`, rcgen on ring), keeps it and its key in
`<data>/tls`, and serves the same one on every later start: a new certificate would be one no team file pins, so
a directory that holds one of the two files and not the other is refused, never repaired. Without them it serves plain HTTP, on a loopback address only, unless `--tls-in-front` says that TLS ends
in front of it; a start that would put plain HTTP on any other address is refused before the directory is touched.
What stands in front must pass the `Authorization` and `Brigade-Create-Token` headers, allow a body of 1 MiB, hold
a quiet request open for more than 30 seconds (the receive that waits) and never send a `POST` again by itself.

Anyone who can reach a server may create a team on it (owner ruling, 2026-11-12). An operator who wants otherwise
starts it with `--create-token-file`: creating a team then needs that file's token in the `Brigade-Create-Token`
header, and everything else is unchanged.

## Taking a team in

`--import-team <name> --join-secret-file <file>` writes one `team.created` record for a team that exists
elsewhere: the reference inside the secret, the name, the SHA-256 of the whole secret, and no creator and no
member. A join checks that hash against the team the secret names, so the team's members join with the secret
they have. It is a job of its own kind for the engine, appended and synced like any other, and not a request:
`answer` knows no path for it, so no address of the server can reach it. It runs between a stop and a start,
because one process owns the log. The same secret again changes nothing; another secret for the same reference
is refused.

## Administering a team

From 0.4.0 the contract's section 3a is three requests like any other: `/v1/team/rotate-secret`,
`/v1/team/revoke-member` and `/v1/team/transfer`. One gate in `execute.rs` answers them for the team's owner and
gives everyone else exactly what `member_team` gives a stranger.

- **Four records.** `team.secret_rotated`, `member.revoked`, `members.revoked_by_version` and
  `team.owner_changed`. A revocation by version is one record however many members it covers, because an entry
  has a longest size: applying it finds the members from the state alone. Applying `member.joined` sets the
  member's `joined_secret_version` to the team's.
- **The owner** is `Team.created_by`. A team that was taken in has none until a join whose request `may_own`:
  the HTTP listener passes true, and the gateways' join inside the server passes false.
- **`--set-owner <team_ref> --principal <principal_ref>`** is a job of its own kind between a stop and a start,
  as the import is, and no request reaches it. **`--take-back <team_ref> --secret-file <path>`** is another: one
  entry of a rotation, a revocation by version of everyone and an owner change to nobody, so that the first
  member to join with the new secret owns the team. The file's place is held before the entry is appended.
- **Snapshot format 2** came with them. A 0.3.0 passes a format 2 snapshot over and then refuses a log that
  holds a record it does not know, so it never reads a state whose bans it would drop.
- **A gateway reads its join secret file again** before each attempt to join, at most one a minute after a
  refusal, and presents the credential it holds, so it comes back as the member it was and a ban of it holds. A
  rotation followed by a revocation by version therefore needs the file replaced and nothing restarted.

## Moving a team's members

From 0.5.0 a team opens to a second secret for a time, the move ticket, so that a member's session follows a
team that was taken in with no command of the member's (work item 273; the plan's section 4.9). The contract
does not change: `/v1/team/join` carries a ticket as it carries the join secret, and no answer has a new
member. Recording the ticket on the backend the team leaves, and following it, are the plugin's.

- **`--open-move <team_ref> --ticket-file <path> [--ticket-days <days>]`** and **`--close-move <team_ref>`** are
  jobs of their own kind between a stop and a start, as the import is, and no request reaches either. The
  ticket is minted as a join secret is, `brg1.<team_ref>.<random>`, for 1 to 365 days, 30 unless told
  otherwise. Its file's place is held before the entry is appended, by the writer `--take-back` uses, and the
  file is the one place the ticket is.
- **Two records.** `team.move_opened` holds the ticket's SHA-256 and its end, a whole second;
  `team.move_closed` names the team. A team has one move: applying `team.move_opened` puts the ticket in the
  place of the one the team had. `Team.move_ticket` stays after its end, admitting nobody, until a record
  takes it away: the end is a time, and no record marks it.
- **The join.** `team_join` holds what was presented against the hash of the join secret and the hash of the
  ticket, both every time. A ticket admits while the request's time is before its end. Its join never makes an
  owner, whatever `may_own` says, because every member of the team on the backend it leaves can read the
  ticket. Its `member.joined` carries `by_move_ticket`, which is written only when it is true and is read by
  the journal's line alone: the membership is the one the secret makes, under the team's current secret
  version.
- **A rotation closes a move.** `/v1/team/rotate-secret` and `--take-back` append `team.move_closed` after
  `team.secret_rotated`, in the same entry, when the team has a move. Neither answer changes.
- **Snapshots are format 3.** This build reads formats 1 to 3, and writes a team that is not moving as format
  2 wrote it. A 0.4.x passes a format 3 snapshot over and refuses a log that holds one of the two records, so
  it never reads a team whose move is open as a team with none. Where no move was ever opened it starts from
  the newest snapshot of its own that is kept, or from the log's first entry, and writes format 2 again.

## The gateways

From 0.2.0 the mail and Slack gateways are part of the server (the plan's section 4.6), off until
`--gateway-config` names their configuration.

- **Two layers.** `brigade-gateway` is every rule and every word, ported from `supabase/functions/_shared/` and
  from the four handlers, and checked against that TypeScript by running both over generated inputs. A change to
  what a gateway says or decides is made there, with its test, and the Edge Functions' copy changes with it while
  the two exist. `brigade-server/src/gateway/` is the I/O and nothing else: `config.rs` reads the file (secrets by
  path), `state.rs` keeps the gateways' own records in `<data>/gateway/state.json`, `client.rs` makes one HTTPS
  request, `member.rs` is a gateway acting as the member it is, `presence.rs` joins and keeps its session,
  `slack_in.rs`, `slack_out.rs`, `mail_in.rs` and `mail_out.rs` are the four handlers, `turns.rs` the loop,
  `endpoint.rs` the two addresses, and `refusals.rs` what keeps a stranger's refused requests from filling the
  log: one line a minute for each kind, with the count.
- **A gateway is a member.** It joins a team with the join secret, registers one session (`slack-gateway` with
  harness `gateway-slack`, `mail-gateway` with `gateway-email`) and acts only through the engine's request path
  with its own credential. Nothing in `gateway/` reads or writes the state: a rule a member is held to holds a
  gateway.
- **The loop** receives for each gateway every two seconds and keeps its session with a heartbeat every minute.
  It does not listen to the engine's change signal, which its own heartbeat would move.
- **The endpoints** are outside `/v1/`: `POST /gateway/slack` (the Events API and the slash command, by Slack's
  signature) and `POST /gateway/mail/resend` or `/gateway/mail/postmark` (the provider's webhook, by its
  signature or its secret). Nothing of a request is parsed before it is authenticated, with one exception: a
  body of at most 4 KiB that is Slack's `url_verification` handshake is answered unsigned, which is what lets an
  operator create the Slack app. Postmark's secret is in a header or the path and is checked before the body is
  read. Each request runs in a task of its own, because Slack hangs up after three seconds and hyper drops the
  request of a caller that has gone; 64 run at once, and one more is answered 503 and delivered again.
- **Once, and never lost.** A delivery is written down as done only when it is: the engine took the message and
  the thread row is recorded, or the person was given their answer. While it is being carried its id is held in
  memory, and the same delivery arriving again is told to come later. So a stop or a crash at any point leaves
  either a finished delivery or one the provider delivers again, and the send's idempotency key (the provider's
  own id) keeps a second carry from being a second message. A stop waits for the carries that are running,
  within the drain it gives connections. A delivery that is dropped or ignored is not written down at all.
- **Where it departs from the functions.** A reply address carries a server's message id, which is not a UUID.
  The mark is written after the carry and not before it, as above. The mail gateway sends neither a bounce nor a
  command's answer to an address of its own (the inbox or a tag of it, the public address, the From), so an
  address that forwards to the inbox cannot make it answer itself; the functions guard the inbox only (work item
  271). A Slack request leaves out a team whose gateway is no longer its member and serves the others, saying
  that one team's sessions cannot be reached, where the functions fail the request for every team: on a server a
  team's owner can put its gateway out, by a rotation and a revocation by version (0.4.0). Everything else is the
  functions' behaviour, their oddities included: a Slack `429` is final, and a `to:` line on a reply starts a new
  thread.
- **Tests.** `tests/gateway.rs` starts the binary with a stand-in Slack and a stand-in mail provider on loopback,
  which the configuration's `api` members point the gateways at; the client speaks plain HTTP to loopback and to
  nothing else.

## Release it

The server is released apart from the plugin, by a tag of its own that `release.yml` never sees.

1. Set the version in `server/VERSION` and in `[workspace.package]` of `server/Cargo.toml` (a test holds the two
   equal), and refresh the lock file: `cd server && cargo update --workspace`.
2. Add the `## [<version>]` section to `server/CHANGELOG.md`: it becomes the release's text.
3. Land it on `master` with a green `server` job.
4. `make server-tag`. It checks the three places agree, that the tree is clean and is `origin/master`'s head, and
   pushes the tag `server-v<version>`.

The `server-release` workflow then builds the Linux binaries on Debian 12, so that they start on glibc 2.36 and
later, the macOS binary, and `server/Dockerfile` on a runner of each architecture; it publishes the binaries
with `checksums.txt` on a GitHub release that is not marked latest (the plugin's is), and the image as
`ghcr.io/appshapes/brigade-server:<version>` and `:latest`. Installing by crate needs nothing built: `cargo
install --git … --tag server-v<version> brigade-server --locked` reads the tag. The tags `server-v*` are protected
from deletion and from being moved, as the plugin's are.

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

There is no standby instance (phase 4).

## Known limits

Found by two reviews of the code on 2026-11-12 and left for a decision or a measurement:

- Nothing bounds the sessions a member registers, the principals a join secret or a move ticket makes or the
  teams a server holds. BAP/1 names `register_session` and `join_attempts` as reasons for `rate_limited`; the numbers are a
  product decision, and they weigh more now that anyone may create a team.
- An entry that fails its seal refuses the start, wherever it is in the log. Whether a power loss can leave such
  an entry at the feed's tail depends on Cavalry's recovery of a recording, which is not tested here.
- The seal has one key and no rotation. Nonces are random, which is sound for about four thousand million
  entries under one key: decades at a team's pace, and a number to revisit before then.
- A request nested more than 128 levels deep is refused, where the reference reads 10,000.
- An idempotency key is kept for eight days after its use, as long as its message can be, and not for the
  session's life as the filesystem adapter keeps it.
- Every entry appended wakes every receive that waits, and each asks the engine again.
