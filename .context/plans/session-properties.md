# Brigade session properties: the model and the context occupancy

**Status.** Brief, **v2**, 2026-09-10, **shipped**. v1 of this file (same day) chose the statusline payload as the
source and left five rulings open; v2 records what Rjae ruled instead — **the source is the transcript** — and the
design that was then built. §3 is unchanged: it is the measurement record, and it is what decided both versions.
Rows **P10-1..P10-4** are `done` in `.context/plans/brigade-execution-log.md`; the wire change, the conformance
case, the Supabase migration, both adapters, the harness and the docs landed in **one commit** as
`docs/protocol-v1.md:3-9` requires.

## 1. What was asked

Expand what `brigade sessions` reports, per session, with three properties:

1. **Model** — which model the session is running.
2. **Usage limit** — used and available.
3. **Context** — used and available.

**Two of the three were dropped, and that is a ruling, not an omission** (§4, §7). What shipped is the model and
the context *occupancy* — a token count, with no denominator. Usage limits are not obtainable at all from disk
(§3.1), and the context window size exists only in the statusline payload (§3.2), which is the source this design
gave up.

## 2. What a session record carried before this change

`protocol.SessionRecord` (`internal/protocol/session.go`) was the whole of what `brigade sessions` could show:
`session_id`, `session_name`, `session_description`, `principal_ref`, `human_label`, `state`, `activity`,
`inbound`, `last_seen_at`, `lease_until`, `harness`, `harness_version`, `workspace_label`, `created_at`, `is_self`.
None of the three requested properties was present, and the harness had no path to any of them.

Two existing constraints framed everything below.

- **The protocol is frozen.** `docs/protocol-v1.md:3-9`: changing any wire shape means changing
  `internal/protocol` (types and `Validate`), `docs/protocol-v1.schema.json` (`make schema`), the conformance
  suite and both bundled adapters **in one commit**. A change an existing conforming adapter would *fail* is a new
  major version — this one is not (§5.3), and it landed as one commit.
- **Threat model T10.** `docs/protocol-v1.md` 4.4.2: the registration has no member for a native session id, a
  working directory, a hostname, a username or a transcript path, and "adding one is a protocol change, not a
  convenience." `internal/harness/hook/hook.go` stated a harness-side half that was **stricter than T10 itself** —
  `transcript_path` and `prompt` are "never read, stored or sent" — and it is that sentence, Brigade's own, that
  §4 relaxes. `prompt` is still never read; the wire still has no member for a path.

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
   no capture appeared, while all six hooks fired. This was v1's one permanent blind spot; §7 (c) is what v2
   answers instead.

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

**Nothing in §3 was re-measured for v2, and nothing in it changed.** §3.3 — the transcript — is the source the
shipped design reads; §3.2 is kept because it is the whole reason two of the three properties are gone, and §3.4
because it is the option to revisit if the answer to §7 (c) ever changes.

## 4. The decision, and why

**Ruled by Rjae, 2026-09-10, revising the same day's earlier ruling: the source is the TRANSCRIPT.** The request
shrinks to the two properties a transcript can supply — the model identity and the context occupancy in tokens.
The v1 ruling (the statusline payload) was made before two findings landed, and either one alone is fatal to it:

1. **A plugin cannot supply `statusLine`** — measured, and preserved verbatim below. Only `subagentStatusLine` is
   plugin-contributable. So Brigade could never ship this feature *working*: every member of every team would
   have to hand-edit their own `settings.json`, and the columns would stay blank until they did.
2. **That makes it operator-dependent, and operator-dependence is the showstopper.** `brigade sessions` is a
   cross-machine roster read by other people. A column that is populated for the two members who wired their
   statusline and blank for the other five is not a feature; it is a source of wrong conclusions about
   teammates. The bar this feature has to clear is **no operator intervention** — install the plugin and it
   works — and the transcript route is the only candidate that clears it: the file already exists for every
   interactive session, the hook already receives its path on stdin, and the watcher already runs on the machine
   that owns the file, every 30 seconds, for exactly this session.

**The T10 objection was a misreading of Brigade's own rule.** T10 governs what goes **on the wire**: no member for
a native session id, a working directory, a hostname, a username or a transcript path. It does not say the harness
may not read a file that Claude Code wrote on the user's own machine, for the user's own session, on the user's own
behalf. What forbade that was one comment in `internal/harness/hook/hook.go` — "never read, stored or sent" — which
was stricter than the rule it cited. **Rjae relaxed the comment, not T10.** The protocol is unchanged in the way
that matters and now says so explicitly (4.4.2): "The registration has no member for a native session id, a working
directory, a hostname, a username or a transcript path … The harness MAY report `model` and `context_used_tokens`,
two facts it derives locally from the session's own transcript; the transcript and its path never travel, and
neither fact names a machine, a user or a file." The path is held only in the 0600 by-pid map (§5.2), which already
holds `claude_session_id` under the same rule, and `whoami` does not expose it.

| Route | Gets | Verdict |
| --- | --- | --- |
| **Transcript** (chosen) | model + occupancy | **Shipped.** Zero operator intervention; costs a relaxation of Brigade's own comment, not of T10; gives up the two properties it cannot see |
| Statusline (v1's choice) | all three, including the context denominator | **Dead.** A plugin cannot supply `statusLine`, so it is per-user hand wiring on every machine — and a roster that is blank for whoever did not wire it |
| Plugin JS hooks | all three, live, in-process | **Not taken.** Undocumented binary surface on 2.1.267 that a point release can remove, and it would replace the plugin's exec-form command hooks, which `make plugin-check` requires. Revisit only if §7 (c) is ever answered "headless coverage is required" |

The measurement that settled it, kept from v1 §5.1:

> **A plugin cannot supply `statusLine`.** Measured 2026-09-10 on 2.1.267, from two independent code paths:
> `subagentStatusLine` resolves through `Rst()`, which reads **plugin** settings (guarded by `pluginBaseLoaded` and a
> `tengu_plugin_settings_premature_read` telemetry event), whereas `statusLine` resolves through `lve()`, which takes
> the value from ordinary settings and allows only a `policySettings` override — it never reads plugin settings; and
> the plugin-contributable surface registry is `["agent","subagentStatusLine"]`, which does not include `statusLine`.
> So Brigade **cannot ship this working**: every user adds `brigade statusline` to their own `statusLine.command` by
> hand, and the columns stay blank until they do. This settles §7 (d) and makes the wrapper the better of the two
> shapes in §7 (a).
>
> (An earlier draft of this section cited a "settings-source gate map" as leaning evidence. That was a misreading:
> the two maps at `Wr(e,s)` are feature-disable maps for restricted trust modes and say nothing about plugin
> sourcing. The conclusion is unchanged; the evidence above replaces it.)

## 5. The shipped design

Everything below is what was built, in the order a value travels: the transcript file, the reader, the by-pid map,
the watcher, the wire, the two adapters, the display.

### 5.1 The reader — `internal/harness/transcript`

A new package with one job: turn an append-only NDJSON transcript into two facts, incrementally, without ever
blocking the watcher.

```
type Facts  { Model string; ContextUsedTokens int; HasContext bool }
type Reader { path, offset, attModel, asstModel, asstAfterAtt, tokens, hasContext }
NewReader(path) *Reader ; (*Reader) Path() string ; (*Reader) Refresh() (Facts, error)
```

- **What it reads.** Each line is unmarshalled into a minimal struct — `type`, `isSidechain`, `message{model,
  usage{input_tokens, cache_creation_input_tokens, cache_read_input_tokens}}`, `attachment{type,
  identity{modelId}}` — and nothing else is looked at. No prompt, no answer, no tool call, no file name.
- **Occupancy** is `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` of the **latest
  non-sidechain `assistant` record that has usage** (§3.3). Consecutive assistant records of one API response
  repeat the same usage, so the last one wins and the number does not double-count.
- **Model** is the reconciliation of two sources, because neither is complete on its own: an `attachment` record
  with `attachment.type == "model"` carries the **full** identity (`claude-opus-5[1m]`) and is written at session
  start *and* on every `/model` switch, while an `assistant` record carries the **bare** id (`claude-opus-5`) on
  every turn. The rule: keep the latest attachment id (`attModel`), the latest assistant id (`asstModel`) and
  whether an assistant record was seen *after* the latest attachment; report `asstModel` when there is no
  attachment at all; report `asstModel` when an assistant record came after the attachment and
  `!strings.HasPrefix(attModel, asstModel)` — a switch the attachment missed; otherwise report `attModel`, suffix
  and all.
- **It cannot hang and it cannot be starved.** `os.Stat` first, and a path that is not a **regular file** — a
  FIFO, a device, a directory — is an error with no read at all, so nothing on the command-writer goroutine can
  block on an `open`. Reads start at the stored offset with `bufio`, take **complete lines only** (a trailing
  partial line is left for the next `Refresh`), and cap one line at **64 MiB** — a longer line is skipped and the
  offset advanced past it. `size < offset` is treated as truncation: offset and facts reset. A line that fails to
  parse is skipped.
- **Every error it returns is diagnostic only**, fixed text, and never contains the path.
- **Sanitisation is the caller's job.** `Facts.Model` is raw, exactly as Claude Code wrote it.

Unit tests cover each of those in `t.TempDir()` — never `$CLAUDE_CONFIG_DIR` — with fixtures for: an attachment
then assistant records (full id kept), a second attachment after a switch, an assistant record whose bare id
disagrees with the attachment (bare id wins), no attachment at all (bare id), a sidechain record ignored, a partial
trailing line completed on the next call, truncation resetting, a non-JSON line and a > 64 MiB line skipped, a FIFO
refused, and a missing file.

Two record shapes measured after the first cut and handled by the reader: Claude Code writes an assistant
record with `message.model` **`<synthetic>`** and an all-zero usage for a turn the API refused (a 529, a usage
limit, "not logged in" — five such records across real transcripts on 2.1.267), and the reader skips it whole,
because under the model rule it would otherwise have reported `&lt;synthetic>` as the model and `context=0`; and it
writes lone-surrogate escapes, invalid bytes and duplicated members that json/v2's strict default refuses, so
records are decoded leniently (U+FFFD, last value wins — `JSON.parse`'s own reading). A usage with a negative
count is skipped and a sum that would overflow saturates: the adversarial pass showed that a negative or oversize
`context_used_tokens` on the wire makes the adapter refuse the **whole** heartbeat, lease renewal included, so
the watcher sends the count only when it is one the wire carries and leaves it absent ("unchanged") otherwise.

### 5.2 The path the transcript path itself takes — hook, map, watcher

- **The hook** (`internal/harness/hook`) gains `transcript_path` on its stdin type. It is a documented common hook
  field and was already arriving on every event (§3.1); what changed is that Brigade now keeps it. `buildMap`
  stores it only when `filepath.IsAbs`, else `""`; `refreshMap` and `prompt()` refresh it when it changed, the
  same way `PermissionMode` is refreshed. The type comment says what is now true: `transcript_path` **is** read,
  kept only in the 0600 by-pid map for the watcher, never sent — the two derived facts are what travel — and
  `prompt` is still absent from the type.
- **The map** (`internal/harness/sessionmap/bypid.go`) gains `TranscriptPath string
  json:"transcript_path,omitzero"`, private 0600 Brigade state alongside `claude_session_id`. `Validate` refuses a
  non-empty **relative** path (field `transcript_path`). `whoami` does **not** expose it.
- **The watcher** (`internal/harness/watch`) reads the path from the map where it already reads
  `BrigadeSessionID` and the session name, and refreshes it in `refreshMap` when it changed — so `/clear`, which
  starts a new native session and therefore a new transcript file, is picked up within the existing 2 s poll. It
  holds one `*transcript.Reader`, used **only on the command-writer goroutine**: in `heartbeat()`, an empty path
  means no reader and no facts; a path that differs from the reader's means a new reader; then
  `facts, err := reader.Refresh()` (an error is a `Debug` log of fixed text). The facts go out on **both**
  heartbeat paths — the `WatchCommand` stdin path and the `HeartbeatRequest` RPC path — as `Model` (a pointer to
  `oneLineModel(facts.Model)` when non-empty: `protocol.SanitizeModel` then a `strings.Fields` join, like
  `oneLineName`) and `ContextUsedTokens` (a pointer when `facts.HasContext`). A model **change** is logged at
  `Info` — "model updated from the transcript", `slog.String("model", …)` — and the path never is.

**The registration carries neither.** The first heartbeat arrives within about 2 s of `ready`, so registering
without them costs nothing and keeps the hook free of any reader.

### 5.3 The wire members

Two optional, nullable, capability-gated members on `SessionRegistration`, `SessionRecord`, `HeartbeatRequest` and
the `heartbeat` command of `message watch` — the `session.description` / `session.workspace_label` /
`session.inbound` precedent, exactly:

| Member | Shape | Cap | Capability |
| --- | --- | --- | --- |
| `model` | string, e.g. `claude-opus-5[1m]` | `MaxModelChars` = 128 code points, published as `limits.max_model_chars` | `session.model` |
| `context_used_tokens` | integer | `0..MaxContextUsedTokens` = `2^53 − 1` | `session.context_used_tokens` |

- **Heartbeat semantics: absent means unchanged** (JSON convention 4), and the harness never clears them.
- **`max_model_chars` is its own `limits` member** (P1-4 decision 1): `DefaultLimits` sets it, `Limits.validate`
  requires it > 0, and C-18 therefore requires it of every adapter automatically, by reflection over
  `protocol.Limits`. **`context_used_tokens` deliberately has no `limits` member**: its bound is the largest
  integer JSON carries exactly — a fact of the wire format, not a cap an adapter chooses.
- **`protocol.SanitizeModel(s)`** = `truncateRunes(Sanitize(s), MaxModelChars)`. Every string that reaches a model
  goes through the sanitiser, and a model identity is no different for being machine-written.
- **This stays v1.** Unknown members are accepted and ignored (C-17), so no existing conforming adapter fails —
  the test `docs/protocol-v1.md:9` sets for a new major is not met. An adapter without either capability accepts
  the member and ignores it, like `session.inbound`.
- **Every member is `omitzero`.** `required_test.go` derives requiredness purely from the tag, so a bare one would
  silently oblige every producer to emit the member.
- **`schema.patchProp` fails loudly** on an unknown property, so both members' `maxLength` / numeric patches are
  mandatory on all four shapes rather than optional.

### 5.4 The two adapters

- **Supabase** — a **new** migration (`make migration-new name=session_model_context`; the frozen ones are never
  edited): two columns on `brigade.sessions` (`model text check (char_length(model) <= 128)`,
  `context_used_tokens bigint check (context_used_tokens >= 0)`), `brigade.session_record(s, p_label)` re-created
  with both members (same signature, so no overload), and `register_session` / `session_heartbeat` **dropped and
  re-created** with `p_model text default null, p_context_used_tokens bigint default null` **appended** — which
  keeps the positional pgTAP calls working, keeps `supabase/tests/functions.sql`'s "exactly 28 functions, no
  overloads" true, and keeps `security definer` + `set search_path = ''` on every function. `revoke`/`grant` name
  the full new argument-type lists. Validation raises `brigade:invalid_input:model` /
  `brigade:invalid_input:context_used_tokens` with errcode 22023. The Go adapter passes the two parameters in
  `register`, `heartbeat` and the watch stdin heartbeat, and `describe` advertises both capabilities.
- **fs** — `sessionFile` gains both members, `record()` maps them, `sessionRegister` assigns from the request,
  `store.heartbeat` applies them when non-nil, `watch.go`'s `CommandHeartbeat` → `HeartbeatRequest` mapping passes
  them, and `describe` advertises both capabilities.

### 5.5 The display

`sanitizeRecord` (`internal/harness/commands/format.go`) sanitises `Model` with `protocol.SanitizeModel` — a string
member not added there ships unsanitised. `sessions.go`'s human line inserts, **only when present**,
`model=<modelLine>` after `principal=…` and `context=<tokensLine>` before the `seen …` column, so a record without
them prints exactly what it printed before and the existing goldens are unchanged. `modelLine(s)` is
`oneLine(protocol.SanitizeModel(s))` — the name column's rules, no more; `tokensLine(n)` is `strconv.Itoa(n)` under 1000 and
`(n+500)/1000` with a `k` otherwise (189681 → `190k`, 1500 → `2k`, 999 → `999`).

`cmd/brigade/testdata/script/sessions.txtar` gives two of its four canned records the new members — alice
`claude-opus-5[1m]` / 189681, and carol a **hostile** model string carrying `<system-reminder>` tags and a newline,
so the golden proves the sanitiser on this member the way the session name already proves it on that one.

### 5.6 Conformance

One case, **C-44**, tagged `core` + `cap:session.model` + `cap:session.context_used_tokens` (it skips when
**either** is missing): register with `model` `claude-opus-5[1m]` and `context_used_tokens` 189681 and read both
back from the register result and from `session list`; heartbeat with new values and see them; heartbeat with
neither and see the old ones unchanged; a model of `max_model_chars + 1` code points (built from `é`, so the cap is
proven in code points and not bytes) → `invalid_input` naming `model`, and at the cap → accepted;
`context_used_tokens` of −1 → `invalid_input` naming `context_used_tokens`. The suite is **46 cases**.

## 6. What was done

| Row | Scope as executed | Tier |
| --- | --- | --- |
| **P10-1** | `internal/harness/transcript` (§5.1) and the plumbing that feeds it (§5.2): `transcript_path` on the hook's stdin type and in the by-pid map, the watcher's reader and both heartbeat paths | Fable |
| **P10-2** | **The wire change, in ONE commit** (§5.3–§5.6): the two members and their `Validate` arms on four shapes, `MaxModelChars` / `MaxContextUsedTokens`, `limits.max_model_chars`, `SanitizeModel`, the schema patches and `make schema`, `docs/protocol-v1.md` with its byte-identical examples, C-44, and **both** adapters including the new Supabase migration | Fable |
| **P10-3** | Display (§5.5): `sessions.go`, `format.go`, `sessions.txtar` and the inline `limits` objects in the eight `cmd/brigade` txtar files that a new required `limits` member invalidated | Opus |
| **P10-4** | Docs: `docs/adapter-authors.md`, `docs/security.md`, `CHANGELOG.md`, and this brief and the execution log | Opus |

**What P10-3 did *not* have to touch, against v1's expectation:** `whoami` (it reports this session, not a roster,
and neither member is on it) and the 20 `scripts/ci/testdata/proof-crash-resume/*/sessions/*.json` fixtures (two
new **optional** members change no existing document). v1 predicted both; adding the members as `omitzero`
pointers is what made them free.

## 7. The rulings, answered

- **(a) Wiring shape** — **moot.** Tee or wrapper was a question about a statusline command; there is no such
  command. Nothing to wire, nothing to document, no `plugin/` allowlist change.
- **(b) Opt-in granularity** — **moot for `usage_limits`, answered for the rest.** The properties that publish a
  person's *account* consumption to the whole team are not shipped at all, so the disclosure question they raised
  does not arise. The two that are shipped describe a *session*, and each has its own capability
  (`session.model`, `session.context_used_tokens`) so an adapter or a backend can carry one without the other.
- **(c) Headless coverage** — **answered: nothing, and it is not a regression.** A `-p` session with no inbox
  socket has no watcher, and therefore sends no heartbeat at all, so it does not appear in the roster with stale
  columns; it does not appear with a heartbeat that lacks them. This is the same answer as before this change —
  headless sessions were already invisible to the watcher's cadence — and it is unchanged by the source switch.
- **(d) Plugin-supplied `statusLine`** — **answered 2026-09-10: no.** The measurement is in §4. It is what killed
  the v1 route.
- **(e) Percentage-only usage limits** — **moot.** No usage limits ship.

Two further rulings this brief records:

- **Usage limits: dropped.** No absolute quota figure exists anywhere on disk, rate-limit state is in memory only,
  and the only surface that reports it is the one a plugin cannot install (§3.1, §3.2, §4).
- **The context *window*: dropped.** `context_window_size` is resolved at runtime and exists nowhere on disk
  (§3.2 fact 1), so `context=` is an occupancy in tokens with no denominator, displayed as `190k` rather than as a
  percentage. A percentage would have to guess the window, and a guessed denominator on someone else's session is
  a wrong number rather than a missing one.

## 8. Risks

- **The transcript format is undocumented — and it is load-bearing for `--resume`.** Nothing in the shipped
  binary's documented surface promises the record shapes §3.3 measured. What makes this a tolerable dependency
  rather than a reckless one is that the same file is what Claude Code itself reads to resume a session, so its
  shape cannot churn freely. Every fact in §3.3 was measured on **2.1.267**; **re-measure on a Claude Code bump**,
  and note that the reader degrades to silence rather than to a wrong answer — an unparseable line is skipped, an
  unknown record type is ignored, and a transcript that yields nothing simply sends no members, which the wire
  reads as "unchanged".
- **`model` is a harness's word about itself.** Nothing verifies it, the backend cannot check it, and it is
  displayed as unverified text like every other name on the wire. It is capped at 128 code points and sanitised
  on the way out *and* on the way in to the display (§5.5), and `sessions.txtar` carries a hostile value to prove
  the second half.
- **Occupancy is a lagging number.** It is the latest assistant record's usage, refreshed immediately before each
  heartbeat, so it is at most one heartbeat interval (30 s) old and it does not move while a session is idle —
  which is the truth about that session, not staleness.
- **A migration that must be applied before the plugin updates.** The Supabase adapter sends the two new RPC
  parameters unconditionally, so a project whose functions predate the migration refuses `session register` and
  `session heartbeat`. The `CHANGELOG.md` "Unreleased" entry says so and names the procedure in `docs/setup.md`.
- **One machine's transcript, one machine's read.** The reader runs in the watcher, as the user, on the user's own
  machine, and sends two values. If that ever stops being true — a hosted watcher, a remote session — this design
  has to be re-argued from §4, not extended.
