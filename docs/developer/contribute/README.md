# Contribute

## If you want to

| If you want to | Go to |
| --- | --- |
| commit and push a change | [Commit and push](#commit-and-push) |
| read, add, start or finish the work item the work belongs to | [Work from the Brigade Board](#work-from-the-brigade-board) |
| hand a piece of work to the repository's agents | [Use the agentic loop](#use-the-agentic-loop) |

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

## Use the agentic loop

1. Label an issue `claude`, titled `<work item>: <title>`: the developer, reviewer and auto-merge workflows carry it from branch to squash merge.
2. Agents commit with plain `git` on their branch and never run `make commit`, `make push` or `make release`.

Details: [Use the agentic loop](agentic-loop.md).
