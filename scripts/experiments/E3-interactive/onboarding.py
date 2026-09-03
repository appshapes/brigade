#!/usr/bin/env python3
"""Check 15 — what interrupts a first run, tallied across every session driven here.

The row asks for any onboarding prompt seen before the session prompt (the trust
dialog, "Claude in Chrome extension detected", the fullscreen-renderer write).
Every run's expect script records what its `onboard` proc met, so the answer is a
tally over the marks files rather than one person's recollection.

usage: python3 scripts/experiments/E3-interactive/onboarding.py .ignored/e3-interactive
"""
import collections
import json
import os
import sys

INTERESTING = ("onboard_trust", "onboard_chrome", "onboard_ready", "onboard_quiet",
               "onboard_eof", "canary_timeout", "canary_recover", "canary_ok", "canary_failed")


def main():
    root = sys.argv[1]
    tally = collections.Counter()
    runs = 0
    per_run = []
    for dirpath, _dirs, files in os.walk(root):
        if "marks.ndjson" not in files:
            continue
        runs += 1
        seen = []
        with open(os.path.join(dirpath, "marks.ndjson"), errors="replace") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                try:
                    d = json.loads(line)
                except Exception:
                    continue
                e = d.get("event")
                if e in INTERESTING:
                    tally[e] += 1
                    seen.append(e)
        per_run.append((os.path.relpath(dirpath, root), seen))

    print("sessions driven: %d" % runs)
    print("onboarding / readiness marks:")
    for k in INTERESTING:
        if tally[k]:
            print("   %-18s %4d   (%.0f%% of sessions)" % (k, tally[k], 100.0 * tally[k] / runs))
    print()
    trust = tally["onboard_trust"]
    print("trust dialog seen in %d of %d sessions" % (trust, runs))
    print("Chrome-extension prompt seen in %d of %d sessions" % (tally["onboard_chrome"], runs))
    print("no prompt matched (quiet) in %d of %d sessions" % (tally["onboard_quiet"], runs))
    print()
    print("per run:")
    for name, seen in sorted(per_run):
        print("   %-34s %s" % (name, ",".join(seen) or "-"))


if __name__ == "__main__":
    main()
