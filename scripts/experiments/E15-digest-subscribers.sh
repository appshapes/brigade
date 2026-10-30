#!/bin/sh
# usage: scripts/experiments/E15-digest-subscribers.sh      (run from the repository root after `make build`)
#
# The live proof of card 167 (manager-digest plan §5.5 v2), with no model: two sessions of this machine's own member
# in two checkouts of a throwaway repository, the REAL binding of this machine's team (copied into a throwaway config
# dir, never the real store), the real backend, two real Syncthing instances (two state dirs), the real `brigade hook`
# subcommands driven as Claude Code drives them (a sleeper as CLAUDE_PID), two real watchers in sink mode, the real
# Slack gateway as the subscribers' road. Proves: a 0.43 reports.json reads as one subscriber and `subscribe` rewrites
# it as version 2 and carries the 0.43 marker over; two subscribers, each with their own marker, each getting their
# own digest from one producer tick (the second with `--only mine`, so its digest lists the one decision for it); a
# decision addressed to each, through the Slack gateway; a decision with no --to refused; a decision for nobody
# recorded unsent; another session of the member closing one; the Stop hook's decision sentence naming --to;
# `unsubscribe`; and `report send` reaching the one subscriber left.
#
# The second subscriber has no session of its own on this machine (one binding, one member), so its entry is written
# into reports.json by the script, as that member's own session would write it; its recipients are this member's
# address, so both subscribers' digests and both decisions land in the same Slack DM, one "for" each label.
#
# Everything is under one temporary root; the real ~/.config/brigade and ~/.local/state/brigade are read once (the
# binding copied) and never written. The sessions register on the real team's roster as digest-proof-a and
# digest-proof-b and are closed at the end. Two decisions and three digests reach the real Slack gateway and so the
# member's Slack DMs; the decisions say they are the proof and need no answer.
#
# Output: one `ok:`/`FAIL:` line per assertion, the measured waits, and the teardown. Exit status is the number of
# failed assertions. E15_KEEP=1 keeps the root.
# shellcheck disable=SC2015  # every `A && ok || bad` below: ok never fails, so bad runs only when A does not
set -eu
say() { printf '%s\n' "$*"; }
die() { printf 'E15: %s\n' "$1" >&2; exit 2; }
failures=0
ok() { printf 'ok: %s\n' "$*"; }
bad() { failures=$((failures + 1)); printf 'FAIL: %s\n' "$*"; }
now_s() { date +%s; }

# --- 1. preconditions --------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P) || die "cannot resolve the repository root"
brigade=$repo/bin/brigade
[ -x "$brigade" ] || die "missing $brigade: run make build"
for tool in syncthing jq git shasum; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done
real_cfg=${XDG_CONFIG_HOME:-$HOME/.config}/brigade
[ -f "$repo/.brigade.json" ] || die "no .brigade.json in $repo"
team_ref=$(jq -r .team_ref "$repo/.brigade.json")
key=''
for d in "$real_cfg"/teams/*/; do
  if [ -f "$d/team.json" ] && [ "$(jq -r .team_ref "$d/team.json" 2>/dev/null)" = "$team_ref" ]; then key=$(basename "$d"); fi
done
[ -n "$key" ] || die "this machine holds no binding for team $team_ref"
[ -f "$real_cfg/teams/$key/session.json" ] || die "no credential for team $key"
member=${E15_MEMBER:-rjae@appshapes.com}
label=${E15_LABEL:-$member}
second='E15 second'

# The inherited session environment is stripped by prefix (gates.md).
strip_args=''
for n in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(BRIGADE_[0-9A-Za-z_]*\)=.*/\1/p'); do
  strip_args="$strip_args -u $n"
done

# --- 2. the temporary machine -------------------------------------------------------------------------------------
root=$(mktemp -d "${TMPDIR:-/tmp}/brigade-e15-XXXXXX") || die "mktemp"
root=$(CDPATH='' cd -- "$root" && pwd -P)
say "root: $root"
cfg=$root/cfg/brigade
mkdir -p "$cfg/teams/$key" && chmod 700 "$root/cfg" "$cfg" "$cfg/teams" "$cfg/teams/$key"
cp "$real_cfg/teams/$key/team.json" "$real_cfg/teams/$key/session.json" "$cfg/teams/$key/" && chmod 600 "$cfg/teams/$key"/*.json
plugin_root=$root/plugin
mkdir -p "$plugin_root/bin" && ln -s "$brigade" "$plugin_root/bin/brigade" && cp "$repo/plugin/bin/VERSION" "$plugin_root/bin/VERSION"
claude_cfg=$root/claude-cfg && mkdir -p "$claude_cfg"
cap=$root/cap && mkdir -p "$cap"

# checkout <name>: a throwaway clone with the team file plus the state folder, ignored by git.
checkout() {
  _c=$root/$1
  mkdir -p "$_c"
  git -C "$_c" init -q -b master
  git -C "$_c" config user.email "$member"
  git -C "$_c" config user.name "Proof"
  git -C "$_c" remote add origin git@example.invalid:proof/digest-proof.git
  jq '. + {"sync": {"folders": [".brigade/state"], "state": ".brigade/state"}}' "$repo/.brigade.json" > "$_c/.brigade.json"
  printf '.brigade/state/\n' > "$_c/.gitignore"
  git -C "$_c" add -A && git -C "$_c" commit -q -m "0: the throwaway checkout"
  printf '%s' "$(CDPATH='' cd -- "$_c" && pwd -P)"
}
A=$(checkout a); B=$(checkout b)
pin() { printf '%s:{"adapter":"supabase","url":%s,"publishable_key":%s,"team_ref":%s,"consented_at":"2026-10-29T00:00:00Z"}' "$(jq -n --arg p "$1" '$p')" "$(jq .url "$repo/.brigade.json")" "$(jq .publishable_key "$repo/.brigade.json")" "$(jq .team_ref "$repo/.brigade.json")"; }
printf '{"version":1,"projects":{%s,%s}}\n' "$(pin "$A")" "$(pin "$B")" > "$cfg/projects.json" && chmod 600 "$cfg/projects.json"

# A session is one sleeper, one state dir, one checkout.
sleep 100000 & pa=$!
sleep 100000 & pb=$!
trap 'kill $pa $pb ${wa:-} ${wb:-} 2>/dev/null; true' EXIT
na=$(uuidgen | tr '[:upper:]' '[:lower:]'); nb=$(uuidgen | tr '[:upper:]' '[:lower:]')
state_a=$root/state-a/brigade; state_b=$root/state-b/brigade

# hook <a|b> <sub> <stdin json> [extra env...]: one hook as Claude Code runs it, captured.
# shellcheck disable=SC2086
hook() {
  _s=$1; _sub=$2; _doc=$3; shift 3
  case $_s in a) _p=$pa; _st=$root/state-a;; b) _p=$pb; _st=$root/state-b;; esac
  printf '%s' "$_doc" | env $strip_args HOME="$root/home-$_s" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$_st" XDG_DATA_HOME="$root/data-$_s" XDG_CACHE_HOME="$root/cache-$_s" \
    CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$_p" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli CLAUDE_PLUGIN_ROOT="$plugin_root" \
    CLAUDE_PLUGIN_OPTION_LABEL="$label" PATH="$PATH" "$@" "$brigade" hook "$_sub" --log-level debug \
    >"$cap/$_s.$_sub.out" 2>"$cap/$_s.$_sub.err" || true
}
# cmd <a|b> <args...>: a Bash-tool command inside the session.
# shellcheck disable=SC2086
cmd() {
  _s=$1; shift
  case $_s in a) _p=$pa; _st=$root/state-a; _ck=$A;; b) _p=$pb; _st=$root/state-b; _ck=$B;; esac
  (cd "$_ck" && env $strip_args HOME="$root/home-$_s" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$_st" XDG_DATA_HOME="$root/data-$_s" XDG_CACHE_HOME="$root/cache-$_s" \
    CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$_p" CLAUDECODE=1 PATH="$PATH" "$brigade" "$@")
}
doc() { jq -cn --arg id "$1" --arg cwd "$2" --arg ev "$3" --arg title "$4" --arg mode "${5:-bypassPermissions}" '{session_id:$id, cwd:$cwd, hook_event_name:$ev, source:"startup", session_title:$title, permission_mode:$mode, transcript_path:"/never/read.jsonl", stop_hook_active:false}'; }
end_doc() { jq -cn --arg id "$1" --arg cwd "$2" '{session_id:$id, cwd:$cwd, hook_event_name:"SessionEnd", reason:"other"}'; }
mkdir -p "$root/home-a" "$root/home-b"
for s in a b; do mkdir -p "$root/state-$s" "$root/data-$s" "$root/cache-$s"; done
wait_log() { _f=$1; _n=$2; _b=$3; _t=$(now_s); while [ $(( $(now_s) - _t )) -lt "$_b" ]; do if [ -f "$_f" ] && grep -q "$_n" "$_f"; then return 0; fi; sleep 1; done; return 1; }
wait_file() { _f=$1; _b=$2; _t=$(now_s); while [ $(( $(now_s) - _t )) -lt "$_b" ]; do [ -f "$_f" ] && return 0; sleep 1; done; return 1; }
wait_grep() { _f=$1; _n=$2; _b=$3; _t=$(now_s); while [ $(( $(now_s) - _t )) -lt "$_b" ]; do if [ -f "$_f" ] && grep -q "$_n" "$_f"; then return 0; fi; sleep 1; done; return 1; }
# stem <label>: state.LabelStem, for a marker's file name.
stem() {
  _lower=$(printf '%s' "$1" | tr '[:upper:]' '[:lower:]')
  _slug=$(printf '%s' "$_lower" | tr -c 'a-z0-9\n' '-' | sed -e 's/--*/-/g' -e 's/^-//' -e 's/-$//')
  _hash=$(printf '%s' "$_lower" | shasum -a 256 | cut -c1-6)
  printf '%s-%s' "$_slug" "$_hash"
}
stamp_ago() { date -u -r $(( $(now_s) - $1 )) +%Y-%m-%dT%H:%M:%SZ 2>/dev/null || date -u -d "@$(( $(now_s) - $1 ))" +%Y-%m-%dT%H:%M:%SZ; }

# --- 3. session start, the entries ----------------------------------------------------------------------------------
t0=$(now_s)
hook a session-start "$(doc "$na" "$A" SessionStart digest-proof-a)"
hook b session-start "$(doc "$nb" "$B" SessionStart digest-proof-b)"
for s in a b; do say "session-start $s: $(head -1 "$cap/$s.session-start.out")"; done
map_a=$state_a/sessions/by-pid/$pa.json; map_b=$state_b/sessions/by-pid/$pb.json
[ -f "$map_a" ] && [ -f "$map_b" ] && ok "both sessions registered on the real team ($(( $(now_s) - t0 )) s)" || bad "a map is missing"
sid_a=$(jq -r .brigade_session_id "$map_a"); sid_b=$(jq -r .brigade_session_id "$map_b")
hook a prompt "$(doc "$na" "$A" UserPromptSubmit digest-proof-a)"
hook b prompt "$(doc "$nb" "$B" UserPromptSubmit digest-proof-b)"
t_prompt=$(now_s)
[ -f "$A/.brigade/state/sessions/$sid_a.json" ] && [ "$(jq -r .member "$A/.brigade/state/sessions/$sid_a.json")" = "$label" ] && ok "a's entry is in a's folder with the member's label" || bad "no entry for a, or another member"

# --- 4. the watchers and the sync ----------------------------------------------------------------------------------
# shellcheck disable=SC2086
watcher() {
  _s=$1; shift
  case $_s in a) _p=$pa; _st=$root/state-a; _sd=$state_a;; b) _p=$pb; _st=$root/state-b; _sd=$state_b;; esac
  exec env $strip_args HOME="$root/home-$_s" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$_st" XDG_DATA_HOME="$root/data-$_s" XDG_CACHE_HOME="$root/cache-$_s" \
    CLAUDE_CONFIG_DIR="$claude_cfg" BRIGADE_CLAUDE_PID="$_p" BRIGADE_PROFILE="$key" BRIGADE_CONFIG_DIR="$cfg" BRIGADE_STATE_DIR="$_sd" \
    BRIGADE_ADAPTER_COMMAND='[]' BRIGADE_TEAM_INBOUND=accept BRIGADE_LOG_LEVEL=debug PATH="$PATH" \
    "$brigade" watch --sink "$root/sink-$_s" </dev/null >"$cap/$_s.watch.out" 2>"$cap/$_s.watch.err"
}
watcher a & wa=$!
watcher b & wb=$!
log_a=$state_a/logs/watcher-$pa.log; log_b=$state_b/logs/watcher-$pb.log
wait_log "$log_a" '"msg":"watch ready"' 60 && ok "a's watcher is ready" || bad "a's watcher never became ready"
wait_log "$log_b" '"msg":"watch ready"' 60 && ok "b's watcher is ready" || bad "b's watcher never became ready"
t0=$(now_s)
wait_log "$log_a" '"msg":"sync applied"' 120 && ok "a's sync adapter applied ($(( $(now_s) - t0 )) s)" || bad "a's sync never applied (see $log_a)"
wait_log "$log_b" '"msg":"sync applied"' 120 && ok "b's sync adapter applied" || bad "b's sync never applied (see $log_b)"
t0=$(now_s)
wait_file "$B/.brigade/state/sessions/$sid_a.json" 300 && ok "a's entry reached b's checkout through Syncthing ($(( $(now_s) - t0 )) s)" || bad "a's entry never reached b (see $log_a, $log_b)"
wait_file "$A/.brigade/state/sessions/$sid_b.json" 300 && ok "b's entry reached a's checkout" || bad "b's entry never reached a"

# --- 5. the 0.43 file, the subscriptions, the markers -----------------------------------------------------------------
folder_a=$A/.brigade/state; digests_a=$folder_a/digests
mkdir -p "$digests_a"
printf '{"version":1,"manager":"%s","recipients":["%s"],"every":"2m"}\n' "$member" "$member" > "$folder_a/reports.json"
printf '{"version":1,"produced_at":"%s","session_id":"%s","session_name":"digest-proof-a","decisions_open":0,"commits":0,"active":1}\n' "$(stamp_ago 70)" "$sid_a" > "$digests_a/last.json"
cmd a report show --json >"$cap/a.show-v1.out" 2>&1 || true
[ "$(jq -r '.result.reports.subscribers | length' "$cap/a.show-v1.out" 2>/dev/null)" = 1 ] && [ "$(jq -r '.result.reports.subscribers[0].label' "$cap/a.show-v1.out")" = "$member" ] && ok "§5.5: the 0.43 file reads as one subscriber, the manager" || bad "§5.5: the 0.43 file did not read as one subscriber: $(head -c 300 "$cap/a.show-v1.out")"
if cmd a report subscribe --every 2m >"$cap/a.subscribe.out" 2>&1; then ok "§5.5: subscribe from a: $(cat "$cap/a.subscribe.out")"; else bad "§5.5: subscribe refused: $(cat "$cap/a.subscribe.out")"; fi
[ "$(jq -r .version "$folder_a/reports.json")" = 2 ] && [ "$(jq -r '.subscribers | length' "$folder_a/reports.json")" = 1 ] && [ "$(jq -r '.subscribers[0].recipients[0]' "$folder_a/reports.json")" = "$member" ] && ok "§5.5: reports.json is version 2 with the one subscriber, the address as the recipient" || bad "§5.5: reports.json after subscribe: $(cat "$folder_a/reports.json")"
stem_member=$(stem "$member"); stem_second=$(stem "$second")
[ ! -f "$digests_a/last.json" ] && [ -f "$digests_a/$stem_member.json" ] && [ "$(jq -r .label "$digests_a/$stem_member.json")" = "$member" ] && ok "§5.5: the 0.43 marker was carried over to digests/$stem_member.json with the label" || bad "§5.5: the 0.43 marker was not carried over: $(ls "$digests_a")"
# The second subscriber, as their own session would write it, with a marker 70 s old so both come due together.
jq -c --arg l "$second" --arg r "$member" '.subscribers += [{"label": $l, "recipients": [$r], "every": "2m", "only": "mine"}]' "$folder_a/reports.json" > "$folder_a/reports.json.tmp" && mv "$folder_a/reports.json.tmp" "$folder_a/reports.json"
printf '{"version":1,"label":"%s","produced_at":"%s","session_id":"%s","session_name":"digest-proof-a","decisions_open":0,"commits":0,"active":1}\n' "$second" "$(stamp_ago 70)" "$sid_a" > "$digests_a/$stem_second.json"
t_markers=$(now_s)
t0=$(now_s)
wait_grep "$B/.brigade/state/reports.json" "$second" 300 && ok "§5.5: the two-subscriber file reached b's checkout ($(( $(now_s) - t0 )) s)" || bad "§5.5: the file never reached b"

# --- 6. decisions: one for each subscriber, one with no --to, one for nobody ------------------------------------------
printf 'E15 proof (card 167): a decision with no addressee\n' | cmd a decision --json >"$cap/a.decision-noto.out" 2>&1 || true
grep -q '"reason":"to_needed"' "$cap/a.decision-noto.out" && ok "§5.5: a decision with no --to is refused (to_needed)" || bad "§5.5: no --to was not refused: $(head -c 300 "$cap/a.decision-noto.out")"
printf 'E15 proof (card 167), a decision for %s: this is the proof, no answer is needed\n' "$member" | cmd a decision --to "$member" --options "ignore | ignore too" --card 167 >"$cap/a.decision-member.out" 2>&1 || true
say "decision for the member: $(cat "$cap/a.decision-member.out")"
did_member=$(sed -n 's/^recorded: decision \(.*\)$/\1/p' "$cap/a.decision-member.out")
grep -q "sent: to $member at $member through the slack gateway" "$cap/a.decision-member.out" && ok "§5.5: the decision for the member went through the Slack gateway to their address" || bad "§5.5: the member's decision did not go through Slack"
printf 'E15 proof (card 167), a decision for the second subscriber: this is the proof, no answer is needed\n' | cmd a decision --to "$second" --card 167 >"$cap/a.decision-second.out" 2>&1 || true
say "decision for the second: $(cat "$cap/a.decision-second.out")"
did_second=$(sed -n 's/^recorded: decision \(.*\)$/\1/p' "$cap/a.decision-second.out")
grep -q "sent: to $second at $member through the slack gateway" "$cap/a.decision-second.out" && ok "§5.5: the decision for the second subscriber went through the Slack gateway to their recipient" || bad "§5.5: the second's decision did not go through Slack"
[ -n "$did_second" ] && [ "$(jq -r .to "$folder_a/decisions/$did_second.json")" = "$second" ] && ok "§5.5: the decision file carries its addressee" || bad "§5.5: no addressee in the decision file"
printf 'E15 proof (card 167): a decision for nobody on the roster\n' | cmd a decision --to "nobody-e15" >"$cap/a.decision-nobody.out" 2>&1 || true
grep -q '^not sent: no gateway of their recipients is on the roster, and they have no session' "$cap/a.decision-nobody.out" && ok "§5.5: a decision for nobody is recorded unsent, and the line says so" || bad "§5.5: the decision for nobody: $(cat "$cap/a.decision-nobody.out")"
wait_file "$B/.brigade/state/decisions/$did_member.json" 300 && ok "the member's decision reached b's checkout" || bad "the member's decision never reached b"
printf 'closed by the proof; nothing was decided\n' | cmd b decision --close "$did_member" >"$cap/b.close.out" 2>&1 || true
grep -q '^closed: decision' "$cap/b.close.out" && ok "another session of the member closed it" || bad "the close failed: $(cat "$cap/b.close.out")"

# --- 7. the producer: one tick, two subscribers, two markers -----------------------------------------------------------
t0=$(now_s)
wait_grep "$log_a" '"msg":"state: digests produced"' 240 && ok "§5.5: a produced the digests ($(( $(now_s) - t0 )) s after the decisions, $(( $(now_s) - t_markers )) s after the markers)" || bad "§5.5: a never produced (see $log_a)"
grep -q '"msg":"state: digests produced","sent":2,"missed":0' "$log_a" && ok "§5.5: one tick sent to both subscribers" || bad "§5.5: the tick's count: $(grep 'digests produced' "$log_a" | tail -1)"
grep -q "\"msg\":\"state: digest produced\",\"label\":\"$second\",\"how\":\"slack\",\"decisions_open\":1" "$log_a" && ok "§5.5: the second subscriber's digest went through Slack with the one decision for them (--only mine)" || bad "§5.5: the second's line: $(grep "$second" "$log_a" | grep 'digest produced' | tail -1)"
# Every open decision: the second's and nobody's, and the member's own when b's close has not synced back yet.
grep -Eq "\"msg\":\"state: digest produced\",\"label\":\"$member\",\"how\":\"slack\",\"decisions_open\":[23]," "$log_a" && ok "§5.5: the member's digest went through Slack with every open decision, not only theirs" || bad "§5.5: the member's line: $(grep "$member" "$log_a" | grep 'digest produced' | tail -1)"
grep -q '"msg":"state: digests produced"' "$log_b" && bad "§5.5: b produced too" || ok "§5.5: b did not produce"
[ "$(jq -r .label "$digests_a/$stem_member.json")" = "$member" ] && [ "$(jq -r .label "$digests_a/$stem_second.json")" = "$second" ] && [ "$(jq -r .session_id "$digests_a/$stem_second.json")" = "$sid_a" ] && ok "§5.5: one marker per subscriber, written by a" || bad "§5.5: the markers: $(ls "$digests_a")"
t0=$(now_s)
wait_grep "$B/.brigade/state/digests/$stem_second.json" "$sid_a" 300 && ok "§5.5: the second's marker reached b's checkout ($(( $(now_s) - t0 )) s)" || bad "§5.5: the marker never reached b"

# --- 8. the Stop hook's decision sentence names --to -----------------------------------------------------------------
while [ $(( $(now_s) - t_prompt )) -lt 61 ]; do sleep 1; done
git -C "$A" commit -q --allow-empty -m "167: Prove the Stop hook"
hook a stop "$(doc "$na" "$A" Stop digest-proof-a)"
grep -q '"decision":"block"' "$cap/a.stop.out" && grep -q 'brigade note' "$cap/a.stop.out" && grep -q 'brigade decision --to <their roster label>' "$cap/a.stop.out" && ok "§5.3: a Stop after a commit and a long turn asks for a note and a decision --to: $(head -c 160 "$cap/a.stop.out")…" || bad "§5.3: the Stop ask: $(cat "$cap/a.stop.out")"

# --- 9. unsubscribe, and a send to the one left --------------------------------------------------------------------
cmd b report unsubscribe >"$cap/b.unsubscribe.out" 2>&1 || true
[ "$(cat "$cap/b.unsubscribe.out")" = "unsubscribed: $member" ] && [ "$(jq -r '.subscribers | length' "$B/.brigade/state/reports.json")" = 1 ] && ok "§5.5: unsubscribe from b (another session of the member) removed the member's entry and nobody else's" || bad "§5.5: unsubscribe: $(cat "$cap/b.unsubscribe.out"); $(cat "$B/.brigade/state/reports.json")"
cmd b report send >"$cap/b.send.out" 2>&1 || true
say "send from b: $(cat "$cap/b.send.out")"
[ "$(grep -c '^sent: ' "$cap/b.send.out")" = 1 ] && grep -q "to $second at $member through the slack gateway" "$cap/b.send.out" && ok "§5.5: report send reached the one subscriber left, through Slack" || bad "§5.5: report send: $(cat "$cap/b.send.out")"
[ "$(jq -r .session_id "$B/.brigade/state/digests/$stem_second.json")" = "$sid_b" ] && ok "§5.5: the send wrote the second's marker" || bad "§5.5: the second's marker after the send: $(cat "$B/.brigade/state/digests/$stem_second.json")"

# --- 10. teardown ---------------------------------------------------------------------------------------------------
kill "$wa" "$wb" 2>/dev/null || true; wait "$wa" "$wb" 2>/dev/null || true
hook a session-end "$(end_doc "$na" "$A")"
hook b session-end "$(end_doc "$nb" "$B")"
say "captures: $cap (kept); root removed except cap when E15_KEEP is unset"
if [ -z "${E15_KEEP:-}" ]; then mv "$cap" "${TMPDIR:-/tmp}/brigade-e15-cap-$(date +%s)"; rm -rf "$root"; fi
say "failures: $failures"
exit "$failures"
