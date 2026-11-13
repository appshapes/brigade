# 0001. Record decisions as ADRs in the repository

- **Status:** Accepted; superseded in part by 0002
- **Date:** 2026-11-13
- **Supersedes:** none

## Context

Until this ADR, Brigade kept its decisions in four places: lines in the rule files `CLAUDE.md` imports (an "owner
ruling" and its date), the argued sections of `docs/security.md`, the plans under `.context/plans/`, and the rows
of the execution log. Each says what was decided. None has a fixed place for the alternatives that lost or for
the evidence, and a reader has to know which of the four to search.

Cavalry and muster, two other repositories of the same team, record each decision as an ADR in `docs/decisions/`
(Cavalry's ADR 0001, 2026-10-10). On 2026-11-13 Cavalry held 114 ADRs and muster 18, cited by number 4,387 and 494
times in their own trees.

Work item 302 asked whether work items on the team's Brigade Board should replace those ADRs. The owner ruled on
2026-11-13, in answers recorded on work items 302 and 307:

1. A clone alone must still say why a decision was made: "I love the ADR method...and I hope to get all appshapes
   repos not already using it to use it."
2. An ADR is written in the repository. The board carries a decision until it is accepted.
3. All five repositories are covered. Cavalry and muster take the board's part into their rules (work items 307
   and 308); brigade, telder and appshapes-www adopt the method (work items 309, 310 and 311).
4. An ADR file is never Proposed: "It goes." A decision that still waits lives on the board alone.

Three facts weighed. The board is a folder on each member's machine that file sync shares: it is in no clone, CI
cannot read it, and any member's machine can change any work item (`docs/security.md`, section 19). This
repository is public. And the agents of the agentic loop run on a runner that has no board
(`docs/agent/repository/conventions.md`).

## Decision

Brigade records a decision as an ADR, from this one on.

1. **One decision, one file**: `docs/decisions/NNNN-short-title.md`, copied from `0000-template.md`, numbered in
   order, listed in `docs/decisions/README.md`, and written in the same change as the code, the rule or the page
   it explains.
2. **A file is never Proposed.** Its Status is `Accepted`, `Accepted; superseded in part by NNNN`,
   `Superseded by NNNN` or `Rejected`. A decision that waits on someone (the owner's own hand, another session,
   evidence not yet measured) is a work item on the Brigade Board tagged `decision` beside `brigade`: the question,
   the options and the evidence in the template's sections, and the ruling as a comment. The owner's ruling is
   asked for with `brigade decision --to <the owner's roster label> --card <work item>`, the question on stdin;
   the session that receives the answer records it as a comment on the work item and closes the ask with
   `brigade decision --close <id>`. Once it is ruled, the ADR lands with the change, its References name the
   work item, and the work item's landing comment names the ADR. A decision a session makes on its own inside
   a work item needs no work item of its own: its ADR lands in that work item's change.
3. **An ADR on `master` keeps its text.** Its Status line changes when a later ADR supersedes it. A statement of
   fact that was wrong when it was written (a date, a count, a citation, who decided something) may be corrected
   in place, and each correction adds a dated `- **Corrected:**` entry to the header that says what was wrong.
   The title, the other header lines and the Decision section never change. A change of mind is a new ADR that
   supersedes the old one. An ADR is never removed or renamed, with one exception: when two ADRs have landed
   under one number, one of them takes the next free number and changes nothing else. A link in it is repointed
   when the page it names moves: a link's target is not part of the text.
4. **An ADR is required** for a change to the frozen protocol or the frozen REST contract, to what
   `docs/security.md` argues, to an invariant in `docs/agent/repository/invariants.md`, to a default that every
   team gets, and for a new module in the shipped binary. It is encouraged for anything a newcomer would ask
   "why" about.
5. **No backfill.** The rulings made before this ADR stay where they were written. An ADR may cite them.
6. **A decision is written once.** A plan is how a piece of work is designed, the execution log is where the work
   stands, and a rule file states the rule. From this ADR on, each names the ADR by its number instead of
   retelling its reasons.
7. **The documentation gate holds what a check can** of points 1 to 3 (`make docs-check`, the `decisions` rule):
   an ADR's name, number, title line, Status and Decision section, its place in the index, and that a landed ADR
   keeps its text.

## Alternatives considered

- **Keep the four places.** Lost: the reasons stay scattered, the alternatives and the evidence go unrecorded
  unless a plan happens to keep them, and it is not what the owner asked for.
- **Keep decisions on the board alone** (work item 302's third option). Lost on the owner's first answer: a clone,
  CI, a runner's agent and a reader of this public repository would not see them, and any member's machine could
  rewrite one.
- **Write the ADR on the board and export it into the repository.** Set aside on the owner's second answer. It
  needs an export command, an accepted state no member can edit and a rule for two number sequences, and a link
  from an ADR to a page would go unchecked until the export. It can be added later without undoing this.
- **Keep Proposed as a Status of a file.** Lost on the owner's fourth answer. Cavalry did land Proposed ADRs,
  sixteen of them, and accepted each by a later commit; the last was accepted on 2026-11-05, and its own rules
  have since said an ADR lands Accepted on its evidence. One place for a pending decision is simpler than two.
- **Allow nothing but the Status line to change**, Cavalry's rule. Lost to muster's ADR 0018, which the owner
  chose on 2026-11-13 after a wrong fact was found in an accepted ADR there: a superseding ADR is for a change of
  mind, and without a correction the wrong fact stays for good with nothing to tell a reader it is wrong.
- **Rewrite the past rulings as ADRs.** Lost: they were written at the time in another form, and restating them
  after the fact risks getting wrong who decided what and why.
- **Have the gate refuse a change that needs an ADR and has none**, as Cavalry's does for a change to its `spec/`.
  Not built. A check can only watch files, and the files point 4 names also change for reasons that need no ADR
  (a wording fix in `docs/security.md`, a note added to an invariant), while a default that changes for every
  team is not one file. Point 4 is held by the agent review before a push and by the pull request reviewer.

## Evidence

- The counts above were taken on 2026-11-13 in Cavalry's and muster's trees on `master`: the files in
  `docs/decisions/` without the template, and every occurrence of "ADR" followed by a four-digit number.
- Cavalry's Proposed files: the Status line of each of its ADRs, read at every commit of its `master` that touched
  the file, on 2026-11-13. Sixteen were Proposed at some commit, from ADR 0004 to ADR 0051. The count was first
  given on work item 302 as none; a session working in Cavalry found the error the same day, and a comment there
  corrects it.
- The `decisions` rule is proven able to fail in `scripts/ci/docs_check_test.go` and
  `scripts/ci/docs_decisions_test.go`: one planted violation at a time against a copy of this tree, and, for the
  history, against a copy that is a git repository with a planted ADR committed, on a branch and landed.

## Consequences

- Easier: finding why a rule is the way it is, with what was rejected; and telling a pending decision from a
  made one, since the first is a work item and the second is a file.
- Harder: a change that point 4 names carries a writing step.
- To maintain: the `decisions` rule, which runs `git` to read what has landed. It holds an ADR against the text
  the default branch has: an edit is refused in the working tree, in a commit not yet on that branch and in a
  pull request, an ADR that a branch added can be revised until it merges, and putting the landed text back
  always passes. Two limits. In a tree that is not a repository that half does not run. And the gate does not
  look back: an edit that reached the default branch some other way, without passing the gate, is the landed
  text from then on.
- Revisit when the board gains an export and an accepted state that no member can edit, which would make the
  set-aside alternative cheap; or when a change that needed an ADR reaches `master` without one, which would
  argue for building the refusal into the gate.

## References

- Work items 302, 307 and 309 on the Brigade Board: the owner's answers, in the owner's words, are in their
  comments and descriptions.
- Cavalry's ADR 0001 (record decisions as ADRs) and ADR 0113 (track Cavalry's work on the Brigade Board), and
  muster's ADR 0018 (correct a fact in an accepted ADR by editing it), in `docs/decisions/` of
  `github.com/appshapes/cavalry` and `github.com/appshapes/muster`.
- `docs/agent/repository/documentation.md` and `docs/agent/repository/conventions.md`, which state the rules;
  `internal/docscheck/decisions.go`, which holds them.
