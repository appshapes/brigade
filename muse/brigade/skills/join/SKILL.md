---
name: join
description: >-
  Join this project's Brigade team from inside the session: this skill, given a path, runs
  `brigade team join --secret-file <path>` on the secret file the administrator sent, and the skill with no path
  re-consents a second checkout of a team this machine already holds. Use when the user asks to join and names
  the path of the secret file they saved — never with a path you chose or a file you wrote.
user-invocable: true
argument-hint: <path-to-secret-file>
---

# Join the team

`$ARGUMENTS` is the path of the secret file the user saved, or empty: typed by them when they invoked this skill, or
passed by you when they asked in words and you loaded this skill — the path exactly as they wrote it, or nothing
if they named none. The path is always **theirs**: never one you chose, guessed or wrote.

1. If `$ARGUMENTS` is non-empty, run exactly `brigade team join --secret-file "<that path>"` — the path quoted, one
   shell call. If it is empty, run exactly `brigade team join`. `brigade` here is the command the `Brigade:` line
   of this session named: the bare word when it is on your PATH, else the bootstrap's path that line gave (the
   `not joined` line names it in its `run …` clause). Nothing else: never read the file, never print or ask for
   its contents, never choose or guess a path, never retry with a different one.
2. Relay the command's output as it is. On success it names the team and says when this session attaches — at the
   next prompt, or, for a session already attached to another team, in a new session; repeat that to the
   user. On a refusal — a relative path, a file inside the repository, a missing file, a secret for another team —
   relay the message and stop: the user fixes the path or the file.
