# Brigade execution log

The single source of truth for **where the work stands**. The plan says what to build; this file says what is done,
what is next, and which model tier each remaining task should run on. Update it in the same commit as the work.

Ticket 15 · branch `master` · merges only, never rebase · commit messages `15: <Imperative summary>` ·
`make push message="15: …"` for every commit after the first.

**0.1.0 is released (2026-09-06).** The full record through the release — every Status row, DONE section, plan
correction and ruling — was moved verbatim to `.context/plans/brigade-execution-log-archive.md` on 2026-09-06;
a reference of the form `see "P5-10 DONE — 0.1.0 RELEASED"` resolves there. The complete post-release work list
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

Legend: `done` · `todo` · `blocked (<reason>)` · `wip`. Every row through the 0.1.0 release is in the archive.

| ID | Task (plan §8) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| P5-16 | **Fast follow after 0.1.0: distribution channels** — a Homebrew tap (goreleaser `homebrew_casks`, now possible on a public repository) and a Linux equivalent (goreleaser `nfpms` `.deb`/`.rpm`, or the same tap through Linuxbrew — decide in the brief) | todo — after 0.1.0 | Opus | added 2026-09-05 at Rjae's request; brief to write; the plugin bootstrap stays the primary path and must not be shadowed by a tap install (E0-8 (e) measured the shadow) |
| P5-19 | **Setup-document corrections from the 0.1.0 distribution proof** — nine sentences in `docs/setup.md` / `plugin/README.md` / the bootstrap's own line that do not match what a new user sees (listed in "P5-10 DONE — 0.1.0 RELEASED"): the first use is a foreground download on the documented terminal path; the registration line appears on a later prompt on a cold cache; the undocumented `9 userConfig options not yet set` line; `team create --name/--label` documented only for developers; the `whoami` example's adapter name; the administrator sent to an in-session command before any session exists; `claude plugin marketplace remove` undocumented and the plugin copy kept with an `.orphaned_at` marker; the publishable key's value in no document (by design — say so) | todo — after 0.1.0 (docs only; a patch release is not needed for text on master) | Opus | added 2026-09-06 by the driver of the release steps; brief to write; plain language, no retired words |
