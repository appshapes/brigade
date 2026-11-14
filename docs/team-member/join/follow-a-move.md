# When your team moves

A team can move from one backend to another: from a Supabase project to a Brigade server your organisation runs.
You do nothing. The next session you start in the project follows the team, a session that is already running
follows at its next prompts, and either tells you so in one line:

```text
Brigade: this checkout followed team "ops" to brigade.example.com:8743, trusting the server certificate sha256:…: the team's administrator recorded that move on abc.supabase.co, the backend you joined it on, and this machine joined it there with the move's ticket, under the label you had. To leave the team run `brigade team leave`; to stay put until you join a moved team yourself, set the option follow_team_move to off.
```

## What made it follow

Not the project's `.brigade.json` alone: anyone who can commit can change that file. The session asked the backend
you had joined whether the team's administrator recorded a move there, and followed only because the record named
exactly what the file names: the same server, the same certificate, the same team. Then your machine joined the
server under the label you had. Nothing you hold for the old backend was sent to the server.

## A session that was already running

It follows at its next prompts, from the plugin release after 0.55.0; on 0.55.0 it stays where it is until it
ends. Nothing is asked of you.

1. The first prompt after the team file changed says once that the file names another backend, and starts the
   follow beside the session: the same question to the backend you joined, and the same join. Your prompt is not
   held up.
2. A later prompt, once the checkout has followed, says the line above and moves the session: it is registered
   on the server under the name it had, and the line after it gives the session's new id. A reply sent to the
   id it had does not reach it.

- **A quiet session stays quiet.** `/brigade:quiet` holds on the server as it did before.
- **Held messages keep it back.** A message that is held for your review is on the backend the team left and
  does not move. The session says so once and stays until you release it (`brigade inbox`, then `brigade inbox
  release`, in your own terminal), and moves at its next prompt after that.
- **If the server cannot be reached**, the session stays where it is, says so once, and tries again at a later
  prompt.
- **With no record behind the file**, the session stays where it is. The backend you joined is asked again at
  most once a minute while you prompt, and the address the file names is sent nothing.
- **A Grok Build session** follows when it starts, not while it runs: Grok Build shows nothing a prompt hook
  prints, so the session could not tell you.

## If you would rather decide yourself

Set the option `follow_team_move` to `off` ([Know the options](../configure/options.md)): ask your session `Set
follow_team_move to off.` From then on a session never follows. When a team of yours moves, the session says that
`.brigade.json` does not match the team you joined, and you join the server yourself with the secret file:
`/brigade:join ~/brigade-<team>.secret`. A session that is running says once that it stays where it is, and
moves at its next prompt after your join.

## If the session says the file does not match

Then nothing vouched for the change: the administrator has not recorded a move, or recorded another server than
the file names, or the move has ended. Ask your administrator before you join anything. A team file that changed
with no record behind it is exactly what this check is for.

## To leave

`brigade team leave` in the checkout leaves the team on the server. It holds: a session in another checkout of
the team does not join you again. To come back, join yourself.

What this opens, and to whom, is in [Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend).
