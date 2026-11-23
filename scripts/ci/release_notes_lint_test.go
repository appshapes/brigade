//go:build !windows

package ci_test

import (
	"errors"
	"fmt"
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
	"## Assets\n\n- brigade_0.5.1_darwin_arm64\n- brigade_0.5.1_linux_amd64\n- brigade_0.5.1_windows_amd64.exe\n- checksums.txt\n"

// lint runs the script on one release's notes with the given lists and
// returns exit status and combined output.
func lint(t *testing.T, notes, commands, assets, skills string) (int, string) {
	t.Helper()
	return lintKind(t, "", notes, commands, assets, skills)
}

// lintKind is lint with BRIGADE_NOTES_KIND set: "" for one release's notes,
// "email" for the release-notes email of send-release-notes.yml, which is
// read through the email's renderer (card 73), built here.
func lintKind(t *testing.T, kind, notes, commands, assets, skills string) (int, string) {
	t.Helper()
	var env []string
	if kind == "email" {
		env = append(env, "BRIGADE_EMAIL_RENDERER="+testutil.Build(t, "./cmd/brigade-release-email"))
	}
	return lintWith(t, kind, notes, commands, assets, skills, env...)
}

// lintWith is lintKind with the environment spelled out: the email kind
// without a renderer is a case of its own. The changelog the script reads is
// plainChangelog, in a file of the test's own, unless env names another.
func lintWith(t *testing.T, kind, notes, commands, assets, skills string, env ...string) (int, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(path, []byte(notes), 0o600); err != nil {
		t.Fatal(err)
	}
	env = append([]string{"BRIGADE_NOTES_CHANGELOG=" + changelogFile(t, plainChangelog)}, env...)
	//nolint:gosec // G204: a fixed script path — the repository's own — and a file under this test's t.TempDir()
	cmd := exec.CommandContext(t.Context(), "sh", filepath.Join(testutil.RepoRoot(t), "scripts", "ci", "release-notes-lint.sh"), path, "v0.5.1")
	cmd.Dir = testutil.RepoRoot(t)
	cmd.Env = append(os.Environ(),
		"BRIGADE_NOTES_COMMANDS="+commands,
		"BRIGADE_NOTES_ASSETS="+assets,
		"BRIGADE_NOTES_SKILLS="+skills,
		"BRIGADE_NOTES_KIND="+kind,
		"BRIGADE_EMAIL_RENDERER=",
	)
	cmd.Env = append(cmd.Env, env...)
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
	assets   = "brigade_0.5.1_darwin_arm64 brigade_0.5.1_linux_amd64 brigade_0.5.1_windows_amd64.exe checksums.txt"
	skills   = "join sessions setup team-messaging update"
)

func TestReleaseNotesLintPassesGoodNotes(t *testing.T) {
	t.Parallel()
	code, out := lint(t, goodNotes, commands, assets, skills)
	if code != 0 || !strings.Contains(out, "release-notes-lint: clean") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"ok: /brigade:update exists", "ok: brigade sessions is a command", "ok: asset checksums.txt shipped", "ok: asset brigade_0.5.1_windows_amd64.exe shipped", "ok: the notes name 0.5.1"} {
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
		{"a skill that does not exist", strings.Replace(goodNotes, "`/brigade:update`", "`/brigade:update`, then `/brigade:no-such-skill`", 1), commands, assets, "FAIL: /brigade:no-such-skill is not a skill"},
		{"a CLI command that does not exist", strings.Replace(goodNotes, "`brigade sessions`", "`brigade roster`", 1), commands, assets, "FAIL: brigade roster is not a command"},
		{"a fenced command that does not exist", strings.Replace(goodNotes, "brigade sessions --json", "brigade roster --json", 1), commands, assets, "FAIL: brigade roster is not a command"},
		{"an asset that did not ship", strings.Replace(goodNotes, "- checksums.txt\n", "- checksums.txt\n- brigade_0.5.1_windows_arm64.exe\n", 1), commands, assets, "FAIL: asset brigade_0.5.1_windows_arm64.exe is named but did not ship"},
		{"a Windows asset named without its .exe", strings.Replace(goodNotes, "- brigade_0.5.1_windows_amd64.exe\n", "- brigade_0.5.1_windows_amd64\n", 1), commands, assets, "FAIL: asset brigade_0.5.1_windows_amd64 is named but did not ship"},
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

// absentSkill returns a /brigade:<name> this checkout does not have, so the
// test cannot go red because someone added a skill by that name. It did once:
// the name written here was `sessions`, and P11-6 shipped that skill, turning
// the refusal below into a pass. A literal is the trap; deriving the name from
// the tree removes the class rather than relocating it to the next plausible
// alias (`roster`, which this repository's own prose already uses for the
// roster feature, would have been exactly that).
func absentSkill(t *testing.T) string {
	t.Helper()
	root := testutil.RepoRoot(t)
	for i := 0; ; i++ {
		name := "no-such-skill"
		if i > 0 {
			name = fmt.Sprintf("no-such-skill-%d", i)
		}
		if _, err := os.Stat(filepath.Join(root, "plugin", "skills", name)); err == nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(root, "plugin", "commands", name+".md")); err == nil {
			continue
		}
		return name
	}
}

// TestReleaseNotesLintReadsTheSkillsFromThePluginTree: with no
// BRIGADE_NOTES_SKILLS the real plugin tree is the authority, and a name the
// tree does not carry is refused from the tree itself.
func TestReleaseNotesLintReadsTheSkillsFromThePluginTree(t *testing.T) {
	t.Parallel()
	code, out := lint(t, goodNotes, commands, assets, "")
	if code != 0 {
		t.Fatalf("the good notes failed against the plugin tree: exit %d\n%s", code, out)
	}
	absent := absentSkill(t)
	code, out = lint(t, strings.Replace(goodNotes, "`/brigade:update`", "`/brigade:"+absent+"`", 1), commands, assets, "")
	if code != 1 || !strings.Contains(out, "FAIL: /brigade:"+absent+" is not a skill") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
}

// The release-notes email (send-release-notes.yml, card 43) goes through the
// same script with BRIGADE_NOTES_KIND=email: the checks of one release's
// notes minus the assets, plus two of its own — nobody's address, and no
// card or plan row. goodEmail names no asset and is linted with no asset
// list, which the notes kind refuses ("no asset list", above).
const goodEmail = "# Brigade 0.5.0 to 0.5.1\n\n" +
	"`brigade sessions` shows which model each session is running.\n\n" +
	"## Update\n\nSay `/brigade:update`, then run `/reload-plugins`.\n"

func TestReleaseNotesLintPassesAGoodEmail(t *testing.T) {
	t.Parallel()
	code, out := lintKind(t, "email", goodEmail, commands, "", skills)
	if code != 0 || !strings.Contains(out, "release-notes-lint: clean") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	for _, want := range []string{"ok: the notes name 0.5.1", "ok: /brigade:update exists", "ok: no address", "ok: no card, work item or plan row", "ok: the draft renders as the email"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in:\n%s", want, out)
		}
	}
	// A work item is also a thing of the product: an email may show one being moved without pointing into
	// this repository's tracker (work item 265).
	code, out = lintKind(t, "email", goodEmail+"\nMove work item 41 to Doing, and the team sees it on the board.\n", commands, "", skills)
	if code != 0 {
		t.Fatalf("a sentence about the board's work items was refused: exit %d\n%s", code, out)
	}
	// The install command carries an `@` and is not an address.
	code, out = lintKind(t, "email", goodEmail+"\nInstall with `/plugin install brigade@brigade`.\n", commands, "", skills)
	if code != 0 {
		t.Fatalf("the install command was refused: exit %d\n%s", code, out)
	}
}

func TestReleaseNotesLintEmailRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		notes string
		want  string
	}{
		{"an address", goodEmail + "\nWrite to someone@example.com.\n", "FAIL: the email carries an address"},
		{"a card", strings.Replace(goodEmail, "is running.", "is running (card 34).", 1), "FAIL: the email names a card, a work item or a plan row"},
		// A work item's number is what a card's was (work item 265), where it points into the tracker: in
		// brackets, as the changelog cites one, or with a #.
		{"a work item in brackets", strings.Replace(goodEmail, "is running.", "is running (work item 272).", 1), "FAIL: the email names a card, a work item or a plan row"},
		{"work items in brackets", strings.Replace(goodEmail, "is running.", "is running (work-items 272 and 275).", 1), "FAIL: the email names a card, a work item or a plan row"},
		{"a work item with a #", strings.Replace(goodEmail, "is running.", "is running, see work item #272.", 1), "FAIL: the email names a card, a work item or a plan row"},
		{"a plan row", strings.Replace(goodEmail, "is running.", "is running (P23-1).", 1), "FAIL: the email names a card, a work item or a plan row"},
		{"a skill that does not exist", strings.Replace(goodEmail, "`/brigade:update`", "`/brigade:no-such-skill`", 1), "FAIL: /brigade:no-such-skill is not a skill"},
		{"the newest version unnamed", strings.ReplaceAll(goodEmail, "0.5.1", "0.5.0"), "FAIL: the notes never name version 0.5.1"},
		// The renderer's reading (card 73): what the layout cannot show is a finding, with its line.
		{"a table", goodEmail + "\n| a | b |\n|---|---|\n", "FAIL: the draft does not render as the email"},
		{"a table, by line", goodEmail + "\n| a | b |\n|---|---|\n", "line 9: a table is not supported"},
		{"an image the checkout lacks", goodEmail + "\n![The roster](docs/email/no-such-file.png)\n", "docs/email/no-such-file.png does not exist in the checkout"},
		{"a relative link", strings.Replace(goodEmail, "`/brigade:update`", "[the skill](plugin/skills/update/SKILL.md)", 1), "is not an absolute https URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, out := lintKind(t, "email", tc.notes, commands, "", skills)
			if code != 1 {
				t.Fatalf("exit %d, want 1:\n%s", code, out)
			}
			if !strings.Contains(out, tc.want) {
				t.Fatalf("missing %q in:\n%s", tc.want, out)
			}
		})
	}
}

// The email kind needs the renderer, and says so rather than skipping the
// check; one release's notes never need it.
func TestReleaseNotesLintEmailNeedsTheRenderer(t *testing.T) {
	t.Parallel()
	code, out := lintWith(t, "email", goodEmail, commands, "", skills)
	if code != 1 || !strings.Contains(out, "FAIL: BRIGADE_EMAIL_RENDERER is unset") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if code, out := lintWith(t, "", goodNotes, commands, assets, skills); code != 0 {
		t.Fatalf("one release's notes were refused without a renderer: exit %d\n%s", code, out)
	}
}

// TestReleaseNotesLintKindsDiffer: the two checks of an email do not reach
// one release's notes, whose changelog wording names cards, and an unknown
// kind is refused rather than read as either.
func TestReleaseNotesLintKindsDiffer(t *testing.T) {
	t.Parallel()
	carded := strings.Replace(goodNotes, "fix release", "fix release (card 42)", 1)
	if code, out := lint(t, carded, commands, assets, skills); code != 0 {
		t.Fatalf("one release's notes were refused for naming a card: exit %d\n%s", code, out)
	}
	if code, out := lintKind(t, "email", carded, commands, "", skills); code != 1 || !strings.Contains(out, "FAIL: the email names a card, a work item or a plan row") {
		t.Fatalf("an email naming a card passed: exit %d\n%s", code, out)
	}
	if code, out := lintKind(t, "digest", goodEmail, commands, "", skills); code != 1 || !strings.Contains(out, "FAIL: BRIGADE_NOTES_KIND is neither unset nor email") {
		t.Fatalf("an unknown kind was accepted: exit %d\n%s", code, out)
	}
}

// The changelog the script reads for the version's section (work item 418).
// plainChangelog's section for 0.5.1 says nothing of who must act, as every
// section up to 0.57.0; the sections around it do, and are not the version's.
const plainChangelog = "# Changelog\n\n" +
	"## [0.5.10] — 2026-01-02\n\n" + whoMustAct + "### Fixed\n\n- A later fix.\n\n" +
	"## [0.5.1] — 2026-01-01\n\n### Fixed\n\n- The fix.\n\n" +
	"## [0.5.0] — 2025-12-31\n\n" + whoMustAct + "### Added\n\n- An earlier thing.\n"

// actingChangelog's section for 0.5.1 opens with who must act, wrapped as a
// changelog wraps it, and one line points to an entry as "below".
const actingChangelog = "# Changelog\n\n## [Unreleased]\n\n" +
	"## [0.5.1] — 2026-01-01\n\n" + whoMustAct + "### Changed\n\n- **The label is the account's** (work item 1).\n\n" +
	"## [0.5.0] — 2025-12-31\n\n### Added\n\n- An earlier thing.\n"

const whoMustAct = "**Who must act**\n\n" +
	"- **Team Member:** on Codex CLI, set `label` in the options file\n" +
	"  before you update (\"The label is the account's\", below;\n" +
	"  [Choose your label](https://example.com/your-label.md)).\n" +
	"  Everyone else: nothing.\n" +
	"- **Team Admin:** nothing.\n" +
	"- **Project Owner:** stop the server\n  before the update.\n\n"

// The lines as the notes carry them: each bullet on one line.
const (
	actingHead   = "**Who must act**"
	actingMember = "- **Team Member:** on Codex CLI, set `label` in the options file before you update (\"The label is the account's\", below; [Choose your label](https://example.com/your-label.md)). Everyone else: nothing."
	actingAdmin  = "- **Team Admin:** nothing."
	actingOwner  = "- **Project Owner:** stop the server before the update."
)

// actingNotes is goodNotes with lines put after its first line, a blank line
// after each of them where blank is true.
func actingNotes(blank bool, lines ...string) string {
	first, rest, _ := strings.Cut(goodNotes, "\n\n")
	sep := "\n"
	if blank {
		sep = "\n\n"
	}
	return first + "\n\n" + strings.Join(lines, sep) + "\n\n" + rest
}

func changelogFile(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "CHANGELOG.md")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// lintPointer is the words the script lets a line say for "below": its own
// `pointer`, read from the script.
func lintPointer(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "scripts", "ci", "release-notes-lint.sh"))
	if err != nil {
		t.Fatal(err)
	}
	_, after, found := strings.Cut(string(data), "\npointer='")
	pointer, _, closed := strings.Cut(after, "'\n")
	if !found || !closed || pointer == "" {
		t.Fatal("release-notes-lint.sh has no line that begins pointer='")
	}
	return pointer
}

// TestReleaseNotesLintHoldsTheNotesToWhoMustAct: where the version's section
// of the changelog opens with who must act, the notes carry those four lines
// right after their first, in the changelog's words and each on one line.
func TestReleaseNotesLintHoldsTheNotesToWhoMustAct(t *testing.T) {
	t.Parallel()
	pointer := lintPointer(t)
	pointed := strings.Replace(actingMember, "below", pointer, 1)
	wrapped := strings.Replace(actingMember, " before you update", "\n  before you update", 1)
	const refusal = "FAIL: the notes do not carry the changelog's who must act lines"
	// The lines pass, and the notes are refused for what the case cut off after them.
	const refusalOfAssets = "FAIL: shipped asset checksums.txt is not named in the notes"
	first, _, _ := strings.Cut(goodNotes, "\n\n")
	for _, tc := range []struct {
		name  string
		notes string
		want  string // empty: the notes pass
	}{
		{"the four lines after the first", actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner), ""},
		{"with below pointing to the changelog", actingNotes(false, actingHead+"\n", pointed, actingAdmin, actingOwner), ""},
		{"with no blank line under the heading", actingNotes(false, actingHead, actingMember, actingAdmin, actingOwner), ""},
		{"with a blank line between every two", actingNotes(true, actingHead, actingMember, actingAdmin, actingOwner), ""},
		{"with nothing after the lines", first + "\n\n" + strings.Join([]string{actingHead, actingMember, actingAdmin, actingOwner}, "\n") + "\n", refusalOfAssets},
		{"with spaces at a line's end", actingNotes(false, actingHead+"  \n", actingMember+" ", actingAdmin, actingOwner+"\t"), ""},
		{"none of them", goodNotes, refusal},
		{"after a paragraph of the notes", strings.Replace(goodNotes, "## Assets", strings.Join([]string{actingHead, "", actingMember, actingAdmin, actingOwner, "", "## Assets"}, "\n"), 1), refusal},
		{"before the first line", strings.Join([]string{actingHead, "", actingMember, actingAdmin, actingOwner, "", goodNotes}, "\n"), refusal},
		{"a line left wrapped", actingNotes(false, actingHead+"\n", wrapped, actingAdmin, actingOwner), refusal},
		{"a line in other words", actingNotes(false, actingHead+"\n", actingMember, actingAdmin, strings.Replace(actingOwner, "stop the server", "restart the server", 1)), refusal},
		{"a line cut short", actingNotes(false, actingHead+"\n", strings.Replace(actingMember, " Everyone else: nothing.", "", 1), actingAdmin, actingOwner), refusal},
		{"a line that says more", actingNotes(false, actingHead+"\n", actingMember, actingAdmin+" Unless you run a gateway.", actingOwner), refusal},
		{"two lines the other way round", actingNotes(false, actingHead+"\n", actingMember, actingOwner, actingAdmin), refusal},
		{"a line left out", actingNotes(false, actingHead+"\n", actingMember, actingOwner), refusal},
		{"the heading left out", actingNotes(false, actingMember, actingAdmin, actingOwner), refusal},
		{"the heading in other words", actingNotes(false, "## Who must act\n", actingMember, actingAdmin, actingOwner), refusal},
		{"below pointing elsewhere", actingNotes(false, actingHead+"\n", strings.Replace(actingMember, "below", "in the changelog", 1), actingAdmin, actingOwner), refusal},
		{"a line between the first and the lines", actingNotes(false, "Read this first.\n", actingHead+"\n", actingMember, actingAdmin, actingOwner), refusal},
		// What a release page would run together: the heading into the first sentence, the text into the last bullet.
		{"no blank line before the lines", strings.Replace(actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner), "\n\n"+actingHead, "\n"+actingHead, 1), refusal},
		{"no blank line after the lines", strings.Replace(actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner), actingOwner+"\n\n", actingOwner+"\n", 1), refusal},
		// The first line is the whole first sentence: lines put after the first of two would strand the second.
		{"the first sentence on two lines", strings.Replace(actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner), "is a fix release.", "is a fix\nrelease.", 1), refusal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			code, out := lintWith(t, "", tc.notes, commands, assets, skills, "BRIGADE_NOTES_CHANGELOG="+changelogFile(t, actingChangelog))
			if tc.want == "" {
				if code != 0 || !strings.Contains(out, "ok: the notes carry the changelog's who must act lines after their first line") {
					t.Fatalf("exit %d, want 0 and the ok line:\n%s", code, out)
				}
				return
			}
			if code != 1 || !strings.Contains(out, tc.want) {
				t.Fatalf("exit %d, want 1 and %q:\n%s", code, tc.want, out)
			}
			if tc.want != refusal {
				if !strings.Contains(out, "ok: the notes carry the changelog's who must act lines after their first line") {
					t.Fatalf("the lines were refused:\n%s", out)
				}
				return
			}
			// What the writer's fix cycle copies: the four lines, each whole on one line.
			for _, line := range []string{actingHead, actingMember, actingAdmin, actingOwner} {
				if !strings.Contains(out, "want: "+line+"\n") {
					t.Errorf("the finding does not print the line it wants, %q:\n%s", line, out)
				}
			}
			if !strings.Contains(out, pointer) {
				t.Errorf("the finding does not say what below may read:\n%s", out)
			}
		})
	}
}

// TestReleaseNotesLintAsksNoLinesOfASectionWithoutThem: a section that does
// not open with who must act asks nothing, whatever the sections around it
// open with, and an email is never held to the lines.
func TestReleaseNotesLintAsksNoLinesOfASectionWithoutThem(t *testing.T) {
	t.Parallel()
	code, out := lint(t, goodNotes, commands, assets, skills)
	if code != 0 || !strings.Contains(out, "ok: the changelog's section for 0.5.1 does not open with who must act") {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	// Who must act further down the section is an entry's own words, not the section's opening lines.
	late := strings.Replace(actingChangelog, whoMustAct+"### Changed\n\n", "### Changed\n\n"+whoMustAct, 1)
	if late == actingChangelog {
		t.Fatal("the fixture did not change")
	}
	if code, out := lintWith(t, "", goodNotes, commands, assets, skills, "BRIGADE_NOTES_CHANGELOG="+changelogFile(t, late)); code != 0 {
		t.Fatalf("lines that do not open the section were asked for: exit %d\n%s", code, out)
	}
	acting := "BRIGADE_NOTES_CHANGELOG=" + changelogFile(t, actingChangelog)
	env := []string{acting, "BRIGADE_EMAIL_RENDERER=" + testutil.Build(t, "./cmd/brigade-release-email")}
	if code, out := lintWith(t, "email", goodEmail, commands, "", skills, env...); code != 0 || strings.Contains(out, "who must act") {
		t.Fatalf("an email was held to the lines: exit %d\n%s", code, out)
	}
}

// TestReleaseNotesLintNeedsTheVersionsSection: with no changelog, or none
// that has the version's section, the script cannot know whether lines are
// asked for, and says so rather than passing the notes.
func TestReleaseNotesLintNeedsTheVersionsSection(t *testing.T) {
	t.Parallel()
	for want, changelog := range map[string]string{
		"has no section for 0.5.1": changelogFile(t, strings.Replace(actingChangelog, "## [0.5.1]", "## [0.5.11]", 1)),
		"cannot be read":           filepath.Join(t.TempDir(), "CHANGELOG.md"),
	} {
		code, out := lintWith(t, "", actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner), commands, assets, skills, "BRIGADE_NOTES_CHANGELOG="+changelog)
		if code != 1 || !strings.Contains(out, want+": what the notes are held to is not there") {
			t.Errorf("want %q: exit %d\n%s", want, code, out)
		}
	}
}

// TestReleaseNotesLintReadsTheChangelogsLinesAsWritten: spaces at a line's
// end and a file with CRLF endings do not switch the check off, which a
// heading compared byte for byte would; and a line of the changelog that
// itself says what "below" may read is copied as it is.
func TestReleaseNotesLintReadsTheChangelogsLinesAsWritten(t *testing.T) {
	t.Parallel()
	carried := actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner)
	for name, changelog := range map[string]string{
		"a space after the heading": strings.Replace(actingChangelog, "**Who must act**\n", "**Who must act** \n", 1),
		"CRLF endings":              strings.ReplaceAll(actingChangelog, "\n", "\r\n"),
	} {
		env := "BRIGADE_NOTES_CHANGELOG=" + changelogFile(t, changelog)
		if code, out := lintWith(t, "", carried, commands, assets, skills, env); code != 0 {
			t.Errorf("%s: notes that carry the lines were refused: exit %d\n%s", name, code, out)
		}
		if code, out := lintWith(t, "", goodNotes, commands, assets, skills, env); code != 1 || !strings.Contains(out, "FAIL: the notes do not carry the changelog's who must act lines") {
			t.Errorf("%s: notes without the lines passed: exit %d\n%s", name, code, out)
		}
	}
	pointer := lintPointer(t)
	own := strings.Replace(actingChangelog, "- **Team Admin:** nothing.", "- **Team Admin:** read the entry "+pointer+".", 1)
	notes := actingNotes(false, actingHead+"\n", actingMember, "- **Team Admin:** read the entry "+pointer+".", actingOwner)
	if code, out := lintWith(t, "", notes, commands, assets, skills, "BRIGADE_NOTES_CHANGELOG="+changelogFile(t, own)); code != 0 {
		t.Errorf("a line copied word for word was refused: exit %d\n%s", code, out)
	}
}

// TestReleaseNotesLintRefusesOpeningLinesThatAreNotTheThree: a section that
// opens with who must act and then not with its three lines in their order is
// the changelog's fault, said as that, whatever the notes carry.
func TestReleaseNotesLintRefusesOpeningLinesThatAreNotTheThree(t *testing.T) {
	t.Parallel()
	for name, changed := range map[string]string{
		"a line missing":      strings.Replace(actingChangelog, "- **Team Admin:** nothing.\n", "", 1),
		"another order":       strings.Replace(strings.Replace(actingChangelog, "- **Team Admin:**", "- **Project Owner:**", 1), "- **Project Owner:** stop", "- **Team Admin:** stop", 1),
		"a fourth line":       strings.Replace(actingChangelog, "- **Team Admin:** nothing.\n", "- **Team Admin:** nothing.\n- **Operator:** nothing.\n", 1),
		"a line with no word": strings.Replace(actingChangelog, "- **Team Admin:** nothing.\n", "- **Team Admin:**\n", 1),
		"no line at all":      strings.Replace(actingChangelog, whoMustAct, "**Who must act**\n\n", 1),
		"another heading":     strings.Replace(actingChangelog, "**Who must act**\n", "**Who must act:**\n", 1),
		"a heading of a kind": strings.Replace(actingChangelog, "**Who must act**\n", "### Who must act\n", 1),
	} {
		if changed == actingChangelog {
			t.Fatalf("%s: the fixture did not change", name)
		}
		notes := actingNotes(false, actingHead+"\n", actingMember, actingAdmin, actingOwner)
		code, out := lintWith(t, "", notes, commands, assets, skills, "BRIGADE_NOTES_CHANGELOG="+changelogFile(t, changed))
		if code != 1 || !strings.Contains(out, "mend the changelog") {
			t.Errorf("%s: exit %d\n%s", name, code, out)
		}
	}
}

// TestTheNotesWorkflowSaysTheOrderOnce: the order of the notes is one text,
// the job's NOTES_ORDER, and each of the four agent steps of
// release-notes.yml carries it into its prompt. A prompt with an order of its
// own is how a reviewer and a writer come to disagree with no cycle left to
// settle it, and then no notes are published (work item 418). What the order
// lets a line say for "below" is what the script accepts, letter for letter.
func TestTheNotesWorkflowSaysTheOrderOnce(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github", "workflows", "release-notes.yml"))
	if err != nil {
		t.Fatal(err)
	}
	// Comments are no part of what a runner or an agent reads.
	var kept []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			kept = append(kept, line)
		}
	}
	workflow := strings.Join(kept, "\n")
	const carried = "${{ env.NOTES_ORDER }}"

	_, after, found := strings.Cut(workflow, "\n      NOTES_ORDER: |\n")
	order, _, ended := strings.Cut(after, "\n    steps:\n")
	if !found || !ended {
		t.Fatal("release-notes.yml's job has no NOTES_ORDER block as the last of its env")
	}
	at := 0
	for _, want := range []string{"The first line", "`**Who must act**`", "`- **Team Member:**`", "`- **Team Admin:**`", "`- **Project Owner:**`", "`" + lintPointer(t) + "`", "USER of the plugin", "ADAPTER AUTHOR", "The assets"} {
		next := strings.Index(order[at:], want)
		if next < 0 {
			t.Fatalf("NOTES_ORDER does not say %q after what comes before it:\n%s", want, order)
		}
		at += next + len(want)
	}

	steps := strings.Split(workflow, "\n      - ")
	for _, id := range []string{"draft1", "review1", "draft2", "review2"} {
		var prompt string
		for _, step := range steps {
			if strings.Contains(step, "\n        id: "+id+"\n") {
				_, text, _ := strings.Cut(step, "\n          prompt: |\n")
				prompt, _, _ = strings.Cut(text, "\n          settings: |\n")
			}
		}
		if got := strings.Count(prompt, carried); got != 1 {
			t.Errorf("the prompt of step %s carries NOTES_ORDER %d times, want once", id, got)
		}
		// An order of its own, in the words the prompts had before.
		for _, own := range []string{"USER", "ADAPTER AUTHOR"} {
			if strings.Contains(prompt, own) {
				t.Errorf("the prompt of step %s says %q itself: the order is NOTES_ORDER's to say", id, own)
			}
		}
	}
	if got := strings.Count(workflow, carried); got != 4 {
		t.Errorf("release-notes.yml carries NOTES_ORDER %d times outside its comments, want 4: one in each agent step's prompt", got)
	}
}
