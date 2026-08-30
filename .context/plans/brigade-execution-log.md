# Brigade execution log

The single source of truth for **where the work stands**. The plan says what to build; this file says what is done,
what is next, and which model tier each remaining task should run on. Update it in the same commit as the work.

Ticket 15 · branch `master` · merges only, never rebase · commit messages `15: <Imperative summary>` ·
`make push message="15: …"` for every commit after the first.

## Start here (for a fresh session, a resumed session, or another developer)

1. Read `.context/plans/claude-code-team-messaging-implementation-plan.md` — section 2 (decisions, all marked
   **Decided 2026-08-30** are settled), section 8 (phases and tasks), section 13 (commit plan).
   `.context/plans/claude-code-team-messaging-logical-plan.md` is the conceptual design behind it.
2. Read `docs/research/README.md` for what the eleven digests contain and `docs/research/decisions-2026-08-30.md`
   for the decision brief (it overrides the plan where they disagree).
3. Find the first row below with status `todo`, run it at the model tier in its column, then update its row.
4. Toolchain check before Phase 1: Go 1.27.0, Docker running, `npx --yes supabase@2.116.0 --version`.
   Not installed on the original machine: `shellcheck` (P1-8 needs it: `brew install shellcheck`), `psql`
   (not required — `make advisor-lints` runs psql inside the database container).

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

## Status

Legend: `done` · `todo` · `blocked (<reason>)` · `wip`.

| ID | Task (plan §8) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| — | First commit: scaffold, both plans, first research round | done | Fable | `6386046 15: Add implementation plan and repo conventions` |
| P0-0 | Preserve the first research round under `docs/research/` | done | Fable | in `6386046` |
| — | Plan decisions D3/D6/D18/D20/D22/D31/D32/D33/D34/D35 + all-Go, CLI-only rewrite | done | Fable | this commit |
| P0-2 | Preserve the second research round under `docs/research/` | done | Fable | this commit |
| P0-1 | Injection corpus (`scripts/injection-corpus/`, `expected.json`) | todo | Fable | blocks E0-3 |
| E0-1 | Local stack + `brigade` schema, ported live checks | todo | Fable | needs Docker; SQL is Fable-tier |
| E0-2 | Broadcast-from-DB with the Go Phoenix client (a-d, g settled) | todo | Opus | remaining items are mechanical soaks |
| E0-3 | Inbound framing variants A/C (and B fallback) + injection corpus | todo | Fable | decides D19; security-critical |
| E0-4 | Idle-wake automation | todo | Opus | |
| E0-5 | Detached watcher lifecycle, `/clear`, SessionEnd budget | todo | Opus | scripted observation |
| E0-6 | Token refresh coexistence + flock (core settled) | todo | Opus | |
| E0-7 | Two sessions, two profiles (option delivery settled) | todo | Opus | |
| E0-8 | CLI-only mechanics: bootstrap timing, interactive ask rule, sandbox | todo | Opus | (b) is interactive; needs the user |
| E0-9 | `crossSessionInbound` hold/refuse interaction | todo | Opus | |
| E0-10 | Hosted checks (optional, needs the hosted project) | blocked (D32: after the proof) | Opus | |
| P1-1 | Go module scaffold, Makefile, lint, CI, plugin pins | todo | Opus | first code commit |
| P1-2 | `internal/protocol` (types, errors, NDJSON, sanitiser, schema) | todo | Fable | protocol + sanitiser |
| P1-3 | `internal/adapterkit` (stdin, XDG, atomic writes, flock, redaction) | todo | Fable | redaction is security-critical |
| P1-4 | `docs/protocol-v1.md` + adapter-authors skeleton | todo | Fable | user review gate |
| P1-5 | `cmd/brigade-adapter-fs` + mutants | todo | Opus | |
| P1-6 | `internal/conformance` + `cmd/brigade-conformance` | todo | Fable | suite design |
| P1-7 | `docs/adapter-authors.md` complete | todo | Opus | |
| P1-8 | `plugin/bin/brigade` bootstrap + plugin checks | todo | Opus | needs `shellcheck` |
| P2-1..P2-5 | Supabase schema, RPCs, realtime/housekeeping, pgTAP, advisor lints | todo | Fable | SQL and RLS |
| P2-6..P2-12 | Go Supabase client, profile/team/session/message commands, watch, integration, release rehearsal | todo | Fable (P2-6/P2-7/P2-10) · Opus (P2-8/P2-9/P2-11/P2-12) | credentials and watch are Fable-tier |
| P3-1 | Plugin manifests, marketplace, skills | todo | Opus | |
| P3-2 | `internal/harness` library (frame, socket-post, policy, pipeline) | todo | Fable | security path |
| P3-3..P3-5 | `brigade` session commands, hooks, watcher (+ sink mode) | todo | Fable | injection path |
| P3-6..P3-8 | Bootstrap wiring, headless smoke, interactive checks | todo | Opus | |
| P4-1..P4-6 | Vertical proof, headless/idle-wake runs, crash+resume, interactive checklist, results | todo | Fable (P4-2/P4-5/P4-6) · Opus (P4-1/P4-3/P4-4) | criterion 8 is Fable-tier |
| P5-1..P5-11 | Hardening, admin, docs, keychain, soak, release, `hold` policy | todo | mixed | after the proof |

## Open questions carried from research (settle during the phase that needs them)

- `${CLAUDE_PLUGIN_ROOT}` substitution in a hook's `command` field vs `args` (E0-5 (g), documented but re-record).
- First-use download time vs the SessionStart timeout; whether the background-download bootstrap variant is needed (E0-8 (a)).
- Whether `not_found` should move off SQLSTATE P0002 (HTTP 500 at the gateway) to a `PT4xx` code (P2-2).
- pg_cron availability on the minimal local stack (E0-1 (h)).
- Loopback from a sandboxed Bash tool via `sandbox.network.allowedDomains` (E0-8 (c)).
- `GORELEASER_CURRENT_TAG` with `--skip=validate` on a not-yet-existing tag (P2-12 rehearsal).
- Whether a fresh `CLAUDE_CONFIG_DIR` needs its own `claude login` (E0-7, matters for P5-10).

## Notes for a hand-off

- Everything needed to continue is in the repository: the two plans, this log, `docs/research/`. No session
  context, chat history, or per-user memory is required.
- The user's Claude config dir is non-default (`CLAUDE_CONFIG_DIR=~/.claude-ifthen` on the original machine);
  never hardcode `~/.claude` in code, tests or docs.
- Three probe messages were posted into the planning session's own inbox socket on 2026-08-30 to validate the
  wire protocol (plain, and wrapped with `from-name`); Appendix A.2 records what came back.

## Session journal

- 2026-08-30 ~15:40: a second interactive session (`15-brigade-0830`, same working tree) started and requested
  hand-off of implementation. The planning session replied with the state summary, is not mid-edit on anything,
  and stays hands-off unless Rjae redirects it. Exactly one session drives at a time; both sessions share this
  checkout, so: `git pull` before starting a task, commit + push at every task boundary, never two sessions
  editing concurrently. Keyboard-dependent observations (E0-8 (b) ask-rule dialog, E0-3 (b) preview rendering,
  E0-9 native hold/refuse) need Rjae at the keyboard of whichever session runs them.
- 2026-08-30 ~16:05: Rjae designated the other developer's session `15-implement-brigade-0830` as the
  implementation driver (her own Fable 5 budget; reached across config dirs via a registry-copy bridge).
  The earlier session `15-brigade-0830` was closed without starting anything. The planning session
  (`15-create-team-session-messaging`) is hands-off from here: it edits nothing, remains open as a reference,
  and wrote the cold-start hand-off to `.ignored/handoff-15-to-implement-0830.md`. Driver rules restated:
  `git pull` before each task, `make push message="15: …"` at every task boundary, one driver at a time.
