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
#   (b) plugin/bin/checksums.txt is `<sha256>  <asset>` lines naming brigade_<VERSION>_{darwin,linux}_{amd64,arm64},
#       one each, and the two Windows assets brigade_<VERSION>_windows_{amd64,arm64}.exe (card 133) either both
#       present or both absent -- absent only while the committed file is a release from before Windows
#       (0.40.0 and earlier) -- and nothing else.
#   (c) the fresh file's lines equal the committed file's, OR the published release v<VERSION> carries a
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
muse_json=muse/brigade/.muse-plugin/plugin.json
if [ -f "$muse_json" ]; then
  # The Muse Code package (card 88) carries the same pin and the same three assets.
  muse_version=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$muse_json" | head -n 1)
  [ "$muse_version" = "$version" ] || die "(a) $muse_json version '$muse_version' != $version_file '$version'"
  for asset in brigade VERSION checksums.txt; do
    cmp -s "plugin/bin/$asset" "muse/brigade/bin/$asset" || die "(a) muse/brigade/bin/$asset differs from plugin/bin/$asset"
  done
fi
codex_json=codex/brigade/.codex-plugin/plugin.json
if [ -f "$codex_json" ]; then
  # The Codex CLI package (card 107): the same pin, the same three assets.
  codex_version=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$codex_json" | head -n 1)
  [ "$codex_version" = "$version" ] || die "(a) $codex_json version '$codex_version' != $version_file '$version'"
  for asset in brigade VERSION checksums.txt; do
    cmp -s "plugin/bin/$asset" "codex/brigade/bin/$asset" || die "(a) codex/brigade/bin/$asset differs from plugin/bin/$asset"
  done
fi
grok_json=grok/brigade/.grok-plugin/plugin.json
if [ -f "$grok_json" ]; then
  # The Grok Build package (card 120): the same pin, the same three assets.
  grok_version=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$grok_json" | head -n 1)
  [ "$grok_version" = "$version" ] || die "(a) $grok_json version '$grok_version' != $version_file '$version'"
  for asset in brigade VERSION checksums.txt; do
    cmp -s "plugin/bin/$asset" "grok/brigade/bin/$asset" || die "(a) grok/brigade/bin/$asset differs from plugin/bin/$asset"
  done
fi
printf 'checksums-check: (a) %s == %s == %s\n' "$version_file" "$plugin_json" "$version"

# ---- (b) exactly the assets for this version: the four D33 ones, and the two Windows ones together or not at all ----
count_asset() {
  # The asset name carries the version's dots; escape them so the ERE is literal.
  pattern=$(printf '%s' "$1" | sed 's/[.]/\\./g')
  grep -cE "^[0-9a-f]{64}  ${pattern}\$" "$committed" || true
}
windows=0
for t in windows_amd64 windows_arm64; do
  n=$(count_asset "brigade_${version}_${t}.exe")
  case $n in
    0) ;;
    1) windows=$((windows + 1)) ;;
    *) die "(b) $committed has $n well-formed '<sha256>  brigade_${version}_${t}.exe' line(s), expected at most 1" ;;
  esac
done
[ "$windows" = 0 ] || [ "$windows" = 2 ] || die "(b) $committed names only one of the two Windows assets: a release carries both or neither"
expected_lines=$((4 + windows))
lines=$(awk 'END {print NR}' "$committed")
[ "$lines" = "$expected_lines" ] || die "(b) $committed has $lines line(s), expected exactly $expected_lines"
for t in darwin_amd64 darwin_arm64 linux_amd64 linux_arm64; do
  asset=brigade_${version}_${t}
  n=$(count_asset "$asset")
  [ "$n" = 1 ] || die "(b) $committed has $n well-formed '<sha256>  $asset' line(s), expected exactly 1"
done
printf 'checksums-check: (b) %s names the %s v%s assets in <sha256>  <asset> form\n' "$committed" "$expected_lines" "$version"

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
