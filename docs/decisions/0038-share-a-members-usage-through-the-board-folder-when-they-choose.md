# 0038. Share a member's usage through the board folder when they choose

- **Status:** Superseded by 0040
- **Date:** 2026-11-26
- **Supersedes:** none

## Context

Each session's watcher reads its usage and keeps it on its own machine (ADR 0036), and adds it to the work item
the session works on (ADR 0037). The board's page shows what this machine holds, so a teammate's sessions are not
on it. Usage is money and plan limits, and Brigade's teams cross company lines. The owner ruled on 2026-11-24
(work item 348, its comments): one option, off by default, sharing tokens, estimated cost and plan limits
together. The roster is the backend's, and a new member of a session record changes the frozen protocol and the
frozen REST contract.

## Decision

1. The plugin option `share_usage` is off by default. The session-start hook freezes it into the session map; it
   takes effect at the next session start.
2. With it on, the watcher writes the session's usage into the board folder, as one file for each session,
   `usage/<key>.json`, written again in place: the model, the context in use and its window, the plan's limits
   and extra usage, and the tokens and estimated cost on each work item. It writes when that changed (a new
   reading of the same limits is no change), at most once in five minutes. The session's own watcher is the
   file's one writer, so file sync makes no conflict copy of it, and a session's share never grows.
3. With it off, nothing of the usage is written into the board folder or sent anywhere.
4. `brigade work-item page` shows a teammate's shared usage beside this machine's own: on each card under the
   teammate's label ("a teammate" for a share that names none), and the plan limits under their name. This
   machine's own record of a session wins over the session's share. A share older than 30 days, and limits read
   more than eight days before, are left out.
5. The words of `docs/security.md` that say what is shared are the owner's to approve.

## Alternatives considered

- **Default on.** The owner first chose it, then ruled it off on the same day: usage is money and plan limits,
  and teams cross company lines.
- **The roster.** It lost on cost: the frozen protocol, the frozen REST contract, both adapters and the server.
- **The option only, with the sending in the team page's work item (508).** It lost on Frank's choice: teammates
  would see nothing until the server shows the board.
- **A change written once in the session's directory, `sessions/<key>/`, as its work links are.** It kept the
  board's rule that a change is written once (ADR 0035), and lost on what it costs: a session that shares for a
  week reaches the 2,000 changes of a directory, which stops its `start` and `stop` too, and the reader takes
  the first 2,000 by name, not the newest. The rule is for a file several writers may write; a share has one.
- **Separate options for the limits.** The owner ruled one switch for all three.

## Evidence

- The owner's two rulings of 2026-11-24, on work item 348.
- The tests of work item 348: the option off by default (`internal/harness/config/share_usage_test.go`), no write
  with it off and a rate-limited write with it on (`internal/harness/watch/hostfacts_usage_test.go`), the reader
  (`internal/harness/workitem/work_test.go`) and the page (`internal/harness/commands/workitem_page_usage_test.go`).

## Consequences

- A member who turns it on gives every teammate's machine their sessions' usage, for as long as the board keeps
  files: turning it off stops new shares and recalls none.
- A share is a teammate's machine's word, as every change of the board is: any machine can write one under any
  name (`docs/security.md`, section 19).
- The board holds one share file for each session that ever shared, until a file is removed by hand: the page
  shows only those of the last 30 days.
- Revisit this when the team page (work item 508) shows usage from the server.

## References

Work items 344, 348, 349 and 508; ADRs 0035, 0036 and 0037; `internal/harness/workitem/usage.go`;
`docs/team-member/use/usage.md`, "Share it with the team"; `docs/security.md`.
