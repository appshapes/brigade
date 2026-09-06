# CI, proof and release scripts

Every script under `scripts/` and `scripts/ci/`, what invokes it, and what it needs to run. The shape is the
house's `thinktech-php/.github/scripts/README.md`: a workflow table, one section per script naming its caller
and its runtime dependencies, and a `| Type | Name | Notes |` table of the GitHub configuration the workflows read.

Two adaptations of that shape, both deliberate:

- **It lives beside the scripts themselves, rather than under `.github/`.** Brigade's CI scripts are not
  workflow-only: nine of the thirteen shell scripts are reached through `make`, only two are reached from a
  workflow without a target of their own, and the remaining two are reached by nothing at all. Moving them would
  break `scripts/ci/plugin-check.sh`'s shellcheck glob (`scripts/ci/*.sh scripts/*.sh`), the `make` recipes that
  name them by path, `ci.yml`'s steps, and the ten Go drift tests that open them by path.
- **It also carries the workflow table** that the house keeps in `thinktech-app/.github/workflows/README.md`,
  rather than a second index over there. Brigade has three workflows and fourteen scripts in a near-1:1
  relation, and two indexes of the same thing drift. `thinktech-php` keeps its own workflow table out of its
  own workflow directory for the same reason.

Callers are named by `make` target and by workflow job or step, never by line number: the Makefile and the
workflows renumber on every edit, and a `grep` for a target name or a step name is what actually proves the claim.

## Workflows

| Workflow | Trigger | Purpose |
| --- | --- | --- |
| `.github/workflows/ci.yml` | push to `master`, and every pull request | Five jobs: `fast` (lint, typecheck, unit + conformance(fs) tests, vuln, dependency and schema guards, `make checksums-check`, `make plugin-check`), `macos` (`go test` on the primary user platform plus `make cross`), `reproducibility` (the ubuntu and macOS cross-builds must be byte-identical), `supabase` (the local stack: pgTAP, advisor lints, the integration suite, conformance(supabase) and `make e2e`), and `deploy-staging` (skipped until `BRIGADE_STAGING` is set). |
| `.github/workflows/release.yml` | a pushed `v*` tag | Guards that `plugin/bin/VERSION` and `plugin/.claude-plugin/plugin.json` pin the tag and that `make cross` reproduces `plugin/bin/checksums.txt`, runs goreleaser into a draft release, verifies the built binaries against the committed checksums with `release-verify.sh`, then publishes the draft or discards it. |
| `.github/workflows/keepalive.yml` | daily cron (`37 10 * * *` UTC) and `workflow_dispatch` | Runs `keepalive.sh` so the hosted Supabase project answers a few database requests a day and the Free plan never pauses it. |

## Index

| Script | Invoked by |
| --- | --- |
| `scripts/proof.sh` | `make e2e` → `ci.yml`'s `supabase` job |
| `scripts/proof-headless.sh` | `make proof` (link 2 of 4) |
| `scripts/proof-idle-wake.sh` | `make proof` (link 3 of 4) |
| `scripts/proof-crash-resume.sh` | `make proof` (link 4 of 4) |
| `scripts/harness-smoke.sh` | `make harness-smoke` |
| `scripts/release-prep.sh` | `make release` |
| `scripts/ci/plugin-check.sh` | `make plugin-check` → `ci.yml`'s `fast` job |
| `scripts/ci/no-secrets.sh` | `make plugin-check` → `ci.yml`'s `fast` job |
| `scripts/ci/checksums-check.sh` | `make checksums-check` → `ci.yml`'s `fast` job |
| `scripts/ci/advisor-lints.sql` | `make advisor-lints` → `ci.yml`'s `supabase` job |
| `scripts/ci/release-verify.sh` | `release.yml` only — no `make` target |
| `scripts/ci/keepalive.sh` | `keepalive.yml` only — no `make` target |
| `scripts/ci/bootstrap-alpine.sh` | nothing — run by hand, see its section |
| `scripts/ci/conformance-setup-supabase.sh` | nothing — passed by an adapter author to `--setup`, see its section |

`release-verify.sh` and `keepalive.sh` have no `make` target on purpose, and the house does the same with its own
workflow-only scripts (`thinktech-php` runs `send-weekly-metrics.yml`, `phinx-ownership.yml` and
`permission-writers.yml`'s scripts directly and documents them in a scripts README). `keepalive.sh` needs two
repository variables a developer does not have; `release-verify.sh` consumes a `dist/` directory that goreleaser
produces inside the release job and that exists nowhere else. A target for either would be one no human could usefully run.

Not documented here: `scripts/experiments/` (the promoted Phase 0 regression kit — its reports are in
`docs/experiments/` and its drivers are described there) and `scripts/injection-corpus/` (the P0-1 corpus that
`proof-headless.sh` reads).

## Required GitHub configuration

Names only. No value of any of these appears in this repository, and none may be added.

| Type | Name | Notes |
| --- | --- | --- |
| Var | `BRIGADE_SUPABASE_URL` | The hosted project's url, read by `keepalive.yml`. Public by design: it is one of the two values every member of a team receives. |
| Var | `BRIGADE_SUPABASE_PUBLISHABLE_KEY` | The hosted project's publishable key, read by `keepalive.yml`. Public by design, for the same reason. |
| Var | `BRIGADE_STAGING` | Read by `ci.yml`. The `deploy-staging` job runs only when it is `true` on a push to `master`; otherwise the job is skipped. |
| Secret | `SUPABASE_ACCESS_TOKEN` | `ci.yml`'s `deploy-staging` job: the Supabase CLI's personal access token. |
| Secret | `SUPABASE_DB_PASSWORD` | `ci.yml`'s `deploy-staging` job: the staging project's database password. |
| Secret | `SUPABASE_PROJECT_ID` | `ci.yml`'s `deploy-staging` job: the staging project's ref, passed to `supabase link`. |
| Token | `GITHUB_TOKEN` / `GH_TOKEN` | The job token GitHub mints per run, never a stored secret. `ci.yml` passes it to `make checksums-check` for rule (c)'s release fallback; `release.yml` passes it to goreleaser and to the two `gh release edit`/`delete` steps. |

**The Supabase secret key, the service-role key and the Supabase personal access token are never a variable or a
secret of `ci.yml`, `release.yml` or `keepalive.yml`, and never reach the adapter, the plugin or this
repository.** `SUPABASE_ACCESS_TOKEN` above is scoped to the staging deploy job of a workflow that is skipped
until an administrator opts a staging project in. `scripts/ci/no-secrets.sh` fails the build on a JWT-shaped
string or an `sb_secret_` key anywhere in the tracked tree, in `plugin/`, or in a built binary.

`BRIGADE_SUPABASE_URL` and `BRIGADE_SUPABASE_PUBLISHABLE_KEY` were set on this repository on 2026-09-05, and
`keepalive.yml`'s first *green* run against the hosted project is run `33942302844`. It was not the first run:
the three before it that day reached the project and were red — two on `anonymous_provider_disabled` (HTTP 422)
until the owner enabled anonymous sign-ins, and one on the unexposed `brigade` schema's `406 PGRST106` until
rung 3 was taught to warn rather than fail. Where the two variables are unset — any fork, and this repository
before 2026-09-05 — every run is a green no-op that says so on the run page. `docs/setup.md` carries the
administrator's procedure for setting them.

## `scripts/proof.sh`

The Phase 4 vertical proof (P4-1): no LLM and no Claude Code process, but the real bundled Supabase adapter, the
real `brigade hook session-start`, the real `brigade send` and the real detached watcher in `--sink` mode,
against the local Supabase stack.

Invoked by `make e2e`, which builds first and wraps the run in the Makefile's `$(unclaude)` environment strip.
`ci.yml`'s `supabase` job runs `make e2e` with `BRIGADE_COVER=1` and `GOCOVERDIR` set, so the run contributes
coverage across the process boundary.

Needs: a running local stack (`make supabase-start supabase-env`), plus `jq`, `perl`, `pgrep` and either
`sha256sum` or `shasum`. Its constants block is joined to the Go and SQL sources by `proof_test.go`.

## `scripts/proof-headless.sh`

The headless half of the Phase 4 proof (P4-2): two real `claude -p` sessions against the local stack through the
bundled adapter, then the P0-1 injection corpus posted into fresh receiving sessions. **It spends real model
tokens.** `scripts/proof-headless.sh judge <evidence-dir>` re-scores saved artefacts without any model call, and
that subcommand is what `proof_headless_test.go` exercises in `make test`.

Invoked by `make proof` (link 2 of 4). Never by CI. Needs a logged-in `claude`, a running local stack, `jq`,
`perl`, `pgrep` and `sha256sum` or `shasum`.

## `scripts/proof-idle-wake.sh`

The idle half of the Phase 4 proof (P4-3): real `claude -p` sessions that sit idle with stdin held open and are
woken through the shipped path — `brigade send` → the bundled Supabase adapter → the local stack → the
receiver's own detached `brigade watch` → socketpost → Claude Code's inbox socket. **It spends real model
tokens.** `scripts/proof-idle-wake.sh wake <evidence-dir>` re-scores saved artefacts with no model call, and is
what `proof_idle_wake_test.go` exercises.

Invoked by `make proof` (link 3 of 4). Never by CI. Same dependencies as `proof-headless.sh`, plus `mkfifo`.

## `scripts/proof-crash-resume.sh`

The crash half of the Phase 4 proof (P4-4): a real `claude -p` receiver is SIGKILLed, five messages are sent
while it is down, and `claude -p --resume <native id>` re-attaches to the same Brigade session and catches up on
all five exactly once. **It spends real model tokens.** `scripts/proof-crash-resume.sh catchup <evidence-dir>`
re-scores saved artefacts with no model call, and is what `proof_crash_resume_test.go` exercises.

Invoked by `make proof` (link 4 of 4). Never by CI. Same dependencies as `proof-idle-wake.sh`.

## `scripts/harness-smoke.sh`

The headless harness smoke of P3-7: one real `claude -p` session with the real plugin, the real hooks, the real
detached watcher and the fs adapter as the backend, driven from a second principal in this script's own terminal
environment. It proves the wiring no unit or e2e test can — that Claude Code fires the plugin's hooks and that
the model reaches `brigade` on the Bash tool.

Invoked by `make harness-smoke`, which builds first and wraps the run in `$(unclaude)`. Never by CI. Needs a
logged-in `claude`, `jq` and a built `bin/brigade`.

## `scripts/release-prep.sh`

The release sequence of plan 7.7, in five steps: pin `plugin/bin/VERSION` and `plugin/.claude-plugin/plugin.json`
to the version, rebuild and re-checksum the cross-compiled binaries, commit through the push chain, then tag
`v<version>` and push the tag — which is what fires `release.yml`.

Invoked by `make release version=X.Y.Z [branch=<name>]`. It refuses to run on a dirty tree, refuses any branch
but `master` unless `branch=` is passed, and refuses to rewrite a published tag, because `plugin/bin/checksums.txt`
is a compatibility surface for every plugin installed from that commit.

Needs `git`, the Go toolchain named by `go.mod`, and `bin/goreleaser` (`make setup-goreleaser`, which needs `gh`).

`DRY_RUN=1` is the rehearsal form, and it is **not** side-effect free: steps 1-3 run for real — step 1 rewrites
`plugin/bin/VERSION` and `plugin/.claude-plugin/plugin.json` — and only steps 4 and 5 are printed instead of run.
Rehearse on a throwaway branch and restore `plugin/` afterwards:

```sh
git switch -c rehearsal/local
DRY_RUN=1 scripts/release-prep.sh 0.0.1-rc2 rehearsal/local
git checkout -- plugin/ && git switch master && git branch -D rehearsal/local
make checksums-check          # must be green again, with VERSION back at 0.0.0
```

## `scripts/ci/plugin-check.sh`

The static checks of the shipped plugin tree: the `plugin/` file allowlist, mode `100755` in git for
`plugin/bin/brigade` and `100644` for everything else, `plugin/bin/VERSION` == `plugin/.claude-plugin/plugin.json`'s `"version"`, no `.mcp.json`
and no `mcpServers`/`channels`, exec-form hooks whose command paths exist and are executable, `sh -n` on the
bootstrap under `sh`, `bash` and `zsh`, and `shellcheck -s sh` over `plugin/bin/brigade`, `scripts/ci/*.sh` and
`scripts/*.sh`. It exits on the first failure and prints one `ok:` line per check.

Invoked by `make plugin-check` (which then runs `no-secrets.sh`) → `ci.yml`'s `fast` job.

Needs `shellcheck` for check 9. Without it the check warns and skips locally, but **dies** when `CI` is set — the
Ubuntu runner preinstalls it, so a missing shellcheck there is a real failure. CI's runner (Blacksmith's
`blacksmith-4vcpu-ubuntu-2404` image since P5-17) has shellcheck 0.9.0 — printed by `ci.yml`'s `fast` job in its
"Runner image inventory" step on every run; re-pin this paragraph and CLAUDE.md's when it changes — and a current
macOS `brew` has 0.11, and the two disagree; before pushing a change to any shell file run both:

```sh
shellcheck -s sh <file>                                                    # 0.11, local
docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:v0.9.0 -s sh <file>
```

## `scripts/ci/no-secrets.sh`

The secret scan, and the only one in the tree. It fails, naming file and line, on a JWT-shaped triple or an
`sb_secret_`-shaped key anywhere in the tracked text, under `plugin/`, or inside a built binary, and
additionally on the service-role role NAME appearing in a `plugin/` text file (the role name is not a key, but
nothing hand-written and shipped to a user's machine has any business naming it). It prints the number of files it scanned,
so a run that read nothing is visible instead of vacuously green.

Invoked by `make plugin-check` → `ci.yml`'s `fast` job. No dependencies beyond POSIX `sh`, `git` and `grep`.

## `scripts/ci/checksums-check.sh`

Closes the release loop on every commit. It reads `plugin/bin/VERSION` and then checks that (a)
`plugin/.claude-plugin/plugin.json` pins the same version, (b) `plugin/bin/checksums.txt` matches a fresh
cross-compile, and (c) it also matches the published release's assets. In the pre-release state (`VERSION` =
`0.0.0`) it instead requires `plugin/bin/checksums.txt` to be **empty** and skips (b) and (c).

Invoked by `make checksums-check`, which depends on `make cross` and passes it the fresh
`dist-cross/checksums.txt` → `ci.yml`'s `fast` job, with the job's `GITHUB_TOKEN` in `GH_TOKEN`.

Needs the Go toolchain (through `make cross`) and, for rule (c) only, `gh`. In the pre-release state it never
reaches the `gh` call.

## `scripts/ci/advisor-lints.sql`

Local mirrors of ten Supabase Security Advisor lints. Every query collects findings into a temp table and a
final `DO` block raises — so `psql` exits non-zero — on any finding not on the documented expected list, and
also on any expected finding that has gone missing, so a lint that silently stopped detecting is caught too.

Invoked by `make advisor-lints`, which pipes it into `psql -v ON_ERROR_STOP=1` **inside** the
`supabase_db_brigade` container, so no host `psql` is needed → `ci.yml`'s `supabase` job.

Needs the local stack running (`make supabase-start`) and `docker`.

## `scripts/ci/release-verify.sh`

The release job's last gate: the binaries goreleaser has just built must be byte-identical to the ones
`plugin/bin/checksums.txt` pins, or the draft release is discarded. Only the hash columns are compared, sorted —
goreleaser and `make cross` agree on hashes but need not agree on line order, and the asset-name column is
checked by `checksums-check.sh` rule (b) against the version instead.

Invoked by `.github/workflows/release.yml` only, as
`scripts/ci/release-verify.sh dist/checksums.txt plugin/bin/checksums.txt`. There is no `make` target: the `dist/`
directory it reads exists only inside the release job. `checks_test.go` proves each of its refusals fires.

### Run locally

```sh
# only meaningful against a real goreleaser dist/, e.g. after `bin/goreleaser release --snapshot --clean`
scripts/ci/release-verify.sh dist/checksums.txt plugin/bin/checksums.txt
```

## `scripts/ci/keepalive.sh`

Keeps the hosted Supabase project out of the Free plan's inactivity pause (P5-0). It climbs four rungs and
prints one line for each: (0) configuration — both variables unset is a `::notice::` and exit 0, exactly one set
is exit 1; (1) reachability, `GET /auth/v1/health` with retries; (2) the database write, `POST /auth/v1/signup`,
an anonymous sign-up that inserts into `auth.users` and is the activity Supabase actually counts; (3) the Data
API, `POST /rest/v1/rpc/my_team_ids`, where PostgREST's own "not there yet" answers (`PGRST106`, `PGRST202`) are
a `::warning::` rather than a failure until P5-1 applies the migrations to the hosted project; (4) sign-out,
`POST /auth/v1/logout?scope=global`, a warning at worst.

Invoked by `.github/workflows/keepalive.yml` only. There is no `make` target: the two repository variables it
needs are not a developer's to have.

Needs `curl` and `jq`, both preinstalled on the Ubuntu runner, and the two variables above. The access token
rung 2 mints and the publishable key reach `curl` through `0600` header files (`-H @<file>`) and never on argv —
a runner's `ps` and the Actions debug log both see argv. No response body is ever printed.

### Run locally

```sh
BRIGADE_SUPABASE_URL="$(gh variable get BRIGADE_SUPABASE_URL)" \
BRIGADE_SUPABASE_PUBLISHABLE_KEY="$(gh variable get BRIGADE_SUPABASE_PUBLISHABLE_KEY)" \
scripts/ci/keepalive.sh
```

### Trigger the workflow manually

```sh
gh workflow run keepalive.yml
gh run list --workflow keepalive.yml --limit 1
```

## `scripts/ci/bootstrap-alpine.sh`

The third leg of the bootstrap test matrix (plan 6.2, P1-8): `plugin/bin/brigade` under busybox `ash`, with
busybox `wget` and busybox `sha256sum` and no `curl` at all. `internal/harness/bootstrap` covers the host's
`/bin/sh` (and `dash` where present) with `curl` and `shasum`, and `ci.yml`'s `fast` job runs that same test on
Ubuntu; this script covers the toolset neither can ever exercise on a developer's macOS box.
`internal/harness/bootstrap/doc.go` and `bootstrap_test.go`'s `dash` skip both name it as the documented manual
procedure.

**Nothing invokes it, and that is deliberate.** A `make` target would put a Docker dependency in the Makefile
that `make test` must never acquire — `make test` staying Docker-free is one of the constraints no convention
may override — while CI already shellchecks the file through `plugin-check.sh`'s glob. It is run by hand:

### Run locally

```sh
scripts/ci/bootstrap-alpine.sh        # LOCAL ONLY -- not run by CI; needs a working docker
```

Needs `docker`. Everything else is built inside the container: `plugin/bin/brigade` is mounted read-only, every
fixture is created in the container, and the fake release asset is served over loopback by busybox `httpd`, so
the run touches neither the network nor anything on the host. Three cases, one PASS/FAIL line each: a cold-cache
first run, a warm-cache second run with the served asset deleted first (so success proves it made no request),
and a wrong checksum (exit 11, nothing installed).

## `scripts/ci/conformance-setup-supabase.sh`

The conformance suite's `--setup` hook for the bundled Supabase adapter (plan 9.2, P2-6): the documented
out-of-band alternative for a backend whose principals are provisioned elsewhere. The suite runs it once per
fixture principal — a, b, then c — in that principal's from-scratch environment, and expects the three profiles
to come out joined: a and b in one team, c in another.

**Nothing invokes it, and that is deliberate.** `make test-integration` stopped passing `--setup`: with the
`BRIGADE_SUPABASE_URL` / `BRIGADE_SUPABASE_PUBLISHABLE_KEY` pair in the environment, `team create` and
`team join` bind an empty profile themselves, so the suite provisions its own teams, learns the join secret and
**runs** C-28 and C-40 instead of skipping them. The reasoning also sits on the `test-integration` recipe in the
Makefile and on the corresponding step in `ci.yml`. The file survives because it stores the out-of-band
procedure an adapter author would otherwise have to reconstruct, and because deleting it would quietly shrink
`plugin-check.sh`'s shellchecked set.

### Run locally

```sh
bin/brigade-conformance --env SUPABASE_URL=… --env SUPABASE_PUBLISHABLE_KEY=… \
  --setup scripts/ci/conformance-setup-supabase.sh --adapter bin/brigade -- adapter supabase
```

## Drift tests

`scripts/ci/*_test.go` is package `ci_test` and runs in `make test` — Docker-free, stack-free and model-free.
Ten files, one per thing they guard; the mapping is the house's rule (a test file per subject), the spelling is
Go's:

| Test file | What it guards |
| --- | --- |
| `checks_test.go` | The four POSIX-sh gates — `plugin-check.sh`, `no-secrets.sh`, `checksums-check.sh`, `release-verify.sh` — each proved able to FAIL against a fixture tree built under `t.TempDir()`, plus a read-only pass over the real repository. |
| `release_prep_test.go` | `release-prep.sh`: every refusal fires (dirty tree, wrong branch, a published tag). |
| `keepalive_test.go` | `keepalive.sh`: the four rungs against a fake endpoint, and a join of its endpoints to `gotrue.go`, `postgrest.go` and the schema migration. |
| `manifests_test.go` | `plugin/.claude-plugin/plugin.json`, `plugin/hooks/hooks.json` and `.claude-plugin/marketplace.json` parsed as JSON — `plugin-check.sh` is textual by design and `claude plugin validate` never runs in CI. |
| `proof_test.go` | `proof.sh`'s delimited constants block, joined against the Go and SQL sources. |
| `proof_headless_test.go` | `proof-headless.sh judge`, over `testdata/proof-headless/`. |
| `proof_idle_wake_test.go` | `proof-idle-wake.sh wake`, over `testdata/proof-idle-wake/`, plus the frame literals it shares with `proof.sh`. |
| `proof_crash_resume_test.go` | `proof-crash-resume.sh catchup`, over `testdata/proof-crash-resume/`, plus the shared frame literals and the lease and liveness budgets. |
| `fixtures_test.go` | The ignore rules around the fixture trees, and that no compiled Python is tracked. |
| `shell_test.go` | Every `scripts/*.sh`, `scripts/ci/*.sh` and `plugin/bin/brigade`: no comment line may sit directly after a line ending in a backslash. A commit put four such comments inside the `exec env … \` that launches every nested `claude` session, and three proof scripts silently ran `env` instead while `sh -n`, both shellcheck versions and every other drift test stayed green. |

Those joins are load-bearing and are the reason no script here can become a `make` recipe body: a recipe can be
neither opened by a test nor shellchecked as a file.

## Fixtures

Three fixture trees live under `scripts/ci/testdata/`, one per proof script, named after the script it belongs to
(`testdata/proof-<name>/` ↔ `scripts/proof-<name>.sh`). Every case directory is lowercase-hyphenated and
topic-first, and **a case-directory name is the fixture's identity**: it is the key in the matching test's
mutation table, the tree root is pinned by a Go constant in that test, and `.gitignore`'s
`!scripts/ci/testdata/**/*.log` negation is keyed to the path. Renaming one is a defect, not a tidy-up.

Counts and per-case members, measured 2026-09-05:

| Tree | Cases | Files | Every case contains |
| --- | --- | --- | --- |
| `testdata/proof-headless/` | 15 | 121 | `stream.jsonl`, `transcript.jsonl`, `send.json`, `meta.json`, `map.json`, `decoys.before.sha256`, `decoys.after.sha256`, `expect.json` |
| `testdata/proof-idle-wake/` | 17 | 86 | `stream.jsonl`, `transcript.jsonl`, `meta.json`, `stdin-writes.log`, `expect.json` |
| `testdata/proof-crash-resume/` | 21 | 315 | `expect.json`, `meta.json`, `precrash/{stream.jsonl,transcript.jsonl,map.json,stdin-writes.log,watcher.log}`, `resumed/{the same five}`, `sessions/{pre-kill.json,post-resume.json}`, `inbox/after-catchup.json` |

Two cases carry one extra file each — a scorer output that was committed with the case: `proof-headless/api-refused`
has a `verdict.json` and `proof-idle-wake/woke-boundary` has a `wake.json`. Neither is read: each test copies the
case into a fresh `t.TempDir()` and the scorer writes its own, so the committed copies only make the file counts
above 121 and 86 rather than 120 and 85.

### To add a case

The same three steps for every tree; write the fixture from a real run's evidence, never from memory.

1. **Create `scripts/ci/testdata/proof-<name>/<case>/`** with exactly the member list above, and give it a
   lowercase-hyphenated, topic-first name that says what class it stands for. Every `expect.json` must declare
   the fields that define its class — for `proof-crash-resume` at least `verdict`, `exactly_once`,
   `delivered_count`, `resumed` and `because`, and for `proof-idle-wake` a non-empty `because` — because a
   fixture that pins nothing would "pass" while asserting nothing.
2. **Nothing else is needed for the case to be exercised.** Each test enumerates the tree with `os.ReadDir` and
   subtests every directory it finds against that directory's own `expect.json`, so a new case runs the moment
   it exists. Each test also asserts a *minimum* case count (15 for headless, 13 for idle-wake, 15 for
   crash-resume), so deleting cases fails; adding them never does.
3. **Add a row to the mutation table in the matching `*_test.go`** (`proofHeadlessMutations`,
   `proofIdleWakeMutations`, `proofCrashResumeMutations`) if the case introduces a class the table does not
   already bite on. A mutation makes ONE change to a clean fixture — a dropped line, an edited JSON key, an
   injected tool call — and requires the verdict to flip; the helpers fail loudly when their anchor is missing,
   so a mutation can never silently apply to nothing.

Then, if the case was cut from a real run, run `make test` and confirm `TestFixturesAreNotGitignored` still
passes. It is the guard for the failure that produced it: the P4-3 commit carried seventeen `stdin-writes.log`
fixtures that `.gitignore`'s bare `*.log` swallowed at `git add` time, so every local run passed against the
files on disk while CI failed on a clean checkout that never had them (run `33915532579`).
