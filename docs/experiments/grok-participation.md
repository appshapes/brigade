# Grok Build participation feasibility — card 120

Date: 2026-10-18. [Card 120](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
Brigade base: `a7abf85136f61835a8c3db00c76fb68347317e3f` (0.34.1).
Installed Grok: `1.0.62 (1474f9cbdd91) [stable]`, macOS arm64.

## Verdict

**Runtime and installed-package native acceptance are complete within the
measured macOS arm64, explicit `--no-leader` scope at `d9279cd`.** Independent
Rjae Fable review, PR and release remain pending; this is not a released or
unrestricted compatibility claim. See the [production checkpoint](#installed-package-acceptance-checkpoint)
and its [sanitized evidence](grok-participation-evidence/production-checkpoint.json).

The coordinator used normal plugin trust, default permissions and exact command
approvals in an owned isolated fs team with scratch HOME and SHA256-pinned copied
development binaries. Page reads and synthetic replies were approved explicitly;
this does not establish unattended permission bypass. Shared-leader delivery is
unsupported. Linux native behavior and the full package's 35-minute hold remain
unmeasured. The prior native-only 35-minute supplemental PASS remains separate,
with its original viewport FAIL preserved.

Earlier leader results below remain bounded **transport pilots**. A later normal
TUI exit left the subscribed native actor resident and direct ghost prompts still
succeeded. A rejected prompt returned cancelled/HookDenied with stale previous
modelCalls=1. Thus resident lookup, RPC completion and usage cannot establish
live TUI ownership or native admission. These negatives supersede the earlier
leader-first recommendation without erasing its successful original-TUI evidence.

Both TUIs used explicit `--leader` in the historical transport experiment. Merely selecting
`--leader-socket` yielded successful ACP model replies but **no reply in either
original TUI**. This negative control prevents mistaking a restored copy of saved
history for delivery into a live conversation. On macOS, canonical cwd was also
required: `/tmp/...` attachment failed with `FS_NOT_FOUND` while the saved session
used `/private/tmp/...`.

The earlier authentication-blocked snapshots are retained as historical evidence.
After normal login, initialization advertised `cached_token` and `grok.com`, a
default auth method existed, and `session/new` worked without explicit authenticate.
No credential files were read or copied.

## Historical feasibility results

| Probe | Result | What it establishes |
| --- | --- | --- |
| `grok --version` and CLI help | PASS | Exact build; stdio, shared leader and custom leader socket options exist |
| Isolated ACP `initialize` | PASS | Protocol version 1; load/resume/close capabilities advertised |
| Hook capability metadata | PASS, advertisement only | Installed runtime lists blocking `pre_tool_use`, `stop`, `subagent_stop` and stop signals `continue`, `stopReason`, `additionalContext` |
| `x.ai/interject` with an invented missing ID | `-32601` | Unprefixed wire spelling is not recognized |
| `_x.ai/interject` with the same missing ID | `-32602`, session not found | Addressed method is dispatched and rejects a missing target; not proof of delivery |
| `x.ai/queue/enqueue`, `_x.ai/queue/enqueue` | `-32601` | These guessed enqueue methods do not exist on this build |
| `session/resume` with an invented missing ID | `FS_NOT_FOUND` | Refuses the nonexistent saved session; no existing history was opened |
| `session/new` after normal login | PASS | Synthetic model turn completed; own session closed |
| Earlier explicit `authenticate` | Historical 20-second timeout | Not retried after normal login |
| Explicit leader TUI attach, idle/busy delivery, routing isolation | PASS, bounded pilot | Original TUI displays generated joined nonce; sibling produces no agent text |
| 75-second no-send hold; producer disconnect | PASS | Idle B wakes afterward; terminals remain alive on disconnect |
| Socket-only launch without `--leader` | FAIL existing-TUI witness | ACP success alone is insufficient |
| Direct resident info and prompt without load/resume | PASS transport pilot | Original TUI generated the nonce; missing resident info returned an empty object |
| Trusted prompt rejection | NEGATIVE | Cancelled / HookDenied, stale prior usage.modelCalls=1, no user echo or agent content |
| Normal TUI exit with producer subscribed | NEGATIVE ownership | SessionEnd fired but actor stayed resident; ghost prompt completed |
| Native hooks/install/identity | PASS within measured topology | Normal trusted plugin; new/load/end; nonleader hook PPID equals TUI, native ID exact, socket absent |
| SessionStart systemMessage | UI-only | Visible notice; model answered NONE |
| Stop bootstrap + nonleader short idle monitor wake | PASS bounded pilot | One ordinary command approval, once-per-incarnation block/reason, joined idle nonce without typed nudge |
| Monitor tool-busy | PASS bounded pilot | Ingress during owned 12-second tool, done marker and joined nonce at 18.23 seconds |
| 35-minute monitor idle | Supplemental PASS, native-only | Raw screen FAIL retained; same-session events/export and scroll-only original-TUI witness prove fresh completion at 2.489 seconds after 2,100-second quiet hold |
| Direct-monitor payload | REJECTED | Reused END120 size passes invalidated; fresh 2-KiB random tail not seen at 121.95 seconds; 16-KiB escaped case no reply within 120 seconds |
| Native normal exit | PASS native child only | Child exited in 0.34 seconds; not production watcher proof |
| Native SIGKILL | FAIL native cleanup | Child remained after 10.59 seconds; coordinator cleaned its owned orphan |
| Native resume | PASS explicit rearm pilot | Same native ID, source load, fresh joined nonce; not automatic production rearm proof |
| Native compaction | PASS bounded pilot | Child survived a 25-second compaction; subsequent nonce visible in 2.6 seconds |
| Isolated HOME / existing GROK_HOME | PASS native preflight | Normal authenticated model reply and ordinary shell HOME/native ID matched; package acceptance was still pending at this pilot |
| Full retrieval/burst and production lifecycle at this pilot | OPEN at pilot checkpoint | Later measured package results below supersede this status; names/model/context compatibility is not inferred |

The initial reproduction driver is [probe.py](../../scripts/experiments/grok-participation/probe.py).
Its [README](../../scripts/experiments/grok-participation/README.md) describes scope and cleanup.
Sanitized handshake and live-blocker snapshots are in
[grok-participation-evidence](grok-participation-evidence/README.md).
Initial script runs confirmed the handshake, extension dispatch and former auth blocker.
Independent review identified an unbounded child cleanup path in the initial
probe. The corrected driver escalates only its owned process through EOF,
SIGTERM and SIGKILL with bounded waits, then cancels bounded reader tasks. A
synthetic SIGTERM-ignoring child was killed and reaped in 10.01 seconds; the
not-started cleanup path also passed. Capability output now selects known fields
instead of copying arbitrary metadata.

The standalone model witness now passes. The live TUI driver is
[tui_probe.py](../../scripts/experiments/grok-participation/tui_probe.py). It launches
only synthetic windows on an owned socket, uses explicit default permission mode,
asks for no tools, and checks both ACP-generated content and the original screen.
Busy work is initiated by the producer in that same attached session; this does
not yet cover a human-originated tool call or permission hold. A/B share cwd but
have different generated titles. Model completion latency was 1.49 seconds for
short-idle A in the first successful run; wake onset was not separately timed.
The 75-second hold is not the planned 2,100-second lifetime gate.

## Source-backed candidates, not runtime proof

The [official hooks page](https://docs.x.ai/build/features/hooks) describes lifecycle
identity and passive hook output. The installed capability metadata is richer;
treat that difference as a reason to measure hook behavior rather than choose one
statement as the runtime contract.

The [official ACP guide](https://docs.x.ai/build/cli/headless-scripting) documents
`initialize`, authentication, `session/new` and `session/prompt`. Its example auth
method names do not match this probe's advertised method list. The installed CLI
also has `--leader`, `--no-leader`, and a custom `--leader-socket` option.

The [official source](https://github.com/xai-org/grok-build) contains a shared-leader
transport, session load/resume, addressed interjection and prompt-queue machinery.
The research worker inspected a public revision distinct from the installed
binary; it is architectural evidence, not a version-matched implementation proof.
The inspected public snapshot is Git commit
`2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8`, with `SOURCE_REV`
`559751fdcec02d413e4c57c8832ab275e4f44980`, unlike installed `1474f9cbdd91`.
Relevant pinned source references:

- [Leader protocol](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/leader/protocol.rs)
  and [routing](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/leader/server.rs):
  separate client IDs, session subscriptions and framed IPC. Prefer the host's
  stdio proxy initially rather than implementing private IPC framing prematurely.
- [Prompt handling](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/mvp_agent/acp_agent.rs)
  and [queue](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/session/acp_session_impl/prompt_queue.rs):
  ordinary prompts queue, and the prompt RPC response waits for turn completion.
  A timed-out prompt request may already have been accepted. Configuration can
  promote follow-ups during blocking waits, so non-interruption needs measurement.
- [Interjection handler](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/extensions/interject.rs):
  returns queued status without using the channel-send result; do not treat it as
  an established reliable injection receipt.
- [Idle interjection fallback](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/session/acp_session_impl/interjection.rs):
  a candidate wake mechanism, still untested on the installed build.

Historically, plain `session/prompt` was the initial intake candidate. The later
ghost-actor negative rules it out for production without separate TUI ownership. An
interjection may only work while a turn is active; it cannot be assumed to wake
idle sessions. Never send cancellation or a send-now option simply to make an
otherwise blocked delivery work.

## Authentication follow-up

The pinned upstream [initialization](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-shell/src/agent/mvp_agent/acp_agent.rs)
reloads cached authentication and selects a method when usable. The [login flow](https://github.com/xai-org/grok-build/blob/2bdd1d6a6369de0e8c68132ea4539e9abd9e14a8/crates/codegen/xai-grok-login/src/flow.rs)
can include cache, refresh, external provider and interactive login. The
`headless` metadata flag is not a cache-only promise. TUI and shared-leader
startup have additional auth lifecycle behavior; a valid login in one process
can coexist with a null method in another.

Normal login resolved the measured blocker. No unadvertised authentication method
was forced, and no OAuth flow was repeated. The public source revision mismatch
remains a limitation on source-based explanations.

## Evidence checkpoint and next controls

The [allowlisted coordinator checkpoint](grok-participation-evidence/coordinator-checkpoint.json)
is derived from retained private synthetic reports. It contains booleans, bounded
counters, fixed protocol enums and logical filenames only. The
[evidence index](grok-participation-evidence/README.md) distinguishes derived
assertions from coordinator-observed original-TUI and permission witnesses.
No raw transcript, account metadata, native ID, signature or private host path is
exported. Public source remains mismatched with the binary.

The shared-TUI producer now ignores all reverse interaction requests rather than
cancelling permissions owned by the original TUI. The standalone probe still
denies its own permission requests. Scorer schema 2 correlates agent chunks and
completion with a fresh prompt ID, excludes user echoes/replay/sibling output,
requires exact nonce content and a separate original-TUI witness. Historical
schema-1 snapshots remain historical observations; they do not retroactively pass
the stronger scorer. A later short-idle replay-contaminated run was not a whole-run
PASS, even though its original TUI showed the expected nonce.

**Transport correction:** monitor notifications carry only body-free coalesced
wake hints under 400 UTF-8 bytes, not frames. Native pinned source declares
500/3,000 line/batch truncation constants and a 10-token bucket refilling one token
per two seconds; these are source facts, not version-matched runtime limits.
Earlier size claims using the same END120 tail are invalid: the model could infer
that tail from the smallest case. Fresh 2-KiB tail visibility failed. Use the
[bounded local read contract](grok-participation-contract.md#delivery-correction-wake-notices-plus-local-retrieval)
to retrieve complete original sink frames through validated pages. Existing
in-session held-body privacy stays intact; later production rounds below establish
16-KiB retrieval within the measured page and permission limits.

ACK follows [protocol v1](../protocol-v1.md): existing Muse sink append/write/Close
is local injection and permits ACK. Grok may reuse it without requiring model
receipt. A held/refused message is not appended/ACKed; native monitor drops and
model output are measured separately. Installed batching/rate/retention defaults remain unverified despite the new
pinned-source constants, and no exactly-once processing or durable monitor receipt is claimed.

The [plan gate ledger](../../.context/plans/grok-build-participation.md#gate-ledger)
and [finite test matrix](grok-participation-contract.md#remaining-finite-coordinator-gates)
separate historical pilots, completed package cases and unmeasured scope. The
12-second tool-busy pilot and 35-minute hold remain native-only evidence; neither
is silently promoted into a full-package long-duration or throughput guarantee.
The [operator fixture](../../scripts/experiments/grok-participation/README.md#nonleader-monitor-bootstrap)
prepares an inert scratch plugin; only the coordinator performs normal install,
trust, launch, approval and cleanup. This worker ran only offline scorer/fixture
checks, not any live model session.

## Completed native lifecycle checkpoint

The [schema-3 evidence](grok-participation-evidence/coordinator-checkpoint.json)
exports only allowlisted assertions from retained private coordinator evidence.
After 2,100 seconds, both automatic and control monitors had been quiet and their
initial wakes had passed. The raw screen-only scorer failed at 128.13 seconds:
the viewport remained on old output. It is preserved unchanged. The later retained
private `monitor-lifetime-supplement.json` correlates the same native session's
turn with ingress: start 0.258 seconds, completion 2.489 seconds, exact joined
assistant nonce in that owned session's export, no long nonce in the control.
Two PageDown keys only after the observation window revealed the reply in the
original TUI; no prompt or model nudge was sent. This is supplemental evidence of
the same run, not a rerun or a silent rewrite of the failed screen score. Together
these witnesses establish native long-idle PASS. A process/trigger remaining
present alone never established that pass. The later package acceptance below
does not repeat this full-duration hold; do not infer model receipt from local ACK.
The later leader 35-minute individual controls passed, but that whole run failed
its replay-contaminated short-idle check; it is not an overall PASS or a substitute.

Ordinary shell exposes the full native UUID; the monitor child exposes only
`GROK_AGENT`, with no `GROK_SESSION_ID`. Monitor HOME matches hook HOME. Observed
ancestry was Python -> bash -> actual TUI (kernel name `grok-macos-aarch`). Stop
`backgroundTasks` carried ID/type/status/description and no command field. These
facts require explicit validated binding in the monitor command and honest
unverified task status where exact command evidence is unavailable. Neither
native watcher-count text nor a model's ARMED banner establishes readiness.

Normal exit, explicit-rearm resume, compaction and HOME-split observations above
are native-only. SIGKILL survival requires production watcher/follower retirement;
a native child lifetime promise is insufficient. No auth file was read/copied and
no global configuration changed in the HOME preflight. Later package results
are recorded separately below, including bounded Brigade cleanup after SIGKILL;
they do not erase the generic native child orphaning failure.

## Installed-package acceptance checkpoint

The coordinator supplied `production-checkpoint-input.json`, already sanitized;
this documentation update reads no raw fixture, session or authentication files.
The [allowlisted production JSON](grok-participation-evidence/production-checkpoint.json)
retains case-specific source revisions: round 3 `4ce55d2`, round 5 `7bf519a`,
round 6 `6421256`, round 7/final runtime `d9279cd`. Results at earlier revisions
are not relabelled as reruns at the final head.

Three failures changed the implementation and acceptance procedure:

- Round 3 displayed registration and an ARMED claim, but watchers rejected the
  binding/closed on map mismatch and the live receiver count was zero. No message
  was sent; delivery acceptance failed. The later lifecycle correction stabilized
  registration; banners never constituted readiness.
- Round 5 proved local ACK, denied arming without automatic retry, explicit retry
  of backlog, and short/16-KiB replies, but the model batched pages into Python and
  extracted markers. Complete-frame model visibility was **not proven**. The
  corrected receiving instructions require each complete page visibly returned
  in model context before continuing; marker extraction is not an equivalent.
- The original 2,048-byte serialized page cap allowed native shell newline
  insertion at column 2,000, corrupting escaped JSON. The final cap is **1,800
  UTF-8 bytes for the entire serialized response**, including escaping, metadata
  and trailing newline. Round 7 validates the revised cap with escaped/multiline
  input. This is measured native behavior, separate from mismatched public source.

| Installed-package case | Coordinator evidence and limit |
| --- | --- |
| Maximum 16-KiB ASCII and UTF-8 bodies, round 6 | 18 individually visible pages each; complete frame 17,547 bytes, contiguous offsets, matching length/digest, stable retry, frame-complete then caught-up. Largest serialized pages 1,680/1,771 bytes; first-turn delays 0.279/0.218 s; verified replies and sibling absence. |
| Maximum escaped/multiline body, round 7 | 24 pages, largest exactly 1,800 bytes; complete 17,547-byte frame, matching length/digest, contiguous offsets, retry and caught-up; verified reply. First-turn delay 0.289 s; sibling witness unmeasured (null). |
| Burst and hints, round 6 | Three replies verified for sends spanning 0.137 s. Four observed notices, maximum 94 bytes: three change notices and one status notice. No broad rate envelope or deliberate hint-loss proof inferred. |
| Hold/refuse, round 6 | Neither local ACK nor native body exposure; last read caught-up with empty frame chunk. Already injected records remain a separate local-read boundary. |
| Cursor/retry, round 7 | Repeated pages identical; invalid cursor exits with usage status 2. |
| Resume, round 7 | Same native UUID, fresh incarnation; stale cursor rejected with status 2; fresh idle reply verified. Two pages, complete 1,227-byte frame, matching length/digest; no cross-incarnation backlog adoption proved. |
| Retention, round 7 | Watcher replaced with incarnation preserved under hold; old frame replayed with matching digest and caught-up. **No new empty sink generation materialized under hold**; this is old-sink readability, not a populated-rotation test. |
| Compaction, round 5 | Native compaction completed, monitor persisted and a fresh short message woke/replied. Full-size bodies were not remeasured through compaction. |
| Production cleanup | Round 5 SIGKILL: no owned Brigade processes remained, adapter session closed and map retired when observed at 1.655 s. Round 6 normal exit: same checks at 0.638 s. These are production cleanup measurements, distinct from the generic native monitor orphan. |

The coordinator reports the full acceptance gate passed at `d9279cd` with
`GOMAXPROCS=4`, all four package checks passed (including shellcheck/no-secrets),
18 experiment tests passed, conformance 48 PASS / 0 FAIL / C-14 slow default SKIP,
and source/binary vulnerability scans found none. Pinned ShellCheck 0.9.0 passed
at `2746e55`; the coordinator verified no shell/bootstrap diff afterward. This
worker does not repeat those runtime gates or claim an independent code review.

Native Stop still omits a command field. Task descriptions/status do not prove
command readiness; local status remains unverified despite observed delivery.
ACK still means permitted local append/write/Close, not model receipt or
exactly-once processing. Source-version mismatch, direct-monitor truncation,
invalid reused-tail size claims and leader ghost lifetime remain recorded.

## Limits and next decision

This documentation update creates no team or message; coordinator acceptance used
only its own synthetic fs team and sessions.
No normal Grok leader was killed. In the completed historical pilots, owned
probe clients, TUI windows and leaders were stopped; all live state remains
coordinator-owned. Grok may retain synthetic histories; no real transcript was read.
Temporary TUI workspaces remain outside the repository for inspection. This
checkpoint changes no production files, release pins or configuration.

The [implementation plan](../../.context/plans/grok-build-participation.md)
selects reversible nonleader monitor implementation with explicit release gates.
The measured Stop continuation supplies bootstrap after an initial turn; it is
not launch-time model context or a replacement for long-lived idle monitoring.
Runtime/native acceptance is complete within the measured scope; independent
Rjae Fable review, PR and release remain pending. Linux native execution and the
full-package 35-minute hold are unmeasured. Broader burst/loss envelopes and
cross-incarnation backlog recovery are not claimed; this checkpoint does not
close or release the card.
