#!/usr/bin/env python3
"""E0-4 run 1 driver — idle wake of a stream-json `-p` session.

Starts a NESTED Claude Code session:

    claude -p --input-format stream-json --output-format stream-json --verbose
           --plugin-dir <probe plugin>

writes ONE ordinary short prompt on stdin, waits for that turn's `result`,
holds stdin OPEN and writes NOTHING further, then posts a 6.7 frame into the
nested session's inbox socket with the Go poster and records, with timestamps,
every stdout event that follows.

PASS = a new assistant turn appears on stdout within 10 s of the post without any
stdin input.

Everything the criterion asks to be proven is logged rather than asserted:

  stdin-writes.log     every byte ever written to the child's stdin, with the
                       wall-clock time of the write and the running total, so
                       "stdin was untouched after the first result" is a fact on
                       disk and not a claim.
  events.ndjson        every stdout line, with its arrival time and its offset
                       from both the first result and the frame write.
  idle-probe.json      `ps` state/CPU of the child sampled during the settle
                       window and at post time, so "idle, not still working" is
                       evidence rather than inference.

Environment isolation: exactly the eight leaking CLAUDE_* variables are stripped
from the nested invocation; CLAUDE_CONFIG_DIR is kept so the nested session stays
authenticated. The outer and nested socket/token/session are compared and the run
fails if they match.
"""

import argparse
import datetime
import hashlib
import json
import os
import shutil
import subprocess
import sys
import threading
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin")
STATE = os.path.join(PLUGIN, "state")
PROJECT = os.path.join(HERE, "project")
POSTER = os.path.join(HERE, "poster", "e04poster")

# The eight variables the outer Claude Code session exports that MUST NOT reach a
# nested session. CLAUDE_CONFIG_DIR is deliberately absent: it carries auth.
LEAKY = [
    "CLAUDE_PID",
    "CLAUDE_CODE_SESSION_ID",
    "CLAUDE_CODE_MESSAGING_SOCKET",
    "CLAUDE_CODE_MESSAGING_TOKEN",
    "CLAUDE_CODE_ENTRYPOINT",
    "CLAUDECODE",
    "CLAUDE_CODE_CHILD_SESSION",
    "CLAUDE_CODE_EXECPATH",
]


def iso(t=None):
    if t is None:
        t = time.time()
    return datetime.datetime.fromtimestamp(t).astimezone().strftime(
        "%Y-%m-%dT%H:%M:%S.%f%z")


def sha256_file(path):
    try:
        with open(path, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()
    except OSError:
        return ""


def ps_probe(pid):
    """One `ps` sample of the child: run state and CPU. An idle Claude Code
    process sits in state S (sleeping) with near-zero recent CPU."""
    try:
        out = subprocess.run(
            ["ps", "-o", "state=,%cpu=,time=,etime=", "-p", str(pid)],
            capture_output=True, text=True, timeout=5).stdout.strip()
    except Exception as e:  # pragma: no cover - diagnostic path
        return {"error": str(e)}
    parts = out.split()
    if len(parts) < 4:
        return {"raw": out}
    return {"state": parts[0], "cpu_pct": parts[1], "cpu_time": parts[2],
            "elapsed": parts[3], "raw": out}


class StdinLog:
    """The child's stdin, wrapped so no byte can reach it unrecorded."""

    def __init__(self, pipe, path):
        self.pipe = pipe
        self.path = path
        self.total = 0
        self.writes = 0
        with open(self.path, "w") as f:
            f.write(json.dumps({"ts": iso(), "event": "stdin_log_open"}) + "\n")

    def write(self, data, note=""):
        b = data.encode("utf-8") if isinstance(data, str) else data
        self.total += len(b)
        self.writes += 1
        rec = {"ts": iso(), "event": "stdin_write", "seq": self.writes,
               "bytes": len(b), "running_total_bytes": self.total,
               "note": note, "payload": b.decode("utf-8", "replace")}
        with open(self.path, "a") as f:
            f.write(json.dumps(rec) + "\n")
        self.pipe.write(b)
        self.pipe.flush()

    def note(self, event, **kw):
        rec = {"ts": iso(), "event": event,
               "writes_so_far": self.writes,
               "bytes_so_far": self.total}
        rec.update(kw)
        with open(self.path, "a") as f:
            f.write(json.dumps(rec) + "\n")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tag", default=None)
    ap.add_argument("--settle", type=float, default=3.0,
                    help="seconds to hold idle after the first result before posting")
    ap.add_argument("--wake-window", type=float, default=10.0,
                    help="the criterion's window: seconds allowed from post to a new assistant turn")
    ap.add_argument("--observe", type=float, default=45.0,
                    help="total seconds to keep watching stdout after the post")
    ap.add_argument("--prompt", default="Reply with exactly this and nothing else: E04-READY")
    ap.add_argument("--nonce", default=None)
    ap.add_argument("--control", default="none",
                    choices=["none", "no-stream-input", "close-stdin", "no-post"],
                    help="counterfactuals that isolate WHY the wake is possible: "
                         "no-stream-input drops --input-format stream-json and "
                         "passes the prompt as an argument; close-stdin keeps the "
                         "flag but sends EOF right after the prompt; no-post is "
                         "the NULL POST — an identical idle session that is never "
                         "posted to, to prove an idle session does not emit a turn "
                         "on its own and that the wake is caused by the post")
    args = ap.parse_args()

    tag = args.tag or time.strftime("%Y%m%d-%H%M%S")
    nonce = args.nonce or ("E04WAKE-" + hashlib.sha256(
        (tag + str(time.time())).encode()).hexdigest()[:8].upper())
    results = os.path.join(HERE, "results", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(PROJECT, exist_ok=True)
    os.makedirs(STATE, exist_ok=True)

    cfgdir = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")

    # ---- config protection: snapshot + hash BEFORE ----
    protected = [
        os.path.join(cfgdir, "settings.json"),
        "/Users/rjae/Development/appshapes/brigade/CLAUDE.md",
        os.path.expanduser("~/.claude/CLAUDE.md"),
    ]
    snap = os.path.join(results, "config-snapshot")
    os.makedirs(snap, exist_ok=True)
    pre = {}
    for i, p in enumerate(protected):
        pre[p] = sha256_file(p)
        if os.path.exists(p):
            shutil.copy2(p, os.path.join(snap, "%d.snap" % i))

    # ---- outer identity, for the isolation proof ----
    outer = {
        "session": os.environ.get("CLAUDE_CODE_SESSION_ID", ""),
        "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET", ""),
        "pid": os.environ.get("CLAUDE_PID", ""),
    }
    otok = os.environ.get("CLAUDE_CODE_MESSAGING_TOKEN", "")
    outer["token_sha12"] = hashlib.sha256(otok.encode()).hexdigest()[:12] if otok else ""

    # ---- build the frame (variant A, plan 6.7) with a per-run nonce ----
    frame_path = os.path.join(STATE, "frame.txt")
    body = ("Hi from the payments-api session. This is the E0-4 idle-wake probe. "
            "Do not run any tool. Reply with exactly this one token and nothing else: "
            + nonce)
    env = dict(os.environ)
    env["BRIGADE_E03_BODY"] = body
    env["BRIGADE_E03_SUMMARY"] = "E0-4 idle-wake probe, marker " + nonce
    subprocess.run([sys.executable, os.path.join(HERE, "frame.py"),
                    "--variant", "A", "--out", frame_path],
                   check=True, env=env,
                   stderr=open(os.path.join(results, "frame.meta"), "w"))
    shutil.copy2(frame_path, os.path.join(results, "frame.txt"))

    # ---- clear per-run plugin state ----
    for f in ("hook-env.log", "hook-events.log", "fake-brigade.ndjson"):
        open(os.path.join(STATE, f), "w").close()
    for f in ("nested-env.json", "hook-input.json"):
        try:
            os.unlink(os.path.join(STATE, f))
        except OSError:
            pass

    # ---- launch the nested session ----
    child_env = {k: v for k, v in os.environ.items() if k not in LEAKY}
    cmd = ["claude", "-p"]
    if args.control == "no-stream-input":
        # The control: an ordinary `-p` run. The prompt is an argument, there is
        # no stdin stream to hold open, and the session is expected to EXIT at the
        # first result — leaving nothing for a socket post to wake.
        cmd.append(args.prompt)
    else:
        cmd += ["--input-format", "stream-json"]
    cmd += ["--output-format", "stream-json", "--verbose",
            "--plugin-dir", PLUGIN,
            "--permission-mode", "bypassPermissions"]

    events_path = os.path.join(results, "events.ndjson")
    stdin_path = os.path.join(results, "stdin-writes.log")
    events = []
    lock = threading.Lock()
    t_launch = time.time()

    proc = subprocess.Popen(
        cmd, cwd=PROJECT, env=child_env,
        stdin=subprocess.PIPE, stdout=subprocess.PIPE,
        stderr=open(os.path.join(results, "claude.stderr"), "wb"), bufsize=0)

    sin = StdinLog(proc.stdin, stdin_path)

    def reader():
        for raw in proc.stdout:
            t = time.time()
            line = raw.decode("utf-8", "replace").rstrip("\n")
            try:
                obj = json.loads(line)
            except Exception:
                obj = {"_unparsed": line}
            rec = {"t": t, "ts": iso(t), "since_launch_ms": int((t - t_launch) * 1000),
                   "type": obj.get("type"), "subtype": obj.get("subtype"),
                   "event": obj}
            with lock:
                events.append(rec)
                with open(events_path, "a") as f:
                    f.write(json.dumps(rec) + "\n")

    th = threading.Thread(target=reader, daemon=True)
    th.start()

    # ---- write the ONE prompt, then never touch stdin again ----
    if args.control == "no-stream-input":
        sin.note("stdin_unused_control",
                 detail="prompt passed as an argv argument; nothing is ever written to stdin")
    else:
        first = json.dumps({"type": "user",
                            "message": {"role": "user", "content": args.prompt}}) + "\n"
        sin.write(first, note="first and only prompt")
    if args.control == "close-stdin":
        sin.note("stdin_closed_early_control",
                 detail="EOF sent immediately after the prompt, before the first result")
        try:
            proc.stdin.close()
        except Exception:
            pass

    def snapshot(pred, timeout):
        """Wait until pred(events) is true; returns the matching record or None."""
        deadline = time.time() + timeout
        while time.time() < deadline:
            with lock:
                for r in events:
                    if pred(r):
                        return r
            if proc.poll() is not None:
                # process gone; one last look
                with lock:
                    for r in events:
                        if pred(r):
                            return r
                return None
            time.sleep(0.02)
        return None

    # ---- wait for the FIRST result ----
    first_result = snapshot(lambda r: r["type"] == "result", 120)
    if first_result is None:
        print("FAIL: no first result within 120 s (child alive=%s)" % (proc.poll() is None))
        finish(proc, sin, results, events, None, None, nonce, outer,
               protected, pre, snap, args, tag)
        return 1
    t_result = first_result["t"]
    n_at_result = len(events)
    sin.note("first_result_observed", detail="stdin is now sealed for the rest of the run")

    # ---- the nested session's own inbox coordinates (written by its hook) ----
    nested_env_path = os.path.join(STATE, "nested-env.json")
    deadline = time.time() + 10
    nested = None
    while time.time() < deadline:
        if os.path.exists(nested_env_path):
            try:
                with open(nested_env_path) as f:
                    nested = json.load(f)
                if nested.get("socket") and nested.get("token"):
                    break
            except Exception:
                pass
        time.sleep(0.1)
    if not nested or not nested.get("socket"):
        print("FAIL: nested session published no socket coordinates")
        finish(proc, sin, results, events, t_result, None, nonce, outer,
               protected, pre, snap, args, tag)
        return 1

    # ---- isolation assertion, BEFORE anything is posted ----
    ntok_sha = hashlib.sha256(nested.get("token", "").encode()).hexdigest()[:12]
    iso_fail = []
    if outer["socket"] and nested["socket"] == outer["socket"]:
        iso_fail.append("socket")
    if outer["token_sha12"] and ntok_sha == outer["token_sha12"]:
        iso_fail.append("token")
    if outer["session"] and nested.get("session") == outer["session"]:
        iso_fail.append("session")
    isolation = {
        "outer": outer,
        "nested": {"socket": nested["socket"], "token_sha12": ntok_sha,
                   "session": nested.get("session"), "claude_pid": nested.get("claude_pid")},
        "child_pid_spawned": proc.pid,
        "leaked": iso_fail,
        "ok": not iso_fail,
    }
    with open(os.path.join(results, "isolation.json"), "w") as f:
        json.dump(isolation, f, indent=2)
    if iso_fail:
        print("FAIL: environment leaked (%s) — refusing to post" % ",".join(iso_fail))
        finish(proc, sin, results, events, t_result, None, nonce, outer,
               protected, pre, snap, args, tag)
        return 1

    # ---- settle: prove the session is IDLE, not still working ----
    idle = {"settle_seconds": args.settle, "samples": [],
            "events_at_first_result": n_at_result}
    t_settle_end = t_result + args.settle
    while time.time() < t_settle_end:
        idle["samples"].append({"ts": iso(), "since_result_ms":
                                int((time.time() - t_result) * 1000),
                                "ps": ps_probe(proc.pid),
                                "events_seen": len(events)})
        time.sleep(max(0.0, min(0.75, t_settle_end - time.time())))
    with lock:
        idle["events_during_settle"] = len(events) - n_at_result
        idle["child_alive"] = proc.poll() is None
    idle["ps_at_post"] = ps_probe(proc.pid)
    idle["stdin_writes_before_post"] = sin.writes
    idle["stdin_bytes_before_post"] = sin.total
    # The null-post control is only meaningful if the session was genuinely
    # postable at this instant: alive, with its inbox socket still on disk.
    idle["socket_exists_at_post"] = os.path.exists(nested["socket"])

    # ---- POST: the Go poster writes the frame to the nested inbox socket ----
    t_post_start = time.time()
    if args.control == "no-post":
        # NULL POST. Everything above is identical to a passing run — same flags,
        # same open stdin, same idle hold, same socket alive and reachable — but
        # the poster is never run. If a turn still appears in the observation
        # window, the "wake" in the real runs is not caused by the post.
        t_post_end = time.time()
        post_rec = {"event": "no_post_control",
                    "detail": "the poster was deliberately NOT run; the socket at "
                              + nested["socket"] + " was left untouched",
                    "socket": nested["socket"], "session": nested.get("session")}
        post_rec["poster_exit"] = None
        post_rec["poster_stderr"] = ""
    else:
        post = subprocess.run([POSTER, "-env", nested_env_path, "-frame", frame_path],
                              capture_output=True, text=True, timeout=30)
        t_post_end = time.time()
        try:
            post_rec = json.loads(post.stdout.strip().splitlines()[-1])
        except Exception:
            post_rec = {"event": "unparsed", "stdout": post.stdout, "stderr": post.stderr}
        post_rec["poster_exit"] = post.returncode
        post_rec["poster_stderr"] = post.stderr
    post_rec["driver_t_post_start"] = t_post_start
    post_rec["driver_t_post_end"] = t_post_end
    post_rec["driver_ts_post_end"] = iso(t_post_end)
    with open(os.path.join(results, "post.json"), "w") as f:
        json.dump(post_rec, f, indent=2)
    sin.note("frame_posted_to_socket" if args.control != "no-post"
             else "no_post_control_marker",
             detail=("posted out-of-band on the unix socket; stdin still untouched"
                     if args.control != "no-post" else
                     "NOTHING was posted; stdin still untouched; observation starts here"))

    # The anchor for the latency the design needs: the moment the frame bytes
    # completed their write to the socket.
    t_post = t_post_end

    # ---- watch for the wake ----
    deadline = time.time() + args.observe
    while time.time() < deadline:
        if proc.poll() is not None:
            break
        with lock:
            after = [r for r in events if r["t"] > t_post]
        if any(r["type"] == "result" for r in after):
            # the woken turn has completed; give a beat for trailing lines
            time.sleep(1.0)
            break
        time.sleep(0.05)

    finish(proc, sin, results, events, t_result, t_post, nonce, outer,
           protected, pre, snap, args, tag, isolation=isolation, idle=idle,
           post_rec=post_rec, nested=nested)
    return 0


def finish(proc, sin, results, events, t_result, t_post, nonce, outer,
           protected, pre, snap, args, tag, isolation=None, idle=None,
           post_rec=None, nested=None):
    # ---- close stdin (EOF) and let the child exit ----
    sin.note("stdin_closed_eof", detail="first close of stdin in the whole run")
    try:
        proc.stdin.close()
    except Exception:
        pass
    try:
        proc.wait(timeout=25)
    except subprocess.TimeoutExpired:
        proc.terminate()
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
    time.sleep(0.5)

    if idle is not None:
        with open(os.path.join(results, "idle-probe.json"), "w") as f:
            json.dump(idle, f, indent=2)

    # ---- copy the plugin state and the on-disk transcript ----
    for f in ("hook-env.log", "hook-events.log", "fake-brigade.ndjson",
              "hook-input.json"):
        src = os.path.join(STATE, f)
        if os.path.exists(src):
            shutil.copy2(src, os.path.join(results, f))
    cfgdir = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")
    disk_t = None
    if nested and nested.get("session"):
        for root, _dirs, files in os.walk(os.path.join(cfgdir, "projects")):
            if nested["session"] + ".jsonl" in files:
                disk_t = os.path.join(root, nested["session"] + ".jsonl")
                break
    if disk_t:
        shutil.copy2(disk_t, os.path.join(results, "session-transcript.jsonl"))

    # ---- config protection: hash AFTER, restore on change ----
    cfg = []
    for i, p in enumerate(protected):
        post_h = sha256_file(p)
        changed = post_h != pre[p]
        if changed and os.path.exists(os.path.join(snap, "%d.snap" % i)):
            shutil.copy2(os.path.join(snap, "%d.snap" % i), p)
        cfg.append({"path": p, "pre": pre[p], "post": post_h,
                    "changed": changed, "restored": changed})
    with open(os.path.join(results, "config-protection.json"), "w") as f:
        json.dump(cfg, f, indent=2)

    # ---- verdict ----
    def text_of(rec):
        ev = rec.get("event", {})
        out = []
        msg = ev.get("message") or {}
        for blk in (msg.get("content") or []):
            if isinstance(blk, dict) and blk.get("type") == "text":
                out.append(blk.get("text", ""))
        if isinstance(ev.get("result"), str):
            out.append(ev["result"])
        return "\n".join(out)

    verdict = {
        "tag": tag, "nonce": nonce,
        "isolation": isolation, "post": post_rec,
        "config_protected": all(not c["changed"] for c in cfg),
        "stdin_writes_total": sin.writes,
        "stdin_bytes_total": sin.total,
        "child_exit": proc.returncode,
        "events_total": len(events),
    }
    if t_result and t_post:
        after = [r for r in events if r["t"] > t_post]
        woke = [r for r in after if r["type"] == "assistant"]
        verdict["events_after_post"] = [
            {"ts": r["ts"], "type": r["type"], "subtype": r.get("subtype"),
             "ms_after_post": int((r["t"] - t_post) * 1000)} for r in after]
        verdict["stdin_writes_between_first_result_and_post"] = 0
        verdict["woke"] = bool(woke)
        if woke:
            verdict["wake_latency_ms"] = int((woke[0]["t"] - t_post) * 1000)
            verdict["wake_within_window"] = \
                verdict["wake_latency_ms"] <= args.wake_window * 1000
            txt = "\n".join(text_of(r) for r in after)
            verdict["woken_text"] = txt[:4000]
            verdict["woken_turn_contains_nonce"] = nonce in txt
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2)
    print(json.dumps(verdict, indent=2)[:6000])
    print("results: " + results)


if __name__ == "__main__":
    sys.exit(main())
