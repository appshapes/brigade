#!/usr/bin/env python3
"""Fold an E3-interactive run directory into the rows the checklist needs.

usage: python3 scripts/experiments/E3-interactive/analyze.py .ignored/e3-interactive/<tag>
"""
import json
import os
import sys


def load(p):
    with open(p) as f:
        return json.load(f)


def main():
    root = sys.argv[1]
    s = load(os.path.join(root, "summary.json"))
    print("=" * 100)
    print("E3-interactive checks 1 and 2 -- %s" % s["claude_version"])
    print("  requested mode: %s" % s["permission_mode_requested"])
    print("  settings:       %s" % s["settings"])
    print("  plugin:         %s" % s["plugin"])
    print("=" * 100)

    hdr = ("%-11s %-4s %-6s %-7s %-22s %-7s %-6s %-6s %-7s %s"
           % ("arm", "run", "canary", "manual", "mode (hook/transcript)", "dialog",
              "att", "exec", "stall", "check2"))
    print(hdr)
    print("-" * 100)
    for r in s["runs"]:
        modes = "%s/%s" % (",".join(r["mode_from_hook_payload"]) or "-",
                           ",".join(r["mode_from_transcript"]) or "-")
        if not r["followup_ran"]:
            c2 = "-"
        elif r.get("check2_inconclusive_skill_reinvoked"):
            c2 = "VOID:skill"
        elif r.get("check2_inconclusive_no_bare_attempt"):
            c2 = "VOID:no-att"
        elif r["check2_prompted"]:
            c2 = "PROMPTED"
        elif r["check2_unprompted"]:
            c2 = "unprompted"
        else:
            c2 = "?"
        print("%-11s %-4s %-6s %-7s %-22s %-7s %-6s %-6s %-7s %s"
              % (r["arm"], r["run"], r["canary_ok"], r["mode_is_manual"], modes,
                 r["skill_dialog_shown"], r["brigade_attempts"],
                 r["brigade_executions"], r["first_stall_index"], c2))
    print()

    for r in s["runs"]:
        print("--- %s run %s  (pid %s, cwd %s)" % (r["arm"], r["run"], r["claude_pid"], r["cwd"]))
        print("    mode: hook=%s transcript=%s by-pid-map=%s"
              % (r["mode_from_hook_payload"], r["mode_from_transcript"], r["mode_from_bypid_map"]))
        print("    skill: dialog_shown=%s answered=%s option2=%s attempts=%s executions=%s names=%s"
              % (r["skill_dialog_shown"], r["skill_dialog_answered"], r["skill_option2_selected"],
                 r["skill_tool_attempts"], r["skill_tool_executions"], r["skill_names"]))
        print("    brigade attempts: %s" % r["brigade_attempt_cmds"])
        print("    brigade execs:    %s" % r["brigade_exec_cmds"])
        if r["non_brigade_bash_attempts"]:
            print("    OTHER bash (evasive-form watch): %s" % r["non_brigade_bash_attempts"])
        if r["other_tool_attempts"]:
            print("    OTHER tools attempted:           %s" % r["other_tool_attempts"])
        print("    dialog word counts on screen: %s" % r["dialog_words_in_log"])
        print("    dialog actually answered in the skill window: %s"
              % ("BASH BOX -- VOID" if r.get("answered_a_bash_dialog")
                 else ("Skill box" if r.get("skill_dialog_shown") else "none")))
        if r.get("answered_a_bash_dialog"):
            print("    *** ANSWERED A BASH DIALOG (or matched with no Skill attempt) -- VOID")
        if r["arm"] == "baseline":
            print("    null control stalled on an unexecuted bare attempt: %s" % r.get("baseline_stalled"))
        print("    check1_pass=%s  check2_pass=%s  quiesced=%s"
              % (r.get("check1_pass"), r.get("check2_pass"), r.get("quiesced_before_followup")))
        if r.get("fullpath_attempts"):
            print("    FULL-PATH invocations (the skill forbids these): %s" % r["fullpath_attempts"])
        tr = r["transcript"]
        print("    transcript: %s" % tr["path"])
        print("      versions=%s models=%s" % (tr["versions"], tr["models"]))
        for cl in tr["context_lines"][:2]:
            print("      context: %s" % cl["content"][:160])
        for tu in tr["tool_uses"]:
            print("      USE %-10s %s" % (tu["name"], tu["input"][:110]))
        for res in tr["tool_results"]:
            print("      RES err=%-5s %s" % (res["is_error"], res["content"][:110]))
        print()

    cp = s["config_protection"]
    print("config protection ok: %s" % cp["ok"])
    for f in cp["files"]:
        print("   %-58s changed=%s" % (f["path"], f["changed"]))
    print("restored anything: %s" % cp.get("restored_anything"))
    dc = cp["dot_claude_json"]
    if not dc.get("readable"):
        print("   .claude.json NOT READABLE at %s -- drift not checked" % dc["path"])
    else:
        print("   .claude.json (%s) projects added: %s" % (dc["path"], dc["projects_added"]))
    print()
    print("NULL CONTROL: a baseline run MUST show stall=1. If it does not, the detector cannot")
    print("go red and every other arm in this table is void.")


if __name__ == "__main__":
    main()
