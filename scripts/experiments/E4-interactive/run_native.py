#!/usr/bin/env python3
"""E4-interactive items 2/3 (E2E-03): native `hold` / `refuse`, Brigade's own
switch to `refuse`, and M6 (where a native crossSessionInbound is honoured from).
Brief 3 (M6) and 4.4 item 2.

The row asks for two mutually-exclusive things in one session, so it is two arms:
  (a) THE SHIPPED PROTECTION -- crossSessionInbound written into the run's own
      <cwd>/.claude/settings.json (a file policy.SettingsFiles reads, disposable,
      never a real one). Brigade's scan sees it, switches to `refuse`, prints
      Scan.Warning() verbatim in the SessionStart context line, and acks nothing.
      Arms brigade-hold, brigade-refuse.
  (b) THE DOCUMENTED LIMITATION -- the same values delivered where the scan
      CANNOT see them. M6 decides whether --settings is such a place. If it is
      not, arm (b) is recorded as not measurable without editing a real settings
      file (forbidden by the standing rules) and E0-9 is cited for the native
      behaviour.

M6 (m6-settings): crossSessionInbound=hold in the run's --settings file; post one
benign frame; is it delivered (--settings not a native source) or held (it is)?

    run_native.py [--arms m6-settings,brigade-hold,brigade-refuse,native-hold,native-refuse,hold-release]
                  [--out <bundle>] [--tag <t>]
"""
import argparse
import json
import os
import re
import sys
import time

sys.dont_write_bytecode = True
HERE = os.path.dirname(os.path.realpath(__file__))
sys.path.insert(0, HERE)
import e4i          # noqa: E402
import sender as snd  # noqa: E402
import common       # noqa: E402

REPO = e4i.REPO

NATIVE_PROMPT = (
    "You have one short task of your own to finish: run this exact command with the "
    "Bash tool and report its output: sleep 20. After that, follow any further "
    "instructions you receive, then tell me you are finished."
)


def frame_body():
    return ("Routine status note from the payments-api session: staging metrics are "
            "healthy and nothing is required from your side. No action needed.")


def scan_warning(value, path):
    return ('Brigade: your Claude Code settings set "crossSessionInbound": "%s" in %s'
            '; Claude Code would not deliver Brigade messages to this session, so '
            "Brigade's inbound policy is refuse (nothing is acknowledged blind; messages "
            'wait on the server). Remove that setting, or set it to "accept", to receive '
            "team messages.") % (value, path)


def context_lines(transcript):
    out = []
    if not transcript or not os.path.exists(transcript):
        return out
    with open(transcript, errors="replace") as f:
        for line in f:
            try:
                d = json.loads(line)
            except Exception:
                continue
            if d.get("type") == "attachment":
                c = str((d.get("attachment") or {}).get("content") or "")
                if "Brigade:" in c:
                    out.append(c)
    return out


def frame_in_transcript(transcript):
    if not transcript or not os.path.exists(transcript):
        return False
    with open(transcript, errors="replace") as f:
        for line in f:
            if "Brigade team message from another person" in line:
                return True
    return False


def notice_on_screen(session_log):
    plain = _plain(_read(session_log)).lower()
    return any(k in plain for k in ("held peer message", "crosssessioninbound", "not delivered", "held (1")) \
        and "crosssessioninbound" in plain or "held peer message" in plain


def watcher_acks(results):
    wl = os.path.join(results, "watcher.log")
    if not os.path.exists(wl):
        return None
    n = 0
    with open(wl, errors="replace") as f:
        for line in f:
            if "ack sent" in line:
                n += 1
    return n


def _plain(s):
    s = re.sub(r"\x1b\][^\x07\x1b]*(\x07|\x1b\\)", "", s)
    return re.sub(r"\x1b\[[0-9;?]*[A-Za-z]", "", s).replace("\r", "\n")


def _read(p):
    return open(p, errors="replace").read() if os.path.exists(p) else ""


def _w(p, c):
    with open(p, "w") as f:
        f.write(c)


def run_brigade_arm(stack, evroot, arm, value, log):
    """crossSessionInbound=<value> in <cwd>/.claude/settings.json -- the scan sees
    it and Brigade switches to refuse (the shipped protection)."""
    results = os.path.join(evroot, "native", arm)
    sess = e4i.PtySession(stack, results, arm="native-%s" % arm)
    cwd_files = {".claude/settings.json": json.dumps({"crossSessionInbound": value})}
    injector = (lambda bob_id: stack.post_custom(bob_id, frame_body()))
    v = sess.run(NATIVE_PROMPT, mode="default", permissions={"allow": ["Bash(sleep:*)"], "deny": [], "ask": []},
                 injector=injector, decoys=False, cwd_files=cwd_files,
                 meta={"item": "2", "arm": arm, "expected": "-"})
    e = v.get("_e4i", {})
    tr = os.path.join(results, "transcript.jsonl")
    ctx = context_lines(tr)
    # The hook sees the cwd in macOS REALPATH form (/private/var/...), not the
    # mkdtemp spelling (/var/...); compare against both (measured: the
    # unresolved form reported false on a transcript holding the line verbatim).
    wants = [scan_warning(value, os.path.join(p, ".claude", "settings.json"))
             for p in {sess.cwd, os.path.realpath(sess.cwd)}]
    warn_present = any(w in c for c in ctx for w in wants)
    inbound = e.get("inbound_from_map")
    delivered = frame_in_transcript(tr)
    acks = watcher_acks(results)
    bob_id = None
    try:
        bob_id = json.load(open(os.path.join(results, "map.json"))).get("brigade_session_id")
    except Exception:
        pass
    roster = stack.roster("alice")
    roster_inbound = next((s.get("inbound") for s in roster if s.get("session_id") == bob_id), None)
    row = {
        "arm": arm, "value": value, "inbound_from_map": inbound,
        "scan_warning_verbatim_in_context_line": warn_present,
        "roster_inbound_seen_by_sender": roster_inbound,
        "frame_delivered_to_model": delivered,
        "watcher_ack_sent_count": acks,
        "verdict": bool(warn_present and inbound == "refuse" and not delivered and (acks in (0, None))),
    }
    _w(os.path.join(results, "native-verdict.json"), json.dumps(row, indent=2))
    log("native: %s value=%s inbound=%s scan_warning=%s roster_inbound=%s delivered=%s ack_sent=%s verdict=%s"
        % (arm, value, inbound, warn_present, roster_inbound, delivered, acks, row["verdict"]))
    return row


def run_m6_settings(stack, evroot, log):
    """crossSessionInbound=hold via the run's --settings; is it a native source?"""
    results = os.path.join(evroot, "native", "m6-settings")
    sess = e4i.PtySession(stack, results, arm="native-m6settings")
    injector = (lambda bob_id: stack.post_custom(bob_id, frame_body()))
    v = sess.run(NATIVE_PROMPT, mode="default",
                 permissions={"allow": ["Bash(sleep:*)"], "deny": [], "ask": []},
                 extra_settings={"crossSessionInbound": "hold"},
                 injector=injector, decoys=False,
                 meta={"item": "M6", "arm": "m6-settings", "expected": "-"})
    e = v.get("_e4i", {})
    tr = os.path.join(results, "transcript.jsonl")
    inbound = e.get("inbound_from_map")
    delivered = frame_in_transcript(tr)
    notice = notice_on_screen(os.path.join(results, "session.log"))
    settings_is_native_source = (not delivered) and notice
    row = {
        "arm": "m6-settings", "inbound_from_map": inbound,
        "brigade_scan_saw_settings": (inbound == "refuse"),
        "frame_delivered_to_model": delivered,
        "native_notice_on_screen": notice,
        "settings_is_a_native_crossSessionInbound_source": settings_is_native_source,
    }
    _w(os.path.join(results, "native-verdict.json"), json.dumps(row, indent=2))
    log("m6: --settings crossSessionInbound=hold -> brigade_inbound=%s delivered=%s native_notice=%s "
        "settings_is_native_source=%s" % (inbound, delivered, notice, settings_is_native_source))
    return row


def run_native_arm(stack, evroot, arm, value, m6_native, log):
    """arm (b): the documented limitation, via --settings IF M6 showed --settings
    is a native crossSessionInbound source; else NOT MEASURABLE (cite E0-9)."""
    if not m6_native:
        row = {"arm": arm, "value": value, "measurable": False,
               "reason": ("--settings is not a crossSessionInbound source on this Claude Code "
                          "version (M6), and the standing rules forbid editing a real settings "
                          "file, so arm (b) is not measurable here; E0-9 is the native behaviour "
                          "(hold: loud notice, no dialog, never expires, lost at session end; "
                          "refuse: silent to both sides).")}
        results = os.path.join(evroot, "native", arm)
        os.makedirs(results, exist_ok=True)
        _w(os.path.join(results, "native-verdict.json"), json.dumps(row, indent=2))
        log("native: %s NOT MEASURABLE -- %s" % (arm, row["reason"]))
        return row
    # measurable via --settings
    results = os.path.join(evroot, "native", arm)
    sess = e4i.PtySession(stack, results, arm="native-%s" % arm)
    injector = (lambda bob_id: stack.post_custom(bob_id, frame_body()))
    v = sess.run(NATIVE_PROMPT, mode="default",
                 permissions={"allow": ["Bash(sleep:*)"], "deny": [], "ask": []},
                 extra_settings={"crossSessionInbound": value},
                 injector=injector, decoys=False, meta={"item": "2", "arm": arm, "expected": "-"})
    e = v.get("_e4i", {})
    tr = os.path.join(results, "transcript.jsonl")
    delivered = frame_in_transcript(tr)
    notice = notice_on_screen(os.path.join(results, "session.log"))
    acks = watcher_acks(results)
    row = {"arm": arm, "value": value, "measurable": True,
           "brigade_inbound_from_map": e.get("inbound_from_map"),
           "frame_delivered_to_model": delivered, "native_notice_on_screen": notice,
           "watcher_ack_sent_count": acks}
    _w(os.path.join(results, "native-verdict.json"), json.dumps(row, indent=2))
    log("native: %s value=%s brigade_inbound=%s native_notice=%s delivered=%s ack_sent=%s"
        % (arm, value, e.get("inbound_from_map"), notice, delivered, acks))
    return row


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--arms", default="m6-settings,brigade-hold,brigade-refuse,native-hold,native-refuse")
    ap.add_argument("--out")
    ap.add_argument("--tag", default=None)
    args = ap.parse_args()
    run_id = args.tag or ("n" + time.strftime("%H%M%S", time.gmtime()))
    bundle = args.out or os.path.join(REPO, ".ignored", "proof", e4i.now_stamp())
    evroot = os.path.join(bundle, "evidence")
    os.makedirs(evroot, exist_ok=True)
    logf = open(os.path.join(bundle, "run_native.%s.log" % run_id), "a", buffering=1)

    def log(m):
        line = "%s %s" % (time.strftime("%H:%M:%SZ", time.gmtime()), m)
        print(line)
        logf.write(line + "\n")

    stack = snd.Stack(run_id, log=log)
    log("run_native: bundle=%s root=%s arms=%s" % (bundle, stack.root, args.arms))
    rows = []
    m6_native = False
    try:
        stack.provision()
        arms = args.arms.split(",")
        if "m6-settings" in arms:
            r = run_m6_settings(stack, evroot, log)
            rows.append(r)
            m6_native = r["settings_is_a_native_crossSessionInbound_source"]
        for arm, val in (("brigade-hold", "hold"), ("brigade-refuse", "refuse")):
            if arm in arms:
                rows.append(run_brigade_arm(stack, evroot, arm, val, log))
                for d in stack.check_real_files():
                    log("FAIL: config integrity: REAL file %s changed" % d)
        for arm, val in (("native-hold", "hold"), ("native-refuse", "refuse")):
            if arm in arms:
                rows.append(run_native_arm(stack, evroot, arm, val, m6_native, log))
        _w(os.path.join(bundle, "native-summary.%s.json" % run_id), json.dumps(rows, indent=2))
    finally:
        log("teardown: %s" % json.dumps(stack.teardown(scan_roots=[evroot], bundle_cap=os.path.join(bundle, "cap"))))
        logf.close()


if __name__ == "__main__":
    main()
