package registry_test

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/registry"
	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

func TestReadTheObservedEntry(t *testing.T) {
	t.Parallel()
	rec := fakeregistry.New(t, map[int]string{fakeregistry.ExamplePID: fakeregistry.Example})
	got, err := registry.Read(rec, fakeregistry.ExamplePID)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	want := registry.Entry{
		Found:               true,
		Name:                "15-create-team-session-messaging",
		NameSource:          "user",
		Status:              registry.StatusBusy,
		MessagingSocketPath: "/tmp/cc-socks/632.sock",
		Entrypoint:          "cli",
		Kind:                "interactive",
		Version:             "2.1.251",
	}
	if got != want {
		t.Fatalf("Read =\n %+v\nwant\n %+v", got, want)
	}
	if got.Activity() != registry.StatusBusy {
		t.Fatalf("Activity = %q", got.Activity())
	}
	if opened := rec.Opened(); !slices.Equal(opened, []string{"632.json"}) {
		t.Fatalf("opened %v, want exactly 632.json", opened)
	}
}

func TestReadMissingEntry(t *testing.T) {
	t.Parallel()
	rec := fakeregistry.New(t, map[int]string{fakeregistry.ExamplePID: fakeregistry.Example})
	got, err := registry.Read(rec, 4242)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want not exist (diagnostic only)", err)
	}
	if got != (registry.Entry{}) {
		t.Fatalf("missing entry is not zero: %+v", got)
	}
	if got.Activity() != registry.StatusIdle {
		t.Fatalf("Activity of a missing entry = %q, want idle", got.Activity())
	}
	if opened := rec.Opened(); !slices.Equal(opened, []string{"4242.json"}) {
		t.Fatalf("opened %v", opened)
	}
}

func TestReadMalformedEntries(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		content string
	}{
		{"not json", "{not json"},
		{"an array", "[]"},
		{"a string", `"busy"`},
		{"empty", ""},
		{"duplicate member", `{"name":"a","name":"b"}`},
		{"invalid utf-8", "{\"name\":\"\xff\"}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			rec := fakeregistry.New(t, map[int]string{4242: tc.content})
			got, err := registry.Read(rec, 4242)
			if err == nil {
				t.Fatal("no diagnostic error for a malformed entry")
			}
			if tc.content != "" && strings.Contains(err.Error(), tc.content) {
				t.Fatalf("diagnostic echoes file content: %q", err)
			}
			if got != (registry.Entry{}) {
				t.Fatalf("malformed entry is not zero: %+v", got)
			}
		})
	}
}

func TestReadSkipsMembersOfTheWrongTypeButKeepsTheRest(t *testing.T) {
	t.Parallel()
	rec := fakeregistry.New(t, map[int]string{4242: `{"name":5,"nameSource":null,"status":"busy","messagingSocketPath":["x"],"version":"2.1.259"}`})
	got, err := registry.Read(rec, 4242)
	if err != nil {
		t.Fatal(err)
	}
	want := registry.Entry{Found: true, Status: registry.StatusBusy, Version: "2.1.259"}
	if got != want {
		t.Fatalf("Read =\n %+v\nwant\n %+v", got, want)
	}
}

func TestReadNormalisesStatus(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		status string // the JSON value, or "" for no member
		want   string
	}{
		{"busy", `"busy"`, registry.StatusBusy},
		{"idle", `"idle"`, registry.StatusIdle},
		{"wrong case", `"BUSY"`, registry.StatusIdle},
		{"unknown word", `"asleep"`, registry.StatusIdle},
		{"empty string", `""`, registry.StatusIdle},
		{"a number", `1`, registry.StatusIdle},
		{"absent", "", registry.StatusIdle},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			content := `{"name":"n"}`
			if tc.status != "" {
				content = `{"name":"n","status":` + tc.status + `}`
			}
			rec := fakeregistry.New(t, map[int]string{4242: content})
			got, err := registry.Read(rec, 4242)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Found || got.Status != tc.want || got.Activity() != tc.want {
				t.Fatalf("status %s → Status %q Activity %q, want %q", tc.status, got.Status, got.Activity(), tc.want)
			}
		})
	}
}

func TestReadRefusesANonPositivePIDWithoutOpening(t *testing.T) {
	t.Parallel()
	rec := fakeregistry.New(t, nil)
	for _, pid := range []int{0, -632} {
		got, err := registry.Read(rec, pid)
		if err == nil || got.Found {
			t.Fatalf("Read(%d) = %+v, %v", pid, got, err)
		}
	}
	if opened := rec.Opened(); len(opened) != 0 {
		t.Fatalf("opened %v for an invalid pid", opened)
	}
}

func TestReadOversizeEntryIsNotFound(t *testing.T) {
	t.Parallel()
	big := `{"name":"` + strings.Repeat("x", registry.MaxEntryBytes) + `"}`
	rec := fakeregistry.New(t, map[int]string{4242: big})
	got, err := registry.Read(rec, 4242)
	if err == nil || got.Found {
		t.Fatalf("oversize entry accepted: %+v, %v", got, err)
	}
	// Positive control: an entry padded to exactly the cap reads.
	body := `{"name":"n"}`
	exact := body[:len(body)-1] + strings.Repeat(" ", registry.MaxEntryBytes-len(body)) + "}"
	if len(exact) != registry.MaxEntryBytes {
		t.Fatalf("padding arithmetic: %d", len(exact))
	}
	rec = fakeregistry.New(t, map[int]string{4242: exact})
	if got, err := registry.Read(rec, 4242); err != nil || !got.Found || got.Name != "n" {
		t.Fatalf("entry at the cap refused: %+v, %v", got, err)
	}
}

func TestReadNeverOpensAKeyAcrossEveryPath(t *testing.T) {
	t.Parallel()
	// The recorder already fails the test on any *.key open; this makes
	// the property explicit over every path Read can take.
	rec := fakeregistry.New(t, map[int]string{632: fakeregistry.Example, 700: "{bad"})
	for _, pid := range []int{632, 700, 4242, 0} {
		_, _ = registry.Read(rec, pid)
	}
	for _, name := range rec.Opened() {
		if strings.HasSuffix(name, ".key") || !strings.HasSuffix(name, ".json") {
			t.Fatalf("Read opened %q", name)
		}
	}
	if opened := rec.Opened(); !slices.Equal(opened, []string{"632.json", "700.json", "4242.json"}) {
		t.Fatalf("opened %v", opened)
	}
}

// mutantRead is the reader a careless implementation might be: it opens
// the peer auth key beside the entry "to check the session is real"
// before reading the entry. It exists only to prove the recorder bites.
func mutantRead(fsys fs.FS, pid int) (registry.Entry, error) {
	if f, err := fsys.Open(fakeregistry.KeyName(pid)); err == nil {
		_ = f.Close()
	}
	return registry.Read(fsys, pid)
}

func TestRecorderCatchesAReaderThatOpensTheKey(t *testing.T) {
	t.Parallel()
	entries := map[int]string{632: fakeregistry.Example}

	spy := fakeregistry.NewSpy(t)
	rec := fakeregistry.New(spy, entries)
	if _, err := mutantRead(rec, 632); err != nil {
		t.Fatalf("the mutant still reads the entry: %v", err)
	}
	if !spy.Failed() {
		t.Fatal("the recorder did not catch the mutant opening the key")
	}
	if msgs := spy.Messages(); len(msgs) != 1 || !strings.Contains(msgs[0], fakeregistry.KeyName(632)) {
		t.Fatalf("messages = %v", msgs)
	}

	// Positive control: the real reader against the same recorder and a
	// fresh spy leaves the spy clean.
	spy = fakeregistry.NewSpy(t)
	rec = fakeregistry.New(spy, entries)
	if _, err := registry.Read(rec, 632); err != nil {
		t.Fatal(err)
	}
	if spy.Failed() {
		t.Fatalf("the real reader tripped the recorder: %v", spy.Messages())
	}
}

func TestDirReadsSessionsUnderTheConfigDir(t *testing.T) {
	t.Parallel()
	// The os.DirFS positive control: a real file under
	// <config>/sessions/<pid>.json is read, and the key beside it is not
	// (proven by the recorder tests above; here by the real filesystem
	// simply working end to end).
	configDir := t.TempDir()
	sessions := filepath.Join(configDir, "sessions")
	if err := os.MkdirAll(sessions, 0o700); err != nil {
		t.Fatal(err)
	}
	content := fakeregistry.Observed(4242, "payments-api", "idle", "/tmp/cc-socks/4242.sock")
	if err := os.WriteFile(filepath.Join(sessions, "4242.json"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessions, fakeregistry.KeyName(4242)), []byte(fakeregistry.KeyContent), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := registry.Read(registry.Dir(configDir), 4242)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Found || got.Name != "payments-api" || got.Status != registry.StatusIdle || got.MessagingSocketPath != "/tmp/cc-socks/4242.sock" || got.Version != "2.1.259" {
		t.Fatalf("Read = %+v", got)
	}
	if registry.FileName(4242) != "4242.json" {
		t.Fatalf("FileName = %q", registry.FileName(4242))
	}
}
