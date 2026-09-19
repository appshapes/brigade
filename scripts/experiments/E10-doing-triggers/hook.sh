#!/bin/sh
# E10 logging hook (card 25, plan row P16-0). Installed through `--settings` on the SessionStart and
# UserPromptSubmit events of ONE throwaway interactive session by run.sh. It appends one NDJSON record per
# firing to $E10_LOG: the event name, the source, a timestamp, the native session id, and two BOOLEANS about the
# prompt — whether it looks like a task-notification wake-up and whether it looks like a Brigade message frame.
# The prompt text itself is never written anywhere: the question this experiment answers is WHEN the hook fires,
# not what was said. It prints nothing on stdout, so it adds no context to the session under test.
set -eu
log=${E10_LOG:?E10_LOG must name the log file}
doc=$(cat)
printf '%s\n' "$doc" | jq -c --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" '{
  ts: $ts,
  event: .hook_event_name,
  source: (.source // ""),
  session_id: (.session_id // ""),
  prompt_chars: ((.prompt // "") | length),
  looks_like_task_notification: ((.prompt // "") | test("task-notification")),
  looks_like_brigade_frame: ((.prompt // "") | test("brigade-message"))
}' >> "$log"
