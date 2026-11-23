#!/bin/sh
# usage: scripts/proof-live.sh          (run from the repository root after `make build`)
#        make proof                     (the supported entry point: proof.sh, then this)
#
# The live proof (work item 415): a message wakes an idle Claude Code session, and the session answers it.
#
# One interactive Claude Code session, started in tmux with the plugin of THIS checkout and the binary `make build`
# wrote, sits idle in a throwaway project whose team lives on the fs adapter (a local directory, no backend and no
# Docker). A second session of another member, driven by the real `brigade hook` subcommands with a sleeper as
# CLAUDE_PID, sends it one message. The message asks for one word that it never spells: the word WAKE, a hyphen
# and four random characters, named apart. So the joined word in the team's store can only be the woken session's
# own answer, and the stored reply names the message it answers.
#
# What it shows: the session registers with an inbox socket, its watcher runs, a message is accepted for it while
# it is idle, and without any key pressed in it the session reads the message and replies through `brigade send`.
# What it does not show: any backend but the fs adapter, any harness but Claude Code, a second message, a crash,
# a resume, or what a model does with an adversarial message. The three proofs that measured some of those in
# September 2026 (proof-headless.sh, proof-idle-wake.sh, proof-crash-resume.sh) are retired from `make proof`;
# docs/developer/build-and-test/proofs.md says why and what each showed.
#
# It needs a logged-in Claude Code, tmux, jq and git, and costs a few model calls, so it never runs in CI.
# Everything Brigade touches is under one temporary root (its own XDG_CONFIG_HOME, XDG_STATE_HOME and
# XDG_DATA_HOME, its own project), removed at the end. The user's real CLAUDE_CONFIG_DIR is kept for the `claude`
# run alone (the login lives there): the Brigade plugin installed there is switched off for this run, and the
# transcript it writes under it is removed by name.
#
# Output: one `ok:`/`FAIL:` line per assertion, then the teardown. The evidence (what was sent, the pane before
# and after) is kept under .ignored/proof-live/<UTC stamp>/, which git ignores. Exit status is the number of
# failed assertions, 64 when the rig itself fails. PROOF_LIVE_KEEP=1 keeps the temporary root.
# shellcheck disable=SC2015  # every `A && ok || bad` below: ok never fails, so bad runs only when A does not
set -eu
umask 077

alice='alice@proof.invalid'
bob='bob@proof.invalid'
team_name=live-proof
register_wait=90   # seconds the interactive session may take to register
reply_wait=240     # seconds the idle session may take to answer

say() {
  printf '%s\n' "$*"
  if [ -n "${summary:-}" ]; then printf '%s\n' "$*" >>"$summary"; fi
}
die() { printf 'proof-live.sh: %s\n' "$1" >&2; exit 64; }
failures=0
ok() { say "ok: $*"; }
bad() { failures=$((failures + 1)); say "FAIL: $*"; }
now() { date +%s; }

# ---------------------------------------------------------------------------------------------------------------
# 1. Preconditions
# ---------------------------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd -P) || die "cannot resolve the repository root"
[ -d "$repo/plugin/skills/team-messaging" ] || die "no plugin/skills/team-messaging at $repo"
brigade=$repo/bin/brigade
fs_adapter=$repo/bin/brigade-adapter-fs
[ -x "$brigade" ] || die "missing $brigade: run \`make build\` first"
[ -x "$fs_adapter" ] || die "missing $fs_adapter: run \`make build\` first"
for tool in jq git tmux claude; do
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
out=$repo/.ignored/proof-live/$stamp
mkdir -p "$out"
summary=$out/summary.txt
: > "$summary"

rig=$(mktemp -d "${TMPDIR:-/tmp}/brigade-live-XXXXXX") || die "cannot create a temporary directory"
rig=$(CDPATH='' cd -- "$rig" && pwd -P)
case $rig in */brigade-live-*) ;; *) die "refusing to work in an unexpected temp root: $rig" ;; esac
rig_name=$(basename "$rig")
chmod 700 "$rig"

xdg_config=$rig/xdg/config
xdg_state=$rig/xdg/state
xdg_data=$rig/xdg/data
proj=$rig/proj                  # the sessions' working directory: a git checkout with .brigade.json
sender_cfg=$rig/claude-sender   # the sender's throwaway Claude config dir (never the user's)
sender_brigade=$rig/cfg-sender  # the sender's own credential store: a second member of the team
plugin_root=$rig/plugin         # the sender's plugin root: bin/brigade is the built binary
state_dir=$xdg_state/brigade
secret_file=$rig/join.secret
mkdir -p "$xdg_config/brigade" "$xdg_state" "$xdg_data" "$proj" "$sender_cfg" "$sender_brigade" "$plugin_root/bin"
ln -s "$brigade" "$plugin_root/bin/brigade"
cp "$repo/plugin/bin/VERSION" "$plugin_root/bin/VERSION"

printf '%s\n' "$brigade" > "$xdg_config/brigade/dev-binary"
printf '{"fs": ["%s"]}\n' "$fs_adapter" > "$xdg_config/brigade/adapters.json"
cp "$xdg_config/brigade/adapters.json" "$sender_brigade/adapters.json"

sleeper=''
tmux_sock=brigade-live-$$

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

# shellcheck disable=SC2329,SC2317  # run by the EXIT trap below
cleanup() {
  st=$?
  set +e
  tmux -L "$tmux_sock" kill-server 2>/dev/null
  # A watcher that is told to stop detaches from the sync engine, and the last one to go stops it.
  if [ -d "$state_dir/watchers" ]; then
    for pf in "$state_dir"/watchers/*.json; do
      [ -f "$pf" ] || continue
      wpid=$(jq -r '.pid // empty' "$pf" 2>/dev/null)
      case $wpid in ''|*[!0-9]*) continue ;; esac
      kill_wait "$wpid" "the watcher from $pf"
    done
  fi
  [ -n "$sleeper" ] && kill_wait "$sleeper" "the sender's sleeper"
  # The transcript the run wrote under the user's real Claude config dir: only a directory under its projects/
  # whose name carries this rig's own name is removed.
  for td in "$claude_cfg"/projects/*"$rig_name"*; do
    [ -d "$td" ] || continue
    say "teardown: removing the transcript directory $td"
    rm -rf "$td"
  done
  if [ -z "${PROOF_LIVE_KEEP:-}" ]; then
    case $rig in
      */brigade-live-*) say "teardown: removing the temp root $rig"; rm -rf "$rig" ;;
      *) say "teardown: NOT removing '$rig' (unexpected name)" ;;
    esac
  else
    say "teardown: kept $rig"
  fi
  say "evidence: $out"
  exit "$st"
}
# A signal ends the run with a failing status, and the EXIT trap then tears down once.
trap cleanup EXIT
trap 'exit 130' HUP INT TERM

# ---------------------------------------------------------------------------------------------------------------
# 4. Helpers that run a command with the stripped, temporary environment
# ---------------------------------------------------------------------------------------------------------------
# terminal: a human's terminal outside any session, on the rig's XDG dirs.
# shellcheck disable=SC2086  # $strip_args is a computed `-u NAME` list; names carry no whitespace (section 1)
terminal() {
  env $strip_args CLAUDE_CONFIG_DIR="$sender_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" "$@"
}
# sender: the facts the harness reads inside a session, for the sender's hook and its commands. It is the second
# member, so its credential store is its own (the option config_dir).
# shellcheck disable=SC2086  # as above
sender() {
  (cd "$proj" && env $strip_args CLAUDE_CONFIG_DIR="$sender_cfg" \
    XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" \
    CLAUDE_PID="$sleeper" CLAUDE_CODE_SESSION_ID="proof-sender-$sleeper" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli \
    CLAUDE_PLUGIN_ROOT="$plugin_root" CLAUDE_PLUGIN_OPTION_CONFIG_DIR="$sender_brigade" "$@")
}
pane() { tmux -L "$tmux_sock" capture-pane -p -t live -S -400 2>/dev/null || true; }
maps() { find "$state_dir/sessions/by-pid" -name '*.json' ! -name "$sleeper.json" ! -name '*.start.json' 2>/dev/null; }

# ---------------------------------------------------------------------------------------------------------------
# 5. The project, the team of two members, the sender's session
# ---------------------------------------------------------------------------------------------------------------
say "proof-live.sh: temp root $rig"
say "proof-live.sh: evidence  $out"
say "proof-live.sh: claude $(claude --version 2>/dev/null | head -1); brigade $("$brigade" version 2>/dev/null | head -1)"

git -C "$proj" init -q
printf '# Lanternfish\n\nA small command-line tool, used here as a project with nothing in it.\n' > "$proj/README.md"
# Alice, whose credential store is the rig's default one, creates the team: the interactive session is hers.
(cd "$proj" && terminal "$brigade" team create --adapter fs --url http://127.0.0.1:1 --key placeholder \
  --name "$team_name" --label "$alice" --secret-file "$secret_file") >"$out/team-create.out" 2>"$out/team-create.err" ||
  die "team create failed: $(cat "$out/team-create.err")"
[ -f "$proj/.brigade.json" ] || die "team create wrote no .brigade.json"
ok "team \"$team_name\" created by $alice on the fs adapter"
# Bob joins with the secret file from inside his own session, into a store of his own (the option config_dir):
# the sender is another member, as a teammate is. A session joins after its start hook has run once, which
# writes the facts the join goes by, and the start after the join registers it.
sleep 3600 &
sleeper=$!
sender_start() {  # sender_start <capture stem>
  jq -cn --arg s "proof-sender-$sleeper" --arg c "$proj" \
    '{session_id:$s,cwd:$c,hook_event_name:"SessionStart",transcript_path:"/never/read.jsonl",source:"startup"}' |
    sender "$brigade" hook session-start >"$out/$1.out" 2>"$out/$1.err"
}
sender_start sender-start-unjoined
sender "$brigade" team join --secret-file "$secret_file" --label "$bob" </dev/null \
  >"$out/team-join.out" 2>"$out/team-join.err" || die "team join failed: $(cat "$out/team-join.err")"
rm -f "$secret_file"
ok "$bob joined it with the secret file, which is deleted"
sender_start sender-start
sender_map=$state_dir/sessions/by-pid/$sleeper.json
[ -f "$sender_map" ] || die "the hook wrote no by-pid map for the sender (stderr: $(cat "$out/sender-start.err"))"
sender_id=$(jq -r '.brigade_session_id // empty' "$sender_map")
[ -n "$sender_id" ] && ok "the sender's session is registered through the real hook, with no inbox socket and so no watcher" ||
  die "the sender's by-pid map carries no brigade_session_id"

# ---------------------------------------------------------------------------------------------------------------
# 6. The live session: started, idle, sent one message
# ---------------------------------------------------------------------------------------------------------------
say "live: an interactive Claude Code session, idle, sent one message"
# shellcheck disable=SC2086  # as above
tmux -L "$tmux_sock" new-session -d -s live -x 200 -y 50 -c "$proj" \
  env $strip_args XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" DISABLE_AUTOUPDATER=1 \
  claude -n proof-live --no-chrome --plugin-dir "$repo/plugin" --settings '{"enabledPlugins":{"brigade@brigade":false}}' \
  --permission-mode default --allowedTools 'Bash(brigade:*)' ||
  die "tmux could not start the interactive session"
# The folder-trust question: the cursor is moved to the yes before Enter.
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
  [ -n "$(maps)" ] && break
  sleep 2
done
_deadline=$(( $(now) + register_wait ))
live_map=''
while [ "$(now)" -lt "$_deadline" ]; do
  live_map=$(maps | head -1)
  [ -n "$live_map" ] && break
  sleep 2
done
if [ -z "$live_map" ]; then
  pane > "$out/live.pane.txt"
  die "the interactive session did not register within ${register_wait}s (the pane is in $out/live.pane.txt)"
fi
live_id=$(jq -r '.brigade_session_id // empty' "$live_map")
[ -n "$live_id" ] || die "the interactive session's by-pid map carries no brigade_session_id"
socket=$(jq -r '.socket_path // empty' "$live_map")
harness=$(jq -r '.harness // "claude-code"' "$live_map")
[ -n "$socket" ] && ok "live: the session $(printf '%s' "$live_id" | tail -c 5) is registered, harness $harness, with an inbox socket" ||
  bad "live: the session's map names no inbox socket: $(jq -c '{harness, socket_path}' "$live_map")"
[ "$live_id" != "$sender_id" ] && ok "live: it is another session than the sender's" || bad "live: the live session and the sender are one session"
# Its watcher: the line the detached `brigade watch` logs when it is ready.
_deadline=$(( $(now) + 60 ))
watcher=no
while [ "$(now)" -lt "$_deadline" ]; do
  if grep -rqs 'watch ready' "$state_dir" 2>/dev/null; then watcher=yes; break; fi
  sleep 2
done
[ "$watcher" = yes ] && ok "live: the session's watcher says \`watch ready\`" || bad "live: no \`watch ready\` line under the state directory after 60s"
sleep 5
pane > "$out/live.pane-idle.txt"

tail=$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')
printf 'This is a delivery check and needs no work. Please reply to this message with one word and nothing else: the word WAKE, a hyphen, and then these four characters: %s' "$tail" |
  sender "$brigade" send "$live_id" --summary "delivery check" --json >"$out/send.json" 2>"$out/send.err" ||
  die "the sender's send failed: $(cat "$out/send.err")"
sent_id=$(jq -r '.result.message_id // empty' "$out/send.json")
[ -n "$sent_id" ] || die "the send printed no message id: $(cat "$out/send.json")"
ok "live: the sender's message is accepted: $(jq -c '.result | {message_id, recipient: .recipient.state}' "$out/send.json")"
if grep -rqs "WAKE-$tail" "$rig"; then bad "live: the joined word is in the rig before any reply; the check proves nothing"; fi

_deadline=$(( $(now) + reply_wait ))
woke=no
while [ "$(now)" -lt "$_deadline" ]; do
  if grep -rqs "WAKE-$tail" "$rig"; then woke=yes; break; fi
  sleep 5
done
sleep 5
pane > "$out/live.pane-after.txt"
if [ "$woke" = yes ]; then
  reply=$(grep -rls "WAKE-$tail" "$rig" | head -1)
  ok "live: the idle session was woken by the message and replied WAKE-$tail; the reply is in the team's store"
  cp "$reply" "$out/reply.json" 2>/dev/null || true
  grep -qs "$sent_id" "$reply" && ok "live: the stored reply names the message it answers ($sent_id)" ||
    bad "live: the stored reply does not name the message it answers ($sent_id): see $out/reply.json"
elif grep -q 'Do you want to proceed' "$out/live.pane-after.txt"; then
  bad "live: no reply after ${reply_wait}s: the session waits at a permission prompt (the pane is in $out/live.pane-after.txt)"
else
  bad "live: no reply WAKE-$tail after ${reply_wait}s (the pane is in $out/live.pane-after.txt)"
fi

tmux -L "$tmux_sock" send-keys -t live -l '/exit'
sleep 1
tmux -L "$tmux_sock" send-keys -t live Enter
sleep 5

say "failures: $failures"
exit "$failures"
