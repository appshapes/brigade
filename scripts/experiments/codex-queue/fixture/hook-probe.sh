#!/bin/sh
# Project-level hook probe for Codex: records the stdin payload field names, env NAMES and timing to a private store,
# then (SessionStart only) leaves a detached child running the way Brigade's watcher spawn does (new session, stdio to a log).
STORE=${1:?store directory}
IN=$(cat)
EV=$(printf '%s' "$IN" | python3 -I -c 'import json,sys; d=json.load(sys.stdin); print(d.get("hook_event_name","?"))' 2>/dev/null || echo unknown)
{
  echo "at=$(python3 -c 'import datetime;print(datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.%f")[:-3]+"Z")')"
  echo "event=$EV pid=$$ ppid=$PPID shell=$0 cwd=$(pwd)"
  printf '%s' "$IN" | python3 -I -c 'import json,sys; d=json.load(sys.stdin); print("stdin_fields=" + ",".join(f"{k}:{type(v).__name__}" for k,v in sorted(d.items()))); print("source_or_reason=" + str(d.get("source", d.get("reason","")))); print("transcript_path_null=" + str(d.get("transcript_path") is None)); print("prompt_len=" + str(len(d.get("prompt") or ""))); print("prompt_head=" + (d.get("prompt") or "")[:40].replace("\n"," "))'
  echo "env_names=$(env | cut -d= -f1 | sort | tr '\n' ',')"
  echo "codex_thread_id_eq_stdin=$(printf '%s' "$IN" | python3 -I -c 'import json,sys,os; d=json.load(sys.stdin); print(os.environ.get("CODEX_THREAD_ID")==d.get("session_id"), bool(os.environ.get("CODEX_THREAD_ID")))')"
  echo "ppid_cmd=$(ps -o command= -p $PPID 2>/dev/null | cut -c1-120)"
} >> "$STORE/events-$EV.log" 2>&1
if [ "$EV" = "SessionStart" ]; then
  python3 -I -c '
import os,sys,subprocess
log=open(sys.argv[1],"ab")
p=subprocess.Popen(["/bin/sh","-c","while :; do date -u +%FT%TZ; sleep 5; done"],stdin=subprocess.DEVNULL,stdout=log,stderr=log,start_new_session=True)
open(sys.argv[2],"w").write(str(p.pid))
' "$STORE/child.log" "$STORE/child.pid"
  echo "child_pid=$(cat "$STORE/child.pid")" >> "$STORE/events-$EV.log"
fi
echo "Brigade hook probe ran for $EV"
exit 0
