# Codex CLI participation, reassessed: `codex queue` wakes an idle session (card 107)

Date: 2026-10-17 (morning, EDT; the log timestamps below are UTC)
Card: AppShapes Trello 107, "Implement Codex support"
Precedent: `docs/experiments/codex-participation.md` (2026-09-08, Codex 0.153.4) and row P8-1 of
`.context/plans/brigade-execution-log-archive.md`, closed `won't do` on 2026-09-09 because "Codex has no supported way
to wake a live conversation"; the Muse live test of card 88 (`muse-participation.md`) and its arms, which this run
ports.
Client: OpenAI Codex CLI **0.163.0** (`/opt/homebrew/bin/codex`, Homebrew cask, released 2026-10-15), run directly
Host: macOS 26.6 (Darwin 25.6.0, arm64), `tmux` 3.7c, `python3` 3.14, `sqlite3`
Account: the operator's ChatGPT sign-in in the real `~/.codex` (never a copied `auth.json`; nothing under `CODEX_HOME`
was isolated — see §7 for what the runs left there)
Model: `gpt-5.6-sol` at `xhigh` (the account's default; the pilot ran at `low`)
Driver: `scripts/experiments/codex-queue/` (README there)
Result: **an outside process wakes an idle Codex TUI session with `codex queue --thread <uuid> --message <text>`: the
turn starts inside the command's own 45 ms, the model's answer carries the message 1.1–1.9 s after the send, under
`--dangerously-bypass-approvals-and-sandbox` and under Codex's default profile, after 5 s and 120 s idle, with no
human and no approval; null controls of 120 s saw no turn. The session needs no arming prompt: a message queued
before anyone has typed also starts a turn. A message queued while the model is busy is held and starts the next
turn 2 ms after the current one ends. The capability P8-1 closed on exists on 0.163.0.**

Everything here ran on the real provider. Raw rollout copies, pane captures and the sign-in stayed outside the
repository; the numbers below were produced by `score.py` from Codex's own rollout log (`sessions/<date>/rollout-…
<thread-id>.jsonl`, the analogue of Muse's `session.jsonl`), the driver's timestamped log and a 50 ms poll of
Codex's queue table (`queue_1.sqlite`). Offsets are from the moment the driver's `codex queue` command returned
(40–90 ms after it started), so a turn that started during the command reads as a small negative number.

## 1. What changed since P8-1 (no model)

The September gate found hooks, plugins and a model-visible delivery route, and failed on two things: the shell
identity marker could be stripped by a nested child (ruled parity with Claude for Muse on 2026-10-16: the host's
approval policy is the gate), and no supported interface could start a turn in the user's idle conversation.
Between 0.153.4 and 0.163.0:

- **`codex queue --thread <uuid|name> --message <text>`** exists (added as a wrapper of the app-server method
  `thread/queue/add` in openai/codex PR 39092; the documentation page "Developer commands" lists it as stable:
  "Queue a text message for an existing session by UUID or exact name"). The documentation says nothing about what
  happens when the session is idle; the binary's strings carry a `start-if-idle submission` mode. A third-party spike
  (iaaorgmx/paynani issue 50, 2026-09-05) reported that on 0.153.4 a queued message woke an idle TUI and started a
  turn; the September gate did not test the command.
- **A shared app-server daemon** (`daemon_auto_start`, stable): a TUI launched without configuration overrides now
  starts or joins `codex app-server --listen unix://` from `~/.codex/packages/app-server-daemon/…`, with its control
  socket at `~/.codex/app-server-control/app-server-control.sock` (a symlink into `/private/tmp/codex-daemon-<uid>/`),
  and the TUI is a client of it. `codex agents` browses the daemon's sessions. Any `-c`, `--enable` or `--disable`
  on the command line forces **embedded mode** ("Running without the shared background server: command-line
  configuration overrides … requires embedded mode", warning f2), as does `--no-daemon`.
- **Hooks are stable** (`hooks` feature on by default; events `SessionStart UserPromptSubmit PreToolUse
  PermissionRequest PostToolUse PreCompact PostCompact SubagentStart SubagentStop Stop Interrupt SessionEnd`), live
  in `~/.codex/hooks.json`, `~/.codex/config.toml`, `<repo>/.codex/hooks.json` or a plugin's `hooks/hooks.json`, and
  a non-managed hook runs only after the user trusts its hash (`/hooks`), or under
  `--dangerously-bypass-hook-trust` for one invocation. `SessionEnd` has a 1 s default budget (3 s maximum) and, per
  the documentation, runs when the thread is archived or deleted, when Codex closes normally, or "after 30 idle
  minutes with no connected client".
- The TUI's typed follow-ups **steer** the running turn by default (`steer` graduated; "Send a follow-up while Codex
  is responding to steer it immediately").

Nothing else needed from the account: no feature gate was consulted (`codex features list` shows `hooks`, `plugins`,
`daemon_auto_start`, `sleep_tool`, `unified_exec` stable and on; `agent_message_board` and
`send_message_to_user_async` under development and off).

## 2. The arms and their scoring

Every arm launches a TUI in tmux through `run-codex.sh` (`env -i`, an allow-listed environment, the real
`~/.codex`), answers Codex's "Trust this folder?" prompt with Enter (which Codex saves to the operator's
`config.toml`, §7), **arms** the session with one prompt — "Every message that arrives after this one is a team
message. Reply to each with exactly: RECEIVED <the message text> … Reply now with exactly the word READY" — reads
the thread id from Codex's `state_5.sqlite` (`threads.cwd` = the workspace; the row exists only once the first
prompt has been sent), and never types again. Each send is one `codex queue` of `MSG<n> <hold> <12-hex nonce>` from
a process outside the TUI. Scored per send: the queue row's appearance and disappearance in `queue_1.sqlite`; the
turn start (`event_msg task_started`), the user item carrying the nonce and the model's own `RECEIVED … <nonce>`
text, all from the rollout; keystrokes the driver logged inside the window (always 0). A null control is the same
length as the longest hold with nothing sent, counted in `task_started` records. The `--yolo` evidence rule of the
Muse plan holds: no tool call in any scored session names an evidence path (the scorer prints the tool calls; the
only one in this report is the busy arm's own `sleep`).

## 3. Arm 1 — `codex queue` into an idle session (decisive)

### 3.1 Customer configuration (`--dangerously-bypass-approvals-and-sandbox`, daemon mode), thread `…063905b2bc7e`

| Send | Idle before | Queue row | Turn start | User item | Model text with nonce | Keys in window |
| --- | --- | --- | --- | --- | --- | --- |
| MSG1 | 5 s | never visible | **−0.043 s** | −0.038 s | +1.061 s | 0 |
| MSG2 | 120 s | never visible | **−0.042 s** | −0.036 s | +1.388 s | 0 |

Null control: 120 s idle with nothing sent — **0 turns started**. In daemon mode the message never touches the
queue table: the daemon hands it to the loaded thread and the turn starts while the `codex queue` process is still
running (the command took 43 ms; the turn's `task_started` is 42 ms before it returned). The model's turns took
1.1–1.6 s each (`task_started` to `task_complete`), including the arming turn, so the text latency is model time.

### 3.2 Codex's default profile (no bypass flag: workspace-write sandbox, approvals on request), thread `…b00ba78f980f`

| Send | Idle before | Turn start | User item | Model text with nonce | Keys in window |
| --- | --- | --- | --- | --- | --- |
| MSG1 | 5 s | **−0.042 s** | −0.034 s | +1.145 s | 0 |
| MSG2 | 120 s | **−0.045 s** | −0.036 s | +1.254 s | 0 |

Null control: 60 s — 0 turns. No approval record of any kind: the arming and the two wakes needed no human decision
under the default profile either (the model's reply is text; the sandbox never came into play).

### 3.3 Embedded mode (`--no-daemon`, the mode any `-c` override also forces), thread `…b5a22ca5c6a0`

| Send | Idle before | Queue row | Turn start | Model text with nonce |
| --- | --- | --- | --- | --- |
| MSG1 | 5 s | −0.003 s → +3.582 s | **+3.532 s** | +5.603 s |
| MSG2 | 60 s | +0.013 s → +8.154 s | **+8.090 s** | +9.144 s |
| pilot (`-c` overrides, `low`) | 5 s | −0.064 s → +8.794 s | **+8.749 s** | +10.405 s |

Without the daemon the sender writes the row into `queue_1.sqlite` and the TUI polls the table: the three pickups
measured 3.5 s, 8.1 s and 8.7 s, consistent with a poll of about 10 s. Same wake, slower; the row is visible for
exactly that long.

### 3.4 Before any prompt (the first-prompt question), thread `…638066018788`

The thread id of a fresh TUI is not in `state_5.sqlite` and has no rollout until the first prompt, but Codex's log
database (`logs_2.sqlite`, column `thread_id`) names it from launch. Read from there, with nobody having typed:

| Send | State | `codex queue` | Turn start | What happened |
| --- | --- | --- | --- | --- |
| MSG0 | fresh, no prompt ever | accepted (exit 0) | **+0.004 s** | the TUI showed `› MSG0 before-any-prompt …` and "Working"; the driver's arming prompt, typed 1.6 s later, steered into that same turn, which ended `READY` 2.7 s after it started |
| MSG1 | armed, 5 s idle | accepted | −0.041 s | `RECEIVED MSG1 …` +0.943 s |

So a Codex session is addressable and wakeable **before its user has typed anything**; the arming prompt Muse
needs for its `monitor` does not exist here. What a Brigade hook cannot do is learn the id at launch: SessionStart
fires at the first turn (§5), not at launch. In practice a Codex session joins when its user first types, exactly as
a Muse session does, and from then on every delivery is automatic.

### 3.5 Busy session, thread `…8896142103d7`

The armed session was told to run `sleep 45` and reply `SLEPT`; 12 s into that turn the driver queued `MSGB`.

| Queue row | Busy turn ended (`task_complete SLEPT`) | Next turn started | User item | Model text with nonce |
| --- | --- | --- | --- | --- |
| +0.011 s → +38.428 s | +38.408 s | **+38.410 s** (2 ms later) | +38.417 s | +40.259 s |

A queued message does not steer the running turn (only typed input does); it is held in the queue table and starts
its own turn as soon as the current one completes. The `exec` tool call for `sleep 45` is the only tool call in the
scored sessions.

### 3.6 Line sizes (threads `…8a1be57362c5` and `…868ca8e14e99`)

Each line is `MSG<n> size-<bytes> <nonce> <padding> END=<6 hex>`; the END marker in the model's own reply proves the
line reached it whole. The first session was armed with the standard text ("RECEIVED <the message text>"), so the
model echoes the whole line; the second with a short-reply text ("RECEIVED MSG<n> <nonce> END=<marker>").

| Line | Size (with newline) | Reply form | Turn start | Whole line reached the model | Turn completed |
| --- | --- | --- | --- | --- | --- |
| MSG2 | 64 B | echo | −0.041 s | yes (`END=683da6` in the reply) | yes, 2.7 s |
| MSG3 | 2,048 B | echo | −0.044 s | yes | yes, 3.3 s |
| MSG4 | 8,192 B | echo | +1.419 s (queued behind MSG3's turn) | yes (reply 14,584 chars) | yes, 14.7 s |
| MSG5 | 16,384 B | echo | +11.383 s (behind MSG4's turn) | user item whole | **no**: a Reasoning item 6 s in, the pane streamed `RECEIVED MSG5 …`, no agent message committed, `thread_turns.status = inProgress` 15 min later and after a `/quit` + resume |
| MSG2 (second session) | 16,384 B | short | −0.048 s | yes (`END=b09de7`) | yes, 1.1 s |
| MSG3 (second session) | 32,768 B | short | −0.131 s (the command itself took 0.34 s) | yes (`END=7233a1`) | yes, 1.9 s |

A 16 KiB and a 32 KiB message pass through `codex queue`'s argv and the daemon whole and start a turn as fast as a
64-byte one. The one hung turn was the model's own 16 KiB echo, not the delivery: with a short reply the same size
completes in a second. **That hang matters for the design, though:** while the turn stays `inProgress` the TUI's
composer shows "tab to queue message", `/compact` and `/new` typed into it are never executed, every later
`codex queue` row for the thread is accepted and parked (three were still in `queue_1.sqlite` after `/quit` and
`codex resume`), and the resumed TUI sits at its composer without draining them. The first follow-up arm's
`/compact`, `/new` and resume rows were therefore void (they ran behind the hung turn); the second arm, below,
waited for each turn to complete in the rollout before the next step.

### 3.6a `/compact`, `/quit` + `codex resume`, `/new` (completion-gated, thread `…8093ecf5819e`)

Each step waited for the previous turn's `task_complete` in the rollout. The 2 KiB and 8 KiB echo lines passed again
(turn start −0.045 s and −0.046 s, whole, completed in 4.6 s and 23 s).

| Step | What happened | Send after it | Turn start | Model text with nonce |
| --- | --- | --- | --- | --- |
| `/compact` typed 0.1 s after MSG3's turn completed | the command sat in the composer; MSG4 queued 98 s later started at once and ran 62 s (model time at `xhigh`); the compaction ran as its own turn (`turn_trigger` none, "Context compacted · 3s") **after** MSG4's turn, 10:35:30–10:35:34 | MSG5, 4.6 s after the compaction completed | **−0.055 s** | +1.467 s |
| `/quit`, then `codex resume <id>` | SessionStart `resume` (§5); no queued row waiting this time | MSG6, 13 s after the resume | **−0.044 s** | +1.215 s |
| `/new` typed while the compaction turn was starting (both arms) | never executed: it stayed in the composer with its autocomplete hint; the dedicated run is §3.6b | — | — | — |
| MSG7, 16,384 B, echo form, last | turn started −0.043 s, user item whole, **turn never completed** (240 s), as in §3.6: the 16 KiB echo hangs the turn 2 of 2 | — | — | — |

A compacted thread wakes exactly as before (no re-arming, nothing to re-arm); a resumed thread wakes as before, and
(§3.8) also receives whatever was queued while it was away.

### 3.6b `/new` on an idle session (hook probe installed; threads `…d659902ccfbf` → `…882b7d3c0750`)

`/new` (typed, Enter, then Enter again on Codex's "Where should the new conversation run?" question) opens a fresh
chat in the same TUI on a **new thread id**. Measured in two sessions:

| Fact | Measured |
| --- | --- |
| Hooks at `/new` | none: no `SessionStart` for the new thread (the probe's store still held one `startup` record) |
| `codex queue` to the **new** id before its first prompt | **refused**, exit 1, `no rollout found for thread id …` (unlike a fresh TUI's thread, which the daemon had loaded, §3.4) |
| `codex queue` to the **old** id after `/new` | accepted; the old thread **ran the turn on the daemon** (`task_started`, the model's `RECEIVED …` in the old rollout, `UserPromptSubmit` fired with the old id) and **the TUI showed nothing of it** |
| The user's first prompt in the new chat | `SessionStart` fired again, `source: startup`, for the new thread; its rollout and `threads` row appeared |
| `codex queue` to the new id after that | accepted; turn started; the message and the model's reply visible in the TUI (+10 s to the pane under a 60 s-busy daemon) |

So `/new` is a session change for Brigade: the old registration keeps pointing at a thread that still answers,
unseen; the new thread cannot be reached until the user's first prompt in it, which is also when the hook can
register it. `/clear` was not run (the documentation describes it as `/new` plus a cleared screen).

### 3.7 The ≥ 35-minute hold and its null control (customer configuration)

Two sessions launched 25 s apart and left alone for **2,100 s** while the other arms ran on the same daemon (eleven
other TUI sessions came and went in that window, each with its own thread; a wrong-thread delivery would have shown
as a turn in one of these two).

| Session | Idle | Send | Turn start | Model text with nonce | Keys in window |
| --- | --- | --- | --- | --- | --- |
| hold `…326b392790b5` | 2,100 s | MSG1 | **−0.041 s** (`turn_trigger: queue`) | +1.069 s | 0 |
| null `…6bd0ac3b2063` | 2,100 s | none | **0 turns started** in the window | — | 0 |

After 35 minutes idle the session woke exactly as after 5 s, and the armed session with nothing sent started no
turn of its own in 35 minutes.

### 3.8 A thread whose TUI has gone

| Target | `codex queue` | Then |
| --- | --- | --- |
| the pilot thread, embedded mode, quit 19 min earlier | accepted, exit 0, row stored | `codex resume <id>` 3 min later delivered it **3.3 s after the resume**, the model replied `RECEIVED MSGX …` |
| the hooks-arm thread, daemon mode, TUI quit 1.7 min earlier | accepted, exit 0, row stored; **no turn in 60 s** (the daemon runs no turn for a thread with no client) | `codex resume <id>` delivered it 3.2 s after the resume |
| a thread id that never existed | **refused**, exit 1: `thread/queue/add failed: failed to read thread: … no rollout found for thread id …` | — |

**Exit 0 means stored, not delivered.** A message queued to a session whose TUI has quit waits in `queue_1.sqlite`
and lands, as a user turn, the next time that thread is resumed. Brigade must therefore decide liveness itself
(the session's registry and heartbeat, as today) and never queue to a session it believes gone, or a stale frame
greets the user's next resume.

## 4. Arm 5 — the model's shell (customer configuration, daemon mode)

The model ran `fixture/shell-facts.sh` once. Its shell is a child of the **daemon** (`ppid` = the `codex app-server`
process, not the TUI) and inherits the daemon's full environment — here the operator's login environment (`FNM_*`,
`HOMEBREW_*`, …) although the TUI that started the daemon was launched with `env -i` — plus `CODEX_SESSION_ID`,
`CODEX_THREAD_ID` (both equal to the thread id), `CODEX_TOOL_CALL_ID`, `CODEX_VERSION`, `CODEX_CI`,
`CODEX_APPLY_PATCH_PRESERVE_LINE_ENDINGS`; `CODEX_SANDBOX` and `CODEX_SANDBOX_NETWORK_DISABLED` unset; `ps` sees
990 processes; writes under `$HOME` and `/tmp` succeed; an HTTPS connection to the Supabase host succeeds. The
P8-1 facts stand (the markers are mutable and absent from a `env -u` or nested child; the Muse ruling applies).
The default profile's sandbox was not measured here (Muse §4.2 showed why it matters; for Codex the shape below
keeps the watcher outside the model's shell in any case).

## 5. Arm 3 — hooks at project level (`<workspace>/.codex/hooks.json`, `--dangerously-bypass-hook-trust`, daemon mode)

| Fact | Measured |
| --- | --- |
| Events before the first prompt | none (the TUI sat at its composer 1 s; nothing fired at launch) |
| At the first prompt | `SessionStart` (`source: startup`) 10:20:26.268Z, `UserPromptSubmit` +0.24 s, `Stop` +1.8 s |
| On the queued message `MSG1` | **`UserPromptSubmit` fired** (`prompt` = the queued text, with `turn_id`) and `Stop` fired after the reply |
| On `codex resume <id>` | `SessionStart` (`source: resume`); the stale queued row was delivered and fired `UserPromptSubmit` |
| On `/new` | nothing; at the new chat's first prompt `SessionStart` (`source: startup`) for the new thread (§3.6b) |
| On `/quit` | **`SessionEnd` did not fire** (0 records from two hooks, one ticking every 0.1 s), in both sessions quit this way; the documentation's "after 30 idle minutes with no connected client" was not waited for |
| stdin fields | common `cwd hook_event_name model permission_mode session_id transcript_path` (a real path); `SessionStart` adds `source`; `UserPromptSubmit` adds `prompt turn_id`; `Stop` adds `last_assistant_message stop_hook_active turn_id` |
| Environment names in a hook | `HOME LANG LOGNAME PATH PWD SHELL SHLVL TERM TMPDIR USER _ __CF_USER_TEXT_ENCODING` — no `CODEX_*`, as on 0.153.4 |
| Hook parent | the **TUI** process (`ppid` = `codex --dangerously-bypass-…`), not the daemon; `cwd` = the workspace |
| Detached SessionStart child (Brigade's spawn shape) | alive 2.5 min later and **after `/quit`**; killed by the driver |
| A `Stop` hook that prints plain text | "Hook failed — hook returned invalid stop hook JSON output" in the transcript (a `Stop` hook must print JSON or nothing) |

The arming prompt made the model treat the later "run `shell-facts.sh`" prompt as a team message (`RECEIVED …`), so
the shell facts above come from Arm 5's unarmed session.

## 6. Capability table

Verdicts as in the Muse plan's 5.9: PASS / FAIL / CONDITIONAL / NOT RUN; facts from the rollout, the driver log, the
queue-table poll and the hook probe's private store; the model's own text scored, never an echo.

| Row | Verdict | Evidence |
| --- | --- | --- |
| Route A: `codex queue` wakes an idle session after 5 s and 120 s, customer configuration | **PASS** (turn start −0.04 s, text +1.1 s, +1.4 s) | §3.1 |
| Route A under Codex's default profile | **PASS** (−0.04 s; text +1.1 s, +1.3 s; no approval) | §3.2 |
| Null controls (120 s, 60 s) | PASS (0 turns) | §3.1, §3.2 |
| Wake without any human decision | PASS in both profiles | §3.1, §3.2 |
| Wake before the session's first prompt | **PASS** (+0.004 s; no arming needed) | §3.4 |
| Embedded mode (`--no-daemon`, or any `-c`) | PASS, polled: +3.5 s, +8.1 s, +8.7 s | §3.3 |
| Message queued while busy | PASS: held, starts the next turn 2 ms after the current one ends; does not steer | §3.5 |
| ≥ 35-minute hold | **PASS** (turn start −0.041 s, text +1.1 s) | §3.7 |
| 35-minute null control | PASS (0 turns in 2,100 s) | §3.7 |
| Lines of 2, 8, 16, 32 KiB arrive whole | PASS (END marker in the model's reply for every size) | §3.6 |
| The model's turn completes when asked to echo a 16 KiB line | FAIL (2 of 2 hang `inProgress` and block the thread; a short reply completes) | §3.6, §3.6a |
| Wake after `/compact` | PASS (−0.055 s, 4.6 s after the compaction) | §3.6a |
| Wake after `/quit` + `codex resume` | PASS (−0.044 s), and the rows queued while away are delivered | §3.6a, §3.8 |
| `/new`: hooks, old id, new id | no hook at `/new`; the old thread answers unseen; the new thread is refused until its first prompt, which fires `SessionStart` | §3.6b |
| Exit status as a delivery signal | FAIL: 0 = stored; a quit thread's row waits for the next resume; only a thread with no rollout is refused | §3.8 |
| Hooks: SessionStart / UserPromptSubmit / Stop fire; stdin fields | PASS | §5 |
| UserPromptSubmit fires on a queued-message turn | **PASS** (unlike Muse's monitor wake) | §5 |
| SessionStart at launch | FAIL: at the first turn (as Muse) | §5 |
| SessionEnd on `/quit` | FAIL: did not fire | §5 |
| Detached SessionStart child survives the session | PASS | §5 |
| `CODEX_THREAD_ID` in hooks | absent (as 0.153.4) | §5 |
| `CODEX_THREAD_ID` / `CODEX_SESSION_ID` in the model's shell | present, equal to the thread id; the shell is the daemon's child | §4 |
| Raw JSON-RPC to the daemon through `codex app-server proxy` | NOT RUN (the control socket expects a WebSocket handshake; the proxy relays bytes and the daemon logs `httparse error: invalid token`) | `daemon-probe.sh` |
| Brigade's Claude package on Codex | NOT RUN here (0.153.4: plugin installs, `${PLUGIN_ROOT}` hooks run, bare `bin/` not on PATH) | `codex-participation.md` |
| Identity poison / nested child | NOT RUN (0.153.4's result stands; ruled parity with Claude on 2026-10-16) | `codex-participation.md` |

## 7. What the runs left on this machine, and the clean-up

Launching the TUI wrote to the operator's `~/.codex/config.toml`: `[tui] screen_reader_detection_done = true`, a
`"gpt-6.1-sol"` counter under `[tui.model_availability_nux]`, and one `[projects."<test workspace>"] trust_level =
"trusted"` table per workspace whose trust prompt the driver answered (`work-arm1-yolo`, `work-probe2`,
`work-arm1-fresh`, `work-arm1-fresh2`, `work-arm1-default`, `work-arm1-embedded`, `work-arm1-busy`,
`work-arm3-hooks`, `work-arm5-yolo`, `work-arm1-follow`, `work-arm1-long`, `work-arm1-null`, `work-pilot`); the
test threads are in the operator's Codex history (`codex resume` picker) and the shared daemon it started is
running. The driver's `cleanup.sh` then archived every test thread (`codex archive <id>`; they stay in the
operator's history as archived, with their rollouts), removed the trust tables for the test workspaces from
`config.toml` (the two `[tui]` entries stay: Codex rewrites them at every launch), stopped the daemon
(`codex app-server daemon stop`) and killed the tmux server and the pollers. The two threads with a hung 16 KiB
turn (§3.6) still hold three and one parked queue rows; archiving them is what keeps those rows from landing.

## 8. What this does and does not prove

**Proved on this machine, this build, this account.** An outside process running `codex queue --thread <uuid>
--message <text>` starts a turn in an idle Codex TUI session within the command's own run time (daemon mode) or
within one queue poll of about 10 s (embedded mode), under the bypass flag and under the default profile, after idle
holds of 5 s, 120 s and 35 minutes, with the text reaching the model as a user message and no human decision anywhere.
It also works before the session's first prompt, and a message queued mid-turn starts the next turn. `SessionStart`,
`UserPromptSubmit` and `Stop` fire with the thread id, `UserPromptSubmit` fires for a queued-message turn, and a
child the SessionStart hook detaches outlives the session. That is the capability P8-1 closed on, by the route it
said did not exist.

**Not proved, and what each means for a build.**

- *Delivery is not confirmed by the sender.* Exit 0 is "stored". A thread whose TUI has quit keeps accepting rows,
  and they land on the next resume. Brigade's watcher must stop delivering when the TUI is gone: `SessionEnd` does
  not fire on `/quit` in daemon mode, so the signal is the TUI process (the hook's parent pid, which the SessionStart
  hook can record) going away, as Claude's `CLAUDE_PID` liveness poll does today, plus the lease.
- *The thread id reaches Brigade at the first turn.* `SessionStart` fires then, not at launch; before it, nothing of
  Brigade's knows the session exists. The user types once, and the session is a peer.
- *The queued text is a user turn.* The model sees it as something its user typed; the TUI shows it as `› <text>`.
  Brigade's frame preamble (the Muse-receiver variant with host-neutral prose) carries the "this is a team message"
  statement; the `UserPromptSubmit` hook sees the same text in `prompt` and can recognise Brigade's own frames.
- *Any local process can type into any session.* `codex queue` has no admission control beyond the user account
  and the 0700 socket directory; that is the same reach as the keyboard of the same user, and Muse's gate has no
  counterpart here. The P8-1 ruling on markers applies unchanged.
- *Packaging.* Codex plugins bundle hooks (`hooks/hooks.json`, string commands run through a shell) and installing
  one does not trust its hooks: the user trusts them once in `/hooks`, or the package documents
  `--dangerously-bypass-hook-trust`. The 0153.4 finding that nothing puts a plugin's `bin/` on PATH stands; the
  Muse package's D9 answer (the SessionStart line names the bootstrap's absolute path) applies.
- *Embedded mode is slower, not closed.* A user who runs `codex -c …` or `--no-daemon` gets the ~10 s poll.
- *Cost.* Each wake is a model turn on the account; nothing here measured quota.

**Decision rule (the Muse plan's 5.10, applied).** Outcome 2 — idle wake without a human — turns on route A: wake
within seconds ✔, in both profiles ✔, null controls ✔, before the first prompt ✔, busy ✔, the ≥ 35-min hold ✔,
`/compact` ✔, resume ✔. The one fact for Rjae's acceptance is weaker than Muse's: the session is reachable
only once `SessionStart` has run, which is the user's first prompt — but no arming text, no model turn and no
`monitor` is needed, and a message sent before that moment is not lost if Brigade learns the id, only unknown to it.
