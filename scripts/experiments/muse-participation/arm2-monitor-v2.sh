#!/bin/bash
# Arm 2 v2 (route B): persistent Monitor with wake_delay_ms 0 armed by the model; outside appends wake the idle
# session?  Holds: 5 s, 120 s (and optional long hold), each followed by an append; a null control of the longest hold.
# Usage: [INBOX_OVERRIDE=<path>] [HOLDS="5 120"] [CONTROL=<s>] [LAUNCHER=<script taking the workspace dir>] arm2-monitor-v2.sh <label> <tmux-session> <long-hold-seconds|0> [muse flags...]
set -u
ROOT=${MUSE_TEST_ROOT:?set MUSE_TEST_ROOT to the test root, outside the repository}
LABEL="$1"; TS="$2"; LONG="$3"; shift 3
EV="$ROOT/evidence/arm2v2-$LABEL"; mkdir -p "$EV"
WORK="$ROOT/work-arm2v2-$LABEL"; mkdir -p "$WORK"; INBOX="${INBOX_OVERRIDE:-$WORK/inbox.ndjson}"; mkdir -p "$(dirname "$INBOX")"; : > "$INBOX"
FACTS="$ROOT/state/monitor-facts-$LABEL.txt"; rm -f "$FACTS"
now(){ python3 -c 'import time;print(time.time())'; }
log(){ printf '%s %s\n' "$(python3 -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]+"Z")')" "$*" | tee -a "$EV/driver.log"; }
pane(){ tmux -L muse88 capture-pane -p -t "$TS" -S -400 | sed -e 's/[[:space:]]*$//' | grep -v '^$'; }
sjs(){ echo "$ROOT/data/muse/sessions/2026/10/16/$SID/session.jsonl"; }
# shellcheck disable=SC2012  # session ids are UUIDs
before=$(ls "$ROOT"/data/muse/sessions/2026/10/16/ 2>/dev/null | sort)
if [ -n "${LAUNCHER:-}" ]; then LAUNCH="$LAUNCHER $WORK"; else LAUNCH="$ROOT/run-muse.sh --cwd $WORK $* -c tui.away_recap_enabled=false -c tui.prompt_hint_enabled=false -c tui.auto_session_title_enabled=false --provider meta"; fi
tmux -L muse88 new-session -d -s "$TS" -x 200 -y 50 "$LAUNCH; echo EXITED; sleep 3600"
log "launched TUI flags=[$*] cwd=$WORK"
# shellcheck disable=SC2012
for i in $(seq 1 120); do SID=$(comm -13 <(echo "$before") <(ls "$ROOT"/data/muse/sessions/2026/10/16/ | sort) | head -1); [ -n "$SID" ] && [ -f "$(sjs)" ] && break; sleep 0.25; done
sleep 5; echo "$SID" > "$EV/session-uuid.txt"; log "session $SID"; pane > "$EV/pane-00-start.txt"
PROMPT="Arm your Monitor tool once, with persistent true and wake_delay_ms 0, on exactly this command: $ROOT/fixture/watch.sh $INBOX $FACTS  . Each stdout line it prints is a message for you. Every time a line arrives, reply with exactly: RECEIVED <the line> and nothing else, then end your turn and keep waiting. Never run any other command, never read or write a file, never stop the monitor. When the monitor is running, reply with exactly the word ARMED and end your turn."
tmux -L muse88 send-keys -t "$TS" -l "$PROMPT"; sleep 1; tmux -L muse88 send-keys -t "$TS" Enter; log "prompt submitted"
armed=0; for i in $(seq 1 480); do if grep -q '"tool_name": *"monitor"' "$(sjs)" 2>/dev/null && pane | grep -q '^◆ ARMED'; then armed=1; break; fi; sleep 0.5; done
log "armed=$armed after ${i}x0.5s (monitor tool call recorded and ◆ ARMED line shown)"; pane > "$EV/pane-01-armed.txt"
if [ $armed -ne 1 ]; then log "ABORT: not armed"; pane | tail -40; exit 3; fi
grep -o '"arguments":[^}]*}' "$(sjs)" | grep -i monitor | head -3 > "$EV/monitor-arguments.txt"; grep -c . "$EV/monitor-arguments.txt" >/dev/null
sleep 3; if [ -f "$FACTS" ]; then log "monitor facts file present"; cp "$FACTS" "$EV/monitor-facts.txt"; else log "monitor facts file ABSENT (sandbox?)"; fi
intake(){ grep -c '"payload_type":"runtime.user_intent.accepted"' "$(sjs)"; }
runs(){ grep -c '"kind": "started", "prompt"' "$(sjs)"; }
n=0
for hold in ${HOLDS:-5 120} $LONG; do
  [ "$hold" = "0" ] && continue; n=$((n+1))
  log "hold $hold s (idle, nothing appended)"; sleep "$hold"
  NONCE=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n'); LINE="MSG$n after-${hold}s $NONCE"
  r0=$(runs); t0=$(now); printf '%s\n' "$LINE" >> "$INBOX"; log "appended: $LINE (runs_before=$r0)"
  woke=0; for i in $(seq 1 480); do if pane | grep -q "RECEIVED.*$NONCE"; then woke=1; break; fi; sleep 0.25; done
  t1=$(now); log "MSG$n woke=$woke latency_s=$(echo "$t1 - $t0" | bc) runs_after=$(runs)"; pane > "$EV/pane-1$n-after-msg$n.txt"
done
ctl=${CONTROL:-120}; [ "$LONG" != "0" ] && ctl=$LONG; [ "$ctl" = "0" ] && ctl=1
log "null control: $ctl s armed, nothing appended"; r0=$(runs); sleep "$ctl"; log "control runs_before=$r0 runs_after=$(runs)"; pane > "$EV/pane-20-after-control.txt"
grep -o '"payload_type":"[^"]*"' "$(sjs)" | sort | uniq -c | sort -rn > "$EV/payload-types.txt"
tmux -L muse88 send-keys -t "$TS" '/exit' Enter; sleep 6; tmux -L muse88 kill-session -t "$TS" 2>/dev/null
cp "$(sjs)" "$EV/session.jsonl"; shasum -a 256 "$EV/session.jsonl" > "$EV/session.jsonl.sha256"; log "done"
