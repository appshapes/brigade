# Documentation: where a page goes, and the gate that holds it

The documentation is one tree for humans and one for agents (card 110, `.context/plans/documentation-redesign.md`).

- **Humans enter at `README.md`**, which has two parts only: the title, description and video; and the five
  personas, each a link to its root. The roots are `docs/project-owner/`, `docs/team-admin/`, `docs/team-member/`,
  `docs/developer/` and `docs/agent/`, each a `README.md`.
- **A persona root** has an `## If you want to` table whose rows link to the persona's topic roots, then one H2
  per topic: at most two sentences and a link to the topic's `README.md`. Nothing else.
- **A topic root** (`docs/<persona>/<topic>/README.md`) has an `## If you want to` table whose rows link to its
  own action sections, then one H2 per action: the TL;DR (the shortest listing that gets it done, at most 20
  lines) and a `Details:` line linking to the action's page. Every `.md` beside the README is such a page and is
  named by exactly one `Details:` line; a page nothing names fails the gate.
- **A detail page** is the leaf: the details, concise, in one logical order, no mannered prose, no restating of
  the TL;DR. It may link to a reference document instead of repeating it.
- **Reference documents stay where code names them** and are reached from the tree: `docs/security.md` (argued;
  its sentences carry owner rulings, so summarise it and never rewrite it), `docs/protocol-v1.md` (frozen),
  `docs/adapter-authors.md`, `docs/backend-rest-v1.md` (frozen at version 1 with work item 89's first server release),
  `docs/sync-adapters.md`, `docs/mail-connectors.md`, `docs/webhook-gateway.md`,
  `docs/email/README.md`,
  `docs/experiments/`, `docs/research/`, `docs/decisions/` (the ADRs, below), `scripts/ci/README.md`,
  `internal/adapters/fs/README.md`, and the
  package page of every registered harness (`plugin/README.md`, `muse/brigade/README.md`,
  `codex/brigade/README.md`, `grok/brigade/README.md`).
- **A decision is an ADR** (ADR 0001, work items 302 and 309): one file `docs/decisions/NNNN-short-title.md`,
  copied from `0000-template.md`, with the next number, listed in `docs/decisions/README.md`, in the same change
  as the code, the rule or the page it explains. Its Status is `Accepted`, `Accepted; superseded in part by NNNN`,
  `Superseded by NNNN` or `Rejected`, never Proposed. Once it is on `master` it keeps its text: its Status line
  changes when a later ADR supersedes it, and a fact that was wrong when it was written is corrected in place
  under a dated header entry, `- **Corrected:** YYYY-MM-DD: <what was wrong, and what the ADR says now>`, after
  the Supersedes line and any earlier entry. The title, the other header lines and the Decision section never
  change, and an ADR is never removed or renamed: a change of mind is a new ADR that supersedes the old one.
  Two things are not a change: a link in it is repointed when the page it names moves, and when two ADRs have
  landed under one number, one of them takes the next free number with nothing else changed.
  Which changes need one, and where a decision lives before it is made, is in [conventions.md](conventions.md).
- **Agents enter at `CLAUDE.md`**, whose `@docs/agent/repository/<file>.md` imports load every rule file; a rule
  belongs in one of those files, never in `CLAUDE.md` itself, and every file under `docs/agent/repository/` must
  be imported there and linked from `AGENTS.md`. Inside a team, a Brigade skill is the agent's documentation:
  `docs/agent/in-a-team/<harness>.md` lists each package's skills by path.
- **A harness** is one entry in `docs/harnesses.json` (id, name, vendor, host, package, skills) and six pages:
  `docs/team-member/{install,join,update,configure,leave}/<id>.md` and `docs/agent/in-a-team/<id>.md`, each
  linked from its topic root, plus a `<package>/README.md` that links to the install page. A directory carrying a
  plugin manifest (`.claude-plugin/`, `.muse-plugin/`, `.codex-plugin/`, `.grok-plugin/`) that the registry does
  not list fails the gate.
- **The gate** is `make docs-check` (`internal/docscheck`, `cmd/brigade-docs`, `scripts/ci/docs_check_test.go`),
  offline, in `make test`, CI's `fast` job, `scripts/release-prep.sh` and `release.yml`. Its rules: the README
  shape; the persona and topic shapes above; every relative link and `#anchor` in the tree, the changelog and the
  package pages resolves; every page under `docs/` outside `docs/research/` is reachable from `README.md`, and
  every experiment report is in its index; the six pages of every harness; the rule files' imports; every
  `userConfig` option of `plugin/.claude-plugin/plugin.json` named in `docs/team-member/configure/options.md`;
  `docs/developer/reference/make-targets.md` equal to what `make docs-generate` writes from the Makefile's `##`
  help lines; `docs/developer/reference/commands.md` equal to what it writes from `internal/cli`'s command table
  (card 112; the generator calls `cli.Commands`, `cli.UsageAll`, `cli.UsageCommand` and `Command.OwnFlags`, so
  the gate needs no built binary); every command `brigade help --all` lists named as `brigade <name>` on at least
  one page of the tree other than the generated reference, `CHANGELOG.md` and the experiment reports (the skill
  files and the research pages the reachability walk follows do not count); a `## [<version>]` changelog
  section for the version `plugin/bin/VERSION` pins; and the ADRs (the `decisions` check,
  `scripts/ci/docs_decisions_test.go`): each one's name, number, title line, Status and `## Decision` section, its
  place in the index's list, and, read from git, that an ADR which has landed on the default branch keeps its
  text as the bullet above says, whether the edit is in the working tree, in a commit not yet on that branch or
  in a pull request (a tree that is no repository skips that half; an ADR a branch added may be revised until it
  merges; the gate does not look back at what the default branch already did). External links are never checked: a site going down
  is not a bug in this repository.
- **When you change** a `make` target's help line or a `brigade` command's table entry (name, arguments, summary
  or flags), run `make docs-generate`; a new command also needs a persona's page that names it. When you add a plugin option,
  document it in `options.md`. When you add a page, name it from a `Details:` line. When you add a harness, add
  the registry entry and the six pages. When a procedure changes, change the leaf and the TL;DR together, and
  the three setup copies (`scripts/ci/setup_docs_test.go` joins the commands across them). When a change needs
  an ADR, write it in the same change and list it in the index.
