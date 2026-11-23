#!/bin/sh
# usage: scripts/ci/release-guard.sh <full commit sha> <tag>
#
# The guard of the two release workflows, release.yml and server-release.yml (work item 416): before anything is
# built or published for a tag, the tag's commit is on `master` and `ci.yml`'s run on it is green.
#
# `make release` and `make server-tag` have asked the second question already (work item 399, ADR 0015), on the
# machine that releases. A tag pushed by hand reached the workflows with no check at all: the tag ruleset
# (`protect-release-tags`) forbids changing and deleting a release tag and does not forbid creating one, and the
# rule against tagging by hand was a sentence. This is the same check, by the same script,
# scripts/release-ci-check.sh, where a tag pushed by hand meets it too. It does not make an override
# impossible by hand: whoever can push a tag can write the line below into its message. What it stops is the tag
# pushed without the check, and the tag on a commit `master` does not hold.
#
# Why `master` as well: `ci.yml` runs for a pull request too, so a commit of a branch that was never merged can
# have a green run of its own, and the CI check alone would let a tag on it through. Asked of GitHub and not of
# the checkout, which in server-release.yml holds one commit.
#
# The override (the owner's ruling of 2026-11-23, ADR 0028). The scripts' check stops over a red or a missing run
# unless `release_over_ci` names it, which is the owner's to allow (ADR 0015). A tag push carries no input, so
# the scripts write the override they were given into the tag's message, as a line `release_over_ci=<value>`,
# and the guard reads it there and hands it to the same check, which takes it only when it names this commit's
# run or this commit. One run, no second trigger, no setting left behind for the next release. A tag pushed by
# hand has no such line, and over a red or a missing run it publishes nothing. The message is read from GitHub
# and not from the checkout, where an annotated tag may have been fetched as its commit alone.
#
# For a release the scripts made the run is green and has ended, so the guard costs four lookups. For a run
# that has not shown yet it waits one minute, and for one still in progress three, and no longer
# (RELEASE_CI_APPEAR_SECONDS and RELEASE_CI_WAIT_SECONDS; the script's own limits are two minutes and thirty,
# for a person at a terminal): a release job has its own time limit, and the answer then is to run the
# workflow's run again once CI has ended.
#
# It reads GITHUB_REPOSITORY and asks with `gh`, which needs GH_TOKEN and, for the runs, `actions: read`.
#
# POSIX sh, `shellcheck -s sh` clean.
set -eu

say() { printf 'release-guard: %s\n' "$1"; }
die() { printf 'release-guard: %s\n' "$1" >&2; exit 1; }

sha=${1:-}
tag=${2:-}
repo=${GITHUB_REPOSITORY:-}
case $sha in
  ''|*[!0-9a-f]*) die "usage: scripts/ci/release-guard.sh <full commit sha> <tag>   (got '$sha')" ;;
esac
[ "${#sha}" -eq 40 ] || die "pass the full 40-character commit sha, not '$sha'"
case $tag in
  ''|*[!A-Za-z0-9._-]*) die "usage: scripts/ci/release-guard.sh <full commit sha> <tag>   (got the tag '$tag')" ;;
esac
[ -n "$repo" ] || die "GITHUB_REPOSITORY is not set: this runs in a release workflow"
command -v gh >/dev/null 2>&1 || die "gh is not on PATH"

# How master stands to the commit: `ahead` or `identical` when master holds it, `behind` or `diverged` when not.
if ! standing=$(gh api "repos/$repo/compare/$sha...master" --jq .status); then
  die "could not ask GitHub how master stands to $sha: nothing is published blind"
fi
case $standing in
  ahead|identical) say "$sha is on master" ;;
  *) die "$sha is not on master (master is '$standing' to it): nothing is published. A release is tagged from master by \`make release\` or \`make server-tag\`, never by hand" ;;
esac

# The override, from the tag's message. The tag's ref names a tag object when the tag is annotated, as the
# scripts make it, and the commit itself when it is not, which then has no message.
if ! ref=$(gh api "repos/$repo/git/ref/tags/$tag" --jq '.object.type + " " + .object.sha'); then
  die "could not ask GitHub for the tag $tag: nothing is published blind"
fi
over=
case $ref in
  "commit $sha") say "$tag has no message: no override" ;;
  "tag "*)
    # One line: the commit the tag names, then every line of its message that begins `release_over_ci=`.
    if ! named=$(gh api "repos/$repo/git/tags/${ref#tag }" \
      --jq '.object.sha + " " + (.message | split("\n") | map(select(startswith("release_over_ci="))) | join(" "))'); then
      die "could not ask GitHub for the message of the tag $tag: nothing is published blind"
    fi
    [ "${named%% *}" = "$sha" ] || die "the tag $tag names the commit '${named%% *}', not $sha: nothing is published"
    over=${named#* }
    case $over in
      '') say "the message of $tag carries no override" ;;
      *' '*) die "the message of $tag carries more than one release_over_ci line ($over): nothing is published" ;;
      *) over=${over#release_over_ci=}; say "the message of $tag carries release_over_ci=$over" ;;
    esac
    ;;
  *) die "the tag $tag is '$ref' on GitHub, which is neither the commit $sha nor a tag's message: nothing is published" ;;
esac

RELEASE_CI_APPEAR_SECONDS=${RELEASE_CI_APPEAR_SECONDS:-60} RELEASE_CI_WAIT_SECONDS=${RELEASE_CI_WAIT_SECONDS:-180} \
  release_over_ci=$over sh "$(dirname "$0")/../release-ci-check.sh" "$sha" ||
  die "ci.yml is not green on $sha and the message of $tag does not allow the release over it: nothing is published. The check's own advice above is for the release scripts; here, run this workflow's run again once ci.yml's run has ended green, and if it stays red the release takes the next version, made by \`make release\` or \`make server-tag\` with the owner's override"
say "ci.yml is green on $sha, or the message of $tag allows the release over it: the release goes on"
