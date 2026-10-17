#!/bin/bash
# Arm 1 busy: queue a message while the model is mid-turn (running a 45 s sleep under YOLO). When is it delivered?
# Usage: arm1-busy.sh <label> <tmux-session>
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm1-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm1-$LABEL"
. "$ROOT/lib.sh"
launch "$WORK" --dangerously-bypass-approvals-and-sandbox
arm_prompt
SID=$(find_sid "$WORK"); echo "$SID" > "$EV/session-uuid.txt"; log "thread id $SID"; start_poller
P="One exception to the rules: run exactly this one command now and wait for it to finish: sleep 45 . When it has finished, reply with exactly the word SLEPT and nothing else."
T send-keys -t "$TS" -l "$P"; sleep 1; T send-keys -t "$TS" Enter; log "busy prompt submitted"
sleep 12; pane > "$EV/pane-busy-before-send.txt"
send "MSGB" "MSGB while-busy $(nonce)" 150
pane | grep -q 'SLEPT' && log "SLEPT seen" || log "SLEPT not seen"
grep -n -E 'SLEPT|RECEIVED MSGB|› MSGB' "$EV/pane-MSGB.txt" | head
finish; python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
