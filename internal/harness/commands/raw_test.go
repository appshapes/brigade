package commands

import (
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestParseRaw pins the raw grammar: the harness consumes exactly its own
// flags and forwards everything else in order.
func TestParseRaw(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name        string
		args        []string
		adapterFlag bool
		want        rawArgs
		wantErr     bool
	}{
		{"adapter flags pass", []string{"--prompt", "--label", "x"}, false, rawArgs{Rest: []string{"--prompt", "--label", "x"}}, false},
		{"team consumed", []string{"--team", "ops", "--prompt"}, false, rawArgs{Team: "ops", Rest: []string{"--prompt"}}, false},
		{"team= form", []string{"--team=ops"}, false, rawArgs{Team: "ops"}, false},
		{"single dash", []string{"-team", "ops", "-json"}, false, rawArgs{Team: "ops", JSON: true}, false},
		{"profile is not the harness's any more (P7-7)", []string{"--profile", "bob"}, false, rawArgs{Rest: []string{"--profile", "bob"}}, false},
		{"json and log-level", []string{"--json", "--log-level", "debug", "--force"}, false, rawArgs{JSON: true, LogLevel: "debug", Rest: []string{"--force"}}, false},
		{"json=false", []string{"--json=false"}, false, rawArgs{}, false},
		{"adapter only when allowed", []string{"--adapter", "fs"}, false, rawArgs{Rest: []string{"--adapter", "fs"}}, false},
		{"adapter consumed for init", []string{"--adapter", "fs", "--url", "u"}, true, rawArgs{Adapter: "fs", Rest: []string{"--url", "u"}}, false},
		{"adapter= form", []string{"--adapter=fs=/opt/fs"}, true, rawArgs{Adapter: "fs=/opt/fs"}, false},
		{"double dash forwards verbatim", []string{"--team", "p", "--", "--team", "q", "--json"}, false, rawArgs{Team: "p", Rest: []string{"--team", "q", "--json"}}, false},
		{"positional kept", []string{"name", "-", "--prompt"}, false, rawArgs{Rest: []string{"name", "-", "--prompt"}}, false},
		{"missing value", []string{"--team"}, false, rawArgs{}, true},
		{"empty value", []string{"--team="}, false, rawArgs{}, true},
		{"bad level", []string{"--log-level", "shout"}, false, rawArgs{}, true},
		{"json with a value", []string{"--json=maybe"}, false, rawArgs{}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseRaw(tc.args, tc.adapterFlag)
			if tc.wantErr {
				wantCode(t, err, protocol.CodeUsage, "")
				return
			}
			if err != nil {
				t.Fatalf("parseRaw: %v", err)
			}
			if got.JSON != tc.want.JSON || got.LogLevel != tc.want.LogLevel || got.Team != tc.want.Team ||
				got.Adapter != tc.want.Adapter || !slices.Equal(got.Rest, tc.want.Rest) {
				t.Errorf("parseRaw(%v) = %+v, want %+v", tc.args, got, tc.want)
			}
		})
	}
}

// TestVerbOf pins the verb rule: first word, from the closed set.
func TestVerbOf(t *testing.T) {
	t.Parallel()
	verb, rest, err := verbOf([]string{"join", "--prompt"}, "team", teamVerbs)
	if err != nil || verb != "join" || !slices.Equal(rest, []string{"--prompt"}) {
		t.Errorf("verbOf = %q %v %v", verb, rest, err)
	}
	for _, args := range [][]string{nil, {"--prompt"}, {"nuke"}} {
		if _, _, err := verbOf(args, "team", teamVerbs); err == nil {
			t.Errorf("verbOf(%v) accepted", args)
		} else {
			wantCode(t, err, protocol.CodeUsage, "")
		}
	}
}

// TestWithRaw applies the late globals to the invocation.
func TestWithRaw(t *testing.T) {
	t.Parallel()
	inv := Invocation{LogLevel: "warn"}
	got := inv.withRaw(rawArgs{JSON: true, LogLevel: "debug"})
	if !got.JSON || got.LogLevel != "debug" {
		t.Errorf("withRaw = %+v", got)
	}
	same := inv.withRaw(rawArgs{})
	if same.JSON || same.LogLevel != "warn" {
		t.Errorf("withRaw(empty) changed the invocation: %+v", same)
	}
}

// TestRefuseInSessionText pins the fixed line of 6.4 as a usage error in
// ONE shape: refuseAdminInSession (the administration line, P5-2) answers
// `usage`, exit 2 and details.reason in_session, and names the terminal.
// The join-secret line is gone (P7-11): the three verbs that handle the
// secret run inside a session, and TestTeamAdminVerbsRefusedInSession is
// the regression pin that the administration pair did not follow them.
func TestRefuseInSessionText(t *testing.T) {
	t.Parallel()
	err := refuseAdminInSession()
	if err.Code != protocol.CodeUsage || err.Code.Exit() != 2 || err.Message != RefusalAdminInSession || err.Details["reason"] != "in_session" {
		t.Errorf("refuseAdminInSession = %+v", err)
	}
	if !strings.HasPrefix(RefusalAdminInSession, "run this in your own terminal: ") {
		t.Errorf("the administration line does not name the terminal: %q", RefusalAdminInSession)
	}
}
