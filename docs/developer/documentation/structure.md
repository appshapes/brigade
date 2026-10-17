# Know the structure

```
README.md                              the title, description and video; the five personas
docs/<persona>/README.md               persona root
docs/<persona>/<topic>/README.md       topic root
docs/<persona>/<topic>/<action>.md     detail page (the leaf)
CLAUDE.md                              the agent's root: @imports of docs/agent/repository/*.md
AGENTS.md                              the same files as links, for a harness without imports
docs/harnesses.json                    the harness registry the gate reads
```

The personas, in README order: `project-owner`, `team-admin`, `team-member`, `developer`, `agent`.

## The three page shapes

**A persona root** has an H1 with the persona's name, a `## If you want to` table whose rows link to the
persona's topic roots, and then exactly one H2 per topic directory: at most two sentences, one of them linking to
the topic's `README.md`. Nothing else: no procedures, no commands.

**A topic root** has an H1, a `## If you want to` table whose rows link to its own action sections (`#anchor`), and
then one H2 per action. An action section is the TL;DR: the shortest listing that gets the action done, at most 20
non-blank lines, usually a numbered list, and a last line `Details: [<title>](<page>.md)`. Every `.md` beside the
README is such a page and is named by a `Details:` line; a page nothing names is an orphan and fails the gate. A
`Details:` line may name a reference document instead of a sibling page when the details already exist there (the
Team Member's security topic does this with `docs/security.md`).

**A detail page** is the leaf. The details in one logical order, concise, no mannered prose, no restating of the
TL;DR; headings as needed; links to the reference documents for what they already say. It may link across
personas: the admin's "add a repository" page links the member's install page.

## Reference documents

Stay where code, scripts and migrations name them and are reached from the tree: `docs/security.md` (argued, kept
whole), `docs/protocol-v1.md` and `.schema.json` (frozen), `docs/adapter-authors.md`, `docs/sync-adapters.md`,
`docs/mail-connectors.md`, `docs/email/README.md`, `docs/experiments/` (indexed in its README), `docs/research/`,
`scripts/ci/README.md`, `internal/adapters/fs/README.md`, the package pages `plugin/README.md` and
`muse/brigade/README.md`, and the generated `docs/developer/reference/make-targets.md`. A directory under a persona
that holds reference pages rather than a topic is registered in the gate's `referenceDirs`.

## Style

- Lead with what the reader does; say the result they see.
- One idea per sentence. Short lists for parallel items. A table when there are columns.
- Commands in fenced blocks; a value a reader copies in backticks.
- Numbers that came from a measurement name the write-up (`docs/experiments/…`) or the security section.
- British spelling where the repository already uses it (`organisation`, `sanitise`); the Makefile and the CLI keep
  their own words.
- Dates absolute. No secrets, no addresses, no local paths of a real machine.
