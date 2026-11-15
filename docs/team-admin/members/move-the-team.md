# Move the team to a server

Your team is in a Supabase project and is going to a Brigade server. You run one command in a checkout of the
team, and your members run none: each one's next session follows. It works from plugin 0.56.0, on a server of
0.7.0 or later, and needs the migration `20261114120000_team_move` applied to the Supabase project the team is
leaving ([Know which migration adds what](../../project-owner/update/migrations.md)). The whole move, with what
the server's operator and the Project Owner do first, is in
[Move a team from Supabase](../../project-owner/server/move-from-supabase.md). A server older than 0.7.0 takes no
team in by request: [With an older server](#with-an-older-server).

## What you need

| | |
| --- | --- |
| The server's URL | Its operator gives it, with the certificate's fingerprint when the server made its own certificate, and a file that holds its creation token when it asks for one ([Hand over the values](../../project-owner/server/hand-over-the-values.md)). |
| The team's join secret file | The file `team create` wrote, outside the repository. The command reads it. You never type the secret, and nobody else needs the file. |
| To be the team's creator | Only the member who created the team on the old backend can move it, from a machine that is joined to it. |
| Every member on plugin 0.55.0 or later | An older plugin does not follow. The command names the sessions that are online on one. |

## 1. Run the one command

In a session, ask it to move the team (`/brigade:move` in Claude Code). It asks you for the values above, one at
a time, and runs the command. It never asks for the secret itself: it asks for the path of its file. Or run the
command yourself, in a terminal or with `!` in a session, in the checkout:

```sh
brigade team move --to <url> --secret-file <path>
```

For example `brigade team move --to https://brigade.example.com:8743 --secret-file ~/brigade-<team>.secret`. Add:

| | |
| --- | --- |
| `--certificate sha256:<fingerprint>` | for a server that made its own certificate. The fingerprint goes into `.brigade.json`, and each member's session trusts that certificate and no other. |
| `--create-token-file <path>` | for a server that asks for a creation token: the file its operator gave you, outside the repository. |
| `--ticket-days <n>` | to keep the move open for `n` days, from 1 to 365, in place of 30. |

What it does, in this order:

1. **Checks.** This checkout's `.brigade.json` names the backend the team is on, and this machine is joined
   there. The secret file holds this team's secret. The old backend can keep a record, and you are the team's
   creator. It lists the sessions that are online on a plugin older than 0.55.0. The checks ask the server
   nothing and change nothing, with one exception: when the old backend already holds a record of a move, the
   command records that same record again, with the ticket it holds, because only the team's creator is
   answered there. The record names what it named and admits whom it admitted, and only its date moves. A
   member who did not create the team is refused here, before the server and the join secret come into it.
2. **Shows what it is about to do, and asks**: the team, the host it is leaving, the server's host and the
   certificate's fingerprint. In a terminal you answer `y`. In a session nobody can be asked, so it prints the
   same line and proceeds.
3. **Takes the team in on the server**, with you as its owner, under the label you have on the old backend.
4. **Opens the move.** The ticket goes to a private file under Brigade's state directory and is removed when the
   command ends. You never see it. A command that Ctrl-C or a kill ends between this step and the next cannot
   remove the file: it stays there, mode 0600, until the next run of the same move writes over it and removes
   it.
5. **Records the move** on the old backend, with the destination you typed.
6. **Only then rewrites `.brigade.json`**: `adapter` becomes `rest`, `url` the server's, `certificate` is added
   when you gave one, `publishable_key` goes, and everything else stays as it was. This checkout is joined to the
   team on the server.
7. **Says what was done and what is left.**

Run the same command again after a failure: every step is safe to repeat, and the command says so when it stops
part way. Each run opens the move again, which replaces its ticket on the server, so run a move that is finished
again only for a reason: if that run stops before it has recorded the new ticket, the record hands out the one
that was replaced, and a member who has not followed yet does not follow until you run the command once more.
Move at a quiet moment: messages that are waiting on the old backend stay there, and a running session that
holds any stays with them until its member releases them.

## 2. Push the team file

Commit and push `.brigade.json`, in every repository of the team: the file this checkout now has goes into each.
From then on a member's next session, in a checkout that was joined to the team on the old backend, follows: it
says in one line that it followed the team, where to, and that you recorded the move. A session that was already
running follows at its next prompts once its checkout has the changed file, on a plugin later than 0.55.0: the
first prompt starts the follow, a later one moves the session and says the same line. On 0.55.0 it stays where it
is until it ends.

`brigade team move --show` reads the record back and says whether it names what `.brigade.json` names.

## When the command stops

Each refusal says what to do. Nothing is written before the checks pass, but for the date of a record that
was already there.

| It says | What to do |
| --- | --- |
| the project lacks the migration `20261114120000_team_move` | The Project Owner applies it: `make backend-install project=<ref>` ([Update](../../project-owner/update/README.md)). Then run the command again. |
| only the member who created this team can move it | Run it on the administrator's machine, or [transfer the team](transfer-the-team.md) first. |
| the old backend holds a record of a move already, and a private file for its ticket could not be written | The command names the directory under Brigade's state directory: make it one you can write, and run the command again. |
| the record was cleared while this command was checking it | Someone cleared it from another machine. Run the command again. |
| the file holds the join secret of another team | Name the file `team create` wrote for this team. |
| the server takes no team in by request | It is older than 0.7.0: [With an older server](#with-an-older-server). |
| the server asks for a creation token, or refused the one you gave | Ask its operator for the token's file, and pass `--create-token-file <path>`. |
| the team is held on the server and owned by someone else | Someone took the team in there before you. The server's operator names you its owner ([Name a team's owner](../../project-owner/server/name-the-owner.md)); then run the command again. |
| the server holds a team of this reference under another join secret | Tell the server's operator: this team cannot be taken in there. |
| the server did not take the team in | It could not be reached, or its answer did not arrive. Run the same command again, on the same machine. |
| `Run the same command again to finish the move` | It stopped after the server took the team in. Remove the cause and run it again. |

## What a member sees, and when they do not follow

```text
Brigade: this checkout followed team "ops" to brigade.example.com:8743, trusting the server certificate sha256:…: the team's administrator recorded that move on abc.supabase.co, the backend you joined it on, and this machine joined it there with the move's ticket, under the label you had. …
```

A member's session does not follow, says that `.brigade.json` does not match, and joins with the secret file as
before when:

- they set the option `follow_team_move` to `off`, or their plugin is older than 0.55.0;
- the session runs in Grok Build and was running when the file changed: it follows when it starts;
- the record names anything other than what the file they pulled names: check with `brigade team move --show`;
- the ticket has ended, the move was closed, or you rotated the join secret on the server;
- their session on the old backend has ended (they left the team there, or were revoked).

## From a session, and how to turn that off

`brigade team move` runs inside a session, so the session you work in can make the move: you give it the server's
URL and the paths, and it runs the command and prints what the command printed. The skill never asks for a
secret's content, never chooses a path and never writes a secret file.

To keep it out of your sessions, set the option `team_move_in_session` to `off`
([Know the options](../../team-member/configure/options.md)). The command then refuses inside a session when it
would move a team, record a move or clear a record, and you run it in your own terminal. `--show` reads the
record anywhere.

To be asked each time instead, in Claude Code, add an ask rule to your settings:
`"ask": ["Bash(brigade team move*)"]`. Without it the command runs with no dialog wherever `brigade` commands are
allowed already: under a `Bash(brigade:*)` rule of your own, and in a turn that loaded the `team-messaging`
skill. What a session-led move opens is in
[Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend).

## With an older server

A server older than 0.7.0 takes no team in by request, and the one command says so. Its operator does the
server's side, and you do yours in three steps.

1. The operator takes the team in and opens its move, with the server stopped
   ([Take in a team that exists](../../project-owner/server/take-in-a-team.md),
   [Let a team's members follow it](../../project-owner/server/open-a-move.md)), and gives you the server's URL,
   its certificate fingerprint if it has one, and the ticket's file. They need the team's join secret file and
   its name from you, the file the way a join secret is given: never in chat.
2. In one checkout, change `.brigade.json` as
   [Take in a team that exists](../../project-owner/server/take-in-a-team.md#point-the-team-at-the-server) shows,
   and do not commit yet. Join, in a session opened there: `/brigade:join ~/brigade-<team>.secret`. You are the
   first to join the team on the server, so you own it there. Join before you record the move: a member that the
   ticket lets in never becomes the owner, and the record is refused until this machine has joined.
3. Record the move, in a terminal or a session, in the same checkout:

   ```sh
   brigade team move --ticket-file ~/<team>.ticket
   ```

   It shows what it is about to record and asks. Where the team went is read from `.brigade.json`, so the record
   names exactly what each member's session compares it with. The ticket is read from the file: it is never an
   argument, and nothing printed repeats it. Keep the file outside the repository, and delete it once the move
   is recorded. Then [push the team file](#2-push-the-team-file).

## Undo it, and end it

- `brigade team move --clear` removes the record: the old backend no longer says where the team went, so no
  session follows from then on. It does not end the ticket. The move ends when the ticket runs out, when you
  rotate the join secret on the server, or when the server's operator closes it.
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
