# Conventions: git, commits, cards, plans

- **Branch and history.** Work on `master`. Merges only: never `git rebase`, never `git pull --rebase`, never a
  force-push of pushed history. Integrate with `git pull --no-edit`. On a merge conflict, stop and hand it back.
- **Commit messages** are `<card>: <Imperative summary>`, the number being the AppShapes Trello card the work
  belongs to. Commit and push through `make push message="<card>: …"` (typecheck, pull, build, test, stage every
  working-tree change, commit, push) or the `/commit <card>` skill, which is a port of it. `make push` stages
  everything in the checkout, including another session's edits: check `git status` first in a shared checkout.
- **Plans** go in `.context/plans/`; **ephemeral scratch** in `.ignored/` (gitignored) or the session's scratchpad.
  Never a stray file in the repository root. `.context/plans/brigade-execution-log.md` is the single source of
  truth for where the work stands: a commit that finishes a piece of work adds its row in the same commit (a PR
  labelled `maintenance` is exempt, owner ruling 2026-09-09).
- **Cards.** `/trello-read <number | title | id>` reads a card, `/trello-create <title> [--board] [--list]
  [--label] [--from file.md]` creates one (defaults: board AppShapes, list Wanting, label Brigade; every card
  carries exactly one project label; plain titles, Trello assigns the number), and `/trello-done <number | id>`
  closes one. A card is done when its done criteria are met and shipped (released, or merged when no release
  carries it; a research or decision card when its answer is on the card) and nothing on it is open. Then run
  `/trello-done <card>` without being asked.
  When something is still open, say what it is and leave the card;
  never report a card as done without having run it. Never put a secret, a credential or a recipient's address
  on a card: the board is as public as the repository. Every description and comment a session writes on a card
  ends with the line `Session stamp: <team>/<session name>, <YYYY-MM-DD>`, so the card says which session was
  last involved (card 163): the team is `team_name` and the session name the `session_name` of the row whose
  `session_id` equals `self_session_id` in `brigade sessions --here --json` (the roster carries a renamed
  session's new name, and so does `brigade whoami` since card 164). Outside a Brigade session there is no stamp.
- **Brigade's own work is on the team's Brigade Board** (owner, 2026-11-11, card 263). The Brigade cards that
  were open on the AppShapes Trello board moved to work items with the same numbers, and new work items continue
  from 264: a commit's number is its work item's. `brigade work-item` reads and changes them where the plugin
  carries the board (a release newer than 0.49.0). `/work-item-start <number> [<collaborator session>]` takes a
  work item from reading it to Done (2026-11-12, work item 265, the port of `/trello-start`): read, assigned to
  the user and moved to Doing, the scope agreed, built, proven, reviewed, on `master` with CI green, the landing
  comment, moved to Done. A work item it lands is finished when it is on `master` with CI green (the owner's
  ruling of 2026-10-31 for `/trello-start`; the release that carries the work is not the work item's), and it is
  not closed: closing is the owner's word, as it was for work item 264. In this repository it is the way to start
  a work item, not the plugin's `/brigade:work-item-start` of the same name, which knows none of this
  repository's gates. A work item's comment
  records its member and its session, so it carries no `Session stamp:` line; the stamp stays for Trello cards.
  The other `/trello-*` skills are not ported yet (work item 265): the archived Brigade cards stay readable in
  Trello.
- **Skills of this repository** live in `.claude/skills/`: `commit`, `trello-create`, `trello-read`, `trello-done`,
  `work-item-start`, `playwright-cli`. The Brigade plugin's own skills (`plugin/skills/`, `muse/brigade/skills/`,
  `codex/brigade/skills/`, `grok/brigade/skills/`) are the product.
- **Dates** in plans and logs are absolute (`2026-10-17`), never relative.
- **Wording.** Say "the 26 test messages" or "the injection test set", never "corpus".
