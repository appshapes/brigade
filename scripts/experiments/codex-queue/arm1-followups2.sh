#!/bin/bash
# Arm 1 follow-ups, second form: every step waits for the thread's turn to COMPLETE in the rollout (not for pane text),
# so a slow reply cannot leak into the next step. Sizes 2 KiB and 8 KiB; /compact; /new (new thread id from the log
# database); /quit + resume; the 16 KiB line last and alone.
# Usage: arm1-followups2.sh <label> <tmux-session> <thread-id> [codex flags for the resume...]
set -u
LABEL="$1"; TS="$2"; SID="$3"; shift 3
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm1-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm1-$LABEL"
. "$ROOT/lib.sh"
completes(){ grep -c '"type":"task_complete"' "$(rollout_of "$SID")" 2>/dev/null || echo 0; }
wait_complete(){ local want="$1" max="${2:-240}" i; for i in $(seq 1 $((max*2))); do [ "$(completes)" -ge "$want" ] && return 0; sleep 0.5; done; return 1; }
sendc(){ # send then wait for the turn to complete in the rollout
  local c0; c0=$(completes); send "$1" "$2" "${3:-120}"; if wait_complete $((c0+1)) "${3:-120}"; then log "$1 turn completed (completes=$(completes))"; else log "$1 turn NOT completed within ${3:-120}s"; fi; }
log "follow-ups v2 on thread $SID"; start_poller
pad(){ python3 -c 'import sys; n=int(sys.argv[1]); print("x"*n)' "$1"; }
mk(){ local n="$1" size="$2" NON END head tail; NON=$(nonce); END=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n'); head="MSG$n size-$size $NON "; tail=" END=$END"; echo "$head$(pad $((size - ${#head} - ${#tail} - 1)))$tail"; }
n=1
for size in 2048 8192; do n=$((n+1)); body=$(mk $n $size); log "size test MSG$n: ${#body} bytes, END=${body##*END=}"; sendc "MSG$n" "$body" 180; done
log "/compact"; c0=$(completes); T send-keys -t "$TS" '/compact' Enter; for i in $(seq 1 180); do grep -q '"type":"compacted"\|contextCompaction' "$(rollout_of "$SID")" && break; sleep 0.5; done; log "compaction record after ${i}x0.5s"; sleep 5; pane > "$EV/pane-compact.txt"
n=$((n+1)); sendc "MSG$n" "MSG$n after-compact $(nonce)" 120
log "/new"; t_new=$(now); T send-keys -t "$TS" '/new' Enter; sleep 8; pane > "$EV/pane-new.txt"
OLD="$SID"; NEWSID=$(sqlite3 -readonly "$HOME/.codex/logs_2.sqlite" "select thread_id from logs where ts >= ${t_new%.*} and thread_id is not null and thread_id != '$OLD' order by ts desc, id desc limit 1;" 2>/dev/null)
log "thread id after /new from the log database: ${NEWSID:-none} (old $OLD)"
n=$((n+1)); send "MSG$n" "MSG$n to-old-thread-after-new $(nonce)" 45
if [ -n "$NEWSID" ]; then SID="$NEWSID"; n=$((n+1)); c0=$(completes); send "MSG$n" "MSG$n to-new-thread-no-prompt $(nonce)" 90; wait_complete $((c0+1)) 90 && log "MSG$n new-thread turn completed" || log "MSG$n new-thread turn NOT completed"; fi
log "/quit then codex resume $OLD"; T send-keys -t "$TS" '/quit' Enter; sleep 5; T kill-session -t "$TS" 2>/dev/null; sleep 2
T new-session -d -s "$TS" -x 200 -y 50 "$ROOT/run-codex.sh --cwd $WORK resume $OLD $*; echo EXITED; sleep 3600"
stable=0; for i in $(seq 1 80); do if pane | grep -q 'Trust this folder?'; then T send-keys -t "$TS" Enter; sleep 1; continue; fi; if pane | grep -q 'Ask Codex to do anything'; then stable=$((stable+1)); [ $stable -ge 8 ] && break; fi; sleep 0.25; done
log "resumed TUI on $OLD"; sleep 3; SID="$OLD"
# the row queued to the old thread after /new may land now
sleep 10; log "after resume: completes=$(completes) queue rows for old=$(sqlite3 -readonly "$HOME/.codex/queue_1.sqlite" "select count(*) from queued_items where thread_id='$OLD';")"; pane > "$EV/pane-resumed.txt"
n=$((n+1)); sendc "MSG$n" "MSG$n after-resume $(nonce)" 120
n=$((n+1)); body=$(mk $n 16384); log "size test MSG$n: ${#body} bytes, END=${body##*END=}"; sendc "MSG$n" "$body" 240
cp "$(rollout_of "$OLD")" "$EV/rollout.jsonl"; [ -n "${NEWSID:-}" ] && cp "$(rollout_of "$NEWSID")" "$EV/rollout-new.jsonl" 2>/dev/null; stop_poller; shasum -a 256 "$EV/rollout.jsonl" > "$EV/rollout.jsonl.sha256"; log "evidence saved"
python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
