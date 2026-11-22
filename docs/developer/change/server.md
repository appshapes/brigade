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
   own CI job, `server`, which the `master` ruleset requires since the first server release.
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
`--slow` on the Cavalry feed. With it runs one test of the two built programs together that no suite can
make: `bin/brigade`'s `rest` adapter takes a team in through a front that loses the server's answer, and the
same command run again owns the team and opens its move
(`a_take_in_whose_answer_is_lost_is_run_again_by_the_built_adapter_and_owns_the_team`). Beside it, `crates/brigade-core/tests/rules.rs` holds the same rules at the size of a
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
once, the rest within five seconds) and then writes its snapshot. A stop is listened for before the server says
that it is up: the address file and the `listening on` line come after the SIGTERM and SIGINT streams are made,
so a stop at the first sight of either is a stop (work item 374; `a_stop_at_the_first_sight_of_the_address_is_heard`
in `tests/server.rs` holds it). Keep that order when the start changes. A stop that comes after the streams are
made and before the server is up ends the start where it stands, with `stopping: the stop came before the
server was up` and neither the file nor the line (`before_a_stop` in `main.rs`). Before the streams, while the
log is read, the signal ends the process at once: nothing listens yet and nothing has said that anything does.

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

From 0.7.0 a team's move from a Supabase project is one command of its administrator's,
`brigade team move --to <url> --secret-file <path>`, whose three requests a running server answers
([Taking a team in and moving it by request](#taking-a-team-in-and-moving-it-by-request)). The operator's
commands of this section and of [Moving a team's members](#moving-a-teams-members) are the older way, and stay.

`--import-team <name> --join-secret-file <file>` writes one `team.created` record for a team that exists
elsewhere: the reference inside the secret, the name, the SHA-256 of the whole secret, and no creator and no
member. A join checks that hash against the team the secret names, so the team's members join with the secret
they have. It is a job of its own kind for the engine, appended and synced like any other, and not a request:
no path of `answer` reaches that kind. It runs between a stop and a start,
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
- **The owner** is `Team.created_by`. A team that the operator took in has none until a join whose request
  `may_own`: the HTTP listener passes true, and the gateways' join inside the server passes false. A team that
  was taken in by request has the caller that took it in.
- **`--set-owner <team_ref> --principal <principal_ref>`** is a job of its own kind between a stop and a start,
  as the import is, and no request reaches it. **`--take-back <team_ref> --secret-file <path>`** is another: one
  entry of a rotation, a revocation by version of everyone and an owner change to nobody, so that the first
  member to join with the new secret owns the team. The file's place is held before the entry is appended.
  **`--remove-team <team_ref>`**, from 0.9.0, is the third ([Removing a team](#removing-a-team)).
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
  jobs of their own kind between a stop and a start, as the import is, and no request reaches those kinds. From
  0.7.0 the team's owner asks for the same by request (the next section). The
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

## Taking a team in and moving it by request

From 0.7.0 the contract's section 3b is three requests like any other (work item 328, ADR 0006): a server that
runs takes a team in for the caller that presents its join secret, and opens and closes a move for the team's
owner. The operator's three commands stay as they are, and call the same functions.

- **`/v1/team/take-in`** is an open path, as a creation and a join are, and a creation path:
  `Command::creation_path` is what `answer` holds to `may_create`, once the body is known to be one JSON
  object and before any member of it is read. `wire.rs` holds the reference inside the secret to
  `limits::MAX_TAKEN_IN_TEAM_REF_CHARS`, because the caller chose it and the state keeps it. `team_take_in` in
  `execute.rs` has two branches. A team the state does not hold is taken in by one entry: the credential of a
  new principal, `team.created` with the reference inside the secret, the secret's hash and the caller as
  `created_by`, and `member.joined`. A team the state holds is handed to `team_join` as it stands, so every
  rule of a join holds without a second copy of it, and what `team_join` refuses for its secret is answered
  `conflict` with `other_join_secret`, the refusal of `import_team`.
- **`may_own` is read by a take-in too.** A caller whose request may not own takes a team in with no owner, as
  the operator does. Only a gateway's request may not, and no gateway takes a team in.
- **`retry_key`** is read last by `wire.rs`, into a `RetryKey` that prints as nothing of itself, and only its
  form is held there. `execute.rs` hashes it before it looks at the team. The hash goes on `member.joined`
  (`retry_key_hash`, written only when it is not empty) when the request made a principal and made it the
  team's owner, in either branch, and applying that record sets `Team.take_in_retry` to the hash and the
  principal, and `Principal.retry_key_in` to the team: the way back from a record that names the principal.
  The team's member is the one fact of a key and all a snapshot holds of one. The principal's is never
  written and never read: `State` is read through `Written`, which reads it off the teams
  (`find_the_ways_back`), so a snapshot of any build starts a server on which the owner's own record ends
  the key.
  `repeated_take_in` tells a take-in made again: no credential the state knows, the team's current secret
  and the kept hash. `join` then acts for that principal, and `reissue_credential` writes a
  `credential.revoked` for every credential that names it and one `credential.issued`, in the entry of the
  join, whose `member.joined` carries the hash again. Nothing in the answer of any other take-in differs by
  whether the team keeps a key.
- **The key ends in `State::apply`**, with the first record that its principal's own request makes. A record
  that names the principal as the one who acted ends the key that principal has: `session.opened`,
  `member.left`, a `member.joined` with no hash, and `credential.revoked`, which a take-in made again writes
  too, so that entry's join keeps the key once more. A record that only the team's owner is answered for ends
  the key the team keeps: `member.revoked`, `members.revoked_by_version`, and `team.move_opened` and
  `team.move_closed` when they say `by_owner`, which the owner's request writes and the operator's command
  does not. `team.owner_changed` to another principal and `team.secret_rotated` end it as well. So a team
  keeps a key for its owner alone, the same request after the end is a stranger's join, and nothing that
  leaves no record, a read or a refusal, ends anything. The five records of a session (`session.resumed`,
  `session.heartbeat`, `session.closed`, `message.accepted`, `message.acked`) have no arm, because none can
  be an owner's first: a principal that a team keeps a key for has no session, and the request that gives it
  one ends the key. A new request of the owner's that leaves a record of another kind
  needs its arm here. `rules.rs` holds that from both sides: one test asks every request the server reads
  with the credential of an owner whose key is kept, and does not build when a request is added that it does
  not name; and the walk of twenty thousand requests holds at every step that such an owner has no session
  and is in no other team, names every kind of record in a match that does not build when one is added, and
  starts again from a snapshot and the log after it every 199 steps.
- **`taken_in`** is one member more of `team.created`, written only when it is true and read by the journal's
  line alone, as `by_move_ticket` is on `member.joined`. The team is the team a creation makes, so there is no
  new kind of record and no new snapshot format: a 0.6.0 starts on what 0.7.0 wrote.
- **`/v1/team/open-move` and `/v1/team/close-move`** are administration paths: `answer` checks the owner before
  it reads anything of the body but its `team_ref`, and each function checks it again before it calls
  `open_a_move` or `close_a_move`, which the operator's `open_move` and `close_move` call as well: the
  owner's request passes `by_owner`, which the record then says, and the operator's command does not.
  `ticket_days` is read by `wire.rs` with the bounds `open_a_move` holds.
- **A team that is taken in counts** against the principal that takes it in, in `teams_created_at`, because
  applying `team.created` counts it for its `created_by`.
- **`said.rs`** has a line for a team taken in, for a move its owner opened and for one its owner closed. A
  `team.move_closed` in an entry that also holds a rotation is the rotation's; alone it is the owner's. A
  `member.joined` in an entry that makes a credential unknown and issues one is a take-in made again, and has
  a line of its own in the place of the join's. Such an entry always has a credential to make unknown: a
  principal that gave its own up ended its key by that.
- **`/v1/info`** lists `team.take_in` beside `team.admin`, and the conformance suite holds the three with cases
  C-53 to C-55 through the `rest` adapter.

## The record of where a team went

From 0.8.0 the contract's section 3c is two requests like any other (work item 341, ADR 0010): a server keeps the
record of where a team went, which until then only the Supabase and the filesystem backends kept, so a team on a
server is moved to another with the one command and its members follow. The sections above are the server a team
moves **to**; this one is the server a team **leaves**. BAP/1 does not change.

- **`/v1/team/move`** is an administration path (`Command::administration_path`): `answer` holds the caller to
  `owned_team` before any member of the request but its `team_ref` is acted on, so every other caller reads one
  refusal for a record, for a clear and for a request that reads as neither. What `answer` tells before that is
  the same for every caller and says nothing about any team: a body over the cap and one that is not one JSON
  object are `invalid_input`, a caller with no credential the server knows is `unauthenticated`, and a
  `team_ref` that is missing, empty or no string is `invalid_input`. **`/v1/team/moved`** is a team-scoped
  path, as a roster is: `member_team`, then the read. Neither is answered without a credential.
- **Two forms** of `/v1/team/move`, read in `wire.rs` in the order the stand-in reads them: `clear`, which when
  `true` goes with no `adapter`, `url`, `certificate` or `ticket` that is not null, while `false` and null are
  no `clear`; then the adapter, the url and the certificate within BAP/1's caps, then the ticket. A ticket is a
  `JoinSecret` that carries the request's `team_ref`, within `limits::MAX_MOVE_TICKET_CHARS`; what is not is
  `Error::not_a_move_ticket`, fixed words that name `ticket` and spell no format of a join secret: the
  adapter's redactor takes that format for a secret, and the person would read the hint as `[redacted]`.
- **Two records.** `team.move_recorded` holds the destination and the ticket; `team.move_cleared` names the
  team. Applying the first puts `Team.moved_to` in the place of the record the team had, with the record's own
  time as `recorded_at`; applying the second takes it away. A clear of a team that has no record makes no entry.
  Both end the retry key the team keeps for its owner (`State::apply`): only the owner is answered the request.
  No request reaches a kept key through a clear, because a team that keeps a key holds no record, and the rule
  is the same all the same.
- **The record changes nothing else.** No arm of `execute.rs` reads `Team.moved_to` but the two requests: a
  join, a registration and a send are answered as before. `/v1/team/rotate-secret` and `/v1/team/transfer`
  leave it. `take_back` appends `team.move_cleared` after the rotation, in the same entry, when the team has a
  record, and its answer then carries `move_cleared: true`, which the command's line says; a team with none is
  answered as it always was.
- **The ticket is kept whole.** It is the one secret that is not a hash: an active member is handed it. Its
  type, `record::KeptTicket`, serialises as the text and prints as `[redacted]`, so the debug rendering of a
  record, an entry and a state holds none of it, and the one way to its text is `expose`. `said.rs` writes the
  destination, cut and cleaned as a label is, and never the ticket. Keep both true: a new line, a new `Debug`
  and a new log call are where it would leak, and `rules.rs`, `said.rs` and `tests/server.rs` each look for the
  ticket's bytes.
- **Snapshots are format 3 or 4, by what they hold** (`snapshot::format_of`). A state with a `moved_to` is
  written as format 4. A state with none is written as format 3, in the bytes 0.7.0 writes: `Team.moved_to` is
  skipped when it is `None`. This build reads formats 1 to 4.
- **When an older binary stops being able to start.** Until a team's owner records a move, 0.7.0 and 0.8.0
  start on each other's directories. From the first `team.move_recorded`, a server before 0.8.0 does not start:
  it passes a format 4 snapshot over and refuses the log's record as a kind it does not know. Once every record
  is cleared and a snapshot has been written since, which a stop does, the newest snapshot is format 3 again and
  the older binary starts on it without reading the entries before it. Measured with the released 0.7.0 on
  2026-11-15 (`server/CHANGELOG.md`, section 0.8.0). A new member of `Team` that an older build must not drop
  without a word is a new format; one it may drop is not (the retry key, the bounds).
- **`/v1/info`** lists `team.move` beside `team.admin` and `team.take_in`, and the conformance suite holds the
  two with cases C-51 and C-52 through the `rest` adapter. The server's test of the suite names both cases.
- **The walk** in `rules.rs` chooses among the requests by a fixed seed, and asserts at its end that every
  rule was reached. Adding a request to its list is another walk: when the assertions name a rule it did not
  reach, give it a seed from which it does, as work item 341 did.

## Removing a team

From 0.9.0 the operator removes a team with everything the server holds of it (work item 332, ADR 0011). It is
the way to free a reference that was taken in by someone who is not the team, which a server that asks for no
creation token allows anyone: the team's own take-in is then a `conflict`, and `--take-back` would give the team
a secret its members do not hold. The contract does not change, and no request reaches the command.

- **`--remove-team <team_ref>`** is a job of its own kind between a stop and a start, as the import is
  (`Work::RemoveTeam`, `brigade_core::remove_team`). It refuses a team the state does not hold with
  `Error::no_such_team`, and the reference is compared as it was typed: nothing trims it, on the command
  line, in `main.rs` or in the rules, and a test holds each of the three. Its answer is what the command's
  line says: the team's name, the number of its active members, its owner with the label the roster showed,
  and `move_recorded` when a record of where the team went was there.
- **Two refusals are this command's alone**, because it cannot be undone. `options.rs` refuses the option
  given twice, where every other option keeps the last value it was given. And `main.rs` asks
  `engine::holds_a_record_key` before it locks or opens anything: the other commands make the data directory,
  with a key and a log, as a first start does, and this one makes nothing in a directory that is no server's.
- **One record**, `team.removed`, which names the team and holds nothing else of it: what goes is what the
  state holds of the team when the record is applied, as with a revocation by version. Applying it takes the
  team out of `State.teams`, and with it its memberships, sessions, messages, `move_ticket`, `moved_to` and
  `take_in_retry`. From the team that was taken out it then takes each of the team's own sessions out of
  `State.session_teams`, with no pass over the server's other sessions, and clears `Principal.retry_key_in` of
  the principal the team kept a key for. `State.principals` and `State.credentials` are left as they are: a
  credential names a principal that may be a member of other teams, as a revocation leaves them (the plan's
  section 4.8).
- **What a member is answered afterwards** takes no code. `member_team` refuses a team that is not there as it
  refuses a membership that is not, and `owned_session` tells a principal that is a member of nothing
  `unauthorized`, and one that is still a member of another team the uniform `not_found`.
- **No snapshot format.** The state after a removal holds nothing of the team, so `snapshot::format_of` reads
  as before: 3, or 4 when another team records a move. A 0.8.0 therefore starts on a directory on which a team
  was removed whenever the newest snapshot was written after the removal, which the command's own end does,
  and holds the state this build holds; where it has to read the entry, it refuses it as a kind it does not
  know. Measured with a build of 0.8.0 on 2026-11-15 (`server/CHANGELOG.md`, section 0.9.0). A record that
  leaves nothing in the state needs no format; one that leaves a member an older build must not drop does (the
  section above).
- **The gateways.** A gateway that was a member goes with the team, and nothing under `gateway/` is told. One
  whose configuration still names the team finds itself refused when it takes its place at the start, joins
  again and is refused, and says both (`turns.rs`). Its row in `<data>/gateway/state.json` stays, with its
  credential, and brings it back as the principal it was once a team of that reference is there again. In
  the stranger's case the configuration names the real team, whose gateways hold no row: their first join is
  refused for as long as no team of the reference opens to their secret, said in one line by the reference,
  and asked again each minute, so they join by themselves once the real team is there.
- **The line** is `main.rs`'s (`remove_team`), written through `note` as the other commands' lines are.
  `said.rs` has none: no request makes the record.
- **Tests.** `rules.rs` has the removal's two tests, and the walk removes teams among its requests: where a
  team is gone it holds that the entry is the removal alone, that no session is in the team, and that there are
  as many ways back to a key as keys. `tests/server.rs` has the stranger's take-in, the refusals, the removal,
  a start from the command's snapshot and a start that reads the entry from the log, on both logs, and what
  keeps a freed reference for its team. `tests/gateway.rs` has a removed team whose gateways were its members
  and are still configured, and the stranger's case with the real team's gateways configured; `turns.rs` has
  that case with the turns in the test's hands, for the minute a process test would have to wait.

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
2. Add the `## [<version>]` section to `server/CHANGELOG.md`, or give that heading to its `## [Unreleased]`
   section, where work that landed between releases waits: it becomes the release's text. `make server-tag`
   refuses while an `## [Unreleased]` section is left, so that a release's text carries all the release does.
3. Land it on `master` with a green `server` job.
4. `make server-tag`. It checks the three places agree, that the tree is clean and is `origin/master`'s head, and
   that `ci.yml`'s run on that head is green, waiting for one in progress; then it pushes the tag
   `server-v<version>`. Over a red or missing run it stops, and `release_over_ci=<run id|missing:sha>` is the owner's
   override ([The two CI checks](../release/cut-a-release.md#the-two-ci-checks)). `DRY_RUN=1` stops before the tag.

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
  Cavalry is frozen before its 0.1.0.
- A new pin has to open, in place, the directory the pin before it wrote. Cavalry's own gate
  (`conformance/upgrade/log-directory.sh`, Cavalry's work item 362) has one older tree write a directory and
  holds that Cavalry's `master` opens it; on 2026-11-22 that older tree is this pin, `620a2e29`. Before a move
  of the pin, check that the tree Cavalry's gate writes from is the pin being left: it covers no other.
  Brigade's gate has no such test yet (work item 387).
- A pin that cannot open the directory is not moved to, no release is made on it, and operators stay on the
  release they have. No command starts a log again from a snapshot, and none is built until a change of the
  format needs one (ADR 0016, the owner's ruling of 2026-11-22; the pages and the plan promised one until then,
  and a start has only ever read the log from a snapshot's position).
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

- One principal is bounded from 0.6.0 (ADR 0004; `brigade-core`'s `limits.rs`): five teams created and 120
  sessions opened in an hour, both counted in the state from the log's record times, and five failed joins in
  fifteen minutes, counted in the process's memory by `attempts.rs` because a refusal makes no record; a
  gateway's join, the one that may not own, is not counted. A caller
  with no credential is a new principal each time and is held by none of them, and nothing bounds the
  principals a join secret or a move ticket makes or the teams a server holds: a limit by address is the
  proxy's.
- A team that moved away stays on the server it left, with its messages and its gateways, until the server's
  operator removes it there (`--remove-team`, from 0.9.0; ADR 0011): no request ends a team, and its owner
  cannot (ADR 0010). The record of where it went is followed one move at a time: a member whose machine missed
  a move does not follow the next.
- Nothing bounds how often a team's owner records where the team went: ADR 0004 counts no record, each one is
  an entry of the log, of 17,649 bytes at the caps (measured on the file log on 2026-11-15), and only the state
  keeps one. A bound needs a `rate_limited` reason that BAP/1 4.6 does not list and a count in the state, so
  work item 341 built none.
- On a server that asks for no creation token, a stranger can take in a team under a reference that is not
  theirs, with a secret of their own, and the real team's take-in then answers `conflict` (ADR 0006;
  `docs/security.md`, section 20). A creation token keeps a stranger from it. From 0.9.0 the operator removes
  such a team (ADR 0011), and the reference is then free for the stranger too, until the server asks for a
  token or holds the real team.
- What a removed team held stays in the data directory after the removal: in the snapshots that are still
  kept, until two later ones have replaced them, and in the log's earlier entries, which the file log keeps for
  good and the feed lets go with its recordings. Nothing rewrites a log.
- A take-in whose answer is lost is made again with its retry key and owns the team (ADR 0006). Without the
  key, from a client that sends none or a machine that lost the file, the request made again joins as a
  member, and the operator names the owner with `--set-owner`.
- A take-in made again makes the credential before it unknown. A request that is delivered a second time
  after its client kept the first answer, by a proxy that sends it again, and before the owner's first
  request that leaves a record, leaves that client with a credential the server does not know and no key
  file (ADR 0006). After that record the key has ended, and such a request is a stranger's join.
- An entry that fails its seal refuses the start, wherever it is in the log. Whether a power loss can leave such
  an entry at the feed's tail depends on Cavalry's recovery of a recording, which is not tested here.
- The seal has one key and no rotation. Nonces are random, which is sound for about four thousand million
  entries under one key: decades at a team's pace, and a number to revisit before then.
- A request nested more than 128 levels deep is refused, where the reference reads 10,000.
- An idempotency key is kept for eight days after its use, as long as its message can be, and not for the
  session's life as the filesystem adapter keeps it.
- Every entry appended wakes every receive that waits, and each asks the engine again.
