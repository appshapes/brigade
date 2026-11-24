#!/bin/sh
# usage: scripts/commit-push.sh commit <message>
#        scripts/commit-push.sh push
#
# The two last steps of the chain `make push` runs (the Makefile's `commit` and `push` targets), each after
# typecheck, pull, lint, build and test have passed. They are a script and not two recipe lines so that a test
# can run them for real in a throwaway repository (scripts/ci/push_chain_test.go).
#
#   commit   stages every change of the working tree (`git add --verbose :/ .`) and commits it under the
#            message. With nothing to commit it says so and ends well, without a message and without staging:
#            the chain goes on to the push.
#   push     pushes what the branch has that its upstream does not, and says how many commits that is. With
#            nothing ahead it says there is nothing to push and ends well.
#
# Until work item 443 `make push` on a clean tree ran the whole chain and then stopped at git's "nothing to
# commit, working tree clean", so a merge a session had concluded never reached origin that way, and the rule
# file told the session to push it by hand. A message is still required whenever there is something to commit,
# and it is asked for before anything is staged.
set -eu

case ${1:-} in
  commit)
    message=${2:-}
    # An assignment of its own, so that a `git status` that fails stops the step (set -e) and is never read as
    # a clean tree.
    changes=$(git status --porcelain --untracked-files=normal)
    if [ -z "$changes" ]; then
      echo "make commit: nothing to commit: the working tree is clean"
      exit 0
    fi
    case $message in
      *[![:space:]]*) ;;
      *)
        echo "make commit: there are changes to commit and no message: make push message=\"<work item>: <Imperative summary>\"" >&2
        exit 2
        ;;
    esac
    git add --verbose :/ .
    git commit -m "$message"
    ;;
  push)
    if upstream=$(git rev-parse --abbrev-ref --symbolic-full-name '@{upstream}' 2>/dev/null); then
      ahead=$(git rev-list --count "$upstream..HEAD")
      if [ "$ahead" -eq 0 ]; then
        echo "make push: nothing to push: $upstream has every commit of this branch"
        exit 0
      fi
      echo "make push: pushing $ahead commit(s) that $upstream does not have"
    fi
    git push --verbose
    ;;
  *)
    echo "usage: scripts/commit-push.sh commit <message> | push" >&2
    exit 2
    ;;
esac
