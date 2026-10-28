# Native Windows support (Trello card 133)

Decision D33 (`docs/research/decisions-2026-08-30.md`, 2026-08-30) said native Windows is not supported and
Windows users use WSL 2. Card 133 asked for a new ruling. This plan records the ruling, the design it rests on,
and the work.

## 1. The ruling (Rjae, 2026-10-27)

- **Native Windows is in**, for one profile: the Windows user who runs Claude Desktop's Code tab in the native
  Windows environment (and the same Claude Code from a terminal). "Any decently technical Windows user can use
  WSL 2"; the target is the one who does not.
- **The one condition: no impact on macOS and Linux users.** "I would rather not support Windows than impact
  macOS and Linux users." The design below is built so the hooks file, the POSIX bootstrap's behaviour and the
  macOS and Linux binaries' behaviour do not change; if the condition cannot be met, D33 stands.
- **Folder sync is in** from the first release (Syncthing ships Windows amd64 and arm64 builds; the engine's only
  Unix-specific parts are the detached start, the signal stop and the liveness probe).
- **Security checks on Windows trust the profile directory** for now: the thirteen "regular file, mode 0600,
  owned by me" checks accept a regular, non-symlink file on Windows, `docs/security.md` gets a Windows paragraph
  saying so, and an ACL verification through `golang.org/x/sys/windows` is its own card.
- **The proof** is a Windows CI job for everything mechanical plus a Windows 11 ARM virtual machine (UTM) on
  the release machine for the Claude Desktop Code tab itself, which Rjae signs in to once.
- **Targets:** windows/amd64 and windows/arm64 binaries in the release and the reproducible checksums; one
  committed amd64 launcher, which runs under Windows' x64 emulation on ARM machines and downloads the native
  arm64 binary there.
- **Out of scope, documented as such:** the sound and the banner on Windows; the Muse, Codex and Grok packages
  (macOS and Linux as before); PowerShell as the tool shell (Git for Windows is a requirement, so the skills'
  heredoc commands keep working); WSL 2 stays the documented route for whoever prefers it.

## 2. What the card's research got right and what it left open (verified 2026-10-27)

Right: D33's text; the four goreleaser targets; the POSIX `plugin/bin/brigade`; the named pipe plus the auth
line first on Windows (`.context/plans/implementation/A-verified-facts.md` §A.2); "Plugins aren't available in
WSL sessions" on the Desktop page (still true today).

Answered: the card asked whether plugin hooks run through Git Bash. They do not for this plugin. Our three hooks
are exec form (`command` plus `args`, which `make plugin-check` requires), and the hooks reference says exec form
spawns the command directly with no shell on every platform; on Windows the command must resolve to a real
executable. Only shell-form hooks run through Git Bash, and moving to shell form would put a shell in front of
every macOS and Linux hook call, which is the impact the ruling forbids.

## 3. The design

### 3.1 The launcher: `plugin/bin/brigade.exe` beside the POSIX bootstrap, hooks file unchanged

Node's process spawner on Windows tries `<name>.com` and then `<name>.exe` for an extensionless path. If Claude
Code's spawner does the same, the hooks file every platform uses today (`"command":
"${CLAUDE_PLUGIN_ROOT}/bin/brigade"`) runs the sh bootstrap on macOS and Linux and `brigade.exe` on Windows with
no change at all. **Step 0 measures exactly this** (`.github/workflows/experiment-windows-hooks.yml`,
`scripts/experiments/windows-hook-probe`): plugin/ copied as it ships, the probe as `bin/brigade.exe`, Claude
Code installed natively on a Windows runner, one `claude -p` session. A `hook session-start` block in the probe's
log is GO; nothing is NO-GO, and NO-GO ends the card with D33 kept, because every alternative changes the hook
path on all three platforms. **Measured 2026-10-27: GO** (`docs/experiments/E11-windows-hook-resolution.md`), and
the same run showed a plain `os.OpenFile` of the named pipe with the auth line first is delivered as a user turn,
the Bash tool is Git Bash with `bin/` on PATH, and the hook environment has `USERPROFILE` and no `HOME`.

The launcher is a small Go program (`cmd/brigade-launcher`, windows-only): it reads the committed
`plugin/bin/VERSION` and `checksums.txt` beside itself, downloads `brigade_<version>_windows_<arch>.exe` with
the `curl.exe` Windows 10 1803+ ships (an argv array, never a shell), verifies the sha256, installs it atomically
under the user's data directory and runs it with stdin, stdout, stderr and the exit code passed through. No TLS
stack of its own, so about 1.5 MB. It is reproducible from source with the release flags and a CI check
rebuilds it and diffs (like `make checksums-check`); it is re-committed only when its own source changes, never
per release. The bootstrap sh script gains one case arm: under Git Bash (`uname -s` = `MINGW*`, `MSYS*`,
`CYGWIN*`) it execs `brigade.exe` beside itself, so the Bash tool's bare `brigade` works on Windows too.

### 3.2 The port, all behind `_windows.go` files

Unix files stay as they are; shared files change only where a platform-specific check is hoisted into a
two-implementation helper.

- Delivery: `socketpost` dials the named pipe and writes the auth frame first.
- `procutil`: liveness, start time and command name through the Windows process API.
- `adapterkit`: file lock (`LockFileEx`), detached spawn (`CREATE_NEW_PROCESS_GROUP`, `DETACHED_PROCESS`; 0.41.1 replaced the latter with `CREATE_NO_WINDOW`, P61-11),
  strict read.
- `harness`: adapter and watcher spawn, hook end, session-map owner, inbound sink, watch guard and host files
  and inject, inbox follow, grok lock (Grok is out of scope: Windows reports "not Grok").
- The thirteen mode-and-owner checks: one helper, `private(fi)` on Unix is today's check, on Windows "regular,
  not a symlink".
- Syncthing engine: detached windowless start, stop through the REST shutdown then TerminateProcess after
  StopWait, liveness through procutil.
- Notify: Windows returns no sound and no banner.
- Paths: `CLAUDE_CONFIG_DIR ?? %USERPROFILE%\.claude`; Brigade's own directories from the Windows profile
  directories.
- Build and lint for a third OS: `GOOS=windows` in `make typecheck`'s cross-check and `make lint`.
- Tests: sh-fixture tests tagged `!windows`; Windows tests for every new file; a Windows CI job on a GitHub-hosted
  runner (free for a public repository) running the suite and the real-engine Syncthing test with the Windows
  zip unpacked on the runner.

### 3.3 Release and packages

- goreleaser and `make cross`: six targets; `checksums.txt` gains two lines (the POSIX bootstrap greps its own
  platform's line, so the extra lines are inert on macOS and Linux).
- `plugin-check`: the allowlist admits `bin/brigade.exe`; the no-secrets scan skips the binary; a new check
  reproduces the launcher.
- Muse, Codex and Grok packages: unchanged. Their "assets equal to plugin/bin's" checks compare `VERSION`,
  `checksums.txt` and `brigade`; `brigade.exe` is the Claude Code package's alone.
- Version: 0.41.0 (a new platform is a minor).

### 3.4 Documentation

Install, join, update, configure and leave pages: a Windows section each, or one Windows page named from a
`Details:` line, with "Git for Windows; Syncthing on PATH if the project syncs folders"; `docs/security.md`: the
Windows paragraph (the profile-directory ruling); the setup and join skills: one line each so a session can tell
a Windows user; the changelog section; `docs/experiments/E11-windows-hook-resolution.md` (step 0) and the
native-Windows proof report.

## 4. Work breakdown (execution-log rows P61-*)

| Id | Work | State |
| --- | --- | --- |
| P61-1 | Step 0: the hook-resolution experiment on a Windows runner; report E11 | done 2026-10-27: GO (run 40571499492, Claude Code 2.1.311) |
| P61-2 | The launcher (`cmd/brigade-launcher`), its reproducibility check, the bootstrap's Git Bash arm | done 2026-10-27 |
| P61-3 | `procutil`, `adapterkit` and the thirteen checks on Windows | done 2026-10-27 (one commit, P61-3 row) |
| P61-4 | The harness on Windows: delivery through the named pipe, spawns, watcher, sink, session map | done 2026-10-27 (one commit, P61-3 row) |
| P61-5 | The Syncthing engine on Windows | done 2026-10-27 (one commit, P61-3 row) |
| P61-6 | Build, lint and the Windows CI job | done 2026-10-27 (one commit, P61-3 row) |
| P61-7 | Release targets, package checks, documentation, skills | |
| P61-8 | The Windows VM proof of the Claude Desktop Code tab; report | CLI half done 2026-10-27 (E12); the Code tab waits for Rjae at the VM window |
| P61-9 | Release 0.41.0; card closed | 0.41.0 released 2026-10-27 (v0.41.0, cf35698); the card closes after the Desktop step |
