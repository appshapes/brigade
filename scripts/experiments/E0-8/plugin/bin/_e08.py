"""Shared helpers for the E0-8 probe plugin.

Every artefact of a run lands under ONE state directory, chosen by the launcher
via BRIGADE_E08_STATE so concurrent/serial runs never share a log. The fallback
is plugin/state, which only happens if the launcher forgot.
"""
import datetime
import hashlib
import json
import os

SELF = os.path.realpath(__file__)
PLUGIN_ROOT = os.path.dirname(os.path.dirname(SELF))
STATE = os.environ.get("BRIGADE_E08_STATE") or os.path.join(PLUGIN_ROOT, "state")


def now():
    return datetime.datetime.now().astimezone().isoformat()


def ms():
    return int(datetime.datetime.now().timestamp() * 1000)


def append(name, rec):
    os.makedirs(STATE, exist_ok=True)
    rec.setdefault("ts", now())
    rec.setdefault("ms", ms())
    with open(os.path.join(STATE, name), "a") as f:
        f.write(json.dumps(rec) + "\n")
        f.flush()
        os.fsync(f.fileno())


def sha12(s):
    return hashlib.sha256(s.encode("utf-8", "replace")).hexdigest()[:12] if s else ""


def claude_env_names():
    return sorted(k for k in os.environ
                  if k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"))
