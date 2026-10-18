# Grok Build participation feasibility — card 120

Date: 2026-10-18. [Card 120](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
Brigade base: `a7abf85136f61835a8c3db00c76fb68347317e3f` (0.34.1).
Installed Grok: `1.0.62 (1474f9cbdd91) [stable]`, macOS arm64.

## Verdict

**BLOCKED on live authentication; first-class participation is not yet proven.**
The installed runtime exposes promising ACP and shared-leader surfaces. No
production Grok integration has been implemented, and no real user's session or
credential files were inspected. Registration, busy delivery and idle wake must
not be inferred from plugin compatibility or a successful protocol handshake.

The user has installed and updated Grok. A separate `grok agent --no-leader stdio`
process successfully initialized, but `session/new` returned `-32000`,
`Authentication required`, with `no auth method id provided`. Initialization
advertised only the `grok.com` auth method and no default auth method. An explicit
`authenticate` request with that method and `headless: true` did not answer within
20 seconds. That process was stopped. Interactive authentication was attempted; no completion was observed and no credential was read or copied. The final probe does not invoke that login flow.
This does not prove the user's normal TUI is logged out: independent ACP startup
may need different authentication wiring. Confirm a normal terminal prompt, then
resolve that contract before proceeding with live delivery tests.

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
| `session/new` | BLOCKED, `-32000` | This standalone connection cannot create a model session with its current auth state |
| Explicit `authenticate` | BLOCKED, 20-second timeout | No headless auth completion observed; no model request followed |
| Same-session busy/idle delivery, TUI attach, routing isolation | NOT RUN | Requires live authenticated sessions |
| Lifecycle hooks, names/model/context, package install | NOT RUN | No inferred compatibility claims |

The initial reproduction driver is [probe.py](../../scripts/experiments/grok-participation/probe.py).
Its [README](../../scripts/experiments/grok-participation/README.md) describes scope and cleanup.
Sanitized handshake and live-blocker snapshots are in
[grok-participation-evidence](grok-participation-evidence/README.md).
Repeated script runs confirmed the handshake, extension dispatch and auth blocker.
Independent review identified an unbounded child cleanup path in the initial
probe. The corrected driver escalates only its owned process through EOF,
SIGTERM and SIGKILL with bounded waits, then cancels bounded reader tasks. A
synthetic SIGTERM-ignoring child was killed and reaped in 10.01 seconds; the
not-started cleanup path also passed. Capability output now selects known fields
instead of copying arbitrary metadata.

The model-content witness never passed: no successful model response was observed.

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

## Next live arms and controls

1. Obtain a successful ordinary Grok prompt using the existing login. Resolve
   authentication for the isolated leader/client topology without exporting auth.
2. Launch two synthetic TUI conversations A and B on a separately owned leader.
   Keep their full native IDs and client attachment identities distinct. A second
   producer must attach without stealing either TUI or loading a replacement run.
3. Deliver a split nonce to A after a short idle hold. Require a completed model
   reply joining its two pieces; the joined answer must not appear in the request.
   B must stay silent. Measure wake and model-content separately.
4. Repeat while A is busy. Require eventual model-visible delivery without
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
No normal Grok leader was killed. The standalone probe process was closed; its
workspace was temporary. Failed session creation left no probe-created conversation
to close. No production files, release pins or configuration were changed.

The [conditional implementation plan](../../.context/plans/grok-build-participation.md)
starts with the live proof. If only next-human-prompt polling or a separate worker
is available, report that limitation and revisit the product scope; do not call it
first-class support. The card remains open through implementation and release.
