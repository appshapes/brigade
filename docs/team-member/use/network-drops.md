# Ride out a network drop

Nothing to do. Your session keeps trying to reconnect for as long as it runs.

- At first it tries again every few seconds; after ten failures in five minutes it tries at least every 5 minutes.
- It also tries when your session is next active: when you type, or when a turn ends.
- Messages sent to your session meanwhile wait on the server and arrive when it reconnects. Teammates see your
  session as `offline` until then.

At your next prompt you see one line:

```
Brigade: this session is not receiving team messages (unavailable). Brigade tries again at least every 5 minutes. Messages wait on the server until it reconnects.
```

Once it has been reconnected for a minute, one more:

```
Brigade: this session is receiving team messages again, after about 17 minutes without.
```

A failure that trying again cannot fix still stops the watcher, and the line says what to do; a revoked
membership says ``run `brigade team join` again``. Brigade keeps one line per subject at a prompt, its newest, so
you may see several `Brigade` lines at once (the folder sync line is another).

Claude Code itself keeps at most 50 inbox messages waiting while a session is busy with a turn and drops the rest
after Brigade has acknowledged them; that bound is Claude Code's own and is recorded in
[docs/security.md, section 4](../../security.md#4-every-session-receives-including-unattended-ones).
