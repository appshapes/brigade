#!/usr/bin/env python3
"""E0-4 run 2 driver — the same idle-wake criterion, INTERACTIVE, driven by expect.

An interactive `claude` (no -p) is spawned on a pty by expect. The trust dialog
is answered, one ordinary prompt is typed, and the run then waits for that turn
to finish. From that point expect writes NOTHING to the pty: the frame is posted
out of band by the Go poster, which expect launches with `exec` (a child process,
not a keystroke). Every byte expect ever sends to the pty is logged by the `xsend`
wrapper, so "no stdin pending" is a fact on disk here too.

THE ECHO TRAP, and how this run avoids it. An interactive session renders an
injected message in the transcript, so any marker that appears verbatim inside
the frame would also appear on screen without the model having done anything.
The wake marker is therefore SPLIT: the frame asks for the letters WOKE followed
by a hex tail, and never contains the concatenation. Matching the joined token on
screen is proof that a model turn produced it.
"""

import argparse
import datetime
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin")
STATE = os.path.join(PLUGIN, "state")
PROJECT = os.path.join(HERE, "project")
POSTER = os.path.join(HERE, "poster", "e04poster")

LEAKY = ["CLAUDE_PID", "CLAUDE_CODE_SESSION_ID", "CLAUDE_CODE_MESSAGING_SOCKET",
         "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_ENTRYPOINT", "CLAUDECODE",
         "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_CODE_EXECPATH"]

EXPECT_TEMPLATE = r"""
# E0-4 interactive driver.
set timeout 240
log_file -a "{results}/interactive.log"
set marklog "{results}/expect-marks.ndjson"
set sendlog "{results}/pty-writes.log"

proc mark {{event args}} {{
    global marklog
    set ms [clock milliseconds]
    set rec "{{\"ms\": $ms, \"event\": \"$event\""
    foreach {{k v}} $args {{ append rec ", \"$k\": \"$v\"" }}
    append rec "}}"
    set f [open $marklog a]; puts $f $rec; close $f
    send_user "\n\[\[E04 $event ms=$ms\]\]\n"
}}

# Every byte that reaches the pty goes through here, and nowhere else.
proc xsend {{what note}} {{
    global sendlog
    set ms [clock milliseconds]
    set f [open $sendlog a]
    puts $f "{{\"ms\": $ms, \"note\": \"$note\", \"bytes\": [string length $what]}}"
    close $f
    send -- $what
}}

mark spawn
spawn -noecho env {unsets} claude --plugin-dir "{plugin}" --permission-mode bypassPermissions

# Trust dialog on a first run in a directory outside any trusted tree; harmless
# if it never appears (a subdirectory of a trusted project inherits the trust).
# The highlighted default is "No, exit", so a bare Enter QUITS the session:
# the selection must be moved down to "Yes, I trust this folder" first.
# The dialog is drawn as a box, so cursor-positioning escapes fall between the
# words: a multi-word regex never matches. Single words do.
expect {{
  -re {{trust|workspace}} {{
     mark trust_dialog_shown
     expect -re {{exit}}
     sleep 1
     xsend "\033\[B" "trust dialog: move selection off the default No, exit"
     sleep 1
     xsend "\r" "trust dialog: confirm Yes, I trust this folder"
     mark trust_dialog_accepted
  }}
  -re {{. for shortcuts|Welcome back|bypass permissions}} {{ mark ui_ready_no_trust }}
  timeout {{ mark trust_timeout }}
}}

sleep 4
mark send_first_prompt
xsend "{prompt}\r" "the one and only typed prompt"

# Wait for the first turn to finish. The token appears on screen TWICE: once as
# the terminal's echo of what was just typed, and once as the model's answer.
# Matching it only once would time the echo, so both are consumed.
expect {{
  -re {{{ready_re}}} {{ mark first_turn_prompt_echo }}
  timeout {{ mark first_turn_timeout }}
}}
expect {{
  -re {{{ready_re}}} {{ mark first_turn_done }}
  timeout {{ mark first_turn_timeout2 }}
}}

# ---- IDLE HOLD: nothing is written to the pty from here on. ----
mark idle_hold_start seconds {settle}
sleep {settle}
mark idle_hold_end

# ---- ISOLATION GATE: refuse to post if the env leaked. ----
# run1 gates its post on this; run2 used to check only afterwards, which would
# have let a leaked run post into the OUTER session and still look green.
# `exec` raises on a non-zero exit, so a leak aborts the run instead of posting.
mark isolation_gate_start
set isolated [exec python3 "{gate}" "{nested_env}"]
mark isolation_gate_ok

# ---- POST: the Go poster, as a child process, not a keystroke. ----
mark post_start
set posted [exec "{poster}" -env "{nested_env}" -frame "{frame}"]
mark post_done
set f [open "{results}/post.json" w]; puts $f $posted; close $f
set f [open "{results}/isolation-gate.json" w]; puts $f $isolated; close $f

set t_post [clock milliseconds]

# ---- WAKE: the joined token can only come from a model turn. ----
expect {{
  -re {{{wake_re}}} {{
     set t_wake [clock milliseconds]
     mark woke latency_ms [expr {{$t_wake - $t_post}}]
  }}
  timeout {{ mark wake_timeout latency_ms [expr {{[clock milliseconds] - $t_post}}] }}
}}

sleep 6
mark exiting
xsend "/exit\r" "shutdown"
expect eof
mark eof
"""


def iso(t=None):
    if t is None:
        t = time.time()
    return datetime.datetime.fromtimestamp(t).astimezone().isoformat()


def sha256_file(path):
    try:
        with open(path, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()
    except OSError:
        return ""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tag", default=None)
    ap.add_argument("--settle", type=int, default=20)
    ap.add_argument("--project-dir", default=None,
                    help="explicit project directory (used to reach a directory "
                         "outside any already-trusted tree)")
    ap.add_argument("--fresh-project", action="store_true",
                    help="run in a never-before-seen directory so the trust dialog "
                         "actually appears and is answered on the record")
    args = ap.parse_args()

    tag = args.tag or ("run2-" + time.strftime("%Y%m%d-%H%M%S"))
    results = os.path.join(HERE, "results", tag)
    os.makedirs(results, exist_ok=True)
    project = PROJECT
    if args.project_dir:
        project = args.project_dir
    elif args.fresh_project:
        project = os.path.join(HERE, "project-fresh-" + tag)
    os.makedirs(project, exist_ok=True)

    cfgdir = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")
    protected = [os.path.join(cfgdir, "settings.json"),
                 "/Users/rjae/Development/appshapes/brigade/CLAUDE.md",
                 os.path.expanduser("~/.claude/CLAUDE.md")]
    snap = os.path.join(results, "config-snapshot")
    os.makedirs(snap, exist_ok=True)
    pre = {}
    for i, p in enumerate(protected):
        pre[p] = sha256_file(p)
        if os.path.exists(p):
            shutil.copy2(p, os.path.join(snap, "%d.snap" % i))

    outer = {"session": os.environ.get("CLAUDE_CODE_SESSION_ID", ""),
             "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET", ""),
             "pid": os.environ.get("CLAUDE_PID", "")}
    otok = os.environ.get("CLAUDE_CODE_MESSAGING_TOKEN", "")
    outer["token_sha12"] = hashlib.sha256(otok.encode()).hexdigest()[:12] if otok else ""

    # ---- the split wake token ----
    tail = hashlib.sha256((tag + str(time.time())).encode()).hexdigest()[:4].upper()
    joined = "WOKE" + tail          # never appears in the frame
    ready = "E04-READY"

    body = ("Hi from the payments-api session. This is the E0-4 idle-wake probe. "
            "Reply with the four capital letters W, O, K, E immediately followed by "
            "the four characters " + tail + " — one unbroken eight-character token, "
            "on a line of its own, and nothing else. Do not run any tool and do not "
            "send a reply message; just print the token.")
    summary = "E0-4 interactive idle-wake probe (tail " + tail + ")"

    frame_path = os.path.join(STATE, "frame.txt")
    env = dict(os.environ)
    env["BRIGADE_E03_BODY"] = body
    env["BRIGADE_E03_SUMMARY"] = summary
    subprocess.run([sys.executable, os.path.join(HERE, "frame.py"),
                    "--variant", "A", "--out", frame_path], check=True, env=env,
                   stderr=open(os.path.join(results, "frame.meta"), "w"))
    frame_text = open(frame_path).read()
    if joined in frame_text:
        print("ABORT: the joined wake token leaks into the frame — echo trap open")
        return 1
    shutil.copy2(frame_path, os.path.join(results, "frame.txt"))

    for f in ("hook-env.log", "hook-events.log", "fake-brigade.ndjson"):
        open(os.path.join(STATE, f), "w").close()
    for f in ("nested-env.json", "hook-input.json"):
        try:
            os.unlink(os.path.join(STATE, f))
        except OSError:
            pass

    # The gate the expect script runs immediately before posting. The outer
    # identity is baked in here at generation time because the expect child's
    # environment has those variables stripped and cannot look them up.
    gate_path = os.path.join(results, "isolation-gate.py")
    with open(gate_path, "w") as f:
        f.write(
            "import hashlib, json, sys\n"
            "OUTER = " + repr({"socket": outer["socket"], "session": outer["session"],
                               "token_sha12": outer["token_sha12"]}) + "\n"
            "n = json.load(open(sys.argv[1]))\n"
            "t = hashlib.sha256(n.get('token','').encode()).hexdigest()[:12]\n"
            "leaked = [k for k, v in (('socket', n.get('socket') == OUTER['socket']),\n"
            "                         ('session', n.get('session') == OUTER['session']),\n"
            "                         ('token', t == OUTER['token_sha12'])) if v]\n"
            "if not n.get('socket') or not n.get('token'):\n"
            "    print('GATE FAIL: nested session published no inbox coordinates')\n"
            "    sys.exit(2)\n"
            "if leaked:\n"
            "    print('GATE FAIL: environment leaked -> ' + ','.join(leaked))\n"
            "    sys.exit(3)\n"
            "print(json.dumps({'gate': 'ok', 'nested_socket': n.get('socket'),\n"
            "                  'nested_session': n.get('session'),\n"
            "                  'nested_token_sha12': t, 'outer': OUTER}))\n")

    exp = EXPECT_TEMPLATE.format(
        results=results, plugin=PLUGIN, poster=POSTER, gate=gate_path,
        nested_env=os.path.join(STATE, "nested-env.json"), frame=frame_path,
        unsets=" ".join("-u " + v for v in LEAKY),
        prompt="Reply with exactly this and nothing else: " + ready,
        ready_re=ready, wake_re=joined, settle=args.settle)
    exp_path = os.path.join(results, "drive.exp")
    with open(exp_path, "w") as f:
        f.write(exp)

    child_env = {k: v for k, v in os.environ.items() if k not in LEAKY}
    t0 = time.time()
    with open(os.path.join(results, "interactive.raw"), "wb") as out:
        subprocess.run(["expect", "-f", exp_path], cwd=project, env=child_env,
                       stdout=out, stderr=subprocess.STDOUT, timeout=600)
    t1 = time.time()

    # ---- collect ----
    for f in ("hook-env.log", "hook-events.log", "fake-brigade.ndjson"):
        src = os.path.join(STATE, f)
        if os.path.exists(src):
            shutil.copy2(src, os.path.join(results, f))
    nested = {}
    nep = os.path.join(STATE, "nested-env.json")
    if os.path.exists(nep):
        with open(nep) as f:
            nested = json.load(f)
    disk_t = None
    if nested.get("session"):
        for root, _d, files in os.walk(os.path.join(cfgdir, "projects")):
            if nested["session"] + ".jsonl" in files:
                disk_t = os.path.join(root, nested["session"] + ".jsonl")
                break
    if disk_t:
        shutil.copy2(disk_t, os.path.join(results, "session-transcript.jsonl"))

    cfg = []
    for i, p in enumerate(protected):
        post_h = sha256_file(p)
        changed = post_h != pre[p]
        if changed and os.path.exists(os.path.join(snap, "%d.snap" % i)):
            shutil.copy2(os.path.join(snap, "%d.snap" % i), p)
        cfg.append({"path": p, "pre": pre[p], "post": post_h, "changed": changed})
    with open(os.path.join(results, "config-protection.json"), "w") as f:
        json.dump(cfg, f, indent=2)

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
    sends = []
    sp = os.path.join(results, "pty-writes.log")
    if os.path.exists(sp):
        for line in open(sp):
            line = line.strip()
            if line:
                try:
                    sends.append(json.loads(line))
                except Exception:
                    pass

    ntok = hashlib.sha256(nested.get("token", "").encode()).hexdigest()[:12]
    by = {m["event"]: m for m in marks}
    woke = "woke" in by

    # Did any keystroke reach the pty between the first turn ending and the post?
    t_done = by.get("first_turn_done", {}).get("ms")
    t_post = by.get("post_done", {}).get("ms")
    between = [s for s in sends if t_done and t_post and t_done <= s["ms"] <= t_post]

    verdict = {
        "tag": tag, "mode": "interactive-expect", "wake_token": joined,
        "project_dir": project, "fresh_project": args.fresh_project,
        "wake_token_in_frame": joined in frame_text,
        "wall_seconds": round(t1 - t0, 1),
        "isolation": {
            "outer": outer,
            "nested": {"socket": nested.get("socket"), "token_sha12": ntok,
                       "session": nested.get("session"),
                       "claude_pid": nested.get("claude_pid")},
            "ok": bool(nested.get("socket")) and nested.get("socket") != outer["socket"]
                  and ntok != outer["token_sha12"]
                  and nested.get("session") != outer["session"],
        },
        "config_protected": all(not c["changed"] for c in cfg),
        "marks": marks,
        "pty_writes": sends,
        "pty_writes_between_first_turn_and_post": between,
        "woke": woke,
        "wake_latency_ms": int(by["woke"]["latency_ms"]) if woke else None,
        "wake_within_10s": (int(by["woke"]["latency_ms"]) <= 10000) if woke else False,
        "idle_hold_seconds": args.settle,
        "transcript": disk_t,
    }
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2)
    print(json.dumps(verdict, indent=2))
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
