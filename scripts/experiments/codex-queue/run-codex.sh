#!/bin/sh
# Runs codex outside the Claude session: allow-listed environment only (no CLAUDE*, AI_AGENT*, BRIGADE*),
# the operator's real CODEX_HOME (never a copied auth.json), the test root's TMPDIR.
# Usage: run-codex.sh [--cwd DIR] [--env K=V]... <codex args...>
ROOT=${CODEX_TEST_ROOT:?set CODEX_TEST_ROOT}
CWD="$ROOT/work"
EXTRA=""
while :; do
  case "$1" in
    --cwd) CWD="$2"; shift 2 ;;
    --env) EXTRA="$EXTRA $2"; shift 2 ;;
    *) break ;;
  esac
done
cd "$CWD" || exit 2
# shellcheck disable=SC2086
exec /usr/bin/env -i \
  PATH="/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin" \
  HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL=/bin/zsh TERM="${TERM:-xterm-256color}" LANG="${LANG:-en_US.UTF-8}" \
  TMPDIR="$ROOT/tmp" \
  $EXTRA \
  /opt/homebrew/bin/codex "$@"
