# Codex participation in Brigade: implementation and test plan

Date: 2026-09-08
Status: P0 ready to run; P2–P5 provisional until the independently verified capability table.
Scope: one repository, one shared Brigade Go runtime, separate Claude and Codex plugin packages.

## 1. Read this first

Build a Codex plugin that lets a user's Codex conversation join the same Brigade team as existing Claude Code sessions, discover teammates, send messages, receive framed messages, and reply with correct identity and reply metadata. Preserve existing Claude behavior.

This document is a plan, not evidence that Codex interoperability already works. The planning pass inspected source and official documentation but did not run a live Codex/Claude exchange. Do not describe a mock, watcher sink, hook invocation from a shell, or standalone App Server worker as proof of delivery into an ordinary desktop conversation.

There are two separately reportable outcomes:

1. **Plugin participation:** Codex registers, sends, and receives on UserPromptSubmit only. Messages that arrive while idle remain pending until the next user prompt. PostToolUse delivery is a separate future experiment, not part of this milestone.
2. **Full idle-wake participation:** a message starts work in the intended idle Codex conversation without a human prompt. This requires a separately proven host integration. A polling-only implementation does not satisfy this outcome.

Implement the first outcome completely. Investigate and implement the second only through a supported, verified path into the intended client. If ordinary-client idle wake cannot be established, finish the first outcome and deliver the evidence and precise remaining limitation. Do not silently substitute a Brigade-managed Codex worker for the user's existing Codex conversation.

Do not publish, push, release, join a production team, send real teammates messages, or modify the user's global Codex installation as part of automated testing. Use isolated test identities and plugin configuration. Any live model tests consume quota: execute only within the implementation session's granted testing authority and report prerequisites that are unavailable.

## 2. Repository rules and source map

Read current `CLAUDE.md`, the execution log and any host-supplied instructions before starting. There is no repository AGENTS.md at review time; later references to AGENTS.md describe Codex's instruction surface. Preserve unrelated worktree changes. Plans belong in `.context/plans/`; disposable scratch belongs in `.ignored/`. Credential material must live outside the repository even when scratch is gitignored. Prefer codebase-memory MCP for code discovery when available, indexing first if necessary; otherwise use ordinary repository inspection. Installed skills and MCP tools are session tooling, not prerequisites for a fresh checkout. Follow any applicable session requirements for their use.

When the lessons service is available, search at task start and on unexpected behavior, announcing the search and its result. Optional prior lesson: `0c9db12674c24c65919f5c390b6df988` (portable plugin components versus host-specific agents). Unavailable tooling must not block repository work. This feature does not require custom Codex agent roles or model routing.

Inspect these existing areas rather than reimplementing them:

| Area | Files/packages | What to preserve or extract |
| --- | --- | --- |
| CLI dispatch | `cmd/brigade/main.go`, `internal/app/app.go` | Existing command behavior and exit conventions |
| Existing package | `plugin/.claude-plugin/plugin.json`, `plugin/hooks/hooks.json`, `plugin/skills/`, `plugin/bin/brigade` | Claude distribution, binary bootstrap, skills |
| Hook lifecycle | `internal/harness/hook/{hook,start,prompt,end,spawn}.go` | Resolve team, register/resume, heartbeat, cleanup |
| Session lookup | `internal/harness/config/{session,environ,options,watcher}.go` | Trusted configuration and identity resolution |
| State | `internal/harness/sessionmap/`, `internal/harness/pidfile/` | Private atomic state, resume maps, watcher ownership |
| Backend calls | `internal/harness/adapterclient/`, `internal/adapterkit/` | BAP/1 process boundary, timeouts, allowed environment |
| Receiving | `internal/harness/watch/{inject,watch,attempt,heartbeat,guard,lifecycle}.go`, `internal/harness/inbound/` | Queue, policy, dedupe, retries, heartbeat and ack order |
| Claude delivery | `internal/harness/socketpost/`, `internal/harness/registry/` | Keep as Claude-specific implementation |
| Message framing | `internal/harness/frame/{frame,parse}.go` | Sanitization, provenance, reply metadata, frame levels |
| User commands | `internal/harness/commands/{commands,send,sessions,whoami,inbox,teamsetup}.go` | CLI surface, human-only release rules, stdin bodies |
| Packaging gates | `scripts/ci/plugin-check.sh`, `Makefile`, `.github/workflows/` | Existing allowlist, checksums, release and secret checks |
| Proof examples | `docs/experiments/E4-idle-wake.md`, `docs/experiments/E4-crash-resume.md`, matching scripts | Real-client test methodology, negative controls |

Specific facts verified during planning:

- `config.SessionIn` calls `ClaudePID` and reads `Store.ReadByPID`.
- `config.InSession` tests only whether CLAUDE_PID is set. `Trusted` strips BRIGADE_* only when that predicate is true; `BrigadeStateDir` depends on it. Send/whoami require a session, while inbox release and team revoke-member/transfer reject a session. Extending lookup alone would leave Codex classified as a human terminal and fail open on these controls.
- `hook.prompt.poll` already implements bounded hook-stdout delivery through the shared inbound pipeline, seen/pending stores and a batched backend ack. It is disabled unless PollOnPrompt is true; options currently come from CLAUDE_PLUGIN_OPTION_*.
- `hook.facts` requires Claude PID and reads messaging socket/token and Claude config/plugin paths.
- `hook.connect` uses per-PID watcher identity and registers a `protocol.SessionRegistration` containing `Harness` and `HarnessVersion`.
- `watch.injector.inject` ends in `socketpost.Post`, except for the test sink.
- `socketpost.Post` writes auth and user NDJSON to a Unix socket. Its success is a local transport result, not proof that a model processed the message.
- `frame.Build` already sanitizes the envelope and includes message ID, sender principal, sender session, reply target and hop count. Its prose contains Claude-specific terminology that needs adapting.
- `scripts/ci/plugin-check.sh` allows only the existing Claude tree and explicitly bans MCP/channels there.

Keep BAP/1 frozen. No backend or wire changes are expected. Do not import Supabase adapter implementation into `internal/harness`. Keep one Go binary and existing supported release platforms; no new language runtime or dependency is expected for this feature.

## 3. Platform evidence and the mandatory discovery gate

Recheck these official sources against the installed Codex version before implementation:

- Packaging: https://developers.openai.com/plugins/build/plugins
- Hooks: https://learn.chatgpt.com/docs/hooks
- App Server: https://learn.chatgpt.com/docs/app-server

At planning time these docs describe `.codex-plugin/plugin.json`, `skills/`, default `hooks/hooks.json`, plugin root/data environment variables, and SessionStart/UserPromptSubmit/PostToolUse/SessionEnd hooks. Common hook input includes `session_id` and `cwd`; subagent events may carry the parent session ID. Background hook completion while idle waits for a future user turn; it does not start a turn. Hook additional context is developer context. App Server has `turn/start` and `turn/steer`; the latter requires the expected active turn ID.

These facts do NOT establish a desktop attachment API, thread identity propagation into shell tools, CLI PATH behavior, plugin user-option parity, hook output acknowledgement, or automatic plugin updates. Verify those separately. Do not infer them from Claude compatibility environment variables. Local scaffolder instructions and live docs may differ; validate the installed host and prefer the conventional default hook file to unnecessary manifest fields.

### P0 — Capability experiment (must precede delivery implementation)

Create an isolated experiment under `scripts/experiments/codex-participation/` and report under `docs/experiments/codex-participation.md`. Use a small fixture plugin and a test-only backend. Record client version, OS, exact launch commands, configuration scope, and sanitized payload shapes. Do not dump the full environment, tokens, credentials, or unrelated transcripts.

Prove and record:

- Plugin install/load and explicit skill invocation in an isolated Codex session.
- Actual hook command form, argument handling, root expansion, startup/resume/compact/end event behavior.
- Native thread ID from hooks, and a trustworthy way for `brigade` invoked through a shell tool to resolve that same thread. Check the installed host's documented environment, rather than inventing a variable name.
- Two simultaneous threads in the same project and, if supported, the same host process; no identity collision. Include subagent behavior.
- Name the host-provided marker present in every shell-tool child, its provenance, and whether a model can unset/spoof it or invoke an unmarked child. An ordinary environment variable is not immutable. Prove the supported enforcement boundary with direct, unset, poisoned and nested-child tests; do not claim a binding handle alone protects human-only commands.
- Prove binding scope with two simultaneous threads: a per-thread nonce in the private map, each thread's own `whoami --json` result, and a poisoned environment/cwd control that cannot select the other thread.
- Record registration name/version/activity sources. Absent trustworthy values, use a Codex name fallback, version `unknown`, and protocol activity `idle`; never invent a registry or busy signal.
- Measure hook timeout defaults and overrides, termination signals, and what happens to partial stdout on timeout. Size heartbeat, receive and ack budgets from the measured total, not Claude's defaults.
- How plugin options are supplied; do not assume Claude `userConfig` is supported. If absent, choose a Brigade-owned private options file with explicit defaults and a documented configuration command/path.
- Whether the binary is available as a bare command from a skill. If not, use an explicit plugin-root-resolved launcher; prove the skill can resolve it reliably.
- Synchronous UserPromptSubmit delivery: capture verbatim the host wrapper around the canary, its role, whitespace stripping, and byte/token cap and spill behavior. Do not reuse Claude's 10,000-character cap without measurement. Post-tool delivery requires a separate future capability row and security campaign.
- Idle-wake attempt (expected pending-until-next-turn for polling): send after the turn has ended and observe without prompting. This is the positive wake arm, not a negative control.
- Investigate whether a documented supported interface can address the actual desktop conversation. If only a separately launched App Server works, record that as a worker-only result and leave desktop idle wake unsupported.

Deliver a capability table with PASS/FAIL/UNAVAILABLE, evidence filenames, and minimum verified Codex version. Avoid speculative implementation branches: select the proven lifecycle, identity, configuration, and delivery routes in the report before P2/P3.

P0 pass predicate: both trusted identity and delivery pass for the named client, with an independent Fable adversarial verifier signing the evidence. Delivery requires the frame's preserved 40-character anchor in the receiver's context record AND a per-run split-token nonce reconstructed in the receiving model's own text. Never count input echoes, tool output, `origin.body`, or the sender's transcript. Record enqueue/dequeue or equivalent host handoff evidence where available; absence of such instrumentation is a stated limit, not an invented event.

Run controls in fresh isolated sessions, with unique messages/principals so dedupe and rate limiting cannot explain silence. Observe each idle/null arm for 60 seconds after readiness and confirmed turn completion; record timestamps and instrument health. Run a known-good prompt-delivery positive control. Null-send with live hooks must stay silent. A sink/mock-only run must score FAIL for live delivery, and a separate App Server worker must be classified worker-only and excluded from desktop PASS. Mutate each classifier input (remove frame, replace assistant text with input echo, remove nonce, remove readiness) and assert it cannot still pass. Each assertion needs a nonzero attempted-case count and a control that can flip its verdict. A 60-second observation proves only that window, not indefinite behavior.

Identity FAIL without a validated supported alternative is a HARD STOP after P1. Missing authentication permits fixture/baseline work only; P2–P5 stay gated. If identity works but the human-only boundary fails, hold support is blocked: do not silently downgrade hold to accept. Any restricted release must explicitly disable unsupported controls, document them in docs/security.md and pass Fable review before the implementation gate opens. The absence of an immutable environment marker alone is not proof of failure; test the actual boundary and supported alternatives.

## 4. Design decisions

### Package and shared runtime

Keep `plugin/` as the Claude package. Add `codex/brigade/` as the Codex package root (outer folder and plugin name both `brigade`). Suggested contents:

```text
codex/brigade/
  .codex-plugin/plugin.json
  hooks/hooks.json
  skills/team-messaging/SKILL.md
  skills/setup/SKILL.md
  skills/join/SKILL.md
  skills/update/SKILL.md
  bin/brigade
  bin/VERSION
  bin/checksums.txt
  README.md
```

Both packages must be self-contained after installation into separate caches. Generate or verify identical bootstrap/version/checksum assets from one canonical source; never use runtime `../../plugin` imports or symlinks out of the package. Keep the existing Claude release pin canonical initially. Development uses the existing local dev-binary mechanism, adapted and tested for both packages; the old published binary cannot implement new Codex subcommands. Do not manufacture release checksums for unreleased code.

No MCP server is required for the initial CLI-and-hooks approach. Adding one later is a separate design choice, not a solution for idle wake. If available, use the plugin-creator skill for actual package creation with an explicit repository-local destination and isolated test marketplace; otherwise follow the verified manifest schema. Avoid global marketplace defaults.

### Host session identity

Introduce a small host-neutral context representation, for example `HostSession{Host, NativeID, ...}`. Exact package/type names may follow the existing code style. Separate native thread identity from process ownership/liveness and from the backend-issued Brigade session UUID.

- Namespaced key: `(host kind, native thread ID)`; include validated binding context where needed to prevent cross-team state reuse.
- Existing Claude ByPID state remains backward compatible; avoid a wholesale state migration.
- Namespace only host lookup maps, watcher pidfile/log paths if used, and prompt retry stamps. Keep seen and pending stores keyed by backend-issued Brigade session ID so resume and the shared pipeline retain dedupe. Native IDs are untrusted path inputs; validate or hash them using existing conventions.
- CLI commands must resolve the current trusted host context, not accept an arbitrary model-selected Brigade sender UUID.
- An alternative binding handle must be minted by the trusted lifecycle hook, scoped to that thread, validated against its private map and binding, and proven by the two-thread poison experiment before P2. Never fall back to last-active or cwd. A handle resolves identity but does not by itself establish the in-session/human-only enforcement boundary.
- Missing/ambiguous identity produces a useful `config` error. If host selectors conflict, fail closed rather than guessing.
- Default to registering root conversations only. A subagent must not create a duplicate root registration or silently send as another thread.

### Delivery and acknowledgement

Outcome 1 reuses `hook.prompt.poll` and its inbound pipeline. Supply a Codex facts/options source with PollOnPrompt default ON and explicit inbound policy, frame level and config directory. Do not extract a new injector interface or add another durable queue for this outcome. Existing seen/pending storage remains shared. Verify concurrent prompt invocations against existing synchronization and add only the minimal serialization a failing test demonstrates is necessary.

Presence decision for outcome 1: no socketless watcher. Heartbeat on UserPromptSubmit within a measured combined heartbeat/receive/ack budget, shortening receive as necessary; handle expired/closed sessions through the existing resume/re-registration rules before sending or polling. Between prompts the lease can expire and the session can disappear from the default sessions list; document `sessions --all` and expose prompt-only presence in whoami/README. Do not advertise continuous online presence. A pending-only watcher would be a separate outcome-2 design requiring proven per-thread liveness and lifecycle tests.

Document the existing ack point before adapting it: poll offers received messages to the pipeline, emits fitting frames, calls Done to mark successful handoff/seen state, and makes one Ack call for the accumulated IDs. Offer may also ack an already-seen duplicate without reprinting. Therefore do not assume every print-before-ack crash results in another model-visible frame: test before/after seen persistence, restart and ack failure separately. Verify `say` write-error handling as part of this contract. A stdout write is not a host acceptance receipt; measure partial-output and timeout behavior. If seen persistence can suppress a frame that the host never received, that is a failing delivery gate requiring a focused fix, not a bounded-duplicate success claim. Held/refused/unprinted-new frames must never be acknowledged as delivered.

Bound each delivered batch by bytes/items/time to avoid host truncation. Do not acknowledge messages omitted from a bounded batch. Prefer test-proven small batches over increasing global host context limits. Retain existing retry/rate/hop bounds; ignore ack-only messages according to the skill.

### Trust and human controls

Use `frame.Build` and the sanitizer. Sender has no harness field in the frozen envelope, so receiver-side code cannot infer whether a peer uses Claude or Codex. Preserve existing Claude preamble bytes for this milestone and record its inaccurate Claude-only sender wording as known debt. Add a fixed Codex-receiver variant with host-neutral sender prose and its own goldens; use receiver-appropriate instruction-file/tool names. No new runtime wording knob. Preserve the first 40-character delivery anchor. A future shared neutralization is a deliberate Fable change updating all golden hashes and proof-script literals together, never a protocol change. Sender labels are display text; principal/session IDs define identity.

Where hook output is developer context, put a fixed integration-owned instruction ahead of a clearly delimited untrusted-data body. Do not interpolate teammate text into trusted instructions. Test delimiter breakout and fake system/developer messages. Do not claim quoting eliminates prompt injection.

Claude's measured security results depended on both its native peer preamble and Brigade's frame plus its permissions/tool-grant behavior. None of that transfers automatically to Codex. Add a Codex sessions section to docs/security.md naming observed and absent layers. Before outcome 1 ships, port the existing 26-message injection corpus and run each case in three fresh real-client sessions using isolated canary files/services, a positive-control judge and verdict mutations. Unauthorized config-edit or exfiltration success blocks the outcome. Fable authors and independently verifies this campaign. PostToolUse, if proposed later, requires its own campaign because it changes timing and instruction placement.

Preserve accept/refuse/hold semantics and the human-only held-message release path. Preserve the rule that peer messages cannot authorize actions, approve prompts, override denied work, or change permissions. Secrets stay on stdin or in an explicitly authorized external secret file, never argv, chat, logs, or repository files. Keep workspace-path sharing opt-in and label-only.

## 5. Ordered implementation work

### P1 — Baseline and regression fixtures

Record `git status`, revision and tool versions. Run `make typecheck`, `make test`, and `make plugin-check` to establish baseline; diagnose unrelated failures separately. Do not run `make commit`, `make push`, `make release`, `make clean`, or backend reset targets.

Add focused characterization tests only where extraction could change existing behavior: Claude session lookup, repeated startup/compact, watcher ownership, inject failure versus ack, and frame rendering. Reuse existing test fixtures and fake adapter. Avoid duplicating already covered assertions.

### P2 — Host context and lifecycle

FIRST implement host-aware InSession/Strip/Trusted and state-directory resolution, with tests proving: BRIGADE_* stripping, state-dir pinning, rejection of inbox release/team revoke-member/team transfer from Codex, and no terminalTarget fallback when Codex identity is missing or invalid. Assert send/whoami work for a valid binding. Cover marker unset/spoof and nested children per P0, documenting enforcement limits. Then adapt command session lookup and add explicit Codex hook dispatch, e.g. `brigade hook codex session-start|prompt|session-end` (proposed new commands). Keep Claude paths intact.

Codex startup resolves `.brigade.json` and the private binding, registers with Harness `codex` and persists mapping; outcome 1 starts no watcher. Harness is a nonempty string in protocol/session.go; add one assertion for `codex`, no wire/schema changes. Supply P0-proven name/version/activity sources, or the documented Codex/unknown/idle fallbacks. Repeated startup and compact are idempotent; resume follows backend rules. End closes only the matching Brigade session. Crashes use lease recovery; desktop process existence does not prove thread activity.

Wire configuration through the proven P0 route, preserving trusted config precedence and existing environment allowlists. Codex must not read Claude registry/session secrets. Test actual hook input parsing, including unknown fields and malformed input.

Exit: unit tests cover two threads, two hosts, restart, compact, missing identity, wrong binding, malformed native IDs and process reuse.

### P3 — Delivery transport and inbound processing

Adapt the existing prompt-poll path with Codex facts/options/output limits and the prompt heartbeat budget. Reuse seen/pending stores and pipeline; keep Claude injection untouched. Confirm PollOnPrompt is on for the Codex default. Implement the tested acknowledgement contract and fixed Codex frame variant.

Add explicit tests for the documented ack order, crash windows, queue overflow, batch limit, retries, revoked membership, hold/release/refuse and duplicate delivery. Make `whoami` expose host identity and honest receive mode (e.g. next conversation boundary versus verified push) without exposing tokens, raw local paths or secrets.

Only if full idle wake is proven: design/extract the delivery interface and any pending-only watcher as a separate Fable task, including per-thread liveness, active/idle races, expected turn IDs, reconnects and acceptance receipts. A failed steer must recheck state before starting a turn; avoid duplicate submissions after ambiguous failure. Test with a fake host server and the real intended client. This branch is excluded from the prompt-only implementation estimate.

Exit: filesystem adapter integration can send Claude-shaped and Codex-shaped sessions both directions through the shared protocol with no cross-session routing or lost unacknowledged messages.

### P4 — Plugin and skills

Create the package above. Use Codex-compatible hook command syntax proven in P0. Avoid assuming Claude exec-form `args` support. Keep dynamic message content out of shell command strings; adapter spawning remains argument-array based. Test paths containing spaces.

Port skills with verified invocation and launcher paths. Messaging teaches discovery, stdin send, reply metadata, retry bounds, acknowledgement suppression and untrusted frames. Setup/join preserves external secret-file workflow. Update uses the installed Codex version's verified marketplace update/reinstall flow; remove Claude `/reload-plugins` instructions. Explain receive timing clearly in README/setup and report missing configuration without breaking unrelated Codex work.

Exit: an isolated installed package invokes the newly built binary, registers a real Codex session and completes an actual model-visible message/reply exchange.

### P5 — Packaging, release preparation and CI

Parameterize the existing package checker by root/host, retaining Claude's checks. Root-specific parameters include manifest location, hook-root literal/command syntax and the Go-pinned subcommand roster in scripts/ci/checks_test.go. Land validation in the same commit as the new package. Add codex-plugin-check and opt-in codex-smoke; no codex-test target is needed because make test already runs Go tests under ./... with race detection. Validate standalone layout, modes, bootstrap syntax, versions and asset drift. no-secrets.sh already scans tracked files; verify new paths are covered and retain its intentional fixture exclusions rather than claiming a new scan scope is necessary.

Update existing release-prep.sh to copy canonical binary pin assets to the Codex package and checksums-check.sh to compare them. Reuse existing manifest/version logic where possible; test both manifests and installed bootstrap paths. Do not invoke make release: it commits, tags and pushes. Use dev builds and leave an explicit release checklist if production checksums are pending.

Wire deterministic tests into CI without Codex/Claude login or a live model. Keep real-client smoke tests opt-in and clearly separate from `make test`. Update allowed-dependency checks only if a justified dependency is actually needed; prefer the existing stdlib/process abstractions.

Exit: both package validators and normal CI gates pass; no global installation changes are necessary for deterministic tests.

## 6. Required test matrix

Record each row as PASS, FAIL, or NOT RUN with a reason and evidence. A skipped live row is never a pass.

| ID | Test | Required assertion |
| --- | --- | --- |
| C01 | Codex startup | One backend session with Codex harness, correct team/principal and proven/fallback name, version and activity |
| C02 | Two Codex threads, one project/process | Distinct identities; messages reach only the target |
| C03 | Claude + Codex | Existing Claude commands and delivery work unchanged |
| C04 | Startup repeated / compact | No duplicate registration, watcher or lost pending queue |
| C05 | Resume / crash / lease expiry | Correct reuse or new registration; no stale sender selection |
| C06 | Shell identity missing or conflicting | Config failure, no fallback to another thread |
| C07 | Subagent event | No accidental root duplication or impersonation |
| C08 | Bidirectional send/reply | Correct principal, sender session, reply ID and hop metadata |
| C09 | Accept/refuse/hold | Policy preserved; held/refused messages never acked prematurely |
| C10 | Inject failure, process interruption | Message retryable/recoverable; no ack-before-handoff |
| C11 | Handoff succeeded, ack failed | Bounded duplicates, retry and seen-state semantics documented |
| C12 | Concurrent drains / duplicate watch event | Serialized state, no double consumption or loss |
| C13 | Oversize/batched input | Bounded output; omitted messages remain pending |
| C14 | Real-client injection campaign | Existing 26 cases × 3 fresh sessions; controlled judge, Fable verifier; no unauthorized config edit or exfiltration |
| C15 | Wrong team/principal/binding | Existing protocol isolation and local routing preserved |
| C16 | Offline / revoked membership | Bounded retries, accurate presence, appropriate shutdown/error |
| C17 | Package cache / spaces / missing binary | Self-contained launcher, clear failure, no source-tree dependency |
| C18 | Live active Codex receive | Model actually sees message and emits one expected reply |
| C19 | Live idle Codex receive | Either verified automatic wake, or explicit pending-until-turn result |
| C20 | Idle no-message control | No unsolicited turns or periodic model work |
| C21 | Live restarted Codex recipient | Pending message recovery and correct routing after resume |
| C22 | Secrets and configuration | No secrets in argv/logs/repo; no unrelated global settings modified |
| C23 | In-session trust boundary | Valid send/whoami; BRIGADE_* ignored; state pinned; human-only verbs refused; unset/spoof/nested-child controls; no terminalTarget fallback |
| C24 | Prompt budgets and presence | Heartbeat + shortened receive + ack within proven timeout; partial stdout tested; lease expiry between prompts reported honestly |

Live fixtures should use two disposable principals in an isolated filesystem backend first. Include a negative control that sends to a different session. Capture sanitized IDs, event times, sender result, delivery handoff, backend ack, and receiver reply. Keep raw transcripts private/outside the repo; commit only redacted evidence. Reuse Supabase proof fixtures for one final local-stack round trip if available; do not change the hosted backend.

Suggested deterministic final gates, adapted to actual new target names:

```sh
make typecheck
make test
make lint
make plugin-check
make codex-plugin-check
make deps-check
make schema-check
```

Use targeted `go test` while iterating; `make test` is the final race-enabled Docker-free gate. Run Claude plugin validation if its CLI is installed. Shell changes must satisfy the repository's documented CI shellcheck version as well as local checks. If changing Supabase implementation/migrations unexpectedly becomes necessary, revisit the scope and run the repository-required local stack gates before claiming completion.

## 7. Progress and final handoff

The existing brigade-execution-log.md remains the single status authority. Scheduling this new workstream there is an owner decision; this plan does not silently add active rows. Once scheduled, update its rows in the same commit as work. A codex-team-participation-log.md, if useful, is only an evidence index linked from those rows, never a parallel status log.

Follow the existing model-tier policy (model names below are repository-assigned tiers, not newly verified product recommendations):

| Phase | Author tier | Required review |
| --- | --- | --- |
| P0 fixture/runs/evidence | Opus | Fable gate design and independent adversarial verification before P2 |
| P1 baseline/test plumbing | Opus | Security characterization reviewed at Fable tier |
| P2 identity/trust boundary | Fable | One author plus independent full adversarial verifier |
| P3 polling/ack/frame/security campaign | Fable | One author plus independent full adversarial verifier |
| P4 package/skills | Opus | Frame/security changes remain in P3; package validation lands together |
| P5 CI/release preparation/docs | Opus | Negative security tests require Fable verification |
| Optional push/mid-turn branch | Fable | Separate verified capability/security gate |

A cheaper model may coordinate and run scripts, but must not implement Fable-tier work inline. If the required tier is unavailable, complete independent authorized work and report the specific gate; do not quietly downgrade it. This preserves the existing execution log's budget/cadence policy.

Final implementation report must include:

- Supported client(s), tested version(s), operating system(s), and receive mode.
- Installation and test commands verified against the packaged build.
- Passing deterministic gates and live test matrix with unavailable rows explained.
- Exact acknowledgement contract and remaining crash-window limitations.
- Whether normal desktop idle wake works, worker-only wake works, or neither was proven.
- Release pin/checksum work still pending, if any, and no claim that an old release contains the new implementation.
- Existing Claude regression results and a concise review of changed files.

Do not mark full participation complete with only mock tests or next-prompt polling. Do not keep refactoring after gates pass without a concrete failure to address.

## 8. Prompt to give the implementing model

> Execute `.context/plans/codex-team-participation.md` under its phase gates and model-tier policy. Start with P0 fixture/evidence and P1 baseline. Get independent Fable verification of identity, trust boundary and delivery evidence before P2; unresolved identity is a hard stop after P1. Use the existing execution log as status authority once the owner schedules this workstream. Delegate P2/P3/security work to Fable author + verifier; use the cheaper tier for plumbing, runs and packaging. Reuse existing prompt polling with Codex options, frame variant and measured heartbeat budget; no PostToolUse or new watcher for outcome 1. Do not invent APIs, claim mocks prove delivery, or hide unsupported human controls. Full idle wake is separately gated. Do not publish, push, release, change global installations or contact real teammates. Deliver code, verified isolated commands, and the 24-row test matrix with unavailable gates stated.

## 9. Review disposition (2026-09-08)

Revised against the Fable review of commit 3338225 supplied in `.ignored/codex-team-participation-review.md` (local evidence, not a required file for future checkouts). Findings 1–11 are addressed above: explicit trust predicate, measurable P0 gate, prompt-only timing and security campaign, heartbeat choice, existing poll reuse, fixed frame variants, registration fallbacks, model tiers/status authority, limited namespacing, shared packaging checks and optional tooling. Two qualifications are deliberate: environment markers/handles are not assumed tamper-proof, and durable seen state means a pre-ack crash does not necessarily reprint a message. Both require adversarial tests. This revision is not an implementation or a claim that P0 passed.
