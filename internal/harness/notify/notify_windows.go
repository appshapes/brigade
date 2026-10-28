//go:build windows

package notify

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Card 153: on Windows the sound and the notification are calls into
// Windows' own libraries from this process, never a child. The sound is
// the system's notification event through winmm's PlaySound, played
// asynchronously at about a quarter of this process's wave volume. The
// notification is a notification-area balloon: a message-only window on
// a thread of this process owns an icon, and each showing is NIM_MODIFY
// with NIF_INFO, which Windows 10 and 11 render as a toast that respects
// quiet hours. Measured on Windows 11 25H2 (E13): the toast shows for
// six to eight seconds and is never kept in the notification centre,
// deleting the icon early cuts nothing short, and the toast's header is
// the exe's file name unless the process names an AppUserModelID, in
// which case it is that string; so the thread names "Brigade" before it
// creates the window, and the icon is added for a showing and removed
// iconLinger later, so a session nobody messages shows nothing in the
// notification area; a watcher stopping cleanly removes it at once
// (Close).

var (
	user32  = windows.NewLazySystemDLL("user32.dll")
	shell32 = windows.NewLazySystemDLL("shell32.dll")
	winmm   = windows.NewLazySystemDLL("winmm.dll")

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
	procGetModuleHandleW = windows.NewLazySystemDLL("kernel32.dll").NewProc("GetModuleHandleW")
	procPlaySoundW       = winmm.NewProc("PlaySoundW")
	procWaveOutSetVolume = winmm.NewProc("waveOutSetVolume")
)

// The sound: the notification event of the user's sound scheme
// (HKCU\AppEvents\Schemes\Apps\.Default\Notification.Default), and the
// file Windows ships for it when the scheme has none. The volume is of
// 0xFFFF per channel on waveOut's tapered scale: 0x4000 measured 42 % of
// the full amplitude (E13), so a quarter, afplay's -v 0.25, is near
// 0x2000.
const (
	soundAlias  = "Notification.Default"
	soundFile   = `Media\Windows Notify System Generic.wav`
	soundVolume = 0x2000

	sndAsync     = 0x00000001
	sndNoDefault = 0x00000002
	sndAlias     = 0x00010000
	sndFilename  = 0x00020000
	waveMapper   = ^uintptr(0) // WAVE_MAPPER, (UINT)-1
)

// The balloon: Shell_NotifyIcon's messages and flags (shellapi.h), the
// field lengths of NOTIFYICONDATAW, and the icon's life after a showing.
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
	hwndMessage        = ^uintptr(2) // HWND_MESSAGE, (HWND)-3
	wmUser             = 0x0400
	pmRemove           = 0x0001
	idiInformation     = 32516

	balloonTitleMax = 63  // szInfoTitle holds 64 UTF-16 units with the terminator
	balloonTextMax  = 255 // szInfo holds 256
	iconLinger      = 2 * time.Second
	trayPoll        = 100 * time.Millisecond
	className       = "BrigadeNotify"
	// appUserModelID is what the toast's header reads: a process without
	// one is named by its exe's file name, the cached
	// brigade-<version>-windows-<arch>.exe here (E13).
	appUserModelID = "Brigade"
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

// RunInProcess runs a Windows descriptor: the sound, or the balloon with
// the title and the text BannerArgv put after it. Anything else is an
// error, as a program's path would be here.
func RunInProcess(ctx context.Context, argv []string) error {
	if len(argv) == 1 && argv[0] == WindowsSound {
		return playSound()
	}
	if len(argv) != 3 || argv[0] != WindowsBanner {
		return errors.New("notify: not a Windows descriptor")
	}
	title, text := argv[1], argv[2] //nolint:gosec // G602: three elements, checked on the line above
	return showBalloon(ctx, title, text)
}

var volumeOnce sync.Once

// playSound plays the notification event once, asynchronously (winmm's
// own thread plays it; this process lives on), after setting this
// process's wave volume once. The scheme's event first; the file Windows
// ships for it when the scheme has the event unset.
func playSound() error {
	volumeOnce.Do(func() {
		_, _, _ = procWaveOutSetVolume.Call(waveMapper, uintptr(uint32(soundVolume)|uint32(soundVolume)<<16))
	})
	alias, err := windows.UTF16PtrFromString(soundAlias)
	if err != nil {
		return err
	}
	if r, _, _ := procPlaySoundW.Call(uintptr(unsafe.Pointer(alias)), 0, sndAlias|sndAsync|sndNoDefault); r != 0 { //nolint:gosec // G103: a Win32 call takes the string's address
		return nil
	}
	file, err := windows.UTF16PtrFromString(soundPath())
	if err != nil {
		return err
	}
	if r, _, _ := procPlaySoundW.Call(uintptr(unsafe.Pointer(file)), 0, sndFilename|sndAsync|sndNoDefault); r == 0 { //nolint:gosec // G103: as above
		return errors.New("PlaySound played neither the notification event nor its file: no sound device, or the file is missing")
	}
	return nil
}

// soundPath is the shipped notification sound under the Windows directory.
func soundPath() string {
	dir, err := windows.GetSystemWindowsDirectory()
	if err != nil || dir == "" {
		dir = `C:\Windows`
	}
	return filepath.Join(dir, soundFile)
}

// Exists reports whether the shipped sound file is present (the hook's
// and the tests' question; the alias needs no file).
func soundFileExists() bool {
	fi, err := os.Stat(soundPath())
	return err == nil && fi.Mode().IsRegular()
}

type balloonRequest struct {
	title, text string
	done        chan error
}

// tray is the one notification-area thread of this process, started by
// the first balloon.
var tray struct {
	once  sync.Once
	ready chan struct{}
	err   error
	req   chan balloonRequest
	close chan struct{}
}

// showBalloon hands one showing to the tray thread and waits for its
// answer, or for ctx.
func showBalloon(ctx context.Context, title, text string) error {
	tray.once.Do(func() {
		tray.ready = make(chan struct{})
		tray.req = make(chan balloonRequest)
		tray.close = make(chan struct{})
		go trayThread()
	})
	select {
	case <-tray.ready:
	case <-ctx.Done():
		return ctx.Err()
	}
	if tray.err != nil {
		return tray.err
	}
	r := balloonRequest{title: title, text: text, done: make(chan error, 1)}
	select {
	case tray.req <- r:
	case <-tray.close:
		return errors.New("notify: the notification area was closed")
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-r.done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close removes the icon and the window of a tray thread that was
// started; a clean watcher exit calls it so no stale icon is left for
// the shell to find on the next hover.
func Close() {
	if tray.ready == nil {
		return
	}
	<-tray.ready
	if tray.err != nil {
		return
	}
	select {
	case <-tray.close:
	default:
		close(tray.close)
	}
}

// trayThread owns the window and the icon on one OS thread for the life
// of the process: the shell's callbacks go to DefWindowProc, and the
// loop serves showings, pumps messages, and removes the icon iconLinger
// after the last showing, or on Close.
func trayThread() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	hwnd, icon, err := trayWindow()
	tray.err = err
	close(tray.ready)
	if err != nil {
		return
	}
	defer func() { _, _, _ = procDestroyWindow.Call(hwnd) }()
	nid := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: hwnd, UID: 1}
	added := false
	remove := func() {
		if added {
			del := notifyIconData{CbSize: nid.CbSize, HWnd: hwnd, UID: 1}
			_, _, _ = procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&del))) //nolint:gosec // G103: a Win32 call takes the struct's address
			added = false
		}
	}
	defer remove()
	var removeAt time.Time
	ticker := time.NewTicker(trayPoll)
	defer ticker.Stop()
	for {
		select {
		case r := <-tray.req:
			err := show(&nid, icon, &added, r.title, r.text)
			r.done <- err
			removeAt = time.Now().Add(iconLinger)
		case <-tray.close:
			return
		case <-ticker.C:
		}
		pump()
		if added && !removeAt.IsZero() && time.Now().After(removeAt) {
			remove()
			removeAt = time.Time{}
		}
	}
}

// trayWindow registers the class once and creates the message-only
// window the icon belongs to, and loads the stock information icon.
func trayWindow() (hwnd, icon uintptr, err error) {
	if id, err := windows.UTF16PtrFromString(appUserModelID); err == nil {
		_, _, _ = procSetProcessAUMID.Call(uintptr(unsafe.Pointer(id))) //nolint:gosec // G103: a Win32 call takes the string's address
	}
	hInstance, _, _ := procGetModuleHandleW.Call(0)
	name, err := windows.UTF16PtrFromString(className)
	if err != nil {
		return 0, 0, err
	}
	wc := wndClassExW{
		CbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		LpfnWndProc:   procDefWindowProcW.Addr(),
		HInstance:     hInstance,
		LpszClassName: name,
	}
	if atom, _, e := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 { //nolint:gosec // G103: a Win32 call takes the struct's address
		if !errors.Is(e, windows.ERROR_CLASS_ALREADY_EXISTS) {
			return 0, 0, errors.New("RegisterClassExW: " + e.Error())
		}
	}
	hwnd, _, e := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(name)), 0, 0, 0, 0, 0, 0, hwndMessage, 0, hInstance, 0) //nolint:gosec // G103: a Win32 call takes the string's address
	if hwnd == 0 {
		return 0, 0, errors.New("CreateWindowExW: " + e.Error())
	}
	icon, _, _ = procLoadIconW.Call(0, idiInformation)
	return hwnd, icon, nil
}

// show adds the icon when it is not in the area, and shows one balloon.
func show(nid *notifyIconData, icon uintptr, added *bool, title, text string) error {
	if !*added {
		nid.UFlags = nifMessage | nifIcon | nifTip
		nid.UCallbackMessage = wmUser + 1
		nid.HIcon = icon
		fill(nid.SzTip[:], Title)
		if r, _, e := procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(nid))); r == 0 { //nolint:gosec // G103: a Win32 call takes the struct's address
			return errors.New("Shell_NotifyIcon(NIM_ADD) refused the icon: " + e.Error())
		}
		*added = true
		nid.UVersion = notifyIconVersion4
		_, _, _ = procShellNotifyIconW.Call(nimSetVersion, uintptr(unsafe.Pointer(nid))) //nolint:gosec // G103: as above
	}
	nid.UFlags = nifInfo
	nid.DwInfoFlags = niifInfo | niifNoSound | niifRespectQuietTime
	fill(nid.SzInfoTitle[:], truncate(title, balloonTitleMax))
	fill(nid.SzInfo[:], truncate(text, balloonTextMax))
	if r, _, e := procShellNotifyIconW.Call(nimModify, uintptr(unsafe.Pointer(nid))); r == 0 { //nolint:gosec // G103: as above
		*added = false // the shell may have restarted; the next showing adds the icon again
		return errors.New("Shell_NotifyIcon(NIM_MODIFY) refused the balloon: " + e.Error())
	}
	return nil
}

// pump serves the thread's queued messages (the shell's callbacks).
func pump() {
	var m msg
	for {
		if r, _, _ := procPeekMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0, pmRemove); r == 0 { //nolint:gosec // G103: a Win32 call takes the struct's address
			return
		}
		_, _, _ = procTranslateMessage.Call(uintptr(unsafe.Pointer(&m))) //nolint:gosec // G103: as above
		_, _, _ = procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m))) //nolint:gosec // G103: as above
	}
}

// fill writes s into a fixed UTF-16 field, terminated, cut to fit.
func fill(dst []uint16, s string) {
	u := windows.StringToUTF16(s)
	if len(u) > len(dst) {
		u = append(u[:len(dst)-1], 0)
	}
	copy(dst, u)
}

// truncate cuts s to at most n UTF-16 units, on a rune boundary.
func truncate(s string, n int) string {
	var units int
	for i, r := range s {
		w := 1
		if r > 0xFFFF {
			w = 2
		}
		if units+w > n {
			return s[:i]
		}
		units += w
	}
	return s
}
