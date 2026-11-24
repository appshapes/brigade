# 0032. Run each scheduled job as one workflow, named by a standing work item

- **Status:** Accepted
- **Date:** 2026-11-24
- **Supersedes:** none

## Context

Three jobs run on the first of each month: the structural documentation refresh, the dependency update and the
repository review. Until this decision each only opened an issue labelled `claude`. The issue started an
issue-driven loop joined by GitHub's events: `claude.yml` ran a writer and its job `open-pr` opened a pull
request, `review-pull-request.yml` reviewed it, `claude.yml` ran again as the fixer, `auto-merge.yml` merged. One
month's work was four or more runs of four workflows.

That loop cost two things. A pull request it opened for an issue that named no work item was titled `15: …`,
the number of the build-out card of the Trello days, which is no work item of the Brigade Board; the title
became the commit on `master`, so the commit-message rule held only by an exception written into seven files.
And a work order could vanish between two of its runs: on 2026-10-01 the run that should have started the
dependency update was cancelled in a concurrency group and nothing said so, and on 2026-11-22 an agent ended
with two pages edited and nothing committed and its issue was closed as finished (work item 398 repaired both).

The owner ruled on 2026-11-24, on the third call of work item 314 and on work item 433: `15` goes; each job
becomes one workflow whose steps are the personas in turn, as `release-notes.yml` and `send-release-notes.yml`
are built; a scheduled run names its work by a standing work item of its own, since a runner cannot reach the
board; and the issue-driven loop goes unless a workflow needs it. Asked where the standing work items should
sit so that they do not read as open work, he chose to close them rather than give the board a list for them.

What a workflow needs of the loop was read from the repository's history on that day. The parts that serve a
pull request a person opens stay: the reviewer, the fixer and the merge of a pull request labelled
`auto-merge`. The parts that only an issue reached go.

## Decision

Each scheduled job is one workflow, under its old file name, and one run of it is the whole job. No issue is
opened, and no issue is a work order: an issue labelled `claude` starts nothing, and `@claude` in a comment
starts an agent on a pull request only.

A run has three jobs.

- `sweep` runs no agent. It removes the branch of an earlier run whose pull request is merged or closed, and it
  stops the run while a pull request of the job is still open, and when the run was started on a branch other
  than `master`.
- `work` runs the personas as steps: the writer, by its agent file; a shell gate; a reviewer; one fix cycle;
  the gate again; a second review bounded to the first one's findings and to what the fix changed; the decision.
  Nothing in this job can write to the repository. Its token is `contents: read`, and every agent step is
  handed that token, so the action mints none of its own. The writer commits in the checkout and never pushes.
  The commits leave the job as a `git bundle`, after a look for a token in everything that leaves.
- `land` runs no agent. It pushes the bundle's commits as the branch `scheduled/<job>-<date>` with its own
  token and opens the pull request with the owner's token, so that `ci.yml` runs on it. For the documentation
  refresh and the dependency update it arms the merge for the commit it pushed, which the five required
  checks of `master` then let through; the repository review's pull request waits for the owner, as it always
  did. `land` runs nothing
  that came out of the bundle: its checkout stays at the commit the run started on, the script it runs is that
  commit's, and the bundle is fetched into a ref and pushed.

A scheduled job names its work by its standing work item: a work item of the board, added once, closed on
purpose, whose number the job's workflow file carries. The pull request is titled
`<that number>: <the job's title>` and the squash merge takes the title, so the commit-message rule holds with
no exception. The record of one run is its pull request. The three are 434, 435 and 436.

The shell steps are one script, `scripts/ci/scheduled-job.sh`, and the three workflow files are one text from a
marked line on; what is a job's own (its number, its title, its gate, how its pull request merges, the
writer's brief) is above that line. `review-pull-request.yml` skips a pull request whose branch begins
`scheduled/`: it was reviewed, twice at most, before it existed.

## Alternatives considered

- **Keep the loop and give its pull requests a real number.** It removes `15` and nothing else: the work would
  still be four runs joined by events, and the owner asked for the later technique.
- **A second review on the pull request**, by `review-pull-request.yml`, with `auto-merge.yml` arming the merge
  as before. It needs no clause in that workflow, but it costs one more session for each job each month, and a
  request for changes there starts `claude.yml`'s fixer outside the run: the job would again not end in one
  run. The manager session decided against it under the owner's ruling.
- **One reusable workflow called by three.** It would say the steps once without a test to hold three copies
  equal. The one time `claude-code-action` ran under `workflow_call` in this repository it refused (under a tag
  push, run 34537045037), and under a schedule it is not tried. Three whole files run the action exactly as
  `send-release-notes.yml` does on its schedule, and the test that holds them to one text costs less than
  finding out on the first of a month.
- **Push to `master` without a pull request.** The ruling says a run's record is its pull request, the
  `supabase`, `e2e` and `server` checks cannot run before a direct push, and the repository review has to wait
  for the owner.
- **Let the writer push its own branch**, as the loop's writer did, with `contents: write` on its job. Then
  every agent step of the job could push, the reviewer included. With the bundle no step that runs a model
  holds a token that can write, and the job that pushes takes the commits as data.
- **A list of their own for the standing work items, or the hidden list of parked work.** A list changes the
  board of the five repositories; the parked list would mix what never ends with what someone may wake or
  close. The owner chose to close them: a closed work item is in no listing and is still shown by its number.
- **A workflow on `pull_request: closed` to remove a job's branch at once.** It would be one more run joined by
  an event, on every pull request that closes. A merged pull request's branch is removed at once by the
  repository's own setting; a closed one's at the job's next run, by `sweep`, which the job needs anyway to
  know whether a pull request of its own is still open.

## Evidence

Read on 2026-11-24 from the tree at fff81e31 and from GitHub (`gh pr list`, `gh issue list`, `gh run list`).

- 13 issues were ever labelled `claude`: 4 by hand, the last on 2026-09-17, and 9 by the three schedules.
- `review-pull-request.yml` reviewed 22 pull requests of another member's, the last on 2026-11-08, and one of
  the owner's on 2026-11-22; 16 of that member's merged pull requests carry the label `auto-merge`.
- Of the 33 runs of `claude.yml` that a review started and that got past the job's condition, 23 were on a
  pull request a person had opened, the last on 2026-10-26.
- Of 56 runs of `claude.yml` that a comment started, 3 got past the condition and all 3 were cancelled; none
  was a comment on an issue that ran to its end. The `claude` label on a pull request never started an agent
  (41 runs, all skipped).
- `claude-code-action` at the pin (c0949c50, `src/github/token.ts`, `src/modes/agent/index.ts` and
  `src/github/operations/git-config.ts`, read 2026-11-24): given no `github_token` input it exchanges the
  job's identity for a token of the Claude GitHub App, puts it in the session's environment as `GITHUB_TOKEN`
  and `GH_TOKEN`, and writes it into the checkout's remote URL. What that token may do is decided by the
  exchange, not by the job's `permissions:` block. Given the input, it uses that token and exchanges nothing.
  Read, not measured: no token's permissions were tried.
- The past runs, for what a run in the new form will cost: a writer's session took 439 to 1069 s for the
  documentation refresh and the repository review, a reviewer's 80 to 576 s, and the gate's targets about five minutes in `ci.yml`'s jobs. No run in
  the new form existed when this was written.
- No file of the tree carries a string of the shape of a model token or of a GitHub token
  (`git grep` with the script's pattern, 2026-11-24), so the look for one reads every line of a change.
- `scripts/ci/scheduled_job_test.go` runs every verb of the script in fixture repositories, 49 cases, and holds
  the three workflows; each case and each line it holds is shown failing on a mutated copy.
- An agent review of the working tree before the first push found ten things, four of them ways a change
  could land that nobody had reviewed or a correct month could end red: a run started on another branch, a
  verdict read from a part of its first line, a fix cycle that left nothing, and a merge armed for whatever
  head the branch would have. Each is closed in the script and held by a case.

## Consequences

- **A token cannot leave by accident.** The writer holds two: the model's, and the job's read-only token,
  which the action writes into the remote URL, so `git remote -v` in a report would print it. Before anything
  leaves `work`, as the bundle or as the run's artifact, one step compares both tokens whole with every commit
  (message and patch) and with every file of the job's directory (the report, the gate's output, the reviews,
  the execution logs), and looks for the shape of a model token and of a GitHub token in the same. A hit fails
  the run, names the place and prints no line; nothing is uploaded and nothing is bundled. `land` looks for the
  shapes again in the commits and the report, because it trusts nothing the other job says of its own work.
  The release-notes workflows do less: `release-notes-lint.sh` refuses three Supabase shapes in the draft and
  compares nothing with the tokens the job holds, and their agents hold the App's token, not the job's.
- **It holds against a mistake, not against an agent that means harm.** The agents share the job `work` with
  its shell steps, and a step can write the environment of the next; a model that was turned could also send a
  token somewhere by a command it is allowed to run. What it can reach is the model's token, which it holds by
  construction, and a read-only token of a public repository. It cannot push, cannot open a pull request and
  cannot reach the owner's token, which is why `land` is a job of its own.
- **The same reading applies to three workflows this decision does not change.** `review-pull-request.yml`,
  `release-notes.yml` and `send-release-notes.yml` pass no `github_token`, so their agents hold the App's
  token. Their headers say the job's `contents: read` is what keeps a reviewer from pushing; whether it is has
  not been measured. That is the owner's to look at, and is told on work item 433.
- **A run that fails is a red run and nothing else.** Its artifact has the writer's report, the gate's findings
  and the reviews, and the run's summary says how it ended. The owner hears of it from GitHub's own mail for a
  failed run: for a scheduled run it goes to the account that last changed the file's `schedule:`, for a run
  started by hand to whoever started it. No issue is opened and no message is sent; the work is simply not
  done that month until someone starts the job again or does it in a session.
- **A push to an armed pull request is not reviewed.** The merge is armed for the commit the run pushed, and
  GitHub checks that commit when the merge is armed, not when it is made. While the required checks run, a few
  minutes, someone with write access can push to the branch, or call the fixer on the pull request with
  `@claude`, and what they push is merged with CI and without a review: `review-pull-request.yml` skips the
  branch. `auto-merge.yml` holds an approval to the current head; this does not. The repository review's pull
  request is not armed and has no such window. Closing it would mean waiting for the checks inside `land` and
  merging with the commit named, which keeps the owner's token in a step for the length of a CI run.
- **One pull request of a job at a time.** While the repository review's pull request waits for the owner, the
  next month's run of that job does nothing and says so.
- **Three files to keep as one.** A change to a step is made in all three; the test fails until it is. A new
  scheduled job gets a standing work item first.
- **A person can no longer hand work to an agent by an issue.** A pull request is the way in: its review, its
  fixer and its merge are as they were.
- **To revisit** when `claude-code-action` is shown to run under `workflow_call` from a schedule (one reusable
  workflow could then replace the three), or when a runner can reach the board (a run could then add a work
  item for itself, which the owner asked about first).

## References

- Work item 433 (the rebuild, the owner's rulings and the design as accepted), work item 314 (its third call:
  `15` does not stand), work items 434, 435 and 436 (the standing work items), work item 398 (the two ways a
  work order vanished), work item 397 (the reviewer's whole login).
- `.github/workflows/update-documentation.yml`, `update-dependencies.yml`, `review-repository.yml`,
  `scripts/ci/scheduled-job.sh`, `scripts/ci/scheduled_job_test.go`.
- [Automation](../agent/repository/automation.md) and [Conventions](../agent/repository/conventions.md), which
  state the rules; [Use the repository's agents](../developer/contribute/repository-agents.md).
