# Install in Grok Build

Brigade has shipped a package for xAI's Grok Build since 0.35.0 (2026-10-19): a direct TUI launched with
`grok --no-leader` joins the same team as the Claude Code, Muse Code and Codex CLI sessions, and takes its
messages through a persistent native monitor.

The measured hosts are Grok Build 1.0.62 (`1474f9cbdd91`, kernel name `grok-macos-aarch`), 1.0.67
(kernel name `grok-1.0.67-maco`) and 1.0.92, where on 2026-11-22 a session installed the package by the
commands below, armed its monitor and answered a team message; all three on macOS arm64. Linux is not
measured. Later versions must preserve the native hook and monitor contracts.
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
nothing. The package is installed as the repository's default branch has it at that moment.

Join the project's team as [the joining page](../join/grok.md) describes. Type an ordinary prompt in the direct
TUI. SessionStart registers it and shows a UI-only notice; after the first completed turn, Stop offers one
model-visible instruction to arm the persistent inbox monitor. Normal permissions may ask you to approve that
one command. A denial/failure settles without automatic retries; explicitly request monitor setup through
`/brigade:team-messaging` when ready.

Four native hooks run: SessionStart, UserPromptSubmit, Stop and SessionEnd. They use `exec` so the verified
parent is the TUI itself. Resume through Grok's normal session picker in a `grok --no-leader` TUI; a native
`load` starts a fresh receiver incarnation and gets one setup opportunity. Compaction alone does not rearm.
Native leader socket presence, an unknown parent, conflicting host markers or mismatched UUID/workspace
refuse registration. Do not change host trust files or permissions to work around a refusal.

`grok plugin details brigade` locates the installed package. The command is its absolute `bin/brigade`, or
bare `brigade` when your PATH already names it. `brigade whoami --json` supplies the exact quoted
`monitor_command` and `monitor_description` for this incarnation; use those values rather than guessing a
cache path. [Configuration](../configure/grok.md), [monitor status and skills](../../agent/in-a-team/grok.md),
[security limits](../../security.md#17-grok-build-monitor-sessions),
[measured evidence](../../experiments/grok-participation.md) and
[package](../../../grok/brigade/README.md).
