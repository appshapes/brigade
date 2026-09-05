#!/usr/bin/env python3
"""E5-soak driver (plan row P5-11, E2E-12): two INTERACTIVE pty sessions on
ONE profile for two hours of wall clock, both idle most of the time, with a
low steady beat of traffic between them; the token refresh through the
sidecar flock is the subject (brief 4.1).

    python3 scripts/experiments/E5-soak/soak.py [--minutes 120] [--beat 12] [--sessions 2] [--profile bob]
                                                [--out <bundle>] [--tag <t>] [--no-burst] [--burst-at 60,90]

Defaults with no arguments are the deliverable. Develop with `--minutes 6`
(M6 is `--minutes 12 --beat 4`) before spending 120. Never run two driver
instances concurrently. Every session started is counted and reported.

The shape (brief 4.1, 4.4): both sessions carry
pluginConfigs."brigade@inline".options.profile = <profile>, so both
SessionStart hooks register two distinct Brigade sessions under one
principal and spawn two watchers whose adapter children share
profiles/<profile>/session.json and its sidecar lock. After the canary each
session gets ONE setup prompt; then, per session, at t = beat/2 + k*beat
minutes, a beat: `brigade sessions --json` then `brigade send <peer>
TICK-<n>-<hex>`. The rotation poller (profile status every 30 s) predicts
each rotation from token_expires_at and schedules an extra beat at
expiry - 60 s on session A (the deliberate three-way contention). At
t = minutes - 1 the final round trip: A sends B FINAL-<hex>, the driver
requires the frame in B's transcript and the reply row in the database.
Unless --no-burst, burst.py's phases run inline against session A at the
--burst-at minutes (60 and 90) when the run is long enough to reach them.

The evidence bundle layout is brief 4.7; score.py re-derives summary.json,
soak.tsv and burst.tsv from it offline.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import e5s      # noqa: E402
import burst    # noqa: E402

REPO = e5s.REPO


def _w(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(content)


def isolation_record(sessions):
    """Brief section 1: isolation asserted POSITIVELY before anything is sent.
    The nested session's identity comes from its by-pid map read live
    (claude_pid, claude_session_id, socket_path); the outer's from this
    process's environment (common.outer_identity). The map carries no token
    and no environment is ever read (no `ps e`), so three of
    common.isolation_verdict's four differences are checked; the token's is
    not derivable without reading the nested environment and is recorded as
    such."""
    outer = e5s.common.outer_identity()
    nested = {}
    for n, s in sessions.items():
        m = s.map or {}
        r = {"claude_pid": s.claude_pid, "map_claude_pid": m.get("claude_pid"),
             "claude_session_id": m.get("claude_session_id"), "socket_path": m.get("socket_path"),
             "pid_differs": bool(s.claude_pid) and str(s.claude_pid) != str(outer.get("pid") or ""),
             "session_differs": bool(m.get("claude_session_id")) and m.get("claude_session_id") != (outer.get("session") or ""),
             "socket_differs": bool(m.get("socket_path")) and m.get("socket_path") != (outer.get("socket") or "")}
        r["ok"] = r["pid_differs"] and r["session_differs"] and r["socket_differs"]
        nested[n] = r
    return {"outer": {k: outer.get(k) for k in ("session", "socket", "pid")}, "nested": nested,
            "ok": bool(nested) and all(v["ok"] for v in nested.values()),
            "token_check": "not derivable: the map carries no token and no environment is read (no ps e/-E/eww)"}


class Soak:
    def __init__(self, args, bundle, log):
        self.args = args
        self.bundle = bundle
        self.log = log
        self.evroot = os.path.join(bundle, "evidence", "soak")
        os.makedirs(self.evroot, exist_ok=True)
        self.events = os.path.join(self.evroot, "events.ndjson")
        self.liveness = os.path.join(self.evroot, "liveness.ndjson")
        self.sessions_started = 0
        self.locks = {}
        self.refusals_seen = {}
        self.run = {"bundle": bundle, "args": vars(args), "started": e5s.iso_now(), "sessions_started": 0,
                    "beats": [], "refresh_beats": [], "final": None, "bursts": {}}

    def event(self, **kv):
        kv.setdefault("ms", e5s.now_ms())
        kv.setdefault("t", e5s.iso_now())
        e5s.append_ndjson(self.events, kv)

    # ----- setup ------------------------------------------------------------ #
    def start_sessions(self, stack):
        names = ["a", "b", "c", "d"][: self.args.sessions]
        self.sess = {}
        for n in names:
            s = e5s.PtySoak(stack, os.path.join(self.evroot, n), arm="soak-" + n, profile=self.args.profile)
            s.start()
            self.sessions_started += 1
            self.sess[n] = s
            self.locks[n] = threading.Lock()
        ready = {}
        for n, s in self.sess.items():
            ok, ms = s.wait_ready(timeout=300)
            ready[n] = {"ok": ok, "ms": ms, "claude_pid": s.claude_pid, "brigade_id": s.brigade_id,
                        "map": {k: s.map.get(k) for k in ("profile", "inbound", "permission_mode", "adapter_command", "team_name", "session_name")}}
            self.log("%s session %s ready in %d ms: claude pid %s, brigade %s, map %s"
                     % ("ok:" if ok else "FAIL:", n, ms, s.claude_pid, s.brigade_id, json.dumps(ready[n]["map"])))
        self.run["ready"] = ready
        if not all(r["ok"] for r in ready.values()):
            raise SystemExit("a session never became ready; nothing after this would mean anything")
        # the preconditions on the maps (brief 4.2, 4.3)
        for n, r in ready.items():
            m = r["map"]
            if m.get("permission_mode") != "default" or m.get("profile") != self.args.profile or m.get("inbound") != "accept" \
                    or (m.get("adapter_command") or []) != []:
                self.log("FAIL: session %s map is not the soak's shape: %s" % (n, json.dumps(m)))
                raise SystemExit("map precondition failed")
        ids = [r["brigade_id"] for r in ready.values()]
        if len(set(ids)) != len(ids):
            raise SystemExit("the sessions did not get distinct Brigade session ids")
        iso = isolation_record(self.sess)
        self.run["isolation"] = iso
        self.log("%s isolation: nested pid/session/socket differ from the outer's for every session: %s"
                 % ("ok:" if iso["ok"] else "FAIL:", json.dumps({n: v["ok"] for n, v in iso["nested"].items()})))
        if not iso["ok"]:
            raise SystemExit("isolation not established; nothing is sent")
        for n, s in self.sess.items():
            if not os.path.exists(s.pidfile_path()):
                self.log("FAIL: session %s has no watcher pidfile at %s" % (n, s.pidfile_path()))
        self.log("ok: %d sessions on profile %s share one credential file: %s"
                 % (len(self.sess), self.args.profile, os.path.join(stack.cfg, "profiles", self.args.profile, "session.json")))

    def peer_of(self, n):
        names = list(self.sess)
        return names[(names.index(n) + 1) % len(names)]

    # ----- beats ------------------------------------------------------------ #
    def beat(self, n, k, kind="beat"):
        s = self.sess[n]
        peer = self.sess[self.peer_of(n)]
        with self.locks[n]:
            text = e5s.beat_prompt(peer.brigade_id, k, e5s.rand_hex(2))
            self.event(kind="beat_start", session=n, n=k, beat_kind=kind)
            r = s.beat(text, kind, k, meta={"peer": self.peer_of(n)})
            self.event(kind="beat_done", session=n, n=k, beat_kind=kind, outcome=r.get("outcome"),
                       duration_ms=r.get("duration_ms"), new_assistant=r.get("new_assistant_records"),
                       dialogs=r.get("dialogs_escaped_during"))
            self.log("measured: %s beat %d (%s) on %s: %s in %s ms, %s new assistant records, %s dialogs escaped"
                     % (kind, k, "->" + self.peer_of(n), n, r.get("outcome"), r.get("duration_ms"), r.get("new_assistant_records"),
                        r.get("dialogs_escaped_during")))
            return r

    def setup(self):
        ths = []
        out = {}
        for n, s in self.sess.items():
            peer = self.sess[self.peer_of(n)]

            def run(n=n, s=s, peer=peer):
                with self.locks[n]:
                    out[n] = s.beat(e5s.setup_prompt("soak-" + n, "soak-" + self.peer_of(n), peer.brigade_id), "setup", 0)
            t = threading.Thread(target=run, daemon=True)
            t.start()
            ths.append(t)
        for t in ths:
            t.join(timeout=e5s.BEAT_CAP + 60)
        for n, r in out.items():
            self.log("measured: setup prompt on %s: %s in %s ms" % (n, r.get("outcome"), r.get("duration_ms")))
        self.run["setup"] = out

    def final_round_trip(self, stack):
        a = list(self.sess)[0]
        b = self.peer_of(a)
        A, B = self.sess[a], self.sess[b]
        hexs = e5s.rand_hex(3)
        t0 = e5s.now_ms()
        with self.locks[a]:
            r = A.beat(e5s.final_prompt(B.brigade_id, hexs), "final", 99)
        # the frame in B's transcript
        found_ms = None
        deadline = time.monotonic() + 180
        while time.monotonic() < deadline:
            recs = burst.transcript_records_since(B.transcript_path(), t0)
            if any(("FINAL-" + hexs) in text for _, text in burst.user_texts(recs)):
                found_ms = e5s.now_ms()
                break
            time.sleep(1)
        # B's reply row in the database (B -> A after t0) and its frame in A's transcript
        reply_rows = []
        reply_in_a = None
        deadline = time.monotonic() + 240
        while time.monotonic() < deadline:
            reply_rows = stack.message_rows(A.brigade_id, sender=B.brigade_id, since_iso=burst.iso_ms(t0 - 2000))
            if reply_rows:
                fr = burst.frames_in(burst.transcript_records_since(A.transcript_path(), t0))
                if any(mid in fr for mid in (r["message_id"] for r in reply_rows)):
                    reply_in_a = e5s.now_ms()
                    break
            time.sleep(2)
        rec = {"hex": hexs, "t0_ms": t0, "a_beat": r, "frame_in_b_ms": found_ms,
               "reply_rows": reply_rows, "reply_frame_in_a_ms": reply_in_a,
               "ok": bool(found_ms and reply_rows and reply_in_a)}
        self.run["final"] = rec
        self.event(kind="final", ok=rec["ok"], frame_in_b_ms=found_ms, reply_rows=len(reply_rows), reply_in_a_ms=reply_in_a)
        self.log("%s final round trip: frame in B after %s ms, %d reply row(s) in the database, B's reply framed in A after %s ms"
                 % ("ok:" if rec["ok"] else "FAIL:", (found_ms - t0) if found_ms else None, len(reply_rows), (reply_in_a - t0) if reply_in_a else None))
        return rec

    # ----- the run ---------------------------------------------------------- #
    def schedule(self):
        beat = self.args.beat
        minutes = self.args.minutes
        times = []
        t = beat / 2.0
        while t < minutes - 1:
            times.append(t)
            t += beat
        return times

    def main_loop(self, stack, pollers):
        T0 = e5s.now_ms()
        self.run["t0_ms"] = T0
        beats_due = [(t, k + 1) for k, t in enumerate(self.schedule())]
        bursts_due = [] if self.args.no_burst else [m for m in self.args.burst_at if m < self.args.minutes - 3]
        served_expiries = set()
        final_at = (self.args.minutes - 1) * 60.0
        last_sample = 0
        roster_marks = {0: "t0", 60: "t060", self.args.minutes - 1: "t%03d" % (self.args.minutes - 1)}
        a = list(self.sess)[0]
        A = self.sess[a]
        wlog_a = A.watcher_log_path()
        alog = os.path.join(stack.state, "logs", "adapter-%s.log" % self.args.profile)
        final_done = False
        while True:
            el = (e5s.now_ms() - T0) / 1000.0
            # regular beats, both sessions concurrently
            due = [b for b in beats_due if el >= b[0] * 60]
            for (tmin, k) in due:
                beats_due.remove((tmin, k))
                for n in self.sess:
                    threading.Thread(target=self.beat, args=(n, k), daemon=True).start()
            # the refresh-window beat (brief 4.1): expiry - 60 s, on A
            exp = pollers.token_expires_at()
            if exp:
                exp_ms = burst._parse_iso_ms(exp)
                if exp not in served_expiries and e5s.now_ms() >= exp_ms - 60000 and e5s.now_ms() < exp_ms + 30000:
                    served_expiries.add(exp)
                    self.run["refresh_beats"].append({"expiry": exp, "at_ms": e5s.now_ms()})
                    self.log("say: refresh window: token expires at %s; extra beat on %s now (expiry - %d s)" % (exp, a, (exp_ms - e5s.now_ms()) // 1000))
                    threading.Thread(target=self.beat, args=(a, 1000 + len(served_expiries), "refresh"), daemon=True).start()
            # the bursts, inline against A
            for m in list(bursts_due):
                if el >= m * 60:
                    bursts_due.remove(m)
                    threading.Thread(target=self.run_burst, args=(stack, m, wlog_a, alog), daemon=True).start()
            # roster snapshots
            for m, name in list(roster_marks.items()):
                if el >= m * 60:
                    roster_marks.pop(m)
                    snap = stack.roster_snapshot(self.args.profile)
                    _w(os.path.join(self.evroot, "roster", name + ".json"), json.dumps(snap, indent=2))
                    stack.ps_sample(name)
            # liveness sample every 60 s
            if el - last_sample >= 60:
                last_sample = el
                refusals = {n: s.provider_refusals_total() for n, s in self.sess.items()}
                rec = {"ms": e5s.now_ms(), "t": e5s.iso_now(), "elapsed_s": int(el),
                       "session_log_bytes": {n: s.session_log_bytes() for n, s in self.sess.items()},
                       "token_expires_at": exp,
                       "drain_marks": {n: sum(1 for m in s.marks() if m.get("event") == "drain") for n, s in self.sess.items()},
                       "alive": {n: bool(s.thread and s.thread.is_alive()) for n, s in self.sess.items()},
                       "provider_refusals": refusals}
                e5s.append_ndjson(self.liveness, rec)
                self.log("sample: t=%dm session.log bytes %s, token_expires_at %s, drains %s, alive %s, provider refusals %s"
                         % (int(el // 60), rec["session_log_bytes"], exp, rec["drain_marks"], rec["alive"], refusals))
                for n, s in self.sess.items():
                    if s.thread and not s.thread.is_alive():
                        self.log("FAIL: session %s's expect process has exited at t=%dm (%s)" % (n, int(el // 60), s.how))
                    if refusals[n] and refusals[n] != self.refusals_seen.get(n):
                        self.refusals_seen[n] = refusals[n]
                        self.log("FAIL: session %s: the provider refused %d turn(s) so far (safeguards; the session may stay refused)" % (n, refusals[n]))
            # the final round trip
            if not final_done and el >= final_at:
                final_done = True
                self.final_round_trip(stack)
            if el >= self.args.minutes * 60 and final_done and not beats_due:
                break
            time.sleep(5)
        # let in-flight beats finish
        for n in self.sess:
            with self.locks[n]:
                pass

    def run_burst(self, stack, minute, wlog_a, alog):
        a = list(self.sess)[0]
        A = self.sess[a]
        with self.locks[a]:
            self.log("say: burst phase at t=%d min against %s (%s)" % (minute, a, A.brigade_id))
            if minute == self.args.burst_at[0]:
                r = burst.phase_hints(stack, A, A.brigade_id, os.path.join(self.bundle, "evidence", "burst", "hints"),
                                      self.args.hints, self.args.hint_seconds, self.log, watcher_log=wlog_a, adapter_log=alog)
                self.run["bursts"]["b1"] = r
            else:
                r, srecs = burst.phase_messages(stack, A, A.brigade_id, os.path.join(self.bundle, "evidence", "burst", "messages"),
                                                self.args.senders, self.args.per_sender, self.log, watcher_log=wlog_a,
                                                pause_watcher=self.args.pause_watcher, redeliver_minutes=6,
                                                kill_adapter_child=self.args.kill_adapter_child)
                self.run["bursts"]["b2"] = r
                if r.get("bound_reached") and not self.args.no_b3:
                    wait = (r["t0_ms"] + 60000 - e5s.now_ms()) / 1000.0
                    if wait > 0:
                        time.sleep(wait)
                    self.run["bursts"]["b3"] = burst.phase_sustained(stack, A, A.brigade_id,
                                                                     os.path.join(self.bundle, "evidence", "burst", "sustained"),
                                                                     srecs, 5, 20, self.log, watcher_log=wlog_a)

    # ----- the end ---------------------------------------------------------- #
    def finish(self, stack, pollers):
        # 5.3 clause 7: both watchers alive BEFORE exit
        pre = {}
        for n, s in self.sess.items():
            pre[n] = {"pidfile": os.path.exists(s.pidfile_path()), "map": os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % s.claude_pid)),
                      "expect_alive": bool(s.thread and s.thread.is_alive())}
        self.run["pre_exit"] = pre
        self.log("%s before exit: %s" % ("ok:" if all(v["pidfile"] and v["map"] and v["expect_alive"] for v in pre.values()) else "FAIL:", json.dumps(pre)))
        notices = [n for n in os.listdir(stack.state) if n.endswith(".notice")] if os.path.isdir(stack.state) else []
        self.run["notice_files"] = notices
        # stop both sessions
        ths = []
        stops = {}
        for n, s in self.sess.items():
            def run(n=n, s=s):
                stops[n] = s.stop_session()
            t = threading.Thread(target=run, daemon=True)
            t.start()
            ths.append(t)
        for t in ths:
            t.join(timeout=180)
        self.run["stops"] = stops
        after = {}
        for n, s in self.sess.items():
            t0 = time.monotonic()
            while time.monotonic() - t0 < 15 and (os.path.exists(s.pidfile_path()) or os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % s.claude_pid))):
                time.sleep(0.25)
            after[n] = {"pidfile_gone": not os.path.exists(s.pidfile_path()),
                        "map_gone": not os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % s.claude_pid)),
                        "ms": int((time.monotonic() - t0) * 1000), "how": s.how}
        self.run["after_exit"] = after
        self.log("%s after /exit: %s" % ("ok:" if all(v["pidfile_gone"] and v["map_gone"] for v in after.values()) else "FAIL:", json.dumps(after)))
        pollers.stop()
        # the run's state directory into the bundle
        st_dst = os.path.join(self.evroot, "state")
        for sub in ("watchers", "logs"):
            src = os.path.join(stack.state, sub)
            if os.path.isdir(src):
                shutil.copytree(src, os.path.join(st_dst, sub), dirs_exist_ok=True)
        for n in notices:
            shutil.copy2(os.path.join(stack.state, n), os.path.join(st_dst, n))


def run_smoke(stack, soak, log):
    """M5 (brief section 3): one interactive session on the soak's exact
    settings -- onboard, canary, `brigade whoami`, /exit. Asserts the by-pid
    map (profile, inbound accept, permission_mode default, adapter_command []),
    the watcher pidfile mid-run, that the model reached `brigade` as a BARE
    command with no dialog, the SessionStart context line read from the tree
    of today (hook.go startLine), and that the map and the pidfile are gone
    after /exit. One session, about three minutes."""
    s = e5s.PtySoak(stack, os.path.join(soak.evroot, "smoke"), arm="smoke", profile=soak.args.profile)
    s.start()
    soak.sessions_started += 1
    ok, ms = s.wait_ready(timeout=300)
    rec = {"ready": ok, "ready_ms": ms, "claude_pid": s.claude_pid, "brigade_id": s.brigade_id,
           "map": {k: s.map.get(k) for k in ("profile", "inbound", "permission_mode", "adapter_command", "team_name", "session_name")},
           "pidfile_mid_run": os.path.exists(s.pidfile_path()) if s.claude_pid else False}
    log("%s smoke: session ready in %d ms; map %s; pidfile %s" % ("ok:" if ok else "FAIL:", ms, json.dumps(rec["map"]), rec["pidfile_mid_run"]))
    if ok:
        rec["isolation"] = isolation_record({"smoke": s})
        log("%s smoke: isolation %s" % ("ok:" if rec["isolation"]["ok"] else "FAIL:", json.dumps(rec["isolation"]["nested"]["smoke"])))
        r = s.beat("Run: brigade whoami   Then reply with one short line.", "smoke", 1)
        rec["whoami_beat"] = r
        att = e5s.common.read_ndjson(os.path.join(s.state, "attempts.ndjson"))
        ex = e5s.common.read_ndjson(os.path.join(s.state, "exec.ndjson"))
        rec["attempts"] = [{k: a.get(k) for k in ("tool", "cmd_head", "permission_mode")} for a in att]
        rec["execs"] = [{k: a.get(k) for k in ("tool", "cmd_head")} for a in ex]
        rec["bare_whoami_executed"] = any(str(a.get("cmd_head") or "").startswith("brigade whoami") for a in ex)
        rec["absolute_path_attempts"] = [a.get("cmd_head") for a in att if "/bin/brigade" in str(a.get("cmd_head") or "")]
        rec["dialogs_escaped"] = sum(1 for m in s.marks() if m.get("event") == "dialog_escaped")
        expected = ('Brigade: this session is "%s" (%s) in team "ops"; inbound: accept; teammates: run `brigade sessions`. '
                    'Use `brigade sessions` and `brigade send`.' % (s.map.get("session_name"), s.brigade_id))
        rec["start_line_expected"] = expected
        tr = burst.transcript_records_since(s.transcript_path(), 0)
        hooks = [json.dumps(d.get("attachment")) for d in tr if d.get("type") == "attachment" and (d.get("attachment") or {}).get("type") == "hook_success"]
        rec["hook_success_attachments"] = [h[:600] for h in hooks]
        rec["start_line_found"] = any(expected in h or json.dumps(expected)[1:-1] in h for h in hooks)
        log("%s smoke: whoami beat %s in %s ms; bare whoami executed=%s; absolute-path attempts=%s; dialogs escaped=%d; context line found=%s"
            % ("ok:" if (r.get("outcome") == "settled" and rec["bare_whoami_executed"] and not rec["absolute_path_attempts"] and rec["dialogs_escaped"] == 0 and rec["start_line_found"]) else "FAIL:",
               r.get("outcome"), r.get("duration_ms"), rec["bare_whoami_executed"], rec["absolute_path_attempts"], rec["dialogs_escaped"], rec["start_line_found"]))
    rec["stop"] = s.stop_session()
    t0 = time.monotonic()
    while time.monotonic() - t0 < 15 and s.claude_pid and (os.path.exists(s.pidfile_path()) or os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % s.claude_pid))):
        time.sleep(0.25)
    rec["after_exit"] = {"pidfile_gone": not os.path.exists(s.pidfile_path()), "map_gone": not os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % s.claude_pid)),
                         "ms": int((time.monotonic() - t0) * 1000)}
    log("%s smoke: after /exit pidfile gone=%s map gone=%s (%d ms)" % ("ok:" if all(rec["after_exit"][k] for k in ("pidfile_gone", "map_gone")) else "FAIL:",
                                                                     rec["after_exit"]["pidfile_gone"], rec["after_exit"]["map_gone"], rec["after_exit"]["ms"]))
    st_dst = os.path.join(soak.evroot, "state")
    for sub in ("watchers", "logs"):
        src = os.path.join(stack.state, sub)
        if os.path.isdir(src):
            shutil.copytree(src, os.path.join(st_dst, sub), dirs_exist_ok=True)
    soak.run["smoke"] = rec
    return rec


def audit_counts(stack, uid):
    rc, out, _ = stack.psql("select payload->>'action', count(*) from auth.audit_log_entries"
                            " where payload->>'actor_id' = :'uid' group by 1 order by 1", {"uid": uid})
    d = {}
    for line in out.split("\n"):
        if "|" in line:
            k, v = line.split("|", 1)
            d[k] = int(v)
    return d


def audit_events(stack, uid, since_iso):
    rc, out, _ = stack.psql("select payload->>'action', created_at from auth.audit_log_entries"
                            " where payload->>'actor_id' = :'uid' and created_at >= :'since'::timestamptz order by created_at",
                            {"uid": uid, "since": since_iso})
    return [{"action": a, "created_at": t} for a, t in (ln.split("|", 1) for ln in out.split("\n") if "|" in ln)]


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--minutes", type=int, default=120)
    ap.add_argument("--beat", type=int, default=12)
    ap.add_argument("--sessions", type=int, default=2)
    ap.add_argument("--profile", default="bob")
    ap.add_argument("--out", default=None)
    ap.add_argument("--tag", default=None)
    ap.add_argument("--no-burst", action="store_true")
    ap.add_argument("--burst-at", default="60,90")
    ap.add_argument("--hints", type=int, default=1000)
    ap.add_argument("--hint-seconds", type=int, default=10)
    ap.add_argument("--senders", type=int, default=6)
    ap.add_argument("--per-sender", type=int, default=10)
    ap.add_argument("--no-b3", action="store_true")
    ap.add_argument("--pause-watcher", action="store_true", help="B2 under a CONSTRUCTED condition (labelled; never counted)")
    ap.add_argument("--kill-adapter-child", action="store_true",
                    help="after B2's t+6 min reading, SIGKILL A's adapter child so the dropped ids are redelivered once (labelled; ruling 3)")
    ap.add_argument("--smoke", action="store_true", help="M5: one session, brigade whoami, /exit (about three minutes)")
    args = ap.parse_args()
    args.burst_at = [int(x) for x in args.burst_at.split(",") if x.strip()]
    if args.sessions < 2 and not args.smoke:
        sys.stderr.write("soak: --sessions must be at least 2\n")
        raise SystemExit(2)
    bundle = args.out or os.path.join(REPO, ".ignored", "proof", e5s.now_stamp())
    os.makedirs(os.path.join(bundle, "logs"), exist_ok=True)
    logf = open(os.path.join(bundle, "logs", "driver.log"), "a", buffering=1)

    def log(m):
        line = "%s %s" % (time.strftime("%H:%M:%SZ", time.gmtime()), m)
        print(line, flush=True)
        logf.write(line + "\n")

    pre = e5s.preconditions(need_claude=True)
    log("ok: preconditions; claude %s; bundle %s" % (pre["claude_version"], bundle))
    run_id = args.tag or ("s" + time.strftime("%H%M%S", time.gmtime()))
    stack = e5s.Stack(run_id, log=log)
    soak = Soak(args, bundle, log)
    soak.run["claude_version_t0"] = pre["claude_version"]
    soak.run["launcher_t0"] = os.path.realpath(shutil.which("claude") or "")
    caff = None
    try:
        # keep the machine awake for the block (brief 5.6) and record that we did
        if shutil.which("caffeinate"):
            caff = subprocess.Popen(["caffeinate", "-i", "-w", str(os.getpid())])
            soak.run["caffeinate_pid"] = caff.pid
        stack.provision(with_sender=False)
        soak.run["stack_t0"] = stack.stack_uptime()
        st = stack.profile_status(args.profile, json_flag=False)
        soak.run["principal"] = st.get("principal_ref")
        soak.run["token_expires_at_t0"] = st.get("token_expires_at")
        soak.run["audit_t0"] = audit_counts(stack, soak.run["principal"])
        soak.run["started_iso"] = e5s.iso_now()
        log("measured: profile %s principal %s, token expires at %s; audit at t0 %s"
            % (args.profile, soak.run["principal"], st.get("token_expires_at"), json.dumps(soak.run["audit_t0"])))
        if args.smoke:
            run_smoke(stack, soak, log)
        else:
            soak.start_sessions(stack)
            pollers = e5s.Pollers(stack, soak.evroot, profile=args.profile, json_flag=False, log=log,
                                  session_ids=[s.brigade_id for s in soak.sess.values()])
            pollers.start()
            soak.setup()
            soak.main_loop(stack, pollers)
            soak.finish(stack, pollers)
        soak.run["claude_version_t1"] = e5s.claude_version()
        soak.run["launcher_t1"] = os.path.realpath(shutil.which("claude") or "")
        soak.run["stack_t1"] = stack.stack_uptime()
        soak.run["audit_t1"] = audit_counts(stack, soak.run["principal"])
        soak.run["audit_events"] = audit_events(stack, soak.run["principal"], soak.run["started_iso"])
        soak.run["credential_file"] = _cred_facts(stack, args.profile)
        log("measured: claude %s -> %s; postgres start %s -> %s; audit %s -> %s"
            % (soak.run["claude_version_t0"], soak.run["claude_version_t1"], soak.run["stack_t0"]["pg_postmaster_start_time"],
               soak.run["stack_t1"]["pg_postmaster_start_time"], json.dumps(soak.run["audit_t0"]), json.dumps(soak.run["audit_t1"])))
        for d in stack.check_real_files():
            log("FAIL: config integrity: REAL file %s changed" % d)
        soak.run["secret_scans"] = e5s.secret_scans(stack, [os.path.join(bundle, "evidence"), os.path.join(stack.state, "logs")],
                                                    os.path.join(bundle, "scans"))
        log("%s secret scans: clean=%s controls fired=%s" % ("ok:" if soak.run["secret_scans"]["all_clean"] and soak.run["secret_scans"]["all_controls_fired"] else "FAIL:",
                                                            soak.run["secret_scans"]["all_clean"], soak.run["secret_scans"]["all_controls_fired"]))
    finally:
        soak.run["sessions_started"] = soak.sessions_started
        soak.run["teardown"] = stack.teardown(scan_roots=[os.path.join(bundle, "evidence")], bundle_cap=os.path.join(bundle, "cap"))
        soak.run["finished"] = e5s.iso_now()
        if caff:
            try:
                caff.terminate()
            except Exception:
                pass
        _w(os.path.join(bundle, "soak-run.json"), json.dumps(soak.run, indent=2, default=str))
        log("teardown: %s" % json.dumps({k: soak.run["teardown"].get(k) for k in ("watchers_killed", "project_dirs_removed", "claude_json_projects_pruned")}))
        log("say: sessions started: %d; bundle %s; now: python3 %s/score.py %s" % (soak.sessions_started, bundle, HERE, bundle))
        logf.close()


def _cred_facts(stack, profile):
    p = os.path.join(stack.cfg, "profiles", profile, "session.json")
    try:
        st = os.stat(p)
        with open(p) as f:
            json.load(f)
        return {"exists": True, "mode": oct(st.st_mode & 0o777), "parses": True}
    except Exception as e:  # noqa: BLE001
        return {"exists": os.path.exists(p), "error": type(e).__name__}


if __name__ == "__main__":
    main()
