# Automation: the release-notes email, the scheduled jobs, the pull-request agents, pinned actions

- **The release-notes email** (card 43, `.github/workflows/send-release-notes.yml`, whose writer and reviewer both
  follow `.claude/agents/release-notes-email.md`) goes to a list kept by hand in
  the secret `RELEASE_NOTES_RECIPIENTS`. Never print, upload, commit or put on a work item the address of a
  recipient: this repository is public, so the addresses are secrets, never variables and never a file, every
  reader is `bcc`, and the list is read after the last agent step. The send step's name is how the next run
  finds the last email: rename it in the workflow and in `scripts/ci/send-release-notes.sh` together. The run
  history it is found in is kept for the repository's retention period only, 90 days since 2026-11-22 and seven
  before (work item 401): never
  make the script believe a mark with a run of the workflow missing after it, which would mail the list every
  release since an older email again. A release
  may show one picture under a change, made by the rules of `docs/email/README.md` (`make email-picture`).
- **The scheduled jobs** (work item 433, ADR 0032). Three jobs run on the first of each month, each as ONE
  workflow whose steps are the personas in turn: `update-documentation.yml` (its writer follows
  `.claude/agents/documenter.md`), `update-dependencies.yml` (`developer.md`, dependency-update mode) and
  `review-repository.yml` (`reviewer.md`, review-repository mode). After the writer come a shell gate, a
  reviewer (`reviewer.md`, scheduled-job mode), one fix cycle (`developer.md`) and a second review; an approved
  change leaves as a pull request, which merges by itself when the required checks are green, except the
  repository review's, which waits for the owner. No issue is opened and no issue is a work order: an issue
  labelled `claude` starts nothing.
  - **A scheduled job names its work by its standing work item.** The number is `STANDING_WORK_ITEM` in the
    job's workflow file (434, 435 and 436; [conventions.md](conventions.md) says what such a work item is). The
    pull request is titled `<that number>: <the job's title>`, the squash merge takes the title, and the record
    of one run is its pull request. A new scheduled job gets a standing work item of its own first.
  - **No step that runs a model holds a token that can write to the repository.** The job `work` is
    `contents: read` and every agent step is handed that token (`github_token`): without the input the action
    mints a token of the Claude GitHub App, which the job's permissions do not bound. Never give that job a
    write permission, `id-token` or `GH_ACTIONS_TOKEN`, and never drop the input. The job `land` holds the
    owner's token in one step and runs no agent and nothing that came out of the writer's commits.
  - **The three are one text** from a marked line on, and the shell steps are one script,
    `scripts/ci/scheduled-job.sh`, which the job `work` runs from the commit the run started on. Change a step
    in all three; what is a job's own (its number, title, gate, how it merges, the writer's brief) is above the
    line. `scripts/ci/scheduled_job_test.go` holds all of it.
  - A run that fails is a red run with the report, the gate's findings and the reviews as its artifact. The
    owner hears of it from GitHub's own mail for a failed run; nothing else tells anyone.
- **The agents on a pull request.** A pull request from a branch of the repository is reviewed by the reviewer
  agent (`review-pull-request.yml`, `.claude/agents/reviewer.md`); its request for changes, or a person's
  `@claude` on the pull request, starts the fixer on the branch (`claude.yml`, `.claude/agents/developer.md`);
  `auto-merge.yml` merges a pull request labelled `auto-merge` once the reviewer approved its head. A pull
  request whose branch begins `scheduled/` is a scheduled job's and gets none of the three. Agents commit with
  plain `git` and never run `make commit`, `make push` or `make release`. A PR labelled `maintenance` is exempt
  from the execution-log-row rule (owner ruling, 2026-09-09); its squash-merge title,
  `<work item>: <Imperative summary>`, satisfies the commit-message rule.
  Agent memory lives under `.context/plans/agent-memory/`, never under `.claude/`.
- **An agent's token** (work item 438, ADR 0033). A step that runs `anthropics/claude-code-action` is handed the
  job's own token (`github_token: ${{ github.token }}`), in a job that grants `contents: read`, nothing at write
  and no `id-token`: handed none, the action mints a token of the Claude GitHub App, which the job's
  `permissions:` do not bound. A write is made by a job that runs no agent, from files the agents left. Two
  jobs hold the App's token, each named in `scripts/ci/agent_token_test.go` with its reason and the wish its
  step sends: the reviewer (`review-pull-request.yml`), whose step asks for it at `contents: read` and
  `issues: read`, so that it reviews as `claude[bot]` and cannot push or merge; and the fixer (`claude.yml`),
  whose `gh` and comments are `claude[bot]`'s. That token is the agent's `gh`'s: an agent's `git push` goes
  with the job's own token, the checkout's credential, which is why the fixer's job is the one job with an
  agent that grants `contents: write`, and why the reviewer's `contents: read` keeps its git from pushing:
  never raise it. Never drop a line of the reviewer's `additional_permissions`, never grant `id-token` in
  another job, and never add a job to that table without the owner: what such a token can do is set by
  Anthropic's exchange and was measured once (the ADR has what was and what was not).
- **Pinned actions.** `anthropics/claude-code-action` is pinned by **full commit SHA**, the only action in this
  repository that is, because it is the only one that runs a model with repository write access; every other
  action stays on its major tag.
- **Hosted projects this repository keeps alive.** `keepalive.yml` has one job per hosted project, each reading
  its own pair of repository variables; `scripts/ci/keepalive_test.go` pins every job name and `vars.` expression
  against the Project Owner's page. A new project is a new job, a new pair and a row on that page.
