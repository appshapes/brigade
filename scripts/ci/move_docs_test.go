//go:build !windows

// What the pages, the skills and the plugin's manifest say of a team's move, held against what the command does
// (work item 341). The verification of that work found sentences the code contradicted, each of which sent a
// person or a session the wrong way: a manifest that said only a team's creator can move it, a page that said a
// team command goes to the backend the team file names, skills that named an undo the command refuses once a team
// has moved before. A sentence here is pinned because it was false once; nothing else of the prose is.
package ci_test

import (
	json "encoding/json/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

// oneLine reads a file of the repository with every run of white space made one space, so that a sentence is found
// wherever its lines break.
func oneLine(t *testing.T, rel string) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(string(text)), " ")
}

// TestTheOptionOfAMoveInASessionSpeaksOfTheOwner: on a Brigade server a team is moved by its owner, who need not be
// the member who created it (a transfer, the first join of a team the operator took in, the operator's own
// command). The option that lets a session move a team said "a team you created" and "only its creator can move
// it", in the manifest a member reads when they set it and in the page of the options.
func TestTheOptionOfAMoveInASessionSpeaksOfTheOwner(t *testing.T) {
	t.Parallel()
	var manifest struct {
		UserConfig map[string]struct {
			Description string `json:"description"`
		} `json:"userConfig"`
	}
	text, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), "plugin", ".claude-plugin", "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(text, &manifest); err != nil {
		t.Fatal(err)
	}
	option := strings.Join(strings.Fields(manifest.UserConfig["team_move_in_session"].Description), " ")
	row := ""
	for _, line := range strings.Split(oneLineKeepingRows(t, "docs/team-member/configure/options.md"), "\n") {
		if strings.HasPrefix(line, "| `team_move_in_session`") {
			row = line
		}
	}
	for name, said := range map[string]string{"plugin.json": option, "options.md": row} {
		if said == "" {
			t.Fatalf("%s does not describe team_move_in_session", name)
		}
		for _, gone := range []string{"a team you created", "a team you did not create", "only its creator can move"} {
			if strings.Contains(said, gone) {
				t.Errorf("%s says %q of team_move_in_session: on a server a team's owner moves it, who need not have created it", name, gone)
			}
		}
		for _, need := range []string{"a team you own", "only the team's owner can move it"} {
			if !strings.Contains(said, need) {
				t.Errorf("%s does not say %q of team_move_in_session", name, need)
			}
		}
	}
}

// oneLineKeepingRows is a file with the white space inside each of its lines made one space.
func oneLineKeepingRows(t *testing.T, rel string) string {
	t.Helper()
	text, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(text), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}
	return strings.Join(lines, "\n")
}

// TestThePagesOfAMoveSayWhatTheCommandDoes: the sentences of the move's pages and skills that the command
// contradicted, and the ones that say what it does now.
func TestThePagesOfAMoveSayWhatTheCommandDoes(t *testing.T) {
	t.Parallel()
	skills := []string{"plugin/skills/move/SKILL.md", "muse/brigade/skills/move/SKILL.md", "codex/brigade/skills/move/SKILL.md", "grok/brigade/skills/move/SKILL.md"}
	admin, operator := "docs/team-admin/members/move-the-team.md", "docs/project-owner/server/move-to-another-server.md"
	for _, tc := range []struct {
		why   string
		files []string
		gone  []string
		need  []string
	}{
		{
			why:   "a team command goes to the backend the checkout's pin names, and `--on` names another: ending a team on the server it left by the team file of an older commit rotated the secret of the server it lives on",
			files: []string{admin},
			gone:  []string{"a team command goes to the backend the file names"},
			need:  []string{"brigade team rotate-secret --on <the old server's url or host>", "brigade team revoke-member --on <the old server's url or host>"},
		},
		{
			why:   "the operators' page names the same flag for ending a team on the server it left",
			files: []string{operator},
			need:  []string{"--on <the old server's url or host>"},
		},
		{
			why:   "the command no longer leaves the administrator on a copy's roster twice, and never names --set-owner for the same server or a copy of its data",
			files: []string{operator},
			gone:  []string{"The Team Admin is then on that server twice"},
			need:  []string{"the same server under another address, or a copy of its data"},
		},
		{
			why:   "a team that moved before leaves the machine joined to more than one earlier backend: the undo is the command the last line names, and the same command run again finds the backend it leaves or names the candidates",
			files: skills,
			gone:  []string{"**To undo**: `brigade team move --clear`, and what the command said"},
			need:  []string{"the command its last line named", "--from <host>"},
		},
		{
			why:   "a server's journal has no line for a rotation or a revocation: what shows them is what each command prints",
			files: []string{operator},
			gone:  []string{"your journal has the rotation and the revocations"},
			need:  []string{"Your journal has no line for either"},
		},
		{
			why:   "a follow joins with the credential the machine holds and sets nothing aside, so a ban holds and a revocation alone does not keep a member out of an open move",
			files: []string{admin, "docs/team-member/join/follow-a-move.md", "CHANGELOG.md"},
			gone:  []string{"sets it aside", "as a machine that had never joined"},
		},
		{
			why:   "the page on moving says what keeps a member out while a move is open",
			files: []string{admin},
			need:  []string{"While a move is open, a revocation alone does not keep a member out", "A member is kept out during an open move by a ban", "or by closing the move"},
		},
		{
			why:   "the Team Admin's page says what the same command run again does on such a machine",
			files: []string{admin},
			need:  []string{"names them and the flag", "the record's own form clears that record first"},
		},
	} {
		for _, file := range tc.files {
			said := oneLine(t, file)
			for _, gone := range tc.gone {
				if strings.Contains(said, gone) {
					t.Errorf("%s still says %q: %s", file, gone, tc.why)
				}
			}
			for _, need := range tc.need {
				if !strings.Contains(said, need) {
					t.Errorf("%s does not say %q: %s", file, need, tc.why)
				}
			}
		}
	}
}
