# When your team moves

A team can move from one backend to another: from a Supabase project to a Brigade server your organisation runs.
You do nothing. The next session you start in the project follows the team and tells you so in one line:

```text
Brigade: this checkout followed team "ops" to brigade.example.com:8743, trusting the server certificate sha256:…: the team's administrator recorded that move on abc.supabase.co, the backend you joined it on, and this machine joined it there with the move's ticket, under the label you had. To leave the team run `brigade team leave`; to stay put until you join a moved team yourself, set the option follow_team_move to off.
```

## What made it follow

Not the project's `.brigade.json` alone: anyone who can commit can change that file. The session asked the backend
you had joined whether the team's administrator recorded a move there, and followed only because the record named
exactly what the file names: the same server, the same certificate, the same team. Then your machine joined the
server under the label you had. Nothing you hold for the old backend was sent to the server.

## A session that was already running

It stays where it is until it ends, and says once that the team file now names another backend. Start a new
session, or exit and resume this one.

## If you would rather decide yourself

Set the option `follow_team_move` to `off` ([Know the options](../configure/options.md)): ask your session `Set
follow_team_move to off.` From then on a session never follows. When a team of yours moves, the session says that
`.brigade.json` does not match the team you joined, and you join the server yourself with the secret file:
`/brigade:join ~/brigade-<team>.secret`.

## If the session says the file does not match

Then nothing vouched for the change: the administrator has not recorded a move, or recorded another server than
the file names, or the move has ended. Ask your administrator before you join anything. A team file that changed
with no record behind it is exactly what this check is for.

## To leave

`brigade team leave` in the checkout leaves the team on the server. It holds: a session in another checkout of
the team does not join you again. To come back, join yourself.

What this opens, and to whom, is in [Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend).
