#!/bin/sh
# usage: scripts/ci/codex-plugin-check.sh          (run from the repository root; no arguments)
#
# Static checks of the shipped Codex CLI package, codex/brigade/ (card 107), the sibling of scripts/ci/plugin-check.sh
# for plugin/ and scripts/ci/muse-plugin-check.sh for muse/brigade/. Exits non-zero on the FIRST failure. One
# `ok: …` line per check.
#
# 1  codex/brigade/ holds only allow-listed paths          6  hooks.json declares exactly SessionStart, UserPromptSubmit
# 2  the three pinned assets under bin/ are byte-equal       and SessionEnd, each ONE command string of the exact form
#    to plugin/bin/'s (one canonical pin, three packages)     "${PLUGIN_ROOT}/bin/brigade" hook <event's subcommand>
# 3  the manifest's "version" equals bin/VERSION             --host codex --plugin-root "${PLUGIN_ROOT}", with a
# 4  the manifest is native (name brigade, a `skills`        subcommand the binary implements, and SessionEnd's timeout
#    member, none of mcpServers, channels, userConfig)        within Codex's 3 s maximum
# 5  no symlink anywhere under codex/                      7  .agents/plugins/marketplace.json names the package
#                                                          8  every skills/<dir>/SKILL.md exists, is named after its
#                                                             directory, and none declares disable-model-invocation
#
# The JSON checks are textual on purpose, as plugin-check.sh's are: the manifests are small and hand-written, jq is
# not on every host, and scripts/ci/codex_plugin_check_test.go parses the three files as JSON.
set -eu

die() { printf 'codex-plugin-check: %s\n' "$1" >&2; exit 1; }
ok() { printf 'ok: %s\n' "$1"; }

root=codex/brigade
manifest=$root/.codex-plugin/plugin.json
hooks=$root/hooks/hooks.json
marketplace=.agents/plugins/marketplace.json
[ -d "$root" ] || die "run me from the repository root: no $root directory here"
[ -f "$manifest" ] || die "missing $manifest"
[ -f "$hooks" ] || die "missing $hooks"

# ---- 1. the file allowlist ----------------------------------------------------------------------------------------
find "$root" -type f -print | LC_ALL=C sort | while IFS= read -r f; do
  rel=${f#"$root"/}
  case $rel in
    bin/brigade|bin/VERSION|bin/checksums.txt|.codex-plugin/plugin.json|hooks/hooks.json|README.md) ;;
    skills/*/*) ;;
    *) die "not allow-listed under $root/: $f" ;;
  esac
done
ok "$root/ contains only allow-listed paths"

# ---- 2. the pinned assets are the Claude package's, byte for byte ------------------------------------------------
for asset in brigade VERSION checksums.txt; do
  [ -f "plugin/bin/$asset" ] || die "missing plugin/bin/$asset"
  [ -f "$root/bin/$asset" ] || die "missing $root/bin/$asset"
  cmp -s "plugin/bin/$asset" "$root/bin/$asset" || die "$root/bin/$asset differs from plugin/bin/$asset: one canonical pin, three packages (release-prep copies it)"
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
grep -qE '^[[:space:]]*"name":[[:space:]]*"brigade"[[:space:]]*,?[[:space:]]*$' "$manifest" || die "$manifest is not named brigade"
grep -qE '^[[:space:]]*"skills":[[:space:]]*"\./skills/?"' "$manifest" || die "$manifest declares no \"skills\": \"./skills/\" member"
if grep -qE '"(userConfig|mcpServers|channels)"[[:space:]]*:' "$manifest" "$hooks"; then
  die "$manifest or $hooks carries a member this package must not (userConfig, mcpServers or channels)"
fi
ok "$manifest is a native Codex manifest named brigade with a skills directory and none of userConfig, mcpServers, channels"

# ---- 5. no symlink -------------------------------------------------------------------------------------------------
links=$(find codex -type l -print)
[ -z "$links" ] || die "symlink(s) under codex/, which the package must not carry: $links"
ok "no symlink under codex/"

# ---- 6. hooks.json: the three events, each one exact command --------------------------------------------------------
# The list check 6 compares against; scripts/ci/codex_plugin_check_test.go asserts it is exactly the hook package's
# subcommands, as checks_test.go does for plugin-check.sh.
hook_subcommands="prompt session-end session-start"
for event in SessionStart UserPromptSubmit SessionEnd; do
  n=$(grep -cE "^[[:space:]]*\"$event\":[[:space:]]*\[" "$hooks" || true)
  [ "$n" = 1 ] || die "$hooks declares the $event event $n time(s), want exactly once"
done
events=$(grep -cE '^[[:space:]]*"(SessionStart|UserPromptSubmit|SessionEnd|PreToolUse|PostToolUse|PermissionRequest|PreCompact|PostCompact|SubagentStart|SubagentStop|Stop|Interrupt)":[[:space:]]*\[' "$hooks" || true)
[ "$events" = 3 ] || die "$hooks declares $events events, want exactly 3 (SessionStart, UserPromptSubmit, SessionEnd): a Stop hook must print JSON or nothing, and no other event is Brigade's"
cmds=$(grep -oE '"command"[[:space:]]*:[[:space:]]*"([^"\\]|\\.)*"' "$hooks" | sed -e 's/^"command"[[:space:]]*:[[:space:]]*"//' -e 's/"$//' -e 's/\\"/"/g')
[ "$(printf '%s\n' "$cmds" | grep -c .)" = 3 ] || die "$hooks declares $(printf '%s\n' "$cmds" | grep -c .) hook commands, want exactly 3"
types=$(awk '{ n += gsub(/"type"[[:space:]]*:[[:space:]]*"command"/, "") } END { print n + 0 }' "$hooks")
[ "$types" = 3 ] || die "$hooks has $types command-typed hooks, want 3"
printf '%s\n' "$cmds" | while IFS= read -r cmd; do
  sub=$(printf '%s' "$cmd" | sed -nE 's/^"\$\{PLUGIN_ROOT\}\/bin\/brigade" hook ([a-z-]+) --host codex --plugin-root "\$\{PLUGIN_ROOT\}"$/\1/p')
  [ -n "$sub" ] || die "$hooks command is not of the exact form \"\${PLUGIN_ROOT}/bin/brigade\" hook <subcommand> --host codex --plugin-root \"\${PLUGIN_ROOT}\": $cmd"
  found=no
  for known in $hook_subcommands; do [ "$sub" = "$known" ] && found=yes; done
  [ "$found" = yes ] || die "$hooks names the hook subcommand '$sub', which the binary does not implement (known: $hook_subcommands)"
done
# Each event's command carries its own subcommand: the SessionStart block names session-start, and so on.
check_event_sub() {
  block=$(awk -v ev="\"$1\"" '$0 ~ ev {p=1} p {print} p && /^[[:space:]]*\][[:space:]]*,?[[:space:]]*$/ {exit}' "$hooks")
  printf '%s\n' "$block" | grep -qF "hook $2 --host codex" || die "$hooks: the $1 block does not run \`hook $2\`"
}
check_event_sub SessionStart session-start
check_event_sub UserPromptSubmit prompt
check_event_sub SessionEnd session-end
end_timeout=$(awk '/"SessionEnd"/ {p=1} p && /"timeout"/ { gsub(/[^0-9]/, ""); print; exit }' "$hooks")
[ -n "$end_timeout" ] || die "$hooks: the SessionEnd hook declares no timeout"
[ "$end_timeout" -le 3 ] || die "$hooks: the SessionEnd timeout is $end_timeout s; Codex caps a SessionEnd hook at 3 s"
ok "$hooks runs the bootstrap for exactly SessionStart, UserPromptSubmit and SessionEnd with the exact command form, and SessionEnd stays within Codex's 3 s"

# ---- 7. the marketplace names the package ------------------------------------------------------------------------
[ -f "$marketplace" ] || die "missing $marketplace: \`codex plugin marketplace add appshapes/brigade\` reads it"
grep -qE '^[[:space:]]*"name":[[:space:]]*"brigade"' "$marketplace" || die "$marketplace names no plugin brigade"
grep -qE '"path":[[:space:]]*"\./codex/brigade"' "$marketplace" || die "$marketplace does not point at ./codex/brigade"
ok "$marketplace names the brigade plugin at ./codex/brigade"

# ---- 8. the skills exist, are named after their directories, and stay reachable (card 28) -------------------------
count=0
for d in "$root"/skills/*/; do
  [ -d "$d" ] || continue
  name=$(basename "$d")
  f=$d/SKILL.md
  [ -f "$f" ] || die "skill directory $d has no SKILL.md"
  grep -qE "^name:[[:space:]]*${name}[[:space:]]*$" "$f" || die "$f is not named after its directory ($name)"
  if grep -qE '^[[:space:]]*disable-model-invocation[[:space:]]*:' "$f"; then
    die "disable-model-invocation is declared in $f -- Brigade's skills are reachable by the model (card 28)"
  fi
  count=$((count + 1))
done
[ "$count" -gt 0 ] || die "$root/skills/ holds no skill"
ok "$count skills, each named after its directory, none declaring disable-model-invocation"

printf 'codex-plugin-check: all checks passed\n'
