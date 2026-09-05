# E5-soak harness — plan row P5-11 (E2E-12, E2E-13)

Results and findings: `docs/experiments/E5-soak.md`. Runs on Claude Code 2.1.261, macOS arm64, against the
**local Supabase stack through the bundled adapter**. Python + `expect`. Never in CI (D31).

These drivers **import** `scripts/experiments/E4-interactive/{e4i,sender}.py` (the pty rig, the
local-stack provisioning, the roster, the teardown) and `scripts/experiments/E0-8/common.py` (the expect
prelude — `mark`, `xsend`, `nap`, `submit`, `onboard`, `canary`, `shutdown` — the by-prefix environment strip
and the config-dir resolution). Nothing under `E4-interactive/`, `E0-8/` or `plugin/` is modified or copied;
the one runtime rebinding is `sender.MARKER = "brigade-e5s-"`, so the imported `Stack`'s temp roots, its
project-directory guard and its `~/.claude.json` prune all carry this experiment's marker.

| File | What it does |
| --- | --- |
| `e5s.py` | the engine: the long-run pty session (`PtySoak`: spawn, onboard, split-token canary, **the permanent draining wait**, beats through files, the Escape-everything dialog policy, `session.log` rotation), the three-member `Stack` (alice, bob, dana; N synthetic hook-registered senders per principal; sink-mode watchers for the no-model measurements), the SQL instruments (`hints.sql`, `pg_stat_statements`, `realtime.messages`, `brigade.messages`, `brigade.sessions.last_seen_at`, `auth.refresh_tokens`), the pollers (`profile status` every 30 s, the roster and `ps -o pid,rss,%cpu,etime,args` every 60 s, the server's `last_seen_at` by SQL every 15 s), the three secret scans with their controls, the preconditions |
| `baseline.py` | M1–M4 of the brief (no model, no Claude): the rotation probe is read-only; the server-side rotation instrument on a throwaway principal (with the adapter's terminal branch fired deliberately); ten fabricated hints against a live watcher; the receive-only topic from code |
| `soak.py` | E2E-12: two interactive sessions on one profile, the isolation record (nested pid/session/socket differ from the outer's, asserted before anything is sent), beats every 12 min, the extra beat inside each refresh window, the final round trip at t = minutes − 1, the bursts inline at t = 60 and 90 min; `--smoke` is M5; `--kill-adapter-child` passes the labelled arm through to B2 |
| `burst.py` | E2E-13: B1 the 1,000 hints in 10 s (one psql script, ten batches paced by `pg_sleep`, the span from the server's clock; the responsiveness probes are ordinary `brigade whoami` beats judged from the transcript), B2 the 6 × 10 messages across two principals, B3 the optional sustained arm; standalone against its own session, or in-process from `soak.py`. `--kill-adapter-child`: after B2's t + 6 min reading, SIGKILL the target watcher's adapter child (the shipped supervision respawns it) so the queue-dropped ids are re-emitted and injected once — clause 10's second half, its construction named in every record. `--pause-watcher`: the watcher SIGSTOPped while the sends land — a constructed arrival condition, labelled, **never counted** |
| `hints.sql` | the trigger's own `realtime.send` call with fabricated ids; `sid` and `n` as psql variables |
| `score.py` | offline: bundle → `summary.json` / `soak.tsv` / `burst.tsv`; `--selftest` builds a synthetic bundle and proves the detectors catch a vacuous row, a notice without drops, a doubled `/token` count, a redelivery arm that injected a dropped id twice, and a `--pause-watcher` bundle (clause 10 `counted: false`). Foreign `claude` pids (the outer session) are excluded from the RSS series by the run's own pids |

```sh
# no model calls, no Claude session:
python3 scripts/experiments/E5-soak/baseline.py                       # M1-M4 (about 3 minutes; 4 anonymous sign-ups)
python3 scripts/experiments/E5-soak/baseline.py --only m2             # the throwaway-principal instrument alone
python3 scripts/experiments/E5-soak/score.py --selftest <dir>         # the scorer's own detectors
python3 scripts/experiments/E5-soak/score.py <bundle>                 # re-score a bundle offline

# model calls (never in CI; local stack required; count every session):
python3 scripts/experiments/E5-soak/soak.py --minutes 6 --beat 4 --no-burst      # the pilot
python3 scripts/experiments/E5-soak/soak.py --minutes 12 --beat 4 --no-burst     # M6
python3 scripts/experiments/E5-soak/burst.py --senders 6 --per-sender 3 --hints 100 --no-b3   # M7
python3 scripts/experiments/E5-soak/burst.py                                     # the standalone burst (about ten minutes)
python3 scripts/experiments/E5-soak/soak.py --kill-adapter-child                 # the deliverable: 120 min, bursts at 60/90, the labelled kill arm after B2
python3 scripts/experiments/E5-soak/burst.py --pause-watcher --kill-adapter-child # the constructed arm (after the deliverable; never counted)
```

Preconditions (exit 2, one stderr line): `bin/brigade` executable (`make build`), `jq`, `pgrep`, `expect`,
`python3`, `docker` with `supabase_db_brigade` running, `.env.test` present (`make supabase-env`), no other
`brigade` on `PATH`, `CLAUDE_CONFIG_DIR ?? ~/.claude` an absolute directory, no `crossSessionInbound` other than
`accept` in the three files the scan reads, and — for the model-calling drivers — `claude` on `PATH` with a
non-empty `--version`.

## The rules built in

- **Every wait is `expect`, never `sleep`.** Between beats the expect body sits in a permanent draining loop: an
  `expect` with a 5 s timeout inside a `while` that re-arms, one `drain` mark per slice, ended only by the stop
  file or eof. A `session.log` that stops growing while the session is supposed to be alive is a `FAIL:`.
- **`DISABLE_AUTOUPDATER=1`** in every nested session; `XDG_DATA_HOME` is **not** overridden (E4's launcher
  hazard); `claude --version` at t = 0 and at the end must be equal.
- **Marker `brigade-e5s-`** on every temp root and cwd; the transcript directories under the real
  `$CLAUDE_CONFIG_DIR/projects/` are removed by absolute path behind a name guard; `~/.claude.json`'s `projects`
  keys are pruned surgically; the real config files are hashed and **reported, never restored**.
- **The environment is stripped by prefix** (`common.nested_env()`: every `CLAUDE*`, `CLAUDECODE`, `AI_AGENT`,
  keeping only `CLAUDE_CONFIG_DIR`); `BRIGADE*` never reaches a `claude` process.
- **Secrets:** the join secret goes from `team create --secret-file <0600 file outside every scanned root>` into
  `team join`'s stdin through a pipe and is deleted before anything is scanned; `.env.test` is parsed line by
  line, never sourced; no token is ever printed; the exact-token scan reads its patterns from a 0600 file
  outside the roots; the `sb_secret_` canary is assembled at run time.
- **`ps` sampling is `-o pid,rss,%cpu,etime,args` only** — never `ps e`, `-E` or `eww` (the watcher's
  environment carries the messaging token).
- **stdout discipline:** the drivers print `ok:` / `FAIL:` / `measured:` / `sample:` / `say:` lines and nothing
  else; every `claude`, `brigade`, `expect`, `psql` and `docker` invocation's stdout and stderr goes to a file
  under the bundle.
- **The local stack is never stopped, started or reset;** the only container operations are `docker exec … psql`
  and `docker logs` (read-only).
- **Isolation is asserted before anything is sent:** the nested session's `claude_pid`, `claude_session_id` and
  `socket_path` (its by-pid map, read live) must differ from the outer session's (`common.outer_identity()`); the
  nested token is never read (the map carries none; no `ps e`).
- **The heartbeat instrument is the server's own `last_seen_at`, by SQL every 15 s** (no adapter process, nothing on
  the credential path). The 60 s roster poll only bounds the gap; the watcher's `heartbeat` line is DEBUG.
- **The split-token `READY` canary runs once per session, at setup.** Measured 2026-09-05: the THIRD `READY`
  canary of a session was refused by the provider's safeguard (`API Error: … safeguards flagged this message …
  [reasoning_extraction]`, 2 of 2), the first two never were (9 of 9), and every turn after a flag was refused too
  (4 of 4). A refused turn carries `stop_reason: refusal`, which `e4i.DONE_STOPS` counts as done, so the beat judge
  reads the transcript's `isApiErrorMessage` records and scores such a beat `provider_refusal`, never `settled`;
  the liveness sample counts refusals per session every minute and prints a `FAIL:` line at the first.
- **Two labelled constructions, never blurred with the shipped race:** `--kill-adapter-child` (clause 10's second
  half: the dropped ids redelivered once after the child restarts) and `--pause-watcher` (B2's arrival condition;
  a bundle made with it is scored `counted: false`).
- **Every process is stopped and reaped:** every `claude` pty, every hook-spawned watcher, every sender sleeper,
  every sink watcher; the dead-pid maps are pruned.

## What the baseline settled (2026-09-05, this tree)

- `profile status` **refuses `--json`** (`usage`, exit 2): the adapter always emits protocol JSON, so the poller
  runs it without the flag. Twenty reads in 143 ms (phase 1; 121 ms in phase 2) while a real watcher + adapter pair of the same profile was
  live: exit 0 every time, `token_expires_at` constant, the principal's `auth.refresh_tokens` family unchanged.
- `pg_stat_statements` (1.11, preloaded) **does** record PostgREST's RPCs: ten hints → exactly **+2**
  `fetch_inbox` calls within 3 s (the coalesced drain and its follow-up) against a background of 2 per 60 s (one
  live watcher's 30 s timer). No `message` event, nothing injected, the channel still joined.
- The adapter logs **nothing on a successful drain at any level**, and `message queued` / `message injected` /
  `heartbeat` are DEBUG, unreachable at the shipped INFO level the plugin's hooks use (`hooks.json` passes no
  `--log-level`); so drains are counted server-side, heartbeats from the roster's `last_seen_at`, and injections
  from the transcript cross-checked against `ack sent`.
- Each rotation **inserts one `auth.refresh_tokens` row** (`parent` = the previous token) and marks the previous
  row `revoked`; `auth.sessions.refresh_token_counter` stays empty on this build. GoTrue's audit trail
  (`auth.audit_log_entries`, `payload->>'actor_id'`) records a rotation as `token_refreshed` + `token_revoked`,
  a one-behind redemption as `token_refreshed` alone, and a refused `/token` as nothing — the attributable
  server-side `/token` counter the soak uses.
- A two-behind token gets HTTP 400 `refresh_token_already_used` and **flags every row of the family `revoked`**,
  yet the leaf still refreshes 200 (E0-6's "the family survives", now with its table signature). A global
  sign-out **deletes the `auth.sessions` row** (the family cascades away) and the token then gets
  `refresh_token_not_found`. The adapter's terminal branch fires on both: exit 4, the shipped "credential
  revoked; run `brigade team join` again" line, `session.json` deleted, exactly one Warn line.
- The adapter's `message watch` **emits each message id at most once per process** (`watch.go` `seen`), so a
  queue-dropped (unacknowledged) message is not redelivered until the watch child restarts; `burst.py` measures
  the dropped ids' server state and injection count at t + 6 min rather than assuming a redelivery.

The evidence bundle lives under `.ignored/proof/<UTC stamp>/` (gitignored), in the layout of the brief's 4.7.
