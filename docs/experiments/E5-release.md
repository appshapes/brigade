# E5-release — the 0.1.0 and 0.2.0 releases: preconditions, rehearsal, and the distribution proof

Date: 2026-09-05/06 (0.1.0) and 2026-09-07 (0.2.0) · Ticket 15 · Status: **0.2.0 RELEASED 2026-09-07 — tag
`v0.2.0` on `ccc6a12`, release run 34109358721, five assets; the distribution proof is in "0.2.0 release
(P7-9)" at the end of this document. 0.1.0 RELEASED 2026-09-06 — tag `v0.1.0` on `2fb158b`, run 34029404604,
five assets; section 11 measured after the tag (see "Measured after the tag")** ·
Harness: `.ignored/proof/<stamp>-release-a/harness/` in the lane worktree (the E0-8 pacing server with a route and a
request log added, plus a driver and a probe plugin written for this item); phase B's records are in
`.ignored/tools/p5-10b/author/`

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

## 6. Phase B — the marketplace install, the one-time login, and `CLAUDE_PLUGIN_ROOT`

Phase B's measurements ran on **2026-09-06, 04:06–04:17 UTC**, in the worktree `.ignored/wt/p5-10b`, detached at **`7bc6f61`**
(P5-12 landed there; P5-18 landed at `1c849bd`; P5-17 merged at `897e75a`). Claude Code **2.1.263** —
`readlink ~/.local/bin/claude` → `/Users/rjae/.local/share/claude/versions/2.1.263`, the same before and after every
invocation. Same host as phase A: macOS 26.6.1 (build 25G76), Darwin 25.6.0, arm64. Raw records, one file per
command, in `.ignored/tools/p5-10b/author/`.

**Sessions: three `claude -p` invocations, two of which became sessions.** All three were nested with the
environment stripped **by prefix** — every name beginning `CLAUDE` (so `CLAUDECODE` too) plus `AI_AGENT`, keeping
only `CLAUDE_CONFIG_DIR` — and with `DISABLE_AUTOUPDATER=1`. The two that became sessions also carried a throwaway
`XDG_CONFIG_HOME`, `XDG_DATA_HOME` and `XDG_STATE_HOME` under one `mktemp -d` root; the first never started a
session and needed none. The strip removed the same 11 names phase A measured: `AI_AGENT`,
`CLAUDECODE`, `CLAUDE_CODE_BRIDGE_SESSION_ID`, `CLAUDE_CODE_CHILD_SESSION`, `CLAUDE_CODE_ENTRYPOINT`,
`CLAUDE_CODE_EXECPATH`, `CLAUDE_CODE_MESSAGING_SOCKET`, `CLAUDE_CODE_MESSAGING_TOKEN`, `CLAUDE_CODE_SESSION_ID`,
`CLAUDE_EFFORT`, `CLAUDE_PID`. Invocation 1 (the login measurement) exited before a session existed; invocation 2
(a control) and invocation 3 (the measurement) were real sessions of 4 s and 5 s. Sixteen `claude plugin …` commands
ran besides, across three throwaway configuration directories and the machine's own; those are not sessions.

### 6.1 What moved since phase A

| Phase A said, at `fe36367` | Now, at `7bc6f61` | How |
| --- | --- | --- |
| P5-12 `todo`, in flight | landed in `7bc6f61` | the execution log's "P5-12 DONE" |
| the cold-cache first prompt is a finding for the owner | P5-18 landed at `1c849bd`: the session-start worker is the only downloader, a failed install is reported once, the retry stamp follows the attempt | "P5-18 DONE" |
| P5-17 (PR #1) not merged | merged as `897e75a`; master's CI runs on Blacksmith | the log, 2026-09-06 02:0x |
| the repository is private | **public** | `gh api repos/appshapes/brigade` → `"private": false`, `"visibility": "public"` (04:16:50Z) |
| no tag ruleset (`[]`) | `protect-release-tags`, id **22364575**, target tag, `refs/tags/v*`, enforcement **active** | `gh api /repos/appshapes/brigade/rulesets` (04:16:50Z) |
| the outside-collaborator setting not set | `approval_policy: all_external_contributors` | `gh api /repos/appshapes/brigade/actions/permissions/fork-pr-contributor-approval` |
| CI green on `fe36367` (run 33999363868) | green on `7bc6f61`: run **34010317694**, 7m52s, `fast`, `macos`, `supabase` and `reproducibility` all `success`, `deploy-staging` skipped | `gh run list --workflow=ci.yml --commit 7bc6f61ae71d3442add39c84095ec6b507f45158` |
| E0-10 inside the "must" list | **waived** for 0.1.0, with the daily keep-alive and P5-1's hosted conformance as the mitigation | the addendum's ruling of 2026-09-05 23:1x |
| the D1 rehearsal repeated on Blacksmith: not done | done, and torn down — section 9 | the driver's record |
| no tag, no release | still none: `gh api repos/…/tags` empty, `gh release list` empty (04:16:50Z) | the release is the owner's command |

### 6.2 The instrument, and what could have made it pass for the wrong reason

- **The plugin at `7bc6f61` pins `0.0.0` with an empty `bin/checksums.txt`, so there is no release binary to
  download.** The session arm therefore put a dev-binary pointer in its throwaway `XDG_CONFIG_HOME` so the
  marketplace copy's bootstrap had something to `exec`. This section measures **where Claude Code puts a
  marketplace plugin and what it exports**, not the download: phase A measured the download against a local
  server, and the real network is after the tag (section 11).
- **The whole XDG triple is a throwaway**, so the machine's own dev pointer (`~/.config/brigade/dev-binary`, which
  exists on this machine and would have made any run look green) and its cache could not be borrowed.
  `~/.local/share/brigade` did not exist before the run and did not exist after it.
- **A fresh `CLAUDE_CONFIG_DIR` cannot start a session** (6.4 below), so the session arm used the machine's real
  configuration directory. The marketplace was added there and the plugin installed at **local** scope from a
  throwaway working directory, then both were removed. Afterwards `settings.json`, `plugins/known_marketplaces.json`
  and `plugins/installed_plugins.json` are **byte-identical** to copies taken before (`diff`, three times no
  output), and `plugins/cache` and `plugins/marketplaces` hold exactly what they held before.
- **The Brigade profile and team are the fs adapter's**, created inside the throwaway config home, so the session
  registered for real and `brigade whoami` read a real by-pid map rather than a string.
- **The option that proves the settings key was one the output shows.** The session was launched with
  `--settings '{"pluginConfigs":{"brigade@brigade":{"options":{"team_inbound":"refuse"}}}}'`; a wrong key would
  have left the default `accept` in the same line that carries the path.

### 6.3 `CLAUDE_PLUGIN_ROOT` for a marketplace install — the row's `[likely]` comes off

The whole measurement is one `brigade whoami` inside a real session with the marketplace-installed plugin:

```
session a4de4ad747c7e654982a59622b0a8818 "cwd-1d" in team "p510b" (profile default, adapter brigade-adapter-fs 0.0.0-dev); inbound: refuse
terminal: /Users/rjae/.claude-ifthen/plugins/cache/brigade/brigade/0.0.0/bin/brigade
frame: open
```

The `terminal:` line prints the by-pid map's `plugin_bin`, which the `SessionStart` hook sets from
`${CLAUDE_PLUGIN_ROOT}` (`internal/harness/hook/hook.go`'s `pluginBin`, `${CLAUDE_PLUGIN_ROOT}/bin/brigade` resolved
through symlinks). So on Claude Code 2.1.263 a marketplace install's `CLAUDE_PLUGIN_ROOT` is

```
<configuration directory>/plugins/cache/<marketplace>/<plugin>/<version>/
```

exactly as both Claude Code documentation pages describe it, **with the version in the path**. Three further
readings agree:

| What | Measured | How |
| --- | --- | --- |
| the second instrument | `/Users/rjae/.claude-ifthen/plugins/cache/brigade/brigade/0.0.0/bin/brigade` | `find "$CLAUDE_CONFIG_DIR/plugins" -maxdepth 6 -name brigade` |
| the CLI's own answer, in a **fresh** configuration directory | `installPath: /tmp/p5-10b.…/config/plugins/cache/brigade/brigade/0.0.0`, `id: brigade@brigade`, `version: 0.0.0`, `scope: user`, `enabled: true` | `claude plugin list --json` |
| the marketplace's name | `brigade` (source `github`, repo `appshapes/brigade`), cloned to `<config dir>/plugins/marketplaces/brigade` | `claude plugin marketplace list --json` |
| the `--plugin-dir` control | the checkout itself, used in place, data directory id `<plugin>-inline` | phase A, section 4 |

**`plugin/README.md`'s claim is confirmed, both halves.** The `pluginConfigs` key is `brigade@brigade` for a
marketplace install — the option passed under that key produced `inbound: refuse` in the line above — and
`brigade@inline` for a `--plugin-dir` checkout (phase A). The same id is what the install writes into the settings
file it declares:

```json
"enabledPlugins": { "brigade@brigade": true }
```

Two more facts from the same runs, both relevant to `docs/setup.md`:

- **What is copied.** The plugin copy is the plugin tree only — 56 KB, `bin/brigade` mode `0755`. The marketplace
  clone beside it is the whole repository, 17 MB. Nothing is compiled, no package manager runs, and no server is
  started; the install is a git clone plus a directory copy. (The row's negative criterion — "no install step of
  any other kind runs" — is asserted here for the install itself at `0.0.0`; the after-tag proof still owes the
  process tree of a first session.)
- **The transport.** `claude plugin marketplace add appshapes/brigade` printed `Cloning via SSH:
  git@github.com:appshapes/brigade.git` and succeeded. `CLAUDE_CODE_PLUGIN_PREFER_HTTPS=1` did **not** change that
  on 2.1.263 — a second fresh configuration directory cloned over SSH again. With SSH forced to fail
  (`GIT_SSH_COMMAND=/usr/bin/false`, a third fresh directory) the CLI printed `SSH clone failed, retrying with
  HTTPS: https://github.com/appshapes/brigade.git` and succeeded. So a user with no GitHub key can add the
  marketplace from the public repository; the machine's git configuration was otherwise untouched, so a machine
  with no GitHub account at all is still unmeasured.

### 6.4 The login finding

E0-7 item 4 reproduces on 2.1.263: a fresh `CLAUDE_CONFIG_DIR` does **not** inherit the login.

```
CLAUDE_CONFIG_DIR=<fresh> claude -p "reply with OK"
Not logged in · Please run /login          exit 1, about 1 s
```

What is new, and what `docs/setup.md` now says: **the plugin commands need no login at all.** In that same fresh,
never-logged-in directory, `claude plugin marketplace add appshapes/brigade`, `claude plugin marketplace list
--json`, `claude plugin install brigade@brigade -y --scope user` and `claude plugin list --json` each exited **0**,
and the install reported `9 userConfig options not yet set`. Installing is login-free; starting a session is not.

**Still not measured:** that *one* login makes a fresh configuration directory usable. It needs a browser and is
the owner's to do. E0-7's own limits sentence stands, and `docs/setup.md` says only what was measured — a
directory that has never been logged in stops with that line, so log in once in it.

**The command is `claude auth login`, not `claude login`** (the verifier's correction, measured on 2.1.263).
`claude --help` lists no `login` command: it lists `auth`, whose subcommands are `login`, `logout` and `status`.
A bare `claude login` is parsed as a *prompt*, so in a fresh configuration directory it answers the same
`Not logged in · Please run /login` and exits 1 without logging anyone in, and in a logged-in one it would send
the word "login" to the model. Inside a session the slash command is `/login`, which is what Claude Code's own
message names. `docs/setup.md` and the checklist below say `claude auth login`. E0-7 and the brief both carry
the old form; they are records of their own date and are left as they are.

**Two shipped sentences the verifier corrected in `docs/setup.md`.** The login command above, and the install's
`-y` flag: `claude plugin install brigade@brigade` with **no** flag, stdin and stdout both redirected, in a fresh
never-logged-in configuration directory, exited **0** with no confirmation prompt (`✔ Successfully installed
plugin: brigade@brigade (scope: user)`). On 2.1.263 `-y` is documented as accepting a *marketplace-declared
command* — a plugin installed by running a command, or one whose archive comes through a `headersHelper` — and
Brigade's marketplace entry declares none, so the flag changes nothing for it. The brief's §6.2 note that `-y` is
"required when stdin or stdout is not a TTY" is true only of that command-declaring case.

## 7. The version strings

Measured at `7bc6f61` on 2026-09-06. The release flags are the ones `make cross` and `make release` use
(`GOTOOLCHAIN=go1.27.0 CGO_ENABLED=0 -trimpath -buildvcs=false -ldflags '-s -w -X …/internal/buildinfo.Version=<v>'`).

| Build | `brigade version` prints | Bytes (darwin/arm64) |
| --- | --- | --- |
| the release flags at `0.1.0` | **`0.1.0`** | 8,361,218 |
| `go install github.com/appshapes/brigade/cmd/brigade@7bc6f61ae71d3442add39c84095ec6b507f45158` | **`v0.0.0-20260906035827-7bc6f61ae71d`** | 12,276,130 |
| `make build` (the dev build) | `0.0.0-dev` | — |
| `go install …@v0.1.0`, after the tag | `v0.1.0` — the same mechanism, one measurement short | — |

Three things this settles.

1. **The module is public and installable.** That `go install` ran with no credentials, no `GOPRIVATE` and no git
   configuration of its own: it downloaded `github.com/appshapes/brigade v0.0.0-20260906035827-7bc6f61ae71d`
   through the module proxy and exited 0. `GOBIN` pointed at the lane's scratch directory on purpose — a `brigade`
   from `go install` on `PATH` shadows the plugin's pinned bootstrap (E0-8 (e)), which is the warning
   `docs/setup.md` now carries beside the command.
2. **The leading `v` is real and is not a defect.** `go install` applies no `-ldflags`, so `internal/buildinfo`
   falls back to `debug.ReadBuildInfo().Main.Version`, which is the module version — a pseudo-version here, and
   `v0.1.0` from a tag. The released binary carries `0.1.0` through `-X`. Both documents state the difference and
   neither "fixes" it.
3. **The asset is about 8.4 MB and the floors of phase A still hold.** 8,361,218 B against phase A's 8,344,610 B
   at `fe36367`: `size/45` is 185.8 kB/s (phase A: 185.4), `size/20` is 418.1 kB/s, `size/60` is 139.4 kB/s. The
   sentence P5-18 put in `docs/setup.md` — roughly 185 kB/s or better — is that middle number.

## 8. The release documents, and what changed in each

The preparation commit lands **before** `make release`, which then rewrites `plugin/bin/VERSION`,
`plugin/.claude-plugin/plugin.json` and `plugin/bin/checksums.txt` itself. This lane changed none of those three:
at the end of it `VERSION` still reads `0.0.0` and `checksums.txt` is still 0 bytes.

| File | What phase B wrote |
| --- | --- |
| `docs/setup.md` | a new **"Installing the plugin"** section: the marketplace route with both commands and their in-session forms, the one-time login per configuration directory, the `--plugin-dir` route and its `brigade@inline` key, the first-use paragraph (8 MB in the background, roughly 185 kB/s, one `Brigade: not installed:` line if it cannot finish), `go install` in its public form with the leading-`v` and shadowing notes, and the "no Homebrew tap and no `.deb` or `.rpm` in this release" note with its reason |
| `docs/setup.md`, "Terminal use" | the corrected `terminal:` example — `…/plugins/cache/brigade/brigade/0.1.0/bin/brigade`, from 6.3, with the `frame:` line the shipped `whoami` prints — and the symlink instruction: `ln -sf`, plus the paragraph that settles the version question (**re-point it after a plugin upgrade**; `brigade whoami` prints the new path; a `--plugin-dir` checkout has no version in its path) |
| `docs/setup.md`, keep-alive section 4 | the repository is public, so GitHub's 60-day rule for scheduled workflows now applies and is an administrator's standing task |
| `README.md` | the Status paragraph: Phase 5 done, **0.1.0 is the first release** and what `make release` writes, the install line, and what is not in 0.1.0 (no tap, no Linux package, no keychain) |
| `plugin/README.md` | the Status paragraphs: the frame levels ship; `bin/VERSION` and `bin/checksums.txt` explained as what a session downloads and checks; the pre-release `0.0.0` state described as a state rather than as today. Plus one sentence in the hooks bullet for P5-18's cold-cache behaviour |
| `docs/adapter-authors.md` | the one sentence about the packaged release pins |
| `CHANGELOG.md` | the heading dated **2026-09-06**; the release-mechanics half of the entry (how it is installed, what is published — four assets and a `checksums.txt`, macOS and Linux only — and how the plugin trusts them); and P5-18's item under "Added — the plugin" |
| `docs/security.md` | section 12 "Reporting a problem" completed with the public issues link and the two rules (never paste a secret; leave a security problem's working details out of a first public issue), and the five lines that used the retired word for the injection test set reworded to "the 26 test messages" and "those test messages", with no number changed |
| `scripts/release-prep.sh` | the recovery header only: the workflow-only fix that dies at step 4, the by-hand step 5, `release.yml`'s `v*`-only trigger, and the red-`fast`-job hazard of a release commit whose tag was deleted. `shellcheck -s sh` 0.11 (local) and 0.9.0 (`koalaman/shellcheck:v0.9.0` in Docker, the version CI's Blacksmith image carries) both exit 0, and so does `sh -n` |
| `docs/experiments/README.md` | this file's row in the Phase 5 table |

## 9. The release rehearsal on the Blacksmith runner (the driver's, folded in)

The driver ran D1's whole chain again on the merged Blacksmith runner while this lane wrote, on a throwaway branch
and a throwaway tag, and tore it down. Its record is `.ignored/tools/p5-10b/rehearsal-blacksmith.md` with the raw
files beside it; the short form:

- `make release version=0.0.1-rc2 branch=rehearsal/0.0.1-rc2` exit 0 — the pins bumped, `make cross`, goreleaser
  reproducing `dist-cross/checksums.txt` byte for byte, the commit `df9df40` of exactly the three files, and the
  tag pushed **with the ruleset active** (it blocks updates and deletions, not creation).
- `release.yml` run **34010542882 on `blacksmith-4vcpu-ubuntu-2404`: every step green in 37 s** (04:03:54–04:04:31
  UTC), against D1's 1m35s on a GitHub-hosted runner. The guard's `make cross` on the runner reproduced the
  checksums this machine committed.
- The published release carried five assets; the published `checksums.txt` was **byte-identical** to the committed
  `plugin/bin/checksums.txt` (`cmp`), and `shasum -a 256 -c` over the four downloaded binaries was OK four times.
  `make checksums-check` passed all three rules on the release commit. One honest gap in the driver's record: that
  the release was not a draft and was a pre-release is **inferred** from which workflow branch ran, not read back —
  the read-back used a wrong JSON field name and the release was deleted before a second one.
- Teardown at 04:05:25–04:05:31 UTC: the release deleted, the ruleset's enforcement set to `disabled` for two
  seconds to allow the tag deletion and set back to `active`, the tag and the branch deleted, the worktree removed;
  proof read back afterwards — ruleset active, tag 404, release not found, branch 404, master clean at `7bc6f61`.

**What it proves for 0.1.0:** `release.yml` runs end to end on the third-party runner — goreleaser-action, the `gh`
CLI in the Publish step and the `contents: write` token path — and the assets it publishes are the bytes this
machine committed. **What it does not:** the `--latest` arm of the Publish step, which only a tag without a
pre-release suffix takes, and the real-network first use.

## 10. For the owner

Everything below the tag is prepared. Three preconditions are already done and only need a glance; five steps are
yours. Nothing in this lane ran `make release`, tagged, pushed a tag, or changed a repository setting.

**Already done — verify, do not redo.**

- **The repository is public.** `gh api repos/appshapes/brigade --jq .visibility` → `public` (2026-09-06 04:16:50Z).
- **The tag ruleset exists and is on.** `gh api /repos/appshapes/brigade/rulesets` → `protect-release-tags`, id
  **22364575**, target `tag`, `refs/tags/v*`, enforcement `active`, no bypass actors. It blocks updates and
  deletions, not creation, so `make release` can push `v0.1.0` with it on — the Blacksmith rehearsal did exactly
  that (section 9). It also means a never-published tag can only be deleted by disabling the ruleset for those few
  seconds, the way the rehearsal's teardown did.
- **Fork pull requests need approval.** `gh api /repos/appshapes/brigade/actions/permissions/fork-pr-contributor-approval`
  → `{"approval_policy":"all_external_contributors"}`. CI runs on self-hosted Blacksmith VMs, so this one matters.

**Your five steps.**

1. **Check the date on the CHANGELOG entry.** `CHANGELOG.md`'s heading reads `## [0.1.0] — 2026-09-06`. If the tag
   is pushed on another day, change that date first — it is one line, it goes in the preparation commit, and the
   release body is copied from this section (step 4).

2. **Run the release from a clean `master`**, after the preparation commit is in and its CI is green
   (`gh run list --workflow=ci.yml --commit <the full 40-character sha>` — a short sha matches nothing):

   ```sh
   make release version=0.1.0
   ```

   What it does: pins `plugin/bin/VERSION` and the plugin manifest to `0.1.0`; builds the four binaries with the
   release flags; has goreleaser build them again and refuses to go on unless the two `checksums.txt` are
   byte-identical; copies that file to `plugin/bin/checksums.txt`; commits and pushes `15: Release 0.1.0`; then
   tags `v0.1.0` and pushes the tag. It refuses a dirty tree, including untracked files.

3. **Watch the workflow to `success`.**

   ```sh
   gh run list --workflow=release.yml --limit 3
   gh run watch <run-id> --exit-status
   ```

   Expect six steps and a published, non-draft, non-prerelease `v0.1.0` with five assets. On Blacksmith the
   rehearsal took **37 s**. If it fails, the draft is discarded and nothing is published, but **the tag is
   pushed**: while nothing was published, `git tag -d v0.1.0 && git push --delete origin v0.1.0` is the recovery —
   it needs the ruleset's enforcement set to `disabled` for those seconds and back to `active` afterwards. A
   **published** tag is never moved; a mistake costs a `v0.1.1`. If the fix is to the workflow alone, re-running
   `make release` dies at step 4 with "nothing to commit" — do step 5 by hand, as `scripts/release-prep.sh`'s
   header now explains.

4. **Replace the release body.** goreleaser generates one line per commit in the whole history — 122 of them,
   measured in phase A — because there is no earlier tag. After the workflow publishes:

   ```sh
   gh release edit v0.1.0 --notes-file <a file holding the CHANGELOG's 0.1.0 section>
   ```

   This edits the description only. It does not move the tag.

5. **Hand the distribution proof to the next session.** It is the row's acceptance criterion and it needs the
   published tag: a fresh `CLAUDE_CONFIG_DIR` with one `claude auth login`, `claude plugin marketplace add
   appshapes/brigade`, `claude plugin install brigade@brigade`, then a real session that shows the registration
   context line after a first-use download verified against the committed checksums — and the published state read
   back (`gh release view v0.1.0`, five assets, the published `checksums.txt` byte-equal to the committed one), the
   real-network cold-cache timing, and `make checksums-check` green by its published-release arm on the commit
   after the release. Section 11 is the list.

---

## Findings the release owner should see

**Where each of these stands at phase B (2026-09-06).** Findings 1 and 4 are **fixed**: P5-18 (`1c849bd`) made the
session-start worker the only downloader, gave the prompt and session-end hooks an immediate return on a cold
cache, and reports a failed install once — measured there at 0 killed hooks in 19 sessions and one download per
session. Finding 3 is **settled**: `CLAUDE.md` and `scripts/ci/README.md` are re-pinned to the measured shellcheck
0.9.0, and this lane ran both versions over the one shell file it edited. Finding 2 (`make test` red twice under
load in `internal/harness/watch`) **did not reproduce**: `make typecheck lint build test vuln deps-check
schema-check tidy-check plugin-check checksums-check`, `go test ./internal/protocol/... ./scripts/ci/...` and
`sh scripts/ci/no-secrets.sh` all exited 0 on `7bc6f61` in this worktree, `make test` included. It stays a flake
note to watch on the release commit, not a cleared finding.


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

## 11. What is still after the tag

Phase B stopped one command short, by the owner's ruling. These are the things that need the published `v0.1.0`
and nothing else, in the order the next session should take them:

1. **The row's acceptance criterion, end to end.** A fresh `CLAUDE_CONFIG_DIR` with its one `claude auth login`, the
   marketplace install, and a real session that shows the **registration** context line — the one pinned in
   `internal/harness/hook/start_test.go`, not the "installing in the background" line — after a first-use download
   verified against the committed checksums. Assert the cache file is absent before the run and present, `0755`,
   with the right sha256 after it; assert the dev pointer is absent (this machine has one).
2. **The published state.** `gh release view v0.1.0` (not draft, not prerelease, five assets), every asset's size
   and sha256, and the published `checksums.txt` byte-equal to `plugin/bin/checksums.txt`.
3. **The real-network first-use timing**, three cold runs, with the break-evens recomputed for the published
   asset's real size (section 7 gives them for 8,361,218 B: 418.1 / 185.8 / 139.4 kB/s). Everything measured so
   far — phase A's arms and P5-18's — came from a local server, never from a network.
4. **`make checksums-check` by both arms of rule (c):** the fresh-build arm on the release commit, and the
   published-release arm on the commit after it. The second is what every later commit depends on.
5. **`go install github.com/appshapes/brigade/cmd/brigade@v0.1.0`**, to turn section 7's last row from a mechanism
   into a measurement — and `claude plugin uninstall brigade` and `claude plugin marketplace remove brigade`, to
   prove the round trip and to see what the cache keeps by design.
6. **The `--latest` arm of the Publish step**, which no rehearsal has exercised: `v0.1.0` carries no pre-release
   suffix, so it takes the branch neither D1's `v0.0.1-rc1` nor the Blacksmith rehearsal's `v0.0.1-rc2` took.

Two things phase B could not measure and that no tag will settle. That **one** `claude auth login` makes a fresh
configuration directory usable is still inferred, not measured: it needs a browser (6.4). And a machine with no
GitHub account at all was not simulated — the SSH-to-HTTPS fallback was measured by forcing SSH to fail on a
machine whose git configuration was otherwise untouched (6.3).

### Measured after the tag (2026-09-06, the distribution proof; driver `15-implement-brigade-090523`, Fable author + verifier)

Steps 2–4 of section 10 ran at 07:07–07:10 EDT at Rjae's word: `make release version=0.1.0` from the clean main checkout at `9787b35`
(release commit `2fb158b`: exactly `plugin/bin/VERSION`, `plugin/.claude-plugin/plugin.json`, the four-line `plugin/bin/checksums.txt`),
the annotated tag `v0.1.0` pushed with the ruleset active, `release.yml` run 34029404604 green in 43 s (guard, goreleaser, verify,
Publish success, the draft discard skipped — the `--latest` arm's first run; `releases/latest` answers `v0.1.0`), published
2026-09-06T11:09:50Z, not draft, not prerelease, five assets, the body replaced from the CHANGELOG's 0.1.0 section (15,197 bytes).

Then the six items above, measured (evidence under `.ignored/tools/p5-10c/proof/`; every number re-derived by the verifier):

1. **The acceptance criterion holds.** A fresh `CLAUDE_CONFIG_DIR` with one `claude auth login` (Rjae's, in a terminal);
   `claude plugin marketplace add appshapes/brigade` and `claude plugin install brigade@brigade` exit 0 with no prompt (SSH clone;
   plugin root `<config>/plugins/cache/brigade/brigade/0.1.0`, its `bin/brigade` byte-identical to the repository's); the first-use
   download of `brigade_0.1.0_darwin_arm64` (8,361,218 B in 0.823 s, 10.2 MB/s, a foreground download by the terminal `profile init`
   the setup document has the user run first) verified against the committed checksums and cached `0755` under
   `~/.local/share/brigade/bin` (absent before; sha256 = the committed line; the dev pointer absent as the proof saw it); then a real
   `claude -p` session on the hosted project whose SessionStart hook printed the registration line of `start_test.go:37`'s shape
   (hook 282 ms), not the "installing in the background" line — six such sessions in all (author 5, verifier 1).
2. **The published state**: five assets — 9,046,336 / 8,361,218 / 8,859,808 / 8,192,160 / 370 B — each sha256 equal to GitHub's digest
   and to the committed `checksums.txt` line; the published `checksums.txt` byte-identical (`cmp`) to `plugin/bin/checksums.txt` at
   `2fb158b`; `shasum -a 256 -c` OK 4/4.
3. **Real-network first-use timing**, cold cache, n = 3 (only the cached binary removed between runs): download 0.957 / 1.099 / 0.753 s
   (mean 0.936 s; 7.6–11.1 MB/s from GitHub's CDN), the cache landed 0.95–1.28 s after launch, SessionStart returned the background
   line in 19–20 ms, the prompt sent at launch went without Brigade and the second prompt's hook (509–580 ms) registered the session —
   exactly one degraded prompt per cold run, 3/3, no `not installed`, no killed hook (P5-18's fail-fast); warm cache: hook 245–282 ms,
   0 degraded. Break-evens for the published asset unchanged from section 7 (418.1 / 185.8 / 139.4 kB/s — it is those bytes).
4. **`make checksums-check`, the fresh-build arm** on `2fb158b`: exit 0, rules (a)(b)(c), twice (author, verifier), only the
   gitignored `dist-cross/` written. **The published-release arm of rule (c) is still unexercised in CI**: on the commit that records
   this section (`722f13f`, run 34031171517) rule (c) passed by its fresh-build arm again — the job log reads "(c) a fresh build of
   this source reproduces plugin/bin/checksums.txt" — because no Go source changed after the release commit. The download fallback
   (`gh release download v0.1.0`) engages on the first post-release commit that changes Go source; a docs-only commit cannot
   exercise it. (Corrected 2026-09-06 after the peer driver read the job log; the first version of this item claimed the arm proven.)
5. **`go install github.com/appshapes/brigade/cmd/brigade@v0.1.0`** with the default `GOPROXY`: exit 0 in 18 s (the proxy already had
   the tag; `sum.golang.org` both lines); `brigade version` prints exactly `v0.1.0`. The documented uninstall order (`team leave`,
   `profile reset`, `claude plugin uninstall brigade`, `claude plugin marketplace remove brigade`) ran clean twice; kept by design:
   the Brigade cache, and — Claude Code 2.1.263's own behaviour — the plugin copy under `plugins/cache/…/0.1.0/` with an
   `.orphaned_at` marker, and a `settings.json` of two empty maps.
6. **The `--latest` arm of Publish** ran for the first time and succeeded; `releases/latest` = `v0.1.0`.

**What did not match the documents** (nine, each confirmed by the verifier; Status row P5-19 in the execution log): the first use is a
foreground download on the documented terminal path, not "in the background while you work"; "the session starts with a line like
this one" holds only on a warm cache; "available on your next prompt" was the second prompt for a script that prompts at launch;
the install prints an undocumented `9 userConfig options not yet set` line; `team create --name/--label` are documented only in the
README's developer paragraph; the `whoami` example says `adapter supabase` where the binary prints `adapter brigade-adapter-supabase`;
the administrator is sent to `whoami` in a session before one exists; `claude plugin marketplace remove` is undocumented and
`plugin uninstall` keeps the plugin copy; the publishable key's value is in no document (by the documented model the administrator
sends it — say so).

**Still unmeasured after the tag**: the HTTPS fallback and a machine with no GitHub account; a pty session's cold-cache first prompt;
the Linux and Intel assets (hashed, not run); the proxy's "not seen yet" retry arm; a slow link (7.6–11.1 MB/s here; section 3
covers 250 kB/s from a local server); the login itself (measured by use only). Two empty throwaway teams (`p510c`, `p510cv`) remain
on the hosted project.

## 12. Why 0.1.0 has no Homebrew tap, and the four conditions for adding one (brief §7.1, transcribed)

No tap for 0.1.0, for four reasons in descending weight. A tap is new infrastructure the release model does not have —
a `homebrew-tap` repository, a token with write access to it as a release secret, and a publishing step in
`release.yml`. A brew-installed `brigade` lands on the PATH position that shadows the plugin's pinned bootstrap: E0-8 (e)
measured that with another `brigade` earlier on `PATH` the session-start hook printed its shadowing warning **and the
Bash tool ran the other binary**. The distribution that ships is already the documented one — the plugin bootstrap for
sessions and, for a terminal, the symlink to the plugin's own binary, which keeps a person on the pinned version
upgrade for upgrade. And it is unrehearsed: adding it now adds a release step nobody has run to the one workflow that
must not fail. That is row **P5-16**, after 0.1.0.

**Add a tap only when all four hold**, so the decision reopens on evidence: (i) the repository is public and has at least
two published releases; (ii) there is a real terminal-only user — someone who wants `brigade` without the Claude Code
plugin; (iii) the shadowing warning is *measured* to fire for a brew-installed binary, and `docs/setup.md` carries the
resulting instruction; (iv) the tap repository, its release token and the `homebrew_casks` block are rehearsed on a
throwaway version the way D1 and the Blacksmith rehearsal (section 9) rehearsed the release.

## 13. What becomes a compatibility surface on the day the tag is pushed (brief §10, transcribed)

Once `v0.1.0` exists, the plugin's pinned version and the published checksums make the release workflow a
compatibility surface, and a convention change becomes a migration. What hardens, specifically:

1. **`plugin/bin/brigade` is the most frozen file in the repository.** A user who installs 0.1.0 runs 0.1.0's copy of
   the bootstrap until they update the plugin; a bug in it is fixed only by a plugin update, never by a new binary
   release. That is why section 3's measurement preceded the tag and why P5-18 landed before it.
2. **The release URL form and the asset names are frozen for the installed base**:
   `https://github.com/appshapes/brigade/releases/download/v<version>/brigade_<version>_<os>_<arch>`, verified against
   a two-space `<sha256>  <asset>` checksum file. `.goreleaser.yaml`'s `name_template` and `formats: [binary]` are now
   contract, not configuration.
3. **`checksums-check.sh` rule (c) changes character for every developer.** At `0.0.0` it short-circuits; from 0.1.0
   on, a commit that changes Go source without bumping the pin makes the fresh build differ and the check falls back
   to `gh release download v0.1.0 -p checksums.txt`, which needs `gh` and the network. CI has `GH_TOKEN`; a developer
   without `gh` gets a red `make checksums-check` that says so. This is the new daily cost.
4. **`go.mod`'s `go` line is a release-reproducibility pin.** The release job rebuilds from the tag and diffs against
   the committed checksums; a toolchain bump changes the bytes, so bumping Go means bumping the version too, or
   accepting a red `fast` job until the next release. Stated in `CLAUDE.md`'s Brigade block as of this commit.
5. **`plugin.json`'s `userConfig` keys, their types and their defaults** are stored in users' settings by the install
   flow. Renaming any of the nine options, or changing a default, is a migration for anyone on 0.1.0.
6. **Text that other things pin**: the session-start context line (P5-13, pinned in `start_test.go`, `e2e_test.go` and
   the hook txtar) and the frame's instruction paragraph at each level (P5-12).
7. **On-disk shapes**: `state/by-pid/<pid>.json`, `state/seen/<id>.json` (P5-14), the profile directory layout, and
   `adapters.json` (D36). A 0.1.0 user who upgrades brings these files along.
8. **Protocol v1** was already frozen (`docs/protocol-v1.md`); 0.1.0 is when that freeze acquires an installed base.
9. **The keep-alive's public-repository clause is active**: GitHub disables a scheduled workflow after 60 days without
   repository activity in a public repository. `docs/setup.md` says so for a public repository as of this commit, and
   the administrator has that standing responsibility.

---

## 0.2.0 release (P7-9)

Date: 2026-09-07 · Host: macOS 26.6.1, Darwin 25.6.0, arm64 · Go go1.27.0 (`go.mod` go line `1.27.0`,
**unchanged** since 0.1.0 — no toolchain bump, so the release rebuilds reproducibly) · goreleaser v2.18.0.

0.2.0 is "the project owns the team" (P7-1..P7-8): the committed `.brigade.json`, one-command `team create`,
parameterless `team join`, the attach-only hook, and the deletion of the user-facing profile concept. Breaking, no
migration (zero users), no protocol change.

**The release sequence, run end to end (Rjae's ruling 2026-09-07).**

1. Release-prep commit `3cf0bab`: the `## [0.2.0]` changelog section (breaking changes lead) and the version
   strings that move with the release (`go install …@v0.2.0`, the "current release" prose). Version pins
   (`VERSION`, `plugin.json`) left for `make release` to bump.
2. `DRY_RUN=1 make release version=0.2.0` — steps 1–3 only: bumped the pins, `make cross` built the four targets,
   and **goreleaser reproduced `dist-cross/checksums.txt` byte for byte** (the drift guard between
   `.goreleaser.yaml` and the Makefile). The dry-run pin changes were reset to a clean tree.
3. `make release version=0.2.0` — the real run: pinned `VERSION`/`plugin.json` to 0.2.0, rebuilt, cross-checked
   with goreleaser again, committed the checksums as `ccc6a12` ("15: Release 0.2.0") through the full push gate,
   tagged `v0.2.0` and pushed the tag.
4. Release workflow **run 34109358721, success**: rebuilt the four binaries from the tag, verified them against
   the committed `plugin/bin/checksums.txt`, and published the release `v0.2.0` (not a draft).

**Acceptance criteria — all met.**

| Criterion | Result |
| --- | --- |
| `make deps-check` green, `docs/allowed-deps.txt` zero-diff | ✅ the new packages (`teamfile`, `teamstore`, `teamstore/write`) are stdlib + internal only |
| `go.mod` `go` line unchanged since 0.1.0 | ✅ `git diff 3cf0bab..ccc6a12 -- go.mod` empty |
| `make plugin-check`: VERSION == plugin.json == 0.2.0 | ✅ |
| `checksums-check` rule (c) on the release-record commit passes **by its fresh-build arm** (the `5ae1d18` precedent) | ✅ "(c) a fresh build of this source reproduces plugin/bin/checksums.txt" — never the published-release fallback |
| The tag's release run reproduces the committed checksums and publishes | ✅ run 34109358721, five assets |
| Published `checksums.txt` == committed `plugin/bin/checksums.txt` | ✅ byte-identical |
| Every published binary's sha256 matches `checksums.txt` (the plugin's own download check) | ✅ all four `OK` under `shasum -a 256 -c` |
| `go install github.com/appshapes/brigade/cmd/brigade@v0.2.0` builds and reports `v0.2.0` | ✅ (the module-derived leading `v`; the released binary reports `0.2.0`) |
| The create→commit→join→attach→send→inject→reply loop on the released model | ✅ proven GREEN by `make harness-smoke` (P7-6b) against a build byte-identical to the release cross-compile; a real `claude -p` session attached through `.brigade.json`, the pin and the binding and completed the loop |

**Published assets** (release `v0.2.0`): `brigade_0.2.0_{darwin_arm64,darwin_amd64,linux_amd64,linux_arm64}` and
`checksums.txt` — five, macOS and Linux only (Windows means WSL 2), ~8 MB each.

**One arm owner-gated, not run here.** A fresh-configuration-dir **marketplace** install of the *published* plugin
driving a real session against a *hosted* Supabase project — the E5-release-style end-to-end distribution proof —
needs real Claude Code, a network and a hosted project, and stays the owner's to run at the keyboard, as for 0.1.0.
Its every component is covered above: the download-and-verify path (the checksum match), the binary itself (`go
install` + the sha256 checks), and the session loop (harness-smoke on the byte-identical build).

## 0.3.0 release (P7-12)

Date: 2026-09-08 · Host: macOS 26.6.1, Darwin 25.6.0, arm64 · Go go1.27.0 (`go.mod` go line `1.27.0`,
**unchanged** since 0.1.0 — no toolchain bump, so the release rebuilds reproducibly) · goreleaser v2.18.0.

0.3.0 is "joining from inside a session" (P7-11, P7-11b): `team create`, `team join` and `team rotate-secret` run
inside a Claude Code session, `team join --secret-file <path>` reads the secret from the file `team create` wrote
(location checked, mode and owner not), SessionStart's start facts carry the store to a not-yet-attached session,
and the `..` bypass of the outside-the-repository check is closed. Additive, no protocol change.

**The release sequence, run end to end (Rjae's ruling 2026-09-08, "go ahead and release a new version").**

1. Release-prep commit `3177dae`: the `## [0.3.0]` changelog section (verified by a one-lens adversarial pass over
   every sentence against the two feature commits; its seven wording findings folded in) and the version strings
   that move (`go install …@v0.3.0`, the `v0.3.0`/`0.3.0` sentence, the whoami/symlink examples, plugin/README's
   current-release line, adapter-authors'). Version pins left for `make release` to bump.
2. `DRY_RUN=1 scripts/release-prep.sh 0.3.0` — steps 1–3 only: bumped the pins, `make cross` built the four
   targets, and **goreleaser reproduced `dist-cross/checksums.txt` byte for byte**. The dry-run pin changes were
   reset to a clean tree.
3. `make release version=0.3.0` — the real run: pinned `VERSION`/`plugin.json` to 0.3.0, rebuilt, cross-checked
   with goreleaser again, committed the checksums as `a337a43` ("15: Release 0.3.0") through the full push gate,
   tagged `v0.3.0` and pushed the tag.
4. Release workflow **run 34247726834, success**: rebuilt the four binaries from the tag, verified them against
   the committed `plugin/bin/checksums.txt`, and published the release `v0.3.0` (not a draft).

**Acceptance criteria — all met.**

| Criterion | Result |
| --- | --- |
| `make deps-check` green, `docs/allowed-deps.txt` zero-diff | ✅ `git diff ccc6a12..a337a43 -- docs/allowed-deps.txt` empty (the new packages are stdlib + internal only) |
| `go.mod` `go` line unchanged since 0.1.0 | ✅ `git diff 3177dae..a337a43 -- go.mod` empty |
| `make plugin-check`: VERSION == plugin.json == 0.3.0 | ✅ |
| `checksums-check` rule (c) on the release commit passes **by its fresh-build arm** | ✅ CI run 34247724877 (`fast` job) on `a337a43` — "(c) a fresh build of this source reproduces plugin/bin/checksums.txt" — never the published-release fallback; `fast`, `macos`, `supabase` and `reproducibility` all green |
| The tag's release run reproduces the committed checksums and publishes | ✅ run 34247726834, five assets |
| Published `checksums.txt` == committed `plugin/bin/checksums.txt` | ✅ byte-identical (`diff` empty) |
| Every published binary's sha256 matches `checksums.txt` (the plugin's own download check) | ✅ all four `OK` under `shasum -a 256 -c` |
| `go install github.com/appshapes/brigade/cmd/brigade@v0.3.0` builds and reports `v0.3.0` | ✅ (`GOPROXY=direct` into a scratch `GOBIN`; `brigade version` → `v0.3.0`) |
| The in-session join on the released model | ✅ `cmd/brigade/testdata/script/team.txtar` drives the real binary: carol creates and dave joins (`--secret-file` on a plain copy of the file) inside a session, into the stores the start facts name, no secret on either stream; `make test` green on `a337a43` |

**Published assets** (release `v0.3.0`): `brigade_0.3.0_{darwin_arm64,darwin_amd64,linux_amd64,linux_arm64}` and
`checksums.txt` — five, macOS and Linux only (Windows means WSL 2), ~8–9 MB each.

**Not re-run here.** `make harness-smoke` (the send→inject→reply loop) was not re-run for 0.3.0: nothing on the
messaging path or the wire changed. The owner-gated arm stands as for 0.2.0 — a fresh-configuration-dir marketplace
install of the published plugin against a hosted project — and now has one more thing to measure at the keyboard:
`!brigade team join --secret-file <path>` typed with the `!` prefix (the tree measured the Bash tool, not `!`;
brief ruling 3).

## 0.4.0 release (P7-14)

Date: 2026-09-08 · Host: macOS 26.6.1, Darwin 25.6.0, arm64 · Go go1.27.0 (`go.mod` go line `1.27.0`,
**unchanged** since 0.1.0) · goreleaser v2.18.0.

0.4.0 is "two skills, one install path" (P7-13): `/brigade:join <path>` and `/brigade:update`, the `/plugin`
commands as the only documented install form, the six `make` install/update targets gone, the plugin tree opened
(CI checks invariants only), and this repository's `.claude/settings.json` enabling `brigade@brigade` for every
collaborator with the `brigade` marketplace named beside it. No Go source under internal/ or cmd/ changed since
0.3.0, so the binaries are byte-identical to 0.3.0's apart from the version stamp; no protocol change.

**The release sequence, run end to end (Rjae's ruling 2026-09-08, "Yes, release 0.4.0").**

1. Release-prep commit `63318f8`: the `## [0.4.0]` changelog section (a one-lens adversarial pass over every
   sentence; its eight wording findings folded in, one of which — "two skills" in two READMEs — was a stale
   count rather than changelog text), the version strings that move, and the `extraKnownMarketplaces` entry in
   `.claude/settings.json` (the project-scope install had written only `enabledPlugins`, because the marketplace
   was already known on this machine — a collaborator's would not be). Version pins left for `make release`.
2. `DRY_RUN=1 scripts/release-prep.sh 0.4.0` — steps 1–3 only: bumped the pins, `make cross` built the four
   targets, and **goreleaser reproduced `dist-cross/checksums.txt` byte for byte**. Pins reset to a clean tree.
3. `make release version=0.4.0` — the real run: pinned `VERSION`/`plugin.json` to 0.4.0, rebuilt, cross-checked
   with goreleaser again, committed the checksums as `6f9fb27` ("15: Release 0.4.0") through the full push gate,
   tagged `v0.4.0` and pushed the tag.
4. Release workflow **run 34258274984, success**: rebuilt the four binaries from the tag, verified them
   against the committed `plugin/bin/checksums.txt`, and published the release `v0.4.0` (not a draft).

**Acceptance criteria — all met.**

| Criterion | Result |
| --- | --- |
| `make deps-check` green, `docs/allowed-deps.txt` zero-diff | ✅ `git diff a337a43..6f9fb27 -- docs/allowed-deps.txt` empty (no Go source changed) |
| `go.mod` `go` line unchanged since 0.1.0 | ✅ `git diff a337a43..6f9fb27 -- go.mod` empty |
| `make plugin-check`: VERSION == plugin.json == 0.4.0 | ✅ |
| `checksums-check` rule (c) on the release commit passes **by its fresh-build arm** | ✅ CI run 34258272271 (`fast` job) on `6f9fb27` — "(c) a fresh build of this source reproduces plugin/bin/checksums.txt" — never the published-release fallback; `fast`, `macos`, `supabase` and `reproducibility` all green |
| The tag's release run reproduces the committed checksums and publishes | ✅ run 34258274984, five assets |
| Published `checksums.txt` == committed `plugin/bin/checksums.txt` | ✅ byte-identical (`diff` empty) |
| Every published binary's sha256 matches `checksums.txt` (the plugin's own download check) | ✅ all four `OK` under `shasum -a 256 -c` |
| `go install github.com/appshapes/brigade/cmd/brigade@v0.4.0` builds and reports `v0.4.0` | ✅ (`GOPROXY=direct` into a scratch `GOBIN`; `brigade version` → `v0.4.0`) |
| The two skills reach a session | ✅ `plugin/skills/{join,update}/SKILL.md` ship in the tagged tree; `make plugin-check` admits them; the manifest gate reads their frontmatter (`disable-model-invocation`, `allowed-tools`) as recognised keys |

**Published assets** (release `v0.4.0`): `brigade_0.4.0_{darwin_arm64,darwin_amd64,linux_amd64,linux_arm64}` and
`checksums.txt` — five, macOS and Linux only (Windows means WSL 2), ~8–9 MB each.

**Measured in this session, before the tag.** `/plugin marketplace add appshapes/brigade` and
`/plugin install brigade@brigade` typed at the prompt of a session running on `CLAUDE_CONFIG_DIR=~/.claude-ifthen`
installed into that directory ("Plugin is now active."), where the earlier terminal `make brigade-install` had
installed into `~/.claude` — the reason the CLI form left the docs. The session's next prompt carried the
registration line (team `brigade`), the plugin having downloaded the 0.3.0 binary in the background.

**Not re-run here.** `make harness-smoke`: nothing on the messaging path or the wire changed. Owner-gated as
before: a fresh-configuration-dir marketplace install of the *published* plugin against the hosted project — now
with `/brigade:join <path>` typed by a real member — and a collaborator opening this repository cold, to see
Claude Code add the marketplace from `.claude/settings.json` and print the one install command that remains.
