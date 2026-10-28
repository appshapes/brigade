# Windows-native notifications (Trello card 153)

Owner: Rjae. Session: 153-windows-native-notifications (macOS, builds), with 153-collaborator (Windows,
measures and tests). Started 2026-10-28. Rows P62-* in brigade-execution-log.md.

## 1. The ask

The `message_sound` and `message_notification` member options (cards 35, 36; interval card 38) work on native
Windows as they do on macOS and Linux: one quiet sound, one desktop notification naming the session and the
count, nothing from the message, once per interval. No new option; the defaults stay off (owner ruling
2026-10-06: defaults stay, knobs opt-in). Released as 0.42.0.

## 2. Design (card 153 and its 2026-10-28 comment)

- **Sound, in process.** `notify_windows.go`: `PlaySound` in `winmm.dll` (x/sys/windows lazy DLL, no cgo), the
  system notification sound, asynchronous, at about a quarter volume through `waveOutSetVolume` on this process.
  No child, no window.
- **Desktop notification, a notification-area balloon.** `Shell_NotifyIconW` with `NIF_INFO`, which Windows 10
  and 11 render as a toast: a message-only window on a goroutine locked to its OS thread that pumps messages,
  `NIM_ADD` (stock information icon or Brigade's), `NIM_SETVERSION` 4, `NIM_MODIFY` with `NIF_INFO`,
  `NIIF_INFO | NIIF_NOSOUND | NIIF_RESPECT_QUIET_TIME`, title 64 and text 256 characters. No PowerShell, no
  AppUserModelID in the registry. Fallback if the measurement disappoints: the PowerShell WinRT toast under
  PowerShell's own ID, title and text in environment variables.
- **In the watcher.** `notify.Sound`/`Banner` answer Windows with an in-process descriptor instead of a path
  (`notify.InProcess`), and the announcer runs `notify.RunInProcess` for it instead of the spawn seam; the hook's
  SessionStart check is unchanged in shape.
- **Measured before coding the balloon** (E13, by 153-collaborator): how a balloon renders on that Windows 11
  build and what header it carries for a Go exe with no version resource; whether deleting the icon dismisses
  it (transient icon per notification vs. one per watcher); Focus Assist; PlaySound's alias and volume.

## 3. Steps

| Step | What | State |
| --- | --- | --- |
| P62-1 | Measurements on the Windows machine: PowerShell balloon first, then the Go probe `scripts/experiments/windows-notify-probe` (E13) | started 2026-10-28 |
| P62-2 | `notify_windows.go` (sound, balloon), the in-process seam in `watch/announce.go`, tests (`notify_test.go` windows cases, `notify_windows_test.go`), docs (options.md rows, install/windows.md, security.md sentence, changelog) | |
| P62-3 | Live test of the dev build by 153-collaborator (developer pointer), then release 0.42.0 and confirm on the release build; close the card | |
