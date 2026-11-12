# A Cavalry adapter: a team that runs with no backend project (work item 89)

Owner: Rjae. Session: brigade-fix-1e, with the Cavalry session `cavalry-adapter-cavalry`. Written 2026-11-11 from
this session's review of both repositories and from that session's answers and spike. Status: **superseded on
2026-11-12 by `brigade-server.md`**, when Rjae turned the design from peer-to-peer to a server that members reach
over HTTPS. Nothing in §10 here is to be built. The page stays for §3, the facts the Cavalry session's spike
measured, and for the design the server plan grew out of.

## 1. The goal

A Brigade team runs with no Supabase project and no other hosted service: sessions, the roster, messages, team
management and the gateways, carried between the members' machines by Cavalry (`appshapes/cavalry`, read at
`0d28208`). The join secret stays the team's boundary. Once Cavalry can carry it for every member, the adapter is
meant to replace Supabase as the shipped default; this plan changes no default.

**The yardstick** is the green-field team (ruling 6), judged by what each person does once the plan has shipped:

- The Team Admin runs `brigade team create --adapter cavalry --name <team> --secret-file <path>`, with
  `--anchor <host:port>` when the team has an anchor, and commits the team file.
- Each member installs the plugin and runs `/brigade:join <path>`. Nothing else: the adapter is bundled in the
  binary and the node comes with the release.
- Each session has sessions, messages, doing, quiet, folder sync and the board; email and Slack once the team
  runs the gateway process (§6).

## 2. Rulings (Rjae, 2026-11-11)

1. An **anchor** is acceptable: one always-on peer the team owns, running the same binary as every member.
2. A **shared network**, a LAN or an overlay such as Tailscale, is the first supported setup.
3. **No key pairs**, not initially at least. The team key is the only boundary and identity has no authority, as
   the Syncthing adapter's ruling of 2026-10-26.
4. A joiner's **seed** is the anchor's address in the team file; with no anchor it travels in the file that
   carries the secret.
5. The **node is a Rust binary** in Brigade's release; the adapter stays in Go.
6. **Green field first**; an existing team's move is later.

Rulings 1 and 6 relax two of the criteria of 2026-10-27 that the Syncthing message adapter failed
(`.context/plans/syncthing-adapter.md`, head): no hub, and nobody rejoins. The third, every gateway remains, is
met by §6. The fourth, no device ids in the team file, is ruling 4's: an anchor's address is the team's own
machine, named once.

## 3. Ground truth

**Brigade.** The adapter protocol (`docs/protocol-v1.md`) asks for at-least-once delivery with no ordering, a
roster of sessions with leases, and a message kept seven days for a recipient who is off. Card 155's review
found the messaging core adapter-neutral and proven on the fs adapter; what a team loses on another adapter is the
hosted gateways, the capabilities the adapter does not carry, and the harness's Supabase-shaped team file. The
board, the digest, decisions, notes and plan files travel by folder sync and need only `sync_peer` on the roster.
The binary builds with `CGO_ENABLED=0` for six targets, two of them Windows, and links only
`docs/allowed-deps.txt`.

**Cavalry**, as the Cavalry session confirmed fact by fact (its answers, section A):

- Publish and subscribe over UDP between hosts, a durable log with replay from a position, one publication to
  up to 64 destinations added and removed at run time, and a driver that a Rust program embeds. Linux and macOS
  only.
- A subscriber binds a port the publisher must reach. A publisher behind NAT can send to a reachable subscriber;
  a subscriber behind NAT cannot receive. No relay, hole punching or discovery.
- Nothing on the wire is encrypted or authenticated. Anyone who reaches a port can start a stream there, and
  anyone who also knows a session id can put a frame into an image ahead of its publisher, after which the true
  frame is dropped as a duplicate.
- A `cavalry:udp` publication with no connected receiver refuses every offer, recorded or not (`cavalry:ipc` was
  not tested).
- Every run of a publisher is a new recording. The position keys (ADR 0059), under one initial term id every
  run, let each run start where the last stopped, so a feed is one line of positions and a follower keeps one
  number. A recording still live from an earlier client must be stopped before the node reads where the feed
  stopped.
- A node suspended for more than 10 s comes back with its client closed. Connecting again is the restart path.
- A replay with `FOLLOW_LENGTH` plays a recording from a position and then every byte the recorder syncs, so one
  stream carries history and what follows it.
- An address is resolved once, on both sides. A peer that moves is found by removing and adding its destination.
- The Raft cluster has no client outside a member, no restart on an existing log and at most 8 fixed members.

**The spike** (the Cavalry session, 2026-11-11, an Apple M5 Pro, macOS, not idle; scratch under
`../cavalry/.ignored/cavalry-adapter/`, which may not survive): two processes that each embed a driver. 22 checks
passed and 7 more for a suspended node; this session read the transcripts. Every record arrived once and in order
across a follower's restart, the publisher's restart and kill, and a follower that moved port. A second run, 10
checks, read a feed only through a following replay, as §4.3 does, while the publisher rolled its recording, was
killed and was suspended for 15 s, and while the follower was suspended: each time the follower asked again from
its kept position and read on. Measured:

| Node | Resident | One core, idle |
| --- | --- | --- |
| Cavalry as it is (16 MiB term), three logs | 308 MiB | 3.0% to 3.6% |
| The same, four logs: this plan's node with an anchor (§4.7) | 355 MiB | not measured apart |
| A patched scratch copy: 1 MiB term, a backoff that sleeps up to 10 ms, three logs | 29 MiB | 0.9% |

Not checked by anyone: Linux, Windows, a NAT, a host whose own address changes, loss on a real network, more
than three nodes, a feed past one term, a power loss, a suspension shorter than 10 s (the 10 s is the spec's
timeouts, not a measured threshold), and anything between two hosts: the whole spike ran on the loopback.

## 4. The design

### 4.1 Not the Raft cluster

A quorum of laptops that sleep stops the team whenever most are off, a member alone at night could not register
a session, and the protocol needs no total order. Every fact below has one writer, so nothing needs consensus.
Publish and subscribe with the durable log are enough, and they are built.

### 4.2 Three pieces

- **The adapter**, `brigade adapter cavalry`: a second bundled name in the one Go binary
  (`internal/adapters/cavalry`), as the Syncthing adapter was. It holds every Brigade rule: validation, the
  store, leases, caps, the errors, the sealing of records. It reuses `adapterkit` and the fs adapter's store
  rules.
- **The node**, `brigade-node`: a Rust binary in this repository that embeds `cavalry::Driver` and a client
  (`cavalry` with `default-features = false`, a git dependency pinned to one commit until 0.1.0). It knows
  feeds, positions and peers, and moves records it cannot read.
- **A local socket** between them, under the state directory, mode 0600. The node's verbs: `status`, `append`
  (answers with the position, after the recorder's sync), `read` (a feed, from a position), `follow` (a stream
  of feed, position and record) and `peers`.

The node is started on demand by the adapter, as the sync engine is, and runs while a session on the machine
lives. An anchor runs it as a service.

### 4.3 Feeds and records

A node has **one feed per team**: an exclusive publication created with the position keys, recorded by the
driver. The adapter appends a **record** for each fact and the node appends nothing of its own. A follower reads
a feed through one replay that follows (`FOLLOW_LENGTH`) from the position it kept, so it needs no merge and is
only ever given what is on the publisher's disk. Three consequences the spike fixed:

- Until Cavalry lets a recorded publication take appends with no receiver (§7, ask 3), the node subscribes to
  its own feed and adds that endpoint as the first destination. Since followers read only through replays, that
  may be the feed's one destination, on the loopback, so that only replays leave the machine; this is untested
  between hosts.
- A restart, a wake from more than 10 s of sleep and a moved address are one path for the publisher: connect,
  stop the feed's live recording, read where the feed stopped, add the publication there, tell the peers the
  endpoint.
- The follower's half of that path: whatever ends a recording (a roll, a restart, a wake) ends every follower's
  replay, and the follower asks again from the position it kept. The publisher cannot do it for a follower
  whose position it does not know.
- A record that fails its seal is a hole: the node fetches that range again by replay.

A record is `{version, team, epoch, author principal, type, payload}` sealed with the team key: AES-256-GCM
under a key derived by HKDF, both from Go's standard library, so `docs/allowed-deps.txt` does not grow. The
join secret is never stored (C-05); the derived key is, in the profile's credential file, mode 0600.

Retention is by rolling, once a day, in this order: the node waits until the recorder has written everything
published, stops the recording, closes the publication and adds the feed again at its position (40 ms in the
spike, the destinations kept). It purges recordings older than the seven days the protocol asks for; a purge is
refused while a replay still reads the recording. A follower away for longer asks for a position no recording
holds, and the node answers that itself: the follower starts from the oldest position held. The adapter's own
store is a cache built from the feeds and sweeps as the fs adapter's does.

### 4.4 Sessions and the roster

`session.open`, `session.update` (a name, a description, an inbound policy) and `session.close` are records on
the owner's feed. Heartbeats travel on a second, unrecorded stream, a **beat** every few seconds that carries
each open session's current fields and the seconds its lease has left, so two clocks that disagree do not move
a lease. Each node computes `state` on its own clock, as 4.5.8 has an adapter do. `sync_peer` rides the beat, so
folder sync and the board work unchanged.

### 4.5 Messages

`message send` appends a `message.send` record and answers `accepted` once the recorder has synced it: accepted
means on the sender's disk. The record reaches the recipient's node through the anchor, or directly when the two
machines next meet, and the recipient's acknowledgement is a record on its own feed. Idempotency keys,
`hop_count`, `reply_to` and the rate limits are checked by the sender against its own replica, as the Syncthing
adapter did. On one machine this is exact, which is what the conformance suite sees; between machines the caps
are the sender's own, and the harness's receive-side limits stay.

### 4.6 The team and its secret

- **Create.** A random `team_ref`, a random 256-bit secret, the key derived from it, a `team.create` record. The
  team file carries `adapter: "cavalry"` and an adapter-defined `backend` object: the anchor's address when there
  is one, and a check value derived from the key, which lets a joiner's adapter refuse a wrong secret before it
  reaches anyone.
- **Join.** The joiner's node sends a beat to the seed, sealed with a key derived from the team key. A node that
  can open it starts a following replay to the joiner and follows the joiner's feed. The joiner appends its
  `member.join`. With no seed reachable the join answers `unavailable`.
- **Leave** is a `member.leave` record and the member's sessions closed.
- **Admin.** `rotate-secret` appends a `team.rotate` under the old key and seals everything after it under the
  new one; a member that has not joined again with the new secret file answers `unauthenticated`.
  `revoke-member` and `transfer` are records every honest node obeys at once.

What ruling 3 means here, to be said in `docs/security.md` and on the admin's pages: a record proves that a
holder of the secret wrote it, never which one. Any member can claim any sender and can forge an admin record;
every node that carries a message can read it; and the only exclusion that binds a member who edits their
client is a rotation, after which the members who remain join again. Key pairs would change each of these.

### 4.7 Two topologies

- **Mesh**, with no anchor: each node follows each other node's feed directly. A message waits on its sender's
  disk until sender and recipient are on at the same time. A node serves one replay per follower, and Cavalry
  bounds a driver at 64 live replays and 64 send endpoints, the feed's own among them: 63 followers. A node in
  a mesh of N holds 2N logs.
- **Star**, with an anchor: each member's feed is followed by the anchor alone. The anchor appends what it
  receives to its own feed, and members follow that one feed. A member holds four logs whatever the team's size
  (its publication, the image of its own feed, the replay that serves the anchor, the image of the anchor's
  replay), three once ask 3 of §7 lands, and the anchor 2N + 2. A message sent while its recipient is off waits
  on the anchor, and a member's own record coming back on the anchor's feed is the proof the anchor has it.

The anchor holds the team key, so it is as trusted as a member. It is installed by the admin from a secret file
and runs the node as a service. By the Cavalry session's reading (its answers, B2), an anchor on the overlay is
fine and an anchor on a port that faces the internet waits for Cavalry's encryption.

## 5. What a session gets

| Feature | On this adapter |
| --- | --- |
| Sessions, roster, send, receive, ack, watch | Yes. The watch is push: a record arrives in milliseconds |
| `doing`, quiet, labels, model, version | Yes: session fields on the feed and the beat |
| Team create, join, leave, members | Yes |
| Rotate, revoke, transfer | Yes, with the limits of §4.6 |
| Folder sync, the board, digest, decisions, notes | Yes, through Syncthing as today, which stays a dependency |
| A message to a member who is off | With an anchor, as today. Without one, when the two machines next meet |
| Email and Slack | Through the gateway process of §6 |
| Windows members | No, until Cavalry runs there (§7) |
| Members on different networks with no overlay | No, until Cavalry has a subscriber that calls out (§7) |

## 6. Gateways

The hosted gateways are Edge Functions and do not come along. Work item 156's section 4 is the answer and is
adapter-neutral: one Go gateway process over the adapter protocol, an ordinary member, here run on the anchor.
Slack over Socket Mode needs no public URL. Mail needs an IMAP connector, or a provider's webhook through a
tunnel; a tunnel such as ngrok carries webhooks only, never Cavalry's UDP. This plan depends on that phase of
work item 156 and does not repeat it. The webhook gateway runs over any adapter today.

## 7. What Cavalry would need to grow

Sized by the Cavalry session; nothing is landed there and no ADR is written. ADR 0061 keeps asks 4 to 6 out of
0.1.0, estimated between 2026-12-14 and 2026-12-28. The first setup needs none of them.

| # | Ask | Size, by the Cavalry session | Needed for |
| --- | --- | --- | --- |
| 1 | A term length per channel (1 MiB: the longest message is a term's eighth) | One card, two pull requests; changes `spec/udp.md`, so best before 0.1.0 freezes the page | A node a laptop can hold: tens of MiB, not hundreds |
| 2 | An idle that sleeps a fixed time | Two pull requests; no spec page | The same node at under 1% of a core |
| 3 | A recorded publication that takes appends with no receiver | One card, two pull requests; changes `spec/udp.md` too | Dropping the node's subscription to its own feed, and a log with it |
| 4 | A subscriber that calls out to a reachable publisher | One card, three pull requests | A member behind NAT |
| 5 | Wire encryption, a pre-shared key first | A slice, the largest | An anchor that faces the internet |
| 6 | Windows | A slice | Windows members |

Cavalry asks in return (its answers, section E): the sentence that says which item on ADR 0061's out list
Brigade needs first, which for the first setup is none; `cavalry doctor` run on real laptops, with every wrong,
noisy or missing finding reported; a week's soak of a Brigade team with each node's counters; and each question
this work had to ask, as a gap in Cavalry's pages.

## 8. The harness and the release

- **The team file.** An adapter-defined `backend` object in place of `url` and `publishable_key`, and
  `team create --adapter`: work item 156's phase 1, needed here in its smallest form.
- **The seed with no anchor.** The file that carries the secret gains optional `seed <host:port>` lines after
  its first line, which the harness passes as `backend.seeds`.
- **The node in the release.** A second toolchain (Rust 1.89 or later): four targets, a CI job that stays under
  five minutes and joins the `master` ruleset, the node's checksums beside the binary's, the rule for
  reproducing it from the tag, and the plugin's bootstrap fetching it. Until that phase the node is a
  development binary, as the webhook gateway is.
- **Windows.** The adapter compiles there and answers `unavailable` with a reason that names the node.

## 9. Proof

- `make test` stays free of Rust and of Docker: the adapter reads and writes feeds through one interface with
  two implementations, files in a directory and the node's socket. The conformance suite runs on the files, its
  three principals sharing one directory through `--shared-env`, as the Syncthing adapter's local mode did.
- Two nodes on the loopback behind `BRIGADE_TEST_CAVALRY=1`: create, join, the roster, a message each way, a
  restart, a suspension, a moved port, a message sent while its recipient was off.
- Then two machines on one network, and the done criterion of work item 89.

## 10. Phases

Each phase is its own release, changes no default, carries its pages and changelog section through
`make docs-check`, and adds its rows to the execution log.

| Phase | What | Needs |
| --- | --- | --- |
| 0 | Done 2026-11-11: the review, the rulings, the Cavalry session's answers and spike | |
| 1 | The adapter over feeds in files: the record format, the store built from feeds, every verb, the sealing, a green conformance run in `make test` | nothing |
| 2 | The node: the crate, the socket, append, sync, follow, the restart path, the two-node test, a Rust job in CI | question 4 |
| 3 | Mesh on a shared network: beats, peers, seeds, the team file's `backend` object, two machines | Cavalry asks 1 and 2 before a member runs it |
| 4 | The anchor: the role, its feed of everyone's records, its install, a message held for a member who is off | phase 3 |
| 5 | Email and Slack through the gateway process | work item 156, phase 4 |
| 6 | The node in the release, the member's and admin's pages, `docs/security.md`'s section, an experimental label | question 4 |
| 7 | Beyond the shared network: NAT, an anchor on the internet, Windows, key pairs, then the question of the default | Cavalry asks 4 to 6 |

Phase 1 depends on nothing in Cavalry and proves the part of the design that is Brigade's alone.

## 11. Questions for Rjae

1. **Cavalry's three small asks** (§7, asks 1 to 3): cards on Cavalry's side now, before 0.1.0 freezes
   `spec/udp.md`? Recommendation: yes for asks 1 and 3, which both change that page; ask 2 can follow.
2. **Windows members.** How many of a Brigade team's members run Windows natively is the evidence a Cavalry ADR
   would need (its answers, B4).
3. **What `accepted` means with an anchor.** On the sender's disk, as §4.5 has it; or held for up to a few
   seconds for the anchor's copy. Recommendation: the sender's disk, and say so on the member's page.
4. **A Rust toolchain in CI and the release**: the job, the ruleset, the checksums and the reproducibility
   rule. Ruling 5 agreed the binary; this is its cost.
5. **Admin verbs under ruling 3** (§4.6): advisory records, and a rotation after which every member joins
   again. Acceptable for the first setup?
6. **The trigger for ADR 0061.** The sentence Cavalry asks for: the first setup needs nothing from its out list.

## 12. What this plan does not do

- It does not change the Supabase path, a default, or a wire shape of the adapter protocol.
- It does not move an existing team (ruling 6), carry messages over, or vouch for a member across backends.
- It does not give identity authority (ruling 3), hide a message from the members who carry it, or protect a
  team from a member who edits their client.
- It does not cross a NAT, face the internet or run on Windows before Cavalry can.
- It does not replace Syncthing for folder sync and the board.
- It does not use Cavalry's cluster.
