#!/bin/sh
# usage: scripts/ci/release-verify.sh <goreleaser-checksums> <committed-checksums>
#        (run by .github/workflows/release.yml as: scripts/ci/release-verify.sh dist/checksums.txt plugin/bin/checksums.txt)
#
# The release job's last gate (plan 7.7): the binaries goreleaser just built must be byte-identical to the ones
# the committed plugin/bin/checksums.txt pins, or the draft release is discarded. Only the HASH COLUMNS are
# compared, sorted: goreleaser and `make cross` agree on the hashes but need not agree on line order, and the
# asset-name column is checked by checksums-check.sh (rule b) against the version instead.
set -eu

die() { printf 'release-verify: %s\n' "$1" >&2; exit 1; }

a=${1:-}
b=${2:-}
[ -n "$a" ] && [ -n "$b" ] || die "usage: scripts/ci/release-verify.sh <goreleaser-checksums> <committed-checksums>"
[ -f "$a" ] || die "no such file: $a"
[ -f "$b" ] || die "no such file: $b"
[ -s "$a" ] || die "$a is empty: nothing to verify"
[ -s "$b" ] || die "$b is empty: nothing to verify"

cols_a=$(awk '{print $1}' "$a" | LC_ALL=C sort)
cols_b=$(awk '{print $1}' "$b" | LC_ALL=C sort)

if [ "$cols_a" != "$cols_b" ]; then
  printf 'release-verify: %s\n' "$a" >&2
  printf '%s\n' "$cols_a" >&2
  printf 'release-verify: %s\n' "$b" >&2
  printf '%s\n' "$cols_b" >&2
  die "the built binaries do not match the committed checksums: refusing to publish"
fi

n=$(printf '%s\n' "$cols_a" | grep -c . || true)
printf 'release-verify: %s hash(es) in %s match %s\n' "$n" "$a" "$b"
