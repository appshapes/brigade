package adapterclient

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The two adapter binaries the tests drive as real children: the fs
// adapter for happy paths and the scripted fake for every error path.
// They are built once (TestMain) into a directory that outlives every
// test, because each test builds a fresh Client but the binaries are the
// same, and per-test builds would multiply `go build` invocations.
var (
	fsAdapterBin   string
	fakeAdapterBin string
)

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "adapterclient-bins-")
	if err != nil {
		panic(err)
	}
	fsAdapterBin = mustBuild(dir, "brigade-adapter-fs", "github.com/appshapes/brigade/cmd/brigade-adapter-fs")
	fakeAdapterBin = mustBuild(dir, "brigade-fake-adapter", "github.com/appshapes/brigade/cmd/brigade-fake-adapter")
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code) //nolint:forbidigo // TestMain owns the process exit
}

// mustBuild compiles a repository binary the way `make build` does
// (CGO_ENABLED=0, no -race: the child is not race-instrumented, matching
// testutil.Build's contract).
func mustBuild(dir, name, pkg string) string {
	out := filepath.Join(dir, name)
	//nolint:gosec // G204: the package path is a constant; no shell is involved
	cmd := exec.CommandContext(context.Background(), "go", "build", "-trimpath", "-o", out, pkg)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if b, err := cmd.CombinedOutput(); err != nil {
		panic(fmt.Sprintf("building %s: %v\n%s", pkg, err, b))
	}
	return out
}

// writeFakeScript marshals a fake-adapter script to a file the test owns
// and returns its path.
func writeFakeScript(t *testing.T, s fakeadapter.Script) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "script.json")
	data, err := json.Marshal(&s)
	if err != nil {
		t.Fatalf("marshal script: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write script: %v", err)
	}
	return path
}

// newDirs lays out and creates a hermetic directory tree for one Client.
func newDirs(t *testing.T) testutil.Dirs {
	t.Helper()
	d := testutil.NewDirs(t.TempDir())
	if err := d.Mkdir(); err != nil {
		t.Fatalf("mkdir dirs: %v", err)
	}
	return d
}

// fakeClient builds a Client whose adapter is the scripted fake at
// scriptPath, with a minimal PATH-only parent environment. A test that
// needs a hostile parent environment builds its Client directly.
func fakeClient(t *testing.T, scriptPath string) *Client {
	t.Helper()
	d := newDirs(t)
	return &Client{
		Adapter:   config.Adapter{Argv: []string{fakeAdapterBin, "--script", scriptPath}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		Environ:   []string{"PATH=" + os.Getenv("PATH")},
	}
}

// fsClient builds a Client whose adapter is the real fs adapter. Its store
// lives under ${StateDir}/fs-adapter, so BRIGADE_FS_ROOT never has to reach
// the child (matching a live session, where it would be stripped).
func fsClient(t *testing.T) *Client {
	t.Helper()
	d := newDirs(t)
	return &Client{
		Adapter:   config.Adapter{Argv: []string{fsAdapterBin}, Source: config.SourceMap},
		Profile:   "default",
		ConfigDir: d.BrigadeConfig,
		StateDir:  d.BrigadeState,
		Environ:   []string{"PATH=" + os.Getenv("PATH")},
	}
}
