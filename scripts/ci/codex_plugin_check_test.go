package ci_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/hook"
	"github.com/appshapes/brigade/internal/testutil"
)

// codexTree lays out the Codex CLI package of card 107 in the throwaway
// tree: the real codex/brigade/ files (manifest, hooks.json, skills, README),
// the real bootstrap under both bin/ directories with the pre-release pins,
// and the repository root's marketplace file, every executable recorded
// 100755 as the repository records it.
func (tr *tree) codexTree() {
	tr.t.Helper()
	repo := testutil.RepoRoot(tr.t)
	copyFile := func(rel string) {
		body, err := os.ReadFile(filepath.Join(repo, rel))
		if err != nil {
			tr.t.Fatalf("reading %s: %v", rel, err)
		}
		tr.write(rel, string(body))
	}
	copyFile("plugin/bin/brigade")
	tr.write("plugin/bin/VERSION", "0.0.0\n")
	tr.write("plugin/bin/checksums.txt", "")
	copyFile("codex/brigade/bin/brigade")
	tr.write("codex/brigade/bin/VERSION", "0.0.0\n")
	tr.write("codex/brigade/bin/checksums.txt", "")
	for _, rel := range []string{
		"codex/brigade/hooks/hooks.json", "codex/brigade/README.md", ".agents/plugins/marketplace.json",
		"codex/brigade/skills/team-messaging/SKILL.md", "codex/brigade/skills/sessions/SKILL.md",
		"codex/brigade/skills/setup/SKILL.md", "codex/brigade/skills/join/SKILL.md", "codex/brigade/skills/update/SKILL.md",
		"codex/brigade/skills/email/SKILL.md", "codex/brigade/skills/slack/SKILL.md", "codex/brigade/skills/quiet/SKILL.md",
	} {
		copyFile(rel)
	}
	manifest, err := os.ReadFile(filepath.Join(repo, "codex/brigade/.codex-plugin/plugin.json"))
	if err != nil {
		tr.t.Fatalf("reading the manifest: %v", err)
	}
	tr.write("codex/brigade/.codex-plugin/plugin.json", replaceVersion(string(manifest), "0.0.0"))
	tr.git("add", "plugin", "codex", ".agents")
	for _, exe := range []string{"plugin/bin/brigade", "codex/brigade/bin/brigade"} {
		tr.git("update-index", "--add", "--chmod=+x", exe)
		//nolint:gosec // G302: the fixtures must be executable for the check's own -x assertion
		if err := os.Chmod(filepath.Join(tr.root, exe), 0o700); err != nil {
			tr.t.Fatalf("chmod: %v", err)
		}
	}
}

// codexHooksCommand is the exact command form check 6 of codex-plugin-check.sh
// pins, for one subcommand.
func codexHooksCommand(sub string) string {
	return `"\"${PLUGIN_ROOT}/bin/brigade\" hook ` + sub + ` --host codex --plugin-root \"${PLUGIN_ROOT}\""`
}

func TestCodexPluginCheck(t *testing.T) {
	t.Parallel()

	t.Run("the real repository passes", func(t *testing.T) {
		t.Parallel()
		root := testutil.RepoRoot(t)
		r := runScript(t, "codex-plugin-check.sh", root, baseEnv(t, t.TempDir()))
		wantPass(t, r, "all checks passed", "exact command form", "names the brigade plugin at ./codex/brigade")
	})

	t.Run("the pre-release fixture passes", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantPass(t, r, "all checks passed")
	})

	t.Run("a stray file under codex/brigade/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write("codex/brigade/hooks/.DS_Store", "junk\n")
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not allow-listed under codex/brigade/")
	})

	t.Run("an asset that differs from the Claude package's fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write("codex/brigade/bin/VERSION", "0.0.0\n# drift\n")
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "differs from plugin/bin/VERSION")
	})

	t.Run("a manifest version that is not the pin fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		manifest, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/.codex-plugin/plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("codex/brigade/.codex-plugin/plugin.json", replaceVersion(string(manifest), "9.9.9"))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "!= codex/brigade/bin/VERSION")
	})

	t.Run("a manifest carrying mcpServers fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		manifest, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/.codex-plugin/plugin.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("codex/brigade/.codex-plugin/plugin.json", replaceOnce(string(manifest), `"skills": "./skills/",`, `"skills": "./skills/", "mcpServers": {},`))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "this package must not")
	})

	t.Run("a hook command of another form fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		hooks, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/hooks/hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		// The host dropped: the hook would key nothing and the session never connect.
		tr.write("codex/brigade/hooks/hooks.json", replaceOnce(string(hooks), codexHooksCommand("prompt"), `"\"${PLUGIN_ROOT}/bin/brigade\" hook prompt"`))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not of the exact form")
	})

	t.Run("a hook naming a subcommand the binary lacks fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		hooks, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/hooks/hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("codex/brigade/hooks/hooks.json", replaceOnce(string(hooks), codexHooksCommand("prompt"), codexHooksCommand("start")))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "'start'", "does not implement")
	})

	t.Run("an event running another event's subcommand fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		hooks, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/hooks/hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("codex/brigade/hooks/hooks.json", replaceOnce(string(hooks), codexHooksCommand("prompt"), codexHooksCommand("session-start")))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "UserPromptSubmit block does not run `hook prompt`")
	})

	t.Run("a fourth event fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		hooks, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/hooks/hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("codex/brigade/hooks/hooks.json", replaceOnce(string(hooks), `    "SessionEnd": [`, `    "Stop": [{"hooks": [{"type": "command", "command": "echo"}]}],
    "SessionEnd": [`))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "want exactly 3")
	})

	t.Run("a SessionEnd timeout over Codex's cap fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		hooks, err := os.ReadFile(filepath.Join(tr.root, "codex/brigade/hooks/hooks.json"))
		if err != nil {
			t.Fatal(err)
		}
		tr.write("codex/brigade/hooks/hooks.json", replaceOnce(string(hooks), `"timeout": 3`, `"timeout": 10`))
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "caps a SessionEnd hook at 3 s")
	})

	t.Run("a marketplace that does not name the package fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write(".agents/plugins/marketplace.json", `{"name": "brigade", "plugins": []}`+"\n")
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "names no plugin brigade")
	})

	t.Run("a skill not named after its directory fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write("codex/brigade/skills/update/SKILL.md", "---\nname: upgrade\ndescription: x\n---\n# x\n")
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "not named after its directory")
	})

	t.Run("a skill declaring disable-model-invocation fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		tr.write("codex/brigade/skills/update/SKILL.md", "---\nname: update\ndisable-model-invocation: true\n---\n# x\n")
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "disable-model-invocation")
	})

	t.Run("a symlink under codex/ fails", func(t *testing.T) {
		t.Parallel()
		tr := newTree(t)
		tr.codexTree()
		if err := os.Remove(filepath.Join(tr.root, "codex/brigade/README.md")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../plugin/README.md", filepath.Join(tr.root, "codex/brigade/README.md")); err != nil {
			t.Fatal(err)
		}
		r := runScript(t, "codex-plugin-check.sh", tr.root, tr.env())
		wantFail(t, r, "symlink")
	})
}

// codexSubcommandList is the `hook_subcommands="…"` line of codex-plugin-check.sh.
var codexSubcommandList = regexp.MustCompile(`(?m)^hook_subcommands="([^"]*)"`)

// TestCodexHookSubcommandListMatchesTheGoConstants is the Codex twin of
// TestHookSubcommandListMatchesTheGoConstants: check 6's authority is the
// hook package's three subcommands.
func TestCodexHookSubcommandListMatchesTheGoConstants(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "scripts", "ci", "codex-plugin-check.sh"))
	if err != nil {
		t.Fatal(err)
	}
	m := codexSubcommandList.FindSubmatch(data)
	if m == nil {
		t.Fatal("codex-plugin-check.sh has no `hook_subcommands=\"…\"` line: check 6 has lost its authority")
	}
	got := strings.Fields(string(m[1]))
	want := []string{hook.SubSessionStart, hook.SubPrompt, hook.SubSessionEnd}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("codex-plugin-check.sh hook_subcommands = %v, want %v", got, want)
	}
}

// TestCodexManifestsAreJSON parses the three Codex files the textual
// check reads — the manifest, hooks.json and the marketplace — as JSON,
// and pins what the check cannot see: the manifest's identity, the
// marketplace's source, and that each hook event carries one command.
func TestCodexManifestsAreJSON(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	manifest, ok := readJSONObject(t, root, "codex/brigade/.codex-plugin/plugin.json")
	if !ok {
		return
	}
	if name, _ := manifest["name"].(string); name != "brigade" {
		t.Errorf("manifest name %q", name)
	}
	if skills, _ := manifest["skills"].(string); skills != "./skills/" {
		t.Errorf("manifest skills %q", skills)
	}
	pin, err := os.ReadFile(filepath.Join(root, "plugin/bin/VERSION"))
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := manifest["version"].(string); v != strings.TrimSpace(string(pin)) {
		t.Errorf("manifest version %q, pin %q", v, strings.TrimSpace(string(pin)))
	}
	hooks, ok := readJSONObject(t, root, "codex/brigade/hooks/hooks.json")
	if !ok {
		return
	}
	events, _ := hooks["hooks"].(map[string]any)
	if len(events) != 3 {
		t.Errorf("hooks.json declares %d events, want 3", len(events))
	}
	for event, sub := range map[string]string{"SessionStart": "session-start", "UserPromptSubmit": "prompt", "SessionEnd": "session-end"} {
		groups, _ := events[event].([]any)
		if len(groups) != 1 {
			t.Errorf("%s: %d groups, want 1", event, len(groups))
			continue
		}
		group, _ := groups[0].(map[string]any)
		list, _ := group["hooks"].([]any)
		if len(list) != 1 {
			t.Errorf("%s: %d hooks, want 1", event, len(list))
			continue
		}
		h, _ := list[0].(map[string]any)
		cmd, _ := h["command"].(string)
		if want := `"${PLUGIN_ROOT}/bin/brigade" hook ` + sub + ` --host codex --plugin-root "${PLUGIN_ROOT}"`; cmd != want {
			t.Errorf("%s command %q, want %q", event, cmd, want)
		}
	}
	market, ok := readJSONObject(t, root, ".agents/plugins/marketplace.json")
	if !ok {
		return
	}
	plugins, _ := market["plugins"].([]any)
	if len(plugins) != 1 {
		t.Fatalf("marketplace lists %d plugins, want 1", len(plugins))
	}
	p, _ := plugins[0].(map[string]any)
	src, _ := p["source"].(map[string]any)
	if p["name"] != "brigade" || src["source"] != "local" || src["path"] != "./codex/brigade" {
		b, _ := json.Marshal(p)
		t.Errorf("marketplace plugin %s", b)
	}
}
