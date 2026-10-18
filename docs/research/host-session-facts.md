# Host session facts (card 117)

Schema inspection: 2026-10-18. All local inspection was read-only: field names, discriminator values, version
numbers and identity-equality checks only. No conversation, credentials or real session records were copied or
changed. The fixtures in `internal/harness/watch/testdata/hostfacts/` use invented IDs, titles and counts in the
shapes below. No new shipped dependency is needed; the SQLite indexes are not opened by Brigade.

## Codex CLI

Installed records from **0.163.0** have `history_mode: "paginated"`, yet still carry the ordinary JSONL rollout
envelope in `$CODEX_HOME/sessions/YYYY/MM/DD/rollout-<timestamp>-<thread-id>.jsonl`. `CODEX_HOME` defaults to
`$HOME/.codex`; the hook already forwards an absolute `CODEX_HOME` to its watcher. A relative override disables
this reader, rather than accidentally reading another installation.

Measured fields:

- `session_meta.payload.id` equals the native thread ID. `payload.session_id` is a separate connection identity
  and must not be used for this lookup. This header binds the file to the thread before any usage is accepted.
- `$CODEX_HOME/session_index.jsonl` contains `id`, `thread_name`, `updated_at`. The last complete matching entry
  supplies the name, including renames. Names and working directories are never lookup keys.
- `event_msg.payload.type: "token_count"` has `info.last_token_usage.input_tokens`, `cached_input_tokens`,
  `cache_write_input_tokens`, `output_tokens`, `reasoning_output_tokens`, `total_tokens`, alongside
  `info.total_token_usage` and `model_context_window`. The latest **input_tokens** supplies occupancy, matching
  Brigade's existing convention of reporting the last request's input context. Cache hits are already included;
  output, cumulative totals and the window capacity are not added.
- The same installed files also contain `token_usage_record` with `usage`, `turn_token_usage`, and
  `thread_token_usage`. These accounting records are deliberately ignored: the observed `token_count` event
  provides the context fact without interpreting cumulative billing counters.

This works for classic rollouts and the observed paginated mode because both have the same event envelope;
the extra `ordinal` field is irrelevant. It does **not** claim to decode an alternative page-only, database-only,
compressed or future history format without these files. A missing title index leaves the starting name;
missing usage leaves context unknown. No app-server or CLI process is started to obtain metadata.

## Muse Code

Installed **1.4.8-R6256.1** binary SHA-256:
`94ea8fd1c73664c22e5c6157d8706945b667b11bd1c7c51691811e79f17dfcf1`.
Local logs included 0.2.1 and 1.4.8 session metadata. The data directory contains `session-index.db` (SQLite),
but the authoritative session journal is also present at
`$HOME/.local/share/muse/sessions/YYYY/MM/DD/<session-id>/session.jsonl`.

Measured JSONL envelope: `schema_version`, `id`, `stream: {kind, id}`, `sequence`, `recorded_at`, `record_type`,
`durability`, `causation_id`, `payload_type`, `payload_schema_version`, `payload`. In all inspected session
envelopes, `stream.kind` was `session` and `stream.id` equalled the enclosing session-directory UUID. The reader
requires both on **every** record. Subagent directories are never searched.

- Observed `session.name.changed` payload: `authority_id`, `operation_id`, `session_id`, `previous_name`,
  `new_name`, `source`. The payload's session ID is checked too. Both automatically assigned names and host
  renames use this event; command-intake requests and generated descriptions are not treated as applied names.
- Observed `run.model.configured` payload: `kind: "run_model"`, `record.run_stream: {kind, id}`,
  `record.provider_id`, `record.model_id`, and other model-display metadata.
- Observed `runtime.session` payload: `kind: "run"`, `run_id`, `event: {kind, ...}`,
  `source_run_record_id`, `source_run_record_sequence`.
- The installed binary's serialized enum/struct descriptors identify `model_completed` with `usage`,
  `duration_ms`, `finish_reason`, `model`; its `Usage` has `input_tokens`, `output_tokens`, `cached_tokens`,
  `cache_write_tokens`, `cache_read_tokens`, `reasoning_tokens`, `prompt_tokens`. The local sample sessions had
  no successful provider completion, so the **usage fixture combines this binary schema evidence with the
  observed session/run envelope**, rather than claiming a live captured usage record.

Meta's [token-usage schema](https://meta-models.github.io/muse-code-sdk/next/generated/msp/types/sessiontokenusageparams/)
distinguishes counted-once prompt tokens from cumulative session totals and child usage.
Its [context-usage schema](https://meta-models.github.io/muse-code-sdk/next/generated/msp/types/sessioncontextusageparams/)
describes a prompt-plus-output occupancy floor; Brigade consistently reports the **input context** as it does
for Claude Code. The reader uses explicit `prompt_tokens`; for older/null prompt counts it uses `input_tokens`
only when the same run's `run.model.configured` identifies the native `meta` provider, whose input includes
cache hits. Unknown provider conventions remain unknown. Child rollups, reviewer completions, CPU telemetry,
and cumulative usage are never context. Model display continues to come from the hook-written map.

Muse clears `XDG_*` from plugin hooks (card 88), so the watcher has no reliable original `XDG_DATA_HOME` to
follow. It uses HOME only and ignores inherited XDG values. A host launched with a separate data directory is
unsupported by this reader. The stored `retained_frame: "session_permission_transaction"` wraps permission
records; the reader ignores it, as it contains neither fact. A future storage format that wraps these facts
would need separate evidence and support. The read-only inspection did not rename or run a real Muse session;
live host-to-roster verification remains an owner release check.

## Bounds and failure behaviour

Only the watcher reads these files, immediately before each heartbeat. A discovered path is cached. Discovery
visits at most 8,192 directory entries under exactly the three date levels, newest names first, without following
directory symlinks or searching subagent trees. Very large trees beyond that budget may remain undiscovered.
Native IDs must be complete UUIDs, never prefixes, names or working-directory guesses.

Each changed journal reads at most its first 1 MiB and last 8 MiB; the title index reads its last 8 MiB. Files
unchanged in identity, size and modification time are not reparsed. Lines over 1 MiB and partial boundary lines
are skipped; malformed lines do not stop later records. Facts older than these windows can be unavailable on a
fresh watcher. A live rename appended in the supported shape arrives on the next heartbeat. Missing files keep
last known facts; truncation/replacement resets journal facts. Changing host, root or native ID clears the cache.

Reads require regular files and use nonblocking, no-follow opens. JSON remains lenient on duplicate members and
invalid Unicode, matching the Claude transcript reader. Negative/non-integer counters are rejected, integer
overflow saturates, and counts beyond the wire's safe range are omitted so lease renewal still works. Names use
the existing name sanitiser. No raw file bytes, paths, native IDs, timestamps, provider usage detail or filesystem
errors are logged or sent. Only the sanitised roster name and derived token count leave the reader; the existing
model path and Claude reader are unchanged (T10, U-22).
