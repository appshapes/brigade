#!/usr/bin/env python3
"""E0-8 check (h): a ~12 KB QUOTED-heredoc `brigade send`, with `Bash(brigade:*)`
allowed, in interactive Manual mode and in `-p`.

Why it might not just work: the permissions documentation caps parseable commands
at 10,000 characters, and no Brigade digest has ever carried more than a 60-byte
body, so this path has never been exercised. If the parser gives up past the cap,
an allowed prefix rule stops matching and a command that should be silent starts
prompting -- which is exactly what the skill's `--body-file` threshold exists to
avoid.

MEASUREMENT. The ground truth for "how long was the command the permission system
actually judged" is the PreToolUse hook's record of `tool_input.command`, not
anything the model says. The ground truth for "did it run" is the fake `brigade`
appending an exec row. So per size:

    attempt row + exec row            -> SILENT SUCCESS (rule matched)
    attempt row, no exec row          -> BLOCKED: a prompt interactively,
                                         a denial in -p (which cannot answer one)
    no attempt row                    -> the model never tried; run is void

The body is delivered to the model as a file it must reproduce verbatim, because
a model asked to emit 150 identical lines from a rule is not reliable at exact
length -- and length is the whole measurement. Whatever it actually emits, the
hook records the true length, so every data point is honest about its own size.
"""
import argparse
import json
import os
import re
import secrets
import shutil
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402

HERE = common.HERE
FILL = "brigade-e08-heredoc-filler-0123456789-abcdefghijklmnopqrstuvwxyz-0123456"  # 72
assert len(FILL) == 72


def build_command(target, tagword):
    """A quoted heredoc whose TOTAL length is exactly `target` characters."""
    header = "brigade send peer-h --reply-to %s <<'BRIGADE_EOF'\n" % tagword
    trailer = "BRIGADE_EOF"
    room = target - len(header) - len(trailer)
    if room < 2:
        raise ValueError("target too small")
    body = []
    n = 0
    while room - n > 80:
        body.append(FILL + "-%05d" % len(body))  # 72 + 6 = 78 chars + newline
        n += 79
    pad = room - n - 1
    body.append("Z" * pad)
    cmd = header + "\n".join(body) + "\n" + trailer
    assert len(cmd) == target, (len(cmd), target)
    return cmd


PROMPT = (
    "The file `{path}` contains ONE multi-line shell command, {n} characters long. "
    "Read that file, then make your VERY NEXT tool call a single Bash call whose "
    "`command` is that file's ENTIRE contents, character for character. "
    "Do not modify it, do not shorten it, do not summarise it, do not add "
    "redirections or pipes, and do NOT run it via `bash {path}`, `source`, `cat`, "
    "`awk`, `wc` or any other helper -- the command text itself must be the Bash "
    "tool's `command`, and no other Bash call may come first. Print the output. "
    "Then say H-DONE and stop."
)

BODY = r"""
mark spawn size @SIZE@
spawn -noecho env @UNSETS@ claude --plugin-dir "@PLUGIN@" --settings "@SETTINGS@" --permission-mode default

onboard
if {![canary @TAIL@ 3]} { mark canary_failed ; shutdown ; exit 0 }

mark h_prompt_sent
submit "@PROMPT@" "heredoc prompt"
set w [waitcount "@EXEC@" 1 @T@]
if {$w < 0} {
    mark blocked attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
} else {
    mark executed waited_ms $w attempts [ndcount "@ATT@"]
}
nap 5
mark final attempts [ndcount "@ATT@"] execs [ndcount "@EXEC@"]
shutdown
"""

DIALOG = re.compile(r"(Do you want to proceed|don.t ask again|This command requires)", re.I)


def prepare(size, kind, idx):
    tagword = "h-%s-%d" % (kind, size)
    results = common.new_results("%s/%s-%d" % (idx, kind, size))
    state = os.path.join(results, "state")
    shutil.rmtree(state, ignore_errors=True)
    os.makedirs(state)
    proj = os.path.join(HERE, "proj", "h-%s-%d" % (kind, size))
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)
    cmd = build_command(size, tagword)
    cmdfile = os.path.join(proj, "cmd.txt")
    with open(cmdfile, "w") as f:
        f.write(cmd)
    with open(os.path.join(results, "cmd.txt"), "w") as f:
        f.write(cmd)
    return results, state, proj, cmdfile, cmd


def classify(atts, execs, size):
    brig = [a for a in atts if a.get("tool") == "Bash"
            and (a.get("cmd_head") or "").startswith("brigade")]
    if not brig:
        return "void-no-attempt", brig
    if execs:
        return "silent-success", brig
    return "blocked", brig


def run_headless(size, idx, args):
    results, state, proj, cmdfile, cmd = prepare(size, "p", idx)
    env, _ = common.nested_env({"BRIGADE_E08_STATE": state})
    prompt = PROMPT.format(path=cmdfile, n=size)
    argv = ["claude", "-p", prompt,
            "--plugin-dir", common.PLUGIN,
            "--settings", os.path.join(HERE, "settings", "allow.json"),
            "--permission-mode", "default",
            "--output-format", "stream-json", "--verbose"]
    try:
        r = subprocess.run(argv, cwd=proj, env=env, capture_output=True,
                           text=True, timeout=args.p_timeout)
        out, rc = r.stdout, r.returncode
    except subprocess.TimeoutExpired as e:
        out, rc = (e.stdout or b"").decode("utf-8", "replace"), "timeout"
    with open(os.path.join(results, "stream.jsonl"), "w") as f:
        f.write(out)

    tool_uses, tool_results, final = [], [], ""
    for line in out.splitlines():
        try:
            j = json.loads(line)
        except Exception:
            continue
        if j.get("type") == "assistant":
            for c in j["message"].get("content", []):
                if c.get("type") == "tool_use":
                    ci = c.get("input", {}).get("command")
                    tool_uses.append({"name": c["name"],
                                      "cmd_len": len(ci) if isinstance(ci, str) else None,
                                      "cmd_head": ci[:90] if isinstance(ci, str) else None})
        if j.get("type") == "user":
            for c in (j.get("message", {}).get("content") or []):
                if isinstance(c, dict) and c.get("type") == "tool_result":
                    t = c.get("content")
                    if isinstance(t, list):
                        t = " ".join(x.get("text", "") for x in t if isinstance(x, dict))
                    tool_results.append(str(t)[:300])
        if j.get("type") == "result":
            final = str(j.get("result", ""))[:400]

    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    outcome, brig = classify(atts, execs, size)
    v = {"mode": "-p", "target_size": size, "exit": rc, "outcome": outcome,
         "actual_cmd_len": [a.get("cmd_len") for a in brig],
         "attempt_rows": len(atts), "exec_rows": len(execs),
         "exec_body_len": [e.get("body_len") for e in execs],
         "exec_body_sha12": [e.get("body_sha12") for e in execs],
         "tool_uses": tool_uses, "tool_results": tool_results, "final": final,
         "hook_rows": common.read_ndjson(os.path.join(state, "hook-env.log"))}
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def run_interactive(size, idx, args):
    results, state, proj, cmdfile, cmd = prepare(size, "tty", idx)
    env, stripped = common.nested_env({"BRIGADE_E08_STATE": state})
    prompt = PROMPT.format(path=cmdfile, n=size)
    body = (BODY
            .replace("@SIZE@", str(size))
            .replace("@UNSETS@", " ".join("-u " + v for v in stripped))
            .replace("@PLUGIN@", common.PLUGIN)
            .replace("@SETTINGS@", os.path.join(HERE, "settings", "allow.json"))
            .replace("@TAIL@", secrets.token_hex(2).upper())
            .replace("@PROMPT@", prompt)
            .replace("@EXEC@", os.path.join(state, "exec.ndjson"))
            .replace("@ATT@", os.path.join(state, "attempts.ndjson"))
            .replace("@T@", str(args.t)))
    exp = common.write_expect(results, body)
    how = common.run_expect(exp, proj, env, args.hard_timeout, results)

    atts = common.read_ndjson(os.path.join(state, "attempts.ndjson"))
    execs = common.read_ndjson(os.path.join(state, "exec.ndjson"))
    marks = common.read_ndjson(os.path.join(results, "marks.ndjson"))
    by = {m["event"] for m in marks}
    log = ""
    lp = os.path.join(results, "session.log")
    if os.path.exists(lp):
        log = open(lp, errors="replace").read()
    outcome, brig = classify(atts, execs, size)
    if outcome == "blocked":
        outcome = "prompted" if DIALOG.search(log) else "blocked-no-dialog-text"
    v = {"mode": "interactive", "target_size": size, "outcome": outcome,
         "expect_process": how, "canary_ok": "canary_ok" in by,
         "actual_cmd_len": [a.get("cmd_len") for a in brig],
         "attempt_rows": len(atts), "exec_rows": len(execs),
         "attempt_tools": [(a.get("tool"), (a.get("cmd_head") or "")[:50]) for a in atts],
         "exec_body_len": [e.get("body_len") for e in execs],
         "exec_body_sha12": [e.get("body_sha12") for e in execs],
         "dialog_text_seen": bool(DIALOG.search(log)),
         "hook_rows": common.read_ndjson(os.path.join(state, "hook-env.log"))}
    with open(os.path.join(results, "verdict.json"), "w") as f:
        json.dump(v, f, indent=2)
    return v


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--sizes", default="600,9800,9990,10010,10200,12000")
    ap.add_argument("--mode", default="p", choices=["p", "tty", "both"])
    ap.add_argument("--t", type=int, default=150)
    ap.add_argument("--p-timeout", type=int, default=420)
    ap.add_argument("--hard-timeout", type=int, default=900)
    ap.add_argument("--tag", default=None)
    args = ap.parse_args()

    tag_root = args.tag or common.stamp("h")
    root = common.new_results(tag_root)
    guard = common.Guard(root)
    outer = common.outer_identity()
    sizes = [int(s) for s in args.sizes.split(",")]
    modes = ["p", "tty"] if args.mode == "both" else [args.mode]

    rows = []
    for m in modes:
        for s in sizes:
            v = (run_headless if m == "p" else run_interactive)(s, tag_root, args)
            rows.append(v)
            print(json.dumps({k: v.get(k) for k in
                              ("mode", "target_size", "outcome", "actual_cmd_len",
                               "attempt_rows", "exec_rows", "exec_body_len",
                               "canary_ok", "tool_results")}))
            sys.stdout.flush()

    hook_rows = [h for r in rows for h in r.get("hook_rows", [])]
    summary = {"tag": tag_root, "rows": rows,
               "isolation": common.isolation_verdict(hook_rows, outer),
               "config_protection": guard.verify()}
    with open(os.path.join(root, "summary.json"), "w") as f:
        json.dump(summary, f, indent=2)
    print(json.dumps({"isolation_ok": summary["isolation"]["ok"],
                      "config_protection": summary["config_protection"]}, indent=2))
    print("results: " + root)


if __name__ == "__main__":
    main()
