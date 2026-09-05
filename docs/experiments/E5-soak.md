# E5-soak — the soak and the burst (`scripts/experiments/E5-soak/`, plan row P5-11)

Date: 2026-09-05 · Ticket 15 · Status: **PHASE 2 DONE — the 2 h two-session soak ran to the end (E2E-12 green: 11 of the scorer's 13 clauses `ok`); E2E-13's hint half is green and its drop half is an **honest negative** on the shipped race (clauses 10 and 11 `FAIL`: Brigade's queue never filled), shown instead under a labelled construction; one Claude Code finding (9 of 60 acknowledged frames lost in Claude Code's own 50-entry queue) and one provider-safeguard finding** · Driver: `scripts/experiments/E5-soak/`
(Python + `expect`, importing `E4-interactive/{e4i,sender}.py` and `E0-8/common.py`) · Claude Code 2.1.261, the
account's default model (`claude-opus-5`, "Opus 5 (1M context)" on the TUI) · macOS 25.6.0 (darwin/arm64) · Local
Supabase stack (PostgreSQL 17.6, GoTrue v2.196.0, `pg_stat_statements` 1.11) through the bundled adapter · Tree
`b343b0b`, `bin/brigade` sha256 `f905094a…225c` from `make build` at the start of phase 2.

**What this experiment is.** E2E-12: two Claude Code sessions sharing one adapter profile on one machine run for
2 hours with token refresh — no reuse-detection lockout. E2E-13: the watcher receives 1,000 realtime hints in
10 s — injection stays bounded, the session remains responsive, one summarised drop notice appears
(`docs/research/security-threat-model.md:685-686`; the row at `.context/plans/implementation/08-phases.md:144`).
What P5-11 adds over E0-6 (30m28s at `jwt_expiry = 300`, three processes, no Claude) is **realism**: real
interactive Claude Code sessions, the real plugin, the real detached watchers, the real inbox socket, for two
hours at the shipped `jwt_expiry = 3600`.

**Every number in this file comes from a command run on 2026-09-05.** Phase 1 (no model call) built the rig and
measured M1–M4; phase 2 re-measured the baseline against the restarted stack, ran M5–M7 and the pilots (nine
sessions), then the deliverable (two sessions), then the labelled constructed arm (one session). Every session is
counted below. The raw bundles are under `.ignored/proof/<UTC stamp>-<name>/` (gitignored) and the scratch
outputs under `.ignored/tools/p5-11/author/`.

## Running it

```sh
# no model calls, no Claude session:
python3 scripts/experiments/E5-soak/baseline.py                          # M1-M4 (about 3 min; 4 anonymous sign-ups)
python3 scripts/experiments/E5-soak/baseline.py --only m2                # the throwaway-principal instrument alone
python3 scripts/experiments/E5-soak/score.py --selftest <dir>            # the scorer's seven detectors
python3 scripts/experiments/E5-soak/score.py <bundle>                    # re-score any bundle offline

# model calls (never in CI, D31; the local stack required; count every session), in this order:
python3 scripts/experiments/E5-soak/soak.py --smoke                      # M5: one session, brigade whoami, /exit (~1.5 min)
python3 scripts/experiments/E5-soak/soak.py --minutes 6 --beat 4 --no-burst    # the pilot of the two-session shape (~8 min)
python3 scripts/experiments/E5-soak/soak.py --minutes 12 --beat 4 --no-burst   # M6: the idle life at 1/10 scale (~14 min)
python3 scripts/experiments/E5-soak/burst.py --senders 6 --per-sender 3 --hints 100 --no-b3   # M7: the burst in miniature (~9 min)
python3 scripts/experiments/E5-soak/burst.py                             # the standalone burst (~10 min; the verifier's re-derivation)
python3 scripts/experiments/E5-soak/soak.py --kill-adapter-child         # the deliverable: 120 min, bursts at 60 and 90 min, the labelled kill arm
python3 scripts/experiments/E5-soak/burst.py --pause-watcher --kill-adapter-child   # the constructed arm, after the deliverable; never counted
```

Preconditions (`e5s.preconditions`, exit 2, one stderr line): `bin/brigade` executable (`make build`), `jq`,
`pgrep`, `expect`, `python3`, `docker` with `supabase_db_brigade` running, `.env.test` present (`make
supabase-env`), no other `brigade` on `PATH`, `CLAUDE_CONFIG_DIR ?? ~/.claude` an absolute directory (here
`/Users/rjae/.claude-ifthen`), no top-level `crossSessionInbound` other than `accept` in the three files the
scan reads, and for the model-calling drivers a `claude` on `PATH` with a non-empty `--version` (2.1.261 today;
the launcher `~/.local/bin/claude` resolves under `~/.local/share/claude/versions/2.1.261`).

A long run is launched detached (`nohup sh -c '… ; echo exit=$? > <name>.status'`) with `caffeinate -i -w
<wrapper pid>` beside it, and watched by polling the bundle's `logs/driver.log`. A foreground shell would cap it.

## What the run does

**The soak (`soak.py`, brief 4.1–4.4).** Provisions `alice`, `bob` and `dana` in team `ops` under a temp
`XDG_CONFIG_HOME` (the join secret through a 0600 file outside every scanned root, piped into `team join`,
deleted before anything is scanned). Spawns two interactive pty sessions whose `--settings` both carry
`pluginConfigs."brigade@inline".options.profile = "bob"`, so both `SessionStart` hooks register two distinct
Brigade sessions under one principal and spawn two watchers whose adapter children share
`profiles/bob/session.json` and its sidecar lock — the subject of E2E-12. Each session: `stty_init "rows 50
columns 200"`, `spawn -noecho env -u … claude --permission-mode default --session-id … --plugin-dir … --settings
…`, `onboard`, the split-token `canary` (once, here only), the by-pid map read live, then **the permanent
draining loop** until the stop file. Before anything is sent, the isolation record: the nested session's
`claude_pid`, `claude_session_id` and `socket_path` (its by-pid map) must differ from the outer session's
(`common.outer_identity()`); the map carries no token and no environment is read. After both are ready each gets
one setup prompt (< 400 characters: you are `soak-a`, your teammate is `soak-b` with this id; reply to every
Brigade message with one short line via `brigade send`; run `brigade` only as the bare command; never edit
files). Then at t = 6, 18, …, 114 min each session is told `Run: brigade sessions --json   Then run: brigade
send <peer> with the body TICK-<n>-<hex> on stdin (a heredoc)`. The rotation poller (`profile status` every 30 s)
predicts each rotation from `token_expires_at` and schedules an extra beat on A at expiry − 60 s (three processes
inside the 90 s margin at once). At t = 119 min the final round trip: A sends B `FINAL-<hex>`, the driver
requires the frame in B's transcript and B's reply row in the database and its frame in A's transcript. Bursts
run inline against A at t = 60 (B1) and t = 90 (B2, the labelled kill arm, then B3 if the bound was reached).
Throughout: the roster every 60 s (`last_seen_at`, `state`), **the server's own `brigade.sessions.last_seen_at`
by SQL every 15 s** (no adapter process, nothing on the credential path — the heartbeat instrument; the roster
poll only bounds the gap and the watcher's `heartbeat` line is DEBUG), `ps -A -o pid,rss,%cpu,etime,args` every
60 s, `session.log` bytes and the count of provider-refused turns every 60 s, roster snapshots at t0/t60/t119,
`caffeinate -i` for the block.

**A beat's verdict.** A beat is `settled` when at least one new assistant record appeared after the submit mark,
the last carries a DONE `stop_reason`, and no further assistant record came for 35 s (E4's rule). One addition,
measured into existence today: a provider refusal is an assistant record with `isApiErrorMessage: true` and
`stop_reason: refusal`, and `refusal` is in `e4i.DONE_STOPS`, so the settle rule alone would call a refused turn
answered. The judge reads the transcript's `isApiErrorMessage` records and scores such a beat `provider_refusal`,
never `settled`; the scorer counts only settled beats with zero refusals toward the workload floor.

**The burst (`burst.py`, brief 4.5).** B1: `hints.sql` — the trigger's own `realtime.send(…, 'message_accepted',
'brigade:session:<A>', true)` with fabricated ids — as **one psql script through `docker exec -i
supabase_db_brigade psql`, ten batches of 100 with `pg_sleep(1)` between them**, so the pacing is the server's
clock; the span the hints land in is the first batch's `t0` to the last batch's `t1` (`clock_timestamp()`),
recorded as `sql_span_ms` beside the docker wall. Two responsiveness probes, submitted at t+5 s and t+60 s: an
ordinary beat (`Run: brigade whoami   Then reply with one short line.`), its round trip the first real assistant
record's timestamp minus the submit mark. The drain count from `pg_stat_statements` against a background rate
taken in the 30 s before; the watcher and adapter log slices; `ps` every 5 s across the window. B2: six synthetic
hook-registered senders (`alice` × 3, `dana` × 3; a sleeper as `CLAUDE_PID`, no socket, no watcher), 10 distinct
bodies each, all sixty sends issued concurrently; the watcher's `injection queue full` Warn lines, the drop
notice in A's transcript (the literal, unframed), rate notices, frames per sender per minute, `ack sent`, the
server's `delivery_state` per id at t+60 s and t+6 min. **Then the labelled kill arm** (`--kill-adapter-child`,
driver ruling 3): the adapter child of A's watcher is SIGKILLed; the shipped supervision respawns it (`watch
child failed; restarting`, then a new `watch ready`, counted, never time-matched); the new child's per-process
`seen` map is empty, so exactly the queue-dropped ids — still `accepted` on the server — are re-emitted, offered,
injected once and acknowledged. With no dropped ids the arm records `not_run`. B3 (optional, only after a green
B2): ~20 sends/min for 5 min starting 60 s after B2.

**The scorer (`score.py`).** Offline, from the bundle alone: the rotation series, the audit-trail `/token`
counts, the lock and terminal lines, the heartbeat gaps (roster and SQL), RSS first/median/last and a
least-squares slope per process (the run's own `claude` pids only; the outer Claude Code session is a foreign
`claude` and is excluded by pid), the beats answered / frames injected / acks per session, the final round trip,
the end state, the burst phases; then brief 7's clauses, each `ok` or not, and `green` only when all are.
`--selftest` builds a synthetic bundle, scores it green, re-scores it byte-identically, then plants a vacuous row
(0 beats, 0 frames, 0 acks), a drop notice with zero drops, four `/token` calls for two rotations, a redelivery
arm that injected a dropped id twice, and a `--pause-watcher` B2, and requires each to be caught (the last as
clause 10 `counted: false`).

## The soak baseline

Made 2026-09-05 with no model call and no Claude session, through the engine itself
(`scripts/experiments/E5-soak/baseline.py`). Phase 1 measured against a postgres started `16:25:23 UTC`; the
stack was restarted by someone else before phase 2 (postgres start `18:22:08 UTC`, containers "Up 14 minutes" at
18:36 UTC; never restarted by this lane), so phase 2 re-ran the whole baseline once (`--out
.ignored/tools/p5-11/author/phase2-baseline`, started 18:39:02Z, 0 `FAIL` lines, `baseline.json` `m1..m4`
`ok: true`). The numbers below are phase 2's; phase 1's were the same shape.

**M1 — the rotation probe is read-only.** A fresh `bob`, a session registered through the real `brigade hook
session-start` with a sleeper as `CLAUDE_PID`, then the shipped `brigade watch --sink` for it — the real watcher
plus its real `adapter supabase message watch` child, minus the socket. Watcher start → `watch ready` **254 ms**,
`watch status live/joined` in the same read. **`profile status --json` is refused (`usage`, exit 2)** — the
adapter always emits protocol JSON and takes no `--json`; without the flag exit 0 with `token_expires_at`. Then
**twenty reads in 121 ms**: exits `[0]` every time, `token_expires_at` constant (`2026-09-05T19:39:03Z`), `state`
`joined` every time, the principal's `auth.refresh_tokens` rows **1 → 1**, `refresh_token_counter` unchanged
(empty), the adapter log **118 → 118 bytes**, zero `/token` lines in the auth container's log, `pg_stat_statements`
`fetch_inbox` calls 93 → 93. `profile status` is a probe, not a third refresher.

**M2 — the server-side rotation instrument.** `\d auth.refresh_tokens` first (columns on this GoTrue v2.196.0:
`instance_id, id, token, user_id, revoked, created_at, updated_at, parent, session_id`; `auth.sessions` carries
`refresh_token_counter`, empty here). Then a throwaway principal in its own team, every step snapshotting the
family (rows / revoked / with-parent for the principal's `auth.sessions.id`) and GoTrue's audit trail
(`auth.audit_log_entries`, `payload->>'actor_id'`), 12 s between refreshes so the 10 s reuse interval cannot
explain anything:

| Step | HTTP | family rows / revoked / with parent | audit `token_refreshed` / `token_revoked` |
| --- | --- | --- | --- |
| after sign-up (T0 on disk) | — | 1 / 0 / 0 | 0 / 0 |
| refresh #1 with T0 → T1 | 200, rotated | 2 / 1 / 1 | 1 / 1 |
| refresh #2 with T1 → T2 | 200, rotated | 3 / 2 / 2 | 2 / 2 |
| present T0 (two behind) | **400 `refresh_token_already_used`** | 3 / **3** / 2 | 2 / 2 (a refusal leaves no event) |
| present T2 (the leaf) after the refusal | **200, rotated → T3** (the family survives, E0-6) | 4 / 3 / 3 | 3 / 3 |
| present T2 (one behind) | 200, **returned the active token** (no rotation) | 4 / 3 / 3 | **4 / 3** |
| the adapter's `session list` on the stale file (T0, the JWT's `exp` forged into the past so it must refresh) | exit **4**, `credential revoked; run `brigade team join` again`, `session.json` deleted, exactly one Warn `credential is terminal; removing session.json` (`code: refresh_token_already_used`) | 4 / 4 / 3 | 4 / 3 |
| the adapter's `profile revoke-credentials` on the ACTIVE credential (T3) | exit 0; the `auth.sessions` row **deleted** (the family cascades away) | — | 5 / 4 + `logout` 1 |
| present the signed-out family's token | **400 `refresh_token_not_found`** | — | unchanged |
| the adapter's `session list` on the signed-out credential | exit **4**, one Warn (`code: refresh_token_not_found`) | — | unchanged |

So on this build: **the rotation counter is the row count for the principal's `session_id`** (each rotation
inserts one row whose `parent` is the previous token and revokes the previous row); GoTrue's audit trail counts a
rotation as `token_refreshed` + `token_revoked` and a one-behind redemption as `token_refreshed` alone, and it is
attributable by `actor_id` — that is the soak's server-side `/token` witness (brief 4.6 item 2). A **revoked
family** presents two ways: a two-behind refusal flags **every** row `revoked = true` yet the leaf still refreshes
(the `revoked` column is *not* the lockout signal), while a global sign-out removes the `auth.sessions` row and
the token answers `refresh_token_not_found`. The adapter's terminal branch (5.3's one lockout presentation) was
fired deliberately on both and behaved exactly as `credentials.go` says (the Warn at `:238`).

**M3 — a fabricated hint is indistinguishable from a real one.** With bob's sink watcher live and its inbox
empty: the control first, **background `fetch_inbox` calls over 60.1 s with no hints: 2** (one watcher's 30 s
live timer). Then `hints.sql` with `-v sid=<bob's session> -v n=10`: rc 0, `sent 10` in **0.077 s**,
`realtime.messages` rows on the topic **0 → 10**, `fetch_inbox` calls **+2 within 3 s** and still +2 at 8 s (the
drain plus the one coalesced follow-up of `supabase/watch.go` `onHint`), **`message offered` 0 → 0, `ack sent`
0 → 0, the sink 0 → 0 bytes**, the last `watch status` still `live`, zero WARN/ERROR lines in the adapter log.
Ten hints in one batch produce **two drains, not ten**. M7 and the pilots then measured **100 hints in ten
batches one second apart → +20/+21 drains within 3 s of the last batch** (2 per batch, the same ratio), which is
the prediction B1 is scored against. Attribution caveat: `pg_stat_statements` aggregates by statement, not by
session; the delta is attributable only against a measured background on a quiet stack — this machine ran
nothing else against the stack during phase 2 (the driver's window; checked with `ps` before every run).

**M4 — a client cannot broadcast, from code.** `supabase/migrations/20260830120100_brigade_realtime.sql`: the
`WARNING` downgrade at **:19-20**, the trigger's `perform realtime.send(…)` at **:26-27**, `No insert policy:
clients cannot broadcast; only the database emits` at **:50**, the `for select to authenticated` policy at
**:52**. `internal/adapters/supabase/realtime_integration_test.go`: `type rawSocket struct` at **:34**, `join` at
**:69**, `broadcasts` at **:103**, and no `send`/`broadcast`/`push` helper. SQL through the database container is
the only honest instrument; a client push would be silently dropped.

**M5 — the smoke session** (`soak.py --smoke`, bundle `20260905T184235Z-m5`, **1 session**). Ready in 21,321 ms;
the by-pid map `profile bob`, `inbound accept`, `permission_mode default`, `adapter_command []`; the watcher
pidfile present mid-run (its keys `brigade_session_id, pid, socket_path, start_token, token_sha256`; `pid` is
the watcher); the isolation record ok (nested pid 9334 = the map's `claude_pid`, its own session id and socket);
the `whoami` beat settled in 38,392 ms with exactly one attempt and one execution, both `brigade whoami` — **a
bare command, no dialog, no absolute path**; the `SessionStart` context line found verbatim in the transcript's
`hook_success` attachment (below); after `/exit` the pidfile and the map gone in 0 ms; `claude --version`
2.1.261 at both ends; postgres start equal; the three secret scans clean with their controls firing. The
adapter log had 0 lines at INFO.

**The 6-minute pilot** (`--minutes 6 --beat 4 --no-burst`, bundle `20260905T184406Z-pilot6`, **2 sessions**).
Both ready in 20,787 ms; setup prompts settled in 36.3/36.4 s; beat 1 settled in 42.4 s (b→a, 5 records) and
43.5 s (a→b, 6 records), 0 dialogs; the final round trip: the frame in B after 46,040 ms, 1 reply row in the
database, B's reply framed in A after 46,135 ms; both alive before `/exit`, both gone after. The scorer said what
a 6-minute run should: clauses 4, 5, 6, 8, 14 and the freeze/update/restart gate `ok`; 1, 2, 3 and 7 `FAIL`
(no 120 minutes, no rotation, no workload floor). It also exposed two instrument gaps, fixed before M6 was
scored: the roster poll's 60 s resolution reports a median `last_seen_at` gap of 60 s (the poll interval, not
the cadence) — hence the 15 s SQL sampler; and the `ps` sampler had counted the outer Claude Code session (pid
1299) as a soak process — hence the own-pid filter.

**M6 — the idle life at 1/10 scale** (`--minutes 12 --beat 4 --no-burst`, bundle `20260905T185203Z-m6`, **2
sessions**). Beats 1–3 settled on both sessions in 42.4–47.5 s (4–6 assistant records each), 0 dialogs, 0
prose matches; the final round trip: frame in B after 47,569 ms, 1 reply row, reply framed in A after 47,653 ms;
both alive before `/exit`, gone after; `claude --version` and postgres start equal; scans clean; `ack sent` ids
7 + 7, frames 5 + 4. **The log budget:** `session.log` at minute 2 → minute 12 grew 12,872 → 90,823 bytes on A
(**7,756 B/min**) and 12,458 → 78,135 on B (**6,535 B/min**); × 120 = **0.93 MB and 0.78 MB per session**,
against a rotation threshold of 200 MB (`e5s.LOG_ROTATE_BYTES`) — **no rotation needed**; the idle rate between
beats is 384 B/min (the TUI's redraw), a beat adds ~15–25 KB. `session.log` grew in the last third of the run on
both (the freeze detector). Roster heartbeats: 13 distinct `last_seen_at` per session, max gap 78.0/76.0 s, 0
`offline`. RSS over 12 min: `claude` 318→416 MB and 315→421 MB (start-up growth), watchers 15.3→19.1 and
15.4→19.4 MB, adapter children 15.8→17.0 and 15.6→16.7 MB.

**The kill mechanics, without a model** (`.ignored/tools/p5-11/author/killcheck/`). A sink watcher (pid 13034)
and its adapter child (13037, found through `pgrep -P`) on the local stack: the child SIGKILLed at
15:06:21.816 local → the watcher's `watch child ended` (exit −1) → WARN `watch child failed; restarting`
(`code unavailable`, `why child_signal`, `delay 524.5 ms`, `consecutive_failures 1`) → `watch child started` →
`watch ready` **554 ms after the kill**, new child 13039, the watcher alive. The first attempt of this check
matched the ORIGINAL `watch ready` line through a 1 s time tolerance (a −262 ms "restart"); the detector is
count-based since.

**M7 — the burst in miniature** (`burst.py --senders 6 --per-sender 3 --hints 100 --no-b3`, bundle
`20260905T190633Z-m7`, **1 session**). B2 end to end: 6 senders registered (alice × 3, dana × 3), **18 sends
issued concurrently in 121 ms, 18 accepted, 0 refused**; after 60 s: **0 drops, 0 held back, 0 deferred, `ack
sent` ids 18, frames injected 18/18 counted by id, max 3 per sender per minute, 0 drop notices, 0 rate
notices**, the server's 18 rows `injected`; at t+6 min nothing dropped to look at; B2 `ok`, bound not reached,
as 3 per sender must be. B1 in miniature: 100 hints, `realtime.messages` +100, `fetch_inbox` +20 within 3 s /
+22 by the window's end against a background of 1 per 30 s, `ack sent` 0, the channel joined. Two instrument
defects, both in B1's harness and fixed before the deliverable: (1) ten separate `docker exec psql` batches took
16.17 s of wall for "10 s" of hints (≈1.6 s of startup each) — B1 is one paced psql script since (pilots b–d:
**100 hints in a 9.08–9.12 s server span**); (2) the in-loop `READY` canary inherited the loop's 5 s expect
timeout while the model answered at ~10 s (`READY47B3` painted after "Worked for 10s"; rtt recorded 8,160 and
8,939 ms) — and its third use in the session was **refused by the provider's safeguard** (next paragraph).

**Three instrument-validation pilots** (`burst.py --senders 2 --per-sender 1 --hints 100 --no-b3
--redeliver-minutes 1`, bundles `…-pilotb`, `…-pilotc`, `…-pilotd`, **1 session each**). pilotb (the canary
timeout raised to 90 s): the DURING canary ok in 7,155 ms, the AFTER canary — the third `READY` prompt of the
session — answered by `API Error: Opus 5 (1M context)'s safeguards flagged this message … Details:
[reasoning_extraction]` (request `req_011CekqFFA6ECGZR8istTA8T`), and the two inbound-frame turns after it
refused the same way. pilotc (launched by mistake on the same canary path, counted): the same, third of three,
two refused turns after. Across the day the first and second `READY` canary of a session were never refused (12
of 12), the third was refused 3 of 3 (M7, pilotb, pilotc), and every turn after a flag was refused (6 of 6). So
the split-token canary runs once per session, at setup, and B1's probes became ordinary `brigade whoami` beats.
pilotd (that path): DURING probe ok, first reply **704 ms** after the submit mark (settled 38.4 s), AFTER probe ok,
**700 ms** (37.4 s), 0 refusals; 100 hints in a **9,076 ms** server span (9,138 ms docker wall), +21 drains
within 3 s / +23 by the window's end, background 1/30 s; B2 2/2; hygiene clean.

## What each assertion reads

| # | Assertion (brief 7) | Source and expression (`score.py`) |
| --- | --- | --- |
| 1 | two sessions, ≥ 120 min, both alive at the end | `soak-run.json` `args.minutes`; `pre_exit` per session: pidfile present, by-pid map present, the expect process alive — read BEFORE `/exit` |
| 2 | ≥ 2 rotations | `evidence/soak/profile-status.ndjson`: every change of `token_expires_at` across the 30 s series; the predicted schedule from `token_expires_at_t0` − 90 s, then every 3510 s |
| 3 | `/token` calls == rotations | `soak-run.json` `audit_t0`/`audit_t1`: GoTrue's `auth.audit_log_entries` for the principal — `token_refreshed` delta (a one-behind redemption is a `token_refreshed` without a `token_revoked`, reported separately); equality is the flock's proof, twice it is the headline finding |
| 4 | no lockout (5.3, eight clauses) | `evidence/soak/state/logs/adapter-bob.log`: zero `credential is terminal; removing session.json`, zero `lock_timeout`; `credential_file` exists / parses / mode `0o600`; every `profile status` sample `joined` with exit 0; zero `unauthorized`/`unauthenticated` in the watcher logs; the final round trip; both watchers alive with `ack sent` after t = 110 min; zero `state/*.notice` files |
| 5 | heartbeats, no lease expiry, online at t = 119 | `evidence/soak/roster.ndjson` (60 s): per session the distinct `last_seen_at` series, max gap ≤ 90 s, zero `offline`; `evidence/soak/heartbeat-sql.ndjson` (15 s): the gap distribution, zero gaps over the lease, no `closed_at`; the last roster sample's `state` in `{idle, active}` |
| 6 | the final round trip in the database | `soak-run.json` `final`: the `FINAL-<hex>` body in B's transcript (user texts and `origin.body`), ≥ 1 `brigade.messages` row from B to A after t0, and that row's id framed in A's transcript |
| 7 | workload floor | per session `beats.ndjson` outcomes `settled` with zero `provider_refusals`, frames counted by `message-id` in the transcript (never by record), `ack sent` `ids` summed: each ≥ 20 across the two sessions — **a stalled or refused soak fails here** |
| 8 | RSS slope | `evidence/soak/ps.ndjson`: per pid first / median / last RSS and a least-squares slope in KB/h, the run's own `claude` pids only; a slope × 24 h > the first sample is listed under `projected_doubling_in_24h` |
| 9 | 1,000 hints in 10 s, responsive, joined | `evidence/burst/hints/b1.json`: `sent_total` 1000, `realtime.messages` rows on the topic +1000, `sql_span_ms` ≤ 12 000 (the docker wall reported beside it), `fetch_inbox` calls within 3 s of the last batch MORE than the measured 30 s background count (the verifier found the original `≥ 1` floor satisfiable by the background alone; tightened after verification, the deliverable's +43 against 2 unchanged), `ack sent` 0, `watch child failed` 0, both probes answered with zero refusals |
| 10 | injection bounded | `evidence/burst/messages/b2.json`: max frames per sender per sliding minute ≤ 10, `bound_reached` (≥ 1 `injection queue full` Warn line), no frame injected twice, no `injection failed`; **and** the kill arm's `all_injected_exactly_once` and `all_injected_on_server` for the dropped ids; `counted: false` when B2 ran under `--pause-watcher` |
| 11 | exactly one drop notice, zero rate notices | the literal `Brigade: N messages dropped from the injection queue (limit 50); they remain on the server and will be delivered later` in A's transcript slice, count 1, with `dropped ≥ 1` asserted separately; zero `held back for rate limiting` notices |
| 12 | B3 | `b3.json`: `dropped_total` before/after, drop notices ≤ 1 inside the window, rate notices reported |
| 14 | the secret scans, each with its control | `scans/secret-scans.json`: the `brg1.` shape, the exact tokens of all three profiles + a sentinel through a 0600 pattern file outside the roots, the supply-chain shapes with a run-time-assembled `sb_secret_` canary; each control found only its planted file; the same three over every `ps -A -o args=` sample |
| — | not green if frozen, updated or restarted | per session `session.log` bytes still growing in the last third of `liveness.ndjson`; `claude --version` equal at t0 and t1; `pg_postmaster_start_time()` equal at t0 and t1; the provider-refusal count per session reported beside them |

Selftest, phase 2 (`score.py --selftest .ignored/tools/p5-11/author/score-selftest-p2`, exit 0): `synthetic green
bundle -> green=True (13 acceptance clauses)`, `re-score byte-identical=True`, the planted vacuous row →
`green=False, failing clauses=['4_no_lockout', '7_workload_floor']`, the drop notice with zero drops →
`['10_injection_bounded', '11_one_drop_notice_zero_rate_notices']`, four `/token` calls for two rotations →
`['3_token_calls_equal_rotations']`, a redelivery arm that injected a dropped id twice → `['10_injection_bounded']`,
a `--pause-watcher` B2 → `['10_injection_bounded']` with `counted: false`.

## Measured facts


All from the deliverable bundle `.ignored/proof/20260905T193637Z-soak/` (`soak-run.json`, `summary.json` sha256
`1e9b7cf6…`, reproduced byte-identically by a second `score.py` run and by the verifier's), launched 19:36:37Z as `soak.py
--kill-adapter-child` under `caffeinate -i -w 16368` (the driver's own `caffeinate -i -w <its pid>` was pid
16377, recorded in `soak-run.json`), t0 = **19:37:44.209Z**, the sessions stopped at 21:37:48–58Z, teardown done
21:38:00Z. **Two sessions started** (claude pids 16398 = A, 16399 = B; watchers 16454/16455; adapter children
16462/16469, both `--profile bob`), the account's default model `claude-opus-5`.

**E2E-12 — the soak.**

1. **Rotations per profile: 2**, from 242 `profile status` samples every 30 s (exit 0 and `joined` every time).
   `token_expires_at` moved `20:36:37Z → 21:35:10Z` (seen at t = 57.81 min) and `21:35:10Z → 22:33:40Z` (seen at
   t = 116.36 min); the predicted schedule (`t0` expiry − 90 s, then every 3510 s) was t = 57.38 and 115.88 min.
   GoTrue's audit trail puts the refreshes at **20:35:10.56Z** (86.4 s before expiry) and **21:33:40.59Z**
   (89.4 s before expiry): the margin trigger, on the first heartbeat inside it.
2. **Server-side `/token` calls: 2** — `auth.audit_log_entries` for the principal went `{}` → `token_refreshed
   2, token_revoked 2`, one-behind redemptions 0. **Equal to the rotation count, not twice it**: two adapter
   children shared `profiles/bob/session.json` through the sidecar flock for two hours and only one of them
   redeemed each refresh token; the other adopted the fresher file (`credentials.go:216`, which logs nothing at
   INFO — the adoption is proved by the count, not seen).
3. **Contention events.** Both watchers' children were inside the 90 s margin at each rotation (their heartbeats
   are ≤ 30 s apart). The brief's third contender never landed: the extra beat was scheduled at expiry − 60 s
   (4.1), but the children refresh at expiry − 90 s on the next heartbeat, so by the time the beat's window opened
   the file already carried the new token and the poller had already seen it (`refresh_beats: []`). An instrument
   correction, recorded below. `lock_timeout` errors: **0**. `credential is terminal`: **0**. `adapter-bob.log`:
   **0 bytes after two hours** (nothing at INFO on a normal refresh, an adoption, or a drain). Contended-acquisition
   latency: cited from P1-3, not re-measured.
4. **Heartbeats.** The server's `last_seen_at` by SQL every 15 s (481 samples inside the run): **257 and 254
   distinct values, median gap 30.0 s, max gap 42.0 s (A) and 43.5 s (B), min 6.5 s (an activity flip), 0 gaps
   over the 90 s lease, no `closed_at`.** The roster every 60 s (121 samples): max gap 82.0 / 81.1 s (the poll's
   own resolution), **0 `offline`**, states `idle` 115/116 and `active` 6/5; both `idle` in the t0, t60 and t119
   snapshots. Both online at the end.
5. **No lockout, all eight clauses of 5.3**: zero `credential is terminal; removing session.json`; `session.json`
   exists, parses, mode `0o600`; every `profile status` sample `joined`, exit 0; zero `lock_timeout`; zero
   `unauthorized`/`unauthenticated` in the watcher logs; the final round trip; both watchers alive with `ack sent`
   after t = 110 min (A's last acks at the final round trip, 21:37); zero `state/*.notice` files.
6. **The final round trip at t = 119 min**: A's beat was submitted at 21:36:51.7Z; A's `FINAL-6af79e` row was
   created at **21:36:53.058Z**, the frame was enqueued in B's transcript at 21:36:53.069Z (**11 ms** later) and
   written as B's user record at 21:36:53.102Z; B's reply row was created at **21:36:55.568Z** and its frame enqueued in
   A's transcript at 21:36:55.580Z — the whole round trip inside **2.5 s** of A's send. (`soak-run.json`'s
   `frame_in_b_ms` / `reply_frame_in_a_ms`, 47,739 / 47,818 ms after the final's t0, are the instants the driver
   *looked*, after A's beat had settled under the 35 s rule — observation times, not latencies; corrected by the
   verifier from the transcripts' own timestamps and the row's `created_at`.)
7. **Workload:** 23 beats answered (A: 10 beats + setup + final = 12 of 12; B: 10 beats + setup = 11 of 11) plus
   A's two B1 probes; **0 capped, 0 not submitted, 0 dialogs escaped, 0 prose matches, 0 provider refusals**;
   settle times 36.4–49.6 s (median 43.6 s A, 45.5 s B). **Frames injected: 93** by id (A 72, of which 60 were
   delivered through Claude Code's queue; B 21, 9 through the queue; **no id twice**). **`ack sent` ids: 102**
   (A 81 = 10 TICKs + 60 burst + the final and its reply; B 21).
8. **Memory and CPU over 2 h** (121 `ps` samples a minute apart; RSS in KB; the whole-run least-squares slope and
   the second-hour slope):

   | Process | first | median | last | slope KB/h (2 h) | slope KB/h (2nd hour) | CPU median / max |
   | --- | --- | --- | --- | --- | --- | --- |
   | `claude` A (16398) | 315,872 | 446,400 | 563,456 | 109,740 | 134,720 | 0.5 / 23.6 |
   | `claude` B (16399) | 315,584 | 426,208 | 469,552 | 52,775 | 42,635 | 0.5 / 14.5 |
   | watcher A (16454) | 15,040 | 19,920 | 20,800 | 1,257 | 1,220 | 0.0 / 0.3 |
   | watcher B (16455) | 15,504 | 19,488 | 19,664 | 708 | 203 | 0.0 / 0.2 |
   | adapter child A (16462) | 15,744 | 19,984 | 20,752 | 2,072 | 661 | 0.0 / 0.0 |
   | adapter child B (16469) | 15,696 | 19,856 | 20,176 | 1,799 | 245 | 0.0 / 0.0 |

   By the brief's rule (slope × 24 h > the first sample) every process "projects to a doubling" over the whole
   run, because each grew in its first minutes; over the second hour alone the rule still names **watcher A**
   (+1.2 MB/h from 19.9 MB — it posted and acknowledged 81 frames, B's 21) and both `claude` processes (Claude
   Code's own growth, +43 to +135 MB/h). The adapter children flattened to +0.2–0.7 MB/h. Recorded as a finding
   below; a 24 h soak is out of scope.
9. **Liveness of the instrument.** `session.log`: A 362,992 bytes (2,913 B/min over the run), B 280,123
   (2,218 B/min); both still growing in the last third; 0 rotations (threshold 200 MB). Drain marks 1,440 / 1,442
   (one per 5 s slice). `claude --version` `2.1.261 (Claude Code)` at both ends, the launcher resolving to
   `~/.local/share/claude/versions/2.1.261` at both ends; `pg_postmaster_start_time()` `18:22:08.310332+00` at
   both ends. Provider refusals: **0 and 0**.

**E2E-13 — the burst.**

10. **B1 at t = 60 min** (20:37:44Z, A's inbox empty of `accepted` rows): background `fetch_inbox` 2 per 30 s
    (two watchers on the stack); **1,000 hints in ten batches, server span 9,131 ms** (docker wall 9,193 ms),
    10/10 batches `rc 0`, `realtime.messages` +1000 on A's topic; **`fetch_inbox` +43 within 3 s of the last
    batch and +47 by the window's end** (about 4 drains per 100-hint batch — M3's 2 per 10-hint batch and the
    pilots' 2 per 10-hint batch, at this batch size a little more coalescing work per batch, and 21× fewer drains
    than hints); `ack sent` 0; `watch child failed` 0; no `watch status` change (still joined); 0 frames and 0
    notices in A's transcript slice. The probes: **DURING (t+5 s) first reply 985 ms** after the submit mark
    (settled 38.5 s), **AFTER (t+60 s) 645 ms** (37.5 s); 0 refusals. B's watcher, credential and heartbeat were
    undisturbed (its gaps above).
11. **B2 at t = 90 min** (21:08:39Z): 6 senders registered (alice × 3, dana × 3), **60 sends issued concurrently
    in 342 ms, 60 accepted, 0 refused** (0 `recipient_inbox_full`); the watcher: **0 `injection queue full` lines
    (`dropped_total` 0), 0 held back, 0 deferred, 0 `injection failed`, 60 `ack sent` in that second (21:08:40.01–.62Z;
    B's t = 90 TICK traffic had been acked at 21:07:57Z and 21:08:01Z, before the burst)**, every one `stdin: true`; the server: 60 rows `injected` at t+60 s; **0 drop notices, 0 rate
    notices**. Per sender per minute in A's transcript, by the corrected reading: 9, 9, 9, 8, 8, 8 — **≤ 10**.
    **The shipped race did not fill Brigade's queue** (the injector posts each frame in ~10 ms and the adapter's
    drains arrived in pages), so **the bound was not reached, no id was dropped and no notice was due**: clauses
    10 and 11 are honest negatives. The kill arm recorded `not_run: no dropped ids to redeliver`; B3 did not run.
12. **What Claude Code did with the 60 posts** (the finding of this run). A's transcript (`evidence/soak/a/
    transcript.jsonl`) holds **51 of the 60 ids**: the first delivered directly as a `user` record 53 ms after
    t0 (`isMeta`), the next 50 written as `queue-operation` `enqueue` records 10 ms apart (21:08:40.033–.515Z)
    with a `queued_command` attachment each, then 50 `remove` records at one instant (21:08:42.901Z) as the queue
    emptied into the turn; the model's reply: "Burst handled: one consolidated ACK sent to each of the six
    sessions … rather than ~50 individual replies", and six rows from A in `brigade.messages` say `msgs 1-9`
    (alice × 3) and `msgs 1-8` (dana × 3). **The last nine posts — positions 52–60 in the server's `seq` order,
    one per alice sender and two per dana sender — appear nowhere in the transcript**, yet the watcher's 60 posts
    all returned success (the socket answers nothing; `socketpost.Post` returns nil on "written and closed"),
    Brigade acknowledged them and the server marked them `injected`. Claude Code 2.1.261's inbox queue held 50
    while the turn was in flight and dropped the rest silently. Those nine messages are lost.

**Every session started in phase 2: 12** — M5 (1), the 6-minute pilot (2), M6 (2), M7 (1), pilots b/c/d (3), the
deliverable (2), the constructed arm (1). No void; no re-run.


**The constructed arm — constructed, not counted** (`burst.py --pause-watcher --kill-adapter-child`, bundle
`.ignored/proof/20260905T214157Z-constructed/`, launched 21:41:57Z, **1 session**, its own scorer summary with
clause 10 and 11 `counted: false`). Its B1 repeated the deliverable's: 1,000 hints in a 9,136 ms server span, +41
drains within 3 s (+43 by the window) against 1 per 30 s, probes 728 / 791 ms, 0 acks. Its B2 with **the watcher
SIGSTOPped while the 60 sends landed (244 ms) and SIGCONTed 1 s later** — the reader then offered the whole
backlog in a tight loop ahead of the injector: **Brigade's queue reached its bound: 4 `injection queue full;
oldest message dropped` lines, `dropped_total` 4, 56 posted and acknowledged**, the server 56 `injected` + 4
`accepted`; at t+6 min the 4 dropped ids were still `accepted` and injected 0 times (the corrected expectation,
ruling 3). **The notice, exactly once** — `Brigade: 2 messages dropped from the injection queue (limit 50); they
remain on the server and will be delivered later` — went through Claude Code's queue like any other post
(enqueue record 6 of the burst's 155, 21:45:18.387Z), so the runtime's user-record reading saw 0 and the
scorer's full-transcript reading sees 1; it names 2 because `Next` handed it out when two had been dropped and
the two later drops folded into the same 5-minute window without a second notice (B3 saw none either). Zero rate
notices; per sender per minute 9, 9, 8, 8, 8, 8. **Then the kill arm**: the adapter child (pid 25405) of A's
watcher (25390) SIGKILLed at t+360.3 s → `watch child failed; restarting` (`child_signal`, backoff 982.7 ms) → a
new child (26757) and `watch ready` **1,018 ms after the kill**; the new child re-emitted exactly the 4 dropped
ids; **4 `ack sent` after the kill; all 4 `injected` on the server; each framed exactly once in A's transcript**
(three through Claude Code's queue, one directly — the runtime's older reading said 1 of 4, the scorer's
re-derivation from the full transcript says 4 of 4, `redelivery_rederived.all_exactly_once: true`). B3 then sent
100 messages over 5.0 min (100 accepted, ~20/min across the six senders): `dropped_total` stayed 4, 0 further
drops, 0 notices, 0 held back — at that rate the injector keeps up and the queue never refills. Claude Code's
queue did the same as in the deliverable: of the 57 posts made while the turn was in flight (56 frames + the
notice) it kept 51 and **6 frames never appear in the transcript** (`never_framed` 6) — acknowledged, lost.
Hygiene as every run: `/exit`, pidfile and map gone, version equal, scans clean.



## Verbatim excerpts


The `SessionStart` context line, as the transcript's `hook_success` attachment carried it (M5, `soak-run.json`
`smoke.hook_success_attachments`):

```text
Brigade: this session is "brigade-e5s-smoke-n-i2je5-d8" (5f76bd44-c543-49fc-8295-f21546c714b6) in team "ops"; inbound: accept; teammates: run `brigade sessions`. Use `brigade sessions` and `brigade send`.
```

The lockout presentation the soak must never show, as the adapter printed it on the throwaway principal
(`phase2-baseline/cap/base-probe-list-stale.{json,err}`) — the soak's `adapter-bob.log` stayed empty:

```text
{"ok":false,"protocol_version":"1","error":{"code":"unauthenticated","message":"credential revoked; run `brigade team join` again","retryable":false,"details":{"reason":"credential_revoked"}}}
{"time":"2026-09-05T14:39:43.434589-04:00","level":"WARN","msg":"credential is terminal; removing session.json","comp":"adapter-supabase","code":"refresh_token_already_used"}
```

GoTrue's audit trail for the soak's principal, the whole two hours (`soak-run.json` `audit_events`):

```text
token_refreshed 2026-09-05 20:35:10.56256+00
token_revoked   2026-09-05 20:35:10.562962+00
token_refreshed 2026-09-05 21:33:40.587246+00
token_revoked   2026-09-05 21:33:40.587656+00
```

The watcher's supervision after a SIGKILLed adapter child (the no-model check; the same lines the constructed arm
shows):

```text
{"level":"INFO","msg":"watch child ended","exit":-1}
{"level":"WARN","msg":"watch child failed; restarting","code":"unavailable","why":"child_signal","exit":-1,"delay":524523670,"consecutive_failures":1}
{"level":"INFO","msg":"watch child started"}
{"level":"INFO","msg":"watch ready","mode":"push","session_id":"…"}
```

The provider's refusal, as the transcript recorded it (an `assistant` record, `isApiErrorMessage: true`,
`stop_reason: refusal`, `model: <synthetic>`; pilotb, request `req_011CekqFFA6ECGZR8istTA8T`):

```text
API Error: Opus 5 (1M context)'s safeguards flagged this message (https://www.anthropic.com/legal/aup). This sometimes happens with safe, normal conversations. Claude Code can't respond to this message with Opus 5 (1M context). … Details: `[reasoning_extraction]`
```

The drop notice the burst must show exactly once when a drop happens, from `internal/harness/inbound/limiter.go:192-194`
(not produced in the deliverable: nothing was dropped; produced once in the constructed arm, as a `queue-operation`
`enqueue` record at 21:45:18.387Z, naming the two drops that had happened when it was handed out):

```text
Brigade: 2 messages dropped from the injection queue (limit 50); they remain on the server and will be delivered later
```


## Teardown, and what is left behind


Every run: both `claude` ptys ended by `/exit` through the loop's `shutdown` (`how: exited`, `claude_alive_at_stop:
false` for both soak sessions), both watchers gone with their pidfiles (`pidfile released`, `watcher exiting` are
the last lines of both watcher logs; `after_exit` pidfile and map gone in 0 ms), every sender's `hook session-end`
run and its sleeper killed, the temp roots (`$TMPDIR/brigade-e5s-*`) and the secret scratch removed behind the
marker guard, the two project directories the run created under the real `$CLAUDE_CONFIG_DIR/projects/` removed
by absolute path, and the two `~/.claude.json` `projects` keys pruned (`soak-run.json` `teardown`). After the
deliverable and after the constructed arm `ps` showed no `brigade watch`, no `brigade adapter`, no `sleep
100000`, no `expect`, and no `brigade-e5s-*` directory under `$TMPDIR`. The real config files were hashed and
compared, never restored (`check_real_files`: no change reported in any run). The anonymous principals of every
run (alice, bob, dana; plus the baseline's probe) remain in `auth.users` until P5-3's gc reaps them (354 rows at
18:39Z before phase 2's runs). The join secret was deleted before any scan in every run. The `caffeinate` wrappers
exited with their runs.


## Gates


```text
$ make build                                                                              # exit 0 (make-build.status); bin/brigade sha256 f905094a…
$ python3 -B scripts/experiments/E5-soak/baseline.py --out …/phase2-baseline              # 0 FAIL lines; m1..m4 ok (exit status not captured by the launcher, see the report)
$ python3 -B scripts/experiments/E5-soak/soak.py --smoke --out …-m5                        # exit 0, 0 FAIL, 1 session
$ python3 -B scripts/experiments/E5-soak/soak.py --minutes 6 --beat 4 --no-burst           # exit 0, 0 FAIL, 2 sessions
$ python3 -B scripts/experiments/E5-soak/soak.py --minutes 12 --beat 4 --no-burst          # exit 0, 0 FAIL, 2 sessions (M6)
$ python3 -B scripts/experiments/E5-soak/burst.py --senders 6 --per-sender 3 --hints 100 --no-b3   # exit 0, 1 FAIL (B1's canary instrument), 1 session (M7)
$ python3 -B scripts/experiments/E5-soak/burst.py --senders 2 --per-sender 1 --hints 100 --no-b3 --redeliver-minutes 1   # ×3 (pilots b, c, d): exit 0; FAIL 1, 1, 0
$ python3 -B scripts/experiments/E5-soak/soak.py --kill-adapter-child                      # exit 0, 0 FAIL, 2 sessions — THE DELIVERABLE
$ python3 -B scripts/experiments/E5-soak/score.py <the deliverable>                        # exit 0: 11 ok, FAIL 10_injection_bounded, FAIL 11_one_drop_notice_zero_rate_notices; green=False; re-score byte-identical
$ python3 -B scripts/experiments/E5-soak/burst.py --pause-watcher --kill-adapter-child    # the constructed arm, 1 session — never counted
$ python3 -B scripts/experiments/E5-soak/score.py --selftest …/score-selftest-p2           # exit 0: seven detectors fire
```

The three constants the task turns on, grepped in the tree of 2026-09-05 (`b343b0b`) and equal to the brief's:
`refreshMargin = 90 * time.Second` (`internal/adapters/supabase/credentials.go:47`), `QueueCapacity = 50`
(`internal/harness/inbound/queue.go:12`), `SenderRatePerMinute = 10` (`internal/harness/inbound/limiter.go:21`).


## Honest limits

- **At the shipped `jwt_expiry = 3600` the soak observes two rotations**, a thinner sample than E0-6's 19
  refreshes at 300 s; what P5-11 adds is realism, not sample size (brief 5.1). Lowering `jwt_expiry` is a stack
  restart and a tracked-file edit, both forbidden in this lane; the lowered-expiry arm (`soak.py --minutes 45` at
  300 s, E0-6's precedent) was **deferred by the driver on 2026-09-05** (no stack restart that day) and is a
  later, separate arm. Until it runs, E0-6 is the 300 s measurement (19 refreshes, 0 `refresh_token_already_used`,
  30m28s, `E0-6.md:1-10`).
- **The shipped log level hides most of the mechanism.** The plugin's hooks pass no `--log-level`
  (`plugin/hooks/hooks.json`) and the hook reads its level from argv only, so the soak's watcher and adapter logs
  are INFO: `message queued` / `message injected` / `message offered` / `heartbeat` are DEBUG and unreachable;
  the adapter logs **nothing** on a successful drain at any level and nothing at INFO on a normal refresh or an
  adoption (the soak's `adapter-bob.log` had 0 lines after two hours). So: drains are counted
  server-side (`pg_stat_statements`), heartbeats from the server's `last_seen_at`, rotations from `profile
  status` and GoTrue's audit trail, injections from the transcript cross-checked against `ack sent`, and "50
  queued" in B2 is **inferred** (accepted − dropped), never read. The contended-acquisition latency is cited from
  P1-3 (47 of 80 contended, min 5.66 / median 16.96 / max 68.40 ms over two real processes), not re-measured.
- **`pg_stat_statements` is not per session.** The drain delta is attributable only against a measured background
  on a stack nobody else is using. During phase 2 nothing else ran against the local stack (the driver's quiet
  window); every B1 takes its own 30 s background first.
- **The dropped ids are not redelivered while the same watch child lives.** `internal/adapters/supabase/watch.go:160`
  keeps `seen map[string]bool // emitted at most once per process`, so a queue-dropped, unacknowledged message
  stays `accepted` on the server and is re-emitted only after the adapter child restarts. The brief's B2 (f)
  ("redelivered later — injected once at t+6 min") cannot hold inside a running watcher; the t+6 min reading
  records the honest form, and the redelivery is shown under a **named construction** — the child SIGKILLed, the
  shipped supervision respawning it (driver ruling 3). The construction is the kill; the restart and the
  redelivery are shipped behaviour.
- **The queue bound is a race the shipped configuration makes hard to reach.** The injector posts one frame per
  socket connect in milliseconds; sixty sends from six concurrent processes land over hundreds of milliseconds
  and arrive across several coalesced drains, so the queue's high-water mark can sit below 50 with no drop.
  The deliverable measured exactly that: 60 concurrent sends, 0 drops, every frame posted within ~600 ms — **the bound was not reached on this machine**, and the writeup says so rather than manufacturing it. `--pause-watcher` (the watcher SIGSTOPped while the sends land) is a **constructed** arrival
  condition, labelled in every record it touches and scored `counted: false` — never the shipped race.
- **The provider's safeguard is part of the environment.** On the account's default model today
  (`claude-opus-5`), the third split-token `READY` canary of a session was refused with `API Error: … safeguards
  flagged this message … [reasoning_extraction]` (3 of 3), and every turn after a flag was refused too (6 of 6):
  one flag ends a session's usefulness for the rest of a run. The rig now sends that canary once per session and
  probes responsiveness with ordinary beats, and every beat carries its refusal count. The deliverable's two sessions had 0 refusals in 2 h. A
  refused turn is not a Brigade defect — the frames were still injected and acknowledged — but a soak whose
  sessions stop answering is not a measurement of two hours, and the rig says so instead of scoring it `settled`.
- **The allow-list is a rig choice.** `Bash(brigade:*)` and `Skill` are pre-approved so no beat of a 2 h run
  stalls on a dialog, and the setup prompt tells the model to use the bare `brigade`; zero dialogs in the soak is
  therefore not evidence about D20 (brief 4.3 item 4).
- **A word match can still cost a beat.** The dialog corroborator (`bin/pending`) reads an un-executed attempt as a
  dialog; a running `brigade send` that coincides with a prose `approval`/`proceed` on screen would be Escaped
  and the turn interrupted — recorded as `dialog_escaped`, one beat lost, the next recovers. The deliverable recorded 0 dialogs and 0 prose matches.
- **The roster poll cannot see a 30 s cadence.** Its median gap is its own 60 s interval; the SQL series every
  15 s is what shows the cadence, and even it is sampled, not event-driven. The `last_seen_at` column also moves
  on the session's own activity flips, so the shortest gaps are not heartbeats.
- **One scorer floor was tightened after verification.** The verifier planted a copy of the real bundle with clause 9's 3 s drain delta set to the background rate (2 per 30 s) and the original `≥ 1` floor scored it `ok`; the floor now requires the delta to exceed the measured background. The deliverable (+43 against 2) and the verifier's standalone burst (+41 against 1) pass either way, and `summary.json` re-scored after the change is byte-identical to the verified file.
- **The isolation record checks three of four differences.** The nested pid, session id and socket are read from
  the by-pid map and compared with the outer session's; the nested token is never read (the map carries none and
  no environment is printed), so `common.isolation_verdict`'s fourth difference is not derivable here.

## Citation drift corrected (brief of 2026-09-05 → the tree at the time of writing, `b343b0b`)

| Brief cites | Today |
| --- | --- |
| `08-phases.md:133` (P5-11), `:125-137` (Phase 5 table) | `:144`; the table header is at `:133`, P5-0 (added above P5-1) at `:135`, P5-1 at `:136` |
| `09-testing.md:167` (criterion 9), `:233`/`:234` (E2E-12/13), `:172-173` (9.8) | `:173`; `:239`/`:240`; 9.8 at `:176` |
| `02-decisions.md:36-38` (D18/19/20), `:52` (D33), `:55` (D36), `:64` (D31) | `:40-42`; `:55`; `:58`; `:53` |
| `watch.go:83,84,88,95-96,100,109` | `:87, :88, :92, :99-100, :104, :111-113` |
| `attempt.go:183` (`watch ready`), `:281` (`ack sent`) | `:187`, `:287` |
| `credentials.go:237-243` (the terminal branch) | the Warn line at `:238` |
| `pipeline.go:270-338` Offer, `:307-311`, `:316-318`, `:332-335`, `:341-380` Next, `:363`, `:369-372`, `:383-400` Done | `:319`, `:376`, `:385`, `:407-409`, `:549`, `:568`, `:575-577`, `:593` |
| `pipeline_test.go:410-433` `TestBurstFromOneSenderE2E13` | `:568` |
| `limiter.go:189-194` DropNotice, `:182-187` RateNotice, `:196-201` plural, `:118-131` hit | `:192-194`, `:185-187`, `:248`, `:122-135` |
| `realtime.go:219-232` hintFor, `:403,:485,:533` | `:227` (comment from `:219`); the hint cases at `:485` and `:533` only |
| `hook.go:472-483` (the F1 comment) | `:472-483`; `startLine` from `:472` |
| `supabase/watch.go:518-543` onHint | `:504` (the call), `:518-521` (comment), the function to `:543` unchanged |
| `common.py:205-380` prelude, `287-297` submit, `299-338` onboard, `340-369` canary, `371-380` shutdown | `216-384`, `293-298`, `310-341`, `350-367`, `369-383` |
| `run_scenarios.py:70-132` run_loop | `:84-132` |
| `Makefile:274-275`, `:388-425` | `:275`, `:389-425` |
| execution log: "Model tier policy" 21–44, Status 46–98 (P5-11 at 93), "P4-3 DONE" 499–560, "P4-4 DONE" 561–617, "P4-5 DONE" 618–676, "P4-6 DONE" 677–, E0-6 journal 1066–1080 (the `jwt_expiry` sentence at 1073) | 22–45; 46–108 (P5-11 at 97); 556–617; 618–674; 675–733; 734–792; 1591–1599 (the `jwt_expiry` sentence at 1598–1599) |
| archive: "P1-3 DONE" 255–266, the decisive live defect 1430–1450 | 253–298; 848 (inside "P2 ADAPTER DONE", 810–901) |
| `E0-2.md` §(i) 114–130, "Why this suite is trustworthy" 131–165 | 114–136, 137–165 |
| `E0-6.md:118-120` | 116–120 |

Unchanged and verified by grep on 2026-09-05: `credentials.go:47` (`refreshMargin`), `:216` (adoption),
`:225-234`; `flock.go:24,32,37-39,53-81,68-77` (`lock_timeout` at `:75`); `queue.go:12`; `limits.go:30-46`
(`:38`, `:46`); `profile.go:24-33,72-88,335-354`; `supabase/watch.go:22-40,99-100,160`; `seen.go:18`;
`supervise.go:80`; `classify.go:56` (`child_signal`); the two migrations' lines; `config.toml:164,170,173,202`;
`security-threat-model.md:685-686`; `e4i.py:65` (`DONE_STOPS`); `e4i.py` and `sender.py` as cited;
`proof-headless.sh:1023,1413-1527`; `proof-idle-wake.sh:732-760`.

## Plan corrections for the driver to transcribe (brief section 9, plus phase 1's and phase 2's)

1. `internal/adapterkit/lock.go` does not exist; the flock is `internal/adapterkit/flock.go` (brief 9.1).
2. `05-supabase-adapter.md` §5.1 still prescribes the 100 ms `LOCK_NB` poll; the shipped retry is 5 ms
   (`flock.go:26-32`, E0-6, P1-3) (brief 9.2).
3. §5.1's "a token two or more steps behind revokes the family" is wrong: measured again today, twice — 400
   `refresh_token_already_used`, every row flagged `revoked`, the leaf still refreshes (brief 9.3; M2).
4. The row's "1,000-hint burst" and "one drop notice" are carried by different mechanisms: hints trigger drains
   (M3: ten hints → two drains; 100 hints in ten batches → ~20 drains; nothing injected); only messages queue
   and drop. B1/B2 is the row's operative reading, and `09-testing.md:240` already pairs the soak with the unit
   test (brief 9.4).
5. The shipped caps leave a drop window of exactly ten messages (`MaxUnackedPerRecipient = 60` − `QueueCapacity
   = 50`), stated nowhere (brief 9.5). The deliverable put 60 in flight and the server refused none; the drop, when it happens, can only be ten wide.
6. New facts for `A-verified-facts.md`: on GoTrue v2.196.0 each rotation inserts one `auth.refresh_tokens` row
   with `parent` set and revokes the previous; `auth.sessions.refresh_token_counter` is unused; a two-behind
   refusal flags the whole family `revoked` without ending it; a global sign-out deletes the `auth.sessions`
   row; the audit trail (`auth.audit_log_entries`) is attributable per principal and distinguishes a rotation
   from a one-behind redemption; a refused `/token` writes no audit event. An interactive session's
   `session.log` grows ~384 B/min idle and ~7 KB/min with a beat every 4 min (M6), ~2.2–2.9 KB/min over a 2 h soak with a beat every 12 min (280–363 KB in total) — no rotation
   is ever needed at 200 MB.
7. **`profile status` takes no `--json`** (`usage`, exit 2): the brief's M1/4.6 command and any document that
   writes `profile status --json` for the adapter are wrong; the adapter's output is always protocol JSON.
8. **A queue-dropped message is not redelivered until the watch child restarts** (`supabase/watch.go:160` `seen`,
   "emitted at most once per process"; the fs adapter has the same rule). 6.8 item 6's "left unacked, redelivered"
   and the drop notice's "will be delivered later" are true across a child restart, not within one; the brief's
   B2 (f) expectation is corrected to the measured form (the driver's ruling 3 of 2026-09-05 accepted this and
   asked for the labelled kill arm). Measured in the constructed arm: at t+6 min the four dropped ids were still `accepted` and never injected; after the child restart each was injected exactly once and acknowledged (4 acks after the kill).
9. The adapter logs nothing on a successful drain and nothing at INFO on a refresh or an adoption, and the
   plugin's hooks run the watcher at INFO with no way to raise it from `--settings`; 6.6's "logs … NDJSON" is
   true but carries none of E2E-12/13's mechanism at the shipped level (the instruments above are what a soak can
   read).
10. **The split-token `READY` canary of `E0-8/common.py` is refused by the provider's safeguard on its third use
    in a session** (`[reasoning_extraction]`, 3 of 3 on `claude-opus-5`, 2026-09-05), and a refused turn carries
    `stop_reason: refusal`, which `e4i.DONE_STOPS` treats as a completed turn. Any rig that reuses the canary
    inside a session, or judges a turn by `DONE_STOPS` alone, must read `isApiErrorMessage`. P4-5's runs
    (one canary per session) are unaffected.
11. The watcher's `pidfile` carries `pid` (the watcher), `brigade_session_id`, `socket_path`, `start_token` and
    `token_sha256`; a SIGKILLed adapter child is classified `child_signal` and restarted after a ~0.5 s backoff
    (`classify.go:56`, `supervise.go:80`; measured 554 ms kill → `watch ready`).
