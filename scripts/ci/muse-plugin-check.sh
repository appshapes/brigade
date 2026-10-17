#!/bin/sh
# usage: scripts/ci/muse-plugin-check.sh          (run from the repository root; no arguments)
#
# Static checks of the shipped Muse Code package, muse/brigade/ (card 88), the sibling of scripts/ci/plugin-check.sh
# for plugin/. Exits non-zero on the FIRST failure. One `ok: …` line per check.
#
# 1  muse/brigade/ holds only allow-listed paths          6  every hook command is one argv element naming a
# 2  the three pinned assets under bin/ are byte-equal       wrapper that exists, is executable and is tracked 100755
#    to plugin/bin/'s (one canonical pin, two packages)   7  every wrapper execs `bin/brigade hook <subcommand>` with a
# 3  the manifest's "version" equals bin/VERSION             subcommand the binary implements, and no two hooks share a
# 4  the manifest is native (schemaVersion 1, a            source (Muse refuses that)
#    `capabilities` member, no `args`, no `userConfig`)   8  sh -n / bash -n / zsh -n and shellcheck -s sh on the wrappers
# 5  no symlink anywhere under muse/ (Muse refuses the    9  every skill the manifest names exists, and no skill declares
#    package)                                                disable-model-invocation (card 28)
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
ok "$root/ contains only allow-listed paths"

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

printf 'muse-plugin-check: all checks passed\n'
