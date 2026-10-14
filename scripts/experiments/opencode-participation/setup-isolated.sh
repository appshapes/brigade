#!/bin/sh
# Build an isolated opencode 2.x home and service for the probe. Usage: setup-isolated.sh <dir>
# Prints the path of env.sh; source it, then run `opencode run --session <id> "<text>" </dev/null`.
# Needs LM Studio on localhost:1234 with qwen/qwen3-coder-next. Never touches the real service.
set -eu
S=$1; HERE=$(cd "$(dirname "$0")" && pwd)
mkdir -p "$S/config/opencode/plugins" "$S/data" "$S/state" "$S/cache" "$S/work"
cat > "$S/config/opencode/opencode.json" <<'JSON'
{
  "$schema": "https://opencode.ai/config.json",
  "model": "lmstudio/qwen/qwen3-coder-next",
  "provider": { "lmstudio": { "npm": "@ai-sdk/openai-compatible", "name": "LM Studio (local)",
    "options": { "baseURL": "http://localhost:1234/v1", "apiKey": "lm-studio" },
    "models": { "qwen/qwen3-coder-next": { "name": "qwen3-coder-next" } } } },
  "permission": { "bash": "allow", "edit": "allow" }
}
JSON
cp "$HERE/probe-plugin.js" "$S/config/opencode/plugins/probe-plugin.js"
cat > "$S/env.sh" <<EOF2
export XDG_CONFIG_HOME=$S/config XDG_DATA_HOME=$S/data XDG_STATE_HOME=$S/state XDG_CACHE_HOME=$S/cache
export PROBE_LOG=$S/events.jsonl PROBE_TRIGGER=$S/trigger.txt
for v in \$(env | grep -o '^CLAUDE[A-Z_0-9]*'); do unset \$v; done; unset AI_AGENT
EOF2
. "$S/env.sh"
: > "$S/events.jsonl"
opencode service set port 4097
opencode service start
echo "$S/env.sh"
