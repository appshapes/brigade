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
| choose the label teammates see beside your sessions | [Choose your label](#choose-your-label) |
| know what happens when your team moves to another backend | [When your team moves](#when-your-team-moves) |
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

## Choose your label

1. By default teammates see the email address of the Claude account your install is signed in to.
2. Set the plugin option `label` to `none`, or to any text, **before** you join; `--label` on the join command does
   the same for that one command.
3. Every Claude profile on one machine is the same member, under the label of the profile that joined:
   `brigade whoami` shows the label and where it came from, and the option `config_dir` gives a profile a
   membership of its own.

Details: [Choose your label](your-label.md).

## When your team moves

1. Do nothing. When the team's administrator moved it to a Brigade server and recorded the move, your next
   session follows and says so in one line: where the team went and who vouched for it.
2. A session that was already running stays put until it ends; start a new one.
3. To decide each move yourself: set the option `follow_team_move` to `off`, and join a moved team with
   `/brigade:join ~/brigade-<team>.secret`.

Details: [When your team moves](follow-a-move.md).

## Grok Build

Use a normally installed/trusted package in `grok --no-leader`; the inbox monitor starts through normal command permission.

Details: [Grok Build](grok.md).
