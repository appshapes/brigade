# Create

You need a backend the [Project Owner](../../project-owner/README.md) runs: a Supabase project Brigade is deployed
into, with its URL and its publishable key, or a Brigade server, with its URL. The values are public. The rest is
one command and two things to hand out.

## If you want to

| If you want to | Go to |
| --- | --- |
| create the team and its committed file | [Create the team](#create-the-team) |
| create it on a Brigade server, with no Supabase project | [Create the team on a server](#create-the-team-on-a-server) |
| give members the join secret | [Hand out the secret](#hand-out-the-secret) |
| make another repository part of the same team | [Add a repository](#add-a-repository) |
| keep the only administrative credential safe | [Back up the credential](#back-up-the-credential) |

## Create the team

1. In the project checkout (with `!` in a session, or in a terminal):
   `brigade team create --url https://<ref>.supabase.co --key sb_publishable_… --name <team> --secret-file ~/brigade-<team>.secret`
2. `git add .brigade.json && git commit && git push`.
3. `brigade team status` names the team and the project URL.

Details: [Create the team](create-the-team.md).

## Create the team on a server

1. In the project checkout:
   `brigade team create --adapter rest --url https://<host>:8743 --name <team> --secret-file ~/brigade-<team>.secret`
2. Add `--certificate sha256:…` when the Project Owner gave you a fingerprint, and `--create-token-file <path>`
   when they gave you a creation token.
3. `git add .brigade.json && git commit && git push`. Members need the plugin at 0.52.0 or later.

Details: [Create the team on a server](create-on-a-server.md).

## Hand out the secret

1. Send each member the file `team create` wrote, over a password-manager share: not chat, not email.
2. They run `/brigade:join <path>` (Claude Code) or `brigade team join --secret-file <path>` (a terminal).

Details: [Hand out the secret](hand-out-the-secret.md).

## Add a repository

1. Copy `.brigade.json` from a repository already on the team into the new repository's top level; commit it.
2. `brigade team join` once in a checkout of it; no secret on a machine that already holds the credential.

Details: [Add a repository](add-a-repository.md).

## Back up the credential

1. Copy `~/.config/brigade/teams/<key>` (mode 0700) somewhere you control.
2. Without it nobody can rotate the secret, revoke a member or transfer the team, ever.

Details: [Back up the credential](back-up-the-credential.md).
