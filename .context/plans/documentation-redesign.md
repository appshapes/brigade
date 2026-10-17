# Documentation redesign: five personas and a release gate (card 110)

**Status:** implementing, 2026-10-17. **Owner's brief** (Rjae, 2026-10-17): documentation that is excellent for
each of five personas; a stringent gate that releases cannot pass without; humans traverse the README hierarchy by
hyperlinks, agents traverse the CLAUDE.md hierarchy by imports; more harnesses are coming after Claude Code and
Muse Code, so the shape must take a new one without redesign. Authorised to decide everything and push.

## 1. What the research showed

- **Diátaxis** (<https://diataxis.fr>; Daniele Procida, a Django core developer) is the framework most projects
  that are praised for their documentation have converged on: every page is one of four kinds (tutorial, how-to,
  reference, explanation), and the structure follows the reader's need, not the product's. Canonical has adopted it
  across its product documentation (Ubuntu, Juju, MAAS, Canonical Kubernetes, which states "every page should fit
  into one of those categories"). Brigade's persona tree is a how-to tree: a topic root's action sections are
  how-to entries, the leaves are how-to pages, and the argued documents the repository already has
  (`docs/security.md`, `docs/protocol-v1.md`, `docs/adapter-authors.md`, the experiments and research) are its
  explanation and reference, reached from the tree and never duplicated into it.
- **Generated-reference drift checks** are the common release gate: regenerate, then fail when the committed file
  differs (Grafana Loki's `check-doc` target, the Terraform provider convention of `tfplugindocs` plus a diff of
  `docs/`). Brigade applies it to the `make` target reference, generated from the Makefile's `##` help lines.
- **Cavalry** (`../cavalry`, ADR 0006, `scripts/lint-docs.py`) is the closest precedent: a stdlib script, offline
  by design (an online link check is deliberately not a gate: a site going down is not a bug in the repository),
  checking that every page is listed, every relative link and anchor resolves, every ADR is well formed, and that
  a change to a public item ships with a documentation change; wired into `just docs`, `just lint-docs`, CI on
  both runners, the commit skill and the writer's brief. Brigade takes the offline rule, the pages-links-anchors
  core and the planted-violation evidence, and puts the gate where this repository already puts its checks: a Go
  package with a drift test in `scripts/ci/`, run by `make test`, so `make push`, CI's `fast` job and
  `scripts/release-prep.sh` all run it, plus a named guard step in `release.yml`.

## 2. The shape

```
README.md                         level 0: title, description, video; the five personas
docs/<persona>/README.md          persona root: "If you want to" table of root topics; one short section per topic
docs/<persona>/<topic>/README.md  topic root: "If you want to" table of actions; one TL;DR section per action
docs/<persona>/<topic>/<action>.md  action detail: the leaf
```

Personas and directories: `project-owner` (runs the hosted backend project), `team-admin` (creates and
administers a team), `team-member` (a person whose sessions take part), `developer` (works on this repository),
`agent` (the model: in a session, and working on this repository).

Pages outside the tree stay where code, scripts and migrations name them and are reached from it as reference:
`docs/security.md` (argued; carries owner rulings, so it is kept whole and summarised, never rewritten),
`docs/protocol-v1.md` and its schema (frozen), `docs/adapter-authors.md`, `docs/sync-adapters.md`,
`docs/mail-connectors.md`, `docs/email/README.md`, `docs/experiments/`, `docs/research/`,
`scripts/ci/README.md`, `internal/adapters/fs/README.md`, and the package pages `plugin/README.md` and
`muse/brigade/README.md` (what each package ships; the member pages hold the procedures). Dissolved into the
tree: `docs/setup.md`, `docs/sync.md`, `docs/mail-gateway.md`, `docs/slack-gateway.md`, `docs/development.md`,
`docs/claude-code-usage.md`.

**Agents.** `CLAUDE.md` is the agent's root: a short preamble and `@docs/agent/repository/<rule>.md` imports, one
file per rule set (git, invariants, gates, workflow, documentation, automation, harnesses); every former `CLAUDE.md`
rule is kept, regrouped. `AGENTS.md` lists the same files for a harness that reads no imports. The agent persona
root is the human-readable page about both roles; a Brigade skill is the agent's documentation inside a team, so
`docs/agent/in-a-team/<harness>.md` lists each package's skills by path rather than restating them.

**Harnesses.** `docs/harnesses.json` is the registry: id, name, package directory, skills directory, host string.
A harness ships six pages: `docs/team-member/{install,join,update,configure,leave}/<id>.md` and
`docs/agent/in-a-team/<id>.md`, each linked from its topic root, plus a package `README.md` that links to the
install page. The gate holds all of it, so a third harness cannot land without its documentation.

## 3. The gate (`make docs-check`; `internal/docscheck`, `scripts/ci/docs_check_test.go`)

Offline, standard library only, seconds. It fails on any of:

1. `readme` — `README.md` has one H1 and exactly one H2; the H2 section lists the five persona roots, in order,
   each as a link to `docs/<persona>/README.md`; no other heading.
2. `persona` — each persona root has an `## If you want to` table whose rows link to the persona's topic roots,
   and one H2 per topic directory whose text has at most two sentences and links to that topic's `README.md`.
3. `topic` — each topic root has an `## If you want to` table whose rows link to H2 sections of the same page;
   every other H2 is an action with a `Details:` line linking to a page; every `.md` beside it is linked from
   one such line (no orphan leaf); an action section is at most 20 lines.
4. `links` — every relative link and `#anchor` in `README.md`, `AGENTS.md`, `CLAUDE.md`, `CHANGELOG.md`,
   `plugin/README.md`, `muse/brigade/README.md` and `docs/**/*.md` outside `docs/research/` resolves to a file
   or a heading. External links are not checked (offline rule).
5. `reach` — every `.md` under `docs/` outside `docs/research/` is reachable from `README.md` by links; every
   report under `docs/experiments/` is linked from its index.
6. `harness` — the six pages and the package README of every registry entry exist and are linked; every
   `<skills>/*/SKILL.md` is named in the harness's agent page; a package directory not in the registry fails.
7. `agent` — every `docs/agent/repository/*.md` is imported by `CLAUDE.md` and linked from `AGENTS.md`; every
   `@` import in `CLAUDE.md` resolves (`@./CLAUDE.user.md` may be absent: it is local-only).
8. `options` — every `userConfig` key in `plugin/.claude-plugin/plugin.json` is named in
   `docs/team-member/configure/options.md`.
9. `make` — `docs/developer/reference/make-targets.md` equals the table generated from the Makefile's `##` lines
   (`make docs-generate` rewrites it).
10. `changelog` — the version `plugin/bin/VERSION` pins, when not `0.0.0`, has a `## [<version>]` section in
    `CHANGELOG.md`. `make release` bumps the pin and then runs `make push`, whose `make test` runs this check, so a
    release with no changelog section stops before the commit.

Wiring: `make docs-check` (the test alone), `make test` (runs it with everything else), `ci.yml` `fast` job (an
explicit step after `make plugin-check`), `release.yml` (a guard step before the build), `scripts/release-prep.sh`
(a precondition), the `commit` skill and the agents' briefs. Evidence: the test plants each violation in a copy of
the tree and proves the gate reports it.

## 4. References that move

Code and scripts that name a dissolved page are repointed: `internal/harness/commands/{whoami,send}.go`,
`internal/harness/hook/hook.go`, `internal/adapters/supabase/compat.go`, `internal/harness/foldersync/*_test.go`,
`internal/syncadapters/syncthing/real_test.go`, `scripts/{gateway-install,gateway-senders,slack-gateway-install}.sh`,
`scripts/ci/keepalive.sh` and `keepalive.yml`, the Makefile help lines, both packages' skills, `plugin.json`'s
`documentationUrl`, `CHANGELOG.md`'s links, `.claude/agents/documenter.md`, `update-documentation.yml`,
`scripts/ci/setup_docs_test.go` (the witness join follows the commands to their new pages). Migration comments
that name `docs/mail-gateway.md` and `docs/slack-gateway.md` are left as written: an applied migration is not
edited.

## 5. Open after this card

- The `brigade` command reference is still the skill and `brigade help`; a generated command reference, like the
  make-target one, would let the gate pin it.
- The injection test set has not been run against Muse's model (card 88's limit); the security page says so.
