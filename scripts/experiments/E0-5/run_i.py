#!/usr/bin/env python3
"""E0-5 CHECK (i) — the start-time token guard.

Two arms against the SAME live watcher, in this order:

  CONTROL  an UNEDITED pidfile for a live process must be judged ALIVE and left
           alone (no unlink, no second watcher). Without this arm the check
           proves nothing: a routine that always replaces would pass the other.
  EDITED   the pidfile's start_token is rewritten to a different value. The
           entry must be judged DEAD and replaced.

Then a third, unasked-for observation that falls out of the replacement: the
watcher the routine declared dead is still running, and when it later exits it
would delete a pidfile that now belongs to somebody else.

No `claude` is involved: the guard is about pid + start time, so the watched
process is a plain `sleep`. That keeps the arms deterministic.
"""

import json
import os
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import wlib  # noqa: E402

HERE = os.path.dirname(os.path.realpath(__file__))


def spawn_routine(state, cpid, sock, tag):
    r = subprocess.run([sys.executable, os.path.join(HERE, "spawn.py"),
                        "--state-dir", state, "--claude-pid", str(cpid),
                        "--socket", sock, "--brigade-session-id", "e05-check-i",
                        "--token-sha256", wlib.sha256_hex("dummy-token"),
                        "--tag", tag, "--poll", "2", "--exit-delay", "0"],
                       capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit("spawn.py failed: " + r.stderr)
    return json.loads(r.stdout)


def stat_of(p):
    try:
        st = os.stat(p)
        return {"mode": oct(st.st_mode & 0o777), "mtime": st.st_mtime, "ino": st.st_ino}
    except OSError:
        return None


def main():
    tag = "checki-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results", tag)
    state = os.path.join(HERE, "state", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)
    sock = os.path.join(state, "fake.sock")
    open(sock, "w").close()

    out = {"tag": tag, "state_dir": state, "steps": []}

    watched = subprocess.Popen(["/bin/sleep", "600"])
    out["watched_pid"] = watched.pid
    time.sleep(0.3)

    # ---- initial spawn ----
    d0 = spawn_routine(state, watched.pid, sock, "i0")
    path = d0["pidfile"]
    w1 = d0["pidfile_after"]["pid"]
    out["steps"].append({"step": "initial_spawn", "decision": d0["action"],
                         "watcher_pid": w1,
                         "pidfile": d0["pidfile_after"], "stat": stat_of(path)})
    time.sleep(1.0)

    # ---- CONTROL: unedited pidfile, same live watcher ----
    before = wlib.read_pidfile(path)
    st_before = stat_of(path)
    d1 = spawn_routine(state, watched.pid, sock, "i1")
    after = wlib.read_pidfile(path)
    st_after = stat_of(path)
    control_ok = (d1["action"] == "kept" and d1["evaluation"]["verdict"] == "alive"
                  and before == after and st_before["ino"] == st_after["ino"]
                  and d1["spawned"] is None and wlib.kill0(w1)[0])
    out["steps"].append({
        "step": "control_unedited", "decision": d1["action"],
        "evaluation": d1["evaluation"], "spawned": d1["spawned"],
        "pidfile_identical": before == after,
        "inode_identical": st_before["ino"] == st_after["ino"],
        "watcher_still_alive": wlib.kill0(w1)[0],
        "pass": bool(control_ok),
    })

    # ---- EDITED: a different start_token for the same live pid ----
    entry = wlib.read_pidfile(path)
    real_token = entry["start_token"]
    forged = "Mon Jan  1 00:00:00 2001"
    entry["start_token"] = forged
    with open(path, "w") as f:
        json.dump(entry, f, indent=2, sort_keys=True)
    d2 = spawn_routine(state, watched.pid, sock, "i2")
    new_entry = d2["pidfile_after"]
    w2 = new_entry["pid"] if new_entry else None
    edited_ok = (d2["evaluation"]["verdict"] == "dead"
                 and d2["evaluation"].get("start_token_match") is False
                 and d2["action"] == "replaced"
                 and d2["pidfile_unlinked"] is True
                 and w2 is not None and w2 != w1)
    out["steps"].append({
        "step": "edited_start_token", "real_start_token": real_token,
        "forged_start_token": forged, "decision": d2["action"],
        "evaluation": d2["evaluation"], "old_watcher_pid": w1,
        "new_watcher_pid": w2, "new_pidfile": new_entry,
        "stat": stat_of(path), "pass": bool(edited_ok),
    })

    # ---- fallout: the "dead" watcher is not dead ----
    out["orphan"] = {
        "note": "the routine declared w1 dead on start_token mismatch; it does "
                "not signal the process, so w1 keeps running and keeps polling",
        "w1_alive_after_replacement": wlib.kill0(w1)[0],
        "w1_pid": w1, "w2_pid": w2,
    }

    # ---- shut the watched process down and see who deletes the pidfile ----
    t_kill = time.time()
    watched.kill()
    watched.wait()
    deadline = t_kill + 20
    while time.time() < deadline and (wlib.kill0(w1)[0] or wlib.kill0(w2)[0]):
        time.sleep(0.2)
    out["teardown"] = {
        "kill_epoch": round(t_kill, 3),
        "w1_alive": wlib.kill0(w1)[0], "w2_alive": wlib.kill0(w2)[0],
        "pidfile_present_after": os.path.exists(path),
        "seconds": round(time.time() - t_kill, 2),
    }

    logs = {}
    logdir = os.path.join(state, "logs")
    for fn in sorted(os.listdir(logdir)) if os.path.isdir(logdir) else []:
        logs[fn] = [json.loads(l) for l in open(os.path.join(logdir, fn)) if l.strip()]
    out["watcher_logs"] = logs
    cl = os.path.join(state, "session-close.ndjson")
    out["session_close"] = ([json.loads(l) for l in open(cl) if l.strip()]
                            if os.path.exists(cl) else [])

    out["pass"] = all(s.get("pass") for s in out["steps"] if "pass" in s)
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(out, f, indent=2)
    print(json.dumps(out, indent=2))
    print("results: " + results)


if __name__ == "__main__":
    main()
