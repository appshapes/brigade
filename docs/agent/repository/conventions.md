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
  carries exactly one project label; plain titles, Trello assigns the number), `/trello-done <number | id>`
  closes one. A card is done when its done criteria are met and shipped (released, or merged when no release
  carries it; a research or decision card when its answer is on the card) and nothing on it is open. Then run
  `/trello-done <card>` without being asked. When something is still open, say what it is and leave the card;
  never report a card as done without having run it. Never put a secret, a credential or a recipient's address
  on a card: the board is as public as the repository.
- **Skills of this repository** live in `.claude/skills/`: `commit`, `trello-create`, `trello-read`, `trello-done`,
  `playwright-cli`. The Brigade plugin's own skills (`plugin/skills/`, `muse/brigade/skills/`, `codex/brigade/skills/`)
  are the product.
- **Dates** in plans and logs are absolute (`2026-10-17`), never relative.
- **Wording.** Say "the 26 test messages" or "the injection test set", never "corpus".
