package adapterkit_test

import (
	"flag"
	"io"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// quietFlags is a flag set as an adapter builds one: it never prints.
func quietFlags() *flag.FlagSet {
	fs := flag.NewFlagSet("adapter", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

// revokeRequest parses args as `team revoke-member` would and resolves the
// request with stdin behind it.
func revokeRequest(t *testing.T, stdin string, args ...string) (*protocol.TeamRevokeMemberRequest, error) {
	t.Helper()
	fs := quietFlags()
	flags := adapterkit.NewTeamRevokeFlags(fs)
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse %v: %v", args, err)
	}
	return flags.Request(strings.NewReader(stdin))
}

// TestTeamRevokeFlagsRequest: the flags make the request when one was
// given and stdin is then never read; with none, the one stdin document
// does, through the shape's own Validate.
func TestTeamRevokeFlagsRequest(t *testing.T) {
	t.Parallel()
	// unread fails the test if the request is resolved by reading it.
	const unread = `{"principal_ref":"from-stdin"}`
	for _, tc := range []struct {
		name    string
		stdin   string
		args    []string
		ref     string
		ban     bool
		version int // -1: no version
	}{
		{"--principal", unread, []string{"--principal", "p1"}, "p1", false, -1},
		{"--principal with --ban", unread, []string{"--principal", "p1", "--ban"}, "p1", true, -1},
		{"--ban=false with --principal", unread, []string{"--ban=false", "--principal", "p1"}, "p1", false, -1},
		{"--max-version", unread, []string{"--max-version", "3"}, "", false, 3},
		{"--max-version 0 is a value", unread, []string{"--max-version", "0"}, "", false, 0},
		{"the highest version", unread, []string{"--max-version", "2147483647"}, "", false, protocol.MaxSecretVersion},
		{"stdin, one member", `{"principal_ref":"p2","unknown":true}`, nil, "p2", false, -1},
		{"stdin, one member banned", `{"principal_ref":"p2","ban":true}`, nil, "p2", true, -1},
		{"stdin, by version", `{"joined_secret_version_lte":0}`, nil, "", false, 0},
	} {
		req, err := revokeRequest(t, tc.stdin, tc.args...)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		got := -1
		if req.JoinedSecretVersionLTE != nil {
			got = *req.JoinedSecretVersionLTE
		}
		if req.PrincipalRef != tc.ref || req.Ban != tc.ban || got != tc.version {
			t.Errorf("%s: request %+v (version %d), want principal %q, ban %v, version %d", tc.name, req, got, tc.ref, tc.ban, tc.version)
		}
		if err := req.Validate(); err != nil {
			t.Errorf("%s: the request does not validate: %v", tc.name, err)
		}
	}
}

// TestTeamRevokeFlagsRefusals: what is wrong on argv is `usage` with the
// reason every adapter gives and a message that never carries the value;
// what is wrong in the stdin document is `invalid_input` naming the
// member.
func TestTeamRevokeFlagsRefusals(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--principal", "p1", "--max-version", "1"},
		{"--ban"},
		{"--ban", "--max-version", "1"},
		{"--principal", ""},
		{"--max-version", "-1"},
		{"--max-version", "2147483648"},
	} {
		if strings.Contains(strings.Join(args, " "), "2147483648") && protocol.MaxSecretVersion == int(^uint(0)>>1) {
			continue // a 32-bit int: the flag set refuses the value before this code sees it
		}
		_, err := revokeRequest(t, `{"principal_ref":"p1"}`, args...)
		perr := asProtocol(t, err)
		if perr.Code != protocol.CodeUsage || perr.Details["reason"] != "invalid_arguments" {
			t.Errorf("%v: %s %v, want usage with reason invalid_arguments", args, perr.Code, perr.Details)
		}
		if strings.Contains(perr.Message, "p1") || strings.Contains(perr.Message, "2147483648") {
			t.Errorf("%v: the message carries an argument: %q", args, perr.Message)
		}
	}
	for stdin, field := range map[string]string{
		`{}`: "principal_ref",
		`{"principal_ref":"p1","joined_secret_version_lte":1}`: "principal_ref",
		`{"joined_secret_version_lte":1,"ban":true}`:           "ban",
		`{"joined_secret_version_lte":-1}`:                     "joined_secret_version_lte",
	} {
		_, err := revokeRequest(t, stdin)
		perr := asProtocol(t, err)
		if perr.Code != protocol.CodeInvalidInput || perr.Details["field"] != field {
			t.Errorf("%s: %s %v, want invalid_input naming %s", stdin, perr.Code, perr.Details, field)
		}
	}
	for _, stdin := range []string{"", "not json"} {
		_, err := revokeRequest(t, stdin)
		if perr := asProtocol(t, err); perr.Code != protocol.CodeInvalidInput {
			t.Errorf("stdin %q: %s, want invalid_input", stdin, perr.Code)
		}
	}
}

// TestTeamTransferFlagsRequest: --principal makes the request and stdin is
// not read; an empty value is `usage`; with no flag the stdin document
// does, and one without principal_ref is `invalid_input` naming it.
func TestTeamTransferFlagsRequest(t *testing.T) {
	t.Parallel()
	request := func(stdin string, args ...string) (*protocol.TeamTransferRequest, error) {
		fs := quietFlags()
		flags := adapterkit.NewTeamTransferFlags(fs)
		if err := fs.Parse(args); err != nil {
			t.Fatalf("parse %v: %v", args, err)
		}
		return flags.Request(strings.NewReader(stdin))
	}
	if req, err := request(`{"principal_ref":"from-stdin"}`, "--principal", "p1"); err != nil || req.PrincipalRef != "p1" {
		t.Errorf("--principal: %+v %v, want p1 and stdin unread", req, err)
	}
	if req, err := request(`{"principal_ref":"p2","unknown":1}`); err != nil || req.PrincipalRef != "p2" {
		t.Errorf("stdin: %+v %v, want p2", req, err)
	}
	_, err := request(`{"principal_ref":"p2"}`, "--principal", "")
	if perr := asProtocol(t, err); perr.Code != protocol.CodeUsage || perr.Details["reason"] != "invalid_arguments" {
		t.Errorf("an empty --principal: %s %v, want usage", perr.Code, perr.Details)
	}
	for _, stdin := range []string{`{}`, `{"principal_ref":""}`} {
		_, err := request(stdin)
		if perr := asProtocol(t, err); perr.Code != protocol.CodeInvalidInput || perr.Details["field"] != "principal_ref" {
			t.Errorf("stdin %s: %s %v, want invalid_input naming principal_ref", stdin, perr.Code, perr.Details)
		}
	}
	_, err = request("")
	if perr := asProtocol(t, err); perr.Code != protocol.CodeInvalidInput {
		t.Errorf("no stdin document: %s, want invalid_input", perr.Code)
	}
}
