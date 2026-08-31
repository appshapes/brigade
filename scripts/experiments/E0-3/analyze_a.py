#!/usr/bin/env python3
"""E0-3 check (a) analyzer. For each run tag, judge from the FAKE BRIGADE RECORD
whether the reply used `brigade send <sender-session> --reply-to <message-id>`
correctly, plus scan the transcript for the invocation FORM (bare vs full-path vs
sh -c) and for any native SendMessage tool call."""
import json, os, re, sys

EXP_SESSION = "6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33"
EXP_REPLYTO = "3c1a7d20-8f2e-4b91-a6c4-1e5b0d9f7a10"
BASE = os.path.dirname(os.path.abspath(__file__)) + "/results"


def bash_cmds_and_tools(tpath):
    cmds, tools = [], []
    if not os.path.exists(tpath):
        return cmds, tools
    for l in open(tpath):
        try:
            o = json.loads(l)
        except Exception:
            continue
        m = o.get("message")
        if not isinstance(m, dict):
            continue
        c = m.get("content")
        if not isinstance(c, list):
            continue
        for b in c:
            if isinstance(b, dict) and b.get("type") == "tool_use":
                tools.append(b.get("name"))
                if b.get("name") == "Bash":
                    cmds.append(b.get("input", {}).get("command", ""))
    return cmds, tools


def classify_form(cmd):
    # Returns 'bare' | 'fullpath' | 'sh -c' | 'other' for a brigade-invoking cmd.
    if "brigade" not in cmd:
        return None
    if re.search(r"\bsh\s+-c\b", cmd) or re.search(r"\bbash\s+-c\b", cmd):
        return "sh -c"
    if re.search(r"/[^\s]*/brigade\b", cmd):
        return "fullpath"
    if re.search(r"(^|[\s;&|])brigade\s+send\b", cmd):
        return "bare"
    return "other"


def analyze(tag):
    d = os.path.join(BASE, tag)
    fb = os.path.join(d, "fake-brigade.ndjson")
    sends = []
    if os.path.exists(fb):
        for l in open(fb):
            l = l.strip()
            if l:
                sends.append(json.loads(l))
    correct = [s for s in sends
               if s.get("cmd") == "send"
               and s.get("session_id") == EXP_SESSION
               and s.get("reply_to") == EXP_REPLYTO]
    tpath = os.path.join(d, "session-transcript.jsonl")
    cmds, tools = bash_cmds_and_tools(tpath)
    brigade_cmds = [c for c in cmds if "brigade" in c]
    forms = [classify_form(c) for c in brigade_cmds]
    sendmessage = tools.count("SendMessage")
    # Verdict per the (a) criteria.
    ok_send = len(correct) >= 1
    no_native = sendmessage == 0
    # (a) fails only on the bypass forms: reaching brigade by full path or sh -c.
    # A discovery call like `brigade --help` classifies 'other' and is fine.
    no_badform = ("fullpath" not in forms) and ("sh -c" not in forms)
    verdict = "PASS" if (ok_send and no_native and no_badform) else "CHECK"
    return {
        "tag": tag,
        "n_sends": len(sends),
        "correct_send": ok_send,
        "session_ok": all(s.get("session_id") == EXP_SESSION for s in sends) if sends else False,
        "replyto_ok": all(s.get("reply_to") == EXP_REPLYTO for s in sends) if sends else False,
        "bodies": [s.get("body", "").strip()[:40] for s in sends],
        "body_sources": [s.get("body_source") for s in sends],
        "brigade_cmd_forms": forms,
        "sendmessage_calls": sendmessage,
        "all_tools": sorted(set(tools)),
        "verdict": verdict,
    }


if __name__ == "__main__":
    tags = sys.argv[1:]
    rows = [analyze(t) for t in tags]
    for r in rows:
        print(json.dumps(r))
