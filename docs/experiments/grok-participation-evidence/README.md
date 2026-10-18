# Grok participation evidence (2026-10-18)

Sanitized output of the committed [probe](../../../scripts/experiments/grok-participation/probe.py)
on Grok Build 1.0.62. No session transcripts, credentials or identifying host paths.

- [Handshake and missing-target dispatch](handshake-report.json): default invocation, exit 0.
- [Live authentication blocker](live-report.json): `--live`, exit 1; session creation refused before model execution.

The initial snapshots above do not prove busy/idle delivery. See the [experiment record](../grok-participation.md).

- [Supplemental authentication attempt](auth-attempt.md): manually recorded request and timeout from the initial diagnostic.
- [Other method errors](method-errors.json): missing-target queue/resume probes, all against an invented ID.

## After normal login

- [Authenticated standalone model turn](authenticated-report.json): `probe.py --live`, exit 0.
- [Explicit leader TUI pilot](leader-tui-report.json): original windows displayed
  idle and queued busy replies; 75-second no-send control passed. Synthetic tmux
  names are omitted. This first run used the scratch predecessor of `tui_probe.py`.
- [Socket-only negative control](socket-only-negative.json): same setup without
  explicit TUI `--leader`; ACP replies succeeded but every original-TUI witness
  failed. This must not score as delivery.

The scorer accepted the leader pilot, rejected the socket-only control, and
rejected 24 mutations of required booleans, response text and completion reasons.
Long idle, trusted plugin hooks, permission holds and durable admission remain open.

- [Reproducible driver repeat](leader-tui-repeat.json): committed `tui_probe.py`, exit 0; all required content, original-TUI, isolation and cleanup checks passed.
