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

// antigravity is Antigravity CLI's package check: antigravity/brigade/
// (work item 532). A member installs one release of the package from its
// tag (`agy plugin install https://github.com/appshapes/brigade/tree/v<version>/antigravity/brigade`),
// so there is no catalog and no pin to hold: the tag is the pin. No host is
// launched.
func (c *checker) antigravity() {
	const dir = "antigravity/brigade"
	if len(c.o.Args) > 0 {
		c.usage("antigravity-plugin-check takes no arguments")
	}
	if err := AntigravitySource(c.o.Root); err != nil {
		c.die("%s", err)
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
	manifest, _ := c.read(dir + "/plugin.json")
	if declaredVersion(manifest) != firstLine(pin) {
		c.die("manifest version differs from release pin")
	}
	if !c.isExecutable(dir + "/bin/brigade") {
		c.die("bootstrap is not executable")
	}
	c.say("antigravity-plugin-check: package source and release assets checked; what a live agy session shows is measured apart from this check")
}

// AntigravitySkills are the fifteen skills Antigravity CLI's package
// carries, by directory: the same as every package's.
var AntigravitySkills = GrokSkills

// antigravityHooks are the three events the package hooks, each with the
// `brigade hook` subcommand it runs. agy runs a plugin's hook in the
// plugin's root, so the command names the bootstrap from there, and has no
// session-end event.
var antigravityHooks = map[string]string{"SessionStart": "session-start", "PreInvocation": "prompt", "Stop": "stop"}

// AntigravitySource holds the source of Antigravity CLI's package under
// root: its files, its skills, its manifest and its three hooks.
func AntigravitySource(root string) error {
	read := func(rel string) ([]byte, error) { return os.ReadFile(filepath.Join(root, rel)) }
	pkg := "antigravity/brigade"
	allowed := map[string]bool{"README.md": true, "plugin.json": true, "hooks.json": true, "bin/brigade": true, "bin/VERSION": true, "bin/checksums.txt": true}
	for _, name := range AntigravitySkills {
		allowed["skills/"+name+"/SKILL.md"] = true
	}
	if err := filepath.WalkDir(filepath.Join(root, pkg), func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&os.ModeSymlink != 0 {
			return errors.New("symlink in Antigravity package")
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
	if !slices.Equal(names, AntigravitySkills) {
		return fmt.Errorf("expected exactly fifteen skills, got %v", names)
	}
	for _, name := range AntigravitySkills {
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
	data, err := read(pkg + "/plugin.json")
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
	data, err = read(pkg + "/hooks.json")
	if err != nil {
		return err
	}
	type handler struct {
		Type    string  `json:"type"`
		Command string  `json:"command"`
		Timeout float64 `json:"timeout"`
	}
	var hooks map[string]map[string]json.RawMessage
	if err := json.Unmarshal(data, &hooks); err != nil {
		return err
	}
	named, ok := hooks["brigade"]
	if len(hooks) != 1 || !ok {
		return errors.New(`expected exactly one named hook, "brigade"`)
	}
	if len(named) != len(antigravityHooks) {
		return errors.New("expected exactly three lifecycle hooks: SessionStart, PreInvocation and Stop")
	}
	for event, sub := range antigravityHooks {
		var handlers []handler
		if err := json.Unmarshal(named[event], &handlers); err != nil || len(handlers) != 1 {
			return fmt.Errorf("invalid %s handlers", event)
		}
		h := handlers[0]
		want := "./bin/brigade hook " + sub + " --host antigravity --plugin-root ."
		// A start registers under the shared start's 55-second budget, so
		// its timeout is above that; the other two stay short.
		limit := 15.0
		if event == "SessionStart" {
			limit = 60
		}
		if h.Type != "command" || h.Command != want || h.Timeout <= 0 || h.Timeout > limit || event == "SessionStart" && h.Timeout < 56 {
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
	return nil
}
