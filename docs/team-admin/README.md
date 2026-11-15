# Team Admin

You create a team and you are its only administrator: the credential `team create` left on your machine is the
one that rotates the secret, revokes a member and hands the team over. A team belongs to a project, through the
committed `.brigade.json`, and you decide what else that file makes the project's checkouts share. You are also
the one who moves the team from a Supabase project to a Brigade server, with one command.

## If you want to

| If you want to | Go to |
| --- | --- |
| create a team, hand out its secret, add a repository, back up the credential | [Create](create/README.md) |
| decide what the project's checkouts sync, set up the digests, how often sessions check in, how people without a session reach the team | [Configure](configure/README.md) |
| revoke or ban a member, rotate a leaked secret, transfer the team | [Members](members/README.md) |
| move the team from a Supabase project to a Brigade server: one command, and your members run none | [Members](members/README.md#move-the-team-to-a-server) |
| end a team | [Delete](delete/README.md) |

## Create

One command in the project checkout creates the team, writes `.brigade.json` and the secret file, and stores your
credential. [Create](create/README.md) also covers handing out the secret, adding a repository and the backup.

## Configure

`.brigade.json` may carry a `sync`, a `polling` and a `gateway` member, each read by every session of the project.
[Configure](configure/README.md) says what each does and what every member's plugin must be first.

## Members

Revoking takes effect at once, rotating evicts nobody, a transfer is the only way out of the creator role, and
one command, `brigade team move --to <url> --secret-file <path>`, moves the team from a Supabase project to a
Brigade server. [Members](members/README.md) has each procedure in order.

## Delete

Brigade has no team-delete verb; a team ends when its members are revoked and its secret rotated away.
[Delete](delete/README.md) says what remains.
