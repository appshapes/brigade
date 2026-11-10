#!/bin/sh
# usage: scripts/experiments/E16-held-decision-live.sh      (run from the repository root after `make build`)
#
# E16 part 2 (card 236; report docs/experiments/E16-held-decision.md): does a real model, told by the shipped
# words alone, ask its own user in the conversation and record a decision Brigade holds? One real interactive
# Claude Code session on the freshly built binary and the repository's plugin tree, driven by E10's expect driver
# (scripts/experiments/E10-doing-triggers/acceptance.exp), with the fs adapter as the backend, a state folder in a
# throwaway checkout, the member subscribed, and a session with the Slack gateway's harness on the same store,
# whose inbox is what "Slack" would have received. One run, no string tuning:
#   LEG 1  one prompt whose turn runs longer than a minute and ends on a question for the user. PASS when a
#          decision for the session's own member is in the state folder, held (held_until set, nothing sent), and
#          the gateway's inbox is empty: the model asked in the conversation and recorded it, and nothing left.
#   LEG 2  the user answers in the conversation. PASS when the watcher ends the hold at that prompt
#          (hold_ended "prompt"), the gateway's inbox is still empty, and the model closes the decision.
# If LEG 1 fails the run stops there and reports what the model did.
#
# It needs a logged-in Claude Code, `expect`, `jq` and `git`, costs model calls (about six minutes), and never runs
# in CI. The rig is E10's: one temp root holding its own XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME (the
# dev-binary pointer, the fs store, the session map, the watcher), its own git-initialised project directory, and
# the user's REAL CLAUDE_CONFIG_DIR kept for the `claude` run alone (the login lives there); the transcript
# directory that run creates under it is removed by absolute path at the end. The inherited session environment is
# stripped by prefix, and every brigade bootstrap on PATH is dropped so the plugin's own is the one the model runs.
# The marketplace-installed plugin is switched off for the run through --settings, and the member's label is set
# through the plugin's `label` option, so nothing of the user's own account is written into the rig.
#
# Output: .ignored/card-236/e16-live-<stamp>/ — tty.log, summary.txt, the decision files, the gateway's inbox, the
# by-pid map, the watcher's log and the Bash commands the model ran. One `ok:`/`FAIL:` line per assertion; exit
# status is the number of failed assertions, 64 when the rig itself fails.
# shellcheck disable=SC2015  # every `A && ok || bad` below: ok never fails, so bad runs only when A does not
set -eu
umask 077

team_name=held-live
alice='alice@held.invalid'
# shellcheck disable=SC2016  # the backquotes are the prompt's own, typed to the model as they stand
# The command is a slow build's stand-in: Claude Code 2.1.321 refuses a bare foreground `sleep 75` (measured in
# this script's first run, whose turn then ended in 4 s and opened no end-of-turn gate).
prompt_1=${E16_PROMPT_1:-'Run this one command and wait for it to finish, it takes 75 seconds: `python3 -c "import time; time.sleep(75); print(1)"`. After that I need to choose the name of a new notes file in this folder: ask me, as your reply, whether it should be called notes-a.md or notes-b.md, and create nothing until I have answered.'}
prompt_2=${E16_PROMPT_2:-'notes-b.md'}
start_wait=120   # seconds for the SessionStart hook to register the interactive session
leg1_wait=360    # seconds leg 1 may take: the 75 s command, the question, the end-of-turn ask, the command
leg2_wait=240    # seconds leg 2 may take: the watcher's next check (20 s) and the model's close

say() {
  printf '%s\n' "$*"
  if [ -n "${summary:-}" ]; then printf '%s\n' "$*" >>"$summary"; fi
}
die() { printf 'E16 live: %s\n' "$1" >&2; exit 64; }
failures=0
ok() { say "ok: $*"; }
bad() { failures=$((failures + 1)); say "FAIL: $*"; }
now() { date +%s; }

# --- 1. preconditions -----------------------------------------------------------------------------------------------
here=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd -P) || die "cannot resolve my own directory"
repo=$(CDPATH='' cd -- "$here/../.." && pwd -P) || die "cannot resolve the repository root"
driver=$here/E10-doing-triggers/acceptance.exp
brigade_bin=$repo/bin/brigade
fs_adapter=$repo/bin/brigade-adapter-fs
[ -x "$brigade_bin" ] || die "missing $brigade_bin: run make build"
[ -x "$fs_adapter" ] || die "missing $fs_adapter: run make build"
[ -f "$driver" ] || die "missing $driver"
for tool in jq expect git claude; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done

# Every brigade bootstrap on PATH goes: the plugin's bin/ is appended to the Bash tool's PATH, so any other
# `brigade` earlier on it would be the one the model runs.
scrubbed=''
old_ifs=$IFS; IFS=:
for entry in $PATH; do
  case $entry in */plugins/cache/*|*/plugin/bin) continue ;; esac
  scrubbed=${scrubbed:+$scrubbed:}$entry
done
IFS=$old_ifs
PATH=$scrubbed
export PATH
if command -v brigade >/dev/null 2>&1; then die "a \`brigade\` is still on PATH ($(command -v brigade)); it would shadow the plugin's"; fi

claude_cfg=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
case $claude_cfg in /*) ;; *) die "CLAUDE_CONFIG_DIR must be absolute: '$claude_cfg'" ;; esac
[ -d "$claude_cfg" ] || die "no Claude config dir at $claude_cfg"

# The inherited session environment is stripped by prefix, keeping only CLAUDE_CONFIG_DIR (gates.md).
strip_args=''
for stripped_name in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(BRIGADE_[0-9A-Za-z_]*\)=.*/\1/p'); do
  if [ "$stripped_name" = CLAUDE_CONFIG_DIR ]; then continue; fi
  strip_args="$strip_args -u $stripped_name"
done

# --- 2. the output directory and the temporary machine ---------------------------------------------------------------
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=$repo/.ignored/card-236/e16-live-$stamp
mkdir -p "$out"
summary=$out/summary.txt
: > "$summary"
rig=$(mktemp -d "${TMPDIR:-/tmp}/brigade-e16l-XXXXXX") || die "cannot create a temporary directory"
rig=$(CDPATH='' cd -- "$rig" && pwd -P)
case $rig in */brigade-e16l-*) ;; *) die "refusing to work in an unexpected temp root: $rig" ;; esac
xdg_config=$rig/xdg/config; xdg_state=$rig/xdg/state; xdg_data=$rig/xdg/data
proj=$rig/proj; ctl=$rig/ctl; state_dir=$xdg_state/brigade
secret_file=$rig/join.secret
mkdir -p "$xdg_config/brigade" "$xdg_state" "$xdg_data" "$proj" "$ctl"
# The dev-binary pointer: the plugin's bootstrap execs it, so the freshly built binary is the one under test.
printf '%s\n' "$brigade_bin" > "$xdg_config/brigade/dev-binary"
printf '{"fs": ["%s"]}\n' "$fs_adapter" > "$xdg_config/brigade/adapters.json"
expect_pid=''
transcript_dir=''

# shellcheck disable=SC2329,SC2317  # called from cleanup(), which the EXIT trap runs
kill_wait() {
  kill -0 "$1" 2>/dev/null || return 0
  kill -TERM "$1" 2>/dev/null || true
  _n=0
  while kill -0 "$1" 2>/dev/null; do
    _n=$((_n + 1))
    if [ "$_n" -gt 100 ]; then kill -KILL "$1" 2>/dev/null || true; return 0; fi
    sleep 0.1
  done
}
# shellcheck disable=SC2329,SC2317  # run by the EXIT/HUP/INT/TERM trap
cleanup() {
  st=$?
  set +e
  [ -n "$expect_pid" ] && kill_wait "$expect_pid"
  if [ -d "$state_dir/watchers" ]; then
    for pf in "$state_dir"/watchers/*.json; do
      [ -f "$pf" ] || continue
      wpid=$(jq -r '.pid // empty' "$pf" 2>/dev/null)
      case $wpid in ''|*[!0-9]*) continue ;; esac
      kill_wait "$wpid"
    done
  fi
  if [ -n "$transcript_dir" ] && [ -d "$transcript_dir" ]; then
    say "teardown: removing the transcript directory $transcript_dir"
    rm -rf "$transcript_dir"
  fi
  case $rig in */brigade-e16l-*) rm -rf "$rig"; say "teardown: removed the temp root $rig" ;; esac
  say "evidence: $out"
  exit "$st"
}
trap cleanup EXIT HUP INT TERM

# terminal: a human's shell outside any session, on the rig's directories and a throwaway Claude config.
# shellcheck disable=SC2086  # $strip_args is a computed `-u NAME` list; names carry no whitespace
terminal() {
  env $strip_args CLAUDE_CONFIG_DIR="$rig/claude-none" XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" "$@"
}

# --- 3. the project, the team, the subscription, the gateway --------------------------------------------------------
say "E16 live: temp root $rig"
git -C "$proj" init -q -b master
git -C "$proj" config user.email "$alice"
git -C "$proj" config user.name "Proof"
printf '# Held proof\n\nA throwaway folder for one question.\n' > "$proj/README.md"
(cd "$proj" && terminal "$brigade_bin" team create --adapter fs --url http://127.0.0.1:1 --key placeholder \
  --name "$team_name" --label "$alice" --secret-file "$secret_file") >"$out/team-create.out" 2>"$out/team-create.err" ||
  die "team create failed: $(cat "$out/team-create.err")"
team_ref=$(jq -r '.team_ref // empty' "$proj/.brigade.json")
[ -n "$team_ref" ] || die "team create wrote no team_ref"
jq '. + {"sync": {"folders": [".brigade"], "state": ".brigade/state"}}' "$proj/.brigade.json" > "$proj/.brigade.json.tmp" && mv "$proj/.brigade.json.tmp" "$proj/.brigade.json"
printf '.brigade\n' > "$proj/.gitignore"
git -C "$proj" add -A && git -C "$proj" commit -q -m "0: the throwaway checkout"
folder=$proj/.brigade/state
mkdir -p "$folder"
# The member's subscription, as their own session's `brigade report subscribe --recipients "@alice"` writes it:
# the end-of-turn ask names a decision only where someone is subscribed.
printf '{"version":2,"subscribers":[{"label":"%s","recipients":["@alice"]}]}\n' "$alice" > "$folder/reports.json"
ok "team \"$team_name\" on the fs adapter, the checkout's state folder, and $alice subscribed (the default wait, ten minutes)"

key=''
for d in "$xdg_config"/brigade/teams/*/; do [ -f "$d/team.json" ] && key=$(basename "$d"); done
[ -n "$key" ] || die "no team binding under $xdg_config/brigade/teams"
adapter() { env BRIGADE_PROFILE="$key" BRIGADE_CONFIG_DIR="$xdg_config/brigade" BRIGADE_STATE_DIR="$state_dir" "$fs_adapter" "$@"; }
printf '{"harness":"gateway-slack","harness_version":"0.1.0","session_name":"slack-gateway","activity":"idle","inbound":"accept","lease_seconds":600}' |
  adapter session register >"$out/gateway-register.out" 2>"$out/gateway-register.err" || die "the gateway session did not register: $(cat "$out/gateway-register.err")"
gw=$(jq -r '.result.session_id // empty' "$out/gateway-register.out")
[ -n "$gw" ] || die "no gateway session id"
# inbox_count <file>: how many decision asks the gateway session holds. The member is subscribed, so the watcher
# also produces their digest, which goes to the same gateway and is not counted.
inbox_count() {
  adapter message receive --session "$gw" --limit 50 </dev/null >"$1" 2>"$1.err" || true
  jq -r '[.result.messages[] | select(.body | contains("Brigade digest:") | not)] | length' "$1" 2>/dev/null || printf '?'
}
ok "a gateway-slack session is on the roster"

# --- 4. the interactive session ---------------------------------------------------------------------------------------
printf '%s\n' "$prompt_1" > "$ctl/prompt-1.txt"
printf '%s\n' "$prompt_2" > "$ctl/prompt-2.txt"
cp "$ctl/prompt-1.txt" "$ctl/prompt-2.txt" "$out/"
settings=$(jq -cn --arg label "$alice" '{enabledPlugins: {"brigade@brigade": false}, pluginConfigs: {"brigade@inline": {options: {label: $label}}}}')
say "E16 live: starting the interactive claude session (bypassPermissions, --plugin-dir $repo/plugin)"
run_start=$(now)
# shellcheck disable=SC2086
( cd "$proj" && exec env $strip_args XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" DISABLE_AUTOUPDATER=1 \
    expect -f "$driver" "$out/tty.log" "$ctl" \
      claude --plugin-dir "$repo/plugin" --permission-mode bypassPermissions --settings "$settings" ) \
  >"$out/expect.out" 2>"$out/expect.err" &
expect_pid=$!

map=''
deadline=$(( $(now) + start_wait ))
while :; do
  for mf in "$state_dir"/sessions/by-pid/*.json; do
    [ -f "$mf" ] || continue
    case $(basename "$mf") in *.start.json) continue ;; esac
    map=$mf
  done
  [ -n "$map" ] && break
  kill -0 "$expect_pid" 2>/dev/null || break
  [ "$(now)" -ge "$deadline" ] && break
  sleep 1
done
if [ -z "$map" ]; then
  bad "no by-pid map for the interactive session within ${start_wait}s: the SessionStart hook did not register it"
  : > "$ctl/exit"; wait "$expect_pid" || true; expect_pid=''
  exit "$failures"
fi
cp "$map" "$out/bypid-map.json"
sid=$(jq -r '.brigade_session_id // empty' "$map")
transcript=$(jq -r '.transcript_path // empty' "$map")
# The transcript directory this run makes under the real Claude config dir goes with the rig, whatever ends the run.
case $(dirname "$transcript") in "$claude_cfg"/projects/*brigade-e16l-*) transcript_dir=$(dirname "$transcript") ;; esac
ok "the interactive session registered as $sid (doing_mode \"$(jq -r '.doing_mode // empty' "$map")\", state folder \"$(jq -r '.state_folder // empty' "$map")\", Claude Code $(jq -r '.harness_version // empty' "$map")) after $(( $(now) - run_start ))s"
[ "$(jq -r '.state_folder // empty' "$map")" = "$folder" ] || bad "the map names another state folder than $folder"
: > "$ctl/go-1"

# held_decision: the id of a decision for alice that is held (held_until set, never sent); fails with none yet.
held_decision() {
  for df in "$folder"/decisions/*.json; do
    [ -f "$df" ] || continue
    if jq -e --arg a "$alice" '((.to // "") | ascii_downcase) == $a and (.held_until // "") != "" and (.sent // "") == ""' "$df" >/dev/null 2>&1; then
      basename "$df" .json; return 0
    fi
  done
  return 1
}
# model_commands: the Bash commands the model ran so far, from the transcript.
model_commands() {
  [ -f "$transcript" ] || return 0
  jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use" and .name == "Bash") | .input.command' "$transcript" 2>/dev/null || true
}

# --- 5. LEG 1: a long turn that ends on a question for the user -------------------------------------------------------
deadline=$(( $(now) + 150 ))
while [ ! -f "$ctl/typed-1" ] && [ "$(now)" -lt "$deadline" ] && kill -0 "$expect_pid" 2>/dev/null; do sleep 1; done
[ -f "$ctl/typed-1" ] || die "the driver never typed the first prompt"
t1=$(now)
id=''
deadline=$(( t1 + leg1_wait ))
while [ "$(now)" -lt "$deadline" ] && kill -0 "$expect_pid" 2>/dev/null; do
  if id=$(held_decision); then break; fi
  id=''
  sleep 2
done
model_commands > "$out/commands-leg1.txt"
cp "$folder"/decisions/*.json "$out/" 2>/dev/null || true
if [ -n "$id" ]; then
  ok "LEG 1: $(( $(now) - t1 ))s after the prompt, the model had recorded decision $id for its own user, and Brigade holds it: $(jq -c '{to, question, options, held_until, sent}' "$folder/decisions/$id.json")"
  [ "$(inbox_count "$out/inbox-leg1.json")" = 0 ] && ok "LEG 1: nothing reached the gateway" || bad "LEG 1: the gateway holds $(inbox_count "$out/inbox-leg1b.json") messages"
  grep -q 'brigade decision' "$out/commands-leg1.txt" && ok "LEG 1: the model ran: $(grep 'brigade decision' "$out/commands-leg1.txt" | head -1 | cut -c1-200)" || bad "LEG 1: no brigade decision among the model's commands"
else
  bad "LEG 1: no held decision for $alice within ${leg1_wait}s; the model ran: $(tr '\n' ';' < "$out/commands-leg1.txt" | cut -c1-600); decisions: $(cat "$folder"/decisions/*.json 2>/dev/null | head -c 600)"
  cp "$state_dir"/logs/*.log "$out/" 2>/dev/null || true
  : > "$ctl/exit"; wait "$expect_pid" || true; expect_pid=''
  if [ -n "$transcript" ]; then cp "$transcript" "$out/transcript.jsonl" 2>/dev/null || true; fi
  exit "$failures"
fi
sleep 20   # the turn the end-of-turn ask continued is over (a heuristic, as E10's)

# --- 6. LEG 2: the user answers in the conversation -------------------------------------------------------------------
: > "$ctl/go-2"
deadline=$(( $(now) + 60 ))
while [ ! -f "$ctl/typed-2" ] && [ "$(now)" -lt "$deadline" ] && kill -0 "$expect_pid" 2>/dev/null; do sleep 1; done
t2=$(now)
ended=''; closed=''
deadline=$(( t2 + leg2_wait ))
while [ "$(now)" -lt "$deadline" ] && kill -0 "$expect_pid" 2>/dev/null; do
  ended=$(jq -r '.hold_ended // empty' "$folder/decisions/$id.json" 2>/dev/null || true)
  closed=$(jq -r '.closed_at // empty' "$folder/decisions/$id.json" 2>/dev/null || true)
  if [ -n "$closed" ] && { [ -n "$ended" ] || [ "$(( $(now) - t2 ))" -gt 45 ]; }; then break; fi
  sleep 2
done
cp "$folder/decisions/$id.json" "$out/decision-after-leg2.json" 2>/dev/null || true
model_commands > "$out/commands-leg2.txt"
if [ "$ended" = prompt ]; then
  ok "LEG 2: the watcher ended the hold at the user's prompt (hold_ended \"prompt\")"
elif [ -n "$closed" ]; then
  ok "LEG 2: the model closed the decision before the watcher's next check, which ends the hold as well (hold_ended \"$ended\")"
else
  bad "LEG 2: the hold did not end: $(cat "$folder/decisions/$id.json")"
fi
[ -n "$closed" ] && ok "LEG 2: the model closed the decision after the answer: $(jq -c '{outcome, created_at, closed_at}' "$folder/decisions/$id.json")" || bad "LEG 2: the model did not close the decision within ${leg2_wait}s"
[ "$(jq -r '.sent // empty' "$folder/decisions/$id.json")" = "" ] && [ "$(inbox_count "$out/inbox-leg2.json")" = 0 ] && ok "LEG 2: nothing was sent, and the gateway's inbox is empty" || bad "LEG 2: something was sent: $(cat "$folder/decisions/$id.json")"
[ -f "$proj/notes-b.md" ] && say "note: the model created notes-b.md after the answer" || say "note: no notes-b.md in the project at the end"

# --- 7. the end ---------------------------------------------------------------------------------------------------------
cp "$state_dir"/logs/*.log "$out/" 2>/dev/null || true
: > "$ctl/exit"
wait "$expect_pid" || true
expect_pid=''
if [ -n "$transcript" ] && [ -f "$transcript" ]; then
  jq -c 'select(.type == "attachment" or .type == "system") | {type, subtype: (.subtype // .attachment.type // null), text: ((.attachment.content // .content // "") | tostring | .[0:400])}' "$transcript" > "$out/hook-lines.jsonl" 2>/dev/null || true
fi
say "failures: $failures"
exit "$failures"
