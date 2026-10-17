#!/bin/bash
# Arm 5: what the MODEL's shell sees (environment names, reach) under the given flags. One prompt, one command.
# Usage: arm5-identity.sh <label> <tmux-session> [codex flags...]
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm5-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm5-$LABEL"; FACTS="$ROOT/state/shell-facts-$LABEL.txt"; rm -f "$FACTS"
. "$ROOT/lib.sh"
launch "$WORK" "$@"
P="Run exactly this one command and nothing else: $ROOT/fixture/shell-facts.sh $FACTS . Then reply with exactly the word FACTS."
T send-keys -t "$TS" -l "$P"; sleep 1; T send-keys -t "$TS" Enter; log "facts prompt submitted"
for i in $(seq 1 240); do pane | grep -q -E '^\S*\s*FACTS$' && break; sleep 0.5; done; log "facts reply after ${i}x0.5s; file present=$([ -f "$FACTS" ] && echo yes || echo no)"; pane > "$EV/pane-facts.txt"
[ -f "$FACTS" ] && { cp "$FACTS" "$EV/shell-facts.txt"; cat "$EV/shell-facts.txt" | cut -c1-400; }
SID=$(find_sid "$WORK"); echo "$SID" > "$EV/session-uuid.txt"; finish
T send-keys -t "$TS" '/quit' Enter; sleep 4; T kill-session -t "$TS" 2>/dev/null; log "closed"
