# Join in Grok Build

Use a direct `grok --no-leader` TUI with the installed trusted package. Save the administrator's secret file
outside the repository, then ask the `join` skill to use that exact path:

```
/brigade:join /absolute/path/to/the-secret-file
```

The skill runs `brigade team join --secret-file <that path>`, relays its output and never reads or prints the
secret. The hook's start facts allow a not-yet-attached session to find its configuration. A successful join
attaches at the next prompt; one already attached to another team stays there until a new session.

A person may instead run `brigade team join` in their terminal in the checkout, confirm the backend and read
the secret without echo. Each checkout needs consent. Set a literal `label` or `none` in
[the Grok options file](../configure/grok.md) before joining; Grok never borrows a Claude account label.

Joining and monitor readiness are separate. After registration, the first eligible Stop offers one monitor
setup attempt. Approve the exact command through the normal permission prompt, or leave it unarmed and request
an explicit retry later. A membership or watcher on the roster does not prove the model is receiving events.
