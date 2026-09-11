package ci_test

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// release-notes-lint.sh is the deterministic half of the release-notes
// gate: a drafted set of notes may not name a skill, a CLI command or an
// asset that does not exist, must name the released version and every
// shipped asset, and may not carry a placeholder, "Unreleased" or
// anything secret-shaped. Each refusal below has a passing control beside
// it, and the good notes are the control for all of them.

const goodNotes = "Brigade 0.5.1 is a fix release.\n\n" +
	"Update with `/brigade:update`, then run `/reload-plugins` and ask the session to run `brigade sessions`.\n\n" +
	"```\nbrigade sessions --json\n```\n\n" +
	"## Assets\n\n- brigade_0.5.1_darwin_arm64\n- brigade_0.5.1_linux_amd64\n- checksums.txt\n"

// lint runs the script on notes with the given lists and returns exit
// status and combined output.
func lint(t *testing.T, notes, commands, assets, skills string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G204: a fixed script path — the repository's own — and a file under this test's t.TempDir()
	cmd := exec.CommandContext(t.Context(), "sh", filepath.Join(testutil.RepoRoot(t), "scripts", "ci", "release-notes-lint.sh"), path, "v0.5.1")
	cmd.Dir = testutil.RepoRoot(t)
	cmd.Env = append(os.Environ(),
		"BRIGADE_NOTES_COMMANDS="+commands,
		"BRIGADE_NOTES_ASSETS="+assets,
		"BRIGADE_NOTES_SKILLS="+skills,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("run: %v\n%s", err, out)
		}
		code = ee.ExitCode()
	}
	return code, string(out)
}

const (
	commands = "version help sessions send whoami team inbox"
	assets   = "brigade_0.5.1_darwin_arm64 brigade_0.5.1_linux_amd64 checksums.txt"
	skills   = "join sessions setup team-messaging update"
)

func TestReleaseNotesLintPassesGoodNotes(t *testing.T) {
	t.Parallel()
	code, out := lint(t, goodNotes, commands, assets, skills)
	if code != 0 || !strings.Contains(out, "release-notes-lint: clean") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"ok: /brigade:update exists", "ok: brigade sessions is a command", "ok: asset checksums.txt shipped", "ok: the notes name 0.5.1"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
}

func TestReleaseNotesLintRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		notes    string
		commands string
		assets   string
		want     string
	}{
		{"a skill that does not exist", strings.Replace(goodNotes, "`/brigade:update`", "`/brigade:update`, then `/brigade:roster`", 1), commands, assets, "FAIL: /brigade:roster is not a skill"},
		{"a CLI command that does not exist", strings.Replace(goodNotes, "`brigade sessions`", "`brigade roster`", 1), commands, assets, "FAIL: brigade roster is not a command"},
		{"a fenced command that does not exist", strings.Replace(goodNotes, "brigade sessions --json", "brigade roster --json", 1), commands, assets, "FAIL: brigade roster is not a command"},
		{"an asset that did not ship", strings.Replace(goodNotes, "- checksums.txt\n", "- checksums.txt\n- brigade_0.5.1_windows_amd64\n", 1), commands, assets, "FAIL: asset brigade_0.5.1_windows_amd64 is named but did not ship"},
		{"a shipped asset left unnamed", goodNotes, commands, assets + " brigade_0.5.1_linux_arm64", "FAIL: shipped asset brigade_0.5.1_linux_arm64 is not named"},
		{"the wrong version", strings.ReplaceAll(goodNotes, "0.5.1", "0.5.0"), commands, strings.ReplaceAll(assets, "0.5.1", "0.5.0"), "FAIL: the notes never name version 0.5.1"},
		{"Unreleased", strings.Replace(goodNotes, "fix release", "fix release (Unreleased)", 1), commands, assets, "FAIL: the notes say Unreleased"},
		{"a placeholder", goodNotes + "\nTODO: write more\n", commands, assets, "FAIL: the notes carry a placeholder"},
		{"something secret-shaped", goodNotes + "\ntoken sbp_0123456789abcdef0123456789abcdef\n", commands, assets, "FAIL: the notes carry something secret-shaped"},
		{"empty", "\n\n", commands, assets, "FAIL: the notes are empty"},
		{"no command list", goodNotes, "", assets, "FAIL: BRIGADE_NOTES_COMMANDS is unset"},
		{"no asset list", goodNotes, commands, "", "FAIL: BRIGADE_NOTES_ASSETS is unset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, out := lint(t, tc.notes, tc.commands, tc.assets, skills)
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("missing %q in:\n%s", tc.want, out)
			}
		})
	}
}

// TestReleaseNotesLintReadsTheSkillsFromThePluginTree: with no
// BRIGADE_NOTES_SKILLS the real plugin tree is the authority. The name it
// refuses is `roster`, which the plugin has never had and is not planned.
// The v0.5.0 slip itself, `/brigade:sessions`, is now a real skill, so the
// test written against that name started passing the day the skill landed —
// which is how this one was found.
func TestReleaseNotesLintReadsTheSkillsFromThePluginTree(t *testing.T) {
	t.Parallel()
	code, out := lint(t, goodNotes, commands, assets, "")
	if code != 0 {
		t.Fatalf("the good notes failed against the plugin tree: exit %d\n%s", code, out)
	}
	code, out = lint(t, strings.Replace(goodNotes, "`/brigade:update`", "`/brigade:roster`", 1), commands, assets, "")
	if code != 1 || !strings.Contains(out, "FAIL: /brigade:roster is not a skill") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}
