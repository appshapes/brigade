# 0022. Give each harness a status in the registry

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

The pages presented Claude Code, Muse Code, Codex CLI and Grok Build as four equal ways in: `docs/harnesses.json`
gives each the same six pages, and the install root listed them side by side. They are not equally far along, and
a member who picked one learned that when it failed (work item 378, the repository review of 2026-11-21).

The owner ruled on 2026-11-22, in two answers recorded on the work item: each harness carries a status in the
registry, which the install root and each package's page show and `make docs-check` holds; and the status has
three values, "Primary", "Supported, with known gaps" and "In trial". Which harness gets which was left to
evidence, and who keeps the field at a release was left to the session that built it.

## Decision

`docs/harnesses.json` gives each harness a `status`, one of:

- `Primary`: the harness Brigade is built and proven on first;
- `Supported, with known gaps`: it works, and its gaps are named on its install page;
- `In trial`: it may not work for you yet, and its install page says what is and is not proven.

A page shows it on a line that begins `Status: `. Three pages show it: the harness's section of the install
root, its install page and its package page. The install page of a harness that is not `Primary` has a section
`## Known gaps` or `## In trial`. `make docs-check` holds all of it.

A status is set from evidence, and each value says on its page what it rests on. Today:

| Harness | Status | What it rests on |
| --- | --- | --- |
| Claude Code | Primary | CI drives the no-LLM proof on every push; `make proof` wakes a live session; the experiments E0 to E19 ran on it; it is the one harness proven on Windows |
| Codex CLI | Supported, with known gaps | the live test of 0.163.0; a live session on 0.166.0 on 2026-11-22 that answered a message 14 seconds after the send. Its gaps: the hooks are reviewed again after every update, a launch flag can slow delivery, the default profile's approvals, a quit that is not an end, one measured platform, no session seen on a server |
| Grok Build | Supported, with known gaps | the native acceptance of October 2026 on 1.0.62; a live session on 1.0.92 on 2026-11-22 that armed its monitor by itself and answered 48 seconds after the send; another member's session on the team's own server. Its gaps: only a direct TUI, nothing before the monitor is armed, reading in pages, one measured platform, a package read from the default branch, care in removing it |
| Muse Code | In trial | a live session on 1.4.25 on 2026-11-22 answered 5 seconds after the send once its monitor was armed, but the model armed nothing after a plain first prompt and the session took no message until asked; two faults found then are fixed (work item 249) and not yet seen fixed in a live session |

Whoever cuts a release reads the four values first, against what happened since the last one: it is a step of
the release's preconditions on its page. No script and no agent sets a value.

## Alternatives considered

- **Peers, and the gaps closed instead**, or **peers with a line of known gaps and no field.** Lost by the
  owner's ruling: one field tells a member the truth before they install.
- **The monthly documentation agent keeps the field.** Lost: that agent corrects structure, a path or a count,
  and is told to leave judgement alone. A status is a judgement of evidence.
- **The release sequence sets it**, from a script. Lost for the same reason: `scripts/release-prep.sh` can show a
  value and cannot know whether a live session passed. The person or session that cuts the release reads it.
- **Grok Build in trial**, as the owner was told was likely when he chose the three values. Not taken, on the
  evidence: its one live session needed nothing but the approval its page names, and it has the widest native
  measurement of the three. It is his to overrule, as each value is.
- **A status for each platform or backend.** Not taken: three values for four harnesses is what was ruled, and
  the page of each says which platform was measured.

## Evidence

- The live sessions of 2026-11-22 (work item 381; the execution log's row P106-23): Grok Build 1.0.92, Muse Code
  1.4.25 and Codex CLI 0.166.0, each sent one message while idle, on the fs adapter.
- `docs/experiments/grok-participation.md`, `codex-queue-participation.md` and `muse-participation.md`: each
  harness's own measurements, all on macOS arm64.
- The team's roster on 2026-11-22: a Muse Code and a Grok Build session of another member on the team's Brigade
  server. They show a registration there and say nothing of receiving.
- `scripts/ci/docs_check_test.go`: the gate refuses a registry entry with no status or another value, a package
  page or an install page that does not show it, and an install page with no section for it.
- Not yet in the evidence: a live Muse Code session after work item 249's fix and a live Grok Build session
  after work item 377's change. Both wait on the owner's word. Grok Build's value was set before the second.

## Consequences

- A member reads a harness's status where they choose it, and its gaps where they install it.
- A status can be wrong the day after a release. The release's precondition is the one place it is read again,
  and a live session that fails is the reason to lower one between releases.
- A fifth harness (work item 267) arrives with a status, and the gate refuses it without one.
- The package page of each harness changed with this, Codex CLI's among them, so Codex asks its members to
  review Brigade's hooks once more.
- Revisit when a live session changes what a value rests on, and when a harness is measured on a second
  platform.

## References

- Work item 378 on the Brigade Board, with the owner's two rulings of 2026-11-22 as comments; work items 249, 377
  and 381; work item 313, part 6, the sessions not yet proven on a server.
- `docs/harnesses.json`; `internal/docscheck/docscheck.go` (`harnessStatus`); `docs/team-member/install/`.
- `docs/developer/release/cut-a-release.md`, the precondition that reads the statuses.
