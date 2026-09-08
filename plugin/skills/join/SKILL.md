---
name: join
description: >-
  Join this project's Brigade team from inside the session: `/brigade:join <path>` runs
  `brigade team join --secret-file <path>` on the secret file the administrator sent, and `/brigade:join` alone
  re-consents a second checkout of a team this machine already holds. Runs only when the user invokes it.
user-invocable: true
disable-model-invocation: true
argument-hint: <path-to-secret-file>
allowed-tools: Bash(brigade:*)
---

# Join the team

Your user invoked this skill. `$ARGUMENTS` is the path of the secret file they saved, or empty.

1. If `$ARGUMENTS` is non-empty, run exactly `brigade team join --secret-file "<that path>"` — the path quoted, one
   Bash call. If it is empty, run exactly `brigade team join`. Nothing else: never read the file, never print or ask
   for its contents, never choose or guess a path, never retry with a different one.
2. Relay the command's output as it is. On success it names the team and says when this session attaches — at the
   next prompt, or, for a session already attached to another team, after `/reload-plugins`; repeat that to the
   user. On a refusal — a relative path, a file inside the repository, a missing file, a secret for another team —
   relay the message and stop: the user fixes the path or the file.
