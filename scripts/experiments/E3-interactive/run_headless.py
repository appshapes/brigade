#!/usr/bin/env python3
"""The headless half of E3-interactive: what `claude -p` CAN settle about checks 1-2.

`-p` has no approval surface, so with `--permission-prompts none` anything that
would raise a prompt is DENIED instead. That is useless for counting dialogs, but
it is decisive about the permission DECISION, and it is far cheaper and steadier
than a pty. Three arms:

  direct    nothing pre-approved, no skill: `brigade sessions` must be DENIED.
            The null control -- it shows the rig can go red.
  grant     ONLY the `Skill` tool pre-approved, so the Bash decision can come from
            nothing but the skill's own `allowed-tools: Bash(brigade:*)`.
            The brigade commands must RUN.
  expiry    the `grant` session RESUMED, nothing pre-approved, no skill:
            `brigade sessions` must be DENIED again -- the grant died with the turn.

Every artefact is kept: each arm's own state directory, stream, stderr and the
transcript path. usage:

    python3 scripts/experiments/E3-interactive/run_headless.py [--results DIR]
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile
import uuid

HERE = os.path.dirname(os.path.realpath(__file__))
REPO = os.path.dirname(os.path.dirname(os.path.dirname(HERE)))
sys.dont_write_bytecode = True   # do not leave __pycache__ in a committed experiment dir
sys.path.insert(0, os.path.join(REPO, "scripts", "experiments", "E0-8"))
import common  # noqa: E402

PLUGIN = os.path.join(REPO, "plugin")
HOOKBIN = os.path.join(HERE, "bin")

DIRECT = ("Without using any skill, run the Bash command `brigade sessions` directly and "
          "paste its output. Use the bare command name `brigade`, never a path to the binary.")
SKILLQ = "Use the brigade team-messaging skill to list the team's sessions and tell me who is online."


def settings(path):
    doc = {
        "permissions": {"allow": [], "deny": [], "ask": []},
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


def run(arm, prompt, results, allowed=None, sid=None, resume=False, proj=None):
    d = os.path.join(results, arm)
    state = os.path.join(d, "state")
    os.makedirs(state, exist_ok=True)
    proj = proj or tempfile.mkdtemp(prefix="brigade-e3hl-%s-" % arm)
    setf = settings(os.path.join(d, "settings.json"))
    sid = sid or str(uuid.uuid4())

    env, stripped = common.nested_env({"BRIGADE_E3_STATE": state})
    cmd = ["claude", "-p", prompt,
           "--plugin-dir", PLUGIN, "--settings", setf,
           "--permission-mode", "default", "--permission-prompts", "none",
           "--output-format", "stream-json", "--verbose", "--max-turns", "8"]
    cmd[3:3] = ["--resume", sid] if resume else ["--session-id", sid]
    if allowed:
        cmd += ["--allowedTools", allowed]

    with open(os.path.join(d, "stream.jsonl"), "wb") as out, \
         open(os.path.join(d, "stderr.txt"), "wb") as err, \
         open(os.devnull, "rb") as devnull:
        subprocess.run(cmd, cwd=proj, env=env, stdin=devnull, stdout=out, stderr=err,
                       timeout=600)

    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    fullpath = common.read_ndjson(os.path.join(state, "fullpath-exec.ndjson"))
    skills = common.read_ndjson(os.path.join(state, "skill-exec.ndjson"))
    tr = None
    for dirpath, _dirs, files in os.walk(os.path.join(common.cfgdir(), "projects")):
        if sid + ".jsonl" in files:
            tr = os.path.join(dirpath, sid + ".jsonl")
            break
    denials = []
    if tr:
        with open(tr, errors="replace") as f:
            for line in f:
                try:
                    r = json.loads(line)
                except Exception:
                    continue
                if r.get("type") == "user":
                    for c in ((r.get("message") or {}).get("content") or []):
                        if isinstance(c, dict) and c.get("type") == "tool_result" and c.get("is_error"):
                            denials.append(str(c.get("content"))[:160])
    v = {
        "arm": arm, "sid": sid, "cwd": proj, "allowedTools": allowed, "resumed": resume,
        "argv": cmd,
        "attempts": [{"tool": a.get("tool"), "form": a.get("brigade_form"),
                      "mode": a.get("permission_mode"),
                      "what": a.get("skill_name") or a.get("cmd_head")} for a in atts],
        "bare_executions": [e.get("cmd_head") for e in execs],
        "fullpath_executions": [e.get("cmd_head") for e in fullpath],
        "skill_executions": [s.get("skill_name") for s in skills],
        "denials": denials,
        "transcript": tr,
    }
    with open(os.path.join(d, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--results", default=os.path.join(REPO, ".ignored", "e3-interactive"))
    ap.add_argument("--tag", default=None)
    args = ap.parse_args()
    root = os.path.join(args.results, args.tag or common.stamp("e3hl"))
    os.makedirs(root, exist_ok=True)

    rows = []
    rows.append(run("direct", DIRECT, root))
    grant_sid = str(uuid.uuid4())
    grant_proj = tempfile.mkdtemp(prefix="brigade-e3hl-grant-")
    rows.append(run("grant", SKILLQ, root, allowed="Skill", sid=grant_sid, proj=grant_proj))
    rows.append(run("expiry", DIRECT, root, sid=grant_sid, resume=True, proj=grant_proj))

    for v in rows:
        print("--- %s  (allowedTools=%s resumed=%s)" % (v["arm"], v["allowedTools"], v["resumed"]))
        for a in v["attempts"]:
            print("    attempt tool=%-6s form=%-8s mode=%-8s %s"
                  % (a["tool"], a["form"], a["mode"], a["what"]))
        print("    bare executions:     %s" % v["bare_executions"])
        print("    fullpath executions: %s" % v["fullpath_executions"])
        print("    skill executions:    %s" % v["skill_executions"])
        for d in v["denials"]:
            print("    DENIED: %s" % d)
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump({"claude_version": subprocess.run(["claude", "--version"],
                                                    capture_output=True, text=True).stdout.strip(),
                   "runs": rows}, f, indent=2)
    print("results: " + root)


if __name__ == "__main__":
    main()
