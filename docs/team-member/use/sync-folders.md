# Sync folders

A project can list folders in its `.brigade.json`, and every checkout of that repository on the team keeps them in
step: a file one teammate writes into `docs/shared` appears in every other teammate's `docs/shared`. Brigade does
not carry the files. It runs [Syncthing](https://syncthing.net) on each member's machine and introduces it, from
the roster, to the machine of every teammate's online session in the same repository; Syncthing moves the files
peer to peer, over its own TLS, directly or through a relay. Sync runs while a session is active on the machine and
stops when the last one ends.

## What you need

- `syncthing` on PATH: `brew install syncthing` (macOS), `sudo apt install syncthing` (Debian, Ubuntu); measured
  with 2.1.5. Without it the session connects and a prompt soon after says file sync is off.
- A plugin at 0.11.0 or later, and on the same side of 0.16.0 as every teammate's
  ([Know what an update changes](../update/what-an-update-changes.md)).
- Nothing else: no port to open, no account, no setting on your side. The list is the project's
  ([Set the folders to sync](../../team-admin/configure/sync-folders.md)); `sync: off` is your one switch.

## What you see

At session start, after the line that names your team: `Brigade: file sync on: 2 folder(s) through syncthing.`,
or why not (`the sync option is off`, `no folders listed`, `no watcher runs without an inbox socket`, …). At a
prompt, the watcher's own notice line: `Brigade sync: 2 folders, 1 of 3 peers connected`, with `; 1 folder is held
by another checkout` or `; 1 folder no longer listed is paused` when those apply. A machine without Syncthing sees
`Brigade sync: the syncthing sync adapter is not usable (unavailable: syncthing is not on PATH …)`.

`brigade sync status`, inside a session, shows the engine, this project's folders and the peers:

```
sync: syncthing, engine running, this machine's peer MFZWI3D-…
FOLDER                  STATE
brigade/docs            idle
brigade/.context/plans  not shared yet
PEER             CONNECTED
bob@example.com  yes
[XYZ1234]        no
```

A folder's state is Syncthing's own word, or `not shared yet` before the first round, `rejected`, `conflict_path`
(another checkout on this machine holds it) or `no longer listed (paused)`. `--json` carries the ids in full.

## What is written in your checkout

The listed folders (created when missing) and whatever teammates' machines put in them; `.stfolder` in each (the
marker); `.stversions` in each (the trash can: a file a teammate's change replaced or deleted is kept there 14
days); conflict copies, `<name>.sync-conflict-<date>-<time>-<device>.<ext>`, when two machines changed one file
before either saw the other's change; `.syncthing.<name>.tmp` while a file arrives. **Deletions propagate.** Git
sees all of it: a tracked folder syncs like any other, so list folders the repository ignores and add each to
`.gitignore`.

## How the pieces fit

- **Who syncs with whom.** Each session publishes its machine's Syncthing device id to the roster. Every 15 s
  (the team's `polling.roster_seconds`) the watcher reads the roster and shares the listed folders with the
  device of every other online session of the team in the same repository (the REPO name). A session that sends no
  repository name introduces nobody and is introduced to nobody.
- **The folder id** is derived on every checkout without exchange: `brigade-`, the team reference's first eight
  characters, and a hash of `<repository>/<folder>`. Two repositories of one team can list the same folder and each
  syncs its own.
- **The instance** is Brigade's own Syncthing, never yours: home `~/.local/state/brigade/sync/syncthing/`, 0700;
  its REST API and web GUI on 127.0.0.1 only, at the port in `port`; peers on a port of its own in `listen-port`,
  never 22000. The first session on the machine starts it, the last to end stops it; one instance serves every
  session and team on the machine.
- **When.** Only while a session on the machine is active; a change made while the other side is away arrives the
  next time both are. For a copy that is always there, keep a session open on a machine that stays up, or run a
  plain Syncthing on a server sharing the folder under the same id (`brigade sync status --json` shows it) and add
  it by hand in the instance's web GUI; a device added by hand stays.

## Limits

- One clone of a repository per machine syncs a folder; the second shows `conflict_path` and
  `1 folder is held by another checkout`. Remove the folder from the instance in its web GUI to move it.
- Two repositories with the same name share their ids; a checkout whose name differs (a fork, a different
  directory name with no remote) syncs with nobody.
- A folder the project stops listing is paused, its files stay; remove it in the web GUI to be rid of it.
- A crash (`SIGKILL`) leaves a reference the next session drops; after a power loss, delete the files in
  `~/.local/state/brigade/sync/syncthing/refs/` with no session running.
- No file size or count limit of Brigade's; what Syncthing handles, syncs.
- Updating to 0.16.0: Brigade moves each folder to its new id by itself, no file touched; until the last member
  has updated the team is in two groups that exchange nothing, and when they meet, a file deleted in one group
  comes back from the other.

Another sync engine: `"adapter": "<name>"` in the `sync` member runs `brigade-sync-<name>` from each member's
PATH ([docs/sync-adapters.md](../../sync-adapters.md)). What file sync opens, and that no permission rule sees it:
[docs/security.md, section 12](../../security.md#12-file-sync).
