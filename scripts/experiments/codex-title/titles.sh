#!/bin/bash
# The whole run (card 190): where does the " | <something>" after a renamed Codex session's name come from?
# Builds folders whose repository root, remote, working directory and worktree names all differ, asks
# `codex doctor` what project name it would show in each, then starts a TUI in each, reads the terminal title
# before and after `/rename`, and reads back what Codex stored; then the same with the title setting changed, as
# a launch override and from a repository's own .codex/config.toml. No model turn unless TURN=1.
# Usage: CODEX_TEST_ROOT=<dir outside any repository> ./titles.sh
set -u
ROOT=${CODEX_TEST_ROOT:?set CODEX_TEST_ROOT}
ROOT=$(cd "$ROOT" && pwd -P) || exit 2
export CODEX_TEST_ROOT="$ROOT"
# shellcheck source=/dev/null
. "$ROOT/lib.sh"
TOP="$ROOT/evidence"; EV="$TOP"; TS=none
mkdir -p "$TOP" "$ROOT/tmp" "$ROOT/work" "$ROOT/remotes"
NONCE=$(od -An -N2 -tx1 /dev/urandom | tr -d ' \n')
G(){ git -c user.email=t@example.invalid -c user.name=t -c init.defaultBranch=main "$@"; }

# ---- state before the run (cleanup.sh reads it) --------------------------------------------------------------
# Written by the first run in this test root and kept: a second run would find the daemon the first one started
# and record it as the operator's.
[ -f "$TOP/before.txt" ] || {
  echo "codex: $("$ROOT/run-codex.sh" --cwd "$ROOT" --version 2>&1)"
  if pgrep -f 'codex app-server' >/dev/null; then echo "daemon: running"; else echo "daemon: not running"; fi
  echo "terminal_title lines in config.toml: $(grep -c 'terminal_title' "$CODEX_DIR/config.toml" 2>/dev/null)"
} > "$TOP/before.txt"
cat "$TOP/before.txt"

# ---- the folders ---------------------------------------------------------------------------------------------
( cd "$ROOT" || exit 2
  mkdir -p work/plain-repo && ( cd work/plain-repo && { [ -d .git ] || { G init -q && echo one > README.md && G add -A && G commit -q -m init; }; } )
  [ -d remotes/remote-name.git ] || G init -q --bare remotes/remote-name.git
  [ -d work/folder-name ] || { G clone -q "$ROOT/remotes/remote-name.git" work/folder-name 2>/dev/null
    ( cd work/folder-name && mkdir -p sub-dir/deep-dir && echo two > sub-dir/deep-dir/file.txt && G add -A && G commit -q -m init && G push -q origin HEAD 2>/dev/null ); }
  mkdir -p work/plain-dir/inner-dir
  [ -d work/worktree-name ] || ( cd work/folder-name && G worktree add -q "$ROOT/work/worktree-name" -b wt-branch 2>/dev/null )
  # a repository that carries the setting itself: Codex reads .codex/config.toml once the folder is trusted
  mkdir -p work/config-repo/.codex && ( cd work/config-repo && { [ -d .git ] || { G init -q && printf '[tui]\nterminal_title = ["thread-name"]\n' > .codex/config.toml && G add -A && G commit -q -m init; }; } )
)
LAYOUTS="repo:plain-repo clone:folder-name subdir:folder-name/sub-dir/deep-dir worktree:worktree-name norepo:plain-dir/inner-dir"

# ---- codex doctor: the project name Codex says it would show, with no TUI and no model ------------------------
printf 'label\tcwd\tproject source\tproject value\titems\titems from\n' > "$TOP/doctor.tsv"
for l in $LAYOUTS; do
  label=${l%%:*}; rel=${l#*:}
  "$ROOT/run-codex.sh" --cwd "$ROOT/work/$rel" doctor --json > "$TOP/doctor-$label.json" 2>/dev/null
  python3 -I - "$TOP/doctor-$label.json" "$label" "$rel" >> "$TOP/doctor.tsv" <<'PY'
import json,sys
d=json.load(open(sys.argv[1]))["checks"]["terminal.title"]["details"]
print("\t".join([sys.argv[2],sys.argv[3],d.get("terminal title project source",""),d.get("terminal title project value",""),d.get("terminal title items",""),d.get("terminal title source","")]))
PY
done
column -t -s "$(printf '\t')" "$TOP/doctor.tsv"

# ---- the TUI: the title before and after /rename, and what Codex stored ---------------------------------------
printf 'label\tcwd\tcodex arguments\ttitle unnamed\ttitle named\tindex thread_name\tstate name\ttitle after quit\n' > "$TOP/titles.tsv"
# probe <label> <cwd under work/> [codex arguments]
probe(){
  local label="$1" rel="$2" args="${3:-}" name="probe-$1-$NONCE" unnamed named idx sid stored _
  EV="$TOP/$label"; TS="t-$label"; mkdir -p "$EV"
  launch "$ROOT/work/$rel" "$args" || { T kill-session -t "$TS" 2>/dev/null; return 1; }
  unnamed=$(title); note unnamed
  keys "/rename $name" || { quit; return 1; }
  for _ in $(seq 1 20); do [ "$(title)" != "$unnamed" ] && break; sleep 0.25; done; sleep 1
  named=$(title); note named
  idx=$(index_line "$name"); sid=$(printf '%s' "$idx" | field id); stored=$(printf '%s' "$idx" | field thread_name)
  log "index=$idx state_name=$(state_name "$sid")"
  [ -n "$sid" ] && printf '%s\n' "$sid" >> "$TOP/thread-ids.txt"
  quit
  osc_titles > "$EV/osc-titles.txt"
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$label" "$rel" "${args:-(none)}" "$unnamed" "$named" \
    "${stored:-(no entry)}" "$(state_name "$sid")" "$(tail -1 "$EV/osc-titles.txt")" >> "$TOP/titles.tsv"
  EV="$TOP"
}
for l in $LAYOUTS; do probe "${l%%:*}" "${l#*:}"; done
# the supported setting, as a launch override (so the operator's config.toml is never written)
probe only-thread   folder-name/sub-dir/deep-dir "-c 'tui.terminal_title=[\"thread-name\"]'"
probe only-project  folder-name/sub-dir/deep-dir "-c 'tui.terminal_title=[\"project-name\"]'"
probe thread-cwd    folder-name/sub-dir/deep-dir "-c 'tui.terminal_title=[\"thread-name\",\"current-dir\",\"git-branch\"]'"
probe thread-title  folder-name/sub-dir/deep-dir "-c 'tui.terminal_title=[\"thread-title\",\"project-name\"]'"
probe empty         folder-name/sub-dir/deep-dir "-c 'tui.terminal_title=[]'"
# the same setting from the repository's own .codex/config.toml, and what `codex doctor` then says of it
probe project-config config-repo
"$ROOT/run-codex.sh" --cwd "$ROOT/work/config-repo" doctor --json > "$TOP/doctor-project-config.json" 2>/dev/null
python3 -I - "$TOP/doctor-project-config.json" > "$TOP/doctor-project-config.txt" <<'PY'
import json,sys
c=json.load(open(sys.argv[1]))["checks"]["terminal.title"]
print("doctor in config-repo, trusted:", c["summary"])
for k,v in c["details"].items(): print("  "+k+" = "+str(v))
PY
cat "$TOP/doctor-project-config.txt"

# ---- one model turn (TURN=1): the title while Codex works, and whether a first turn names the thread ----------
if [ "${TURN:-0}" = 1 ]; then
  EV="$TOP/turn"; TS=t-turn; mkdir -p "$EV"
  if launch "$ROOT/work/plain-repo"; then
    note unnamed
    if keys "Reply with exactly the word READY and nothing else. Do not run any command."; then
      for _ in $(seq 1 480); do pane | grep -q -E '^\S*\s*READY$' && break; sleep 0.25; done
      # Codex may name the thread itself after its first turn, some seconds later: give it 30 s to show in the
      # title (a name is followed by " | ") before the rename below replaces the question
      for _ in $(seq 1 120); do title | grep -q ' | ' && break; sleep 0.25; done
      note after-turn; pane > "$EV/pane-01-after-turn.txt"
      keys "/rename probe-turn-$NONCE" && sleep 3
      note named
      idx=$(index_line "probe-turn-$NONCE"); log "index=$idx"
      sid=$(printf '%s' "$idx" | field id); [ -n "$sid" ] && printf '%s\n' "$sid" >> "$TOP/thread-ids.txt"
    fi
    # a thread whose rename did not land is still archived: cleanup.sh also asks Codex's state database
    quit; osc_titles > "$EV/osc-titles.txt"; cat "$EV/osc-titles.txt"
  else
    T kill-session -t "$TS" 2>/dev/null
  fi
  EV="$TOP"
fi
echo; column -t -s "$(printf '\t')" "$TOP/titles.tsv"
echo "terminal_title lines in config.toml after the run: $(grep -c 'terminal_title' "$CODEX_DIR/config.toml" 2>/dev/null)"
