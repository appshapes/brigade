# 0033. Hand an agent the job's own token, and name the jobs that hold the Claude App's

- **Status:** Accepted
- **Date:** 2026-11-24
- **Supersedes:** none
- **Corrected:** 2026-11-24: the Context said that the session's `gh` and git both hold the Claude App's token, and the Consequences that the fixer's push is that token's. That was read from the action's source and not run, and it is wrong for git. The checkout keeps its own credential, the job's token, and git presents it before it would ask for the App's: on pull request #130 the fixer's push was refused as `github-actions[bot]` while its job had `contents: read` (run 45502056649), and went through with `contents: write` back (run 45507085498). The ADR now says that the App's token is the agent's `gh`'s, that an agent's `git push` goes with its job's own token, that the fixer's job grants `contents: write` for that, and that the reviewer's git is bounded by its job's `contents: read`, which is GitHub's. The Decision's reason for the fixer, its push, is therefore not what its exception rests on: what the App's token gives the fixer is that its comments on a pull request are `claude[bot]`'s, and whether it needs that token at write is left to the owner.

## Context

`anthropics/claude-code-action` takes a `github_token`. Handed none, it exchanges the job's identity
(`id-token: write`) with Anthropic's exchange for a token of the Claude GitHub App, and the session's `gh`
holds that token in place of the job's own. The session's git does not: it presents the credential the
checkout step keeps, which is the job's own token. What the App's token can do is decided by the exchange and
not by the job's `permissions:` block. ADR 0032 read this in the action's source and built the three scheduled jobs so that
every agent step is handed the job's read-only token; its Consequences say the same reading applies to three
workflows it did not change.

Those three were `review-pull-request.yml`, `release-notes.yml` and `send-release-notes.yml`. Each granted
`id-token: write` and handed its agents nothing. The reviewer's header named the job's `contents: read` as
"the boundary" that keeps it from pushing, the email's said "the job's token is read-only", and in each the
agents held the App's token: contents, pull requests and issues at write (measured, below). The reviewer reads
diffs that anyone with a branch of the repository can write, with `Bash(git:*)` and `Bash(gh:*)` allowed.

Two facts of the repository limit what the reviewer's token can be. Its setting
`can_approve_pull_request_reviews` is false, so a job's own token may not approve a pull request. And an event
raised with a job's own token starts no workflow run, so a review submitted with one would start neither
`auto-merge.yml` nor `claude.yml`'s fixer. The owner ruled on 2026-11-24, on work item 438, with the
measurement before him: "Keep claude[bot], ask for read-only (Recommended)".

## Decision

A step that runs the action is handed the job's own token, `github_token: ${{ github.token }}`, in a job
whose `permissions:` block grants `contents: read`, nothing at write and no `id-token`, and that never names
the owner's token. Nothing an agent holds there can write to the repository. A write a workflow has to make is
made in a job that runs no agent, from files the agents left.

- `send-release-notes.yml`: its one job was read-only already; its four agent steps are handed that token and
  `id-token` is gone.
- `release-notes.yml` is two jobs. `notes` runs the four agent steps, the lint and the gate under
  `contents: read`. `publish` runs no agent, starts only when `notes` ended green, takes the artifact and makes
  the two writes, `gh release edit` and the announcement, with `contents: write` and `issues: write`. A
  `dry_run` input takes a run through both jobs with nothing published and nothing announced.

Two jobs hold the Claude App's token instead, each with one agent step, each named in
`scripts/ci/agent_token_test.go` with its reason and with the wish it sends the exchange, letter for letter.

- The reviewer, `review-pull-request.yml`'s `review`. Its review has to be `claude[bot]`'s, for the two facts
  above. Its step asks for the token at `contents: read` and `issues: read` (`additional_permissions`, which
  the action lays over its own wish of `contents`, `pull_requests` and `issues` at write). With that token the
  agent can review and comment and cannot push or merge. The header says that this is the boundary, that it
  rests on the exchange honouring the wish and not on GitHub's `permissions:`, and what was measured.
- The fixer, `claude.yml`'s `agent`. It has to push, and its push has to start `ci.yml` and the reviewer
  again. It keeps the token as the action asks for it, with `actions: read` added, and its header says the
  measured reach.

No other job grants `id-token`, and no workflow grants it to all its jobs.

The action's pin moves from c0949c50 to eb56b6666facdbd501035f4a0269d8caee2e7a8b, the commit `v1` named on
2026-11-24 (v1.0.289), in every workflow at once. The work item's ruling is the owner's word for the move.

## Alternatives considered

- **Hand the reviewer the job's own token and rebuild the loop around it.** The agent would write its verdict
  to a file under a read-only token, and steps with no model would post the review, arm the merge and start
  the fixer. It makes GitHub's `permissions:` the boundary, which is the stronger one. It lost on its cost: it
  needs the owner's setting to let Actions approve pull requests, or an approval shown as a comment;
  `auto-merge.yml`'s job, the fixer's start and the fix cap's count all move to another login; and the whole
  loop is drilled again on real pull requests. It is the way to go if the exchange stops honouring the wish.
- **Leave the reviewer's token as it was and only correct the header.** The reviewer would keep contents at
  write while reading diffs it does not control. One input removes that.
- **Keep `release-notes.yml` as one job and hand its agents the job's token.** The work item allowed it: the
  job's `permissions:` would then be the boundary. But that token has `contents: write` and `issues: write`
  for the two writes, so the agents would hold a write token, where the scheduled jobs' agents hold none.
- **Narrow the fixer's token too.** It was thought to need contents at write to push, which it does not (the
  correction above): its push goes with its job's own token. What its `gh` needs of the App's token is pull
  requests, to comment. Narrowing it is then a wish nobody measured and a change to the loop, left to the
  owner.
- **Narrow the App's installation to chosen repositories.** It covers every repository of the organisation.
  The measurement shows a token minted here reaches this one alone, so nothing in this work depends on it;
  the setting is the owner's.

## Evidence

- The exchange, measured on 2026-11-24 by a throwaway workflow of this work item (`probe-438.yml`, run
  45476572377 on 0f32e4e3; no action and no model): three tokens minted with the job's identity as the action
  mints them, two with no wish and one with the wish `contents: read, pull_requests: write, issues: read`.
  Each write asked of GitHub was one that cannot succeed, so that a missing permission answers 403 and one
  that is there answers 422 or 409; nothing was written.
  - All three tokens differed, and each was revoked by itself at the end (204).
  - Every token listed one repository, this one (`selection: selected`); a private repository of the
    organisation answered 404 to a read and to a write.
  - With no wish: a ref, a pull request and an issue were refused for their content (422), so contents, pull
    requests and issues are at write; the cancel of a run (403), a check run (403), the repository's Actions
    settings (403) and its webhooks (403) were refused for the permission. The two tokens agreed line for line.
  - With the wish: the ref was refused for the permission (403); the pull request still for its content (422).
- **Not measured.** Whether a token with contents at write could write a workflow file: only a real write
  shows it. Whether `issues: read` lowers issues: the issue with no title was still refused for its content
  (422) under the wish, which either means the exchange did not lower it or that GitHub reads that request's
  body before its permission. The `master` ruleset against these tokens. The measurement is one run of one day.
- The App's installation on the organisation: every repository, with `contents`, `workflows`, `actions`,
  `checks`, `pull_requests`, `issues`, `discussions`, `repository_hooks` and `repository_creation` at write
  and `administration` at read (`gh api orgs/appshapes/installations`, 2026-11-24). The exchange hands out far
  less than the installation holds.
- The action's source, the same at both pins (`src/github/token.ts`): with no `additional_permissions` it
  sends the exchange no wish; with one it sends its three at write overlaid by the input's lines. Its own test
  holds that a line can override one of the three (`test/parse-permissions.test.ts` at eb56b666). Its page
  names only what can be added, not that a default can be lowered.
- The repository's setting `can_approve_pull_request_reviews`: false
  (`gh api repos/appshapes/brigade/actions/permissions/workflow`, 2026-11-24).
- The job's own token under the action: four agent steps of `update-dependencies.yml` ran under
  `contents: read` with no `id-token` (run 45359451690, at the earlier pin).
- The 41 commits between the pins, read on 2026-11-24. What bears on these workflows: a step with
  `allowed_bots`, which every agent step here has, gets its token through a credential helper and no longer in
  the checkout's remote URL, and keeps the earlier permission mode where other runs now default to auto mode;
  a cancelled run stops its session and revokes the App's token, where a cancelled reviewer's token lived on
  for up to its hour (`review pr` cancels a run on every new push); the action puts back `GITHUB_ENV`,
  `GITHUB_PATH` and git's configuration after a session; pull requests from forks are skipped by default.
- `scripts/ci/agent_token_test.go` holds the decision on every workflow and is shown failing on twenty-five
  mutated copies.
- Which token an agent's git presents, shown on 2026-11-24 on pull request #130, on the new pin. With the
  fixer job's `contents` at `read` for one commit (426d7158) the agent's push was refused as
  `github-actions[bot]` (run 45502056649): the checkout logs `persist-credentials: true`, and the action logs
  "No existing authentication headers to remove" before it sets up its helper. With `write` back (cecf7ce5)
  a request for changes started the fixer, which pushed ba21eb9a, and `approve-fixer-runs` approved the two
  runs GitHub held on it (run 45507085498). The reviewer's runs log the same two lines; no push was tried
  from its job.

## Consequences

- **In five workflows GitHub's `permissions:` is the boundary of what an agent can do**, and a reader can
  check it in the file: the three scheduled jobs, the email and the release's notes.
- **The boundary of the reviewer's `gh` is the exchange's.** It is a service of the action's vendor, asked by an input
  whose lowering of a default is not on the action's page. It was measured once and could change without a
  commit here; this repository would not notice. A cheap way to notice, not built: a scheduled run of the
  measurement's third mint, red when the impossible ref is refused for anything but the permission.
- **The fixer still holds a token that can push and merge**, on this repository alone: its `gh` holds the
  App's, and its `git push` goes with its job's own token, for which the job grants `contents: write`. The
  runs that push raises are held until `approve-fixer-runs` approves them. What keeps the agent to its branch
  is its settings' denies and the `master` ruleset, as before.
- **The boundary of the reviewer's git is GitHub's.** It presents its job's own token, `contents: read`, and
  the test holds that block whole. So the reviewer has two boundaries, one for each of its two tools.
- **A release's notes cross a job.** `publish` takes the artifact `notes` uploaded. A fault in that hand-over
  shows at the next release; a dry run takes the same path up to the two writes themselves.
- **A new job that runs the action** is handed the job's token, or is added to the test's table with its
  reason and its wish. A new grant of `id-token` fails the test until it is named.
- **To revisit** when the measurement is repeated and differs, when the action documents or removes the
  lowering of a default, or when the owner lets Actions approve pull requests, which removes one of the two
  reasons for the reviewer's exception.

## References

- Work item 438 (the owner's rulings, the probe's log and its reading), work item 433 and ADR 0032 (the
  scheduled jobs, where the reading began), work item 397 (the reviewer's whole login).
- `.github/workflows/review-pull-request.yml`, `claude.yml`, `release-notes.yml`, `send-release-notes.yml`,
  `scripts/ci/agent_token_test.go`.
- [Automation](../agent/repository/automation.md), which states the rule;
  [Use the repository's agents](../developer/contribute/repository-agents.md).
- `anthropics/claude-code-action` at eb56b6666facdbd501035f4a0269d8caee2e7a8b: `src/github/token.ts`,
  `src/github/operations/git-config.ts`, `docs/security.md`, `docs/configuration.md`.
