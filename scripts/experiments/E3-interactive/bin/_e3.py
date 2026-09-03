"""Shared helpers for the E3-interactive detector hooks.

Every artefact of a run lands under ONE state directory, chosen by the driver via
BRIGADE_E3_STATE, so serial runs never share a log.
"""
import datetime
import hashlib
import json
import os

STATE = os.environ.get("BRIGADE_E3_STATE") or "/tmp/brigade-e3-state"


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


def read_stdin_json():
    try:
        raw = __import__("sys").stdin.read()
    except Exception:
        raw = ""
    try:
        return json.loads(raw) if raw.strip() else {}
    except Exception:
        return {"_parse_error": True, "_raw_head": raw[:400]}


def brigade_form(cmd):
    """How a Bash command names the brigade binary.

    Only the BARE form can match the skill's `allowed-tools: Bash(brigade:*)`, so
    the two must never be pooled: a full-path invocation always faces the
    permission gate however the grant behaves. The skill forbids the full path
    outright, and the plugin's own SessionStart context line advertises it, so a
    model that has not loaded the skill reaches for it -- measured, both headless
    and interactively, on 2026-09-03. Recording it separately is what lets the
    writeup say which form was actually under test.
    """
    if not cmd:
        return None
    c = cmd.strip()
    if c.startswith("brigade ") or c == "brigade":
        return "bare"
    head = c.split(None, 1)[0] if c.split() else ""
    if head.endswith("/brigade"):
        return "fullpath"
    return None


def brigade_cmd(hi):
    """(tool_name, command-or-None, form) where form is bare | fullpath | None."""
    tool = hi.get("tool_name")
    ti = hi.get("tool_input") or {}
    cmd = ti.get("command") if isinstance(ti.get("command"), str) else None
    form = brigade_form(cmd) if tool == "Bash" else None
    return tool, cmd, form
