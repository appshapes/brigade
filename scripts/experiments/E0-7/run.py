#!/usr/bin/env python3
"""E0-7 driver: TWO Claude Code sessions, TWO profiles, ONE CLAUDE_CONFIG_DIR.

Reuses the E0-3 probe-plugin shape and the E0-3/E0-4 protections:

  * environment isolation by PREFIX -- CLAUDECODE, CLAUDE_PID and every
    CLAUDE_* except CLAUDE_CONFIG_DIR are stripped from the nested environment,
    and each nested session's socket/session/token are asserted different from
    the outer harness session's and from each other's;
  * config protection -- SHA-256 of the config dir's settings.json, the repo
    CLAUDE.md and ~/.claude/CLAUDE.md before and after, restored on change and
    reported either way; plus a surgical watch on .claude.json's
    `fullscreenAutoDisabled` (E0-5 saw Claude Code write it by itself).

Both sessions run concurrently in throwaway project directories.
"""
import argparse
import hashlib
import json
import os
import shutil
import subprocess
import sys
import threading
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin")
STATE = os.path.join(PLUGIN, "state")

PROMPT = (
    "You are an environment probe. Run EXACTLY these four commands with the Bash "
    "tool, one at a time, in this order, and report each command's complete output "
    "verbatim:\n"
    "1. brigade wait-peer 90\n"
    "2. brigade whoami --json\n"
    "3. brigade sessions --json\n"
    "4. printenv CLAUDE_PLUGIN_OPTION_PROFILE; echo \"printenv-exit=$?\"\n"
    "Do not run any other command, do not edit any file, and do not use any other "
    "tool. Then stop."
)


def sha256_file(p):
    try:
        with open(p, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()
    except OSError:
        return ""


def nested_env():
    """Strip by PREFIX. Keep CLAUDE_CONFIG_DIR (the login)."""
    keep = {"CLAUDE_CONFIG_DIR"}
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k in keep:
            out[k] = v
        elif k.startswith("CLAUDE") or k == "CLAUDECODE" or k == "AI_AGENT":
            stripped.append(k)
        else:
            out[k] = v
    return out, sorted(stripped)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--tag", default=None)
    ap.add_argument("--timeout", type=int, default=300)
    ap.add_argument("--profiles", default="alpha,bravo")
    args = ap.parse_args()

    tag = args.tag or ("e07-" + time.strftime("%Y%m%d-%H%M%S"))
    results = os.path.join(HERE, "results", tag)
    os.makedirs(results, exist_ok=True)
    profiles = args.profiles.split(",")

    cfgdir = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")
    protected = [os.path.join(cfgdir, "settings.json"),
                 "/Users/rjae/Development/appshapes/brigade/CLAUDE.md",
                 os.path.expanduser("~/.claude/CLAUDE.md")]
    snap = os.path.join(results, "config-snapshot")
    os.makedirs(snap, exist_ok=True)
    pre = {}
    for i, p in enumerate(protected):
        pre[p] = sha256_file(p)
        if os.path.exists(p):
            shutil.copy2(p, os.path.join(snap, "%d.snap" % i))

    # .claude.json is written concurrently by the OUTER session, so it is never
    # hashed or restored wholesale -- only the one key E0-5 saw appear is watched.
    dotclaude = os.path.join(cfgdir, ".claude.json")

    def fullscreen_key():
        try:
            with open(dotclaude) as f:
                return json.load(f).get("fullscreenAutoDisabled", "<absent>")
        except Exception:
            return "<unreadable>"

    fs_pre = fullscreen_key()

    outer = {"session": os.environ.get("CLAUDE_CODE_SESSION_ID", ""),
             "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET", ""),
             "pid": os.environ.get("CLAUDE_PID", "")}
    otok = os.environ.get("CLAUDE_CODE_MESSAGING_TOKEN", "")
    outer["token_sha12"] = hashlib.sha256(otok.encode()).hexdigest()[:12] if otok else ""

    # fresh state for this run
    if os.path.isdir(STATE):
        shutil.rmtree(STATE)
    os.makedirs(STATE)

    env, stripped = nested_env()
    launches = []
    for prof in profiles:
        proj = os.path.join(HERE, "proj-" + prof)
        os.makedirs(proj, exist_ok=True)
        launches.append({
            "profile": prof,
            "project": proj,
            "settings": os.path.join(HERE, "settings", prof + ".json"),
            "stdout": os.path.join(results, prof + ".transcript.jsonl"),
            "stderr": os.path.join(results, prof + ".stderr"),
        })

    procs = {}

    def run_one(L):
        cmd = ["claude", "-p", PROMPT,
               "--plugin-dir", PLUGIN,
               "--settings", L["settings"],
               "--permission-mode", "bypassPermissions",
               "--output-format", "stream-json", "--verbose"]
        with open(L["stdout"], "wb") as so, open(L["stderr"], "wb") as se:
            p = subprocess.Popen(cmd, cwd=L["project"], env=env, stdout=so, stderr=se)
            procs[L["profile"]] = p
            p.wait()

    t0 = time.time()
    threads = [threading.Thread(target=run_one, args=(L,)) for L in launches]
    for t in threads:
        t.start()
    for t in threads:
        t.join(timeout=max(1, args.timeout - int(time.time() - t0)))
    for prof, p in procs.items():
        if p.poll() is None:
            p.kill()
    for t in threads:
        t.join(timeout=10)
    t1 = time.time()

    exits = {prof: p.poll() for prof, p in procs.items()}

    # ---- collect plugin state ----
    def read_ndjson(name):
        out = []
        p = os.path.join(STATE, name)
        if os.path.exists(p):
            for line in open(p):
                line = line.strip()
                if line:
                    try:
                        out.append(json.loads(line))
                    except Exception:
                        pass
        return out

    hook_env = read_ndjson("hook-env.log")
    regs = read_ndjson("registrations.ndjson")
    calls = read_ndjson("fake-brigade.ndjson")
    per_profile_calls = {}
    pdir = os.path.join(STATE, "profiles")
    if os.path.isdir(pdir):
        for prof in sorted(os.listdir(pdir)):
            f = os.path.join(pdir, prof, "calls.ndjson")
            rows = []
            if os.path.exists(f):
                for line in open(f):
                    line = line.strip()
                    if line:
                        try:
                            rows.append(json.loads(line))
                        except Exception:
                            pass
            per_profile_calls[prof] = rows

    registry = {}
    rdir = os.path.join(STATE, "registry")
    if os.path.isdir(rdir):
        for prof in sorted(os.listdir(rdir)):
            d = os.path.join(rdir, prof)
            if os.path.isdir(d):
                registry[prof] = sorted(os.listdir(d))
    bypid = {}
    bdir = os.path.join(STATE, "by-pid")
    if os.path.isdir(bdir):
        for fn in sorted(os.listdir(bdir)):
            bypid[fn] = json.load(open(os.path.join(bdir, fn)))

    shutil.copytree(STATE, os.path.join(results, "state"), dirs_exist_ok=True)

    # ---- config protection ----
    cfg = []
    for i, p in enumerate(protected):
        post = sha256_file(p)
        changed = post != pre[p]
        if changed and os.path.exists(os.path.join(snap, "%d.snap" % i)):
            shutil.copy2(os.path.join(snap, "%d.snap" % i), p)
        cfg.append({"path": p, "pre": pre[p], "post": post, "changed": changed})
    fs_post = fullscreen_key()

    # ---- isolation ----
    starts = [h for h in hook_env if h["event"] == "hook_session_start"]
    iso_rows = []
    for h in starts:
        iso_rows.append({
            "profile_option": h["CLAUDE_PLUGIN_OPTION_PROFILE"],
            "claude_pid": h["claude_pid"],
            "session": h["session"],
            "socket": h["socket"],
            "token_sha12": h["token_sha12"],
            "differs_from_outer_socket": bool(h["socket"]) and h["socket"] != outer["socket"],
            "differs_from_outer_session": h["session"] != outer["session"],
            "differs_from_outer_token": h["token_sha12"] != outer["token_sha12"],
            "differs_from_outer_pid": h["claude_pid"] != outer["pid"],
        })
    sockets = [r["socket"] for r in iso_rows]
    sessions_ids = [r["session"] for r in iso_rows]
    iso_ok = (all(r["differs_from_outer_socket"] and r["differs_from_outer_session"]
                  and r["differs_from_outer_token"] and r["differs_from_outer_pid"]
                  for r in iso_rows)
              and len(set(sockets)) == len(sockets) and len(sockets) >= 1
              and len(set(sessions_ids)) == len(sessions_ids))

    verdict = {
        "tag": tag,
        "wall_seconds": round(t1 - t0, 1),
        "config_dir_in_use": cfgdir,
        "profiles": profiles,
        "claude_exit_codes": exits,
        "outer": outer,
        "stripped_env_vars": stripped,
        "isolation": {"rows": iso_rows, "ok": iso_ok},
        "config_protection": cfg,
        "config_protection_ok": all(not c["changed"] for c in cfg),
        "fullscreenAutoDisabled": {"pre": fs_pre, "post": fs_post,
                                   "appeared": fs_pre != fs_post},
        "hook_env": hook_env,
        "registrations": regs,
        "registry_dirs": registry,
        "by_pid_map": bypid,
        "brigade_calls": calls,
        "per_profile_call_counts": {k: len(v) for k, v in per_profile_calls.items()},
    }
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2)
    print(json.dumps({k: v for k, v in verdict.items()
                      if k not in ("hook_env", "brigade_calls")}, indent=2))
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
