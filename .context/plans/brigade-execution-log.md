# Brigade execution log

The single source of truth for **where the work stands**. The plan says what to build; this file says what is done,
what is next, and which model tier each remaining task should run on. Update it in the same commit as the work.
**Exception (Rjae, 2026-09-09): a pull request labelled `maintenance` — the agentic workflows' own PRs,
dependency bumps included — carries no row.**

Ticket 15 · branch `master` · merges only, never rebase · commit messages `15: <Imperative summary>` ·
`make push message="15: …"` for every commit after the first.

**0.4.1 is released (2026-09-08, after 0.3.0 and 0.4.0 the same day); 0.2.0 was released 2026-09-07 and 0.1.0 on 2026-09-06.** The full record through the release — every Status row, DONE section, plan
correction and ruling — was moved verbatim to `.context/plans/brigade-execution-log-archive.md` on 2026-09-06;
a reference of the form `see "P5-10 DONE — 0.1.0 RELEASED"` resolves there; on 2026-09-08, after 0.4.1, the twenty-one
rows that carried the 0.2.0–0.4.1 work (P5-16, P5-19, the eighteen P7 rows P7-1..P7-16 with 6b, 6c and 11b, F4 — every one `done` or `won't do`) followed, verbatim,
below the archive's marker dated 2026-09-08; on 2026-09-09 the Codex workstream's two rows (P8-1 `won't do`, F5 `done`) followed,
below the marker dated 2026-09-09. The complete post-release work list
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
   proven high value to be worked, and the twenty-one finished rows moved to the archive. On 2026-09-09 Rjae closed the
   Codex workstream (P8-1, `won't do`; F5 `done`; both in the archive): Codex has no supported way to wake a live
   conversation, so a Codex member could only be a mailbox, never a peer. OpenAI models are used by keeping Claude Code as
   the session and adding OpenAI's `openai/codex-plugin-cc`; Brigade needs no code for that. The brief
   (`.context/plans/codex-team-participation.md`) and its P0 evidence (`docs/experiments/codex-participation.md`) stay as
   the record. On 2026-09-10 Rjae chartered the agentic-workflows brief (`.context/plans/agentic-workflows.md`, v4) and
   rows P9-1..P9-7 were opened and done the same day, in one commit. **There are no live rows. The next substantial
   work is a second adapter**, which Rjae will chart as its own brief; nothing is to be started on it until then.

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

Legend: `done` · `todo` · `blocked (<reason>)` · `wip` · `won't do (<date>)`. Every row through the 0.4.1 release and the Codex workstream is in the archive; the seven P9 rows below were opened and closed together on 2026-09-10, and no other rows are live (see Start here, step 3).

| ID | Task (plan §, named in the row) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| P9-1 | GitHub configuration: verify the two repository secrets (both already set, 2026-09-10) and **note their renewal dates**, set the squash-merge title source (the only setting that actually changes), the three labels, the outside-collaborator UI gate, the `master` ruleset (agentic-workflows brief §4.10 (1)–(7)) | done | Opus | `gh secret list` and the `gh api` read-backs recorded in `docs/experiments/agentic-workflows-first-runs.md` (ruleset `master-pr-checks`, id 22773942; squash title `PR_TITLE`, body `PR_BODY`; the three labels); the outside-collaborator approval was already `all_external_contributors`, so no UI step was needed; renewal dates recorded there (PAT none, OAuth token 2027-09-10); §5 (c) answered from `thinktech-api` in the same file |
| P9-2 | **Everything in the brief's §4, in ONE commit** (§4.10 (8) — no staging, no burn-in): the **eight workflows** `claude.yml` (two jobs, `agent` → `open-pr`), `review-pull-request.yml` (including the `gh pr comment` on a guard trip), `auto-merge.yml`, `_create-claude-issue.yml`, `update-documentation.yml` (`auto_merge: true`), `review-repository.yml` (monthly cron live), `release-notes.yml` and `update-dependencies.yml`; plus `scripts/ci/pr-guard.sh`, the three `.claude/agents/*.md`, and **every record line** — `scripts/ci/README.md`'s eight workflow rows, its one script row, its two configuration rows and its opening-sentence counts, `CLAUDE.md`'s paragraph, and this log's exemption sentence; resolve the `claude-code-action` SHA pin (7.7) | done | Opus | this commit — the eight workflows, `scripts/ci/pr-guard.sh` (mode 100755), `.claude/agents/{reviewer,documenter,developer}.md` and every record line above; `make plugin-check lint test` green; `pr-guard.sh` through both shellchecks (local **and** the pinned 0.9.0 container) |
| P9-3 | The loop's provisional budgets (`timeout-minutes`, `--max-turns`) pinned in the workflow files; records (a)–(d), (i) and (j) — and the re-pin from ten real runs — follow on the first runs | done | Opus | this commit — the loop is live and the provisional budgets are pinned in it; first runs to be recorded in `docs/experiments/agentic-workflows-first-runs.md` as they happen. Adds no file, so no `scripts/ci/README.md` row |
| P9-4 | `update-dependencies.yml` and `.claude/agents/developer.md`'s dependency-update mode; records (e) and (f) — rule (c) green through the published-release arm, the plugin serving the last release until `make release` — follow on the first run | done | Opus | this commit — `update-dependencies.yml` and `.claude/agents/developer.md`'s dependency-update mode; first runs to be recorded in `docs/experiments/agentic-workflows-first-runs.md` as they happen. Adds no file |
| P9-5 | **Remedy (a) of the brief's §4.7**: BOTH edits to `release.yml` — the widened top-level `permissions:` and the `notes` job that calls `release-notes.yml` — plus record (g) | done | Opus | this commit — `release.yml`'s `permissions:` widened to `contents`/`issues`/`id-token` and the `notes` job added, passing exactly one named secret; first runs to be recorded in `docs/experiments/agentic-workflows-first-runs.md` as they happen |
| P9-6 | `update-documentation.yml` (`auto_merge: true`) and `.claude/agents/documenter.md`; record (h), both halves, follows on the first run | done | Opus | this commit — `update-documentation.yml` (`auto_merge: true`) and `.claude/agents/documenter.md`; first runs to be recorded in `docs/experiments/agentic-workflows-first-runs.md` as they happen |
| P9-7 | **The seeded memory marker** `.context/plans/agent-memory/review-repository.md` (`Date: (unseeded)`, `Commit: (none)`, `Scope: whole tree`) and the first `review-repository` pass | done | Opus | this commit — the seeded marker and `review-repository.yml` (monthly cron live, no `auto-merge` label); first runs to be recorded in `docs/experiments/agentic-workflows-first-runs.md` as they happen |
