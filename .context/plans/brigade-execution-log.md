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
   proven high value to be worked, and the twenty-one finished rows moved to the archive. **The one live row is P8-1**,
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
| P8-1 | **Codex participation — P0, the capability experiment** (§3 of `.context/plans/codex-team-participation.md`, Frank's brief; reviewed 2026-09-08 by `15-0908` with three lenses plus critics, findings sent to `frank-brigade-main` as Brigade message `c04031ec`): a fixture Codex plugin under `scripts/experiments/codex-participation/` and the report `docs/experiments/codex-participation.md`, run on the real Codex client, answering — with the review's additions — (1) whether a host-set per-conversation marker reaches a shell-tool child that the model cannot set (the `CLAUDE_PID` analog: `config.InSession` keys on it, so without one the `hold` policy's human-only release, `revoke-member` and `transfer` cannot be protected on Codex); (2) a stable native thread id in hook stdin and two concurrent threads without collision, proven the E0-7 way (a per-thread nonce written only to the private map, `whoami --json` from each thread's shell returns its own, a poison control ignored); (3) whether hook stdout reaches the model, what Codex wraps around it verbatim, and its output cap; (4) the hook timeout — default, per-hook configurability, kill signal, fate of stdout already written; (5) a null control in its own fresh session (nothing sent, fixed window, instrument named) beside the idle-wake attempt, labelled as such. Pass predicate: a per-run split nonce in the receiving model's own text, never the frame echoed back in the host's own output (Claude's `origin.body`); sink, mock and worker-only arms score FAIL or are excluded; a positive control and mutations that flip each verdict. The report is re-checked by an adversarial verifier before any P2 row is opened | todo | Opus (run + record) + Fable verifier | added 2026-09-08 from `.context/plans/codex-team-participation.md` §3 (P0; reviewed at 3338225, revised at d7088fc); owner: Frank's team (the brief's author is a Codex session; Frank assigns the runner); Rjae's ruling 2026-09-08: doable for outcome 1 (boundary receipt) — the brief's P2–P5 become rows only from P0's capability table, recorded in `docs/experiments/codex-participation.md` (this log stays the single status authority; the brief's revised §7 agrees); identity FAIL with no valid binding handle is a hard stop after P1; outcome 2 (idle wake) is not expected through hooks |
