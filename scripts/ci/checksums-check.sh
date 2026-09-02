#!/bin/sh
# usage: scripts/ci/checksums-check.sh <fresh-checksums-file>
#        (run from the repository root; `make checksums-check` builds dist-cross/checksums.txt and passes it)
#
# Closes the release loop on every commit (plan 7.7). Reads plugin/bin/VERSION and then:
#
#   pre-release (VERSION = 0.0.0): plugin/bin/checksums.txt must be EMPTY, and rules (b) and (c) are skipped --
#       (b) can never hold against an empty file, and no release exists to back it.
#   (a) VERSION equals plugin/.claude-plugin/plugin.json's "version" (a real version needs a manifest, so a
#       missing plugin.json is a failure once VERSION is not 0.0.0).
#   (b) plugin/bin/checksums.txt is exactly four `<sha256>  <asset>` lines naming
#       brigade_<VERSION>_{darwin,linux}_{amd64,arm64}.
#   (c) the fresh file's four lines equal the committed file's, OR the published release v<VERSION> carries a
#       checksums.txt equal to the committed one (`gh release download`, authenticated with GH_TOKEN because the
#       repository is private). Otherwise: the checksums are stale and nothing backs them.
set -eu

die() { printf 'checksums-check: %s\n' "$1" >&2; exit 1; }

fresh=${1:-}
[ -n "$fresh" ] || die "usage: scripts/ci/checksums-check.sh <fresh-checksums-file>"
[ -f "$fresh" ] || die "no such fresh checksums file: $fresh"

committed=plugin/bin/checksums.txt
version_file=plugin/bin/VERSION
plugin_json=plugin/.claude-plugin/plugin.json

[ -r "$version_file" ] || die "missing $version_file"
[ -f "$committed" ] || die "missing $committed"
version=$(head -n 1 "$version_file")
case $version in
  ''|*[!0-9A-Za-z.+-]*) die "$version_file is not a version token: '$version'" ;;
esac

# ---- pre-release ----------------------------------------------------------------------------------------------
if [ "$version" = 0.0.0 ]; then
  [ ! -s "$committed" ] || die "$version_file is 0.0.0 (pre-release) but $committed is not empty: no release exists to back it"
  printf 'checksums-check: pre-release state (VERSION 0.0.0, empty %s); rules (b) and (c) skipped\n' "$committed"
  exit 0
fi

# ---- (a) the manifest pins the same version --------------------------------------------------------------------
[ -f "$plugin_json" ] || die "(a) VERSION is $version but $plugin_json does not exist: a real version needs a manifest"
manifest=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$plugin_json" | head -n 1)
[ -n "$manifest" ] || die "(a) $plugin_json declares no \"version\""
[ "$manifest" = "$version" ] || die "(a) $plugin_json version '$manifest' != $version_file '$version'"
printf 'checksums-check: (a) %s == %s == %s\n' "$version_file" "$plugin_json" "$version"

# ---- (b) exactly the four assets for this version ---------------------------------------------------------------
lines=$(awk 'END {print NR}' "$committed")
[ "$lines" = 4 ] || die "(b) $committed has $lines line(s), expected exactly 4"
for t in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  asset=brigade_${version}_${t}
  # The asset name carries the version's dots; escape them so the ERE below is literal.
  pattern=$(printf '%s' "$asset" | sed 's/[.]/\\./g')
  n=$(grep -cE "^[0-9a-f]{64}  ${pattern}\$" "$committed" || true)
  [ "$n" = 1 ] || die "(b) $committed has $n well-formed '<sha256>  $asset' line(s), expected exactly 1"
done
printf 'checksums-check: (b) %s names the four v%s assets in <sha256>  <asset> form\n' "$committed" "$version"

# ---- (c) a fresh build or the published release backs the committed file ------------------------------------------
if diff -u "$committed" "$fresh" >/dev/null 2>&1; then
  printf 'checksums-check: (c) a fresh build of this source reproduces %s\n' "$committed"
  exit 0
fi

printf 'checksums-check: (c) the fresh build differs from %s; trying the published release v%s\n' "$committed" "$version" >&2
command -v gh >/dev/null 2>&1 || die "(c) checksums are stale relative to the source and no release backs them (gh is not installed, so the release fallback could not run)"
released=$(mktemp -t checksums-released.XXXXXX) || die "cannot create a temporary file"
# The trap removes exactly the one absolute mktemp path it created.
trap 'rm -f "$released"' EXIT HUP INT TERM
if ! gh release download "v$version" -p checksums.txt -O - > "$released" 2>/dev/null; then
  diff -u "$committed" "$fresh" >&2 || true
  die "(c) checksums are stale relative to the source and no release backs them (no published v$version)"
fi
if ! diff -u "$committed" "$released" >/dev/null 2>&1; then
  diff -u "$committed" "$released" >&2 || true
  die "(c) checksums are stale relative to the source and the published v$version disagrees with $committed"
fi
printf 'checksums-check: (c) the published release v%s backs %s (this commit changed the source without bumping the pin)\n' "$version" "$committed"
