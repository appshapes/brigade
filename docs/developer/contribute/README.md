# Contribute

## If you want to

| If you want to | Go to |
| --- | --- |
| commit and push a change | [Commit and push](#commit-and-push) |
| read, add, start or finish the work item the work belongs to | [Work from the Brigade Board](#work-from-the-brigade-board) |
| record a decision, or correct one that is recorded | [Record a decision](#record-a-decision) |
| have a pull request reviewed, fixed and merged by the repository's agents, or run a scheduled job | [Use the repository's agents](#use-the-repositorys-agents) |

## Commit and push

1. `make push message="<work item>: <Imperative summary>"`, or `/commit <work item>` in Claude Code.
2. `master` only, merges only, never rebase; plans in `.context/plans/`, scratch in `.ignored/`.
3. A merge conflict is yours to resolve in place: never `git merge --abort`, never a rebase or a force-push.

Details: [Commit and push](commits.md).

## Work from the Brigade Board

1. The number in a commit message is a work item's on the Brigade Board of the team `appshapes`; `brigade work-item list`, `show <n>`, `add`.
2. `/brigade:work-item-start <n>` takes a work item from reading it to Done under the repository's rules: taken, scope agreed, built, proven, reviewed, on `master` with CI green, the landing comment on it.
3. A work item is finished when on `master` with CI green and nothing on it is open; then comment and move it to Done, unasked. Closing is the owner's.

Details: [Work from the Brigade Board](work-items.md).

## Record a decision

1. A decision a newcomer would ask "why" about is an ADR: `docs/decisions/NNNN-short-title.md`, copied from `0000-template.md`, listed in the index, in the same change as what it explains.
2. While it waits on someone it is a work item tagged `decision`; ask for the owner's ruling with `brigade decision --to <label> --card <work item>`, the question on stdin. A file is never Proposed.
3. On `master` an ADR keeps its text: only its Status line changes, or a wrong fact is corrected under a dated `- **Corrected:**` header entry.

Details: [Decisions](../../decisions/README.md).

## Use the repository's agents

1. Open a pull request from a branch of the repository, titled `<work item>: <Imperative summary>`: the reviewer agent reviews it, and its request for changes starts the fixer on the branch.
2. Label it `auto-merge` to have it merged once the reviewer approved its head and the required checks are green.
3. `gh workflow run update-documentation.yml` (or `update-dependencies.yml`, `review-repository.yml`) starts a scheduled job by hand: one run writes, reviews and opens the pull request.

Details: [Use the repository's agents](repository-agents.md).
