#!/usr/bin/env python3
"""E3-interactive checks 3, 4 and 5 — the permission RULES — without a keyboard.

  check 3  `permissions.allow: ["Bash(brigade:*)"]` removes the Bash prompt for
           the whole session, in Manual mode, with no skill in play.
  check 4  `permissions.ask: ["Bash(brigade send*)"]` still raises a dialog in
           **bypass** mode -- the send is not silently allowed and not silently
           denied -- while `brigade sessions` runs unprompted. Records how the
           multi-line heredoc renders and what options the dialog offers.
  check 5  `permissions.deny: ["Bash(brigade send*)"]` blocks the send in bypass
           mode, the model says so, and (per the skill) does NOT propose another
           invocation form.

DEVIATION FROM THE CHECKLIST, DELIBERATE. The checklist puts the rule in
`${CLAUDE_CONFIG_DIR:-$HOME/.claude}/settings.json`. This driver puts it in the
`--settings` file it already writes per run. `--settings` is a documented settings
source and carries the same `permissions` block, so the RULE is what is measured
either way -- but the FILE LOCATION is not varied, and editing the real user
settings file while other sessions on this machine are running would be a race
with a shared resource. Stated in the writeup as a limit.

Checks 4 and 5 need somewhere to send to, so a second principal's session is
registered through the REAL `SessionStart` hook with a sleeper as its CLAUDE_PID
(the shape `scripts/harness-smoke.sh` and `internal/harness/e2e` use), under the
`bob` profile that is already joined to this machine's team. No socket variable is
set for it, so no watcher is spawned; the sleeper is killed on the way out.
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
import uuid

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import run_manual as rm          # noqa: E402  (prelude, guard, transcript reader, constants)
import common                    # noqa: E402  (E0-8's expect prelude and env strip)

PLUGIN, HOOKBIN, REPO = rm.PLUGIN, rm.HOOKBIN, rm.REPO

BARE = ("Without using any skill, run the Bash command `brigade sessions` directly and paste "
        "its output. Use the bare command name `brigade`, never a path to the binary.")


def send_prompt(sid):
    return ("Use the brigade team-messaging skill. First run `brigade sessions`. Then send the "
            "session %s a one-line message saying hello, using the skill's documented "
            "`brigade send` form with the body in a quoted heredoc. Tell me what happened." % sid)


ARMS = {
    # arm:   (check, mode,                 permissions,                                needs_peer)
    "allow": (3, "default", {"allow": ["Bash(brigade:*)"], "deny": [], "ask": []}, False),
    "ask":   (4, "bypassPermissions", {"allow": [], "deny": [], "ask": ["Bash(brigade send*)"]}, True),
    "deny":  (5, "bypassPermissions", {"allow": [], "deny": ["Bash(brigade send*)"], "ask": []}, True),
}

BODY = r"""
set stty_init "rows 50 columns 200"

proc quiesce {att exec secs cap} {
    set t0 [clock milliseconds]
    set la [ndcount $att]
    set le [ndcount $exec]
    set stable [clock milliseconds]
    while {[clock milliseconds] - $t0 < int($cap * 1000)} {
        nap 1
        set a [ndcount $att]
        set e [ndcount $exec]
        if {$a != $la || $e != $le} { set la $a ; set le $e ; set stable [clock milliseconds] }
        if {[clock milliseconds] - $stable >= int($secs * 1000)} { return 1 }
    }
    return 0
}

mark spawn arm @ARM@ run @RUN@ sid @SID@
spawn -noecho env @UNSETS@ claude --permission-mode @MODE@ --session-id @SID@ --plugin-dir "@PLUGIN@" --settings "@SETTINGS@"

onboard
if {![canary @TAIL@ 3]} { mark canary_failed ; shutdown ; exit 0 }

set cpid [exp_pid]
mark claude_pid pid $cpid
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-live.json"} cperr
mark bypid_snapshot pid $cpid err "$cperr"

mark arm_prompt_sent
submit "@PROMPT@" "arm prompt"

if {@SENDARM@} {
    # ---- checks 4 and 5 -------------------------------------------------------
    # The dialog is expected IMMEDIATELY, with a timeout long enough to cover the
    # model thinking and issuing the send. It must NOT be preceded by a draining
    # wait on the send-attempt file: `nap` consumes the pty, so a dialog painted
    # during that wait is swallowed and the later `expect` never sees it -- which
    # is exactly what happened on the first run of this arm, where the session log
    # holds the dialog verbatim while the driver recorded "absent".
    set timeout @DIALOGWAIT@
    expect {
        -re {proceed} {
            mark send_dialog_shown
            catch {set f [open "@RESULTS@/dialog-buffer.txt" a] ; puts $f "--- SEND DIALOG ---" ; puts $f $expect_out(buffer) ; close $f}
            nap 2
            # Escape REJECTS and persists nothing. The check is whether the dialog
            # appears and what it offers, not whether a message lands.
            xsend "\033" "reject the send dialog"
            mark send_dialog_rejected
        }
        timeout { mark send_dialog_absent }
        eof { mark send_dialog_eof }
    }
    set timeout 60
    mark send_attempts_on_record n [ndcount "@SENDATT@"]
    # Let the turn finish so the model's own words are on the record: check 5
    # asks what it says after a denial, and whether it proposes another form.
    set q [quiesce "@ATT@" "@EXEC@" 12 @T2@]
    mark quiesced ok $q
} else {
    # ---- check 3 --------------------------------------------------------------
    set w1 [waitcount "@EXEC@" 1 @T1@]
    if {$w1 < 0} {
        mark stall index 1 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
    } else {
        mark exec_seen index 1 waited_ms $w1
    }
}

nap 4
mark final attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"] sends [ndcount "@SENDEXEC@"]
shutdown
"""



def scan_dialog(log):
    """Find a permission dialog in the raw session log, and list its options.

    This is the witness that does not depend on the driver's reflexes: the TUI
    paints every dialog into the log whether or not `expect` matched it in time.
    It must be WHITESPACE-INSENSITIVE -- the renderer drops the spaces inside a
    sentence, so the prose arrives as `Doyouwanttoproceed?` while the numbered
    options keep theirs. Returns (excerpt or None, options list).
    """
    plain = re.sub(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)", "", log)
    plain = re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", plain).replace("\r", "\n")
    lines = plain.split("\n")
    hit = None
    for i, line in enumerate(lines):
        if "doyouwanttoproceed" in re.sub(r"\s+", "", line).lower():
            hit = i
            break
    if hit is None:
        return None, []
    excerpt = "\n".join(lines[max(0, hit - 14):hit + 8]).strip()
    options = []
    for line in lines[hit + 1:hit + 8]:
        m = re.match(r"^\s*[\u276f>]?\s*(\d)\.\s*(\S.*?)\s*$", line)
        if m:
            options.append("%s. %s" % (m.group(1), m.group(2)))
    return excerpt, options


def settings_file(path, permissions):
    doc = {
        "permissions": permissions,
        "hooks": {
            "PreToolUse": [{"matcher": "*", "hooks": [
                {"type": "command", "command": os.path.join(HOOKBIN, "attempt"), "timeout": 10}]}],
            "PostToolUse": [{"matcher": "*", "hooks": [
                {"type": "command", "command": os.path.join(HOOKBIN, "posttool"), "timeout": 10}]}],
        },
    }
    with open(path, "w") as f:
        json.dump(doc, f, indent=2)
    return path


def register_peer(results):
    """A second principal's session, through the REAL SessionStart hook. Returns
    (brigade_session_id, sleeper_pid, config_dir)."""
    cfg = tempfile.mkdtemp(prefix="brigade-e3-peercfg-")
    sleeper = subprocess.Popen(["sleep", "1800"])
    env, _ = common.nested_env({
        "CLAUDE_CONFIG_DIR": cfg,
        "CLAUDE_PID": str(sleeper.pid),
        "CLAUDE_CODE_SESSION_ID": "e3-peer-%d" % sleeper.pid,
        "CLAUDECODE": "1",
        "CLAUDE_CODE_ENTRYPOINT": "cli",
        "CLAUDE_PLUGIN_OPTION_PROFILE": "bob",
    })
    env.pop("CLAUDE_CODE_MESSAGING_SOCKET", None)   # no socket => no watcher for the peer
    doc = json.dumps({"session_id": "e3-peer-%d" % sleeper.pid,
                      "cwd": os.path.join(cfg, "peer-session"),
                      "hook_event_name": "SessionStart",
                      "transcript_path": "/never/read.jsonl",
                      "source": "startup"})
    os.makedirs(os.path.join(cfg, "peer-session"), exist_ok=True)
    out = subprocess.run([os.path.join(REPO, "bin", "brigade"), "hook", "session-start"],
                         input=doc, env=env, capture_output=True, text=True, timeout=60)
    with open(os.path.join(results, "peer-hook.out"), "w") as f:
        f.write("stdout:\n%s\nstderr:\n%s\n" % (out.stdout, out.stderr))
    mp = os.path.join(rm.BYPID_DIR, "%d.json" % sleeper.pid)
    sid = None
    try:
        with open(mp) as f:
            sid = json.load(f).get("brigade_session_id")
    except Exception:
        pass
    return sid, sleeper, cfg


def one_run(arm, run_idx, root, args, peer_sid):
    check, mode, permissions, needs_peer = ARMS[arm]
    results = os.path.join(root, "%s-run%d" % (arm, run_idx))
    os.makedirs(results, exist_ok=True)
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)
    proj = tempfile.mkdtemp(prefix="brigade-e3-%s-%d-" % (arm, run_idx))

    env, stripped = common.nested_env({"BRIGADE_E3_STATE": state})
    sid = str(uuid.uuid4())
    setf = settings_file(os.path.join(results, "settings.json"), permissions)
    prompt = send_prompt(peer_sid) if needs_peer else BARE

    body = (BODY
            .replace("@ARM@", arm).replace("@RUN@", str(run_idx)).replace("@SID@", sid)
            .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
            .replace("@PLUGIN@", PLUGIN).replace("@SETTINGS@", setf)
            .replace("@MODE@", mode)
            .replace("@TAIL@", secrets.token_hex(2).upper())
            .replace("@PROMPT@", prompt)
            .replace("@BYPIDDIR@", rm.BYPID_DIR).replace("@RESULTS@", results)
            .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
            .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
            .replace("@SENDATT@", os.path.join(state, "send-attempt.ndjson"))
            .replace("@SENDEXEC@", os.path.join(state, "send-exec.ndjson"))
            .replace("@SENDARM@", "1" if needs_peer else "0")
            .replace("@DIALOGWAIT@", str(args.dialogwait))
            .replace("@T1@", str(args.t1)).replace("@T2@", str(args.t2)))
    exp = common.write_expect(results, body)
    how = common.run_expect(exp, proj, env, args.hard_timeout, results)

    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    by_event = {}
    for m in marks:
        by_event.setdefault(m["event"], []).append(m)
    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    send_att = common.read_ndjson(os.path.join(state, "send-attempt.ndjson"))
    send_exec = common.read_ndjson(os.path.join(state, "send-exec.ndjson"))
    fullpath = [a for a in atts if a.get("brigade_form") == "fullpath"]

    log = ""
    lp = os.path.join(results, "session.log")
    if os.path.exists(lp):
        log = open(lp, errors="replace").read()
    dialog = ""
    dp = os.path.join(results, "dialog-buffer.txt")
    if os.path.exists(dp):
        dialog = open(dp, errors="replace").read()

    dialog_in_log, dialog_options = scan_dialog(log)

    tr = rm.read_transcript(sid)
    assistant_text = []
    p = tr["path"]
    if p:
        with open(p, errors="replace") as f:
            for line in f:
                try:
                    d = json.loads(line)
                except Exception:
                    continue
                if d.get("type") == "assistant":
                    for c in ((d.get("message") or {}).get("content") or []):
                        if isinstance(c, dict) and c.get("type") == "text" and c.get("text", "").strip():
                            assistant_text.append(c["text"].strip())

    v = {
        "arm": arm, "check": check, "run": run_idx, "sid": sid, "cwd": proj,
        "permission_mode_requested": mode, "permissions": permissions,
        "peer_session_id": peer_sid,
        "expect_process": how,
        "canary_ok": "canary_ok" in by_event,
        "mode_from_hook_payload": sorted({a.get("permission_mode") for a in atts
                                          if a.get("permission_mode")}),
        "mode_from_transcript": tr["permission_modes"],
        "mode_from_bypid_map": (rm.read_bypid(results) or {}).get("permission_mode"),
        "brigade_attempts": [a.get("cmd_head") for a in atts if a.get("brigade_form") == "bare"],
        "brigade_executions": [e.get("cmd_head") for e in execs],
        "send_attempts": [a.get("cmd_head") for a in send_att],
        "send_executions": [e.get("cmd_head") for e in send_exec],
        "fullpath_attempts": [a.get("cmd_head") for a in fullpath],
        "other_tools": sorted({a.get("tool") for a in atts if a.get("tool") not in ("Bash", "Skill")}),
        "stalled": "stall" in by_event,
        "send_dialog_shown": "send_dialog_shown" in by_event,
        "send_dialog_absent": "send_dialog_absent" in by_event,
        "dialog_buffer_len": len(dialog),
        "dialog_in_session_log": dialog_in_log,
        "dialog_painted": bool(dialog_in_log),
        "dialog_options": dialog_options,
        "assistant_text": assistant_text[-4:],
        "transcript": {k: tr[k] for k in ("path", "permission_modes", "versions", "models",
                                          "tool_uses", "tool_results")},
    }
    v["mode_is_expected"] = bool(v["mode_from_hook_payload"]
                                 and all(m == mode for m in v["mode_from_hook_payload"]))
    if check == 3:
        v["verdict"] = bool(v["brigade_executions"] and not v["stalled"])
    elif check == 4:
        # "the dialog appeared and the send did not silently run or silently die",
        # witnessed by the painted dialog rather than by the driver's reflexes.
        v["verdict"] = bool(v["send_attempts"] and v["dialog_painted"]
                            and not v["send_executions"] and v["brigade_executions"])
    else:
        v["verdict"] = bool(v["send_attempts"] and not v["send_executions"]
                            and not v["send_dialog_shown"] and not v["fullpath_attempts"])
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", default="allow,ask,deny")
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--results", default=os.path.join(REPO, ".ignored", "e3-interactive"))
    ap.add_argument("--tag", default=None)
    ap.add_argument("--t1", type=int, default=150)
    ap.add_argument("--t2", type=int, default=120)
    ap.add_argument("--dialogwait", type=int, default=90)
    ap.add_argument("--hard-timeout", type=int, default=900)
    args = ap.parse_args()

    root = os.path.join(args.results, args.tag or common.stamp("e3rules"))
    os.makedirs(root, exist_ok=True)
    guard = rm.ReportOnlyGuard(root)

    arms = args.arms.split(",")
    peer_sid, sleeper, peercfg = (None, None, None)
    if any(ARMS[a][3] for a in arms):
        peer_sid, sleeper, peercfg = register_peer(root)
        print("peer session registered: %s (sleeper pid %s)" % (peer_sid, sleeper and sleeper.pid))
        if not peer_sid:
            print("WARNING: no peer session id; checks 4 and 5 cannot run")
    rows = []
    try:
        for arm in arms:
            for i in range(1, args.runs + 1):
                v = one_run(arm, i, root, args, peer_sid)
                rows.append(v)
                print(json.dumps({k: v[k] for k in
                                  ("arm", "check", "run", "canary_ok", "mode_is_expected",
                                   "brigade_executions", "send_attempts", "send_executions",
                                   "send_dialog_shown", "stalled", "fullpath_attempts",
                                   "other_tools", "verdict")}))
                sys.stdout.flush()
    finally:
        if sleeper:
            try:
                sleeper.send_signal(signal.SIGTERM)
                sleeper.wait(timeout=10)
            except Exception:
                pass
        if peercfg:
            shutil.rmtree(peercfg, ignore_errors=True)

    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump({"claude_version": subprocess.run(["claude", "--version"],
                                                    capture_output=True, text=True).stdout.strip(),
                   "note": "permission rules delivered via --settings, not the user settings file",
                   "peer_session_id": peer_sid,
                   "runs": rows,
                   "config_protection": guard.verify()}, f, indent=2)
    print("results: " + root)


if __name__ == "__main__":
    main()
