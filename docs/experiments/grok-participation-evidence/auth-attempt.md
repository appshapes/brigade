# Supplemental authentication observation

Coordinator observation on 2026-10-18, Grok Build 1.0.62. This is a manual record
of the initial diagnostic, not output of the final committed probe.

After `initialize`, this request was sent to the owned standalone ACP process:

```json
{"jsonrpc":"2.0","id":2,"method":"authenticate","params":{"methodId":"grok.com","_meta":{"headless":true}}}
```

The request had no response within its 20-second `asyncio.wait_for` deadline;
the caller raised `TimeoutError` and closed its owned process. No subsequent
`session/new` or model request was made in that diagnostic. No credentials or
login URL are retained here. Upstream research subsequently established that
this method starts an interactive login flow, not a cache-only lookup. Therefore
the conclusion is **authentication attempted, no completion observed**, not
"cached login failed" or "the normal TUI is logged out".

The final probe deliberately omits `authenticate`. Its current handshake snapshot
records whether a default auth method is present, and live mode stops when
`session/new` fails. [Other method errors](method-errors.json) retain the initial
queue and missing-session resume diagnostics; all targets were the invented UUID
`00000000-0000-4000-8000-000000000120` in the probe's separate runtime.
