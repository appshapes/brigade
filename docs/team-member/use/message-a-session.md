# Message a session or a member

Ask your session and name the teammate's session by its SESSION cell:

```
Tell bbbbb that the tenant_id migration has landed.
```

Your session runs `brigade send bbbbb` with your message on stdin. The answer says who got it:

```
accepted: message 3c1a… to 6f0f…bbbbb. Accepted means durably stored by the adapter, not read.
recipient: bbbbb, idle, inbound accept, name "billing" (unverified)
```

A `waiting:` line follows when the recipient cannot read the message yet:

```
waiting: the recipient was offline when this was sent; the message waits until that session runs again
waiting: the recipient holds team messages; this one waits until its human releases it
waiting: the recipient refuses team messages; this one waits, unread, while it does
```

- The five characters are the end of the session's id; the id in full works too. When two sessions end with the
  same characters nothing is sent and the answer lists both ids in full. Your own five are the `short id:` line
  of `brigade whoami`, for a teammate who asks how to reach you.
- Only an id is an address. A name is not: anyone can copy a name.
- `--summary "<one line>"` gives the message a summary; `--reply-to <message_id>` threads a reply;
  `--body-file <path>` carries a body over about 8 KB, which a shell heredoc cannot.
- Send text only: findings, decisions, questions, status. Never a secret, a credential file, a transcript or a
  file a teammate did not ask you for; what you send is stored in plain text on the backend.

## A person, not a session

```
brigade send --member alice@example.com <<'EOF' … EOF
```

picks that member's best session the way the gateways do: the gateways and the sessions that refuse messages left
out, active before idle before offline, the most recently seen among equals. `<who>` is their MEMBER cell, or
eight or more characters of their principal reference. The `recipient:` line says which session was chosen and
why, and which others could have taken it. A label two members share is refused, both named by principal prefix.

## Limits a hostile teammate meets

The backend caps a session at 20 messages a minute and 200 an hour, a person at 60 and 600, 15 unacknowledged
messages between one sender and one recipient and 60 to one recipient in all; a reply chain stops after 32 hops
(`loop_detected`), and a chain quiet for ten minutes starts counting again. `rate_limited` and `loop_detected` are
answers to stop on, not retry.
