# Grok Build participation feasibility — card 120

Date: 2026-10-18. [Card 120](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
Brigade base: `a7abf85136f61835a8c3db00c76fb68347317e3f` (0.34.1).
Installed Grok: `1.0.62 (1474f9cbdd91) [stable]`, macOS arm64.

## Verdict

**Conditional feasibility: explicit leader-backed Grok TUIs receive external turns.**
After the user completed normal login, the standalone ACP model-content check
passed. An independent producer then delivered joined nonce replies into two
original Grok terminal windows using their native IDs. Idle delivery, queued busy
delivery without cancellation, a 75-second no-send hold, sibling isolation, and
producer disconnect survival passed in the tested topology. No production Grok
integration is implemented; longer idle, hooks, lifecycle and ACK semantics remain
unproven.

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
| Lifecycle hooks, names/model/context, package install | NOT RUN | No inferred compatibility claims |

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

Plain `session/prompt` is the candidate intake operation to test first. An
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

## Live arms and remaining controls

1. DONE: obtain a successful ordinary Grok prompt using the existing login. Resolve
   authentication for the isolated leader/client topology without exporting auth.
2. DONE for explicit leader mode: launch two synthetic TUI conversations A and B on a separately owned leader.
   Keep their full native IDs and client attachment identities distinct. A second
   producer must attach without stealing either TUI or loading a replacement run.
3. DONE for content completion: deliver a split nonce to A after a short idle hold. Require a completed model
   reply joining its two pieces; the joined answer must not appear in the request.
   B must stay silent. Measure wake and model-content separately.
4. DONE for producer-started work: repeat while A is busy. Require eventual model-visible delivery without
   cancelling its current work. Determine whether prompts queue or interject.
5. Hold an existing receiver idle for 2,100 seconds with a no-send control; repeat
   across three independent sessions. This tests attachment lifetime, not just a
   turn-boundary hook. Proposed pilot budgets are 30 seconds to wake and 120 seconds
   for content completion, measured separately; freeze them before running.
6. Check wrong/closed ID, producer disconnect, receiver exit with leader alive,
   resume/fork/new/compact, and ingress success with a lost response. Prove the
   acknowledgment point before connecting Brigade's real inbound pipeline.
7. Exercise normal installed/trusted hooks and 64 B, 2 KiB, 8 KiB, 16 KiB and maximum
   production-frame payloads. Verify no silent truncation, cross-session routing,
   credentials in reports, or global host-state edits.

A no-send control, wrong-ID target and scorer mutations must be able to make each
success claim fail. Sender echo, queued notifications, hook stdout and transcript
input are not model-content witnesses. A successful private headless conversation
is useful baseline evidence but cannot pass the existing-TUI gate.

## Limits and next decision

This investigation creates no backend team and sends no messages to teammates.
No normal Grok leader was killed. Owned probe clients, TUI windows and leaders
were stopped. Grok may retain synthetic histories; no real transcript was read.
Temporary TUI workspaces remain outside the repository for inspection. No production files, release pins or configuration were changed.

The [conditional implementation plan](../../.context/plans/grok-build-participation.md)
starts with the live proof. If only next-human-prompt polling or a separate worker
is available, report that limitation and revisit the product scope; do not call it
first-class support. The card remains open through implementation and release.
