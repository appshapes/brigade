# Grok Build participation feasibility — card 120

Date: 2026-10-18. [Card 120](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
Brigade base: `a7abf85136f61835a8c3db00c76fb68347317e3f` (0.34.1).
Installed Grok: `1.0.62 (1474f9cbdd91) [stable]`, macOS arm64.

## Verdict

**Monitor-first reversible implementation is feasible; shared-leader delivery is
not production-ready.** The coordinator measured a normally installed/trusted
Stop hook bootstrapping a native monitor in a fresh `--no-leader` TUI, with one
normal command permission and generated content after idle external ingress.
The [contract](grok-participation-contract.md) is accepted as implementation
guidance; the user's launch-mode scope preference is still pending. Monitor tool-busy wake passed; the 35-minute idle test remains ongoing. Full-body
wake/read retrieval, resume and cleanup
gates also remain open.

Earlier leader results below remain bounded **transport pilots**. A later normal
TUI exit left the subscribed native actor resident and direct ghost prompts still
succeeded. A rejected prompt returned cancelled/HookDenied with stale previous
modelCalls=1. Thus resident lookup, RPC completion and usage cannot establish
live TUI ownership or native admission. These negatives supersede the earlier
leader-first recommendation without erasing its successful original-TUI evidence.

Both TUIs must explicitly use `--leader` in this experiment. Merely selecting
`--leader-socket` yielded successful ACP model replies but **no reply in either
original TUI**. This negative control prevents mistaking a restored copy of saved
history for delivery into a live conversation. On macOS, canonical cwd was also
required: `/tmp/...` attachment failed with `FS_NOT_FOUND` while the saved session
used `/private/tmp/...`.

The earlier authentication-blocked snapshots are retained as historical evidence.
After normal login, initialization advertised `cached_token` and `grok.com`, a
default auth method existed, and `session/new` worked without explicit authenticate.
No credential files were read or copied.

## Measured results

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
| 35-minute monitor idle | ONGOING | No pass claimed |
| Direct-monitor payload | REJECTED | Reused END120 size passes invalidated; fresh 2-KiB random tail not seen at 121.95 seconds; 16-KiB escaped case no reply within 120 seconds |
| Payload/burst, monitor resume/crash, names/model/context | OPEN | No inferred compatibility claims |

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
in-session held-body privacy stays intact; full 16-KiB retrieval proof awaits
production implementation and coordinator measurement.

ACK follows [protocol v1](../protocol-v1.md): existing Muse sink append/write/Close
is local injection and permits ACK. Grok may reuse it without requiring model
receipt. A held/refused message is not appended/ACKed; native monitor drops and
model output are measured separately. Installed batching/rate/retention defaults remain unverified despite the new
pinned-source constants, and no exactly-once processing or durable monitor receipt is claimed.

The [plan gate ledger](../../.context/plans/grok-build-participation.md#gate-ledger)
and [finite test matrix](grok-participation-contract.md#remaining-finite-coordinator-gates)
carry the passed tool-busy wake, ongoing 35-minute monitor run and pending full
wake/read retrieval, payload, burst, resume,
denied-arming, sibling, stale binding and crash cleanup controls.
The [operator fixture](../../scripts/experiments/grok-participation/README.md#nonleader-monitor-bootstrap)
prepares an inert scratch plugin; only the coordinator performs normal install,
trust, launch, approval and cleanup. This worker ran only offline scorer/fixture
checks, not any live model session.

## Limits and next decision

This investigation creates no backend team and sends no messages to teammates.
No normal Grok leader was killed. In the completed historical pilots, owned
probe clients, TUI windows and leaders were stopped; current ongoing pilots remain
coordinator-owned. Grok may retain synthetic histories; no real transcript was read.
Temporary TUI workspaces remain outside the repository for inspection. This
checkpoint changes no production files, release pins or configuration.

The [implementation plan](../../.context/plans/grok-build-participation.md)
selects reversible nonleader monitor implementation with explicit release gates.
The measured Stop continuation supplies bootstrap after an initial turn; it is
not launch-time model context or a replacement for long-lived idle monitoring.
The card remains open through implementation and release.
