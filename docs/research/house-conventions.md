# House conventions — Rjae's repositories, read against Brigade (P6-1, 2026-09-04)

The input to Phase 6 (`.context/plans/brigade-execution-log.md`, "Phase 6 — house conventions"). P6-1 is a
**reading** task: nothing in P6-2..P6-4 may be invented from taste, and where a convention collides with a
load-bearing Brigade guard, the collision is recorded and the guard wins (the log names them: the `plugin/`
file allowlist and modes, the reproducibility job's byte-identical cross-build, `make test` staying
Docker-free, the by-prefix `CLAUDE*` strip, `15: <Imperative summary>` commit messages, merges only).
Phase 6 runs **before** Phase 5 so the release plumbing is reshaped while it is still free to change.

**Repositories read** (read-only, at the checkouts on this machine, 2026-09-04): the two Rjae named —
`/Users/rjae/Development/thinktech/thinktech-web` (Makefile 284 lines, `README.md`, `.github/workflows/`,
`Jenkinsfile`, `scripts/`, `deploy/`, `CLAUDE.md`) and `thinktech-app` (Makefile 253 lines, `README.md`,
`.github/workflows/`, `.documentation/`) — plus, for the two conventions those two do not show,
`thinktech-php` and `thinktech-api` and a sweep of every sibling checkout under
`/Users/rjae/Development/thinktech/`. Citations are `repository/path:line`.

**Score: 8 statements — 3 confirmed, 4 refined, 1 contradicted as stated (0 not found).** Every one of the
eight occurs somewhere; two of them (the README TOC, the `.docs/` detail folder) occur in repositories other
than the two named, which is why the sweep was needed.

| # | The owner's statement (verbatim, 2026-09-04) | Verdict | One-line reason |
| --- | --- | --- | --- |
| 1 | Prefer all "scripts" be inlined as `Makefile` targets | **refined** | The repos inline *invocations* (1–8 line recipes), never *logic*: real logic lives in a file (`scripts/*.mjs`, `bin/*.php`, `.github/scripts/*.py`, a 912-line `deploy/aws/*.sh`) that a target or a runner calls |
| 2 | Organize `Makefile` into categories/groups | **confirmed** | `# ========== Name ==========` banners, exact form, in both named repos; Brigade already matches |
| 3 | Avoid excessive or arbitrary targets; every target **must** need to exist | **confirmed as the rule, contradicted by the sample** | The rule is real (a target exists to *store knowledge* — a flag set, an ARN, an order), but the repos keep 43–45 targets each including pure one-line aliases |
| 4 | Workflows use `Makefile` targets wherever possible | **contradicted as stated / refined** | The **Jenkins** deploy pipeline is 100% `make` in all three server repos; **GitHub Actions** calls `make` only in thinktech-app — thinktech-web calls `pnpm`, thinktech-php calls `php bin/…` and `python3 .github/scripts/…` |
| 5 | Maintain a TOC navigation in `README.md` | **confirmed — but not in the two named repos** | `thinktech-php/README.md:5` (anchor bullet list) and `thinktech-api/README.md:7` (a "You want to… / Go to" task table + every heading linking back). thinktech-web and thinktech-app have no TOC |
| 6 | `README.md` sections concisely summarize, link to details in `.docs/` folder | **refined** | The pattern is real and everywhere; the folder's *name* is not `.docs/` in most repos — `.documentation/` (api, app), `.docs/context/` (php), `.context/reference/` (web) |
| 7 | Releasing is a product of merging `master` to target branch (e.g. `development`, `production`) | **refined** | True wherever a long-lived branch is the deploy substrate (web/php/api each have `origin/development` + `origin/production`); thinktech-app has **neither branch** and releases from a `v*` **tag** |
| 8 | Versioning: "In thinktech-app I use a `Makefile` target to bump the version." | **refined** | It is `version-set version=X.Y.Z` — a **set**, not a bump: no auto-increment, and the *pair* is `version-get` + `version-set`, with `deploy` re-reading the version to build the tag |

---

## 1. Convention by convention

### 1.1 "Scripts inlined as `Makefile` targets" — REFINED

**Where it occurs.** Every recipe in both named Makefiles is a thin call into the project's own runner:
`thinktech-web/Makefile:54-55` (`build: ## Build the Nitro server bundle` → `pnpm build`), `:73-75`
(`test:` → `pnpm test --run`), `:134-136` (`helm-upgrade:` → one `helm upgrade --install …` line),
`thinktech-app/Makefile:24-26` (`build:` → `./gradlew build`), `thinktech-api/Makefile:17-18`
(`build: lint` → `dotnet build -warnaserror`).

**The exact form.** The measured ceiling for an *inline* recipe in these repos is about eight lines, and
every recipe at that length is a shell `if`/loop that could not be a single command:

- `thinktech-web/Makefile:158-163` — 6-line `@if [ -z "$(explore_cdn_production)" ]; then … else … fi`,
  backslash-continued, one `\` per line, `@`-prefixed so the `if` itself is not echoed.
- `thinktech-web/Makefile:186-189` — the optional floating-tag push, same shape.
- `thinktech-app/Makefile:88-94` — `deploy`, 7 lines, `@ver=$${version:-$$(grep …)}; \` … a single shell
  invocation held together by `; \`, with `$$` for shell variables and `$(version)` for make variables.
- `thinktech-app/Makefile:218-225` — `tail-android`, 8 lines, `if`/`else` over `adb`.
- `thinktech-app/Makefile:245-249` — `version-set`, four `sed -i ''` calls (BSD sed, macOS-only).

**And what is *not* inlined.** Logic longer than that lives in a file, and the file is not always reached
through make at all:

| File | Lines | Invoked by |
| --- | --- | --- |
| `thinktech-web/scripts/*.mjs` (13 files, `#!/usr/bin/env node`) | 2,902 total | `package.json:21-27` (`"verify:layout": "node scripts/verify-layout.mjs"`), then `pnpm verify:layout` from `ci.yml:94`; only ONE of them has a make target (`Makefile:77-79 verify-ownership`) |
| `thinktech-web/deploy/aws/explore-create.sh` | 912 | nothing — a plan/apply script the owner runs by hand (`"No IaC, no CI credential: the owner runs this from their own session"`, line 12) |
| `thinktech-php/bin/check-phinx-ownership.php` | — | `.github/workflows/phinx-ownership.yml:43` — `run: php bin/check-phinx-ownership.php`, no make target exists |
| `thinktech-php/.github/scripts/weekly-cluster-metrics.py` | — | `.github/workflows/send-weekly-metrics.yml:34` — `run: python3 .github/scripts/weekly-cluster-metrics.py`, no make target, and documented in its own `.github/scripts/README.md` |

**Brigade today.** `Makefile:260` `$(unclaude) scripts/proof.sh`, `:264`, `:268`, `:278-279`, `:355`, `:360`,
`:272` — every script is reached through a target except two (see §2). Recipes are one to three lines; the
longest is `cross` (`Makefile:346-352`, a 5-line `for t in $(targets)` loop) — inside the house ceiling.

**Verdict: match on the real convention, and the statement's literal reading would break Brigade.** The
convention is "every script is *reachable* by a target", not "the script's body lives in the recipe".
Inlining `scripts/proof.sh` (1,694 lines) into a recipe is not something any of these repositories does to
anything.

### 1.2 Group banners — CONFIRMED, exact form

**Where and the exact string.** `# ========== <Name> ==========`, ten equals signs on each side, one blank
line before and after, groups separated by a blank line:

- `thinktech-web/Makefile:4` `# ========== Variables ==========`, `:32` `Help`, `:43` `Setup`, `:51`
  `Build / Test / Lint`, `:81` `Run`, `:87` `Git`, `:112` `Database`, `:122` `Deploy`, `:165` `Docker`,
  `:215` `FNM`, `:233` `Kubernetes`.
- `thinktech-app/Makefile:13` `# ========== Setup Commands ==========`, `:22` `Build Commands`, `:40`
  `Code Quality Commands`, `:72` `Run`, `:84` `Release`, `:137` `Test`, `:155` `Dependency`, `:161`
  `Web App`, `:175` `Git`, `:187` `Git Hooks`, `:197` `CI`, `:208` `Utility` — all suffixed `Commands`.

The two named repos disagree on the suffix, and the **newer** file drops it: thinktech-web (mtime
2026-08-28) uses bare nouns, thinktech-app (May 2026 shape) appends `Commands`. Treat the bare noun as
current. `thinktech-api/Makefile` has **no groups at all** — 40 targets in one alphabetical run, no `##`
docs, no `help` — so the group convention is a recent practice, not an old one.

**Ordering.** Variables first, then `Help`, then the rest roughly by workflow (setup → build → run → git →
deploy → infra). Targets are alphabetical *within* a group in thinktech-api and roughly so elsewhere.
`.DEFAULT_GOAL := help` sits at the **end** of the file under a bare `# Default target` comment
(`thinktech-web/Makefile:275-276`, `thinktech-app/Makefile:252-253`).

**Brigade today.** `Makefile:1-2` carries the same header as `thinktech-web/Makefile:1-2` — *"Conventions:
lowercase variable names; per-target .PHONY; ## doc-comments scraped by help."* — the same
banner form at `:4, 66, 77, 101, 274, 338, 367, 432, 454, 469`, and `.DEFAULT_GOAL := help` at `:488` under
`# Default target`. Bare-noun group names. **Match.**

**One inherited wart worth fixing in P6-3.** `thinktech-web/Makefile:278-284` appends
`s3-bucket-policies` *after* `.DEFAULT_GOAL` at :276 — an ungrouped target below the file's own footer.
Brigade has not copied that; do not.

### 1.3 The `help` scrape, `##` docs and `.PHONY` — CONFIRMED, with a live Brigade defect

**The scrape, byte-for-byte** (`thinktech-web/Makefile:41`, `thinktech-app/Makefile:11`, differing only in
the column width `%-22s` vs `%-20s`):

    @grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-22s %s\n", $$1, $$2}'

preceded by four `@echo` lines: a title, blank, `Usage: make [target] [var=value ...]`, blank,
`Available targets:` (`thinktech-web/Makefile:36-40`).

**Doc-comment form.** `target: [deps] ## Sentence case, no trailing period`, and where the target takes
variables the doc carries the usage inline: `## Exec into a running container (usage: make docker-exec
container=... command=sh)` (`thinktech-web/Makefile:199`), `## Set app version (usage: make version-set
version=1.0.22)` (`thinktech-app/Makefile:243`). A target deliberately kept **out** of `help` simply has no
`##` — the only instances are `claude-run` (`thinktech-app/Makefile:234-236`, `thinktech-api/Makefile:20-21`).

**`.PHONY` form.** One `.PHONY: <target>` line immediately above each target (both named repos, stated at
`thinktech-web/Makefile:2`). The older repos use one aggregate line at the top instead
(`thinktech-php/Makefile:1`) or a single stray `.PHONY: deploy` (`thinktech-api/Makefile:1`).

**Brigade today.** `Makefile:69-75` is the same block with `%-22s`; per-target `.PHONY` throughout.

> **DEFECT, found by running it.** The scrape's `^[a-zA-Z_-]+:` matches no **digit**, so
> `e2e: build ## Phase 4 no-LLM proof …` (`Makefile:258-259`) is **invisible in `make help`** — `make help |
> grep -c e2e` returns 0, while all 54 other targets list. It is the target CI runs (`ci.yml:126`) and the
> one a contributor is most likely to look for. thinktech-web and thinktech-app happen to have no target
> with a digit in its name, so they never hit it. P6-3: widen to `^[a-zA-Z0-9_-]+:` (or rename the target),
> and keep the width at 22 — Brigade's longest listed name is `supabase-config-push` at 20.

### 1.4 "Every target must need to exist" — CONFIRMED as the rule; the sample is looser than the rule

**What the repos actually justify.** A target earns its place when it *stores knowledge* the caller would
otherwise have to remember: the `--namespace/--context` pair (`thinktech-web/Makefile:239-241`), an ARN or
a policy file path (`:191-196`), the ORDER of a gate (`:94` `commit: typecheck pull build test
verify-ownership`, whose ordering rationale is written out in `thinktech-web/CLAUDE.md:208`), a fail-soft
rule (`:146-154`), or a Jenkins entry point (`:138-144`, doc-commented `## Jenkins-invoked:`).

**But the same files keep targets that store nothing**: `thinktech-app/Makefile:213-214` `fix: format`
(an alias of `format`, itself an alias of `ktlint-format` at `:210-211`), `:157-159` `deps:` →
`./gradlew dependencies`, `thinktech-api/Makefile:75-76 docker-decode` / `:81-82 docker-encode`,
`:161-162 ngrok-run`. Counts: thinktech-web **45** targets, thinktech-app **43**, Brigade **55**.

**Verdict.** The rule is real and the owner states it more strictly than her repositories practise it.
Applied to Brigade it has exactly one unambiguous bite, and it is a good one — §2 names it.

### 1.5 "Workflows use `Makefile` targets wherever possible" — CONTRADICTED AS STATED, refined by system

This is the statement the first grep tripped over, and the split is clean.

**The Jenkins deploy pipeline is 100% `make`, in all three server repos** — and each Jenkins container
*installs* make first because the image lacks it:

| Citation | Line |
| --- | --- |
| `thinktech-web/Jenkinsfile:69` | `sh 'apk add --no-cache make'` |
| `thinktech-web/Jenkinsfile:71,72` | `sh 'make docker-deploy-build'`, `sh "make docker-deploy-push moniker=${env.GIT_BRANCH}"` |
| `thinktech-web/Jenkinsfile:93,113,134,154` | `make deploy-development`, `make invalidate-explore-development`, `make deploy-production`, `make invalidate-explore-production` |
| `thinktech-php/Jenkinsfile:72,73,94,114,135,155` | the same six stages, `make docker-deploy-build/-push`, `make deploy-{development,production}`, `make invalidate-cdn-{development,production}` |
| `thinktech-api/Jenkinsfile:72,73,94,115` | `make deploy-build-api`, `make deploy-push-api`, `make deploy-api-{development,production}` |

Every one of those targets is doc-commented `## Jenkins-invoked:` in the Makefile
(`thinktech-web/Makefile:139,143,147,157`). **This is the strongest form of the convention in the whole
sample**: the pipeline holds branch gates, credentials and Slack notification and *no build knowledge at
all*.

**GitHub Actions is mixed, and follows the language's own runner:**

- **thinktech-app calls make** — `.github/workflows/ci.yml:60` `run: make lint` (with a 3-line comment
  explaining that `make lint` + the test jobs together equal `make validate`), and
  `.github/workflows/release-android.yml:93` `run: make validate`. Its runner is gradle, which the Makefile
  already wraps.
- **thinktech-web does not** — `ci.yml` runs `pnpm verify:layout` (`:94`), `pnpm verify:stack` (`:96`),
  `pnpm verify:queue` (`:100`), `pnpm verify:components` (`:103`), `pnpm verify:ui-colors` (`:108`),
  `pnpm typecheck` (`:132`), `pnpm lint` (`:143`), `pnpm test --run --shard=…` (`:162`), `pnpm build`
  (`:204`), `pnpm exec playwright test …` (`:243`), `pnpm verify:ownership` (`:258`) — **zero** `make`.
  The Makefile instead documents the correspondence in its doc comments: *"vue-tsc over the generated Nuxt
  types (mirrors the CI `Typecheck` step)"* (`Makefile:62`) and *"Drizzle ownership lint … (mirrors the CI
  `drizzle-ownership` job)"* (`Makefile:78`).
- **thinktech-php does not** — `phinx-ownership.yml:43` `run: php bin/check-phinx-ownership.php`,
  `permission-writers.yml:43` `run: php bin/check-permission-writers.php`,
  `send-weekly-metrics.yml:34` `run: python3 .github/scripts/weekly-cluster-metrics.py`. No make target
  exists for any of the three.

**Brigade today.** Go has no `package.json` script layer, so `make` *is* Brigade's runner and `ci.yml`
already follows the thinktech-app shape: 12 of its ~15 substantive steps are `make …`
(`ci.yml:32,33,34,35,37,41,50,58,59,97,98,99,100,126,132`). Three steps are not, and each is defensible:
`ci.yml:58`'s raw `go test ./...` on macOS (a deliberately different command from `make test`),
`release.yml:33` `scripts/ci/release-verify.sh` and `keepalive.yml:47` `scripts/ci/keepalive.sh`.

**Verdict: match, with the `keepalive` question left open in §2.** The house rule as practised is: *the
deploy pipeline calls only make; a CI gate calls whatever the project's canonical runner is.* For Brigade
those are the same thing.

**Two workflow-layout conventions Brigade does not have** (both house, both cheap):

1. **A workflows README.** `thinktech-app/.github/workflows/README.md:1-14` — a `| Workflow | Trigger |
   Purpose |` table split into "Build & release" and "Agent automation", linked from
   `thinktech-app/README.md:57`. `thinktech-php/README.md:834-845` carries the same table inline in the
   root README. Brigade has three workflows and no such table anywhere.
2. **A scripts README beside workflow-only scripts.** `thinktech-php/.github/scripts/README.md` documents
   `weekly-cluster-metrics.py`: what invokes it, its runtime dependencies, and a
   `| Type | Name | Notes |` table of every required secret and variable. This is a direct precedent for
   what `scripts/ci/` should carry — Brigade's `keepalive.yml:22-31` puts that same content in a workflow
   header comment instead.

Also house but absent from Brigade: a **composite action** for repeated setup
(`thinktech-web/.github/actions/setup-node-pnpm/action.yml`, factored out precisely because three workflows
shared it; Brigade repeats `actions/checkout@v7` + `actions/setup-go@v7` in four jobs); `timeout-minutes`
on **every** job (`thinktech-web/ci.yml:50,89,124,138,153,180,251,271`, with the reason written at
`:24-26`: six jobs once hung for six hours on the 360-minute default — Brigade sets it on one job of five,
`ci.yml:90`); and an **aggregator job** as the single required branch-protection context
(`thinktech-web/ci.yml:260-283`, `build:`, `needs: [changes, gates, typecheck, lint, test, e2e-public]`,
`if: ${{ !cancelled() }}`, whose only step `jq`-asserts every `needs.*.result`). Brigade's
`permissions: {contents: read}` + `concurrency: {group, cancel-in-progress: true}` (`ci.yml:17-18`) already
match `thinktech-web/ci.yml:9-16` and `thinktech-app/ci.yml:10-12`.

### 1.6 README TOC — CONFIRMED, and neither named repo has one

**Where it occurs.** Two forms, both in repos outside the two named:

1. **Anchor bullet list** — `thinktech-php/README.md:5-52`: `## Table of Contents` then a two-level
   `- [Section](#section)` list, sub-items indented two spaces, duplicate anchors disambiguated by GitHub's
   own suffix (`:48` `- [Feature Flags (Usage Guide)](#feature-flags-1)`).
2. **Task-routing table + heading back-links** — `thinktech-api/README.md:7-23`: `# Table of Contents`, then
   a `| You want to… | Go to |` table whose left column is a *job* in bold (`**Get started**`,
   `**Develop**`, `**Operate**`, `**Work with Schoology**`) and whose right column is `·`-separated anchor
   links, mixing in-page anchors with links into `.documentation/*.md` (`:14-16`). Then **every** top-level
   heading is written as a link back to it: `# [Overview](#table-of-contents)` (`:25`),
   `# [Repository layout](#table-of-contents)` (`:29`), `# [Dependencies](#table-of-contents)` (`:54`),
   `# [Build](#table-of-contents)` (`:62`), `# [Test](#table-of-contents)` (`:76`). A build badge sits
   above the TOC in an HTML `<table>` (`:1-5`).

Form 2 is the more developed one and the one that answers "TOC **navigation**".

**Not present in**: `thinktech-web/README.md` (41 lines, six `##` sections, no TOC),
`thinktech-app/README.md` (223 lines, no TOC — though `:23-34` is a `| Task | Command |` table that does
the same routing job for commands), `thinktech-www`, `compass`.

**Brigade today.** `README.md` is 79 lines with two `##` sections (`:22` "For adapter contributors",
`:66` "Layout") and no TOC. Its `Layout` table (`:68-79`, `| Path | What |`) is the api-style routing table
pointed at the tree rather than at itself. **Gap** — and a small one: at 79 lines a php-style anchor list
would be noise; the api-style `| You want to… | Go to |` table is the right target, and it should route to
`docs/adapter-authors.md`, `docs/protocol-v1.md`, `docs/setup.md`, `plugin/README.md` and
`.context/plans/brigade-execution-log.md`, which is what a reader actually needs.

### 1.7 "Sections summarize, details in `.docs/`" — REFINED (the pattern is house; the folder name is not `.docs/`)

**Where each folder actually occurs** (swept across all sixteen checkouts under `/Users/rjae/Development/thinktech/`):

| Folder | Repos | Contents |
| --- | --- | --- |
| `.documentation/` | thinktech-api, thinktech-app (+ their `-work`/`-fix` worktrees) | `api/.documentation/{infinite-campus,schoology,student-demographics}.md`; `app/.documentation/{README.md,development.md,release.md}` |
| `.docs/` | thinktech-php (+ worktrees) | `.docs/context/resource-center/editing-guide.md`, `.docs/context/lockdown-browser-kiosk-mode/guide.md` — **product-facing** guides, one directory per topic, the file named `guide.md` or `<topic>-guide.md` |
| `.context/reference/` | thinktech-web | the engineering detail (`ci-deploy.md`, `explore.md`) |
| `docs/` (no dot) | thinktech-www, thinktech-resources | — |

**The exact linking form.**

- Root README summarizes and links out: `thinktech-app/README.md:59-65` — a `## Documentation` section of
  three lines, `- **[Development Guide](.documentation/development.md)** - Development environment setup`,
  and `thinktech-app/README.md:57` routes CI/agent detail to `.github/workflows/README.md` in one sentence.
- `thinktech-api/README.md:14-16` puts the `.documentation/*.md` links **in the TOC table itself**, beside
  in-page anchors, so a reader never learns whether the destination is a section or a file.
- The detail folder gets its own index when it has more than two files:
  `thinktech-app/.documentation/README.md:1-17` (`## Documentation` links + `## Quick Commands` +
  `## Project Info`).
- In prose, the summary→detail pointer is written with an arrow and a section:
  `thinktech-web/CLAUDE.md:208` ends *"→ `.context/reference/ci-deploy.md` § Git targets (`make pull` /
  `make commit` / `make push`)"*.
- File naming is lowercase-hyphenated, topic-first, no dates, no ticket numbers: `development.md`,
  `release.md`, `schoology.md`, `student-demographics.md`, `infinite-campus.md`, `ci-deploy.md`.

**Brigade today.** `docs/` (visible, no dot) holds `protocol-v1.md`, `adapter-authors.md` (143 KB),
`setup.md`, `allowed-deps.txt`, `protocol-v1.schema.json`, `experiments/` (14 files + its own
`README.md`), `research/` (this file's home, with its own `README.md` index). Naming already matches
(lowercase-hyphenated, topic-first). `README.md:66-79`'s Layout table points into it. The dot-prefix
differs, and **it should stay different**: `docs/adapter-authors.md` and `docs/protocol-v1.md` are the
public contract that third-party adapter authors are told to read (`README.md:27-34`), so hiding them
behind a dot would be a regression, not a convention. `docs/experiments/README.md` and
`docs/research/README.md` are already the per-folder index the house pattern asks for.

**Verdict: match on intent and on naming; deliberate divergence on the dot, recorded here so P6-5 need not
re-litigate it.**

### 1.8 Release model — REFINED: two models, chosen by deployment substrate

**Model A — merge `master` → a long-lived branch, Jenkins deploys the branch.** The three server repos.
The recipe is *byte-identical* in all three (`thinktech-web/Makefile:124-132`,
`thinktech-php/Makefile:37-44`, `thinktech-api/Makefile:46-53`):

    deploy: ## Promote $(source) into $(target) on origin (default: master -> development)
    	$(eval current_branch := $(shell git rev-parse --abbrev-ref HEAD))
    	git checkout $(target)
    	git pull
    	git merge origin/$(source) --no-edit
    	git push
    	git checkout $(current_branch)
    	git pull

with `source := master` and `target := development` as file-level defaults
(`thinktech-web/Makefile:13,15`), so `make deploy` promotes to development and
`make deploy target=production` to production. `git branch -r` confirms `origin/development` and
`origin/production` exist in thinktech-web, thinktech-php and thinktech-api. The push is the trigger:
`thinktech-php/README.md:825` — "Jenkins pipeline (`Jenkinsfile`) triggers on pushes to `development` and
`production` branches", and every Jenkinsfile stage is gated on
`env.GIT_BRANCH == "development" || == "production"` (`thinktech-web/Jenkinsfile:28-37`).
`thinktech-php/README.md:857-867` documents it under `## Manual Deploy`, including
`make deploy source=feature-branch target=staging` and the sentence "The CI/CD pipeline triggers
automatically when the target branch is pushed."
**No version tags exist in these repos** — thinktech-php's only tags are `archived/*`, written by
`thinktech-web/Makefile:102-110 git-archive`; thinktech-api has none.

**Model B — tag `v<version>` from `master`, a workflow builds the artifact.** thinktech-app. `git branch
-r` shows **no** `development` and **no** `production`; `git tag` shows `v1.0.34, v1.0.33, v1.0.32, …`.
`thinktech-app/Makefile:86-94`:

    deploy: ## Create and push git tag (usage: make deploy [version=1.0.22])
    	@ver=$${version:-$$(grep 'versionName' androidApp/build.gradle.kts | head -1 | sed 's/.*"\(.*\)".*/\1/')}; \
    	tag="v$$ver"; \
    	echo "Releasing $$tag..."; \
    	git tag -d $$tag 2>/dev/null || true; \
    	git push origin --delete $$tag 2>/dev/null || true; \
    	git tag $$tag; \
    	git push origin $$tag; \

— note it **force-recreates** the tag (delete local, delete remote, re-tag, push) and defaults the version
by reading it back out of the gradle file. The tag is what fires the release:
`thinktech-app/.github/workflows/release-android.yml:15-19` `on: push: tags: ['v*']` plus a
`workflow_dispatch` with a `version` input; `release-ios.yml` the same
(`thinktech-app/.github/workflows/README.md:12-13`: "`release-android.yml` | version tag `v*`, manual |
Android APK/AAB + GitHub release").

**The rule behind the two.** The substrate decides: a service that Jenkins deploys from a branch uses
Model A; a **distributed artifact** — an app store submission, a downloadable build — uses Model B, because
there is no branch for a deployer to watch. Brigade ships a binary and a plugin from a GitHub release,
so **thinktech-app is Brigade's precedent, not thinktech-web**. §3 works through what Model A would cost.

**One shared detail worth keeping.** In both models the target is named `deploy`, not `release`, and it is
the *only* command a human runs. Brigade calls it `release` (`Makefile:357-360`). That is a naming
divergence, not a behavioural one.

### 1.9 Versioning — REFINED: `version-get` / `version-set version=…`, a set and not a bump

**`thinktech-app/Makefile:238-250`, in full:**

    version-get: ## Show current app version
    	@echo "Version: $$(grep 'versionName' androidApp/build.gradle.kts | head -1 | sed 's/.*"\(.*\)".*/\1/') ($$(grep 'versionCode' androidApp/build.gradle.kts | head -1 | sed 's/[^0-9]*//g'))"

    version-set: ## Set app version (usage: make version-set version=1.0.22)
    	@if [ -z "$(version)" ]; then echo "Usage: make version-set version=1.0.22"; exit 1; fi
    	@code=$$(echo "$(version)" | awk -F. '{print $$NF}'); \
    	sed -i '' "s/versionCode = [0-9]*/versionCode = $$code/" androidApp/build.gradle.kts; \
    	sed -i '' "s/versionName = \"[^\"]*\"/versionName = \"$(version)\"/" androidApp/build.gradle.kts; \
    	sed -i '' "s/CURRENT_PROJECT_VERSION = [0-9]*;/CURRENT_PROJECT_VERSION = $$code;/g" iosApp/iosApp.xcodeproj/project.pbxproj; \
    	sed -i '' "s/MARKETING_VERSION = [^;]*;/MARKETING_VERSION = $(version);/g" iosApp/iosApp.xcodeproj/project.pbxproj
    	@make version-get

Five properties worth naming, because they are the convention:

1. **Set, not bump.** No auto-increment anywhere; the caller supplies the whole `MAJOR.MINOR.PATCH`.
2. **A guard clause with the usage string as the error** (`:244`), matching the `##` doc verbatim.
3. **One command writes every place the version lives** — two files, four fields — so the platforms cannot
   drift; the build code is *derived* (`awk -F. '{print $NF}'`, the last dotted field), never given.
4. **It echoes the result** by re-invoking `version-get` (`:250` `@make version-get` — a recursive `make`
   without `$(MAKE)`; the house writes it that way, `thinktech-app/Makefile:127` does the same).
5. **Documented in the README as a pair**, with sample output:
   `thinktech-app/README.md:36-49` (`## Version Management` → `make version-get` → `# Output: Version:
   1.0.21 (21)` → `make version-set version=1.0.22`), and both rows in the command table at `:32-33`.

**Brigade today.** `Makefile:340-342` `print-version: ## Print the version pinned in plugin/bin/VERSION` —
the `version-get` half, under a different name. There is **no `version-set`**: the write is buried at
`scripts/release-prep.sh:109-113` (a `sed -i.bak -E` on `plugin/.claude-plugin/plugin.json`'s `"version"`,
verified by reading it back, then `printf '%s\n' "$v" > plugin/bin/VERSION`), reachable only by running the
whole release. **Gap.** The house shape would be `version-get` + `version-set version=X.Y.Z` writing both
`plugin/bin/VERSION` and `plugin/.claude-plugin/plugin.json`, with `release-prep.sh` *calling* it rather
than duplicating the sed — which also removes one of the two places the manifest's `"version"` line format
is depended upon (`checksums-check.sh` rule (a) and `release.yml:23` are the others).

### 1.10 Conventions the repositories show that the statement does not mention

Recurring in three or four repos each; P6-3 should treat them as house.

| Convention | Citations | Brigade |
| --- | --- | --- |
| **The git target chain** `pull` → `commit-only`/`commit` → `push`, gates as *prerequisites*, `git add --verbose :/ .`, `git commit -m "$(message)"`, `git push --verbose` | `thinktech-web/Makefile:89-100`; `thinktech-app/Makefile:177-185`; `thinktech-api/Makefile:32-38`; `thinktech-php/Makefile:24-29` | `Makefile:457-467` — **match**, with `--no-edit` on the pull and no `commit-only` split |
| **Gate order is load-bearing and documented** — cheapest first, pull *before* the expensive gates so they run on the merged tree | `thinktech-web/Makefile:94` + `thinktech-web/CLAUDE.md:208` | `Makefile:461` `commit: typecheck pull build test` — **match** |
| **Lowercase, `:=`, alphabetical variables**; `?=` only where an environment default is wanted | `thinktech-web/Makefile:6-30`; `thinktech-api/Makefile:3-15` (incl. `password ?= $(CONF_PROD_PASSWORD)`) | `Makefile:4-64`, banner says "(alphabetical)" — **match** |
| **`$(eval current_branch := $(shell …))`** to save and restore the branch | all three server Makefiles' `deploy` | n/a (no branch dance) |
| **A long dated/ticketed comment above a recipe carrying the *reason*** | `thinktech-web/Makefile:22-30` (ticket 1773 + the 2026-08-28 apply), `:148-152`, `:173-176`, `:183-186`, `:193-195` | pervasive (`Makefile:7-19`, `:242-245`, `:400-408`) — **match, and Brigade is denser** |
| **Fail-soft with an echoed `WARNING:`** where a red build would be worse than a stale cache | `thinktech-web/Makefile:153-154`, `:158-163` | none needed today |
| **`## Jenkins-invoked:` prefix** marking a target whose caller is CI, not a human | `thinktech-web/Makefile:139,143,147,157` | absent — Brigade's CI-called targets are unmarked. Cheap to add; makes "who calls this" answerable from `make help` |
| **A `# Default target` comment above `.DEFAULT_GOAL`** | `thinktech-web/Makefile:275`, `thinktech-app/Makefile:252` | `Makefile:487` — **match** |
| **`#!/usr/bin/env node|bash|python3` + a comment block naming the contract and its plan section** | `thinktech-web/scripts/verify-layout.mjs:1-3`, `deploy/aws/explore-create.sh:1-20` | `scripts/*.sh` use `#!/bin/sh` with the same header shape — **match** (POSIX `sh` is Brigade's own constraint, see §3) |

---

## 2. Brigade inventory — every target, every script, and who calls it

Evidence for P6-3's two rules ("every target must need to exist", "scripts inlined as targets"). Counts as
of this commit: **55 targets** (54 listed by `make help` + `e2e`, hidden by the scrape defect of §1.3),
**14 files under `scripts/` and `scripts/ci/`** excluding `testdata/`, `injection-corpus/` data and
`experiments/`.

### 2.1 Targets

Legend for **Called by**: *CI* = a step in `.github/workflows/ci.yml`; *dep* = a prerequisite of another
target; *human* = documented for a person (README/CLAUDE.md/plan); *script* = invoked from a script.

| Group | Target (`Makefile:` line) | `##` doc (abridged) | Called by |
| --- | --- | --- | --- |
| Help | `help:69` | Show this help message | `.DEFAULT_GOAL:488` |
| Setup | `setup:80` | go mod download, lint+goreleaser into ./bin, dev tools, .env, docker check | human |
| | `setup-lint:92` | Install only the pinned golangci-lint (what CI runs) | **CI:32**, dep of `setup` |
| | `setup-goreleaser:97` | Download the pinned goreleaser (release rehearsal only) | human; required by `release-prep.sh:86` |
| Build/Test/Lint | `version-check:107` | Fail when the version stamp would be empty | dep of `build`, `cross` |
| | `build:115` | bin/brigade + dev binaries, release flags | **CI:35,58,97**, dep of 6 targets |
| | `clean:122` | Remove build artifacts and local caches | human |
| | `typecheck:126` | `go build ./...` + `go vet ./...` | **CI:33**, dep of `commit` |
| | `fmt:131` | gofmt + goimports through golangci-lint | dep of `lint-fix`, human |
| | `lint:163` | golangci-lint for darwin AND linux + gofmt check | **CI:34** |
| | `lint-fix:178` | golangci-lint --fix and fmt | human |
| | `test:183` | unit + testscript + harness + conformance(fs), -race, no Docker | **CI:35**, dep of `commit`, `test-all` |
| | `vuln:188` | govulncheck on tree and binary | **CI:37** |
| | `tidy-check:193` | Fail if go.mod/go.sum would change | **CI:33** |
| | `deps-check:210` | bin/brigade links exactly docs/allowed-deps.txt | **CI:37** |
| | `schema:219` | Regenerate docs/protocol-v1.schema.json | human (CLAUDE.md: `make schema`) |
| | `schema-check:228` | Fail if the schema is stale | **CI:37** |
| | `test-db:239` | pgTAP against the running stack | **CI:99**, dep of `test-all` |
| | `test-integration:247` | integration + conformance(supabase) | **CI:118**, dep of `test-all` |
| | `test-all:252` | everything | human (CLAUDE.md) |
| | `conformance:255` | run the suite against any adapter | human — **the adapter-author entry point** (`README.md:38-43`) |
| | `e2e:259` | Phase 4 no-LLM proof (runs in CI) | **CI:126**, dep of `proof`, `test-all` — **not in `make help`** |
| | `proof:263` | + headless LLM run + idle-wake run | human |
| | `harness-smoke:267` | headless `claude -p` smoke with the fs adapter | human |
| | `advisor-lints:271` | Advisor lint mirrors via the container's psql | **CI:100**, dep of `test-all` |
| Plugin | `plugin-check:277` | plugin/ static checks + secret scan | **CI:50** |
| | `plugin-validate:282` | `claude plugin validate` | human (needs the `claude` CLI) |
| | `plugin-dev-pointer:287` | write the dev-binary pointer | dep of `plugin-dev`; **script** (harness-smoke/proof) |
| | `plugin-dev:310` | start Claude Code with the local plugin | human (CLAUDE.md) |
| | `plugin-dev-off:335` | remove the pointer | human (CLAUDE.md) |
| Release | `print-version:341` | print plugin/bin/VERSION | human |
| | `cross:345` | build all four targets + checksums.txt | **CI:59**, dep of `checksums-check`; `release-prep.sh:117` |
| | `checksums-check:354` | pins vs a fresh build or the published release | **CI:41** |
| | `release:358` | pin, commit, tag, push the tag | human |
| | `release-dry-run:363` | goreleaser check + local release | human (D1 rehearsal) |
| Supabase (local) | `migrations-check:374` | guard `supabase-start` | dep of `supabase-start`, `supabase-reset` |
| | `supabase-start:386` | start the minimal stack | **CI:98**, human |
| | `supabase-stop:390` | stop, keep data | human |
| | `supabase-clean:394` | stop and delete data | **CI:132**, human |
| | `supabase-status:398` | URLs and keys | human |
| | `supabase-env:410` | write .env.test in both name sets | **CI:100**, human |
| | `supabase-reset:425` | recreate from migrations + seed | human |
| | `migration-new:429` | create a timestamped migration | human |
| Supabase (hosted) | `supabase-link:435`, `supabase-push-dry:439`, `supabase-push:443`, `supabase-config-push:447` | link / dry-run / apply / push config | standalone since P5-1 (`backend-install` inlines link → dry-run → push → `scripts/backend-settings.sh` → `migration list` → `api-keys`; `supabase-config-push` refuses without `i_know=1`); human; `deploy-staging` uses the raw `supabase` CLI (`ci.yml:144-145`), **not** these |
| | `backend-install:451` | one-shot hosted setup for a team admin | human (Phase 5, `docs/setup.md`) |
| Git | `pull:457`, `commit:461`, `push:466` | plain merge; gate+commit; +push | human; `release-prep.sh:140` calls `make push` |
| Docker | `docker-build:472`, `docker-exec:476`, `docker-stop:480`, `docker-up:484` | `docker compose …` | **NOTHING — see below** |

> **The one unambiguous "does not need to exist".** The whole **Docker group** (`Makefile:469-486`, four
> targets) is dead. Brigade has **no `docker-compose.yml` and no `Dockerfile`** (`ls` confirms), and the
> three variables its recipes interpolate — `$(service)`, `$(container)`, `$(command)` — are **never
> defined in Brigade's Makefile** (only `detach := --detach` at `:20` survived the copy from
> `thinktech-web/Makefile:6-12`, where all four are defined and a compose file exists). `make docker-build`
> expands to `docker compose build` with an empty service and fails on the missing compose file. All four
> are listed in `make help`, so they are also four wrong answers to "what can I run here". Brigade's actual
> container use is the Supabase CLI's own stack (`supabase-start`) and one `docker exec` into
> `supabase_db_brigade` (`Makefile:272`). **P6-3: delete the group** (the Docker banner too), which takes
> Brigade from 55 targets to 51.
>
> Two softer cases, judgment not evidence: `plugin-dev-pointer:287` is a `plugin-dev` prerequisite that is
> separately public because scripts call it (its `##` says so) — keep; `supabase-push-dry:439` is a
> one-line wrapper whose knowledge is one flag — keep only if `docs/setup.md` tells an admin to run it.

### 2.2 Scripts

**(a) inlineable** — small enough that the house ceiling (§1.1) allows it. **(b) too large or too
structured** — a multi-hundred-line POSIX-sh program, usually with a Go drift test joined against its
constants; inlining it is not something any house repo does. **(c) must stay a file** — it *ships*, or its
caller is not make.

| File | Lines | Called by | Class |
| --- | --- | --- | --- |
| `scripts/proof.sh` | 1694 | `make e2e` (`Makefile:260`) → CI (`ci.yml:126`); drift-tested by `scripts/ci/proof_test.go` (577) | **(b)** |
| `scripts/proof-headless.sh` | 1612 | `make proof` (`Makefile:264`); re-entered as `sh proof-headless.sh judge` by `proof-idle-wake.sh:1453`; drift-tested by `proof_headless_test.go` (625) | **(b)** — and it is a *subcommand host*, not just a script |
| `scripts/proof-idle-wake.sh` | 1837 | `make proof` (`Makefile:264`) | **(b)** (landing under P4-3 with its own Go drift test; not this task's) |
| `scripts/harness-smoke.sh` | 558 | `make harness-smoke` (`Makefile:268`) only | **(b)** |
| `scripts/release-prep.sh` | 146 | `make release` (`Makefile:360`); tested by `scripts/ci/release_prep_test.go` (482) | **(b)** — 5 numbered steps, a DRY_RUN mode, 8 precondition guards |
| `scripts/ci/plugin-check.sh` | 205 | `make plugin-check` (`Makefile:278`) → CI (`ci.yml:50`); tested by `checks_test.go` (895) and `manifests_test.go` (1025) | **(b)** — 9 numbered checks, shellchecked by check 9 |
| `scripts/ci/no-secrets.sh` | 76 | `make plugin-check` (`Makefile:279`) → CI; tested by `checks_test.go` | **(b)** — the only secret scan (CLAUDE.md) |
| `scripts/ci/checksums-check.sh` | 80 | `make checksums-check` (`Makefile:355`) → CI (`ci.yml:41`); tested by `checks_test.go:653+` | **(b)** — three lettered rules + a pre-release branch |
| `scripts/ci/advisor-lints.sql` | 147 | `make advisor-lints` (`Makefile:272`) → CI (`ci.yml:100`) | **(c)** — SQL fed to `psql` on stdin; not shell at all |
| `scripts/ci/release-verify.sh` | 35 | **`release.yml:33` only — no make target** | **(a)/(c)** — small enough to inline, but its caller is a workflow. See below |
| `scripts/ci/keepalive.sh` | 142 | **`keepalive.yml:47` only — no make target**; tested by `keepalive_test.go` (1041, incl. a shellcheck case at `:754`) | **(c)** — see below |
| `scripts/ci/bootstrap-alpine.sh` | 148 | **nothing.** Named only in `internal/harness/bootstrap/doc.go:15` and `bootstrap_test.go:802` as "run locally under docker" | **(c)-orphan** — a documented manual procedure with no runner |
| `scripts/ci/conformance-setup-supabase.sh` | 85 | **nothing.** `Makefile:244` and `ci.yml:107` are comments explaining it is *no longer* used: `--setup` was dropped so the suite provisions its own teams and runs C-28/C-40 instead of skipping them | **(c)-orphan** — kept deliberately as the documented out-of-band alternative for adapter authors |
| `plugin/bin/brigade` | 125 | **SHIPS.** The POSIX-sh download-and-verify bootstrap; `plugin/hooks/hooks.json` execs it, `plugin-check.sh` checks 1/2/8/9 pin its path, its git mode 100755 and its shellcheck cleanliness | **(c)** — inlining it is not even expressible |
| `scripts/ci/*_test.go` (6 files, 4,645 lines) | | `make test` → CI (`ci.yml:35`) | Go tests; the drift joins that make (b) safe |
| `scripts/injection-corpus/` (26 `.txt` + `expected.json`) | | data, read by `proof-headless.sh:1211-1213` **and** `internal/corpus/corpus_test.go:23`, `internal/protocol/fuzz_test.go:16`, `internal/harness/frame/frame_test.go:270` | data, not script |
| `scripts/experiments/**` (E0-1..E0-9, E3-interactive, sitting) | ~15k | run by hand, reported in `docs/experiments/*.md`; `Makefile:400-408` documents that `supabase-env` writes both name sets *because* E0-1/E0-2/E0-6 read the CLI's own names | evidence, not build tooling |

**The `keepalive.sh` question the task asks explicitly.** Would the convention make `keepalive.yml:47` read
`run: make keepalive`? **The house says no, and so should Brigade.** `thinktech-php` is the closest
precedent and it does the opposite: `send-weekly-metrics.yml:34` runs `python3
.github/scripts/weekly-cluster-metrics.py` with no make target, and the script is documented in
`.github/scripts/README.md` instead. The convention's actual force ("workflows call make") lands on the
*deploy pipeline*, where a human must be able to reproduce what CI did. `keepalive.sh` is the opposite
case: it is a scheduled probe against the **hosted** project, needing two repository variables a developer
does not have (`keepalive.yml:42-44`), and a `make keepalive` target would be a target no human can
usefully run — failing rule 3. The same reasoning covers `release-verify.sh` (it consumes `dist/` produced
by goreleaser *inside the release job*; `release.yml:33`). **Recommendation for P6-3: leave both as
`run: <script>`, and pay the convention its due by adding `scripts/ci/README.md` in the
`thinktech-php/.github/scripts/README.md` shape** — one section per script: what invokes it, its
dependencies, and a `| Type | Name | Notes |` table of the variables and secrets it needs. That also gives
the two orphans (`bootstrap-alpine.sh`, `conformance-setup-supabase.sh`) a documented reason to exist,
which is the only thing currently protecting them from a future "delete what nothing calls" sweep.

---

## 3. Collisions with the constraints the log names

Per convention. "Collides" means: applying the convention as stated would weaken or delete a guard the
execution log lists as load-bearing and measured.

| Convention | Collides? | What the collision is |
| --- | --- | --- |
| 1. Scripts inlined as targets | **Yes, on the literal reading** | The four (b)-class scripts are 1,600–1,850 lines each and are **drift-joined to Go tests** (`proof_test.go:1` "The drift join for scripts/proof.sh"; `keepalive_test.go:43` pins `scripts/ci/keepalive.sh` by path; `release_prep_test.go:216` and `checks_test.go:202` *execute the real file from the repository*). A recipe body is not a file those tests can open, so inlining deletes the drift joins. Also `plugin-check.sh` check 9 and `keepalive_test.go:754` shellcheck **files**; a recipe cannot be shellchecked, and CLAUDE.md requires both the 0.10 (docker) and 0.11 (local) runs. **No change; record the refined reading of §1.1.** |
| 2. Group banners | No | Already adopted. |
| 3. Every target must need to exist | **No — it helps** | Its only bite is the dead Docker group. One caution: `version-check:107`, `migrations-check:374` and `schema-check:228` look like ceremony but each *is* a guard (E0-1 is cited on `migrations-check`), so "excessive" must not be read as "delete the small ones". |
| 4. Workflows call make | **Yes, in three places** | (a) `ci.yml:58`'s macOS `go test -race … ./...` is deliberately **not** `make test` — `make test` also runs conformance(fs) and builds; converting it changes what the primary platform proves. (b) `ci.yml:41,50` already call make but carry `env: {GH_TOKEN: …}` and ordering that the *workflow* owns, not the Makefile. (c) `release.yml:24` runs `make cross && diff dist-cross/checksums.txt plugin/bin/checksums.txt` inline — the **reproducibility guard**; wrapping that diff in a target would give a future edit a way to loosen it in one place instead of two. Leave all three. |
| 5. README TOC | No | Pure addition. |
| 6. Details in `.docs/` | **Yes, if the dot is taken literally** | `docs/adapter-authors.md` and `docs/protocol-v1.md` are the *published contract* (`README.md:27-34`, and `docs/protocol-v1.md` is frozen per CLAUDE.md). Moving them under a dotted, conventionally-hidden directory would hide the deliverable from the people the repository exists to serve, and would break every citation in the plan, the log, `docs/experiments/*` and the conformance suite. Keep `docs/`. |
| 7. Release by merging `master` → a branch | **Yes, structurally — the whole release chain** | Detailed below. |
| 8. `version-set` target | No | Pure addition; it would *reduce* duplication of the manifest's `"version"` sed (three copies today: `release-prep.sh:109`, `checksums-check.sh` rule (a), `release.yml:23`). |
| (house) commit chain, `## Jenkins-invoked:`, workflows README, composite action, per-job `timeout-minutes`, aggregator job | No | All additive. The aggregator interacts with branch protection, which is Rjae's call, not P6-3's. |

Two constraints are **untouched by every convention above**, and should be stated so P6-5 can record them
as such: the `plugin/` file allowlist and modes (`plugin-check.sh:31-40`; no convention here has any
opinion about `plugin/`), and the by-prefix `CLAUDE*` strip (`Makefile:62-64` — `unclaude_vars` greps
`env` for `^CLAUDE[0-9A-Za-z_]*` and `^AI_AGENT[0-9A-Za-z_]*`, minus `CLAUDE_CONFIG_DIR`, and prefixes
`env -u …` onto `proof.sh`, `proof-headless.sh`, `proof-idle-wake.sh`, `harness-smoke.sh` and both
`plugin-dev` recipes). Note in passing that thinktech-app has a `claude-run` target
(`thinktech-app/Makefile:234-236`) that launches `claude --model opus --dangerously-skip-permissions` with
**no** environment strip; Brigade must not adopt that target, and this is the one place the house style and
a measured Brigade guard point in opposite directions on the same line of code.

### 3.1 What "merge `master` to `production`" would mean for Brigade's release chain — analysis for P6-2/P5-10, not a decision

**The chain today**, end to end:

1. `make release version=X.Y.Z` (`Makefile:357-360`) → `scripts/release-prep.sh X.Y.Z`.
2. The script: preconditions (clean tree via `git status --porcelain --untracked-files=normal`; on `master`
   unless `branch=` is passed; `bin/goreleaser` present; `GOTOOLCHAIN=go<go.mod line>` selectable —
   `:63-93`) → **step 1** writes `plugin/.claude-plugin/plugin.json` and `plugin/bin/VERSION` (`:109-113`)
   → **step 2** `make cross version=$v` (`:117`) → **step 3** goreleaser builds the same four targets and
   `diff dist/checksums.txt dist-cross/checksums.txt` must be empty, "which is what stops
   .goreleaser.yaml and the Makefile drifting apart" (`:121-128`) → **step 4** `cp dist/checksums.txt
   plugin/bin/checksums.txt` and `make push message="15: Release $v"` (`:139-140`) → **step 5**
   `git tag -a v$v && git push origin v$v` (`:144-145`).
3. `release.yml` fires `on: push: tags: ["v*"]` (`:7-9`), re-asserts `v$(cat plugin/bin/VERSION)` equals
   the tag **and** that `plugin.json`'s version equals `VERSION` **and** that `make cross` reproduces the
   committed `plugin/bin/checksums.txt` byte for byte (`:20-24`), runs goreleaser pinned to `v2.18.0`
   (`:25-31`), verifies goreleaser's own `dist/checksums.txt` against the committed file
   (`scripts/ci/release-verify.sh`, `:32-33`), then publishes the draft (`:34-45`) or deletes it (`:46-48`).
4. Every subsequent commit re-checks the pins: `ci.yml:41` `make checksums-check` →
   `checksums-check.sh` rule (c) — the fresh cross-build equals the committed file **or** the published
   release `v<VERSION>` carries a `checksums.txt` equal to it.
5. `plugin/bin/brigade` (the shipped bootstrap) downloads the release asset and verifies it against
   `plugin/bin/checksums.txt` **from the commit the user installed** — which is why the file must be *in*
   the tagged commit, and why the whole scheme rests on reproducibility (`release-prep.sh:5-9`).

**What Model A would require.** "Merge `master` to `production`" means the *push of the branch* is the
release trigger, replacing the tag. Concretely:

- **Who creates the tag.** Nobody, or a workflow. `release.yml`'s `on: push: tags` would become
  `on: push: branches: [production]`, and `GITHUB_REF_NAME` — which the workflow uses **four times** as the
  version (`:22` the pin guard, `:42,43` `gh release edit`, `:48` the draft delete) — would become
  `production`, not `v0.1.0`. Every one of those four uses needs a version from somewhere else, i.e. from
  `plugin/bin/VERSION`. A GitHub release still needs a tag object, so the workflow would have to *create*
  `v$(cat plugin/bin/VERSION)` itself, with `contents: write`. That moves tag creation from a human on a
  clean tree to a job on a branch — and if `production` is ever pushed twice at the same VERSION, the tag
  already exists and the job must decide between failing and force-moving it. thinktech-app's `deploy`
  chooses force (`thinktech-app/Makefile:91-92` deletes local and remote before re-tagging), which
  `release-prep.sh:23-27` explicitly forbids for Brigade: "A PUBLISHED tag is never rewritten: bump the
  patch version instead." **That is the sharpest single conflict.**
- **Where the version lives.** Unchanged and *more* load-bearing: `plugin/bin/VERSION` +
  `plugin/.claude-plugin/plugin.json`, since the ref no longer carries it. This is where the house
  `version-set` target (§1.9) stops being cosmetic — bumping the pins becomes a separate, human, reviewable
  act on `master` (`make version-set version=X.Y.Z`, commit, then merge to `production`), which is
  precisely the thinktech-app split (`version-set` then `deploy`) with the substrate swapped.
- **How the checksums stay verifiable.** `plugin/bin/checksums.txt` must still be committed **before** the
  release is built, and it is produced by `make cross`/goreleaser at the pinned toolchain. Nothing about
  the branch model changes reproducibility — the guard at `release.yml:24` (`make cross && diff …`) works
  identically on a branch push. What changes is *when the developer runs steps 2–4*: today they run on
  `master` and the tag then points at that commit; under Model A they would run on `master`, commit, and
  then the merge to `production` must be a **fast-forward or a plain merge that changes no file**, or the
  merge commit's tree could differ from the one whose checksums were computed. `checksums-check.sh` rule
  (c) would still catch that (it compares a fresh cross-build against the committed file), so the failure
  mode is a red CI on `production` rather than a bad release — acceptable, but it is a new way to be red.
- **What `merges only, never rebase` implies.** A merge into `production` produces a merge commit whose
  tree equals `master`'s if nothing else ever lands on `production`. If anything *ever* lands on
  `production` directly, the two histories diverge and every later merge risks a tree that reproduces
  nothing. Model A therefore needs the rule "`production` is only ever written by a merge from `master`"
  written down and, ideally, branch-protected.
- **What it buys.** A named, inspectable pointer to what is released (`git log origin/production`), and a
  release trigger that survives a mistyped tag. What it costs, beyond the above: `make e2e`/`ci.yml` gain a
  branch to think about, `keepalive.yml`'s "scheduled runs fire only on the default branch" note (`:13-14`)
  is unaffected, and `checksums-check.sh` rule (c)'s `gh release download v$VERSION` still needs a tag
  named `v<VERSION>` to exist — so the tag does not actually go away under Model A, it only changes author.

**The reading of the evidence** (offered as analysis, per the task): Brigade is a *distributed artifact*,
not a Jenkins-deployed service, and the house has a model for exactly that — thinktech-app's, which is
already what Brigade does, down to `on: push: tags: ['v*']`. The convention Rjae stated is real and is her
practice for every repository that has a deployment branch; Brigade has none, has no Jenkins, and its
"deploy" is a GitHub release asset that a bootstrap verifies by hash. The two house conventions Brigade is
genuinely *missing* from the thinktech-app release story are the `version-get`/`version-set` pair (§1.9)
and the `deploy`-vs-`release` target name — not the branch model. P6-2/P5-10 should decide explicitly, and
if Model A is chosen, the tag-force question (`release-prep.sh:23-27` vs
`thinktech-app/Makefile:91-92`) has to be settled first, because the two houses answer it differently.

---

## 4. Open questions for Rjae — only those the repositories cannot answer

1. **Release model.** Brigade has no deployment branch and ships a hash-verified artifact; thinktech-app —
   the one repository in the same shape — releases from a `v*` tag, not a branch merge. Does "releasing is
   merging `master` to a target branch" extend to Brigade, or does it mean "for anything Jenkins deploys"?
   If it extends: **may a release tag ever be force-moved?** thinktech-app's `deploy` deletes and re-pushes
   the tag; `release-prep.sh:23-27` forbids it for Brigade because a published `checksums.txt` is a
   compatibility surface. These cannot both hold.
2. **`.docs/` vs `docs/`.** `docs/protocol-v1.md` and `docs/adapter-authors.md` are the published contract
   third-party adapter authors are pointed at. Confirm they stay in a visible `docs/` — and if the *dot* is
   what matters to you, say whether it should apply to the internal-only material (`docs/experiments/`,
   `docs/research/`) while the contract stays visible.
3. **TOC form.** `thinktech-api/README.md:7-23`'s "You want to… / Go to" task table with
   `# [Heading](#table-of-contents)` back-links, or `thinktech-php/README.md:5-52`'s nested anchor list?
   Brigade's README is 79 lines today; the api form fits, the php form would dwarf the content.
4. **Group-name suffix.** thinktech-web uses `# ========== Setup ==========`, thinktech-app
   `# ========== Setup Commands ==========`. Brigade follows the web form. Is the bare noun the current
   house style, or is web the outlier?
5. **`version-set`.** Should Brigade grow `make version-set version=X.Y.Z` writing both
   `plugin/bin/VERSION` and `plugin/.claude-plugin/plugin.json` (with `release-prep.sh` calling it), and
   should the release entry point be renamed `deploy` to match all four thinktech repositories — or does
   `release` read better for a repository whose output is a release?
6. **Branch protection / an aggregator job.** `thinktech-web/ci.yml:260-283` exists because branch
   protection names one required context. Brigade's CI has five jobs and no aggregator. Do you want the
   same shape here (which implies deciding what is *required* on `master`), or is that Phase 5's business?
7. **`make test-all` and the CI/human split.** thinktech-app's CI runs `make lint` and
   `release-android.yml` runs `make validate`, i.e. CI runs *the same named gate a developer runs*.
   Brigade's CI decomposes into `make typecheck tidy-check`, `make lint`, `make build test`, `make vuln
   deps-check schema-check` across four steps for step-level attribution in the run log. Do you want CI
   collapsed to one named gate target (a `make ci` in the thinktech-app style, `thinktech-app/Makefile:199-206`),
   accepting that a failure then names the target rather than the step?
