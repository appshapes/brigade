#!/usr/bin/env python3
"""E0-5 driver for CHECK (a) and CHECK (b) — one interactive, expect-driven
`claude` per run.

  --mode survive : CHECK (a), interactive-exit half. The watcher is spawned from
                   SessionStart with a long --exit-delay so it can still be
                   photographed after the session is gone; ps is sampled BEFORE
                   hook exit (by the hook itself), AFTER hook exit, and AFTER the
                   interactive session exits.
  --mode kill    : CHECK (b1). SIGKILL the `claude` process.
  --mode socket  : CHECK (b2). Remove the socket path; leave claude alive.

Two expect facts inherited from E0-4 and not re-derived here: the trust dialog's
highlighted default is "No, exit", so a bare Enter quits the session (send Down
then Enter); and the dialog is drawn as a box, so only SINGLE-word regexes match.

Isolation: every CLAUDE* name except CLAUDE_CONFIG_DIR is stripped with `env -u`
by prefix, and the run ABORTS before any measurement if the nested socket,
session id, token or claude pid match the outer session's.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402
import wlib  # noqa: E402

HERE = common.HERE
PLUGIN = common.PLUGIN

EXPECT = r"""
set timeout 240
log_file -a "{results}/interactive.log"
set marklog "{results}/expect-marks.ndjson"

proc mark {{event args}} {{
    global marklog
    set ms [clock milliseconds]
    set rec "{{\"ms\": $ms, \"event\": \"$event\""
    foreach {{k v}} $args {{ append rec ", \"$k\": \"$v\"" }}
    append rec "}}"
    set f [open $marklog a]; puts $f $rec; close $f
    send_user "\n\[\[E05 $event ms=$ms\]\]\n"
}}

mark spawn
spawn -noecho env {unsets} claude --plugin-dir "{plugin}" --permission-mode bypassPermissions
mark spawned pid $spawn_id

# Trust dialog. Default is "No, exit" -> a bare Enter QUITS. Single-word regexes only.
expect {{
  -re {{trust}} {{
     mark trust_dialog_shown
     expect -re {{exit}}
     sleep 1
     send -- "\033\[B"
     sleep 1
     send -- "\r"
     mark trust_dialog_accepted
  }}
  -re {{shortcuts|Welcome|bypass}} {{ mark ui_ready_no_trust }}
  timeout {{ mark trust_timeout }}
}}

# Do NOT time the startup. On one run claude took 52 s between accepting the
# trust dialog and firing SessionStart, and a fixed sleep let /exit reach the
# input buffer before the prompt existed. Wait for the hook's own ready file.
mark wait_for_hook
set n 0
while {{![file exists "{ready_file}"] && $n < 900}} {{ sleep 1; incr n }}
mark hook_ready waited_seconds $n
sleep 3
mark ui_ready
set f [open "{results}/ui-ready" w]; puts $f [clock milliseconds]; close $f

# The driver now does its work out of band. Nothing is written to the pty during
# the hold except, in the survive run, the /exit at the end.
mark hold_start seconds {hold}
sleep {hold}
mark hold_end

mark exiting
send -- "/exit\r"
expect eof
mark eof
set f [open "{results}/eof" w]; puts $f [clock milliseconds]; close $f
"""


def waitfor(path, timeout, poll=0.05):
    end = time.time() + timeout
    while time.time() < end:
        if os.path.exists(path):
            return True
        time.sleep(poll)
    return False


def read_json(p):
    try:
        with open(p) as f:
            return json.load(f)
    except Exception:
        return None


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", required=True, choices=["survive", "kill", "socket"])
    ap.add_argument("--hold", type=int, default=150)
    ap.add_argument("--reap", action="store_true",
                    help="kill mode: after the SIGKILL, also kill the process "
                         "that would otherwise leave claude a zombie, so the "
                         "corpse is reparented to launchd and reaped at once")
    args = ap.parse_args()

    tag = "%s-%s" % (args.mode, time.strftime("%Y%m%d-%H%M%S"))
    results = os.path.join(HERE, "results", tag)
    state = os.path.join(HERE, "state", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)

    # Throwaway project directory OUTSIDE the repo working tree.
    scratch = os.environ.get("E05_SCRATCH") or "/tmp"
    project = os.path.join(scratch, "e05-projects", tag)
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    outer = common.outer_identity()
    leaky = common.leaky_names()

    exit_delay = "60" if args.mode == "survive" else "0"
    exp = EXPECT.format(results=results, plugin=PLUGIN, hold=args.hold,
                        ready_file=os.path.join(state, "ready-%s.json" % tag),
                        unsets=" ".join(common.env_unset_args()))
    exp_path = os.path.join(results, "drive.exp")
    with open(exp_path, "w") as f:
        f.write(exp)

    env = common.child_env({
        "BRIGADE_STATE_DIR": state,
        "BRIGADE_E05_HOME": HERE,
        "BRIGADE_E05_TAG": tag,
        "BRIGADE_E05_EXIT_DELAY": exit_delay,
    })

    out = {"tag": tag, "mode": args.mode, "results": results, "state_dir": state,
           "project_dir": project, "exit_delay": exit_delay,
           "outer_claude_env_names_stripped": leaky,
           "samples": [], "events": []}

    def note(ev, **kw):
        r = {"epoch": round(time.time(), 3), "ts": wlib.iso(), "event": ev}
        r.update(kw)
        out["events"].append(r)
        print("[driver] " + json.dumps(r))
        return r

    raw = open(os.path.join(results, "interactive.raw"), "wb")
    note("expect_spawn")
    proc = subprocess.Popen(["expect", "-f", exp_path], cwd=project, env=env,
                            stdout=raw, stderr=subprocess.STDOUT)

    ready_file = os.path.join(state, "ready-%s.json" % tag)
    nested_file = os.path.join(state, "nested-env.json")
    hookexit_file = os.path.join(state, "hook-exit.json")

    ok = waitfor(ready_file, 900)
    ready = read_json(ready_file)
    nested = read_json(nested_file) or {}
    note("watcher_ready", ok=ok, ready=ready)

    gate = common.isolation_gate(nested, outer)
    out["isolation"] = gate
    with open(os.path.join(results, "isolation-gate.json"), "w") as f:
        json.dump(gate, f, indent=2)
    if not gate["ok"]:
        note("ISOLATION_GATE_FAIL", leaks=gate["leaks"])
        proc.kill()
        out["aborted"] = "isolation gate failed"
        out["config"] = guard.verify(results)
        with open(os.path.join(results, "verdict.json"), "w") as f:
            json.dump(out, f, indent=2)
        print(json.dumps(out, indent=2))
        return 2
    note("isolation_gate_ok", nested=gate["nested"])

    wpid = ready["watcher_pid"] if ready else None
    cpid = int(nested.get("claude_pid") or 0)
    sock = nested.get("socket") or ""
    out["watcher_pid"] = wpid
    out["claude_pid"] = cpid
    out["socket"] = sock

    # ---- sample 1: BEFORE hook exit (taken by the hook, while it is alive) ----
    before = read_json(os.path.join(state, "ps-before-hook-exit.json"))
    out["samples"].append({"when": "before_hook_exit", "source": "hook", "data": before})

    # ---- sample 2: AFTER hook exit ----
    waitfor(hookexit_file, 60)
    hk = read_json(hookexit_file) or {}
    hpid = hk.get("hook_pid")
    end = time.time() + 30
    while hpid and common.alive(hpid) and time.time() < end:
        time.sleep(0.05)
    s2 = common.ps_sample(wpid, "after_hook_exit")
    s2["hook_pid"] = hpid
    s2["hook_alive"] = common.alive(hpid) if hpid else None
    s2["claude_alive"] = common.alive(cpid) if cpid else None
    out["samples"].append({"when": "after_hook_exit", "source": "driver", "data": s2})
    note("sampled_after_hook_exit", raw=s2["raw"])

    waitfor(os.path.join(results, "ui-ready"), 960)
    note("ui_ready")
    time.sleep(3)

    logdir = os.path.join(state, "logs")

    def watcher_log():
        recs = []
        if os.path.isdir(logdir):
            for fn in sorted(os.listdir(logdir)):
                for line in open(os.path.join(logdir, fn)):
                    line = line.strip()
                    if line:
                        recs.append(json.loads(line))
        return recs

    if args.mode == "survive":
        # ---- CHECK (a): let the interactive session exit on its own ----
        # The hold is short in this mode; wait for expect to reach eof.
        note("waiting_for_session_exit")
        proc.wait(timeout=900)
        t_eof = time.time()
        note("session_exited", claude_alive=common.alive(cpid) if cpid else None)
        # Poll until claude is really gone, then photograph the watcher.
        end = time.time() + 30
        while cpid and common.alive(cpid) and time.time() < end:
            time.sleep(0.05)
        t_gone = time.time()
        s3 = common.ps_sample(wpid, "after_session_exit")
        s3["claude_alive"] = common.alive(cpid) if cpid else None
        s3["watcher_alive"] = common.alive(wpid) if wpid else None
        s3["seconds_after_claude_gone"] = round(time.time() - t_gone, 2)
        out["samples"].append({"when": "after_session_exit", "source": "driver", "data": s3})
        note("sampled_after_session_exit", raw=s3["raw"])
        # A second photograph a few seconds later, to show it is still there and
        # to prove the survival is not a sub-second artefact of sampling early.
        time.sleep(6)
        s4 = common.ps_sample(wpid, "after_session_exit_plus6s")
        s4["watcher_alive"] = common.alive(wpid) if wpid else None
        out["samples"].append({"when": "after_session_exit_plus6s", "source": "driver",
                               "data": s4})
        out["timing"] = {"eof_epoch": round(t_eof, 3), "claude_gone_epoch": round(t_gone, 3)}
        # let the exit-delay elapse so the run also records a clean shutdown
        end = time.time() + 120
        while wpid and common.alive(wpid) and time.time() < end:
            time.sleep(0.2)
        out["watcher_exited_after_delay"] = not common.alive(wpid) if wpid else None

    else:
        # ---- CHECK (b): trigger, then measure ----
        pre = common.ps_sample(wpid, "pre_trigger")
        out["samples"].append({"when": "pre_trigger", "source": "driver", "data": pre})
        n_before = len(watcher_log())

        if args.mode == "kill":
            t0 = time.time()
            out["sigkill_epoch"] = round(t0, 6)
            os.kill(cpid, 9)
            note("SIGKILL_sent", claude_pid=cpid, epoch=round(t0, 6))
            # Is the SIGKILLed process a reaped corpse or a zombie? kill(pid,0)
            # succeeds on a zombie, and `ps -o lstart=` still answers for one,
            # so both halves of the plan's liveness rule keep saying "alive".
            zomb = []
            for dt in (0.2, 1.0, 3.0, 6.0):
                while time.time() - t0 < dt:
                    time.sleep(0.02)
                p = subprocess.run(
                    ["ps", "-o", "pid,ppid,stat,lstart,command", "-p", str(cpid)],
                    capture_output=True, text=True)
                zomb.append({"at_seconds": dt, "rc": p.returncode,
                             "ps": p.stdout.rstrip("\n"),
                             "kill0": common.alive(cpid),
                             "lstart": wlib.start_token(cpid),
                             "socket_present": os.path.exists(sock)})
            out["zombie_probe"] = zomb
            note("zombie_probe", first=zomb[0]["ps"].splitlines()[-1] if zomb[0]["ps"] else "",
                 kill0_at_6s=zomb[-1]["kill0"])
            if args.reap:
                # Reparent the corpse to launchd by killing the process that has
                # not waited for it, so the realistic "parent reaps promptly"
                # case can be measured too.
                t0 = time.time()
                proc.kill()
                note("parent_expect_killed_to_force_reap", epoch=round(t0, 6))
        else:
            st = os.path.exists(sock)
            t0 = time.time()
            os.unlink(sock)
            note("socket_removed", socket=sock, existed_before=st,
                 epoch=round(t0, 6), claude_alive=common.alive(cpid))

        # poll for the watcher process to disappear
        t_gone = None
        end = t0 + 60
        while time.time() < end:
            if not common.alive(wpid):
                t_gone = time.time()
                break
            time.sleep(0.02)

        recs = watcher_log()
        by = {}
        for r in recs:
            if r.get("event") not in by:
                by[r["event"]] = r
        det = by.get("exit_condition_detected")
        clo = by.get("session_close_done")
        rem = by.get("pidfile_removed")

        closes = []
        cl = os.path.join(state, "session-close.ndjson")
        if os.path.exists(cl):
            closes = [json.loads(l) for l in open(cl) if l.strip()]

        out["trigger_epoch"] = round(t0, 6)
        out["measure"] = {
            "trigger": ("SIGKILL claude" + (" (t0 = the forced reap)" if args.reap else "")
                        if args.mode == "kill" else "unlink socket"),
            "seconds_measured_from": ("forced reap of the zombie" if args.reap
                                      else "the trigger itself"),
            "claude_alive_after_trigger": common.alive(cpid) if cpid else None,
            "watcher_exited": t_gone is not None,
            "seconds_to_detect": round(det["epoch"] - t0, 3) if det else None,
            "seconds_to_session_close": round(clo["epoch"] - t0, 3) if clo else None,
            "seconds_to_pidfile_removed": round(rem["epoch"] - t0, 3) if rem else None,
            "seconds_to_process_gone": round(t_gone - t0, 3) if t_gone else None,
            "within_5s": (round(t_gone - t0, 3) <= 5.0) if t_gone else False,
            "detect_reason": det.get("reason") if det else None,
            "session_close_ran": bool(closes),
            "session_close_before_exit": bool(clo and det and clo["epoch"] >= det["epoch"]),
            "session_close_records": closes,
            "pidfile_present_after": os.path.exists(
                wlib.pidfile_path(state, cpid)),
            "log_records_after_trigger": len(recs) - n_before,
            "signals_logged": [r for r in recs if r.get("event") == "signal_received"],
        }
        post = common.ps_sample(wpid, "post_trigger")
        out["samples"].append({"when": "post_trigger", "source": "driver", "data": post})

        if args.mode == "socket":
            # Does claude notice and re-create the socket? If it does, the
            # plan's socket-ENOENT exit condition is a FALSE POSITIVE: the
            # session is still there and the watcher has already quit.
            recheck = []
            for dt in (2, 5, 10, 20, 30, 45):
                while time.time() - t0 < dt:
                    time.sleep(0.05)
                recheck.append({"at_seconds": dt,
                                "socket_present": os.path.exists(sock),
                                "claude_alive": common.alive(cpid)})
            out["socket_recreation_probe"] = recheck
            note("socket_recreation_probe",
                 recreated=any(r["socket_present"] for r in recheck),
                 claude_alive_at_45s=recheck[-1]["claude_alive"])
        note("measured", **{k: v for k, v in out["measure"].items()
                            if k not in ("session_close_records", "signals_logged")})

        # let expect finish (it will /exit; in kill mode claude is already gone)
        try:
            proc.wait(timeout=args.hold + 180)
        except subprocess.TimeoutExpired:
            proc.kill()
        if common.alive(cpid):
            try:
                os.kill(cpid, 9)
            except OSError:
                pass

    raw.close()
    out["watcher_log"] = watcher_log()
    out["hook_env"] = [json.loads(l) for l in
                       open(os.path.join(state, "hook-env.log"))] \
        if os.path.exists(os.path.join(state, "hook-env.log")) else []
    hev = os.path.join(state, "hook-events.log")
    out["session_end_hook"] = [json.loads(l) for l in open(hev) if l.strip()] \
        if os.path.exists(hev) else []
    out["spawn_decision"] = read_json(os.path.join(state, "spawn-decision.json"))
    marks = []
    mp = os.path.join(results, "expect-marks.ndjson")
    if os.path.exists(mp):
        for line in open(mp):
            line = line.strip()
            if line:
                try:
                    marks.append(json.loads(line))
                except Exception:
                    pass
    out["expect_marks"] = marks
    out["config"] = guard.verify(results)
    out["dot_claude_json"] = guard.surgical_revert_projects(results, [project])
    shutil.rmtree(project, ignore_errors=True)

    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(out, f, indent=2)
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
