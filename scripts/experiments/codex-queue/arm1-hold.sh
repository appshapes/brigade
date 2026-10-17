#!/bin/bash
# Arm 1 long hold (route A): same as arm1-queue.sh but HOLDS may be empty (a null control only); HOLDS=2100 CONTROL=0 is the ≥ 35-min hold.
# Usage: [HOLDS="5 120"] [CONTROL=120] [NOPROMPT=1] [KEEP=1] arm1-queue.sh <label> <tmux-session> [codex flags...]
set -u
LABEL="$1"; TS="$2"; shift 2
ROOT=${CODEX_TEST_ROOT:?}; EV="$ROOT/evidence/arm1-$LABEL"; mkdir -p "$EV"; WORK="${WORKDIR:-$ROOT/work-arm1-$LABEL}"
. "$ROOT/lib.sh"
if [ -z "${EXISTING:-}" ]; then launch "$WORK" "$@"; else log "using the running TUI in tmux session $TS, cwd $WORK"; fi
daemon_facts
if [ -z "${NOPROMPT:-}" ]; then arm_prompt; fi
SID=""; for i in $(seq 1 40); do SID=$(find_sid "$WORK"); [ -n "$SID" ] && break; sleep 0.5; done
echo "$SID" > "$EV/session-uuid.txt"; log "thread id ${SID:-NONE} (threads row present after ${i}x0.5s)"
if [ -z "$SID" ]; then log "no thread row: the thread is not addressable yet"; pane > "$EV/pane-09-nosid.txt"; [ -n "${KEEP:-}" ] || { T send-keys -t "$TS" '/quit' Enter; sleep 3; T kill-session -t "$TS"; }; exit 4; fi
start_poller
n=0
for hold in ${HOLDS-5 120}; do
  n=$((n+1)); log "hold $hold s (idle, nothing sent)"; sleep "$hold"
  send "MSG$n" "MSG$n after-${hold}s $(nonce)"
done
ctl=${CONTROL:-120}
if [ "$ctl" != "0" ]; then t0=$(turns); log "null control: $ctl s idle, nothing sent (turns_before=$t0)"; sleep "$ctl"; log "control turns_before=$t0 turns_after=$(turns)"; pane > "$EV/pane-20-after-control.txt"; fi
finish
python3 -I "$ROOT/score.py" "$EV" | tee "$EV/score.txt"
if [ -z "${KEEP:-}" ]; then T send-keys -t "$TS" '/quit' Enter; sleep 4; pane | tail -3; T kill-session -t "$TS" 2>/dev/null; log "closed"; fi
