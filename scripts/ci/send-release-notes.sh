#!/bin/sh
# usage: scripts/ci/send-release-notes.sh window       which releases the email covers, and its sources
#        scripts/ci/send-release-notes.sh recipients   the addresses the email goes to
#        scripts/ci/send-release-notes.sh finish       the approved draft with the footer under it
#
# The three deterministic steps of .github/workflows/send-release-notes.yml (card 43). The email's text is
# composed by an agent and gated by release-notes-lint.sh and a reviewer; WHICH releases it covers, WHO
# receives it and WHAT it says about why they received it are decided here, by the shell.
#
#   window       The releases published after the last email and up to this run's start. "The last email" is
#                the newest successful run of the workflow in which the step named by `send_step` below
#                concluded `success` -- a preview, a run with nothing to send and a run that failed all leave
#                the mark where it was, so the next run covers what they did not. No such run: the last seven
#                days. BRIGADE_EMAIL_FROM (the workflow's `from` input, a tag) overrides both and is
#                INCLUSIVE: `v0.5.0` means the email starts with 0.5.0. It writes window.txt (one tag a line,
#                oldest first), sources/<tag>.md (that release's published notes, which passed the
#                release-notes gate) and sources/changelog.md (CHANGELOG.md's sections for those versions).
#   recipients   The addresses of BRIGADE_EMAIL_RECIPIENTS, a list the maintainers keep by hand (owner,
#                2026-09-29: GitHub gives out the address of almost nobody who starred, so the list is fixed
#                for now and collecting addresses is a later card). Each entry is held to one plain address,
#                lower-cased and counted once. It writes recipients.txt (0600) and the `bcc` output, and
#                registers every address as a mask BEFORE anything else can print it: this repository is
#                public and so are its run logs. It makes no request.
#   finish       Writes email.md: notes.md, then a footer that says where the release notes are, why the
#                reader received this and how to stop it -- by answering the email, which is why the
#                workflow sets a Reply-To that someone reads. The footer is the shell's and not the agent's
#                so that no draft can leave it out.
#
# Environment:
#   BRIGADE_EMAIL_DIR         the working directory, outside the checkout (the workflow: /tmp/brigade-email)
#   GITHUB_REPOSITORY         owner/name (Actions sets it)
#   GITHUB_API_URL            Actions sets it; https://api.github.com when unset
#   GITHUB_SERVER_URL         Actions sets it; https://github.com when unset
#   GITHUB_RUN_ID             this run (Actions sets it); unset, the window's upper bound is now
#   GH_TOKEN                  the job token. It reaches curl through a 0600 header file and never on argv --
#                             a runner's `ps` and the Actions debug log both see argv (CLAUDE.md)
#   BRIGADE_EMAIL_FROM        window: the first tag to cover, optional
#   BRIGADE_EMAIL_CHANGELOG   window: the changelog to read; CHANGELOG.md of the working directory when unset
#   BRIGADE_EMAIL_RECIPIENTS  recipients: the list -- addresses separated by commas, blanks or line ends, `#`
#                             starts a comment. A SECRET of the workflow, never a variable and never a
#                             file of the repository: a variable is printed unmasked, and the repository
#                             is public
#
# Output: every line goes to stdout, the annotations included, so that GitHub renders them on the run page.
# No address is ever printed except on its own `::add-mask::` line, which the runner consumes.
set -eu

# 0600 for the header file and recipients.txt, 0700 for the directories, set before anything is created.
umask 077

say()  { printf 'send-release-notes: %s\n' "$1"; }
note() { printf '::notice::send-release-notes: %s\n' "$1"; }
warn() { printf '::warning::send-release-notes: %s\n' "$1"; }
die()  { printf '::error::send-release-notes: %s\n' "$1"; exit 1; }

# The name of the workflow's step that sends to the recipients, and the workflow's file name. `window` finds
# the last email by this step's conclusion, so the two are joined to send-release-notes.yml by
# send_release_notes_test.go: renaming the step there without renaming it here would make every run believe
# no email was ever sent.
send_step='Send to the recipients'
workflow_file='send-release-notes.yml'

# Seven days, in seconds: the window when no earlier email is found. The workflow's schedule is weekly.
default_window=604800

# ---- configuration --------------------------------------------------------------------------------------
mode=${1:-}
case $mode in
  window|recipients|finish) ;;
  *) echo 'usage: scripts/ci/send-release-notes.sh window|recipients|finish' >&2; exit 2 ;;
esac

dir=${BRIGADE_EMAIL_DIR:-}
repo=${GITHUB_REPOSITORY:-}
api=${GITHUB_API_URL:-https://api.github.com}
server=${GITHUB_SERVER_URL:-https://github.com}
token=${GH_TOKEN:-}
addresses=${BRIGADE_EMAIL_RECIPIENTS:-}
unset GH_TOKEN BRIGADE_EMAIL_RECIPIENTS  # curl and jq are spawned below; neither needs them in its environment

[ -n "$dir" ] || die 'BRIGADE_EMAIL_DIR is not set: the working directory is required'
[ -n "$repo" ] || die 'GITHUB_REPOSITORY is not set: the repository (owner/name) is required'
printf '%s\n' "$repo" | grep -E -q -x '[A-Za-z0-9._-]+/[A-Za-z0-9._-]+' || die "GITHUB_REPOSITORY is not an owner/name: $repo"

api=${api%/}
server=${server%/}
# https only: the job token rides on every request. Loopback is the exception this script's own tests run on.
case $api in
  https://*) ;;
  http://127.0.0.1|http://127.0.0.1:*|http://localhost|http://localhost:*) ;;
  *) die "GITHUB_API_URL must use https (http is allowed only for 127.0.0.1/localhost): $api" ;;
esac

for tool in curl jq awk; do
  command -v "$tool" >/dev/null 2>&1 || die "$tool is not installed, and this script needs it (the Ubuntu runners preinstall all three)"
done

mkdir -p "$dir"
tmp=$(mktemp -d) || die 'cannot create a temporary directory'
trap 'rm -rf "$tmp"' EXIT HUP INT TERM
body=$tmp/response.json                  # every response body lands here and is read by jq, never printed
hdr=$tmp/headers                         # 0600: the job token, when there is one
{
  printf 'Accept: application/vnd.github+json\n'
  printf 'X-GitHub-Api-Version: 2022-11-28\n'
  if [ -n "$token" ]; then printf 'Authorization: Bearer %s\n' "$token"; fi
} > "$hdr"
unset token                              # from here on the token exists only in the 0600 header file

# output <name> <value> -> a step output, when the script runs as a workflow step.
output() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"; fi
}

# api_get <path and query> [<output file>] -> the response body in the file ($body by default), or the end
# of the run. GitHub's `message` is the only field quoted back: it carries no credential.
api_get() {
  out=${2:-$body}
  status=$(curl -sS --max-time 30 --retry 3 --retry-delay 5 -o "$out" -w '%{http_code}' -H @"$hdr" "$api$1") || true
  if [ "$status" != 200 ]; then
    message=$(jq -r '.message // empty' < "$out" 2>/dev/null) || message=''
    die "GET ${1%%\?*} answered HTTP $status: $message"
  fi
}

# api_pages <path> <output file> -> every page of a list endpoint, as one JSON array. Sixty pages of a hundred
# is a bound, not a target: a repository with more than 6,000 of anything needs more than this script (the
# job token's rate limit is 1,000 requests an hour).
api_pages() {
  : > "$tmp/pages"
  page=1
  while [ "$page" -le 60 ]; do
    api_get "$1?per_page=100&page=$page"
    length=$(jq 'length' < "$body")
    cat "$body" >> "$tmp/pages"
    if [ "$length" -lt 100 ]; then break; fi
    page=$((page + 1))
  done
  jq -s 'add // []' < "$tmp/pages" > "$2"
}

# ---- window ---------------------------------------------------------------------------------------------
# last_send -> sets `lower` to the `created_at` of the newest successful run whose send step succeeded, or
# leaves it empty. It sets a variable rather than printing: inside `$( )` a refusal of the API would end the
# subshell only, and its message would become the value.
last_send() {
  lower=''
  api_get "/repos/$repo/actions/workflows/$workflow_file/runs?status=success&per_page=30"
  jq -r '.workflow_runs // [] | sort_by(.created_at) | reverse | .[] | "\(.id) \(.created_at)"' < "$body" > "$tmp/runs"
  while read -r id created; do
    if [ "$id" = "${GITHUB_RUN_ID:-}" ]; then continue; fi
    api_get "/repos/$repo/actions/runs/$id/jobs?per_page=100" "$tmp/jobs.json"
    sent=$(jq --arg step "$send_step" '[.jobs[]?.steps[]? | select(.name == $step and .conclusion == "success")] | length' < "$tmp/jobs.json")
    if [ "$sent" -gt 0 ]; then
      lower=$created
      return 0
    fi
  done < "$tmp/runs"
}

window() {
  changelog=${BRIGADE_EMAIL_CHANGELOG:-CHANGELOG.md}
  [ -f "$changelog" ] || die "$changelog does not exist: run this from the checkout, or set BRIGADE_EMAIL_CHANGELOG"

  # Published, final releases only, oldest first. The tag's shape is checked because a tag becomes a file
  # name below; a pre-release carries a suffix and is not announced by email.
  api_pages "/repos/$repo/releases" "$tmp/all.json"
  jq 'map(select(.draft == false and .prerelease == false and .published_at != null))
      | map(select(.tag_name | test("^v[0-9]+\\.[0-9]+\\.[0-9]+$")))
      | sort_by(.published_at)' < "$tmp/all.json" > "$tmp/releases.json"

  # The upper bound is this run's own start, which is also what the NEXT run reads as its lower bound: a
  # release published while this run is composing belongs to the next email, and to that one only.
  upper=''
  if [ -n "${GITHUB_RUN_ID:-}" ]; then
    api_get "/repos/$repo/actions/runs/$GITHUB_RUN_ID"
    upper=$(jq -r '.created_at // empty' < "$body")
  fi
  [ -n "$upper" ] || upper=$(jq -rn 'now | todate')

  from=${BRIGADE_EMAIL_FROM:-}
  if [ -n "$from" ]; then
    case $from in v*) ;; *) from="v$from" ;; esac
    lower=$(jq -r --arg tag "$from" '.[] | select(.tag_name == $tag) | .published_at' < "$tmp/releases.json")
    [ -n "$lower" ] || die "$from is not a published release of $repo"
    inclusive=true
    say "the window starts with $from, as asked"
  else
    last_send
    inclusive=false
    if [ -n "$lower" ]; then
      say "the last email was sent by the run created at $lower"
    else
      lower=$(jq -rn --argjson seconds "$default_window" 'now - $seconds | todate')
      say "no earlier email was found: the window is the last seven days, from $lower"
    fi
  fi

  # Timestamps are GitHub's, all `YYYY-MM-DDTHH:MM:SSZ`, so comparing them as strings orders them in time.
  jq --arg lower "$lower" --arg upper "$upper" --argjson inclusive "$inclusive" \
    'map(select((if $inclusive then .published_at >= $lower else .published_at > $lower end) and .published_at <= $upper))' \
    < "$tmp/releases.json" > "$tmp/window.json"

  rm -rf "$dir/sources"
  mkdir -p "$dir/sources"
  jq -r '.[].tag_name' < "$tmp/window.json" > "$dir/window.txt"
  : > "$dir/sources/changelog.md"
  while read -r tag; do
    jq -r --arg tag "$tag" '.[] | select(.tag_name == $tag) | .body // ""' < "$tmp/window.json" > "$dir/sources/$tag.md"
    awk -v version="${tag#v}" '
      index($0, "## [" version "]") == 1 { on = 1; print; next }
      /^## \[/ { on = 0 }
      on { print }
    ' "$changelog" >> "$dir/sources/changelog.md"
  done < "$dir/window.txt"

  count=$(jq 'length' < "$tmp/window.json")
  output count "$count"
  if [ "$count" -eq 0 ]; then
    say 'no release was published in the window: there is nothing to send'
    note 'no release was published since the last email; nothing to send'
    return 0
  fi
  first=$(jq -r '.[0].tag_name' < "$tmp/window.json")
  last=$(jq -r '.[-1].tag_name' < "$tmp/window.json")
  if [ "$count" -eq 1 ]; then
    subject="Brigade ${last#v}: what's new"
  else
    subject="Brigade ${first#v} to ${last#v}: what's new"
  fi
  output first "$first"
  output last "$last"
  output versions "$(jq -r '[.[].tag_name | ltrimstr("v")] | join(" ")' < "$tmp/window.json")"
  output subject "$subject"
  say "$count release(s), $first to $last; the subject is \"$subject\""
}

# ---- recipients -----------------------------------------------------------------------------------------
# sendable <address> -> true for one plain address. The result becomes a comma-separated input of the mail
# step, so anything but one address -- a display name, a second address glued on with a semicolon -- is
# refused rather than cleaned. GitHub's own no-reply addresses accept no mail.
sendable() {
  case $1 in
    *..*|*@users.noreply.github.com) return 1 ;;
  esac
  printf '%s\n' "$1" | grep -E -q -x '[A-Za-z0-9._%+-]+@([A-Za-z0-9-]+\.)+[A-Za-z]{2,}'
}

recipients() {
  list=$dir/recipients.txt
  : > "$list"

  # One entry a line: comments off, then split at commas and blanks.
  printf '%s\n' "$addresses" | awk '{
    sub(/#.*$/, "")
    n = split($0, entry, /[,[:blank:]]+/)
    for (i = 1; i <= n; i++) if (entry[i] != "") print entry[i]
  }' > "$tmp/entries"

  entries=0 refused=0 repeated=0
  while read -r address; do
    entries=$((entries + 1))
    # Masked before it is judged: an entry that is refused is still somebody's address, or most of one.
    case $address in *@*) printf '::add-mask::%s\n' "$address" ;; esac
    if ! sendable "$address"; then
      refused=$((refused + 1))
      warn "entry $entries of the list is not one plain address; not used"
      continue
    fi
    address=$(printf '%s' "$address" | tr '[:upper:]' '[:lower:]')
    printf '::add-mask::%s\n' "$address"
    if grep -q -x -F "$address" "$list"; then
      repeated=$((repeated + 1))
      continue
    fi
    printf '%s\n' "$address" >> "$list"
  done < "$tmp/entries"

  count=$(awk 'END { print NR }' "$list")
  output count "$count"
  output bcc "$(paste -s -d , "$list")"
  say "the list has $entries entries; $count will receive the email"
  if [ "$repeated" -gt 0 ]; then say "$repeated repeat an address above and are counted once"; fi
  if [ "$refused" -gt 0 ]; then say "$refused were refused (above)"; fi
  if [ "$count" -eq 0 ]; then
    note 'the list of recipients is empty (the secret RELEASE_NOTES_RECIPIENTS); nothing to send'
  fi
}

# ---- finish ---------------------------------------------------------------------------------------------
finish() {
  notes=$dir/notes.md
  [ -s "$notes" ] || die "there is no draft at $notes"
  {
    cat "$notes"
    printf '\n\n---\n\n'
    printf 'Every release and its notes: %s/%s/releases\n\n' "$server" "$repo"
    printf 'You are receiving this because you are on the list for the release notes of [%s](%s/%s). To stop these emails, reply to this one and say so.\n' "$repo" "$server" "$repo"
  } > "$dir/email.md"
  say "the email is $(awk 'END { print NR }' "$dir/email.md") lines, footer included"
}

case $mode in
  window)     window ;;
  recipients) recipients ;;
  finish)     finish ;;
esac
