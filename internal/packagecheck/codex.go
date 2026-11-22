package packagecheck

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	codexNameRE     = re(`^_"name":_"brigade"_,?_$`)
	codexSkillsRE   = re(`^_"skills":_"\./skills/?"`)
	codexForeignRE  = re(`"(userConfig|mcpServers|channels)"_:`)
	codexAnyEventRE = re(`^_"(SessionStart|UserPromptSubmit|SessionEnd|PreToolUse|PostToolUse|PermissionRequest|PreCompact|PostCompact|SubagentStart|SubagentStop|Stop|Interrupt)":_\[`)
	codexCommandRE  = re(`"command"_:_"((?:[^"\\\n]|\\.)*)"`)
	codexTypedRE    = re(`"type"_:_"command"`)
	codexFormRE     = regexp.MustCompile(`^"\$\{PLUGIN_ROOT\}/bin/brigade" hook ([a-z-]+) --host codex --plugin-root "\$\{PLUGIN_ROOT\}"$`)
	codexBlockEndRE = regexp.MustCompile(`^` + sp + `*\]` + sp + `*,?` + sp + `*$`)
	codexMarketName = re(`^_"name":_"brigade"`)
	codexMarketPath = re(`"path":_"\./codex/brigade"`)
	nonDigitsRE     = regexp.MustCompile(`[^0-9]`)
	// The name is put in after the blanks are: an underscore in a skill's
	// name is its own and no blank.
	codexSkillNameRE = func(name string) *regexp.Regexp {
		return regexp.MustCompile(`(?m)^name:` + sp + `*` + regexp.QuoteMeta(name) + sp + `*$`)
	}
)

// codex is the Codex CLI package's check: codex/brigade/ and the
// marketplace file (card 107).
func (c *checker) codex() {
	const (
		dir         = "codex/brigade"
		manifest    = "codex/brigade/.codex-plugin/plugin.json"
		hooksFile   = "codex/brigade/hooks/hooks.json"
		marketplace = ".agents/plugins/marketplace.json"
	)
	if !c.isDir(dir) {
		c.die("run me from the repository root: no %s directory here", dir)
	}
	text, there := c.read(manifest)
	if !there {
		c.die("missing %s", manifest)
	}
	hooks, hasHooks := c.read(hooksFile)
	if !hasHooks {
		c.die("missing %s", hooksFile)
	}

	// 1. Only allow-listed paths.
	c.allowed(dir, func(rel string) bool {
		return oneOf(rel, "bin/brigade", "bin/VERSION", "bin/checksums.txt", ".codex-plugin/plugin.json", "hooks/hooks.json", "README.md") ||
			underSkills(rel)
	})
	c.ok("%s/ contains only allow-listed paths", dir)

	// 2. The three pins are plugin/bin's, and the bootstrap an executable.
	c.assets(dir, "three")
	modes := c.gitModes(dir)
	if !tracked100755(modes, dir+"/bin/brigade") {
		c.die("git does not track %s/bin/brigade as 100755", dir)
	}
	c.ok("%s/bin/{brigade,VERSION,checksums.txt} equal plugin/bin/'s and the bootstrap is 100755 in git", dir)

	// 3. The manifest's version is the pin's.
	pin, _ := c.read(dir + "/bin/VERSION")
	version := firstLine(pin)
	declared := declaredVersion(text)
	if declared == "" {
		c.die("%s declares no \"version\"", manifest)
	}
	if declared != version {
		c.die("%s version '%s' != %s/bin/VERSION '%s'", manifest, declared, dir, version)
	}
	c.ok("%s/bin/VERSION (%s) matches %s", dir, version, manifest)

	// 4. A native manifest named brigade, with nothing the package must not carry.
	if !codexNameRE.MatchString(text) {
		c.die("%s is not named brigade", manifest)
	}
	if !codexSkillsRE.MatchString(text) {
		c.die("%s declares no \"skills\": \"./skills/\" member", manifest)
	}
	if codexForeignRE.MatchString(text) || codexForeignRE.MatchString(hooks) {
		c.die("%s or %s carries a member this package must not (userConfig, mcpServers or channels)", manifest, hooksFile)
	}
	c.ok("%s is a native Codex manifest named brigade with a skills directory and none of userConfig, mcpServers, channels", manifest)

	// 5. No symbolic link.
	if links := c.links("codex"); len(links) > 0 {
		c.die("symlink(s) under codex/, which the package must not carry: %s", strings.Join(links, "\n"))
	}
	c.ok("no symlink under codex/")

	// 6. Exactly the three events, each with the exact command form: Codex
	// runs a hook's command through its shell, so the form is pinned whole.
	known := strings.Join(PointedHookSubcommands, " ")
	for _, event := range []string{"SessionStart", "UserPromptSubmit", "SessionEnd"} {
		if n := len(re(`^_"`+event+`":_\[`).FindAllString(hooks, -1)); n != 1 {
			c.die("%s declares the %s event %d time(s), want exactly once", hooksFile, event, n)
		}
	}
	if events := len(codexAnyEventRE.FindAllString(hooks, -1)); events != 3 {
		c.die("%s declares %d events, want exactly 3 (SessionStart, UserPromptSubmit, SessionEnd): a Stop hook must print JSON or nothing, and no other event is Brigade's", hooksFile, events)
	}
	var cmds []string
	for _, m := range codexCommandRE.FindAllStringSubmatch(hooks, -1) {
		cmds = append(cmds, strings.ReplaceAll(m[1], `\"`, `"`))
	}
	if len(cmds) != 3 {
		c.die("%s declares %d hook commands, want exactly 3", hooksFile, len(cmds))
	}
	if types := len(codexTypedRE.FindAllString(hooks, -1)); types != 3 {
		c.die("%s has %d command-typed hooks, want 3", hooksFile, types)
	}
	for _, cmd := range cmds {
		m := codexFormRE.FindStringSubmatch(cmd)
		if m == nil {
			c.die("%s command is not of the exact form \"${PLUGIN_ROOT}/bin/brigade\" hook <subcommand> --host codex --plugin-root \"${PLUGIN_ROOT}\": %s", hooksFile, cmd)
		}
		if !oneOf(m[1], PointedHookSubcommands...) {
			c.die("%s names the hook subcommand '%s', which the binary does not implement (known: %s)", hooksFile, m[1], known)
		}
	}
	lines := strings.Split(hooks, "\n")
	for _, pair := range [][2]string{{"SessionStart", "session-start"}, {"UserPromptSubmit", "prompt"}, {"SessionEnd", "session-end"}} {
		if !strings.Contains(eventBlock(lines, pair[0]), "hook "+pair[1]+" --host codex") {
			c.die("%s: the %s block does not run `hook %s`", hooksFile, pair[0], pair[1])
		}
	}
	endTimeout := ""
	inEnd := false
	for _, line := range lines {
		if strings.Contains(line, `"SessionEnd"`) {
			inEnd = true
		}
		if inEnd && strings.Contains(line, `"timeout"`) {
			endTimeout = nonDigitsRE.ReplaceAllString(line, "")
			break
		}
	}
	if endTimeout == "" {
		c.die("%s: the SessionEnd hook declares no timeout", hooksFile)
	}
	if seconds, err := strconv.Atoi(endTimeout); err != nil || seconds > 3 {
		c.die("%s: the SessionEnd timeout is %s s; Codex caps a SessionEnd hook at 3 s", hooksFile, endTimeout)
	}
	c.ok("%s runs the bootstrap for exactly SessionStart, UserPromptSubmit and SessionEnd with the exact command form, and SessionEnd stays within Codex's 3 s", hooksFile)

	// 7. The marketplace file names the package.
	market, hasMarket := c.read(marketplace)
	if !hasMarket {
		c.die("missing %s: `codex plugin marketplace add appshapes/brigade` reads it", marketplace)
	}
	if !codexMarketName.MatchString(market) {
		c.die("%s names no plugin brigade", marketplace)
	}
	if !codexMarketPath.MatchString(market) {
		c.die("%s does not point at ./codex/brigade", marketplace)
	}
	c.ok("%s names the brigade plugin at ./codex/brigade", marketplace)

	// 8. Every skill is named after its directory, and none takes the
	// model's reach away.
	count := 0
	for _, d := range c.glob(dir + "/skills/*") {
		if !c.isDir(d) {
			continue
		}
		name := d[strings.LastIndex(d, "/")+1:]
		f := d + "/SKILL.md"
		skill, hasSkill := c.read(f)
		if !hasSkill || !c.isFile(f) {
			c.die("skill directory %s/ has no SKILL.md", d)
		}
		if !codexSkillNameRE(name).MatchString(skill) {
			c.die("%s is not named after its directory (%s)", d+"//SKILL.md", name)
		}
		if c.disablesTheModel(f) {
			c.die("disable-model-invocation is declared in %s -- Brigade's skills are reachable by the model (card 28)", d+"//SKILL.md")
		}
		count++
	}
	if count == 0 {
		c.die("%s/skills/ holds no skill", dir)
	}
	c.ok("%d skills, each named after its directory, none declaring disable-model-invocation", count)
	c.say("codex-plugin-check: all checks passed")
}

// eventBlock is the lines of hooks.json from the one that names the event
// to the first that closes an array, as the script's awk read them.
func eventBlock(lines []string, event string) string {
	var block []string
	in := false
	for _, line := range lines {
		if strings.Contains(line, `"`+event+`"`) {
			in = true
		}
		if in {
			block = append(block, line)
			if codexBlockEndRE.MatchString(line) {
				break
			}
		}
	}
	return strings.Join(block, "\n")
}
