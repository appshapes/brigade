# Use the agentic loop

Larger or riskier work goes through the repository's agents instead of a session at a keyboard: label an issue
`claude`, and the developer, reviewer and auto-merge workflows carry it from branch to squash merge with no human
step; the only time a person steps in is the fix cap handing a PR back.

| Agent (`.claude/agents/`) | Role |
| --- | --- |
| `developer.md` | implements a `claude`-labelled issue on a branch, and applies the reviewer's blockers on a PR branch |
| `reviewer.md` | reviews pull requests against the repository's invariants; approves, or requests changes with specific blockers; also the monthly repository review |
| `documenter.md` | the monthly structural refresh of the persona pages: paths, make targets, versions, command names; never `docs/security.md`, `docs/protocol-v1.md` or the rule files |
| `release-notes-email.md` | writes and reviews the release-notes email |

- An issue labelled `claude` is a work order (`.github/workflows/claude.yml`). The hub approves the `ci` and
  `review pr` runs a fixer push raises at `action_required` with the owner's token the moment the fixer's job ends.
- **The number.** Title the issue `<number>: <title>` with its work item's number. The runner has no Brigade
  Board, so the agents never read one there and never invent a number: the workflow keeps the title as the pull
  request's, and the squash merge takes it. An issue whose title names no number is a maintenance work order (the
  scheduled callers write those) and is minted `15: <title>`; `15` was the build-out card of the Trello days and
  is not a work item of the board.
- Agents commit with plain `git` on their branch and never run `make commit`, `make push` or `make release`; a PR
  labelled `maintenance` is exempt from the execution-log-row rule, and its squash-merge title `<work item>:
  <Imperative summary>` satisfies the commit-message rule.
- Agent memory lives under `.context/plans/agent-memory/`, never under `.claude/`.
- `anthropics/claude-code-action` is pinned by full commit SHA, the only action in the repository that is, because
  it is the only one that runs a model with repository write access.
- The workflows, their schedules and the required GitHub configuration: [scripts/ci/README.md](../../../scripts/ci/README.md).
