#!/bin/sh
# usage: scripts/ci/muse-plugin-check.sh          (run from the repository root; no arguments)
#
# Static checks of the shipped Muse Code package, muse/brigade/ (card 88), the sibling of scripts/ci/plugin-check.sh
# for plugin/. Exits non-zero on the FIRST failure. One `ok: …` line per check.
#
#  1  muse/brigade/ holds only allow-listed paths, and git tracks every one of them (Muse hashes every file of a
#     package, so a file no clone holds would put a digest in the catalog that no clone reproduces)
#  2  the three pinned assets under bin/ are byte-equal to plugin/bin/'s (one canonical pin, two packages)
#  3  the manifest's "version" equals bin/VERSION
#  4  the manifest is native (schemaVersion 1, a `capabilities` member, no `args`, no `userConfig`)
#  5  no symlink anywhere under muse/ (Muse refuses the package)
#  6  every hook command is one argv element naming a wrapper that exists, is executable and is tracked 100755
#  7  every wrapper execs `bin/brigade hook <subcommand>` with a subcommand the binary implements, and no two hooks
#     share a source (Muse refuses that)
#  8  sh -n / bash -n / zsh -n and shellcheck -s sh on the wrappers
#  9  every skill the manifest names exists, and no skill declares disable-model-invocation (card 28)
# 10  marketplace.json, the Muse catalog at the root, names brigade at muse/brigade, pins bin/VERSION, carries a
#     sha256 digest and the available status (card 115)
#
# The VALUE of the catalog's digest is not checked here: it is the package digest Muse computes, which
# internal/musedigest reproduces (card 114), and `make muse-plugin-check` runs `brigade-muse-digest check` right after
# this script, as `make test` does through scripts/ci/muse_digest_test.go. No machine needs Muse Code for it.
#
# The JSON checks are textual on purpose, as plugin-check.sh's are: the manifest is small and hand-written, and
# `muse plugins validate` (run by a developer with Muse installed) covers the schema half.
set -eu

die() { printf 'muse-plugin-check: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }

root=muse/brigade
manifest=$root/.muse-plugin/plugin.json
[ -d "$root" ] || die "run me from the repository root: no $root directory here"
[ -f "$manifest" ] || die "missing $manifest"

# ---- 1. the file allowlist ----------------------------------------------------------------------------------------
find "$root" -type f -print | LC_ALL=C sort | while IFS= read -r f; do
  rel=${f#"$root"/}
  case $rel in
    bin/brigade|bin/VERSION|bin/checksums.txt|.muse-plugin/plugin.json|README.md) ;;
    hooks/session-start.sh|hooks/prompt.sh|hooks/session-end.sh) ;;
    skills/*/*) ;;
    *) die "not allow-listed under $root/: $f" ;;
  esac
done
# Muse hashes every file of the package it is given, tracked or not, and so does `make muse-digest`: a file git does
# not track (an ignored .DS_Store under skills/ passes the allowlist above) would enter the catalog's digest here
# and exist in no clone, where Muse then refuses the install. `git ls-files --others` lists ignored files too.
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository: cannot tell which files under $root/ are tracked"
stray=$(git ls-files --others -- "$root") || die "git ls-files failed"
[ -z "$stray" ] || die "git does not track these under $root/, and Muse would hash them into the package digest: $(printf '%s' "$stray" | tr '\n' ' ')-- delete them, or add them to git and run 'make muse-digest'"
ok "$root/ contains only allow-listed paths, each tracked by git"

# ---- 2. the pinned assets are the Claude package's, byte for byte ------------------------------------------------
for asset in brigade VERSION checksums.txt; do
  [ -f "plugin/bin/$asset" ] || die "missing plugin/bin/$asset"
  [ -f "$root/bin/$asset" ] || die "missing $root/bin/$asset"
  cmp -s "plugin/bin/$asset" "$root/bin/$asset" || die "$root/bin/$asset differs from plugin/bin/$asset: one canonical pin, two packages (release-prep copies it)"
done
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository: cannot check the recorded file modes"
modes=$(git ls-files -s "$root") || die "git ls-files failed"
[ -n "$modes" ] || die "git tracks no file under $root/"
printf '%s\n' "$modes" | grep -q "^100755 .*	$root/bin/brigade\$" || die "git does not track $root/bin/brigade as 100755"
ok "$root/bin/{brigade,VERSION,checksums.txt} equal plugin/bin/'s and the bootstrap is 100755 in git"

# ---- 3. the version pin ------------------------------------------------------------------------------------------
version=$(head -n 1 "$root/bin/VERSION")
declared=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$manifest" | head -n 1)
[ -n "$declared" ] || die "$manifest declares no \"version\""
[ "$declared" = "$version" ] || die "$manifest version '$declared' != $root/bin/VERSION '$version'"
ok "$root/bin/VERSION ($version) matches $manifest"

# ---- 4. a native manifest ----------------------------------------------------------------------------------------
grep -qE '^[[:space:]]*"schemaVersion":[[:space:]]*1[[:space:]]*,?[[:space:]]*$' "$manifest" || die "$manifest is not a native Muse manifest (no \"schemaVersion\": 1)"
grep -qE '"capabilities"[[:space:]]*:' "$manifest" || die "$manifest declares no \"capabilities\""
grep -qE '"manifestDir"[[:space:]]*:[[:space:]]*"\.muse-plugin"' "$manifest" || die "$manifest's compat.manifestDir is not \".muse-plugin\""
if grep -qE '"(args|userConfig|mcpServers|channels)"[[:space:]]*:' "$manifest"; then
  die "$manifest carries a member Muse does not run (args, userConfig, mcpServers or channels)"
fi
ok "$manifest is a native manifest with capabilities and none of args, userConfig, mcpServers, channels"

# ---- 5. no symlink -------------------------------------------------------------------------------------------------
links=$(find muse -type l -print)
[ -z "$links" ] || die "symlink(s) under muse/, which Muse refuses to install: $links"
ok "no symlink under muse/"

# ---- 6 and 7. the hooks: one wrapper each, each wrapper a one-line exec of the bootstrap -------------------------
# A hook's command is a JSON array; its first (and only) element is the wrapper, relative to the package root.
hook_subcommands="prompt session-end session-start"
cmds=$(grep -oE '"command"[[:space:]]*:[[:space:]]*\[[^]]*\]' "$manifest" | sed -e 's/^"command"[[:space:]]*:[[:space:]]*\[//' -e 's/\]$//')
[ -n "$cmds" ] || die "$manifest declares no hook \"command\""
count=0
printf '%s\n' "$cmds" | while IFS= read -r arr; do
  first=$(printf '%s' "$arr" | sed -nE 's/^[[:space:]]*"([^"]*)".*/\1/p')
  rest=$(printf '%s' "$arr" | sed -nE 's/^[[:space:]]*"[^"]*"[[:space:]]*(.*)$/\1/p')
  [ -n "$first" ] || die "$manifest has an empty hook command"
  [ -z "$rest" ] || die "$manifest hook command $arr has more than one element; the wrapper carries the subcommand"
  case $first in
    hooks/*.sh) ;;
    *) die "$manifest hook command '$first' is not a wrapper under hooks/" ;;
  esac
  wrapper=$root/$first
  [ -f "$wrapper" ] || die "$manifest names a wrapper that does not exist: $first"
  [ -x "$wrapper" ] || die "wrapper is not executable: $wrapper"
  printf '%s\n' "$modes" | grep -q "^100755 .*	$wrapper\$" || die "git does not track $wrapper as 100755"
  # shellcheck disable=SC2016  # the pattern matches the wrapper's literal $dir and $@
  sub=$(sed -nE 's/^exec "\$dir\/\.\.\/bin\/brigade" hook ([a-z-]+) "\$@"$/\1/p' "$wrapper")
  [ -n "$sub" ] || die "$wrapper does not end in exec \"\$dir/../bin/brigade\" hook <subcommand> \"\$@\""
  found=no
  for known in $hook_subcommands; do [ "$sub" = "$known" ] && found=yes; done
  [ "$found" = yes ] || die "$wrapper names the hook subcommand '$sub', which the binary does not implement (known: $hook_subcommands)"
  [ "$first" = "hooks/$sub.sh" ] || die "$wrapper execs the '$sub' hook but is not named hooks/$sub.sh"
done
# Duplicate sources: the same wrapper named twice.
dups=$(printf '%s\n' "$cmds" | sed -nE 's/^[[:space:]]*"([^"]*)".*/\1/p' | LC_ALL=C sort | uniq -d)
[ -z "$dups" ] || die "two hooks share one source, which Muse refuses: $dups"
count=$(printf '%s\n' "$cmds" | grep -c .)
[ "$count" = 3 ] || die "$manifest declares $count hooks, want exactly 3 (SessionStart, UserPromptSubmit, SessionEnd)"
ok "the three hooks each name their own wrapper, which execs the bootstrap with a subcommand the binary implements"

# ---- 8. the wrappers parse everywhere, and shellcheck -------------------------------------------------------------
checked="sh"
for w in "$root"/hooks/*.sh; do sh -n "$w" || die "sh -n $w failed"; done
for shell in bash zsh dash; do
  if command -v "$shell" >/dev/null 2>&1; then
    for w in "$root"/hooks/*.sh; do "$shell" -n "$w" || die "$shell -n $w failed"; done
    checked="$checked $shell"
  fi
done
if command -v shellcheck >/dev/null 2>&1; then
  shellcheck -s sh "$root"/hooks/*.sh || die "shellcheck reported problems"
  ok "wrappers parse under: $checked; shellcheck -s sh clean"
elif [ -n "${CI:-}" ]; then
  die "shellcheck is not installed and CI is set"
else
  printf 'muse-plugin-check: WARNING: shellcheck is not installed; skipping (CI enforces it)\n' >&2
  ok "wrappers parse under: $checked (shellcheck skipped, not CI)"
fi

# ---- 9. the skills the manifest names exist and stay reachable (card 28) ----------------------------------------
grep -oE '"path"[[:space:]]*:[[:space:]]*"skills/[^"]*"' "$manifest" | sed -e 's/^"path"[[:space:]]*:[[:space:]]*"//' -e 's/"$//' |
  while IFS= read -r p; do
    [ -f "$root/$p" ] || die "$manifest names a skill that does not exist: $p"
  done
disabled=""
for f in "$root"/skills/*/SKILL.md; do
  [ -f "$f" ] || continue
  if grep -qE '^[[:space:]]*disable-model-invocation[[:space:]]*:' "$f"; then disabled="$disabled $f"; fi
done
[ -z "$disabled" ] || die "disable-model-invocation is declared in:$disabled -- Brigade's skills are reachable by the model (card 28)"
ok "every skill the manifest names exists and none declares disable-model-invocation"

# ---- 10. the marketplace catalog (card 115) -----------------------------------------------------------------------
# Muse reads marketplace.json at the repository root first (before the Codex and the Claude Code catalogs), so a
# Muse member installs this package with `muse plugins install brigade@<marketplace>`. Its one entry must name this
# package, pin the release and carry the package digest (`make muse-digest` writes it); the digest's value is
# `brigade-muse-digest check`'s, the make target's second step.
catalog=marketplace.json
[ -f "$catalog" ] || die "missing $catalog: the Muse marketplace catalog (card 115), what \`muse plugins marketplace add brigade appshapes/brigade\` reads"
grep -qE '^[[:space:]]*"schemaVersion":[[:space:]]*1[[:space:]]*,?[[:space:]]*$' "$catalog" || die "$catalog declares no \"schemaVersion\": 1"
grep -qE '^[[:space:]]*"name":[[:space:]]*"brigade"' "$catalog" || die "$catalog names no plugin brigade"
grep -qE '"transport":[[:space:]]*"local-path"' "$catalog" || die "$catalog's install transport is not local-path (the only transport a snapshot supports)"
grep -qE '"source":[[:space:]]*"muse/brigade"' "$catalog" || die "$catalog does not point at muse/brigade"
grep -qE '"status":[[:space:]]*"available"' "$catalog" || die "$catalog's availability is not \"available\" (Muse installs no other status)"
grep -qE '^[[:space:]]*"digest":[[:space:]]*"sha256:[0-9a-f]{64}"' "$catalog" || die "$catalog carries no \"digest\": \"sha256:<64 hex>\" on its own line (make muse-digest writes it)"
catalog_version=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$catalog" | head -n 1)
[ "$catalog_version" = "$version" ] || die "$catalog version '$catalog_version' != $root/bin/VERSION '$version' (make muse-digest rewrites both)"
ok "$catalog names brigade at muse/brigade, pins $version and carries a sha256 digest"

printf 'muse-plugin-check: all checks passed\n'
