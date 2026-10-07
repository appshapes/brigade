#!/bin/sh
# usage: scripts/ci/release-notes-lint.sh <notes-file> <tag>
#
# The deterministic half of the release-notes gate (release-notes.yml): the drafted notes may not name anything
# that does not exist. A model composed them, and the v0.5.0 notes named a `/brigade:sessions` skill that did not
# exist at the time — a grounding slip that more thinking makes rarer and that this script makes impossible. (That
# skill was written later and is real now; the slip was naming it before it was.) It exits 1 on the
# first class of failure, printing one `FAIL:` line per finding so the writer's fix cycle can read them back, and
# `ok:` lines for what it checked. It reads nothing but the notes, the plugin tree and four environment variables:
#
#   BRIGADE_NOTES_COMMANDS   the CLI's top-level commands, space-separated (release-notes.yml derives them from
#                            `brigade --help` of a fresh build; the tests set them by hand)
#   BRIGADE_NOTES_ASSETS     the release's asset names, space-separated (from `gh release view --json assets`)
#   BRIGADE_NOTES_SKILLS     optional: the plugin's skills and commands, space-separated; when unset they are read
#                            from plugin/skills/*/ and plugin/commands/*.md of the checkout
#   BRIGADE_NOTES_KIND       optional: `email` when the file is the release-notes email of
#                            send-release-notes.yml (card 43) rather than one release's notes; <tag> is then the
#                            newest release the email covers
#   BRIGADE_EMAIL_RENDERER   the email kind only: the path of the email's renderer (`go build
#                            ./cmd/brigade-release-email`), which reads the draft in -check mode
#
# Checks, in order: the file is non-empty and carries no placeholder; it names the released version and never
# "Unreleased"; every `/brigade:<name>` is a skill or command that exists; every backticked `brigade <verb>` (and
# every fenced line that starts with one) names a command the CLI has; every asset-shaped token it names shipped,
# and every shipped asset is named at least once; nothing secret-shaped is in it.
#
# An email differs in three ways. It lists no assets, so the asset check is skipped and BRIGADE_NOTES_ASSETS is
# not read. It leaves the repository for readers who never saw its tracker or its plans, so it may carry no
# address of anyone and no card or plan-row reference -- the changelog it is composed from is full of both. And
# it is laid out as HTML by a renderer that knows the brief's shape and nothing else (card 73), so the draft must
# render: a table, a quote, raw HTML, a nested list, a relative link or an image the checkout does not have is
# a finding here, with its line, rather than a surprise in the inbox.
set -u

notes=${1:-}
tag=${2:-}
if [ -z "$notes" ] || [ -z "$tag" ]; then echo "usage: scripts/ci/release-notes-lint.sh <notes-file> <tag>" >&2; exit 2; fi
[ -f "$notes" ] || { echo "FAIL: $notes does not exist" >&2; exit 1; }

fails=0
fail() { echo "FAIL: $*"; fails=$((fails + 1)); }
ok() { echo "ok: $*"; }
has_word() { # has_word <word> <space-separated list>
  case " $2 " in *" $1 "*) return 0 ;; esac
  return 1
}

version=${tag#v}
kind=${BRIGADE_NOTES_KIND:-notes}
case $kind in
  notes|email) ;;
  *) echo "FAIL: BRIGADE_NOTES_KIND is neither unset nor email: $kind" >&2; exit 1 ;;
esac

# 1. Not empty, no placeholder, no "Unreleased".
if ! grep -q '[^[:space:]]' "$notes"; then fail "the notes are empty"; else ok "the notes are not empty"; fi
if grep -n -E 'TODO|TBD|FIXME|XXX' "$notes"; then fail "the notes carry a placeholder (above)"; else ok "no placeholder"; fi
if grep -n -i 'unreleased' "$notes"; then fail "the notes say Unreleased (above)"; else ok "no Unreleased"; fi

# 2. The released version is named, as a version.
if grep -q -F "$version" "$notes"; then ok "the notes name $version"; else fail "the notes never name version $version"; fi

# 3. Skills and slash commands: every /brigade:<name> exists.
skills=${BRIGADE_NOTES_SKILLS:-}
if [ -z "$skills" ]; then
  for d in plugin/skills/*/; do [ -d "$d" ] && skills="$skills $(basename "$d")"; done
  for f in plugin/commands/*.md; do [ -f "$f" ] && skills="$skills $(basename "$f" .md)"; done
fi
names=$(grep -o -E '/brigade:[A-Za-z0-9_-]+' "$notes" | sed 's|^/brigade:||' | sort -u)
for name in $names; do
  if has_word "$name" "$skills"; then ok "/brigade:$name exists"; else fail "/brigade:$name is not a skill or command of the plugin (have:$skills)"; fi
done

# 4. CLI commands: every `brigade <verb>` names a command the binary has.
commands=${BRIGADE_NOTES_COMMANDS:-}
[ -n "$commands" ] || fail "BRIGADE_NOTES_COMMANDS is unset: the CLI command list is required"
verbs=$( { grep -o -E '`brigade [a-z][a-z-]*' "$notes" | sed 's/^`brigade //'; grep -E '^[[:space:]]*brigade [a-z][a-z-]*' "$notes" | sed -E 's/^[[:space:]]*brigade ([a-z][a-z-]*).*/\1/'; } | sort -u)
for verb in $verbs; do
  if has_word "$verb" "$commands"; then ok "brigade $verb is a command"; else fail "brigade $verb is not a command of the CLI (have: $commands)"; fi
done

# 5. Assets: everything named shipped, and everything that shipped is named. Not for an email.
if [ "$kind" = notes ]; then
  assets=${BRIGADE_NOTES_ASSETS:-}
  [ -n "$assets" ] || fail "BRIGADE_NOTES_ASSETS is unset: the release's asset list is required"
  tokens=$(grep -o -E 'brigade_[0-9][A-Za-z0-9.+-]*_[a-z0-9]+_[a-z0-9]+|checksums\.txt' "$notes" | sort -u)
  for token in $tokens; do
    if has_word "$token" "$assets"; then ok "asset $token shipped"; else fail "asset $token is named but did not ship (have: $assets)"; fi
  done
  for asset in $assets; do
    if grep -q -F "$asset" "$notes"; then :; else fail "shipped asset $asset is not named in the notes"; fi
  done
fi

# 6. Nothing secret-shaped.
if grep -n -E 'sbp_[A-Za-z0-9]{20,}|sb_secret_|eyJ[A-Za-z0-9_-]{20,}\.eyJ' "$notes"; then fail "the notes carry something secret-shaped (above)"; else ok "nothing secret-shaped"; fi

# 7. An email only: nobody's address, and nothing that points into the tracker or the plans.
if [ "$kind" = email ]; then
  if grep -n -E '[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}' "$notes"; then fail "the email carries an address (above)"; else ok "no address"; fi
  if grep -n -i -E '(^|[^A-Za-z])(card|ticket)s? #?[0-9]+|(^|[^A-Za-z0-9])P[0-9]+-[0-9]+' "$notes"; then fail "the email names a card or a plan row (above)"; else ok "no card or plan row"; fi
fi

# 8. An email only: the draft renders. The renderer reads the draft from the checkout's root (an image's
#    file must exist there) and prints every finding on its own line.
if [ "$kind" = email ]; then
  renderer=${BRIGADE_EMAIL_RENDERER:-}
  if [ -z "$renderer" ]; then
    fail "BRIGADE_EMAIL_RENDERER is unset: the email's renderer is required"
  elif findings=$("$renderer" -check -in "$notes" -root . 2>&1); then
    ok "the draft renders as the email"
  else
    printf '%s\n' "$findings"
    fail "the draft does not render as the email (above)"
  fi
fi

if [ "$fails" -gt 0 ]; then echo "release-notes-lint: $fails finding(s)"; exit 1; fi
echo "release-notes-lint: clean"
