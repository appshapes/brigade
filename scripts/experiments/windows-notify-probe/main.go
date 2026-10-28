//go:build windows

// Command windows-notify-probe measures, on a real Windows machine, the two in-process routes card 153 wants
// for the message sound and the desktop notification (.context/plans/windows-notifications.md, report E13):
// winmm's PlaySound for the sound, and a notification-area balloon through Shell_NotifyIconW for the
// notification, which Windows 10 and 11 render as a toast. It is run by hand from a clone (`go build
// ./scripts/experiments/windows-notify-probe`), one measurement per invocation, and writes what it did and what
// each call returned to stderr and to notify-probe.log under TEMP; a person at the screen reports what appeared.
//
//	probe sound [alias <name> | file <path>] [-volume <0..65535>]   play once, synchronously
//	probe balloon [-keep <seconds>] [-aumid <id>] [-title <t>] [-text <t>]
//	    a message-only window, an icon in the notification area (the stock information icon), one balloon with
//	    NIIF_INFO | NIIF_NOSOUND | NIIF_RESPECT_QUIET_TIME; the icon stays -keep seconds (default 12) and is then
//	    deleted. -aumid calls SetCurrentProcessExplicitAppUserModelID first, to see whether the toast's header
//	    changes for a process that names one.
package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	shell32  = windows.NewLazySystemDLL("shell32.dll")
	winmm    = windows.NewLazySystemDLL("winmm.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")

	procRegisterClassExW = user32.NewProc("RegisterClassExW")
	procCreateWindowExW  = user32.NewProc("CreateWindowExW")
	procDefWindowProcW   = user32.NewProc("DefWindowProcW")
	procDestroyWindow    = user32.NewProc("DestroyWindow")
	procPeekMessageW     = user32.NewProc("PeekMessageW")
	procTranslateMessage = user32.NewProc("TranslateMessage")
	procDispatchMessageW = user32.NewProc("DispatchMessageW")
	procLoadIconW        = user32.NewProc("LoadIconW")
	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procSetProcessAUMID  = shell32.NewProc("SetCurrentProcessExplicitAppUserModelID")
	procPlaySoundW       = winmm.NewProc("PlaySoundW")
	procWaveOutSetVolume = winmm.NewProc("waveOutSetVolume")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")
)

// Shell_NotifyIcon messages and flags (shellapi.h).
const (
	nimAdd        = 0x0
	nimModify     = 0x1
	nimDelete     = 0x2
	nimSetVersion = 0x4

	nifMessage = 0x01
	nifIcon    = 0x02
	nifTip     = 0x04
	nifInfo    = 0x10

	niifInfo             = 0x01
	niifNoSound          = 0x10
	niifRespectQuietTime = 0x80

	notifyIconVersion4 = 4

	hwndMessage    = ^uintptr(2) // HWND_MESSAGE, (HWND)-3
	wmUser         = 0x0400
	pmRemove       = 0x0001
	idiInformation = 32516

	sndSync      = 0x0000
	sndNoDefault = 0x0002
	sndFilename  = 0x00020000
	sndAlias     = 0x00010000
)

// notifyIconData is NOTIFYICONDATAW for a 64-bit process: 976 bytes.
type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GUIDItem         windows.GUID
	HBalloonIcon     uintptr
}

// wndClassExW is WNDCLASSEXW.
type wndClassExW struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

// msg is MSG, with the private field the current SDK adds.
type msg struct {
	HWnd    uintptr
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
	Private uint32
}

var logFile *os.File

func say(format string, args ...any) {
	line := fmt.Sprintf(format, args...)
	_, _ = fmt.Fprintln(os.Stderr, line)
	if logFile != nil {
		_, _ = logFile.WriteString(time.Now().UTC().Format(time.RFC3339Nano) + " " + line + "\n")
	}
}

func main() {
	dir := os.TempDir()
	f, err := os.OpenFile(filepath.Join(dir, "notify-probe.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) //nolint:gosec // G304: a fixed name under the temp directory
	if err == nil {
		logFile = f
		defer f.Close()
	}
	args := os.Args[1:]
	if len(args) == 0 {
		say("usage: probe sound|balloon ...")
		return
	}
	say("probe %s %q (pid %d)", args[0], args[1:], os.Getpid())
	switch args[0] {
	case "sound":
		err = sound(args[1:])
	case "balloon":
		err = balloon(args[1:])
	default:
		err = errors.New("unknown measurement " + args[0])
	}
	if err != nil {
		say("FAILED: %v", err)
		return
	}
	say("done")
}

func utf16(s string) *uint16 {
	p, err := windows.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

func fill(dst []uint16, s string) {
	u := windows.StringToUTF16(s)
	if len(u) > len(dst) {
		u = u[:len(dst)-1]
		u = append(u, 0)
	}
	copy(dst, u)
}

// sound plays once, synchronously, so the process lives for the sound's length: an alias (an event under
// HKCU\AppEvents\Schemes\Apps\.Default, "Notification.Default" by default) or a file, after setting this
// process's wave volume when -volume is given (0xFFFF is full; 0x4000 is about a quarter).
func sound(args []string) error {
	kind, name, volume := "alias", "Notification.Default", -1
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "alias", "file":
			if i+1 >= len(args) {
				return errors.New(args[i] + " needs a name")
			}
			kind, name = args[i], args[i+1]
			i++
		case "-volume":
			if i+1 >= len(args) {
				return errors.New("-volume needs a number")
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil || v < 0 || v > 0xFFFF {
				return errors.New("-volume takes 0..65535")
			}
			volume = v
			i++
		default:
			return errors.New("unknown sound argument " + args[i])
		}
	}
	if volume >= 0 {
		both := uint32(volume) | uint32(volume)<<16 //nolint:gosec // G115: bounded above
		r, _, e := procWaveOutSetVolume.Call(0, uintptr(both))
		say("waveOutSetVolume(WAVE_MAPPER, %#x) = %d (%v)", both, r, e)
	}
	flags := uintptr(sndSync | sndNoDefault)
	if kind == "alias" {
		flags |= sndAlias
	} else {
		flags |= sndFilename
	}
	start := time.Now()
	r, _, e := procPlaySoundW.Call(uintptr(unsafe.Pointer(utf16(name))), 0, flags) //nolint:gosec // G103: a Win32 call takes the string's address
	say("PlaySoundW(%s %q, flags %#x) = %d (%v) after %s", kind, name, flags, r, e, time.Since(start).Round(time.Millisecond))
	if r == 0 {
		return errors.New("PlaySound returned FALSE: no such alias or file, or no device")
	}
	return nil
}

// balloon shows one notification-area balloon from a message-only window and keeps the icon -keep seconds.
func balloon(args []string) error {
	keep, aumid := 12, ""
	title, text := "Brigade", "A message arrived for 153-collaborator."
	for i := 0; i < len(args); i++ {
		if i+1 >= len(args) {
			return errors.New(args[i] + " needs a value")
		}
		switch args[i] {
		case "-keep":
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n < 0 || n > 600 {
				return errors.New("-keep takes 0..600 seconds")
			}
			keep = n
		case "-aumid":
			aumid = args[i+1]
		case "-title":
			title = args[i+1]
		case "-text":
			text = args[i+1]
		default:
			return errors.New("unknown balloon argument " + args[i])
		}
		i++
	}
	if aumid != "" {
		r, _, e := procSetProcessAUMID.Call(uintptr(unsafe.Pointer(utf16(aumid)))) //nolint:gosec // G103: a Win32 call takes the string's address
		say("SetCurrentProcessExplicitAppUserModelID(%q) = %#x (%v)", aumid, r, e)
	}
	// The window and the icon belong to this thread; keep it.
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	className := utf16("BrigadeNotifyProbe")
	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   procDefWindowProcW.Addr(),
		HInstance:     hInstance,
		LpszClassName: className,
	}
	atom, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))) //nolint:gosec // G103: a Win32 call takes the struct's address
	say("RegisterClassExW = %d (%v)", atom, e)
	if atom == 0 {
		return errors.New("RegisterClassExW failed")
	}
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(utf16("brigade-notify"))), //nolint:gosec // G103: Win32 takes the strings' addresses
		0, 0, 0, 0, 0, hwndMessage, 0, hInstance, 0)
	say("CreateWindowExW(HWND_MESSAGE) = %#x (%v)", hwnd, e)
	if hwnd == 0 {
		return errors.New("CreateWindowExW failed")
	}
	defer func() {
		r, _, _ := procDestroyWindow.Call(hwnd)
		say("DestroyWindow = %d", r)
	}()
	icon, _, e := procLoadIconW.Call(0, idiInformation)
	say("LoadIconW(IDI_INFORMATION) = %#x (%v)", icon, e)

	nid := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: hwnd, UID: 1}
	say("NOTIFYICONDATAW size = %d (976 expected on x64)", nid.CbSize)
	nid.UFlags = nifMessage | nifIcon | nifTip
	nid.UCallbackMessage = wmUser + 1
	nid.HIcon = icon
	fill(nid.SzTip[:], "Brigade")
	r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid))) //nolint:gosec // G103: a Win32 call takes the struct's address
	say("Shell_NotifyIconW(NIM_ADD) = %d (%v)", r, e)
	if r == 0 {
		return errors.New("NIM_ADD failed")
	}
	defer func() {
		del := notifyIconData{CbSize: nid.CbSize, HWnd: hwnd, UID: 1}
		r, _, _ := procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&del))) //nolint:gosec // G103: a Win32 call takes the struct's address
		say("Shell_NotifyIconW(NIM_DELETE) = %d", r)
	}()
	nid.UVersion = notifyIconVersion4
	r, _, e = procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(&nid))) //nolint:gosec // G103: a Win32 call takes the struct's address
	say("Shell_NotifyIconW(NIM_SETVERSION 4) = %d (%v)", r, e)

	nid.UFlags = nifInfo
	nid.DwInfoFlags = niifInfo | niifNoSound | niifRespectQuietTime
	fill(nid.SzInfoTitle[:], title)
	fill(nid.SzInfo[:], text)
	shown := time.Now()
	r, _, e = procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(&nid))) //nolint:gosec // G103: a Win32 call takes the struct's address
	say("Shell_NotifyIconW(NIM_MODIFY, NIF_INFO) = %d (%v); keeping the icon %d s", r, e, keep)
	if r == 0 {
		return errors.New("NIM_MODIFY failed")
	}
	pump(time.Duration(keep) * time.Second)
	say("kept %s; deleting the icon and the window", time.Since(shown).Round(time.Millisecond))
	return nil
}

// pump runs this thread's message loop for d, so the shell's callbacks (which DefWindowProc answers) are served.
func pump(d time.Duration) {
	deadline := time.Now().Add(d)
	var m msg
	for time.Now().Before(deadline) {
		for {
			r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove) //nolint:gosec // G103: a Win32 call takes the struct's address
			if r == 0 {
				break
			}
			_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m))) //nolint:gosec // G103: same
			_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m))) //nolint:gosec // G103: same
		}
		time.Sleep(50 * time.Millisecond)
	}
}
