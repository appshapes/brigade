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
#   * a Brigade join secret  brg1.<ref>.<8+>        -- in TEXT files only, never in a binary: the conformance
#     suite's frozen brg1.x.y fixtures compile into the binaries, where the string table's concatenated
#     neighbours always extend the tail past the pattern's minimum, so brg1-in-a-binary is noise by
#     construction. A real join secret is runtime data; the text scan catches the accident at the source
#     before it could ever be compiled in. The bare brg1. prefix alone is legitimate text everywhere (P7-1).
# The word service_role is NOT a pattern (retired 2026-09-08, Rjae: open by default): the role name is not a key,
# and a real service-role key is JWT-shaped and caught by the shape scan above wherever it appears.
#
# Scope: every file under plugin/, every tracked file except the fixture-bearing trees listed below, plus
# dist-cross/ and bin/brigade* when a build left them behind (scanned with grep -a as binaries). The scanned
# file count is printed, so a run that read nothing is visible instead of vacuously green.
set -eu

die() { printf 'no-secrets: %s\n' "$1" >&2; exit 1; }

[ -d plugin ] || die "run me from the repository root: no plugin/ directory here"

jwt='eyJ[A-Za-z0-9_-]\{8,\}\.eyJ[A-Za-z0-9_-]\{8,\}\.[A-Za-z0-9_-]\{8,\}'
sbsecret='sb_secret_[A-Za-z0-9_-]\{8,\}'
# The ref class includes '.' because ParseJoinSecret deliberately allows dotted team refs (D2); the
# tail stays dot-free so prose like 'brg1. and more words' cannot match. A JSON-escaped secret is
# invisible to any text grep by construction -- the teamfile parser's post-decode check owns that case.
brg1='brg1\.[A-Za-z0-9._-]\{1,\}\.[A-Za-z0-9_-]\{8,\}'

tmp=$(mktemp -t no-secrets.XXXXXX) || die "cannot create a temporary file"
# The trap removes exactly the absolute mktemp path it created, nothing else.
trap 'rm -f "$tmp"' EXIT HUP INT TERM

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
    scripts/experiments/*) continue ;;  # historical-record drivers, same rationale as docs/experiments (P7-1)
    docs/protocol-v1.md) continue ;;    # frozen; its join_secret examples are brg1-shaped on purpose (P7-1)
  esac
  [ -f "$f" ] || continue
  printf '%s\n' "$f"
done > "$tmp"

count=$(grep -c . "$tmp" || true)
[ "$count" -gt 0 ] || die "the scan found no files at all: refusing to pass vacuously"

hits=0
while IFS= read -r f; do
  case $f in
    dist-cross/*|bin/brigade*)
      if grep -aHn -e "$jwt" -e "$sbsecret" "$f" >&2; then hits=$((hits + 1)); fi ;;
    *)
      if grep -aHn -e "$jwt" -e "$sbsecret" -e "$brg1" "$f" >&2; then hits=$((hits + 1)); fi ;;
  esac
done < "$tmp"
[ "$hits" = 0 ] || die "$hits file(s) above contain a JWT-shaped string, an sb_secret_ key or a brg1. join secret"

printf 'no-secrets: scanned %s tracked/plugin/built file(s) for JWT, sb_secret_ and brg1. shapes: clean\n' "$count"
