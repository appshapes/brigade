#!/usr/bin/env python3
"""E0-8 (c): the Bash sandbox, loopback, and the read-only-home refresh path.

Everything here talks to the REAL local Supabase stack over loopback, so a
verdict is the sandbox's or the server's and never a mock's. The CLI on the
plugin's bin/ is a real Go binary (cbin/) that signs in, lists sessions and
sends a message, and prints one JSON diagnosis per run: the proxy variables it
was handed, the HTTP status of every request, the transport error verbatim, and
whether a token refresh happened and whether it could be persisted.

The sandboxed process writes NOTHING for the harness: its whole output is read
back out of the `tool_result` events of `--output-format stream-json`, together
with any <sandbox_violations> block Claude Code appends there. So nothing about
the measurement depends on the sandboxed process being able to write.

The throwaway HOME deliberately lives OUTSIDE the project directory and outside
TMPDIR, because those two are exactly the places the Bash sandbox leaves
writable -- a profile under the project tree would have made the read-only-home
arm meaningless.
"""
import argparse
import json
import os
import shutil
import stat
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin_c")
CLI = os.path.join(HERE, "cbin", "brigade")
# Outside the project tree and outside TMPDIR on purpose (see module docstring).
HOMEROOT = os.path.expanduser("~/.e08c-homes")

ENVT = {}
for line in open("/Users/rjae/Development/appshapes/brigade/.env.test"):
    line = line.strip()
    if "=" in line:
        k, v = line.split("=", 1)
        ENVT[k] = v.strip('"')

PROMPT = ("Run these two Bash commands, exactly as written, one at a time, and print the "
          "complete output of each verbatim:\n"
          "1. `brigade sessions --json`\n"
          "2. `brigade send peer hello-from-sandbox`\n"
          "Do not run any other command, and do not use a helper such as awk, sed or a shell "
          "variable. If a command fails, print its error verbatim and continue to the next one.")

# ADVERSARIAL PASS: the enforcement probe runs ONE command that carries its own
# positive and negative controls, so a "success" arm proves the sandbox was live.
PROBE_PROMPT = ("Run the single Bash command `brigade probe --force-proxy` exactly as written "
                "and print its complete output verbatim. Do not run any other command and do "
                "not use a helper such as awk, sed, cat or a shell variable.")


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


def chmod_tree(root, mode_dir, mode_file):
    for d, dirs, files in os.walk(root, topdown=False):
        for f in files:
            os.chmod(os.path.join(d, f), mode_file)
        os.chmod(d, mode_dir)


def bootstrap(home):
    """Fresh anonymous principal per arm: a refresh rotates the token family, so
    reusing one profile across arms would eventually hand a two-steps-behind
    token to a later arm and revoke the family for reasons nothing to do with
    the sandbox."""
    pdir = os.path.join(home, ".config", "brigade", "profiles", "default")
    os.makedirs(pdir, mode=0o700, exist_ok=True)
    env = {"PATH": "/usr/bin:/bin", "HOME": os.environ["HOME"],
           "BRIGADE_CONFIG_DIR": pdir,
           "BRIGADE_API_URL": ENVT["API_URL"], "BRIGADE_KEY": ENVT["PUBLISHABLE_KEY"],
           "BRIGADE_JWT_SECRET": ENVT["JWT_SECRET"]}
    p = subprocess.run([CLI, "bootstrap"], env=env, capture_output=True, text=True)
    d = json.loads(p.stdout)
    if not d.get("ok"):
        raise RuntimeError("bootstrap failed: " + json.dumps(d)[:600])
    return pdir, d["result"]


def make_expired_401(pdir):
    """An access token that is genuinely expired but whose FILE claims it is
    fresh, so the CLI presents it and the SERVER rejects it. That exercises the
    401/PGRST303 branch rather than the proactive `<90 s left` branch."""
    exp = json.load(open(os.path.join(pdir, "session.expired.json")))
    exp["expires_at"] = int(time.time()) + 3600      # the file lies
    json.dump(exp, open(os.path.join(pdir, "session.json"), "w"), indent=2)


def use_expired_proactive(pdir):
    shutil.copy2(os.path.join(pdir, "session.expired.json"),
                 os.path.join(pdir, "session.json"))


# Extra levers beyond allowedDomains, kept in one place: whether the CLI is told
# to ignore NO_PROXY and go through the sandbox's own proxy (which separates
# "the sandbox denies the direct loopback connect" from "the proxy refuses the
# host"), and whether `brigade` is listed in sandbox.excludedCommands, the
# documented way to run one command outside the sandbox.
EXTRA = {
    "sbx-forceproxy":    {"force_proxy": True},
    "sbx-fp-ip":         {"force_proxy": True},
    "sbx-fp-probe":      {"force_proxy": True, "probe": True},
    "nosandbox-probe":   {"force_proxy": True, "probe": True},
    "sbx-fp-localhost":  {"force_proxy": True},
    "sbx-fp-none":       {"force_proxy": True},
    "sbx-fp-ro-401":     {"force_proxy": True},
    "sbx-fp-ro-pro":     {"force_proxy": True},
    "sbx-excluded":      {"excluded": ["brigade"]},
    "sbx-unixsock":      {"unix_sockets": True},
}

ARMS = {
    # label:            (sandbox_enabled, allowedDomains, token, readonly_home)
    "sbx-forceproxy":    (True,  ["127.0.0.1", "localhost"], "valid", False),
    "sbx-fp-ip":         (True,  ["127.0.0.1"], "valid", False),
    # Same configuration as sbx-fp-ip -- the arm that SUCCEEDED -- but the one
    # command carries a non-allowlisted destination and a filesystem write, so
    # enforcement is demonstrated inside the successful run itself.
    "sbx-fp-probe":      (True,  ["127.0.0.1"], "valid", False),
    # Control: identical command with the sandbox OFF. If the negatives fail
    # here too, they prove nothing about the sandbox.
    "nosandbox-probe":   (False, [], "valid", False),
    "sbx-fp-localhost":  (True,  ["localhost"], "valid", False),
    "sbx-fp-none":       (True,  [], "valid", False),
    "sbx-fp-ro-401":     (True,  ["127.0.0.1", "localhost"], "expired401", True),
    "sbx-fp-ro-pro":     (True,  ["127.0.0.1", "localhost"], "expiredpro", True),
    "sbx-excluded":      (True,  ["127.0.0.1", "localhost"], "valid", False),
    "sbx-unixsock":      (True,  ["127.0.0.1", "localhost"], "valid", False),
    "nosandbox":         (False, [], "valid", False),
    "sbx-nodomains":     (True,  [], "valid", False),
    "sbx-ip":            (True,  ["127.0.0.1"], "valid", False),
    "sbx-localhost":     (True,  ["localhost"], "valid", False),
    "sbx-both":          (True,  ["127.0.0.1", "localhost"], "valid", False),
    "sbx-ipport":        (True,  ["127.0.0.1:54321"], "valid", False),
    "sbx-star":          (True,  ["*"], "valid", False),
    "sbx-ro-expired401": (True,  ["127.0.0.1", "localhost"], "expired401", True),
    "sbx-ro-expiredpro": (True,  ["127.0.0.1", "localhost"], "expiredpro", True),
    "sbx-ro-valid":      (True,  ["127.0.0.1", "localhost"], "valid", True),
}


def one(arm, idx, args):
    enabled, domains, token, ro = ARMS[arm]
    results = os.path.join(HERE, "results", "c", "%s-run%d" % (arm, idx))
    shutil.rmtree(results, ignore_errors=True)
    os.makedirs(results)
    state = os.path.join(results, "state")
    os.makedirs(state)
    proj = os.path.join(HERE, "proj", "c-%s-%d" % (arm, idx))
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)

    home = os.path.join(HOMEROOT, "%s-%d" % (arm, idx))
    if os.path.isdir(home):
        chmod_tree(home, 0o700, 0o600)
        shutil.rmtree(home)
    os.makedirs(home, mode=0o700)
    pdir, prof = bootstrap(home)
    if token == "expired401":
        make_expired_401(pdir)
    elif token == "expiredpro":
        use_expired_proactive(pdir)
    pre_session = open(os.path.join(pdir, "session.json")).read()
    if ro:
        # Two independent read-only guarantees: the sandbox's own filesystem
        # policy (this HOME is outside the project dir and outside TMPDIR), and
        # POSIX mode bits, so the arm still means something if the first is
        # weaker than expected.
        chmod_tree(home, 0o500, 0o400)

    extra = EXTRA.get(arm, {})
    settings = {"permissions": {"allow": ["Bash(brigade:*)"], "deny": [], "ask": []}}
    if enabled:
        net = {"allowedDomains": domains}
        if extra.get("unix_sockets"):
            net["allowAllUnixSockets"] = True
        settings["sandbox"] = {"enabled": True, "network": net}
        if extra.get("excluded"):
            settings["sandbox"]["excludedCommands"] = extra["excluded"]
    prompt = PROMPT
    if extra.get("probe"):
        prompt = PROBE_PROMPT
    elif extra.get("force_proxy"):
        prompt = PROMPT.replace("`brigade sessions --json`", "`brigade sessions --json --force-proxy`") \
                       .replace("`brigade send peer hello-from-sandbox`",
                                "`brigade send --force-proxy peer hello-from-sandbox`")
    sp = os.path.join(results, "settings.json")
    json.dump(settings, open(sp, "w"), indent=2)

    # HOME is NOT overridden: on macOS the login keychain -- where Claude Code's
    # own credentials live -- is resolved through $HOME, and a fake HOME makes
    # the nested session report "Not logged in" before it can measure anything.
    # The profile is pointed elsewhere with BRIGADE_CONFIG_DIR instead, at a path
    # that is outside the project directory and outside TMPDIR, i.e. exactly
    # where the Bash sandbox denies writes -- the same place ~/.config/brigade
    # would sit in production.
    env, stripped = nested_env({"BRIGADE_E08_STATE": state,
                                "BRIGADE_CONFIG_DIR": pdir})
    settings.setdefault("env", {})["BRIGADE_CONFIG_DIR"] = pdir
    sp = os.path.join(results, "settings.json")
    json.dump(settings, open(sp, "w"), indent=2)
    cmd = ["claude", "-p", prompt, "--plugin-dir", PLUGIN, "--settings", sp,
           "--permission-mode", "default", "--output-format", "stream-json", "--verbose"]
    t0 = time.time()
    p = subprocess.run(cmd, cwd=proj, env=env, capture_output=True, text=True,
                       stdin=subprocess.DEVNULL, timeout=args.hard_timeout)
    wall = time.time() - t0
    open(os.path.join(results, "stream.jsonl"), "w").write(p.stdout)
    open(os.path.join(results, "claude.stderr.txt"), "w").write(p.stderr)

    events = []
    for line in p.stdout.splitlines():
        line = line.strip()
        if line:
            try:
                events.append(json.loads(line))
            except Exception:
                pass

    calls, results_by_id, final = [], {}, None
    for e in events:
        if e.get("type") == "assistant":
            for b in (e.get("message") or {}).get("content") or []:
                if b.get("type") == "tool_use" and b.get("name") == "Bash":
                    calls.append({"id": b.get("id"), "command": (b.get("input") or {}).get("command")})
        if e.get("type") == "user":
            for b in (e.get("message") or {}).get("content") or []:
                if b.get("type") == "tool_result":
                    c = b.get("content")
                    if isinstance(c, list):
                        c = "".join(x.get("text", "") for x in c if isinstance(x, dict))
                    results_by_id[b.get("tool_use_id")] = {"is_error": b.get("is_error"),
                                                           "text": c or ""}
        if e.get("type") == "result":
            final = e

    obs = []
    for c in calls:
        r = results_by_id.get(c["id"], {})
        text = r.get("text", "")
        diag = None
        s = text.find("{")
        if s >= 0:
            for end in range(len(text), s, -1):
                try:
                    diag = json.loads(text[s:end])
                    break
                except Exception:
                    continue
        obs.append({
            "command": c["command"],
            "is_error": r.get("is_error"),
            "sandbox_violations": ("<sandbox_violations>" in text),
            "violations_text": (text.split("<sandbox_violations>")[1].split("</sandbox_violations>")[0]
                                if "<sandbox_violations>" in text else None),
            "result_text_head": text[:600],
            "diag": diag,
        })

    post_session = ""
    try:
        post_session = open(os.path.join(pdir, "session.json")).read()
    except OSError as e:
        post_session = "<unreadable: %s>" % e
    if ro:
        chmod_tree(home, 0o700, 0o600)

    def pick(cmdsub):
        for o in obs:
            if o["command"] and cmdsub in o["command"]:
                return o
        return None

    sess, send = pick("sessions"), pick("send")

    def verdict(o):
        if not o:
            return "not attempted"
        d = o.get("diag") or {}
        if d.get("ok"):
            v = "OK"
            if d.get("refreshed"):
                v += " (refreshed, persisted=%s)" % d.get("persisted")
            return v
        errs = [s.get("err") for s in (d.get("steps") or []) if s.get("err")]
        if errs:
            return "TRANSPORT ERROR: " + errs[0]
        if o["sandbox_violations"]:
            return "SANDBOX VIOLATION"
        return "FAILED: " + (d.get("error") or o["result_text_head"][:150])

    rec = {
        "arm": arm, "run": idx, "sandbox_enabled": enabled,
        "allowedDomains": domains, "token": token, "readonly_home": ro,
        "home": home, "profile_dir": pdir, "profile": prof,
        "settings": settings,
        "sessions_verdict": verdict(sess), "send_verdict": verdict(send),
        "observations": obs,
        "attempts": [json.loads(l) for l in open(os.path.join(state, "attempts.ndjson"))
                     if l.strip()] if os.path.exists(os.path.join(state, "attempts.ndjson")) else [],
        "session_file_changed": pre_session != post_session,
        "permission_denials": (final or {}).get("permission_denials"),
        "final_result": ((final or {}).get("result") or "")[:400],
        "claude_wall_s": round(wall, 3), "claude_exit": p.returncode,
        "stripped": stripped, "results": results,
    }
    json.dump(rec, open(os.path.join(results, "summary.json"), "w"), indent=2)
    return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--reps", type=int, default=1)
    ap.add_argument("--arms", default=",".join(ARMS))
    ap.add_argument("--hard-timeout", type=float, default=240)
    a = ap.parse_args()
    os.makedirs(os.path.join(HERE, "results", "c"), exist_ok=True)
    rows = []
    for arm in a.arms.split(","):
        for i in range(1, a.reps + 1):
            try:
                r = one(arm, i, a)
            except Exception as e:
                print("[%s run%d] HARNESS ERROR %s: %s" % (arm, i, type(e).__name__, e))
                sys.stdout.flush()
                continue
            rows.append(r)
            print("[%s run%d] sessions=%s | send=%s | violations=%s | file_changed=%s" % (
                arm, i, r["sessions_verdict"], r["send_verdict"],
                any(o["sandbox_violations"] for o in r["observations"]),
                r["session_file_changed"]))
            for o in r["observations"]:
                if o["violations_text"]:
                    print("      VIOL: %s" % o["violations_text"].strip().replace("\n", " ")[:300])
            sys.stdout.flush()
    json.dump(rows, open(os.path.join(HERE, "results", "c", "all.json"), "w"), indent=2)


if __name__ == "__main__":
    main()
