# Join

A team belongs to a project: the committed `.brigade.json` at the repository's top level names it, and the join
secret your administrator sent you, saved outside the repository, lets you in. You join each checkout once.

## If you want to

| If you want to | Go to |
| --- | --- |
| join from a Claude Code session | [Join in Claude Code](#join-in-claude-code) |
| join from a Muse Code session | [Join in Muse Code](#join-in-muse-code) |
| join from a Codex CLI session | [Join in Codex CLI](#join-in-codex-cli) |
| join a second clone, or another repository of the same team | [Join another checkout](#join-another-checkout) |
| join a team that runs over Syncthing, with no project | [Join a team over Syncthing](#join-a-team-over-syncthing) |
| choose the label teammates see beside your sessions | [Choose your label](#choose-your-label) |
| use Grok Build join status | [Grok Build](#grok-build) |

## Join in Claude Code

1. Save the secret file your administrator sent you outside the project folder, say `~/brigade-<team>.secret`.
2. In a session opened in the checkout: `/brigade:join ~/brigade-<team>.secret`.
3. The session attaches at your next prompt; the start line names your team, and `/brigade:sessions` shows who is on it.

Details: [Join in Claude Code](claude-code.md).

## Join in Muse Code

1. Save the secret file your administrator sent you outside the project folder.
2. In a `muse` session opened in the checkout, after one prompt: ask it to use the `join` skill with
   `~/brigade-<team>.secret`; or, in a terminal, `brigade team join --secret-file ~/brigade-<team>.secret`.
3. The session attaches at your next prompt; `brigade sessions` shows who is on the team.

Details: [Join in Muse Code](muse-code.md).

## Join in Codex CLI

1. Save the secret file your administrator sent you outside the project folder.
2. In a `codex` session opened in the checkout, after one prompt: `$brigade:join ~/brigade-<team>.secret`; or, in a
   terminal, `brigade team join --secret-file ~/brigade-<team>.secret`.
3. The session attaches at your next prompt; `brigade sessions` shows who is on the team.

Details: [Join in Codex CLI](codex.md).

## Join another checkout

1. In the new checkout, `/brigade:join` with no path (or `brigade team join` in a terminal).
2. It shows the team the file names; in a terminal you confirm. No secret: this machine already holds the credential.

Details: [Join another checkout](another-checkout.md).

## Join a team over Syncthing

1. `brew install syncthing` or `sudo apt install syncthing`: the team's messages travel through Syncthing.
2. Join as above, while a session of one of the team's seed machines is running.
3. `brigade team status` says `team_folder: present` once the team has reached your machine, within seconds.

Details: [Join a team over Syncthing](over-syncthing.md).

## Choose your label

1. By default teammates see the email address of the Claude account your install is signed in to.
2. Set the plugin option `label` to `none`, or to any text, **before** you join; `--label` on the join command does
   the same for that one command.

Details: [Choose your label](your-label.md).

## Grok Build

Use a normally installed/trusted package in `grok --no-leader`; the inbox monitor starts through normal command permission.

Details: [Grok Build](grok.md).
