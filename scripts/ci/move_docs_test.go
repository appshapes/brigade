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
			why:   "a record alone does not choose the backend a command that is run again leaves: the checkout does, and the refusal prints the command for each backend",
			files: []string{admin, "CHANGELOG.md"},
			gone:  []string{"finds the backend the team is leaving by itself: the one whose record names the server", "by the record that names the server, or by the checkout's own pin"},
			need:  []string{"A record alone does not choose"},
		},
		{
			why:   "a team is not moved onto the server it is on, whatever the certificate: a new certificate is a change of the team file and a join",
			files: []string{admin},
			need:  []string{"is the server this team is on", "not a move"},
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
			why:   "a ban holds for the membership of one installation, its principal: a person who resets Brigade or uses another machine is a new principal, whom an open move's ticket or the current secret admits, so the page on moving does not say that a ban alone keeps a person out",
			files: []string{admin},
			gone:  []string{"A member is kept out during an open move by a ban", "which the ticket does not get past"},
			need:  []string{"While a move is open, a revocation alone does not keep a member out", "holds for that installation's membership", "is a new principal", "a ban together with a rotation"},
		},
		{
			why:   "the page on a ban says the same: it is that principal's rejoin a ban refuses, not a person's",
			files: []string{"docs/team-admin/members/revoke-a-member.md"},
			gone:  []string{"makes the member's rejoin answer"},
			need:  []string{"holds for that installation's membership", "is a new principal", "a ban together with a rotation"},
		},
		{
			why:   "a session whose rejoin the server refused says so once, with the join that helps, and no session presents the ticket again for fifteen minutes: it does not say that the team file does not match",
			files: []string{"docs/team-member/join/follow-a-move.md"},
			gone:  []string{"the server refuses the join, and the session says that the file does not match"},
			need:  []string{"brigade team join --secret-file <path>", "fifteen minutes", "refuses the membership"},
		},
		{
			why:   "a server that answers a refusal is not told as one that did not answer, a refused rejoin is asked for once in fifteen minutes, and a line a refusal prints to be run carries the certificate's pin",
			files: []string{"CHANGELOG.md"},
			gone:  []string{"Any other failure changes nothing, and the session says that the backend the team moved to did not answer", "a ban does, and so does closing the move"},
			need:  []string{"fifteen minutes", "refuses the membership", "--certificate sha256:"},
		},
		{
			why:   "the Team Admin's page says what the same command run again does on such a machine",
			files: []string{admin},
			need:  []string{"names them and the flag", "the record's own form clears that record first"},
		},
		{
			why:   "the checkout's backend is not taken for the one a command that is run again leaves where another backend records the move too: a checkout left behind at an earlier move shows the same, so the skills do not say that the refusal means only that the checkout says nothing",
			files: skills,
			gone:  []string{"nothing in this checkout says which backend it is leaving now. The refusal"},
			need:  []string{"is not the only one that records a move to the server"},
		},
		{
			why:   "the Team Admin's page and the changelog say the same of a checkout whose backend is one of two that record the move",
			files: []string{admin, "CHANGELOG.md"},
			need:  []string{"a checkout left behind at an earlier move"},
		},
		{
			why:   "a session says that the team file does not match of a backend it joined that does not answer, of a follow that did not get the lock and of a member who left: the member's page named none of these, and said that nothing vouched for the change",
			files: []string{"docs/team-member/join/follow-a-move.md"},
			gone:  []string{"Then nothing vouched for the change: the administrator"},
			need:  []string{"The backend you joined did not answer", "Another session of yours was following or joining", "You left the team on the backend it moved to"},
		},
		{
			why:   "a backend the member joined that no longer counts them a member has answered: the session does not say again what an earlier one was told of the backend the team moved to, and the member is not told to wait",
			files: []string{"docs/team-member/join/follow-a-move.md"},
			gone:  []string{"It did not answer, or it no longer counts you a member", "A session that could not ask, the second and third of these"},
			need:  []string{"The backend you joined no longer counts you a member", "waiting mends nothing", "is not such a case"},
		},
		{
			why:   "a join that the backend a team moved to did not answer is told as a backend that did not answer, and a session that could not ask says what the last one that asked was told",
			files: []string{"docs/team-member/join/follow-a-move.md"},
			need:  []string{"joined it with the move's ticket", "A session that could not ask"},
		},
		{
			why:   "the changelog says the same of a join that was not answered and of a session that starts while it cannot ask",
			files: []string{"CHANGELOG.md"},
			need:  []string{"A join that was not answered is no refusal", "while it cannot ask at all"},
		},
		{
			why:   "the Team Admin's page names the backend a member joined not answering among the reasons a session says that the file does not match",
			files: []string{admin},
			need:  []string{"the old backend does not answer when their session asks it whether the team moved"},
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
