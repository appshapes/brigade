# Run headless or sandboxed sessions

## `claude -p`

A `claude -p` session binds an inbox socket and runs hooks and plugins (`claude --bare` does neither), so a
headless worker registers and receives team messages exactly as a terminal session does; the default policy is
`accept` there too. An idle worker whose standard input is still open is woken by an arriving message (29 of 29
measured wakes started a turn, first reply within 2.0 to 4.1 s); a worker that closes its input after its prompt
cannot be woken.

- A worker that must not take team messages starts with
  `--settings '{"pluginConfigs":{"brigade@brigade":{"options":{"team_inbound":"refuse"}}}}'`.
- Do **not** give it the ask rule on `brigade send`: that rule denies rather than prompts in `-p`, so the worker
  silently sends nothing.
- When a worker is woken, the `result` record it prints carries `origin.body`: the whole inbound frame in plain
  text on its standard output. Anything that reads that output is ingesting another person's untrusted text, and a
  tool that searches a transcript for a marker must never count `origin.body`.
- Use `--body-file` for any long body.

## The Bash sandbox

Hooks and the background watcher run **outside** the sandbox; only the commands the model runs are inside it.

- Add the project's host, `<ref>.supabase.co`, to `sandbox.network.allowedDomains`, or the first send is refused.
  The allowlist matches the literal host in the team file's URL.
- A sandboxed command cannot write to the credential directory, so a credential refresh there happens in memory
  only; the watcher is the process that refreshes and saves. An in-session `team create` or `team join` writes the
  credential directory and so fails under the sandbox: run them in a terminal.
- A Supabase stack on your own machine, on loopback, is out of reach from a sandboxed session, and this is not
  changing in this version.

The argument and the measurements: [docs/security.md, section 7](../../security.md#7-headless-claude--p-sessions)
and [section 10](../../security.md#10-your-own-machine).
