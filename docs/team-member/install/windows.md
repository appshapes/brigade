# Install on Windows

Brigade runs natively on Windows from 0.41.0 (card 133): in Claude Desktop's Code tab, in Claude Code from a
terminal, and in the VS Code extension. The same plugin, the same two commands; one more requirement, and a
few things that differ from macOS and Linux.

## Before you start

- **Git for Windows.** Install it from [git-scm.com](https://git-scm.com/downloads/win) before the first session.
  Claude Code uses its Git Bash for the Bash tool, and Brigade's skills run `brigade` through that tool. Without
  it Claude Code falls back to PowerShell, where the skills' commands do not run. Claude Desktop reads `PATH`
  from your user or system environment variables, not from a PowerShell profile, so after installing Git quit
  and reopen Desktop.
- **Syncthing, if the project syncs folders** (its `.brigade.json` has a `sync` member): `winget install
  Syncthing.Syncthing`, or unpack the Windows zip from [syncthing.net](https://syncthing.net/downloads/) and put
  `syncthing.exe` on `PATH`. Without it the session joins all the same and says file sync is off.
- **Windows 10 1809 or later**, which is also Claude Code's floor; both x64 and ARM64 machines are supported.

## The commands

The same as [Install in Claude Code](claude-code.md): in a session opened in a project that uses Brigade,
`/plugin install brigade@brigade` (choose **user**), then `/reload-plugins`. The first machine of a project runs
`/plugin marketplace add appshapes/brigade` first.

## What the first use does

The plugin carries `bin\brigade.exe`, a small launcher, beside the macOS and Linux start script. On Windows the
hooks and the Bash tool run the launcher, which on a cold cache downloads the Windows binary for your CPU
(about 10 MB), checks it against the sha256 that ships inside the plugin and keeps it under
`%LOCALAPPDATA%\brigade\bin`. As on the other platforms the first `SessionStart` starts that download in the
background and your next prompt attaches; a download that fails is reported once, on a later prompt, as a line
beginning `Brigade: not installed:`, and `/clear` or a new session retries.

## Where Brigade keeps its files

| What | macOS and Linux | Windows |
| --- | --- | --- |
| configuration (profiles, credentials, the developer pointer) | `~/.config/brigade` | `%APPDATA%\brigade` |
| state (session maps, pidfiles, the watcher's log) | `~/.local/state/brigade` | `%LOCALAPPDATA%\brigade` |
| the cached binary | `~/.local/share/brigade/bin` | `%LOCALAPPDATA%\brigade\bin` |

`BRIGADE_CONFIG_DIR` and `BRIGADE_STATE_DIR` override the first two as they do elsewhere; the `XDG_*` variables
are not read on Windows. The join secret file goes outside the repository, as always: `~/brigade-<team>.secret`
in Git Bash, which is `C:\Users\<you>\brigade-<team>.secret`.

## In a terminal

`brigade whoami`, run in a session, prints the `terminal:` line with the launcher's path,
`…\plugins\cache\brigade\brigade\<version>\bin\brigade.exe`. Run that path in PowerShell or Git Bash with the
command after it, as [Use brigade in a terminal](terminal.md) describes; the three terminal-only commands are
the same three.

## What differs

- **The sound and the notification are Windows' own.** `message_sound: on` plays the notification sound of your
  sound scheme at about a quarter volume, from the watcher itself; `message_notification: on` shows a
  notification-area balloon, which Windows presents as a toast headed "Brigade" for about six seconds (it is not
  kept in the notification centre). Nothing is started for either. Focus assist and do not disturb apply to the
  toast as to any other.
- **The Muse Code, Codex CLI and Grok Build packages** are macOS and Linux only; on Windows, Brigade is the
  Claude Code plugin.
- **Private files.** On macOS and Linux Brigade refuses a credential or a session map that any other user could
  read. Windows has no such mode bits; Brigade trusts the user profile directories above, which Windows gives
  each user a private ACL on. [docs/security.md](../../security.md) §10 says so, and what it means.
- **WSL 2** still works, as Linux: Claude Code installed inside the distribution, with the plugin installed
  there. Claude Desktop's WSL sessions cannot load plugins, so a Desktop user on Windows uses the native route
  above.

Next: [Join in Claude Code](../join/claude-code.md).
