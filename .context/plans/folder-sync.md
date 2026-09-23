# Folder sync: a sync-adapter model with Syncthing as the default

**Status.** **Shipped as 0.11.0 on 2026-09-23** (tag v0.11.0; migration applied to both hosted projects); plan of 2026-09-22, owner Rjae. Trello **32** (the team file opens:
the `sync` member) and **33** (the sync-adapter model, the `sync_peer` wire member, the Syncthing adapter, the
docs, the release). Anchors are file:line at **9b2c0d7**. Execution-log rows **P17-1..P17-2** (32) and
**P18-1..P18-7** (33). **One additive wire member and one append-only Supabase migration; no new Go dependency;
Brigade never carries a file byte.** Built through P18-6 (2026-09-23): §4.1 and §4.4 below are updated to the
code as merged through P18-4; P18-5 (the smoke) and P18-6 (the reviews and their fixes — among them a dropped
folder is paused, never deleted, and every member's plugin must be 0.11.0 before a project commits a `sync`
member) are done; P18-7 (the release) is to do.

Why this shape: the first design for this feature built its own transport over Brigade messages (base64 chunks,
a replication rule). Six adversarial review rounds found the replication rule unsound four times running —
ordering, conflicts, resume and caps are exactly what a mature sync tool has spent years hardening — so on
2026-09-22 Rjae chose to reach for one instead: **Syncthing**, the most used peer-to-peer folder sync for
distributed teams (open source, Go, per-file version vectors, conflict copies, resume, TLS device identity), driven
by Brigade through the one thing it has that Syncthing lacks — a team roster. The abandoned design and its review
record are kept out of the tree at `.ignored/folder-sync/transport-design-v7-abandoned.md`.

## 1. What was asked, and the rulings

Rjae, 2026-09-22: sync a configurable set of folders between the checkouts of a distributed team, configured in
`.brigade.json`. Then: "an adapter model for folder sync, using syncthing as the default when folders are listed
but allow for alternative adapters; the syncthing adapter assumes it's installed on the PATH; syncing only while
a session is active — if a team wants more they can use an externally managed solution; clean up what the earlier
design made obsolete; document it in Brigade's plain manner; definition of done is a release."

Rulings that stand from earlier the same day: the security model is **open by design, closed by configuration**
(the watcher may write into the project directory; `brigade_version` may be compared; automatic actions bypass
the outbound ask/deny rules; no secret scan — "teams use this for gitignored folders, otherwise they would use
git"); `.brigade.json` **opens** (unknown members ignored; the project declares what it syncs; a user can only
narrow or switch off). Decided at the hand-off: **typed message bodies are dropped** (no user without a chunk
transport); **the agent applies the migration** to both hosted projects (appshapes-brigade, thinktech-brigade) as part of the release, with `make backend-install` (clarified 2026-09-22: "when we say you apply it, we mean you, the agent"); **deletions propagate** with Syncthing's
trash-can versioning (14 days) as the safety net. And, last thing before sleep: "Brigade favors features over
security, open by default, and overridden by configuration. Do not reach for secure controls tonight — we can
always add controls later as needed." So: **no deny-lists, no hardening beyond what the repo's own rules require
(no shell, no secrets on argv or in logs), Syncthing's defaults as they are, and `sync: off` as the one override.**

## 2. The answer in one page

- **`.brigade.json` declares the folders**, and optionally the adapter:

  ```json
  "sync": { "adapter": "syncthing", "folders": [".context/plans", "docs/shared"] }
  ```

  Card 32 opens the file: unknown top-level members are ignored with one hook line, the `sync` member is
  validated (an unusable one switches sync off for the session with one line, never a team-file refusal), and
  `team create --force` carries it across.
- **Every session publishes a `sync_peer`** — an opaque string `<adapter>:<descriptor>` (for Syncthing, its device
  id) — as a new optional session member on registration and heartbeat, the way `brigade_version` travels: one
  additive protocol member, capability `session.sync_peer`, conformance case C-47, one append-only migration,
  both bundled adapters.
- **The watcher drives a sync adapter** through a small argv+JSON protocol: `attach` (start or join the engine on
  this machine; learn the peer descriptor), `apply` (every 60 s and on roster change: introduce the teammates'
  peers listed for the same repo, share the project's folders with them), `status`, `detach` (drop this session's
  reference; the engine stops with the last one — **sync runs only while a session is active**). The adapter
  named `syncthing` is bundled (`brigade sync-adapter syncthing <verb>`); any other name resolves to
  `brigade-sync-<name>` on `PATH`, the git-subcommand convention, documented for adapter authors.
- **The Syncthing adapter** runs a **dedicated Syncthing instance** under Brigade's state directory (own config,
  own certificate, API bound to `127.0.0.1` with its API key in a 0600 file), started by the first session on the
  machine and stopped by the last; it introduces peers and folders over Syncthing's REST API. Syncthing does
  everything else: discovery and relays (its defaults), transfer, conflicts as `<name>.sync-conflict-<date>-<device>`
  copies, deletions, the `.stversions` trash can, resume.
- **A user switches it off** with the plugin option `sync: off`. Nothing else is configurable in v1.
- **`brigade sync status`** shows the engine, the peers and the folders; one Brigade line at session start says
  what is syncing, or why not (no `syncthing` on `PATH`, no folders, a backend without the migration).

## 3. Ground truth

### 3.1 What the tree already does that this reuses

- **A wire member added this way twice before.** `brigade_version` (card 29, commit `dceaaf7`) touched: the types
  and `Validate` in `internal/protocol/session.go` (`:48-65`, `:115-117`, `:151-152`, `:207-209`) and the bound in
  `limits.go` (a wire-format bound, **no `limits` member** — `limits` members are required, so a new one would
  fail `describe` for every older adapter); `docs/protocol-v1.md` (4.4.2, 4.4.3, 4.4.4, the 4.4.9 heartbeat
  command, the 4.7 registry) and `docs/protocol-v1.schema.json` via `make schema`; conformance
  `internal/conformance/cases/c46_brigade_version.go` (+ `all.go`, `all_test.go`, `case.go`, `doc.go`,
  `suite_test.go`, `cmd/brigade-conformance/main.go` counts); the fs adapter (`describe.go`, `session.go`,
  `store.go`, `watch.go`); the Supabase adapter (`describe.go`, `session.go` RPC params, `watch.go`, and a
  `compat.go` entry that retries without the parameter on PGRST202 when the backend lacks the migration); the
  migration `20260920180000_session_brigade_version.sql` (column, `session_record`, `DROP` + `CREATE` of
  `register_session` and `session_heartbeat` with the appended defaulted parameter, restated grants) with
  `scripts/ci/advisor-lints.sql` and `supabase/tests/functions.sql` signature pins updated in the same commit;
  the harness (`hook/start.go` registration `:210` and the start heartbeat `:645`, `watch/heartbeat.go:47-58`,
  `watch/watch.go`, `watch/reopen.go`, `sessionmap`), and `commands/sessions.go` `--json`. Do the same, in one
  commit.
- **The team file:** `internal/harness/teamfile/teamfile.go` — package doc `:2-8`, closed `members` map `:94-97`,
  `unknown_field` refusal `:196-197`, `MaxBytes` 4096 `:40`/`:158-160`, the closed `Reasons()` list pinned by
  `teamfile_test.go:342-359` and `hook/resolve_test.go:237-240`, rendered by `hook/hook.go:549-560`; the only
  writer `commands/teamsetup.go:270-279` (`--force` at `:67-74`); readers `hook/start.go:486`,
  `commands/teamsetup.go:330`.
- **Spawning a child.** `exec.Command` is forbidden tree-wide by forbidigo except in `adapterclient/spawn.go`,
  `hook/spawn.go`, `adapterkit/spawn.go`, `procutil`, `testutil` and `conformance` (`.golangci.yml:98-110`).
  `adapterkit.Spawn(ctx, SpawnSpec{Argv, …})` (`adapterkit/spawn.go:123`) runs one child to completion with an
  allow-listed environment (`ChildEnv`, `:341`) and maps its stdout onto the 4.3 envelope — it is how the harness
  runs a backend adapter, and how it will run a sync adapter. The **long-running Syncthing daemon** is spawned by
  the sync adapter process, not the harness: `internal/syncadapters/syncthing` gets a forbidigo carve-out of its
  own, with the comment that it is the one daemon Brigade starts.
- **State on disk.** `~/.local/state/brigade` (`adapterkit.StateDir`), by-pid maps (`sessionmap/bypid.go`) with
  the `FrameLevel`/`DoingMode` precedent for values the hook resolves once and freezes, `adapterkit.LockFile`
  (`flock.go:53`), `WritePidfile`/`RemovePidfile` (`pidfile.go`), `WriteAtomic` 0600 (`atomicfile.go:31`).
- **The watcher.** One per session; `goWriter` goroutines beside the injector (`watch.go:646-649`); its own
  `adapterclient.Client` (`watch.go:567`) with `ListSessions` (`results.go:114`); heartbeats every lease
  (`watch/heartbeat.go`); the single-line `.notice` file (`watch.go:748`) printed by the next prompt hook.
- **Sessions on the wire.** `SessionRecord` carries `principal_ref`, `state`, `workspace_label` (default
  `teamfile.RepoName`, the repo's name), `brigade_version`, `is_self` (`protocol/session.go:136-154`).
- **Release.** `make release version=0.11.0 card=33` → `scripts/release-prep.sh` (pins, reproducible cross-build,
  checksums, `make push message="33: Release 0.11.0"`, tag `v0.11.0`, push the tag); `release.yml` fires on the
  tag only; the release-notes workflow drafts notes gated by `scripts/ci/release-notes-lint.sh` (every `brigade
  <verb>` it names must exist). `make commit`/`make push` run typecheck, pull (merge), build and `make test`.
  `make test` is Docker-free; the live Supabase tests need `make supabase-start supabase-env` and Docker.

### 3.2 Syncthing, as it is

- Installed by the user (`brew install syncthing`, `apt install syncthing`, `winget`); **assumed on `PATH`**.
  Absent → one session-start line, sync off for that session. (Installed on this machine with Homebrew on
  2026-09-22, with Rjae's permission, for the smoke test.)
- **Measured on this machine with syncthing v2.1.5 (2026-09-22):** `generate --no-default-folder` does not exist in
  v2; a plain `syncthing serve --home <dir> --no-browser --no-restart --no-upgrade --no-port-probing
  --gui-address 127.0.0.1:<port>` on a **fresh** home creates the certificate (the device id) and `config.xml`
  (with a random GUI API key) and starts with an **empty** folder list. The adapter therefore never runs
  `generate`: it starts `serve`, waits for `config.xml` to carry `<apikey>`, then for `GET /rest/system/status`
  to answer (`myID` is the device id). `POST /rest/system/shutdown` (with the key) stops the daemon cleanly
  (`{"ok": "shutting down"}`, process gone within 2 s) — preferred to SIGTERM. The daemon binds the sync
  listener on `[::]:22000` by default; a second instance on the same machine must be given its own
  `listenAddresses` (`PATCH /rest/config/options`) or it fails to bind — the smoke test needs this, a real
  team does not.
- REST config endpoints (`docs.syncthing.net/rest/config.html`): `POST /rest/config/devices` and
  `POST /rest/config/folders` take one object and **replace-or-add** by id, so `apply` is idempotent;
  `X-API-Key` header. A folder object: `{id, label, path, type: "sendreceive", devices: [{deviceID}], versioning:
  {type: "trashcan", params: {cleanoutDays: "14"}}, fsWatcherEnabled: true, rescanIntervalS: 60}`; a device:
  `{deviceID, name, addresses: ["dynamic"]}`. Defaults keep global discovery, relays and NAT traversal on, which
  is how two laptops on different networks find each other with no configuration.
- Syncthing writes a `.stfolder` marker in every synced folder and keeps the trash can in `.stversions`; conflicts
  land beside the file as `<name>.sync-conflict-<date>-<time>-<device>.<ext>`.
- One Syncthing instance cannot hold one folder id at two local paths, so the second clone of the same repo on one
  machine cannot join that folder — a session-start line says so.

## 4. Design, in travel order

### 4.1 Card 32 — the team file opens (`internal/harness/teamfile`, `hook/hook.go`, `commands/teamsetup.go`)

- `parseDocument`: an unknown top-level member is **ignored** (JSON convention 2, as the protocol); the parser
  returns the ignored names (sanitised as attributes, capped) and the SessionStart hook prints one fixed line:
  `Brigade: .brigade.json carries members this version does not define (<names>); ignored.` The secret-shaped-name
  refusal stays. `ReasonUnknownField` leaves `Reasons()`; `TestReasonsListIsClosed` and `resolve_test.go:237-240`
  follow. `MaxBytes` → **32,768** (as built, P17-1b: 32 folders of 128 bytes, each byte a control character JSON
  spells in six, is about 25 KiB once the folder rules stopped asking anything of a folder's characters);
  `TestParseLargestLegalFile` builds that file. The ignored-members line lists at most eight names and counts the
  rest (`…, and <n> more`).
- `sync` member, `teamfile.File.Sync *SyncConfig{Adapter string; Folders []string}`: `adapter` optional, default
  `syncthing`, matching `^[a-z0-9][a-z0-9_-]{0,31}$` (the backend-adapter name rule: a *name*, resolved user-side);
  `folders` ≤ 32 entries, each ≤ 128 bytes, a **relative path that `path.Clean`s to itself** (so `docs/shared`,
  `.context/plans`; not `/abs`, not `a//b`, not empty) — nothing more (no `.git`, `.brigade.json`, nesting or
  character denies: open by default; Syncthing reports a folder it cannot use through `status`); unknown inner
  members ignored (named `sync.<name>` in the ignored-members line). **An unusable `sync` is never a refusal**:
  `Sync == nil` plus a reason token, one hook line
  (`Brigade: .brigade.json's sync member is not usable (<token>); file sync is off for this session.`), and the
  session connects. **As built (P17-1b)** the closed token list, in check order, is `not_object`,
  `adapter_invalid`, `folders_invalid`, `too_many_folders`, `folder_too_long`, `folder_not_relative`,
  `folder_not_clean` (empty, `a/`, `./a`, `a//b`, `a/./b`) and `folder_root` (`.`) — `teamfile.SyncReasons()`;
  P17-1's first cut had seven more (`folder_empty`, `folder_bad_char`, `folder_dotdot`, `folder_git`,
  `folder_team_file`, `folder_duplicate`, `folder_nested`) and P17-1b cut them to
  a relative, `path.Clean`-unchanged path other than `.`, at most 32 × 128 bytes — `..` included on purpose, so a
  folder can sit beside or above the checkout (the pre-release review corrected "under the checkout" in the docs).
  No `folders` member, or an empty array, is a usable member that syncs nothing.
- `team create --force` **always replaces** the file, as before card 32, and carries `sync` across through
  `teamfile.CarriedSync`: a usable member comes over unchanged; an unusable one, a refused file that declares one,
  or a file refused unread (a symlink, a special, world-writable or oversized file) carries nothing, and the
  command prints one line with the fixed token (from `Reasons`, `SyncReasons` or `unreadable`):
  `the previous .brigade.json's sync member was not carried into the new file (<token>); add it again if this
  project syncs folders`. (P17-1 first refused the replacement in that case, `sync_not_carried`; P17-1b made it
  always go ahead.)
- The package doc is rewritten: the file is discovery **and** the project's declaration of what it syncs; it still
  never names a command, an option that changes how Brigade connects, or a secret.
- Tests: ignored members at both levels; every `sync` rule both ways; the maximal file; `--force` round trip; a
  0.10.0 fixture parses byte-identically; the fuzz corpus extended.

### 4.2 Card 33 — the `sync_peer` wire member (one commit, the card-29 template)

- `SessionRegistration.SyncPeer *string` (`json:"sync_peer,omitzero"`), on `SessionRecord`, `HeartbeatRequest` and
  the watch `heartbeat` command; nullable; absent means unchanged on a heartbeat, never cleared by one; ≤
  `MaxSyncPeerChars = 256` code points (a wire-format bound, no `limits` member); unverified text, sanitised by
  every consumer. Capability `session.sync_peer`; conformance **C-47** (register with it, list it back, heartbeat
  updates it, oversize → `invalid_input` naming `sync_peer`); 49 cases.
- Migration `2026MMDDhhmmss_session_sync_peer.sql` (`make migration-new name=session_sync_peer`): column
  `sync_peer text check (char_length(sync_peer) <= 256)`; `session_record` adds it; `register_session` and
  `session_heartbeat` are dropped and recreated with the appended defaulted parameter — repeat the latest bodies
  byte for byte apart from the addition, restate the grants with the full new type list, and update the pins in
  `scripts/ci/advisor-lints.sql` and `supabase/tests/functions.sql`. Comment header in the house style.
- Supabase adapter: params `p_sync_peer` in `session.go`, a `compat.go` entry naming the migration file so a
  backend without it degrades (the value dropped, never the registration), the capability in `describe.go`. fs
  adapter: the member in `store.go`/`session.go`/`watch.go`, the capability.
- `docs/protocol-v1.md`: 4.4.2/4.4.3/4.4.4/4.4.9 tables, 4.7 registry, Appendix A; `make schema`;
  `docs/adapter-authors.md` mention.
- Harness: the hook registers with it when known (it usually is not at SessionStart; the watcher's first heartbeat
  carries it once `attach` returns); `commands/sessions.go --json` carries `sync_peer` (no table column — an
  opaque id). `brigade sessions` human output unchanged.

### 4.3 Card 33 — the sync-adapter protocol (`docs/sync-adapters.md`, `internal/harness/foldersync`)

**Invocation:** `<argv…> <verb>`, one JSON request on stdin, one 4.3 result envelope on stdout, exit codes of 4.6,
the allow-listed environment of `adapterkit.ChildEnv` plus `BRIGADE_STATE_DIR`; spawned with `adapterkit.Spawn`
under a 20 s budget; never a shell. Resolution of the name in `.brigade.json`: `syncthing` → the bundled adapter,
argv `[<plugin binary>, "sync-adapter", "syncthing"]` (the by-pid map's `PluginBin`); any other name →
`brigade-sync-<name>` found on `PATH`, argv `[<that>]`. Nothing in the repo file ever names a path.

**Verbs** (request → result; every one idempotent):

| verb | request | result |
| --- | --- | --- |
| `describe` | `{}` | `{"name","version","protocol_version":"sync/1"}` |
| `attach` | `{"state_dir","session_id"}` | `{"peer":"<descriptor>"}` — the engine is running on this machine afterwards, with this session counted |
| `apply` | `{"state_dir","session_id","folders":[{"id","path","label"}],"peers":[{"peer","label"}]}` | `{"folders":[{"id","state"}],"peers":[{"peer","connected"}]}` |
| `status` | `{"state_dir"}` | `{"running","peer","folders":[{"id","path","state"}],"peers":[{"peer","connected"}]}` |
| `detach` | `{"state_dir","session_id"}` | `{"stopped": bool}` — the engine stops when no session remains |

`peer` on the wire is `<adapter>:<descriptor>`; the harness adds and strips the prefix, and only offers a session's
peer to an adapter of the same name. The descriptor's meaning is the adapter's (a Syncthing device id).

**The harness side** (`internal/harness/foldersync`, wired in `watch/watch.go`):

- SessionStart (`hook/start.go`, beside `resolveTeam`): from the team file's `Sync` and the plugin option `sync`
  (`on` default | `off`; anything else → `off` with a line), freeze `sync_adapter`, `sync_folders`, `sync_root`
  (the canonical toplevel) into the by-pid map (`omitzero`, validated). No folders, `off`, or an unusable member →
  nothing frozen, one line. A change in these on the continue path is a respawn reason.
- The watcher runs `goWriter("sync")`: `describe` (absent adapter → one notice line, done), `attach` → publish
  `sync_peer` on the next heartbeat; then every 60 s and after each session-list change: `ListSessions`, keep
  records that are not `is_self`, same `workspace_label`, `sync_peer` present with our adapter's prefix (any state:
  a peer is worth introducing even when offline — Syncthing connects when it can), and call `apply` with the
  project's folders (`id = "brigade-" + team_ref[:8] + "-" + sha256(folder)[:12]`, `path = sync_root/folder`,
  `label = repo + "/" + folder`) and those peers (label = the session's `human_label` or principal prefix,
  sanitised). On watcher exit: `detach`. The adapter's results are logged as scalars only; a `status` summary
  goes to the notice line on change (`Brigade sync: 2 folders, 3 peers connected` / `Brigade sync: syncthing is not
  on PATH; file sync is off`).
- `brigade sync status [--json]` (`commands/sync.go`, `internal/cli`): calls `status` and prints folders, peers and
  the engine state through the `format.go` printers; human output only on the documented stdout path.
- Plugin: `userConfig.sync` (`on`/`off`); `plugin/skills/team-messaging/SKILL.md` gains three lines (synced
  folders hold teammates' files; `brigade sync status`).

**As built (P18-2, de50ccc; merged fb49428):** the watcher re-reads the roster and applies every 60 s only (no
extra round on a session-list change) and repeats `attach` before each round; peers match on the same
`workspace_label` (no label, no peers). The SessionStart lines are `Brigade: file sync on: <n> folder(s) through
<adapter>.` and `Brigade: file sync off (<the sync option is off | no folders listed | the checkout's toplevel
could not be resolved>).`; a team file with no `sync` member prints neither. The notice lines are `Brigade sync:
<f> folders, <c> of <p> peers connected`, `Brigade sync: brigade-sync-<name> is not on PATH; file sync is off for
this session` and `Brigade sync: the <name> sync adapter is not usable (<code>: <message>); file sync is off for
this session`. The option's warning is `config.WarnSyncInvalid`. `brigade sync status` refuses outside a session.

### 4.4 Card 33 — the Syncthing adapter (`internal/syncadapters/syncthing`, `brigade sync-adapter syncthing`)

- **Home:** `<state_dir>/sync/syncthing/` (0700): `config.xml`, `cert.pem`, `key.pem`, `index-*`, plus Brigade's
  `daemon.pid`, `port`, and `refs/<session_id>` files. First `attach` runs `syncthing generate --home … --no-default-folder`
  (argv array; the `syncthing` executable found on `PATH` by `exec.LookPath`), then patches `config.xml`'s
  `<gui>` to `127.0.0.1:<port>` with a port Brigade picks (a free port, persisted) — or passes `--gui-address`
  to `serve`; either way the API is loopback-only and the key stays in the 0600 config.
- **Lifecycle:** `attach` writes `refs/<session_id>`, then, under `adapterkit.LockFile(<home>/lock)`, starts the
  daemon if `daemon.pid` is dead: `syncthing serve --home … --no-browser --no-restart --no-upgrade
  --gui-address 127.0.0.1:<port>`, detached in its own session (`Setsid`), stdout/stderr to `<home>/syncthing.log`
  (rotated by size), and waits up to 10 s for `GET /rest/system/status` to answer. `detach` removes the ref and,
  when `refs/` is empty, SIGTERMs the daemon (SIGKILL after 5 s). A stale pidfile (no such process) is treated as
  stopped. The instance is **per machine**, shared by every session and team on it; folder ids keep teams apart.
- **`apply`:** for each peer, `POST /rest/config/devices` `{deviceID, name: label, addresses: ["dynamic"]}`; for
  each folder, `POST /rest/config/folders` with the object of §3.2 and every current peer in `devices`; devices no
  longer listed stay (harmless; removing them would churn), folders no longer listed are left alone (a project that
  drops a folder stops sharing it on its next `apply` from the *other* side only when that side drops it too —
  documented). A folder whose path is already held by another folder id → reported `state: "conflict_path"`.
- **`status`:** `GET /rest/system/status`, `/rest/system/connections`, `/rest/db/status?folder=<id>` → the result
  shape of §4.3.
- Every REST call: `http.Client` with a 5 s timeout, `X-API-Key` from config, loopback only; bodies are Syncthing's
  own JSON, never logged. Tests: a fake REST server and a fake `syncthing` fixture script launched as `/bin/sh
  <script>` (the repo rule for script fixtures) covering generate/serve/pidfile/refs; the real binary is exercised
  by the smoke test of §5.
- forbidigo: this package joins the exec carve-out in `.golangci.yml` with a comment naming it as the one daemon
  Brigade starts.

**As built (P18-3, 9b2c5fc; merged f66762a) — where the code differs from the bullets above, the code is the
truth:**

- **No `generate`.** v2 has no `--no-default-folder` (§3.2), so the first `attach` runs `syncthing serve --home
  <home> --no-browser --no-restart --no-upgrade --no-port-probing --gui-address 127.0.0.1:<port>` on the fresh home
  and waits (`startWait` 15 s) for `config.xml` to carry `<apikey>` and `GET /rest/system/status` to answer with
  `myID`. The GUI port is a free loopback port picked once and kept in `<home>/port`, re-picked when another
  program holds it. The API key is read from `config.xml` on every call and lives in no other file.
- **The carve-out is one file**, `internal/syncadapters/syncthing/instance.go` (forbidigo `exec.Command` and gosec
  G204), which carries `//go:build darwin || linux` (`Setsid`, `procutil`). The spawn uses a background context
  that is never cancelled, `adapterkit.ChildEnv`, stdin from the null device and both streams appended to
  `syncthing.log`, truncated at start when over 4 MiB.
- **`daemon.pid` is `<pid> <start token>`** (`procutil`), so a reused pid reads as stopped. `attach` and `detach`
  decide under `adapterkit.LockFile(<home>/lock)` (`lockWait` 16 s). The last `detach` asks the daemon to stop
  with `POST /rest/system/shutdown`, then SIGTERM, then SIGKILL, `stopWait` (5 s) apart.
- **`apply`** creates a missing folder directory (0755), posts every peer (a peer Syncthing answers 4xx is skipped
  and left out of every folder's `devices`), and posts each folder with **exactly** the accepted peers as its
  `devices` — a replace, so a device added to the instance by hand is dropped from the folder on the next round. A
  folder Syncthing answers 4xx is `rejected`; `conflict_path` is only for a path held by a **different** folder id.
  No `listenAddresses` patch: the instance listens where Syncthing does by default.
- **`status`** on a stopped instance is `running: false` with empty lists, not an error, and lists every folder and
  every device the instance holds (the harness filters folders to the project's in the human form).
- **Found writing the docs (P18-4), for P18-6:** (a) two checkouts on one machine that list the same folder for
  the same team — a second clone, or another repository of the team listing the same path — derive the same
  folder id (the id carries no repository), and `heldByOther` ignores a same-id entry, so each checkout's `apply`
  re-points the folder at its own path every round; `docs/sync.md` states this as a limit, and whether Syncthing
  treats the re-pointed folder's missing files as deletions was not measured. (b) A watcher killed without its
  exit path leaves `refs/<session_id>` behind and nothing prunes it, so the instance outlives the last session;
  documented. (c) The always-on node of §7 cannot be a Syncthing that Brigade did not start, because (per the
  `apply` bullet) each round replaces a folder's device list with the roster's peers; `docs/sync.md` says a
  machine keeping a session open is the always-on node instead.
- **Fixed (P18-6), superseding (a)–(c) and the "no listenAddresses patch" line above:** `apply` only adds — a
  device the instance already holds is never re-posted, and a folder's `devices` is its existing list plus the
  accepted peers, so a hand-added server stays; a folder id held at a **different** path is `conflict_path` and left
  alone (the harness counts it in the notice, `; 1 folder is held by another checkout`, and `brigade sync status`
  shows it for an engine path that is not this checkout's); `attach` and `detach` take an optional `pid` (the
  watcher's `os.Getpid()`), kept as `<pid> <start token>` in `refs/<session_id>`, and both prune refs whose holder
  is dead (a ref with no pid only by its own detach); the watcher reads the roster every 15 s
  (`DefaultSyncListInterval`) and applies when the peer set changed, once more at the next read after such an
  apply (the introduced peer has connected by then, so the notice's count is fresh), after 60 s, or after a
  failed apply; the instance gets its own sync listen port (`<home>/listen-port`, free for TCP and UDP) set by
  `PATCH /rest/config/options` `listenAddresses` = tcp/quic on it plus the dynamic relay pool, only when it
  differs. Measured with Syncthing 2.1.5: introduction 16.2 s after the first SessionStart (was 60.8 s), the
  notice `1 of 1 peers connected` at 30.8 s, both instances listening on their own ports and on no 22000.
  Syncthing v2.1.5 runs a monitor process even with `--no-restart`: `daemon.pid` names the monitor, and its child
  (same process group) holds the sockets.

### 4.5 Documentation — Brigade's plain manner

- `docs/sync.md` (new): what it does, what it needs (Syncthing on `PATH`, the migration), the `sync` member, what
  is written where (`.stfolder`, `.stversions`, conflict copies), what travels how (peer to peer over Syncthing's
  TLS; discovery and relays; never through the backend), when it runs (while a session is active), how to switch
  it off, `brigade sync status`, and the limits (second clone of a repo on one machine; a folder should be
  gitignored; deletions propagate; large files fine).
- `docs/sync-adapters.md` (new): the protocol of §4.3 for adapter authors, in the manner of `adapter-authors.md`.
- README: a "Sync folders" section beside "Add a repository to the team"; `docs/setup.md`: the Syncthing install
  line, the migration line for administrators of other teams (`make backend-install project=<ref>`), the `sync`
  member;
  `docs/security.md`: §3 (the watcher now starts one daemon and writes into folders the project lists), §5 (sync
  is automatic, outside ask/deny), §10 (what is written into the project directory now; the loopback API; what a
  teammate's machine can do — write and delete anything in a listed folder), a short new "File sync" section;
  `docs/development.md` (the forbidigo carve-out; `syncthing` for the smoke test); CHANGELOG `0.11.0`;
  `plugin/README.md`; the execution log rows.

## 5. Proof before release

1. `make test`, `make lint`, `make plugin-check`, `make deps-check`, `make schema-check`, `make tidy-check`;
   `make test-integration` and `make test-db` against the local stack when Docker is available.
2. **Smoke with the real binary**, two personas on this machine: two config dirs, two state dirs (so two Syncthing
   instances on two ports), two checkouts of a throwaway repo with a `sync` member, the fs adapter as backend;
   start both sessions' watchers in sink mode (`make e2e`'s rig or by hand), confirm both publish a `sync_peer`,
   both `apply`, the instances connect over local discovery, a file written in one checkout's folder appears in
   the other's, and a conflict produces a `.sync-conflict-*` copy; stop one session and confirm its instance stays
   (the other session still references it), stop both and confirm it exits.
3. An adversarial review of the diff (Fable): the team-file parser, the REST key handling (never on argv, never
   logged), loopback binding, argv arrays and the exec carve-out, path handling of `sync_root`, the migration's
   signatures and pins, the docs against the tree.
4. `make release version=0.11.0 card=33`; watch `release.yml` publish; the release-notes workflow follows.
5. **Apply the migration to both hosted projects** — `make backend-install project=wmgtaraqmoufmrnyojzf` and
   `make backend-install project=hsopqzvznxajeyswzztv` (the PATs live in `CLAUDE.user.md`, read by the target's
   own mechanism, never pasted on argv) — then confirm `describe` from each team advertises `session.sync_peer`.

## 6. Rows

| ID | Task | Model | Depends on |
| --- | --- | --- | --- |
| P17-1 | Card 32: the team file opens — ignored members, the `sync` member, unusable-never-refuses, `MaxBytes`, `--force`, the package doc, tests (§4.1) | Fable | — |
| P17-2 | Card 32: hook lines and `resolve_test`, CHANGELOG entry | Opus | P17-1 |
| P18-1 | The `sync_peer` wire member end to end, one commit (§4.2) | Opus | — |
| P18-2 | The sync-adapter protocol doc and the harness side: map members, SessionStart, the watcher's `sync` goroutine, `brigade sync status`, the `sync` option (§4.3) | Opus | P17-1, P18-1 |
| P18-3 | The Syncthing adapter and `brigade sync-adapter syncthing` (§4.4) | Opus | P18-2 |
| P18-4 | Documentation (§4.5) and the execution-log rows | Opus | P18-2, P18-3 |
| P18-5 | The real-binary smoke (§5.2) | Opus | P18-3 |
| P18-6 | Adversarial review of the whole diff (§5.3); fixes | Fable | P18-4, P18-5 |
| P18-7 | `make release version=0.11.0 card=33`; verify the published release; apply the migration to both hosted projects and verify the capability; Trello 32 and 33 updated | Opus | P18-6 |

## 7. Out of scope, on purpose

An always-on node (as planned: a team installs Syncthing on a server and shares the same folder ids. As built,
each `apply` replaces a folder's device list with the roster's peers, so a Syncthing Brigade did not start is
dropped within a round; `docs/sync.md` names the working form — a machine that keeps a session open in a checkout
— and the plan's form is P18-6's to rule on, §4.4 "Found writing the docs"); per-folder receive-only modes; a Brigade-bundled Syncthing binary;
typed message bodies; anything the earlier transport design specified.
