#!/bin/sh
# E0-3 run driver.
#
# Starts a NESTED Claude Code session, posts a chosen frame into its inbox socket
# (via the probe plugin's detached poster), lets it respond, then collects the
# fake-brigade NDJSON, the transcript, the poster/hook logs, and before/after
# config hashes. Enforces environment isolation and real-config protection.
#
# Usage:
#   run.sh --variant A|B|C --mode p|interactive [--prompt "..."]
#          [--delay SECONDS] [--timeout SECONDS] [--tag LABEL]
#
# Every nested invocation unsets exactly the eight leaking CLAUDE_* variables and
# KEEPS CLAUDE_CONFIG_DIR so the nested session stays authenticated.
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
PLUGIN="$HERE/plugin"
PROJECT="$HERE/project"
STATE="$PLUGIN/state"

# ---- args ----
VARIANT=""; MODE=""; PROMPT=""; DELAY="2"; TIMEOUT="90"; TAG=""
while [ $# -gt 0 ]; do
  case "$1" in
    --variant) VARIANT="$2"; shift 2;;
    --mode) MODE="$2"; shift 2;;
    --prompt) PROMPT="$2"; shift 2;;
    --delay) DELAY="$2"; shift 2;;
    --timeout) TIMEOUT="$2"; shift 2;;
    --tag) TAG="$2"; shift 2;;
    *) echo "unknown arg: $1" >&2; exit 2;;
  esac
done
[ -n "$VARIANT" ] || { echo "need --variant A|B|C" >&2; exit 2; }
[ -n "$MODE" ] || { echo "need --mode p|interactive" >&2; exit 2; }
[ -n "$TAG" ] || TAG="$(date +%Y%m%d-%H%M%S)-$VARIANT-$MODE"
if [ -z "$PROMPT" ]; then
  PROMPT="Run this exact command with the Bash tool and report its output: sleep 6 && echo e03-ready. After that, follow any further instructions you receive."
fi

RESULTS="$HERE/results/$TAG"
mkdir -p "$RESULTS"

# ---- capture OUTER (harness) messaging identity for the isolation proof ----
OUTER_SOCKET="${CLAUDE_CODE_MESSAGING_SOCKET:-}"
OUTER_TOKEN="${CLAUDE_CODE_MESSAGING_TOKEN:-}"
OUTER_SESSION="${CLAUDE_CODE_SESSION_ID:-}"
OUTER_TOKEN_SHA=""
[ -n "$OUTER_TOKEN" ] && OUTER_TOKEN_SHA="$(printf '%s' "$OUTER_TOKEN" | shasum -a 256 | awk '{print $1}' | cut -c1-12)"

# ---- real-config protection: snapshot + hash BEFORE ----
CFG_SETTINGS="${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json"
CFG_BRIGADE="/Users/rjae/Development/appshapes/brigade/CLAUDE.md"
CFG_GLOBAL="$HOME/.claude/CLAUDE.md"
PROTECTED="$CFG_SETTINGS $CFG_BRIGADE $CFG_GLOBAL"
SNAP="$RESULTS/config-snapshot"; mkdir -p "$SNAP"

hashof() { shasum -a 256 "$1" 2>/dev/null | awk '{print $1}'; }
i=0
for f in $PROTECTED; do
  i=$((i+1))
  cp "$f" "$SNAP/$i.snap" 2>/dev/null
  eval "PRE_$i=\"$(hashof "$f")\""
  eval "PATH_$i=\"$f\""
done
PROT_N=$i

# ---- build the frame -> plugin/state/frame.txt ----
mkdir -p "$STATE"
python3 "$HERE/frame.py" --variant "$VARIANT" --out "$STATE/frame.txt" 2> "$RESULTS/frame.meta"
cp "$STATE/frame.txt" "$RESULTS/frame.txt"

# ---- clear per-run logs ----
: > "$STATE/fake-brigade.ndjson"
: > "$STATE/poster.log"
: > "$STATE/hook-env.log"
: > "$STATE/poster.nohup.log"
: > "$STATE/hook-events.log"
rm -f "$STATE/hook-input.json"

echo "E0-3 run  tag=$TAG  variant=$VARIANT  mode=$MODE  delay=$DELAY  timeout=$TIMEOUT"
echo "outer session=$OUTER_SESSION  outer socket=$OUTER_SOCKET  outer token_sha12=$OUTER_TOKEN_SHA"
echo "config dir=${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
echo

# ---- the eight variables to strip from every nested invocation ----
UNSET="env -u CLAUDE_PID -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_MESSAGING_SOCKET -u CLAUDE_CODE_MESSAGING_TOKEN -u CLAUDE_CODE_ENTRYPOINT -u CLAUDECODE -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_EXECPATH"

# ---- launch the nested session ----
if [ "$MODE" = "p" ]; then
  cd "$PROJECT" || exit 1
  $UNSET \
    BRIGADE_E03_POST_DELAY="$DELAY" \
    claude -p "$PROMPT" \
      --plugin-dir "$PLUGIN" \
      --permission-mode bypassPermissions \
      --output-format stream-json --verbose \
      > "$RESULTS/transcript.jsonl" 2> "$RESULTS/claude.stderr" &
  CPID=$!
  # bound the wait
  waited=0
  while kill -0 "$CPID" 2>/dev/null; do
    sleep 1; waited=$((waited+1))
    [ "$waited" -ge "$TIMEOUT" ] && { echo "timeout: killing nested claude"; kill "$CPID" 2>/dev/null; break; }
  done
  wait "$CPID" 2>/dev/null
  NESTED_EXIT=$?
  echo "nested claude exited: $NESTED_EXIT (after ${waited}s)"
elif [ "$MODE" = "interactive" ]; then
  # Drive an interactive session with expect: answer the trust dialog, send the
  # prompt, wait for the injected frame to arrive and be handled, then exit.
  EXP="$RESULTS/drive.exp"
  cat > "$EXP" <<EXPECT
set timeout $TIMEOUT
log_file -a "$RESULTS/interactive.log"
spawn -noecho env -u CLAUDE_PID -u CLAUDE_CODE_SESSION_ID -u CLAUDE_CODE_MESSAGING_SOCKET -u CLAUDE_CODE_MESSAGING_TOKEN -u CLAUDE_CODE_ENTRYPOINT -u CLAUDECODE -u CLAUDE_CODE_CHILD_SESSION -u CLAUDE_CODE_EXECPATH BRIGADE_E03_POST_DELAY=$DELAY claude --plugin-dir "$PLUGIN" --permission-mode bypassPermissions
# Trust dialog (first run in a new dir): choose "yes, proceed".
expect {
  -re {trust|proceed|Do you trust} { send -- "\r"; }
  timeout {}
}
# Wait for the prompt UI, then type our instruction.
sleep 3
send -- "$PROMPT\r"
# Let the injected frame arrive and be handled.
sleep [expr $TIMEOUT - 15]
send -- "/exit\r"
expect eof
EXPECT
  cd "$PROJECT" || exit 1
  expect -f "$EXP" > "$RESULTS/interactive.raw" 2>&1
  NESTED_EXIT=$?
  echo "interactive expect exited: $NESTED_EXIT"
else
  echo "unknown mode: $MODE" >&2; exit 2
fi

# give a late poster/ack a moment to flush
sleep 1

# ---- collect logs ----
cp "$STATE/fake-brigade.ndjson" "$RESULTS/fake-brigade.ndjson" 2>/dev/null
cp "$STATE/poster.log" "$RESULTS/poster.log" 2>/dev/null
cp "$STATE/hook-env.log" "$RESULTS/hook-env.log" 2>/dev/null
cp "$STATE/poster.nohup.log" "$RESULTS/poster.nohup.log" 2>/dev/null
cp "$STATE/hook-events.log" "$RESULTS/hook-events.log" 2>/dev/null
cp "$STATE/hook-input.json" "$RESULTS/hook-input.json" 2>/dev/null

# ---- config protection: hash AFTER, restore on change ----
echo; echo "=== config protection ==="
CFG_FAIL=0
i=0
while [ $i -lt $PROT_N ]; do
  i=$((i+1))
  # Indirect read of the PATH_<i>/PRE_<i> pairs set above. Assigned via command substitution rather than
  # `eval "pre=..."` so the assignment is visible to shellcheck (SC2154) as well as to the shell.
  f=$(eval "printf '%s' \"\${PATH_$i}\"")
  pre=$(eval "printf '%s' \"\${PRE_$i}\"")
  post="$(hashof "$f")"
  if [ "$pre" = "$post" ]; then
    echo "OK    unchanged: $f"
  else
    echo "FAIL  CHANGED: $f  (pre=$pre post=$post) -> RESTORING from snapshot"
    cp "$SNAP/$i.snap" "$f" && echo "      restored from $SNAP/$i.snap"
    CFG_FAIL=1
  fi
done

# ---- isolation assertion ----
echo; echo "=== environment isolation ==="
ISO_FAIL=0
NESTED_SOCKET="$(python3 -c "import json,sys
s=''
try:
  for l in open('$STATE/hook-env.log'):
    r=json.loads(l); s=r.get('socket','')
except Exception: pass
print(s)")"
NESTED_TOKSHA="$(python3 -c "import json
s=''
try:
  for l in open('$STATE/hook-env.log'):
    import json as j; r=j.loads(l); s=r.get('token_sha12','')
except Exception: pass
print(s)")"
NESTED_SESSION="$(python3 -c "import json
s=''
try:
  for l in open('$STATE/hook-env.log'):
    r=json.loads(l); s=r.get('session','')
except Exception: pass
print(s)")"
echo "nested hook socket   = $NESTED_SOCKET"
echo "nested hook token_sha= $NESTED_TOKSHA"
echo "nested hook session  = $NESTED_SESSION"
if [ -z "$NESTED_SOCKET" ]; then
  echo "WARN  no nested hook env captured (hook may not have fired)"
fi
if [ -n "$OUTER_SOCKET" ] && [ "$NESTED_SOCKET" = "$OUTER_SOCKET" ]; then
  echo "FAIL  nested socket EQUALS outer socket — env leaked!"; ISO_FAIL=1
elif [ -n "$NESTED_SOCKET" ]; then
  echo "OK    nested socket differs from outer socket"
fi
if [ -n "$OUTER_TOKEN_SHA" ] && [ "$NESTED_TOKSHA" = "$OUTER_TOKEN_SHA" ]; then
  echo "FAIL  nested token EQUALS outer token — env leaked!"; ISO_FAIL=1
elif [ -n "$NESTED_TOKSHA" ]; then
  echo "OK    nested token differs from outer token"
fi
if [ -n "$OUTER_SESSION" ] && [ "$NESTED_SESSION" = "$OUTER_SESSION" ]; then
  echo "FAIL  nested session id EQUALS outer session id — env leaked!"; ISO_FAIL=1
fi

# ---- delivery: did the frame reach the nested session, verbatim? ----
# The authoritative record is the on-disk session transcript under the config
# dir; a socket-injected message arrives there verbatim (as a queued_command).
# The `claude -p` stream-json output does NOT echo the injected frame inline in
# its user event, so it is collected for reference but not used for the verbatim
# check.
echo; echo "=== delivery ==="
DELIV_FAIL=1
CFGDIR="${CLAUDE_CONFIG_DIR:-$HOME/.claude}"
DISK_T=""
if [ -n "$NESTED_SESSION" ]; then
  DISK_T="$(find "$CFGDIR/projects" -name "${NESTED_SESSION}.jsonl" 2>/dev/null | head -1)"
fi
if [ -n "$DISK_T" ] && [ -f "$DISK_T" ]; then
  cp "$DISK_T" "$RESULTS/session-transcript.jsonl"
  echo "on-disk transcript: $DISK_T"
fi
# Needle = the longest line of the posted frame that has no double-quote or
# backslash, so a plain grep -F matches even though the on-disk transcript stores
# the frame JSON-escaped (attribute quotes become \"). Present in every variant.
NEEDLE="$(python3 -c "
import sys
best=''
for ln in open('$STATE/frame.txt'):
    ln=ln.rstrip('\n')
    if '\"' in ln or '\\\\' in ln: continue
    if len(ln)>len(best): best=ln
print(best)
")"
if [ -n "$DISK_T" ] && grep -qF "$NEEDLE" "$DISK_T" 2>/dev/null; then
  echo "OK    frame present VERBATIM in on-disk session transcript"
  echo "      needle: $(printf '%s' "$NEEDLE" | cut -c1-72)..."
  DELIV_FAIL=0
elif [ -f "$RESULTS/transcript.jsonl" ] && grep -qF "$NEEDLE" "$RESULTS/transcript.jsonl" 2>/dev/null; then
  echo "OK    frame present VERBATIM in stream-json transcript"
  DELIV_FAIL=0
elif [ -f "$RESULTS/interactive.raw" ] && grep -qF "brigade-message" "$RESULTS/interactive.raw" 2>/dev/null; then
  echo "OK    frame present in interactive capture"
  DELIV_FAIL=0
else
  echo "FAIL  frame text NOT found verbatim in any transcript"
fi

# ---- poster result ----
echo; echo "=== poster ==="
if grep -qF '"event": "posted"' "$STATE/poster.log" 2>/dev/null || grep -qF '"event":"posted"' "$STATE/poster.log" 2>/dev/null; then
  echo "OK    poster reported a successful post"; cat "$STATE/poster.log"
else
  echo "WARN  poster did not report success:"; cat "$STATE/poster.log" 2>/dev/null; cat "$STATE/poster.nohup.log" 2>/dev/null
fi

# ---- fake brigade recorder ----
echo; echo "=== fake brigade recorder ==="
SENDS="$(wc -l < "$STATE/fake-brigade.ndjson" 2>/dev/null | tr -d ' ')"
echo "recorded sends: ${SENDS:-0}"
[ "${SENDS:-0}" -gt 0 ] && cat "$STATE/fake-brigade.ndjson"

# ---- summary ----
echo; echo "=== SUMMARY ($TAG) ==="
echo "config_protected: $([ $CFG_FAIL -eq 0 ] && echo PASS || echo FAIL)"
echo "isolation:        $([ $ISO_FAIL -eq 0 ] && echo PASS || echo FAIL)"
echo "delivery:         $([ $DELIV_FAIL -eq 0 ] && echo PASS || echo FAIL)"
echo "sends_recorded:   ${SENDS:-0}"
echo "results dir:      $RESULTS"

# Exit non-zero on any hard failure (config or isolation), which are the
# security-critical invariants; delivery is reported but not fatal (it is the
# thing E0-3 measures).
[ $CFG_FAIL -eq 0 ] && [ $ISO_FAIL -eq 0 ]
