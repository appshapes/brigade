# 0014. Stop a release over a red or missing CI run, with an override

- **Status:** Accepted
- **Date:** 2026-11-22
- **Supersedes:** none

## Context

The repository's rule is that `master` with CI green is deployed (`docs/agent/repository/conventions.md`). A
release is where a member's machine gets the work, and it was the one step that did not ask. Until this
decision `scripts/release-prep.sh` (`make release`) and `scripts/server-tag.sh` (`make server-tag`) looked up no
run at all. The plugin release pushed its tag about thirty seconds after the push of its release commit, while
`ci.yml` takes about four minutes, so the release ran ahead of the answer even on a healthy tree.

Two plugin releases went out over a red run. Read with `gh` on 2026-11-22: the tag `v0.6.6` points at `56d127b5`,
whose run 35362774140 concluded `failure`, and `v0.43.0` at `51d3ff0b`, whose run 40954800938 concluded
`failure`. The server has less behind it still: the `server-release` workflow runs no test, so the `server` job
of `ci.yml` is the only test a server release has.

The repository review of 2026-11-21 raised it (work item 399) and left one choice to the owner: what a release
does when the run on its commit is red or missing.

## Decision

A release reads `ci.yml`'s run on the commit it releases, says what it is, and stands only on a green one.

- A run in progress is waited for, in every case, and then read.
- Over a green run the release goes on.
- Over a red run, or no run at all, the release stops and says how to release anyway: one named variable on the
  command line, `release_over_ci`. That variable is the owner's to allow, and a session passes it only on his
  word.

This is the owner's ruling of 2026-11-22: "Stop, with an override", so that a release over a red run is a
deliberate act.

The rest is this work item's own, inside that ruling:

- **The variable names what was found.** `release_over_ci=<the red run's id>` or
  `release_over_ci=missing:<the commit's sha>`. Any other value does not override, the word for one commit covers
  no other, and nothing overrides a run that has not ended.
- **The tag is put on the commit whose run was read**, by its sha, not on wherever `HEAD` is after the wait.
- **Green is the whole run**: the run's own conclusion, `success`, so every job of `ci.yml` counts, `windows`
  included, though the `master` ruleset does not require that one.
- **The plugin release asks twice.** About the commit it starts from, before anything is changed, and about the
  release commit, between its push and the tag. The server release asks once, about the head it tags.
- **A stop after the push can be resumed.** The same `make release` again finds nothing to commit and goes on
  from the check, once the run is green or with the override.
- **When GitHub cannot be asked, five times in a row, the release stops**, and the override does not apply.
- **A release starts from a commit origin has**, and from a version origin has no tag for.

## Alternatives considered

The three the owner was given:

- **Say so and go on.** The script would print the run's result and release whatever it was. Not chosen: it is
  what the two releases above would have done, with one more line in a terminal.
- **Stop with no override.** Not chosen. The ruling keeps a way to release over a red run, as a deliberate act
  on the owner's word.
- **Stop with an override.** Chosen.

Inside the ruling:

- **A switch, `release_over_ci=1`, or a bare `missing`.** Lost to a value that names the run or the commit.
  `make` takes a variable from the environment as well as from the command line, so a switch exported once would
  override every later release in that shell, and a bare `missing` given for the commit a release starts from
  would also cover a release commit whose run was only slow to appear. A run's id fits one run and a sha one
  commit.
- **Only the jobs the ruleset requires.** Lost: `v0.43.0` is in the Context because `windows` was red, and
  `windows` is not required. A release that ships a Windows binary stands on the Windows job.
- **One check, of the release commit alone.** Lost: when the commit the release starts from is already red, that
  check would stop with a release commit pushed that pins a version nothing published backs, which
  `scripts/release-prep.sh`'s header describes as a state to get out of with care. The first check stops before
  anything is changed.
- **A check in `release.yml` instead.** Lost for now: the tag would already be public when the workflow refused,
  and `make server-tag` has no such workflow step to refuse in. It could be added beside this.

## Evidence

- `scripts/ci/release_ci_check_test.go`: the check run against a stand-in `gh`, eighteen answers and overrides,
  with seven mutations of the script each caught by its case.
- `scripts/ci/release_prep_ci_test.go`: both release scripts run to their last step in fixtures whose `origin` is
  a bare repository. A tag reaches `origin` only over a green run or the override that names the run.
- The check run on this repository on 2026-11-22: `63e466e3` printed `success (run 45029570829)`; `51d3ff0b`, the
  commit of `v0.43.0`, printed `failure (run 40954800938)` and stopped, went on with
  `release_over_ci=40954800938` and stopped with any other value; `dc59f5c0`, a commit that was pushed inside
  another push and so has no run, printed `no run` and stopped. Both scripts were also run for real in a scratch
  clone whose `origin` is a local copy, against those commits' real runs; the execution log's row has the lines.
- Not measured when this was written: a real release. None is cut for this decision; the next one is the first
  to go through both checks.

## Consequences

- A plugin release takes about as long as CI longer, since it waits for the release commit's run.
- The machine that releases needs `gh`, logged in. A release cannot be cut offline.
- A flaky job stops a release. The way on is to run the failed jobs again and then the same command, not the
  override.
- `docs/agent/repository/gates.md` carries the rule, and that a tag pushed by hand follows the same check.
- To revisit: if `server-release.yml` or `release.yml` gains a test or a check of its own, or if the wait
  becomes long enough that people reach for the override out of habit.

## References

- Work item 399 on the Brigade Board, with the owner's ruling as its comment of 2026-11-22.
- `scripts/release-ci-check.sh`, `scripts/release-prep.sh`, `scripts/server-tag.sh`.
- `docs/agent/repository/gates.md` ("A release stands on a green run"), and
  `docs/developer/release/cut-a-release.md` ("The two CI checks").
