# Brigade for Codex CLI

This directory is the Codex CLI package of Brigade: team messaging between the coding sessions of different people
and machines, with a Codex session taking part beside Claude Code and Muse Code ones. It ships the same bootstrap,
version pin and checksums as the Claude Code package (`plugin/`), a native Codex manifest, a `hooks/hooks.json`
whose three commands run that bootstrap, and the seven skills worded for Codex. The repository root's
`.agents/plugins/marketplace.json` is the marketplace that names it. One Brigade team, one backend, three kinds of
session.

The procedures are in the documentation tree: [install](../../docs/team-member/install/codex.md),
[join](../../docs/team-member/join/codex.md), [update](../../docs/team-member/update/codex.md),
[configure](../../docs/team-member/configure/codex.md), [leave](../../docs/team-member/leave/codex.md); the whole
[Team Member](../../docs/team-member/README.md) tree; what the model reads and how a Codex session is woken,
[AI Agent › Codex CLI](../../docs/agent/in-a-team/codex.md).

## What this package ships

- **`.codex-plugin/plugin.json`**, the native manifest: the plugin's name, version, description and interface, and
  `skills` pointing at `skills/`.
- **`hooks/hooks.json`**: three hooks (SessionStart, UserPromptSubmit, SessionEnd), each one command string Codex
  runs through its shell: `"${PLUGIN_ROOT}/bin/brigade" hook <event> --host codex --plugin-root "${PLUGIN_ROOT}"`.
  Codex runs its hooks with no `CODEX_*` variable in their environment, so the host and the package root travel on
  argv and the thread id comes from the hook's document.
- **`bin/`**: copies of `plugin/bin`'s bootstrap, `VERSION` and `checksums.txt`, kept equal by the release sequence
  and checked by `make codex-plugin-check` and the release workflow.
- **The skills**, under `skills/`: the same seven as the Claude Code package, worded for Codex (`$brigade:sessions`, `$brigade:join`,
  `$brigade:update`, `$brigade:setup`, `$brigade:email`, `$brigade:slack`, and `team-messaging`, which the model loads itself).

## What is different from the Claude Code package, and why

- **Delivery is a queued turn.** Codex has no inbox socket, but `codex queue --thread <id> --message <text>` wakes an
  idle session from outside; the watcher runs it for each frame, after checking the Codex process is still alive,
  because the command's exit 0 means stored, not delivered. The frame arrives as a turn the person did not type,
  and says so. Nothing is armed; nothing is polled.
- **The hooks run at the first prompt**, not at launch, from Codex's shared background daemon in daemon mode: so a
  session is keyed by its thread, not by a process, `/new` is a new thread and a new session, and `/quit` leaves
  the Brigade session `idle` until Codex ends the thread (archive, delete, or 30 minutes after the quit, measured),
  since Codex fires no SessionEnd at `/quit` there. In embedded mode the hooks are the TUI's children and `/quit`
  ends the session at once.
- **Options** come from `~/.config/brigade/codex-options.json`: Codex plugins have no option mechanism.
- **Directories** are resolved from `HOME` alone: the hooks inherit the TUI's environment and the model's shell the
  app-server daemon's, and `HOME` is what both agree on.
- **The frame's preamble names no Claude Code surface:** it says the message came from "another person's coding
  session", names AGENTS.md beside CLAUDE.md, and says `codex queue` cannot reach a Brigade session.
- **`brigade` is not on the shell's PATH** unless you put it there: the session-start line names the bootstrap's
  path, and a symlink gives your terminal and the session the bare word.
- **Trusting the hooks is the person's step.** Installing a Codex plugin does not trust its hooks: the first
  launch afterwards asks ("Trust all and continue"), and `/hooks` then `t` does the same later, once. The
  `--dangerously-bypass-hook-trust` flag runs them too, but puts Codex in embedded mode, where delivery waits for a
  poll of a few seconds instead of starting the turn at once.

The measurements behind these choices are in [the live test](../../docs/experiments/codex-queue-participation.md);
what Codex's own layers add and what has not been measured, [docs/security.md, section 16](../../docs/security.md#16-codex-cli-sessions).
