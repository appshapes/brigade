#!/bin/bash
# After the run: closes the tmux server, archives the run's threads (the ids titles.sh recorded, and every
# unarchived thread Codex's state database places in the test root or under it), removes the trust tables the
# run added to the operator's config.toml (the test root's own and those under it, nothing that merely starts
# with the same letters), and stops the app-server daemon if the run started it (before.txt says whether one
# was running).
set -u
ROOT=${CODEX_TEST_ROOT:?set CODEX_TEST_ROOT}
ROOT=$(cd "$ROOT" && pwd -P) || exit 2
export CODEX_TEST_ROOT="$ROOT"
# codex runs with no CODEX_HOME (run-codex.sh), so its home is this one whatever the caller's environment says
CODEX_DIR="$HOME/.codex"
tmux -L codex190 kill-server 2>/dev/null
echo "== threads archived"
LIST="$ROOT/evidence/threads-to-archive.txt"
{ cat "$ROOT/evidence/thread-ids.txt" 2>/dev/null
  python3 -I - "$CODEX_DIR/state_5.sqlite" "$ROOT" <<'PY'
import sqlite3,sys,urllib.parse
root=sys.argv[2]
try:
    db=sqlite3.connect("file:"+urllib.parse.quote(sys.argv[1])+"?mode=ro",uri=True)
    for (i,) in db.execute("select id from threads where archived=0 and (cwd=? or substr(cwd,1,?)=?)",(root,len(root)+1,root+"/")):
        print(i)
except sqlite3.Error:
    pass
PY
} | grep -E '^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$' | sort -u > "$LIST"
while read -r id; do
  out=$("$ROOT/run-codex.sh" --cwd "$ROOT" archive "$id" 2>&1); echo "archive $id: $(echo "$out" | tr '\n' ' ' | cut -c1-100)"
done < "$LIST"
echo "== trust tables removed from config.toml"
python3 -I - "$CODEX_DIR/config.toml" "$ROOT" <<'PY'
import os,re,sys,tempfile
path,root=os.path.realpath(sys.argv[1]),sys.argv[2]
with open(path,encoding="utf-8",newline="") as f: s=f.read()
# the root itself or a path under it: the "/" keeps a sibling such as <root>-old out
pat=re.compile(r'\[projects\."'+re.escape(root)+r'(?:/[^"\n]*)?"\]\ntrust_level = "trusted"\n\n?')
found=pat.findall(s)
for f in found: print(f.splitlines()[0][len('[projects."')+len(root):].rstrip('"]') or "/")
print(f"{len(found)} tables")
if found:
    # written whole beside the file and renamed into place: an interrupt never leaves half a configuration
    fd,tmp=tempfile.mkstemp(dir=os.path.dirname(path),prefix=".config.toml.")
    try:
        with os.fdopen(fd,"w",encoding="utf-8",newline="") as f: f.write(pat.sub("",s))
        os.chmod(tmp,os.stat(path).st_mode & 0o7777)
        os.replace(tmp,path)
    except BaseException:
        try: os.unlink(tmp)
        except OSError: pass
        raise
PY
echo "== daemon"
if grep -q '^daemon: not running' "$ROOT/evidence/before.txt" 2>/dev/null; then
  "$ROOT/run-codex.sh" --cwd "$ROOT" app-server daemon stop 2>&1 | tail -1 | cut -c1-120; sleep 2
  # `daemon stop` leaves the daemon's updater loop behind; it came with the daemon the run started
  pkill -f 'codex app-server daemon pid-update-loop' 2>/dev/null; sleep 1
else echo "left running: it was running before the run"; fi
left=$(pgrep -fl 'codex app-server' | cut -c1-120)
if [ -n "$left" ]; then echo "$left"; else echo "no app-server process"; fi
