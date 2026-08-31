#!/bin/sh
# Common launcher. Strips the outer session's Claude vars by PREFIX (keeping CLAUDE_CONFIG_DIR),
# points the probe poster at a chosen frame, and starts a REAL interactive claude for you to watch.
# NOTE: both path arguments are made ABSOLUTE before we cd into the throwaway project dir.
S="$(cd "$(dirname "$0")" && pwd)"
abspath() {
  [ -z "$1" ] && return 0
  case "$1" in /*) printf '%s' "$1" ;;
    *) printf '%s' "$(cd "$(dirname "$1")" 2>/dev/null && pwd)/$(basename "$1")" ;;
  esac
}
FRAME="$(abspath "$1")"; DELAY="${2:-8}"; MODE="${3:-bypassPermissions}"; SETTINGS="$(abspath "$4")"
[ -n "$FRAME" ] && [ ! -f "$FRAME" ] && { echo "launcher: frame not found: $FRAME" >&2; exit 1; }
[ -n "$SETTINGS" ] && [ ! -f "$SETTINGS" ] && { echo "launcher: settings not found: $SETTINGS" >&2; exit 1; }
STRIP=""
for v in $(env | sed -n 's/^\(CLAUDE[A-Z_]*\)=.*/\1/p'; echo AI_AGENT); do
  [ "$v" = "CLAUDE_CONFIG_DIR" ] && continue
  STRIP="$STRIP -u $v"
done
set -- --plugin-dir "$S/plugin" --permission-mode "$MODE"
[ -n "$SETTINGS" ] && set -- "$@" --settings "$SETTINGS"
cd "$S/proj" || exit 1
# $STRIP holds "-u NAME -u NAME …" and MUST word-split into separate argv entries here.
# Quoting it makes env receive one giant argument and fail with "No such file or directory".
# shellcheck disable=SC2086
exec env $STRIP \
  BRIGADE_E03_FRAME_FILE="$FRAME" \
  BRIGADE_E03_POST_DELAY="$DELAY" \
  claude "$@"
