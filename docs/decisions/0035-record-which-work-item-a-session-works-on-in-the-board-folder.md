# 0035. Record which work item a session works on in the board folder

- **Status:** Accepted
- **Date:** 2026-11-26
- **Supersedes:** none

## Context

The cost of a work item, and the page that shows the agents on it, need to know which session worked on which
work item, from when to when (work items 344, 346 and 349). Until now only conventions linked the two: the
session's name `<number>-<slug>` and its roster line. A name changes, and a roster line can name two work
items.

The board is a folder of change files on every teammate's machine, whatever repository their session is in
(`internal/harness/workitem`, ADR 0012). The roster is the backend's: a new member of a session record changes
the frozen protocol (`docs/protocol-v1.md`) and the frozen REST contract (`docs/backend-rest-v1.md`), with both
adapters and the server. A session's state entry (`.brigade/state/sessions/`) is in one repository's state
folder, so a machine holds it only for the repositories it has.

## Decision

1. `brigade work-item start <number>` records that this session works on that work item from now on, and
   `brigade work-item stop` records that this session stopped work on that work item. A session works on one work item at a time (Frank's
   decision of 2026-11-26): a start on another work item stops the one before.
2. Each start and each stop is a change file, written once, in the board folder under `sessions/<key>/`, where
   the key is the first 32 hex digits of the SHA-256 of the Brigade session id. That session writes its files
   there; like any change, another machine can write one that says the same (`docs/security.md`, section 19).
   The change carries the session id, the work item's id and number, or the reason of the stop.
3. The session's SessionEnd hook writes a stop, with the reason `session_end`, after it signals the watcher.
   When the session's process dies without the hook, the watcher writes that stop as it closes the session. A
   `clear` or a resume keeps the session, and its work goes on.
4. `brigade work-item show` lists each period of work on the work item, with its member, its session and its
   times; a period that is still open says `no stop recorded`. A `start` on a roadmap item or a closed work item is refused. `brigade sessions`
   shows the number in an `ITEM` column for each session that is not offline, and `--json` carries it.
5. The `work-item-start` skill of each package runs `start` when it takes a work item, and `stop` when it lands
   it or is blocked.

## Alternatives considered

- **A member of the session record in the roster.** Every session would see it at once, with no board. It
  lost on cost: the frozen protocol, the frozen REST contract, both adapters and the server change in one
  commit, for data that only a team with a board uses.
- **The session's state entry.** It already has the session's start, end and roster lines. It lost because a
  machine holds that state only for the repositories it has, and the board is on every teammate's machine.
- **One file for each session, rewritten in place.** Fewer files. It lost on the board's rule that a change is
  written once and never rewritten, which keeps the readers and the sync engine simple.
- **Several work items at once.** It lost on part 5: each usage delta would have to be divided between work
  items, with a rule for how.

## Evidence

- The tests of work item 346: `internal/harness/workitem/work_test.go` (start, a second start that replaces
  the first, stop, a session's end, foreign files), `internal/harness/commands/workitem_work_test.go` (the
  verbs, `show`, the `ITEM` column and `--json`) and `internal/harness/hook/end_work_test.go` (the SessionEnd
  hook stops the work for an end, and not for a `clear` or a resume) and `internal/harness/watch/work_test.go`
  (the watcher stops it when the session's process is gone).
- The investigation of work item 344, section 3.3 (`.context/plans/board-dashboard-344-investigation.md`).

## Consequences

- Part 5 (work item 349) can add each usage delta to the work item that is current at that heartbeat.
- The board carries more: for each session that ran `start`, its id, its name, its member's label and its
  times. Like every change, a teammate's machine can forge one (`docs/security.md`, section 19).
- A machine that stops runs neither the hook nor the watcher, so the session's work stays open on the board.
  The roster shows no work item for an offline session, `show` says `no stop recorded`, and the next `start` or
  `stop` of that session after a resume closes it.
- `brigade sessions` reads the board folder on this machine, so a member whose machine has not yet received a
  teammate's start does not see it. It reads only the directories of the live sessions it shows.
- The board keeps the directory of every session that ever ran `start`. `brigade work-item show` reads all of
  them, at most 20,000, so its cost grows with the team's sessions over time, and past that count it leaves
  some out. Revisit this before a team comes near it.
- Revisit this when the roster needs the work item on a machine with no board, or when a session must work on
  several work items at once.

## References

Work items 344, 346 and 349; `internal/harness/workitem/work.go`; `docs/team-member/use/work-items.md`, "Say
which work item a session works on"; ADR 0012.
