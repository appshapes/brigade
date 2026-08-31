#!/usr/bin/env python3
"""E0-5 second-stage plumbing shared by checks (c), (d), (f), (h).

Adds two things to common.py, which is reused as-is:

  * TopKeyGuard -- the previous stage found that Claude Code writes a top-level
    `fullscreenAutoDisabled` key into $CLAUDE_CONFIG_DIR/.claude.json by itself
    when expect-driven sessions fail to start the fullscreen renderer, and that
    key disables the renderer for the WHOLE config dir, i.e. for the user's own
    sessions too. So every top-level key of that file is snapshotted before a
    run and any key the run ADDED is removed surgically afterwards. Never a
    wholesale restore: the outer session writes the same file concurrently.

  * an isolation gate that also refuses when the nested socket/session/token is
    missing, so an aborted startup can never look isolated by accident.
"""

import json
import os
import shutil
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.realpath(__file__)))
import common  # noqa: E402

HERE = common.HERE
CFGDIR = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")
DOTCLAUDE = os.path.join(CFGDIR, ".claude.json")


class TopKeyGuard(object):
    def __init__(self, results):
        self.results = results
        self.pre_keys = []
        self.pre_projects = []
        self.ok = os.path.exists(DOTCLAUDE)
        if self.ok:
            try:
                d = json.load(open(DOTCLAUDE))
                self.pre_keys = sorted(d.keys())
                self.pre_projects = sorted(d.get("projects", {}).keys())
                shutil.copy2(DOTCLAUDE, os.path.join(results, "claude.json.pre.bak"))
            except Exception as e:
                self.ok = False
                self.err = str(e)

    def revert(self, project_paths):
        rec = {"top_keys_added": [], "top_keys_removed": [],
               "projects_added": [], "projects_removed": [], "note": ""}
        if not self.ok or not os.path.exists(DOTCLAUDE):
            rec["note"] = "no readable .claude.json"
            return rec
        try:
            d = json.load(open(DOTCLAUDE))
        except Exception as e:
            rec["note"] = "unreadable after run: %s" % e
            return rec
        added = [k for k in d if k not in self.pre_keys]
        rec["top_keys_added"] = sorted(added)
        for k in added:
            del d[k]
            rec["top_keys_removed"].append(k)
        projects = d.get("projects", {})
        padded = [k for k in projects if k not in self.pre_projects]
        rec["projects_added"] = sorted(padded)
        for k in padded:
            if any(os.path.realpath(k) == os.path.realpath(p) for p in project_paths):
                del projects[k]
                rec["projects_removed"].append(k)
        if rec["top_keys_removed"] or rec["projects_removed"]:
            tmp = DOTCLAUDE + ".e05tmp"
            with open(tmp, "w") as f:
                json.dump(d, f, indent=2)
            os.replace(tmp, DOTCLAUDE)
        rec["top_keys_after"] = sorted(json.load(open(DOTCLAUDE)).keys()) \
            if os.path.exists(DOTCLAUDE) else []
        with open(os.path.join(self.results, "dot-claude-json.json"), "w") as f:
            json.dump(rec, f, indent=2)
        return rec


def gate(nested, outer):
    g = common.isolation_gate(nested, outer)
    if not nested.get("session") or not nested.get("claude_pid") or not nested.get("token"):
        g["ok"] = False
        g.setdefault("leaks", []).append("incomplete-nested-identity")
    return g


def read_json(p):
    try:
        with open(p) as f:
            return json.load(f)
    except Exception:
        return None


def read_ndjson(p):
    out = []
    if os.path.exists(p):
        for line in open(p):
            line = line.strip()
            if line:
                try:
                    out.append(json.loads(line))
                except ValueError:
                    pass
    return out


def waitfor(path, timeout, poll=0.05):
    end = time.time() + timeout
    while time.time() < end:
        if os.path.exists(path):
            return True
        time.sleep(poll)
    return False
