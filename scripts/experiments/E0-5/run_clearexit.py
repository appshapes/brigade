#!/usr/bin/env python3
"""E0-5 ADVERSARIAL PASS — does a /clear boundary that is immediately followed by
an exit still record its hooks?

Where this comes from. Two /clear runs of the TRANSITIONS stage (results2/
c-clear-105719 and -110501) ended with a native session id DIFFERENT from the
one at SessionStart while recording no SessionStart(source=clear) and no
SessionEnd(reason=clear) at all. The stage wrote both runs off as "the pty-block
bug" and did not mention the id movement. run_idle.py has since ruled out the
benign explanation: an idle interactive session that types nothing keeps its id
from SessionStart to SessionEnd, so the id did not drift on its own -- the
/clear really did execute, and its hooks left no record.

In those runs the queued /clear was processed at the very end, immediately
before the exit. That suggests a hypothesis with nothing to do with the harness
bug, and one that matters for 6.3: a boundary that is immediately followed by
the session exiting may lose its hook records, because the SessionEnd budget is
~1.5 s and the process is already on its way out.

This run tests exactly that, on a healthy, fully drained pty: settle, send
/clear, and send /exit right behind it without waiting for the boundary's
SessionStart. Everything else -- plugin_c recorders, expectlib.tcl draining
waits, trust handling -- is the house pattern, unchanged.
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
    send_user "\n\[\[E05X $event $args ms=$ms\]\]\n"
}}
mark spawn
spawn -noecho env {unsets} claude --plugin-dir "{plugin}" --permission-mode bypassPermissions
expect {{
  -re {{trust}} {{ mark trust_shown; expect -re {{exit}}; sleep 1
                  send -- "\033\[B"; sleep 1; send -- "\r"; mark trust_accepted }}
  -re {{shortcuts|Welcome|bypass}} {{ mark ui_ready_no_trust }}
  timeout {{ mark trust_timeout }}
}}
mark hook_ready_1 waited [waitfile "{ready1}" 600]
nap 12
mark settled

# The boundary, with NO wait for its SessionStart -- the exit follows straight
# behind it. This is the ordering the two invalidated runs ended up in.
mark clear_send
send -- "/clear"
nap 2
send -- "\r"
mark clear_enter
nap {gap}
mark exit_send gap {gap}
send -- "/exit\r"
expect eof
mark eof
"""


def main():
    gap = os.environ.get("E05_GAP", "0.3")
    tag = "clearexit-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results3", tag)
    state = os.path.join(HERE, "state3", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)
    project = os.path.join("/tmp", "e05-clearexit", tag)
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    topguard = g2.TopKeyGuard(results)
    outer = common.outer_identity()
    out = {"tag": tag, "results": results, "state_dir": state,
           "project_dir": project, "gap_seconds_clear_to_exit": float(gap),
           "stripped_env_names": common.leaky_names()}

    ready1 = os.path.join(state, "ready-1.json")
    env = common.child_env({"BRIGADE_STATE_DIR": state, "BRIGADE_E05_HOME": HERE,
                            "BRIGADE_E05_TAG": tag, "BRIGADE_E05_SPAWN": "0"})
    exp = EXPECT.format(results=results, plugin=PLUGIN, ready1=ready1, gap=gap,
                        lib=os.path.join(HERE, "expectlib.tcl"),
                        unsets=" ".join(common.env_unset_args()))
    with open(os.path.join(results, "drive.exp"), "w") as f:
        f.write(exp)
    proc = subprocess.Popen(["expect", "-f", os.path.join(results, "drive.exp")],
                            cwd=project, env=env,
                            stdout=open(os.path.join(results, "interactive.raw"), "wb"),
                            stderr=subprocess.STDOUT)

    ok = g2.waitfor(ready1, 700)
    nested = g2.read_json(os.path.join(state, "nested-env-1.json")) or {}
    gate = g2.gate(nested, outer)
    out["isolation"] = gate
    if not ok or not gate["ok"]:
        try:
            proc.kill()
        except Exception:
            pass
        out["aborted"] = "isolation gate" if ok else "hook never became ready"
    else:
        out["claude_pid"] = int(nested["claude_pid"])
        try:
            proc.wait(timeout=900)
        except subprocess.TimeoutExpired:
            proc.kill()
        time.sleep(3)

    starts = g2.read_ndjson(os.path.join(state, "startup.ndjson"))
    matched = g2.read_ndjson(os.path.join(state, "matched.ndjson"))
    ends = g2.read_ndjson(os.path.join(state, "sessionend.ndjson"))
    out["startup_records"] = [{"n": r["n"], "source": r.get("payload_source"),
                               "pid": r.get("env_claude_pid"),
                               "session_id": r.get("payload_session_id"),
                               "socket": r.get("env_socket"),
                               "token_sha12": r.get("env_token_sha12"),
                               "ts": r["ts"], "epoch": r["epoch"]} for r in starts]
    out["matched_sessionstart_records"] = len(matched)
    out["sessionend_records"] = [{"reason": r.get("payload_reason"),
                                  "pid": r.get("env_claude_pid"),
                                  "session_id": r.get("payload_session_id"),
                                  "ts": r["ts"], "epoch": r["epoch"]} for r in ends]

    ids_start = [r.get("payload_session_id") for r in starts]
    ids_end = [r.get("payload_session_id") for r in ends]
    out["answer"] = {
        "n_sessionstart": len(starts),
        "sessionstart_sources": [r.get("payload_source") for r in starts],
        "n_sessionend": len(ends),
        "sessionend_reasons": [r.get("payload_reason") for r in ends],
        "clear_boundary_happened": bool(ids_end and ids_start
                                        and ids_end[-1] != ids_start[0]),
        "sessionstart_clear_recorded": any(r.get("payload_source") == "clear"
                                           for r in starts),
        "sessionend_clear_recorded": any(r.get("payload_reason") == "clear"
                                         for r in ends),
    }
    out["answer"]["boundary_lost_its_hooks"] = bool(
        out["answer"]["clear_boundary_happened"]
        and not (out["answer"]["sessionstart_clear_recorded"]
                 and out["answer"]["sessionend_clear_recorded"]))
    out["marks"] = g2.read_ndjson(os.path.join(results, "marks.ndjson"))
    out["config"] = guard.verify(results)
    out["dot_claude_json"] = topguard.revert([project])
    shutil.rmtree(project, ignore_errors=True)
    json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
    print(json.dumps({k: v for k, v in out.items()
                      if k in ("tag", "gap_seconds_clear_to_exit", "claude_pid",
                               "startup_records", "sessionend_records",
                               "matched_sessionstart_records", "answer")}, indent=2))
    print("results: " + results)


if __name__ == "__main__":
    main()
