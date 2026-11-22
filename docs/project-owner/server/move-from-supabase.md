# Move a team from Supabase

A team in a Supabase project moves to a Brigade server with one command, which its Team Admin runs in a checkout
of the team:

```sh
brigade team move --to <url> --secret-file <path>
```

The server's operator runs nothing, and only hands over the server's URL. The members run nothing: each one's
next session follows, and a session that is already running follows at its next prompts. The team keeps its join
secret, its reference and its name. This page is the order of the move; each step's own page has the details.

## Before the move

| What | Needs | Who |
| --- | --- | --- |
| The server | 0.7.0 or later, at an HTTPS address every member's machine can reach ([Run the server](run-the-server.md)). An older one, from 0.5.0: [With a server older than 0.7.0](#with-a-server-older-than-070) | the server's operator |
| The Supabase project | the migration `20261114120000_team_move`: `make backend-install project=<ref>` ([Update](../update/README.md)) | the Project Owner |
| The Team Admin's machine | plugin 0.56.0 or later, joined to the team, with the file `team create` wrote. The file stays with the Team Admin: the operator needs neither it nor the team's name. | the Team Admin |
| Each member's machine | plugin 0.55.0 or later; an older one does not follow, and joins with the secret file instead | each member |
| The server's URL | the operator gives it to the Team Admin, with the certificate's fingerprint when the server made its own, and the creation token's file when the server asks for one ([Hand over the values](hand-over-the-values.md)) | the server's operator |

Take a copy of the server's data first ([Keep the server](keep-the-server.md)): going back to a server older than
0.5.0 needs it, because one does not start on a data directory in which a move was opened.

## The move

Move at a quiet moment: a message that is waiting on the old backend stays there.

| | Step | Who | Details |
| --- | --- | --- | --- |
| 1 | In a checkout of the team, the command above, with `--certificate sha256:<fingerprint>` when the server made its own certificate and `--create-token-file <path>` when it asks for a token. In a session, `/brigade:move` asks for these and runs it. Answer `y`. | Team Admin | [Move the team to a server](../../team-admin/members/move-the-team.md) |
| 2 | Commit and push `.brigade.json`, in every repository of the team. | Team Admin | the same page |
| 3 | Run nothing: pull, as on any day. The next session in a checkout that has the changed file follows, and says so in one line. A session that is running there follows at its next prompts, on plugin 0.56.0 or later. | each member | [When your team moves](../../team-member/join/follow-a-move.md) |

The one command checks first and changes nothing until its checks pass, but for the date of a record of a move
that the Supabase project already holds: it records that record again as it is, which only the team's creator is
answered, so anyone else is refused before the server is asked anything. Then the server takes the team in with
the Team Admin as its owner, the move is opened, the Supabase project records where the team went, and only then
is `.brigade.json` rewritten. The server is not stopped, no ticket file changes hands, and the operator runs
nothing ([Take in a team that exists](take-in-a-team.md#by-request-with-the-server-running),
[Let a team's members follow it](open-a-move.md#by-request-by-the-teams-owner)).

## With a server older than 0.7.0

A server from 0.5.0 to 0.6.0 takes no team in by request, and the one command says so. Update it to 0.7.0 and the
move is the one command ([Update it](keep-the-server.md#update-it)). On a server that stays older, the operator
does the server's side with the server stopped, and the Team Admin gives them the team's join secret file and
its name, the file the way a join secret is given: never in chat.

| | Step | Who | Details |
| --- | --- | --- | --- |
| 1 | Take the team in: stop the server, `brigade-server --data <dir> --import-team <name> --join-secret-file <file>`, start it. | operator | [Take in a team that exists](take-in-a-team.md#take-the-team-in-yourself) |
| 2 | Open the move, between a stop and a start again: `--open-move <team_ref> --ticket-file <file>`. Give the ticket's file to the Team Admin the way you would give a join secret. | operator | [Let a team's members follow it](open-a-move.md#open-the-move-yourself) |
| 3 | In one checkout, point `.brigade.json` at the server: `adapter` becomes `rest`, `url` the server's, `publishable_key` goes, `certificate` is added when the server made its own, `team_ref` stays. Do not commit yet. Join there: `/brigade:join <the secret file>`. | Team Admin | [With an older server](../../team-admin/members/move-the-team.md#with-an-older-server) |
| 4 | In that checkout: `brigade team move --ticket-file <path>`, and answer `y`. | Team Admin | the same page |
| 5 | Commit and push `.brigade.json`, in every repository of the team. | Team Admin | the same page |
| 6 | Run nothing, as in step 3 of the move above: the next session in a checkout that has the changed file follows, and a session that is running there at its next prompts. | each member | [When your team moves](../../team-member/join/follow-a-move.md) |

Here the Team Admin joins before recording the move: the first member to join a team its operator took in owns
it there, and a member that the ticket lets in never does.

## After the move

- **Check it.** `brigade team status` in a checkout names the server, and `brigade sessions` lists the members'
  sessions as they follow. The server's journal has one line for each member that came in with the ticket.
- **Each checkout follows by itself**, at its first session after it pulled the changed file, or at the next
  prompts of a session that is running in it. The journal does not show a checkout that has not followed yet.
- **A running session that has not moved** holds messages for its member's review and moves once they are
  released, or it was started in a checkout that does not have the changed file yet, or it runs in Grok Build or
  on a plugin older than 0.56.0 and follows when it starts.
- **A member who did not follow** joins with the secret file they already have, as before the move
  ([what a member sees](../../team-admin/members/move-the-team.md#what-a-member-sees-and-when-they-do-not-follow)).
- **The gateways.** Configure them on the server with the team's secret file, and point the Slack app's two
  addresses and the mail provider's webhook at the server ([Open the gateways](gateways.md)). Then take them out
  of the Supabase project
  ([After a team moved to a Brigade server](../remove/remove-a-gateway.md#after-a-team-moved-to-a-brigade-server)).
- **A webhook gateway** does not follow. It is a process on a machine of yours and stays a session of the team in
  the Supabase project, where a request for a member whose sessions have left is still answered 202 and read by
  nobody. Stop it, join its member on the server with the team's secret file, and start it again
  ([When the team moves](../configure/webhook-gateway.md#when-the-team-moves)). It needs a gateway built since
  2026-11-22: an earlier one cannot run for a team on a server.
- **When no checkout is left to follow.** The operator closes the move, `--close-move <team_ref>`, or lets the
  ticket run out after 30 days, or after the days the Team Admin chose with `--ticket-days`. The Team Admin ends the team on the old backend, from a checkout that still
  names it ([Undo it, and end it](../../team-admin/members/move-the-team.md#undo-it-and-end-it)). A checkout
  that has not followed by then connects to nothing, and its member joins there with the secret file.
- **A Supabase project with no team left** needs no keep-alive
  ([Stop the keep-alive](../remove/stop-the-keep-alive.md)) and can be deleted
  ([Delete the project](../delete/delete-the-project.md)).

What the team keeps and what stays behind:
[What comes over, and what does not](take-in-a-team.md#what-comes-over-and-what-does-not). To undo a move that is
recorded: [Undo it, and end it](../../team-admin/members/move-the-team.md#undo-it-and-end-it).
