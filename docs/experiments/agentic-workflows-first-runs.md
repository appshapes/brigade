# Agentic workflows — configuration record and first runs

The evidence file that `.context/plans/agentic-workflows.md` §5 asks for: one entry per configuration step and
per path of the loop the first time it fires for real. Everything below is measured (`gh`, dated), never
inferred; where a value is a policy choice it says so.

## Configuration (P9-1) — 2026-09-10, applied with `gh` by session `rjae-fable-general-purpose-session`

| Step (§4.10) | Result |
| --- | --- |
| (1) Repository secrets | `gh secret list`: exactly `GH_ACTIONS_TOKEN` (set 12:13 UTC) and `CLAUDE_CODE_OAUTH_TOKEN` (set 12:25 UTC), both created by Rjae by hand the same day. No repository variables; no `BRIGADE_BOT_*`. |
| (2) Claude GitHub App | Installed on the `appshapes` org for all repositories (`app_slug` `claude`, `app_id` 1236702; measured 2026-09-09). Nothing to do. |
| (3) Renewal dates | Rjae, 2026-09-10: the classic PAT has **no expiry**; the OAuth token is the one-year kind, so it lapses on **2027-09-10** and every workflow agent fails at authentication that day — renew with `claude setup-token` under `reaston@ifthen.com` and `gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo appshapes/brigade` (paste at the prompt) before then; a calendar reminder for 2027-08-10 is Rjae's. The PAT needs renewal only if revoked or rotated. |
| (4) Repository settings | `PATCH repos/appshapes/brigade`: `squash_merge_commit_title` `COMMIT_OR_PR_TITLE` → **`PR_TITLE`** (the `15: ` rule depends on it); `squash_merge_commit_message` `COMMIT_MESSAGES` → **`PR_BODY`** (the PR body, where the agent writes `Closes #N`, becomes the commit body — a deliberate choice). Already correct and left alone: `allow_auto_merge=true`, `allow_squash_merge=true`, `allow_rebase_merge=false`, `delete_branch_on_merge=true`. |
| (5) Outside-collaborator approval | The REST endpoint the brief asked to verify exists: `GET repos/appshapes/brigade/actions/permissions/fork-pr-contributor-approval` → `{"approval_policy":"all_external_contributors"}` — **already set** (at flip-public time), so no UI step was needed. |
| (6) Labels | Created: `claude` (`5319e7`), `auto-merge` (`0e8a16`), `maintenance` (`fbca04`). Three, not four — no `dependencies` label (no Dependabot). |
| (7) `master` ruleset | `POST repos/appshapes/brigade/rulesets` from `.ignored/master-pr-checks.json` → id **22773942**, `master-pr-checks`, `enforcement: active`. Read back: `bypass_actors` `[{actor_id: 5, actor_type: RepositoryRole, bypass_mode: always}]`, required contexts `fast`, `macos`, `reproducibility`, `supabase`, `strict_required_status_checks_policy: false`. `deploy-staging` deliberately absent. The pre-existing `protect-release-tags` ruleset is untouched. |

## §5 (c) — does `gh pr merge --auto` wait for the required checks when the merging PAT is a bypass actor?

Answered first from `GoThinkTech/thinktech-api`, which runs the same loop with the same kind of classic PAT
(the brief's own instruction): its `master` has branch protection with required checks `check` and `test` and
`enforce_admins: false` — i.e. the merging identity can bypass — and its last three `auto-merge`-labelled PRs
merged **after** the final CI check completed, not before:

| PR | Last CI check completed | Merged (by Rjae's PAT) | Gap |
| --- | --- | --- | --- |
| #369 Update repository documentation | 2026-09-01T09:25:43Z | 2026-09-01T09:26:10Z | +27 s |
| #367 Update Dependencies | 2026-09-01T09:25:43Z | 2026-09-01T09:25:54Z | +11 s |
| #365 Review repository | 2026-09-01T04:37:11Z | 2026-09-01T04:37:15Z | +4 s |

So `--auto` waits for the required checks even though the actor could bypass them. Brigade's `auto-merge.yml`
keeps its deterministic contingency (`gh pr checks --required` before any plain merge) but it is not expected
to fire. Confirmation on this repository is recorded under (c) below the first time an `auto-merge`-labelled
PR merges.

## First runs (§5 (a)–(j)) — recorded as each path fires

### 2026-09-10 — first live run: `update-documentation` dispatched by hand at 13:41:32 UTC

**(a) issue → agent → PR, App identity.** `gh workflow run update-documentation.yml` → run 34484227312 (`update
documentation`, success) → issue #2 "Refresh the structural documentation", labels `claude` + `maintenance` +
`auto-merge`, authored by Rjae's PAT → the `claude` workflow fired on the `labeled` event (run 34484257940; job
`agent` 13:42:01–13:49:21, job `open-pr` 13:49:34–13:49:42) → branch `claude/issue-2-20260910-1342` → PR #3 titled
**`15: Refresh the structural documentation`**, labels `auto-merge` + `maintenance`, body `Closes #2`, 5 lines changed
in `README.md` and `docs/adapter-authors.md`. PASS. Two cosmetic observations: the three label events queue three
`claude` runs in the issue's concurrency group and GitHub cancels the two pending ones (runs 34484258780,
34484259270 show `cancelled`; only the `claude`-label run was ever going to pass the `if:`); and Blacksmith's
"codesmith" App appends its own footer to the PR body.

**(b) review → fix, first cycle.** `review pr` run 34485085258 (actor Rjae, success): `claude[bot]` submitted
`CHANGES_REQUESTED` at 13:51:38 — "two of the three refreshed facts check out; the third replaces a stale statement
with a false one" — with the fixer trailer. The `claude` workflow fired on `pull_request_review` (run
34485296081): the fix gate counted 1 ≤ 3 and proceeded, the fixer pushed `b82edd7` ("15: Confine the fs-only
measurement note to the argv list") and "Verify the fix was pushed" passed. PASS for the gate and the push.

**Two findings from the fix push, both fixed the same day:**

1. **Approval policy vs. the loop's own pushes.** GitHub attributes the fixer's push — made with the Claude App
   token the action mints via OIDC — to `github-actions[bot]` (commit `b82edd7`: author and committer login
   `github-actions[bot]`, name `claude[bot]`). Under `approval_policy: all_external_contributors` the resulting
   `review pr` and `ci` runs (34485452913, 34485452904) parked at `action_required`; both were approved by hand
   (`POST …/actions/runs/{id}/approve`) and the policy was set to **`first_time_contributors`** — GitHub's default
   for public repositories, which still gates every first-time outside human. Whether the bot is exempt under it
   is answered by the next fixer push; if not, `first_time_contributors_new_to_github` is the remaining step.
2. **`allowed_bots` on the reviewer.** The approved re-review run 34485452913 (actor `github-actions[bot]`) was
   refused by the action's own trigger check — every inner step `skipped`, the step failed, no review at head
   `b82edd7`. Fix: `review-pull-request.yml` `allowed_bots: "claude,github-actions"` (the hub and release-notes
   keep `"claude"`), committed as the correction below; PR #3 closed and reopened to re-run the reviewer.

**(c) `--auto` waits** — not yet exercised here (no approval has landed); the thinktech-api observation above stands.
**(h)** — pending PR #3's merge.
