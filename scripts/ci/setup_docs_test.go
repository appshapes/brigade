// The setup drift join. Plan 6.9 says `plugin/skills/setup/SKILL.md`, `plugin/README.md` and `docs/setup.md`
// "carry the same text" for the same three procedures, and until now nothing checked it. Three copies is a
// deliberate decision, not an accident: a skill body cannot follow a link, and a marketplace reader may only
// ever see plugin/README.md. What each copy carries differs (docs/setup.md has the reasons, the README has the
// commands plus a link, the skill has the commands with ${CLAUDE_PLUGIN_ROOT} substituted) — but the COMMANDS
// and the bearer-capability sentence must be identical in all three, because a command that drifts is a command
// one of the three readers runs wrong.
//
// The join is deliberately narrow. It pins the five invocation forms a person types and the one sentence that
// tells them what the join secret is worth. It does not pin prose, so a lane may rewrite the reasons in
// docs/setup.md without touching this file; it bites the moment a flag, a profile name or that sentence moves in
// one copy and not the others.
package ci_test

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// The three copies, relative to the repository root.
var setupDocFiles = []string{
	"docs/setup.md",
	"plugin/README.md",
	"plugin/skills/setup/SKILL.md",
}

// pluginBinPrefix is the one difference the join tolerates: each copy names the binary the way its own reader
// reaches it. Everything after the program name must match.
var pluginBinPrefix = regexp.MustCompile(`(\$\{CLAUDE_PLUGIN_ROOT\}|<plugin>)/bin/brigade`)

// whitespaceRun collapses wrapping. These are three hand-wrapped markdown files, so the same sentence sits on
// one line in one copy and across two in another; that is formatting, not drift, and must not fail the join.
var whitespaceRun = regexp.MustCompile(`\s+`)

// The six witnesses. Five are the invocation forms of the three procedures (create, join, leave, uninstall);
// the sixth is the sentence that says what holding the join secret buys, which is the one security claim all
// three copies must make in the same words.
var setupDocWitnesses = []string{
	"brigade profile init --url https://<ref>.supabase.co --key sb_publishable_",
	"brigade team create --prompt --secret-file ~/brigade-<team>.secret",
	"brigade team join --profile default --prompt",
	"brigade team leave --profile default",
	"brigade profile reset --profile default",
	"The secret is a bearer capability: anyone holding it can join and pick any label",
}

// checkSetupDocs asserts every witness in every copy, with the path prefix normalised away.
func checkSetupDocs(r reporter, root string) {
	r.Helper()
	for _, rel := range setupDocFiles {
		text, ok := readText(r, root, rel)
		if !ok {
			continue
		}
		normalised := whitespaceRun.ReplaceAllString(pluginBinPrefix.ReplaceAllString(text, "brigade"), " ")
		for _, witness := range setupDocWitnesses {
			if !strings.Contains(normalised, witness) {
				r.Errorf("%s does not carry %q; the three setup copies (%s) must name the same commands and "+
					"the same bearer-capability sentence (plan 6.9)", rel, witness, strings.Join(setupDocFiles, ", "))
			}
		}
	}
}

// TestSetupDocsAgree runs the join against the committed tree.
func TestSetupDocsAgree(t *testing.T) {
	t.Parallel()
	checkSetupDocs(t, testutil.RepoRoot(t))
}

// copySetupDocs copies the three files into a fresh temporary root, preserving their relative paths.
func copySetupDocs(t *testing.T) string {
	t.Helper()
	src := testutil.RepoRoot(t)
	dst := t.TempDir()
	for _, rel := range setupDocFiles {
		//nolint:gosec // G304: rel is a constant from the table above, under the repository being tested.
		data, err := os.ReadFile(filepath.Join(src, rel))
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		target := filepath.Join(dst, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatalf("creating the directory for %s: %v", rel, err)
		}
		//nolint:gosec // G703: target is filepath.Join(t.TempDir(), a constant).
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
	return dst
}

// replaceOnceIn rewrites the first occurrence of old in one copy, and fails loudly when the target is absent —
// a mutation that changed nothing would make the failure test vacuous.
func replaceOnceIn(rel, old, replacement string) func(t *testing.T, root string) {
	return func(t *testing.T, root string) {
		t.Helper()
		path := filepath.Join(root, filepath.FromSlash(rel))
		//nolint:gosec // G304: path is filepath.Join(t.TempDir(), a constant from the table below).
		f, err := os.Open(path)
		if err != nil {
			t.Fatalf("opening %s: %v", rel, err)
		}
		data, err := io.ReadAll(f)
		_ = f.Close()
		if err != nil {
			t.Fatalf("reading %s: %v", rel, err)
		}
		text := string(data)
		if !strings.Contains(text, old) {
			t.Fatalf("the mutation's target %q is not in %s, so the mutation would prove nothing", old, rel)
		}
		//nolint:gosec // G703: as above.
		if err := os.WriteFile(path, []byte(strings.Replace(text, old, replacement, 1)), 0o600); err != nil {
			t.Fatalf("writing %s: %v", rel, err)
		}
	}
}

var setupDocMutations = []struct {
	name    string
	mutate  func(t *testing.T, root string)
	because string
}{
	{
		"a_setup_doc_changes_a_flag",
		replaceOnceIn("docs/setup.md", "team join --profile default --prompt", "team join --profile default"),
		"a flag dropped in one copy is a command that reader runs wrong",
	},
	{
		"b_plugin_readme_changes_a_flag",
		replaceOnceIn("plugin/README.md", "team create --prompt --secret-file", "team create --prompt --secret-out"),
		"the marketplace reader may see only this copy",
	},
	{
		"c_skill_changes_a_profile_name",
		replaceOnceIn("plugin/skills/setup/SKILL.md", "profile reset --profile default", "profile reset --profile main"),
		"the skill body cannot follow a link, so its commands must be right on their own",
	},
	{
		"d_setup_doc_drops_the_bearer_sentence",
		replaceOnceIn("docs/setup.md",
			"The secret is a bearer capability",
			"The secret lets a member in"),
		"the one security claim all three copies must make in the same words",
	},
	{
		"e_plugin_readme_softens_the_bearer_sentence",
		replaceOnceIn("plugin/README.md",
			"anyone holding it can join and pick any label",
			"anyone holding it can join"),
		"softening it in one copy is exactly the drift this join exists to catch",
	},
	{
		"f_skill_softens_the_bearer_sentence",
		replaceOnceIn("plugin/skills/setup/SKILL.md",
			"The secret is a bearer capability",
			"The secret is a password"),
		"same sentence, third copy",
	},
	{
		"g_a_copy_goes_missing",
		func(t *testing.T, root string) {
			t.Helper()
			if err := os.Remove(filepath.Join(root, filepath.FromSlash("plugin/skills/setup/SKILL.md"))); err != nil {
				t.Fatalf("removing the skill: %v", err)
			}
		},
		"a deleted copy must read as a failure, not as agreement",
	},
}

// TestSetupDocsChecksPassOnAnUnmutatedCopy is the positive control: the same check, the same code path, an
// untouched copy, and silence.
func TestSetupDocsChecksPassOnAnUnmutatedCopy(t *testing.T) {
	t.Parallel()
	root := copySetupDocs(t)
	var rec recorder
	checkSetupDocs(&rec, root)
	if len(rec.msgs) != 0 {
		t.Errorf("the setup join reported %d problem(s) on an unmutated copy: %s",
			len(rec.msgs), strings.Join(rec.msgs, "; "))
	}
}

// TestSetupDocsChecksFailOnAMutatedCopy proves the join can fail, one mutation at a time.
func TestSetupDocsChecksFailOnAMutatedCopy(t *testing.T) {
	t.Parallel()
	for _, m := range setupDocMutations {
		t.Run(m.name, func(t *testing.T) {
			t.Parallel()
			root := copySetupDocs(t)
			m.mutate(t, root)
			var rec recorder
			checkSetupDocs(&rec, root)
			if len(rec.msgs) == 0 {
				t.Fatalf("the mutation changed nothing the join can see, so the join is vacuous (%s)", m.because)
			}
			t.Logf("caught: %s", strings.Join(rec.msgs, "; "))
		})
	}
}
