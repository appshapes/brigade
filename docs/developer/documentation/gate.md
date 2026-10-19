# Run the gate

```sh
make docs-check        # go run ./cmd/brigade-docs check: one finding per line, then the summary; exit 1 on any
make docs-generate     # rewrite docs/developer/reference/make-targets.md from the Makefile's ## lines
                       # and docs/developer/reference/commands.md from internal/cli's command table
```

The gate is `internal/docscheck`, offline, nothing outside the repository and the standard library (it imports
`internal/cli` for the command table, so it needs no built binary), a second or two. `scripts/ci/docs_check_test.go`
runs it inside `make test` against the repository and against a copy of the tree with each violation planted, so
the gate is proven able to fail; `make push` therefore runs it before every commit, CI's `fast` job runs it as a
step, `scripts/release-prep.sh` runs it as a precondition, and `release.yml` runs it as a guard before the build.

| Check | Fails when |
| --- | --- |
| `readme` | `README.md` has more than one H2, a heading below H2, or its persona list is not exactly the five roots in order |
| `persona` | a persona root lacks the `If you want to` table, a row names something that is not a topic root, a topic has no section or no row, a section has more than two sentences, or a topic directory has no `README.md` |
| `topic` | a topic root lacks the table, an action has no `Details:` line or no row, a row names no action, an action section is over 20 lines, or a page beside the README is named by no `Details:` line |
| `links` | a relative link or `#anchor` in `README.md`, `AGENTS.md`, `CLAUDE.md`, `CHANGELOG.md`, the package pages or `docs/**` (outside `research/`) does not resolve; an `@import` in `CLAUDE.md` names no file |
| `reach` | a page under `docs/` (outside `research/`) is reachable from `README.md` by no chain of links; an experiment report is missing from `docs/experiments/README.md` |
| `harness` | a registry entry lacks a field, its six pages or its package page; an agent page omits a skill; a package page does not link the install page; a directory with a plugin manifest is not in the registry |
| `agent` | a file under `docs/agent/repository/` is not imported by `CLAUDE.md` or not linked from `AGENTS.md` |
| `options` | a `userConfig` key of `plugin/.claude-plugin/plugin.json` is not in `docs/team-member/configure/options.md` |
| `make` | `docs/developer/reference/make-targets.md` differs from what `make docs-generate` writes |
| `commands` | `docs/developer/reference/commands.md` differs from what `make docs-generate` writes from `internal/cli`'s command table |
| `coverage` | a command `brigade help --all` lists is named as `brigade <name>` on no page of the tree other than the generated reference, `CHANGELOG.md` and the experiment reports |
| `changelog` | `plugin/bin/VERSION` pins a version (not `0.0.0`) with no `## [<version>]` section in `CHANGELOG.md` |

External links are never checked: a site going down is not a bug in this repository. Markdown inside fenced code
blocks and inline code is ignored. An anchor matches a heading by GitHub's slug rule, and by letters and digits
alone when a dialect differs.

A finding reads `docs-check: <check>: <path>: <what>`; fix the page it names. The planted violations in the test
are the catalogue of what each check catches.
