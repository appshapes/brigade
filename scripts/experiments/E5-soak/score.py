#!/usr/bin/env python3
"""E5-soak offline scorer (plan row P5-11): bundle -> summary.json / soak.tsv /
burst.tsv. NO model calls, NO stack access: every number of brief 4.6 and
every clause of brief 7 is re-derived from the saved evidence alone, so the
verifier and any later reader can reproduce summary.json byte-identically.

    python3 scripts/experiments/E5-soak/score.py <bundle>
    python3 scripts/experiments/E5-soak/score.py --selftest <dir>   # a synthetic bundle, then a planted vacuous row

E0-2's rule shapes the acceptance: a soak that asserts len(drained) == sent
is vacuously satisfiable. So the scorer requires a WORKLOAD FLOOR (>= 20
beats answered, >= 20 frames injected, >= 20 acks across the two sessions),
a "STILL WORKING AT THE END" floor (the final round trip witnessed in the
peer's transcript AND in the database), and it demonstrates its own
detectors can fail: --selftest scores a synthetic green bundle, then plants
a vacuous row (zero beats, zero frames, zero acks, everything else green)
and a notice-without-drops row, and requires both to be caught.
"""
import argparse
import json
import os
import re
import shutil
import sys

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import burst    # noqa: E402  (log/transcript readers, the notice regexes)
import common   # noqa: E402  (read_ndjson)

REFRESH_MARGIN_S = 90          # credentials.go refreshMargin
JWT_EXPIRY_S = 3600            # supabase/config.toml jwt_expiry
LEASE_S = 90                   # protocol.LeaseDefaultSeconds
HEARTBEAT_S = 30               # watch.go DefaultHeartbeatInterval
QUEUE_CAPACITY = 50            # inbound/queue.go
SENDER_RATE_PER_MINUTE = 10    # inbound/limiter.go
WORKLOAD_FLOOR = 20            # brief 7 clause 7


def _load(path, default=None):
    try:
        with open(path) as f:
            return json.load(f)
    except (OSError, ValueError):
        return default


def _nd(path):
    return common.read_ndjson(path)


def _median(xs):
    xs = sorted(xs)
    if not xs:
        return None
    n = len(xs)
    return xs[n // 2] if n % 2 else (xs[n // 2 - 1] + xs[n // 2]) / 2.0


_pg_ts_ms = burst.pg_ms   # psql -At timestamptz -> epoch ms


def _slope_per_hour(points):
    """Least squares over (ms, value) -> value units per hour."""
    if len(points) < 2:
        return None
    n = len(points)
    xs = [p[0] / 3600000.0 for p in points]
    ys = [p[1] for p in points]
    mx = sum(xs) / n
    my = sum(ys) / n
    sxx = sum((x - mx) ** 2 for x in xs)
    if sxx == 0:
        return None
    return round(sum((x - mx) * (y - my) for x, y in zip(xs, ys)) / sxx, 1)


# --------------------------------------------------------------------------- #
# the soak
# --------------------------------------------------------------------------- #
def score_soak(bundle):
    ev = os.path.join(bundle, "evidence", "soak")
    run = _load(os.path.join(bundle, "soak-run.json"), {})
    out = {"present": os.path.isdir(ev)}
    if not out["present"]:
        return out
    sessions = sorted(n for n in os.listdir(ev) if len(n) == 1 and os.path.isdir(os.path.join(ev, n)))
    out["sessions"] = sessions
    t0 = run.get("t0_ms")
    minutes = (run.get("args") or {}).get("minutes")

    # 1. rotations from the profile-status series
    status = _nd(os.path.join(ev, "profile-status.ndjson"))
    series = [(r["ms"], r.get("token_expires_at")) for r in status if r.get("token_expires_at")]
    rotations = []
    prev = None
    for ms, exp in series:
        if prev is not None and exp != prev:
            rotations.append({"observed_ms": ms, "from_expiry": prev, "to_expiry": exp,
                              "t_min": round((ms - t0) / 60000.0, 2) if t0 else None})
        prev = exp
    predicted = []
    if run.get("token_expires_at_t0") and t0:
        exp0 = burst._parse_iso_ms(run["token_expires_at_t0"])
        # the adapter refreshes when fewer than 90 s remain: expected at exp - 90 s, then every 3510 s
        t = exp0 - REFRESH_MARGIN_S * 1000
        while minutes and t < t0 + minutes * 60000:
            predicted.append({"ms": t, "t_min": round((t - t0) / 60000.0, 2)})
            t += (JWT_EXPIRY_S - REFRESH_MARGIN_S) * 1000
    out["rotations"] = {"count": len(rotations), "observed": rotations, "predicted": predicted,
                        "status_samples": len(status), "status_exits": sorted(set(r.get("rc") for r in status)),
                        "status_states": sorted(set(r.get("state") for r in status))}

    # 2. server-side /token calls: GoTrue's audit trail for the principal (soak.py snapshots it)
    a0, a1 = run.get("audit_t0") or {}, run.get("audit_t1") or {}
    refreshed = (a1.get("token_refreshed", 0) - a0.get("token_refreshed", 0))
    revoked = (a1.get("token_revoked", 0) - a0.get("token_revoked", 0))
    out["token_calls"] = {"token_refreshed": refreshed, "token_revoked": revoked,
                          "one_behind_redemptions": refreshed - revoked,
                          "events": run.get("audit_events") or [],
                          "equal_to_rotation_count": refreshed == len(rotations)}

    # 3. contention and the lock
    state_logs = os.path.join(ev, "state", "logs")
    alog = os.path.join(state_logs, "adapter-%s.log" % (run.get("args") or {}).get("profile", "bob"))
    alines = burst.log_lines_since(alog, 0)
    lock_timeouts = sum(1 for d in alines if "lock_timeout" in json.dumps(d))
    terminal = sum(1 for d in alines if d.get("msg") == "credential is terminal; removing session.json")
    already_used = sum(1 for d in alines if "already used" in (d.get("msg") or ""))
    refresh_beats = run.get("refresh_beats") or []
    contention = []
    for rb in refresh_beats:
        exp_ms = burst._parse_iso_ms(rb["expiry"])
        contention.append({"expiry": rb["expiry"], "extra_beat_at_ms": rb["at_ms"],
                           "inside_margin": (exp_ms - REFRESH_MARGIN_S * 1000) <= rb["at_ms"] <= exp_ms})
    out["contention"] = {"adapter_log_lines": len(alines), "lock_timeout_errors": lock_timeouts,
                         "terminal_lines": terminal, "already_used_lines_debug_only": already_used,
                         "refresh_beats": contention,
                         "note": "adoption logs nothing at INFO; the contended-acquisition latency is CITED from P1-3 "
                                 "(47 of 80 contended, min 5.66 / median 16.96 / max 68.40 ms), not re-measured"}

    # 4. heartbeats from the roster's last_seen_at
    roster = _nd(os.path.join(ev, "roster.ndjson"))
    # the SQL sampler runs until the pollers stop, 10-20 s after /exit: only samples inside the run count
    t_run_end = (t0 + (minutes or 0) * 60000) if (t0 and minutes) else None
    hbs = [h for h in _nd(os.path.join(ev, "heartbeat-sql.ndjson")) if t_run_end is None or h.get("ms", 0) <= t_run_end]
    ids = {n: (_load(os.path.join(ev, n, "bypid-live.json"), {}) or {}).get("brigade_session_id") for n in sessions}
    hb = {}
    for n, sid in ids.items():
        seen = []
        states = {}
        for snap in roster:
            for s in snap.get("sessions") or []:
                if s.get("session_id") == sid:
                    ls = s.get("last_seen_at")
                    if ls:
                        seen.append(burst._parse_iso_ms(ls))
                    states[s.get("state")] = states.get(s.get("state"), 0) + 1
        seen = sorted(set(seen))
        gaps = [(b - a) / 1000.0 for a, b in zip(seen, seen[1:])]
        hb[n] = {"session_id": sid, "distinct_last_seen": len(seen), "max_gap_s": max(gaps) if gaps else None,
                 "median_gap_s": _median(gaps), "states_seen": states, "offline_samples": states.get("offline", 0),
                 "gap_over_lease": any(g > LEASE_S for g in gaps),
                 "roster_resolution_s": 60}
        # the SQL series (brigade.sessions.last_seen_at every 15 s): the heartbeat cadence at a usable resolution
        sq = sorted(set(_pg_ts_ms(s["last_seen_at"]) for snap in hbs for s in (snap.get("sessions") or [])
                        if s.get("session_id") == sid and s.get("last_seen_at")))
        sgaps = [(b - a) / 1000.0 for a, b in zip(sq, sq[1:])]
        hb[n]["sql"] = ({"samples": len(hbs), "distinct_last_seen": len(sq), "gap_min_s": min(sgaps) if sgaps else None,
                         "gap_median_s": _median(sgaps), "gap_max_s": max(sgaps) if sgaps else None,
                         "gaps_over_lease": sum(1 for g in sgaps if g > LEASE_S),
                         "closed_at_seen": any(s.get("closed_at") for snap in hbs for s in (snap.get("sessions") or []) if s.get("session_id") == sid),
                         "resolution_s": 15} if hbs else None)
    out["heartbeats"] = {"roster_samples": len(roster), "sql_samples": len(hbs), "per_session": hb}

    # 7. memory and CPU
    ps = _nd(os.path.join(ev, "ps.ndjson"))
    procs = {}
    # only the run's own claude pids (soak-run.json `ready`): the outer Claude Code session is a foreign `claude`
    own_claude = set(int(v.get("claude_pid")) for v in (run.get("ready") or {}).values() if v.get("claude_pid"))
    foreign = set()
    for snap in ps:
        for r in snap.get("rows") or []:
            if _proc_kind(r["args"]) == "claude" and own_claude and r["pid"] not in own_claude:
                foreign.add(r["pid"])
                continue
            key = "%d %s" % (r["pid"], _proc_kind(r["args"]))
            procs.setdefault(key, {"args": r["args"][:120], "rss": [], "cpu": []})
            procs[key]["rss"].append((snap["ms"], r["rss_kb"]))
            procs[key]["cpu"].append(r["cpu"])
    mem = {}
    for key, p in procs.items():
        rss = [v for _, v in p["rss"]]
        if len(rss) < 3:
            continue
        half = p["rss"][len(p["rss"]) // 2:]     # the second half of the run: warm-up excluded
        mem[key] = {"kind": _proc_kind(p["args"]), "samples": len(rss), "first_kb": rss[0], "median_kb": _median(rss),
                    "last_kb": rss[-1], "slope_kb_per_hour": _slope_per_hour(p["rss"]),
                    "second_half_first_kb": half[0][1], "slope_second_half_kb_per_hour": _slope_per_hour(half),
                    "cpu_median": _median(p["cpu"]), "cpu_max": max(p["cpu"])}
    doubling = {k: v for k, v in mem.items() if v["slope_kb_per_hour"] and v["first_kb"] and v["slope_kb_per_hour"] * 24 > v["first_kb"]}
    doubling2 = {k: v for k, v in mem.items() if v["slope_second_half_kb_per_hour"] and v["second_half_first_kb"]
                 and v["slope_second_half_kb_per_hour"] * 24 > v["second_half_first_kb"]}
    out["memory"] = {"ps_samples": len(ps), "processes": mem, "projected_doubling_in_24h": sorted(doubling),
                     "projected_doubling_in_24h_from_second_half": sorted(doubling2),
                     "foreign_claude_pids_excluded": sorted(foreign)}

    # 8. liveness of the instrument, per session
    live = _nd(os.path.join(ev, "liveness.ndjson"))
    per = {}
    beats_answered_total = 0
    frames_total = 0
    acks_total = 0
    for n in sessions:
        d = os.path.join(ev, n)
        beats = [b for b in _nd(os.path.join(d, "beats.ndjson")) if "outcome" in b]
        marks = _nd(os.path.join(d, "marks.ndjson"))
        stop = _load(os.path.join(d, "stop.json"), {})
        wl = burst.log_lines_since(os.path.join(d, "watcher.log"), 0)
        tr = burst.transcript_records_since(os.path.join(d, "transcript.jsonl"), 0)
        frames = burst.frames_in(tr)
        acks = sum(int(x.get("ids", 0)) for x in wl if x.get("msg") == "ack sent")
        answered = [b for b in beats if b.get("outcome") == "settled" and not b.get("provider_refusals")
                    and b.get("kind") in ("beat", "refresh", "final", "setup")]
        capped = [b for b in beats if b.get("outcome") == "capped"]
        refused_beats = [b for b in beats if b.get("outcome") == "provider_refusal" or b.get("provider_refusals")]
        provider_refusals = sum(1 for r in tr if r.get("type") == "assistant" and r.get("isApiErrorMessage"))
        bytes_series = [(x["ms"], (x.get("session_log_bytes") or {}).get(n)) for x in live if (x.get("session_log_bytes") or {}).get(n) is not None]
        growth = None
        if len(bytes_series) >= 2:
            growth = (bytes_series[-1][1] - bytes_series[0][1]) / max(1.0, (bytes_series[-1][0] - bytes_series[0][0]) / 60000.0)
        stalled = None
        if len(bytes_series) >= 3:
            # the last third of the run must still have grown
            third = bytes_series[len(bytes_series) * 2 // 3:]
            stalled = (third[-1][1] - third[0][1]) <= 0
        per[n] = {"beats_total": len(beats), "beats_answered": len(answered), "beats_capped": len(capped),
                  "beats_with_provider_refusal": len(refused_beats), "provider_refusals": provider_refusals,
                  "beats_not_submitted": sum(1 for b in beats if b.get("outcome") == "not_submitted"),
                  "dialogs_escaped": sum(1 for m in marks if m.get("event") == "dialog_escaped"),
                  "prose_matches_ignored": sum(1 for m in marks if m.get("event") == "prose_match_ignored"),
                  "drain_marks": sum(1 for m in marks if m.get("event") == "drain"),
                  "log_rotations": sum(1 for m in marks if m.get("event") == "log_rotated"),
                  "canary_ok": any(m.get("event") == "canary_ok" for m in marks),
                  "frames_injected": len(frames), "frames_injected_more_than_once": sorted(k for k, v in frames.items() if v["occurrences"] > 1),
                  "frames_via_queue": sum(1 for v in frames.values() if v.get("via") == "queue"),
                  "ack_ids": acks, "ack_lines": sum(1 for x in wl if x.get("msg") == "ack sent"),
                  "watcher_warn_error": sum(1 for x in wl if x.get("level") in ("WARN", "ERROR")),
                  "watcher_child_restarts": sum(1 for x in wl if x.get("msg") == "watch child failed; restarting"),
                  "watch_ready_lines": sum(1 for x in wl if x.get("msg") == "watch ready"),
                  "last_ack_ms": max([burst._parse_iso_ms(x["time"]) for x in wl if x.get("msg") == "ack sent"] or [0]),
                  "session_log_bytes": stop.get("session_log_bytes"), "session_log_bytes_per_minute": round(growth, 1) if growth is not None else None,
                  "session_log_stalled_in_last_third": stalled, "expect": stop.get("how"),
                  "assistant_records": sum(1 for r in tr if r.get("type") == "assistant"),
                  "unauthorized_lines": sum(1 for x in wl if "unauthorized" in json.dumps(x) or "unauthenticated" in json.dumps(x))}
        beats_answered_total += len(answered)
        frames_total += len(frames)
        acks_total += acks
    out["sessions_detail"] = per
    out["workload"] = {"beats_answered": beats_answered_total, "frames_injected": frames_total, "acks": acks_total,
                       "floor": WORKLOAD_FLOOR}

    # the final round trip and the end-of-run state
    final = run.get("final") or {}
    out["final_round_trip"] = {"ok": bool(final.get("ok")), "frame_in_b_ms": final.get("frame_in_b_ms"),
                               "reply_rows": len(final.get("reply_rows") or []), "reply_frame_in_a_ms": final.get("reply_frame_in_a_ms")}
    pre = run.get("pre_exit") or {}
    after = run.get("after_exit") or {}
    t_end = t0 + (minutes or 0) * 60000 if t0 else None
    last_roster = None
    for snap in roster:
        if t_end is None or snap["ms"] <= t_end + 120000:
            last_roster = snap
    online_at_end = {}
    if last_roster:
        for n, sid in ids.items():
            st = [s for s in last_roster.get("sessions") or [] if s.get("session_id") == sid]
            online_at_end[n] = bool(st) and st[0].get("state") in ("idle", "active")
    out["end_state"] = {"pre_exit": pre, "after_exit": after, "online_at_end": online_at_end,
                        "notice_files": run.get("notice_files"), "credential_file": run.get("credential_file"),
                        "claude_version": [run.get("claude_version_t0"), run.get("claude_version_t1")],
                        "launcher": [run.get("launcher_t0"), run.get("launcher_t1")],
                        "pg_start": [(run.get("stack_t0") or {}).get("pg_postmaster_start_time"), (run.get("stack_t1") or {}).get("pg_postmaster_start_time")]}
    out["sessions_started"] = run.get("sessions_started")
    out["minutes"] = minutes
    # 5.3 clause 7: `ack sent` still appearing after t = minutes - 10 (t = 110 min in the deliverable)
    out["late_ack_threshold_ms"] = (t0 + (minutes - 10) * 60000) if (t0 and minutes) else None
    out["overlap_minutes"] = _overlap_minutes(run, sessions)
    return out


def _proc_kind(args):
    if "brigade adapter supabase" in args:
        return "adapter-watch-child"
    if "brigade watch" in args:
        return "watcher"
    if "claude" in args:
        return "claude"
    return "other"


def _overlap_minutes(run, sessions):
    t0 = run.get("t0_ms")
    stops = run.get("stops") or {}
    if not t0 or not stops:
        return None
    # both sessions were alive from t0 until the earliest stop
    fin = run.get("finished")
    return round(((run.get("args") or {}).get("minutes") or 0), 1) if fin else None


# --------------------------------------------------------------------------- #
# the burst
# --------------------------------------------------------------------------- #
def score_burst(bundle):
    ev = os.path.join(bundle, "evidence", "burst")
    out = {"present": os.path.isdir(ev)}
    if not out["present"]:
        return out
    b1 = _load(os.path.join(ev, "hints", "b1.json"))
    b2 = _load(os.path.join(ev, "messages", "b2.json"))
    b3 = _load(os.path.join(ev, "sustained", "b3.json"))
    if b1:
        cans = {k: {"ok": v.get("ok"), "rtt_ms": v.get("rtt_ms")} for k, v in (b1.get("canaries") or {}).items() if isinstance(v, dict) and "ok" in v}
        out["b1"] = {"hints": b1.get("hints"), "sent_total": b1.get("sent_total"), "fire_wall_ms": b1.get("fire_wall_ms"),
                     "sql_span_ms": b1.get("sql_span_ms"),
                     "batches_ok": sum(1 for b in b1.get("batches") or [] if b.get("rc") == 0),
                     "realtime_rows_delta": (b1.get("realtime_rows_on_topic") or [0, 0])[1] - (b1.get("realtime_rows_on_topic") or [0, 0])[0],
                     "drains_within_3s_of_end": b1.get("drains_within_3s_of_end"), "drains_by_end_of_window": b1.get("drains_by_end_of_window"),
                     "background_drains_per_30s": b1.get("background_drains_per_30s"),
                     "ack_sent": (b1.get("watcher_slice") or {}).get("ack_sent"), "child_failed": (b1.get("watcher_slice") or {}).get("child_failed"),
                     "watch_status_changes": (b1.get("watcher_slice") or {}).get("watch_status"),
                     "frames_in_transcript_slice": (b1.get("transcript_slice") or {}).get("frames"),
                     "notices_in_transcript_slice": {"drop": (b1.get("transcript_slice") or {}).get("drop_notices"), "rate": (b1.get("transcript_slice") or {}).get("rate_notices")},
                     "canaries": cans, "inbox_before": b1.get("inbox_before"), "inbox_after": b1.get("inbox_after")}
    if b2:
        tr = b2.get("transcript") or {}
        w = b2.get("watcher") or {}
        later_key = [k for k in b2 if k.startswith("dropped_after_")]
        later = b2.get(later_key[0]) if later_key else {}
        out["b2"] = {"senders": b2.get("senders"), "per_sender": b2.get("per_sender"), "constructed_condition": b2.get("constructed_condition"),
                     "sends": b2.get("sends"), "dropped": len(w.get("dropped_ids") or []), "dropped_total": w.get("dropped_total"),
                     "held_back_lines": w.get("held_back_lines"), "deferred_lines": w.get("deferred_lines"),
                     "injection_failed": w.get("injection_failed"), "ack_ids": w.get("ack_ids_sum"),
                     "frames_injected": tr.get("burst_frames_injected"), "frames_more_than_once": tr.get("frames_injected_more_than_once"),
                     "max_frames_per_sender_per_minute": tr.get("max_frames_per_sender_per_minute"),
                     "drop_notices": tr.get("drop_notices"), "rate_notices": len(tr.get("rate_notices") or []),
                     "queued_inferred": ((b2.get("sends") or {}).get("accepted") or 0) - len(w.get("dropped_ids") or []),
                     "server_after_60s": b2.get("server_after_60s"),
                     "dropped_later": {"key": later_key[0] if later_key else None,
                                       "server_states": sorted(set((later.get("server_state") or {}).values())),
                                       "injected_occurrences": sorted(set((later.get("injected_occurrences") or {}).values())),
                                       "watcher_child_restarts": later.get("watcher_child_restarts")},
                     "kill_adapter_child": _kill_arm_summary(b2.get("kill_adapter_child") or {}),
                     "bound_reached": b2.get("bound_reached")}
    if b3:
        out["b3"] = {"sends_issued": b3.get("sends_issued"), "accepted": b3.get("accepted"), "dropped_total": b3.get("dropped_total"),
                     "drop_lines": b3.get("drop_lines"), "held_back_lines": b3.get("held_back_lines"),
                     "drop_notices": len(b3.get("drop_notices") or []), "rate_notices": len(b3.get("rate_notices") or []),
                     "frames_injected": b3.get("frames_injected")}
    # the burst ids' fate in the FULL transcript of the target session (copied at stop): when each accepted id was
    # first framed, relative to B2's t0 -- Claude Code queues frames that arrive while a turn is in flight and the
    # 60 s slice sees only what it had delivered by then (measured 2026-09-05: 1 of 60 at +60 s)
    if b2:
        sends = _nd(os.path.join(ev, "messages", "sends.ndjson"))
        acc = {x["message_id"]: x for x in sends if x.get("accepted") and x.get("message_id")}
        t0 = b2.get("t0_ms") or 0
        full = None
        for cand in (os.path.join(bundle, "evidence", "soak", "a", "transcript.jsonl"), os.path.join(ev, "a", "transcript.jsonl")):
            if os.path.exists(cand):
                full = cand
                break
        if full and acc:
            fr = burst.frames_in(burst.transcript_records_since(full, t0 - 1000))
            lat = sorted((burst._parse_iso_ms(fr[m]["first"]) - t0) / 1000.0 for m in acc if m in fr and fr[m].get("first"))
            recs_by_ts = {}
            for m in acc:
                if m in fr:
                    recs_by_ts[fr[m]["first"]] = recs_by_ts.get(fr[m]["first"], 0) + 1
            # the kill arm's second half re-derived offline: every dropped id's occurrences in the full transcript
            w = b2.get("watcher") or {}
            dropped_ids = w.get("dropped_ids") or []
            if dropped_ids:
                occ = {m: (fr.get(m) or {}).get("occurrences", 0) for m in dropped_ids}
                out["b2"]["redelivery_rederived"] = {"dropped": len(dropped_ids), "occurrences": occ,
                                                     "all_exactly_once": all(v == 1 for v in occ.values()),
                                                     "via": {m: (fr.get(m) or {}).get("via") for m in dropped_ids}}
            recs_full = burst.transcript_records_since(full, t0 - 1000)
            out["b2"]["notices_rederived"] = {"drop": burst.notices_in(recs_full, burst.DROP_NOTICE_RE),
                                              "rate": len(burst.notices_in(recs_full, burst.RATE_NOTICE_RE))}
            out["b2"]["full_transcript"] = {
                "source": os.path.relpath(full, bundle), "accepted": len(acc), "framed_ever": len(lat),
                "framed_within_60s": sum(1 for x in lat if x <= 60), "never_framed": sorted(m for m in acc if m not in fr),
                "first_frame_s_after_t0": {"min": lat[0] if lat else None, "median": _median(lat), "max": lat[-1] if lat else None},
                "frames_more_than_once": sorted(m for m in acc if m in fr and fr[m]["occurrences"] > 1),
                "ids_per_record_timestamp": dict(sorted(recs_by_ts.items())[:12])}
    bs = _load(os.path.join(bundle, "burst-summary.json"), {})
    if bs:
        out["standalone"] = {"sessions_started": bs.get("sessions_started"), "claude_version": [bs.get("claude_version_t0"), bs.get("claude_version_t1")],
                             "after_exit": bs.get("after_exit"), "secret_scans": {k: (bs.get("secret_scans") or {}).get(k) for k in ("all_clean", "all_controls_fired")}}
    return out


# --------------------------------------------------------------------------- #
# acceptance (brief 7)
# --------------------------------------------------------------------------- #
def acceptance(soak, brs, bundle):
    c = {}
    if soak.get("present"):
        det = soak.get("sessions_detail") or {}
        end = soak.get("end_state") or {}
        rot = soak.get("rotations") or {}
        tok = soak.get("token_calls") or {}
        con = soak.get("contention") or {}
        hb = (soak.get("heartbeats") or {}).get("per_session") or {}
        wl = soak.get("workload") or {}
        mins = soak.get("minutes") or 0
        both_alive = bool(end.get("pre_exit")) and all(v.get("pidfile") and v.get("map") and v.get("expect_alive") for v in end["pre_exit"].values())
        c["1_two_sessions_120min_both_alive"] = {"ok": len(det) >= 2 and mins >= 120 and both_alive,
                                                 "sessions": len(det), "minutes": mins, "both_alive_at_end": both_alive}
        c["2_rotations_observed"] = {"ok": rot.get("count", 0) >= 2, "count": rot.get("count"), "predicted": len(rot.get("predicted") or [])}
        c["3_token_calls_equal_rotations"] = {"ok": tok.get("token_refreshed", -1) == rot.get("count", -2) and rot.get("count", 0) >= 1,
                                              "token_refreshed": tok.get("token_refreshed"), "rotations": rot.get("count")}
        cred = end.get("credential_file") or {}
        lockout = {
            "no_terminal_line": con.get("terminal_lines", 1) == 0,
            "credential_file_ok": bool(cred.get("exists")) and bool(cred.get("parses")) and cred.get("mode") == "0o600",
            "status_joined_throughout": (rot.get("status_states") or []) == ["joined"] and (rot.get("status_exits") or []) == [0],
            "no_lock_timeout": con.get("lock_timeout_errors", 1) == 0,
            "no_unauthorized": all(v.get("unauthorized_lines", 1) == 0 for v in det.values()),
            "both_send_and_receive_at_end": bool((soak.get("final_round_trip") or {}).get("ok")),
            "both_watchers_alive_and_acking_late": both_alive and soak.get("late_ack_threshold_ms") is not None
                                                  and all(v.get("last_ack_ms", 0) >= soak["late_ack_threshold_ms"] and v.get("ack_ids", 0) > 0 for v in det.values()),
            "no_notice_files": (end.get("notice_files") or []) == [],
        }
        c["4_no_lockout"] = {"ok": all(lockout.values()), "clauses": lockout}
        hb_ok = bool(hb) and all(v.get("offline_samples", 1) == 0 and not v.get("gap_over_lease", True) and v.get("distinct_last_seen", 0) >= 2 for v in hb.values())
        sql_ok = all((v.get("sql") is None) or (v["sql"].get("gaps_over_lease", 1) == 0 and not v["sql"].get("closed_at_seen")) for v in hb.values())
        c["5_heartbeats_no_lease_expiry"] = {"ok": hb_ok and sql_ok and all(end.get("online_at_end", {}).values()) and bool(end.get("online_at_end")),
                                             "per_session": {n: {"roster_max_gap_s": v.get("max_gap_s"), "offline": v.get("offline_samples"),
                                                                 "sql_gap_median_s": (v.get("sql") or {}).get("gap_median_s"),
                                                                 "sql_gap_max_s": (v.get("sql") or {}).get("gap_max_s"),
                                                                 "sql_gaps_over_lease": (v.get("sql") or {}).get("gaps_over_lease")} for n, v in hb.items()},
                                             "online_at_end": end.get("online_at_end"),
                                             "note": "the roster poll is 60 s (its median gap is the poll interval); the SQL series of brigade.sessions.last_seen_at "
                                                     "every 15 s carries the cadence; the watcher's heartbeat line is DEBUG and unreachable"}
        c["6_final_round_trip_in_db"] = {"ok": bool((soak.get("final_round_trip") or {}).get("ok")), **(soak.get("final_round_trip") or {})}
        c["7_workload_floor"] = {"ok": wl.get("beats_answered", 0) >= WORKLOAD_FLOOR and wl.get("frames_injected", 0) >= WORKLOAD_FLOOR and wl.get("acks", 0) >= WORKLOAD_FLOOR,
                                 **wl}
        mem = soak.get("memory") or {}
        c["8_rss_slope_reported"] = {"ok": bool(mem.get("processes")), "projected_doubling_in_24h": mem.get("projected_doubling_in_24h")}
        stalled = {n: v.get("session_log_stalled_in_last_third") for n, v in det.items()}
        c["not_green_if_frozen_or_updated_or_restarted"] = {
            "ok": not any(stalled.values()) and end.get("claude_version", [None, None])[0] == end.get("claude_version", [None, None])[1]
                  and end.get("pg_start", [None, None])[0] == end.get("pg_start", [None, None])[1],
            "session_log_stalled": stalled, "claude_version": end.get("claude_version"), "pg_start": end.get("pg_start"),
            "provider_refusals": {n: v.get("provider_refusals") for n, v in det.items()}}
    if brs.get("present"):
        b1 = brs.get("b1") or {}
        b2 = brs.get("b2") or {}
        b3 = brs.get("b3")
        if b1:
            cans = b1.get("canaries") or {}
            span = b1.get("sql_span_ms") if b1.get("sql_span_ms") is not None else b1.get("fire_wall_ms")
            c["9_hints_1000_in_10s_responsive_joined"] = {
                "ok": b1.get("sent_total") == 1000 and b1.get("realtime_rows_delta") == 1000 and (span or 99999) <= 12000
                      and (b1.get("drains_within_3s_of_end") or 0) > (b1.get("background_drains_per_30s") or 0) and (b1.get("ack_sent") or 0) == 0 and (b1.get("child_failed") or 0) == 0
                      and bool(cans) and all(v.get("ok") for v in cans.values()),
                "sent": b1.get("sent_total"), "fire_wall_ms": b1.get("fire_wall_ms"), "sql_span_ms": b1.get("sql_span_ms"),
                "span_used_ms": span, "drains": b1.get("drains_within_3s_of_end"),
                "background_per_30s": b1.get("background_drains_per_30s"), "canaries": cans, "ack_sent": b1.get("ack_sent")}
        if b2:
            n_drop = len(b2.get("drop_notices") or [])
            ka = b2.get("kill_adapter_child") or {}
            counted = not bool(b2.get("constructed_condition"))   # a --pause-watcher bundle is never counted (ruling 2)
            bounded = ((b2.get("max_frames_per_sender_per_minute") or 0) <= SENDER_RATE_PER_MINUTE and bool(b2.get("bound_reached"))
                       and (b2.get("injection_failed") or 0) == 0 and not b2.get("frames_more_than_once"))
            rd = b2.get("redelivery_rederived") or {}
            # the arm's own verdict, corrected by the offline re-derivation of the per-id occurrences when present
            redelivered_once = bool(ka.get("ran")) and (rd.get("all_exactly_once") if rd else bool(ka.get("ok"))) \
                and bool((ka.get("all_injected_on_server")) if ka.get("all_injected_on_server") is not None else True)
            c["10_injection_bounded"] = {
                "ok": counted and bounded and redelivered_once,
                "counted": counted, "first_half_bounded": bounded, "second_half_redelivered_once": redelivered_once,
                "max_frames_per_sender_per_minute": b2.get("max_frames_per_sender_per_minute"), "bound_reached": b2.get("bound_reached"),
                "dropped": b2.get("dropped"), "dropped_later": b2.get("dropped_later"), "constructed_condition": b2.get("constructed_condition"),
                "redelivery_arm": ka, "redelivery_rederived": rd,
                "note": "the dropped ids stay `accepted` on the server while the same watch child lives (the adapter's per-process seen map); "
                        "the second half (redelivered later, injected exactly once) is measured under a named construction: the adapter child "
                        "SIGKILLed after the t+6 min reading and respawned by the shipped supervision (driver ruling 3). A bundle whose B2 ran "
                        "under --pause-watcher is labelled constructed and never counted"}
            nr = b2.get("notices_rederived")
            if nr is not None:     # the full transcript, user records and the queue: authoritative when present
                n_drop = len(nr.get("drop") or [])
                n_rate = nr.get("rate") or 0
                verbatim = [n.get("text") for n in nr.get("drop") or []]
            else:
                n_rate = b2.get("rate_notices") or 0
                verbatim = [n.get("text") for n in (b2.get("drop_notices") or [])]
            c["11_one_drop_notice_zero_rate_notices"] = {
                "ok": counted and n_drop == 1 and (b2.get("dropped") or 0) >= 1 and n_rate == 0,
                "counted": counted, "drop_notices": n_drop, "dropped": b2.get("dropped"), "rate_notices": n_rate,
                "rederived_from_full_transcript": nr is not None, "verbatim": verbatim}
        if b3:
            c["12_sustained"] = {"ok": (b3.get("drop_notices") or 0) <= 1, "dropped_total": b3.get("dropped_total"),
                                 "drop_notices": b3.get("drop_notices"), "rate_notices": b3.get("rate_notices")}
    scans = _load(os.path.join(bundle, "scans", "secret-scans.json"), {})
    c["14_secret_scans_with_controls"] = {"ok": bool(scans) and bool(scans.get("all_clean")) and bool(scans.get("all_controls_fired")),
                                          "all_clean": scans.get("all_clean"), "all_controls_fired": scans.get("all_controls_fired")}
    return c


def score(bundle):
    soak = score_soak(bundle)
    brs = score_burst(bundle)
    acc = acceptance(soak, brs, bundle)
    green = bool(acc) and all(v.get("ok") for v in acc.values())
    summary = {"bundle_basename": os.path.basename(os.path.normpath(bundle)), "soak": soak, "burst": brs,
               "acceptance": acc, "green": green}
    text = json.dumps(summary, indent=2, sort_keys=True, default=str) + "\n"
    with open(os.path.join(bundle, "summary.json"), "w") as f:
        f.write(text)
    write_soak_tsv(bundle, soak)
    write_burst_tsv(bundle, brs)
    return summary


def _kill_arm_summary(ka):
    """The labelled arm of clause 10's second half (ruling 3): ran, its
    construction, the restart latency, and whether every dropped id was
    injected exactly once and is `injected` on the server."""
    rd = ka.get("redelivery") or {}
    rs = ka.get("restart") or {}
    return {"ran": "redelivery" in ka, "not_run": ka.get("not_run"), "constructed": ka.get("constructed"),
            "construction": ka.get("construction"), "kill_at_s_after_t0": ka.get("kill_at_s_after_t0"),
            "restart_latency_ms": rs.get("restart_latency_ms"), "child_failed_line": rs.get("child_failed_line"),
            "all_injected_exactly_once": rd.get("all_injected_exactly_once"), "all_injected_on_server": rd.get("all_injected_on_server"),
            "latency_from_kill_ms": rd.get("latency_from_kill_ms"), "ack_ids_after_kill": rd.get("ack_ids_after_kill"),
            "occurrences_after": rd.get("occurrences_after"), "ok": ka.get("ok")}


def write_soak_tsv(bundle, soak):
    ev = os.path.join(bundle, "evidence", "soak")
    rows = ["t\tms\ttoken_expires_at\tstate\trc\troster\trss_kb"]
    status = _nd(os.path.join(ev, "profile-status.ndjson"))
    roster = _nd(os.path.join(ev, "roster.ndjson"))
    ps = _nd(os.path.join(ev, "ps.ndjson"))
    for r in status:
        ms = r.get("ms", 0)
        near_r = min(roster, key=lambda x: abs(x["ms"] - ms)) if roster else None
        near_p = min(ps, key=lambda x: abs(x["ms"] - ms)) if ps else None
        rs = ",".join("%s:%s" % ((s.get("session_name") or "")[:8], s.get("state")) for s in (near_r or {}).get("sessions") or []) if near_r and abs(near_r["ms"] - ms) < 90000 else ""
        pr = ",".join("%s:%s" % (_proc_kind(x["args"])[:7], x["rss_kb"]) for x in (near_p or {}).get("rows") or []) if near_p and abs(near_p["ms"] - ms) < 90000 else ""
        rows.append("\t".join(str(x) for x in (r.get("t"), ms, r.get("token_expires_at"), r.get("state"), r.get("rc"), rs, pr)))
    with open(os.path.join(bundle, "soak.tsv"), "w") as f:
        f.write("\n".join(rows) + "\n")


def write_burst_tsv(bundle, brs):
    ev = os.path.join(bundle, "evidence", "burst")
    rows = ["phase\tt_ms\tkind\tsender\tmessage_id\toutcome"]
    b1 = _load(os.path.join(ev, "hints", "b1.json"))
    if b1:
        for b in b1.get("batches") or []:
            rows.append("\t".join(str(x) for x in ("B1", b.get("issued_ms"), "hint_batch", "-", "-", "sent=%s rc=%s" % (b.get("sent"), b.get("rc")))))
        for k, v in (b1.get("canaries") or {}).items():
            if isinstance(v, dict) and "ok" in v:
                rows.append("\t".join(str(x) for x in ("B1", v.get("submitted_at_ms"), "canary_" + k, "-", "-", "ok=%s rtt_ms=%s" % (v.get("ok"), v.get("rtt_ms")))))
    for phase, sub in (("B2", "messages"), ("B3", "sustained")):
        for s in _nd(os.path.join(ev, sub, "sends.ndjson")):
            rows.append("\t".join(str(x) for x in (phase, s.get("sent_at_ms"), "send", (s.get("sender") or {}).get("title"), s.get("message_id"),
                                                    "accepted" if s.get("accepted") else "refused:%s" % (((s.get("result") or {}).get("error") or {}).get("code")))))
        for d in burst.log_lines_since(os.path.join(ev, sub, "watcher.slice.log"), 0):
            if d.get("msg") == "injection queue full; oldest message dropped":
                rows.append("\t".join(str(x) for x in (phase, burst._parse_iso_ms(d["time"]), "drop", "-", d.get("dropped_message_id"), "dropped_total=%s" % d.get("dropped_total"))))
            if d.get("msg") == "ack sent":
                rows.append("\t".join(str(x) for x in (phase, burst._parse_iso_ms(d["time"]), "ack", "-", "-", "ids=%s" % d.get("ids"))))
        bj = _load(os.path.join(ev, sub, "b2.json" if phase == "B2" else "b3.json"))
        if bj:
            for n in (bj.get("transcript") or bj).get("drop_notices") or []:
                rows.append("\t".join(str(x) for x in (phase, burst._parse_iso_ms(n["timestamp"]), "drop_notice", "-", "-", n["text"])))
            for n in (bj.get("transcript") or bj).get("rate_notices") or []:
                rows.append("\t".join(str(x) for x in (phase, burst._parse_iso_ms(n["timestamp"]), "rate_notice", "-", "-", n["text"])))
    with open(os.path.join(bundle, "burst.tsv"), "w") as f:
        f.write("\n".join(rows) + "\n")


# --------------------------------------------------------------------------- #
# --selftest: a synthetic bundle, scored green, then a planted vacuous row
# --------------------------------------------------------------------------- #
def _synthetic_bundle(root, minutes=120, beats_per_session=12, frames_per_session=12, acks_per_session=12,
                      drops=10, drop_notices=1, rotations=2):
    if os.path.isdir(root):
        shutil.rmtree(root)
    ev = os.path.join(root, "evidence", "soak")
    os.makedirs(ev)
    T0 = 1_800_000_000_000
    exp0 = T0 + 3000 * 1000
    ids = {"a": "aaaaaaaa-0000-4000-8000-000000000001", "b": "bbbbbbbb-0000-4000-8000-000000000002"}
    # profile-status series: 30 s, rotations at exp - 90 s
    exps = [exp0 + k * 3510 * 1000 for k in range(rotations + 1)]
    with open(os.path.join(ev, "profile-status.ndjson"), "w") as f:
        for i in range(minutes * 2):
            ms = T0 + i * 30000
            cur = exps[0]
            for k in range(1, rotations + 1):
                if ms >= exps[k - 1] - 90000:
                    cur = exps[k]
            f.write(json.dumps({"ms": ms, "t": burst.iso_ms(ms), "rc": 0, "state": "joined", "token_expires_at": burst.iso_ms(cur)[:19] + "Z"}) + "\n")
    with open(os.path.join(ev, "roster.ndjson"), "w") as f, open(os.path.join(ev, "ps.ndjson"), "w") as g, open(os.path.join(ev, "liveness.ndjson"), "w") as h:
        for i in range(minutes + 1):
            ms = T0 + i * 60000
            f.write(json.dumps({"ms": ms, "sessions": [{"session_id": ids[n], "session_name": "soak-" + n, "state": "idle",
                                                        "last_seen_at": burst.iso_ms(ms - 5000)} for n in ids]}) + "\n")
            g.write(json.dumps({"ms": ms, "rows": [{"pid": 100 + j, "rss_kb": 20000 + 10 * i, "cpu": 0.1, "etime": "01:00", "args": a}
                                                   for j, a in enumerate(("claude --session-id x", "brigade watch", "brigade adapter supabase --profile bob message watch"))]}) + "\n")
            h.write(json.dumps({"ms": ms, "elapsed_s": i * 60, "session_log_bytes": {n: 1000 + 500 * i for n in ids}}) + "\n")
    for n, sid in ids.items():
        d = os.path.join(ev, n)
        os.makedirs(d)
        with open(os.path.join(d, "bypid-live.json"), "w") as f:
            json.dump({"brigade_session_id": sid, "profile": "bob", "inbound": "accept", "permission_mode": "default"}, f)
        with open(os.path.join(d, "beats.ndjson"), "w") as f:
            for k in range(beats_per_session):
                f.write(json.dumps({"k": k + 1, "kind": "beat", "outcome": "settled", "duration_ms": 30000}) + "\n")
        with open(os.path.join(d, "marks.ndjson"), "w") as f:
            f.write(json.dumps({"ms": T0, "event": "canary_ok"}) + "\n")
            for i in range(minutes * 12):
                f.write(json.dumps({"ms": T0 + i * 5000, "event": "drain", "n": i + 1}) + "\n")
        with open(os.path.join(d, "stop.json"), "w") as f:
            json.dump({"how": "exited", "session_log_bytes": 1000 + 500 * minutes}, f)
        with open(os.path.join(d, "watcher.log"), "w") as f:
            f.write(json.dumps({"time": burst.iso_ms(T0), "level": "INFO", "msg": "watch ready"}) + "\n")
            for k in range(acks_per_session):
                f.write(json.dumps({"time": burst.iso_ms(T0 + (k + 1) * 600000), "level": "INFO", "msg": "ack sent", "ids": 1}) + "\n")
        with open(os.path.join(d, "transcript.jsonl"), "w") as f:
            peer = ids["b" if n == "a" else "a"]
            for k in range(frames_per_session):
                mid = "%08x-0000-4000-8000-%012d" % (k, 7 if n == "a" else 8)
                f.write(json.dumps({"type": "user", "isMeta": True, "timestamp": burst.iso_ms(T0 + (k + 1) * 600000),
                                    "message": {"role": "user", "content": '<brigade-message team="ops" message-id="%s" reply-to-session-id="%s" hops="0">\nTICK\n</brigade-message>' % (mid, peer)}}) + "\n")
                f.write(json.dumps({"type": "assistant", "timestamp": burst.iso_ms(T0 + (k + 1) * 600000 + 5000), "message": {"stop_reason": "end_turn", "content": []}}) + "\n")
    os.makedirs(os.path.join(ev, "state", "logs"))
    with open(os.path.join(ev, "state", "logs", "adapter-bob.log"), "w") as f:
        f.write(json.dumps({"time": burst.iso_ms(T0), "level": "INFO", "msg": "backend answering again"}) + "\n")
    run = {"t0_ms": T0, "args": {"minutes": minutes, "profile": "bob"}, "token_expires_at_t0": burst.iso_ms(exp0)[:19] + "Z",
           "audit_t0": {"token_refreshed": 0, "token_revoked": 0}, "audit_t1": {"token_refreshed": rotations, "token_revoked": rotations},
           "refresh_beats": [{"expiry": burst.iso_ms(e)[:19] + "Z", "at_ms": e - 60000} for e in exps[:rotations]],
           "final": {"ok": True, "frame_in_b_ms": T0 + 119 * 60000 + 8000, "reply_rows": [{"message_id": "r"}], "reply_frame_in_a_ms": T0 + 119 * 60000 + 20000},
           "pre_exit": {n: {"pidfile": True, "map": True, "expect_alive": True} for n in ids},
           "after_exit": {n: {"pidfile_gone": True, "map_gone": True} for n in ids},
           "notice_files": [], "credential_file": {"exists": True, "parses": True, "mode": "0o600"},
           "claude_version_t0": "2.1.261 (Claude Code)", "claude_version_t1": "2.1.261 (Claude Code)",
           "stack_t0": {"pg_postmaster_start_time": "x"}, "stack_t1": {"pg_postmaster_start_time": "x"},
           "sessions_started": 2, "finished": "y", "stops": {n: {} for n in ids}}
    with open(os.path.join(root, "soak-run.json"), "w") as f:
        json.dump(run, f)
    bev = os.path.join(root, "evidence", "burst")
    os.makedirs(os.path.join(bev, "hints"))
    os.makedirs(os.path.join(bev, "messages"))
    with open(os.path.join(bev, "hints", "b1.json"), "w") as f:
        json.dump({"hints": 1000, "sent_total": 1000, "fire_wall_ms": 9500, "batches": [{"rc": 0, "sent": 100, "issued_ms": T0 + 3600000 + k * 1000} for k in range(10)],
                   "realtime_rows_on_topic": [0, 1000], "drains_within_3s_of_end": 12, "drains_by_end_of_window": 14, "background_drains_per_30s": 2,
                   "watcher_slice": {"ack_sent": 0, "child_failed": 0, "watch_status": []}, "transcript_slice": {"frames": 0, "drop_notices": 0, "rate_notices": 0},
                   "canaries": {"during": {"ok": True, "rtt_ms": 4000, "submitted_at_ms": T0 + 3605000}, "after": {"ok": True, "rtt_ms": 3500, "submitted_at_ms": T0 + 3660000}}}, f)
    dropped_ids = ["%08x-0000-4000-8000-%012d" % (k, 9) for k in range(drops)]
    notice = "Brigade: %d messages dropped from the injection queue (limit 50); they remain on the server and will be delivered later" % drops
    with open(os.path.join(bev, "messages", "b2.json"), "w") as f:
        json.dump({"senders": 6, "per_sender": 10, "constructed_condition": False, "t0_ms": T0 + 5400000,
                   "sends": {"issued": 60, "accepted": 60, "refused": 0, "refusal_codes": {}},
                   "watcher": {"dropped_ids": dropped_ids, "dropped_total": drops, "held_back_lines": 0, "deferred_lines": 0, "ack_ids_sum": 60 - drops, "injection_failed": 0},
                   "transcript": {"burst_frames_injected": 60 - drops, "frames_injected_more_than_once": [], "max_frames_per_sender_per_minute": 10,
                                  "drop_notices": [{"timestamp": burst.iso_ms(T0 + 5400000 + 2000), "text": notice}] * drop_notices, "rate_notices": []},
                   "server_after_60s": {"by_state": {"injected": 60 - drops, "accepted": drops}},
                   "dropped_after_6min": {"server_state": {i: "accepted" for i in dropped_ids}, "injected_occurrences": {i: 0 for i in dropped_ids}, "watcher_child_restarts": 0},
                   "kill_adapter_child": ({"constructed": True, "construction": "synthetic", "kill_at_s_after_t0": 361.0,
                                           "restart": {"restart_latency_ms": 900, "child_failed_line": {"code": "x"}},
                                           "redelivery": {"occurrences_after": {i: 1 for i in dropped_ids}, "all_injected_exactly_once": True,
                                                          "all_injected_on_server": True, "latency_from_kill_ms": 4000, "ack_ids_after_kill": drops},
                                           "ok": True} if drops > 0 else {"not_run": "no dropped ids to redeliver"}),
                   "bound_reached": drops > 0}, f)
    os.makedirs(os.path.join(root, "scans"))
    with open(os.path.join(root, "scans", "secret-scans.json"), "w") as f:
        json.dump({"all_clean": True, "all_controls_fired": True}, f)
    return root


def selftest(root):
    os.makedirs(root, exist_ok=True)
    results = {}
    green = _synthetic_bundle(os.path.join(root, "green"))
    s = score(green)
    results["green"] = s["green"]
    print("selftest: synthetic green bundle -> green=%s (%d acceptance clauses)" % (s["green"], len(s["acceptance"])))
    # byte-identical re-score
    first = open(os.path.join(green, "summary.json"), "rb").read()
    score(green)
    second = open(os.path.join(green, "summary.json"), "rb").read()
    results["byte_identical_rescore"] = first == second
    print("selftest: re-score byte-identical=%s" % (first == second))
    # the planted VACUOUS row: zero beats, zero frames, zero acks, everything else green
    vac = _synthetic_bundle(os.path.join(root, "vacuous"), beats_per_session=0, frames_per_session=0, acks_per_session=0)
    sv = score(vac)
    failed = sorted(k for k, v in sv["acceptance"].items() if not v.get("ok"))
    results["vacuous_caught"] = (not sv["green"]) and "7_workload_floor" in failed
    print("selftest: planted vacuous row (0 beats, 0 frames, 0 acks) -> green=%s, failing clauses=%s" % (sv["green"], failed))
    # a drop notice with ZERO drops
    nod = _synthetic_bundle(os.path.join(root, "notice-without-drops"), drops=0, drop_notices=1)
    sn = score(nod)
    failed2 = sorted(k for k, v in sn["acceptance"].items() if not v.get("ok"))
    results["notice_without_drops_caught"] = (not sn["green"]) and "11_one_drop_notice_zero_rate_notices" in failed2
    print("selftest: planted drop notice with zero drops -> green=%s, failing clauses=%s" % (sn["green"], failed2))
    # a rotation count the server does not corroborate (audit says 4 /token calls for 2 rotations: the flock not working)
    dbl = _synthetic_bundle(os.path.join(root, "double-token"))
    r = json.load(open(os.path.join(dbl, "soak-run.json")))
    r["audit_t1"] = {"token_refreshed": 4, "token_revoked": 4}
    json.dump(r, open(os.path.join(dbl, "soak-run.json"), "w"))
    sd = score(dbl)
    failed3 = sorted(k for k, v in sd["acceptance"].items() if not v.get("ok"))
    results["double_token_caught"] = (not sd["green"]) and "3_token_calls_equal_rotations" in failed3
    print("selftest: planted 4 /token calls for 2 rotations -> green=%s, failing clauses=%s" % (sd["green"], failed3))
    # the redelivery arm reporting a dropped id injected TWICE (or a --pause-watcher bundle): clause 10 must not pass
    twice = _synthetic_bundle(os.path.join(root, "redelivered-twice"))
    bp = os.path.join(twice, "evidence", "burst", "messages", "b2.json")
    b = json.load(open(bp))
    b["kill_adapter_child"]["redelivery"]["all_injected_exactly_once"] = False
    b["kill_adapter_child"]["ok"] = False
    json.dump(b, open(bp, "w"))
    st = score(twice)
    failed4 = sorted(k for k, v in st["acceptance"].items() if not v.get("ok"))
    results["redelivered_twice_caught"] = (not st["green"]) and "10_injection_bounded" in failed4
    print("selftest: planted redelivery arm with a dropped id injected twice -> green=%s, failing clauses=%s" % (st["green"], failed4))
    cons = _synthetic_bundle(os.path.join(root, "constructed-not-counted"))
    bp = os.path.join(cons, "evidence", "burst", "messages", "b2.json")
    b = json.load(open(bp))
    b["constructed_condition"] = True
    json.dump(b, open(bp, "w"))
    sc = score(cons)
    failed5 = sorted(k for k, v in sc["acceptance"].items() if not v.get("ok"))
    results["constructed_not_counted"] = (not sc["green"]) and "10_injection_bounded" in failed5 and sc["acceptance"]["10_injection_bounded"].get("counted") is False
    print("selftest: planted --pause-watcher (constructed) B2 -> green=%s, failing clauses=%s" % (sc["green"], failed5))
    ok = all(results.values())
    print("selftest: %s %s" % ("ok:" if ok else "FAIL:", json.dumps(results, sort_keys=True)))
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("bundle", nargs="?")
    ap.add_argument("--selftest", default=None, help="build a synthetic bundle under this directory and prove the detectors")
    args = ap.parse_args()
    if args.selftest:
        raise SystemExit(selftest(args.selftest))
    if not args.bundle:
        ap.error("bundle required")
    s = score(args.bundle)
    acc = s["acceptance"]
    for k in sorted(acc):
        print("%s %s" % ("ok:" if acc[k].get("ok") else "FAIL:", k))
    print("measured: green=%s; wrote %s" % (s["green"], os.path.join(args.bundle, "summary.json")))


if __name__ == "__main__":
    main()
