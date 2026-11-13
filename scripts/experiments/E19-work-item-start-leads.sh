#!/bin/sh
# usage: scripts/experiments/E19-work-item-start-leads.sh      (run from the repository root after `make build`)
#        E19_LEGS="solo ask" scripts/experiments/E19-work-item-start-leads.sh   (a subset, in the order given)
#
# The live proof of work item 303: `brigade:work-item-start` names the session after the work item and leads the
# user's other sessions as followers. Real Claude Code sessions with the plugin of THIS checkout and the binary
# `make build` wrote, in a throwaway project whose team file turns the board on, on the fs adapter (a local
# directory, no backend): never a hosted team and never a real board. Report: docs/experiments/E19-work-item-start-leads.md.
#
#   solo  `/brigade:work-item-start 1`, one headless session and nobody else named. It runs as it did before the
#         change (read, take, do, comment, move to Done) and also names itself: `brigade whoami`, then
#         `brigade rename`, which prints `recorded: 1-<slug>`. It reads no roster and sends nothing. The session
#         is allowed no `brigade` command of its own, so every one it runs is granted by the skill's allowed-tools.
#   ask   `/brigade:work-item-start 2 nobody-here <bob's session>`, one headless session whose name already
#         begins with `2-`. It keeps that name (no `brigade rename`), reads the roster, and finds no follower:
#         `nobody-here` is no live session, and the other is a session of ANOTHER member who carries the same
#         label as the user. It says so, asks, and sends nothing.
#   team  `/brigade:work-item-start 3 helper-a <helper-b's SESSION cell>`, three sessions held open (stdin a
#         FIFO, as E17's): the leader and helper-a in one working tree, helper-b in a second clone of the same
#         origin. Each follower gets a first message of its own with its part; helper-a commits nothing and the
#         leader commits in the shared tree; no follower touches the work item; the leader's comment names both
#         parts and the work item ends in Done.
#   live  `/brigade:work-item-start 4` typed into an INTERACTIVE session (in tmux), then a second prompt: the
#         name `brigade rename` recorded is applied by the prompt hook, and the roster shows it. A headless run
#         exits before any next prompt, so this is the one leg that can see the applied name. Its work item
#         changes no file, so nothing it needs waits at a permission prompt nobody is there to answer. Skipped,
#         and said so, where `tmux` is not on PATH.
#
# The four work items are added up front by an observer, a session of the same member driven by the real
# `brigade hook` subcommands with a sleeper as CLAUDE_PID, which also reads the board and the roster after each
# leg. A second member, bob, is driven the same way from a config directory of his own (E17's rig).
#
# It needs a logged-in Claude Code, `jq`, `git` and `mkfifo`, costs model calls (about half an hour), and never
# runs in CI. Everything Brigade touches is under one temporary root (its own XDG_CONFIG_HOME, XDG_STATE_HOME and
# XDG_DATA_HOME, its own origin repository and two clones of it), removed at the end; the user's real
# CLAUDE_CONFIG_DIR is kept for the `claude` runs alone (the login lives there), the Brigade plugin installed
# there is switched off for these runs, and the transcripts they write under it are removed by name. The dev-binary
# pointer written is the temporary root's own: the machine's is never touched, so `make plugin-dev` elsewhere is
# not disturbed. The interactive leg answers Claude Code's folder-trust question for the temporary project.
#
# Output: one `ok:`/`FAIL:` line per assertion, then the teardown. The evidence (each session's stream, the
# commands it ran, the messages the leader sent, the observer's readings) is kept under
# .ignored/work-item-303/e19/<UTC stamp>/. Exit status is the number of failed assertions, 64 when the rig itself
# fails. E19_KEEP=1 keeps the temporary root.
# shellcheck disable=SC2015  # every `A && ok || bad` below: ok never fails, so bad runs only when A does not
set -eu
umask 077

alice='alice@lead.invalid'
team_name=lead-proof
legs=${E19_LEGS:-solo ask team live}

title_solo="Say the project's hello in a HELLO.md at the top"
name_solo='1-say-the-projects-hello-in-a-hello'
title_ask='Write a TODO.md with three next steps'
kept_name='2-my-own-name-for-it'
title_team='Document the two commands and answer the first questions'
name_team='3-document-the-two-commands-and'
title_live='Name the two commands the README documents'
name_live='4-name-the-two-commands-the-readme'

follower_prompt='Another session of mine is about to lead a work item and will send you your part of it over Brigade. When its message arrives, do that part as it says and report back to it. Until then there is nothing to do: reply with the single word ready.'

run_wait=480     # seconds one headless run may take
start_wait=120   # seconds for the SessionStart hook to register a session
turn_wait=240    # seconds a short turn may take
team_wait=1800   # seconds the three sessions may take to bring the work item to Done
stall_wait=300   # seconds without a tool call in any of the three before the leg is taken to be waiting on its user
settle_wait=60   # seconds without a new tool call before a held session is taken to be idle
exit_wait=60     # seconds a session may take to end after its stdin closes
roster_wait=150  # seconds for an applied name to reach the roster

say() {
  printf '%s\n' "$*"
  if [ -n "${summary:-}" ]; then printf '%s\n' "$*" >>"$summary"; fi
}
die() { printf 'E19: %s\n' "$1" >&2; exit 64; }
failures=0
ok() { say "ok: $*"; }
bad() { failures=$((failures + 1)); say "FAIL: $*"; }
now() { date +%s; }
wants() { case " $legs " in *" $1 "*) return 0 ;; *) return 1 ;; esac; }

# ---------------------------------------------------------------------------------------------------------------
# 1. Preconditions
# ---------------------------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P) || die "cannot resolve the repository root"
[ -d "$repo/plugin/skills/work-item-start" ] || die "no plugin/skills/work-item-start at $repo"
brigade=$repo/bin/brigade
fs_adapter=$repo/bin/brigade-adapter-fs
[ -x "$brigade" ] || die "missing $brigade: run \`make build\` first"
[ -x "$fs_adapter" ] || die "missing $fs_adapter: run \`make build\` first"
for tool in jq git claude mkfifo; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
done
for leg in $legs; do
  case $leg in solo|ask|team|live) ;; *) die "unknown leg '$leg'" ;; esac
done

# Every brigade bootstrap on PATH goes: the plugin's bin/ is APPENDED to the Bash tool's PATH, so any other
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
if command -v brigade >/dev/null 2>&1; then
  die "a \`brigade\` is still on PATH ($(command -v brigade)); it would shadow the plugin's"
fi

claude_cfg=${CLAUDE_CONFIG_DIR:-$HOME/.claude}
case $claude_cfg in /*) ;; *) die "CLAUDE_CONFIG_DIR must be absolute: '$claude_cfg'" ;; esac
[ -d "$claude_cfg" ] || die "no Claude config dir at $claude_cfg"

# The inherited session environment is stripped BY PREFIX: every CLAUDE* (no underscore, so CLAUDECODE too),
# every AI_AGENT* and every BRIGADE_*, keeping only CLAUDE_CONFIG_DIR (gates.md; E0-4, E0-7).
strip_args=''
for stripped_name in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(BRIGADE_[0-9A-Za-z_]*\)=.*/\1/p'); do
  if [ "$stripped_name" = CLAUDE_CONFIG_DIR ]; then continue; fi
  strip_args="$strip_args -u $stripped_name"
done

# ---------------------------------------------------------------------------------------------------------------
# 2. The output directory and the temporary machine
# ---------------------------------------------------------------------------------------------------------------
stamp=$(date -u +%Y%m%dT%H%M%SZ)
out=$repo/.ignored/work-item-303/e19/$stamp
mkdir -p "$out"
summary=$out/summary.txt
: > "$summary"

rig=$(mktemp -d "${TMPDIR:-/tmp}/brigade-lead-XXXXXX") || die "cannot create a temporary directory"
rig=$(CDPATH='' cd -- "$rig" && pwd -P)
case $rig in */brigade-lead-*) ;; *) die "refusing to work in an unexpected temp root: $rig" ;; esac
rig_name=$(basename "$rig")
chmod 700 "$rig"

xdg_config=$rig/xdg/config
xdg_state=$rig/xdg/state
xdg_data=$rig/xdg/data
origin=$rig/lanternfish.git       # the project's origin: a bare repository both clones push to
proj=$rig/proj                    # the leader's working tree, and helper-a's: a clone of the origin
proj2=$rig/proj-second            # helper-b's working tree: a second clone of the origin
observer_cfg=$rig/claude-observer # the observer's throwaway Claude config dir (never the user's)
plugin_root=$rig/plugin           # the observer's plugin root: bin/brigade is the built binary
bob_cwd=$rig/helper-c             # bob's cwd; his session is named after it
bob_cfg=$rig/cfg-bob              # bob's Brigade config dir (two principals of one team cannot share one)
bob_claude_cfg=$rig/claude-bob    # bob's throwaway Claude config dir
state_dir=$xdg_state/brigade
secret_file=$rig/join.secret
tmux_sock=e19-$$
mkdir -p "$xdg_config/brigade" "$xdg_state" "$xdg_data" "$observer_cfg" "$plugin_root/bin" "$bob_cwd" "$bob_cfg" "$bob_claude_cfg"
ln -s "$brigade" "$plugin_root/bin/brigade"
cp "$repo/plugin/bin/VERSION" "$plugin_root/bin/VERSION"

printf '%s\n' "$brigade" > "$xdg_config/brigade/dev-binary"
printf '{"fs": ["%s"]}\n' "$fs_adapter" > "$xdg_config/brigade/adapters.json"
cp "$xdg_config/brigade/adapters.json" "$bob_cfg/adapters.json"

sleeper=''
bob_sleeper=''
visitor=''
claude_pid=''
held_pids=''
known_maps=''
tmux_up=no
open_fds=''

# ---------------------------------------------------------------------------------------------------------------
# 3. Teardown (always)
# ---------------------------------------------------------------------------------------------------------------
# shellcheck disable=SC2329,SC2317  # called from cleanup(), which the EXIT trap runs
kill_wait() {  # kill_wait <pid> <label>
  _pid=$1; _label=$2
  kill -0 "$_pid" 2>/dev/null || return 0
  kill -TERM "$_pid" 2>/dev/null || true
  _n=0
  while kill -0 "$_pid" 2>/dev/null; do
    _n=$((_n + 1))
    if [ "$_n" -gt 150 ]; then
      kill -KILL "$_pid" 2>/dev/null || true
      say "teardown: $_label ($_pid) survived SIGTERM; SIGKILLed"
      return 0
    fi
    sleep 0.1
  done
  say "teardown: $_label ($_pid) is gone"
}

# shellcheck disable=SC2329,SC2317  # run by the EXIT/HUP/INT/TERM trap below
cleanup() {
  st=$?
  set +e
  if [ "$tmux_up" = yes ]; then
    tmux -L "$tmux_sock" kill-server 2>/dev/null
    say "teardown: the interactive session's tmux server is stopped"
  fi
  [ -n "$claude_pid" ] && kill_wait "$claude_pid" "a claude session"
  for _hp in $held_pids; do kill_wait "$_hp" "a held claude session"; done
  # A watcher that is told to stop detaches from the sync engine, and the last one to go stops it.
  if [ -d "$state_dir/watchers" ]; then
    for pf in "$state_dir"/watchers/*.json; do
      [ -f "$pf" ] || continue
      wpid=$(jq -r '.pid // empty' "$pf" 2>/dev/null)
      case $wpid in ''|*[!0-9]*) continue ;; esac
      kill_wait "$wpid" "the watcher from $pf"
    done
  fi
  if [ -f "$state_dir/sync/syncthing/daemon.pid" ]; then
    dpid=$(cut -d' ' -f1 "$state_dir/sync/syncthing/daemon.pid" 2>/dev/null)
    case $dpid in ''|*[!0-9]*) ;; *) kill_wait "$dpid" "the rig's sync engine" ;; esac
  fi
  [ -n "$sleeper" ] && kill_wait "$sleeper" "the observer's sleeper"
  [ -n "$bob_sleeper" ] && kill_wait "$bob_sleeper" "bob's sleeper"
  [ -n "$visitor" ] && kill_wait "$visitor" "the second clone's sleeper"
  # The transcripts the runs wrote under the user's real Claude config dir: only a directory under its projects/
  # whose name carries this rig's own name is removed.
  for td in "$claude_cfg"/projects/*"$rig_name"*; do
    [ -d "$td" ] || continue
    say "teardown: removing the transcript directory $td"
    rm -rf "$td"
  done
  if [ -z "${E19_KEEP:-}" ]; then
    case $rig in
      */brigade-lead-*) say "teardown: removing the temp root $rig"; rm -rf "$rig" ;;
      *) say "teardown: NOT removing '$rig' (unexpected name)" ;;
    esac
  else
    say "teardown: kept $rig"
  fi
  say "evidence: $out"
  exit "$st"
}
trap cleanup EXIT HUP INT TERM

# ---------------------------------------------------------------------------------------------------------------
# 4. Helpers that run a command with the stripped, temporary environment
# ---------------------------------------------------------------------------------------------------------------
# terminal: a human's terminal outside any session, on the rig's XDG dirs.
# shellcheck disable=SC2086  # $strip_args is a computed `-u NAME` list; names carry no whitespace (section 1)
terminal() {
  env $strip_args CLAUDE_CONFIG_DIR="$observer_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" "$@"
}
# observer: the facts the harness reads inside a session, for the observer's hooks and its Bash-tool commands.
# shellcheck disable=SC2086  # as above
observer() {
  (cd "$proj" && env $strip_args CLAUDE_CONFIG_DIR="$observer_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    CLAUDE_PID="$sleeper" CLAUDE_CODE_SESSION_ID="lead-observer-$sleeper" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli \
    CLAUDE_PLUGIN_ROOT="$plugin_root" "$@")
}
# bob_session: the same facts for bob, whose Brigade config dir is his own (the config_dir option, which only
# the hook reads).
# shellcheck disable=SC2086  # as above
bob_session() {
  (cd "$bob_cwd" && env $strip_args CLAUDE_CONFIG_DIR="$bob_claude_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    CLAUDE_PID="$bob_sleeper" CLAUDE_CODE_SESSION_ID="lead-bob-$bob_sleeper" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli \
    CLAUDE_PLUGIN_ROOT="$plugin_root" CLAUDE_PLUGIN_OPTION_CONFIG_DIR="$bob_cfg" "$@")
}
bob_start() {  # bob_start <capture-stem>: fire bob's SessionStart hook from his cwd
  jq -cn --arg s "lead-bob-$bob_sleeper" --arg c "$bob_cwd" \
    '{session_id:$s,cwd:$c,hook_event_name:"SessionStart",transcript_path:"/never/read.jsonl",source:"startup"}' |
    bob_session "$brigade" hook session-start >"$out/$1.out" 2>"$out/$1.err"
}
# board <args…>: one `brigade work-item … --json` as the observer; prints the envelope's result.
board() { observer "$brigade" work-item "$@" --json | jq -c '.result'; }
# roster: the observer's `brigade sessions --json`, the envelope's result.
roster() { observer "$brigade" sessions --json | jq -c '.result'; }
# roster_name <session id>: the name the roster shows for that session.
roster_name() { roster | jq -r --arg id "$1" '[.sessions[] | select(.session_id == $id) | .session_name] | first // ""'; }
# add_item <title> <description>: the observer adds one work item.
add_item() { printf '%s\n\n%s\n' "$1" "$2" | observer "$brigade" work-item add >>"$out/observer-add.out" || die "the observer could not add \"$1\""; }

# session <leg> <name> <allowed tools> <max turns> <prompt>: one headless Claude Code session in the project,
# given one prompt, with this checkout's plugin and the installed Brigade plugin switched off.
# shellcheck disable=SC2086  # as above
session() {
  _leg=$1
  ( cd "$proj" && exec env $strip_args \
      XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" DISABLE_AUTOUPDATER=1 \
      claude -p "$5" -n "$2" --plugin-dir "$repo/plugin" --settings '{"enabledPlugins":{"brigade@brigade":false}}' \
        --permission-mode default --allowedTools "$3" \
        --output-format stream-json --verbose --max-turns "$4" \
  ) >"$out/$_leg.stream.jsonl" 2>"$out/$_leg.claude.err" </dev/null &
  claude_pid=$!
  _deadline=$(( $(now) + run_wait ))
  while kill -0 "$claude_pid" 2>/dev/null; do
    if [ "$(now)" -ge "$_deadline" ]; then
      kill -TERM "$claude_pid" 2>/dev/null || true
      say "note: $_leg did not end within ${run_wait}s; sent SIGTERM"
      break
    fi
    sleep 1
  done
  wait "$claude_pid" 2>/dev/null || true
  claude_pid=''
}

# hold <fd> <name> <cwd> <allowed tools> <max turns>: a real session whose stdin is held open on that descriptor
# (E17's shape: a FIFO opened read-write, pumped by `cat` into an anonymous pipe, because a session whose stdin
# IS the FIFO does not exit on its EOF). Every child closes all three descriptors the driver may hold.
# shellcheck disable=SC2086  # as above
hold() {
  _fd=$1; _name=$2
  _fifo=$rig/$_name.fifo
  rm -f "$_fifo"
  mkfifo "$_fifo" || die "cannot create the stdin FIFO at $_fifo"
  eval "exec $_fd<>\"\$_fifo\""
  open_fds="$open_fds $_fd"
  ( exec cat <"$_fifo" ) 7>&- 8>&- 9>&- | ( cd "$3" && exec env $strip_args \
      XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" DISABLE_AUTOUPDATER=1 \
      claude -p -n "$_name" --plugin-dir "$repo/plugin" --settings '{"enabledPlugins":{"brigade@brigade":false}}' \
        --permission-mode default --allowedTools "$4" \
        --input-format stream-json --output-format stream-json --verbose --max-turns "$5" \
  ) 7>&- 8>&- 9>&- >"$out/$_name.stream.jsonl" 2>"$out/$_name.claude.err" &
  held_pids="$held_pids $!"
}
# tell <fd> <prompt>: the user types one prompt into the session held on that descriptor.
tell() { jq -cn --arg p "$2" '{type:"user",message:{role:"user",content:$p}}' >&"$1"; }
# release: close every held session's stdin and wait for them to end.
release() {
  for _fd in $open_fds; do eval "exec $_fd>&-"; done
  open_fds=''
  _deadline=$(( $(now) + exit_wait ))
  for _hp in $held_pids; do
    while kill -0 "$_hp" 2>/dev/null; do
      if [ "$(now)" -ge "$_deadline" ]; then
        kill -TERM "$_hp" 2>/dev/null || true
        say "note: a held session ($_hp) did not end ${exit_wait}s after its stdin closed; sent SIGTERM"
        break
      fi
      sleep 1
    done
    wait "$_hp" 2>/dev/null || true
  done
  held_pids=''
}
# new_map: wait for a by-pid map that was not there before (the session just started), and leave its path in
# $map_file. Never called in a command substitution: the list of known maps has to outlive the call.
new_map() {
  map_file=''
  _deadline=$(( $(now) + start_wait ))
  while :; do
    for mf in "$state_dir"/sessions/by-pid/*.json; do
      [ -f "$mf" ] || continue
      case $(basename "$mf") in *.start.json) continue ;; esac
      case " $known_maps " in *" $mf "*) continue ;; esac
      known_maps="$known_maps $mf"
      map_file=$mf
      return 0
    done
    [ "$(now)" -ge "$_deadline" ] && return 1
    sleep 1
  done
}
# note_maps: every by-pid map there is now is known, so the next new one is the next session's.
note_maps() {
  for mf in "$state_dir"/sessions/by-pid/*.json; do
    [ -f "$mf" ] || continue
    case " $known_maps " in *" $mf "*) ;; *) known_maps="$known_maps $mf" ;; esac
  done
}
# results <stem>: how many turns the stream says have ended.
results() { grep -c '"type":"result"' "$out/$1.stream.jsonl" 2>/dev/null || true; }
# wait_turns <stem> <n> <seconds>: until the stream has n ended turns.
wait_turns() {
  _deadline=$(( $(now) + $3 ))
  while [ "$(results "$1")" -lt "$2" ]; do
    [ "$(now)" -ge "$_deadline" ] && return 1
    sleep 1
  done
  return 0
}
# commands <stem>: one line per Bash tool call the model made, the command with its line breaks folded.
commands() {
  jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use" and .name == "Bash") | .input.command | gsub("\n"; " ⏎ ")' \
    "$out/$1.stream.jsonl" 2>/dev/null || true
}
# tool_count <stem>: how many tool calls the stream holds.
tool_count() {
  jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use") | .name' "$out/$1.stream.jsonl" 2>/dev/null | wc -l | tr -d ' '
}
# answer <stem>: the session's final answer (the last turn's).
answer() { jq -r 'select(.type == "result") | .result // empty' "$out/$1.stream.jsonl" 2>/dev/null | tail -n 200 || true; }
# denials <stem>: how many tool calls the permission rules denied, over every turn.
denials() { jq -s '[.[] | select(.type == "result") | (.permission_denials // []) | length] | add // 0' "$out/$1.stream.jsonl" 2>/dev/null || printf '?\n'; }
# sends <stem>: every `brigade send` the session ran, one JSON string per line (the command with its body).
sends() {
  jq -c 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use" and .name == "Bash") | .input.command | select(test("^brigade send"))' \
    "$out/$1.stream.jsonl" 2>/dev/null || true
}
# sends_to <stem> <cell>: those whose first line names the session that ends in <cell>.
sends_to() { sends "$1" | jq -c --arg c "$2" 'select((split("\n")[0]) | contains($c))'; }

# ---------------------------------------------------------------------------------------------------------------
# 5. The origin, the two clones, the team with a board, the observer, bob, the four work items
# ---------------------------------------------------------------------------------------------------------------
say "E19: temp root $rig"
say "E19: evidence  $out"
say "E19: claude $(claude --version 2>/dev/null | head -1); brigade $("$brigade" version 2>/dev/null | head -1); legs: $legs"

git init -q --bare -b main "$origin"
git clone -q "$origin" "$proj" 2>/dev/null
clone_setup() {  # the clone's own identity, so a commit needs nothing from the machine's git configuration
  git -C "$1" config user.name 'Alice'
  git -C "$1" config user.email "$alice"
  git -C "$1" config commit.gpgsign false
  git -C "$1" config pull.rebase false
}
clone_setup "$proj"
git -C "$proj" checkout -q -b main 2>/dev/null || true
cat > "$proj/README.md" <<'EOF'
# Lanternfish

Lanternfish is a small command-line tool that turns a repository's `CHANGELOG.md` into a set of static HTML
release pages. It reads the Keep-a-Changelog headings, groups the entries by version and by kind, and writes one
page per release plus an index with an RSS feed.

- `lanternfish build` reads `CHANGELOG.md` in the current directory and writes `site/`.
- `lanternfish check` reports headings that do not follow the convention, without writing anything.
EOF
(cd "$proj" && terminal "$brigade" team create --adapter fs --url http://127.0.0.1:1 --key placeholder \
  --name "$team_name" --label "$alice" --secret-file "$secret_file") >"$out/team-create.out" 2>"$out/team-create.err" ||
  die "team create failed: $(cat "$out/team-create.err")"
jq '. + {sync: {board: true}}' "$proj/.brigade.json" > "$rig/team-file.json" && cp "$rig/team-file.json" "$proj/.brigade.json"
chmod 644 "$proj/.brigade.json"
jq -e '.sync.board == true' "$proj/.brigade.json" >/dev/null || die "the team file does not turn the board on"
git -C "$proj" add README.md .brigade.json
git -C "$proj" commit -q -m 'Lanternfish, and its Brigade team'
git -C "$proj" push -q origin main 2>/dev/null || die "the first push to the rig's origin failed"
git clone -q "$origin" "$proj2" 2>/dev/null
clone_setup "$proj2"
# The second clone joins as a second checkout of a team this machine holds does: from inside a session there
# (`/brigade:join` with no path), with no secret. The session is a sleeper's, gone once the checkout is joined.
sleep 3600 &
visitor=$!
# shellcheck disable=SC2086  # as above
visitor_session() {
  (cd "$proj2" && env $strip_args CLAUDE_CONFIG_DIR="$observer_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    CLAUDE_PID="$visitor" CLAUDE_CODE_SESSION_ID="lead-visitor-$visitor" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli \
    CLAUDE_PLUGIN_ROOT="$plugin_root" "$@")
}
jq -cn --arg s "lead-visitor-$visitor" --arg c "$proj2" \
  '{session_id:$s,cwd:$c,hook_event_name:"SessionStart",transcript_path:"/never/read.jsonl",source:"startup"}' |
  visitor_session "$brigade" hook session-start >"$out/second-start.out" 2>"$out/second-start.err" || true
visitor_session "$brigade" team join </dev/null >"$out/second-join.out" 2>"$out/second-join.err" ||
  die "the second clone's join failed: $(cat "$out/second-join.err")"
kill "$visitor" 2>/dev/null || true
wait "$visitor" 2>/dev/null || true
visitor=''
ok "team \"$team_name\" created by $alice with a board; its project has an origin and two clones, and the second clone joined with no secret: $(tail -1 "$out/second-join.out")"

sleep 3600 &
sleeper=$!
jq -cn --arg s "lead-observer-$sleeper" --arg c "$proj" \
  '{session_id:$s,cwd:$c,hook_event_name:"SessionStart",transcript_path:"/never/read.jsonl",source:"startup"}' |
  observer "$brigade" hook session-start >"$out/observer-start.out" 2>"$out/observer-start.err"
observer_map=$state_dir/sessions/by-pid/$sleeper.json
[ -f "$observer_map" ] || die "the hook wrote no by-pid map for the observer (stderr: $(cat "$out/observer-start.err"))"
board list > "$out/board-0.json" || die "the observer cannot read the board"

# bob: a second member, who chose the SAME label as alice. A label is text anyone can copy; his principal is his own.
sleep 3600 &
bob_sleeper=$!
git -C "$bob_cwd" init -q
cp "$proj/.brigade.json" "$bob_cwd/.brigade.json"
bob_start bob-start-1 || true
bob_session "$brigade" team join --secret-file "$secret_file" --label "$alice" </dev/null \
  >"$out/bob-join.out" 2>"$out/bob-join.err" || die "bob's join failed: $(cat "$out/bob-join.err")"
bob_start bob-start-2
bob_map=$state_dir/sessions/by-pid/$bob_sleeper.json
[ -f "$bob_map" ] || die "the hook wrote no by-pid map for bob (stderr: $(cat "$out/bob-start-2.err"))"
bob_id=$(jq -r '.brigade_session_id // empty' "$bob_map")
[ -n "$bob_id" ] || die "bob's by-pid map carries no brigade_session_id"
roster > "$out/roster-0.json"
bob_name=$(jq -r --arg id "$bob_id" '[.sessions[] | select(.session_id == $id) | .session_name] | first // ""' "$out/roster-0.json")
bob_ref=$(jq -r --arg id "$bob_id" '[.sessions[] | select(.session_id == $id) | .principal_ref] | first // ""' "$out/roster-0.json")
bob_label=$(jq -r --arg id "$bob_id" '[.sessions[] | select(.session_id == $id) | .human_label] | first // ""' "$out/roster-0.json")
alice_ref=$(jq -r '.self_session_id as $s | [.sessions[] | select(.session_id == $s) | .principal_ref] | first // ""' "$out/roster-0.json")
[ -n "$bob_name" ] || die "the roster does not show bob's session: $(cat "$out/roster-0.json")"
{ [ "$bob_label" = "$alice" ] && [ -n "$alice_ref" ] && [ "$bob_ref" != "$alice_ref" ]; } &&
  ok "bob's session \"$bob_name\" is on the roster under the label $bob_label, the same as alice's, with a principal of his own" ||
  bad "bob's row is not another principal under alice's label: label '$bob_label', principal '$bob_ref' against alice's '$alice_ref'"
[ "$(jq '[.sessions[] | select(.is_self == true)] | length' "$out/roster-0.json")" = 0 ] &&
  ok "a plain \`brigade sessions --json\` marks no row is_self; the session's own row is the one self_session_id names" ||
  say "note: a row of the plain roster carries is_self true"

: > "$out/observer-add.out"
add_item "$title_solo" 'Create HELLO.md at the top of this repository, holding the one word hello. Done when the file exists with that content. No commit is needed.'
add_item "$title_ask" 'Create TODO.md at the top of this repository with three next steps for the project. Done when the file exists. No commit is needed.'
add_item "$title_team" "Two pages for new users, and a link to each.

Part one: docs/USAGE.md, with one section for each of the two commands README.md names, saying what the command does. Part two: docs/FAQ.md, with three questions a new user would ask and their answers, drawn from README.md. The two pages are separate files and can be written by two sessions at once. Last, README.md gets one line that links both pages.

Done when both pages and the README line are committed on the main branch of the origin repository."
add_item "$title_live" 'Read README.md and say, in the comment you leave on this work item, which two commands it documents and what each does. Nothing in the repository changes. Done when the comment names both commands.'
board list > "$out/board-1.json"
[ "$(jq -c '[.items[] | .number] | sort' "$out/board-1.json")" = '[1,2,3,4]' ] &&
  ok "the observer added work items 1 to 4: $(jq -r '[.items[] | "\(.number) \"\(.title)\""] | join("; ")' "$out/board-1.json")" ||
  die "the board does not hold work items 1 to 4: $(cat "$out/board-1.json")"
note_maps

# ---------------------------------------------------------------------------------------------------------------
# 6. Leg solo: nobody else named
# ---------------------------------------------------------------------------------------------------------------
if wants solo; then
  say ""
  say "leg solo: /brigade:work-item-start 1"
  session solo e19-solo 'Skill,Read,Write,Edit,Glob,Bash(ls:*),Bash(cat:*)' 40 "/brigade:work-item-start 1"
  note_maps
  commands solo > "$out/solo.commands.txt"
  answer solo > "$out/solo.answer.txt"
  board show 1 > "$out/board-solo.json"
  for want in 'brigade work-item show 1' 'brigade work-item assign 1 --mine' 'brigade work-item move 1 doing' 'brigade work-item comment 1' 'brigade work-item move 1 done'; do
    grep -qi "^$want" "$out/solo.commands.txt" &&
      ok "solo: the session ran \`$want\`" || bad "solo: the session did not run \`$want\`"
  done
  [ "$(grep -c '^brigade doing' "$out/solo.commands.txt")" -ge 2 ] &&
    ok "solo: it set its roster line when it took the work item and again when it landed it: $(grep '^brigade doing' "$out/solo.commands.txt" | tail -1)" ||
    bad "solo: \`brigade doing\` ran $(grep -c '^brigade doing' "$out/solo.commands.txt") time(s), not at the start and at the end"
  grep -q '^brigade whoami' "$out/solo.commands.txt" &&
    ok "solo: it read its own name with \`brigade whoami\`" || bad "solo: it did not run \`brigade whoami\`"
  [ "$(grep -c '^brigade rename' "$out/solo.commands.txt")" = 1 ] && grep -q "^brigade rename <<'EOF' ⏎ $name_solo ⏎ EOF" "$out/solo.commands.txt" &&
    ok "solo: it ran one \`brigade rename\`, with the name the slug rule gives: $name_solo" ||
    bad "solo: the rename is not one command naming $name_solo: $(grep '^brigade rename' "$out/solo.commands.txt" | tr '\n' ';')"
  grep -qF "recorded: $name_solo" "$out/solo.stream.jsonl" &&
    ok "solo: the command answered \`recorded: $name_solo\`" || bad "solo: the stream carries no \`recorded: $name_solo\`"
  if grep -q -e '^brigade sessions' -e '^brigade send' "$out/solo.commands.txt"; then
    bad "solo: with nobody named it still read the roster or sent a message: $(grep -e '^brigade sessions' -e '^brigade send' "$out/solo.commands.txt" | tr '\n' ';')"
  else
    ok "solo: with nobody named it read no roster and sent no message"
  fi
  { [ -f "$proj/HELLO.md" ] && grep -qi 'hello' "$proj/HELLO.md"; } &&
    ok "solo: the work was done: HELLO.md holds \"$(tr -d '\n' < "$proj/HELLO.md")\"" || bad "solo: HELLO.md is not there, or does not say hello"
  [ "$(jq -r '.item.list' "$out/board-solo.json")" = 'done' ] && [ "$(jq -r '.item.assignee' "$out/board-solo.json")" = "$alice" ] &&
    [ "$(jq -r '.item.closed' "$out/board-solo.json")" = false ] && [ "$(jq '.item.comments | length' "$out/board-solo.json")" -ge 1 ] &&
    ok "solo: work item 1 is in Done, assigned to $alice, open, with $(jq '.item.comments | length' "$out/board-solo.json") comment(s)" ||
    bad "solo: work item 1 after the run: $(jq -c '.item | {list, assignee, closed, comments: (.comments | length)}' "$out/board-solo.json")"
  [ "$(denials solo)" = 0 ] &&
    ok "solo: no command was denied, though the session was allowed no \`brigade\` command of its own: the skill's allowed-tools granted work-item, doing, whoami and rename" ||
    bad "solo: $(denials solo) tool call(s) were denied"
  say "solo: its report begins: $(head -c 300 "$out/solo.answer.txt" | tr '\n' ' ')"
  rm -f "$proj/HELLO.md"
fi

# ---------------------------------------------------------------------------------------------------------------
# 7. Leg ask: a name that is no live session, and another member's session under the user's label
# ---------------------------------------------------------------------------------------------------------------
if wants ask; then
  say ""
  say "leg ask: /brigade:work-item-start 2 nobody-here $bob_name"
  session ask "$kept_name" 'Skill,Read,Glob' 30 "/brigade:work-item-start 2 nobody-here $bob_name"
  note_maps
  commands ask > "$out/ask.commands.txt"
  answer ask > "$out/ask.answer.txt"
  board show 2 > "$out/board-ask.json"
  grep -q '^brigade whoami' "$out/ask.commands.txt" && ! grep -q '^brigade rename' "$out/ask.commands.txt" &&
    ok "ask: the session read its name, \"$kept_name\", and kept it: no \`brigade rename\`" ||
    bad "ask: the name was not read and kept: $(grep -e '^brigade whoami' -e '^brigade rename' "$out/ask.commands.txt" | tr '\n' ';')"
  grep -q '^brigade sessions.*--json' "$out/ask.commands.txt" &&
    ok "ask: it read the roster: $(grep '^brigade sessions' "$out/ask.commands.txt" | head -1)" ||
    bad "ask: it did not run \`brigade sessions --json\`: $(grep '^brigade sessions' "$out/ask.commands.txt" | tr '\n' ';')"
  if grep -q '^brigade send' "$out/ask.commands.txt"; then
    bad "ask: it sent a message though neither name is a follower: $(grep '^brigade send' "$out/ask.commands.txt" | tr '\n' ';')"
  else
    ok "ask: it sent no message: neither name is a session of the user's"
  fi
  { grep -qF 'nobody-here' "$out/ask.answer.txt" && grep -qF "$bob_name" "$out/ask.answer.txt" && grep -qF '?' "$out/ask.answer.txt"; } &&
    ok "ask: its answer names both and asks the user: $(tr '\n' ' ' < "$out/ask.answer.txt" | cut -c1-420)" ||
    bad "ask: the answer does not name both sessions and ask: $(head -c 600 "$out/ask.answer.txt")"
  [ "$(jq -r '.item.list' "$out/board-ask.json")" != 'done' ] &&
    ok "ask: work item 2 was not carried on without the user's answer: it is in $(jq -r '.item.list_name // .item.list' "$out/board-ask.json")" ||
    bad "ask: work item 2 went to Done without the user's answer"
  [ "$(denials ask)" = 0 ] &&
    ok "ask: no command was denied, though the session was allowed no \`brigade\` command of its own: the skill's allowed-tools granted sessions as well" ||
    bad "ask: $(denials ask) tool call(s) were denied"
  rm -f "$proj/TODO.md"
fi

# ---------------------------------------------------------------------------------------------------------------
# 8. Leg team: two followers, one in the leader's working tree and one in a second clone
# ---------------------------------------------------------------------------------------------------------------
if wants team; then
  say ""
  tools_follow='Skill,Read,Write,Edit,Glob,Grep,Bash(brigade:*),Bash(git:*),Bash(ls:*),Bash(cat:*),Bash(mkdir:*)'
  hold 7 helper-a "$proj" "$tools_follow" 60
  new_map || die "helper-a did not register within ${start_wait}s: $(head -c 300 "$out/helper-a.claude.err")"
  a_map=$map_file
  hold 8 helper-b "$proj2" "$tools_follow" 60
  new_map || die "helper-b did not register within ${start_wait}s: $(head -c 300 "$out/helper-b.claude.err")"
  b_map=$map_file
  a_id=$(jq -r '.brigade_session_id // empty' "$a_map")
  b_id=$(jq -r '.brigade_session_id // empty' "$b_map")
  { [ -n "$a_id" ] && [ -n "$b_id" ] && [ "$a_id" != "$b_id" ]; } || die "the followers' by-pid maps do not carry two session ids: '$a_id' and '$b_id'"
  a_cell=$(printf '%s' "$a_id" | tail -c 5)
  b_cell=$(printf '%s' "$b_id" | tail -c 5)
  tell 7 "$follower_prompt"
  tell 8 "$follower_prompt"
  wait_turns helper-a 1 "$turn_wait" || die "helper-a's first turn did not end within ${turn_wait}s"
  wait_turns helper-b 1 "$turn_wait" || die "helper-b's first turn did not end within ${turn_wait}s"
  hold 9 e19-lead "$proj" "$tools_follow" 150
  new_map || die "the leader did not register within ${start_wait}s: $(head -c 300 "$out/e19-lead.claude.err")"
  l_map=$map_file
  l_id=$(jq -r '.brigade_session_id // empty' "$l_map")
  { [ -n "$l_id" ] && [ "$l_id" != "$a_id" ] && [ "$l_id" != "$b_id" ]; } || die "the leader's by-pid map carries no session id of its own: '$l_id'"
  tell 9 "For the work item I am about to give you: my session helper-a works in this same working tree as you, and my other session, the one whose SESSION cell is $b_cell, works in its own clone of this repository, which pushes to the same origin. Reply with the single word understood."
  wait_turns e19-lead 1 "$turn_wait" || die "the leader's first turn did not end within ${turn_wait}s"
  roster > "$out/roster-team-before.json"
  [ "$(jq -r --arg a "$a_id" --arg b "$b_id" --arg l "$l_id" '[.sessions[] | select(.session_id == $a or .session_id == $b or .session_id == $l) | .principal_ref] | unique | length' "$out/roster-team-before.json")" = 1 ] &&
    ok "team: the leader and both followers are on the roster under one principal, alice's" ||
    bad "team: the three sessions are not one principal's: $(jq -c --arg a "$a_id" --arg b "$b_id" --arg l "$l_id" '[.sessions[] | select(.session_id == $a or .session_id == $b or .session_id == $l) | {session_name, principal_ref}]' "$out/roster-team-before.json")"
  say "leg team: helper-a is $a_cell in the leader's working tree, helper-b is $b_cell in a second clone, the leader is $(printf '%s' "$l_id" | tail -c 5)"
  say "leg team: /brigade:work-item-start 3 helper-a $b_cell"
  tell 9 "/brigade:work-item-start 3 helper-a $b_cell"

  # Until the work item is in Done, or the three sessions have made no tool call for stall_wait seconds (the
  # leader is waiting on its user, whom nobody plays here); then until all three have been quiet for settle_wait.
  _began=$(now)
  _deadline=$(( _began + team_wait ))
  _before="$(tool_count e19-lead) $(tool_count helper-a) $(tool_count helper-b)"
  _stall=$(( _began + stall_wait ))
  landed=no
  while [ "$(now)" -lt "$_deadline" ]; do
    if [ "$(board show 3 | jq -r '.item.list')" = 'done' ]; then landed=yes; break; fi
    _after="$(tool_count e19-lead) $(tool_count helper-a) $(tool_count helper-b)"
    if [ "$_after" != "$_before" ]; then _before=$_after; _stall=$(( $(now) + stall_wait )); fi
    [ "$(now)" -ge "$_stall" ] && break
    sleep 10
  done
  [ "$landed" = yes ] && say "leg team: work item 3 reached Done $(( $(now) - _began ))s after the command" ||
    say "leg team: work item 3 was not in Done $(( $(now) - _began ))s after the command (no session made a tool call for ${stall_wait}s, or ${team_wait}s passed)"
  _before="$(tool_count e19-lead) $(tool_count helper-a) $(tool_count helper-b)"
  _quiet=$(( $(now) + settle_wait ))
  _deadline=$(( $(now) + 600 ))
  while [ "$(now)" -lt "$_quiet" ] && [ "$(now)" -lt "$_deadline" ]; do
    sleep 5
    _after="$(tool_count e19-lead) $(tool_count helper-a) $(tool_count helper-b)"
    if [ "$_after" != "$_before" ]; then _before=$_after; _quiet=$(( $(now) + settle_wait )); fi
  done
  roster > "$out/roster-team-after.json"
  release
  note_maps

  for s in e19-lead helper-a helper-b; do commands "$s" > "$out/$s.commands.txt"; done
  sends e19-lead > "$out/team.sends.jsonl"
  sends_to e19-lead "$a_cell" > "$out/team.sends-to-a.jsonl"
  sends_to e19-lead "$b_cell" > "$out/team.sends-to-b.jsonl"
  sed -n 1p "$out/team.sends-to-a.jsonl" | jq -r . > "$out/team.first-to-a.txt" 2>/dev/null || true
  sed -n 1p "$out/team.sends-to-b.jsonl" | jq -r . > "$out/team.first-to-b.txt" 2>/dev/null || true
  board show 3 > "$out/board-team.json"
  jq -r '.item.comments[-1].text // ""' "$out/board-team.json" > "$out/team.landing-comment.txt"

  grep -q '^brigade sessions.*--json' "$out/e19-lead.commands.txt" &&
    ok "team: the leader read the roster as JSON to find its followers" || bad "team: the leader did not run \`brigade sessions --json\`"
  grep -q "^brigade rename <<'EOF' ⏎ $name_team ⏎ EOF" "$out/e19-lead.commands.txt" && grep -qF "recorded: $name_team" "$out/e19-lead.stream.jsonl" &&
    ok "team: the leader named itself, and the command answered \`recorded: $name_team\`" ||
    bad "team: the leader's rename: $(grep '^brigade rename' "$out/e19-lead.commands.txt" | tr '\n' ';')"
  { [ -s "$out/team.first-to-a.txt" ] && [ -s "$out/team.first-to-b.txt" ]; } &&
    ok "team: each follower got a first message of its own: $(wc -l < "$out/team.sends-to-a.jsonl" | tr -d ' ') message(s) to helper-a ($a_cell), $(wc -l < "$out/team.sends-to-b.jsonl" | tr -d ' ') to helper-b ($b_cell)" ||
    bad "team: the leader did not send each follower a message: $(wc -l < "$out/team.sends.jsonl" | tr -d ' ') send(s) in all"
  { sed -n 1p "$out/team.first-to-a.txt" | grep -q -- '--summary "3: ' && sed -n 1p "$out/team.first-to-b.txt" | grep -q -- '--summary "3: '; } &&
    ok "team: both first messages carry a summary that begins with the work item's number" ||
    bad "team: a first message's summary does not begin \"3: \": $(sed -n 1p "$out/team.first-to-a.txt") / $(sed -n 1p "$out/team.first-to-b.txt")"
  { grep -q 'USAGE\.md' "$out/team.first-to-a.txt" && grep -q 'FAQ\.md' "$out/team.first-to-b.txt"; } ||
    { grep -q 'FAQ\.md' "$out/team.first-to-a.txt" && grep -q 'USAGE\.md' "$out/team.first-to-b.txt"; } &&
    ok "team: each first message names that follower's own part (one page each)" ||
    bad "team: the first messages do not give each follower one of the two pages"
  grep -Eqi 'uncommitted|not commit|n.t commit|without committing|no commit|never commit' "$out/team.first-to-a.txt" &&
    ok "team: helper-a, in the leader's working tree, was told to leave its edits uncommitted: $(grep -Ei 'uncommitted|not commit|n.t commit|without committing|no commit|never commit' "$out/team.first-to-a.txt" | head -1 | cut -c1-240)" ||
    bad "team: helper-a's first message does not tell it to leave its edits uncommitted"
  grep -Eqi 'comment|move|close' "$out/team.first-to-a.txt" && grep -Eqi 'comment|move|close' "$out/team.first-to-b.txt" &&
    ok "team: both first messages say the follower leaves the work item to the leader" ||
    bad "team: a first message does not say the follower leaves the work item alone"
  if grep -Eq '(^| )git( -C [^ ]+)? (commit|push)' "$out/helper-a.commands.txt"; then
    bad "team: helper-a committed or pushed in the shared working tree: $(grep -E 'git( -C [^ ]+)? (commit|push)' "$out/helper-a.commands.txt" | tr '\n' ';' | cut -c1-300)"
  else
    ok "team: helper-a ran no git commit and no git push in the shared working tree"
  fi
  grep -Eq '(^| )git( -C [^ ]+)? commit' "$out/e19-lead.commands.txt" &&
    ok "team: the leader committed in the shared working tree: $(grep -Eo 'git( -C [^ ]+)? commit[^⏎]*' "$out/e19-lead.commands.txt" | head -1 | cut -c1-160)" ||
    bad "team: the leader ran no git commit"
  say "team: helper-b's git commands in its own clone: $(grep -Eo 'git( -C [^ ]+)? (commit|push)[^⏎]*' "$out/helper-b.commands.txt" | tr '\n' ';' | cut -c1-240)"
  if grep -Eq '^brigade work-item (comment|move|close|assign|edit|tag|reopen)' "$out/helper-a.commands.txt" "$out/helper-b.commands.txt"; then
    bad "team: a follower changed the work item: $(grep -E '^brigade work-item (comment|move|close|assign|edit|tag|reopen)' "$out/helper-a.commands.txt" "$out/helper-b.commands.txt" | tr '\n' ';' | cut -c1-300)"
  else
    ok "team: neither follower commented on, moved, assigned or closed the work item"
  fi
  git -C "$proj" log --format= --name-only > "$out/team.proj-files.txt" 2>/dev/null || true
  git --git-dir="$origin" log --format= --name-only main > "$out/team.origin-files.txt" 2>/dev/null || true
  { grep -qx 'docs/USAGE.md' "$out/team.origin-files.txt" && grep -qx 'docs/FAQ.md' "$out/team.origin-files.txt" && git --git-dir="$origin" log -p --format= main -- README.md | grep -q '^+.*USAGE'; } &&
    ok "team: the origin's main branch holds both pages and the README's line: $(git --git-dir="$origin" log --format='%h %s' main | tr '\n' ';' | cut -c1-300)" ||
    bad "team: the origin's main branch does not hold both pages and the README line: $(sort -u "$out/team.origin-files.txt" | tr '\n' ' ')"
  [ "$(jq -r '.item.list' "$out/board-team.json")" = 'done' ] && [ "$(jq -r '.item.assignee' "$out/board-team.json")" = "$alice" ] &&
    ok "team: work item 3 is in Done, assigned to $alice" ||
    bad "team: work item 3 after the run: $(jq -c '.item | {list, assignee, closed, comments: (.comments | length)}' "$out/board-team.json")"
  jq -r '[.item.comments[].session] | unique | join(", ")' "$out/board-team.json" > "$out/team.comment-sessions.txt"
  if grep -q 'helper-' "$out/team.comment-sessions.txt" || [ ! -s "$out/team.landing-comment.txt" ]; then
    bad "team: the work item's comments are not the leader's alone: sessions '$(cat "$out/team.comment-sessions.txt")'"
  else
    ok "team: every comment on the work item is the leader's: session(s) $(cat "$out/team.comment-sessions.txt")"
  fi
  { grep -q 'USAGE\.md' "$out/team.landing-comment.txt" && grep -q 'FAQ\.md' "$out/team.landing-comment.txt"; } &&
    ok "team: the landing comment names both followers' work: $(tr '\n' ' ' < "$out/team.landing-comment.txt" | cut -c1-600)" ||
    bad "team: the landing comment does not name both pages: $(tr '\n' ' ' < "$out/team.landing-comment.txt" | cut -c1-600)"
  { grep -q 'helper-a' "$out/team.landing-comment.txt" && grep -q -e 'helper-b' -e "$b_cell" "$out/team.landing-comment.txt"; } &&
    ok "team: the landing comment names both follower sessions" ||
    say "note: the landing comment does not name both follower sessions by name or SESSION cell"
  { [ "$(wc -l < "$out/team.sends-to-a.jsonl" | tr -d ' ')" -ge 2 ] && [ "$(wc -l < "$out/team.sends-to-b.jsonl" | tr -d ' ')" -ge 2 ]; } &&
    ok "team: the leader wrote to each follower again after the first message; its last to helper-a: $(tail -1 "$out/team.sends-to-a.jsonl" | jq -r . | sed -n '2,3p' | tr '\n' ' ' | cut -c1-200)" ||
    bad "team: a follower got no message after its first (it was not told the work item is finished)"
  say "team: denied tool calls: leader $(denials e19-lead), helper-a $(denials helper-a), helper-b $(denials helper-b)"
  say "team: the leader's name on the roster at the end: $(jq -r --arg id "$l_id" '[.sessions[] | select(.session_id == $id) | .session_name] | first // "(offline)"' "$out/roster-team-after.json")"
  say "team: the leader's last answer begins: $(answer e19-lead | tail -n 40 | head -c 400 | tr '\n' ' ')"
fi

# ---------------------------------------------------------------------------------------------------------------
# 9. Leg live: the recorded name is applied at the next prompt, and the roster shows it
# ---------------------------------------------------------------------------------------------------------------
if wants live; then
  say ""
  if ! command -v tmux >/dev/null 2>&1; then
    say "leg live: SKIPPED: tmux is not on PATH, so no interactive session can be driven here"
  else
    say "leg live: an interactive session, /brigade:work-item-start 4, then a second prompt"
    pane() { tmux -L "$tmux_sock" capture-pane -p -t live -S -400 2>/dev/null || true; }
    # shellcheck disable=SC2086  # as above
    tmux -L "$tmux_sock" new-session -d -s live -x 200 -y 50 -c "$proj" \
      env $strip_args XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" DISABLE_AUTOUPDATER=1 \
      claude -n e19-live --plugin-dir "$repo/plugin" --settings '{"enabledPlugins":{"brigade@brigade":false}}' \
      --permission-mode default --allowedTools 'Skill,Read,Glob,Grep,Bash(brigade:*),Bash(ls:*),Bash(cat:*)' ||
      die "tmux could not start the interactive session"
    tmux_up=yes
    # The folder-trust question, when Claude Code asks it of this new directory: the choice the cursor is on is
    # "No, exit" in 2.1.323, so the cursor is moved to the yes before Enter.
    _maps_before=$(find "$state_dir/sessions/by-pid" -name '*.json' 2>/dev/null | wc -l | tr -d ' ')
    _deadline=$(( $(now) + 60 ))
    while [ "$(now)" -lt "$_deadline" ]; do
      if pane | grep -qi 'trust'; then
        _n=0
        while ! pane | grep '❯' | grep -qi 'yes' && [ "$_n" -lt 4 ]; do
          tmux -L "$tmux_sock" send-keys -t live Down
          sleep 1
          _n=$((_n + 1))
        done
        pane > "$out/live.pane-trust.txt"
        tmux -L "$tmux_sock" send-keys -t live Enter
        break
      fi
      [ "$(find "$state_dir/sessions/by-pid" -name '*.json' 2>/dev/null | wc -l | tr -d ' ')" != "$_maps_before" ] && break
      sleep 2
    done
    new_map || { pane > "$out/live.pane.txt"; die "the interactive session did not register within ${start_wait}s (the pane is in live.pane.txt)"; }
    live_map=$map_file
    live_id=$(jq -r '.brigade_session_id // empty' "$live_map")
    [ -n "$live_id" ] || die "the interactive session's by-pid map carries no brigade_session_id"
    sleep 5
    name_before=$(roster_name "$live_id")
    say "live: the session is $(printf '%s' "$live_id" | tail -c 5), named \"$name_before\" on the roster"
    tmux -L "$tmux_sock" send-keys -t live -l '/brigade:work-item-start 4'
    sleep 2
    tmux -L "$tmux_sock" send-keys -t live Enter
    _deadline=$(( $(now) + run_wait ))
    live_landed=no
    while [ "$(now)" -lt "$_deadline" ]; do
      if [ "$(board show 4 | jq -r '.item.list')" = 'done' ]; then live_landed=yes; break; fi
      sleep 5
    done
    sleep 20
    pane > "$out/live.pane-1.txt"
    if [ "$live_landed" = yes ]; then
      ok "live: the interactive session carried work item 4 to Done; its comment: $(board show 4 | jq -r '.item.comments[-1].text // ""' | tr '\n' ' ' | cut -c1-240)"
    elif grep -q 'Do you want to proceed' "$out/live.pane-1.txt"; then
      bad "live: work item 4 was not in Done after ${run_wait}s: the session waits at a permission prompt (the pane is in live.pane-1.txt)"
    else
      bad "live: work item 4 was not in Done after ${run_wait}s (the pane is in live.pane-1.txt)"
    fi
    grep -qF "recorded: $name_live" "$out/live.pane-1.txt" &&
      ok "live: the pane shows the command's answer, \`recorded: $name_live\`" ||
      say "note: the pane does not show \`recorded: $name_live\` (the tool output may be folded)"
    name_mid=$(roster_name "$live_id")
    say "live: before the second prompt the roster still says \"$name_mid\""
    tmux -L "$tmux_sock" send-keys -t live -l 'Thank you.'
    sleep 2
    tmux -L "$tmux_sock" send-keys -t live Enter
    _deadline=$(( $(now) + roster_wait ))
    name_after=''
    while [ "$(now)" -lt "$_deadline" ]; do
      name_after=$(roster_name "$live_id")
      [ "$name_after" = "$name_live" ] && break
      sleep 5
    done
    roster > "$out/roster-live.json"
    pane > "$out/live.pane-2.txt"
    [ "$name_after" = "$name_live" ] &&
      ok "live: after the user's next prompt the roster names the session \"$name_after\" (it was \"$name_before\")" ||
      bad "live: ${roster_wait}s after the second prompt the roster names the session \"$name_after\", not \"$name_live\""
    tmux -L "$tmux_sock" send-keys -t live -l '/exit'
    sleep 1
    tmux -L "$tmux_sock" send-keys -t live Enter
    sleep 5
    tmux -L "$tmux_sock" kill-server 2>/dev/null || true
    tmux_up=no
  fi
fi

# ---------------------------------------------------------------------------------------------------------------
# 10. What reached the backend
# ---------------------------------------------------------------------------------------------------------------
# The board is its folder: no change file is under the backend's root. What a session SAID of a work item goes
# where every name, roster line and message goes, so a title can be in the backend's files: counted here, not
# asserted (a leader's first message carries the title by the skill's own words).
say ""
held=''
for t in "$title_solo" "$title_ask" "$title_team" "$title_live"; do
  held="$held $(grep -rlF -e "$t" "$state_dir/fs-adapter" 2>/dev/null | wc -l | tr -d ' ')"
done
say "the backend's files that hold the title of work items 1 to 4, in a roster line or a message:$held; the board's own files are $(find "$state_dir/boards" -name '*.json' | wc -l | tr -d ' ') change files under boards/"

say ""
say "E19: $failures assertion(s) failed"
exit "$failures"
