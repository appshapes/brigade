// The test of scripts/muse-digest.sh (card 115): the digest Muse computes over the Muse Code package, obtained by
// having Muse install the package into a throwaway root, then printed, written into marketplace.json or compared
// with it. A `muse` STUB on PATH stands in for Muse here: it records the environment and the arguments it is
// handed and prints the one JSON line the script reads, so the contract (the isolated XDG root, the stripped
// session environment, the argv) is asserted offline. The digest's real value is checked by
// muse_plugin_check_test.go's "the real repository passes" case on a machine with Muse installed.
package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

var (
	digestZero = "sha256:" + strings.Repeat("0", 64)
	digestA    = "sha256:" + strings.Repeat("a", 64)
	digestB    = "sha256:" + strings.Repeat("b", 64)
)

// catalogJSON is marketplace.json as the repository writes it: one entry, each key on its own line, because
// muse-digest.sh and muse-plugin-check.sh read "version" and "digest" with anchored seds.
func catalogJSON(version, digest string) string {
	return `{
  "schemaVersion": 1,
  "source": "local",
  "plugins": [
    {
      "name": "brigade",
      "version": "` + version + `",
      "install": {
        "transport": "local-path",
        "source": "muse/brigade"
      },
      "integrity": {
        "digest": "` + digest + `"
      },
      "availability": {
        "status": "available"
      }
    }
  ]
}
`
}

// stubMuse writes <dir>/muse: a Muse that records its argv and environment in $STUB_LOG (when set) and prints
// `package_sha256` = digest, or no digest line at all when digest is empty.
func stubMuse(t *testing.T, dir, digest string) {
	t.Helper()
	body := "#!/bin/sh\n" +
		"if [ -n \"${STUB_LOG:-}\" ]; then { printf 'argv: %s\\n' \"$*\"; env | LC_ALL=C sort; } > \"$STUB_LOG\"; fi\n"
	if digest != "" {
		body += "printf '{\\n  \"installed\": {\\n    \"package_sha256\": \"" + digest + "\"\\n  }\\n}\\n'\n"
	} else {
		body += "printf '{\\n  \"installed\": {}\\n}\\n'\n"
	}
	body += "exit 0\n"
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	testutil.WriteExecutable(t, filepath.Join(dir, "muse"), []byte(body))
}

// fixtureVersion is the package pin every digest fixture carries; the catalog starts at 0.0.0 so that write
// has something to change.
const fixtureVersion = "1.2.3"

// digestFixture lays out what the script reads under a fresh root: the package marker, its VERSION and the
// catalog, and returns the root and a stub directory to put on PATH.
func digestFixture(t *testing.T, catalogDigest string) (root, stubDir string) {
	t.Helper()
	root = t.TempDir()
	version := fixtureVersion
	for rel, body := range map[string]string{
		"muse/brigade/.muse-plugin/plugin.json": "{\n  \"name\": \"brigade\",\n  \"version\": \"" + version + "\"\n}\n",
		"muse/brigade/bin/VERSION":              version + "\n",
		"marketplace.json":                      catalogJSON("0.0.0", catalogDigest),
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, filepath.Join(root, ".stub-bin")
}

// runMuseDigest runs scripts/muse-digest.sh -- the real one -- in root with PATH led by stubDir.
func runMuseDigest(t *testing.T, root, stubDir string, extraEnv []string, mode string) result {
	t.Helper()
	env := baseEnv(t, filepath.Join(root, ".home"), extraEnv...)
	for i, kv := range env {
		if strings.HasPrefix(kv, "PATH=") {
			env[i] = "PATH=" + stubDir + string(os.PathListSeparator) + os.Getenv("PATH")
		}
	}
	return runDigestScript(t, root, env, mode)
}

// runDigestScript runs scripts/muse-digest.sh, which lives under scripts/ rather than scripts/ci/, from workdir.
func runDigestScript(t *testing.T, workdir string, env []string, args ...string) result {
	t.Helper()
	return runScriptAt(t, filepath.Join(testutil.RepoRoot(t), "scripts", "muse-digest.sh"), workdir, env, args...)
}

func TestMuseDigest(t *testing.T) {
	t.Parallel()

	t.Run("print hands Muse an isolated root and a stripped environment", func(t *testing.T) {
		t.Parallel()
		root, stub := digestFixture(t, digestZero)
		stubMuse(t, stub, digestA)
		log := filepath.Join(root, "muse.log")
		r := runMuseDigest(t, root, stub, []string{
			"STUB_LOG=" + log, "MUSE_SESSION_ID=abc", "MUSE_EXPERIMENTAL_X=on", "CLAUDECODE=1", "CLAUDE_PID=1",
			"AI_AGENT=1", "CLAUDE_CONFIG_DIR=/keep/me",
		}, "print")
		wantPass(t, r, digestA)
		if strings.TrimSpace(r.stdout) != digestA {
			t.Fatalf("print wrote more than the digest: %q", r.stdout)
		}
		recorded, err := os.ReadFile(log) //nolint:gosec // G304: a path under this test's own temp directory
		if err != nil {
			t.Fatalf("the stub recorded nothing: %v", err)
		}
		got := string(recorded)
		realRoot, err := filepath.EvalSymlinks(root)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			"argv: plugins install " + realRoot + "/muse/brigade --scope user --json\n",
			"XDG_DATA_HOME=", "XDG_CONFIG_HOME=", "XDG_STATE_HOME=", "XDG_CACHE_HOME=",
			"CLAUDE_CONFIG_DIR=/keep/me\n",
		} {
			if !strings.Contains(got, want) {
				t.Errorf("the stub was not handed %q:\n%s", want, got)
			}
		}
		for _, line := range strings.Split(got, "\n") {
			if strings.HasPrefix(line, "XDG_") && (strings.Contains(line, root) || strings.Contains(line, "/.home")) {
				t.Errorf("an XDG directory points at the fixture or its HOME, not a throwaway root: %s", line)
			}
			for _, leaked := range []string{"MUSE_SESSION_ID=", "MUSE_EXPERIMENTAL_X=", "CLAUDECODE=", "CLAUDE_PID=", "AI_AGENT="} {
				if strings.HasPrefix(line, leaked) {
					t.Errorf("the session environment leaked into Muse: %s", line)
				}
			}
		}
	})

	t.Run("write rewrites the catalog's digest and version and nothing else", func(t *testing.T) {
		t.Parallel()
		root, stub := digestFixture(t, digestZero)
		stubMuse(t, stub, digestA)
		wantPass(t, runMuseDigest(t, root, stub, nil, "write"), "wrote marketplace.json: version "+fixtureVersion+", digest "+digestA)
		got, err := os.ReadFile(filepath.Join(root, "marketplace.json"))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != catalogJSON(fixtureVersion, digestA) {
			t.Fatalf("the catalog after write:\n%s", got)
		}
		if _, err := os.Stat(filepath.Join(root, "marketplace.json.bak")); err == nil {
			t.Fatal("write left marketplace.json.bak behind")
		}
	})

	t.Run("check passes on the digest Muse computes and fails on any other", func(t *testing.T) {
		t.Parallel()
		root, stub := digestFixture(t, digestA)
		stubMuse(t, stub, digestA)
		wantPass(t, runMuseDigest(t, root, stub, nil, "check"), "ok: marketplace.json's digest equals")
		stubMuse(t, stub, digestB)
		wantFail(t, runMuseDigest(t, root, stub, nil, "check"), "differs from what Muse computes", "make muse-digest")
	})

	t.Run("without muse on PATH, check skips, and requires or writes refuse", func(t *testing.T) {
		t.Parallel()
		root, _ := digestFixture(t, digestA)
		empty := filepath.Join(root, ".empty-bin")
		if err := os.MkdirAll(empty, 0o700); err != nil {
			t.Fatal(err)
		}
		env := baseEnv(t, filepath.Join(root, ".home"))
		for i, kv := range env {
			if strings.HasPrefix(kv, "PATH=") {
				env[i] = "PATH=" + empty
			}
		}
		wantPass(t, runDigestScript(t, root, env, "check"), "skipped: muse is not on PATH")
		wantFail(t, runDigestScript(t, root, append(env, "MUSE_DIGEST_REQUIRE=1"), "check"), "muse is not on PATH", "owner ruling 2026-10-17")
		wantFail(t, runDigestScript(t, root, env, "write"), "muse is not on PATH")
		wantFail(t, runDigestScript(t, root, env, "print"), "muse is not on PATH")
	})

	t.Run("a Muse that reports no digest is refused", func(t *testing.T) {
		t.Parallel()
		root, stub := digestFixture(t, digestA)
		stubMuse(t, stub, "")
		wantFail(t, runMuseDigest(t, root, stub, nil, "print"), "printed no package_sha256")
	})

	t.Run("a bad mode and a wrong directory are refused", func(t *testing.T) {
		t.Parallel()
		root, stub := digestFixture(t, digestA)
		stubMuse(t, stub, digestA)
		wantFail(t, runMuseDigest(t, root, stub, nil, "frobnicate"), "usage: scripts/muse-digest.sh")
		wantFail(t, runMuseDigest(t, filepath.Join(root, "muse"), stub, nil, "print"), "run this from the repository root")
	})
}
