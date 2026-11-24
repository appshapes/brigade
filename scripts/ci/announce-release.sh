#!/bin/sh
# Announces a published release to the members, once, and closes the announcements of the releases before it.
# Called by .github/workflows/release-notes.yml's "Announce, once" step after the notes are published; the
# step was inline shell until work item 403, and is a file so that its two decisions can be tested and read
# against the repository's issues as they stand.
#
# The announcement is one issue titled "<version> is out — run /brigade:update". Until work item 403 nothing
# closed one: on 2026-11-22, 71 of the repository's 74 open issues were announcements, from 0.5.0 on, and the
# three that needed an answer were lost under them. Now the open announcement is the newest release's alone.
#
# 1. Open it once. The guard lists the bot's own issues through the issues endpoint, open and closed, and
#    compares titles exactly; the endpoint lists pull requests too, hence `.pull_request == null`. It replaced
#    `gh issue list --search "… in:title"`, which is backed by the search index: for v0.6.1 the index had not
#    caught up with the issue another run had created one second earlier, and both runs opened one (#10,
#    #11). A listing is consistent with the write, and the workflow's `concurrency` group keeps a second run
#    of one tag behind the first. An existing issue skips the opening and nothing else. So does a higher
#    version that is already announced: a dispatch by hand for an older tag rewrites that tag's notes and
#    announces nothing.
# 2. Close every open announcement of a version below the highest one announced, this release's included.
#    On a release that is every earlier one. On a dispatch by hand for an older tag it is still the older
#    ones, so the newest release's announcement is never the one closed. An issue is closed only when the
#    bot opened it and its whole title is the announcement's form: an issue that names a version in passing,
#    or that a person opened, is never touched. No comment is written on a closed one.
#
# An error from the API fails the script loud, before anything is written where it can: the notes are
# published either way, and a dispatch for the same tag does again whatever was left.
#
# Usage: RELEASE_TAG=v0.58.0 GITHUB_REPOSITORY=<owner>/<name> GH_TOKEN=… scripts/ci/announce-release.sh [plan]
# With `plan` it reads the issues and prints what it would open and close, and writes nothing. It needs `gh`
# and `jq`.
set -eu

mode=${1:-announce}
case $mode in
  announce|plan) ;;
  *) echo "announce-release: usage: announce-release.sh [plan]" >&2; exit 2 ;;
esac
: "${RELEASE_TAG:?announce-release: RELEASE_TAG is not set}"
: "${GITHUB_REPOSITORY:?announce-release: GITHUB_REPOSITORY is not set}"

version=${RELEASE_TAG#v}
title="$version is out — run /brigade:update"

# A version is three numbers, compared as numbers. A tag with more after them, a release candidate, announces
# itself as every tag did, closes nothing and is never ranked against the others: it is not known where it
# stands among them. Its announcement is closed by the release of its version, or of a later one.
case $version in
  *[!0-9.]*|*.*.*.*|.*|*.|*..*) plain=false ;;
  *.*.*) plain=true ;;
  *) plain=false ;;
esac

# Every issue the bot opened, open and closed, as one JSON array. `--paginate` prints one array a page. The
# listing is read into a variable first: in a pipeline its failure would be lost behind jq's success, and an
# empty listing reads as "no announcement yet".
list() {
  listed=$(gh api --paginate "repos/${GITHUB_REPOSITORY}/issues?state=all&creator=github-actions%5Bbot%5D&per_page=100")
  issues=$(printf '%s' "$listed" | jq -s 'add // [] | map(select(.pull_request == null) | {number, state, title})')
}

# The announcements among them, each with a key that orders it: the three numbers, then 1, or 0 for a version
# with more after its numbers, which is below the plain one. An issue is an announcement only when its whole
# title has the form: one that names a version in passing is not.
# shellcheck disable=SC2016  # $version and the other names are jq's, meant literally
announcements='
  def key: (.v | split(".") | map(tonumber)) + [if .pre == null then 1 else 0 end];
  def announcements: [ .[]
    | . + {key: (.title | capture("^(?<v>[0-9]+\\.[0-9]+\\.[0-9]+)(?<pre>-[0-9A-Za-z.-]+)? is out — run /brigade:update$") | key)} ];
  def thiskey: ($version | split(".") | map(tonumber)) + [1];'

list
existing=$(printf '%s' "$issues" | TITLE="$title" jq -r '[.[] | select(.title == env.TITLE) | "#\(.number)"] | join(" ")')
# The highest plain version announced that is above this one, if there is one: its announcement stands, and
# an older release's would tell the members to update to what they already have behind them.
above=""
if [ "$plain" = true ]; then
  above=$(printf '%s' "$issues" | jq -r --arg version "$version" "$announcements"'
    [announcements[] | select(.key[3] == 1 and .key > thiskey)] | max_by(.key) | if . == null then "" else (.title | split(" ")[0]) end')
fi
if [ -n "$existing" ]; then
  echo "announcement exists ($existing, titled \"$title\"); not opening a second one"
elif [ -n "$above" ]; then
  echo "$version is below $above, which is announced; no announcement is opened for it"
elif [ "$mode" = plan ]; then
  echo "would open: \"$title\""
else
  # No `claude` label: it is an announcement, not a work order.
  # shellcheck disable=SC2016  # the backticks in the single-quoted format are Markdown, meant literally
  gh issue create --repo "$GITHUB_REPOSITORY" --title "$title" --body "$(printf 'Brigade %s is published. To get it, run:\n\n```\n/brigade:update\n```\n\nthen `/reload-plugins`.\n\nRelease notes: https://github.com/%s/releases/tag/%s\n' "$version" "$GITHUB_REPOSITORY" "$RELEASE_TAG")"
  # What is closed below is decided on the issues as they are now, this one among them: a run for another
  # tag may have opened its own since the first listing.
  list
fi

if [ "$plain" = false ]; then
  echo "$version is not a plain version; no earlier announcement is closed"
  exit 0
fi

# The earlier ones: `<number><TAB><title>` for each open announcement below the highest plain version
# announced, this release's included.
earlier=$(printf '%s' "$issues" | jq -r --arg version "$version" "$announcements"'
  announcements as $all
  | ([$all[] | select(.key[3] == 1) | .key] + [thiskey] | max) as $newest
  | $all[] | select(.state == "open" and .key < $newest) | "\(.number)\t\(.title)"')

if [ -z "$earlier" ]; then
  echo "no earlier announcement is open"
  exit 0
fi
printf '%s\n' "$earlier" | while IFS="$(printf '\t')" read -r number was; do
  if [ "$mode" = plan ]; then
    echo "would close: #$number \"$was\""
    continue
  fi
  # gh says "Closed issue" on stderr when it has; this script's own line is the one in the log, and gh's
  # words are printed only when it failed.
  if ! said=$(gh issue close "$number" --repo "$GITHUB_REPOSITORY" --reason completed 2>&1); then
    printf '%s\n' "$said" >&2
    echo "announce-release: #$number could not be closed" >&2
    exit 1
  fi
  echo "closed: #$number \"$was\""
  # One write a second: GitHub asks for a pause between writes.
  sleep 1
done
