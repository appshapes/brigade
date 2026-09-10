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

_None yet. Each entry: date, run id(s), the pass predicate from §5, what was observed, and any deviation._
