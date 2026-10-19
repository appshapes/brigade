package ci_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

func TestGrokBootstrapUsesHomeAndStripsOverrides(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := testutil.RepoRoot(t)
	bindir := filepath.Join(root, "plugin with 'quote'", "bin")
	home := filepath.Join(root, "owned home")
	evil := filepath.Join(root, "unrelated xdg")
	for _, dir := range []string{bindir, filepath.Join(home, ".local/share/brigade/bin"), filepath.Join(evil, "brigade")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, asset := range []string{"brigade", "VERSION", "checksums.txt"} {
		b, err := os.ReadFile(filepath.Join(repo, "plugin/bin", asset))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bindir, asset), b, 0600); err != nil { //nolint:gosec // asset is one of three fixed names in the owned fixture
			t.Fatal(err)
		}
	}
	v, err := os.ReadFile(filepath.Join(bindir, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".local/share/brigade/bin", "brigade-"+strings.TrimSpace(string(v))+"-"+runtime.GOOS+"-"+runtime.GOARCH)
	body := []byte("#!/bin/sh\n[ -z \"${XDG_CONFIG_HOME:-}${XDG_DATA_HOME:-}${BRIGADE_PROFILE:-}${BRIGADE_RELEASE_BASE_URL:-}\" ] || exit 4\nprintf 'home-only\\n'\n")
	if err := os.WriteFile(target, body, 0700); err != nil { //nolint:gosec // executable under the owned temporary cache only
		t.Fatal(err)
	}
	hostile := filepath.Join(root, "hostile")
	if err := os.WriteFile(hostile, []byte("#!/bin/sh\nprintf 'wrong-store\\n'\n"), 0700); err != nil { //nolint:gosec // poisoned executable fixture under t.TempDir only
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(evil, "brigade/dev-binary"), []byte(hostile+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", filepath.Join(bindir, "brigade"), "inbox", "follow") //nolint:gosec // argv fixture, no shell command string
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "GROK_SESSION_ID=02000000-0000-7000-8000-000000000001", "XDG_CONFIG_HOME=" + evil, "XDG_DATA_HOME=" + evil, "BRIGADE_PROFILE=wrong", "BRIGADE_RELEASE_BASE_URL=http://invalid"}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "home-only\n" {
		t.Fatalf("bootstrap %q %v", out, err)
	}
}
