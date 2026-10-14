#!/usr/bin/env python3
"""RPC-mode probe: idle wake, session identity, bash env, busy-turn delivery. Usage: rpc_probe.py <workdir>"""
import json, os, subprocess, sys, threading, time, pathlib

work = pathlib.Path(sys.argv[1]).resolve()
home, log, trig = work / "home", work / "events.jsonl", work / "trigger.txt"
here = pathlib.Path(__file__).parent.resolve()
subprocess.run([str(here / "setup-home.sh"), str(home)], check=True)
env = {k: v for k, v in os.environ.items() if not k.startswith("CLAUDE") and k != "AI_AGENT"}
env.update(PI_CODING_AGENT_DIR=str(home), PROBE_LOG=str(log), PROBE_TRIGGER=str(trig), PI_SKIP_VERSION_CHECK="1", PI_TELEMETRY="0")
log.write_text("")
p = subprocess.Popen(["pi", "--mode", "rpc", "-e", str(here / "probe-ext.ts"), "--session-dir", str(home / "sessions")],
                     stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, cwd=work, env=env)
events = []
def reader():
    for line in p.stdout:
        try: events.append((time.time(), json.loads(line)))
        except Exception: pass
threading.Thread(target=reader, daemon=True).start()
def send(o): p.stdin.write((json.dumps(o) + "\n").encode()); p.stdin.flush()
def types(since=0): return [e["type"] for t, e in events if t >= since]
def wait_for(pred, timeout):
    end = time.time() + timeout
    while time.time() < end:
        if pred(): return True
        time.sleep(0.2)
    return False
def wake(text): trig.write_text(text + "\n")

out = {}
time.sleep(3)
send({"id": "s", "type": "get_state"})
wait_for(lambda: any(e.get("id") == "s" for _, e in events), 10)
st = next((e for _, e in events if e.get("id") == "s"), {})
out["get_state"] = {k: st.get("data", {}).get(k) for k in ("sessionId", "sessionFile")}

# T2: idle wake, no prompt sent by the client
t0 = time.time(); n0 = len(events)
wake("idle Reply with exactly the word PONG.")
out["idle_wake_agent_end"] = wait_for(lambda: "agent_end" in types(t0), 90)
out["idle_wake_event_types"] = sorted(set(types(t0)))

# T3: bash children env
t1 = time.time()
send({"id": "p1", "type": "prompt", "message": "Run this exact bash command and show its output: env | grep -E '^(PI_|AI_AGENT)' | sed 's/=.*//' | sort"})
wait_for(lambda: "agent_end" in types(t1), 120)

# T4: trigger while a turn runs
t2 = time.time()
send({"id": "p2", "type": "prompt", "message": "Count slowly from 1 to 40, one number per line, then say DONE."})
wait_for(lambda: "agent_start" in types(t2), 20)
time.sleep(1)
wake("followUp Reply with exactly the word FOLLOWED.")
wait_for(lambda: types(t2).count("agent_end") >= 2, 180)
out["busy_followup_agent_ends"] = types(t2).count("agent_end")

send({"type": "abort"}); time.sleep(1); p.stdin.close()
try: p.wait(10)
except Exception: p.kill()
out["log"] = [json.loads(l) for l in log.read_text().splitlines() if l]
(work / "result.json").write_text(json.dumps(out, indent=1))
print(json.dumps({k: v for k, v in out.items() if k != "log"}, indent=1))
print("log events:", [(l["ev"], l.get("messageType") or l.get("mode") or l.get("tool")) for l in out["log"]])
