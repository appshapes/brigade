package packagecheck

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
)

// grok is Grok Build's package check: grok/brigade/ (cards 132, 135). With
// `--source` it checks the source alone, which an unreleased package has;
// by default it also holds the three pins to plugin/bin's. No host is
// launched either way.
func (c *checker) grok() {
	const dir = "grok/brigade"
	sourceOnly := false
	switch {
	case len(c.o.Args) > 1:
		c.usage("too many arguments")
	case len(c.o.Args) == 1 && c.o.Args[0] == "--source":
		sourceOnly = true
	case len(c.o.Args) == 1:
		c.usage("usage: grok-plugin-check [--source]")
	}
	if err := GrokSource(c.o.Root); err != nil {
		c.die("%s", err)
	}
	if sourceOnly {
		c.say("grok-plugin-check: source checked; what a live Grok Build session shows is measured apart from this check")
		return
	}
	for _, asset := range []string{"brigade", "VERSION", "checksums.txt"} {
		mine, canonical := dir+"/bin/"+asset, "plugin/bin/"+asset
		if !c.isFile(mine) {
			c.die("missing release-produced %s; run the normal release sequence", mine)
		}
		a, aerr := os.ReadFile(c.path(canonical))
		b, _ := os.ReadFile(c.path(mine))
		if aerr != nil || !bytes.Equal(a, b) {
			c.die("%s differs from %s", mine, canonical)
		}
	}
	pin, _ := c.read(dir + "/bin/VERSION")
	manifest, _ := c.read(dir + "/.grok-plugin/plugin.json")
	if declaredVersion(manifest) != firstLine(pin) {
		c.die("manifest version differs from release pin")
	}
	if !c.isExecutable(dir + "/bin/brigade") {
		c.die("bootstrap is not executable")
	}
	c.pin()
	c.say("grok-plugin-check: release assets equal canonical pins; live contract gates remain independent")
}

// GrokSkills are the fifteen skills Grok Build's package carries, by
// directory: its check holds the package to exactly these.
var GrokSkills = []string{"email", "join", "move", "quiet", "sessions", "setup", "slack", "subscribe", "team-messaging", "update", "whoami", "work-item", "work-item-next", "work-item-start", "work-items"}

// GrokSource holds the source of Grok Build's package under root: its
// files, its skills, its manifest and its four hooks. Source validation
// deliberately makes no live-host or release-readiness claim; the check's
// strict mode separately requires release-produced pins.
func GrokSource(root string) error {
	read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(root, rel)) }
	pkg := "grok/brigade"
	allowed := map[string]bool{"README.md": true, ".grok-plugin/plugin.json": true, "hooks/hooks.json": true, "bin/brigade": true, "bin/VERSION": true, "bin/checksums.txt": true}
	for _, name := range GrokSkills {
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
	if !slices.Equal(names, GrokSkills) {
		return fmt.Errorf("expected exactly fifteen skills, got %v", names)
	}
	for _, name := range GrokSkills {
		data, err := read(pkg + "/skills/" + name + "/SKILL.md")
		if err != nil {
			return err
		}
		if !strings.Contains(string(data), "\nname: "+name+"\n") || strings.Contains(string(data), "disable-model-invocation:") {
			return fmt.Errorf("unreachable or misnamed skill %s", name)
		}
		if err := DescriptionFits(string(data)); err != nil {
			return fmt.Errorf("%s/skills/%s/SKILL.md: %w", pkg, name, err)
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
	// The catalog names the package at the commit of a release (work item
	// 424, pin.go); that the commit is Codex CLI's too, and a release's, is
	// the whole check's to hold, which has git to ask.
	if _, err := pins[1].commitIn(root); err != nil {
		return fmt.Errorf("catalog does not name the distinct Grok package: %w", err)
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
	if len(hooks.Hooks) != 4 {
		return errors.New("expected exactly four native lifecycle hooks")
	}
	for event, sub := range map[string]string{"SessionStart": "session-start", "UserPromptSubmit": "prompt", "SessionEnd": "session-end", "Stop": "stop"} {
		groups := hooks.Hooks[event]
		if len(groups) != 1 || len(groups[0].Hooks) != 1 {
			return fmt.Errorf("invalid %s handlers", event)
		}
		h := groups[0].Hooks[0]
		want := `exec "${GROK_PLUGIN_ROOT}/bin/brigade" hook ` + sub + ` --host grok --plugin-root "${GROK_PLUGIN_ROOT}"`
		if h.Type != "command" || h.Command != want || h.Timeout <= 0 || (event == "SessionStart" && h.Timeout > 15 || event != "SessionStart" && h.Timeout > 5) || event == "SessionEnd" && h.Timeout > 1 {
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
