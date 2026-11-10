#!/bin/bash
# Shared driver functions for the Codex title runs (card 190). Source after setting EV (evidence directory) and
# TS (tmux session name).
ROOT=${CODEX_TEST_ROOT:?set CODEX_TEST_ROOT}
# The root goes into the command lines tmux runs: refuse anything a shell would split or expand.
case "$ROOT" in *[!A-Za-z0-9/._-]*) echo "CODEX_TEST_ROOT may hold only letters, digits and / . _ -" >&2; exit 2 ;; esac
# codex runs with no CODEX_HOME (run-codex.sh), so its home is this one whatever the caller's environment says
CODEX_DIR="$HOME/.codex"
UNSET="(no title set)"
T(){ tmux -L codex190 "$@"; }
utc(){ python3 -I -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]+"Z")'; }
log(){ printf '%s %s\n' "$(utc)" "$*" | tee -a "$EV/driver.log"; }
# pane: the visible screen only, never the scrollback: a prompt answered long ago must not be seen again
pane(){ T capture-pane -p -t "$TS" | sed -e 's/[[:space:]]*$//' | grep -v '^$'; }
# title: what the TUI last set as the terminal title. tmux keeps an OSC 0/2 title as the pane's title; the pane
# starts with the sentinel $UNSET, so a TUI that sets nothing reads as that.
title(){ T display-message -p -t "$TS" '#{pane_title}'; }
note(){ log "title[$1]=$(title)"; }
# composer: true only when the composer is on screen and no modal is. Every line the driver types goes through
# this: the update prompt's Enter runs `brew upgrade --cask codex`.
composer(){ local p; p=$(pane); echo "$p" | grep -q 'for shortcuts' && ! echo "$p" | grep -q 'enter continue\|enter confirm\|esc skip\|esc cancel\|esc close'; }
# launch <workdir> [codex arguments as one shell-quoted string]: starts the TUI in tmux with its raw output piped
# to raw.log; skips the update prompt and the hooks review with Esc (never Enter: nothing is updated, no hook is
# trusted), accepts the trust prompt of the test folder, waits for the composer. Each pass decides from one look
# at the screen. The trust prompt's Enter is the one key sent outside `composer`, and only when a second look,
# just before it, still shows that prompt and no update prompt.
launch(){
  local work="$1" args="${2:-}"
  T new-session -d -s "$TS" -x 200 -y 50 "sleep 1; '$ROOT/run-codex.sh' --cwd '$work' $args; echo EXITED; sleep 3600"
  T select-pane -t "$TS" -T "$UNSET"
  T pipe-pane -t "$TS" -o "cat >> '$EV/raw.log'"
  log "launched TUI args=[$args] cwd=${work#"$ROOT"/}"
  local _ p stable=0
  for _ in $(seq 1 200); do
    p=$(pane)
    if echo "$p" | grep -q 'Update now'; then T send-keys -t "$TS" Escape; log "update prompt skipped (Esc)"; stable=0; sleep 1; continue; fi
    if echo "$p" | grep -q 'Hooks need review'; then T send-keys -t "$TS" Escape; log "hooks review skipped (Esc)"; stable=0; sleep 1; continue; fi
    if echo "$p" | grep -q 'Trust this folder?'; then
      sleep 0.3; p=$(pane)
      if echo "$p" | grep -q 'Trust this folder?' && ! echo "$p" | grep -q 'Update now'; then
        T send-keys -t "$TS" Enter; log "trust prompt accepted (Enter)"
      fi
      stable=0; sleep 1; continue
    fi
    if composer; then stable=$((stable+1)); [ $stable -ge 12 ] && break; fi
    sleep 0.25
  done
  composer || { pane > "$EV/pane-failed.txt"; log "FAILED: no composer; pane saved"; return 1; }
  sleep 1; pane > "$EV/pane-00-start.txt"; log "composer ready"
}
# keys <text>: types one line and Enter, only into the composer
keys(){ composer || { log "REFUSED to type [$1]: the composer is not on screen"; return 1; }; T send-keys -t "$TS" -l "$1"; sleep 1; T send-keys -t "$TS" Enter; }
# quit: /quit, then waits for the TUI to exit before the tmux session is closed
quit(){ keys "/quit" || true; local _; for _ in $(seq 1 40); do pane | grep -q '^EXITED$' && break; sleep 0.25; done; T kill-session -t "$TS" 2>/dev/null; log "TUI closed"; }
# index_line <thread name>: the last entry of Codex's title index with exactly that name (the file Brigade reads)
index_line(){ python3 -I - "$CODEX_DIR/session_index.jsonl" "$1" <<'PY'
import json,sys
last=""
for line in open(sys.argv[1],encoding="utf-8",errors="replace"):
    try: e=json.loads(line)
    except ValueError: continue
    if e.get("thread_name")==sys.argv[2]: last=json.dumps({"id":e.get("id"),"thread_name":e.get("thread_name")})
print(last)
PY
}
# field <name>: one member of the JSON object on stdin, or nothing
field(){ python3 -I -c 'import json,sys; s=sys.stdin.read(); print(json.loads(s).get(sys.argv[1],"") if s.strip() else "")' "$1"; }
# state_name <thread id>: the name column of the thread's row in Codex's state database
state_name(){ sqlite3 -readonly "$CODEX_DIR/state_5.sqlite" "select name from threads where id='$1';" 2>/dev/null; }
# osc_titles: every OSC 0/2 title in raw.log, in order, with the spinner frames and consecutive repeats folded
osc_titles(){ python3 -I - "$EV/raw.log" <<'PY'
import re,sys
b=open(sys.argv[1],'rb').read()
last=None
for m in re.finditer(rb'\x1b\]([02]);([^\x07\x1b]*)(?:\x07|\x1b\\)', b):
    t=re.sub('[⠀-⣿]','<spinner>',m.group(2).decode('utf-8','replace'))
    if t!=last: print('OSC '+m.group(1).decode()+' '+repr(t)); last=t
PY
}
