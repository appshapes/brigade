# 3. Architecture

Design record from 2026-08-30. Where this text and the code disagree, the code, docs/protocol-v1.md and the execution log are authoritative; the corrections block below lists what changed.

## Corrections recorded in the execution log

- 2026-08-31 — "Plan corrections from E0-8" (archive) — 3.x: the `SessionStart` context line is `stdout.strip()`, not the hook's stdout byte for byte — leading indentation and a trailing newline are removed.
- 2026-08-31 — "Plan corrections required before Phase 3 (from E0-5)" (archive) — 3.8: a plugin cannot buy `SessionEnd` budget (a `--settings` file can); the 1 s cap stands, and the budget is one shared wall-clock deadline across the parallel SessionEnd hooks.
- 2026-09-04 — "P4-4 DONE" — 3.7 case 2: "within 2 s" is the watcher's poll interval (`watch.go:84`), not a bound — detection is up to one tick plus up to 3 s of close budget, and the poll does not run during a restart backoff (`supervise.go:83-88`), so a death during a `message watch` restart is noticed up to 30 s later; measured 0.5–1.9 s (n=8) on the normal path.
- 2026-09-04 — "P4-4 DONE" — 3.7 case 1/2: "each is injected once (dedupe)" holds only while the Claude pid is kept — the seen file is keyed by CLAUDE PID (`inbound/seen.go:36-38`), so it does not carry across a crash and `--resume`; exactly-once across a restart rests entirely on the backend's `delivery_state` flip, and a message injected but not yet acknowledged at the instant of the SIGKILL is injected a second time in the resumed session (bounded, reasoned from the code, not constructed). Phase 5 candidate: key the seen file by Brigade session id or seed the new pid's file from the by-native entry.
- 2026-09-04 — "P4-4 DONE" — 3.8: the watcher-exit line is stale twice — the shipped code reads the process STATE through `procutil` so a zombie reads as dead (E0-5 correction 1, in code since Phase 3, never carried into 3.8's "`kill(pid, 0) == ESRCH`"), and "socket ENOENT" is not an exit condition (E0-5 correction 2; `lifecycle.go:68`). Measured: arm A's watcher logged `closing the session` `reason: claude_gone` and exited 0 within 0.5–1.5 s.
- 2026-09-05 — "P5-14 DONE" — 3.7 case 2: the P4-4 window is closed — a message injected but not yet acknowledged at the SIGKILL is not injected a second time after `--resume` onto the same Brigade session (`TestCrashAndResumeDedupe`, three arms with a positive and a vacuity control); the backend's `delivery_state` flip stays an independent second control. Accepted consequence (driver's ruling): a SIGKILL of Claude between the socket write and the model's consumption of the frame now loses that one frame (at-most-once, bounded by the enqueue→consume gap) where before it was double-injected — no consumption signal exists to tell the two apart. A crash followed by a FRESH registration (3.7 case 3) is unchanged: the message belongs to a session that no longer exists. 3.3's "seen as a duplicate by the second" holds for a later-starting process, not a concurrent one.

<!-- verbatim from the one-file plan -->
## 3. Architecture

### 3.1 Components

```text
+-----------------------------------------------------------------------------------------------+
| Claude Code process (v2.1.251), one per terminal or -p run                                    |
|   binds inbox socket CLAUDE_CODE_MESSAGING_SOCKET (0600), exports the per-session TOKEN       |
|   registry $CLAUDE_CONFIG_DIR/sessions/<pid>.json (name, status, socket path)                 |
|   Bash tool: PATH += <plugin>/bin (appended last); env carries CLAUDE_PID, the socket and     |
|              token, but no CLAUDE_PLUGIN_ROOT/DATA and no CLAUDE_PLUGIN_OPTION_*  [A.7]       |
|                                                                                               |
|   plugin "brigade" (plugin/, marketplace root = repo root; no MCP server, D34)                 |
|     bin/brigade           POSIX-sh bootstrap: execs the pinned release binary (per-user cache) |
|     bin/VERSION, bin/checksums.txt   the pinned version and the sha256 of its four assets     |
|     hooks/hooks.json      exec form -> ${CLAUDE_PLUGIN_ROOT}/bin/brigade hook <event>         |
|     skills/               team-messaging (allowed-tools: Bash(brigade:*)), setup              |
|                                                                                               |
|   model (Bash tool)  ->  brigade sessions | brigade send <sid> --reply-to <mid> <<'EOF' … EOF |
|                          brigade whoami   (reads the hook-written by-pid map for CLAUDE_PID)   |
|   hook (session-start) --spawn detached (Setsid)--> brigade watch                             |
|        - spawns adapter `message watch --session <id>` (NDJSON out, commands in)              |
|        - dedupes, applies the inbound policy, sanitises, frames, posts to the socket           |
|        - acks after injection, heartbeats every 30 s, exits when CLAUDE_PID dies              |
+-------------------------------|------------------------------|--------------------------------+
                                | argv + stdin JSON            | argv + stdin/stdout NDJSON
                                v                              v
             +------------------ Brigade Adapter Protocol v1 (section 4) -----------------+
             |                                                                             |
   brigade adapter supabase <group> <verb>                            brigade-adapter-fs
   (hidden subcommand of the same binary, spawned as a child            (dev and test only;
    process; hand-rolled GoTrue + PostgREST + Phoenix client)            polling watch)
   describe | profile init/status/reset/revoke-credentials |
   team create/join/leave/members | session register/heartbeat/list/close |
   message send/receive/watch/ack
             |
             | HTTPS (GoTrue, PostgREST) + WSS (Realtime, Phoenix vsn 1.0.0);
             | publishable key + anonymous-user JWT
             v
   Supabase project (local stack for the proof; hosted in Phase 5)
   schema brigade: teams, memberships, sessions, messages, join_attempts
   SECURITY DEFINER RPCs; RLS (select only); stamping triggers;
   AFTER INSERT trigger -> realtime.send(ids only) on topic brigade:session:<recipient>
   pg_cron -> gc_expired()
```

| Component | Package / path | Trust | Responsibility |
| --- | --- | --- | --- |
| Protocol library | `internal/protocol` (+ `internal/protocol/schema`, `cmd/brigade-schema`) | trusted code | Go types for every wire shape (`encoding/json/v2` tags, loose parsing by default) with a hand-written `Validate()` per type, constants (limits, retention, lease), error taxonomy and exit codes, NDJSON reader (1 MiB lines; longer lines dropped with a warning, reading continues) and writer, sanitiser, join-secret parser, JSON Schema export (draft 2020-12). No I/O. |
| Adapter kit | `internal/adapterkit`, `internal/cli` | trusted code | dispatch table with interspersed flags (stdlib `flag` plus a 20-line helper), bounded stdin document (1 MiB), result printer, XDG directory resolver, atomic 0600 writes with a world-readable refusal, `flock` helper, redacting `slog` handler, profile schema, TTY detection (termios ioctl, not `ModeCharDevice`). |
| Filesystem adapter | `cmd/brigade-adapter-fs`, `internal/adapters/fs` | test fixture | full protocol over a local directory; polling watch; the dry run for a future object-store adapter; three mutants behind build tags; never shipped. |
| Supabase adapter | `brigade adapter supabase …`, `internal/adapters/supabase` | trusted code, untrusted data | the default adapter; owns profiles and credentials; the only component that talks to the network; the GoTrue, PostgREST and Phoenix clients of section 5. |
| Conformance suite | `cmd/brigade-conformance`, `internal/conformance` | trusted code | C-01..C-43 driven through the process boundary against any adapter; `go test` subtests for CI and a CLI for third-party authors. |
| Supabase backend | `supabase/` | authority for identity, isolation, limits, retention | migrations, RLS, RPCs, triggers, realtime policy, cron, pgTAP tests. |
| Harness | `internal/app`, `internal/harness/*`, `internal/procutil` | trusted code, untrusted data | `app` (the multi-call dispatch: commands, `hook`, `watch`, `adapter supabase`); `harness/commands` (`sessions`, `send`, `whoami`, `team`/`profile` pass-through, `inbox` in Phase 5), `harness/hook`, `harness/watch`, and the library (`adapterclient`, `config`, `frame`, `socketpost`, `registry`, `sessionmap`, `pidfile`, `policy`, `inbound`; logging goes through the shared `adapterkit/log`, the one redacting handler, 7.3); `procutil` (detach, start-time token, signals). `internal/harness/**` never imports the Supabase packages (depguard, 7.3). |
| Plugin | `plugin/` | shipped artefact | `.claude-plugin/plugin.json`, `bin/brigade` (bootstrap), `bin/VERSION`, `bin/checksums.txt`, `hooks/hooks.json`, `skills/`, `README.md`. No built output. |
| Release | `.goreleaser.yaml`, `.github/workflows/release.yml`, `scripts/ci/*.sh` | build | four static binaries plus `checksums.txt` per `v*` tag, verified byte-for-byte against the committed `plugin/bin/checksums.txt` before the release is published (7.7). |

The harness never learns how an adapter authenticates; an adapter never learns anything about the Claude session beyond `session_name`, `activity`, `inbound`, `harness`, `harness_version` and an opt-in `workspace_label`.

### 3.2 Data flow and state on disk

| Location | Owner | Content | Mode |
| --- | --- | --- | --- |
| `${BRIGADE_CONFIG_DIR}/profiles/<name>/profile.json` | adapter | `{version, adapter, url, publishable_key, team_ref, team_name, principal_ref, human_label, secret_store, created_at}` (no secrets) | 0600 in 0700 |
| `${BRIGADE_CONFIG_DIR}/profiles/<name>/adapter` | harness (`brigade profile init --adapter`) | one line: the profile's default adapter, a registry name or an absolute path or a JSON array (D36); read at `SessionStart` when the `adapter_command` option is empty; never written by an adapter | 0600 |
| `${BRIGADE_CONFIG_DIR}/adapters.json` | harness (`brigade profile init --adapter <name>` registers on first use; editable by the human) | `{"<name>": "<absolute path>" \| ["<path>", "<fixed arg>", …]}`; the bundled `supabase` needs no entry; `fs` names the dev binary (D36) | 0600 |
| `${BRIGADE_CONFIG_DIR}/profiles/<name>/session.json` | adapter | the GoTrue session as returned by sign-up/refresh (`access_token`, `refresh_token`, `expires_at`, `user.id`); sidecar `session.json.lock` for the advisory `flock` (5.1) | 0600 |
| `${XDG_CONFIG_HOME:-~/.config}/brigade/dev-binary` | developer (`make plugin-dev`) | one line, the absolute path of a local build the bootstrap execs instead of the pinned release; the only override the bootstrap honours (D35); the sh bootstrap reads neither `BRIGADE_CONFIG_DIR` nor the `config_dir` option, so this path is fixed | 0600 |
| `${BRIGADE_STATE_DIR}/sessions/by-pid/<claude_pid>.json` | hook | `{claude_pid, claude_session_id, brigade_session_id, team_ref, team_name, session_name, permission_mode, non_interactive, inbound, socket_path, profile, config_dir, adapter_command, plugin_bin, harness_version, registered_at, updated_at}` (`plugin_bin` is the bootstrap's resolved realpath, for the shadowing check of 6.2); the session-bound CLI (`brigade sessions|send|whoami`) resolves everything from this file; never the token | 0600 |
| `${BRIGADE_STATE_DIR}/sessions/by-native/<claude_session_id>.json` | hook | `{brigade_session_id, team_ref, session_name, updated_at}`; survives session end so `--resume` re-opens the same Brigade session | 0600 |
| `${BRIGADE_STATE_DIR}/watchers/<claude_pid>.json` | hook/watcher | `{pid, start_token, brigade_session_id, socket_path, token_sha256}` pidfile (`O_EXCL`); `start_token` is the child's process start time as printed by `ps -o lstart=` (macOS) or `/proc/<pid>/stat` field 22 (Linux), compared byte-for-byte later (PID-reuse guard); `token_sha256` is the hex SHA-256 of the messaging token the watcher was spawned with, never the token, so a later `SessionStart` hook can detect a rotated token and respawn (D9) | 0600 |
| `${BRIGADE_STATE_DIR}/state/<claude_pid>.seen.json` | watcher | last 2,000 injected `message_id`s | 0600 |
| `${BRIGADE_STATE_DIR}/state/<claude_pid>.pending.json`, `.release.json` | watcher / `brigade inbox release` | Phase 5, under `hold`: ids and sender names seen but not injected, and the ids the human released from a terminal | 0600 |
| `${BRIGADE_STATE_DIR}/state/<claude_pid>.notice` | watcher | one line the next `prompt` hook prints once (e.g. "watcher stopped: unauthenticated") | 0600 |
| `${BRIGADE_STATE_DIR}/logs/watcher-<claude_pid>.log`, `adapter-<profile>.log` | watcher, adapter | NDJSON, redacted, rotated at 5 MB; stdout and stderr of the detached process | 0600 |
| `${XDG_DATA_HOME:-~/.local/share}/brigade/bin/brigade-<version>-<os>-<arch>` | bootstrap | the verified release binary, one file per pinned version; shared by hooks, the Bash tool and the human's terminal | 0755 in 0700 |

`BRIGADE_CONFIG_DIR` defaults to `$XDG_CONFIG_HOME/brigade` or `~/.config/brigade` (macOS and Linux; `XDG_*` values that are not absolute are ignored, as the XDG specification requires — the sh bootstrap applies the same absolute-only rule to `HOME`, `XDG_CONFIG_HOME` and `XDG_DATA_HOME` (6.2) — and `os.UserConfigDir()` is not used because it answers `~/Library/Application Support` on macOS [verified, A.7]). `BRIGADE_STATE_DIR` defaults to `$XDG_STATE_HOME/brigade` or `~/.local/state/brigade`. The harness keeps its state there rather than under `CLAUDE_PLUGIN_DATA` because the Bash tool, where the model runs `brigade send`, receives `CLAUDE_PID` but not `CLAUDE_PLUGIN_DATA` [verified, A.7]: the by-pid map must be at a path every caller can compute from `HOME` and `XDG_*` alone. `CLAUDE_PLUGIN_DATA` (`$CLAUDE_CONFIG_DIR/plugins/data/<id>/`, id `brigade-inline` for `--plugin-dir` and `brigade-brigade` for a marketplace install; the `pluginConfigs` settings key is spelled `brigade@inline` / `brigade@brigade` [verified]) is therefore referenced by nothing in v1; the consequence for uninstalling is in 6.13.

The messaging token is read from the environment by the hook, placed in the detached watcher's environment by the hook, and used only to write the socket auth line. It is never written to a file (only its SHA-256, in the pidfile), passed as an argument, logged, or placed in the adapter's environment. Whether Claude Code regenerates the token on `/clear` or in-process `/resume` is unverified (E0-5 (c)); the pidfile hash comparison in D9 makes the watcher correct either way.

Configuration sources inside a Claude Code session (hook, watcher, session-bound CLI): only `CLAUDE_PLUGIN_OPTION_*` (hooks; user-set values only, defaults are not exported [verified]), the by-pid map the hook wrote from them (CLI and watcher), `CLAUDE_PID`, `CLAUDE_CONFIG_DIR`, `CLAUDE_CODE_MESSAGING_*`, `HOME`, `XDG_*` and values the runtime computes itself. Inherited `BRIGADE_*` variables are ignored and stripped whenever `CLAUDE_PID` is set: a trusted repository's `.claude/settings.json` `env` block is written into the session's process environment and overrides the shell for every subprocess [verified: https://code.claude.com/docs/en/env-vars; https://code.claude.com/docs/en/settings: most `env` values apply after the teammate trusts the folder], so honouring an inherited `BRIGADE_CONFIG_DIR`, `BRIGADE_PROFILE`, `BRIGADE_TEAM_INBOUND` or a sink switch would let a repository redirect the session to an attacker's principal and team, change the inbound policy, or divert every inbound frame to a file. Users who relocate profiles set the `config_dir` plugin option (6.1); the terminal commands (`brigade team join`, `brigade profile …`, `brigade adapter supabase …` run by a human with no `CLAUDE_PID`) honour `BRIGADE_*` from the shell (4.1) because that environment is the user's own. The hook resolves `profile`, `config_dir` and `adapter_command` once from the options and records the absolute results in the by-pid map, which it rewrites on every `SessionStart`, so a planted map cannot survive to the first Bash call; the CLI additionally refuses a map that is not 0600 and owned by the current uid.

Environment built from scratch for adapter children (nothing else is inherited): `PATH`, `HOME`, `TMPDIR`, `XDG_*`, `LANG`, `LC_*`, `CLAUDE_CONFIG_DIR`, the proxy variables the Bash sandbox sets for its children (`HTTP_PROXY`, `HTTPS_PROXY`, `NO_PROXY` and their lowercase forms; without them a sandboxed `brigade send` could not reach the backend [verified, A.7]), `SSL_CERT_FILE` and `SSL_CERT_DIR` when non-empty (corporate CAs), plus the harness-computed `BRIGADE_PROFILE`, `BRIGADE_CONFIG_DIR` (from the map's `config_dir`), `BRIGADE_STATE_DIR` and `BRIGADE_LOG_LEVEL`. `GODEBUG`, `GOFLAGS`, `NODE_OPTIONS` and every other variable are absent by construction; `CLAUDE_CODE_MESSAGING_*` is never passed.

### 3.3 Sequence: register and heartbeat

```text
Claude Code            <plugin>/bin/brigade hook session-start          adapter (child)                 backend
    |  stdin {session_id, cwd, permission_mode, source, session_title}
    |---------------------------------------->|
    |                                         | bootstrap: dev-binary pointer? cached binary? else download+verify+install; exec
    |                                         | source=compact -> refresh by-pid map, exit 0 (no network)
    |                                         | read $CLAUDE_CONFIG_DIR/sessions/<CLAUDE_PID>.json (name, status; best effort)
    |                                         | resolve options: profile, config_dir, adapter_command, team_inbound (accept|refuse)
    |                                         | resolve the adapter (D36): adapter_command override -> profiles/<p>/adapter sidecar
    |                                         |   -> profile.json `adapter` name via adapters.json -> bundled supabase; `config` if none fits
    |                                         | live watcher for this PID, same brigade_session_id, same socket path and token hash?
    |                                         |   yes (clear/resume in-process) -> `session heartbeat` {session_name, inbound}; done
    |                                         |   same session, different socket/token hash -> SIGTERM watcher, respawn with the new
    |                                         |     values, heartbeat; done (D9)
    |                                         |   no  -> resume hint = by-native/<session_id>.json (source=resume|startup) if present
    |                                         |          `session register` stdin {session_name, activity, inbound, harness,
    |                                         |            harness_version, lease_seconds, resume?: {session_id}}
    |                                         |------------------------------------------>| credentials: read session.json (flock),
    |                                         |                                           |   refresh if < 90 s left
    |                                         |                                           | POST /rest/v1/rpc/register_session
    |                                         |                                           |   resume -> reopen if owned and closed/expired; not_found if foreign;
    |                                         |                                           |             conflict:session_live if open with a valid lease (4.5.8)
    |                                         |                                           |   new    -> insert (owner := auth.uid())
    |                                         |<------ {ok:true, result: SessionRecord + resumed, lease_seconds, server_time}
    |                                         | on not_found or conflict for the resume hint: register again without it; rewrite by-native to the new id
    |                                         | write by-pid (with profile, config_dir, adapter_command) and by-native maps (no token)
    |                                         | spawn detached `brigade watch` (Setsid; env: socket, token, CLAUDE_PID, computed BRIGADE_*)
    |                                         | warn if `command -v brigade` on the session's PATH is not the plugin's bootstrap
    |<---- stdout "Brigade: this session is "payments-api" (6f0f…) in team "ops"; inbound: accept; 2 teammates online.
    |               Use `brigade sessions` and `brigade send`; terminal commands: /path/to/plugin/bin/brigade"
    |
brigade watch (every 30 s, and on registry status flip busy<->idle)
    | stdin to adapter watch {"type":"heartbeat","activity":"busy|idle","session_name":"…"}   (capability)
    | or spawn `session heartbeat --session <id>` (fallback)
    |------------------------------------------------------------------------------>| rpc session_heartbeat -> last_seen_at = now()
```

### 3.4 Sequence: send

```text
model (Bash tool)        brigade send (session-bound CLI)                adapter (child)                   backend
  | brigade send <sid> [--summary "…"] [--reply-to <mid>] <<'EOF' … EOF     (or --body-file <path>)
  |------------------->| body from stdin: valid UTF-8, 1..16,384 bytes (byte length) else exit 3 invalid_input, no spawn
  |                    | map := ${BRIGADE_STATE_DIR}/sessions/by-pid/<CLAUDE_PID>.json (0600, own uid) else exit 11 config
  |                    |   (details.reason = "not_registered": the hook failed or the plugin was enabled mid-session)
  |                    | sender_session_id := map.brigade_session_id; profile, config_dir, adapter_command from the map
  |                    | idempotency_key := base64url(sha256(sender\0recipient\0body\0minute))
  |                    | spawn adapter [message send --profile p] stdin {sender_session_id, recipient_session_id,
  |                    |   body, summary, reply_to, idempotency_key}; 20 s timeout; argv only; allow-listed env (3.2)
  |                    |---------------------------------------------->| credentials (flock; in memory only if the config dir
  |                    |                                                |   is read-only, as under the Bash sandbox, 6.12)
  |                    |                                                | POST /rest/v1/rpc/send_message
  |                    |                                                |---> sender owned by auth.uid() (else not_found), not closed;
  |                    |                                                |     recipient same team + active member (else not_found);
  |                    |                                                |     idempotency lookup (same key: duplicate / conflict);
  |                    |                                                |     rate: <20/min, <200/h sender session; <60/min, <600/h principal;
  |                    |                                                |     <15 unacked from this sender to this recipient; <60 unacked recipient;
  |                    |                                                |     reply_to received by sender -> hop_count+1; no reply_to -> hop_count of the
  |                    |                                                |       recipient's latest message to the sender within 10 min + 1, else 0 (<=32);
  |                    |                                                |     touch sender last_seen_at; insert (stamps sender, team, time)
  |                    |                                                |     AFTER INSERT -> realtime.send(ids only)
  |                    |<---- {ok:true, result:{status:"accepted", message_id, recipient_session_id, created_at, duplicate, hop_count}}
  |<-- stdout "accepted: message 3c1a… to payments-api (6f0f…). Accepted means durably stored by the adapter, not read."
  |     (or the same as JSON with --json); exit 0
```

Errors: adapter exit code plus `{ok:false, error:{code, message, retryable, retry_after_ms?, details?}}` on stdout; `brigade send` prints one line `brigade send failed (<code>): <message>` on stderr (the same object with `--json` on stdout), exits with the code's exit status, and never shows raw adapter stderr; it retries once on `unavailable` with the same key and never retries `rate_limited` or `loop_detected`.

### 3.5 Sequence: receive, inject, acknowledge

```text
backend                 adapter `message watch --session <sid>`        brigade watch                        Claude Code socket
  |                       | wss /realtime/v1/websocket?apikey=…&vsn=1.0.0; phx_join realtime:brigade:session:<sid>
  |                       |   {access_token, config:{private:true, broadcast:{ack:false,self:false}, presence:{enabled:false}}}
  |                       | emit {"event":"ready", …} exactly once when the initial catch-up starts
  |                       | on join ok / on broadcast 'message_accepted' / every 30 s (10 s if not joined): drain()
  |                       |   rpc fetch_inbox(sid, 100): rows still delivery_state='accepted', order by seq
  |                       |   for each row: stdout {"event":"message","message":{envelope}}  (at least once)
  |                       |-------------------------------------------->| validate (loose object); dedupe by message_id (LRU 2000 + seen file)
  |                       |                                             | policy (D18): accept -> continue ; refuse -> ignore, no ack ; (hold: Phase 5)
  |                       |                                             | per-sender bucket 10/min; identical body within 60 s deferred (no ack); queue 50
  |                       |                                             | sanitise body; build <brigade-message> frame (6.7)
  |                       |                                             | pre-check socket (path == captured, not symlink, own uid, 0600)
  |                       |                                             | dial; write {"type":"auth","token":…}\n ; write {"type":"user",…}\n ; close
  |                       |                                             |------------------------------------------------>| delivered between tool calls,
  |                       |                                             |                                                 | or starts a new turn if idle
  |                       |                                             | close without error == injected
  |                       |<------- stdin {"type":"ack","message_ids":[id]}  (or spawn `message ack`)
  |<---- rpc ack_messages -> delivery_state='injected', injected_at=now()
  |                       |--- stdout {"event":"acked","message_ids":[id]} ->|
```

`injected` means exactly "the frame was written to the session's inbox socket and the connection closed without error". The socket returns nothing [verified], so nothing stronger is claimable. Under an explicit native `crossSessionInbound: refuse` the harness drops the post silently; the plugin reads the user settings file best-effort at session start and warns (6.10).

### 3.6 Sequence: hold and release (Phase 5, opt-in `team_inbound = hold`)

```text
brigade watch (policy hold): validates and dedupes as above; never injects; never acks;
                       writes state/<pid>.pending.json {ids, senders, count, updated_at}
UserPromptSubmit hook: if pending.json is non-empty: stdout "Brigade: 3 team messages held for your review
                       (from payments-api, ci-runner). Run `brigade inbox release` in your own terminal to deliver them."
                       (one line, context only; sender names pass through the sanitiser and the attribute rules of 6.7,
                       at most three names plus a count)
human (terminal):  brigade inbox                     -> lists the held messages of this user's live sessions: sender, summary,
                                                        sanitised body (read through `message receive`; nothing stored locally)
human (terminal):  brigade inbox release [--session <sid>] [--all | <message_id>…]
                                                     -> writes state/<pid>.release.json {ids}; refuses to run when CLAUDE_PID
                                                        is set ("run this in your own terminal, not from a Claude Code session")
brigade watch (2 s tick): sees release.json -> injects those messages through the accept path (frame, socket), acks them,
                          removes them from pending.json, deletes release.json
model:  receives the frames as ordinary Brigade messages and acts only on the user's instruction
```

Because nothing is acknowledged until release, a held message survives local data loss and the server's per-recipient cap tells senders `recipient_inbox_full` truthfully. The gate is the terminal: no command the model can run performs a release (the verb refuses inside a session, and under the Bash sandbox it could not write the release file anyway, 6.12), no permission prompt is involved, and nothing depends on a Claude Code feature beyond hooks and the socket. The earlier design (an MCP tool flagged `requiresUserInteraction`) is gone with D34.

### 3.7 Sequence: offline catch-up

```text
Common start:
t0  A sends m1, m2 to B while B cannot receive: rows in brigade.messages with delivery_state='accepted';
    the realtime broadcast has no subscriber and is lost (at-most-once fan-out), which is expected.

Case 1 (watcher outage, Claude Code alive):
t1  B's watcher died (crash, or SIGKILL in the test). B's lease expires 90 s later; peers see B as offline.
t2  B's next UserPromptSubmit hook finds the pidfile dead and re-spawns the watcher (by-pid map still names B's session).
t3  The adapter's first drain after the join emits every row still 'accepted' (m1, m2); each is injected once (dedupe), then acked.

Case 2 (Claude Code restarted with --resume <id>):
t1  B's Claude Code is killed (SIGKILL). The watcher sees CLAUDE_PID gone within 2 s, runs `session close` (3 s cap), exits.
t2  `claude --resume <id>` starts a new process (new PID). SessionStart(source=resume) finds by-native/<id>.json and passes
    resume.session_id to `session register`; the server clears closed_at, renews the lease, keeps the id (resumed: true).
t3  The first drain delivers m1, m2.

Case 3 (Claude Code restarted without --resume):
    A new native session is a new Brigade session; the old one shows offline and its pending messages wait for retention (7 days)
    or for a later `--resume` of the old native session. This is by design: the address is the session.
```

### 3.8 Sequence: session close and lease expiry

```text
SessionEnd (reason not in {clear, resume})   brigade hook session-end
    |-> read watchers/<pid>.json ; SIGTERM watcher ; unlink pidfile and by-pid map (by-native map is kept)
    |-> spawn adapter `session close --session <sid>` with a 1 s timeout (fire and forget; hook exits 0 regardless)
watcher on SIGTERM: stop heartbeats, write {"type":"close"} to the adapter (or spawn `session close`, 3 s cap),
                    close the child (stdin EOF, SIGTERM after 5 s), remove the pidfile, exit 0
watcher on CLAUDE_PID death (poll 2 s, kill(pid, 0) == ESRCH), socket ENOENT, or by-pid map gone: same path
server: state is 'offline' when closed_at is set or last_seen_at + lease_seconds < now(); gc_expired() deletes sessions
        closed or expired for more than 7 days together with their messages
```

`SessionEnd` hooks share a 1.5 s budget, and the hooks page now says that a longer per-hook `timeout` raises that budget up to 60 s [verified today: https://code.claude.com/docs/en/hooks]; whether a plugin's `hooks.json` timeout counts as "your settings" for that rule is [uncertain] (E0-5 (h) measures it with `timeout: 5`). The hook is written for the 1.5 s case; a raised budget only gives `session close` more room. Clean closure is an optimisation; lease expiry is authoritative (logical plan).

---
