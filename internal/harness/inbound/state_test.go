package inbound

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// These tests use real files under t.TempDir and no synctest bubble: the
// claim of every one of them is that a file crosses a process.

var stateNow = time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)

// TestStateNames is the encoding table of P5-9 (3.1), over all four
// functions: a safe id is the stem verbatim, anything else is its SHA-256
// with the ".sha256" suffix, and EVERY row — "..", a separator, a NUL, a
// newline, a non-ASCII rune, the empty string, a 65-byte id — lands
// directly in its own state/<seen|pending|release> directory, which is
// the anti-escape assertion that matters. The three paths share ONE
// encoding: their bases are identical for every row.
func TestStateNames(t *testing.T) {
	t.Parallel()
	const root = "/s"
	digest := func(id string) string {
		sum := sha256.Sum256([]byte(id))
		return hex.EncodeToString(sum[:]) + ".sha256"
	}
	hex32 := strings.Repeat("0123456789abcdef", 2)
	uuid := "c876fc4e-72c1-44eb-9027-f5d03941e8d1"
	id64 := strings.Repeat("a", 64)
	id65 := strings.Repeat("a", 65)
	rows := []struct {
		name, id, stem string
		plain          bool
	}{
		{"32 hex", hex32, hex32, true},
		{"uuid", uuid, uuid, true},
		{"64 bytes", id64, id64, true},
		{"65 bytes", id65, digest(id65), false},
		{"separator", "a/b", digest("a/b"), false},
		{"dot-dot", "..", digest(".."), false},
		{"traversal", "state/pending/../../../etc/x", digest("state/pending/../../../etc/x"), false},
		{"NUL", "a\x00b", digest("a\x00b"), false},
		{"newline", "a\nb", digest("a\nb"), false},
		{"non-ASCII", "sessión", digest("sessión"), false},
		{"empty", "", digest(""), false},
		{"4 KiB", strings.Repeat("z", 4096), digest(strings.Repeat("z", 4096)), false},
	}
	dirs := map[string]string{
		"seen":    filepath.Join(root, "state", "seen"),
		"pending": filepath.Join(root, "state", "pending"),
		"release": filepath.Join(root, "state", "release"),
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			if got := StateName(r.id); got != r.stem {
				t.Fatalf("StateName(%q) = %q, want %q", r.id, got, r.stem)
			}
			paths := map[string]string{
				"seen":    SeenPath(root, r.id),
				"pending": PendingPath(root, r.id),
				"release": ReleasePath(root, r.id),
			}
			for kind, got := range paths {
				if want := filepath.Join(dirs[kind], r.stem+".json"); got != want {
					t.Fatalf("%s path for %q = %q, want %q", kind, r.id, got, want)
				}
				if filepath.Dir(got) != dirs[kind] {
					t.Fatalf("%s path for %q = %q escapes %q", kind, r.id, got, dirs[kind])
				}
				base := filepath.Base(got)
				if strings.ContainsAny(base, "/\\\x00\n") || !strings.HasSuffix(base, ".json") {
					t.Fatalf("%s base %q", kind, base)
				}
				if base != filepath.Base(paths["seen"]) {
					t.Fatalf("the three paths differ in their base: %q vs %q", base, filepath.Base(paths["seen"]))
				}
			}
			if safeSeenStem(r.id) != r.plain {
				t.Fatalf("plain branch for %q: %v, want %v", r.id, !r.plain, r.plain)
			}
		})
	}
	// The drift join of P5-14 (3.2): the plain branch must partition every
	// charset case as sessionmap.CheckNativeID does. inbound must not
	// import sessionmap; this test file may.
	for _, id := range []string{"a", "s1", uuid, hex32, "ABC_xyz-09", "-", "_", id64, "", ".", "..", "a.b", "a/b", "a b", "a\x00b", "sessión", "a:b", "a@b"} {
		if native := sessionmap.CheckNativeID(id) == nil; safeSeenStem(id) != native {
			t.Errorf("id %q: plain %v, CheckNativeID accepts %v", id, safeSeenStem(id), native)
		}
	}
}

func pendingStore(t *testing.T) FilePendingStore {
	t.Helper()
	root := filepath.Join(t.TempDir(), "state-root")
	return FilePendingStore{Path: PendingPath(root, "brigade-sess-1"), SessionID: "brigade-sess-1"}
}

func pendingEntry(id, sender string, at time.Time) PendingEntry {
	return PendingEntry{
		MessageID: id, SenderSessionID: "sid-" + sender, SenderName: sender, SenderPrincipal: "principal-" + sender,
		Summary: "summary of " + id, ReceivedAt: at,
	}
}

func TestPendingStoreRoundTrip(t *testing.T) {
	t.Parallel()
	s := pendingStore(t)
	f, err := s.Load()
	if err != nil || len(f.Entries) != 0 || f.Version != PendingFileVersion || f.SessionID != "brigade-sess-1" {
		t.Fatalf("missing file: %+v %v, want an empty document for the session", f, err)
	}
	released := pendingEntry("m2", "ci-runner", stateNow)
	released.ReleasedAt = stateNow.Add(time.Minute)
	want := []PendingEntry{pendingEntry("m1", "payments-api", stateNow), released}
	if err := s.Save(PendingFile{Entries: want, DroppedTotal: 3, UpdatedAt: stateNow, Version: 99, SessionID: "not-mine"}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != PendingFileVersion || got.SessionID != "brigade-sess-1" || got.DroppedTotal != 3 || !got.UpdatedAt.Equal(stateNow) {
		t.Fatalf("document %+v: the store's version and session id must win", got)
	}
	if len(got.Entries) != 2 || got.Entries[0].MessageID != "m1" || got.Entries[1].MessageID != "m2" || !got.Entries[1].Released() || got.Entries[0].Released() {
		t.Fatalf("entries %+v", got.Entries)
	}
	if got.Entries[0].Summary != "summary of m1" || got.Entries[1].SenderPrincipal != "principal-ci-runner" || !got.Entries[0].ReceivedAt.Equal(stateNow) {
		t.Fatalf("entries %+v", got.Entries)
	}
	// The file names no body member at all: the shape has none.
	raw, err := os.ReadFile(s.Path)
	if err != nil || bytes.Contains(raw, []byte(`"body"`)) {
		t.Fatalf("raw %s %v", raw, err)
	}
}

func TestPendingLoadRefusesForeignSession(t *testing.T) {
	t.Parallel()
	s := pendingStore(t)
	other := FilePendingStore{Path: s.Path, SessionID: "brigade-sess-2"}
	if err := other.Save(PendingFile{Entries: []PendingEntry{pendingEntry("m1", "a", stateNow)}}); err != nil {
		t.Fatal(err)
	}
	_, err := s.Load()
	if !errors.Is(err, errPendingFileForeign) {
		t.Fatalf("err = %v, want the foreign-session refusal", err)
	}
	// The other store reads its own file.
	if f, err := other.Load(); err != nil || len(f.Entries) != 1 {
		t.Fatalf("own load: %+v %v", f, err)
	}
}

func TestPendingLoadRefusesNonPrivateFile(t *testing.T) {
	t.Parallel()
	s := pendingStore(t)
	if err := s.Save(PendingFile{Entries: []PendingEntry{pendingEntry("m1", "a", stateNow)}}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode is the PRECONDITION
		t.Fatal(err)
	}
	_, err := s.Load()
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConfig || perr.Details["reason"] != "insecure_mode" {
		t.Fatalf("err = %v, want ReadStrict's insecure_mode refusal", err)
	}
	// Malformed, wrong version and over-large files are fixed-text errors.
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"malformed", []byte("{not json"), errPendingFileMalformed},
		{"version", []byte(`{"version":2,"session_id":"brigade-sess-1","entries":[]}`), errPendingFileVersion},
	} {
		if err := os.Remove(s.Path); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(s.Path, tc.data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Load(); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestPendingLoadTruncatesToCapacity(t *testing.T) {
	t.Parallel()
	s := pendingStore(t)
	entries := make([]PendingEntry, 0, HoldCapacity+60)
	for i := range HoldCapacity + 50 {
		entries = append(entries, pendingEntry("m"+strconv.Itoa(i), "a", stateNow.Add(time.Duration(i)*time.Second)))
	}
	// Invalid rows: an empty id, an over-long id, a missing received_at.
	entries = append(entries, PendingEntry{MessageID: "", ReceivedAt: stateNow}, PendingEntry{MessageID: strings.Repeat("x", MaxMessageIDBytes+1), ReceivedAt: stateNow}, PendingEntry{MessageID: "no-time"})
	// Save's own truncation keeps the last HoldCapacity, so write the
	// over-long document directly to prove Load truncates too.
	data := mustMarshalPending(t, PendingFile{Version: PendingFileVersion, SessionID: "brigade-sess-1", Entries: entries})
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != HoldCapacity || got.Entries[0].MessageID != "m50" || got.Entries[HoldCapacity-1].MessageID != "m149" {
		t.Fatalf("loaded %d entries, first %q last %q", len(got.Entries), got.Entries[0].MessageID, got.Entries[len(got.Entries)-1].MessageID)
	}
	// Save truncates the same way.
	if err := s.Save(PendingFile{Entries: entries[:HoldCapacity+20]}); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || len(got.Entries) != HoldCapacity || got.Entries[0].MessageID != "m20" {
		t.Fatalf("after Save: %d entries, err %v", len(got.Entries), err)
	}
	// The byte cap.
	big := append(bytes.Repeat([]byte(" "), MaxPendingFileBytes), data...)
	if err := os.WriteFile(s.Path, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(); err == nil {
		t.Fatal("an over-large pending file was loaded")
	}
}

func mustMarshalPending(t *testing.T, f PendingFile) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString(`{"version":` + strconv.Itoa(f.Version) + `,"session_id":"` + f.SessionID + `","entries":[`)
	for i, e := range f.Entries {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"message_id":"` + e.MessageID + `","sender_session_id":"s","sender_name":"n","sender_principal":"p"`)
		if !e.ReceivedAt.IsZero() {
			b.WriteString(`,"received_at":"` + e.ReceivedAt.Format(time.RFC3339Nano) + `"`)
		}
		b.WriteByte('}')
	}
	b.WriteString(`],"dropped_total":0}`)
	return b.Bytes()
}

func TestPendingSaveIs0600InA0700Dir(t *testing.T) {
	t.Parallel()
	s := pendingStore(t)
	if err := s.Save(PendingFile{}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(s.Path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("file: %v %v", info, err)
	}
	for dir := filepath.Dir(s.Path); strings.HasSuffix(dir, "state") || strings.HasSuffix(dir, "pending"); dir = filepath.Dir(dir) {
		if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("dir %s: %v %v", dir, info, err)
		}
	}
	if got, err := s.Load(); err != nil || len(got.Entries) != 0 {
		t.Fatalf("empty save reloads as %+v %v", got, err)
	}
	raw, _ := os.ReadFile(s.Path)
	if !bytes.Contains(raw, []byte(`"entries":[]`)) {
		t.Fatalf("an empty document must carry an empty array, not null: %s", raw)
	}
}

func TestReleaseRoundTripAndConsume(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "state-root")
	path := ReleasePath(root, "brigade-sess-1")
	if _, _, err := ReadRelease(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	if err := WriteRelease(path, ReleaseFile{SessionID: "brigade-sess-1", MessageIDs: []string{"m1", "m2", "m1", "", strings.Repeat("x", MaxMessageIDBytes+1)}, WrittenAt: stateNow}); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("release file: %v %v", info, err)
	}
	f, raw, err := ReadRelease(path)
	if err != nil || f.Version != ReleaseFileVersion || f.SessionID != "brigade-sess-1" || strings.Join(f.MessageIDs, ",") != "m1,m2" || !f.WrittenAt.Equal(stateNow) {
		t.Fatalf("read back %+v %v", f, err)
	}
	// The compare-then-delete arm: a second batch written between the
	// read and the consume leaves different bytes, so the consume is a
	// no-op and the file survives for the next tick.
	if err := WriteRelease(path, ReleaseFile{SessionID: "brigade-sess-1", MessageIDs: []string{"m1", "m2", "m3"}, WrittenAt: stateNow}); err != nil {
		t.Fatal(err)
	}
	if removed, err := ConsumeRelease(path, raw); err != nil || removed {
		t.Fatalf("consume with stale bytes: removed=%v err=%v, want false and nil", removed, err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("the rewritten release file was removed: %v", err)
	}
	f2, raw2, err := ReadRelease(path)
	if err != nil || strings.Join(f2.MessageIDs, ",") != "m1,m2,m3" {
		t.Fatalf("second read %+v %v", f2, err)
	}
	if removed, err := ConsumeRelease(path, raw2); err != nil || !removed {
		t.Fatalf("consume with current bytes: removed=%v err=%v", removed, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("release file survived its consume: %v", err)
	}
	if removed, err := ConsumeRelease(path, raw2); err != nil || removed {
		t.Fatalf("consume of a missing file: %v %v", removed, err)
	}
	// Refusals are fixed-text.
	if err := os.WriteFile(path, []byte(`{"version":7,"session_id":"brigade-sess-1","message_ids":["m1"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadRelease(path); !errors.Is(err, errReleaseFileVersion) {
		t.Fatalf("version: %v", err)
	}
	if err := os.WriteFile(path, []byte("nope"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadRelease(path); !errors.Is(err, errReleaseFileMalformed) {
		t.Fatalf("malformed: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode is the PRECONDITION
		t.Fatal(err)
	}
	var perr *protocol.Error
	if _, _, err := ReadRelease(path); !errors.As(err, &perr) || perr.Details["reason"] != "insecure_mode" {
		t.Fatalf("insecure mode: %v", err)
	}
}

func TestMergeRelease(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "state-root")
	path := ReleasePath(root, "brigade-sess-1")
	f, disregarded, err := MergeRelease(path, "brigade-sess-1", []string{"m1"}, stateNow)
	if err != nil || disregarded != "" || strings.Join(f.MessageIDs, ",") != "m1" {
		t.Fatalf("first merge: %+v %q %v", f, disregarded, err)
	}
	f, disregarded, err = MergeRelease(path, "brigade-sess-1", []string{"m2", "m1"}, stateNow.Add(time.Second))
	if err != nil || disregarded != "" || strings.Join(f.MessageIDs, ",") != "m1,m2" {
		t.Fatalf("second merge must union in order: %+v %q %v", f, disregarded, err)
	}
	got, _, err := ReadRelease(path)
	if err != nil || strings.Join(got.MessageIDs, ",") != "m1,m2" || !got.WrittenAt.Equal(stateNow.Add(time.Second)) {
		t.Fatalf("on disk %+v %v", got, err)
	}
	// A file at this path naming another session is planted or corrupt:
	// replaced, and the caller is told.
	if err := WriteRelease(path, ReleaseFile{SessionID: "brigade-sess-2", MessageIDs: []string{"x"}}); err != nil {
		t.Fatal(err)
	}
	f, disregarded, err = MergeRelease(path, "brigade-sess-1", []string{"m3"}, stateNow)
	if err != nil || disregarded != "foreign_session" || strings.Join(f.MessageIDs, ",") != "m3" {
		t.Fatalf("foreign merge: %+v %q %v", f, disregarded, err)
	}
	// The cap.
	ids := make([]string, 0, MaxReleaseIDs+5)
	for i := range MaxReleaseIDs + 5 {
		ids = append(ids, "id"+strconv.Itoa(i))
	}
	f, _, err = MergeRelease(path, "brigade-sess-1", ids, stateNow)
	if err != nil || len(f.MessageIDs) != MaxReleaseIDs || f.MessageIDs[0] != "m3" {
		t.Fatalf("cap: %d ids, first %q, err %v", len(f.MessageIDs), f.MessageIDs[0], err)
	}
}
