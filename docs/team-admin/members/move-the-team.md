# Move the team to a server

Your team is in a Supabase project and is going to a Brigade server. You do three things, in this order, and your
members do nothing: their next session follows. It works from plugin 0.55.0, on a server of 0.5.0 or later, and
needs the migration `20261114120000_team_move` applied to the Supabase project the team is leaving
([Know which migration adds what](../../project-owner/update/migrations.md)). The whole move, with the
operator's steps, is in [Move a team from Supabase](../../project-owner/server/move-from-supabase.md).

## What you need

| | |
| --- | --- |
| The team taken in on the server, and its move opened | The server's operator does both, and gives you the server's URL, its certificate fingerprint if it has one, and the ticket's file ([Let a team's members follow it](../../project-owner/server/open-a-move.md)). |
| The team's join secret file | The file `team create` wrote. You join the server with it. |
| Your own terminal | `team move` refuses inside a session when it records or clears. |
| Every member on plugin 0.55.0 or later | An older plugin does not follow: that member's next session says the file does not match, and they join with the secret file as before. |

## 1. Point the team file at the server, and join

In one checkout, change `.brigade.json` as [Take in a team that exists](../../project-owner/server/take-in-a-team.md#point-the-team-at-the-server)
shows: `adapter` becomes `rest`, `url` the server's, `publishable_key` goes, `certificate` is added when the
server made its own, and `team_ref` stays. Do not commit yet. Then join, in a session opened there:

```text
/brigade:join ~/brigade-<team>.secret
```

You are the first to join the team on the server, so you own it there: you may rotate its secret, revoke a member
and transfer it. Join before you record the move. A member that the ticket lets in never becomes the owner, and
`team move` refuses to record until this machine has joined.

## 2. Record the move

In your terminal, in the same checkout:

```sh
brigade team move --ticket-file ~/<team>.ticket
```

- It shows what it is about to record and asks: the team, the host it is leaving, the server's host, the adapter
  and the certificate fingerprint. Answer `y`.
- **Where the team went is read from `.brigade.json`**, so the record names exactly what each member's session
  compares it with. Nothing is typed.
- **It is recorded on the backend the team is leaving**, as you: only the team's creator can.
- The ticket is read from the file. It is never an argument, and nothing printed repeats it. Keep the file
  outside the repository; delete it once the move is recorded.
- `brigade team move --show` reads the record back and says whether it names what `.brigade.json` names.

## 3. Push the team file

Commit and push `.brigade.json`, in every repository of the team. From then on a member's next session, in a
checkout that was joined to the team on the old backend, follows: it says in one line that it followed the team,
where to, and that you recorded the move. A session that was already running stays where it is until it ends, and
is told once that a new session follows. Move at a quiet moment: messages that are waiting on the old backend
stay there.

## What a member sees, and when they do not follow

```text
Brigade: this checkout followed team "ops" to brigade.example.com:8743, trusting the server certificate sha256:…: the team's administrator recorded that move on abc.supabase.co, the backend you joined it on, and this machine joined it there with the move's ticket, under the label you had. …
```

A member's session does not follow, says that `.brigade.json` does not match, and joins with the secret file as
before when:

- they set the option `follow_team_move` to `off`, or their plugin is older than 0.55.0;
- the record names anything other than what the file they pulled names: check with `brigade team move --show`;
- the ticket has ended, the operator closed the move, or you rotated the join secret on the server;
- their session on the old backend has ended (they left the team there, or were revoked).

## Undo it, and end it

- `brigade team move --clear` removes the record: the old backend no longer says where the team went, so no
  session follows from then on. It does not end the ticket. The server's operator closes the move, or it runs
  out.
- Clear the record **before** you point `.brigade.json` back at the old backend. If the file already names it,
  say which backend holds the record: `brigade team move --clear --from <the old backend's host>`, and
  `--show --from <host>` reads it.
- When no checkout is left to follow, end the team on the old backend ([End a team](../delete/end-a-team.md)),
  with three differences. Run it in a checkout whose `.brigade.json` still names the old backend, one you
  kept at the commit before the move: a team command goes to the backend the file names, and in a checkout
  that followed that is the server. Give `rotate-secret` a new file, not the team's secret file, which still
  joins the server. And leave `.brigade.json` in the repositories. The record goes with the team.

## What you are deciding for your members

Before this, nobody could point a member's sessions at another backend without that member's own command. With a
recorded move you can, and so can whoever controls the old backend. The server you name is read by its operator.
Each member is told in one line and can turn it off. [Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend)
says all of it.
