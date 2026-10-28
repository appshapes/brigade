# E12 — The brigade binary on native Windows: join, register, deliver both ways, wake through a named pipe, stop

Card 133, plan §1 "the proof". Run 2026-10-27 on a Windows 11 Pro ARM64 virtual machine (build 26300, QEMU
10 on Apple's hypervisor, 8 vCPU, 8 GB; Git for Windows 2.56, Claude Code 2.1.312 installed natively but not
signed in), driven from the macOS release machine over SSH. Binary: a development build of this tree at
`650b1b2` for `windows/arm64` (`brigade-dev.exe`, version `0.41.0-dev`), run through the committed launcher
`plugin/bin/brigade.exe` by the developer pointer, against the real appshapes Supabase project in a test
team, `win133`. Drivers: `scripts/experiments/windows-native-proof/` (`proof-win3.ps1`, `proof-win4.ps1`,
`proof-win5.ps1` and `pipe-server.ps1` on the Windows side, `proof-mac.sh` on the Mac side), all in the shape of
`scripts/proof.sh`: a sleeper process as `CLAUDE_PID`, the real hooks, the real detached watcher, no model.

## Why

E11 showed Claude Code on Windows runs the plugin's hooks through `bin/brigade.exe` and delivers what a
process writes into its named pipe. This run shows the brigade binary itself does its work on Windows: the
Supabase adapter over TLS, the session registration, the detached watcher, delivery in both directions
between a Windows session and a macOS one, the watcher's post into a named pipe, and the stop. Nothing here
needs a sign-in; the Claude Desktop Code tab is the step after it (plan §1), with a person at the VM window.

## Results

**1. The launcher chain.** `plugin\bin\brigade.exe version` prints `0.41.0-dev` through the developer pointer
at `%APPDATA%\brigade\dev-binary`; under Git Bash (`uname -s` = `MINGW64_NT-10.0-26300-ARM64`, an x86_64 MSYS
under emulation), the POSIX bootstrap `plugin/bin/brigade version` prints the same, having exec'd the launcher
beside itself. `claude plugin validate` passes on the checkout. A first run of the launcher refused with "no
sha256 for brigade_0.40.0_windows_arm64.exe": it read the checksums line before the pointer, where the
bootstrap reads the pointer first; fixed in `650b1b2` with a regression test.

**2. Join over TLS.** In the session shape (`CLAUDE_PID` set), `brigade team join --secret-file
C:\Users\brigade\win133.secret` in `C:\src\win133` answered `joined team "win133"`; `team status` reports the
team key, `joined`, a token expiry; the store is `%APPDATA%\brigade\teams\<key>\{team,session}.json` and the pin
in `%APPDATA%\brigade\projects.json` under `C:\src\win133`. A non-TTY join outside a session takes the
scripted pass-through, as on macOS (not a Windows difference; the first attempt over plain SSH hit it).

**3. Registration.** `hook session-start` (stdin JSON with `cwd`) printed `Brigade: this session is "win133"
(<id>) in team "win133"; inbound: accept …`; `whoami` shows `terminal: C:\src\brigade\plugin\bin\brigade.exe`;
`brigade sessions` from the Windows session lists it on the roster, member `[9c1af5be]`, version `0.41.0-dev`.

**4. The watcher, and the host-process check.** A sink-mode watcher (`watch --sink`) reached `watch ready`
(mode push), wrote `%LOCALAPPDATA%\brigade\watchers\<pid>.json` with a start token (the process creation
FILETIME) and heartbeats. When the first rig's SSH session ended and Windows killed the sleeper standing in for
Claude Code, the watcher exited with reason `claude_gone`, closed the session and released its pidfile: the
liveness check through OpenProcess and GetExitCodeProcess, at work. The rig then created its processes through
WMI so they outlive the SSH session.

**5. Delivery both ways.** From a fake macOS session in the same team (`proof-mac.sh`, `0.40.0-dev` of the
same tree): `brigade send <windows session>` → the Windows watcher's sink carried the full frame
(`message-id`, `reply-to-session-id`, the sender summary and body) **6 s** after the send. From the Windows
session: `brigade send <mac session>` (body on stdin, `cmd /c … <` ) answered `accepted … recipient: … idle,
inbound accept`, and the macOS sink carried the frame **7 s** later, `hops="1"`.

**6. The wake path.** A second Windows session with `CLAUDE_CODE_MESSAGING_SOCKET=\\.\pipe\LOCAL\cc-msg-brigade133`
and a token: `hook session-start` (run through WMI) registered it and logged `watcher started`
(`watcher_pid` 4388): the real detached spawn (`DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`;
the flags 0.41.1 corrected, below),
pidfile `socket_path` the pipe. A PowerShell `NamedPipeServerStream` on that name recorded, **5 s** after the
Mac's send, two lines: `{"type":"auth","token":…}` then `{"type":"user","message":{"role":"user","content":"<cross-session-message …`
with the whole frame. That is what Claude Code consumed as a user turn in E11.

**7. The stop.** `hook session-end` (through WMI, the session's environment) took **1,343 ms**; the watcher's
log reads `closing the session reason=stop`, `watcher exiting exit=0 reason=stop`, and the process was gone
**1,397 ms** after the hook started, the pidfile removed by the hook's compare-then-delete. The stop reached
the watcher through the named event `Local\brigade-stop-<pid>` (procutil.Terminate on Windows), not
TerminateProcess: the session was closed first.

## The first real machine: x64, Claude Code in Windows Terminal (2026-10-28)

The day after 0.41.0, a native Windows session of the `brigade` team itself (Windows 11 Pro 10.0.26200 on x64;
Claude Code started from cmd.exe inside Windows Terminal 1.24; plugin 0.41.0 from the marketplace; the Bash
tool Git Bash) joined with `/brigade:join`, sent a message with `brigade send` to the macOS session that built
the card, and received that session's reply three minutes later: delivery both ways between two real Claude
Code sessions, a model reading each frame. Two bugs on the way, both reproduced there and pinned to the lines,
fixed in 0.41.1:

1. **The launcher's curl could resolve no host under Git Bash.** `brigade team join` from the Bash tool
   failed with `curl: (6) Could not resolve host: github.com` four times, while the machine resolved the name
   and both curls answered 200 from the same shell. `curlFetch` matched the variables it passes to `curl.exe`
   by exact case (`SystemRoot`, `windir`), and Git Bash spells them `SYSTEMROOT` and `WINDIR`; without a system
   root, Windows' own curl cannot resolve anything (reproduced there with System32's curl and a cleared
   environment: `TEMP` and `TMP` alone exit 6, with `SystemRoot` 200). The SessionStart hook's download had
   failed the same way at `/reload-plugins`, so the join then met "this session has no start facts" until a
   second reload with the binary cached (the launcher had been run once from PowerShell, where the names keep
   their case). 0.41.1 matches the names without regard to case, passes each under the parent's spelling and
   supplies `SystemRoot` when the parent has none; the launcher is rebuilt.
2. **A console window from the watcher.** At the first prompt after the join a terminal window appeared,
   titled with the cached binary's path, hosted by Windows Terminal as the default terminal application, and
   stayed; closing it ended the adapter, which the watcher restarted, window and all. The watcher was started
   with `DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`; Windows ignores `CREATE_NO_WINDOW`
   beside `DETACHED_PROCESS`, so the watcher had no console, and its adapter child, a console program started
   with no flags, got a fresh, visible one (the live process tree: the adapter with its own `conhost.exe`
   child and a visible top-level window). The rigs above started everything through WMI with a hidden window,
   and nobody watched the VM's desktop, which is how it passed. 0.41.1's `procutil.DetachAttr` is
   `CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`: the watcher, the Syncthing daemon and the launcher's download
   worker own a hidden console that their children inherit.

## What this run does not prove

- The Claude Desktop Code tab on Windows: the real machine above ran Claude Code in a terminal, which proves
  the model's reaction to a delivered frame on native Windows; E11 covered the harness's side of the pipe.
- ~~The launcher's download path~~: measured after 0.41.0 was published, with the pointer removed and the
  cache empty: `plugin\bin\brigade.exe version` printed `brigade: first use: downloading brigade 0.41.0 for
  windows/arm64 from https://github.com/appshapes/brigade/releases/download` and then `0.41.0`, **4.6 s** in
  all; the cached `brigade-0.41.0-windows-arm64.exe` (9,183,232 bytes) has the sha256 of the committed line;
  the warm cache answers in **0.3 s**; the Git Bash bootstrap prints `0.41.0` through the same cache.
- ~~Windows on x64~~: the VM is ARM64; the real machine above is x64, with the amd64 launcher and binary.
- Folder sync: Syncthing was not installed in the VM; the engine's Windows code is covered by its unit tests
  and the shape of the stop (REST shutdown, then TerminateProcess), not by a live folder here.
