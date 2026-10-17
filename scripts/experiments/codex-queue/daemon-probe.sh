#!/bin/sh
# Asks the running app-server daemon (through `codex app-server proxy`) which threads are loaded and listed.
# Usage: daemon-probe.sh [cwd-filter]
ROOT=${CODEX_TEST_ROOT:?}
{
  printf '%s\n' '{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"clientInfo":{"name":"brigade_probe","title":"Brigade probe","version":"0.0.0"}}}'
  sleep 1
  printf '%s\n' '{"jsonrpc":"2.0","method":"initialized"}'
  printf '%s\n' '{"jsonrpc":"2.0","id":1,"method":"thread/loaded/list","params":{}}'
  printf '%s\n' '{"jsonrpc":"2.0","id":2,"method":"thread/list","params":{"limit":5}}'
  sleep 2
} | "$ROOT/run-codex.sh" --cwd "$ROOT" app-server proxy 2>&1 | head -c 20000
