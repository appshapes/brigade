#!/usr/bin/env python3
"""E0-5 driver for CHECK (f): `claude --resume <native id>` WHILE THE ORIGINAL
PROCESS IS STILL ALIVE.

Session A is an expect-driven interactive session that stays up for the whole
run. Session B is `claude -p --resume <A's native session id>` in the SAME
throwaway project directory, launched while A is provably alive (kill(pidA,0)
is checked immediately before and after). Session C repeats B with
`--fork-session`, so the difference the flag makes is measured rather than
assumed.

What is recorded, all of it from the hooks' own records:

  * B's CLAUDE_PID, CLAUDE_CODE_MESSAGING_SOCKET and SessionStart `source`;
  * B's native session id -- if it equals A's, then a Brigade session keyed by
    NATIVE ID would have had two live processes pointing at one session, which
    is what 4.5.8's `session_live` guard refuses and 6.3's live-pidfile check
    avoids;
  * whether B gets its own watcher registry entry, i.e. whether the pidfile
    keyed by <claude_pid> keeps them apart where a native-id key would not;
  * the transcript path each one was given, and whether B's turn landed in A's
    transcript file (the docs' "interleaves both into one transcript" claim).
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
import g2  # noqa: E402
import wlib  # noqa: E402

HERE = common.HERE
PLUGIN = os.path.join(HERE, "plugin_c")

EXPECT_A = r"""
set timeout 300
log_file -a "{results}/A.log"
source "{lib}"
spawn -noecho env {unsets} claude --plugin-dir "{plugin}" --permission-mode bypassPermissions
expect {{
  -re {{trust}} {{ expect -re {{exit}}; sleep 1; send -- "\033\[B"; sleep 1; send -- "\r" }}
  -re {{shortcuts|Welcome|bypass}} {{ }}
  timeout {{ }}
}}
# Every wait DRAINS the pty (expectlib.tcl): a sleeping expect lets the TUI
# fill the pty buffer, and a `claude` blocked on write stops processing input.
waitfile "{ready1}" 600
nap 12
set f [open "{results}/A-ready" w]; puts $f [clock milliseconds]; close $f
# A stays alive and RESPONSIVE, untouched, until the driver says to go.
waitfile "{done}" 1200
send -- "/exit\r"
expect eof
set f [open "{results}/A-eof" w]; puts $f [clock milliseconds]; close $f
"""


def transcript_stat(path):
    if not path or not os.path.exists(path):
        return {"path": path, "exists": False}
    st = os.stat(path)
    n = sum(1 for _ in open(path, "rb"))
    return {"path": path, "exists": True, "bytes": st.st_size, "lines": n,
            "mtime": round(st.st_mtime, 3)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--fork", action="store_true",
                    help="also run a --fork-session arm")
    a = ap.parse_args()

    tag = "f-%s" % time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results2", tag)
    stateA = os.path.join(HERE, "state2", tag, "A")
    stateB = os.path.join(HERE, "state2", tag, "B")
    stateC = os.path.join(HERE, "state2", tag, "C")
    for d in (results, stateA, stateB, stateC):
        os.makedirs(d, mode=0o700, exist_ok=True)
    project = "/tmp/e05-projects/%s" % tag
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    topguard = g2.TopKeyGuard(results)
    outer = common.outer_identity()

    ready1 = os.path.join(stateA, "ready-1.json")
    done = os.path.join(results, "driver-done")
    exp = EXPECT_A.format(results=results, plugin=PLUGIN, ready1=ready1, done=done,
                          lib=os.path.join(HERE, "expectlib.tcl"),
                          unsets=" ".join(common.env_unset_args()))
    open(os.path.join(results, "A.exp"), "w").write(exp)

    out = {"tag": tag, "results": results, "project_dir": project,
           "stripped_env_names": common.leaky_names(), "events": []}

    def note(ev, **kw):
        r = {"epoch": round(time.time(), 3), "event": ev}
        r.update(kw)
        out["events"].append(r)
        print("[f] " + json.dumps(r))

    envA = common.child_env({"BRIGADE_STATE_DIR": stateA, "BRIGADE_E05_HOME": HERE,
                             "BRIGADE_E05_TAG": tag + "-A", "BRIGADE_E05_SPAWN": "1"})
    note("launch_A")
    pA = subprocess.Popen(["expect", "-f", os.path.join(results, "A.exp")],
                          cwd=project, env=envA,
                          stdout=open(os.path.join(results, "A.raw"), "wb"),
                          stderr=subprocess.STDOUT)
    if not g2.waitfor(ready1, 700):
        note("A_STARTUP_FAILED")
        pA.kill()
        out["aborted"] = "A never started"
        json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
        return 3

    nestedA = g2.read_json(os.path.join(stateA, "nested-env-1.json")) or {}
    gate = g2.gate(nestedA, outer)
    out["isolation_A"] = gate
    if not gate["ok"]:
        note("ISOLATION_GATE_FAIL", leaks=gate["leaks"])
        pA.kill()
        out["aborted"] = "isolation gate"
        json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
        return 2
    note("isolation_gate_ok_A", nested=gate["nested"])

    recA = g2.read_ndjson(os.path.join(stateA, "startup.ndjson"))[0]
    pidA = int(recA["env_claude_pid"])
    sidA = recA["env_session_id"]
    out["A"] = {"claude_pid": pidA, "native_session_id": sidA,
                "socket": recA["env_socket"], "token_sha12": recA["env_token_sha12"],
                "transcript": recA["payload_transcript_path"],
                "source": recA["payload_source"]}
    note("A_up", **out["A"])
    g2.waitfor(os.path.join(results, "A-ready"), 400)

    # A session with no completed turn is NOT resumable: `--resume <id>` answers
    # "No conversation found with session ID: <id>" and /resume's picker lists
    # nothing. So A is given one real turn first, out of band through its inbox
    # socket -- nothing is typed at A's pty.
    marker = "/tmp/e05f-%s-A" % tag
    try:
        os.unlink(marker)
    except OSError:
        pass
    body = ("Using the Bash tool, run exactly this one command and then stop:\n\n"
            "    touch %s\n\nNothing else." % marker)
    pr = subprocess.run([sys.executable, os.path.join(HERE, "post.py"), "--coords",
                         os.path.join(stateA, "nested-env-1.json"), "--body", body,
                         "--log", os.path.join(stateA, "posts.ndjson"),
                         "--label", "A-warmup"], capture_output=True, text=True)
    warm = g2.waitfor(marker, 240)
    out["A_warmup_turn"] = {"posted": pr.stdout.strip()[:200], "turn_completed": warm}
    note("A_warmup_turn", turn_completed=warm)
    time.sleep(25)  # let the turn finish flushing to the transcript
    try:
        os.unlink(marker)
    except OSError:
        pass

    out["A_transcript_before"] = transcript_stat(recA["payload_transcript_path"])

    def resume_arm(label, statedir, extra_flags, marker_text):
        env = common.child_env({"BRIGADE_STATE_DIR": statedir, "BRIGADE_E05_HOME": HERE,
                                "BRIGADE_E05_TAG": tag + "-" + label,
                                "BRIGADE_E05_SPAWN": "1"})
        alive_before = common.alive(pidA)
        cmd = (["env"] + common.env_unset_args() +
               ["claude", "-p", marker_text, "--resume", sidA,
                "--plugin-dir", PLUGIN, "--permission-mode", "bypassPermissions"]
               + extra_flags)
        t0 = time.time()
        p = subprocess.run(cmd, cwd=project, env=env, stdin=subprocess.DEVNULL,
                           capture_output=True, text=True, timeout=600)
        alive_after = common.alive(pidA)
        recs = g2.read_ndjson(os.path.join(statedir, "startup.ndjson"))
        rec = recs[0] if recs else None
        res = {
            "label": label, "flags": extra_flags,
            "cmd": "claude -p <prompt> --resume %s%s" % (sidA, " " + " ".join(extra_flags) if extra_flags else ""),
            "rc": p.returncode, "seconds": round(time.time() - t0, 2),
            "stdout_head": p.stdout[:400], "stderr_head": p.stderr[:600],
            "A_alive_before": alive_before, "A_alive_after": alive_after,
            "started": bool(rec),
        }
        if rec:
            res.update({
                "claude_pid": rec["env_claude_pid"],
                "native_session_id": rec["env_session_id"],
                "payload_session_id": rec["payload_session_id"],
                "socket": rec["env_socket"],
                "token_sha12": rec["env_token_sha12"],
                "source": rec["payload_source"],
                "transcript": rec["payload_transcript_path"],
                "same_native_id_as_A": rec["env_session_id"] == sidA,
                "same_pid_as_A": rec["env_claude_pid"] == str(pidA),
                "same_socket_as_A": rec["env_socket"] == recA["env_socket"],
                "same_transcript_as_A": rec["payload_transcript_path"] == recA["payload_transcript_path"],
            })
            wd = os.path.join(statedir, "watchers")
            res["own_registry_entry"] = sorted(os.listdir(wd)) if os.path.isdir(wd) else []
        note("arm_%s" % label, **{k: v for k, v in res.items()
                                  if k in ("started", "rc", "claude_pid",
                                           "native_session_id", "same_native_id_as_A",
                                           "same_transcript_as_A", "A_alive_after",
                                           "source")})
        return res

    out["B_resume_while_alive"] = resume_arm(
        "B", stateB, [], "Reply with exactly: E05F_B_RESUMED")
    out["A_transcript_after_B"] = transcript_stat(recA["payload_transcript_path"])

    if a.fork:
        out["C_resume_fork_while_alive"] = resume_arm(
            "C", stateC, ["--fork-session"], "Reply with exactly: E05F_C_FORKED")
        out["A_transcript_after_C"] = transcript_stat(recA["payload_transcript_path"])

    # A shared registry keyed by <claude_pid> keeps them apart; one keyed by the
    # NATIVE id would not. Show both keyings side by side.
    ids = [("A", str(pidA), sidA)]
    for k in ("B_resume_while_alive", "C_resume_fork_while_alive"):
        r = out.get(k)
        if r and r.get("started"):
            ids.append((r["label"], r["claude_pid"], r["native_session_id"]))
    out["keying"] = {
        "rows": [{"arm": x[0], "claude_pid": x[1], "native_session_id": x[2]} for x in ids],
        "distinct_claude_pids": len({x[1] for x in ids}),
        "distinct_native_session_ids": len({x[2] for x in ids}),
        "by_native_id_would_collide": len({x[2] for x in ids}) < len(ids),
    }
    note("keying", **{k: v for k, v in out["keying"].items() if k != "rows"})

    # transcript directory census
    tdir = os.path.dirname(recA["payload_transcript_path"])
    out["transcript_dir"] = {
        "dir": tdir,
        "files": sorted(os.listdir(tdir)) if os.path.isdir(tdir) else [],
    }

    open(done, "w").write("go")
    try:
        pA.wait(timeout=300)
    except subprocess.TimeoutExpired:
        pA.kill()
    time.sleep(2)
    if common.alive(pidA):
        try:
            os.kill(pidA, 15)
        except OSError:
            pass

    out["transcript_dir_after_exit"] = {
        "files": sorted(os.listdir(tdir)) if os.path.isdir(tdir) else [],
    }
    out["A_transcript_final"] = transcript_stat(recA["payload_transcript_path"])
    out["sessionend_A"] = g2.read_ndjson(os.path.join(stateA, "sessionend.ndjson"))
    out["sessionend_B"] = g2.read_ndjson(os.path.join(stateB, "sessionend.ndjson"))
    out["startup_A"] = g2.read_ndjson(os.path.join(stateA, "startup.ndjson"))
    out["startup_B"] = g2.read_ndjson(os.path.join(stateB, "startup.ndjson"))
    out["startup_C"] = g2.read_ndjson(os.path.join(stateC, "startup.ndjson"))

    left = []
    for d in (stateA, stateB, stateC):
        wd = os.path.join(d, "watchers")
        for fn in sorted(os.listdir(wd)) if os.path.isdir(wd) else []:
            e = wlib.read_pidfile(os.path.join(wd, fn))
            if e and common.alive(e["pid"]):
                left.append({"state": os.path.basename(d), "watcher_pid": e["pid"]})
                try:
                    os.kill(e["pid"], 15)
                except OSError:
                    pass
    out["watchers_killed_on_teardown"] = left

    out["config"] = guard.verify(results)
    out["dot_claude_json"] = topguard.revert([project])
    shutil.rmtree(project, ignore_errors=True)
    json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
