# Join in Grok Build

Use a direct `grok --no-leader` TUI with the installed trusted package. Save the administrator's secret file
outside the repository, then ask the `join` skill to use that exact path:

```
/brigade:join /absolute/path/to/the-secret-file
```

The skill runs `brigade team join --secret-file <that path>`, relays its output and never reads or prints the
secret. The hook's start facts allow a not-yet-attached session to find its configuration. A successful join
attaches at the next prompt; one already attached to another team stays there until a new session. A session
that is running when its team moves to another backend stays where it is too, and follows when it starts:
Grok Build shows nothing a prompt hook prints ([When your team moves](follow-a-move.md)).

A person may instead run `brigade team join` in their terminal in the checkout, confirm the backend and read
the secret without echo. Each checkout needs consent. The join sends the `label` of
[the Grok options file](../configure/grok.md): left out, it is `account`, and where a Claude account is on this
machine its email address is sent, as on every harness ([Choose your label](your-label.md)). Set a literal `label`
or `none` there before joining to send that instead.

Joining and monitor readiness are separate. After registration, the first eligible Stop offers one monitor
setup attempt. Approve the exact command through the normal permission prompt, or leave it unarmed and request
an explicit retry later. A membership or watcher on the roster does not prove the model is receiving events.
