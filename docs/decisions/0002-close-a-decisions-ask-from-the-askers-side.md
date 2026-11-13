# 0002. Close a decision's ask from the asker's side, with the ruling as its outcome

- **Status:** Accepted
- **Date:** 2026-11-13
- **Supersedes:** ADR 0001, in part: the one sentence of its point 2 that says who records a ruling and who closes
  the ask. The rest of ADR 0001 stands.

## Context

ADR 0001, point 2, says of a decision that waits on the owner: "the session that receives the answer records it
as a comment on the work item and closes the ask with `brigade decision --close <id>`." It landed on 2026-11-13
(commit 82838cd). Two things in that sentence are wrong. The review of Cavalry's pull request 281, which was
putting the same sentence into Cavalry's rules, found them the same day, and they were checked here in
`internal/harness/commands/decision.go`:

1. A close takes its outcome on stdin and refuses an empty one. The sentence gives the command without it.
2. Only the asking session, or another session of the member who asked, closes an ask; a session of another
   member is refused (`not_the_asker`). The sentence has whichever session receives the answer close it.

The sentence is in a Decision section, which ADR 0001's own point 3 says never changes: a correction under a
Corrected entry is for a fact outside it. So the fix is this ADR.

## Decision

A decision that waits on the owner is asked and settled in three steps.

1. **The ask**: `brigade decision --to <the owner's roster label> --card <work item>`, the question on stdin.
2. **The record**: the session that receives the answer records it on the work item with
   `brigade work-item comment <work item>`, the owner's words on stdin.
3. **The close**: the asking session, or another session of the member who asked, closes the ask with
   `brigade decision --close <the ask's id>`, the ruling as its outcome on stdin. A session of another member that
   receives the answer records it and tells the asker.

Everything else in ADR 0001 stands.

## Alternatives considered

- **Edit ADR 0001's sentence in place.** Lost: its Decision section never changes, and the gate refuses the edit.
- **Leave the sentence, and say the whole of it in the rule file alone.** Lost: ADR 0001 would go on giving a
  command that fails as written and a close that is refused, with nothing in it to say so.
- **Let whichever session receives the answer close the ask**, by changing the command. Lost: that only the
  asker's side closes is the product's rule for every team
  (`docs/team-member/use/notes-and-decisions.md`), not this repository's to change for its own convenience.

## Evidence

- `internal/harness/commands/decision.go`: `closeDecision` reads the outcome from stdin and fails on an empty
  one; a close by a session of another member fails with the reason `not_the_asker`.
- `docs/team-member/use/notes-and-decisions.md` says both, with an example of a close whose outcome is a heredoc.
- In use on 2026-11-13: the asks of work items 302 and 307 were each closed by the session that asked, with the
  owner's ruling as the outcome on stdin.

## Consequences

- The rule file and the decisions page give the three steps, and ADR 0001's Status reads
  `Accepted; superseded in part by 0002`.
- The same three steps are the wording for the team's other four repositories (work items 307, 308, 310 and
  311), so none of them lands the shorter sentence.
- This is the first use of the rule that a Decision section is not edited: it cost one short ADR the day ADR
  0001 landed.
- Revisit if the command changes who may close an ask.

## References

- Work item 309, in whose change this lands, and work item 307, whose pull request's review found it.
- ADR 0001, point 2 and point 3.
- `docs/agent/repository/conventions.md` and `docs/decisions/README.md`, which give the three steps.
