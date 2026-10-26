---
name: migrate
description: >-
  Move this project's Brigade team off its Supabase project onto the syncthing adapter, which keeps the team's
  messages in a folder Syncthing replicates between the members' machines: `/brigade:migrate <path>` runs
  `brigade team migrate --secret-file <path>`, which recreates the team over Syncthing under the same reference
  and name, writes a NEW join secret to that file and rewrites `.brigade.json`. Use when the team's creator asks
  to move the team to Syncthing, to drop the Supabase project, or to run Brigade with no backend. Creator only;
  never with a path you chose.
user-invocable: true
argument-hint: <path-for-the-new-secret-file>
allowed-tools: Bash(brigade:*)
---

# Move the team to Syncthing

`$ARGUMENTS` is the path where the team's NEW join secret is to be written: typed by the user through
`/brigade:migrate`, or passed by you when they asked in words and you loaded this skill. It is always **theirs**,
absolute or `~/…`, outside the repository; never one you chose. The command runs only for the team's creator and
only in a checkout whose `.brigade.json` names another adapter.

1. If `$ARGUMENTS` is empty, say that the command needs the path of a file to write the new secret to, outside the
   repository (say `~/brigade-<team>.secret`), and stop. Otherwise run exactly
   `brigade team migrate --secret-file "<that path>"`, the path quoted, one Bash call. Nothing else: never read the
   file afterwards, never print or ask for its contents, never retry with a different path.
2. Relay the command's output as it is. On success it names the team, this machine as the seed device, the file the
   secret went to, and the next steps: commit and push `.brigade.json`; send every member the new secret file over
   a password-grade channel; each member pulls and runs `/brigade:join <path>` with it, and their sessions move at
   their next start. Repeat those steps to the user. Say too what the output says is not carried: messages on the
   old backend, and the hosted mail and Slack gateways, which stay with the Supabase project.
3. On a refusal — not the creator, already over Syncthing, no `.brigade.json`, a path inside the repository, no
   `syncthing` on PATH — relay the message and stop: the user decides what to do.
