#!/bin/sh
# usage: work_item=<n> scripts/release-pin.sh <version>   (run by `make release-pin version=0.60.0 work_item=<n>`,
#        and by scripts/release-prep.sh as the last step of `make release`)
#
# Points the Codex CLI and the Grok Build package at a published release (work item 424, ADR 0030). Both
# harnesses read their marketplace file from `master`: .agents/plugins/marketplace.json and
# .grok-plugin/marketplace.json name the package at one commit, the commit of the last published release, and
# this script is the only thing that moves it. Until it has run for a release, the members of the two harnesses
# stay on the release before, which is safe: a release that stops before this step, or whose step fails, has
# changed nothing for them. Run by itself it finishes such a release.
#
#   preconditions   a version token and a work item's number, the repository root, `master`, a tree with no
#                   change but this script's own, gh, the tag v<version> on origin, and <version> the release
#                   origin/master's plugin/bin/VERSION pins: the pin never goes back to an older release
#   a pre-release   (a version with a `-`) moves nothing: exit 0
#   already there   origin/master names the tag's commit in both files: said, exit 0
#   1  the release is published   waited for: release.yml publishes its draft as its last step. A run of
#                                 release.yml that ended any other way than in success stops the wait at once,
#                                 and so does a gh that cannot ask (logged out, no network), with its words
#   2  write        go run ./cmd/brigade-packages pin <the tag's commit>, which refuses a commit that is not a
#                   release tag's or that holds no package
#   3  commit       make push message="<work item>: Point the Codex CLI and Grok Build packages at <version>"
#                   (typecheck -> pull -> lint -> build -> test -> add -> commit -> push). When an earlier run
#                   made the commit and origin refused its push, the tree is clean: make pull, then git push
#
# The commit is an ordinary commit on `master`: it comes after the tag, changes no release pin, and ci.yml runs
# on it as on any other. Nothing here makes, moves or reads a tag's message, and nothing here is the release
# workflows' to wait for.
#
# Two numbers a caller or a test may set, in seconds: RELEASE_PIN_WAIT_SECONDS (how long to wait for the release
# to be published, 900: release.yml takes about two minutes) and RELEASE_PIN_POLL_SECONDS (15, at least 1).
#
# POSIX sh, `shellcheck -s sh` clean. It does not cd.
set -eu

die() { printf 'release-pin: %s\n' "$1" >&2; exit 1; }
say() { printf 'release-pin: %s\n' "$1"; }

codex_market=.agents/plugins/marketplace.json
grok_market=.grok-plugin/marketplace.json

v=${1:-}
work_item=${work_item:-}
limit=${RELEASE_PIN_WAIT_SECONDS:-900}
poll=${RELEASE_PIN_POLL_SECONDS:-15}

# ---- preconditions -------------------------------------------------------------------------------------------
[ -n "$v" ] || die "usage: make release-pin version=X.Y.Z work_item=<n>"
case $v in
  v*)                   die "pass the version without the leading 'v' (got '$v')" ;;
  *[!0-9A-Za-z.+-]*)    die "not a version token: '$v'" ;;
  [0-9]*.[0-9]*.[0-9]*) ;;
  *)                    die "not a MAJOR.MINOR.PATCH version: '$v'" ;;
esac
case $work_item in
  '')        die "usage: make release-pin version=X.Y.Z work_item=<n>: work_item is the number of the release's work item on the team's Brigade Board (the commit reads '<work item>: Point the Codex CLI and Grok Build packages at X.Y.Z')" ;;
  *[!0-9]*)  die "work_item must be a work item's number (got '$work_item')" ;;
esac
for n in "$limit" "$poll"; do
  case $n in
    ''|*[!0-9]*) die "RELEASE_PIN_WAIT_SECONDS and RELEASE_PIN_POLL_SECONDS are whole numbers of seconds (got '$n')" ;;
  esac
done
[ "$poll" -ge 1 ] || die "RELEASE_PIN_POLL_SECONDS is at least 1"

if [ ! -f go.mod ] || [ ! -f "$codex_market" ] || [ ! -f "$grok_market" ]; then
  die "run this from the repository root (no go.mod, $codex_market or $grok_market here)"
fi
git rev-parse --git-dir >/dev/null 2>&1 || die "not a git repository"

case $v in
  *-*) say "v$v is a pre-release: the Codex CLI and Grok Build packages stay at the last release"; exit 0 ;;
esac

branch=$(git branch --show-current)
[ "$branch" = master ] || die "the pin is moved on master, not on '$branch': the two harnesses read the marketplace files from the default branch"

# Step 3's `make push` stages every change in the checkout, so nothing may be there but what an earlier run of
# this script left: the two files, written and not yet committed. Asked here, and again just before step 3,
# because step 1 can wait for minutes in a checkout another session shares.
only_the_pin() {
  other=$(git status --porcelain --untracked-files=normal | grep -v -x -F -e " M $codex_market" -e " M $grok_market" || true)
  if [ -n "$other" ]; then
    printf '%s\n' "$other" >&2
    die "tree not clean: commit or discard these changes first (step 3's 'make push' runs 'git add :/ .')$1"
  fi
}
only_the_pin ""

command -v gh >/dev/null 2>&1 || die "gh is not on PATH: whether v$v is published is read with it (https://cli.github.com), logged in"

git fetch --quiet --tags origin master || die "could not fetch master and the tags from origin"
commit=$(git rev-parse --quiet --verify "refs/tags/v$v^{commit}") || die "there is no tag v$v: the pin is the commit of a release, and 'make release version=$v work_item=<n>' is what tags one"

# The pin goes forward only: to the release master pins, which is the newest. A hint from a release that
# stopped here, followed after the next release moved the pin, would otherwise put members back.
pinned_version=$(git show origin/master:plugin/bin/VERSION 2>/dev/null | head -n 1) || pinned_version=
[ "$pinned_version" = "$v" ] || die "origin/master's plugin/bin/VERSION pins '$pinned_version', not $v: the packages are pointed at the release master pins, never back at an older one"

# ---- already there ---------------------------------------------------------------------------------------------
stay="the Codex CLI and Grok Build packages stay at the release before"
origin_pins() { git show "origin/master:$1" 2>/dev/null | grep -q -F "\"sha\": \"$commit\""; }
if origin_pins "$codex_market" && origin_pins "$grok_market"; then
  say "origin/master points the Codex CLI and Grok Build packages at v$v ($commit) already: nothing to do"
  exit 0
fi

# ---- 1. the release is published -------------------------------------------------------------------------------
# A member's harness fetches the package at the pinned commit and its bootstrap downloads the release's binary,
# so the pin moves only once that binary can be downloaded.
# gh's own answer for a release that is not there yet is `release not found`; any other failure is a gh that
# cannot ask, and waiting would only report it as a release nobody published.
published() {
  if answer=$(gh release view "v$v" --json isDraft --jq .isDraft 2>&1); then
    [ "$answer" = false ]
    return
  fi
  case $answer in
    *'release not found'*) return 1 ;;
  esac
  die "gh could not read the release v$v ($answer): is gh logged in, is the network up? $stay. Run 'make release-pin version=$v work_item=$work_item' when it can"
}
# The newest run of release.yml on the tag's commit as one line, "<id> <status> <conclusion> <url>", or nothing.
release_run() {
  gh run list --workflow release.yml --commit "$commit" --limit 20 \
    --json databaseId,status,conclusion,url \
    --jq 'sort_by(.databaseId) | last | select(. != null) | [(.databaseId | tostring), .status, (if (.conclusion // "") == "" then "-" else .conclusion end), .url] | join(" ")' 2>/dev/null || true
}
waited=0
told=0
until published; do
  line=$(release_run)
  if [ -n "$line" ]; then
    IFS=' ' read -r id status conclusion url <<EOF
$line
EOF
    if [ "$status" = completed ] && [ "$conclusion" != success ]; then
      die "release.yml ended in $conclusion on v$v (run $id, $url), so v$v is not published: $stay. When the release is published, run 'make release-pin version=$v work_item=$work_item'"
    fi
  fi
  [ "$waited" -lt "$limit" ] || die "v$v is not published after ${limit}s: $stay. When it is, run 'make release-pin version=$v work_item=$work_item'"
  if [ "$told" = 0 ]; then
    say "waiting up to ${limit}s for release.yml to publish v$v"
    told=1
  fi
  sleep "$poll"
  waited=$((waited + poll))
done
say "1. v$v is published"

# ---- 2. write the commit into both files -------------------------------------------------------------------------
go run ./cmd/brigade-packages pin "$commit" || die "the pin was not written: $stay"
say "2. wrote $commit into $codex_market and $grok_market"

# ---- 3. commit and push --------------------------------------------------------------------------------------------
only_the_pin ". The two marketplace files are written and not committed: run 'make release-pin version=$v work_item=$work_item' again when the tree holds nothing else"
if [ -n "$(git status --porcelain --untracked-files=normal)" ]; then
  make push message="$work_item: Point the Codex CLI and Grok Build packages at $v"     # typecheck -> pull -> lint -> build -> test -> add -> commit -> push
else
  # Nothing to commit, and origin does not have the pin: an earlier run made the commit and its push was
  # refused, because origin had moved under the chain.
  make pull
  git push origin master
fi
say "3. pushed: the Codex CLI and Grok Build packages are at v$v. A Codex CLI member gets it at a start of Codex, with one review of Brigade's hooks; a Grok Build member at their next update"
