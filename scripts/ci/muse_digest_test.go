//go:build !windows

// The Muse marketplace catalog's digest (cards 115 and 114). Muse installs the package from a marketplace only when
// marketplace.json's `integrity.digest` equals the package digest it computes over muse/brigade/, and
// internal/musedigest computes that value without Muse. This file holds what follows from it for THIS repository:
//
//   - the committed catalog carries the digest of the committed package: the gate, on every machine, which is how
//     `make test`, `make push`, CI and the release sequence refuse a stale catalog;
//   - the places that write and verify the digest outside `make test` still do (the Makefile, the release script,
//     the release workflow), and none of them asks for Muse;
//   - where Muse Code is installed, Muse itself installs the package from the catalog: the cross-check that the
//     reproduction still is what Muse computes, skipped with a line where there is no `muse`.
package ci_test

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/musedigest"
	"github.com/appshapes/brigade/internal/testutil"
)

var (
	digestZero = "sha256:" + strings.Repeat("0", 64)
	digestA    = "sha256:" + strings.Repeat("a", 64)
)

// catalogJSON is marketplace.json as the repository writes it: one entry, each key on its own line, because
// muse-plugin-check.sh and release.yml read "version" with an anchored sed and brigade-muse-digest rewrites the
// "version" and "digest" lines in place.
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

// TestMuseCatalogCarriesThePackageDigest is the gate: marketplace.json is what `make muse-digest` writes for this
// checkout, the digest of muse/brigade/ and the version its bin/VERSION pins. After a change under muse/brigade/,
// `make muse-digest` rewrites it.
func TestMuseCatalogCarriesThePackageDigest(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	digest, err := musedigest.Check(root)
	if err != nil {
		t.Fatal(err)
	}
	version, _, err := musedigest.Recorded(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s: version %s, digest %s", musedigest.CatalogFile, version, digest)
}

// TestMuseDigestIsWiredWithoutMuse pins the three places outside `make test` that write or verify the catalog's
// digest, and that the release sequence no longer asks for Muse Code on the machine (card 114).
func TestMuseDigestIsWiredWithoutMuse(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	read := func(rel string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, want := range []struct{ file, line, why string }{
		{"Makefile", "muse-plugin-check:", "the target exists"},
		{"Makefile", "\tsh scripts/ci/muse-plugin-check.sh\n\tgo run ./cmd/brigade-muse-digest check\n", "make muse-plugin-check verifies the digest's value after the catalog's shape (CI's fast job runs it)"},
		{"Makefile", "\tgo run ./cmd/brigade-muse-digest write\n", "make muse-digest rewrites the catalog"},
		{"scripts/release-prep.sh", "  go run ./cmd/brigade-muse-digest check >/dev/null || die ", "a release checks the catalog before it mutates anything"},
		{"scripts/release-prep.sh", "[ ! -f \"$muse_json\" ] || go run ./cmd/brigade-muse-digest write\n", "step 4 of a release rewrites the catalog once the package's bin/ is final"},
		{".github/workflows/release.yml", "          go run ./cmd/brigade-muse-digest check ", "the release job refuses a tag whose catalog is stale"},
	} {
		if !strings.Contains(read(want.file), want.line) {
			t.Errorf("%s does not contain %q: %s", want.file, want.line, want.why)
		}
	}
	for _, file := range []string{"Makefile", "scripts/release-prep.sh", "scripts/ci/muse-plugin-check.sh", ".github/workflows/release.yml"} {
		for _, gone := range []string{"command -v muse", "MUSE_DIGEST_REQUIRE", "muse-digest.sh"} {
			if strings.Contains(read(file), gone) {
				t.Errorf("%s still contains %q: the digest is computed without Muse since card 114", file, gone)
			}
		}
	}
}

// TestMuseInstallsThePackageFromTheCatalog is the cross-check against Muse itself, where Muse Code is installed:
// a copy of marketplace.json and muse/brigade/ is added as a local marketplace in a throwaway root (HOME and the
// four XDG directories, so nothing under the real Muse directories is read or written) and Muse installs brigade
// from it. Muse refuses a catalog whose digest is not the one it computes ("failed integrity check"), so a passing
// install is Muse agreeing with internal/musedigest, and a Muse release that changed the construction fails here
// before it fails a member's install.
func TestMuseInstallsThePackageFromTheCatalog(t *testing.T) {
	t.Parallel()
	muse, err := exec.LookPath("muse")
	if err != nil {
		t.Skip("muse is not on PATH: the catalog's digest is verified by internal/musedigest alone here; a machine with Muse Code also has Muse install from it")
	}
	root := testutil.RepoRoot(t)
	_, recorded, err := musedigest.Recorded(root)
	if err != nil {
		t.Fatal(err)
	}

	// A marketplace of its own, so Muse snapshots the catalog and the package and nothing else of the checkout.
	// os.CopyFS keeps the execute bits of the bootstrap and the hook wrappers.
	market := filepath.Join(t.TempDir(), "market")
	if err := os.CopyFS(filepath.Join(market, filepath.FromSlash(musedigest.PackageDir)), os.DirFS(filepath.Join(root, filepath.FromSlash(musedigest.PackageDir)))); err != nil {
		t.Fatal(err)
	}
	catalog, err := os.ReadFile(filepath.Join(root, musedigest.CatalogFile))
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G703: the catalog's copy under this test's own temp directory
	if err := os.WriteFile(filepath.Join(market, musedigest.CatalogFile), catalog, 0o600); err != nil {
		t.Fatal(err)
	}

	// The session environment is stripped by prefix, as every run of a harness outside the current session is:
	// MUSE*, CLAUDE* (keeping CLAUDE_CONFIG_DIR) and AI_AGENT.
	home := t.TempDir()
	env := []string{
		"HOME=" + home, "XDG_DATA_HOME=" + filepath.Join(home, "data"), "XDG_CONFIG_HOME=" + filepath.Join(home, "config"),
		"XDG_STATE_HOME=" + filepath.Join(home, "state"), "XDG_CACHE_HOME=" + filepath.Join(home, "cache"),
	}
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case name == "HOME" || strings.HasPrefix(name, "XDG_"):
		case name == "AI_AGENT" || strings.HasPrefix(name, "MUSE"):
		case strings.HasPrefix(name, "CLAUDE") && name != "CLAUDE_CONFIG_DIR":
		default:
			env = append(env, kv)
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	run := func(args ...string) []byte {
		t.Helper()
		cmd := exec.CommandContext(ctx, muse, args...) //nolint:gosec // G204: the muse on PATH, fixed arguments and a path under this test's own temp directory
		cmd.Env = env
		cmd.Dir = home
		// The report is stdout's alone: a notice Muse prints on stderr (an update, a first run in this fresh
		// HOME) must not break the parse, and is shown only when the command fails.
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("muse %s: %v\nstdout: %s\nstderr: %s", strings.Join(args, " "), err, out, stderr.String())
		}
		return out
	}
	run("plugins", "marketplace", "add", "brigade-catalog-check", market, "--json")
	out := run("plugins", "install", musedigest.PluginName+"@brigade-catalog-check", "--json")

	var report struct {
		Installed struct {
			Version       string `json:"version"`
			PackageSHA256 string `json:"package_sha256"`
		} `json:"installed"`
	}
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("muse plugins install printed no report: %v\n%s", err, out)
	}
	if report.Installed.PackageSHA256 != recorded {
		t.Fatalf("Muse installed the package with package_sha256 %q, and %s carries %q", report.Installed.PackageSHA256, musedigest.CatalogFile, recorded)
	}
	t.Logf("Muse installed %s %s from the catalog: package_sha256 %s", musedigest.PluginName, report.Installed.Version, report.Installed.PackageSHA256)
}
