#!/usr/bin/env python3
"""E0-8 check (f): after `/clear`, does `brigade whoami` in the Bash tool still
resolve through an UNCHANGED CLAUDE_PID?

E0-5 established that /clear keeps the OS pid and rotates the native session id;
E0-7 established that whoami resolves via the by-pid map. This confirms the two
compose -- i.e. that a cleared session is still the same Brigade principal.

The proof is the MAP NONCE. The SessionStart hook mints it, writes it into
state/by-pid/<CLAUDE_PID>.json and nowhere else -- not into any environment
variable, not into the prompt, not onto the screen. `brigade whoami` can only
print it by reading that file, which it can only find via CLAUDE_PID. So the
same nonce before and after /clear is proof the same pid resolved to the same
principal, not merely that a number looked familiar.

The Bash tool's CLAUDE_CODE_SESSION_ID is RECORDED on both sides (the task asks
for it to be recorded, not relied upon) so the rotation, or lack of it, is on
the record next to the pid that did not move.

Permission mode is `default` (Manual) with `Bash(brigade:*)` allowed, so the two
whoami turns run without a prompt and the run measures identity, not permissions.
"""
import argparse
import json
import os
import secrets
import shutil
import sys

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402

HERE = common.HERE

# `brigade whoami` is the ONLY command asked for. An earlier version also asked
# for `printenv CLAUDE_CODE_SESSION_ID`, which is NOT covered by Bash(brigade:*)
# and therefore raised its own permission dialog -- and the "/clear" typed next
# went into that DIALOG instead of the input box, so /clear never ran and the run
# silently measured nothing. The Bash tool's CLAUDE_CODE_SESSION_ID is recorded
# by the fake brigade out of its own environment anyway, so the extra command was
# never needed.
WHOAMI_PROMPT = (
    "Run the single Bash command `brigade whoami` exactly as written, and print its "
    "complete output verbatim. Do not run any other command. Then stop."
)

BODY = r"""
mark spawn run @RUN@
spawn -noecho env @UNSETS@ claude --plugin-dir "@PLUGIN@" --settings "@SETTINGS@" --permission-mode default

onboard
if {![canary @TAIL@ 3]} { mark canary_failed ; shutdown ; exit 0 }

# ---- BEFORE /clear ----
mark before_prompt
submit "@WHOAMI@" "whoami, before /clear"
set b [waitcount "@EXEC@" 1 @T@]
mark before_done waited_ms $b execs [ndcount "@EXEC@"]
if {$b < 0} { mark before_stalled ; shutdown ; exit 0 }
nap 6

# ---- /clear ----
mark clear_sent
submit "/clear" "/clear"
# Mechanical confirmation that /clear really happened: SessionStart fires again
# (source "clear"), so the hook log gains a second row. Without this the run
# cannot tell "/clear ran and the pid held" from "/clear never ran".
set c [waitcount "@HOOK@" 2 40]
mark clear_confirmed hook_rows [ndcount "@HOOK@"] waited_ms $c
nap 8
mark clear_settled

# ---- AFTER /clear ----
mark after_prompt
submit "@WHOAMI@" "whoami, after /clear"
set a [waitcount "@EXEC@" 2 @T@]
mark after_done waited_ms $a execs [ndcount "@EXEC@"]

nap 4
mark final execs [ndcount "@EXEC@"] attempts [ndcount "@ATT@"]
shutdown
"""


def one_run(run_idx, tag_root, args):
    tag = "%s/run%d" % (tag_root, run_idx)
    results = common.new_results(tag)
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)
    proj = os.path.join(HERE, "proj", "f-%d" % run_idx)
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)

    env, stripped = common.nested_env({"BRIGADE_E08_STATE": state})
    body = (BODY
            .replace("@RUN@", str(run_idx))
            .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
            .replace("@PLUGIN@", common.PLUGIN)
            .replace("@SETTINGS@", os.path.join(HERE, "settings", "allow.json"))
            .replace("@TAIL@", secrets.token_hex(2).upper())
            .replace("@WHOAMI@", WHOAMI_PROMPT)
            .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
            .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
            .replace("@HOOK@", os.path.join(state, "hook-env.log"))
            .replace("@T@", str(args.t)))
    exp = common.write_expect(results, body)
    how = common.run_expect(exp, proj, env, args.hard_timeout, results)

    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    hooks = common.read_ndjson(os.path.join(state, "hook-env.log"))
    by = {}
    for m in marks:
        by.setdefault(m["event"], []).append(m)

    whoamis = [e for e in execs if e.get("cmd") == "whoami"]
    before = whoamis[0] if whoamis else None
    after = whoamis[1] if len(whoamis) > 1 else None

    def pick(e):
        if not e:
            return None
        return {"claude_pid": e.get("claude_pid"),
                "map_nonce": e.get("map_nonce"),
                "resolved": e.get("resolved"),
                "bash_tool_CLAUDE_CODE_SESSION_ID": e.get("claude_session_id"),
                "claude_env_names": e.get("claude_env_names")}

    b, a = pick(before), pick(after)
    verdict = {
        "run": run_idx, "tag": tag, "expect_process": how,
        "canary_ok": "canary_ok" in by,
        "clear_sent": "clear_sent" in by,
        "clear_confirmed_by_second_session_start": bool(
            by.get("clear_confirmed") and int(by["clear_confirmed"][0]["hook_rows"]) >= 2),
        "before": b, "after": a,
        "session_start_hook_rows": hooks,
        "pid_unchanged": bool(b and a) and b["claude_pid"] == a["claude_pid"],
        "map_nonce_unchanged": bool(b and a) and b["map_nonce"] == a["map_nonce"],
        "resolved_after_clear": bool(a and a["resolved"]),
        "bash_session_id_rotated": (
            bool(b and a) and b["bash_tool_CLAUDE_CODE_SESSION_ID"]
            != a["bash_tool_CLAUDE_CODE_SESSION_ID"]),
        "session_start_hook_fires": len(hooks),
        "session_start_sources": [h.get("hook_source") for h in hooks],
        "session_start_session_ids": [h.get("session") for h in hooks],
        "session_start_pids": sorted({h.get("claude_pid") for h in hooks}),
        "registrations": common.read_ndjson(
            os.path.join(state, "registrations.ndjson")),
        "hook_rows": hooks,
    }
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2)
    return verdict


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--runs", type=int, default=2)
    ap.add_argument("--t", type=int, default=90)
    ap.add_argument("--hard-timeout", type=int, default=900)
    ap.add_argument("--tag", default=None)
    args = ap.parse_args()

    tag_root = args.tag or common.stamp("f")
    root = common.new_results(tag_root)
    guard = common.Guard(root)
    outer = common.outer_identity()

    rows = []
    for i in range(1, args.runs + 1):
        v = one_run(i, tag_root, args)
        rows.append(v)
        print(json.dumps({k: v[k] for k in
                          ("run", "canary_ok", "pid_unchanged", "map_nonce_unchanged",
                           "resolved_after_clear", "bash_session_id_rotated",
                           "session_start_hook_fires", "session_start_sources",
                           "session_start_session_ids", "session_start_pids",
                           "before", "after")}, indent=2))
        sys.stdout.flush()

    hook_rows = [h for r in rows for h in r["hook_rows"]]
    summary = {"tag": tag_root, "runs": rows,
               "isolation": common.isolation_verdict(hook_rows, outer),
               "config_protection": guard.verify()}
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump(summary, f, indent=2)
    print(json.dumps({"isolation": summary["isolation"],
                      "config_protection": summary["config_protection"]}, indent=2))
    print("results: " + root)


if __name__ == "__main__":
    main()
