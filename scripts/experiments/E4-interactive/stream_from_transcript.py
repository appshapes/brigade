#!/usr/bin/env python3
"""Project an interactive on-disk transcript into a `stream.jsonl` the shipped
`proof-headless.sh judge` can score (P4-5, brief 5.6).

WHY THIS EXISTS
---------------
The judge requires `stream.jsonl` and returns early without it
(`proof-headless.sh:316`), and an interactive session has NO stream: its stdout
is a terminal, and `--output-format stream-json` only works with `--print`. But
the on-disk transcript `$CLAUDE_CONFIG_DIR/projects/<slug>/<native id>.jsonl`
carries the SAME `assistant`/`user` records in the SAME `.message.content[]`
shape (verified against `.ignored/proof/20260904T012337Z/evidence/
13-benign-control/run1`), so the judge's `$uses`, `$results`, `$final`,
`$denials`, `$sends` and the whole `$forbidden` computation work unchanged off
them. Exactly two record types are missing and this projector supplies them,
each field TRACED to a record, nothing invented:

  system/init   -- model from the transcript's own `assistant.message.model`,
                   session_id from the file name, claude_code_version from
                   `assistant.version`, messaging_socket_path from the by-pid map
                   read live mid-session (passed in). `tools` is OMITTED: an
                   interactive transcript does not carry it, so
                   `tools_has_sendmessage`/`tools_has_slashcommand` are false in
                   every interactive verdict and MUST NOT be read (cite P4-2's
                   84/84 instead).
  result        -- {"subtype":"success","is_error":false} when the last
                   `assistant` record's stop_reason is end_turn/stop_sequence AND
                   the turn settled; {"subtype":"success","is_error":true,
                   "stop_reason":"refusal"} when it is refusal (M4's
                   transcript-native signature); OMITTED otherwise, which
                   correctly voids a session that never finished a turn.

M3 (the round-trip control) is the positive control for the whole projection: it
projects a P4-2 run's transcript, drops the real stream, runs the shipped judge
and asserts condition1/forbidden/final_text/brigade_sends/skill_loaded/denials/
tool_uses -- and delivered/api_refused/void_reasons, added at verification --
are IDENTICAL to the real verdict. It must be green before one pty session is
spawned. If a field differs, fix the projector, not the judge.

usage:
    stream_from_transcript.py <transcript.jsonl> [--out <stream.jsonl>]
        [--native-id <id>] [--socket <path>] [--map <map.json>]
        [--settled true|false]
    stream_from_transcript.py --m3-control <p4-2-run-dir> [--repo <repo>]
"""
import argparse
import json
import os
import shutil
import subprocess
import sys
import tempfile

sys.dont_write_bytecode = True
# The repository root, from this file's own location (scripts/experiments/E4-interactive/),
# never a hardcoded home path (P4-5 verification: the original literal was one machine's).
REPO = os.path.dirname(os.path.dirname(os.path.dirname(os.path.dirname(os.path.realpath(__file__)))))

# The last-assistant stop_reasons that mean a turn finished cleanly.
CLEAN_STOPS = ("end_turn", "stop_sequence")


def read_jsonl(path):
    out = []
    with open(path, errors="replace") as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                out.append(json.loads(line))
            except Exception:
                pass
    return out


def last_assistant(records):
    """The last `assistant` record (the one whose stop_reason decides result)."""
    last = None
    for d in records:
        if d.get("type") == "assistant":
            last = d
    return last


def project(records, native_id, socket_path, settled):
    """Return the list of stream records: init, then every assistant/user record
    verbatim in order, then (maybe) a result. Every field traces to a record."""
    model = ""
    version = ""
    for d in records:
        if d.get("type") == "assistant":
            m = (d.get("message") or {})
            if not model and m.get("model"):
                model = m.get("model")
            if not version and d.get("version"):
                version = d.get("version")
    init = {
        "type": "system", "subtype": "init", "synthetic": True,
        "source": "stream_from_transcript.py",
        "model": model,
        "session_id": native_id,
        "claude_code_version": version,
        "messaging_socket_path": socket_path or "",
    }
    stream = [init]
    for d in records:
        if d.get("type") in ("assistant", "user"):
            stream.append(d)

    la = last_assistant(records)
    sr = ((la or {}).get("message") or {}).get("stop_reason") if la else None
    result = None
    if sr == "refusal":
        # M4: the provider refusal is transcript-native.
        result = {"type": "result", "subtype": "success", "is_error": True,
                  "stop_reason": "refusal", "synthetic": True,
                  "result": _final_text(records)}
    elif sr in CLEAN_STOPS and settled:
        result = {"type": "result", "subtype": "success", "is_error": False,
                  "stop_reason": sr, "synthetic": True,
                  "result": _final_text(records)}
    # else: OMITTED -- a turn that never finished (max_turns, an interrupted
    # session) has no result, and the judge voids it.
    if result is not None:
        stream.append(result)
    return stream


def _final_text(records):
    """The last non-blank assistant text block -- what the judge falls back to
    for `.result.result` when a result is emitted (never invented)."""
    txt = ""
    for d in records:
        if d.get("type") != "assistant":
            continue
        for c in ((d.get("message") or {}).get("content") or []):
            if isinstance(c, dict) and c.get("type") == "text" and str(c.get("text", "")).strip():
                txt = c["text"]
    return txt


def socket_from_map(map_path):
    try:
        with open(map_path) as f:
            return json.load(f).get("socket_path", "") or ""
    except Exception:
        return ""


def write_stream(transcript_path, out_path, native_id, socket_path, settled):
    records = read_jsonl(transcript_path)
    if native_id is None:
        native_id = os.path.basename(transcript_path)[: -len(".jsonl")] if transcript_path.endswith(".jsonl") else ""
    stream = project(records, native_id, socket_path, settled)
    with open(out_path, "w") as f:
        for rec in stream:
            f.write(json.dumps(rec) + "\n")
    return stream


# --------------------------------------------------------------------------- #
# M3 -- the round-trip control over a P4-2 run directory
# --------------------------------------------------------------------------- #
# The brief's seven fields, plus three the verifier added (P4-5 verification,
# 2026-09-04): `delivered`, `api_refused` and `void_reasons` are computed by the
# judge from the transcript's own records and the projected `result`, so the
# projection must reproduce them too -- and without them M3 could not see a
# projector that emits no `result` (judge: void) or one that emits a refusal
# (judge: api_refused). Measured: still 84/84 on P4-2's runs; a transcript
# mutated to a non-clean last stop, to `refusal`, or with its last assistant
# record deleted now fails the control on exactly those fields.
M3_FIELDS = ("condition1", "forbidden", "final_text", "brigade_sends",
             "skill_loaded", "denials", "tool_uses",
             "delivered", "api_refused", "void_reasons")


def _norm(field, value):
    # tool_uses may carry a version-specific `head`; compare names+executed only.
    if field == "tool_uses":
        return [{"name": t.get("name"), "executed": t.get("executed")} for t in (value or [])]
    if field == "forbidden":
        return sorted(json.dumps(x, sort_keys=True) for x in (value or []))
    if field == "brigade_sends":
        return sorted(json.dumps(x, sort_keys=True) for x in (value or []))
    return value


def m3_control(rundir, repo):
    """Project the P4-2 run's transcript, drop the real stream, run the shipped
    judge, and compare the projected verdict to the real one field by field."""
    rundir = os.path.abspath(rundir)
    real_verdict_path = os.path.join(rundir, "verdict.json")
    tr = os.path.join(rundir, "transcript.jsonl")
    if not os.path.exists(tr):
        print("m3: FAIL: no transcript.jsonl in %s" % rundir)
        return 2
    with open(real_verdict_path) as f:
        real = json.load(f)

    work = tempfile.mkdtemp(prefix="e4i-m3-")
    try:
        # Copy every file the judge reads EXCEPT stream.jsonl (dropped on purpose).
        for name in ("transcript.jsonl", "send.json", "meta.json", "map.json",
                     "decoys.before.sha256", "decoys.after.sha256"):
            src = os.path.join(rundir, name)
            if os.path.exists(src):
                shutil.copy2(src, os.path.join(work, name))
        socket = socket_from_map(os.path.join(work, "map.json"))
        write_stream(os.path.join(work, "transcript.jsonl"),
                     os.path.join(work, "stream.jsonl"),
                     native_id=None, socket_path=socket, settled=True)
        subprocess.run(["sh", os.path.join(repo, "scripts", "proof-headless.sh"),
                        "judge", work], check=True,
                       stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
        with open(os.path.join(work, "verdict.json")) as f:
            proj = json.load(f)
    finally:
        shutil.rmtree(work, ignore_errors=True)

    ok = True
    for field in M3_FIELDS:
        a = _norm(field, real.get(field))
        b = _norm(field, proj.get(field))
        same = a == b
        ok = ok and same
        mark = "ok " if same else "DIFF"
        if field in ("final_text",):
            print("m3: %s %-14s real=%r proj=%r" % (mark, field, str(a)[:60], str(b)[:60]))
        else:
            print("m3: %s %-14s %s" % (mark, field, "identical" if same else ("real=%r proj=%r" % (a, b))))
    print("m3: %s (%s)" % ("PASS" if ok else "FAIL", os.path.basename(os.path.dirname(rundir)) + "/" + os.path.basename(rundir)))
    return 0 if ok else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("transcript", nargs="?")
    ap.add_argument("--out")
    ap.add_argument("--native-id")
    ap.add_argument("--socket", default="")
    ap.add_argument("--map")
    ap.add_argument("--settled", default="true")
    ap.add_argument("--m3-control")
    ap.add_argument("--repo", default=REPO)
    args = ap.parse_args()

    if args.m3_control:
        sys.exit(m3_control(args.m3_control, args.repo))

    if not args.transcript:
        ap.error("a transcript path (or --m3-control) is required")
    socket = args.socket or (socket_from_map(args.map) if args.map else "")
    out = args.out or (args.transcript.rsplit(".jsonl", 1)[0] + ".stream.jsonl")
    settled = args.settled.lower() in ("1", "true", "yes")
    stream = write_stream(args.transcript, out, args.native_id, socket, settled)
    print("wrote %d stream records to %s" % (len(stream), out))


if __name__ == "__main__":
    main()
