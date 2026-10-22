# Hold messages for review

Brigade delivers team messages into your session as they arrive. Set `team_inbound` to `hold` and it stops: an
arriving message is recorded (sender, summary, when) and **not** delivered and **not** acknowledged, so it stays on
the server and its sender is never told it arrived. At your next prompt the session shows one line: how many are
held, who they are from, and what to run.

## Reading and releasing

In your own terminal, never from the chat:

```sh
brigade inbox                          # what waits; each body fetched fresh, shown once, nothing stored or acknowledged
brigade inbox release --all            # hand every held message to the session's watcher
brigade inbox release <message_id>…    # or only these
```

The watcher delivers a released message the way an ordinary one is delivered, within a few seconds.
`brigade inbox release` refuses to run inside a session, so no model can release its own reading; inside a session
`brigade inbox` shows only the count and the sender names.

## What to know

- Held messages are not kept forever: nothing is acknowledged, so the backend keeps them under its unacknowledged
  retention, at least seven days on the bundled adapter, and deletes them after that.
- A session that holds also fills up: the backend refuses new messages to it once sixty are unacknowledged, and
  senders are told so.
- The sender can see the policy: `brigade send` prints a `waiting:` line saying the recipient holds its team
  messages.
- `hold` withholds messages from delivery and from the model's ordinary context. The file that holds the records is
  mode 0600 in your own state directory, and the model's shell runs as you, so it is not a sandbox against a model
  that goes looking for state files.
- `refuse` delivers nothing and acknowledges nothing, ever; `brigade inbox` in your terminal still lists what waits.
- To hold one session's messages for a while without changing the option, say `/brigade:quiet` in that session:
  [Make one session quiet](../use/quiet-session.md).
- In Claude Code, that harness's own `crossSessionInbound` setting is a second layer with rules of its own:
  [Configure in Claude Code](claude-code.md).

What the policies were proven to do: [docs/security.md, section 4](../../security.md#4-every-session-receives-including-unattended-ones).
