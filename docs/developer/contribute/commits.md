# Commit and push

Brigade is developed on `master`: merges only, never rebase, and every commit goes through one chain:

```sh
make push message="110: Redesign the documentation around five personas"
```

`make commit` typechecks, pulls (`git pull --no-edit`, a plain merge), builds, tests (with the drift tests and the
documentation gate), stages **every** working-tree change (`git add --verbose :/ .`) and commits; `make push` adds
the push. In a shared checkout, check `git status` first: another session's edits are staged too.

A merge conflict is resolved where it happens, by whoever hit it, not handed back (the owner, 2026-11-13, work
item 298): keep what both sides meant, run the gate, and say in the merge's commit message which files conflicted
and how each was resolved. Never `git merge --abort` to escape one, never a rebase or a force-push to resolve
one. The steps, for a pull that conflicts and for one that refuses because local changes would be overwritten,
are in one place, the rule file: [Conventions](../../agent/repository/conventions.md). The pull request branches
of the agentic loop are the exception: an agent on a runner hands a conflict back in a comment
([The agentic loop](agentic-loop.md)).

- **The message** is `<work item>: <Imperative summary>`, the number being the work item's on the team's Brigade
  Board ([Work from the Brigade Board](work-items.md)). `/commit <work item>` in Claude Code writes the message
  from the diff and runs the chain without asking for approval; it refuses without a number and stops at anything
  that looks like a secret.
- **The execution log.** `.context/plans/brigade-execution-log.md` is the single source of truth for where the work
  stands: a commit that finishes a piece of work adds its row in the same commit. A PR labelled `maintenance` is
  exempt.
- **Plans** go in `.context/plans/`, **scratch** in `.ignored/` (gitignored); never a stray file in the root.
- **Shell** goes through shellcheck 0.9.0 in Docker before a push ([Run the gate](../build-and-test/gates.md)).
- **Documentation** moves with the change: a changed procedure changes its leaf and its TL;DR; a new `make` help
  line runs `make docs-generate`; a new option gets its row ([Documentation](../documentation/README.md)).
