# Create

You need a project the [Project Owner](../../project-owner/README.md) has deployed Brigade into: its URL and its
publishable key, both public. The rest is one command and two things to hand out. A team can also run with no
project at all, over Syncthing: the same command with `--adapter syncthing`.

## If you want to

| If you want to | Go to |
| --- | --- |
| create the team and its committed file | [Create the team](#create-the-team) |
| create a team with no project, over Syncthing | [Create a team over Syncthing](#create-a-team-over-syncthing) |
| move a team off its Supabase project | [Move a team to Syncthing](#move-a-team-to-syncthing) |
| give members the join secret | [Hand out the secret](#hand-out-the-secret) |
| make another repository part of the same team | [Add a repository](#add-a-repository) |
| keep the only administrative credential safe | [Back up the credential](#back-up-the-credential) |

## Create the team

1. In the project checkout (with `!` in a session, or in a terminal):
   `brigade team create --url https://<ref>.supabase.co --key sb_publishable_… --name <team> --secret-file ~/brigade-<team>.secret`
2. `git add .brigade.json && git commit && git push`.
3. `brigade team status` names the team and the project URL.

Details: [Create the team](create-the-team.md).

## Create a team over Syncthing

1. `syncthing` on PATH, on your machine and on every member's (`brew install syncthing`, `sudo apt install syncthing`).
2. In the project checkout: `brigade team create --adapter syncthing --name <team> --secret-file ~/brigade-<team>.secret`.
3. `git add .brigade.json && git commit && git push`; the file names your machine as the team's seed device.
4. Members join while a session of yours is running; `brigade team status` says `engine: running`, `team_folder: present`.

Details: [Create a team over Syncthing](create-over-syncthing.md).

## Move a team to Syncthing

1. In the project checkout, as the creator: `brigade team migrate --secret-file ~/brigade-<team>.secret` (or `/brigade:migrate <path>` in a session).
2. `git add .brigade.json && git commit && git push`; send every member the new secret file.
3. Each member pulls and runs `/brigade:join <path>`; their sessions move at their next start. Messages and the hosted gateways stay behind.

Details: [Move a team to Syncthing](move-to-syncthing.md).

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
