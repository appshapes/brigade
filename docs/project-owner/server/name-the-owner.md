# Name a team's owner

A team on a Brigade server has one owner: the member who may rotate its join secret, revoke a member and hand the
team to someone else ([Team Admin › Members](../../team-admin/members/README.md)). The server records who that
is, so whoever runs the server can name another (from 0.4.0 of the server).

## Who the owner is

| The team | Its owner |
| --- | --- |
| Created on the server | the member who created it |
| Moved here from a Supabase project by its Team Admin's one command, `brigade team move --to`, from server 0.7.0 | the member whose machine took it in by request: the Team Admin |
| Taken in by you, with `--import-team` | the first member to join it on the server |
| After `brigade team transfer` | the member the owner named |

A gateway joins as a member and never becomes an owner.

The server cannot check who administered a team on the old backend. So anyone who holds the join secret, and the
creation token where the server asks for one, can take a team in by request, or be the first to join one you
took in. The Team Admin's one command takes the team in before `.brigade.json` points anyone else at the server;
after your own `--import-team`, the Team Admin joins before that file is pushed
([Take in a team that exists](take-in-a-team.md)). If someone else got there first, name the right owner with
the command below. The Team Admin's one command says when that is needed: it stops, says that the team is owned
by someone else, and prints `--set-owner` with the team's reference and the principal of the Team Admin's
machine. If that someone has also changed the secret or put the others out,
[take the team back](#take-a-team-back). And if the team on the server is not your team at all, because someone
took its reference in under a secret of their own before your team came, [remove it](#remove-a-team).

## Name another owner

```sh
brigade team members --json     # on a member's machine: copy the new owner's principal_ref
sudo systemctl stop brigade-server
sudo -u brigade brigade-server --data /var/lib/brigade-server --set-owner <team_ref> --principal <principal_ref>
sudo systemctl start brigade-server
```

- `<team_ref>` is the `team_ref` of the team's `.brigade.json`. The principal must be an active member of that
  team: the command refuses any other, and refuses a team the server does not hold.
- It writes one record and says who owns the team now. Run for the member who already owns it, it changes nothing
  and says so.
- One process at a time owns the server's log, so it runs between a stop and a start, as taking a team in does.
- It is a command for whoever can run the server on its data directory. No address of the server names an owner
  this way: over the network only the owner can, with `brigade team transfer`.

## Take a team back

Naming an owner needs a member who can still join. When the wrong person owns a team and has rotated its secret or
revoked the others, nobody you would name can. One command starts the team's membership again:

```sh
sudo systemctl stop brigade-server
sudo -u brigade brigade-server --data /var/lib/brigade-server --take-back <team_ref> --secret-file <path>
sudo systemctl start brigade-server
```

- **A new join secret**, written to the file and nowhere else, with mode 0600. The path is absolute, in a
  directory that exists, and names nothing yet: the command refuses to write over a file. Every secret the team
  had before stops working.
- **Every membership ends**, the owner's among them, and their sessions close. A member who was banned stays
  banned.
- **The record of where the team went is cleared**, from 0.8.0, when its owner had made one
  ([Move a team to another server](move-to-another-server.md)). The record is the owner's word: whoever held
  the team could have recorded a move to a server of their own, and every member who came back would have
  followed it. The command then says `cleared the record of where it went` in its line.
- **The team has no owner** until someone joins with the new secret, and the first to do so owns it. So give the
  file to the Team Admin alone, let them join, and only then let them hand it to the members they keep
  ([Hand out the secret](../../team-admin/create/hand-out-the-secret.md)).
- A member who joins again from the machine they used before is the member they were. The Team Admin can ban
  whoever took the team, by the `principal_ref` it had
  ([Revoke or ban a member](../../team-admin/members/revoke-a-member.md)).
- The gateways are put out with everyone else: replace their secret file with the new one
  ([Open the gateways](gateways.md#after-the-join-secret-is-rotated)). A gateway never becomes the owner, so it
  does not matter whether it joins before the Team Admin.
- It says `took back team "<name>" (<reference>)`, how many memberships it ended and where the secret is. If it
  says the secret could not be written, the secret is lost: run it again.

## Remove a team

A team's reference is public: it is the `team_ref` of the team's committed `.brigade.json`. On a server that asks
for no creation token, anyone who can reach it can take a team in under that reference, with a join secret of
their own invention. The team whose reference it is then cannot come in: its Team Admin's
`brigade team move --to` stops and says that the server holds a team of this reference under another join secret.
Naming an owner does not help, because your team has no member there. Taking the team back does not help either:
it gives the team a new secret, and your team comes in under the secret its members hold. From 0.9.0 one command
removes the team that holds the reference
([ADR 0011](../../decisions/0011-remove-a-team-from-a-server-by-the-operators-command.md)):

```sh
sudo systemctl stop brigade-server
sudo -u brigade brigade-server --data /var/lib/brigade-server --remove-team <team_ref>
# The reference is now free for anyone who can reach the server, the stranger included.
# Keep it for your team before the start: take the team in with the secret file its Team Admin sent.
# Leave the next line out only on a server that is started with --create-token-file.
sudo -u brigade brigade-server --data /var/lib/brigade-server --import-team <name> --join-secret-file <file>
sudo systemctl start brigade-server
```

- **Make sure which team it is.** The server cannot tell a stranger's team from yours; you and the Team Admin
  can. `<team_ref>` is the `team_ref` of the team's `.brigade.json`, spelled exactly as it is there. The journal
  has the line of the take-in, with its time: `member <reference> took in team <name> (<team_ref>) with the join
  secret it has on another backend, and owns it`. The Team Admin knows whether that was their machine.
- **Everything the team holds goes**: its members, their sessions and the messages they held, its open move and
  the record of where it went. No command brings it back. Where there is any doubt, copy the data directory
  after the stop and before the command ([Back it up](keep-the-server.md#back-it-up)).
- **It says what it removed**, on your terminal: `removed team "<name>" (<reference>) with its 2 members: it was
  owned by <principal_ref> ("<label>"), and its reference is free`. The members are the ones its roster showed.
  A team that recorded where it went adds `and the record of where it went`, and a team nobody owned says `it
  had no owner`. The line is not in the service's journal, and the server holds nothing of the team afterwards,
  so keep the line if you want an account of what was removed.
- **The step after the removal keeps the reference for your team.** A server that asks for no creation token
  takes the reference in again for the first caller that asks, the stranger included. `--import-team` puts your
  team there, under its own secret, before the server serves; `<name>` is the team's name as its
  `.brigade.json` has it in `team_name`
  ([Take the team in yourself](take-in-a-team.md#take-the-team-in-yourself)). The stranger's take-in is then the
  one that is refused, and the Team Admin's take-in is answered that the team is there and that their machine
  owns it, as the first to join it. A server that is started with `--create-token-file` takes nothing in
  without the token, so there the step can be left out: give the token's file to the Team Admin
  ([Hand over the values](hand-over-the-values.md)), whose take-in then takes the team in and owns it.
- **Then the Team Admin runs the move again**: `brigade team move --to <url> --secret-file <path>`, with
  `--create-token-file <path>` where the server asks for a token.
- **What it refuses**, with no team removed:
  - a reference the server does not hold: `the team was not removed: not_found: the server holds no team of
    that reference`. The reference is compared exactly as it is typed: another case, or a space before or
    after it, names no team;
  - a `--data` that is not a server's data directory: `the team was not removed: <dir> is not a server's data
    directory: it holds no record.key, which a server makes on its first start`. Nothing is made there: a path
    that was typed wrongly does not become a directory;
  - a server that runs on the directory: `another server is running on <dir>`;
  - `--remove-team` given twice: `--remove-team is given once: one run removes one team`, and neither team is
    removed;
  - another of the commands on this page, `--import-team`, `--open-move` or `--close-move`, or an option that
    serving takes, on the same command line.
- **A member of the removed team is a member of nothing there.** The server still knows the credential of
  their machine: a credential names an installation, which may be a member of other teams on the server, and
  those are untouched. Whatever they ask about the removed team is answered `unauthorized`, `not an active member
  of this team`, as for a team that does not exist. A session they had in it is answered the same, unless they
  are still a member of another team on the server: there it is answered `not_found`, as a session that was
  never theirs is. The secret they hold joins nothing.
- **The gateways.** The command reads no gateway file and changes nothing the gateways keep for themselves. In
  the case above the file's entries are your team's, whose gateways were never members of the stranger's team:
  leave them as they are, and the gateways join by themselves once your team is there. Entries for a team that
  was removed and is not coming back are yours to take out
  ([After a team is removed](gateways.md#after-a-team-is-removed)).
- **What stays on the disk.** The command writes a snapshot that holds nothing of the team. The snapshots before
  it and the log's earlier entries still hold the team, its messages and, where it recorded a move, that move's
  ticket: the server keeps the newest three snapshots, so the last of them is replaced once two more have been
  written, the file log keeps every entry, and the feed lets its earlier recordings go as later snapshots cover
  them. A copy of the directory made before the removal holds all of it
  ([Security, section 20](../../security.md#20-a-team-on-a-brigade-server)).
- **Going back to 0.8.0** on the directory afterwards: [Update it](keep-the-server.md#update-it).
- It is a command for whoever can run the server on its data directory. No address of the server removes a
  team, and a team's owner has no command that does.

It also removes a team that is yours and is no longer wanted, such as one that moved to another server and whose
members have all followed ([The server the team leaves](move-to-another-server.md#the-server-the-team-leaves)).
There the block is the stop, the removal and the start: nothing is taken in after it.

## When an owner is lost

An owner who lost their machine, or ran `brigade team reset`, has lost the credential the server knew them by.
They join the team again with the join secret, which makes them a new member with a new `principal_ref`, and you
name that member the owner. Nothing else about the team changes. The member they were before stays on the roster
until the new owner revokes it.
