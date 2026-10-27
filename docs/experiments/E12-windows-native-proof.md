# E12 — The brigade binary on native Windows: join, register, deliver both ways, wake through a named pipe, stop

Card 133, plan §1 "the proof". Run 2026-10-27 on a Windows 11 Pro ARM64 virtual machine (build 26300, QEMU
10 on Apple's hypervisor, 8 vCPU, 8 GB; Git for Windows 2.56, Claude Code 2.1.312 installed natively but not
signed in), driven from the macOS release machine over SSH. Binary: a development build of this tree at
`650b1b2` for `windows/arm64` (`brigade-dev.exe`, version `0.41.0-dev`), run through the committed launcher
`plugin/bin/brigade.exe` by the developer pointer, against the real appshapes Supabase project in a test
team, `win133`. Drivers: `.ignored/windows-vm/proof-win3.ps1`, `proof-win4.ps1`, `proof-win5.ps1`,
`pipe-server.ps1` (the Windows side), `proof-mac.sh` (the Mac side), all in the shape of `scripts/proof.sh`:
a sleeper process as `CLAUDE_PID`, the real hooks, the real detached watcher, no model.

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
(`watcher_pid` 4388): the real detached spawn (`DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP | CREATE_NO_WINDOW`),
pidfile `socket_path` the pipe. A PowerShell `NamedPipeServerStream` on that name recorded, **5 s** after the
Mac's send, two lines: `{"type":"auth","token":…}` then `{"type":"user","message":{"role":"user","content":"<cross-session-message …`
with the whole frame. That is what Claude Code consumed as a user turn in E11.

**7. The stop.** `hook session-end` (through WMI, the session's environment) took **1,343 ms**; the watcher's
log reads `closing the session reason=stop`, `watcher exiting exit=0 reason=stop`, and the process was gone
**1,397 ms** after the hook started, the pidfile removed by the hook's compare-then-delete. The stop reached
the watcher through the named event `Local\brigade-stop-<pid>` (procutil.Terminate on Windows), not
TerminateProcess: the session was closed first.

## What this run does not prove

- The Claude Desktop Code tab on Windows and the model's reaction to a delivered frame: a person signs in at
  the VM window and runs the install and join; E11 covered the harness's side of the pipe.
- The launcher's download path: no release carried a Windows asset yet, so the developer pointer stood in.
  The release of 0.41.0 and the marketplace install in the VM measure it.
- Windows on x64: the VM is ARM64 (the amd64 launcher ran under emulation for E11's probe on a GitHub runner,
  and the arm64 binary ran natively here). The CI `windows` job runs the suite on x64.
- Folder sync: Syncthing was not installed in the VM; the engine's Windows code is covered by its unit tests
  and the shape of the stop (REST shutdown, then TerminateProcess), not by a live folder here.
