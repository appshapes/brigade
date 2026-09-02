#!/bin/sh
# usage: scripts/ci/no-secrets.sh            (run from the repository root; no arguments)
#
# Fails, naming the file and line, when a credential shape reaches something we ship or track (plan 7.7,
# success criterion: the Supabase secret/service-role key must never appear in the adapter, the plugin, the
# repository or the CI variables that ship).
#
# Patterns, everywhere in scope -- tracked text, plugin/, AND every built binary:
#   * a JWT-shaped triple  eyJ<8+>.eyJ<8+>.<8+>     (a Supabase legacy anon/service key looks exactly like this)
#   * a Supabase secret key  sb_secret_<8+>
# Pattern, in plugin/ text files ONLY -- never in a binary:
#   * the word service_role. The role NAME is not a key: the migrations legitimately REVOKE from that role, the
#     Makefile names SERVICE_ROLE_KEY, and the Supabase adapter embeds it as an error-mapping string and as the
#     SQL role name, so a shipped binary carrying it is clean. A real service-role KEY is JWT-shaped and is
#     therefore already caught, in binaries included, by the shape scan above. The rule survives for plugin/
#     because nothing hand-written and shipped to a user's machine has any business naming that role.
#
# Scope: every file under plugin/, every tracked file except the fixture-bearing trees listed below, plus
# dist-cross/ and bin/brigade* when a build left them behind (scanned with grep -a as binaries). The scanned
# file count is printed, so a run that read nothing is visible instead of vacuously green.
set -eu

die() { printf 'no-secrets: %s\n' "$1" >&2; exit 1; }

[ -d plugin ] || die "run me from the repository root: no plugin/ directory here"

jwt='eyJ[A-Za-z0-9_-]\{8,\}\.eyJ[A-Za-z0-9_-]\{8,\}\.[A-Za-z0-9_-]\{8,\}'
sbsecret='sb_secret_[A-Za-z0-9_-]\{8,\}'

tmp=$(mktemp -t no-secrets.XXXXXX) || die "cannot create a temporary file"
plug=$(mktemp -t no-secrets-plugin.XXXXXX) || die "cannot create a temporary file"
# The trap removes exactly the two absolute mktemp paths it created, nothing else.
trap 'rm -f "$tmp" "$plug"' EXIT HUP INT TERM

# ---- the credential-shape scope: tracked text, plugin/, and any built binary ---------------------------------------
# Deliberate exclusions: the redaction tests and the research/experiment evidence carry JWT-shaped fixtures on
# purpose, and the injection corpus exists to hold hostile strings.
{
  find plugin -type f -print
  git ls-files
  if [ -d dist-cross ]; then find dist-cross -type f -print; fi
  for b in bin/brigade*; do
    [ -f "$b" ] && printf '%s\n' "$b"
  done
  true
} | LC_ALL=C sort -u | while IFS= read -r f; do
  case $f in
    *_test.go|*/testdata/*|testdata/*) continue ;;
    docs/research/*|docs/experiments/*|scripts/injection-corpus/*|supabase/migrations/*) continue ;;
  esac
  [ -f "$f" ] || continue
  printf '%s\n' "$f"
done > "$tmp"

count=$(grep -c . "$tmp" || true)
[ "$count" -gt 0 ] || die "the scan found no files at all: refusing to pass vacuously"

hits=0
while IFS= read -r f; do
  if grep -aHn -e "$jwt" -e "$sbsecret" "$f" >&2; then hits=$((hits + 1)); fi
done < "$tmp"
[ "$hits" = 0 ] || die "$hits file(s) above contain a JWT-shaped string or an sb_secret_ key"

# ---- service_role: plugin/ text files only, never the binaries ------------------------------------------------------
find plugin -type f -print | LC_ALL=C sort -u > "$plug"

plugcount=$(grep -c . "$plug" || true)
plughits=0
if [ "$plugcount" -gt 0 ]; then
  while IFS= read -r f; do
    if grep -aHn -e 'service_role' "$f" >&2; then plughits=$((plughits + 1)); fi
  done < "$plug"
fi
[ "$plughits" = 0 ] || die "$plughits file(s) above under plugin/ name service_role"

printf 'no-secrets: scanned %s tracked/plugin/built file(s) for JWT and sb_secret_ shapes, and %s plugin/ file(s) additionally for service_role: clean\n' "$count" "$plugcount"
