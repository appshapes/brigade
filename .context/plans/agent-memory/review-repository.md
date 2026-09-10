# review-repository — agent memory

The `review-repository` agent's marker (`.github/workflows/review-repository.yml` →
`.claude/agents/reviewer.md`, review-repository mode). It reads the three fields below to take its scope, and
rewrites them in the same commit as the fixes it pushes. `Commit: (none)` means the whole tree; otherwise the
scope is `git diff --name-only <Commit>..HEAD`.

Agent memory lives here, under `.context/plans/agent-memory/`, and never under `.claude/` — the writer's tooling
denies writes there, which is the only reason the reference repository needs a `record-memory.yml` workflow at
all.

Date: (unseeded)
Commit: (none)
Scope: whole tree
