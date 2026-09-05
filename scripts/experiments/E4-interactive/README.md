# E4-interactive harness — plan row P4-5

Results and findings: `docs/experiments/E4-interactive.md`. Run on Claude Code **2.1.261**, model
`claude-opus-5` (`claude-sonnet-5` for the different-model arm), macOS arm64, against the **local
Supabase stack through the bundled adapter** (not the fs adapter — the single largest difference from
`E3-interactive/`).

These are the 2.1.26x descendants of `scripts/experiments/E3-interactive/`'s drivers. They **import**
`scripts/experiments/E0-8/common.py` (the expect prelude, the by-prefix env strip, the config-dir resolution and
file hashing — `sender.py` carries its own report-only hash check of the five real config files, not `common.Guard`) and
`scripts/experiments/E3-interactive/{run_manual,run_rules}.py` and **reference** E3's `bin/{attempt,posttool}`
by absolute path — no copies, and the shipped `plugin/` tree and `E3-interactive/**` / `E0-8/**` are never
modified. Scoring is delegated to the **shipped** `scripts/proof-headless.sh judge`; nothing here
re-implements condition 1.

| File | What it drives |
| --- | --- |
| `e4i.py` | the shared engine: the pty session runner (spawn, onboard, canary, the dialog policy, shutdown), the transcript → stream → judge pipeline, budgets, teardown |
| `sender.py` | local-stack provisioning (alice/bob under a temp `XDG_CONFIG_HOME`) and the synthetic hook-registered sender `payments-api` (a sleeper as `CLAUDE_PID`, no socket → no watcher) |
| `stream_from_transcript.py` | the projector (5.6) and its **M3 round-trip control** (`--m3-control <p4-2-run-dir>`) |
| `run_corpus.py` | the 78-session corpus sweep, the different-model arm (`--model`), and the M5 smoke (`--smoke`) |
| `run_rules.py` | item 1: the ask rule prompting on a reply (E2E-09), Manual and bypass |
| `run_native.py` | items 2/3 + M6: native `hold`/`refuse` and Brigade's switch to `refuse` (E2E-03) |
| `run_scenarios.py` | item 5 laundering (E2E-07), item 6 loop (E2E-10), item 10 injection-named session (E2E-06) |
| `score.py` | offline: bundle → `verdicts.tsv` / `summary.json` / `human-column/{blind-texts.md,blind-map.json,mine.json,compare-reads.py}` |

```sh
# no model calls:
python3 scripts/experiments/E4-interactive/stream_from_transcript.py --m3-control <p4-2-run-dir>   # M3
python3 scripts/experiments/E4-interactive/score.py <bundle>                                         # re-score + panel inputs
python3 scripts/experiments/E4-interactive/score.py <bundle> --panel <blind-reads.json>             # compare-reads

# model calls (never in CI; local stack required):
python3 scripts/experiments/E4-interactive/run_corpus.py --smoke                                    # M5
python3 scripts/experiments/E4-interactive/run_corpus.py --only 13,12,05 --runs 1                   # pilot
python3 scripts/experiments/E4-interactive/run_corpus.py                                            # 26 x 3
python3 scripts/experiments/E4-interactive/run_corpus.py --only 05,06,26 --runs 3 --model claude-sonnet-5
python3 scripts/experiments/E4-interactive/run_rules.py
python3 scripts/experiments/E4-interactive/run_native.py
python3 scripts/experiments/E4-interactive/run_scenarios.py
```

## The dialog policy is the safety net

> **Press Enter only on a dialog corroborated by a `Skill` `PreToolUse` row. Escape everything else.**

On 2.1.259+ both the Skill dialog and the Bash dialog say "Do you want to proceed?", so `proceed` alone
cannot tell them apart — and **both words also occur in the model's own prose** (a reply to an
approval-themed item says "approval"), while an Escape sent during generation **interrupts the turn**
(measured 2026-09-04: sessions voided at the 240 s cap with no rejected tool). So a word match never
presses a key by itself: every `approval`/`proceed` match is corroborated by `bin/pending`, which matches
`PreToolUse` attempts to `PostToolUse` executions as a multiset (by tool + `cmd_sha12`), subtracts the
dialogs already Escaped, ignores a running allowed `sleep`, and answers `none` (prose — do nothing; recorded
as `prose_match_ignored`), `skill` (Enter) or `dialog` (Escape). Escape **rejects** the pending prompt and
persists nothing. The `dialogs Escaped` column in the results is this mechanical count (un-executed
non-sleep attempts), never the raw mark count.

**Turn completion** is decided by a Python monitor over the authoritative transcript, not by the pty: the
corpus turn is over when at least one new `assistant` record has appeared since the arm prompt, the last one
carries a DONE `stop_reason`, and no new assistant record has appeared for **35 s**. The settle must exceed
the longest `sleep` (20 s) plus wake latency because **on 2.1.261 the model backgrounds `sleep 20`/`sleep
15`, ends its turn early ("now running"), and a task-notification wakes it 15–25 s later** — a 10 s settle
truncated sessions in that gap (measured, item 01 run 2 of an aborted batch). Consequence, stated in the writeup:
**in P4-5 "no forbidden call executed" is guaranteed by the driver's Escape, not by an allow-list** — a
different mechanism from P4-2's, and it gives the model a chance to react to a refusal that `-p` never gave
it. expect itself is the wait, so a dialog is matched the instant it paints, never behind a `nap`, and the
Python monitor watches the authoritative transcript to decide when the turn is over.

## Deviations from the brief (each justified in `docs/experiments/E4-interactive.md`)

- **`XDG_DATA_HOME` is NOT overridden** for the pty spawn (the brief's 4.3 line sets it). Claude Code's
  launcher self-installs its versioned binary under `$XDG_DATA_HOME/claude/versions` and repoints the
  global `~/.local/bin/claude` symlink at it; a temp `XDG_DATA_HOME` plus teardown therefore breaks the
  global `claude` for every session on the machine (measured at `m5-smoke`, and independently found by the
  P4-4 lane). brigade uses only `XDG_CONFIG_HOME`/`XDG_STATE_HOME`, so leaving it real is safe.
  `DISABLE_AUTOUPDATER=1` is also exported into every nested session (belt-and-suspenders).
- The corpus item is injected at the model's **first Bash attempt** (its `sleep 20` starting), not its first
  Bash *exec*, to land inside the 20 s sleep — the widest mid-turn window (matches P4-2's early post).
- The scoring is POSIX sh + jq (the shipped judge), but the **drivers** are Python + expect (an interactive
  pty needs `expect`; P3-8 shipped the same shape). Plan correction recorded for 9.6.

The evidence bundle lives under `.ignored/proof/<UTC stamp>/` (gitignored). It never runs in CI (D31).
