# Install in Codex CLI

Brigade ships a package for OpenAI's Codex CLI, so a Codex session joins the same team as the Claude Code and Muse
Code ones.

Status: Supported, with known gaps. It works, and [Known gaps](#known-gaps) below names what does not.

## Install

In a terminal:

```sh
codex plugin marketplace add appshapes/brigade
codex plugin add brigade@brigade
```

The first command adds this repository as a plugin marketplace (its `.agents/plugins/marketplace.json` names the
package); the second installs the plugin into Codex's plugin cache. Codex 0.163.0 or later: the `codex queue`
command Brigade delivers through arrived with it.

## What you get, and when

The package of the last published release, whatever `master` holds since: the marketplace file names the
package at that release's commit, and only a release moves it (work item 424). A push to this repository
changes nothing for you. When a release is published, one commit moves the pin to it; Codex looks at the
marketplace by itself when it starts, and installs the new release's package then, with no command of yours.
It asks you to review Brigade's hooks once for that release ([Known gaps](#known-gaps)).

An install from before 2026-11-23 followed `master`. It needs nothing: at a start of Codex it moves to the
last release's package by itself, and asks for the hooks review once if the two differ.

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

## Where brigade is

Codex does not put a plugin's binaries on the shell's PATH. The bootstrap is under Codex's plugin cache
(`~/.codex/plugins/cache/brigade/brigade/<version>/bin/brigade`); the session-start line names its path, and that
path is the command, in the session and in your terminal. It carries the version, so the next session's start line
names the new one after an update. No symlink and no copy: both go stale at the next update
([Use brigade in a terminal](terminal.md)).

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

## Known gaps

It works: a message sent to an idle Codex session starts a turn there and is answered (Codex CLI 0.166.0 on macOS
arm64, 2026-11-22: the reply was stored 14 seconds after the send; again on 2026-11-23, on the build that
follows 0.57.0: 10 seconds). On a team that lives on a Brigade server the same was seen once (2026-11-24, a
server on the same machine: the session registered, stood on the server's roster and answered another member's
message; the reply was stored 88 seconds after the send, two approvals included).

- **Codex asks you to review Brigade's hooks again once for every release**, and until you trust them nothing
  of Brigade runs: no registration, no message. Its trust covers every file of the installed package (measured
  2026-11-22), and every release changes the package. Between two releases nothing changes it
  ([What you get, and when](#what-you-get-and-when)). The start in which Codex fetches the changed package may
  not ask: on 2026-11-23 one such start showed `Hook failed: plugin contents changed; hook approval required`
  at the first prompt and ran no hook of Brigade's, and the start after it asked "Hooks need review"; another
  asked at once. Only the hooks' content hashes change in `config.toml` when you trust them again.
- **A launch flag can slow delivery to a poll.** `-c`, `--enable`, `--disable`, `--no-daemon` and
  `--dangerously-bypass-hook-trust` put Codex in embedded mode ([Keep Codex in daemon mode](#keep-codex-in-daemon-mode)).
- **Under the default profile each `brigade` command that reaches the backend asks for your approval**: the
  model's shell has no network there, and a server on the same machine is no exception (2026-11-24). What else
  that profile's sandbox does to Brigade was not measured.
- **A quit is not an end.** The session stays `idle` on the roster for up to 30 minutes after `/quit`, and a
  message sent meanwhile waits for your `codex resume`.
- **One platform.** The measurements are on macOS arm64; Linux is not measured, and the package does not run on
  Windows.

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
