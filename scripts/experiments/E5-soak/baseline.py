#!/usr/bin/env python3
"""E5-soak baseline (brief section 3, M1-M4): the no-model, no-Claude
measurements the soak's predicates rest on, made through the engine so the
engine itself is exercised before a session is spent.

    python3 scripts/experiments/E5-soak/baseline.py [--out <dir>] [--skip m2]

  M1  `profile status` is a PROBE: twenty reads while a real watcher + adapter
      pair of the same profile is live; token_expires_at constant, exit 0
      every time, the principal's auth.refresh_tokens family unchanged.
  M2  the server-side rotation instrument on a THROWAWAY principal: the real
      column set of auth.refresh_tokens, what one refresh does to it, what a
      two-steps-behind token gets (E0-6's rule), that the family survives, the
      adapter's own terminal branch fired deliberately, and what a globally
      signed-out (revoked) family looks like.
  M3  ten fabricated hints from SQL against the live watcher's topic: the
      drain count moves (pg_stat_statements), no `message` event, nothing
      injected (the sink stays empty), the channel stays joined; the
      background drain rate is measured first so the ten hints' drains are
      attributable.
  M4  from code, by grep: a client cannot broadcast on the topic.

No `claude` is started. The local stack is never stopped or restarted.
Every psql, adapter and docker invocation's output goes to files under --out.
"""
import argparse
import base64
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.request

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import e5s  # noqa: E402

REPO = e5s.REPO


def log(line):
    print(line, flush=True)


def wait_for_line(path, needle, timeout):
    t0 = time.monotonic()
    while time.monotonic() - t0 < timeout:
        if os.path.exists(path):
            with open(path, errors="replace") as f:
                if needle in f.read():
                    return int((time.monotonic() - t0) * 1000)
        time.sleep(0.25)
    return -1


def count_lines(path, needle):
    if not os.path.exists(path):
        return 0
    with open(path, errors="replace") as f:
        return sum(1 for line in f if needle in line)


def last_watch_status(path):
    last = None
    if os.path.exists(path):
        with open(path, errors="replace") as f:
            for line in f:
                if '"msg":"watch status"' in line:
                    try:
                        last = json.loads(line)
                    except ValueError:
                        pass
    return last


def jwt_claims(token):
    """The unverified payload of a JWT (the adapter reads `exp` the same
    way). The token itself is never written anywhere."""
    part = token.split(".")[1]
    part += "=" * (-len(part) % 4)
    return json.loads(base64.urlsafe_b64decode(part))


def _write_cred(path, doc):
    tmp = path + ".tmp"
    with open(tmp, "w") as f:
        json.dump(doc, f)
    os.chmod(tmp, 0o600)
    os.replace(tmp, path)


def forge_expired(token, seconds_ago=600):
    """The same JWT with its payload's `exp` moved into the past. Nothing
    verifies the signature locally (the adapter reads only `exp`, 5.1) and the
    server never sees it (a refresh presents the REFRESH token), so this makes
    the adapter take its refresh path on the next command: the deliberate
    drive of its terminal branch (M2)."""
    h, payload, sig = token.split(".")
    part = payload + "=" * (-len(payload) % 4)
    claims = json.loads(base64.urlsafe_b64decode(part))
    claims["exp"] = int(time.time()) - seconds_ago
    enc = base64.urlsafe_b64encode(json.dumps(claims, separators=(",", ":")).encode()).decode().rstrip("=")
    return ".".join((h, enc, sig))


def audit_counts(stack, uid):
    """GoTrue's audit trail (auth.audit_log_entries) for one principal:
    token_refreshed / token_revoked / logout counts. A real rotation is a
    token_refreshed paired with a token_revoked; a one-behind redemption is a
    token_refreshed alone; a refused /token leaves nothing. This is the
    attributable server-side /token witness of brief 4.6 item 2."""
    rc, out, _ = stack.psql("select payload->>'action', count(*) from auth.audit_log_entries"
                            " where payload->>'actor_id' = :'uid' group by 1 order by 1", {"uid": uid})
    d = {}
    for line in out.split("\n"):
        if "|" in line:
            k, v = line.split("|", 1)
            d[k] = int(v)
    return d


class GoTrue:
    """M2's by-hand refresher: the three GoTrue calls of plan 5.1 through
    urllib. Tokens live in this process's memory only; every record written
    carries statuses, codes and booleans, never a token."""

    def __init__(self, url, key):
        self.url = url
        self.key = key

    def refresh(self, refresh_token):
        body = json.dumps({"refresh_token": refresh_token}).encode()
        req = urllib.request.Request(self.url + "/auth/v1/token?grant_type=refresh_token", data=body, method="POST",
                                     headers={"apikey": self.key, "Content-Type": "application/json",
                                              "X-Supabase-Api-Version": "2024-01-01"})
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                return r.status, json.loads(r.read().decode())
        except urllib.error.HTTPError as e:
            try:
                return e.code, json.loads(e.read().decode())
            except Exception:
                return e.code, {}

    def logout(self, access_token):
        req = urllib.request.Request(self.url + "/auth/v1/logout?scope=global", data=b"", method="POST",
                                     headers={"apikey": self.key, "Authorization": "Bearer " + access_token,
                                              "X-Supabase-Api-Version": "2024-01-01"})
        try:
            with urllib.request.urlopen(req, timeout=20) as r:
                return r.status
        except urllib.error.HTTPError as e:
            return e.code


def auth_log_token_calls(since_iso):
    """Lines of the auth container's log since `since_iso` that name /token
    (read-only `docker logs`); the count is the server-side /token witness
    candidate of 4.6 item 2."""
    r = subprocess.run(["docker", "logs", "--since", since_iso, "supabase_auth_brigade"],
                       capture_output=True, text=True, timeout=60)
    text = r.stdout + r.stderr
    hits = [ln for ln in text.split("\n") if "/token" in ln]
    return len(hits), hits[:5]


def m1(stack, out, rep):
    log("--- M1: the rotation probe is read-only ---")
    w = stack.launch_sink_watcher("bob", "soak-probe-a")
    ms = wait_for_line(w["log"], '"msg":"watch ready"', 60)
    if ms < 0:
        log("FAIL: M1: bob's sink watcher never became ready (see %s)" % w["log"])
        rep["m1"] = {"ready": False}
        return None
    log("measured: M1 sink watcher start->ready %d ms (real `brigade watch` + `adapter supabase message watch` child)" % ms)
    ms2 = wait_for_line(w["log"], '"detail":"joined"', 30)
    log("measured: M1 watch status live/joined after %d ms" % ms2)
    # the principal and its family before
    st0 = stack.profile_status("bob", json_flag=True)
    st0b = stack.profile_status("bob", json_flag=False)
    json_ok = st0.get("rc") == 0 and st0.get("token_expires_at")
    plain_ok = st0b.get("rc") == 0 and st0b.get("token_expires_at")
    log("measured: M1 `profile status --json` rc=%s token_expires_at=%s; without --json rc=%s token_expires_at=%s"
        % (st0.get("rc"), bool(st0.get("token_expires_at")), st0b.get("rc"), bool(st0b.get("token_expires_at"))))
    json_flag = bool(json_ok)
    principal = st0.get("principal_ref") or st0b.get("principal_ref")
    fam0 = stack.refresh_family(principal)
    alog0 = os.path.getsize(w["adapter_log"]) if os.path.exists(w["adapter_log"]) else 0
    since = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    pss0 = stack.pss_calls("fetch_inbox")
    reads = []
    t0 = time.monotonic()
    for i in range(20):
        reads.append(stack.profile_status("bob", json_flag=json_flag))
    elapsed = int((time.monotonic() - t0) * 1000)
    fam1 = stack.refresh_family(principal)
    alog1 = os.path.getsize(w["adapter_log"]) if os.path.exists(w["adapter_log"]) else 0
    tok_calls, tok_lines = auth_log_token_calls(since)
    exits = sorted(set(r["rc"] for r in reads))
    expiries = sorted(set(r.get("token_expires_at") or "" for r in reads))
    states = sorted(set(r.get("state") or "" for r in reads))
    rows_before = sum(x["rows"] for x in fam0)
    rows_after = sum(x["rows"] for x in fam1)
    ctr_before = [x["refresh_token_counter"] for x in fam0]
    ctr_after = [x["refresh_token_counter"] for x in fam1]
    ok = (exits == [0] and len(expiries) == 1 and expiries[0] != "" and rows_before == rows_after
          and ctr_before == ctr_after and states == ["joined"])
    log("%s M1: 20 reads in %d ms: exits=%s, token_expires_at constant=%s (%s), state=%s, refresh_tokens rows %d -> %d, "
        "refresh_token_counter %s -> %s, adapter log bytes %d -> %d, auth-log /token lines in the window: %d"
        % ("ok:" if ok else "FAIL:", elapsed, exits, len(expiries) == 1, expiries[0][:25] if expiries else "", states,
           rows_before, rows_after, ctr_before, ctr_after, alog0, alog1, tok_calls))
    pss1 = stack.pss_calls("fetch_inbox")
    log("measured: M1 pg_stat_statements fetch_inbox calls %s -> %s across the 20 reads (the live watcher's own drains)" % (pss0, pss1))
    rep["m1"] = {"ready_ms": ms, "joined_ms": ms2, "json_flag_accepted": json_flag, "plain_accepted": bool(plain_ok),
                 "reads": reads, "exits": exits, "expiries": expiries, "states": states, "elapsed_ms": elapsed,
                 "principal": principal, "family_before": fam0, "family_after": fam1,
                 "adapter_log_bytes": [alog0, alog1], "auth_log_token_lines": tok_calls, "auth_log_sample": tok_lines,
                 "pss_fetch_inbox": [pss0, pss1], "ok": ok, "watcher": {k: w[k] for k in ("pid", "session_id", "log", "sink")}}
    e5s._w(os.path.join(out, "m1.json"), json.dumps(rep["m1"], indent=2, default=str))
    return w


def m2(stack, out, rep):
    log("--- M2: the server-side rotation instrument, on a throwaway principal ---")
    rc, out_ddl, err = stack.psql("\\d auth.refresh_tokens\n\\d auth.sessions\nselect version();")
    e5s._w(os.path.join(out, "m2-ddl.txt"), out_ddl + "\n" + err)
    cols = re.findall(r"^([a-z_]+)\|", out_ddl, re.M)
    log("measured: M2 auth.refresh_tokens columns on this build: %s" % ",".join(c for c in cols if c in
        ("instance_id", "id", "token", "user_id", "revoked", "created_at", "updated_at", "parent", "session_id")))
    # a throwaway principal in its own team
    rc = stack._adapter("probe", stack.cap, "profile-init-probe", "profile", "init", "--url", stack.url, "--key", stack.key)
    secret = os.path.join(stack.scratch, "probe.secret")
    rc = stack._adapter("probe", stack.cap, "team-create-probe", "team", "create", "--secret-file", secret,
                        stdin=json.dumps({"team_name": "probe-team", "human_label": "probe@proof.invalid"}))
    if os.path.exists(secret):
        os.remove(secret)
    if rc != 0:
        log("FAIL: M2: the throwaway principal could not be minted (rc=%s)" % rc)
        rep["m2"] = {"ok": False}
        return
    sf = os.path.join(stack.cfg, "profiles", "probe", "session.json")
    with open(sf) as f:
        cred = json.load(f)
    claims = jwt_claims(cred["access_token"])
    uid = claims.get("sub")
    auth_sid = claims.get("session_id")
    gt = GoTrue(stack.url, stack.key)
    steps = []

    def snap(label, extra=None):
        fam = stack.refresh_family(uid)
        aud = audit_counts(stack, uid)
        rec = {"step": label, "family": fam, "audit": aud, "t": e5s.iso_now()}
        if extra:
            rec.update(extra)
        steps.append(rec)
        mine = [x for x in fam if x["auth_session_id"] == auth_sid]
        m = mine[0] if mine else {}
        log("measured: M2 %-52s rows=%s revoked=%s parent=%s audit=%s%s"
            % (label, m.get("rows"), m.get("revoked_rows"), m.get("rows_with_parent"),
               json.dumps(aud, sort_keys=True), (" " + json.dumps(extra)) if extra else ""))

    snap("after sign-up (T0 on disk)")
    # refresh #1: T0 -> T1
    s1, r1 = gt.refresh(cred["refresh_token"])
    snap("refresh #1 with T0", {"status": s1, "rotated": bool(r1.get("refresh_token")) and r1.get("refresh_token") != cred["refresh_token"]})
    time.sleep(12)   # past the 10 s reuse interval, so dedup cannot explain what follows (E0-6)
    s2, r2 = gt.refresh(r1["refresh_token"])
    snap("refresh #2 with T1", {"status": s2, "rotated": bool(r2.get("refresh_token")) and r2.get("refresh_token") != r1.get("refresh_token")})
    time.sleep(12)
    # two behind: T0 again
    s3, r3 = gt.refresh(cred["refresh_token"])
    snap("present T0 (two behind)", {"status": s3, "code": r3.get("code") or r3.get("error_code")})
    time.sleep(2)
    # the active token still works: the family survives
    s4, r4 = gt.refresh(r2["refresh_token"])
    snap("present T2 (active) after the refusal", {"status": s4, "rotated": bool(r4.get("refresh_token"))})
    time.sleep(12)
    # one behind: T2 (T3 is active now) -> 200 carrying the ACTIVE token
    s5, r5 = gt.refresh(r2["refresh_token"])
    snap("present T2 (one behind)", {"status": s5, "returned_active_token": r5.get("refresh_token") == r4.get("refresh_token")})
    # THE ADAPTER'S OWN TERMINAL BRANCH, deliberately (brief M2: "drive it deliberately"): the file still
    # holds T0 (four behind now); its access token is rewritten with an `exp` in the past so the next command
    # must refresh; `session list` then presents T0, gets refresh_token_already_used, re-reads the same file,
    # and goes terminal: exit 4, the Warn line, session.json deleted (credentials.go refreshCredential).
    # A directly-run adapter logs to its stderr (the harness's adapterclient is what redirects a CHILD's
    # stderr into logs/adapter-<profile>.log), and `_adapter` captures that stderr per invocation.
    def terminal_lines(name):
        return count_lines(os.path.join(stack.cap, "%s.err" % name), "credential is terminal; removing session.json")
    term0 = 0
    stale = dict(cred)
    stale["access_token"] = forge_expired(cred["access_token"])
    _write_cred(sf, stale)
    rc_term = stack._adapter("probe", stack.cap, "probe-list-stale", "session", "list")
    term1 = term0 + terminal_lines("probe-list-stale")
    snap("adapter `session list` on the stale file (forged-expired JWT)",
         {"rc": rc_term, "session_json_exists": os.path.exists(sf), "terminal_lines": [term0, term1]})
    # what a globally signed-out (REVOKED) family looks like: give the adapter the ACTIVE credential (T3, from
    # the one-behind answer) and run `profile revoke-credentials` (signOut refreshes first, then /logout global)
    fresh = dict(r5) if (s5 == 200 and r5.get("refresh_token")) else dict(r4)
    fresh["last_team_ref"] = cred.get("last_team_ref")
    _write_cred(sf, fresh)
    rc_rev = stack._adapter("probe", stack.cap, "probe-revoke-active", "profile", "revoke-credentials")
    snap("adapter revoke-credentials on the ACTIVE credential (global sign-out)",
         {"rc": rc_rev, "session_json_exists": os.path.exists(sf),
          "auth_session_row_present": any(x["auth_session_id"] == auth_sid for x in stack.refresh_family(uid))})
    s6, r6 = gt.refresh(fresh["refresh_token"])
    snap("present the signed-out family's token", {"status": s6, "code": r6.get("code") or r6.get("error_code")})
    # and the adapter on a signed-out credential: terminal at once (refresh_token_not_found), exit 4
    gone = dict(fresh)
    gone["access_token"] = forge_expired(fresh["access_token"])
    _write_cred(sf, gone)
    rc_term2 = stack._adapter("probe", stack.cap, "probe-list-revoked", "session", "list")
    term2 = term1 + terminal_lines("probe-list-revoked")
    snap("adapter `session list` on the signed-out credential",
         {"rc": rc_term2, "session_json_exists": os.path.exists(sf), "terminal_lines": [term1, term2]})
    rc, rc2 = rc_term, rc_rev
    del cred, r1, r2, r3, r4, r5, r6, fresh, stale, gone
    ok = (s1 == 200 and s2 == 200 and s3 == 400 and s4 == 200 and s5 == 200 and rc_term == 4 and term1 == term0 + 1
          and rc_rev == 0 and s6 in (400, 401, 403) and rc_term2 == 4 and term2 == term1 + 1)
    log("%s M2: refresh 200/200, two-behind %s, active-after-refusal %s, one-behind %s, adapter terminal on the stale file rc=%s "
        "(Warn lines +%d), revoke-credentials rc=%s, signed-out token %s, adapter terminal on the signed-out credential rc=%s (Warn lines +%d)"
        % ("ok:" if ok else "FAIL:", s3, s4, s5, rc_term, term1 - term0, rc_rev, s6, rc_term2, term2 - term1))
    rep["m2"] = {"ok": ok, "principal": uid, "auth_session_id": auth_sid, "columns": cols, "steps": steps,
                 "statuses": {"refresh1": s1, "refresh2": s2, "two_behind": s3, "active_after_refusal": s4,
                              "one_behind": s5, "adapter_stale_list_rc": rc, "adapter_revoke_rc": rc2, "signed_out": s6,
                              "adapter_revoked_list_rc": rc_term2, "terminal_warn_lines": [term0, term1, term2]}}
    e5s._w(os.path.join(out, "m2.json"), json.dumps(rep["m2"], indent=2, default=str))


def m3(stack, w, out, rep):
    log("--- M3: a fabricated hint is indistinguishable from a real one ---")
    sid = w["session_id"]
    inbox = stack.inbox_state(sid)
    log("measured: M3 inbox of %s before: %s" % (sid, inbox or "{}"))
    # the background drain rate over a quiet 60 s (the 30 s live timer)
    p0 = stack.pss_calls("fetch_inbox")
    t0 = time.monotonic()
    time.sleep(60)
    p1 = stack.pss_calls("fetch_inbox")
    bg = (p1 - p0) if (p0 is not None and p1 is not None) else None
    log("measured: M3 background fetch_inbox calls over %.1f s with no hints: %s (the 30 s live timer of one watcher)"
        % (time.monotonic() - t0, bg))
    rt0 = stack.realtime_rows(sid)
    offered0 = count_lines(w["log"], '"msg":"message offered"')
    acks0 = count_lines(w["log"], '"msg":"ack sent"')
    status0 = last_watch_status(w["log"])
    sink0 = os.path.getsize(w["sink"]) if os.path.exists(w["sink"]) else 0
    p2 = stack.pss_calls("fetch_inbox")
    ts = time.monotonic()
    rc, sent, raw = stack.hints(sid, 10, os.path.join(out, "m3-hints-psql.out"))
    t_fire = time.monotonic() - ts
    time.sleep(3)
    p3 = stack.pss_calls("fetch_inbox")
    time.sleep(5)
    p4 = stack.pss_calls("fetch_inbox")
    rt1 = stack.realtime_rows(sid)
    offered1 = count_lines(w["log"], '"msg":"message offered"')
    acks1 = count_lines(w["log"], '"msg":"ack sent"')
    status1 = last_watch_status(w["log"])
    sink1 = os.path.getsize(w["sink"]) if os.path.exists(w["sink"]) else 0
    alog_warn = 0
    if os.path.exists(w["adapter_log"]):
        alog_warn = count_lines(w["adapter_log"], '"level":"WARN"') + count_lines(w["adapter_log"], '"level":"ERROR"')
    drains3 = (p3 - p2) if (p2 is not None and p3 is not None) else None
    drains8 = (p4 - p2) if (p2 is not None and p4 is not None) else None
    joined = bool(status1 and status1.get("state") == "live")
    ok = (rc == 0 and sent == 10 and (int(rt1) - int(rt0)) == 10 and offered1 == offered0 and acks1 == acks0
          and sink1 == sink0 and joined and (drains3 or 0) >= 1)
    log("%s M3: psql rc=%d sent=%s in %.3f s; realtime.messages rows on the topic %s -> %s; fetch_inbox calls +%s within 3 s "
        "(+%s within 8 s) against a background of %s/60 s; message events offered %d -> %d; ack sent %d -> %d; sink bytes %d -> %d; "
        "last watch status %s; adapter WARN/ERROR lines %d"
        % ("ok:" if ok else "FAIL:", rc, sent, t_fire, rt0, rt1, drains3, drains8, bg, offered0, offered1, acks0, acks1,
           sink0, sink1, (status1 or {}).get("state"), alog_warn))
    rep["m3"] = {"ok": ok, "session_id": sid, "inbox_before": inbox, "background_drains_per_60s": bg,
                 "psql_rc": rc, "sent": sent, "fire_seconds": t_fire, "realtime_rows": [rt0, rt1],
                 "fetch_inbox_calls": {"before": p2, "after_3s": p3, "after_8s": p4},
                 "offered": [offered0, offered1], "acks": [acks0, acks1], "sink_bytes": [sink0, sink1],
                 "watch_status_before": status0, "watch_status_after": status1, "adapter_warn_error_lines": alog_warn}
    e5s._w(os.path.join(out, "m3.json"), json.dumps(rep["m3"], indent=2, default=str))


def m4(out, rep):
    log("--- M4: a client cannot broadcast (from code) ---")
    mig = os.path.join(REPO, "supabase", "migrations", "20260830120100_brigade_realtime.sql")
    found = {}
    with open(mig) as f:
        lines = f.read().split("\n")
    for i, line in enumerate(lines, 1):
        if "downgrades the refused insert to a" in line:
            found["warning_downgrade"] = i
        if "No insert policy: clients cannot broadcast; only the database emits" in line:
            found["no_insert_policy"] = i
        if "for select to authenticated" in line:
            found["select_only_policy"] = i
        if "perform realtime.send(" in line:
            found["trigger_send_call"] = i
    it = os.path.join(REPO, "internal", "adapters", "supabase", "realtime_integration_test.go")
    with open(it) as f:
        t = f.read().split("\n")
    for i, line in enumerate(t, 1):
        if line.startswith("type rawSocket struct"):
            found["rawSocket_type"] = i
        if "func (s *rawSocket) join(" in line:
            found["rawSocket_join"] = i
        if "func (s *rawSocket) broadcasts(" in line:
            found["rawSocket_broadcasts"] = i
    has_broadcast_helper = any(re.search(r"func \(s \*rawSocket\) (send|broadcast|push)\(", ln) for ln in t)
    ok = all(k in found for k in ("warning_downgrade", "no_insert_policy", "select_only_policy", "trigger_send_call",
                                  "rawSocket_join", "rawSocket_broadcasts")) and not has_broadcast_helper
    log("%s M4: %s; rawSocket has a broadcast helper: %s" % ("ok:" if ok else "FAIL:", json.dumps(found), has_broadcast_helper))
    rep["m4"] = {"ok": ok, "lines": found, "rawSocket_has_broadcast_helper": has_broadcast_helper}
    e5s._w(os.path.join(out, "m4.json"), json.dumps(rep["m4"], indent=2))


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--out", default=None)
    ap.add_argument("--skip", default="")
    ap.add_argument("--only", default="", help="m2: only the throwaway-principal instrument (no alice/bob/dana)")
    args = ap.parse_args()
    out = args.out or os.path.join(REPO, ".ignored", "tools", "p5-11", "author", "baseline-" + e5s.now_stamp())
    os.makedirs(out, exist_ok=True)
    pre = e5s.preconditions(need_claude=False)
    log("ok: preconditions (brigade=%s, claude_cfg=%s)" % (pre["brigade"], pre["claude_cfg"]))
    rep = {"out": out, "started": e5s.iso_now(), "stack": None}
    stack = e5s.Stack("base", log=log)
    log("ok: temp root %s" % stack.root)
    try:
        rep["stack"] = stack.stack_uptime()
        log("measured: postgres started %s" % rep["stack"]["pg_postmaster_start_time"])
        if args.only == "m2":
            m2(stack, out, rep)
        else:
            stack.provision(with_sender=False)
            w = m1(stack, out, rep)
            if "m2" not in args.skip:
                m2(stack, out, rep)
            if w:
                m3(stack, w, out, rep)
            m4(out, rep)
    finally:
        rep["teardown"] = stack.teardown(scan_roots=[out], bundle_cap=os.path.join(out, "cap"))
        rep["finished"] = e5s.iso_now()
        e5s._w(os.path.join(out, "baseline.json"), json.dumps(rep, indent=2, default=str))
        log("teardown: %s" % json.dumps({k: rep["teardown"].get(k) for k in ("watchers_killed", "project_dirs_removed")}))
        log("ok: baseline written to %s" % out)


if __name__ == "__main__":
    main()
