# A Brigade server over Cavalry: a team that runs with no Supabase project (work item 89)

Written 2026-11-12 by the session `brigade-fix-1e`. It replaces `.context/plans/cavalry-adapter.md` (the
peer-to-peer design of 2026-11-11), which stays for the facts the Cavalry session's spike measured and for the
design this one grew out of. Where the two disagree, this page is the plan.

Corrected 2026-11-12 by the session `89-brigade-server-over-cavalry`, which built phase 2: the owner's two
rulings of that day (§2, §4.2, §4.5, §10), Cavalry's cards 271 and 272 (§3, §6, §10), and what was built and
measured (§4.5, §7, §8).

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
6. TLS (ruling 3 of §10), 2026-11-12: "Yes, but [I'd] like an option for an admin to put TLS termination before
   the server". To the shape proposed for that option he said "Agree with recommendations.": the server refuses
   to start in plain HTTP on an address that is not loopback unless the operator sets an explicit option saying
   that TLS ends in front of it, and the adapter still requires https unless the host is loopback.
7. The server's HTTP and TLS crates (ruling 4), 2026-11-12: "my intention is that installation and delivery of
   the server is separate from the plug-in. Wouldn't that eliminate Brigade refusal? And if so, [I'd] prefer the
   'usual one' option." So the stack is hyper, tokio and tokio-rustls. The allowlist in `server/deny.toml` stays
   ("Agree with recommendations.").

8. Who may create a team on a server (ruling 2), 2026-11-12, asked whether it should stay that anyone who can
   reach a server started without `--create-token-file` can create one: "Anyone". So a server is open to team
   creation by default, as phase 2 built it. He was not asked about the option itself and did not speak of it: it
   stays, for an operator who wants a token, and the contract already says a server may require one.
9. Landing phase 2 on `master`, 2026-11-12: "Yes".

10. How sessions work from here, 2026-11-12: "I want you and all future sessions to run autonomously, using your
    own recommendations when decisions are needed, and that includes releasing. When you truly require my input,
    pause and ask me."
11. The first server release, 2026-11-12: "Agreed" to version 0.1.0 tagged `server-v0.1.0`, binaries for Linux
    (x86-64 and aarch64) and macOS (aarch64) on a GitHub release, and the image at
    `ghcr.io/appshapes/brigade-server`. And: "Can users also install via crate? I think that is how most admins
    will want to install." So `cargo install --git … --tag server-v0.1.0 brigade-server --locked` is a documented
    way from the first release; `cargo install brigade-server` from crates.io waits for Cavalry there, because
    crates.io refuses a crate with a git dependency.
12. The `server` CI job becomes required when phase 3 ships (ruling 5): "Agreed."
13. The `security.md` section for the server: "No need to show me, understood."
14. Limits (the flood bounds the Supabase backend has): "Agreed. But a reinforcing reminder: Brigade is open by
    design, and only closed by choice/configuration." So nothing closes by default: no cap on teams or members,
    and what closes is a switch an operator turns on.
15. `--create-token-file`: "Keep."
16. Rulings 1, 6, 7 and 8 of §10, 2026-11-12: one writer at a time with promotion by command (asked whether that
    blocks automatic promotion on Cavalry's cluster later, the answer given was no: the log sits behind one
    interface and the cluster's log goes behind it); the stand-in stays a test fixture, "Agreed"; the work items
    as proposed, "Yes"; an early tester of Cavalry, "Yes". He has other hosts for a standby.
17. The proof with a real team, 2026-11-12: "I want to move Brigade repo over since it is only two devs. As part
    of this, I want to switch Brigade to the `appshapes` team - which Cavalry and all other appshapes repos are
    currently on. […] In short: no burn-in, we go straight to live testing using Brigade. I have a clean,
    dedicated host in mind (`sled`)." Asked whether the whole team moves at once: "agreed, A", the whole team.
18. The gateways, 2026-11-12, told that a team on a server has neither Slack nor email (both are Supabase Edge
    Functions): "if you are confident that the gateways are a **fast** follow then it can wait, but if not then
    we need to wait for the gateways. also, why can't the gateways ship as part of the `server`? if it's a
    best-practice thing, then i would side on bundling together to ease the lift on Brigade admins." The session's
    answer: Slack is quick, receiving email on a host that is not on the public internet is not sure, so the team
    moves after the gateways; and they are built into `brigade-server` (§8, phase 3b).

19. The team keeps its join secret, 2026-11-12, said in another of his sessions (`brigade-89`) and recorded on
    work item 272: the `appshapes` team moves to the server "keeping its existing join secret if that is
    technically feasible"; "the whole point of an adapter is to be able to swap it without affecting the
    clients". He cannot have members join again with a new secret, and the thinktech team will move the same way
    later. So the team is not made with `team create --adapter rest`: the server takes it in (§4.7).
20. The same day, there: the repositories on that team on the server are brigade, cavalry, muster and telder, and
    the mail gateway's address is `appshapes-team@appshapes.com`. Work item 272 carries the move of the
    repositories, the board and the members; this plan carries the server's side.

21. One roadmap, 2026-11-12, said in `brigade-89`'s conversation and recorded on work items 272, 273 and 274:
    work item 156 is split and closed, into 273 (a team moves keeping its join secret, then with no action by
    its members) and 274 (the admin verbs on the server); and work items 89, 273, 274 and 272 "should be
    considered one atomic roadmap by the 89 session", sequenced here so that the cut-over happens once. And:
    "Once we get to the point of being able to start 272, I must be notified." Section 11 is that roadmap.

Items 6 and 7 were said in `brigade-fix-1e`'s conversation. They reached the session that built phase 2 as that
session's relay, with his words quoted, and are on work item 89 (its comments of 2026-11-12, 10:16 and 10:21 UTC).
Items 8 to 18 were said to the session that built phases 2 and 3, in answer to its reports and questions.

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
- 2026-11-12, the Cavalry session's report (its answers, section G, and its message to the phase 2 session): both
  asks of §6 are Cavalry cards now, 271 (`ssc=true` on a feed's channel: a live recording connects the
  publication) and 272 (`term-length=<length>`). They were built on branches and not on Cavalry's `master`, which
  was `620a2e2`, when phase 2 was built; at that commit a channel that names either key is refused. On
  `term-length=1m|ssc=true` it measured one instance at 11.0 MiB of driver directory and 22 MiB resident, with a
  longest message of 131,072 bytes. JSON can write a character as six bytes, so a sealed record of a 16,384-byte
  body is up to about 104 KiB: `1m` fits it and `256k` or `512k` does not.

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
- **Transport security.** HTTPS (ruled 2026-11-12, §2 item 6). The server serves TLS itself when it is given a
  certificate and its key. Without them it serves plain HTTP on loopback only, unless the operator sets
  `--tls-in-front`, which says that a proxy or a tunnel ends TLS in front of it; plain HTTP on any other address
  is refused at the start. What stands in front must pass the `Authorization` and `Brigade-Create-Token` headers,
  allow a 1 MiB body, hold a quiet request open for more than 30 seconds and never send a `POST` again by itself.
  A self-signed server is pinned by a fingerprint in the team file (phase 3: the member's side is not built).

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

- **`answer(&mut State, Request, now, &mut dyn Entropy) -> Result<Outcome, Error>`** takes a request's path and
  body, checks it in the stand-in server's order (the credential before the shape, and nothing of the body read
  beyond its members' names until the credential is known), applies its records to the state and hands them to
  the caller with the answer (`Outcome`), which appends and syncs them before it answers. A failed append ends the process: the state must never be ahead of the log.
  `caller` is the hash of the credential the request presented, and `Entropy` the source of what the command may
  need (new ids, a new credential, a new join secret), which the caller owns: the operating system's in the
  server, a counter in a test.
- **`apply(&mut State, &Record)`** is what a restart and a standby run. It cannot fail and reads no clock.
- **Time.** The caller reads the clock once per request and passes `now`; a record carries `at`. A session's
  state is computed from `lease_until` and the reader's `now` (4.5.8). Retention and the rate windows are
  functions of record times, so the sweep needs no record of its own.
- **Entries.** The records one request made are one entry of the log, `{sequence, records}`, sealed as one: the
  log holds all of a request's records or none. Entries are numbered from one inside the seal, and a start
  refuses an entry that is not the one after the last it applied, so a log read wrongly is never believed.
- **Records** (each `{version, at, kind, …}`, the team a member of the kinds that have one): `team.created`,
  `member.joined`, `member.left`, `credential.issued`, `credential.revoked`, `session.opened`, `session.resumed`,
  `session.heartbeat`, `session.closed`, `message.accepted`, `message.acked`. A heartbeat is a record so that a
  standby has the leases; if the log's growth ever matters, heartbeats are the first thing to thin. No entry is
  longer than 104 KiB before its seal: the server refuses to start on a log that takes less, and it caps the two
  members BAP/1 leaves uncapped (`harness`, `harness_version`) at 256 code points, which the contract's section 5
  now allows.
- **Waiting receives** are the HTTP layer's: a waiter per session, woken when a `message.accepted` for that
  session is applied.
- **The log trait**: `append(records) -> position` (returns after the sync), `read_from(position)` for a restart,
  and `release(position)`, which tells the log what the kept snapshots cover. A standby's `follow` comes with
  phase 4: its request travels over HTTP and names a session (§4.3), so its shape is that phase's to fix. The
  Cavalry implementation is the spike's pattern (`../cavalry/.ignored/cavalry-adapter/spike-term/`): a recorded
  `cavalry:udp` publication under initial term id 0, a subscription to its own feed until a pin has Cavalry's
  card 271, the catalog read at start after stopping any recording still live, a restart's read through a replay
  over `cavalry:ipc`, and positions taken from the image's arithmetic. One constant, `Shape` in `feed.rs`, holds
  what differs between pins. A client that was closed under an append (a process held still for more than ten
  seconds) is connected again, and only the records the feed does not hold are offered again. When a snapshot is
  taken and the live recording holds `--feed-roll-bytes` or more, the feed is rolled, and the recordings that end
  before the oldest kept snapshot are purged.
- **HTTP.** Thirteen fixed `POST` paths, bodies of at most 1 MiB, a few tens of connections, some of them
  waiting. The stack is hyper on tokio, with rustls on ring through tokio-rustls (ruled 2026-11-12, §2 item 7),
  and no router framework. `ring` also does the sealing, the hashes and the randomness. The HTTP layer is thin:
  it reads the path, the credential and the body, and writes the envelope with its status. Every connection
  carries one request. A body that is too long is read on, up to 4 MiB, before it is refused, so the refusal
  reaches a client that is still sending.
- **One thread owns the state and the log** (`engine.rs`). Handlers send it a command over a channel and await
  the answer, so the blocking append and sync never run on tokio's workers and the one order is the thread's own.
  The jobs that are waiting when the thread looks are answered together and share one sync. A receive that waits
  is asked again every time the state has changed and the change is on the disk.
- **Snapshots** (`snapshot.rs`): a header line with the format's version, the log's position and a checksum, then
  the state as JSON; written beside its name and renamed into place; one every `--snapshot-every` entries, after
  a start that read entries, and at a stop; the newest three are kept, and a damaged one is passed over for the
  one behind it. The checksum covers the position, and the log is shortened only once two snapshots exist.
- **Reviewed.** Two independent reviews of the code on 2026-11-12, one of the rules against BAP/1 and the Go
  reference, one of the engine, the log and the HTTP layer. What they found and what was changed is in the
  execution log's row P86-2; what was left for a decision or a measurement is the "Known limits" section of
  `docs/developer/change/server.md`.
- **The oracle** is the conformance suite through `brigade adapter rest`, as for the stand-in. `brigade-core`
  also has its own tests, and the fs adapter's store is the worked example for every rule's detail.

### 4.6 Slack and email inside the server (phase 3b)

The owner's rulings are §2 items 17 and 18, and three he added the same day: Slack "however the Slack app is
configured today (i.e., mode)", which is the Events API over public HTTPS and not Socket Mode; email may be a
pull, Resend or Postmark alone is fine for the first version, throughput does not matter, and "priority number one
is limiting the lift on the Brigade admin", knowing that each organisation has its own mailbox provider; and the
team moves only once the gateways work on the server.

What exists today is written down, from the Edge Functions and their tests, in
`.ignored/brigade-server/gateway-spec.md` (2026-11-12, gitignored scratch in the main checkout): identity, both
directions of both gateways, the state, the secrets, what leans on Supabase, the test cases and the sizes (about
3,000 lines of code and 2,300 of Deno tests). That page is the port's reference and the Deno tests are the cases
to carry over.

- **Two layers.** A new crate, `brigade-gateway`, is the logic with no I/O: the leading directives
  (`_shared/block.ts`), the mail text (`mail.ts`), recipient resolution, inbound and outbound resolution, the
  renderers and the roster text (`gateway.ts`, `slack.ts`), and the two signature checks. Its tests are the Deno
  tests, ported case for case. `brigade-server` gets a `gateway` module: the configuration, the state, an HTTPS
  client, the Slack Web API and the mail providers' calls, the HTTP endpoints and the loop.
- **Identity, unchanged for members.** A gateway is an ordinary member with one session per team and kind:
  `slack-gateway` with harness `gateway-slack`, `mail-gateway` with harness `gateway-email`, idle, accepting, a
  lease of 600 s, and the description the functions write. The plugin's skills find it on the roster by harness,
  so no plugin change is needed. It becomes a member the ordinary way: the operator gives the server the team's
  join secret in a file, the server joins through its own `/v1/team/join` and keeps the credential in its gateway
  state. From then on it acts only through the engine's request path with that credential, so every rule and
  every limit applies to it as to any member.
- **Off until configured.** `--gateway-config <file>`: one JSON file that names the Slack app (the bot token's
  and the signing secret's files, the bot's handle, the workspace), the mail provider (its key's and its webhook
  secret's files, the inbox, the public address, the From) and, for each, the join secret file of every team it
  serves. Secrets are named by path and never by value.
- **Inbound, on the server's own listener and outside `/v1/`.** `POST /gateway/slack` takes the Events API
  (the `url_verification` handshake, `message` and `app_mention`) and the slash command, checked by Slack's
  signature. `POST /gateway/mail/resend` and `/gateway/mail/postmark` take the provider's webhook, checked as the
  connectors check it today. So the server must be reachable at a name with a certificate a public authority
  signed, as the owner's is.
- **Outbound.** A loop inside the server, every few seconds and at once when a message is accepted for a gateway's
  session: heartbeat, receive up to 50, deliver, record the thread, acknowledge. Slack through its Web API; mail
  through the provider's send call. Parity with the functions first, their known oddities included and listed.
- **State.** The gateway's own bookkeeping, under `gateway/` in the data directory: each gateway's credential and
  session, the thread rows that make a reply routable, and the ids already received. Files written whole and
  moved into place, swept after 90 days as today. It is not in the ordered log: a standby (phase 4) has to be
  given it another way, and that is noted there.
- **The HTTPS client.** hyper's client connection over tokio-rustls with the webpki roots, a few new crates in
  `server/deny.toml`, and no HTTP client framework.
- **Order.** The crate and the ported logic with its tests; the state, the configuration and the client; Slack out
  and in; mail out and in; the operator's page and the `security.md` paragraph; release 0.2.0 and the server on
  `sled` moved to it; then the owner's part (the Slack app's two request URLs set to
  `https://brigade.appshapes.com/gateway/slack`, the bot token and signing secret in files on the host, the mail
  provider's key, webhook secret and webhook URL); then the team. As first written that was `team create
  --adapter rest` for `appshapes`; the owner's ruling of §2 item 19 replaced it with §4.7.

### 4.7 Taking in a team that exists (phase 3c)

A join checks the SHA-256 of the whole secret against the team the secret names, and `team.created` carries a
reference, a name, that hash and a creator. So a team that lives on another backend is taken in by one record:
`team.created` with the reference inside its existing secret, its name, the secret's hash, and no creator and no
member. The server's parser takes the reference as everything between the prefix and the last dot, so a Supabase
reference, a UUID, is taken as it is.

- **An operator's command**, `brigade-server --data <dir> --import-team <name> --join-secret-file <file>`, run
  between a stop and a start because one process owns the log. It is a job of its own kind for the engine and
  not a request, so the frozen contract is untouched and no address of the server can take a team in. The same
  secret again changes nothing; another secret for the same reference is refused.
- **What a member's machine sees.** The team file's `adapter` and `url` change and `publishable_key` goes, which
  reads as a move to another backend: the next session attaches to nothing and says to run `team join`, and the
  join needs the secret file the member already has. One join for each machine. A member doing nothing at all is
  not possible yet: their credential is the old backend's, which the server cannot check (work item 156's last
  phase).
- **What keeping the reference keeps.** The board's folder id, which comes from the reference, and every name a
  machine or a person has for the team. Members, sessions and waiting messages do not come over.
- **The gateways** join with the same secret file.

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

Nothing blocks. Two small asks were worth making before 0.1.0 freezes `spec/udp.md`. Both are Cavalry cards
since 2026-11-12, 272 and 271 (§3; the Cavalry session reports that Rjae told it to proceed on its own
recommendations), and the server moves its pin and its `Shape` when a commit with both is on Cavalry's `master`:

- **A term length per channel.** I called it nice to have; the Cavalry session disagrees, and its numbers are why.
  A log is 48 MiB today. With a 1 MiB term an instance among five holds 38 MiB, not 488 MiB, and an admin has
  nothing to size. Until it lands, the server's container needs `/dev/shm` set (the Cavalry session's formula for
  a full mesh of n is 8 MiB + 48 MiB × 4n), and **the server checks the room at start and refuses to start with
  a plain message** rather than fail quietly. Built in phase 2: the check is sized from the term the feed asks
  for, 201 MiB for one instance at the current pin and about 20 MiB on a 1 MiB term. In a container with Docker's
  default 64 MiB the server ends at once with "has room for 64 MiB and the feed needs 201 MiB" and how to give it
  more (measured 2026-11-12).
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
  it, and every unacknowledged message is still there. Measured 2026-11-12 on the developer's Mac
  (`make server-test`): through `brigade adapter rest`, 48 passed, 0 failed, 1 slow case skipped on the file log,
  and 49 passed, 0 failed, 0 skipped with `--slow` on the Cavalry feed at `620a2e2`. On both logs a server that
  was killed and started again held every unacknowledged message, the acknowledgements, the cursors and the
  idempotency keys. On the Cavalry feed a server held still for 12 seconds went on with nothing lost or
  repeated, and a server made to roll at every snapshot let go of the recordings its snapshots covered and held
  the same state after a kill. The same tests and both suite runs are green in a Linux container (Linux 7.0 on
  aarch64 in Docker Desktop, `--shm-size=512m`): not a real disk. CI's runner, x86-64, ran the `server` job for
  the first time on `87df95b` (run 43222730771): green in 2 min 22 s on a cold cargo cache.
- **A real team:** `make e2e` against the adapter and the server, then a green-field team of two members for a
  week, with the server's log directory and its counters as the measurement. Changed by the owner on 2026-11-12
  (§2 item 17): no week of two members; the `appshapes` team, every repository of it, moves to a server on the
  host `sled` once the gateways work there, and real use is the proof. What stands in for `make e2e`, which is
  written for a Supabase stack and real Claude Code sessions, is `TestATeamOnAServerThroughTheRealBinary`: the
  built `brigade` and its bundled adapter as real processes, against the stand-in and against the server.
- **Standby:** two instances on two hosts; send through the primary, stop it, promote, receive from the standby.

## 8. Phases

| Phase | What | Needs |
| --- | --- | --- |
| 0 | Research, the spike, the direction. Done 2026-11-12. | |
| 1 | The contract page, the `rest` adapter, the stand-in, conformance green in `make test`. Go only. Built and verified 2026-11-12 (execution log P86-1): conformance(rest) 48 passed, 0 failed, 1 slow case skipped; 49 of 49 with `--slow`. | nothing |
| 2 | The server with one instance: the workspace, records, the state machine, the recorded feed, snapshots, HTTP, credentials; the conformance suite green against it; the `server` CI job. Built and verified 2026-11-12 (execution log P86-2): `server/` with `brigade-core`, `brigade-log` and `brigade-server`; `make server-build`, `server-check`, `server-test` and `server-deps-check`; the `server` job in `ci.yml`, green on its first run (2 min 22 s, cold cache) and not required by the ruleset. On `master` as `87df95b`. | phase 1; rulings 3, 4 |
| 3 | A real team: `team create --adapter rest`, the team file, the session-start lines, `make e2e`, the Project Owner and Team Admin pages, the `security.md` section, the first server release, the experimental label. Built 2026-11-12 so far (execution log P86-3): `rest` is a second bundled adapter the harness resolves by name; `team create --adapter rest --url … [--certificate …] [--create-token-file …]`; the team file's `certificate` member, a pin the member's adapter trusts in place of an authority; join and re-consent carry it; one end-to-end test through the built binary, against the stand-in in `make test` and against `brigade-server` over TLS in `make server-test`. Then (P86-4): the Project Owner and Team Admin pages, the `security.md` section, `--tls-self-signed`, version 0.1.0, `make server-tag` and the `server-release` workflow, the contract frozen at version 1, and `server` a required check. | phase 2; rulings 2, 5 |
| 3b | Slack and email inside the server (§2 item 18): Slack as the Slack app is configured today, the Events API over public HTTPS (the owner, 2026-11-12: "However the Slack app is configured today (i.e., mode) that's how I want `server` to be designed"), email through Resend or Postmark first; off until an operator configures it; the gateway acts through the same request path as a member. Then the `appshapes` team moves to the server on `sled`, reached as `brigade.appshapes.com` on TCP 443 with the owner's own certificate (§2 item 17). Built 2026-11-12 (execution log P86-6): the crate `brigade-gateway`, the Edge Functions' shared TypeScript and the four handlers' texts ported and checked against the TypeScript by running both; `brigade-server/src/gateway/`, the I/O; `--gateway-config`; `POST /gateway/slack` and `POST /gateway/mail/resend` or `/postmark`; an independent review and its fixes; released as server 0.2.0. Not yet: the gateways switched on at the owner's host, and the team's move. | phase 3; the release |
| 3c | Taking in a team that exists, with its join secret (§4.7; §2 items 19 and 20): `--import-team`, released as server 0.3.0, then the `appshapes` team taken in on the owner's host and the gateways configured with its secret. The move of the four repositories, the board and the members is work item 272. | phase 3b |
| 4 | Standby instances: following, snapshots for a new instance, promotion, the adapter's second address (work item 156's team file version 2), the two-host proof, a week's soak. | phase 3; ruling 1 |
| 5 | Email and Slack beside the server: work item 156, phase 4. Superseded for a team on this server by phase 3b; 156 keeps the gateway process for other backends. | independent |
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

1. **One writer at a time** (§4.3), with promotion by an operator's command first. Ruled 2026-11-12 (§2 item
   16): yes.
2. **Who may create a team on a server.** Ruled 2026-11-12 (§2 item 8): "Anyone". The default this plan worked
   to, that a stranger who can reach a server should not, is withdrawn: a server started without
   `--create-token-file` lets anyone who reaches it create a team. The option stays for an operator who wants a
   token, read from a file, never argv. Still open beside it: nothing bounds the sessions a member registers or
   the principals a join secret makes (`docs/developer/change/server.md`, "Known limits").
3. **TLS.** Ruled 2026-11-12 (§2 item 6): the default, and an option for TLS that ends in front of the server.
4. **The server's dependencies.** Ruled 2026-11-12 (§2 item 7): hyper, tokio and tokio-rustls, and the allowlist
   in `server/deny.toml` stays.
5. **The `server` CI job and the ruleset.** Ruled 2026-11-12 (§2 item 12): the job runs from phase 2 and becomes
   required when phase 3 ships. Phase 2 worked to it: the job is in `ci.yml` and the ruleset does not name it.
6. **The stand-in.** Ruled 2026-11-12 (§2 item 16): a test fixture only, never shipped.
7. **Work items.** Ruled 2026-11-12 (§2 item 16), as proposed, with one change that item 18 made the same day:
   the Slack and email gateways of a team on a server are built into the server, here. 89 carries phases 1 to 4; 156 keeps the team file version 2, the protocol additions
   and the gateways, and a comment there says its phase 3 is built here.
8. **Cavalry's two small asks as cards now** (§6), and whether Brigade's team is invited as an early tester of
   Cavalry before 0.1.0. The first half is answered: they are Cavalry's cards 271 and 272 (§3). The invitation:
   ruled 2026-11-12 (§2 item 16), yes; the server on `sled` is that tester.

## 11. The roadmap to the cut-over: work items 89, 273, 274 and 272 as one

The owner made these four one roadmap on 2026-11-12 (§2 item 21) and gave its order to this plan. The cut-over is
the one moment at which the `appshapes` team's repositories, members, Slack and mail leave the Supabase project
for the server. Everything before it changes nothing for anyone; nothing in it is done twice.

| Step | Work item | What | Who | State |
| --- | --- | --- | --- | --- |
| 1 | 273, part 1 | The server takes in a team that exists, with its join secret (§4.7): `--import-team`, server 0.3.0. | the 89 session | built and released as 0.3.0, 2026-11-12 (execution log P86-7) |
| 2 | 274 | The admin verbs on a team on the server: rotate the join secret, revoke a member, transfer the team; and who owns a team that was taken in, which has no creator. A server release and a plugin release. | the 89 session, unless the owner gives it to another | not started |
| 3 | 89 | Ready for the cut-over: the owner's host on the release that has steps 1 and 2; the `appshapes` team taken in there with its existing secret; the gateways' configuration written with that secret's file and the mail team's inbox; the Slack app's bot token and signing secret in their files on the host (the owner). | the 89 session, and the owner for the two Slack files | not started |
| | | **Here work item 272 can start, and the owner is told** (below). | | |
| 4 | 272 | First the check that file sync and the board work on a team on the server, and the board's carry-over tried on a scratch folder. Then the cut-over, once, in the order 272 gives: the team file in the four repositories; the gateways go live (the Slack app's two addresses moved by the owner, the mail provider's webhook added by the 89 session); the board carried over; each member joins once with the secret file they have; the check. | 272's session, with the 89 session for the gateways | waits for step 3 |
| 5 | 273, part 2 | A member's next session attaches with no command run: a voucher the server trusts. A security design and the owner's ruling first. Before the thinktech team moves. | to be assigned | after the cut-over |

**Why 274 comes before the cut-over.** A team on the server cannot rotate its join secret or remove a member
until 274 ships. The move keeps an old secret on purpose, and every member, one of them from another
organisation, holds it in a file. Moving first would take away, at that moment, the two controls the team has on
Supabase today. The other order is possible, the cut-over first and that gap accepted for as long as 274 takes;
it is the owner's to choose, and this plan takes the safer one until he says otherwise.

**What is switched only in the cut-over, never before.** The Slack app's two request addresses: a Slack app
points at one backend, so the moment they move, Slack stops for every member who has not joined the server yet.
The mail provider's second webhook: once it exists, a mail to the team's address is carried by both backends, and
the server would bounce it while nobody has joined. So step 3 prepares both and switches neither.

**When 272 can start, and how the owner is told.** When step 3 is done: the release with the import and the
admin verbs is on the host, the team is taken in, the gateways are configured and ready, and the two Slack files
are there. The 89 session then tells the owner twice, as he asked: in its conversation, first in its message; and
with `brigade decision --to 'rjae@appshapes.com' --options "start 272 now | wait"`, which reaches his digest's
recipient if he has not answered in the conversation within his wait. It tells `brigade-89` too, if that session
is still there. Work item 272 carries the same rule for a session that gets there first.

**What is still the owner's to decide** (on work item 272): whether the member from another organisation joins
the one team; what becomes of the old teams' Slack and mail addresses; the numbering of carried-over work items;
and, here, the order of steps 2 and 4.
