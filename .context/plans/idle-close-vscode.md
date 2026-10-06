# Close idle VS Code extension sessions (card 61)

Rjae, 2026-10-06. Brief for the change; the execution log row is P30-7.

## 1. The problem

Teammates on the Claude Code VS Code extension accumulate `idle` rows on the roster. Closing a conversation in the
extension leaves its `claude` process running (the conversation stays resumable) and fires no hook, so the Brigade
watcher keeps heartbeating a session nobody returns to: `SEEN 20s`, state `idle`, for days. Each such session also
keeps the backend timers running — about 480 requests an hour at the 0.26.0 defaults, the quota card 53 was
about. The CLI never shows this: `/exit` ends the process, and the watcher's liveness poll closes the session within
one tick of the process going.

Rjae's rulings: the teammates will not change their habits; apply a threshold only to VS Code sessions; six hours;
undocumented host detection is acceptable because the tests pin it.

## 2. The host tag (measured 2026-10-06)

- The extension's bundle (`anthropic.claude-code` 2.1.204 and 2.1.220 on disk) builds the spawned process's
  environment from its own, applies the user's `environmentVariables` setting, then sets
  `CLAUDE_CODE_ENTRYPOINT="claude-vscode"` unconditionally and deletes `CLAUDECODE` and `CLAUDE_CODE_CHILD_SESSION`.
- Claude Code 2.1.291 keeps an inherited value and branches on `claude-vscode` itself. Launched with that variable
  (`-p`, an unservable model name, a directory with no team), a SessionStart hook saw
  `CLAUDE_CODE_ENTRYPOINT=claude-vscode` and `$CLAUDE_CONFIG_DIR/sessions/<pid>.json` carried
  `"entrypoint":"claude-vscode"`, `"kind":"interactive"`. Hook stdin carries no host member.
- The CLI in any terminal, VS Code's integrated terminal included, reads `cli`; `-p` reads `sdk-cli`. The binary also
  knows `jetbrains`, `claude-desktop`, `cowork`, `remote*`, `sdk-ts`, `sdk-py`.
- None of this is in the official docs. The gate therefore fails safe: an unknown or missing value is never closed.

## 3. Design

- **Setting.** `idle_close_hours` inside the team file's `polling` member: a whole number of hours, 0 to 168, 0
  switching the close off, absent meaning the default of 6. It rides the paths `polling` already has: never a
  refusal (an unusable member leaves every default), carried by `team create --force`, frozen into the by-pid map by
  SessionStart, a respawn reason on the continue path, one start line when set.
- **The map** carries two new members: `entrypoint` (the hook's `CLAUDE_CODE_ENTRYPOINT`, else the registry's) and
  `idle_close_hours` (a pointer: nil is the default, 0 is off). `ByPID.IdleClose()` is the duration the watcher
  applies: zero unless the entrypoint is a host that keeps its process after the conversation is closed
  (`polling.IdleCloseHost`: `claude-vscode` today, a set so `jetbrains` can join once measured).
- **The watcher** keeps `lastActive`, started at its own start, and on every liveness tick (2 s) sets it to now when
  the registry says busy or the transcript's mtime changed since the last tick. When `now - lastActive` reaches
  the threshold it stops with reason `idle_expired`; the exit path closes the session (the clean path, one
  `session close`, so the session is offline at once), removes the pidfile and leaves the map, as every stop does.
- **Return.** The next prompt's hook finds no watcher and respawns one (`ensureWatcher`); its first heartbeat is
  answered `session_closed` and the P10-7 re-open registers with `resume.session_id`, so the session is back under
  its id with its waiting messages within seconds. A re-opened watcher's clock starts at its start. A window reload
  is a `resume` SessionStart, which the by-native map already maps to the same id.
- **What is not done.** No notice at the re-opening prompt (silent). No roster column. No protocol change. CLI and
  `-p` sessions are never closed this way.

## 4. Costs

A quiet but open VS Code conversation leaves the roster after the threshold; a message sent to it then waits at the
backend until the next prompt instead of waking it, and folder sync for that session pauses until the next prompt
(the watcher drives it). A team that hands work to unattended VS Code sessions raises the number in its team file.

## 5. Tests

- `polling`: bounds, default, the host set.
- `teamfile`: every rule of `idle_close_hours` both ways, the rule order, carry on `team create --force`.
- `sessionmap`: round trip, omission, validation of both members, `IdleClose()` by host and value.
- `hook`: the map carries the entrypoint from the environment (else the registry) and the hours; a changed value
  respawns the watcher on the continue path; the start line names it.
- `watch`: an eligible session past the threshold closes with reason `idle_expired` (store closed, pidfile gone,
  map kept); `cli`, `sdk-cli` and an empty entrypoint never close; busy status and a transcript write each reset
  the clock; 0 switches it off. A test clock that jumps forward drives the hours.
