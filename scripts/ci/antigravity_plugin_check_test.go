//go:build !windows

package ci_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/packagecheck"
	"github.com/appshapes/brigade/internal/testutil"
)

// TestAntigravityPackageSource: the committed package passes its own check
// (work item 532).
func TestAntigravityPackageSource(t *testing.T) {
	t.Parallel()
	onceInCI(t, "make antigravity-plugin-check")
	if err := packagecheck.AntigravitySource(testutil.RepoRoot(t)); err != nil {
		t.Fatal(err)
	}
}

// TestAntigravityPackageSourceRejectsDrift: each kind of drift fails the
// check.
func TestAntigravityPackageSourceRejectsDrift(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"skill missing", "skill unreachable", "extra file", "symlink", "wrong hook host", "hook outside the package", "fourth hook", "long timeout", "mcp manifest", "bootstrap drift"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := testutil.RepoRoot(t)
			for _, dir := range []string{"antigravity/brigade", "plugin/bin"} {
				if err := os.CopyFS(filepath.Join(root, dir), os.DirFS(filepath.Join(repo, dir))); err != nil {
					t.Fatal(err)
				}
			}
			replace := func(rel, from, to string) {
				t.Helper()
				path := filepath.Join(root, rel)
				b, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				if !bytes.Contains(b, []byte(from)) {
					t.Fatalf("mutation anchor missing: %s", from)
				}
				if err := os.WriteFile(path, bytes.Replace(b, []byte(from), []byte(to), 1), 0o600); err != nil { //nolint:gosec // allowlisted package copy under t.TempDir only
					t.Fatal(err)
				}
			}
			const hooks = "antigravity/brigade/hooks.json"
			switch mutation {
			case "skill missing":
				if err := os.Remove(filepath.Join(root, "antigravity/brigade/skills/join/SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "skill unreachable":
				replace("antigravity/brigade/skills/join/SKILL.md", "name: join", "name: join\ndisable-model-invocation: true")
			case "extra file":
				if err := os.WriteFile(filepath.Join(root, "antigravity/brigade/unexpected"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("README.md", filepath.Join(root, "antigravity/brigade/link")); err != nil {
					t.Fatal(err)
				}
			case "wrong hook host":
				replace(hooks, "--host antigravity", "--host codex")
			case "hook outside the package":
				replace(hooks, `"./bin/brigade hook prompt`, `"brigade hook prompt`)
			case "fourth hook":
				replace(hooks, `"Stop": [`, `"PostInvocation": [{"type": "command", "command": "./bin/brigade hook prompt --host antigravity --plugin-root .", "timeout": 5}],
    "Stop": [`)
			case "long timeout":
				replace(hooks, `"timeout": 10`, `"timeout": 30`)
			case "mcp manifest":
				replace("antigravity/brigade/plugin.json", `"name": "brigade",`, `"name": "brigade", "mcpServers": {},`)
			case "bootstrap drift":
				replace("antigravity/brigade/bin/brigade", "set -u", "set -u\n:")
			}
			if err := packagecheck.AntigravitySource(root); err == nil {
				t.Fatal("accepted package drift")
			}
		})
	}
}

// agyBootstrap copies plugin/bin's three assets into a fresh package bin/
// and returns the bootstrap's path, with a home whose cache dir exists.
func agyBootstrap(t *testing.T) (bootstrap, home string) {
	t.Helper()
	root := t.TempDir()
	repo := testutil.RepoRoot(t)
	bindir := filepath.Join(root, "plugins", "brigade", "bin")
	home = filepath.Join(root, "home")
	for _, dir := range []string{bindir, filepath.Join(home, ".local/share/brigade/bin")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, asset := range []string{"brigade", "VERSION", "checksums.txt"} {
		b, err := os.ReadFile(filepath.Join(repo, "plugin/bin", asset))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bindir, asset), b, 0o600); err != nil { //nolint:gosec // asset is one of three fixed names in the owned fixture
			t.Fatal(err)
		}
	}
	return filepath.Join(bindir, "brigade"), home
}

// TestAntigravityBootstrapUsesHomeAndStripsOverrides: in an Antigravity
// session the bootstrap resolves the cache from HOME alone, as the Go
// resolvers do for a home-only host, and ignores inherited BRIGADE_* and
// XDG_*.
func TestAntigravityBootstrapUsesHomeAndStripsOverrides(t *testing.T) {
	t.Parallel()
	bootstrap, home := agyBootstrap(t)
	v, err := os.ReadFile(filepath.Join(filepath.Dir(bootstrap), "VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(home, ".local/share/brigade/bin", "brigade-"+strings.TrimSpace(string(v))+"-"+runtime.GOOS+"-"+runtime.GOARCH)
	testutil.WriteExecutable(t, target, []byte("#!/bin/sh\n[ -z \"${XDG_CONFIG_HOME:-}${XDG_DATA_HOME:-}${BRIGADE_PROFILE:-}\" ] || exit 4\nprintf 'home-only\\n'\n"))
	evil := t.TempDir()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", bootstrap, "whoami") //nolint:gosec // argv fixture, no shell command string
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "ANTIGRAVITY_CONVERSATION_ID=b95ca511-d709-4698-99c5-e85de16b4ab8", "XDG_CONFIG_HOME=" + evil, "XDG_DATA_HOME=" + evil, "BRIGADE_PROFILE=wrong"}
	out, err := cmd.CombinedOutput()
	if err != nil || string(out) != "home-only\n" {
		t.Fatalf("bootstrap %q %v", out, err)
	}
}

// TestAntigravityBootstrapAnswersJSONOnAColdCache: before the binary is
// cached, the prompt and stop hooks of an Antigravity session print the
// empty JSON answer and exit 0, and start no download; agy reads a hook's
// stdout as JSON and nothing else.
func TestAntigravityBootstrapAnswersJSONOnAColdCache(t *testing.T) {
	t.Parallel()
	for _, sub := range []string{"prompt", "stop"} {
		bootstrap, home := agyBootstrap(t)
		cmd := exec.CommandContext(t.Context(), "/bin/sh", bootstrap, "hook", sub, "--host", "antigravity", "--plugin-root", ".") //nolint:gosec // argv fixture, no shell command string
		cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "ANTIGRAVITY_CONVERSATION_ID=b95ca511-d709-4698-99c5-e85de16b4ab8"}
		var stdout bytes.Buffer
		cmd.Stdout = &stdout
		if err := cmd.Run(); err != nil || stdout.String() != "{}\n" {
			t.Fatalf("cold %s: stdout %q, %v", sub, stdout.String(), err)
		}
	}
}

// TestAntigravityBootstrapAnswersJSONByItsArgv: a hook whose argv names
// --host antigravity answers in JSON even when its environment carries
// another host's marker and not agy's, and a failure before the binary runs
// (a dev pointer that names nothing) is the empty JSON answer and exit 0,
// with the reason on stderr (review of work item 532).
func TestAntigravityBootstrapAnswersJSONByItsArgv(t *testing.T) {
	t.Parallel()
	run := func(home string, env []string, args ...string) (string, string, error) {
		bootstrap, _ := agyBootstrap(t)
		cmd := exec.CommandContext(t.Context(), "/bin/sh", append([]string{bootstrap}, args...)...) //nolint:gosec // argv fixture, no shell command string
		cmd.Env = append([]string{"HOME=" + home, "PATH=/usr/bin:/bin"}, env...)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		return stdout.String(), stderr.String(), err
	}
	_, home := agyBootstrap(t)
	out, _, err := run(home, []string{"CODEX_THREAD_ID=0199c0de-0000-7000-8000-000000000042"}, "hook", "prompt", "--host", "antigravity", "--plugin-root", ".")
	if err != nil || out != "{}\n" {
		t.Fatalf("cold prompt under another host's marker: %q %v", out, err)
	}
	broken := t.TempDir()
	if err := os.MkdirAll(filepath.Join(broken, ".config/brigade"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, ".config/brigade/dev-binary"), []byte("/nowhere/brigade\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"session-start", "prompt", "stop"} {
		out, errOut, err := run(broken, []string{"ANTIGRAVITY_CONVERSATION_ID=b95ca511-d709-4698-99c5-e85de16b4ab8"}, "hook", sub, "--host=antigravity", "--plugin-root", ".")
		if err != nil || out != "{}\n" || !strings.Contains(errOut, "dev-binary") {
			t.Fatalf("%s with a broken dev pointer: stdout %q, stderr %q, %v", sub, out, errOut, err)
		}
	}
	// A command in the session, not a hook, keeps the plain exit code.
	if _, _, err := run(broken, []string{"ANTIGRAVITY_CONVERSATION_ID=b95ca511-d709-4698-99c5-e85de16b4ab8"}, "whoami"); err == nil {
		t.Fatal("a broken dev pointer let whoami exit 0")
	}
}

// TestTheBootstrapAddsNoSystemMessageToAnAgyConversation: every answer the
// launcher itself gives an Antigravity hook adds user messages only, as
// the hook's own answers do (the owner's ruling of 2026-11-27 on work item
// 535). The cold-cache start answer is read from the script, since running
// it would start a real download in the background.
func TestTheBootstrapAddsNoSystemMessageToAnAgyConversation(t *testing.T) {
	t.Parallel()
	script, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "plugin/bin/brigade"))
	if err != nil {
		t.Fatal(err)
	}
	answers := 0
	for _, line := range strings.Split(string(script), "\n") {
		if !strings.Contains(line, "injectSteps") {
			continue
		}
		start, end := strings.Index(line, "'{"), strings.LastIndex(line, "}'")
		if start < 0 || end < start {
			t.Fatalf("an injectSteps answer not in single quotes: %s", line)
		}
		var answer struct {
			InjectSteps []map[string]any `json:"injectSteps"`
		}
		if err := json.Unmarshal([]byte(line[start+1:end+1]), &answer); err != nil {
			t.Fatalf("not JSON: %s: %v", line, err)
		}
		for _, step := range answer.InjectSteps {
			if _, ok := step["userMessage"].(string); !ok || len(step) != 1 {
				t.Fatalf("a step that is not one user message: %v", step)
			}
		}
		answers++
	}
	if answers == 0 {
		t.Fatal("the launcher gives an Antigravity hook no injectSteps answer: the test reads nothing")
	}
}
