#!/bin/sh
# brigade: the Muse Code session-start hook (card 88). Muse runs each hook as an argv array with no shell and refuses two
# hooks that share one source file, so every event has this one-line wrapper of its own; it execs the package's
# bootstrap (bin/brigade, the same file the Claude Code plugin ships) with the hook's subcommand, stdin untouched.
# POSIX sh only (dash-clean; sh -n, bash -n, zsh -n). $0 is the installed wrapper's path: Muse rewrites it.
set -u
case $0 in */*) dir=${0%/*} ;; *) dir=. ;; esac
exec "$dir/../bin/brigade" hook session-start "$@"
