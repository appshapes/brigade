#!/bin/sh
# Runs the pinned Muse binary outside the Claude session: allow-listed environment only
# (no CLAUDE*, AI_AGENT*, BRIGADE*, META_API_KEY), real sign-in (real XDG_CONFIG_HOME),
# isolated data/state/cache under the test root.
# Usage: run-muse.sh [--cwd DIR] [--env K=V]... <muse args...>
ROOT=${MUSE_TEST_ROOT:?set MUSE_TEST_ROOT to the test root, outside the repository}
BIN=${MUSE_BIN:?set MUSE_BIN to the pinned muse binary (never the self-updating launcher)}
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
  PATH="/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin:$HOME/.local/bin" \
  HOME="$HOME" USER="$USER" SHELL=/bin/zsh TERM="${TERM:-xterm-256color}" LANG="${LANG:-en_US.UTF-8}" \
  TMPDIR="$ROOT/cache/tmp" \
  XDG_DATA_HOME="$ROOT/data" XDG_STATE_HOME="$ROOT/state" XDG_CACHE_HOME="$ROOT/cache" \
  MUSE_EXPERIMENTAL_FOREIGN_PERSONAL_CONTEXT_KILL=on \
  MUSE_NO_AUTO_UPDATE=1 \
  $EXTRA \
  "$BIN" "$@"
