package inbound

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
)

// These tests use real files under t.TempDir and no synctest bubble.

func seenStore(t *testing.T) FileSeenStore {
	t.Helper()
	return FileSeenStore{Path: SeenPath(filepath.Join(t.TempDir(), "state-root"), "brigade-sess-1")}
}

// TestSeenPath is the encoding table of P5-14 (3.2): a safe id is the stem
// verbatim, anything else is its SHA-256 with the ".sha256" suffix, and
// EVERY row — "..", a separator, a NUL, the empty string, a 4 KiB string —
// lands in ${stateDir}/state/seen, which is the assertion that matters.
func TestSeenPath(t *testing.T) {
	t.Parallel()
	const root = "/s"
	dir := filepath.Join(root, "state", "seen")
	digest := func(id string) string {
		sum := sha256.Sum256([]byte(id))
		return hex.EncodeToString(sum[:]) + ".sha256"
	}
	hex32 := strings.Repeat("0123456789abcdef", 2)
	uuid := "c876fc4e-72c1-44eb-9027-f5d03941e8d1"
	id64 := strings.Repeat("a", 64)
	id65 := strings.Repeat("a", 65)
	big := strings.Repeat("z", 4096)
	traversal := "../../sessions/by-pid/1"
	rows := []struct {
		name, id, stem string
		plain          bool
	}{
		{"fs adapter 32 hex", hex32, hex32, true},
		{"supabase uuid", uuid, uuid, true},
		{"underscore, dash, case", "Sess_01-x", "Sess_01-x", true},
		{"64 bytes (the boundary)", id64, id64, true},
		{"65 bytes", id65, digest(id65), false},
		{"separator", "a/b", digest("a/b"), false},
		{"backslash", `a\b`, digest(`a\b`), false},
		{"dot-dot", "..", digest(".."), false},
		{"traversal", traversal, digest(traversal), false},
		{"dot", "a.b", digest("a.b"), false},
		{"digest-shaped text", "x.sha256", digest("x.sha256"), false},
		{"NUL", "a\x00b", digest("a\x00b"), false},
		{"newline", "a\nb", digest("a\nb"), false},
		{"space", "a b", digest("a b"), false},
		{"non-ASCII", "sessión", digest("sessión"), false},
		{"empty", "", digest(""), false},
		{"4 KiB", big, digest(big), false},
	}
	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			t.Parallel()
			got := SeenPath(root, r.id)
			if want := filepath.Join(dir, r.stem+".json"); got != want {
				t.Fatalf("SeenPath(%q) = %q, want %q", r.id, got, want)
			}
			// The anti-escape assertion: the file is directly in
			// state/seen whatever the id holds.
			if filepath.Dir(got) != dir {
				t.Fatalf("SeenPath(%q) = %q escapes %q", r.id, got, dir)
			}
			base := filepath.Base(got)
			if strings.ContainsAny(base, "/\\\x00\n") || !strings.HasSuffix(base, ".json") {
				t.Fatalf("SeenPath(%q) base %q", r.id, base)
			}
			if safeSeenStem(r.id) != r.plain {
				t.Fatalf("safeSeenStem(%q) = %v, want %v", r.id, !r.plain, r.plain)
			}
			if !r.plain && len(base) != 64+len(".sha256.json") {
				t.Fatalf("digest name %q is %d bytes, want %d", base, len(base), 64+len(".sha256.json"))
			}
		})
	}
	// Injective across the branches: a plain stem never contains ".", so no
	// plain id can spell another id's digest name, and distinct unsafe ids
	// have distinct digests. Only the suffix keeps the branches apart: an
	// unsafe id whose digest text is itself a safe stem would otherwise
	// collide with the session literally named by those 64 hex digits.
	if SeenPath(root, "..") == SeenPath(root, "a/b") {
		t.Fatal("two unsafe ids share a path")
	}
	if strings.Contains(seenStem(hex32), ".") || !strings.Contains(seenStem(".."), ".sha256") {
		t.Fatalf("stems: plain %q, digest %q", seenStem(hex32), seenStem(".."))
	}
	sum := sha256.Sum256([]byte(".."))
	if literal := hex.EncodeToString(sum[:]); !safeSeenStem(literal) || SeenPath(root, literal) == SeenPath(root, "..") {
		t.Fatalf("the digest text of %q is a safe stem, so without the suffix it would collide: %q", "..", SeenPath(root, literal))
	}
	// The join is filepath's, nothing more: a relative root stays relative.
	if got := SeenPath("rel/root", "s1"); got != filepath.Join("rel", "root", "state", "seen", "s1.json") {
		t.Fatalf("relative root: %q", got)
	}
}

// TestSeenStemAgreesWithCheckNativeID is the drift join of 3.2: the plain
// branch of SeenPath and sessionmap.CheckNativeID (sessionmap/bynative.go:39-59)
// must partition every CHARSET case the same way, so a character one
// accepts and the other refuses cannot creep in silently. The length caps
// differ by design — 64 here (the fs adapter's safeRef, fs/store.go:172-190)
// against MaxNativeIDLen 80 there — so the charset rows stay within 64
// bytes and the one length divergence is pinned explicitly rather than
// hidden. inbound itself must not import sessionmap; this test file may.
func TestSeenStemAgreesWithCheckNativeID(t *testing.T) {
	t.Parallel()
	cases := []string{
		"a", "s1", "brigade-sess-1", "c876fc4e-72c1-44eb-9027-f5d03941e8d1",
		strings.Repeat("0123456789abcdef", 2), "ABC_xyz-09", "-", "_", strings.Repeat("a", 64),
		"", ".", "..", "a.b", "x.sha256", "a/b", `a\b`, "a b", "a\tb", "a\nb", "a\x00b", "a\x7fb",
		"sessión", "日本", "a:b", "a@b", "a+b", "a~b", "a%2fb", "a=b", "a,b", "a;b", "'a'", `"a"`,
	}
	for _, id := range cases {
		native := sessionmap.CheckNativeID(id) == nil
		if plain := safeSeenStem(id); plain != native {
			t.Errorf("id %q: safeSeenStem %v, CheckNativeID accepts %v", id, plain, native)
		}
	}
	// The pinned divergence: 65–80 bytes is a native id but not a plain
	// stem; above 80 both refuse.
	id65, id80, id81 := strings.Repeat("a", 65), strings.Repeat("a", sessionmap.MaxNativeIDLen), strings.Repeat("a", sessionmap.MaxNativeIDLen+1)
	if sessionmap.CheckNativeID(id65) != nil || safeSeenStem(id65) || sessionmap.CheckNativeID(id80) != nil || safeSeenStem(id80) {
		t.Fatal("65-80 bytes: want CheckNativeID to accept and safeSeenStem to refuse")
	}
	if sessionmap.CheckNativeID(id81) == nil || safeSeenStem(id81) {
		t.Fatal("81 bytes: both must refuse")
	}
	if maxPlainSeenStem >= sessionmap.MaxNativeIDLen {
		t.Fatalf("maxPlainSeenStem %d is not below MaxNativeIDLen %d; drop the divergence rows", maxPlainSeenStem, sessionmap.MaxNativeIDLen)
	}
}

func TestFileSeenStoreRoundTripModesAndMissing(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	ids, err := s.Load()
	if err != nil || ids != nil {
		t.Fatalf("missing file: (%v, %v), want (nil, nil)", ids, err)
	}
	want := []string{"m1", "m2", "m3"}
	if err := s.Save(want); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("file mode %o, want 0600", fi.Mode().Perm())
	}
	// The whole chain Save created is 0700: state/seen and state above it.
	for _, dir := range []string{filepath.Dir(s.Path), filepath.Dir(filepath.Dir(s.Path))} {
		di, err := os.Stat(dir)
		if err != nil {
			t.Fatal(err)
		}
		if di.Mode().Perm() != 0o700 {
			t.Fatalf("%s mode %o, want 0700", dir, di.Mode().Perm())
		}
	}
	got, err := s.Load()
	if err != nil || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("Load = (%v, %v), want %v", got, err, want)
	}
	// Overwrite semantics: a later Save replaces, never appends.
	if err := s.Save([]string{"m9"}); err != nil {
		t.Fatal(err)
	}
	got, err = s.Load()
	if err != nil || len(got) != 1 || got[0] != "m9" {
		t.Fatalf("after overwrite: (%v, %v)", got, err)
	}
	// An empty list is a valid file that loads as empty.
	if err := s.Save(nil); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || len(got) != 0 {
		t.Fatalf("empty list: (%v, %v)", got, err)
	}
}

func TestFileSeenStoreBounded(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	ids := make([]string, 0, SeenCapacity+500)
	for i := range SeenCapacity + 500 {
		ids = append(ids, "id-"+strconv.Itoa(i))
	}
	if err := s.Save(ids); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != SeenCapacity || got[0] != "id-500" || got[len(got)-1] != "id-"+strconv.Itoa(SeenCapacity+499) {
		t.Fatalf("bounded load: %d ids, first %q, last %q", len(got), got[0], got[len(got)-1])
	}
	// The file itself holds only the bound (not merely the reader).
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), `"id-499"`) || !strings.Contains(string(data), `"id-500"`) {
		t.Fatal("file was not bounded at save time")
	}
}

func TestFileSeenStoreRefusesInsecureMode(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	if err := s.Save([]string{"m1"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(s.Path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode is what this test refuses
		t.Fatal(err)
	}
	_, err := s.Load()
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeConfig || perr.Details["reason"] != "insecure_mode" {
		t.Fatalf("0644 seen file: err %v, want config/insecure_mode", err)
	}
	// Positive control: back to 0600 it loads.
	if err := os.Chmod(s.Path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Load(); err != nil || len(got) != 1 {
		t.Fatalf("0600 seen file: (%v, %v)", got, err)
	}
	// A Save after a refusal replaces the insecure file with a 0600 one.
	if err := os.Chmod(s.Path, 0o644); err != nil { //nolint:gosec // G302: the insecure mode is what this test refuses
		t.Fatal(err)
	}
	if err := s.Save([]string{"m2"}); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode after re-save %o", fi.Mode().Perm())
	}
}

func TestFileSeenStoreMalformedAndBadIDs(t *testing.T) {
	t.Parallel()
	s := seenStore(t)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o700); err != nil {
		t.Fatal(err)
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(s.Path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{
		"not json":      `{"version":1,"message_ids":[`,
		"wrong version": `{"version":2,"message_ids":["a"]}`,
		"no version":    `{"message_ids":["a"]}`,
		"wrong type":    `{"version":1,"message_ids":"a"}`,
		"too large":     `{"version":1,"message_ids":["` + strings.Repeat("x", MaxSeenFileBytes) + `"]}`,
	} {
		write(content)
		if got, err := s.Load(); err == nil {
			t.Errorf("%s: loaded %v, want an error", name, got)
		}
	}
	// A file holding more than the bound loads as the LAST SeenCapacity.
	var many strings.Builder
	many.WriteString(`{"version":1,"message_ids":[`)
	for i := range SeenCapacity + 100 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`"f` + strconv.Itoa(i) + `"`)
	}
	many.WriteString(`]}`)
	write(many.String())
	got, err := s.Load()
	if err != nil || len(got) != SeenCapacity || got[0] != "f100" || got[len(got)-1] != "f"+strconv.Itoa(SeenCapacity+99) {
		t.Fatalf("over-bound file: %d ids, err %v", len(got), err)
	}
	// Bad ids are skipped, good ones kept, order preserved.
	write(`{"version":1,"message_ids":["", "ok1", "` + strings.Repeat("x", MaxMessageIDBytes+1) + `", "ok2", "bad�"]}`)
	got, err = s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "ok1,ok2,bad�" {
		t.Fatalf("filtered load = %q", got)
	}
}

func TestMemorySeenStore(t *testing.T) {
	t.Parallel()
	var m MemorySeenStore
	if got, err := m.Load(); err != nil || len(got) != 0 || m.Saves() != 0 {
		t.Fatalf("zero store: %v %v %d", got, err, m.Saves())
	}
	in := []string{"a", "b"}
	if err := m.Save(in); err != nil {
		t.Fatal(err)
	}
	in[0] = "mutated"
	got, _ := m.Load()
	if strings.Join(got, ",") != "a,b" || m.Saves() != 1 {
		t.Fatalf("Save did not copy: %v (saves %d)", got, m.Saves())
	}
	got[0] = "mutated"
	if ids := m.IDs(); ids[0] != "a" {
		t.Fatalf("Load did not copy: %v", ids)
	}
}

func TestFileSeenStoreSaveFailsWhenTheDirectoryCannotBeMade(t *testing.T) {
	t.Parallel()
	// The parent of the state directory is a regular file, so the 0700
	// chain cannot be created and Save reports it instead of writing
	// somewhere else.
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := FileSeenStore{Path: SeenPath(blocker, "s1")}
	if err := s.Save([]string{"m1"}); err == nil {
		t.Fatal("Save succeeded under a file")
	}
	if _, err := os.Stat(s.Path); err == nil {
		t.Fatal("something was written under the blocker file")
	}
}
