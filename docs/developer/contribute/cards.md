# Work a card

A ticket is a card on the AppShapes Trello board, and its number is the commit prefix; Brigade's own work is a
work item of the Brigade Board since 2026-11-11 ([below](#start-a-work-item-of-the-brigade-board)). Three skills in
Claude Code use [mheap/trello-cli](https://github.com/mheap/trello-cli):

| Skill | Does |
| --- | --- |
| `/trello-read <number \| title \| id>` | the card's description, comments, checklists, attachments; search lags for a card created in the last hour, so the skill falls back to listing the board |
| `/trello-create <title> [--board] [--list] [--label] [--from file.md]` | a card; defaults: board AppShapes, list Wanting, label Brigade. Every card carries exactly one project label, one of the board's. Plain titles; Trello assigns the number. `--from file.md` takes the title from the first heading and the description from the rest |
| `/trello-done <number \| id>` | the closing comment (what shipped, the version or commit, the date, what was left out and where it went), the complete check, the archive |

**When a card is done.** Its done criteria are met and shipped (released, or merged when no release carries it; a
research or decision card when its answer is on the card) and nothing on it is open. Then run `/trello-done <card>`
without being asked. When something is still open, say what it is and leave the card; never report a card as done
without having run it. Never put a secret, a credential, a recipient's address or a customer's name on a card: the
board is as public as the repository.

## Setting up the CLI

1. `npm install -g trello-cli`
2. Create an API key at https://trello.com/power-ups/admin (New → API key).
3. `trello auth:api-key <key>`: it prints a token URL; open it, Allow, copy the token.
4. `trello auth:token <token>`
5. `trello sync`
6. `trello board:list` should list AppShapes.

The key and token live only in `~/.trello-cli/default/config.json`; they never appear in the repository, a card or
the chat.

## A typical cycle

```
1.  /trello-read 110                  read the card (or write one with /trello-create)
2.  work, in conversation             plans in .context/plans/, scratch in .ignored/, make test
3.  /commit 110                       commit and push to master
4.  /trello-done 110                  when every criterion is met and shipped
```

## Start a work item of the Brigade Board

Brigade's own work moved to the team's Brigade Board on 2026-11-11: a work item's number is the commit prefix.
`/work-item-start <number> [<collaborator session>]` takes one from reading it to Done on its own, asking you only
where the work item leaves a real choice: the work item read, assigned to you and moved to Doing, the scope and
approach agreed with you, the work built under the rule files and proven against each done criterion (a green
build alone proves nothing), reviewed, pushed to `master` with `ci.yml` green (what "deployed to development"
means here: Brigade has no development environment, and a release is not part of it), the landing comment on the
work item, and the work item moved to Done. It is finished then; the release that carries the work follows
separately, and closing the work item is yours. A second argument names another of your sessions that this one
leads over Brigade. In this repository it replaces the plugin's `/brigade:work-item-start`, which knows none of
the gates.

```
1.  /work-item-start 265              read, take, agree the scope, build, prove, review, push, CI, comment, Done
```
