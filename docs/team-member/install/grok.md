# Install in Grok Build

Brigade has shipped a package for xAI's Grok Build since 0.35.0 (2026-10-19): a direct TUI launched with
`grok --no-leader` joins the same team as the Claude Code, Muse Code and Codex CLI sessions, and takes its
messages through a persistent native monitor.

Status: Supported, with known gaps. It works, and [Known gaps](#known-gaps) below names what does not.

The measured hosts are Grok Build 1.0.62 (`1474f9cbdd91`, kernel name `grok-macos-aarch`), 1.0.67
(kernel name `grok-1.0.67-maco`) and 1.0.92 (kernel name `grok-macos-aarch`), where on 2026-11-22 and again on
2026-11-23 a session installed the package by the commands below, armed its monitor and answered a team
message; all three on macOS arm64. Linux is not measured. Later versions must preserve the native hook and
monitor contracts.
Leader mode is unsupported: its shared process does not identify a live terminal receiver.

Use Grok's normal marketplace and trust flow in your own terminal:

```sh
grok plugin marketplace add appshapes/brigade
grok plugin install brigade --trust
grok plugin enable brigade
grok --no-leader
```

The first command adds this repository as a plugin marketplace (its `.grok-plugin/marketplace.json` names the
package). Leave it out where `grok plugin marketplace list` already shows `brigade`: Grok Build also lists the
marketplaces Claude Code knows on the machine, and the command then stops at `Marketplace source already
configured`. In Grok Build 1.0.92 `install --trust` enables the plugin too, and the third command changes
nothing.

## What you get, and when

The package of the last published release, whatever `master` holds since: the marketplace file names the
package at that release's commit, and only a release moves it (work item 424). A push to this repository
changes nothing for you. When a release is published, one commit moves the pin to it, and your next update
brings that release's package ([Update in Grok Build](../update/grok.md)). Grok Build keeps the package as a
clone of this repository at that commit, with the plugin in its `grok/brigade` directory.

An install from before 2026-11-23 followed `master` and cannot follow the pin: its update fails, and it is
removed and installed again once ([Update in Grok Build](../update/grok.md#an-install-from-before-2026-11-23)).

Join the project's team as [the joining page](../join/grok.md) describes. Type an ordinary prompt in the direct
TUI. SessionStart registers it and shows a UI-only notice; after the first completed turn, Stop offers one
model-visible instruction to arm the persistent inbox monitor. Normal permissions may ask you to approve that
one command. A denial/failure settles without automatic retries; explicitly request monitor setup through
`/brigade:team-messaging` when ready.

Four native hooks run: SessionStart, UserPromptSubmit, Stop and SessionEnd. They use `exec` so the verified
parent is the TUI itself. Resume through Grok's normal session picker in a `grok --no-leader` TUI; a native
`load` starts a fresh receiver incarnation and gets one setup opportunity. Compaction alone does not rearm.
A leader socket in the environment, another harness's session marker beside Grok Build's, and a hook whose
parent is no live process of yours refuse registration. A fact Brigade merely did not expect, such as a binary
under a new name, no longer does: the session registers and its start says what was found, unless you set the
option `grok_strict` ([Configure in Grok Build](../configure/grok.md#the-strict-checks-grok_strict)). Do not
change host trust files or permissions to work around a refusal.

`grok plugin details brigade` locates the installed package. The command is its absolute `bin/brigade`, or
bare `brigade` when your PATH already names it. `brigade whoami --json` supplies the exact quoted
`monitor_command` and `monitor_description` for this incarnation; use those values rather than guessing a
cache path. [Configuration](../configure/grok.md), [monitor status and skills](../../agent/in-a-team/grok.md),
[security limits](../../security.md#17-grok-build-monitor-sessions),
[measured evidence](../../experiments/grok-participation.md) and
[package](../../../grok/brigade/README.md).

## Known gaps

It works: a Grok Build session registers, arms its monitor after its first turn, reads a team message and
answers it (Grok Build 1.0.92 on macOS arm64, 2026-11-22: the reply was stored 48 seconds after the send; again
on 2026-11-23, on the build that follows 0.57.0: 51 seconds). On a team that lives on a Brigade server the same
was measured on 2026-11-24, with a server on the same machine: 36 seconds.

- **Only a direct TUI.** Leader mode is refused, for a measured reason. In Grok Build 1.0.92 a plain `grok` is
  a direct TUI already (measured 2026-11-23; its guide names `--leader` and `use_leader = true` as what turns
  leader mode on), and `grok --no-leader` makes sure of it.
- **Nothing is received before the monitor is armed.** The session asks to arm it after its first completed
  turn, and you approve that one command. Refused or failed, messages wait until you ask for it again.
- **A message is read in pages**, one tool call each, so an answer takes longer than in the other harnesses.
- **One platform.** The measurements are on macOS arm64. Linux is not measured, nor is a session left idle for
  more than half an hour with the whole package, and the package does not run on Windows.
- **An install from before 2026-11-23 does not update.** It keeps working as it is, and is removed and installed
  again once ([Update in Grok Build](../update/grok.md#an-install-from-before-2026-11-23)).
- **Removing it needs care**: an uninstall leaves a line in Grok Build's `config.toml`, and `grok plugin
  marketplace remove brigade` can take the marketplace out of Claude Code's list too
  ([Leave and uninstall from Grok Build](../leave/grok.md)).
- **The roster shows no context size** for a Grok Build session.
- **The roster sentence is asked for at the end of a turn, and only under always-approve.** Grok Build shows the
  model nothing a prompt hook prints, so Brigade's reminder is its Stop hook's answer there: at most once in ten
  minutes, one more turn of the model each time; `share_doing: false` stops it. Under the other modes the
  session's line stays blank unless you ask for it
  ([Where Brigade asks the model](../use/your-session-on-the-roster.md#the-sentence-under-your-row)).
