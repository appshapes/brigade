# Read and release held messages

A session with `team_inbound: hold` records each arriving message and delivers nothing. At your next prompt it
shows one line: how many are held, who they are from, and what to run. Reading and releasing are yours, in your own
terminal:

```sh
brigade inbox                          # what waits: sender, summary, when; each body fetched fresh and shown once
brigade inbox release --all            # deliver every held message to the session, within a few seconds
brigade inbox release <message_id>…    # deliver only these
brigade inbox --session <id>           # another session of yours on this machine
```

- `brigade` in your terminal is the path `brigade whoami` prints on its `terminal:` line
  ([Use brigade in a terminal](../install/terminal.md)).
- Nothing is stored on your machine and nothing is acknowledged by looking.
- `brigade inbox release` refuses to run inside a session, so no model can release its own reading; inside a
  session `brigade inbox` shows only the count and the sender names.
- A released message is delivered the way an ordinary one is, through the session's watcher.
- A refusing session's messages (`team_inbound: refuse`) are listed the same way and can be read, never released
  into that session.
- A session you made quiet with `/brigade:quiet` holds its messages the same way, and they are listed and released
  the same way; `/brigade:quiet off` releases nothing by itself ([Make one session quiet](quiet-session.md)).

The policy itself, its retention and its limits: [Hold messages for review](../configure/hold-messages.md).
