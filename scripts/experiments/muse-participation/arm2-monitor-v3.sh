#!/bin/bash
# Arm 2 v3 (route B): line sizes (2 KiB, 8 KiB, 16 KiB arrive whole?), then re-arm questions: wake after /compact,
# after /clear, and after /exit + `muse resume <uuid>`.  Usage: arm2-monitor-v3.sh <label> <tmux-session> [muse flags...]
set -u
ROOT=${MUSE_TEST_ROOT:?set MUSE_TEST_ROOT to the test root, outside the repository}
LABEL="$1"; TS="$2"; shift 2
EV="$ROOT/evidence/arm2v3-$LABEL"; mkdir -p "$EV"
WORK="$ROOT/work-arm2v3-$LABEL"; mkdir -p "$WORK"; INBOX="$WORK/inbox.ndjson"; : > "$INBOX"
FACTS="$ROOT/state/monitor-facts-v3-$LABEL.txt"; rm -f "$FACTS"
now(){ python3 -c 'import time;print(time.time())'; }
log(){ printf '%s %s\n' "$(python3 -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]+"Z")')" "$*" | tee -a "$EV/driver.log"; }
pane(){ tmux -L muse88 capture-pane -p -t "$TS" -S -400 | sed -e 's/[[:space:]]*$//' | grep -v '^$'; }
sjs(){ echo "$ROOT/data/muse/sessions/2026/10/16/$SID/session.jsonl"; }
FLAGS="$* -c tui.away_recap_enabled=false -c tui.prompt_hint_enabled=false -c tui.auto_session_title_enabled=false --provider meta"
# shellcheck disable=SC2012  # session ids are UUIDs
before=$(ls "$ROOT"/data/muse/sessions/2026/10/16/ 2>/dev/null | sort)
tmux -L muse88 new-session -d -s "$TS" -x 200 -y 50 "$ROOT/run-muse.sh --cwd $WORK $FLAGS; echo EXITED; sleep 3600"
log "launched TUI flags=[$FLAGS]"
# shellcheck disable=SC2012
for i in $(seq 1 120); do SID=$(comm -13 <(echo "$before") <(ls "$ROOT"/data/muse/sessions/2026/10/16/ | sort) | head -1); [ -n "$SID" ] && [ -f "$(sjs)" ] && break; sleep 0.25; done
sleep 5; echo "$SID" > "$EV/session-uuid.txt"; log "session $SID"
PROMPT="Arm your Monitor tool once, with persistent true and wake_delay_ms 0, on exactly this command: $ROOT/fixture/watch.sh $INBOX $FACTS  . Each stdout line it prints is a message for you; every line starts with MSG, a number, a 12-character nonce, and ends with the marker END= followed by 6 characters. Every time a line arrives, reply with exactly: RECEIVED MSG<number> <nonce> END=<the 6 characters after END=> and nothing else, then end your turn and keep waiting. Never run any other command, never read or write a file, never stop the monitor. When the monitor is running, reply with exactly the word ARMED and end your turn."
submit(){ tmux -L muse88 send-keys -t "$TS" -l "$1"; sleep 1; tmux -L muse88 send-keys -t "$TS" Enter; }
arm(){ submit "$PROMPT"; log "arming prompt submitted"; armed=0; for i in $(seq 1 480); do if pane | grep -q '^◆ ARMED'; then armed=1; break; fi; sleep 0.5; done; log "armed=$armed after ${i}x0.5s"; }
arm; [ $armed -ne 1 ] && { log ABORT; exit 3; }; sleep 3
n=0
send(){ # send <size-bytes> <tag>
  n=$((n+1)); NONCE=$(od -An -N6 -tx1 /dev/urandom | tr -d ' \n'); TAIL=$(od -An -N3 -tx1 /dev/urandom | tr -d ' \n')
  head="MSG$n $NONCE "; foot=" END=$TAIL"; padlen=$(( $1 - ${#head} - ${#foot} - 1 )); [ $padlen -lt 0 ] && padlen=0
  pad=$(head -c "$padlen" /dev/zero | tr '\0' 'x'); LINE="$head$pad$foot"
  t0=$(now); printf '%s\n' "$LINE" >> "$INBOX"; log "appended MSG$n size=$(( ${#LINE} + 1 )) tag=$2 nonce=$NONCE end=$TAIL"
  woke=0; for i in $(seq 1 480); do if pane | grep -q "RECEIVED MSG$n $NONCE END=$TAIL"; then woke=2; break; elif pane | grep -q "RECEIVED MSG$n $NONCE"; then woke=1; fi; sleep 0.25; done
  t1=$(now); log "MSG$n ($2) woke=$woke (2=whole line echoed, 1=nonce only) latency_s=$(echo "$t1 - $t0" | bc)"; pane > "$EV/pane-msg$n-$2.txt"
}
send 64 baseline; sleep 5
send 2048 2KiB; sleep 5
send 8192 8KiB; sleep 5
send 16384 16KiB; sleep 5
log "/compact"; submit "/compact"; sleep 25; pane > "$EV/pane-after-compact.txt"; send 64 after-compact; sleep 5
log "/clear"; submit "/clear"; sleep 10; pane > "$EV/pane-after-clear.txt"; send 64 after-clear
log "exit then resume"; submit "/exit"; sleep 8; tmux -L muse88 kill-session -t "$TS" 2>/dev/null; cp "$(sjs)" "$EV/session-before-resume.jsonl"
tmux -L muse88 new-session -d -s "$TS" -x 200 -y 50 "$ROOT/run-muse.sh --cwd $WORK $FLAGS resume $SID; echo EXITED; sleep 3600"; sleep 12; pane > "$EV/pane-after-resume.txt"
send 64 after-resume
grep -o '"payload_type":"[^"]*"' "$(sjs)" | sort | uniq -c | sort -rn > "$EV/payload-types.txt"
submit "/exit"; sleep 6; tmux -L muse88 kill-session -t "$TS" 2>/dev/null; cp "$(sjs)" "$EV/session.jsonl"; if [ -f "$FACTS" ]; then cp "$FACTS" "$EV/monitor-facts.txt"; fi; log "done"
