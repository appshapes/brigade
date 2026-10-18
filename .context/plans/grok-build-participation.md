# Grok Build participation — card 120

Date: 2026-10-18. Status: feasibility in progress; implementation phases conditional.
[Card](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
[Measured experiment](../../docs/experiments/grok-participation.md).
Base: `a7abf85136f61835a8c3db00c76fb68347317e3f`; Grok Build 1.0.62.

## Objective and current decision

Make a normal Grok Build conversation a first-class Brigade peer: join the same
team, appear with the correct identity, send/reply through skills, receive while
busy and idle, recover on resume, and close without harming sibling sessions.
This work first establishes feasibility, then chooses the integration contract.

**No GO yet.** ACP handshake and missing-target interjection dispatch are measured.
The standalone ACP client cannot create a session with its current authentication
state. Busy/idle delivery and normal TUI attachment remain unproven. The following
phases are a dependency-ordered proposal, not claims of supported behavior.

## Phase 0 — prove the host contract

Use the committed probe as a baseline and extend it with coordinator-owned,
synthetic test sessions. Do not attach to existing user conversations or print
credentials. Resolve the difference between normal TUI login and standalone ACP
login through supported host behavior; do not copy auth files or bypass trust.

| Gate | Required evidence | Current state |
| --- | --- | --- |
| G0: version and protocol | Exact executable version; initialized capabilities; bounded cleanup | PASS for standalone ACP |
| G1: authentication | A synthetic session completes a fixed response with the existing login | BLOCKED |
| G2: exact routing | Independent producer addresses A; B with same directory/title remains untouched | NOT RUN |
| G3: idle and busy | Same live TUI consumes split nonce after idle and during/after busy work, no typed nudge or cancellation | NOT RUN |
| G4: lifetime | 35-minute idle hold plus matched no-send control; repeat short cases in three independent sessions | NOT RUN |
| G5: hook and identity | Normal installed/trusted hook payloads, tool env, shared-parent behavior and session-specific liveness | NOT RUN |
| G6: admission and recovery | Wrong/closed IDs, disconnects, lost response, backpressure, native permission hold and replay behavior | NOT RUN |
| G7: lifecycle and size | New/fork/resume/compact/exit/crash, leader survives receiver exit; frame sizes up to production cap | NOT RUN |

Choose `session/prompt` through a secondary shared-leader client as the first
candidate, based on source inspection. Compare `session/load` and `session/resume`
only after inspecting their attachment and ownership effects. `_x.ai/interject`
is a separate busy-delivery candidate; its recognized method alone proves nothing
about idle wake. Do not invent a `queue/enqueue` command: the installed probe
rejected it. Avoid a new prompt process resuming saved history concurrently with
the real receiver, since that could be a different execution owner.

Keep transport acceptance, wake, model-visible content and completed action as
separate milestones. Pilot budgets proposed before a run: wake within 30 seconds
of idle ingress (or of the busy receiver becoming runnable), completed content
within 120 seconds. Provider/auth failure is BLOCKED, not proof of transport
failure. Score an assistant-produced joined nonce that is absent from input;
mutating the target, completion marker or nonce must fail the scorer.

Decision after G1–G7:

- **GO:** same existing session receives busy and idle; isolation, long idle,
  lifecycle and a defensible local injection boundary all pass under normal trust.
- **CONDITIONAL:** only a measured topology or explicit arming step works. State
  the limitation and obtain product acceptance before presenting it as support.
- **NO-GO:** only next-user-prompt polling, destructive interruption, a replacement
  worker, unsupported history writes, credential copying or trust bypass works.
- **BLOCKED:** required auth, API or instrumentation is unavailable; preserve the
  evidence and identify the precise next action. Do not substitute assumptions.

## Phase 1 — freeze the selected contract

Write a short host contract after the proof: minimum measured Grok build and modes;
full native ID; identity source for hooks and model shell; endpoint discovery;
client/session ownership; session-specific liveness; trust/permission semantics;
payload cap; request response and retry behavior; cleanup on client disconnect.
Private extension methods require an explicit version/capability gate and a drift
test, not an unbounded promise of compatibility.

Prefer a native addressed transport over a monitor if both work. A monitor remains
an alternative only after proving idle wake, readiness, re-arm, sink lifetime and
failure semantics. Do not ship a separate MCP server or add a runtime dependency
without revisiting the repository's all-Go CLI design and dependency allowlist.

## Phase 2 — identity, configuration and lifecycle

Existing seams to extend narrowly, after the contract is known:

- `internal/harness/config/environ.go` (`Host`, `HostOf`, `Pointed`) and
  `config/session.go` (`pointedSession`): recognize Grok explicitly and validate
  by-host pointers against the full native/Brigade/host binding. Do not fall into
  Claude's credential or configuration lookup.
- `internal/harness/hook/hook.go` (`adoptHost`, `facts`) and `hook/start.go`
  (`buildMap`, `sessionStart`, `resumeHint`, `retirePointer`): obtain measured
  native identity and separate it from the real process/liveness witness.
- `sessionmap`, `hook/prompt.go`, `hook/end.go`, and `watch/lifecycle.go`: register,
  retry, compact, resume and close using the measured lifecycle. A shared leader
  PID alone cannot represent the lifetime of one conversation.

Tests: two sessions under one leader do not overwrite maps; same CWD/title is not
identity; invalid/missing/mismatched pointers fail closed; inherited host variables
do not select another peer; new/fork/compact/resume transitions are explicit;
receiver crash or missing end hook yields bounded close/lease expiry without
terminating the shared leader. Keep old Claude/Muse/Codex maps compatible.

## Phase 3 — inbound delivery and acknowledgments

Add one host-specific route at `internal/harness/watch/inject.go` after identity is
sound. Reuse `internal/harness/inbound/pipeline.go` (`Offer`, `Done`) for validation,
policy, dedupe, limits and pending state. Use the established external-message
frame and untrusted-source semantics in `internal/harness/frame`.

Brigade's ACK denotes successful local injection, not model processing. Determine
what the Grok transport guarantees before returning success to `Pipeline.Done`.
A queue notification or socket write alone may not establish native admission.
A rejected, permission-held or nonexistent target must not be marked seen/ACKed.
A lost response after accepted ingress is uncertain, not safely retryable by
assumption. Measure native message IDs/dedupe or document the at-least-once window;
never claim exactly-once processing.

Tests: failures before admission, accepted request with lost response, watcher
crash before backend ACK, producer disconnect, receiver closing while leader
lives, stalled busy session, full queue, malformed/oversized frame, hold/refuse,
concurrent siblings and identity change. Preserve `watch/attempt.go` ACK retries
and frozen protocol v1. No direct dependency on Supabase implementation.

## Phase 4 — skills, presence and metadata

Port team messaging, sessions, join and gateway skills to the measured Grok
package conventions. Confirm tool-shell resolution uses the same map as hooks,
without assuming Claude-compatible discovery implies compatible runtime inputs.
Verify send/reply against a local scratch adapter or owned test sessions before
messaging any actual teammate. No unsolicited Slack/email test sends.

Watcher heartbeat remains independent of user prompts. Start with proven name,
model and version sources; unknown context stays omitted. Add bounded, exact-ID
host-history readers only after real successful usage evidence establishes the
counting semantics. Test rename and child/cumulative usage exclusions. Presence
must distinguish watcher alive, receiver ready and last injection; none proves a
message was processed by the model.

## Phase 5 — native packaging, documentation and regression

Once runtime participation passes, add the host package with manifest, hooks,
reachable skills, bootstrap and canonical assets. Extend `docs/harnesses.json`,
the six required persona pages, the package README and install/update/leave
instructions. Add a package check beside the existing three host checks and wire
it into CI. Do not generate VERSION/checksum pins by hand; release tooling must
produce all package assets together.

Run the repository acceptance gate:

```sh
make typecheck lint build test vuln deps-check schema-check tidy-check docs-check
```

Run existing and new package checks and the normal installed/trusted live-host
matrix. If shell files change, verify with pinned shellcheck 0.9.0 as well as the
local version. Preserve all existing hosts, protocol v1, stdout discipline and
secret redaction. Record limitations and minimum build in the changelog and
security documentation before release. Card completion requires shipping and
meeting its criteria, not this research plan alone.

## Work ownership and artifacts

The coordinator owns live processes and external state. Sol researched Grok's
installed CLI and upstream surfaces; Astra independently mapped Brigade seams and
proof requirements. Workers use isolated worktrees, do not read credentials, and
cannot expand scope through their reports. The retained local reports provide
additional file/function references; the public experiment record contains only
sanitized, reproducible observations.

Next action: confirm normal Grok login, prove isolated ACP session creation, then
run a two-TUI shared-leader delivery experiment. Implementation phases wait for
the resulting GO decision. No release or message to another person is authorized
by this research milestone.
