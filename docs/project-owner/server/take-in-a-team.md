# Take in a team that exists

From server 0.7.0 you take no team in yourself. The Team Admin of a team in a Supabase project, or on another
Brigade server, runs one command,
`brigade team move --to <url> --secret-file <path>`, and the running server takes the team in by request, with
the Team Admin as its owner: you run nothing, and the server is not stopped
([Move a team from Supabase](move-from-supabase.md), [Move a team to another server](move-to-another-server.md)). Your own command, `--import-team`, stays. It is the way on
a server older than 0.7.0, from 0.3.0, and for a team that is to be on the server before its administrator acts:
[Take the team in yourself](#take-the-team-in-yourself).

Either way the team keeps its join secret, so nobody is sent a new one, and its reference and its name. Its
members, sessions and waiting messages do not come over
([What comes over, and what does not](#what-comes-over-and-what-does-not)).

## By request, with the server running

The request is `/v1/team/take-in` of [the backend's contract](../../backend-rest-v1.md) (section 3b). The Team
Admin's machine makes it through the bundled `rest` adapter (`team take-in`), with the team's join secret and its
name. It is the first step of `brigade team move --to`, which then opens the team's move and records it
([Move the team to a server](../../team-admin/members/move-the-team.md)).

- **Whoever takes the team in owns it**, from that request on: the server cannot check who administered the team
  where it came from. The team's members then join, or follow, as members.
- **It is allowed as creating a team is.** A server started with `--create-token-file` asks for that token on a
  take-in too, so the Team Admin needs it ([Hand over the values](hand-over-the-values.md)). A server with no
  creation token takes a team in for anyone who can reach it and holds a join secret.
- The same request again, with the same secret, changes nothing: the team is here, and the caller joins it. With
  another secret for the same team it is refused.
- **One at a time.** A command that writes a machine's credential for a server holds a lock on that profile
  for its whole length: the take-in of a move, a join, `team create`, `team leave`, `team reset` and
  `team revoke-credentials`. Run twice at once on one machine, the second waits two seconds for the first and
  then refuses with `another brigade command is changing this profile`, having sent nothing: run it again
  when the first has ended. Two at once could otherwise leave the machine with a credential the server no
  longer knows and no retry key. The lock ends with its command however that ends, a Ctrl-C included.
- **An answer that is lost costs nothing.** The server's answer carries the credential of the owner it made. So
  that a timeout does not leave the team with an owner nobody can be, the Team Admin's machine keeps a retry
  key for the take-in, in a file of its own beside its credential, until it has the answer. Run again, the same
  command is answered as the owner the first one made, with a new credential, and the journal says `member
  <reference> made its take-in of team <name> (<team_ref>) again with the retry key of the first: it has a new
  credential, and the one it had is not known any more`. The server keeps the key's hash, never the key, and
  only until the owner first asks for something with its own credential that the server records, such as
  opening the team's move or registering a session: from then on the same command with that key is a
  member's join, and takes nothing from the owner. A rotation of the team's join secret ends the key as well.
  Without that file, on another machine or after the profile was reset, the command run again joins as a
  member, and you name the owner ([Name a team's owner](name-the-owner.md)). On a machine that joined the team
  on the server after the answer was lost, the command run again acts for the member that join made: it stops
  and says that the team is owned by someone else and that you can name its owner. The retry key is still
  there. The adapter keeps its file and says so in its own log (`logs/adapter-<profile>.log` under Brigade's
  state directory), not on the terminal. Once that machine's credential at the server is revoked, the same
  command owns the team: `brigade team revoke-credentials`, run in a checkout whose `.brigade.json` names the
  server. In a checkout that still names the backend the team leaves, that command revokes the membership
  there, the one the move is recorded with, so it is not run there. The adapter cannot tell an answer that was
  lost from a request that never arrived, and its line says so: where the server never took the team in for
  that machine, the same two steps make it a new member and leave its old membership with no credential, and
  you name the owner.
- The journal says who did it: `member <reference> took in team <name> (<team_ref>) with the join secret it has on
  another backend, and owns it`. No line holds the secret.
- **Decide about the creation token first.** A team's reference is in its committed `.brigade.json`, so it is
  public. On a server with no creation token a stranger can take in a team under your team's reference, with a
  secret of their own, and your team's take-in is then refused. A creation token closes that. From 0.9.0 you
  remove such a team with `--remove-team`, between a stop and a start, and the reference is free again
  ([Remove a team](name-the-owner.md#remove-a-team);
  [Security, section 20](../../security.md#20-a-team-on-a-brigade-server)).

## Take the team in yourself

Your own command runs on the server's data directory, with the server stopped.

| | |
| --- | --- |
| The team's join secret file | The file `team create` wrote when the team was made. The Team Admin has it. |
| The team's name | As `.brigade.json` has it in `team_name`. |
| A moment with the server stopped | One process at a time owns the server's log, so the team is taken in between a stop and a start. |

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

## Point the team at the server

The Team Admin's one command rewrites `.brigade.json` itself. For a team you took in yourself the file is changed
by hand. In each repository of the team, change three members of `.brigade.json` and leave the rest as it is:

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
[Name a team's owner](name-the-owner.md)).

## What each member does

With a move, nothing: each member's next session follows, from plugin 0.55.0, and a session that is running
follows at its next prompts, from 0.56.0 ([When your team moves](../../team-member/join/follow-a-move.md)). The
Team Admin's one command opens the move and records it. After your own `--import-team`, from server 0.5.0, you
open it and the Team Admin records it ([Let a team's members follow it](open-a-move.md)). Without a move, or for
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
