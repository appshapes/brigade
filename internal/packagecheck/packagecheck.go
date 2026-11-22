// Package packagecheck is the one check of the harness packages (work item
// 382): the static rules that `make plugin-check`, `muse-plugin-check`,
// `codex-plugin-check` and `grok-plugin-check` hold of plugin/,
// muse/brigade/, codex/brigade/ and grok/brigade/. It was four shell
// scripts, one per package, each with its own copy of what the four share:
// the allow-list of files, the assets equal to plugin/bin's, the
// bootstrap's mode in git, the version the manifest declares, the shells
// the scripts parse under, and that no skill takes the model's reach away.
// Those are written once here; what a package has of its own (its
// manifest's form, how its hooks are worded, its marketplace file) is
// beside them, one file a harness.
//
// A check reads the tree and runs git, sh and shellcheck where they are;
// it starts no host and changes nothing. Its lines are the scripts' own:
// `ok: …` on stdout for each rule held, one `<name>: …` line on stderr for
// the first rule broken, and exit status 1.
package packagecheck

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// Options are what one check is run with.
type Options struct {
	// Root is the repository's root, which the scripts took as their
	// working directory.
	Root string
	// Environ is the environment git, sh and shellcheck are found and run
	// with, and where CI is read.
	Environ []string
	// Args are the check's own arguments (Grok Build's takes `--source`).
	Args []string
	// Stdout and Stderr receive the check's lines.
	Stdout, Stderr io.Writer
}

// A check is one package's rules.
type check struct {
	// name is the word the check's lines begin with, the script's name.
	name string
	run  func(c *checker)
}

// checks holds the four, by the harness's id in docs/harnesses.json.
var checks = map[string]check{
	"claude-code": {"plugin-check", (*checker).claude},
	"muse-code":   {"muse-plugin-check", (*checker).muse},
	"codex":       {"codex-plugin-check", (*checker).codex},
	"grok":        {"grok-plugin-check", (*checker).grok},
}

// IDs are the harnesses that have a check, sorted.
func IDs() []string {
	out := make([]string, 0, len(checks))
	for id := range checks {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// Run checks the package of the harness id and returns the exit status: 0
// when every rule holds, 1 at the first that does not, 2 for a harness
// that has no check or an argument the check does not take.
func Run(id string, o Options) (status int) {
	ch, known := checks[id]
	if o.Stdout == nil {
		o.Stdout = io.Discard
	}
	if o.Stderr == nil {
		o.Stderr = io.Discard
	}
	if !known {
		_, _ = fmt.Fprintf(o.Stderr, "package-check: no check for the harness %q (known: %s)\n", id, strings.Join(IDs(), ", "))
		return 2
	}
	c := &checker{name: ch.name, o: o}
	defer func() {
		switch stop := recover().(type) {
		case nil:
		case stopped:
			status = int(stop)
		default:
			panic(stop)
		}
	}()
	ch.run(c)
	return 0
}

// stopped ends a check at its first broken rule, with the exit status.
type stopped int

type checker struct {
	name string
	o    Options
}

// die says which rule is broken and ends the check.
func (c *checker) die(format string, args ...any) {
	_, _ = fmt.Fprintf(c.o.Stderr, "%s: %s\n", c.name, fmt.Sprintf(format, args...))
	panic(stopped(1))
}

// usage ends the check over its arguments.
func (c *checker) usage(format string, args ...any) {
	_, _ = fmt.Fprintf(c.o.Stderr, format+"\n", args...)
	panic(stopped(2))
}

func (c *checker) ok(format string, args ...any) {
	_, _ = fmt.Fprintf(c.o.Stdout, "ok: %s\n", fmt.Sprintf(format, args...))
}

func (c *checker) skip(format string, args ...any) {
	_, _ = fmt.Fprintf(c.o.Stdout, "skip: %s (P3-1)\n", fmt.Sprintf(format, args...))
}

func (c *checker) say(line string) {
	_, _ = fmt.Fprintln(c.o.Stdout, line)
}

func (c *checker) warn(format string, args ...any) {
	_, _ = fmt.Fprintf(c.o.Stderr, format+"\n", args...)
}

// path is a path of the tree as the operating system spells it.
func (c *checker) path(rel string) string { return filepath.Join(c.o.Root, filepath.FromSlash(rel)) }

func (c *checker) isDir(rel string) bool {
	info, err := os.Stat(c.path(rel))
	return err == nil && info.IsDir()
}

func (c *checker) isFile(rel string) bool {
	info, err := os.Stat(c.path(rel))
	return err == nil && info.Mode().IsRegular()
}

func (c *checker) isExecutable(rel string) bool {
	info, err := os.Stat(c.path(rel))
	return err == nil && adapterkit.IsExecutable(info)
}

// read is the file's text, and whether it could be read.
func (c *checker) read(rel string) (string, bool) {
	data, err := os.ReadFile(c.path(rel))
	return string(data), err == nil
}

// files are the regular files under dir, as `find dir -type f` lists them
// and `LC_ALL=C sort` orders them: slash-separated, relative to the root.
func (c *checker) files(dir string) []string {
	var out []string
	_ = filepath.WalkDir(c.path(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // what cannot be read is not listed, as find lists it not
		}
		if d.Type().IsRegular() {
			rel, rerr := filepath.Rel(c.o.Root, p)
			if rerr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// links are the symbolic links under dir.
func (c *checker) links(dir string) []string {
	var out []string
	_ = filepath.WalkDir(c.path(dir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // as files
		}
		if d.Type()&fs.ModeSymlink != 0 {
			rel, rerr := filepath.Rel(c.o.Root, p)
			if rerr == nil {
				out = append(out, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	return out
}

// which is the tool's path on the check's PATH, "" when it is not there.
func (c *checker) which(tool string) string {
	for _, dir := range filepath.SplitList(adapterkit.Getenv(c.o.Environ, "PATH")) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, tool)
		if info, err := os.Stat(p); err == nil && info.Mode().IsRegular() && adapterkit.IsExecutable(info) {
			return p
		}
	}
	return ""
}

// command runs a tool in the root with the check's environment and returns
// what it printed on stdout. The tool's stderr goes to the check's own.
func (c *checker) command(tool string, args ...string) (string, error) {
	return c.spawn(c.o.Stderr, tool, args...)
}

// inGit reports whether the root is inside a git repository. Git's own
// words for "it is not" are dropped: the check has its line for that.
func (c *checker) inGit() bool {
	_, err := c.spawn(io.Discard, "git", "rev-parse", "--git-dir")
	return err == nil
}

func (c *checker) spawn(stderr io.Writer, tool string, args ...string) (string, error) {
	p := c.which(tool)
	if p == "" {
		return "", errors.New(tool + " is not on PATH")
	}
	cmd := exec.CommandContext(context.Background(), p, args...) //nolint:gosec // G204: a tool found on the check's PATH, an argv array, no shell
	cmd.Dir = c.o.Root
	cmd.Env = c.o.Environ
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = stderr
	err := cmd.Run()
	return out.String(), err
}

// sp is the blank a line holds: what [[:space:]] matches in sed, grep and
// awk, which read a line at a time and so never match across two.
const sp = `[ \t\r\f\v]`

// re compiles a pattern in which `_` stands for any run of blanks within
// a line, and reads the text line by line (`^` and `$` are a line's).
func re(pattern string) *regexp.Regexp {
	return regexp.MustCompile(`(?m)` + strings.ReplaceAll(pattern, "_", sp+"*"))
}

var versionRE = re(`^_"version":_"([^"]+)"`)

// declaredVersion is the first "version" a manifest declares on a line of
// its own, "" when it declares none.
func declaredVersion(text string) string {
	if m := versionRE.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	return ""
}

// firstLine is the text's first line as `head -n 1` gives it: without
// its line feed and with anything else, a carriage return included. A pin
// that ends its line the Windows way is then no version token, and equal
// to no manifest's version.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	return line
}

// lineCount is what `awk 'END {print NR}'` prints: the lines, a last one
// with no end among them.
func lineCount(text string) int {
	n := strings.Count(text, "\n")
	if text != "" && !strings.HasSuffix(text, "\n") {
		n++
	}
	return n
}

// allowed walks the package and ends the check at the first file that
// keep does not allow. keep is given the path below the package.
func (c *checker) allowed(dir string, keep func(rel string) bool) {
	for _, f := range c.files(dir) {
		if !keep(strings.TrimPrefix(f, dir+"/")) {
			c.die("not allow-listed under %s/: %s", dir, f)
		}
	}
}

// underSkills is the shell's `skills/*/*`: a skill's directory and
// whatever it carries.
func underSkills(rel string) bool {
	rest, ok := strings.CutPrefix(rel, "skills/")
	return ok && strings.Contains(rest, "/")
}

func oneOf(rel string, names ...string) bool {
	for _, n := range names {
		if rel == n {
			return true
		}
	}
	return false
}

// gitModes is `git ls-files -s dir`: a line a tracked file, its mode
// first and its path last.
func (c *checker) gitModes(dir string) string {
	if !c.inGit() {
		c.die("not a git repository: cannot check the recorded file modes")
	}
	modes, err := c.command("git", "ls-files", "-s", dir)
	if err != nil {
		c.die("git ls-files failed")
	}
	if strings.TrimSpace(modes) == "" {
		c.die("git tracks no file under %s/", dir)
	}
	return modes
}

// tracked100755 reports whether git records the path as an executable.
func tracked100755(modes, path string) bool {
	for _, line := range strings.Split(modes, "\n") {
		if strings.HasPrefix(line, "100755 ") && strings.HasSuffix(line, "\t"+path) {
			return true
		}
	}
	return false
}

// assets holds a package's three pins to plugin/bin's, the canonical ones.
func (c *checker) assets(dir, packages string) {
	for _, asset := range []string{"brigade", "VERSION", "checksums.txt"} {
		canonical, mine := "plugin/bin/"+asset, dir+"/bin/"+asset
		if !c.isFile(canonical) {
			c.die("missing %s", canonical)
		}
		if !c.isFile(mine) {
			c.die("missing %s", mine)
		}
		a, _ := os.ReadFile(c.path(canonical))
		b, _ := os.ReadFile(c.path(mine))
		if !bytes.Equal(a, b) {
			c.die("%s differs from %s: one canonical pin, %s packages (release-prep copies it)", mine, canonical, packages)
		}
	}
}

// parses holds each file to the shells that are here: sh always, and
// bash, zsh and dash where they are installed. It returns their names.
func (c *checker) parses(failed func(shell, file string), files ...string) string {
	checked := []string{"sh"}
	for _, f := range files {
		if _, err := c.command("sh", "-n", f); err != nil {
			failed("sh", f)
		}
	}
	for _, shell := range []string{"bash", "zsh", "dash"} {
		if c.which(shell) == "" {
			continue
		}
		for _, f := range files {
			if _, err := c.command(shell, "-n", f); err != nil {
				failed(shell, f)
			}
		}
		checked = append(checked, shell)
	}
	return strings.Join(checked, " ")
}

var disabledRE = re(`^_disable-model-invocation_:`)

// disablesTheModel reports whether a skill declares
// disable-model-invocation (card 28: every Brigade skill is reachable by
// the model, and this is the one thing a check holds of what a skill
// declares).
func (c *checker) disablesTheModel(skill string) bool {
	text, _ := c.read(skill)
	return disabledRE.MatchString(text)
}

// glob is the files a shell pattern names under the root, sorted.
func (c *checker) glob(pattern string) []string {
	matches, _ := filepath.Glob(c.path(pattern))
	var out []string
	for _, m := range matches {
		if rel, err := filepath.Rel(c.o.Root, m); err == nil {
			out = append(out, filepath.ToSlash(rel))
		}
	}
	sort.Strings(out)
	return out
}

func isFileAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func isExecutableAt(path string) bool {
	info, err := os.Stat(path)
	return err == nil && adapterkit.IsExecutable(info)
}
