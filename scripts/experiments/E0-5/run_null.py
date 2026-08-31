#!/usr/bin/env python3
"""E0-5 ADVERSARIAL PASS — the NULL CONTROL that checks (b1) and (b2) were missing.

(b1) reported 27.55 s / 58.96 s to detect a SIGKILLed session and (b2) 1.47 s to
detect an unlinked socket. Both numbers are meaningless unless the watcher would
have STAYED ALIVE had neither trigger happened: a watcher that self-terminates
after ~30 s would produce the same logs.

Each arm therefore runs the SAME stand-in (detach.py, unmodified) against a live
process whose socket file is present, holds for HOLD seconds -- deliberately
longer than the longest reported latency (58.96 s) -- and samples throughout.
Only then does the arm fire its trigger.

  arm "socket"  hold, assert alive, then unlink the socket        -> must exit
  arm "kill"    hold, assert alive, then SIGKILL *and reap* the
                watched process                                   -> must exit

So each arm carries both directions in one run: X absent -> Y absent for the
whole hold, then X present -> Y follows. The watched process is a plain
`/bin/sleep`: the watcher's only inputs are kill(pid,0) and os.path.exists on
the socket path, so nothing about it is claude-specific -- and using `sleep`
keeps the arms deterministic and touches no claude state at all.
"""

import json
import os
import signal
import subprocess
import sys
import threading
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import wlib  # noqa: E402
import common  # noqa: E402

HERE = os.path.dirname(os.path.realpath(__file__))
HOLD = float(os.environ.get("E05_NULL_HOLD", "130"))
SAMPLE = 5.0


def spawn_routine(state, cpid, sock, tag):
    r = subprocess.run([sys.executable, os.path.join(HERE, "spawn.py"),
                        "--state-dir", state, "--claude-pid", str(cpid),
                        "--socket", sock, "--brigade-session-id", "e05-null-" + tag,
                        "--token-sha256", wlib.sha256_hex("dummy-token"),
                        "--tag", tag, "--poll", "2", "--exit-delay", "0"],
                       capture_output=True, text=True)
    if r.returncode != 0:
        raise SystemExit("spawn.py failed: " + r.stderr)
    return json.loads(r.stdout)


def readlog(state, tag, wpid):
    p = os.path.join(state, "logs", "watcher-%s-%d.ndjson" % (tag, wpid))
    if not os.path.exists(p):
        return []
    return [json.loads(l) for l in open(p) if l.strip()]


def closelog(state):
    p = os.path.join(state, "session-close.ndjson")
    if not os.path.exists(p):
        return []
    return [json.loads(l) for l in open(p) if l.strip()]


def run_arm(mode, out):
    tag = "null-%s" % mode
    state = os.path.join(HERE, "state3", "%s-%s" % (tag, time.strftime("%Y%m%d-%H%M%S")))
    os.makedirs(state, mode=0o700, exist_ok=True)
    sock = os.path.join(state, "fake.sock")
    open(sock, "w").close()

    watched = subprocess.Popen(["/bin/sleep", "900"])
    time.sleep(0.3)
    d = spawn_routine(state, watched.pid, sock, tag)
    wpid = d["pidfile_after"]["pid"]
    path = d["pidfile"]
    a = {"mode": mode, "state_dir": state, "watched_pid": watched.pid,
         "watcher_pid": wpid, "pidfile": path, "socket": sock,
         "spawn_action": d["action"], "hold_seconds": HOLD, "samples": []}

    t0 = time.time()
    while time.time() - t0 < HOLD:
        time.sleep(SAMPLE)
        log = readlog(state, tag, wpid)
        a["samples"].append({
            "t": round(time.time() - t0, 2),
            "watcher_alive": wlib.kill0(wpid)[0],
            "watched_alive": wlib.kill0(watched.pid)[0],
            "pidfile_present": os.path.exists(path),
            "socket_present": os.path.exists(sock),
            "polls": sum(1 for r in log if r["event"] == "poll"),
            "exit_events": [r["event"] for r in log
                            if r["event"] in ("exit_condition_detected",
                                              "session_close_done", "watcher_exit",
                                              "signal_received")],
            "session_closes": len(closelog(state)),
        })

    log = readlog(state, tag, wpid)
    a["held"] = {
        "seconds": round(time.time() - t0, 2),
        "watcher_alive_at_end_of_hold": wlib.kill0(wpid)[0],
        "pidfile_present_at_end_of_hold": os.path.exists(path),
        "polls_during_hold": sum(1 for r in log if r["event"] == "poll"),
        "exit_condition_events_during_hold":
            [r for r in log if r["event"] == "exit_condition_detected"],
        "session_closes_during_hold": len(closelog(state)),
        "signals_during_hold": [r for r in log if r["event"] == "signal_received"],
    }
    a["null_control_pass"] = bool(
        a["held"]["watcher_alive_at_end_of_hold"]
        and a["held"]["pidfile_present_at_end_of_hold"]
        and not a["held"]["exit_condition_events_during_hold"]
        and a["held"]["session_closes_during_hold"] == 0)

    # ---- now, and only now, the trigger ----
    if mode == "socket":
        trig = time.time()
        os.unlink(sock)
        a["trigger"] = "unlink socket"
    else:
        os.kill(watched.pid, signal.SIGKILL)
        watched.wait()          # reap immediately: E0-5 already showed the zombie
        trig = time.time()      # t0 is the reap, as in the -102651 control
        a["trigger"] = "SIGKILL + immediate reap of the watched process"
    a["trigger_epoch"] = round(trig, 4)

    gone_at = None
    deadline = trig + 30
    while time.time() < deadline:
        if not wlib.kill0(wpid)[0]:
            gone_at = time.time()
            break
        time.sleep(0.01)
    log = readlog(state, tag, wpid)

    def ev(name):
        for r in log:
            if r["event"] == name:
                return r
        return None

    det, cls, ex = ev("exit_condition_detected"), ev("session_close_done"), ev("watcher_exit")
    a["after_trigger"] = {
        "seconds_measured_from": "the trigger epoch above",
        "seconds_to_detect": round(det["epoch"] - trig, 3) if det else None,
        "detect_reason": det.get("reason") if det else None,
        "seconds_to_session_close": round(cls["epoch"] - trig, 3) if cls else None,
        "seconds_to_watcher_exit": round(ex["epoch"] - trig, 3) if ex else None,
        "seconds_to_process_gone": round(gone_at - trig, 3) if gone_at else None,
        "watcher_exited": gone_at is not None,
        "pidfile_present_after": os.path.exists(path),
        "session_close_records": closelog(state),
        "signals_logged": [r["signal"] for r in log if r["event"] == "signal_received"],
    }
    a["positive_control_pass"] = bool(a["after_trigger"]["watcher_exited"] and det)

    if mode == "socket":
        try:
            os.kill(watched.pid, signal.SIGKILL)
            watched.wait()
        except OSError:
            pass
    out[mode] = a


def main():
    results = os.path.join(HERE, "results3", "null-" + time.strftime("%Y%m%d-%H%M%S"))
    os.makedirs(results, exist_ok=True)
    guard = common.ConfigGuard(results)
    out = {"hold_seconds": HOLD,
           "longest_reported_latency_being_ruled_out": 58.959,
           "detach_py_sha256": common.sha256_file(os.path.join(HERE, "detach.py")),
           "wlib_py_sha256": common.sha256_file(os.path.join(HERE, "wlib.py"))}
    arms = {}
    ts = [threading.Thread(target=run_arm, args=(m, arms)) for m in ("socket", "kill")]
    for t in ts:
        t.start()
    for t in ts:
        t.join()
    out["arms"] = arms
    out["pass"] = all(a["null_control_pass"] and a["positive_control_pass"]
                      for a in arms.values())
    out["config"] = guard.verify(results)
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(out, f, indent=2)
    print(json.dumps({k: v for k, v in out.items() if k != "arms"}, indent=2))
    for m, a in arms.items():
        print("\n--- arm %s: null_control_pass=%s positive_control_pass=%s" %
              (m, a["null_control_pass"], a["positive_control_pass"]))
        print(json.dumps(a["held"], indent=1))
        print(json.dumps(a["after_trigger"], indent=1)[:1200])
    print("\nresults: " + results)


if __name__ == "__main__":
    main()
