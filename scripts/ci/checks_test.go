// Package ci_test is the test of the four POSIX-sh gates in scripts/ci/. It holds no non-test code: the gates
// are shell, and this file is the thing that proves each of them can FAIL, which is the only property a check
// really has. Every case builds its own fixture tree under t.TempDir() — with its own `git init` where the
// check reads the index — and runs the real script against it through `sh`. Two cases deliberately run
// read-only against the repository itself: plugin-check.sh and no-secrets.sh must pass on the real tree.
package ci_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/hook"
	"github.com/appshapes/brigade/internal/testutil"
)

// ---------------------------------------------------------------------------------------------------------
// running a script

type result struct {
	code   int
	stdout string
	stderr string
}

func (r result) all() string { return r.stdout + r.stderr }

// runScript runs scripts/ci/<name> with `sh`, from workdir, with an environment built from scratch.
func runScript(t *testing.T, name, workdir string, env []string, args ...string) result {
	t.Helper()
	script := filepath.Join(testutil.RepoRoot(t), "scripts", "ci", name)
	//nolint:gosec // G204: a fixed script path under the repository and this test's own fixture arguments
	cmd := exec.CommandContext(t.Context(), "sh", append([]string{script}, args...)...)
	cmd.Dir = workdir
	cmd.Env = env
	cmd.Stdin = strings.NewReader("")
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	err := cmd.Run()
	r := result{stdout: out.String(), stderr: errb.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		r.code = ee.ExitCode()
	default:
		t.Fatalf("running %s: %v", script, err)
	}
	return r
}

// baseEnv is the environment every fixture run gets: PATH so tools can be found, a temp HOME so git never
// reads the developer's own configuration, and the two GIT_CONFIG_* muzzles for the same reason.
func baseEnv(t *testing.T, home string, extra ...string) []string {
	t.Helper()
	return append([]string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + home,
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=ci-test",
		"GIT_AUTHOR_EMAIL=ci-test@example.invalid",
	}, extra...)
}

func wantPass(t *testing.T, r result, mentions ...string) {
	t.Helper()
	if r.code != 0 {
		t.Fatalf("exit %d, want 0\nstdout: %s\nstderr: %s", r.code, r.stdout, r.stderr)
	}
	for _, m := range mentions {
		if !strings.Contains(r.all(), m) {
			t.Fatalf("output does not mention %q\n%s", m, r.all())
		}
	}
}

func wantFail(t *testing.T, r result, mentions ...string) {
	t.Helper()
	if r.code == 0 {
		t.Fatalf("exit 0, want non-zero\nstdout: %s\nstderr: %s", r.stdout, r.stderr)
	}
	for _, m := range mentions {
		if !strings.Contains(r.all(), m) {
			t.Fatalf("output does not mention %q\n%s", m, r.all())
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// fixture trees

// tree is a throwaway repository: <root>/plugin/... plus its own .git, so `git ls-files -s` has an index to
// read that has nothing to do with the developer's checkout.
type tree struct {
	t    *testing.T
	root string
	home string
}

func newTree(t *testing.T) *tree {
	t.Helper()
	root := t.TempDir()
	tr := &tree{t: t, root: root, home: filepath.Join(root, ".home")}
	if err := os.MkdirAll(tr.home, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	tr.git("init", "-q")
	return tr
}

func (tr *tree) git(args ...string) {
	tr.t.Helper()
	//nolint:gosec // G204: git with a fixed argument list, inside this test's own temp directory
	cmd := exec.CommandContext(tr.t.Context(), "git", args...)
	cmd.Dir = tr.root
	cmd.Env = baseEnv(tr.t, tr.home)
	if out, err := cmd.CombinedOutput(); err != nil {
		tr.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func (tr *tree) write(rel, body string) {
	tr.t.Helper()
	p := filepath.Join(tr.root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		tr.t.Fatalf("mkdir: %v", err)
	}
	//nolint:gosec // G703: rel is a compile-time literal at every call site in this file, joined onto this
	// test's own t.TempDir(); nothing here writes outside the throwaway tree.
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		tr.t.Fatalf("writing %s: %v", rel, err)
	}
}

// pluginTree lays out the pre-release plugin: the real bootstrap (so the syntax and shellcheck checks have
// something honest to read), VERSION 0.0.0 and an empty checksums.txt, all tracked, with the bootstrap
// recorded 100755 exactly as the repository records it.
func (tr *tree) pluginTree() {
	tr.t.Helper()
	src := filepath.Join(testutil.RepoRoot(tr.t), "plugin", "bin", "brigade")
	body, err := os.ReadFile(src)
	if err != nil {
		tr.t.Fatalf("reading %s: %v", src, err)
	}
	tr.write("plugin/bin/brigade", string(body))
	tr.write("plugin/bin/VERSION", "0.0.0\n")
	tr.write("plugin/bin/checksums.txt", "")
	tr.git("add", "plugin")
	tr.git("update-index", "--add", "--chmod=+x", "plugin/bin/brigade")
	//nolint:gosec // G302: the bootstrap fixture must be executable for plugin-check's own -x assertion
	if err := os.Chmod(filepath.Join(tr.root, "plugin", "bin", "brigade"), 0o700); err != nil {
		tr.t.Fatalf("chmod: %v", err)
	}
}

func (tr *tree) env(extra ...string) []string { return baseEnv(tr.t, tr.home, extra...) }

// restrictedPath builds a PATH directory of symlinks to exactly the named tools, so a check that asks
// `command -v <tool>` can be made to find nothing.
func restrictedPath(t *testing.T, root string, tools ...string) string {
	t.Helper()
	dir := filepath.Join(root, ".restricted-bin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, tool := range tools {
		src, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("%s is not on PATH: cannot build a restricted PATH for this case", tool)
		}
		if err := os.Symlink(src, filepath.Join(dir, tool)); err != nil && !os.IsExist(err) {
			t.Fatalf("symlinking %s: %v", tool, err)
		}
	}
	return dir
}

// haveShellcheck reports whether shellcheck is on PATH. It is its own function so that the lookup (which
// reads PATH) stays out of any test body that also writes fixture files.
func haveShellcheck() bool {
	_, err := exec.LookPath("shellcheck")
	return err == nil
}

// ---------------------------------------------------------------------------------------------------------
// plugin-check.sh

func TestPluginCheck(t *testing.T) {
	t.Parallel()

	t.Run("the real repository passes", func(t *testing.T) {
		t.Parallel()
		root := testutil.RepoRoot(t)
		r := runScript(t, "plugin-check.sh", root, baseEnv(t, t.TempDir()))
		wantPass(t, r, "all checks passed", "100755")
	})

	t.Run("the pre-release fixture passes and reports its skips", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "all checks passed", "skip:", "(P3-1)")
	})

	t.Run("a stray file under plugin/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/.DS_Store", "junk\n")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not allow-listed", ".DS_Store")
	})

	t.Run("a skill outside skills/<name>/SKILL.md fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/skills/teammsg/extra/SKILL.md", "# nope\n")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not allow-listed")
	})

	t.Run("a 100644 bootstrap fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.git("update-index", "--chmod=-x", "plugin/bin/brigade")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "must be 100755")
	})

	t.Run("a second executable under plugin/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "# brigade\n")
		tr.git("add", "plugin/README.md")
		tr.git("update-index", "--add", "--chmod=+x", "plugin/README.md")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "must be 100644")
	})

	t.Run("a VERSION that is not a single token fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "0.1.0\nand more\n")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "exactly one line")
	})

	t.Run("a VERSION that disagrees with plugin.json fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"9.9.9\"\n}\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "!= plugin/bin/VERSION")
	})

	t.Run("a matching plugin.json passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"0.0.0\"\n}\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "matches plugin/.claude-plugin/plugin.json")
	})

	t.Run("an mcpServers key fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"version\": \"0.0.0\",\n  \"mcpServers\": {}\n}\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "mcpServers or channels")
	})

	t.Run("a --channels string anywhere under plugin/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "run `brigade send --channels team`\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "--channels")
	})

	t.Run("a hooks.json naming a missing command fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/hooks/hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/does-not-exist", "args": ["hook", "session-start"], "timeout": 60}]}]}}`+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "does not exist")
	})

	t.Run("a hooks.json whose command is a shell string fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/hooks/hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade hook session-start", "timeout": 60}]}]}}`+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "shell string")
	})

	t.Run("a hooks.json whose type is not command fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/hooks/hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "shell", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "args": []}]}]}}`+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not \"command\"")
	})

	t.Run("the plan's own hooks.json passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/hooks/hooks.json", `{
  "hooks": {
    "SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "args": ["hook", "session-start"], "timeout": 60, "statusMessage": "Connecting to the Brigade team"}]}],
    "UserPromptSubmit": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "args": ["hook", "prompt"], "timeout": 5}]}],
    "SessionEnd": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "args": ["hook", "session-end"], "timeout": 5}]}]
  }
}
`)
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "exec form and every hook command exists")
	})

	t.Run("a minified hooks.json with a hook that has no command fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		// Two hook objects on ONE line, only one of them exec form. Counting matching LINES rather than
		// occurrences made this pass, which is the one shape check 5 exists to catch.
		tr.write("plugin/hooks/hooks.json", `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "args": []}, {"type": "command"}]}]}}`+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "every hook must be exec form")
	})

	t.Run("a minified but complete hooks.json passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/hooks/hooks.json", `{"hooks":{"SessionStart":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/brigade","args":["hook","session-start"]}]}],"SessionEnd":[{"hooks":[{"type":"command","command":"${CLAUDE_PLUGIN_ROOT}/bin/brigade","args":["hook","session-end"]}]}]}}`+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "exec form and every hook command exists")
	})

	t.Run("a plugin/.mcp.json fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/.mcp.json", "{}\n")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		// Caught by check 1: .mcp.json is not on the allowlist and check 1 runs first, which is why check 4
		// carries no `[ ! -e plugin/.mcp.json ]` guard of its own. The message is asserted, not just the
		// exit code, so that moving the file onto the allowlist could not silently keep this case green.
		wantFail(t, r, "not allow-listed under plugin/", "plugin/.mcp.json")
	})

	t.Run("a bootstrap that does not parse fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/brigade", "#!/bin/sh\n) unbalanced\n")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "-n plugin/bin/brigade failed")
	})

	t.Run("a shellcheck finding in the bootstrap fails", func(t *testing.T) {
		t.Parallel()
		if !haveShellcheck() {
			t.Skip("shellcheck is not installed on this host; CI enforces it because CI is set")
		}
		tr := newTree(t)
		tr.pluginTree()
		// SC1007: `CDPATH= cd` is valid sh and is what plan 6.2 prints, so it is also the finding this
		// script exists to keep out of the shipped bootstrap.
		tr.write("plugin/bin/brigade", "#!/bin/sh\nCDPATH= cd /tmp || true\n")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "shellcheck reported problems")
	})

	t.Run("a missing shellcheck is a warning locally and a failure under CI", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		// git, find, sort, awk, sed, head, grep and sh are everything plugin-check.sh runs that is not a
		// shell builtin. shellcheck is deliberately absent from this PATH.
		dir := restrictedPath(t, tr.root, "git", "find", "sort", "awk", "sed", "head", "grep", "sh")

		local := runScript(t, "plugin-check.sh", tr.root, tr.env("PATH="+dir))
		wantPass(t, local, "shellcheck is not installed")

		ci := runScript(t, "plugin-check.sh", tr.root, tr.env("PATH="+dir, "CI=true"))
		wantFail(t, ci, "shellcheck is not installed and CI is set")
	})

	// ---- check 6: the hook subcommand names (P3-6) ----------------------------------------------------

	t.Run("a hooks.json naming a subcommand the binary does not implement fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		// Exec form, the command exists, the args array is an array: check 5 is silent and only check 6
		// can catch the typo. `start` is what a careless rename of `session-start` looks like.
		tr.write(hooksRel, hooksJSON(`"args": ["hook", "start"]`))
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "names the hook subcommand 'start'", "does not implement")
	})

	t.Run("a hooks.json whose args do not begin with hook fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write(hooksRel, hooksJSON(`"args": ["session-start"]`))
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "first element is 'session-start'")
	})

	t.Run("a hooks.json with no args array at all fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		// The bootstrap with no arguments is a valid exec-form hook to check 5 and a hook that can never
		// do anything to check 6.
		tr.write(hooksRel, `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", "timeout": 60}]}]}}`+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "declares no \"args\" array")
	})

	t.Run("the real hooks.json passes check 6", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		body, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), hooksRel))
		if err != nil {
			t.Fatalf("reading the real %s: %v", hooksRel, err)
		}
		tr.write(hooksRel, string(body))
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "names a brigade hook subcommand that exists")
	})

	// ---- check 7: the plugin README's Status paragraph (P3-6) -----------------------------------------

	t.Run("a plugin README that still says the wiring is not implemented fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "# brigade\n\n## Status\n\nThe hooks and the commands are not implemented yet.\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "still disclaims the wiring")
	})

	t.Run("a plugin README that says the wiring is not runnable fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "# brigade\n\n## Status\n\nNot runnable yet: the harness lands with P3-3.\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "still disclaims the wiring")
	})

	t.Run("a plugin README with no Status section fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "# brigade\n\nEverything works.\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "no '## Status' section")
	})

	t.Run("a plugin README with a Status section and no disclaimer passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "# brigade\n\n## Status\n\nThe hooks, the commands and the watcher run end to end.\n")
		tr.git("add", "plugin")
		r := runScript(t, "plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "no 'not implemented' disclaimer")
	})
}

// hooksRel is the shipped hook manifest, relative to a repository root.
const hooksRel = "plugin/hooks/hooks.json"

// hooksJSON is a one-hook exec-form manifest whose args clause is the caller's, so a case can vary exactly
// the thing check 6 reads and nothing else.
func hooksJSON(args string) string {
	return `{"hooks": {"SessionStart": [{"hooks": [{"type": "command", "command": "${CLAUDE_PLUGIN_ROOT}/bin/brigade", ` + args + `, "timeout": 60}]}]}}` + "\n"
}

// subcommandList is the `hook_subcommands="…"` line of plugin-check.sh: the fixed list check 6 compares
// hooks.json against.
var subcommandList = regexp.MustCompile(`(?m)^hook_subcommands="([^"]*)"`)

// TestHookSubcommandListMatchesTheGoConstants keeps the shell check honest about what the binary implements.
// Check 6 cannot ask the Go code (plugin-check.sh runs on hosts with no toolchain and must stay textual), so
// the list is a literal in the script — and a literal drifts. This test is the join: rename
// hook.SubSessionStart in Go and this fails, which is the only way the shell list can be kept true.
func TestHookSubcommandListMatchesTheGoConstants(t *testing.T) {
	t.Parallel()
	script := filepath.Join(testutil.RepoRoot(t), "scripts", "ci", "plugin-check.sh")
	body, err := os.ReadFile(script)
	if err != nil {
		t.Fatalf("reading %s: %v", script, err)
	}
	m := subcommandList.FindStringSubmatch(string(body))
	if m == nil {
		t.Fatalf("plugin-check.sh has no `hook_subcommands=\"…\"` line: check 6 has lost its authority")
	}
	got := strings.Fields(m[1])
	slices.Sort(got)
	want := []string{hook.SubSessionStart, hook.SubPrompt, hook.SubSessionEnd}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("plugin-check.sh hook_subcommands = %v, want the hook package's subcommands %v", got, want)
	}
}

// ---------------------------------------------------------------------------------------------------------
// no-secrets.sh
//
// The planted strings below are the whole point of the file: they are positive controls for a scanner, not
// credentials. They are shaped like a JWT and like a Supabase secret key and decode to nothing.

const (
	plantedJWT       = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.eyJyb2xlIjoicGxhbnRlZC1jb250cm9sIn0.not-a-real-signature-value" //nolint:gosec // G101: a planted positive control for the scanner under test
	plantedSecretKey = "sb_secret_planted_control_not_a_real_key"                                                            //nolint:gosec // G101: as above
)

func TestNoSecrets(t *testing.T) {
	t.Parallel()

	t.Run("the real repository passes", func(t *testing.T) {
		t.Parallel()
		root := testutil.RepoRoot(t)
		r := runScript(t, "no-secrets.sh", root, baseEnv(t, t.TempDir()))
		wantPass(t, r, "clean", "scanned")
	})

	t.Run("a clean fixture passes and says what it scanned", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("docs/notes.md", "nothing to see\n")
		tr.git("add", "docs")
		r := runScript(t, "no-secrets.sh", tr.root, tr.env())
		wantPass(t, r, "scanned", "clean")
	})

	t.Run("a planted sb_secret_ key under plugin/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/README.md", "key: "+plantedSecretKey+"\n")
		tr.git("add", "plugin")
		r := runScript(t, "no-secrets.sh", tr.root, tr.env())
		wantFail(t, r, "plugin/README.md", "sb_secret_")
	})

	t.Run("a planted JWT in a tracked source file fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("internal/config.go", "const token = \""+plantedJWT+"\"\n")
		tr.git("add", "internal")
		r := runScript(t, "no-secrets.sh", tr.root, tr.env())
		wantFail(t, r, "internal/config.go")
	})

	t.Run("a planted JWT in an excluded fixture tree passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("internal/redact_test.go", "const token = \""+plantedJWT+"\"\n")
		tr.write("internal/testdata/sample.json", "{\"jwt\": \""+plantedJWT+"\"}\n")
		tr.write("docs/research/evidence.md", plantedJWT+"\n")
		tr.git("add", "internal", "docs")
		r := runScript(t, "no-secrets.sh", tr.root, tr.env())
		wantPass(t, r, "clean")
	})

	t.Run("service_role in a built binary passes, a credential shape in the same binary fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		// The role NAME is not a key. The Supabase adapter will embed it -- as an error-mapping string and
		// as the SQL role the migrations REVOKE from -- so a binary carrying it is clean, and a rule that
		// failed on it would only teach people to disable the gate. The second half is the positive
		// control that keeps the binary in scope at all: a real service-role key is JWT-shaped, and the
		// shape scan must still find one in the very same bytes.
		bin := "dist-cross/brigade_1.2.3_linux_amd64"
		tr.write(bin, "\x00\x01PGRST service_role\x00\n")
		wantPass(t, runScript(t, "no-secrets.sh", tr.root, tr.env()), "clean")

		tr.write(bin, "\x00\x01PGRST service_role\x00"+plantedJWT+"\x00\n")
		wantFail(t, runScript(t, "no-secrets.sh", tr.root, tr.env()), bin, "JWT-shaped")
	})

	t.Run("an sb_secret_ key in a built binary fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("bin/brigade", "\x00ELF\x00"+plantedSecretKey+"\x00\n")
		wantFail(t, runScript(t, "no-secrets.sh", tr.root, tr.env()), "bin/brigade", "sb_secret_")
	})

	t.Run("service_role fails under plugin/ but not in the source", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("supabase/policies.md", "grant to service_role\n")
		tr.git("add", "supabase")
		wantPass(t, runScript(t, "no-secrets.sh", tr.root, tr.env()), "clean")

		tr.write("plugin/README.md", "uses the service_role key\n")
		tr.git("add", "plugin")
		wantFail(t, runScript(t, "no-secrets.sh", tr.root, tr.env()), "service_role")
	})
}

// ---------------------------------------------------------------------------------------------------------
// checksums-check.sh

// checksumLines is a well-formed committed checksums.txt for version v.
func checksumLines(v, hashPrefix string) string {
	var b strings.Builder
	for i, tgt := range []string{"darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64"} {
		h := hashPrefix + strings.Repeat("0", 63-len(hashPrefix)) + string(rune('1'+i))
		b.WriteString(h + "  brigade_" + v + "_" + tgt + "\n")
	}
	return b.String()
}

func TestChecksumsCheck(t *testing.T) {
	t.Parallel()

	t.Run("the real repository passes in the pre-release state", func(t *testing.T) {
		t.Parallel()
		root := testutil.RepoRoot(t)
		// Only the pre-release state can be asserted from a unit test: with
		// VERSION at 0.0.0 the script checks the sentinel and skips (b) and
		// (c). At any real version, rule (c) needs a fresh cross-build that
		// reproduces the committed file, or the published release behind
		// it -- a network call through gh, and one that cannot succeed
		// between `make release`'s step 1 (bump the pins) and step 5 (push
		// the tag), which is exactly when the release chain's own `make
		// push` runs this test. Measured in the 0.0.1-rc1 rehearsal on
		// 2026-09-02: the chain stopped here, before the release commit. The
		// real-version state is covered by `make checksums-check` (CI, with
		// a real fresh build and GH_TOKEN), so this test skips there rather
		// than pretend an "irrelevant" fresh file can prove anything.
		pinned, err := os.ReadFile(filepath.Join(root, "plugin", "bin", "VERSION"))
		if err != nil {
			t.Fatalf("reading plugin/bin/VERSION: %v", err)
		}
		if v := strings.TrimSpace(string(pinned)); v != "0.0.0" {
			t.Skipf("plugin/bin/VERSION pins %s, not the pre-release sentinel: rule (c) needs a fresh build or the published release (make checksums-check covers it)", v)
		}
		fresh := filepath.Join(t.TempDir(), "fresh.txt")
		if err := os.WriteFile(fresh, []byte("irrelevant\n"), 0o600); err != nil {
			t.Fatalf("writing: %v", err)
		}
		r := runScript(t, "checksums-check.sh", root, baseEnv(t, t.TempDir()), fresh)
		wantPass(t, r, "pre-release state")
	})

	t.Run("no argument fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		wantFail(t, runScript(t, "checksums-check.sh", tr.root, tr.env()), "usage:")
	})

	t.Run("0.0.0 with an empty checksums.txt passes and skips (b) and (c)", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		fresh := filepath.Join(tr.root, "fresh.txt")
		tr.write("fresh.txt", checksumLines("0.0.0", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), fresh)
		wantPass(t, r, "pre-release state", "(b) and (c) skipped")
	})

	t.Run("0.0.0 with a non-empty checksums.txt fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/checksums.txt", checksumLines("0.0.0", "ab"))
		tr.write("fresh.txt", checksumLines("0.0.0", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "is not empty")
	})

	t.Run("(a) a real version with no plugin.json fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.3", "ab"))
		tr.write("fresh.txt", checksumLines("1.2.3", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "(a)", "does not exist")
	})

	t.Run("(a) a plugin.json that disagrees fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.4\"\n}\n")
		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.3", "ab"))
		tr.write("fresh.txt", checksumLines("1.2.3", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "(a)", "1.2.4")
	})

	t.Run("(b) three lines fails, four well-formed lines passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n")
		short := strings.SplitAfter(checksumLines("1.2.3", "ab"), "\n")
		tr.write("plugin/bin/checksums.txt", strings.Join(short[:3], ""))
		tr.write("fresh.txt", checksumLines("1.2.3", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "(b)", "3 line(s)")

		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.3", "ab"))
		wantPass(t, runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt")), "(b)", "(c)")
	})

	t.Run("(b) four lines naming the wrong version fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n")
		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.9", "ab"))
		tr.write("fresh.txt", checksumLines("1.2.9", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "(b)", "expected exactly 1")
	})

	t.Run("(b) a malformed hash column fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n")
		tr.write("plugin/bin/checksums.txt", strings.Replace(checksumLines("1.2.3", "ab"), "ab", "ZZ", 1))
		tr.write("fresh.txt", checksumLines("1.2.3", "ab"))
		r := runScript(t, "checksums-check.sh", tr.root, tr.env(), filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "(b)")
	})

	t.Run("(c) a stale file with no release fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n")
		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.3", "ab"))
		tr.write("fresh.txt", checksumLines("1.2.3", "cd"))
		// A `gh` that fails, standing in for "no published release" without touching the network.
		dir := filepath.Join(tr.root, ".fakebin")
		writeExec(t, filepath.Join(dir, "gh"), "#!/bin/sh\necho 'release not found' >&2\nexit 1\n")
		r := runScript(t, "checksums-check.sh", tr.root,
			tr.env("PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH")),
			filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "checksums are stale relative to the source and no release backs them")
	})

	t.Run("(c) a published release that matches passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n")
		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.3", "ab"))
		tr.write("fresh.txt", checksumLines("1.2.3", "cd"))
		tr.write("released.txt", checksumLines("1.2.3", "ab"))
		dir := filepath.Join(tr.root, ".fakebin")
		writeExec(t, filepath.Join(dir, "gh"), "#!/bin/sh\ncat '"+filepath.Join(tr.root, "released.txt")+"'\n")
		r := runScript(t, "checksums-check.sh", tr.root,
			tr.env("PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH")),
			filepath.Join(tr.root, "fresh.txt"))
		wantPass(t, r, "the published release v1.2.3 backs")
	})

	t.Run("(c) a published release that disagrees fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.pluginTree()
		tr.write("plugin/bin/VERSION", "1.2.3\n")
		tr.write("plugin/.claude-plugin/plugin.json", "{\n  \"name\": \"brigade\",\n  \"version\": \"1.2.3\"\n}\n")
		tr.write("plugin/bin/checksums.txt", checksumLines("1.2.3", "ab"))
		tr.write("fresh.txt", checksumLines("1.2.3", "cd"))
		tr.write("released.txt", checksumLines("1.2.3", "ef"))
		dir := filepath.Join(tr.root, ".fakebin")
		writeExec(t, filepath.Join(dir, "gh"), "#!/bin/sh\ncat '"+filepath.Join(tr.root, "released.txt")+"'\n")
		r := runScript(t, "checksums-check.sh", tr.root,
			tr.env("PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH")),
			filepath.Join(tr.root, "fresh.txt"))
		wantFail(t, r, "the published v1.2.3 disagrees")
	})
}

func writeExec(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	//nolint:gosec // G306: an executable test fixture; 0700 keeps it owner-only
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

// ---------------------------------------------------------------------------------------------------------
// release-verify.sh

func TestReleaseVerify(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, dir, name, body string) string {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", p, err)
		}
		return p
	}

	t.Run("identical hash columns in a different order pass", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a := write(t, dir, "goreleaser.txt", "bbb  brigade_1.2.3_linux_amd64\naaa  brigade_1.2.3_darwin_arm64\n")
		b := write(t, dir, "committed.txt", "aaa  brigade_1.2.3_darwin_arm64\nbbb  brigade_1.2.3_linux_amd64\n")
		wantPass(t, runScript(t, "release-verify.sh", dir, baseEnv(t, dir), a, b), "2 hash(es)")
	})

	t.Run("a differing hash column fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a := write(t, dir, "goreleaser.txt", "aaa  brigade_1.2.3_darwin_arm64\nxxx  brigade_1.2.3_linux_amd64\n")
		b := write(t, dir, "committed.txt", "aaa  brigade_1.2.3_darwin_arm64\nbbb  brigade_1.2.3_linux_amd64\n")
		wantFail(t, runScript(t, "release-verify.sh", dir, baseEnv(t, dir), a, b), "refusing to publish")
	})

	t.Run("an empty file fails rather than passing vacuously", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		a := write(t, dir, "goreleaser.txt", "")
		b := write(t, dir, "committed.txt", "aaa  brigade_1.2.3_darwin_arm64\n")
		wantFail(t, runScript(t, "release-verify.sh", dir, baseEnv(t, dir), a, b), "is empty")
	})

	t.Run("a missing argument fails", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		wantFail(t, runScript(t, "release-verify.sh", dir, baseEnv(t, dir)), "usage:")
	})
}
