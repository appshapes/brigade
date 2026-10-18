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
wake, isolation or acknowledgment durability. See the [experiment record](../../../docs/experiments/grok-participation.md)
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
short transport pilot, not hook, permission, durable-ACK or long-idle certification.
The busy turn originates from the producer, not from a human or tool execution.
