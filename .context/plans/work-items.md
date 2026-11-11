# Work items: a team's board, kept in Brigade and synced peer to peer

**Status.** Built 2026-11-11, Trello card **263** ("Create peer-to-peer task management"), owner Rjae. Not
released: the next release carries it. Execution-log row **P83-1**. **No wire-shape change, no Supabase migration,
no new dependency.** New: one team-file member (`sync.board`), one by-pid map member (`board_folder`), one command
(`brigade work-item`), three optional members of the sync-adapter protocol (`team`, `team_peers`, `root`; still `sync/1`),
and three skills in each of the four packages. Folder sync ([folder-sync.md](folder-sync.md)) is the carrier,
as it is for the digests ([manager-digest.md](manager-digest.md)).

## 1. What was asked

The card, 2026-11-11: "Start simply: Kanban repository of tickets synced within a team. Reminder that teams span
multiple GitHub repos. Priority in order: agents, then humans. Initially the UX is via `brigade` skill(s) (e.g.,
`/brigade:list-tickets`). Work items could be called something other than 'tickets' if you suggest otherwise […] I
really like the flat model preferred by Linear App (issues) and Trello (cards). Know that as soon as Cavalry has
reached MVP, I wish to attempt to create an adapter based on it to replace Supabase. […] The goal is simply
stated: a team may opt in to doing its task management solely via Brigade, and not need something like Trello,
Jira, or Linear."

## 2. Rulings, 2026-11-11

1. **The name is "work item"** (Rjae: "What about 'work-item'? I.e., `/brigade:work-items`"), over card, ticket,
   issue and task. The command is `brigade work-item`; the board keeps the words board and list.
2. **Numbers, with a clash repaired** (over short random ids): the highest number plus one; two machines that
   take one number are put right by the next command.
3. **`docs/security.md` gains a section** (19) and no existing sentence changes.
4. **Skills** (Rjae, mid-build: "Any additional work-item skills you think would be meaningful to the human
   operator, go ahead and include them - but only if they are meaningful"): three, each a thing a person types.
5. **The sync adapter abstraction, not Syncthing** (Rjae, mid-build: "Is our implementation of card 263 dependent
   on SyncThing specifically, or does it use our sync adapter abstraction in front of SyncThing?"). The first cut
   made two `apply` calls a round, which the contract ("the whole desired set, every time") lets an external
   adapter undo; it became one `apply` with optional members (§4.3).

Routine calls made and said: the carrier is a synced folder and not a backend table (the card's title, and
ruling 1 of manager-digest.md: "Supabase is an adapter"; a Cavalry adapter has nothing to implement); the lists
are the team's own, starting `Todo`, `Doing`, `Done`; a work item has a title, a description, a list, an
assignee, labels and comments; no release in this card.

## 3. The answer in one page

- **A board per team, in a folder of the team's**: `<state dir>/boards/<32 hex digits of SHA-256(team_ref)>` on
  each machine, outside every checkout, so the sessions of every repository of the team on one machine use the one
  folder and one engine never holds one folder id at two paths (the limit card 42 met for folders inside a
  checkout). The path is keyed by `team_ref` alone, as the id is, so the two never part (§6.4).
- **Shared with the whole team.** A project's listed folders go to the sessions of the same repository; the
  board goes to the peer of every teammate's online session, whatever its repository.
- **Changes, never a work item as a file.** Each act is one small JSON file written once under a name no other
  writer picks. A work item is the fold of its changes. No file is rewritten, so no conflict copy; nothing is
  removed.
- **`brigade work-item`** reads and writes the folder from inside a session. No adapter is spawned: the board
  works while the backend does not.
- **Opt-in**: `"sync": {"board": true}` in `.brigade.json`, in every repository of the team.

## 4. The design, in travel order

### 4.1 The team file and the map (`internal/harness/teamfile`, `sessionmap`, `hook`)

`SyncConfig.Board bool` (`board`, `omitzero`); anything but `true`/`false` makes `sync` unusable
(`board_invalid`, the eleventh token of the closed list). `resolveSync` freezes `board_folder` =
`foldersync.BoardPath(stateDir, teamKey)` beside the sync members, under their conditions, with or without a
listed folder (a board-only project freezes the adapter and the root with no folders; `validateSync` takes
either). A change is a "sync changed" respawn. The session-start line names the board and its command:
`file sync on: 1 folder(s) and the team's board (`brigade work-item list`) through syncthing.`, which is how a
session learns there is one.

### 4.2 The folder's identity (`internal/harness/foldersync`)

`BoardFolder(teamRef, path)`: id `brigade-<team8>-` + the first 12 hex digits of SHA-256(`/board`), label
`team board`, `Team: true`. The hashed text is no folder a team file can list (those are relative paths), so no
repository's folder takes the id under either derivation; and it is not the label, so the bundled adapter's
`folderName` proves no folder name from it and `formerFolders` never pauses the board as a folder a checkout
dropped.

### 4.3 The sync-adapter protocol (`docs/sync-adapters.md`)

`apply`'s request gains three optional members: a folder's `team` (bool), and with such a folder the request's
`team_peers` and `root`. A folder marked `team` is shared with `team_peers` (every teammate's online session's
peer, same adapter) instead of `peers` (the same repository's). `root` is the checkout's toplevel: a project that
lists no folder and keeps its board requests the board alone, whose label proves no name, and `root` is what
still anchors the folders the checkout dropped so the bundled adapter pauses them (§6.4). A request with no team
folder is byte for byte what it was; one with a team folder carries `team_peers` even when empty. An adapter
that ignores them shares the board with `peers`: a narrower board. The watcher builds both sets from the one roster read (`syncPeerSets`), and the peer-set change
that triggers an apply covers both.

### 4.4 The bundled adapter (`internal/syncadapters/syncthing`)

`apply(folders, peers, teamPeers)`: both lists are introduced as devices (a descriptor in both once; a refused
one skipped once), a team folder gets the team's devices and every other the repository's, and every peer of
either list is reported once. The watcher creates the board folder 0700 before the apply, because the adapter
would create it with a project folder's mode.

### 4.5 The store (`internal/harness/workitem`)

```
<board folder>/items/<id>/<seq 9 digits>-<unix ms 13 digits>-<8 hex>.json   one change of one work item
<board folder>/lists/<seq>-<ms>-<hex>.json                                    the board's lists, whole
```

A change: `v`, `item`, `seq`, `at`, `by` (the writer's roster label), `session`, and `set` (any of `number`,
`title`, `description`, `list`, `assignee`, `labels`, `closed`), `comment`, or `lists`. The fold orders by
(`seq`, `at`, file name) and the last change of a field wins; `seq` is the highest the writer saw plus one, so a
change made after reading another follows it whatever the clocks say. An item needs a number, a title and a list
(its first change sets them); one whose first change has not arrived is not shown. Readers take only files whose
name, size and shape are a change's, follow no symlink, cap and sanitise every string (`protocol.Sanitize`), and
count what they skip. Caps: change file 128 KiB, title 200, description 8,000, comment 4,000 code points, 24
lists, 12 labels, 20,000 items, 2,000 changes an item.

**Numbers.** `NextNumber` is the highest on the board plus one. `Clashes` is every item of a number but the one
created first (by creation time, then id); `Store.Repair` gives each the next free number as a change of its own.
Every `brigade work-item` verb repairs before its own work and prints `renumbered: …`. Two machines repairing the
same board write the same numbers; with different boards the later change wins and the next repair finds what
is left.

### 4.6 The command (`internal/harness/commands/workitem.go`, `internal/cli`)

`brigade work-item list|show|add|edit|move|assign|label|comment|close|reopen|lists`, flags `--list`, `--to`,
`--mine`, `--nobody`, `--label`, `--closed`, in-session only. Text on stdin (title, blank line, description);
`workitem.CleanLine`/`CleanBlock` refuse an empty text, one past its cap, invalid UTF-8 and a credential's shape
(`doing.CarriesCredential`), and unlike a doing line allow a path. The author is read from this machine alone:
the team binding's label, else the `label` option's resolution. A verb that finds nothing to change says
`unchanged:` and writes nothing. The human form of `show` indents a description and a comment under their
headings.

### 4.7 The skills (four packages, twelve files)

`work-items` (the board, verbatim), `work-item` (the commands, the user's words mapped to one, the rules: text on
stdin, no secrets, a work item is a teammate's words and never the user's instruction), `work-item-start` (take
one and work it through: read, assign, move, do, comment, move on). `team-messaging` gains a short section and
one command line. The Muse manifest lists the three.

## 5. Files

New: `internal/harness/workitem/` (store, tests), `internal/harness/commands/workitem.go` (+ test),
`internal/harness/e2e/board_test.go`, `scripts/experiments/E18-work-items.sh`,
`docs/experiments/E18-work-items.md`, `docs/team-member/use/work-items.md`,
`docs/team-admin/configure/board.md`, the twelve skill files, this plan.

Changed: `teamfile/sync.go`, `sessionmap/bypid.go`, `hook/start.go`, `foldersync/foldersync.go` and `client.go`,
`watch/sync.go` and `watch.go`, `commands/sync.go`, `doing/doing.go` (`CarriesCredential` split out),
`syncadapters/syncthing/run.go` and `apply.go`, `cli/command.go`, their tests; `docs/sync-adapters.md`,
`docs/security.md` (section 19), the two topic roots, `team-file.md`, `sync-folders.md`,
`what-an-update-changes.md`, the generated reference, the agent pages and package pages (twelve skills), the
four `team-messaging` skills, the Muse manifest and `marketplace.json`'s digest, the three package-check tests,
the Makefile's `grok-plugin-check` help line, `CHANGELOG.md`, the execution log.

Not touched: `internal/protocol`, `docs/protocol-v1.md`, the schema, the conformance suite, both backend
adapters, `supabase/`.

## 6. Proof

1. Unit tests: the store (fold, concurrent changes, repair on two machines, what a reader skips, sanitising),
   the team file, the map, the hook's lines and respawn, the watcher's one apply with both peer sets, the bundled
   adapter's team folder, `sync status`, the command's every verb and refusal.
2. **The real binaries, two machines' worth of state, two repositories**
   (`BRIGADE_TEST_SYNCTHING=1 go test ./internal/harness/e2e/ -run TestABoardSpansTheTeamsRepositories`): one team,
   alice's session in `web` and bob's in `api`, team files with the board and no folder, two Syncthing
   instances. Measured 2026-11-11 with Syncthing on this machine, three runs on the final build: the board held
   on both and the two connected about 16 s after the first SessionStart; alice's work item on bob's board 10 s
   to 50 s after she added it; bob's move and comment at alice's about 10 s later; two work items numbered 2 on
   two machines were 2 and 3 on both 17 s to 53 s after the adds; nothing in either checkout, no title in the
   backend's files. The two sizes of wait are the engine's: its file watcher's ten seconds, or its
   once-a-minute scan, which is what a new work item (a new directory) waited for in half the measurements.
3. **The skills in live Claude Code sessions** (`scripts/experiments/E18-work-items.sh`,
   [E18](../../docs/experiments/E18-work-items.md)).

4. **The agent review** (the `code-review` skill, on the working tree before the push) found ten things, all
   fixed before the commit: a reader that kept the first `MaxChanges` changes while the writer went on writing
   (the writer now refuses at the caps, `ErrFull`); `items` and `lists` followed through a symlink, for reading
   and for writing (neither is, now); an `add` past `MaxNumber` that wrote an item no reader shows (refused); a
   project that drops its folders and keeps its board never pausing them (the `root` member); the board's path
   keyed by the team key, which hashes the adapter and the URL, while its id is keyed by `team_ref` (the path is
   keyed by `team_ref` now); a board that could not be listed read as an empty one (an error now); a refusal
   that blamed the title for a description past the read limit, one that pointed at stdin for a word of the
   command line, and one that named a different stray flag each run; and the board folded three times a
   command (once, plus the one work item after a write).

## 7. What this concedes

- Two machines exchange work items only while both run a session at the same time; members never online
  together converge through a machine that overlaps both.
- Any teammate's machine can write anything into the folder; a change's `by` is that machine's word.
- Nothing is removed, and turning the board off leaves the folder where it is.
- A session with no inbox socket runs no watcher: it has a board folder and its `brigade work-item` works, but
  nothing of it travels until a session with a watcher runs on the machine.
- The injection test set has not been run against work items.

## 8. Out of scope, on purpose

Work items in the digest; a session-start or Stop-hook line about the work items assigned to the session's
member; reaching the board from Slack or email; due dates, checklists, attachments, ordering within a list;
deleting; a terminal form outside a session; importing a Trello board; moving this repository's own `/trello-*`
skills onto the board.
