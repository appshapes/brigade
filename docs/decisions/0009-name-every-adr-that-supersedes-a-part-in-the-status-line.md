# 0009. Name every ADR that supersedes a part in the Status line

- **Status:** Accepted
- **Date:** 2026-11-14
- **Supersedes:** 0001 in part (point 2's form of a Status that is superseded in part)

## Context

ADR 0001 gives a Status four forms, and the one for an ADR that a later one replaces a part of is
`Accepted; superseded in part by NNNN`: one number. The documentation gate holds that form with one pattern
(`adrStatusRe` in `internal/docscheck/decisions.go`), so a Status that names two ADRs fails `make docs-check`.

On 2026-11-14 three pieces of work each replaced a different part of ADR 0003, on three branches at once: ADR 0006
(a team is taken in and its move opened by a request, and whoever takes it in owns it), ADR 0007 (the record of a
move is allowed inside a session) and ADR 0008 (a running session follows at its next prompts). Two of the branches
set ADR 0003's Status to their own number, and the merge of the two conflicted on that one line. No form of the
line that ADR 0001 allows says what is true: that three ADRs each hold a part.

ADR 0001 is in the same place with this ADR: ADR 0002 superseded a part of its point 2, and this one supersedes
another part.

## Decision

The Status of an ADR that is superseded in part names every ADR that supersedes a part of it, each once, the lowest
number first:

- one: `Accepted; superseded in part by 0002`
- two: `Accepted; superseded in part by 0002 and 0009`
- three or more: `Accepted; superseded in part by 0006, 0007 and 0008`

An ADR that supersedes a part of an earlier one adds its number to that line in the same change, and says in its
own Supersedes line which part it replaces.

The other three forms stay as ADR 0001 gives them. `Superseded by NNNN` names one ADR: an ADR replaced as a whole is
replaced by one decision.

The gate holds the form: the line's shape, that every ADR it names is in `docs/decisions/`, and the order.

## Alternatives considered

- **Name the newest ADR only.** Lost: a reader of ADR 0003 would be sent to ADR 0008 and never learn that its rule
  on who owns a moved team is ADR 0006's now. The Status line is the one place an ADR that keeps its text can point
  forward.
- **Name the first, and leave the rest to each later ADR's Supersedes line.** Lost for the same reason: finding the
  rest means reading every later ADR's header.
- **One `Superseded in part by` header entry per ADR.** Lost: ADR 0001 says the other header lines never change,
  the gate holds exactly that, and a new kind of entry would need the larger change for no more than a list says.
- **Let the last branch to merge rewrite the earlier ADRs so that only one supersedes.** Lost: ADR 0006, 0007 and
  0008 are three decisions with their own alternatives and evidence, made in three pieces of work.

## Evidence

- The conflict: `git merge-tree --write-tree 328-one-command-move 327-running-session-follows` on 2026-11-14 reports
  `CONFLICT (content)` in ADR 0003, the two sides being `superseded in part by 0005` and `superseded in part by 0007`:
  the numbers the take-in's ADR and the running session's ADR had on their branches. Another piece of work (work
  item 319) landed an ADR 0005 on `master` before these were merged, so the three took the next free numbers, 0006
  to 0008, and this one 0009, in the merge and before any of them was on `master`.
- The gate's pattern before this ADR took one four-digit number after `superseded in part by` and nothing else.
- The rule is proven able to fail in `scripts/ci/docs_decisions_test.go` (a list that names an ADR that is not
  there, a list out of order, an ADR named twice, a list with no `and`, three joined by `and` alone) and to pass
  (two ADRs, three ADRs), and in `scripts/ci/docs_check_test.go` on a tree with no history.

## Consequences

- Easier: an ADR's Status says where every replaced part of it went.
- Harder: nothing. A Status that names one ADR is written as before.
- To maintain: the pattern and the order check in `internal/docscheck/decisions.go`.
- Revisit if an ADR is replaced as a whole by two ADRs between them, which `Superseded by NNNN` cannot say.

## References

- Work item 328 on the Brigade Board, in whose merge with work item 327 the conflict arose.
- ADR 0001, point 2; ADR 0003, the first ADR with more than one.
- `docs/agent/repository/documentation.md` and `docs/decisions/README.md`, which state the rule;
  `internal/docscheck/decisions.go`, which holds it.
