#!/usr/bin/env python3
"""E0-8 check (b): does a skill's `allowed-tools: Bash(brigade:*)` actually stop
the permission prompt in INTERACTIVE Manual mode?

D20 claims "once the skill is in play a reply needs no prompt for that turn",
but that was only ever verified in `-p`. This measures it where the product
promise lives: a real pty, `--permission-mode default`, and NO permission rules
of any kind (`settings/none.json` sets allow/deny/ask all empty).

THE DETECTOR IS MECHANICAL, not prose. A PreToolUse hook fires when the model has
produced tool parameters and BEFORE the permission decision; the fake `brigade`
on the plugin's PATH appends a row only when the command actually EXECUTES. So

    attempt row present  +  no exec row  =  the permission system stopped it,
                                            and with no deny rule that is a PROMPT

and the session visibly stalls on it (waitcount returns -1). The session.log is
grepped afterwards for the dialog's own words purely as corroboration.

Three arms, each repeated:
  baseline  -- no skill; the three commands asked for directly in the prompt
  skill     -- the same three commands, delivered by `brigade:teammsg`,
               which declares `allowed-tools: Bash(brigade:*)`
  plain     -- control: `brigade:teamplain`, byte-identical except that it
               declares NO allowed-tools. It separates "a skill is in play"
               from "the grant did it".

Each arm runs the SAME three commands in the same order -- a read verb, a second
read verb, then a WRITE verb -- so a grant that covers only the first command, or
only read verbs, shows up as a stall at a known index. A follow-up prompt in the
same session, after the routine, tests whether the grant outlives its turn.
"""
import argparse
import json
import os
import re
import secrets
import shutil
import sys

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402

HERE = common.HERE

DIRECT_PROMPT = (
    "Run these three commands with the Bash tool, ONE tool call each, in this exact "
    "order, and print each command's complete output verbatim before moving on: "
    "1. brigade sessions   2. brigade whoami   3. brigade send peer-9 hello-from-skill . "
    "Run each command EXACTLY as written: no redirections, no pipes, no quoting changes, "
    "no extra arguments. Do not combine them into one call. Do not run any other command "
    "and do not use any other tool. When the third command has been run, say ROUTINE-COMPLETE and stop."
)

SKILL_PROMPT = (
    "Invoke the skill named `brigade:{skill}` with the Skill tool and follow it exactly. "
    "Do not do anything else first."
)

FOLLOWUP_PROMPT = (
    "Now run the single Bash command `brigade sessions` once more, exactly as written, "
    "and print its output. Then stop."
)

ARMS = {
    "baseline": DIRECT_PROMPT,
    "skill": SKILL_PROMPT.format(skill="teammsg"),
    "plain": SKILL_PROMPT.format(skill="teamplain"),
    # ADVERSARIAL PASS. The `plain` control only separates "a skill is in play"
    # from "the grant did it". It does NOT rule out the rival hypothesis that
    # ANY allowed-tools declaration blanket-suppresses Bash prompting for the
    # turn. `mismatch` is byte-identical to teammsg in its body and declares
    # allowed-tools too -- but the pattern is Bash(ls:*), which cannot match a
    # brigade command. Pattern-scoped grant => it prompts like `plain`.
    "mismatch": SKILL_PROMPT.format(skill="teammismatch"),
}

BODY = r"""
mark spawn arm @ARM@ run @RUN@
spawn -noecho env @UNSETS@ claude --plugin-dir "@PLUGIN@" --settings "@SETTINGS@" --permission-mode default

onboard
if {![canary @TAIL@ 3]} {
    mark canary_failed
    shutdown
    exit 0
}

mark arm_prompt_sent
submit "@PROMPT@" "arm prompt"

# ---- THE SKILL TOOL ITSELF PROMPTS IN MANUAL MODE ---------------------------
# [Measured, E0-8: `Use skill "brigade:teammsg"? / Do you want to proceed?` with
# "1. Yes" highlighted.] That gate sits BEFORE the thing under test, so the run
# would otherwise stall on the wrong dialog and say nothing about the Bash grant.
# It is answered by a one-time "Yes" -- a bare Enter, since option 1 is the
# highlighted default -- which persists NO rule and leaves settings/none.json
# still the only permission configuration in force.
# `proceed` is a SINGLE word: box drawing interleaves cursor escapes and a
# multi-word regex never matches the dialog.
if {@SKILLARM@} {
    expect {
        -re {proceed} {
            mark skill_dialog_shown
            nap 1
            xsend "\r" "approve the Skill tool once (option 1, Yes)"
            mark skill_dialog_approved
        }
        timeout { mark skill_dialog_absent }
    }
    nap 2
}

set w1 [waitcount "@EXEC@" 1 @T1@]
if {$w1 < 0} {
    mark stall index 1 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
    set w2 -1
    set w3 -1
} else {
    mark exec_seen index 1 waited_ms $w1 attempts [ndcount "@ATT@"]
    set w2 [waitcount "@EXEC@" 2 @T2@]
    if {$w2 < 0} {
        mark stall index 2 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
        set w3 -1
    } else {
        mark exec_seen index 2 waited_ms $w2 attempts [ndcount "@ATT@"]
        set w3 [waitcount "@EXEC@" 3 @T3@]
        if {$w3 < 0} {
            mark stall index 3 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
        } else {
            mark exec_seen index 3 waited_ms $w3 attempts [ndcount "@ATT@"]
        }
    }
}
mark stage1_done w1 $w1 w2 $w2 w3 $w3

# The follow-up only makes sense if the turn under test finished; a session
# sitting on a permission dialog would receive these keystrokes in the DIALOG.
if {$w3 >= 0} {
    nap 6
    mark followup_sent
    submit "@FOLLOWUP@" "follow-up prompt, no skill, new turn"
    set w4 [waitcount "@EXEC@" 4 @T4@]
    if {$w4 < 0} {
        mark stall index 4 attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
    } else {
        mark exec_seen index 4 waited_ms $w4 attempts [ndcount "@ATT@"]
    }
    mark stage2_done w4 $w4
}

nap 4
mark final attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
shutdown
"""

DIALOG_WORDS = re.compile(
    r"(Do you want to proceed|Do you want to run|Yes, and don|No, and tell Claude|"
    r"esc to interrupt.*|Allow|permission)", re.I)


def one_run(arm, run_idx, tag_root, args):
    tag = "%s/%s-run%d" % (tag_root, arm, run_idx)
    results = common.new_results(tag)
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)
    proj = os.path.join(HERE, "proj", "%s-%s-%d" % (args.projprefix, arm, run_idx))
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)

    env, stripped = common.nested_env({"BRIGADE_E08_STATE": state})
    tail = secrets.token_hex(2).upper()

    body = (BODY
            .replace("@ARM@", arm).replace("@RUN@", str(run_idx))
            .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
            .replace("@PLUGIN@", common.PLUGIN)
            .replace("@SETTINGS@", os.path.join(HERE, "settings", "none.json"))
            .replace("@TAIL@", tail)
            .replace("@PROMPT@", ARMS[arm])
            .replace("@FOLLOWUP@", FOLLOWUP_PROMPT)
            .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
            .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
            .replace("@SKILLARM@", "1" if arm in ("skill", "plain", "mismatch") else "0")
            .replace("@T1@", str(args.t1)).replace("@T2@", str(args.t2))
            .replace("@T3@", str(args.t3)).replace("@T4@", str(args.t4)))
    exp = common.write_expect(results, body)

    how = common.run_expect(exp, proj, env, args.hard_timeout, results)

    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    hooks = common.read_ndjson(os.path.join(state, "hook-env.log"))

    log = ""
    lp = os.path.join(results, "session.log")
    if os.path.exists(lp):
        log = open(lp, errors="replace").read()
    dialog_hits = sorted({m.group(0)[:60] for m in DIALOG_WORDS.finditer(log)})

    skill_atts = [a for a in atts if a.get("tool") == "Skill"]
    brig_att = [a for a in atts if a.get("tool") == "Bash"
                and (a.get("cmd_head") or "").startswith("brigade")]
    by_event = {}
    for m in marks:
        by_event.setdefault(m["event"], []).append(m)

    stalls = [int(m["index"]) for m in by_event.get("stall", [])]
    seen = {int(m["index"]): int(m["waited_ms"]) for m in by_event.get("exec_seen", [])}

    canary_ok = "canary_ok" in by_event
    stage1_cmds = [e.get("cmd") for e in execs[:3]]

    verdict = {
        "arm": arm, "run": run_idx, "tag": tag,
        "expect_process": how,
        "canary_ok": canary_ok,
        "skill_dialog_shown": "skill_dialog_shown" in by_event,
        "skill_dialog_approved": "skill_dialog_approved" in by_event,
        "onboarding_marks": [m["event"] for m in marks
                             if m["event"].startswith("onboard") or m["event"].startswith("canary")],
        "skill_tool_attempts": len(skill_atts),
        "skill_names_invoked": [a.get("skill_name") for a in skill_atts],
        "bash_brigade_attempts": len(brig_att),
        "brigade_executions": len(execs),
        "stage1_commands_executed": stage1_cmds,
        "first_stall_index": stalls[0] if stalls else None,
        "exec_wait_ms": seen,
        "attempt_cmds": [(a.get("tool"), (a.get("cmd_head") or "")[:60]) for a in atts],
        "dialog_words_in_log": dialog_hits,
        "hook_rows": hooks,
        "prompted": bool(stalls) and len(brig_att) > len(execs),
        "unprompted_all_three": len(execs) >= 3 and not [s for s in stalls if s <= 3],
        "followup_unprompted": (4 in seen),
        "followup_ran": "followup_sent" in by_event,
    }
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2)
    return verdict


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", default="baseline,skill,plain")
    ap.add_argument("--projprefix", default="b")
    ap.add_argument("--runs", type=int, default=3)
    ap.add_argument("--t1", type=int, default=100)
    ap.add_argument("--t2", type=int, default=60)
    ap.add_argument("--t3", type=int, default=60)
    ap.add_argument("--t4", type=int, default=90)
    ap.add_argument("--hard-timeout", type=int, default=900)
    ap.add_argument("--tag", default=None)
    args = ap.parse_args()

    tag_root = args.tag or common.stamp("b")
    root = common.new_results(tag_root)
    guard = common.Guard(root)
    outer = common.outer_identity()

    rows = []
    for arm in args.arms.split(","):
        for i in range(1, args.runs + 1):
            v = one_run(arm, i, tag_root, args)
            rows.append(v)
            print(json.dumps({k: v[k] for k in
                              ("arm", "run", "canary_ok", "skill_names_invoked",
                               "bash_brigade_attempts",
                               "brigade_executions", "first_stall_index",
                               "prompted", "unprompted_all_three",
                               "followup_ran", "followup_unprompted",
                               "stage1_commands_executed")}))
            sys.stdout.flush()

    hook_rows = [h for r in rows for h in r["hook_rows"]]
    summary = {
        "tag": tag_root,
        "claude_version": os.popen("claude --version").read().strip(),
        "permission_mode": "default (Manual)",
        "settings": "settings/none.json -- allow/deny/ask all empty",
        "runs": rows,
        "isolation": common.isolation_verdict(hook_rows, outer),
        "config_protection": guard.verify(),
        "per_arm": {},
    }
    for arm in args.arms.split(","):
        rs = [r for r in rows if r["arm"] == arm and r["canary_ok"]]
        summary["per_arm"][arm] = {
            "valid_runs": len(rs),
            "skill_names_invoked": [n for r in rs for n in r["skill_names_invoked"]],
            "prompted": sum(1 for r in rs if r["prompted"]),
            "unprompted_all_three": sum(1 for r in rs if r["unprompted_all_three"]),
            "first_stall_indexes": [r["first_stall_index"] for r in rs],
            "executions": [r["brigade_executions"] for r in rs],
            "followup_unprompted": [r["followup_unprompted"] for r in rs if r["followup_ran"]],
        }
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump(summary, f, indent=2)
    print(json.dumps({k: summary[k] for k in
                      ("tag", "per_arm", "isolation", "config_protection")}, indent=2))
    print("results: " + root)


if __name__ == "__main__":
    main()
