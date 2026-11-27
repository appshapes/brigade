# 0040. Keep usage, cost and time per work item out of Brigade, in a separate plugin

- **Status:** Accepted
- **Date:** 2026-11-27
- **Supersedes:** 0035, 0036, 0037 and 0038

## Context

Work items 346 to 349 landed on `master` on 2026-11-26 and were never released. They added a record on the board
of which work item a session works on (ADR 0035), each session's usage read by its watcher (ADR 0036), an
estimated cost on each work item from a price table in the binary (ADR 0037), and a member's usage shared through
the board folder (ADR 0038). The owner reviewed them on 2026-11-27 and ruled: "we must not allow these changes
that are in master to be released".

The owner's reasons, with what the review measured (`.context/plans/time-and-usage-plugin-538.md`):

- **A team that enabled nothing would still incur it.** No switch gated the reading. Every session's watcher
  built a usage record at each heartbeat and wrote it again every five minutes, a Muse Code session ran
  `muse usage --json` every five minutes, and the `work-item-start` skill wrote a directory for each session
  into the board folder of every team. The owner's rule: "Teams that do not enable these new values must never
  incur anything related to this feature. Nothing surfaced in the UI, nor in files, nor in processing (load)."
- **A session was limited to one work item** (ADR 0035, decision 1). The owner's rule: a session must be able to
  work on several work items at once.
- **A span from start to stop measures how long a session was open.** In this repository's six most recent Claude
  Code sessions, 546 of 4,826 minutes of wall clock were spent in turns: 11 percent.
- **The estimated cost was not sound.** On the same sessions the method gave $269 where list price gives $332.
  Claude Code 2.1.332 writes a subagent's calls to files of their own, which the reader did not read (228 of
  1,942 model calls), and every cache write was a 1-hour write, priced at the 5-minute price. A price table in
  the binary has no price for a new model until a release.
- **Brigade was meant for session collaboration**, and it kept growing.

A Claude Code install takes the plugin's files from `master` when its version changes, and its binary from the
release. An install that was made, or that moved to 0.61.0, after 2026-11-26 had skills that told the model to
run `brigade work-item start`, which the 0.61.0 binary refuses (measured on 2026-11-27, on an install at commit
57b659c8).

## Decision

1. Brigade does not record which work item a session works on, does not read a session's usage, estimates no
   cost and shares no usage. Work items 346 to 349 are taken out of `master` (work item 537), and nothing of
   them is released.
2. Time, tokens and cost per work item are the work of a separate plugin, in a repository of its own, which a
   team installs if it wants it (work item 538). It uses the `brigade` command line as it is. Brigade gains
   nothing for it.
3. The owner's rules for that plugin, of 2026-11-27: a session can work on several work items at once; it keeps
   a time-spent number for each work item and adds to it; it reads at the end of a turn and never on a timer;
   nothing of it shows in `brigade sessions`; it ships no price table; sharing with the team is off unless a
   member turns it on.
4. ADRs 0035, 0036, 0037 and 0038 are superseded whole. The usage of an Antigravity CLI session, the usage part
   of work item 534, is not built in Brigade.

## Alternatives considered

- **Keep it in Brigade behind a team-level switch, off when absent.** A team that left it off would run none of
  it. It lost: the code, the option, the skill steps and the words of `docs/security.md` would still ship to
  every team, and each follow-up (a budget, the team page) would land in Brigade again.
- **Keep it and repair the faults.** It lost on the owner's rule that a team which enabled nothing incurs
  nothing, which no repair of the reader meets.
- **A general extension model in Brigade**, where the team file names code that Brigade runs on events. It lost
  for now: a file in the repository that makes every member's machine run code needs the member's consent, a
  frozen contract and support in five harnesses, for one feature. A harness's own plugin system and the
  `brigade` command line already let a second plugin stand beside Brigade.

## Evidence

- The review of 2026-11-27, its measurements and how to repeat them: `.context/plans/time-and-usage-plugin-538.md`,
  section 2.
- The owner's words of 2026-11-27, recorded on work items 537 and 538.

## Consequences

- A team gets no usage, cost or time from Brigade. `brigade usage` and `brigade statusline` do not exist,
  `brigade work-item` has no `start` and no `stop`, `brigade sessions` has no `ITEM` column, and the board's
  page shows no agents and no plan limits.
- The click that opened a work item in a dialog on the board's page left with work item 349. It can come back
  as a change of its own.
- The board folder of a team whose sessions ran the unreleased build keeps a `sessions/` and a `usage/` folder
  until someone removes them. Brigade reads neither.
- An install that took the unreleased skills under version 0.61.0 keeps them until its version changes: the next
  release repairs it.
- The plugin needs its own package for each harness it supports, and its own releases.
- Revisit this when a team needs totals on the board that the plugin cannot give through the `brigade` command
  line. The smallest answer then is one number on a work item that anything can add to.

## References

Work items 537 and 538; work items 346 to 349, 350, 508 and 534; ADRs 0035 to 0038;
`.context/plans/time-and-usage-plugin-538.md`.
