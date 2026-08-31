#!/usr/bin/env python3
"""E0-7 item 4: the FRESH CLAUDE_CONFIG_DIR probe (recording only).

Points CLAUDE_CONFIG_DIR at a brand-new empty temp directory and starts `claude`
twice -- once headless (`-p`) and once interactive on a pty via expect -- to
record whether the login is inherited from this user or demanded anew.

NOTHING here attempts to log in: no credential is typed, no browser is opened,
no keychain item is written by this script. Both runs are hard-bounded by a
timeout so a login prompt cannot hang the harness. Every wait inside the expect
driver is built from `expect`, never from `sleep`.
"""
import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
SCRATCH = os.environ.get(
    "E07_SCRATCH",
    "/private/tmp/claude-501/-Users-rjae-Development-appshapes-brigade/"
    "7eff29f9-c369-45e4-aebd-7c542dda31a9/scratchpad")

EXPECT_DRIVER = r"""
set timeout {t}
log_file -a "{log}"
proc mark {{what}} {{
    set f [open "{marks}" a]
    puts $f "[clock milliseconds] $what"
    close $f
    send_user "\n\[\[E07 $what\]\]\n"
}}
mark spawn
spawn -noecho env {unsets} CLAUDE_CONFIG_DIR={cfg} claude
# Single-word regexes only: the dialogs are drawn as boxes and cursor-position
# escapes fall between words, so a multi-word pattern never matches.
# NOTHING is ever sent to this pty: this run only observes.
expect {{
  -re {{login}}       {{ mark saw_login }}
  -re {{Login}}       {{ mark saw_Login }}
  -re {{authenticate}}{{ mark saw_authenticate }}
  -re {{Sign}}        {{ mark saw_Sign }}
  -re {{browser}}     {{ mark saw_browser }}
  -re {{subscription}}{{ mark saw_subscription }}
  -re {{theme}}       {{ mark saw_theme }}
  -re {{trust}}       {{ mark saw_trust }}
  -re {{shortcuts}}   {{ mark saw_ready_prompt }}
  -re {{Welcome}}     {{ mark saw_welcome }}
  eof                 {{ mark eof_early }}
  timeout             {{ mark timeout_no_match }}
}}
# Second observation window: whatever came first, record what follows it.
expect {{
  -re {{login}}       {{ mark then_login }}
  -re {{browser}}     {{ mark then_browser }}
  -re {{subscription}}{{ mark then_subscription }}
  -re {{theme}}       {{ mark then_theme }}
  -re {{shortcuts}}   {{ mark then_ready_prompt }}
  eof                 {{ mark then_eof }}
  timeout             {{ mark then_timeout }}
}}
mark closing
catch {{ exec kill -TERM [exp_pid] }}
expect {{ eof {{ mark eof }} timeout {{ mark eof_timeout }} }}
"""


def nested_env(cfg):
    keep = {"CLAUDE_CONFIG_DIR"}
    out = {}
    for k, v in os.environ.items():
        if k in keep or k.startswith("CLAUDE") or k == "CLAUDECODE" or k == "AI_AGENT":
            continue
        out[k] = v
    out["CLAUDE_CONFIG_DIR"] = cfg
    return out


def keychain_service(cfgdir):
    return "Claude Code-credentials-" + hashlib.sha256(cfgdir.encode()).hexdigest()[:8]


def keychain_present(service):
    p = subprocess.run(["security", "find-generic-password", "-s", service],
                       capture_output=True, text=True)
    return p.returncode == 0, (p.stdout + p.stderr).strip()[:400]


def tree(d):
    out = []
    for root, dirs, files in os.walk(d):
        for f in dirs + files:
            out.append(os.path.relpath(os.path.join(root, f), d))
    return sorted(out)


def main():
    tag = "fresh-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results", tag)
    os.makedirs(results, exist_ok=True)

    real_cfg = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")
    fresh = os.path.join(SCRATCH, "fresh-config-" + str(int(time.time())))
    os.makedirs(fresh, mode=0o700)
    proj = os.path.join(SCRATCH, "fresh-project-" + str(int(time.time())))
    os.makedirs(proj, exist_ok=True)

    svc = keychain_service(fresh)
    real_svc = keychain_service(real_cfg)
    kc_before = keychain_present(svc)
    real_kc = keychain_present(real_svc)
    plain_kc = keychain_present("Claude Code-credentials")

    rec = {
        "tag": tag,
        "real_config_dir_observed": real_cfg,
        "fresh_config_dir": fresh,
        "fresh_dir_empty_before": tree(fresh),
        "keychain": {
            "naming_rule": "Claude Code-credentials-<sha256(CLAUDE_CONFIG_DIR)[:8]>, account = OS user",
            "service_for_fresh_dir": svc,
            "fresh_service_present_before": kc_before[0],
            "service_for_real_dir": real_svc,
            "real_service_present": real_kc[0],
            "unsuffixed_service_present": plain_kc[0],
        },
    }

    env = nested_env(fresh)

    # ---- run A: headless, hard timeout ----
    t0 = time.time()
    try:
        p = subprocess.run(["claude", "-p", "Reply with the two characters OK and nothing else."],
                           cwd=proj, env=env, capture_output=True, text=True, timeout=90)
        rec["headless"] = {"exit": p.returncode, "seconds": round(time.time() - t0, 1),
                           "stdout": p.stdout[:4000], "stderr": p.stderr[:4000],
                           "timed_out": False}
    except subprocess.TimeoutExpired as e:
        rec["headless"] = {"exit": None, "seconds": round(time.time() - t0, 1),
                           "stdout": (e.stdout or b"").decode(errors="replace")[:4000]
                           if isinstance(e.stdout, bytes) else (e.stdout or "")[:4000],
                           "stderr": (e.stderr or b"").decode(errors="replace")[:4000]
                           if isinstance(e.stderr, bytes) else (e.stderr or "")[:4000],
                           "timed_out": True}
    rec["fresh_dir_after_headless"] = tree(fresh)

    # ---- run B: interactive on a pty, observe only ----
    log = os.path.join(results, "interactive.log")
    marks = os.path.join(results, "marks.txt")
    exp = EXPECT_DRIVER.format(t=25, log=log, marks=marks, cfg=fresh,
                               unsets=" ".join("-u " + k for k in sorted(
                                   k for k in os.environ
                                   if k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"))))
    exp_path = os.path.join(results, "drive.exp")
    with open(exp_path, "w") as f:
        f.write(exp)
    t0 = time.time()
    try:
        r = subprocess.run(["expect", "-f", exp_path], cwd=proj, env=env,
                           capture_output=True, text=True, timeout=120)
        raw, rc = r.stdout + r.stderr, r.returncode
    except subprocess.TimeoutExpired as e:
        raw, rc = ((e.stdout or b"").decode(errors="replace")
                   if isinstance(e.stdout, bytes) else (e.stdout or "")), None
    with open(os.path.join(results, "interactive.raw"), "w") as f:
        f.write(raw)
    rec["interactive"] = {
        "exit": rc, "seconds": round(time.time() - t0, 1),
        "marks": [l.strip() for l in open(marks)] if os.path.exists(marks) else [],
        "screen_tail": raw[-4000:],
    }
    rec["fresh_dir_after_interactive"] = tree(fresh)
    kc_after = keychain_present(svc)
    rec["keychain"]["fresh_service_present_after"] = kc_after[0]
    rec["keychain"]["fresh_service_after_detail"] = kc_after[1]

    # anything the fresh dir wrote that names an account?
    fc = os.path.join(fresh, ".claude.json")
    if os.path.exists(fc):
        try:
            o = json.load(open(fc))
            oa = o.get("oauthAccount") or {}
            rec["fresh_claude_json"] = {
                "keys": sorted(o.keys())[:40],
                "hasCompletedOnboarding": o.get("hasCompletedOnboarding"),
                "oauthAccount_present": bool(oa),
                "oauthAccount_uuid_prefix": (oa.get("accountUuid") or "")[:8],
                "fullscreenAutoDisabled": o.get("fullscreenAutoDisabled", "<absent>"),
            }
        except Exception as e:
            rec["fresh_claude_json"] = {"error": str(e)}
    else:
        rec["fresh_claude_json"] = None

    with open(os.path.join(results, "fresh.json"), "w") as f:
        json.dump(rec, f, indent=2)
    print(json.dumps(rec, indent=2)[:12000])
    print("results: " + results)
    print("fresh config dir left in place for inspection: " + fresh)
    return 0


if __name__ == "__main__":
    sys.exit(main())
