# review-repository — agent memory

The `review-repository` agent's marker (`.github/workflows/review-repository.yml` →
`.claude/agents/reviewer.md`, review-repository mode). It reads the three fields below to take its scope, and
rewrites them in the same commit as the fixes it pushes. `Commit: (none)` means the whole tree; otherwise the
scope is `git diff --name-only <Commit>..HEAD`.

Agent memory lives here, under `.context/plans/agent-memory/`, and never under `.claude/` — the writer's tooling
denies writes there, which is the only reason the reference repository needs a `record-memory.yml` workflow at
all.

Date: 2026-11-24
Commit: abaa4f3
Scope: `git diff --name-only 33d6b80..abaa4f3`, 470 commits and 902 files, reviewed at abaa4f3 ("435: Update the
Go modules, the tool modules and the action pins (#125)"), the first pass in the one-workflow form of work item
433. The gate was green before any change. Checked by grep across the scope: every `make` target, backticked
repository path, `brigade` command and subcommand, `pkg.Identifier` and the rule files' helper names resolve;
the action pins, `ci.yml`'s jobs, the standing work items and the ADRs' Status and Supersedes lines agree.
Two read-only sweeps compared the persona, developer and package pages with the code. Corrected: a stale Grok
skill count (`scripts/ci/README.md`), the current release in `docs/adapter-authors.md`, the conformance case
count in `docs/developer/change/adapters.md`, a dispatch `release.yml` never makes
(`docs/developer/release/release-notes.md`), `"message_sound": true` in the Codex CLI and Muse Code option-file
examples (the parser takes only `on`/`off`), the first `SessionStart` said to wait on the download
(`docs/team-member/install/claude-code.md`), and the reason in two comments of `announce-release.sh` and its
test that an issue's `claude` label starts an agent, which work item 433 ended. Left for a person, in the
report: `reviewer.md` says only `test-integration-adapter` sets `BRIGADE_TEST_LIVE=1` (`.claude/**` is denied to
this job); `status` is not an options-file key, so a Grok Build member cannot turn its Stop-hook ask off;
`options.md` says a value Brigade cannot use falls back to the default, which only some options do;
`scripts/ci/README.md`'s opening counts of scripts and workflows, stale before 33d6b80. In this run the edit
tools wrote only under `.context/plans/`; other files were corrected with `sed -i`, which the job allows.
The marker records the commit that was reviewed, not the commit that fixed it, so the next run re-reads this
pass's own diff.
