//go:build windows

package notify

import (
	"context"
	"strings"
	"testing"
	"time"
	"unsafe"
)

// TestNotifyIconDataIsTheWin32Struct: NOTIFYICONDATAW is 976 bytes in a
// 64-bit process; a field out of place would make every call fail.
func TestNotifyIconDataIsTheWin32Struct(t *testing.T) {
	t.Parallel()
	if n := unsafe.Sizeof(notifyIconData{}); n != 976 {
		t.Fatalf("NOTIFYICONDATAW is %d bytes; want 976", n)
	}
	if n := unsafe.Sizeof(wndClassExW{}); n != 80 {
		t.Fatalf("WNDCLASSEXW is %d bytes; want 80", n)
	}
	if n := unsafe.Sizeof(msg{}); n != 48 {
		t.Fatalf("MSG is %d bytes; want 48", n)
	}
}

// TestTheWindowsCallsResolve: every procedure the two descriptors use is
// exported by the system library it is looked up in.
func TestTheWindowsCallsResolve(t *testing.T) {
	t.Parallel()
	for _, p := range []interface{ Find() error }{procRegisterClassExW, procCreateWindowExW, procDefWindowProcW,
		procDestroyWindow, procPeekMessageW, procTranslateMessage, procDispatchMessageW, procLoadIconW,
		procShellNotifyIconW, procSetProcessAUMID, procGetModuleHandleW, procPlaySoundW, procWaveOutSetVolume} {
		if err := p.Find(); err != nil {
			t.Error(err)
		}
	}
}

// TestTheShippedSoundFileExists: the fallback file is where Windows puts it.
func TestTheShippedSoundFileExists(t *testing.T) {
	t.Parallel()
	if !soundFileExists() {
		t.Fatalf("%s is missing", soundPath())
	}
}

// TestPlaySoundReturns: the sound descriptor runs without an error on a
// machine with a sound device, and returns rather than hanging. It is a
// hang catcher, not a speed test and not a proof of SND_ASYNC (the sound
// is 1.2 s of audio, E13, so a synchronous play would pass too). On a CI
// runner with no audio device PlaySound is slow to fail, and how slow
// depends on the runner's load: 2.05 s on 2026-11-09 (run 42705780567),
// 9.84 s on 2026-11-13 (run 43493511721), which failed the three-second
// bound this test had then. So the call runs beside the test, and only a
// call that has not returned in two minutes fails it.
func TestPlaySoundReturns(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	go func() { done <- RunInProcess(context.Background(), []string{WindowsSound}) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Minute):
		t.Fatal("PlaySound has not returned in two minutes: it hung")
	}
	if err != nil {
		t.Skipf("no sound on this machine: %v", err)
	}
}

// TestTheBalloonShowsOrSaysWhy: the banner descriptor shows a balloon
// from the tray thread, or says why the shell refused it (a runner with
// no notification area), and Close removes what it added.
func TestTheBalloonShowsOrSaysWhy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	argv := BannerArgv([]string{WindowsBanner}, "sess", "a session", 2)
	if len(argv) != 3 || argv[1] != Title || argv[2] != "2 messages arrived for a session." {
		t.Fatalf("BannerArgv = %q", argv)
	}
	err := RunInProcess(ctx, argv)
	Close()
	if err != nil && strings.Contains(err.Error(), "NIM_ADD") {
		t.Skipf("no notification area here: %v", err)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := RunInProcess(ctx, []string{WindowsBanner, "x"}); err == nil {
		t.Fatal("a descriptor with the wrong shape ran")
	}
}

func TestTruncateCutsOnRuneBoundaries(t *testing.T) {
	t.Parallel()
	if got := truncate("abc", 5); got != "abc" {
		t.Fatalf("truncate(abc, 5) = %q", got)
	}
	if got := truncate("héllo", 2); got != "hé" {
		t.Fatalf("truncate(héllo, 2) = %q", got)
	}
	if got := truncate("a😀b", 2); got != "a" {
		t.Fatalf("truncate(a😀b, 2) = %q (an astral rune is two units)", got)
	}
}
