# Automation: the release-notes email, the agentic loop, pinned actions

- **The release-notes email** (card 43, `.github/workflows/send-release-notes.yml`) goes to a list kept by hand in
  the secret `RELEASE_NOTES_RECIPIENTS`. Never print, upload, commit or put on a card the address of a recipient:
  this repository is public, so the addresses are secrets, never variables and never a file, every reader is
  `bcc`, and the list is read after the last agent step. The send step's name is how the next run finds the last
  email: rename it in the workflow and in `scripts/ci/send-release-notes.sh` together. A release may show one
  picture under a change, made by the rules of `docs/email/README.md` (`make email-picture`).
- **Repository automation.** An issue labelled `claude` is a work order for the agentic loop
  (`.github/workflows/claude.yml`). An agent working on a PR branch follows `.claude/agents/developer.md`; the PR
  reviewer follows `.claude/agents/reviewer.md`; the monthly documentation refresh follows
  `.claude/agents/documenter.md`. Agents commit with plain `git` on their branch and never run `make commit`,
  `make push` or `make release`. A PR labelled `maintenance` is exempt from the execution-log-row rule (owner
  ruling, 2026-09-09); its squash-merge title, `<card>: <Imperative summary>`, satisfies the commit-message rule.
  Agent memory lives under `.context/plans/agent-memory/`, never under `.claude/`.
- **Pinned actions.** `anthropics/claude-code-action` is pinned by **full commit SHA**, the only action in this
  repository that is, because it is the only one that runs a model with repository write access; every other
  action stays on its major tag.
- **Hosted projects this repository keeps alive.** `keepalive.yml` has one job per hosted project, each reading
  its own pair of repository variables; `scripts/ci/keepalive_test.go` pins every job name and `vars.` expression
  against the Project Owner's page. A new project is a new job, a new pair and a row on that page.
