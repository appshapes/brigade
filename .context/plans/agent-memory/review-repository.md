# review-repository — agent memory

The `review-repository` agent's marker (`.github/workflows/review-repository.yml` →
`.claude/agents/reviewer.md`, review-repository mode). It reads the three fields below to take its scope, and
rewrites them in the same commit as the fixes it pushes. `Commit: (none)` means the whole tree; otherwise the
scope is `git diff --name-only <Commit>..HEAD`.

Agent memory lives here, under `.context/plans/agent-memory/`, and never under `.claude/` — the writer's tooling
denies writes there, which is the only reason the reference repository needs a `record-memory.yml` workflow at
all.

Date: 2026-11-01
Commit: 33d6b80
Scope: whole tree, reviewed at 33d6b80 ("164: Keep the session map name level with a rename"). The acceptance
gate was green before any change. The pass corrected one class of drift: Grok Build has been a registered
harness since card 120, with its own package, `make grok-plugin-check`, a `ci.yml` step and `HostGrok` in
`internal/harness/config`, but the agent rule files still enumerated three harnesses. `CLAUDE.md`,
`conventions.md`, `gates.md`, `invariants.md`, `documentation.md` and `docs/agent/repository/README.md` now
name four, and `docs/agent/in-a-team/README.md`'s Grok section carries the listing shape the other three have.
The marker records the commit that was reviewed, not the commit that fixed it, so the next run re-reads this
pass's own diff.
