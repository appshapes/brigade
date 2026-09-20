---
name: sessions
description: >-
  Show the team's sessions: `/brigade:sessions` runs `brigade sessions` and prints its output unchanged, and
  `/brigade:sessions --all` includes the offline ones. Use it whenever the roster is being SHOWN to the user, so
  they see what the command printed rather than a retelling of it; reading the roster to address a message is
  `brigade:team-messaging` and its `--json` form.
user-invocable: true
argument-hint: "[--all]"
allowed-tools: Bash(brigade sessions:*)
---

# Show the team's sessions

`$ARGUMENTS` is what came after the command name: typed by the user through `/brigade:sessions`, or passed by
you when you loaded this skill to show the roster — `--all` only when the user asked for the offline sessions
too, otherwise nothing. Either way the steps below are the whole of it.

1. If `$ARGUMENTS` is empty, run exactly `brigade sessions`. If it is `--all`, ignoring surrounding whitespace,
   run exactly `brigade sessions --all`. For anything else, say that `--all` is the only argument this skill
   takes, and stop — never a flag the user did not ask for, never `--json`, never a second command.
2. If the command succeeded, print what it printed **verbatim**, inside a fenced block, and write nothing else.
   No table, no list, no counts, no summary, no "this session is …", no comparison with an earlier run, no remark
   about any session or its state. On the first use on a machine, a `brigade: first use: downloading …` line
   precedes the roster: print that too rather than editing the output. The command's output is the display;
   reformatting it is what this skill exists to stop.
3. If the command failed, print what it printed, verbatim, and stop. Do not run it again and do not offer
   another form of it.

Sending a message, reading the roster on your own initiative, and every other `brigade` verb belong to the
`brigade:team-messaging` skill. This one only shows what `brigade sessions` prints.
