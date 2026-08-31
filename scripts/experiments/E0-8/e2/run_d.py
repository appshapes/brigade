#!/usr/bin/env python3
"""E0-8 (d): does the SessionStart context line carry EXACTLY the hook's stdout?

The authority is the TRANSCRIPT, not the model. Claude Code writes every turn of
a session to $CLAUDE_CONFIG_DIR/projects/<slug>/<session-id>.jsonl, and the
SessionStart hook's output lands there as its own entry. Comparing that entry's
text with the bytes the hook wrote to a file at the same moment it wrote them to
stdout is a byte-for-byte comparison with nothing in between -- no model, no
terminal, no re-rendering.

The payload is adversarial on purpose (leading and trailing spaces, a tab, a
CRLF, an interior blank line, a markdown-looking bullet, a non-ASCII character,
and a variant with no trailing newline) so that trimming, normalisation or
re-wrapping would be visible rather than plausible.
"""
import argparse
import glob
import json
import os
import secrets
import shutil
import subprocess
import sys
import time

HERE = os.path.dirname(os.path.realpath(__file__))
PLUGIN = os.path.join(HERE, "plugin_d")


def nested_env(extra=None):
    out, stripped = {}, []
    for k, v in os.environ.items():
        if k == "CLAUDE_CONFIG_DIR":
            out[k] = v
        elif k.startswith("CLAUDE") or k in ("CLAUDECODE", "AI_AGENT"):
            stripped.append(k)
        else:
            out[k] = v
    if extra:
        out.update(extra)
    return out, sorted(stripped)


def cfgdir():
    return os.environ.get("CLAUDE_CONFIG_DIR") or os.path.expanduser("~/.claude")


def slug(path):
    return path.replace("/", "-").replace(".", "-") if False else None


def find_transcript(session_id):
    pat = os.path.join(cfgdir(), "projects", "*", session_id + ".jsonl")
    hits = glob.glob(pat)
    return hits[0] if hits else None


def walk_strings(obj, path="$"):
    """Every string anywhere in the transcript entry, with its json path."""
    if isinstance(obj, str):
        yield path, obj
    elif isinstance(obj, dict):
        for k, v in obj.items():
            yield from walk_strings(v, path + "." + k)
    elif isinstance(obj, list):
        for i, v in enumerate(obj):
            yield from walk_strings(v, path + "[%d]" % i)


def one(variant, idx, args):
    results = os.path.join(HERE, "results", "d", "%s-run%d" % (variant, idx))
    shutil.rmtree(results, ignore_errors=True)
    os.makedirs(results)
    state = os.path.join(results, "state")
    os.makedirs(state)
    proj = os.path.join(HERE, "proj", "d-%s-%d" % (variant, idx))
    shutil.rmtree(proj, ignore_errors=True)
    os.makedirs(proj)
    nonce = "E08D-" + secrets.token_hex(4).upper()

    env, stripped = nested_env({"BRIGADE_E08_STATE": state,
                                "BRIGADE_E08_NONCE": nonce,
                                "BRIGADE_E08_VARIANT": variant})
    # The transcript is the authority; this prompt is the CORROBORATION. It asks
    # the model to echo the hook-produced context back verbatim, so the answer
    # can be compared against both the hook's raw stdout and the transcript's
    # trimmed `content`, and any wrapper text around it is reported too.
    prompt = (
        "At the start of this session a SessionStart hook injected some text into your "
        "context. Do not use any tool. Answer in exactly this form and nothing else:\n"
        "WRAPPER_BEFORE: <<<the text, if any, that appears immediately BEFORE that "
        "hook-produced text, or the word NONE>>>\n"
        "HOOK_TEXT: <<<the hook-produced text reproduced EXACTLY, character for "
        "character, including every leading space, trailing space, tab, and blank line>>>\n"
        "WRAPPER_AFTER: <<<the text, if any, that appears immediately AFTER it, or the word NONE>>>"
    )
    cmd = ["claude", "-p", prompt,
           "--plugin-dir", PLUGIN, "--output-format", "json"]
    t0 = time.time()
    p = subprocess.run(cmd, cwd=proj, env=env, capture_output=True, text=True,
                       stdin=subprocess.DEVNULL, timeout=args.hard_timeout)
    wall = time.time() - t0
    open(os.path.join(results, "claude.stdout.json"), "w").write(p.stdout)
    open(os.path.join(results, "claude.stderr.txt"), "w").write(p.stderr)
    try:
        res = json.loads(p.stdout)
    except Exception:
        res = {}
    sid = res.get("session_id")

    raw = b""
    bp = os.path.join(state, "ctx-stdout.bin")
    if os.path.exists(bp):
        raw = open(bp, "rb").read()
    want = raw.decode("utf-8")

    tpath = find_transcript(sid) if sid else None
    entries = []
    if tpath:
        shutil.copy2(tpath, os.path.join(results, "transcript.jsonl"))
        for line in open(tpath):
            line = line.strip()
            if line:
                try:
                    entries.append(json.loads(line))
                except Exception:
                    pass

    # Locate every string in the transcript that contains the nonce.
    hits = []
    for n, e in enumerate(entries):
        for path, s in walk_strings(e):
            if nonce in s:
                hits.append({"entry": n, "entry_type": e.get("type"),
                             "json_path": path, "len": len(s),
                             "hex": s.encode("utf-8").hex(),
                             "repr": repr(s),
                             "equals_hook_stdout": s == want,
                             "equals_stripped": s.strip() == want.strip(),
                             "contains_hook_stdout": want in s,
                             "prefix_before": s.split(nonce)[0][:400],
                             "suffix_after": s.split(nonce)[-1][:400]})
    # The hook's stderr is RECORDED in the transcript's attachment row (that is
    # bookkeeping, and is expected). Whether it reaches the model is settled by
    # docs and is not re-measured here; what IS checked is whether the model's
    # own answer quotes it back, which would be a live contradiction.
    answer = res.get("result") or ""
    stderr_in_attachment = any(
        h.get("json_path", "").endswith(".stderr") for h in hits)
    stderr_echoed_by_model = "must never reach the model" in answer

    content_hits = [h for h in hits if h["json_path"].endswith(".content")]
    exact = any(h["equals_hook_stdout"] for h in content_hits)
    rec = {"variant": variant, "run": idx, "nonce": nonce,
           "session_id": sid, "transcript": tpath,
           "hook_stdout_bytes": len(raw), "hook_stdout_hex": raw.hex(),
           "hook_stdout_repr": repr(want),
           "transcript_hits": hits,
           "injected_content_equals_hook_stdout": exact,
           "injected_content_equals_stripped": any(h["equals_stripped"] for h in content_hits),
           "injected_content_repr": content_hits[0]["repr"] if content_hits else None,
           "delta": ("none" if exact else
                     ("leading/trailing whitespace trimmed"
                      if content_hits and content_hits[0]["equals_stripped"] else "other")),
           "stderr_present_in_transcript_attachment": stderr_in_attachment,
           "stderr_echoed_by_model": stderr_echoed_by_model,
           "model_answer": answer,
           "claude_wall_s": round(wall, 3), "claude_exit": p.returncode,
           "claude_result": (res.get("result") or "")[:200],
           "stripped": stripped, "results": results}
    json.dump(rec, open(os.path.join(results, "summary.json"), "w"), indent=2)
    return rec


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--reps", type=int, default=3)
    ap.add_argument("--variants", default="rich,oneline,nonl")
    ap.add_argument("--hard-timeout", type=float, default=180)
    a = ap.parse_args()
    rows = []
    for v in a.variants.split(","):
        for i in range(1, a.reps + 1):
            r = one(v, i, a)
            rows.append(r)
            print("[%s run%d] hits=%d exact=%s delta=%s stderr_echoed=%s" % (
                v, i, len(r["transcript_hits"]),
                r["injected_content_equals_hook_stdout"], r["delta"],
                r["stderr_echoed_by_model"]))
            print("    MODEL: %s" % r["model_answer"].replace("\n", " | ")[:600])
            for h in r["transcript_hits"]:
                print("    %s entry=%s type=%s eq=%s eq_stripped=%s contains=%s len=%d"
                      % (h["json_path"], h["entry"], h["entry_type"],
                         h["equals_hook_stdout"], h["equals_stripped"],
                         h["contains_hook_stdout"], h["len"]))
                print("      hook repr: %s" % r["hook_stdout_repr"])
                print("      ctx  repr: %s" % h["repr"])
            sys.stdout.flush()
    json.dump(rows, open(os.path.join(HERE, "results", "d", "all.json"), "w"), indent=2)


if __name__ == "__main__":
    main()
