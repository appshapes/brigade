# Use the repository's agents

The repository's workflows run agents in two places: on a pull request that a person opens, and in three jobs
that a schedule starts. An issue starts nothing: the loop that took an issue labelled `claude` as a work order
was removed on 2026-11-24 ([ADR 0032](../../decisions/0032-run-each-scheduled-job-as-one-workflow-named-by-a-standing-work-item.md)).

| Agent (`.claude/agents/`) | Role |
| --- | --- |
| `reviewer.md` | reviews a pull request against the repository's invariants; reviews a scheduled job's change inside its run; writes the monthly repository review |
| `developer.md` | applies the reviewer's blockers on a pull request's branch; fixes what a scheduled job's review found; writes the monthly dependency update |
| `documenter.md` | writes the monthly structural refresh of the persona pages: paths, make targets, versions, command names; never `docs/security.md`, `docs/protocol-v1.md` or the rule files |
| `release-notes-email.md` | writes and reviews the release-notes email |

## On a pull request

- **The review.** A pull request from a branch of the repository is reviewed by the reviewer agent
  (`review-pull-request.yml`). A fork's is not: it gets CI and a person. Title it
  `<work item>: <Imperative summary>`; the squash merge takes the title as the commit's subject.
- **The fixer.** A request for changes starts the developer agent on the branch (`claude.yml`), three times at
  most; then the pull request is handed back with a comment. `@claude <what you want>` in a comment or a review
  on the pull request starts it by hand, when you have write access, and so does the label `claude` put on the
  pull request. A comment on an issue starts nothing.
- **The merge.** Label the pull request `auto-merge` and it is merged once the reviewer approved its current
  head and the required checks are green (`auto-merge.yml`). Without the label it is yours to merge.
- **Whose review counts.** The reviewer agent reviews as `claude[bot]`, the Claude GitHub App, and the workflows
  compare that login whole, never by a prefix. Only its approval arms the merge, and only its requests for
  changes are counted against the fix cap of three.
- The hub approves the `ci` and `review pr` runs that a fixer's push raises at `action_required`, with the
  owner's token, the moment the fixer's job ends.

## The scheduled jobs

| Workflow | Runs | Its writer | Its pull request | Standing work item |
| --- | --- | --- | --- | --- |
| `update-documentation.yml` | the 1st, 09:17 UTC | `documenter.md` | merges when green | 434 |
| `update-dependencies.yml` | the 1st, 11:29 UTC | `developer.md`, dependency-update mode | merges when green | 435 |
| `review-repository.yml` | the 1st, 04:43 UTC | `reviewer.md`, review-repository mode | waits for the owner | 436 |

Until each has run once by hand in this form (work item 433), the three files carry `workflow_dispatch` alone
and the schedule above is not in them.

- **One run is the whole job.** The writer commits in the runner's checkout; a shell gate reads what the
  checkout holds and runs `pr-guard.sh` and the job's `make` targets; the reviewer reads the commits against
  the writer's brief; the writer gets one fix cycle; a second review is bounded to the first one's findings and
  to what the fix changed. An approved change is pushed as `scheduled/<job>-<date>` and opened as a pull
  request by a job that runs no agent. A month with nothing to change ends green with no pull request.
- **Start one by hand** with `gh workflow run <file>`. A run does nothing while the job's last pull request is
  still open: merge or close it first.
- **The number.** A scheduled job's pull request is titled `<its standing work item>: <the job's title>`. The
  three work items are closed on the board on purpose: they are the number the job's commits carry, never a
  piece of work, and the record of a run is its pull request.
- **When a run is red.** Its artifact has the writer's report, the gate's findings (`gate.txt`, and
  `gate-make.log` for the whole output) and the reviews; the run's summary says how it ended. Nothing landed:
  start the job again, or do the work in a session.
- **What an agent there can reach.** The job that runs the agents cannot write to the repository; the owner's
  token is in a job that runs none. The three files are one text from a marked line on: a step is changed in
  all three.

## Both

- Agents commit with plain `git` and never run `make commit`, `make push` or `make release`; a PR labelled
  `maintenance` is exempt from the execution-log-row rule, and its squash-merge title
  `<work item>: <Imperative summary>` satisfies the commit-message rule.
- Agent memory lives under `.context/plans/agent-memory/`, never under `.claude/`.
- `anthropics/claude-code-action` is pinned by full commit SHA, the only action in the repository that is, because
  it is the only one that runs a model.
- The workflows, their schedules and the required GitHub configuration: [scripts/ci/README.md](../../../scripts/ci/README.md).
