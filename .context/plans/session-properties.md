# Brigade session properties: model, context and usage limits

**Status.** Brief, v1, 2026-09-10. Written at Rjae's request; the source choice in §4 is Rjae's ruling of
2026-09-10. **The rulings in §7 are still open, and nothing in §5/§6 is to be started until they are answered.**

## 1. What was asked

Expand what `brigade sessions` reports, per session, with three properties:

1. **Model** — which model the session is running.
2. **Usage limit** — used and available.
3. **Context** — used and available.

## 2. What a session record carries today

`protocol.SessionRecord` (`internal/protocol/session.go:94-110`) is the whole of what `brigade sessions` can show:
`session_id`, `session_name`, `session_description`, `principal_ref`, `human_label`, `state`, `activity`,
`inbound`, `last_seen_at`, `lease_until`, `harness`, `harness_version`, `workspace_label`, `created_at`, `is_self`.
None of the three requested properties is present, and the harness has no path to any of them today.

Two existing constraints frame everything below.

- **The protocol is frozen.** `docs/protocol-v1.md:3-9`: changing any wire shape means changing
  `internal/protocol` (types and `Validate`), `docs/protocol-v1.schema.json` (`make schema`), the conformance
  suite and both bundled adapters **in one commit**. A change an existing conforming adapter would *fail* is a new
  major version — this one is not (see §5.2).
- **Threat model T10.** `docs/protocol-v1.md:315`: the registration has no member for a native session id, a
  working directory, a hostname, a username or a transcript path, and "adding one is a protocol change, not a
  convenience." `internal/harness/hook/hook.go:113-115` states the harness-side half: `transcript_path` and
  `prompt` are "deliberately absent from the type — they are never read, stored or sent."

## 3. Where the three properties actually live

Measured on Claude Code **2.1.267** on this machine, from four sources: the shipped binary's payload
constructors, Zod schemas and embedded doc strings; a live hook-capture probe; live transcript and registry
files; and `claude -p` output. Anything not marked measured is inference and is labelled as such.

### 3.1 Places that are *not* sources — each checked, each negative

| Candidate | Result |
| --- | --- |
| Session registry `$CLAUDE_CONFIG_DIR/sessions/<pid>.json` | **No model, no usage, no context.** Live entry carries `pid, sessionId, cwd, startedAt, procStart, version, peerProtocol, peerFeatures, kind, entrypoint, pidDomain, messagingSocketPath, name, nameSource, nameSince, status, updatedAt, statusUpdatedAt, bridgeSessionId`. This is the file the watcher already polls, so it was the cheapest possible source; it is empty for our purposes. |
| Environment variables | No model variable exists. `CLAUDE_EFFORT` is exported (measured: `xhigh`); nothing else is relevant. |
| Hook stdin payloads | Base fields are `session_id, transcript_path, cwd, prompt_id, permission_mode` (captured live on every event). **No usage, no cost, no context-window size on any event.** The only model-adjacent fields are `SessionStart.model?` — measured `undefined` on a fresh `startup`, populated only on resume/fork — and `Pre/PostModelSwitch`, which fire only on a switch. |
| Transcript, for usage limits | **Nothing.** Grepped a full transcript for `rate_limits\|five_hour\|seven_day\|context_window\|context_window_size`: zero hits. |
| Any on-disk cache of rate-limit state | **Does not exist.** Rate limits are held in memory only, populated from API response headers (`extractQuotaStatusFromHeaders` / `extractQuotaStatusFromError`). `.claude.json` carries `oauthAccount.organizationRateLimitTier` (here `default_claude_max_20x`) — the *tier*, never the consumption. |
| Any absolute quota figure | **Does not exist anywhere.** Usage limits are reported as percentages and reset times only. "Available" can only ever mean `100 - used_percentage`. |

### 3.2 The statusline payload — the only complete source

Delivered on stdin to the single command configured at `settings.json` → `statusLine.command`. Recovered from the
binary's constructor. Members relevant here:

```
model:          { id, display_name }
context_window: { total_input_tokens, total_output_tokens, context_window_size,
                  current_usage: { input_tokens, output_tokens,
                                   cache_creation_input_tokens, cache_read_input_tokens } | null,
                  used_percentage, remaining_percentage }
exceeds_200k_tokens
rate_limits?:   { five_hour?:   { used_percentage, resets_at },
                  seven_day?:   { used_percentage, resets_at },
                  spend_limit?: { used_percentage, resets_at } }
```

Also carried, and deliberately **not** used by this design: `session_id`, `transcript_path`, `cwd`, `workspace.*`
(including `repo:{host,owner,name}`), `cost.*`, `prompt_cache?`, `version`, `output_style`, `effort?`, `thinking`,
`fast_mode`, `vim?`, `agent?`, `remote?`.

Four measured facts that shape the design:

1. `context_window_size` is **the only file-free source of the context limit.** It is not in the transcript, the
   registry or `.claude.json`. It is resolved at runtime (`1e6` for 1M-capable models or when the
   `context-1m-2025-08-07` beta header is active, else the model's declared window), and is overridable by
   `CLAUDE_CODE_MAX_CONTEXT_TOKENS` — an override, not a report.
2. `used_percentage` is `round((input_tokens + cache_creation_input_tokens + cache_read_input_tokens) /
   context_window_size * 100)`, clamped 0–100.
3. `rate_limits` is **conditional**: the constructor spreads it only as
   `...(five_hour || seven_day || spend_limit) && {rate_limits}`, the percentages are `utilization * 100`,
   `resets_at` is **unix epoch seconds**, and `spend_limit` appears only when the auth source is `"gateway"`. The
   binary's embedded help says `five_hour` is "present only while the API reports it and its `resets_at` has not
   passed." On an API-key account the whole block can be absent.
4. **The statusline does not run in `-p` (headless) mode.** Measured: a probe configured a statusline command and
   no capture appeared, while all six hooks fired. This is the design's one permanent blind spot (§8).

### 3.3 The transcript — model and occupancy only

`$CLAUDE_CONFIG_DIR/projects/<slug>/<session-id>.jsonl`. An `attachment` record carries the full model identity
(`identity.modelId` = `claude-opus-5[1m]`, `marketingName` = `Opus 5 (1M context)`); per-turn `message.model`
carries the **bare** id with the `[1m]` suffix stripped. `message.usage` gives occupancy as
`input_tokens + cache_read_input_tokens + cache_creation_input_tokens`. It carries **no window size and no
rate-limit state** (§3.1).

### 3.4 Undocumented in-process surfaces — recorded, not used

Both exist and both work; both are rejected in §4.

- **Plugin JS hooks**: `$.session.usage()` returns
  `{context:{tokens,window,percent}, rateLimits:[{kind,percentUsed,resetsAt}], cost:{usd}}` live and in-process,
  alongside `$.session.model()`, `$.session.messages()`, `$.session.id()`, `$.session.turnCount()`. The only
  source that yields all three properties at once with no file and no transcript read.
- **`claude -p "/usage"`**: the `usage` slash command is marked `supportsNonInteractive: true` and was run for
  real; it returns account-wide state as free text (`Current session: 7% used · resets …`).

## 4. The decision, and why

**Ruled by Rjae, 2026-09-10: the statusline payload is the source.** The three candidates and the reasons:

| Route | Gets | Cost |
| --- | --- | --- |
| **Statusline** (chosen) | all three, including the context denominator | opt-in per user; no headless coverage |
| Transcript | model + occupancy only | needs a T10 exception; still cannot produce usage limits or a window size |
| Plugin JS hooks | all three, live, in-process | undocumented binary surface; collides with `make plugin-check`'s exec-form hook requirement |

Two reasons decided it.

1. **The transcript route cannot deliver two of the three properties**, so it does not implement the request. Usage
   limits are impossible from disk (§3.1) and the context denominator is absent (§3.3). It buys a model name and a
   numerator.
2. **The statusline route needs no T10 exception at all.** The read happens inside the user's own statusline
   script; the harness never touches `transcript_path`, so `hook.go`'s refusal stands untouched and the harness
   never parses the user's conversation. For a repository that freezes its protocol and pins its release bytes,
   getting the feature without reopening a ruling is worth the opt-in cost.

The JS hooks route is the cleanest engineering answer and the least durable one: it is undocumented surface on
2026-09-10's 2.1.267 that a point release can remove, and it would replace the plugin's exec-form command hooks
with a JS module. It is recorded here so the option is not rediscovered from scratch, and it is the route to
revisit if §7 (c) is ever answered "headless coverage is required".

## 5. Design

### 5.1 The cache — `brigade statusline`

A new subcommand reads the statusline payload on stdin, extracts **only** the members named in §5.2, and writes
them to a small JSON cache under `BRIGADE_STATE_DIR` keyed by pid — the same by-pid lookup the watcher already
uses for `sessions/<pid>.json`.

Non-negotiables, each from an existing rule:

- **The cache stores only the extracted fields.** The raw payload is never persisted. `cwd`, `workspace.*`,
  `repo.*`, `transcript_path`, `session_id` and `cost.*` are read past and dropped. This is what keeps T10 intact:
  no local-environment identity is stored by Brigade or sent to any adapter.
- **Nothing on stdout.** `CLAUDE.md` forbids writing to stdout from a command except protocol JSON/NDJSON or
  documented human output, and forbidigo enforces it. In the wrapper form (below) the wrapped command's stdout is
  passed through unmodified and Brigade adds nothing of its own.
- **Never spawn with a shell.** The wrapper form spawns an argument array with an allow-listed environment.
- **Not under the project directory.** The cache lives in the state dir.

Two wiring shapes, to be chosen in §7 (a):

- **Tee** — the user adds `brigade statusline` as a `tee` target in their own script. Simplest to document,
  requires the user to edit their script's pipeline.
- **Wrapper** — `brigade statusline -- <their real command>`: Brigade reads stdin, caches, forwards stdin to the
  child and passes the child's stdout through. One edit, cannot break the existing statusline, and is the better
  ergonomics for a plugin that wants to ship a working default.

Whether a **plugin** may supply `statusLine` itself is **unresolved** and is the one open measurement (§7 (d)).
Evidence leans no: the binary carries a settings-source gate map listing `statusLine:false` alongside
`plugins:true`. If a plugin cannot, every user wires this by hand and the columns are blank until they do.

### 5.2 The protocol members

Three optional, capability-gated members on `SessionRegistration`, `SessionRecord` and `HeartbeatRequest`,
following the `session.description` / `session.workspace_label` / `session.inbound` precedent exactly:

| Member | Shape | Capability |
| --- | --- | --- |
| `model` | string, e.g. `claude-opus-5[1m]` | `session.model` |
| `context` | `{used_tokens, window_tokens}` | `session.context` |
| `usage_limits` | `{five_hour?, seven_day?, spend_limit?}`, each `{used_percentage, resets_at}` | `session.usage_limits` |

Design notes:

- **Three capabilities, not one.** `model` and `context` describe a *session*; `usage_limits` describes a
  *person's account*, published to everyone on the team. It gets its own capability and its own opt-in (§7 (b)).
- **Store no derived values.** "Available" is `window_tokens - used_tokens` and `100 - used_percentage`, computed
  at display time.
- **`resets_at` is an RFC 3339 timestamp on the wire**, converted from the payload's unix epoch seconds.
- **This stays v1.** Unknown members are accepted and ignored (JSON convention 2, C-17), so no existing conforming
  adapter fails — the test `docs/protocol-v1.md:9` sets for a new major version is not met.
- **Every member is `omitzero`.** The json tag decides requiredness: `required_test.go` derives it purely from the
  presence of `omitzero`, so a bare tag would silently oblige every producer to emit the member.

### 5.3 Refresh and staleness

The watcher already polls the registry every 2 s (`DefaultPollInterval`) and heartbeats every 30 s
(`DefaultHeartbeatInterval`) — the carrier exists and needs no new cadence. The cache is read on the same path
that already calls `refreshRegistry`.

**The cache must be timestamped and stale values must be withheld.** The statusline is event-driven with a 300 ms
debounce, so an idle session stops updating it. That is harmless for `context`, which does not move while idle,
but `usage_limits` are **account-wide** and move while this session sits idle — a stale cache silently
under-reports someone's consumption, which is worse than reporting nothing. The setup docs should recommend
`refreshInterval: 30` on the `statusLine` config (the schema is `{type:"command", command, padding?,
refreshInterval?}`) to align it with the heartbeat.

### 5.4 Display

New columns in `internal/harness/commands/sessions.go`'s `columns(...)` call, absent when the member is absent.
`sanitizeRecord` in `format.go:99-118` must gain every new string member — a string member not added there ships
unsanitised.

## 6. Work breakdown

| Row | Scope | Tier |
| --- | --- | --- |
| **P10-1** | The cache: `brigade statusline`, the state-dir format, the timestamp/staleness rule, the wiring shape from §7 (a), `plugin/` allowlist and `plugin-check` implications | Opus |
| **P10-2** | **The wire change, in ONE commit**: the three members and their `Validate` arms; `limits.go` caps *and* matching `Limits` members; `describe.go` capabilities and limits rows; `watch.go` `WatchCommand`; `make schema`; `docs/protocol-v1.md` §§4.4.1–4.4.4 and the byte-identical `internal/protocol/testdata/examples/*.json`; the conformance suite; **both** adapters | Fable |
| **P10-3** | Display: `sessions.go` columns, `format.go` `sanitizeRecord`, `whoami.go`, and every golden | Opus |
| **P10-4** | User-facing docs: the statusline wiring in `docs/setup.md`, `docs/adapter-authors.md`, README | Opus |

P10-2 is Fable tier per the model-tier policy (protocol/schema design, Supabase SQL, the conformance suite).

**Two structural traps in P10-2**, both measured:

1. The Postgres `grant` lines name **full argument-type lists**
   (`supabase/migrations/20260830120000_brigade_schema.sql:361-362, 396-397`). Adding an RPC parameter creates an
   *overload* unless the old signature is dropped, which trips `supabase/tests/functions.sql`'s "exactly 28
   functions, no overloads" assertion. `supabase/tests/rpc_sessions.sql:75` also calls `register_session`
   **positionally**, so a new parameter shifts it.
2. `schema.patchProp` **fails loudly** on an unknown property
   (`internal/protocol/schema/schema.go:401-415`), so a member added without its cap patch is a hard error, not a
   silent omission.

**Golden files P10-3 must regenerate**: `cmd/brigade/testdata/script/sessions.txtar` (`want-sessions.txt` is a
byte-for-byte `cmp` — every new column rewrites all three session lines — plus the fake adapter's
`script.json.in`); roughly a dozen further txtar files embedding complete `describe`/`register` documents
(`hook-session-start`, `hook-prompt`, `hook-session-end`, `hook-frame`, `watch-hold`, `watch-sink`, `errors`,
`env-isolation`, `fs-session`, `inbox`, `send`, `readonly`, `whoami`, `smoke`); the 20
`scripts/ci/testdata/proof-crash-resume/*/sessions/*.json` fixture dirs; and
`internal/conformance/fixture_lease_test.go:73`.

`scripts/ci/pr-guard.sh` (P9-2) refuses a PR that changes `internal/protocol` without the schema, so P10-2's
one-commit rule is now mechanically enforced rather than conventional.

## 7. Open rulings

- **(a) Wiring shape** — tee or wrapper (§5.1). Affects the setup docs and whether the plugin can ship a default.
- **(b) Opt-in granularity** — three separate opt-ins, or one for `model`/`context` and a second for
  `usage_limits`? The recommendation is that `usage_limits` is always separate, because it publishes a person's
  account consumption to the whole team.
- **(c) Headless coverage** — is a permanently blank set of columns for `-p` sessions acceptable? If not, the
  statusline route cannot satisfy the requirement and §3.4's JS hooks route is the only candidate that can.
- **(d) Plugin-supplied `statusLine`** — unmeasured (§5.1). Needs one empirical check before P10-1 is scoped.
- **(e) Percentage-only usage limits** — confirm that `used_percentage` + `resets_at` is enough, given that no
  absolute quota figure exists anywhere (§3.1).

## 8. Risks

- **Headless blind spot (permanent).** `-p` sessions have no statusline, so all three columns stay empty for
  them. Not fixable within this route.
- **Opt-in blanks.** `brigade sessions` is a cross-machine roster; a teammate who has not wired their script shows
  blank columns and Brigade cannot fix that for them. The `workspace_label` precedent sets the same expectation.
- **`rate_limits` may never appear.** Subscription accounts only, after the first API response, and only while the
  window has not reset (§3.2). An API-key account yields nothing.
- **Undocumented payload shape.** The statusline payload is documented, but three members this design reads —
  `cost`, `exceeds_200k_tokens`, `fast_mode` — are emitted and absent from the built-in doc string, and the
  measured facts in §3.2 come from a binary at one version. Re-measure on a Claude Code bump.
- **Disclosure.** Publishing "this person is at 87% of their weekly limit" to every teammate is a product
  decision, not a plumbing one. §7 (b) is where it is settled.
