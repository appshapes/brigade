#!/bin/sh
# Run by the MODEL from its shell: records environment NAMES and reach facts of the model's shell (never values).
FACTS=${1:?facts file}
{
  echo "at=$(date -u +%FT%TZ) pid=$$ ppid=$PPID cwd=$(pwd) uid=$(id -u)"
  echo "env_names=$(env | cut -d= -f1 | sort | tr '\n' ',')"
  echo "codex_thread_id_present=$([ -n "$CODEX_THREAD_ID" ] && echo yes || echo no)"
  echo "codex_session_id_present=$([ -n "$CODEX_SESSION_ID" ] && echo yes || echo no)"
  echo "codex_sandbox=$CODEX_SANDBOX network_disabled=$CODEX_SANDBOX_NETWORK_DISABLED"
  echo "ps_visible=$(ps -e 2>/dev/null | wc -l | tr -d ' ')"
  echo "home_write=$( (echo t > "$HOME/.brigade-codex-probe-$$" 2>/dev/null && rm -f "$HOME/.brigade-codex-probe-$$" && echo yes) || echo no)"
  echo "tmp_write=$( (echo t > /tmp/.brigade-codex-probe-$$ 2>/dev/null && rm -f /tmp/.brigade-codex-probe-$$ && echo yes) || echo no)"
  echo "https_connect=$( (python3 -c 'import socket; s=socket.create_connection(("wmgtaraqmoufmrnyojzf.supabase.co",443),timeout=4); print("ok")' 2>&1 | tail -1) )"
  echo "ppid_cmd=$(ps -o command= -p $PPID 2>/dev/null | cut -c1-100)"
} > "$FACTS" 2>&1
echo "facts recorded"
