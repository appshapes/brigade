#!/bin/sh
# Tag master's head as the Brigade server release its tree names, and push the tag (work item 89). The
# server-release workflow does the rest: the binaries, their checksums, the image and the GitHub release.
#
# It refuses unless the three places that name the version agree, the tree is clean and is origin/master's
# head, the tag does not exist yet, and ci.yml's run on that head is green. It changes nothing but the tag.
#
# The CI check (work item 399, ADR 0015; scripts/release-ci-check.sh does the looking): until 2026-11-22 this
# script tagged whatever the head's run said, and the `server` job of ci.yml is the only test a server release
# has, since the server-release workflow runs none. A run in progress is waited for; a red or a missing one
# stops the tag. The owner's ruling is stop, with an override: `release_over_ci=<the red run's id>`, or
# `release_over_ci=missing:<the commit>`, tags anyway; it is the owner's to allow, and a session passes it only
# on his word.
#
# DRY_RUN=1 does every check and stops before the tag, saying what it would have tagged. Like the plugin
# release's switch, anything but unset, empty, 0, no or false turns it ON, so a typo fails safe.
set -eu

cd "$(dirname "$0")/.."

fail() {
	echo "server-tag: $1" >&2
	exit 1
}

version=$(cat server/VERSION)
tag="server-v$version"
case ${DRY_RUN:-0} in
'' | 0 | no | false) dry_run=0 ;;
*) dry_run=1 ;;
esac

case "$version" in
'' | 0.0.0) fail "server/VERSION names no release" ;;
esac
grep -q "^version = \"$version\"\$" server/Cargo.toml || fail "server/Cargo.toml does not name version $version"
grep -q "^## \[$version\]" server/CHANGELOG.md || fail "server/CHANGELOG.md has no section for $version"
# Work that lands between releases waits under `## [Unreleased]` (since work item 374), and the release's text is
# cut from the version's section alone: a section left behind is work the release carries and does not say.
if grep -q '^## \[Unreleased\]' server/CHANGELOG.md; then
	fail "server/CHANGELOG.md still has a '## [Unreleased]' section: give it the heading '## [$version]', or move its entries there"
fi

git fetch --quiet --tags origin master
[ -z "$(git status --porcelain)" ] || fail "the working tree has changes; a release is tagged from a clean tree"
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/master)" ] || fail "HEAD is not origin/master's head"
if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
	fail "the tag $tag exists already"
fi

# Last, because it is the one check that waits: the head's run, read by the script the plugin release uses.
head=$(git rev-parse HEAD)
release_over_ci=${release_over_ci:-} sh scripts/release-ci-check.sh "$head" ||
	fail "$tag is NOT tagged: ci.yml is not green on $head. Run it again when the run is green, or with release_over_ci on the owner's word"

if [ "$dry_run" = 1 ]; then
	echo "server-tag: DRY_RUN=1: every check passed; a real run would now tag $head as $tag and push the tag"
	exit 0
fi

# The tag names the commit whose run was read, not HEAD, which may have moved while the check waited.
# The override, when one was given, goes into the tag's message as a line of its own (work item 416, ADR 0028):
# server-release.yml asks the same check again before it publishes, and reads the owner's word there.
tag_message="Brigade server $version"
[ -z "${release_over_ci:-}" ] || tag_message="Brigade server $version

release_over_ci=$release_over_ci"
git tag --annotate "$tag" --message "$tag_message" "$head"
git push origin "$tag"
echo "server-tag: pushed $tag; the server-release workflow publishes it"
