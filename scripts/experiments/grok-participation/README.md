# Grok Build participation probes (card 120)

The first milestone is proving delivery into an existing Grok TUI conversation,
including an idle conversation, before implementing a package. The probe below
is only the initial ACP capability/authentication check; it is not that proof.

Run with Python 3.10+ and the installed `grok` executable:

```sh
python3 scripts/experiments/grok-participation/probe.py
python3 scripts/experiments/grok-participation/probe.py --live
```

The default starts a separate `grok agent --no-leader stdio` process in a temporary
workspace. It does not attach to the user's leader, read existing sessions, change
configuration, or call a model. It checks the handshake and compares extension
method spelling against a deliberately nonexistent session ID. Expected errors
are evidence of dispatch, not successful delivery.

`--live` additionally attempts to create a synthetic session and asks for one
fixed reply. It can consume model quota. It uses the normal installed login,
never reads credentials, never launches an OAuth flow, and refuses every tool
permission request. If Grok requires authentication, complete a normal prompt in
a terminal and investigate the ACP authentication contract before retrying.
The driver closes its own session/process; Grok may retain the synthetic history.
No real transcript is copied into the report. Reports contain only allowlisted
capabilities, booleans, error codes and fixed test labels.

The temporary workspace is removed after the process exits. The JSON report is
printed to stdout; redirect it under `.ignored/` for scratch evidence. A blocked
or failed live check exits nonzero. No shared leader is stopped.

The TUI pilot below tests a separate producer connected to an isolated shared
leader and two real TUIs. A headless reply or queue receipt alone cannot satisfy idle
wake or isolation. ACK durability is not claimed by this transport pilot. See the [experiment record](../../../docs/experiments/grok-participation.md)
and [implementation plan](../../../.context/plans/grok-build-participation.md).

## Two existing terminal windows

After normal login, with `tmux` installed, run the bounded live pilot (model quota
is consumed; allow several minutes):

```sh
mkdir -p .ignored/grok-tui
python3 scripts/experiments/grok-participation/tui_probe.py \
  --scratch .ignored/grok-tui --report .ignored/grok-tui/report.json
```

The driver creates two synthetic TUIs with explicit `--leader`, a unique socket,
canonical workspace path and default permission mode. A separate ACP client
attaches by full session UUID, then sends split nonces to idle A, busy A, and B
after a 75-second quiet hold. Passing requires generated content in ACP **and**
the original TUI, no sibling agent text, completed original busy work, surviving
TUIs after producer disconnect, and cleanup. Exit 1 means failure, including
missing TUI witnesses. Progress is NDJSON on stdout; `--report` holds final JSON.

Only owned tmux sessions and leader are stopped. The private scratch includes
synthetic IDs and failure screens; temporary workspaces and Grok's synthetic
histories are retained. Do not publish private scratch wholesale. This is a
historical transport pilot, not a supported production route or hook, permission,
local-ACK or long-idle certification. Later graceful-exit ghost prompts rule out
resident-session lookup as a TUI liveness witness.
The busy turn originates from the producer, not from a human or tool execution.

## Nonleader monitor bootstrap

This is a **coordinator-operated synthetic experiment**, not a production plugin.
The installed-guide-backed fixture has offline tests but has not itself been run
live at this checkpoint. The measured coordinator pilot used a separate scratch
fixture. Python startup can exceed native hook deadlines: record a timeout as a
failed hook, never as a bootstrap pass. Do not change global settings to repair it.

Prepare private files only (Python standard library; no trust or process changes):

```sh
python3 scripts/experiments/grok-participation/monitor_fixture.py prepare
```

Keep the printed `root`, `workspace`, `plugin` and `plugin_name` privately. Substitute
those values below; do not publish them. The fixture makes an exact-cwd-guarded
plugin with SessionStart, Stop and SessionEnd hooks. Hook commands exec Python so
the parent is not an extra shell wrapper. Stop emits block/reason at most once per
native-ID/parent-PID/start-time incarnation, excludes subagents/teardown and honors
`stopHookActive`. The marker records an attempt, **not** verified readiness. The
fixture intentionally does not implement production native-task correlation.

1. Inspect the generated plugin and its exact workspace guard. With the normal
   login, the coordinator installs only this unique scratch plugin:
   `grok plugin install <printed-plugin-path> --trust`. Do not use `--plugin-dir`,
   disable trust/sandbox, edit global config, or copy credentials. Record the
   installed unique plugin name for later removal; stop on validation refusal.
2. Open a fresh terminal in the printed workspace. Remove inherited harness
   markers by prefix (CLAUDE, CODEX, MUSE, BRIGADE and GROK) from **this launch's
   environment only**, retaining normal HOME/PATH/login and CLAUDE_CONFIG_DIR if
   used. Launch `grok --no-leader --permission-mode default --no-subagents`.
   Do not attach to an existing conversation. Record the actual owned TUI PID and
   start identity privately; verify its match with the fixture owner record after
   arming. No GROK_LEADER_SOCKET should be present in its hook/model shell.
3. Submit one ordinary prompt: `Reply INITIAL_READY, then finish.` The SessionStart
   notice is UI-only. At the first eligible Stop, expect one automatic continuation
   asking the native `monitor` tool to run the fixture command with
   `persistent:true`. Review and approve **that command once**, not a broad rule.
   Record the native task ID/type/status and exact command privately. Expect
   AUTO_MONITOR_ARMED, then no repeated Stop continuations or duplicate monitors.
   A banner without a live native task is insufficient.
4. Wait untouched for a short idle period. In a second terminal append one line
   to `<workspace>/.monitor-fixture/trigger.txt`:
   `Join AUTO_ and WAKE_120 with no separator and reply only with the result.`
   The line must end in a newline. Require generated AUTO_WAKE_120 in the original
   TUI, absent from the trigger text, with no keys or typed prompt in that TUI.
   Record append, native event and model-output times separately. Repeat future
   attempts with fresh split nonces, not the same visible answer.
5. For the negative arm, prepare a **new** fixture/session and deny its one command
   permission. Require no ready claim/duplicate autonomous retry. Do not remove
   the marker to force retries. Explicitly ask the model to arm the same command
   later; that is a separate operator action. Check native task state first.
6. Only after the short case, run the plan's busy and 2,100-second no-send/idle
   cases. The fixture exits after at most 3,600 seconds, has a 16-KiB line guard,
   reads from the beginning on restart and emits one line per poll (250 ms).
   These are **fixture limits**, not native defaults or production payload proof.
   Do not send full bodies through this fixture: native monitor truncates them.
   Use the production hint/read route for the payload/burst gate.
7. Cleanup: `/exit` only the owned TUI, verify the fixture's end marker/follower
   exit within ten seconds, and check no model wake after exit. The follower also
   checks the recorded parent start identity and ends on death/reuse; it sends no
   signals. If it survives, mark cleanup failed and terminate only the recorded
   owned helper after rechecking PID/start identity, waiting up to five seconds
   before a final owned-process kill and another five-second wait. Never kill a
   leader, shared process or process found merely by name. Remove only the unique
   scratch plugin with `grok plugin uninstall <printed-plugin-name> --confirm` using the
   installed CLI's documented uninstall command; no global trust-store edits. Retain private
   fixture artifacts for inspection or remove only that printed temporary root.

The normal-exit marker is a fast signal, not a crash guarantee. The production
watcher must own retirement independently. The fixture runs `ps` only for its
selected parent's start identity and emits no diagnostics/heartbeats into monitor
stdout/stderr. Owner records contain private synthetic identity, never export them.
No promise of durable receipt or exactly-once processing follows from this pilot.

## Offline checks and evidence export

```sh
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover \
  -s scripts/experiments/grok-participation -p 'test_*.py'
python3 scripts/experiments/grok-participation/summarize_evidence.py \
  --private-dir <retained-private-coordinator-evidence> \
  --output <sanitized-checkpoint-path>
```

The exporter reads only six named owned JSON reports and selects fixed assertions;
extra account/model metadata, signatures, IDs, paths and content are never copied.
The prose hook/permission/TUI observations remain separately attributed to the
coordinator. Never commit the raw inputs or failure screens.

`tui_probe.py` now uses passive shared-client interaction handling and scorer
schema 2. Every model turn has a fresh caller prompt ID; generated chunks must
carry that ID and the result must complete uncancelled with the same ID. Replay
is drained before the initial clear, but correlation is mandatory even after that
wait. Exact content and original-TUI witnesses remain required. Schema-1 reports
remain historical evidence and cannot pass the new scorer as-is. A native field
change fails the probe; do not weaken the correlation predicate to pass it.

The old `monitor-payload-report.json` is invalidated because every case reused the
same tail. The exporter rejects it and unverified positive random-tail results.
Current schema 2 exports the failed fresh 2-KiB tail case and the passed native
tool-busy wake separately. Full-frame delivery must use the
[wake/read contract](../../../docs/experiments/grok-participation-contract.md#delivery-correction-wake-notices-plus-local-retrieval);
this short-line fixture is only a bootstrap/wake probe, not a payload proof.
