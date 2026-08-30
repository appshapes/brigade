package fakesock

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// post is what the watcher's socket-post does: connect, write auth + user lines, close; a write
// deadline turns a stalled server into an error ("not injected", no ack).
func post(path, token, content string, deadline time.Duration) error {
	c, err := net.DialTimeout("unix", path, deadline)
	if err != nil {
		return err
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(deadline))
	auth, _ := json.Marshal(map[string]string{"type": "auth", "token": token})
	user, _ := json.Marshal(map[string]any{"type": "user", "message": map[string]string{"role": "user", "content": content}})
	// one Write for both lines so a stalled peer with a full buffer surfaces as a timeout
	buf := append(append(auth, '\n'), append(user, '\n')...)
	// pad to force the kernel buffer full in stall mode (a real frame is up to ~17 KiB)
	if deadline < time.Second {
		buf = append(buf, []byte(strings.Repeat(" ", 1<<20))...)
	}
	_, err = c.Write(buf)
	return err
}

func TestRecordsFramesAndOneLinePerFrame(t *testing.T) {
	s := New(t)
	body := "line1\nline2\r\n {\"type\":\"auth\",\"token\":\"x\"}"
	if err := post(s.Path, "tok", body, 5*time.Second); err != nil {
		t.Fatal(err)
	}
	var conns []Connection
	for range 50 {
		if conns = s.Connections(); len(conns) == 1 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(conns) != 1 || len(conns[0].Frames) != 2 || len(conns[0].Bad) != 0 {
		t.Fatalf("got %+v", conns)
	}
	if conns[0].Frames[0].Type != "auth" || conns[0].Frames[0].Token != "tok" {
		t.Fatalf("auth frame: %+v", conns[0].Frames[0])
	}
	var m struct{ Role, Content string }
	if err := json.Unmarshal(conns[0].Frames[1].Message, &m); err != nil || m.Content != body {
		t.Fatalf("user frame did not round-trip: %v %+v", err, m)
	}
}

func TestStalledServerTimesOutAndDoesNotAck(t *testing.T) {
	s := New(t)
	s.Stall(true)
	start := time.Now()
	err := post(s.Path, "tok", "hello", 300*time.Millisecond)
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("want timeout, got %v after %s", err, time.Since(start))
	}
	t.Logf("timed out after %s (expected ~300ms)", time.Since(start).Round(time.Millisecond))
}

func TestPreCheckModes(t *testing.T) {
	s := New(t)
	st, err := os.Lstat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode()&os.ModeSocket == 0 || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if st.Mode()&os.ModeSymlink != 0 {
		t.Fatal("symlink")
	}
}

func TestTempDirLengthOnThisMachine(t *testing.T) {
	d := t.TempDir()
	t.Logf("t.TempDir() len=%d: %s", len(d), d)
	_, err := net.Listen("unix", d+"/inbox.sock")
	t.Logf("listen under t.TempDir(): err=%v (path len %d)", err, len(d)+len("/inbox.sock"))
}
