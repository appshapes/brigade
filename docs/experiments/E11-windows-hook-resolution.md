# E11 — Does Claude Code on native Windows run an exec-form hook through the `.exe` beside the extensionless command?

Card 133, step 0 of `.context/plans/native-windows.md`. Run 2026-10-27 on a GitHub-hosted `windows-latest`
runner (Windows NT 10.0.26100, AMD64, image `win25-vs2026` 20261021.278.1), Claude Code **2.1.311** installed
natively with `irm https://claude.ai/install.ps1 | iex`. Driver: `.github/workflows/experiment-windows-hooks.yml`
(dispatched by hand) and `scripts/experiments/windows-hook-probe`. Run 40571499492; its `windows-hook-probe`
artifact holds the probe log (4.9 MB).

## Why

The native Windows ruling has one condition: no impact on macOS and Linux. The plugin's three hooks are exec form
(`"command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade"` with `args`), and the hooks reference says exec form spawns
the command directly, with no shell, and on Windows needs a real executable. Node's process spawner tries
`<name>.com` and `<name>.exe` for an extensionless path. If Claude Code's spawner does the same, a committed
`plugin/bin/brigade.exe` beside the POSIX bootstrap makes every platform run its own launcher from the hooks file
every platform already uses, and nothing changes for macOS and Linux. If it does not, every alternative changes
the hook path on all three platforms, and D33 stands.

## Method

`plugin/` copied as it ships, hooks file untouched; the probe program built on the runner and copied to
`bin/brigade.exe`; `claude plugin validate` on the copy; then ONE `claude -p` session with `--plugin-dir`,
`--permission-mode bypassPermissions` and `--max-turns 12`, whose prompt asked the model to run seven Bash
commands and report them. The probe appends one block per invocation to a log under `RUNNER_TEMP`: argv, the
working directory, its own executable path, stdin, the environment (secret-looking values redacted), the Claude
Code session registry's file names, a stat of `CLAUDE_CODE_MESSAGING_SOCKET`, and, from the `hook prompt`
invocation, the result of writing the auth frame and one user frame into that socket.

## Results

**1. GO: the SessionStart hook ran `bin/brigade.exe` through the extensionless command.** The first block of the
log, 1.6 s after the session started:

```
argv: D:\a\_temp\plugin/bin/brigade hook session-start
cwd: D:\a\brigade\brigade
executable: D:\a\_temp\plugin\bin\brigade.exe
stdin: {"session_id":"1acf0e54-…","transcript_path":"C:\\Users\\runneradmin\\.claude\\projects\\D--a-brigade-brigade\\1acf0e54-….jsonl","cwd":"D:\\a\\brigade\\brigade","hook_event_name":"SessionStart","source":"startup"}
```

`argv[0]` is the command as substituted (`${CLAUDE_PLUGIN_ROOT}` with backslashes, the rest of the path with the
forward slash from the hooks file); the executable that ran is the `.exe`. `claude plugin validate` passed on the
copy with one advisory line about the README's install command, nothing about the `bin/` directory.

**2. Delivery through the named pipe works from a hook child, with a plain file open.** The socket is a named
pipe, `CLAUDE_CODE_MESSAGING_SOCKET=\\.\pipe\LOCAL\cc-msg-<32 hex>`, with a 32-byte
`CLAUDE_CODE_MESSAGING_TOKEN`. The `hook prompt` probe opened it with `os.OpenFile(path, O_WRONLY, 0)` (no
`go-winio`, no overlapped I/O) and wrote `{"type":"auth","token":"…"}` then
`{"type":"user","message":{"role":"user","content":"PROBE-DELIVERED <time>"}}`, 150 bytes:

```
post: wrote 150 bytes (auth line then user line)
```

Claude Code delivered every post as a **new user turn**: each one fired `UserPromptSubmit` again with a fresh
`prompt_id`, whose probe posted again, about every 3 s, until the job's 20-minute timeout cancelled the session
(roughly 350 blocks). The loop was the probe's own doing (fixed afterwards: the probe skips a prompt whose text
carries its marker), and it is the strongest evidence this run gives: the pipe accepted the frames, the auth line
authenticated them, and the harness turned each into a prompt. The session's stat of the pipe reports mode
`-rw-rw-rw-`, size 0, which is what Go reports for a pipe handle, not a permission.

**3. The Bash tool is Git Bash, and `brigade.exe` is on its PATH.** One block came from the model's Bash call
`brigade.exe --probe-bash-exe`: `SHELL=C:\Program Files\Git\bin\bash.exe`, `MSYSTEM=UCRT64`,
`HOME=C:\Users\runneradmin`, stdin a character device. The plugin's `bin/` was on that shell's PATH as a bare
command, as on macOS and Linux. The `brigade --probe-bash` step (the POSIX bootstrap under Git Bash) produced no
block, as expected: the bootstrap refuses `uname -s` `MINGW64_NT-…` before anything runs, so the Git Bash arm the
plan adds to the bootstrap (exec `brigade.exe` beside itself) is needed for the Bash tool's bare `brigade`.

**4. The hook environment on Windows.** No `HOME`; `USERPROFILE=C:\Users\runneradmin`, `HOMEDRIVE`, `HOMEPATH`,
`APPDATA`, `LOCALAPPDATA`, `TEMP`/`TMP` present. `CLAUDE_PID=668`, `CLAUDE_CODE_SESSION_ID`,
`CLAUDE_PLUGIN_ROOT=D:\a\_temp\plugin`, `CLAUDE_PLUGIN_DATA=C:\Users\runneradmin\.claude\plugins\data\brigade-inline`,
`CLAUDE_PROJECT_DIR`, `CLAUDE_ENV_FILE=C:\Users\runneradmin\.claude\session-env\<session>\sessionstart-hook-0.sh`
(an sh file even on Windows), `CLAUDE_CODE_ENTRYPOINT=sdk-cli` and `CLAUDE_CODE_CHILD_SESSION=1` (the `-p`
form), `AI_AGENT=claude-code_2-1-311_harness`, `CLAUDECODE=1`. The session registry under
`%USERPROFILE%\.claude\sessions` held `668.json` and `668.<64 hex>.key`, the same two shapes as on macOS.
`PATHEXT=.COM;.EXE;.BAT;.CMD;…`. The image carries Git 2.56.0.windows.2 (bash 5.3.15, Cygwin build), `curl`
8.21.0 with Schannel in `System32`, PowerShell 7.6.7, and Go 1.27.3 from `setup-go`.

## What this run does not prove

- It is the `-p` form on a server image, not the Desktop app's Code tab on a user's machine. The Desktop step is
  the Windows VM proof (plan §1).
- Whether a **detached** process (the watcher, not a hook child) can open the pipe: the probe posted from the hook
  child. The same open with the token from the by-pid map is measured by the Windows CI job on the real binary.
- Delivery timing and the model's reaction: the session never finished its turn, so `session-output.json` was not
  captured. The loop itself shows the frames reached the harness; nothing here measures how the model reads them.
- Windows on ARM: the runner is AMD64. The arm64 binary and the amd64 launcher under emulation are measured on
  the ARM VM.
