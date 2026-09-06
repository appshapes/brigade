#!/bin/sh
# usage: scripts/harness-smoke.sh          (run from the repository root; no arguments)
#        make harness-smoke                (the supported entry point: it builds first and wraps this in $(unclaude))
#
# The headless harness smoke of plan P3-7: one real `claude -p` session, the real plugin, the real hooks, the real
# detached watcher and the fs adapter as the backend, with a SECOND principal driven from this script's own terminal
# environment. It proves the wiring no unit or e2e test can: that Claude Code fires the plugin's hooks, that the
# SessionStart hook registers the session and names its team, that the model reaches `brigade` on the Bash tool's
# PATH, that a message another principal sends is injected MID-TURN into the running session, and that the model
# answers it with `brigade send … --reply-to`.
#
# It needs a logged-in Claude Code and costs model calls, so it never runs in CI. `jq` is required.
#
# EVERYTHING it touches is under one temporary root: its own XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME (so the
# dev-binary pointer, the fs store, the session maps, the watcher pidfile and the binary cache are all throwaway), its
# own project directory, and its own Claude config dir for the second principal's hook. The user's real
# CLAUDE_CONFIG_DIR is kept for the `claude` run alone -- the login lives there -- and the transcript directory that
# run creates under it is removed by absolute path at the end. The user's real ~/.config/brigade dev pointer,
# ~/.local/state/brigade and ~/.local/share/brigade are never read or written.
#
# Where the evidence lives, measured on Claude Code 2.1.259 (docs/experiments/E3-smoke.md):
#   * `SessionStart` is the ONLY hook that reaches `--output-format stream-json` (as system/hook_started and
#     system/hook_response). The transcript records it a second time as an `attachment` of type `hook_success`,
#     with the hook's `statusMessage` in place of its command.
#   * A `UserPromptSubmit` (or `SessionEnd`) hook that exits 0 and prints NOTHING is recorded in no source at all --
#     not the stream, not the transcript, not stderr. Its only observable is what it wrote: the prompt hook is the
#     only writer of `permission_mode` in the by-pid map (Claude Code sends `permission_mode` on UserPromptSubmit and
#     not on SessionStart), so this script reads that map WHILE the session runs, before SessionEnd deletes it.
#   * An injected message is not a stream-json event (6.11). In the transcript it is a `queue-operation`/`enqueue`
#     and an `attachment` of type `queued_command`, whose `origin` is `{kind: "peer", name: "<the variant-C wrapper's
#     from-name>"}` and whose `prompt` carries the whole `<brigade-message …>` frame.
#
# Output: one `ok:`/`FAIL:` line per assertion, then the evidence excerpts the assertions read, then the teardown.
# Exit status is the number of failed assertions (0 = green).
set -eu

# ---------------------------------------------------------------------------------------------------------------
# 0. Constants
# ---------------------------------------------------------------------------------------------------------------
team_name=smoke
alice_profile=alice
bob_profile=bob
alice_label='alice@smoke.invalid'
bob_label='bob@smoke.invalid'
model_body='hello bob, this is the smoke test'
bob_body='mid-turn message from bob to alice'
max_turns=8
send_wait=240   # seconds to wait for the model's `brigade send` to reach bob's inbox
run_wait=600    # seconds the whole `claude -p` run may take

say() { printf '%s\n' "$*"; }
die() { printf 'harness-smoke: %s\n' "$1" >&2; exit 2; }

failures=0
ok() { printf 'ok: %s\n' "$*"; }
bad() { failures=$((failures + 1)); printf 'FAIL: %s\n' "$*"; }
# names <dir>: the file names in dir, space separated (find, not ls: shellcheck SC2012)
names() { find "$1" -maxdepth 1 -type f -exec basename {} \; 2>/dev/null | sort | tr '\n' ' '; }

# ---------------------------------------------------------------------------------------------------------------
# 1. Preconditions
# ---------------------------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P) || die "cannot resolve the repository root"
[ -d "$repo/plugin" ] || die "no plugin/ directory at $repo: run me from the repository"
brigade_bin=$repo/bin/brigade
fs_adapter=$repo/bin/brigade-adapter-fs
[ -x "$brigade_bin" ] || die "missing $brigade_bin: run \`make build\` first (\`make harness-smoke\` does)"
[ -x "$fs_adapter" ] || die "missing $fs_adapter: run \`make build\` first (\`make harness-smoke\` does)"
command -v jq >/dev/null 2>&1 || die "jq is required"
command -v claude >/dev/null 2>&1 || die "claude is required and must be logged in"
# The plugin's bin/ is APPENDED to the Bash tool's PATH (docs/research/plugin-bootstrap-cli.md, finding 1), so any
# other `brigade` earlier on PATH wins and the model would drive a different binary than this script asserts on.
if command -v brigade >/dev/null 2>&1; then
  die "a \`brigade\` is already on PATH ($(command -v brigade)); it would shadow the plugin's and invalidate the smoke"
fi

# The user's real Claude config dir: the login, and where the nested run writes its transcript.
claude_cfg=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
case $claude_cfg in /*) ;; *) die "CLAUDE_CONFIG_DIR must be absolute: '$claude_cfg'" ;; esac
[ -d "$claude_cfg" ] || die "no Claude config dir at $claude_cfg"

# The inherited session environment is stripped BY PREFIX -- every CLAUDE* (there is no underscore, so CLAUDECODE is
# covered too) and every AI_AGENT*, keeping only CLAUDE_CONFIG_DIR. An enumerated list is measurably short (E0-4,
# E0-7). Variable names cannot contain whitespace, so the unquoted expansion below is safe, and it is the only way to
# hand `env` a computed `-u` list from POSIX sh.
strip_args=''
for stripped_name in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p'); do
  if [ "$stripped_name" = CLAUDE_CONFIG_DIR ]; then continue; fi
  strip_args="$strip_args -u $stripped_name"
done

# ---------------------------------------------------------------------------------------------------------------
# 2. The temporary machine
# ---------------------------------------------------------------------------------------------------------------
smoke_root=$(mktemp -d "${TMPDIR:-/tmp}/brigade-smoke-XXXXXX") || die "cannot create a temporary directory"
smoke_root=$(CDPATH='' cd -- "$smoke_root" && pwd -P)
case $smoke_root in */brigade-smoke-*) ;; *) die "refusing to work in an unexpected temp root: $smoke_root" ;; esac

xdg_config=$smoke_root/xdg/config
xdg_state=$smoke_root/xdg/state
xdg_data=$smoke_root/xdg/data
bob_cfg=$smoke_root/claude-config          # the second principal's throwaway Claude config dir (its own registry)
proj=$smoke_root/proj                      # the `claude -p` working directory
bob_cwd=$smoke_root/bob-session            # names bob's session (identity falls back to basename(cwd))
capture=$smoke_root/capture
state_dir=$xdg_state/brigade
store=$state_dir/fs-adapter                # the fs adapter's DEFAULT root: ${BRIGADE_STATE_DIR}/fs-adapter
secret_file=$smoke_root/join.secret
stream=$capture/stream.jsonl
claude_err=$capture/claude.stderr
mkdir -p "$xdg_config/brigade" "$xdg_state" "$xdg_data" "$bob_cfg" "$proj" "$bob_cwd" "$capture"
chmod 700 "$smoke_root"

# The dev-binary pointer, under THIS run's XDG_CONFIG_HOME (the `plugin-dev-pointer` logic honours it). The plugin's
# bootstrap execs it before anything else, so no download is ever attempted and the cold-cache SessionStart branch
# of the bootstrap is never taken.
printf '%s\n' "$brigade_bin" > "$xdg_config/brigade/dev-binary"

adapter_json=$(jq -cn --arg fs "$fs_adapter" '[$fs] | tojson')   # ["/abs/bin/brigade-adapter-fs"] as a JSON STRING
adapter_array=$(printf '%s' "$adapter_json" | jq -r .)           # the same value unquoted, for --adapter

bob_sleeper=''
claude_pid=''
transcript_dir=''

# ---------------------------------------------------------------------------------------------------------------
# 3. Teardown (always)
# ---------------------------------------------------------------------------------------------------------------
# shellcheck disable=SC2329,SC2317  # called from cleanup(), which shellcheck cannot see is run by the EXIT trap
#                                     (0.11 reports the function as never invoked, SC2329; 0.10 reports its body as
#                                     unreachable, SC2317 -- CI runs 0.10, this machine 0.11; both must be clean)
kill_wait() {  # kill_wait <pid> <label>
  _pid=$1; _label=$2
  kill -0 "$_pid" 2>/dev/null || return 0
  kill -TERM "$_pid" 2>/dev/null || true
  _n=0
  while kill -0 "$_pid" 2>/dev/null; do
    _n=$((_n + 1))
    if [ "$_n" -gt 100 ]; then
      kill -KILL "$_pid" 2>/dev/null || true
      say "teardown: $_label ($_pid) survived SIGTERM; SIGKILLed"
      return 0
    fi
    sleep 0.1
  done
  say "teardown: $_label ($_pid) is gone"
}

# shellcheck disable=SC2329,SC2317  # run by the EXIT/HUP/INT/TERM trap below (SC2317 is 0.10's spelling of it)
cleanup() {
  st=$?
  set +e
  [ -n "$claude_pid" ] && kill_wait "$claude_pid" "the claude process"
  # Any watcher this run's session still has, from its own pidfile (SIGTERM is the watcher's clean exit path; a
  # session that ended normally has already removed it).
  if [ -d "$state_dir/watchers" ]; then
    for pf in "$state_dir"/watchers/*.json; do
      [ -f "$pf" ] || continue
      wpid=$(jq -r '.pid // empty' "$pf" 2>/dev/null)
      case $wpid in ''|*[!0-9]*) continue ;; esac
      kill_wait "$wpid" "the watcher from $pf"
    done
  fi
  [ -n "$bob_sleeper" ] && kill_wait "$bob_sleeper" "bob's sleeper"
  if [ -n "$transcript_dir" ] && [ -d "$transcript_dir" ]; then
    say "teardown: removing the transcript directory $transcript_dir"
    rm -rf "$transcript_dir"
  fi
  case $smoke_root in
    */brigade-smoke-*)
      say "teardown: removing the temp root $smoke_root"
      rm -rf "$smoke_root" ;;
    *) say "teardown: NOT removing '$smoke_root' (unexpected name)" ;;
  esac
  exit "$st"
}
trap cleanup EXIT HUP INT TERM

# ---------------------------------------------------------------------------------------------------------------
# 4. Helpers that run a command with the stripped, temporary environment
# ---------------------------------------------------------------------------------------------------------------
# terminal: this script's own shell -- the human's terminal, outside any session. No CLAUDE_PID, so `team`/`profile`
# pass through to the adapter and `send`/`whoami` would refuse (P3-3: they are session-only).
# shellcheck disable=SC2086  # $strip_args is a computed `-u NAME` list; names carry no whitespace (see section 1)
terminal() {
  env $strip_args \
    CLAUDE_CONFIG_DIR="$bob_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    "$@"
}

# bob_session: the same, plus the facts the harness reads inside a session, so `hook session-start` registers bob and
# `send` finds his by-pid map. No CLAUDE_CODE_MESSAGING_SOCKET: no watcher is spawned for bob.
# shellcheck disable=SC2086  # as above
bob_session() {
  env $strip_args \
    CLAUDE_CONFIG_DIR="$bob_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    CLAUDE_PID="$bob_sleeper" \
    CLAUDE_CODE_SESSION_ID="smoke-bob-$bob_sleeper" \
    CLAUDECODE=1 \
    CLAUDE_CODE_ENTRYPOINT=cli \
    CLAUDE_PLUGIN_OPTION_PROFILE="$bob_profile" \
    CLAUDE_PLUGIN_OPTION_ADAPTER_COMMAND="$adapter_array" \
    "$@"
}

# ---------------------------------------------------------------------------------------------------------------
# 5. Onboarding, from the terminal (the sequence a developer runs once)
# ---------------------------------------------------------------------------------------------------------------
say "harness-smoke: temp root $smoke_root"
say "harness-smoke: fs store  $store"

# P7-6: terminal resolution reads the binding's adapter NAME through adapters.json.
printf '{"fs": %s}\n' "$adapter_array" > "$xdg_config/brigade/adapters.json"
chmod 600 "$xdg_config/brigade/adapters.json"
for p in "$alice_profile" "$bob_profile"; do
  terminal "$brigade_bin" profile init --profile "$p" --adapter "$adapter_array" >"$capture/profile-init-$p.json"
done
ok "profiles $alice_profile and $bob_profile bound to the fs adapter (profile init --adapter)"

jq -cn --arg t "$team_name" --arg l "$alice_label" '{team_name:$t,human_label:$l}' |
  terminal "$brigade_bin" team create --profile "$alice_profile" --secret-file "$secret_file" \
    >"$capture/team-create.json"
team_ref=$(jq -r '.result.team_ref' "$capture/team-create.json")
if [ -z "$team_ref" ] || [ "$team_ref" = null ]; then die "team create returned no team_ref"; fi

# The join secret never reaches a variable, a log or this script's stdout: jq reads it straight from the 0600 file
# the adapter wrote and pipes the join document into the adapter's stdin.
jq -Rn --arg l "$bob_label" '{join_secret: input, human_label: $l}' < "$secret_file" |
  terminal "$brigade_bin" team join --profile "$bob_profile" >"$capture/team-join.json"
ok "team \"$team_name\" created by $alice_profile and joined by $bob_profile (team_ref $team_ref)"

# --- P7-6: the project owns the team. Bob's store moves under the derived team key, his binding gains the
# harness-owned backend trio, his session cwd becomes a checkout with .brigade.json, and the pin records the
# consent his `team join` implies. Alice never runs a session here, so her name-keyed store stays as the
# terminal bridge uses it.
smoke_key=$(printf 'fs\nhttp://127.0.0.1:1\n%s' "$team_ref" | shasum -a 256 | cut -c1-32)
bstore=$xdg_config/brigade/teams
mv "$bstore/$bob_profile" "$bstore/$smoke_key"
jq '.adapter="fs" | .url="http://127.0.0.1:1" | .publishable_key="placeholder"' \
  "$bstore/$smoke_key/team.json" > "$bstore/$smoke_key/team.json.new"
mv "$bstore/$smoke_key/team.json.new" "$bstore/$smoke_key/team.json"
chmod 600 "$bstore/$smoke_key/team.json"
mkdir "$bob_cwd/.git"
jq -cn --arg r "$team_ref" --arg t "$team_name" \
  '{version:1,adapter:"fs",url:"http://127.0.0.1:1",publishable_key:"placeholder",team_ref:$r,team_name:$t}' \
  > "$bob_cwd/.brigade.json"
canon_bob=$(cd "$bob_cwd" && pwd -P)
jq -cn --arg d "$canon_bob" --arg r "$team_ref" \
  '{version:1,projects:{($d):{adapter:"fs",url:"http://127.0.0.1:1",publishable_key:"placeholder",team_ref:$r,consented_at:"2026-09-06T00:00:00Z"}}}' \
  > "$xdg_config/brigade/projects.json"
chmod 600 "$xdg_config/brigade/projects.json"
say "harness-smoke: bob's store keyed $smoke_key; checkout pinned at $canon_bob"

# ---------------------------------------------------------------------------------------------------------------
# 6. bob's session, registered through the REAL hook (the shipped path; it puts him in the roster)
# ---------------------------------------------------------------------------------------------------------------
sleep 900 &
bob_sleeper=$!
jq -cn --arg s "smoke-bob-$bob_sleeper" --arg c "$bob_cwd" \
  '{session_id:$s,cwd:$c,hook_event_name:"SessionStart",transcript_path:"/never/read.jsonl",source:"startup"}' |
  bob_session "$brigade_bin" hook session-start >"$capture/bob-session-start.out" 2>"$capture/bob-session-start.err"
bob_map=$state_dir/sessions/by-pid/$bob_sleeper.json
[ -f "$bob_map" ] || die "the hook wrote no by-pid map for bob at $bob_map (stderr: $(cat "$capture/bob-session-start.err"))"
bob_id=$(jq -r '.brigade_session_id' "$bob_map")
bob_name=$(jq -r '.session_name' "$bob_map")
if [ -z "$bob_id" ] || [ "$bob_id" = null ]; then die "bob's by-pid map carries no brigade_session_id"; fi
if [ -f "$state_dir/watchers/$bob_sleeper.json" ]; then
  bad "a watcher was spawned for bob, who has no inbox socket"
else
  ok "bob registered through \`brigade hook session-start\` as \"$bob_name\" ($bob_id); no watcher (no socket)"
fi

# ---------------------------------------------------------------------------------------------------------------
# 7. The headless session
# ---------------------------------------------------------------------------------------------------------------
# P7-6: the headless session attaches through ITS checkout and ITS OWN store (two principals of one team
# cannot share a key-keyed store): alice's credential is copied under the derived key into a session-only
# config dir, the backend trio completed, the fs adapter registered by name, $proj made a checkout with the
# team file, and the pin written for it.
alice_cfg=$smoke_root/cfg-alice-session
mkdir -p "$alice_cfg/teams"
cp -R "$xdg_config/brigade/teams/$alice_profile" "$alice_cfg/teams/$smoke_key"
jq '.adapter="fs" | .url="http://127.0.0.1:1" | .publishable_key="placeholder"' \
  "$alice_cfg/teams/$smoke_key/team.json" > "$alice_cfg/teams/$smoke_key/team.json.new"
mv "$alice_cfg/teams/$smoke_key/team.json.new" "$alice_cfg/teams/$smoke_key/team.json"
chmod 600 "$alice_cfg/teams/$smoke_key/team.json"
printf '{"fs": %s}\n' "$adapter_array" > "$alice_cfg/adapters.json"
chmod 600 "$alice_cfg/adapters.json"
mkdir "$proj/.git"
cp "$bob_cwd/.brigade.json" "$proj/.brigade.json"
canon_proj=$(cd "$proj" && pwd -P)
jq -cn --arg d "$canon_proj" --arg r "$team_ref" \
  '{version:1,projects:{($d):{adapter:"fs",url:"http://127.0.0.1:1",publishable_key:"placeholder",team_ref:$r,consented_at:"2026-09-06T00:00:00Z"}}}' \
  > "$alice_cfg/projects.json"
chmod 600 "$alice_cfg/projects.json"


settings=$(jq -cn --arg cd "$alice_cfg" --argjson ac "$adapter_json" \
  '{pluginConfigs:{"brigade@inline":{options:{config_dir:$cd,adapter_command:$ac}}}}')

prompt="Brigade smoke test. Use ONLY the Bash tool, ONE command per tool call, in this order:
1. brigade sessions
2. brigade send $bob_id --summary \"smoke\" <<'EOF'
$model_body
EOF
3. sleep 20
4. By now a Brigade team message will have arrived in your context. Follow the reply instruction inside it: run
   brigade send <its reply-to-session-id> --summary \"reply\" --reply-to <its message-id> <<'EOF' with a one-line
   body, exactly as the message says.
Then answer with the single word done. Do not use any other tool and do not run any other command."

say "harness-smoke: starting claude -p (max-turns $max_turns)"
run_start=$(date +%s)
# DISABLE_AUTOUPDATER: the native launcher ~/.local/bin/claude is a symlink the auto-updater repoints into
# $XDG_DATA_HOME/claude/versions/, so an update inside this temporary data home leaves the launcher dangling
# when the root is removed -- measured 2026-09-04 16:36 (2.1.260 -> 2.1.261) by P4-4; no session could start until
# the symlink was repointed. The variable is not CLAUDE-prefixed, so the strip above keeps it.
# shellcheck disable=SC2086  # as above; CLAUDE_CONFIG_DIR is deliberately NOT overridden here (the login lives there)
( cd "$proj" && exec env $strip_args \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    DISABLE_AUTOUPDATER=1 \
    claude -p "$prompt" \
      --plugin-dir "$repo/plugin" \
      --settings "$settings" \
      --allowedTools "Bash(brigade:*),Bash(sleep:*),Skill" \
      --output-format stream-json --verbose \
      --max-turns "$max_turns" ) >"$stream" 2>"$claude_err" &
claude_pid=$!

# ---------------------------------------------------------------------------------------------------------------
# 8. bob's mid-turn message, posted as soon as the model's own send lands in bob's inbox
# ---------------------------------------------------------------------------------------------------------------
inbox_bob=$store/teams/$team_ref/inbox/$bob_id
alice_map=''
deadline=$(( $(date +%s) + send_wait ))
while :; do
  if [ -z "$alice_map" ] && [ -d "$state_dir/sessions/by-pid" ]; then
    for mf in "$state_dir"/sessions/by-pid/*.json; do
      [ -f "$mf" ] || continue
      if [ "$(basename "$mf")" = "$bob_sleeper.json" ]; then continue; fi
      alice_map=$mf
    done
  fi
  if [ -d "$inbox_bob" ] && [ -n "$(names "$inbox_bob")" ]; then
    say "harness-smoke: the model's message reached bob's inbox; posting bob's message now"
    break
  fi
  if ! kill -0 "$claude_pid" 2>/dev/null; then
    say "harness-smoke: the claude run ended before the model's send reached bob's inbox"
    break
  fi
  if [ "$(date +%s)" -ge "$deadline" ]; then
    say "harness-smoke: timed out after ${send_wait}s waiting for the model's send; posting bob's message anyway"
    break
  fi
  sleep 1
done

alice_id=''
alice_mode=''
alice_claude_pid=''
watcher_pid=''
if [ -n "$alice_map" ] && [ -f "$alice_map" ]; then
  # Read WHILE the session runs: the SessionEnd hook deletes this file, and `permission_mode` is the only on-disk
  # trace the UserPromptSubmit hook leaves (see the header).
  cp "$alice_map" "$capture/alice-map.json"
  alice_id=$(jq -r '.brigade_session_id // empty' "$capture/alice-map.json")
  alice_mode=$(jq -r '.permission_mode // empty' "$capture/alice-map.json")
  alice_claude_pid=$(basename "$alice_map" .json)
  if [ -f "$state_dir/watchers/$alice_claude_pid.json" ]; then
    cp "$state_dir/watchers/$alice_claude_pid.json" "$capture/alice-pidfile.json"
    watcher_pid=$(jq -r '.pid // empty' "$capture/alice-pidfile.json")
  fi
fi

if [ -z "$alice_id" ]; then
  bad "no by-pid map for the headless session appeared under $state_dir/sessions/by-pid: bob has nowhere to send"
else
  printf '%s\n' "$bob_body" |
    bob_session "$brigade_bin" send "$alice_id" --summary "from bob" --json >"$capture/bob-send.json" 2>"$capture/bob-send.err" ||
    bad "bob's send failed: $(cat "$capture/bob-send.err")"
fi
bob_msg_id=$(jq -r '.result.message_id // empty' "$capture/bob-send.json" 2>/dev/null || true)

# ---------------------------------------------------------------------------------------------------------------
# 9. Wait for the run
# ---------------------------------------------------------------------------------------------------------------
run_deadline=$(( run_start + run_wait ))
while kill -0 "$claude_pid" 2>/dev/null; do
  if [ "$(date +%s)" -ge "$run_deadline" ]; then
    bad "the claude run exceeded ${run_wait}s; terminating it"
    kill -TERM "$claude_pid" 2>/dev/null || true
    break
  fi
  sleep 1
done
claude_exit=0
wait "$claude_pid" 2>/dev/null || claude_exit=$?
claude_pid=''
wall=$(( $(date +%s) - run_start ))
model=$(jq -r 'select(.type=="system" and .subtype=="init") | .model' "$stream" 2>/dev/null | head -1)
socket_path=$(jq -r 'select(.type=="system" and .subtype=="init") | .messaging_socket_path // empty' "$stream" 2>/dev/null | head -1)
native_id=$(jq -r 'select(.type=="system" and .subtype=="init") | .session_id' "$stream" 2>/dev/null | head -1)
say "harness-smoke: claude exited $claude_exit after ${wall}s (model $model, socket ${socket_path:-none})"

# The transcript: located by the native session id, never by guessing how the cwd is mangled into a directory name.
transcript=''
if [ -n "$native_id" ]; then
  transcript=$(find "$claude_cfg/projects" -maxdepth 2 -type f -name "$native_id.jsonl" 2>/dev/null | head -1)
fi
if [ -n "$transcript" ]; then
  transcript_dir=$(dirname "$transcript")
  case $transcript_dir in
    "$claude_cfg"/projects/*brigade-smoke*) ;;
    *) say "harness-smoke: $transcript_dir is not this run's directory; it will NOT be removed"
       transcript_dir='' ;;
  esac
fi

# ---------------------------------------------------------------------------------------------------------------
# 10. Assertions
# ---------------------------------------------------------------------------------------------------------------
say ""
say "--- assertions ---"

# (1) SessionStart: the hook fired, exited 0, and its stdout is the context line naming the team.
start_line=$(jq -r 'select(.type=="system" and .subtype=="hook_response" and .hook_event=="SessionStart" and .exit_code==0) | .stdout' "$stream" 2>/dev/null | head -1)
case $start_line in
  "Brigade: this session is \""*"in team \"$team_name\"; inbound: accept;"*)
    ok "SessionStart hook_response exit 0; context line: $start_line" ;;
  *)
    bad "SessionStart context line missing or wrong: ${start_line:-<none>}" ;;
esac
line_id=$(printf '%s' "$start_line" | sed -n 's/^Brigade: this session is "[^"]*" (\([^)]*\)).*/\1/p')
if [ -n "$line_id" ] && [ -n "$alice_id" ] && [ "$line_id" != "$alice_id" ]; then
  bad "the context line names session $line_id but the by-pid map says $alice_id"
fi

# (2) The session was registered and a LIVE detached watcher served it (the by-pid map and the pidfile, read while
#     the session ran).
if [ -n "$watcher_pid" ]; then
  ok "the SessionStart hook spawned a detached watcher (pid $watcher_pid, pidfile $state_dir/watchers/$alice_claude_pid.json)"
else
  bad "no watcher pidfile for the headless session while it ran"
fi

# (3) UserPromptSubmit: it leaves no record in the stream, the transcript or stderr when it exits 0 silently, so the
#     evidence is the by-pid map's permission_mode -- written by the prompt hook alone -- plus the absence of any
#     failed hook record in the transcript.
if [ -n "$alice_mode" ]; then
  ok "the UserPromptSubmit hook ran and exited 0: it wrote permission_mode=\"$alice_mode\" into the by-pid map"
else
  bad "the by-pid map carries no permission_mode: the UserPromptSubmit hook did not complete its work"
fi
if [ -n "$transcript" ]; then
  hook_fail=$(jq -c 'select(.type=="attachment" and (.attachment.type=="hook_non_blocking_error" or (.attachment.hookEvent != null and .attachment.exitCode != 0)))' "$transcript" 2>/dev/null | head -1)
  if [ -z "$hook_fail" ]; then
    ok "no failed hook record anywhere in the transcript"
  else
    bad "a hook failed: $hook_fail"
  fi
else
  bad "no transcript found for native session $native_id under $claude_cfg/projects"
fi

# (4) the model ran `brigade sessions`
cmds=$capture/tool-commands.txt
jq -r 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use" and .name=="Bash") | .input.command' "$stream" 2>/dev/null >"$cmds" || true
# bash_cmd <prefix> [substring]: true when SOME Bash tool_use command, taken as a WHOLE string, starts with
# <prefix> (and contains <substring> when one is given). A jq predicate, not a grep over $cmds: that file holds
# every LINE of every command, heredoc bodies included, so `grep '^brigade sessions'` would also be satisfied by
# a model that merely quoted the instruction inside a message body and never ran it.
bash_cmd() {
  jq -e --arg a "$1" --arg b "${2:-}" '
    select(.type=="assistant") | .message.content[]?
    | select(.type=="tool_use" and .name=="Bash")
    | .input.command
    | select(startswith($a) and ($b == "" or contains($b)))' "$stream" >/dev/null 2>&1
}
if bash_cmd "brigade sessions"; then
  ok "the model ran \`brigade sessions\` through the Bash tool"
else
  bad "no \`brigade sessions\` Bash tool_use in the stream"
fi

# (5) the model's `brigade send` reached bob's inbox in the fs store
if bash_cmd "brigade send $bob_id"; then
  ok "the model ran \`brigade send $bob_id …\`"
else
  bad "no \`brigade send $bob_id\` Bash tool_use in the stream"
fi
delivered=no
if [ -d "$inbox_bob" ]; then
  for f in "$inbox_bob"/*.json; do
    [ -f "$f" ] || continue
    if jq -e --arg b "$model_body" '.body | contains($b)' "$f" >/dev/null 2>&1; then delivered=yes; fi
  done
fi
if [ "$delivered" = yes ]; then
  ok "the body the model sent is in bob's fs inbox ($inbox_bob)"
else
  bad "the model's body never reached bob's inbox at $inbox_bob"
fi

# (6) bob's message was injected mid-turn, attributed to bob, and acknowledged in the store
frame_line=''
origin=''
if [ -n "$transcript" ]; then
  jq -c 'select(.type=="attachment" and .attachment.type=="queued_command")' "$transcript" >"$capture/queued.jsonl" 2>/dev/null || true
  frame_line=$(jq -r '.attachment.prompt' "$capture/queued.jsonl" 2>/dev/null | grep -m1 '^<brigade-message ' || true)
  origin=$(jq -c '.attachment.origin' "$capture/queued.jsonl" 2>/dev/null | head -1)
fi
if [ -n "$frame_line" ]; then
  case $frame_line in
    *"from-name=\"$bob_name\""*"team=\"$team_name\""*|*"team=\"$team_name\""*"from-name=\"$bob_name\""*)
      ok "bob's frame was injected mid-turn as a queued_command; origin $origin" ;;
    *) bad "the injected frame does not name bob or the team: $frame_line" ;;
  esac
else
  bad "no <brigade-message frame in a queued_command attachment: nothing was injected mid-turn"
fi
frame_msg_id=$(printf '%s' "$frame_line" | sed -n 's/.* message-id="\([^"]*\)".*/\1/p')
if [ -n "$bob_msg_id" ] && [ "$frame_msg_id" = "$bob_msg_id" ]; then
  ok "the injected frame is the message bob sent ($bob_msg_id)"
else
  bad "the injected frame carries message-id '${frame_msg_id:-<none>}'; bob sent '${bob_msg_id:-<none>}'"
fi
if [ -n "$alice_id" ] && [ -n "$bob_msg_id" ] && \
   [ -f "$(find "$store/teams/$team_ref/acked/$alice_id" -name "*.$bob_msg_id.json" 2>/dev/null | head -1)" ]; then
  ok "the watcher acknowledged it: the envelope moved to acked/$alice_id/"
else
  bad "bob's message is not under acked/$alice_id/ in the fs store (the watcher did not acknowledge it)"
fi

# (7) the model replied with --reply-to
if [ -n "$bob_msg_id" ] && bash_cmd "brigade send $bob_id" "--reply-to $bob_msg_id"; then
  ok "the model replied with \`brigade send $bob_id … --reply-to $bob_msg_id\`"
else
  bad "no \`brigade send $bob_id … --reply-to $bob_msg_id\` in the stream"
fi

# (8) no native SendMessage anywhere
sendmsg=$(jq -r 'select(.type=="assistant") | .message.content[]? | select(.type=="tool_use") | .name' "$stream" 2>/dev/null | grep -c '^SendMessage$' || true)
if [ "${sendmsg:-0}" = 0 ]; then
  ok "no SendMessage tool_use anywhere (Brigade sessions are unreachable from the native tool)"
else
  bad "$sendmsg SendMessage tool_use records in the stream"
fi

# (9) every brigade invocation is the bare command: no full path, no shell wrapper
if grep -q -e '/brigade' -e 'sh -c' "$cmds"; then
  bad "a Bash command invoked brigade through a path or a shell: $(grep -m1 -e '/brigade' -e 'sh -c' "$cmds")"
else
  ok "every brigade invocation is the bare \`brigade\` from the Bash tool's PATH"
fi

# (10) the session itself succeeded
if jq -e 'select(.type=="result") | .is_error == false' "$stream" >/dev/null 2>&1; then
  ok "result is_error false (num_turns $(jq -r 'select(.type=="result") | .num_turns' "$stream" | head -1))"
else
  bad "result is_error is not false: $(jq -c 'select(.type=="result") | {subtype,is_error,num_turns}' "$stream" 2>/dev/null | head -1)"
fi

# (11) nothing of this run survives it: the watcher closed its own session and removed its pidfile
left=$(names "$state_dir/watchers")
if [ -z "$left" ]; then
  ok "no watcher pidfile survived the session (the SessionEnd hook and the watcher's exit path cleaned up)"
else
  bad "watcher pidfiles left behind: $left"
fi
if [ -n "$watcher_pid" ] && kill -0 "$watcher_pid" 2>/dev/null; then
  bad "the watcher process $watcher_pid is still alive after the session ended"
elif [ -n "$watcher_pid" ]; then
  ok "the watcher process $watcher_pid is gone"
fi

# ---------------------------------------------------------------------------------------------------------------
# 11. Evidence excerpts (the temp root goes away with this process; these lines are the record)
# ---------------------------------------------------------------------------------------------------------------
say ""
say "--- evidence ---"
say "captures (removed at teardown): $stream, $claude_err, $transcript"
say "model: $model   wall: ${wall}s   claude exit: $claude_exit   turns: $(jq -r 'select(.type=="result") | .num_turns' "$stream" 2>/dev/null | head -1)"
say "stream hook events: $(jq -c 'select(.type=="system" and (.subtype|startswith("hook"))) | {subtype,hook_name,exit_code}' "$stream" 2>/dev/null | tr '\n' ' ')"
say "hook_response(SessionStart): $(jq -c 'select(.type=="system" and .subtype=="hook_response") | {hook_name,hook_event,exit_code,stdout,stderr}' "$stream" 2>/dev/null | head -1)"
if [ -n "$transcript" ]; then
  say "transcript hook attachments: $(jq -c 'select(.type=="attachment" and .attachment.hookEvent != null) | {t:.attachment.type,e:.attachment.hookEvent,x:.attachment.exitCode,c:.attachment.command}' "$transcript" 2>/dev/null | tr '\n' ' ')"
  say "transcript queue operations: $(jq -c 'select(.type=="queue-operation") | {operation,reason,head:(.content//""|.[0:40])}' "$transcript" 2>/dev/null | tr '\n' ' ')"
fi
say "by-pid map (read mid-run): $(jq -c '{brigade_session_id,team_name,profile,session_name,inbound,permission_mode,socket_path,adapter_command}' "$capture/alice-map.json" 2>/dev/null)"
say "watcher pidfile (read mid-run): $(jq -c '{pid,brigade_session_id,socket_path,token_sha256:(.token_sha256[0:12]+"…")}' "$capture/alice-pidfile.json" 2>/dev/null)"
say "Bash tool_use commands:"
sed 's/^/  | /' "$cmds"
say "injected frame tag line: ${frame_line:-<none>}"
say "injected queued_command origin: ${origin:-<none>}"
say "fs store inbox/$bob_id: $(names "$inbox_bob")"
say "fs store acked/$alice_id: $(names "$store/teams/$team_ref/acked/$alice_id")"
say "watcher log tail: $(tail -2 "$state_dir/logs/watcher-$alice_claude_pid.log" 2>/dev/null | tr '\n' ' ')"
say "result: $(jq -c 'select(.type=="result") | {subtype,is_error,num_turns,duration_ms}' "$stream" 2>/dev/null | head -1)"
if [ -s "$claude_err" ]; then say "claude stderr: $(head -c 500 "$claude_err")"; fi

say ""
if [ "$failures" -eq 0 ]; then
  say "harness-smoke: GREEN (all assertions passed)"
else
  say "harness-smoke: RED ($failures assertion(s) failed)"
fi
exit "$failures"
