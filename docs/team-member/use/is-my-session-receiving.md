# Check that your session is receiving

Ask your session: `Is this session receiving team messages?` It runs `brigade whoami`, whose last line is the
answer:

```
delivery: watcher running (0.31.0), connected for 12m; last delivery recorded 3m ago; 0 held
```

| The line says | It means |
| --- | --- |
| `connected for 12m` | Your session is receiving. |
| `connecting for 4s` | It is starting, or starting again. |
| `not connected for 17m and retrying` | The backend cannot be reached. Messages wait on the server. |
| `watcher not running` | Nothing arrives until your next prompt starts it again. |
| `no watcher: this session has no inbox socket` | This host gives the session no inbox; nothing is pushed to it. |
| `last delivery recorded 3m ago` | When a message last reached this session. |
| `2 held` | Two messages wait for you: [Read and release held messages](held-messages.md). |

The line is read from files on your machine; nothing is asked of the backend. The lines above it name the
session, its team and adapter, the inbound policy, the frame level and, outside Claude Code, the host (`host:
muse`, `host: codex`); the `terminal:` line is the plugin binary's path ([Use brigade in a terminal](../install/terminal.md)).

A Muse Code session is receiving once its model has armed the monitor at the first prompt; until then, or after a
`muse resume` with no prompt yet, it is a mailbox whose messages wait. A Codex CLI session is receiving once its
first prompt has registered it; until then it is unknown to the team, and a message sent to its old session waits
on the server.
