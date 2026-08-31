#!/usr/bin/env python3
"""E0-7 ADVERSARIAL run: the negative controls, IN LIVE SESSIONS.

main-1 showed each session naming its own profile. That is compatible with a
hardcoded / defaulted / environment-sourced answer unless the map is shown to be
CAUSAL. Three concurrent sessions, identical in every way except what the
SessionStart hook writes into the by-pid map:

  alpha    normal  -> map says "alpha"                 expect whoami == alpha
  bravo    poison  -> map says "POISON-XXXXXX" (a string generated at run time
                      inside the hook, present in NO settings file, NO env var,
                      NO prompt), while the option and the REGISTRY still say
                      "bravo"                          expect whoami == POISON-*
  charlie  nomap    -> hook writes NO map entry at all  expect whoami exit 6

If bravo answers "bravo", the map is not the resolution path. If charlie answers
anything, there is a fallback. If alpha and bravo still work while charlie
fails, the breakage is scoped to exactly the session whose map was removed.

Team-scoping controls, seeded into the registry BEFORE launch:
  delta   team=ops   live, pid alive   -> MUST appear in every session's listing
  sierra  team=sales live, pid alive   -> MUST NOT appear (different team)
  echo    team=ops   live, pid DEAD    -> appears, process_alive must be false

Same protections as run.py: prefix env stripping, per-run isolation assertions,
config hashing/restore, surgical fullscreenAutoDisabled watch, throwaway project
directories.
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import threading
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin_adv")
STATE = os.path.join(PLUGIN, "state")

PROMPT = (
    "You are an environment probe. Run EXACTLY these three commands with the Bash "
    "tool, one at a time, in this order, and report each command's complete output "
    "verbatim, including any error message and the exit status:\n"
    "1. brigade whoami --json; echo \"exit=$?\"\n"
    "2. brigade sessions --json; echo \"exit=$?\"\n"
    "3. printenv CLAUDE_PLUGIN_OPTION_PROFILE; echo \"printenv-exit=$?\"\n"
    "Some of these commands are EXPECTED to fail. A failure is a valid result: do "
    "not retry it, do not try to fix it, do not investigate. Just report what it "
    "printed and move to the next command. Do not run any other command, do not "
    "edit any file, do not use any other tool. Then stop."
)

MODES = {"alpha": "normal", "bravo": "poison", "charlie": "nomap"}


def sha256_file(p):
    try:
        with open(p, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()
    except OSError:
        return ""


def nested_env():
    keep = {"CLAUDE_CONFIG_DIR"}
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k in keep:
            out[k] = v
        elif k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"):
            stripped.append(k)
        else:
            out[k] = v
    return out, sorted(stripped)


def jdump(path, obj):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        json.dump(obj, f, indent=2)


def main():
    tag = "adv-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results", tag)
    os.makedirs(results, exist_ok=True)
    profiles = ["alpha", "bravo", "charlie"]

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

    if os.path.isdir(STATE):
        shutil.rmtree(STATE)
    os.makedirs(STATE)
    jdump(os.path.join(STATE, "adv-control.json"), MODES)

    # ---- decoy registrations (team-scoping controls) ----
    keepalive = []
    decoys = {}
    for name, team, live_proc in (("delta", "ops", True),
                                  ("sierra", "sales", True),
                                  ("echo", "ops", False)):
        if live_proc:
            proc = subprocess.Popen(["sleep", "600"],
                                    stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            keepalive.append(proc)
            pid = str(proc.pid)
        else:
            pid = "999999"          # a pid that is not running
        decoys[name] = {"team": team, "pid": pid, "expected_alive": live_proc}
        jdump(os.path.join(STATE, "registry", name, pid + ".json"),
              {"schema": "e07-by-pid/1", "claude_pid": pid, "profile": name,
               "team": team, "map_nonce": "DECOY-" + name.upper(),
               "cwd": "/decoy/" + name, "state": "live",
               "registered_under": name, "_decoy": True})

    env, stripped = nested_env()
    launches = []
    for prof in profiles:
        proj = os.path.join(HERE, "proj-" + prof)
        os.makedirs(proj, exist_ok=True)
        launches.append({
            "profile": prof, "project": proj,
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
        t.join(timeout=max(1, 300 - int(time.time() - t0)))
    for prof, p in procs.items():
        if p.poll() is None:
            p.kill()
    for t in threads:
        t.join(timeout=10)
    t1 = time.time()
    for p in keepalive:
        p.kill()

    exits = {prof: p.poll() for prof, p in procs.items()}

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

    cfg = []
    for i, p in enumerate(protected):
        post = sha256_file(p)
        changed = post != pre[p]
        if changed and os.path.exists(os.path.join(snap, "%d.snap" % i)):
            shutil.copy2(os.path.join(snap, "%d.snap" % i), p)
        cfg.append({"path": p, "pre": pre[p], "post": post, "changed": changed})
    fs_post = fullscreen_key()

    starts = [h for h in hook_env if h["event"] == "hook_session_start"]
    iso_rows = []
    for h in starts:
        iso_rows.append({
            "profile_option": h["CLAUDE_PLUGIN_OPTION_PROFILE"],
            "claude_pid": h["claude_pid"], "session": h["session"],
            "socket": h["socket"], "token_sha12": h["token_sha12"],
            "differs_from_outer_socket": bool(h["socket"]) and h["socket"] != outer["socket"],
            "differs_from_outer_session": h["session"] != outer["session"],
            "differs_from_outer_token": h["token_sha12"] != outer["token_sha12"],
            "differs_from_outer_pid": h["claude_pid"] != outer["pid"],
        })
    sockets = [r["socket"] for r in iso_rows]
    sess_ids = [r["session"] for r in iso_rows]
    iso_ok = (all(all(r[k] for k in r if k.startswith("differs_")) for r in iso_rows)
              and len(set(sockets)) == len(sockets) and len(sockets) == 3
              and len(set(sess_ids)) == len(sess_ids))

    verdict = {
        "tag": tag, "wall_seconds": round(t1 - t0, 1),
        "config_dir_in_use": cfgdir, "profiles": profiles, "adv_modes": MODES,
        "decoys": decoys, "claude_exit_codes": exits, "outer": outer,
        "stripped_env_vars": stripped,
        "isolation": {"rows": iso_rows, "ok": iso_ok},
        "config_protection": cfg,
        "config_protection_ok": all(not c["changed"] for c in cfg),
        "fullscreenAutoDisabled": {"pre": fs_pre, "post": fs_post,
                                   "appeared": fs_pre != fs_post},
        "hook_env": hook_env, "registrations": regs,
        "registry_dirs": registry, "by_pid_map": bypid, "brigade_calls": calls,
    }
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(verdict, f, indent=2)
    print(json.dumps({k: v for k, v in verdict.items()
                      if k not in ("hook_env", "brigade_calls")}, indent=2))
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
