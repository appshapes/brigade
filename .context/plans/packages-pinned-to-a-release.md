# Work item 424: Codex CLI and Grok Build take the package a release has

Session `worker-brigade-fix` (b91e4), 2026-11-23, given the work item by the manager session
`development-manager-brigade`. The decision and its evidence are ADR 0030; this file is how the work was done.
Where it stands is the execution log's row P106-60.

## What is asked

Work item 424's done criteria: a push to `master` that changes `codex/brigade/` or `grok/brigade/` changes nothing
for a member of those harnesses until a release carries it, shown in a live session of each; a Codex CLI member is
asked to review Brigade's hooks at most once for a release that changes its package; where a harness cannot be
pinned, that is measured and written.

## What was found first (2026-11-23, read-only)

- Both harnesses can be pinned in the marketplace file itself, at a commit: Codex CLI 0.166.0 with a `git-subdir`
  entry and a `sha`, Grok Build 1.0.92 with a `url` entry, a `sha` and a `path`.
- Codex CLI fetches the marketplace by itself when its app server starts and reinstalls its plugins when the
  marketplace's revision moved; its trust of the hooks is a hash over every file of the installed package.
- A skill from `master` did name a command the pinned binary lacked (`work-items` and `page`, 2026-11-16 to
  2026-11-23).
- Claude Code moves an install only on a version change, and Muse Code reads a snapshot the member refreshes:
  neither has the problem.

## What was built

1. **The two marketplace files** name the package at a commit, the same in both. The first is 0.59.0's
   (`b9908ccf`), the manager's call on the owner's word, a comment on the work item.
2. **`internal/packagecheck/pin.go`**: the form of both entries, one commit in both, and from git that the commit
   is a release tag's and holds both packages; `behind:` and a pass while a newer release is tagged and the pin
   has not moved; the writer. `go run ./cmd/brigade-packages pin [<commit>]` says where the pin stands, or
   writes it. The Codex CLI check and Grok Build's strict check both hold it.
3. **`scripts/release-pin.sh`**, the release sequence's step 6 and `make release-pin`: waits for the publication,
   writes the tag's commit, pushes through the chain. Rerun-safe; a pre-release moves nothing.
   `scripts/release-prep.sh` calls it after its tag, names it in a dry run, and prints where the pin stands when
   it starts.
4. **The pages**: install and update of both harnesses ("What you get, and when"; Grok Build's "An install from
   before 2026-11-23"), the two package pages, the developer's pages on a release ("The last step"), on harness
   packages ("Trying an unreleased package") and on the tools, `gates.md`, `scripts/ci/README.md`.
5. **ADR 0030**, the changelog's entries and its "Who must act" line, the execution log's row.

## Held against what 416 and 423 need of a change to the release sequence

1. The tag's commit is on `master`: unchanged. The pointer commit comes after the tag, and no branch is added.
2. No tag is pushed to try anything: the step makes none, and its tests run on fixture repositories.
3. A real run is on `master` only: the step refuses another branch, and a dry run of `release-prep.sh` prints it.
4. The override in the tag's message: the step reads and writes no tag message; a test holds the message is the
   title alone after a release with the step.
5. The guard's call in the two workflows: neither workflow is changed.
6. `ci.yml` runs for `master`: the pointer commit is a commit on `master` and has its run.
7. The guard is changed only just after a release: `release.yml` runs Grok Build's package check, which now
   reads the pin, and this lands just after 0.59.0.
8. The push chain: the step's commit goes through `make push`, and `push_chain_test.go` holds the script's line
   beside the ten places that say the chain.

## Proof

The acceptance gate with lint, the four package checks, `make docs-check`; the rig (a scratch copy served on
localhost, both harnesses' CLIs in throwaway homes) with the files in their built form; a dry run of
`make release`; an agent review; then, after the push and on the manager's word for each harness, a live session
of each against GitHub. What each showed is in the execution log's row and the landing comment.
