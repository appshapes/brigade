package foldersync_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesync"
)

// TestFolderIDIsTheSpecifiedDerivation pins §4.3's formula independently
// of the implementation: every checkout of a team must arrive at the same
// id for the same folder without exchanging it.
func TestFolderIDIsTheSpecifiedDerivation(t *testing.T) {
	t.Parallel()
	sum := sha256.Sum256([]byte("docs/shared"))
	want := "brigade-6f0f2b41-" + hex.EncodeToString(sum[:])[:12]
	if got := foldersync.FolderID("6f0f2b41-5a3c-49d7-b8e2-0c7a4f1e6d33", "docs/shared"); got != want {
		t.Fatalf("FolderID = %q, want %q", got, want)
	}
	// Dashes are removed BEFORE the eight are taken.
	if got := foldersync.FolderID("6f0f-2b41-5a3c", "x"); !strings.HasPrefix(got, "brigade-6f0f2b41-") {
		t.Fatalf("FolderID with early dashes = %q", got)
	}
	// A short ref is taken whole; the folder is hashed exactly as written.
	if got := foldersync.FolderID("t1", "Docs"); !strings.HasPrefix(got, "brigade-t1-") || got == foldersync.FolderID("t1", "docs") {
		t.Fatalf("FolderID(t1, Docs) = %q", got)
	}
	if foldersync.FolderID("team-a-1", "docs") == foldersync.FolderID("team-b-1", "docs") {
		t.Fatal("two teams share a folder id")
	}
}

func TestFoldersDerivesPathAndLabel(t *testing.T) {
	t.Parallel()
	got := foldersync.Folders("team-1", "/home/u/repo", "brigade", []string{".context/plans", "docs"})
	want := []foldersync.Folder{
		{ID: foldersync.FolderID("team-1", ".context/plans"), Path: "/home/u/repo/.context/plans", Label: "brigade/.context/plans"},
		{ID: foldersync.FolderID("team-1", "docs"), Path: "/home/u/repo/docs", Label: "brigade/docs"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("Folders = %+v\nwant %+v", got, want)
	}
	if got := foldersync.Folders("team-1", "/r", "", []string{"docs"}); got[0].Label != "docs" {
		t.Fatalf("a session without a workspace label: label %q, want the folder alone", got[0].Label)
	}
}

func TestPeerPrefix(t *testing.T) {
	t.Parallel()
	if got := foldersync.WirePeer("syncthing", "ABC-DEF"); got != "syncthing:ABC-DEF" {
		t.Fatalf("WirePeer = %q", got)
	}
	cases := []struct {
		wire string
		want string
		ok   bool
	}{
		{"syncthing:ABC-DEF", "ABC-DEF", true},
		{"syncthing:a:b", "a:b", true},
		{"syncthing:", "", false},
		{"syncthingx:ABC", "", false},
		{"other:ABC", "", false},
		{"ABC", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		got, ok := foldersync.PeerFor("syncthing", c.wire)
		if got != c.want || ok != c.ok {
			t.Errorf("PeerFor(syncthing, %q) = (%q, %v), want (%q, %v)", c.wire, got, ok, c.want, c.ok)
		}
	}
}

func TestPeerLabel(t *testing.T) {
	t.Parallel()
	cases := []struct{ label, ref, want string }{
		{"alice@example.com", "b49eac08-1111", "alice@example.com"},
		{"  Alice\n of  Ops ", "b49eac08-1111", "Alice of Ops"},
		{"", "b49eac08-1111-2222", "b49eac08"},
		{"\u200b", "abc", "abc"},
		{"", "", ""},
	}
	for _, c := range cases {
		if got := foldersync.PeerLabel(c.label, c.ref); got != c.want {
			t.Errorf("PeerLabel(%q, %q) = %q, want %q", c.label, c.ref, got, c.want)
		}
	}
}

// TestArgvResolution is §4.3's name resolution: the bundled name runs the
// plugin binary's `sync-adapter syncthing`; any other name is
// brigade-sync-<name> found on the CLIENT's PATH (not the process's), by
// absolute path; a missing one is IsNotFound; a name that is not a name
// never reaches PATH at all.
func TestArgvResolution(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// An executable is only looked up here, never run.
	exe := filepath.Join(dir, "brigade-sync-rsyncish")
	testutil.WriteExecutable(t, exe, []byte("#!/bin/sh\nexit 0\n"))
	nonexec := filepath.Join(dir, "brigade-sync-plainfile")
	if err := os.WriteFile(nonexec, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	env := []string{"PATH=relative/bin:" + filepath.Join(dir, "absent") + ":" + dir}

	c := &foldersync.Client{Adapter: "syncthing", PluginBin: "/opt/plugin/bin/brigade", Environ: env}
	if got, err := c.Argv(); err != nil || !slices.Equal(got, []string{"/opt/plugin/bin/brigade", "sync-adapter", "syncthing"}) {
		t.Fatalf("bundled Argv = %v, %v", got, err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c = &foldersync.Client{Adapter: "syncthing", Environ: env}
	if got, err := c.Argv(); err != nil || got[0] != self {
		t.Fatalf("bundled Argv without a plugin binary = %v, %v; want this executable", got, err)
	}
	c = &foldersync.Client{Adapter: "rsyncish", Environ: env}
	if got, err := c.Argv(); err != nil || !slices.Equal(got, []string{exe}) {
		t.Fatalf("external Argv = %v, %v", got, err)
	}
	for _, name := range []string{"plainfile", "nosuch"} {
		c = &foldersync.Client{Adapter: name, Environ: env}
		_, err := c.Argv()
		if !foldersync.IsNotFound(err) {
			t.Fatalf("%s: Argv error %v, want not found", name, err)
		}
	}
	c = &foldersync.Client{Adapter: "../../bin/sh", Environ: env}
	if _, err := c.Argv(); err == nil || foldersync.IsNotFound(err) {
		t.Fatalf("a path as an adapter name: %v, want the invalid-name refusal", err)
	}
	c = &foldersync.Client{Adapter: "anything", Command: []string{"/bin/sh", "/tmp/x.sh"}}
	if got, err := c.Argv(); err != nil || !slices.Equal(got, []string{"/bin/sh", "/tmp/x.sh"}) {
		t.Fatalf("Command override = %v, %v", got, err)
	}
}

// TestVerbsOverTheWire drives every verb through the fake adapter: one
// request document on stdin in the shape of §4.3, BRIGADE_STATE_DIR in the
// child's environment, the result decoded.
func TestVerbsOverTheWire(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	fake := fakesync.Write(t, dir, fakesync.Answers{
		Status: `{"running":true,"peer":"SELF-DEVICE-1","folders":[{"id":"f1","path":"/r/docs","state":"syncing"}],"peers":[{"peer":"P1","connected":false}]}`,
	})
	c := &foldersync.Client{Adapter: "syncthing", StateDir: state, Environ: []string{"PATH=/usr/bin:/bin"}, Command: fake.Argv}
	ctx := t.Context()

	desc, err := c.Describe(ctx)
	if err != nil || desc.Name != "syncthing" || desc.ProtocolVersion != foldersync.ProtocolVersion {
		t.Fatalf("Describe = %+v, %v", desc, err)
	}
	att, err := c.Attach(ctx, "sess-1")
	if err != nil || att.Peer != fakesync.SelfPeer {
		t.Fatalf("Attach = %+v, %v", att, err)
	}
	folders := foldersync.Folders("team-1", "/r", "repo", []string{"docs"})
	peers := []foldersync.Peer{{Peer: "P1", Label: "alice"}}
	ap, err := c.Apply(ctx, "sess-1", folders, peers)
	if err != nil || len(ap.Folders) != 1 || len(ap.Peers) != 1 || !ap.Peers[0].Connected {
		t.Fatalf("Apply = %+v, %v", ap, err)
	}
	st, err := c.Status(ctx)
	if err != nil || !st.Running || len(st.Folders) != 1 || st.Folders[0].Path != "/r/docs" || st.Peers[0].Connected {
		t.Fatalf("Status = %+v, %v", st, err)
	}
	det, err := c.Detach(ctx, "sess-1")
	if err != nil || !det.Stopped {
		t.Fatalf("Detach = %+v, %v", det, err)
	}

	recs := fake.Records(t)
	if got := fake.Verbs(t); !slices.Equal(got, []string{"describe", "attach", "apply", "status", "detach"}) {
		t.Fatalf("verbs = %v", got)
	}
	for _, r := range recs {
		if r.StateDir != state {
			t.Errorf("%s: BRIGADE_STATE_DIR = %q, want %q", r.Verb, r.StateDir, state)
		}
	}
	wantReq := map[string]string{
		"describe": `{}`,
		"attach":   `{"state_dir":"` + state + `","session_id":"sess-1"}`,
		"status":   `{"state_dir":"` + state + `"}`,
		"detach":   `{"state_dir":"` + state + `","session_id":"sess-1"}`,
	}
	for _, r := range recs {
		if want, ok := wantReq[r.Verb]; ok && string(r.Request) != want {
			t.Errorf("%s request = %s, want %s", r.Verb, r.Request, want)
		}
	}
	var apply foldersync.ApplyRequest
	if err := json.Unmarshal(recs[2].Request, &apply); err != nil {
		t.Fatal(err)
	}
	if apply.StateDir != state || apply.SessionID != "sess-1" || !slices.Equal(apply.Folders, folders) || !slices.Equal(apply.Peers, peers) {
		t.Fatalf("apply request = %+v", apply)
	}
}

// TestAttachAndDetachCarryThePID: a Client with a PID sends it as
// attach's and detach's optional "pid" member (and only theirs); without
// one the member is absent, as TestVerbsOverTheWire pins.
func TestAttachAndDetachCarryThePID(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	state := filepath.Join(dir, "state")
	fake := fakesync.Write(t, dir, fakesync.Answers{})
	c := &foldersync.Client{Adapter: "syncthing", StateDir: state, PID: 4242, Environ: []string{"PATH=/usr/bin:/bin"}, Command: fake.Argv}
	ctx := t.Context()
	if _, err := c.Attach(ctx, "sess-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Detach(ctx, "sess-1"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"attach": `{"state_dir":"` + state + `","session_id":"sess-1","pid":4242}`,
		"status": `{"state_dir":"` + state + `"}`,
		"detach": `{"state_dir":"` + state + `","session_id":"sess-1","pid":4242}`,
	}
	for _, r := range fake.Records(t) {
		if string(r.Request) != want[r.Verb] {
			t.Errorf("%s request = %s, want %s", r.Verb, r.Request, want[r.Verb])
		}
	}
}

// TestFailuresAreProtocolErrors: an adapter's failing envelope comes back
// with its code; a describe of another sync protocol is protocol_mismatch;
// an attach without a descriptor is an invalid result — none of them
// carries a result body.
func TestFailuresAreProtocolErrors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	fake := fakesync.Write(t, dir, fakesync.Answers{ErrorOn: []string{"describe"}, ErrorMessage: "syncthing is not on PATH"})
	c := &foldersync.Client{Adapter: "syncthing", StateDir: dir, Command: fake.Argv}
	_, err := c.Describe(ctx)
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeUnavailable || perr.Message != "syncthing is not on PATH" || foldersync.IsNotFound(err) {
		t.Fatalf("Describe error = %v", err)
	}

	for name, answers := range map[string]fakesync.Answers{
		"another protocol": {Describe: `{"name":"x","version":"1","protocol_version":"sync/2"}`},
		"an empty peer":    {Attach: `{"peer":""}`},
		"an oversize peer": {Attach: `{"peer":"` + strings.Repeat("A", protocol.MaxSyncPeerChars) + `"}`},
	} {
		d := t.TempDir()
		f := fakesync.Write(t, d, answers)
		c := &foldersync.Client{Adapter: "syncthing", StateDir: d, Command: f.Argv}
		if name == "another protocol" {
			_, err = c.Describe(ctx)
			if !errors.As(err, &perr) || perr.Code != protocol.CodeProtocolMismatch {
				t.Errorf("%s: %v", name, err)
			}
			continue
		}
		_, err = c.Attach(ctx, "s1")
		if !errors.As(err, &perr) || perr.Details["reason"] != foldersync.ReasonResultInvalid {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// TestSpawnSpecIsArgvAndAllowListedEnvironment records the spawn: the
// verb appended to the argv prefix, an environment built from scratch —
// the allow-list plus BRIGADE_STATE_DIR, never an inherited BRIGADE_* or
// the messaging token — and the child's stderr to the sync log.
func TestSpawnSpecIsArgvAndAllowListedEnvironment(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	var got adapterkit.SpawnSpec
	c := &foldersync.Client{
		Adapter: "syncthing", PluginBin: "/opt/p/brigade", StateDir: state,
		Environ: []string{"PATH=/bin", "HOME=/h", "BRIGADE_PROFILE=evil", "CLAUDE_CODE_MESSAGING_TOKEN=tok", "OTHER=x"},
		Spawn: func(_ context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
			got = spec
			return &adapterkit.SpawnResult{Envelope: &protocol.Envelope{OK: true, ProtocolVersion: "1", Result: []byte(`{"running":false,"peer":"","folders":[],"peers":[]}`)}}, nil
		},
	}
	if _, err := c.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got.Argv, []string{"/opt/p/brigade", "sync-adapter", "syncthing", "status"}) {
		t.Fatalf("argv = %v", got.Argv)
	}
	want := []string{"PATH=/bin", "HOME=/h", "BRIGADE_STATE_DIR=" + state}
	if !slices.Equal(got.Env, want) {
		t.Fatalf("env = %v, want %v", got.Env, want)
	}
	if got.Stderr == nil {
		t.Fatal("the child's stderr goes nowhere; want the sync log")
	}
	if _, err := os.Stat(filepath.Join(state, "logs", "sync-syncthing.log")); err != nil {
		t.Fatalf("sync log: %v", err)
	}
}

// TestTheSyncLogRotatesAtTheWatcherCap: a sync log already at the cap is
// renamed to <log>.1 before the next call writes, and the call's stderr
// lands in a fresh file — one kept generation, as the watcher's log.
func TestTheSyncLogRotatesAtTheWatcherCap(t *testing.T) {
	t.Parallel()
	state := t.TempDir()
	logPath := filepath.Join(state, "logs", "sync-syncthing.log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(logPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(logPath, foldersync.LogRotateBytes); err != nil {
		t.Fatal(err)
	}
	c := &foldersync.Client{
		Adapter: "syncthing", PluginBin: "/opt/p/brigade", StateDir: state, Environ: []string{"PATH=/bin"},
		Spawn: func(_ context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
			_, _ = spec.Stderr.Write([]byte("fresh\n"))
			return &adapterkit.SpawnResult{Envelope: &protocol.Envelope{OK: true, ProtocolVersion: "1", Result: []byte(`{"running":false,"peer":"","folders":[],"peers":[]}`)}}, nil
		},
	}
	if _, err := c.Status(t.Context()); err != nil {
		t.Fatal(err)
	}
	if fi, err := os.Stat(logPath + ".1"); err != nil || fi.Size() != foldersync.LogRotateBytes {
		t.Fatalf("the full log was not kept as .1: %v", err)
	}
	if raw, err := os.ReadFile(logPath); err != nil || string(raw) != "fresh\n" {
		t.Fatalf("the fresh log = %q (%v), want the call's stderr alone", raw, err)
	}
}
