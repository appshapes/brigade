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
	testutil.WriteExecutable(t, target, body)
	hostile := filepath.Join(root, "hostile")
	testutil.WriteExecutable(t, hostile, []byte("#!/bin/sh\nprintf 'wrong-store\\n'\n"))
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

func TestGrokBootstrapKeepsOverridesForOlderHost(t *testing.T) {
	t.Parallel()
	for _, marker := range []string{"CLAUDE_PID=17", "MUSE_SESSION_ID=muse-1", "CODEX_THREAD_ID=codex-1"} {
		t.Run(marker, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := testutil.RepoRoot(t)
			bindir := filepath.Join(root, "bin")
			data := filepath.Join(root, "xdg-data")
			if err := os.MkdirAll(bindir, 0o700); err != nil {
				t.Fatal(err)
			}
			for _, asset := range []string{"brigade", "VERSION", "checksums.txt"} {
				b, err := os.ReadFile(filepath.Join(repo, "plugin/bin", asset))
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(bindir, asset), b, 0o600); err != nil { //nolint:gosec // asset is one of three fixed names
					t.Fatal(err)
				}
			}
			v, err := os.ReadFile(filepath.Join(bindir, "VERSION"))
			if err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(data, "brigade/bin", "brigade-"+strings.TrimSpace(string(v))+"-"+runtime.GOOS+"-"+runtime.GOARCH)
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			testutil.WriteExecutable(t, target, []byte("#!/bin/sh\nprintf '%s\\n' \"${BRIGADE_PROFILE:-unset}\"\n"))
			cmd := exec.CommandContext(t.Context(), "/bin/sh", filepath.Join(bindir, "brigade"), "whoami") //nolint:gosec // argv fixture
			cmd.Env = []string{"HOME=" + filepath.Join(root, "home"), "PATH=/usr/bin:/bin", "GROK_SESSION_ID=02000000-0000-7000-8000-000000000001", "XDG_DATA_HOME=" + data, "BRIGADE_PROFILE=kept", marker}
			out, err := cmd.CombinedOutput()
			if err != nil || string(out) != "kept\n" {
				t.Fatalf("bootstrap %q %v", out, err)
			}
		})
	}
}

func TestBootstrapStopColdCacheDoesNotDownload(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := testutil.RepoRoot(t)
	bindir := filepath.Join(root, "bin")
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(bindir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, asset := range []string{"brigade", "VERSION", "checksums.txt"} {
		b, err := os.ReadFile(filepath.Join(repo, "plugin/bin", asset))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bindir, asset), b, 0o600); err != nil { //nolint:gosec // asset is one of three fixed names
			t.Fatal(err)
		}
	}
	run := func(args ...string) ([]byte, error) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "/bin/sh", append([]string{filepath.Join(bindir, "brigade")}, args...)...) //nolint:gosec // argv fixture
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
		return cmd.CombinedOutput()
	}
	out, err := run("hook", "stop")
	if err != nil || strings.Contains(string(out), "downloading") {
		t.Fatalf("cold stop %q %v", out, err)
	}
	v, err := os.ReadFile(filepath.Join(bindir, "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	failed := filepath.Join(home, ".local/share/brigade/bin", "brigade-"+strings.TrimSpace(string(v))+"-"+runtime.GOOS+"-"+runtime.GOARCH+".failed")
	if err := os.MkdirAll(filepath.Dir(failed), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(failed, []byte("9 download failed: synthetic\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), "/bin/sh", filepath.Join(bindir, "brigade"), "hook", "stop") //nolint:gosec // argv fixture
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err = cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "not installed: download failed: synthetic") {
		t.Fatalf("stop marker %q %v", out, err)
	}
	if _, statErr := os.Stat(failed); !os.IsNotExist(statErr) {
		t.Fatal("install-failure marker was not consumed")
	}
}
