#!/usr/bin/env python3
"""Fold the E0-8 run directories into the three answers the task asks for."""
import json
import os
import sys

HERE = os.path.dirname(os.path.realpath(__file__))
R = os.path.join(HERE, "results")


def load(p):
    with open(p) as f:
        return json.load(f)


def b(tag):
    s = load(os.path.join(R, tag, "summary.json"))
    print("=" * 78)
    print("(b) SKILL GRANT -- interactive Manual mode, %s" % s["claude_version"])
    print("    settings: %s" % s["settings"])
    print("=" * 78)
    hdr = ("%-9s %-4s %-6s %-8s %-6s %-7s %-9s %-20s %s"
           % ("arm", "run", "canary", "attempts", "execs", "stall", "prompted", "commands run", "followup"))
    print(hdr)
    for r in s["runs"]:
        print("%-9s %-4d %-6s %-8d %-6d %-7s %-9s %-20s %s"
              % (r["arm"], r["run"], r["canary_ok"], r["bash_brigade_attempts"],
                 r["brigade_executions"], r["first_stall_index"], r["prompted"],
                 ",".join(x or "?" for x in r["stage1_commands_executed"]) or "-",
                 ("unprompted" if r["followup_unprompted"] else
                  ("PROMPTED" if r["followup_ran"] else "-"))))
    print()
    for arm, v in s["per_arm"].items():
        print("  %-9s valid=%d prompted=%d all-three-unprompted=%d stalls=%s execs=%s followup_unprompted=%s"
              % (arm, v["valid_runs"], v["prompted"], v["unprompted_all_three"],
                 v["first_stall_indexes"], v["executions"], v["followup_unprompted"]))
    print()
    print("  isolation ok: %s" % s["isolation"]["ok"])
    for n in s["isolation"]["nested"]:
        print("    nested pid=%s session=%s socket=%s  (all differ from outer: %s)"
              % (n["claude_pid"], n["session"][:8], n["socket"],
                 n["pid_differs"] and n["session_differs"] and n["socket_differs"] and n["token_differs"]))
    print("  outer: pid=%s session=%s socket=%s"
          % (s["isolation"]["outer"]["pid"], s["isolation"]["outer"]["session"][:8],
             s["isolation"]["outer"]["socket"]))
    cp = s["config_protection"]
    print("  config protection ok: %s" % cp["ok"])
    for f in cp["files"]:
        print("    %-55s changed=%s" % (f["path"], f["changed"]))
    print("    .claude.json: projects_added=%s top_level_changed=%s"
          % (cp["dot_claude_json"]["projects_added"],
             cp["dot_claude_json"].get("top_level_keys_that_changed")))


def f(tag):
    s = load(os.path.join(R, tag, "summary.json"))
    print("=" * 78)
    print("(f) /clear AND THE PID")
    print("=" * 78)
    for r in s["runs"]:
        bb, aa = r["before"], r["after"]
        print("run %d canary=%s" % (r["run"], r["canary_ok"]))
        print("   before  pid=%s nonce=%s bash CLAUDE_CODE_SESSION_ID=%s"
              % (bb and bb["claude_pid"], bb and bb["map_nonce"],
                 bb and bb["bash_tool_CLAUDE_CODE_SESSION_ID"]))
        print("   after   pid=%s nonce=%s bash CLAUDE_CODE_SESSION_ID=%s resolved=%s"
              % (aa and aa["claude_pid"], aa and aa["map_nonce"],
                 aa and aa["bash_tool_CLAUDE_CODE_SESSION_ID"], aa and aa["resolved"]))
        print("   pid_unchanged=%s nonce_unchanged=%s resolved_after_clear=%s bash_session_rotated=%s"
              % (r["pid_unchanged"], r["map_nonce_unchanged"],
                 r["resolved_after_clear"], r["bash_session_id_rotated"]))
        print("   SessionStart fires=%s sources=%s ids=%s pids=%s"
              % (r["session_start_hook_fires"], r["session_start_sources"],
                 [x[:8] for x in r["session_start_session_ids"]], r["session_start_pids"]))
    print("  isolation ok: %s   config ok: %s"
          % (s["isolation"]["ok"], s["config_protection"]["ok"]))


def h(tag):
    s = load(os.path.join(R, tag, "summary.json"))
    print("=" * 78)
    print("(h) 12 KB QUOTED HEREDOC, Bash(brigade:*) ALLOWED")
    print("=" * 78)
    print("%-14s %-8s %-12s %-10s %-8s %-9s %s"
          % ("mode", "target", "actual len", "attempts", "execs", "body len", "outcome"))
    for r in s["rows"]:
        print("%-14s %-8d %-12s %-10d %-8d %-9s %s"
              % (r["mode"], r["target_size"], r.get("actual_cmd_len"),
                 r["attempt_rows"], r["exec_rows"], r.get("exec_body_len"),
                 r["outcome"]))
    print("  isolation ok: %s   config ok: %s"
          % (s["isolation"]["ok"], s["config_protection"]["ok"]))


if __name__ == "__main__":
    which, tag = sys.argv[1], sys.argv[2]
    {"b": b, "f": f, "h": h}[which](tag)
