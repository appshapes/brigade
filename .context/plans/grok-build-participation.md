# Grok Build participation — card 120

Date: 2026-10-18. Status: runtime and installed-package native acceptance complete
within measured macOS arm64, explicit `--no-leader` scope at `d9279cd`.
Independent Rjae Fable review, PR and release pending. Full-package 35-minute hold
and Linux native execution remain unmeasured; historical native-only supplemental
PASS and raw viewport FAIL stay separate. Full-frame monitor output is superseded
by bounded wake notices and local sink retrieval with a final 1,800-byte page cap.
[Card](https://trello.com/c/sTkdphju/120-make-grok-build-a-first-class-brigade-participant).
[Measured experiment](../../docs/experiments/grok-participation.md).
[Contract](../../docs/experiments/grok-participation-contract.md).
Implementation base: `70111d68bde3532dbf7d5ac98345864a6cff0fc3`; measured Grok Build
`1.0.62 (1474f9cbdd91) [stable]`.

## Objective and decision

Make Grok a Brigade peer with correct identity, seven skills, busy/idle receiving,
resume and bounded cleanup. The reversible **explicit --no-leader native
monitor route** is implemented; Sol owns production code and coordinator owns Git and live
external state. The coordinator selected this initial scope after the optional
preference question received no answer; this is not claimed user approval.
Acceptance covers only the measured topology, workload and permission flow. Shared-leader delivery is unsupported
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

| Gate | Current evidence | Scope still unmeasured or pending |
| --- | --- | --- |
| G0: version/protocol | Runtime d9279cd; Grok 1.0.62 (1474f9cbdd91), macOS arm64; full acceptance and four package checks passed | Linux native execution/version drift; public source still differs from installed binary |
| G1: authentication/trust | Normally trusted installed package, isolated scratch HOME/fs team, copied SHA256-pinned dev binaries, default permissions; denial cancelled turn without auto-retry and explicit retry retrieved backlog | No always-approve/unattended permission claim; exact page/reply command approvals were required |
| G2: identity/routing | Stable package registration after historical round-3 failure; ASCII/UTF-8 sibling absence; resumed same native UUID with fresh incarnation and stale-cursor rejection | Escaped/resume sibling witness null; no blanket topology/environment coverage |
| G3: busy/idle | Package idle short and maximum ASCII/UTF-8/escaped body replies, full visible pages; three-message burst over 0.137 s | Native-only 12-second busy pilot remains separate; larger bursts, deliberate hint loss and general rate envelope unmeasured |
| G4: lifetime | Native-only 2,100-second quiet hold supplemental PASS with correlated completion/export/scroll-only TUI witness; raw viewport FAIL retained | Full installed-package 35-minute hold not repeated; leader whole run remains failed due replay contamination |
| G5: hooks/arming | Stable registration, pre-arm backlog/retry, fresh-incarnation resume and short-message compaction delivery | Stop has no command field; task status remains unverified rather than claiming armed; no full-size compaction remeasurement |
| G6: local injection | Local ACK parity preserved; hold/refuse no ACK/body exposure. Same-incarnation watcher replacement retains readable old sink under hold | No new empty sink materialized; populated new rotation and cross-incarnation adoption not proved. ACK is not model receipt |
| G7: lifecycle/payload | Production SIGKILL cleanup observed at 1.655 s; normal exit 0.638 s; no owned Brigade processes, adapter closed/map retired. Complete 17,547-byte frames and digest/offset/retry proof; final escaped pages at most 1,800 bytes | Generic native orphan, invalid reused-tail passes, marker-only round 5 and failed 2,048-byte native cap remain historical negatives; independent review/PR/release pending |

Live tests are coordinator-owned, synthetic and limited to owned sessions. No
credentials, real transcripts, global config edits or auto-trust plugin-dir route.
The [scope matrix](../../docs/experiments/grok-participation-contract.md#remaining-finite-coordinator-gates)
and [production checkpoint](../../docs/experiments/grok-participation.md#installed-package-acceptance-checkpoint)
separate original proposed budgets from measured acceptance. The proposed 10/50
frame envelope and full-package 2,100-second hold are not passes by inference.

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
   Native acceptance measured delivery while this status remained unverified;
   descriptions/status alone still cannot establish exact command readiness.
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
Use bounded lossless pages, at most 1,800 UTF-8 bytes for the **entire
serialized response**, with record ID, whole-frame length/digest, exact offsets,
completion markers and continuation cursor, including escaping and the newline.
The initial proposed 2,048-byte cap failed native escaped/multiline retrieval:
the shell inserted raw newlines at column 2,000, corrupting JSON escapes. The
1,800-byte cap leaves margin below that measured boundary; round 7 validates it
with 24 escaped/multiline pages, exact complete-frame length/digest and reply. The skill exposes each entire page in model context in a separate native tool
call, without subprocess batching or marker extraction, before treating the original
untrusted frame as complete, continues through records until caught-up, and never
infers missing text. Read/notice emission never deletes, consumes, marks processed
or ACKs. Cursor loss permits replay; lost/coalesced hints do not discard bodies.

Completed measured payload gate: production hint -> read -> all pages -> reply for
16-KiB ASCII/UTF-8/escaped/multiline bodies with fresh hidden independent random
beginning/middle/end sentinels. Unit tests reassemble exact bytes and verify encoded
page budgets. Coordinator rounds 6/7 verify complete native pages, a three-message
burst, cursor retry/rejection and hold/refusal isolation; old-sink retention was
measured after same-incarnation watcher replacement, not a populated new rotation. Prior END120 tail
reuse invalidates the entire earlier size-pilot success claim. Fresh random-tail
2-KiB direct-monitor visibility failed at 121.95 seconds; 16-KiB escaped case had
no reply within 120 seconds. Native tool-busy wake passed at 18.23 seconds, while
the completed 35-minute hold has a supplemental native PASS at 2.489 seconds
(raw viewport FAIL retained). These native successes and lifecycle negatives are
not substitutes for the separately scoped production retrieval evidence.

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

Coordinator reports the full acceptance gate passed at `d9279cd` with
`GOMAXPROCS=4`:

```sh
make typecheck lint build test vuln deps-check schema-check tidy-check docs-check
```

All four package checks passed, 18 experiment tests passed, conformance was
48 PASS / 0 FAIL / C-14 slow default SKIP, and source/binary vulnerability scans
found none. Pinned ShellCheck 0.9.0 passed at `2746e55`, with no shell/bootstrap
diff afterward per coordinator. These are reported checks, not this documentation
worker repeating the runtime gate. See the [sanitized production evidence](../../docs/experiments/grok-participation-evidence/production-checkpoint.json).

Next: independent Rjae Fable report-only review, then coordinator-owned PR and
shipping steps. Linux native behavior and the full-package 35-minute hold remain
unmeasured limitations. This is no blanket all-green, review approval, release or
card-completion claim. Historical failure records and per-case revisions remain
intact; no raw fixture/auth/session artifacts are part of this update.
