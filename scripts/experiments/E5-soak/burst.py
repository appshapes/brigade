#!/usr/bin/env python3
"""E5-soak burst (plan row P5-11, E2E-13): the two burst phases, measured
separately because they are carried by two different mechanisms (brief 4.5,
5.2), plus the optional sustained arm.

    python3 scripts/experiments/E5-soak/burst.py [--hints 1000] [--hint-seconds 10] [--senders 6] [--per-sender 10]
                                                 [--target <brigade session id> | --provision] [--out <bundle>]
                                                 [--no-b3] [--b3-minutes 5] [--b3-per-minute 20] [--redeliver-minutes 6]
                                                 [--pause-watcher]

  B1  "1,000 realtime hints in 10 s": the shipped trigger's own realtime.send
      call from SQL (hints.sql) with fabricated ids, 10 batches of 100 one
      second apart, against session A's topic with A's inbox empty. Measures
      the drain count (pg_stat_statements, against a background rate taken
      just before), zero `message` events / injections / acks / notices, the
      session answering an ordinary probe beat (`brigade whoami`) DURING
      (t+5 s) and AFTER (t+60 s) -- not the READY canary: its third use in a
      session was refused by the provider's safeguard (2026-09-05) -- ps
      samples across the window, and that the channel stayed joined.
  B2  "injection bounded, one summarised drop notice": six synthetic
      hook-registered senders on TWO principals (alice x3, dana x3), 10
      distinct bodies each, all sends issued concurrently. Measures the
      server's acceptance (60, 0 recipient_inbox_full), the watcher's
      `injection queue full` Warn lines and dropped_total, the exact
      DropNotice text in the transcript (count must be 1), zero rate notices,
      injected frames per sender per minute <= 10 (transcript, cross-checked
      against `ack sent`), and the dropped ids' fate on the server at
      t+redeliver-minutes (still `accepted`; injected how many times).
  B3  optional: ~20 sends/min across the six senders for 5 minutes, starting
      60 s after B2 so the per-sender buckets have rolled; dropped_total and
      the notice count over the window.

Standalone (`--provision`, the default): provisions alice/bob/dana, spawns ONE
interactive pty session as bob (the e5s engine), runs B1, B2, B3 against it,
tears down -- about ten minutes, one Claude session. `soak.py` calls the same
phase functions in-process against its session A at t=60 and t=90 min.

`--target <id>` runs B1 only against an already-registered session whose
watcher is live (no pty, so no canaries; recorded as not run).

`--pause-watcher` is a CONSTRUCTED condition, off by default and labelled in
every record it touches: the watcher process (never the adapter child) is
SIGSTOPped while the 60 sends land and SIGCONTed afterwards, so the reader
offers one drain page in a tight loop -- the "injector slower than the
arrivals" case stated as a rig intervention, never as the shipped race.
A bundle made with it is never counted toward acceptance (score.py marks
clause 10 `counted: false`).

`--kill-adapter-child` is the LABELLED arm of clause 10's second half
(driver ruling 3 of 2026-09-05): the adapter's `message watch` emits each id
at most once per process (supabase/watch.go `seen`), so a queue-dropped id
is not re-emitted while the same child lives. After the t+redeliver-minutes
reading, the target watcher's adapter child is SIGKILLed; the shipped
watcher supervision (supervise.go) respawns it; the new child's seen map is
empty, so exactly the dropped ids -- still `accepted` on the server -- are
re-emitted, offered, injected ONCE and acknowledged. The construction is
named in every record it touches; nothing else is touched. With no dropped
ids the arm records `not_run`.
"""
import argparse
import json
import os
import re
import signal
import subprocess
import sys
import threading
import time

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import e5s      # noqa: E402
import common   # noqa: E402

REPO = e5s.REPO
DROP_NOTICE_RE = re.compile(r"Brigade: (\d+ messages|1 message) dropped from the injection queue \(limit 50\); "
                            r"they remain on the server and will be delivered later")
RATE_NOTICE_RE = re.compile(r"Brigade: (\d+ messages|1 message) from [^\n]+ held back for rate limiting; they will be delivered later")
FRAME_ID_RE = re.compile(r'<brigade-message team="[^"]*" message-id="([0-9a-f-]{36})" reply-to-session-id="([0-9a-f-]{36})"')


def _w(path, content):
    os.makedirs(os.path.dirname(path), exist_ok=True)
    with open(path, "w") as f:
        f.write(content)


def iso_ms(ms):
    return time.strftime("%Y-%m-%dT%H:%M:%S", time.gmtime(ms / 1000.0)) + ".%03dZ" % (ms % 1000)


# --------------------------------------------------------------------------- #
# log readers (watcher log: NDJSON with RFC3339 `time`; transcript: JSONL)
# --------------------------------------------------------------------------- #
def log_lines_since(path, since_ms):
    out = []
    if not os.path.exists(path):
        return out
    with open(path, errors="replace") as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            t = d.get("time")
            if t and _parse_iso_ms(t) >= since_ms:
                out.append(d)
    return out


def _parse_iso_ms(s):
    """RFC3339 with fractional seconds and an offset -> epoch ms."""
    m = re.match(r"^(\d{4})-(\d\d)-(\d\d)T(\d\d):(\d\d):(\d\d)(?:\.(\d+))?(Z|[+-]\d\d:\d\d)$", s)
    if not m:
        return 0
    import calendar
    y, mo, d, h, mi, se = (int(x) for x in m.groups()[:6])
    frac = (m.group(7) or "0")[:3].ljust(3, "0")
    base = calendar.timegm((y, mo, d, h, mi, se))
    off = 0
    tz = m.group(8)
    if tz != "Z":
        sign = 1 if tz[0] == "+" else -1
        off = sign * (int(tz[1:3]) * 3600 + int(tz[4:6]) * 60)
    return (base - off) * 1000 + int(frac)


def pg_ms(s):
    """psql -At timestamptz ('2026-09-05 19:08:10.123456+00') -> epoch ms; RFC3339 passes through."""
    m = re.match(r"^(\d{4}-\d\d-\d\d)[ T](\d\d:\d\d:\d\d)(\.\d+)?([+-]\d\d)(?::?(\d\d))?$", s or "")
    if not m:
        return _parse_iso_ms(s or "")
    return _parse_iso_ms("%sT%s%s%s:%s" % (m.group(1), m.group(2), m.group(3) or "", m.group(4), m.group(5) or "00"))


def transcript_records_since(path, since_ms):
    out = []
    if not path or not os.path.exists(path):
        return out
    with open(path, errors="replace") as f:
        for line in f:
            try:
                d = json.loads(line)
            except ValueError:
                continue
            t = d.get("timestamp")
            if t and _parse_iso_ms(t) >= since_ms:
                out.append(d)
    return out


def user_texts(records):
    """Every text a user-role record carries (string content, text blocks,
    and the peer `origin.body`), with the record's timestamp."""
    out = []
    for d in records:
        if d.get("type") != "user":
            continue
        ts = d.get("timestamp")
        m = d.get("message") or {}
        c = m.get("content")
        if isinstance(c, str):
            out.append((ts, c))
        elif isinstance(c, list):
            for b in c:
                if isinstance(b, dict) and b.get("type") == "text":
                    out.append((ts, b.get("text") or ""))
        ob = (d.get("origin") or {}).get("body")
        if isinstance(ob, str):
            out.append((ts, ob))
    return out


def frames_in(records):
    """{message_id: (first timestamp, sender_session_id, occurrences, via)}
    over the user-role texts -- counted by ID, never by record (P4-4: Claude
    Code batches a backlog into one record; and the same frame may appear in
    both the content and origin.body of one record, which is one injection)
    -- plus the frames Claude Code delivered through its message QUEUE: a
    frame posted while a turn is in flight is written as a
    `queue-operation` `enqueue` record (its `content` carries the frame) and
    later as a `queued_command` attachment, never as a user record
    (measured 2026-09-05, the deliverable's B2: 1 user record, 50 enqueues,
    9 posts never recorded at all). One enqueue is one delivery, whatever
    else refers to the same id."""
    seen = {}
    queued = {}
    for d in records:
        ts = d.get("timestamp")
        if d.get("type") == "queue-operation" and d.get("operation") == "enqueue":
            for mid, sender in FRAME_ID_RE.findall(d.get("content") or ""):
                if mid not in queued:
                    queued[mid] = {"first": ts, "sender": sender}
            continue
        if d.get("type") != "user":
            continue
        ids_here = set()
        for _, text in user_texts([d]):
            for mid, sender in FRAME_ID_RE.findall(text):
                ids_here.add((mid, sender))
        for mid, sender in ids_here:
            if mid in seen:
                seen[mid]["occurrences"] += 1
            else:
                seen[mid] = {"first": ts, "sender": sender, "occurrences": 1, "via": "user"}
    # an enqueue followed by a user record is ONE delivery (enqueue -> dequeue -> the user record, measured for
    # every frame that arrived mid-turn and was then delivered directly); the queue path counts only for an id
    # that never became a user record (delivered into the turn as a queued_command attachment)
    for mid, q in queued.items():
        if mid in seen:
            seen[mid]["via"] = seen[mid]["via"] + "+enqueued"
        else:
            seen[mid] = {"first": q["first"], "sender": q["sender"], "occurrences": 1, "via": "queue"}
    return seen


def notices_in(records, rx):
    """The notices (bare text, unframed) in the user-role texts AND in the
    `queue-operation` enqueue records: a notice posted while a turn is in
    flight is queued like any other post and never becomes a user record
    (measured 2026-09-05, the constructed arm)."""
    out = []
    for ts, text in user_texts(records):
        for m in rx.finditer(text):
            out.append({"timestamp": ts, "text": m.group(0)})
    for d in records:
        if d.get("type") == "queue-operation" and d.get("operation") == "enqueue":
            for m in rx.finditer(d.get("content") or ""):
                out.append({"timestamp": d.get("timestamp"), "text": m.group(0), "via": "queue"})
    # one record may carry the notice in content and origin.body: dedupe by (ts, text)
    uniq = []
    seen = set()
    for n in out:
        k = (n["timestamp"], n["text"])
        if k not in seen:
            seen.add(k)
            uniq.append(n)
    return uniq


# --------------------------------------------------------------------------- #
# B1: the hints
# --------------------------------------------------------------------------- #
def phase_hints(stack, sess, target_id, results, hints, seconds, log, watcher_log=None, adapter_log=None):
    os.makedirs(results, exist_ok=True)
    rec = {"phase": "B1", "target": target_id, "hints": hints, "seconds": seconds, "canaries": {}}
    inbox = stack.inbox_state(target_id)
    rec["inbox_before"] = inbox
    if inbox.get("accepted"):
        log("FAIL: B1: A's inbox is not empty before the hints (%s)" % inbox)
    # the background drain rate, 30 s, right before
    p0 = stack.pss_calls("fetch_inbox")
    time.sleep(30)
    p1 = stack.pss_calls("fetch_inbox")
    rec["background_drains_per_30s"] = (p1 - p0) if (p0 is not None and p1 is not None) else None
    log("measured: B1 background fetch_inbox calls over 30 s: %s" % rec["background_drains_per_30s"])
    rt0 = int(stack.realtime_rows(target_id) or 0)
    wl0 = len(log_lines_since(watcher_log, 0)) if watcher_log else 0
    stack.ps_sample("b1-before")
    # ONE psql script: hints.sql ten times with `pg_sleep(seconds/10)` between the batches, so the pacing is the
    # server's own clock and the "in 10 s" is real. Ten separate `docker exec` invocations cost ~1.6 s of
    # startup each and stretched 100 hints over 16 s in M7 (2026-09-05); the span the hints actually land in
    # is the first batch's t0 to the last batch's t1 (clock_timestamp()), recorded as sql_span_ms.
    with open(e5s.HINTS_SQL) as f:
        unit = f.read()
    gap = seconds / 10.0
    script = ""
    for k in range(10):
        script += unit + "\n"
        if k < 9:
            script += "select 'sleep', pg_sleep(%s);\n" % gap
    rendered = script.replace(":'sid'", "'%s'" % target_id).replace(":n", str(hints // 10))
    _w(os.path.join(results, "hints.rendered.sql"), rendered)
    t0 = e5s.now_ms()
    rec["t0_ms"] = t0
    batches = []
    fire_rec = {}
    ps_rows = []
    stop_ps = threading.Event()

    def ps_loop():
        while not stop_ps.is_set():
            ps_rows.append(stack.ps_sample())
            stop_ps.wait(5)

    pt = threading.Thread(target=ps_loop, daemon=True)
    pt.start()

    def fire():
        tk = e5s.now_ms()
        rc, out, err = stack.psql(script, {"sid": target_id, "n": str(hints // 10)}, timeout=seconds + 180)
        _w(os.path.join(results, "psql.out"), "rc=%d\n--- stdout ---\n%s\n--- stderr ---\n%s" % (rc, out, err))
        fire_rec.update({"issued_ms": tk, "done_ms": e5s.now_ms(), "rc": rc})
        cur = {}
        for line in out.split("\n"):
            if line.startswith("t0|"):
                cur = {"k": len(batches) + 1, "rc": rc, "t0_server": line[3:], "issued_ms": pg_ms(line[3:])}
            elif line.startswith("sent|"):
                try:
                    cur["sent"] = int(line[5:])
                except ValueError:
                    cur["sent"] = None
            elif line.startswith("t1|"):
                cur["t1_server"] = line[3:]
                cur["done_ms"] = pg_ms(line[3:])
                batches.append(cur)
                cur = {}

    ft = threading.Thread(target=fire, daemon=True)
    ft.start()
    # the responsiveness probe DURING, submitted at t+5 s (brief 4.5 B1 (c)): an ordinary beat (e5s.PROBE_PROMPT),
    # its rtt the first real assistant record's timestamp minus the submit mark -- never the READY canary again (see
    # e5s.PROBE_PROMPT: the third READY canary of a session was refused by the provider's safeguard, 2 of 2)
    if sess is not None:
        wait = (t0 + 5000 - e5s.now_ms()) / 1000.0
        if wait > 0:
            time.sleep(wait)
        c1 = sess.probe("B1 during")
        c1["submitted_at_ms"] = t0 + 5000
        rec["canaries"]["during"] = c1
        log("measured: B1 probe DURING the burst: ok=%s first reply after %s ms (settled %s in %s ms, refusals %s)"
            % (c1.get("ok"), c1.get("rtt_ms"), c1.get("outcome"), c1.get("settle_ms"), c1.get("provider_refusals")))
    ft.join(timeout=seconds + 200)
    t_end = e5s.now_ms()
    rec["batches"] = batches
    rec["fire_wall_ms"] = (fire_rec.get("done_ms") or t_end) - t0     # the psql's own wall, not the probe's wait
    rec["psql"] = fire_rec
    rec["sql_span_ms"] = (batches[-1]["done_ms"] - batches[0]["issued_ms"]) if len(batches) >= 2 else None
    rec["sent_total"] = sum((b.get("sent") or 0) for b in batches)
    time.sleep(3)
    p2 = stack.pss_calls("fetch_inbox")
    rec["drains_within_3s_of_end"] = (p2 - p1) if (p1 is not None and p2 is not None) else None
    if sess is not None:
        wait = (t0 + 60000 - e5s.now_ms()) / 1000.0
        if wait > 0:
            time.sleep(wait)
        c2 = sess.probe("B1 after")
        c2["submitted_at_ms"] = t0 + 60000
        rec["canaries"]["after"] = c2
        log("measured: B1 probe AFTER the burst (t+60 s): ok=%s first reply after %s ms (settled %s in %s ms, refusals %s)"
            % (c2.get("ok"), c2.get("rtt_ms"), c2.get("outcome"), c2.get("settle_ms"), c2.get("provider_refusals")))
    else:
        wait = (t0 + 30000 - e5s.now_ms()) / 1000.0
        if wait > 0:
            time.sleep(wait)
        rec["canaries"] = {"not_run": "no pty session (--target mode)"}
    stop_ps.set()
    pt.join(timeout=10)
    p3 = stack.pss_calls("fetch_inbox")
    rec["drains_by_end_of_window"] = (p3 - p1) if (p1 is not None and p3 is not None) else None
    rec["fetch_inbox_calls"] = {"before_bg": p0, "at_t0": p1, "end_plus_3s": p2, "window_end": p3}
    rt1 = int(stack.realtime_rows(target_id) or 0)
    rec["realtime_rows_on_topic"] = [rt0, rt1]
    rec["inbox_after"] = stack.inbox_state(target_id)
    stack.ps_sample("b1-after")
    with open(os.path.join(results, "ps.ndjson"), "w") as f:
        for r in ps_rows:
            f.write(json.dumps(r) + "\n")
    if watcher_log:
        sl = log_lines_since(watcher_log, t0 - 1000)
        _w(os.path.join(results, "watcher.slice.log"), "".join(json.dumps(d) + "\n" for d in sl))
        rec["watcher_slice"] = {
            "lines": len(sl), "lines_before": wl0,
            "ack_sent": sum(1 for d in sl if d.get("msg") == "ack sent"),
            "message_offered": sum(1 for d in sl if d.get("msg") == "message offered"),
            "watch_status": [{"time": d.get("time"), "state": d.get("state"), "detail": d.get("detail")} for d in sl if d.get("msg") == "watch status"],
            "warn_error": [d for d in sl if d.get("level") in ("WARN", "ERROR")][:20],
            "child_failed": sum(1 for d in sl if d.get("msg") == "watch child failed; restarting"),
        }
    if adapter_log:
        al = log_lines_since(adapter_log, t0 - 1000)
        _w(os.path.join(results, "adapter.slice.log"), "".join(json.dumps(d) + "\n" for d in al))
        rec["adapter_slice"] = {"lines": len(al), "warn_error": [d for d in al if d.get("level") in ("WARN", "ERROR")][:20]}
    if sess is not None:
        tr = sess.transcript_path()
        recs = transcript_records_since(tr, t0)
        rec["transcript_slice"] = {"records": len(recs), "frames": len(frames_in(recs)),
                                   "drop_notices": len(notices_in(recs, DROP_NOTICE_RE)),
                                   "rate_notices": len(notices_in(recs, RATE_NOTICE_RE))}
    ok = (rec["sent_total"] == hints and rt1 - rt0 == hints
          and (rec.get("watcher_slice", {}).get("ack_sent", 0) == 0)
          and (rec.get("watcher_slice", {}).get("child_failed", 0) == 0)
          and (rec["drains_within_3s_of_end"] or 0) >= 1
          and all(c.get("ok") for c in rec["canaries"].values() if isinstance(c, dict) and "ok" in c))
    rec["ok"] = ok
    log("%s B1: %d hints fired in %.2f s wall (server span %s ms; %d/%d batches rc=0), realtime rows +%d, fetch_inbox calls +%s (3 s) / +%s (window) vs background %s/30 s, "
        "ack sent %s, canaries %s"
        % ("ok:" if ok else "FAIL:", rec["sent_total"], rec["fire_wall_ms"] / 1000.0, rec["sql_span_ms"], sum(1 for b in batches if b["rc"] == 0), len(batches),
           rt1 - rt0, rec["drains_within_3s_of_end"], rec["drains_by_end_of_window"], rec["background_drains_per_30s"],
           rec.get("watcher_slice", {}).get("ack_sent"), {k: (v.get("ok"), v.get("rtt_ms")) for k, v in rec["canaries"].items() if isinstance(v, dict)}))
    _w(os.path.join(results, "b1.json"), json.dumps(rec, indent=2, default=str))
    return rec


# --------------------------------------------------------------------------- #
# B2: the messages
# --------------------------------------------------------------------------- #
def register_burst_senders(stack, senders, log):
    recs = []
    principals = ["alice", "dana"]
    for i in range(senders):
        profile = principals[i % len(principals)]
        r = stack.register_sender(profile, "burst-%s-%d" % (profile, i // len(principals) + 1))
        recs.append(r)
    log("ok: registered %d burst senders: %s" % (len(recs), ", ".join("%s(%s)" % (r["title"], r["session_id"][:8]) for r in recs)))
    return recs


def watcher_pid_for(stack, claude_pid):
    p = os.path.join(stack.state, "watchers", "%s.json" % claude_pid)
    try:
        with open(p) as f:
            return int(json.load(f).get("pid") or 0)
    except Exception:
        return 0


def adapter_child_of(watcher_pid):
    """The `brigade adapter supabase ... message watch` child of a watcher,
    through `pgrep -P` (never `ps e`/`-E`/`eww`: the watcher's environment
    carries the messaging token)."""
    if not watcher_pid:
        return 0
    r = subprocess.run(["pgrep", "-P", str(watcher_pid), "-f", "adapter supabase"], capture_output=True, text=True)
    pids = [int(x) for x in r.stdout.split() if x.strip().isdigit()]
    return pids[0] if pids else 0


def kill_adapter_child_arm(stack, sess, target_id, dropped_ids, watcher_log, wpid, t0, log,
                           wait_restart=120, wait_redeliver=240):
    """The LABELLED arm of clause 10's second half (driver ruling 3 of
    2026-09-05). Construction: SIGKILL of the target watcher's adapter child;
    the shipped supervision respawns it (`watch child failed; restarting`,
    then a new `watch ready`); the new child's per-process seen map is empty
    so the queue-dropped ids -- still `accepted` on the server -- are
    re-emitted, offered, injected once and acknowledged. Measured: the
    restart latency, every dropped id's occurrences in the transcript since
    t0 (must be exactly 1), its server state afterwards (`injected`), the
    `ack sent` ids after the kill. Nothing else is touched."""
    rec = {"constructed": True,
           "construction": "SIGKILL of the adapter child (`brigade adapter supabase ... message watch`) of the target's "
                           "watcher after the t+redeliver reading; the shipped watcher supervision (supervise.go) respawned "
                           "it; the watcher, the session, the server and the other watcher were not touched"}
    if not dropped_ids:
        rec["not_run"] = "no dropped ids to redeliver"
        return rec
    if sess is None or not wpid:
        rec["not_run"] = "no pty session or no watcher pid"
        return rec
    child = adapter_child_of(wpid)
    if not child:
        rec["not_run"] = "no adapter child found under watcher %d" % wpid
        return rec
    wl_before = log_lines_since(watcher_log, 0) if watcher_log else []
    tr = sess.transcript_path()
    fr0 = frames_in(transcript_records_since(tr, t0))
    rec.update({
        "watcher_pid": wpid, "child_pid": child,
        "watch_ready_lines_before": sum(1 for d in wl_before if d.get("msg") == "watch ready"),
        "child_failed_lines_before": sum(1 for d in wl_before if d.get("msg") == "watch child failed; restarting"),
        "occurrences_before": {m: (fr0.get(m) or {}).get("occurrences", 0) for m in dropped_ids},
        "server_state_before": {d["message_id"]: d["delivery_state"]
                                for d in stack.message_rows(target_id, since_iso=iso_ms(t0 - 2000)) if d["message_id"] in dropped_ids},
    })
    t_kill = e5s.now_ms()
    os.kill(child, signal.SIGKILL)
    rec["kill_ms"] = t_kill
    rec["kill_at_s_after_t0"] = round((t_kill - t0) / 1000.0, 1)
    log("say: B2 KILL ARM (constructed, labelled; ruling 3): SIGKILL adapter child %d of watcher %d at t+%.1f s; %d dropped ids await redelivery"
        % (child, wpid, (t_kill - t0) / 1000.0, len(dropped_ids)))
    # count-based, never time-based: the log's timestamps are the watcher's clock, and a tolerance window would
    # match the ORIGINAL `watch ready` line (measured in the no-model killcheck of 2026-09-05)
    ready_ms = None
    failed_line = None
    ready_before = rec["watch_ready_lines_before"]
    failed_before = rec["child_failed_lines_before"]
    deadline = time.monotonic() + wait_restart
    while time.monotonic() < deadline:
        wl = log_lines_since(watcher_log, 0) if watcher_log else []
        fails = [d for d in wl if d.get("msg") == "watch child failed; restarting"]
        readies = [d for d in wl if d.get("msg") == "watch ready"]
        if len(fails) > failed_before and failed_line is None:
            failed_line = {k: fails[failed_before].get(k) for k in ("time", "code", "why", "exit", "delay", "consecutive_failures")}
        if len(readies) > ready_before:
            ready_ms = _parse_iso_ms(readies[-1]["time"])
            break
        time.sleep(0.5)
    rec["restart"] = {"child_failed_line": failed_line, "watch_ready_ms": ready_ms,
                      "restart_latency_ms": (ready_ms - t_kill) if ready_ms else None,
                      "new_child_pid": adapter_child_of(wpid)}
    log("measured: kill arm: the watcher restarted the child: %s; new `watch ready` %s ms after the kill (new child pid %s)"
        % (json.dumps(failed_line), rec["restart"]["restart_latency_ms"], rec["restart"]["new_child_pid"]))
    deadline = time.monotonic() + wait_redeliver
    occ = {}
    t_seen = None
    while time.monotonic() < deadline:
        fr = frames_in(transcript_records_since(tr, t0))
        occ = {m: (fr.get(m) or {}).get("occurrences", 0) for m in dropped_ids}
        if all(v >= 1 for v in occ.values()):
            t_seen = e5s.now_ms()
            break
        time.sleep(2)
    time.sleep(15)   # let the acks land before the server is read
    fr = frames_in(transcript_records_since(tr, t0))
    occ = {m: (fr.get(m) or {}).get("occurrences", 0) for m in dropped_ids}
    server_after = {d["message_id"]: d["delivery_state"]
                    for d in stack.message_rows(target_id, since_iso=iso_ms(t0 - 2000)) if d["message_id"] in dropped_ids}
    wl_after = log_lines_since(watcher_log, t_kill) if watcher_log else []
    rec["redelivery"] = {
        "occurrences_after": occ,
        "all_injected_exactly_once": bool(occ) and all(v == 1 for v in occ.values()),
        "redelivered_by_ms": t_seen, "latency_from_kill_ms": (t_seen - t_kill) if t_seen else None,
        "server_state_after": server_after,
        "all_injected_on_server": bool(server_after) and len(server_after) == len(dropped_ids)
                                  and all(v == "injected" for v in server_after.values()),
        "ack_ids_after_kill": sum(int(d.get("ids", 0)) for d in wl_after if d.get("msg") == "ack sent"),
        "inbox_after": stack.inbox_state(target_id),
        "frames_injected_more_than_once_since_t0": sorted(m for m, v in fr.items() if v["occurrences"] > 1),
    }
    rec["ok"] = bool(ready_ms) and rec["redelivery"]["all_injected_exactly_once"] and rec["redelivery"]["all_injected_on_server"]
    log("%s kill arm: %d/%d dropped ids injected exactly once after the child restart (%s ms after the kill); server states %s; ack ids after the kill %d; frames twice since t0: %s"
        % ("ok:" if rec["ok"] else "FAIL:", sum(1 for v in occ.values() if v == 1), len(dropped_ids), rec["redelivery"]["latency_from_kill_ms"],
           sorted(set(server_after.values())) or "-", rec["redelivery"]["ack_ids_after_kill"], rec["redelivery"]["frames_injected_more_than_once_since_t0"]))
    return rec


def phase_messages(stack, sess, target_id, results, senders, per_sender, log, watcher_log=None,
                   sender_recs=None, pause_watcher=False, redeliver_minutes=6, kill_adapter_child=False):
    os.makedirs(results, exist_ok=True)
    rec = {"phase": "B2", "target": target_id, "senders": senders, "per_sender": per_sender,
           "pause_watcher": pause_watcher, "constructed_condition": bool(pause_watcher)}
    recs = sender_recs or register_burst_senders(stack, senders, log)
    _w(os.path.join(results, "senders.json"), json.dumps([{k: r[k] for k in ("profile", "title", "pid", "session_id")} for r in recs], indent=2))
    rec["inbox_before"] = stack.inbox_state(target_id)
    tr = sess.transcript_path() if sess else None
    frames_before = frames_in(transcript_records_since(tr, 0)) if tr else {}
    wl_before = log_lines_since(watcher_log, 0) if watcher_log else []
    dropped_total_before = max([d.get("dropped_total", 0) for d in wl_before if d.get("msg") == "injection queue full; oldest message dropped"] or [0])
    stack.ps_sample("b2-before")
    wpid = watcher_pid_for(stack, sess.claude_pid) if sess else 0
    t0 = e5s.now_ms()
    rec["t0_ms"] = t0
    if pause_watcher and wpid:
        os.kill(wpid, signal.SIGSTOP)
        rec["watcher_paused_pid"] = wpid
        log("say: B2 CONSTRUCTED CONDITION: watcher %d SIGSTOPped while the sends land" % wpid)
    sends = []
    lock = threading.Lock()

    def run_sender(r):
        for j in range(per_sender):
            body = "BURST-%s-%d-%s" % (r["title"], j + 1, e5s.rand_hex(3))
            s = stack.send_from(r, target_id, body)
            s["j"] = j + 1
            with lock:
                sends.append(s)

    ths = [threading.Thread(target=run_sender, args=(r,), daemon=True) for r in recs]
    for t in ths:
        t.start()
    for t in ths:
        t.join(timeout=300)
    t_sent = e5s.now_ms()
    if pause_watcher and wpid:
        time.sleep(1)
        os.kill(wpid, signal.SIGCONT)
        rec["watcher_resumed_ms"] = e5s.now_ms()
    with open(os.path.join(results, "sends.ndjson"), "w") as f:
        for s in sorted(sends, key=lambda x: x["sent_at_ms"]):
            f.write(json.dumps(s, default=str) + "\n")
    accepted = [s for s in sends if s.get("accepted")]
    refused = [s for s in sends if not s.get("accepted")]
    codes = {}
    for s in refused:
        c = ((s.get("result") or {}).get("error") or {}).get("code") or "?"
        codes[c] = codes.get(c, 0) + 1
    rec["sends"] = {"issued": len(sends), "accepted": len(accepted), "refused": len(refused), "refusal_codes": codes,
                    "issue_wall_ms": t_sent - t0}
    log("measured: B2 %d sends issued concurrently in %d ms: %d accepted, %d refused %s"
        % (len(sends), t_sent - t0, len(accepted), len(refused), codes or ""))
    # let the injector and Claude Code settle: 60 s, then read everything
    time.sleep(60)
    stack.ps_sample("b2-after")
    sent_ids = {s["message_id"]: s for s in accepted if s.get("message_id")}
    wl = log_lines_since(watcher_log, t0 - 1000) if watcher_log else []
    _w(os.path.join(results, "watcher.slice.log"), "".join(json.dumps(d) + "\n" for d in wl))
    drops = [d for d in wl if d.get("msg") == "injection queue full; oldest message dropped"]
    dropped_ids = [d.get("dropped_message_id") for d in drops]
    dropped_total = max([d.get("dropped_total", 0) for d in drops] or [dropped_total_before])
    held = [d for d in wl if d.get("msg") == "message held back for rate limiting"]
    deferred = [d for d in wl if d.get("msg") == "identical body deferred"]
    acks = [d for d in wl if d.get("msg") == "ack sent"]
    ack_ids = sum(int(d.get("ids", 0)) for d in acks)
    inj_fail = [d for d in wl if d.get("msg") == "injection failed; backing off"]
    rec["watcher"] = {"drop_lines": len(drops), "dropped_ids": dropped_ids, "dropped_total_before": dropped_total_before,
                      "dropped_total": dropped_total, "held_back_lines": len(held), "deferred_lines": len(deferred),
                      "ack_sent_lines": len(acks), "ack_ids_sum": ack_ids, "injection_failed": len(inj_fail),
                      "note": "message queued/injected are DEBUG lines and unreachable at the shipped INFO level; "
                              "queued = accepted - dropped is INFERRED, injected is counted in the transcript and by ack ids"}
    recs_tr = transcript_records_since(tr, t0) if tr else []
    _w(os.path.join(results, "transcript.slice.jsonl"), "".join(json.dumps(d) + "\n" for d in recs_tr))
    fr = frames_in(recs_tr)
    fr_burst = {mid: v for mid, v in fr.items() if mid in sent_ids}
    per_sender_min = {}
    for mid, v in fr_burst.items():
        minute = _parse_iso_ms(v["first"]) // 60000 if v.get("first") else 0
        key = (v["sender"], minute)
        per_sender_min[key] = per_sender_min.get(key, 0) + 1
    drop_notices = notices_in(recs_tr, DROP_NOTICE_RE)
    rate_notices = notices_in(recs_tr, RATE_NOTICE_RE)
    _w(os.path.join(results, "notices.txt"), "".join("%s\t%s\n" % (n["timestamp"], n["text"]) for n in drop_notices + rate_notices))
    rec["transcript"] = {"records": len(recs_tr), "burst_frames_injected": len(fr_burst),
                         "frames_injected_more_than_once": sorted(m for m, v in fr_burst.items() if v["occurrences"] > 1),
                         "max_frames_per_sender_per_minute": max(per_sender_min.values() or [0]),
                         "per_sender_minute": {"%s@%d" % k: v for k, v in per_sender_min.items()},
                         "drop_notices": drop_notices, "rate_notices": rate_notices}
    server_rows = stack.message_rows(target_id, since_iso=iso_ms(t0 - 2000))
    by_state = {}
    for r in server_rows:
        by_state[r["delivery_state"]] = by_state.get(r["delivery_state"], 0) + 1
    rec["server_after_60s"] = {"rows": len(server_rows), "by_state": by_state,
                               "dropped_ids_state": {d["message_id"]: d["delivery_state"] for d in server_rows if d["message_id"] in dropped_ids}}
    log("measured: B2 after 60 s: drops=%d (dropped_total %d -> %d), held-back=%d, deferred=%d, ack ids=%d, frames injected=%d/%d, "
        "max per sender per minute=%d, drop notices=%d, rate notices=%d, server rows by state %s"
        % (len(drops), dropped_total_before, dropped_total, len(held), len(deferred), ack_ids, len(fr_burst), len(sent_ids),
           rec["transcript"]["max_frames_per_sender_per_minute"], len(drop_notices), len(rate_notices), by_state))
    if drop_notices:
        log("measured: B2 the drop notice, verbatim: %s (at %s)" % (drop_notices[0]["text"], drop_notices[0]["timestamp"]))
    # the dropped ids' fate at t+redeliver_minutes: the adapter's per-process seen map means NO re-emit while the
    # same watch child lives (supabase/watch.go `seen`); measured, never assumed
    wait = (t0 + redeliver_minutes * 60000 - e5s.now_ms()) / 1000.0
    if wait > 0:
        time.sleep(wait)
    rows_later = stack.message_rows(target_id, since_iso=iso_ms(t0 - 2000))
    later_state = {d["message_id"]: d["delivery_state"] for d in rows_later if d["message_id"] in dropped_ids}
    recs_later = transcript_records_since(tr, t0) if tr else []
    fr_later = frames_in(recs_later)
    rec["dropped_after_%dmin" % redeliver_minutes] = {
        "server_state": later_state,
        "injected_occurrences": {m: (fr_later.get(m) or {}).get("occurrences", 0) for m in dropped_ids},
        "inbox": stack.inbox_state(target_id),
        "watcher_child_restarts": sum(1 for d in log_lines_since(watcher_log, t0) if d.get("msg") == "watch child failed; restarting") if watcher_log else None,
    }
    rec["inbox_after"] = stack.inbox_state(target_id)
    ok = (len(accepted) == senders * per_sender and not refused and len(rate_notices) == 0
          and rec["transcript"]["max_frames_per_sender_per_minute"] <= 10 and not rec["transcript"]["frames_injected_more_than_once"]
          and len(drop_notices) == (1 if drops else 0) and len(inj_fail) == 0)
    rec["bound_reached"] = bool(drops)
    rec["ok"] = ok
    log("%s B2: accepted %d/%d, bound reached=%s (dropped %d), notices drop=%d rate=%d, dropped ids at t+%d min: server %s, injected %s"
        % ("ok:" if ok else "FAIL:", len(accepted), senders * per_sender, bool(drops), len(dropped_ids), len(drop_notices), len(rate_notices),
           redeliver_minutes, sorted(set(later_state.values())) or "-", sorted(set(rec["dropped_after_%dmin" % redeliver_minutes]["injected_occurrences"].values())) or "-"))
    # the labelled second half of clause 10 (ruling 3): the adapter child killed, the dropped ids redelivered once.
    # b2.json is written BEFORE the arm and again after it, and an arm failure is recorded, never raised.
    rec["kill_adapter_child"] = {"not_run": "flag off"} if not kill_adapter_child else {"pending": True}
    _w(os.path.join(results, "b2.json"), json.dumps(rec, indent=2, default=str))
    if kill_adapter_child:
        try:
            rec["kill_adapter_child"] = kill_adapter_child_arm(stack, sess, target_id, dropped_ids, watcher_log, wpid, t0, log)
        except Exception as e:  # noqa: BLE001
            rec["kill_adapter_child"] = {"constructed": True, "error": "%s: %s" % (type(e).__name__, e), "ok": False}
            log("FAIL: kill arm raised %s: %s" % (type(e).__name__, e))
        _w(os.path.join(results, "b2.json"), json.dumps(rec, indent=2, default=str))
    return rec, recs


# --------------------------------------------------------------------------- #
# B3: sustained pressure
# --------------------------------------------------------------------------- #
def phase_sustained(stack, sess, target_id, results, sender_recs, minutes, per_minute, log, watcher_log=None):
    os.makedirs(results, exist_ok=True)
    rec = {"phase": "B3", "target": target_id, "minutes": minutes, "per_minute": per_minute}
    wl_before = log_lines_since(watcher_log, 0) if watcher_log else []
    dt0 = max([d.get("dropped_total", 0) for d in wl_before if d.get("msg") == "injection queue full; oldest message dropped"] or [0])
    t0 = e5s.now_ms()
    rec["t0_ms"] = t0
    interval = 60.0 / per_minute
    n = minutes * per_minute
    sends = []
    tr = sess.transcript_path() if sess else None
    for i in range(n):
        r = sender_recs[i % len(sender_recs)]
        s = stack.send_from(r, target_id, "SUSTAIN-%s-%d-%s" % (r["title"], i + 1, e5s.rand_hex(3)))
        sends.append(s)
        target = t0 + int((i + 1) * interval * 1000)
        wait = (target - e5s.now_ms()) / 1000.0
        if wait > 0:
            time.sleep(wait)
    t_end = e5s.now_ms()
    time.sleep(60)
    with open(os.path.join(results, "sends.ndjson"), "w") as f:
        for s in sends:
            f.write(json.dumps(s, default=str) + "\n")
    wl = log_lines_since(watcher_log, t0 - 1000) if watcher_log else []
    _w(os.path.join(results, "watcher.slice.log"), "".join(json.dumps(d) + "\n" for d in wl))
    drops = [d for d in wl if d.get("msg") == "injection queue full; oldest message dropped"]
    dt1 = max([d.get("dropped_total", 0) for d in drops] or [dt0])
    held = [d for d in wl if d.get("msg") == "message held back for rate limiting"]
    recs_tr = transcript_records_since(tr, t0) if tr else []
    drop_notices = notices_in(recs_tr, DROP_NOTICE_RE)
    rate_notices = notices_in(recs_tr, RATE_NOTICE_RE)
    accepted = sum(1 for s in sends if s.get("accepted"))
    codes = {}
    for s in sends:
        if not s.get("accepted"):
            c = ((s.get("result") or {}).get("error") or {}).get("code") or "?"
            codes[c] = codes.get(c, 0) + 1
    rec.update({"sends_issued": len(sends), "accepted": accepted, "refusal_codes": codes, "wall_ms": t_end - t0,
                "dropped_total": [dt0, dt1], "drop_lines": len(drops), "held_back_lines": len(held),
                "drop_notices": drop_notices, "rate_notices": rate_notices,
                "frames_injected": len(frames_in(recs_tr)), "inbox_after": stack.inbox_state(target_id)})
    log("measured: B3 %d sends over %.1f min (%d accepted %s): dropped_total %d -> %d, held-back %d, drop notices %d, rate notices %d, frames injected %d"
        % (len(sends), (t_end - t0) / 60000.0, accepted, codes or "", dt0, dt1, len(held), len(drop_notices), len(rate_notices), rec["frames_injected"]))
    _w(os.path.join(results, "b3.json"), json.dumps(rec, indent=2, default=str))
    return rec


# --------------------------------------------------------------------------- #
# standalone
# --------------------------------------------------------------------------- #
def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--hints", type=int, default=1000)
    ap.add_argument("--hint-seconds", type=int, default=10)
    ap.add_argument("--senders", type=int, default=6)
    ap.add_argument("--per-sender", type=int, default=10)
    ap.add_argument("--target", default=None)
    ap.add_argument("--provision", action="store_true")
    ap.add_argument("--out", default=None)
    ap.add_argument("--no-b3", action="store_true")
    ap.add_argument("--b3-minutes", type=int, default=5)
    ap.add_argument("--b3-per-minute", type=int, default=20)
    ap.add_argument("--redeliver-minutes", type=int, default=6)
    ap.add_argument("--pause-watcher", action="store_true", help="B2 under a CONSTRUCTED condition (labelled; never counted)")
    ap.add_argument("--kill-adapter-child", action="store_true", help="after B2's t+redeliver reading, SIGKILL the adapter child (labelled; ruling 3)")
    ap.add_argument("--tag", default=None)
    args = ap.parse_args()
    if args.target and args.provision:
        sys.stderr.write("burst: --target and --provision are exclusive\n")
        raise SystemExit(2)
    bundle = args.out or os.path.join(REPO, ".ignored", "proof", e5s.now_stamp())
    evroot = os.path.join(bundle, "evidence", "burst")
    os.makedirs(os.path.join(bundle, "logs"), exist_ok=True)
    logf = open(os.path.join(bundle, "logs", "driver.log"), "a", buffering=1)

    def log(m):
        line = "%s %s" % (time.strftime("%H:%M:%SZ", time.gmtime()), m)
        print(line, flush=True)
        logf.write(line + "\n")

    run_id = args.tag or ("b" + time.strftime("%H%M%S", time.gmtime()))
    pre = e5s.preconditions(need_claude=not args.target)
    log("ok: preconditions; claude %s" % pre.get("claude_version"))
    stack = e5s.Stack(run_id, log=log)
    sessions_started = 0
    sess = None
    summary = {"bundle": bundle, "mode": "target" if args.target else "provision", "claude_version_t0": pre.get("claude_version")}
    try:
        stack.provision(with_sender=False)
        summary["stack_t0"] = stack.stack_uptime()
        if args.target:
            target = args.target
            b1 = phase_hints(stack, None, target, os.path.join(evroot, "hints"), args.hints, args.hint_seconds, log)
            summary["b1"] = b1
        else:
            sess = e5s.PtySoak(stack, os.path.join(evroot, "a"), arm="burst-a", profile="bob")
            sess.start()
            sessions_started += 1
            ok, ms = sess.wait_ready(timeout=240)
            log("%s burst-a session ready in %d ms (claude pid %s, brigade %s)" % ("ok:" if ok else "FAIL:", ms, sess.claude_pid, sess.brigade_id))
            if not ok:
                raise SystemExit("the burst target session never became ready")
            target = sess.brigade_id
            pollers = e5s.Pollers(stack, os.path.join(evroot, "poll"), profile="bob", json_flag=False, log=log,
                                  session_ids=[sess.brigade_id])
            pollers.start()
            r = sess.beat(e5s.setup_prompt("soak-a", "nobody yet", "none"), "setup", 0)
            log("measured: setup prompt %s in %d ms" % (r.get("outcome"), r.get("duration_ms", -1)))
            wlog = sess.watcher_log_path()
            alog = os.path.join(stack.state, "logs", "adapter-bob.log")
            b1 = phase_hints(stack, sess, target, os.path.join(evroot, "hints"), args.hints, args.hint_seconds, log,
                             watcher_log=wlog, adapter_log=alog)
            summary["b1"] = b1
            b2, srecs = phase_messages(stack, sess, target, os.path.join(evroot, "messages"), args.senders, args.per_sender, log,
                                       watcher_log=wlog, pause_watcher=args.pause_watcher, redeliver_minutes=args.redeliver_minutes,
                                       kill_adapter_child=args.kill_adapter_child)
            summary["b2"] = b2
            if not args.no_b3:
                # 60 s after B2's sends so the per-sender buckets have rolled
                wait = (b2["t0_ms"] + 60000 - e5s.now_ms()) / 1000.0
                if wait > 0:
                    time.sleep(wait)
                summary["b3"] = phase_sustained(stack, sess, target, os.path.join(evroot, "sustained"), srecs,
                                                args.b3_minutes, args.b3_per_minute, log, watcher_log=wlog)
            pollers.stop()
            st = sess.stop_session()
            log("measured: session stopped: %s" % json.dumps({k: st[k] for k in ("how", "claude_alive_at_stop", "session_log_bytes")}))
            # the pidfile and the map must be gone after /exit
            t0 = time.monotonic()
            while time.monotonic() - t0 < 15 and (os.path.exists(sess.pidfile_path()) or os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % sess.claude_pid))):
                time.sleep(0.25)
            summary["after_exit"] = {"pidfile_gone": not os.path.exists(sess.pidfile_path()),
                                     "map_gone": not os.path.exists(os.path.join(stack.bypid_dir, "%s.json" % sess.claude_pid)),
                                     "ms": int((time.monotonic() - t0) * 1000)}
            log("%s after /exit: pidfile gone=%s map gone=%s" % ("ok:" if all(summary["after_exit"][k] for k in ("pidfile_gone", "map_gone")) else "FAIL:",
                                                                summary["after_exit"]["pidfile_gone"], summary["after_exit"]["map_gone"]))
            # the run's state (watchers/*.json, logs) into the bundle
            st_dst = os.path.join(evroot, "state")
            for sub in ("watchers", "logs"):
                src = os.path.join(stack.state, sub)
                if os.path.isdir(src):
                    import shutil
                    shutil.copytree(src, os.path.join(st_dst, sub), dirs_exist_ok=True)
        summary["claude_version_t1"] = e5s.claude_version() if not args.target else None
        summary["stack_t1"] = stack.stack_uptime()
        summary["sessions_started"] = sessions_started
        for d in stack.check_real_files():
            log("FAIL: config integrity: REAL file %s changed" % d)
        summary["secret_scans"] = e5s.secret_scans(stack, [os.path.join(bundle, "evidence"), os.path.join(stack.state, "logs")],
                                                   os.path.join(bundle, "scans"))
        log("%s secret scans: clean=%s controls fired=%s" % ("ok:" if summary["secret_scans"]["all_clean"] and summary["secret_scans"]["all_controls_fired"] else "FAIL:",
                                                            summary["secret_scans"]["all_clean"], summary["secret_scans"]["all_controls_fired"]))
    finally:
        summary["teardown"] = stack.teardown(scan_roots=[os.path.join(bundle, "evidence")], bundle_cap=os.path.join(bundle, "cap"))
        summary["sessions_started"] = sessions_started
        _w(os.path.join(bundle, "burst-summary.json"), json.dumps(summary, indent=2, default=str))
        log("teardown: %s" % json.dumps({k: summary["teardown"].get(k) for k in ("watchers_killed", "project_dirs_removed", "claude_json_projects_pruned")}))
        log("say: sessions started: %d; bundle %s" % (sessions_started, bundle))
        logf.close()


if __name__ == "__main__":
    main()
