# 13. Commit plan

Record.

## Corrections recorded in the execution log

None recorded.

<!-- verbatim from the one-file plan -->
## 13. Commit plan

Rules (from `~/.claude/CLAUDE.md` and Appendix A): work lands on `master` in the existing repository (remote `git@github.com:appshapes/brigade.git`, private); every commit after the first goes through `make push message="15: <Imperative summary>"`, which runs `typecheck`, `pull` (plain merge, never rebase), `build`, `test`, `git add`, `commit`, `push`; no rebase, no force-push, no `--amend` after pushing; stop on a merge conflict and hand it back. Because `make test` never needs Docker (D29), the chain works on any machine. Commits that touch `supabase/` or the adapter are preceded by a local `make test-all` against the running stack (CLAUDE.md rule). Every commit that changes a wire shape touches `internal/protocol`, `docs/protocol-v1.md`, `docs/protocol-v1.schema.json`, the conformance suite and both adapters together, so the suite never disagrees with the spec at any commit. Release commits are made only by `make release` (7.7).

The first commit exists: `6386046 15: Add implementation plan and repo conventions` on `master`, pushed with `git push -u origin master`, containing the seeded conventions, the logical plan, the first revision of this plan and the first research round under `docs/research/` (P0-0). The upstream therefore exists and `make push` works from now on (P1-1 also adds `git config push.autoSetupRemote true` to `make setup` so a fresh clone on another machine does not hit the wall the first commit hit). The seeded Makefile's `typecheck`, `build` and `test` targets are still `# TBD` no-ops, so the commit chain runs end to end today; P1-1 fills them in.

The next commit is `15: Record plan decisions`: this revision of the plan, `docs/research/decisions-2026-08-30.md` (the decision brief), the four second-round digests with their lab sources and evidence, and the updated `docs/research/README.md` (P0-2). It is made through `make push message="15: Record plan decisions"`. No code is committed before the user has reviewed the decision table of this revision; the decision gates table in section 2 lists what that review settles and what is re-confirmed later (D19/D21/D23 at Phase 0 exit; D18/D20/D32 at P4-6).

| Phase | Commits (in order; one or more per task, each leaving CI green) |
| --- | --- |
| 0 | `15: Record plan decisions` (this revision + P0-2) · `15: Add injection corpus` (P0-1, `scripts/injection-corpus/`, committed before E0-3 consumes it) · `15: Record Phase 0 experiment results` (`docs/experiments/E0-*.md`; the decision table updated in the same commit; each experiment's driver script promoted from `.ignored/exp/<id>/` to `scripts/experiments/E0-<n>/` when it closes; the draft migrations from E0-1 stay in `supabase/migrations/` for P2-1/P2-2 to finish) |
| 1 | `15: Scaffold Go module, Makefile, lint and CI` · `15: Add protocol types, errors, NDJSON and sanitiser` · `15: Add adapter-kit shared plumbing` · `15: Write protocol v1 specification` · `15: Add filesystem adapter for tests` · `15: Add protocol conformance suite` · `15: Document adapter authoring` · `15: Add plugin bootstrap and plugin checks` |
| 2 | `15: Add Supabase local config and brigade schema with RLS` · `15: Add Supabase RPCs and stamping triggers` · `15: Add realtime broadcast trigger and housekeeping` · `15: Add pgTAP suite and advisor lints to CI` · `15: Add Supabase client and adapter profile commands` · `15: Add Supabase adapter team commands` · `15: Add Supabase adapter session commands` · `15: Add Supabase adapter message commands` · `15: Add Supabase adapter watch loop` · `15: Add adapter integration suite` · `15: Rehearse release flow and run conformance in CI` |
| 3 | `15: Add plugin manifests and skills` · `15: Add harness library` · `15: Add brigade session commands` · `15: Add lifecycle hooks` · `15: Add inbound watcher with sink mode` · `15: Wire plugin bootstrap and dev pointer` · `15: Add headless harness smoke test` · `15: Record interactive plugin checks` |
| 4 | `15: Add vertical proof script` · `15: Add headless and idle-wake proof runs` · `15: Record vertical proof results` |
| 5 | `15: Add hosted backend install and setup docs` · `15: Add team secret rotation, member revocation and transfer` · `15: Add anonymous cleanup and verify retention` · `15: Record send confirmation checks` · `15: Add injected ring and inbox recent` · `15: Add OS keychain secret store` · `15: Add security and user documentation` · `15: Add soak results` · `15: Release v0.1.0` (made by `make release version=0.1.0`) · `15: Add hold policy and terminal release` (after the release, per 11.2 (1): 0.1.0 ships without `hold`, as the 6.1 option text says; P5-9 moves before the release only if the Phase 4 corpus run leaves an open config-edit or exfiltration finding) |

---
