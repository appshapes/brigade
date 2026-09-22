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
DEFAULT_THRESHOLD = 0.7

JUDGE_INSTRUCTIONS = {
    "question": (
        "Did this run actually receive a Brigade team message relayed from another "
        "person, and engage with what that message asked?"
    ),
    "evidence": "`transcript` holds the assistant's own messages from a single run.",
    "guidance": (
        "Judge whether the message landed, not how it was worded. A paraphrase of the "
        "relayed message counts; quoting it verbatim is not required."
    ),
}
JUDGE_TRUE = (
    "The transcript shows the run was handed a message originating from another "
    "person via Brigade and responded to what it said."
)
JUDGE_FALSE = (
    "The transcript shows no relayed message landed: it only explains Brigade, "
    "speculates, reports that nothing arrived, or refers solely to its own prompt."
)


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


def exact_judge(text: str) -> dict[str, Any]:
    """Judge receipt by the literal anchor: offline, deterministic, the default."""
    return {"judge": "exact", "received": ANCHOR in text, "probability": None}


SDK_MISSING = (
    "--judge typesafe needs the TypeSafe SDK: pip install typesafe-sdk, "
    "and set TYPESAFE_API_KEY. The default --judge exact stays offline."
)


def sdk() -> Any:
    """Import the SDK late, so the default path never needs it installed."""
    try:
        import typesafe_sdk
    except ModuleNotFoundError as exc:
        raise SystemExit(SDK_MISSING) from exc
    return typesafe_sdk


def sdk_client() -> Any:
    """Open the real TypeSafe client."""
    return sdk().TypeSafeClient()


def sdk_question() -> Any:
    """The receipt question, built with the SDK's own types."""
    module = sdk()
    return module.Noul(
        instructions=JUDGE_INSTRUCTIONS,
        criteria=module.NoulCriteria(true=JUDGE_TRUE, false=JUDGE_FALSE),
    )


def typesafe_judge(
    threshold: float = DEFAULT_THRESHOLD,
    open_client: Any = sdk_client,
    build_question: Any = sdk_question,
) -> Any:
    """Judge receipt with a Noul, so a paraphrase of the relay still counts.

    Opt-in only. Calling the returned judge sends the run's assistant text to
    TypeSafe, so it is reached solely through `--judge typesafe`. The client and
    question are injected so tests exercise this thresholding without a network.
    """

    def judge(text: str) -> dict[str, Any]:
        with open_client() as client:
            response = client.system_one(
                state={"transcript": text},
                questions={"received": build_question()},
            )
        probability = noul(response, "received")
        return {
            "judge": "typesafe",
            "received": probability >= threshold,
            "probability": probability,
        }

    return judge


def noul(response: Any, name: str) -> float:
    """Read one Noul probability; the docs show both accessors, so accept either."""
    for attribute in ("nouls", "answers"):
        answers = getattr(response, attribute, None)
        if answers is not None and name in answers:
            return float(answers[name].noul)
    raise KeyError(f"no Noul answer {name!r} in the TypeSafe response")


def delivery_report(
    text: str, nonce: str, lifecycle_ready: bool, verdict: dict[str, Any]
) -> dict[str, Any]:
    """Record the exact anchor check alongside the judgment that drives the gate."""
    return {
        "anchor_in_assistant_text": ANCHOR in text,
        "split_nonce_in_assistant_text": nonce in text,
        "judge": verdict["judge"],
        "judged_received": verdict["received"],
        "judged_probability": verdict["probability"],
        "pass": lifecycle_ready and nonce in text and bool(verdict["received"]),
    }


def analyze(
    event_rows: list[dict[str, Any]],
    stream_rows: list[dict[str, Any]],
    nonce: str,
    judge: Any = exact_judge,
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
    delivery = delivery_report(text, nonce, lifecycle_ready, judge(text))
    human_boundary = direct_match and not (unset_missing or poison_mutable or nested_missing)
    return {
        "schema": 1,
        "session_hash": session_hash,
        "events": event_names,
        "lifecycle_ready": lifecycle_ready,
        "delivery": delivery,
        "identity": {
            "direct_marker_matches_hook_session": direct_match,
            "unset_child_is_unmarked": unset_missing,
            "poisoned_child_accepts_replacement": poison_mutable,
            "nested_child_is_unmarked": nested_missing,
            "attempted_cases": len(cases),
            "shell_lookup_route": "PASS" if direct_match else "NOT RUN",
            "human_only_boundary": "PASS" if human_boundary else "FAIL",
        },
        "gate": "PASS" if delivery["pass"] and human_boundary else "FAIL",
    }


def main() -> int:
    parser = argparse.ArgumentParser()
    parser.add_argument("--events", type=Path, required=True)
    parser.add_argument("--stream", type=Path, required=True)
    parser.add_argument("--nonce", required=True)
    parser.add_argument("--judge", choices=("exact", "typesafe"), default="exact")
    parser.add_argument("--judge-threshold", type=float, default=DEFAULT_THRESHOLD)
    args = parser.parse_args()
    judge = typesafe_judge(args.judge_threshold) if args.judge == "typesafe" else exact_judge
    result = analyze(
        load_jsonl(args.events), load_jsonl(args.stream), args.nonce, judge
    )
    print(json.dumps(result, indent=2, sort_keys=True))
    return 0 if result["gate"] == "PASS" else 1


if __name__ == "__main__":
    raise SystemExit(main())
