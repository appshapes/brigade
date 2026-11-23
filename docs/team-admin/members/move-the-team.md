# Move the team to a server

Your team is in a Supabase project, or on a Brigade server, and is going to a Brigade server. You run one command
in a checkout of the team. The server's operator runs none and only gives you the server's URL, and your members
run none: each one's next session follows. The server the team goes to is 0.7.0 or later; one older than that
takes no team in by request: [With an older server](#with-an-older-server). What else it needs depends on where
the team is:

| The team is | It needs | The whole move |
| --- | --- | --- |
| in a Supabase project | Plugin 0.56.0 or later, and the migration `20261114120000_team_move` applied to the project ([Know which migration adds what](../../project-owner/update/migrations.md)). | [Move a team from Supabase](../../project-owner/server/move-from-supabase.md) |
| on a Brigade server | Plugin 0.57.0 or later, and that server at 0.8.0 or later: an older one keeps no record of where a team goes. | [Move a team to another server](../../project-owner/server/move-to-another-server.md) |

## What you need

| | |
| --- | --- |
| The server's URL | Its operator gives it, with the certificate's fingerprint when the server made its own certificate, and a file that holds its creation token when it asks for one ([Hand over the values](../../project-owner/server/hand-over-the-values.md)). |
| The team's join secret file | The file `team create` wrote, outside the repository. The command reads it. You never type the secret, and nobody else needs the file. |
| To administer the team where it is | In a Supabase project, only the member who created the team can move it. On a server, only its owner: the member who created it or took it in there, or whoever it was transferred to. Either way from a machine that is joined to it. |
| Every member on a plugin that follows | 0.55.0 or later when the team leaves a Supabase project, 0.57.0 or later when it leaves a server. An older plugin does not follow. The command names the sessions that are online on one. |
| The server's host, for members with sandboxed sessions | A session that runs with the Bash sandbox on reaches only the hosts in its `sandbox.network.allowedDomains`. Tell those members the server's host before the move: with the old host alone, their sends are refused after it ([Run headless or sandboxed sessions](../../team-member/configure/headless-and-sandbox.md#the-bash-sandbox)). |

## 1. Run the one command

In a session, ask it to move the team (`/brigade:move` in Claude Code and Grok Build, `$brigade:move` in Codex
CLI, the `move` skill in Muse Code). It asks you for the values above, one at a time, and runs the command. It
never asks for the secret itself: it asks for the path of its file. Or run the command yourself, in a terminal
or with `!` in a session, in the checkout:

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
   creator, or its owner on a server. It lists the sessions that are online on a plugin too old to follow
   (older than 0.55.0, or than 0.57.0 when the team leaves a server), and says so when the old
   backend cut its roster short and the list may lack some. The checks ask the server nothing and change
   nothing, with one exception: when the old backend already holds a record of a move, the command records
   that same record again, with the ticket it holds, because only the team's creator is answered there. The
   record names what it named and admits whom it admitted, and only its date moves. A member who did not
   create the team is refused here, before the server and the join secret come into it.
2. **Shows what it is about to do, and asks**: the team, the host it is leaving, the server's host, with the
   path of its address when it has one, and the certificate's fingerprint. In a terminal you answer `y`. In a
   session nobody can be asked, so it prints the same line and proceeds.
3. **Takes the team in on the server**, with you as its owner, under the label you have on the old backend.
   A team that was on that server before, and comes back, is there already: the server then still records
   that the team left it, and the command clears that record here, before anything is recorded on the backend
   the team leaves. So the two never each say that the team is on the other. When this machine has joined the
   team on that server before, the question says that this will be done. A step that fails after that does not
   put the record back, and the command says so: a member who had not yet followed the team's earlier move
   away from that server follows nothing until this move is finished.
4. **Opens the move.** The ticket goes to a private file under Brigade's state directory and is removed when the
   command ends. You never see it. A command that Ctrl-C or a kill ends between this step and the next cannot
   remove the file: it stays there, mode 0600, until the next run of the same move writes over it and removes
   it.
5. **Records the move** on the old backend, with the destination you typed.
6. **Only then rewrites `.brigade.json`**: `adapter` becomes `rest`, `url` the server's, `certificate` is added
   when you gave one, `publishable_key` goes, and everything else stays as it was. A file that named a server
   keeps its `adapter` and gets the new server's `url` and `certificate`, or loses the `certificate` when you
   gave none. This checkout is joined to the team on the server.
7. **Says what was done and what is left.**

Run the same command again after a failure: every step is safe to repeat, and the command says so when it stops
part way. On a machine that has joined the team on more than one earlier backend, which is every machine whose
team moved before, the command run again has to know which of them the team is leaving. The checkout says: the
backend it is still joined to, where a run stopped after it wrote `.brigade.json`, or the backend a session
there followed from. A record alone does not choose: a backend the team left long ago for this same server
still says so. In the checkout the command itself joined to the server nothing says, so it changes nothing,
names them and the flag, prints itself once for each with its `--from`, and you run the one that names the
backend the team is on. Each run opens the move again, which replaces its ticket on the server, so run a move that is finished
again only for a reason: if that run stops before it has recorded the new ticket, the record hands out the one
that was replaced, and a member who has not followed yet does not follow until you run the command once more.
Move at a quiet moment: messages that are waiting on the old backend stay there, and a running session that
holds any stays with them until its member releases them.

## 2. Push the team file

Commit and push `.brigade.json`, in every repository of the team: the file this checkout now has goes into each.
From then on a member's next session, in a checkout that was joined to the team on the old backend, follows: it
says in one line that it followed the team, where to, and that you recorded the move. A session that was already
running follows at its next prompts once its checkout has the changed file, on plugin 0.56.0 or later: the
first prompt starts the follow, a later one moves the session and says the same line. On 0.55.0 and 0.55.1 it stays
where it is until it ends.

`brigade team move --show` reads the record back and says whether it names what `.brigade.json` names.

## When the command stops

Each refusal says what to do. Nothing is written before the checks pass, but for the date of a record that
was already there.

| It says | What to do |
| --- | --- |
| the project lacks the migration `20261114120000_team_move` | The Project Owner applies it: `make backend-install project=<ref>` ([Update](../../project-owner/update/README.md)). Then run the command again. |
| only the member who created this team can move it | Run it on the administrator's machine, or [transfer the team](transfer-the-team.md) first. |
| not an active member of this team | The backend's one answer to everyone but the administrator, a member included, when a record or its clear is refused and no more can be read. Brigade says so after it ([Know where the authority lives](where-the-authority-lives.md#when-a-member-who-is-not-the-administrator-runs-one-of-its-commands)). Run it on the administrator's machine. |
| only the owner of this team can move it | The team is on a server, and this machine's member does not own it there. Run it on the owner's machine, or have the owner [transfer the team](transfer-the-team.md) to you first. |
| the server this team is on keeps no record of where a team goes | It is older than 0.8.0. Its operator updates it ([Keep the server](../../project-owner/server/keep-the-server.md#update-it)); then run the command again. Until then each member joins the new server with the secret file. |
| the team is taken in on the server, and it still records that the team left it | The team was on that server before, and its record of the leaving could not be cleared or read. Nothing was recorded on the backend the team is leaving, so no session follows yet. Remove the cause the refusal names and run the same command again. |
| the answer to the clear of that record did not arrive, so the record may be cleared all the same | The same stop, when the server did not answer the clear: it may have cleared the record, and `brigade team move --show --from <that server's host>` says. Run the same command again either way. |
| is the server this team is on | You named the server `.brigade.json` already names, with another `--certificate` than the file has, or with none. A team is moved to another server, not onto its own. A server's new certificate is a change of `certificate` in `.brigade.json` and a `brigade team join` in each checkout, not a move; a move to that server that you are running again takes the `--certificate` the file has, which the refusal prints. |
| this machine has joined this team on more than one other backend | You ran a move again whose `.brigade.json` already names the server, and which backend the team is leaving is not settled: nothing in this checkout says it; or the backend this checkout was joined to is not the one whose record names the server; or it is one of two that both record a move there, which is what a checkout left behind at an earlier move shows too. A record that names the server does not say it by itself, and neither does the checkout beside a second such record. The refusal names them and prints the command for each: run the one whose `--from` is the backend the team is on. Where it says that one of them did not answer what it records, that backend may be the one the team is leaving, so nothing was chosen: run the command again when it answers, or with `--from <host>`. Where it says that one of them refuses to say what it records to this machine, that backend no longer counts your machine a member of the team: it may be the one the team is leaving all the same, so nothing was chosen, and waiting mends nothing. Run the command with `--from <host>`. The line the refusal prints is the whole command, with `--certificate sha256:…` for a server that made its own certificate. |
| the same server under another address, or a copy of its data | The server you named holds the team with a membership your machine has: on the server the team is on, or on one it was on before. A team is not moved to the server it is on, nor to a copy; to move it back to a server it was on before, name that server by the address your machine knows, which the refusal prints with the certificate's fingerprint where that server made its own. The command ended the membership it had made there; when it could not, it names the one command that does. To change a server's address, its operator starts a fresh server at the new one ([Move it](../../project-owner/server/keep-the-server.md#move-it)). |
| the old backend holds a record of a move already, and a private file for its ticket could not be written | The command names the directory under Brigade's state directory: make it one you can write, and run the command again. |
| the record was cleared while this command was checking it | Someone cleared it from another machine. Run the command again. |
| the file holds the join secret of another team | Name the file `team create` wrote for this team. |
| the server takes no team in by request | It is older than 0.7.0: [With an older server](#with-an-older-server). |
| the server asks for a creation token, or refused the one you gave | Ask its operator for the token's file, and pass `--create-token-file <path>`. |
| the team is held on the server and owned by someone else | Someone took the team in there before you, or this machine did in a run whose answer was lost and has joined the team as a member since. The server's operator names you its owner ([Name a team's owner](../../project-owner/server/name-the-owner.md)); then run the command again. The command says this only of another server: of the server the team is on, or a copy of it, it says the row above and names no owner. |
| the server holds a team of this reference under another join secret | Tell the server's operator: this team cannot be taken in there. If the team was on that server before and its join secret was rotated since, on either side, name the file of the secret it has on that server. If the team was never there, someone took its reference in under a secret of their own, which a server that asks for no creation token allows anyone: from server 0.9.0 its operator removes that team, and you run the command again ([Remove a team](../../project-owner/server/name-the-owner.md#remove-a-team)). |
| the server did not take the team in | It could not be reached, or its answer did not arrive. Run the same command again, on the same machine. |
| `Run the same command again to finish the move` | It stopped after the server took the team in. Remove the cause and run it again. |

## What a member sees, and when they do not follow

```text
Brigade: this checkout followed team "ops" to brigade.example.com:8743, trusting the server certificate sha256:…: the team's administrator recorded that move on abc.supabase.co, the backend you joined it on, and this machine joined it there with the move's ticket, under the label you had. …
```

A member's session does not follow, says that `.brigade.json` does not match, and joins with the secret file as
before when:

- they set the option `follow_team_move` to `off`, or their plugin is older than 0.55.0, or than 0.57.0 when
  the team left a server;
- they missed an earlier move: [A member who missed a move](#a-member-who-missed-a-move);
- the session runs in Grok Build and was running when the file changed: it follows when it starts;
- the record names anything other than what the file they pulled names: check with `brigade team move --show`;
- the ticket has ended, the move was closed, or you rotated the join secret on the server;
- their session on the old backend has ended (they left the team there, or were revoked);
- the old backend does not answer when their session asks it whether the team moved: the session says the same
  line of a backend that is only out of reach, and the next session asks again, as that one does at its prompts;
- the two backends each record that the team moved to the other. The session says exactly that, once, and
  follows neither. No command of Brigade's leaves two backends so; `brigade team move --show --from <host>`
  reads each record, and `--clear --from <host>` clears the one that is out of date.

A member whose machine was on the server before, and whom you revoked there when the team had left it, follows
all the same when the team comes back: the server refuses the membership the machine held, and the session joins
with the move's ticket under the credential it holds, so the member comes back as who they were. A member the
server refuses that join too stays out: one you banned there, or one whose ticket no longer admits. A session of
theirs says once that the server refuses the membership their machine holds, with the command that joins,
`brigade team join --secret-file <path>` and the file of the team's current join secret. No session of theirs
presents the ticket again for fifteen minutes, because a server counts the joins a principal fails and their own
join has to be heard; the first session to start after that asks once more, so a member whose ban you lift comes
back with no command of theirs.

**While a move is open, a revocation alone does not keep a member out.** The move's ticket admits the team's
members until it ends, and a member you revoke on the server the team moved to still reads that ticket on the
old backend: their next session in a checkout that has not followed joins with it, as the member they were.
That is what a revocation is, a member who may come back. A ban,
`brigade team revoke-member --principal <principal_ref> --ban` ([Revoke or ban a member](revoke-a-member.md)),
holds for that installation's membership, its `principal_ref`: the ticket does not let that principal back in.
A person who resets Brigade (`brigade team reset`) or uses another machine is a new principal, whom an open
move's ticket or the current join secret admits. So a person is kept out by a ban together with a rotation of
the join secret on the server, which closes the move: the ticket then admits nobody, and they are not given the
new secret.

## A member who missed a move

A team that moves twice is followed one move at a time, each on the record of the backend the member's machine
is on. Say the team went from A to B and then to C, and a member's machine was still on A when the team file came
to name C. That member's session follows nothing and says that `.brigade.json` does not match the team: A records
a move to B, and the file names C. Two ways on:

- join C with the secret file, as before a move could be followed: `/brigade:join ~/brigade-<team>.secret`;
- or follow each move in turn: start one session in a checkout whose `.brigade.json` is the one of the first
  move, which follows to B while B's move is open, then pull, and the next session follows from B to C.

You can keep it from happening: leave time between two moves for every member to start a session. `brigade team
members`, in the checkout after the first move, lists who has arrived on the new server.

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
   ([Take in a team that exists](../../project-owner/server/take-in-a-team.md#take-the-team-in-yourself),
   [Let a team's members follow it](../../project-owner/server/open-a-move.md#open-the-move-yourself)), and
   gives you the server's URL, its certificate fingerprint if it has one, and the ticket's file. They need the
   team's join secret file and its name from you, the file the way a join secret is given: never in chat.
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
   names exactly what each member's session compares it with. When the server the team goes to itself records
   that the team left it, a team that comes back, the record's own form clears that record first, as the one
   command does, and says so. When it cannot, and that record names the backend the team is leaving, it
   records nothing and names the command that clears it: the two would otherwise each say that the team is on
   the other. When the clear got no answer it says that instead, because the record may be cleared all the
   same, and that `brigade team move --show --from <host>` reads it. The ticket is read from the file: it is never an
   argument, and nothing printed repeats it. Keep the file outside the repository, and delete it once the move
   is recorded. Then [push the team file](#2-push-the-team-file).

## Undo it, and end it

- `brigade team move --clear` removes the record: the old backend no longer says where the team went, so no
  session follows from then on. That is all it undoes, and it is safe to run at any time, before or after your
  members followed, and more than once. On a server the record is its owner's alone to clear: to another
  member of the team there the command says `only the owner of this team on <host> can clear the record of
  where it went`, and the record stands. What it does not undo:
  - **the ticket.** It still admits at the new server. The move ends when the ticket runs out, when you rotate
    the join secret on the server, or when the server's operator closes it;
  - **anyone who followed.** Their machines are members of the team on the new server, their checkouts are
    pinned to it, and their sessions stay there;
  - **the team on the new server**, which you own, and **`.brigade.json`**, which names it. To go back, point
    the file at the old backend again, and each member who followed joins there again with `brigade team join`.

  A record that was cleared is made again by the same `brigade team move --to` command, which opens the move
  again. Where the machine has joined the team on more than one earlier backend, that run asks which one the
  team is leaving, and names them: add `--from <host>`.
- Clear the record **before** you point `.brigade.json` back at the old backend. If the file already names it,
  say which backend holds the record: `brigade team move --clear --from <the old backend's host>`, and
  `--show --from <host>` reads it. A team that moved before leaves your machine joined to more than one earlier
  backend, and `--clear` and `--show` then ask which is meant in the same way: the last line of the move names
  the command with its `--from`. Where one host names two backends of the team, two servers on one machine or
  behind one front under two paths, the host names both: every command a move prints then carries the
  backend's whole address after `--from`, so run it as it is printed.
- When no checkout is left to follow, end the team on the old backend ([End a team](../delete/end-a-team.md)),
  with four differences. A team command goes to the backend the checkout is **joined** to, which after the
  move is the server, whatever the `.brigade.json` of an older commit names: `brigade team status` says which.
  So name the old backend, with `--on <its url or host>`, on the two commands that end the team there
  (its steps 1 and 2), and read what they print: each says the backend it acts on before it acts, and in its
  result. Give `rotate-secret` a new file, not the team's secret file, which still joins the server. Leave its
  step 3 out: `team leave` and `team reset` take no `--on`, and in a checkout that followed they would leave
  the server the team lives on. And leave `.brigade.json` in the repositories. The record goes with the team.
- **When the old backend is a server**, the move ends nothing there: the team stays as it was, and a member
  who has not followed still reaches it. No command of yours removes a team from a server; from server 0.9.0
  its operator can, once every member has followed
  ([Remove a team](../../project-owner/server/name-the-owner.md#remove-a-team)). What you can do there, once
  every member has followed, from any checkout of the team:

  ```sh
  brigade team rotate-secret --on <the old server's url or host> --secret-file <a new file>
  brigade team revoke-member --on <the old server's url or host> --max-version <n>
  ```

  After the first nobody new joins it, and the second ends the memberships that joined with the earlier
  secret. Each prints `team … acts on team "<name>" (<team_ref>) at <host>` first: without `--on` that host is
  the server the team lives on, and the two commands would close the move and put out the members who
  followed. Where one host names two servers the team is joined on, under two paths, the line carries the
  whole address in the host's place. Neither clears the record. A member you revoke there reads no record from then on, so revoke only
  those who have followed, in every checkout they have. What the team wrote stays on that server. A removal
  by its operator, from server 0.9.0, frees the team's reference there and erases nothing: what the team wrote
  stays in that server's log and in the snapshots it still keeps until it lets them go, and in every copy of
  its data directory ([Remove a team](../../project-owner/server/name-the-owner.md#remove-a-team);
  [Security, section 20](../../security.md#20-a-team-on-a-brigade-server)).

## What you are deciding for your members

Before this, nobody could point a member's sessions at another backend without that member's own command. With a
recorded move you can, and so can whoever controls the old backend. The server you name is read by its operator.
Each member is told in one line and can turn it off. [Security, section 21](../../security.md#21-a-team-that-moves-to-another-backend)
says all of it.
