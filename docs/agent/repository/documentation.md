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
  `docs/adapter-authors.md`, `docs/sync-adapters.md`, `docs/mail-connectors.md`, `docs/email/README.md`,
  `docs/experiments/`, `docs/research/`, `scripts/ci/README.md`, `internal/adapters/fs/README.md`, and the
  package pages `plugin/README.md` and `muse/brigade/README.md`.
- **Agents enter at `CLAUDE.md`**, whose `@docs/agent/repository/<file>.md` imports load every rule file; a rule
  belongs in one of those files, never in `CLAUDE.md` itself, and every file under `docs/agent/repository/` must
  be imported there and linked from `AGENTS.md`. Inside a team, a Brigade skill is the agent's documentation:
  `docs/agent/in-a-team/<harness>.md` lists each package's skills by path.
- **A harness** is one entry in `docs/harnesses.json` (id, name, vendor, host, package, skills) and six pages:
  `docs/team-member/{install,join,update,configure,leave}/<id>.md` and `docs/agent/in-a-team/<id>.md`, each
  linked from its topic root, plus a `<package>/README.md` that links to the install page. A directory carrying a
  plugin manifest (`.claude-plugin/`, `.muse-plugin/`, `.codex-plugin/`) that the registry does not list fails the gate.
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
  files and the research pages the reachability walk follows do not count); and a `## [<version>]` changelog
  section for the version `plugin/bin/VERSION` pins. External links are never checked: a site going down is not
  a bug in this repository.
- **When you change** a `make` target's help line or a `brigade` command's table entry (name, arguments, summary
  or flags), run `make docs-generate`; a new command also needs a persona's page that names it. When you add a plugin option,
  document it in `options.md`. When you add a page, name it from a `Details:` line. When you add a harness, add
  the registry entry and the six pages. When a procedure changes, change the leaf and the TL;DR together, and
  the three setup copies (`scripts/ci/setup_docs_test.go` joins the commands across them).
