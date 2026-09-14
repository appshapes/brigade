#!/bin/sh
# usage: scripts/experiments/E8-plugin-scope/run-session.sh <config-dir> <project-dir> <prompt> [claude flags...]
#
# Starts one non-interactive Claude Code session (`claude -p`) as a FRESH process, for measuring which plugin
# version a session loads (docs/experiments/E8-plugin-scope.md). Two things make the measurement honest, and
# the first run of E8 lacked both:
#   - every inherited variable whose name begins with CLAUDE, plus AI_AGENT, is unset (CLAUDE.md: strip by
#     prefix, never by a name list), then CLAUDE_CONFIG_DIR is set to the account under test;
#   - PATH is rebuilt from the directory of the `claude` binary plus the system directories, so the calling
#     session's own plugin `bin/` cannot answer `brigade version` on behalf of the account under test.
# Run the negative control first: with no Brigade install in the account, the session's `brigade version` must
# fail with `command not found`. Only then does a version answered inside a session mean anything.
set -eu
[ $# -ge 3 ] || { echo "usage: run-session.sh <config-dir> <project-dir> <prompt> [claude flags...]" >&2; exit 2; }
cfg=$1; proj=$2; prompt=$3; shift 3
claude_bin=$(command -v claude) || { echo "run-session.sh: claude is not on PATH" >&2; exit 2; }
for v in $(env | cut -d= -f1 | grep -E '^(CLAUDE|AI_AGENT)'); do unset "$v"; done
PATH="$(dirname "$claude_bin"):/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin"
export PATH
CLAUDE_CONFIG_DIR=$cfg
export CLAUDE_CONFIG_DIR
cd "$proj"
"$claude_bin" -p "$prompt" --output-format text --max-turns 3 "$@" < /dev/null 2>&1 | grep -v '^Warning: no stdin'
