#!/usr/bin/env python3
"""E0-8 (a): first-use download timing inside the SessionStart hook.

The asset is the REAL size (8,324,402 bytes = 7.94 MiB), served by a local Go
server that paces its writes to an exact byte rate. Cold cache is guaranteed by
giving every run a fresh XDG_DATA_HOME, so the bootstrap always takes the
download path.

What is measured, per run:
  * the fetch alone (curl's own time_total, and the hook's wall clock around it)
  * the whole hook (download + sha256 + install)
  * the whole `claude -p` invocation, which is what the human waits for

Nothing sleeps to approximate a rate: the server computes, after each chunk, the
instant those bytes were due and sleeps until it, so drift never accumulates.
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
ASSET = os.path.join(HERE, "asset", "brigade-darwin-arm64")
SHA = open(os.path.join(HERE, "asset", "sha256.txt")).read().split()[0]
PLUGIN = os.path.join(HERE, "plugin_a")


def nested_env(extra=None):
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k == "CLAUDE_CONFIG_DIR":
            out[k] = v
        elif k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"):
            stripped.append(k)
        else:
            out[k] = v
    if extra:
        out.update(extra)
    return out, sorted(stripped)


def outer_identity():
    import hashlib
    tok = os.environ.get("CLAUDE_CODE_MESSAGING_TOKEN", "")
    return {"session": os.environ.get("CLAUDE_CODE_SESSION_ID", ""),
            "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET", ""),
            "pid": os.environ.get("CLAUDE_PID", ""),
            "token_sha12": hashlib.sha256(tok.encode()).hexdigest()[:12] if tok else ""}


def start_server(rate, results, chunk=16384):
    pf = os.path.join(results, "port.txt")
    if os.path.exists(pf):
        os.remove(pf)
    log = open(os.path.join(results, "server.log"), "ab")
    p = subprocess.Popen([os.path.join(HERE, "server", "throttle"),
                          "-file", ASSET, "-rate", str(rate), "-chunk", str(chunk),
                          "-portfile", pf, "-addr", "127.0.0.1:0"],
                         stdout=log, stderr=log)
    for _ in range(100):
        if os.path.exists(pf):
            addr = open(pf).read().strip()
            if addr:
                return p, addr
        time.sleep(0.05)
    p.kill()
    raise RuntimeError("server did not start")


def one(rate, label, idx, args):
    results = os.path.join(HERE, "results", args.tag, "%s-run%d" % (label, idx))
    shutil.rmtree(results, ignore_errors=True)
    os.makedirs(results)
    state = os.path.join(results, "state")
    os.makedirs(state)
    # COLD CACHE: a brand-new data home each run, so the bootstrap can never hit
    # a warm cache left by a previous run.
    xdg = os.path.join(results, "xdg-data")
    os.makedirs(xdg)
    proj = os.path.join(HERE, "proj", "a-%s-%d" % (label, idx))
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)

    srv, addr = start_server(rate, results)
    try:
        env, stripped = nested_env({
            "BRIGADE_E08_STATE": state,
            "BRIGADE_E08_BASE": "http://" + addr,
            "BRIGADE_E08_SHA": SHA,
            "BRIGADE_E08_RATE_LABEL": label,
            "XDG_DATA_HOME": xdg,
            "BRIGADE_E08_MODE": args.mode,
        })
        cmd = ["claude", "-p", "Reply with the single word ACK and nothing else. Do not use any tool.",
               "--plugin-dir", PLUGIN, "--output-format", "json"]
        t0 = time.time()
        p = subprocess.run(cmd, cwd=proj, env=env, capture_output=True, text=True,
                           stdin=subprocess.DEVNULL, timeout=args.hard_timeout)
        wall = time.time() - t0
        if args.mode == "background":
            # The detached worker outlives the session; wait for the cache to go
            # warm (bounded) so the server is not torn down under it, and record
            # WHEN it went warm relative to the session's own start.
            deadline = time.time() + 120
            while time.time() < deadline:
                bp0 = os.path.join(state, "bootstrap.ndjson")
                if os.path.exists(bp0) and any(
                        json.loads(l).get("event") == "background_fetch_done"
                        for l in open(bp0) if l.strip()):
                    break
                time.sleep(0.5)
    finally:
        srv.terminate()
        try:
            srv.wait(timeout=5)
        except Exception:
            srv.kill()

    boots = []
    bp = os.path.join(state, "bootstrap.ndjson")
    if os.path.exists(bp):
        for line in open(bp):
            if line.strip():
                boots.append(json.loads(line))
    open(os.path.join(results, "claude.stdout.json"), "w").write(p.stdout)
    open(os.path.join(results, "claude.stderr.txt"), "w").write(p.stderr)
    try:
        res = json.loads(p.stdout)
    except Exception:
        res = {}

    installed = None
    for root, _, files in os.walk(xdg):
        for f in files:
            fp = os.path.join(root, f)
            installed = {"path": fp, "size": os.path.getsize(fp),
                         "mode": oct(os.stat(fp).st_mode & 0o777)}
    rec = {
        "label": label, "rate_bytes_per_s": rate, "run": idx,
        "server_addr": addr,
        "claude_wall_s": round(wall, 3),
        "claude_exit": p.returncode,
        "claude_result": (res.get("result") or "")[:120],
        "claude_is_error": res.get("is_error"),
        "bootstrap_rows": boots,
        "hook_total_s": boots[0]["hook_total_s"] if boots else None,
        "fetch_s": boots[0].get("fetch_s") if boots else None,
        "curl": boots[0].get("curl") if boots else None,
        "cache_hit": boots[0].get("cache_hit") if boots else None,
        "bootstrap_error": boots[0].get("error") if boots else None,
        "installed": installed,
        "stripped": stripped,
        "results": results,
    }
    json.dump(rec, open(os.path.join(results, "summary.json"), "w"), indent=2)
    return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--hard-timeout", type=float, default=240)
    ap.add_argument("--rates", default="unthrottled:0,1MBps:1000000,250kBps:250000")
    ap.add_argument("--mode", default="sync", choices=["sync", "background"])
    ap.add_argument("--tag", default="a")
    a = ap.parse_args()
    rows = []
    for spec in a.rates.split(","):
        label, rate = spec.split(":")
        for i in range(1, a.reps + 1):
            r = one(int(rate), label, i, a)
            rows.append(r)
            print("[%s run%d] claude_wall=%.2fs hook_total=%s fetch=%s curl=%s err=%s" % (
                label, i, r["claude_wall_s"], r["hook_total_s"], r["fetch_s"],
                json.dumps(r["curl"]) if r["curl"] else None, r["bootstrap_error"]))
            sys.stdout.flush()
    out = os.path.join(HERE, "results", a.tag, "all.json")
    json.dump({"outer": outer_identity(), "rows": rows,
               "asset_bytes": os.path.getsize(ASSET), "asset_sha256": SHA},
              open(out, "w"), indent=2)
    print("wrote", out)


if __name__ == "__main__":
    main()
