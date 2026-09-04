# Brigade execution log — archive

Finished-phase reports and the plan corrections that preceded Phase 4, moved out of the live log so it
carries only work still in flight; the live log is `.context/plans/brigade-execution-log.md`.
Every section title below is unchanged, so a reference of the form `see "P1-5 DONE"` or
`see "Plan corrections from E0-8"` resolves here. Moved 2026-09-04.

<!-- verbatim from the execution log -->
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

## P1-2 DONE — the two `ndjson.go` defects are now CLOSED

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

**FIXED in `e449197`.** Both had one root cause: the tolerance was applied to the RAW byte count including
the terminator rather than to the CONTENT length after trimming, so `\r\n` spent the one-byte allowance twice
while the EOF path spent it on a terminator that was not there. The accumulation bound now carries the longest
terminator (`MaxLineBytes+2`) and a single `deliver` helper applies the cap to the trimmed content, so the two
exit paths cannot drift apart again. `TestLineReaderContentCapBoundary` now covers the full matrix — cap-1,
cap and cap+1 against each of LF, CRLF and EOF — and reverting the reader to its original logic turns exactly
two of those nine cells red (`cap/CRLF` wrongly dropped, `cap+1/EOF` wrongly delivered) and no others, which
is the positive control the original tests never had.

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

## P1-3 DONE — the E0-6 flock correction is now evidenced, and section 4.6 has a measured defect

Full gate green. Three authors plus one adversarial pass; **0 surviving mutations at hand-off**.

**The E0-6 correction works, and it is measured rather than asserted.** 5.1 prescribes `LOCK_NB` polled every
100 ms; E0-6 measured that this rounds ANY contention up to a whole quantum against a 36 ms hold (min 100.2 /
median 101.1 / max 303.1 ms). The implemented fix (5 ms retry, the 10 s bound kept) gives, over **two real
processes**, 47 of 80 contended at **min 5.66 / median 16.96 / max 68.40 ms** — the forbidden 100 ms-quantum
signature is gone. The 10 s `unavailable` bound fired at 10.0055 s against a 13 s holder in another process.
**5.1's text still prescribes the 100 ms poll and should be corrected when P2-6 touches it.**
Note the trap the tests avoid: `flock` is per open FILE DESCRIPTION, so two goroutines in one process can
appear to serialise while the real cross-process property is broken. Exclusivity and the bound are both proven
across two real processes; the same-process test is a fast path only.

**Plan 4.6 has a measured defect.** It says a missing executable is detected with
`errors.Is(err, os.ErrNotExist)`. Measured on go1.27.0: that predicate is **false for a PATH-searched name** —
`exec.ErrNotFound` does not wrap `fs.ErrNotExist` — and true only for an absolute-path miss. So the prescribed
predicate misses the common case, and an adapter that is simply not on PATH would map to `internal` rather
than `unavailable` / `adapter_not_found`. Fix the wording in P1-4.

**Two mutations survived the authors' suite and were killed by the pass.** The sharper one: dropping attribute
**KEY** redaction passed the ENTIRE suite including the fuzz target, because the fuzzer strips canary fragments
from its key operand — so the instrument was structurally blind to the property it was supposed to guard. This
is the third task running in which the *instrument*, not the code, was the defect (P1-1's tautological
buildinfo test and unguarded `tscmd.Status`; P1-2's sanitiser suite that a no-op could pass).

**`slog.AnyValue` evaded the scalar-only policy; now closed.** The whole reason `slog.Any` is banned is that
`ReplaceAttr` cannot redact inside a wrapped struct — but `slog.Attr{Value: slog.AnyValue(x)}` is the same hole
by another name and the `^slog\.Any$` pattern did not match it. Verified evadable, then verified caught.
Two related limits are documented rather than fixed, and belong in the threat model: `ReplaceAttr` never sees
group NAMES (a secret used as a group name cannot be redacted by any handler — they are compile-time constants
here), and the redactor can leak under match composition when JWT-shaped junk is glued directly before a real
token.

**Ratified (the plan is silent, so this was a judgment call and it stands):** a RELATIVE `BRIGADE_CONFIG_DIR`
or `BRIGADE_STATE_DIR` is **refused** with `config` (exit 11). 3.2/6.2 give the absolute-only rule for `XDG_*`
and `HOME` but say nothing about the `BRIGADE_*` pair. Honouring a cwd-relative state directory is the exact
hazard 6.2 describes; silently ignoring it would betray a user who set it deliberately. The harness always
computes absolute values, so no harness path is affected.

**Correction to this driver's own task brief, not to the plan:** the workflow prompt assigned U-08, U-09, U-10
and U-23 to every P1-3 lane. Per 9.9 only **U-10** (atomic write / mode refusal) lands in adapterkit's core;
U-08 belongs to the CLI's join-secret-argv txtar and `internal/cli`, and U-09 and U-23 to
`internal/adapterkit/log`. All are covered in their real homes; the mis-assignment cost nothing but is worth
recording so P1-6 maps the ids correctly.

## P1-4 DECISIONS — Rjae settled the ten open section-4 questions (2026-09-01)

These are the owner's decisions and they are final for v1. `docs/protocol-v1.md` is written against them.

| # | Question | Decision |
| --- | --- | --- |
| 1 | `team_name` and `workspace_label` have no cap in `limits` (4.5.11 promises one) | **Add two members**: `max_team_name_codepoints` = 64 (a team name is a frame tag attribute, and 6.7 rule 4 caps those at 64) and `max_workspace_label_chars` = 128 (it is a label, like `human_label`). Published in `describe`, so programs read the number rather than prose. |
| 2 | `retryable` is REQUIRED but unenforceable under loose parsing | **Adapters MUST send it; consumers treat absent as `false`; consumers SHOULD derive retryability from `code`** — only `rate_limited` (8) and `unavailable` (9) are ever retryable, so the flag is advisory. Absent-as-false fails safe. No `*bool`. |
| 3 | The `invalid_input` `details` key naming the offending member is unspecified | **`details.field`**, value = the wire member name (JSON name, dotted for nesting, e.g. `resume.session_id`). Companions blessed: `reason`, `limit`, `actual`, `unit`. |
| 4 | `ready.mode` values and `status.state` set never enumerated | **`mode` ∈ {`push`, `polling`}; `status.state` ∈ {`live`, `polling`}, unknown states ignored.** An adapter that omits `message.watch.push` MUST send `mode: polling` (checkable by C-33). |
| 5 | `"unknown": []` shown present while the tag convention is `omitzero` | **General rule: a REQUIRED array is always present, `[]` when empty; `omitzero` applies only to OPTIONAL members.** Settles `unknown`, `acked`, `capabilities`, `messages`, `sessions` at once. |
| 6 | `…` placeholders inside typed members in the 4.4 examples | Editorial: real RFC 3339 values in typed members; `…` only in opaque strings. |
| 7 | "unknown fields survive" vs "unknown members are ignored" | Editorial: unknown members are **accepted and ignored**; they do **not** survive re-serialisation. |
| 8 | The JSON Schema emits zero `required` arrays | Emit `required` for members `Validate()` requires; the spec states the schema is **advisory** and `Validate()` is normative. |
| 9 | 4.6's `errors.Is(err, os.ErrNotExist)` is false for a PATH-searched name (measured, go1.27.0) | The predicate also matches `exec.ErrNotFound`; 4.6's text is corrected. |
| 10 | 4.1 names `BRIGADE_<ADAPTER>_*` but 3.2's from-scratch child environment never carries them | Documented: adapter-specific variables **never arrive under a live session**; adapter configuration comes from the profile file or `adapter_command` fixed args. |

**Owner directive recorded in the same review.** Rjae raised a concern that the design had drifted from her core
value — simple for the people using it — toward over-architecture, then withdrew it on reflection: **"we must
design assuming that any adapter — current or future — can be used"**, and there is **no plan to cut anything
unless it is proven to hinder Brigade**. The distinction that settled it: everything in P1-1..P1-3 and all ten
questions above are internal wire format; the member's three steps (install, `profile init`, `team join`) have
not grown since Phase 0 measured them.

## P1-4 DONE — BAP/1 is frozen; the owner review was WAIVED, and here is what stood in for it

`docs/protocol-v1.md` is the authority from this commit on; plan section 4 is history. `docs/adapter-authors.md`
is a skeleton for P1-7. `internal/protocol` and `docs/protocol-v1.schema.json` were changed in the same commit,
per the CLAUDE.md wire-shape rule (the conformance-suite and adapter halves of that rule are vacuous until P1-6
and P2). Full gate green.

**The review gate.** P1-4's acceptance criterion is "reviewed by the user". On 2026-09-01, going to sleep, Rjae
instructed the driver to **skip her spec review** and hand off to `15-implement-brigade-0902`. That is the
owner's call in her own words and it is recorded as a WAIVER, not a skipped gate. What stood in for it: the
adversarial pass was briefed with an explicit "nothing smuggled in" rule — any MUST, member, cap or code present
in the spec but in NEITHER plan section 4 NOR the ten decisions is reported as undecided, never silently
accepted. It found four. The driver resolved all four in the one safe direction under a waived review: **remove
or downgrade anything that adds an obligation nobody decided.** Each is reversible by a one-paragraph edit if
Rjae disagrees on reading the spec:

1. **4.4.1 said "an adapter MAY advertise tighter values but the caps it enforces are the ones it advertises."**
   Undecided, and it CONTRADICTS the suite as written — C-16 asserts a 64-code-point name passes and C-27 a
   16,384-byte body passes, so a tighter adapter would fail both. Rewritten: the v1 constants are the caps every
   adapter enforces, and an adapter may not advertise or enforce a tighter one. Matches the code, which enforces
   the constants absolutely.
2. **4.4.6 froze `details.reason = "forbidden_member"` inside a MUST**, while decision 3 blessed the KEY `reason`
   and 4.3.1 says the tokens are informative. The token moved out of the MUST into a "the reference
   implementation reports" sentence.
3. **Two P1-3 behaviours had entered the frozen text from code rather than from a decision.** (i) The exit-2 row
   of the 4.6 table had grown "stdin is a terminal on a command that reads a document"; the row is back to the
   plan's wording and the TTY refusal is described below the table as an `adapterkit.ReadInput` courtesy, not
   a protocol obligation. (ii) Convention 1 stated duplicate-member rejection as an adapter obligation with a
   C-02 citation; invalid UTF-8 keeps the MUST (a non-UTF-8 document is not a document), duplicate members are
   now a SHOULD described as the reference codec's behaviour.
4. **Decision 8 was implemented as a superset** — the schema's `required` arrays list every member without
   `omitzero`, which includes but is wider than "members `Validate()` requires". Convention 6 says so openly and
   the schema is advisory by the same decision. Accepted as-is.

**For P1-6 — eleven MUSTs have no conformance case, and three citations lean on a neighbour.** The spec marks
each `[no case: B-n]` inline and its Appendix B lists them. The ones that matter most: B-2 (`retryable` MUST be
sent — now that the Go type is a plain `bool` per decision 2, the suite's own parser no longer catches absence,
so C-02/C-37 need an explicit presence assertion); B-1 (a no-input command MUST NOT read stdin — nothing holds
stdin open); B-11 (adapters never exit 126/127/≥128 — nothing asserts the observed status is in 0..12). The
three neighbouring citations to tighten: unknown-flag→C-02, `mode: polling`→C-33, six forbidden members→C-23.

**Verifier corrections applied to the spec before this commit:** 4.3.1's informative reason list gained
`read_error` and `terminal` (the code emits them); convention 7 now says precisely that 27 of the 31 examples are
the testdata files byte-for-byte and the other four are composite results with no Go type, pinned two ways by a
new `TestSpecExamplesAreTheTestdataFiles` with three demonstrated kill paths; Appendix A's capability suffixes on
C-03/C-03b/C-04/C-08 were restored. Decision 9 was proven with real processes (a bare name off `PATH` and a
missing absolute path both map to `unavailable` / `adapter_not_found`).

## Phase 1 corrections found on P1-5 entry (2026-09-02, session `15-implement-brigade-0902`)

1. **CI was RED for three commits (P1-3, the P1-4 decisions, P1-4) and nobody had looked.** The `fast` job failed at
   `make lint` on `internal/adapterkit/pty_linux_test.go` (gosec G103 on two `unsafe.Pointer` ioctl operands) in runs
   33550171587, 33578789887 and 33581196998, while the `macos` job was green and every local gate was green. The
   file is `//go:build linux`, so a native `make lint` on this darwin machine never loaded it: the "full gate green"
   claims in the P1-3 and P1-4 rows were true of the local gate and false of CI. Fixed by an audited `//nolint:gosec`
   on the two lines and — the part that matters — `make lint` now runs golangci-lint under `GOOS=darwin` AND
   `GOOS=linux`, so a finding in a build-constrained file fails locally. Reproduced locally with
   `GOOS=linux bin/golangci-lint run ./...` before the fix (2 issues) and green after. Cost: about 3 s. Lesson for the
   task-boundary ritual: read the CONCLUSION column of `gh run list` after every push, not just that a run exists.
2. **`internal/protocol` bounded `lease_seconds` by the 4.4.1 EXAMPLE's 30..600 in every request shape's
   `Validate()` and in the schema.** The frozen spec makes the range the adapter's: 4.4.1 "`lease` is the range of
   `lease_seconds` an adapter accepts", 4.4.2/4.4.4 "within `lease.min_seconds..lease.max_seconds`", and the harness
   learns it from `describe`, never at compile time. The P1-5 row requires the fs adapter to advertise
   `lease.min_seconds = 1` (so the slow expiry of C-14/C-19b takes seconds), which the P1-2 check made unreachable:
   the request would have been refused before the adapter saw it. Corrected without a spec change: the shapes'
   `Validate()` now requires only a positive integer (`details.min = "1"`), a new `Lease.CheckSeconds(field, *int)`
   applies the adapter's advertised range with the same `out_of_range` details, `DefaultLease()` still carries the
   4.4.1 values, and the schema's `lease_seconds` says `minimum: 1` with no maximum (regenerated with `make schema`;
   convention 6's advisory-schema rule covers exactly this — a static schema cannot carry an adapter's bound).
   `TestLeaseCheckSeconds` proves the check reads the RECEIVER's bounds, not the constants. Protocol and schema in
   one commit per the CLAUDE.md rule; the adapter and conformance halves are P1-5 and P1-6.

## P1-5 DONE — the fs adapter; the plan's 500-line estimate was off by six; the lint gate had a hole

One Opus author, one Opus adversarial verifier, one Opus fixer, driven from `.ignored/briefs/p1-5-fs-adapter.md` (a
cold-readable design brief the driver wrote from the frozen spec; the verifier's full case table is saved beside it
as `p1-5-verifier-report.md` for P1-6). Full gate green, `go test -race -count=3` stable. `internal/adapters/fs`
(21 source files, README, three build-tagged mutant pairs), `cmd/brigade-adapter-fs` (the stub replaced), three
txtar scripts (`fs-team`, `fs-session`, `fs-message`; P1-7's examples). Capabilities: everything but
`message.watch.push`; `lease.min_seconds = 1`; 200 ms polling watch with stdin commands; a store-wide flock; the
retention sweep once per command.

**The verifier stood in for the conformance suite and drove every case** — C-01..C-43 with C-03b/C-19b/C-29b, and
B-1/B-2/B-5/B-6/B-7/B-8/B-11 of Appendix B — against `bin/brigade-adapter-fs` as a real child process with a
from-scratch environment at `BRIGADE_LOG_LEVEL=debug`: 1,241 adapter runs, every exit status in 0..12 (histogram
`0:1126 2:15 3:33 4:9 5:9 6:12 7:9 8:8 11:17 12:3`), every failing envelope carrying the `retryable` KEY in the raw
JSON, no join secret on any stderr or in any file. It then mutated the six most important subjects and confirmed
the author's tests bite. What it found, in the order that matters:

1. **DECISIVE, code: `team create --secret-file` honoured a RELATIVE path and wrote the secret AFTER binding.**
   Measured: `--secret-file relative-secret.txt` exited 0 and dropped a live `brg1.` secret in the working
   directory — the first run of the new test deposited one INSIDE the repository, exactly what CLAUDE.md forbids;
   and an unwritable path exited 1 `internal` with the profile already bound to a team whose secret was gone, so
   every retry answered `conflict profile_bound` and nobody could ever be invited. Fixed: a relative path is
   `usage` before anything is read or created; the file is written before `team.json`, the member file and the
   binding; a write failure is `config` (`details.reason = "secret_file_unwritable"`), leaving the profile
   `not_member` and the store empty.
2. **DECISIVE, instrument: the 4.5.12 ORDER of the two unacked caps was covered by no test.** Swapping the checks —
   the exact rule 4.5.12 forbids, "one sender exhausts a recipient's inbox for everyone else" — left the entire
   suite green. Fixed with a test that seeds both caps at once and fails under the swap. The `retry_after_ms ≥ 1`
   floor was untested too (a sub-millisecond remainder rounds to 0, which C-28 forbids); fixed the same way.
3. **Two team-isolation gaps, found by the verifier, closed by the fixer with failing-first tests.** A running
   `message watch` never re-authorised: after `team leave` it kept emitting messages (4.5.7 says a revoked
   principal is `unauthorized` on EVERY verb that touches the team). Now every poll and every stdin command
   re-applies the membership check; a revocation ends the watch with one `unauthorized` error event and exit 5
   within a poll interval (the command-handler half is covered only through the drain's test, because the two paths
   converge within 200 ms). And `message send` to a session whose owner had left was accepted into a dead inbox
   (4.5.6: a session outside the team "cannot be messaged", and C-08 already hides it from `session list`); now the
   uniform `not_found`, byte-identical to an unknown id.
4. **The lint gate did not lint the three real twins.** `.golangci.yml` set `run.build-tags` to ALL THREE mutant
   tags at once, which excludes every `//go:build !mutant_*` file from the build golangci-lint sees — so
   `store_ack.go`, `store_list.go` and `store_send.go` had never been linted, the opposite of the plan 7.3
   comment's intent. Measured both ways with a planted unused function (0 issues from the config-tagged run; 1 from
   the corrected one). `make lint` now runs the plain build plus one run per tag on `internal/adapters/fs`, with
   the tags on the command line (the config line would override them). Together with the GOOS finding above, the
   lint gate had two separate blind spots this session; both are now measured closed.

**Plan corrections (all measured):**

- **P1-5's "source under about 500 lines excluding mutants" is off by six.** Non-test, non-mutant Go is 3,181 lines
  (about 2,500 without comments and blanks); mutants 133; tests about 2,700. Nothing in the spec was cut to chase
  the estimate, per Rjae's directive. The estimate belongs to a sketch of the adapter, not to the protocol as
  frozen: twenty commands, a polling watch with stdin commands, four rate windows and two caps, explicit and
  implicit hop counting, idempotency, resume, retention and a four-state profile machine.
- **C-40 as written ("100 messages sent while the watch is stopped") is unsatisfiable through the protocol on ANY
  conforming adapter**: `limits.max_unacked_per_recipient` is 60, so the 61st unacknowledged message to one
  recipient MUST be `rate_limited`. The verifier produced 60 through `message send` and wrote 40 more into the
  store directly (all 100 were emitted, so the drain has no page limit). P1-6 writes the case at 60 (above the
  50-message default page of `message receive`, which is what "paging works" tests).
- **"`mutant_noack` fails exactly C-30 and C-36" cannot hold.** C-29b's implicit chain must ping-pong on ONE
  session pair (4.5.12 keys the implicit hop on the pair), so reaching `max_hop_count = 32` puts 17 messages on
  one pair, two past `max_unacked_per_sender_recipient = 15`; only `message ack` gets through, so a no-ack adapter
  necessarily fails C-29b (measured: the chain stops at hop 30 with `sender_quota_for_recipient`). C-28 and C-29
  CAN be written ack-free (several recipients for the rate windows; a fresh session pair per hop for the explicit
  chain) and then survive the mutant. P1-6's expected set for `mutant_noack` is `{C-29b, C-30, C-36, C-41}` (C-41's
  restart check is a stdin-ack check), asserted EXACTLY; `teamleak` → `{C-12, C-26}` and `trustsender` →
  `{C-23, C-24}` were confirmed exact.
- **The frozen per-principal budget (60/min) is smaller than the suite's own appetite**: one heavy case drains a
  principal for a minute and the whole fs run takes seconds, so the plan's three-principal fixture cannot carry
  C-28 and C-40 back to back. P1-6 provisions fresh principals per heavy case through `team join` (and skips those
  cases, with a reason, on a `--setup`-provisioned adapter that lacks `team.join`).

**BAP/1.x questions for Rjae, recorded and NOT changed (the spec is frozen):** (a) 4.4.9 keys the watch's SURVIVAL
on `error.retryable` while 4.3 makes the flag advisory and derivable from `code` — a rejected `heartbeat` command is
`invalid_input` (never retryable by code) yet must not kill the watch, so the fs adapter sends `retryable: true`
there; the text should say the watch's flag means "fatal or not". (b) `describe` carries no poll-interval member,
so C-35's "two poll intervals" cannot be read by the suite; P1-6 uses 5 s for polling adapters too. (c) Whether a
watch must re-check membership per drain and whether a revoked member's session is "outside the team" for `send`
— the fs adapter now answers yes to both; Supabase's RLS will behave the same; the text could say so. (d) 4.4.5's
example and the testdata file show `"reply_to": null` while the Go type omits an absent `reply_to` (both legal
under convention 4). (e) A `message watch` failure raised during argv setup (a poison flag, a bad leading
`--log-level`) answers with a 4.3 envelope, while one raised after dispatch answers with an NDJSON `error` event —
one line either way, and C-37 passes, but two shapes on one command.

**Adapter decisions recorded (all cited to the spec by the author; none re-opens a frozen shape):** identifiers used
as path components are accepted only as `[A-Za-z0-9_-]{1,64}` (anything else is simply "not found", so no `..`
can escape the root); the idempotency record stores `recipient_session_id` and `hop_count` so a duplicate send can
answer the ORIGINAL `SendResponse` after the message was acknowledged and swept; `seq` is unix nanoseconds floored
at one past the recipient's highest so far (monotonic across acks, deletions and a backwards clock); an empty
leading `--profile`/`--root` value is `usage`; `rejoined` is true whenever a member file for the principal already
exists, whatever its status (C-08 rejoins AFTER the profile was unbound); the retention sweep runs once per
command, not per watch poll; `team members` computes `last_seen_at` over sessions in any state (`null` with none)
and `session_count` over non-offline ones; the three mutant pairs hold only the functions that differ
(`ackMessages`; `collectSessions`; `decodeSendRequest` + `resolveSenderSession`).

**Known residuals:** the `--prompt` TTY input path of `team create`/`team join` is implemented but only its
non-TTY refusal (B-7) is tested (adapterkit's PTY helper is package-private); and (e) above.

## P1-6 DONE — the conformance suite; every case was made to fail on purpose; the 5 s target is off by four

Five Fable subagents from `.ignored/briefs/p1-6-conformance.md` (the driver's design, written against the P1-5
verifier's findings): one core author (launcher, `T` API, lazy fixture with extra principals, `WatchProc`, reports,
CLI), two case authors in parallel lanes (C-03..C-27, C-28..C-43), one integrator, one adversarial verifier; then one
Opus fixer for the four items the verifier left open (recorded at the end of this block). Full gate green. `make test`
now runs the real suite against the fs adapter; `go test ./internal/conformance` runs one parallel subtest per case
in its own run directory, a sequential whole run, and the mutants test.

**What the verifier did, case by case:** for each of the 45 cases it BROKE the fs adapter's subject with a temporary
edit (159 mutant builds, the adapter tree byte-compared back to git after every one), rebuilt, ran `--only <id>`, and
required the case to fail naming the rule. 41 failed at once. Four passed while broken and were strengthened:

1. **C-28 did not assert the ORDER of the two unacked caps** — the same 4.5.12 rule whose test P1-5's verifier had to
   add to the adapter, now missing from the suite: swapping the checks passed. Fixed with a third extra principal
   that puts both caps at their limit at once, so the swapped order answers `recipient_inbox_full` where
   `sender_quota_for_recipient` is required. The fixer then gave the rule a standing positive control (below).
2. **C-03/C-04's B-7 arm passed for the wrong reason**: an adapter that ignores the no-TTY rule and prompts on the
   pipe read the one-line JSON as the team name, ran out of input on the label prompt and answered `usage` by
   accident. The document on the pipe is now two non-empty lines, so a prompting adapter creates a team (and fails).
3. **C-01 was order-dependent** — in every SHUFFLED whole run it failed falsely ("describe created the shared
   directory", because an earlier case's store existed) — and the naive fix lost the catch of a `describe` that
   creates the root, because the start-of-run `describe` had already created it. Final fix: the runner records what
   the start-of-run `describe` (the one spawn guaranteed to precede every case) left behind, and C-01 reports it.
4. **C-12 was order-dependent**: after C-28/C-40 had joined extra principals into T1, its "every principal ∈ {A, B}"
   assertion failed on principals the suite itself provisioned. It now asserts membership of the T1 principal set
   the suite created (`T.T1Principals()`), still team isolation (`mutant_teamleak` still fails it).

Order independence was exercisable only through the library (the binary ran ids in list order), which is why the
fixer added `--shuffle <seed>` and made the whole-run test shuffled. The verifier also attacked the launcher's global
checks with wrapper adapters — an extra stdout line, exit 127, a planted secret on stderr, an envelope without
`retryable`, a non-event line on a watch — each failed the running case naming 4.1 / B-11 / C-05 / B-2 / 4.4.9; a
`describe` that is not ok, a major "2", a failing `--setup` and a missing adapter each exited 3; an unknown `--only`
exited 2; an environment dump proved every adapter process sees exactly the section-3 variable set with the
developer's `CLAUDE_CONFIG_DIR`, `HOME` and every inherited `BRIGADE_*`/`CLAUDE*` value absent, and `-v` elides
`SECRET`/`TOKEN`/`KEY` values. Mutant sets, measured by the suite's own test: `mutant_noack` → `{C-29b, C-30, C-36,
C-41}`, `mutant_teamleak` → `{C-12, C-26}`, `mutant_trustsender` → `{C-23, C-24}`, the normal build → `{}`, each
EXACT.

**The integrator's one real finding: the C-05 secret scan produced a false positive on the spec's own text.** The
protocol's fixed `invalid_input` message for a malformed join secret spells the FORMAT, `expected
brg1.<team_ref>.<secret>` (as `docs/protocol-v1.md` line 623 does), and the launcher scanned every stream for the
bare `brg1.` substring, so C-04 — which provokes that message on purpose — failed under C-05's name on every run and
polluted every mutant set with C-04. The scan now requires a secret-SHAPED token (`brg1.` plus two or more
dot-separated components of characters a real secret can carry) besides every secret the run knows verbatim;
`TestSecretShapedScan` pins both sides. The adapter's message conforms (no part of the input; 4.4.10, 4.5.14).

**Plan corrections (measured):**

- **9.2's "the fs run must stay under 5 s" is off by four: the floor is about 20.3 s** (three idle runs 20.24-20.73 s;
  26.5 s with `--slow`). Eight seconds are absence windows the design mandates — C-36's "not re-emitted after the ack"
  waits the full 5 s push deadline (no poll-interval member exists on the wire, see P1-5's question (b)), C-41's
  restart quiet window 2 s, C-33's 1 s — and the rest is about 500 adapter spawns at about 25 ms each (the fs
  adapter's flock plus `WriteAtomic`'s fsync; a bare `describe` is 4.6 ms). Nothing mandated was shortened. The whole-
  run test's ceiling is 30 s and it runs sequentially (under `-race` with the per-case subtests and four mutant builds
  in parallel it measured 38-41 s). `make test` grows by about 20 s and `go test ./internal/conformance` takes about
  70 s under `-race`; CI's `fast` job stays well under its 5-minute target. A poll-interval member in `describe`
  (BAP/1.x) would let the suite cut the 5 s windows for a fast poller.
- The fixture of 9.2 (three principals) cannot carry the heavy cases: the frozen `principal_send_rate` (60/min) is
  spent by one heavy case inside a run that takes seconds. `T.JoinPrincipal` provisions extra T1 principals through
  `team join` (C-28 uses three, C-40 one); on a `--setup`-provisioned adapter without `team.join` those cases SKIP
  with the reason. Principal A still ends a run at about 48 of 60 sends; a new A-sending case must be placed with care.
- C-40 is 60 messages (P1-5's correction, now implemented); C-12 asserts the provisioned T1 set (above).

**BAP/1.x questions, added to P1-5's list (recorded, not changed):** (f) a malformed or empty stdin document on an
UNCONFIGURED profile: 4.1 says `invalid_input`, 4.6 says `config`/`unauthenticated`, and nothing orders the two
checks; the fs adapter resolves the profile first (`config`), so C-02's stdin half runs on a joined principal and the
Supabase adapter must do the same for C-02 to stay adapter-independent. (g) C-08 asserts that a running watch of a
revoked member exits 5 with an `unauthorized` event and that a send to a revoked member's session is the uniform
`not_found`, and C-31 asserts `message receive` works on a closed, owned session; all three follow from 4.5.6/4.5.7/
4.5.8 as read in P1-5 and are the fs adapter's measured behaviour, but no sentence of the spec states them in those
words — the Supabase adapter must match or fail them.

**Suite design decisions beyond the brief, all recorded by the agents and accepted:** slow cases are reported SKIP
("slow case; run with --slow") rather than dropped, so a report never hides what was not run; a fixture failure ends
the run with exit 3 and the report still written; `--setup`/`--rebind` run without the protocol checks and with three
times `--timeout`; the JSON report adds `rule`, `notes` and `duration_ms` while keeping every key of the P1-1 stub's
shape; a signal death reports `Exit -1` with the signal named; `Result.Raw` exposes the loosely parsed stdout for
key-presence checks; `ExpectNone` ignores informational `status` events; `-v` redacts every known join secret.

**The Opus fixer's four closures, each with failing-first evidence:** (1) a selection that names no case (`--tags
cap:nosuch`, `--only X --skip X`) is now `usage` (exit 2) before any adapter is launched — a run that selects nothing
is not a pass (the P1-1 stub's "0 cases, exit 0" is gone for good); (2) C-37 now asserts the foreign watch's `error`
line is byte-identical to a never-issued id's (4.5.7's uniform answer), with a positive control — the watch's own
`usage` refusal for a missing `--session`, which 4.1 obliges to be an NDJSON `error` event, not a 4.3 envelope
(BAP/1.x note (h): the spec should say so in words); (3) `--shuffle <seed>` (a hand-written splitmix64 Fisher-Yates,
so a seed names one permutation on every Go release), the seed on both reports, and the sequential whole-run test
now runs shuffled with a logged, reproducible seed (`-conformance-seed`); six seeds through the binary found no
further order dependence; (4) a fourth mutant, **`mutant_caporder`** (`store_caps.go` / `store_caps_mutant.go`, the
two unacked-cap checks swapped), listed in the Makefile's `mutant_tags` so both twins are linted, proven live by the
adapter's own mutants test, and failing EXACTLY `{C-28}` in the suite's — the rule whose instrument was missing in
P1-5 and in P1-6 now has a standing positive control. The fs README's `mutant_noack` row and its stale
`.golangci.yml` sentence were corrected on the way.

## P1-8 DONE — the bootstrap, the CI scripts, and the two CI steps un-gated; the background-download test was vacuous

One Opus author, one Opus adversarial verifier, one Opus fixer, from `.ignored/briefs/p1-8-bootstrap.md` (plan 6.2 and
7.7 as the base, with the E0-8 corrections). Delivered: `plugin/bin/brigade` (POSIX sh, mode 100755 IN GIT),
`internal/harness/bootstrap/bootstrap_test.go` (25 subtests against an `httptest` release server — never the real
cache or pointer file), `scripts/ci/{plugin-check,no-secrets,checksums-check,release-verify}.sh` with
`scripts/ci/checks_test.go` (44 subtests over fixture trees, each rule shown to fail), `scripts/ci/bootstrap-alpine.sh`
(the local-only `alpine:3.20` leg: busybox ash + wget + sha256sum, 3/3 PASS, run three times), `.github/workflows/ci.yml`
un-gated (`make checksums-check` with `GH_TOKEN`, `make plugin-check` no longer `if: false`) and the CLAUDE.md bullet
rewritten to the present truth. The bootstrap ran end to end under sh, bash, zsh, dash and ksh on this machine and
under busybox ash in the container; `shellcheck -s sh` is clean on all six shell files. `make plugin-check` reports
`skip: … (P3-1)` for the two checks whose files (`plugin.json`, `hooks.json`) do not exist yet, so nothing is vacuous
and nothing is pretended.

**The decisive finding, again an instrument.** The E0-8 correction — the SessionStart hook must start the first-use
download in the BACKGROUND and return at once — is the whole reason the `hook session-start` branch exists, and its
test could not tell a background download from a synchronous one: deleting the trailing `&` left the subtest green,
because the 2 s bound was measured against a loopback server that answers in 10 ms. Fixed: the test server now holds
every answer for 2 s, the hook must return in under 1 s, and the cache file must be ABSENT at the instant it returns;
the synchronous mutant now fails ("the hook took 2.07 s … the download was not detached"). Two more: `plugin-check.sh`
counted `"type"` and `"command"` with `grep -c` (LINES, not occurrences), so a minified or one-line `hooks.json` could
never fail the exec-form rule — now `awk`/`gsub` counts occurrences, with a regression subtest; and a subtest's comment
credited `--proto '=https'` for a failure that the TLS handshake causes with or without the flag — the comment now says
what the case proves (no silent https→http downgrade) and records that `--proto` is not exercisable without a real
certificate. 20 of 22 bootstrap mutations were killed by the author's tests; the two survivors are the ones above.

**Fixed by the driver's fixer after the pass (each with a failing-first test):** plan 6.2's loopback exception was a
PREFIX glob — `http://127.0.0.1*|http://localhost*` — so `http://127.0.0.1.evil.example` and
`http://localhost.attacker.net` counted as loopback. **This was not theoretical: `localhost.attacker.net` RESOLVED on
this machine, and the pre-fix bootstrap downloaded, checksum-verified and exec'd the test binary from it over
plaintext http, exit 0** (bounded by the sha256 trust anchor — a hostile base can fail or beacon, never substitute a
binary — but a plaintext fetch from a non-loopback host is exactly what the rule exists to forbid). Tightened to the
exact hosts with an optional port or path; two subtests pin it with zero requests observed. The `service_role` scan of
`no-secrets.sh` no longer covers the BINARIES: the Supabase adapter will embed that role NAME (an error string, a SQL
role), which is not a key, while a service-role JWT is caught by the JWT-shape scan wherever it appears — so the
literal-word rule now applies to `plugin/` text only, which would otherwise have broken CI on the first P2 push. A
dead `.mcp.json` line in `plugin-check.sh` (unreachable behind the allowlist) was removed.

**Plan corrections (measured):** (1) 6.2's script is not `shellcheck -s sh` clean as written — `CDPATH= cd` trips
SC1007; `CDPATH='' cd` is the same POSIX prefix assignment. (2) 6.2's loopback glob, above. (3) `alpine:3.20`'s base
busybox has NO `httpd` applet (`busybox --list` lacks it); the leg installs `busybox-extras` (busybox's own httpd,
packaged separately), so it needs network inside the container. (4) The release.yml `sed -nE` that reads
`plugin.json`'s version matches only a `"version"` that starts a line: a single-line manifest reads as "declares no
version" — a safe failure, not a false pass, but P3-1 must write `plugin.json` multi-line. (5) E0-8's "honest limits"
said the background variant's context line was an inference; it is now evidenced byte for byte by the bootstrap test.

**Decisions recorded (the author's, accepted):** the hook branch is guarded `$# -ge 2 && $1 = hook && $2 = session-start`
and sits AFTER every exit-11 precondition (missing pins, unsupported platform, no sha256 line, non-loopback http, no
hasher, no curl/wget) and after the cache `mkdir`, so a misconfiguration is still reported loudly and synchronously
and only the network fetch is detached; the download-verify-install body is one `install_verified()` shared by both
paths, and the temp file is created INSIDE the detached subshell so the parent's trap can never remove it; on the hook
path stdout is exactly the one context line (E0-8 (d): `stdout.strip()` becomes context, so a second line would too);
`plugin-check.sh`'s hooks checks are textual (no jq) and fix a testable definition of "exec form" — `"type": "command"`,
a single `"command"` path (absolute or `${CLAUDE_PLUGIN_ROOT}`-rooted, no shell metacharacters), arguments in
`"args"`, and the resolved path must exist and be executable (E0-8: `claude plugin validate --strict` does not check
that); check 1 walks the working tree (`find`), because `--plugin-dir` packages untracked strays too, while check 2
reads the index, because the recorded mode is what the exec-form hook depends on; `checksums-check.sh` rule (b) uses
`grep -E` intervals rather than awk's (not universal); the wrong-checksum case serves DIFFERENT bytes under the same
asset name (a substituted download), not a corrupted hash.

**Also in this commit:** the conformance whole-run test's hard 30 s ceiling tripped once at 30.75 s while a second
gate ran alongside (the fs run's unloaded floor is 20.3 s under `-race`), and CI runners are slower than this
machine, so the ceiling is now 120 s — it catches a hang or an order-of-magnitude regression, never load — and the
wall time stays LOGGED on every run, which is the measurement this log records.

**Known residuals, recorded:** `--proto '=https'` (a downgrade-redirect guard) is asserted nowhere — it needs a real
certificate; `wget` is absent on this macOS host, so the wget branch is covered only by the alpine leg; the alpine
driver's inner ash script is a quoted heredoc and so outside `shellcheck`'s reach (exercised end to end instead);
`no-secrets.sh` silently skips the tracked-file half outside a git repository (no CI path); `make release` and
`make e2e` reference `scripts/release-prep.sh` (P2-12) and `scripts/proof.sh` (P4-1), which do not exist yet, and
`plugin-check.sh`'s shellcheck glob will cover them the moment they land; after the first tag, a developer without
`gh` cannot get a green `make checksums-check` when the source has moved on (the script says so rather than passing).

## P1-7 DONE — the adapter authors guide, proven by a reader who was allowed to read nothing else

One Opus author, three rounds of an independent DOC-ONLY implementer (a fresh agent permitted to read only
`docs/adapter-authors.md` and to run `bin/brigade-conformance`; every file it opened is listed in its report and
none was the spec, the adapter, the suite or a brief), an author fix after each round, and one adversarial reviewer.
The acceptance criterion of the plan row — "a reader can implement `describe` + `session list` from the doc alone" —
was run as an experiment, not a read-through: each round's implementer built a standalone adapter (Go, its own
module, no Brigade import) and ran `--only C-01,C-02,C-05,C-06`; all three PASSED on the first suite invocation, and
each round's list of guesses and contradictions became document text (the gaps closed: where a store-backed
adapter's root comes from and when it is validated, empty `--profile`, non-object stdin, which parts of an error are
frozen and which are the author's, identifier shapes, the 0600 rule on `profile.json`, how "advertise exactly what you
implement" coexists with the two advertisements that may run ahead of the code, `team leave` when unbound,
`lease_seconds` in full, convention flags versus a stdin document, the label on a rejoin, the suite's child
environment stated once and completely). The reviewer traced 308 claims, re-ran every pasted command (only ids,
timestamps and durations differed), diffed the three embedded txtar scripts byte for byte, and fixed nine
statements — the sharpest: the page had sold the unbound-profile code as a free choice between 4 and 11, but 4.6 pairs
each code with a state and C-08 asserts `config` after `team leave` exactly; four places said "both bundled adapters"
about an adapter that does not exist yet; a third SKIP cause (`--setup` without a known secret skips C-28 and C-40)
was missing; the shuffle seed is on the header line, not the summary.

**Two things it surfaced beyond the document.** (1) The acceptance set `--only C-01,C-02,C-05,C-06` builds the
fixture, so a partial adapter on the self-provisioning route must advertise `team.join`, and nothing in those four
cases then checks `team leave` — the half-kept promise the round-2 implementer deliberately shipped passed. The page
says so; the suite gap (nothing exercises `--limit`, a missing `--session`, an unrecognised `--session`) joins
Appendix B's list for a BAP/1.x pass. (2) `cmd/brigade/testdata/script/fs-session.txtar` credited its
closed-session check to C-14; the case is C-12 (C-14 is the lease-EXPIRY twin). Fixed in the script and in the
page's embedded copy in this commit.

**BAP/1.x questions added:** (i) 4.1's stderr recommendation names NDJSON members (`ts`, `event`) that no shipped
component emits (`adapterkit/log` is a stock `slog` JSON handler: `time`, `level`, `msg`); (j) a recognised CORE verb
an adapter has not implemented on a bound profile has no code in 4.2/4.6 — the page prescribes `internal` (exit 1)
with a `details.reason`; (k) the launcher's end-of-run directory walk makes "never persist a raw join secret" a hard
conformance requirement that no normative sentence states; (l) 4.4.1 and the page say "the bundled adapters answer
`none`" for `delivery.ordering` — one adapter exists today. Recorded adapter facts, not defects: a mistyped member is
reported as `malformed_json` without `details.field`; a top-level `null` decodes as an empty request; the credential
file keeps `last_team_ref` after `team leave` (documented in the fs README's spirit, worth a line there); the fs
adapter's `error.details` member ORDER is non-deterministic across runs (JSON-insignificant; the two byte-identical
errors carry no `details`). The external contributor's brief (`.ignored/adapter-contributor-early-start.md`) was
refreshed to the present facts as a local file; **the published artifact was NOT republished — that is Rjae's
outward-facing call** (see the hand-off).

## PHASE 1 COMPLETE — the four exit criteria of plan section 8, verified 2026-09-02

| Criterion | Evidence |
| --- | --- |
| CI `fast` and `macos` jobs green | **run 33604975116 on `c1bf8b7`** (the SIGTERM-ordering fix, the last code commit of Phase 1): `fast` success, `macos` success, `reproducibility` success (`supabase` gated until P2-1) — with `make checksums-check` and `make plugin-check` un-gated and running in `fast`. Earlier: run 33604039333 on `81ff5eb` green; run 33604389754 on the log-only `17b2ab1` red with the C-38 race described below |
| conformance green on the fs adapter | `bin/brigade-conformance --shared-env BRIGADE_FS_ROOT --adapter bin/brigade-adapter-fs` → 44 passed, 0 failed, 1 skipped (C-14, `slow`), 20.3 s wall; `--slow` → 45 passed, 0 failed, 0 skipped in 26.3 s; `make test` runs it on every gate and in CI |
| the RFC committed | `docs/protocol-v1.md`, BAP/1, frozen at `702e047`, 939 lines; its Appendix B is consumed by the suite except B-3/B-4 (receiver-side, harness unit tests in P3) and B-9/B-10 (unobservable within the suite's budget; adapter-specific tests) |
| the bootstrap tested against a local server | `internal/harness/bootstrap/bootstrap_test.go` (25 subtests against an `httptest` release server, macOS `/bin/sh` and `dash` locally, Ubuntu dash + `sha256sum` in CI) plus `scripts/ci/bootstrap-alpine.sh` (busybox ash + wget + sha256sum in `alpine:3.20`, 3/3 PASS, run three times) |

**One more defect surfaced by CI after the run above**, on the log-only commit `17b2ab1`: in the `mutant_trustsender`
run of the suite's mutants test, C-38 reported `exit -1 after SIGTERM, want 0` — the fs adapter installed its SIGTERM
handler inside the watch loop, AFTER writing `ready`, so a harness that signals the instant it sees `ready` (exactly
what C-38 does) could hit the default disposition and kill the process by signal, the one exit 4.4.9 forbids. Never
seen on this machine; the slower CI runner under `-race` widened the window. Fixed in the commit that follows: the
handler is installed before anything is written, and `TestWatchExitsZeroWhenSignalledOnReady` drives the real binary
eight times, signalling at the earliest observable moment (a race cannot be made to fail on demand; the test
documents the contract and catches a regression on any machine slow enough to show it). The CI run on that commit is
the final evidence for criterion 1: **run 33604975116 on `c1bf8b7`, green on `fast`, `macos` and `reproducibility`.**

Phase 1 ran from `0af93a1` (P1-1, 2026-09-01) to `81ff5eb` (P1-7, 2026-09-02) plus the SIGTERM-ordering fix. Everything is on `master`, the tree
is clean, and nothing is retained outside the repository. **E0-10 stays `blocked (D32)`** and is not a Phase 1 item.
The cold-start hand-off for Phase 2 is `.ignored/handoff-15-phase-2.md` (gitignored; it names the decisions this
session made as driver, the BAP/1.x question list (a)–(l), what Phase 2 must know before writing SQL or Go, and the
one outward-facing item left for Rjae: republishing the external contributor's artifact). **Rjae decides who drives
Phase 2**; this session is hands-off from this commit.

## OWNER DECISION 2026-09-02 — D36: a profile carries its default adapter; a session may override it

Rjae's design, in her words: (1) one team per session; (2) one session per adapter; (3) profile default adapter,
overridable by session. (1) and (2) were already the design (D8, D9, D26). (3) is new and recorded as **plan D36**:
the `adapter_command` plugin option becomes the PER-SESSION OVERRIDE; the profile's default adapter is written by
`brigade profile init <name> --adapter <name-or-command>` into a harness sidecar beside the profile, with a harness
registry `${BRIGADE_CONFIG_DIR}/adapters.json` mapping names to commands; the profile file's own `adapter` member is a
best-effort fallback; the bundled Supabase adapter is the last resort. What it fixes: until now the profile–adapter
pairing had to be restated at every launch through `--settings`, nothing prevented pairing a profile with the wrong
adapter, and the pairing lived nowhere durable. What it does not change: the protocol, the conformance suite, both
adapters, the profile file formats, the by-pid map (which already records the resolved command). What it rules out:
one session live in several teams at once (the Phase 5 note in the onboarding block above stands only for a
session-scoped SWITCH). The hard constraint restated in the decision: a team lives on exactly one backend, and its
members' adapters must speak that backend's data model, which 4.8 leaves to adapters. Plan sections changed: 2
(D36), 3.2 (two harness-owned files), 3.3, 6.1, 6.3, 6.4, 6.5, and the P3-3/P3-4/P3-6 rows; `docs/adapter-authors.md`'s
wiring section rewritten to match. All Phase 3 work; nothing for Phase 2.

## P2 BACKEND DONE — P2-1..P2-5: the migrations finished, 756 pgTAP assertions, 46 mutations, `not_found` is HTTP 404

Three Fable agents from `.ignored/briefs/p2-1-5-supabase-backend.md` (a SQL author, a pgTAP author, one adversarial
verifier), sequenced on the one running stack. The E0-1 drafts were audited line by line against the plan AND
against the sixteen behaviours Phase 1 pinned after they were written: eleven were already right, five changed —
every cap violation is now an explicit `invalid_input` raise naming the member (the draft let some fall through to a
`23514` check violation); the resume live-check is scoped to the caller's team so an owned live session in ANOTHER
team answers the uniform `not_found`; `list_members.session_count` counts live sessions only (the draft counted
every session ever registered; C-43 and the fs adapter's rule); `list_sessions` orders online-first before its
`v_cap + 1` cut so a truncated list never drops an online session for a more recently seen offline one; and the
`not_found` SQLSTATE. One premise of the brief was stale: the draft already compared the recipient in the
idempotency check (C-22); nothing changed there.

**`not_found` is `PT404`, measured.** A throwaway definer function raised the same `brigade:not_found` under
`P0002` and `PT404` through PostgREST with a real anonymous principal: `P0002` → HTTP 500, `PT404` → HTTP 404, both
73-byte bodies with the `brigade:` prefix byte for byte and `details`/`hint` carried when set. All eight `not_found`
raises switched; the E0-1 kit's seven foreign-vs-random byte-identity pairs now read 404/404 and stay identical.
**P2-6 maps BOTH `PT404` and `P0002` to `not_found`** (the `brigade:` prefix first, SQLSTATE second, HTTP status
never). Text that now describes the old state — the spec's INFORMATIVE 4.6 "Supabase adapter mapping" paragraph
(`P0002` → 6), plan 5.4's error table, 9.4's integration list, the P2-6 row's acceptance and E0-1's "gateway
behaviour 1" — is recorded as **BAP/1.x editorial (m)**: the paragraph is informative, so no wire changes.

**Two orderings the SQL author decided and the verifier pinned:** `session_heartbeat` and `send_message` check
active membership BEFORE the closed-session conflict (plan 5.4 had closed first), so a revoked principal is told
`unauthorized` on every verb (4.5.7) even after `leave_team` closed its sessions — pinned by rls_isolation after a
real `leave_team`, which is the only state in which the two orders differ; and the roster rule above.

**The instrument again, twice.** (1) `supabase test db` (CLI 2.116.0, pg_prove 3.36) discovers tests RECURSIVELY, so
the bare recipe ran `supabase/tests/helpers/auth.sql` — 9.3's `\ir`-included fixture helper — as a test and failed
the whole run with "No plan found in TAP output" while every assertion passed; `make test-db` now lists
`supabase/tests/*.sql` (the layout of 9.3 is kept). (2) The verifier ran **46 SQL mutations** — every policy
predicate, grant, revoke, `search_path`, trigger, limit, cap order, guard and the realtime join — each applied,
reset from scratch, run against pgTAP, reverted and byte-compared: 38 were killed by a rule-naming assertion as
delivered, one (an infinite implicit-reply window) was "killed" only by a file ABORT that hid every later assertion,
and **seven survived** because the suite could not see them: the roster's `session_count` rule (no member had a
closed or expired session at roster time), the two membership-before-closed orderings (with an OPEN session both
orders answer the same), `ack_messages` flipping a pending message addressed to ANOTHER session (every "unknown"
id in the tests was already injected or random), the `list_sessions` truncation order (the closed fixture session
tied on `now()`), the sender-ownership clause of `send_message` (masked by the stamping trigger for open sessions,
but a CLOSED foreign session answered `conflict` — a closed-state oracle), and **within-team message privacy** (team
A had two members and every message involved both, so a policy that let any member read every message of its team
passed 733 of 733). All seven are now killed by named assertions; the abort became a non-aborting `pg_temp.send_hop`
helper. The pgTAP author found one genuine migration defect on the way: the implicit-reply lookup's
`order by created_at desc limit 1` is nondeterministic when two candidates share `created_at` (every message inside
one pgTAP transaction does), so the hop chain was `0,1,2,1,2,…` and the bound never tripped; now `order by
created_at desc, seq desc`, and the chain reads 0..32 with the 34th message refused.

**Counts.** Nine files, 756 assertions in about 4 s: rls_isolation 106, rls_stamping 55, rpc_join 51, rpc_send 107,
rpc_sessions 113, realtime_policy 39, retention 48, hygiene 60, functions 177. `scripts/ci/advisor-lints.sql`
mirrors the ten named lints (14 expected findings, all documented inline — `join_attempts` has RLS and no policy
by design; the three function lints scope to `public` and `brigade` because `graphql_public` holds only Supabase's
own `graphql()` wrapper) and was shown to fail on a planted RLS-less table and a mutable-`search_path` definer
function. CI: the `supabase` job is un-gated; inside it `make test-integration` (P2-11), `make e2e` (P4-1) and the
`covdata` summary step are gated `if: false` with the task named, the P1-1 idiom.

**Plan corrections (measured):** 9.3's "`loop_detected` at the 33rd" is off by one — 33 messages succeed (hops 0..32)
and the 34th is refused, as the suite's C-29/C-29b and the SQL both read it; 9.3's `helpers/auth.sql` layout needs
the explicit file list in the recipe; 5.4's closed-before-membership order in heartbeat and send is reversed (4.5.7);
`register_session`'s `harness`/`harness_version` caps (32) are table constraints the protocol does not publish in
`limits`, now `invalid_input` raises with `details.field`, which P2-6 maps; the `permissive_rls_policy` mirror is a
superset of Supabase's 0024 (plus 0006 `multiple_permissive_policies`) and its always-true detection compares to
`true` only — align in a later editorial pass. Observations, not defects: `messages_select`'s recipient clause is
evaluated under `sessions`' own RLS, so a revoked member's received messages are hidden by the sessions policy even
when the messages policy's membership predicate is removed (defence in depth; both policies are pinned);
`realtime_policy.sql` depends on the realtime container having created today's `realtime.messages` partition (it
fails loudly otherwise, by intent); pgTAP files that call an RPC bare inside `is(...)` abort on a raise and hide the
rest — the `pg_temp.err`/`pg_temp.send_hop` wrappers are the pattern.

## P2 ADAPTER DONE — P2-6..P2-10: the bundled Supabase adapter passes the suite 45/0/0

Six agents from `.ignored/briefs/p2-6-10-supabase-adapter.md`: a Fable core author (client, GoTrue, PostgREST,
credentials, the one error-mapping table, describe, profile, the hidden `brigade adapter supabase` entry, the
`--setup` script, `testutil.RequireSupabase`), then a Fable team lane in parallel with an Opus session/message lane,
then a Fable watch author, one Fable adversarial verifier, and one Fable fixer for the two things outside the adapter
that kept the run red. Driver-measured on the final tree: **45 passed, 0 failed, 0 skipped with `--slow`, three
consecutive runs of 79.5, 79.5 and 83.4 s**; `make test-integration` and the Docker-free gate green; `deps-check` is
now the EQUALITY test P2-6 promised (five modules, byte-equal to the allow-list).

**Before the fixer the run was 27/16/2, for two reasons outside the adapter's files.** (1) The migration's
`register_session` cap of 30 sessions per principal per hour was below one run's demand (about 55 on the busiest
fixture principal): eleven cases failed with `rate_limited register_session`; the cap is now 120 with the reasoning
in the migration and the pgTAP assertion moved with it. (2) The suite's scratch principals get no `--setup`, so
`team create`/`team join` ran on an empty config dir and answered `config`; the team lane honours the 4.1
`BRIGADE_<ADAPTER>_*` pair — `BRIGADE_SUPABASE_URL` / `BRIGADE_SUPABASE_PUBLISHABLE_KEY` — on those two commands
only, only when the profile names no backend, writing it into `profile.json` exactly as `profile init` would; the
Makefile's `test-integration` line now passes that pair and drops `--setup`, so the suite provisions its own teams,
learns the join secret and RUNS C-28 and C-40 (which `--setup` provisioning skips by design).
`scripts/ci/conformance-setup-supabase.sh` stays as the documented out-of-band alternative.

**The watch's drain timer, decided by push.** The watch author found that Realtime re-evaluates a channel's
authorization only at join and on a token push, so a revoked member's running watch could learn of `team leave`
only from its drain timer — and set the timer to 1 s to meet C-08's 2 s bound (one RPC per second per watcher, too
expensive for a hosted backend). Resolved the other way: `leave_team` now emits a `membership_revoked` broadcast on
each of the leaver's open-session topics BEFORE closing them (a definer-function `realtime.send`, exactly as the
message trigger; errors swallowed, so an outage cannot fail the leave), the watch treats ANY broadcast on its own
topic as a drain hint, and the timers are the plan's 30 s while joined and 10 s while polling. pgTAP pins the hint
row; the adapter's unit tests pin the drain-on-hint and the timer's negative control (no hint → no drain within 5 s
at the 30 s setting); C-08 and C-35 pass live. pgTAP is now 770 assertions.

**What the verifier found.** 23 adapter mutations (edit, rebuild, run, revert, byte-compare): 18 killed as delivered,
4 survived because the instrument was weak and were fixed in place — the `PT404`/`P0002` SQLSTATE rows were never
exercised because every test body carried the `brigade:` prefix (non-prefixed rows added); the SIGTERM-on-ready
order was pinned only after `status live` (a gated-stdout test now signals INSIDE the catch-up write, which the
late-handler mutant fails by dying); "describe never dials" was proved only under `BRIGADE_TEST_OFFLINE=1`, which
short-circuits inside the client before any request (the test now runs describe without the switch and asserts
zero requests) — and one was an equivalent mutant (a secret logged through the redacting logger is redacted).
**The decisive live defect:** `profile revoke-credentials` and `profile reset` signed out with the access token as it
sat in `session.json` and treated GoTrue's 401 on `/logout` as "already dead" — for an EXPIRED access token, exactly
the state after an idle hour, the command answered `ok`, deleted `session.json`, and the refresh-token family stayed
alive (measured: the saved refresh token rotated after the revoke). `signOut` now refreshes first, so the sign-out
carries a verified token and a terminal refresh means the family is already gone. Live checks passed on the real
stack: the anonymous claims; refresh rotation and the one-behind rule both inside and past the 10 s reuse window
(one-behind is the server rule, not the window); `refresh_token_already_used` through the adapter's own state
machine — exactly one re-read-and-retry, then exit 4 with the rejoin message and `session.json` deleted while the
family survives; the re-read path with a proxy that rotates the file under the adapter (two `/token` calls, no
terminal error); a foreign topic refused as `unauthorized` after the server's 5 s backoff, not as a timeout; the
ids-only broadcast payload; live delivery 6 ms after a send; revocation ending a watch 994 ms after `leave_team`;
polling degradation with the realtime container stopped and recovery when started (the one container operation,
restored); `PT404` as HTTP 404 through the gateway; and a secret scan of every spawn's raw stderr and the kept run
directory (JWTs only inside 0600 `session.json` files; no join secret anywhere).

**Adapter decisions recorded (each cited by its author; none touches a frozen shape):** `session.json` is the parsed
session plus this adapter's `last_team_ref` (an idempotent `team leave` on an unbound profile still answers a
non-empty `team_ref`; unknown GoTrue members do not survive a rewrite); `profile revoke-credentials` signs out AND
deletes `session.json` (state `unauthenticated`, matching the fs adapter and 4.2's "leaves the profile in place for a
rejoin"; plan 5.2's "only signs out" is corrected), and a sign-out that cannot reach the backend is `unavailable`
and deletes nothing; a `--secret-file` that fails to write AFTER `create_team` leaves the profile UNBOUND (the
single-member team is reclaimed by housekeeping) rather than bound to a team whose only key nobody holds; a
`team join` `backend` member equal to the configured one is a no-op and a different one is `conflict`; `join_team`'s
200 statuses `invalid_secret` and `invalid_input` both map to the one fixed `unauthorized` (the suite's random ref
is hex, which the backend answers as `invalid_input`); a send with no `idempotency_key` gets a fresh random key,
never a payload fingerprint (4.5.4 keys idempotency on the CALLER's key; two deliberate identical sends stay two
messages — recorded because it is invisible on the wire); `is_self` is the adapter's, from `--session`, never asked
of the backend; optional arguments travel as SQL null and `p_reply_to` is omitted so the implicit chain runs; ids
that are not uuid-shaped answer the uniform `not_found` (or land in `ack`'s `unknown`) BEFORE any dial, because a
`22P02` would otherwise become `internal`; every RPC answer is decoded into the protocol types and re-marshalled, so
a member a future migration adds cannot leak onto the wire; a refused channel join is ADVISORY (a closed owned
session's topic is refused by `owns_session_topic` while `fetch_inbox` still drains it, C-31) — the drain decides,
and the watch reports `status polling` and retries the join after 60 s; transport loss reconnects with
realtime-js's backoff forever with `status polling` meanwhile, and the 30-minute budget applies to consecutive
retryable DRAIN failures instead (one `error` event with `retryable: true` at the outage's start, exit 9 past the
budget); stdin command failures are retryable iff the code is retryable by 4.6 or is `invalid_input`/`conflict`,
and `unauthorized` on an ack IS the membership re-check (fatal); `close` runs `close_session` on a background
context so a racing SIGTERM cannot cancel it.

**Plan corrections:** 5.2 (`profile revoke-credentials` also deletes the local credential file); 5.4's registration
cap (30 → 120, and why); 5.6's timer stands only because revocation is now pushed; 5.11 gains the two
`BRIGADE_SUPABASE_*` names (documented in the adapter's doc.go and the README); the P2-6 row's "a `P0002` body with
HTTP 500 maps to `not_found`" stays true and `PT404` under 404 maps the same; 9.4's `RequireSupabase` runs the
integration tests whenever `.env.test` is present and the stack answers (as 9.4 intends), so a developer's
`make test` with the stack up takes about 15 s longer and writes rows the housekeeping sweep reclaims — CI's `fast`
job has no `.env.test` and skips them. **BAP/1.x (n):** `join_team` answers a rejected secret as a 200 RESULT
`{"status": "invalid_secret"}` rather than a raise — a backend convention, invisible on the wire. **Suite
limitations recorded:** C-38's SIGTERM-on-ready is a race a late-installed handler can win (both adapters pin the
order in unit tests); C-01/C-07 cannot see a best-effort dial in `describe` (the unit test's request count does).

**Left for P2-11/P2-12:** the `BRIGADE_TEST_DOCKER=1` fault tests as tests (the realtime stop/start is proven by the
verifier's script, not by a test), `pgx` fixtures for backdating, `session_integration_test.go`'s team fixtures
through the verbs instead of the RPCs, un-gating CI's `test-integration` step, and the release rehearsal.

## P2-11 / P2-12 DONE — the integration suite runs in CI; the release script exists and was rehearsed locally

Two Opus lanes in parallel from `.ignored/briefs/p2-11-12-integration-release.md`, each with its own Opus adversarial
verifier.

**P2-11.** New: `fault_integration_test.go` (under `BRIGADE_TEST_DOCKER=1` only: polling degradation with the
realtime container stopped and started — `status polling` at once, a message delivered by the 10 s drain timer,
`status live` 5 s after the start; and the P2-10 row's stack-restart recovery through `make supabase-stop` /
`supabase-start`, 26 s here, the watch rejoining within a second of the start — both restore the stack in
`t.Cleanup` on a context that survives the test's cancellation); `fixtures_integration_test.go` (the `pgx` fixtures
of 9.4 through `database/sql` against `SUPABASE_DB_URL`: an expired lease lists offline and resumes without a 32 s
sleep, the C-29b after-window arm the suite cannot run, `gc_expired()` reclaiming a backdated acked message, and
I-23 read out of `auth.users`); `realtime_integration_test.go` (I-13's local half — a public join is accepted by
this stack and receives none of ten broadcasts, with the ten messages verified in the inbox so the negative is not
vacuous — and I-14, a client broadcast on an owned private topic dropped, with the database's own broadcast as the
positive control on the same socket); I-34 and the I-16 RPC half added; the session/message fixtures now build
their teams through `team create`/`team join` rather than the RPCs. Coverage across the process boundary:
`testutil.Build` and `make build` instrument under `BRIGADE_COVER=1`, `testutil.Env` forwards `GOCOVERDIR`, and the
Makefile passes `--env GOCOVERDIR=…` through the suite's own flag (the launcher builds every child's environment
from scratch, so an instrumented child otherwise wrote nothing — measured, `covdata percent` found no files). CI's
`supabase` job now runs `make test-integration` and the coverage summary; expected cost 5-8 min on an Ubuntu runner
on top of the job's 8, inside its 25-minute timeout. `pgx` is a test-only module: `deps-check`'s equality still
reports five linked modules.

Two facts the lane corrected in the brief: the existing integration tests drive the adapter IN-PROCESS (`run(...)`)
and only the conformance half crosses a process boundary — the fault tests follow the suite's shape, and the watch
path across a real process boundary is exercised by C-33..C-41; and a single watch cannot re-emit its own unacked
message (it dedupes within its process by design), so the stack-restart test proves the running watch's recovery and
then a FRESH watch's re-emission separately, which is the property C-36 and a harness restart actually rely on.
The verifier proved 16 of 17 checks able to fail and strengthened one instrument: the fault tests' waits reported
only "no watch event within 60 s" under a mutant that never emits `status polling`; they now name what was
expected. Driver's follow-ups in this commit: the stack-restart test's hard 5-minute bound (a CI flake risk on a
slow runner doing Docker work) is now a hang catcher at 10 minutes with the wall time logged; `make test`'s fs
conformance line carries the coverage variable too. Recorded, not changed: `BRIGADE_COVER=1` without `GOCOVERDIR`
turns `cmd/brigade`'s built-binary test red with Go's own warning — loud on purpose, so a CI misconfiguration cannot
produce silently empty coverage.

**P2-12.** `scripts/release-prep.sh` is plan 7.7's script with a `DRY_RUN` mode that stops before the commit and
prints what steps 4–5 would do, and fail-fast guards (a version-token check, a repository-root check, `bin/goreleaser`
present, a post-bump check that the manifest's `"version"` really changed — a single-line manifest reads as "declares
no version" to the anchored `sed`, execution-log correction 4). Rehearsed on a throwaway local branch never pushed and
deleted by name, with a throwaway manifest (P3-1's does not exist): `make cross`, then goreleaser
`release --clean --skip=publish,validate,announce` with `GORELEASER_CURRENT_TAG=v0.1.0` — **which goreleaser v2.18.0
accepts for a tag that does not exist as a git object** (`couldn't find any tags before "v0.1.0"`, then it built the
four targets; the 7.7 [uncertain] is settled and the fallback is not needed) — and `dist/checksums.txt` byte-equal
to `dist-cross/checksums.txt`, confirming that goreleaser's `dist/` holds `brigade_<os>_<arch>_<v1|v8.0>/`
directories and that its checksum file lists the upload names in `make cross`'s order. `checksums-check.sh` in the
bumped state passes (a) and (b) and fails (c) with the expected pre-tag text (`no published v0.1.0`);
`release-verify.sh` passes. **The verifier found four defects in the script and fixed them with regression
tests:** `DRY_RUN=true` (anything but the literal `1`) fell through to the REAL release path — proven in a fixture
with an upstream: it committed, tagged and pushed `v0.1.0` — now any value but `''`, `0`, `no`, `false` is a dry
run, so a typo fails safe; the clean-tree precondition ignored UNTRACKED files while step 4's `make push` runs
`git add :/ .`, so an unreviewed file could land in the tagged release commit — now `git status --porcelain
--untracked-files=normal`; bare `make release` invoked the script with `0.0.0` because the Makefile's
`version ?= $(plugin_version)` defaults it before the `test -n` guard runs — the script now refuses the pre-release
sentinel; and the manifest check ran after `VERSION` was written, leaving a bumped `VERSION` beside a stale manifest
on refusal — reordered. Recorded, not changed: the version pattern is a shape check, not a semver parse (`0.0.1-rc1`
must keep passing); step 3 has no guard for a missing `dist/checksums.txt`; a dry run leaves the bumped pins and the
dist directories for the operator to restore (`git restore --worktree` and `make clean`); goreleaser refuses a
repository with no remote at all.

**What a REAL rehearsal or release does beyond this, none of it done — Rjae's call:** `git pull --no-edit` on master;
`cp dist/checksums.txt plugin/bin/checksums.txt` and `make push message="15: Release <v>"` (a release commit on
`origin/master`, after which the `fast` job re-verifies rule (c) by fresh build); `git tag -a v<v>` and `git push
origin v<v>`, which TRIGGERS `release.yml`: the three guards, goreleaser creating a DRAFT release on the private
repository, `release-verify.sh`, then publish (or delete the draft on failure). A real run also needs P3-1's
`plugin.json`, which is why the rehearsal used a throwaway one. `make release` needs a clean tree, so it cannot run
while any lane has uncommitted work.

## P3-1 DONE — the plugin manifests, hooks, skills and README; `claude plugin validate --strict` is blind to skill frontmatter

One Opus author and one Opus adversarial verifier (no fix round: both defects were fixed in place), from
`.ignored/briefs/p3-1-plugin-manifests.md`, driven by session `15-implement-brigade-0902T18`. Delivered:
`plugin/.claude-plugin/plugin.json` (6.1: the seven `userConfig` options; `"version"` the second key and on its own
line for the four anchored-sed readers; no `license` — the repository has no LICENSE file, so neither manifest claims
one until it does, an owner question; no `hooks`/`mcpServers`/`channels` keys), `plugin/hooks/hooks.json` (6.3
verbatim: exec form, 60/5/5 s, the `statusMessage`), `plugin/skills/team-messaging/SKILL.md` (6.9 with the measured
corrections: the "reply via SendMessage" bullet is gone — the native wrapper gives no reply instruction and the
built-in tool cannot reach a Brigade session; the 8 KB `--body-file` threshold with the 10,000-character reason; the
evasive forms forbidden and, per the sitting, never proposed to the user; `config` in the error table; no `brigade
inbox`), `plugin/skills/setup/SKILL.md` (the three sections of 6.9/6.13, `user-invocable: true`, and a "Not runnable
yet" paragraph — **P3-3/P3-6 must remove it** when the pass-through lands), `plugin/README.md` (the same three
sections, the options table, D20's permission mechanics, the `-p`/sandbox notes, a Status paragraph that says plainly
that the hooks and commands do not run yet), `.claude-plugin/marketplace.json` (description, owner url, entry
metadata; **no `version` on the entry** — the validator checks it against `plugin.json`, and `make release` bumps only
the manifest and `VERSION`, so it would drift at the first release), the root README's Status, the Makefile's
`plugin-validate` guard removed, and `scripts/ci/manifests_test.go`: CI has no `claude` binary, so a Go test over the
REAL manifests asserts valid JSON, name/version/on-its-own-line, the forbidden keys, the `userConfig` shape, the hook
shape, the marketplace entry, the skills' frontmatter (name == directory; `allowed-tools: Bash(brigade:*)` on
team-messaging only; a whitelist of the frontmatter keys Claude Code 2.1.259 documents) and three forbidden literals —
26 mutation subtests each show one assertion failing on a mutated copy, and a positive control shows every check
silent on an unmutated copy.

**Acceptance, measured on Claude Code 2.1.259.** `make plugin-check` green with no `skip:` line (checks 3 and 5 now run
against the real files); `claude plugin validate .` with zero warnings (the marketplace `description` removed the
one it had); `claude plugin validate ./plugin --strict` exit 0. The headless run — `claude --plugin-dir ./plugin -p
--output-format stream-json --verbose --max-turns 1`, the session environment stripped by prefix, a temporary XDG
triple, the dev pointer under it — shows `system/init` with `brigade:setup` and `brigade:team-messaging` in both
`skills` and `slash_commands`, `mcp_servers: []`, no `plugin_errors` key, `plugins: [{brigade, brigade@inline,
0.0.0}]`, and a `messaging_socket_path` (a `-p` run binds a socket, 6.11); each hook fired exactly once with exit 1
and the P3-4 placeholder's stderr (`brigade hook failed (internal): brigade hook is not implemented yet; it arrives
with plan task P3-4`), which no event treats as blocking; `result: success`, exit 0. The verifier reproduced every
number independently, twice.

**What the verifier found.** (1) The setup sections handed a human five commands (`profile init --url`, `team create
--prompt`, `team join`, `team leave`, `profile reset`) that the shipped binary answers with `usage` exit 2 today —
the harness pass-through is P3-3 — and neither the skill nor the README said so: fixed with the explicit "not
runnable yet" paragraph and the Status clause. (2) **An unknown SKILL.md frontmatter key was caught by nothing**:
`claude plugin validate --strict` exits 0 on `not-a-real-field: nonsense` (and on a frontmatter `name` that mismatches
its directory), `plugin-check.sh` does not read skills, and the Go test's first version did not either — so a typo
such as `allowed_tools` would silently drop D20's grant with every gate green. The Go test now whitelists the
documented frontmatter keys and the mutation is exercised. Twelve checks were proven able to fail by mutation and
restored byte-for-byte; every one of the author's mutation subtests was read and run.

**Plan corrections (measured):** (1) 6.1 and the research skeleton — no `license` until a LICENSE file exists; no
`version` on the marketplace entry. (2) 6.9 — the "harness preamble says reply via SendMessage" bullet and the
`brigade inbox` line are gone (sitting correction 2; P5-11); `manifests_test.go` forbids both literals under
`plugin/`. (3) **P3-1's acceptance text is unmeasurable as written on 2.1.259**: `--verbose --output-format stream-json`
carries `system/hook_started` and `system/hook_response` for `SessionStart` ONLY (`hook_name: "SessionStart:startup"`,
`hook_event`, `exit_code`, `stderr`, no command field); `UserPromptSubmit` appears only in the transcript JSONL
(`attachment.type = "hook_non_blocking_error"`, with `hookEvent`, `command`, `exitCode`, `stderr`) and `SessionEnd`
only on the process stderr (`SessionEnd hook [<command>] failed: …`) — **P3-6's and P3-7's smoke checks must read all
three sources, never the stream alone.** (4) **A hook's `statusMessage` REPLACES the command string in the transcript's
hook record**: the SessionStart attachment reads `command: "Connecting to the Brigade team"` while the UserPromptSubmit
one (no statusMessage) reads `${CLAUDE_PLUGIN_ROOT}/bin/brigade hook prompt` — a test that identifies a hook by its
command path will not work for SessionStart. (5) `claude plugin validate --strict --json`'s `contents` array is not an
inventory: it lists a component only when it has a finding (proven by breaking a skill's frontmatter: `success:
false` with the file listed), so "the skills are listed" cannot be read from it; `--strict` polices `plugin.json`'s
fields, not skill frontmatter. (6) `docs/adapter-authors.md`'s wiring section writes `brigade profile init <name>
--adapter …` (a positional name) while plan 6.4 and the setup skill write `profile init [--profile <p>] --adapter …`;
P3-3 settles the form and P3-6 fixes the doc.

**Also in this commit — `release.yml` could not publish a pre-release.** Its Publish step ran `gh release edit
"$TAG" --draft=false --latest` unconditionally, and GitHub's REST API refuses `make_latest` for a prerelease
("Drafts and prereleases cannot be set as latest"); goreleaser's `prerelease: auto` marks `v0.0.1-rc1` as one, so the
P2-12 rehearsal as planned would have failed at publish and then deleted the draft it had just verified. `--latest`
is now passed only for a tag without a pre-release suffix (plan 7.7 correction; the rehearsal below is the test).

**Also in this commit — a timing flake of the hand-off's predicted shape.** The driver's full `-race` gate tripped
`TestSpawnWaitDelayGrandchildResultStands` once at 5.56 s against its 5 s bound (0.5 s in isolation, 5/5); the bound
was a performance bound on a 300 ms wait delay against a 10 s grandchild. It is now a hang catcher: the grandchild
sleeps 30 s, the bound is 15 s, and the wall time is logged. Same-shape bounds worth the same treatment if they ever
trip: `spawn_test.go` line ~220 (5 s against a 250 ms deadline) and `flock_test.go` lines 65 and 311.

**Recorded, not changed:** the team-messaging skill carries no status qualification (a model invoking it today gets
one clean `internal` line from `brigade sessions`, which P3-3 replaces); nested `claude -p` runs write a transcript
directory under the user's real `CLAUDE_CONFIG_DIR/projects/` keyed by the temporary cwd (E0-5 (7); the driver
removed this task's two by absolute path); the plugin pins stay at the pre-release `0.0.0`, so the bootstrap can
download nothing until D1's rehearsal and the first release.

## D1 RELEASE REHEARSAL DONE — the whole chain ran for real on a throwaway branch, found two defects, and was torn down

Run by the driver session `15-implement-brigade-0902T18` on 2026-09-02 (~20:00–20:15 local) in the plan's P2-12 form,
after P3-1 landed and CI was green: `git switch -c rehearsal/0.0.1-rc1 && git push -u origin rehearsal/0.0.1-rc1`
(the upstream is REQUIRED: the script's `git pull --no-edit` and `make push` fail on a branch without one), then
`make release version=0.0.1-rc1 branch=rehearsal/0.0.1-rc1`.

**Two defects, both found by the rehearsal and both fixed on master before the successful run.** (1) `release.yml`'s
Publish step ran `gh release edit "$TAG" --draft=false --latest` unconditionally; GitHub's REST API refuses
`make_latest` for a prerelease ("Drafts and prereleases cannot be set as latest"), and goreleaser's `prerelease:
auto` marks `v0.0.1-rc1` as one, so the rc publish would have failed and the Discard step would have deleted the
draft it had just verified — `--latest` is now passed only for a tag without a pre-release suffix (`060114f`).
(2) The FIRST attempt stopped at step 4 with nothing committed, pushed or tagged: `make push` runs `make test`, and
`scripts/ci`'s `TestChecksumsCheck/the_real_repository_passes_in_the_pre-release_state` ran `checksums-check.sh`
against the real tree in its bumped state — rule (c) fell back to `gh release download v0.0.1-rc1`, which cannot
exist between bumping the pins and pushing the tag; the same test would have broken every `make test` after the
first real release on a machine without `gh` or the release, and CI's `make build test` step (no `GH_TOKEN`). The
test now skips with the reason whenever `plugin/bin/VERSION` is not `0.0.0` (`036e175`; the real-version state is
`make checksums-check`'s). The pins were restored, master merged into the branch, and the second run went through.

**The successful run, measured.** Steps 1–3 as in the local rehearsal (goreleaser's `checksums.txt` byte-equal to
`make cross`'s). Step 4 committed `3a921d7 15: Release 0.0.1-rc1` (exactly `plugin/bin/VERSION`,
`plugin/.claude-plugin/plugin.json` and `plugin/bin/checksums.txt`) through the push chain; step 5 pushed `v0.0.1-rc1`.
**`release.yml` run 33698279695: every step green** — the guard on the Ubuntu runner reproduced the checksums this
macOS machine committed (cross-host reproducibility, now through the real release flow), goreleaser built the four
targets and created the draft, `release-verify.sh` matched, Publish published it as a **prerelease** (not draft,
not latest), Discard skipped. Assets: `brigade_0.0.1-rc1_{darwin_amd64 8,102,480 B, darwin_arm64 7,500,818 B,
linux_amd64 7,913,632 B, linux_arm64 7,340,192 B}` and `checksums.txt` (386 B, byte-identical to the committed
file). Exercised against the published release: `make checksums-check` on the release commit passes by rule (c)'s
fresh-build arm; `scripts/ci/checksums-check.sh <tampered fresh file>` passes by the release arm ("the published
release v0.0.1-rc1 backs plugin/bin/checksums.txt"); `gh release download v0.0.1-rc1` into `.ignored/rel`, served on
`127.0.0.1:<port>` and the shipped `plugin/bin/brigade` run with a TEMP XDG triple and `BRIGADE_RELEASE_BASE_URL`
pointed at it: cold cache → two GETs (`checksums.txt`, the darwin/arm64 asset), verified, cached 0755 under
`XDG_DATA_HOME/brigade/bin/brigade-0.0.1-rc1-darwin-arm64`, exec'd, `0.0.1-rc1` on stdout, **0.329 s** wall; warm
cache → **0.025 s**, zero requests; the raw asset copied alone into an empty directory runs (`./brigade version`);
all four downloaded assets verify against the committed checksums.

**Torn down with the documented recovery steps:** `gh release delete v0.0.1-rc1 --yes`; `git tag -d v0.0.1-rc1 &&
git push --delete origin v0.0.1-rc1`; the branch deleted remotely and locally; `dist/`, `dist-cross/` and
`.ignored/rel/` removed by absolute path. Master is unchanged (`036e175`, VERSION `0.0.0`, empty `checksums.txt`);
no release, no tag, no branch but `master` remains on the remote. Recorded, not changed: the release's
`targetCommitish` read `master` although the tag pointed at the branch commit (GitHub's default for a tag release;
harmless, the assets and the tag are what the bootstrap uses); `gh release view` has no `isLatest` field (use
`isPrerelease`/`isDraft`). **P5-10's real `0.1.0` release can follow this exact sequence from master.**

## P3-2 DONE — the harness library: nine packages, the fixtures, 101 mutations; `Watch.Wait` deadlocked and a FIFO at the map path hung every command

Five Fable authors in two waves on disjoint packages (A: `config`, `sessionmap`, `registry`, `testutil/fakeregistry`;
C: `frame`, `socketpost`, `testutil/fakesock`; E: `pidfile`, `procutil`, `testutil.NewSleeper`/`Eventually`; then B:
`adapterclient`, `testutil/fakeadapter`, `cmd/brigade-fake-adapter`; D: `inbound`, `backoff`, `policy`), then two
Fable adversarial verifiers (the security path; the process path), from `.ignored/briefs/p3-2-harness-library.md`.
Driven by session `15-implement-brigade-0902T18`. No fix round was needed: 10 defects, 8 fixed in place by the
verifiers, 2 low ones left for the driver (the `clean` recipe now removes `bin/brigade-fake-adapter`; the process-global
describe cache is documented for consumers' tests). **101 checks proven able to fail** by mutating the SUBJECT and
restoring byte for byte (18 on the process path, 83 on the security path).

**What exists now (the API the next three tasks consume; the authors' reports in the workflow journal carry every
signature).** `config`: `ParseOptions` (the seven options with defaults; `CLAUDE_PLUGIN_OPTION_*`; `hold`/junk
`team_inbound` → `refuse` plus a fixed warning), `InSession`/`Strip`/`Trusted` (every `BRIGADE_*` ignored whenever
`CLAUDE_PID` is set — U-27's unit half), `BrigadeConfigDir`/`BrigadeStateDir` (revive's stutter rule refused
`config.ConfigDir`), `ClaudeConfigDir` (the one place `~/.claude` is spelled as a default), `ResolveAdapter` (D36:
option → sidecar → profile member via `adapters.json` → bundled; three forms; every refusal a `config` reason;
`supabase` is implicit and never rebound through the registry), `WriteSidecar`/`RegisterAdapter` (the WRITE half of
D36 for `profile init --adapter`, added by the verifier), `Session`/`SessionIn` (the by-pid map through the strict
reader: `not_registered`, `map_not_private`, …), `FromWatcherEnv`/`WatcherEnv.Vars` (the six hook-built variables the
watcher accepts, nothing else; `BRIGADE_ADAPTER_COMMAND` carries the RESOLVED JSON array, `[]` = bundled — a name is
refused there). `sessionmap`: the two maps with an O_NOFOLLOW|O_NONBLOCK reader (mode/owner/symlink/FIFO/size
refusals; `claude_pid` must equal the file name; a native id is validated before it becomes a path; overwrite on
every write so a recurring native id is fine). `registry`: `Read` over an `fs.FS` opening exactly `<pid>.json`,
never listing, never a `*.key` (the fakeregistry recorder proves a mutant that opens the key fails). `frame`:
`Build` (6.7 exactly — the goldens are byte-identical to E0-3's `frame.py` output for variants A and C), `Wrap`
(D19 = C, `from-name` only), `Parse` (U-03 with corpus items 09/22/23/24/25 as bodies), `PollPreamble`. `socketpost`:
`Post` with injectable stat/uid/timeouts, the U-19 pre-checks, one physical line per frame (U-17), four `errors.Is`
sentinels (`ErrPrecheck`, `ErrSocketGone`, `ErrTimeout`, `ErrWrite`), the token in no error and no log line.
`pidfile` + `procutil`: `Lookup(pid)` reads the process STATE through `unix.SysctlKinfoProc` on darwin (`p_stat` =
SZOMB, `p_starttime` to the microsecond) and `/proc/<pid>/stat` on linux, so **a zombie reads as dead (E0-5 defect 1)
and the start token is finer than `ps -o lstart=`'s 1 s (E0-5's "unfixed limitation", fixed by construction on
darwin; 10 ms ticks on linux)**; `Create`/`Read`/`Alive`/`Remove` (compare-then-delete, E0-5 item 3)/`Replace`/
`Check`/`TokenSHA256`; no subprocess anywhere. `adapterclient`: `Client.Call` and typed `Describe`/`Register`/
`Heartbeat`/`Close`/`ListSessions`/`Send`/`Receive`/`Ack`/`TeamMembers` over `adapterkit.Spawn` with the from-scratch
environment (proven: a hostile parent environment with `BRIGADE_FS_ROOT`, `GODEBUG`, `NODE_OPTIONS` and the messaging
token reaches no child), the 4.1 budgets as constants, `retryable` absent-as-false, the protocol check
(`protocol_mismatch`), a per-process describe cache, `Client.Spawn` — the injectable spawn seam (zero-spawn and
token-absent assertions for U-25 and P3-3's hermetic tests), and `StartWatch` (the ONE `exec.CommandContext` in the
harness; stdin/stdout pipes, stderr to the 0600 adapter log; B-4, B-5, B-6 honoured; `Ack`/`Heartbeat`/`Close`;
`Wait` via `ProcessState`). `policy`: `Decide`/`Effective` (D18's 420-row table: `permission_mode` × entrypoint ×
option × native scan — the mode never changes the policy), `ScanNative` over the three settings files (any
`hold`/`refuse` wins). `inbound`: the pure `Pipeline` (validate U-18 → dedupe U-13 with the LRU and the 0600 seen file →
policy → per-sender sliding window 10/min U-14 with one notice per 5-minute window → identical-body deferral 60 s →
queue 50 oldest-drop U-15/E2E-13 → frame → `Offer`/`Next`/`Done`/`Drain` so the caller reports injected/printed
per message before acks are decided). `backoff`: `WatchRestart` (1..30 s) and `AdapterError` (cap 5 min) with a
caller-seeded `rand.Rand`, `Retryable(code)` (only `rate_limited` and `unavailable`), `RetryableExit` (every exit
status 1..12 through the 4.6 codes; an unowned status or a signal death restarts). Fixtures: `cmd/brigade-fake-adapter`
(a real BAP/1 executable scripted by `--script <abs path>` — a leading fixed argument, because the harness builds the
child environment from scratch — with ordered responses, `stdout_bytes`, `exit_code`, `sleep_ms`, a watch replay
with delays honouring `ack`/`heartbeat`/`close`, and an argv/stdin/environment dump), `fakesock`, `fakeregistry`,
`NewSleeper` (reaped; SIGTERMed in cleanup), `Eventually` (synctest-clean). `cmd/brigade/main_test.go`'s P1-1
instrument is replaced by the real fake adapter and `smoke.txtar` asserts the new shape.

**The decisive findings.** (1) **`Watch.Wait` deadlocked on its own documented stop sequence**: the reader sent on an
unbuffered channel and `Wait` waited for the reader, so a caller that had stopped consuming events (P3-5 on shutdown
or a restart decision) hung forever with the child already dead; a probe against the unfixed code hung at the 10 s
catcher, and the permanent test times the package out under the reverted line. Fixed: the reader stops delivering
once the context is cancelled and drains to EOF; the contract (consume events to close, or cancel, before `Wait`) is
on the type. (2) `Wait` reported `(-1, context canceled)` for a child that exited 0 on the SIGTERM it was sent —
fixed to read `ProcessState`, so P3-5's restart policy is not fed a "crash" for a stop it asked for. (3) **A FIFO
planted at `${stateDir}/sessions/by-pid/<pid>.json` blocked every session-bound command and the watcher's map
re-read until a writer appeared** (the E0-7 trust boundary again: the map is guarded by the file system alone, so
the reader must refuse everything that is not a private regular file, and open without blocking) — fixed with
`O_NONBLOCK`, a `Mkfifo` test and a 30 s hang catcher. (4) `Client` had no spawn seam, so U-25's "token absent from
every child" and P3-3's "zero spawns on an oversize body" could only be asserted with a real process — added. (5)
`config` had only the READ half of D36; `profile init --adapter` would have re-implemented the parsing and the
atomic 0600 write and could have written a sidecar the next `SessionStart` refuses — `WriteSidecar`/
`RegisterAdapter` added with refusals for an insecure registry and a value the resolver would refuse. (6) Four
checks were green for weaker reasons than claimed (retryable trusted from the wire; any signal name; only the
absolute-path miss) — strengthened.

**Plan and brief corrections (measured):** 6.6/3.2 name `ps -o lstart=` as the start token and 7.1 puts detach/signals
in `procutil`; the token now comes from the kernel (`kern.proc.pid` sysctl / procfs) and `procutil` spawns nothing —
P3-4's watcher spawn sets `SysProcAttr{Setsid}` itself. `unix.SZOMB` does not exist in x/sys v0.47.0 (declared
locally as 5 from `<sys/proc.h>`, proven by the measured `p_stat` of an unreaped child); darwin's sysctl answers
`EIO` for a gone pid (mapped to gone with `ESRCH`/`ENOENT`). 6.7's "every attribute through the 64-code-point cap" and
"ids in full" cannot both hold — the fs adapter's message ids are 64 hex characters, one short of biting — so the
three ids keep the character rules and drop the cap. A unix-socket write returns once the kernel buffered the bytes
(8 KiB on darwin), so a stalled peer is invisible for any real frame — which is exactly 4.9's `injected`; the bounded-
wait test uses a 2 MiB content. `ENOENT` is observed at the `Lstat` pre-check (zero dials), not on dial. 6.8's "token
bucket 10/min" is a sliding 60 s window of 10 (a refilling bucket would admit 18 of 25 in a minute, contradicting
U-14's own expectation). 6.6 leaves exit statuses 1, 2, 3, 6, 7 and 12 unclassified for the watch child —
`RetryableExit` maps every status through the codes (only 8 and 9 restart). The 3 s `describe` cap belongs to the
caller's context, not inside `Describe` (an internal cap flaked at 3.03 s under `-race -count=3`). `StartWatch` routes
the child's stderr to the adapter log, not a third pipe. A 2 MiB stdout is `stdout_not_json`, not `stdout_overflow`
(the cap is 4 MiB); the 1 MiB line cap is the watch reader's. The by-pid map's mode rule is `adapterkit.ReadStrict`'s
(no group/other bit; 0400 accepted). Caps the plan does not name: map 64 KiB, registry entry 256 KiB, settings and
seen file 1 MiB, native id 80 characters.

**For the threat model and the next briefs (recorded, not changed):** `adapterkit.ReadStrict` opens with `os.Open`
— it follows a symlink and blocks on a FIFO — and is what `config` reads the sidecar, `adapters.json` and
`profile.json` through, and `inbound.FileSeenStore` its seen file; only the two maps (own reader) and the pidfile
(`Lstat` first) are immune. An `O_NOFOLLOW|O_NONBLOCK` + `LimitReader` `ReadStrict` is an additive change with three
callers: **P3-3 does it** (the adapters read `profile.json` through it too, so the change is tested against the fs
adapter and the conformance suite). `CLAUDE_PLUGIN_OPTION_*` exported by Claude Code cannot be told from the same
name planted by a trusted repository's settings `env` block (E0-7 measured that block reaching the session
environment): `CLAUDE_PLUGIN_OPTION_ADAPTER_COMMAND=<path in the repo>` would select the adapter for that session — no
new capability (a trusted repository already runs arbitrary hooks) but it belongs beside the by-pid item in
`docs/security.md`. `registry.Dir` is `os.DirFS`, which follows symlinks inside `$CLAUDE_CONFIG_DIR/sessions` (Claude
Code's own 0700 directory). `adapterkit.Getenv` skips a later EMPTY entry where os/exec's `dedupEnv` honours it
(fails closed for the session rule; a doc note). The `sleeper` testscript command is no longer exercised by any
script. The linux `procutil` file was cross-vetted, test-compiled and linted under `GOOS=linux` but first RUNS in
CI. The watch `error` events pass the wire `retryable` through (absent = false); P3-5 keys restarts on
`backoff.Retryable(code)` per 4.3, never on the flag.

## P3-3/P3-4/P3-5 DONE — commands, hooks and the watcher, integrated end to end; the e2e was green for the wrong reason until the verifier crashed the watcher

Three Fable lanes in parallel on disjoint files (P3-3: `internal/harness/commands` + the `internal/cli` table + the
txtar scripts + the `adapterkit.ReadStrict` hardening; P3-4: `internal/harness/hook`; P3-5: `internal/harness/watch`),
then one integrator (`internal/app` routing, the `hook`/`watch` table entries, the one `.golangci.yml` exclusion for
the hook's `spawn.go`, `internal/harness/e2e`, the hook/watch txtar scripts) and two Fable adversarial verifiers (the
injection path; the process path), from `.ignored/briefs/p3-3-4-5-commands-hook-watch.md`. Driven by
`15-implement-brigade-0902T18`. **The Fable session limit stopped the integrator once** ("resets 1am
America/New_York"); per the model tier policy the driver waited and resumed the workflow from its journal at
05:47 EDT with the lanes' results cached. No fix round was needed: 9 defects, 7 fixed in place, 2 left to the driver
(below); 46 checks proven able to fail by mutating the subject and restoring byte for byte (23 per lens).

**What exists now.** `brigade sessions|send|whoami|team members` (6.4's layouts; every remote string sanitised;
`human_label` always ` (unverified)`; `--json` = the adapter's result plus `self_session_id`/`note`; errors one
stderr line `brigade <command> failed (<code>): <message>` with the code's exit; raw adapter stderr never shown,
U-24); `send` validates the body in BYTES before any spawn (U-05, zero spawns proven through the seam), D11's key
(`base64url(sha256(sender\0recipient\0body\0minute))`, stable within a minute), 20 s budget with one retry on
`unavailable`, never on `rate_limited`/`loop_detected`, and refuses a TTY stdin; `team create|join|leave` and
`profile init|status|reset|revoke-credentials` pass through to the adapter with inherited stdio
(`adapterclient.PassThrough`; the adapter's exit status forwarded), `create`/`join` refused inside a session with
exit 2 and the fixed line; `profile init --adapter <spec>` writes the D36 sidecar (and registers
`<name>=<absolute path | JSON array>`) BEFORE the adapter's own `profile init`; `profile status` prints the harness
line naming the default adapter and the in-session override. `send`/`whoami` are session-only (`config`,
`details.reason = not_in_session`). `brigade hook session-start|prompt|session-end` (6.3 as measured: exit 0 on every
failure, `usage` the only non-zero; identity per 6.5 with `session_title` and `permission_mode` optional; D36
resolution into the by-pid map; idempotent per `CLAUDE_PID` with the D9 socket/hash compare and respawn; the resume
hint from by-native (or this pid's own map) and its E0-5 (f) skip; a native `hold`/`refuse` scan → policy `refuse`
plus a warning line; the shadowing warning as a true second line; the prune stamp; the poll path through
`inbound.Pipeline` acking only printed frames — about eight frames fit under the 10,000-character cap; a
registration retry from the prompt hook at most once a minute, which makes the "retrying at your next prompt" line
true; `session-end` SIGTERMs the watcher, compare-then-deletes the pidfile, deletes the by-pid map, `session close`
in 1 s). `brigade watch [--sink <file>]` (6.6 as corrected by E0-5: the pidfile guard with the kernel start token,
duplicate → exit 0, different values → SIGTERM + replace, dead or forged → replace by content; supervision on
`backoff.RetryableExit` — 8/9/signal/unowned restart with 1..30 s jitter, 4/5/10/11 stop with the one-line notice
file, give-up after 10 failures in 5 min → exit 3; `ready` within 10 s; heartbeat 30 s plus an immediate beat on the
registry's busy/idle flip; the name and the socket path re-read from the registry, the policy from the map, every
2 s; liveness by process STATE — gone, zombie, foreign or a changed start token — and by the map (gone, another
session id or profile → exit); **socket `ENOENT` is never an exit**; the exit path stops heartbeats first, closes
the session (1 s clean / 3 s after a Claude death), closes the child, removes the pidfile by content, exit 0 —
measured 14–19 ms after `hook session-end`; `--sink` refused with the socket variable, `config` with nothing to
inject into; the 5 MB single-generation log rotation; the token only in the redactor). `internal/harness/e2e`
drives the BUILT `bin/brigade` and `bin/brigade-adapter-fs`: team create/join through the pass-through, alice's
`session-start` with a fake socket → the context line, both maps, a LIVE detached watcher (pid alive with its start
token); bob registered through the hook with a sleeper; bob's `send` → the frame at the fake socket wrapped in
variant C with the right ids, `injected` in the store; a reply with `--reply-to` → hop 1; the watcher CRASHED
(SIGKILL) → `hook prompt` respawns it (a dead pidfile replaced by content, a second `watch ready`); `session-end` →
the watcher gone, the pidfile and by-pid map removed, `closed_at` set; then the token grepped out of every file
under the rig and every adapter child's argv and environment (U-25, with a positive control). 17 txtar scripts run
every command, hook and the sink watcher through `exec brigade` as real forks.

**The decisive findings.** (1) **The e2e's session-end step was green for the wrong reason**: step 4 stopped the
watcher with SIGTERM, which is its CLEAN path — it closed the session itself — so step 5's `closed_at` assertion
had already been satisfied, and the prompt hook's respawn was only ever exercised for a MISSING pidfile, never a
DEAD one; a pre-session-end assertion proved it (`the session was closed before session-end`). Fixed: step 4 now
SIGKILLs (a crash leaves the pidfile dead and the session open), asserts `pidfile.Check` finds it dead with the old
pid, and step 5's `closed_at` is load-bearing. (2) The watcher's `refuse` test was passing under a mutant that
ignored the map's policy at construction: the fixture's 50 ms map re-read corrected the policy before the message
arrived; fixed with a one-hour poll in that test and a new test for the runtime policy flip through the map. (3)
An adapter-produced `config` at `describe`/`register` printed the generic `not connected (config)` line,
byte-identical to the line for bad stdin — P3-6's D36 acceptance needs the reason: the line now carries the
adapter's fixed `details.reason` (`Brigade: not connected (config: <reason>); …`). (4) The sink refusal test would
have hung for the package's 10-minute timeout under a mutant that accepted `--sink` with the socket variable — now
its own 30 s catcher. (5) The `watcher exiting` exit-status grep matched the child's exit line too — anchored. (6)
Two fs-adapter txtar scripts lacked the `GORACE=atexit_sleep_ms=0` fixture and spent TSan's 1 s exit sleep per
fork (team 10.6 s, profile 15.7 s → 1.2 s and 2.2 s). (7) `docs/adapter-authors.md` still said the wiring was not
runnable.

**A macOS fact that made `make test` red four times in no lane's code (the integrator measured it):** the FIRST
exec of a freshly created executable is suspended 0.2–0.5 s idle and **3–6 s during the opening seconds of a
whole-tree `go test`** (probe: t=3 s 4.47 s, t=6 s 5.85 s, later 0.2–0.4 s; the second exec 7–16 ms), longer than
the 3 s `describe` budget of 4.1 that every fake-adapter txtar and the hook/watch TestMains hit first. **Rule
adopted: every freshly created executable that a budgeted spawn will hit is first run once with no deadline** (a
`describe` warm-up in the ten scripts; `warmExecutable()` after `mustBuild` in the TestMains). `GORACE=
atexit_sleep_ms=0` is set for the race-instrumented test binary and its children in every script. Watch for the
same `adapter did not finish within its deadline` on a slow Linux runner; the levers are `-p` in `go_test_flags` or
a describe-budget seam in `Deps`.

**Plan and brief corrections (measured):** `sessions` always asks the adapter with `--include-offline` and hides
offline locally (the count is unknowable otherwise); `send`'s confirmation names the recipient by id; a registration
made from the prompt hook is named from the cwd (no `session_title` on that document); the policy and shadowing
warnings are additional lines after the one start line (E0-8 (d) strips only the ends); the per-sender poll window
is per prompt (only injected ids persist); plan 6.6's `exec.Command` is `exec.CommandContext(context.WithoutCancel
(…))` (noctx); the watcher environment is `adapterkit.ChildEnv` + `WatcherEnv.Vars()` + the socket and token (so a
proxy/CA reaches the watcher); the fs adapter's rejected stdin `heartbeat`/`ack` is `invalid_input` with
`retryable: true` — the watcher keys restarts on the child's EXIT status through `backoff.RetryableExit`, never on
an event's code; a stalled unix socket is only felt by a content larger than the kernel buffer, so U-20's watcher
row is tested with a 1 ns write deadline; `internal/harness/e2e` needs `internal/adapters/fs` not at all (the built
binary); the conformance suite's literal `--adapter bin/brigade-adapter-fs` line fails C-02 without `--shared-env
BRIGADE_FS_ROOT` (the Makefile's form passes 44/0/1). `smoke.txtar`'s one placeholder row moved from `sessions` to
`inbox` (the last P5-11 placeholder) — blessed. `internal/adapterkit` stays over the 30 s `-count` bound because
of the pre-existing 14 s `TestFlockTimeoutBoundBetweenTwoProcesses` (not this phase's).

**Recorded, not changed, for the threat model and P3-6/P3-7:** a `brigade` symlinked into `~/.local/bin` warns as a shadow only when it does NOT resolve to the plugin's own
bootstrap (P3-6 measured: the setup skill's `ln -s ${CLAUDE_PLUGIN_ROOT}/bin/brigade …` is silent; a symlink to a
`bin/brigade` build warns); a watcher stopped by SIGTERM closes its session (only a crash leaves it open); the
fs adapter re-emits an unacknowledged message only when its watch child restarts; in one-shot mode (no
`stdin_commands`) shutdown waits the close budget before cancelling a child that cannot end on its own (bounded, an
inefficiency); a writer blocked on a full child stdin at exit would lose its `close` (lease expiry closes the
session; both bundled adapters read stdin concurrently); `ReadStrict` returns a planted unix socket as a raw
`ENXIO` error rather than `config` (cosmetic); the shared scratchpad let one verifier's mutation script overwrite
another's mid-run (restored and cmp-verified) — **future briefs give each agent a private scratch subdirectory**.

## P3-6/P3-7 DONE — the plugin runs end to end through real headless sessions; a silent hook leaves no trace anywhere

Two Opus lanes in parallel (P3-6: the Makefile's `plugin-dev [adapter=fs] [profile=<p>]` and its onboarding comment,
`plugin-check.sh` checks 6 and 7 with nine new `checks_test.go` subtests and a Go join test, the harness-side contract
section of `docs/adapter-authors.md`, "Under the plugin" in `internal/adapters/fs/README.md`, `plugin/README.md`'s
developer paragraph, `docs/experiments/E3-wiring.md`; P3-7: `scripts/harness-smoke.sh` (545 lines, POSIX sh,
shellcheck-clean, 100755) and `docs/experiments/E3-smoke.md`) and one Opus adversarial verifier, from
`.ignored/briefs/p3-6-7-wiring-smoke.md`, each agent in a private scratch directory. Driven by
`15-implement-brigade-0902T18`. No fix round: 3 defects, all fixed in place; 24 checks proven able to fail (the two
new plugin-check rules by script AND subject mutation; every one of the smoke's fifteen assertions by doctoring a
real capture or the store).

**Measured through `claude -p` (Claude Code 2.1.259, the session default model, every run from a temporary XDG
triple with the dev pointer under it and the environment stripped by prefix).** (a) After `bin/brigade profile init
--adapter '["<abs>/bin/brigade-adapter-fs"]'` and `bin/brigade team create --name ops --label dev --secret-file …`, a
one-turn session shows the `SessionStart` `hook_response` with exit 0 and the context line naming team `ops`; the
by-pid map (profile, team, the adapter argv, `plugin_bin`, `harness_version 2.1.259`, no token), the by-native map
and the watcher pidfile exist mid-run with the watcher ALIVE; **the session's own `SessionEnd` stopped the watcher
0.361 s before `claude` exited** (pidfile present +0.27 s after start, gone at +2.04 s, `claude` gone at +2.40 s); the
cold-cache path is never taken with a pointer present (`XDG_DATA_HOME/brigade/bin` never created). (b) `make
plugin-check plugin-validate` green with the two new rules. (c) Two profiles, `fsa` with the JSON-array sidecar and
`fsb` with a REGISTERED name carrying a fixed `--root` (`fsdev=[…,"--root",<store2>]` — the only "different adapter"
available without a second backend, and a genuinely separate store), no `adapter_command` in either `--settings`:
each session's start line names its own team, and `brigade sessions` run by the model in each lists only that
team's sessions. (d) An unresolvable `adapter_command` (a relative path) → the D36 line `Brigade: not connected
(config): the adapter for profile "default" could not be resolved from option; …`; a resolvable override that cannot
read profile `ghost` → `Brigade: not connected (config: profile_missing); …`; in both, no watcher. The smoke
(`make harness-smoke`): alice's session lists the team, sends bob a message that lands in bob's fs inbox, receives
bob's message MID-TURN (posted by the script during the model's `sleep 20` through bob's own hook-registered session
with a sleeper as `CLAUDE_PID`), which alice's watcher acknowledges (`acked/<alice>/`), and replies with `brigade
send <bob> --reply-to <id>` — hop 0, 1, 2 in the store; zero native `SendMessage`; nothing through a path or a
shell; `result` success; five nested sessions, 0 flakes, ~30 s each; the verifier's own run 19/19.

**Facts measured that the plan and the earlier blocks did not know.** (1) **A hook that exits 0 and prints nothing
is recorded NOWHERE on 2.1.259** — not in stream-json, not on stderr, not in the transcript JSONL, and `--debug
hooks` adds nothing under `-p`; the transcript records a `UserPromptSubmit` hook only when it printed (as
`hook_success` with `content`) or failed. P3-1's "`UserPromptSubmit` appears in the transcript" holds only for a
hook with output; the smoke asserts the prompt hook's EFFECT (the map's `permission_mode`) instead. The transcript's
`hook_success` record keeps `${CLAUDE_PLUGIN_ROOT}` UNSUBSTITUTED in `command`. (2) **An injected socket message is
never a `user` record in the transcript**: it is a `queue-operation` (`enqueue`, then `remove` with
`absorbed_mid_turn`) plus a `queued_command` attachment whose `origin` is `{kind: peer, from: unknown, name:
<from-name>, body: <the frame>}` — the wrapper's `from-name` becomes the origin's name. (3) The brief's suggested
shape for (d) — the fs binary with `--root` at an EMPTY directory for a bound profile — yields `unauthorized` exit 5,
not `config`: `describe` reads local files only and `register` then finds no such team in that store. (4) With
`--allowedTools "Bash(brigade:*),Skill"` the model's FIRST attempt in both (c) runs was the ABSOLUTE PATH the start
line prints after `terminal commands:` — denied by the rule, after which it ran the bare `brigade sessions`
(nothing broken, but the start line invites the mismatch; P3-8 watches for it interactively and the wording is a
candidate for P5's polish). (5) A `brigade` symlink that resolves to the plugin's bootstrap is silent (the setup
skill's own suggestion); one that resolves elsewhere warns. (6) Under `-p`, `permission_mode` arrives on
`UserPromptSubmit` and not on `SessionStart` (proved with a stdin-logging wrapper). (7) `BRIGADE_FS_ROOT` in the
script's environment is decoration under the plugin: `ChildEnv` drops it; both principals share a store because both
resolve `BRIGADE_STATE_DIR` from the same `XDG_STATE_HOME`. (8) An unlabelled answer inside the implicit reply
window is hop 1 (4.5.12) — bob's first message carries `hops="1"`.

**Recorded, not changed:** the smoke's assertions about what the MODEL chose to run (list, send, reply, no native
tool) held 7/7 sessions but are model properties, not harness properties; the mid-turn window is timing (the post
lands during `sleep 20`), a slow machine could degrade it to "injected at the next boundary"; bob's inbox side of
the `acked/` clause is not exercised (bob has no watcher); an interactive `make plugin-dev` was run by nobody (it
would overwrite the developer's real pointer) — all four parameter forms were dry-run with `make -n`, and P3-8 runs it
at the keyboard; two stray process families from other sessions (the E0-5 python adapters of 31 Aug, a P1-8 shell
matrix of 2 Sep) are still in `ps` and were left alone.

<!-- verbatim from the execution log -->
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

