# Conventions: git, commits, work items, plans

- **Branch and history.** Work on `master`. Merges only: never `git rebase`, never `git pull --rebase`, never a
  force-push of pushed history. Integrate with `git pull --no-edit`. On a merge conflict, stop and hand it back.
- **Commit messages** are `<work item>: <Imperative summary>`, the number being the work item's on the team's
  Brigade Board. Commit and push through `make push message="<work item>: …"` (typecheck, pull, build, test,
  stage every working-tree change, commit, push) or the `/commit <work item>` skill, which is a port of it.
  `make push` stages everything in the checkout, including another session's edits: check `git status` first in
  a shared checkout. The agentic loop's runner has no board (2026-11-13, work item 265): a pull request's number
  is the work item's that its issue's title names (`<number>: <title>`), never read from the board there and
  never invented. An issue that names none is a maintenance work order and takes `15`, the number of the
  build-out card of the Trello days, which is not a work item of the board.
- **Plans** go in `.context/plans/`; **ephemeral scratch** in `.ignored/` (gitignored) or the session's scratchpad.
  Never a stray file in the repository root. `.context/plans/brigade-execution-log.md` is the single source of
  truth for where the work stands: a commit that finishes a piece of work adds its row in the same commit (a PR
  labelled `maintenance` is exempt, owner ruling 2026-09-09).
- **Work items.** Brigade's own work is on the team's Brigade Board (owner, 2026-11-11, card 263). Since
  2026-11-13 (work item 272) the team is `appshapes`, on the Brigade server at `https://brigade.appshapes.com`,
  with the mail address `appshapes-team@appshapes.com`. This repository shares it with cavalry, muster, telder
  and appshapes-www: one team, one board and one number sequence for the five repositories. Reading, adding and
  changing a work item is the product's own, `brigade work-item show|list|add|move|assign|comment` and the
  plugin's skills `brigade:work-item` and `brigade:work-items`. This repository has no skill of its own for it
  and none for Trello (owner, 2026-11-13, work item 272: `trello-create`, `trello-read` and `trello-done` are
  removed, the reading one too). The word is "work item" (2026-11-13, the choice of the session leading work
  item 272 under the owner's standing rule that a session decides on its own recommendation; the owner may
  overrule it): `make release` takes `work_item=<n>`, and `card=<n>` is still accepted with the same meaning.
  A work item of this repository carries the tag `brigade` from work item 289 on, as the other four
  repositories' carry theirs, so one listing shows a repository's work (the same session's choice; the earlier
  ones carry none).
  A `card N` in a commit, a comment or a plan is a Trello card of the time before the board; the Brigade cards
  open on 2026-11-11 are work items with the same numbers. Never put a secret, a credential, a recipient's
  address or a customer's name on a work item: the board is a folder on every teammate's machine. A work item's
  comment carries no `Session stamp:` line, because the board records its member and its session; the stamp
  rule for Trello cards (card 163) went with them (2026-11-13, work item 265).
- **Starting and finishing a work item.** `/work-item-start <number> [<collaborator session>]` takes a work item
  from reading it to Done (2026-11-12, work item 265, the port of `/trello-start`): read, assigned to the user
  and moved to Doing, the scope agreed, built, proven, reviewed, on `master` with CI green, the landing comment,
  moved to Done. In this repository it is the way to start a work item, not the plugin's
  `/brigade:work-item-start` of the same name, which knows none of this repository's gates. A work item is
  finished when its done criteria are met, the work is on `master` with CI green (a research or decision work
  item: its answer is on it) and nothing on it is open; the release that carries the work is not the work
  item's (the owner's ruling of 2026-10-31 for `/trello-start`). Then write the landing comment and move it to
  Done without being asked (2026-11-13, work item 265: the done check of the removed `trello-done`). When
  something is still open, say what it is and leave the work item where it is; never report one as finished
  without having moved it. Done is not closed: closing is the owner's word, as it was for work item 264.
- **Skills of this repository** live in `.claude/skills/`: `commit`, `work-item-start`, `playwright-cli`. The
  Brigade plugin's own skills (`plugin/skills/`, `muse/brigade/skills/`, `codex/brigade/skills/`,
  `grok/brigade/skills/`) are the product.
- **Dates** in plans and logs are absolute (`2026-10-17`), never relative.
- **Wording.** Say "the 26 test messages" or "the injection test set", never "corpus".
