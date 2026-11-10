#!/bin/sh
# usage: scripts/experiments/E16-held-decision.sh      (run from the repository root after `make build`)
#
# The live proof of card 236 (a decision for the asking session's own user is asked in the conversation, and its
# ask is sent only after their wait), with no model and nothing sent to anyone: one session of one member in a
# throwaway checkout, a throwaway team on the fs adapter (a local directory, no backend), the REAL `brigade hook`
# subcommands driven as Claude Code drives them (a sleeper as CLAUDE_PID), the real `brigade decision` and
# `brigade report`, a real watcher in sink mode, and a session with the Slack gateway's harness registered on the
# same store, whose inbox is what "Slack" received. Proves:
#   A  an ask for the session's own user is recorded and HELD (the command says so and when it would go out, the
#      gateway gets nothing, no digest lists it), and the watcher sends it once the member's wait has passed with
#      no prompt in the conversation — the question and its options under the gateway's `to:` line — and tells the
#      session at its next prompt;
#   B  a prompt in the conversation after the ask ends the hold for good: nothing is sent, however long one waits,
#      the digest lists the decision as asked in the conversation, and the session closes it;
#   C  an ask for anyone else goes out at once, as before;
#   D  the wait is the member's own (`brigade report subscribe --wait`): ten minutes when unset, and zero sends an
#      ask for the session's own user at once;
#   E  the Stop hook's decision sentence says the session's own user is asked in the conversation first.
#
# Everything is under one temporary root, removed at the end; the real ~/.config/brigade and ~/.local/state/brigade
# are never read or written, and no gateway, mailbox or Slack workspace is involved. Syncthing is not needed: the
# watcher's sync goroutine ends when it finds no engine, and the state goroutine runs alone.
#
# Output: one `ok:`/`FAIL:` line per assertion, the measured waits, and the teardown. Exit status is the number of
# failed assertions, 64 when the rig itself fails. E16_KEEP=1 keeps the root. It takes about four minutes: the
# watcher looks at its held asks every 20 s, and the Stop sentence needs a turn longer than a minute.
# shellcheck disable=SC2015  # every `A && ok || bad` below: ok never fails, so bad runs only when A does not
set -eu
umask 077
say() { printf '%s\n' "$*"; }
die() { printf 'E16: %s\n' "$1" >&2; exit 64; }
failures=0
ok() { printf 'ok: %s\n' "$*"; }
bad() { failures=$((failures + 1)); printf 'FAIL: %s\n' "$*"; }
now_s() { date +%s; }

# --- 1. preconditions ---------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P) || die "cannot resolve the repository root"
brigade=$repo/bin/brigade
fs_adapter=$repo/bin/brigade-adapter-fs
[ -x "$brigade" ] || die "missing $brigade: run make build"
[ -x "$fs_adapter" ] || die "missing $fs_adapter: run make build"
for tool in jq git; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done
alice='alice@held.invalid'
bob='bob@held.invalid'
wait_s=20     # the member's wait in legs A and B
tick_s=20     # the watcher's check interval (watch.DefaultStateCheckInterval)

# The inherited session environment is stripped by prefix (gates.md), every BRIGADE_* with it.
strip_args=''
for n in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(BRIGADE_[0-9A-Za-z_]*\)=.*/\1/p'); do
  strip_args="$strip_args -u $n"
done

# --- 2. the temporary machine --------------------------------------------------------------------------------------
root=$(mktemp -d "${TMPDIR:-/tmp}/brigade-e16-XXXXXX") || die "mktemp"
root=$(CDPATH='' cd -- "$root" && pwd -P)
case $root in */brigade-e16-*) ;; *) die "refusing to work in an unexpected temp root: $root" ;; esac
say "root: $root"
xdg_config=$root/xdg/config; xdg_state=$root/xdg/state; xdg_data=$root/xdg/data; xdg_cache=$root/xdg/cache
cfg=$xdg_config/brigade; state_dir=$xdg_state/brigade
home=$root/home; claude_cfg=$root/claude-cfg; cap=$root/cap; proj=$root/proj; plugin_root=$root/plugin
mkdir -p "$cfg" "$xdg_state" "$xdg_data" "$xdg_cache" "$home" "$claude_cfg" "$cap" "$proj" "$plugin_root/bin"
ln -s "$brigade" "$plugin_root/bin/brigade" && cp "$repo/plugin/bin/VERSION" "$plugin_root/bin/VERSION"
# The fs adapter, registered by name: `team create --adapter fs` and every later resolution read this.
printf '{"fs": ["%s"]}\n' "$fs_adapter" > "$cfg/adapters.json"
secret_file=$root/join.secret

sleep 100000 & sleeper=$!
watcher_pid=''
# shellcheck disable=SC2329,SC2317  # run by the EXIT trap
cleanup() {
  st=$?
  set +e
  [ -n "$watcher_pid" ] && kill "$watcher_pid" 2>/dev/null
  kill "$sleeper" 2>/dev/null
  if [ -z "${E16_KEEP:-}" ]; then
    case $root in */brigade-e16-*) rm -rf "$root"; say "teardown: removed $root" ;; esac
  else
    say "teardown: kept $root"
  fi
  exit "$st"
}
trap cleanup EXIT HUP INT TERM

# terminal: a human's shell outside any session, on the rig's directories.
# shellcheck disable=SC2086  # $strip_args is a computed `-u NAME` list; names carry no whitespace
terminal() {
  env $strip_args HOME="$home" XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" XDG_CACHE_HOME="$xdg_cache" \
    CLAUDE_CONFIG_DIR="$claude_cfg" PATH="$PATH" "$@"
}
# hook <sub> <stdin json>: one hook as Claude Code runs it, captured under a name of its own.
hook_n=0
# shellcheck disable=SC2086
hook() {
  hook_n=$((hook_n + 1)); hook_out=$cap/hook-$hook_n.$1.out
  printf '%s' "$2" | (cd "$proj" && env $strip_args HOME="$home" XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" XDG_CACHE_HOME="$xdg_cache" \
    CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$sleeper" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli CLAUDE_PLUGIN_ROOT="$plugin_root" \
    CLAUDE_PLUGIN_OPTION_LABEL="$alice" PATH="$PATH" "$brigade" hook "$1" --log-level debug) >"$hook_out" 2>"$cap/hook-$hook_n.$1.err" || true
}
# cmd <args...>: a Bash-tool command inside the session.
# shellcheck disable=SC2086
cmd() {
  (cd "$proj" && env $strip_args HOME="$home" XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" XDG_CACHE_HOME="$xdg_cache" \
    CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$sleeper" CLAUDECODE=1 PATH="$PATH" "$brigade" "$@")
}
native=$(uuidgen | tr '[:upper:]' '[:lower:]')
doc() { jq -cn --arg id "$native" --arg cwd "$proj" --arg ev "$1" '{session_id:$id, cwd:$cwd, hook_event_name:$ev, source:"startup", session_title:"held-proof", permission_mode:"bypassPermissions", transcript_path:"/never/read.jsonl", stop_hook_active:false}'; }
wait_grep() { _f=$1; _n=$2; _b=$3; _t=$(now_s); while [ $(( $(now_s) - _t )) -lt "$_b" ]; do if [ -f "$_f" ] && grep -q "$_n" "$_f"; then return 0; fi; sleep 1; done; return 1; }
count_grep() { if [ -f "$1" ]; then grep -c "$2" "$1" || true; else printf '0'; fi; }

# --- 3. the team, the checkout, the session -------------------------------------------------------------------------
git -C "$proj" init -q -b master
git -C "$proj" config user.email "$alice"
git -C "$proj" config user.name "Proof"
(cd "$proj" && terminal "$brigade" team create --adapter fs --url http://127.0.0.1:1 --key placeholder \
  --name held-proof --label "$alice" --secret-file "$secret_file") >"$cap/team-create.out" 2>"$cap/team-create.err" ||
  die "team create failed: $(cat "$cap/team-create.err")"
[ -f "$proj/.brigade.json" ] || die "team create wrote no .brigade.json"
jq '. + {"sync": {"folders": [".brigade"], "state": ".brigade/state"}}' "$proj/.brigade.json" > "$proj/.brigade.json.tmp" && mv "$proj/.brigade.json.tmp" "$proj/.brigade.json"
printf '.brigade\n' > "$proj/.gitignore"
git -C "$proj" add -A && git -C "$proj" commit -q -m "0: the throwaway checkout"
ok "team held-proof created on the fs adapter by $alice; the checkout names .brigade/state as its state folder"

hook session-start "$(doc SessionStart)"
say "session-start: $(head -1 "$hook_out")"
map=$state_dir/sessions/by-pid/$sleeper.json
[ -f "$map" ] || die "the hook wrote no by-pid map (stderr: $(cat "$cap/hook-$hook_n.session-start.err"))"
sid=$(jq -r .brigade_session_id "$map"); key=$(jq -r .team_key "$map")
folder=$(jq -r '.state_folder // empty' "$map")
[ -n "$folder" ] || die "the map names no state folder: $(jq -c '{sync_adapter, sync_folders, state_folder}' "$map")"
hook prompt "$(doc UserPromptSubmit)"
[ -f "$folder/sessions/$sid.json" ] && [ "$(jq -r .member "$folder/sessions/$sid.json")" = "$alice" ] && ok "the session's entry is in the state folder with the member's label" || bad "no entry, or another member"

# The watcher, as the hook would spawn it, in sink mode (a sleeper has no inbox socket).
adapter_json=$(jq -c .adapter_command "$map")
# shellcheck disable=SC2086
env $strip_args HOME="$home" XDG_CONFIG_HOME="$xdg_config" XDG_STATE_HOME="$xdg_state" XDG_DATA_HOME="$xdg_data" XDG_CACHE_HOME="$xdg_cache" \
  CLAUDE_CONFIG_DIR="$claude_cfg" BRIGADE_CLAUDE_PID="$sleeper" BRIGADE_PROFILE="$key" BRIGADE_CONFIG_DIR="$cfg" BRIGADE_STATE_DIR="$state_dir" \
  BRIGADE_ADAPTER_COMMAND="$adapter_json" BRIGADE_TEAM_INBOUND=accept BRIGADE_LOG_LEVEL=debug PATH="$PATH" \
  "$brigade" watch --sink "$root/sink" </dev/null >"$cap/watch.out" 2>"$cap/watch.err" &
watcher_pid=$!
wlog=$state_dir/logs/watcher-$sleeper.log
wait_grep "$wlog" '"msg":"watch ready"' 60 && ok "the session's watcher is ready" || die "the watcher never became ready (see $cap/watch.err)"

# adapter <group> <verb> [flags]: the fs adapter as the member's own client runs it (stdin is the request).
adapter() { env BRIGADE_PROFILE="$key" BRIGADE_CONFIG_DIR="$cfg" BRIGADE_STATE_DIR="$state_dir" "$fs_adapter" "$@"; }
# A session with the Slack gateway's harness on the same store: its inbox is what Slack would have received.
printf '{"harness":"gateway-slack","harness_version":"0.1.0","session_name":"slack-gateway","activity":"idle","inbound":"accept","lease_seconds":600}' |
  adapter session register >"$cap/gateway-register.out" 2>"$cap/gateway-register.err" || die "the gateway session did not register: $(cat "$cap/gateway-register.err")"
gw=$(jq -r '.result.session_id // empty' "$cap/gateway-register.out")
[ -n "$gw" ] || die "no gateway session id in $(cat "$cap/gateway-register.out")"
# gateway_inbox <file>: the decision asks the gateway session holds, as a JSON array. Nothing is acknowledged, so
# each read sees them all. The members are subscribed, so the watcher also produces their digests, which go to
# the same gateway and are left out here (asks.digests counts them).
gateway_inbox() {
  adapter message receive --session "$gw" --limit 50 </dev/null >"$1.raw" 2>"$1.err" || true
  jq -c '{asks: [.result.messages[] | select(.body | contains("Brigade digest:") | not)], digests: [.result.messages[] | select(.body | contains("Brigade digest:"))]}' "$1.raw" >"$1" 2>/dev/null || printf '{"asks":[],"digests":[]}\n' >"$1"
}
inbox_count() { gateway_inbox "$1"; jq -r '.asks | length' "$1"; }
ok "a gateway-slack session is on the roster ($gw)"

# The subscriptions: alice's own, by her own session, with a short wait; bob's, as his session would write it.
cmd report subscribe --recipients "@alice" --wait "${wait_s}s" >"$cap/subscribe.out" 2>&1 || bad "subscribe refused: $(cat "$cap/subscribe.out")"
say "subscribe: $(cat "$cap/subscribe.out")"
grep -q "an ask from your own session held for ${wait_s}s" "$cap/subscribe.out" && [ "$(jq -r '.subscribers[0].wait' "$folder/reports.json")" = "${wait_s}s" ] && ok "D: subscribe --wait ${wait_s}s wrote the member's wait into reports.json" || bad "D: the wait was not written: $(cat "$folder/reports.json")"
jq -c --arg l "$bob" '.subscribers += [{"label": $l, "recipients": ["@bob"]}]' "$folder/reports.json" > "$folder/reports.json.tmp" && mv "$folder/reports.json.tmp" "$folder/reports.json"

# --- 4. leg A: held, then sent after the wait ------------------------------------------------------------------------
t_ask=$(now_s)
printf 'E16 leg A: ship now, or wait?\n' | cmd decision --to "$alice" --options "ship now | wait" >"$cap/a.decision.out" 2>&1 || true
say "A: $(tr '\n' '|' < "$cap/a.decision.out")"
id_a=$(sed -n 's/^recorded: decision \(.*\)$/\1/p' "$cap/a.decision.out")
[ -n "$id_a" ] || die "leg A recorded no decision: $(cat "$cap/a.decision.out")"
grep -q "^held: $alice is this session's own user, so ask them here in the conversation; it is sent to them at .* UTC (in ${wait_s}s) only if no prompt arrives here and it is still open" "$cap/a.decision.out" && ok "A: the command says the ask is held, for whom, and when it would go out" || bad "A: no held line"
grep -q '^sent:' "$cap/a.decision.out" && bad "A: the command says it sent the ask" || ok "A: the command sent nothing"
[ "$(jq -r '.held_until // empty' "$folder/decisions/$id_a.json")" != "" ] && [ "$(jq -r '.sent // empty' "$folder/decisions/$id_a.json")" = "" ] && ok "A: the decision file carries held_until and no sent way" || bad "A: the decision file: $(cat "$folder/decisions/$id_a.json")"
[ "$(inbox_count "$cap/a.inbox-before.json")" = 0 ] && ok "A: the gateway has nothing while the hold runs" || bad "A: the gateway already holds a message"
cmd report show >"$cap/a.show-during.out" 2>&1 || true
grep -q 'E16 leg A' "$cap/a.show-during.out" && bad "A: the digest lists the decision while its hold runs" || ok "A: the digest leaves the decision out while its hold runs"
wait_grep "$wlog" '"msg":"decision: held ask sent"' $((wait_s + 2 * tick_s + 10)) && ok "A: the watcher sent the held ask $(( $(now_s) - t_ask )) s after it was recorded (the wait is ${wait_s} s, the check every ${tick_s} s)" || bad "A: the watcher never sent the held ask (see $wlog)"
[ $(( $(now_s) - t_ask )) -ge "$wait_s" ] && ok "A: not before the wait had passed" || bad "A: it went out before the wait had passed"
gateway_inbox "$cap/a.inbox-after.json"
[ "$(jq -r '.asks | length' "$cap/a.inbox-after.json")" = 1 ] && ok "A: the gateway received one ask" || bad "A: the gateway holds $(jq -r '.asks | length' "$cap/a.inbox-after.json") asks"
body_a=$(jq -r '.asks[0].body' "$cap/a.inbox-after.json")
want_a=$(printf 'to: @alice\n\nE16 leg A: ship now, or wait?\n\n- ship now\n- wait')
[ "$body_a" = "$want_a" ] && [ "$(jq -r '.asks[0].summary' "$cap/a.inbox-after.json")" = "E16 leg A: ship now, or wait?" ] && ok "A: it is the question and its options under the gateway's to: line, the question as its summary" || bad "A: the message: $(jq -c '.asks[0] | {body, summary}' "$cap/a.inbox-after.json")"
# The digest the watcher produced while the hold ran went to the same gateway: it lists no decision.
jq -e '.digests | length > 0 and all(.[]; .body | contains("E16 leg A") | not)' "$cap/a.inbox-after.json" >/dev/null && ok "A: the digests produced meanwhile ($(jq -r '.digests | length' "$cap/a.inbox-after.json")) do not carry the held decision" || say "note: no digest was produced during leg A, or one carries the decision: $(jq -c '[.digests[].summary]' "$cap/a.inbox-after.json")"
[ "$(jq -r .sent "$folder/decisions/$id_a.json")" = slack ] && [ "$(jq -r .sent_to "$folder/decisions/$id_a.json")" = "@alice" ] && ok "A: the decision file records how it went" || bad "A: the decision file after the send: $(cat "$folder/decisions/$id_a.json")"
hook prompt "$(doc UserPromptSubmit)"
grep -q "Brigade decision $id_a: your user did not answer here within their wait, so it was sent to them through the slack gateway" "$hook_out" && ok "A: the next prompt tells the session the ask went out" || bad "A: the next prompt's lines: $(cat "$hook_out")"
printf 'ship now: the proof closed it\n' | cmd decision --close "$id_a" >"$cap/a.close.out" 2>&1 || true

# --- 5. leg B: a prompt in the conversation ends the hold -------------------------------------------------------------
sleep 1
t_ask=$(now_s)
printf 'E16 leg B: which name, a or b?\n' | cmd decision --to "$alice" --options "a | b" >"$cap/b.decision.out" 2>&1 || true
say "B: $(tr '\n' '|' < "$cap/b.decision.out")"
id_b=$(sed -n 's/^recorded: decision \(.*\)$/\1/p' "$cap/b.decision.out")
[ -n "$id_b" ] && grep -q '^held: ' "$cap/b.decision.out" && ok "B: a second ask for the session's own user is held" || die "leg B recorded no held decision: $(cat "$cap/b.decision.out")"
sleep 2
hook prompt "$(doc UserPromptSubmit)"   # the user answers in the conversation
t_prompt=$(now_s)
wait_grep "$wlog" '"msg":"decision: hold ended with nothing sent","how":"prompt"' $((tick_s + 10)) && ok "B: the watcher ended the hold at the prompt ($(( $(now_s) - t_prompt )) s after it)" || bad "B: the hold did not end at the prompt (see $wlog)"
[ "$(jq -r .hold_ended "$folder/decisions/$id_b.json")" = prompt ] && ok "B: the decision file records that a prompt ended the hold" || bad "B: the decision file: $(cat "$folder/decisions/$id_b.json")"
# Past the wait and two checks more: nothing goes out.
while [ $(( $(now_s) - t_ask )) -lt $((wait_s + 2 * tick_s + 5)) ]; do sleep 1; done
[ "$(count_grep "$wlog" '"msg":"decision: held ask sent"')" = 1 ] && [ "$(inbox_count "$cap/b.inbox.json")" = 1 ] && ok "B: $(( $(now_s) - t_ask )) s after the ask, past the wait, nothing more was sent" || bad "B: something was sent: $(count_grep "$wlog" 'held ask sent') sends, $(inbox_count "$cap/b.inbox2.json") messages"
cmd report show >"$cap/b.show.out" 2>&1 || true
grep -A4 'E16 leg B' "$cap/b.show.out" | grep -q 'asked in the conversation' && ok "B: the digest lists it as open, asked in the conversation" || bad "B: the digest: $(cat "$cap/b.show.out")"
printf 'a: answered in the conversation\n' | cmd decision --close "$id_b" >"$cap/b.close.out" 2>&1 || true
grep -q "^closed: decision $id_b" "$cap/b.close.out" && ok "B: the session closed it with the outcome" || bad "B: the close: $(cat "$cap/b.close.out")"

# --- 6. leg C: anyone else, at once -----------------------------------------------------------------------------------
printf 'E16 leg C: for another member\n' | cmd decision --to "$bob" >"$cap/c.decision.out" 2>&1 || true
say "C: $(tr '\n' '|' < "$cap/c.decision.out")"
grep -q "^sent: to $bob at @bob through the slack gateway" "$cap/c.decision.out" && [ "$(inbox_count "$cap/c.inbox.json")" = 2 ] && ok "C: an ask for another member went out at once, through the gateway" || bad "C: not sent at once: $(cat "$cap/c.decision.out")"

# --- 7. leg D: the default wait, and a wait of zero ---------------------------------------------------------------------
cmd report subscribe --recipients "@alice" >"$cap/d.subscribe-default.out" 2>&1 || true
grep -q 'an ask from your own session held for 10m0s' "$cap/d.subscribe-default.out" && ok "D: a subscription that names no wait holds for ten minutes" || bad "D: $(cat "$cap/d.subscribe-default.out")"
printf 'E16 leg D: the default wait\n' | cmd decision --to "$alice" >"$cap/d.decision-default.out" 2>&1 || true
id_d=$(sed -n 's/^recorded: decision \(.*\)$/\1/p' "$cap/d.decision-default.out")
grep -q '(in 10m0s)' "$cap/d.decision-default.out" && ok "D: the ask is held for 10m0s" || bad "D: $(cat "$cap/d.decision-default.out")"
printf 'closed by the proof before its wait\n' | cmd decision --close "$id_d" >"$cap/d.close.out" 2>&1 || true
cmd report subscribe --recipients "@alice" --wait 0 >"$cap/d.subscribe-zero.out" 2>&1 || true
grep -q 'an ask from your own session sent at once' "$cap/d.subscribe-zero.out" && ok "D: --wait 0 says an ask from the member's own session is sent at once" || bad "D: $(cat "$cap/d.subscribe-zero.out")"
printf 'E16 leg D: a wait of zero\n' | cmd decision --to "$alice" >"$cap/d.decision-zero.out" 2>&1 || true
grep -q "^sent: to $alice at @alice through the slack gateway" "$cap/d.decision-zero.out" && [ "$(inbox_count "$cap/d.inbox.json")" = 3 ] && ok "D: with a wait of zero the ask for the session's own user went out at once" || bad "D: $(cat "$cap/d.decision-zero.out")"

# --- 8. leg E: the Stop hook's sentence ---------------------------------------------------------------------------------
while [ $(( $(now_s) - t_prompt )) -lt 62 ]; do sleep 1; done
hook stop "$(doc Stop)"
grep -q 'brigade decision --to <their roster label>' "$hook_out" && grep -q 'your own user is asked here in the conversation first, and the recorded one is held and reaches them only if they have not answered here within their wait' "$hook_out" && ok "E: the Stop ask says the session's own user is asked in the conversation first: $(head -c 120 "$hook_out")…" || bad "E: the Stop ask: $(cat "$hook_out")"

# --- 9. teardown ----------------------------------------------------------------------------------------------------------
kill "$watcher_pid" 2>/dev/null || true; wait "$watcher_pid" 2>/dev/null || true; watcher_pid=''
hook session-end "$(jq -cn --arg id "$native" --arg cwd "$proj" '{session_id:$id, cwd:$cwd, hook_event_name:"SessionEnd", reason:"other"}')"
say "failures: $failures"
exit "$failures"
