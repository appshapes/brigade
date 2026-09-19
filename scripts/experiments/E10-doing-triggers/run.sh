#!/bin/sh
# E10 — Which turns fire the UserPromptSubmit hook? (card 25, plan row P16-0; report docs/experiments/E10-doing-triggers.md)
#
# One real interactive Claude Code session, launched OUTSIDE the calling session with the inherited session
# environment stripped BY PREFIX (every CLAUDE* variable and AI_AGENT; CLAUDE_CONFIG_DIR alone survives — the
# Makefile's `unclaude` recipe, in sh). `--settings` installs hook.sh on SessionStart and UserPromptSubmit; the
# expect script types one prompt that starts a background task; the task's completion is a wake-up turn. If
# `brigade` is on PATH and the session under test appears on the roster, one Brigade message is sent to it as a
# second kind of wake-up (an inbox-socket peer turn). The hook records WHEN it fired and never what was said.
#
# Usage: sh scripts/experiments/E10-doing-triggers/run.sh [seconds-to-wait]   (default 150)
# Output: .ignored/card-25/e10/<stamp>/{hooks.ndjson,tty.log,summary.txt}; the summary is printed at the end.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/../../.." && pwd)
waitsecs=${1:-150}
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=$root/.ignored/card-25/e10/$stamp
work=$out/work
mkdir -p "$work"
log=$out/hooks.ndjson
: > "$log"

command -v expect >/dev/null || { echo "E10: expect is required" >&2; exit 2; }
command -v jq >/dev/null || { echo "E10: jq is required" >&2; exit 2; }
command -v claude >/dev/null || { echo "E10: claude is required on PATH" >&2; exit 2; }

# Strip the calling session's variables by prefix, keeping only CLAUDE_CONFIG_DIR (see the Makefile).
for v in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p'); do
  [ "$v" = CLAUDE_CONFIG_DIR ] || unset "$v"
done

settings=$(jq -cn --arg cmd "sh $here/hook.sh" '{
  hooks: {
    SessionStart:      [{hooks: [{type: "command", command: $cmd, timeout: 5}]}],
    UserPromptSubmit:  [{hooks: [{type: "command", command: $cmd, timeout: 5}]}]
  }
}')

printf 'E10: session under test starts in %s; hook log %s; waiting %ss after the prompt\n' "$work" "$log" "$waitsecs"

# The peer wake-up: after the prompt has been typed, look for the new session on the roster and send it one
# message. Best effort; the task-notification arm needs nothing from it.
if command -v brigade >/dev/null; then
  (
    sleep 70
    before=$(brigade sessions --json 2>/dev/null | jq -r '.result.sessions[]? | select(.is_self | not) | .session_id' | sort) || exit 0
    # The session under test registered at its own SessionStart; find an id that appeared since this script's
    # own view of the roster, i.e. one that is not this driver's session and is younger than 5 minutes.
    id=$(brigade sessions --json 2>/dev/null | jq -r --arg now "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '
      .result.sessions[]? | select(.is_self | not) | select(.state != "offline")
      | select((($now | fromdate) - (.created_at | sub("\\.[0-9]+"; "") | fromdate)) < 300) | .session_id' | head -1) || exit 0
    [ -n "$id" ] || { echo "E10: no fresh session on the roster; peer arm skipped" >> "$out/summary.txt"; exit 0; }
    printf '%s\n' "$before" > "$out/roster-before.txt"
    brigade send "$id" --summary "E10 probe" <<'EOF' >> "$out/send.log" 2>&1 || exit 0
E10 experiment probe: this message exists only to produce a wake-up turn. No reply is needed.
EOF
    echo "E10: peer message sent to $id at $(date -u +%H:%M:%SZ)" >> "$out/summary.txt"
  ) &
fi

cd "$work"
E10_LOG=$log expect -f "$here/drive.exp" "$out/tty.log" "$waitsecs" \
  claude --permission-mode bypassPermissions --settings "$settings" || true
wait

{
  echo "E10 run $stamp"
  echo "hook firings, in order (event, source, task-notification?, brigade-frame?, prompt chars):"
  jq -r '"  \(.ts)  \(.event)  \(.source)  task=\(.looks_like_task_notification)  frame=\(.looks_like_brigade_frame)  chars=\(.prompt_chars)"' "$log"
  echo "UserPromptSubmit firings: $(jq -c 'select(.event=="UserPromptSubmit")' "$log" | wc -l | tr -d ' ')"
  echo "  of which task-notification wake-ups: $(jq -c 'select(.event=="UserPromptSubmit" and .looks_like_task_notification)' "$log" | wc -l | tr -d ' ')"
  echo "  of which Brigade-frame wake-ups: $(jq -c 'select(.event=="UserPromptSubmit" and .looks_like_brigade_frame)' "$log" | wc -l | tr -d ' ')"
  echo "model answered DONE in the tty log: $(grep -c 'DONE' "$out/tty.log" || true)"
} | tee -a "$out/summary.txt"
