# Commit and push

Brigade is developed on `master`: merges only, never rebase, and every commit goes through one chain:

```sh
make push message="110: Redesign the documentation around five personas"
```

`make commit` typechecks, pulls (`git pull --no-edit`, a plain merge), builds, tests (with the drift tests and the
documentation gate), stages **every** working-tree change (`git add --verbose :/ .`) and commits; `make push` adds
the push. In a shared checkout, check `git status` first: another session's edits are staged too. On a merge
conflict, stop and hand it back; never `git merge --abort` into a rebase, never a force-push.

- **The message** is `<card>: <Imperative summary>`, the number being the AppShapes Trello card the work belongs to
  ([Work a card](cards.md)). `/commit <card>` in Claude Code writes the message from the diff, confirms it with you
  and runs the chain; it refuses without a numeric card.
- **The execution log.** `.context/plans/brigade-execution-log.md` is the single source of truth for where the work
  stands: a commit that finishes a piece of work adds its row in the same commit. A PR labelled `maintenance` is
  exempt.
- **Plans** go in `.context/plans/`, **scratch** in `.ignored/` (gitignored); never a stray file in the root.
- **Shell** goes through shellcheck 0.9.0 in Docker before a push ([Run the gate](../build-and-test/gates.md)).
- **Documentation** moves with the change: a changed procedure changes its leaf and its TL;DR; a new `make` help
  line runs `make docs-generate`; a new option gets its row ([Documentation](../documentation/README.md)).
