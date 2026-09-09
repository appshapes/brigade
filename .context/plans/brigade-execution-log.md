# Brigade execution log

The single source of truth for **where the work stands**. The plan says what to build; this file says what is done,
what is next, and which model tier each remaining task should run on. Update it in the same commit as the work.

Ticket 15 · branch `master` · merges only, never rebase · commit messages `15: <Imperative summary>` ·
`make push message="15: …"` for every commit after the first.

**0.4.1 is released (2026-09-08, after 0.3.0 and 0.4.0 the same day); 0.2.0 was released 2026-09-07 and 0.1.0 on 2026-09-06.** The full record through the release — every Status row, DONE section, plan
correction and ruling — was moved verbatim to `.context/plans/brigade-execution-log-archive.md` on 2026-09-06;
a reference of the form `see "P5-10 DONE — 0.1.0 RELEASED"` resolves there; on 2026-09-08, after 0.4.1, the twenty-one
rows that carried the 0.2.0–0.4.1 work (P5-16, P5-19, the eighteen P7 rows P7-1..P7-16 with 6b, 6c and 11b, F4 — every one `done` or `won't do`) followed, verbatim,
below the archive's marker dated 2026-09-08. The complete post-release work list
(deferred measurements, open findings, conditional obligations, backlog) is
`.context/plans/v0.1.0-followups.md`; this file carries only the rows being actively worked.

## Start here (for a fresh session, a resumed session, or another developer)

1. Read `.context/plans/claude-code-team-messaging-implementation-plan.md` — the index — then
   `implementation/02-decisions.md` (decisions, all marked **Decided 2026-08-30** are settled),
   `implementation/08-phases.md` (phases and tasks), `implementation/13-commit-plan.md` (the commit plan).
   `.context/plans/claude-code-team-messaging-logical-plan.md` is the conceptual design behind it.
2. Read `docs/research/README.md` for what the eleven digests contain and `docs/research/decisions-2026-08-30.md`
   for the decision brief (it overrides the plan where they disagree).
3. Read `.context/plans/v0.1.0-followups.md` for everything open after 0.1.0, then find the first row below with
   status `todo`, run it at the model tier in its column, and update its row.
   On 2026-09-08 Rjae closed P5-16, P7-6c and F4 after the follow-ups triage (P7-16) under the bar that an item must be
   proven high value to be worked, and the twenty-one finished rows moved to the archive. **The one `todo` row is P8-1**,
   the capability experiment that opens Frank's Codex-participation brief (`.context/plans/codex-team-participation.md`),
   which Rjae ruled doable for boundary receipt on 2026-09-08; the brief's P2–P5 become rows only from P0's capability
   table. **The next substantial work after that is a second adapter**, which Rjae will chart as its own brief; nothing
   is to be started on it until then.

## Model tier policy

The user's Fable 5 usage is a hard budget (64% consumed at the time of planning). Run **Fable 5** only where the
quality difference matters; run **Opus 5** wherever it can succeed. If the Fable budget runs out while
Fable-tier tasks remain, **stop and wait for the reset** rather than downgrading them — the user stated this
explicitly and prefers waiting to a subpar implementation. The user may also hand the work to another developer
with her own Fable 5 session on this machine, which is why this log lives in the repository.

- **Fable 5**: protocol/schema design, Supabase SQL (RLS, RPCs, triggers), the security path (frame, sanitiser,
  socket poster, inbound policy, watcher), the conformance suite's design, adversarial review/verification passes,
  and any task whose acceptance criteria include a negative security test.
- **Opus 5**: scaffolding, Makefile/CI/goreleaser, the bootstrap script, docs, running scripted experiments and
  recording their results, test plumbing, mechanical ports, evidence collection.

**Cadence within a Fable-tier task (decided by Rjae, 2026-08-30).** Run **one author agent plus one full adversarial
verifier** — roughly 4–6 Fable subagents per task. No multi-lens design panels, no separate completeness critic. The
evidence for putting the budget here: the adversarial pass found **7** assertions passing for the wrong reason in E0-1
and **15** in E0-2 (including a soak that would have passed having sent zero messages, and a "50 concurrent senders"
claim that was never actually measured). Both authors reported fully green beforehand and neither caught its own gaps.
The design panels, by contrast, mostly yielded nice-to-have extras. So when the budget is trimmed, the verifier is what
survives. Prompt the verifier to assume a pass is for the wrong reason, to check that each assertion *could* fail, to
demand positive controls, and to strengthen weak checks in place before reporting. When the driver session's main loop
is a smaller model, Fable-tier work goes to subagents (`model: 'fable'`), never inline.

## Status

Legend: `done` · `todo` · `blocked (<reason>)` · `wip` · `won't do (<date>)`. Every row through the 0.4.1 release is in the archive.

| ID | Task (plan §, named in the row) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| P8-1 | **Codex participation — P0, the capability experiment** (§3 of `.context/plans/codex-team-participation.md`, Frank's brief; reviewed 2026-09-08 by `15-0908` with three lenses plus critics, findings sent to `frank-brigade-main` as Brigade message `c04031ec`): a fixture Codex plugin under `scripts/experiments/codex-participation/` and the report `docs/experiments/codex-participation.md`, run on the real Codex client, answering — with the review's additions — (1) whether a host-set per-conversation marker reaches a shell-tool child that the model cannot set (the `CLAUDE_PID` analog: `config.InSession` keys on it, so without one the `hold` policy's human-only release, `revoke-member` and `transfer` cannot be protected on Codex); (2) a stable native thread id in hook stdin and two concurrent threads without collision, proven the E0-7 way (a per-thread nonce written only to the private map, `whoami --json` from each thread's shell returns its own, a poison control ignored); (3) whether hook stdout reaches the model, what Codex wraps around it verbatim, and its output cap; (4) the hook timeout — default, per-hook configurability, kill signal, fate of stdout already written; (5) a null control in its own fresh session (nothing sent, fixed window, instrument named) beside the idle-wake attempt, labelled as such. Pass predicate: a per-run split nonce in the receiving model's own text, never the frame echoed back in the host's own output (Claude's `origin.body`); sink, mock and worker-only arms score FAIL or are excluded; a positive control and mutations that flip each verdict. The report is re-checked by an adversarial verifier before any P2 row is opened | wip | Opus (run + record) + Fable verifier | added 2026-09-08 from `.context/plans/codex-team-participation.md` §3 (P0; reviewed at 3338225, revised at d7088fc); owner: Frank's team (the brief's author is a Codex session; Frank assigns the runner); Rjae's ruling 2026-09-08: doable for outcome 1 (boundary receipt) — the brief's P2–P5 become rows only from P0's capability table, recorded in `docs/experiments/codex-participation.md` (this log stays the single status authority; the brief's revised §7 agrees); identity FAIL with no valid binding handle is a hard stop after P1; outcome 2 (idle wake) is not expected through hooks; known window on the shared poll path, to be settled in the brief's P3 with P0's killed-hook stdout measurement and not before: the seen file is persisted before the single batch ack (inbound/pipeline.go:369 and :668, hook/prompt.go:239-247), so a hook killed after printing acks silently at the next poll — a loss if the host discarded that stdout, not a duplicate (Rjae 2026-09-08: no row of its own; `poll_on_prompt` is off by default and every Claude Code session has the socket). **First run, 2026-09-08 (CLI 0.153.4, single-thread):** `docs/experiments/codex-participation.md` and `docs/experiments/codex-participation-evidence.json` — hook-to-direct-shell identity PASS (hashed `CODEX_SESSION_ID`/`CODEX_THREAD_ID` match the hook `session_id`); delivery PASS in 5/5 author runs (40-char anchor plus split nonce in the model's own text); human-only enforcement boundary FAIL (unset/poisoned/nested child controls all defeat the marker); Fable verifier unavailable in that runtime; `make test` red on that host, plausibly an arm64/amd64 Go-toolchain (Rosetta) mismatch against the 3 s `WatchRequestTimeout`/`DescribeTimeout` (`adapterclient/client.go:53,55`), not yet attributed to Brigade. The run's own handoff called this a hard stop; `15-0908`'s re-review (Brigade message `fcc4b17f`, verified word-for-word against this brief's §3) corrected that: identity PASSED for the single-thread case; the boundary result is neither a hard stop nor a restriction — under the security model (allow by default; Brigade does not protect a harness from message content, the host's own mechanisms do — Rjae, 2026-09-09) it is a capability-row fact, parity with Claude (see F5): the refusals stay best-effort, Codex's approval policy is recorded as the gate, nothing is hardened or disabled. (The re-review's "restricted-release branch" wording is superseded by that ruling.) **Outstanding before P2 opens:** the two-thread identity+delivery arm with a cross-thread poison control, a proper null control (nothing sent, fresh session, live hooks, 60 s window), a new capability row for Codex's own approval/sandbox policy as the durable enforcement layer (the Claude analog: `Bash(brigade:*)` permission matching, not the environment marker), and the Fable adversarial verifier's sign-off. **Verifier pass, 2026-09-09 (`15-0908`; two independent verifiers — an instrument attack with synthetic streams and a report audit against §3 — with the decisive lines re-read by hand; Brigade message `6f48316a`): NOT SIGNED, P2 stays closed.** Five highs: the boundary classifier fails open (analyze.py:107 — each control is False when its label is absent; the facts are parsed from model-writable command output, :50-63 — a lone or forged DIRECT line scores gate PASS); NESTED is `unset.copy()` (probe.py:84) — three cases, not four, and the genuinely nested observation (the DIRECT chain) shows nesting KEEPS the markers; the delivery nonce rides codex's launch environment (fixture/README.md:19) into every shell child, no digest recorded, a bare substring test (:106); the 5/5 is a hand-written aggregate the analyzer cannot have produced (evidence JSON keys ≠ analyze.py:108-128; no per-run records, no driver); every arm ran in `codex exec --json` with `--ask-for-approval never --sandbox read-only --dangerously-bypass-hook-trust`, undisclosed in the report — not a desktop conversation, the null/idle arms impossible by construction, the approval layer switched off. Mediums: unidentified-session scoring (:83-84), BARE uninstrumented, timeout/cap documented-only, the report header and evidence JSON still say hard stop against this row, C06/C13/C19/C23 mislabelled, `make test` red unattributed (no GOARCH recorded). Kept as right: the anchor (frame.go:103), the join requirement, the three mutations, the hashing, and privacy (nothing leaks). Completing arms, in priority order, sent with the message: a real nested case + an all-four-labels analyzer + the Claude F5 parity control; interactive-client null and idle-wake arms with the approval mode recorded; two threads with a cross-thread poison control; the nonce channel closed and per-run analyzer records committed; timeout/kill/partial-stdout and the context cap measured; approval-policy, hook-trust, registration-sources and options rows; header and labels fixed. Re-verify after (cheap once the instrument is sound). |
| F5 | **`docs/security.md` §4 no longer says the in-session refusal holds "whatever your permissions say"** — measured 2026-09-08 from a session's Bash tool: `brigade team revoke-member` (no arguments) refuses, exit 2; `env -u CLAUDE_PID brigade team revoke-member` passes the refusal and reaches the adapter, exit 3 on the empty stdin — because `config.InSession` is the `CLAUDE_PID` check (environ.go:72-74). The paragraph now says so and names the gate that holds: Claude Code's permission rules (the stripped command text matches no `Bash(brigade:*)` rule, so the default mode raises the ask dialog; `bypassPermissions` has nothing between the model and the verb, as for every command in that mode). Doc only — no detection change: process ancestry is heuristic and defeatable, and the exposure exists only in bypass mode | done | Opus | this commit — found during the re-review of the Codex brief (d7088fc), whose P0 runs the same "unset" arm on Codex and should carry this Claude measurement as its control; Rjae's ruling 2026-09-08: reword, do not build detection |

