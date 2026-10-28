# E12 drivers: the brigade binary on native Windows

The scripts behind [docs/experiments/E12-windows-native-proof.md](../../../docs/experiments/E12-windows-native-proof.md)
(card 133), kept as a record of what ran. They assume the machine the report describes: a Windows 11 guest with a
development build at `C:\src\brigade-dev.exe`, the plugin checkout at `C:\src\brigade`, a checkout of the test
team at `C:\src\win133`, and a Mac holding the same team's checkout. Nothing here runs in CI.

| Script | Side | What it does |
| --- | --- | --- |
| `proof-win3.ps1` | Windows | A sleeper as `CLAUDE_PID` and the real `hook session-start`, then a sink-mode watcher, both created through WMI so they outlive the SSH session (results 3 to 5). |
| `proof-win4.ps1` | Windows | A second session whose inbox is a named pipe, the real hook spawning the real detached watcher (result 6). |
| `pipe-server.ps1` | Windows | The stand-in for Claude Code's inbox: one `LOCAL\` named pipe, every line logged with a timestamp. |
| `proof-win5.ps1` | Windows | `hook session-end` through the session's environment, timing the stop (result 7). |
| `proof-mac.sh` | macOS | The Mac half: `start`, `watch`, `send <session> …`, `sink`, `whoami`, `sessions`, `end`; strips the outer session's variables by prefix (gates.md). `WIN133_REPO` and `PROOF_ROOT` name its checkout and scratch directory. |
