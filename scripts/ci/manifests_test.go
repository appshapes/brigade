// The manifest gate. `scripts/ci/plugin-check.sh` is textual by design (no jq on every host) and
// `claude plugin validate` never runs in CI, because CI has no `claude` binary — so nothing else parses
// plugin/.claude-plugin/plugin.json, plugin/hooks/hooks.json and .claude-plugin/marketplace.json as JSON and
// asserts what is IN them. This file does, against the real committed tree, and then proves each assertion can
// fail by re-running it against a mutated copy under t.TempDir().
package ci_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// The four files under test, relative to the repository root.
const (
	pluginManifestRel = "plugin/.claude-plugin/plugin.json"
	hooksManifestRel  = "plugin/hooks/hooks.json"
	marketplaceRel    = ".claude-plugin/marketplace.json"
	versionPinRel     = "plugin/bin/VERSION"
	skillsDirRel      = "plugin/skills"
	pluginDirRel      = "plugin"
	licenseRel        = "LICENSE"
)

// versionLine is the Go spelling of the anchored sed the three shell readers use to pull the manifest's
// version — scripts/ci/plugin-check.sh, scripts/ci/checksums-check.sh and .github/workflows/release.yml all
// run `sed -nE 's/^[[:space:]]*"version":[[:space:]]*"([^"]+)".*/\1/p'`, and scripts/release-prep.sh REWRITES
// that line in place. A minified manifest reads as "declares no version" to all four, so the on-its-own-line
// rule is a real constraint and this pattern is what enforces it here.
var versionLine = regexp.MustCompile(`(?m)^[[:blank:]]*"version":[[:blank:]]*"([^"]+)"`)

// userConfigOptions is the exact option set of plan 6.1 plus P5-12's two frame options. Adding a tenth option or
// dropping one is a change to the plugin's public configuration surface and must be a deliberate edit here too.
var userConfigOptions = []string{
	"adapter_command",
	"config_dir",
	"frame",
	"frame_file",
	"poll_on_prompt",
	"share_workspace_label",
	"team_inbound",
	"workspace_label",
}

// hookEvents maps each lifecycle event to the second word of its `args` array (plan 6.3).
var hookEvents = map[string]string{
	"SessionEnd":       "session-end",
	"SessionStart":     "session-start",
	"UserPromptSubmit": "prompt",
}

// skillDirs is the exact set of skills the plugin ships (plan 6.9).
var skillDirs = []string{"setup", "team-messaging"}

// skillFrontmatterKeys is the frontmatter surface Claude Code 2.1.259 recognises in a SKILL.md (brief section 3,
// docs re-read 2026-09-03). `claude plugin validate --strict` rejects an unrecognised top-level field in
// plugin.json but NOT in a skill's frontmatter -- measured: a `not-a-real-field: nonsense` line in
// plugin/skills/setup/SKILL.md leaves the strict JSON report `"success": true, "contents": []`. So a typo
// (`allowed_tools`, `userInvocable`, `when-to-use`) would be silently ignored at load time, and the grant or the
// intent it was meant to express would simply not exist. Nothing else in the repository would notice; this does.
var skillFrontmatterKeys = []string{
	"agent", "allowed-tools", "argument-hint", "arguments", "background", "compatibility", "context",
	"description", "disable-model-invocation", "disallowed-tools", "effort", "hooks", "license", "metadata",
	"model", "name", "paths", "shell", "user-invocable", "when_to_use",
}

// topLevelFrontmatterKey matches a frontmatter line that opens a top-level key. Continuation lines of a folded
// block (`description: >-`) are indented, so they never match.
var topLevelFrontmatterKey = regexp.MustCompile(`^([A-Za-z0-9_.-]+):`)

// forbiddenPluginText are strings that must never reach a user's machine inside plugin/:
// `service_role` is scripts/ci/no-secrets.sh's literal-word rule, and the SendMessage sentence is the harness
// preamble the plan quotes and that 2.1.259 does NOT emit (execution log, "Plan corrections from the
// interactive sitting" item 2), so a skill repeating it would teach the model something untrue.
var forbiddenPluginText = []string{"service_role", "SendMessage to the from="}

// forbiddenSkillText are strings that must never reach the MODEL through plugin/skills/: `brigade inbox` is the
// human's side of the hold policy (P5-9; D18: the release path "is not a model tool" — the verb refuses to run
// inside a session and the in-session listing deliberately withholds bodies), so a skill naming it would
// advertise a command to the one reader it is not for. The human-facing README and the option description in
// plugin.json name it on purpose.
var forbiddenSkillText = []string{"brigade inbox"}

// ---------------------------------------------------------------------------------------------------------
// reporting
//
// Every assertion below is written against `reporter` rather than *testing.T so that the SAME code can run
// twice: once against the repository, where a failure is a failure, and once against a deliberately mutated
// copy, where a failure is the expected result and silence is the bug.

type reporter interface {
	Helper()
	Errorf(format string, args ...any)
}

// recorder is the reporter used against a mutated copy: it collects what would have been reported.
type recorder struct{ msgs []string }

func (*recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.msgs = append(r.msgs, fmt.Sprintf(format, args...))
}

// ---------------------------------------------------------------------------------------------------------
// reading

func readText(r reporter, root, rel string) (string, bool) {
	r.Helper()
	data, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		r.Errorf("reading %s: %v", rel, err)
		return "", false
	}
	return string(data), true
}

func readJSONObject(r reporter, root, rel string) (map[string]any, bool) {
	r.Helper()
	text, ok := readText(r, root, rel)
	if !ok {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		r.Errorf("%s is not a JSON object: %v", rel, err)
		return nil, false
	}
	return m, true
}

func stringField(r reporter, rel string, m map[string]any, key string) (string, bool) {
	r.Helper()
	v, present := m[key]
	if !present {
		r.Errorf("%s declares no %q", rel, key)
		return "", false
	}
	s, ok := v.(string)
	if !ok {
		r.Errorf("%s: %q is %T, want a string", rel, key, v)
		return "", false
	}
	return s, true
}

// ---------------------------------------------------------------------------------------------------------
// (a) all three manifests are valid JSON

func checkJSONWellFormed(r reporter, root string) {
	r.Helper()
	for _, rel := range []string{pluginManifestRel, hooksManifestRel, marketplaceRel} {
		readJSONObject(r, root, rel)
	}
}

// ---------------------------------------------------------------------------------------------------------
// (b) the plugin's identity and its version pin

func checkPluginIdentity(r reporter, root string) {
	r.Helper()
	m, ok := readJSONObject(r, root, pluginManifestRel)
	if !ok {
		return
	}
	if name, ok := stringField(r, pluginManifestRel, m, "name"); ok && name != "brigade" {
		r.Errorf("%s: name is %q, want %q", pluginManifestRel, name, "brigade")
	}

	pin, ok := readText(r, root, versionPinRel)
	if !ok {
		return
	}
	pin = strings.TrimRight(pin, "\n")
	if pin == "" || strings.Contains(pin, "\n") {
		r.Errorf("%s must be exactly one non-empty line, got %q", versionPinRel, pin)
		return
	}
	manifestVersion, ok := stringField(r, pluginManifestRel, m, "version")
	if !ok {
		return
	}
	if manifestVersion != pin {
		r.Errorf("%s version %q != %s %q", pluginManifestRel, manifestVersion, versionPinRel, pin)
	}

	// The on-its-own-line rule: what the three shell readers see must be the manifest's own version, and it
	// must be the FIRST match so a nested "version" could never be read instead.
	text, ok := readText(r, root, pluginManifestRel)
	if !ok {
		return
	}
	match := versionLine.FindStringSubmatch(text)
	if match == nil {
		r.Errorf("%s has no line matching %s: the shell readers would report \"declares no version\"",
			pluginManifestRel, versionLine)
		return
	}
	if match[1] != manifestVersion {
		r.Errorf("%s: the first anchored \"version\" line reads %q but the parsed manifest version is %q",
			pluginManifestRel, match[1], manifestVersion)
	}
}

// ---------------------------------------------------------------------------------------------------------
// (c) keys the manifest must NOT carry

func checkPluginForbiddenKeys(r reporter, root string) {
	r.Helper()
	m, ok := readJSONObject(r, root, pluginManifestRel)
	if !ok {
		return
	}
	// hooks: hooks/hooks.json is auto-discovered, so a path field would be a second source of truth.
	// mcpServers and channels: D34, and scripts/ci/plugin-check.sh check 4 fails on either anywhere under
	// plugin/. commands: the plugin ships none.
	//
	// `license` used to be forbidden here, for the reason that the repository shipped no LICENSE file and a
	// manifest must not claim one. The repository now ships one (plan 6.1 always specified `"license": "MIT"`),
	// so the rule inverts rather than disappears: checkLicense below requires the manifest's claim and the
	// shipped file to agree.
	for _, key := range []string{"hooks", "mcpServers", "channels", "commands"} {
		if _, present := m[key]; present {
			r.Errorf("%s must not declare %q", pluginManifestRel, key)
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// (c2) the licence the manifest claims is the licence the repository ships

// checkLicense keeps the manifest's claim and the shipped file in step, in both directions: a manifest that
// claims a licence the repository does not ship is the failure the old forbidden-key rule guarded against, and
// a repository that ships one while the manifest stays silent is the same defect facing the other way.
func checkLicense(r reporter, root string) {
	r.Helper()
	text, ok := readText(r, root, licenseRel)
	if !ok {
		return
	}
	for _, want := range []string{"MIT License", `THE SOFTWARE IS PROVIDED "AS IS"`, "Copyright (c)"} {
		if !strings.Contains(text, want) {
			r.Errorf("%s does not read as the MIT licence: no %q", licenseRel, want)
		}
	}
	m, ok := readJSONObject(r, root, pluginManifestRel)
	if !ok {
		return
	}
	got, present := m["license"]
	if !present {
		r.Errorf("%s declares no license, but the repository ships %s", pluginManifestRel, licenseRel)
		return
	}
	if got != "MIT" {
		r.Errorf("%s declares license %v, but %s is the MIT licence", pluginManifestRel, got, licenseRel)
	}
}

// ---------------------------------------------------------------------------------------------------------
// (d) userConfig: exactly nine options, each with exactly four fields

func checkUserConfig(r reporter, root string) {
	r.Helper()
	m, ok := readJSONObject(r, root, pluginManifestRel)
	if !ok {
		return
	}
	raw, present := m["userConfig"]
	if !present {
		r.Errorf("%s declares no userConfig", pluginManifestRel)
		return
	}
	uc, ok := raw.(map[string]any)
	if !ok {
		r.Errorf("%s: userConfig is %T, want an object", pluginManifestRel, raw)
		return
	}
	if got := slices.Sorted(maps.Keys(uc)); !slices.Equal(got, userConfigOptions) {
		r.Errorf("%s: userConfig options are %v, want exactly %v", pluginManifestRel, got, userConfigOptions)
	}
	for _, name := range slices.Sorted(maps.Keys(uc)) {
		opt, ok := uc[name].(map[string]any)
		if !ok {
			r.Errorf("%s: userConfig.%s is %T, want an object", pluginManifestRel, name, uc[name])
			continue
		}
		// Exactly these four fields: no enum, no sensitive, no required, no multiple. The hook validates
		// values (P3-4), and a field the manifest does not declare cannot drift from what the hook enforces.
		if got := slices.Sorted(maps.Keys(opt)); !slices.Equal(got, []string{"default", "description", "title", "type"}) {
			r.Errorf("%s: userConfig.%s has fields %v, want exactly [default description title type]",
				pluginManifestRel, name, got)
			continue
		}
		for _, field := range []string{"title", "description"} {
			if s, ok := opt[field].(string); !ok || s == "" {
				r.Errorf("%s: userConfig.%s.%s is %#v, want a non-empty string", pluginManifestRel, name, field, opt[field])
			}
		}
		typ, _ := opt["type"].(string)
		switch typ {
		case "string":
			if _, ok := opt["default"].(string); !ok {
				r.Errorf("%s: userConfig.%s is type string but its default is %#v", pluginManifestRel, name, opt["default"])
			}
		case "boolean":
			if _, ok := opt["default"].(bool); !ok {
				r.Errorf("%s: userConfig.%s is type boolean but its default is %#v", pluginManifestRel, name, opt["default"])
			}
		default:
			// config_dir stays a string, not a directory: the docs' directory type may validate existence
			// and open a picker, and the hook resolves the value itself.
			r.Errorf("%s: userConfig.%s type is %#v, want \"string\" or \"boolean\"", pluginManifestRel, name, opt["type"])
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// (e) hooks.json: three events, exec form, no matcher

// firstGroup returns the single group object of one event, or nil when the shape is not one group.
func firstGroup(hooks map[string]any, event string) map[string]any {
	groups, ok := hooks[event].([]any)
	if !ok || len(groups) != 1 {
		return nil
	}
	group, _ := groups[0].(map[string]any)
	return group
}

// firstHook returns the single hook object of one event, or nil when the shape is not one hook in one group.
func firstHook(hooks map[string]any, event string) map[string]any {
	group := firstGroup(hooks, event)
	if group == nil {
		return nil
	}
	inner, ok := group["hooks"].([]any)
	if !ok || len(inner) != 1 {
		return nil
	}
	hook, _ := inner[0].(map[string]any)
	return hook
}

func checkHooks(r reporter, root string) {
	r.Helper()
	m, ok := readJSONObject(r, root, hooksManifestRel)
	if !ok {
		return
	}
	hooks, ok := m["hooks"].(map[string]any)
	if !ok {
		r.Errorf("%s: hooks is %T, want an object", hooksManifestRel, m["hooks"])
		return
	}
	if got, want := slices.Sorted(maps.Keys(hooks)), slices.Sorted(maps.Keys(hookEvents)); !slices.Equal(got, want) {
		r.Errorf("%s: events are %v, want exactly %v", hooksManifestRel, got, want)
	}
	for _, event := range slices.Sorted(maps.Keys(hookEvents)) {
		group := firstGroup(hooks, event)
		if group == nil {
			r.Errorf("%s: %s must be exactly one group object", hooksManifestRel, event)
			continue
		}
		// No matcher on any of the three: every SessionStart source goes through the hook (compact is a
		// no-op inside it) and every SessionEnd reason does too.
		if _, present := group["matcher"]; present {
			r.Errorf("%s: %s declares a matcher; all sources and reasons must reach the hook", hooksManifestRel, event)
		}
		hook := firstHook(hooks, event)
		if hook == nil {
			r.Errorf("%s: %s must contain exactly one hook object", hooksManifestRel, event)
			continue
		}
		if _, present := hook["matcher"]; present {
			r.Errorf("%s: the %s hook declares a matcher", hooksManifestRel, event)
		}
		if typ, _ := hook["type"].(string); typ != "command" {
			r.Errorf("%s: the %s hook type is %#v, want \"command\"", hooksManifestRel, event, hook["type"])
		}
		const bootstrap = "${CLAUDE_PLUGIN_ROOT}/bin/brigade"
		if cmd, _ := hook["command"].(string); cmd != bootstrap {
			r.Errorf("%s: the %s hook command is %#v, want %q", hooksManifestRel, event, hook["command"], bootstrap)
		}
		wantArgs := []any{"hook", hookEvents[event]}
		if args, _ := hook["args"].([]any); !slices.Equal(args, wantArgs) {
			r.Errorf("%s: the %s hook args are %#v, want %#v", hooksManifestRel, event, hook["args"], wantArgs)
		}
		if timeout, ok := hook["timeout"].(float64); !ok || timeout <= 0 {
			r.Errorf("%s: the %s hook timeout is %#v, want a positive number of seconds",
				hooksManifestRel, event, hook["timeout"])
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// (f) the marketplace entry

func checkMarketplace(r reporter, root string) {
	r.Helper()
	m, ok := readJSONObject(r, root, marketplaceRel)
	if !ok {
		return
	}
	if desc, ok := stringField(r, marketplaceRel, m, "description"); ok && strings.TrimSpace(desc) == "" {
		r.Errorf("%s: description is blank; `claude plugin validate .` warns without one", marketplaceRel)
	}
	plugins, ok := m["plugins"].([]any)
	if !ok || len(plugins) != 1 {
		r.Errorf("%s: plugins is %#v, want exactly one entry", marketplaceRel, m["plugins"])
		return
	}
	entry, ok := plugins[0].(map[string]any)
	if !ok {
		r.Errorf("%s: plugins[0] is %T, want an object", marketplaceRel, plugins[0])
		return
	}
	if name, ok := stringField(r, marketplaceRel, entry, "name"); ok && name != "brigade" {
		r.Errorf("%s: plugins[0].name is %q, want %q", marketplaceRel, name, "brigade")
	}
	if source, ok := stringField(r, marketplaceRel, entry, "source"); ok && source != "./plugin" {
		r.Errorf("%s: plugins[0].source is %q, want %q", marketplaceRel, source, "./plugin")
	}
	// No version on the entry: the validator checks it against plugin.json, and `make release` bumps only
	// plugin.json and plugin/bin/VERSION, so a marketplace version would drift on the first release.
	if _, present := entry["version"]; present {
		r.Errorf("%s: plugins[0] must not carry a version; `make release` bumps only %s and %s",
			marketplaceRel, pluginManifestRel, versionPinRel)
	}
}

// ---------------------------------------------------------------------------------------------------------
// (g) the two skills

// frontmatter returns the lines between the opening and closing `---` of a SKILL.md. Claude Code reads the
// frontmatter only when `---` is line 1, so that is asserted rather than tolerated.
func frontmatter(r reporter, root, rel string) ([]string, bool) {
	r.Helper()
	text, ok := readText(r, root, rel)
	if !ok {
		return nil, false
	}
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || lines[0] != "---" {
		r.Errorf("%s: line 1 must be exactly \"---\" or the frontmatter is not read", rel)
		return nil, false
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == "---" {
			return lines[1:i], true
		}
	}
	r.Errorf("%s: the frontmatter is never closed with \"---\"", rel)
	return nil, false
}

// frontmatterValue returns the value of a top-level frontmatter key, and whether the key is present.
func frontmatterValue(lines []string, key string) (string, bool) {
	for _, line := range lines {
		if rest, found := strings.CutPrefix(line, key+":"); found {
			return strings.TrimSpace(rest), true
		}
	}
	return "", false
}

func checkSkills(r reporter, root string) {
	r.Helper()
	entries, err := os.ReadDir(filepath.Join(root, skillsDirRel))
	if err != nil {
		r.Errorf("reading %s: %v", skillsDirRel, err)
		return
	}
	var found []string
	for _, e := range entries {
		if !e.IsDir() {
			r.Errorf("%s/%s is not a directory; skills are %s/<name>/SKILL.md", skillsDirRel, e.Name(), skillsDirRel)
			continue
		}
		found = append(found, e.Name())
	}
	slices.Sort(found)
	if !slices.Equal(found, skillDirs) {
		r.Errorf("%s holds %v, want exactly %v", skillsDirRel, found, skillDirs)
	}
	for _, name := range found {
		rel := filepath.Join(skillsDirRel, name, "SKILL.md")
		lines, ok := frontmatter(r, root, rel)
		if !ok {
			continue
		}
		if got, present := frontmatterValue(lines, "name"); !present || got != name {
			r.Errorf("%s: frontmatter name is %q (present=%v), want the directory name %q", rel, got, present, name)
		}
		if _, present := frontmatterValue(lines, "description"); !present {
			r.Errorf("%s: frontmatter declares no description; the listing shows nothing", rel)
		}
		for _, line := range lines {
			m := topLevelFrontmatterKey.FindStringSubmatch(line)
			if m == nil || slices.Contains(skillFrontmatterKeys, m[1]) {
				continue
			}
			r.Errorf("%s: frontmatter declares %q, which Claude Code does not recognise; it is read as nothing",
				rel, m[1])
		}
		got, present := frontmatterValue(lines, "allowed-tools")
		switch name {
		case "team-messaging":
			// The declaration is what buys the model an unprompted `brigade` call for the turn that
			// invoked the skill (D20), and only a pattern that MATCHES buys the Bash silence (E0-8).
			if !present || got != "Bash(brigade:*)" {
				r.Errorf("%s: allowed-tools is %q (present=%v), want %q", rel, got, present, "Bash(brigade:*)")
			}
		default:
			// The setup skill runs nothing: it prints commands for a human's own terminal, so declaring
			// allowed-tools would raise a Skill dialog and grant a tool it never uses.
			if present {
				r.Errorf("%s: declares allowed-tools %q; this skill runs no tool", rel, got)
			}
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// (h) strings that must not appear anywhere under plugin/

func checkNoForbiddenText(r reporter, root string) {
	r.Helper()
	dir := filepath.Join(root, pluginDirRel)
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		//nolint:gosec // G122: the walk is over plugin/ under a root this test chose (the repository, or its
		// own t.TempDir() copy); there is no concurrent writer to win a symlink race with.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		for _, bad := range forbiddenPluginText {
			if bytes.Contains(data, []byte(bad)) {
				r.Errorf("%s contains the forbidden string %q", rel, bad)
			}
		}
		if strings.HasPrefix(filepath.ToSlash(rel), skillsDirRel+"/") {
			for _, bad := range forbiddenSkillText {
				if bytes.Contains(data, []byte(bad)) {
					r.Errorf("%s contains the forbidden string %q", rel, bad)
				}
			}
		}
		return nil
	})
	if err != nil {
		r.Errorf("walking %s: %v", pluginDirRel, err)
	}
}

// ---------------------------------------------------------------------------------------------------------
// the check table, and the run against the real tree

type manifestCheck struct {
	name string
	fn   func(r reporter, root string)
}

var manifestChecks = []manifestCheck{
	{"a_valid_json", checkJSONWellFormed},
	{"b_name_and_version_pin", checkPluginIdentity},
	{"c_no_forbidden_manifest_keys", checkPluginForbiddenKeys},
	{"c2_license_claim_matches_the_shipped_file", checkLicense},
	{"d_user_config", checkUserConfig},
	{"e_hooks", checkHooks},
	{"f_marketplace", checkMarketplace},
	{"g_skills", checkSkills},
	{"h_no_forbidden_text", checkNoForbiddenText},
}

// checkManifests runs every manifest assertion against the plugin tree rooted at root, one subtest each.
func checkManifests(t *testing.T, root string) {
	t.Helper()
	for _, c := range manifestChecks {
		t.Run(c.name, func(t *testing.T) { c.fn(t, root) })
	}
}

func TestManifests(t *testing.T) {
	checkManifests(t, testutil.RepoRoot(t))
}

// ---------------------------------------------------------------------------------------------------------
// mutations: every assertion above, shown failing
//
// A check that has never been seen to fail is not a check. Each case below copies the four real files into
// t.TempDir(), makes ONE change, and requires the matching check to report at least once.

// copyManifestTree copies plugin/ and .claude-plugin/marketplace.json into a fresh temporary root.
func copyManifestTree(t *testing.T) string {
	t.Helper()
	src := testutil.RepoRoot(t)
	dst := t.TempDir()
	for _, rel := range []string{pluginDirRel, filepath.Dir(marketplaceRel)} {
		from := filepath.Join(src, rel)
		err := filepath.WalkDir(from, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			r, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, r)
			if d.IsDir() {
				return os.MkdirAll(target, 0o700)
			}
			//nolint:gosec // G122: same walk, same reason -- the source is the repository's own tree.
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			//nolint:gosec // G703: target is filepath.Join(t.TempDir(), <path relative to the repository root>).
			return os.WriteFile(target, data, 0o600)
		})
		if err != nil {
			t.Fatalf("copying %s: %v", rel, err)
		}
	}
	// The licence lives at the repository root, not under plugin/, so the directory walk above does not reach
	// it and checkLicense would fail on an unmutated copy.
	//nolint:gosec // G304: src is the repository root.
	if data, err := os.ReadFile(filepath.Join(src, licenseRel)); err == nil {
		//nolint:gosec // G703: target is filepath.Join(t.TempDir(), a constant).
		if err := os.WriteFile(filepath.Join(dst, licenseRel), data, 0o600); err != nil {
			t.Fatalf("copying %s: %v", licenseRel, err)
		}
	}
	return dst
}

// editJSON rewrites one manifest of the copy. indent keeps one key per line, which is what the anchored
// "version" readers depend on; compact is used by the mutation that deliberately minifies the manifest.
func editJSON(t *testing.T, path string, indent bool, edit func(m map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	edit(m)
	var out []byte
	if indent {
		out, err = json.MarshalIndent(m, "", "  ")
	} else {
		out, err = json.Marshal(m)
	}
	if err != nil {
		t.Fatalf("re-encoding %s: %v", path, err)
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}

type mutation func(t *testing.T, root string)

func editManifest(rel string, indent bool, edit func(m map[string]any)) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		editJSON(t, filepath.Join(root, rel), indent, edit)
	}
}

func editHookMap(edit func(hooks map[string]any)) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		editJSON(t, filepath.Join(root, hooksManifestRel), true, func(m map[string]any) {
			hooks, ok := m["hooks"].(map[string]any)
			if !ok {
				t.Fatalf("%s: hooks is not an object; the copy is wrong, not the mutation", hooksManifestRel)
			}
			edit(hooks)
		})
	}
}

func editUserConfig(edit func(uc map[string]any)) mutation {
	return editManifest(pluginManifestRel, true, func(m map[string]any) {
		if uc, ok := m["userConfig"].(map[string]any); ok {
			edit(uc)
		}
	})
}

func truncateJSON(rel string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		//nolint:gosec // G703: path is filepath.Join(t.TempDir(), <constant from the table below>).
		if err := os.WriteFile(path, append(data, '{'), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

func appendToFile(rel, text string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		//nolint:gosec // G703: path is filepath.Join(t.TempDir(), <constant from the table below>).
		if err := os.WriteFile(path, append(data, text...), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// writeFile replaces a file wholesale. Used to swap the licence text for another licence.
func writeFile(rel, text string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		//nolint:gosec // G703: path is filepath.Join(t.TempDir(), <constant from the table below>).
		if err := os.WriteFile(filepath.Join(root, rel), []byte(text), 0o600); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
}

// replaceInFile swaps one exact substring, failing loudly when the anchor is gone so a mutation can never
// silently apply to nothing.
func replaceInFile(rel, old, replacement string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		//nolint:gosec // G304: path is filepath.Join(t.TempDir(), <constant from the table below>).
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		if !strings.Contains(string(data), old) {
			t.Fatalf("%s no longer contains the anchor %q, so this mutation would be vacuous", rel, old)
		}
		//nolint:gosec // G703: as above.
		if err := os.WriteFile(path, []byte(strings.Replace(string(data), old, replacement, 1)), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

// insertFrontmatterLine adds one frontmatter line immediately after an existing one, failing loudly when the
// anchor line is gone (a mutation that silently applies to nothing would make its subtest vacuous).
func insertFrontmatterLine(rel, after, line string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		out := strings.Replace(string(data), after+"\n", after+"\n"+line+"\n", 1)
		if out == string(data) {
			t.Fatalf("%s: no %q line to anchor the mutation to", path, after)
		}
		//nolint:gosec // G703: path is filepath.Join(t.TempDir(), <constant from the table below>).
		if err := os.WriteFile(path, []byte(out), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

func dropFrontmatterLine(rel, key string) mutation {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		var kept []string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, key+":") {
				continue
			}
			kept = append(kept, line)
		}
		//nolint:gosec // G703: path is filepath.Join(t.TempDir(), <constant from the table below>).
		if err := os.WriteFile(path, []byte(strings.Join(kept, "\n")), 0o600); err != nil {
			t.Fatalf("writing %s: %v", path, err)
		}
	}
}

var manifestMutations = []struct {
	name    string
	check   func(r reporter, root string)
	mutate  mutation
	because string
}{
	{
		"a_plugin_json_is_not_json", checkJSONWellFormed, truncateJSON(pluginManifestRel),
		"a manifest that does not parse must not read as green",
	},
	{
		"a_hooks_json_is_not_json", checkJSONWellFormed, truncateJSON(hooksManifestRel),
		"same for the hooks manifest",
	},
	{
		"a_marketplace_json_is_not_json", checkJSONWellFormed, truncateJSON(marketplaceRel),
		"same for the marketplace manifest",
	},
	{
		"b_plugin_is_renamed", checkPluginIdentity,
		editManifest(pluginManifestRel, true, func(m map[string]any) { m["name"] = "brigade-next" }),
		"the plugin's name is the pluginConfigs key and the skill prefix",
	},
	{
		"b_version_drifts_from_the_pin", checkPluginIdentity,
		editManifest(pluginManifestRel, true, func(m map[string]any) { m["version"] = "9.9.9" }),
		"plugin.json and plugin/bin/VERSION must name the same release",
	},
	{
		"b_manifest_is_minified", checkPluginIdentity,
		editManifest(pluginManifestRel, false, func(map[string]any) {}),
		"a one-line manifest reads as \"declares no version\" to plugin-check, checksums-check and release.yml",
	},
	{
		"c_commands_key_appears", checkPluginForbiddenKeys,
		editManifest(pluginManifestRel, true, func(m map[string]any) { m["commands"] = "./commands" }),
		"the plugin ships no commands; a manifest that claims them would package a second surface",
	},
	{
		"c_mcp_server_appears", checkPluginForbiddenKeys,
		editManifest(pluginManifestRel, true, func(m map[string]any) { m["mcpServers"] = "./mcp.json" }),
		"D34: the plugin is a CLI, never an MCP server",
	},
	{
		"d_option_gains_an_extra_field", checkUserConfig,
		editUserConfig(func(uc map[string]any) {
			if opt, ok := uc["team_inbound"].(map[string]any); ok {
				opt["enum"] = []any{"accept", "refuse"}
			}
		}),
		"the hook validates values; a manifest enum would be a second, drifting source of truth",
	},
	{
		"d_option_is_removed", checkUserConfig,
		editUserConfig(func(uc map[string]any) { delete(uc, "poll_on_prompt") }),
		"the nine options are the plugin's public configuration surface",
	},
	{
		"d_frame_gains_an_enum", checkUserConfig,
		editUserConfig(func(uc map[string]any) {
			if opt, ok := uc["frame"].(map[string]any); ok {
				opt["enum"] = []any{"open", "guarded", "strict"}
			}
		}),
		"P5-12: the hook validates the frame level; a manifest enum would be a second, drifting source of truth",
	},
	{
		"d_frame_is_removed", checkUserConfig,
		editUserConfig(func(uc map[string]any) { delete(uc, "frame") }),
		"P5-12: the frame level is one of the nine options; dropping it must be a deliberate edit here too",
	},
	{
		"d_boolean_option_defaults_to_a_string", checkUserConfig,
		editUserConfig(func(uc map[string]any) {
			if opt, ok := uc["share_workspace_label"].(map[string]any); ok {
				opt["default"] = "false"
			}
		}),
		"a default whose type does not match its declared type is a runtime surprise",
	},
	{
		"e_hook_grows_a_matcher", checkHooks,
		editHookMap(func(hooks map[string]any) {
			if g := firstGroup(hooks, "SessionStart"); g != nil {
				g["matcher"] = "startup"
			}
		}),
		"a matcher would drop the clear, resume and fork sources",
	},
	{
		"e_hook_command_becomes_a_bare_name", checkHooks,
		editHookMap(func(hooks map[string]any) {
			if h := firstHook(hooks, "UserPromptSubmit"); h != nil {
				h["command"] = "brigade"
			}
		}),
		"hooks do not get the plugin's bin/ on their PATH",
	},
	{
		"e_hook_args_word_is_wrong", checkHooks,
		editHookMap(func(hooks map[string]any) {
			if h := firstHook(hooks, "SessionEnd"); h != nil {
				h["args"] = []any{"hook", "close"}
			}
		}),
		"the second word selects the hook subcommand",
	},
	{
		"e_an_event_is_dropped", checkHooks,
		editHookMap(func(hooks map[string]any) { delete(hooks, "SessionEnd") }),
		"all three lifecycle events must be wired",
	},
	{
		"e_hook_timeout_is_removed", checkHooks,
		editHookMap(func(hooks map[string]any) {
			if h := firstHook(hooks, "SessionStart"); h != nil {
				delete(h, "timeout")
			}
		}),
		"the 60 s SessionStart timeout is the first-use download budget",
	},
	{
		"f_marketplace_entry_pins_a_version", checkMarketplace,
		editManifest(marketplaceRel, true, func(m map[string]any) {
			if plugins, ok := m["plugins"].([]any); ok && len(plugins) == 1 {
				if entry, ok := plugins[0].(map[string]any); ok {
					entry["version"] = "0.0.0"
				}
			}
		}),
		"make release bumps only plugin.json and VERSION, so a marketplace version drifts on the first release",
	},
	{
		"f_marketplace_description_is_removed", checkMarketplace,
		editManifest(marketplaceRel, true, func(m map[string]any) { delete(m, "description") }),
		"`claude plugin validate .` warns without one",
	},
	{
		"g_team_messaging_loses_allowed_tools", checkSkills,
		dropFrontmatterLine("plugin/skills/team-messaging/SKILL.md", "allowed-tools"),
		"without the declaration D20's grant does not exist and every reply prompts",
	},
	{
		"g_setup_declares_allowed_tools", checkSkills,
		insertFrontmatterLine("plugin/skills/setup/SKILL.md", "user-invocable: true", "allowed-tools: Bash(brigade:*)"),
		"the setup skill runs nothing, so it must grant nothing",
	},
	{
		"g_skill_name_stops_matching_its_directory", checkSkills,
		func(t *testing.T, root string) {
			t.Helper()
			dropFrontmatterLine("plugin/skills/setup/SKILL.md", "name")(t, root)
		},
		"the frontmatter name is the last segment of /brigade:<name>",
	},
	{
		"g_skill_declares_an_unknown_frontmatter_field", checkSkills,
		insertFrontmatterLine("plugin/skills/setup/SKILL.md", "user-invocable: true", "not-a-real-field: nonsense"),
		"`claude plugin validate --strict` accepts any frontmatter key, so a misspelt one would be silently dropped",
	},
	{
		"g_a_third_skill_appears", checkSkills,
		func(t *testing.T, root string) {
			t.Helper()
			dir := filepath.Join(root, skillsDirRel, "extra")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatalf("creating %s: %v", dir, err)
			}
			body := "---\nname: extra\ndescription: an unreviewed third skill\n---\n\nbody\n"
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
				t.Fatalf("writing the extra skill: %v", err)
			}
		},
		"the shipped skill set is exactly the two of plan 6.9",
	},
	{
		"h_a_plugin_file_names_the_secret_role", checkNoForbiddenText,
		appendToFile("plugin/README.md", "\nUse the project's service_role key.\n"),
		"scripts/ci/no-secrets.sh forbids that literal word anywhere under plugin/",
	},
	{
		"h_a_skill_quotes_the_absent_harness_preamble", checkNoForbiddenText,
		appendToFile("plugin/skills/team-messaging/SKILL.md", "\nThe harness says to reply via SendMessage to the from= address.\n"),
		"2.1.259 emits no such sentence; the skill must not teach the model an untruth",
	},
	{
		"c2_the_manifest_claims_a_licence_the_repository_does_not_ship", checkLicense,
		replaceInFile(pluginManifestRel, `"license": "MIT"`, `"license": "Apache-2.0"`),
		"the manifest's claim and the shipped LICENSE must agree; this is the defect the old forbidden-key rule guarded against",
	},
	{
		"c2_the_manifest_stops_declaring_the_licence_it_ships", checkLicense,
		replaceInFile(pluginManifestRel, "\n  \"license\": \"MIT\",", ""),
		"a repository that ships a licence while its manifest stays silent is the same defect facing the other way",
	},
	{
		"c2_the_licence_file_is_not_the_mit_licence", checkLicense,
		writeFile(licenseRel, "All rights reserved.\n"),
		"the manifest says MIT, so the file has to be the MIT licence and not a placeholder",
	},
	{
		"h_a_skill_names_the_humans_inbox_verb", checkNoForbiddenText,
		appendToFile("plugin/skills/team-messaging/SKILL.md", "\nRun brigade inbox to see held messages.\n"),
		"`brigade inbox` is the human's verb (D18); the skill must not advertise it to the model",
	},
}

// TestManifestChecksPassOnAnUnmutatedCopy is the positive control for every mutation case below: if the copy
// helper itself were broken, each mutation would "fail" for the wrong reason and the whole suite would be
// vacuous.
func TestManifestChecksPassOnAnUnmutatedCopy(t *testing.T) {
	root := copyManifestTree(t)
	for _, c := range manifestChecks {
		var rec recorder
		c.fn(&rec, root)
		if len(rec.msgs) != 0 {
			t.Errorf("%s reported %d problem(s) on an unmutated copy: %s", c.name, len(rec.msgs), strings.Join(rec.msgs, "; "))
		}
	}
}

func TestManifestChecksFailOnAMutatedCopy(t *testing.T) {
	for _, m := range manifestMutations {
		t.Run(m.name, func(t *testing.T) {
			root := copyManifestTree(t)
			m.mutate(t, root)
			var rec recorder
			m.check(&rec, root)
			if len(rec.msgs) == 0 {
				t.Fatalf("the mutation changed nothing the check can see, so the check is vacuous (%s)", m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
