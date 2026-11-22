#!/bin/sh
# usage: scripts/ci/send-release-notes.sh window       which releases the email covers, and its sources
#        scripts/ci/send-release-notes.sh adopt        the approved draft of an earlier run, in place of a new one
#        scripts/ci/send-release-notes.sh recipients   the addresses the email goes to
#        scripts/ci/send-release-notes.sh finish       the approved draft with the footer under it
#
# The four deterministic steps of .github/workflows/send-release-notes.yml (card 43). The email's text is
# composed by an agent and gated by release-notes-lint.sh and a reviewer; WHICH releases it covers, WHO
# receives it and WHAT it says about why they received it are decided here, by the shell.
#
#   window       The releases published after the last email and up to this run's start. "The last email" is
#                the newest successful run of the workflow in which the step named by `send_step` below
#                concluded `success` -- a preview, a run with nothing to send and a run that failed all leave
#                the mark where it was, so the next run covers what they did not. No such run: the last seven
#                days, counted back from this run's start. The same when a run of the workflow is missing
#                from the history between that run and this one: since 2026-10-01 GitHub removes a run when
#                its repository's retention period ends (for this one 90 days from 2026-11-22 on, seven
#                before; work item 401), a run that is gone may have sent a later email, and the newest mark
#                that is left would mail everything since an older one again. That mark is still an email
#                that was sent: when it is newer than the seven days, the window starts at it instead.
#                BRIGADE_EMAIL_FROM (the workflow's `from` input, a tag) overrides all of it and is
#                INCLUSIVE: `v0.5.0` means the email starts with 0.5.0. It writes window.txt (one tag a line,
#                oldest first), sources/<tag>.md (that release's published notes, which passed the
#                release-notes gate) and sources/changelog.md (CHANGELOG.md's sections for those versions).
#   adopt        Takes the draft an earlier run wrote and its gate approved, so that what was previewed is
#                what is sent: every run writes a new draft, and the first send (run 36636440249) was
#                refused by the gate after a preview of the same releases had been approved and read. It
#                refuses unless the run is a successful run of this workflow on the default branch --
#                a run only succeeds when its gate approved -- its review says APPROVED, and its window is
#                this run's window, release for release: a release published since belongs in the email,
#                and the old draft does not have it. The workflow downloads that run's artifact into
#                BRIGADE_EMAIL_DRAFT_DIR first; this copies notes.md and review.md from it. The lint runs
#                on the adopted draft as on any other.
#   recipients   The addresses of BRIGADE_EMAIL_RECIPIENTS, a list the maintainers keep by hand (owner,
#                2026-09-29: GitHub gives out the address of almost nobody who starred, so the list is fixed
#                for now and collecting addresses is a later card). Each entry is held to one plain address,
#                lower-cased and counted once. It writes recipients.txt (0600) and the `bcc` output, and
#                registers every address as a mask BEFORE anything else can print it: this repository is
#                public and so are its run logs. It makes no request.
#   finish       Writes email.md: notes.md, then a footer that says where the release notes are, why the
#                reader received this and how to stop it -- by answering the email, which is why the
#                sender's address is an inbox someone reads. The footer is the shell's and not the agent's
#                so that no draft can leave it out. Then renders email.html from it with the renderer
#                BRIGADE_EMAIL_RENDERER names (cmd/brigade-release-email, card 73): the layout an inbox
#                shows -- the card, the masthead with the plugin icon, the version chips, the terminal
#                cards -- with every image pinned to GITHUB_SHA so that an email never changes under its
#                reader. The mail step sends email.html as the HTML part and email.md as the text part.
#
# Environment:
#   BRIGADE_EMAIL_DIR         the working directory, outside the checkout (the workflow: /tmp/brigade-email)
#   GITHUB_REPOSITORY         owner/name (Actions sets it)
#   GITHUB_API_URL            Actions sets it; https://api.github.com when unset
#   GITHUB_SERVER_URL         Actions sets it; https://github.com when unset
#   GITHUB_RUN_ID             this run (Actions sets it); unset, the window's upper bound is now
#   GITHUB_RUN_NUMBER         this run's number among the workflow's (Actions sets it): the last email's mark
#                             is believed only when every run between it and this number is in the history;
#                             unset, as by hand, what the history has is taken as it is
#   GH_TOKEN                  the job token. It reaches curl through a 0600 header file and never on argv --
#                             a runner's `ps` and the Actions debug log both see argv (CLAUDE.md)
#   BRIGADE_EMAIL_FROM        window: the first tag to cover, optional
#   BRIGADE_EMAIL_CHANGELOG   window: the changelog to read; CHANGELOG.md of the working directory when unset
#   BRIGADE_EMAIL_DRAFT_RUN   adopt: the id of the earlier run (the workflow's `draft_run` input)
#   BRIGADE_EMAIL_DRAFT_DIR   adopt: where the workflow downloaded that run's artifact
#   BRIGADE_EMAIL_RECIPIENTS  recipients: the list -- addresses separated by commas, blanks or line ends, `#`
#                             starts a comment. A SECRET of the workflow, never a variable and never a
#                             file of the repository: a variable is printed unmasked, and the repository
#                             is public
#   BRIGADE_EMAIL_RENDERER    finish: the path of the built renderer (`go build ./cmd/brigade-release-email`)
#   GITHUB_SHA                finish: the commit the email's images are pinned to (Actions sets it); `master`
#                             when unset
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

# Seven days, in seconds: the window when no earlier email is found, counted back from this run's start. The
# workflow's schedule is weekly.
default_window=604800

# ---- configuration --------------------------------------------------------------------------------------
mode=${1:-}
case $mode in
  window|adopt|recipients|finish) ;;
  *) echo 'usage: scripts/ci/send-release-notes.sh window|adopt|recipients|finish' >&2; exit 2 ;;
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

# api_try <path and query> <output file> -> as api_get, but a 404 is an answer and not the end of the run:
# `status` says which. A run that the history listed a moment ago can be gone when its jobs are asked for.
api_try() {
  status=$(curl -sS --max-time 30 --retry 3 --retry-delay 5 -o "$2" -w '%{http_code}' -H @"$hdr" "$api$1") || true
  case $status in
    200 | 404) ;;
    *)
      message=$(jq -r '.message // empty' < "$2" 2>/dev/null) || message=''
      die "GET ${1%%\?*} answered HTTP $status: $message"
      ;;
  esac
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
#
# The run history is not a record that lasts (work item 401): GitHub removes a run, with its checks, when the
# retention period of its repository ends. For this one that was seven days until 2026-11-22, and the weekly
# run that sent the last email was seven days old when the next one looked for it; it is 90 days for a run
# made since. When a sending run is gone, the newest one still listed is an older email, and a
# window from there mails the list again everything the runs that are gone had sent. So a mark is believed as
# the last email only when the history accounts for every run after it:
#
#   - every number between the mark's and this run's is in the history, and up to the highest number the
#     history has when that is higher, as it is for a run that is run again;
#   - no successful run newer than the mark is listed without any step, and none went out of the history
#     between being listed and being asked for its jobs: what such a run did is not known. (A run that has
#     its steps and none of the send step's name sent nothing this script knows of, as it always read.)
#
# Otherwise `doubt` says what is not accounted for, `lower` is left empty, and `stale` is the newest mark
# that is left, when one is: everything up to it was sent, whatever came after. With no mark left at all, the
# numbers are counted from the workflow's first run: a history with a run missing may have lost the email
# too, and `doubt` says so. Outside a workflow run there is no number to count from, and what is found is
# taken as it is.
last_send() {
  lower=''
  stale=''
  doubt=''
  mark=''
  api_get "/repos/$repo/actions/workflows/$workflow_file/runs?status=success&per_page=30"
  jq -r '.workflow_runs // [] | sort_by(.created_at) | reverse | .[] | "\(.id) \(.created_at) \(.run_number // "")"' < "$body" > "$tmp/runs"
  while read -r id created number; do
    if [ "$id" = "${GITHUB_RUN_ID:-}" ]; then continue; fi
    api_try "/repos/$repo/actions/runs/$id/jobs?per_page=100" "$tmp/jobs.json"
    if [ "$status" = 404 ]; then
      [ -n "$doubt" ] || doubt="run ${number:-$id} of this workflow went out of the run history while it was being read"
      continue
    fi
    sent=$(jq --arg step "$send_step" '[.jobs[]?.steps[]? | select(.name == $step and .conclusion == "success")] | length' < "$tmp/jobs.json")
    if [ "$sent" -gt 0 ]; then
      # Under a doubt the reading went on only to find this: the newest email that is left, not believed.
      if [ -n "$doubt" ]; then
        stale=$created
        return 0
      fi
      lower=$created
      mark=$number
      break
    fi
    steps=$(jq '[.jobs[]?.steps[]?] | length' < "$tmp/jobs.json")
    if [ "$steps" -eq 0 ] && [ -z "$doubt" ]; then
      doubt="run ${number:-$id} of this workflow is in the run history without its steps"
    fi
  done < "$tmp/runs"
  if [ -n "$doubt" ]; then
    # A mark that was left would have ended the reading above.
    doubt="$doubt, and no run that is left sent an email"
    return 0
  fi
  this=${GITHUB_RUN_NUMBER:-}
  if [ -z "$this" ]; then return 0; fi
  # A mark without a number is no run of GitHub's: it is taken as it is.
  if [ -n "$lower" ] && [ -z "$mark" ]; then return 0; fi
  case "${mark:-0}$this" in *[!0-9]*) return 0 ;; esac
  begin=$((${mark:-0} + 1))
  # Every run of the workflow, whatever it concluded: the hundred newest.
  api_get "/repos/$repo/actions/workflows/$workflow_file/runs?per_page=100"
  held=$(jq -r '[.workflow_runs[]?.run_number | tostring] | join(" ")' < "$body")
  highest=$(jq -r '[.workflow_runs[]?.run_number] | max // 0' < "$body")
  lowest=$(jq -r '[.workflow_runs[]?.run_number] | min // 0' < "$body")
  listed=$(jq -r '.workflow_runs // [] | length' < "$body")
  if [ "$highest" -gt "$this" ]; then this=$highest; fi
  if [ "$listed" -ge 100 ] && [ "$lowest" -gt "$begin" ]; then
    doubt="the run history is longer than this check reads, a hundred runs, and does not reach back to run $begin"
  fi
  number=$begin
  while [ -z "$doubt" ] && [ "$number" -lt "$this" ]; do
    case " $held " in
      *" $number "*) ;;
      *) doubt="run $number of this workflow is not in the run history" ;;
    esac
    number=$((number + 1))
  done
  if [ -n "$doubt" ]; then
    if [ -z "$lower" ]; then doubt="$doubt, and no run that is left sent an email"; fi
    stale=$lower
    lower=''
  fi
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
      # Counted back from this run's own start, the upper bound, and not from the clock: a run that waited
      # in the workflow's queue would otherwise leave out what was published in the time it waited.
      lower=$(jq -rn --arg upper "$upper" --argjson seconds "$default_window" '($upper | fromdate) - $seconds | todate') \
        || die "this run's start, $upper, is not a time in GitHub's form"
      if [ -z "$doubt" ]; then
        say "no earlier email was found: the window is the last seven days, from $lower"
      else
        # GitHub keeps a run for the repository's retention period only, so a run that is not accounted for
        # may have sent a later email than any that is left. What the newest mark left had sent is still
        # sent: when it is inside the seven days, the window starts there and not before it.
        kept="the window is the last seven days, from $lower"
        if [ -n "$stale" ]; then
          later=$(jq -rn --arg stale "$stale" --arg floor "$lower" 'if $stale > $floor then $stale else $floor end')
          if [ "$later" != "$lower" ]; then
            lower=$later
            kept="the window starts at that email, which is inside the last seven days"
          fi
          doubt="$doubt, and the newest email that is left, of $stale, is not believed to be the last"
        fi
        warn "$doubt: GitHub keeps a run for the repository's retention period only, and a run that is not accounted for may have sent an email since. So $kept. Run the workflow with its 'from' input to name the first release yourself"
      fi
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
    if [ -n "${doubt:-}" ]; then
      note "no release was published since $lower, where this run's window starts; nothing to send"
    else
      note 'no release was published since the last email; nothing to send'
    fi
    return 0
  fi
  first=$(jq -r '.[0].tag_name' < "$tmp/window.json")
  last=$(jq -r '.[-1].tag_name' < "$tmp/window.json")
  # The subject is the app and the highest release the email covers, the shape Cavalry's email uses too (card
  # 189): highest by version, not newest by publish time, so a fix of an older line published later is not it.
  latest=$(jq -r 'map(.tag_name | ltrimstr("v")) | sort_by(split(".") | map(tonumber)) | .[-1]' < "$tmp/window.json")
  subject="Brigade $latest"
  output first "$first"
  output last "$last"
  output versions "$(jq -r '[.[].tag_name | ltrimstr("v")] | join(" ")' < "$tmp/window.json")"
  output subject "$subject"
  say "$count release(s), $first to $last; the subject is \"$subject\""
}

# ---- adopt ----------------------------------------------------------------------------------------------
adopt() {
  run=${BRIGADE_EMAIL_DRAFT_RUN:-}
  from_dir=${BRIGADE_EMAIL_DRAFT_DIR:-}
  printf '%s\n' "$run" | grep -E -q -x '[0-9]+' || die 'BRIGADE_EMAIL_DRAFT_RUN is not the id of a run: digits only'
  [ -n "$from_dir" ] || die 'BRIGADE_EMAIL_DRAFT_DIR is not set: where that run'"'"'s artifact was downloaded'
  [ "$run" != "${GITHUB_RUN_ID:-}" ] || die 'a run cannot adopt its own draft'
  [ -f "$dir/window.txt" ] || die "there is no window at $dir/window.txt: run window first"

  api_get "/repos/$repo"
  branch=$(jq -r '.default_branch // empty' < "$body")
  api_get "/repos/$repo/actions/runs/$run"
  run_path=$(jq -r '.path // empty' < "$body")
  run_conclusion=$(jq -r '.conclusion // empty' < "$body")
  run_branch=$(jq -r '.head_branch // empty' < "$body")
  # The path can carry `@<ref>` after the file name.
  case $run_path in
    ".github/workflows/$workflow_file"|".github/workflows/$workflow_file@"*) ;;
    *) die "run $run is not a run of $workflow_file" ;;
  esac
  [ "$run_conclusion" = success ] || die "run $run did not succeed ($run_conclusion): its gate approved no draft"
  if [ -z "$branch" ] || [ "$run_branch" != "$branch" ]; then
    die "run $run ran on $run_branch, not on the default branch"
  fi

  [ -s "$from_dir/notes.md" ] || die "run $run left no draft: notes.md is missing from its artifact, or empty"
  [ -f "$from_dir/review.md" ] || die "run $run left no review"
  [ "$(head -n 1 "$from_dir/review.md")" = APPROVED ] || die "the review of run $run does not say APPROVED"
  [ -f "$from_dir/window.txt" ] || die "run $run left no window"
  if ! cmp -s "$from_dir/window.txt" "$dir/window.txt"; then
    die "the draft of run $run covers $(awk 'END { print NR }' "$from_dir/window.txt") release(s) and this email covers $(awk 'END { print NR }' "$dir/window.txt"), or not the same ones: write a new draft"
  fi

  cp "$from_dir/notes.md" "$dir/notes.md"
  cp "$from_dir/review.md" "$dir/review.md"
  say "the draft of run $run is adopted: approved there, $(awk 'END { print NR }' "$dir/window.txt") release(s), the same as this email covers"
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
  renderer=${BRIGADE_EMAIL_RENDERER:-}
  [ -s "$notes" ] || die "there is no draft at $notes"
  [ -n "$renderer" ] || die 'BRIGADE_EMAIL_RENDERER is not set: the path of the email renderer (go build ./cmd/brigade-release-email) is required'
  [ -x "$renderer" ] || die "the renderer $renderer does not exist or is not executable"
  [ -f "$dir/window.txt" ] || die "there is no window at $dir/window.txt: run window first"
  {
    cat "$notes"
    printf '\n\n---\n\n'
    printf 'Every release and its notes: %s/%s/releases\n\n' "$server" "$repo"
    printf 'You are receiving this because you are on the list for the release notes of [%s](%s/%s). To stop these emails, reply to this one and say so.\n' "$repo" "$server" "$repo"
  } > "$dir/email.md"
  # The HTML part. The renderer refuses what the writer's shape forbids -- the lint ran it on the draft
  # already, so a refusal here is the footer's or the renderer's own, and nothing is sent.
  "$renderer" -in "$dir/email.md" -window "$dir/window.txt" -out "$dir/email.html" \
    -repo "$repo" -server "$server" -commit "${GITHUB_SHA:-master}" -date "$(date -u +%Y-%m-%d)" \
    || die 'the email did not render as HTML (above); nothing is sent'
  say "the email is $(awk 'END { print NR }' "$dir/email.md") lines, footer included; the HTML is $(wc -c < "$dir/email.html" | tr -d ' ') bytes"
}

case $mode in
  window)     window ;;
  adopt)      adopt ;;
  recipients) recipients ;;
  finish)     finish ;;
esac
