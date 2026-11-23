//go:build !windows

package account

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// hostEnvs is one environment for each place the default label is read
// from: a session of each of the four harnesses, and a terminal. extra is
// appended to every one.
func hostEnvs(home string, extra ...string) map[string][]string {
	envs := map[string][]string{
		"claude code": {config.ClaudePIDVar + "=4242"},
		"muse code":   {config.MuseSessionIDVar + "=muse-1"},
		"codex cli":   {config.CodexThreadIDVar + "=codex-1"},
		"grok build":  {config.GrokSessionIDVar + "=grok-1"},
		"a terminal":  {},
	}
	for name, env := range envs {
		envs[name] = append(append(env, "HOME="+home), extra...)
	}
	return envs
}

// TestTheRuleIsOneOnEveryHost is ADR 0014 (work item 379): where a Claude
// account is on the machine, its email is the default label in a session
// of Claude Code, Muse Code, Codex CLI and Grok Build alike, and at a
// terminal; the `label` option turns it off, or names a text, the same
// way on all of them. Until then Grok Build was given no file to read
// here, and two of the callers left Muse Code and Codex CLI out by rules
// of their own.
func TestTheRuleIsOneOnEveryHost(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	file := filepath.Join(home, FileName)
	if err := os.WriteFile(file, []byte(`{"oauthAccount":{"emailAddress":"member@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range hostEnvs(home) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			// With no CLAUDE_CONFIG_DIR: Claude Code's default directory
			// under HOME, then HOME's own file, which is where an install
			// with no CLAUDE_CONFIG_DIR keeps it.
			if got, want := Paths(env), []string{filepath.Join(home, ".claude", FileName), file}; !slices.Equal(got, want) {
				t.Fatalf("Paths = %v, want %v", got, want)
			}
			for option, want := range map[string]string{"": "member@example.com", config.LabelAccount: "member@example.com", config.LabelNone: "", "Rae on the laptop": "Rae on the laptop"} {
				label, from, err := Label(option, env)
				if label != want {
					t.Errorf("Label(%q) = %q, want %q", option, label, want)
				}
				if account := want == "member@example.com"; account && (from != file || err != nil) || !account && (from != "" || err != nil) {
					t.Errorf("Label(%q) names the file %q with the error %v", option, from, err)
				}
			}
		})
	}
}

// A session that inherited CLAUDE_CONFIG_DIR from the shell it was started
// in reads that profile's file first, whatever its harness: the variable
// is Claude Code's, neither a BRIGADE_* nor an XDG_* one, and a Muse Code,
// Codex CLI or Grok Build session that ignores those still honours it.
func TestEveryHostHonoursAnInheritedClaudeConfigDir(t *testing.T) {
	t.Parallel()
	home, profile := t.TempDir(), t.TempDir()
	if err := os.WriteFile(filepath.Join(home, FileName), []byte(`{"oauthAccount":{"emailAddress":"default@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, FileName), []byte(`{"oauthAccount":{"emailAddress":"profile@example.com"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	for name, env := range hostEnvs(home, config.ClaudeConfigDirVar+"="+profile, "XDG_CONFIG_HOME=/never/read", "BRIGADE_CONFIG_DIR=/never/read") {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if label, from, err := Label("", env); label != "profile@example.com" || from != filepath.Join(profile, FileName) || err != nil {
				t.Fatalf("Label = %q from %q, %v; want the inherited profile's account", label, from, err)
			}
			// `brigade whoami` reads the same files (work item 414).
			if email, from, err := EmailFile(env); email != "profile@example.com" || from != filepath.Join(profile, FileName) || err != nil {
				t.Fatalf("EmailFile = %q from %q, %v", email, from, err)
			}
		})
	}
}

// With no account on the machine every host sends no label and says why,
// in fixed words: a member whose account cannot be read joins unlabelled.
func TestEveryHostSendsNoLabelWhereThereIsNoAccount(t *testing.T) {
	t.Parallel()
	for name, env := range hostEnvs(t.TempDir()) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if label, from, err := Label("", env); label != "" || from != "" || !errors.Is(err, ErrNoFile) {
				t.Fatalf("Label = %q from %q, %v; want no label and ErrNoFile", label, from, err)
			}
		})
	}
}
