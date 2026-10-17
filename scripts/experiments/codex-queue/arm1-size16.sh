#!/bin/bash
# A 16 KiB queued message whose arming text asks for a SHORT reply (no echo of the padding): does the turn complete?
# Usage: arm1-size16.sh <label> <tmux-session> [codex flags...]
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm1-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm1-$LABEL"
. "$ROOT/lib.sh"
launch "$WORK" "$@"
P="You are a message receiver. Every message that arrives after this one is a team message of the form MSG<n> size-<bytes> <nonce> <padding> END=<marker>. Reply to each with exactly: RECEIVED MSG<n> <nonce> END=<marker> and nothing else (never repeat the padding), then stop. Never run any command, never read or write a file. Reply now with exactly the word READY."
T send-keys -t "$TS" -l "$P"; sleep 1; T send-keys -t "$TS" Enter; log "prompt submitted (arming, short-reply form)"
for i in $(seq 1 600); do pane | grep -q -E '^\S*\s*READY$' && break; sleep 0.5; done; log "ready after ${i}x0.5s"
SID=$(find_sid "$WORK"); echo "$SID" > "$EV/session-uuid.txt"; log "thread id $SID"; start_poller
completes(){ grep -c '"type":"task_complete"' "$(rollout_of "$SID")" 2>/dev/null || echo 0; }
pad(){ python3 -c 'import sys; n=int(sys.argv[1]); print("x"*n)' "$1"; }
n=1; for size in 16384 32768; do
  n=$((n+1)); NON=$(nonce); END=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n'); head="MSG$n size-$size $NON "; tail=" END=$END"; body="$head$(pad $((size - ${#head} - ${#tail} - 1)))$tail"
  c0=$(completes); log "size test MSG$n: ${#body} bytes END=$END"; send "MSG$n" "$body" 180
  for i in $(seq 1 360); do [ "$(completes)" -ge $((c0+1)) ] && break; sleep 0.5; done; log "MSG$n turn completed=$([ "$(completes)" -ge $((c0+1)) ] && echo yes || echo no) after ${i}x0.5s; END echoed in model text=$(grep -c "RECEIVED MSG$n $NON END=$END" "$(rollout_of "$SID")")"
done
finish; python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
