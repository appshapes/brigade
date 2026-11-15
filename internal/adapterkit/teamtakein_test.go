package adapterkit_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The join secret of team "t1" on the backend it comes from, put together
// here: no file of the repository holds the shape of one.
var elsewhereSecret = strings.Join([]string{"brg1", "t1", strings.Repeat("cd", 16)}, ".")

// takeInRequest parses args as `team take-in` would and resolves the
// request with stdin behind it.
func takeInRequest(t *testing.T, stdin string, args ...string) (*adapterkit.TeamTakeIn, error) {
	t.Helper()
	fs := quietFlags()
	flags := adapterkit.NewTeamTakeInFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return flags.Request(strings.NewReader(stdin))
}

// writeSecret writes text to a file under a directory of the test's own.
func writeSecret(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops.secret")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// takeInDocument is the stdin document of a take-in of team "t1".
func takeInDocument(secret, name, label string) string {
	doc := `{"join_secret":"` + secret + `","team_name":"` + name + `"`
	if label != "" {
		doc += `,"human_label":"` + label + `"`
	}
	return doc + `,"unknown":1}`
}

// TestTeamTakeInFlagsRequest: the request is the one stdin document, with
// each flag that was given in the place of its member; the secret of
// --secret-file is the file's first line, trimmed; and with both
// --secret-file and --name stdin is never read.
func TestTeamTakeInFlagsRequest(t *testing.T) {
	t.Parallel()
	path := writeSecret(t, "  "+elsewhereSecret+" \r\nnot read\n")
	other := strings.Join([]string{"brg1", "t2", strings.Repeat("ef", 16)}, ".")
	for _, tc := range []struct {
		name, stdin string
		args        []string
		team, label string
	}{
		{"stdin", takeInDocument(elsewhereSecret, "ops", "alice"), nil, "ops", "alice"},
		{"stdin with no label", takeInDocument(elsewhereSecret, "ops", ""), nil, "ops", ""},
		{"--label over the document's", takeInDocument(elsewhereSecret, "ops", "alice"), []string{"--label", "bob"}, "ops", "bob"},
		{"--name over the document's", takeInDocument(elsewhereSecret, "ops", "alice"), []string{"--name", "renamed"}, "renamed", "alice"},
		{"--secret-file over the document's", takeInDocument(other, "ops", "alice"), []string{"--secret-file", path}, "ops", "alice"},
		{"--secret-file beside a document with no secret", `{"team_name":"ops"}`, []string{"--secret-file", path}, "ops", ""},
		{"--secret-file and --name, stdin unread", "not JSON", []string{"--secret-file", path, "--name", "ops"}, "ops", ""},
		{"--secret-file, --name and --label", "", []string{"--secret-file", path, "--name", "ops", "--label", "carol"}, "ops", "carol"},
	} {
		taken, err := takeInRequest(t, tc.stdin, tc.args...)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if taken.Secret.Secret() != elsewhereSecret || taken.Secret.TeamRef() != "t1" || taken.Request.JoinSecret != elsewhereSecret ||
			taken.Request.TeamName != tc.team || taken.Request.HumanLabel != tc.label {
			t.Errorf("%s: the request is not the secret, the name %q and the label %q that were given (name %q, label %q)",
				tc.name, tc.team, tc.label, taken.Request.TeamName, taken.Request.HumanLabel)
		}
	}
	// The backend member travels with the request, as it does on a join.
	taken, err := takeInRequest(t, `{"join_secret":"`+elsewhereSecret+`","team_name":"ops","backend":{"url":"https://b.example"}}`)
	if err != nil || string(taken.Request.Backend) != `{"url":"https://b.example"}` {
		t.Errorf("a document with a backend: %v", err)
	}
}

// TestTeamTakeInFlagsRefusals: argv that breaks a rule is `usage`, a
// request that breaks one is `invalid_input` naming the member, a secret
// file that cannot be read is `config`, and a secret that is not one is
// `invalid_input` naming join_secret, with no part of it in the refusal.
func TestTeamTakeInFlagsRefusals(t *testing.T) {
	t.Parallel()
	path := writeSecret(t, elsewhereSecret+"\n")
	document := takeInDocument(elsewhereSecret, "ops", "")
	for name, args := range map[string][]string{
		"an empty --secret-file":        {"--secret-file", ""},
		"a relative --secret-file":      {"--secret-file", "ops.secret"},
		"an empty --name":               {"--name", ""},
		"an empty --name beside a file": {"--secret-file", path, "--name", ""},
	} {
		_, err := takeInRequest(t, document, args...)
		refusal(t, name, err, protocol.CodeUsage, "reason", "invalid_arguments", elsewhereSecret)
	}
	long := strings.Repeat("x", 300)
	for name, tc := range map[string]struct {
		stdin, field string
		args         []string
	}{
		"stdin with no secret":            {`{"team_name":"ops"}`, "join_secret", nil},
		"stdin with no name":              {`{"join_secret":"` + elsewhereSecret + `"}`, "team_name", nil},
		"stdin with neither":              {`{}`, "join_secret", nil},
		"stdin with a long name":          {takeInDocument(elsewhereSecret, long, ""), "team_name", nil},
		"stdin with a long label":         {takeInDocument(elsewhereSecret, "ops", long), "human_label", nil},
		"a long --name":                   {document, "team_name", []string{"--name", long}},
		"a long --label":                  {document, "human_label", []string{"--label", long}},
		"stdin whose backend is a string": {`{"join_secret":"` + elsewhereSecret + `","team_name":"ops","backend":"x"}`, "backend", nil},
		"stdin that is not an object":     {`[]`, "", nil},
		"no stdin at all":                 {``, "stdin", nil},
		"no stdin beside --secret-file":   {``, "stdin", []string{"--secret-file", path}},
		"no stdin beside --name":          {``, "stdin", []string{"--name", "ops"}},
		"a secret that is not one":        {takeInDocument("brg1-nonsense", "ops", ""), "join_secret", nil},
		"a secret with a space inside":    {takeInDocument(strings.Replace(elsewhereSecret, ".cd", ".c d", 1), "ops", ""), "join_secret", nil},
	} {
		_, err := takeInRequest(t, tc.stdin, tc.args...)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidInput || (tc.field != "" && perr.Details["field"] != tc.field) {
			t.Errorf("%s: %v, want invalid_input naming %q", name, err, tc.field)
			continue
		}
		if strings.Contains(perr.Message+strings.Join(mapValues(perr.Details), " "), "nonsense") {
			t.Errorf("%s: the refusal carries part of the secret", name)
		}
	}

	_, err := takeInRequest(t, document, "--secret-file", filepath.Join(t.TempDir(), "no-such.secret"))
	refusal(t, "a file that is not there", err, protocol.CodeConfig, "reason", "secret_file_missing", "")
	_, err = takeInRequest(t, document, "--secret-file", t.TempDir())
	refusal(t, "a directory", err, protocol.CodeConfig, "reason", "secret_file_unreadable", "")
	oversize := filepath.Join(t.TempDir(), "long.secret")
	if err := os.WriteFile(oversize, []byte(strings.Repeat("x", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = takeInRequest(t, document, "--secret-file", oversize)
	refusal(t, "a file larger than a secret is", err, protocol.CodeInvalidInput, "field", "join_secret", "")
	for _, tc := range []struct{ name, text string }{
		{"an empty file", ""},
		{"a blank first line", "\n" + elsewhereSecret + "\n"},
		{"no prefix", strings.TrimPrefix(elsewhereSecret, "brg1.") + "\n"},
		{"a space inside", strings.Replace(elsewhereSecret, ".cd", ".c d", 1) + "\n"},
		{"nothing after the last dot", strings.TrimSuffix(elsewhereSecret, strings.Repeat("cd", 16)) + "\n"},
	} {
		// The document's own secret is a good one, and the file's is what counts.
		_, err := takeInRequest(t, document, "--secret-file", writeSecret(t, tc.text))
		refusal(t, tc.name, err, protocol.CodeInvalidInput, "field", "join_secret", strings.TrimSpace(tc.text))
	}
}

// openMoveRequest parses args as `team open-move` would and resolves them.
func openMoveRequest(t *testing.T, args ...string) (*adapterkit.TeamOpenMove, error) {
	t.Helper()
	fs := quietFlags()
	flags := adapterkit.NewTeamOpenMoveFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return flags.Request()
}

// TestTeamOpenMoveFlags: --ticket-file is required and names an absolute
// path in a directory that exists; --ticket-days is 1 to 365 when it is
// given, and 0, the backend's default, when it is not.
func TestTeamOpenMoveFlags(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ops.ticket")
	for _, tc := range []struct {
		name string
		args []string
		days int
	}{
		{"no days", []string{"--ticket-file", path}, 0},
		{"one day", []string{"--ticket-file", path, "--ticket-days", "1"}, 1},
		{"the most days", []string{"--ticket-file", path, "--ticket-days", "365"}, protocol.MoveTicketMaxDays},
	} {
		open, err := openMoveRequest(t, tc.args...)
		if err != nil || open.TicketFile != path || open.TicketDays != tc.days {
			t.Errorf("%s: %+v, %v; want the path and %d days", tc.name, open, err, tc.days)
		}
	}
	for name, args := range map[string][]string{
		"no --ticket-file":         nil,
		"an empty --ticket-file":   {"--ticket-file", ""},
		"a relative --ticket-file": {"--ticket-file", "ops.ticket"},
		"no days at all":           {"--ticket-file", path, "--ticket-days", "0"},
		"a day too many":           {"--ticket-file", path, "--ticket-days", "366"},
		"days below zero":          {"--ticket-file", path, "--ticket-days", "-1"},
		// The days are refused before the directory is looked at.
		"days out of range and no directory": {"--ticket-file", filepath.Join(dir, "missing", "t"), "--ticket-days", "0"},
	} {
		_, err := openMoveRequest(t, args...)
		refusal(t, name, err, protocol.CodeUsage, "reason", "invalid_arguments", "")
	}
	_, err := openMoveRequest(t, "--ticket-file", filepath.Join(dir, "missing", "t"))
	refusal(t, "a directory that is not there", err, protocol.CodeConfig, "reason", "ticket_file_unwritable", "")
	if _, statErr := os.Stat(path); statErr == nil {
		t.Errorf("resolving the flags wrote the ticket file")
	}
}
