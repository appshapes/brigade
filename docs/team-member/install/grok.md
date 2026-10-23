# Install in Grok Build

This source implements monitor participation for Grok Build direct TUIs launched with `--no-leader`. Final native acceptance and the next release remain coordinator gates; the current release pins are unchanged.

The measured hosts are Grok Build 1.0.62 (`1474f9cbdd91`, kernel name `grok-macos-aarch`) and 1.0.67
(kernel name `grok-1.0.67-maco`). Later versions must preserve the native hook and monitor contracts.
Leader mode is unsupported: its shared process does not identify a live terminal receiver.

After a monitor-enabled release, use Grok's normal marketplace and trust flow in your own terminal:

```sh
grok plugin marketplace add appshapes/brigade
grok plugin install brigade --trust
grok plugin enable brigade
grok --no-leader
```

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
