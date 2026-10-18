#!/bin/sh
# usage: scripts/muse-digest.sh print | write | check        (run from the repository root)
#
# The package digest of the Muse Code package, muse/brigade/ (card 115). Muse installs a plugin from a marketplace
# only when the catalog entry's `integrity.digest` equals the SHA-256 Muse itself computes over the package tree,
# and the serialisation behind that SHA-256 is not documented (card 114 researches it), so the one way to obtain it
# is to have Muse install the package once and report `package_sha256`. This script does that in a throwaway XDG
# root: nothing under the real Muse directories is read, written or copied.
#
#   print   compute the digest and print it (`sha256:<64 hex>`)
#   write   compute it and rewrite marketplace.json's `integrity.digest`, and its `version` from muse/brigade/bin/VERSION
#   check   compute it and compare with marketplace.json's; exit 1 on a difference
#
# Without `muse` on PATH, `check` prints a `skipped:` line and exits 0 (CI's runners have no Muse; the catalog's
# version and shape are checked without it by scripts/ci/muse-plugin-check.sh), while `print` and `write` fail, and
# so does `check` under MUSE_DIGEST_REQUIRE=1, which scripts/release-prep.sh sets: a release never ships a catalog
# it did not verify (owner ruling 2026-10-17).
#
# POSIX sh, `shellcheck -s sh` clean under 0.9.0 and 0.11 (plugin-check.sh check 7 runs it over scripts/*.sh).
set -eu

die() { printf 'muse-digest: %s\n' "$1" >&2; exit 1; }
say() { printf 'muse-digest: %s\n' "$1"; }

package=muse/brigade
catalog=marketplace.json
version_file=$package/bin/VERSION
mode=${1:-}
case $mode in
  print|write|check) ;;
  *) die "usage: scripts/muse-digest.sh print | write | check" ;;
esac
[ -f "$package/.muse-plugin/plugin.json" ] || die "run this from the repository root: no $package/.muse-plugin/plugin.json here"
[ -f "$version_file" ] || die "missing $version_file"
read -r version < "$version_file"
case $version in ''|*[!0-9A-Za-z.+-]*) die "invalid version in $version_file: '$version'" ;; esac

if ! command -v muse >/dev/null 2>&1; then
  if [ "$mode" = check ] && [ "${MUSE_DIGEST_REQUIRE:-0}" = 0 ]; then
    say "skipped: muse is not on PATH, so $catalog's digest is not verified here (a machine with Muse Code verifies it; card 114 is the way without one)"
    exit 0
  fi
  die "muse is not on PATH: $catalog's digest is what Muse computes over $package/, so Muse Code must be installed here (card 115, owner ruling 2026-10-17)"
fi

# A throwaway root for Muse's data, configuration, state and cache: the install lands there and the real Muse
# directories are never touched. The session environment is stripped by prefix (MUSE*, CLAUDE*, AI_AGENT, keeping
# CLAUDE_CONFIG_DIR), as every script that runs a harness outside the current session does.
tmp=$(mktemp -d "${TMPDIR:-/tmp}/brigade-muse-digest.XXXXXX") || die "mktemp failed"
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
for name in $(env | sed -nE 's/^(MUSE[A-Za-z0-9_]*|CLAUDE[A-Za-z0-9_]*|AI_AGENT)=.*/\1/p'); do
  [ "$name" = CLAUDE_CONFIG_DIR ] || unset "$name"
done
here=$(pwd -P)
if ! out=$(XDG_DATA_HOME="$tmp/data" XDG_CONFIG_HOME="$tmp/config" XDG_STATE_HOME="$tmp/state" XDG_CACHE_HOME="$tmp/cache" \
    muse plugins install "$here/$package" --scope user --json 2>&1); then
  printf '%s\n' "$out" >&2
  die "muse plugins install $package failed"
fi
digest=$(printf '%s\n' "$out" | sed -nE 's/^[[:space:]]*"package_sha256":[[:space:]]*"(sha256:[0-9a-f]{64})".*/\1/p' | head -n 1)
if [ -z "$digest" ]; then
  printf '%s\n' "$out" >&2
  die "muse plugins install printed no package_sha256"
fi

case $mode in
  print)
    printf '%s\n' "$digest"
    ;;
  write)
    [ -f "$catalog" ] || die "missing $catalog"
    sed -i.bak -E "s/^([[:space:]]*\"digest\":[[:space:]]*)\"[^\"]*\"/\1\"$digest\"/" "$catalog"
    sed -i.bak -E "s/^([[:space:]]*\"version\":[[:space:]]*)\"[^\"]*\"/\1\"$version\"/" "$catalog"
    rm -f "$catalog.bak"
    got=$(sed -nE 's/^[[:space:]]*"digest":[[:space:]]*"([^"]*)".*/\1/p' "$catalog" | head -n 1)
    [ "$got" = "$digest" ] || die "the write did not take in $catalog (its digest reads as '$got'): \"digest\" must start its own line"
    got=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]*)".*/\1/p' "$catalog" | head -n 1)
    [ "$got" = "$version" ] || die "the write did not take in $catalog (its version reads as '$got'): \"version\" must start its own line"
    say "wrote $catalog: version $version, digest $digest"
    ;;
  check)
    [ -f "$catalog" ] || die "missing $catalog"
    recorded=$(sed -nE 's/^[[:space:]]*"digest":[[:space:]]*"([^"]*)".*/\1/p' "$catalog" | head -n 1)
    [ -n "$recorded" ] || die "$catalog declares no \"digest\""
    [ "$recorded" = "$digest" ] || die "$catalog's digest $recorded differs from what Muse computes over $package/ ($digest): run 'make muse-digest' and commit the catalog"
    say "ok: $catalog's digest equals what Muse computes over $package/ ($digest)"
    ;;
esac
