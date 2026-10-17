#!/bin/bash
# Arm 1 follow-ups on a RUNNING armed session: line sizes, then /compact, then /new (old id and new id), then /quit + resume.
# Usage: arm1-followups.sh <label> <tmux-session> <thread-id> [codex flags for the resume...]
set -u
LABEL="$1"; TS="$2"; SID="$3"; shift 3
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm1-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm1-$LABEL"
. "$ROOT/lib.sh"
log "follow-ups on thread $SID"; start_poller
pad(){ python3 -c 'import sys; n=int(sys.argv[1]); print("x"*n)' "$1"; }
n=1
for size in 64 2048 8192 16384; do
  n=$((n+1)); NON=$(nonce); END=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')
  head="MSG$n size-$size $NON "; tail=" END=$END"; body="$head$(pad $((size - ${#head} - ${#tail} - 1)))$tail"
  log "size test: ${#body} bytes (+newline would be $size)"
  send "MSG$n" "$body" 240
  pane | grep -q "RECEIVED MSG$n size-$size $NON.*END=$END" && log "MSG$n END marker echoed: whole line reached the model" || log "MSG$n END marker NOT echoed"
done
log "/compact"; T send-keys -t "$TS" '/compact' Enter; sleep 25; pane > "$EV/pane-compact.txt"
n=$((n+1)); send "MSG$n" "MSG$n after-compact $(nonce)"
log "/new (a new thread inside the same TUI)"; T send-keys -t "$TS" '/new' Enter; sleep 6; pane > "$EV/pane-new.txt"
OLD="$SID"; n=$((n+1)); send "MSG$n" "MSG$n to-old-thread-after-new $(nonce)" 60
NEWSID=$(find_sid "$WORK"); log "newest thread row for cwd after /new: ${NEWSID:-none} (old $OLD)"
if [ -n "$NEWSID" ] && [ "$NEWSID" != "$OLD" ]; then
  SID="$NEWSID"; n=$((n+1)); send "MSG$n" "MSG$n to-new-thread-no-prompt $(nonce)" 90
fi
log "/quit then codex resume $OLD"; T send-keys -t "$TS" '/quit' Enter; sleep 5; T kill-session -t "$TS" 2>/dev/null; sleep 2
T new-session -d -s "$TS" -x 200 -y 50 "$ROOT/run-codex.sh --cwd $WORK resume $OLD $*; echo EXITED; sleep 3600"
for i in $(seq 1 60); do pane | grep -q 'Ask Codex to do anything' && break; sleep 0.5; done; sleep 3; pane > "$EV/pane-resumed.txt"; log "resumed TUI on $OLD"
SID="$OLD"; n=$((n+1)); send "MSG$n" "MSG$n after-resume-no-prompt $(nonce)" 120
cp "$(rollout_of "$OLD")" "$EV/rollout.jsonl"; [ -n "${NEWSID:-}" ] && [ "$NEWSID" != "$OLD" ] && cp "$(rollout_of "$NEWSID")" "$EV/rollout-new.jsonl"; stop_poller; log "evidence saved"
python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
