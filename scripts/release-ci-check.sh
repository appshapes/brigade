#!/bin/sh
# usage: scripts/release-ci-check.sh <full commit sha>
#        release_over_ci=<run id | missing:<full commit sha>> scripts/release-ci-check.sh <full commit sha>
#
# Says what `ci.yml` made of a commit, for a release to stand on (work item 399, ADR 0015). Until 2026-11-22 no
# release looked: `make release` tagged about thirty seconds after its push, CI takes about four minutes, and two
# plugin releases went out over a red job. `scripts/release-prep.sh` and `scripts/server-tag.sh` both call this.
#
#   a run in progress    is waited for, in every case, and then read
#   a green run          exit 0: the release goes on
#   a red run            exit 1, unless release_over_ci is that run's id
#   no run at all        exit 1, unless release_over_ci is `missing:<that commit's sha>`
#   a run still going    exit 1 after the limit: nothing overrides a run nobody has seen end
#
# The owner's ruling (2026-11-22): stop, with an override. `release_over_ci` is the one named variable, it is the
# owner's to allow, and a session passes it only on his word. Its value has to name what was found, the red run's
# id or `missing:` and the commit that has no run, so that the word given for one commit covers no other: not the
# release commit after the commit the release started from, and not the next release from a shell the variable
# was left in (`make` takes a variable from the environment as well as from its command line).
#
# "Green" is the run's own conclusion, `success`: every job of ci.yml, `windows` included, although the `master`
# ruleset does not require that one. The newest run of the workflow on the commit is the one read, so a run that
# was started again counts as it stands now.
#
# Read with `gh`, which must be logged in on the machine that releases. When GitHub cannot be asked the script
# stops: a release that cannot see CI is the release this script exists to end. One answer that fails is not
# that: a wait asks some sixteen times in four minutes, so the script asks again and gives up after five
# failures in a row.
#
# Three numbers a caller or a test may set, in seconds: RELEASE_CI_APPEAR_SECONDS (how long to wait for a run to
# appear at all, 120: a run shows within seconds of a push, and a commit that was pushed inside another push's
# range never gets one), RELEASE_CI_WAIT_SECONDS (how long to wait for a run to end, 1800) and
# RELEASE_CI_POLL_SECONDS (15, at least 1: both waits are counted in polls).
#
# POSIX sh, `shellcheck -s sh` clean.
set -eu

say() { printf 'release-ci: %s\n' "$1"; }
die() { printf 'release-ci: %s\n' "$1" >&2; exit 1; }

workflow=ci.yml
sha=${1:-}
override=${release_over_ci:-}
appear=${RELEASE_CI_APPEAR_SECONDS:-120}
limit=${RELEASE_CI_WAIT_SECONDS:-1800}
poll=${RELEASE_CI_POLL_SECONDS:-15}

case $sha in
  ''|*[!0-9a-f]*) die "usage: scripts/release-ci-check.sh <full commit sha>   (got '$sha')" ;;
esac
# gh matches a run by the whole sha alone: a short one finds nothing, which would read here as "no run".
[ "${#sha}" -eq 40 ] || die "pass the full 40-character commit sha, not '$sha': gh finds no run by a short one"
case $override in
  ''|missing:*) ;;
  *[!0-9]*)     die "release_over_ci='$override' is neither a run's id nor 'missing:<the commit's sha>'" ;;
esac
for n in "$appear" "$limit" "$poll"; do
  case $n in
    ''|*[!0-9]*) die "RELEASE_CI_APPEAR_SECONDS, RELEASE_CI_WAIT_SECONDS and RELEASE_CI_POLL_SECONDS are whole numbers of seconds (got '$n')" ;;
  esac
done
[ "$poll" -ge 1 ] || die "RELEASE_CI_POLL_SECONDS is at least 1: the waits are counted in polls, and 0 would ask GitHub without a pause"
command -v gh >/dev/null 2>&1 || die "gh is not on PATH: a release reads $workflow's run with it (https://cli.github.com), logged in"

# The newest run of the workflow on the commit as one line, "<id> <status> <conclusion> <url>", or nothing.
lookup() {
  gh run list --workflow "$workflow" --commit "$sha" --limit 20 \
    --json databaseId,status,conclusion,url \
    --jq 'sort_by(.databaseId) | last | select(. != null) | [(.databaseId | tostring), .status, (if (.conclusion // "") == "" then "-" else .conclusion end), .url] | join(" ")'
}

how_to() {
  say "Not releasing. To release anyway, which is the owner's to allow, run the same command again with release_over_ci=$1"
}

waited_for_a_run=0
waited_for_its_end=0
told_waiting=""
failed_in_a_row=0
while :; do
  if ! line=$(lookup); then
    failed_in_a_row=$((failed_in_a_row + 1))
    [ "$failed_in_a_row" -lt 5 ] || die "could not ask GitHub for $workflow's runs on $sha, five times in a row (is gh logged in, is the network up?): not releasing blind"
    say "gh could not list $workflow's runs on $sha; asking again in ${poll}s"
    sleep "$poll"
    continue
  fi
  failed_in_a_row=0
  if [ -z "$line" ]; then
    if [ "$waited_for_a_run" -ge "$appear" ]; then
      say "$workflow on $sha: no run"
      if [ "$override" = "missing:$sha" ]; then
        say "releasing over a missing run: release_over_ci=missing:$sha was given"
        exit 0
      fi
      [ -z "$override" ] || say "release_over_ci='$override' does not name this commit's missing run"
      how_to "missing:$sha"
      exit 1
    fi
    if [ "$told_waiting" != none ]; then
      say "$workflow has no run on $sha yet; waiting up to ${appear}s for one"
      told_waiting=none
    fi
    sleep "$poll"
    waited_for_a_run=$((waited_for_a_run + poll))
    continue
  fi
  IFS=' ' read -r id status conclusion url <<EOF
$line
EOF
  if [ "$status" = completed ]; then
    break
  fi
  if [ "$waited_for_its_end" -ge "$limit" ]; then
    say "$workflow on $sha: run $id is still $status after ${limit}s ($url)"
    die "not releasing over a run that has not ended: run the same command again when it has"
  fi
  if [ "$told_waiting" != "$id" ]; then
    say "$workflow on $sha: run $id is $status; waiting for it to end ($url)"
    told_waiting=$id
  fi
  sleep "$poll"
  waited_for_its_end=$((waited_for_its_end + poll))
done

say "$workflow on $sha: $conclusion (run $id, $url)"
if [ "$conclusion" = success ]; then
  [ -z "$override" ] || say "release_over_ci='$override' was given and is not needed: the run is green"
  exit 0
fi
if [ "$override" = "$id" ]; then
  say "releasing over the red run $id: release_over_ci=$id was given"
  exit 0
fi
[ -z "$override" ] || say "release_over_ci='$override' does not name this run"
how_to "$id"
exit 1
