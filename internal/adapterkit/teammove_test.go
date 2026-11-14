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

// A ticket of team "t1", put together here: no file of the repository
// holds the shape of one.
var teamTicket = strings.Join([]string{"brg1", "t1", strings.Repeat("ab", 16)}, ".")

// moveRequest parses args as `team move` would and resolves the request
// with stdin behind it.
func moveRequest(t *testing.T, stdin string, args ...string) (*adapterkit.TeamMove, error) {
	t.Helper()
	fs := quietFlags()
	flags := adapterkit.NewTeamMoveFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return flags.Request(strings.NewReader(stdin))
}

// writeTicket writes text to a file under a directory of the test's own.
func writeTicket(t *testing.T, text string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ops.ticket")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// refusal asserts that err is a *protocol.Error of the code, with the
// details member named, and that no part of secret is in it.
func refusal(t *testing.T, what string, err error, code protocol.Code, key, value, secret string) {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != code || perr.Details[key] != value {
		t.Errorf("%s: %v, want %s with details.%s %q", what, err, code, key, value)
		return
	}
	if secret != "" && strings.Contains(perr.Message+strings.Join(mapValues(perr.Details), " "), secret) {
		t.Errorf("%s: the refusal carries part of the ticket", what)
	}
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}

// TestTeamMoveFlagsRequest: the destination is the flags when one of them
// was given, and stdin is then never read; with none, the one stdin
// document is, through the shape's own Validate. The ticket is the first
// line of the file, trimmed. --clear reads neither.
func TestTeamMoveFlagsRequest(t *testing.T) {
	t.Parallel()
	path := writeTicket(t, "  "+teamTicket+" \r\nnot read\n")
	const unread = `{"adapter":"from-stdin","url":"https://stdin.example"}`
	for _, tc := range []struct {
		name, stdin string
		args        []string
		url, pin    string
	}{
		{"flags", unread, []string{"--ticket-file", path, "--adapter", "rest", "--url", "https://b.example"}, "https://b.example", ""},
		{"flags with a certificate", unread, []string{"--ticket-file", path, "--adapter", "rest", "--url", "https://b.example", "--certificate", "sha256:00"}, "https://b.example", "sha256:00"},
		{"stdin", `{"adapter":"rest","url":"https://c.example","unknown":1}`, []string{"--ticket-file", path}, "https://c.example", ""},
		{"stdin with a certificate", `{"adapter":"rest","url":"https://c.example","certificate":"sha256:11"}`, []string{"--ticket-file", path}, "https://c.example", "sha256:11"},
	} {
		move, err := moveRequest(t, tc.stdin, tc.args...)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if move.Clear || move.Ticket != teamTicket || move.Destination.Adapter != "rest" || move.Destination.URL != tc.url || move.Destination.Certificate != tc.pin {
			t.Errorf("%s: the request is not the destination given and the file's ticket (clear %v, url %q, certificate %q)",
				tc.name, move.Clear, move.Destination.URL, move.Destination.Certificate)
		}
	}
	// --clear: nothing is read, stdin and a ticket file that is not there included.
	move, err := moveRequest(t, "not JSON", "--clear")
	if err != nil || !move.Clear || move.Destination != nil || move.Ticket != "" {
		t.Errorf("--clear: %+v, %v", move, err)
	}
}

// TestTeamMoveFlagsRefusals: argv that breaks a rule is `usage`, a
// destination that breaks one is `invalid_input` naming the member, a
// ticket file that cannot be read is `config`, and a file that holds no
// ticket is `invalid_input` naming ticket with no part of the file in it.
func TestTeamMoveFlagsRefusals(t *testing.T) {
	t.Parallel()
	path := writeTicket(t, teamTicket+"\n")
	const destination = `{"adapter":"rest","url":"https://b.example"}`
	for name, args := range map[string][]string{
		"no --ticket-file":             nil,
		"an empty --ticket-file":       {"--ticket-file", ""},
		"a relative --ticket-file":     {"--ticket-file", "ops.ticket"},
		"--clear with --ticket-file":   {"--clear", "--ticket-file", path},
		"--clear with a destination":   {"--clear", "--adapter", "rest", "--url", "https://b.example"},
		"--adapter without --url":      {"--ticket-file", path, "--adapter", "rest"},
		"--url without --adapter":      {"--ticket-file", path, "--url", "https://b.example"},
		"--certificate alone":          {"--ticket-file", path, "--certificate", "sha256:00"},
		"an empty --adapter":           {"--ticket-file", path, "--adapter", "", "--url", "https://b.example"},
		"an empty --url beside a name": {"--ticket-file", path, "--adapter", "rest", "--url", ""},
	} {
		_, err := moveRequest(t, destination, args...)
		refusal(t, name, err, protocol.CodeUsage, "reason", "invalid_arguments", teamTicket)
	}
	for name, tc := range map[string]struct{ stdin, field string }{
		"stdin with no adapter":      {`{"url":"https://b.example"}`, "adapter"},
		"stdin with no url":          {`{"adapter":"rest"}`, "url"},
		"stdin with a long adapter":  {`{"adapter":"` + strings.Repeat("a", protocol.MaxMoveAdapterChars+1) + `","url":"https://b.example"}`, "adapter"},
		"stdin with a long url":      {`{"adapter":"rest","url":"` + strings.Repeat("u", protocol.MaxMoveURLChars+1) + `"}`, "url"},
		"stdin with a long pin":      {`{"adapter":"rest","url":"https://b.example","certificate":"` + strings.Repeat("c", protocol.MaxMoveCertificateChars+1) + `"}`, "certificate"},
		"stdin that is not a object": {`[]`, ""},
	} {
		_, err := moveRequest(t, tc.stdin, "--ticket-file", path)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidInput || (tc.field != "" && perr.Details["field"] != tc.field) {
			t.Errorf("%s: %v, want invalid_input naming %q", name, err, tc.field)
		}
	}
	long := filepath.Join(t.TempDir(), "long.ticket")
	if err := os.WriteFile(long, []byte(strings.Repeat("x", 64<<10+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := moveRequest(t, "", "--ticket-file", path, "--adapter", strings.Repeat("a", protocol.MaxMoveAdapterChars+1), "--url", "https://b.example")
	refusal(t, "a long --adapter", err, protocol.CodeInvalidInput, "field", "adapter", teamTicket)

	_, err = moveRequest(t, destination, "--ticket-file", filepath.Join(t.TempDir(), "no-such.ticket"))
	refusal(t, "a file that is not there", err, protocol.CodeConfig, "reason", "ticket_file_missing", "")
	_, err = moveRequest(t, destination, "--ticket-file", t.TempDir())
	refusal(t, "a directory", err, protocol.CodeConfig, "reason", "ticket_file_unreadable", "")
	_, err = moveRequest(t, destination, "--ticket-file", long)
	refusal(t, "a file larger than a ticket is", err, protocol.CodeInvalidInput, "field", "ticket", "")
	for _, tc := range []struct{ name, text string }{
		{"an empty file", ""},
		{"a blank first line", "\n" + teamTicket + "\n"},
		{"no prefix", strings.TrimPrefix(teamTicket, "brg1.") + "\n"},
		{"a space inside", strings.Replace(teamTicket, ".ab", ".a b", 1) + "\n"},
		{"nothing after the last dot", strings.TrimSuffix(teamTicket, strings.Repeat("ab", 16)) + "\n"},
	} {
		_, err := moveRequest(t, destination, "--ticket-file", writeTicket(t, tc.text))
		refusal(t, tc.name, err, protocol.CodeInvalidInput, "field", "ticket", strings.TrimSpace(tc.text))
	}
}

// TestCheckMoveTicket: a ticket is a join secret that carries the team's
// own reference; one of another team, and text that is no join secret,
// are refused with the same answer.
func TestCheckMoveTicket(t *testing.T) {
	t.Parallel()
	if err := adapterkit.CheckMoveTicket(teamTicket, "t1"); err != nil {
		t.Errorf("the team's own ticket: %v", err)
	}
	for name, ticket := range map[string]string{
		"another team":   strings.Replace(teamTicket, ".t1.", ".t2.", 1),
		"no join secret": "brg1-nonsense",
		"empty":          "",
	} {
		refusal(t, name, adapterkit.CheckMoveTicket(ticket, "t1"), protocol.CodeInvalidInput, "field", "ticket", ticket)
	}
}

// TestTicketFileTargetAndWrite: `team moved --ticket-file` takes an
// absolute path in a directory that exists and nothing else, an empty path
// being no path; the ticket is written there for its owner alone, with a
// line end, over whatever was there.
func TestTicketFileTargetAndWrite(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "read.ticket")
	if err := adapterkit.CheckTicketFileTarget(""); err != nil {
		t.Errorf("no path: %v", err)
	}
	if err := adapterkit.CheckTicketFileTarget(path); err != nil {
		t.Errorf("a path in a directory that exists: %v", err)
	}
	refusal(t, "a relative path", adapterkit.CheckTicketFileTarget("read.ticket"), protocol.CodeUsage, "reason", "invalid_arguments", "")
	refusal(t, "a directory that is not there", adapterkit.CheckTicketFileTarget(filepath.Join(dir, "missing", "t")),
		protocol.CodeConfig, "reason", "ticket_file_unwritable", "")
	//nolint:gosec // G306: the mode the write below has to replace
	if err := os.WriteFile(path, []byte("an earlier file, readable by all\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteTicketFile(path, teamTicket); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || !adapterkit.OwnerReadWriteOnly(info) {
		t.Fatalf("the ticket file is not a regular file for its owner alone: %v %v", info, err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != teamTicket+"\n" { //nolint:gosec // the test's own temp file
		t.Fatalf("the ticket file does not hold the ticket and a line end: %v", err)
	}
	refusal(t, "a path that cannot be written", adapterkit.WriteTicketFile(filepath.Join(dir, "missing", "t"), teamTicket),
		protocol.CodeConfig, "reason", "ticket_file_unwritable", teamTicket)
}
