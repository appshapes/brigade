# E13 — Windows-native notifications: the sound in process and the notification-area balloon, measured

Card 153, plan `.context/plans/windows-notifications.md` §2 (P62-1). Run 2026-10-28 on the x64 Windows 11
machine of the card 133 proof (Windows 11 Pro 25H2, build 26200.9711, Intel i7-8750H, three displays, the
primary 1920×1080 at 125 %), by a Brigade session of the `brigade` team on that machine (Claude Code in Windows
Terminal, plugin 0.41.1), run unattended on its user's authorisation: "seen" is screenshots of the primary
display at fixed offsets, a WinRT `UserNotificationListener` listing of the notification centre and one Win+N
screenshot; "heard" is a WASAPI loopback capture of the default render endpoint (peak |sample|, float32 48 kHz
stereo, master volume 25 %, not muted). Driver: `scripts/experiments/windows-notify-probe` (commit `5075b23`),
built there with Go 1.27.3 (installed under the user profile from the official zip, SHA-256 verified; the machine
had no Go). Numbers as measured by that session; this report quotes its lines.

## Why

The design on the card puts both options in the watcher's own process, never a child: the sound through winmm's
`PlaySound`, the notification as a `Shell_NotifyIcon` balloon, which Windows 10 and 11 render as a toast. Two
things could not be known from documentation: what header a balloon from a Go exe with no version resource
carries, and whether the icon must stay for the toast to live. The volume scale of `waveOutSetVolume` was a
third.

## Results

**1. A balloon from PowerShell's `NotifyIcon` (`ShowBalloonTip`, 10 000 ms, icon held 12 s).** A Windows 11
toast at the bottom right of the primary display: header "Windows PowerShell" with the small blue circled-i
(the `NotifyIcon`'s information icon) at its left, no icon in the body, "Brigade" / "A message arrived for
153-collaborator.", the "..." and "x" controls. On screen at 0.3, 2, 4 and 6.2 s, gone at 8.2 s (the
10 000 ms argument is ignored, as documented; the accessibility message duration is the 5 s default). The
listener lists it only while it is on screen (app "Windows PowerShell", an auto-generated
`NotifyIconGeneratedAumid_…`); not at 9 s with the icon still alive, nor after `Dispose`; the Win+N panel held
only two older OneDrive notifications. **The notification centre keeps no balloon.** Disposing the icon 1.03 s
after showing: the toast stayed at 1.5, 3 and 6 s and left on its own at the same time as before.

**2. Sound facts.** `C:\Windows\Media\Windows Notify System Generic.wav` exists (248 092 bytes), with
`notify.wav`, `Windows Notify Calendar.wav`, `Windows Notify Email.wav`, `Windows Notify Messaging.wav` and
`Windows Notify.wav` beside it. The `.Default` scheme maps `Notification.Default` to that file and
`SystemAsterisk` to `Windows Background.wav`. `[System.Media.SystemSounds]::Asterisk.Play()` rendered (loopback
peak 0.32, 0.63 s to 1.80 s after launch). Do not disturb was off in effect: every banner appeared.

**3. The probe's sound runs** (`PlaySoundW` returned 1 each time, synchronous, 1.2 s of audio):

| run | volume | peak | note |
| --- | --- | --- | --- |
| `sound` (alias `Notification.Default`) | untouched | 0.475 | 0.45 s to 1.68 s after launch |
| `sound alias Notification.Default -volume 16384` | `waveOutSetVolume(WAVE_MAPPER, 0x40004000)` = 0 | 0.199 | 42 % of the full amplitude, about −7.5 dB |
| `sound file "…\Windows Notify System Generic.wav" -volume 16384` | the same | 0.199 | identical to the alias: the alias is that file |

The waveOut volume scale is tapered, not linear: 0x4000 of 0xFFFF is 42 % amplitude, not a quarter. By that
curve (amplitude ≈ (v/0xFFFF)^0.63), a quarter is near 0x2000, which the implementation uses; the live test
confirms it by ear.

**4. The probe's balloon runs** (`RegisterClassExW` 49818, `CreateWindowExW(HWND_MESSAGE)` a handle,
`NOTIFYICONDATAW` 976 bytes, `NIM_ADD` 1, `NIM_SETVERSION 4` 1, `NIM_MODIFY` with `NIF_INFO` 1, `NIM_DELETE` 1):

- **`balloon` (icon kept 12 s).** A toast at the bottom right: **header "notify-probe.exe"**, the exe's file
  name, with the small blue circled-i at its left; body: the large blue circled-i (`NIIF_INFO`), "Brigade",
  "A message arrived for 153-collaborator."; "..." and "x". On screen at 0.5, 3 and 6.1 s after the process
  started, gone at 8.1 s. **No sound** (loopback peak 0 through the first 3 s): `NIIF_NOSOUND` is honoured. The
  listener at 3 s: app "notify-probe.exe", an auto-generated AUMID, the title and the text; at 14 s (after
  `NIM_DELETE`) and after exit: not listed. Not in the notification centre afterwards.
- **`balloon -keep 1`** (icon deleted 1.01 s after showing): the toast on screen at 0.5, 3 and 6 s, gone at 8 s.
  **Deleting the icon early cuts nothing short**, and the centre never had it.
- **`balloon -aumid AppShapes.Brigade`** (`SetCurrentProcessExplicitAppUserModelID` = S_OK): **the header is the
  AUMID string itself**, "AppShapes.Brigade", with no icon in the header (an unregistered AUMID has no display
  name and no logo); the body unchanged; the listener reports an empty app and AUMID. `DestroyWindow` returned 0
  on this run alone.

## What the implementation takes from it

- The watcher names its AppUserModelID "Brigade" before creating the window, so the toast's header reads
  Brigade rather than the cached binary's file name (`brigade-<version>-windows-<arch>.exe`); the price is no
  small icon in the header, and the toast is not kept in the notification centre either way.
- The icon lingers 2 s after a showing, not 20: nothing depends on it once the toast is up, and a watcher that
  is killed inside those seconds leaves at most a ghost icon the shell drops on the next hover.
- `soundVolume` 0x2000. `NIIF_NOSOUND` keeps the toast silent so the sound is Brigade's one sound.
- The notifications page says the toast shows for about six seconds and is not kept in the notification
  centre, which is what this build does.

## What this run does not prove

- The watcher itself showing and playing on a message: the live test of the dev build through the developer
  pointer, with both options on, follows (P62-3), then the release build.
- Windows 10, and Windows on ARM: measured on Windows 11 25H2 x64 only; the calls are the same.
- A person's ear: "heard" here is a loopback capture; the live test asks the person at the machine.
