#!/usr/bin/env python3
"""E0-5 driver plumbing: environment isolation, config protection, ps sampling."""

import hashlib
import json
import os
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin")
REPO = "/Users/rjae/Development/appshapes/brigade"

# E0-4 established that the plan's eight-name list is incomplete, so the rule is
# a PREFIX rule, not a list: strip every CLAUDE* name except CLAUDE_CONFIG_DIR,
# which nested `claude` needs for auth.
KEEP = {"CLAUDE_CONFIG_DIR"}


def leaky_names(env=None):
    env = env if env is not None else os.environ
    return sorted(k for k in env if k.startswith("CLAUDE") and k not in KEEP)


def env_unset_args(env=None):
    return [x for k in leaky_names(env) for x in ("-u", k)]


def child_env(extra=None):
    e = {k: v for k, v in os.environ.items() if not (k.startswith("CLAUDE") and k not in KEEP)}
    if extra:
        e.update(extra)
    return e


def outer_identity():
    tok = os.environ.get("CLAUDE_CODE_MESSAGING_TOKEN", "")
    return {
        "session": os.environ.get("CLAUDE_CODE_SESSION_ID", ""),
        "socket": os.environ.get("CLAUDE_CODE_MESSAGING_SOCKET", ""),
        "pid": os.environ.get("CLAUDE_PID", ""),
        "token_sha12": hashlib.sha256(tok.encode()).hexdigest()[:12] if tok else "",
    }


def sha256_file(path):
    try:
        with open(path, "rb") as f:
            return hashlib.sha256(f.read()).hexdigest()
    except OSError:
        return ""


class ConfigGuard(object):
    """Snapshot -> verify -> restore-on-change -> report."""

    def __init__(self, results):
        cfgdir = os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")
        self.paths = [os.path.join(cfgdir, "settings.json"),
                      os.path.join(REPO, "CLAUDE.md"),
                      os.path.expanduser("~/.claude/CLAUDE.md")]
        self.snapdir = os.path.join(results, "config-snapshot")
        os.makedirs(self.snapdir, exist_ok=True)
        self.pre = {}
        for i, p in enumerate(self.paths):
            self.pre[p] = sha256_file(p)
            if os.path.exists(p):
                shutil.copy2(p, os.path.join(self.snapdir, "%d.snap" % i))
        # .claude.json is written concurrently by the OUTER session, so it is
        # backed up but only ever reverted SURGICALLY (project keys we added).
        self.dotclaude = os.path.join(cfgdir, ".claude.json")
        self.dot_backup = os.path.join(self.snapdir, "claude.json.bak")
        self.dot_projects_pre = []
        if os.path.exists(self.dotclaude):
            shutil.copy2(self.dotclaude, self.dot_backup)
            try:
                self.dot_projects_pre = sorted(json.load(open(self.dotclaude))
                                               .get("projects", {}).keys())
            except Exception:
                pass

    def verify(self, results):
        out = []
        for i, p in enumerate(self.paths):
            post = sha256_file(p)
            changed = post != self.pre[p]
            restored = False
            if changed and os.path.exists(os.path.join(self.snapdir, "%d.snap" % i)):
                shutil.copy2(os.path.join(self.snapdir, "%d.snap" % i), p)
                restored = True
            out.append({"path": p, "pre": self.pre[p], "post": post,
                        "changed": changed, "restored": restored})
        with open(os.path.join(results, "config-protection.json"), "w") as f:
            json.dump(out, f, indent=2)
        return out

    def surgical_revert_projects(self, results, only_paths):
        """Remove ONLY the throwaway project keys this run added. Never a
        wholesale restore: the outer session writes to this file concurrently."""
        rec = {"removed": [], "added_by_run": [], "note": ""}
        if not os.path.exists(self.dotclaude):
            rec["note"] = "no .claude.json"
            return rec
        try:
            data = json.load(open(self.dotclaude))
        except Exception as e:
            rec["note"] = "unreadable: %s" % e
            return rec
        projects = data.get("projects", {})
        added = [k for k in projects if k not in self.dot_projects_pre]
        rec["added_by_run"] = sorted(added)
        for k in added:
            if any(os.path.realpath(k) == os.path.realpath(p) for p in only_paths):
                del projects[k]
                rec["removed"].append(k)
        if rec["removed"]:
            tmp = self.dotclaude + ".e05tmp"
            with open(tmp, "w") as f:
                json.dump(data, f, indent=2)
            os.replace(tmp, self.dotclaude)
        with open(os.path.join(results, "dot-claude-json-revert.json"), "w") as f:
            json.dump(rec, f, indent=2)
        return rec


def getsid(pid):
    # macOS `ps -o sess` prints the kernel session-struct address and reports 0
    # for every process, so the real session id has to come from getsid(2).
    try:
        return os.getsid(int(pid))
    except OSError:
        return None


def ps_sample(pid, label):
    if not pid:
        return {"label": label, "raw": "", "rc": None, "note": "no pid"}
    p = subprocess.run(["ps", "-o", "pid,ppid,pgid,sess", "-p", str(pid)],
                       capture_output=True, text=True)
    return {"label": label, "ts": time.strftime("%H:%M:%S"),
            "cmd": "ps -o pid,ppid,pgid,sess -p %s" % pid,
            "rc": p.returncode, "raw": p.stdout.rstrip("\n"),
            "getsid": getsid(pid)}


def alive(pid):
    try:
        os.kill(int(pid), 0)
        return True
    except OSError:
        return False


def isolation_gate(nested, outer):
    t = hashlib.sha256((nested.get("token") or "").encode()).hexdigest()[:12]
    leaks = []
    if nested.get("socket") and nested["socket"] == outer["socket"]:
        leaks.append("socket")
    if nested.get("session") and nested["session"] == outer["session"]:
        leaks.append("session")
    if t and t == outer["token_sha12"]:
        leaks.append("token")
    if nested.get("claude_pid") and nested["claude_pid"] == outer["pid"]:
        leaks.append("claude_pid")
    ok = bool(nested.get("socket")) and not leaks
    return {"ok": ok, "leaks": leaks, "outer": outer,
            "nested": {"socket": nested.get("socket"), "session": nested.get("session"),
                       "claude_pid": nested.get("claude_pid"), "token_sha12": t}}
