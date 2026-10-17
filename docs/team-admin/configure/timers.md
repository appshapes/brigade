# Set the timers

A session's watcher talks to the backend on timers while nobody types: a heartbeat, and in a project that syncs
folders a read of the session list. A backend plan with a request or egress cap, such as Supabase's free plan, may
need them slower. An optional `polling` member sets them:

```json
"polling": { "heartbeat_seconds": 100, "roster_seconds": 300 }
```

| Value | What it does | Range | Default | What a longer value costs |
| --- | --- | --- | --- | --- |
| `heartbeat_seconds` | how often a session reports it is alive; a session counts as online for three heartbeats after its last | 10 to 200 | 30 | a session that ended without closing itself (a crash, a machine asleep) shows online for three heartbeats: 90 s at 30, 10 min at 200 |
| `roster_seconds` | how often a session reads the session list for folder sync; only a project with a `sync` member reads it | 15 to 3600 | 15 | a teammate's new session joins folder sync up to this long after it starts |
| `idle_close_hours` | after this long with no prompt and no model work, a session the Claude Code VS Code extension started closes itself; its next prompt re-opens it under the same id with the messages that waited. Only those sessions: the extension keeps the `claude` process after a conversation is closed and fires no hook. The CLI and `-p` runs are never closed this way | 0 to 168; 0 switches it off | 6 | a quiet but open VS Code conversation leaves the roster after this long; a message to it then waits for its next prompt instead of waking it |

- Neither timer changes how fast a message arrives: Supabase Realtime signals each message as it is sent, and a
  lost signal is caught by the adapter's own 30-second inbox check.
- The session list is the large response, about 650 bytes per session including offline ones; at 15 s it is nearly
  all of a team's egress. `100` and `300` cut it about twentyfold, at the cost of a crashed session showing online
  for up to 5 minutes and a new teammate joining folder sync within 5 minutes.
- Leave a value out to keep its default. A value Brigade cannot use (not a whole number, or out of range) makes the
  whole member unusable: the session says so in one line and runs on the defaults. An edit takes effect at the
  next session start or `/clear`. A session whose team sets a non-default value says so in one line at start.
- Every member's plugin must be 0.11.0 or later before the member is committed; 0.11.0 to 0.24.0 ignores it and
  keeps the defaults; 0.25.0 and 0.26.0 ignore `idle_close_hours` alone.
