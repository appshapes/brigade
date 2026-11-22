# 0016. Strike the promise of a new log started from a snapshot

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

Brigade's server keeps its state in a log, written through Cavalry, and in snapshots of its own format. Cavalry
is a git dependency pinned to one commit, `620a2e29`, and nothing in it is frozen before its 0.1.0, so a later pin
may write a log of another format.

Until this decision six places named one way out of such a change: start a new log from the newest snapshot.
The operator's page `docs/project-owner/server/keep-the-server.md`, the developer's page
`docs/developer/change/server.md`, the head of `server/crates/brigade-server/src/snapshot.rs`, the plan
`.context/plans/brigade-server.md` (twice), the comment on the pin in `server/crates/brigade-log/Cargo.toml` and
the rule file `docs/agent/repository/gates.md`.

No command does it. A start reads the log from the snapshot's position (`engine.rs`), and both logs refuse a
position past their end as the snapshot of another: the Cavalry feed, which is the log a pin can change
(`feed.rs`), and Brigade's own one-file log (`file.rs`). Done today it would mean editing a snapshot's name,
header and SHA-256 by hand. The repository review of 2026-11-21 raised it (work item 386) and left one
choice to the owner: build the command, or stop promising it.

On 2026-11-22 (UTC) Cavalry's work item 362 landed a gate in Cavalry's own repository
(`conformance/upgrade/log-directory.sh`, its commit `52c25642`): it has the tree at `620a2e29` write a log
directory as a server writes its own and holds that Cavalry's `master` opens it in place.

## Decision

Nothing promises a new log started from a snapshot.

- An upgrade opens the data directory in place.
- A release that could not open the directory of the release before it is not made, and operators stay on the
  release they have.
- The command is built only when a change of the format needs it.

This is the owner's ruling of 2026-11-22: "Strike the promise".

For a developer it means that a new pin of Cavalry has to open, in place, the directory the pin before it wrote,
and that a pin which cannot is not moved to. For an operator it means one more line at an update: keep the
earlier binary until the new one has started.

## Alternatives considered

- **Build the command now**: an operator's command that anchors the newest snapshot on a fresh log, with a test.
  Not chosen. No change of format asks for it today, and Cavalry's gate now holds that the directory the pinned
  commit writes opens under Cavalry's `master`. The decision keeps it for the day a change needs it.
- **Leave the sentence and build nothing.** Not offered: it is the state the review found, a way out that no
  operator could take.

## Evidence

- `server/crates/brigade-server/src/engine.rs` reads the log from the snapshot's position.
  `server/crates/brigade-log/src/feed.rs` refuses a position past the feed's end ("the snapshot is of another
  feed"), and `file.rs` beside it does the same for the one-file log; read on 2026-11-22.
- Cavalry's work item 362 on the Brigade Board: its second part, pull request 312 there, says that a directory
  written at `620a2e29`, stopped or killed and with purges in it, opens in place under Cavalry's `master`, and
  that nothing needed fixing. The script was read in Cavalry's tree on 2026-11-22: the tree it writes from is
  `620a2e29`. That is Cavalry's gate and Cavalry's record. It was not run from this repository, and it covers
  that one older tree: after a move of the pin it says nothing about the pin that was left unless Cavalry moves
  it too.
- None measured here: Brigade's own gate has no test that a directory written by one server release opens under
  the next across a move of the pin. Work item 387 asks for one.

## Consequences

- An operator has one thing to do at an update, and the page says it in two sentences.
- "Opens in place" across a move of the pin rests on Cavalry's gate until work item 387 gives Brigade one of its
  own. A move of the pin without that test stands on another repository's word.
- If a change of the log's format ever cannot be opened in place, operators are held on a release until the
  command exists. That is the moment to build it, and to supersede this decision.

## References

- Work item 386 on the Brigade Board, with the owner's ruling as its comment of 2026-11-22; work item 387, the
  move of the pin with a test in Brigade's gate; Cavalry's work item 362.
- `docs/project-owner/server/keep-the-server.md` ("Update it"), `docs/developer/change/server.md` ("Cavalry"),
  `docs/agent/repository/gates.md` ("The server").
