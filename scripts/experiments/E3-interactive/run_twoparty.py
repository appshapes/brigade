#!/usr/bin/env python3
"""E3-interactive check 6 — send and MID-TURN receive between two principals.

The checklist's shape: session A sends B a message and then sleeps with the Bash
tool; while A sleeps, B replies with `brigade send --reply-to`; A must receive
that reply **mid-turn**, framed, and must not fall back to the native
`SendMessage`.

Here A is a real interactive pty session (it has an inbox socket, so it has a
watcher — that is the half `claude -p` cannot exercise) and B is a second
principal driven from this process, exactly as `scripts/harness-smoke.sh` drives
one: registered through the REAL `SessionStart` hook with a sleeper as its
CLAUDE_PID, under the `bob` profile already joined to this machine's team.

B's reply is posted from a background thread that watches B's inbox in the fs
store and fires the moment A's message lands, so the reply really does arrive
while A is inside its sleep rather than after the turn has ended.

`E3-smoke.md` already proved this round trip headlessly. What this adds is the
interactive path: the TUI session, its watcher, and the injection into a live
turn.
"""
import argparse
import json
import os
import re
import secrets
import shutil
import signal
import subprocess
import sys
import tempfile
import threading
import time
import uuid

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import run_manual as rm      # noqa: E402
import run_rules as rr       # noqa: E402  (register_peer)
import common                # noqa: E402

PLUGIN, HOOKBIN, REPO = rm.PLUGIN, rm.HOOKBIN, rm.REPO
STATE = os.path.expanduser(os.environ.get("XDG_STATE_HOME", "~/.local/state"))
STORE = os.path.join(STATE, "brigade", "fs-adapter")
BRIGADE = os.path.join(REPO, "bin", "brigade")

BODY = r"""
set stty_init "rows 50 columns 200"

mark spawn run @RUN@ sid @SID@
spawn -noecho env @UNSETS@ claude --permission-mode bypassPermissions --session-id @SID@ --plugin-dir "@PLUGIN@" --settings "@SETTINGS@"

onboard
if {![canary @TAIL@ 3]} { mark canary_failed ; shutdown ; exit 0 }

set cpid [exp_pid]
mark claude_pid pid $cpid
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-live.json"} e
mark bypid_snapshot err "$e"

mark arm_prompt_sent
submit "@PROMPT@" "send-then-sleep prompt"

# The peer's reply is posted by a thread in the driver the moment A's message
# lands in B's inbox. All this has to do is stay alive, and keep DRAINING, for
# longer than A's sleep so the injection has somewhere to arrive.
nap @WATCH@
mark watch_done attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
nap 8
mark final
shutdown
"""


def settings_file(path):
    doc = {"hooks": {
        "PreToolUse": [{"matcher": "*", "hooks": [
            {"type": "command", "command": os.path.join(HOOKBIN, "attempt"), "timeout": 10}]}],
        "PostToolUse": [{"matcher": "*", "hooks": [
            {"type": "command", "command": os.path.join(HOOKBIN, "posttool"), "timeout": 10}]}]}}
    with open(path, "w") as f:
        json.dump(doc, f, indent=2)
    return path


def team_ref():
    teams = os.path.join(STORE, "teams")
    names = sorted(os.listdir(teams)) if os.path.isdir(teams) else []
    return names[0] if names else None


def peer_replier(bob_sid, bob_pid, bob_cfg, results, deadline, out):
    """Watch B's inbox; the moment A's message lands, reply to it with
    --reply-to, as B, through the real binary."""
    rec = {"waited_s": None, "inbox_file": None, "message_id": None,
           "reply_to_session": None, "sent": False, "stdout": "", "stderr": "",
           "error": None}
    try:
        ref = team_ref()
        inbox = os.path.join(STORE, "teams", ref, "inbox", bob_sid)
        t0 = time.monotonic()
        msg = None
        while time.monotonic() - t0 < deadline:
            if os.path.isdir(inbox):
                files = sorted(f for f in os.listdir(inbox) if f.endswith(".json"))
                if files:
                    rec["inbox_file"] = os.path.join(inbox, files[0])
                    with open(rec["inbox_file"]) as f:
                        msg = json.load(f)
                    break
            time.sleep(0.25)
        if msg is None:
            rec["error"] = "A's message never reached B's inbox at %s" % inbox
            return
        rec["waited_s"] = round(time.monotonic() - t0, 2)
        # The stored file is a protocol.MessageEnvelope (internal/protocol/message.go):
        # `message_id` at the top and the address to reply to under `sender.session_id`
        # -- there is no `from_session_id`. It may be written nested, so unwrap.
        env = msg if "message_id" in msg else next(
            (v for v in msg.values() if isinstance(v, dict) and "message_id" in v), msg)
        rec["raw_keys"] = sorted(env.keys())
        rec["message_id"] = env.get("message_id")
        sender = env.get("sender") or {}
        rec["sender"] = {k: sender.get(k) for k in ("session_id", "session_name", "human_label")}
        rec["reply_to_session"] = sender.get("session_id")
        rec["body_received"] = str(env.get("body", ""))[:200]
        if not (rec["message_id"] and rec["reply_to_session"]):
            rec["error"] = "could not read message_id/reply-to from %s" % rec["raw_keys"]
            return
        env, _ = common.nested_env({
            "CLAUDE_CONFIG_DIR": bob_cfg,
            "CLAUDE_PID": str(bob_pid),
            "CLAUDE_CODE_SESSION_ID": "e3-peer-%s" % bob_pid,
            "CLAUDECODE": "1",
            "CLAUDE_CODE_ENTRYPOINT": "cli",
            "CLAUDE_PLUGIN_OPTION_PROFILE": "bob",
        })
        env.pop("CLAUDE_CODE_MESSAGING_SOCKET", None)
        body = "Reply from bob: got it, nothing needed on your side. [%s]" % secrets.token_hex(3)
        rec["body"] = body
        r = subprocess.run([BRIGADE, "send", rec["reply_to_session"],
                            "--reply-to", rec["message_id"], "--summary", "bob's reply"],
                           input=body, env=env, capture_output=True, text=True, timeout=120)
        rec["stdout"], rec["stderr"] = r.stdout[:600], r.stderr[:600]
        rec["sent"] = (r.returncode == 0)
        rec["returncode"] = r.returncode
    except Exception as e:                                   # noqa: BLE001
        rec["error"] = "%s: %s" % (type(e).__name__, e)
    finally:
        with open(out, "w") as f:
            json.dump(rec, f, indent=2)


def scan_injection(path):
    """A message injected mid-turn is not a normal user record. E3-smoke measured
    its shape: a `queue-operation`/enqueue plus an `attachment` of type
    `queued_command` whose `prompt` carries the whole frame."""
    out = {"queue_operations": 0, "queued_command_attachments": 0, "frames": [],
           "origins": [], "native_sendmessage_calls": 0, "tool_uses": []}
    if not path or not os.path.exists(path):
        return out
    with open(path, errors="replace") as f:
        for line in f:
            try:
                d = json.loads(line)
            except Exception:
                continue
            t = d.get("type")
            if t == "queue-operation":
                out["queue_operations"] += 1
                p = json.dumps(d)
                if "<brigade-message" in p:
                    out["frames"].append(p[:400])
            elif t == "attachment":
                a = d.get("attachment") or {}
                if a.get("type") == "queued_command":
                    out["queued_command_attachments"] += 1
                    if a.get("origin"):
                        out["origins"].append(json.dumps(a["origin"])[:160])
                    pr = str(a.get("prompt", ""))
                    if "<brigade-message" in pr:
                        out["frames"].append(pr[:500])
            elif t == "assistant":
                for c in ((d.get("message") or {}).get("content") or []):
                    if isinstance(c, dict) and c.get("type") == "tool_use":
                        out["tool_uses"].append(c.get("name"))
                        if c.get("name") in ("SendMessage", "ListAgents"):
                            out["native_sendmessage_calls"] += 1
    return out


def one_run(run_idx, root, args, bob_sid, bob_pid, bob_cfg):
    results = os.path.join(root, "twoparty-run%d" % run_idx)
    os.makedirs(results, exist_ok=True)
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)
    proj = tempfile.mkdtemp(prefix="brigade-e3-twoparty-%d-" % run_idx)
    sid = str(uuid.uuid4())

    prompt = ("Use the brigade team-messaging skill. Send the session %s a one-line message "
              "saying hello, and then run the Bash command `sleep %d` and wait for it to "
              "finish. Do not use any other tool." % (bob_sid, args.sleep))

    env, stripped = common.nested_env({"BRIGADE_E3_STATE": state})
    body = (BODY
            .replace("@RUN@", str(run_idx)).replace("@SID@", sid)
            .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
            .replace("@PLUGIN@", PLUGIN)
            .replace("@SETTINGS@", settings_file(os.path.join(results, "settings.json")))
            .replace("@TAIL@", secrets.token_hex(2).upper())
            .replace("@PROMPT@", prompt)
            .replace("@BYPIDDIR@", rm.BYPID_DIR).replace("@RESULTS@", results)
            .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
            .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
            .replace("@WATCH@", str(args.sleep + 60)))
    exp = common.write_expect(results, body)

    reply_out = os.path.join(results, "peer-reply.json")
    th = threading.Thread(target=peer_replier,
                          args=(bob_sid, bob_pid, bob_cfg, results, args.sleep + 120, reply_out),
                          daemon=True)
    th.start()
    how = common.run_expect(exp, proj, env, args.hard_timeout, results)
    th.join(timeout=30)

    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    by_event = {}
    for m in marks:
        by_event.setdefault(m["event"], []).append(m)
    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    sends = common.read_ndjson(os.path.join(state, "send-exec.ndjson"))
    tr = rm.read_transcript(sid)
    inj = scan_injection(tr["path"])
    reply = {}
    if os.path.exists(reply_out):
        with open(reply_out) as f:
            reply = json.load(f)

    alice_sid = None
    bp = os.path.join(results, "bypid-live.json")
    if os.path.exists(bp):
        with open(bp) as f:
            alice_sid = json.load(f).get("brigade_session_id")

    ref = team_ref()
    acked = os.path.join(STORE, "teams", ref, "acked", alice_sid or "")
    acked_files = sorted(os.listdir(acked)) if os.path.isdir(acked) else []

    v = {
        "run": run_idx, "sid": sid, "cwd": proj,
        "alice_session_id": alice_sid, "peer_session_id": bob_sid,
        "canary_ok": "canary_ok" in by_event,
        "expect_process": how,
        "brigade_executions": [e.get("cmd_head") for e in execs],
        "send_executions": [e.get("cmd_head") for e in sends],
        "other_tools": sorted({a.get("tool") for a in atts if a.get("tool") not in ("Bash", "Skill")}),
        "peer_reply": reply,
        "injection": inj,
        "acked_dir": acked, "acked_files": acked_files,
        "transcript_path": tr["path"],
    }
    v["received_mid_turn"] = bool(inj["queued_command_attachments"] or inj["queue_operations"])
    v["frame_present"] = bool(inj["frames"])
    v["no_native_messaging"] = (inj["native_sendmessage_calls"] == 0)
    v["verdict"] = bool(v["send_executions"] and reply.get("sent")
                        and v["received_mid_turn"] and v["frame_present"]
                        and v["no_native_messaging"])
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--sleep", type=int, default=60)
    ap.add_argument("--results", default=os.path.join(REPO, ".ignored", "e3-interactive"))
    ap.add_argument("--tag", default=None)
    ap.add_argument("--hard-timeout", type=int, default=900)
    args = ap.parse_args()

    root = os.path.join(args.results, args.tag or common.stamp("e3two"))
    os.makedirs(root, exist_ok=True)
    guard = rm.ReportOnlyGuard(root)
    bob_sid, sleeper, bob_cfg = rr.register_peer(root)
    print("peer registered: %s (sleeper %s)" % (bob_sid, sleeper and sleeper.pid))
    rows = []
    try:
        for i in range(1, args.runs + 1):
            v = one_run(i, root, args, bob_sid, sleeper.pid, bob_cfg)
            rows.append(v)
            print(json.dumps({k: v[k] for k in
                              ("run", "canary_ok", "send_executions", "received_mid_turn",
                               "frame_present", "no_native_messaging", "acked_files",
                               "other_tools", "verdict")}))
            print("   peer reply: %s" % json.dumps({k: v["peer_reply"].get(k) for k in
                                                    ("waited_s", "message_id", "sent",
                                                     "returncode", "error")}))
            sys.stdout.flush()
    finally:
        if sleeper:
            try:
                sleeper.send_signal(signal.SIGTERM)
                sleeper.wait(timeout=10)
            except Exception:
                pass
        shutil.rmtree(bob_cfg, ignore_errors=True)
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump({"claude_version": subprocess.run(["claude", "--version"],
                                                    capture_output=True, text=True).stdout.strip(),
                   "runs": rows, "config_protection": guard.verify()}, f, indent=2)
    print("results: " + root)


if __name__ == "__main__":
    main()
