#!/usr/bin/env python3
"""E0-8 second sitting: roll every per-run summary up into one report.

Reads the per-run summary.json files rather than the per-invocation all.json,
because run_c.py was invoked more than once and each invocation rewrote its own
all.json; the per-run files are the durable record.
"""
import glob
import json
import os
import statistics as st

HERE = os.path.dirname(os.path.realpath(__file__))


def load(pattern):
    out = []
    for p in sorted(glob.glob(os.path.join(HERE, "results", pattern))):
        try:
            out.append(json.load(open(p)))
        except Exception:
            pass
    return out


def sec_a():
    print("=" * 78)
    print("(a) FIRST-USE DOWNLOAD TIMING")
    rows = load("a/*/summary.json")
    by = {}
    for r in rows:
        by.setdefault(r["label"], []).append(r)
    for lab in ("unthrottled", "1MBps", "250kBps"):
        rs = by.get(lab, [])
        if not rs:
            continue
        f = [r["fetch_s"] for r in rs]
        h = [r["hook_total_s"] for r in rs]
        w = [r["claude_wall_s"] for r in rs]
        eff = [r["curl"]["speed"] for r in rs]
        print("  %-12s n=%d  fetch %.2f-%.2f  hook_total %.2f-%.2f (median %.2f)  "
              "claude -p wall %.2f-%.2f  effective %.0f B/s  err=%s" %
              (lab, len(rs), min(f), max(f), min(h), max(h), st.median(h),
               min(w), max(w), st.median(eff), set(r["bootstrap_error"] for r in rs)))
    bg = load("abg/*/summary.json")
    for r in bg:
        rows2 = {x["event"]: x for x in r["bootstrap_rows"]}
        warm = rows2.get("background_fetch_done", {})
        print("  BACKGROUND  %-9s hook_total %.2f s  claude -p wall %.2f s  "
              "cache warm after %.2f s  installed=%s" %
              (r["label"], r["hook_total_s"], r["claude_wall_s"],
               warm.get("fetch_s", -1), bool(r["installed"])))


def sec_c():
    print("=" * 78)
    print("(c) SANDBOX")
    for p in sorted(glob.glob(os.path.join(HERE, "results", "c", "*", "summary.json"))):
        r = json.load(open(p))
        d0 = (r["observations"][0]["diag"] if r["observations"] else None) or {}
        print("  %-20s run%d sandbox=%-5s domains=%-28s ro=%-5s token=%-10s" %
              (r["arm"], r["run"], r["sandbox_enabled"], json.dumps(r["allowedDomains"]),
               r["readonly_home"], r["token"]))
        print("      sessions: %s" % r["sessions_verdict"])
        print("      send    : %s" % r["send_verdict"])
        print("      home_writable_from_sandbox=%s  session_file_changed=%s  denials=%s" %
              (d0.get("home_writable"), r["session_file_changed"], r["permission_denials"]))


def sec_d():
    print("=" * 78)
    print("(d) SessionStart CONTEXT LINE vs HOOK STDOUT")
    for r in load("d/*/summary.json"):
        ch = [h for h in r["transcript_hits"] if h["json_path"].endswith(".content")]
        print("  %-8s run%d  exact=%-5s delta=%-34s stdout_bytes=%d content_len=%s" %
              (r["variant"], r["run"], r["injected_content_equals_hook_stdout"],
               r["delta"], r["hook_stdout_bytes"], ch[0]["len"] if ch else None))
        if ch and not r["injected_content_equals_hook_stdout"]:
            print("      hook stdout : %s" % r["hook_stdout_repr"])
            print("      injected    : %s" % ch[0]["repr"])


def sec_e():
    print("=" * 78)
    print("(e) PATH SHADOWING")
    for r in load("e/*/summary.json"):
        print("  %-22s run%d route=%-24s hook_shadowed=%-5s warning_in_context=%-5s who_ran=%s" %
              (r["arm"], r["run"], r["route"], r["hook_saw_shadow"],
               r["warning_in_context"], r["who_ran"]))


def isolation():
    print("=" * 78)
    print("ISOLATION (nested vs outer)")
    outer = {"pid": os.environ.get("CLAUDE_PID"),
             "session": os.environ.get("CLAUDE_CODE_SESSION_ID"),
             "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET")}
    print("  outer:", json.dumps(outer))
    seen = []
    for r in load("a/*/summary.json") + load("abg/*/summary.json"):
        b = r["bootstrap_rows"][0]
        seen.append((b["claude_pid"], b["session"], b["socket"]))
    for r in load("e/*/summary.json"):
        for h in r["hook_rows"]:
            seen.append((h["claude_pid"], h["session"], h["socket"]))
    bad = [s for s in seen if s[0] == outer["pid"] or s[1] == outer["session"]
           or s[2] == outer["socket"]]
    print("  nested identities observed: %d, distinct pids: %d, colliding with outer: %d"
          % (len(seen), len(set(s[0] for s in seen)), len(bad)))
    print("  sample:", seen[:3])
    print("  isolation ok:", not bad and bool(seen))


if __name__ == "__main__":
    sec_a()
    sec_c()
    sec_d()
    sec_e()
    isolation()
