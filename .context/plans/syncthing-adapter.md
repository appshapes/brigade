# The Syncthing message adapter: Brigade with no backend

**Withdrawn 2026-10-27 (Trello card 148, rows P57-7 and P57-8).** Shipped in 0.39.0, removed in 0.40.0. Rjae's
goal, stated after the release: no loss of functionality and no action other than `brigade:update` for the migrate
scenario — every gateway remains, members do not rejoin — and no device ids in the team file. Shape 1 cannot meet
the three together: the mail and Slack gateways are Edge Functions that need a hosted project; a member's project
credential cannot become a device acceptance without a channel both sides already trust, which is the project
itself; a joiner with only a secret cannot find a peer without a seed list. Every shape that meets them puts a
hosted component back (the project, or a hub). The engine extraction of P57-1 (`internal/syncthing`) stays, trimmed
to what folder sync uses. The rest of this page is the design as built, kept for the record.

Trello card: see the execution log rows P57-*. Owner rulings (Rjae, 2026-10-26): "I recommend Shape 1, with some
changes: we keep the test adapter, so this would become the third adapter; no key pair (we may follow with key pair
support, but not needed yet); understood, identity will have no authority." Then: create a card, proceed
autonomously including the release, handle the Supabase-to-Syncthing move for the project admin ("perhaps as
simple as a `/brigade:migrate` skill"), done when the card is tested, marked complete and archived.

## 1. What this is

A third BAP/1 adapter, `syncthing`, bundled in the one binary beside `supabase` (`brigade adapter syncthing …`).
Its backend is one directory per team that Syncthing keeps in step between the members' machines: the same
engine, instance and REST client the folder-sync adapter already drives (cards 32, 33). There is no server, no
project, no account and no egress cap. Nothing in the frozen protocol changes.

The store is the test fs adapter's layout, which was written as the dry run for exactly this (its README: "the
dry run for the object-store adapter"), with the changes a replicated directory needs: every path has one
writer, the flock is machine-local and lives outside the folder, and membership status is split so the creator's
revocation never rewrites a member's own file. The test fs adapter stays as it is (its mutants and conformance
expectations are pinned); the new package is `internal/adapters/syncthing`.

## 2. The answer in one page

- **Store**: `<state dir>/syncthing/teams/<team_ref>/`, a Syncthing folder with id
  `brigade-<team_ref first 8>-team-messages` (four dash-separated parts, so the sync adapter's `teamPrefix` never
  reads it as a checkout folder and never pauses it). Inside: `team.json`, `members/<principal>.json`,
  `revoked/<principal>.json`, `sessions/<id>.json`, `inbox/<recipient>/<seq>.<id>.json`,
  `acked/<recipient>/…`, `idem/<sender>/…`, `.stignore`. The lock is `<state dir>/syncthing/teams/<team_ref>.lock`.
- **One writer per path**: a message file is written once by its sender; a session file by its owner; an ack
  moves the file by the recipient; `members/<me>.json` by me (`team join`, `team leave`); `revoked/<p>.json` by
  the creator (`team revoke-member`), deleted by the member's own rejoin when it is not a ban; `team.json` by the
  creator (`team create`, `team rotate-secret`, `team transfer`). Retention deletes race only against deletes.
- **Engine**: the machine's one Brigade Syncthing instance (`<state dir>/sync/syncthing`, shared with folder
  sync), through the extracted package `internal/syncthing` (engine lifecycle, REST client). `message watch`
  attaches at start and detaches at exit, so the engine runs while a session lives; `team create` and `team join`
  attach to learn the device id and leave the reference to the next prune. Every other command touches only the
  local replica.
- **Joining is device acceptance.** `.brigade.json` carries `devices`: Syncthing device ids (public) of seed
  machines; `team create` writes the creator's. `team join` adds the seeds and the team folder to the local
  engine and names the local device `brigade1-<hex HMAC-SHA256(key = SHA-256(join secret), message = own device
  id)>`. Any member's running watch polls the engine's pending devices, verifies that name against
  `team.json`'s `join_secret_sha256`, and accepts the device onto the team folder (never a banned device id). The
  joiner's member file (with its device id) then reaches every member, whose watch adds the device to its own
  folder. The join returns at once with the team name from the file; the folder arrives when a seed is online.
  `profile status` says `team folder: present|pending`.
- **Identity has no authority** (ruling). The sender stamps itself; a member who writes files by hand can claim
  any sender. Caps and rate limits are self-enforced at the sender from its replica. Documented in `security.md`.
- **Revocation is eventual.** The creator writes `revoked/<p>.json`; every member's reads filter on it at once
  on their own replica; every member's watch drops the device from the team folder once the marker is 15 s old
  (the grace lets the marker reach the revoked machine). A revoked member's own watch exits `unauthorized` when
  the marker arrives, as `team leave` does. Rotation rewrites the digest; `--max-version` evictions use the
  `secret_version` each member recorded when its join completed.
- **Every member holds every message** of the team on disk. Stated plainly, with the Supabase row policy as the
  contrast, in `security.md` §18 and the admin page.
- **Local mode**: `BRIGADE_SYNCTHING_STORE=<dir>` (test-only, the conformance suite's `--shared-env`) names the
  teams root and switches the engine off. Three principals share one store and the whole suite runs locally in
  `make test`. The cross-machine behaviour is proven by a two-engine Go test behind `BRIGADE_TEST_SYNCTHING=1`.
- **The move off Supabase**: `brigade team migrate --secret-file <path>` (and `/brigade:migrate <path>` in the
  four packages) creates the team on the syncthing adapter with the same `team_ref` and name (so synced folder ids
  stay), a fresh secret, rewrites `.brigade.json` (carrying `sync` and `polling`, dropping `gateway` with a line),
  binds and pins. Members pull and `/brigade:join` the new secret file. Messages are not carried over; the hosted
  gateways stay with the project.

## 3. Ground truth used

- The protocol leaves storage, identity, membership and transport to the adapter (4.8); `team.admin` verbs are
  adapter-defined (4.2, 4.7). `TeamJoinResult` requires a non-empty `team_name` (so the join carries it in
  `backend`). `TeamJoinRequest.backend` is the adapter-defined object the harness already fills for Supabase.
- The harness resolves a bundled adapter to `[]` in the by-pid map and `os.Executable()` at call time
  (`adapterclient.bundledAdapterArgv`); a second bundled name resolves to the absolute argv
  `[<self>, "adapter", "syncthing"]` and needs no new grammar in the map or the watcher's environment.
- The team file requires `url` and `publishable_key` for every adapter today (`teamfile.validate`); for
  `syncthing` they become optional and `devices` required.
- The sync adapter's `apply` unions devices on a project folder and keeps hand-added ones; it never removes a
  device. The team folder is the message adapter's own and is managed by it alone.
- The conformance suite builds each principal's environment from scratch, gives each its own state dir and
  offers `--shared-env NAME` for a store the principals share (`docs/adapter-authors.md`).
- Syncthing 2.1.5 on this machine; `GET /rest/cluster/pending/devices` lists devices that connected and were
  not accepted, with the name each announced; `fsWatcherDelayS` is the folder's change-detection delay (default
  10 s; the team folder uses 1).

## 4. Rows

| Row | Work |
| --- | --- |
| P57-1 (done 2026-10-26) | Extract the engine: `internal/syncthing` (instance lifecycle, REST client, config readers, the fake REST server for tests); the sync adapter keeps its verbs and apply policy on top of it. Pure move, pinned by its existing tests. |
| P57-2 (done 2026-10-26) | `internal/adapters/syncthing`: the store (fs layout, one writer per path, outside lock, revoked markers, `.stignore`), every verb including `team.admin`, the watch (inbox poll, engine tick: pending-device acceptance, folder device list, pending-join completion, membership check), local mode, README. Conformance in `make test`. |
| P57-3 (done 2026-10-26) | Harness: `teamfile` `devices` and the optional URL/key for `syncthing`; a second bundled name in `config`/`app`; `team create --adapter syncthing` (device id from `profile status`); the join's `backend`; `team migrate`; `commands.md` regenerated. |
| P57-4 (done 2026-10-26) | The two-engine real test (`BRIGADE_TEST_SYNCTHING=1`): create, join through acceptance, roster, a message across engines, revoke drops the device. |
| P57-5 (done 2026-10-26) | Skills (`migrate` in four packages, the setup skills' Syncthing route), package checks, Muse digest; docs: admin create and move pages, team-file, members pages, member join page, `security.md` §18, `plugin/README.md`, developer adapters page, agent pages, CHANGELOG. |
| P57-6 (done 2026-10-26) | Release 0.39.0; CI green; card closed. |
| P57-7 (done 2026-10-27) | Withdrawn (card 148): the adapter, `team migrate`, the `migrate` skills, the team file's `devices` and every page removed; the engine package trimmed; released as 0.40.0. |

## 5. Out of scope, on purpose

Key pairs and signed files (ruling: later); a hub process; per-recipient privacy between members; an encrypted
(untrusted-device) hub; the hosted gateways on this adapter (the webhook gateway is bundled on the Supabase
adapter and can follow in a card of its own); removing a revoked member's device from project sync folders (the
sync adapter's union rule, unchanged by this card).
