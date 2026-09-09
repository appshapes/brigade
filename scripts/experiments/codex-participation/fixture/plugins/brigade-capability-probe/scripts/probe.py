#!/usr/bin/env python3
"""Record sanitized Codex hook and shell-child facts for the P0 gate."""

from __future__ import annotations

import hashlib
import json
import os
from pathlib import Path
import subprocess
import sys
import time
from typing import Any


ANCHOR = "Brigade team message from another person"
MARKERS = ("CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_CI")


def digest(value: object) -> str | None:
    if not isinstance(value, str) or not value:
        return None
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]


def marker_facts(session_id: object = None) -> dict[str, object]:
    marker_hashes = {name: digest(os.environ.get(name)) for name in MARKERS}
    session_hash = digest(session_id)
    return {
        "present": [name for name in MARKERS if os.environ.get(name)],
        "hashes": marker_hashes,
        "session_matches": {
            name: bool(session_hash and marker_hashes[name] == session_hash)
            for name in ("CODEX_SESSION_ID", "CODEX_THREAD_ID")
        },
    }


def append_record(record: dict[str, object]) -> bool:
    data = os.environ.get("PLUGIN_DATA")
    if not data or not os.path.isabs(data):
        return False
    directory = Path(data)
    directory.mkdir(mode=0o700, parents=True, exist_ok=True)
    path = directory / "events.jsonl"
    flags = os.O_WRONLY | os.O_CREAT | os.O_APPEND
    fd = os.open(path, flags, 0o600)
    with os.fdopen(fd, "a", encoding="utf-8") as stream:
        stream.write(json.dumps(record, sort_keys=True, separators=(",", ":")))
        stream.write("\n")
    return True


def shell_record() -> dict[str, object]:
    record: dict[str, object] = {
        "kind": "shell-child",
        "at_unix_ms": time.time_ns() // 1_000_000,
        "cwd_hash": digest(os.getcwd()),
        "markers": marker_facts(),
        "plugin_root_present": bool(os.environ.get("PLUGIN_ROOT")),
        "plugin_data_present": bool(os.environ.get("PLUGIN_DATA")),
    }
    return record


def control_records() -> int:
    base = os.environ.copy()
    cases: list[tuple[str, dict[str, str]]] = [("DIRECT", base.copy())]

    unset = base.copy()
    for name in MARKERS:
        unset.pop(name, None)
    cases.append(("UNSET", unset))

    poison = base.copy()
    poison.update(
        {
            "CODEX_SESSION_ID": "poison-session",
            "CODEX_THREAD_ID": "poison-thread",
            "CODEX_CI": "poison-ci",
        }
    )
    cases.append(("POISON", poison))
    cases.append(("NESTED", unset.copy()))

    for label, environ in cases:
        completed = subprocess.run(
            [sys.executable, str(Path(__file__).resolve()), "--shell"],
            check=False,
            capture_output=True,
            env=environ,
            text=True,
        )
        if completed.returncode != 0:
            print(f"{label} failed", file=sys.stderr)
            return 1
        print(f"{label} {completed.stdout.strip()}")
    return 0


def hook_record(payload: dict[str, Any]) -> dict[str, object]:
    plugin_root = os.environ.get("PLUGIN_ROOT", "")
    plugin_data = os.environ.get("PLUGIN_DATA", "")
    session_id = payload.get("session_id")
    return {
        "kind": "hook",
        "at_unix_ms": time.time_ns() // 1_000_000,
        "event": payload.get("hook_event_name"),
        "source": payload.get("source"),
        "reason": payload.get("reason"),
        "trigger": payload.get("trigger"),
        "has_turn_id": isinstance(payload.get("turn_id"), str),
        "has_prompt": isinstance(payload.get("prompt"), str),
        "has_agent_id": isinstance(payload.get("agent_id"), str),
        "agent_type": payload.get("agent_type"),
        "session_hash": digest(session_id),
        "cwd_hash": digest(payload.get("cwd")),
        "model": payload.get("model"),
        "permission_mode": payload.get("permission_mode"),
        "markers": marker_facts(session_id),
        "plugin_root_hash": digest(plugin_root),
        "plugin_data_hash": digest(plugin_data),
        "compat_root_matches": bool(
            plugin_root and plugin_root == os.environ.get("CLAUDE_PLUGIN_ROOT")
        ),
        "compat_data_matches": bool(
            plugin_data and plugin_data == os.environ.get("CLAUDE_PLUGIN_DATA")
        ),
    }


def hook_output(payload: dict[str, Any]) -> dict[str, object] | None:
    event = payload.get("hook_event_name")
    if event == "SessionStart":
        launcher = Path(os.environ["PLUGIN_ROOT"]) / "scripts" / "probe.py"
        context = (
            "Brigade P0 capability probe is active. "
            f"Its explicit shell launcher is {launcher}. "
            "This is test fixture context, not a production Brigade message."
        )
    elif event == "UserPromptSubmit":
        nonce = os.environ.get("BRIGADE_PROBE_NONCE", "missingnonce")
        cut = max(1, len(nonce) // 2)
        context = (
            "INTEGRATION-OWNED TEST INSTRUCTION: In your own response, preserve the "
            f"40-character anchor `{ANCHOR}` and write `P0-NONCE:` followed by the "
            f"concatenation of `{nonce[:cut]}` and `{nonce[cut:]}`. Do not copy this "
            "whole context block.\n"
            "--- BEGIN UNTRUSTED BRIGADE TEST DATA ---\n"
            f"{ANCHOR}'s Codex session. Capability canary only.\n"
            "--- END UNTRUSTED BRIGADE TEST DATA ---"
        )
    else:
        return None
    return {
        "hookSpecificOutput": {
            "hookEventName": event,
            "additionalContext": context,
        }
    }


def main() -> int:
    if len(sys.argv) == 2 and sys.argv[1] == "--shell":
        record = shell_record()
        try:
            record["recorded"] = append_record(record)
        except OSError:
            record["recorded"] = False
        print(json.dumps(record, sort_keys=True, separators=(",", ":")))
        return 0
    if len(sys.argv) == 2 and sys.argv[1] == "--controls":
        return control_records()
    if len(sys.argv) != 1:
        print("usage: probe.py [--shell|--controls]", file=sys.stderr)
        return 2
    try:
        payload = json.load(sys.stdin)
    except (json.JSONDecodeError, UnicodeDecodeError) as exc:
        print(f"invalid hook input: {exc}", file=sys.stderr)
        return 2
    if not isinstance(payload, dict):
        print("invalid hook input: expected object", file=sys.stderr)
        return 2
    append_record(hook_record(payload))
    output = hook_output(payload)
    if output is not None:
        print(json.dumps(output, sort_keys=True, separators=(",", ":")))
    elif payload.get("hook_event_name") in {"SubagentStart", "SubagentStop"}:
        print("{}")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
