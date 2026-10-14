#!/bin/sh
# TUI probe: idle wake in the interactive UI, then resume identity. Usage: tui_probe.sh <workdir>
# Prerequisite: rpc_probe.py already built <workdir>/home. Needs tmux.
set -eu
W=$1; H=$W/home; HERE=$(cd "$(dirname "$0")" && pwd); L=$W/tui-events.jsonl; T=$W/tui-trigger.txt
: > "$L"
run() { env -u AI_AGENT PI_CODING_AGENT_DIR="$H" PROBE_LOG="$L" PROBE_TRIGGER="$T" PI_SKIP_VERSION_CHECK=1 pi -e "$HERE/probe-ext.ts" --session-dir "$H/sessions" "$@"; }
tmux kill-session -t pip 2>/dev/null || true
tmux new-session -d -s pip -x 120 -y 40 -c "$W" "$(command -v env) -u AI_AGENT PI_CODING_AGENT_DIR=$H PROBE_LOG=$L PROBE_TRIGGER=$T PI_SKIP_VERSION_CHECK=1 pi -e $HERE/probe-ext.ts --session-dir $H/sessions"
sleep 6
echo "idle Reply with exactly the word PONG-TUI." > "$T"
sleep 25
tmux capture-pane -t pip -p | grep -v '^$' | tail -15
tmux send-keys -t pip C-c C-c; sleep 2; tmux kill-session -t pip 2>/dev/null || true
echo "--- events"; cut -c1-200 "$L"
