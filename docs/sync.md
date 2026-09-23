# Syncing folders

A project can list folders in its `.brigade.json`, and every checkout of that repository on the team keeps them in
step: a file one teammate writes into `docs/shared` appears in every other teammate's `docs/shared`. Brigade does
not carry the files. It runs [Syncthing](https://syncthing.net) on each member's machine and tells it which
teammates' machines to connect to, from the team roster; Syncthing moves the files peer to peer. Sync runs while a
session is active on the machine, and stops when the last one ends.

What a teammate's machine can do in a listed folder on yours, and what the network and the backend see, is
[docs/security.md](security.md), section 12.

## What it needs

- **Syncthing on `PATH`**, on every machine that syncs. Brigade does not ship it:

  ```sh
  brew install syncthing        # macOS (Homebrew)
  sudo apt install syncthing    # Debian, Ubuntu
  ```

  Measured with Syncthing 2.1.5. A machine without it still connects to the team, and a prompt soon after says
  file sync is off (below).
- **The migration `20260923022323_session_sync_peer.sql`** on the team's backend, applied once by the
  administrator with `make backend-install project=<ref>` ([docs/setup.md](setup.md), "File sync needs
  `20260923022323`"). It stores each session's `sync_peer` — its machine's Syncthing device id — in the roster.
  On a backend without it every session still connects, but no peer is stored, so no checkout is ever introduced
  to another and nothing syncs.
- **A `sync` member in `.brigade.json`**, committed by the project.

Nothing else: no port to open, no account, no setting on the member's side.

## The `sync` member

```json
"sync": { "adapter": "syncthing", "folders": [".context/plans", "docs/shared"] }
```

- **`folders`** — at most 32 entries, each at most 128 bytes. An entry is a path relative to the repository's top
  level, written exactly as Go's `path.Clean` writes it: `docs/shared` and `.context/plans` are folders;
  `/srv/shared` (absolute), `./docs`, `docs/`, `docs//shared`, the empty string and `.` (the whole checkout) are
  not. Nothing else is asked of an entry. A listed folder that does not exist yet is created (mode 0755) the
  first time the session syncs.
- **`adapter`** — optional; `syncthing`, the bundled adapter, when absent. Any other name runs
  `brigade-sync-<name>` from each member's `PATH` ("Other adapters", below).
- Any other member inside `sync` is ignored and named in the session-start line about ignored members.

A `sync` member that breaks a rule never stops a session: the session connects without file sync, with one line
naming the first rule broken —

```
Brigade: .brigade.json's sync member is not usable (folder_not_relative); file sync is off for this session.
```

— where the reason is one of `not_object`, `adapter_invalid`, `folders_invalid`, `too_many_folders`,
`folder_too_long`, `folder_not_relative`, `folder_not_clean` and `folder_root`, checked in that order.

The project owns the list: whoever can commit `.brigade.json` decides what every checkout syncs, and a member can
only switch it off. A changed list takes effect in each session at its next session start. `brigade team create
--force` carries the member into the file it writes; when it cannot — the old member was not usable, or the old
file was refused — it writes the new file without it and says so in one line:

```
the previous .brigade.json's sync member was not carried into the new file (<reason>); add it again if this project syncs folders
```

## What you see

**At session start**, after the line that names your team, one line says whether this session syncs:

```
Brigade: file sync on: 2 folder(s) through syncthing.
```

or why it does not:

```
Brigade: file sync off (the sync option is off).
Brigade: file sync off (no folders listed).
Brigade: file sync off (the checkout's toplevel could not be resolved).
```

A project with no `sync` member gets no line.

**At a prompt**, the session's watcher reports through its one notice line, printed once at the next prompt and
again whenever it changes:

```
Brigade sync: 2 folders, 1 of 3 peers connected
```

The line follows each round the watcher applies (below), so a teammate who has just connected shows at the next
one. When another checkout on this machine holds one of the folders ("Limits"), it ends in one more clause:

```
Brigade sync: 2 folders, 1 of 3 peers connected; 1 folder is held by another checkout
```

A machine without Syncthing sees this instead, and that session carries on without file sync until its next start:

```
Brigade sync: the syncthing sync adapter is not usable (unavailable: syncthing is not on PATH; install it to sync folders); file sync is off for this session
```

**`brigade sync status`**, inside a session, shows the engine, this project's folders and the peers:

```
sync: syncthing, engine running, this machine's peer MFZWI3D-BONSGYC-YLTMRWG-C43ENR5-QXGZDMM-FZWI3DP-BONSGYY-LTMRWAD
FOLDER                  STATE
brigade/docs            idle
brigade/.context/plans  not shared yet
PEER             CONNECTED
bob@example.com  yes
[XYZ1234]        no
```

A folder is labelled `<repository>/<folder>`. Its state is Syncthing's own word (`idle`, `scanning`, `syncing`,
`error`, …), or `not shared yet` before the watcher's first round, `rejected` when Syncthing refused the folder,
and `conflict_path` when another checkout on this machine holds it — the instance has the folder at that
checkout's path — or another folder id already holds this path. A peer is
labelled with its session's label from the roster, or the first seven characters of its device id in brackets
when the roster names none; the list is every device the instance knows, which can include peers of another
project on the same machine. `--json` carries the same, plus every folder the instance holds and the ids in full.
In a session that syncs nothing it prints `file sync is off for this session (the Brigade line at session start
says why)`; outside a session it refuses, because it reads the session's map.

## How it works

- **Who syncs with whom.** Each session publishes its machine's Syncthing device id in the roster, as its
  `sync_peer` (`syncthing:<device id>`). Every 15 seconds the watcher reads the roster, and whenever the devices it
  finds have changed — and at least once a minute regardless — it shares the listed folders with the device of
  every other session of the team **in the same repository** — the same `REPO` name in `brigade sessions` —
  online or offline; Syncthing connects to each whenever both are up. A teammate who starts a session is
  introduced within about 15 seconds. A session that sends no repository name
  (`share_workspace_label` off) introduces nobody, and nobody introduces it.
- **What travels how.** Files go directly between two machines' Syncthing instances, over Syncthing's own TLS,
  each device authenticated by its certificate. Syncthing's defaults are kept as they are: global discovery
  (Syncthing's public discovery servers learn each device's id and addresses), local discovery on the network,
  NAT traversal, and relays when no direct connection is possible (a relay forwards the encrypted stream). One
  default is changed: the instance listens for peers, over TCP and QUIC on every interface, on a port of its own
  that Brigade picks when it first starts and keeps in `listen-port`, instead of Syncthing's 22000 — so it never
  shares a port with a Syncthing you run yourself. Discovery announces that port, so nothing needs to be opened
  by hand; where a firewall stops it, relays carry the connection. The
  backend stores only the `sync_peer` string. No file, file name or folder name passes through Brigade's backend.
- **The folder id.** Every checkout derives the same Syncthing folder id for a folder without exchanging it:
  `brigade-`, the first 8 characters of the team reference with its dashes removed, `-`, and the first 12 hex
  digits of the SHA-256 of the folder exactly as `.brigade.json` writes it — team `6f0f2b41-5a3c-…` and folder
  `docs` give `brigade-6f0f2b41-46b42b4229cd` ([docs/sync-adapters.md](sync-adapters.md), "Folder ids"). The id
  names the team and the folder, not the repository.
- **The instance.** Brigade runs a Syncthing of its own, never yours: its home is
  `~/.local/state/brigade/sync/syncthing/` (under `XDG_STATE_HOME` when that is set; mode 0700), holding
  Syncthing's certificate — the device id —, `config.xml`, database and `syncthing.log`, and Brigade's
  `daemon.pid`, `port`, `listen-port` and `refs/`. The first session on the machine starts it, each session holds
  a reference in `refs/` naming its watcher's process, and the last session to end stops it; a reference whose
  watcher died without ending its session is dropped the next time any session on the machine starts or ends.
  One instance serves every session and every team on the
  machine. Its REST API and web GUI listen on 127.0.0.1 only, on a port Brigade picks and keeps in `port`; the
  API key stays in `config.xml`. The adapter's own diagnostics go to `~/.local/state/brigade/logs/sync-syncthing.log`.
- **When it runs.** Only while a session on the machine is active. Two machines exchange changes while both have
  a session running; a change made while the other is away arrives the next time both are.

**Always on is not built in, and two ways give it.** A team that wants a copy that is always there can keep a
session open on a machine that stays up — Claude Code left running in a checkout of the repository on a server:
that session is on the roster like any other, so every teammate's session introduces it, and it holds the
folders while everyone else is away. Or it can run a plain Syncthing on a server, sharing a folder under the same
folder id (`brigade sync status --json` shows it), and add that server as a device to each member's Brigade
instance and folder by hand, in the instance's web GUI (on 127.0.0.1, at the port in `port`). Brigade only ever
adds devices: a device added by hand, to the instance or to a folder, stays as it was added, round after round.

## What is written in your checkout

- The listed folders, created when missing, and whatever teammates' machines put in them.
- `.stfolder` in each listed folder: Syncthing's marker that the folder is present.
- `.stversions` in each: the trash can. A file that a teammate's change replaces or deletes on your machine is
  moved there first and kept 14 days.
- Conflict copies. When two machines changed one file before either saw the other's change, Syncthing keeps one
  version as the file and the other beside it as `<name>.sync-conflict-<date>-<time>-<device>.<ext>`.
- `.syncthing.<name>.tmp` files while a file is arriving.

**Deletions propagate.** A file deleted in one checkout is deleted in every other, and lands in each of their
`.stversions`.

**Git sees all of it.** Brigade does not check whether a folder is tracked, and a tracked folder syncs like any
other, so a teammate's edit shows in your `git status` as a change of yours. List folders the repository ignores,
and add each one to `.gitignore`.

## Switching it off

Set the plugin option **`sync`** to `off` — from `/plugin` in a session, or on the command line:

```sh
claude --settings '{"pluginConfigs":{"brigade@brigade":{"options":{"sync":"off"}}}}'
```

From the next session start that session syncs nothing and starts no Syncthing. `on` is the default. Any other
value is off, with one line:

```
Brigade: sync must be "on" or "off"; the value set is neither, so file sync is off for this session.
```

The option is yours and the folder list is the project's: the option can switch sync off, never add a folder.

## Other adapters

`"adapter": "<name>"` in the `sync` member runs `brigade-sync-<name>`, found on each member's `PATH`, in place of
the bundled Syncthing adapter. Sessions are only introduced to sessions that use the same adapter name. A machine
without the executable connects without file sync, and a prompt soon after says:

```
Brigade sync: brigade-sync-<name> is not on PATH; file sync is off for this session
```

The protocol an adapter speaks — five verbs, one JSON document in and one out — is
[docs/sync-adapters.md](sync-adapters.md).

## Limits

- **One checkout per folder id on a machine.** One Syncthing instance holds a folder id at one path. Two checkouts
  on one machine that list the same folder for the same team — a second clone of the repository, or another
  repository of the team that lists the same path — derive the same id. The checkout whose session shared the
  folder first keeps it; the other leaves it where it is, shows `conflict_path` for it in `brigade sync status`,
  and its notice line says `1 folder is held by another checkout`. That checkout's folder does not sync. To move
  the folder to it, remove the folder from the instance in its web GUI; the next round of whichever session gets
  there first shares it from that session's checkout. A checkout that was deleted keeps holding the folder the
  same way until it is removed.
- **A crash is cleaned up by the next session.** A session whose watcher is killed without its exit path
  (`SIGKILL`) leaves its reference in `refs/`. The reference names the watcher's process, so the next session to
  start or end on the machine drops it, and the instance stops with the last live session. Until then — or after a
  power loss, when no session ever starts again — the instance keeps running; delete the files in
  `~/.local/state/brigade/sync/syncthing/refs/` with no session running, and the next session's end stops it. A
  reference written by a Brigade before this change names no process and is dropped only by its own session's end.
- **Your own Syncthing is a separate instance.** A Syncthing you already run has its own configuration and device
  id; Brigade never reads or changes it. Brigade's instance listens for peers on its own port (`listen-port`), not
  on Syncthing's default 22000, so the two never share a port.
- **Brigade limits no file size and no file count.** What Syncthing handles, syncs.
