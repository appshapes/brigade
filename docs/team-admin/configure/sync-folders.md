# Set the folders to sync

```json
"sync": { "adapter": "syncthing", "folders": [".context/plans", "docs/shared"] }
```

Every session in a checkout of the repository then keeps those folders in step with the teammates' checkouts,
peer to peer through Syncthing, while the session is active. The project owns the list: a member can only switch
sync off (`sync: off`). What a member sees and how it works: [Sync folders](../../team-member/use/sync-folders.md).

## Before you commit it

- **Every member's plugin at 0.11.0 or later.** A plugin of 0.10.0 or earlier reads the file against a closed
  schema and refuses it whole: its sessions say `Brigade: not connected (config: team_file_unknown_field)` and
  connect to nothing until that member updates. The VERSION column of `brigade sessions` shows who is behind.
- **The backend has the migration `20260923022323_session_sync_peer.sql`**, applied by the Project Owner with
  `make backend-install`. Without it no peer is stored and nothing syncs.
- **Each machine has `syncthing` on its PATH**; a machine without it connects and says file sync is off.

## The rules

- `folders`: at most 32 entries, each at most 128 bytes, a path relative to the repository's top level written as
  Go's `path.Clean` writes it (`docs/shared`, `.context/plans`; not `/srv/shared`, `./docs`, `docs/`, `.`). `..` is
  allowed, so a folder may sit beside or above the checkout; [docs/security.md, section 12](../../security.md#12-file-sync)
  says what that exposes. A listed folder that does not exist is created at the first sync.
- `adapter`: optional; `syncthing` when absent. Another name runs `brigade-sync-<name>` from each member's PATH
  ([docs/sync-adapters.md](../../sync-adapters.md)).
- A member that breaks a rule never stops a session: it connects without file sync and names the first rule
  broken (`folder_not_relative`, `too_many_folders`, …).
- List folders the repository ignores and add each to `.gitignore`: a tracked folder syncs like any other, and a
  teammate's edit then shows in everyone's `git status`.

## Changing the list

A changed list reaches a session at its next start. A folder added is shared at that session's first round. A
folder removed is paused on each machine that shared it; its files stay, and `brigade sync status` shows it as
`no longer listed (paused)`. Every folder removed, or the member removed, stops the session syncing and pauses
nothing. `team create --force` carries the member into the file it rewrites, and says so in one line when it
cannot.

## What this opens

A teammate's machine can write, overwrite and delete anything in a listed folder on every member's machine, with
no prompt and no review; a folder a tool reads as configuration (`.claude`, `.git`) can be listed. The join secret
is the boundary, as it is for messages.
