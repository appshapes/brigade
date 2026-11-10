#!/bin/sh
# Runs codex outside the calling session: an allow-listed environment only (so no CLAUDE*, AI_AGENT, BRIGADE_*,
# MUSE_* or CODEX_* variable reaches it) and the test root's TMPDIR. With no CODEX_HOME passed on, codex uses the
# operator's real home, $HOME/.codex (never a copied auth.json), which is the directory the rest of the driver
# reads: a CODEX_HOME in the caller's environment is ignored by all of it.
# Usage: run-codex.sh --cwd DIR <codex arguments...>
ROOT=${CODEX_TEST_ROOT:?set CODEX_TEST_ROOT}
[ "${1:-}" = "--cwd" ] || { echo "usage: run-codex.sh --cwd DIR <codex arguments...>" >&2; exit 2; }
CWD=$2; shift 2
cd "$CWD" || exit 2
exec /usr/bin/env -i \
  PATH="/usr/bin:/bin:/usr/sbin:/sbin:/opt/homebrew/bin" \
  HOME="$HOME" USER="$USER" LOGNAME="$USER" SHELL=/bin/zsh TERM="${TERM:-xterm-256color}" LANG="${LANG:-en_US.UTF-8}" \
  TMPDIR="$ROOT/tmp" \
  codex "$@"
