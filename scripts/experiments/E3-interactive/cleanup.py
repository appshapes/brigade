#!/usr/bin/env python3
"""Remove what the E3-interactive runs left behind on this machine.

Each run creates a fresh project directory, and Claude Code records a `projects`
entry for it in `~/.claude.json` (trust decision, telemetry, and -- for the
`dismiss` arm -- the skill dismissal). Those entries are litter once the runs are
read. The temporary directories themselves are removed too.

`~/.claude.json` is NEVER restored wholesale: the driving session writes it
concurrently, so this does one late, surgical read-modify-write of the `projects`
key alone and prints exactly what it removed.

usage: python3 scripts/experiments/E3-interactive/cleanup.py [--apply]
       (without --apply it only lists what it would remove)
"""
import argparse
import json
import os
import shutil
import sys

PREFIXES = ("brigade-e3-", "brigade-e3hl-")


def prune_dead_maps():
    """Remove by-pid session maps whose process is gone.

    Only `SessionEnd` deletes a map entry, so a SIGKILLed session (check 13) and
    a hook-registered peer (checks 4-6) both leave one behind. It is litter
    rather than a trust hole -- `SessionStart` will only adopt an existing map
    when the adapter still reports that session ALIVE, and the watcher has
    already marked a killed session offline -- but it is this harness's litter
    and it should not accumulate.
    """
    state = os.path.expanduser(os.environ.get("XDG_STATE_HOME", "~/.local/state"))
    d = os.path.join(state, "brigade", "sessions", "by-pid")
    if not os.path.isdir(d):
        return
    for name in sorted(os.listdir(d)):
        if not name.endswith(".json"):
            continue
        path = os.path.join(d, name)
        try:
            with open(path) as f:
                pid = int(json.load(f).get("claude_pid") or 0)
        except Exception:
            continue
        if pid <= 0:
            continue
        try:
            os.kill(pid, 0)
        except ProcessLookupError:
            os.remove(path)
            print("   pruned dead session map: %s" % name)
        except PermissionError:
            pass


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--apply", action="store_true")
    args = ap.parse_args()

    cfg = os.path.expanduser(os.environ.get("CLAUDE_CONFIG_DIR", "~/.claude") + "/../.claude.json"
                             if os.environ.get("CLAUDE_CONFIG_DIR") else "~/.claude.json")
    d = json.load(open(cfg))
    keys = [k for k in d.get("projects", {})
            if any(p in k for p in PREFIXES)]
    dirs = [k for k in keys if os.path.isdir(k)]

    print("config: %s" % cfg)
    print("project entries from these runs: %d" % len(keys))
    for k in keys:
        print("   %s" % k)
    print("temp directories still present: %d" % len(dirs))

    if not args.apply:
        print("\n(dry run -- pass --apply to remove)")
        return 0

    for k in dirs:
        shutil.rmtree(k, ignore_errors=True)
    prune_dead_maps()
    if keys:
        d = json.load(open(cfg))          # re-read as late as possible
        for k in keys:
            d.get("projects", {}).pop(k, None)
        tmp = cfg + ".e3tmp"
        with open(tmp, "w") as f:
            json.dump(d, f, indent=2)
        os.replace(tmp, cfg)
    print("removed %d project entries and %d directories" % (len(keys), len(dirs)))
    return 0


if __name__ == "__main__":
    sys.exit(main())
