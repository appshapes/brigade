#!/bin/sh
# Tag master's head as the Brigade server release its tree names, and push the tag (work item 89). The
# server-release workflow does the rest: the binaries, their checksums, the image and the GitHub release.
#
# It refuses unless the three places that name the version agree, the tree is clean and is origin/master's
# head, and the tag does not exist yet. It changes nothing but the tag.
set -eu

cd "$(dirname "$0")/.."

fail() {
	echo "server-tag: $1" >&2
	exit 1
}

version=$(cat server/VERSION)
tag="server-v$version"

case "$version" in
'' | 0.0.0) fail "server/VERSION names no release" ;;
esac
grep -q "^version = \"$version\"\$" server/Cargo.toml || fail "server/Cargo.toml does not name version $version"
grep -q "^## \[$version\]" server/CHANGELOG.md || fail "server/CHANGELOG.md has no section for $version"

git fetch --quiet --tags origin master
[ -z "$(git status --porcelain)" ] || fail "the working tree has changes; a release is tagged from a clean tree"
[ "$(git rev-parse HEAD)" = "$(git rev-parse origin/master)" ] || fail "HEAD is not origin/master's head"
if git rev-parse --quiet --verify "refs/tags/$tag" >/dev/null; then
	fail "the tag $tag exists already"
fi

git tag --annotate "$tag" --message "Brigade server $version"
git push origin "$tag"
echo "server-tag: pushed $tag; the server-release workflow publishes it"
