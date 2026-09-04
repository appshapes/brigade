# 7. Repository layout and toolchain

Design record from 2026-08-30. Where this text and the code disagree, the code, docs/protocol-v1.md and the execution log are authoritative; the corrections block below lists what changed.

## Corrections recorded in the execution log

- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.2: `go get -tool -modfile=tools.mod …` does not work on a fresh checkout — `tools.mod` must be seeded first; the claim that `-modfile` leaves `go.mod` byte-identical IS true.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.2: `go.mod` needs an `ignore docs/research` directive, which the plan does not mention, or `go build ./...` and `go mod tidy -diff` both fail.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3/7.4: `gofmt -l .` is a FILESYSTEM walk while the other gates are MODULE-scoped; 19 nested-module files fail it, so the list must come from `go list`.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3: the `.golangci.yml` as written cannot be satisfied by any working program; the `os.Stdout`/`os.Exit` exemption must be scoped to entry-point FILES.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3: golangci-lint's default issue caps (`max-same-issues` 3, `max-issues-per-linter` 50) silently truncate the forbidigo report that IS the stdout/spawn discipline.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.7: the CI `fast` job as specified would be RED on its first push — it calls `make checksums-check` and `make plugin-check`, whose scripts are P1-8 deliverables.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.2: dependency versions have moved — `golang.org/x/text` v0.41.0, a new `x509roots/fallback` pseudo-version, `pgx/v5` v5.10.0.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3: the forbidigo exemption `path` values were UNANCHORED substrings, so a bare `cmd/` exempted any path merely containing it.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3: `issues.uniq-by-line` defaults to TRUE and drops all but one finding per source line, hiding forbidigo findings specifically.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.4: `make supabase-env` as specified truncates `.env.test` and renames away the names the promoted Phase 0 drivers read; it must write both name sets through a temp file.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3/7.4: `gofmt -l <dir>` RECURSES, so a directory-based fix reverts to a whole-tree walk once a package sits at the repository root; list FILES.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.4: `schema-check` and `deps-check` pipelines masked their own failures — the pipeline status came from `diff` alone.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.4: `make cross` did not neutralise `GOAMD64`, `GOARM64` or `GOFLAGS`, defeating the reproducibility criterion against `.goreleaser.yaml`'s own pins.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.7: the cross-host reproducibility criterion was not a GATE — nothing compared the two jobs' checksums until a `reproducibility` job was added to diff them.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.3: `UPDATE_SCRIPTS=1` in the ambient environment disarmed every byte-for-byte txtar golden; goldens update by the `-update` test FLAG only.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.7: `release.yml` did not pin the goreleaser version the Makefile pins, so the committed-checksum flow rested on whatever 2.x resolved at run time.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.2: the `go.mod` require block is the P2-6 end state, not P1-1's, so `deps-check` inspects zero modules and must say so rather than exiting 0 silently.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.4: `supabase-start`/`supabase-reset` need a `migrations-check`, and `unclaude` must strip the session environment BY PREFIX rather than by 7.4's eight-name list.
- 2026-08-31 — "Plan corrections from P1-1 (the first code commit)" (archive) — 7.x: the gates that are known-vacuous until their task lands — `deps-check`, the conformance step, the `depguard` harness rule, the mutant build tags, `govulncheck -mode binary`, and CI's gated `plugin-check`/`checksums-check`.
- 2026-08-31 — "Plan corrections from E0-8" (archive) — 7.x CI: `claude plugin validate --strict` does NOT detect a missing hook-command binary, so `scripts/ci/plugin-check.sh` must assert hook-command existence and the executable bit itself.
- 2026-09-02 — "Phase 1 corrections found on P1-5 entry (2026-09-02, session `15-implement-brigade-0902`)" (archive) — 7.3: `make lint` must run golangci-lint under `GOOS=darwin` AND `GOOS=linux` — CI was red for three commits on a `//go:build linux` file the local gate never loaded.

<!-- verbatim from the one-file plan -->
## 7. Repository layout and toolchain

### 7.1 Directory tree

```text
brigade/                                  git repo root = plugin marketplace root; branch master
  .claude-plugin/marketplace.json         {"name":"brigade","owner":{"name":"appshapes"},"plugins":[{"name":"brigade","source":"./plugin"}]}
  .claude/skills/{commit,playwright-cli}/ existing repo skills (unchanged)
  .context/plans/
    claude-code-team-messaging-logical-plan.md            (existing)
    claude-code-team-messaging-implementation-plan.md     this document
    brigade-proof-results.md                              Phase 4 results (P4-6)
  .github/workflows/ci.yml                fast (ubuntu), macos (go test only) and supabase (Docker) jobs (7.7)
  .github/workflows/release.yml           on tags v*: guard, goreleaser draft, verify checksums, publish (7.7)
  .golangci.yml                           7.3
  .goreleaser.yaml                        7.7
  .env.example                            documented variables only (7.5)
  .gitignore                              existing + additions (7.6)
  .ignored/                               experiment scratch (gitignored, repo rule)
  CLAUDE.md                               existing + additions (7.8)
  Makefile                                existing targets filled in + new targets (7.4)
  go.mod, go.sum                          module github.com/appshapes/brigade, `go 1.27.0`, no `toolchain` line (7.2)
  tools.mod, tools.sum                    dev tools only (govulncheck, goimports) driven by `go tool -modfile=tools.mod`
  cmd/
    brigade/main.go                       the one shipped binary; main() = app.Main()
    brigade/main_test.go                  testscript wiring: installs "brigade", "brigade-adapter-fs" and "fake-adapter" on PATH
    brigade/testdata/script/*.txtar       CLI, hook and adapter scenarios through argv/stdin/stdout/exit codes (9.1)
    brigade-adapter-fs/main.go            dev and test adapter (never shipped); main() = adapterfs.Main()
    brigade-conformance/main.go           C-01..C-43 runner for any adapter (never shipped); main() = conformance.CLI()
    brigade-schema/main.go                writes docs/protocol-v1.schema.json (invopop/jsonschema; never shipped)
  internal/
    app/                                  multi-call dispatch (commands | hook | watch | adapter supabase); Run(args, stdin, stdout, stderr, environ) int
    buildinfo/                            Version: ldflags -X, falling back to debug.ReadBuildInfo().Main.Version for `go install`
    cli/                                  command table, parseInterspersed, --json and human printers, usage, exit-code mapping
    protocol/                             wire types (json/v2 tags), constants, error taxonomy + exit map, Validate() per type,
                                          NDJSON reader/writer, sanitiser, join-secret parser; testdata/examples/*.json (the 4.4 examples)
    protocol/schema/                      JSON Schema generator (used by cmd/brigade-schema) + example-validation test (santhosh-tekuri)
    adapterkit/                           bounded stdin document, result printer, XDG dirs, atomic 0600 files, flock, redacting slog
                                          handler (adapterkit/log), profile file schema, TTY detection, child spawn helper
    adapters/fs/                          filesystem adapter store + polling watch; *_mutant.go behind build tags (dev and test only)
    adapters/supabase/                    client.go, gotrue.go, postgrest.go, realtime.go (Phoenix on coder/websocket), credentials.go,
                                          errors.go, watch.go, commands/*.go; *_integration_test.go (self-skipping)
    conformance/                          suite library: launcher, fixture, cases/c01_describe.go … c43_roster.go, report; suite_test.go, mutants_test.go
    corpus/                               corpus_test.go: the scripts/injection-corpus/ file names and expected.json agree (P0-1)
    harness/adapterclient/                spawn the adapter child (bundled: os.Executable() + "adapter supabase"; third-party: adapter_command),
                                          env allow-list, stdin JSON, 4 MiB stdout cap, timeouts, exit-code mapping, describe cache
    harness/bootstrap/                    bootstrap_test.go only: the sh bootstrap tested against an httptest release server (P1-8)
    harness/commands/                     brigade sessions | send | whoami | team … | profile … | inbox … (human output + --json)
    harness/config/                       option and by-pid-map resolution; the only harness package allowed to read the environment
    harness/frame/                        <brigade-message> builder (variants A/C) and parser; golden files
    harness/hook/                         session-start | prompt | session-end
    harness/inbound/                      dedupe, policy application, buckets, identical-body deferral, queue (pure; synctest-tested)
    harness/pidfile/                      O_EXCL pidfile, kill(pid,0), start-time token guard
    harness/policy/                       inbound policy (accept/refuse; hold arrives in Phase 5)
    harness/registry/                     $CLAUDE_CONFIG_DIR/sessions/<pid>.json best-effort reader (never the .key files)
    harness/sessionmap/                   by-pid and by-native maps
    harness/socketpost/                   Claude Code inbox socket client (pre-checks, auth line, deadlines)
    harness/watch/                        detached watcher: supervision, heartbeat, liveness, --sink; lifecycle tests
    procutil/                             detach (Setsid), start-time token (ps lstart on darwin, procfs on linux; build-tagged), signals
    testutil/                             fakesock, fakeregistry, fakeadapter, sleeper, dotenv, buildbin, tscmd (testscript commands), reporoot, runid
  plugin/                                 what --plugin-dir points at; nothing built lives here
    .claude-plugin/plugin.json            "version" pins the release tag (6.1)
    bin/brigade                           POSIX-sh bootstrap (6.2); on the Bash tool's PATH as `brigade`
    bin/VERSION                           the pinned version (0.0.0 before the first release)
    bin/checksums.txt                     goreleaser-format sha256 lines for the four binaries of the pinned version (empty before the first release)
    hooks/hooks.json                      exec form (6.3)
    skills/{team-messaging,setup}/SKILL.md
    README.md
  docs/
    protocol-v1.md                        the normative spec (section 4)
    protocol-v1.schema.json               generated from the Go types; drift-checked
    adapter-authors.md                    how to write and conformance-test an adapter (fs adapter as the worked example; txtar scripts as examples)
    allowed-deps.txt                      the modules bin/brigade may link (7.2); checked from `go version -m`
    experiments/E0-*.md                   Phase 0 reports (commands, raw observations, verdict)
    research/                             committed research inputs: the seven first-round digests with their SQL, scripts, skeleton and
                                          evidence (commit 6386046); the four second-round digests with their lab sources and evidence,
                                          and decisions-2026-08-30.md (P0-2)
    security.md                           user-facing threat summary (Phase 5)
    setup.md                              team admin (hosted) and member (join) guides (Phase 5)
  supabase/
    config.toml
    seed.sql                              empty
    migrations/20260830120000_brigade_schema.sql
    migrations/20260830120100_brigade_realtime.sql
    migrations/20260830120200_brigade_housekeeping.sql
    tests/helpers/auth.sql                pg_temp.login/logout/new_user/as_user
    tests/{rls_isolation,rls_stamping,rpc_join,rpc_send,rpc_sessions,realtime_policy,retention,hygiene,functions}.sql
    .gitignore                            .temp/, .branches/, .env (written by supabase init inside a git repo)
  scripts/
    proof.sh                              Phase 4 no-LLM proof (sink mode; runs in CI)
    proof-headless.sh                     Phase 4 LLM run (local only; POSIX sh + jq over --output-format stream-json)
    proof-idle-wake.sh                    Phase 4 idle-wake run (local only)
    harness-smoke.sh                      Phase 3 headless smoke with the fs adapter (local only)
    release-prep.sh                       the release sequence (7.7)
    experiments/E0-<n>/                   each Phase 0 driver, promoted here when its experiment closes (the R1 regression kit)
    injection-corpus/                     P0-1: <nn>-<slug>.txt bodies, <nn>-<slug>.summary.txt summary-only payloads, expected.json (9.6)
    ci/advisor-lints.sql                  Security Advisor lint mirrors
    ci/checksums-check.sh                 plugin/bin/{VERSION,checksums.txt} consistent with plugin.json, the source or the release (7.7)
    ci/release-verify.sh                  goreleaser's checksums.txt == plugin/bin/checksums.txt (hash columns)
    ci/plugin-check.sh                    exec-form hooks only, no .mcp.json, no channels, VERSION == plugin.json, bootstrap mode 100755 in git, shellcheck of the bootstrap
    ci/no-secrets.sh                      no sb_secret_/service_role/JWT-looking strings in plugin/, the source or the binaries
    ci/conformance-setup-supabase.sh      --setup hook for conformance(supabase): profile init per principal
```

Plugin files must live under `plugin/` because a plugin cannot reference paths outside its root and marketplace installs copy only that subtree [verified: https://code.claude.com/docs/en/plugins-reference]. The binaries `brigade-adapter-fs`, `brigade-conformance` and `brigade-schema` are built into `bin/` (gitignored) by `make build` and referenced by tests through `adapter_command` or `PATH`, never shipped. Files that use `syscall.SysProcAttr{Setsid}`, `syscall.Flock` or the termios ioctls carry `//go:build darwin || linux` (or `_darwin.go`/`_linux.go` names), so `go vet ./...` on a third OS fails loudly instead of compiling a broken binary (D33).

### 7.2 Go module and dependency policy

`go.mod` (versions read from the module proxy on 2026-08-30 by the Go digest; confirm with `go list -m -versions` on scaffold day):

```
module github.com/appshapes/brigade

go 1.27.0

require (
	github.com/coder/websocket v1.8.15
	golang.org/x/crypto/x509roots/fallback v0.0.0-20260826144058-afebf4cb4efb   // embedded Mozilla roots (5.1); see below
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0
	golang.org/x/text v0.39.0           // >= v0.39.0: GO-2026-5970 is fixed there (govulncheck flagged v0.14.0 in the lab)
)

// Development and test only: linked into cmd/brigade-schema and *_test.go, never into cmd/brigade
// (`make deps-check` asserts that from the built binary's `go version -m` output).
require (
	github.com/invopop/jsonschema v0.14.0
	github.com/jackc/pgx/v5 v5.x.y      // integration tests only: direct SQL for fixtures and backdating (9.4)
	github.com/rogpeppe/go-internal v1.16.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)
```

Rules [verified, A.7, unless marked]: `go 1.27.0` is the minimum and language version; no `toolchain` line, because an explicit `toolchain go1.27.0` next to `go 1.27.0` makes every `go build` fail with "updates to go.mod needed", and the implicit toolchain is the `go` line anyway. `GOTOOLCHAIN=auto` (the default) is NOT enough for the reproducible recipes: `auto` uses the locally installed toolchain whenever it is "at least as new as the `go` or `toolchain` lines" [verified today: https://go.dev/doc/toolchain], so a developer on go1.27.1 would build different bytes than CI's 1.27.0 and `checksums-check` rule (c) would go red on the release commit. The Makefile therefore computes `go_toolchain := go$(shell sed -n 's/^go //p' go.mod)` and sets `GOTOOLCHAIN=$(go_toolchain)` on the `build` and `cross` recipes, `release-prep.sh` sets it on its goreleaser invocation and asserts `go env GOVERSION` under it, and everything else (tests, vet, tools) stays on `auto`; Go downloads the pinned toolchain if it is absent. CI pins through `actions/setup-go` with `go-version-file: go.mod`. Bump the `go` line only in its own commit and only to a released version. Go 1.27 requires macOS 13 or later (the darwin binaries carry `minos 13.0`); Linux needs any x86-64/arm64 with a CA bundle (or the embedded roots do the job, 5.1). `go mod tidy` on a `go 1.27` module merges `require` blocks [verified: https://go.dev/doc/go1.27], so the dev-only distinction above may not survive a tidy; the authority is `docs/allowed-deps.txt`, which lists exactly `github.com/coder/websocket`, `golang.org/x/crypto/x509roots/fallback`, `golang.org/x/sys`, `golang.org/x/term`, `golang.org/x/text` (five modules), and `make deps-check` checks it against the `dep` lines of `go version -m bin/brigade` (a subset test until P2-6, byte equality after; 7.4). Why each shipped dependency: `coder/websocket` for the Phoenix client (zero dependencies, autobahn-tested, maintained); `x/term` for `team join --prompt` (`ReadPassword`, `IsTerminal`) and `x/sys` because `x/term` requires it; `x/text/unicode/norm` for the sanitiser's NFC step (no NFC in the standard library); `golang.org/x/crypto/x509roots/fallback` for the embedded roots — and note it is its own nested module, not a package of `golang.org/x/crypto`: requiring `golang.org/x/crypto vX` would not provide it, it has only pseudo-versions (no semver tags), and the built binary's `go version -m` lists the fallback module and never `golang.org/x/crypto` itself [verified today: `go get golang.org/x/crypto/x509roots/fallback@latest` → `v0.0.0-20260826144058-afebf4cb4efb`; `go list -m -versions` prints no tags; a scratch build's only `dep` line is the fallback module]. The pseudo-version's date is the date of the Mozilla bundle; bump it with `go get golang.org/x/crypto/x509roots/fallback@latest` as part of release prep when a new bundle ships. Everything else (`syscall.Flock`, `syscall.Kill`, `SysProcAttr.Setsid`, `os/exec` cancel and wait-delay, `log/slog`, `encoding/json/v2`) is standard library. Dev tools never enter `go.mod`: `go get -tool -modfile=tools.mod golang.org/x/vuln/cmd/govulncheck@v1.7.0 golang.org/x/tools/cmd/goimports@v0.49.0` writes a `tool` block into `tools.mod`/`tools.sum` and leaves `go.mod` byte-identical, and `go tool -modfile=tools.mod govulncheck ./...` analyses the main module from the repository root; without `-modfile`, `go get -tool` pulls `x/tools`, `x/vuln`, `x/telemetry`, `x/mod` and `x/sync` into the main graph. golangci-lint is a pinned binary installed by its `install.sh` (its docs say `go install`/`go tool` installations "aren't guaranteed to work"); goreleaser is a pinned release binary needed only for the release rehearsal. No cobra: stdlib `flag` plus the 20-line interspersed-flag helper of 7.3 covers a 25-entry command table, and cobra's defaults (usage on stdout, suggestions, `help`/`completion` subcommands, exit 1 on unknown commands) would all have to be switched off to satisfy the frozen core (unknown flags are `usage` exit 2 with JSON on stdout; nothing but the result is ever written to stdout).

### 7.3 Build, test and lint conventions

- Dispatch (`internal/cli`): every `flag.FlagSet` is `ContinueOnError` with `SetOutput(io.Discard)`; `flag.ErrHelp` prints usage to stdout and exits 0 for human commands; any other parse error is `usage` (exit 2) with the JSON error on stdout when `--json` or when the caller is the harness, and a one-line message on stderr otherwise; `--json` and `--log-level` are global; secrets are never flags (`--join-secret` is registered as a poison flag whose presence returns `usage`, C-05). `parseInterspersed` lets flags follow positionals (`brigade send <session_id> --reply-to <id>`), because stdlib `flag` stops at the first non-flag argument [verified, A.7]:

  ```go
  func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, err error) {
  	var tail []string
  	if i := slices.Index(args, "--"); i >= 0 {
  		tail, args = args[i+1:], args[:i]
  	}
  	for {
  		if err := fs.Parse(args); err != nil {
  			return nil, err // flag.ErrHelp or a usage error; the caller maps to help / exit 2
  		}
  		rest := fs.Args()
  		if len(rest) == 0 {
  			return append(positional, tail...), nil
  		}
  		positional = append(positional, rest[0])
  		args = rest[1:]
  	}
  }
  ```

- JSON: `encoding/json/v2` for every wire shape (generally available in Go 1.27 without `GOEXPERIMENT` [verified: https://go.dev/doc/go1.27]). Unknown members are ignored by default (D16), duplicate member names and invalid UTF-8 are rejected, member names are case-sensitive and a wrongly-cased member is silently zero, so every wire type has a `Validate()` that checks required fields, byte caps (`len`), code-point caps (`utf8.RuneCountInString`) and, for `SendRequest`, that the forbidden sender members (declared as `jsontext.Value` with `omitzero`) are empty (C-23). Never `RejectUnknownMembers`, never `MatchCaseInsensitiveNames`. The stdin document is read through `io.LimitReader(stdin, 1<<20+1)` and refused past 1 MiB. Struct tags `json:"name,omitzero"`; pointers where null and absent must differ; `time.Time` marshals RFC 3339. Tests never assert on Go's JSON error text (it changed in 1.27 and may change again), only on the mapped `invalid_input`.
- NDJSON: one reader for the watcher, the conformance suite and `message watch`'s stdin commands, built on `bufio.Reader.ReadSlice('\n')`: lines up to 1 MiB are delivered, a longer line is marked, discarded to its end with a warning, and reading continues (a `bufio.Scanner` stops permanently with `ErrTooLong` [verified, A.7]). The writer is `jsonv2.Marshal` plus `\n` behind a mutex.
- Processes: adapters are spawned with `exec.CommandContext`, `cmd.Cancel` sending SIGTERM, `cmd.WaitDelay` (then SIGKILL and pipe closure), argv only, `Env` built from scratch, stdin from a `bytes.Reader`, stdout into a capped writer that cancels the context at 4 MiB; a timeout is detected with `ctx.Err()`; `exec.ErrWaitDelay` with a parsed result and exit 0 is logged and treated as success (an adapter must not hand its stdout to a grandchild). The watcher is detached with `Setsid` (6.6).
- Logging: one redacting `slog` JSON handler (`internal/adapterkit/log`) for adapters and harness; scalars only; `slog.Any` banned outside that package by `forbidigo`.
- stdout discipline: nothing under `internal/` writes to `os.Stdout` except `adapterkit.PrintResult`, the NDJSON event writer and `harness/commands` (`forbidigo`); `os.Exit` only in `main` packages and the `Main` wrappers; `os.Getenv` only in `harness/config`, `adapterkit/env.go` and tests (the static half of the environment-isolation rule of 3.2, U-27); `exec.Command` only through `harness/adapterclient`, `adapterkit.Spawn` and `testutil`.
- Tests: `go test -race -shuffle=on -count=1 ./...`; `t.Parallel()` on every test that does not call `t.Setenv`/`t.Chdir` (Go panics otherwise); child environments passed explicitly (`testutil.Env`), never through the test process, so tests stay parallel and never touch the developer's real `CLAUDE_CONFIG_DIR` (`/Users/rjae/.claude-ifthen` here); `testing/synctest` for every time-based behaviour (buckets, deferral windows, backoff, lease schedulers; stable since Go 1.25, `synctest.Sleep` in 1.27 [verified]); real sockets and files stay outside the bubble; unix sockets under `os.MkdirTemp("/tmp", "bsk")`, never `t.TempDir()` (macOS caps `sun_path` at 103 bytes and `t.TempDir()` is already about 91 here [verified, A.7]); `-race` never combined with `CGO_ENABLED=0` (the race detector requires cgo; the darwin host tolerated it today but a Linux cross-build does not [verified, A.7]); goldens with `-update`; fuzz seeds from the P0-1 corpus.
- Formatting and lint: golangci-lint v2.13.2 pinned locally and in CI through the same `install.sh` into `./bin` (`make setup-lint`); the `golangci/golangci-lint-action` is deliberately not used, so local and CI run one pinned binary; never `linters.default: all` (its docs warn it breaks on minor upgrades). `go vet ./...` includes `stdversion` (part of `go vet`'s suite since Go 1.23); what Go 1.27 changed is that `go test` now runs the `stdversion` check by default [verified today: https://go.dev/doc/go1.27], so a too-new standard-library symbol fails `make test` even before `make lint`. `go vet` runs as a separate one-second step so a failure is legible. `gofmt -l .` is the zero-config fallback. Config:

  ```yaml
  # .golangci.yml
  version: "2"
  run:
    timeout: 5m
    tests: true
    build-tags: [mutant_noack, mutant_teamleak, mutant_trustsender]   # lint the mutant files too (9.2)
  linters:
    default: standard            # errcheck, govet, ineffassign, staticcheck, unused
    enable:
      - bodyclose                # http.Response bodies (the Supabase client)
      - copyloopvar
      - depguard                 # the harness never imports the Supabase client
      - errorlint                # errors.Is/As instead of == and type assertions
      - exhaustive               # switch over protocol.Code and event kinds
      - forbidigo                # stdout, exit, environment and spawn discipline
      - gocritic
      - gosec                    # file modes, subprocess audit, TLS, weak RNG
      - misspell
      - nilerr
      - noctx                    # every HTTP request carries a context
      - perfsprint
      - revive
      - thelper
      - tparallel
      - unconvert
      - unparam
      - usestdlibvars
      - usetesting               # t.TempDir, t.Context instead of os.* in tests
    settings:
      gosec:
        excludes: [G304]         # file paths are computed from BRIGADE_CONFIG_DIR/BRIGADE_STATE_DIR by design
        config:
          G301: "0700"           # directories: profiles/ and state dirs are 0700 (T7, T14)
          G302: "0600"           # chmod/create
          G306: "0600"           # WriteFile: every state, profile, credential, map and log file is 0600
      forbidigo:
        analyze-types: true
        forbid:
          - pattern: ^fmt\.Print(f|ln)?$
            msg: stdout is protocol output only; use adapterkit.PrintResult, the NDJSON writer, or harness/commands
          - pattern: ^os\.Stdout$
            msg: same as above
          - pattern: ^os\.Exit$
            msg: return an exit code from Run; only Main may exit (stdout would be truncated)
          - pattern: ^os\.Getenv$
            msg: read configuration through harness/config or adapterkit/env (inherited BRIGADE_* is ignored on purpose, plan 3.2)
          - pattern: ^exec\.Command(Context)?$
            msg: spawn through harness/adapterclient or adapterkit.Spawn (argv arrays, allow-listed env, no shell)
          - pattern: ^slog\.Any$
            msg: ReplaceAttr cannot redact inside struct/map values; log scalars or use adapterkit/log helpers
      depguard:
        rules:
          harness-is-adapter-agnostic:
            files: ["**/internal/harness/**"]
            deny:
              - pkg: github.com/appshapes/brigade/internal/adapters/supabase
                desc: the plugin talks only the adapter protocol; the Supabase adapter is a child process
    exclusions:
      generated: lax
      presets: [comments, std-error-handling]
      rules:
        - path: internal/adapterkit/result\.go|internal/protocol/ndjson\.go|internal/harness/commands/
          linters: [forbidigo]
          text: "fmt.Print|os.Stdout"
        - path: internal/app/main\.go|internal/adapters/fs/main\.go|internal/conformance/cli\.go|cmd/
          linters: [forbidigo]
          text: "os.Exit"
        - path: internal/harness/config/|internal/adapterkit/env\.go|internal/testutil/|_test\.go
          linters: [forbidigo]
          text: "os.Getenv|exec.Command"
        - path: internal/harness/adapterclient/spawn\.go|internal/adapterkit/spawn\.go|internal/procutil/|internal/testutil/
          linters: [gosec]
          text: "G204"           # subprocess with variable argv is the design (4.1); no shell is ever involved
        - path: internal/adapterkit/log/
          linters: [forbidigo]
          text: "slog.Any"
  formatters:
    enable: [gofmt, goimports]
    settings:
      goimports:
        local-prefixes: [github.com/appshapes/brigade]
  ```

  The config keys, the standard set and the availability of every named linter were verified against today's docs; the exact `forbidigo`/`depguard` settings syntax is [likely] (unchanged from v1 apart from nesting) and `golangci-lint config verify` runs first in `make lint`.
- Vulnerabilities: `govulncheck` in source mode on `./...` and in binary mode on `bin/brigade` (both through `tools.mod`); it exits non-zero only for a reachable vulnerability (JSON/SARIF modes always exit 0) [verified]; `go mod verify` and `go mod tidy -diff` are the lockfile discipline (CI-01/CI-03).
- JSON Schema: `internal/protocol/schema` reflects the wire structs with `invopop/jsonschema` (`AllowAdditionalProperties: true` for loose objects, `RequiredFromJSONSchemaTags: true`, one document with `$defs`, a `false` property schema for each forbidden `SendRequest` member); `make schema` writes `docs/protocol-v1.schema.json`, `make schema-check` diffs it in CI; a unit test validates every example in `internal/protocol/testdata/examples/` against the committed schema with `santhosh-tekuri/jsonschema/v6` and checks that the C-23 shapes fail. The byte cap on `body` cannot be expressed in JSON Schema (`maxLength` counts code points); the description says so and `Validate()` enforces bytes. Whether the generator's output is byte-stable across machines is [likely] (ordered maps); the first two CI runs show it, and a canonical post-processor is the fallback.

### 7.4 Makefile

The seeded conventions are kept (lowercase variables, per-target `.PHONY`, `##` doc comments scraped by `help`, `commit` = `typecheck pull build test`, `push` = `commit` + `git push`). `test` stays Docker-free so `make push` never needs the local stack (D29). One seeded recipe changes: `pull` becomes `git pull --no-edit`, the form the user's global `CLAUDE.md` prescribes (the seeded bare `git pull` would open an editor for a merge commit in a TTY). `ld_flags` uses `=` (not `:=`) so a `version=` passed on the command line reaches sub-makes. macOS ships no `timeout(1)`, so no recipe relies on it [verified, A.7]. Full recipes (the existing `help`, `commit`, `push` and `docker-*` targets are unchanged and omitted here):

```make
# ========== Variables (alphabetical) ==========

bin_dir            := bin
detach             := --detach
dist_cross         := dist-cross
env_test           := .env.test
go_flags           := -trimpath -buildvcs=false
go_test_flags      := -race -shuffle=on -count=1 -timeout 15m
go_toolchain       := go$(shell sed -n 's/^go //p' go.mod)
golangci_lint      := $(bin_dir)/golangci-lint
golangci_version   := v2.13.2
goreleaser         := $(bin_dir)/goreleaser
goreleaser_version := v2.18.0
ld_flags            = -s -w -X github.com/appshapes/brigade/internal/buildinfo.Version=$(version)
ld_flags_dev        = -s -w -X github.com/appshapes/brigade/internal/buildinfo.Version=$(version)-dev
plugin_json        := plugin/.claude-plugin/plugin.json
plugin_version     := $(shell cat plugin/bin/VERSION)
sha256              = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)
supabase           ?= npx --yes supabase@2.116.0
supabase_exclude   := studio,postgres-meta,imgproxy,storage-api,edge-runtime,mailpit,logflare,vector,supavisor
tag                := $(shell git log -1 --pretty=format:"%H")
targets            := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
tools_mod          := tools.mod
# Strip the outer Claude Code session's variables from targets that run proof scripts or launch claude:
# inherited CLAUDE_PID/CLAUDE_CODE_MESSAGING_* would make the harness ignore the scripts' BRIGADE_* values,
# refuse --sink, and leak the outer session's socket into nested `claude -p` runs (9.6). CLAUDE_CONFIG_DIR stays.
unclaude           := env -u CLAUDECODE -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_ENTRYPOINT -u CLAUDE_CODE_EXECPATH -u CLAUDE_CODE_MESSAGING_SOCKET -u CLAUDE_CODE_MESSAGING_TOKEN -u CLAUDE_CODE_SESSION_ID -u CLAUDE_PID
version            ?= $(plugin_version)

# ========== Setup ==========

.PHONY: setup
setup: ## go mod download, pinned golangci-lint and goreleaser into ./bin, dev tools, .env scaffold, docker check, push.autoSetupRemote
	go mod download
	go mod download -modfile=$(tools_mod)
	mkdir -p $(bin_dir)
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(bin_dir) $(golangci_version)
	$(MAKE) setup-goreleaser
	@command -v shellcheck >/dev/null || { [ "$$(uname -s)" = Darwin ] && brew install shellcheck || echo "install shellcheck for make plugin-check (CI enforces it)"; }
	@cp -n .env.example .env || true
	docker version >/dev/null
	git config push.autoSetupRemote true

.PHONY: setup-lint
setup-lint: ## Install only the pinned golangci-lint into ./bin (what CI runs; no Docker, no goreleaser)
	mkdir -p $(bin_dir)
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(bin_dir) $(golangci_version)

.PHONY: setup-goreleaser
setup-goreleaser: ## Download the pinned goreleaser release binary into ./bin (release rehearsal only)
	gh release download $(goreleaser_version) --repo goreleaser/goreleaser --pattern "goreleaser_$$(uname -s)_$$(uname -m | sed 's/aarch64/arm64/').tar.gz" --dir $(bin_dir) --clobber
	tar -xzf $(bin_dir)/goreleaser_*.tar.gz -C $(bin_dir) goreleaser && rm -f $(bin_dir)/goreleaser_*.tar.gz

# ========== Git ==========

.PHONY: pull
pull: ## Merge origin into the current branch (plain merge, never rebase; no editor)
	git pull --no-edit

# ========== Build / Test / Lint ==========

.PHONY: build
build: ## Build bin/brigade plus the dev-only binaries for this host with the release flags (version stamped `<version>-dev`)
	GOTOOLCHAIN=$(go_toolchain) CGO_ENABLED=0 go build $(go_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade ./cmd/brigade
	GOTOOLCHAIN=$(go_toolchain) CGO_ENABLED=0 go build $(go_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade-adapter-fs ./cmd/brigade-adapter-fs
	GOTOOLCHAIN=$(go_toolchain) CGO_ENABLED=0 go build $(go_flags) -ldflags '$(ld_flags_dev)' -o $(bin_dir)/brigade-conformance ./cmd/brigade-conformance

.PHONY: clean
clean: ## Remove build artifacts and local caches
	rm -rf $(bin_dir)/brigade $(bin_dir)/brigade-adapter-fs $(bin_dir)/brigade-conformance dist $(dist_cross) cover.out $(env_test) playwright-report test-results

.PHONY: typecheck
typecheck: ## go build ./... and go vet ./... (every package, including tests)
	go build ./...
	go vet ./...

.PHONY: fmt
fmt: ## gofmt + goimports through golangci-lint
	$(golangci_lint) fmt ./...

.PHONY: lint
lint: ## golangci-lint (config verify + run, formatters included) and a plain gofmt check
	$(golangci_lint) config verify
	test -z "$$(gofmt -l .)" || (gofmt -l .; exit 1)
	$(golangci_lint) run ./...

.PHONY: lint-fix
lint-fix: ## golangci-lint --fix and fmt
	$(golangci_lint) run --fix ./...
	$(golangci_lint) fmt ./...

.PHONY: test
test: build ## Unit + testscript + harness + conformance(fs) with -race; no Docker (what `make commit` runs)
	go test $(go_test_flags) -covermode=atomic -coverprofile=cover.out ./...
	$(bin_dir)/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter $(bin_dir)/brigade-adapter-fs

.PHONY: vuln
vuln: build ## govulncheck on the source tree and on the built binary (pinned in tools.mod)
	go tool -modfile=$(tools_mod) govulncheck ./...
	go tool -modfile=$(tools_mod) govulncheck -mode binary $(bin_dir)/brigade

.PHONY: tidy-check
tidy-check: ## Fail if go.mod/go.sum would change (CI)
	go mod tidy -diff
	go mod verify

.PHONY: deps-check
deps-check: build ## Fail if bin/brigade links a module outside docs/allowed-deps.txt (subset test; P2-6 appends the equality line)
	go version -m $(bin_dir)/brigade | awk '$$1 == "dep" {print $$2}' | sort > $(bin_dir)/deps.txt
	! grep -vxF -f docs/allowed-deps.txt $(bin_dir)/deps.txt
# P2-6, once every shipped module is actually linked, adds:  diff $(bin_dir)/deps.txt docs/allowed-deps.txt

.PHONY: schema
schema: ## Regenerate docs/protocol-v1.schema.json from the Go wire types
	go run ./cmd/brigade-schema > docs/protocol-v1.schema.json

.PHONY: schema-check
schema-check: ## Fail if docs/protocol-v1.schema.json is stale (CI)
	go run ./cmd/brigade-schema | diff - docs/protocol-v1.schema.json

.PHONY: test-db
test-db: ## pgTAP tests in supabase/tests against the running local stack
	$(supabase) test db

.PHONY: test-integration
test-integration: build ## Adapter integration + conformance(supabase) against the local stack (reads $(env_test))
	set -a; . ./$(env_test); set +a; BRIGADE_TEST_DOCKER=1 go test -count=1 -timeout 20m -run 'Integration|Supabase' ./internal/adapters/supabase/...
	set -a; . ./$(env_test); set +a; $(bin_dir)/brigade-conformance --slow --env SUPABASE_URL=$$SUPABASE_URL --env SUPABASE_PUBLISHABLE_KEY=$$SUPABASE_PUBLISHABLE_KEY --setup scripts/ci/conformance-setup-supabase.sh --adapter $(bin_dir)/brigade -- adapter supabase

.PHONY: test-all
test-all: test test-db advisor-lints test-integration e2e ## Everything (requires `make supabase-start supabase-env`)

.PHONY: conformance
conformance: build ## Run the conformance suite against an adapter (usage: make conformance adapter=<executable> [args="-- fixed args"])
	$(bin_dir)/brigade-conformance --adapter $(adapter) $(args)

.PHONY: e2e
e2e: build ## Phase 4 no-LLM proof in watcher sink mode against the local stack (runs in CI)
	$(unclaude) scripts/proof.sh

.PHONY: proof
proof: e2e ## Phase 4 proof including the headless LLM run and the idle-wake run (needs a logged-in claude)
	$(unclaude) scripts/proof-headless.sh && $(unclaude) scripts/proof-idle-wake.sh

.PHONY: harness-smoke
harness-smoke: build ## Headless claude -p smoke test with the fs adapter (needs a logged-in claude)
	$(unclaude) scripts/harness-smoke.sh

.PHONY: advisor-lints
advisor-lints: ## Security Advisor lint mirrors, run with the psql inside the local database container (no host psql needed)
	docker exec -i supabase_db_brigade psql -U postgres -d postgres -v ON_ERROR_STOP=1 < scripts/ci/advisor-lints.sql

# ========== Plugin ==========

.PHONY: plugin-check
plugin-check: ## Static checks of plugin/: exec-form hooks, no .mcp.json, VERSION == plugin.json, shellcheck, no secrets
	scripts/ci/plugin-check.sh
	scripts/ci/no-secrets.sh

.PHONY: plugin-validate
plugin-validate: ## claude plugin validate on the plugin root and the marketplace
	claude plugin validate ./plugin --strict && claude plugin validate .

.PHONY: plugin-dev-pointer
plugin-dev-pointer: build ## Write the dev-binary pointer (honours XDG_CONFIG_HOME) without launching anything; used by scripts too
	mkdir -p "$${XDG_CONFIG_HOME:-$$HOME/.config}/brigade"
	echo "$(CURDIR)/$(bin_dir)/brigade" > "$${XDG_CONFIG_HOME:-$$HOME/.config}/brigade/dev-binary"

.PHONY: plugin-dev
plugin-dev: plugin-dev-pointer ## Start Claude Code with the local plugin (usage: make plugin-dev [adapter=fs] — fs selects the dev adapter via adapter_command)
ifeq ($(adapter),fs)
	$(unclaude) claude --plugin-dir ./plugin --settings '{"pluginConfigs":{"brigade@inline":{"options":{"adapter_command":"[\"$(CURDIR)/$(bin_dir)/brigade-adapter-fs\"]"}}}}'
else
	$(unclaude) claude --plugin-dir ./plugin
endif

.PHONY: plugin-dev-off
plugin-dev-off: ## Remove the local-build pointer so the bootstrap uses the pinned release again
	rm -f "$${XDG_CONFIG_HOME:-$$HOME/.config}/brigade/dev-binary"

# ========== Release ==========

.PHONY: print-version
print-version: ## Print the version pinned in plugin/bin/VERSION
	@echo $(plugin_version)

.PHONY: cross
cross: ## Build dist-cross/brigade_$(version)_<os>_<arch> for every target with the exact release flags, plus checksums.txt
	rm -rf $(dist_cross) && mkdir -p $(dist_cross)
	for t in $(targets); do \
	  GOTOOLCHAIN=$(go_toolchain) CGO_ENABLED=0 GOOS=$${t%/*} GOARCH=$${t#*/} go build $(go_flags) -ldflags '$(ld_flags)' \
	    -o $(dist_cross)/brigade_$(version)_$${t%/*}_$${t#*/} ./cmd/brigade || exit 1; \
	done
	cd $(dist_cross) && $(sha256) brigade_* > checksums.txt

.PHONY: checksums-check
checksums-check: cross ## Fail if plugin/bin/{VERSION,checksums.txt} disagree with plugin.json, a fresh build, or the published release (CI)
	scripts/ci/checksums-check.sh $(dist_cross)/checksums.txt

.PHONY: release
release: ## Pin the plugin to $(version), commit through the push chain, tag v$(version) and push the tag (usage: make release version=0.1.0 [branch=<throwaway>] — branch only for the P2-12 rehearsal)
	@test -n "$(version)" || { echo "usage: make release version=X.Y.Z [branch=<name>]"; exit 1; }
	scripts/release-prep.sh $(version) $(branch)

.PHONY: release-dry-run
release-dry-run: ## goreleaser check + a local release without publishing (needs a clean tree and a tag on HEAD)
	$(goreleaser) check
	GOTOOLCHAIN=$(go_toolchain) $(goreleaser) release --skip=publish --clean

# ========== Supabase (local stack) ==========

.PHONY: supabase-start
supabase-start: ## Start the minimal local stack (db, auth, rest, realtime, kong); applies migrations + seed
	$(supabase) start -x $(supabase_exclude)

.PHONY: supabase-stop
supabase-stop: ## Stop the local stack, keep data
	$(supabase) stop

.PHONY: supabase-clean
supabase-clean: ## Stop the local stack and delete its data
	$(supabase) stop --no-backup

.PHONY: supabase-status
supabase-status: ## Show URLs and keys of the running stack
	$(supabase) status

.PHONY: supabase-env
supabase-env: ## Write $(env_test) from the running stack (never commit it)
	$(supabase) status -o env \
	  --override-name api.url=SUPABASE_URL \
	  --override-name auth.publishable_key=SUPABASE_PUBLISHABLE_KEY \
	  --override-name auth.secret_key=SUPABASE_SECRET_KEY \
	  --override-name auth.service_role_key=SUPABASE_SERVICE_ROLE_KEY \
	  --override-name db.url=SUPABASE_DB_URL > $(env_test)

.PHONY: supabase-reset
supabase-reset: ## Recreate the local database from migrations + seed
	$(supabase) db reset

.PHONY: migration-new
migration-new: ## Create supabase/migrations/<timestamp>_$(name).sql (usage: make migration-new name=add_x)
	$(supabase) migration new $(name)

# ========== Supabase (hosted project; needs SUPABASE_ACCESS_TOKEN) ==========

.PHONY: supabase-link
supabase-link: ## Link a hosted project (usage: make supabase-link project=<ref>)
	$(supabase) link --project-ref $(project)

.PHONY: supabase-push-dry
supabase-push-dry: ## Show migrations that would be applied to the linked project
	$(supabase) db push --dry-run

.PHONY: supabase-push
supabase-push: ## Apply migrations to the linked project
	$(supabase) db push

.PHONY: supabase-config-push
supabase-config-push: ## Push config.toml settings (anonymous sign-ins, exposed schemas) to the linked project
	$(supabase) config push

.PHONY: backend-install
backend-install: supabase-link supabase-push supabase-config-push ## One-shot hosted backend setup for a team admin (Phase 5)
	$(supabase) projects api-keys --project-ref $(project)
```

`make test` builds first, as the seeded chain expects; `-shuffle=on` prints the seed, and a failure is replayed with `go test -shuffle=<seed> -run <name> ./pkg`. The conformance suite runs twice on `make test` (as `go test` subtests and as the binary adapter authors use); the five extra seconds keep `go test ./...` a complete gate on its own.

### 7.5 `.env.example`

```dotenv
# Local Supabase stack. `make supabase-env` writes the real values to .env.test (gitignored).
# The local keys are well-known development constants; never paste hosted keys here.
SUPABASE_URL=http://127.0.0.1:54321
SUPABASE_PUBLISHABLE_KEY=
# Admin-only, LOCAL stack only; never a hosted value; never shipped in the adapter or plugin. These keys have no
# privileges on brigade.* (5.3); they are used only for auth admin calls in local tests (principal cleanup), if E0-1 (g)
# shows they are needed. Database fixtures use SUPABASE_DB_URL as postgres with simulated claims (9.3).
SUPABASE_SECRET_KEY=
SUPABASE_SERVICE_ROLE_KEY=
SUPABASE_DB_URL=postgresql://postgres:postgres@127.0.0.1:54322/postgres
# Hosted project operations (maintainers/CI only; keep out of the repo)
SUPABASE_ACCESS_TOKEN=
SUPABASE_PROJECT_ID=
SUPABASE_DB_PASSWORD=
# Brigade runtime overrides for a human terminal, CI and containers (ignored inside a Claude Code session, plan 3.2)
BRIGADE_CONFIG_DIR=
BRIGADE_STATE_DIR=
BRIGADE_PROFILE=default
BRIGADE_LOG_LEVEL=info
# Filesystem adapter root (tests only)
BRIGADE_FS_ROOT=
# Bootstrap: alternative release base for mirrors and the bootstrap test server (the committed sha256 is the trust anchor)
BRIGADE_RELEASE_BASE_URL=
```

Runtime profile data (project URL, publishable key, team ref, refresh token) is per-user and never lives in repo `.env` files. Go has no `--env-file` flag: the Makefile sources `.env.test` (`set -a; . ./.env.test; set +a`) and the integration tests read it themselves through a 30-line parser that fills only unset variables (9.4).

### 7.6 `.gitignore` additions

```gitignore
# Brigade
.env
.env.*
!.env.example
/bin/
/dist/
/dist-cross/
cover.out
supabase/.temp/
supabase/.branches/
supabase/.env

# Committed research evidence must never be swallowed by the seeded Node template's broad patterns
# (`logs`, `out`, `dist` and `*.out` match at any depth [verified today: git check-ignore under docs/]).
!docs/research/**/logs/
!docs/research/**/logs/**
!docs/research/**/out/
!docs/research/**/out/**
!docs/research/**/dist/
!docs/research/**/dist/**
!docs/research/**/*.out
```

The seeded file already ignores `node_modules/`, `.ignored/`, `CLAUDE.user.md`, `*.log` (which is why the research evidence logs are committed as `*.log.txt`) and `*.test` (which also covers `go test -c` binaries), and its bare `dist` pattern covers any nested `dist` as well; the seeded `logs`/`out` patterns likewise match anywhere, which is why P0-2 lands the request/response logs under `evidence/` directories and the negations above are belt and braces (`git status --ignored docs/research` must list nothing, P0-2). Nothing built is committed any more (D35), so the former `!plugin/dist/` negation is gone; `scripts/ci/plugin-check.sh` asserts that `plugin/` contains no file other than the manifests, the bootstrap, `VERSION`, `checksums.txt`, the hooks, the skills and the README.

### 7.7 CI and release

`.github/workflows/ci.yml`:

```yaml
name: ci
on:
  push: {branches: [master]}
  pull_request: {}
permissions: {contents: read}
concurrency: {group: "ci-${{ github.ref }}", cancel-in-progress: true}

jobs:
  fast:                                   # no Docker; target < 5 minutes
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with: {fetch-depth: 0}            # checksums-check needs tags and the release listing
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod         # 1.27.0 from the `go` line (no toolchain line, 7.2)
          cache-dependency-path: |
            go.sum
            tools.sum
      - run: make setup-lint              # golangci-lint via install.sh into ./bin (pinned)
      - run: make typecheck tidy-check
      - run: make lint
      - run: make build test              # unit + testscript + harness + conformance(fs), -race, coverage profile
      - run: go tool cover -func=cover.out | tail -1 >> "$GITHUB_STEP_SUMMARY"
      - run: make vuln deps-check schema-check
      - run: make checksums-check         # all four targets rebuilt; plugin/bin pins consistent (below)
        env: {GH_TOKEN: "${{ github.token }}"}
      - run: cat dist-cross/checksums.txt >> "$GITHUB_STEP_SUMMARY"   # cross-host reproducibility evidence (P1-1)
      - run: make plugin-check            # exec-form hooks only, no .mcp.json, no channels, VERSION == plugin.json, shellcheck, no secrets

  macos:                                  # the primary user platform: unix sockets, ps -o lstart, shasum paths
    runs-on: macos-latest                 # macOS 26 arm64 today [verified]
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with: {go-version-file: go.mod}
      - run: make build && go test -race -shuffle=on -count=1 -timeout 15m ./...
      - run: make cross && cat dist-cross/checksums.txt >> "$GITHUB_STEP_SUMMARY"   # must equal the fast job's and the developer's (P1-1)

  supabase:                               # Docker; about 8 minutes (2-3 of them image pulls)
    runs-on: ubuntu-latest
    needs: fast
    timeout-minutes: 25
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with: {go-version-file: go.mod}
      - uses: supabase/setup-cli@v3
        with: {version: 2.116.0}          # pinned explicitly; no lockfile exists any more to read it from
      - run: make build
      - run: make supabase-start supabase=supabase   # `supabase ?=` in the Makefile: CI overrides the npx form with
      - run: make test-db supabase=supabase          # the setup-cli binary, so local and CI run the same recipes and
      - run: make supabase-env advisor-lints supabase=supabase   # the version is pinned in exactly one place per context
      - run: make test-integration        # go test (self-skip lifted by .env.test) + conformance(supabase) --slow
        env: {BRIGADE_COVER: "1", GOCOVERDIR: "${{ runner.temp }}/cov"}
      - run: make e2e                     # scripts/proof.sh, watcher in --sink mode, no LLM (D31)
        env: {GOCOVERDIR: "${{ runner.temp }}/cov"}
      - run: go tool covdata percent -i="${{ runner.temp }}/cov" >> "$GITHUB_STEP_SUMMARY"
      - if: always()
        run: make supabase-clean supabase=supabase

  deploy-staging:                         # Phase 5; only when a hosted staging project exists
    if: github.ref == 'refs/heads/master' && vars.BRIGADE_STAGING == 'true'
    needs: [fast, supabase]
    environment: staging
    runs-on: ubuntu-latest
    env: {SUPABASE_ACCESS_TOKEN: "${{ secrets.SUPABASE_ACCESS_TOKEN }}", SUPABASE_DB_PASSWORD: "${{ secrets.SUPABASE_DB_PASSWORD }}"}
    steps:
      - uses: actions/checkout@v7
      - uses: supabase/setup-cli@v3
        with: {version: 2.116.0}
      - run: supabase link --project-ref "${{ secrets.SUPABASE_PROJECT_ID }}"
      - run: supabase db push --dry-run && supabase db push && supabase config push --yes
```

(`setup-lint` is `setup` without the goreleaser and Docker steps; the Makefile lists it next to `setup`.) Action versions read today: `actions/checkout@v7.0.1`, `actions/setup-go@v7.0.0` (`cache: true` by default, keyed on the sum files named in `cache-dependency-path`), `golangci/golangci-lint-action@v9.3.0` (used locally through the Makefile instead, so local and CI run the same pinned binary), `goreleaser/goreleaser-action@v7.2.3`, `supabase/setup-cli@v3` (needs Node 20+) [verified, A.7]. `ubuntu-latest` is Ubuntu 24.04 with Docker 28, gcc, Node 24, shellcheck, gh and jq preinstalled; `macos-latest` is macOS 26 arm64 [verified]. The `macos` job runs only `go test` (no lint, no Docker, no cross-compile) to keep the arm64 minutes small; it is the job that would have caught the 103-byte socket path limit. `supabase start` takes 2-3 minutes mostly for image pulls, and image caching is not worth it (local-dev digest). `claude plugin validate`, the LLM proof runs and the interactive checklists need Claude Code and run locally (whether `claude plugin validate` works in CI without a login is E0-8 (g)).

`.goreleaser.yaml` (validated by `goreleaser check`; the dry run produced sha256 values identical to plain `go build` for all four targets [verified, A.7]):

```yaml
version: 2
project_name: brigade
before:
  hooks:
    - go mod verify
builds:
  - id: brigade
    main: ./cmd/brigade
    binary: brigade
    env: [CGO_ENABLED=0]
    goos: [darwin, linux]
    goarch: [amd64, arm64]                          # the four D33 targets, nothing else
    flags: [-trimpath, -buildvcs=false]             # both REQUIRED for reproducibility: VCS stamping changes the bytes per commit and
                                                    # with a dirty tree, and -trimpath alone does not give cross-path reproducibility
    ldflags:
      - -s -w -X github.com/appshapes/brigade/internal/buildinfo.Version={{ .Version }}   # no .Commit, no .Date: the bytes must not depend on the release commit
    mod_timestamp: "{{ .CommitTimestamp }}"
archives:
  - id: raw
    formats: [binary]                               # raw binaries, no tar/zip: the bootstrap downloads one file and verifies one sha256
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
checksum:
  name_template: checksums.txt
  algorithm: sha256
changelog:
  use: git
  sort: asc
  filters:
    exclude: ["^Merge "]                            # git's default merge subject (`git pull --no-edit` produces
                                                    # "Merge remote-tracking branch 'origin/master'", never "15: Merge …")
release:
  github: {owner: appshapes, name: brigade}
  draft: true                                       # published by the workflow only after the checksum verification below
  prerelease: auto
  mode: keep-existing
```

Asset names: `brigade_0.1.0_darwin_arm64`, `brigade_0.1.0_darwin_amd64`, `brigade_0.1.0_linux_amd64`, `brigade_0.1.0_linux_arm64`, plus `checksums.txt` with lines `<sha256>  <asset name>` [verified from the dry run, A.7]. goreleaser's own defaults would break this: its default ldflags embed `.Commit` and `.Date`, it does not pass `-trimpath` or `-buildvcs=false`, and it refuses a repository without a remote or with a dirty tree (untracked files included) [verified, A.7], which is why the release workflow runs on a clean tag checkout and why `make cross` uses the identical flags.

`.github/workflows/release.yml`:

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write                                   # create the release and upload assets
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with: {fetch-depth: 0}                      # goreleaser needs the tag history for the changelog
      - uses: actions/setup-go@v7
        with: {go-version-file: go.mod}             # the same toolchain the developer used for plugin/bin/checksums.txt
      - name: Guard that the plugin pins this tag and that the source reproduces the committed checksums
        run: |
          test "v$(cat plugin/bin/VERSION)" = "${GITHUB_REF_NAME}"
          test "$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' plugin/.claude-plugin/plugin.json)" = "$(cat plugin/bin/VERSION)"
          make cross && diff dist-cross/checksums.txt plugin/bin/checksums.txt
      - uses: goreleaser/goreleaser-action@v7
        with: {distribution: goreleaser, version: "~> v2", args: release --clean}   # builds, checksums, creates a DRAFT release
        env: {GITHUB_TOKEN: "${{ secrets.GITHUB_TOKEN }}"}
      - name: Verify goreleaser's build matches the committed checksums
        run: scripts/ci/release-verify.sh dist/checksums.txt plugin/bin/checksums.txt   # sorted hash columns must be identical
      - name: Publish
        if: success()
        run: gh release edit "${GITHUB_REF_NAME}" --draft=false --latest
        env: {GH_TOKEN: "${{ github.token }}"}
      - name: Discard the draft
        if: failure()
        run: gh release delete "${GITHUB_REF_NAME}" --yes || true
        env: {GH_TOKEN: "${{ github.token }}"}
```

The release sequence (chicken and egg). `plugin/bin/checksums.txt` must be in the commit the tag points at (a plugin installed from that commit, or from `master`, verifies the binary against it), but goreleaser builds the binary from that tag afterwards. The resolution is reproducibility: with the same toolchain (forced by `GOTOOLCHAIN`, 7.2), `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false` and a version-only `-X`, the bytes are identical across rebuilds, directories, git state and between plain `go build` and goreleaser — proven today, but only on this one macOS arm64 machine [verified, A.7]; that a macOS developer build equals an ubuntu CI build byte for byte rests so far on Go's cross-host reproducibility claim (https://go.dev/blog/rebuild) [likely]. So P1-1 puts the claim under test from the first CI run rather than at P2-12: both the `fast` and `macos` jobs append their `make cross` checksums to the job summary, and P1-1's acceptance requires them to equal the developer's local ones. If they ever differ, the fallback is already recorded: the checksums are produced by the release job and committed in a follow-up commit, with the plugin pinned one version behind — a change confined to this section, not to Phase 2. The checksums are computed locally from the intended source before the commit, and the tag's CI build is required to reproduce them:

```sh
#!/bin/sh
# scripts/release-prep.sh 0.1.0 [branch]   (run by `make release version=0.1.0 [branch=<throwaway>]`; POSIX sh,
# no process substitution — the P2-12 rehearsal runs it from a throwaway branch via the branch argument)
set -eu
v=$1
want_branch=${2:-master}
git diff --quiet && git diff --cached --quiet || { echo "tree not clean"; exit 1; }
[ "$(git branch --show-current)" = "$want_branch" ] || { echo "release from $want_branch (pass branch=<name> only for a rehearsal)"; exit 1; }
go_line=$(sed -n 's/^go //p' go.mod)
[ "$(GOTOOLCHAIN=go$go_line go env GOVERSION)" = "go$go_line" ] || { echo "cannot select toolchain go$go_line"; exit 1; }
git pull --no-edit
# 1. bump the pins
printf '%s\n' "$v" > plugin/bin/VERSION
sed -i.bak -E "s/^([[:space:]]*\"version\":[[:space:]]*)\"[^\"]+\"/\1\"$v\"/" plugin/.claude-plugin/plugin.json && rm -f plugin/.claude-plugin/plugin.json.bak
# 2. reproducible build of the four targets with the release flags (Makefile `cross` = the flags of .goreleaser.yaml)
make cross version="$v"
# 3. cross-check against goreleaser itself, so the Makefile and .goreleaser.yaml cannot drift apart. With
#    `formats: [binary]` goreleaser leaves dist/ as brigade_<os>_<arch>_<v1|v8.0>/brigade DIRECTORIES — the
#    name_template applies only to the uploads [verified: goreleaser archive docs] — so never hash dist/brigade_*;
#    compare the checksum FILES, whose lines (`<sha256>  <upload name>`, alphabetical) are byte-identical to
#    `make cross`'s [verified today: the release-lab's two files diff clean].
GORELEASER_CURRENT_TAG="v$v" GOTOOLCHAIN="go$go_line" bin/goreleaser release --clean --skip=publish,validate,announce
diff dist/checksums.txt dist-cross/checksums.txt
# 4. commit the checksums goreleaser produced
cp dist/checksums.txt plugin/bin/checksums.txt
make push message="15: Release $v"        # typecheck → pull → build → test → add → commit → push (the fast job re-verifies)
# 5. tag the commit that now contains VERSION + checksums; the release workflow builds, verifies and publishes
git tag -a "v$v" -m "v$v" && git push origin "v$v"
```

`--skip=validate` bypasses goreleaser's dirty-tree and tag checks locally; `--snapshot` is not used because it rewrites the version to `<v>-SNAPSHOT-<commit>`, which would change the embedded bytes [verified from the docs, A.7]. Whether `GORELEASER_CURRENT_TAG` accepts a tag that does not yet exist as a git object is [uncertain]; the P2-12 rehearsal on a scratch tag settles it, and if it does not, step 3 is dropped and `make cross` alone produces the checksums, with the release job's verification still guarding the result. (That `checksums.txt` lists the binary-format assets under their upload `name_template` names is already settled: the dry run showed it [verified, A.7].) Step 3 is deliberately redundant with step 2: it catches a flag drift between the Makefile and `.goreleaser.yaml` before the commit rather than in the release job. `scripts/ci/checksums-check.sh` closes the loop on every commit: (a) `plugin/bin/VERSION` equals `plugin.json` `version`; (b) `checksums.txt` names exactly the four assets for that version; (c) the hashes of this commit's `make cross` output equal the committed file, or a published release `v$VERSION` exists whose `checksums.txt` (`gh release download v$VERSION -p checksums.txt -O -`, authenticated with the job token because the repository is private) equals the committed file; otherwise it fails ("checksums are stale relative to the source and no release backs them"). On the release commit (c) holds by construction; on later commits the published release backs the file; on a commit that changes source without bumping the version, the check is what tells the developer that the plugin still pins the older binary, which is correct and expected. Pre-release state: `VERSION` is `0.0.0` and `checksums.txt` is empty until the first tag; the bootstrap refuses to download in that state and requires the developer pointer file, and `checksums-check.sh` branches explicitly: when `VERSION` is `0.0.0` it requires `checksums.txt` to be EMPTY and skips (b) and (c) entirely — (b) can never hold against an empty file — and (b)/(c) apply only to a real version, so the fast job is green from P1-8 onward. Failure and recovery: if the release job's verification fails, the draft is deleted, nothing is published, and the cause is a flag or dependency drift (a toolchain difference is prevented by the forced `GOTOOLCHAIN`, 7.2; a flag drift is caught earlier by step 3); before rerunning `make release` after the fix, delete the never-published tag with `git tag -d "v$v" && git push --delete origin "v$v"` (permitted exactly because nothing was published; `git tag -a` would otherwise refuse the existing local tag); a published tag is never rewritten (bump the patch version instead). `shellcheck -s sh plugin/bin/brigade` runs in `plugin-check.sh`; the script skips that one step with a warning when `shellcheck` is absent (it is not installed on this machine [verified today]; `make setup` installs it via brew on darwin) while CI, where the Ubuntu runner preinstalls it, always enforces it.

### 7.8 `CLAUDE.md` additions

```markdown
# Brigade
- Protocol v1 is frozen in docs/protocol-v1.md. Changing a wire shape means changing internal/protocol (types and
  Validate), docs/protocol-v1.schema.json (`make schema`), the conformance suite and both adapters in one commit.
- Never write to stdout from a command, the watcher or an adapter except protocol JSON/NDJSON or the documented human
  output of harness/commands (forbidigo enforces it); diagnostics go to stderr through the redacting logger; never
  `slog.Any`. Never spawn with a shell; always argument arrays with an allow-listed environment.
- Never put secrets on argv, in logs, in `describe` output, or in files under the project directory. The join secret is
  read from stdin or a no-echo prompt only. The Supabase secret/service-role key must never appear in the adapter, the
  plugin, the repo or CI variables that ship.
- Never hardcode `~/.claude`; use `CLAUDE_CONFIG_DIR ?? ~/.claude`. Never read or copy `$CLAUDE_CONFIG_DIR/sessions/*.key`;
  the session registry JSON is read best-effort only and never written. Inside a Claude Code session (CLAUDE_PID set)
  the harness ignores every inherited BRIGADE_* variable and reads its configuration from the hook-written by-pid map.
- internal/harness must not import internal/adapters/supabase (depguard); the plugin talks only the adapter protocol
  and spawns the bundled adapter as a child process (`brigade adapter supabase …`).
- The shipped binary links only the modules in docs/allowed-deps.txt (`make deps-check`); dev tools live in tools.mod,
  never in go.mod; go.mod has no `toolchain` line.
- `make test` is Docker-free. Run `make supabase-start supabase-env` once, then `make test-all` before pushing anything
  that touches supabase/ or internal/adapters/supabase.
- Never commit a plugin/bin/checksums.txt or plugin/bin/VERSION you did not produce with `make release`; CI verifies them
  against a fresh cross-compile or the published release.
- Local dev: `make plugin-dev` writes the dev-binary pointer and starts Claude Code with the local plugin; `make
  plugin-dev-off` removes it. Two profiles on one machine: pass
  `--settings '{"pluginConfigs":{"brigade@inline":{"options":{"profile":"<name>"}}}}'`.
- Experiment reports live in docs/experiments/ and their driver scripts in scripts/experiments/; the research digests and
  their evidence are committed under docs/research/ (the threat model defines U-01..U-25, I-01..I-33, E2E-*, CI-*;
  U-26..U-28 and I-34 are this plan's additions, defined in its sections 9.8/9.9); scratch in .ignored/; plans in
  .context/plans/.
- Proof and experiment scripts, and the e2e/proof/harness-smoke/plugin-dev targets, run outside the current Claude
  session: they unset the inherited CLAUDE_* session variables (CLAUDE_PID, CLAUDECODE, CLAUDE_CODE_MESSAGING_* and
  friends; keep CLAUDE_CONFIG_DIR) before doing anything (plan 9.6).
- Commit messages: `15: <Imperative summary>`; `make push message="15: ..."`; merges only, never rebase.
```

---
