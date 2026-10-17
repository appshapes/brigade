# Contribute

## If you want to

| If you want to | Go to |
| --- | --- |
| commit and push a change | [Commit and push](#commit-and-push) |
| read, create or close the Trello card the work belongs to | [Work a card](#work-a-card) |
| hand a piece of work to the repository's agents | [Use the agentic loop](#use-the-agentic-loop) |

## Commit and push

1. `make push message="<card>: <Imperative summary>"`, or `/commit <card>` in Claude Code.
2. `master` only, merges only, never rebase; plans in `.context/plans/`, scratch in `.ignored/`.

Details: [Commit and push](commits.md).

## Work a card

1. The number in a commit message is an AppShapes Trello card; `/trello-read <n>`, `/trello-create <title>`, `/trello-done <n>`.
2. A card is done when shipped and nothing on it is open; then close it, unasked.

Details: [Work a card](cards.md).

## Use the agentic loop

1. Label an issue `claude`: the developer, reviewer and auto-merge workflows carry it from branch to squash merge.
2. Agents commit with plain `git` on their branch and never run `make commit`, `make push` or `make release`.

Details: [Use the agentic loop](agentic-loop.md).
