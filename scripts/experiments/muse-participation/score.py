#!/usr/bin/env python3
"""Offline scorer for the Muse monitor arm (route B).

Reads one session.jsonl (Muse's own log) and the driver.log beside it and prints, per outside append:
  queued_at (inbox_item_queued), run_started_at (a run whose origin is inbox_delivery.run_origin), drained_at,
  assistant text containing the nonce, latency append->run start, and whether any keystroke the driver logged
  falls inside the window.  Also counts background-origin runs during the null control.  No model output is
  trusted beyond the nonce match in the model's own committed assistant text.
Usage: score.py <evidence-dir>
"""
import json, re, sys, os, datetime

def ts(r):
    t = r.get("recorded_at")
    return t / 1e6 if t else None

def parse_driver(path):
    rows = []
    for line in open(path):
        m = re.match(r'(\S+)Z (.*)', line.rstrip('\n'))
        if not m: continue
        t = datetime.datetime.fromisoformat(m.group(1)).replace(tzinfo=datetime.timezone.utc).timestamp()
        rows.append((t, m.group(2)))
    return rows

def main(ev):
    recs = [json.loads(l) for l in open(os.path.join(ev, "session.jsonl")) if l.strip()]
    drv = parse_driver(os.path.join(ev, "driver.log"))
    appends = [(t, re.search(r'nonce=([0-9a-f]{12})', m) or re.search(r'MSG\d+ \S* ?([0-9a-f]{12})', m), m) for t, m in drv if m.startswith("appended")]
    keys = [(t, m) for t, m in drv if "submitted" in m or m.startswith("/")]
    # index records
    queued = []; runs = []; drained = []; texts = []; user_intents = []
    bg_run_ids = set()
    for r in recs:
        pt = r.get("payload_type", ""); p = r.get("payload") or {}
        if pt == "runtime.retained_fact" and (p.get("record") or {}).get("payload_type") == "inbox_delivery.run_origin":
            bg_run_ids.add(p.get("run_id"))
        if pt == "runtime.session" and p.get("kind") == "run":
            ev_ = p.get("event") or {}
            k = ev_.get("kind")
            if k == "inbox_item_queued": queued.append((ts(r), ev_.get("body", ""), ev_.get("disposition")))
            elif k == "started": runs.append((ts(r), p.get("run_id"), ev_.get("prompt", "")))
            elif k == "inbox_item_drained": drained.append((ts(r), p.get("run_id"), ((ev_.get("delivery_snapshot") or {}).get("body", ""))))
            elif k == "assistant_message_committed": texts.append((ts(r), p.get("run_id"), json.dumps(ev_)))
        if pt == "runtime.user_intent.accepted": user_intents.append(ts(r))
    # --yolo evidence rule: no tool call in this session may name an evidence path or the driver's files
    tools = []; tainted = []
    for r in recs:
        p = r.get("payload") or {}
        if r.get("payload_type","").startswith("tool_batch.effect") and (p.get("record") or {}).get("tool_name"):
            tools.append(p["record"]["tool_name"])
        s = json.dumps(p)
        if '"arguments"' in s or '"command"' in s:
            for bad in ("/evidence/", "driver.log", "session.jsonl", "score.py", "arm2-monitor"):
                if bad in s and "monitor-facts" not in s: tainted.append(bad)
    for r in recs:
        p = r.get("payload") or {}
        ev_ = p.get("event") if isinstance(p, dict) else None
        if isinstance(ev_, dict) and ev_.get("kind") == "assistant_tool_calls_committed":
            for c in ev_.get("tool_calls", []):
                if c.get("name") == "monitor":
                    try: a = json.loads(c.get("args", "{}"))
                    except Exception: a = {"raw": c.get("args")}
                    a = {k: (v if k != "command" else v[:80] + "…") for k, v in a.items()}
                    print(f"monitor call args: {json.dumps(a)}")
    print(f"tool calls={sorted(set(tools))} evidence-path mentions in tool records={sorted(set(tainted))}")
    print(f"session records={len(recs)} runs={len(runs)} background_origin_runs={len([r for r in runs if r[1] in bg_run_ids])} user_intents={len(user_intents)} queued={len(queued)}")
    for t_app, m, line in appends:
        nonce = m.group(1) if m else None
        q = next((x for x in queued if nonce and nonce in x[1]), None)
        d = next((x for x in drained if nonce and nonce in x[2]), None)
        run = next((x for x in runs if d and x[1] == d[1]), None)
        txt = next((x for x in texts if nonce and nonce in x[2] and "RECEIVED" in x[2]), None)
        keys_in = [k for k in keys if run and t_app <= k[0] <= run[0]]
        def rel(x): return f"{x - t_app:+.3f}s" if x else "none"
        print(f"- {line[:60]}")
        print(f"    queued {rel(q[0] if q else None)} disp={q[2] if q else None} | run_start {rel(run[0] if run else None)} bg={run[1] in bg_run_ids if run else None} | drained {rel(d[0] if d else None)} | text {rel(txt[0] if txt else None)} | keys_in_window={len(keys_in)}")
    ctl = [(t, m) for t, m in drv if m.startswith("null control") or m.startswith("control")]
    for t, m in ctl:
        print(f"- control at +0: {m[:90]}")
    if ctl:
        t0 = ctl[0][0]; t1 = next((t for t, m in drv if m.startswith("control runs")), None)
        if t1:
            n = len([r for r in runs if t0 <= (r[0] or 0) <= t1])
            print(f"    runs started inside the control window [{t0:.0f},{t1:.0f}]: {n}")

if __name__ == "__main__":
    main(sys.argv[1])
