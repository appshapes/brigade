#!/bin/sh
# The shell steps of a scheduled job: .github/workflows/update-documentation.yml, update-dependencies.yml and
# review-repository.yml (work item 433, ADR 0032). Each of the three is one workflow whose steps are the
# personas in turn: a writer, this script's gate, a reviewer, one fix cycle, the gate again, a second review,
# and a verdict. What the model writes is judged here and carried from here, never by the model itself.
#
# Three jobs call it, and what each may do is the job's, not this file's:
#   sweep   in the job `sweep` (a token that can delete a branch, no agent): removes the branches earlier runs
#           of this job left whose pull request is merged or closed, and says whether one is still open. A run
#           does nothing while one is.
#   gate, verdict, decide, scan, bundle
#           in the job `work`, beside the agents, whose token cannot write to the repository. The workflow
#           runs the copy of the commit the run started on (`git show "$GITHUB_SHA:…"`), never the working
#           tree's, which an agent can edit; the two scripts this one calls it reads the same way.
#   land, open
#           in the job `land` (no agent). `land` holds the job's own token and pushes the bundle's commits as
#           a branch; `open` holds GH_ACTIONS_TOKEN and opens the pull request. Neither runs anything that
#           came out of the bundle: the checkout stays at the commit the run started on, and the bundle is
#           fetched into a ref and pushed.
#
# The verbs:
#   sweep     reads the branches `scheduled/<job>-<date>` and each one's pull request; output `open_pr`.
#   gate      what the checkout holds (agent-leftover.sh), pr-guard.sh, the paths no scheduled job changes,
#             then each `make` target of the job. Findings go to gate.txt as `FAIL:` lines, for the reviewer
#             and the fixer to read; outputs `changed` (true when there is a commit or a finding), `head` and
#             `state`. It exits 1 on a finding.
#   verdict   reads review.md's first line; outputs `verdict` and, in round 1, `fix`. It fails when the
#             reviewer's session changed the checkout.
#   decide    the run's outcome from the steps' outcomes; output `land`. It exits 1 when the change was not
#             approved or the writer died with nothing written.
#   scan      looks for a token in everything that leaves the job: the commits and every file of the job's
#             directory. The tokens the job held are compared whole; the shapes of a model token and a GitHub
#             token are looked for in every line. It names the place and never prints the line.
#   bundle    writes the commits as change.bundle.
#   land      verifies the bundle against the commit the run started on, holds it to pr-guard.sh and the same
#             paths again, and pushes it as `scheduled/<job>-<date>`; outputs `branch` and `head`.
#   open      opens the pull request `<standing work item>: <title>` with the writer's report as its body and
#             the label `maintenance`; with SCHEDULED_MERGE=when-green it arms the merge.
#
# Environment: SCHEDULED_JOB (the workflow's file name without `.yml`), STANDING_WORK_ITEM (the number of the
# job's standing work item, digits), SCHEDULED_TITLE, SCHEDULED_GATE (the `make` targets, space-separated),
# SCHEDULED_MERGE (`when-green` or `by-hand`), SCHEDULED_DIR (the job's directory outside the checkout),
# GITHUB_SHA (the commit the run started on), GITHUB_REF (`refs/heads/master`, or sweep, land and open refuse),
# and per verb what its section names. It needs `git`, and `gh`
# and `jq` for sweep and open.
set -eu

verb=${1:-}

usage() {
  echo "scheduled-job: usage: scheduled-job.sh sweep | gate | verdict | decide | scan | bundle | land | open" >&2
  exit 2
}

: "${SCHEDULED_JOB:?scheduled-job: SCHEDULED_JOB is not set}"
case $SCHEDULED_JOB in
  *[!a-z-]*|-*|*-) echo "scheduled-job: SCHEDULED_JOB is not a job's name: small letters and hyphens" >&2; exit 2 ;;
esac

# The token shapes looked for by `scan` and `land`: a model credential (`sk-ant-…`) and GitHub's (`ghp_`,
# `gho_`, `ghu_`, `ghs_`, `ghr_`, `github_pat_`). They are the two kinds a job of these workflows holds.
token_shapes='sk-ant-[A-Za-z0-9_-]{20,}|gh[pousr]_[A-Za-z0-9]{30,}|github_pat_[A-Za-z0-9_]{30,}'

# The paths no scheduled job changes: an agent's tooling denies it the writes, and a workflow file could not
# be pushed by the job's token in any case.
forbidden_paths='^(\.github/|\.claude/)'

out() {
  if [ -n "${GITHUB_OUTPUT:-}" ]; then
    printf '%s=%s\n' "$1" "$2" >> "$GITHUB_OUTPUT"
  fi
}

need_number() {
  case ${STANDING_WORK_ITEM:-} in
    ''|*[!0-9]*)
      echo "::error::STANDING_WORK_ITEM is not a work item's number: the workflow of $SCHEDULED_JOB names its standing work item in digits, and a pull request is titled with it"
      exit 1 ;;
  esac
}

# A scheduled job works on `master` and its pull request is opened against `master`. A run started by hand on
# another branch would carry that branch's commits into the pull request under the job's title, read by no
# reviewer: the reviewer of the run reads the writer's commits alone.
need_master() {
  if [ "${GITHUB_REF:-}" != refs/heads/master ]; then
    echo "::error::a scheduled job runs on master and nowhere else (this run's ref is ${GITHUB_REF:-not set}): its pull request is opened against master and would carry the other branch's commits"
    exit 1
  fi
}

need_dir() {
  : "${SCHEDULED_DIR:?scheduled-job: SCHEDULED_DIR is not set}"
  mkdir -p "$SCHEDULED_DIR"
}

need_base() {
  : "${GITHUB_SHA:?scheduled-job: GITHUB_SHA is not set}"
  base=$GITHUB_SHA
}

# A directory of this run's own, outside the checkout and outside the job's directory, removed at exit.
scratch() {
  work=$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/scheduled-job.XXXXXX")
  trap 'rm -rf "$work"' EXIT HUP INT TERM
}

# A script of the commit the run started on, written into the scratch directory: `masters pr-guard.sh`.
masters() {
  git show "$base:scripts/ci/$1" > "$work/$1"
}

# What the checkout is, in two words: its head and a hash of its status. `verdict` compares the pair before
# and after a reviewer's session.
state() {
  printf '%s %s' "$(git rev-parse HEAD)" "$(git status --porcelain | git hash-object --stdin)"
}

# ---------------------------------------------------------------------------------------------------------
sweep() {
  need_number
  need_master
  : "${GITHUB_REPOSITORY:?scheduled-job: GITHUB_REPOSITORY is not set}"
  owner=${GITHUB_REPOSITORY%%/*}
  # Read into a variable first: in a pipeline a failed listing would be lost behind jq's success, and an
  # empty listing reads as "no branch left".
  refs=$(gh api --paginate "repos/$GITHUB_REPOSITORY/git/matching-refs/heads/scheduled/$SCHEDULED_JOB-")
  branches=$(printf '%s' "$refs" | jq -r -s 'add // [] | .[].ref | sub("^refs/heads/"; "")')
  open=""
  for branch in $branches; do
    case $branch in
      "scheduled/$SCHEDULED_JOB-"[0-9][0-9][0-9][0-9][0-9][0-9][0-9][0-9]) ;;
      *) echo "left alone: $branch is not a name this job gives a branch"; continue ;;
    esac
    pulls=$(gh api "repos/$GITHUB_REPOSITORY/pulls?state=all&head=$owner:$branch&per_page=100")
    opened=$(printf '%s' "$pulls" | jq -r '[.[] | select(.state == "open") | .number] | first // empty')
    if [ -n "$opened" ]; then
      open=$opened
      echo "pull request #$opened of $branch is still open"
      continue
    fi
    gh api -X DELETE "repos/$GITHUB_REPOSITORY/git/refs/heads/$branch" > /dev/null
    echo "removed $branch: its pull request is merged or closed, or it never had one"
  done
  out open_pr "$open"
  if [ -n "$open" ]; then
    echo "::notice::pull request #$open of an earlier run of $SCHEDULED_JOB is still open: this run does nothing. Merge or close it, then start the job again."
  fi
}

# ---------------------------------------------------------------------------------------------------------
gate() {
  need_dir
  need_base
  scratch
  report="$SCHEDULED_DIR/gate.txt"
  : > "$report"
  fails=0
  finding() {
    printf 'FAIL: %s\n' "$1" >> "$report"
    fails=$((fails + 1))
  }

  # 1. What the checkout holds.
  masters agent-leftover.sh
  GITHUB_OUTPUT="$work/leftover.out" sh "$work/agent-leftover.sh" > "$work/leftover.txt" 2>&1
  count() { sed -n "s/^$1=//p" "$work/leftover.out" | head -n 1; }
  changed=$(count changed)
  unpushed=$(count unpushed)
  stashed=$(count stashed)
  commits=$(git rev-list --count "$base..HEAD")
  if ! git merge-base --is-ancestor "$base" HEAD; then
    finding "HEAD no longer descends from the commit the run started on ($base): never rewrite history, commit on top of it"
  fi
  if [ "$changed" -gt 0 ] || [ "$stashed" -gt 0 ]; then
    finding "the checkout holds work that is not committed ($changed changed or untracked path(s), $stashed stash(es)): commit what belongs to the change and remove the rest. What it holds:"
    grep -v '^::' "$work/leftover.txt" | sed 's/^/    /' >> "$report"
  fi
  if [ "$unpushed" -gt "$commits" ]; then
    finding "a commit sits on a local branch that HEAD does not carry: only the commits of the branch the run started on leave this job"
  fi

  if [ "$commits" -eq 0 ] && [ "$fails" -eq 0 ]; then
    echo "nothing to change: no commit, and the checkout is as the run found it" | tee -a "$report"
    out changed false
    out head "$(git rev-parse HEAD)"
    out state "$(state)"
    return 0
  fi
  out changed true
  # Before the targets as well as after them: a gate that is ended at its limit has still said what the
  # checkout was, and the verdict has something to compare.
  out head "$(git rev-parse HEAD)"
  out state "$(state)"
  # The review that follows this gate is the reviewer's own or none: a file an earlier session left under
  # this name is no verdict.
  rm -f "$SCHEDULED_DIR/review.md"

  if [ "$commits" -gt 0 ]; then
    head=$(git rev-parse HEAD)
    # 2. The three invariants outside an agent's judgement, by the script of the commit the run started on.
    masters pr-guard.sh
    if ! sh "$work/pr-guard.sh" "$base" "$head" > "$work/guard.txt" 2>&1; then
      sed 's/^::error::/FAIL: /' "$work/guard.txt" >> "$report"
      fails=$((fails + 1))
    fi
    # 3. The paths no scheduled job changes.
    touched=$(git diff --name-only "$base...$head" | grep -E "$forbidden_paths" || true)
    if [ -n "$touched" ]; then
      finding "the change touches a path no scheduled job changes (.github/ and .claude/ are the owner's to push to master): $(printf '%s' "$touched" | tr '\n' ' ')"
    fi
    # 4. The job's own gate, one target at a time, so that a finding names its target and every target runs.
    for target in ${SCHEDULED_GATE:-}; do
      case $target in
        *[!a-z-]*|-*) echo "scheduled-job: SCHEDULED_GATE names $target, which is not a make target's name" >&2; exit 2 ;;
      esac
      echo "---- make $target" >> "$SCHEDULED_DIR/gate-make.log"
      status=0
      # shellcheck disable=SC2086  # SCHEDULED_MAKE_VARS is `name=value` words for make, split on purpose
      make "$target" ${SCHEDULED_MAKE_VARS:-} >> "$SCHEDULED_DIR/gate-make.log" 2>&1 || status=$?
      if [ "$status" -ne 0 ]; then
        finding "make $target failed (exit $status). The last lines of its output (all of it is in gate-make.log):"
        tail -n 40 "$SCHEDULED_DIR/gate-make.log" | sed 's/^/    /' >> "$report"
      else
        echo "ok: make $target" >> "$report"
      fi
    done
  fi

  out head "$(git rev-parse HEAD)"
  out state "$(state)"
  cat "$report"
  if [ "$fails" -gt 0 ]; then
    echo "::warning::the gate has $fails finding(s) for the fix cycle"
    return 1
  fi
}

# ---------------------------------------------------------------------------------------------------------
verdict() {
  need_dir
  round=${SCHEDULED_ROUND:?scheduled-job: SCHEDULED_ROUND is not set}
  expect=${SCHEDULED_EXPECT_STATE:?scheduled-job: SCHEDULED_EXPECT_STATE is not set}
  # A reviewer reads. One that left the checkout other than it found it has written what nobody reviewed.
  if [ "$(state)" != "$expect" ]; then
    echo "::error::the reviewer's session changed the checkout (it was \"$expect\" and is \"$(state)\"): nothing leaves this job"
    exit 1
  fi
  review="$SCHEDULED_DIR/review.md"
  verdict=""
  if [ -f "$review" ]; then
    # The whole first line, spaces and a carriage return apart: `NOT APPROVED` is not an approval.
    case $(head -n 1 "$review" | tr -d ' \t\r') in
      APPROVED) verdict=APPROVED ;;
      CHANGES_REQUESTED) verdict=CHANGES_REQUESTED ;;
    esac
  fi
  if [ -z "$verdict" ]; then
    echo "the reviewer wrote no verdict in round $round: that is not an approval" >&2
    printf 'CHANGES_REQUESTED\nThe reviewer wrote no verdict. Address every FAIL line of the gate, if any, and hold the change to its brief again.\n' > "$review"
    verdict=CHANGES_REQUESTED
  fi
  out verdict "$verdict"
  echo "round $round: gate ${SCHEDULED_GATE_OUTCOME:-not run}, review $verdict"
  cat "$review"
  if [ "$round" = 1 ]; then
    if [ "${SCHEDULED_GATE_OUTCOME:-}" = success ] && [ "$verdict" = APPROVED ]; then
      out fix false
    else
      out fix true
      # The fixer and the second reviewer read round 1 under these names. review.md is taken away, so that a
      # second reviewer who writes nothing has approved nothing.
      cp "$review" "$SCHEDULED_DIR/review-round1.md"
      cp "$SCHEDULED_DIR/gate.txt" "$SCHEDULED_DIR/gate-round1.txt" 2> /dev/null || true
      rm -f "$review"
    fi
  fi
}

# ---------------------------------------------------------------------------------------------------------
decide() {
  summary() {
    echo "$1"
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      echo "$1" >> "$GITHUB_STEP_SUMMARY"
    fi
  }
  if [ "${SCHEDULED_CHANGED:-}" != true ]; then
    if [ "${SCHEDULED_CHANGED:-}" = false ] && [ "${SCHEDULED_WRITER_OUTCOME:-}" = success ]; then
      summary "$SCHEDULED_JOB: nothing to change. The writer committed nothing and left the checkout as the run found it; no pull request is opened."
      out land false
      return 0
    fi
    summary "$SCHEDULED_JOB: the writer's session ended in '${SCHEDULED_WRITER_OUTCOME:-not run}' and the gate found '${SCHEDULED_CHANGED:-nothing it could read}': that is not a run with nothing to change."
    echo "::error::the writer's session failed or the gate never looked at the checkout; nothing was written and nothing landed"
    out land false
    exit 1
  fi
  case ${SCHEDULED_FIX_OUTCOME:-skipped} in
    skipped|'') round=1; gate=${SCHEDULED_GATE1:-}; verdict=${SCHEDULED_VERDICT1:-} ;;
    *)          round=2; gate=${SCHEDULED_GATE2:-}; verdict=${SCHEDULED_VERDICT2:-} ;;
  esac
  # The first gate found work that was never committed and no commit, and the fix cycle removed it: the
  # second gate found the checkout as the run found it. That is a run with nothing to change.
  if [ "$round" = 2 ] && [ "${SCHEDULED_CHANGED2:-}" = false ] && [ "$gate" = success ]; then
    summary "$SCHEDULED_JOB: nothing to change. The fix cycle left no commit and the checkout as the run found it; no pull request is opened."
    out land false
    return 0
  fi
  if [ "$gate" = success ] && [ "$verdict" = APPROVED ]; then
    summary "$SCHEDULED_JOB: approved after $round round(s); the change goes to a pull request."
    out land true
    return 0
  fi
  summary "$SCHEDULED_JOB: not approved after $round round(s) (gate ${gate:-not run}, review ${verdict:-none}). Nothing landed; the report, the gate's findings and the reviews are this run's artifact."
  echo "::error::the change was not approved after $round round(s); nothing left this job"
  out land false
  exit 1
}

# ---------------------------------------------------------------------------------------------------------
# The whole of a token that this job held, compared with a file: the value is read by grep from standard
# input, never from its arguments.
holds_value() {
  printf '%s\n' "$1" | grep -q -F -f - -- "$2"
}

# scan_file <file> <what to call it>: adds to `hits`. No file of this repository carries either shape (read
# on 2026-11-24), so every line is read, a commit's message and a patch's context with it.
scan_file() {
  for value in "${SCHEDULED_SECRET_MODEL:-}" "${SCHEDULED_SECRET_GITHUB:-}"; do
    # An empty or a short value would be found in every file.
    if [ "${#value}" -ge 20 ] && holds_value "$value" "$1"; then
      echo "::error::$2 carries, whole, a token this job held"
      hits=$((hits + 1))
    fi
  done
  lines=$(grep -n -E "$token_shapes" "$1" | cut -d : -f 1 | tr '\n' ' ' || true)
  if [ -n "$lines" ]; then
    echo "::error::$2 carries something of a token's shape at line(s) $lines"
    hits=$((hits + 1))
  fi
}

scan() {
  need_dir
  need_base
  scratch
  hits=0
  scanned=0
  if [ "$(git rev-list --count "$base..HEAD")" -gt 0 ]; then
    # Every commit that would be pushed, message and patch: the branch is public before the squash.
    git log -p --format=fuller "$base..HEAD" > "$work/commits.txt"
    scan_file "$work/commits.txt" "the commits"
    scanned=$((scanned + 1))
  fi
  # Every file under the job's directory, as the artifact takes it: the whole tree.
  find "$SCHEDULED_DIR" -type f > "$work/files.txt"
  while IFS= read -r file; do
    if [ ! -f "$file" ]; then
      # A name with a line break in it reads as two names, neither of them a file.
      echo "::error::a file under the job's directory has a name that cannot be read line by line"
      hits=$((hits + 1))
      continue
    fi
    scan_file "$file" "${file#"$SCHEDULED_DIR"/}"
    scanned=$((scanned + 1))
  done < "$work/files.txt"
  if [ "$hits" -gt 0 ]; then
    echo "::error::$hits place(s) above would have carried a token out of this job; nothing leaves it"
    exit 1
  fi
  echo "scan: $scanned place(s) read, no token in them"
}

# ---------------------------------------------------------------------------------------------------------
bundle() {
  need_dir
  need_base
  git bundle create "$SCHEDULED_DIR/change.bundle" "$base..HEAD"
  echo "bundle: $(git rev-list --count "$base..HEAD") commit(s) on $base"
}

# ---------------------------------------------------------------------------------------------------------
land() {
  need_master
  need_dir
  need_base
  scratch
  bundle="$SCHEDULED_DIR/change.bundle"
  refuse() {
    echo "::error::$1; nothing was pushed"
    exit 1
  }
  [ -f "$bundle" ] || refuse "the run's artifact has no change.bundle"
  git bundle verify "$bundle" > /dev/null 2>&1 || refuse "change.bundle is no bundle on a commit this checkout has"
  # Into a ref of its own and never into the working tree: the checkout stays at the commit the run started on.
  git -c fetch.fsckObjects=true fetch --quiet --no-tags "$bundle" "HEAD:refs/scheduled-job/incoming"
  tip=$(git rev-parse --verify 'refs/scheduled-job/incoming^{commit}')
  [ "$tip" != "$base" ] || refuse "the bundle holds no commit"
  git merge-base --is-ancestor "$base" "$tip" || refuse "the bundle's commits do not sit on the commit the run started on ($base)"
  masters pr-guard.sh
  sh "$work/pr-guard.sh" "$base" "$tip" || refuse "the change trips pr-guard.sh"
  touched=$(git diff --name-only "$base...$tip" | grep -E "$forbidden_paths" || true)
  [ -z "$touched" ] || refuse "the change touches a path no scheduled job changes: $(printf '%s' "$touched" | tr '\n' ' ')"
  # The shapes again, by this job, which trusts nothing the other one said of its own work.
  hits=0
  git log -p --format=fuller "$base..$tip" > "$work/commits.txt"
  scan_file "$work/commits.txt" "the commits"
  if [ -f "$SCHEDULED_DIR/report.md" ]; then
    scan_file "$SCHEDULED_DIR/report.md" "report.md"
  fi
  [ "$hits" -eq 0 ] || refuse "the change or its report carries something of a token's shape"
  branch="scheduled/$SCHEDULED_JOB-$(date -u +%Y%m%d)"
  git push --quiet origin "$tip:refs/heads/$branch"
  echo "pushed $(git rev-list --count "$base..$tip") commit(s) as $branch at $tip"
  out branch "$branch"
  out head "$tip"
}

# ---------------------------------------------------------------------------------------------------------
open() {
  need_number
  need_master
  need_dir
  scratch
  : "${GITHUB_REPOSITORY:?scheduled-job: GITHUB_REPOSITORY is not set}"
  : "${SCHEDULED_TITLE:?scheduled-job: SCHEDULED_TITLE is not set}"
  : "${SCHEDULED_BRANCH:?scheduled-job: SCHEDULED_BRANCH is not set}"
  : "${SCHEDULED_HEAD:?scheduled-job: SCHEDULED_HEAD is not set}"
  case ${SCHEDULED_MERGE:-} in
    when-green) merge="It merges by itself when the required checks of \`master\` are green." ;;
    by-hand)    merge="It waits for the owner's merge." ;;
    *) echo "scheduled-job: SCHEDULED_MERGE is neither when-green nor by-hand" >&2; exit 2 ;;
  esac
  # The body is the squash commit's message as well (the repository takes it from the pull request's body):
  # the writer's report, then where it came from.
  {
    if [ -s "$SCHEDULED_DIR/report.md" ]; then
      head -c 20000 "$SCHEDULED_DIR/report.md"
      echo
      echo
    fi
    echo "Scheduled job \`$SCHEDULED_JOB\`: written, gated and reviewed in one run${SCHEDULED_RUN_URL:+, $SCHEDULED_RUN_URL}. $merge"
  } > "$work/body.md"
  url=$(gh pr create --repo "$GITHUB_REPOSITORY" --base master --head "$SCHEDULED_BRANCH" \
    --title "$STANDING_WORK_ITEM: $SCHEDULED_TITLE" --body-file "$work/body.md" --label maintenance)
  echo "opened $url"
  out pull_request "$url"
  if [ "$SCHEDULED_MERGE" = when-green ]; then
    # auto-merge.yml's line, and its reasons: `--auto` hands the merge to GitHub, which waits for the
    # ruleset's required checks; it errors when the pull request is already mergeable, and then the merge is
    # made only when `gh pr checks --required` says the checks are green, because this token could merge
    # past them. `--match-head-commit` on both: the merge is asked for the commit this run reviewed and
    # pushed, and is refused when the branch's head is another by then.
    gh pr merge "$url" --squash --auto --match-head-commit "$SCHEDULED_HEAD" --repo "$GITHUB_REPOSITORY" || {
      gh pr checks "$url" --required --repo "$GITHUB_REPOSITORY" &&
        gh pr merge "$url" --squash --match-head-commit "$SCHEDULED_HEAD" --repo "$GITHUB_REPOSITORY"
    }
  fi
}

case $verb in
  sweep|gate|verdict|decide|scan|bundle|land|open) "$verb" ;;
  *) usage ;;
esac
