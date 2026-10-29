#!/bin/sh
# usage: scripts/experiments/E14-manager-digest.sh      (run from the repository root after `make build`)
#
# The live proof of card 162 (manager-digest plan §8), with no model: two sessions of this machine's own member
# in two checkouts of a throwaway repository, the REAL binding of this machine's team (copied into a throwaway
# config dir, never the real store), the real backend, two real Syncthing instances (two state dirs), the real
# `brigade hook …` subcommands driven as Claude Code drives them (a sleeper as CLAUDE_PID), two real watchers in
# sink mode, the real mail gateway as the manager's road. Proves §8.1, the hook half of §8.2, §8.3, §8.4, §8.5
# and §8.6; §8.7 is unit-tested (digest.TestBuildCaps). The model half of §8.2 — that the model acts on the
# ask — is a live-session observation, not this script's.
#
# Everything is under one temporary root; the real ~/.config/brigade and ~/.local/state/brigade are read once
# (the binding copied) and never written. The sessions register on the real team's roster as digest-proof-a and
# digest-proof-b and are closed at the end. One decision's ask and one digest reach the real mail gateway and so
# the manager's mailbox: that is what §8.3 and §8.4 prove.
#
# Output: one `ok:`/`FAIL:` line per assertion, the measured waits, and the teardown. Exit status is the number of
# failed assertions.
set -eu
say() { printf '%s\n' "$*"; }
die() { printf 'E14: %s\n' "$1" >&2; exit 2; }
failures=0
ok() { printf 'ok: %s\n' "$*"; }
bad() { failures=$((failures + 1)); printf 'FAIL: %s\n' "$*"; }
now_s() { date +%s; }

# --- 1. preconditions --------------------------------------------------------------------------------------------
repo=$(CDPATH='' cd -- "$(dirname -- "$0")/../.." && pwd -P) || die "cannot resolve the repository root"
brigade=$repo/bin/brigade
[ -x "$brigade" ] || die "missing $brigade: run make build"
for tool in syncthing jq git; do command -v "$tool" >/dev/null 2>&1 || die "$tool is required"; done
real_cfg=${XDG_CONFIG_HOME:-$HOME/.config}/brigade
[ -f "$repo/.brigade.json" ] || die "no .brigade.json in $repo"
team_ref=$(jq -r .team_ref "$repo/.brigade.json")
key=''
for d in "$real_cfg"/teams/*/; do
  if [ -f "$d/team.json" ] && [ "$(jq -r .team_ref "$d/team.json" 2>/dev/null)" = "$team_ref" ]; then key=$(basename "$d"); fi
done
[ -n "$key" ] || die "this machine holds no binding for team $team_ref"
[ -f "$real_cfg/teams/$key/session.json" ] || die "no credential for team $key"
manager=${E14_MANAGER:-rjae@appshapes.com}
label=${E14_LABEL:-$manager}

# The inherited session environment is stripped by prefix (gates.md).
strip_args=''
for n in $(env | sed -n -e 's/^\(CLAUDE[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(AI_AGENT[0-9A-Za-z_]*\)=.*/\1/p' -e 's/^\(BRIGADE_[0-9A-Za-z_]*\)=.*/\1/p'); do
  strip_args="$strip_args -u $n"
done

# --- 2. the temporary machine -------------------------------------------------------------------------------------
root=$(mktemp -d "${TMPDIR:-/tmp}/brigade-e14-XXXXXX") || die "mktemp"
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
  git -C "$_c" config user.email "$manager"
  git -C "$_c" config user.name "Proof"
  git -C "$_c" remote add origin git@example.invalid:proof/digest-proof.git
  jq '. + {"sync": {"folders": [".brigade/state"], "state": ".brigade/state"}}' "$repo/.brigade.json" > "$_c/.brigade.json"
  if [ "${2:-ignore}" = ignore ]; then printf '.brigade/state/\n' > "$_c/.gitignore"; fi
  git -C "$_c" add -A && git -C "$_c" commit -q -m "0: the throwaway checkout"
  printf '%s' "$(CDPATH='' cd -- "$_c" && pwd -P)"
}
A=$(checkout a); B=$(checkout b); C=$(checkout c noignore)
pin() { printf '%s:{"adapter":"supabase","url":%s,"publishable_key":%s,"team_ref":%s,"consented_at":"2026-10-29T00:00:00Z"}' "$(jq -n --arg p "$1" '$p')" "$(jq .url "$repo/.brigade.json")" "$(jq .publishable_key "$repo/.brigade.json")" "$(jq .team_ref "$repo/.brigade.json")"; }
printf '{"version":1,"projects":{%s,%s,%s}}\n' "$(pin "$A")" "$(pin "$B")" "$(pin "$C")" > "$cfg/projects.json" && chmod 600 "$cfg/projects.json"

# A session is one sleeper, one state dir, one checkout.
sleep 100000 & pa=$!
sleep 100000 & pb=$!
sleep 100000 & pc=$!
trap 'kill $pa $pb $pc ${wa:-} ${wb:-} 2>/dev/null; true' EXIT
na=$(uuidgen | tr '[:upper:]' '[:lower:]'); nb=$(uuidgen | tr '[:upper:]' '[:lower:]'); nc=$(uuidgen | tr '[:upper:]' '[:lower:]')
state_a=$root/state-a/brigade; state_b=$root/state-b/brigade; state_c=$root/state-c/brigade

# hook <a|b|c> <sub> <stdin json> [extra env...]: one hook as Claude Code runs it, captured.
# shellcheck disable=SC2086
hook() {
  _s=$1; _sub=$2; _doc=$3; shift 3
  case $_s in a) _p=$pa; _st=$root/state-a; _ck=$A;; b) _p=$pb; _st=$root/state-b; _ck=$B;; c) _p=$pc; _st=$root/state-c; _ck=$C;; esac
  printf '%s' "$_doc" | env $strip_args HOME="$root/home-$_s" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$_st" XDG_DATA_HOME="$root/data-$_s" XDG_CACHE_HOME="$root/cache-$_s" \
    CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$_p" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli CLAUDE_PLUGIN_ROOT="$plugin_root" \
    CLAUDE_PLUGIN_OPTION_LABEL="$label" PATH="$PATH" "$@" "$brigade" hook "$_sub" --log-level debug \
    >"$cap/$_s.$_sub.out" 2>"$cap/$_s.$_sub.err" || true
}
# cmd <a|b|c> <args...>: a Bash-tool command inside the session.
# shellcheck disable=SC2086
cmd() {
  _s=$1; shift
  case $_s in a) _p=$pa; _st=$root/state-a; _ck=$A;; b) _p=$pb; _st=$root/state-b; _ck=$B;; c) _p=$pc; _st=$root/state-c; _ck=$C;; esac
  (cd "$_ck" && env $strip_args HOME="$root/home-$_s" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$_st" XDG_DATA_HOME="$root/data-$_s" XDG_CACHE_HOME="$root/cache-$_s" \
    CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$_p" CLAUDECODE=1 PATH="$PATH" "$brigade" "$@")
}
doc() { jq -cn --arg id "$1" --arg cwd "$2" --arg ev "$3" --arg title "$4" --arg mode "${5:-bypassPermissions}" '{session_id:$id, cwd:$cwd, hook_event_name:$ev, source:"startup", session_title:$title, permission_mode:$mode, transcript_path:"/never/read.jsonl", stop_hook_active:false}'; }
mkdir -p "$root/home-a" "$root/home-b" "$root/home-c"
for s in a b c; do mkdir -p "$root/state-$s" "$root/data-$s" "$root/cache-$s"; done

# --- 3. session start, the entries (§8.1) ---------------------------------------------------------------------
t0=$(now_s)
hook a session-start "$(doc "$na" "$A" SessionStart digest-proof-a)"
hook b session-start "$(doc "$nb" "$B" SessionStart digest-proof-b)"
hook c session-start "$(doc "$nc" "$C" SessionStart digest-proof-c)" CLAUDE_PLUGIN_OPTION_SYNC=off
for s in a b c; do say "session-start $s: $(head -1 "$cap/$s.session-start.out")"; done
map_a=$state_a/sessions/by-pid/$pa.json; map_b=$state_b/sessions/by-pid/$pb.json; map_c=$state_c/sessions/by-pid/$pc.json
[ -f "$map_a" ] && [ -f "$map_b" ] && ok "§8.1: both sessions registered on the real team ($(( $(now_s) - t0 )) s)" || bad "§8.1: a map is missing"
sid_a=$(jq -r .brigade_session_id "$map_a"); sid_b=$(jq -r .brigade_session_id "$map_b")
[ "$(jq -r .state_folder "$map_a")" = "$A/.brigade/state" ] && ok "§8.1: a's map names the state folder" || bad "§8.1: a's state_folder is $(jq -r .state_folder "$map_a")"
[ "$(jq -r '.state_folder // ""' "$map_c")" = "" ] && ok "§8.5: c (sync off) has no state folder" || bad "§8.5: c has a state folder"
hook a prompt "$(doc "$na" "$A" UserPromptSubmit digest-proof-a)"
hook b prompt "$(doc "$nb" "$B" UserPromptSubmit digest-proof-b)"
[ -f "$A/.brigade/state/sessions/$sid_a.json" ] && ok "§8.1: a's entry is in a's folder after the prompt" || bad "§8.1: no entry for a"
[ "$(jq -r .member "$A/.brigade/state/sessions/$sid_a.json")" = "$label" ] && ok "§8.1: the entry carries the member's label" || bad "§8.1: member is $(jq -r .member "$A/.brigade/state/sessions/$sid_a.json")"
[ "$(jq -r .start_head "$A/.brigade/state/sessions/$sid_a.json")" = "$(git -C "$A" rev-parse HEAD)" ] && ok "§8.1: start_head is HEAD" || bad "§8.1: start_head"

# --- 4. the watchers and the sync (§8.1) --------------------------------------------------------------------------
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
wait_log() { _f=$1; _n=$2; _b=$3; _t=$(now_s); while [ $(( $(now_s) - _t )) -lt "$_b" ]; do if [ -f "$_f" ] && grep -q "$_n" "$_f"; then return 0; fi; sleep 1; done; return 1; }
log_a=$state_a/logs/watcher-$pa.log; log_b=$state_b/logs/watcher-$pb.log
wait_log "$log_a" '"msg":"watch ready"' 60 && ok "a's watcher is ready" || bad "a's watcher never became ready"
wait_log "$log_b" '"msg":"watch ready"' 60 && ok "b's watcher is ready" || bad "b's watcher never became ready"
t0=$(now_s)
wait_log "$log_a" '"msg":"sync applied"' 120 && ok "a's sync adapter applied ($(( $(now_s) - t0 )) s)" || bad "a's sync never applied (see $log_a)"
wait_log "$log_b" '"msg":"sync applied"' 120 && ok "b's sync adapter applied" || bad "b's sync never applied (see $log_b)"
t0=$(now_s)
wait_file() { _f=$1; _b=$2; _t=$(now_s); while [ $(( $(now_s) - _t )) -lt "$_b" ]; do [ -f "$_f" ] && return 0; sleep 1; done; return 1; }
wait_file "$B/.brigade/state/sessions/$sid_a.json" 300 && ok "§8.1: a's entry reached b's checkout through Syncthing ($(( $(now_s) - t0 )) s)" || bad "§8.1: a's entry never reached b (see $log_a, $log_b)"
wait_file "$A/.brigade/state/sessions/$sid_b.json" 300 && ok "§8.1: b's entry reached a's checkout" || bad "§8.1: b's entry never reached a"
printf 'writing the manager digest proof\n' | cmd a doing >"$cap/a.doing.out" 2>&1 && say "doing: $(cat "$cap/a.doing.out")"
[ "$(jq -r '.doing[-1].text' "$A/.brigade/state/sessions/$sid_a.json")" = "writing the manager digest proof" ] && ok "§8.1: the doing line is in a's entry history" || bad "§8.1: doing history: $(jq -c .doing "$A/.brigade/state/sessions/$sid_a.json")"

# --- 5. the address rule and reports.json (§8.6) ---------------------------------------------------------------
hook c prompt "$(doc "$nc" "$C" UserPromptSubmit digest-proof-c)"
if cmd a report configure --manager "$manager" --recipients "$manager" --every 2m >"$cap/a.configure.out" 2>&1; then ok "§8.6: configure with an address in the ignored checkout: $(cat "$cap/a.configure.out")"; else bad "§8.6: configure refused in the ignored checkout: $(cat "$cap/a.configure.out")"; fi
# c's checkout does not ignore the folder, but sync is off there: a checkout D with sync on and no ignore.
D=$(checkout d noignore)
printf '{"version":1,"projects":{%s,%s,%s,%s}}\n' "$(pin "$A")" "$(pin "$B")" "$(pin "$C")" "$(pin "$D")" > "$cfg/projects.json"
sleep 100000 & pd=$!; nd=$(uuidgen | tr '[:upper:]' '[:lower:]'); mkdir -p "$root/state-d" "$root/data-d" "$root/cache-d" "$root/home-d"
printf '%s' "$(doc "$nd" "$D" SessionStart digest-proof-d)" | env $strip_args HOME="$root/home-d" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$root/state-d" XDG_DATA_HOME="$root/data-d" XDG_CACHE_HOME="$root/cache-d" CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$pd" CLAUDECODE=1 CLAUDE_CODE_ENTRYPOINT=cli CLAUDE_PLUGIN_ROOT="$plugin_root" CLAUDE_PLUGIN_OPTION_LABEL="$label" PATH="$PATH" "$brigade" hook session-start >"$cap/d.session-start.out" 2>"$cap/d.session-start.err" || true
if (cd "$D" && env $strip_args HOME="$root/home-d" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$root/state-d" CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$pd" CLAUDECODE=1 PATH="$PATH" "$brigade" report configure --manager "$manager" --recipients "$manager" >"$cap/d.configure.out" 2>&1); then bad "§8.6: configure with an address was accepted in a checkout that does not ignore the folder"; else grep -q 'does not ignore it' "$cap/d.configure.out" && ok "§8.6: configure with an address refused where git does not ignore the folder" || bad "§8.6: another refusal: $(cat "$cap/d.configure.out")"; fi
printf '%s' "$(jq -cn --arg id "$nd" --arg cwd "$D" '{session_id:$id, cwd:$cwd, hook_event_name:"SessionEnd", reason:"other"}')" | env $strip_args HOME="$root/home-d" XDG_CONFIG_HOME="$root/cfg" XDG_STATE_HOME="$root/state-d" CLAUDE_CONFIG_DIR="$claude_cfg" CLAUDE_PID="$pd" CLAUDECODE=1 CLAUDE_PLUGIN_ROOT="$plugin_root" PATH="$PATH" "$brigade" hook session-end >/dev/null 2>&1 || true
kill "$pd" 2>/dev/null || true

# --- 6. a decision (§8.3) -------------------------------------------------------------------------------------------
printf 'E14 proof: ship 0.43.0 with the digest now, or wait for a live-session measurement of the Stop ask?\n' \
  | cmd a decision --options "ship now | wait" --default "wait" --by 2026-10-31 --card 162 >"$cap/a.decision.out" 2>&1 || true
say "decision: $(cat "$cap/a.decision.out")"
did=$(sed -n 's/^recorded: decision \(.*\)$/\1/p' "$cap/a.decision.out")
[ -n "$did" ] && [ -f "$A/.brigade/state/decisions/$did.json" ] && ok "§8.3: the decision file is in the folder" || bad "§8.3: no decision file"
grep -q 'through the email gateway' "$cap/a.decision.out" && ok "§8.3: the ask went through the mail gateway" || bad "§8.3: the ask did not go through a gateway"
wait_file "$B/.brigade/state/decisions/$did.json" 300 && ok "§8.3: the decision reached b's checkout" || bad "§8.3: the decision never reached b"
printf 'wait: the live-session measurement comes first (E14)\n' | cmd b decision --close "$did" >"$cap/b.close.out" 2>&1 || true
say "close from b: $(cat "$cap/b.close.out")"
grep -q '^closed: decision' "$cap/b.close.out" && ok "§8.3: another session of the member closed it" || bad "§8.3: the close failed"

# --- 7. the Stop hook, its hook half (§8.2) --------------------------------------------------------------------------
git -C "$A" commit -q --allow-empty -m "162: Prove the Stop hook"
hook a stop "$(doc "$na" "$A" Stop digest-proof-a)"
grep -q '"decision":"block"' "$cap/a.stop.out" && grep -q 'brigade note' "$cap/a.stop.out" && ok "§8.2: a Stop after a commit asks for a note: $(head -c 120 "$cap/a.stop.out")…" || bad "§8.2: no ask after the commit: $(cat "$cap/a.stop.out")"
hook a stop "$(doc "$na" "$A" Stop digest-proof-a | jq -c '.stop_hook_active = true')"
[ -s "$cap/a.stop.out" ] && bad "§8.2: the continuation's Stop asked again" || ok "§8.2: the Stop with stop_hook_active asks nothing"
hook a stop "$(doc "$na" "$A" Stop digest-proof-a)"
[ -s "$cap/a.stop.out" ] && bad "§8.2: asked twice in a row" || ok "§8.2: never twice in a row"
printf 'Done: proved the Stop hook asks once after a commit\n' | cmd a note >"$cap/a.note.out" 2>&1 || true
say "note: $(cat "$cap/a.note.out")"

# --- 8. the producer (§8.4) ---------------------------------------------------------------------------------------
t0=$(now_s)
wait_log "$log_a" '"msg":"state: digest produced"' 180 && ok "§8.4: a (rank 0) produced the digest ($(( $(now_s) - t0 )) s)" || bad "§8.4: a never produced (see $log_a)"
grep -q '"msg":"state: digest produced"' "$log_b" && bad "§8.4: b produced too" || ok "§8.4: b did not produce"
wait_file "$B/.brigade/state/digests/last.json" 300 && ok "§8.4: the marker reached b's checkout" || bad "§8.4: the marker never reached b"
say "marker: $(cat "$A/.brigade/state/digests/last.json")"
cmd b report show >"$cap/b.show.out" 2>&1 || true
grep -q 'digest-proof-c' "$cap/b.show.out" && ok "§8.5: the digest lists the sync-off session as roster only" || bad "§8.5: c is not in the digest: $(grep -c . "$cap/b.show.out") lines"
# Hand over: a ends; b writes a note; the period (2 m) elapses; b produces.
kill "$wa" 2>/dev/null || true; wait "$wa" 2>/dev/null || true
hook a session-end "$(jq -cn --arg id "$na" --arg cwd "$A" '{session_id:$id, cwd:$cwd, hook_event_name:"SessionEnd", reason:"other"}')"
kill "$pa" 2>/dev/null || true
printf 'Next: hand the digest over to b\n' | cmd b note >/dev/null 2>&1 || true
t0=$(now_s)
wait_log "$log_b" '"msg":"state: digest produced"' 300 && ok "§8.4: b produced the next digest after a ended ($(( $(now_s) - t0 )) s after the note)" || bad "§8.4: b never produced after a ended (see $log_b)"

# --- 9. teardown -------------------------------------------------------------------------------------------------
kill "$wb" 2>/dev/null || true; wait "$wb" 2>/dev/null || true
hook b session-end "$(jq -cn --arg id "$nb" --arg cwd "$B" '{session_id:$id, cwd:$cwd, hook_event_name:"SessionEnd", reason:"other"}')"
hook c session-end "$(jq -cn --arg id "$nc" --arg cwd "$C" '{session_id:$id, cwd:$cwd, hook_event_name:"SessionEnd", reason:"other"}')"
say "captures: $cap (kept); root removed except cap when E14_KEEP is unset"
if [ -z "${E14_KEEP:-}" ]; then mv "$cap" "${TMPDIR:-/tmp}/brigade-e14-cap-$(date +%s)"; rm -rf "$root"; fi
say "failures: $failures"
exit "$failures"
