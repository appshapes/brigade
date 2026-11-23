//go:build !windows

package commands

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
)

// hostilePath is a path a team file and `--to` accept, with every
// character work item 357 names in it: a space, `$(…)`, a backtick and a
// single quote, and others a shell acts on. Each command in it would leave
// a file behind. It has no `?` and no `#`, which `--to` refuses as a query
// and a fragment, and no backslash, which the rigs' team files, written
// here as JSON by hand, would need escaped: the backslash is tried on the
// function itself and on the helpers below.
const hostilePath = "/a b$(touch pwned1)`touch pwned2`'c;touch pwned3;'$(touch pwned4)&touch pwned5|d*~!{e,f}"

// hostileAddress adds what hostilePath leaves out.
const hostileAddress = "https://brigade.example.test:8743" + hostilePath + `\'$(touch pwned6)?#x`

// posixShells are the shells of this machine a printed command is pasted
// into for the test: every one that is installed, and sh always.
func posixShells(t *testing.T) []string {
	t.Helper()
	shells := []string{"/bin/sh"}
	for _, name := range []string{"bash", "zsh", "dash"} {
		if path, err := exec.LookPath(name); err == nil {
			shells = append(shells, path)
		}
	}
	return shells
}

// pasted runs a command line as a shell runs it when a person pastes it,
// in each shell of the machine, with `brigade` standing for a function that
// writes its arguments down, one a line. It returns those arguments, the
// same in every shell or the test fails, and fails when the line left any
// file behind: something in it was carried out. The line is run from a
// file, as `<shell> <file>`, and never with `-c`.
func pasted(t *testing.T, line string) []string {
	t.Helper()
	// `--secret-file <path>` is a placeholder for its reader, and a
	// redirection to a shell: it is made a word, since it is no address.
	line = strings.ReplaceAll(line, "<path>", "PATH-PLACEHOLDER")
	var first []string
	for n, shell := range posixShells(t) {
		dir := t.TempDir()
		script := "brigade() { : > args; for a in \"$@\"; do printf '%s\\n' \"$a\" >> args; done; }\n" + line + "\n"
		if err := os.WriteFile(filepath.Join(dir, "pasted"), []byte(script), 0o600); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // G204: a shell of this machine, found by name, on a file this test wrote
		cmd := exec.CommandContext(t.Context(), shell, "pasted")
		cmd.Dir = dir
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + dir}
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s could not run the pasted line: %v\n%s\n%s", shell, err, out, line)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range entries {
			if e.Name() != "pasted" && e.Name() != "args" {
				t.Errorf("%s carried out something in the pasted line, which left %s behind:\n%s", shell, e.Name(), line)
			}
		}
		raw, err := os.ReadFile(filepath.Join(dir, "args"))
		if err != nil {
			t.Fatalf("%s did not run brigade for the pasted line: %v\n%s", shell, err, line)
		}
		args := strings.Split(strings.TrimSuffix(string(raw), "\n"), "\n")
		if n == 0 {
			first = args
		} else if strings.Join(args, "\x00") != strings.Join(first, "\x00") {
			t.Errorf("%s read other arguments than /bin/sh:\n%q\n%q", shell, args, first)
		}
	}
	return first
}

// TestShellWordIsTheValueAsOneWordInEveryShell (work item 357): whatever a
// value holds, a shell takes its printed form as one word, the value, and
// carries out nothing in it. An ordinary address is printed as it is.
func TestShellWordIsTheValueAsOneWordInEveryShell(t *testing.T) {
	t.Parallel()
	for _, plain := range []string{
		"https://brigade.example.test:8743", "brigade.example.test", "https://h.example.test/teams/alpha_1-2", "127.0.0.1:8743",
		"sha256:" + strings.Repeat("0a", 32), "https://h.example.test/a%20b+c",
	} {
		if got := shellWord(plain); got != plain {
			t.Errorf("an ordinary value is printed as it is: %s, got %s", plain, got)
		}
	}
	for value, want := range map[string]string{
		"":                   `''`,
		"a b":                `'a b'`,
		"a'b":                `'a'"'"'b'`,
		`a\b`:                `'a'"\\"'b'`,
		`\'`:                 `"\\'"`,
		"https://[::1]:8743": `'https://[::1]:8743'`,
		"a,b":                `'a,b'`,
		"@a":                 `'@a'`,
		// PowerShell reads a typographic single quote as a single quote: it
		// is never left between single quotes, where it would end them.
		"a\u2019;calc;\u2018b": "'a'\"\u2019\"';calc;'\"\u2018\"'b'",
		"\u201a\u201b":         "\"\u201a\u201b\"",
		"a\u201cb\u201d":       "'a\u201cb\u201d'",
	} {
		if got := shellWord(value); got != want {
			t.Errorf("shellWord(%q) = %s, want %s", value, got, want)
		}
	}
	values := []string{
		hostileAddress, "a b", "$(touch pwned)", "`touch pwned`", "'", "''", "a'b", `\`, `a\`, `\'`, `a\'$(touch pwned)`,
		`'\''$(touch pwned)'\''`, `"$(touch pwned)"`, ";touch pwned;", "&touch pwned", "|touch pwned", "a>pwned", "<pwned", "*", "?", "~", "~root", "!!", "!$",
		"#x", "{a,b}", "https://[::1]:8743", "=ls", "$HOME", "${HOME}", "%1", "-n", "é ü", "$'\\x41'", "a\tb",
		"a,b", "@a", "a\u2019;touch pwned;\u2018b", "a'b!c", "x!e'$(touch pwned)!echo",
	}
	for _, value := range values {
		args := pasted(t, "brigade team move --from "+shellWord(value)+" --after")
		if want := []string{"team", "move", "--from", value, "--after"}; strings.Join(args, "\x00") != strings.Join(want, "\x00") {
			t.Errorf("shellWord(%q) = %s is read as %q", value, shellWord(value), args)
		}
	}
}

// commandsIn are the commands a text prints for its reader to run: what
// stands between two backticks and begins with `brigade ` or with a flag.
// A backtick inside a quoted address does not end a command: the cut is at
// the backtick that is followed by the text around a command, which is a
// space, a bracket, a point of punctuation or the end.
func commandsIn(t *testing.T, text string) []string {
	t.Helper()
	var out []string
	for rest := text; ; {
		i := strings.Index(rest, "`")
		if i < 0 {
			return out
		}
		rest = rest[i+1:]
		if !strings.HasPrefix(rest, "brigade ") && !strings.HasPrefix(rest, "--") {
			continue
		}
		end := commandEndRE.FindStringIndex(rest)
		if end == nil {
			t.Fatalf("a command is opened and never closed in: %s", text)
		}
		out = append(out, rest[:end[0]])
		rest = rest[end[0]+1:]
	}
}

var commandEndRE = regexp.MustCompile("`( says| clears| stops|;|\\)|\\.|,|$|\n)")

// TestACommandPrintedWithAHostileAddressDoesNoMoreThanItsArguments (work
// item 357): at every site that prints an address after a flag, an address
// with a space, `$(…)`, a backtick and a single quote in it is one word to
// a shell, the address, and nothing in it is carried out. The sites, each
// reached here: the record's form (`--show --from` and `--clear --from` of
// a clear that was not answered, twice; `--clear --from` for the record's
// owner, twice, through ownersFrom), the move's rerun (`--to … --from …`),
// the move's stop (`--show --from`), the address a team moves back to
// (`--to`, through toServer, with its `--certificate`), and the command
// that undoes a move (`--clear --from`, clearCommandFor).
func TestACommandPrintedWithAHostileAddressDoesNoMoreThanItsArguments(t *testing.T) {
	t.Parallel()
	// holds fails unless one of the commands a text prints carries the
	// address, whole, as the argument after the flag.
	holds := func(t *testing.T, site, text, flag, address string) {
		t.Helper()
		found := false
		for _, command := range commandsIn(t, text) {
			if !strings.Contains(command, flag+" ") {
				continue
			}
			line := command
			if strings.HasPrefix(line, "--") {
				line = "brigade team move " + line
			}
			args := pasted(t, line)
			for i, a := range args {
				if a == flag && i+1 < len(args) && args[i+1] == address {
					found = true
				}
			}
		}
		if !found {
			t.Errorf("%s: no command of the text carries %s %q as its argument:\n%s", site, flag, address, text)
		}
	}

	t.Run("the record's form", func(t *testing.T) {
		t.Parallel()
		refusal, lines := commandsOfTheRecordsForm(t, hostilePath)
		address := addressLine(lastAddress(t, refusal, hostilePath))
		holds(t, "the refusal of a clear that was not answered", refusal, "--from", address)
		holds(t, "the lines of a record that may stand", lines, "--from", address)
	})
	t.Run("a move", func(t *testing.T) {
		t.Parallel()
		rerun, stop := commandsOfAMove(t, hostilePath)
		holds(t, "the command that is run again", rerun, "--from", addressLine(lastAddress(t, rerun, hostilePath)))
		holds(t, "the stop that says where the record is read", stop, "--from", addressLine(lastAddress(t, stop, hostilePath)))
	})
	t.Run("the helpers the other sites print through", func(t *testing.T) {
		t.Parallel()
		hostile := hostileAddress
		holds(t, "the record's owner", "with `brigade team move --clear --from "+ownersFrom(hostile)+"`", "--from", addressLine(hostile))
		holds(t, "the address a team moves back to", "name it: `--to "+toServer(hostile, moveOldPin)+"`. ", "--to", addressLine(hostile))
		holds(t, "its pin", "name it: `--to "+toServer(hostile, moveOldPin)+"`. ", "--certificate", moveOldPin)
		// A pin only a hand-edited store can hold is one word too.
		pin := "sha256:$(touch pwned)`touch pwned`' x"
		holds(t, "a pin that is no pin", "`--to "+toServer(hostile, pin)+"`.", "--certificate", pinLine(pin))

		// The command that undoes a move names the backend the team left by
		// its whole address where its host holds two of the team's.
		store := t.TempDir()
		other := "https://brigade.example.test:8743/other"
		for _, url := range []string{hostile, other, "https://gone-to.example.test"} {
			if err := write.EnsureBinding(store, teamstore.Key("rest", url, "t_bob"), "rest", url, "", "", "t_bob", "devs", "p_"+teamstore.Key("rest", url, "t_bob")[:6]); err != nil {
				t.Fatal(err)
			}
		}
		undo := clearCommandFor(store, "t_bob", teamstore.Key("rest", "https://gone-to.example.test", "t_bob"), hostile)
		holds(t, "the command that undoes a move", "to undo: `"+undo+"` stops", "--from", addressLine(hostile))
	})
}

// lastAddress is the address a rig built on the path: the text names its
// host and port, which the rig chose, before the path.
func lastAddress(t *testing.T, text, path string) string {
	t.Helper()
	m := regexp.MustCompile(`https?://[A-Za-z0-9.:\[\]-]+`).FindAllString(text, -1)
	if len(m) == 0 {
		t.Fatalf("the text names no address: %s", text)
	}
	return m[len(m)-1] + path
}

// TestNoCommandPrintsAnAddressButAsOneShellWord (work item 357) reads this
// package's own source: wherever a string that ends in `--to `, `--from `,
// `--on `, `--url ` or `--certificate ` is followed by a value, the value
// is one of the forms that are a shell word. A site added tomorrow that
// prints an address as it stands fails here, and is then added to the test
// above.
//
// What it does not hold: a value after another flag (a path after
// `--secret-file`, a name after `--team`), a value put in by other means
// than `"… --flag " + value` (a join of several, a format verb other than
// %s, %q and %v), and a command printed by another package. Those are for
// whoever adds one to see.
func TestNoCommandPrintsAnAddressButAsOneShellWord(t *testing.T) {
	t.Parallel()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	// What may follow the flag: a call that makes a shell word, or a
	// variable whose every assignment in the file is from one.
	site := regexp.MustCompile("--(to|from|on|url|certificate) \"\\s*\\+\\s*([A-Za-z_][A-Za-z0-9_.]*\\(?)")
	format := regexp.MustCompile(`--(to|from|on|url|certificate) %[sqv]`)
	words := map[string]bool{"addressWord(": true, "shellWord(": true, "ownersFrom(": true, "toServer(": true}
	variables := map[string]bool{"destFrom": true}
	assigned := func(text, name string) bool {
		all := regexp.MustCompile(`\b`+name+`\b[^=\n]*:?= `).FindAllStringIndex(text, -1)
		words := regexp.MustCompile(`(?m)^\s*`+name+` :?= addressWord\(`).FindAllStringIndex(text, -1)
		return len(all) > 0 && len(all) == len(words)
	}
	sites := 0
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if m := format.FindString(text); m != "" {
			t.Errorf("%s formats a value after a flag (%s): print it with addressWord or shellWord", file, m)
		}
		for _, m := range site.FindAllStringSubmatch(text, -1) {
			sites++
			next := m[2]
			if words[next] || variables[next] && assigned(text, next) {
				continue
			}
			t.Errorf("%s prints %s after --%s as it stands: a value after a flag in a printed command is addressWord's or shellWord's", file, next, m[1])
		}
	}
	// The sites of 2026-11-23: nine strings in teammove.go and teammoveto.go.
	// Fewer means the pattern no longer finds them, and the test holds nothing.
	if sites < 9 {
		t.Errorf("found %d sites that print a value after a flag, and there were 9: the pattern has gone blind", sites)
	}
}
