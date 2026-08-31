#!/usr/bin/env python3
"""E0-7 item 4, second pass: reach the FIRST screen a fresh CLAUDE_CONFIG_DIR
actually blocks on, and record it verbatim.

Pass 1 established the headless answer ("Not logged in - Please run /login") and
showed the interactive session opening onboarding at the theme picker. This pass
advances past the theme picker ONLY -- one Enter on the already-highlighted theme
entry, which is not an authentication action -- and then records, without typing
anything further, the screen that follows. No credential is ever entered, no
browser flow is ever confirmed, and the whole run is bounded by expect timeouts.

Every wait is an `expect`; there is no `sleep` anywhere.
"""
import json
import os
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))

DRIVER = r"""
set timeout 30
log_file -a "{log}"
proc mark {{what}} {{
    set f [open "{marks}" a]
    puts $f "[clock milliseconds] $what"
    close $f
    send_user "\n\[\[E07 $what\]\]\n"
}}
proc xsend {{what note}} {{
    set f [open "{sendlog}" a]
    puts $f "[clock milliseconds] $note"
    close $f
    send -- $what
}}
# A settle built from `expect`, not `sleep`: wait for a pattern that cannot
# occur, so the wait ends on expect's own timeout.
proc settle {{secs}} {{
    set old $::timeout
    set ::timeout $secs
    expect {{ -re {{\x00NEVERMATCHES\x00}} {{}} timeout {{}} }}
    set ::timeout $old
}}

mark spawn
spawn -noecho env {unsets} CLAUDE_CONFIG_DIR={cfg} claude

# Single-word regexes only (box drawing interleaves cursor escapes between words).
expect {{
  -re {{Welcome}} {{ mark saw_welcome }}
  eof {{ mark eof_early; exit 0 }}
  timeout {{ mark timeout_before_welcome }}
}}
expect {{
  -re {{theme}} {{ mark saw_theme_picker }}
  -re {{login}} {{ mark saw_login_first }}
  eof {{ mark eof_after_welcome; exit 0 }}
  timeout {{ mark timeout_before_theme }}
}}

# Let the picker finish painting before touching it, then move the selection
# once (down, then back up) so the keypress is unambiguously registered, and
# confirm. This advances onboarding; it is not a login action and enters no
# credential.
settle 3
xsend "\033\[B" "theme picker: move selection down"
settle 1
xsend "\033\[A" "theme picker: move selection back up"
settle 1
xsend "\r" "theme picker: confirm highlighted entry"
mark theme_confirmed

# Whatever the next blocking screen is, record which words appear on it.
expect {{
  -re {{login}} {{ mark next_login }}
  -re {{Login}} {{ mark next_Login }}
  -re {{account}} {{ mark next_account }}
  -re {{subscription}} {{ mark next_subscription }}
  -re {{browser}} {{ mark next_browser }}
  -re {{Console}} {{ mark next_Console }}
  -re {{shortcuts}} {{ mark next_ready_prompt }}
  eof {{ mark next_eof }}
  timeout {{ mark next_timeout }}
}}
expect {{
  -re {{browser}} {{ mark then_browser }}
  -re {{Console}} {{ mark then_Console }}
  -re {{subscription}} {{ mark then_subscription }}
  -re {{shortcuts}} {{ mark then_ready_prompt }}
  eof {{ mark then_eof }}
  timeout {{ mark then_timeout }}
}}

mark closing
catch {{ exec kill -TERM [exp_pid] }}
expect {{ eof {{ mark eof }} timeout {{ mark eof_timeout }} }}
"""


def main():
    cfg = sys.argv[1]
    proj = sys.argv[2]
    tag = "fresh2-" + time.strftime("%Y%m%d-%H%M%S")
    results = os.path.join(HERE, "results", tag)
    os.makedirs(results, exist_ok=True)

    leaky = sorted(k for k in os.environ
                   if k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"))
    env = {k: v for k, v in os.environ.items() if k not in leaky}
    env["CLAUDE_CONFIG_DIR"] = cfg

    log = os.path.join(results, "interactive.log")
    marks = os.path.join(results, "marks.txt")
    sendlog = os.path.join(results, "pty-writes.log")
    src = DRIVER.format(log=log, marks=marks, sendlog=sendlog, cfg=cfg,
                        unsets=" ".join("-u " + k for k in leaky))
    exp_path = os.path.join(results, "drive.exp")
    with open(exp_path, "w") as f:
        f.write(src)

    t0 = time.time()
    try:
        r = subprocess.run(["expect", "-f", exp_path], cwd=proj, env=env,
                           capture_output=True, text=True, timeout=200)
        raw, rc = r.stdout + r.stderr, r.returncode
    except subprocess.TimeoutExpired as e:
        raw, rc = ((e.stdout or b"").decode(errors="replace")
                   if isinstance(e.stdout, bytes) else (e.stdout or "")), None
    with open(os.path.join(results, "interactive.raw"), "w") as f:
        f.write(raw)

    # Strip escapes so the recorded screen text is readable evidence.
    import re
    plain = re.sub(r"\x1b\[[0-9;?]*[a-zA-Z]", "", raw)
    plain = re.sub(r"\x1b[()][B0]|\x1b[78]|\x1b\][^\x07]*\x07|[\x00-\x08\x0b-\x1f]", "", plain)
    with open(os.path.join(results, "screen.txt"), "w") as f:
        f.write(plain)

    rec = {
        "tag": tag, "config_dir": cfg, "project": proj, "exit": rc,
        "seconds": round(time.time() - t0, 1),
        "marks": [l.strip() for l in open(marks)] if os.path.exists(marks) else [],
        "pty_writes": [l.strip() for l in open(sendlog)] if os.path.exists(sendlog) else [],
        "screen_plain_tail": plain[-3500:],
    }
    fc = os.path.join(cfg, ".claude.json")
    if os.path.exists(fc):
        o = json.load(open(fc))
        rec["claude_json"] = {
            "hasCompletedOnboarding": o.get("hasCompletedOnboarding"),
            "oauthAccount_present": bool(o.get("oauthAccount")),
            "theme": o.get("theme"),
            "fullscreenAutoDisabled": o.get("fullscreenAutoDisabled", "<absent>"),
        }
    with open(os.path.join(results, "fresh2.json"), "w") as f:
        json.dump(rec, f, indent=2)
    print(json.dumps({k: v for k, v in rec.items() if k != "screen_plain_tail"}, indent=2))
    print("--- screen (escapes stripped, tail) ---")
    print(plain[-3000:])
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
