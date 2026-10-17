#!/bin/bash
# Fresh arm, second form: find the fresh thread's id from Codex's own log database (no model turn), queue to it before
# any prompt (expected: refused, no rollout yet), then arm with one prompt and queue again (expected: wake).
# Usage: arm1-fresh2.sh <label> <tmux-session>
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm1-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm1-$LABEL"
. "$ROOT/lib.sh"
t_launch=$(now)
launch "$WORK" --dangerously-bypass-approvals-and-sandbox
sleep 3
# the daemon logs carry thread_id; the newest thread id logged since launch is this TUI's
SID=$(sqlite3 -readonly "$HOME/.codex/logs_2.sqlite" "select thread_id from logs where ts >= ${t_launch%.*} and thread_id is not null order by ts desc, id desc limit 1;" 2>/dev/null)
log "thread id from the log database: ${SID:-none}; threads row: $(find_sid "$WORK"); rollout: $(rollout_of "$SID" 2>/dev/null)"
[ -z "$SID" ] && { log "no id; stop"; exit 5; }
echo "$SID" > "$EV/session-uuid.txt"; start_poller
NON=$(nonce); LINE="MSG0 before-any-prompt $NON"
t0=$(now); OUT=$("$ROOT/run-codex.sh" --cwd "$WORK" queue --thread "$SID" --message "$LINE" 2>&1); RC=$?
log "sent: MSG0 $LINE rc=$RC out=$(echo "$OUT" | tr '\n' ' ' | cut -c1-220)"
woke=0; for i in $(seq 1 240); do if pane | grep -q "$NON"; then woke=1; break; fi; sleep 0.25; done
log "MSG0 visible_in_tui=$woke latency_s=$(echo "$(now) - $t0" | bc)"; pane > "$EV/pane-MSG0.txt"
arm_prompt
SID2=$(find_sid "$WORK"); log "after the first prompt: threads row=$SID2 same_id=$([ "$SID2" = "$SID" ] && echo yes || echo no) rollout=$(rollout_of "$SID" | sed 's#.*/##')"
sleep 5; send "MSG1" "MSG1 after-first-prompt $(nonce)"
finish; python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
