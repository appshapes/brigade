#!/bin/sh
# Says what an agent's checkout held when its session ended: paths changed or untracked, commits that no
# remote branch carries, stashed work, and a `make` or `go` still running. Called by
# scripts/ci/scheduled-job.sh, the gate of the three scheduled jobs, after the writer's session and after the
# fixer's: a writer that had nothing to change must be told from one that ended with its work uncommitted.
# It was written for the issue-driven loop (work item 398: on 2026-11-22 an agent edited two pages, never
# committed, and its issue was closed as "finished without changes"), which work item 433 removed.
#
# It never fails on what it finds: it prints the finding and, when $GITHUB_OUTPUT is set, writes its outputs:
# the four counts it took (`changed`, `unpushed`, `stashed`, and `running`, the names still running), `left`
# (`true` when a path changed, a commit was never pushed or something is stashed, else `false`) and `summary`
# (the lines printed). Its caller runs the copy of the commit the run started on, never the working tree's,
# which the agent could have edited. Usage, in the checkout: scripts/ci/agent-leftover.sh
set -eu

# One line per changed or untracked path, as `git status --porcelain` prints it. Ignored files (bin/, the
# linter, coverage output) are not listed, so a build or a test run by itself leaves nothing here.
status=$(git status --porcelain)
changed=$(printf '%s\n' "$status" | grep -c . || true)

# Commits on HEAD or on any local branch that no remote-tracking ref reaches: made in this checkout and never
# pushed. Every local branch, because an agent that committed on its own branch and then checked out another
# leaves a clean status and a HEAD with nothing new on it.
unpushed=$(git rev-list --count HEAD --branches --not --remotes)

# Work put aside with `git stash` is in neither of the two above.
stashed=$(git stash list | grep -c . || true)

# A `make` or a `go` that outlived the session is what a command the agent did not wait for looks like. Only
# these two names are looked for and only they are ever printed.
running=""
if command -v pgrep >/dev/null 2>&1; then
  for name in make go; do
    if pgrep -x "$name" >/dev/null 2>&1; then
      running="${running:+$running }$name"
    fi
  done
fi

left=false
if [ "$changed" -gt 0 ] || [ "$unpushed" -gt 0 ] || [ "$stashed" -gt 0 ]; then
  left=true
fi

# The summary goes into a job output and from there into a comment on a public issue, and its lines are file
# names an agent chose. So: status lines made of plain characters only, twenty at most, each cut at 160; every
# other line is counted, not shown. Commits are named by their short hash, never by their subject, and stashes
# by their number alone.
summary=$(
  if [ "$changed" -gt 0 ]; then
    echo "$changed changed or untracked path(s):"
    plain=$(printf '%s\n' "$status" | grep -E '^[ A-Z?!]{2} [A-Za-z0-9._/ >-]+$' || true)
    printf '%s\n' "$plain" | grep . | head -n 20 | cut -c 1-160 || true
    shown=$(printf '%s\n' "$plain" | grep . | head -n 20 | grep -c . || true)
    if [ "$shown" -lt "$changed" ]; then
      echo "and $((changed - shown)) more, not shown"
    fi
  fi
  if [ "$unpushed" -gt 0 ]; then
    hashes=$(git rev-list --abbrev-commit --max-count=10 HEAD --branches --not --remotes | tr '\n' ' ' | sed 's/ $//')
    echo "$unpushed commit(s) that no remote branch carries: $hashes"
  fi
  if [ "$stashed" -gt 0 ]; then
    echo "$stashed stash(es)"
  fi
  if [ -n "$running" ]; then
    echo "still running when the session ended: $running"
  fi
  if [ "$left" = false ] && [ -z "$running" ]; then
    echo "nothing: no path changed, no commit unpushed, nothing stashed, no make or go still running"
  fi
)

printf '%s\n' "$summary"
if [ "$left" = true ]; then
  echo "::warning::The agent's session ended with work in its checkout that it never pushed: $changed changed or untracked path(s), $unpushed unpushed commit(s), $stashed stash(es)"
elif [ -n "$running" ]; then
  echo "::warning::The agent's session ended while a command was still running: $running"
fi

if [ -n "${GITHUB_OUTPUT:-}" ]; then
  # No line of the summary can be the delimiter: each one starts with a count, two status characters or a
  # fixed word.
  {
    echo "left=$left"
    echo "changed=$changed"
    echo "unpushed=$unpushed"
    echo "stashed=$stashed"
    echo "running=$running"
    echo "summary<<AGENT_LEFTOVER_SUMMARY"
    printf '%s\n' "$summary"
    echo "AGENT_LEFTOVER_SUMMARY"
  } >> "$GITHUB_OUTPUT"
fi
