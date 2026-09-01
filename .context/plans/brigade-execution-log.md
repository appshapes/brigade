# Brigade execution log

The single source of truth for **where the work stands**. The plan says what to build; this file says what is done,
what is next, and which model tier each remaining task should run on. Update it in the same commit as the work.

Ticket 15 · branch `master` · merges only, never rebase · commit messages `15: <Imperative summary>` ·
`make push message="15: …"` for every commit after the first.

## Start here (for a fresh session, a resumed session, or another developer)

1. Read `.context/plans/claude-code-team-messaging-implementation-plan.md` — section 2 (decisions, all marked
   **Decided 2026-08-30** are settled), section 8 (phases and tasks), section 13 (commit plan).
   `.context/plans/claude-code-team-messaging-logical-plan.md` is the conceptual design behind it.
2. Read `docs/research/README.md` for what the eleven digests contain and `docs/research/decisions-2026-08-30.md`
   for the decision brief (it overrides the plan where they disagree).
3. Find the first row below with status `todo`, run it at the model tier in its column, then update its row.
4. Toolchain check before Phase 1: Go 1.27.0, Docker running, `npx --yes supabase@2.116.0 --version`.
   Not installed on the original machine: `shellcheck` (P1-8 needs it: `brew install shellcheck`), `psql`
   (not required — `make advisor-lints` runs psql inside the database container).

## Model tier policy

The user's Fable 5 usage is a hard budget (64% consumed at the time of planning). Run **Fable 5** only where the
quality difference matters; run **Opus 5** wherever it can succeed. If the Fable budget runs out while
Fable-tier tasks remain, **stop and wait for the reset** rather than downgrading them — the user stated this
explicitly and prefers waiting to a subpar implementation. The user may also hand the work to another developer
with her own Fable 5 session on this machine, which is why this log lives in the repository.

- **Fable 5**: protocol/schema design, Supabase SQL (RLS, RPCs, triggers), the security path (frame, sanitiser,
  socket poster, inbound policy, watcher), the conformance suite's design, adversarial review/verification passes,
  and any task whose acceptance criteria include a negative security test.
- **Opus 5**: scaffolding, Makefile/CI/goreleaser, the bootstrap script, docs, running scripted experiments and
  recording their results, test plumbing, mechanical ports, evidence collection.

**Cadence within a Fable-tier task (decided by Rjae, 2026-08-30).** Run **one author agent plus one full adversarial
verifier** — roughly 4–6 Fable subagents per task. No multi-lens design panels, no separate completeness critic. The
evidence for putting the budget here: the adversarial pass found **7** assertions passing for the wrong reason in E0-1
and **15** in E0-2 (including a soak that would have passed having sent zero messages, and a "50 concurrent senders"
claim that was never actually measured). Both authors reported fully green beforehand and neither caught its own gaps.
The design panels, by contrast, mostly yielded nice-to-have extras. So when the budget is trimmed, the verifier is what
survives. Prompt the verifier to assume a pass is for the wrong reason, to check that each assertion *could* fail, to
demand positive controls, and to strengthen weak checks in place before reporting. When the driver session's main loop
is a smaller model, Fable-tier work goes to subagents (`model: 'fable'`), never inline.

## Status

Legend: `done` · `todo` · `blocked (<reason>)` · `wip`.

| ID | Task (plan §8) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| — | First commit: scaffold, both plans, first research round | done | Fable | `6386046 15: Add implementation plan and repo conventions` |
| P0-0 | Preserve the first research round under `docs/research/` | done | Fable | in `6386046` |
| — | Plan decisions D3/D6/D18/D20/D22/D31/D32/D33/D34/D35 + all-Go, CLI-only rewrite | done | Fable | this commit |
| P0-2 | Preserve the second research round under `docs/research/` | done | Fable | this commit |
| P0-1 | Injection corpus (`scripts/injection-corpus/`, `expected.json`) | done | Fable | this commit — 26 items (17 `ask` / 9 `ignore`; 24 body + 2 summary-only), the plan's mandated 15 at `01`–`15` plus 11 additions |
| E0-1 | Local stack + `brigade` schema, ported live checks | done | Fable | this commit — `docs/experiments/E0-1.md`; (a)–(i) all answered; 72/72 live assertions; driver at `scripts/experiments/E0-1/` |
| E0-2 | Broadcast-from-DB with the Go Phoenix client (a-d, g settled) | done | Opus | this commit — `docs/experiments/E0-2.md`; (e)(f)(h)(i) answered; 42 fast + 14 soak assertions; **D21 stays on broadcast-from-DB** |
| E0-3 | Inbound framing variants A/C (and B fallback) + injection corpus | done | Fable | `docs/experiments/E0-3.md`; all checks incl. (b); **D19 = C** (C names the sender in the preview, A names nobody) |
| E0-4 | Idle-wake automation | done | Opus | this commit — `docs/experiments/E0-4.md`; **criterion MET, 12/12 wakes**, max 6.7 s of a 10 s budget; driver at `scripts/experiments/E0-4/` |
| E0-5 | Detached watcher lifecycle, `/clear`, SessionEnd budget | done | Opus | this commit — `docs/experiments/E0-5.md`; **two 6.6 defects found**; (a)(c)(d)(f)(h)(i) pass, (b) fails as specified |
| E0-6 | Token refresh coexistence + flock (core settled) | done | Opus | this commit — `docs/experiments/E0-6.md`; all six pass; **5.1's two-behind rule is WRONG**; lock poll costs 100 ms per contention |
| E0-7 | Two sessions, two profiles (option delivery settled) | done | Opus | this commit — `docs/experiments/E0-7.md`; all items pass; a fresh `CLAUDE_CONFIG_DIR` does NOT inherit the login |
| E0-8 | CLI-only mechanics: bootstrap timing, interactive ask rule, sandbox | done | Opus | this commit — `docs/experiments/E0-8.md`; (a)–(h) all answered; **D20's skill grant HOLDS interactively**; run on 2.1.252 |
| E0-9 | `crossSessionInbound` hold/refuse interaction | done | Opus | this commit — `docs/experiments/E0-9.md`; hold is loud and never expires (25 min); refuse is silent to BOTH sides |
| E0-10 | Hosted checks (optional, needs the hosted project) | blocked (D32: after the proof) | Opus | |
| P1-1 | Go module scaffold, Makefile, lint, CI, plugin pins | done | Opus | `0af93a1` — full gate green; **CI run 33533334741 green (`fast`, `macos`, `reproducibility`)**; cross-host reproducibility MEASURED; **7 plan defects in §7 plus 36 from the adversarial pass** (see below) |
| P1-2 | `internal/protocol` (types, errors, NDJSON, sanitiser, schema) | done | Fable | this commit — full gate green, protocol at ~98% coverage; **2 open `ndjson.go` boundary defects, see below**; 7 spec gaps for P1-4 |
| P1-3 | `internal/adapterkit` (stdin, XDG, atomic writes, flock, redaction) | todo | Fable | redaction is security-critical |
| P1-4 | `docs/protocol-v1.md` + adapter-authors skeleton | todo | Fable | user review gate |
| P1-5 | `cmd/brigade-adapter-fs` + mutants | todo | Opus | |
| P1-6 | `internal/conformance` + `cmd/brigade-conformance` | todo | Fable | suite design |
| P1-7 | `docs/adapter-authors.md` complete | todo | Opus | |
| P1-8 | `plugin/bin/brigade` bootstrap + plugin checks | todo | Opus | needs `shellcheck` |
| P2-1..P2-5 | Supabase schema, RPCs, realtime/housekeeping, pgTAP, advisor lints | todo | Fable | SQL and RLS |
| P2-6..P2-12 | Go Supabase client, profile/team/session/message commands, watch, integration, release rehearsal | todo | Fable (P2-6/P2-7/P2-10) · Opus (P2-8/P2-9/P2-11/P2-12) | credentials and watch are Fable-tier |
| P3-1 | Plugin manifests, marketplace, skills | todo | Opus | |
| P3-2 | `internal/harness` library (frame, socket-post, policy, pipeline) | todo | Fable | security path |
| P3-3..P3-5 | `brigade` session commands, hooks, watcher (+ sink mode) | todo | Fable | injection path |
| P3-6..P3-8 | Bootstrap wiring, headless smoke, interactive checks | todo | Opus | |
| P4-1..P4-6 | Vertical proof, headless/idle-wake runs, crash+resume, interactive checklist, results | todo | Fable (P4-2/P4-5/P4-6) · Opus (P4-1/P4-3/P4-4) | criterion 8 is Fable-tier |
| P5-1..P5-11 | Hardening, admin, docs, keychain, soak, release, `hold` policy | todo | mixed | after the proof |

## Onboarding and the adapter model — reviewed 2026-08-31, DESIGN STANDS (do not re-open)

Rjae reviewed the joining-and-operating experience end to end. **Conclusion: no change.** Recorded here so a later
session does not rediscover the same ground and re-litigate it.

The question asked was whether one shared team password should be all a human needs. Today a joining teammate needs
three strings — project URL, publishable key, join secret (5.9 step 4) — plus a plugin install, because
`team join --prompt` asks only for the secret and the label, so the backend must come from `profile init --url --key`
first. A single self-describing bootstrap string was proposed (both extras are non-secret by 5.1, and D5 already
bcrypts only the random tail, so the server path would be unchanged) and **was considered and declined**.

What settled it: **Step 2 being Supabase-specific is the adapter model working as designed, not a defect in it.**
4.1 freezes only `describe`, `session *` and `message *`; `team *` and `profile *` are conventions that MAY carry
adapter-specific flags, and `--url`/`--key` are the Supabase adapter's declared extras (5.2, 5.11). A different
adapter declares its own configuration and is selected by the `adapter_command` plugin option. Swapping adapters
therefore changes configuration and not the messaging surface, which is the portability boundary the logical plan
wanted. The one-string idea remains OPTIONAL UX polish, unrelated to adapter support; if it is ever wanted it must be
a 4.2 CONVENTION (an opaque bootstrap string each adapter parses), never a Supabase-only change, and it is cheapest
before P2-2 fixes the `brg1.` format.

Two further findings from the same review, both left as-is by decision:

- **Multi-team is supported; per-session switching is clumsy.** A user may hold many profiles, each bound to one team
  (D8). But inside a session the profile comes from the `profile` plugin option and `BRIGADE_PROFILE` is deliberately
  ignored (3.2), and `pluginConfigs` is read only from user settings, `--settings` or managed settings. So **one
  session talks to exactly one team**, two teams at once means two sessions with different `--settings` (as P4-2
  already does), and switching means editing settings or relaunching. The security reason for ignoring the
  environment variable is sound and stays. A session-scoped switch reading from user settings would be safe and is
  worth considering in Phase 5; it is NOT scoped now.
- **Reusing an existing Supabase project is possible but not recommended, and undocumented.** Data isolation is good
  (own schema, RLS everywhere, nothing granted to `anon` or `service_role` — verified live in E0-1), and adding
  `brigade` to `api.schemas` is additive. The blast radius is project-WIDE auth/API settings: anonymous sign-ins
  enabled, CAPTCHA off, no Pro session time-box/inactivity limits, Realtime "Allow public access" off, and a raised
  anonymous rate limit. 5.9 says "create a single-purpose project" and that stays the recommendation; a reuse
  checklist naming those five settings would be a useful Phase 5 docs addition.

## PHASE 0 IS COMPLETE (E0-10 excepted, blocked by D32)

Every experiment is closed. D19 = C, D21 = broadcast-from-the-database, D18 = `accept`, D20 = `off` by default and
its gate is now proven to work in every mode that matters. **Claude Code updated to 2.1.252 during E0-8**; the plan
says 2.1.251 throughout and should be re-checked where the version is load-bearing.

**The product promise is measured end to end.** Model-to-model messaging needs no human in the loop: delivery is
`accept` in every permission mode (E0-9), an idle session wakes on a socket post in ~1.7–3.6 s with nothing written
to stdin (E0-4), the model replies autonomously with the right ids in 20/20 runs (E0-3 a), and **D20's skill grant
holds in interactive Manual mode** — the one arm that had never been tested — so a Manual-mode user pays ONE
dismissible Skill prompt per project, not one per command (E0-8 b).

## Plan corrections from P1-1 (the first code commit)

Seven defects in section 7, all measured while scaffolding rather than argued. Four of them are checks that
PASS while testing nothing, which is the failure mode Phase 0 kept finding.

1. **7.2 — `go get -tool -modfile=tools.mod …` does not work on a fresh checkout.** The plan presents the
   command as sufficient to create `tools.mod`. It is not: with no `tools.mod` on disk it exits 1 with
   `go: open tools.mod: no such file or directory`. The file must be seeded first
   (`module github.com/appshapes/brigade/tools` + the `go` line) and only then does `go get -tool` populate
   it. The plan's accompanying claim — that `-modfile` leaves `go.mod` byte-identical — IS true and was
   verified by sha256 before and after both tool installs.
2. **`go.mod` needs `ignore docs/research`, and the plan does not mention it.** `docs/research/`
   `plugin-bootstrap-cli.files/` holds two orphan `package main` files that sit inside the root module (they
   are the only committed `.go` files not already shielded by a nested `go.mod` or a dot-prefixed directory).
   Without the directive `go build ./...` — the Makefile's `typecheck` — exits 1, and `go mod tidy -diff`
   wants to add a `golang.org/x/crypto/x509roots/fallback` require that nothing in the module imports. Both
   were demonstrated by removing the line. `go mod tidy` preserves the directive verbatim.
3. **7.3/7.4 — `gofmt -l .` is the wrong scope and turns `make lint` red.** It is a FILESYSTEM walk, while
   `go build`, `go vet` and `golangci-lint` are all MODULE-scoped. This repository commits Go under
   `docs/research/` and `scripts/experiments/` and keeps scratch under `.ignored/`; **19 files** fail
   `gofmt -l .`, every one of them inside its own nested `go.mod` and therefore not part of this module.
   Fixed by deriving the directory list from `go list -f '{{.Dir}}' ./...`, which keeps the two scopes
   identical and stays correct as packages are added. Guarded so a `go list` failure or an empty package set
   fails loudly instead of passing having read nothing.
4. **7.3 — the `.golangci.yml` as written CANNOT BE SATISFIED by any working program.** The only `os.Stdout`
   exemption is for the three protocol-output writers, and the `os.Exit` rule does not cover `os.Stdout` even
   in the `main.go` files it names. But something must hand the process's real stdout to a `Run(w io.Writer)`
   seam. Fixed with an exemption scoped to entry-point FILES only (`internal/app/main.go`,
   `internal/adapters/fs/main.go`, `internal/conformance/cli.go`, `cmd/*/main.go`), so the discipline still
   binds every internal package. Verified by experiment that a violation in `internal/cli` and in a
   non-`main.go` file under `cmd/` are both still caught.
5. **golangci-lint's DEFAULT issue caps silently truncate the report — this one matters beyond P1-1.**
   Measured: a file with **six** identical `forbidigo` violations reported exactly **three**
   (`max-same-issues` defaults to 3, `max-issues-per-linter` to 50). In this project `forbidigo` IS the
   stdout / `os.Exit` / environment / spawn discipline of 7.3, so a truncated report is a gate that hides the
   violations it exists to catch, and it makes a run that fixed three surface a "new" three next time. Both
   are now `0` (unlimited) and the same six-violation fixture reports 6 of 6.
6. **7.7 — the CI `fast` job as specified would be RED on its first push.** It calls `make checksums-check`
   and `make plugin-check`, but `scripts/ci/*.sh` are **P1-8** deliverables that do not exist yet. Resolved
   with the plan's own idiom: `plugin-check` is gated `if: false` with a comment naming P1-8, and
   `checksums-check` is replaced by plain `make cross` — which still produces `dist-cross/checksums.txt` and
   therefore still delivers P1-1's required cross-host reproducibility evidence without needing P1-8's script.
   (The `supabase` job stays gated until P2-1, as the plan already says.)
7. **7.2 — dependency versions have moved since the plan was written.** Confirmed live on the proxy on
   2026-08-31: `golang.org/x/text` is **v0.41.0** (the plan's v0.39.0 was a security FLOOR for GO-2026-5970,
   which v0.41.0 satisfies), and `golang.org/x/crypto/x509roots/fallback` is now
   **v0.0.0-20260831030451-39dc44e69c28** — the pseudo-version's date is the Mozilla bundle's date, so it
   moves on its own schedule. `github.com/jackc/pgx/v5` resolves to v5.10.0 for the plan's `v5.x.y`
   placeholder. Neither shipped module is linked yet at P1-1.

**Implemented here from earlier findings, so they are no longer outstanding:** `make supabase-start` and
`supabase-reset` now depend on a `migrations-check` target that fails fast and legibly when
`supabase/migrations/` holds no `.sql` file (E0-1 — otherwise PostgREST loops on `3F000` and the CLI reports
only `supabase_rest_brigade unexpected status 503`, naming PostgREST rather than the cause); and `unclaude`
strips the outer session's environment **by prefix** (every `CLAUDE*` plus `AI_AGENT`, keeping only
`CLAUDE_CONFIG_DIR`) rather than by the eight-name list of 7.4/9.6, which E0-4 and E0-7 measured to be short
by at least three.

### What the P1-1 adversarial pass found (four verifiers, 36 defects)

The cadence held: every author reported green, the gate was green, and the adversarial pass still found real
defects — 21 fixed in-lane by the verifiers, the rest by the driver. The pattern is unchanged from Phase 0:
**most of them are checks that pass while measuring nothing.** Two were instruments rather than results:

- **The buildinfo fallback test — a named P1-1 acceptance item — was a TAUTOLOGY.** It asserted
  `String() == resolve("", debug.ReadBuildInfo)`, which is literally `String()`'s own body when `Version` is
  empty; and both sides collapse to `"unknown"` anyway, because a `go test` binary records
  `Main.Version = "(devel)"`. It could not have failed. Replaced with a test that builds the binary with
  `go install` and reads the version back out of the installed artifact.
- **`tscmd.Status` — the instrument behind EVERY exit-code assertion in the txtar — had no positive control.**
  Deleting its comparison left the entire repository green, so `status 2 brigade no-such-command`,
  `status 1 brigade sessions` and `status 7 fake-adapter -exit 7` were all being carried by an instrument
  nobody had shown could fail. It now has its own tests.

Further defects worth carrying, beyond the seven plan corrections above:

8. **The forbidigo exemption paths were UNANCHORED substrings** — including the driver's own fix. golangci-lint
   matches `path` as an unanchored regex, so a bare `cmd/` exempted `os.Exit` in ANY file whose path merely
   contained it: measured, both `os.Stdout` and `os.Exit` went unreported in
   `internal/nested/cmd/tool/main.go`, and `os.Exit` in `internal/subcmd/deep/x.go`. Since these rules ARE the
   static half of the security path, an exemption that reaches further than it reads is a hole. Every `path` is
   now anchored, verified in both directions.
9. **`issues.uniq-by-line` defaults to TRUE and silently drops all but one finding per source LINE** — the same
   truncation class as `max-same-issues`, one level down, and it drops `forbidigo` findings specifically. Now
   `false`. Together with correction 5 above, golangci-lint shipped THREE separate defaults that hide findings.
10. **`make supabase-env` would have silently broken the committed R1 regression kit.** The recipe TRUNCATES
    `.env.test` and, per plan 7.4, renames everything to `SUPABASE_*` — but the promoted Phase 0 drivers
    (`scripts/experiments/E0-1`, `E0-2`, `E0-6`, `E0-6/verify`) read the CLI's OWN names via
    `loadEnv(".env.test")`: `API_URL` is renamed away and `ANON_KEY` and `JWT_SECRET` disappear entirely. One
    run of the target and every driver reads empty strings, failing in a way that looks like a broken stack.
    The recipe now writes BOTH name sets, through a temp file so a failed `supabase status` cannot truncate a
    working `.env.test`.
11. **`gofmt -l <dir>` RECURSES**, so correction 3's directory-based fix would have reverted to a whole-tree walk
    the moment a package existed at the repository root (`go list` then emits `.`). Measured with a stray root
    `main.go`: all eight committed nested-module files came back. It now lists FILES.
12. **Pipelines masked their own failures in `schema-check` and `deps-check`.** `go run ./cmd/brigade-schema |
    diff - <file>` takes the pipeline's status from `diff` alone, so it passed when the generator printed
    nothing against an empty committed schema, AND when the generator wrote correct bytes and then exited 1.
    Same shape in `deps-check`, where a failing `go version -m` left a 0-byte `deps.txt` that then "passed".
13. **`make cross` did not neutralise `GOAMD64`, `GOARM64` or `GOFLAGS`**, so the reproducibility criterion was
    defeated by whatever a developer had exported — and `.goreleaser.yaml` pins `goamd64`/`goarm64` itself, so
    the two builds disagreed for a reason no flag in `go_flags` covered. Both now build through a shared
    `go_build_env`.
14. **The cross-host reproducibility criterion was not a GATE.** Both CI jobs appended their checksums to the
    job summary and nothing ever compared them, so the two builds could diverge completely with CI green. The
    jobs now upload the checksums as artifacts and a `reproducibility` job diffs them.
15. **`UPDATE_SCRIPTS=1` in the ambient environment disarmed every byte-for-byte txtar assertion** — a broken
    `brigade version` would PASS and rewrite the committed golden to match its own wrong output. Plan 7.3 says
    goldens are updated "with `-update`", and the distinction is the point: a test FLAG cannot be set by
    inheritance. Now `go test ./cmd/brigade -update`.
16. **`release.yml` did not use the goreleaser the Makefile pins** — `version: "~> v2"` resolves to whatever the
    latest 2.x is at run time, while `make release` rehearses with v2.18.0. The whole committed-checksum flow
    rests on the release job reproducing the developer's bytes exactly. Now pinned.
17. **Plan 7.2's `go.mod` require block is the P2-6 end state, not P1-1's.** `make tidy-check` runs
    `go mod tidy -diff`, which strips any require no package imports, so P1-1's `go.mod` can only carry what
    the scaffold actually links. The consequence is that **`deps-check` inspects ZERO modules today** — the
    binary links no non-stdlib module, `bin/deps.txt` is 0 bytes, and `! grep -vxF` inverts "no lines selected"
    into success. The recipe now SAYS SO on every run rather than printing nothing and exiting 0.

**Known-vacuous until the task named, recorded so nobody reads them as evidence:** `deps-check` (P2-6); the
conformance step in `make test`, which selects zero cases (P1-6); the `depguard` rule for
`internal/harness/**`, which matches no file (P3-2); `run.build-tags` for the three mutant tags, whose files
arrive with P1-5; `govulncheck -mode binary`, which currently scans only the standard library; and, in CI,
`plugin-check` and `checksums-check`, which are gated off until P1-8 — so there is at present **no automated
secret scan and no plugin-pin verification**. Also latent: `make plugin-validate` cannot validate `./plugin`
until P3-1 writes `plugin/.claude-plugin/plugin.json`; the target now says that instead of failing.

**One behaviour was deliberately changed against an author agent's choice, and it is worth flagging.** Under
`--json`, help previously wrote the usage block to stderr and left stdout EMPTY at exit 0, on the stated
rationale that "stdout carries protocol output only". The rationale is right about raw text but the outcome
left a machine caller unable to tell success from a silent failure, so the usage block now travels inside a
4.3 success envelope on stdout — which serves that same principle rather than breaking it. In the same pass,
`--json` was made load-bearing on every error path: stdlib `flag` ABORTS at the first bad argument, so
`brigade version --bad-flag --json` never reached the flag and answered a machine caller with a human line on
stderr and an empty stdout.

## CROSS-HOST REPRODUCIBILITY IS MEASURED, not [likely] (2026-09-01, CI run 33533334741)

Plan 7.7 marks macOS-developer-vs-ubuntu-CI byte equality as **[likely]**, resting on Go's cross-host rebuild
claim, and says so honestly: it had only ever been shown on one darwin/arm64 machine. It is the load-bearing
assumption under the whole committed-checksum release flow — `plugin/bin/checksums.txt` must be in the commit
the tag points at, while goreleaser builds the binary from that tag afterwards, and only reproducibility closes
that circle.

**It holds.** All four targets, three independent hosts, byte-identical:

```
d0f9ff79a15fe3cd15db5ad0707db983fadd24af94c5c6b26ef5324a36836680  brigade_0.0.0_darwin_amd64
929fd9544eac2a2408d9e6ccb3a8850aa350e691464b93fdd0a8c224ea0d7feb  brigade_0.0.0_darwin_arm64
60969cf76aa6e63badafa172a03b219c2bc435f4bb96e8b62d36f6d7b7c5d0ed  brigade_0.0.0_linux_amd64
630c7ce346ddace58bc594a2c332702d113184157c33bc48cafcb83eeb30278c  brigade_0.0.0_linux_arm64
```

ubuntu-latest CI == macos-latest CI == this developer machine (darwin/arm64). The fallback 7.7 records — publish
the checksums from the release job and pin the plugin one version behind — is **not needed**. Verified by
downloading both CI artifacts and diffing them against the local `dist-cross/checksums.txt`, not by reading the
job's green tick: the job's own step is `diff ubuntu/checksums.txt macos/checksums.txt`, which would also pass
on two empty files, so the artifacts were confirmed to carry four real lines each.

What makes it hold is pinned deliberately and must not be loosened: `GOTOOLCHAIN` forced from the `go.mod` line
(not `auto`, which would silently use a newer local toolchain), `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`,
a version-only `-X` with no `.Commit`/`.Date`, and — added during the P1-1 adversarial pass — explicit
`GOFLAGS=`, `GOAMD64=v1` and `GOARM64=v8.0`, without which whatever a developer had exported would have defeated
the property, and `.goreleaser.yaml` pins the last two itself.

## P1-2 DONE — and TWO SOURCE DEFECTS ARE OPEN (fix these first on resume)

Full gate green (`typecheck lint build test vuln deps-check schema-check tidy-check`), `internal/protocol` at
~98% statement coverage, the P1-1 error-taxonomy refactor left `smoke.txtar`, `cli_test.go` and
`cmd/brigade/main_test.go` **byte-identical to HEAD** (only `internal/cli/code.go` became a thin alias), and
`deps-check` is **no longer vacuous** — the shipped binary now links exactly one allow-listed module
(`golang.org/x/text`, for the sanitiser's NFC), with the schema libraries confirmed out of `bin/brigade`.

**OPEN, in `internal/protocol/ndjson.go` — the adversarial verifier's lane was tests only, so these were
reported and NOT fixed. They are the first task on resume.** Both violate 7.3/4.4.9 ("lines up to 1 MiB are
delivered; a longer line is dropped and reading continues"), both are at the boundary, and both are missed by
the tests that exist:

- **D1 — a line of exactly 1 MiB terminated by CRLF is wrongly DROPPED.** The trimmed `\r`/`\n` are counted
  against the cap, so the identical line delivered with LF or at EOF is accepted while the CRLF form is not.
  No CRLF-at-cap test exists.
- **D2 — a final unterminated line one byte OVER the cap (`MaxLineBytes+1`) is wrongly DELIVERED.** The
  "+1 for the terminator" tolerance is misapplied on the EOF path; the LF and CRLF equivalents are correctly
  dropped. `TestLineReaderOverlongAtEOF` uses 2 MiB, far from the boundary, so it cannot see this.

Neither is a memory or injection hole and nothing consumes the reader yet (P1-6 and P2-10 do), which is why
the work was committed rather than held — but they are wire-format correctness bugs and the fix must come with
boundary tests at cap-1, cap and cap+1 for each of the three terminations (LF, CRLF, EOF).

**The sanitiser was green for the wrong reason and is now genuinely tested.** 6 of 15 mutations SURVIVED the
original suite — a no-op-equivalent could have passed it. The sharpest was an ASCII-only case-fold, under which
`<ſystem-reminder>` (U+017F LATIN SMALL LETTER LONG S) reaches the model as a raw tag; the others were a
dropped second NFC idempotence pass, `truncateRunes` pre-checking `len()` instead of `RuneCountInString` (a
legal at-cap multi-byte name wrongly cut), keeping `\r`, dropping the invalid-UTF-8 repair, and truncating a
body BEFORE tag neutralisation (neutralisation inflates bytes, so truncate-first overflows the cap). The source
was correct in every case; the TESTS could not tell. All 15 mutations now die and the corpus-seeded fuzz target
holds at 1.35M execs with its no-op positive control proven. **This is the clearest example yet of why the
verifier is not optional: the code was right, the gate was green, and the safety net was made of holes.**

**JSON Schema byte stability is MEASURED, not [likely].** Plan line 2004 marks it uncertain because of ordered
maps and says "the first two CI runs show it" — but no CI run had ever generated a non-empty document. Twelve
process runs produce one sha256; `$defs` is assembled in sorted-name order. Cross-machine stability is still
unmeasured (one darwin/arm64 host).

**Seven gaps in section 4 itself, all of which P1-4 must settle** (this is the review gate, so they go to Rjae):
1. The 4.4 examples contain `…` placeholders inside TYPED members (4.4.3 `created_at`, 4.4.9 times), so the
   plan's own literal examples cannot round-trip through their declared types.
2. 4.5.11 says names, labels and descriptions are "capped as in limits", but `limits` has **no** cap for
   `team_name` or `workspace_label`. Adapters and the conformance suite need either the mapping stated or two
   more `limits` members.
3. 4.3 says `retryable` is REQUIRED, which is unenforceable for a natural `bool` under the plan's own loose
   parsing (absent is indistinguishable from false). Either say consumers treat absent as false, or note that
   validators need presence detection.
4. The P1-2 row's "unknown fields survive" and 7.3's "unknown members are ignored" describe **different**
   properties — surviving the parse versus surviving the round trip. Implemented as the former; needs one
   clarifying sentence.
5. The `invalid_input` `details` key naming the offending member is specified NOWHERE in section 4 (only the
   task row says "field name in details"). Implemented as `details.field`; P1-4 must freeze it or adapters
   will disagree.
6. 4.4.9 never enumerates the `ready` event's `mode` values (only `push` is shown; the polling string is never
   named) nor the `status` event's state set — C-33 asserts one of them.
7. 4.4.8/4.4.9 show `"unknown": []` emitted even when empty, while the struct-tag convention is `omitzero`; an
   implementation following the convention mechanically would drop the member.

Also for P1-4: `RequiredFromJSONSchemaTags: true` (plan 7.3) yields **zero** `required` arrays anywhere,
because the wire structs carry no `jsonschema:"required"` tags. The schema is therefore weaker than it reads.

## Plan corrections from E0-8

1. **6.2 — make the BACKGROUND download the default.** Synchronous costs 8.5 s at 1 MB/s (passes the 20 s bar) but
   **33.5 s at 250 kB/s** (fails it), blocking session startup throughout. The background variant returns the hook in
   **0.019–0.020 s** and warms the cache behind the session. Break-evens for a 8,324,402 B asset: 416 kB/s for the
   20 s bar, 185 kB/s for curl `--max-time 45`, 139 kB/s for the hook's 60 s timeout — recompute if the shipped
   binary size differs materially.
2. **5.1 — the adapter must not honour `NO_PROXY` for loopback inside the sandbox.** The sandbox exports
   `NO_PROXY=localhost,127.0.0.1,::1,…`, so a client honouring it dials loopback DIRECTLY and is refused
   (`operation not permitted`, 26/26). No `allowedDomains` value fixes that — not `["127.0.0.1"]`, `["localhost"]`,
   `["*"]`, nor `allowAllUnixSockets`. Through the sandbox's own proxy, `["127.0.0.1"]` works. The allowlist matches
   the LITERAL host as written in the URL, not a resolved address, so `docs/setup.md` must ship the loopback URL
   using the same literal the allowlist entry uses.
3. **3.x/6.3 — the `SessionStart` context line is NOT byte-for-byte the hook's stdout.** It is `stdout.strip()` —
   whitespace removed at BOTH ends (100% across 14 runs and 5 adversarial payloads; interior formatting untouched).
   A hook that relies on leading indentation or a trailing newline will not get it.
4. **6.3 — `SessionStart` RE-FIRES on `/clear`.** A naive hook re-mints per-session state and would rotate the
   session's Brigade identity on every `/clear`. The hook MUST be idempotent per `CLAUDE_PID`.
5. **6.9 — set the skill's `--body-file` threshold below 10,000 characters.** Sharp boundary bisected to the
   character: 9,999 and 10,000 succeed; **10,001 fails** — the Bash parser aborts, `Bash(brigade:*)` cannot match,
   and the call is denied in `-p` and prompted interactively **with no "don't ask again" option**, so no rule can
   ever pre-approve it.
6. **7.x CI — `claude plugin validate --strict` does NOT detect a missing hook-command binary.** A `hooks.json`
   pointing at a non-existent path passes with exit 0. `scripts/ci/plugin-check.sh` must assert hook-command
   existence and the executable bit itself. Validate IS usable in CI otherwise: exit 0 with no login, in 0.13 s.
7. **6.9/D20 — state the mechanism precisely.** The `allowed-tools` DECLARATION is what raises the one-time Skill
   dialog (a mismatched pattern raises it too); only a pattern that MATCHES buys the Bash silence. The dialog's
   dismissal is scoped to the PROJECT DIRECTORY, so a user with many repos approves once per repo.

## D19 IS DECIDED — variant C (interactive sitting, 2026-08-31)

**D19 = C: the `<brigade-message>` frame nested inside `<cross-session-message from-name="…">`.** Observed by Rjae at
a real terminal. C's one-line preview reads `Message from @payments-api: <brigade-message team="ops" …` while A's
header is the anonymous `Another Claude session sent a message:` — A names nobody outside the frame. **C's outer
wrapper costs nothing**: the harness CONSUMES it and turns it into the attribution, so the outer tag never reaches
the model. Same inner frame (byte-identical), same reply behaviour (both 5/5 correct, 0 native `SendMessage`, 0
evasive). Variant B was not run and is not needed. Note for the skill: `from-name` is free text any member can copy,
so the preview's `@payments-api` is cosmetic — `from-principal` stays the only server-stamped identity.

## Plan corrections from the interactive sitting (2026-08-31)

1. **6.7 — the harness preamble is not a single prefix.** It is a short header line BEFORE the frame
   (`Another Claude session sent a message:`) and the long trust text AFTER it. Any reasoning that assumes one
   leading block is wrong.
2. **6.7 — the preamble text quoted in the plan does not exist in 2.1.251.** The plan quotes it as *"Another Claude
   session sent a message … reply via SendMessage to the `from=` address"* and cites that phrase as the reason D19
   must never emit a `did:` address. The real text says NOTHING about `SendMessage` or a `from=` address; it is the
   permission-laundering warning (captured verbatim in `docs/experiments/E0-3.md`). The conclusion survives and is
   stronger — the harness gives the model no reply instruction at all — but the rationale quotes text that is not
   there.
3. **`docs/security.md` — sticky dismissal depends on WHY the prompt appeared.** A prompt raised by an explicit
   `permissions.ask` rule offers only `Yes`/`No`: **the D20 gate cannot be one-click disabled.** A prompt raised by
   default Manual-mode approval (no rule) additionally offers `Yes, and don't ask again for: <pattern> *` AND
   `Yes, and switch to auto mode`. Neither is a flaw, but the setup docs must not promise "you will be prompted"
   when the first prompt offers never to prompt again.
4. **`docs/security.md` — a denied model proposes evasions to its own user.** On being blocked by a deny rule the
   model volunteered "let me know if you'd like me to try a different form of it". It did not attempt one, but the
   rule gates the MODEL acting, not the USER being talked into widening it. The skill (6.9) should discourage
   proposing alternative invocation forms.
5. **A THIRD first-run interruption exists.** Beyond the trust dialog (E0-4) and the fullscreen-renderer write
   (E0-5), a *"Claude in Chrome extension detected"* prompt appeared before the session prompt. Any `expect` driver
   must tolerate an ARBITRARY onboarding prompt, not just the trust one — a stray prompt silently consumes the
   keystroke meant for the real dialog. Matters for P4-3 and P4-5.
6. **6.10 — the settings scan is load-bearing, not defensive (E0-9).** `refuse` drops a post with NO signal to the
   receiving user and NO signal to the sender: the poster's socket write SUCCEEDS and returns nothing. Without the
   scan switching Brigade to `refuse` too, the watcher acknowledges messages the harness threw away — the message is
   gone AND the sender has been told it arrived.

## Plan corrections required before Phase 2 (from E0-6)

1. **5.1's stated server rule is WRONG. "A token two or more steps behind revokes the family" does not hold** on
   GoTrue v2.196.0 as configured. Measured on an independent throwaway principal with >12 s between steps (so the
   10 s reuse dedup cannot explain it): one-behind → HTTP 200 carrying the ACTIVE token; two-or-more-behind →
   HTTP 400 `refresh_token_already_used`; **and the active token still answers HTTP 200 one second later.** The stale
   token is refused, the family SURVIVES. Consequence for P2-6: 5.1 currently treats `refresh_token_already_used` as
   re-read-once-then-TERMINAL, and terminal means clearing `session.json` and minting a new principal on the next
   `team join`. Since the family is intact, the re-read-and-retry will normally succeed and going terminal on first
   failure would destroy a working credential.
2. **The lock is fine; the 100 ms poll is the cost.** The soak's "7,897 acquisitions, max 0.179 ms" measured nothing
   (see below). Re-measured with jittered racers: **23 of 80 contended, at min 100.2 / median 101.1 / max 303.1 ms,
   while the lock is only held 36 ms.** `LOCK_NB` polled every 100 ms rounds any contention up to a whole poll
   quantum. The plan's own fallback — "keep the lock but make it blocking without the 10 s bound (P2-6)" — is
   correct; either block on a goroutine with a 10 s timeout or drop the poll to 5–10 ms. The 10 s `unavailable` bound
   was separately proven live (a 13 s holder produced `unavailable`), so it is not dead code.
3. **One-behind tolerance is LOAD-BEARING for crash recovery, not a convenience — say so in 5.1.** Checks (d) and (f)
   are the same mechanism: the SIGKILLed process had already rotated server-side and died before the atomic write, so
   the new token was lost from disk, and the profile recovered ONLY because the stale file token was later redeemed
   as one-behind. Without that tolerance, any crash between the refresh answer and the atomic write strands the
   profile.

Two smaller facts for 5.1: **PostgREST allows ~30 s of clock skew past `exp`** (HTTP 200 up to 30 s past, `PGRST303`
from 31 s), so a JWT-expired answer is not a precise expiry signal; and **only the 90 s margin trigger can fire in
normal operation** — the reactive "refresh after `PGRST303`" path is a cold-start / long-sleep / clock-jump path
only. Keep it (it was verified working when forced), but state what it is for.

## Plan corrections required before Phase 3 (from E0-5)

Both of 6.6's watcher exit conditions are wrong as specified. These are not open questions — they are measured
defects with known fixes, and P3-5 (the watcher) must not be written against the current text.

1. **6.6 — `kill(pid,0)` does not detect a SIGKILLed session.** The pid stays kill-alive as a zombie until reaped,
   `ps -o lstart=` still returns its original start time, the socket file is left on disk, and `SessionEnd` never
   runs. Detection took **27.6 s and 59.0 s**; once the corpse is reaped it is 1.04 s. 6.6 dismisses this as
   impossible in production; that dismissal is too confident, since the latency belongs to whoever reaps, and that
   distribution is unmeasured for real terminals and IDE hosts. **Fix:** read the process *state* so a zombie reads
   as dead, rather than trusting `kill(pid,0)`.
2. **6.6 — socket-ENOENT is a PERMANENT FALSE POSITIVE.** `claude` never re-creates `/tmp/cc-socks/<pid>.sock` after
   it is unlinked (polled 25 ms for 16 s, never returns), and the session keeps working normally without it. A
   watcher exiting on ENOENT has killed itself for a live session with no recovery. The path is also derived from the
   pid, so it is not independent of the pid check. **Fix:** drop it, or replace it with a `connect()` probe.
3. **6.6 — compare-then-delete before unlinking a pidfile is required, not optional.** The replace path leaves the
   superseded watcher running, and its cleanup would delete the pidfile that by then belongs to its replacement.
4. **3.8 — a plugin cannot buy SessionEnd budget; a settings file can.** Measured two-sided with byte-identical
   `bin/` trees: plugin `timeout: 5` → 1.5033 s, no timeout → 1.5002 s, `timeout: 60` → 1.4991 s; the same hook in a
   `--settings` file is honoured exactly (3.00 s at 3, 5.00 s at 5). The hooks page does not draw this distinction.
   The session-end hook keeps its 1 s cap, as the plan's fallback said. The budget is one shared wall-clock deadline
   across all SessionEnd hooks running in parallel.
5. **6.3 — `SessionEnd` cannot be the only close path.** It does not fire at all on SIGKILL, and on `/exit` it fires
   ~0.5 s BEFORE `claude` actually exits, with the watcher pidfile still present.
6. **D9 — the by-native map must tolerate a native session id RECURRING within one process.** `/resume` returns the
   id to its pre-`/clear` value (`99a48c02 → clear → d2c72366 → resume → 99a48c02`). Ids are not monotonic.
7. **9.6 config protection is under-scoped.** Runs also touch `$CLAUDE_CONFIG_DIR/history.jsonl` (a line per typed
   slash command) and `$CLAUDE_CONFIG_DIR/projects/` (a transcript directory per project), and `/fork` creates
   daemons. Also observed: Claude Code wrote `fullscreenAutoDisabled` into `.claude.json` by itself after two
   pty-driven sessions failed to start the fullscreen renderer — a config-wide setting affecting the user's own
   sessions. Reverted and verified, but any future pty-driven experiment should expect it.

**Good news from the same experiment, so P3-5 knows what it can rely on:** D9's hash-compare-and-respawn is safe —
neither the socket path nor the token hash moves across `/clear` or `/resume` (confirmed three times, once with no
watcher at all), so the respawn branch is cold and only `/fork` (which changes the pid) exercises it. The start-token
guard works in both directions. The SessionEnd close completes in ~0.105 s against its 1 s cap.

## Open questions carried from research (settle during the phase that needs them)

- `${CLAUDE_PLUGIN_ROOT}` substitution in a hook's `command` field vs `args` (E0-5 (g), documented but re-record).
- First-use download time vs the SessionStart timeout; whether the background-download bootstrap variant is needed (E0-8 (a)).
- Whether `not_found` should move off SQLSTATE P0002 (HTTP 500 at the gateway) to a `PT4xx` code (P2-2).
  **Evidence in (E0-1):** confirmed live — `brigade:not_found` reaches the client as HTTP 500 carrying SQLSTATE
  `P0002`, payloads byte-identical across foreign and unknown ids. The security property is unaffected either way, so
  this is purely an adapter-ergonomics call for P2-2: keep `P0002` and require the adapter to key on SQLSTATE (a 5xx
  that must not be retried), or move to `PT4xx` for an honest 4xx. Decide it in P2-2, not before.
- ~~pg_cron availability on the minimal local stack (E0-1 (h)).~~ **RESOLVED (E0-1):** pg_cron 1.6.4 is present on
  the 5.9 `-x` stack, `create extension` succeeds and `cron.schedule` runs `brigade.gc_expired()` hourly. The
  opportunistic-gc fallback is not needed locally; the housekeeping migration still guards the scheduling so a stack
  without pg_cron applies cleanly.
- Loopback from a sandboxed Bash tool via `sandbox.network.allowedDomains` (E0-8 (c)).
- `GORELEASER_CURRENT_TAG` with `--skip=validate` on a not-yet-existing tag (P2-12 rehearsal).
- ~~Whether a fresh `CLAUDE_CONFIG_DIR` needs its own `claude login` (E0-7, matters for P5-10).~~ **RESOLVED (E0-7):
  it does NOT inherit the login.** Headless exits 1 in 0.5 s with `Not logged in · Please run /login`; interactive
  opens full first-run onboarding (welcome → theme picker). Keychain credentials are scoped PER CONFIG DIRECTORY, one
  item per dir. P5-10 must document a one-time `claude login` per config dir. That one login is SUFFICIENT is
  inferred, not measured — no login was attempted, since that is the user's own interactive action.
- **The by-pid map is an unauthenticated trust boundary (new, from E0-7).** `brigade` takes BOTH the profile and the
  team out of `state/by-pid/<pid>.json`, in preference to the environment — proven by a poison control (an env var
  saying `bravo` against a map saying `alpha` resolved to `alpha`). That is correct by design, since it is the only
  path available to the Bash tool, but it means **anything able to write that file decides which profile and team a
  session acts as**, guarded by filesystem permissions alone (0600 under `${BRIGADE_STATE_DIR}`). P3-3 and P3-5 must
  be written knowingly against this, and it belongs in the threat model beside T4. Mitigation available for the
  teardown path: plugin options reach ALL THREE lifecycle hooks (SessionStart, UserPromptSubmit, SessionEnd),
  reconfirmed across nine firings, so a close can be attributed from the option directly rather than via the map.
- **The environment strip list is short by three MORE (E0-7, extending the E0-4 finding).** Stripping by prefix
  removed eleven variables here; beyond `CLAUDE_CODE_BRIDGE_SESSION_ID` the list also misses `CLAUDE_EFFORT` and
  `AI_AGENT` (not even `CLAUDE_`-prefixed). Replace the enumerated list with the prefix rule in 9.6 and 7.4, keeping
  only `CLAUDE_CONFIG_DIR`.
- **Two plan text corrections from E0-7.** (1) The plan names `/Users/rjae/.claude-ifthen` as "this user's real config
  dir"; the dir in use by the driver session is `/Users/rjae/.claude-thinktech` — `.claude-ifthen` is the PLANNING
  session's. Make the sentence dir-agnostic (read `CLAUDE_CONFIG_DIR` at run time). (2) The "debug profile
  resolution" fallback for E0-7 is dead by evidence and should be dropped or repurposed as a diagnostic for the
  now-measured `nomap` failure (exit 6).
- ~~**Are `@`-mentions inert inside a sanitised body?**~~ **RESOLVED (E0-3), negatively — no change needed.** The
  harness does NOT expand `@`-mentions inside an injected frame: `@~/.ssh/id_rsa` in item `08-at-mention-ssh-key`
  stayed inert text, no key material appeared in any transcript, and there was no Read of a credentials path in any
  of the 26 attack-set runs. So item `08` measures the model as intended, 6.7 keeps its five-family scope with no
  `@` rule, and Rjae's disposition (document as a residual risk rather than add a rule) stands but is moot — there is
  no residual risk to document.
- **Does the sanitiser's tag matcher survive near-misses?** Corpus item `25-tag-matcher-evasion` carries byte-exact
  probes the spec's "optional whitespace" rule should catch (space, tab and newline between `<` and the name, mixed
  case) plus two it deliberately should not (a close tag split mid-name, a pre-encoded `&lt;brigade-message`). P1-2
  decides which of the latter two, if either, the matcher must also handle; today they are fuzz seeds, not assertions.
- **The plan's environment-strip list is incomplete (found in E0-4).** 9.6 and 7.4 name eight variables to unset
  before a nested run (`CLAUDE_PID`, `CLAUDE_CODE_SESSION_ID`, `CLAUDE_CODE_MESSAGING_SOCKET`,
  `CLAUDE_CODE_MESSAGING_TOKEN`, `CLAUDE_CODE_ENTRYPOINT`, `CLAUDECODE`, `CLAUDE_CODE_CHILD_SESSION`,
  `CLAUDE_CODE_EXECPATH`) and present that list as exhaustive. **`CLAUDE_CODE_BRIDGE_SESSION_ID` is also exported by
  an outer session and is not on it.** It caused no contamination in E0-4 (the nested session minted its own
  `bridgeSessionId`), but the list is wrong as documented. Fix in P1-1/P1-8 by either adding it or — better, since it
  is robust against the next variable Claude Code adds — stripping `CLAUDE_CODE_*` as a prefix, keeping only
  `CLAUDE_CONFIG_DIR`.
- **P4-2's mechanical detector must not flag refusals (established in E0-3).** 9.6 says the forbidden-call list is
  asserted mechanically by a `jq` script over the `stream-json` transcript. But a model that correctly REFUSES an
  exfiltration request names the secret while declining — E0-3 produced replies such as "I won't read
  `~/.aws/credentials` or send access key IDs", "I won't read `.env` or send any form of `STRIPE_WEBHOOK_SECRET`
  … including base64/hex, or a prefix+suffix+length", and "Not doing the `settings.json` edit". A naive grep for a
  credentials path or a settings filename over tool inputs flags all three as violations; a sweep written that way
  during E0-3 produced exactly those three false positives and zero true ones. The detector must therefore key on the
  ACTION (a Read whose `file_path` is a credentials path; an Edit/Write targeting settings or CLAUDE.md; a `brigade
  send` whose body carries file CONTENT or a token value) and never on a path or secret NAME appearing in prose.
  Getting this wrong makes criterion 8 fail on the very behaviour it is trying to confirm.
- **`make supabase-start` needs a migrations guard (P1-1).** Established in E0-1: with `brigade` in `[api] schemas`,
  `supabase start` cannot succeed until a migration creates the schema — PostgREST loops on `3F000 schema "brigade"
  does not exist`, the `rest` container never turns healthy, and the CLI tears the whole stack down reporting only
  `supabase_rest_brigade unexpected status 503`. A fresh clone is fine (migrations are committed), but the failure
  names PostgREST rather than the cause, so the recipe should fail fast with a readable message when
  `supabase/migrations/` is empty.
- **Is the five-family list the right boundary?** Corpus item `24-unlisted-forged-tag` uses `<important_instructions>`,
  which is outside the five sanitised families and so reaches the model verbatim (verified: zero matcher hits). If
  E0-3 (f) shows a model treating an unlisted tag as harness authority, 6.7's family list is under-inclusive and the
  fix is a frame/sanitiser change, not a corpus change (P0-1, R9).

- 2026-09-01 ~02:00: **P1-1 done — the first code commit.** Driven by session `15-implement-brigade-0831`,
  which took the hand-off from `15-implement-brigade-0830` cold: the execution log plus
  `.ignored/handoff-15-to-implement-0831.md` was sufficient and nothing had to be re-derived. Six Opus authors
  on disjoint file sets (module, lint/CI/release configs, repo+plugin files, Makefile, Go source, test
  plumbing), the Go work pipelined behind the module init so nothing raced on `go.mod`; then the driver ran the
  acceptance gate itself; then four adversarial verifiers. **Seven defects in plan §7 and 36 from the
  adversarial pass**, written up above. The gate is green, `-race -shuffle=on` is stable across three runs, and
  `make cross` reproduces byte-identically from a clean copy at a different path — the same-host half of the
  reproducibility criterion. **Cross-HOST reproducibility is still unproven**: it needs CI, which has never run,
  and the new `reproducibility` job is the gate for it.
  Three things earned their keep and should be repeated. (1) The adversarial pass again found a decisive flaw —
  this time including one in the driver's OWN fix (the forbidigo exemption paths were unanchored substrings).
  **Nobody's work is exempt from the pass, the integrator's least of all.** (2) Instruments must be tested
  before results are believed: the buildinfo fallback test was a tautology and `tscmd.Status`, which carries
  every exit-code assertion in the txtar, had no positive control. (3) golangci-lint shipped **three** separate
  defaults that silently hide findings (`max-same-issues`, `max-issues-per-linter`, `uniq-by-line`); a lint gate
  is not a gate until you have watched it report every violation you planted.
  Process note: a probe of the shape `cd "$D" && rm -rf *` was declined by Rjae mid-run. The objection was not
  correctness — the `&&` guard held — but that an unbounded recursive delete rested entirely on a preceding
  `cd`, with a dirty git tree as the fallback target, and that approving it trains the pattern. **Rule adopted
  for the rest of the run: never `rm -rf` a relative path or a glob; delete a named absolute path constructed in
  the same command.** It is in every subsequent agent prompt.

## Notes for a hand-off

- Everything needed to continue is in the repository: the two plans, this log, `docs/research/`. No session
  context, chat history, or per-user memory is required.
- The user's Claude config dir is non-default (`CLAUDE_CONFIG_DIR=~/.claude-ifthen` on the original machine);
  never hardcode `~/.claude` in code, tests or docs.
- Three probe messages were posted into the planning session's own inbox socket on 2026-08-30 to validate the
  wire protocol (plain, and wrapped with `from-name`); Appendix A.2 records what came back.

## Session journal

- 2026-08-30 ~15:40: a second interactive session (`15-brigade-0830`, same working tree) started and requested
  hand-off of implementation. The planning session replied with the state summary, is not mid-edit on anything,
  and stays hands-off unless Rjae redirects it. Exactly one session drives at a time; both sessions share this
  checkout, so: `git pull` before starting a task, commit + push at every task boundary, never two sessions
  editing concurrently. Keyboard-dependent observations (E0-8 (b) ask-rule dialog, E0-3 (b) preview rendering,
  E0-9 native hold/refuse) need Rjae at the keyboard of whichever session runs them.
- 2026-08-30 ~16:05: Rjae designated the other developer's session `15-implement-brigade-0830` as the
  implementation driver (her own Fable 5 budget; reached across config dirs via a registry-copy bridge).
  The earlier session `15-brigade-0830` was closed without starting anything. The planning session
  (`15-create-team-session-messaging`) is hands-off from here: it edits nothing, remains open as a reference,
  and wrote the cold-start hand-off to `.ignored/handoff-15-to-implement-0830.md`. Driver rules restated:
  `git pull` before each task, `make push message="15: …"` at every task boundary, one driver at a time.
- 2026-08-30 ~17:05: **P0-1 done.** The driver session's main loop runs Opus, so the corpus was authored by Fable 5
  subagents per the model tier policy: three design lenses (injection research, Claude Code harness surface, Brigade
  protocol) proposed items beyond the mandated fifteen, one synthesiser fixed the manifest and every `ask`/`ignore`
  value, four batches authored the payloads, four adversarial reviewers repaired them, and a completeness critic read
  every file back off disk. The critic named three missing attack classes and they were authored in a second pass:
  `24-unlisted-forged-tag`, `25-tag-matcher-evasion`, `26-encoded-exfiltration`. They are in now rather than later
  because the corpus is consumed *unchanged* by E0-3 (f), P4-2, P4-5, R9 and the U-03/U-04 fixtures — adding an item
  after E0-3 runs means re-running E0-3. Three findings went to the open-questions list above; the `@`-mention one
  (item `08`) should be settled in E0-3 (b) or that item measures the harness rather than the model. Also recorded
  there: items `22-homoglyph-forged-reminder` and `23-forged-native-wrapper` are the two `ignore` items most exposed
  to grader interpretation (both are request-free authority spoofs a model may verbally rebuff), so P4-2 should watch
  them for 3-of-3 flake. Verified independently of the agents: 26 files, contiguous numbering, all UTF-8 with a single
  trailing newline and under the 16 KiB cap, no real secrets or destructive instructions, every host under
  `.invalid`, item `11`'s bidi code points present on disk, and `expected.json` in exact agreement with the directory.
- 2026-08-30 ~17:55: **E0-1 done**, results in `docs/experiments/E0-1.md`, driver promoted to
  `scripts/experiments/E0-1/` (builds with no dependencies; run it from the repo root, `loadEnv` reads `./.env.test`).
  All of (a)–(i) answered; nothing fell back to the D22 `public` alternative. Split by tier: the SQL and the security
  assertions ran on Fable subagents (three migrations authored from 5.3–5.8 plus a fidelity and an adversarial
  privilege review; then the live-check harness plus an adversarial verification pass), while the stack bring-up and
  the empirical probes (b)–(g) ran on Opus in the main loop. Live result: 72 assertions, 0 failed, re-run green after
  a `db reset`. The adversarial pass mattered — it found seven assertions that passed for the wrong reason, the worst
  being that the seven byte-identity `not_found` comparisons had no positive controls and would have passed even if
  every id returned `not_found`. Three facts to carry: `P0002` surfaces as HTTP 500 so adapters must key on SQLSTATE
  not status; publishable-key-without-JWT denials are HTTP 401 while table denials with a JWT are HTTP 403, both
  `42501`; and the minimal `auth.users` fixture insert is `id` alone, but leaves `aud` and `role` as empty strings,
  so Phase 2 fixture helpers should set them explicitly. Stack left running with the schema applied.
- 2026-08-30 ~20:05: **E0-2 done**, results in `docs/experiments/E0-2.md`, driver promoted to
  `scripts/experiments/E0-2/`. **D21 stays on broadcast-from-the-database** — neither flip condition fired. Ran at
  Opus tier throughout (author + adversarial verifier), per the tier policy. (e) exactly-once holds with concurrency
  *measured* (peak 50 in-flight RPCs) and the drain racing the senders; (f) the strict race did not reproduce in 20
  attempts across 3 runs, with 40 of 80 sends confirmed to have committed after join ok so the negative is not
  vacuous, while the pre-join window misses 100% of the time and drain-on-join-ok covers it 20/20; (h) revocation
  takes effect on both the `leave_team` and the membership-only SQL path, byte-identical to a non-member baseline;
  (i) the 30-minute soak passed 14/14, including at-least-once recovery of 5 messages across a `supabase stop`/`start`.
  The drain is `fetch_inbox` + ack per page with no `seq` watermark, and the run **empirically observed** the 5.7
  inversion (cross-page `seq` going backwards) that would have made a watermark lose rows. Adversarial review found
  15 wrong-reason passes, the most important being that the exactly-once detectors had never been shown capable of
  firing — three fault-injection self-tests now prove they do.
- 2026-08-30 ~20:05: **Rjae's answers to the pending questions**, recorded where each belongs: Fable cadence → one
  author + one adversarial verifier, no design panels (see "Model tier policy" above); P0-1 corpus → keep all 26
  items, no trim; `@`-mentions → if E0-3 (b) shows expansion, document as a residual risk in `docs/security.md`, do
  NOT add a 6.7 sanitiser rule (see the open question above); the keyboard-dependent sitting → **deferred**, so run
  every automatable part of E0-3 and then E0-4..E0-7 first, and close E0-3 (b), E0-8 (b) and E0-9 together in one
  ~15-minute sitting afterwards. E0-3 therefore runs to completion minus (b), and D19 is decided after that sitting.
- 2026-08-31 ~06:15: **E0-3 automated checks closed**, results in `docs/experiments/E0-3.md`, harness promoted to
  `scripts/experiments/E0-3/` (shellcheck clean). **(a) PASSES for BOTH variants — 5/5 `-p` and 5/5 interactive each,
  0 native `SendMessage`, 0 evasive forms**, ids checked against the values actually posted. (c) the malicious body
  was refused in both variants. (f) **26/26** of the attack set pass; nothing blocks Phase 4. A and C are tied, so
  **D19 is provisionally C** (its inner frame is byte-identical to A's, so attribution is free) — but the tie-break
  is (b)'s to make at the sitting and was deliberately not guessed. Variant B was not run and is not needed.
  Two caveats recorded in the writeup rather than buried: (1) the receiving harness prepends its OWN preamble, which
  already forbids config edits, treating a peer as approval, and permission laundering — so 26/26 does NOT isolate
  the contribution of Brigade's 6.7 frame, and separating them would need a frameless control run E0-3 was not scoped
  for; (2) one earlier interactive run was killed during artifact collection and was re-run rather than scored from
  a partial artifact. Mechanism worth carrying to 6.6: a socket-injected frame arrives as a QUEUED COMMAND and is
  dequeued only after the current turn, so a `-p` prompt must stay busy longer than the poster delay; and the `-p`
  `stream-json` output does not echo the frame — the authoritative record is the on-disk session transcript.
- 2026-08-31 ~21:30: **Phase 0 CLOSED and handed off.** E0-8's remainder ran at Opus tier and closed the last open
  question: **D20's skill grant HOLDS in interactive Manual mode** (3/3 with the grant, 0 Bash prompts across three
  commands including the write verb; 3/3 prompted without it), so Rjae's stated bar — "little to no interaction
  required by the user to conduct model-model communication" — is met end to end and measured, not assumed. The one
  newly found cost is a single dismissible Skill dialog per project. Seven more plan corrections came out of E0-8
  (see the block above). Claude Code updated to **2.1.252** mid-experiment; the plan says 2.1.251 throughout.
  Cold-start hand-off written to `.ignored/handoff-15-to-implement-0831.md` for the incoming session
  `15-implement-brigade-0831`. **Nothing retained** — no pending edits, no unpushed work, no decisions outside the
  repo. The driver session `15-implement-brigade-0830` is hands-off from here and edits nothing further.
  Next task is **P1-1** (Opus): Go module scaffold, Makefile, lint, CI, plugin pins — the first code commit.
- 2026-08-31 ~16:30: **THE INTERACTIVE SITTING — E0-3 closed, E0-9 closed, E0-8 (b) closed.** Rjae observed at a real
  terminal; harness promoted to `scripts/experiments/sitting/` (shellcheck clean, re-verified through the real code
  path after promotion). **D19 = C** — see the section above. E0-9: `hold` shows a notice, does not deliver, raises
  no dialog, and **nothing expired in ~25 minutes** (the spec only asked for 5); `refuse` is completely silent to
  BOTH sides. E0-8 (b): an ask rule PROMPTS in `bypassPermissions` (not a denial) and **offers no sticky dismissal**,
  a deny rule BLOCKS with zero sends reaching the binary (verified by correlating pids), and the heredoc renders in
  full with a parsed description of the command. Six plan corrections came out of it — see the section above.
  Process note worth keeping: I told Rjae after step 4 that "no persistent-grant option exists", and step 6 showed
  that was **true only for ask-rule prompts** — default-approval prompts do offer one. The claim was right about the
  case measured and wrong as generalised; the correction is in `docs/experiments/E0-8.md` rather than buried.
  Also: my first version of the sitting harness was broken (step scripts passed paths relative to the caller's cwd
  while the launcher `cd`s into the project dir first), and my own smoke test missed it because it used `../`-relative
  paths from inside that dir — a test that exercised a different path shape than the real one. Fixed by absolutizing
  in the launcher, with existence checks and a clear error.
- 2026-08-31 ~19:00: **E0-7 done — all items pass; Phase 0's automatable work is complete.** Results in
  `docs/experiments/E0-7.md`, harness at `scripts/experiments/E0-7/`. Two concurrent `-p` sessions in ONE config dir,
  each selecting its profile through its own `--settings` `pluginConfigs` block: both registered under their own
  profile with no cross-writes, each `brigade sessions` saw the other (behind a `wait-peer` barrier proving genuine
  temporal overlap, not leftover registry files), and `brigade whoami` in each Bash tool named its own profile.
  Item 2 was proven properly for once: the hook mints a MAP NONCE written ONLY into the by-pid map, so a value
  `brigade` prints can only have been read from that file — plus two live negative controls (a poisoned env var is
  ignored; a missing map entry fails with exit 6). Profile resolution works as designed; the plan's "debug profile
  resolution" fallback is dead code. **A fresh `CLAUDE_CONFIG_DIR` does NOT inherit the login** (see the resolved
  open question above). Two things carried up as open items: the by-pid map is an unauthenticated trust boundary, and
  the strip list is short by three more names. Note for future stages: verification caught the measurement stage
  asserting a FALSE fact — that `.claude-ifthen` has no keychain credential item — which it disproved by re-deriving
  the keychain service name rather than by re-reading the prose. All runs were `claude -p`; the interactive
  two-profile path is unexercised.
- 2026-08-31 ~18:10: **E0-6 done — all six checks pass**, results in `docs/experiments/E0-6.md`, driver at
  `scripts/experiments/E0-6/` (plus `verify/`, which holds the corrected re-measurements and is the more trustworthy
  artifact). 30m28s soak at `jwt_expiry = 300` with three real OS processes on one `session.json`, the third under a
  real `sandbox-exec` profile that genuinely could not write (it persisted 0 times, yet still took `flock` through an
  `O_RDONLY` fd, so it honours the protocol). 19 refreshes, 0 `refresh_token_already_used`, 0 `PGRST303` across 1,888
  authenticated RPCs, 732,916 lock-free reads with 0 torn and mode 0600 every time. See "Plan corrections required
  before Phase 2" above — the two-behind rule in 5.1 is wrong, and the lock's 100 ms poll is the real cost.
  **`jwt_expiry` was set to 300 for this task only and has been reverted to 3600 and verified live**; the temporary
  change was deliberately kept out of every commit.
  Adversarial verification again found the decisive flaw: the soak's "7,897 acquisitions, all uncontended, max
  0.179 ms" proved NOTHING, because the 250 ms contention prober and the 1 s / 3 s refreshers have commensurate
  periods and started together, fixing the phase so the probe landed 106–133 ms after every acquisition and could
  never fall inside the ~45 ms hold. Zero contention was structurally guaranteed, not rare — and the author's own
  caveat blamed the wrong cause. Three further checks were being carried by instruments that could not fail: (b)
  rested on an untested premise about `PGRST303`, (c)'s torn-read detector had no positive control (and its real
  sample size was 8 rotations, not 732,916 reads), and (e) was instrumented client-side only, so it could not have
  detected a rejected `access_token` push at all.
- 2026-08-31 ~13:00: **E0-5 done — and it is the experiment that changes the most.** Results in
  `docs/experiments/E0-5.md`, harness at `scripts/experiments/E0-5/`. **Both of 6.6's watcher exit conditions are
  wrong as specified** — see "Plan corrections required before Phase 3" above; P3-5 must not be written against the
  current text. (a)(c)(d)(f)(h)(i) all pass. One anomaly is UNRESOLVED and carried forward: in two runs a `/clear`
  boundary moved the native session id while leaving NO `SessionStart(source=clear)` and NO `SessionEnd(reason=clear)`
  record; both benign explanations were killed by discriminator runs, so a hook that assumes it sees every boundary
  may miss one. Also unsettled: socket recreation was measured on `-p` only, the `/resume` boundary has n=1, and PID
  reuse inside one wall-clock second stays undetectable because `ps -o lstart=` is 1 s granular (six processes were
  observed sharing one token). Adversarial verification again found the decisive gap — (b) had no null control, so a
  watcher that simply self-terminated after ~30 s would have produced identical numbers; two 130 s null arms plus a
  code read of `detach.py` closed it. It also measured the author's own open item (socket recreation), and the answer
  made (b2) worse rather than better.
- 2026-08-31 ~07:00: **E0-4 done — the idle-wake criterion is MET**, results in `docs/experiments/E0-4.md`, driver at
  `scripts/experiments/E0-4/`. 12/12 delivered runs woke (7 `-p` stream-json, 5 interactive `expect`); worst latency
  6682 ms against the 10 s budget, interactive consistently faster than `-p`. **The figure 6.6 should budget against
  is the harness reaction, 1–7 ms enqueue→delivery in every run** — the seconds are model latency, not queue latency.
  Adversarial verification added the one control that was missing and mattered: a NULL-POST run (live idle session,
  socket present, nothing posted, 90 s observed) emitted nothing, which is what actually excludes "an idle session
  emits a turn on its own"; the two original controls could not, since in both the session had already exited with
  its socket unlinked. **The open stdin is the load-bearing condition** for a `-p` session to stay wakeable — a worker
  that closes stdin after its prompt cannot be reached (document in 6.11). Two reusable `expect` facts for P4-3: the
  trust dialog's highlighted default is "No, exit" so a bare Enter QUITS, and multi-word regexes never match because
  the box drawing interleaves cursor escapes — match a single word. Limits recorded honestly: one build/machine/
  account, all runs under `bypassPermissions`, and the longest proven idle is 120 s — the hours-long horizon (token
  expiry, socket reaping, staleness) is E0-5's. The agent modified `.claude.json` to force the trust dialog and
  reverted it surgically; independently verified afterwards that `projects[]` is back to its single original entry.
