#!/usr/bin/env python3
"""E0-5 ADVERSARIAL PASS — discriminator for an anomaly the TRANSITIONS report
did not mention.

In BOTH /clear runs that the pty-block invalidated (results2/c-clear-105719 and
-110501) the NATIVE session id at SessionEnd differs from the one at
SessionStart, while NO SessionStart(source=clear) and NO SessionEnd(reason=clear)
was ever recorded:

  105719  SessionStart startup 25d52888 ... SessionEnd prompt_input_exit b1df5b09
  110501  SessionStart startup 0518c34f ... SessionEnd prompt_input_exit 8ef2a09d

Two explanations, with very different consequences for 6.3 and D9:

  (A) the queued /clear was processed during the eof drain and the boundary
      happened WITHOUT its hooks being recorded -- i.e. a session boundary can
      occur that a hook-driven design never sees;
  (B) an interactive session that never completes a turn simply gets a different
      native id by the time SessionEnd fires, and /clear has nothing to do with
      it -- in which case there is no finding at all.

This run discriminates: an interactive session that types NOTHING but /exit. If
the id still moves, (B). If it holds, (A) survives and deserves its own run.

Reuses the E0-4/E0-5 house patterns unchanged: the plugin_c recorders (with
BRIGADE_E05_SPAWN=0, since no watcher is involved), expectlib.tcl's draining
waits, and the trust-dialog handling (Down then Enter; single-word regexes).
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
    send_user "\n\[\[E05I $event $args ms=$ms\]\]\n"
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

# Idle, but DRAINING the pty the whole time, so the session is never blocked on
# write. Nothing whatsoever is typed here: no prompt, no slash command.
mark idle_start
nap {hold}
mark idle_end

send -- "/exit\r"
expect eof
mark eof
"""


def main():
    hold = int(os.environ.get("E05_IDLE_HOLD", "45"))
    tag = "idle-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results3", tag)
    state = os.path.join(HERE, "state3", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)
    project = os.path.join("/tmp", "e05-idle", tag)
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    topguard = g2.TopKeyGuard(results)
    outer = common.outer_identity()
    out = {"tag": tag, "results": results, "state_dir": state,
           "project_dir": project, "hold_seconds": hold,
           "typed_during_run": ["/exit only"],
           "stripped_env_names": common.leaky_names()}

    ready1 = os.path.join(state, "ready-1.json")
    env = common.child_env({"BRIGADE_STATE_DIR": state, "BRIGADE_E05_HOME": HERE,
                            "BRIGADE_E05_TAG": tag, "BRIGADE_E05_SPAWN": "0"})
    exp = EXPECT.format(results=results, plugin=PLUGIN, ready1=ready1, hold=hold,
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
        time.sleep(2)

    starts = g2.read_ndjson(os.path.join(state, "startup.ndjson"))
    ends = g2.read_ndjson(os.path.join(state, "sessionend.ndjson"))
    prompts = g2.read_ndjson(os.path.join(state, "prompt.ndjson"))
    out["startup_records"] = [{"n": r["n"], "source": r.get("payload_source"),
                               "pid": r.get("env_claude_pid"),
                               "payload_session_id": r.get("payload_session_id"),
                               "env_session_id": r.get("env_session_id"),
                               "socket": r.get("env_socket"),
                               "token_sha12": r.get("env_token_sha12"),
                               "ts": r["ts"]} for r in starts]
    out["sessionend_records"] = [{"reason": r.get("payload_reason"),
                                  "pid": r.get("env_claude_pid"),
                                  "payload_session_id": r.get("payload_session_id"),
                                  "env_session_id": r.get("env_session_id"),
                                  "ts": r["ts"]} for r in ends]
    out["n_userpromptsubmit"] = len(prompts)

    if starts and ends:
        s0, e0 = starts[0], ends[-1]
        out["answer"] = {
            "n_sessionstart": len(starts),
            "n_sessionend": len(ends),
            "sessionstart_id": s0.get("payload_session_id"),
            "sessionend_id": e0.get("payload_session_id"),
            "id_moved_without_any_slash_command":
                s0.get("payload_session_id") != e0.get("payload_session_id"),
            "explains_the_anomaly_as_benign":
                s0.get("payload_session_id") != e0.get("payload_session_id"),
        }
    out["marks"] = g2.read_ndjson(os.path.join(results, "marks.ndjson"))
    out["config"] = guard.verify(results)
    out["dot_claude_json"] = topguard.revert([project])
    shutil.rmtree(project, ignore_errors=True)
    json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
    print(json.dumps({k: v for k, v in out.items()
                      if k not in ("marks", "dot_claude_json", "stripped_env_names")},
                     indent=2))
    print("results: " + results)


if __name__ == "__main__":
    main()
