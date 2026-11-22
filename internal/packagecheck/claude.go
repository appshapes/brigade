package packagecheck

import (
	"regexp"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// ClaudeHookSubcommands are the `brigade hook` subcommands the Claude Code
// plugin's hooks may name. A test holds them to the hook package's own.
var ClaudeHookSubcommands = []string{"prompt", "session-end", "session-start", "stop"}

var (
	typeKeyRE     = re(`"type"_:`)
	commandKeyRE  = re(`"command"_:_"`)
	typeValueRE   = re(`"type"_:_"[^"]*"`)
	commandRE     = re(`"command"_:_"([^"]*)"`)
	argsValueRE   = regexp.MustCompile(`"args"` + sp + `*:` + sp + `*[^ \t\r\f\v\n]`)
	argsArrayRE   = re(`"args"_:_\[([^\]\n]*)\]`)
	argsOpenRE    = re(`"args"_:_\[`)
	firstQuotedRE = regexp.MustCompile(`^` + sp + `*"([^"]*)"`)
	secondRE      = regexp.MustCompile(`^` + sp + `*"[^"]*"` + sp + `*,` + sp + `*"([^"]*)"`)
	shellCharsRE  = regexp.MustCompile("[ \t;|&<>()`\"']")
	versionTokRE  = regexp.MustCompile(`^[0-9A-Za-z.+-]+$`)
	keysRE        = regexp.MustCompile(`"mcpServers"|"channels"`)
)

// claude is the Claude Code plugin's check: plugin/. Its checks keep the
// numbers the script gave them, which the rule files, docs/security.md and
// the tests name them by ("check 9", "check 10"): 1 the allow-list, 2 the
// bootstrap's mode in git, 3 the version, 4 no MCP server and no channel,
// 5 exec-form hooks whose commands exist, 6 hook subcommands the binary
// has, 8 the shells the bootstrap parses under, 9 shellcheck, 10 no skill
// that declares disable-model-invocation. There is no 7: it was retired on
// 2026-09-08 (a README's wording is not CI's business).
func (c *checker) claude() {
	const (
		dir        = "plugin"
		pluginJSON = "plugin/.claude-plugin/plugin.json"
		hooksJSON  = "plugin/hooks/hooks.json"
		rootVar    = "${CLAUDE_PLUGIN_ROOT}"
	)
	if !c.isDir(dir) {
		c.die("run me from the repository root: no plugin/ directory here")
	}

	// 1. Only allow-listed paths: no .mcp.json, nothing a build left.
	c.allowed(dir, func(rel string) bool {
		// brigade.exe: the Windows launcher, card 133 (make launcher-check).
		return oneOf(rel, "bin/brigade", "bin/brigade.exe", "bin/VERSION", "bin/checksums.txt",
			".claude-plugin/plugin.json", ".claude-plugin/icon.png", "hooks/hooks.json", "README.md") || underSkills(rel)
	})
	c.ok("plugin/ contains only allow-listed paths")

	// 2. The bootstrap is an executable in git, whatever the checkout's mode.
	modes := c.gitModes(dir)
	for _, line := range strings.Split(modes, "\n") {
		mode, _, _ := strings.Cut(line, " ")
		if _, path, found := strings.Cut(line, "\t"); found && path == "plugin/bin/brigade" && mode != "100755" {
			c.die("plugin/bin/brigade is %s in the index, must be 100755 (git update-index --add --chmod=+x plugin/bin/brigade)", mode)
		}
	}
	if !tracked100755(modes, "plugin/bin/brigade") {
		c.die("git does not track plugin/bin/brigade as 100755")
	}
	c.ok("plugin/bin/brigade is 100755 in git")

	// 3. VERSION is one token, and the manifest's.
	text, readable := c.read("plugin/bin/VERSION")
	if !readable {
		c.die("missing plugin/bin/VERSION")
	}
	if lineCount(text) != 1 {
		c.die("plugin/bin/VERSION must be exactly one line")
	}
	version := firstLine(text)
	if !versionTokRE.MatchString(version) {
		c.die("plugin/bin/VERSION is not a version token: '%s'", version)
	}
	if manifest, there := c.read(pluginJSON); there {
		declared := declaredVersion(manifest)
		if declared == "" {
			c.die("%s declares no \"version\"", pluginJSON)
		}
		if declared != version {
			c.die("%s version '%s' != plugin/bin/VERSION '%s'", pluginJSON, declared, version)
		}
		c.ok("plugin/bin/VERSION (%s) matches %s", version, pluginJSON)
	} else {
		c.ok("plugin/bin/VERSION is one token (%s)", version)
		c.skip("VERSION == plugin.json: %s does not exist yet", pluginJSON)
	}

	// 4. No MCP server and no channel wiring, anywhere in the package.
	c.nowhere(dir, func(line string) bool { return keysRE.MatchString(line) },
		"an mcpServers or channels key appears under plugin/")
	c.nowhere(dir, func(line string) bool {
		return strings.Contains(line, "--channels") || strings.Contains(line, "claude/channel")
	},
		"'--channels' or 'claude/channel' appears under plugin/")
	c.ok("no mcpServers, no channels and no --channels anywhere under plugin/ (.mcp.json: check 1)")

	hooks, hasHooks := c.read(hooksJSON)

	// 5. Every hook is exec form, and its command is there and executable.
	if hasHooks {
		types, cmds := len(typeKeyRE.FindAllString(hooks, -1)), len(commandKeyRE.FindAllString(hooks, -1))
		if types == 0 {
			c.die("%s declares no hook \"type\"", hooksJSON)
		}
		if types != cmds {
			c.die("%s has %d \"type\" keys but %d \"command\" strings: every hook must be exec form", hooksJSON, types, cmds)
		}
		bad := 0
		for _, m := range typeValueRE.FindAllString(hooks, -1) {
			if !strings.HasSuffix(m, `"command"`) {
				bad++
			}
		}
		if bad != 0 {
			c.die("%s has %d hook entries whose \"type\" is not \"command\"", hooksJSON, bad)
		}
		for _, m := range commandRE.FindAllStringSubmatch(hooks, -1) {
			cmd := m[1]
			if cmd == "" {
				c.die("%s has an empty \"command\"", hooksJSON)
			}
			resolved := cmd
			if rest, rooted := strings.CutPrefix(cmd, rootVar); rooted {
				if !strings.HasPrefix(rest, "/") {
					c.die("%s command '%s' must continue with / after %s", hooksJSON, cmd, rootVar)
				}
				resolved = dir + rest
			} else if !strings.HasPrefix(cmd, "/") {
				c.die("%s command '%s' is neither absolute nor %s-rooted", hooksJSON, cmd, rootVar)
			}
			if shellCharsRE.MatchString(cmd) {
				c.die("%s command '%s' is a shell string, not exec form: put arguments in \"args\"", hooksJSON, cmd)
			}
			if !c.hookCommandIsFile(resolved) {
				c.die("%s names a hook command that does not exist: %s -> %s", hooksJSON, cmd, resolved)
			}
			if !c.hookCommandIsExecutable(resolved) {
				c.die("%s names a hook command that is not executable: %s -> %s", hooksJSON, cmd, resolved)
			}
		}
		for _, m := range argsValueRE.FindAllString(hooks, -1) {
			if !strings.HasSuffix(m, "[") {
				c.die("%s has an \"args\" value that is not a JSON array", hooksJSON)
			}
		}
		c.ok("%s is exec form and every hook command exists and is executable", hooksJSON)
	} else {
		c.skip("hooks.json exec form and command existence: %s does not exist yet", hooksJSON)
	}

	// 6. Every hook names a subcommand the binary has.
	known := strings.Join(ClaudeHookSubcommands, " ")
	if hasHooks {
		for _, m := range argsArrayRE.FindAllStringSubmatch(hooks, -1) {
			group, sub := "", ""
			if g := firstQuotedRE.FindStringSubmatch(m[1]); g != nil {
				group = g[1]
			}
			if s := secondRE.FindStringSubmatch(m[1]); s != nil {
				sub = s[1]
			}
			if group != "hook" {
				c.die("%s has an \"args\" array whose first element is '%s', not \"hook\"", hooksJSON, group)
			}
			if sub == "" {
				c.die("%s has an \"args\" array with no subcommand after \"hook\"", hooksJSON)
			}
			if !oneOf(sub, ClaudeHookSubcommands...) {
				c.die("%s names the hook subcommand '%s', which the binary does not implement (known: %s)", hooksJSON, sub, known)
			}
		}
		if len(argsOpenRE.FindAllString(hooks, -1)) == 0 {
			c.die("%s declares no \"args\" array: no hook names a subcommand", hooksJSON)
		}
		c.ok("every hooks.json \"args\" pair names a brigade hook subcommand that exists (%s)", known)
	} else {
		c.skip("hook subcommand names: %s does not exist yet", hooksJSON)
	}

	// 8. The bootstrap parses under every shell that is here.
	if !c.isFile("plugin/bin/brigade") {
		c.die("missing plugin/bin/brigade")
	}
	checked := c.parses(func(shell, file string) { c.die("%s -n %s failed", shell, file) }, "plugin/bin/brigade")
	c.ok("plugin/bin/brigade parses under: %s", checked)

	// 9. shellcheck, over the bootstrap and every script of the repository.
	scripts := append([]string{"plugin/bin/brigade"}, c.glob("scripts/ci/*.sh")...)
	scripts = append(scripts, c.glob("scripts/*.sh")...)
	switch {
	case c.which("shellcheck") != "":
		out, err := c.command("shellcheck", append([]string{"-s", "sh"}, scripts...)...)
		if err != nil {
			c.warn("%s", strings.TrimRight(out, "\n"))
			c.die("shellcheck reported problems")
		}
		c.ok("shellcheck -s sh clean: %s", strings.Join(scripts, " "))
	case adapterkit.Getenv(c.o.Environ, "CI") != "":
		c.die("shellcheck is not installed and CI is set: the Ubuntu runner preinstalls it, so this is a real failure")
	default:
		c.warn("plugin-check: WARNING: shellcheck is not installed; skipping (CI enforces it)")
		c.ok("shellcheck skipped with a warning (not CI)")
	}

	// 10. No skill takes the model's reach away (card 28): the check the
	// rule files and docs/security.md name by this number.
	if c.isDir("plugin/skills") {
		var disabled []string
		for _, f := range c.glob("plugin/skills/*/SKILL.md") {
			if c.isFile(f) && c.disablesTheModel(f) {
				disabled = append(disabled, " "+f)
			}
		}
		if len(disabled) > 0 {
			c.die("disable-model-invocation is declared in:%s -- Brigade's skills are reachable by the model (card 28). user-invocable keeps the slash command; this flag only takes autonomy away.", strings.Join(disabled, ""))
		}
		c.ok("no skill declares disable-model-invocation")
	} else {
		c.skip("no skill declares disable-model-invocation")
	}
	c.say("plugin-check: all checks passed")
}

// A hook command is a path of the tree when it is rooted in the plugin,
// and a path of the machine when it is absolute.
func (c *checker) hookCommandIsFile(resolved string) bool {
	if strings.HasPrefix(resolved, "/") {
		return isFileAt(resolved)
	}
	return c.isFile(resolved)
}

func (c *checker) hookCommandIsExecutable(resolved string) bool {
	if strings.HasPrefix(resolved, "/") {
		return isExecutableAt(resolved)
	}
	return c.isExecutable(resolved)
}

// nowhere ends the check when a line of any file under dir is one that
// found matches, after printing the lines, as `grep -rn` does.
func (c *checker) nowhere(dir string, found func(line string) bool, refusal string) {
	hit := false
	for _, f := range c.files(dir) {
		text, _ := c.read(f)
		for i, line := range strings.Split(text, "\n") {
			if found(line) {
				hit = true
				c.warn("%s:%d:%s", f, i+1, line)
			}
		}
	}
	if hit {
		c.die("%s", refusal)
	}
}
