#!/usr/bin/env python3
"""E0-5 ADVERSARIAL PASS — closes the one sub-check the LIFECYCLE stage left
UNMEASURED: after the messaging socket file is unlinked, does `claude` put it
back?

Why it matters for 6.6. Check (b2) established that unlinking
/tmp/cc-socks/<pid>.sock makes the watcher exit in 1.47 s WHILE THE SESSION IS
STILL ALIVE AND WELL -- i.e. socket-ENOENT is a false positive for liveness. If
claude re-creates the file, the false positive is a transient the watcher could
ride out with a re-check; if it never does, the session is left with no inbox
and the watcher's exit is at least self-consistent. The two lead to different
designs, so the answer is not decoration.

Cheap and repeatable: a `claude -p` session, as the budget runs used -- no pty,
no trust dialog, no fullscreen renderer, ~15 s. The plugin is plugin_h_ss
(SessionStart only) with BRIGADE_E05_SPAWN=0, so no watcher is involved at all;
this measures claude, not the stand-in.

Sequence: launch -p with a prompt that keeps the session busy ~12 s -> wait on
the hook's ready file -> stat and unlink the socket -> poll the path every 25 ms
for the rest of the session -> record whether it ever comes back (and with which
inode), and whether the session still completed its turn with its socket gone.
"""

import json
import os
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402
import g2  # noqa: E402

HERE = os.path.dirname(os.path.realpath(__file__))
PROMPT = ("Using the Bash tool, run exactly this one command: sleep 12 . "
          "Then reply with exactly: E05SR_OK")


def statinfo(p):
    try:
        st = os.stat(p)
        return {"exists": True, "ino": st.st_ino, "mode": oct(st.st_mode & 0o7777),
                "ctime": round(st.st_ctime, 3)}
    except OSError:
        return {"exists": False}


def main():
    tag = "sockre-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results3", tag)
    state = os.path.join(HERE, "state3", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)
    project = os.path.join("/tmp", "e05-sockre", tag)
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    topguard = g2.TopKeyGuard(results)
    outer = common.outer_identity()
    out = {"tag": tag, "results": results, "state_dir": state,
           "project_dir": project, "prompt": PROMPT,
           "stripped_env_names": common.leaky_names()}

    plugin = os.path.join(HERE, "plugin_h_ss")
    env = common.child_env({"BRIGADE_STATE_DIR": state, "BRIGADE_E05_HOME": HERE,
                            "BRIGADE_E05_TAG": tag, "BRIGADE_E05_SPAWN": "0"})
    cmd = ["env"] + common.env_unset_args() + [
        "claude", "-p", PROMPT, "--plugin-dir", plugin,
        "--permission-mode", "bypassPermissions"]
    t_launch = time.time()
    proc = subprocess.Popen(cmd, cwd=project, env=env, stdin=subprocess.DEVNULL,
                            stdout=open(os.path.join(results, "p-stdout.txt"), "wb"),
                            stderr=open(os.path.join(results, "p-stderr.txt"), "wb"))

    ok = g2.waitfor(os.path.join(state, "ready-1.json"), 180)
    nested = g2.read_json(os.path.join(state, "nested-env-1.json")) or {}
    gate = g2.gate(nested, outer)
    out["isolation"] = gate
    if not ok or not gate["ok"]:
        try:
            proc.kill()
        except Exception:
            pass
        out["aborted"] = "isolation gate" if ok else "hook never became ready"
        out["config"] = guard.verify(results)
        out["dot_claude_json"] = topguard.revert([project])
        shutil.rmtree(project, ignore_errors=True)
        json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
        print(json.dumps(out, indent=2))
        return 2

    sock = nested["socket"]
    cpid = int(nested["claude_pid"])
    out["claude_pid"] = cpid
    out["socket"] = sock
    out["socket_before"] = statinfo(sock)

    # ---- the trigger, with claude provably alive ----
    alive_before = common.alive(cpid)
    t0 = time.time()
    os.unlink(sock)
    out["trigger"] = {"epoch": round(t0, 4), "claude_alive_before_unlink": alive_before,
                      "claude_alive_after_unlink": common.alive(cpid)}

    # ---- poll the path for the rest of the session ----
    reappeared = None
    samples = []
    last = None
    while proc.poll() is None and time.time() - t0 < 180:
        s = statinfo(sock)
        if s["exists"] and reappeared is None:
            reappeared = {"seconds_after_unlink": round(time.time() - t0, 3), "stat": s}
        if s != last:
            samples.append({"t": round(time.time() - t0, 3), **s})
            last = s
        time.sleep(0.025)
    rc = proc.wait()
    t_exit = time.time()
    out["socket_reappeared_while_running"] = reappeared
    out["path_state_changes_while_running"] = samples
    out["socket_at_process_exit"] = statinfo(sock)

    # a short tail after exit, in case re-creation is part of teardown
    tail = []
    for _ in range(40):
        tail.append(statinfo(sock)["exists"])
        time.sleep(0.05)
    out["socket_exists_in_2s_after_exit"] = any(tail)

    stdout = open(os.path.join(results, "p-stdout.txt")).read()
    out["session"] = {
        "rc": rc,
        "seconds_total": round(t_exit - t_launch, 2),
        "seconds_unlink_to_exit": round(t_exit - t0, 2),
        "stdout_head": stdout[:300],
        "turn_completed_after_unlink": "E05SR_OK" in stdout,
        "stderr_head": open(os.path.join(results, "p-stderr.txt")).read()[:500],
    }
    out["startup_records"] = g2.read_ndjson(os.path.join(state, "startup.ndjson"))
    out["answer"] = {
        "claude_recreates_the_socket_file": bool(reappeared),
        "session_kept_working_without_it": out["session"]["turn_completed_after_unlink"],
    }

    out["config"] = guard.verify(results)
    out["dot_claude_json"] = topguard.revert([project])
    shutil.rmtree(project, ignore_errors=True)
    json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
    print(json.dumps({k: v for k, v in out.items()
                      if k not in ("startup_records", "path_state_changes_while_running")},
                     indent=2))
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
