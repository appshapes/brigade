# Using Claude Code

How this repository is worked on with Claude Code: the agents, the slash-command skills, the daily
workflow, the Trello CLI the ticket numbers come from, and a reference for every command. Type any
command directly in the Claude Code chat.

## Agents

Three agents are configured in `.claude/agents/`. They are the roles of the repository's agentic
loop (`.github/workflows/claude.yml` and its siblings; the workflow table is in
[`scripts/ci/README.md`](../scripts/ci/README.md)), and a session can also address them directly.

| Agent | Purpose |
|---|---|
| `@developer` | Implements a `claude`-labelled issue on a branch, and applies the reviewer's blockers on a PR branch |
| `@reviewer` | Reviews pull requests against the repository's invariants; approves, or requests changes with specific blockers |
| `@documenter` | Keeps the structural documentation (paths, make targets, versions, command names) in step with the tree; runs monthly |

The agents commit with plain `git` on their branch and never run `make commit`, `make push` or
`make release`; their memory lives under `.context/plans/agent-memory/`.

## Skills

Invoked via `/command`:
- `/commit <ticket-id>` - Commit, pull and push through `make push` with a `<ticket-id>: <Imperative summary>` message
- `/playwright-cli` - Browser automation for testing, screenshots and data extraction
- `/trello-create <title> [--board] [--list] [--from file.md]` - Create a Trello card (defaults: board AppShapes, list Wanting)
- `/trello-read <number | title | id>` - Read a Trello card: description, comments, checklists, attachments

The Brigade plugin's own skills — `/brigade:setup`, `/brigade:join`, `/brigade:update`,
`/brigade:sessions` and the team-messaging skill that handles incoming frames — are documented in
[`plugin/README.md`](../plugin/README.md); they are the product, not the tooling for working on it.

## Daily Workflow

Brigade is developed on `master`: merges only, never rebase, and every commit goes through the
`make push` gate (typecheck, pull, build, test). A ticket is a card on the AppShapes Trello board;
its number is the commit prefix (`20:` for the intermittent-failures card; the history before
2026-09-13 carries `15:`, the build-out card). A commit that finishes a plan row updates the
execution log in the same commit.

Your typical cycle for working on a card:

```
1.  /trello-read 20                     Read the card (or write one with /trello-create)
2.  Describe what you want, iterate,    Work with Claude Code; plans in .context/plans/,
    make test                           scratch in .ignored/ (gitignored)
3.  /commit 20                          Commit and push to master when done
```

Steps 1 and 3 are slash commands; step 2 is normal conversation with Claude Code. Larger or riskier
work goes through the agentic loop instead: label an issue `claude`, and the developer, reviewer
and auto-merge workflows carry it from branch to squash merge. The fixer's pushes start their own CI and
review runs without a human only because the repository's fork-pull-request approval policy gates accounts
new to GitHub alone; `scripts/ci/README.md` (Required GitHub configuration) records that setting.

## Trello CLI

The ticket numbers are Trello card numbers, and the two Trello skills use
[mheap/trello-cli](https://github.com/mheap/trello-cli):

1. `npm install -g trello-cli`
2. Create an API key at https://trello.com/power-ups/admin (New → API key).
3. `trello auth:api-key <key>` — it prints a token URL; open it, click Allow, copy the token.
4. `trello auth:token <token>`
5. `trello sync`
6. `trello board:list` — the AppShapes board should be listed.

The key and token live only in `~/.trello-cli/default/config.json` (and the secrets file the
`trello-create` skill names); they never appear in the repository, a card, or the chat.

## All Commands

### `/commit`

Analyze changes, generate a commit message (`<ticket-id>: <Imperative summary>`), confirm with you,
then run `make push`, which typechecks, pulls, builds, tests, stages every working-tree change,
commits and pushes. It refuses without a numeric ticket id and stops on any gate failure.

Example: `/commit 20`

### `/playwright-cli`

Browser automation for testing, screenshots, form filling and data extraction.

Example: `/playwright-cli`

### `/trello-create`

Create a Trello card. Board defaults to `AppShapes`, list to `Wanting` (the other lists are Doing,
Completing and Pinning). With `--from file.md`, the file's first heading is the title and the rest
is the description. Trello assigns the card number; the title carries none.

Examples:
- `/trello-create Retry supabase start once in CI's supabase job`
- `/trello-create --list Doing --from .ignored/card.md`

### `/trello-read`

Read a Trello card by ticket number, title or id: description, comments, checklists, attachments.
Search lags for a card created in the last hour; the skill falls back to listing the board.

Example: `/trello-read 20`
