//go:build !windows

// The setup drift join. Plan 6.9 says the setup skill and the documentation "carry the same text" for the same
// procedures, and until now nothing checked it. Several copies is a deliberate decision, not an accident: a skill
// body cannot follow a link, and each harness ships its own skill. What each copy carries differs (the persona
// pages have the reasons, the skills have the commands with ${CLAUDE_PLUGIN_ROOT} substituted) — but the COMMANDS
// must be identical in every copy that exists, because a command that drifts is a command one of the readers
// runs wrong. Since card 110 the pages are the member's and the admin's leaves, each joined on the forms of its
// own procedure.
//
// The join is deliberately narrow. It pins the six invocation forms a person types and nothing else: not
// prose, not which copies exist (a copy may be consolidated away — Rjae's ruling 2026-09-08, open by default),
// so a lane may rewrite the reasons in docs/setup.md without touching this file; it bites the moment a flag or
// a verb moves in one copy and not the others.
package ci_test

import (
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// The copies, relative to the repository root, each with the witnesses it carries: the four setup skills carry
// every form (Grok Build's since work item 383, when it was carried over from Codex CLI's); since card 110 the
// member and admin pages carry the forms of their own procedures.
var setupDocFiles = map[string][]string{
	"plugin/skills/setup/SKILL.md":              setupDocWitnesses,
	"muse/brigade/skills/setup/SKILL.md":        setupDocWitnesses,
	"codex/brigade/skills/setup/SKILL.md":       setupDocWitnesses,
	"grok/brigade/skills/setup/SKILL.md":        setupDocWitnesses,
	"docs/team-admin/create/create-the-team.md": {setupDocWitnesses[0], setupDocWitnesses[1]},
	"docs/team-member/join/muse-code.md":        {setupDocWitnesses[2]},
	"docs/team-member/join/codex.md":            {setupDocWitnesses[2]},
	"docs/team-member/join/grok.md":             {setupDocWitnesses[2]},
	"docs/team-member/leave/leave-the-team.md":  {setupDocWitnesses[3], setupDocWitnesses[4]},
	// The one command of a team's move (work item 328): the setup skills name it, the `move` skill of each
	// package runs it, and the Team Admin's and the Project Owner's pages are its procedure.
	"plugin/skills/move/SKILL.md":                     {setupDocWitnesses[5]},
	"muse/brigade/skills/move/SKILL.md":               {setupDocWitnesses[5]},
	"codex/brigade/skills/move/SKILL.md":              {setupDocWitnesses[5]},
	"grok/brigade/skills/move/SKILL.md":               {setupDocWitnesses[5]},
	"docs/team-admin/members/README.md":               {setupDocWitnesses[5]},
	"docs/team-admin/members/move-the-team.md":        {setupDocWitnesses[5]},
	"docs/project-owner/server/README.md":             {setupDocWitnesses[5]},
	"docs/project-owner/server/move-from-supabase.md": {setupDocWitnesses[5]},
	// The pages that name the one command since they were revised to lead with it (2026-11-15, execution log
	// P103-4), and the page of a team's move from one server to another (work item 341): every path a reader
	// takes to the move names the same command.
	"docs/project-owner/server/move-to-another-server.md": {setupDocWitnesses[5]},
	"docs/project-owner/server/hand-over-the-values.md":   {setupDocWitnesses[5]},
	"docs/project-owner/server/keep-the-server.md":        {setupDocWitnesses[5]},
	"docs/project-owner/server/open-a-move.md":            {setupDocWitnesses[5]},
	"docs/project-owner/server/run-the-server.md":         {setupDocWitnesses[5]},
	"docs/project-owner/server/take-in-a-team.md":         {setupDocWitnesses[5]},
	"docs/project-owner/README.md":                        {setupDocWitnesses[5]},
	"docs/project-owner/delete/README.md":                 {setupDocWitnesses[5]},
	"docs/project-owner/delete/delete-the-project.md":     {setupDocWitnesses[5]},
	"docs/project-owner/update/migrations.md":             {setupDocWitnesses[5]},
	"docs/team-admin/README.md":                           {setupDocWitnesses[5]},
	"docs/team-admin/configure/team-file.md":              {setupDocWitnesses[5]},
	"docs/developer/change/server.md":                     {setupDocWitnesses[5]},
	"docs/agent/in-a-team/README.md":                      {setupDocWitnesses[5]},
	"docs/agent/in-a-team/claude-code.md":                 {setupDocWitnesses[5]},
	"docs/agent/in-a-team/codex.md":                       {setupDocWitnesses[5]},
	"docs/agent/in-a-team/grok.md":                        {setupDocWitnesses[5]},
	"docs/agent/in-a-team/muse-code.md":                   {setupDocWitnesses[5]},
}

// pluginBinPrefix is the one difference the join tolerates: each copy names the binary the way its own reader
// reaches it. Everything after the program name must match.
var pluginBinPrefix = regexp.MustCompile(`(\$\{CLAUDE_PLUGIN_ROOT\}|<plugin>)/bin/brigade`)

// whitespaceRun collapses wrapping. These are three hand-wrapped markdown files, so the same sentence sits on
// one line in one copy and across two in another; that is formatting, not drift, and must not fail the join.
var whitespaceRun = regexp.MustCompile(`\s+`)

// The six witnesses: the invocation forms of the procedures a person types (create, join, leave, reset), and of
// the one a person or their session types to move a team (work item 328).
var setupDocWitnesses = []string{
	"brigade team create --url https://<ref>.supabase.co --key sb_publishable_",
	"--name <team> --secret-file ~/brigade-<team>.secret",
	"brigade team join",
	"brigade team leave",
	"brigade team reset",
	"brigade team move --to <url> --secret-file <path>",
}

// checkSetupDocs asserts each copy's witnesses in every copy that exists, with the path prefix normalised away.
func checkSetupDocs(r reporter, root string) {
	r.Helper()
	rels := make([]string, 0, len(setupDocFiles))
	for rel := range setupDocFiles {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		if _, err := os.Stat(filepath.Join(root, rel)); err != nil {
			continue // a copy that is gone has nothing to drift
		}
		text, ok := readText(r, root, rel)
		if !ok {
			continue
		}
		normalised := whitespaceRun.ReplaceAllString(pluginBinPrefix.ReplaceAllString(text, "brigade"), " ")
		for _, witness := range setupDocFiles[rel] {
			if !strings.Contains(normalised, witness) {
				r.Errorf("%s does not carry %q; the setup copies (%s) must name the same commands (plan 6.9)",
					rel, witness, strings.Join(rels, ", "))
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
	for rel := range setupDocFiles {
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
		"a_admin_page_changes_a_flag",
		replaceOnceIn("docs/team-admin/create/create-the-team.md", "--name <team> --secret-file ~/brigade-<team>.secret", "--name <team> --secret-off ~/brigade-<team>.secret"),
		"a flag mistyped in one copy is a command that reader runs wrong",
	},
	{
		"b_muse_skill_changes_a_flag",
		replaceOnceIn("muse/brigade/skills/setup/SKILL.md", "team create --url https://<ref>.supabase.co --key sb_publishable_", "team create --url https://<ref>.supabase.co --secret sb_publishable_"),
		"the Muse reader may see only this copy",
	},
	{
		"d_a_server_page_changes_the_move",
		replaceOnceIn("docs/project-owner/server/move-to-another-server.md", "brigade team move --to <url> --secret-file <path>", "brigade team move --url <url> --secret-file <path>"),
		"an operator who reads only the page of a move between two servers gives the Team Admin that command",
	},
	{
		"c_skill_changes_a_profile_name",
		replaceOnceIn("plugin/skills/setup/SKILL.md", "brigade team reset", "brigade team wipe"),
		"the skill body cannot follow a link, so its commands must be right on their own",
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
