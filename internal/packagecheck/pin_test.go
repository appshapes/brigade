package packagecheck_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/packagecheck"
)

// The two marketplace files in the pinned form (work item 424), each
// naming the commit it is given, as the repository writes them: the
// commit on a line of its own.
func codexMarket(commit string) string {
	return `{
  "name": "brigade",
  "plugins": [
    {
      "name": "brigade",
      "source": {
        "source": "git-subdir",
        "url": "https://github.com/appshapes/brigade.git",
        "path": "codex/brigade",
        "sha": "` + commit + `"
      },
      "category": "Productivity"
    }
  ]
}
`
}

func grokMarket(commit string) string {
	return `{
  "name": "brigade",
  "plugins": [
    {
      "name": "brigade",
      "source": {
        "source": "url",
        "url": "https://github.com/appshapes/brigade.git",
        "sha": "` + commit + `",
        "path": "grok/brigade"
      }
    }
  ]
}
`
}

var (
	commitA = strings.Repeat("a", 40)
	commitB = strings.Repeat("b", 40)
)

// pinRoot is a directory that is no git repository and holds the two files.
func pinRoot(t *testing.T, codex, grok string) string {
	t.Helper()
	root := t.TempDir()
	for rel, text := range map[string]string{packagecheck.CodexMarketplace: codex, packagecheck.GrokMarketplace: grok} {
		if text == "" {
			continue
		}
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func readPinFile(t *testing.T, root, rel string) string {
	t.Helper()
	//nolint:gosec // G304: under t.TempDir()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// pinOptions run the reading with no tool to find, so that it reads the
// two files alone, whatever directory the test's own lies in.
func pinOptions(root string) packagecheck.Options {
	return packagecheck.Options{Root: root, Environ: []string{"PATH="}}
}

func TestWritePinChangesTheCommitAndNothingElse(t *testing.T) {
	t.Parallel()
	root := pinRoot(t, codexMarket(commitA), grokMarket(commitA))
	if err := packagecheck.WritePin(root, commitB); err != nil {
		t.Fatal(err)
	}
	if got := readPinFile(t, root, packagecheck.CodexMarketplace); got != codexMarket(commitB) {
		t.Errorf("Codex CLI's file after the write:\n%s", got)
	}
	if got := readPinFile(t, root, packagecheck.GrokMarketplace); got != grokMarket(commitB) {
		t.Errorf("Grok Build's file after the write:\n%s", got)
	}
	pin, err := packagecheck.ReadPin(pinOptions(root))
	if err != nil || pin.Commit != commitB || pin.Release != "" || pin.Ahead != "" {
		t.Errorf("ReadPin = %+v, %v; want the commit %s and no release read", pin, err, commitB)
	}
	if line := pin.String(); !strings.Contains(line, "the Codex CLI and Grok Build packages are pinned to the commit "+commitB) {
		t.Errorf("the pin's line: %q", line)
	}
}

func TestWritePinRefusesAndLeavesBothFiles(t *testing.T) {
	t.Parallel()
	oneLine := strings.Replace(grokMarket(commitA), "\",\n        \"sha\"", "\", \"sha\"", 1)
	for _, c := range []struct {
		name, codex, grok, commit, want string
	}{
		{"a short commit", codexMarket(commitA), grokMarket(commitA), "b9908ccf", "not a full commit sha"},
		{"a commit in capitals", codexMarket(commitA), grokMarket(commitA), strings.Repeat("B", 40), "not a full commit sha"},
		{"a tag's name", codexMarket(commitA), grokMarket(commitA), "v0.59.0", "not a full commit sha"},
		{"a file that is not in the pinned form", strings.Replace(codexMarket(commitA), `"git-subdir"`, `"local"`, 1), grokMarket(commitA), commitB, "does not name the package at a release's commit"},
		{"a commit that shares its line", codexMarket(commitA), oneLine, commitB, `0 lines that are a "sha" alone`},
		{"a missing file", codexMarket(commitA), "", commitB, "missing .grok-plugin/marketplace.json"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			root := pinRoot(t, c.codex, c.grok)
			err := packagecheck.WritePin(root, c.commit)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("WritePin = %v, want an error naming %q", err, c.want)
			}
			if got := readPinFile(t, root, packagecheck.CodexMarketplace); got != c.codex {
				t.Errorf("the refusal changed Codex CLI's file:\n%s", got)
			}
		})
	}
}

func TestReadPinHoldsTheFormOfBothFiles(t *testing.T) {
	t.Parallel()
	form := "does not name the package at a release's commit"
	for _, c := range []struct {
		name, codex, grok, want string
	}{
		{"the directory of the default branch, as it was", strings.Replace(strings.Replace(codexMarket(commitA), `"git-subdir"`, `"local"`, 1), `"codex/brigade"`, `"./codex/brigade"`, 1), grokMarket(commitA), form},
		{"a branch beside the commit", codexMarket(commitA), strings.Replace(grokMarket(commitA), `"source": "url",`, `"source": "url", "ref": "master",`, 1), form},
		{"another repository", strings.Replace(codexMarket(commitA), "appshapes/brigade.git", "example/brigade.git", 1), grokMarket(commitA), form},
		{"another package's directory", codexMarket(commitA), strings.Replace(grokMarket(commitA), `"grok/brigade"`, `"codex/brigade"`, 1), form},
		{"a short commit", strings.Replace(codexMarket(commitA), commitA, "aaaaaaa", 1), grokMarket(commitA), form},
		{"no plugin", `{"name": "brigade", "plugins": []}`, grokMarket(commitA), "does not name the one plugin brigade"},
		{"not JSON", codexMarket(commitA) + "}", grokMarket(commitA), "is not JSON"},
		{"two commits", codexMarket(commitA), grokMarket(commitB), "one release is one commit"},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := packagecheck.ReadPin(pinOptions(pinRoot(t, c.codex, c.grok)))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("ReadPin = %v, want an error naming %q", err, c.want)
			}
		})
	}
}
