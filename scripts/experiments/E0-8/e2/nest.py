#!/usr/bin/env python3
"""Strip-by-prefix launcher. Keeps only CLAUDE_CONFIG_DIR (optionally overridden)."""
import json, os, subprocess, sys, time

def nested_env(cfgdir=None, extra=None, keep_cfg=True):
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k == "CLAUDE_CONFIG_DIR":
            if keep_cfg:
                out[k] = v
            stripped.append(k) if not keep_cfg else None
        elif k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"):
            stripped.append(k)
        else:
            out[k] = v
    if cfgdir:
        out["CLAUDE_CONFIG_DIR"] = cfgdir
    if extra:
        out.update(extra)
    return out, sorted(stripped)

if __name__ == "__main__":
    import argparse
    ap = argparse.ArgumentParser()
    ap.add_argument("--cfgdir")
    ap.add_argument("--cwd")
    ap.add_argument("--out")
    ap.add_argument("--timeout", type=float, default=180)
    ap.add_argument("cmd", nargs="+")
    a = ap.parse_args()
    env, stripped = nested_env(a.cfgdir)
    t0 = time.time()
    p = subprocess.run(a.cmd, cwd=a.cwd, env=env, capture_output=True, text=True,
                       stdin=subprocess.DEVNULL, timeout=a.timeout)
    rec = {"cmd": a.cmd, "cwd": a.cwd, "stripped": stripped,
           "config_dir_used": env.get("CLAUDE_CONFIG_DIR"),
           "exit": p.returncode, "wall_s": round(time.time()-t0, 3),
           "stdout": p.stdout, "stderr": p.stderr}
    js = json.dumps(rec, indent=2)
    if a.out:
        open(a.out, "w").write(js)
    print(js)
