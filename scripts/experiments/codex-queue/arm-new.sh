#!/bin/bash
# /new on an IDLE armed session, with the hook probe installed: does SessionStart fire (which source)? does the thread id
# change? does a message queued to the OLD id still start a turn? and one queued to the NEW id (no prompt in it yet)?
# Usage: arm-new.sh <label> <tmux-session>
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm-$LABEL"; STORE="$ROOT/state/hooks-$LABEL"; mkdir -p "$STORE"; rm -f "$STORE"/*
. "$ROOT/lib.sh"
mkdir -p "$WORK/.codex"
cat > "$WORK/.codex/hooks.json" <<JSON
{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "$ROOT/fixture/hook-probe.sh $STORE", "timeout": 10}]}],
           "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "$ROOT/fixture/hook-probe.sh $STORE", "timeout": 10}]}]}}
JSON
launch "$WORK" --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust
arm_prompt
OLD=$(find_sid "$WORK"); SID="$OLD"; echo "$OLD" > "$EV/session-uuid.txt"; log "thread id $OLD; SessionStart sources so far: $(grep '^source_or_reason' "$STORE/events-SessionStart.log" | tr '\n' ' ')"
sleep 5; t_new=$(now); T send-keys -t "$TS" -l '/new'; sleep 1; T send-keys -t "$TS" Enter; log "/new typed"; for i in $(seq 1 40); do if pane | grep -q 'Where should the new conversation run'; then T send-keys -t "$TS" Enter; log "answered the worktree question with Enter (use the current worktree) at ${i}x0.25s"; break; fi; sleep 0.25; done
sleep 8; pane > "$EV/pane-new.txt"; log "fresh chat shown (arming prompt gone from the pane): $(pane | grep -c 'You are a message receiver' | sed 's/^0$/yes/; s/^[1-9].*/no/')"
NEWSID=$(sqlite3 -readonly "$HOME/.codex/logs_2.sqlite" "select thread_id from logs where ts >= ${t_new%.*} and thread_id is not null and thread_id != '$OLD' order by ts desc, id desc limit 1;" 2>/dev/null)
log "after /new: SessionStart sources: $(grep '^source_or_reason' "$STORE/events-SessionStart.log" | tr '\n' ' '); new thread id from the log database: ${NEWSID:-none}; threads rows for cwd: $(sqlite3 -readonly "$HOME/.codex/state_5.sqlite" "select count(*) from threads where cwd='$WORK';")"
start_poller
R=$(rollout_of "$OLD"); b=$(grep -c '"type":"task_started"' "$R"); NON=$(nonce); send "MSGO" "MSGO to-old-thread-after-new $NON" 60
log "old thread turns before=$b after=$(grep -c '"type":"task_started"' "$R"); nonce in old rollout=$(grep -c "$NON" "$R"); pane shows reply=$(pane | grep -c "RECEIVED MSGO")"
if [ -n "$NEWSID" ]; then
  NON2=$(nonce); t0=$(now); OUT=$("$ROOT/run-codex.sh" --cwd "$WORK" queue --thread "$NEWSID" --message "MSGN to-new-thread-no-prompt $NON2" 2>&1); RC=$?; log "sent: MSGN to new thread rc=$RC out=$(echo "$OUT" | tr '\n' ' ' | cut -c1-160)"
  for i in $(seq 1 240); do pane | grep -q "$NON2" && break; sleep 0.25; done; log "MSGN visible in TUI after ${i}x0.25s; new rollout: $(rollout_of "$NEWSID" | sed 's#.*/##')"; sleep 15; pane > "$EV/pane-MSGN.txt"
  log "UserPromptSubmit records: $(grep -c '^event=UserPromptSubmit' "$STORE/events-UserPromptSubmit.log" 2>/dev/null)"
  # the user's first prompt in the NEW chat: does SessionStart fire for the new thread, and is it addressable then?
  T send-keys -t "$TS" -l "Reply with exactly the word READY2 and nothing else."; sleep 1; T send-keys -t "$TS" Enter; log "first prompt typed in the new chat"
  for i in $(seq 1 240); do pane | grep -q -E '^\S*\s*READY2$' && break; sleep 0.5; done; log "READY2 after ${i}x0.5s; SessionStart sources now: $(grep '^source_or_reason' "$STORE/events-SessionStart.log" | tr '\n' ' '); new rollout: $(rollout_of "$NEWSID" | sed 's#.*/##'); threads rows for cwd: $(sqlite3 -readonly "$HOME/.codex/state_5.sqlite" "select count(*) from threads where cwd='$WORK';")"
  NON3=$(nonce); t0=$(now); OUT=$("$ROOT/run-codex.sh" --cwd "$WORK" queue --thread "$NEWSID" --message "MSGN2 to-new-thread-after-its-first-prompt $NON3" 2>&1); RC=$?; log "sent: MSGN2 to new thread rc=$RC out=$(echo "$OUT" | tr '\n' ' ' | cut -c1-120)"
  for i in $(seq 1 240); do pane | grep -q "$NON3" && break; sleep 0.25; done; log "MSGN2 visible in TUI after ${i}x0.25s"; sleep 12; pane > "$EV/pane-MSGN2.txt"; R2=$(rollout_of "$NEWSID"); log "new thread rollout turns=$(grep -c '"type":"task_started"' "$R2" 2>/dev/null) nonce in it=$(grep -c "$NON3" "$R2" 2>/dev/null)"
fi
grep -h '^at=\|^event=\|^source_or_reason' "$STORE"/events-*.log > "$EV/hook-events.txt"; cp "$R" "$EV/rollout.jsonl"; [ -n "${NEWSID:-}" ] && [ -n "$(rollout_of "$NEWSID")" ] && cp "$(rollout_of "$NEWSID")" "$EV/rollout-new.jsonl"
stop_poller; T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
