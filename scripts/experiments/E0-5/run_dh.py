#!/usr/bin/env python3
"""E0-5 driver for CHECK (d) -- the SessionEnd budget -- and CHECK (h) -- whether
a PLUGIN hook's `timeout: 5` raises that budget.

One nested `claude` per run. The only difference between the two plugin
variants is one JSON field:

  plugin_h_to/hooks/hooks.json  SessionEnd ... "timeout": 5
  plugin_h_nt/hooks/hooks.json  SessionEnd ... (no timeout field)

Their bin/ trees are byte-identical, so any difference in the measured allowance
is attributable to that field and nothing else. Both sides are RUN; nothing here
is inferred from one arm.

The measurement: the SessionEnd hook heartbeats every 25 ms with fsync, so the
last line on disk is the last instant it was alive. `heartbeat_finished_uncut`
present means it was never cut off at all.

Isolation and config protection are the same rules as the previous stage:
strip every CLAUDE* name except CLAUDE_CONFIG_DIR by PREFIX, abort before
measuring if the nested identity matches the outer one, and revert surgically
any top-level or `projects` key the run added to .claude.json.
"""

import argparse
import json
import os
import shutil
import subprocess
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402
import g2  # noqa: E402
import wlib  # noqa: E402

HERE = common.HERE

EXPECT = r"""
set timeout 300
log_file -a "{results}/interactive.log"
proc mark {{event}} {{
    set f [open "{results}/marks.ndjson" a]
    puts $f "{{\"ms\": [clock milliseconds], \"event\": \"$event\"}}"
    close $f
}}
mark spawn
spawn -noecho env {unsets} claude --plugin-dir "{plugin}" --permission-mode bypassPermissions
# Trust dialog: the highlighted default is "No, exit", so a bare Enter QUITS.
# The dialog is a drawn box, so only SINGLE-word regexes ever match.
expect {{
  -re {{trust}} {{ mark trust_shown; expect -re {{exit}}; sleep 1
                  send -- "\033\[B"; sleep 1; send -- "\r"; mark trust_accepted }}
  -re {{shortcuts|Welcome|bypass}} {{ mark ui_ready_no_trust }}
  timeout {{ mark trust_timeout }}
}}
set n 0
while {{![file exists "{ready1}"] && $n < 600}} {{ sleep 1; incr n }}
mark hook_ready
sleep 4
set f [open "{results}/ui-ready" w]; puts $f [clock milliseconds]; close $f
mark exiting
send -- "/exit\r"
expect eof
mark eof
set f [open "{results}/eof" w]; puts $f [clock milliseconds]; close $f
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--variant", required=True,
                    choices=["to", "nt", "two", "t60", "ss_to", "ss_nt", "ss_t3"])
    ap.add_argument("--adapter-delay", type=float, default=0.10)
    ap.add_argument("--mode", default="p", choices=["p", "interactive"])
    ap.add_argument("--cap", type=float, default=1.0)
    ap.add_argument("--label", default="")
    a = ap.parse_args()

    # ss_* : the SessionEnd hook comes from a THROWAWAY --settings file instead
    # of a plugin, so the hooks page's "your settings" claim is tested on its own
    # terms. The user's real settings.json is never touched.
    settings_path = None
    if a.variant.startswith("ss_"):
        plugin = os.path.join(HERE, "plugin_h_ss")
    else:
        plugin = os.path.join(HERE, "plugin_h_%s" % a.variant)
    tag = "dh-%s-%s%s-%s" % (a.variant, a.mode,
                             ("-slow" if a.adapter_delay >= 1.0 else "-fast"),
                             time.strftime("%Y%m%d-%H%M%S"))
    if a.label:
        tag += "-" + a.label
    results = os.path.join(HERE, "results2", tag)
    state = os.path.join(HERE, "state2", tag)
    os.makedirs(results, exist_ok=True)
    os.makedirs(state, mode=0o700, exist_ok=True)
    scratch = os.environ.get("E05_SCRATCH") or "/tmp"
    project = os.path.join(scratch, "e05-projects", tag)
    os.makedirs(project, exist_ok=True)

    guard = common.ConfigGuard(results)
    topguard = g2.TopKeyGuard(results)
    outer = common.outer_identity()

    # AF_UNIX paths cap at ~104 bytes on macOS, so the adapter socket lives at a
    # short path, not under the (long) per-run state dir.
    adapter_sock = "/tmp/e05a-%d.sock" % os.getpid()
    adapter_log = os.path.join(state, "adapter.ndjson")
    ad = subprocess.Popen([sys.executable, os.path.join(HERE, "adapter.py"),
                           "--socket", adapter_sock, "--delay", str(a.adapter_delay),
                           "--log", adapter_log],
                          stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    for _ in range(100):
        if os.path.exists(adapter_sock):
            break
        time.sleep(0.05)

    env = common.child_env({
        "BRIGADE_STATE_DIR": state,
        "BRIGADE_E05_HOME": HERE,
        "BRIGADE_E05_TAG": tag,
        "BRIGADE_E05_VARIANT": a.variant,
        "BRIGADE_E05_ADAPTER": adapter_sock,
        "BRIGADE_E05_CAP": str(a.cap),
        "BRIGADE_E05_SPAWN": "0",   # no watcher here; (d)/(h) measure the hook only
    })

    out = {"tag": tag, "variant": a.variant, "mode": a.mode,
           "plugin": plugin, "adapter_delay": a.adapter_delay, "cap": a.cap,
           "hooks_json": json.load(open(os.path.join(plugin, "hooks", "hooks.json"))),
           "results": results, "state_dir": state, "project_dir": project,
           "stripped_env_names": common.leaky_names(), "events": []}

    def note(ev, **kw):
        r = {"epoch": round(time.time(), 3), "event": ev}
        r.update(kw)
        out["events"].append(r)
        print("[dh] " + json.dumps(r))

    ready1 = os.path.join(state, "ready-1.json")
    t_launch = time.time()

    extra = []
    if a.variant.startswith("ss_"):
        hookdef = {"type": "command", "command": os.path.join(plugin, "bin", "end")}
        if a.variant == "ss_to":
            hookdef["timeout"] = 5
        elif a.variant == "ss_t3":
            hookdef["timeout"] = 3
        settings_path = os.path.join(results, "throwaway-settings.json")
        with open(settings_path, "w") as f:
            json.dump({"hooks": {"SessionEnd": [{"hooks": [hookdef]}]}}, f, indent=2)
        extra = ["--settings", settings_path]
        out["throwaway_settings"] = json.load(open(settings_path))

    if a.mode == "p":
        cmd = ["env"] + common.env_unset_args() + [
            "claude", "-p", "Reply with exactly: E05DH_OK",
            "--plugin-dir", plugin, "--permission-mode", "bypassPermissions"] + extra
        note("launch_p", cmd=" ".join(cmd[:2]) + " ... claude -p")
        proc = subprocess.Popen(cmd, cwd=project, env=env,
                                stdin=subprocess.DEVNULL,
                                stdout=open(os.path.join(results, "p-stdout.txt"), "wb"),
                                stderr=open(os.path.join(results, "p-stderr.txt"), "wb"))
    else:
        exp = EXPECT.format(results=results, plugin=plugin, ready1=ready1,
                            unsets=" ".join(common.env_unset_args()))
        with open(os.path.join(results, "drive.exp"), "w") as f:
            f.write(exp)
        note("launch_interactive")
        proc = subprocess.Popen(["expect", "-f", os.path.join(results, "drive.exp")],
                                cwd=project, env=env,
                                stdout=open(os.path.join(results, "interactive.raw"), "wb"),
                                stderr=subprocess.STDOUT)

    # ---- isolation gate, before any measurement is trusted ----
    g2.waitfor(ready1, 700)
    nested = g2.read_json(os.path.join(state, "nested-env-1.json")) or {}
    gate = g2.gate(nested, outer)
    out["isolation"] = gate
    if not gate["ok"]:
        note("ISOLATION_GATE_FAIL", leaks=gate["leaks"])
        try:
            proc.kill()
        except Exception:
            pass
        ad.kill()
        out["aborted"] = "isolation gate"
        out["config"] = guard.verify(results)
        out["dot_claude_json"] = topguard.revert([project])
        shutil.rmtree(project, ignore_errors=True)
        json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
        print(json.dumps(out, indent=2))
        return 2
    note("isolation_gate_ok", nested=gate["nested"])

    cpid = int(nested.get("claude_pid") or 0)
    out["claude_pid"] = cpid

    # ---- wait for the process to go away, timing it ----
    rc = proc.wait(timeout=900)
    t_proc_done = time.time()
    end = time.time() + 30
    while cpid and common.alive(cpid) and time.time() < end:
        time.sleep(0.01)
    t_claude_gone = time.time()
    note("exited", rc=rc, seconds_since_launch=round(t_proc_done - t_launch, 3))

    time.sleep(1.5)  # let a hook that outlived the process write its last line
    hb = g2.read_ndjson(os.path.join(state, "budget-%s.ndjson" % a.variant))
    ad.kill()

    # The `two` variant runs a SECOND SessionEnd hook, which logs to its own
    # file. If the 1.5 s is a SHARED budget both are cut at the same wall clock;
    # if it is per-hook they are cut 1.5 s after each one's own start.
    other = {}
    for fn in sorted(os.listdir(state)):
        if fn.startswith("budget-") and fn != "budget-%s.ndjson" % a.variant:
            recs = g2.read_ndjson(os.path.join(state, fn))
            if recs:
                other[fn] = {"n_records": len(recs),
                             "first_epoch": recs[0]["epoch"],
                             "last_epoch": recs[-1]["epoch"],
                             "allowance_seconds": round(max(r["elapsed"] for r in recs), 4),
                             "last_event": recs[-1]["event"],
                             "signals": [r for r in recs if r["event"] == "signal_received"]}
    out["second_sessionend_hook"] = other

    starts = [r for r in hb if r["event"] == "hook_start"]
    hbs = [r for r in hb if r["event"] == "hb"]
    uncut = [r for r in hb if r["event"] == "heartbeat_finished_uncut"]
    sigs = [r for r in hb if r["event"] == "signal_received"]
    closes = [r for r in hb if r["event"] == "close_result"]

    allowance = None
    if hb:
        allowance = round(max(r["elapsed"] for r in hb), 4)

    ppids = sorted({r.get("ppid") for r in hb})
    out["budget"] = {
        "hook_fired": bool(starts),
        "reason": starts[0].get("reason") if starts else None,
        "heartbeats": len(hbs),
        "last_event": hb[-1]["event"] if hb else None,
        "allowance_seconds": allowance,
        "finished_uncut": bool(uncut),
        "signals_logged": sigs,
        "ppids_seen": ppids,
        "reparented_to_launchd_midway": (len(ppids) > 1 and 1 in ppids),
        "hook_start_epoch": starts[0]["epoch"] if starts else None,
        "last_line_epoch": hb[-1]["epoch"] if hb else None,
        "claude_gone_epoch": round(t_claude_gone, 4),
        "seconds_hook_start_to_claude_gone": (round(t_claude_gone - starts[0]["epoch"], 3)
                                              if starts else None),
        "close": closes[0] if closes else None,
    }
    try:
        os.unlink(adapter_sock)
    except OSError:
        pass

    # ---- calibration: how much of the budget is eaten before the hook's own
    # clock starts? Spawn the identical hook script from here, with a known
    # spawn epoch, and read the epoch of its first log line. The real allowance
    # is the measured one PLUS this. ----
    calstate = os.path.join(state, "cal")
    os.makedirs(calstate, mode=0o700, exist_ok=True)
    calenv = dict(env)
    calenv.update({"BRIGADE_STATE_DIR": calstate, "BRIGADE_E05_ADAPTER": "",
                   "BRIGADE_E05_HB_MAX": "0.2"})
    cals = []
    for _ in range(5):
        for fn in os.listdir(calstate):
            os.unlink(os.path.join(calstate, fn))
        t = time.time()
        p = subprocess.run([os.path.join(plugin, "bin", "end")], env=calenv,
                           input=b"{}", capture_output=True)
        recs = g2.read_ndjson(os.path.join(calstate, "budget-%s.ndjson" % a.variant))
        if recs:
            cals.append(round(recs[0]["epoch"] - t, 4))
    out["spawn_overhead_seconds"] = {
        "samples": cals,
        "median": sorted(cals)[len(cals) // 2] if cals else None,
        "note": "epoch of the hook's first log line minus the epoch it was spawned; "
                "interpreter start + imports + stdin read. The harness's clock "
                "starts before this, so the true allowance is measured + this.",
    }
    if cals and allowance is not None:
        out["budget"]["allowance_seconds_corrected"] = round(
            allowance + sorted(cals)[len(cals) // 2], 4)

    out["adapter_log"] = g2.read_ndjson(adapter_log)
    out["sessionend_records"] = hb
    out["startup_records"] = g2.read_ndjson(os.path.join(state, "startup.ndjson"))
    out["config"] = guard.verify(results)
    out["dot_claude_json"] = topguard.revert([project])
    shutil.rmtree(project, ignore_errors=True)
    note("measured", **{k: v for k, v in out["budget"].items()
                        if k not in ("signals_logged", "close")})
    json.dump(out, open(os.path.join(results, "verdict.json"), "w"), indent=2)
    print("results: " + results)
    return 0


if __name__ == "__main__":
    sys.exit(main())
