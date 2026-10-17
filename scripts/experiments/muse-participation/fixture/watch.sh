#!/bin/sh
# Monitor-command fixture for Arm 2: tails a trigger file (one event per appended line) and, once at start,
# records environment NAMES and sandbox facts of the monitor's shell to a facts file (never values).
# Usage: watch.sh <trigger-file> <facts-file>
TRIGGER="$1"; FACTS="$2"
{
  echo "started_at=$(date -u +%FT%TZ)"
  echo "pid=$$ ppid=$PPID"
  echo "uid=$(id -u) user=$(id -un 2>/dev/null)"
  echo "env_names=$(env | cut -d= -f1 | sort | tr '\n' ',')"
  echo "muse_session_id_present=$([ -n "$MUSE_SESSION_ID" ] && echo yes || echo no)"
  echo "muse_thread_id_present=$([ -n "$MUSE_THREAD_ID" ] && echo yes || echo no)"
  echo "cwd=$(pwd)"
  echo "ps_visible=$(ps -e 2>/dev/null | wc -l | tr -d ' ')"
  echo "home_readable=$([ -r "$HOME" ] && echo yes || echo no)"
  echo "outside_write=$( (echo t > "$HOME/.brigade-muse-probe-$$" 2>/dev/null && rm -f "$HOME/.brigade-muse-probe-$$" && echo yes) || echo no)"
  echo "tmp_write=$( (echo t > /tmp/.brigade-muse-probe-$$ 2>/dev/null && rm -f /tmp/.brigade-muse-probe-$$ && echo yes) || echo no)"
  echo "loopback_connect_54321=$( (python3 -c 'import socket,sys; s=socket.socket(); s.settimeout(1); r=s.connect_ex(("127.0.0.1",54321)); print(r)' 2>/dev/null) || echo err)"
  echo "https_connect=$( (python3 -c 'import socket; s=socket.create_connection(("wmgtaraqmoufmrnyojzf.supabase.co",443),timeout=4); print("ok")' 2>&1 | tail -1) )"
} > "$FACTS" 2>&1
exec tail -n 0 -F "$TRIGGER"
