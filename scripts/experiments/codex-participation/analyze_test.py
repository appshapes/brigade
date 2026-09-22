from __future__ import annotations

import copy
import importlib.util
import unittest

from analyze import (
    ANCHOR,
    DEFAULT_THRESHOLD,
    SDK_MISSING,
    analyze,
    digest,
    exact_judge,
    noul,
    sdk,
    typesafe_judge,
)


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

    def test_paraphrase_fails_exact_anchor_but_passes_a_judge(self) -> None:
        rows = copy.deepcopy(self.stream)
        rows[-1]["item"].update(
            text=f"A teammate relayed a request through Brigade; {self.nonce}"
        )
        exact = analyze(self.events, rows, self.nonce)
        self.assertFalse(exact["delivery"]["anchor_in_assistant_text"])
        self.assertFalse(exact["delivery"]["pass"])

        judged = analyze(self.events, rows, self.nonce, stub_judge(0.93))
        self.assertFalse(judged["delivery"]["anchor_in_assistant_text"])
        self.assertTrue(judged["delivery"]["judged_received"])
        self.assertTrue(judged["delivery"]["pass"])

    def test_judgment_never_excuses_a_missing_nonce_or_lifecycle(self) -> None:
        confident = stub_judge(1.0)
        no_nonce = copy.deepcopy(self.stream)
        no_nonce[-1]["item"].update(text=ANCHOR)
        self.assertFalse(analyze(self.events, no_nonce, self.nonce, confident)["delivery"]["pass"])

        short_events = copy.deepcopy(self.events)
        short_events.pop(1)
        self.assertFalse(
            analyze(short_events, self.stream, self.nonce, confident)["delivery"]["pass"]
        )

    def test_default_judge_is_exact_and_records_no_probability(self) -> None:
        result = analyze(self.events, self.stream, self.nonce)
        self.assertEqual(result["delivery"]["judge"], "exact")
        self.assertIsNone(result["delivery"]["judged_probability"])
        self.assertEqual(exact_judge(ANCHOR)["received"], True)
        self.assertEqual(exact_judge("nothing arrived")["received"], False)

    def test_typesafe_judge_thresholds_the_probability(self) -> None:
        for probability, expected in (
            (DEFAULT_THRESHOLD - 0.01, False),
            (DEFAULT_THRESHOLD, True),
            (0.99, True),
            (0.0, False),
        ):
            with self.subTest(probability=probability):
                verdict = fake_typesafe_judge(probability)("some transcript")
                self.assertEqual(verdict["received"], expected)
                self.assertEqual(verdict["judge"], "typesafe")
                self.assertEqual(verdict["probability"], probability)

    def test_typesafe_judge_sends_the_transcript_as_state(self) -> None:
        client = FakeClient(0.9)
        typesafe_judge(
            DEFAULT_THRESHOLD, lambda: client, lambda: "question"
        )("the transcript")
        self.assertEqual(client.state, {"transcript": "the transcript"})
        self.assertEqual(client.questions, {"received": "question"})

    def test_typesafe_judge_verdict_drives_the_gate(self) -> None:
        rows = copy.deepcopy(self.stream)
        rows[-1]["item"].update(text=f"a teammate asked via Brigade; {self.nonce}")
        result = analyze(self.events, rows, self.nonce, fake_typesafe_judge(0.95))
        self.assertTrue(result["delivery"]["pass"])
        self.assertEqual(result["delivery"]["judged_probability"], 0.95)

    def test_absent_sdk_explains_itself_instead_of_a_traceback(self) -> None:
        if importlib.util.find_spec("typesafe_sdk") is not None:
            self.skipTest("typesafe-sdk is installed")
        with self.assertRaises(SystemExit) as caught:
            sdk()
        self.assertEqual(str(caught.exception), SDK_MISSING)

    def test_noul_accepts_either_documented_accessor(self) -> None:
        self.assertEqual(noul(NoulsResponse(0.4), "received"), 0.4)
        self.assertEqual(noul(AnswersResponse(0.6), "received"), 0.6)

    def test_noul_names_the_answer_it_could_not_find(self) -> None:
        with self.assertRaises(KeyError):
            noul(NoulsResponse(0.4), "absent")


def json_compact(value: object) -> str:
    import json

    return json.dumps(value, sort_keys=True, separators=(",", ":"))


def stub_judge(probability: float):
    """An injected judge, so no test reaches the network."""

    def judge(text: str) -> dict:
        return {
            "judge": "typesafe",
            "received": probability >= DEFAULT_THRESHOLD,
            "probability": probability,
        }

    return judge


def fake_typesafe_judge(probability: float, threshold: float = DEFAULT_THRESHOLD):
    """The real typesafe_judge with a fake client, so no test reaches the network."""
    return typesafe_judge(
        threshold, lambda: FakeClient(probability), lambda: "question"
    )


class NoulsResponse:
    def __init__(self, probability: float) -> None:
        self.nouls = {"received": Answer(probability)}


class AnswersResponse:
    def __init__(self, probability: float) -> None:
        self.answers = {"received": Answer(probability)}


class Answer:
    def __init__(self, probability: float) -> None:
        self.noul = probability


class FakeClient:
    def __init__(self, probability: float) -> None:
        self.probability = probability
        self.state: object = None
        self.questions: object = None

    def __enter__(self) -> "FakeClient":
        return self

    def __exit__(self, *_: object) -> None:
        return None

    def system_one(self, state: object, questions: object) -> NoulsResponse:
        self.state = state
        self.questions = questions
        return NoulsResponse(self.probability)


if __name__ == "__main__":
    unittest.main()
