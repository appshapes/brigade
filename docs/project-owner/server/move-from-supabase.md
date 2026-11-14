# Move a team from Supabase

A team in a Supabase project moves to a Brigade server and keeps its join secret, its reference and its name. Its
members run no command: each one's next session follows. This page is the order of the move; each step's own
page has the details.

## Before the move

| What | Needs | Who |
| --- | --- | --- |
| The server | 0.5.0 or later, at an HTTPS address every member's machine can reach ([Run the server](run-the-server.md)) | the server's operator |
| The Supabase project | the migration `20261114120000_team_move`: `make backend-install project=<ref>` ([Update](../update/README.md)) | the Project Owner |
| Each member's machine | plugin 0.55.0 or later; an older one does not follow, and joins with the secret file instead | each member |
| The team's join secret file, and its name | the file `team create` wrote, and `team_name` in `.brigade.json`. The Team Admin gives both to the operator, the file the way a join secret is given: never in chat. | the Team Admin |
| The server's URL | the operator gives it to the Team Admin, with the certificate's fingerprint when the server made its own ([Hand over the values](hand-over-the-values.md)) | the server's operator |

Take a copy of the server's data first ([Keep the server](keep-the-server.md)): going back to a server older than
0.5.0 needs it, because one does not start on a data directory in which a move was opened.

## The move

Move at a quiet moment: a message that is waiting on the old backend stays there.

| | Step | Who | Details |
| --- | --- | --- | --- |
| 1 | Take the team in: stop the server, `brigade-server --data <dir> --import-team <name> --join-secret-file <file>`, start it. | operator | [Take in a team that exists](take-in-a-team.md) |
| 2 | Open the move, between a stop and a start again: `--open-move <team_ref> --ticket-file <file>`. Give the ticket's file to the Team Admin the way you would give a join secret. | operator | [Let a team's members follow it](open-a-move.md) |
| 3 | In one checkout, point `.brigade.json` at the server: `adapter` becomes `rest`, `url` the server's, `publishable_key` goes, `certificate` is added when the server made its own, `team_ref` stays. Do not commit yet. Join there: `/brigade:join <the secret file>`. | Team Admin | [Move the team to a server](../../team-admin/members/move-the-team.md) |
| 4 | In a terminal, in that checkout: `brigade team move --ticket-file <path>`, and answer `y`. | Team Admin | the same page |
| 5 | Commit and push `.brigade.json`, in every repository of the team. | Team Admin | the same page |
| 6 | Pull, as on any day. The next session in a checkout that has the changed file follows, and says so in one line. | each member | the same page |

The Team Admin joins before recording the move: the first member to join the team on the server owns it there,
and a member that the ticket lets in never does.

## After the move

- **Check it.** `brigade team status` in a checkout names the server, and `brigade sessions` lists the members'
  sessions as they follow. The server's journal has one line for each member that came in with the ticket.
- **Each checkout follows by itself**, at its first session after it pulled the changed file. The journal does
  not show a checkout that has not followed yet.
- **A member who did not follow** joins with the secret file they already have, as before the move
  ([what a member sees](../../team-admin/members/move-the-team.md#what-a-member-sees-and-when-they-do-not-follow)).
- **The gateways.** Configure them on the server with the team's secret file, and point the Slack app's two
  addresses and the mail provider's webhook at the server ([Open the gateways](gateways.md)). Then take them out
  of the Supabase project ([Remove a gateway](../remove/remove-a-gateway.md)).
- **When no checkout is left to follow.** The operator closes the move, `--close-move <team_ref>`, or lets the
  ticket run out after 30 days. The Team Admin ends the team on the old backend, from a checkout that still
  names it ([Undo it, and end it](../../team-admin/members/move-the-team.md#undo-it-and-end-it)). A checkout
  that has not followed by then connects to nothing, and its member joins there with the secret file.
- **A Supabase project with no team left** needs no keep-alive
  ([Stop the keep-alive](../remove/stop-the-keep-alive.md)) and can be deleted
  ([Delete the project](../delete/delete-the-project.md)).

What the team keeps and what stays behind:
[What comes over, and what does not](take-in-a-team.md#what-comes-over-and-what-does-not). To undo a move that is
recorded: [Undo it, and end it](../../team-admin/members/move-the-team.md#undo-it-and-end-it).
