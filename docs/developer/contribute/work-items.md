# Work from the Brigade Board

Brigade's own work is on the Brigade Board of the team `appshapes`, on the Brigade server at
`https://brigade.appshapes.com`: on a board since 2026-11-11, on this team and server since 2026-11-13. Five
repositories are on the team (brigade, cavalry, muster, telder and appshapes-www) and share one board and one
number sequence. A work item's number is the commit prefix ([Commit and push](commits.md)).

## What you need

The Brigade plugin, this machine joined to the team, and `syncthing` on `PATH`, because file sync carries the
board ([Install](../../team-member/install/README.md), [Join](../../team-member/join/README.md)). A session started
in the checkout then has the board.

## Read, add and change a work item

The board is the product's own, so this repository has no skill of its own for it. A session runs one `brigade
work-item` command for each of these, asked for in words or with the plugin's skill
([Keep the team's work on the Brigade Board](../../team-member/use/work-items.md) has every verb and refusal):

| To | Command | Plugin skill |
| --- | --- | --- |
| see the board | `brigade work-item list`; `--all` adds the hidden Hibernating list | `/brigade:work-items` |
| read one | `brigade work-item show <n>` | `/brigade:work-item <n>` |
| add one | `brigade work-item add --tag brigade`, with the title, a blank line and the description on stdin | `/brigade:work-item add <title>` |
| take one | `brigade work-item assign <n> --mine`, then `brigade work-item move <n> doing` | `/brigade:work-item <n> assign me`, then `<n> move doing` |
| comment on one | `brigade work-item comment <n>`, with the text on stdin | `/brigade:work-item <n> comment <text>` |
| move one to Done | `brigade work-item move <n> done` | `/brigade:work-item <n> move done` |

- The lists are Hibernating, Wanting, Doing and Done. A new work item goes to Wanting, and the board gives it
  the next number.
- Never put a secret, a credential, a recipient's address or a customer's name on a work item: the board is a
  folder on every teammate's machine.
- A comment carries no `Session stamp:` line: the board records the member and the session of every change.
- A `card N` in a commit, a comment or a plan is a Trello card of the time before the board. The Brigade cards
  that were open on 2026-11-11 are work items with the same numbers.

## Start a work item

`/work-item-start <number> [<collaborator session>]` takes one from reading it to Done on its own, asking you only
where the work item leaves a real choice: the work item read, assigned to you and moved to Doing, the scope and
approach agreed with you, the work built under the rule files and proven against each done criterion (a green
build alone proves nothing), reviewed, pushed to `master` with `ci.yml` green (what "deployed to development"
means here: Brigade has no development environment, and a release is not part of it), the landing comment on the
work item, and the work item moved to Done. A second argument names another of your sessions that this one leads
over Brigade. In this repository it replaces the plugin's `/brigade:work-item-start`, which knows none of the
gates.

## When a work item is finished

Its done criteria are met, the work is on `master` with `ci.yml` green (a research or decision work item: its
answer is on it), and nothing on it is open. Then write the landing comment (where the work landed, what changed,
how each done criterion was checked, what is still open and whose it is) and move the work item to Done, without
being asked. When something is still open, say what it is and leave the work item where it is; never report one
as finished without having moved it. Done is not closed: `brigade work-item close <n>` takes a work item off the
board's lists, and closing is the owner's. The release that carries the work follows separately
([Cut a release](../release/cut-a-release.md)).

## A typical cycle

```
1.  /work-item-start 265              read, take, agree the scope, build, prove, review, push, CI, comment, Done
```

The same by hand:

```
1.  brigade work-item show 265        read it (or add one with brigade work-item add --tag brigade)
2.  brigade work-item assign 265 --mine
3.  brigade work-item move 265 doing
4.  work, in conversation             plans in .context/plans/, scratch in .ignored/, make test
5.  /commit 265                       commit and push to master, then watch ci.yml until it is green
6.  brigade work-item comment 265     the landing comment, on stdin
7.  brigade work-item move 265 done   when every criterion is met and nothing is open
```
