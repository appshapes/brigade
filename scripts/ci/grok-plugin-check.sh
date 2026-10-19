#!/bin/sh
# Default: strict release assets. --source: the unreleased package's source
# scaffolding only, with an explicit non-readiness line. No host is launched.
set -eu
case ${1:-} in
  '') source_only=0 ;;
  --source) source_only=1 ;;
  *) printf 'usage: grok-plugin-check.sh [--source]\n' >&2; exit 2 ;;
esac
[ "$#" -le 1 ] || { printf 'too many arguments\n' >&2; exit 2; }
go test ./scripts/ci -run '^TestGrokPackageSource$' -count=1
if [ "$source_only" = 1 ]; then
  printf 'grok-plugin-check: source scaffolding checked; NOT a release or live participation check\n'
  exit 0
fi
for asset in brigade VERSION checksums.txt; do
  [ -f "grok/brigade/bin/$asset" ] || {
    printf 'grok-plugin-check: missing release-produced grok/brigade/bin/%s; run the normal release sequence\n' "$asset" >&2
    exit 1
  }
  cmp "plugin/bin/$asset" "grok/brigade/bin/$asset"
done
version=$(head -n 1 grok/brigade/bin/VERSION)
declared=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' grok/brigade/.grok-plugin/plugin.json)
[ "$declared" = "$version" ] || { printf 'grok-plugin-check: manifest version differs from release pin\n' >&2; exit 1; }
[ -x grok/brigade/bin/brigade ] || { printf 'grok-plugin-check: bootstrap is not executable\n' >&2; exit 1; }
printf 'grok-plugin-check: release assets equal canonical pins; live contract gates remain independent\n'
