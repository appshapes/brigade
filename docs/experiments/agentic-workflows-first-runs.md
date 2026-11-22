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
   **Answered 2026-09-16: not exempt.** The fixer pushes `029cd34` (PR #7, 2026-09-11), `fac1ec2` (PR #8,
   2026-09-11) and `3f333fb` (PR #8, 2026-09-13) were all held: the policy gates the *actor* of the pull request
   event, and the bot never earns a merged commit under its own login because every merge is a squash authored
   by the owner. The policy is now `first_time_contributors_new_to_github` at the repository and the organization
   (card 22, item 2; execution-log row P14-2); the next fixer push measures it. **Measured 2026-09-17, PR #22:**
   the fixer push `e849247` (login `github-actions[bot]`, name `claude[bot]`) was held again — `ci` 35233246307
   and `review pr` 35233246560 at `action_required` — so no value of this policy exempts the bot; the lever is
   elsewhere (P14-2).
2. **`allowed_bots` on the reviewer.** The approved re-review run 34485452913 (actor `github-actions[bot]`) was
   refused by the action's own trigger check — every inner step `skipped`, the step failed, no review at head
   `b82edd7`. Fix: `review-pull-request.yml` `allowed_bots: "claude,github-actions"` (the hub and release-notes
   keep `"claude"`), committed as the correction below; PR #3 closed and reopened to re-run the reviewer.

**(c) `--auto` waits — PROVEN on this repository.** After the reopen (14:18:32) a fresh `ci` run 34488196052 started
on head `b82edd7` at 14:18:33; the reviewer (run 34488196063, actor Rjae) submitted `APPROVED` at 14:20:28; the
`auto merge` run executed at 14:20:50 with CI still in progress — `gh pr merge --auto` armed — and the PR merged at
**14:27:06, 24 s after that CI run completed at 14:26:42**, merged by Rjae (the PAT, a ruleset bypass actor). So
`--auto` waits for the four required checks even though the merging identity could bypass them; the deterministic
contingency in `auto-merge.yml` did not fire. Half (iii) — an approval landing on an already-green PR, where the
`||` fallback (guarded by `gh pr checks --required`) runs — has not yet occurred and is recorded when it does.

**(h) documentation refresh, both halves — PASS.** Squash commit **`4a9d5a3`**, title `15: Refresh the structural
documentation (#3)` (GitHub appends the PR number; the `15: ` prefix rule holds), body = the PR body (`PR_BODY`), so
`Closes #2` closed issue #2 at 14:27:07. Observation for Rjae: Blacksmith's "codesmith" App appends an HTML footer
to every PR body, which `PR_BODY` now copies into the squash commit's message; either `squash_merge_commit_message`
goes to `BLANK` (losing `Closes #N` from the commit body — the issue still closes through the PR) or the codesmith
footer is switched off in Blacksmith. Cosmetic; no loop behaviour depends on it.

**Elapsed, dispatch to merge: 45 min 34 s** (13:41:32 → 14:27:06), of which ~20 min were the two stalls above
(manual run approval, the reviewer's actor refusal) that are now fixed; the loop's own work was ~7.5 min of agent,
~2.5 min of fixer, two ~2 min reviews and two ~8 min CI runs.

### 2026-09-10 — first release through the loop: v0.5.0 (`make release version=0.5.0`, 22:21 UTC)

**(g) release notes — FAIL on the designed path, PASS on the fixed one.** `release.yml` run **34537045037**: the
`goreleaser` job passed end to end (the pin guard, `make cross` reproducing `plugin/bin/checksums.txt`, goreleaser's
draft, `release-verify.sh`, and the publish with `--latest` for a final version — the four binaries and
`checksums.txt` were public at 22:22:39). The `notes` job — the reusable `release-notes.yml` called through
`workflow_call` — died in `claude-code-action`'s first step with **`Action failed with error: Unsupported event
type: push`**: a called job runs under the caller's event, a tag `push`, and the action gates on the event type.
Neither remedy in the brief's §4.7 could have worked as written: the `release: published` trigger never fires
because the publish uses `github.token` (job-token writes raise no workflow runs), and the call inherits `push`.
**Fix** (this commit): `release.yml` no longer calls the workflow; its last step is
`gh workflow run release-notes.yml -f tag="${GITHUB_REF_NAME}"` — `workflow_dispatch` is one of the two events the
job token *may* raise — under `actions: write`, and the caller's `issues: write` / `id-token: write` ceiling is
gone (those live in `release-notes.yml`, which grants them itself). **Proof**: the same dispatch run by hand for
v0.5.0, run **34537527402**, succeeded in one pass — the notes are on
<https://github.com/appshapes/brigade/releases/tag/v0.5.0> and the announcement is issue **#4** (`0.5.0 is out — run
/brigade:update`, no `claude` label, as the prompt requires). The pre-release arm of (g) — `--latest` withheld on a
`-rc` tag — is still unmeasured; nothing here changed it.

**A `fast` flake, not a regression.** The `ci` run on the release commit, **34537042672**, failed `fast` in
`internal/conformance` `TestRunRefusesARunThatOutlivedTheFixtureLease` (`fixture_lease_test.go:276`): the test
registers a fixture on a real 1 s lease and sleeps 2 s, and on the runner the launcher aborted for another reason
before the "outlived its lease" path, so the three expected phrases were absent. The same tree passed `make test`
locally three times in a row and `go test -count=3` on the file; the re-run of the failed job (`gh run rerun
--failed`) went green, and `reproducibility` — the checksums against the now-published release — passed with it.
Recorded in `.context/plans/v0.1.0-followups.md` as an obligation: a real-clock test on a shared runner.

**(g), second pass — v0.5.1 (23:53 UTC): the dispatch path fires; the actor gate stops it.** `release.yml` run
**34539771452** published v0.5.1 and its new last step raised release-notes.yml run **34539842793** as a
`workflow_dispatch` — the event problem is gone — and the action refused that run at once: *"Workflow initiated by
non-human actor: github-actions (type: Bot). Add bot to allowed_bots list"*. The dispatch's actor is the job token,
and `release-notes.yml` allowed only `claude`, the same gap `review-pull-request.yml` closed on the loop's first
run (`b3538a0`). **Fix** (this commit): `allowed_bots: "claude,github-actions"`. The v0.5.1 notes were written by a
hand dispatch meanwhile (a human actor passes the gate as it stood). The next release is the first that will run the
whole path unattended.

**P9-8, the release-notes gate — two runs on v0.5.1 (23:13 and 23:24 UTC).** Run **34541118273**, the first with
the gate: the writer drafted, the lint ran, the reviewer ran, and the verdict step failed because no `review.md`
existed — and no `notes.md` either. The action's result for each agent said `permission_denials_count: 2`: it
honours every `Bash(<prefix>:*)` rule in `settings` and none of the `Write(//tmp/…)` rules, so both agents were
refused the one write they were told to make and stopped. **The gate held**: nothing was published; the page kept
the earlier hand-written notes. Fix (`64f5b2a`): the agents write with `tee <path> <<'EOF'` from the Bash tool
under a `Bash(tee <path>:*)` rule, each agent step's execution log is copied into the artifact, and the verdict
parser takes `APPROVED`/`CHANGES_REQUESTED` anywhere on the first line. Run **34541572413**, the first complete
pass: draft → lint clean → **reviewer: CHANGES_REQUESTED** — the lead said "a patch release with one fix" while
`CHANGELOG.md`'s `## [0.5.1]` has two `### Fixed` entries, "undercounts and distorts the changelog" — → fix →
lint clean → **APPROVED** → published → the announcement step found issue #5 and opened no second one. Four agent
sessions, about eleven minutes end to end. The published notes name `/brigade:update`, `brigade sessions` and the
five assets, and nothing that does not exist.

**P9-8 on v0.5.2 — the first unattended dispatch (09:15 UTC) and its re-run (09:31 UTC).** `release.yml` run
**34583228799** published v0.5.2 and dispatched release-notes.yml as run **34583310273** — actor `github-actions`,
now allowed — which drafted, passed the lint, and then **the reviewer step failed**: the reviewer spent 28 turns at
`xhigh` reading `internal/harness/watch/reopen.go` against the draft's claims, matching every asset digest to
`checksums.txt`, and found one real inaccuracy ("the plugin ships the binary for your platform" — it does not; the
bootstrap downloads it) — and `claude-code-action` fails a step whose agent exceeds `--max-turns` (20) even after
it has written its verdict. Nothing was published; the page kept goreleaser's default until the fix. Fix
(`d684bca`): 50 turns for all four agent steps, `continue-on-error` on them so the lint and the verdict file decide
rather than the action's exit status (a missing review counts as `CHANGES_REQUESTED` and the fix cycle runs), the
keep-log steps copy only their own agent's log, and the reviewer's read-only verbs (`git diff`, `git ls-files`,
`head`, `wc`, `file`, `shasum`) are allowed. The re-dispatch, run **34583822028**: draft → lint clean → **APPROVED in
round one** → published → issue **#6**. The next release is the one to watch for a fully unattended pass.

### 2026-11-22 — the reviewer's login measured again, and compared whole (work item 397)

**The measurement §5 (a) asks for, over every pull request.** `gh api repos/appshapes/brigade/pulls/<n>/reviews`
for each of the repository's pull requests: 85 reviews, 79 by `claude[bot]` (account type `Bot`) and 6 by the
owner. The reviewer agent has reviewed under no other login.

**The change.** Until `83e001d6` the workflows took a review for the reviewer agent's when its login passed
`startsWith(…, 'claude')`, in thirteen places of `auto-merge.yml`, `claude.yml` and `review-pull-request.yml`.
A review from any account whose login begins with `claude` would have passed too. Each place now compares the
whole login `claude[bot]`, and `scripts/ci/loop_login_test.go` holds every workflow to it. Whether GitHub hands
such a stranger's review to these workflows was not tried: it needs a second account.

**The writer half, by dispatch: no pull request, and a closing comment that is not true.** `gh workflow run
update-documentation.yml` at 10:36:36 UTC → run 45013281133 (success) → issue #118 with the three labels → the
`claude` workflow on the three `labeled` events. Run 45013295630 ran the agent. Runs 45013296437 and 45013297165
were cancelled while pending in the issue's concurrency group, the second when the agent's own tracking comment
queued run 45013325617 (an `issue_comment` by `claude[bot]`, which the job's `if:` skips). The agent step ended
`success` after 135 turns and 9m04s, with `permission_denials_count` 12, and pushed no branch
(`claude/issue-118-20261122-1037` is a 404). Its checklist on the issue names two pages it had edited,
`docs/adapter-authors.md` and `docs/developer/change/layout.md`, and stops at "Run `make docs-check` and
`make test`". `open-pr` then closed #118 with "The agent finished this run without changes". It had changes: to
that step, an agent that ends without pushing its edits reads the same as one with nothing to change. FAIL for
the monthly refresh. It is not work item 397's: an `issues` event meets no login comparison, and the four steps
the fix gate holds back all ran. The cause is not known, because `show_full_output: false` keeps the session out
of the log. Under the same action pin the fixer pushed twice on 2026-11-08 (pull request #105).

**The review and the merge, by a pull request opened by hand** (the shape of §5 (c) (iii)). The pull request
that carries this section is the drill: documentation only, labelled `auto-merge`, opened by a session with the
owner's `gh`. What it has to show: a `review pr` run whose "Verify a review was submitted" step finds the review
of `claude[bot]` at the head; on that review's approval, an `auto merge` run whose job is not skipped; and the
squash merge into `master`. The run ids and the merge are in the execution log, row P106-1, written after the
merge: a pull request cannot record its own.

### 2026-11-22 — a work order no longer vanishes: the queue, and what an agent left (work item 398)

**The race, as the runs record it.** The factory labels a work order with three labels in one call, three
`labeled` runs of `claude` reach the issue's concurrency group at once, and the group held one waiting run: a
newer run took its place and GitHub cancelled the older. On 2026-10-01, issue #50, the month's dependency
update: run 36856551752, the `claude` one, was cancelled after 2 s. No agent ran, and the issue stood open with
no comment until this work item. On 2026-11-22, issue #118: run 45013296437 was cancelled after 2 s by the third
label's run, and that run, 45013297165, after 22 s by run 45013325617, which the agent's own tracking comment
raised. The record of 2026-09-10 above calls the first of these cosmetic. It was, as long as the run that
started first was the `claude` one.

**`queue: max`, measured on this repository before the hub got it.** A throwaway branch, `probe/398-queue`,
carried one workflow with `concurrency: {group, cancel-in-progress: false, queue: max}` and a 25-second job.
Four pushes in four seconds made runs 45017373650, 45017374969, 45017377856 and 45017379677. GitHub accepted
the key, three runs were `pending` at once, and all four ended `success` one after another in the order they
came (11:27:32, 11:28:05, 11:28:38 and 11:29:10 UTC), none cancelled. The branch is deleted. `actionlint`
1.7.12 does not know the key and reports it as unexpected.

**Why the documenter of issue #118 pushed nothing, as far as it can be told without its session.** Three
things, each read or measured. (1) `make test` takes about four minutes on the runner: 222 s for `make build
test go_test_p=4` in the `fast` job of run 45015837326, and an agent's bare `make test` runs two packages at a
time. (2) The Bash tool gives a command two minutes unless the model asks for more, and Claude Code moves a
command that reaches its limit to the background instead of stopping it (its pages `env-vars` and
`tools-reference`, read 2026-11-22; the action installed Claude Code 2.1.318). The session ended in `success`
9m04s after it began, with the agent's checklist at "Run `make docs-check` and `make test`". (3) The action
commits an agent's uncommitted work itself only when the agent's branch already exists on GitHub
(`checkAndCommitOrDeleteBranch`, read at the pin `c0949c5`), and for an issue it makes the branch in the
checkout alone, so nothing caught the two edited pages. What is not known: that the test run was the command
that was moved, and why the session did not wait for it. `show_full_output: false` keeps the session out of the
log, and it stays so.
