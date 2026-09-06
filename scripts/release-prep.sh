#!/bin/sh
# usage: scripts/release-prep.sh <version> [branch]     (run by `make release version=0.1.0 [branch=<name>]`)
#        DRY_RUN=1 scripts/release-prep.sh <version> [branch]   -- steps 1-3 for real, then stop and PRINT 4-5
#
# The release sequence of plan 7.7. plugin/bin/checksums.txt must already be in the commit the tag points at (a
# plugin installed from that commit verifies the downloaded binary against it), but the release workflow builds
# the binaries from the tag afterwards. The resolution is reproducibility: with the toolchain forced
# (GOTOOLCHAIN), CGO_ENABLED=0, -trimpath, -buildvcs=false and a version-only -X, the bytes do not depend on the
# commit, so the checksums can be computed here, committed, and re-verified by the release job.
#
#   preconditions   a version token, the repository root, a clean tree, the expected branch, the plugin
#                   manifest, bin/goreleaser, and a selectable go<go.mod line> toolchain
#   1  bump the pins        plugin/bin/VERSION and plugin/.claude-plugin/plugin.json "version"
#   2  reproducible build   make cross version=<v>  ->  dist-cross/{brigade_<v>_<os>_<arch>,checksums.txt}
#   3  cross-check          goreleaser's own build of the same four targets; the two checksums.txt must be
#                           identical, which is what stops .goreleaser.yaml and the Makefile drifting apart
#   4  commit               cp dist/checksums.txt plugin/bin/checksums.txt; make push message="15: Release <v>"
#   5  tag                  git tag -a v<v> && git push origin v<v>  ->  release.yml builds, verifies, publishes
#
# POSIX sh, no process substitution, `shellcheck -s sh` clean (plugin-check.sh check 7 enforces it). Never
# `CDPATH= cd` -- that empty prefix assignment is SC1007; this script does not cd at all.
#
# Failure and recovery (7.7): if the release job's verification fails the draft is deleted and nothing is
# published; fix the drift, then delete the never-published tag with
#   git tag -d v<v> && git push --delete origin v<v>
# before rerunning (git tag -a refuses an existing local tag). A PUBLISHED tag is never rewritten: bump the
# patch version instead.
#
# The workflow-only fix, which the deletion above does not finish. If the failure was in release.yml (or in
# anything else this script does not build), a re-run of `make release version=<v>` DIES AT STEP 4: the pins are
# already <v>, so step 1 is a no-op; no built byte changed, so the cp is a no-op too; and `make push` fails with
# "nothing to commit". Do step 5 by hand instead:
#   git tag -a v<v> -m v<v> && git push origin v<v>
# release.yml triggers on `v*` and on nothing else -- there is no workflow_dispatch -- so a release cannot be
# re-driven without a tag. And note that deleting the tag leaves the release commit on the branch: it pins <v>
# against checksums no published release backs, so until the retag keep every fix to non-Go files -- a Go source
# change makes checksums-check's fresh build differ from the committed file and turns the `fast` job red.
set -eu

die() { printf 'release-prep: %s\n' "$1" >&2; exit 1; }
say() { printf 'release-prep: %s\n' "$1"; }

plugin_json=plugin/.claude-plugin/plugin.json
version_file=plugin/bin/VERSION
committed=plugin/bin/checksums.txt

v=${1:-}
# `make release` passes $(branch), which is EMPTY when the caller did not set it, so an empty second argument
# has to mean the default too -- ${2:-master} alone would leave want_branch empty and no branch would match.
want_branch=${2:-}
[ -n "$want_branch" ] || want_branch=master
# DRY_RUN is a switch, not a number: `DRY_RUN=true` must not silently perform a REAL release (commit, push,
# tag, push the tag). Anything but unset/empty/0/no/false turns the dry run ON, so a typo fails SAFE.
case ${DRY_RUN:-0} in
  ''|0|no|false) dry_run=0 ;;
  *)             dry_run=1 ;;
esac

# ---- preconditions -------------------------------------------------------------------------------------------
[ -n "$v" ] || die "usage: scripts/release-prep.sh <version> [branch]   (make release version=X.Y.Z [branch=<name>])"
case $v in
  v*)                   die "pass the version without the leading 'v' (got '$v'); the script tags v<version> itself" ;;
  # `make release` with no version= does NOT hit the Makefile's `test -n "$(version)"` guard: `version ?=
  # $(plugin_version)` has already defaulted it to whatever plugin/bin/VERSION pins. 0.0.0 is the pre-release
  # sentinel checksums-check.sh requires an EMPTY checksums.txt at, so releasing it would poison every later
  # commit's fast job. Refuse it here, where the mistake is still cheap.
  0.0.0)                die "0.0.0 is the pre-release sentinel (checksums-check.sh requires $committed to be EMPTY there), so it cannot be released: pass make release version=X.Y.Z" ;;
  *[!0-9A-Za-z.+-]*)    die "not a version token: '$v'" ;;
  [0-9]*.[0-9]*.[0-9]*) ;;
  *)                    die "not a MAJOR.MINOR.PATCH version: '$v'" ;;
esac

if [ ! -f go.mod ] || [ ! -f "$version_file" ]; then
  die "run this from the repository root (no go.mod / $version_file here)"
fi
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"

# `git diff`/`git diff --cached` see TRACKED changes only. Step 4's `make push` runs `git add --verbose :/ .`,
# so an untracked file would be swept into the release commit and the tag would point at a commit carrying work
# nobody reviewed. --porcelain covers staged, unstaged, unmerged AND untracked; the explicit
# --untracked-files=normal defeats a local `status.showUntrackedFiles=no`. Ignored files stay ignored.
dirty=$(git status --porcelain --untracked-files=normal)
if [ -n "$dirty" ]; then
  printf '%s\n' "$dirty" >&2
  die "tree not clean: commit or discard your changes first (step 4's 'make push' runs 'git add :/ .')"
fi

branch=$(git branch --show-current)
[ "$branch" = "$want_branch" ] || die "release from $want_branch, not '$branch' (pass branch=<name> only for a rehearsal)"

# The manifest is checked BEFORE anything is mutated: `checksums-check.sh` rule (a) requires plugin.json's
# "version" to equal VERSION, so a release without a manifest would commit a pin nothing can verify. Until
# P3-1 lands the manifest, a real run stops here (the P2-12 rehearsal writes a throwaway one on its branch).
[ -f "$plugin_json" ] || die "$plugin_json does not exist: the plugin manifest must exist and carry a \"version\" before a release (it lands with P3-1)"

[ -x bin/goreleaser ] || die "bin/goreleaser is missing or not executable: run 'make setup-goreleaser'"

go_line=$(sed -n 's/^go //p' go.mod | head -n 1)
[ -n "$go_line" ] || die "go.mod declares no 'go' line: cannot pin the toolchain"
# Forced, not `auto`: `auto` uses the local toolchain whenever it is at least as new as the go line, so a
# developer one patch release ahead would build different bytes than CI and the checksums would not reproduce.
have=$(GOTOOLCHAIN="go$go_line" go env GOVERSION) || die "cannot run 'go env GOVERSION' under GOTOOLCHAIN=go$go_line"
[ "$have" = "go$go_line" ] || die "cannot select toolchain go$go_line (go env GOVERSION says '$have')"

say "preparing v$v on $branch with GOTOOLCHAIN=go$go_line"

if [ "$dry_run" = 1 ]; then
  # A rehearsal branch has no upstream, and a dry run must not move the tree it was pointed at anyway.
  say "DRY_RUN=1: skipping 'git pull --no-edit'"
else
  git pull --no-edit
fi

# ---- 1. bump the pins ------------------------------------------------------------------------------------------
# The MANIFEST is bumped and verified FIRST; $version_file is written only once that took. release.yml and
# checksums-check.sh both read the manifest with this same anchored sed, so a "version" that is not at the
# start of its own line reads as "declares no version" -- and on such a manifest the sed below is a no-op, so
# failing here leaves the tree as it was found instead of stranding a bumped VERSION beside a stale manifest.
sed -i.bak -E "s/^([[:space:]]*\"version\":[[:space:]]*)\"[^\"]+\"/\1\"$v\"/" "$plugin_json"
rm -f "$plugin_json.bak"
manifest=$(sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p' "$plugin_json" | head -n 1)
[ "$manifest" = "$v" ] || die "the bump did not take in $plugin_json (its version reads as '$manifest'): \"version\" must start its own line"
printf '%s\n' "$v" > "$version_file"
say "1. pinned $version_file and $plugin_json to $v"

# ---- 2. the reproducible build ---------------------------------------------------------------------------------
make cross version="$v"
[ -f dist-cross/checksums.txt ] || die "make cross produced no dist-cross/checksums.txt"
say "2. built dist-cross/ with the release flags"

# ---- 3. goreleaser must agree ----------------------------------------------------------------------------------
# --skip=validate bypasses goreleaser's dirty-tree and tag checks (the tag does not exist yet, and step 1 just
# dirtied the tree); --snapshot is NOT usable, it rewrites the version to <v>-SNAPSHOT-<commit> and so changes
# the bytes. With `formats: [binary]` dist/ holds brigade_<os>_<arch>_<v1|v8.0>/brigade DIRECTORIES -- the
# name_template applies to the uploads only -- so compare the checksum FILES, never dist/brigade_*.
GORELEASER_CURRENT_TAG="v$v" GOTOOLCHAIN="go$go_line" bin/goreleaser release --clean --skip=publish,validate,announce
diff dist/checksums.txt dist-cross/checksums.txt || die "goreleaser and 'make cross' disagree: .goreleaser.yaml and the Makefile have drifted apart"
say "3. goreleaser reproduces dist-cross/checksums.txt byte for byte"

if [ "$dry_run" = 1 ]; then
  say "DRY_RUN=1: stopping before the commit. A real run would now:"
  say "  4. cp dist/checksums.txt $committed"
  say "  4. make push message=\"15: Release $v\""
  say "  5. git tag -a v$v -m v$v && git push origin v$v"
  exit 0
fi

# ---- 4. commit the checksums goreleaser produced -----------------------------------------------------------------
cp dist/checksums.txt "$committed"
make push message="15: Release $v"        # typecheck -> pull -> build -> test -> add -> commit -> push
say "4. committed $committed"

# ---- 5. tag the commit that now carries VERSION + checksums --------------------------------------------------------
git tag -a "v$v" -m "v$v"
git push origin "v$v"
say "5. pushed v$v; release.yml now builds, verifies against $committed and publishes the draft"
