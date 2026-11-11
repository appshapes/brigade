//go:build !windows

package workitem

import (
	"os"
	"path/filepath"
	"testing"
)

// A symlink in the folder is a teammate's, like every file there: none
// is followed, as a work item's directory or as a change.
func TestReadFollowsNoSymlink(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	id := add(t, s, "Fix the login redirect", alice)

	// Another board's work item, reached through a symlinked directory.
	outside := Store{Folder: t.TempDir(), Now: c.now}
	other := add(t, outside, "From elsewhere", bob)
	if err := os.Symlink(filepath.Join(outside.Folder, ItemsDir, other), filepath.Join(s.Folder, ItemsDir, other)); err != nil {
		t.Fatal(err)
	}
	// A change that is a symlink to a real change of this work item.
	dir := filepath.Join(s.Folder, ItemsDir, id)
	files, err := os.ReadDir(dir)
	if err != nil || len(files) != 1 {
		t.Fatalf("change files = %v, %v", files, err)
	}
	if err := os.Symlink(filepath.Join(dir, files[0].Name()), filepath.Join(dir, "000000002-1762851601000-0a0b0c0d.json")); err != nil {
		t.Fatal(err)
	}

	b := read(t, s.Folder)
	if len(b.Items) != 1 || b.Items[0].ID != id || len(b.Items[0].History) != 1 {
		t.Fatalf("items = %+v", b.Items)
	}
	if b.Skipped != 2 {
		t.Errorf("skipped = %d, want 2", b.Skipped)
	}
}

// A teammate's machine can put a symlink where `items` or `lists`
// belongs: neither is read through, and nothing is written through one.
func TestNoTopLevelDirectoryIsFollowedThroughASymlink(t *testing.T) {
	c := newClock()
	outside := t.TempDir()
	for _, name := range []string{ItemsDir, ListsDir} {
		s := Store{Folder: t.TempDir(), Now: c.now}
		// Another board's directory, with something in it to be read.
		other := Store{Folder: outside + "-" + name, Now: c.now}
		add(t, other, "From elsewhere", bob)
		setLists(t, other, bob, "Theirs")
		if err := os.Symlink(filepath.Join(other.Folder, name), filepath.Join(s.Folder, name)); err != nil {
			t.Fatal(err)
		}
		b := read(t, s.Folder)
		if len(b.Items) != 0 || b.Lists[0].Name != "Todo" || b.Skipped != 1 {
			t.Errorf("%s as a symlink: items %d, lists %v, skipped %d", name, len(b.Items), b.Lists, b.Skipped)
		}
		before, err := os.ReadDir(filepath.Join(other.Folder, name))
		if err != nil {
			t.Fatal(err)
		}
		var werr error
		if name == ItemsDir {
			_, werr = s.Add(b, Fields{Title: ptr("T"), List: ptr("todo")}, alice)
		} else {
			werr = s.SetLists(b, []List{{Key: "mine", Name: "Mine"}}, alice)
		}
		if werr == nil {
			t.Errorf("%s as a symlink: a change was written through it", name)
		}
		after, err := os.ReadDir(filepath.Join(other.Folder, name))
		if err != nil || len(after) != len(before) {
			t.Errorf("%s as a symlink: the directory it points at went from %d to %d entries (%v)", name, len(before), len(after), err)
		}
		if it, err := ReadItem(s.Folder, "0123456789abcdef", nil); it != nil || err != nil {
			t.Errorf("%s as a symlink: ReadItem = %v, %v", name, it, err)
		}
	}
}
