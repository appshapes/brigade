#!/usr/bin/env python3
"""E0-5 driver for CHECK (c): /clear (and /resume, /fork) in an expect-driven
interactive session.

What each run establishes, all from records written by the hooks themselves:

  * whether SessionEnd(reason=clear) is followed by SessionStart(source=clear);
  * whether the NATIVE session id is new while CLAUDE_PID stays the same;
  * how many watchers are left afterwards -- the spawn/liveness routine runs on
    EVERY SessionStart, so a second one would show up as a second pidfile or a
    second live process;
  * THE D9 QUESTION: does CLAUDE_CODE_MESSAGING_SOCKET or the SHA-256 of
    CLAUDE_CODE_MESSAGING_TOKEN change across the boundary? The SessionStart
    hook does that comparison itself, against its own record #1.
  * whether a post made with the STARTUP coordinates -- the ones the watcher is
    holding -- is still DELIVERED after the boundary. Delivery is proved by a
    file the model can only create by running a tool: nothing is typed at the
    pty after the boundary, so an echo cannot forge it.

Two expect facts inherited from E0-4: the trust dialog's highlighted default is
"No, exit" (send Down then Enter), and only SINGLE-word regexes match the box.
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

EXPECT = r"""
set timeout 300
log_file -a "{results}/interactive.log"
source "{lib}"
proc mark {{event args}} {{
    set ms [clock milliseconds]
    set f [open "{results}/marks.ndjson" a]
    puts $f "{{\"ms\": $ms, \"event\": \"$event\", \"args\": \"$args\"}}"
    close $f
    send_user "\n\[\[E05C $event $args ms=$ms\]\]\n"
}}
proc touch {{p}} {{ set f [open $p w]; puts $f [clock milliseconds]; close $f }}

mark spawn
spawn -noecho env {unsets} claude --plugin-dir "{plugin}" --permission-mode bypassPermissions
expect {{
  -re {{trust}} {{ mark trust_shown; expect -re {{exit}}; sleep 1
                  send -- "\033\[B"; sleep 1; send -- "\r"; mark trust_accepted }}
  -re {{shortcuts|Welcome|bypass}} {{ mark ui_ready_no_trust }}
  timeout {{ mark trust_timeout }}
}}

# Never a fixed sleep after the trust dialog: startup latency was measured at
# 1 s to 330 s in the previous stage. Wait for the hook's own ready file --
# and wait by DRAINING the pty (see expectlib.tcl), never by sleeping, or the
# TUI fills the pty buffer and `claude` blocks on write.
mark hook_ready_1 waited [waitfile "{ready1}" 600]
nap 12
touch "{results}/ui-ready-1"
mark ui_ready_1

# The driver takes its BEFORE sample and runs the CONTROL post here: a post
# made before the boundary, on the same coordinates, proves the delivery
# mechanism works in this harness -- without it, a failure after the boundary
# would be unattributable.
mark driver_go waited [waitfile "{go}" 600]
{warmup}
{pre_boundary}

# ---- the boundary under test ----
mark boundary_send cmd "{slash}"
send -- "{slash}"
nap 2
send -- "\r"
{extra_keys}
set w [waitfile "{ready3}" 30]
if {{$w < 0}} {{
   # The slash-command menu may have swallowed the first Enter as a completion.
   mark boundary_retry_enter
   send -- "\r"
   set w [waitfile "{ready3}" 45]
}}
if {{$w >= 0}} {{ mark boundary_done waited $w }} else {{ mark boundary_no_second_sessionstart }}
nap 5
touch "{results}/ui-ready-2"
mark ui_ready_2

# ---- IDLE HOLD. Nothing is written to the pty from here until /exit, but the
# pty is still drained, so `claude` keeps running. ----
mark hold_start seconds {hold}
nap {hold}
mark hold_end

send -- "/exit\r"
expect eof
mark eof
touch "{results}/eof"
"""


def watcher_census(state, cpid):
    wd = os.path.join(state, "watchers")
    files = sorted(os.listdir(wd)) if os.path.isdir(wd) else []
    entries = []
    for fn in files:
        p = os.path.join(wd, fn)
        e = wlib.read_pidfile(p)
        st = os.stat(p)
        entries.append({"file": fn, "mode": oct(st.st_mode & 0o777), "inode": st.st_ino,
                        "entry": e,
                        "watcher_alive": common.alive(e["pid"]) if e else None,
                        "evaluation": wlib.evaluate(e)})
    live = []
    r = subprocess.run(["ps", "-axo", "pid,ppid,command"], capture_output=True, text=True)
    for line in r.stdout.splitlines():
        if "detach.py" in line and "--claude-pid %d" % cpid in line:
            live.append(line.strip())
    return {"pidfiles": files, "n_pidfiles": len(files), "entries": entries,
            "live_watcher_processes": live, "n_live_watcher_processes": len(live)}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--mode", required=True, choices=["clear", "resume", "fork"])
    ap.add_argument("--hold", type=int, default=150)
    ap.add_argument("--via-clear", action="store_true",
                    help="run a /clear before the boundary, so /resume has a "
                         "conversation on disk to list")
    ap.add_argument("--warmup", action="store_true",
                    help="type one ordinary prompt before the boundary")
    ap.add_argument("--preseed", action="store_true",
                    help="run a throwaway `claude -p` in the project first, so "
                         "/resume's picker has something to list -- in a fresh "
                         "directory it reports 'No conversations found' and the "
                         "modal stays open")
    a = ap.parse_args()

    slash = {"clear": "/clear", "resume": "/resume", "fork": "/fork"}[a.mode]
    # /resume's picker lists NOTHING in a fresh project -- not even a session a
    # `claude -p` just created there. `/clear`'s own description says the
    # previous session "stays on disk (resumable with ...)", so a /clear run
    # first is what gives the picker something to select.
    pre_boundary = ('mark pre_clear_send\nsend -- "/clear"\nnap 2\nsend -- "\\r"\n'
                    'mark pre_clear_wait waited [waitfile "{ready2}" 45]\nnap 20'
                    if a.via_clear else "")
    # /resume opens a picker: after the command is submitted, one more Enter
    # selects the highlighted entry.
    extra_keys = ('nap 4\nsend -- "\\r"\nmark picker_enter\nnap 3' if a.mode == "resume" else "")
    # /fork answers "Nothing to fork yet. Send a message first." unless the
    # conversation already has a TYPED turn, so fork runs get a warm-up prompt.
    warmup = ('mark warmup_send\nsend -- "Reply with exactly: E05WARM\\r"\nnap 25\n'
              'mark warmup_done' if a.warmup else "")

    tag = "c-%s-%s" % (a.mode, time.strftime("%Y%m%d-%H%M%S"))
    results = os.path.join(HERE, "results2", tag)
    state = os.path.join(HERE, "state2", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)
    scratch = os.environ.get("E05_SCRATCH") or "/tmp"
    project = os.path.join(scratch, "e05-projects", tag)
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    topguard = g2.TopKeyGuard(results)
    outer = common.outer_identity()

    ready1 = os.path.join(state, "ready-1.json")
    ready2 = os.path.join(state, "ready-2.json")
    # With --via-clear the /clear consumes ready-2, so the boundary's own
    # SessionStart is #3.
    ready3 = os.path.join(state, "ready-3.json" if a.via_clear else "ready-2.json")
    go = os.path.join(state, "driver-go")
    marker0 = "/tmp/e05c-%s-pre" % tag      # control: delivery BEFORE the boundary
    marker = "/tmp/e05c-%s-post" % tag      # the test: startup coords AFTER it
    marker2 = "/tmp/e05c-%s-cur" % tag      # control: current coords after it
    for m in (marker0, marker, marker2):
        try:
            os.unlink(m)
        except OSError:
            pass

    exp = EXPECT.format(results=results, plugin=PLUGIN, ready1=ready1, ready2=ready2,
                        go=go, slash=slash, extra_keys=extra_keys, hold=a.hold,
                        ready3=ready3,
                        warmup=warmup, pre_boundary=pre_boundary.format(ready2=ready2),
                        lib=os.path.join(HERE, "expectlib.tcl"),
                        unsets=" ".join(common.env_unset_args()))
    with open(os.path.join(results, "drive.exp"), "w") as f:
        f.write(exp)

    env = common.child_env({"BRIGADE_STATE_DIR": state, "BRIGADE_E05_HOME": HERE,
                            "BRIGADE_E05_TAG": tag, "BRIGADE_E05_SPAWN": "1"})

    out = {"tag": tag, "mode": a.mode, "slash": slash, "results": results,
           "state_dir": state, "project_dir": project,
           "stripped_env_names": common.leaky_names(), "events": []}

    def note(ev, **kw):
        r = {"epoch": round(time.time(), 3), "event": ev}
        r.update(kw)
        out["events"].append(r)
        print("[c] " + json.dumps(r))

    if a.preseed:
        seedstate = os.path.join(state, "seed")
        os.makedirs(seedstate, mode=0o700, exist_ok=True)
        senv = common.child_env({"BRIGADE_STATE_DIR": seedstate,
                                 "BRIGADE_E05_HOME": HERE,
                                 "BRIGADE_E05_TAG": tag + "-seed",
                                 "BRIGADE_E05_SPAWN": "0"})
        sp = subprocess.run(["env"] + common.env_unset_args() +
                            ["claude", "-p", "Reply with exactly: E05_SEED",
                             "--plugin-dir", PLUGIN,
                             "--permission-mode", "bypassPermissions"],
                            cwd=project, env=senv, stdin=subprocess.DEVNULL,
                            capture_output=True, text=True, timeout=600)
        seed = g2.read_ndjson(os.path.join(seedstate, "startup.ndjson"))
        out["preseed"] = {"rc": sp.returncode,
                          "session_id": seed[0]["env_session_id"] if seed else None,
                          "claude_pid": seed[0]["env_claude_pid"] if seed else None}
        note("preseeded", **out["preseed"])

    note("spawn")
    proc = subprocess.Popen(["expect", "-f", os.path.join(results, "drive.exp")],
                            cwd=project, env=env,
                            stdout=open(os.path.join(results, "interactive.raw"), "wb"),
                            stderr=subprocess.STDOUT)

    if not g2.waitfor(ready1, 700):
        note("STARTUP_FAILED")
        proc.kill()
        out["aborted"] = "no SessionStart within 700 s"
        out["config"] = guard.verify(results)
        out["dot_claude_json"] = topguard.revert([project])
        shutil.rmtree(project, ignore_errors=True)
        json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
        return 3

    coords1 = os.path.join(state, "nested-env-1.json")
    nested = g2.read_json(coords1) or {}
    gate = g2.gate(nested, outer)
    out["isolation"] = gate
    if not gate["ok"]:
        note("ISOLATION_GATE_FAIL", leaks=gate["leaks"])
        proc.kill()
        out["aborted"] = "isolation gate"
        out["config"] = guard.verify(results)
        out["dot_claude_json"] = topguard.revert([project])
        shutil.rmtree(project, ignore_errors=True)
        json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
        return 2
    note("isolation_gate_ok", nested=gate["nested"])

    cpid = int(nested["claude_pid"])
    out["claude_pid"] = cpid
    g2.waitfor(os.path.join(results, "ui-ready-1"), 300)
    time.sleep(2)
    out["watchers_before"] = watcher_census(state, cpid)
    note("watchers_before", n_pidfiles=out["watchers_before"]["n_pidfiles"],
         n_live=out["watchers_before"]["n_live_watcher_processes"])

    postlog = os.path.join(state, "posts.ndjson")

    def body_for(m):
        return ("Tool-use request from the Brigade E0-5 probe. Using the Bash tool, run "
                "exactly this one command and then stop:\n\n    touch %s\n\n"
                "Do not ask for confirmation and do not do anything else." % m)

    def do_post(coords, m, label, wait=200):
        t = time.time()
        r = subprocess.run([sys.executable, os.path.join(HERE, "post.py"),
                            "--coords", coords, "--body", body_for(m),
                            "--log", postlog, "--label", label],
                           capture_output=True, text=True)
        rec = (json.loads(r.stdout) if r.stdout.strip()
               else {"posted": False, "stderr": r.stderr[:2000]})
        ok = g2.waitfor(m, wait) if rec.get("posted") else False
        res = {"label": label, "coords": os.path.basename(coords), "post": rec,
               "marker": m, "delivered": ok,
               "seconds_to_marker": round(time.time() - t, 2) if ok else None}
        note("post", label=label, posted=rec.get("posted"), delivered=ok,
             seconds=res["seconds_to_marker"], error=rec.get("error"))
        return res

    # ---- CONTROL: a post BEFORE the boundary, same coordinates. Without this a
    # post-boundary failure could not be told apart from a harness that never
    # delivered anything. ----
    out["control_before_boundary"] = do_post(coords1, marker0, "control-before-boundary")

    # The control post's turn is still finishing when its marker file appears.
    # A slash command typed into a busy session is QUEUED as a message, not
    # executed ("Press up to edit queued messages"), which jams the run. Settle
    # first.
    note("settling_before_boundary", seconds=30)
    time.sleep(30)

    # ---- release expect into the boundary ----
    with open(go, "w") as f:
        f.write(str(time.time()))
    t_boundary = time.time()
    note("boundary_released", slash=slash)

    got2 = g2.waitfor(ready3, 120)
    out["second_sessionstart"] = got2
    note("boundary_result", second_sessionstart=got2,
         seconds=round(time.time() - t_boundary, 2))
    time.sleep(3)

    starts = g2.read_ndjson(os.path.join(state, "startup.ndjson"))
    ends = g2.read_ndjson(os.path.join(state, "sessionend.ndjson"))
    out["startup_records"] = starts
    out["sessionend_records"] = ends
    out["watchers_after"] = watcher_census(state, cpid)
    note("watchers_after", n_pidfiles=out["watchers_after"]["n_pidfiles"],
         n_live=out["watchers_after"]["n_live_watcher_processes"])

    # ---- THE TEST: post with the STARTUP coordinates, from the far side of the
    # boundary. Nothing has been typed at the pty since the slash command. ----
    out["post_after_boundary_startup_coords"] = do_post(
        coords1, marker, "startup-coords-after-%s" % a.mode)

    # control: the same post with whatever coordinates a SessionStart(clear)
    # would have published, when there is one
    coords2 = os.path.join(state, "nested-env-%d.json" % (3 if a.via_clear else 2))
    if os.path.exists(coords2):
        out["post_after_boundary_current_coords"] = do_post(
            coords2, marker2, "post-%s-coords" % a.mode)

    out["posts"] = g2.read_ndjson(postlog)
    out["prompt_records"] = g2.read_ndjson(os.path.join(state, "prompt.ndjson"))

    try:
        proc.wait(timeout=a.hold + 300)
    except subprocess.TimeoutExpired:
        proc.kill()
    time.sleep(2)
    if common.alive(cpid):
        try:
            os.kill(cpid, 15)
        except OSError:
            pass
        time.sleep(2)

    out["startup_records"] = g2.read_ndjson(os.path.join(state, "startup.ndjson"))
    out["sessionend_records"] = g2.read_ndjson(os.path.join(state, "sessionend.ndjson"))
    out["spawn_decisions"] = g2.read_ndjson(os.path.join(state, "spawn.ndjson"))
    out["watchers_at_end"] = watcher_census(state, cpid)
    out["marks"] = g2.read_ndjson(os.path.join(results, "marks.ndjson"))
    out["prompt_records"] = g2.read_ndjson(os.path.join(state, "prompt.ndjson"))
    out["matched_sessionstart_records"] = g2.read_ndjson(
        os.path.join(state, "matched", "startup.ndjson"))

    # cleaned screen text, for the record: it is the only place a slash command
    # that silently did nothing would be visible
    try:
        import re
        d = open(os.path.join(results, "interactive.log"), "rb").read().decode("utf8", "replace")
        d = re.sub(r"\x1b\[[0-9;?]*[a-zA-Z]", "", d)
        d = re.sub(r"\x1b\][^\x07]*\x07", "", d)
        lines = [l.rstrip() for l in d.replace("\r", "\n").split("\n") if l.strip()]
        out["screen_tail"] = lines[-60:]
    except Exception as e:
        out["screen_tail"] = ["<unreadable: %s>" % e]

    # tidy any watcher this run left behind
    left = []
    for e in out["watchers_at_end"]["entries"]:
        pid = (e.get("entry") or {}).get("pid")
        if pid and common.alive(pid):
            left.append(pid)
            try:
                os.kill(pid, 15)
            except OSError:
                pass
    out["watchers_killed_on_teardown"] = left
    for m in (marker, marker2):
        try:
            os.unlink(m)
        except OSError:
            pass

    out["config"] = guard.verify(results)
    out["dot_claude_json"] = topguard.revert([project])
    shutil.rmtree(project, ignore_errors=True)
    json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
