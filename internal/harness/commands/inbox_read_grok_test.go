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
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/testutil"
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
	// Dense escaped multiline content must fit after JSON encoding, while
	// multibyte characters must survive every page boundary unchanged.
	frame := strings.Repeat("λ🙂\n\"\\\t\r\x01$(never)", 1000) + "unique tail"
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
		// Pin the public budget independently of the production constant so
		// reverting to 2,048 fails even if the encoder remains lossless.
		if out.Len() > 1800 {
			t.Fatalf("serialized page budget: %d", out.Len())
		}
		if !utf8.Valid(out.Bytes()) || utf8.RuneCount(out.Bytes()) >= 2000 || bytes.Count(out.Bytes(), []byte{'\n'}) != 1 || !bytes.HasSuffix(out.Bytes(), []byte{'\n'}) {
			t.Fatal("page must be valid UTF-8 with only its trailing newline, below the native 2,000-column wrap")
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

func TestGrokReadTornTailDoesNotAbortThePage(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	state := filepath.Join(home, ".local/state/brigade")
	incarnation := strings.Repeat("a", 32)
	m := &sessionmap.ByPID{Harness: "grok", ClaudePID: 4242, ClaudeSessionID: "02000000-0000-7000-8000-000000000001", BrigadeSessionID: "s", TeamRef: "t", TeamName: "ops", SessionName: "grok", Inbound: "accept", FrameLevel: "open", TeamKey: "work", ConfigDir: "/c", Grok: &sessionmap.GrokReceiver{Cwd: "/owned", ProcessStart: "start", Incarnation: incarnation}}
	store := sessionmap.Store{StateDir: state}
	if err := store.WriteByPID(m); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteByHost("grok", m.ClaudeSessionID, &sessionmap.ByHost{HostPID: 4242, BrigadeSessionID: "s"}); err != nil {
		t.Fatal(err)
	}
	dir := inbound.GrokSinkDir(state, "s", incarnation)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	whole, _ := json.Marshal(inbound.SinkRecord{MessageID: "m", Frame: "complete"})
	whole = append(whole, '\n')
	path := filepath.Join(dir, "00000000000000000001.ndjson")
	if err := os.WriteFile(path, append(append([]byte{}, whole...), []byte("torn")...), 0o600); err != nil {
		t.Fatal(err)
	}
	inv := Invocation{Environ: []string{"HOME=" + home, "GROK_SESSION_ID=" + m.ClaudeSessionID}, Deps: Deps{Getppid: func() int { return 4343 }, Lookup: func(int) (procutil.Info, error) {
		return procutil.Info{Exists: true, Command: "grok", StartToken: "start", ParentPID: 4242}, nil
	}}}
	var out bytes.Buffer
	inv.Out = &out
	if err := inboxReadGrok(inv, nil, ""); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result grokReadPage `json:"result"`
	}
	if json.Unmarshal(out.Bytes(), &env) != nil || env.Result.Chunk != "complete" || !env.Result.FrameComplete {
		t.Fatalf("page %+v from %q", env.Result, out.String())
	}
	out.Reset()
	if err := inboxReadGrok(inv, nil, env.Result.Cursor); err != nil {
		t.Fatal(err)
	}
	env.Result = grokReadPage{}
	if json.Unmarshal(out.Bytes(), &env) != nil || !env.Result.CaughtUp {
		t.Fatalf("torn tail did not settle: %s", out.String())
	}
	later := filepath.Join(dir, "00000000000000000002.ndjson")
	if err := os.WriteFile(path, []byte("torn-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	second, _ := json.Marshal(inbound.SinkRecord{MessageID: "m2", Frame: "after"})
	if err := os.WriteFile(later, append(second, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := inboxReadGrok(inv, nil, ""); err != nil {
		t.Fatal(err)
	}
	env.Result = grokReadPage{}
	if json.Unmarshal(out.Bytes(), &env) != nil || env.Result.Chunk != "after" {
		t.Fatalf("later file hidden by a torn prefix: %s", out.String())
	}
	if err := os.Remove(later); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := inboxReadGrok(inv, nil, ""); err != nil || !bytes.Contains(out.Bytes(), []byte(`"caught_up":true`)) {
		t.Fatalf("sole torn tail: %v %s", err, out.String())
	}
}

func TestGrokMonitorCommandRestoresMissingNativeIdentity(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "spaces and 'quotes'")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "brigade")
	testutil.WriteExecutable(t, bin, []byte("#!/bin/sh\nprintf '%s\\n' \"$GROK_SESSION_ID\" \"$@\"\n"))
	m := &sessionmap.ByPID{Harness: "grok", ClaudeSessionID: "02000000-0000-7000-8000-000000000001", PluginBin: bin, Grok: &sessionmap.GrokReceiver{Cwd: dir, ProcessStart: "start", Incarnation: strings.Repeat("a", 32)}}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", m.MonitorCommand()) //nolint:gosec // only the generated command from this owned fixture is executed
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != m.ClaudeSessionID+"\ninbox\nfollow\n--incarnation\n"+m.Grok.Incarnation+"\n" {
		t.Fatalf("command %q %v", out, err)
	}
}
