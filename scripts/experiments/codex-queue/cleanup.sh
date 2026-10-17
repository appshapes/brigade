#!/bin/bash
# After the run: stop tmux TUIs and pollers, archive the test threads, remove the trust tables the runs added to the
# operator's ~/.codex/config.toml, stop the shared daemon the runs started (it was not running before), report leftovers.
set -u
ROOT=${CODEX_TEST_ROOT:?}
tmux -L codex107 kill-server 2>/dev/null; pkill -f "select count\(\*\) from queued_items" 2>/dev/null
echo "== threads to archive"
IDS=$(sqlite3 -readonly "$HOME/.codex/state_5.sqlite" "select id from threads where cwd like '%scratchpad/codex-test%' and archived=0;")
for id in $IDS; do out=$("$ROOT/run-codex.sh" --cwd "$ROOT" archive "$id" 2>&1); echo "archive $id: $(echo "$out" | tr '\n' ' ' | cut -c1-120)"; done
echo "== trust tables added to config.toml (removed below)"
python3 -I - "$HOME/.codex/config.toml" "$ROOT" <<'PY'
import sys,re
path,root=sys.argv[1],sys.argv[2]
s=open(path).read()
pat=re.compile(r'\[projects\."'+re.escape(root)+r'[^"]*"\]\ntrust_level = "trusted"\n\n?')
found=pat.findall(s); print("\n".join(f[:160].strip() for f in found)); print(f"{len(found)} tables")
s2=pat.sub("",s).rstrip("\n")+"\n"
open(path,"w").write(s2)
PY
echo "== config.toml now"; cat "$HOME/.codex/config.toml"
echo "== daemon stop"; "$ROOT/run-codex.sh" --cwd "$ROOT" app-server daemon stop 2>&1 | tail -3; sleep 2; ls -la "$HOME/.codex/app-server-control/" 2>&1 | tail -2; ps -axo pid,command | grep -i "[c]odex" | grep -v -E "zsh|grep" | cut -c1-120
echo "== queue rows left"; sqlite3 -readonly "$HOME/.codex/queue_1.sqlite" "select thread_id, count(*) from queued_items group by thread_id;"
