# Grok participation evidence (2026-10-18)

Sanitized output of the committed [probe](../../../scripts/experiments/grok-participation/probe.py)
on Grok Build 1.0.62. No session transcripts, credentials or identifying host paths.

- [Handshake and missing-target dispatch](handshake-report.json): default invocation, exit 0.
- [Live authentication blocker](live-report.json): `--live`, exit 1; session creation refused before model execution.

These snapshots do not prove busy/idle delivery. See the [experiment record](../grok-participation.md).

- [Supplemental authentication attempt](auth-attempt.md): manually recorded request and timeout from the initial diagnostic.
- [Other method errors](method-errors.json): missing-target queue/resume probes, all against an invented ID.
