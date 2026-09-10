#!/bin/sh
# Fails a pull request that touches one of three repository invariants CLAUDE.md places outside an agent's
# judgement. Called by .github/workflows/review-pull-request.yml BEFORE the reviewer agent runs, so a violating
# PR is never even reviewed. Usage: scripts/ci/pr-guard.sh <base-sha> <head-sha>
set -eu

BASE_SHA=${1:?base sha required}
HEAD_SHA=${2:?head sha required}

# THREE DOTS, and this is load-bearing. `git diff A B` (two dots) compares the two trees, so once `master` moves
# past a release the PR's diff against it shows plugin/bin/VERSION, plugin/bin/checksums.txt and the `go` line
# as changed — the exact three paths guarded below — and every open PR is falsely accused of changing a release
# pin it never touched, feeding the fixer agent an unfixable blocker. `git diff A...B` compares B against the
# MERGE BASE, i.e. what this PR actually changed. It is also why the checkout above needs fetch-depth: 0.
RANGE="$BASE_SHA...$HEAD_SHA"

files=$(git diff --name-only "$RANGE")
fail=0

if printf '%s\n' "$files" | grep -qE '^plugin/bin/(VERSION|checksums\.txt)$'; then
  echo "::error::This PR changes a release pin (plugin/bin/VERSION or plugin/bin/checksums.txt). Those files are produced only by \`make release\` on master — see CLAUDE.md."
  fail=1
fi

if git diff "$RANGE" -- go.mod | grep -qE '^[+-]go [0-9]'; then
  echo "::error::This PR changes go.mod's \`go\` line, which is a release-reproducibility pin. Only a release bumps it — see CLAUDE.md and \`make checksums-check\` rule (c)."
  fail=1
fi

if printf '%s\n' "$files" | grep -q '^internal/protocol/' && ! printf '%s\n' "$files" | grep -q '^docs/protocol-v1\.schema\.json$'; then
  echo "::error::This PR changes internal/protocol/ without docs/protocol-v1.schema.json. Protocol v1 is frozen: types, schema (\`make schema\`), the conformance suite and both adapters move in ONE commit."
  fail=1
fi

exit "$fail"
