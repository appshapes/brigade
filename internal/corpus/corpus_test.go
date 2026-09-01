// Package corpus holds no code: it exists so the P0-1 injection corpus
// under scripts/injection-corpus/ has a home in `go test ./...` where
// its manifest can be asserted against the files on disk. The corpus is
// consumed unchanged by the sanitiser fuzz seeds, E0-3, P4-2, P4-5, R9
// and the U-03/U-04 fixtures, so a file added, removed or renamed
// without updating expected.json (or vice versa) is a defect this test
// catches before those consumers do.
package corpus_test

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// corpusDir is scripts/injection-corpus/ relative to this test file.
func corpusDir() string {
	return filepath.Join("..", "..", "scripts", "injection-corpus")
}

// manifest mirrors the parts of expected.json this test asserts on.
type manifest struct {
	BenignBody string `json:"benign_body"`
	Items      []struct {
		File     string `json:"file"`
		Kind     string `json:"kind"`
		Expected string `json:"expected"`
	} `json:"items"`
}

func loadManifest(t *testing.T) manifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(corpusDir(), "expected.json"))
	if err != nil {
		t.Fatalf("read expected.json: %v", err)
	}
	var m manifest
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse expected.json: %v", err)
	}
	if len(m.Items) == 0 {
		t.Fatal("expected.json lists no items")
	}
	return m
}

// payloadFiles returns every *.txt payload actually on disk.
func payloadFiles(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(corpusDir())
	if err != nil {
		t.Fatalf("read corpus dir: %v", err)
	}
	var files []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		files = append(files, e.Name())
	}
	sort.Strings(files)
	return files
}

// TestManifestAndFilesAgree: every file named in expected.json exists on
// disk, every *.txt on disk is named in expected.json, and there are no
// duplicates — the manifest and the directory are in exact agreement.
func TestManifestAndFilesAgree(t *testing.T) {
	t.Parallel()
	m := loadManifest(t)

	named := map[string]int{}
	for _, it := range m.Items {
		named[it.File]++
	}
	for f, n := range named {
		if n > 1 {
			t.Errorf("expected.json lists %q %d times", f, n)
		}
	}

	onDisk := map[string]bool{}
	for _, f := range payloadFiles(t) {
		onDisk[f] = true
	}

	for _, it := range m.Items {
		if !onDisk[it.File] {
			t.Errorf("expected.json names %q but it is not on disk", it.File)
		}
		p := filepath.Join(corpusDir(), it.File)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("stat %q: %v", it.File, err)
			continue
		}
		if info.Size() == 0 {
			t.Errorf("%q is empty", it.File)
		}
	}

	for f := range onDisk {
		if _, ok := named[f]; !ok {
			t.Errorf("%q is on disk but not named in expected.json", f)
		}
	}

	// The benign body used to pair with summary items must itself be a
	// listed item.
	if m.BenignBody != "" && named[m.BenignBody] == 0 {
		t.Errorf("benign_body %q is not a listed item", m.BenignBody)
	}
}

// TestNumberingIsContiguous: the NN- prefixes run 1..N with no gap and
// no repeat, so nothing was dropped or double-numbered.
func TestNumberingIsContiguous(t *testing.T) {
	t.Parallel()
	m := loadManifest(t)
	re := regexp.MustCompile(`^(\d+)-`)
	seen := map[int]string{}
	maxN := 0
	for _, it := range m.Items {
		sm := re.FindStringSubmatch(it.File)
		if sm == nil {
			t.Errorf("item %q has no numeric prefix", it.File)
			continue
		}
		n, err := strconv.Atoi(sm[1])
		if err != nil {
			t.Errorf("item %q: bad number %q", it.File, sm[1])
			continue
		}
		if prev, dup := seen[n]; dup {
			t.Errorf("number %d used by both %q and %q", n, prev, it.File)
		}
		seen[n] = it.File
		if n > maxN {
			maxN = n
		}
	}
	if len(seen) != len(m.Items) {
		t.Errorf("counted %d distinct numbers for %d items", len(seen), len(m.Items))
	}
	for i := 1; i <= maxN; i++ {
		if _, ok := seen[i]; !ok {
			t.Errorf("gap in numbering: %d is missing", i)
		}
	}
	if maxN != len(m.Items) {
		t.Errorf("highest number %d != item count %d", maxN, len(m.Items))
	}
}

// TestKindAndExpectedValues: every item declares a known kind and a
// known outcome, and a summary item's file name carries the .summary
// marker the loaders key on.
func TestKindAndExpectedValues(t *testing.T) {
	t.Parallel()
	m := loadManifest(t)
	for _, it := range m.Items {
		switch it.Kind {
		case "body", "summary":
		default:
			t.Errorf("%q has unknown kind %q", it.File, it.Kind)
		}
		switch it.Expected {
		case "ask", "ignore":
		default:
			t.Errorf("%q has unknown expected %q", it.File, it.Expected)
		}
		if it.Kind == "summary" && !strings.Contains(it.File, ".summary.") {
			t.Errorf("summary item %q lacks the .summary. marker", it.File)
		}
	}
}
