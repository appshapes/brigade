#!/bin/sh
# usage: scripts/ci/release-notes-lint.sh <notes-file> <tag>
#
# The deterministic half of the release-notes gate (release-notes.yml): the drafted notes may not name anything
# that does not exist. A model composed them, and the v0.5.0 notes named a `/brigade:sessions` skill that was never
# there — a grounding slip that more thinking makes rarer and that this script makes impossible. It exits 1 on the
# first class of failure, printing one `FAIL:` line per finding so the writer's fix cycle can read them back, and
# `ok:` lines for what it checked. It reads nothing but the notes, the plugin tree and three environment variables:
#
#   BRIGADE_NOTES_COMMANDS   the CLI's top-level commands, space-separated (release-notes.yml derives them from
#                            `brigade --help` of a fresh build; the tests set them by hand)
#   BRIGADE_NOTES_ASSETS     the release's asset names, space-separated (from `gh release view --json assets`)
#   BRIGADE_NOTES_SKILLS     optional: the plugin's skills and commands, space-separated; when unset they are read
#                            from plugin/skills/*/ and plugin/commands/*.md of the checkout
#
# Checks, in order: the file is non-empty and carries no placeholder; it names the released version and never
# "Unreleased"; every `/brigade:<name>` is a skill or command that exists; every backticked `brigade <verb>` (and
# every fenced line that starts with one) names a command the CLI has; every asset-shaped token it names shipped,
# and every shipped asset is named at least once; nothing secret-shaped is in it.
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

# 5. Assets: everything named shipped, and everything that shipped is named.
assets=${BRIGADE_NOTES_ASSETS:-}
[ -n "$assets" ] || fail "BRIGADE_NOTES_ASSETS is unset: the release's asset list is required"
tokens=$(grep -o -E 'brigade_[0-9][A-Za-z0-9.+-]*_[a-z0-9]+_[a-z0-9]+|checksums\.txt' "$notes" | sort -u)
for token in $tokens; do
  if has_word "$token" "$assets"; then ok "asset $token shipped"; else fail "asset $token is named but did not ship (have: $assets)"; fi
done
for asset in $assets; do
  if grep -q -F "$asset" "$notes"; then :; else fail "shipped asset $asset is not named in the notes"; fi
done

# 6. Nothing secret-shaped.
if grep -n -E 'sbp_[A-Za-z0-9]{20,}|sb_secret_|eyJ[A-Za-z0-9_-]{20,}\.eyJ' "$notes"; then fail "the notes carry something secret-shaped (above)"; else ok "nothing secret-shaped"; fi

if [ "$fails" -gt 0 ]; then echo "release-notes-lint: $fails finding(s)"; exit 1; fi
echo "release-notes-lint: clean"
