#!/usr/bin/env python3
"""E0-8 (e): another `brigade` earlier on PATH.

Two questions, each answered mechanically rather than by inspection:

  1. Does the SessionStart hook print its shadowing warning? -- the hook looks
     `brigade` up on its OWN PATH (which never contains the plugin's bin/), so
     anything it finds necessarily precedes the appended plugin bin/ in the Bash
     tool. The warning text goes to stdout, i.e. the context line, and is read
     back out of the session transcript.

  2. Does the Bash tool then run the OTHER binary? -- BOTH candidates record
     their own identity when they run. Exactly one of them writes a row, and the
     row says which. No inference from PATH ordering is involved.

Three arms:
  shadow  -- a foreign decoy `brigade` earlier on PATH        (expect: warn, decoy runs)
  clean   -- nothing named brigade anywhere but the plugin    (expect: no warn, plugin runs)
  symlink -- the documented ~/.local/bin symlink TO the
             plugin's own bootstrap earlier on PATH           (expect: no warn, plugin runs)
"""
import argparse
import glob
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin_e")

DECOY = r'''#!/usr/bin/env python3
"""E0-8 (e) DECOY brigade -- a foreign binary earlier on PATH. It records that
IT was the one that ran, so the question 'which binary executed' is answered by
a written artefact rather than by reasoning about PATH order."""
import json, os, sys
STATE = os.environ["BRIGADE_E08_STATE"]
rec = {"who": "DECOY", "argv": sys.argv, "self": os.path.realpath(__file__),
       "path": os.environ.get("PATH", ""),
       "claude_pid": os.environ.get("CLAUDE_PID", ""), "cwd": os.getcwd()}
with open(os.path.join(STATE, "exec.ndjson"), "a") as f:
    f.write(json.dumps(rec) + "\n")
print("E08-RAN=DECOY self=%s" % rec["self"])
print("brigade version 9.9.9-stale-homebrew")
'''


def nested_env(extra=None):
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k == "CLAUDE_CONFIG_DIR":
            out[k] = v
        elif k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"):
            stripped.append(k)
        else:
            out[k] = v
    if extra:
        out.update(extra)
    return out, sorted(stripped)


def cfgdir():
    return os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")


def transcript_for(sid):
    hits = glob.glob(os.path.join(cfgdir(), "projects", "*", sid + ".jsonl"))
    return hits[0] if hits else None


def one(arm, idx, args):
    results = os.path.join(HERE, "results", "e", "%s-run%d" % (arm, idx))
    shutil.rmtree(results, ignore_errors=True)
    os.makedirs(results)
    state = os.path.join(results, "state")
    os.makedirs(state)
    proj = os.path.join(HERE, "proj", "e-%s-%d" % (arm, idx))
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)

    early = os.path.join(results, "earlybin")
    os.makedirs(early)
    base = arm.replace("-settingsenv", "")
    if base == "shadow":
        d = os.path.join(early, "brigade")
        open(d, "w").write(DECOY)
        os.chmod(d, 0o755)
    elif base == "symlink":
        os.symlink(os.path.join(PLUGIN, "bin", "brigade"), os.path.join(early, "brigade"))
    # arm == "clean": earlybin stays empty

    path = early + ":" + os.environ["PATH"]
    # Two routes by which a foreign brigade can precede the plugin's bin/. The
    # realistic one is the user's own inherited PATH; the settings-env one is
    # measured separately so the two are never conflated.
    settings = {"permissions": {"allow": ["Bash(brigade:*)"], "deny": [], "ask": []}}
    if arm.endswith("-settingsenv"):
        settings["env"] = {"PATH": path}
        launch_path = os.environ["PATH"]          # process PATH stays clean
    else:
        launch_path = path                        # inherited PATH carries the shadow
    sp = os.path.join(results, "settings.json")
    json.dump(settings, open(sp, "w"), indent=2)

    env, stripped = nested_env({"BRIGADE_E08_STATE": state, "PATH": launch_path})
    prompt = ("Run the single Bash command `brigade whoami` exactly as written and print its "
              "complete output verbatim. Do not run any other command.")
    cmd = ["claude", "-p", prompt, "--plugin-dir", PLUGIN, "--settings", sp,
           "--permission-mode", "default", "--output-format", "json"]
    t0 = time.time()
    p = subprocess.run(cmd, cwd=proj, env=env, capture_output=True, text=True,
                       stdin=subprocess.DEVNULL, timeout=args.hard_timeout)
    wall = time.time() - t0
    open(os.path.join(results, "claude.stdout.json"), "w").write(p.stdout)
    open(os.path.join(results, "claude.stderr.txt"), "w").write(p.stderr)
    try:
        res = json.loads(p.stdout)
    except Exception:
        res = {}

    def nd(name):
        fp = os.path.join(state, name)
        return [json.loads(l) for l in open(fp) if l.strip()] if os.path.exists(fp) else []

    hooks, execs, attempts = nd("hook.ndjson"), nd("exec.ndjson"), nd("attempts.ndjson")

    ctx = None
    tp = transcript_for(res.get("session_id") or "")
    if tp:
        shutil.copy2(tp, os.path.join(results, "transcript.jsonl"))
        for line in open(tp):
            if not line.strip():
                continue
            try:
                e = json.loads(line)
            except Exception:
                continue
            a = e.get("attachment") or {}
            if a.get("hookEvent") == "SessionStart":
                ctx = {"content": a.get("content"), "stdout": a.get("stdout"),
                       "exitCode": a.get("exitCode")}

    rec = {
        "arm": arm, "run": idx,
        "route": "settings.env.PATH" if arm.endswith("-settingsenv") else "inherited process PATH",
        "launch_PATH_head": launch_path.split(":")[0],
        "settings": settings,
        "early_dir": early,
        "hook_rows": hooks,
        "hook_saw_shadow": hooks[0]["shadowed"] if hooks else None,
        "hook_which_brigade": hooks[0]["which_brigade"] if hooks else None,
        "hook_which_realpath": hooks[0]["which_brigade_realpath"] if hooks else None,
        "plugin_bin_on_hook_path": hooks[0]["plugin_bin_on_hook_path"] if hooks else None,
        "context_line": ctx,
        "warning_in_context": bool(ctx and "WARNING" in (ctx.get("content") or "")),
        "exec_rows": execs,
        "who_ran": [e["who"] for e in execs],
        "bash_attempts": attempts,
        "model_answer": (res.get("result") or "")[:600],
        "claude_exit": p.returncode, "claude_wall_s": round(wall, 3),
        "permission_denials": res.get("permission_denials"),
        "stripped": stripped, "results": results,
    }
    json.dump(rec, open(os.path.join(results, "summary.json"), "w"), indent=2)
    return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--arms", default="shadow,clean,symlink")
    ap.add_argument("--hard-timeout", type=float, default=180)
    a = ap.parse_args()
    rows = []
    for arm in a.arms.split(","):
        for i in range(1, a.reps + 1):
            r = one(arm, i, a)
            rows.append(r)
            print("[%s run%d] hook_shadowed=%s warning_in_context=%s who_ran=%s which=%s" % (
                arm, i, r["hook_saw_shadow"], r["warning_in_context"],
                r["who_ran"], r["hook_which_realpath"]))
            if r["context_line"]:
                print("    CTX: %s" % (r["context_line"]["content"] or "").replace("\n", " | ")[:400])
            sys.stdout.flush()
    os.makedirs(os.path.join(HERE, "results", "e"), exist_ok=True)
    json.dump(rows, open(os.path.join(HERE, "results", "e", "all.json"), "w"), indent=2)


if __name__ == "__main__":
    main()
