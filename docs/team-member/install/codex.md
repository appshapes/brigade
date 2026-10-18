# Install in Codex CLI

Brigade ships a package for OpenAI's Codex CLI, so a Codex session joins the same team as the Claude Code and Muse
Code ones.

## Install

In a terminal:

```sh
codex plugin marketplace add appshapes/brigade
codex plugin add brigade@brigade
```

The first command adds this repository as a plugin marketplace (its `.agents/plugins/marketplace.json` names the
package); the second installs the plugin into Codex's plugin cache. Codex 0.163.0 or later: the `codex queue`
command Brigade delivers through arrived with it.

## Trust the hooks

Installing a plugin does not trust its hooks. The first `codex` you start after the install asks about them:
choose **Trust all and continue**. Later, `/hooks` in a session lists them (SessionStart, UserPromptSubmit,
SessionEnd, each "needs review") and `t` trusts all three. Until they are trusted nothing of Brigade runs: a
prompt registers nothing and `brigade whoami` says the session is not registered. Do not use
`--dangerously-bypass-hook-trust` for this: it runs the hooks, but it also puts Codex in embedded mode (below).

## Keep Codex in daemon mode

Never pass `-c`, `--enable`, `--disable`, `--no-daemon` or `--dangerously-bypass-hook-trust` on a `codex` launch
that should take part in a team: any of them puts Codex in embedded mode, where a queued message waits for a poll
of a few seconds instead of starting a turn at once. Options go in `config.toml`; hooks are trusted once in
`/hooks`.

## Put brigade on your PATH

Codex does not put a plugin's binaries on the shell's PATH. The bootstrap is under Codex's plugin cache
(`~/.codex/plugins/cache/brigade/brigade/<version>/bin/brigade`); the session-start line names its path. A symlink
gives your terminal and the session the bare word:

```sh
ln -sf <that path> ~/.local/bin/brigade
```

## Join, then take part

1. Join the project's team in a terminal, in the checkout: [Join in Codex CLI](../join/codex.md).
2. Open `codex` in the checkout and type one prompt, any prompt; `brigade sessions` is a good one.

Codex runs Brigade's SessionStart hook at that first prompt, not at launch. That is when the session registers;
from then on every team message wakes the idle session as a turn of its own, within a second of the send. A
session is unknown to the team until that prompt; a resumed session (`codex resume`) registers again at its first
prompt, and the new thread `/new` opens is a new session; `/compact` needs nothing.

**Ending a session.** Codex runs Brigade's hooks from its shared background daemon, which outlives `/quit`, and
fires no SessionEnd at `/quit`; the Brigade session therefore stays `idle` on the roster until Codex ends the
thread — when you archive or delete it, or 30 minutes after the quit (Codex's idle rule, measured to the second:
the session was offline at 30:00). A message sent to it meanwhile is accepted and waits in Codex's own queue; it
appears as a turn when you `codex resume` that thread. The same holds for the thread you leave behind with `/new`. (In embedded mode `/quit` ends the session at
once.)

## What differs from Claude Code

- **Options** come from `~/.config/brigade/codex-options.json`, not plugin settings: [Configure in Codex CLI](../configure/codex.md).
- **Directories.** A Codex session resolves Brigade's directories from `HOME` alone, so `~/.config/brigade` and
  `~/.local/state/brigade` whatever `XDG_*` says; a member who sets `XDG_CONFIG_HOME` puts `config_dir` in the
  options file.
- **Approval.** Under Codex's default profile the model's shell runs in a sandbox with the network off, so a
  `brigade` command that reaches the backend asks for your approval; under
  `--dangerously-bypass-approvals-and-sandbox` nothing is asked. Brigade's hooks and its watcher run outside the
  sandbox either way, so team messages arrive under both.
- **A message is a turn.** It looks like a prompt in the transcript, and its first sentence says it was not typed
  by you.

What Codex's own layers add and what has not been measured: [docs/security.md, section 16](../../security.md#16-codex-cli-sessions).
The package itself: [codex/brigade/README.md](../../../codex/brigade/README.md); the measurements behind it:
[the live test](../../experiments/codex-queue-participation.md).
