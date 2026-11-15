# Let a team's members follow it

A team that you took in keeps its join secret, but no member's machine keeps that secret, so until server 0.5.0
each member joined the server once with the secret file ([Take in a team that exists](take-in-a-team.md)). With a
move opened, they do nothing: the team's administrator records where the team went on the backend it is leaving,
and each member's next session follows on that record. It needs server 0.5.0 or later, plugin 0.55.0 or later on
every member's machine, and, for a team in a Supabase project, the migration `20261114120000_team_move` applied to
that project.

There are two ways to open a move. You run a command on the server's data directory, with the server stopped:
the sections below. Or, from server 0.7.0, the team's owner asks the running server to do it from their own
machine, and you run nothing: [By request, by the team's owner](#by-request-by-the-teams-owner).

## What you need

| | |
| --- | --- |
| The team taken in | `--import-team` or a request, as in [Take in a team that exists](take-in-a-team.md). The team's reference is what that command printed. |
| A moment with the server stopped | For your own command: one process at a time owns the server's log. A move opened by request needs none. |
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
  before, and so owns the team, or owns it already for having taken it in by request.
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

## By request, by the team's owner

From server 0.7.0 a running server opens and closes a team's move when the team's owner asks it to, by the
requests `/v1/team/open-move` and `/v1/team/close-move` of [the backend's contract](../../backend-rest-v1.md)
(section 3b). The owner's machine makes them through the bundled `rest` adapter (`team open-move --ticket-file
<path>` and `team close-move`). The server is not stopped, and you run nothing. The Team Admin's
`brigade team move --to <url> --secret-file <path>` opens the move this way, after it took the team in
([Move the team to a server](../../team-admin/members/move-the-team.md)).

- **Only the team's owner is answered**, by the credential its machine holds. A member that is not the owner, and
  a member the ticket let in, is refused as a stranger is.
- **No ticket file changes hands.** The server shows the ticket once, in its answer, and the owner's adapter
  writes it to a file of the owner's own, readable by its owner alone. It is in no argument and no output.
- It admits for 30 days unless the owner asks for another number from 1 to 365, as with `--ticket-days`.
- The requests and your commands are one move: what one opens, the other closes, and a second open replaces the
  ticket whoever opened the first.

## What the journal shows

A member that came in with the ticket leaves the line `member <reference> joined team <name> (<team_ref>) with
its move ticket, labelled "<label>"`, so you can tell who followed from who joined with the secret. A move the
team's owner opened or closed by request leaves `member <reference> opened the move of team <name> (<team_ref>):
its ticket admits until <time>` and `member <reference> closed the move of team <name> (<team_ref>): its ticket
admits nobody from now on`.

## An older server

Server 0.4.x does not start on a data directory in which a move was opened. Before going back to one, restore the
copy you took before the update ([Keep the server](keep-the-server.md)). Server 0.5.0 and 0.6.0 start on what
0.7.0 wrote, a team taken in by request and a move opened by one included; they answer none of the three
requests, and they keep no retry key: a take-in whose answer was lost, run again after one of them wrote a
snapshot of the directory, joins as a member.
