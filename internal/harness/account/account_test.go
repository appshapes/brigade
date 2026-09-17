package account_test

import (
	"encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/protocol"
)

// The card-24 part B reader: the account email is a DEFAULT for a display
// label, so every failure it can meet answers "" and a diagnostic, and no
// test here may see it refuse, panic or write.
//
// t.Setenv forbids t.Parallel, so these tests are serial by construction:
// the reader takes an environ, and the environment is what the brief names
// as the thing to fake (CLAUDE_CONFIG_DIR and HOME over t.TempDir).

// dirs lays out a config directory and a home directory under one temp
// root and points the environment at them.
func dirs(t *testing.T) (claudeConfig, home string) {
	t.Helper()
	root := t.TempDir()
	claudeConfig, home = filepath.Join(root, "claude-config"), filepath.Join(root, "home")
	for _, d := range []string{claudeConfig, home} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeConfig)
	t.Setenv("HOME", home)
	return claudeConfig, home
}

// writeConfig puts a .claude.json carrying body in dir.
func writeConfig(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, account.FileName)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// controlCharacterDoc is a config file whose emailAddress carries a BEL,
// an ESC, a NUL and a newline. The characters are built from their code
// points and marshalled, so this source file holds no control character
// of its own and nothing that rewrites it can normalise them away.
func controlCharacterDoc(t *testing.T) string {
	t.Helper()
	nasty := "a" + string(rune(0x07)) + "l" + string(rune(0x1b)) + "i" +
		string(rune(0x00)) + "c" + string(rune(0x0a)) + "e@example.com"
	doc, err := json.Marshal(map[string]any{"oauthAccount": map[string]any{"emailAddress": nasty}})
	if err != nil {
		t.Fatal(err)
	}
	return string(doc)
}

// withAccount is a realistic file: the key among others, in a document
// that also carries members this package has never heard of.
func withAccount(email string) string {
	return `{"numStartups":41,"projects":{"/home/x/repo":{"lastCost":0.5}},` +
		`"oauthAccount":{"accountUuid":"u-1","emailAddress":"` + email + `","organizationName":"Acme"},` +
		`"tipsHistory":{"new-user-warmup":3}}`
}

func TestEmailReadsTheConfigDirectoryFile(t *testing.T) {
	cfg, _ := dirs(t)
	writeConfig(t, cfg, withAccount("alice@example.com"))

	got, err := account.Email(os.Environ())
	if err != nil {
		t.Fatalf("Email: %v", err)
	}
	if got != "alice@example.com" {
		t.Fatalf("Email = %q, want alice@example.com", got)
	}
}

func TestEmailPrefersTheConfigDirectoryOverHome(t *testing.T) {
	cfg, home := dirs(t)
	writeConfig(t, cfg, withAccount("config-dir@example.com"))
	writeConfig(t, home, withAccount("home@example.com"))

	got, err := account.Email(os.Environ())
	if err != nil {
		t.Fatalf("Email: %v", err)
	}
	if got != "config-dir@example.com" {
		t.Fatalf("Email = %q, want the config-directory file's value", got)
	}
}

func TestEmailFallsBackToTheHomeFile(t *testing.T) {
	_, home := dirs(t)
	writeConfig(t, home, withAccount("home@example.com"))

	got, err := account.Email(os.Environ())
	if err != nil {
		t.Fatalf("Email: %v", err)
	}
	if got != "home@example.com" {
		t.Fatalf("Email = %q, want the home file's value", got)
	}
}

// Without CLAUDE_CONFIG_DIR the first path is HOME/.claude/.claude.json —
// the config directory's default, never a hardcoded ~/.claude — and the
// home file is still the second.
func TestEmailWithoutTheConfigDirVariable(t *testing.T) {
	_, home := dirs(t)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	defaultDir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(defaultDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeConfig(t, defaultDir, withAccount("default-dir@example.com"))
	writeConfig(t, home, withAccount("home@example.com"))

	got, err := account.Email(os.Environ())
	if err != nil {
		t.Fatalf("Email: %v", err)
	}
	if got != "default-dir@example.com" {
		t.Fatalf("Email = %q, want the default config directory's value", got)
	}
}

// Every way the read can come up empty. Each one answers "" — the label
// simply is not defaulted — with a diagnostic error and no failure.
func TestEmailIsEmptyWhenThereIsNothingToRead(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"malformed JSON", `{"oauthAccount":{"emailAddress":"a@b.c"`},
		{"not an object", `["oauthAccount"]`},
		{"no oauthAccount", `{"numStartups":41}`},
		{"oauthAccount is null", `{"oauthAccount":null}`},
		{"oauthAccount is a string", `{"oauthAccount":"alice@example.com"}`},
		{"no emailAddress", `{"oauthAccount":{"accountUuid":"u-1"}}`},
		{"empty emailAddress", `{"oauthAccount":{"emailAddress":""}}`},
		{"whitespace emailAddress", `{"oauthAccount":{"emailAddress":"   "}}`},
		{"emailAddress is a number", `{"oauthAccount":{"emailAddress":42}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, _ := dirs(t)
			writeConfig(t, cfg, c.body)
			got, err := account.Email(os.Environ())
			if got != "" {
				t.Fatalf("Email = %q, want \"\"", got)
			}
			if err == nil {
				t.Fatal("Email returned no diagnostic for an empty answer")
			}
		})
	}
}

// A missing file is the ordinary case on a machine that has never signed
// in, and it is reported as such so a caller can tell it from a corrupt
// file in a debug line — as ErrNoFile, never as the *fs.PathError
// os.Open returns, whose text carries the member's home directory.
func TestEmailMissingFile(t *testing.T) {
	cfg, home := dirs(t)
	got, err := account.Email(os.Environ())
	if got != "" {
		t.Fatalf("Email = %q, want \"\"", got)
	}
	if !errors.Is(err, account.ErrNoFile) {
		t.Fatalf("err = %v, want ErrNoFile", err)
	}
	noPathIn(t, err, cfg, home)
}

// noPathIn asserts that a diagnostic names none of the member's
// directories. The caller writes it to a debug line as it stands, and the
// redacting logger redacts registered tokens and the secret key list, not
// paths — so fixed text is the only thing that keeps a home directory out
// of a log.
func noPathIn(t *testing.T, err error, dirs ...string) {
	t.Helper()
	for _, dir := range dirs {
		if strings.Contains(err.Error(), dir) {
			t.Fatalf("err = %q leaks the path %q", err, dir)
		}
	}
}

// An unreadable file (mode 0000, and a directory where the file should be)
// is a read failure, not a panic and not a refusal.
func TestEmailUnreadableFile(t *testing.T) {
	t.Run("mode 0000", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file regardless of its mode")
		}
		cfg, home := dirs(t)
		path := writeConfig(t, cfg, withAccount("alice@example.com"))
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		got, err := account.Email(os.Environ())
		if got != "" || err == nil {
			t.Fatalf("Email = %q, %v; want \"\" and a diagnostic", got, err)
		}
		if !errors.Is(err, account.ErrUnreadable) {
			t.Fatalf("err = %v, want ErrUnreadable", err)
		}
		noPathIn(t, err, cfg, home)
	})
	t.Run("a directory in its place", func(t *testing.T) {
		cfg, home := dirs(t)
		if err := os.Mkdir(filepath.Join(cfg, account.FileName), 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := account.Email(os.Environ())
		if got != "" || err == nil {
			t.Fatalf("Email = %q, %v; want \"\" and a diagnostic", got, err)
		}
		noPathIn(t, err, cfg, home)
	})
}

// The value is Claude Code's to change, so it leaves the package through
// protocol.SanitizeLabel: control characters are gone and the label is
// capped at MaxHumanLabelChars.
func TestEmailIsSanitised(t *testing.T) {
	t.Run("control characters", func(t *testing.T) {
		cfg, _ := dirs(t)
		writeConfig(t, cfg, controlCharacterDoc(t))
		got, err := account.Email(os.Environ())
		if err != nil {
			t.Fatalf("Email: %v", err)
		}
		if got != protocol.SanitizeLabel(got) {
			t.Fatalf("Email = %q, which is not already sanitised", got)
		}
		// The sanitiser's rule 1 keeps \n and \t and strips every other
		// C0/C1 control; the roster folds the rest onto one line
		// (commands.oneLine). What must not survive is the BEL, the ESC
		// that starts a terminal escape sequence, and the NUL.
		for _, r := range got {
			if (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f {
				t.Fatalf("Email = %q carries control character %#U", got, r)
			}
		}
	})
	t.Run("capped", func(t *testing.T) {
		cfg, _ := dirs(t)
		long := strings.Repeat("a", protocol.MaxHumanLabelChars+50) + "@example.com"
		writeConfig(t, cfg, `{"oauthAccount":{"emailAddress":"`+long+`"}}`)
		got, err := account.Email(os.Environ())
		if err != nil {
			t.Fatalf("Email: %v", err)
		}
		if n := len([]rune(got)); n != protocol.MaxHumanLabelChars {
			t.Fatalf("Email is %d code points, want the %d cap", n, protocol.MaxHumanLabelChars)
		}
	})
}

// With neither variable there is nowhere to look, and the reader says so
// rather than guessing a path.
func TestEmailWithoutAnyDirectory(t *testing.T) {
	got, err := account.Email([]string{"PATH=/usr/bin"})
	if got != "" {
		t.Fatalf("Email = %q, want \"\"", got)
	}
	if !errors.Is(err, account.ErrNoConfigDir) {
		t.Fatalf("err = %v, want ErrNoConfigDir", err)
	}
}

// Paths names the two files and nothing else — above all never a
// sessions/*.key peer auth key, which this package must never open.
func TestPathsNamesTheTwoFiles(t *testing.T) {
	cfg, home := dirs(t)
	got := account.Paths(os.Environ())
	want := []string{filepath.Join(cfg, account.FileName), filepath.Join(home, account.FileName)}
	if len(got) != len(want) {
		t.Fatalf("Paths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Paths = %v, want %v", got, want)
		}
		if strings.Contains(got[i], "sessions") || strings.HasSuffix(got[i], ".key") {
			t.Fatalf("Paths names %q, which is not a config file", got[i])
		}
	}
}

// Reading never writes: the tree is byte-identical afterwards, and a
// directory with no file in it stays empty.
func TestEmailWritesNothing(t *testing.T) {
	cfg, home := dirs(t)
	writeConfig(t, cfg, withAccount("alice@example.com"))
	before := listing(t, filepath.Dir(cfg))
	if _, err := account.Email(os.Environ()); err != nil {
		t.Fatalf("Email: %v", err)
	}
	if after := listing(t, filepath.Dir(cfg)); after != before {
		t.Fatalf("the tree changed:\nbefore %s\nafter  %s", before, after)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Fatalf("home = %v, %v; want it untouched", entries, err)
	}
}

// listing is a stable rendering of every file under root with its size.
func listing(t *testing.T, root string) string {
	t.Helper()
	var b strings.Builder
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, ierr := d.Info()
		if ierr != nil {
			return ierr
		}
		b.WriteString(p + ":" + info.Mode().String() + ":" + strconv.FormatInt(info.Size(), 10) + "\n")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return b.String()
}
