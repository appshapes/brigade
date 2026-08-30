# Go toolchain, module layout and release engineering for Brigade

Date: 2026-08-30. Machine: macOS 25.6 arm64, Go 1.27.0 (released 2026-08-19), Docker 29, Claude Code v2.1.251.
Scope: the engineering setup that decisions D33/D34/D35 (Go, one static `brigade` binary, no MCP, four targets, GitHub Releases + a sh bootstrap in `plugin/bin`) require. Everything marked `[verified]` was either run on this machine today (lab under `scratchpad/wf2/research/{go-lab,release-lab,release-plain,bootstrap-lab,modfile-lab,toolchain-lab}`) or read today from the official page cited. `[likely]` = documented but not exercised here; `[uncertain]` = needs a Phase 0 experiment.

## 0. Decisions in one table

| Topic | Decision | Confidence |
| --- | --- | --- |
| Go version policy | `go 1.27.0` in go.mod, no `toolchain` line (implicit `toolchain go1.27.0`); `GOTOOLCHAIN` left at its default `auto`; CI uses `actions/setup-go@v7` with `go-version-file: go.mod`. Bump the `go` line deliberately, in its own commit. | verified |
| Module | One module `github.com/appshapes/brigade`; `cmd/brigade` (shipped), `cmd/brigade-adapter-fs`, `cmd/brigade-conformance`, `cmd/brigade-schema` (dev only); everything else under `internal/`. Dev tools in a separate `tools.mod` driven by `go tool -modfile=tools.mod`. | verified |
| Subcommand dispatch | stdlib `flag` + a 60-line table dispatcher; not cobra/urfave. One helper makes flags interspersable with positionals (stdlib `flag` stops at the first positional, and the grammar is `brigade send <session_id> --reply-to <id>`). | verified (sizes, behaviour) |
| JSON | `encoding/json/v2` (generally available in Go 1.27, no GOEXPERIMENT) for every wire shape; loose parsing is the v2 default (unknown members ignored); duplicates and invalid UTF-8 rejected by default; case-sensitive names. `UnmarshalRead` for the stdin document with a 1 MiB `io.LimitReader`. | verified |
| Validation | Hand-written `Validate() error` on each wire type (byte and code-point caps, required fields, forbidden sender fields on `SendRequest`); no reflection validator library. JSON Schema is documentation, generated from the same types. | design |
| JSON Schema export | `github.com/invopop/jsonschema v0.14.0` (draft 2020-12) in dev-only `cmd/brigade-schema` → `docs/protocol-v1.schema.json`, drift-checked in CI; `github.com/santhosh-tekuri/jsonschema/v6 v6.0.3` in `_test.go` only, to validate the spec examples against the generated schema. Neither is linked into `brigade` (asserted by a `go version -m` allowlist check). | verified |
| NDJSON | Own `bufio.Reader`-based reader: lines ≤ 1 MiB are delivered, longer lines are dropped with a warning and reading continues (`bufio.Scanner` stops permanently with `ErrTooLong`). Writer: `jsonv2.Marshal` + `\n`. | verified |
| Unix socket client | `net.Dialer{Timeout}.DialContext(ctx, "unix", path)`, write deadline, `Lstat` pre-checks (socket, not symlink, uid, 0600), `errors.Is(err, syscall.ENOENT)`. | verified |
| Spawning the adapter | `exec.CommandContext` + `cmd.Cancel` (SIGTERM) + `cmd.WaitDelay` (then SIGKILL), argv only, env built from scratch, stdin from a `bytes.Reader`, stdout into a capped writer that cancels the context at 4 MiB. Timeout is detected with `ctx.Err()`, not from `Wait`'s error. | verified |
| Detaching the watcher | `SysProcAttr{Setsid: true}`, stdin nil (`/dev/null`), stdout/stderr = the 0600 log file, `Dir = $HOME`, explicit `Env`, `Process.Release()`. Child: ppid 1, own session and process group, no tty, fds 0-2 only (Go opens everything else `O_CLOEXEC`). Same on Linux. | verified (macOS + Alpine) |
| PID liveness / reuse | `syscall.Kill(pid, 0)`: nil = alive, `ESRCH` = gone, `EPERM` = alive but another user. Start-time token: macOS `ps -o lstart= -p` (1 s resolution, format `Mon Jan _2 15:04:05 2006`); Linux `/proc/<pid>/stat` field 22 (busybox `ps` has no `lstart`). Store the token verbatim in the pidfile and compare for equality. | verified |
| File locks | `syscall.Flock` (stdlib, darwin and linux): `LOCK_EX|LOCK_NB` → `EWOULDBLOCK`; blocking `LOCK_EX` waits. Lock a sidecar `session.json.lock`, never the file that is renamed over. | verified |
| Atomic 0600 writes | `os.CreateTemp` (0600, `O_EXCL`) in the target dir → write → `Sync` → `Close` → `Rename` → fsync dir. Pidfiles: `O_CREATE|O_EXCL`. Refuse world-readable files (`perm&0o077 != 0`). | verified |
| XDG dirs | Own resolver: `$BRIGADE_*` override → `$XDG_CONFIG_HOME`/`$XDG_STATE_HOME` (absolute only) → `~/.config/brigade`, `~/.local/state/brigade`. `os.UserConfigDir` is NOT usable (returns `~/Library/Application Support` on macOS). | verified |
| Logging | `log/slog` JSON handler with a `ReplaceAttr` redactor (secret keys, JWT/`brg1.`/`sb_secret_`/Bearer patterns, exact messaging token). Rule: never log struct/map values (`ReplaceAttr` does not visit their fields); a `forbidigo` rule bans `slog.Any` outside `internal/log`. | verified |
| Signals | `signal.Notify` for SIGTERM/SIGINT (watcher and `message watch`); clean exit in ~2 ms; SIGHUP cannot reach a `Setsid` child from a terminal; unhandled SIGTERM shows as `signal: terminated`, `ExitCode() == -1`. | verified |
| Cross-compilation | `CGO_ENABLED=0 GOOS/GOARCH go build -trimpath -buildvcs=false -ldflags "-s -w -X main.version=<v>"` for darwin/arm64, darwin/amd64, linux/amd64, linux/arm64. The hello+net/http+websocket binary is 5.7-6.2 MB (2.3-2.6 MB gzip); with json/v2, slog, os/exec, sha256, regexp it is 6.8-7.5 MB. | verified |
| Reproducibility | With those flags the binary is byte-identical across rebuilds, directories and git state; goreleaser 2.18.0 with the same flags produced identical sha256 for all four targets. `-trimpath` and `-buildvcs=false` are both required. | verified |
| Release | goreleaser v2 (2.18.0), `archives.formats: [binary]` (raw binaries named `brigade_<version>_<os>_<arch>`), `checksums.txt`, git changelog, GitHub Release via `goreleaser/goreleaser-action@v7` on `v*` tags. `plugin.json` `version` == tag; `plugin/bin/checksums.txt` is regenerated by `make plugin-checksums` before tagging and re-verified by the release workflow before publishing. | verified (local dry run) |
| Lint / format / vuln | golangci-lint v2.13.2 (`version: "2"` config; `standard` = errcheck, govet, ineffassign, staticcheck, unused, plus a curated enable list; formatters gofmt+goimports) installed by the official `install.sh` pinned to a version (docs say `go install`/`go tool` "aren't guaranteed to work"); `golangci/golangci-lint-action@v9` in CI; `govulncheck` v1.7.0 via `tools.mod`; `go vet` as the `typecheck` target. | verified |
| Dependency policy | Shipped binary may link only stdlib + `github.com/coder/websocket` (zero deps) + `golang.org/x/{sys,term,text}`. Dev/test-only: invopop/jsonschema, santhosh-tekuri/jsonschema. Tools never enter go.mod. CI asserts the shipped dependency list from `go version -m`. | verified (deps), design (policy) |
| Distribution | `plugin/bin/brigade` POSIX-sh bootstrap (tested under macOS `/bin/sh` and busybox ash; `sh -n`, `bash -n`, `zsh -n` clean): first use downloads the pinned raw binary (curl or wget), verifies sha256 (`sha256sum` or `shasum -a 256`), installs atomically under `CLAUDE_PLUGIN_DATA/bin/`, execs; later runs exec the cache in ~14 ms; a dev pointer file under the user's own data dir overrides. | verified |

## 1. What was run (evidence index)

Lab module `scratchpad/wf2/research/go-lab` (`go 1.27.0`): `sizeprobe` (hello + net/http + coder/websocket), `sizeprobeplus` (adds json/v2, slog, os/exec, sha256, regexp, bufio, unix `net`, flag), `hello` (flag only), `cobraprobe` (cobra 1.10.2), and `lab` with 17 subcommands (json2, ndjson, unix, exec-cap, exec-timeout, detach/child-report, kill0, lstart, flock, atomic, slog, xdg, signals, schema, stdin). `lab` was built for linux/arm64 and run in `alpine:3.20`; the sizeprobe was also run in `debian:bookworm-slim` and `ubuntu:24.04`. `release-lab` is a git repo with the proposed `.goreleaser.yaml`, tagged `v0.1.0`, released with `goreleaser release --skip=publish --clean` (goreleaser 2.18.0). `bootstrap-lab` served the four binaries plus `checksums.txt` from a local `python3 -m http.server` and ran the bootstrap on macOS and in Alpine. `modfile-lab` exercised `go get -tool -modfile=tools.mod`; `toolchain-lab` exercised the `go`/`toolchain` directives. Tools installed into `go-lab/bin`: golangci-lint 2.13.2 (install.sh), govulncheck 1.7.0 (`go install`), goreleaser 2.18.0 (release tarball via `gh release download`).

Latest versions read today from the Go module proxy and the GitHub API: coder/websocket v1.8.15 (go.mod says `go 1.23`); invopop/jsonschema v0.14.0 (`go 1.24`; requires pb33f/ordered-map/v2, bahlo/generic-list-go, buger/jsonparser); santhosh-tekuri/jsonschema/v6 v6.0.3 (requires golang.org/x/text); spf13/cobra v1.10.2; golang.org/x/sys v0.47.0, x/term v0.45.0, x/tools v0.49.0, x/vuln v1.7.0; golangci-lint v2.13.2 (2026-08-27); goreleaser v2.18.0 (2026-08-24); actions/setup-go v7.0.0; actions/checkout v7.0.1; goreleaser/goreleaser-action v7.2.3; golangci/golangci-lint-action v9.3.0; golang/govulncheck-action v1.1.0. `[verified]`

## 2. Go version policy

Facts `[verified: https://go.dev/doc/toolchain and toolchain-lab]`:

- The `go` line is "the minimum required Go version" and the language version; "The Go toolchain refuses to load a module or workspace that declares a minimum required Go version greater than the toolchain's own version." Observed: with `go 1.28.0` and `GOTOOLCHAIN=local`, `go build` prints `go: go.mod requires go >= 1.28.0 (running go 1.27.0; GOTOOLCHAIN=local)`; with the default `auto` it tries `go: downloading go1.28.0` and fails with `toolchain not available` because no such release exists.
- "If the `toolchain` line is omitted, the module ... is considered to have an implicit `toolchain go_V_` line" where V is the `go` line. A `toolchain` line is only a suggestion: with `toolchain go1.27.9` (nonexistent) and `GOTOOLCHAIN=local` the build succeeded on 1.27.0; with `auto` it tried to download go1.27.9.
- Gotcha: writing an explicit `toolchain go1.27.0` next to `go 1.27.0` makes every `go build` fail with `go: updates to go.mod needed; to update it: go mod tidy` (the redundant line must be removed; `go mod tidy` removes it). Do not write it.
- `go 1.27` (no patch) is accepted and builds; `go 1.27.0` is the conventional form for a pinned toolchain and is what `go mod init` writes.

Policy: `go 1.27.0`; no `toolchain` line; developers on an older Go get the refusal above and either upgrade or let `auto` download 1.27.0 (default behaviour); CI pins through `go-version-file: go.mod` (setup-go "prioritizes the `toolchain` directive in `go.mod` if present; otherwise it defaults to the `go` directive" `[verified: setup-go README]`). Bump the `go` line only in a dedicated commit and only to a released version. Go 1.27 requires macOS 13 Ventura or later `[verified: release notes]`; the linker stamps `LC_BUILD_VERSION minos 13.0` `[verified: otool]`, which should be stated in the README (`macOS 13+`; Linux: any x86-64/arm64 with a CA bundle, section 17).

## 3. Module layout

### 3.1 Proposed go.mod

```
module github.com/appshapes/brigade

go 1.27.0

require (
	github.com/coder/websocket v1.8.15
	golang.org/x/sys v0.47.0
	golang.org/x/term v0.45.0
	golang.org/x/text v0.39.0
)

// Development-only: linked into cmd/brigade-schema and *_test.go files, never into cmd/brigade
// (make deps-check asserts that from the built binary's `go version -m` output).
require (
	github.com/invopop/jsonschema v0.14.0
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3
)
```

Why each shipped dependency: `coder/websocket` for the Phoenix-channels client (zero dependencies, autobahn-tested, maintained by Coder `[verified: README]`); `x/term` for `team join --prompt` (`ReadPassword` reads "a line of input from a terminal without local echo", `IsTerminal` `[verified: go doc]`) and it requires `x/sys`; `x/text/unicode/norm` for the sanitiser's NFC step (no stdlib NFC). `x/text` must be ≥ v0.39.0: govulncheck reported GO-2026-5970 for `golang.org/x/text@v0.14.0` ("Fixed in golang.org/x/text@v0.39.0") in the lab module, where v0.14.0 arrived through santhosh-tekuri `[verified]`. Everything else (`syscall.Flock`, `syscall.Kill`, `SysProcAttr.Setsid`, `os/exec` cancel/wait-delay, `log/slog`, `encoding/json/v2`) is stdlib and was exercised.

Cobra is not needed (section 4). No `vendor/`. Renovate or Dependabot for `gomod` and `github-actions` ecosystems is recommended (not exercised).

### 3.2 Directory tree

```text
brigade/                                  git root = plugin marketplace root; branch master
  .claude-plugin/marketplace.json         {"name":"brigade","owner":{"name":"appshapes"},"plugins":[{"name":"brigade","source":"./plugin"}]}
  .github/workflows/ci.yml                fast job (ubuntu + macos), supabase job later (section 20)
  .github/workflows/release.yml           on tags v*: guard (plugin.json version == tag, checksums match), goreleaser
  .golangci.yml                           section 19
  .goreleaser.yaml                        section 18
  Makefile                                section 21
  go.mod, go.sum
  tools.mod, tools.sum                    dev tools only (govulncheck, goimports) via `go tool -modfile=tools.mod`
  cmd/
    brigade/main.go                       the one shipped binary: builds the dispatch table, calls cli.Main
    brigade-adapter-fs/main.go            dev/test adapter (never shipped)
    brigade-conformance/main.go           C-01..C-42 runner for any adapter (never shipped)
    brigade-schema/main.go                writes docs/protocol-v1.schema.json (invopop/jsonschema)
  internal/
    buildinfo/                            version: ldflags -X, falling back to debug.ReadBuildInfo().Main.Version for `go install`
    cli/                                  dispatcher, interspersed-flag helper, --json/human printers, exit codes, usage
    protocol/                             wire types (json/v2 tags), constants, error taxonomy + exit map, Validate() per type,
                                          NDJSON reader/writer, join-secret parser; protocol_test.go validates examples vs the schema
    sanitize/                             NFC, control/format stripping, tag neutralisation, attribute rules
    frame/                                <brigade-message> builder (variants A/C), sender summary placement
    adapterkit/                           shared adapter plumbing: bounded stdin document, result printer, XDG dirs,
                                          atomic 0600 files, flock, redacting slog handler, profile file schema
    adapters/supabase/                    gotrue.go, postgrest.go, realtime.go (phoenix on coder/websocket), commands/*.go, errors.go
    adapters/fs/                          filesystem adapter store + polling watch (dev/test only)
    conformance/                          suite, launcher, mutants (used by cmd/brigade-conformance)
    harness/adapterclient/                spawn adapter child (bundled: os.Executable() + "adapter supabase"; third-party: adapter_command),
                                          env allowlist, stdin JSON, 4 MiB cap, timeouts, exit-code mapping, describe cache
    harness/hook/                         session-start | prompt | session-end
    harness/watch/                        detached watcher: supervision, dedupe, policy, inject, ack, heartbeat, liveness, --sink
    harness/socketpost/                   Claude Code inbox socket client (pre-checks, auth line, deadlines)
    harness/registry/                     $CLAUDE_CONFIG_DIR/sessions/<pid>.json best-effort reader (never the .key files)
    harness/sessionmap/                   by-pid / by-native maps
    harness/pidfile/                      O_EXCL pidfile, kill(pid,0), start-time token guard
    harness/policy/                       inbound policy (accept/refuse; hold arrives in Phase 5)
    procutil/                             detach (Setsid), start-time token (ps lstart | procfs, build-tagged), signals
  plugin/                                 what --plugin-dir points at; nothing built lives here
    .claude-plugin/plugin.json            "version" pins the release tag (plugins-reference: setting it "pins the plugin to that version string")
    bin/brigade                           POSIX-sh bootstrap (section 22); on the Bash tool's PATH as `brigade`
    bin/checksums.txt                     goreleaser-format sha256 lines for the four binaries of the pinned version
    hooks/hooks.json                      exec form: "command": "sh", "args": ["${CLAUDE_PLUGIN_ROOT}/bin/brigade", "hook", "session-start"]
    skills/team-messaging/SKILL.md, skills/setup/SKILL.md
    README.md
  docs/protocol-v1.md, docs/protocol-v1.schema.json, docs/adapter-authors.md, docs/research/, docs/experiments/
  supabase/                               unchanged (config.toml, migrations, tests)
  scripts/proof.sh, scripts/ci/*.sh, scripts/injection-corpus/
```

What this replaces in the plan's section 7.1: `packages/*`, `package.json`, `package-lock.json`, `build.mjs`, `tsconfig*`, `eslint.config.js`, `.prettierrc`, `.prettierignore`, `plugin/.mcp.json`, `plugin/dist/`, `plugin/package.json`, `scripts/ci/dist-check.sh`, `scripts/ci/check-no-native-deps.sh`, the `!plugin/dist/` gitignore negation and the `node_modules` rules. `.gitignore` additions become `/bin/`, `/dist/`, `coverage.out`, `.env`, `.env.*`, `!.env.example`, `supabase/.temp/`. The `*.test` line in the seeded Go template ignores `go test -c` binaries; the seeded template does not ignore `/bin` or `/dist`, so add them.

Build-constraint policy: files that use `syscall.SysProcAttr{Setsid}`, `syscall.Flock`, `unix.TCGETS`/`TIOCGETA` carry `//go:build darwin || linux` (or `_darwin.go`/`_linux.go` names); the whole binary is only released for those two OSes (D33). `go vet ./...` on a third OS fails loudly instead of compiling a broken binary.

## 4. Subcommand dispatch: stdlib `flag` + a small dispatcher

Recommendation: stdlib. Evidence and rationale:

- Size: hello+flag 1,655,666 B; hello+cobra 2,522,114 B (darwin/arm64, `-s -w -trimpath`): cobra costs 0.87 MB and three modules (cobra, pflag, mousetrap) `[verified]`. Not decisive on its own, but it is weight for features the protocol forbids.
- Behaviour: the frozen core says unknown flags are `usage` exit 2 with a JSON error on stdout, help must not leak onto stdout, and stdout carries exactly one JSON document. Cobra's defaults (usage on error, "did you mean" suggestions, `help`/`completion` subcommands, `--help` handling, exit 1 on unknown command `[verified: cobraprobe exits 2 only because main maps it]`) all have to be switched off or re-implemented; the dispatcher we need is a 25-entry table.
- Interspersed flags: stdlib `flag` "parsing stops just before the first non-flag argument", and the model's grammar puts the positional first (`brigade send <session_id> --reply-to <message_id>`). One helper fixes that; cobra/pflag handle it natively. That is the only real convenience cobra would buy.

Design (`internal/cli`):

```go
type Command struct {
	Group, Verb string                // "" group for the top-level human verbs (sessions, send, whoami)
	Hidden      bool                  // "hook *", "watch", "adapter supabase *"
	Positional  []string              // names for usage; count enforced
	Flags       func(fs *flag.FlagSet, o *Options)
	Run         func(ctx context.Context, env *Env, o *Options, pos []string) (result any, err error)
}

// Main parses argv, runs, prints one result (JSON with --json or for machine callers, human text otherwise)
// and returns the exit code from protocol.ExitCode(err). Only main() calls os.Exit.
func Main(args []string, stdin io.Reader, stdout, stderr io.Writer) int

// parseInterspersed lets positionals and flags mix. Everything after "--" is positional.
func parseInterspersed(fs *flag.FlagSet, args []string) (positional []string, err error) {
	tail := []string(nil)
	if i := slices.Index(args, "--"); i >= 0 {
		tail, args = args[i+1:], args[:i]
	}
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err // flag.ErrHelp or a usage error; the caller maps to exit 2 / help
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

Rules: every `FlagSet` is `flag.ContinueOnError` with `SetOutput(io.Discard)` so the package never writes usage itself; `flag.ErrHelp` prints usage to stdout and exits 0 for human commands; any other parse error is `usage` (exit 2) with the JSON error on stdout when `--json` (or when the caller is the harness) and a one-line message on stderr otherwise; `--json` and `--log-level` are global flags registered on every set; secrets are never flags (`--join-secret` is registered as a poison flag whose presence returns `usage`, so C-05 holds). Bodies come from stdin (`<<'EOF'` heredoc) or `--body-file`.

## 5. JSON: `encoding/json/v2` is generally available in Go 1.27

Release notes, verbatim `[verified: https://go.dev/doc/go1.27]`: "Two new packages are now available: The encoding/json/v2 package is a major revision of encoding/json. It provides Marshal, MarshalWrite, MarshalEncode, Unmarshal, UnmarshalRead, and UnmarshalDecode, all of which accept variadic Options arguments ... The encoding/json/jsontext package provides lower-level syntactic processing of JSON." "The encoding/json package is now backed by the v2 implementation. Marshaling and unmarshaling behavior is preserved, but the exact text of error messages may differ." "Users who encounter compatibility problems with the new implementation may disable it by setting GOEXPERIMENT=nojsonv2 at build time, restoring the original v1 implementation. This opt-out is expected to be removed in a future release." Removed during the experiment: the `format` and `unknown` tag options, `DiscardUnknownMembers`, `SkipFunc`; `inline` was renamed `embed`. The lab compiled `import "encoding/json/v2"` with an empty `GOEXPERIMENT` `[verified]`.

Observed v1 vs v2 on identical inputs `[verified: lab json2]`:

| Input | v1 (`encoding/json`) | v2 (`encoding/json/v2`) |
| --- | --- | --- |
| `{"body":"hi","unknown":1}` | ignored | ignored (default); `RejectUnknownMembers(true)` → error `unknown object member name "unknown"` |
| `{"Body":"case"}` (wrong case) | matched `body` | not matched, no error, zero value |
| `{"body":"a","body":"b"}` | last wins (`b`) | error `jsontext: duplicate object member name "body"` (first value kept) |
| `{"body":"x","count":"7"}` | type error | type error, message names the JSON pointer `/count` |
| `omitzero` on pointer/int/time.Time | supported (since 1.24) | supported |
| string `<x> &  ` | escaped `<`, `&`, ` ` | raw `<x> & ` (still valid JSON); `jsontext.EscapeForHTML(true)` / `EscapeForJS(true)` restore escaping |
| `UnmarshalRead` on `{...}\n` | n/a | ok (trailing whitespace allowed) |
| `UnmarshalRead` on `{...} junk` or two documents | n/a | error `invalid character ... after top-level value` |
| empty input | `Decode` → `io.EOF` | `jsontext: unexpected EOF` |

Consequences for Brigade:

- Loose parsing (D16: "consumers parse with loose objects and ignore unknown fields") is the v2 default; never set `RejectUnknownMembers` on wire shapes. The named exception (`SendRequest` rejecting `sender`, `principal_ref`, `human_label`, `team_ref`, `created_at`, `hop_count`, C-23) is implemented by declaring those members on the Go struct as `jsontext.Value` with `omitzero` and returning `invalid_input` from `Validate()` when any is non-empty; no reflection needed.
- Case sensitivity means a client that sends `Body` gets a silent zero value, so `Validate()` must check required fields (`body` non-empty, ids non-empty); the schema documents the exact names. Do not enable `MatchCaseInsensitiveNames`.
- Duplicate-member rejection is a free strictness win on hostile input; keep it.
- The stdin document: `io.LimitReader(stdin, 1<<20+1)`, read fully, `len > 1<<20` → `invalid_input`; then `jsonv2.Unmarshal(bytes.TrimSpace(data), &v)` (`UnmarshalRead` on the limited reader is equivalent). Verified with a 2,000,000-byte input: stops after 1,048,577 bytes `[verified]`. Commands that take no input never touch stdin (the harness passes `/dev/null`); commands that do, and find a terminal on stdin, print usage instead of blocking. TTY detection must use a termios ioctl (`unix.IoctlGetTermios` with `TIOCGETA` on darwin / `TCGETS` on linux, or `x/term.IsTerminal`): `os.ModeCharDevice` is true for `/dev/null` as well `[verified]`, and the Bash tool's stdin is a pipe or socket.
- NDJSON output uses `jsonv2.Marshal`: `\n`, `\r` inside strings are always escaped, so a body can never make a second line (C-39); U+2028/2029 are emitted raw, which is valid JSON and irrelevant to a `\n`-splitting reader. For the socket post to Claude Code (Node `JSON.parse`) raw U+2028 inside a string is valid JSON and valid JS since ES2019 `[likely]`; if E0-3 shows any rendering oddity, add `jsontext.EscapeForJS(true)` `[verified: option exists]`.
- Struct tags: `json:"name,omitzero"`; pointers for optional members that must distinguish null from absent; `time.Time` marshals RFC 3339 in both versions.

## 6. Input validation

Each wire type gets `func (r *SendRequest) Validate() error` returning `*protocol.Error{Code: "invalid_input", Message: "body exceeds 16384 bytes", Details: {"field": "body"}}`. Byte caps use `len(s)`; code-point caps use `utf8.RuneCountInString`; `utf8.ValidString` is implied by json/v2 (invalid UTF-8 is rejected at decode). A tiny `check` helper keeps this to a few lines per field. No go-playground/validator (reflection, struct tags as a second schema language, 5+ transitive modules). The JSON Schema (section 7) is documentation and a cross-check in tests, not the runtime validator. Adapter-side validation happens before any network call (C-23, C-27).

## 7. JSON Schema export

`invopop/jsonschema v0.14.0` reflects Go types into draft 2020-12 `[verified: README and output]`. Settings that match the protocol: `RequiredFromJSONSchemaTags: true` (required only where `jsonschema:"required"` says so), `AllowAdditionalProperties: true` (loose parsing; without it every object gets `additionalProperties: false`, which would contradict D16). Struct tags used: `required`, `minLength`, `maxLength`, `enum=a,enum=b`, `minimum`, `description`. Verified output for `Result`, `ErrorBody`, `SendRequest` (lab `schema`): enums, required arrays, `$ref` to `#/$defs/ErrorBody`, `any` → `true` (accept anything), `map[string]any` → `{"type":"object"}`; top-level document assembled as `{"$schema": ..., "$id": ..., "$defs": {...}}` by merging each reflected type's `Definitions`. The generator is `cmd/brigade-schema` (dev-only; `make schema` writes `docs/protocol-v1.schema.json`; `make schema-check` diffs in CI). Note the byte cap on `body` cannot be expressed in JSON Schema (`maxLength` counts code points); the description says so and `Validate()` enforces bytes.

`santhosh-tekuri/jsonschema/v6 v6.0.3` validated instances against a `#/$defs/SendRequest` fragment: an instance with an extra member passes, one missing `recipient_session_id` and `idempotency_key` fails with `missing properties ...`, an empty body fails with `minLength: got 0, want 1` `[verified]`. Use it only in `internal/protocol/protocol_test.go` to validate every example in `docs/protocol-v1.md` against the generated schema; it is never linked into a shipped binary.

## 8. NDJSON reader and writer

`bufio.Scanner` with `Buffer(..., 1<<20)` returned `token too long` at the 2 MiB line and stopped: 1 line scanned out of 8 `[verified]`. The spec says a line longer than 1 MiB "is dropped by the reader with a warning", so the reader is a `bufio.Reader.ReadSlice('\n')` loop that accumulates chunks (`ErrBufferFull`), marks the line as overflowed once it passes 1 MiB, discards the rest of that line and continues. Result on the test stream: accepted 6, dropped 2 (a 2 MiB line and a 1 MiB+1 line), while a 1 MiB-1 line, an exact 1 MiB line and a final line without `\n` were kept `[verified]`. The implementation is 35 lines (lab `readNDJSON`) and is the only NDJSON reader used by the watcher, the conformance suite and `message watch`'s stdin command reader. The writer is `w.Write(append(jsonv2.Marshal(v), '\n'))` behind a mutex, followed by `Flush` per event when stdout is buffered.

## 9. Unix domain socket client

`[verified: lab unix on macOS and Alpine]` `net.Listen("unix", path)` + `os.Chmod(path, 0o600)` for the test server; client `net.Dialer{Timeout: 5 * time.Second}.DialContext(ctx, "unix", path)`, `SetWriteDeadline`, two `\n`-terminated JSON lines, `Close()`; the server received both lines intact, including a body with `\n`, U+2028 and `<x>` (escaped by the writer). Pre-checks before connecting, all from `os.Lstat`: `Mode()&os.ModeSocket != 0`, `Mode()&os.ModeSymlink == 0`, `Sys().(*syscall.Stat_t).Uid == os.Getuid()`, `Mode().Perm() == 0o600`. Dialing a missing path yields an error for which `errors.Is(err, syscall.ENOENT)` is true (the U-20 "re-read the registry for a new socket path" trigger). "Injected" = write returned without error and `Close()` returned nil; the socket sends nothing back (Appendix A).

## 10. Spawning the adapter as a child process

Pattern `[verified: lab exec-cap / exec-timeout, macOS and Alpine]`:

```go
ctx, cancel := context.WithTimeout(ctx, timeout)
defer cancel()
out := &cappedWriter{limit: 4 << 20, cancel: cancel} // Write returns an error and cancels ctx on overflow
cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) // never a shell
cmd.Stdin = bytes.NewReader(input)                    // or nil → /dev/null for commands without input
cmd.Stdout = out
cmd.Stderr = &cappedWriter{limit: 256 << 10}          // for the debug log only
cmd.Env = allowListedEnv()                            // built from scratch: PATH, HOME, TMPDIR, XDG_*, CLAUDE_CONFIG_DIR, CLAUDE_PLUGIN_DATA, BRIGADE_* computed here
cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
cmd.WaitDelay = 2 * time.Second                       // then SIGKILL and close the pipes
err := cmd.Run()
```

Observed:

| Case | Result |
| --- | --- |
| `head -c 100 /dev/zero` | 100 bytes, 1-3 ms |
| `head -c 60000000 /dev/zero`, cap 4 MiB | overflow flagged at 4,177,920 B, context cancelled, child died of SIGPIPE (`signal: broken pipe`), 5 ms (41 ms on Alpine) |
| stdin `{"x":1}` into `cat` | echoed back |
| missing executable | `fork/exec ...: no such file or directory`, `errors.Is(err, os.ErrNotExist)` true → `unavailable` (`details.reason = "adapter_not_found"`) |
| `sh -c 'exit 8'` | `*exec.ExitError`, `ExitCode() == 8` |
| `sleep 30`, 500 ms timeout | `signal: terminated`, `ExitCode() == -1`, 502 ms; `errors.Is(err, context.DeadlineExceeded)` is FALSE |
| child ignoring SIGTERM, 500 ms + WaitDelay 1 s | `signal: killed`, 1.503 s |
| child exits 0 but a grandchild keeps stdout open | output `parent-done` captured, `exec.ErrWaitDelay` after 1.01 s |

Mapping rules that follow: timeout is detected with `ctx.Err() != nil` (Wait reports the signal death, not the deadline) → `unavailable` (`details.reason = "timeout"`); `ExitError.Sys().(syscall.WaitStatus).Signaled()` → `unavailable` (`details.signal`); overflow → `internal` (`details.reason = "stdout_cap"`); non-JSON stdout → `internal`; `exec.ErrWaitDelay` with a parsed result and exit 0 is treated as success and logged (an adapter must not leak its stdout to grandchildren; the fs adapter never spawns). Exit codes 0-12 map straight to the taxonomy. The bundled adapter is spawned as `exec.Command(selfPath, "adapter", "supabase", group, verb, flags...)` with `selfPath` from `os.Executable()` (a real process boundary, D35); `adapter_command` as a JSON array selects a third-party executable.

## 11. Detaching the watcher

`[verified: lab detach on macOS; identical in Alpine]`

```go
logf, _ := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
cmd := exec.Command(self, "watch")
cmd.Stdin = nil            // /dev/null
cmd.Stdout, cmd.Stderr = logf, logf
cmd.Dir = home
cmd.Env = watcherEnv       // PATH, HOME, TMPDIR, XDG_*, CLAUDE_* incl. the messaging socket and token, computed BRIGADE_*
cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
err := cmd.Start()         // then cmd.Process.Release(); never Wait
```

Child observations 0.5 s after the parent exited: `ppid=1`, `pgid == sid == pid`, `tty ??`, `lsof` shows only `0r /dev/null`, `1w`/`2w` the log file and `3u KQUEUE` (the Go runtime's own poller); a file the parent had open (`/etc/hosts`) was not inherited (Go opens with `O_CLOEXEC`); `cwd=/Users/rjae`; the environment carried only what `Env` listed (the token travelled in env only, per T13.4). After 3 s the child was still alive with ppid 1 and exited cleanly; `kill(pid, 0)` then returned ESRCH. `Setsid` gives the child its own session with no controlling terminal, so terminal hang-ups never reach it; the parent hook returns immediately because the child holds none of the hook's stdio pipes. Nothing here needs `nohup`, `disown` or a double fork.

## 12. PID liveness and the PID-reuse guard

`[verified: lab kill0/lstart on macOS, Alpine, Ubuntu]`

- `syscall.Kill(pid, 0)`: `nil` → alive; `ESRCH` → gone; `EPERM` → alive but owned by another user (pid 1 on macOS). Treat `EPERM` as alive-but-foreign, which for our own watcher pidfile means "not our process" (reuse by another user) → replace.
- Start-time token: macOS `ps -o lstart= -p <pid>` prints `Sun Aug 30 13:32:35 2026`, parsed with `time.ParseInLocation("Mon Jan _2 15:04:05 2006", s, time.Local)`; 1-second resolution. Ubuntu (procps) prints the same format; Alpine/busybox `ps` has no `lstart` and exits 1. Linux therefore reads `/proc/<pid>/stat` and takes field 22 (`starttime`, clock ticks since boot) from after the last `)` (the `comm` field may contain spaces and parentheses) `[verified in Alpine: starttime_ticks=139079274]`.
- Guard: at spawn, capture the token (`ps lstart` string on darwin, raw tick string on linux) for the child pid and write it into the pidfile as `start_token`; later, "alive" means `kill(pid,0) == nil` and the current token equals the stored one byte-for-byte. No clock arithmetic, no `CLK_TCK`. The token function is build-tagged (`procstart_darwin.go` runs `ps`, `procstart_linux.go` reads procfs).

## 13. File locks for the shared credential file

`syscall.Flock` is in the stdlib for both targets `[verified: lab flock on macOS and Alpine]`: with one process holding `LOCK_EX`, a second process's `LOCK_EX|LOCK_NB` fails with `EWOULDBLOCK` (`errors.Is(err, syscall.EWOULDBLOCK)`), and a blocking `LOCK_EX` returned after the holder released (2.48 s / 1.68 s). `flock` is advisory and bound to the open file description, so lock a stable sidecar (`profiles/<name>/session.json.lock`, 0600) rather than `session.json`, which is replaced by `rename` on every write (a lock on the old inode would not protect the new one). Because D35 replaces auth-js (whose re-read-before-refresh logic was the reason D23 skipped a lock) with a hand-rolled GoTrue client, take the lock around read → refresh → write from the start: `LOCK_EX` with a 10 s bound (poll `LOCK_NB` every 100 ms, then `unavailable`), re-read the file after acquiring (another process may have refreshed already; if the stored access token is still valid, skip the refresh). E0-6 then measures two processes sharing one file with this lock.

## 14. Atomic 0600 writes

`[verified: lab atomic on macOS and Alpine]` `os.CreateTemp(dir, ".name.tmp-*")` creates with `O_RDWR|O_CREATE|O_EXCL` and mode 0600; then `Chmod(0600)` (belt and braces), `Write`, `Sync`, `Close`, `os.Rename(tmp, path)`, then open and `Sync` the directory. Two consecutive writes left one file with mode 0600 and the second content, no leftovers, under `umask 022`. Pidfiles: `os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)`; a second attempt fails with `errors.Is(err, os.ErrExist)`. World-readable refusal: `perm&0o077 != 0` (a 0644 file is refused with `config`, U-10). Profile directories: `os.MkdirAll(dir, 0o700)` → mode 700.

## 15. XDG directories

`os.UserConfigDir()` returns `/Users/rjae/Library/Application Support` on macOS and `~/.config` on Linux; `os.UserCacheDir()` returns `~/Library/Caches` `[verified]`. The plan (3.2) wants `~/.config/brigade` on both OSes, so the resolver is our own: `$BRIGADE_CONFIG_DIR` if absolute, else `$XDG_CONFIG_HOME/brigade` if `XDG_CONFIG_HOME` is absolute (the XDG spec says relative values must be ignored; a relative `XDG_STATE_HOME=relative/state` was ignored in the lab), else `~/.config/brigade`; state: `$BRIGADE_STATE_DIR` → `$XDG_STATE_HOME/brigade` → `~/.local/state/brigade`; the hook passes `BRIGADE_STATE_DIR=$CLAUDE_PLUGIN_DATA` as the plan says. The bootstrap's cache for humans without `CLAUDE_PLUGIN_DATA` is `$XDG_CACHE_HOME/brigade` → `~/.cache/brigade` (section 22).

## 16. Structured redacting logging with `log/slog`

`[verified: lab slog]` A `slog.NewJSONHandler` with `HandlerOptions.ReplaceAttr` that (1) replaces the value of any attr whose key is one of `token, secret, authorization, apikey, access_token, refresh_token, join_secret, password` (case-insensitive) with `[redacted]`, (2) runs every string value and every `error` value (after `Value.Resolve()`) through regexps for JWTs (`eyJ…​.…​.…`), join secrets (`brg1.<uuid>.<32 hex>`), `sb_secret_…`, `Bearer …`, and exact matches for `CLAUDE_CODE_MESSAGING_TOKEN` and other known secrets. `ReplaceAttr` is also called for the built-in `msg` attr, so a secret in the message text is redacted, and for attrs inside `slog.Group` (`http.Authorization` was redacted). Output lines are NDJSON with `time`, `level`, `msg`.

Pitfall demonstrated: `log.Info("...", "req", struct{Token string}{jwt})` printed the JWT verbatim, because the handler JSON-encodes a `KindAny` struct without visiting its fields. Policy: never pass structs, maps or slices to the logger; log scalar attrs, and wrap errors with a helper. A `forbidigo` rule bans `slog.Any` outside `internal/log`, and the handler additionally stringifies unknown `KindAny` values with `fmt.Sprint` and redacts the result (a cheap second net that still would not catch a nested map key, hence the rule). Log destinations: stderr for CLI commands (the harness captures at debug), the 0600 log file for the watcher, rotated by size (rename at 5 MB).

## 17. Signal handling

`[verified: lab signals]` `signal.Notify(ch, syscall.SIGTERM, syscall.SIGINT, syscall.SIGHUP)` in the watcher and in `message watch`; the child handled SIGTERM and exited 0 in 2 ms. An unhandled SIGTERM yields `signal: terminated` with `ExitCode() == -1` in the parent. `signal.NotifyContext` is the idiomatic wrapper when only cancellation is needed. The watcher's shutdown path (stop heartbeats, `close` command or `session close`, close the child, remove the pidfile) runs on SIGTERM/SIGINT and on the liveness triggers; SIGHUP is registered for completeness but cannot come from a terminal because of `Setsid`. Go's runtime turns a write to a closed stdout/stderr pipe into a SIGPIPE death (exit by signal, which the harness maps to `unavailable`); writes to other fds return `EPIPE` as an error. Nothing catches SIGKILL, which is what lease expiry is for.

## 18. Cross-compilation, sizes, reproducibility, goreleaser

### 18.1 Measured sizes (bytes; `CGO_ENABLED=0 -trimpath -buildvcs=false -ldflags "-s -w -X main.version=0.1.0"`) `[verified]`

| Binary | darwin/arm64 | darwin/amd64 | linux/amd64 | linux/arm64 |
| --- | --- | --- | --- | --- |
| hello + net/http + coder/websocket | 5,797,794 (gzip 2,411,249) | 6,244,736 (2,618,617) | 6,103,200 (2,576,667) | 5,701,792 (2,325,248) |
| + json/v2, slog, os/exec, sha256, regexp, bufio, unix net, flag | 6,913,426 (2,867,327) | 7,470,512 (3,124,710) | 7,323,808 (3,083,868) | 6,815,904 (2,773,693) |

darwin/arm64 comparisons: hello + `flag` 1,655,666; hello + cobra 2,522,114; the sizeprobe without `-s -w` 8,598,530; with Go's default flags 8,615,058. `file` reports `Mach-O 64-bit executable arm64/x86_64` and `ELF 64-bit LSB executable ... statically linked ... stripped`. The darwin binaries are ad-hoc linker-signed (`codesign`: `flags=0x20002(adhoc,linker-signed)`, `Signature=adhoc`) with `minos 13.0`; they ran on this machine after a curl download with only a `com.apple.provenance` xattr and no quarantine flag `[verified]`. The linux binaries ran unmodified in Alpine (no libc). Expect the real `brigade` (Supabase client, sanitiser with x/text tables, watcher) at roughly 8-9 MB per target, 3-4 MB gzip; GitHub serves raw release assets uncompressed, so first-use download is about 8 MB.

HTTPS and WSS from the CGO_ENABLED=0 binaries `[verified]`: on macOS, `https://example.com` returned 200 and `wss://ws.postman-echo.com/raw` completed the handshake (system roots are read without cgo); in `alpine:3.20` (ships `/etc/ssl/certs/ca-certificates.crt`) the same succeeded; in `debian:bookworm-slim` and `ubuntu:24.04` base images, which carry no CA bundle, the request failed with `x509: certificate signed by unknown authority`. A Go static binary reads the system CA store at run time; the adapter must map `x509.UnknownAuthorityError`/`tls` verification errors to `unavailable` with the hint "install ca-certificates" (WSL desktop installs of Ubuntu have it; container images often do not).

### 18.2 Reproducibility `[verified]`

sha256 of the darwin/arm64 sizeprobe across builds:

| Build | Hash equal to the reference? |
| --- | --- |
| reference: `-trimpath -buildvcs=false`, from `go-lab` | `60fa67e4…` |
| same command, second run | identical |
| same flags, from a copy of the tree at a different path | identical |
| same flags, inside a git repository with a clean tree | identical |
| `GOFLAGS=-mod=mod` | identical |
| goreleaser 2.18.0 (same flags and ldflags), all four targets | identical to plain `go build` for every target (`diff` of `checksums.txt`: none) |
| without `-trimpath` (same path vs other path) | two different hashes (the build path is embedded) |
| without `-buildvcs=false` in a git repo, clean | different (`vcs.revision`, `vcs.time`, `vcs.modified=false` stamped) |
| same, dirty tree | different again (`vcs.modified=true`) |
| without `-s -w` | different (and 2.8 MB larger) |

Rules: identical Go toolchain (pinned by go.mod + setup-go), `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`, ldflags exactly `-s -w -X main.version=<v>` (no commit, no date), default `GOAMD64=v1` and `GOARM64=v8.0` (what both `go build` and goreleaser used). goreleaser's docs confirm none of these are defaults: env is `os.Environ() ++ env`, flags default empty ("for reproducible builds ... pass `-trimpath` to `flags`"), default ldflags are `-s -w -X main.version={{.Version}} -X main.commit={{.Commit}} -X main.date={{.Date}} -X main.builtBy=goreleaser` (the commit and date would break local reproduction), `mod_timestamp` defaults to build time (it only affects the file mtime, set it to `{{ .CommitTimestamp }}` anyway) `[verified: https://goreleaser.com/customization/builds/go/]`.

### 18.3 `.goreleaser.yaml` (validated by `goreleaser check`; used for the dry run) `[verified]`

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
    env:
      - CGO_ENABLED=0
    flags:
      - -trimpath
      - -buildvcs=false
    ldflags:
      - -s -w -X main.version={{ .Version }}
    mod_timestamp: "{{ .CommitTimestamp }}"
    targets:
      - darwin_arm64
      - darwin_amd64
      - linux_amd64
      - linux_arm64
archives:
  - id: raw
    formats:
      - binary
    name_template: "{{ .ProjectName }}_{{ .Version }}_{{ .Os }}_{{ .Arch }}"
checksum:
  name_template: checksums.txt
  algorithm: sha256
changelog:
  use: git
  sort: asc
  filters:
    exclude:
      - "^docs:"
      - "^chore:"
      - "^test:"
release:
  github:
    owner: appshapes
    name: brigade
  draft: false
  prerelease: auto
  mode: replace
  make_latest: true
```

Dry-run facts (`goreleaser release --skip=publish --clean`, 0.34 s with a warm cache): four targets built as `dist/brigade_<os>_<arch>_<v1|v8.0>/brigade`; upload names `brigade_0.1.0_darwin_amd64`, `brigade_0.1.0_darwin_arm64`, `brigade_0.1.0_linux_amd64`, `brigade_0.1.0_linux_arm64`; `dist/checksums.txt` lines are `<sha256>  <upload name>` (two spaces, the `shasum`/`sha256sum` format); `dist/CHANGELOG.md` from `git log`; `dist/metadata.json` with `tag`, `version`, `commit`. goreleaser refuses to run without a git remote (`couldn't get remote URL`) and with untracked files (`git is in a dirty state`), which is why the release workflow runs on a clean tag checkout. `formats` is the current key (`format` is deprecated since v2.6); with the `binary` format "the `name_template` still applies, but only at upload locations like GitHub releases" `[verified: archive docs]`. `ids` replaced `builds` in archives (v2.8+). The GitHub release needs a token with `contents: write`; `prerelease: auto` marks `v1.0.0-rc1`-style tags `[verified: release docs]`.

### 18.4 Version pinning between plugin.json and the tag

`plugin.json` `version` "pins the plugin to that version string, so users only receive updates when you bump it" `[verified: plugins-reference]`. The release tag, `plugin.json.version`, `VERSION=` in `plugin/bin/brigade` and the names in `plugin/bin/checksums.txt` must agree. The flow, all driven by the Makefile (section 21):

1. `make release version=0.1.0`: refuses on a dirty tree; writes `version` into `plugin/.claude-plugin/plugin.json` and `VERSION="0.1.0"` into `plugin/bin/brigade`; runs `make plugin-checksums` (cross-compiles the four binaries locally with the exact release flags and writes `plugin/bin/checksums.txt` from them); runs the seeded `make push message="15: Release v0.1.0"` chain (typecheck, pull, build, test, commit, push); tags `v0.1.0` and pushes the tag.
2. `release.yml` (on the tag): checks out with full history, guards that `v$(plugin.json.version) == $GITHUB_REF_NAME` and that a fresh `make cross-compile` produces byte-identical checksums to the committed file, then runs goreleaser, which builds the same bytes (verified above) and publishes them with `checksums.txt`.
3. A user installing the plugin at that commit gets a bootstrap that downloads exactly the bytes whose hashes are committed; a substituted asset fails the check (section 22).

Since the guard runs before publishing, a non-reproducible build (a toolchain patch release between local and CI, say) fails the workflow instead of shipping a mismatch; the fix is to rerun `make plugin-checksums` on the same toolchain CI uses and retag.

## 19. golangci-lint v2, gofmt/goimports, govulncheck, go vet

Config (`config verify` passed; `run` executed in 1.3 s on the lab) `[verified: golangci-lint 2.13.2]`:

```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard          # errcheck, govet, ineffassign, staticcheck, unused (printed by `golangci-lint linters`)
  enable:
    - bodyclose
    - copyloopvar
    - errorlint
    - exhaustive
    - forbidigo
    - gocritic
    - gosec
    - misspell
    - nilerr
    - noctx
    - perfsprint
    - revive
    - unconvert
    - unparam
    - usestdlibvars
  settings:
    forbidigo:
      forbid:
        - pattern: ^fmt\.Print(ln|f)?$
          msg: stdout carries protocol output only; use the cli printer
        - pattern: ^os\.Exit$
          msg: return an exit status from cli.Main; only main may exit
        - pattern: ^slog\.Any$
          msg: ReplaceAttr cannot redact inside struct/map values; log scalars or use internal/log helpers
    gosec:
      excludes:
        - G204   # subprocess launched with a variable: the adapter command is configuration by design
  exclusions:
    generated: lax
    presets:
      - comments
      - std-error-handling
    rules:
      - path: cmd/                       # main packages may call os.Exit and print usage
        linters: [forbidigo]
      - path: internal/cli/print.go
        linters: [forbidigo]
formatters:
  enable:
    - gofmt
    - goimports
  settings:
    goimports:
      local-prefixes:
        - github.com/appshapes/brigade
```

Notes: in v2 `golangci-lint run` reports formatter findings as issues (a `gofmt` issue appeared in the lab run), so `make lint` covers formatting; `golangci-lint fmt` rewrites, `--diff` previews; `gofmt -l .` remains a zero-config fallback. Install: the docs' command is `curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(go env GOPATH)/bin v2.13.2` (the raw GitHub URL used today works too); Homebrew "can use an unexpected version of Go to build the binary"; "Using `go install`/`go get`, 'tools pattern', and `tool` command/directives installations aren't guaranteed to work" `[verified: install docs]`. CI: `golangci/golangci-lint-action@v9` (Node 24; `install-mode: binary` default; `version: v2.13.2`; it relies on `actions/setup-go` for the module cache) `[verified: action README]`.

`go vet ./...` is the `typecheck` target together with `go build ./...` (vet type-checks every package including tests). `govulncheck` (x/vuln v1.7.0): `govulncheck ./...` exits non-zero only when a vulnerability is reachable from the code ("exits successfully ... if there are no vulnerabilities, and exits unsuccessfully if there are"; JSON/SARIF modes always exit 0) `[verified: pkg doc]`; on the lab it reported 0 reachable and "1 vulnerability in modules you require" (x/text v0.14.0, GO-2026-5970), exit 0; `-mode binary dist/sizeprobe_linux_amd64` scans a release artefact `[verified]`. Run both in CI: source mode on `./...` and binary mode on the built `bin/brigade`. `golang/govulncheck-action@v1` exists (inputs `go-version-file`, `go-package`) but calling the pinned tool through the Makefile keeps local and CI identical.

Dev tools live in `tools.mod`, not go.mod `[verified: modfile-lab]`: `go get -tool -modfile=tools.mod golang.org/x/vuln/cmd/govulncheck@v1.7.0 golang.org/x/tools/cmd/goimports@v0.49.0` wrote a `tool (...)` block and `// indirect` requires into `tools.mod`/`tools.sum` and left go.mod byte-identical; `go tool -modfile=tools.mod govulncheck ./...` then analysed the main module from the repository root (1.6 s); `go tool -modfile=tools.mod goimports -l ./cmd` worked; `go mod tidy` on the main module was a no-op. Without `-modfile`, `go get -tool` pulled x/tools, x/vuln, x/telemetry, x/mod, x/sync into the main go.mod as indirect requirements and let them participate in version selection for the shipped binary, which the dependency policy forbids. golangci-lint stays a pinned binary (its docs), installed by `make setup`.

## 20. GitHub Actions

`ci.yml` (the Node matrix of the plan becomes an OS matrix: the harness has darwin-specific code paths (`ps lstart`, kqueue, `TIOCGETA`) and macOS runners are free for public repos and cheap enough for a private one; Go is pinned, so no Go-version matrix):

```yaml
name: ci
on:
  push: {branches: [master]}
  pull_request: {}
permissions:
  contents: read
jobs:
  fast:
    strategy:
      matrix:
        os: [ubuntu-latest, macos-latest]
    runs-on: ${{ matrix.os }}
    steps:
      - uses: actions/checkout@v7
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache-dependency-path: |
            go.sum
            tools.sum
      - run: make typecheck
      - uses: golangci/golangci-lint-action@v9
        with:
          version: v2.13.2
      - run: make build test            # unit + conformance(fs); Docker-free
      - run: make vuln                  # govulncheck source + binary mode
      - run: make schema-check deps-check plugin-checksums-check
  supabase:                             # Phase 2: Docker, local stack, pgTAP, integration, conformance(supabase), proof.sh
    needs: fast
    runs-on: ubuntu-latest
    if: false                           # enabled when supabase/ lands
    steps: []
```

`release.yml`:

```yaml
name: release
on:
  push:
    tags: ["v*"]
permissions:
  contents: write
jobs:
  goreleaser:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
        with: {fetch-depth: 0}          # required for the changelog
      - uses: actions/setup-go@v7
        with:
          go-version-file: go.mod
          cache-dependency-path: go.sum
      - name: Guard that the plugin pins this tag
        run: |
          test "v$(make -s print-version)" = "$GITHUB_REF_NAME"
          make plugin-checksums-check
      - uses: goreleaser/goreleaser-action@v7
        with:
          distribution: goreleaser
          version: "~> v2"
          args: release --clean
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
```

Versions: actions/checkout v7.0.1 (v5+ needs runner ≥ 2.327.1 / node24), actions/setup-go v7.0.0 (`cache: true` by default keyed on go.mod unless `cache-dependency-path` names the sum files), goreleaser-action v7.2.3 (README example uses `setup-go@v6`, `version: '~> v2'`, `permissions: contents: write`, `fetch-depth: 0` "required for the changelog"), golangci-lint-action v9.3.0 `[verified: GitHub API + READMEs]`.

## 21. Makefile

Seeded conventions kept (lowercase variables, per-target `.PHONY`, `##` doc comments, `commit = typecheck pull build test`, `push = commit + git push`, `pull` becomes `git pull --no-edit` per the user's global rule). New targets map the Go workflow; recipes verified individually today (cross-compile flags, checksums format, tools invocation, plugin dev pointer path).

```make
# ========== Variables (alphabetical) ==========

bin_dir          := bin
detach           := --detach
dist_dir         := dist
go_flags         := -trimpath -buildvcs=false
golangci_lint    := $(bin_dir)/golangci-lint
golangci_version := v2.13.2
goreleaser       := $(bin_dir)/goreleaser
goreleaser_version := v2.18.0
ld_flags          = -s -w -X main.version=$(version)
plugin_data      ?= $(or $(CLAUDE_PLUGIN_DATA),$(or $(CLAUDE_CONFIG_DIR),$(HOME)/.claude)/plugins/data/brigade-inline)
plugin_json      := plugin/.claude-plugin/plugin.json
plugin_version   := $(shell sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' $(plugin_json))
tag              := $(shell git log -1 --pretty=format:"%H")
targets          := darwin/arm64 darwin/amd64 linux/amd64 linux/arm64
tools_mod        := tools.mod
version          ?= $(plugin_version)

# sha256 in the goreleaser/shasum line format on both macOS and Linux
sha256 = $(if $(shell command -v sha256sum),sha256sum,shasum -a 256)

# ========== Setup ==========

.PHONY: setup
setup: ## go mod download, pinned golangci-lint and goreleaser into ./bin, dev tools, .env scaffold, push.autoSetupRemote
	go mod download
	go mod download -modfile=$(tools_mod)
	mkdir -p $(bin_dir)
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b $(bin_dir) $(golangci_version)
	$(MAKE) setup-goreleaser
	@cp -n .env.example .env || true
	docker version >/dev/null
	git config push.autoSetupRemote true

.PHONY: setup-goreleaser
setup-goreleaser: ## Download the pinned goreleaser release binary into ./bin
	gh release download $(goreleaser_version) --repo goreleaser/goreleaser --pattern "goreleaser_$$(uname -s)_$$(uname -m | sed 's/aarch64/arm64/').tar.gz" --dir $(bin_dir) --clobber
	tar -xzf $(bin_dir)/goreleaser_*.tar.gz -C $(bin_dir) goreleaser && rm -f $(bin_dir)/goreleaser_*.tar.gz

# ========== Build / Test / Lint ==========

.PHONY: build
build: ## Build bin/brigade plus the dev-only binaries with the release flags
	CGO_ENABLED=0 go build $(go_flags) -ldflags '$(ld_flags)' -o $(bin_dir)/brigade ./cmd/brigade
	CGO_ENABLED=0 go build $(go_flags) -ldflags '$(ld_flags)' -o $(bin_dir)/brigade-adapter-fs ./cmd/brigade-adapter-fs
	CGO_ENABLED=0 go build $(go_flags) -ldflags '$(ld_flags)' -o $(bin_dir)/brigade-conformance ./cmd/brigade-conformance

.PHONY: clean
clean: ## Remove build artifacts and local caches
	rm -rf $(bin_dir)/brigade* $(dist_dir) coverage.out playwright-report test-results

.PHONY: typecheck
typecheck: ## go build ./... and go vet ./... (every package, including tests)
	go build ./...
	go vet ./...

.PHONY: lint
lint: ## golangci-lint (config verify + run, formatters included)
	$(golangci_lint) config verify
	$(golangci_lint) run ./...

.PHONY: lint-fix
lint-fix: ## golangci-lint --fix and fmt
	$(golangci_lint) run --fix ./...
	$(golangci_lint) fmt ./...

.PHONY: test
test: build ## Unit tests with -race plus conformance(fs); no Docker (what `make commit` runs)
	go test -race -count=1 ./...
	$(bin_dir)/brigade-conformance --adapter $(bin_dir)/brigade-adapter-fs

.PHONY: vuln
vuln: build ## govulncheck on the source tree and on the built binary
	go tool -modfile=$(tools_mod) govulncheck ./...
	go tool -modfile=$(tools_mod) govulncheck -mode binary $(bin_dir)/brigade

.PHONY: deps-check
deps-check: build ## Fail if bin/brigade links a module outside the allowlist in docs/allowed-deps.txt
	go version -m $(bin_dir)/brigade | awk '$$1 == "dep" {print $$2}' | sort | diff - docs/allowed-deps.txt

.PHONY: schema
schema: ## Regenerate docs/protocol-v1.schema.json from the Go types
	go run ./cmd/brigade-schema > docs/protocol-v1.schema.json

.PHONY: schema-check
schema-check: ## Fail if docs/protocol-v1.schema.json is stale (CI)
	go run ./cmd/brigade-schema | diff - docs/protocol-v1.schema.json

# ========== Release ==========

.PHONY: print-version
print-version: ## Print the version pinned in plugin.json
	@echo $(plugin_version)

.PHONY: cross-compile
cross-compile: ## Build dist/brigade_$(version)_<os>_<arch> for every target with the exact release flags, plus dist/checksums.txt
	rm -rf $(dist_dir) && mkdir -p $(dist_dir)
	for t in $(targets); do \
	  CGO_ENABLED=0 GOOS=$${t%/*} GOARCH=$${t#*/} go build $(go_flags) -ldflags '$(ld_flags)' \
	    -o $(dist_dir)/brigade_$(version)_$${t%/*}_$${t#*/} ./cmd/brigade || exit 1; \
	done
	cd $(dist_dir) && $(sha256) brigade_* > checksums.txt

.PHONY: plugin-checksums
plugin-checksums: cross-compile ## Pin plugin/bin to $(version): checksums.txt and VERSION= in the bootstrap
	cp $(dist_dir)/checksums.txt plugin/bin/checksums.txt
	sed -i.bak -E 's/^VERSION=.*/VERSION="$(version)"/' plugin/bin/brigade && rm -f plugin/bin/brigade.bak

.PHONY: plugin-checksums-check
plugin-checksums-check: cross-compile ## Fail if plugin/bin/checksums.txt or the bootstrap VERSION disagree with a fresh build (CI, release guard)
	diff $(dist_dir)/checksums.txt plugin/bin/checksums.txt
	grep -q '^VERSION="$(version)"$$' plugin/bin/brigade

.PHONY: release
release: ## Pin the plugin to $(version), commit through the push chain, tag v$(version) and push the tag (usage: make release version=0.1.0)
	@test -n "$(version)" || { echo "usage: make release version=X.Y.Z"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree is dirty"; exit 1; }
	sed -i.bak -E 's/^([[:space:]]*"version":[[:space:]]*)"[^"]+"/\1"$(version)"/' $(plugin_json) && rm -f $(plugin_json).bak
	$(MAKE) plugin-checksums version=$(version)
	$(MAKE) push message="15: Release v$(version)"
	git tag -a v$(version) -m "v$(version)"
	git push origin v$(version)

.PHONY: release-dry-run
release-dry-run: ## goreleaser check + a local release without publishing (needs a clean tree and a tag on HEAD)
	$(goreleaser) check
	$(goreleaser) release --skip=publish --clean

# ========== Plugin ==========

.PHONY: plugin-dev
plugin-dev: build ## Point the bootstrap at the local build (pointer file in the user's plugin data dir), then start Claude Code with the plugin
	mkdir -p $(plugin_data)
	echo "$(CURDIR)/$(bin_dir)/brigade" > $(plugin_data)/dev-binary
	claude --plugin-dir ./plugin

.PHONY: plugin-dev-off
plugin-dev-off: ## Remove the local-build pointer so the bootstrap uses the pinned release again
	rm -f $(plugin_data)/dev-binary

.PHONY: plugin-validate
plugin-validate: ## claude plugin validate on the plugin root and the marketplace
	claude plugin validate ./plugin --strict && claude plugin validate .

# ========== Git ==========

.PHONY: pull
pull: ## Merge origin into the current branch (plain merge, never rebase; no editor)
	git pull --no-edit
```

`commit`, `push`, `help` and `docker-*` are unchanged from the seeded file. `ld_flags` uses `=` (not `:=`) so `version=` passed on the command line reaches sub-makes. `make test` builds first, as the seeded chain expects.

## 22. Distribution: the `plugin/bin/brigade` bootstrap

Plugins-reference `[verified]`: `bin/` holds "Executables added to the Bash tool's PATH and invokable as bare commands while the plugin is enabled" (not allowed for plugins distributed through claude.ai organisation settings, which is not this project's channel). `CLAUDE_PLUGIN_DATA` is a "persistent directory that survives plugin updates, created on first reference" at `~/.claude/plugins/data/{id}/` (under `CLAUDE_CONFIG_DIR` on this machine; id `brigade-inline` for `--plugin-dir`, `brigade-brigade` for a marketplace install per Appendix A).

The script (kept verbatim in `scratchpad/wf2/research/bootstrap-lab/brigade`) does, in order: resolve its own directory; pick `data = $CLAUDE_PLUGIN_DATA`, else `$XDG_CACHE_HOME/brigade`, else `~/.cache/brigade` (a human running the symlinked script from a terminal); honour a `dev-binary` pointer file under `data` (written only by `make plugin-dev`, i.e. by the user, never by a repository); `exec` the cached `data/bin/brigade-$VERSION` if present; otherwise map `uname -s`/`uname -m` to the four targets (anything else, including native Windows, exits 11 with "use WSL 2"); look up the expected hash for `brigade_${VERSION}_${os}_${arch}` in `checksums.txt` beside the script; `mktemp` in `data/bin`; download with `curl -fsSL --proto '=https' --retry 3 --max-time 120` or `wget -q -T 120`; hash with `sha256sum` or `shasum -a 256`; compare; `chmod 755`; `mv -f` into place; `exec`. Every failure exits 11 (`config`) with a one-line reason on stderr; a `trap` removes the temp file.

Test results `[verified]`: syntax-clean under `sh -n`, `bash -n`, `zsh -n`; macOS (`/bin/sh`, curl, shasum): first run downloaded and printed the version in 0.49 s against a local server; cached run 14 ms wall (the binary alone is 6 ms), no quarantine xattr; `brigade --version` resolves through `PATH` as the Bash tool will see it; a checksums file with a wrong hash → "checksum mismatch ... expected ..., got ..." and exit 11 with nothing installed; the dev pointer executed the local build; without `CLAUDE_PLUGIN_DATA` the binary landed in `$XDG_CACHE_HOME/brigade/bin`; Alpine (busybox ash, no curl, wget + sha256sum): download, verify and exec in 30 ms, cached run under 10 ms. Idempotency: two concurrent first runs each download to their own temp file and `mv -f` the same bytes into place.

`hooks.json` should call the bootstrap through `sh` with the plugin path in `args`, which is the documented substitution point: `{"type": "command", "command": "sh", "args": ["${CLAUDE_PLUGIN_ROOT}/bin/brigade", "hook", "session-start"], "timeout": 15}`. Whether Claude Code substitutes `${CLAUDE_PLUGIN_ROOT}` inside `command` itself is `[uncertain]` (Appendix A only records substitution in `args`); E0-5 records it, and the `sh` form works either way. The first-use download therefore happens inside the `SessionStart` hook: about 8 MB with a 15 s hook timeout. On a slow link the hook may time out; the script is idempotent, the next `UserPromptSubmit` hook or the first Bash call retries, and the hook prints "Brigade: installing the brigade binary" as its context line when it detects a missing cache before starting the download `[design; measure in E0-5]`. `go install github.com/appshapes/brigade/cmd/brigade@v0.1.0` must also produce a binary that reports its version: `main.version` stays "dev" under `go install`, so `internal/buildinfo` falls back to `debug.ReadBuildInfo().Main.Version` `[likely]`.

## 23. Plan deltas this digest implies (sections 3, 6, 7, 8, 9)

- 3.1/3.2: the component table becomes `cmd/brigade` + `internal/*` (section 3.2 here); "Node only", `process.exitCode`, `NODE_OPTIONS`, `spawn(process.execPath, …)` and `.mcp.json` rows disappear; the bundled adapter is `exec.Command(os.Executable(), "adapter", "supabase", …)`.
- 4.1: "The adapter sets `process.exitCode` and returns; it never calls `process.exit()`" becomes "the command returns an exit status to `cli.Main`; only `main` calls `os.Exit`, after stdout is flushed" (`forbidigo` enforces it).
- 6.2/6.4: removed (D34). 6.3: hooks call the bootstrap (section 22). 6.6: the spawn snippet becomes section 11 here; "PID-reuse guard: `ps -o lstart=`" becomes the start-time token (section 12; procfs on Linux). 6.5: `process.ppid` is replaced by `CLAUDE_PID` in the Bash tool environment (decision D34).
- 7.2/7.3: replaced by sections 3, 19, 20; D27/D28 are superseded (no committed build output at all: the plugin directory contains only the sh bootstrap, the checksums and manifests). 7.4: section 21. 7.6: gitignore additions above. 7.7: section 20. 7.8: CLAUDE.md lines about `plugin/dist`, `npm`, `@supabase/*` become: "the shipped binary links only the modules in docs/allowed-deps.txt; dev tools go in tools.mod; `make plugin-checksums` before tagging; never `slog.Any`; stdout is protocol output only".
- 8 (P1-1): the scaffold commit is `go.mod`, `tools.mod`, Makefile, `.golangci.yml`, `.goreleaser.yaml`, both workflows, `docs/allowed-deps.txt`, the bootstrap with an empty checksums file (version `0.0.0`, refused until the first release), and `make setup typecheck lint build test` green on macOS and Ubuntu. CI-01..CI-04 map to `make typecheck lint test`, `deps-check` (no native/unknown modules), `vuln`, and `schema-check`/`plugin-checksums-check` (drift).
- 9.1: "Unit" runs `go test -race ./...`; "Plugin tests" spawn the built `bin/brigade` (hook, watch --sink, cli) against `bin/brigade-adapter-fs` and a fake socket server (`net.Listen("unix")`); the conformance suite is a Go binary with its own runner (it cannot use `testing` outside `go test`).

## 24. Gotchas (each observed today unless marked)

1. `toolchain go1.27.0` written next to `go 1.27.0` breaks every `go build` ("updates to go.mod needed"). Omit the line.
2. stdlib `flag` stops at the first positional; the grammar puts the session id before `--reply-to`. Use `parseInterspersed` (section 4).
3. `bufio.Scanner` stops for good on an overlong line; the spec wants a drop-and-continue reader (section 8).
4. `os.UserConfigDir()` is `~/Library/Application Support` on macOS; XDG paths must be computed by hand (section 15).
5. `slog` `ReplaceAttr` never sees the fields of a struct/map passed through `slog.Any`; a JWT inside such a value was printed verbatim. Ban `slog.Any`, log scalars.
6. `cmd.Wait` after a context timeout returns `signal: terminated`, not `context.DeadlineExceeded`; check `ctx.Err()`.
7. A grandchild that inherits the child's stdout makes `Wait` block until `WaitDelay` (`exec.ErrWaitDelay`); adapters must not hand their stdout to background processes.
8. Static Linux binaries need a system CA bundle: `debian:bookworm-slim` and `ubuntu:24.04` base images fail TLS with `x509: certificate signed by unknown authority`; Alpine 3.20 and desktop distributions have one. Map the error to `unavailable` with a hint.
9. busybox `ps` has no `-o lstart`; Linux must read `/proc/<pid>/stat` (field 22, parsed after the last `)`).
10. `/dev/null` is a character device: `os.ModeCharDevice` is not a TTY test; use a termios ioctl or `x/term.IsTerminal`.
11. json/v2 silently ignores a wrongly-cased member (no error, zero value) and rejects duplicate names; `Validate()` catches the first, keep the second.
12. json/v2 does not HTML-escape (`<`, `>`, `&`, U+2028 are emitted raw); valid JSON, but different bytes from v1; `jsontext.EscapeForJS(true)` exists if a consumer needs the old form.
13. golangci-lint must be a pinned binary; `go install`/`go tool` are explicitly unsupported by its docs. Keep it out of `tools.mod`.
14. `go get -tool` without `-modfile` drags x/tools, x/vuln, x/telemetry into the main module graph; always `-modfile=tools.mod`.
15. `invopop/jsonschema` brings three transitive modules and `santhosh-tekuri` brings `x/text`; keep both out of `cmd/brigade` and let `deps-check` prove it; pin `x/text` ≥ v0.39.0 (GO-2026-5970).
16. goreleaser refuses a repo without a remote and a dirty tree (untracked files count); its default ldflags embed commit and date, which would defeat local reproduction; `-trimpath`/`-buildvcs=false` are not defaults.
17. `-buildvcs` stamping changes the binary on every commit and with a dirty tree; `-trimpath` alone is not enough for cross-path reproducibility (verified both ways).
18. macOS has no `timeout(1)`; Makefile recipes must not rely on it (or use a Go helper).
19. Ports on the developer machine can be occupied by unrelated processes (a stale Python server on 8765 produced misleading 404s during the bootstrap test); test servers should bind a random free port.
20. `-race` works with `CGO_ENABLED=0` on darwin/arm64 (Go 1.27) `[verified]`; on Linux CI runners cgo is available anyway. Do not set `CGO_ENABLED=0` globally in the Makefile for `go test`; set it per build command.

## 25. Open questions

1. Does Claude Code substitute `${CLAUDE_PLUGIN_ROOT}` in a hook's `command` string, or only in `args`? (E0-5; the `sh` form sidesteps it.)
2. First-use download inside the 15 s `SessionStart` budget on slow networks: measure; decide whether the hook should print "installing" and return (retry on the next hook) instead of waiting.
3. `x/text` version to pin: v0.39.0 is the fix version govulncheck names; confirm the latest tag on scaffold day (`go list -m -versions golang.org/x/text`).
4. Whether to add `homebrew_casks` to goreleaser for the human install path (Phase 5; not exercised).
5. Whether E0-3 needs `jsontext.EscapeForJS` for the socket frame (raw U+2028 in JSON strings is standards-valid; rendering in Claude Code is unmeasured).
6. `go install ...@version` fallback via `debug.ReadBuildInfo` (marked likely; verify when `cmd/brigade` exists).
7. macOS CI runner: `macos-latest` is arm64; whether to also test darwin/amd64 under Rosetta is a cost question (the code has no amd64-specific paths).

## 26. Files produced by this research

- `scratchpad/wf2/research/go-lab/` — lab module, `.golangci.yml`, `dist/` with all measured binaries, `lab` sources (NDJSON reader, capped exec, detach, pidfile, flock, atomic write, redacting handler, XDG, schema).
- `scratchpad/wf2/research/release-lab/.goreleaser.yaml` and `dist/` — the validated release configuration and its dry-run output (`checksums.txt`, `artifacts.json`, `CHANGELOG.md`).
- `scratchpad/wf2/research/release-plain/checksums.txt` — the plain `go build` checksums that matched goreleaser's.
- `scratchpad/wf2/research/bootstrap-lab/brigade` — the bootstrap script as tested (with `BASE` pointing at GitHub Releases).
- `scratchpad/wf2/research/modfile-lab/tools.mod` — the tools module produced by `go get -tool -modfile`.
