# Turn on the Brigade Board

```json
"sync": { "folders": [".context/plans"], "board": true }
```

The **Brigade Board** is a team's board of work items. A team that sets `"board": true` in the `sync` member of
`.brigade.json` keeps its work in Brigade itself: work items in the lists of one board, read and changed with
`brigade work-item` ([Keep the team's work on the Brigade Board](../../team-member/use/work-items.md)). The board is the team's, not a
repository's. It needs no listed folder: `"sync": { "board": true }` alone is a project that syncs nothing but
the board.

## The rules

- **Every repository of the team** that should see the board carries the member in its own `.brigade.json`. A
  session reads the file of the checkout it started in; a repository whose file has no `board` has sessions with
  no board, beside teammates who have one.
- `board` is `true` or `false`. Anything else makes the whole `sync` member unusable (`board_invalid`): the
  session connects, without file sync, and says why.
- **Every member's plugin must be newer than 0.49.0.** An older one ignores the member, says so at session start
  (`.brigade.json carries members this version does not define (sync.board); ignored`), syncs the listed folders
  as before and has no board; with no listed folder it says `file sync off (no folders listed)`.
- **Each machine needs `syncthing` on its PATH**, as for any synced folder
  ([Set the folders to sync](sync-folders.md)). A member whose `sync` option is off has no board.
- **Nothing to apply to the backend.** The board asks nothing of the message adapter: no migration, no table,
  no capability.

## Where it is

The board is one folder per team on each member's machine, under `boards/` in Brigade's state directory
(`~/.local/state/brigade` on macOS and Linux, `%LOCALAPPDATA%\brigade` on Windows) and named after the team's
reference, outside every checkout: the sessions of every repository of the team on one machine read and write
the one folder, and it stays where it is when the team file is pointed at another backend URL. Its sync folder id is
derived from the team alone, so every machine arrives at it without exchanging anything:

```
"brigade-" + first 8 characters of team_ref with its dashes removed + "-" + first 12 hex digits of SHA-256("/board")
```

Team `6f0f2b41-5a3c-…` gives `brigade-6f0f2b41-10ec23840280`. The folder is shared with the machine of every
teammate's online session, whatever repository that session is in; a project's listed folders stay with the
sessions of its own repository. `brigade sync status` shows the board as `team board`.

A sync adapter of the team's own gets the board in `apply` as a folder marked `"team": true`, with the team's
peers in `team_peers` and the checkout's toplevel in `root` ([Writing a sync adapter](../../sync-adapters.md#apply)).

## The lists

A board starts with `Todo`, `Doing` and `Done`. The lists are the team's own: any member names them anew, as a
whole and in order, and renames one without moving a work item
([The board's lists](../../team-member/use/work-items.md#the-boards-lists)):

```bash
brigade work-item lists Backlog Doing Review Done
brigade work-item rename-list review 'In review'
```

A list is a state with a key, set once from its first name and never changed, and a name, which can change.
Up to 24 lists, each name up to 64 characters. A work item in a list the new ones leave out stays where it is.
A board holds 20,000 work items and a work item 2,000 changes; past either, `brigade work-item` refuses
(`conflict`, `board_full`) rather than write what would not be read.

## What the folder holds

Changes, never a work item as a file. Each act — adding a work item, moving it, a comment — is one small JSON
file, written once and never rewritten: `items/<id>/<sequence>-<time>-<random>.json`, and
`lists/<sequence>-<time>-<random>.json` for the board's lists. A work item is what its changes add up to, the
last change of a field winning. Two machines therefore never write the same file, and the sync engine makes no
conflict copy. Nothing is ever removed: a closed work item stays in the folder, and so does every change.

## When it travels

Two machines exchange work items only while both run a session at the same time, as for any synced folder. A
team whose members are never online together converges through a machine that overlaps them: one that keeps a
session open, in any checkout of the team, is the always-on node.

## Turning it off

Remove the member, or set `"board": false`, and commit. Sessions started afterwards have no board and
`brigade work-item` refuses (`config`, `no_board`). The folder and its files stay on every machine, and a sync
engine that already shares the folder goes on carrying it while it runs for another folder.
