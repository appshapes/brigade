# Grok participation contract investigation — card 120

2026-10-18. Brigade base: `70111d68bde3532dbf7d5ac98345864a6cff0fc3`.
Installed Grok: `1.0.62 (1474f9cbdd91) [stable]`.
Read with the [measured probes](grok-participation.md),
[implementation plan](../../.context/plans/grok-build-participation.md) and
[harness package boundaries](../developer/change/harness-packages.md).

## Recommendation for implementation

Implement a reversible **native monitor route for explicitly nonleader Grok
TUIs**, using body-free monitor wake notices plus bounded local sink retrieval.
Keep the existing sink and protocol-v1 local-injection ACK.
Bootstrap once through the measured Stop block/reason continuation; retain an
explicit skill retry. Leader-backed delivery remains unsupported. The coordinator
selected explicit `--no-leader` scope as a reasonable implementation choice; the
unanswered preference question is not user approval or an indefinite gate. The
35-minute native hold now has a supplemental PASS from correlated events/export
and scroll-only original-TUI witness; retain the raw viewport FAIL. Actual package
technical gates remain mandatory before merge-ready acceptance.

Stop-assisted arming, short idle and native tool-busy wake have passed coordinator-owned
synthetic pilots under normal plugin trust and command-specific permission.
They establish bounded feasibility only. Native explicit-rearm resume and
compaction also passed; native crash cleanup failed. Long-idle supplemental
evidence corrected a screen-only false-negative. None is
actual production package acceptance or unrestricted launch-mode support. No durable native receipt or
exactly-once model processing is claimed or required by this contract.

## Evidence and provenance

Coordinator records are `hook-findings.md`, `monitor-summary.json`,
`direct-report.json`, `rejected-report.json` and `liveness-normal-report.json`
are retained private coordinator evidence. This report uses only their owned
synthetic evidence; the [allowlisted checkpoint](grok-participation-evidence/coordinator-checkpoint.json)
exports assertions without raw paths, native IDs, transcripts or account metadata.
The coordinator supplied the automatic-arming result in addition to the earlier
hook findings; the summary corroborates identity and the generated nonce.

| Measured observation | Consequence |
| --- | --- |
| Normally installed/trusted plugin emits SessionStart new/load and SessionEnd | Use native hooks; no `--plugin-dir` auto-trust shortcut |
| SessionStart systemMessage is visible in the TUI; model answered NONE | Startup JSON is a user notice, not model context |
| Fresh nonleader Stop emits block/reason once; model calls `monitor` on `python3 auto_monitor.py`, requests one normal permission; coordinator approves that command only | Stop can bootstrap the monitor after an initial user turn |
| Model replies AUTO_MONITOR_ARMED; an idle external split nonce then produces AUTO_WAKE_120 without terminal input | Separate model-output wake proof passed; an armed banner alone is insufficient |
| Nonleader hook PPID equals known TUI PID; GROK_SESSION_ID is exact; no GROK_LEADER_SOCKET | Direct TUI parent is the measured pointed identity candidate |
| Hook/shell HOME agrees; XDG absent | Absence is not a poisoned-environment test |
| Stop emits once per incarnation, without indefinite continuation | Keep explicit one-attempt and recursion guards |
| Leader hook PPID is shared leader; normal TUI /exit leaves subscribed actor resident and ghost prompt succeeds | Resident session/producer subscription cannot prove TUI ownership |
| Rejected direct prompt returns cancelled / HookDenied with stale usage.modelCalls=1, no user echo or agent text | RPC success, usage and completion alone cannot prove admission |

`monitor-summary.json` confirms `hook_parent_equals_tui`,
`native_identity_matches` and `auto_nonce_visible` true, and
`socket_env_present` false. The earlier manual monitor and STOP_CONTEXT_120
sentinel also passed. Earlier Python startup/prompt hooks timed out at five
seconds; those runs are not evidence that all hooks succeeded. Prefer the Go
hook fast path and bounded native hook timeouts.

Public source is pinned to `2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`, with earlier
reported SOURCE_REV `559751fdcec02d413e4c57c8832ab275e4f44980`; neither matches the
installed binary. Coordinator supplied `source/{protocol,server,acp_agent,session,
message_delivery}.rs`, indexed through graph MCP. Human ACP prompt conversion
sets `admission`, `prompt_admitted` and `persist_ack` to None; the explicit
admission helper belongs to a different internal parent-agent path. Leader
source retains subscribers/transfers drivers and the inspected diagnostics do
not expose a usable per-TUI attachment identity. These explain the direct-path
negatives; they do not establish installed monitor behavior. No further leader
source speculation is required for this implementation.

## Delivery correction: wake notices plus local retrieval

**Direct full-frame monitor output is rejected.** The earlier
`monitor-payload-report.json` reused END120 across sizes; the model could infer
that tail from the 64-byte case. All claimed larger size passes are invalidated
and must not enter release evidence. The fresh unseen-tail 2,048-byte ASCII case
failed tail visibility after 121.95 seconds; the coordinator also reports no
16-KiB escaped-case reply within 120 seconds. Neither proves an exact installed
truncation threshold, but neither supports full-body transport. The independent
12-second native tool-busy pilot passed: in-flight ingress, completion marker and
joined nonce, 18.23 seconds. The completed 35-minute monitor test has a
supplemental native PASS (2.489-second completion), retaining the raw screen FAIL
as a viewport false-negative; see the [native checkpoint](grok-participation.md#completed-native-lifecycle-checkpoint).

Coordinator retrieved `monitor-{types,event,tool,rate_limiter}.rs` at public pin
`2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`. Source declares line/batch truncation
constants 500/3,000, a bucket of 10 with one token per 2,000 ms, 200-ms debounce,
and termination after sustained suppression. `event.rs` tests string byte length
before calling `truncate_str`; do not turn comments saying characters into an
installed UTF-8 byte guarantee. `tool.rs` processes output through those stages
before notification. This pin still differs from binary `1474f9cbdd91`.
The 10,000-character monitor tool-result constant and 10-MiB output-file cap are
not terminal read-response budgets. No verified native terminal output budget
was found in the inspected material.

Sol's implementable correction, retaining the nonleader identity/arming contract:

1. **Separate wake from content.** Preserve Muse's current follower output.
   Grok's follower emits only a fixed body-free notice, strictly under 400 UTF-8
   bytes including newline, at most once per five seconds. Coalesce all appends
   during that interval and emit a trailing notice when changed. No sender/body,
   summary, shell command assembled from message data or raw frame goes into the
   monitor. The skill supplies the trusted installed command path. Restart may
   emit one notice for retained records. Neither emitting nor dropping a notice
   changes a delivery cursor, ACK, retention or message contents.
2. **Read only locally injected records.** Add in-session Grok
   `brigade inbox read [--cursor <opaque-token>]`. Resolve the current exact native
   UUID, actual TUI PID/start identity, Brigade UUID and trusted state directory
   through the existing pointed map. Revalidate before returning data. Refuse
   other hosts/topologies, stale/swapped bindings and caller-supplied session or
   file paths. Never call backend receive, read pending/held bodies, release held
   data or mutate policy. Existing bare `inbox` continues to hide held bodies
   in-session. Only complete valid records from this receiver's own local sink
   stream are eligible; partial/malformed records never become frame fragments.
3. **Preserve rotations and validate cursors.** Read all retained sink generations
   authorized to this receiver, in order, not merely `NewestSink`. Bind a versioned,
   length-capped cursor to that exact receiver incarnation, sink generation,
   record identity and frame byte offset. Treat it as untrusted input, not an
   authorization token: validate UUIDs, integer ranges, generation membership,
   file identity, record boundaries/digest and UTF-8 boundaries against trusted
   state. Reject traversal, symlinks, substituted/truncated files and sibling or
   old-incarnation cursors. Use existing safe file-opening conventions, bounded
   record/index work and explicit stale/corrupt/limit errors; never silently skip
   lost generations. A resumed receiver needs explicit authorized adoption of old
   sink generations before they can be read. Do not delete them merely because a
   watcher rotates or emits a hint.
4. **Page the actual output, losslessly.** The initial proposed CLI budget was
   2,048 UTF-8 bytes. Native escaped/multiline retrieval exposed raw newline
   insertion at shell output column 2,000, corrupting JSON escapes. The revised
   cap is **1,800 UTF-8 bytes for the entire serialized stdout response**, including
   metadata, escaping and newline, with margin below that measured boundary.
   Native validation of the revised cap and tool-wrapper behavior remains pending.
   Return one complete record or a code-produced lossless
   UTF-8 slice with message UUID, frame byte length, SHA-256 of the whole stored
   frame, byte offset, next offset, `frame_complete`, `next_cursor` and explicit
   page-complete marker. Compute/validate the original complete record in code;
   do not ask the model to decode base64, repair escapes, infer missing text or
   reconstruct dropped monitor chunks. Reduce slice length until the full
   encoded envelope fits. Repeated calls with the same cursor return the same
   slice; metadata-only overflow fails explicitly. No trimming, sanitizing again,
   replacing or normalizing the stored original untrusted frame on this path.
5. **Drain from one hint.** The skill reads from its last returned cursor (or the
   start after lost context), then follows each validated continuation until an
   explicit caught-up result. At record end it must continue to the next record,
   not stop because `frame_complete` is true. Recheck the current high-water mark
   before caught-up so coalesced/intermediate hints are unnecessary. Retain a
   valid EOF cursor for later appends; a new hint continues there. Every part is
   explicitly untrusted data; process/reply only after all ordered parts of the
   original frame and its message ID have arrived. Code guarantees byte-exact
   slicing; the model must not guess a missing part. Missing/truncated JSON or
   page marker means retry that page, not advance. Reading is not consuming or
   marking processed. Replay after lost cursor is allowed and deduped by message
   ID. An entirely lost final wake can still delay fetching: bodies remain locally
   available for a later wake or explicit skill retry; do not promise all wakes
   arrive or continuously auto-prompt to hide that limitation.
6. **Keep ACK unchanged.** Permitted sink append/write/Close remains successful
   local injection and permits Pipeline.Done/ACK, as Muse does. Hint/read/model
   success is a separate milestone. Held/refused messages never enter this path;
   read does not ACK anything. Retain accepted sink bodies independently of hint
   emission and retrieval. Document bounded retention/error behavior; no durable
   native receipt, processed state or exactly-once delivery is implied.

Release proof must exercise the **implemented hint -> read -> all pages -> original
TUI** path with fresh independent hidden random sentinels at beginning/middle/end
of each maximum-size 16-KiB body (ASCII, UTF-8, quotes, backslashes, newlines).
Offline tests must concatenate code-produced slices byte-for-byte to the original
frame and verify digest, offsets and full encoded-page budget. Drop/coalesce
intermediate hints, rotate sinks, interrupt/retry a page, tamper cursor/UUID and
hold/refuse a message; all accepted bodies remain retrievable only by their own
current binding and held bodies stay inaccessible. Observe native tool output
truncation indicators/end markers for every page under default permissions.
These end-to-end retrieval tests are pending coordinator runs after Sol implements;
a short monitor nonce or familiar tail cannot substitute.

## ACK parity: reuse is justified, with explicit limits

[Protocol v1 section 4.5](../protocol-v1.md) defines ACK after **local injection**;
`injected` is terminal and `processed` is reserved. A held or refused message is
not acknowledged. Preserve that protocol unchanged.

In [inject.go](../../internal/harness/watch/inject.go), a configured sink makes
`inject` call `appendSink`: marshal one JSON record, open with append/create and
0600 creation mode, write one newline-terminated record, then Close. Only success
flows through `Pipeline.Done(err=nil)` to queued ACK. There is no fsync, follower
receipt or model receipt. Grok can use this same boundary: successful append and
close to its correctly bound private sink means locally injected. Open, write
or close failure must not ACK. Existing hold/refusal policy must prevent append
and ACK. Do not ACK from native task status, prompt usage or monitor output.

This corrects the earlier report's proposed native/model receipt requirement.
That stronger requirement would exceed Muse's contract. Arming denied or failed
means **not receiving model events**, not retroactive failure of a successful
local append. As with Muse, pre-arming backlog may already be locally injected
and ACKed. Show the unarmed state and retry instructions honestly. If the product
chooses hold-until-armed, implement it as an explicit existing-policy decision,
not a different meaning of ACK. A cancelled model turn after injection likewise
does not undo local injection.

[The current follower](../../internal/harness/commands/inbox_follow.go) polls at
250 ms, checks registration every five seconds, reads the newest sink from its
beginning and drains the old open sink before switching. Partial final JSON
lines are retained until newline; completed records over 1 MiB are skipped.
There is no persisted consumer cursor: restarting can replay already ACKed
records. A sink append survives helper restart while that file exists, but is
not a native durable queue or a promise of indefinite local retention. Do not
infer acknowledged-message recovery from server pending storage.

Muse's current record rendering expands into multiline output; keep that behavior
unchanged. Grok must use the wake/read route above instead. Its new follower must
stop on a closed consumer and revalidate the original receiver/incarnation, not
merely accept any valid replacement map. The read endpoint must preserve eligible
older sink generations; a newest-file-only scan can lose unread locally injected
records even though monitor hints are coalesced correctly.

## Implementation guidance

The [plan](../../.context/plans/grok-build-participation.md) now owns the concrete
identity, options, hook, arming, cleanup, skills and packaging instructions.
The contract is accepted as guidance for reversible monitor-first implementation;
the coordinator selected explicit `--no-leader` scope without claiming user
approval. The
[fixture and operator steps](../../scripts/experiments/grok-participation/README.md)
reproduce the bootstrap without automating trust or touching unrelated sessions.

Read-only seam check of Sol's current implementation: `MonitorCommand` supplies
hook-bound GROK_SESSION_ID and incarnation explicitly; `grokStop` keeps an exact
native description-only match unverified, and suppresses repeat automatic arming.
`inboxReadGrok` resolves the current receiver and local sink, binds cursors to its
identity and record, and initially used a proposed 2,048-byte serialized page budget
(now reduced to 1,800 after the measured native wrap failure above). These are code
observations only, not a review verdict or executed production/native tests. The
actual native Stop lacks command evidence, so readiness remains unverified under
that code path. Installed command execution, ancestry checks, complete pages,
ACK behavior and watcher retirement require coordinator acceptance.

## Remaining finite coordinator gates

All numbers here are proposed test budgets, not measured Grok defaults. Freeze
bounds before each run and record local append/ACK, helper output, native event
and generated model output separately. Use only disposable owned sessions and
synthetic frames; no broader permission grants.

| Gate | Finite next experiment and pass criterion |
| --- | --- |
| Payload/latency | Direct full-frame monitor output rejected; implemented wake/read retrieval proof pending. Isolated 64 B, 2 KiB, 8 KiB and actual protocol-maximum body, each with single-line and multiline content. Include UTF-8 and split writes. Require complete ID/body/end marker and generated split-nonce reply; candidate wake/content budgets 30/120 seconds. Measure encoded record/frame expansion; do not invent a new wire cap. |
| Burst/drop/batching | Count hints separately from retrieved records; drop/coalesce hints deliberately. Send 1, 10 and 50 numbered frames at 1 frame/s, then a bounded 10-frame burst. Count all four boundaries and native task survival. Measure batching delay and missing/partial frames; repeat with proposed pacing. Freeze a supported envelope only after zero unexplained losses within it; installed monitor rate/retention limits remain unverified. |
| Long idle | Native supplemental PASS: 2,100-second quiet hold, exact own-session assistant export, correlated 2.489-second completion and scroll-only original-TUI witness. Raw 128.13-second screen FAIL retained as viewport false-negative. Actual package long-idle acceptance still required; leader whole run failed replay scoring. |
| Resume and task guards | New/load, same UUID/new TUI incarnation, compaction, helper failure and explicit retry. Exactly one bound live monitor; one automatic attempt per incarnation. Deny the initial command permission: no ready state or repeated automatic continuation; later explicit retry works. Native explicit-rearm resume and compaction passed only. Actual Stop lacks command; verify binding without claiming description-only evidence is ready. |
| Ownership/cleanup | Native /exit child exit 0.34 seconds; SIGKILL child survived 10.59 seconds (owned orphan cleaned). Production normal /exit and owned TUI crash, same-cwd sibling, stale pointer/PID and receiver replacement. Candidate local retirement bound: 10 seconds (two current map-check intervals), measured from TUI death. No post-retirement append/ACK by stale watcher, surviving ready binding or model wake. Distinguish an already accepted local append from a new stale-target injection. |
| Local ACK regressions | Synthetic sink write/close failure, hold/refusal, pre-arm backlog, watcher/helper restart, output closure and partial record. Only successful permitted local injection ACKs; replay is tolerated. Check native loss separately, without claiming ACK is model receipt. |
| Trusted environment | Native scratch HOME/existing GROK_HOME preflight passed without auth reads/copies; actual package hook isolation still pending. Normal installed/trusted plugin, default permissions, native UUID/canonical cwd/actual TUI parent, isolated synthetic HOME/path tests and poisoned XDG/BRIGADE inputs. No credential access or global configuration changes. |

## Scope choice and validation status

Monitor-first can offer Stop-assisted setup after the first eligible user turn
with explicit skill fallback, subject to the gates above. An explicit-arm-only
variant reduces bootstrap scope while keeping the same sink/monitor contract.
Neither promises launch-time model context or leader support. If the user
requires shared-leader participation, that remains blocked on independently
proven TUI ownership; the resident direct transport is not a substitute.

Installed `20-background-tasks.md:90–140` documents persistent monitor lifetime
and merged stdout/stderr; binary tool-help strings document batching, dropping
and noise termination. Coordinator subsequently retrieved pinned monitor source successfully; its
constants are recorded above. Installed-runtime rate/batch/retention and native
terminal-output limits remain unverified. Earlier worker fetch failures do not
mean the source is still unavailable. Guide/source claims and coordinator runtime
measurements remain distinct.

The evidence checkpoint updates the experiment, plan, research execution-log row
and isolated experimental scripts; production files are unchanged. No live Grok
sessions, instruction files, host trust/configuration, credentials, real
transcripts, Git operations or team messages were touched by this worker.
Task-start lessons had no matching evidence-packaging guidance; the test-count
follow-up reinforced checking each mutation's discriminating value. Coordinator
owns shared external state and any subsequent lesson capture.

Validation: sixteen offline tests cover passive interactions, replay/correlation,
30 invalid field mutations, 11 missing-check cases, private-data exclusion and
fixture scope/one-shot/lifecycle guards, plus rejection of invalidated payload
passes, completed long-idle status/duration/required checks and lifecycle/environment
export boundaries, plus every supplemental witness and timing guard. No production pagination or end-to-end retrieval test is claimed. Python
syntax, supplied synthetic-summary
assertions, local relative links/anchors, whitespace and `git diff --check` are
checked separately. The final `make docs-check` passed (169 pages, 665 links); the earlier protected
Go-cache denial remains historical, with no permission/cache workaround used. No
production/live tests were run by this worker. Coordinator may import the scoped
checkpoint; actual package gates still block acceptance.
