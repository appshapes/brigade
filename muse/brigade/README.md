# Brigade for Muse Code

This directory is the Muse Code package of Brigade: team messaging between the coding sessions of different people
and machines, with a Muse Code session taking part beside Claude Code ones. It ships the same bootstrap, version pin
and checksums as the Claude Code package (`plugin/`), a native Muse manifest with one wrapper per hook, and the
nine skills worded for Muse. One Brigade team, one backend, two kinds of session.

The procedures are in the documentation tree: [install](../../docs/team-member/install/muse-code.md),
[join](../../docs/team-member/join/muse-code.md), [update](../../docs/team-member/update/muse-code.md),
[configure](../../docs/team-member/configure/muse-code.md), [leave](../../docs/team-member/leave/muse-code.md); the
whole [Team Member](../../docs/team-member/README.md) tree; what the model reads and how a Muse session is woken,
[AI Agent › Muse Code](../../docs/agent/in-a-team/muse-code.md).

## What this package ships

- **`.muse-plugin/plugin.json`**, the native manifest: three hooks (session-start, prompt, session-end) as argument
  vectors with no shell, each its own one-line wrapper under `hooks/` that execs the bootstrap, because Muse refuses
  two hooks that share one source file; and the nine skills.
- **`bin/`**: copies of `plugin/bin`'s bootstrap, `VERSION` and `checksums.txt`, kept equal by the release sequence
  and checked by `make muse-plugin-check` and the release workflow.
- **The skills**, under `skills/`: the same nine as the Claude Code package, worded for Muse.

## How it is installed

From `marketplace.json` at the repository root, Muse's catalog (card 115): one entry, this directory, with the
digest Muse computes over it. Muse refuses a catalog entry without a digest and an install whose tree does not
match it, so `make muse-digest` rewrites the catalog after any change here and `make release` rewrites it for
every release, which is why Muse Code is installed on the release machine (owner ruling 2026-10-17; card 114 is the
way without it). Muse reads this catalog before the Codex one (`.agents/plugins/marketplace.json`) and the Claude
Code one (`.claude-plugin/marketplace.json`), and neither of those hosts reads it.

## What is different from the Claude Code package, and why

- **No inbox socket.** Muse has no way for an outside process to post into a session, so the watcher writes each
  message to a per-session inbox file and the model's own monitor, armed on `brigade inbox follow` at the first
  prompt from Brigade's instruction, is what wakes the session.
- **The hooks run at the first prompt**, not at launch, and only on typed prompts, so the watcher carries the
  heartbeat; SessionEnd gets a few hundred milliseconds, so the watcher's own exit closes the session.
- **Options** come from `~/.config/brigade/muse-options.json`: Muse runs no plugin `userConfig`.
- **Directories** are resolved from `HOME` alone: the hooks see no `XDG_*`, and the shell must agree with them.
- **The frame's preamble names no Claude Code surface:** it says the message came from "another person's coding
  session", names AGENTS.md beside CLAUDE.md, and says Muse's own session messaging cannot reach a Brigade session.
- **`brigade` is not on the shell's PATH** unless you put it there: the session-start line names the bootstrap's
  path, and a symlink gives your terminal and the session the bare word.

The measurements behind these choices are in [the live test](../../docs/experiments/muse-participation.md); what
Muse's own layers add and what has not been measured, [docs/security.md, section 15](../../docs/security.md#15-muse-code-sessions).
