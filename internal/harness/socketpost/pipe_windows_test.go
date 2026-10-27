//go:build windows

package socketpost

import (
	"bufio"
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// servePipe is a one-connection named-pipe server in the LOCAL namespace,
// the shape Claude Code's inbox has (E11): it accepts one client, reads
// lines until the client closes, and hands them back.
func servePipe(t *testing.T) (path string, lines <-chan []string) {
	t.Helper()
	path = `\\.\pipe\LOCAL\brigade-test-` + strconv.Itoa(os.Getpid()) + `-` + strconv.FormatInt(time.Now().UnixNano(), 36)
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateNamedPipe(name, windows.PIPE_ACCESS_INBOUND, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	out := make(chan []string, 1)
	listening := make(chan struct{})
	go func() {
		defer func() { _ = windows.CloseHandle(h) }()
		close(listening)
		// A client that connected, wrote and closed before this call answers ERROR_NO_DATA ("the pipe is
		// being closed"), with its bytes still in the pipe; ERROR_PIPE_CONNECTED is a client already there.
		if err := windows.ConnectNamedPipe(h, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) && !errors.Is(err, windows.ERROR_NO_DATA) {
			out <- []string{"connect: " + err.Error()}
			return
		}
		f := os.NewFile(uintptr(h), path)
		var got []string
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			got = append(got, sc.Text())
		}
		out <- got
	}()
	<-listening
	time.Sleep(50 * time.Millisecond) // let the goroutine reach ConnectNamedPipe before the client arrives
	return path, out
}

func TestPostWritesTheAuthLineThenTheUserLineIntoANamedPipe(t *testing.T) {
	path, lines := servePipe(t)
	target := Target{Path: path, Token: "tok-123"}
	if err := Post(context.Background(), target, "hello from windows", Options{}); err != nil {
		t.Fatalf("Post = %v", err)
	}
	select {
	case got := <-lines:
		if len(got) != 2 {
			t.Fatalf("the server read %d line(s): %q", len(got), got)
		}
		if got[0] != `{"type":"auth","token":"tok-123"}` {
			t.Fatalf("line 1 = %s", got[0])
		}
		if !strings.HasPrefix(got[1], `{"type":"user","message":{"role":"user","content":"hello from windows"}`) {
			t.Fatalf("line 2 = %s", got[1])
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the server saw nothing within 5 s")
	}
}

func TestAMissingPipeIsSocketGone(t *testing.T) {
	t.Parallel()
	err := Post(context.Background(), Target{Path: `\\.\pipe\LOCAL\brigade-absent-` + strconv.Itoa(os.Getpid()), Token: "t"}, "x", Options{})
	if !errors.Is(err, ErrSocketGone) {
		t.Fatalf("Post to an absent pipe = %v; want ErrSocketGone", err)
	}
}

func TestThePrecheckOnWindowsIsThePipeNameShape(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		`\\.\pipe\LOCAL\cc-msg-9a2476672f9b848b7f6f6c44109946fb`: "",
		`\\.\PIPE\LOCAL\cc-msg-abc`:                              "",
		``:                                                       ReasonEmpty,
		`C:\Users\me\sock`:                                       ReasonNotPipe,
		`/tmp/cc-socks/1.sock`:                                   ReasonNotPipe,
		`\\.\pipe\`:                                              ReasonNotPipe,
		`\\.\pipe\LOCAL/cc-msg-x`:                                ReasonNotPipe,
		`\\.\pipe\` + strings.Repeat("a", 257):                   ReasonPathTooLong,
	}
	for path, want := range cases {
		err := precheck(path, nil, 0)
		switch {
		case want == "" && err != nil:
			t.Errorf("%q: refused with %s", path, err.Reason)
		case want != "" && (err == nil || err.Reason != want):
			t.Errorf("%q: %v; want reason %s", path, err, want)
		}
	}
}
