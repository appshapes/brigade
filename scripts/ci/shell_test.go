package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// TestNoCommentInsideAContinuedCommand fails when a shell script has a
// comment line directly after a line that ends in a backslash. The shell
// removes the backslash-newline before it tokenises, so the comment is
// joined onto the command and ends it there: everything after the comment
// runs as a separate command. Commit 79467ce put four comment lines inside
// the `exec env … \` continuation that launches every nested `claude`
// session, and the three proof scripts silently launched `env` instead —
// `sh -n`, shellcheck 0.10 and 0.11 and every drift test stayed green
// (shellcheck's SC1143 covers only a backslash at the END of a comment).
// The first run of scripts/proof-idle-wake.sh after that commit was RED
// with 0/0 wakes (bundle 20260905T032923Z), which is how it was found.
func TestNoCommentInsideAContinuedCommand(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	var files []string
	for _, pattern := range []string{"scripts/*.sh", "scripts/ci/*.sh", "plugin/bin/brigade"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, matches...)
	}
	if len(files) < 5 {
		t.Fatalf("found only %d shell files under scripts/ and plugin/bin: the glob is wrong", len(files))
	}
	var bad []string
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(string(data), "\n")
		for i := 1; i < len(lines); i++ {
			prev := strings.TrimRight(lines[i-1], " \t")
			// A backslash that ends a COMMENT line continues nothing (shellcheck's
			// SC1143 says so); only a backslash that ends a command line does.
			if strings.HasPrefix(strings.TrimSpace(prev), "#") {
				continue
			}
			if strings.HasSuffix(prev, "\\") && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
				rel, _ := filepath.Rel(root, f)
				bad = append(bad, rel+":"+itoa(i+1))
			}
		}
	}
	if len(bad) > 0 {
		t.Fatalf("a comment line inside a backslash-continued command ends the command there — move it above the command:\n  %s", strings.Join(bad, "\n  "))
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
