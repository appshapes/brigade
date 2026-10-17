#!/usr/bin/env python3
"""Offline scorer for the Codex queue arm (route A: `codex queue` into an idle TUI session).

Reads the session's rollout JSONL (Codex's own log, copied into the evidence dir as rollout.jsonl), the driver.log
beside it and the queue-poll log (queue.log: timestamped row counts of queue_1.sqlite for the thread) and prints,
per outside send: when the queued row appeared and vanished, when the turn started (event_msg task_started), when
the queued text became a user message in the thread, when the model's own text containing the nonce landed,
keystrokes the driver logged inside the window, and the turns started inside the null control.  Only the model's
own agent message counts for CONTENT; echoes of the prompt never do.
Usage: score.py <evidence-dir>
"""
import json, re, sys, os, datetime

def iso(t):
    return datetime.datetime.fromisoformat(t.replace('Z', '+00:00')).timestamp()

def parse_driver(path):
    rows = []
    for line in open(path):
        m = re.match(r'(\S+)Z (.*)', line.rstrip('\n'))
        if not m: continue
        rows.append((iso(m.group(1) + 'Z'), m.group(2)))
    return rows

def parse_queue(path):
    rows = []
    if not os.path.exists(path): return rows
    for line in open(path):
        p = line.split()
        if len(p) >= 2:
            try: rows.append((float(p[0]), int(p[1])))
            except ValueError: pass
    return rows

def main(ev):
    recs = []
    for l in open(os.path.join(ev, "rollout.jsonl")):
        l = l.strip()
        if not l: continue
        try: recs.append(json.loads(l))
        except Exception: pass
    drv = parse_driver(os.path.join(ev, "driver.log"))
    qlog = parse_queue(os.path.join(ev, "queue.log"))
    sends = [(t, re.search(r'([0-9a-f]{12})', m), m) for t, m in drv if m.startswith("sent:")]
    keys = [(t, m) for t, m in drv if "submitted" in m or m.startswith("typed")]
    starts = []; completes = []; users = []; agents = []; tools = []
    for r in recs:
        t = iso(r["timestamp"]) if r.get("timestamp") else None
        ty = r.get("type"); p = r.get("payload") or {}
        pt = p.get("type") if isinstance(p, dict) else None
        if ty == "event_msg" and pt == "task_started": starts.append((t, p.get("turn_id"), (p.get("turn_attribution") or {}).get("turn_trigger")))
        elif ty == "event_msg" and pt == "task_complete": completes.append((t, p.get("turn_id"), p.get("last_agent_message") or ""))
        elif ty == "event_msg" and pt == "user_message": users.append((t, p.get("message") or json.dumps(p)))
        elif ty == "event_msg" and pt == "agent_message": agents.append((t, p.get("message") or json.dumps(p)))
        elif ty == "event_msg" and pt == "item_completed":
            it = p.get("item") or {}
            if it.get("type") == "userMessage": users.append(((p.get("completed_at_ms") or 0) / 1000 or t, json.dumps(it)))
            elif it.get("type") == "agentMessage": agents.append(((p.get("completed_at_ms") or 0) / 1000 or t, it.get("text") or ""))
        elif ty == "response_item" and pt == "message":
            role = p.get("role"); txt = json.dumps(p.get("content"))
            if role == "user": users.append((t, txt))
            elif role == "assistant": agents.append((t, txt))
        elif ty == "response_item" and pt in ("function_call", "custom_tool_call"):
            tools.append((p.get("name"), (p.get("arguments") or p.get("input") or "")[:200]))
    tainted = sorted({bad for _, a in tools for bad in ("/evidence/", "driver.log", "rollout.jsonl", "score.py", "queue.log") if bad in a})
    trig = {}
    for s_ in starts: trig[s_[2]] = trig.get(s_[2], 0) + 1
    print(f"rollout records={len(recs)} turns_started={len(starts)} by_trigger={trig} turns_completed={len(completes)} user_msgs={len(users)} agent_msgs={len(agents)}")
    print(f"tool calls={sorted({n for n, _ in tools})} evidence-path mentions in tool args={tainted}")
    for t_send, m, line in sends:
        nonce = m.group(1) if m else None
        # queue row: first poll sample after the send with count>0, then first with count 0 after that
        appeared = next((t for t, n in qlog if t >= t_send - 0.5 and n > 0), None)
        vanished = next((t for t, n in qlog if appeared and t > appeared and n == 0), None)
        st = next((x for x in starts if x[0] and x[0] >= t_send - 0.5), None)
        um = next((x for x in users if nonce and nonce in x[1] and x[0] and x[0] >= t_send - 0.5), None)
        ag = next((x for x in agents if nonce and nonce in x[1] and "RECEIVED" in x[1] and x[0] and x[0] >= t_send - 0.5), None)
        keys_in = [k for k in keys if st and t_send <= k[0] <= st[0]]
        def rel(x): return f"{x - t_send:+.3f}s" if x else "none"
        print(f"- {line[:70]}")
        print(f"    queue_row {rel(appeared)}->{rel(vanished)} | turn_start {rel(st[0] if st else None)} trigger={st[2] if st else None} | user_item {rel(um[0] if um else None)} | agent_text {rel(ag[0] if ag else None)} | keys_in_window={len(keys_in)}")
    ctl = [(t, m) for t, m in drv if m.startswith("null control")]
    end = [(t, m) for t, m in drv if m.startswith("control turns")]
    if ctl and end:
        t0, t1 = ctl[0][0], end[0][0]
        n = [s for s in starts if s[0] and t0 <= s[0] <= t1]
        print(f"- null control [{datetime.datetime.utcfromtimestamp(t0).strftime('%H:%M:%S')}..{datetime.datetime.utcfromtimestamp(t1).strftime('%H:%M:%S')}] ({t1-t0:.0f}s): turns started inside = {len(n)}")

if __name__ == "__main__":
    main(sys.argv[1])
