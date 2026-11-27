---
name: join
description: >-
  Join this project's Brigade team from inside the session: {{if in "muse"}}this skill, given a path,{{else}}`{{invoke "join"}} <path>`{{end}} runs
  `brigade team join --secret-file <path>` on the secret file the administrator sent, and {{if in "muse"}}the skill with no path{{else}}`{{invoke "join"}}` alone{{end}}
  re-consents a second checkout of a team this machine already holds. Use when the user asks to join and names
  the path of the secret file they saved — never with a path you chose or a file you wrote.
user-invocable: true
argument-hint: <path-to-secret-file>
allowed-tools: Bash(brigade:*)
---

# Join the team

`$ARGUMENTS` is the path of the secret file the user saved, or empty: typed by them {{if in "muse"}}when they invoked this skill{{else}}through `{{invoke "join"}}`{{end}}, or
passed by you when they asked in words and you loaded this skill — the path exactly as they wrote it, or nothing
if they named none. The path is always **theirs**: never one you chose, guessed or wrote.

1. If `$ARGUMENTS` is non-empty, run exactly `brigade team join --secret-file "<that path>"` — the path quoted, one
   {{say "claude" "Bash" "*" "shell"}} call. If it is empty, run exactly `brigade team join`.{{if in "muse" "codex" "antigravity"}} `brigade` here is the command the `Brigade:` line
   of this session named: the bare word when it is on your PATH, else the bootstrap's path that line gave (the
   `not joined` line names it in its `run …` clause).{{end}} Nothing else: never read the file, never print or ask
   for its contents, never choose or guess a path, never retry with a different one.{{if in "grok"}}
   {{commandAt "   "}}{{end}}
2. Relay the command's output as it is. On success it names the team and says when this session attaches — at the
   next prompt, or, for a session already attached to another team, in a new session{{if in "claude"}} or in this one once it is
   exited and resumed (`/reload-plugins` does not move it){{end}}; repeat that to the
   user. On a refusal — a relative path, a file inside the repository, a missing file, a secret for another team —
   relay the message and stop: the user fixes the path or the file.
