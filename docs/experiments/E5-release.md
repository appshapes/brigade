# E5-release — preparing 0.1.0: the preconditions, the practice rehearsal, and the cold-cache first prompt

Date: 2026-09-05 · Ticket 15 · Status: **PHASE A — draft; the phase-B sections are marked pending** ·
Harness: `.ignored/proof/<stamp>-release-a/harness/` in the lane worktree (the E0-8 pacing server with a route and a
request log added, plus a driver and a probe plugin written for this item)

Phase A is everything the release needs that does **not** need the release commit or the five shared documents
(P5-12 owns those right now): the preconditions checklist, a practice `DRY_RUN=1` rehearsal, the version-bump list
verified file by file, section 5.4's hazard re-read against the Blacksmith runner, and — the one measurement that is
a precondition of the tag rather than a note after it — the cold-cache first prompt of the release brief's §6.4.

This lane never ran `make release`, never tagged, never pushed, never touched repository visibility. It stops one
command short by design (the owner's ruling of 2026-09-05).

---

## Environment and versions

| What | Value | How |
| --- | --- | --- |
| Host | macOS 26.6.1 (build 25G76), Darwin 25.6.0, arm64 | `sw_vers`, `uname -a` |
| Claude Code | **2.1.261** | `claude --version`; `readlink ~/.local/bin/claude` → `/Users/rjae/.local/share/claude/versions/2.1.261`, identical before and after every session |
| Go | go1.27.0 darwin/arm64; `go.mod` go line `1.27.0` | `go version`; `GOTOOLCHAIN=go1.27.0 go env GOVERSION` → `go1.27.0` |
| goreleaser | **v2.18.0** (GoVersion go1.27.0) | `bin/goreleaser --version` |
| shellcheck (this machine) | 0.11.0 | `shellcheck --version` |
| curl (this machine) | 8.7.1 | `curl --version` |
| Worktree | `fe36367` detached, clean before and after | `git rev-parse HEAD`, `git status --porcelain --untracked-files=normal` |
| Repository | `appshapes/brigade`, **PRIVATE**, no tags, no releases | `gh repo view --json isPrivate,visibility`, `git tag -l`, `gh release list` |

Sessions started by this lane: **10** real Claude Code sessions, all nested with the environment stripped **by
prefix** (`CLAUDE*` — so `CLAUDECODE` too — plus `AI_AGENT`, keeping only `CLAUDE_CONFIG_DIR`), all with
`DISABLE_AUTOUPDATER=1`, none with an `XDG_DATA_HOME` override. The strip removed 11 names every time:
`AI_AGENT`, `CLAUDECODE`, `CLAUDE_CODE_BRIDGE_SESSION_ID`, `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_ENTRYPOINT`,
`CLAUDE_CODE_EXECPATH`, `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN`, `CLAUDE_CODE_SESSION_ID`,
`CLAUDE_EFFORT`, `CLAUDE_PID` — three of which the plan's enumerated eight-name list does not contain.

---

## 1. The preconditions checklist (brief §4, as amended by the addendum)

`yes` / `no` / `phase B`, each with the command that produced it.

**Content**

| # | Precondition | Result | Evidence |
| --- | --- | --- | --- |
| 1 | Every "must" row of §3.1 is `done` | **no — two outstanding** | Status table at `fe36367`: P5-1 `done`, P5-2 `done`, P5-4 `done` (folded into P5-7b), P5-7a/P5-7b `done`, P5-11 `done` (E2E-12 green; E2E-13's drop half an accepted honest negative), **P5-12 `todo` (in flight, LAST before the release)**, **E0-10 `unblocked`, not yet run**. P5-7c (protocol appendices) is a new small row, editorial only. |
| 2 | `CHANGELOG.md` exists with a `0.1.0` entry | **yes** | `CHANGELOG.md:10` = `## [0.1.0] — Unreleased`; the heading becomes `## [0.1.0] — 2026-XX-XX` when the owner tags |
| 3 | The §9 doc edits are committed **before** `make release` | **phase B** | none of them are made yet; §4 below verifies the list file by file |

**Tree and CI**

| # | Precondition | Result | Evidence |
| --- | --- | --- | --- |
| 4 | On `master`, tree clean | **n/a in phase A** | the lane runs in a detached worktree at `fe36367`; the rehearsal used a throwaway branch (below). The real run must be on `master`. |
| 5 | `git pull --no-edit` clean | **phase B** | `DRY_RUN=1` skips it by design (`release-prep.sh`: "DRY_RUN=1: skipping 'git pull --no-edit'") |
| 6 | CI green on the release commit (the brief says "all seven jobs"; `ci.yml` at `fe36367` defines **five** — `fast`, `macos`, `reproducibility`, `supabase`, `deploy-staging` — so the brief's count is stale) | **yes on `fe36367`** | `gh run list --workflow=ci.yml --commit fe363671b593330c7370676418312d64e50545b4` → run **33999363868**, `success`, 12m32s; `gh run view 33999363868 --json jobs` → four `success` and `deploy-staging` `skipped`. The release commit is a later one, so this must be re-run on it — with the **full 40-character sha**; a short sha matches nothing. |
| 7 | Local gates green on that commit | **partly — see the finding** | `make plugin-check checksums-check` **exit 0** (all nine plugin checks; `checksums-check: pre-release state (VERSION 0.0.0, empty plugin/bin/checksums.txt); rules (b) and (c) skipped`). `make lint` **exit 0** ("0 issues" on every configuration). `make typecheck build` exit 0. **`make test` exit 2 on two consecutive full runs** — both in `internal/harness/watch`, both the same shape (see "Findings"), and green on every targeted re-run. `make test-integration` and `make e2e` were **not run**: both need the local Supabase stack, which is shared with other lanes and which this lane was told not to disturb. They belong to the phase-B gate. |

**Release plumbing**

| # | Precondition | Result | Evidence |
| --- | --- | --- | --- |
| 8 | `bin/goreleaser` present, **v2.18.0** | **yes** | `bin/goreleaser --version` → `GitVersion: 2.18.0`; `Makefile:37` `goreleaser_version := v2.18.0`; `release.yml:49` `version: "v2.18.0"`. All three agree. |
| 9 | `GOTOOLCHAIN=go<go.mod line>` selects that toolchain | **yes** | `GOTOOLCHAIN=go1.27.0 go env GOVERSION` → `go1.27.0`, exit 0 |
| 10 | The D1 rehearsal is cited, not repeated blind | **yes** | archive "D1 RELEASE REHEARSAL DONE": release run **33698279695**, `v0.0.1-rc1`, green, torn down; plus P6-5's re-run after Phase 6 rewrote the workflows (`REHEARSAL_EXIT=0`). Both were on **GitHub-hosted** runners — see §5. |
| 11 | A fresh `DRY_RUN=1` run on the release commit | **practice run done here; the real one is phase B** | §2 below |

**Authority**

| # | Precondition | Result | Evidence |
| --- | --- | --- | --- |
| 12 | A token that can push a tag | **yes** | `gh auth status`: account Rjae, scopes include `repo` and `workflow` |
| 13 | Decision 1 answered by the owner | **yes** | addendum §1: **the repository goes public for 0.1.0** |
| 14 | Who runs `make release` | **yes** | addendum §1.2: the owner, or a session the owner directs |

**Added by the addendum (§5, §6)**

| Item | Result | Evidence |
| --- | --- | --- |
| A tag ruleset on `refs/tags/v*` blocking updates and deletions | **absent** | `gh api /repos/appshapes/brigade/rulesets` → `[]`. Owner action, before the tag. |
| P5-17 (PR #1, Blacksmith runners) merged | **not merged** | `gh pr view 1` → `state: OPEN`, `mergeable: MERGEABLE`, `mergeStateStatus: CLEAN`; on run 33998830833 the four jobs that ran — `fast`, `macos`, `supabase`,
`reproducibility` — are `SUCCESS`, and `deploy-staging` and the `[code]smith` check are `SKIPPED`. `master` at `fe36367` still carries `runs-on: ubuntu-latest` / `macos-latest`. |
| The D1 release rehearsal repeated on the Blacksmith runner | **not done** | `release.yml` has run exactly once ever, on a GitHub-hosted runner. Phase B / owner. |
| "Require approval for all outside collaborators" set at flip-public time | **not set** | owner action; on the checklist below |
| The runner-image inventory read | **read — and it changes a standing rule** | see "Findings" |

---

## 2. The practice rehearsal — `DRY_RUN=1 scripts/release-prep.sh 0.1.0`

Run in the lane worktree on a throwaway branch (`release-prep.sh` refuses a detached HEAD, because
`git branch --show-current` is empty there; the script's own second argument exists for exactly this rehearsal).

```
git checkout -b p5-10a-rehearsal                       # at fe36367
DRY_RUN=1 scripts/release-prep.sh 0.1.0 p5-10a-rehearsal
REHEARSAL_EXIT=0      (1.49 s wall; start 2026-09-05T23:51:04Z, end 23:51:06Z)
```

What it did, from its own output:

```
release-prep: preparing v0.1.0 on p5-10a-rehearsal with GOTOOLCHAIN=go1.27.0
release-prep: DRY_RUN=1: skipping 'git pull --no-edit'
release-prep: 1. pinned plugin/bin/VERSION and plugin/.claude-plugin/plugin.json to 0.1.0
release-prep: 2. built dist-cross/ with the release flags
release-prep: 3. goreleaser reproduces dist-cross/checksums.txt byte for byte
release-prep: DRY_RUN=1: stopping before the commit. A real run would now:
release-prep:   4. cp dist/checksums.txt plugin/bin/checksums.txt
release-prep:   4. make push message="15: Release 0.1.0"
release-prep:   5. git tag -a v0.1.0 -m v0.1.0 && git push origin v0.1.0
```

`diff dist/checksums.txt dist-cross/checksums.txt` → **exit 0, no output**: goreleaser's build and `make cross`
agree byte for byte. The four assets and their sha256s (this machine, go1.27.0, from `fe36367`):

| Asset | Bytes | sha256 |
| --- | --- | --- |
| `brigade_0.1.0_darwin_amd64` | 9,025,744 | `7ce160cbf928df8d158371e44c7671e26a9f2bf95b054ebb7836f9bbc0c8c514` |
| `brigade_0.1.0_darwin_arm64` | 8,344,610 | `7b584373010237b4d58160839d85d1352697f13b0a6817f72870740effb1857d` |
| `brigade_0.1.0_linux_amd64` | 8,835,232 | `aaf190a11f30c0b80db713c754d7a7853fb4153bd14e4c052b5bd14826c6cb6e` |
| `brigade_0.1.0_linux_arm64` | 8,192,160 | `9ce2a21aa4b89ddbed34a98621cc8e553786b2d755439c6864bf99d70598eb46` |

These are the bytes of *this* commit; the release commit's will differ if any Go source changes between now and the
tag. The point of the rehearsal is that the two builders agree, not that these hashes are final.

**The release body goreleaser will generate (brief §8's open question, now measured).** The dry run wrote
`dist/CHANGELOG.md`: 123 lines, 13,880 bytes, **122 bullet lines** — one per commit in the repository's whole
history, sorted alphabetically by subject, from `15: Accept either teardown path…` to `15: Write the security
document…`, each prefixed with its full 40-character sha. goreleaser said `couldn't find any tags before "v0.1.0"`
and fell back to everything. The brief's expectation is confirmed: this is what the GitHub release body will contain
unless it is replaced **after** publication with

```sh
gh release edit v0.1.0 --notes-file <the CHANGELOG's 0.1.0 section>
```

which edits the description only and does not move the tag.

**Restored, and proven clean.** `git checkout -- plugin/bin/VERSION plugin/.claude-plugin/plugin.json`; `dist/` and
`dist-cross/` removed by absolute path; back to detached `fe36367`; the throwaway branch deleted. After:
`git status --porcelain --untracked-files=normal` printed **0 lines**, `plugin/bin/VERSION` reads `0.0.0`,
`plugin/.claude-plugin/plugin.json` reads `"version": "0.0.0"`, `plugin/bin/checksums.txt` is **0 bytes**, and
neither `dist/` nor `dist-cross/` exists. (A dry run deliberately leaves both — recorded in "P2-11 / P2-12 DONE",
not changed.)

---

## 3. §6.4 — the cold-cache first prompt. **The prediction holds, and it is worse at low bandwidth than predicted**

### What was predicted, and by what reasoning

`plugin/bin/brigade`'s detached branch is guarded `[ $# -ge 2 ] && [ "$1" = hook ] && [ "$2" = session-start ]`, so
only `SessionStart` returns immediately; `hook prompt` takes the synchronous path, bounded by curl's
`--max-time 45`, under a `UserPromptSubmit` timeout of **5 s** (`plugin/hooks/hooks.json`). `retryConnect`'s
one-per-minute rate limit (`hook.go:93`) was predicted not to engage, because it only runs once the Go binary is
executing and on a *slow* cold cache the bootstrap is killed before `exec`. **The verifier measured that this holds
only for the throttled arms.** On a link fast enough for the bootstrap to finish the download inside the 5 s budget
but not fast enough to finish the registration too, the binary *does* `exec`, `retryConnect` writes its retry stamp
(`prompt.go:109`, written *before* `connect`) and is then killed mid-registration — so every prompt for the next
minute takes the early return and prints nothing at all. Measured 1 of 3 unthrottled cold-cache runs: the context
line never appeared in the session and no `hook_*` attachment was recorded for prompts 2 and 3
(`.ignored/tools/p5-10/verifier/vacfast-analysis.txt`, run `unth-run1`; the other two runs' first-prompt hook took
3,358 ms and 3,740 ms, i.e. under the 5 s bar but not by much). The same band shows in this bundle's own `root`
control, which the results tables below do not cover: both of its GETs completed in 0.195 s and 0.272 s, yet its
first prompt's `hook prompt` was still `hook_cancelled` at 5,016 ms and the context line first appeared on prompt 2.
So the 5 s kill is **download time plus roughly 1.7–4.5 s of registration work**, not the transfer alone — which is
an argument *for* the fail-fast recommendation below, and against reading the degraded-prompt counts as a pure
function of bandwidth.

### The instrument, and what could have made it pass for the wrong reason

- The **shipped** bootstrap, byte for byte: the plugin tree was copied and only `bin/VERSION` (→ `0.1.0`),
  `bin/checksums.txt` (→ the rehearsal's real four-line file) and the manifest version were changed.
  `shasum -a 256` of `plugin/bin/brigade` is identical in the repository and in the copy
  (`e6aa5b911182bf294ee02058933b23748c6ee1a306e27c9a53d681081e07f34d`).
- The asset served is the **real** `brigade_0.1.0_darwin_arm64` from the rehearsal — 8,344,610 B — and the sha256 in
  the plugin's `checksums.txt` is the sha256 of that file, so the bootstrap's integrity check is live, not bypassed.
- The server is E0-8's paced writer (it computes the instant each chunk was *due* and sleeps to it, so drift never
  accumulates), with two changes only: the route is `/` instead of `/asset`, so the bootstrap's real
  `/v0.1.0/brigade_0.1.0_darwin_arm64` path is served, and each request appends an NDJSON row. Measured effective
  rates: **999,908–999,943 B/s** and **249,985–249,989 B/s**.
- **Cold cache without an `XDG_DATA_HOME` override**: the cache file
  `~/.local/share/brigade/bin/brigade-0.1.0-darwin-arm64` is removed by absolute name before every run and its
  absence asserted. (`~/.local/share/brigade` did not exist on this machine at all before the first run. It was
  removed after the throttled arms; the later `root` control run recreated it, and the verifier found
  `~/.local/share/brigade/bin/brigade-0.1.0-darwin-arm64` still present — 8,344,610 B, mode 0755. It must be removed
  before phase B's real-network cold-cache timing, which needs a genuinely cold cache.)
- **`XDG_CONFIG_HOME` *is* a throwaway, and that is load-bearing.** This machine carries a real
  `~/.config/brigade/dev-binary` pointing at a local build. The bootstrap consults that pointer **before** the cache
  and `exec`s it, so a run that inherited it would never download at all and would look perfectly green. The driver
  asserts the pointer is absent in the throwaway config home before every run and again after the profile setup.
- The Brigade profile and team are created inside that throwaway config home with the **fs** adapter, so the
  registration path is real and the context line is a real registration, not a string match.
- A second, independent plugin (`p54probe`, loaded with a second `--plugin-dir`) records every hook fire and, in one
  arm, traps signals — it is the instrument for "which signal", and it never touches Brigade's files.

### Results — 1 MB/s, three runs

| Run | Prompt wall time (s), prompts 1…8 | Prompts degraded | Context line on prompt |
| --- | --- | --- | --- |
| 1 | 6.99, 6.25, **1.61**, 1.26, 1.37, 1.32, 1.18, 1.32 | 2 | **3** |
| 2 | 7.13, 6.39, **1.69**, 1.23, 1.35, 1.24, 1.24, 1.25 | 2 | **3** |
| 3 | 6.98, 6.29, **1.69**, 1.52, 1.31, 1.44, 1.19, 1.56 | 2 | **3** |

### Results — 250 kB/s, three runs

| Run | Prompt wall time (s), prompts 1…10 | Prompts degraded | Context line on prompt |
| --- | --- | --- | --- |
| 1 | 6.92, 6.31, 6.37, 6.31, 6.23, 6.25, **1.89**, 1.28, 1.27, 1.39 | 6 | **7** |
| 2 | 6.98, 6.38, 6.25, 6.28, 6.51, 6.31, **1.77**, 1.45, 1.24, 1.48 | 6 | **7** |
| 3 | 7.06, 6.32, 6.25, 6.33, 6.32, 6.25, **1.71**, 1.52, 1.46, 2.03 | 6 | **7** |

A normal prompt on this machine costs **1.18–2.03 s** (the model turn). A degraded prompt costs **6.22–7.13 s**: the
5 s hook timeout plus the same model turn. 3/3 at each rate; no run disagreed with any other.

### The mechanism, from three independent instruments

1. **Claude Code's own transcript** records each killed hook as an attachment of type **`hook_cancelled`** with
   `exitCode: null` and `durationMs` of **5017–5042 ms** (n = 30, mean 5034.7, median 5035). The successful one is
   `hook_success`, `exitCode: 0`, `durationMs` 327–492 ms, carrying the registration line on its `stdout`.
2. **The server** logs, for every killed hook, a `request_aborted` row: `write: broken pipe` after **5.015–5.032 s**
   and 5,013,504–5,029,888 bytes at 1 MB/s, and after **5.049–5.115 s** and 1,261,568–1,277,952 bytes at 250 kB/s (17 of the 18 aborts are
   5.112–5.115 s / 1,277,952 B; `a250k` run 3's fifth is 5.049 s / 1,261,568 B). The
   download was genuinely in flight and genuinely cut.
3. **The detached `SessionStart` download completes anyway**: one `request_done` per run —
   8,344,610 B in **8.345 s** (1 MB/s, 3/3) and in **33.380–33.381 s** (250 kB/s, 3/3). The `SessionStart` hook
   itself returned in **16–17 ms** (`durationMs`), never blocking startup. E0-8's background variant measured
   19–20 ms and 8.36 s / 33.33 s; the shipped code reproduces it.

The cache file afterwards: present, mode **0755**, sha256
`7b584373010237b4d58160839d85d1352697f13b0a6817f72870740effb1857d` — equal to the committed checksum line for
`brigade_0.1.0_darwin_arm64`. **No orphan `.brigade-0.1.0.*` temp files** in 6/6 runs.

### The signal Claude Code sends on a hook timeout: **SIGTERM**

Measured directly, with a probe hook that arms `trap` for TERM/INT/HUP/QUIT/PIPE and then loops:

```
{"sub":"prompt","pid":11295,"event":"fire","t":1788652812.908364}
{"sub":"prompt","pid":11295,"event":"sigterm","t":1788652817.930945}     # 5.023 s
{"sub":"prompt","pid":11373,"event":"fire","t":1788652819.350617}
{"sub":"prompt","pid":11373,"event":"sigterm","t":1788652824.375198}     # 5.025 s
```

2/2. This is why there are no orphans: the bootstrap's `trap 'rm -f "$tmp"' EXIT HUP INT TERM` catches it, and the
signal reaches curl too (the server sees the connection close at 5.01–5.13 s, not at curl's own 45 s bound).

### The arm the brief did not ask for: **below curl's floor, the session never recovers**

At **150 kB/s** — under the 185.4 kB/s that curl's `--max-time 45` implies for this asset — one run, 6 prompts:

- every prompt degraded (7.08, 6.41, 6.31, 6.39, 6.28, 6.22 s), 6/6 `hook_cancelled`;
- the detached download **fails**: `request_aborted` after **45.115 s** and 6,766,592 bytes, then curl's
  `--retry 3` opens a **second** 45 s attempt (aborted at 45.113 s) and a third — so a doomed first use costs the
  machine roughly three minutes of transfer, not 45 s;
- the cache file never appears (`exists: false`), **the context line never appears at any prompt**, and Brigade is
  simply absent from that session — with nothing on screen to say so, because a `hook_cancelled` prints nothing;
- one temp file was present at the moment of measurement (the third attempt was still running); after the worker
  finally gave up, `~/.local/share/brigade/bin/` was **empty** — the trap does fire.

### Break-evens recomputed for the real 0.1.0 asset

E0-8's instruction is `bar = size/20`, `curl floor = size/45`, `hook floor = size/60`. For 8,344,610 B:

| Bound | Break-even | E0-8's asset (8,324,402 B) |
| --- | --- | --- |
| A 20 s bar on the first use | **417.2 kB/s** | 416.2 kB/s |
| curl's `--max-time 45` | **185.4 kB/s** | 185.0 kB/s |
| the `SessionStart` 60 s timeout | **139.1 kB/s** | 138.7 kB/s |

The asset grew 0.24%; the floors are unchanged in substance. The floor that bites in the shipped design is still
curl's 45 s, because the `SessionStart` path is detached but the detached worker and every synchronous caller are
bounded by it.

### The recommendation

**The prediction holds and the cost is a user-visible one: two prompts at 1 MB/s, six at 250 kB/s, and every prompt
for ever below 185 kB/s.** The author does not fix it here — any bootstrap edit re-opens P1-8's whole evidence base
(six shells, both shellchecks, `plugin-check.sh` checks 8 and 9, `internal/harness/bootstrap/bootstrap_test.go`) and
belongs in its own lane at its own tier, landing **before** the tag. Of the brief's two candidates the second is the
cheaper and the safer:

> **Make any `hook <sub>` on a cold cache fail fast** — exit 9 without fetching — and leave the download to the
> `session-start` path alone.

It is a smaller edit than detaching `hook prompt` (which would need a second detached branch and its own temp-file
and trap reasoning), it removes the 5 s stall completely rather than moving it, it removes the redundant downloads
this measurement observed at every rate (one GET per cold-cache `hook prompt` on top of the detached
`SessionStart` one: 2 GETs per run at 1 MB/s, 7 at 250 kB/s, 10 at 150 kB/s), and it
costs nothing a user notices: on the fast path the cache is warm and the branch is never taken. What it does *not*
fix is the 150 kB/s case — a link that slow leaves the session without Brigade either way — so whichever option is
taken, `docs/setup.md` should say plainly that a first use needs roughly 185 kB/s or better.

Deciding this is the driver's and the owner's, not this lane's.

---

## 4. The version-bump list of §9, verified file by file (report only — no edit made)

`scripts/release-prep.sh` changes exactly three files by itself: `plugin/bin/VERSION` (`0.0.0` → `0.1.0`),
`plugin/.claude-plugin/plugin.json`'s `"version"` (step 1, with the post-bump assertion), and
`plugin/bin/checksums.txt` (step 4, copied from goreleaser's output). Verified in the rehearsal: those three and
nothing else moved.

Everything below belongs to the preparation commit that lands **before** `make release`. Line numbers are at
`fe36367`; P5-7b moved several of them from the brief's numbers.

| File | Where, today | What must change |
| --- | --- | --- |
| `CHANGELOG.md` | **exists**; `:10` `## [0.1.0] — Unreleased`; `:15` "until that run the repository carries the pre-release `0.0.0` and an empty checksums file" | date the heading; P5-10 supplies the release-mechanics half (asset names and platforms, the checksum-pinning model, the install routes, the `go install` caveat) and adds nothing that is not in it |
| `README.md` | `:110–112` "there is no tag yet (`plugin/bin/VERSION` reads `0.0.0` and `plugin/bin/checksums.txt` is empty)" | rewrite for a released 0.1.0; keep the honest "not done" list (P5-5, P5-6 and P5-9's `hold` are settled — P5-5/P5-6 discarded, `hold` shipped — so that paragraph is *shorter* than the brief assumed) |
| `plugin/README.md` | `:154` `## Status`; `:168` "The pins are still at the pre-release `0.0.0` with an empty `bin/checksums.txt`, so there is no release to download yet" | rewrite. **`plugin-check.sh` check 7** greps this file for `not (yet )?(implemented\|runnable)` and requires a `## Status` section — run `make plugin-check` after the edit |
| `plugin/README.md` | `:47` "`pluginConfigs` key is `brigade@inline` for a `--plugin-dir` checkout and `brigade@brigade` for a marketplace install" | the **`inline` half is confirmed** here (see below); the `brigade@brigade` half is phase B |
| `docs/setup.md` | `:90` `terminal: /Users/you/.claude/plugins/brigade/bin/brigade`; `:98` `ln -s /Users/you/.claude/plugins/brigade/bin/brigade ~/.local/bin/brigade` | **both are wrong for a marketplace install** and both hardcode `~/.claude` where the config dir is `CLAUDE_CONFIG_DIR ?? ~/.claude`. The documented cache layout is `<config dir>/plugins/cache/<marketplace>/<plugin>/<version>/`, which carries the **version**, so the symlink breaks on every plugin upgrade. Correct from phase B's measurement and settle whether the `ln -s` instruction survives at all. |
| `docs/setup.md` | — | new: the install section (both routes), the one-time `claude login` per config dir, `go install` (public form only — the addendum drops the `GOPRIVATE` text), the "no Homebrew tap in 0.1.0, it is P5-16" note, and (recommended, from §3 above) the bandwidth a first use needs |
| `docs/adapter-authors.md` | `:1985` "Only the packaged release pins are still at the pre-release `0.0.0`, so there is nothing to download yet" | rewrite that sentence only. Every other `0.0.0`/`0.0.0-dev` in that file is the **fs dev adapter's** conformance output (e.g. `:446`) and must stay. |
| `docs/experiments/README.md` | the index tables | add this file's row — **phase B**, after P5-12's own row lands (this lane must not touch that file) |

Do **not** grep-and-replace `0.0.0`. The tracked files that must keep it: `CLAUDE.md:30`, `Makefile:114`,
`.github/workflows/ci.yml:51` (all three describe the pre-release state as a state), `docs/adapter-authors.md:446`,
`docs/experiments/E3-wiring.md:119`, and every `cmd/brigade/testdata/script/*.txtar` and `main_test.go` fixture
(`0.0.0-fake`, `0.0.0-testscript`).

**`CLAUDE_PLUGIN_ROOT` for a `--plugin-dir` plugin, measured (the control for phase B's marketplace measurement).**
From the probe plugin's own hook environment, in all four hooks of one session:

```
CLAUDE_PLUGIN_ROOT=/…/harness/probe          # the checkout itself
realpath=/…/harness/probe                     # identical — used IN PLACE, not copied
CLAUDE_PLUGIN_DATA=/Users/rjae/.claude-ifthen/plugins/data/p54probe-inline
```

Two things follow. The docs' "a `--plugin-dir` plugin is used in place" is confirmed on 2.1.261. And the data
directory's id is `<plugin>-inline`, which corroborates `plugin/README.md:47`'s `brigade@inline` for the
`pluginConfigs` key of a `--plugin-dir` checkout; the `brigade@brigade` half still needs a marketplace install.

---

## 5. §5.4's hazard, re-read against the Blacksmith runner

`master` at `fe36367` still runs `release.yml` on `ubuntu-latest`; PR #1 (P5-17) would move it to
`blacksmith-4vcpu-ubuntu-2404`. Both states matter, because which one is in force on the day of the tag depends on
a merge that has not happened.

The hazard itself is unchanged by the runner: **the release commit is already on `master` when the workflow fails.**
Deleting the never-published tag does not remove the commit, so `master` then pins `0.1.0` with checksums that no
published release backs, and `make checksums-check` rule (c) passes only by its fresh-build arm — any commit that
changes Go source before the retag turns the `fast` job red. Keep the fix to non-Go files, or retag first.

What the runner change does alter:

- **The 1m35s sample no longer bounds the timeout.** `release.yml` has `timeout-minutes: 10` and exactly one
  observation ever (D1, GitHub-hosted, guard 1m02s + goreleaser 9s). On a 4-vCPU Blacksmith VM the guard step —
  which runs `make cross`, four full cross-compiles — is the long pole and is unmeasured. 10 minutes is probably
  still generous, but "probably" is the whole content of the claim.
- **The publish half is unexercised on Blacksmith.** goreleaser-action@v7, the `gh` CLI in the Publish and
  Discard-the-draft steps, and the `contents: write` token path from a third-party VM have never run there. The PR's
  green `fast` and `reproducibility` jobs prove the *build* half only. This is why the addendum puts a repeat of the
  D1 rehearsal on Blacksmith before the tag.
- **Cross-host reproducibility.** `make cross` pins `GOTOOLCHAIN`, `CGO_ENABLED=0`, `-trimpath`, `-buildvcs=false`,
  `GOAMD64=v1`, `GOARM64=v8.0`, so the host should not enter the bytes — and the PR's `reproducibility` job is green
  on Blacksmith. But the job now builds *both* artifacts on Blacksmith, so it no longer witnesses
  Blacksmith-vs-GitHub-hosted equality. The release guard's own `diff dist-cross/checksums.txt
  plugin/bin/checksums.txt` is what actually protects the tag, and it will run on whichever runner is in force.
- **A failed run is more expensive to reason about**, because the recovery path (`git tag -d` / `git push --delete`)
  needs the token that the third-party runner also holds — which is the reason the tag ruleset is a precondition
  rather than a nicety.

Two things the lane found while re-reading, both for the owner or the driver rather than for this document:

1. **The Blacksmith Ubuntu 24.04.3 image ships `shellcheck 0.9.0`**, not 0.10 (run 33998830833's inventory step:
   `Ubuntu 24.04.3 LTS x86_64`, `shellcheck 0.9.0`, `go1.27.0`, `Docker 28.0.4`, `jq-1.7.1`, `curl 8.5.0`,
   `gh 2.85.0`). `CLAUDE.md`'s standing rule — "CI's Ubuntu runner has shellcheck 0.10 and this machine 0.11" with
   the `koalaman/shellcheck:v0.10.0` Docker check — is wrong the moment PR #1 merges, and the pinned Docker image no
   longer matches CI. Phase B edits a shell file (`release-prep.sh`'s recovery header), so this has to be settled
   first.
2. `release.yml` fires on `v*` and nothing else — no `workflow_dispatch` — so a release cannot be re-driven without
   a tag. A workflow-only fix after a failure therefore requires the operator to run step 5 by hand
   (`git tag -a v0.1.0 -m v0.1.0 && git push origin v0.1.0`), because a re-run of `make release version=0.1.0` dies
   at step 4 with "nothing to commit". Phase B adds that to the script's recovery header, comment only.

---

## For the owner

1. **Set a tag ruleset before anything else.** `refs/tags/v*`, blocking updates and deletions. Today
   `gh api /repos/appshapes/brigade/rulesets` returns `[]` and `master` is unprotected; the release token
   (`contents: write`) lives on a third-party runner. Expect: the ruleset listed, and `git push --delete origin
   v0.1.0` refused thereafter — which is deliberate, and is why the never-published-tag recovery of §5.4 must be
   done *before* the ruleset if a rehearsal tag is still outstanding.
2. **Flip the repository to public.** GitHub → Settings → General → Danger zone → Change visibility. Release assets
   take the repository's visibility; there is no per-release switch. Expect `gh repo view --json isPrivate` to read
   `false`. In the same sitting: Actions → General → Fork pull request workflows → **"Require approval for all
   outside collaborators"** (Blacksmith runners are self-hosted VMs and `ci.yml` runs on every `pull_request`), and
   note that `docs/setup.md` §3's "nothing to do while private" sentence about GitHub's 60-day rule for scheduled
   workflows becomes a standing responsibility for the keep-alive.
3. **Run the release, or direct a session to.** From a clean `master`, after the preparation commit and its green CI:
   ```sh
   make release version=0.1.0
   ```
   Then watch `release.yml` to `success`: `gh run list --workflow=release.yml --limit 3`,
   `gh run watch <run-id> --exit-status`. Expect six steps and a published, non-draft, non-prerelease `v0.1.0` with
   five assets. If it fails, the draft is discarded automatically and nothing is published; the tag is not —
   `git tag -d v0.1.0 && git push --delete origin v0.1.0` is permitted **only** while nothing was published, and a
   published tag is never moved (a mistake costs a `v0.1.1`).
4. **Replace the release body.** goreleaser will generate 122 commit lines (measured, §2). After the workflow
   publishes: `gh release edit v0.1.0 --notes-file <the CHANGELOG's 0.1.0 section>`. This does not move the tag.
5. **Then hand the distribution proof to the next session**: §6.3's public route from a fresh `CLAUDE_CONFIG_DIR`
   with its one-time `claude login`, the marketplace install, `CLAUDE_PLUGIN_ROOT` for a marketplace install, the
   real-network cold-cache timing on GitHub's CDN, and §5.3's published-state verification.

Two decisions of this lane's that need the owner's eye before the tag: **the §6.4 finding** (a bootstrap change in
its own lane, landing before `v0.1.0`, or a documented limitation) and **`make test`'s flaky watch package**
(below).

---

## Findings the release owner should see

1. **§6.4 is confirmed** (above): 2 degraded prompts at 1 MB/s, 6 at 250 kB/s, and a permanently Brigade-less
   session below 185 kB/s. Recommendation: make `hook <sub>` fail fast on a cold cache, in its own lane, before the
   tag.
2. **`make test` is not reliably green at `fe36367` on a loaded machine.** Two consecutive full runs failed, each in
   `internal/harness/watch` and each on the same assertion shape: `stopAndWait()` returned **0** and the session's
   `ClosedAt` was still `nil` (`TestRefuseNeverPostsOrAcks`, `inject_test.go:139`, then
   `TestHoldWritesPendingAndNeverPostsOrAcks`, `hold_test.go:87`). Re-running the test alone (3/3), the package
   alone, and the package with `-count=3` all passed. Either the exit path's `session close` budget is being missed
   under load — in which case a watcher can exit 0 leaving the session `online` until its lease expires, which is a
   product statement — or the fixture races its own store read. Precondition 7 asks for a green `make test` on the
   release commit; on this machine that is a coin toss, and the release day is the wrong day to find out.
3. **The Blacksmith image has shellcheck 0.9.0** (above), which invalidates `CLAUDE.md`'s shellcheck sentence and
   the `koalaman/shellcheck:v0.10.0` Docker pin the moment PR #1 merges.
4. **A cold-cache session re-downloads the asset on every degraded prompt.** The detached `SessionStart` worker and
   the first `UserPromptSubmit` hook race, and both complete when the link is fast enough (measured: two
   `request_done` rows in the unthrottled arm, two 8 MB transfers); when it is not, each killed prompt hook has also
   pulled 1.2–5.0 MB before the kill, so a run made 2 GETs at 1 MB/s, 7 at 250 kB/s and 10 at 150 kB/s. It at least
   doubles first-use bytes for every user, and the fail-fast fix of finding 1 removes it.

---

## Honest limits — what phase A did not prove

- **Nothing here touches the network or GitHub.** Every download was from a local server on `127.0.0.1`. The
  real-network first-use timing, the release URL, the CDN and the row's acceptance criterion are phase B.
- **No marketplace install was performed**, so `CLAUDE_PLUGIN_ROOT` for a marketplace install and the
  `brigade@brigade` half of `plugin/README.md:47` remain unmeasured. Only the `--plugin-dir` control was measured.
- **The one-time `claude login` per config dir was not measured.** Every session here reused the machine's existing
  `CLAUDE_CONFIG_DIR`, precisely so that no login was needed; E0-7's "one login suffices" is still inferred.
- **`go install`'s version string is unmeasured.** The release binary prints `0.1.0` (`brigade version`, exit 0,
  measured); the claim that `go install …@v0.1.0` prints `v0.1.0` needs a public tag and is phase B.
- **The §6.4 sessions were driven headless** (`claude -p --input-format stream-json`), one session with several
  prompts. That exercises the same hooks with the same `CLAUDE_PID`, but it is not a person typing into a terminal,
  and a pty session may schedule its prompts differently.
- **The 150 kB/s arm is n = 1.** It is unambiguous in what it shows, but it is one run.
- **The prompt used is trivial** ("reply with ACK"), so the 1.18–2.03 s baseline is this machine's floor, not a
  realistic turn. The degradation is the 5 s constant added to it; the *ratio* would be smaller for a real turn.
- `make test-integration` and `make e2e` were not run (they need the shared local Supabase stack), so precondition 7
  is answered only for the Docker-free half.
- The asset sizes and sha256s here are `fe36367`'s. The release commit's will differ.

---

## Phase B — pending

The release commit's `DRY_RUN=1` rehearsal; the D1 rehearsal repeated on Blacksmith (a throwaway tag, torn down);
the `0.0.0` → `0.1.0` document rewrites and `docs/setup.md`'s install section; `release-prep.sh`'s recovery-header
comment (both shellcheck versions, whichever they turn out to be); the release run id with per-step durations; the
published asset table with sizes and sha256s and the byte-equality of the published and committed `checksums.txt`;
the marketplace measurements; the login finding; the real-network timings; and the row's acceptance criterion end to
end. This document's row in `docs/experiments/README.md` is added then, not now.
