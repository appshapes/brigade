package commands

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
)

func TestGrokReadPagesOwnedRecordAndRefusesOtherReceivers(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	state := filepath.Join(home, ".local/state/brigade")
	m := &sessionmap.ByPID{Harness: "grok", ClaudePID: 4242, ClaudeSessionID: "02000000-0000-7000-8000-000000000001", BrigadeSessionID: "s", TeamRef: "t", TeamName: "ops", SessionName: "grok", Inbound: "accept", FrameLevel: "open", TeamKey: "work", ConfigDir: "/c", Grok: &sessionmap.GrokReceiver{Cwd: "/owned", ProcessStart: "start", Incarnation: strings.Repeat("a", 32)}}
	store := sessionmap.Store{StateDir: state}
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteByHost("grok", m.ClaudeSessionID, &sessionmap.ByHost{HostPID: 4242, BrigadeSessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	frame := strings.Repeat("λ🙂\n\"$(never)", 1000) + "unique tail"
	line, _ := json.Marshal(inbound.SinkRecord{MessageID: "m", Frame: frame})
	line = append(line, '\n')
	path := inbound.GrokSinkPath(state, "s", m.Grok.Incarnation, time.Unix(1, 0))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, line, 0600); err != nil {
		t.Fatal(err)
	}
	inv := Invocation{Environ: []string{"HOME=" + home, "GROK_SESSION_ID=" + m.ClaudeSessionID}, Deps: Deps{Getppid: func() int { return 4343 }, Lookup: func(_ int) (procutil.Info, error) {
		return procutil.Info{Exists: true, Command: "grok", StartToken: "start", ParentPID: 4242}, nil
	}}}
	var joined strings.Builder
	cursor := ""
	offset := 0
	read := func(token string) grokReadPage {
		t.Helper()
		var out bytes.Buffer
		inv.Out = &out
		if err := inboxReadGrok(inv, nil, token); err != nil {
			t.Fatal(err)
		}
		if out.Len() > 2048 {
			t.Fatalf("serialized page budget: %d", out.Len())
		}
		var env struct {
			Result grokReadPage `json:"result"`
		}
		if err := json.Unmarshal(out.Bytes(), &env); err != nil || !env.Result.Untrusted || !env.Result.PageComplete {
			t.Fatalf("invalid complete envelope: %v", err)
		}
		return env.Result
	}
	for {
		page := read(cursor)
		if page.CaughtUp {
			break
		}
		repeated := read(cursor)
		if repeated.Chunk != page.Chunk || repeated.Cursor != page.Cursor {
			t.Fatal("cursor retry changed page")
		}
		if page.Offset != offset || page.Digest != digestBytes([]byte(frame)) || page.Length != len(frame) {
			t.Fatal("frame metadata changed")
		}
		joined.WriteString(page.Chunk)
		offset = page.Next
		cursor = page.Cursor
	}
	if joined.String() != frame {
		t.Fatal("frame changed/truncated across pages")
	}
	// A later wake drains all retained rotations from the stable EOF cursor.
	next := inbound.GrokSinkPath(state, "s", m.Grok.Incarnation, time.Unix(2, 0))
	other, _ := json.Marshal(inbound.SinkRecord{MessageID: "m2", Frame: "second complete frame"})
	if err := os.WriteFile(next, append(other, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	page := read(cursor)
	if page.MessageID != "m2" || page.Chunk != "second complete frame" || !page.FrameComplete {
		t.Fatal("rotation lost an accepted frame")
	}
	if !read(page.Cursor).CaughtUp {
		t.Fatal("stream did not drain")
	}
	if inboxReadGrok(inv, nil, "../unrelated") == nil {
		t.Fatal("invalid cursor accepted")
	}
	decoded, _ := base64.RawURLEncoding.DecodeString(cursor)
	var c grokCursor
	if json.Unmarshal(decoded, &c) != nil {
		t.Fatal("bad generated cursor")
	}
	c.Offset = 1 // lambda is two UTF-8 bytes
	if inboxReadGrok(inv, nil, cursorEncode(c)) == nil {
		t.Fatal("split UTF-8 boundary accepted")
	}
	for _, damaged := range [][]byte{line[:100], []byte("invalid record\n"), []byte("{\"frame\":\"substituted\"}\n")} {
		if err := os.WriteFile(path, damaged, 0600); err != nil {
			t.Fatal(err)
		}
		if inboxReadGrok(inv, nil, cursor) == nil {
			t.Fatal("partial, corrupt or substituted cursor record accepted")
		}
	}
	if err := os.WriteFile(path, line, 0600); err != nil {
		t.Fatal(err)
	}
	m.Grok.Incarnation = strings.Repeat("b", 32)
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if inboxReadGrok(inv, nil, cursor) == nil {
		t.Fatal("old-incarnation cursor accepted")
	}
	m.Grok.Incarnation = strings.Repeat("a", 32)
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(next, path); err != nil {
		t.Fatal(err)
	}
	if inboxReadGrok(inv, nil, cursor) == nil {
		t.Fatal("substituted symlink cursor accepted")
	}
	inv.Deps.Getppid = func() int { return 1 }
	if inboxReadGrok(inv, nil, cursor) == nil {
		t.Fatal("unrelated process ancestry accepted")
	}
}

func TestGrokMonitorCommandRestoresMissingNativeIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "spaces and 'quotes'")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "brigade")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$GROK_SESSION_ID\" \"$@\"\n"), 0700); err != nil { //nolint:gosec // owned executable fixture, not user configuration
		t.Fatal(err)
	}
	m := &sessionmap.ByPID{Harness: "grok", ClaudeSessionID: "02000000-0000-7000-8000-000000000001", PluginBin: bin, Grok: &sessionmap.GrokReceiver{Cwd: dir, ProcessStart: "start", Incarnation: strings.Repeat("a", 32)}}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", m.MonitorCommand()) //nolint:gosec // only the generated command from this owned fixture is executed
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != m.ClaudeSessionID+"\ninbox\nfollow\n--incarnation\n"+m.Grok.Incarnation+"\n" {
		t.Fatalf("command %q %v", out, err)
	}
}
