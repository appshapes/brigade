package packagecheck

import (
	"regexp"
	"sort"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// PointedHookSubcommands are the `brigade hook` subcommands a Muse Code
// wrapper or a Codex CLI hook command may name: neither host has a Stop
// hook of Brigade's.
var PointedHookSubcommands = []string{"prompt", "session-end", "session-start"}

var (
	museSchemaRE     = re(`^_"schemaVersion":_1_,?_$`)
	museCapsRE       = re(`"capabilities"_:`)
	museManifestDir  = re(`"manifestDir"_:_"\.muse-plugin"`)
	museForeignRE    = re(`"(args|userConfig|mcpServers|channels)"_:`)
	museCommandRE    = re(`"command"_:_\[([^\]\n]*)\]`)
	museRestRE       = regexp.MustCompile(`^` + sp + `*"[^"]*"` + sp + `*(.*)$`)
	museWrapperEndRE = regexp.MustCompile(`(?m)^exec "\$dir/\.\./bin/brigade" hook ([a-z-]+) "\$@"$`)
	museSkillPathRE  = re(`"path"_:_"(skills/[^"]*)"`)
	museSkillEntryRE = re(`"id"_:_"([^"]*)"_,_"path"_:_"skills/([^/"]*)/SKILL\.md"`)
	catalogNameRE    = re(`^_"name":_"brigade"`)
	catalogTransport = re(`"transport":_"local-path"`)
	catalogSourceRE  = re(`"source":_"muse/brigade"`)
	catalogStatusRE  = re(`"status":_"available"`)
	catalogDigestRE  = re(`^_"digest":_"sha256:[0-9a-f]{64}"`)
)

// muse is the Muse Code package's check: muse/brigade/ and the catalog at
// the repository's root (cards 88, 115).
func (c *checker) muse() {
	const (
		dir      = "muse/brigade"
		manifest = "muse/brigade/.muse-plugin/plugin.json"
		catalog  = "marketplace.json"
	)
	if !c.isDir(dir) {
		c.die("run me from the repository root: no %s directory here", dir)
	}
	text, there := c.read(manifest)
	if !there {
		c.die("missing %s", manifest)
	}

	// 1. Only allow-listed paths, each tracked: Muse hashes every file of
	// the package into its digest.
	c.allowed(dir, func(rel string) bool {
		return oneOf(rel, "bin/brigade", "bin/VERSION", "bin/checksums.txt", ".muse-plugin/plugin.json", "README.md",
			"hooks/session-start.sh", "hooks/prompt.sh", "hooks/session-end.sh") || underSkills(rel)
	})
	if !c.inGit() {
		c.die("not a git repository: cannot tell which files under %s/ are tracked", dir)
	}
	stray, err := c.command("git", "ls-files", "--others", "--", dir)
	if err != nil {
		c.die("git ls-files failed")
	}
	if strings.TrimSpace(stray) != "" {
		c.die("git does not track these under %s/, and Muse would hash them into the package digest: %s-- delete them, or add them to git and run 'make muse-digest'",
			dir, strings.ReplaceAll(strings.TrimRight(stray, "\n"), "\n", " "))
	}
	c.ok("%s/ contains only allow-listed paths, each tracked by git", dir)

	// 2. The three pins are plugin/bin's, and the bootstrap an executable.
	c.assets(dir, "two")
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

	// 4. A native manifest, with nothing Muse does not run.
	if !museSchemaRE.MatchString(text) {
		c.die("%s is not a native Muse manifest (no \"schemaVersion\": 1)", manifest)
	}
	if !museCapsRE.MatchString(text) {
		c.die("%s declares no \"capabilities\"", manifest)
	}
	if !museManifestDir.MatchString(text) {
		c.die("%s's compat.manifestDir is not \".muse-plugin\"", manifest)
	}
	if museForeignRE.MatchString(text) {
		c.die("%s carries a member Muse does not run (args, userConfig, mcpServers or channels)", manifest)
	}
	c.ok("%s is a native manifest with capabilities and none of args, userConfig, mcpServers, channels", manifest)

	// 5. No symbolic link, which Muse refuses to install.
	if links := c.links("muse"); len(links) > 0 {
		c.die("symlink(s) under muse/, which Muse refuses to install: %s", strings.Join(links, "\n"))
	}
	c.ok("no symlink under muse/")

	// 6. Three hooks, each its own wrapper, which execs the bootstrap with
	// a subcommand the binary has.
	known := strings.Join(PointedHookSubcommands, " ")
	cmds := museCommandRE.FindAllStringSubmatch(text, -1)
	if len(cmds) == 0 {
		c.die("%s declares no hook \"command\"", manifest)
	}
	var firsts []string
	for _, m := range cmds {
		arr := m[1]
		first, rest := "", ""
		if f := firstQuotedRE.FindStringSubmatch(arr); f != nil {
			first = f[1]
		}
		if r := museRestRE.FindStringSubmatch(arr); r != nil {
			rest = r[1]
		}
		firsts = append(firsts, first)
		if first == "" {
			c.die("%s has an empty hook command", manifest)
		}
		if rest != "" {
			c.die("%s hook command %s has more than one element; the wrapper carries the subcommand", manifest, arr)
		}
		if !strings.HasPrefix(first, "hooks/") || !strings.HasSuffix(first, ".sh") {
			c.die("%s hook command '%s' is not a wrapper under hooks/", manifest, first)
		}
		wrapper := dir + "/" + first
		if !c.isFile(wrapper) {
			c.die("%s names a wrapper that does not exist: %s", manifest, first)
		}
		if !c.isExecutable(wrapper) {
			c.die("wrapper is not executable: %s", wrapper)
		}
		if !tracked100755(modes, wrapper) {
			c.die("git does not track %s as 100755", wrapper)
		}
		// Every line of the form counts, as `sed -n …p` printed every one:
		// a wrapper with two names two subcommands, which is none the
		// binary has.
		body, _ := c.read(wrapper)
		var subs []string
		for _, s := range museWrapperEndRE.FindAllStringSubmatch(body, -1) {
			subs = append(subs, s[1])
		}
		sub := strings.Join(subs, "\n")
		if sub == "" {
			c.die("%s does not end in exec \"$dir/../bin/brigade\" hook <subcommand> \"$@\"", wrapper)
		}
		if !oneOf(sub, PointedHookSubcommands...) {
			c.die("%s names the hook subcommand '%s', which the binary does not implement (known: %s)", wrapper, sub, known)
		}
		if first != "hooks/"+sub+".sh" {
			c.die("%s execs the '%s' hook but is not named hooks/%s.sh", wrapper, sub, sub)
		}
	}
	if dups := repeated(firsts); len(dups) > 0 {
		c.die("two hooks share one source, which Muse refuses: %s", strings.Join(dups, "\n"))
	}
	if len(cmds) != 3 {
		c.die("%s declares %d hooks, want exactly 3 (SessionStart, UserPromptSubmit, SessionEnd)", manifest, len(cmds))
	}
	c.ok("the three hooks each name their own wrapper, which execs the bootstrap with a subcommand the binary implements")

	// 7. The wrappers parse, and shellcheck has nothing to say of them.
	wrappers := c.glob(dir + "/hooks/*.sh")
	checked := c.parses(func(shell, file string) { c.die("%s -n %s failed", shell, file) }, wrappers...)
	switch {
	case c.which("shellcheck") != "":
		out, serr := c.command("shellcheck", append([]string{"-s", "sh"}, wrappers...)...)
		if serr != nil {
			c.warn("%s", strings.TrimRight(out, "\n"))
			c.die("shellcheck reported problems")
		}
		c.ok("wrappers parse under: %s; shellcheck -s sh clean", checked)
	case adapterkit.Getenv(c.o.Environ, "CI") != "":
		c.die("shellcheck is not installed and CI is set")
	default:
		c.warn("muse-plugin-check: WARNING: shellcheck is not installed; skipping (CI enforces it)")
		c.ok("wrappers parse under: %s (shellcheck skipped, not CI)", checked)
	}

	// 8. The manifest names every skill, once, under its directory's name,
	// and none takes the model's reach away.
	var paths []string
	for _, m := range museSkillPathRE.FindAllStringSubmatch(text, -1) {
		paths = append(paths, m[1])
	}
	for _, p := range paths {
		if !c.isFile(dir + "/" + p) {
			c.die("%s names a skill that does not exist: %s", manifest, p)
		}
	}
	if dup := repeated(paths); len(dup) > 0 {
		c.die("%s names a skill more than once: %s", manifest, strings.Join(dup, " "))
	}
	entries := museSkillEntryRE.FindAllStringSubmatch(text, -1)
	if len(entries) != len(paths) {
		c.die("%s has a skill entry that is not { \"id\": \"<dir>\", \"path\": \"skills/<dir>/SKILL.md\" } on one line", manifest)
	}
	for _, e := range entries {
		if e[1] != e[2] {
			c.die("%s names skills/%s/ with the id %s: a skill's id is its directory's name", manifest, e[2], e[1])
		}
	}
	var disabled, unnamed []string
	for _, f := range c.glob(dir + "/skills/*/SKILL.md") {
		if !c.isFile(f) {
			continue
		}
		rel := strings.TrimPrefix(f, dir+"/")
		if !oneOf(rel, paths...) {
			unnamed = append(unnamed, " "+rel)
		}
		if c.disablesTheModel(f) {
			disabled = append(disabled, " "+f)
		}
	}
	if len(unnamed) > 0 {
		c.die("%s does not name these skills, and Muse loads only the ones it names:%s", manifest, strings.Join(unnamed, ""))
	}
	if len(disabled) > 0 {
		c.die("disable-model-invocation is declared in:%s -- Brigade's skills are reachable by the model (card 28)", strings.Join(disabled, ""))
	}
	c.descriptionsFit(c.glob(dir + "/skills/*/SKILL.md"))
	c.ok("every skill the manifest names exists, once, under its own id; the manifest names every skill; none declares disable-model-invocation or has a description over %d characters", DescriptionLimit)

	// 9. The catalog Muse reads first names the package, its version and a
	// digest of it.
	cat, hasCatalog := c.read(catalog)
	if !hasCatalog {
		c.die("missing %s: the Muse marketplace catalog (card 115), what `muse plugins marketplace add brigade appshapes/brigade` reads", catalog)
	}
	if !museSchemaRE.MatchString(cat) {
		c.die("%s declares no \"schemaVersion\": 1", catalog)
	}
	if !catalogNameRE.MatchString(cat) {
		c.die("%s names no plugin brigade", catalog)
	}
	if !catalogTransport.MatchString(cat) {
		c.die("%s's install transport is not local-path (the only transport a snapshot supports)", catalog)
	}
	if !catalogSourceRE.MatchString(cat) {
		c.die("%s does not point at muse/brigade", catalog)
	}
	if !catalogStatusRE.MatchString(cat) {
		c.die("%s's availability is not \"available\" (Muse installs no other status)", catalog)
	}
	if !catalogDigestRE.MatchString(cat) {
		c.die("%s carries no \"digest\": \"sha256:<64 hex>\" on its own line (make muse-digest writes it)", catalog)
	}
	if catalogVersion := declaredVersion(cat); catalogVersion != version {
		c.die("%s version '%s' != %s/bin/VERSION '%s' (make muse-digest rewrites both)", catalog, catalogVersion, dir, version)
	}
	c.ok("%s names brigade at muse/brigade, pins %s and carries a sha256 digest", catalog, version)
	c.say("muse-plugin-check: all checks passed")
}

// repeated are the values that occur more than once, sorted, each once.
func repeated(values []string) []string {
	seen := map[string]int{}
	for _, v := range values {
		seen[v]++
	}
	var out []string
	for v, n := range seen {
		if n > 1 {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
