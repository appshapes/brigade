#!/usr/bin/env python3
"""E0-4 transcript analyser.

The on-disk session transcript is the authoritative record of what the receiving
session did and when: it carries the `queue-operation` enqueue/dequeue pair for
the socket-injected frame, the verbatim `user` record it became, and the
assistant records of the turn it provoked. Reading it settles the four questions
the criterion asks, independently of anything the driver believed at the time:

  idle_gap_s   how long the session sat with no turn running before the post
               (last event of the first turn -> enqueue). The criterion needs
               this to be comfortably positive, or the "wake" was really just
               the tail of a turn that had not finished.
  dequeue_ms   enqueue -> dequeue. This is the harness's own reaction, the part
               a watcher design depends on.
  wake_ms      enqueue -> first assistant record of the new turn.
  reflects     whether that turn's text carries the run's marker, i.e. the turn
               is a response to the frame and not unrelated output.

Usage: analyze.py <results-dir> [<results-dir> ...]
"""

import datetime
import json
import os
import sys


def parse_ts(s):
    if not s:
        return None
    try:
        return datetime.datetime.fromisoformat(s.replace("Z", "+00:00"))
    except ValueError:
        return None


def text_of(rec):
    m = rec.get("message") or {}
    c = m.get("content")
    if isinstance(c, str):
        return c
    out = []
    if isinstance(c, list):
        for b in c:
            if isinstance(b, dict) and b.get("type") == "text":
                out.append(b.get("text", ""))
    return "\n".join(out)


def analyze(results_dir):
    tpath = os.path.join(results_dir, "session-transcript.jsonl")
    vpath = os.path.join(results_dir, "verdict.json")
    if not os.path.exists(tpath):
        return {"dir": results_dir, "error": "no session-transcript.jsonl"}
    verdict = {}
    if os.path.exists(vpath):
        with open(vpath) as f:
            verdict = json.load(f)
    marker = verdict.get("wake_token") or verdict.get("nonce") or ""

    recs = []
    for line in open(tpath):
        line = line.strip()
        if not line:
            continue
        try:
            r = json.loads(line)
        except ValueError:
            continue
        t = parse_ts(r.get("timestamp"))
        if t:
            recs.append((t, r))
    recs.sort(key=lambda x: x[0])

    # The frame's enqueue is the LAST enqueue whose paired user record carries the
    # brigade frame; the first prompt also arrives through the queue in -p mode.
    frame_user_i = None
    for i, (t, r) in enumerate(recs):
        if r.get("type") == "user" and "<brigade-message" in text_of(r):
            frame_user_i = i
    if frame_user_i is None:
        return {"dir": results_dir, "error": "frame never reached the transcript"}

    enq = deq = None
    for t, r in recs[:frame_user_i]:
        if r.get("type") == "queue-operation":
            if r.get("operation") == "enqueue":
                enq = t
            elif r.get("operation") == "dequeue":
                deq = t
    t_frame_user = recs[frame_user_i][0]

    # The first turn: everything strictly before the frame's enqueue.
    before = [(t, r) for t, r in recs if enq and t < enq]
    first_turn_end = None
    for t, r in before:
        if r.get("type") in ("assistant", "system", "user"):
            first_turn_end = t

    after = [(t, r) for t, r in recs[frame_user_i + 1:]]
    woken = [(t, r) for t, r in after if r.get("type") == "assistant"]
    woken_text = "\n".join(text_of(r) for _t, r in woken)

    def ms(a, b):
        return None if (a is None or b is None) else int((b - a).total_seconds() * 1000)

    out = {
        "dir": os.path.basename(results_dir),
        "mode": verdict.get("mode", "stream-json -p"),
        "marker": marker,
        "first_turn_end": first_turn_end.isoformat() if first_turn_end else None,
        "enqueue": enq.isoformat() if enq else None,
        "idle_gap_s": round((enq - first_turn_end).total_seconds(), 2)
                      if (enq and first_turn_end) else None,
        "enqueue_to_dequeue_ms": ms(enq, deq),
        "enqueue_to_frame_user_ms": ms(enq, t_frame_user),
        "enqueue_to_first_assistant_ms": ms(enq, woken[0][0]) if woken else None,
        "woke": bool(woken),
        "assistant_records_in_woken_turn": len(woken),
        "reflects_frame": bool(marker) and marker in woken_text,
        "woken_text_head": woken_text.strip()[:300],
    }
    return out


def main():
    dirs = sys.argv[1:]
    rows = [analyze(d) for d in dirs]
    print(json.dumps(rows, indent=2))


if __name__ == "__main__":
    main()
