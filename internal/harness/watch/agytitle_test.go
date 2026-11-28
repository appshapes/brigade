package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
)

// TestAgyTitleReadsTheTopLevelTitle: the title field of a protobuf text
// document, with text format's escapes, and nothing else (work item 544).
func TestAgyTitleReadsTheTopLevelTitle(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ doc, want string }{
		{`title:"frank-brigade-antigravity-hydro"`, "frank-brigade-antigravity-hydro"},
		{"title: \"Brigade Sessions Command\"\n", "Brigade Sessions Command"},
		{"pinned: true\ntitle : \"two\"\nother: 3\n", "two"},
		{`title:"say \"hi\" it\'s \\ ok"`, `say "hi" it's \ ok`},
		{`title:"caf\303\251"`, "café"},
		{`title:"café"`, "café"},
		{"nested {\n  title: \"inner\"\n}\n", ""},
		{"nested { title: \"inner\" }\ntitle: \"outer\"\n", "outer"},
		{`title:'single'`, ""},
		{`title:"unterminated`, ""},
		{`titles:"other field"`, ""},
		{"", ""},
	} {
		if got := agyTitle(tc.doc); got != tc.want {
			t.Errorf("agyTitle(%q) = %q, want %q", tc.doc, got, tc.want)
		}
	}
}

func writeAnnotation(t *testing.T, home, id, body string) {
	t.Helper()
	dir := filepath.Join(home, ".gemini", "antigravity-cli", "annotations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".pbtxt"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestAntigravityTitleFromTheAnnotationFile: the conversation's own file,
// under HOME, sanitized; none for a missing file, an oversize one, a
// file of another shape or an id that is no UUID.
func TestAntigravityTitleFromTheAnnotationFile(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	environ := []string{adapterkit.HomeVar + "=" + home}
	const id = "7cff6781-5437-4b66-a028-177483e5b6d3"
	if got := AntigravityTitle(environ, id); got != "" {
		t.Fatalf("no file: %q", got)
	}
	writeAnnotation(t, home, id, `title:"frank-brigade-antigravity-hydro"`)
	if got := AntigravityTitle(environ, id); got != "frank-brigade-antigravity-hydro" {
		t.Fatalf("title: %q", got)
	}
	writeAnnotation(t, home, id, "title:\"two\\nlines\"")
	if got := AntigravityTitle(environ, id); got != "two lines" {
		t.Fatalf("a title with a line break is not one line: %q", got)
	}
	const other = "d7c35604-5fca-4cdf-8ef2-25f685c47b93"
	writeAnnotation(t, home, other, `{"title":"json"}`)
	if got := AntigravityTitle(environ, other); got != "" {
		t.Fatalf("another shape: %q", got)
	}
	const big = "a552ea89-0e74-49fd-9649-3ac8418fb644"
	writeAnnotation(t, home, big, `title:"`+strings.Repeat("x", agyAnnotationBytes)+`"`)
	if got := AntigravityTitle(environ, big); got != "" {
		t.Fatalf("oversize: %q", got)
	}
	writeAnnotation(t, home, "../escape", `title:"no"`)
	for _, bad := range []string{"../escape", "7CFF6781-5437-4B66-A028-177483E5B6D3", ""} {
		if got := AntigravityTitle(environ, bad); got != "" {
			t.Fatalf("id %q: %q", bad, got)
		}
	}
	if got := AntigravityTitle(nil, id); got != "" {
		t.Fatalf("no HOME: %q", got)
	}
}

// TestTheWatcherReadsAnAgyRenameAtItsNextPoll: the host's reader, as the
// heartbeat asks it, follows a /rename and keeps the last title when the
// file goes away.
func TestTheWatcherReadsAnAgyRenameAtItsNextPoll(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	environ := []string{adapterkit.HomeVar + "=" + home}
	const id = "fb2d2cb6-0653-4345-8a87-f6e959887af3"
	r := &hostFactsReader{host: config.HostAntigravity, root: config.HostAntigravity.HistoryRoot(environ), id: id}
	if got := r.refresh().name; got != "" {
		t.Fatalf("before a title: %q", got)
	}
	writeAnnotation(t, home, id, `title:"Brigade Update Command"`)
	if got := r.refresh().name; got != "Brigade Update Command" {
		t.Fatalf("generated title: %q", got)
	}
	writeAnnotation(t, home, id, `title:"frank-agy-renamed"`)
	if got := r.refresh().name; got != "frank-agy-renamed" {
		t.Fatalf("after /rename: %q", got)
	}
	if err := os.Remove(filepath.Join(home, ".gemini", "antigravity-cli", "annotations", id+".pbtxt")); err != nil {
		t.Fatal(err)
	}
	if got := r.refresh().name; got != "frank-agy-renamed" {
		t.Fatalf("a removed file takes the title back: %q", got)
	}
}
