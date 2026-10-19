package ci_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

var grokSkills = []string{"email", "join", "sessions", "setup", "slack", "team-messaging", "update"}

// Source validation deliberately makes no live-host or release-readiness claim.
// The shell check's strict mode separately requires release-produced pins.
func checkGrokSource(root string) error {
	read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(root, rel)) }
	pkg := "grok/brigade"
	allowed := map[string]bool{"README.md": true, ".grok-plugin/plugin.json": true, "hooks/hooks.json": true, "bin/brigade": true, "bin/VERSION": true, "bin/checksums.txt": true}
	for _, name := range grokSkills {
		allowed["skills/"+name+"/SKILL.md"] = true
	}
	if err := filepath.WalkDir(filepath.Join(root, pkg), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in Grok package")
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(filepath.Join(root, pkg), path)
		if !allowed[filepath.ToSlash(rel)] {
			return fmt.Errorf("unexpected package file: %s", rel)
		}
		return nil
	}); err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(root, pkg, "skills"))
	if err != nil {
		return err
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	if !slices.Equal(names, grokSkills) {
		return fmt.Errorf("expected exactly seven skills, got %v", names)
	}
	for _, name := range grokSkills {
		data, err := read(pkg + "/skills/" + name + "/SKILL.md")
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "\nname: "+name+"\n") || strings.Contains(string(data), "disable-model-invocation:") {
			return fmt.Errorf("unreachable or misnamed skill %s", name)
		}
	}
	data, err := read(pkg + "/.grok-plugin/plugin.json")
	if err != nil {
		return err
	}
	var manifest map[string]any
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	version, _ := manifest["version"].(string)
	if manifest["name"] != "brigade" || version == "" {
		return errors.New("invalid native manifest")
	}
	for _, key := range []string{"userConfig", "mcpServers", "channels"} {
		if _, ok := manifest[key]; ok {
			return fmt.Errorf("unsupported manifest member %s", key)
		}
	}
	data, err = read(".grok-plugin/marketplace.json")
	if err != nil {
		return err
	}
	var market struct {
		Name    string `json:"name"`
		Plugins []struct {
			Name   string `json:"name"`
			Source struct {
				Type string `json:"type"`
				Path string `json:"path"`
			} `json:"source"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(data, &market); err != nil {
		return err
	}
	if market.Name != "brigade" || len(market.Plugins) != 1 || market.Plugins[0].Name != "brigade" || market.Plugins[0].Source.Type != "local" || market.Plugins[0].Source.Path != "./grok/brigade" {
		return errors.New("catalog does not name the distinct Grok package")
	}
	data, err = read(pkg + "/hooks/hooks.json")
	if err != nil {
		return err
	}
	type handler struct {
		Type    string  `json:"type"`
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	}
	var hooks struct {
		Hooks map[string][]struct {
			Hooks []handler `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(data, &hooks); err != nil {
		return err
	}
	if len(hooks.Hooks) != 3 {
		return errors.New("expected exactly three native lifecycle hooks")
	}
	for event, sub := range map[string]string{"SessionStart": "session-start", "UserPromptSubmit": "prompt", "SessionEnd": "session-end"} {
		groups := hooks.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			return fmt.Errorf("invalid %s handlers", event)
		}
		h := groups[0].Hooks[0]
		want := `"${GROK_PLUGIN_ROOT}/bin/brigade" hook ` + sub + ` --host grok --plugin-root "${GROK_PLUGIN_ROOT}"`
		if h.Type != "command" || h.Command != want || h.Timeout <= 0 || h.Timeout > 5 || event == "SessionEnd" && h.Timeout > 1 {
			return fmt.Errorf("invalid %s command/timeout", event)
		}
	}
	bootstrap, err := read(pkg + "/bin/brigade")
	if err != nil {
		return err
	}
	canonical, err := read("plugin/bin/brigade")
	if err != nil {
		return err
	}
	if !bytes.Equal(bootstrap, canonical) {
		return errors.New("bootstrap differs from canonical source")
	}
	for _, asset := range []string{"VERSION", "checksums.txt"} {
		b, err := read(pkg + "/bin/" + asset)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		want, err := read("plugin/bin/" + asset)
		if err != nil {
			return err
		}
		if !bytes.Equal(b, want) {
			return fmt.Errorf("%s is not a canonical release asset", asset)
		}
	}
	return nil
}

func TestGrokPackageSource(t *testing.T) {
	t.Parallel()
	if err := checkGrokSource(testutil.RepoRoot(t)); err != nil {
		t.Fatal(err)
	}
}

func TestGrokPackageSourceRejectsDrift(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"skill missing", "skill unreachable", "extra file", "symlink", "wrong hook host", "wrong catalog", "long end timeout", "mcp manifest"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			repo := testutil.RepoRoot(t)
			for _, dir := range []string{"grok/brigade", ".grok-plugin", "plugin/bin"} {
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
				if err := os.WriteFile(path, bytes.Replace(b, []byte(from), []byte(to), 1), 0600); err != nil {
					t.Fatal(err)
				}
			}
			switch mutation {
			case "skill missing":
				if err := os.Remove(filepath.Join(root, "grok/brigade/skills/join/SKILL.md")); err != nil {
					t.Fatal(err)
				}
			case "skill unreachable":
				replace("grok/brigade/skills/join/SKILL.md", "name: join", "name: join\ndisable-model-invocation: true")
			case "extra file":
				if err := os.WriteFile(filepath.Join(root, "grok/brigade/unexpected"), nil, 0600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink("README.md", filepath.Join(root, "grok/brigade/link")); err != nil {
					t.Fatal(err)
				}
			case "wrong hook host":
				replace("grok/brigade/hooks/hooks.json", "--host grok", "--host codex")
			case "wrong catalog":
				replace(".grok-plugin/marketplace.json", "./grok/brigade", "./codex/brigade")
			case "long end timeout":
				replace("grok/brigade/hooks/hooks.json", `"timeout": 1`, `"timeout": 3`)
			case "mcp manifest":
				replace("grok/brigade/.grok-plugin/plugin.json", `"name": "brigade",`, `"name": "brigade", "mcpServers": {},`)
			}
			if err := checkGrokSource(root); err == nil {
				t.Fatal("accepted package drift")
			}
		})
	}
}
