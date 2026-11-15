# Move a team to another server

A team that lives on one Brigade server moves to another with one command, which its Team Admin runs. Neither
server's operator runs a command for the move, and the members run none: each one's next session follows, and a
session that is running follows at its next prompts. It works when the server the team leaves is 0.8.0 or later,
which keeps the record its members follow on, the server it goes to is 0.7.0 or later, and the plugin is 0.57.0 or
later ([ADR 0010](../../decisions/0010-keep-the-record-of-a-teams-move-on-the-server-it-leaves.md)). It is also how
a server's own address changes without a join on every machine: [Move it](keep-the-server.md#move-it).

## What each must have in place

| Who | What |
| --- | --- |
| The operator of the server the team leaves | The server at 0.8.0 or later ([Update it](keep-the-server.md#update-it)): an older one keeps no record, and the command says so and changes nothing. The server stays up, at the address its members know, until every member has followed. |
| The operator of the server the team goes to | A server at 0.7.0 or later that does not hold the team: [a fresh one, never a copy of the old one's data](#the-server-the-team-goes-to-must-not-be-a-copy). Its URL, its certificate's fingerprint when it made its own, and the creation token's file when it asks for one, given to the Team Admin ([Hand over the values](hand-over-the-values.md)). |
| The Team Admin | To own the team on the server it leaves: its creator, or whoever it was transferred to or the operator named ([Name a team's owner](name-the-owner.md)). The team's join secret file. Plugin 0.57.0 or later. |
| Each member | Plugin 0.57.0 or later. A session on an older plugin does not follow a team that leaves a server: the command names the ones that are online. |

## The move

1. The Team Admin runs, in a checkout of the team, or asks a session to (`/brigade:move`):

   ```sh
   brigade team move --to <url> --secret-file <path>
   ```

   with `--certificate sha256:<fingerprint>` and `--create-token-file <path>` when the new server needs them. It
   checks first, on the server the team leaves, that it keeps a record and that the caller owns the team there.
   Then the new server takes the team in with the Team Admin as its owner and opens the move, the old server
   records where the team went, and only then is `.brigade.json` rewritten. Every step and every refusal is in
   [Move the team to a server](../../team-admin/members/move-the-team.md).
2. The Team Admin commits and pushes `.brigade.json` in every repository of the team.
3. The members follow. Each machine joins the new server once, with the move's ticket, under the label it had.

## The server the team leaves

- **It keeps the team as it is.** Nothing there is ended, closed or refused because the team moved: its
  members, its sessions and its join secret are what they were. A member who has not followed yet still reaches
  it, and that is how that member's session reads where the team went.
- **It holds the record**, and says so once in its journal:

  ```
  member 5f0c… recorded that team ops (3b89…) moved to https://new.example.com:8743 (adapter rest, certificate sha256:…): its members' sessions follow it there, and the team stays here as it is
  ```

  The record names the new server and holds the move's ticket, whole: the one secret a server keeps that is not a
  hash, because it hands it to the team's members. Whoever can read the data directory can read it, and a copy
  of the directory holds it. No line of the journal does.
- **Only the team's owner writes the record or clears it.** `brigade team move --clear`, run by the Team Admin,
  clears it. You have no command that writes one. One of yours clears it: `--take-back`, which takes the team
  from whoever holds it ([Take a team back](name-the-owner.md#take-a-team-back)).
- **Do not go back to 0.7.0** on its data directory once a move is recorded there: an older server does not
  start on it until the record is cleared and the server has stopped once since
  ([Update it](keep-the-server.md#update-it)).
- **Ending the team there is a later step, and not yours.** No command removes a team from a server. Once every
  member has followed, the Team Admin can close the old server to it: rotate the join secret there, to a new
  file, so that nobody new joins, and revoke the members, each with `--on <the old server's url or host>`
  ([Move the team to a server](../../team-admin/members/move-the-team.md#undo-it-and-end-it)). The flag matters:
  after the move the Team Admin's checkout is joined to the new server, and without it the two commands act
  there. Each says the server it acts on, in a line before it acts and as the `url` of its result, with the
  new secret's version or the number of memberships it ended: that is where a rotation and a revocation show.
  Your journal has no line for either; a rotation is there only when it closed a move this server had open. A
  member who was revoked here and whose team later comes back joins again with the move's ticket, with no
  command, as the member they were; an installation that was banned here stays out, and a ban holds for that
  installation and no further ([Revoke or ban a member](../../team-admin/members/revoke-a-member.md)). What the
  team wrote stays in the data directory until the directory goes.

## A team with gateways on the server it leaves

The gateways do not move with the team. A gateway is a member of the team on the server that runs it, and the
server the team leaves keeps the team, so its gateways go on carrying mail and Slack to the team **there**, where
fewer and fewer sessions listen.

1. On the new server, open the gateways for the team: add it to the gateway configuration with its join secret
   file, which is the file it had, and start the server with it ([Open the gateways](gateways.md)). The new
   server needs an HTTPS name with a certificate a public authority signed.
2. Point the Slack app's two request URLs and the mail provider's webhook at the new server. A Slack app points
   at one server: from that moment Slack reaches the team on the new server only.
3. On the old server, take the team out of the gateway configuration and start the server again.

Do it when the members have followed, or at the moment the Team Admin pushes the team file: between the move and
step 2, a mail or a Slack message reaches only the sessions that are still on the old server.

## The server the team goes to must not be a copy

Start the new server on a data directory of its own. A server started from a copy of the old one's data directory,
at another address, is no place to move the team to, and neither is the old server itself under another address
(a second DNS name, another port in front of it). The same holds for any server the team was on before: the
command knows it by the address the Team Admin's machine joined it under, and a team goes back to it by that
address.

- **It holds the team already**, with every member the old one has and the owner the old one has.
- **The Team Admin's machine does not know it.** A member's machine knows a server by its address, and holds
  its credential for the old address only. So the command's take-in arrives with no credential and is the join
  of a new member, who does not own the team.
- **The command sees that and stops.** It finds a membership the Team Admin's machine has of the team, on the
  old server or on one the team was on before, on the roster of the one it was pointed at, and says so: `<host> holds team … with the membership this
  machine has on <old host>: it is the same server under another address, or a copy of its data, and a team
  is not moved to either`. It ends the membership its take-in had just made, so the Team Admin is on that
  roster once, and records nothing. It does not print `--set-owner` for this, and you should not run it:
  on the old server under another address it would take the team from the Team Admin's own membership, and
  on a copy it would let the move go on, with every member who followed there twice, once as the member the
  copy held and once as the member the ticket let in.
- **An address that differs only in spelling is the same address.** The host in another case, the scheme's own
  port (`:443`), a slash at the end: the command knows the server by the spelling `.brigade.json` has, and says
  that the file names it already.

A fresh server holds only what the move puts there: the team, its owner, and each member as they follow.

## What comes over, and what does not

| | |
| --- | --- |
| The team's reference, its name and its join secret | Yes: the new server takes the team in with the secret it has. |
| Each member, under the label they had | Yes, as they follow: a new membership on the new server. |
| The owner | The Team Admin who ran the command. A transfer or a ban made on the old server is not carried: bans are made again. |
| Sessions | They register anew on the new server, under the names they had, with new ids. |
| Messages that were waiting | No. They stay on the old server until their retention ends: move at a quiet moment. |
| The gateways | No: [above](#a-team-with-gateways-on-the-server-it-leaves). |
| The board and synced folders | They are not on a server: they stay as they are, because the team's reference does not change. |

## A member who missed a move

A team that moves twice, from A to B and then to C, is followed one move at a time. A member whose machine was
still on A when the file came to name C follows nothing: the session says that `.brigade.json` does not match the
team, because A records a move to B and the file names C. That member joins C with the secret file
(`/brigade:join <path>`), or starts one session with the team file of the first move, which follows to B while
B's move is open, and then pulls: the next session follows from B to C.

## Check it worked

On a member's machine, after a session followed: `brigade team status` names the new server. On the Team Admin's:
`brigade team move --show --from <the old server's host>` reads the record back and says whether it names what
`.brigade.json` names. On the new server, its journal has one line for the take-in and one for each member that
came in `with its move ticket`.
