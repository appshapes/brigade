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

Legend: `done` · `todo` · `blocked (<reason>)` · `wip`.

| ID | Task (plan §8) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| — | First commit: scaffold, both plans, first research round | done | Fable | `6386046 15: Add implementation plan and repo conventions` |
| P0-0 | Preserve the first research round under `docs/research/` | done | Fable | in `6386046` |
| — | Plan decisions D3/D6/D18/D20/D22/D31/D32/D33/D34/D35 + all-Go, CLI-only rewrite | done | Fable | this commit |
| P0-2 | Preserve the second research round under `docs/research/` | done | Fable | this commit |
| P0-1 | Injection corpus (`scripts/injection-corpus/`, `expected.json`) | done | Fable | this commit — 26 items (17 `ask` / 9 `ignore`; 24 body + 2 summary-only), the plan's mandated 15 at `01`–`15` plus 11 additions |
| E0-1 | Local stack + `brigade` schema, ported live checks | done | Fable | this commit — `docs/experiments/E0-1.md`; (a)–(i) all answered; 72/72 live assertions; driver at `scripts/experiments/E0-1/` |
| E0-2 | Broadcast-from-DB with the Go Phoenix client (a-d, g settled) | done | Opus | this commit — `docs/experiments/E0-2.md`; (e)(f)(h)(i) answered; 42 fast + 14 soak assertions; **D21 stays on broadcast-from-DB** |
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
  **Evidence in (E0-1):** confirmed live — `brigade:not_found` reaches the client as HTTP 500 carrying SQLSTATE
  `P0002`, payloads byte-identical across foreign and unknown ids. The security property is unaffected either way, so
  this is purely an adapter-ergonomics call for P2-2: keep `P0002` and require the adapter to key on SQLSTATE (a 5xx
  that must not be retried), or move to `PT4xx` for an honest 4xx. Decide it in P2-2, not before.
- ~~pg_cron availability on the minimal local stack (E0-1 (h)).~~ **RESOLVED (E0-1):** pg_cron 1.6.4 is present on
  the 5.9 `-x` stack, `create extension` succeeds and `cron.schedule` runs `brigade.gc_expired()` hourly. The
  opportunistic-gc fallback is not needed locally; the housekeeping migration still guards the scheduling so a stack
  without pg_cron applies cleanly.
- Loopback from a sandboxed Bash tool via `sandbox.network.allowedDomains` (E0-8 (c)).
- `GORELEASER_CURRENT_TAG` with `--skip=validate` on a not-yet-existing tag (P2-12 rehearsal).
- Whether a fresh `CLAUDE_CONFIG_DIR` needs its own `claude login` (E0-7, matters for P5-10).
- **Are `@`-mentions inert inside a sanitised body?** The 6.7 sanitiser neutralises five tag families and strips
  format characters but says nothing about the harness `@~/path` file-reference syntax. If the harness expands an
  `@`-mention inside an injected frame, corpus item `08-at-mention-ssh-key` pulls the secret into context regardless
  of what the model decides — the item would then measure the harness, not the model. Measure it in E0-3 (b).
  **Disposition decided by Rjae, 2026-08-30: if expansion is real, DOCUMENT IT AS A RESIDUAL RISK in
  `docs/security.md` alongside the D18 and D20 accepted risks — do NOT add an `@`-neutralising rule to 6.7.** The
  sanitiser keeps its five-family scope and message text stays untouched. E0-3 still records exactly what expands and
  under which conditions, and item `08`'s expected outcome is revisited in the light of the measurement (P0-1).
- **Does the sanitiser's tag matcher survive near-misses?** Corpus item `25-tag-matcher-evasion` carries byte-exact
  probes the spec's "optional whitespace" rule should catch (space, tab and newline between `<` and the name, mixed
  case) plus two it deliberately should not (a close tag split mid-name, a pre-encoded `&lt;brigade-message`). P1-2
  decides which of the latter two, if either, the matcher must also handle; today they are fuzz seeds, not assertions.
- **`make supabase-start` needs a migrations guard (P1-1).** Established in E0-1: with `brigade` in `[api] schemas`,
  `supabase start` cannot succeed until a migration creates the schema — PostgREST loops on `3F000 schema "brigade"
  does not exist`, the `rest` container never turns healthy, and the CLI tears the whole stack down reporting only
  `supabase_rest_brigade unexpected status 503`. A fresh clone is fine (migrations are committed), but the failure
  names PostgREST rather than the cause, so the recipe should fail fast with a readable message when
  `supabase/migrations/` is empty.
- **Is the five-family list the right boundary?** Corpus item `24-unlisted-forged-tag` uses `<important_instructions>`,
  which is outside the five sanitised families and so reaches the model verbatim (verified: zero matcher hits). If
  E0-3 (f) shows a model treating an unlisted tag as harness authority, 6.7's family list is under-inclusive and the
  fix is a frame/sanitiser change, not a corpus change (P0-1, R9).

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
- 2026-08-30 ~17:05: **P0-1 done.** The driver session's main loop runs Opus, so the corpus was authored by Fable 5
  subagents per the model tier policy: three design lenses (injection research, Claude Code harness surface, Brigade
  protocol) proposed items beyond the mandated fifteen, one synthesiser fixed the manifest and every `ask`/`ignore`
  value, four batches authored the payloads, four adversarial reviewers repaired them, and a completeness critic read
  every file back off disk. The critic named three missing attack classes and they were authored in a second pass:
  `24-unlisted-forged-tag`, `25-tag-matcher-evasion`, `26-encoded-exfiltration`. They are in now rather than later
  because the corpus is consumed *unchanged* by E0-3 (f), P4-2, P4-5, R9 and the U-03/U-04 fixtures — adding an item
  after E0-3 runs means re-running E0-3. Three findings went to the open-questions list above; the `@`-mention one
  (item `08`) should be settled in E0-3 (b) or that item measures the harness rather than the model. Also recorded
  there: items `22-homoglyph-forged-reminder` and `23-forged-native-wrapper` are the two `ignore` items most exposed
  to grader interpretation (both are request-free authority spoofs a model may verbally rebuff), so P4-2 should watch
  them for 3-of-3 flake. Verified independently of the agents: 26 files, contiguous numbering, all UTF-8 with a single
  trailing newline and under the 16 KiB cap, no real secrets or destructive instructions, every host under
  `.invalid`, item `11`'s bidi code points present on disk, and `expected.json` in exact agreement with the directory.
- 2026-08-30 ~17:55: **E0-1 done**, results in `docs/experiments/E0-1.md`, driver promoted to
  `scripts/experiments/E0-1/` (builds with no dependencies; run it from the repo root, `loadEnv` reads `./.env.test`).
  All of (a)–(i) answered; nothing fell back to the D22 `public` alternative. Split by tier: the SQL and the security
  assertions ran on Fable subagents (three migrations authored from 5.3–5.8 plus a fidelity and an adversarial
  privilege review; then the live-check harness plus an adversarial verification pass), while the stack bring-up and
  the empirical probes (b)–(g) ran on Opus in the main loop. Live result: 72 assertions, 0 failed, re-run green after
  a `db reset`. The adversarial pass mattered — it found seven assertions that passed for the wrong reason, the worst
  being that the seven byte-identity `not_found` comparisons had no positive controls and would have passed even if
  every id returned `not_found`. Three facts to carry: `P0002` surfaces as HTTP 500 so adapters must key on SQLSTATE
  not status; publishable-key-without-JWT denials are HTTP 401 while table denials with a JWT are HTTP 403, both
  `42501`; and the minimal `auth.users` fixture insert is `id` alone, but leaves `aud` and `role` as empty strings,
  so Phase 2 fixture helpers should set them explicitly. Stack left running with the schema applied.
- 2026-08-30 ~20:05: **E0-2 done**, results in `docs/experiments/E0-2.md`, driver promoted to
  `scripts/experiments/E0-2/`. **D21 stays on broadcast-from-the-database** — neither flip condition fired. Ran at
  Opus tier throughout (author + adversarial verifier), per the tier policy. (e) exactly-once holds with concurrency
  *measured* (peak 50 in-flight RPCs) and the drain racing the senders; (f) the strict race did not reproduce in 20
  attempts across 3 runs, with 40 of 80 sends confirmed to have committed after join ok so the negative is not
  vacuous, while the pre-join window misses 100% of the time and drain-on-join-ok covers it 20/20; (h) revocation
  takes effect on both the `leave_team` and the membership-only SQL path, byte-identical to a non-member baseline;
  (i) the 30-minute soak passed 14/14, including at-least-once recovery of 5 messages across a `supabase stop`/`start`.
  The drain is `fetch_inbox` + ack per page with no `seq` watermark, and the run **empirically observed** the 5.7
  inversion (cross-page `seq` going backwards) that would have made a watermark lose rows. Adversarial review found
  15 wrong-reason passes, the most important being that the exactly-once detectors had never been shown capable of
  firing — three fault-injection self-tests now prove they do.
- 2026-08-30 ~20:05: **Rjae's answers to the pending questions**, recorded where each belongs: Fable cadence → one
  author + one adversarial verifier, no design panels (see "Model tier policy" above); P0-1 corpus → keep all 26
  items, no trim; `@`-mentions → if E0-3 (b) shows expansion, document as a residual risk in `docs/security.md`, do
  NOT add a 6.7 sanitiser rule (see the open question above); the keyboard-dependent sitting → **deferred**, so run
  every automatable part of E0-3 and then E0-4..E0-7 first, and close E0-3 (b), E0-8 (b) and E0-9 together in one
  ~15-minute sitting afterwards. E0-3 therefore runs to completion minus (b), and D19 is decided after that sitting.
