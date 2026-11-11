#!/bin/sh
# usage: scripts/experiments/E18-work-items.sh      (run from the repository root after `make build`)
#
# The live proof of card 263's skills (a team's board of work items): real Claude Code sessions, headless
# (`claude -p`), with the plugin of THIS checkout and the binary `make build` wrote, in a throwaway project whose
# team file turns the board on, on the fs adapter (a local directory, no backend). Each leg is one session given
# one slash command, as a person would type it:
#   A  `/brigade:work-item add <title>` adds a work item: the session runs one `brigade work-item add` with the
#      title on stdin, under the skill's own allowed tools, and the board holds work item 1;
#   B  `/brigade:work-items` shows the board: the session runs `brigade work-item list` and nothing else, and its
#      answer carries the table as the command printed it;
#   C  `/brigade:work-item-start <number>` takes a work item and works it through: the session reads it, assigns
#      it to its user, moves it to Doing, does what it asks (one small file), comments what it did and moves it
#      to Done;
#   D  the user's own words, no command (work item 268): asked "What is on the brigade board right now?", the
#      session knows the Brigade Board is the team's board of work items, runs `brigade work-item list` and
#      answers with what is on it;
#   E  the same for a change: asked to put something "on the brigade board", it runs one `brigade work-item add`
#      and the board holds the new work item;
#   F  a hidden list (work item 269): the observer hides `Done`, and `/brigade:work-items` prints the listing
#      without that list's work item and with the line that says what was left out; `/brigade:work-items all`
#      runs `brigade work-item list --all` and prints every list;
#   G  a question the default listing would answer wrongly: asked whether the board has a work item about
#      HELLO.md, which is in the hidden list, the session reads every list and says yes, with its number.
# A second session of the same member, driven by the real `brigade hook` subcommands with a sleeper as CLAUDE_PID,
# is the observer: it reads the board with `brigade work-item … --json` after each leg, and adds leg C's work item.
# Both sessions are on one machine, so they read and write the one board folder.
#
# It needs a logged-in Claude Code and costs model calls, so it never runs in CI. Everything Brigade touches is
# under one temporary root (its own XDG_CONFIG_HOME, XDG_STATE_HOME and XDG_DATA_HOME, its own project), removed at
# the end; the user's real CLAUDE_CONFIG_DIR is kept for the `claude` runs alone (the login lives there), the
# Brigade plugin installed there is switched off for these runs, and the transcripts they write under it are
# removed by name. Where `syncthing` is on PATH the sessions' watchers start an engine of the rig's own, with no
# peer to talk to, which stops with them.
#
# Output: one `ok:`/`FAIL:` line per assertion, then the teardown. The evidence (each session's stream and the
# observer's readings) is kept under .ignored/card-263/e18/<UTC stamp>/. Exit status is the number of failed
# assertions, 64 when the rig itself fails. E18_KEEP=1 keeps the temporary root.
# shellcheck disable=SC2015  # every `A && ok || bad` below: ok never fails, so bad runs only when A does not
set -eu
umask 077

alice='alice@board.invalid'
team_name=board-proof
title_a='Try the board from a live session'
title_c='Add a HELLO.md that says hello'
title_e='Write the release checklist'
asked_d='What is on the brigade board right now?'
asked_g='Is there a work item about HELLO.md on the brigade board? Answer yes or no, and give its number if there is one.'
run_wait=420    # seconds one `claude -p` run may take

say() {
  printf '%s\n' "$*"
  if [ -n "${summary:-}" ]; then printf '%s\n' "$*" >>"$summary"; fi
}
die() { printf 'E18: %s\n' "$1" >&2; exit 64; }
failures=0
ok() { say "ok: $*"; }
bad() { failures=$((failures + 1)); say "FAIL: $*"; }
now() { date +%s; }

# ---------------------------------------------------------------------------------------------------------------
# 1. Preconditions
# ---------------------------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P) || die "cannot resolve the repository root"
[ -d "$repo/plugin/skills/work-items" ] || die "no plugin/skills/work-items at $repo"
brigade=$repo/bin/brigade
fs_adapter=$repo/bin/brigade-adapter-fs
[ -x "$brigade" ] || die "missing $brigade: run \`make build\` first"
[ -x "$fs_adapter" ] || die "missing $fs_adapter: run \`make build\` first"
for tool in jq git claude; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is required"
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
out=$repo/.ignored/card-263/e18/$stamp
mkdir -p "$out"
summary=$out/summary.txt
: > "$summary"

rig=$(mktemp -d "${TMPDIR:-/tmp}/brigade-board-XXXXXX") || die "cannot create a temporary directory"
rig=$(CDPATH='' cd -- "$rig" && pwd -P)
case $rig in */brigade-board-*) ;; *) die "refusing to work in an unexpected temp root: $rig" ;; esac
rig_name=$(basename "$rig")
chmod 700 "$rig"

xdg_config=$rig/xdg/config
xdg_state=$rig/xdg/state
xdg_data=$rig/xdg/data
proj=$rig/proj                    # the sessions' working directory: a git checkout with .brigade.json
observer_cfg=$rig/claude-observer # the observer's throwaway Claude config dir (never the user's)
plugin_root=$rig/plugin           # the observer's plugin root: bin/brigade is the built binary
state_dir=$xdg_state/brigade
secret_file=$rig/join.secret
mkdir -p "$xdg_config/brigade" "$xdg_state" "$xdg_data" "$proj" "$observer_cfg" "$plugin_root/bin"
ln -s "$brigade" "$plugin_root/bin/brigade"
cp "$repo/plugin/bin/VERSION" "$plugin_root/bin/VERSION"

printf '%s\n' "$brigade" > "$xdg_config/brigade/dev-binary"
printf '{"fs": ["%s"]}\n' "$fs_adapter" > "$xdg_config/brigade/adapters.json"

sleeper=''
claude_pid=''

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
  [ -n "$claude_pid" ] && kill_wait "$claude_pid" "a claude session"
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
  # The transcripts the runs wrote under the user's real Claude config dir: only a directory under its projects/
  # whose name carries this rig's own name is removed.
  for td in "$claude_cfg"/projects/*"$rig_name"*; do
    [ -d "$td" ] || continue
    say "teardown: removing the transcript directory $td"
    rm -rf "$td"
  done
  if [ -z "${E18_KEEP:-}" ]; then
    case $rig in
      */brigade-board-*) say "teardown: removing the temp root $rig"; rm -rf "$rig" ;;
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
    CLAUDE_PID="$sleeper" CLAUDE_CODE_SESSION_ID="board-observer-$sleeper" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli \
    CLAUDE_PLUGIN_ROOT="$plugin_root" "$@")
}
# board <args…>: one `brigade work-item … --json` as the observer; prints the envelope's result.
board() { observer "$brigade" work-item "$@" --json | jq -c '.result'; }
# session <leg> <allowed tools> <max turns> <prompt>: one headless Claude Code session in the project, given one
# prompt, with this checkout's plugin and the installed Brigade plugin switched off. Its stream is the evidence.
# shellcheck disable=SC2086  # as above
session() {
  _leg=$1
  ( cd "$proj" && exec env $strip_args \
      XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" DISABLE_AUTOUPDATER=1 \
      claude -p "$4" -n "e18-$_leg" --plugin-dir "$repo/plugin" --settings '{"enabledPlugins":{"brigade@brigade":false}}' \
        --permission-mode default --allowedTools "$2" \
        --output-format stream-json --verbose --max-turns "$3" \
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
# commands <leg>: one line per Bash tool call the model made, the command with its line breaks folded.
commands() {
  jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use" and .name == "Bash") | .input.command | gsub("\n"; " ⏎ ")' \
    "$out/$1.stream.jsonl" 2>/dev/null || true
}
# skills <leg>: one line per skill the model loaded by itself, through the Skill tool.
skills() {
  jq -r 'select(.type == "assistant") | .message.content[]? | select(.type == "tool_use" and .name == "Skill") | .input.skill // empty' \
    "$out/$1.stream.jsonl" 2>/dev/null || true
}
# answer <leg>: the session's final answer.
answer() { jq -r 'select(.type == "result") | .result // empty' "$out/$1.stream.jsonl" 2>/dev/null || true; }
# start_line <leg>: what the SessionStart hook printed to the session.
start_line() { grep -o 'Brigade: file sync on: [^"\\]*' "$out/$1.stream.jsonl" 2>/dev/null | head -1 || true; }

# ---------------------------------------------------------------------------------------------------------------
# 5. The project, the team with a board, the observer
# ---------------------------------------------------------------------------------------------------------------
say "E18: temp root $rig"
say "E18: evidence  $out"
say "E18: claude $(claude --version 2>/dev/null | head -1); brigade $("$brigade" version 2>/dev/null | head -1)"

git -C "$proj" init -q
printf '# Lanternfish\n\nA small command-line tool, used here as a project with nothing in it.\n' > "$proj/README.md"
(cd "$proj" && terminal "$brigade" team create --adapter fs --url http://127.0.0.1:1 --key placeholder \
  --name "$team_name" --label "$alice" --secret-file "$secret_file") >"$out/team-create.out" 2>"$out/team-create.err" ||
  die "team create failed: $(cat "$out/team-create.err")"
jq '. + {sync: {board: true}}' "$proj/.brigade.json" > "$rig/team-file.json" && cp "$rig/team-file.json" "$proj/.brigade.json"
chmod 644 "$proj/.brigade.json"
jq -e '.sync.board == true' "$proj/.brigade.json" >/dev/null || die "the team file does not turn the board on"
ok "team \"$team_name\" created by $alice; its .brigade.json carries \"sync\": {\"board\": true}"

sleep 3600 &
sleeper=$!
jq -cn --arg s "board-observer-$sleeper" --arg c "$proj" \
  '{session_id:$s,cwd:$c,hook_event_name:"SessionStart",transcript_path:"/never/read.jsonl",source:"startup"}' |
  observer "$brigade" hook session-start >"$out/observer-start.out" 2>"$out/observer-start.err"
# The observer has no inbox socket, so no watcher and no sync of its own: it reads and writes the board folder its
# map names, which is the one the real sessions' maps name too.
observer_map=$state_dir/sessions/by-pid/$sleeper.json
[ -f "$observer_map" ] || die "the hook wrote no by-pid map for the observer (stderr: $(cat "$out/observer-start.err"))"
board_folder=$(jq -r '.board_folder // empty' "$observer_map")
case $board_folder in
  "$state_dir"/boards/*) ok "the observer's map names the board folder under the state directory: boards/$(basename "$board_folder")" ;;
  *) die "the observer's map carries no board folder under $state_dir/boards: '$board_folder'" ;;
esac
board list > "$out/board-0.json" || die "the observer cannot read the board"
[ "$(jq '.items | length' "$out/board-0.json")" = 0 ] && [ "$(jq -r '[.lists[] | .key + "=" + .name] | join(",")' "$out/board-0.json")" = "todo=Todo,doing=Doing,done=Done" ] &&
  ok "the board starts empty, with the lists Todo, Doing, Done under the keys todo, doing, done" || bad "the board at the start: $(cat "$out/board-0.json")"

# ---------------------------------------------------------------------------------------------------------------
# 6. Leg A: /brigade:work-item add
# ---------------------------------------------------------------------------------------------------------------
say ""
say "leg A: /brigade:work-item add $title_a"
session add 'Skill' 8 "/brigade:work-item add $title_a"
commands add > "$out/add.commands.txt"
answer add > "$out/add.answer.txt"
line_a=$(start_line add)
case $line_a in
  *"the team's Brigade Board of work items (\`brigade work-item list\`)"*) ok "A: the session's start line named the Brigade Board: $line_a" ;;
  *) bad "A: the session's stream carries no \`file sync on\` line naming the Brigade Board: $line_a" ;;
esac
grep -q '^brigade work-item add' "$out/add.commands.txt" &&
  ok "A: the session ran $(grep -c '^brigade work-item' "$out/add.commands.txt") brigade work-item command(s): $(head -1 "$out/add.commands.txt")" ||
  bad "A: no \`brigade work-item add\` among the session's commands: $(cat "$out/add.commands.txt")"
board list > "$out/board-a.json"
[ "$(jq -r '[.items[] | select(.number == 1) | .title] | first // ""' "$out/board-a.json")" = "$title_a" ] &&
  ok "A: the observer's board holds work item 1 \"$title_a\" in $(jq -r '.items[0].list_name' "$out/board-a.json")" ||
  bad "A: the board after the add: $(cat "$out/board-a.json")"
grep -q 'added: 1' "$out/add.answer.txt" &&
  ok "A: the session's answer carries the command's own line: $(grep 'added: 1' "$out/add.answer.txt" | head -1)" ||
  bad "A: the answer does not carry \`added: 1\`: $(head -c 400 "$out/add.answer.txt")"

# ---------------------------------------------------------------------------------------------------------------
# 7. Leg B: /brigade:work-items
# ---------------------------------------------------------------------------------------------------------------
say ""
say "leg B: /brigade:work-items"
session show 'Skill' 6 "/brigade:work-items"
commands show > "$out/show.commands.txt"
answer show > "$out/show.answer.txt"
[ "$(cat "$out/show.commands.txt")" = "brigade work-item list" ] &&
  ok "B: the session ran exactly one command: brigade work-item list" ||
  bad "B: the session's commands: $(cat "$out/show.commands.txt")"
observer "$brigade" work-item list > "$out/board-b.txt"
row=$(grep "$title_a" "$out/board-b.txt" || true)
{ grep -qF "$row" "$out/show.answer.txt" && grep -q '^LIST  *NUMBER  *TITLE' "$out/show.answer.txt" && grep -q '^open by list: Todo (1), Doing (0), Done (0)' "$out/show.answer.txt"; } &&
  ok "B: the answer is the table as the command printed it (header, the row, the line of counts)" ||
  bad "B: the answer is not the command's output: $(head -c 600 "$out/show.answer.txt")"

# ---------------------------------------------------------------------------------------------------------------
# 8. Leg C: /brigade:work-item-start
# ---------------------------------------------------------------------------------------------------------------
say ""
printf '%s\n\n%s\n' "$title_c" 'Create HELLO.md at the top of this repository, holding the one word hello. Done when the file exists with that content. No commit is needed.' |
  observer "$brigade" work-item add > "$out/observer-add.out" || die "the observer could not add leg C's work item"
ok "the observer added leg C's work item: $(cat "$out/observer-add.out")"
say "leg C: /brigade:work-item-start 2"
session start 'Skill,Read,Write,Edit,Glob,Bash(brigade:*),Bash(ls:*),Bash(cat:*)' 30 "/brigade:work-item-start 2"
commands start > "$out/start.commands.txt"
answer start > "$out/start.answer.txt"
board show 2 > "$out/board-c.json"
# A list is named by its key or its name: either spelling of the two moves counts.
for want in 'brigade work-item show 2' 'brigade work-item assign 2 --mine' 'brigade work-item move 2 doing' 'brigade work-item comment 2' 'brigade work-item move 2 done'; do
  grep -qi "^$want" "$out/start.commands.txt" &&
    ok "C: the session ran \`$want\`" || bad "C: the session did not run \`$want\`"
done
grep -q '^brigade doing' "$out/start.commands.txt" &&
  ok "C: the session published its roster line: $(grep '^brigade doing' "$out/start.commands.txt" | head -1)" ||
  bad "C: the session published no roster line"
{ [ -f "$proj/HELLO.md" ] && grep -qi 'hello' "$proj/HELLO.md"; } &&
  ok "C: the work was done: HELLO.md holds \"$(tr -d '\n' < "$proj/HELLO.md")\"" || bad "C: HELLO.md is not there, or does not say hello"
[ "$(jq -r '.item.list' "$out/board-c.json")" = 'done' ] && [ "$(jq -r '.item.assignee' "$out/board-c.json")" = "$alice" ] &&
  [ "$(jq -r '.item.closed' "$out/board-c.json")" = false ] && [ "$(jq '.item.comments | length' "$out/board-c.json")" -ge 1 ] &&
  ok "C: work item 2 is in Done, assigned to $alice, open, with $(jq '.item.comments | length' "$out/board-c.json") comment(s)" ||
  bad "C: work item 2 after the run: $(jq -c '.item | {list, assignee, closed, comments: (.comments | length)}' "$out/board-c.json")"
say "C: the comment the session left: $(jq -r '.item.comments[-1].text // ""' "$out/board-c.json" | tr '\n' ' ' | cut -c1-300)"
say "C: the history: $(jq -r '[.item.history[].what] | join(" | ")' "$out/board-c.json")"
[ "$(jq -r '.item.comments[-1].session // ""' "$out/board-c.json")" != "" ] &&
  ok "C: the comment names its session: $(jq -r '.item.comments[-1].session' "$out/board-c.json")" || bad "C: the comment names no session"

# ---------------------------------------------------------------------------------------------------------------
# 9. Legs D and E: the user's own words name the Brigade Board (work item 268)
# ---------------------------------------------------------------------------------------------------------------
say ""
say "leg D: $asked_d"
session words 'Skill,Bash(brigade work-item:*)' 8 "$asked_d"
commands words > "$out/words.commands.txt"
skills words > "$out/words.skills.txt"
answer words > "$out/words.answer.txt"
grep -qx 'brigade:work-items' "$out/words.skills.txt" &&
  ok "D: asked in words about \"the brigade board\", the session loaded the skill itself: $(tr '\n' ' ' < "$out/words.skills.txt")" ||
  bad "D: the session did not load brigade:work-items; it loaded: $(tr '\n' ' ' < "$out/words.skills.txt")"
[ "$(cat "$out/words.commands.txt")" = "brigade work-item list" ] &&
  ok "D: it ran exactly one command: brigade work-item list" ||
  bad "D: the session's commands: $(cat "$out/words.commands.txt")"
{ grep -qF "$title_a" "$out/words.answer.txt" && grep -qF "$title_c" "$out/words.answer.txt"; } &&
  ok "D: its answer names both work items on the board: \"$title_a\" and \"$title_c\"" ||
  bad "D: the answer does not name both work items: $(head -c 600 "$out/words.answer.txt")"

say ""
say "leg E: Put \"$title_e\" on the brigade board."
session put 'Skill,Bash(brigade work-item:*)' 8 "Put \"$title_e\" on the brigade board."
commands put > "$out/put.commands.txt"
skills put > "$out/put.skills.txt"
answer put > "$out/put.answer.txt"
grep -qx 'brigade:work-item' "$out/put.skills.txt" &&
  ok "E: asked to put it \"on the brigade board\", the session loaded the skill itself: $(tr '\n' ' ' < "$out/put.skills.txt")" ||
  bad "E: the session did not load brigade:work-item; it loaded: $(tr '\n' ' ' < "$out/put.skills.txt")"
{ [ "$(wc -l < "$out/put.commands.txt" | tr -d ' ')" = 1 ] && grep -q '^brigade work-item add' "$out/put.commands.txt"; } &&
  ok "E: it ran exactly one command: $(head -1 "$out/put.commands.txt")" ||
  bad "E: the session's commands: $(cat "$out/put.commands.txt")"
board list > "$out/board-e.json"
[ "$(jq -c --arg t "$title_e" '[.items[] | select(.title == $t) | .number]' "$out/board-e.json")" = '[3]' ] &&
  ok "E: the observer's board holds one work item \"$title_e\", number 3" ||
  bad "E: the board after the request: $(jq -c '[.items[] | {number, title}]' "$out/board-e.json")"

# ---------------------------------------------------------------------------------------------------------------
# 10. Legs F and G: a hidden list (work item 269)
# ---------------------------------------------------------------------------------------------------------------
say ""
observer "$brigade" work-item hide-list Done > "$out/observer-hide.out" || die "the observer could not hide the list Done"
ok "the observer hid a list: $(cat "$out/observer-hide.out")"
say "leg F: /brigade:work-items"
session hidden 'Skill' 6 "/brigade:work-items"
commands hidden > "$out/hidden.commands.txt"
answer hidden > "$out/hidden.answer.txt"
[ "$(cat "$out/hidden.commands.txt")" = "brigade work-item list" ] &&
  ok "F: the session ran exactly one command: brigade work-item list" ||
  bad "F: the session's commands: $(cat "$out/hidden.commands.txt")"
{ grep -qF 'open by list: Todo (2), Doing (0); not shown: Done (1); --all shows them' "$out/hidden.answer.txt" && ! grep -qF "$title_c" "$out/hidden.answer.txt"; } &&
  ok "F: the answer leaves the hidden list's work item out and carries the line that says so: $(grep 'not shown' "$out/hidden.answer.txt" | head -1)" ||
  bad "F: the answer is not the listing without the hidden list: $(head -c 600 "$out/hidden.answer.txt")"
say "leg F: /brigade:work-items all"
session all 'Skill' 6 "/brigade:work-items all"
commands all > "$out/all.commands.txt"
answer all > "$out/all.answer.txt"
[ "$(cat "$out/all.commands.txt")" = "brigade work-item list --all" ] &&
  ok "F: with \`all\` the session ran exactly one command: brigade work-item list --all" ||
  bad "F: the session's commands with \`all\`: $(cat "$out/all.commands.txt")"
{ grep -qF "$title_c" "$out/all.answer.txt" && grep -qF 'open by list: Todo (2), Doing (0), Done (1)' "$out/all.answer.txt" && ! grep -q 'not shown' "$out/all.answer.txt"; } &&
  ok "F: the answer shows every list, the hidden one's work item too" ||
  bad "F: the answer with \`all\` is not every list: $(head -c 600 "$out/all.answer.txt")"

say ""
say "leg G: $asked_g"
session ask 'Skill,Bash(brigade work-item:*)' 10 "$asked_g"
commands ask > "$out/ask.commands.txt"
answer ask > "$out/ask.answer.txt"
grep -Eq -- '^brigade work-item (list .*--(all|list)|show 2)' "$out/ask.commands.txt" &&
  ok "G: asked about a work item that is in the hidden list, the session read past the default listing: $(tr '\n' ';' < "$out/ask.commands.txt")" ||
  bad "G: the session read only the default listing: $(tr '\n' ';' < "$out/ask.commands.txt")"
{ grep -qi 'yes' "$out/ask.answer.txt" && grep -qw '2' "$out/ask.answer.txt"; } &&
  ok "G: it answered yes, work item 2: $(tr '\n' ' ' < "$out/ask.answer.txt" | cut -c1-200)" ||
  bad "G: it did not find work item 2: $(head -c 400 "$out/ask.answer.txt")"

# ---------------------------------------------------------------------------------------------------------------
# 11. Nothing went to the backend
# ---------------------------------------------------------------------------------------------------------------
say ""
if grep -rqF -e "$title_a" -e "$title_c" -e "$title_e" "$state_dir/fs-adapter" 2>/dev/null; then
  bad "a work item's title is in the backend's files under $state_dir/fs-adapter"
else
  ok "no work item is in the backend's files: the board is the folder alone ($(find "$state_dir/boards" -name '*.json' | wc -l | tr -d ' ') change files under boards/)"
fi

say ""
say "E18: $failures assertion(s) failed"
exit "$failures"
