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

Next arms must test a separate producer connected to an isolated shared leader
and two real TUIs. A headless reply or queue receipt alone cannot satisfy idle
wake, isolation or acknowledgment durability. See the [experiment record](../../../docs/experiments/grok-participation.md)
and [implementation plan](../../../.context/plans/grok-build-participation.md).
