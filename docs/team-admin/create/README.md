# Create

You need a backend the [Project Owner](../../project-owner/README.md) runs. A new team starts on a Brigade
server, with its URL; a Supabase project Brigade is deployed into, with its URL and its publishable key, is the
other backend and stays supported. The values are public. The rest is one command and two things to hand out.

## If you want to

| If you want to | Go to |
| --- | --- |
| create the team and its committed file, on a Brigade server | [Create the team on a server](#create-the-team-on-a-server) |
| create it in a Supabase project instead | [Create the team in a Supabase project](#create-the-team-in-a-supabase-project) |
| give members the join secret | [Hand out the secret](#hand-out-the-secret) |
| make another repository part of the same team | [Add a repository](#add-a-repository) |
| keep the only administrative credential safe | [Back up the credential](#back-up-the-credential) |

## Create the team on a server

1. In the project checkout (with `!` in a session, or in a terminal):
   `brigade team create --adapter rest --url https://<host>:8743 --name <team> --secret-file ~/brigade-<team>.secret`
2. Add `--certificate sha256:…` when the Project Owner gave you a fingerprint, and `--create-token-file <path>`
   when they gave you a creation token.
3. `git add .brigade.json && git commit && git push`. Members need the plugin at 0.52.0 or later.
4. A team that exists in a Supabase project is not created again: one command moves it
   ([Move the team to a server](../members/README.md#move-the-team-to-a-server)).

Details: [Create the team on a server](create-on-a-server.md).

## Create the team in a Supabase project

1. Where your team's backend is a Supabase project, in the project checkout (with `!` in a session, or in a
   terminal):
   `brigade team create --url https://<ref>.supabase.co --key sb_publishable_… --name <team> --secret-file ~/brigade-<team>.secret`
2. `git add .brigade.json && git commit && git push`.
3. `brigade team status` names the team and the project URL.

Details: [Create the team in a Supabase project](create-the-team.md).

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
