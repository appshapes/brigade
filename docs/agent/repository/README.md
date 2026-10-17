# On this repository

The rules an agent follows when it changes Brigade. `CLAUDE.md` imports every page below, so a Claude Code
session has them before its first prompt; `AGENTS.md` lists them for a harness that reads no imports. Each page is
one subject and is the only place its rules live.

## If you want to

| If you want to | Go to |
| --- | --- |
| commit, push, open or close a card, place a plan or a scratch file | [Follow the conventions](#follow-the-conventions) |
| touch the protocol, stdout, a secret, a configuration directory or a dependency | [Keep the invariants](#keep-the-invariants) |
| know what must pass before a push and before a release | [Pass the gates](#pass-the-gates) |
| add or change a page, a harness's pages, or the documentation gate | [Write documentation](#write-documentation) |
| touch the release-notes email, the agentic loop or a pinned action | [Change the automation](#change-the-automation) |

## Follow the conventions

1. `master`, merges only, never rebase; `make push message="<card>: <Imperative summary>"` or `/commit <card>`.
2. Plans in `.context/plans/`, scratch in `.ignored/`; a finished piece of work adds its execution-log row.
3. A card is done when shipped and nothing on it is open; then `/trello-done <card>`, unasked.

Details: [Conventions](conventions.md).

## Keep the invariants

1. Protocol v1 is frozen; a wire change moves the types, the schema, the suite and both adapters in one commit.
2. Stdout is protocol output; never a shell spawn; never a secret on argv, in a log or under the project.
3. `CLAUDE_CONFIG_DIR ?? ~/.claude`; in a session every inherited `BRIGADE_*` is ignored; harness and adapter
   stay apart; the shipped binary links only `docs/allowed-deps.txt`.

Details: [Invariants](invariants.md).

## Pass the gates

1. `make typecheck lint build test vuln deps-check schema-check tidy-check docs-check`; `make test-all` against the
   local stack before touching `supabase/`.
2. Never hand-edit the release pins; `make plugin-check` and `make muse-plugin-check` hold the package trees;
   shellcheck 0.9.0 through Docker before pushing shell.
3. Scripts that run outside the session strip `CLAUDE*` and `AI_AGENT` by prefix.

Details: [Gates](gates.md).

## Write documentation

1. README → persona root → topic root → detail page; one `Details:` line names every leaf.
2. A harness is a registry entry and six pages; a plugin option is a row in `options.md`; a `make` help line is
   `make docs-generate`.
3. `make docs-check` fails on the shape, a broken link, an orphan, an undocumented option or a missing changelog
   section.

Details: [Documentation](documentation.md).

## Change the automation

1. Recipients of the release-notes email are a secret, never printed, committed or put on a card.
2. Agents of the loop commit with plain `git` on their branch; a `maintenance` PR needs no execution-log row.
3. Only `anthropics/claude-code-action` is pinned by full SHA; every other action stays on its major tag.

Details: [Automation](automation.md).
