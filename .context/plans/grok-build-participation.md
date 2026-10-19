# Grok Build participation — card 120

Date: 2026-10-18. Status: monitor-first contract accepted for reversible
implementation targeting explicit `--no-leader` by coordinator scope choice;
35-minute native monitor supplemental PASS retains the raw viewport FAIL.
Actual production package gates remain pending before merge-ready acceptance. Direct full-frame monitor output is superseded by bounded
wake notices and local sink retrieval.
[Card](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
[Measured experiment](../../docs/experiments/grok-participation.md).
[Contract](../../docs/experiments/grok-participation-contract.md).
Implementation base: `70111d68bde3532dbf7d5ac98345864a6cff0fc3`; measured Grok Build
`1.0.62 (1474f9cbdd91) [stable]`.

## Objective and decision

Make Grok a Brigade peer with correct identity, seven skills, busy/idle receiving,
resume and bounded cleanup. Implement the reversible **explicit --no-leader native
monitor route** while Sol owns production code and coordinator owns Git and live
external state. The coordinator selected this initial scope after the optional
preference question received no answer; this is not claimed user approval.
General support remains technically gated. Shared-leader delivery is unsupported
until independent TUI ownership is proven.

Preserve the prior leader idle/busy/isolation successes as transport pilots.
Later direct prompt without load/resume succeeded in the original TUI, but normal
TUI exit left a subscribed actor resident and a ghost prompt succeeded. Rejection
returned cancelled/HookDenied with stale preceding usage.modelCalls=1 and no
user echo or agent content. Resident handles, completion and usage do not prove
live ownership/admission. Do not ship that direct route.

The normally trusted nonleader Stop bootstrap passed: one block/reason after the
first turn caused one native monitor call, one ordinary command permission,
AUTO_MONITOR_ARMED, then generated AUTO_WAKE_120 after idle external ingress.
Hook parent equaled the actual TUI, native UUID matched, leader socket was absent.
SessionStart systemMessage was UI-only. These are runtime observations, separate
from the public source revision that differs from the installed executable.

## Gate ledger

| Gate | Current evidence | Remaining before support |
| --- | --- | --- |
| G0: version/protocol | PASS: standalone ACP and version captured | Version drift/package checks |
| G1: normal authentication/trust | PASS: existing login, normal local plugin install --trust, one command approval | No bypass or blanket permission; denied-arming case |
| G2: identity/routing | Leader sibling isolation passed; nonleader hook parent/native ID match | Nonleader sibling/duplicate-title and poisoned XDG/BRIGADE tests; same pointed binding in actual package shell/monitor. Native scratch HOME/existing GROK_HOME authenticated preflight passed without auth access; package hook isolation pending |
| G3: busy/idle | Leader producer-originated busy/idle pilot passed; nonleader short idle and Stop bootstrap passed | Native 12-second tool-busy pilot PASS (18.23-second nonce completion); full retrieval still pending; raw replay-contaminated scorer run is not a whole-run pass |
| G4: lifetime | Native supplemental PASS: 2,100-second quiet hold, correlated turn completes 2.489 seconds after ingress, own-session exact assistant export and scroll-only original-TUI witness. Raw 128.13-second viewport FAIL retained | Actual production package long-idle acceptance; do not count stale viewport alone as delivery failure or process survival as success. Leader longhold controls passed individually, whole run failed replay scoring |
| G5: hooks/arming | Normal new/load/end observed; Stop one-shot continuation and nonleader parent measured | Native Stop has no command field; production task binding, denial/retry, no duplicate on resume/compact; Python startup timeouts are not hook passes |
| G6: local injection | Muse parity selected: sink append + close -> Pipeline.Done -> ACK | Grok failure/hold/refusal/restart regressions; native drop/batch counts separate from ACK |
| G7: lifecycle/payload | Native /exit child stops in 0.34 s; SIGKILL child survives 10.59 s (owned orphan cleaned); explicit-rearm resume and compaction nonce pass. Direct full-body monitor rejected; fresh 2-KiB tail failed | Actual package exit/crash/stale binding/replacement, full wake/read retrieval, burst/retention and bounded follower retirement; native-only is not production acceptance |

Live tests are coordinator-owned, synthetic and limited to owned sessions. No
credentials, real transcripts, global config edits or auto-trust plugin-dir route.
Use the [finite numeric matrix](../../docs/experiments/grok-participation-contract.md#remaining-finite-coordinator-gates):
64 B, 2 KiB, 8 KiB and protocol-maximum bodies; 1/10/50 frames at 1 per second and a
10-frame burst; proposed 30/120-second wake/content budgets; 2,100-second idle;
proposed 10-second local retirement bound. These are test budgets, not native
defaults. No unknown rate/retention value becomes a promise by inference.

## Selected monitor contract

1. **Identity and nonleader gate.** Support the explicitly documented
   `grok --no-leader` topology. Refuse a populated GROK_LEADER_SOCKET and unknown
   parent topology; an absent variable alone is insufficient proof. Bind the
   native GROK_SESSION_ID to the actual hook-parent TUI PID plus process start
   identity, canonical workspace and Brigade session. Use a pointed map resolved
   from the native identity for later model shell commands, not their shell
   PPID. Execute hook entrypoints without an intervening long-lived wrapper
   process that changes PPID. Never adopt a leader PID or resurrect saved history.
   Validate stale PID/reuse, sibling, pointer replacement and mismatched cwd.
   Ordinary shell has the full native UUID; native monitor child has only
   GROK_AGENT. Supply the hook-bound identity explicitly in the fixed monitor
   command and validate it against the current map/incarnation and actual TUI
   ancestry. Environment text alone is not authorization. Measured child ancestry
   is Python -> bash -> TUI with kernel name `grok-macos-aarch`.
2. **Options and package boundaries.** Add Grok's host identity and
   `grok-options.json` through the shared options/parser and harness contracts,
   following Muse's typed options and HOME-based trusted discovery. Preserve
   existing consent, hold/refusal, notification and state-path semantics. Native
   shell identity uses GROK_SESSION_ID; GROK_PLUGIN_ROOT is a hook installation
   locator, not permission to trust arbitrary environment paths. Do not inherit
   Claude socket credentials/settings or use GROK_LEADER_SOCKET as delivery.
   Test poisoned XDG/BRIGADE overrides separately; this pilot did not do so.
3. **Startup and hook data.** Package native SessionStart, UserPromptSubmit,
   Stop and SessionEnd hooks with the measured native field spellings. Handle
   new/load explicitly. SessionStart can return exit-0 JSON such as
   `{"systemMessage":"Brigade: monitor setup follows the first completed turn; use the Brigade skill to retry if needed."}`.
   This is UI-only. Hooks must emit only protocol JSON on stdout, consume bounded
   input, avoid transcript reads and finish within their native timeout.
4. **One Stop bootstrap per incarnation.** For main-session genuine `end_turn`
   only, current registered binding, no matching live native monitor task,
   `stopHookActive != true`, and no previous automatic attempt, atomically mark
   attempt-issued then emit exit-0 JSON:

   ```json
   {"decision":"block","reason":"Arm this session's Brigade inbox monitor once using the Brigade skill and normal permission flow, then finish. If permission is denied or arming fails, report how to retry; do not keep trying."}
   ```

   Installed `~/.grok/docs/user-guide/10-hooks.md:549–570` documents that reason
   becomes a user message continuing the same turn. Do not add `continue:false`
   (it overrides blocking), depend on exit-2 stderr fallback, or use the host's
   eight-continuation cap as Brigade's guard. Exclude subagents, teardown,
   cancellation and failure. Do not reset the attempt on every prompt or Stop.
   A failed/crashed attempt remains discoverable as needs-user-action; explicit
   skill retry is available. Resume with a new incarnation may get one new
   opportunity, after checking for an already matching native task. Compaction
   alone must not create another monitor.
5. **Native task evidence and permission.** Arm the fixed installed
   `brigade inbox follow` command using native `monitor` with `persistent:true`;
   keep ordinary permissions and exact command approval. Track native task ID,
   monitor type/live status and binding to the expected command/incarnation via
   supported hook task evidence. The installed guide exposes `backgroundTasks`
   with ID/type/status and optional command, but measured native Stop has **no
   command field**; descriptions can be clipped. A
   model's ARMED text, description substring or attempt marker is not readiness.
   Where command/task binding cannot be proven, keep status unverified and expose
   retry/diagnosis rather than claiming ready or issuing repeated auto-arms.
   Exact production task correlation still needs the synthetic probe below.
6. **Sink and lifecycle.** Reuse the private per-watcher sink and local ACK path
   above. Let the watcher own bounded retirement on actual TUI death, with
   SessionEnd as a prompt signal, not the sole proof. Compare incarnation before
   removing pointers so an old watcher cannot retire a resumed receiver. Keep
   SessionEnd short: its documented default/plugin cap is 1.5 seconds; do not
   await network cleanup there. Stop the bound follower on lost registration,
   changed identity or closed consumer. Do not kill the TUI/shared processes.
   Native monitor merges stdout **and stderr** into events, so emit only body-free
   wake notices under 400 UTF-8 bytes, coalesced at most once per five seconds; no
   heartbeats or debug stream. Keep diagnostics in the existing local logging path. The
   follower stays read-only; do not require it to write protected state from the
   model sandbox. Do not copy Muse's `wake_delay_ms:0` into Grok's tool call.
7. **Skills and launch documentation.** Provide all seven native skills: email,
   setup, join, slack, team-messaging, sessions and update. Preserve their command
   contracts. Add exact normal-trust installation and `--no-leader` launch/resume
   instructions, permission expectation, first-turn bootstrap timing, manual
   arming/retry and status meaning. The receiving skill must recognize complete
   framed messages and handle replay by message ID without claiming exactly once.
   Document unsupported leader launch and unarmed/backlog behavior plainly.

## Inbound and ACK contract

Reuse the existing frame, pipeline validation/policy/dedupe and per-watcher sink.
Successful permitted append/write/Close is local injection and permits ACK,
exactly as Muse; no fsync or native/model receipt is implied. Held/refused messages
are not appended/ACKed; sink errors do not mark seen. Pre-arm backlog may already
be locally injected while receiving is unarmed; document that distinction and
explicit retry. Native arming denial does not retroactively undo a local append.
Do not change frozen protocol v1 or claim exactly-once model processing.

Grok must not put full bodies/frames on the monitor. Implement the
[corrected delivery contract](../../docs/experiments/grok-participation-contract.md#delivery-correction-wake-notices-plus-local-retrieval):
body-free hints under 400 UTF-8 bytes, at most one per five seconds; a read-only
`brigade inbox read [--cursor <token>]` over only this binding's own already
locally injected sink records. Preserve bare inbox's in-session held-body privacy,
Muse follower semantics and ACK timing. Never fetch backend pending bodies or
release policy from read.

Resolve/validate native UUID, actual TUI incarnation and Brigade UUID at each read;
reject caller session/path selection, malformed/cross-binding cursor, symlink or
file substitution. Include retained authorized sink generations, not just newest.
Use bounded lossless pages, initially at most 2,048 UTF-8 bytes for the **entire
serialized response**, with record ID, whole-frame length/digest, exact offsets,
completion markers and continuation cursor. This is a proposed CLI budget, not
a measured host limit. The skill fetches every page before treating the original
untrusted frame as complete, continues through records until caught-up, and never
infers missing text. Read/notice emission never deletes, consumes, marks processed
or ACKs. Cursor loss permits replay; lost/coalesced hints do not discard bodies.

New release gate: production hint -> read -> all pages -> original-TUI proof for
16-KiB ASCII/UTF-8/escaped/multiline bodies with fresh hidden independent random
beginning/middle/end sentinels. Unit tests reassemble exact bytes and verify encoded
page budgets; live tests verify native output is complete, burst/coalescing,
rotation, cursor retry/tampering and hold/refusal isolation. Prior END120 tail
reuse invalidates the entire earlier size-pilot success claim. Fresh random-tail
2-KiB direct-monitor visibility failed at 121.95 seconds; 16-KiB escaped case had
no reply within 120 seconds. Native tool-busy wake passed at 18.23 seconds, while
the completed 35-minute hold has a supplemental native PASS at 2.489 seconds
(raw viewport FAIL retained). These native successes and lifecycle negatives are
not production retrieval passes.

## Implementation, tests and packaging

Extend shared harness/config/hook/sessionmap/watch seams narrowly, with Grok
options and pointed lookup; keep Claude, Muse and Codex compatibility. Start with
proven name/version/model sources and omit unknown usage/context. No host-history
reader based solely on unverified source/schema. No new runtime dependency or
separate MCP server. Follow [harness package boundaries](../../docs/developer/change/harness-packages.md).

Package a distinct native `grok/brigade/`, seven reachable skills and native hooks.
Add registry/six persona pages, package README, install/update/leave instructions,
package check/CI and shared release assets through normal tooling. Do not invent
release pins. Follow [harness documentation](../../docs/developer/documentation/add-a-harness.md).
Use isolated owned adapter fixtures for send/reply, never unsolicited team sends.

Run appropriate focused tests plus the repository acceptance gate:

```sh
make typecheck lint build test vuln deps-check schema-check tidy-check docs-check
```

The final research checkpoint passed `make docs-check` (169 pages, 665 links);
the earlier Go-cache denial was not bypassed. Coordinator owns full acceptance. Run existing/new package checks and pinned shellcheck if
shell files change. Independent review by Rjae Fable follows runtime work; the
coordinator owns subsequent Git/PR/auto-merge authorization. This research
checkpoint is not card completion, shipping, review approval or a live-gate pass.
