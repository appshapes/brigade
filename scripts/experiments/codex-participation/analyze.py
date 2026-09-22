#!/usr/bin/env python3
"""Score redacted P0 delivery and identity evidence from a Codex JSONL run."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path
import re
from typing import Any


ANCHOR = "Brigade team message from another person"
CONTROL_RE = re.compile(r"^(DIRECT|UNSET|POISON|NESTED) (\{.*\})$")


def digest(value: str) -> str:
    return hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]


def load_jsonl(path: Path) -> list[dict[str, Any]]:
    rows: list[dict[str, Any]] = []
    with path.open(encoding="utf-8") as stream:
        for number, line in enumerate(stream, 1):
            try:
                row = json.loads(line)
            except json.JSONDecodeError as exc:
                raise ValueError(f"{path.name}:{number}: invalid JSON") from exc
            if not isinstance(row, dict):
                raise ValueError(f"{path.name}:{number}: expected object")
            rows.append(row)
    return rows


def assistant_text(rows: list[dict[str, Any]]) -> str:
    parts = []
    for row in rows:
        item = row.get("item")
        if (
            row.get("type") == "item.completed"
            and isinstance(item, dict)
            and item.get("type") == "agent_message"
            and isinstance(item.get("text"), str)
        ):
            parts.append(item["text"])
    return "\n".join(parts)


def controls(rows: list[dict[str, Any]]) -> dict[str, dict[str, Any]]:
    found: dict[str, dict[str, Any]] = {}
    for row in rows:
        item = row.get("item")
        if not isinstance(item, dict) or item.get("type") != "command_execution":
            continue
        output = item.get("aggregated_output")
        if not isinstance(output, str):
            continue
        for line in output.splitlines():
            match = CONTROL_RE.match(line)
            if match:
                found[match.group(1)] = json.loads(match.group(2))
    return found


def marker_hash(case: dict[str, Any], name: str) -> object:
    return case.get("markers", {}).get("hashes", {}).get(name)


def marker_names(case: dict[str, Any]) -> list[str]:
    names = case.get("markers", {}).get("present", [])
    return names if isinstance(names, list) else []


def analyze(
    event_rows: list[dict[str, Any]], stream_rows: list[dict[str, Any]], nonce: str
) -> dict[str, Any]:
    thread_ids = [
        row.get("thread_id")
        for row in stream_rows
        if row.get("type") == "thread.started" and isinstance(row.get("thread_id"), str)
    ]
    session_hash = digest(thread_ids[0]) if len(thread_ids) == 1 else None
    relevant = [row for row in event_rows if row.get("session_hash") == session_hash]
    event_names = [row.get("event") for row in relevant]
    text = assistant_text(stream_rows)
    cases = controls(stream_rows)

    direct = cases.get("DIRECT", {})
    direct_match = bool(
        session_hash
        and marker_hash(direct, "CODEX_SESSION_ID") == session_hash
        and marker_hash(direct, "CODEX_THREAD_ID") == session_hash
    )
    unset_missing = "UNSET" in cases and marker_names(cases["UNSET"]) == []
    poison_mutable = bool(
        "POISON" in cases
        and marker_hash(cases["POISON"], "CODEX_SESSION_ID") != session_hash
        and marker_hash(cases["POISON"], "CODEX_THREAD_ID") != session_hash
    )
    nested_missing = "NESTED" in cases and marker_names(cases["NESTED"]) == []

    lifecycle_ready = all(
        event in event_names for event in ("SessionStart", "UserPromptSubmit", "SessionEnd")
    )
    delivery = lifecycle_ready and ANCHOR in text and nonce in text
    human_boundary = direct_match and not (unset_missing or poison_mutable or nested_missing)
    return {
        "schema": 1,
        "session_hash": session_hash,
        "events": event_names,
        "lifecycle_ready": lifecycle_ready,
        "delivery": {
            "anchor_in_assistant_text": ANCHOR in text,
            "split_nonce_in_assistant_text": nonce in text,
            "pass": delivery,
        },
        "identity": {
            "direct_marker_matches_hook_session": direct_match,
            "unset_child_is_unmarked": unset_missing,
            "poisoned_child_accepts_replacement": poison_mutable,
            "nested_child_is_unmarked": nested_missing,
            "attempted_cases": len(cases),
            "shell_lookup_route": "PASS" if direct_match else "NOT RUN",
            "human_only_boundary": "PASS" if human_boundary else "FAIL",
        },
        "gate": "PASS" if delivery and human_boundary else "FAIL",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--events", type=Path, required=True)
    parser.add_argument("--stream", type=Path, required=True)
    parser.add_argument("--nonce", required=True)
    args = parser.parse_args()
    result = analyze(load_jsonl(args.events), load_jsonl(args.stream), args.nonce)
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["gate"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
