# Brigade for Muse Code

This directory is the Muse Code package of Brigade: team messaging between the coding sessions of different
people and machines, with a Muse Code session taking part beside Claude Code ones. It ships the same bootstrap,
version pin and checksums as the Claude Code package (`plugin/`), a native Muse manifest with one wrapper per
hook, and the skills. One Brigade team, one backend, two kinds of session.

## Install

Muse Code installs a local plugin bundle; from a checkout of this repository:

```sh
muse plugins install ./muse/brigade --scope user
muse plugins approve brigade
```

The second command is Muse's own review step: a plugin's hooks stay inactive until the person approves them.
Then join the project's team exactly as a Claude Code member does — `brigade team join --secret-file <path>` in
the checkout, in a terminal — and open `muse` in that checkout. [docs/setup.md](../../docs/setup.md) has the whole
procedure, including the first-time Supabase setup an administrator does once.

## How a Muse session takes part

Muse Code fires Brigade's SessionStart hook at the **first prompt** of a session, not at launch. That prompt is
when Brigade registers the session, starts the watcher that receives the team's messages, and puts two lines in
front of the model: the session's name and team, and an instruction to arm Muse's **Monitor** tool — once,
persistent, with an immediate wake — on `brigade inbox follow`. From then on every team message the watcher
receives is printed by the follower and wakes the idle session about 0.2 s later; the model reads it, acts on it
as the team-messaging skill says, and replies with `brigade send`. So: join once, then in each new or resumed
Muse session type one prompt (anything — `brigade sessions` is a good one, since its answer also shows the
teammates), and the session is a participating member from there.

Two things follow from the first-prompt rule. A session is a mailbox until that prompt. And a resumed session
(`muse resume`) needs its first prompt again, because the monitor died with the previous process; `/compact` and
`/clear` need nothing.

Muse's sandbox (its default profile) lets the follower read the inbox and lets `brigade send` reach the backend
through Muse's proxy, where the first connection to the backend's host is reviewed once; under `--yolo` nothing
is reviewed. Brigade never writes under the workspace: its state lives in `~/.local/state/brigade` and its
credentials in `~/.config/brigade`.

## Options

Muse Code does not run a plugin's `userConfig`, so a Muse session reads its options from one file Brigade owns:

```
~/.config/brigade/muse-options.json
```

a JSON object whose keys are the option names of the Claude Code package (`team_inbound`, `label`, `frame`,
`frame_file`, `sync`, `message_sound`, `message_notification`, `message_interval`, `share_doing`,
`share_workspace_label`, `workspace_label`, `poll_on_prompt`, `adapter_command`, `config_dir`) and whose values
are strings, booleans or numbers — the same meanings and defaults as [plugin/README.md](../plugin/README.md)
"Options". A missing file is the defaults; a file that is not a JSON object earns one line at session start and
the defaults. Unknown keys are ignored.

## What is different from the Claude Code package, and why

- **Hooks are wrappers.** Muse runs a hook as an argument vector with no shell and refuses two hooks that share
  one source file, so each event has its own one-line wrapper under `hooks/` that execs the bootstrap.
- **No inbox socket.** Muse has no way for an outside process to post into a session, so the watcher writes each
  message to a per-session inbox file and the model's own monitor, armed on `brigade inbox follow`, is what
  wakes the session. The arming is the model's doing, from Brigade's instruction, at the first prompt.
- **The prompt hook runs only on typed prompts** (not on monitor-woken turns), so the watcher carries the
  heartbeat, and SessionEnd gets a few hundred milliseconds, so the watcher's own exit closes the session.
- **The frame's preamble names no Claude Code surface:** it says the message came from "another person's coding
  session", names AGENTS.md beside CLAUDE.md, and says Muse's own session messaging cannot reach a Brigade
  session. The rest of the frame is identical.
- **`brigade` is not on the shell's PATH** unless you put it there: the session-start line names the bootstrap's
  path, the skills say to use it, and a symlink such as `ln -sf <bootstrap> ~/.local/bin/brigade` gives your
  terminal and the session the bare word.

The measurements behind these choices are in
[docs/experiments/muse-participation.md](../../docs/experiments/muse-participation.md).

## Update

```sh
muse plugins update brigade
```

from a checkout that has pulled the new version, then start a new session. A running session keeps the version
it started with.

## Uninstall

`muse plugins remove brigade`, then the same `brigade team leave` / state and credential removal as
[docs/setup.md](../../docs/setup.md), "Leaving and uninstalling", describes for Claude Code.
