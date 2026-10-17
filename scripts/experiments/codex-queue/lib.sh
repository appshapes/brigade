#!/bin/bash
# Shared driver functions for the Codex queue arms. Source after setting EV (evidence dir) and TS (tmux session).
ROOT=${CODEX_TEST_ROOT:?set CODEX_TEST_ROOT}
T(){ tmux -L codex107 "$@"; }
now(){ python3 -c 'import time;print(time.time())'; }
utc(){ python3 -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]+"Z")'; }
log(){ printf '%s %s\n' "$(utc)" "$*" | tee -a "$EV/driver.log"; }
pane(){ T capture-pane -p -t "$TS" -S -400 | sed -e 's/[[:space:]]*$//' | grep -v '^$'; }
day(){ date -u +%Y/%m/%d; }
# launch <workdir> [codex flags...]: starts the TUI in tmux, accepts the trust prompt if shown, waits for the composer
launch(){
  local work="$1"; shift
  mkdir -p "$work"; ( cd "$work" && [ -d .git ] || { git init -q && echo "workspace $(basename "$work")" > README.md && git add -A && git -c user.email=t@t -c user.name=t commit -q -m init; } )
  T new-session -d -s "$TS" -x 200 -y 50 "$ROOT/run-codex.sh --cwd $work $*; echo EXITED; sleep 3600"
  log "launched TUI flags=[$*] cwd=$work"
  # the trust prompt can appear after the composer in daemon mode: watch 20 s, answer it whenever shown, settle 4 s
  local i stable=0; for i in $(seq 1 80); do
    if pane | grep -q 'Trust this folder?'; then T send-keys -t "$TS" Enter; log "trust prompt accepted (Enter) at ${i}x0.25s"; stable=0; sleep 1; continue; fi
    if pane | grep -q 'Ask Codex to do anything'; then stable=$((stable+1)); [ $stable -ge 16 ] && break; fi; sleep 0.25; done
  sleep 2; pane > "$EV/pane-00-start.txt"; log "composer ready; pane saved"
}
# find_sid <workdir>: the thread id of the newest thread whose cwd is the workdir (materialised at the first prompt)
find_sid(){ sqlite3 -readonly "$HOME/.codex/state_5.sqlite" "select id from threads where cwd='$1' order by created_at desc limit 1;" 2>/dev/null; }
rollout_of(){ ls "$HOME"/.codex/sessions/*/*/*/rollout-*-"$1".jsonl 2>/dev/null | head -1; }
arm_prompt(){
  local p="You are a message receiver. Every message that arrives after this one is a team message. Reply to each with exactly: RECEIVED <the message text> and nothing else, then stop. Never run any command, never read or write a file, never ask a question. Reply now with exactly the word READY."
  T send-keys -t "$TS" -l "$p"; sleep 1; T send-keys -t "$TS" Enter; log "prompt submitted (arming)"
  local i ready=0; for i in $(seq 1 600); do if pane | grep -q -E '^\S*\s*READY$'; then ready=1; break; fi; sleep 0.5; done
  log "ready=$ready after ${i}x0.5s"; pane > "$EV/pane-01-ready.txt"
}
start_poller(){ ( while :; do printf '%s %s\n' "$(python3 -c 'import time;print(time.time())')" "$(sqlite3 -readonly "$HOME/.codex/queue_1.sqlite" "select count(*) from queued_items where thread_id='$SID';" 2>/dev/null || echo err)"; sleep 0.05; done ) >> "$EV/queue.log" 2>&1 & echo $! > "$EV/queue-poller.pid"; }
stop_poller(){ [ -f "$EV/queue-poller.pid" ] && kill "$(cat "$EV/queue-poller.pid")" 2>/dev/null; }
# send <label> <text> [wait-seconds]: codex queue to $SID, then wait for RECEIVED <nonce> in the pane
send(){
  local label="$1" text="$2" wait="${3:-180}" nonce; nonce=$(echo "$text" | grep -o -E '[0-9a-f]{12}' | head -1)
  local t0 out rc t1; t0=$(now); out=$("$ROOT/run-codex.sh" --cwd "$WORK" queue --thread "$SID" --message "$text" 2>&1); rc=$?; t1=$(now)
  log "sent: $label $(echo "$text" | cut -c1-60) rc=$rc cmd_s=$(echo "$t1 - $t0" | bc) out=$(echo "$out" | tr '\n' ' ' | cut -c1-160)"
  local i woke=0; for i in $(seq 1 $((wait*4))); do if pane | grep -q "RECEIVED.*$nonce"; then woke=1; break; fi; sleep 0.25; done
  log "$label woke=$woke latency_to_text_s=$(echo "$(now) - $t0" | bc)"; pane > "$EV/pane-$label.txt"
}
nonce(){ od -An -N6 -tx1 /dev/urandom | tr -d ' \n'; }
turns(){ grep -c '"type":"task_started"' "$(rollout_of "$SID")" 2>/dev/null || echo 0; }
finish(){ stop_poller; cp "$(rollout_of "$SID")" "$EV/rollout.jsonl" 2>/dev/null; shasum -a 256 "$EV/rollout.jsonl" > "$EV/rollout.jsonl.sha256"; log "evidence saved"; }
daemon_facts(){ { echo "socket: $(ls -la "$HOME/.codex/app-server-control/" 2>&1 | tail -2 | tr '\n' ' ')"; echo "processes:"; ps -axo pid,ppid,etime,command | grep -i "[c]odex" | grep -v -E "zsh|tmux|sqlite|grep" | cut -c1-150; } | tee -a "$EV/daemon-facts.txt"; }
