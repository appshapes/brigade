#!/bin/sh
# The Mac half of the Windows proof (E12): a fake Claude Code session (a sleeper as CLAUDE_PID, the real hooks)
# in a checkout of the test team, with the outer Claude session's variables stripped by prefix (gates.md).
# WIN133_REPO names the checkout, PROOF_ROOT the scratch directory; both default to .ignored/ paths of this tree.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
repo_root=$(cd "$here/../../.." && pwd)
B=$repo_root/bin/brigade
repo=${WIN133_REPO:-$repo_root/.ignored/win133-repo}
root=${PROOF_ROOT:-$repo_root/.ignored/windows-vm/mac-proof}
mkdir -p "$root"
strip=$(env | sed -nE 's/^((CLAUDE[A-Za-z0-9_]*|AI_AGENT|BRIGADE_[A-Za-z0-9_]*))=.*/-u \1/p' | grep -v 'CLAUDE_CONFIG_DIR' | tr '\n' ' ')
# shellcheck disable=SC1091
if [ -f "$root/ids" ]; then . "$root/ids"; else
  sleep 7200 >/dev/null 2>&1 & spid=$!
  native=$(uuidgen | tr '[:upper:]' '[:lower:]')
  printf 'spid=%s\nnative=%s\n' "$spid" "$native" > "$root/ids"
fi
# shellcheck disable=SC2086
session() { env $strip CLAUDE_PID="$spid" CLAUDE_CODE_SESSION_ID="$native" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli CLAUDE_PLUGIN_ROOT="$repo_root/plugin" "$@"; }
cd "$repo"
case ${1:-start} in
  start)
    printf '{"session_id":"%s","cwd":"%s","hook_event_name":"SessionStart","source":"startup"}' "$native" "$repo" | session "$B" hook session-start
    session "$B" whoami --json | sed -E 's/"token_sha256":"[0-9a-f]+"/"token_sha256":"<sha>"/' ;;
  whoami) session "$B" whoami ;;
  sessions) session "$B" sessions --all ;;
  send) shift; session "$B" send "$@" ;;
  inbox) session "$B" inbox read 2>&1 || true ;;
  watch)
    # the sink watcher for the Mac session, as scripts/proof.sh starts it: the six hook-built variables, detached
    profile=$(session "$B" team status | sed -nE 's/.*"name":"([^"]+)".*/\1/p')
    # shellcheck disable=SC2086
    env $strip HOME="$HOME" BRIGADE_CLAUDE_PID="$spid" BRIGADE_PROFILE="$profile" BRIGADE_CONFIG_DIR="$HOME/.config/brigade" \
      BRIGADE_STATE_DIR="$HOME/.local/state/brigade" BRIGADE_ADAPTER_COMMAND='[]' BRIGADE_TEAM_INBOUND=accept BRIGADE_LOG_LEVEL=debug \
      CLAUDE_CODE_MESSAGING_TOKEN=sentinel-token-not-real \
      "$B" watch --sink "$root/sink.ndjson" --log-level debug </dev/null >"$root/watch.out" 2>"$root/watch.err" &
    echo "watcher pid $!"; sleep 4; tail -2 "$HOME/.local/state/brigade/logs/watcher-$spid.log" ;;
  sink) cat "$root/sink.ndjson" 2>/dev/null || echo "(no sink yet)" ;;
  end) printf '{"session_id":"%s","cwd":"%s","hook_event_name":"SessionEnd"}' "$native" "$repo" | session "$B" hook session-end; kill "$spid" 2>/dev/null || true; rm -f "$root/ids" ;;
esac
