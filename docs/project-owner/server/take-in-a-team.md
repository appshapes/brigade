# Take in a team that exists

A team that lives in a Supabase project can move to a Brigade server and keep its join secret, so nobody is sent a
new one (from 0.3.0 of the server). The team keeps its reference and its name. Its members, sessions and waiting
messages do not come over: each member joins the server once, with the secret file they already have.

## What you need

| | |
| --- | --- |
| The team's join secret file | The file `team create` wrote when the team was made. The Team Admin has it. |
| The team's name | As `.brigade.json` has it in `team_name`. |
| A moment with the server stopped | One process at a time owns the server's log, so the team is taken in between a stop and a start. |

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
- It is a command for whoever can run the server on its data directory. No address of the server takes a team in,
  and a creation token has no part in it.

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
[Name a team's owner](name-the-owner.md)).

## What each member does

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
| Who administers the team | not carried: the first member to join on the server owns it, and the operator can name another |
| Sessions and waiting messages | not carried: a message that was waiting on the old backend stays there |
| The gateways | configured on the server with the same secret file ([Open the gateways](gateways.md)); the Slack app's two addresses and the mail provider's webhook then point at the server |

The team in the Supabase project is untouched by any of this. It serves the members who have not moved yet, and
is retired when none is left.

## Check it worked

`brigade team status` in a checkout names the team and the server's URL, and `brigade sessions` lists the members'
sessions as they join.
