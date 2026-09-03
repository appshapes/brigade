#!/usr/bin/env python3
"""E3-interactive checks 7-13 — the session-lifecycle checks — without a keyboard.

  7  `/rename` propagates to the team roster within a heartbeat.
  8  `/clear` keeps the Brigade session: same session id, same watcher, and the
     SessionStart hook re-fires without rotating the identity.
  9  `/compact` is a no-op for Brigade: same id, no new registration.
 10  A second `brigade` earlier on PATH produces the shadowing context line, and
     the Bash tool really runs the other binary.
 11  A `~/.local/bin` symlink that resolves to the plugin's own bootstrap is NOT
     reported as a shadow.
 12  `SessionEnd` closes the session at once and stops the watcher.
 13  A SIGKILLed Claude does not strand the watcher: it exits on its own and the
     session goes offline (E0-5's zombie case).

All seven run in **bypass** permission mode: none of them is about prompts, and
checks 1-5 have already settled that half. Identity and timing are read from
files rather than from the screen — the by-pid map, the watcher pidfile, and the
team's own roster — with `bin/await-gone` taking the sub-second timings that
expect's once-a-second drain cannot.
"""
import argparse
import json
import os
import secrets
import shutil
import subprocess
import sys
import tempfile
import time
import uuid

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import run_manual as rm     # noqa: E402
import common               # noqa: E402

PLUGIN, HOOKBIN, REPO = rm.PLUGIN, rm.HOOKBIN, rm.REPO
STATE = os.path.expanduser(os.environ.get("XDG_STATE_HOME", "~/.local/state"))
WATCHERDIR = os.path.join(STATE, "brigade", "watchers")
AWAITGONE = os.path.join(HOOKBIN, "await-gone")
ROSTERDUMP = os.path.join(HOOKBIN, "roster-dump")
SYMLINK = os.path.expanduser("~/.local/bin/brigade")

WHOAMI = ("Run the single Bash command `brigade whoami` exactly as written and paste its "
          "output. Use the bare command name, and do not use any other tool.")
SESSIONS = ("Run the single Bash command `brigade sessions` exactly as written and paste its "
            "complete output verbatim. Use the bare command name, and do not use any other tool.")

SKELETON = r"""
set stty_init "rows 50 columns 200"

mark spawn arm @ARM@ run @RUN@ sid @SID@
spawn -noecho env @UNSETS@ claude --permission-mode bypassPermissions --session-id @SID@ --plugin-dir "@PLUGIN@" --settings "@SETTINGS@"

onboard
if {![canary @TAIL@ 3]} { mark canary_failed ; shutdown ; exit 0 }

set cpid [exp_pid]
mark claude_pid pid $cpid
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-before.json"} e1
catch {exec cp "@WATCHERDIR@/$cpid.json" "@RESULTS@/watcher-before.json"} e2
mark snapshots_before map_err "$e1" watcher_err "$e2"

@STEPS@

catch {nap 3}
mark final attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
@ENDING@
"""

STEPS = {
    # ---- 7 -------------------------------------------------------------------
    "rename": r"""
submit "@WHOAMI@" "whoami before the rename"
set w [waitcount "@EXEC@" 1 @T1@]
mark whoami_before waited_ms $w
nap 3
xsend "/rename @NEWNAME@" "rename (text)"
nap 2
xsend "\r" "rename (enter)"
mark renamed newname @NEWNAME@
# The watcher re-reads the registry once a heartbeat (~30 s), so give it two.
nap 70
catch {exec "@ROSTERDUMP@" "@RESULTS@/roster-after.json"} re
mark roster_dumped err "$re"
""",
    # ---- 8 -------------------------------------------------------------------
    "clear": r"""
submit "@WHOAMI@" "whoami before /clear"
set w [waitcount "@EXEC@" 1 @T1@]
mark whoami_before waited_ms $w
nap 3
xsend "/clear" "clear (text)"
nap 2
xsend "\r" "clear (enter)"
mark cleared
nap 12
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-after.json"} e3
catch {exec cp "@WATCHERDIR@/$cpid.json" "@RESULTS@/watcher-after.json"} e4
mark snapshots_after map_err "$e3" watcher_err "$e4"
submit "@WHOAMI@" "whoami after /clear"
set w2 [waitcount "@EXEC@" 2 @T1@]
mark whoami_after waited_ms $w2
""",
    # ---- 9 -------------------------------------------------------------------
    "compact": r"""
submit "@WHOAMI@" "whoami before /compact"
set w [waitcount "@EXEC@" 1 @T1@]
mark whoami_before waited_ms $w
nap 3
xsend "/compact" "compact (text)"
nap 2
xsend "\r" "compact (enter)"
mark compacted
nap 60
catch {exec cp "@BYPIDDIR@/$cpid.json" "@RESULTS@/bypid-after.json"} e3
catch {exec cp "@WATCHERDIR@/$cpid.json" "@RESULTS@/watcher-after.json"} e4
mark snapshots_after map_err "$e3" watcher_err "$e4"
submit "@WHOAMI@" "whoami after /compact"
set w2 [waitcount "@EXEC@" 2 @T1@]
mark whoami_after waited_ms $w2
""",
    # ---- 10 and 11 -----------------------------------------------------------
    "shadow": r"""
submit "@SESSIONS@" "sessions under the shadowing PATH"
set w [waitcount "@EXEC@" 1 @T1@]
mark sessions_ran waited_ms $w
nap 4
""",
    "symlink": r"""
submit "@WHOAMI@" "whoami with the ~/.local/bin symlink present"
set w [waitcount "@EXEC@" 1 @T1@]
mark whoami_ran waited_ms $w
nap 4
""",
    # ---- 12 ------------------------------------------------------------------
    "sessionend": r"""
submit "@WHOAMI@" "whoami before /exit"
set w [waitcount "@EXEC@" 1 @T1@]
mark whoami_before waited_ms $w
nap 3
# `/exit` is typed FIRST and submitted last, with the timer started in between,
# so `gone_after_ms` is a tight upper bound on the delay from the event itself.
xsend "/exit" "exit (text)"
nap 2
exec "@AWAITGONE@" "@WATCHERDIR@/$cpid.json" 40 "@RESULTS@/watcher-gone.json" &
mark exiting
catch {xsend "\r" "exit (enter)"}
# Every wait past this point touches a pty that is about to close, and `nap`
# raises "spawn id not open" the moment it does -- which aborted the whole script
# on the first run, losing the roster dump. The dump now happens in Python after
# expect returns; here we only need to not die.
catch {expect { eof { mark eof } timeout { mark exit_timeout } }}
mark steps_done
""",
    # ---- 13 ------------------------------------------------------------------
    "sigkill": r"""
submit "@WHOAMI@" "whoami before SIGKILL"
set w [waitcount "@EXEC@" 1 @T1@]
mark whoami_before waited_ms $w
nap 3
exec "@AWAITGONE@" "@WATCHERDIR@/$cpid.json" 60 "@RESULTS@/watcher-gone.json" &
mark killing pid $cpid
catch {exec kill -9 $cpid}
# As above: the pty dies with the process, so nothing after this may assume it.
catch {expect { eof { mark eof } timeout { mark no_eof } }}
mark steps_done
""",
}

# checks 12 and 13 end the session themselves; the rest use the shared shutdown.
ENDING = {"sessionend": "", "sigkill": ""}
CHECK = {"rename": 7, "clear": 8, "compact": 9, "shadow": 10,
         "symlink": 11, "sessionend": 12, "sigkill": 13}


def settings_file(path):
    doc = {"hooks": {
        "PreToolUse": [{"matcher": "*", "hooks": [
            {"type": "command", "command": os.path.join(HOOKBIN, "attempt"), "timeout": 10}]}],
        "PostToolUse": [{"matcher": "*", "hooks": [
            {"type": "command", "command": os.path.join(HOOKBIN, "posttool"), "timeout": 10}]}]}}
    with open(path, "w") as f:
        json.dump(doc, f, indent=2)
    return path


def jload(p):
    try:
        with open(p) as f:
            return json.load(f)
    except Exception:
        return None


def one_run(arm, run_idx, root, args):
    results = os.path.join(root, "%s-run%d" % (arm, run_idx))
    os.makedirs(results, exist_ok=True)
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)
    proj = tempfile.mkdtemp(prefix="brigade-e3-%s-%d-" % (arm, run_idx))
    sid = str(uuid.uuid4())
    newname = "e3-renamed-%s" % secrets.token_hex(2)

    extra = {"BRIGADE_E3_STATE": state}
    if arm == "shadow":
        extra["PATH"] = "/tmp/shadow:" + os.environ.get("PATH", "")
    env, stripped = common.nested_env(extra)

    made_symlink = False
    if arm == "symlink":
        os.makedirs(os.path.dirname(SYMLINK), exist_ok=True)
        if not os.path.lexists(SYMLINK):
            os.symlink(os.path.join(PLUGIN, "bin", "brigade"), SYMLINK)
            made_symlink = True

    try:
        body = (SKELETON
                .replace("@STEPS@", STEPS[arm])
                .replace("@ENDING@", ENDING.get(arm, "shutdown"))
                .replace("@ARM@", arm).replace("@RUN@", str(run_idx)).replace("@SID@", sid)
                .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
                .replace("@PLUGIN@", PLUGIN)
                .replace("@SETTINGS@", settings_file(os.path.join(results, "settings.json")))
                .replace("@TAIL@", secrets.token_hex(2).upper())
                .replace("@WHOAMI@", WHOAMI).replace("@SESSIONS@", SESSIONS)
                .replace("@NEWNAME@", newname)
                .replace("@BYPIDDIR@", rm.BYPID_DIR).replace("@WATCHERDIR@", WATCHERDIR)
                .replace("@AWAITGONE@", AWAITGONE).replace("@ROSTERDUMP@", ROSTERDUMP)
                .replace("@RESULTS@", results)
                .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
                .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
                .replace("@T1@", str(args.t1)))
        exp = common.write_expect(results, body)
        how = common.run_expect(exp, proj, env, args.hard_timeout, results)
    finally:
        if made_symlink:
            try:
                os.unlink(SYMLINK)
            except OSError:
                pass

    # The timer outlives the expect script, and for a terminating arm the roster
    # must be read after the session is really gone -- both belong here, not in a
    # pty script that dies with its session.
    if arm in ("sessionend", "sigkill"):
        gp = os.path.join(results, "watcher-gone.json")
        deadline = time.monotonic() + 90
        while time.monotonic() < deadline and not os.path.exists(gp):
            time.sleep(0.5)
        time.sleep(3)
        subprocess.run([ROSTERDUMP, os.path.join(results, "roster-after.json")], timeout=120)

    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    by_event = {}
    for m in marks:
        by_event.setdefault(m["event"], []).append(m)
    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    tr = rm.read_transcript(sid)
    pid = (by_event.get("claude_pid") or [{}])[-1].get("pid")

    before, after = jload(os.path.join(results, "bypid-before.json")), jload(os.path.join(results, "bypid-after.json"))
    wbefore, wafter = jload(os.path.join(results, "watcher-before.json")), jload(os.path.join(results, "watcher-after.json"))
    gone = jload(os.path.join(results, "watcher-gone.json"))
    roster = jload(os.path.join(results, "roster-after.json"))
    mine = None
    if roster and before:
        want = before.get("brigade_session_id")
        mine = next((s for s in roster["sessions"] if s["session_id"] == want), None)

    ctx = " || ".join(c["content"] for c in tr["context_lines"])
    results_text = " || ".join(r["content"] for r in tr["tool_results"])

    v = {
        "arm": arm, "check": CHECK[arm], "run": run_idx, "sid": sid, "cwd": proj,
        "claude_pid": pid, "expect_process": how,
        "canary_ok": "canary_ok" in by_event,
        "brigade_executions": [e.get("cmd_head") for e in execs],
        "other_tools": sorted({a.get("tool") for a in atts if a.get("tool") not in ("Bash",)}),
        "context_lines": [c["content"] for c in tr["context_lines"]],
        "context_line_count": len(tr["context_lines"]),
        "shadow_word_in_context": ("shadow" in ctx.lower()),
        "decoy_in_tool_output": ("DECOY" in results_text),
        "session_id_before": (before or {}).get("brigade_session_id"),
        "session_id_after": (after or {}).get("brigade_session_id"),
        "session_name_before": (before or {}).get("session_name"),
        "watcher_pid_before": (wbefore or {}).get("pid") or (wbefore or {}).get("watcher_pid"),
        "watcher_pid_after": (wafter or {}).get("pid") or (wafter or {}).get("watcher_pid"),
        "watcher_gone": gone,
        "roster_entry_after": ({"name": mine["session_name"], "state": mine["state"]} if mine else None),
        "renamed_to": newname if arm == "rename" else None,
        "transcript": {k: tr[k] for k in ("path", "versions", "models", "tool_uses")},
    }
    # ---- per-check verdicts, each stated as the thing the row asserts ---------
    if arm == "rename":
        v["verdict"] = bool(mine and mine["session_name"] == newname)
    elif arm in ("clear", "compact"):
        v["verdict"] = bool(v["session_id_before"] and v["session_id_before"] == v["session_id_after"]
                            and v["watcher_pid_before"] == v["watcher_pid_after"])
    elif arm == "shadow":
        v["verdict"] = bool(v["shadow_word_in_context"] and v["decoy_in_tool_output"])
    elif arm == "symlink":
        v["verdict"] = bool(not v["shadow_word_in_context"] and v["brigade_executions"])
    else:   # sessionend, sigkill
        v["verdict"] = bool(gone and gone.get("gone_after_ms") is not None
                            and mine and mine["state"] == "offline")
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", default="rename,clear,compact,shadow,symlink,sessionend,sigkill")
    ap.add_argument("--runs", type=int, default=1)
    ap.add_argument("--results", default=os.path.join(REPO, ".ignored", "e3-interactive"))
    ap.add_argument("--tag", default=None)
    ap.add_argument("--t1", type=int, default=120)
    ap.add_argument("--hard-timeout", type=int, default=900)
    args = ap.parse_args()

    root = os.path.join(args.results, args.tag or common.stamp("e3life"))
    os.makedirs(root, exist_ok=True)
    guard = rm.ReportOnlyGuard(root)
    rows = []
    for arm in args.arms.split(","):
        for i in range(1, args.runs + 1):
            v = one_run(arm, i, root, args)
            rows.append(v)
            print(json.dumps({k: v[k] for k in
                              ("arm", "check", "run", "canary_ok", "verdict",
                               "session_id_before", "session_id_after",
                               "watcher_pid_before", "watcher_pid_after",
                               "shadow_word_in_context", "decoy_in_tool_output",
                               "roster_entry_after", "watcher_gone")}))
            sys.stdout.flush()
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump({"claude_version": subprocess.run(["claude", "--version"],
                                                    capture_output=True, text=True).stdout.strip(),
                   "permission_mode": "bypassPermissions (these checks are not about prompts)",
                   "runs": rows, "config_protection": guard.verify()}, f, indent=2)
    print("results: " + root)


if __name__ == "__main__":
    main()
