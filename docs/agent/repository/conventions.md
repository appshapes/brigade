# Conventions: git, commits, work items, plans

- **Branch and history.** Work on `master`. Merges only: never `git rebase`, never `git pull --rebase`, never a
  force-push of pushed history. Integrate with `git pull --no-edit` (`make pull`).
- **A merge conflict is the session's to resolve, in place** (the owner's global rule, made this repository's on
  2026-11-13, work item 298; until then the rule here was to stop and hand it back). This is a session's rule:
  an agent of the agentic loop on a runner has nobody to ask and never runs `make push`, so it still hands a
  conflict back in a pull request comment, as `.claude/agents/developer.md` says, and nothing below is for it.
  A pull conflicts when origin and local commits it has not seen change the same lines; `make push` then stops
  at its pull, with a merge in progress.
  1. Read what each side meant (`git log --merge -p`, and the work items its commits name), keep both, and leave
     no marker; fix as well what the merged tree breaks beyond the markers (a file that merged cleanly but fails
     combined).
  2. Stage the files the resolution touched, and none of the work's own uncommitted changes (where a fix
     belongs in a file that also holds the work's changes, stage the fix's lines alone, `git add -p`), then run
     the acceptance gate and the package or server checks [gates.md](gates.md) names for what the merge
     touched, until they pass. They run on the working tree, the merge with the work's uncommitted changes: a
     failure that comes from the unfinished work is the work's to fix, not the merge's.
  3. Conclude the merge with a commit that names every file that conflicted and how it was resolved:
     `git commit -m "<work item>: Merge origin/master: <file>: <how>; <file>: <how>"`. No pull works until it
     is concluded.
  4. Push: `make push message="…"` when the work itself is still uncommitted, otherwise `make pull` then
     `git push` (`make push` on a clean tree runs its whole chain, then stops at `nothing to commit, working
     tree clean`, and the merge never reaches origin). If origin moved again, its new conflict is resolved the
     same way.

  When the pull refuses with `Your local changes to the following files would be overwritten by merge`, no merge
  has started: `git stash push -m "<work item>: before the pull"`, then `make pull`; if that pull conflicts,
  resolve it by steps 1 to 3 first; then pop that stash. It is `stash@{0}` unless another session of the
  checkout stashed meanwhile: find it by its message in `git stash list` and name it, `git stash pop stash@{n}`.
  A pop that prints `Dropped` left nothing to resolve: run `make push`. A pop that prints `CONFLICT` and
  `The stash entry is kept` left markers, `Updated upstream` for origin's side and `Stashed changes` for the
  work: read the two sides with `git stash show -p stash@{n}` and `git log -p 'stash@{n}^..HEAD' -- <file>`
  (`git log --merge` shows nothing here, no merge being in progress), keep both, clear the unmerged state with
  `git reset -q`, drop that one stash with `git stash drop stash@{n}`, and run `make push` with a message that
  also names each file that conflicted and how. Never drop a stash after a pop that dropped its own, and never
  one whose message is not yours: it is someone's parked work. When the refusal names untracked files instead,
  origin added a file the work also added, and a stash does not move it: rename the work's copy aside,
  `make pull`, and carry what it held into origin's file by hand. Never `git merge --abort` to
  escape a conflict, though git's own hint offers it, never a rebase or a force-push to resolve one. When what
  one side meant cannot be told from the code, its commits and its work item, ask the user at once
  and leave the merge in progress, never aborted: until it is concluded, no other session in the checkout can
  pull. The agentic loop's agents are not sessions and have nobody to ask: on a runner a conflict is still handed
  back in a pull request comment (`.claude/agents/developer.md`).
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
