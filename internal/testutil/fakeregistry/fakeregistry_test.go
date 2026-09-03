package fakeregistry_test

import (
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil/fakeregistry"
)

func TestRecorderServesEntriesAndRecordsOpens(t *testing.T) {
	t.Parallel()
	spy := fakeregistry.NewSpy(t)
	rec := fakeregistry.New(spy, map[int]string{fakeregistry.ExamplePID: fakeregistry.Example})
	f, err := rec.Open(fakeregistry.EntryName(fakeregistry.ExamplePID))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	data, err := io.ReadAll(f)
	_ = f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != fakeregistry.Example {
		t.Fatalf("content = %q", data)
	}
	if got := rec.Opened(); !slices.Equal(got, []string{"632.json"}) {
		t.Fatalf("Opened = %v", got)
	}
	if spy.Failed() {
		t.Fatalf("a legitimate open failed the spy: %v", spy.Messages())
	}
}

func TestRecorderMissingEntryIsAnOrdinaryMiss(t *testing.T) {
	t.Parallel()
	spy := fakeregistry.NewSpy(t)
	rec := fakeregistry.New(spy, nil)
	_, err := rec.Open("4242.json")
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Open(missing) = %v, want not exist", err)
	}
	if spy.Failed() {
		t.Fatalf("a miss must not fail the test: %v", spy.Messages())
	}
	if got := rec.Opened(); !slices.Equal(got, []string{"4242.json"}) {
		t.Fatalf("Opened = %v", got)
	}
}

func TestRecorderFailsTheTestOnAKeyOpen(t *testing.T) {
	t.Parallel()
	spy := fakeregistry.NewSpy(t)
	rec := fakeregistry.New(spy, map[int]string{632: fakeregistry.Example})
	for _, name := range []string{fakeregistry.KeyName(632), fakeregistry.KeyName(fakeregistry.StrayKeyPID), "anything.key"} {
		f, err := rec.Open(name)
		if err == nil {
			_ = f.Close()
			t.Fatalf("Open(%q) succeeded; key material must never be readable", name)
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("Open(%q) = %v, want permission", name, err)
		}
	}
	if !spy.Failed() {
		t.Fatal("opening a *.key did not fail the test")
	}
	msgs := spy.Messages()
	if len(msgs) != 3 {
		t.Fatalf("messages = %v, want one per key open", msgs)
	}
	for _, m := range msgs {
		if !strings.Contains(m, ".key") || strings.Contains(m, fakeregistry.KeyContent) {
			t.Fatalf("message %q must name the key file and never its content", m)
		}
	}
}

func TestRecorderFailsTheTestOnADirectoryListing(t *testing.T) {
	t.Parallel()
	spy := fakeregistry.NewSpy(t)
	rec := fakeregistry.New(spy, map[int]string{632: fakeregistry.Example})
	if _, err := fs.ReadDir(rec, "."); err == nil {
		t.Fatal("ReadDir succeeded; the reader must never list the registry")
	}
	if !spy.Failed() {
		t.Fatal("listing the directory did not fail the test")
	}
	if got := rec.Opened(); !slices.Equal(got, []string{"."}) {
		t.Fatalf("Opened = %v", got)
	}
}

func TestRecorderFailsTheTestOnANameOutsideTheContract(t *testing.T) {
	t.Parallel()
	spy := fakeregistry.NewSpy(t)
	rec := fakeregistry.New(spy, nil)
	for _, name := range []string{"632.json.bak", "notes.txt", "sessions/632.json", "632"} {
		if _, err := rec.Open(name); err == nil {
			t.Fatalf("Open(%q) succeeded", name)
		}
	}
	if !spy.Failed() {
		t.Fatal("an off-contract name did not fail the test")
	}
}

func TestObservedIsA3Shaped(t *testing.T) {
	t.Parallel()
	text := fakeregistry.Observed(4242, "payments-api", "idle", "/tmp/cc-socks/4242.sock")
	if strings.Count(text, "\n") != 0 {
		t.Fatalf("Observed is not one line: %q", text)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	var want map[string]any
	if err := json.Unmarshal([]byte(fakeregistry.Example), &want); err != nil {
		t.Fatal(err)
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("Observed lacks the observed member %q", k)
		}
	}
	for k := range got {
		if _, ok := want[k]; !ok {
			t.Fatalf("Observed invents a member %q", k)
		}
	}
	if got["pid"] != float64(4242) || got["name"] != "payments-api" || got["status"] != "idle" || got["messagingSocketPath"] != "/tmp/cc-socks/4242.sock" {
		t.Fatalf("Observed values: %v", got)
	}
}

func TestSpyRecordsWithoutFailingTheRealTest(t *testing.T) {
	t.Parallel()
	spy := fakeregistry.NewSpy(t)
	if spy.Failed() {
		t.Fatal("fresh spy reports failed")
	}
	spy.Errorf("formatted %d %s", 1, "x")
	if !spy.Failed() {
		t.Fatal("Errorf did not mark the spy failed")
	}
	if got := spy.Messages(); !slices.Equal(got, []string{"formatted 1 x"}) {
		t.Fatalf("Messages = %v", got)
	}
	// The real t is untouched: this test is passing as it reads this line.
}
