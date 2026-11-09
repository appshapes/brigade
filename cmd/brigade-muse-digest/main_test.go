package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/musedigest"
)

// fixture lays out a repository root with the smallest Muse package, its VERSION and a catalog carrying digest.
func fixture(t *testing.T, digest string) string {
	t.Helper()
	root := t.TempDir()
	for rel, body := range map[string]string{
		"muse/brigade/.muse-plugin/plugin.json": "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n",
		"muse/brigade/bin/VERSION":              "1.2.3\n",
		"marketplace.json": "{\n  \"plugins\": [\n    {\n      \"name\": \"brigade\",\n      \"version\": \"0.0.0\",\n" +
			"      \"integrity\": {\n        \"digest\": \"" + digest + "\"\n      }\n    }\n  ]\n}\n",
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func runIn(root string, args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, root, &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestRun(t *testing.T) {
	t.Parallel()
	const zeros = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

	t.Run("print writes the digest and nothing else", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, zeros)
		want, err := musedigest.SumDir(filepath.Join(root, "muse", "brigade"))
		if err != nil {
			t.Fatal(err)
		}
		code, stdout, stderr := runIn(root, "print")
		if code != 0 || stdout != want+"\n" || stderr != "" {
			t.Fatalf("print = %d, %q, %q; want 0 and %q alone", code, stdout, stderr, want)
		}
		// With a directory, any Muse package: the root need not be a repository.
		code, stdout, stderr = runIn(t.TempDir(), "print", filepath.Join(root, "muse", "brigade"))
		if code != 0 || stdout != want+"\n" || stderr != "" {
			t.Fatalf("print <dir> = %d, %q, %q; want 0 and %q alone", code, stdout, stderr, want)
		}
	})

	t.Run("check exits 1 on a stale catalog, write repairs it, check then exits 0", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, zeros)
		code, stdout, stderr := runIn(root, "check")
		if code != 1 || stdout != "" || !strings.Contains(stderr, "make muse-digest") {
			t.Fatalf("check on a stale catalog = %d, %q, %q", code, stdout, stderr)
		}
		code, stdout, stderr = runIn(root, "write")
		if code != 0 || !strings.HasPrefix(stdout, "muse-digest: wrote marketplace.json: version 1.2.3, digest sha256:") || stderr != "" {
			t.Fatalf("write = %d, %q, %q", code, stdout, stderr)
		}
		code, stdout, stderr = runIn(root, "check")
		if code != 0 || !strings.HasPrefix(stdout, "muse-digest: ok: ") || stderr != "" {
			t.Fatalf("check after write = %d, %q, %q", code, stdout, stderr)
		}
	})

	t.Run("a directory that is not the repository root fails", func(t *testing.T) {
		t.Parallel()
		for _, mode := range []string{"print", "write", "check"} {
			code, stdout, stderr := runIn(t.TempDir(), mode)
			if code != 1 || stdout != "" || !strings.Contains(stderr, "run this from the repository root") {
				t.Errorf("%s = %d, %q, %q", mode, code, stdout, stderr)
			}
		}
	})

	t.Run("anything else is a usage error", func(t *testing.T) {
		t.Parallel()
		root := fixture(t, zeros)
		for _, args := range [][]string{nil, {"frobnicate"}, {"write", "extra"}, {"check", "extra"}, {"print", "a", "b"}} {
			code, stdout, stderr := runIn(root, args...)
			if code != 2 || stdout != "" || stderr != usage {
				t.Errorf("%v = %d, %q, %q; want 2 and the usage line", args, code, stdout, stderr)
			}
		}
	})
}
