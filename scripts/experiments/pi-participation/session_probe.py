#!/usr/bin/env python3
"""Session-replacement probe: new, fork, clone, switch, compact, steer, nested pi, two sessions per folder. Usage: session_probe.py <workdir>"""
import json, os, subprocess, sys, threading, time, pathlib

work = pathlib.Path(sys.argv[1]).resolve()
here = pathlib.Path(__file__).parent.resolve()
home = work / "home2"
subprocess.run([str(here / "setup-home.sh"), str(home)], check=True)
base = {k: v for k, v in os.environ.items() if not k.startswith("CLAUDE") and k != "AI_AGENT"}
base.update(PI_CODING_AGENT_DIR=str(home), PI_SKIP_VERSION_CHECK="1", PI_TELEMETRY="0")

class Rpc:
    def __init__(self, tag):
        self.log, self.trig = work / f"{tag}.events.jsonl", work / f"{tag}.trigger"
        self.log.write_text("")
        env = dict(base, PROBE_LOG=str(self.log), PROBE_TRIGGER=str(self.trig))
        self.p = subprocess.Popen(["pi", "--mode", "rpc", "-e", str(here / "probe-ext.ts"), "--session-dir", str(home / "sessions")],
                                  stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL, cwd=work, env=env)
        self.ev, self.n = [], 0
        threading.Thread(target=self._read, daemon=True).start()
        time.sleep(3)
    def _read(self):
        for line in self.p.stdout:
            try: self.ev.append((time.time(), json.loads(line)))
            except Exception: pass
    def cmd(self, c, timeout=60):
        self.n += 1; i = f"c{self.n}"
        self.p.stdin.write((json.dumps(dict(c, id=i)) + "\n").encode()); self.p.stdin.flush()
        end = time.time() + timeout
        while time.time() < end:
            for _, e in self.ev:
                if e.get("id") == i and e.get("type") == "response": return e
            time.sleep(0.2)
        return {"timeout": True}
    def count(self, t): return sum(1 for _, e in self.ev if e.get("type") == t)
    def wait_ends(self, n, timeout=120):
        end = time.time() + timeout
        while time.time() < end:
            if self.count("agent_end") >= n: return True
            time.sleep(0.2)
        return False
    def sid(self):
        d = self.cmd({"type": "get_state"}).get("data", {}); return d.get("sessionId")
    def wake(self, text): self.trig.write_text(text + "\n")
    def close(self):
        try: self.p.stdin.close(); self.p.wait(10)
        except Exception: self.p.kill()
    def events(self): return [json.loads(l) for l in self.log.read_text().splitlines() if l]

out = {}
a = Rpc("a")
sid0 = a.sid(); out["sid_startup"] = sid0
a.cmd({"type": "prompt", "message": "Say OK."}); a.wait_ends(1)

r = a.cmd({"type": "new_session"}); sid_new = a.sid()
out["new_session"] = {"changed": sid_new != sid0}
a.wake("idle Reply with exactly the word AFTERNEW."); out["wake_after_new"] = a.wait_ends(2, 60)
out["wake_after_new_single"] = a.count("agent_end") == 2   # one watcher only: a second would double-fire

a.cmd({"type": "prompt", "message": "Say OK again."}); a.wait_ends(3)
fm = a.cmd({"type": "get_fork_messages"}).get("data", {}).get("messages", [])
if fm:
    fk = a.cmd({"type": "fork", "entryId": fm[0].get("entryId")}); out["fork"] = {"ok": fk.get("success"), "sid_changed": a.sid() != sid_new}
cl = a.cmd({"type": "clone"}); out["clone"] = {"ok": cl.get("success"), "sid_changed": a.sid() != sid_new}
sw = a.cmd({"type": "switch_session", "sessionPath": a.cmd({"type":"get_state"})["data"]["sessionFile"]}); out["switch_self"] = sw.get("success")
n_before = a.count("agent_end")
a.cmd({"type": "compact"}, 120); out["compact_sid_same"] = a.sid() is not None
a.wake("idle Reply with exactly the word AFTERCOMPACT."); out["wake_after_compact"] = a.wait_ends(n_before + 1, 60)

# steer / nextTurn while busy
n = a.count("agent_end")
a.cmd({"type": "prompt", "message": "Count slowly from 1 to 60, one per line."}); time.sleep(1)
a.wake("steer Stop counting and reply with exactly STEERED.")
out["steer_ends_by"] = a.wait_ends(n + 1, 120)
n = a.count("agent_end")
a.cmd({"type": "prompt", "message": "Count slowly from 1 to 60, one per line."}); time.sleep(1)
a.wake("nextTurn Remember the word KIWI.")
a.wait_ends(n + 1, 120); time.sleep(3)
out["nextTurn_started_extra_run"] = a.count("agent_end") > n + 1

# resume by id from a second process, same folder, while A is alive
b = Rpc("b"); sid_b = b.sid()
out["two_sessions"] = {"distinct": sid_b != a.sid()}
a.wake("idle Reply with exactly the word ONLYA."); time.sleep(1)
na = a.count("agent_end"); a.wait_ends(na + 1 if na else 1, 60)
out["two_sessions"]["b_not_woken_by_a_trigger"] = b.count("agent_start") == 0
b.close()
sid_a_final = a.sid(); file_a = a.cmd({"type":"get_state"})["data"]["sessionFile"]
a.close()

# resume the same session in a new process: same id?
c = Rpc("c")  # fresh process; now switch into A's file
c.cmd({"type": "switch_session", "sessionPath": file_a}); out["resume_same_id"] = (c.sid() == sid_a_final)
c.close()

# nested pi inside a pi bash child: inherited PI_SESSION_ID vs the child's own
nl = work / "nested.events.jsonl"; nl.write_text("")
env = dict(base, PI_SESSION_ID="PARENT-ID", PROBE_LOG=str(nl))
try:
    subprocess.run(["pi", "-p", "--no-session", "-e", str(here / "probe-ext.ts"), "Say OK."], env=env, cwd=work, capture_output=True, stdin=subprocess.DEVNULL, timeout=90)
except subprocess.TimeoutExpired:
    pass
ss = [json.loads(l) for l in nl.read_text().splitlines() if l and '"session_start"' in l]
out["nested"] = {"ext_sid_is_own": bool(ss) and ss[0]["sid"] != "PARENT-ID", "env_sid_inherited_in_ext_process": bool(ss) and ss[0]["env_sid"]}

out["a_events"] = [(e["ev"], e.get("reason") or e.get("mode") or e.get("messageType")) for e in a.events()]
(work / "session-result.json").write_text(json.dumps(out, indent=1))
print(json.dumps({k: v for k, v in out.items() if k != "a_events"}, indent=1)); print(out["a_events"])
