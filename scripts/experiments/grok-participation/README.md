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
Schema 3 retains the failed fresh 2-KiB tail case and the passed native
tool-busy wake separately. Full-frame delivery must use the
[wake/read contract](../../../docs/experiments/grok-participation-contract.md#delivery-correction-wake-notices-plus-local-retrieval);
this short-line fixture is only a bootstrap/wake probe, not a payload proof.

## Actual native package acceptance preparation

[prepare_native.py](prepare_native.py) creates a fresh 0700 temporary fixture,
using explicit **coordinator-built** native Brigade and fs-adapter binaries. It
executes only local CLI setup; it never starts Grok, installs/trusts a plugin,
changes normal configuration, reads the supplied GROK_HOME, copies authentication,
or invents a receiver map/native ID. Its only credential material is created by
the test-only fs adapter in the new scratch HOME; the preparer never opens those
credential files or the synthetic join-secret contents. The secret is outside
both the checkout and the synthetic workspace. Never export this private root.

Build the two binaries from Sol's implementation checkout into explicit temporary
output paths using the normal sandbox/toolchain. The coordinator owns that build;
this worker does not redirect a denied Go cache or relax permissions. Supply the
actual `grok/brigade` package path, not a replacement plugin or bootstrap binary:

```sh
python3 scripts/experiments/grok-participation/prepare_native.py prepare \
  --dev-bin <compiled-brigade> --fs-bin <compiled-brigade-adapter-fs> \
  --package-root <implementation-checkout>/grok/brigade \
  --grok-home <existing-normal-grok-home>
python3 scripts/experiments/grok-participation/prepare_native.py instructions \
  --root <printed-private-root>
```

`prepare` writes the scratch adapter registry (fixed executable plus private
`--root`), dev-binary pointer and `grok-options.json` with `team_inbound: accept`.
It then runs the current production CLI's `team create --adapter fs` with a
placeholder public backend pair. The fixed fs adapter never contacts that URL.
The CLI writes `.brigade.json`, the real synthetic team binding and per-checkout
consent; the preparer does not fabricate them. A `.git` directory marker follows
`commands/teamsetup_test.go`'s `mkCheckout` constructor; no Git command is run.
This is sufficient for Brigade discovery, not proof of native Grok workspace
handling. There are no production receiver hooks, maps or watchers until the
coordinator launches the native package. `harness-smoke.sh` is obsolete and must
not be used as a setup recipe (it still calls removed `brigade profile`).

Operator sequence, all actual native actions owned by the coordinator:

1. Native HOME separation preflight **passed** in retained private coordinator
   `home-probe-report.json`: normal authenticated model reply, scratch shell HOME
   and native ID matched with existing normal GROK_HOME; no auth copy/read or global
   configuration edit. This does not prove actual package hook HOME or installation
   storage. Revalidate those boundaries for each new production fixture. From the printed workspace, use the printed launch
   environment: `env -i`, scratch HOME/TMPDIR, supplied existing GROK_HOME, normal
   executable PATH, default permissions, `--no-leader`, no inherited harness/XDG
   markers or API-key environment. Before installing Brigade, verify normal login
   still works and inspect only environment **field values** from an ordinary
   approved shell command: HOME, GROK_HOME, GROK_SESSION_ID, GROK_LEADER_SOCKET.
   Keep those paths/IDs private. Do not open auth/config files. If login requires
   copying credentials, HOME is rewritten, or isolation cannot be verified, stop
   and report this gate blocked; do not fall back to normal HOME or trust bypass.
2. Inspect the native plugin inventory using its normal UI. If a Brigade package
   already exists there and is not owned by this test, stop rather than replace
   or uninstall it. Exit the preflight TUI. The printed install command is for the
   actual package via `grok plugin install <package> --trust`, in the split
   environment. It is a recommendation only; the script never executes it. Check
   installation goes to the expected native plugin store before proceeding.
3. Open a **fresh** native TUI in the printed workspace with the printed launch
   command. Native hooks must create the receiver map and watcher normally, using
   the scratch dev pointer. Confirm their actual PID/start identity, native UUID,
   canonical workspace, scratch config/state and private fs adapter binding from
   owned metadata. Do not manually create or repair maps. Invoke the package's
   real skills/`whoami` to obtain the receiver's Brigade ID; no guessed UUID.
   Verify the expected synthetic team, not any real team.
4. Complete an initial user turn and approve only the actual monitor command if
   requested. Record the native task and one-shot arming behavior. New coordinator
   evidence says Stop `backgroundTasks` has ID/type/status/description but **no
   command field**. Current Sol code restores hook-bound GROK_SESSION_ID in its
   fixed monitor command (native monitor has only GROK_AGENT) and keeps an exact
   description-only task match unverified. This is source inspection, not native
   package acceptance. Do not patch
   task metadata or mark ready from a banner to pass acceptance. The measured
   0.34-second native monitor exit after `/exit` is not production watcher proof.
5. Send only to that observed receiver using the private fixture:

   ```sh
   python3 scripts/experiments/grok-participation/prepare_native.py send \
     --root <printed-private-root> --receiver-id <observed-own-brigade-id> \
     --body-file <printed-private-root>/messages/short.txt
   ```

   This checks the exact receiver in the private fs roster, registers an ordinary
   synthetic shell sender through BAP/1, and sends the body on stdin. It never
   registers a native receiver or calls `message ack`. It returns a message ID,
   sender ID and a 600-second sender lease; adapter acceptance is explicitly
   distinct from local injection and model delivery. A mismatched/offline/non-Grok
   receiver refuses before registering the sender. If Sol's harness spelling
   differs from `grok`/`grok-build`, stop and reconcile it, not impersonate one.
6. Require the real watcher/pipeline to append to the receiver's local sink and
   ACK. Check the **owned fs backend receipt** without consuming or acknowledging:

   ```sh
   python3 scripts/experiments/grok-participation/prepare_native.py receipt \
     --root <printed-private-root> --receiver-id <observed-own-brigade-id> \
     --message-id <returned-message-id>
   ```

   `receipt` reads only this fixture's documented fs `acked` record for a message
   the helper sent to that receiver. It reports a boolean, never the body.
   Require a complete sink record plus this ACK evidence; `message send` success
   alone is insufficient. Separately require body-free native wake, real
   `brigade inbox read` pagination and generated NATIVE_WAKE_120 in the original
   TUI. The helper does not provide fake read responses or fake a sink/ACK.
7. Repeat with `messages/16k.txt`: exactly 16,384 UTF-8 bytes and fresh independent
   BEGIN/MIDDLE/END values. Keep `expected-private.json` out of the prompt/tool
   instructions and inspect it only in the operator's terminal after the reply;
   never tell the model to open it. Confirm every production read page is complete
   and the original TUI returns all three values in order. The model's sandbox is
   not assumed to prevent reading sibling files; if it reads the expected file,
   void the test. Offline byte-exact frame/page tests remain necessary. Add burst,
   held/refused and rotation cases only through normal production policy paths.
8. Close only each returned synthetic sender using the printed `adapter_prefix`
   plus `session close --session <returned-sender-id>`. For longer reply tests,
   heartbeat that sender within its lease using BAP/1; do not invent a map. Exit
   the owned TUI normally, verify its watcher/follower retirement against the
   recorded incarnation, and separately run the owned crash gate. Bound waits
   (candidate ten seconds), report survivors, and never kill by name/shared leader.
   Uninstall only the package this run installed using normal Grok CLI in the
   same split environment. Remove the printed private root only after owned
   processes stop; never remove normal GROK_HOME, histories or login material.

`instructions` also prints the exact adapter argv prefix and scratch state path
for private inspection. Manually driving protocol samples means sending JSON on
stdin, e.g. `session heartbeat --session <sender>` with
`{"activity":"idle","lease_seconds":600}`. Native receiver registration remains
exclusively the production hook's job. The script has a 20-second timeout per local
CLI command and checks binary hashes before follow-up commands; a rebuild requires
a fresh fixture. Failed preparation leaves only its private temporary root for
inspection, not a normal configuration change.

Validation at this checkpoint: three new offline orchestration tests use fake CLI
results to check isolated argv/environment, CLI-owned consent, no receiver-map or
Grok action, 16-KiB generation, receiver/path refusal, stdin bodies, ACK observation
without ACK mutation, and binary/bootstrap rejection. These do **not** prove the
real CLI setup or the native pipeline. No supplied compiled development binaries
were available to this worker, so actual fs setup/send is pending coordinator
execution; native HOME/auth preflight has passed separately, while actual package
hook isolation/registration, task readiness and full-frame retrieval remain live gates. No privileged sockets/network or Go-cache
workaround was attempted.

### Completed native gate update

The native-only 2,100-second hold now has **supplemental PASS evidence**, preserving
the raw screen-only FAIL at 128.13 seconds. Same-session native events correlate
ingress with completion at 2.489 seconds; owned assistant export contains the
exact joined nonce, the control does not. PageDown-only input after the observation
window reveals it in the original TUI, without a prompt/model nudge. Do not erase
the raw failure, call this a rerun, or infer delivery from process survival.
Use the same correlated evidence boundaries for actual package long-idle acceptance,
which remains pending with full retrieval and production lifecycle gates.

Native /exit stopped its child in 0.34 seconds; SIGKILL left the child for at least
10.59 seconds and the coordinator cleaned the owned orphan. Resume with explicit
rearm and compaction woke successfully; neither proves production rearm or cleanup.
The small bootstrap fixture above predates the missing-monitor-UUID observation;
its environment assumption must not be mistaken for current native acceptance.
Use the actual package acceptance flow with validated explicit binding. The later
leader longhold controls passed individually, but the whole run failed replay
scoring. These native records do not establish actual package production readiness.
