# Let a team's members follow it

A team that you took in keeps its join secret, but no member's machine keeps that secret, so until server 0.5.0
each member joined the server once with the secret file ([Take in a team that exists](take-in-a-team.md)). With a
move opened, they do nothing: the team's administrator records where the team went on the backend it is leaving,
and each member's next session follows on that record. It needs server 0.5.0 or later, plugin 0.55.0 or later on
every member's machine, and, for a team in a Supabase project, the migration `20261114120000_team_move` applied to
that project.

## What you need

| | |
| --- | --- |
| The team taken in | `--import-team`, as in [Take in a team that exists](take-in-a-team.md). The team's reference is what that command printed. |
| A moment with the server stopped | One process at a time owns the server's log. |
| A place for one file | The ticket goes to a file you name: an absolute path of a file that is not there, in a directory that is. |

## Open the move

```sh
sudo systemctl stop brigade-server
sudo -u brigade brigade-server --data /var/lib/brigade-server --open-move <team_ref> --ticket-file /var/lib/brigade-server/<team>.ticket
sudo systemctl start brigade-server
```

- It says `opened the move of team "<name>" (<team_ref>): its ticket is in "<file>" and admits until <time>` and
  ends. The file is readable by its owner alone.
- **The ticket is a second join secret of that team, with an end.** It admits for 30 days; `--ticket-days <n>`
  chooses from 1 to 365. The server keeps its hash and its end, never the ticket.
- **A member it lets in is a member and never the team's owner.** The Team Admin joins with the secret file, as
  before, and so owns the team.
- The ticket is never an argument and nothing the command prints repeats it.

## Hand the ticket to the Team Admin

Give the file to the team's administrator the way you would give a join secret: over a password-grade channel,
never in chat. They record it with `brigade team move --ticket-file <path>`
([Move the team to a server](../../team-admin/members/move-the-team.md)), and from then on the backend the team
is leaving hands the ticket to each active member's session that asks for it. Delete your copy once it is
recorded.

Whoever can read that backend's database can read the ticket too, and join the team here with it until it ends.
That is the price of members doing nothing ([Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend)).

## Close the move

```sh
sudo systemctl stop brigade-server
sudo -u brigade brigade-server --data /var/lib/brigade-server --close-move <team_ref>
sudo systemctl start brigade-server
```

- It says `closed the move of team "<name>" (<team_ref>): its ticket admits nobody from now on`. Every member that
  came in with the ticket stays a member.
- A move also ends by itself when the ticket runs out, and when the team's owner rotates the join secret: a
  rotation ends everything that let anyone in before it.
- Opening a move for a team that has one replaces the ticket: the earlier one admits nobody from then on, and the
  command says so. That is how a ticket whose file was lost is replaced, and the Team Admin then records the new
  one.

## What the journal shows

A member that came in with the ticket leaves the line `member <reference> joined team <name> (<team_ref>) with
its move ticket, labelled "<label>"`, so you can tell who followed from who joined with the secret.

## An older server

Server 0.4.x does not start on a data directory in which a move was opened. Before going back to one, restore the
copy you took before the update ([Keep the server](keep-the-server.md)).
