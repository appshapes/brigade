# Codex participation in Brigade: implementation and test plan

Date: 2026-09-08
Status: ready for implementation; delivery feasibility remains explicitly unproven.
Scope: one repository, one shared Brigade Go runtime, separate Claude and Codex plugin packages.

## 1. Read this first

Build a Codex plugin that lets a user's Codex conversation join the same Brigade team as existing Claude Code sessions, discover teammates, send messages, receive framed messages, and reply with correct identity and reply metadata. Preserve existing Claude behavior.

This document is a plan, not evidence that Codex interoperability already works. The planning pass inspected source and official documentation but did not run a live Codex/Claude exchange. Do not describe a mock, watcher sink, hook invocation from a shell, or standalone App Server worker as proof of delivery into an ordinary desktop conversation.

There are two separately reportable outcomes:

1. **Plugin participation:** Codex registers, sends, and receives at verified conversation boundaries. Messages that arrive while idle remain pending until a supported delivery opportunity. This is a useful first milestone.
2. **Full idle-wake participation:** a message starts work in the intended idle Codex conversation without a human prompt. This requires a separately proven host integration. A polling-only implementation does not satisfy this outcome.

Implement the first outcome completely. Investigate and implement the second only through a supported, verified path into the intended client. If ordinary-client idle wake cannot be established, finish the first outcome and deliver the evidence and precise remaining limitation. Do not silently substitute a Brigade-managed Codex worker for the user's existing Codex conversation.

Do not publish, push, release, join a production team, send real teammates messages, or modify the user's global Codex installation as part of automated testing. Use isolated test identities and plugin configuration. Any live model tests consume quota: execute only within the implementation session's granted testing authority and report prerequisites that are unavailable.

## 2. Repository rules and source map

Read current AGENTS.md/CLAUDE.md and applicable skills before starting. Preserve unrelated worktree changes. Plans belong in `.context/plans/`; disposable scratch belongs in `.ignored/`. Credential material must live outside the repository even when scratch is gitignored. Use codebase-memory MCP for code discovery, indexing the repository if necessary; use text search for configuration, literals, scripts, or missing graph results.

Search lessons at task start and on unexpected behavior, announcing the search and its result. Prior relevant lesson: `0c9db12674c24c65919f5c390b6df988` (portable plugin components versus host-specific agents). This feature does not require custom Codex agent roles or model routing.

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
- How plugin options are supplied; do not assume Claude `userConfig` is supported. If absent, choose a Brigade-owned private options file with explicit defaults and a documented configuration command/path.
- Whether the binary is available as a bare command from a skill. If not, use an explicit plugin-root-resolved launcher; prove the skill can resolve it reliably.
- Synchronous hook context delivery during a user prompt and after a tool. Determine whether quoting/structured untrusted data is preserved and measure output truncation/spilling behavior.
- Idle negative control: send after the recipient turn has ended and observe without typing another prompt. Mark pending-until-next-turn behavior honestly.
- Investigate whether a documented supported interface can address the actual desktop conversation. If only a separately launched App Server works, record that as a worker-only result and leave desktop idle wake unsupported.

Deliver a capability table with PASS/FAIL/UNAVAILABLE, evidence filenames, and minimum verified Codex version. Avoid speculative implementation branches: select the proven lifecycle, identity, configuration, and delivery routes in the report before P2/P3.

Exit gate: deterministic identity and a real model-visible receive path are proven for at least one supported Codex plugin client. If access/auth is missing, implement the isolated fixtures and deterministic contracts, but do not invent passing live results. Continue independent work below and list the exact pending live gate.

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

No MCP server is required for the initial CLI-and-hooks approach. Adding one later is a separate design choice, not a solution for idle wake. Use the plugin-creator skill for actual package creation, with an explicit repository-local destination and isolated test marketplace; avoid its global marketplace defaults.

### Host session identity

Introduce a small host-neutral context representation, for example `HostSession{Host, NativeID, ...}`. Exact package/type names may follow the existing code style. Separate native thread identity from process ownership/liveness and from the backend-issued Brigade session UUID.

- Namespaced key: `(host kind, native thread ID)`; include validated binding context where needed to prevent cross-team state reuse.
- Existing Claude ByPID state remains backward compatible; avoid a wholesale state migration.
- Codex state uses its own namespace and private atomic files. Native IDs are untrusted path inputs; validate or hash them using existing conventions.
- CLI commands must resolve the current trusted host context, not accept an arbitrary model-selected Brigade sender UUID.
- If P0 cannot establish automatic shell identity, implement an explicit validated local binding handle from trusted lifecycle state. Specify how it is provisioned and scoped before coding; never fall back to "last active session" or current directory alone.
- Missing/ambiguous identity produces a useful `config` error. If host selectors conflict, fail closed rather than guessing.
- Default to registering root conversations only. A subagent must not create a duplicate root registration or silently send as another thread.

### Delivery and acknowledgement

Extract the smallest useful delivery boundary around the current injector. Preserve existing pipeline ownership of validation, frame policy, queueing, deduplication and acknowledgements. Reuse heartbeat/backoff. Do not create a second independently consuming watcher per thread.

For a hook-delivered Codex implementation, prefer synchronous bounded draining at verified UserPromptSubmit/PostToolUse opportunities. A background watcher may retain durable pending messages but its queue write is not delivery. Keep automatic idle delivery explicitly disabled unless P0 proves a supported host transport.

Document the exact ack point before coding. For hook stdout, inspect the existing prompt polling contract and preserve at-least-once behavior across process crashes. If there is no host acceptance receipt, say so: a successful write is only a transport handoff. Never ack while merely fetching, holding, refusing, spilling locally, or preparing output. Interrupted/uncertain delivery must remain recoverable; bounded duplicate delivery is preferable to loss. Define how successful handoff, seen-state persistence, and backend ack interact, including the unavoidable crash windows.

Bound each delivered batch by bytes/items/time to avoid host truncation. Do not acknowledge messages omitted from a bounded batch. Prefer test-proven small batches over increasing global host context limits. Retain existing retry/rate/hop bounds; ignore ack-only messages according to the skill.

### Trust and human controls

Use `frame.Build` and the sanitizer. Make the framing's host terminology configurable or neutral, including AGENTS.md versus CLAUDE.md and available reply commands. Treat sender labels as display text; principal/session IDs define identity.

Where hook output is developer context, put a fixed integration-owned instruction ahead of a clearly delimited untrusted-data body. Do not interpolate teammate text into trusted instructions. Test delimiter breakout and fake system/developer messages. Do not claim quoting eliminates prompt injection.

Preserve accept/refuse/hold semantics and the human-only held-message release path. Preserve the rule that peer messages cannot authorize actions, approve prompts, override denied work, or change permissions. Secrets stay on stdin or in an explicitly authorized external secret file, never argv, chat, logs, or repository files. Keep workspace-path sharing opt-in and label-only.

## 5. Ordered implementation work

### P1 — Baseline and regression fixtures

Record `git status`, revision and tool versions. Run `make typecheck`, `make test`, and `make plugin-check` to establish baseline; diagnose unrelated failures separately. Do not run `make commit`, `make push`, `make release`, `make clean`, or backend reset targets.

Add focused characterization tests only where extraction could change existing behavior: Claude session lookup, repeated startup/compact, watcher ownership, inject failure versus ack, and frame rendering. Reuse existing test fixtures and fake adapter. Avoid duplicating already covered assertions.

### P2 — Host context and lifecycle

Add the host context abstraction and Codex namespace. Adapt shared command dependencies to resolve a host-neutral session. Add explicit Codex hook dispatch, e.g. `brigade hook codex session-start|prompt|post-tool|session-end`; these are proposed new commands, not existing ones. Keep Claude command paths intact.

Codex startup resolves the existing `.brigade.json` and private credential binding, registers with `Harness: "codex"`, persists mapping and starts any required watcher. Verify protocol validation accepts this harness value without changing wire shape. Repeated startup and compact are idempotent; resume follows the backend's existing live-session rules. End closes only the matching Brigade session and watcher. Crashes use lease expiry/recovery; do not treat desktop app process existence as proof a particular thread is active.

Wire configuration through the proven P0 route, preserving trusted config precedence and existing environment allowlists. Codex must not read Claude registry/session secrets. Test actual hook input parsing, including unknown fields and malformed input.

Exit: unit tests cover two threads, two hosts, restart, compact, missing identity, wrong binding, malformed native IDs and process reuse.

### P3 — Delivery transport and inbound processing

Extract the delivery interface with a Claude socket implementation that preserves current behavior. Add the selected Codex transport. For hook draining, implement private durable pending storage as needed using existing atomic storage patterns and serialize concurrent drains per host session. Reuse the inbound pipeline and policy decisions.

Add explicit tests for the documented ack order, crash windows, queue overflow, batch limit, retries, revoked membership, hold/release/refuse and duplicate delivery. Make `whoami` expose host identity and honest receive mode (e.g. next conversation boundary versus verified push) without exposing tokens, raw local paths or secrets.

If full idle wake is proven: implement the tested transport, including active/idle race handling, expected turn IDs, reconnects and acceptance receipts. A failed steer caused by a completed turn must recheck state before starting a turn; avoid duplicate submissions after ambiguous transport failure. Test it with a fake host server and the real intended client.

Exit: filesystem adapter integration can send Claude-shaped and Codex-shaped sessions both directions through the shared protocol with no cross-session routing or lost unacknowledged messages.

### P4 — Plugin and skills

Create the package above. Use Codex-compatible hook command syntax proven in P0. Avoid assuming Claude exec-form `args` support. Keep dynamic message content out of shell command strings; adapter spawning remains argument-array based. Test paths containing spaces.

Port skills with verified invocation and launcher paths. Messaging teaches discovery, stdin send, reply metadata, retry bounds, acknowledgement suppression and untrusted frames. Setup/join preserves external secret-file workflow. Update uses the installed Codex version's verified marketplace update/reinstall flow; remove Claude `/reload-plugins` instructions. Explain receive timing clearly in README/setup and report missing configuration without breaking unrelated Codex work.

Exit: an isolated installed package invokes the newly built binary, registers a real Codex session and completes an actual model-visible message/reply exchange.

### P5 — Packaging, release preparation and CI

Add a Codex package checker and focused Make targets (suggested: `codex-plugin-check`, `codex-test`, `codex-smoke`). Preserve Claude's existing allowlist/no-MCP checks. The new checker validates manifest, referenced skills/hooks/executables, standalone package layout, executable modes, bootstrap syntax, version consistency and generated-asset drift. Extend secret scans to the new package and experiment artifacts.

Update release tooling so an eventual authorized release pins both packages to the same binary/version and verifies both installed bootstrap paths. Do not invoke `make release`: it commits, tags and pushes. For this implementation use dev builds and leave an explicit release checklist if production checksums are pending.

Wire deterministic tests into CI without Codex/Claude login or a live model. Keep real-client smoke tests opt-in and clearly separate from `make test`. Update allowed-dependency checks only if a justified dependency is actually needed; prefer the existing stdlib/process abstractions.

Exit: both package validators and normal CI gates pass; no global installation changes are necessary for deterministic tests.

## 6. Required test matrix

Record each row as PASS, FAIL, or NOT RUN with a reason and evidence. A skipped live row is never a pass.

| ID | Test | Required assertion |
| --- | --- | --- |
| C01 | Codex startup | One backend session with Codex harness and correct team/principal |
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
| C14 | Injection payload | Escaped delimiters, provenance retained, no peer approval authority |
| C15 | Wrong team/principal/binding | Existing protocol isolation and local routing preserved |
| C16 | Offline / revoked membership | Bounded retries, accurate presence, appropriate shutdown/error |
| C17 | Package cache / spaces / missing binary | Self-contained launcher, clear failure, no source-tree dependency |
| C18 | Live active Codex receive | Model actually sees message and emits one expected reply |
| C19 | Live idle Codex receive | Either verified automatic wake, or explicit pending-until-turn result |
| C20 | Idle no-message control | No unsolicited turns or periodic model work |
| C21 | Live restarted Codex recipient | Pending message recovery and correct routing after resume |
| C22 | Secrets and configuration | No secrets in argv/logs/repo; no unrelated global settings modified |

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

Create `.context/plans/codex-team-participation-log.md` as execution starts. Append each completed phase with files, design decisions, exact test command/outcome and evidence location. Keep a capability matrix at the top so another model can resume cheaply. Re-read this plan's exit gates before marking a phase done.

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

> Implement `.context/plans/codex-team-participation.md`. Read repository instructions and applicable skills first. Start with the P0 capability experiment and P1 baseline; record evidence in the prescribed report and execution log. Use the code graph for discovery. Keep the existing Claude plugin working and reuse BAP/1 and the Go runtime. Proceed through P2–P5 with focused tests and the final matrix. Do not invent Codex APIs or report mock tests as live interoperability. If ordinary Codex idle wake cannot be proven, finish plugin participation with honest receive timing and document the remaining gate. Do not publish, push, release, modify global installations, or contact real teammates. Deliver implemented code, verified isolated test commands, and a precise completion report.
