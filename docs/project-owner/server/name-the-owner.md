# Name a team's owner

A team on a Brigade server has one owner: the member who may rotate its join secret, revoke a member and hand the
team to someone else ([Team Admin › Members](../../team-admin/members/README.md)). The server records who that
is, so whoever runs the server can name another (from 0.4.0 of the server).

## Who the owner is

| The team | Its owner |
| --- | --- |
| Created on the server | the member who created it |
| Taken in from a Supabase project | the first member to join it on the server |
| After `brigade team transfer` | the member the owner named |

A gateway joins as a member and never becomes an owner.

A team that was taken in has no owner until someone joins, because the server cannot check who administered it
on the old backend. Anyone who holds the join secret can be that first member. So the Team Admin joins before the
repository's `.brigade.json` points anyone else at the server
([Take in a team that exists](take-in-a-team.md)). If someone else got there first, name the right owner with the
command below; if that someone has also changed the secret or put the others out,
[take the team back](#take-a-team-back).

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

## When an owner is lost

An owner who lost their machine, or ran `brigade team reset`, has lost the credential the server knew them by.
They join the team again with the join secret, which makes them a new member with a new `principal_ref`, and you
name that member the owner. Nothing else about the team changes. The member they were before stays on the roster
until the new owner revokes it.
