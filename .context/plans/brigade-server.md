# A Brigade server over Cavalry: a team that runs with no Supabase project (work item 89)

Written 2026-11-12 by the session `brigade-fix-1e`. It replaces `.context/plans/cavalry-adapter.md` (the
peer-to-peer design of 2026-11-11), which stays for the facts the Cavalry session's spike measured and for the
design this one grew out of. Where the two disagree, this page is the plan.

## 1. The goal

A Brigade team runs with no Supabase project and no cloud account. Its members' sessions reach a **Brigade
server** over HTTPS through an adapter bundled in the `brigade` binary, as they reach Supabase today. The team's
admins run the server on machines they own. The server keeps everything on the file system and uses Cavalry
(`../cavalry`) for its durable log and for replication between instances.

The yardstick is the green-field day one, as work item 156 has it:

- The Project Owner starts one server (a binary or a container) on an always-on machine and gives it a
  directory. Nothing else: no database, no account.
- The Team Admin runs `brigade team create --adapter rest …` against that server and commits the team file.
- Each member installs the plugin and runs `/brigade:join <path>`. Nothing on a member's machine changes shape:
  no daemon, no Rust, and Windows works.
- Each session has every feature: sessions, messages, doing, quiet, folder sync, the admin verbs, and email and
  Slack once the gateway process of work item 156 runs beside the server.

## 2. The owner's direction (Rjae, 2026-11-12)

These are his words from this session; the design in §4 is mine and built on them.

1. A server component. "Brigade would use TCP as it does today. The server would use Cavalry plus file system.
   Brigade team admins would be responsible for running one or more server instances."
2. "The server component would be a separate product, probably written in Rust. I envision admins choosing to
   run the server nodes on always-on machines, either on-prem or cloud."
3. Where it lives: "start in same repo and then move if that becomes desirable."
4. The file system, not a database: asked on 2026-11-12, I recommended the file system with the Cavalry log as
   the record of truth, and he said "Go ahead."
5. Members on Cavalry directly is wanted "only if this kept the plug-in at least as simple as it is today." It
   does not (§9), so it is not in this plan.

The six rulings of 2026-11-11 were made for the peer-to-peer design. What is left of them: an always-on machine
the team owns is acceptable (ruling 1, now the server); green field first, moving an existing team later
(ruling 6). Rulings 2 to 5 no longer apply: members need no shared network, the server issues credentials so
identity has authority again, the server's address is in the team file, and no Rust ships in the plugin.

## 3. Ground truth

**Brigade.** The adapter protocol is BAP/1 (`docs/protocol-v1.md`, frozen). An adapter is a child process per
command, JSON on stdin and stdout, with `message watch` as the one long-running command. The Supabase adapter
reaches its backend with HTTPS calls to `/rest/v1/rpc/` and `/auth/v1/`, and one websocket to
`/realtime/v1/websocket` for the watch. The fs adapter (`internal/adapters/fs`) implements every protocol rule
over files in Go and passes the conformance suite in `make test`. The team file (version 1) carries `adapter`,
`url`, `publishable_key`, `team_ref` and `team_name`; the bundled adapter is resolved by name in
`internal/harness/config/adapter.go`, and the harness may not import an adapter's package (depguard). The
shipped binary is Go with `CGO_ENABLED=0`, six targets, two of them Windows.

**Work item 156** (`.context/plans/swappable-backend.md`, nothing built yet) plans a REST contract, a bundled
`rest` adapter and a reference server in Go with a file store (its phase 3), a team file version 2 with a
`backend` object (phase 1), specified admin verbs and `session.facts` (phase 2) and the gateways as one Go
process over the protocol (phase 4). This plan builds its phase 3 with a different server, and leans on the rest.

**Cavalry**, from the Cavalry session's answers and spike of 2026-11-11 (`../cavalry/.ignored/cavalry-adapter/`,
at `0d28208`; the earlier plan's §3 has the table):

- Linux and macOS only. The embeddable driver is in the Rust API (`cavalry::Driver`); the crate is used with
  `default-features = false`, as a git dependency pinned to a commit until 0.1.0 (estimated 2026-12-14 to
  2026-12-28). Rust 1.89 or later, edition 2024.
- A recorded publication is a durable log. A replay with `FOLLOW_LENGTH` plays a recording from a position and
  then everything the recorder syncs, so one stream carries history and what follows.
- A `cavalry:udp` publication with no connected receiver refuses every offer, so a lone instance subscribes to
  its own feed until Cavalry takes appends with no receiver (its B5).
- Every run of the publisher is a new recording; the position keys keep one line of positions across runs, and a
  still-live earlier recording is stopped before reading where the feed stopped.
- Nothing on the wire is encrypted or authenticated, and a subscription must be reachable by its publisher. Both
  now matter only between instances.
- A log is 48 MiB of memory today (a 16 MiB term). A recording adds a sparse 128 MiB segment file. Nothing in the
  log's format is frozen before 0.1.0.
- The 2026-11-11 spike ran on one macOS host. On 2026-11-12, at `54ad5e0`, the Cavalry session ran three and
  five instances, each recording a feed and following every other, on macOS and in a Linux container (aarch64,
  loopback): 12 checks pass (its answers, section F). Not checked: a real network between hosts, a published port
  or NAT, x86-64, a real disk, a memory or CPU limit, a feed that crosses a term, a power loss.
- A write that is recorded and synced before it is answered took 0.5 ms in the Linux VM and 8.5 ms on the laptop
  at the median. `Recording::request_sync` lets many writes share one sync.
- A container's default `/dev/shm` (64 MiB) is too small and fails quietly: the server starts and every append is
  refused `not-connected`. Only `cavalry doctor` says why (Cavalry issue 238 is a related leak).

## 4. The design

### 4.1 Three pieces

1. **The contract**, `docs/backend-rest-v1.md`: the protocol's commands as HTTP requests. One request per
   command; the request body and the result are the protocol's own JSON shapes, and a failure is the protocol's
   error object. The server stamps identity, time and ids, as 4.5.5 requires of a backend. It is a reference
   document, versioned, and the only thing the adapter and a server share.
2. **The adapter**, `brigade adapter rest` (`internal/adapters/rest`, Go, bundled). It holds the local half: the
   profile and the credential file (0600), the secret read from stdin, input validation with
   `internal/protocol`, the mapping of HTTP and transport failures to the twelve error codes, and the watch loop.
   It links the standard library only, so `docs/allowed-deps.txt` does not change, and it builds for Windows.
3. **The server**, `brigade-server` (Rust, `server/`, its own Cargo workspace). One process that embeds the
   Cavalry driver. It holds every backend rule: teams, members, sessions and their leases, messages, quotas,
   retention.

### 4.2 The server: a state machine over one ordered log

- **A command becomes records.** A request is checked against the current state and, if accepted, turned into
  one or more records. The records are appended to the instance's recorded feed, synced to disk, applied to the
  state, and only then is the request answered. `message send` therefore means what 4.5.1 says.
- **The state is in memory.** A team's members, its live sessions and a week of unacknowledged messages are
  small. `apply(state, record)` is deterministic and has no clock and no randomness of its own: time and new ids
  are in the record. Any instance that reads the same records reaches the same state.
- **The feed is the record of truth.** One recorded `cavalry:udp` publication per instance carries the records
  of every team that instance serves.
- **Snapshots.** The server writes its state in its own versioned format, with the feed position it stands for.
  A start reads the newest snapshot and replays the feed from that position. Recordings older than the retention
  the operator sets are purged once a snapshot covers them. A change of Cavalry's log format before 0.1.0 is
  survived the same way: new feeds start from the snapshot.
- **Record format.** `{version, team, kind, at, payload}`, sealed with AES-256-GCM under a key the operator
  gives every instance in a file. The seal is for the link between instances, which Cavalry does not encrypt,
  and it lets a follower treat a record that fails as a hole to fetch again.
- **Credentials.** `team join` with the secret creates a member and returns an opaque token; the server keeps its
  hash, the adapter keeps the token in the credential file. The server therefore decides who is a member:
  leaving, revoking and rotating work without everyone rejoining, and the admin verbs can be enforced.
- **Transport security.** HTTPS. The server serves TLS itself or sits behind a proxy or a tunnel that does;
  plain HTTP is accepted on loopback only. A self-signed server is pinned by a fingerprint in the team file.

### 4.3 More than one instance: one writer, the others stand by

The protocol's rules need one order: one message per idempotency key, quotas that are not doubled, one process
draining an inbox. So one instance writes at a time.

- The **primary** takes every request. Each **standby** follows the primary's feed through a replay that
  follows, applies the same records, and keeps its own snapshot. This is the spike's pattern, on the kind of
  host Cavalry is built for.
- A standby that is asked answers `unavailable` and names the primary; the adapter, which has every instance's
  address from the team file, goes there.
- **Promotion** is an operator's command in the first version. The standby stops following, starts its own feed
  at the position it reached, and takes requests. What the primary accepted but had not yet sent is lost with the
  primary's disk; an option to answer only after a standby has the record closes that at the price of latency.
- Automatic promotion is Cavalry's cluster, which is being built for exactly this and is not usable yet (no
  restart on an existing log). The state machine here is written so that its log can become the cluster's.
- Links between instances are `cavalry:udp` on a network the admins control. Cavalry's missing NAT traversal and
  wire encryption apply to those links only.
- **The request to follow goes over HTTP between the servers**, since Cavalry has no request and reply. It is
  authenticated with the operator's key. The primary stops any replay still live to that standby's endpoint and
  stream, starts a new one and answers its session id; the standby reads that session's image only. Without this
  a standby that returns within 5 s is handed the old replay, already past its position (measured: it would never
  see five records). The request has three answers: following; purged, so take the snapshot; and ahead, which
  means a wrong generation or a lost disk.
- **The snapshot travels over the same HTTP path**, not over Cavalry: it must outlive a change of the log's
  format. A feed's name carries a generation, raised whenever feeds start again from a snapshot.

The Cavalry session's advice of 2026-11-12 agrees with this shape: feed-and-follow for now and for as long as one
or two instances are supported, with one writer by appointment, and an answer that waits for a standby as the
option. Its section F1 has the comparison with the cluster, which needs Cavalry's cards 228 to 232 and 0.1.0.

I told Rjae on 2026-11-12 that "any instance that is up can serve". That was wrong for this protocol, and §4.3
is what replaces it.

### 4.4 What a session gets

| Feature | How |
| --- | --- |
| Sessions, roster, leases | the server's state; one clock, the primary's |
| Messages, acknowledgement, retention | records in the feed; held for a recipient who is off |
| Watch | the adapter long-polls the server, so `message.watch.push` holds through a proxy or a tunnel |
| Team create, join, leave, members | the contract; a credential per member |
| Admin verbs | enforced by the server once work item 156's phase 2 specifies their shapes |
| Folder sync and the board | unchanged; the server stores `sync_peer` |
| Email and Slack | the gateway process of work item 156, phase 4, beside the server |
| Windows members | yes; only the server's host must run Linux or macOS |

### 4.5 The server's layout, for phase 2

```
server/crates/brigade-core/     the rules: wire shapes, records, the state, execute and apply. No I/O, no clock.
server/crates/brigade-log/      the ordered log behind one trait: a file log for tests, the Cavalry feed for real
server/crates/brigade-server/   the binary: configuration, HTTP, credentials' hashing, snapshots, wiring
```

- **`execute(&mut State, command, now, fresh) -> Result<Reply, Error>`** checks a request, applies its records to
  the state and hands the records to the caller, which appends and syncs them before it answers. A failed append
  ends the process: the state must never be ahead of the log. `fresh` is the randomness the command may need (new
  ids, a new credential, a new join secret), drawn by the caller.
- **`apply(&mut State, &Record)`** is what a restart and a standby run. It cannot fail and reads no clock.
- **Time.** The caller reads the clock once per request and passes `now`; a record carries `at`. A session's
  state is computed from `lease_until` and the reader's `now` (4.5.8). Retention and the rate windows are
  functions of record times, so the sweep needs no record of its own.
- **Records** (each `{version, kind, at, …}`): `team.created`, `member.joined`, `member.left`,
  `credential.issued`, `credential.revoked`, `session.opened`, `session.resumed`, `session.heartbeat`,
  `session.closed`, `message.accepted`, `message.acked`. A heartbeat is a record so that a standby has the leases;
  if the log's growth ever matters, heartbeats are the first thing to thin.
- **Waiting receives** are the HTTP layer's: a waiter per session, woken when a `message.accepted` for that
  session is applied.
- **The log trait**: `append(records) -> position` (returns after the sync), `read_from(position)` for a restart,
  and `follow(position)` for a standby. The Cavalry implementation is the spike's pattern
  (`../cavalry/.ignored/cavalry-adapter/spike/`): a recorded `cavalry:udp` publication under initial term id 0, a
  subscription to its own feed until Cavalry's B5 lands, the catalog read at start after stopping any recording
  still live, and positions taken from the image's arithmetic.
- **HTTP.** Thirteen fixed `POST` paths, bodies of at most 1 MiB, a few tens of connections, some of them
  waiting. Two candidate stacks for ruling 4. The small one: `httparse` for parsing, `rustls` with `ring` for
  TLS, blocking threads, and about 200 lines of strict framing of our own (a `Content-Length` is required, a
  `Transfer-Encoding` is refused, anything odd closes the connection). The usual one: `hyper`, `tokio` and
  `tokio-rustls`, about fifty crates and no framing code of ours. `ring` also does the sealing, the hashes and
  the randomness either way. The HTTP layer is kept thin so that the choice can change.
- **The oracle** is the conformance suite through `brigade adapter rest`, as for the stand-in. `brigade-core`
  also has its own tests, and the fs adapter's store is the worked example for every rule's detail.

## 5. The repository

Rjae's direction is to start here. The reason it is right is that the contract does not exist yet, and the
contract page, the adapter, the conformance cases and the server change together while it is written.

```
docs/backend-rest-v1.md          the contract
internal/adapters/rest/          the adapter (bundled)
internal/adapters/rest/stub/     a stand-in server for tests, over the fs adapter's store
server/                          the Rust workspace: Cargo.toml, Cargo.lock, deny.toml, VERSION, crates/
```

Three rules keep the later move cheap and Brigade's gates as they are:

1. **No shared code.** `server/` touches the rest of the repository only through the contract page and the
   conformance suite run through the `rest` adapter.
2. **Outside the acceptance gate.** `make test` and `make push` stay free of Rust: the Go side is tested against
   the stand-in. The server has its own make targets (`server-build`, `server-test`) and its own CI job with a
   cached build, held under five minutes, and the `master` ruleset changes in the same piece of work.
3. **Its own version, release and dependency list.** `server/VERSION` and tags of their own; a binary for Linux
   and macOS and a container image; `deny.toml` as the allowlist, since Brigade refused a Postgres driver on
   supply-chain grounds and a Rust HTTP and TLS stack brings many crates.

Move it when the contract has its first frozen version, or when the server needs its own release cadence, or
when its CI job cannot stay under five minutes, or when it has users and issues of its own.

## 6. What Cavalry is asked for

Nothing blocks. Two small asks are worth making before 0.1.0 freezes `spec/udp.md`, and whether they become
Cavalry cards is Rjae's to say (§10, ruling 8):

- **A term length per channel.** I called it nice to have; the Cavalry session disagrees, and its numbers are why.
  A log is 48 MiB today. With a 1 MiB term an instance among five holds 38 MiB, not 488 MiB, and an admin has
  nothing to size. Until it lands, the server's container needs `/dev/shm` set (the Cavalry session's formula for
  a full mesh of n is 8 MiB + 48 MiB × 4n), and **the server checks the room at start and refuses to start with
  a plain message** rather than fail quietly.
- **Appending with no receiver** (its B5) removes the self-subscription and one log.

Windows and the idle that sleeps are no longer Brigade's asks. The subscriber that calls out and wire encryption
come back together with any link between instances that crosses a NAT or a network the admins do not control.

What Cavalry wants from Brigade now (its section F5): the soak of a server that rolls and purges for months,
`cavalry doctor` in containers, and a paused VM as the server's closed lid.

## 7. Proof

- **The contract and the adapter:** the conformance suite green on `brigade adapter rest` against the stand-in,
  inside `make test`.
- **The server:** the same suite, with `--slow`, green against a running `brigade-server`; opt-in locally and
  the `server` job in CI. The Rust state machine has its own tests, and a restart test: stop the process, start
  it, and every unacknowledged message is still there.
- **A real team:** `make e2e` against the adapter and the server, then a green-field team of two members for a
  week, with the server's log directory and its counters as the measurement.
- **Standby:** two instances on two hosts; send through the primary, stop it, promote, receive from the standby.

## 8. Phases

| Phase | What | Needs |
| --- | --- | --- |
| 0 | Research, the spike, the direction. Done 2026-11-12. | |
| 1 | The contract page, the `rest` adapter, the stand-in, conformance green in `make test`. Go only. Built and verified 2026-11-12 (execution log P86-1): conformance(rest) 48 passed, 0 failed, 1 slow case skipped; 49 of 49 with `--slow`. | nothing |
| 2 | The server with one instance: the workspace, records, the state machine, the recorded feed, snapshots, HTTP, credentials; the conformance suite green against it; the `server` CI job. | phase 1; rulings 3, 4 |
| 3 | A real team: `team create --adapter rest`, the team file, the session-start lines, `make e2e`, the Project Owner and Team Admin pages, the `security.md` section, the first server release, the experimental label. | phase 2; rulings 2, 5 |
| 4 | Standby instances: following, snapshots for a new instance, promotion, the adapter's second address (work item 156's team file version 2), the two-host proof, a week's soak. | phase 3; ruling 1 |
| 5 | Email and Slack beside the server: work item 156, phase 4. | independent |
| Later | Automatic promotion on Cavalry's cluster; moving an existing team; whether this becomes the default; a repository of its own. | |

Phase 1 is useful whatever happens to the rest: it is work item 156's contract and adapter, and any server in
any stack can stand behind it.

## 9. Not in this plan

- **Members on Cavalry.** A Cavalry client needs a driver on its own machine, the Go client needs cgo, and
  Windows would be lost. A member who wants it can run an instance and point their adapter at loopback; the
  plugin does not change.
- **A database.** Not an external one, ever, in this plan. The state sits behind an interface so an embedded one
  can hold it later if search over history is wanted or the state outgrows memory.
- **Any change to BAP/1's wire shapes,** or to the Supabase path for the teams on it.
- **End-to-end sealing.** Whoever runs the server can read the team's messages, as the owner of the Supabase
  project can today.

## 10. Rulings wanted (Rjae), each with the default I work to until told otherwise

1. **One writer at a time** (§4.3), with promotion by an operator's command first. Default: yes.
2. **Who may create a team on a server.** A stranger who can reach it should not. Default: a token the operator
   issues, read from a file or stdin, never argv.
3. **TLS.** Default: the server can serve TLS itself, plain HTTP on loopback only, and a self-signed server is
   pinned in the team file.
4. **The server's dependencies.** An allowlist in `server/deny.toml`; the HTTP and TLS crates are named on it
   before phase 2 writes code against them.
5. **The `server` CI job and the ruleset.** Default: the job runs from phase 2 and becomes required when phase 3
   ships.
6. **The stand-in.** Default: a test fixture only, never shipped.
7. **Work items.** Default: 89 carries phases 1 to 4; 156 keeps the team file version 2, the protocol additions
   and the gateways, and a comment there says its phase 3 is built here.
8. **Cavalry's two small asks as cards now** (§6), and whether Brigade's team is invited as an early tester of
   Cavalry before 0.1.0. No default: these are in Cavalry, and nothing here waits on them.
