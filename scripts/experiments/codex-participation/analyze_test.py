from __future__ import annotations

import copy
import unittest

from analyze import ANCHOR, analyze, digest


class AnalyzeTest(unittest.TestCase):
    def setUp(self) -> None:
        self.thread = "thread-fixture"
        self.nonce = "joined-nonce"
        session_hash = digest(self.thread)
        self.events = [
            {"event": name, "session_hash": session_hash}
            for name in ("SessionStart", "UserPromptSubmit", "SessionEnd")
        ]
        cases = {
            "DIRECT": {
                "markers": {
                    "present": ["CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_CI"],
                    "hashes": {
                        "CODEX_SESSION_ID": session_hash,
                        "CODEX_THREAD_ID": session_hash,
                    },
                }
            },
            "UNSET": {"markers": {"present": [], "hashes": {}}},
            "POISON": {
                "markers": {
                    "present": ["CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_CI"],
                    "hashes": {
                        "CODEX_SESSION_ID": "poison-a",
                        "CODEX_THREAD_ID": "poison-b",
                    },
                }
            },
            "NESTED": {"markers": {"present": [], "hashes": {}}},
        }
        output = "\n".join(f"{name} {json_compact(case)}" for name, case in cases.items())
        self.stream = [
            {"type": "thread.started", "thread_id": self.thread},
            {
                "type": "item.completed",
                "item": {"type": "command_execution", "aggregated_output": output},
            },
            {
                "type": "item.completed",
                "item": {
                    "type": "agent_message",
                    "text": f"{ANCHOR} {self.nonce}",
                },
            },
        ]

    def test_delivery_passes_but_mutable_identity_fails_gate(self) -> None:
        result = analyze(self.events, self.stream, self.nonce)
        self.assertTrue(result["delivery"]["pass"])
        self.assertEqual(result["identity"]["shell_lookup_route"], "PASS")
        self.assertEqual(result["identity"]["attempted_cases"], 4)
        self.assertEqual(result["identity"]["human_only_boundary"], "FAIL")
        self.assertEqual(result["gate"], "FAIL")

    def test_delivery_classifier_mutations_flip_the_verdict(self) -> None:
        mutations = {
            "anchor": lambda rows: rows[-1]["item"].update(text=self.nonce),
            "nonce": lambda rows: rows[-1]["item"].update(text=ANCHOR),
            "readiness": lambda rows: self.events.pop(1),
        }
        for name, mutate in mutations.items():
            with self.subTest(name=name):
                rows = copy.deepcopy(self.stream)
                events = copy.deepcopy(self.events)
                if name == "readiness":
                    events.pop(1)
                else:
                    mutate(rows)
                self.assertFalse(analyze(events, rows, self.nonce)["delivery"]["pass"])


def json_compact(value: object) -> str:
    import json

    return json.dumps(value, sort_keys=True, separators=(",", ":"))


if __name__ == "__main__":
    unittest.main()
