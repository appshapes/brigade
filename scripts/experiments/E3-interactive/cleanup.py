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
