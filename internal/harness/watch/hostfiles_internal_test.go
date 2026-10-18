package watch

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// Reproduce the cached-path escape with matching native IDs, then exercise
// rediscovery and normal refresh after the original directory is restored.
func TestHostFactsDirectoryReplacement(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostCodex, config.HostMuse} {
		for _, component := range []string{"root", "sessions", "year", "month", "day", "journal-parent"} {
			for _, cached := range []bool{false, true} {
				name := string(host) + "/" + component + "/cold"
				if cached {
					name = string(host) + "/" + component + "/cached"
				}
				t.Run(name, func(t *testing.T) {
					t.Parallel()
					root := filepath.Join(t.TempDir(), "host")
					file := "muse-session.jsonl"
					if host == config.HostCodex {
						file = "codex-rollout.jsonl"
					}
					original := factsFixture(t, file)
					path := factsLog(root, host)
					putFacts(t, path, original)
					putFacts(t, filepath.Join(root, "session_index.jsonl"), factsFixture(t, "codex-index.jsonl"))
					r := hostFactsReader{host: host, root: root, id: factsID}
					var before localHostFacts
					if cached {
						before = r.refresh()
						if !before.hasContext {
							t.Fatal("fixture not discovered")
						}
					}
					parents := map[string]string{
						"root": root, "sessions": filepath.Join(root, "sessions"),
						"year":           filepath.Join(root, "sessions", "2026"),
						"month":          filepath.Join(root, "sessions", "2026", "10"),
						"day":            filepath.Join(root, "sessions", "2026", "10", "18"),
						"journal-parent": filepath.Dir(path),
					}
					parent := parents[component]
					outside := t.TempDir()
					rel, err := filepath.Rel(parent, path)
					if err != nil {
						t.Fatal(err)
					}
					changed := strings.NewReplacer("73000", "777", "42000", "777", "renamed muse", "outside tree").Replace(original)
					putFacts(t, filepath.Join(outside, rel), changed)
					putFacts(t, filepath.Join(outside, "session_index.jsonl"), `{"id":"`+factsID+`","thread_name":"outside tree"}`+"\n")
					if err := os.Rename(parent, parent+"-saved"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(outside, parent); err != nil {
						t.Fatal(err)
					}
					for range 2 { // The second poll exercises invalidation/rediscovery.
						got := r.refresh()
						if got.tokens == 777 || got.name == "outside tree" || got.hasContext != before.hasContext {
							t.Fatalf("adopted replaced directory: %+v", got)
						}
					}
					if err := os.Remove(parent); err != nil {
						t.Fatal(err)
					}
					if err := os.Rename(parent+"-saved", parent); err != nil {
						t.Fatal(err)
					}
					putFacts(t, path, strings.NewReplacer("73000", "123", "42000", "123", "renamed muse", "restored name").Replace(original))
					putFacts(t, filepath.Join(root, "session_index.jsonl"), `{"id":"`+factsID+`","thread_name":"restored name"}`+"\n")
					if got := r.refresh(); !got.hasContext || got.tokens != 123 || got.name != "restored name" {
						t.Fatalf("normal refresh after restoration: %+v", got)
					}
				})
			}
		}
	}
}

func TestHostFactsRootIdentity(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostCodex, config.HostMuse} {
		for _, ancestor := range []bool{false, true} {
			name := string(host) + "/root"
			if ancestor {
				name = string(host) + "/ancestor"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				base := filepath.Join(t.TempDir(), "base")
				root := filepath.Join(base, "host")
				file := "muse-session.jsonl"
				if host == config.HostCodex {
					file = "codex-rollout.jsonl"
				}
				original := factsFixture(t, file)
				putFacts(t, factsLog(root, host), original)
				putFacts(t, filepath.Join(root, "session_index.jsonl"), factsFixture(t, "codex-index.jsonl"))
				r := hostFactsReader{host: host, root: root, id: factsID}
				before := r.refresh()
				if !before.hasContext {
					t.Fatal("fixture not discovered")
				}
				parent := root
				if ancestor {
					parent = base
				}
				if err := os.Rename(parent, parent+"-saved"); err != nil {
					t.Fatal(err)
				}
				if ancestor {
					if err := os.Symlink(t.TempDir(), parent); err != nil {
						t.Fatal(err)
					}
				}
				// A different real root (including one reached via a changed
				// ancestor) must not become trusted after cache invalidation.
				putFacts(t, factsLog(root, host), strings.NewReplacer("73000", "777", "42000", "777").Replace(original))
				putFacts(t, filepath.Join(root, "session_index.jsonl"), `{"id":"`+factsID+`","thread_name":"outside tree"}`+"\n")
				r.path = ""
				for range 2 {
					if got := r.refresh(); got != before {
						t.Fatalf("adopted new root: %+v", got)
					}
				}
			})
		}
	}
}

func TestHostFactsIndexReplacement(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	index := filepath.Join(root, "session_index.jsonl")
	original := factsFixture(t, "codex-index.jsonl")
	putFacts(t, index, original)
	r := hostFactsReader{host: config.HostCodex, root: root, id: factsID}
	before := r.refresh()
	if before.name != "renamed codex" {
		t.Fatal(before)
	}
	outside := filepath.Join(t.TempDir(), "index")
	putFacts(t, outside, `{"id":"`+factsID+`","thread_name":"outside tree"}`+"\n")
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, index); err != nil {
		t.Fatal(err)
	}
	if got := r.refresh(); got != before {
		t.Fatalf("followed index symlink: %+v", got)
	}
	if err := os.Remove(index); err != nil {
		t.Fatal(err)
	}
	putFacts(t, index, strings.ReplaceAll(original, "renamed codex", "restored name"))
	if got := r.refresh(); got.name != "restored name" {
		t.Fatalf("index replacement not refreshed: %+v", got)
	}
}

// A path replacement after acquiring the anchor cannot redirect an open.
// This deterministically exercises the check/open boundary without a race loop.
func TestHostFactsOpenedRootAnchor(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "host")
	putFacts(t, filepath.Join(root, "sessions", "record"), "{}\n")
	dir, _ := openHostRoot(root)
	if dir == nil {
		t.Fatal("no root")
	}
	defer func() { _ = dir.Close() }()
	if err := os.Rename(root, root+"-saved"); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	putFacts(t, filepath.Join(outside, "sessions", "record"), "outside\n")
	if err := os.Symlink(outside, root); err != nil {
		t.Fatal(err)
	}
	f, fi := openHostPath(dir, "sessions/record", false)
	if f == nil {
		t.Fatal("lost opened anchor")
	}
	defer func() { _ = f.Close() }()
	var lines []string
	readHostLines(f, 0, fi.Size(), func(b []byte) { lines = append(lines, string(b)) })
	if len(lines) != 1 || lines[0] != "{}" {
		t.Fatalf("open redirected: %q", lines)
	}
	for _, path := range []string{"../record", "sessions/../../record", "/record", "sessions/../record"} {
		if f, _ := openHostPath(dir, path, false); f != nil {
			_ = f.Close()
			t.Fatalf("accepted non-local or unclean path: %s", path)
		}
	}
}
