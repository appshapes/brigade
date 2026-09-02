#!/bin/sh
# usage: scripts/ci/plugin-check.sh          (run from the repository root; no arguments)
#
# Static checks of the shipped plugin tree (plan 7.7; `make plugin-check` runs this and then no-secrets.sh).
# Exits non-zero on the FIRST failure, so a red line is always the whole story. One `ok: …` line per check;
# `skip: … (P3-1)` for a check whose file has not landed yet (plugin/.claude-plugin/plugin.json,
# plugin/hooks/hooks.json and plugin/skills/ arrive with P3-1).
#
# 1  plugin/ holds only allow-listed paths            5  hooks.json is exec-form and its commands exist
# 2  plugin/bin/brigade is 100755 in git, rest 100644 6  sh -n / bash -n / zsh -n on the bootstrap
# 3  VERSION is one token and equals plugin.json      7  shellcheck -s sh on the bootstrap and the CI scripts
# 4  no mcpServers, no channels anywhere under plugin/ (a .mcp.json file is check 1's job)
#
# The JSON checks are textual on purpose: hooks.json and plugin.json are small hand-written manifests, jq is not
# on every host that runs `make plugin-check`, and the schema half is covered by `make plugin-validate`
# (`claude plugin validate --strict`, which E0-8 (g) measured as login-free but blind to a missing hook binary --
# which is why check 5 exists here).
set -eu

die() { printf 'plugin-check: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }
skip() { printf 'skip: %s (P3-1)\n' "$1"; }

[ -d plugin ] || die "run me from the repository root: no plugin/ directory here"

plugin_json=plugin/.claude-plugin/plugin.json
hooks_json=plugin/hooks/hooks.json

# ---- 1. the plugin/ file allowlist -------------------------------------------------------------------------------
# find, not `git ls-files`: an untracked stray (a .DS_Store, a dist/, a hand-built binary) would be packaged by
# `--plugin-dir` just the same, so the check has to see the working tree.
find plugin -type f -print | LC_ALL=C sort | while IFS= read -r f; do
  rel=${f#plugin/}
  case $rel in
    bin/brigade|bin/VERSION|bin/checksums.txt|.claude-plugin/plugin.json|hooks/hooks.json|README.md) ;;
    skills/*/SKILL.md)
      mid=${rel#skills/}; mid=${mid%/SKILL.md}
      case $mid in ''|*/*) die "not allow-listed: $f (skills must be skills/<name>/SKILL.md)" ;; esac ;;
    *) die "not allow-listed under plugin/: $f" ;;
  esac
done
ok "plugin/ contains only allow-listed paths"

# ---- 2. file modes as git records them (the exec-form hook runs the file directly) --------------------------------
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository: cannot check the recorded file modes"
modes=$(git ls-files -s plugin) || die "git ls-files failed"
[ -n "$modes" ] || die "git tracks no file under plugin/"
printf '%s\n' "$modes" | while IFS= read -r line; do
  mode=${line%% *}
  path=${line#*	}
  case $path in
    plugin/bin/brigade)
      [ "$mode" = 100755 ] || die "plugin/bin/brigade is $mode in the index, must be 100755 (git update-index --add --chmod=+x plugin/bin/brigade)" ;;
    *)
      [ "$mode" = 100644 ] || die "$path is $mode in the index, must be 100644 (only plugin/bin/brigade is executable)" ;;
  esac
done
printf '%s\n' "$modes" | grep -q '^100755 .*	plugin/bin/brigade$' || die "git does not track plugin/bin/brigade as 100755"
ok "plugin/bin/brigade is 100755 in git and nothing else under plugin/ is executable"

# ---- 3. the version pin ------------------------------------------------------------------------------------------
[ -r plugin/bin/VERSION ] || die "missing plugin/bin/VERSION"
[ "$(awk 'END {print NR}' plugin/bin/VERSION)" = 1 ] || die "plugin/bin/VERSION must be exactly one line"
version=$(head -n 1 plugin/bin/VERSION)
case $version in
  ''|*[!0-9A-Za-z.+-]*) die "plugin/bin/VERSION is not a version token: '$version'" ;;
esac
if [ -f "$plugin_json" ]; then
  manifest=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$plugin_json" | head -n 1)
  [ -n "$manifest" ] || die "$plugin_json declares no \"version\""
  [ "$manifest" = "$version" ] || die "$plugin_json version '$manifest' != plugin/bin/VERSION '$version'"
  ok "plugin/bin/VERSION ($version) matches $plugin_json"
else
  ok "plugin/bin/VERSION is one token ($version)"
  skip "VERSION == plugin.json: $plugin_json does not exist yet"
fi

# ---- 4. no MCP, no channels (success criterion 10) ---------------------------------------------------------------
# A plugin/.mcp.json FILE is not tested here: check 1's allowlist does not list it and runs first, so the file
# never reaches this check -- an `[ ! -e plugin/.mcp.json ]` guard here was dead code. What is left is the
# content half, which the allowlist cannot see: an mcpServers or channels key inside an allow-listed file.
if grep -rlE '"mcpServers"|"channels"' plugin >/dev/null 2>&1; then
  grep -rnE '"mcpServers"|"channels"' plugin >&2 || true
  die "an mcpServers or channels key appears under plugin/"
fi
if grep -rlF -e '--channels' -e 'claude/channel' plugin >/dev/null 2>&1; then
  grep -rnF -e '--channels' -e 'claude/channel' plugin >&2 || true
  die "'--channels' or 'claude/channel' appears under plugin/"
fi
ok "no mcpServers, no channels and no --channels anywhere under plugin/ (.mcp.json: check 1)"

# ---- 5. hooks.json: exec form, and every command actually there ---------------------------------------------------
# Exec form (plan 6.3): each entry is {"type": "command", "command": "<one path>", "args": [...]}. The command
# names ONE executable by absolute path or through ${CLAUDE_PLUGIN_ROOT} -- never a shell string, never a bare
# name (hooks do not get the plugin's bin/ on their PATH), and any arguments live in the args array.
if [ -f "$hooks_json" ]; then
  # Count OCCURRENCES, not matching lines. `grep -c` counts lines, so a hooks.json that puts two hook objects
  # on one line -- or a minified one that puts them all on one -- reported 1 and 1 here and the comparison
  # below could never fail, which is exactly the shape it exists to catch.
  types=$(awk '{ n += gsub(/"type"[[:space:]]*:/, "") } END { print n + 0 }' "$hooks_json")
  cmds=$(awk '{ n += gsub(/"command"[[:space:]]*:[[:space:]]*"/, "") } END { print n + 0 }' "$hooks_json")
  [ "$types" -gt 0 ] || die "$hooks_json declares no hook \"type\""
  [ "$types" = "$cmds" ] || die "$hooks_json has $types \"type\" keys but $cmds \"command\" strings: every hook must be exec form"
  bad=$(grep -oE '"type"[[:space:]]*:[[:space:]]*"[^"]*"' "$hooks_json" | grep -cv '"command"$' || true)
  [ "$bad" = 0 ] || die "$hooks_json has $bad hook entries whose \"type\" is not \"command\""
  grep -oE '"command"[[:space:]]*:[[:space:]]*"[^"]*"' "$hooks_json" |
    sed -e 's/^"command"[[:space:]]*:[[:space:]]*"//' -e 's/"$//' |
    while IFS= read -r cmd; do
      [ -n "$cmd" ] || die "$hooks_json has an empty \"command\""
      rest=${cmd#\$\{CLAUDE_PLUGIN_ROOT\}}
      case $rest in
        "$cmd")
          case $cmd in /*) ;; *) die "$hooks_json command '$cmd' is neither absolute nor \${CLAUDE_PLUGIN_ROOT}-rooted" ;; esac
          resolved=$cmd ;;
        /*) resolved=plugin$rest ;;
        *) die "$hooks_json command '$cmd' must continue with / after \${CLAUDE_PLUGIN_ROOT}" ;;
      esac
      case $cmd in
        *[\ \	\;\|\&\<\>\(\)\`\"\']*) die "$hooks_json command '$cmd' is a shell string, not exec form: put arguments in \"args\"" ;;
      esac
      # E0-8 (g): `claude plugin validate --strict` exits 0 on a hooks.json pointing at a missing binary.
      [ -f "$resolved" ] || die "$hooks_json names a hook command that does not exist: $cmd -> $resolved"
      [ -x "$resolved" ] || die "$hooks_json names a hook command that is not executable: $cmd -> $resolved"
    done
  args_bad=$(grep -oE '"args"[[:space:]]*:[[:space:]]*[^[:space:]]' "$hooks_json" | grep -cv '\[$' || true)
  [ "$args_bad" = 0 ] || die "$hooks_json has an \"args\" value that is not a JSON array"
  ok "$hooks_json is exec form and every hook command exists and is executable"
else
  skip "hooks.json exec form and command existence: $hooks_json does not exist yet"
fi

# ---- 6. the bootstrap parses under every shell a user might have --------------------------------------------------
[ -f plugin/bin/brigade ] || die "missing plugin/bin/brigade"
sh -n plugin/bin/brigade || die "sh -n plugin/bin/brigade failed"
checked="sh"
for shell in bash zsh dash; do
  if command -v "$shell" >/dev/null 2>&1; then
    "$shell" -n plugin/bin/brigade || die "$shell -n plugin/bin/brigade failed"
    checked="$checked $shell"
  fi
done
ok "plugin/bin/brigade parses under: $checked"

# ---- 7. shellcheck ------------------------------------------------------------------------------------------------
if command -v shellcheck >/dev/null 2>&1; then
  set -- plugin/bin/brigade
  for f in scripts/ci/*.sh scripts/*.sh; do
    if [ -f "$f" ]; then set -- "$@" "$f"; fi
  done
  shellcheck -s sh "$@" || die "shellcheck reported problems"
  ok "shellcheck -s sh clean: $*"
elif [ -n "${CI:-}" ]; then
  die "shellcheck is not installed and CI is set: the Ubuntu runner preinstalls it, so this is a real failure"
else
  printf 'plugin-check: WARNING: shellcheck is not installed; skipping (CI enforces it)\n' >&2
  ok "shellcheck skipped with a warning (not CI)"
fi

printf 'plugin-check: all checks passed\n'
