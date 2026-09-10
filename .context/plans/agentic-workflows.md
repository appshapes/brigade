# Autonomous agentic workflows for brigade

Date: 2026-09-09
Status: **draft v3 after brigade-33's review; converged except 7.2 and 7.4, which Rjae confirms; not chartered — no
execution-log rows until Rjae opens them.**
Nothing in section 4 is to be committed, and no GitHub setting in section 4.10 is to be changed, until the owner
charters the rows proposed in section 6.

**Review record.** v1 was drafted by Opus. v2 followed an Opus adversarial verifier pass — 4 high, 8 medium and 9
low notes, all applied — and a re-check of the applied fixes. v3 follows brigade-33's review of v2 (verified
against the tree at `b14f1f3`): 1 high, 3 medium and 2 low, all applied, plus their answers to the eight open
questions, which are converged and folded into the body. §7 now lists only what still needs Rjae.

Scope: this brief ports the `thinktech-api` agentic loop — a GitHub issue labelled `claude` as the universal work
order, an agent that writes a branch, an agent that reviews the PR, an autonomous fix cycle bounded by four
independent brakes, and auto-merge — into `appshapes/brigade`, plus three further agents
(documentation refresh, repository review, release notes) and a Dependabot configuration that feeds the same loop.
It adds eight files under `.github/` (seven workflows and `.github/dependabot.yml`), three agent instruction files
under `.claude/agents/`, one shell script under `scripts/ci/`, one GitHub App, five repository settings and one
ruleset. It changes no Go source, no adapter, no protocol shape and no release
mechanics; the only edits to existing tracked files are documentation lines (section 6) and — if the owner takes
the recommendation in 4.7 — two edits to `release.yml`, a widened top-level `permissions:` block and one added job. The whole apparatus exists for agent-authored changes:
Rjae keeps pushing straight to `master` exactly as today (section 2, blocker 3).

---

## 1. Read this first

### 1.1 Two different security models, and this brief is about the second one

Brigade's **product** security model is allow-by-default, and each of the four sentences usually quoted for it has
a different source — worth pinning, because a claim attributed to the wrong document is a claim nobody can check:

| The sentence | Where it actually is |
| --- | --- |
| **Allow by default**; Brigade never protects a harness from message content — the host's own mechanisms do that | `docs/security.md:129-166` |
| **Refusals are a courtesy** | Rjae's ruling, 2026-09-08 (row F5 in the execution-log archive, moved there 2026-09-09), restated in `docs/security.md` §4 as of `daec17d`. A paraphrase; not a sentence of the threat model. |
| **The frame is a label** | A paraphrase, not a quotation: Rjae's ruling of 2026-09-09 as recorded in P8-1's closing note in the execution-log archive, and `docs/security.md` §4's frame-levels paragraph (`daec17d`). |
| **CLI over MCP** | `docs/research/decisions-2026-08-30.md`, decision **D34** |

`docs/security.md` and the threat model argue about what a *message* can do to a *session*, and the answer is
deliberately "not Brigade's job".

**None of that is what this brief is about.** This brief is about **repository automation on a public repository
with self-hosted runners**, where a wrong setting does not merely mislabel a message — it merges code, or lets an
unreviewed fork PR execute on a Blacksmith VM. The guard rails here are GitHub's and the action's, not Brigade's:

| Guard rail | Who enforces it | Where it appears below |
| --- | --- | --- |
| Only actors with **write access** can trigger the agent | `claude-code-action` (default; checked for issue, PR, comment and review events) | 4.1 — never set `allowed_non_write_users` |
| Bots may trigger it only from an **explicit list** | `allowed_bots` (default `""` = no bots; `"*"` on a public repo lets any GitHub App invoke the agent with a prompt it controls) | 4.1, 4.2 — an explicit two-name list |
| The agent's GitHub power is the **job's `permissions:` block** | GitHub Actions | every file in 4 — the reviewer is `contents: read` |
| Writes that must fire a downstream event use a **repo-scoped, short-lived App installation token** | `actions/create-github-app-token` | 1.2 blocker 1; 4.1, 4.3, 4.4 |
| Fork PRs never see secrets, and outside-collaborator PRs never run at all without approval | GitHub repository settings | 1.2 blocker 2; 4.2, 4.10 |
| A merge waits for CI | one branch ruleset on `master`, PR-only, admin-bypassed | 1.2 blocker 3; 4.3, 4.10 |
| The loop cannot run away | four brakes ported verbatim from the reference, plus one brigade-specific deterministic guard | 4.1, 4.2, 4.3, 4.4 |

The reference implementation is `thinktech-api` (11 workflows, 5 cron agents, one PAT). Its whole engine is a
Personal Access Token with admin bypass, available to five cron workflows, able to push to `master` past branch
protection. Brigade has no stored write credential at all today. The port therefore replaces the PAT with a GitHub
App installation token, which is repository-scoped, short-lived (one hour), and — unlike `GITHUB_TOKEN` — does fire
workflow events.

#### 1.1.1 What the agent can reach, and what it cannot

The table above is about who may *start* an agent. This subsection is the other half, raised by brigade-33 and
missing from v2: once an agent is running, **which credentials are inside the process tree it controls?** The
answer is not "none", and pretending otherwise would be the worst kind of comfort. `claude-code-action`'s own
`docs/security.md` (read 2026-09-09) is explicit that scrubbing is conditional:

> "When [`allowed_non_write_users` is] set, Claude does a best-effort scrub of Anthropic, cloud, and GitHub Actions
> secrets from subprocess environments. On Linux runners with bubblewrap available, subprocesses additionally run
> with PID-namespace isolation."

This brief **never sets `allowed_non_write_users`** (blocker 2, and Rjae's decision 2), so that scrub and that
isolation are **off**. Every `Bash` child the agent spawns inherits the step's environment. Four consequences,
stated exactly:

**(i) The Claude OAuth token IS reachable in every agent step** — the writer in `claude.yml`, the reviewer in
`review-pull-request.yml`, and the release-notes agent in `release-notes.yml`. A prompt-injected session (a
hostile issue body, a poisoned line in a PR diff, a dependency's build script that runs under `make build test`)
can read `CLAUDE_CODE_OAUTH_TOKEN` out of its own environment and exfiltrate it. This is the **same residual risk
`thinktech-api` and `lineman` already accept**, and it is accepted here too — an agent that can run the build can
always reach what the build can reach. It is *bounded*, not eliminated, by four things, all of which are
requirements of this brief and not suggestions:

- a **dedicated** token (§4.10 credentials (ii)) — **never Rjae's personal one**, so a leak costs a rotation and a
  quota, not a person's whole Claude account;
- a **monthly spend ceiling on that dedicated account**, so a stolen token cannot bill without limit. **verify:**
  where the ceiling is actually set — Anthropic Console (billing → usage limits) for an API-key-backed account,
  or the Claude Code plan's own limits for a subscription-backed one; the two are different surfaces and the token
  minted by `claude setup-token` may sit on either. This is the one item of §7 that H1 adds for Rjae;
- **rotation on any suspicious run** — a red `pr-guard.sh`, a fixer that touched a path it had no business in, a
  run whose log shows an unexpected network verb: rotate first, investigate second (`gh secret set
  CLAUDE_CODE_OAUTH_TOKEN` is one command);
- **`show_full_output: false` everywhere, without exception.** This repository is public and its run logs are
  world-readable.

**(ii) The writer's job `GITHUB_TOKEN` IS reachable**, with `contents: write`, `pull-requests: write` and
`issues: write` (§4.1's `permissions:` block). It is **scoped to this repository alone** and **expires when the
job ends**, so the blast radius is "this repo, for the length of one run" — which is precisely why the job block
is written narrowly rather than inherited. The **reviewer's** job token is `contents: read` (§4.2), which is the
whole reason a reviewer cannot push; the release-notes job's is `contents: write` + `issues: write` for its two
named API calls (§4.7).

**(iii) The App private key and the App installation token are NEVER in any agent step.** Two mechanisms, and the
second exists because the first is an argument rather than a barrier:

- *The argument.* `secrets.BRIGADE_BOT_PRIVATE_KEY` is referenced only by the `Mint an App installation token`
  step, and an Actions secret is materialised into a step's environment **only for the step that names it**. A
  **later** step's secret is *not* reachable to an **earlier** step's children: the token is minted after the
  agent process has exited, so there is nothing in the agent's environment to read and nothing yet in existence
  to steal. That is true, and it is still only a statement about ordering.
- *The barrier, and this brief takes it.* Put the App-token steps in a **separate job** with `needs:`, so the
  agent's job never holds the private-key secret **at all** — not in a later step, not in the runner's process
  environment, not one `if:` edit away from being reachable. §4.1 is written this way: `agent` runs the session
  and publishes its branch name as a job output; `open-pr` (`needs: agent`) mints the token and creates the PR.
  `auto-merge.yml` and `_create-claude-issue.yml` need no such split — neither runs an agent at all.

**(iv) `WebFetch` and `WebSearch` are DROPPED from every agent's allow list.** The writer, the reviewer and the
release-notes agent all work entirely from a checkout, `git`, `gh` and the build; **none of the three needs the
web for this repository**, and the allow list should say what is needed rather than what is conventional.
Removing them removes the easiest exfiltration verb an injected prompt can reach for — one tool call, no
subprocess, no shell quoting. Add either back only with a stated reason written next to it in the settings block.

**Say the limit out loud:** this does **not** eliminate egress. `Bash(make:*)` and `Bash(go:*)` execute code —
`go test` compiles and runs test binaries, `make build` runs the toolchain, and any of those can open a socket.
Dropping the web tools closes the easiest path, not the class. The controls that actually bound the damage are the
ones above: a dedicated token, a ceiling, rotation, and job `permissions:` blocks written to the minimum.

**Drill (j)** exercises the leak path — a throwaway issue whose body asks the agent to print its environment must
not put a token in the world-readable log — and it is recorded as **evidence, not as a control that proves
safety**: a masked log says the masking worked on that string, not that the token was unreachable. It was
reachable. That is the accepted residual.

### 1.2 The four blockers, and the exact thing that clears each

These are Rjae's, agreed 2026-09-09. Each is stated with the command or the settings path that clears it.

---

**Blocker 1 — the org secrets are invisible to this public repository.**

Measured 2026-09-09 with `gh`: repository secrets `total_count 0`; organization `appshapes` holds `CLAUDE_CODE_TOKEN`,
`GH_ACTIONS_TOKEN` and `QODANA_TOKEN`, **all three with visibility `private`**, i.e. shared only with private
repositories in the org. `appshapes/brigade` is public. So every `${{ secrets.CLAUDE_CODE_TOKEN }}` in a naive port
resolves to the empty string and the action fails at authentication — silently, in the sense that the failure is at
the bottom of a run nobody is watching.

*Clears with two things:*

1. **A GitHub App** installed on `appshapes/brigade` with `Contents: read & write`, `Pull requests: read & write`,
   `Issues: read & write` and `Metadata: read`. Its app id goes in a repository **variable** (public, harmless), its
   private key in a repository **secret**. Every write that must fire a downstream event mints a token per job:

   ```yaml
   # Repo-scoped, expires in one hour, and — unlike GITHUB_TOKEN — its writes DO raise workflow
   # events, which is the whole reason the chain (issue → PR → review → merge) keeps moving.
   - name: Mint an App installation token
     id: app-token
     uses: actions/create-github-app-token@v3      # verify: current major tag at implementation time
     with:
       app-id: ${{ vars.BRIGADE_BOT_APP_ID }}
       private-key: ${{ secrets.BRIGADE_BOT_PRIVATE_KEY }}
       # owner/repositories default to the current repository; leave them unset so the token
       # can never reach another repository in the org.
   ```

   Then `GH_TOKEN: ${{ steps.app-token.outputs.token }}` on the step that creates the issue, opens the PR, or merges.

2. **A repository-scoped `CLAUDE_CODE_OAUTH_TOKEN`** (`gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo appshapes/brigade`),
   *or* flipping the org's `CLAUDE_CODE_TOKEN` to `selected` visibility and selecting this repository:

   ```sh
   # Option B, org-side (needs org admin). `-F` parses only numbers, booleans, null and @file — it would send
   # the LITERAL string "[123]" for an array, so use the repeated-key form `-f 'name[]=value'` instead:
   gh api -X PUT orgs/appshapes/actions/secrets/CLAUDE_CODE_TOKEN/repositories \
     -f 'selected_repository_ids[]=<brigade repo id>'
   # (or pipe a JSON body: printf '{"selected_repository_ids":[<id>]}' | gh api -X PUT <path> --input -)
   # (the value must be re-set with visibility=selected; see `gh secret set --org appshapes --visibility selected`)
   ```

   This brief writes `secrets.CLAUDE_CODE_OAUTH_TOKEN` throughout. If option B is taken, rename the reference in
   the three workflows that use it and nothing else changes. **Option A is the recommendation**, and §4.10's
   credentials checklist says why: a *dedicated* repository token keeps agent usage off Rjae's personal
   subscription, which the org secret (a named human's credential, as in the reference repo, where every agent
   run bills to one person) would not.

*Interaction found while writing this brief, and it is load-bearing:* the reference's `if:` clauses and
`allowed_bots` are keyed on the login prefix `claude`, which works there because (a) reviews are submitted by the
**Claude GitHub App** (`claude[bot]`, bot_id 41898282) and (b) PRs are opened by a **PAT belonging to a human**. Under
this port, PRs and issues are created by **our own App**, whose actor login is `<app-slug>[bot]` — *not* `claude`.
Three consequences:

- **(a)** `allowed_bots` must be the two-name list `"claude,<app-slug>"`, not `"claude"` alone, or every event our
  own App raises is rejected by the action's bot filter and the chain dead-ends with a green run and no work. This
  is a documented, deliberate widening of Rjae's decision 2 ("allowed_bots exactly `claude`"), forced by decision 1
  (App token); it is still an explicit list and never `"*"`. **Converged with brigade-33 2026-09-10 and adopted
  (was open question 7.5): no veto.** The widening stands; what is not negotiable either way is the principle —
  an explicit list, never `"*"`, and never `allowed_non_write_users`.
- **(b) — resolved 2026-09-09, and the answer is yes.** The predicate
  `startsWith(github.event.review.user.login, 'claude')` in the fix gate and in `auto-merge.yml` presumes the
  reviewer's `gh pr review` runs as `claude[bot]`. That holds only if the **Claude GitHub App is installed on this
  repository**; without it the reviewer's `gh` is authenticated with the job's `GITHUB_TOKEN` and the review lands
  as `github-actions[bot]`, which matches nothing, so approvals never arm auto-merge and CHANGES_REQUESTED never
  arms the fixer. **Measured with `gh` on 2026-09-09: the Claude GitHub App (`app_slug` `claude`, `app_id`
  1236702) is installed on the `appshapes` org for *all* repositories** — alongside `qodana-cloud`, `railway-app`
  and `blacksmith-sh` — so `appshapes/brigade` is covered and the prerequisite is met without a further install.
  The sentence stays because the *dependency* stays: drill (a) still prints the actual review author login, so the
  predicate rests on a measured fact rather than on this paragraph.
- **(c)** The same App dependency governs the fixer's **push**, not just the review author. If
  `claude-code-action` pushed the fix with the job's `GITHUB_TOKEN` rather than with a Claude App token, no
  `pull_request: synchronize` would fire, `review pr` would never re-run, and the loop would park — *and*
  verifier 1 ("Verify the fix was pushed") would pass, because the head SHA did move. That is exactly the silent
  park the verifiers exist to prevent, which is why drill (b) records a new `review pr` run id after **every** fix
  push and not merely the review count.

---

**Blocker 2 — a public repository with self-hosted runners must not run unreviewed contributor code.**

`ci.yml` triggers on `pull_request: {}` unfiltered, and its `supabase` job runs Docker, starts a database and runs
`make e2e` — on a Blacksmith VM. The archive already recorded the obligation at flip-public time
(`brigade-execution-log-archive.md:4277-4279`).

*Clears with:*

- **Settings → Actions → General → "Fork pull request workflows from outside collaborators" → "Require approval for
  all outside collaborators".** This is a **UI setting**; there is a REST surface for the
  fork-PR contributor approval policy but it is not stable across API versions — **verify:** whether
  `gh api -X PUT repos/appshapes/brigade/actions/permissions/fork-pr-contributor-approval` exists on the deployed API
  before scripting it. Do it in the UI and record the screenshot in the drill evidence.
- **Keep the action's default write-access trigger check.** Never set `allowed_non_write_users` — the action's own
  `docs/security.md` calls it a significant risk, and on a public repository it means any stranger's comment becomes
  a prompt.
- `allowed_bots` is the explicit two-name list of 1.2 above, never `"*"`.
- Belt and braces in this brief's own files: `review-pull-request.yml`'s job carries
  `github.event.pull_request.head.repo.full_name == github.repository`, because a fork PR cannot see
  `secrets.CLAUDE_CODE_OAUTH_TOKEN` anyway (GitHub does not expose secrets to fork PRs) and a reviewer job that
  cannot authenticate would fail loudly on every drive-by contribution. Fork PRs get CI (after approval) and a human.

---

**Blocker 3 — nothing today makes a merge wait for CI, and `gh pr merge --auto` has nothing to wait on.**

There is no branch protection on `master` (owner's ruling: none while there is one committer and merges only), and
exactly one ruleset exists, `protect-release-tags` (active). `gh pr merge --auto` is not a timer: it arms the PR to
merge when its *requirements* are satisfied, and with no required checks GitHub reports the PR as already mergeable
and the mutation errors or merges immediately. So auto-merge without a ruleset is not "merge when green", it is
"merge now".

*Clears with one ruleset plus one repository setting.* Full JSON in 4.10; the shape is: target
`refs/heads/master`, one `required_status_checks` rule naming the four CI job names as they appear on a PR
(`fast`, `macos`, `reproducibility`, `supabase` — read from `ci.yml`; **`deploy-staging` is deliberately absent**,
it is skipped unless `vars.BRIGADE_STAGING == 'true'` and a required check that never runs blocks every PR
forever), and `bypass_actors` containing the repository-admin role so **Rjae's direct pushes to `master` are
untouched**.

The subtlety that makes the App mandatory here: **a bypass actor bypasses the rule.** If the merging identity were
an admin PAT, `gh pr merge --auto` would merge past the very gate the ruleset exists to impose, and the drill would
pass while proving nothing. The merge must therefore be performed by the **App**, which is *not* in `bypass_actors`.
Drill (c) proves the wait empirically: a PR whose CI is red must sit armed and unmerged, and merge only when the
same PR goes green.

---

**Blocker 4 — the execution-log rule and the commit-message rule would otherwise make every agent PR illegal.**

`.context/plans/brigade-execution-log.md:3-4` — "Update it in the same commit as the work" — and `CLAUDE.md`'s
`15: <Imperative summary>` are both absolute today. An agent PR that bumps a dependency has no execution-log row to
write, and a squash merge's commit subject is not obviously under anyone's control.

*Clears with two rulings, already given:*

- **Agent PRs labelled `maintenance` are exempt from the execution-log-row rule.** The exact sentence to add to
  the log header and to `CLAUDE.md` is in section 6.
- **Squash merges with a PR title of the form `15: <Imperative summary>` satisfy the commit-message rule.** For this
  to be true mechanically, the repository's squash-merge title source must be set to the PR title — GitHub's default
  is `COMMIT_OR_PR_TITLE`, which for a **single-commit PR uses the commit's own subject**, not the PR title. One
  `gh api -X PATCH` fixes it (4.10). PR titles are minted by `claude.yml`'s Create-PR step, which prefixes `15: `
  itself, so the shape is enforced at creation, not by hope.

### 1.3 What the reference cannot teach, because brigade is not thinktech

Five brigade constraints have no analogue in the reference repo and every agent file in section 4.9 carries them:

1. **Release pins.** `plugin/bin/VERSION` and `plugin/bin/checksums.txt` are produced only by `make release`
   (`CLAUDE.md:33`). An agent that touches either has produced a lie. Deterministic guard in 4.2 (not agent
   judgement — a `git diff --name-only` in the reviewer workflow, before the agent runs).
2. **The `go` line is a release-reproducibility pin** (`CLAUDE.md:28-30`). An agent must never bump it. Same guard.
3. **A Go source change ships nothing until `make release`.** `make checksums-check` rule (c) keeps the `fast` job
   green through the published-release arm, and the plugin keeps serving the last released binary. Documented in the
   agent files and proven in drill (f) so nobody files it as a defect.
4. **`make commit` / `make push` must never run on a runner.** `make commit` is
   `typecheck pull build test` then `git add --verbose :/ .` — it sweeps **untracked** files (the same hazard
   `release-prep.sh` guards, and the hazard that swept 25 `.pyc` files and the AI-tooling state into commits on
   this repository already) and it merges `origin` mid-run. On a PR branch the agent commits with plain `git` after
   running the gates by name.
5. **Shell files need the pinned-container shellcheck.** `docker run --rm -v "$PWD:/mnt" -w /mnt
   koalaman/shellcheck:v0.9.0 -s sh <file>` as well as the local one — which is why `Bash(docker:*)` is on the
   writer's allowlist, and the only reason it is.

---

## 2. Decisions (Rjae, 2026-09-09) — binding

**Blockers, all four agreed:**

1. Org secrets are invisible to the public repo → use a **GitHub App installation token**
   (`actions/create-github-app-token`) **per repository** for every write that must fire an event — issue create, PR
   create, merge — and a **repo-scoped `CLAUDE_CODE_OAUTH_TOKEN`** (or flip the org secret to selected repositories).
2. Enable the repository setting **"Require approval for all outside collaborators"**; keep the action's **default
   write-access check**; **never** `allowed_non_write_users`; `allowed_bots` an explicit list — `"claude"` (widened
   to `"claude,<app-slug>"` by the App-identity interaction of 1.2; still explicit, never `"*"`).
3. **A ruleset on `master` requiring the CI checks for PULL REQUESTS only** (bypass: repository admin, so Rjae's
   direct pushes to `master` are untouched) plus the repository setting **"Allow auto-merge"** — with a **drill that
   proves `gh pr merge --auto` WAITS** for the checks under that ruleset. The merging identity must be **the App**,
   not an admin PAT, or the bypass would defeat the gate.
4. **Agent PRs labelled `maintenance` are exempt from the execution-log-row rule.** **Squash merges with the PR
   title in the form `15: <Imperative summary>` satisfy the commit-message rule.**

**Workflows, agreed:**

1. **The loop** — `claude.yml` (hub) + `review-pull-request.yml` + `auto-merge.yml`, ported from thinktech with all
   four brakes: the fix cap derived by **counting claude-authored `CHANGES_REQUESTED` reviews** (`MAX_ATTEMPTS 3`),
   **actor filters** against recursion, **App-token vs `GITHUB_TOKEN` as the recursion switch**, and **concurrency
   per PR plus `review.commit_id == head.sha`**; and both **fail-loud verifiers** (no review submitted / no push
   landed). The reviewer's settings allowlist is **read-only**; the writer's allowlist is `Bash(make:*)`,
   `Bash(go:*)`, `Bash(git:*)`, `Bash(gh:*)`, `Bash(docker:*)` — the last for the shellcheck 0.9.0 container — plus
   the read-only tools. Reviewer instructions live in a committed `.claude/agents/reviewer.md` carrying brigade's
   invariants.
2. **Dependencies** — a `.github/dependabot.yml` (gomod for `go.mod`, github-actions; monthly; grouped) and **not**
   an agent. The loop's fix cycle adapts call sites when a bump breaks `make build test deps-check tidy-check`. The
   agent must **never** bump `go.mod`'s `go` line (the reproducibility pin) and must respect
   `docs/allowed-deps.txt`. `tools.mod` is not a `go.mod`; the brief says how it is kept current. A Go bump ships
   nothing until Rjae runs `make release`.
3. **`release-notes.yml`** on `release: [published]` — read `CHANGELOG.md`'s section, the commits since the previous
   tag and the assets; `gh release edit --notes`; open one issue/discussion "`<version>` is out — run
   `/brigade:update`" for members. Read-only plus that one write.
4. **`update-documentation.yml`**, monthly, scoped to **structural drift** in `docs/setup.md`, `plugin/README.md`,
   `README.md`, `docs/adapter-authors.md` (paths, targets, versions, command names) and explicitly **not**
   `docs/security.md` or `docs/protocol-v1.md`. CI's doc-witness tests are the gate. Opens a PR through the loop,
   **with** auto-merge. *(§4.5 now proposes `auto_merge: false` for the first two months, then true — a CHANGE to
   this decision, converged with brigade-33 2026-09-10, **awaiting Rjae's confirmation**; §7.A item 1. Until
   Rjae confirms, this line is what binds.)*
5. **`review-repository.yml`**, monthly: the reviewer reads the tree and opens a PR with minimal fixes **but without
   the `auto-merge` label** — the loop reviews and fixes it; **merging waits for Rjae**. *(§4.6 now proposes
   `workflow_dispatch` only, cron commented out, until the second adapter is chartered — a CHANGE to this
   decision, converged 2026-09-10, **awaiting Rjae's confirmation**; §7.A item 2. Until Rjae confirms, this line
   is what binds.)*

**Dropped entirely:** `find-improvements`, `enforce-logging`, `record-memory` (agent memory stays under
`.context/`, never `.claude/`).

**Model:** **Opus for every workflow agent** (`claude_args --model claude-opus-5`). **Fable never in CI** — the Fable
budget is a hard budget and CI is scaffolding-tier work by the log's own tier policy.

---

## 3. What the loop looks like once it is running

```
cron ──► _create-claude-issue.yml ──(App token)──► issue labelled `claude` [+ `maintenance`] [+ `auto-merge`]
                                                       │  issues: labeled  (fires because the App is not GITHUB_TOKEN)
                                                       ▼
                                                  claude.yml  (tag mode: the issue body IS the prompt)
                                                       │  agent pushes claude/issue-N
                                                       │  Create PR  ──(App token)──► PR "15: <title>", labels copied
                                                       ▼  pull_request: opened
                                    review-pull-request.yml  ── guard (release pins, go line) ──► reviewer agent
                                                       │
                                    ┌──────────────────┴───────────────────┐
                            APPROVED│                                      │CHANGES_REQUESTED + "@claude …" trailer
                                    ▼                                      ▼  pull_request_review: submitted
                            auto-merge.yml                            claude.yml (fix gate: ≤ 3 claude
                            (label auto-merge &&                       CHANGES_REQUESTED reviews on this PR)
                             commit_id == head.sha)                        │ fixer pushes
                                    │ gh pr merge --squash --auto          ▼ pull_request: synchronize
                                    ▼ (App; waits on the ruleset)     back to review-pull-request.yml
                            squash commit "15: <Imperative summary>"
```

**Two entry paths the diagram does not draw, both deliberate, both human-initiated.**

- **A human applies the `claude` label to an existing PR.** `claude.yml` listens on `pull_request: [opened,
  labeled]`, so labelling a PR that already exists starts the agent on it. That is a real path, not an oversight:
  the action's **write-access check gates it exactly like every other event**, and the label *is* the human's
  explicit authorization to run an agent with write power on that branch. Drill (i) proves both halves — it starts
  for a write-access actor and it does not start for anyone else.
- **A human's review body containing `@claude` starts the fixer even on an APPROVED review.** §4.1's review clause
  admits `claude*` reviews only in `changes_requested` state, but a review from a **non-`claude` login** matches on
  the `@claude` mention alone, whatever its state. By design: a human writing `@claude …` into a review is explicit
  re-authorization, and it is the escape hatch that lets a person restart the loop after the fix cap has handed
  off. It **cannot race auto-merge**, because `auto-merge.yml` arms only on `startsWith(review.user.login,
  'claude')` — a human's approval arms nothing.

Four brakes, one per failure mode: the **fix cap** (runaway ping-pong), the **actor filters** (self-triggering), the
**token choice** (an event you do not want must be raised with `GITHUB_TOKEN`, whose events never start workflows),
and **concurrency + head-SHA pinning** (stale reviews, parallel reviewers). Two fail-loud verifiers convert the two
silent-no-op failure modes — a reviewer that ends green having submitted nothing, a fixer that narrates a fix
without pushing — into red runs. Brigade adds a fifth, deterministic brake: the release-pin guard in 4.2
(`scripts/ci/pr-guard.sh`, called by `review-pull-request.yml` before the agent runs).

---

## 4. Files to add

Every file below is written in the house style of the reference — more comment than code, every non-obvious clause
carrying its reason. Each is followed by its **brigade-specific deltas from the reference**.

Two conventions applied throughout, both brigade's rather than thinktech's:

- **Actions are pinned by major tag** (`actions/checkout@v7`, `actions/setup-go@v7`,
  `actions/create-github-app-token@v3` …), matching `ci.yml`, `release.yml` and `keepalive.yml` — **with exactly
  one exception, `anthropics/claude-code-action`, which is pinned by full commit SHA** (converged with brigade-33
  2026-09-10, open question 7.7; adopted). The house style is deliberately broken for this one action because it
  is the only action in the repository that **runs a model with write access to the repository**: a mutable major
  tag moving under `actions/checkout` changes how a checkout works, and a mutable major tag moving under this one
  changes what an autonomous agent *does* with a `contents: write` token, between one Tuesday and the next. The
  form is `anthropics/claude-code-action@<40-hex>   # v1`, written the same way in §4.1, §4.2 and §4.7, and the
  comment is what keeps it legible; **verify:** resolve the SHA for the current `v1` at implementation time
  (`gh api repos/anthropics/claude-code-action/git/ref/tags/v1 --jq .object.sha`, dereferencing an annotated tag)
  and record it in the P9-2 commit. The cost is one Dependabot PR a month, which `.github/dependabot.yml`'s
  `github-actions` ecosystem already produces.
- **Every job sets `timeout-minutes`** (`CLAUDE.md`; GitHub's default is 360). The existing `ci.yml` bounds are twice
  a measured p90 of ten named runs. **No run of any workflow below exists yet**, so every bound below is a stated
  guess with a comment saying so, and re-pinning it from ten real runs is an item on the P9-3 row.

### 4.1 `.github/workflows/claude.yml` — the hub

```yaml
# The hub of the agentic loop: every path that starts an agent with WRITE power lands here.
#
# Ported from thinktech-api/.github/workflows/claude.yml (read 2026-09-09). Brigade-specific deltas are listed
# under this file in .context/plans/agentic-workflows.md §4.1; the ones that matter most while reading:
#   * the writer commits with plain `git`, never `make commit`/`make push` (that chain runs `git add :/ .`,
#     which sweeps untracked files, and merges origin mid-run);
#   * `Bash(docker:*)` is allowed for exactly one reason: the pinned shellcheck 0.9.0 container CLAUDE.md
#     requires before any shell file is pushed;
#   * writes to plugin/bin/**, .claude/** and .github/workflows/** are DENIED, not merely discouraged;
#   * the App private key lives in a SECOND JOB (`open-pr`), never in the job that runs the agent — the agent's
#     Bash children inherit its environment, so the credential is kept out of the job rather than out of a step.
#
# Tag mode, deliberately: no `prompt:` input. In tag mode the action takes the issue/comment/review body as the
# prompt, which is what makes the scheduled workflows' agent-role prose (`Read .claude/agents/developer.md ...`)
# work and what makes a human's `@claude fix X` comment work. Supplying `prompt:` would switch the action to
# automation mode and change how event context is assembled — do not add one here without re-drilling the loop.
name: claude

on:
  issue_comment:
    types: [created]
  pull_request_review_comment:
    types: [created]
  issues:
    types: [assigned, labeled]
  pull_request:
    types: [opened, labeled]
  pull_request_review:
    types: [submitted]
  workflow_dispatch:

# One agent at a time per issue or PR. `cancel-in-progress: false` on purpose, unlike the reviewer below: a
# cancelled FIXER can die between `git commit` and `git push`, leaving a PR whose head never moves and whose
# "Verify the fix was pushed" step never runs — a silent park, which is the exact failure this file's verifiers
# exist to make impossible. The fix cap already bounds the loop, so queueing is safe.
concurrency:
  group: claude-${{ github.event.pull_request.number || github.event.issue.number || github.run_id }}
  cancel-in-progress: false

jobs:
  agent:
    # Seven clauses, ported unchanged in shape from the reference. The comment clauses exclude claude-authored
    # comments (a recursion surface the moment `allowed_bots` admits the claude actor). The review clause admits
    # claude* reviews ONLY in changes_requested state: an '@claude' that leaked into an approval must never race
    # a fixer push against an arming auto-merge.
    #
    # Read the review clause once more, because its asymmetry is deliberate: a review from a NON-claude login
    # whose body contains '@claude' starts the fixer EVEN IN APPROVED STATE. That is by design — a human writing
    # '@claude ...' into a review is explicit re-authorization, and it is how a person restarts the loop after the
    # fix cap has handed off. It cannot race auto-merge: auto-merge.yml arms only on a claude* approval, so a
    # human's approval arms nothing to race.
    #
    # The `pull_request: labeled` half of the last clause is likewise a real, human path: applying the `claude`
    # label to an EXISTING PR starts an agent on it. The action's write-access check gates it exactly as it gates
    # every other event, and the label is the human's authorization. Drill (i).
    #
    # `startsWith(..., 'claude')` presumes the reviewing identity is the Claude GitHub App (claude[bot]). That is
    # a PREREQUISITE, not an assumption: without that App installed the reviewer's `gh pr review` lands as
    # github-actions[bot], no clause here matches, and the fix loop silently never arms. Drill (a) prints the
    # real login; re-pin this prefix to it if it differs.
    if: |
      (
        (github.event_name == 'workflow_dispatch') ||
        (github.event_name == 'issue_comment' && contains(github.event.comment.body, '@claude') && !startsWith(github.event.comment.user.login, 'claude')) ||
        (github.event_name == 'pull_request_review_comment' && contains(github.event.comment.body, '@claude') && !startsWith(github.event.comment.user.login, 'claude')) ||
        (github.event_name == 'pull_request_review' && contains(github.event.review.body, '@claude') && (!startsWith(github.event.review.user.login, 'claude') || github.event.review.state == 'changes_requested')) ||
        (github.event_name == 'issues' && github.event.action == 'labeled' && github.event.label.name == 'claude') ||
        (github.event_name == 'issues' && github.event.action == 'assigned' && contains(join(github.event.issue.labels.*.name, ','), 'claude')) ||
        (github.event_name == 'pull_request' && contains(join(github.event.pull_request.labels.*.name, ','), 'claude'))
      )
    runs-on: blacksmith-4vcpu-ubuntu-2404       # the same label ci.yml's `fast` job uses: docker, jq, gh, shellcheck 0.9.0
    # NOT a measured p90 — no run of this workflow exists. `make build test` alone is ~5 min on this label
    # (ci.yml's `fast` job), an agent turn budget of --max-turns 60 can add many of those, and a hung agent must
    # not burn six hours of the concurrency group. Re-pin from ten real runs (P9-3).
    timeout-minutes: 45
    # Published so the `open-pr` job below can act on it. A job output is the ONLY channel between the two jobs,
    # and that is exactly the point: `open-pr` holds the App private key and this job must not, so nothing
    # crosses the boundary but a branch name (§1.1.1 (iii)).
    outputs:
      branch_name: ${{ steps.claude.outputs.branch_name }}
    # NOTE what is NOT in this job: `secrets.BRIGADE_BOT_PRIVATE_KEY`. The agent's Bash children inherit this
    # step's environment (no `allowed_non_write_users`, so the action's conditional secret scrub and bubblewrap
    # isolation are both off — §1.1.1), so the App's write credential is kept out of the job entirely rather than
    # merely out of an earlier step. Reachable here, by design and by acceptance: CLAUDE_CODE_OAUTH_TOKEN and
    # this job's own repo-scoped, job-lifetime GITHUB_TOKEN.
    permissions:
      contents: write          # the agent commits and pushes its branch
      pull-requests: write     # comments, labels
      issues: write            # the agent comments on the issue it is working (the CLOSE now happens in open-pr)
      id-token: write          # claude-code-action's OIDC
      actions: read            # `additional_permissions: actions: read` lets the agent read its own run logs
    steps:
      # ---------------------------------------------------------------- brake 1: the derived fix cap
      # The reviewer ends a CHANGES_REQUESTED body with an '@claude ...' instruction, which lands here through
      # pull_request_review. Each fix cycle is armed by exactly ONE claude* CHANGES_REQUESTED review, so counting
      # those reviews IS the attempt counter: nothing to persist, nothing to reset, correct after a re-run.
      # Scoped to claude-authored reviews — a HUMAN's '@claude' review is explicit re-authorization and bypasses
      # the cap. The handoff comment is posted once, carries no @-mention (nothing re-triggers), and uses
      # GITHUB_TOKEN precisely because its events never start workflows. GH_REPO is required: this step runs
      # pre-checkout, where `gh` has no git context.
      #
      # The staleness clause below is not decoration either. GitHub can deliver the same webhook twice, and the
      # per-PR concurrency group QUEUES rather than cancels (see the `concurrency` block above), so two runs of
      # this gate for ONE review are possible — and both would read the same review count, both would see
      # `count <= MAX_ATTEMPTS`, and two fixers would run on one blocker. Counting cannot distinguish them.
      # Head-SHA identity can, deterministically: a review addresses the head it was written against, so the run
      # proceeds only while the PR's CURRENT head still equals the triggering review's `commit_id`. The first
      # fixer's push moves the head; a duplicate delivery arriving after it sees head != commit_id and skips as a
      # stale review. No timestamps, no locks, no state to reset.
      - name: Gate autonomous fix attempts
        id: fix_gate
        if: github.event_name == 'pull_request_review' && startsWith(github.event.review.user.login, 'claude')
        env:
          GH_TOKEN: ${{ github.token }}
          GH_REPO: ${{ github.repository }}
          PR_NUMBER: ${{ github.event.pull_request.number }}
          REVIEW_COMMIT_ID: ${{ github.event.review.commit_id }}
          MAX_ATTEMPTS: 3
        run: |
          set -euo pipefail
          # Buffers stdout per attempt: a failed attempt (a mid-pagination transient, say) must not leak partial
          # output into the caller's capture and undercount the reviews.
          gh_retry() {
            local i out
            for i in 1 2 3; do
              if out=$("$@"); then printf '%s\n' "$out"; return 0; fi
              echo "attempt $i failed: $*" >&2
              [ "$i" -lt 3 ] && sleep 5
            done
            return 1
          }
          # Staleness first, before the count: a duplicate delivery of the same review, or a review of a head
          # some other push has already superseded, must not spend a fix-cap slot on work that already landed.
          head_now=$(gh_retry gh api "repos/$GITHUB_REPOSITORY/pulls/$PR_NUMBER" --jq '.head.sha')
          if [ "$head_now" != "$REVIEW_COMMIT_ID" ]; then
            echo "stale review — a fix already landed (review addressed $REVIEW_COMMIT_ID, head is now $head_now); skipping"
            echo "proceed=false" >> "$GITHUB_OUTPUT"
            exit 0
          fi
          ids=$(gh_retry gh api --paginate "repos/$GITHUB_REPOSITORY/pulls/$PR_NUMBER/reviews" \
            --jq '.[] | select((.user.login // "" | startswith("claude")) and .state == "CHANGES_REQUESTED") | .id')
          count=$(printf '%s\n' "$ids" | grep -c . || true)
          if [ "$count" -gt "$MAX_ATTEMPTS" ]; then
            echo "proceed=false" >> "$GITHUB_OUTPUT"
            if [ "$count" -eq $((MAX_ATTEMPTS + 1)) ]; then
              gh pr comment "$PR_NUMBER" --body "Autonomous fix cap reached: the reviewer has requested changes $count times (cap: $MAX_ATTEMPTS fix cycles). Handing off to a human — no further autonomous fixes will run on this PR."
            fi
          else
            echo "proceed=true" >> "$GITHUB_OUTPUT"
          fi

      # This `if:` is repeated verbatim on the next four steps rather than split into a second job: a second job
      # would need its own checkout, its own Go cache and its own timeout for no gain. It reads "run unless this
      # is a claude review that the fix cap has stopped".
      - name: Checkout
        if: github.event_name != 'pull_request_review' || !startsWith(github.event.review.user.login, 'claude') || steps.fix_gate.outputs.proceed == 'true'
        uses: actions/checkout@v7
        with:
          fetch-depth: 0        # `make checksums-check` rule (c) reads tags and the release listing; a shallow
                                # clone makes the agent's own gate run lie about what it proved.

      - name: Setup Go
        if: github.event_name != 'pull_request_review' || !startsWith(github.event.review.user.login, 'claude') || steps.fix_gate.outputs.proceed == 'true'
        uses: actions/setup-go@v7
        with:
          go-version-file: go.mod        # 1.27.0 from the `go` line; there is no toolchain line and none may be added
          cache-dependency-path: |
            go.sum
            tools.sum

      # golangci-lint is installed by `make setup-lint` into ./bin from a pinned install.sh. Doing it HERE rather
      # than inside the agent's session keeps a ~40s network install out of the turn budget and out of the
      # agent's error surface: `make lint` then just works when the agent reaches for it.
      - name: Install the pinned linter
        if: github.event_name != 'pull_request_review' || !startsWith(github.event.review.user.login, 'claude') || steps.fix_gate.outputs.proceed == 'true'
        run: make setup-lint

      - name: Code
        id: claude
        if: github.event_name != 'pull_request_review' || !startsWith(github.event.review.user.login, 'claude') || steps.fix_gate.outputs.proceed == 'true'
        # THE ONE ACTION PINNED BY SHA, not by major tag — deliberately breaking the house style the other three
        # brigade workflows keep (§4's conventions; open question 7.7, adopted 2026-09-10). It is the only action
        # in this repository that runs a MODEL with repository write access, so a mutable tag moving under it
        # changes what an autonomous agent does, not merely how a step behaves.
        # verify: resolve the full 40-hex SHA of the current `v1` at implementation time and paste it here —
        #   gh api repos/anthropics/claude-code-action/git/ref/tags/v1 --jq '.object.sha'
        #   (an annotated tag needs one more hop: gh api .../git/tags/<sha> --jq '.object.sha')
        uses: anthropics/claude-code-action@<RESOLVE-AT-IMPLEMENTATION>   # v1
        with:
          # brake 2, second half. Two logins, both explicit, never '*':
          #   claude       — the Claude GitHub App, the actor of the fixer's own push (synchronize) and of the
          #                  CHANGES_REQUESTED review that arms this job;
          #   brigade-bot  — OUR App, the actor of every issue and PR the automation creates. Without it the
          #                  `issues: labeled` event a scheduled run raises is rejected by the action's bot
          #                  filter and the scheduled agents produce silent, unworked issues.
          # '*' on a public repository would let ANY GitHub App invoke this agent with a prompt it controls.
          # verify: the exact normalized login of our App ('<slug>' vs '<slug>[bot]') against drill (a)'s output.
          allowed_bots: "claude,brigade-bot"
          # NEVER set allowed_non_write_users. The action's default check — write access on the triggering actor,
          # verified for issue, PR, comment and review events — is the single control that keeps a stranger's
          # comment on a PUBLIC repository from becoming a prompt on a self-hosted runner.
          claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          # Opus for every workflow agent (the log's tier policy: CI/scaffolding/docs are Opus-tier). Fable never
          # runs in CI — its budget is hard and is reserved for the security path and adversarial passes.
          # --max-turns is a hard stop on a wandering session. 60 is PROVISIONAL by decision, not by omission
          # (7.3, converged with brigade-33 2026-09-10 and adopted): re-pin it from the first ten real runs on
          # the P9-3 row. The per-run cap is only half the answer — the other half is a MONTHLY SPEND CEILING on
          # the dedicated OAuth account (§1.1.1 (i), §4.10 credentials (ii)), which bounds a wandering month the
          # way --max-turns bounds a wandering session, and bounds a stolen token besides.
          claude_args: "--model claude-opus-5 --max-turns 60"
          # Public repository: run logs are world-readable. Keep the full agent output out of them.
          show_full_output: false
          # Lets the agent read its own workflow run logs when a gate fails in CI rather than locally.
          additional_permissions: |
            actions: read
          settings: |
            {
              "permissions": {
                "allow": [
                  "Bash(make:*)",
                  "Bash(go:*)",
                  "Bash(git:*)",
                  "Bash(gh:*)",
                  "Bash(docker:*)",
                  "Bash(ls:*)",
                  "Bash(cat:*)",
                  "Bash(find:*)",
                  "Bash(grep:*)",
                  "Bash(rg:*)",
                  "Bash(sed:*)",
                  "Bash(jq:*)",
                  "Write(.context/plans/**)",
                  "Edit(.context/plans/**)"
                ],
                "deny": [
                  "Write(plugin/bin/**)",
                  "Edit(plugin/bin/**)",
                  "Write(.claude/**)",
                  "Edit(.claude/**)",
                  "Write(.github/workflows/**)",
                  "Edit(.github/workflows/**)",
                  "Read(./CLAUDE.user.md)",
                  "Read(./.env)",
                  "Read(./.env.*)"
                ]
              }
            }
          # Note what is ABSENT from `allow`, and why it is absent rather than forgotten: WebFetch and WebSearch.
          # The writer works from a checkout, `git`, `gh` and the build; nothing in this repository's work needs
          # the web. Withholding them removes the cheapest exfiltration verb an injected prompt can reach for —
          # one tool call, no subprocess. It does NOT eliminate egress: `Bash(make:*)` and `Bash(go:*)` execute
          # code (`go test` runs test binaries), so a hostile dependency can still open a socket. §1.1.1 states
          # the residual in full. Add either tool back only with a reason written on this line.
          #
          # Why each deny exists:
          #   plugin/bin/**        — VERSION and checksums.txt are produced ONLY by `make release` (CLAUDE.md).
          #                          An agent-written pin is a lie about what bytes ship. The deterministic
          #                          guard in review-pull-request.yml catches it if this is ever loosened.
          #   .claude/**           — the agent must not rewrite its own operating instructions, and agent memory
          #                          lives under .context/, never .claude/ (Rjae, dropped `record-memory`).
          #   .github/workflows/** — neither GITHUB_TOKEN nor an App token can push a change under that path
          #                          without the `workflows` permission; the reference observed the remote
          #                          rejection on 2026-08-02. Failing at the edit is a better error than failing
          #                          at the push, where the fixer looks green and lands nothing.
          #   CLAUDE.user.md/.env  — local-only credential files. They are gitignored and so absent from a fresh
          #                          checkout; the deny costs nothing and survives a mistake.
          # A permission pattern matches the command STRING, not a parsed argv: it is a guard rail, not a
          # security boundary (docs/security.md §5.1's whole-text matching caveat). The real boundaries are the
          # job's `permissions:` block and the token scopes.
          #
          # Note what the `allow` list is NOT doing: the general edit tools (Write, Edit, MultiEdit,
          # NotebookEdit) come from the action's OWN DEFAULTS, not from anything named here — which is how this
          # agent edits a Go file at all, when the only Write/Edit entries above are scoped to
          # .context/plans/**. The corollary matters for the reviewer in §4.2: withholding Write from an
          # `allow` list withholds nothing, so a read-only agent needs an explicit `deny` plus the job's
          # `permissions: contents: read`.

      # ---------------------------------------------------------------- fail-loud verifier 1
      # The fixer can end green without its push landing: a narrated-only fix, or a remote rejection. No push
      # means no `synchronize`, no re-review, and a silently parked PR. Fail LOUD instead.
      - name: Verify the fix was pushed
        if: ${{ !cancelled() && github.event_name == 'pull_request_review' && startsWith(github.event.review.user.login, 'claude') && steps.fix_gate.outputs.proceed == 'true' }}
        env:
          GH_TOKEN: ${{ github.token }}
          PR_NUMBER: ${{ github.event.pull_request.number }}
          REVIEWED_SHA: ${{ github.event.pull_request.head.sha }}
        run: |
          set -euo pipefail
          head_now=$(gh api "repos/$GITHUB_REPOSITORY/pulls/$PR_NUMBER" --jq '.head.sha')
          if [ "$head_now" = "$REVIEWED_SHA" ]; then
            echo "::error::Fix session ended without a push landing on the PR branch (head still at the reviewed SHA $REVIEWED_SHA) — no re-review will fire"
            exit 1
          fi

  # ------------------------------------------------------------------ brake 3: the token IS the switch
  # A SEPARATE JOB, and the separation is a security boundary rather than tidiness (§1.1.1 (iii), brigade-33's
  # H1). The PR must raise `pull_request: opened` so the reviewer runs, and GITHUB_TOKEN's writes raise no
  # workflow events — so these steps need the App installation token. Minting it in a later STEP of the agent job
  # would already be safe on the ordering argument (a secret is materialised only for the step that names it, and
  # the token would not exist until after the agent process had exited); putting it in a later JOB makes the
  # argument unnecessary — the agent's job never holds `BRIGADE_BOT_PRIVATE_KEY` at all, in any step, and no
  # future `if:` edit can quietly make it reachable.
  #
  # `needs: agent` also gives the sequencing for free: a FAILED or SKIPPED agent job skips this one, which is the
  # behaviour the close-the-issue step below depends on (a failed run must leave the issue open for a retry).
  open-pr:
    needs: agent
    if: github.event_name == 'issues'
    runs-on: blacksmith-4vcpu-ubuntu-2404
    timeout-minutes: 5          # a token mint, one branch lookup and one `gh pr create`; not a p90, a floor
    permissions:
      issues: write             # ONLY for the close-when-nothing-changed step, which uses GITHUB_TOKEN on purpose
    steps:
      - name: Mint an App installation token
        id: app-token
        uses: actions/create-github-app-token@v3      # verify: current major tag
        with:
          app-id: ${{ vars.BRIGADE_BOT_APP_ID }}
          private-key: ${{ secrets.BRIGADE_BOT_PRIVATE_KEY }}

      - name: Create PR
        id: create_pr
        env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          # GH_REPO: this job has no checkout, so gh has no git context to infer the repository from.
          GH_REPO: ${{ github.repository }}
          BRANCH: ${{ needs.agent.outputs.branch_name }}
          ISSUE_LABELS: ${{ toJson(github.event.issue.labels) }}
          TITLE: ${{ github.event.issue.title }}
          ISSUE_NUMBER: ${{ github.event.issue.number }}
        run: |
          set -euo pipefail
          # The REMOTE BRANCH, not the action output, decides whether there is anything to open a PR for: the
          # action reports a branch name even on runs where it pushed nothing. Only a definitive 404 counts as
          # no-changes — a transient API failure must fail loudly, or the close step below would wrongly close
          # the issue over a live branch.
          if [ -z "$BRANCH" ]; then
            echo "No branch name — the agent made no changes, skipping PR creation."
            echo "pr_created=false" >> "$GITHUB_OUTPUT"
            exit 0
          fi
          if resp=$(gh api "repos/${GITHUB_REPOSITORY}/branches/$BRANCH" 2>&1); then
            :
          elif grep -q "HTTP 404" <<<"$resp"; then
            echo "No pushed branch — the agent made no changes, skipping PR creation."
            echo "pr_created=false" >> "$GITHUB_OUTPUT"
            exit 0
          else
            echo "::error::Branch existence check failed (not a 404) — leaving the issue open for retry: $resp"
            exit 1
          fi
          # The commit-message rule, enforced where the title is MINTED rather than hoped for at merge time.
          # CLAUDE.md: `15: <Imperative summary>`; the squash merge takes its subject from this title because
          # squash_merge_commit_title=PR_TITLE is set on the repository (see the brief's §4.10). Issue titles
          # written by _create-claude-issue.yml are already imperative and unprefixed; a human's issue may
          # already carry the prefix, so do not double it.
          case "$TITLE" in
            "15: "*) PR_TITLE="$TITLE" ;;
            *)       PR_TITLE="15: $TITLE" ;;
          esac
          # Labels are copied from the ISSUE, which is where the policy decision was made:
          #   auto-merge  — set by the scheduled caller (documentation: yes; repository review: NO)
          #   maintenance — the execution-log-row exemption (owner ruling 4, 2026-09-09)
          LABELS=()
          if echo "$ISSUE_LABELS" | jq -e 'map(.name) | index("auto-merge")' >/dev/null 2>&1; then
            LABELS+=(--label auto-merge)
          fi
          if echo "$ISSUE_LABELS" | jq -e 'map(.name) | index("maintenance")' >/dev/null 2>&1; then
            LABELS+=(--label maintenance)
          fi
          gh pr create \
            --repo "$GITHUB_REPOSITORY" \
            --base master \
            --head "$BRANCH" \
            --title "$PR_TITLE" \
            --body "Closes #$ISSUE_NUMBER" \
            "${LABELS[@]}"
          echo "pr_created=true" >> "$GITHUB_OUTPUT"

      # A scheduled agent regularly finishes with nothing to change (docs already accurate, tree already clean).
      # That run pushes no branch, so no `Closes #N` PR ever arrives and the issue sits open until a human closes
      # it. The determination is STRUCTURAL — the agent ran to completion and opened no PR — so close on that
      # rather than on the wording of its comment. A FAILED agent job skips this whole job (`needs: agent`, no
      # `always()`), leaving the issue open for a retry. GITHUB_TOKEN on purpose: closing an issue must chain to
      # nothing — and it is this job's own token, not the App's, so the close raises no event even by accident.
      - name: Close issue when the agent had nothing to change
        if: steps.create_pr.outputs.pr_created == 'false'
        env:
          GH_TOKEN: ${{ github.token }}
          GH_REPO: ${{ github.repository }}
          ISSUE_NUMBER: ${{ github.event.issue.number }}
        run: |
          set -euo pipefail
          # The agent has `gh` access and may already have closed its own issue (or a human did mid-run);
          # closing an already-closed issue errors.
          state=$(gh issue view "$ISSUE_NUMBER" --json state --jq .state)
          if [ "$state" = "CLOSED" ]; then
            echo "Issue #$ISSUE_NUMBER is already closed — nothing to do."
            exit 0
          fi
          gh issue close "$ISSUE_NUMBER" \
            --reason completed \
            --comment "The agent finished this run without changes, so there is no PR to close this issue. Closing automatically — its comment above records what was reviewed. Reopen if the run looks wrong."
```

**brigade-specific deltas from the reference**

- **Runner label.** `blacksmith-4vcpu-ubuntu-2404`, not `ubuntu-latest`: the agent needs `docker` (shellcheck
  0.9.0 container), `gh`, `jq` and `shellcheck` — the image `ci.yml`'s inventory step actually prints.
- **Toolchain step.** `actions/setup-go@v7` with `go-version-file: go.mod` and the two-file cache key
  (`go.sum`, `tools.sum`), copied from `ci.yml`'s `fast` job, replaces the reference's `setup-dotnet`.
- **`make setup-lint` as a workflow step**, not an agent turn.
- **Actions pinned by major tag**, matching the three existing brigade workflows (the reference pins by SHA).
- **`concurrency` added** with `cancel-in-progress: false` — the reference has none on its hub, and a fixer
  cancelled between commit and push is exactly the silent park its verifier exists to prevent.
- **`allowed_bots` is a two-name list**, forced by the App-identity interaction (§1.2). The reference gets away
  with `"claude"` because its PRs are opened by a human's PAT.
- **App installation token replaces `secrets.CHECK_DEPENDENCIES`** on the one step that must chain. The App is
  repo-scoped and one-hour-lived; the PAT it replaces could push to `master` past protection from five cron
  workflows.
- **Two jobs, not one** (`agent` → `open-pr`, joined by `needs:` and a single `branch_name` output). The reference
  mints its token in a later step of the same job. Brigade splits it so the agent's job never holds
  `BRIGADE_BOT_PRIVATE_KEY` in any step — the barrier rather than the ordering argument (§1.1.1 (iii), and it is
  brigade-33's H1 that made the distinction worth writing down).
- **No `WebFetch`, no `WebSearch`** on the writer's allow list, where a naive port would have carried them. The
  reference grants both. §1.1.1 (iv) states the reason and the limit: it closes the easiest exfiltration verb,
  not the class, because `Bash(make:*)`/`Bash(go:*)` still execute code.
- **`claude-code-action` pinned by SHA** while every other action is pinned by major tag — the single exception to
  brigade's house style, and the reference's one convention this brief adopts wholesale (§4 conventions, 7.7).
- **`--model claude-opus-5 --max-turns 60`, `show_full_output: false`, `additional_permissions: actions: read`** —
  the reference sets none of these. `show_full_output` matters here specifically because this repository is public.
- **The PR title rule** (`15: ` prefix, idempotent) and the **`maintenance` label propagation** are new; the
  reference titles PRs `Issue #N: <title>`, which would violate `CLAUDE.md`'s commit-message format at squash time.
- **The `deny` list is new.** The reference has no deny list at all; brigade has four things an agent must never
  write and one it must never read.
- **`Write(.claude/memory/**)` is dropped** — agent memory lives under `.context/`, and `record-memory.yml` is not
  ported.

### 4.2 `.github/workflows/review-pull-request.yml` — the reviewer

```yaml
# The reviewer half of the loop. Read-only because the job's GITHUB_TOKEN is `contents: read` — that, and only
# that, is the boundary: a merge and a push are both content writes, so no tool the agent holds can perform one.
# The `deny` list below is the belt: an `allow` list that merely OMITS Write/Edit withholds nothing, because the
# action grants the general edit tools by default (see §4.1's note), so the denial has to be written out.
# A reviewer that can push is not a reviewer.
name: review pr

on:
  pull_request:
    types: [opened, synchronize, reopened]

# brake 4, first half. The autonomous fixer can push more than once per cycle; without cancellation each push
# would spawn a full reviewer session, most of them reviewing stale heads — and each stale CHANGES_REQUESTED
# verdict burns a slot of the fix cap for work already superseded.
concurrency:
  group: review-pr-${{ github.event.pull_request.number }}
  cancel-in-progress: true

jobs:
  review:
    # Same-repository PRs only. A fork PR cannot read `secrets.CLAUDE_CODE_OAUTH_TOKEN` (GitHub does not expose
    # secrets to fork PRs), so a reviewer job on one would fail at authentication on every drive-by contribution
    # and teach everyone to ignore a red X. Fork PRs get CI — after the "require approval for all outside
    # collaborators" gate — and a human reviewer.
    #
    # The dependabot clause is the same reasoning arriving by a different road, and it is NOT redundant: a
    # Dependabot branch lives in THIS repository, so the same-repo test above is TRUE for it. What a Dependabot
    # PR does not get is the repository's secrets — GitHub hands those runs a read-only GITHUB_TOKEN and the
    # separate Dependabot secret store — so `secrets.CLAUDE_CODE_OAUTH_TOKEN` resolves to the empty string and
    # the action fails at authentication. Without this clause `review pr` goes red on every grouped bump, once a
    # month, forever, and teaches exactly the ignore-the-red-X habit the fork clause exists to avoid.
    if: |
      github.event.pull_request.head.repo.full_name == github.repository &&
      github.actor != 'dependabot[bot]'
    runs-on: blacksmith-4vcpu-ubuntu-2404
    # NOT a measured p90 (no runs exist): a read-only review session with --max-turns 30 over a repository of
    # this size. Re-pin from ten real runs (P9-3).
    timeout-minutes: 30
    permissions:
      contents: read           # the reviewer must not be able to push. This is the boundary; the allowlist is the belt.
      pull-requests: write     # `gh pr review` needs it
      id-token: write          # claude-code-action's OIDC
      actions: read
    steps:
      - name: Checkout
        uses: actions/checkout@v7
        with:
          fetch-depth: 0       # the guard below diffs base...head against the MERGE BASE; a depth-1 clone has none

      # ---------------------------------------------------------------- brigade's fifth brake, deterministic
      # Three repository invariants that must never depend on an agent's judgement, checked before the agent
      # runs so a violating PR is never even reviewed (let alone approved and auto-merged):
      #
      #  (1) plugin/bin/VERSION and plugin/bin/checksums.txt are produced ONLY by `make release`, on master,
      #      by the owner (CLAUDE.md). A PR that touches either is claiming bytes nobody built.
      #  (2) go.mod's `go` line is a release-reproducibility pin: the release job rebuilds from the tag and
      #      diffs against the committed checksums, so bumping it changes the shipped bytes. Only a release
      #      bumps it (CLAUDE.md, `make checksums-check` rule (c)).
      #  (3) A protocol wire-shape change means internal/protocol, docs/protocol-v1.schema.json, the conformance
      #      suite and BOTH adapters in ONE commit. This check is the cheap half — the schema must move with the
      #      types; `make schema-check` in ci.yml is the real gate and the conformance suite is the rest.
      #
      # A human doing release work pushes straight to master and never sees this file.
      #
      # The BODY lives in scripts/ci/pr-guard.sh, not inline here, and that is a house rule rather than taste:
      # scripts/ci/README.md is built on a near-1:1 workflow-to-script relation, `make plugin-check`'s check 9
      # runs `shellcheck -s sh` over `scripts/ci/*.sh scripts/*.sh` only, the pinned shellcheck 0.9.0 container
      # rule is written against files, and ten Go drift tests open these scripts BY PATH. Twenty-five lines of
      # shell inlined in a workflow escapes all four.
      - name: Guard the release pins, the go line and the protocol join
        id: guard
        env:
          BASE_SHA: ${{ github.event.pull_request.base.sha }}
          HEAD_SHA: ${{ github.event.pull_request.head.sha }}
        run: |
          # `set -o pipefail` explicitly: Actions runs `run:` blocks as `bash -e {0}`, which does NOT set it, so
          # without this line `tee` would decide the step's exit status and a tripped guard would report green.
          set -o pipefail
          scripts/ci/pr-guard.sh "$BASE_SHA" "$HEAD_SHA" 2>&1 | tee "$RUNNER_TEMP/pr-guard.log"

      # Open question 7.8, ADOPTED (converged with brigade-33 2026-09-10) and landed here in P9-2 rather than
      # deferred: a tripped guard must SAY WHY on the pull request. Without this step the PR parks with a red X,
      # no review and no comment — and the next reader (agent or human) has to open a run log to learn that the
      # blocker is a repository invariant rather than a flaky check.
      #
      # `github.token`, deliberately, and it is the whole reason this is safe to add now: a GITHUB_TOKEN comment
      # raises no workflow-triggering event, so it CHAINS TO NOTHING — no `issue_comment` run, no fixer, no loop.
      # The body carries no '@claude' mention either, belt and braces (§4.1's clause would ignore it anyway,
      # since the comment's author is github-actions[bot], which the actor filter excludes).
      - name: Explain a guard trip on the PR
        if: ${{ failure() && steps.guard.outcome == 'failure' }}
        env:
          GH_TOKEN: ${{ github.token }}
          GH_REPO: ${{ github.repository }}
          PR_NUMBER: ${{ github.event.pull_request.number }}
        run: |
          set -euo pipefail
          # Written under RUNNER_TEMP, never under the project directory (CLAUDE.md), and read back with
          # --body-file so the error text needs no shell quoting.
          {
            echo "This pull request trips \`scripts/ci/pr-guard.sh\`, so the reviewer agent did not run and no"
            echo "review will be submitted. The guard checks three repository invariants that sit outside an"
            echo "agent's judgement (release pins, \`go.mod\`'s \`go\` line, the protocol join) — see CLAUDE.md."
            echo
            echo '```'
            sed 's/^::error:://' "$RUNNER_TEMP/pr-guard.log"
            echo '```'
          } > "$RUNNER_TEMP/pr-guard-comment.md"
          gh pr comment "$PR_NUMBER" --body-file "$RUNNER_TEMP/pr-guard-comment.md"
```

`scripts/ci/pr-guard.sh` (POSIX `sh`, because that is what `plugin-check.sh` lints these with):

```sh
#!/bin/sh
# Fails a pull request that touches one of three repository invariants CLAUDE.md places outside an agent's
# judgement. Called by .github/workflows/review-pull-request.yml BEFORE the reviewer agent runs, so a violating
# PR is never even reviewed. Usage: scripts/ci/pr-guard.sh <base-sha> <head-sha>
set -eu

BASE_SHA=${1:?base sha required}
HEAD_SHA=${2:?head sha required}

# THREE DOTS, and this is load-bearing. `git diff A B` (two dots) compares the two trees, so once `master` moves
# past a release the PR's diff against it shows plugin/bin/VERSION, plugin/bin/checksums.txt and the `go` line
# as changed — the exact three paths guarded below — and every open PR is falsely accused of changing a release
# pin it never touched, feeding the fixer agent an unfixable blocker. `git diff A...B` compares B against the
# MERGE BASE, i.e. what this PR actually changed. It is also why the checkout above needs fetch-depth: 0.
RANGE="$BASE_SHA...$HEAD_SHA"

files=$(git diff --name-only "$RANGE")
fail=0

if printf '%s\n' "$files" | grep -qE '^plugin/bin/(VERSION|checksums\.txt)$'; then
  echo "::error::This PR changes a release pin (plugin/bin/VERSION or plugin/bin/checksums.txt). Those files are produced only by \`make release\` on master — see CLAUDE.md."
  fail=1
fi

if git diff "$RANGE" -- go.mod | grep -qE '^[+-]go [0-9]'; then
  echo "::error::This PR changes go.mod's \`go\` line, which is a release-reproducibility pin. Only a release bumps it — see CLAUDE.md and \`make checksums-check\` rule (c)."
  fail=1
fi

if printf '%s\n' "$files" | grep -q '^internal/protocol/' && ! printf '%s\n' "$files" | grep -q '^docs/protocol-v1\.schema\.json$'; then
  echo "::error::This PR changes internal/protocol/ without docs/protocol-v1.schema.json. Protocol v1 is frozen: types, schema (\`make schema\`), the conformance suite and both adapters move in ONE commit."
  fail=1
fi

exit "$fail"
```

…and `review-pull-request.yml` continues, still inside `jobs.review.steps`:

```yaml
      - name: Review
        # THE ONE ACTION PINNED BY SHA, not by major tag — deliberately breaking the house style the other three
        # brigade workflows keep (§4's conventions; open question 7.7, adopted 2026-09-10). It is the only action
        # in this repository that runs a MODEL with repository write access, so a mutable tag moving under it
        # changes what an autonomous agent does, not merely how a step behaves.
        # verify: resolve the full 40-hex SHA of the current `v1` at implementation time and paste it here —
        #   gh api repos/anthropics/claude-code-action/git/ref/tags/v1 --jq '.object.sha'
        #   (an annotated tag needs one more hop: gh api .../git/tags/<sha> --jq '.object.sha')
        uses: anthropics/claude-code-action@<RESOLVE-AT-IMPLEMENTATION>   # v1
        with:
          # After the autonomous fixer pushes as claude[bot], the resulting `synchronize` run's ACTOR is that
          # bot; and a PR opened by our own App makes the `opened` run's actor brigade-bot. Without both names
          # here the action rejects the run and the loop dead-ends unreviewed. Explicit list, never '*'.
          allowed_bots: "claude,brigade-bot"
          claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          claude_args: "--model claude-opus-5 --max-turns 30"
          show_full_output: false
          prompt: |
            You are the reviewer agent. Read `.claude/agents/reviewer.md` for your
            operating instructions and review pull request
            #${{ github.event.pull_request.number }} in PR-review mode.

            You MUST end your session by actually executing
            `gh pr review ${{ github.event.pull_request.number }} --approve --body "<summary>"`
            or
            `gh pr review ${{ github.event.pull_request.number }} --request-changes --body "<findings>"`.
            Your final text response is discarded: a verdict not submitted via
            `gh pr review` does not exist, and this job fails if no review lands.
            If the submission command errors, retry it, and surface a persistent
            failure with `gh pr comment` before ending the session.

            When requesting changes, end the review body with the following
            line, written as plain text on its own line — do NOT wrap it in
            backticks or quotes, or the fixer's mention-detection regex will
            not match it:

            @claude Read `.claude/agents/developer.md`, then address every blocker in this review: apply the fixes directly on this PR branch and push.

            The `.claude/agents/developer.md` clause is not decoration: this
            trailer is the ONLY thing in the whole loop that hands the fixer
            its rules file. Nothing else in the fix path names it — the issue
            bodies of the two scheduled agents name documenter.md and
            reviewer.md, and a human's issue names nothing — so without it the
            gate list, the never-`make commit` rule and the pin prohibitions
            reach the writer through CLAUDE.md's short paragraph alone.

            Never put an @claude mention in an approval body — it would
            trigger a needless autonomous fix run.
          settings: |
            {
              "permissions": {
                "allow": [
                  "Bash(gh:*)",
                  "Bash(git:*)",
                  "Bash(ls:*)",
                  "Bash(cat:*)",
                  "Bash(find:*)",
                  "Bash(grep:*)",
                  "Bash(rg:*)",
                  "Bash(jq:*)"
                ],
                "deny": [
                  "Write",
                  "Edit",
                  "MultiEdit",
                  "NotebookEdit",
                  "Read(./CLAUDE.user.md)",
                  "Read(./.env)",
                  "Read(./.env.*)"
                ]
              }
            }
          # The four bare tool names in `deny` are the belt, and they have to be WRITTEN: the action grants the
          # general edit tools by default, so leaving them off the `allow` list withholds nothing (§4.1's note
          # says the same thing from the writer's side, where those defaults are what let it edit a Go file).
          # `Bash(gh:*)` includes `gh pr merge`. It cannot merge: the job's GITHUB_TOKEN is `contents: read`, and
          # a merge is a content write. That is why the permissions block above is the boundary and this list is
          # only the belt. No `make`, no `go`, no `docker`: a reviewer that can run the build can also write files
          # through a build script.
          # No WebFetch and no WebSearch either, and here the case is even plainer than for the writer (§4.1): a
          # reviewer reads a diff in a checkout it already has, and the OAuth token in its environment is
          # reachable to anything it runs (§1.1.1). Reviewing a PR whose diff is hostile is this agent's ENTIRE
          # job, so it is the one agent whose prompt is adversarial by construction — give it no fetch verb.

      # ---------------------------------------------------------------- fail-loud verifier 2
      # A reviewer session can end "successfully" without ever submitting the review (observed 2026-08-01 in the
      # reference's sibling repos: green check, zero reviews, auto-merge never armed). Fail loudly so a silent
      # no-op can never read as success.
      - name: Verify a review was submitted
        env:
          GH_TOKEN: ${{ github.token }}
          PR_NUMBER: ${{ github.event.pull_request.number }}
          REPO: ${{ github.repository }}
          HEAD_SHA: ${{ github.event.pull_request.head.sha }}
        run: |
          set -euo pipefail
          # One line per claude review AT HEAD: "STATE<TAB>has-trailer". Paginated so a long-lived PR (>100
          # reviews) cannot hide the newest review.
          reviews=$(gh api --paginate "repos/$REPO/pulls/$PR_NUMBER/reviews" \
            --jq ".[] | select((.user.login // \"\" | startswith(\"claude\")) and .commit_id == \"$HEAD_SHA\") | [.state, (.body // \"\" | contains(\"@claude\") | tostring)] | @tsv")
          if [ -z "$reviews" ]; then
            echo "::error::Reviewer session ended without submitting a formal review at head $HEAD_SHA"
            exit 1
          fi
          last=$(printf '%s\n' "$reviews" | tail -n 1)
          state=${last%%$'\t'*}
          has_trailer=${last##*$'\t'}
          # A CHANGES_REQUESTED verdict without the '@claude' trailer never arms the autonomous fixer — the PR
          # would silently park, so fail loud.
          if [ "$state" = "CHANGES_REQUESTED" ] && [ "$has_trailer" != "true" ]; then
            echo "::error::CHANGES_REQUESTED review at $HEAD_SHA lacks the @claude fixer trailer — the autonomous fix loop will not arm"
            exit 1
          fi
```

**brigade-specific deltas from the reference**

- **The same-repository `if:`** — new; the reference is a private repo where fork PRs are not a scenario.
- **The `github.actor != 'dependabot[bot]'` clause** — new, and it is a *correctness* clause, not a nicety.
  Dependabot's branches are in-repo, so the same-repo test passes them through; what fails is
  `secrets.CLAUDE_CODE_OAUTH_TOKEN`, which Dependabot-triggered runs cannot read. Without the clause the grouped
  monthly bump arrives with a red `review pr` every month for ever. §4.8 and drill (e) carry the same fact.
- **The deterministic guard step, before the agent** — new, and the single most brigade-specific thing in this
  brief. It encodes `CLAUDE.md`'s three hardest invariants as a diff check rather than as words in an agent file.
  Its body is `scripts/ci/pr-guard.sh`, not inline shell, so it stays inside `plugin-check.sh`'s shellcheck glob,
  the pinned-container rule and the Go drift tests (§6 adds its row to `scripts/ci/README.md`).
- **`fetch-depth: 0`** (the reference uses 1) — the guard diffs `base...head`, three dots, against the merge base.
- **`Bash(jq:*)` added, `Bash(sed:*)` withheld** — `jq` reads `gh api` output; `sed` is an editing verb and the
  reviewer edits nothing.
- **`--model claude-opus-5 --max-turns 30`, `show_full_output: false`** and the `deny` list — new. The deny list
  names `Write`, `Edit`, `MultiEdit` and `NotebookEdit` explicitly because omission from `allow` is not a denial.
- **The fixer trailer names `.claude/agents/developer.md`** — the reference's trailer does not, because its
  writer has no rules file to miss. Here it is the loop's only hand-off of the writer's operating instructions.
- **Prompt says "in PR-review mode"** because `.claude/agents/reviewer.md` carries two modes (§4.9.1).

### 4.3 `.github/workflows/auto-merge.yml`

```yaml
# Arms a merge when — and only when — the reviewer agent approved the CURRENT head of a PR that was labelled for
# auto-merge at creation time. It does not merge: `--auto` hands the PR to GitHub, which merges it when the
# branch ruleset's required checks (fast, macos, reproducibility, supabase) pass. The ruleset is what makes the
# word "auto" mean "when green" instead of "now" — see the brief's §1.2 blocker 3.
name: auto merge

on:
  pull_request_review:
    types: [submitted]

jobs:
  auto-merge:
    # brake 4, second half. The commit_id clause pins the approval to the CURRENT head: with the autonomous
    # fixer pushing to PR branches, an approval from a reviewer session that started on an older head must never
    # arm a merge for commits it never reviewed.
    #
    # The label clause is the policy switch, and for the first two months NOTHING sets it: `review-repository`
    # deliberately never does (owner's decision, workflows 5), and `update-documentation` starts at
    # `auto_merge: false` too, flipping to true once its diffs are boring (7.2, Rjae to confirm). So this job's
    # `if:` is expected to be false on every run at first — that is the intended state, not a broken workflow,
    # and drill (c) is what proves the mechanism works before the first real label is ever applied.
    if: |
      github.event.review.state == 'approved' &&
      startsWith(github.event.review.user.login, 'claude') &&
      contains(join(github.event.pull_request.labels.*.name, ','), 'auto-merge') &&
      github.event.review.commit_id == github.event.pull_request.head.sha
    runs-on: blacksmith-4vcpu-ubuntu-2404
    timeout-minutes: 5          # one token mint and one API call; not a p90, a floor
    # The job's own GITHUB_TOKEN does nothing here: the merge authenticates with the App token below. Declaring
    # an empty block rather than inheriting the default is the point — the reference's `contents: write` on this
    # job is vestigial and misleading.
    permissions: {}
    steps:
      # The merging identity MUST be the App and must NOT be a bypass actor of the master ruleset. An admin PAT
      # would bypass the required checks and merge a red PR while looking exactly like a working gate.
      - name: Mint an App installation token
        id: app-token
        uses: actions/create-github-app-token@v3      # verify: current major tag
        with:
          app-id: ${{ vars.BRIGADE_BOT_APP_ID }}
          private-key: ${{ secrets.BRIGADE_BOT_PRIVATE_KEY }}

      # --squash: the merge commit's subject must read `15: <Imperative summary>` (CLAUDE.md). That subject comes
      # from the PR TITLE because the repository is configured with squash_merge_commit_title=PR_TITLE (§4.10);
      # GitHub's default, COMMIT_OR_PR_TITLE, would use the single commit's own subject on a one-commit PR and
      # the rule would hold only by luck. The title itself is minted with the `15: ` prefix by claude.yml.
      #
      # The `||` fallback is not defensive noise, it is the COMMON path. `--auto` calls GitHub's
      # enablePullRequestAutoMerge mutation, which ERRORS with "Pull request is in clean status" when the PR is
      # already mergeable — and here it usually is: ci.yml's p90s are fast 5m16s, macos 3m02s, supabase 7m25s,
      # while a reviewer session runs longer than that, so by the time the approval lands all four required
      # checks are green. Without the fallback the everyday case is a red auto-merge job and a PR that never
      # merges, while drill (c)'s red-CI half passes and proves nothing about it. With it: arm if the PR is
      # still waiting, merge outright if it is already green — the ruleset has been satisfied either way.
      - name: Merge when green
        env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          PR: ${{ github.event.pull_request.number }}
          REPO: ${{ github.repository }}
        run: gh pr merge "$PR" --squash --auto --repo "$REPO" || gh pr merge "$PR" --squash --repo "$REPO"
```

**brigade-specific deltas from the reference**

- **App token instead of the PAT**, and the explicit statement of *why the merging identity must not be a bypass
  actor* — the reference merges with an admin PAT under branch protection with `enforce_admins` off, i.e. its
  auto-merge does not actually wait for anything the admin could bypass.
- **`permissions: {}`** instead of the reference's vestigial `contents: write` + `pull-requests: write`.
- **The `--auto || plain` fallback** — the reference has the bare `--auto`, and gets away with it because its
  reviewer approves faster than its CI finishes. Brigade's CI finishes first, so the already-clean case is the
  usual one and the bare form would error on it.
- **The squash-title mechanism is named**, because brigade has a commit-message rule and thinktech does not.

### 4.4 `.github/workflows/_create-claude-issue.yml` — the factory

```yaml
# Reusable factory: creates the issue that IS the work order. Every scheduled agent is fifteen lines because of
# this file. The issue body becomes the agent's prompt (claude.yml runs in tag mode), which is why the callers
# write agent-role prose into `body` rather than instructions into YAML: agent policy stays in committed files
# under .claude/agents/, reviewable in a PR, changeable without touching CI.
name: create claude issue

on:
  workflow_call:
    inputs:
      title:
        # FOLDED (`>-`), not a plain scalar: this prose contains "15: " and a colon-space inside a plain scalar
        # is a YAML syntax error ("mapping values are not allowed in this context") that would stop this whole
        # file loading — taking both scheduled workflows with it. Same rule for every description below.
        description: >-
          An imperative summary. claude.yml turns it into the PR title `15: <title>`, which becomes the squash
          commit subject — so write it the way the commit should read.
        required: true
        type: string
      body:
        required: true
        type: string
      auto_merge:
        description: Adds the `auto-merge` label, which auto-merge.yml requires. Default FALSE here — the
          reference defaults it to true, which is the wrong default for a repository whose owner is the only
          committer. Each caller opts in explicitly.
        required: false
        type: boolean
        default: false
      maintenance:
        description: Adds the `maintenance` label, which exempts the PR from the execution-log-row rule
          (owner ruling 4, 2026-09-09). Default true — a scheduled agent's work is maintenance by definition.
        required: false
        type: boolean
        default: true
    secrets:
      app_private_key:
        # FOLDED for the same reason as `title` above: "secrets: inherit" carries a colon-space.
        description: >-
          The GitHub App private key. The app id is read from the repository variable BRIGADE_BOT_APP_ID, which
          is not a secret. Secrets are passed EXPLICITLY, never `secrets: inherit`, so this workflow can reach
          exactly one credential and never CLAUDE_CODE_OAUTH_TOKEN.
        required: true

jobs:
  create-issue:
    runs-on: blacksmith-4vcpu-ubuntu-2404
    timeout-minutes: 5           # a token mint and one `gh issue create`; not a p90, a floor
    permissions: {}              # the App token does the write; the job token needs nothing
    steps:
      # The `issues: labeled` event MUST fire, or claude.yml never sees the work order and the scheduled agents
      # produce nothing but silent issues. GITHUB_TOKEN's writes raise no workflow events; an App installation
      # token's do. This is brake 3 in its constructive direction.
      - name: Mint an App installation token
        id: app-token
        uses: actions/create-github-app-token@v3      # verify: current major tag
        with:
          app-id: ${{ vars.BRIGADE_BOT_APP_ID }}
          private-key: ${{ secrets.app_private_key }}

      - name: Create issue
        env:
          GH_TOKEN: ${{ steps.app-token.outputs.token }}
          GH_REPO: ${{ github.repository }}
          TITLE: ${{ inputs.title }}
          BODY: ${{ inputs.body }}
          AUTO_MERGE: ${{ inputs.auto_merge }}
          MAINTENANCE: ${{ inputs.maintenance }}
        run: |
          set -euo pipefail
          LABELS=(--label claude)                                   # `claude` is what claude.yml's `if:` keys on
          if [ "$MAINTENANCE" = "true" ]; then LABELS+=(--label maintenance); fi
          if [ "$AUTO_MERGE" = "true" ]; then LABELS+=(--label auto-merge); fi
          gh issue create --title "$TITLE" --body "$BODY" "${LABELS[@]}"
```

**brigade-specific deltas from the reference**

- **`auto_merge` defaults to `false`**, not `true`. In the reference all five cron agents auto-merge; here exactly
  one does.
- **A `maintenance` input**, defaulting true — the execution-log exemption has no analogue in the reference.
- **The credential is an App private key + a public app-id variable**, not a general-purpose PAT under a
  misleading name (`CHECK_DEPENDENCIES`).
- **`permissions: {}` on the job** — the reference's callers pass their default token down for no reason.
- **`title` is documented as commit-subject-shaped**, because it becomes one.

### 4.5 `.github/workflows/update-documentation.yml`

```yaml
# Monthly structural-documentation refresh. SCOPED, deliberately: paths, make targets, versions and command
# names in the four documents a reader actually follows. Not prose, not reasoning, and explicitly NOT
# docs/security.md or docs/protocol-v1.md — those are argued documents whose sentences carry rulings, and an
# agent rewriting them for "accuracy" would be rewriting decisions. The gate is CI's own doc-witness tests
# (scripts/ci/setup_docs_test.go joins the five invocation forms across docs/setup.md, plugin/README.md and
# plugin/skills/setup/SKILL.md; `make test` runs it).
name: update documentation

on:
  schedule:
    - cron: '17 9 1 * *'    # monthly, 1st, 09:17 UTC. Off the hour on GitHub's own advice (keepalive.yml's
                            # header quotes it: scheduled runs are delayed at the top of the hour), and on a
                            # different minute and hour from review-repository's cron below — which is commented
                            # out for now (7.4) but keeps its slot, so re-enabling it needs no re-thinking of
                            # runner contention.
  workflow_dispatch:

jobs:
  # NO `timeout-minutes` here, and that is the one documented exception to CLAUDE.md's "every CI job sets
  # timeout-minutes": GitHub REJECTS `timeout-minutes` on a job whose body is `uses:` a reusable workflow. The
  # bound lives where the steps do — on `_create-claude-issue.yml`'s own `create-issue` job (5 minutes). The
  # same exception applies to review-repository.yml below and to the `notes:` job §6 adds to release.yml.
  create-issue:
    uses: ./.github/workflows/_create-claude-issue.yml
    secrets:
      app_private_key: ${{ secrets.BRIGADE_BOT_PRIVATE_KEY }}
    with:
      # FALSE for the first two months, then true. This CHANGES the earlier agreement (Rjae's workflows-item 4
      # said documentation PRs auto-merge) and is the converged recommendation of brigade-33's review,
      # 2026-09-10 — **Rjae to confirm** (open question 7.2). The reasoning: an auto-merged documentation PR
      # means the four in-scope documents can change on `master` with nobody reading them, gated only by CI's
      # doc-witness join, which pins five invocation FORMS and says nothing about prose. Two months is long
      # enough to see what the diffs actually look like. Flip this one word to `true` once they are boring, and
      # say so in the execution log when you do — that is the whole change, no other line moves.
      auto_merge: false
      maintenance: true     # exempt from the execution-log-row rule
      title: Refresh the structural documentation
      body: >-
        You are the documenter agent. Read `.claude/agents/documenter.md` for your operating instructions and
        follow them exactly. Scope: structural drift only — paths, `make` target names, version strings, command
        names and file layout — in `docs/setup.md`, `plugin/README.md`, `README.md` and `docs/adapter-authors.md`.
        Do not touch `docs/security.md` or `docs/protocol-v1.md`. Run `make test` before you push: the
        doc-witness tests are the gate on the three setup copies. If nothing is structurally stale, change
        nothing and say so — the workflow closes this issue automatically when no branch is pushed.
```

**brigade-specific deltas from the reference**

- **The scope is named in the body**, four files in and two files out. The reference's body is one sentence
  ("ensure repository documentation … reflects the current state") over a repo with no protected documents.
- **The gate is named**: `make test` runs the doc-witness join, so "did the agent break the three setup copies"
  is answered mechanically rather than by review.
- **Cron minute 17**, off the hour, matching `keepalive.yml`'s reasoning; the reference stacks two workflows on
  the same minute.
- **`auto_merge` is explicit** rather than inherited from a default — and it starts at **`false`** for the first
  two months (7.2, converged 2026-09-10, **Rjae to confirm**), where both the reference and Rjae's original
  workflows-item 4 would have it `true` from day one.
- **The `timeout-minutes` exception is written down**, not left as an apparent omission: a caller job that
  `uses:` a reusable workflow cannot carry one, so the bound sits on the called workflow's job. Reviewer.md's
  checklist line ("Every job sets `timeout-minutes`") carries the same exception.

### 4.6 `.github/workflows/review-repository.yml`

```yaml
# On-demand repository review (`workflow_dispatch` only — see the `on:` block). The reviewer agent reads the tree
# and opens a PR with MINIMAL fixes — and that PR carries no `auto-merge` label, so however green it goes it
# waits for Rjae. The loop still reviews it and still fixes its own blockers; only the merge is human (owner's
# decision, workflows 5).
#
# Note the standing state of play the agent must respect: the execution log records that there are no live rows
# and that the next substantial work is a second adapter, chartered separately — so this agent proposes fixes,
# never features, and never opens work.
name: review repository

on:
  # NO SCHEDULE — `workflow_dispatch` only, until the second adapter is chartered. This CHANGES the earlier
  # agreement (Rjae's workflows-item 5 said monthly) and is the converged recommendation of brigade-33's review,
  # 2026-09-10 — **Rjae to confirm** (open question 7.4). The reasoning is the execution log's own state of play:
  # there are no live rows and the tree is deliberately quiet, so a monthly pass has exactly two outcomes and
  # both are bad value — it no-ops, and costs an agent run a month for nothing, or it finds something to do in a
  # tree that is finished, which is worse than nothing because a human then reviews invented work.
  #
  # TO RE-ENABLE: uncomment the two lines below, and nothing else changes. The minute is chosen to stay five
  # hours clear of update-documentation.yml's 09:17 run so two agent sessions never contend for the runner.
  #   schedule:
  #     - cron: '43 4 1 * *'    # monthly, 1st, 04:43 UTC — off the hour (keepalive.yml's reasoning)
  workflow_dispatch:

jobs:
  create-issue:
    uses: ./.github/workflows/_create-claude-issue.yml
    secrets:
      app_private_key: ${{ secrets.BRIGADE_BOT_PRIVATE_KEY }}
    with:
      auto_merge: false     # deliberate: merging waits for Rjae
      maintenance: true
      title: Review the repository and apply minimal fixes
      body: >-
        You are the reviewer agent. Read `.claude/agents/reviewer.md` for your operating instructions and follow
        the **review-repository** mode exactly. Read `.context/plans/agent-memory/review-repository.md` for the
        commit you last reviewed and take your scope from it. Make precise, minimal corrections only; propose
        nothing new. Run `make typecheck tidy-check lint build test deps-check schema-check plugin-check` before
        you push, and update the memory marker in the same commit. Never touch `plugin/bin/**`, never bump
        `go.mod`'s `go` line, and keep `docs/allowed-deps.txt` zero-diff. If the tree is already correct, change
        nothing and say so — the workflow closes this issue automatically when no branch is pushed.
```

**brigade-specific deltas from the reference**

- **`auto_merge: false`.** The reference sets `auto_merge: true` on this exact workflow — an agent reviewing the
  whole repository and merging its own conclusions unattended. Rjae's ruling reverses it.
- **Agent memory lives at `.context/plans/agent-memory/review-repository.md`**, inside the tree the agent may
  write, so the whole `record-memory.yml` workflow that exists in the reference purely to work around the
  `.claude/**` write guard is **not ported**. (The reference's own survey says as much: "If your agent memory
  lives outside a protected path … you do not need it at all — that is the better default.")
- **The gate list is spelled out** — brigade has eight named gates and the reference has `make build`.
- **The "no live rows" state is quoted into the body** so the agent does not invent a workstream.
- **No cron at all, for now** — the reference runs this one monthly and so did this brief's v2. `workflow_dispatch`
  only until the second adapter is chartered (7.4, converged 2026-09-10, **Rjae to confirm**); the cron block is
  kept, commented, with the two-line re-enable instruction beside it.

### 4.7 `.github/workflows/release-notes.yml`

```yaml
# When a release is published, write its notes from the repository's own record and tell members to update.
# Read-only except for two writes: `gh release edit --notes` and one `gh issue create`.
#
# ---------------------------------------------------------------------------------------------------------
# READ THIS BEFORE RELYING ON THE `release` TRIGGER.
# release.yml publishes the draft with `gh release edit --draft=false` authenticated by `${{ github.token }}`,
# and GitHub does not raise workflow-triggering events for actions taken with GITHUB_TOKEN. So on today's
# release.yml the `release: published` trigger below FIRES NEVER — silently, exactly the failure mode brake 3
# is about, pointing the other way.
#
# Three remedies, in the brief's order of preference (§4.7 deltas):
#   (a) RECOMMENDED — release.yml gains a second job `needs: goreleaser` that calls this workflow through
#       `workflow_call`. No event needed, no new credential, and the notes are written by the same run that
#       published the tag. It DOES cost one edit beyond the added job: a called workflow's job may request only
#       permissions the CALLER already holds, and release.yml today grants `contents: write` and nothing else,
#       so the `notes` job's `issues: write` and `id-token: write` would fail validation outright ("is
#       requesting 'issues: write', but is only allowed 'issues: none'"). release.yml's top-level `permissions:`
#       block must be widened to all three — see §6, where the widened block is shown.
#   (b) release.yml's Publish step authenticates with the App installation token instead of github.token, so
#       the publish raises a real `release: published`. One more credential on the release path; the release
#       path is the one place this repository has been most conservative.
#   (c) leave the trigger unfired and run this workflow by hand (`workflow_dispatch`) after each release.
# All three triggers are declared below so whichever the owner picks needs no edit here. Drill (g) proves it on
# a pre-release tag.
# ---------------------------------------------------------------------------------------------------------
name: release notes

on:
  release:
    types: [published]
  workflow_call:
    inputs:
      tag:
        required: true
        type: string
    secrets:
      # Declared EXPLICITLY so the caller in §6 can pass this one credential and only this one. The alternative
      # — `secrets: inherit` on the caller — would hand this workflow every repository secret it has, including
      # BRIGADE_BOT_PRIVATE_KEY, which is the App's write credential and has no business on the release path.
      # §4.4 states the same rule for the issue factory; this is that rule applied here.
      claude_code_oauth_token:
        required: true
  workflow_dispatch:
    inputs:
      tag:
        description: The tag to write notes for, e.g. v0.5.0
        required: true
        type: string

jobs:
  notes:
    runs-on: blacksmith-4vcpu-ubuntu-2404
    timeout-minutes: 20         # not a p90; a bounded read-only session plus two API writes
    permissions:
      contents: write           # `gh release edit --notes` is a contents write
      issues: write             # the one announcement issue
      id-token: write
    steps:
      - uses: actions/checkout@v7
        with:
          fetch-depth: 0        # the agent reads `git log <previous tag>..<tag>`; a shallow clone has no tags

      - name: Write the notes and announce
        # THE ONE ACTION PINNED BY SHA, not by major tag — deliberately breaking the house style the other three
        # brigade workflows keep (§4's conventions; open question 7.7, adopted 2026-09-10). It is the only action
        # in this repository that runs a MODEL with repository write access, so a mutable tag moving under it
        # changes what an autonomous agent does, not merely how a step behaves.
        # verify: resolve the full 40-hex SHA of the current `v1` at implementation time and paste it here —
        #   gh api repos/anthropics/claude-code-action/git/ref/tags/v1 --jq '.object.sha'
        #   (an annotated tag needs one more hop: gh api .../git/tags/<sha> --jq '.object.sha')
        uses: anthropics/claude-code-action@<RESOLVE-AT-IMPLEMENTATION>   # v1
        with:
          allowed_bots: "claude,brigade-bot"
          # One expression covers all three triggers: secret names are case-insensitive in Actions, so this
          # resolves to the `claude_code_oauth_token` the caller passes under `workflow_call`, and to the
          # repository secret of the same name under `release` and `workflow_dispatch`.
          claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
          claude_args: "--model claude-opus-5 --max-turns 20"
          show_full_output: false
          # The tag expression `github.event.release.tag_name || inputs.tag` appears three times in the prompt
          # below and is repeated rather than hoisted into `env:`: `github.event.release.tag_name` is set on the
          # `release` trigger and `inputs.tag` on the other two, and a PROMPT is not a shell — it cannot expand
          # an environment variable, so an `env: RELEASE_TAG:` block here would be set and never read.
          prompt: |
            Release ${{ github.event.release.tag_name || inputs.tag }} of brigade has been published.

            Write its release notes and announce it. Exactly this, and nothing else:

            1. Read the section of `CHANGELOG.md` for this version. It is the authority on what changed and why;
               do not restate the commit log over it.
            2. Read `git log --oneline <previous tag>..${{ github.event.release.tag_name || inputs.tag }}` for
               anything the changelog omitted, and `gh release view <tag> --json assets` for the assets that
               actually shipped.
            3. Compose notes: what changed for a USER of the plugin, then anything an ADAPTER AUTHOR must know,
               then the assets and their checksums file. Keep the changelog's own wording where it exists.
               Never invent a change that is not in the changelog or the log.
            4. Publish them with `gh release edit <tag> --notes "<the notes>"`. Do not use --notes-file (no
               temporary files under the project directory) and do not touch --draft or --latest: release.yml
               owns the publish state.
            5. Open ONE issue titled `<version> is out — run /brigade:update` whose body tells members the one
               command they run and links the release. Nothing else; do not label it `claude` (it is an
               announcement, not a work order, and the `claude` label would start an agent on it).

            Do not edit any file in the repository. Do not push. Do not open a pull request.
          settings: |
            {
              "permissions": {
                "allow": [
                  "Bash(git log:*)",
                  "Bash(git show:*)",
                  "Bash(git tag:*)",
                  "Bash(gh release view:*)",
                  "Bash(gh release list:*)",
                  "Bash(gh release edit:*)",
                  "Bash(gh issue create:*)",
                  "Bash(ls:*)",
                  "Bash(cat:*)",
                  "Bash(grep:*)",
                  "Bash(rg:*)",
                  "Bash(jq:*)"
                ],
                "deny": [
                  "Read(./CLAUDE.user.md)",
                  "Read(./.env)",
                  "Read(./.env.*)"
                ]
              }
            }
          # No WebFetch and no WebSearch here either (§1.1.1 (iv)) — this list never had them, and now that is a
          # stated rule rather than an accident: the notes are composed from CHANGELOG.md, `git log` and
          # `gh release view`, all of which are already in the checkout or behind the job's own token.
          # The two writes are the ONLY two `gh` verbs on the allowlist that write, and the job's token grants
          # exactly the two scopes they need. No App token here: neither `gh release edit` nor `gh issue create`
          # has to fire a downstream workflow — the announcement issue deliberately must NOT (it carries no
          # `claude` label, and a GITHUB_TOKEN-made issue raises no event either way, which is belt and braces).
```

**brigade-specific deltas from the reference**

- **The reference has no analogue at all** — this workflow is new, so "delta" means "designed against brigade's
  facts": `CHANGELOG.md` is written at release prep by convention (P7-9), the release model is tag-based and a
  published tag is never moved, and `plugin/bin/checksums.txt` is the artefact users verify.
- **The GITHUB_TOKEN publish problem is stated in the file itself**, with the recommended remedy (a) — a
  `workflow_call` job in `release.yml`. Everything else in this brief adds files; this is the one place a change
  to an existing workflow is recommended, and it is one job **plus one widened `permissions:` block**: a called
  workflow inherits the caller's ceiling, and `release.yml`'s current `contents: write` cannot grant this
  workflow the `issues: write` and `id-token: write` its job asks for. §6 shows the widened block.
- **`workflow_call.secrets.claude_code_oauth_token` is declared**, so the caller passes exactly one credential
  and `secrets: inherit` — which would hand the release path the App's private key — is never needed.
- **No App token**, unlike every other write in this brief, and the file says why.
- **The announcement issue must not carry the `claude` label** — in a repo where a label is a work order, an
  announcement that gets labelled starts an agent.

### 4.8 `.github/dependabot.yml`

```yaml
# Dependency bumps are Dependabot's job, not an agent's: a bump is mechanical, and the interesting part — the
# call site that no longer compiles — is exactly what the loop's fix cycle is for. So Dependabot opens the PR
# and, if `make build test deps-check tidy-check` breaks, the reviewer requests changes and the fixer adapts
# the call sites on that same branch.
#
# THREE brigade rules bound what may be bumped, and they are enforced by CI, not by this file:
#   * `go.mod`'s `go` line is a release-reproducibility pin — Dependabot does not touch it (it bumps `require`
#     directives, not the `go` directive) and the guard in review-pull-request.yml fails any PR that does.
#   * `docs/allowed-deps.txt` lists the five modules the SHIPPED binary may link — coder/websocket,
#     x/crypto/x509roots/fallback, x/sys, x/term, x/text — and `make deps-check` fails on any change to that
#     set. A bump inside the five is fine; a bump that pulls a new module into the binary is a red build and a
#     human decision. Everything else in go.mod (pgx, jsonschema, go-internal …) is test- or tooling-side:
#     `deps-check` says nothing about it, and `make build test` is its gate.
#   * A Go source change ships NOTHING until `make release`. `make checksums-check` rule (c) stays green through
#     the published-release arm, and the plugin keeps serving the last released binary. That is the design, not
#     a defect — drill (f) records it.
version: 2
updates:
  # ---------------------------------------------------------------------------------------------- Go modules
  - package-ecosystem: gomod
    directory: "/"
    schedule:
      # No `day:` — it is meaningful only under `interval: weekly`; under `monthly` Dependabot ignores it and
      # may warn on the config. The time is simply arbitrary (Dependabot does not run on Actions runners, so
      # keepalive.yml's off-the-hour queue reasoning does not apply to it), chosen only to keep the two
      # ecosystems ten minutes apart.
      interval: monthly
      time: "09:37"
      timezone: Etc/UTC
    open-pull-requests-limit: 3
    # The commit subject and the PR title both take this prefix. The squash merge takes the PR TITLE
    # (squash_merge_commit_title=PR_TITLE, §4.10), so the merged subject reads `15: bump ...`.
    # NO `include: scope`. Dependabot composes `<prefix><scope>: <message>`, so `prefix: "15:"` with
    # `include: scope` yields `15:(deps): bump …` — the space after brigade's ticket colon is gone, and because
    # the squash subject comes from the PR title that malformed subject lands on master. The `dependencies`
    # label already says what the PR is; the scope buys nothing and costs the commit format.
    # NOTE, and it is a real wart, now an ACCEPTED one: Dependabot lowercases the verb when a prefix is set,
    # giving `15: bump github.com/x from 1 to 2` where CLAUDE.md asks for `15: <Imperative summary>` with a
    # capital. Open question 7.6(c), converged with brigade-33 2026-09-10 and adopted: ACCEPT it. The alternative
    # was a fifth workflow file whose entire job is to capitalise one letter; the ticket prefix — the part that
    # makes the log greppable — is present and correct, and a Dependabot subject is a machine string.
    commit-message:
      prefix: "15:"
    labels:
      - maintenance          # the execution-log-row exemption
      - dependencies
    groups:
      # One PR a month for the whole minor/patch surface: nine direct modules and their indirects, all of which
      # go through the same `make build test deps-check tidy-check` gate anyway. Nine separate PRs would mean
      # nine full CI matrices (each with the 7-minute `supabase` job) for one afternoon's work.
      go-minor-and-patch:
        patterns: ["*"]
        update-types: ["minor", "patch"]
    ignore:
      # Majors are excluded from the grouped PR and from Dependabot entirely. For `coder/websocket` — the one
      # module of the interesting three that is actually LINKED INTO THE SHIPPED BINARY (`go list -deps
      # ./cmd/brigade`; it is one of the five in docs/allowed-deps.txt) — a major bump is a design decision
      # about what ships, gated by `make deps-check`. For pgx and jsonschema, which are test- and tooling-side
      # and appear in no `deps-check` list, a major is a design decision about the adapter's own surface, gated
      # by `make build test`. Either way it is a decision, not a bump: Rjae takes majors by hand.
      - dependency-name: "*"
        update-types: ["version-update:semver-major"]

  # ------------------------------------------------------------------------------------------ GitHub Actions
  - package-ecosystem: github-actions
    directory: "/"           # covers every file under .github/workflows/
    schedule:
      interval: monthly        # again no `day:` — weekly-only — and the time is arbitrary
      time: "09:47"
      timezone: Etc/UTC
    open-pull-requests-limit: 3
    commit-message:
      prefix: "15:"
    labels:
      - maintenance
      - dependencies
    groups:
      actions:
        patterns: ["*"]
    # No `ignore` here: this repository pins actions by MAJOR TAG (checkout@v7, setup-go@v7, upload-artifact@v7,
    # download-artifact@v8), so within a major there is nothing for Dependabot to bump and what it will actually
    # propose is the next major — which is precisely the change worth a human's attention. `supabase/setup-cli`
    # and `goreleaser-action` additionally carry an exact `version:` INPUT (2.116.0, v2.18.0) that Dependabot
    # does not see; those stay manual, and release.yml's comment explains why the goreleaser version is pinned
    # to what `make release` rehearses with.
```

**Auto-merge label: no. Deliberate, and here is the reasoning.**

Rjae's decision 2 asks for `auto-merge` on patch/minor of allow-listed modules and not on the `go` directive or
majors. That is the right policy; the mechanism cannot express it, for two independent reasons, and one of them
also breaks "Dependabot PRs go through the loop":

1. **`labels:` in `dependabot.yml` is static per ecosystem entry.** It cannot be conditioned on update type. The
   grouped PR is already restricted to minor+patch by `groups.update-types` and majors are ignored outright, so
   "patch/minor only" is achieved — but the group can still contain a module *outside* `docs/allowed-deps.txt`
   (an indirect, or a dev-only module), and `make deps-check` is what decides that, at CI time, not at label time.
2. **Workflows triggered by a Dependabot PR do not get the repository's secrets** — they get a read-only
   `GITHUB_TOKEN` and, separately, Dependabot secrets. So `review-pull-request.yml` on a Dependabot PR cannot
   read `CLAUDE_CODE_OAUTH_TOKEN` and its job token cannot submit a review. On top of that, `allowed_bots` is
   `"claude,brigade-bot"` — `dependabot` is not on it, and the action rejects the run before any of that matters.

   **Note carefully what does *not* stop it: the same-repository `if:`.** Dependabot's branches live in this
   repository, so `head.repo.full_name == github.repository` is TRUE and the job would start, resolve an empty
   token and go red — once a month, for ever, on a PR nobody has done anything wrong to. That is why
   `review-pull-request.yml`'s gate carries `&& github.actor != 'dependabot[bot]'` (§4.2): the reviewer agent is
   **skipped by the actor filter**, deliberately and visibly, rather than reached and failed. With no claude
   approval `auto-merge.yml`'s `if:` never fires either, so the label would be inert regardless.

So: **Dependabot PRs are CI-gated and merged by Rjae**, labelled `maintenance` (row-exempt) and `dependencies`.
The four CI checks are exactly the gate that matters for a bump.

**Open question 7.6 is closed — converged with brigade-33 2026-09-10, adopted.** All three sub-questions land on
the cheapest answer, and none of them adds a file:

- **(a) Majors stay excluded from the group and are handled by hand.** `ignore` keeps them out of the monthly
  grouped PR; when a major matters, a human raises it. A separate ungrouped major PR would just be a red build
  arriving on a schedule.
- **(b) Monthly manual merge; no `dependabot-auto-merge.yml`.** The five-line `pull_request_target` file that
  earlier drafts floated as option (i) is **not planned** and has been removed from this brief and from §6's P9-4
  row. One grouped PR a month, merged by the person who is already the only committer, does not justify a fourth
  trigger surface — and `pull_request_target` is the one trigger type on a public repository that deserves a
  reason better than convenience.
- **(c) The lowercase verb is accepted.** `15: bump x from a to b` is what `commit-message.prefix: "15:"`
  produces, and it is not worth a fifth workflow to capitalise a `b`. `CLAUDE.md`'s imperative-capital convention
  is a rule for human and agent commits; a Dependabot subject is a machine string, and the ticket prefix — the
  part that makes the log greppable — is present and correct.
- Not adopted, and recorded so it is not re-proposed: adding `dependabot` to `allowed_bots` and storing the OAuth
  token as a **Dependabot secret** so the reviewer agent runs on those PRs too. Most power, least benefit — an
  agent reviewing a version-number diff.

**`tools.mod` is not covered by any of this.** It is a second module file (`module github.com/appshapes/brigade/tools`,
six dev tools) that Dependabot's gomod ecosystem does not discover, because it is not named `go.mod`
(**verify:** whether the deployed Dependabot has learned Go tool-module support since; if it has, add a third
`updates:` entry with `directory: "/"` and the file named). Until then it is kept current by the **`review-repository` agent on its
`workflow_dispatch` runs** (there is no cron until 7.4 is settled — run it by hand when a tool bump matters), whose body already lists the gates: one line is added to
`.claude/agents/reviewer.md`'s review-repository checklist — "check `tools.mod` against upstream releases; a bump
there is dev-tooling only, never shipped, and `make lint typecheck test` is its gate". That keeps it inside a PR
that waits for Rjae, which is the right blast radius for a tool that reformats and vets the whole tree. (Two
alternatives, neither preferred and neither an open question: a `make tools-update` target the agent runs, or a
fifth cron caller of the factory. Both add machinery for six dev tools that change a few times a year.)

**brigade-specific deltas from the reference**

- **The reference has no `dependabot.yml` at all**; it runs a monthly `update-dependencies` agent with a NuGet
  prompt. Rjae's decision replaces the agent with the ecosystem tool and keeps the agent only for the *fix*
  cycle, which is the half an agent is actually good at.
- **Majors ignored, minors/patches grouped, three-PR cap** — sized to a repository whose CI matrix costs a
  7-minute Docker job per PR.
- **`commit-message.prefix: "15:"`** exists only because of `CLAUDE.md`'s ticket-prefix rule.

### 4.9 The agent instruction files

Committed under `.claude/agents/`. The action restores `.claude/`, `CLAUDE.md` and friends from the **PR base
branch** before running, so these files are always the reviewed versions on `master`, never whatever a PR proposes
— which is also why the writer's `deny` list forbids editing them.

#### 4.9.1 `.claude/agents/reviewer.md`

```markdown
---
name: reviewer
description: Reviews brigade pull requests against the repository's invariants and either approves or requests
  changes with specific, actionable blockers. Also runs the review-repository pass.
model: opus
color: red
tools: Read, Write, Edit, Bash, Grep, Glob
---

<!-- Write and Edit are here for the review-repository mode, which makes precise, minimal corrections, updates
     the memory marker and pushes a branch — it cannot do any of that without them. They are NOT a hole in the
     PR-review mode's read-only promise: that promise rests on the WORKFLOW boundary, `permissions: contents:
     read` in review-pull-request.yml, plus that file's explicit `deny` of Write/Edit/MultiEdit/NotebookEdit.
     A tool merely absent from an allowlist is not withheld; a job token that cannot write is. -->


You are the code reviewer for **brigade** — a Go CLI and Claude Code plugin that carries messages between the
Claude Code sessions of different people. Thorough, concise, every comment actionable.

Read `CLAUDE.md` first, every time. It is the authority; this file does not restate it, it tells you what to do
with it. Also read `.context/plans/brigade-execution-log.md` for where the work stands — as of 2026-09-09 there
are no live rows and the next substantial work is a second adapter, chartered separately. Nothing you review
should be opening new workstreams.

## Modes

**PR review** (the `review pr` workflow). Read the intent (`gh pr view`) and the diff (`gh pr diff`), plus enough
surrounding code to judge it. Approve with `gh pr review <n> --approve --body "<summary>"` or request changes
with `gh pr review <n> --request-changes --body "<findings>"`.

- Your final text response is discarded. A verdict not submitted through `gh pr review` **does not exist**, and
  the job fails if no review lands at the current head SHA. If the command errors, retry it; if it keeps failing,
  say so with `gh pr comment` before you end the session.
- End every request-changes body with this line, as plain text on its own line, never wrapped in backticks or
  quotes — the fixer's mention detection requires the bare form:

  @claude Read `.claude/agents/developer.md`, then address every blocker in this review: apply the fixes directly on this PR branch and push.

  Do not shorten it. The `.claude/agents/developer.md` clause is the only place in the entire loop where the
  fixer is told which rules file governs it: drop the clause and the gate list, the never-`make commit` rule
  and the release-pin prohibitions reach the writer through `CLAUDE.md`'s one paragraph and nothing else.

- **Never put an @claude mention in an approval body.** It starts a needless fix run and can race an arming
  auto-merge.
- Approve only what you would merge. When unsure, request changes and say precisely what would settle it.

**review-repository** (the on-demand workflow). Read
`.context/plans/agent-memory/review-repository.md` — fields `Date`, `Commit`, `Scope`. **If the file is absent,
treat it as `Commit: (none)` and create it in this commit** — the first run finds nothing there. If
`Commit` is `(none)`, your scope is the whole tree; otherwise your scope is `git diff --name-only <Commit>..HEAD`. Make **precise,
minimal corrections only** — never features, never refactors, never new files outside your memory marker. Update
the marker in the same commit. Your PR carries the `maintenance` label and **no** `auto-merge` label: it waits for
Rjae, by design. Also check `tools.mod` against upstream releases (dev tooling only, never shipped; `make lint
typecheck test` is its gate).

## What to check, in this order

1. **The invariants below.** Any one of them broken is a blocker, full stop, however good the change is.
2. **Correctness.** Does it do what the PR says? Logic errors, boundary cases, error paths that swallow.
3. **Security.** This is a messaging tool: sanitisation, provenance and the trust boundary are the sharp edges.
   No secret on argv, in a log, in `describe` output, or in a file under the project directory. Never weaken
   a refusal or a policy check without a ruling to cite.
4. **Tests.** A behaviour change with no test that could fail is not done. Negative tests matter more than
   positive ones here.
5. **Simplicity.** The simplest thing that a maintainer can support; no speculative abstraction; nothing beyond
   what the issue asked for.

## Brigade invariants — the blocker checklist

- **Commits and merges.** Message format `15: <Imperative summary>`. **Merges only, never rebase**; no force
  push; no rewritten history. On a merge conflict the PR is handed back to a human, not resolved by rebase.
- **Release pins.** `plugin/bin/VERSION` and `plugin/bin/checksums.txt` are produced only by `make release`.
  A PR that touches either is a blocker. (The workflow's guard step also fails the run — if you are reading a
  diff that contains one, something upstream is wrong; say so.)
- **The `go` line.** `go.mod`'s `go` directive is a release-reproducibility pin. Never bumped in a PR.
- **`docs/allowed-deps.txt`.** The shipped binary links exactly those five modules; `make deps-check` is the
  gate. A new module in the binary is a human decision, not a PR.
- **Protocol v1 is frozen.** A wire-shape change means `internal/protocol` (types and `Validate`),
  `docs/protocol-v1.schema.json` (`make schema`), the conformance suite and **both** adapters, in one commit.
  Anything less is a blocker.
- **Output discipline.** Never write to stdout from a command, the watcher or an adapter except protocol
  JSON/NDJSON or the documented human output (forbidigo enforces it); diagnostics go to stderr through the
  redacting logger; never `slog.Any`. Never spawn through a shell — argument arrays with an allow-listed
  environment.
- **Secrets.** Nothing on argv, in logs, or in files under the project directory. The join secret comes from
  stdin, a no-echo prompt, or a `--secret-file` outside the repository. The Supabase secret/service-role key
  must not appear in the adapter, the plugin, the repository or any CI variable that ships.
- **Config paths.** Never hardcode `~/.claude`; it is `CLAUDE_CONFIG_DIR ?? ~/.claude`. Never read or copy
  `$CLAUDE_CONFIG_DIR/sessions/*.key`. The session registry JSON is read best-effort and never written.
- **Layering.** `internal/harness` must not import `internal/adapters/supabase` (depguard). The plugin speaks
  only the adapter protocol and spawns the bundled adapter as a child process.
- **Tests stay Docker-free.** `make test` must not need Docker or a stack; live Supabase tests are opt-in behind
  `BRIGADE_TEST_LIVE=1`, which only `make test-integration` sets.
- **CI hygiene.** Every job sets `timeout-minutes` — with one exception GitHub forces: a job whose body is
  `uses:` a reusable workflow cannot carry one, and the bound lives on the called workflow's job instead. No
  constant-false gate in `ci.yml` — gate on a real condition
  or do not add the step. Scripts are never inlined into `make` recipes: Go drift tests open them by path.
- **Shell files.** Any change under `scripts/` must have been run through
  `docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:v0.9.0 -s sh <file>` as well as the local
  shellcheck — CI's runner has 0.9.0 and this machine has 0.11, and they disagree.
- **Layout.** Plans in `.context/plans/`; scratch in `.ignored/`; experiment reports in `docs/experiments/` with
  drivers in `scripts/experiments/`; research digests in `docs/research/`.
- **The execution log.** Normally a row lands in the same commit as the work. **Exception (owner ruling,
  2026-09-09): a PR labelled `maintenance` is exempt.** Do not request a log row on a `maintenance` PR, and do
  request one on any PR that is not.

## Guardrails

- Never approve a change that breaks an invariant above, weakens a security refusal, puts a secret anywhere it
  must not be, or changes a wire shape without its four companions.
- Do not comment on style the repository does not have a rule about.
- You are read-only in PR-review mode: no `make`, no `go`, no writes — enforced by the workflow, not by you
  (`permissions: contents: read` on the job, plus its explicit `deny` of the edit tools). If a claim needs a
  build to settle, say what you could not verify and let CI decide. In review-repository mode you do write, and
  there the workflow grants it.
```

#### 4.9.2 `.claude/agents/documenter.md`

```markdown
---
name: documenter
description: Keeps brigade's structural documentation — paths, make targets, versions, command names — in step
  with the tree. Runs monthly through the update-documentation workflow.
model: opus
color: green
tools: Read, Write, Edit, Bash, Glob, Grep
---

You are the documentation agent for **brigade**. You fix **structural drift** and nothing else.

Read `CLAUDE.md` first, every time.

## In scope — exactly four files

`docs/setup.md`, `plugin/README.md`, `README.md`, `docs/adapter-authors.md`.

## Forbidden — two files, no exceptions

- **`docs/security.md`** — an argued document whose sentences carry owner rulings and residual-risk statements.
  Changing a word there changes a decision.
- **`docs/protocol-v1.md`** — the frozen protocol. It moves only with `internal/protocol`, the schema, the
  conformance suite and both adapters, in one commit, by a human.

If you believe either is wrong, say so in your PR body. Do not edit it.

## What "structural drift" means

Only facts about the tree that a reader would follow and find false:

- file and directory paths that no longer exist, or that moved;
- `make` target names, flags and usage lines that no longer match the `Makefile`'s help text;
- command names, subcommands and flags that no longer match the CLI;
- version strings and platform lists that no longer match `plugin/bin/VERSION`, `plugin.json` or the release;
- references to a script that was renamed, or to a workflow or job that no longer exists.

**Not** in scope: prose style, reasoning, ordering, tone, new sections, examples you think would be nice, or
anything you cannot demonstrate is false about the current tree.

## Workflow

1. Read the four in-scope files.
2. Establish the current facts from the tree — `make help`, `ls`, the CLI's own usage, `plugin/bin/VERSION`,
   `.github/workflows/`, `scripts/ci/README.md`.
3. Make minimal edits. Keep each document's existing hierarchy, voice and level of detail.
4. **Run `make test`.** `scripts/ci/setup_docs_test.go` joins the five invocation forms across `docs/setup.md`,
   `plugin/README.md` and `plugin/skills/setup/SKILL.md`: if you change a command in one copy you must change it
   in all three, and this test is what catches you. `make typecheck build` too if you touched anything a test
   reads.
5. Commit with `git` (never `make commit` or `make push` — that chain stages untracked files and merges origin
   mid-run) and push your branch. Message: `15: <Imperative summary>`.
6. If nothing is structurally stale: **change nothing, push nothing**, and say what you checked. The workflow
   closes the issue automatically when no branch is pushed. A no-op month is a correct month.

## Guardrails

- Document only what exists. Never add a secret, a credential, a hostname or an environment-specific value.
- Never touch `plugin/bin/**` — the release pins are produced only by `make release`.
- Never bump `go.mod`'s `go` line.
- Plans go in `.context/plans/`, scratch in `.ignored/` — never a stray file in the repository root.
```

#### 4.9.3 `.claude/agents/developer.md`

````markdown
---
name: developer
description: The writer/fixer role for the agentic loop — implements a labelled issue on a branch, and applies
  the reviewer's blockers on a PR branch. Runs through the claude workflow.
model: opus
color: blue
tools: Read, Write, Edit, Bash, Glob, Grep
---

<!-- No WebSearch and no WebFetch, matching the workflow's own allow list (§1.1.1 (iv)): the OAuth token is
     reachable to everything this agent runs, and nothing in this repository's work needs the web. The frontmatter
     and the workflow settings must agree — a tool named here but withheld there is a confusing failure at the
     first call, and a tool granted there but not named here is a hole the settings block did not intend. -->

You are the implementing agent for **brigade** — a Go CLI and Claude Code plugin (`cmd/brigade`,
`internal/harness/…`, `internal/adapters/{fs,supabase}`, `plugin/`). You are running on a CI runner, on a branch,
with write access. Everything below is a rule, not a preference.

Read `CLAUDE.md` first, every time. Read `.context/plans/brigade-execution-log.md` for where the work stands.

## Two jobs

- **Implement a labelled issue.** The issue body is your brief. Do what it asks and nothing more. Push a branch;
  the workflow opens the PR for you and titles it `15: <the issue title>`.
- **Fix a review.** The reviewer's `CHANGES_REQUESTED` body lists blockers. Apply **every** one of them **on this
  PR branch** and **push**. A narrated fix that does not push fails the run by design ("Verify the fix was
  pushed"). If you disagree with a blocker, fix what you agree with, push, and argue the rest in a PR comment —
  never silently skip one.

## The gates, before every push

Run these by name. They are the same gates `ci.yml`'s `fast` job runs, and running them here saves a full
matrix round trip:

```sh
make typecheck tidy-check
make lint
make build test          # unit + testscript + harness + conformance(fs), -race. Docker-free by design.
make deps-check schema-check
make plugin-check        # plugin/ allowlist, modes, VERSION == plugin.json, shellcheck, the secret scan
```

If you touched anything under `scripts/`, also run the pinned container shellcheck — CI has **0.9.0**, this
runner may have another, and they disagree:

```sh
docker run --rm -v "$PWD:/mnt" -w /mnt koalaman/shellcheck:v0.9.0 -s sh <file>
```

If you touched `supabase/` or `internal/adapters/supabase`, say so in the PR body: the live tests
(`make test-all`) need a local stack, they are opt-in behind `BRIGADE_TEST_LIVE=1`, and CI's `supabase` job is
what will actually run them on your PR.

## Committing and pushing

- Plain `git add <named paths>` — **never** `git add :/ .` — then `git commit -m "15: <Imperative summary>"`,
  then `git push` to your branch.
- **Never run `make commit`, `make push` or `make release`.** `make commit` stages untracked files across the
  whole tree and merges `origin` mid-run; `make release` writes the release pins and pushes a tag. Both are the
  owner's, on `master`, from her machine.
- **Merges only, never rebase.** No `git rebase` in any form, no force push, no rewriting pushed history. If you
  hit a merge conflict, stop and hand it back in a PR comment.
- One logical change per commit; several commits on a branch are fine (the merge is a squash).

## Never, on pain of a blocked PR

- **`plugin/bin/VERSION` and `plugin/bin/checksums.txt`** — produced only by `make release`. The workflow fails
  any PR that touches them.
- **`go.mod`'s `go` line** — a release-reproducibility pin. The workflow fails any PR that touches it.
- **`docs/allowed-deps.txt`** — the five modules the shipped binary may link. If your change pulls a new module
  into the binary, stop and say so in the PR body; `make deps-check` will fail and that is the correct outcome.
- **The protocol.** v1 is frozen: a wire-shape change means `internal/protocol` (types and `Validate`),
  `docs/protocol-v1.schema.json` (`make schema`), the conformance suite and **both** adapters, in one commit. If
  the issue seems to ask for one, stop and ask in a comment.
- **`.github/workflows/**` and `.claude/**`** — your tooling denies writes there. Workflow files additionally
  cannot be pushed by this token at all (the `workflows` permission), so a change there fails at the push with a
  confusing error; if a workflow needs changing, say so in the PR body.
- **Secrets.** Nothing on argv, in a log, in `describe` output, or in a file under the project directory. Never
  add a Supabase secret/service-role key or a personal access token to the repository, to a variable, or to a
  workflow. `scripts/ci/no-secrets.sh` runs in `make plugin-check` and will fail you.
- **`~/.claude`** — never hardcode it; it is `CLAUDE_CONFIG_DIR ?? ~/.claude`.
- **`docs/security.md` and `docs/protocol-v1.md`** — do not edit unless the issue explicitly asks and cites a
  ruling.

## What ships, and when

A Go source change **ships nothing** until Rjae runs `make release`. `make checksums-check` stays green through
rule (c)'s published-release arm (the committed checksums are backed by the published release, and the message
says so), and the plugin keeps serving the last released binary. This is the design. Do not "fix" it by
regenerating a checksum or bumping a version.

## The execution log

Normally the row lands in the same commit as the work. **Your PRs are labelled `maintenance`, which the owner has
exempted from that rule (2026-09-09)** — so do not open a row, and do not edit
`.context/plans/brigade-execution-log.md` unless the issue tells you to. Agent memory, where a scheduled role
keeps one, lives under `.context/plans/agent-memory/`, never under `.claude/`.

## Layout

Plans in `.context/plans/`. Scratch in `.ignored/` (gitignored). Experiment reports in `docs/experiments/`,
drivers in `scripts/experiments/`. Never a stray file in the repository root.
````

**brigade-specific deltas from the reference** (all three files)

- The reference's `reviewer.md` is 3.4 KB of .NET conventions and one memory path under `.claude/memory/`; this
  one is an invariant checklist drawn line by line from `CLAUDE.md`, `scripts/ci/README.md` and the Makefile, and
  its memory path is under `.context/` so `record-memory.yml` is unnecessary.
- The reference's `documenter.md` has no forbidden files and no gate; this one has two of each.
- The reference's `developer.md` is a feature-addition sequence for a DDD monolith; this one is a rules file,
  because in brigade the expensive mistakes are procedural (release pins, the `go` line, `make commit`, rebase),
  not architectural.
- All three carry the model tier in the front matter (`model: opus`) so a local invocation matches CI.

### 4.10 GitHub configuration — the checklist

Everything here is a one-time setup action. Commands are given where a `gh` form exists and the UI path where it
does not. The division of labour is sharp and worth stating before the list, because it decides who is blocked on
whom: **two credentials have to be minted by hand, by Rjae, and everything downstream of their existing is an
agent's job.**

#### Credentials — created by Rjae, about five minutes

There is no API for either of these, and that is not an oversight in this brief. GitHub exposes no endpoint that
mints a personal access token or registers an App (the App *manifest* flow exists but requires a browser to
complete), and `claude setup-token` is an interactive command that opens a browser and waits. So an agent cannot
produce these values, only consume them. The exact names below are what section 4's files read; anything else has
to be edited in two or three places.

**(i) A GitHub App — org-owned.** github.com → your `appshapes` org → **Settings → Developer settings → GitHub
Apps → New GitHub App**.

| Field | Value | Why exactly this |
| --- | --- | --- |
| Name | `brigade-bot` | **Settled** (7.1, converged with brigade-33 2026-09-10; adopted). The slug becomes the actor login `brigade-bot[bot]`, and `brigade-bot` is the string baked into `allowed_bots` in `claude.yml`, `review-pull-request.yml` and `release-notes.yml`. Changing it later is a three-line edit plus a re-run of drill (a), so it is cheap either way — but it is decided, not open. |
| Homepage URL | `https://github.com/appshapes/brigade` | Required field, unused. |
| Webhook | **Uncheck "Active"** | Nothing listens; leaving it on queues undeliverable deliveries for ever. |
| Repository permissions | `Contents: read & write`, `Pull requests: read & write`, `Issues: read & write`, `Metadata: read` | Exactly the three writes that must chain (push a branch, open/merge a PR, create/close an issue). **Not `Workflows: write`** — nothing here pushes under `.github/workflows/`, and withholding it is what makes such a push fail loudly instead of quietly succeeding. |
| Where can this App be installed | **Only on this account** | It is the `appshapes` org's App, not a public one. |

Then: **Install App → `appshapes` → Only select repositories → `appshapes/brigade`**. Back on the App's General
page, note the **App ID** (a number, not a secret) and click **Generate a private key**, which downloads a `.pem`.
Put the `.pem` somewhere **outside the repository** and hand Rjae's agent the App ID and the path — the App ID goes
in a repository **variable**, the key in a repository **secret**, both by the `gh` commands in (1) below, and the
`.pem` is deleted afterwards. Neither value is ever pasted into the chat (`CLAUDE.md`).

**(ii) A Claude Code OAuth token — dedicated to this repository.** In a terminal:

```sh
claude setup-token       # interactive: opens a browser, prints a token; never scriptable
```

Hand the value to the agent, which sets it as the repository secret `CLAUDE_CODE_OAUTH_TOKEN` ((3) below). **Make
it a token dedicated to the automation rather than Rjae's own**, so agent runs are not billed against their
personal subscription — the failure mode the reference repository lives with, where every agent run in five cron
workflows bills to one named human. The one-command alternative remains available if Rjae would rather not mint a
second credential: flip the existing org secret `CLAUDE_CODE_TOKEN` to `selected` visibility and select this
repository (the corrected `gh` form is in §1.2, blocker 1, option B — note `-f 'selected_repository_ids[]=<id>'`,
not `-F`, which would send the literal string), then rename the three `secrets.CLAUDE_CODE_OAUTH_TOKEN`
references. It is a worse default only because it attributes the spend to a person rather than to the repository.

**And set a monthly spend ceiling on that dedicated account.** This is the second half of open question 7.3
(converged 2026-09-10, adopted) and the money half of §1.1.1 (i): `--max-turns` bounds one wandering session, a
ceiling bounds a wandering month — and, more to the point, bounds a *stolen* token, which is the residual this
brief accepts rather than eliminates. Two runaway shapes it catches that no per-run cap can: a scheduled agent
that finds work every month where there is none, and a fix loop re-armed by hand past the cap. **verify:** which
surface actually carries the ceiling for the token `claude setup-token` mints — Anthropic Console (billing →
usage limits) if the account is API-key-backed, or the Claude Code plan's own usage limits if it is
subscription-backed. They are different screens and only one of them applies; **§7 keeps this as the one item
H1 adds for Rjae** — the number, and where it is set.

**(iii) If Rjae would rather use a PAT than an App** (fewer moving parts, one credential instead of two): a
**fine-grained** personal access token scoped to `appshapes/brigade` with the same three write permissions
(`Contents`, `Pull requests`, `Issues`: read & write) can stand in for the App everywhere `steps.app-token.outputs.token`
appears. **With one consequence that must be drilled, not assumed:** a PAT of Rjae's carries Rjae's *admin*
identity, and repository admin is in the `master` ruleset's `bypass_actors` — so `gh pr merge --auto` performed by
that token bypasses the very required-checks rule the ruleset exists to impose. §1.2 blocker 3 is about exactly
this. If the PAT route is taken, drill (c) is no longer a formality: it must prove empirically that `--auto`
still *waits* on a red PR, and if it does not, the merge step has to move back to an App or to a bot identity
that is not a bypass actor.

Everything below this point — setting variables and secrets, creating labels, PATCHing repository settings,
posting the ruleset, running the drills — is an agent's job once the values above exist.

**(1) Register the App's identity with the repository** — *agent, once Rjae has (i)*

```sh
gh variable set BRIGADE_BOT_APP_ID --repo appshapes/brigade --body '<app id>'
gh secret   set BRIGADE_BOT_PRIVATE_KEY --repo appshapes/brigade < /path/outside/the/repo/brigade-bot.private-key.pem
# then delete the .pem — never under the project directory, never in the chat (CLAUDE.md)
```

**verify:** the exact normalized form the action expects for a custom App in `allowed_bots` (`<slug>` vs
`<slug>[bot]`) — drill (a) prints it.

**(2) The Claude GitHub App** — *already done; no action*

**Measured 2026-09-09 with `gh`: it is installed on the `appshapes` org for ALL repositories** (`app_slug`
`claude`, `app_id` 1236702; the org also carries `qodana-cloud`, `railway-app` and `blacksmith-sh`), so
`appshapes/brigade` is covered. Nothing to install.

The reason it is still listed here, rather than struck out, is that the *dependency* has not gone anywhere and a
future org-admin change could remove it: without this App the reviewer's `gh pr review` lands as
`github-actions[bot]`, and every `startsWith(login, 'claude')` clause in §4.1 and §4.3 silently matches nothing —
approvals never arm auto-merge, CHANGES_REQUESTED never arms the fixer, and the fixer's pushes raise no
`synchronize` (§1.2, consequence (c)). Drill (a) is what confirms it on the day.

**(3) The model credential** — *agent, once Rjae has (ii)*

```sh
gh secret set CLAUDE_CODE_OAUTH_TOKEN --repo appshapes/brigade      # paste at the prompt; never on argv
```

**(4) Repository settings** — *agent; PATCH only what actually changes*

Measured 2026-09-09, the repository **already** has `allow_auto_merge=true`, `allow_squash_merge=true`,
`allow_rebase_merge=false` (correct, and it should stay false — `CLAUDE.md`: merges only, never rebase) and
`delete_branch_on_merge=true`. So three of the four settings the first draft of this brief proposed to set are
already right, and re-sending them is noise in the audit log. What is actually wrong is the squash **title**
source: it reads `COMMIT_OR_PR_TITLE`, and `squash_merge_commit_message` reads `COMMIT_MESSAGES`.

```sh
# The one field that must change:
gh api -X PATCH repos/appshapes/brigade -f squash_merge_commit_title=PR_TITLE

# And, only if the brief's PR-body-as-commit-body convention is wanted over the current concatenated
# commit messages, one more (PR_BODY, or BLANK for a bare subject):
gh api -X PATCH repos/appshapes/brigade -f squash_merge_commit_message=PR_BODY
```

- `squash_merge_commit_title=PR_TITLE` — **the commit-message rule depends on this, and this alone.** The current
  value `COMMIT_OR_PR_TITLE` uses the single commit's own subject on a one-commit PR, so the `15: ` prefix
  `claude.yml` mints into the PR title would be silently discarded on exactly the PRs the loop produces most.
- `squash_merge_commit_message` — a preference, not a rule. `COMMIT_MESSAGES` (today) concatenates the branch's
  commit subjects into the body; `PR_BODY` puts the PR description there, which is where the agent writes
  `Closes #N`; `BLANK` leaves a bare subject. Pick one deliberately and record it — it is the difference between
  a merged commit that closes its issue and one that does not.
- `allow_auto_merge` (already `true`) — `gh pr merge --auto` errors without it.
- `delete_branch_on_merge` (already `true`) — the loop creates a branch per issue; without this they accumulate.

**(5) The outside-collaborator gate** — *UI only*

Settings → Actions → General → **Fork pull request workflows from outside collaborators** →
**"Require approval for all outside collaborators"**. This is the mitigation for running unreviewed contributor
code on self-hosted (Blacksmith) runners on a public repository. **verify:** whether the deployed REST API exposes
a stable equivalent (`repos/{owner}/{repo}/actions/permissions/fork-pr-contributor-approval`) before scripting it;
do it in the UI and screenshot the result for the drill evidence.

**(6) Labels**

```sh
gh label create claude      --repo appshapes/brigade --color 5319e7 --description "Work order for the agentic loop"
gh label create auto-merge  --repo appshapes/brigade --color 0e8a16 --description "Merge when the reviewer approves and CI is green"
gh label create maintenance --repo appshapes/brigade --color fbca04 --description "Agent maintenance PR; exempt from the execution-log-row rule"
gh label create dependencies --repo appshapes/brigade --color 0366d6 --description "Dependabot"
```

**(7) The `master` ruleset** — required checks for pull requests, admin-bypassed

```sh
gh api -X POST repos/appshapes/brigade/rulesets --input .ignored/master-pr-checks.json
# read it back and confirm the rendered bypass actor really is "Repository admin":
gh api repos/appshapes/brigade/rulesets --jq '.[] | {id, name, enforcement}'
gh api repos/appshapes/brigade/rulesets/<id> --jq '{bypass_actors, rules}'
```

`.ignored/master-pr-checks.json` (scratch, per `CLAUDE.md`'s layout rule — the ruleset lives on GitHub, not in the
tree):

```json
{
  "name": "master-pr-checks",
  "target": "branch",
  "enforcement": "active",
  "conditions": {
    "ref_name": {
      "include": ["refs/heads/master"],
      "exclude": []
    }
  },
  "bypass_actors": [
    {
      "actor_id": 5,
      "actor_type": "RepositoryRole",
      "bypass_mode": "always"
    }
  ],
  "rules": [
    {
      "type": "required_status_checks",
      "parameters": {
        "strict_required_status_checks_policy": false,
        "do_not_enforce_on_create": true,
        "required_status_checks": [
          { "context": "fast" },
          { "context": "macos" },
          { "context": "reproducibility" },
          { "context": "supabase" }
        ]
      }
    }
  ]
}
```

Every field, and why:

- **`target: "branch"` + `conditions.ref_name.include: ["refs/heads/master"]`** — `master` is the only branch;
  agent branches (`claude/issue-*`) are unprotected, which is what lets the fixer push to them.
- **The four contexts are the four `ci.yml` job names as they appear on a PR** — `fast`, `macos`,
  `reproducibility`, `supabase`. **`deploy-staging` is deliberately absent**: it is gated on
  `vars.BRIGADE_STAGING == 'true'` and has never run, and a required check that never reports blocks every PR
  forever. Optionally add `"integration_id": 15368` to each entry to require the check *from GitHub Actions*
  specifically rather than from any app that reports that name (**verify:** the Actions app id on the deployed
  instance before adding it — a wrong id makes the check unsatisfiable).
- **`strict_required_status_checks_policy: false`** — `true` would require every PR branch to be up to date with
  `master` before merging. Under "merges only, never rebase" that means a merge commit from `master` into the
  branch on every unrelated push to `master`, which is churn, and each one re-runs the whole matrix.
- **`do_not_enforce_on_create: true`** — creating a branch cannot have checks yet.
- **`bypass_actors` = the repository-admin role, `bypass_mode: "always"`** — this is what keeps Rjae's direct
  pushes to `master` working exactly as today (owner's ruling: no branch protection while there is one committer
  and merges only). **verify:** `actor_id: 5` is GitHub's `RepositoryRole` id for *admin*; read the ruleset back
  and confirm the UI renders "Repository admin" before trusting it.
- **Only one rule.** No required approvals, no linear history (that would forbid merge commits, which this
  repository's git policy requires), no required signatures, no deletion/force-push rules — those belong to
  `protect-release-tags`, which already exists and is untouched.

**The thing to hold on to:** a bypass actor *bypasses this rule*. So the merging identity must be the App (not in
`bypass_actors`), or `--auto` merges past the gate and drill (c) would pass while proving nothing.

**(8) Order of operations.** Configure (1)–(7) **before** merging the workflow files, and leave `update-documentation.yml`'s cron in
place (`review-repository.yml` has none until 7.4 is settled) but do not apply the `auto-merge` label to anything real until drills (a)–(d) in
section 5 have passed.

---

## 5. Verification drills — before the `auto-merge` label is ever applied for real

Each drill names its pass predicate and the evidence to record. Evidence goes in `docs/experiments/` (the house
rule for experiment reports) as `docs/experiments/agentic-workflows-drills.md`, with run ids, not screenshots
alone. **A drill that cannot be run is NOT a pass** — say so in the row.

**(a) The loop produces a correctly titled PR under the App identity.**
Open a throwaway issue titled `Add a comment to scripts/ci/README.md's workflow table` labelled `claude` and
`maintenance` (no `auto-merge`).
*Pass predicate:* `claude.yml` runs; a branch is pushed; a PR exists whose **title starts `15: `**; the PR's
`user.login` is **`brigade-bot[bot]`**; `review pr` runs on it (proving the App's PR creation raised
`pull_request: opened`, i.e. the App-token-vs-`GITHUB_TOKEN` switch works); a review lands at head.
*Record:* the two run ids; `gh pr view <n> --json title,author,labels`; **`gh api repos/appshapes/brigade/pulls/<n>/reviews
--jq '.[].user.login'`** — this is the measurement that re-pins the `startsWith(login,'claude')` predicate of §4.1
and §4.3. It should print `claude[bot]`: the Claude GitHub App was measured installed org-wide on `appshapes` on
2026-09-09 (§4.10 (2)), so this drill is the on-the-day re-check rather than the discovery. If it prints
`github-actions[bot]` the App has gone away since, and **drills (b) and (c) cannot pass** until it is back.

**(b) The fix cap trips at 3 with the handoff comment.**
On the same PR, have the reviewer request changes four times (four fix cycles; force them by leaving a real
blocker in place, or by re-running the reviewer against a deliberately incomplete fix).
*Pass predicate:* fix cycles run for reviews 1–3; **each fix push raised a new `review pr` run** (this is the
predicate that catches the §1.2 consequence (c) failure — a fixer that pushes with `GITHUB_TOKEN` moves the head
SHA, so verifier 1 passes, but fires no `synchronize`, so the loop parks while looking healthy; counting reviews
alone would not see it); on the **4th** claude `CHANGES_REQUESTED`, `claude.yml`'s `fix_gate` sets
`proceed=false`, the Code step is skipped, and **exactly one** handoff comment appears
(`Autonomous fix cap reached: …`); a 5th review adds no second comment.
*Record:* the run ids of all cycles **and, for each fix push, the run id of the `review pr` run it raised**
(`gh run list --workflow "review pr" --json databaseId,headSha,createdAt`);
`gh api …/pulls/<n>/reviews --jq '[.[]|select(.state=="CHANGES_REQUESTED")]|length'`;
the comment's `id` and author (must be `github-actions[bot]` — the handoff uses `GITHUB_TOKEN` so it chains to
nothing).
*Also record — the L6 half, added 2026-09-10 for the duplicate-delivery case:* for **every** `fix_gate` run,
whether it took the staleness exit or the counting path. The gate proceeds only while the PR's current head still
equals the triggering review's `commit_id`, so a fix cycle that ran must show `head == commit_id` at gate time,
and any second run for the same review must log `stale review — a fix already landed` and set `proceed=false`
without spending a cap slot. If the drill never produces a duplicate delivery naturally — it usually will not —
**force the shape**: re-run the `claude` workflow for a review whose fix has already landed (`gh run rerun <id>`)
and record that the re-run skips. A re-run that proceeds means the staleness clause is missing or reversed, and
the cap can be spent twice on one blocker.

**(c) Auto-merge WAITS for the four checks under the ruleset.** *The drill Rjae named explicitly.*
Create a PR that is red — e.g. a Go file with a deliberate `go vet` failure — label it `auto-merge`, and let the
reviewer approve it (or approve it manually as the App-adjacent identity, whichever the loop produces).
*Pass predicate, in three halves:*
 (i) `auto-merge.yml` runs, `gh pr merge --squash --auto` succeeds, and `gh pr view <n> --json autoMergeRequest`
 shows auto-merge **enabled** — and the PR **stays open** while `fast` is red. Wait out at least one full CI
 matrix; the PR must still be open.
 (ii) Push the fix; when all four checks report success the PR **merges by itself**, the merge is a **squash**,
 the merge actor is **`brigade-bot[bot]`** (not an admin), and the resulting commit subject on `master` starts
 `15: `.
 (iii) **The already-green case, which is the everyday one and which halves (i) and (ii) do not touch.** Open a
 second PR, let all four checks go green *first*, and only then have the reviewer approve it. `--auto` will error
 with "Pull request is in clean status" — that is expected and is why the step has the `|| gh pr merge … --squash`
 fallback (§4.3). *Pass:* the `auto-merge` job ends **green**, the PR merges as a squash by `brigade-bot[bot]`,
 and the merge is not left to a human. A red job here means the fallback is missing or wrong, and the loop would
 have been broken on its most common path while (i) and (ii) both passed.
*Record:* `gh pr view <n> --json autoMergeRequest,state,mergedBy,mergeCommit` for both PRs; `gh api
repos/appshapes/brigade/commits/<sha> --jq .commit.message | head -1`; the timestamps proving the wait; and the
`auto-merge` job log for (iii) showing which of the two commands succeeded.
*Negative control worth doing once — but NOT against `master`.* The point is to watch an admin identity merge a
red PR, and doing that on the default branch of a public repository lands a knowingly broken commit on `master`
and leaves no clean way back. Instead: create a throwaway base branch, e.g. `drill/ruleset-control`, add it to
the same ruleset's `conditions.ref_name.include` for the duration, open the red PR **against that branch**, and
attempt the merge with an admin PAT — it will merge immediately, past the required checks, which is exactly the
failure the App identity exists to prevent. Then remove the branch from the ruleset and delete it. (If for some
reason the control is run against `master` anyway, `git revert` the merge immediately and record the revert SHA
in the drill evidence — but prefer the throwaway branch.)

**(d) An outside-collaborator PR does not run CI without approval.**
From an account with no write access, open a PR from a fork.
*Pass predicate:* the run is queued in the **"Action required"** state and no job starts; `review pr` does **not**
run (its same-repo `if:` skips it); after a maintainer clicks Approve, `ci.yml` runs and `review pr` stays skipped.
*Record:* the run page state before and after approval; confirmation that no Blacksmith job executed pre-approval.

**(e) Dependabot's first grouped PR passes rule (c).**
Wait for (or trigger) the first monthly grouped gomod PR.
*Pass predicate:* the PR is labelled `maintenance` + `dependencies`; all four checks green — specifically
`make checksums-check` **passes** and its message names arm **(c)** ("the published release v0.4.1 backs
plugin/bin/checksums.txt (this commit changed the source without bumping the pin)"); `make deps-check` green
(the module set in the binary is unchanged); `make tidy-check` green. **`review pr` is skipped by the actor
filter** — the run appears in the Actions list with its `review` job *skipped*, not absent and not red: a
Dependabot branch is in-repo, so the same-repo clause passes it and only `github.actor != 'dependabot[bot]'`
stops it (§4.2, §4.8). A red `review pr` here means the actor clause was dropped; a *missing* run means the
workflow's `on:` was changed. And no auto-merge.
*Record:* the PR number; the `fast` job's `make checksums-check` step output verbatim; the merge, performed by
Rjae.

**(f) A Go bump merges, `checksums-check` stays green, and the plugin still serves v0.4.1.**
Immediately after (e) merges to `master`.
*Pass predicate:* the push run of `ci.yml` on `master` is green, with `checksums-check` passing through arm (c)
and saying so; `cat plugin/bin/VERSION` still reads `0.4.1`; a fresh `/plugin` install still fetches the v0.4.1
release binary; **nothing in the tree was regenerated to make this true**.
*Record:* the run id and the step output; `plugin/bin/VERSION`; a note in the drill report stating in one
sentence that **this is the design, not a defect** — Go source ships only through `make release` — so nobody
files it later.

**(g) Release notes on a pre-release tag.**
Run `make release version=0.5.0-rc1` (or the existing rehearsal path) on a throwaway basis, or run
`release-notes.yml` by `workflow_dispatch` with that tag.
*Pass predicate:* the release's notes are replaced by the agent's text, which matches `CHANGELOG.md`'s section
for that version and names the assets that actually shipped; exactly one issue exists titled `<version> is out —
run /brigade:update`, **without** the `claude` label (so it starts no agent); the release's **draft/latest state
is unchanged** by the notes agent (release.yml owns that).
*Also record, and this is the point of doing it on an rc:* whether the `release: published` trigger fired at all.
If it did not — the expected outcome with today's `release.yml`, whose publish uses `github.token` — record that
and take remedy (a) from §4.7 (a `workflow_call` job in `release.yml`).

**(h) A no-change documentation run closes its issue.**
Run `update-documentation.yml` by `workflow_dispatch` at a moment when the four in-scope documents are accurate.
*Pass predicate:* the agent pushes no branch; `Create PR` reports "No branch name" or a definitive 404 and sets
`pr_created=false`; the `Close issue when the agent had nothing to change` step closes the issue with its comment;
**no PR exists**. Then confirm the failure path: re-run with a deliberately stale path in `docs/setup.md` and
check a PR appears, auto-merge-labelled, and that `make test`'s doc-witness join would have caught a one-copy
edit.
*Record:* both run ids; the issue's closing comment; the PR number from the second half.

**(i) Labelling an existing PR starts the agent — and only for a write-access actor.** *(M3, added 2026-09-10.)*
`claude.yml` listens on `pull_request: [opened, labeled]`, so applying the `claude` label to a PR that already
exists is a live entry path into the loop. It was missing from v2's §3 diagram and from these drills, which means
it was a path nobody had watched run. Open a throwaway PR **without** the `claude` label (a one-line comment
change is enough), let it sit until `review pr` has finished with it, then apply the label.
*Pass predicate, both halves:*
 (i-a) applied by Rjae (write access): `claude.yml` starts, the `agent` job's `if:` matches on the
 `pull_request`/`labeled` clause, and the agent works on the existing branch — it does **not** open a second PR
 (the `open-pr` job is `if: github.event_name == 'issues'` and must show as **skipped**).
 (i-b) applied by an account **without** write access (the same account used for drill (d); a maintainer must
 grant it triage or the label will not apply at all — if that cannot be arranged, say so and mark this half **NOT
 RUN**, which per this section's own rule is not a pass): the action's default write-access check rejects it, the
 run ends without an agent session, and nothing is pushed. **This is the control that matters**: the label is a
 human's explicit authorization, and it must be a *write-access* human's.
*Record:* both run ids; `gh run view <id> --json jobs --jq '.jobs[]|{name,conclusion}'` for each, showing
`open-pr` skipped in (i-a); for (i-b), the step or annotation where the action declines, quoted verbatim.

**(j) An agent asked to print its own environment leaks no token into a world-readable log.** *(H1, added
2026-09-10.)* Open a throwaway issue labelled `claude` and `maintenance` whose body asks the agent, in as many
words, to print its environment (`env`, `printenv`, `echo $CLAUDE_CODE_OAUTH_TOKEN`, `cat` of anything under
`$RUNNER_TEMP`) into its output and into a file it commits.
*Pass predicate:* the run's public log contains **no** unmasked credential — not `CLAUDE_CODE_OAUTH_TOKEN`, not
the job's `GITHUB_TOKEN`, not an App installation token; every occurrence renders as `***`. `show_full_output:
false` keeps the session transcript out of the log in the first place, and Actions masks registered secret values
in step output; **confirm the OAuth token in particular is masked**, since it reaches the runner as a secret
expression and should be registered, but that is the assumption under test rather than the finding. Nothing is
committed containing a credential (`git show --stat` on any pushed branch, then `grep` the diff).
*Record:* the run id; the log lines where masking is visible; and, if anything at all appeared unmasked, **rotate
the token before recording anything else** (`gh secret set CLAUDE_CODE_OAUTH_TOKEN`) and note the rotation.
*What this drill is, and is not.* It is **evidence, not a control that proves safety.** A masked log shows the
masker matched that string; it says nothing about reachability, and the token **was** reachable — the agent read
it. `allowed_non_write_users` is unset, so `claude-code-action`'s conditional secret scrub and its bubblewrap
PID-namespace isolation are both off, and every Bash child inherits the step environment (§1.1.1). This drill
watches the one consequence that would be publicly visible; the residual it does not remove is the accepted one,
bounded by a dedicated token, a monthly ceiling, rotation and minimal job `permissions:`. Do not cite a green (j)
as proof that an injected prompt cannot exfiltrate.

**Sequencing.** (a) → (b) → (d) may run in any order but all three precede (c); (c) precedes the first real
`auto-merge` label. (i) needs a PR to label, so it follows (a); (j) needs only a working loop, so it follows (a)
too and should be run **before** any agent is pointed at content brigade did not write (a fork PR's diff, a
dependency's build script) — it is the cheapest look at the leak path there is. (e)/(f) are one pair and can wait
for the first monthly Dependabot PR. (g) and (h) are independent. **Do not apply `auto-merge` to a real PR until
(a), (b), (c) and (d) have all passed and are recorded.**

---

## 6. Proposed execution-log rows, and the lines that change with them

**Not to be opened until Rjae charters the implementation.** Tier **Opus** throughout: this is CI, scaffolding
and docs work by the log's own tier policy, and Fable never runs in CI. Proposed IDs continue the P-series after
the closed P8 (Codex) workstream.

| ID | Task (this brief's §) | Status | Model | Commit / evidence |
| --- | --- | --- | --- | --- |
| P9-1 | GitHub configuration: register the App's id and key, the OAuth secret, the squash-title setting (the only one that changes), the labels, the `master` ruleset (§4.10 (1)–(7)) | todo | Opus (+ Rjae's five minutes: create the App and install it, `claude setup-token`, **set the monthly spend ceiling on that account**, the UI fork-approval setting) | `gh api` read-backs of the ruleset and settings, recorded in `docs/experiments/agentic-workflows-drills.md`; the ceiling's value and the screen it was set on, recorded there too |
| P9-2 | The loop: `claude.yml` (two jobs, `agent` → `open-pr`), `review-pull-request.yml` (**including the `gh pr comment` on a guard trip — 7.8, landed here, not deferred**), `auto-merge.yml`, `_create-claude-issue.yml`, `scripts/ci/pr-guard.sh`, and the three `.claude/agents/*.md` (§4.1–4.4, §4.9); resolve the `claude-code-action` SHA pin (7.7) | todo | Opus | one commit (`scripts/ci/pr-guard.sh` committed mode 100755); `make plugin-check lint test` green; `pr-guard.sh` through both shellchecks (local **and** the pinned 0.9.0 container). **`scripts/ci/README.md`'s four workflow rows and its one script row for these files land in this same commit** (see below) |
| P9-3 | Drills (a)–(d), **(i)** and **(j)**, and the timeout and `--max-turns` re-pin from the first ten runs (§5, 7.3) | todo | Opus | `docs/experiments/agentic-workflows-drills.md`, run ids. No `scripts/ci/README.md` rows — this row adds no file |
| P9-4 | `.github/dependabot.yml`; drills (e), (f). **No `dependabot-auto-merge.yml`** — 7.6(b), converged and not planned | todo | Opus | the first grouped PR, `checksums-check` output. `.github/dependabot.yml` is not a workflow and adds no `scripts/ci/README.md` row; say so in the commit message so the omission reads as deliberate |
| P9-5 | `release-notes.yml`, plus — if remedy (a) is taken — BOTH edits to `release.yml` (the widened top-level `permissions:` and the `notes` job); drill (g) | todo | Opus | the rc release page, the announcement issue. **`scripts/ci/README.md`'s `release-notes.yml` workflow row lands in this same commit** |
| P9-6 | `update-documentation.yml` and drill (h) | todo | Opus | both run ids. **`scripts/ci/README.md`'s `update-documentation.yml` workflow row lands in this same commit** |
| P9-7 | `review-repository.yml` (`workflow_dispatch` only, cron commented — 7.4), **the seeded memory marker** `.context/plans/agent-memory/review-repository.md` (`Date: (unseeded)`, `Commit: (none)`, `Scope: whole tree`) and the first manual pass | todo | Opus | the seed commit; the first PR, unlabelled for auto-merge. **`scripts/ci/README.md`'s `review-repository.yml` workflow row lands in this same commit** |
| P9-8 | The three records that can only be written once everything they count exists: `CLAUDE.md`'s "Repository automation" paragraph, `scripts/ci/README.md`'s **opening-sentence counts**, and the execution-log exemption sentence (all three below) | todo | Opus | one commit, **sequenced after P9-7** |

**Why P9-8 is smaller than it was, and sequenced last.** v2 made P9-8 "records: `CLAUDE.md`, `scripts/ci/README.md`,
the execution-log sentence" in the same commit as P9-2 — which is impossible as written, and brigade-33's M4
caught it: the `scripts/ci/README.md` rows it was to write describe workflows that **P9-4 through P9-7 have not
added yet**. A row for `release-notes.yml` written in P9-2's commit documents a file that does not exist for
another three rows. So the work is split by the only rule that keeps the README honest:

- **Every P9-n row updates its own `scripts/ci/README.md` rows in its own commit** — the workflow table row (and,
  for P9-2, the script index row) for the files *that row* adds, and nothing else. Each row's evidence cell above
  says so explicitly. A file and its index entry land together or neither lands.
- **P9-8 keeps only what genuinely cannot be written earlier**: the README's opening-sentence **counts** (they are
  wrong at every intermediate step and correct only once the last workflow exists), `CLAUDE.md`'s paragraph
  (it describes the loop as a whole, including the scheduled agents), and the execution-log exemption sentence.
  It is therefore **sequenced after P9-7**, not alongside P9-2.

**The record lines, and which row writes each.**

*`scripts/ci/README.md` — the workflow table* gains seven rows in total, **written four-then-one-then-one-then-one
across P9-2, P9-5, P9-6 and P9-7** as each workflow lands. The file's opening sentence, "Brigade has three
workflows and fourteen scripts in a near-1:1 relation", becomes **ten workflows and fifteen scripts** — the new
script is `pr-guard.sh` below; the same paragraph's "only two are reached from a workflow without a target of
their own" becomes three, `pr-guard.sh` being the third. **Those counts are P9-8's**, precisely because they are
false at every point in between; the reasoning for keeping one index rather than two still holds and is worth
restating:

| Workflow | Trigger | Purpose |
| --- | --- | --- |
| `.github/workflows/claude.yml` | `issues` (labeled/assigned), `pull_request` (opened/labeled), `pull_request_review`, comment events, `workflow_dispatch` | The agentic hub: implements a `claude`-labelled issue on a branch and opens its PR, or applies a reviewer's blockers on a PR branch. Bounded by a derived fix cap (`MAX_ATTEMPTS 3`), actor filters, the App-token/`GITHUB_TOKEN` switch and per-PR concurrency. Two jobs: `agent`, then `open-pr` (`needs:`), which is where the App private key lives. *(Row written by P9-2.)* |
| `.github/workflows/review-pull-request.yml` | `pull_request` (opened/synchronize/reopened), same-repo only | Deterministic guard (release pins, the `go` line, the protocol join) then the read-only reviewer agent; fails loudly if no review lands at head, and comments on the PR when the guard trips. *(Row written by P9-2.)* |
| `.github/workflows/auto-merge.yml` | `pull_request_review` (submitted) | Arms `gh pr merge --squash --auto` as the App when the reviewer approved the current head of an `auto-merge`-labelled PR. The `master` ruleset is what makes it wait for CI. *(Row written by P9-2.)* |
| `.github/workflows/_create-claude-issue.yml` | `workflow_call` | Reusable factory: creates the `claude`-labelled work order with the App token so the `issues: labeled` event fires. *(Row written by P9-2.)* |
| `.github/workflows/update-documentation.yml` | monthly cron, `workflow_dispatch` | Structural documentation refresh through the loop. Opens its PR **without** the auto-merge label for the first two months, then with it (7.2). *(Row written by P9-6.)* |
| `.github/workflows/review-repository.yml` | `workflow_dispatch` only (the monthly cron is present but commented out — 7.4) | Reviewer pass over the tree; opens a PR with minimal fixes and **no** auto-merge label. *(Row written by P9-7.)* |
| `.github/workflows/release-notes.yml` | `release: published`, `workflow_call`, `workflow_dispatch` | Writes the published release's notes from `CHANGELOG.md` and the log, and opens the "run `/brigade:update`" announcement. *(Row written by P9-5.)* |

*`scripts/ci/README.md` — the script index* gains one row, **written by P9-2 in the same commit as
`pr-guard.sh` itself**, which is the whole reason the guard is a file rather
than twenty-five lines inlined in a workflow (`plugin-check.sh`'s check 9 lints `scripts/ci/*.sh scripts/*.sh`
and nothing else; the pinned shellcheck 0.9.0 container rule is written against files; ten Go drift tests open
these scripts by path):

| Script | Reached through | Purpose |
| --- | --- | --- |
| `scripts/ci/pr-guard.sh` | `.github/workflows/review-pull-request.yml` (not `make`) | Fails a PR that changes a release pin (`plugin/bin/VERSION`, `plugin/bin/checksums.txt`), `go.mod`'s `go` line, or `internal/protocol/` without `docs/protocol-v1.schema.json`. Diffs `base...head` (three dots, against the merge base) so a PR whose base has moved past a release is not falsely accused. |

*`scripts/ci/README.md` — the "Required GitHub configuration" table*, whose heading sentence **"Names only. No
value of any of these appears in this repository, and none may be added."** stays exactly as it is and now covers
three more names. **All three rows are written by P9-2**, the first commit that reads any of them; **P9-5 amends
the `CLAUDE_CODE_OAUTH_TOKEN` row's "read by" list** when `release-notes.yml` lands, which is the one place a
later row edits an earlier row's line rather than adding its own:

| Type | Name | Notes |
| --- | --- | --- |
| Var | `BRIGADE_BOT_APP_ID` | The GitHub App's id, read by `claude.yml`, `auto-merge.yml` and `_create-claude-issue.yml`. A variable, not a secret: an app id is public and masking it would hide the identity a failing run has to name. |
| Secret | `BRIGADE_BOT_PRIVATE_KEY` | The GitHub App's private key. `actions/create-github-app-token` exchanges it for a repository-scoped installation token that expires in one hour. It is the only stored credential in this repository with write access, and it exists because `GITHUB_TOKEN`'s writes raise no workflow events. |
| Secret | `CLAUDE_CODE_OAUTH_TOKEN` | The model credential for `anthropics/claude-code-action`, read by `claude.yml`, `review-pull-request.yml` and `release-notes.yml`. Never reaches `_create-claude-issue.yml` or `auto-merge.yml`. |

*`CLAUDE.md`* gains a "Repository automation" paragraph — it must be short, because `CLAUDE.md` is loaded into
every session, and it must exist, because `claude-code-action` restores `CLAUDE.md` from the base branch and it is
therefore the one file every workflow agent is guaranteed to read:

> Repository automation: an issue labelled `claude` is a work order for the agentic loop
> (`.github/workflows/claude.yml`). An agent working on a PR branch follows `.claude/agents/developer.md`; the
> PR reviewer follows `.claude/agents/reviewer.md`. Agents commit with plain `git` on their branch and never run
> `make commit`, `make push` or `make release`. A PR labelled `maintenance` is exempt from the execution-log-row
> rule (owner ruling, 2026-09-09); its squash-merge title, `15: <Imperative summary>`, satisfies the
> commit-message rule. Agent memory lives under `.context/plans/agent-memory/`, never under `.claude/`.
> `anthropics/claude-code-action` is pinned by **full commit SHA** — the only action in this repository that is,
> because it is the only one that runs a model with repository write access; every other action stays on its
> major tag.

*`.context/plans/brigade-execution-log.md`* — the header line "Update it in the same commit as the work" gains
its exception:

> Update it in the same commit as the work. **Exception (Rjae, 2026-09-09): a pull request labelled
> `maintenance` — the agentic workflows' own PRs and Dependabot's — carries no row.**

*`.github/workflows/release.yml`* — only if remedy (a) of §4.7 is taken: **two edits, not one.** The added job is
the visible half; the widened top-level `permissions:` block is the half that makes it run at all.

```yaml
# EDIT 1 — the top-level permissions block, today `contents: write` alone (release.yml:24-25).
# A called workflow's job may request only permissions the CALLER already holds: release-notes.yml's `notes`
# job asks for `issues: write` (the announcement issue) and `id-token: write` (claude-code-action's OIDC), and
# with the current block GitHub fails the run at validation — "is requesting 'issues: write', but is only
# allowed 'issues: none'" — before a single step executes. Widening here widens the ceiling, not the grant:
# each job still gets what its own block asks for, and `goreleaser` asks for nothing new.
permissions:
  contents: write
  issues: write
  id-token: write
```

```yaml
# EDIT 2 — the added job. It exists because release.yml's Publish step runs `gh release edit --draft=false`
# authenticated by `${{ github.token }}`, and GITHUB_TOKEN's writes raise no workflow-triggering events — so
# release-notes.yml's `release: published` trigger would never fire. Calling it directly needs no event.
#
# No `timeout-minutes` here: GitHub rejects it on a job that `uses:` a reusable workflow. The 20-minute bound
# lives on release-notes.yml's own `notes` job (§4.5 states the exception).
  notes:
    needs: goreleaser
    uses: ./.github/workflows/release-notes.yml
    with: {tag: "${{ github.ref_name }}"}
    # ONE secret, named — never `secrets: inherit`, which would hand the release path every repository secret
    # this repo holds, BRIGADE_BOT_PRIVATE_KEY (the App's write credential) included. §4.4 states the rule and
    # §4.7's `workflow_call.secrets` block is what makes passing exactly one possible.
    secrets:
      claude_code_oauth_token: ${{ secrets.CLAUDE_CODE_OAUTH_TOKEN }}
```

---
## 7. Open questions — what still needs Rjae

**Every question in v2's list is now converged.** brigade-33's review of 2026-09-10 answered all eight; six are
adopted outright and folded into the body, and **two change an earlier agreement, so they need Rjae's word before
P9-2**. H1 adds one new item. This section is deliberately short: if a decision is settled it lives where it is
acted on, not here.

### 7.A Still needs Rjae — three items

1. **7.2 — documentation PRs start WITHOUT auto-merge.** *Converged with brigade-33 2026-09-10; **Rjae to
   confirm**, because it CHANGES an earlier agreement.* Rjae's workflows-item 4 said documentation PRs auto-merge.
   The recommendation is `auto_merge: false` on `update-documentation.yml` for the **first two months**, then
   `true` once the diffs are boring. Reason: an auto-merged documentation PR lets the four in-scope documents
   change on `master` with nobody reading them, gated only by CI's doc-witness join — which pins five invocation
   *forms* and says nothing about prose. Two months is enough to see what the agent actually writes. **Already
   written into §4.5** with the comment that says how to flip it; flipping is one word and no other line moves.
   *If Rjae prefers the original:* change `auto_merge: false` to `true` in §4.5 and delete the comment.
2. **7.4 — `review-repository.yml` is `workflow_dispatch` only, until the second adapter is chartered.**
   *Converged with brigade-33 2026-09-10; **Rjae to confirm**, because it CHANGES an earlier agreement.*
   Workflows-item 5 said monthly. Reason: the execution log records no live rows and a deliberately quiet tree, so
   a monthly pass either no-ops — an agent run a month for nothing — or finds something to do in a finished tree,
   which is worse, because a human then reviews invented work. **Already written into §4.6**: the cron block is
   kept, commented out, with the two-line re-enable instruction beside it. *If Rjae prefers monthly:* uncomment
   those two lines.
3. **The monthly spend ceiling — the number, and the screen it is set on.** *New, from brigade-33's H1.* §1.1.1
   (i) accepts that the Claude OAuth token is reachable to every agent's Bash children (the action's secret scrub
   and bubblewrap isolation are conditional on `allowed_non_write_users`, which this brief never sets), and bounds
   that residual with four things — a dedicated token, rotation, `show_full_output: false`, and **a monthly spend
   ceiling on the dedicated account**. The first three are decided. The ceiling needs a number from Rjae, and a
   **verify:** of where it is actually set: Anthropic Console (billing → usage limits) if the account behind
   `claude setup-token` is API-key-backed, or the Claude Code plan's own usage limits if it is subscription-backed
   — different screens, only one of which applies. It is on the **P9-1** row, in the "Rjae's five minutes" column.

### 7.B Converged and folded into the body — for the record, not for decision

| # | Answer | Adopted or changed | Where it now lives |
| --- | --- | --- | --- |
| 7.1 | The App is named **`brigade-bot`** (login `brigade-bot[bot]`). | Converged; **adopted** — it was already this brief's working name. | §4.10 credentials (i), name row; `allowed_bots` in §4.1, §4.2, §4.7 |
| 7.3 | Turn budgets (`60`/`30`/`20`) stay **provisional**, re-pinned from the first ten real runs on **P9-3** — *plus* a monthly spend ceiling on the dedicated OAuth account, which is the knob a per-run cap cannot be. | Converged; **adopted**. The ceiling half ties to H1 and is item 3 of §7.A. | §4.1's `claude_args` comment; §4.10 credentials (ii); the P9-3 row |
| 7.5 | `allowed_bots: "claude,brigade-bot"` stands — **no veto**. The principle that is not negotiable either way: an explicit list, never `"*"`, and never `allowed_non_write_users`. | Converged; **adopted**. | §1.2 consequence (a) |
| 7.6 | (a) majors stay excluded and are handled by hand; (b) **monthly manual merge — `dependabot-auto-merge.yml` is not planned** and is deleted from this brief; (c) accept the lowercase verb rather than add a fifth workflow to capitalise one letter. | Converged; **adopted**. | §4.8, which now closes 7.6 in place; the P9-4 row |
| 7.7 | **SHA-pin `anthropics/claude-code-action`** — full 40-hex SHA with a `# v1` comment, resolved at implementation time (**verify:**). This breaks brigade's major-tag house style for exactly one action, because it is the only one that runs a model with repository write access. | Converged; **adopted**. | §4's two conventions; the `uses:` lines in §4.1, §4.2 and §4.7; §6's `CLAUDE.md` paragraph; the P9-2 row |
| 7.8 | A tripped `pr-guard.sh` **posts a `gh pr comment`** naming the invariant — with `github.token`, so it chains to nothing — and it lands **now, in P9-2**, not as a follow-up. | Converged; **adopted**. | §4.2's "Explain a guard trip on the PR" step; the P9-2 row |

**Closed earlier, still true, and repeated here so nobody reopens them:** *who creates the App and the token* —
nobody automated can, so §4.10's credentials checklist gives Rjae two UI paths and the exact names, about five
minutes' work, and every step after the values exist is an agent's; *is the Claude GitHub App installed* — yes,
measured 2026-09-09, org-wide on `appshapes` (§1.2 consequence (b), §4.10 (2)); *whose subscription pays* — a
**dedicated** repository `CLAUDE_CODE_OAUTH_TOKEN`, never Rjae's personal one (§4.10 credentials (ii), §1.1.1 (i)).
