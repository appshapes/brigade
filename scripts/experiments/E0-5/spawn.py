#!/usr/bin/env python3
"""E0-5 spawn/liveness routine — what `brigade watch --ensure` will do.

  1. read ${BRIGADE_STATE_DIR}/watchers/<claude_pid>.json
  2. evaluate it: kill(pid,0) nil AND start_token equal byte for byte
  3. ALIVE  -> do nothing, leave the pidfile alone   (decision: "kept")
     DEAD   -> unlink the stale pidfile and spawn a fresh watcher ("replaced")
     ABSENT -> spawn                                  ("spawned")

Prints one JSON decision object on stdout.
"""

import argparse
import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import wlib  # noqa: E402

HERE = os.path.dirname(os.path.realpath(__file__))


def spawn_watcher(a, ready):
    cmd = [sys.executable, os.path.join(HERE, "detach.py"),
           "--state-dir", a.state_dir,
           "--claude-pid", str(a.claude_pid),
           "--socket", a.socket,
           "--brigade-session-id", a.brigade_session_id,
           "--token-sha256", a.token_sha256,
           "--tag", a.tag,
           "--poll", str(a.poll),
           "--exit-delay", str(a.exit_delay),
           "--ready-file", ready,
           "--spawned-by", str(os.getpid())]
    try:
        os.unlink(ready)
    except OSError:
        pass
    # A plain background child: its ppid is THIS process until this process
    # exits. detach.py calls setsid() itself; nothing here pre-detaches it, so
    # check (a) observes a real ppid hand-off rather than a staged one.
    p = subprocess.Popen(cmd, stdin=subprocess.DEVNULL,
                         stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
                         close_fds=True)
    deadline = time.time() + 8
    info = None
    while time.time() < deadline:
        if os.path.exists(ready):
            try:
                with open(ready) as f:
                    info = json.load(f)
                break
            except ValueError:
                pass
        time.sleep(0.05)
    return {"launcher_child_pid": p.pid, "ready": info}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--state-dir", required=True)
    ap.add_argument("--claude-pid", required=True, type=int)
    ap.add_argument("--socket", default="")
    ap.add_argument("--brigade-session-id", default="")
    ap.add_argument("--token-sha256", default="")
    ap.add_argument("--tag", default="x")
    ap.add_argument("--poll", type=float, default=2.0)
    ap.add_argument("--exit-delay", type=float, default=0.0)
    ap.add_argument("--ready-file", default="")
    ap.add_argument("--dry-run", action="store_true",
                    help="evaluate only; never unlink and never spawn")
    a = ap.parse_args()

    path = wlib.pidfile_path(a.state_dir, a.claude_pid)
    entry = wlib.read_pidfile(path)
    decision = wlib.evaluate(entry)

    out = {"ts": wlib.iso(), "pidfile": path, "entry": entry,
           "evaluation": decision, "dry_run": a.dry_run}

    ready = a.ready_file or os.path.join(a.state_dir, "ready-%s.json" % a.tag)

    if decision["verdict"] == "alive":
        out["action"] = "kept"
        out["spawned"] = None
        out["pidfile_unlinked"] = False
    elif a.dry_run:
        out["action"] = "would_" + ("replace" if entry else "spawn")
        out["spawned"] = None
        out["pidfile_unlinked"] = False
    else:
        if entry is not None:
            os.unlink(path)
            out["pidfile_unlinked"] = True
            out["action"] = "replaced"
        else:
            out["pidfile_unlinked"] = False
            out["action"] = "spawned"
        out["spawned"] = spawn_watcher(a, ready)

    out["pidfile_after"] = wlib.read_pidfile(path)
    print(json.dumps(out, indent=2, sort_keys=True))
    return 0


if __name__ == "__main__":
    sys.exit(main())
