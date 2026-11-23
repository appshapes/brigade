# 0028. Ask the CI check again in the release workflows, with the override in the tag's message

- **Status:** Accepted
- **Date:** 2026-11-23
- **Supersedes:** none

## Context

Since ADR 0015 a release stands on a green run of `ci.yml`: `make release` and `make server-tag` read the run on
the commit they release and stop over a red or a missing one unless `release_over_ci` names it. That check lives
in the two scripts, on the machine that releases. A tag reaches `release.yml` and `server-release.yml` by a push,
and a tag pushed by hand reached them with no check at all. The rule against it was a sentence in
`docs/agent/repository/gates.md`.

Read from the repository's settings on 2026-11-23: the tag ruleset `protect-release-tags` covers `v*` and
`server-v*`, forbids updating and deleting such a tag, has no bypass, and does not forbid creating one. And
`ci.yml` runs for a pull request as well as for `master`, so a commit that was never merged can have a green run
of its own.

ADR 0015 left a check in the workflows aside "for now" and said it could be added beside the scripts'. Work item
416 asks for it, and left one choice to the owner: a tag push carries no input, so how does the owner's override
reach a workflow.

## Decision

Each release workflow asks, before anything is built or published, what the scripts asked before they tagged:
`scripts/ci/release-guard.sh`, run on the tag's commit and the tag's name.

- **The tag's commit is on `master`.** Asked of GitHub (how `master` stands to the commit), and held before the
  run is read.
- **`ci.yml` is green on it**, by the same script as on the releasing machine, `scripts/release-ci-check.sh`,
  with waits that fit a release job: one minute for a run to appear and three for one in progress.
- **The override is read from the tag's message.** `make release` and `make server-tag` write the
  `release_over_ci` they were given into the message of the tag they make, as a line of its own,
  `release_over_ci=<value>`. The guard reads that line from the tag on GitHub and hands it to the check, which
  takes it only where it names this commit's red run or this commit's missing run, as ADR 0015 has it.
- **A tag pushed by hand over a red or a missing run publishes nothing.** It has no such line. Its tag stays,
  because a release tag can be neither changed nor deleted, and the release takes the next version.
- **A real release is from `master`.** `scripts/release-prep.sh` on another branch goes no further than a dry
  run: a tag from there would publish nothing and could not be removed.
- **The override is one word.** The check refuses, before any tag is made, a value that is neither a run's id
  nor `missing:` and a full sha, whether or not the run turns out to need it, so a line the guard cannot read
  never reaches a tag's message.

The override in the tag's message is the owner's ruling of 2026-11-23: "In the tag's message", put to him as one
run, no new trigger and no setting left behind. That the commit must be on `master` is this work item's own
call, told to the owner with the question; he said nothing against it.

In `release.yml` the guard is a step before the toolchain is set up. In `server-release.yml` it is a step of the
`guard` job, which every other job waits on.

## Alternatives considered

The four the owner was given:

- **In the tag's message.** Chosen.
- **A repository variable** that the workflow reads. Not chosen: it is a setting that outlives the release it
  was set for, the fault ADR 0015 named in a switch exported once, unless someone remembers to clear it.
- **A manual dispatch with an input.** Not chosen: it gives each workflow a second way to start, which a release
  made by the scripts never uses, and `release.yml` has none today on purpose.
- **No override in the workflows.** Not chosen: a release the owner allowed over a red run on the releasing
  machine would then stop in the workflow, with its tag public and unchangeable.

Inside the ruling:

- **Read the message from the checkout.** Lost: `actions/checkout` may fetch an annotated tag as the commit it
  names, and the message is then not there. GitHub is asked for the tag's ref and its object.
- **Write the override into the message only where the check used it.** Lost: the script would have to learn
  from the check's output whether it did. The check already refuses a value that names another run or another
  commit and says that one is not needed over a green run, so a line that was not needed changes nothing.
- **Only the CI check, without `master`.** Lost: the check alone would let through a tag on a pull request's
  commit whose own run was green.
- **A tag for the proof.** A real tag on a red commit would have shown the workflow itself stop. Lost: a tag
  named `v*` can never be removed, and any other name starts no release workflow.

## Evidence

- `scripts/ci/release_guard_test.go`: the guard against a stand-in `gh`, twenty-two answers (on and off
  `master`, green, red, no run, a run that does not end, each override that fits and each that does not, a tag
  with no message, two override lines, a tag that names another commit, GitHub not answering at each of its
  questions); the filter that reads the message run through a real `jq`; and that each workflow runs the guard
  before it builds, with a token that may read the runs.
- `scripts/ci/release_prep_ci_test.go`: both scripts run to their last step in fixtures whose `origin` is a bare
  repository; the tag's message at `origin` is the tag's title alone without an override, and carries the line
  with one. `scripts/ci/release_prep_test.go`: a real run on another branch is refused with nothing changed.
  `scripts/ci/release_ci_check_test.go`: four malformed overrides, each refused before GitHub is asked.
- The guard run on this repository on 2026-11-23, against GitHub. On the real tags `v0.58.0` and
  `server-v0.10.0` it printed `the release goes on`. On real commits, with tags that were made in a scratch clone
  and never pushed (a stand-in answered the two questions about the tag and nothing else): `e3e07811`, whose run
  45107355462 is red, exit 1, with a tag that has no message and with an annotated one; `3de6fa61`, a commit of
  `master` with no run, exit 1; the head of pull request 121, which `master` does not hold, exit 1 before the
  run was read; the red commit with `release_over_ci=45107355462` in the message, exit 0; with another run's
  id, exit 1; the commit with no run and `release_over_ci=missing:<its sha>`, exit 0.
- Not measured: the step inside a run of either workflow. No tag is pushed for this decision; the next release
  of each kind is the first to go through it.

## Consequences

- A release made by the scripts costs four lookups more in its workflow, since its run is green and has ended.
- A fault in the guard would stop a release in the workflow with its tag public. That is why it landed just
  after the release of 0.58.0 and not before it. The way out of such a stop is to run the workflow's run again
  where the fault was GitHub's, and the next version where it is the guard's.
- Whoever can push a tag can write the line into its message. The guard closes the tag pushed without thought
  and the commit that is not on `master`; an override stays a deliberate act that names one run or one commit.
- `server-release.yml`'s token gains `actions: read`.
- To revisit: if the tag ruleset comes to forbid creating a release tag except by an actor the scripts run as,
  the guard is a second lock and the message is still where the override travels.

## References

- Work item 416 on the Brigade Board, with the owner's ruling as its comment of 2026-11-23.
- [ADR 0015](0015-stop-a-release-over-a-red-or-missing-ci-run-with-an-override.md), whose check this asks again.
- `scripts/ci/release-guard.sh`, `scripts/release-ci-check.sh`, `scripts/release-prep.sh`,
  `scripts/server-tag.sh`, `.github/workflows/release.yml`, `.github/workflows/server-release.yml`.
- `docs/agent/repository/gates.md` ("A release stands on a green run") and
  `docs/developer/release/cut-a-release.md` ("The two CI checks").
