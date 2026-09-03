package commands

import (
	"slices"
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
		{"profile consumed", []string{"--profile", "bob", "--prompt"}, false, rawArgs{Profile: "bob", Rest: []string{"--prompt"}}, false},
		{"profile= form", []string{"--profile=bob"}, false, rawArgs{Profile: "bob"}, false},
		{"single dash", []string{"-profile", "bob", "-json"}, false, rawArgs{Profile: "bob", JSON: true}, false},
		{"json and log-level", []string{"--json", "--log-level", "debug", "--force"}, false, rawArgs{JSON: true, LogLevel: "debug", Rest: []string{"--force"}}, false},
		{"json=false", []string{"--json=false"}, false, rawArgs{}, false},
		{"adapter only when allowed", []string{"--adapter", "fs"}, false, rawArgs{Rest: []string{"--adapter", "fs"}}, false},
		{"adapter consumed for init", []string{"--adapter", "fs", "--url", "u"}, true, rawArgs{Adapter: "fs", Rest: []string{"--url", "u"}}, false},
		{"adapter= form", []string{"--adapter=fs=/opt/fs"}, true, rawArgs{Adapter: "fs=/opt/fs"}, false},
		{"double dash forwards verbatim", []string{"--profile", "p", "--", "--profile", "q", "--json"}, false, rawArgs{Profile: "p", Rest: []string{"--profile", "q", "--json"}}, false},
		{"positional kept", []string{"name", "-", "--prompt"}, false, rawArgs{Rest: []string{"name", "-", "--prompt"}}, false},
		{"missing value", []string{"--profile"}, false, rawArgs{}, true},
		{"empty value", []string{"--profile="}, false, rawArgs{}, true},
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
			if got.JSON != tc.want.JSON || got.LogLevel != tc.want.LogLevel || got.Profile != tc.want.Profile ||
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

// TestRefuseInSessionText pins the fixed line of 6.4 as a usage error.
func TestRefuseInSessionText(t *testing.T) {
	t.Parallel()
	err := refuseInSession()
	if err.Code != protocol.CodeUsage || err.Code.Exit() != 2 || err.Message != RefusalInSession {
		t.Errorf("refuseInSession = %+v", err)
	}
}
