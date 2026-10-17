#!/bin/bash
# Arm 3: project-level hooks (SessionStart, UserPromptSubmit, Stop, SessionEnd) under --dangerously-bypass-hook-trust;
# does UserPromptSubmit fire for a queued message; does a detached SessionStart child survive; SessionEnd budget;
# then the model runs shell-facts.sh once (identity in the model's shell).
# Usage: arm3-hooks.sh <label> <tmux-session>
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm3-$LABEL"; mkdir -p "$EV"; WORK="$ROOT/work-arm3-$LABEL"; STORE="$ROOT/state/hooks-$LABEL"; mkdir -p "$STORE"; rm -f "$STORE"/*
. "$ROOT/lib.sh"
mkdir -p "$WORK/.codex"
cat > "$WORK/.codex/hooks.json" <<JSON
{
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "$ROOT/fixture/hook-probe.sh $STORE", "timeout": 10}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "$ROOT/fixture/hook-probe.sh $STORE", "timeout": 10}]}],
    "Stop": [{"hooks": [{"type": "command", "command": "$ROOT/fixture/hook-probe.sh $STORE", "timeout": 10}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "$ROOT/fixture/hook-probe.sh $STORE", "timeout": 3}, {"type": "command", "command": "$ROOT/fixture/end-budget.sh $STORE", "timeout": 3}]}]
  }
}
JSON
cp "$WORK/.codex/hooks.json" "$EV/hooks.json"
launch "$WORK" --dangerously-bypass-approvals-and-sandbox --dangerously-bypass-hook-trust
log "hook events after launch, before any prompt: $(ls "$STORE" 2>/dev/null | tr '\n' ' ')"
arm_prompt
sleep 3; log "hook events after the first prompt: $(ls "$STORE" 2>/dev/null | tr '\n' ' ')"
SID=$(find_sid "$WORK"); echo "$SID" > "$EV/session-uuid.txt"; log "thread id $SID"; start_poller
ups_before=$(grep -c '^event=UserPromptSubmit' "$STORE/events-UserPromptSubmit.log" 2>/dev/null || echo 0)
sleep 5; send "MSG1" "MSG1 hooks-arm $(nonce)"
sleep 3; ups_after=$(grep -c '^event=UserPromptSubmit' "$STORE/events-UserPromptSubmit.log" 2>/dev/null || echo 0)
log "UserPromptSubmit records before the queued message=$ups_before after=$ups_after (Stop records: $(grep -c '^event=Stop' "$STORE/events-Stop.log" 2>/dev/null || echo 0))"
P="One exception to the rules: run exactly this one command now: $ROOT/fixture/shell-facts.sh $STORE/shell-facts.txt . Then reply with exactly the word FACTS and nothing else."
T send-keys -t "$TS" -l "$P"; sleep 1; T send-keys -t "$TS" Enter; log "facts prompt submitted"
for i in $(seq 1 240); do pane | grep -q -E '^\S*\s*FACTS$' && break; sleep 0.5; done; log "facts reply after ${i}x0.5s; file present=$([ -f "$STORE/shell-facts.txt" ] && echo yes || echo no)"; pane > "$EV/pane-facts.txt"
CHILD=$(cat "$STORE/child.pid" 2>/dev/null); log "detached child pid=${CHILD:-none} alive_before_quit=$([ -n "$CHILD" ] && kill -0 "$CHILD" 2>/dev/null && echo yes || echo no)"
log "/quit"; T send-keys -t "$TS" '/quit' Enter; sleep 6; pane > "$EV/pane-quit.txt"
log "after quit: child alive=$([ -n "$CHILD" ] && kill -0 "$CHILD" 2>/dev/null && echo yes || echo no); SessionEnd records=$(grep -c '^event=SessionEnd' "$STORE/events-SessionEnd.log" 2>/dev/null || echo 0); end ticks=$(wc -l < "$STORE/end-ticks.log" 2>/dev/null || echo 0)"
[ -n "$CHILD" ] && kill "$CHILD" 2>/dev/null
finish; cp -r "$STORE" "$EV/store"; python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
T kill-session -t "$TS" 2>/dev/null; log "closed"
