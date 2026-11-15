# Take in a team that exists

A team that lives in a Supabase project can move to a Brigade server and keep its join secret, so nobody is sent a
new one (from 0.3.0 of the server). The team keeps its reference and its name. Its members, sessions and waiting
messages do not come over: each member joins the server once, with the secret file they already have.

There are two ways to take a team in. You run a command on the server's data directory, with the server stopped:
the first section below. Or, from server 0.7.0, the Team Admin's machine asks the running server to do it, and
you run nothing: [By request, with the server running](#by-request-with-the-server-running).

## What you need

| | |
| --- | --- |
| The team's join secret file | The file `team create` wrote when the team was made. The Team Admin has it. |
| The team's name | As `.brigade.json` has it in `team_name`. |
| A moment with the server stopped | For your own command: one process at a time owns the server's log, so the team is taken in between a stop and a start. A take-in by request needs none. |

## Take the team in

```sh
sudo systemctl stop brigade-server
sudo -u brigade brigade-server --data /var/lib/brigade-server --import-team <name> --join-secret-file <file>
sudo systemctl start brigade-server
```

- The command writes one record: the team, with the reference that is inside the secret, the name you gave and the
  hash of the secret. It says `took in team "<name>" (<reference>)` and ends.
- The secret is read from the file and is never an argument. Nothing the command writes repeats it.
- Run again with the same secret, it changes nothing and says the team is already here. With another secret for
  the same team it refuses.
- It is a command for whoever can run the server on its data directory, and a creation token has no part in it.
- The team has no owner until its first member joins ([Point the team at the server](#point-the-team-at-the-server)).

## By request, with the server running

From server 0.7.0 a running server takes a team in when it is asked to, by the request `/v1/team/take-in` of
[the backend's contract](../../backend-rest-v1.md) (section 3b). The Team Admin's machine makes it through the
bundled `rest` adapter (`team take-in`), with the team's join secret and its name. The server is not stopped, and
you run nothing. The Team Admin's command for it is `brigade team move --to <url> --secret-file <path>`, which
takes the team in, opens its move and records it
([Move the team to a server](../../team-admin/members/move-the-team.md)).

- **Whoever takes the team in owns it**, from that request on: the server cannot check who administered the team
  where it came from. The team's members then join, or follow, as members.
- **It is allowed as creating a team is.** A server started with `--create-token-file` asks for that token on a
  take-in too, so the Team Admin needs it ([Hand over the values](hand-over-the-values.md)). A server with no
  creation token takes a team in for anyone who can reach it and holds a join secret.
- The same request again, with the same secret, changes nothing: the team is here, and the caller joins it. With
  another secret for the same team it is refused.
- **An answer that is lost costs nothing.** The server's answer carries the credential of the owner it made. So
  that a timeout does not leave the team with an owner nobody can be, the Team Admin's machine keeps a retry
  key for the take-in, in a file of its own beside its credential, until it has the answer. Run again, the same
  command is answered as the owner the first one made, with a new credential, and the journal says `member
  <reference> made its take-in of team <name> (<team_ref>) again with the retry key of the first: it has a new
  credential, and the one it had is not known any more`. The server keeps the key's hash, never the key, until
  the team's join secret is rotated. Without that file, on another machine or after the profile was reset, the
  command run again joins as a member, and you name the owner ([Name a team's owner](name-the-owner.md)).
- The journal says who did it: `member <reference> took in team <name> (<team_ref>) with the join secret it has on
  another backend, and owns it`. No line holds the secret.
- **Decide about the creation token first.** A team's reference is in its committed `.brigade.json`, so it is
  public. On a server with no creation token a stranger can take in a team under your team's reference, with a
  secret of their own, and your team's take-in is then refused. A creation token closes that. No command removes
  such a team yet ([Security, section 20](../../security.md#20-a-team-on-a-brigade-server)).

Your own command stays, and is the one to use for a team that is to be on the server before its administrator
acts, with no owner until its first member joins.

## Point the team at the server

In each repository of the team, change three members of `.brigade.json` and leave the rest as it is:

```diff
 {
   "version": 1,
-  "adapter": "supabase",
-  "url": "https://<ref>.supabase.co",
-  "publishable_key": "sb_publishable_…",
+  "adapter": "rest",
+  "url": "https://<host>:8743",
   "team_ref": "6f0f2b41-…",
   "team_name": "ops",
   "sync": { "folders": ["docs/shared"] }
 }
```

Add `"certificate": "sha256:…"` when the server's certificate is one it made for itself
([Hand over the values](hand-over-the-values.md)). `team_ref` must stay exactly as it was: it is what the secret
names.

The Team Admin joins before the file is pushed: change it in one checkout, run `/brigade:join <path>` there with
the secret file, then commit and push. The first member to join the team on the server becomes its owner, the one
who may rotate its secret, revoke a member and transfer it (from 0.4.0 of the server;
[Name a team's owner](name-the-owner.md)). A team that the Team Admin took in by request is theirs already.

## What each member does

From server 0.5.0 and plugin 0.55.0 a member can do nothing at all: you open a move, the Team Admin records it,
and each member's next session follows ([Let a team's members follow it](open-a-move.md)). Without a move, or for
a member whose session does not follow:

1. Update the plugin to 0.53.1 or later, and pull. (0.52.0 reads a server's team file; 0.53.1 also moves the
   synced folders of a repository that had another team's file before.)
2. The next session says that `.brigade.json` does not match the team joined here, and connects to nothing.
3. `/brigade:join <path>` with the secret file they already have (`brigade team join --secret-file <path>` in a
   terminal). A member who no longer has the file is sent the same one again.

The path is needed in every checkout that was joined to the team on the old backend, not once on each machine.
Such a checkout reads the changed file as a move to another backend: it shows what changed and asks for the
secret's file, though the machine holds the server's credential from the first join. With the path the member
joins as who they already are on the server. A checkout that was never joined, a new clone or a repository that
gets the team file later, needs no path: `/brigade:join` alone. The first join is needed because a member's
credential was issued by the old backend, and the server has no way to check it.

## What comes over, and what does not

| | |
| --- | --- |
| The join secret | kept: the file every member has is still the way in |
| The team's reference and name | kept, and with the reference everything a member's machine keys by it, the board's folder among them |
| Members | not carried: each member is new to the server when they join, and `brigade team members` starts empty |
| Who administers the team | not carried: whoever takes the team in by request owns it, on a team you took in the first member to join does, and the operator can name another |
| Sessions and waiting messages | not carried: a message that was waiting on the old backend stays there |
| The gateways | configured on the server with the same secret file ([Open the gateways](gateways.md)); the Slack app's two addresses and the mail provider's webhook then point at the server |

The team in the Supabase project is untouched by any of this. It serves the members who have not moved yet, and
is retired when none is left.

## Check it worked

`brigade team status` in a checkout names the team and the server's URL, and `brigade sessions` lists the members'
sessions as they join.
