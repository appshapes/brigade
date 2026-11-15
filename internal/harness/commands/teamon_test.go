package commands

import (
	json "encoding/json/v2"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestTeamAdminVerbsNameTheBackendAndTakeOn (work item 341): `team
// rotate-secret` and `team revoke-member` say which backend they act on,
// on stderr before the adapter is asked and as the result's `url`, and
// `--on` points them at another backend this machine has joined the team
// on: by its url, in any spelling that is trivially the same, or by its
// host. `--on` is the harness's and never reaches the adapter.
func TestTeamAdminVerbsNameTheBackendAndTakeOn(t *testing.T) {
	t.Parallel()
	const other = "https://old.example.test:8743"
	for _, tc := range []struct {
		name    string
		args    []string
		profile string
		host    string
		url     string
		forward []string
	}{
		{"no flag: the team --team names", []string{"revoke-member", "--team", "bobteam", "--max-version", "1"}, "bob", "abc.supabase.co", "https://abc.supabase.co", []string{"--max-version", "1"}},
		{"--on a host", []string{"revoke-member", "--on", "old.example.test:8743", "--max-version", "1"}, "bob-old", "old.example.test:8743", other, []string{"--max-version", "1"}},
		{"--on a url in another spelling", []string{"rotate-secret", "--secret-file", "/tmp/r.secret", "--on=HTTPS://Old.Example.test:8743/"}, "bob-old", "old.example.test:8743", other, []string{"--secret-file", "/tmp/r.secret"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, dump := passThroughFixture(t, fakeadapter.Script{
				Responses: map[string][]fakeadapter.Response{
					"team " + tc.args[0]: {{Result: []byte(`{"team_ref":"t_bob","revoked":1}`), Stderr: "fake: done"}},
				},
			})
			if tc.profile != "bob" {
				// The same team on the backend it left: two memberships of one
				// team, which --team cannot tell apart.
				if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, "bob-old", &adapterkit.Profile{
					Version: adapterkit.ProfileVersion, Adapter: "fake", URL: other, TeamRef: "t_bob", TeamName: "bobteam",
				}); err != nil {
					t.Fatal(err)
				}
			}
			args := tc.args
			// Outside any project: the team is --team's, or the one --on finds.
			iv := f.inv(f.terminalEnv(), "", args...)
			iv.Deps.Getwd = func() (string, error) { return f.dirs.Root, nil }
			if err := Team(iv); err != nil {
				t.Fatalf("team %s: %v\nstderr: %s", strings.Join(args, " "), err, f.errb.String())
			}
			invs := readDump(t, dump)
			if len(invs) != 1 || invs[0].Env["BRIGADE_PROFILE"] != tc.profile || !slices.Equal(invs[0].Args, tc.forward) {
				t.Fatalf("the adapter was asked %+v, want one call under %q with %v", invs, tc.profile, tc.forward)
			}
			said := `brigade: team ` + tc.args[0] + ` acts on team "bobteam" (t_bob) at ` + tc.host + "\n"
			if !strings.HasPrefix(f.errb.String(), said) || !strings.Contains(f.errb.String(), "fake: done") {
				t.Errorf("stderr = %q, want %q before the adapter's own", f.errb.String(), said)
			}
			var got struct {
				Result struct {
					Revoked int    `json:"revoked"`
					URL     string `json:"url"`
				} `json:"result"`
			}
			if err := json.Unmarshal([]byte(strings.TrimSpace(f.out.String())), &got); err != nil || got.Result.Revoked != 1 || got.Result.URL != tc.url {
				t.Errorf("stdout = %q (%v), want the adapter's result with url %q", f.out.String(), err, tc.url)
			}
		})
	}

	// --on a backend the team was never joined on, and --on with no value.
	f, dump := passThroughFixture(t, fakeadapter.Script{})
	nowhere := f.inv(f.terminalEnv(), "", "revoke-member", "--on", "nowhere.example.test", "--max-version", "1")
	nowhere.Deps.Getwd = func() (string, error) { return f.dirs.Root, nil }
	wantCode(t, Team(nowhere), protocol.CodeConfig, "team_unknown")
	wantCode(t, Team(f.inv(f.terminalEnv(), "", "rotate-secret", "--on")), protocol.CodeUsage, "")
	wantCode(t, Team(f.inv(f.terminalEnv(), "", "rotate-secret", "--on", "a.example.test", "--on", "b.example.test")), protocol.CodeUsage, "")
	if _, err := osLstat(dump); err == nil {
		t.Errorf("a refused --on reached the adapter")
	}
}

// TestWithBackend: the member is added to the result of an envelope that
// succeeded, and nothing else of what the adapter wrote changes.
func TestWithBackend(t *testing.T) {
	t.Parallel()
	const backend = "https://old.example.test:8743"
	for _, tc := range []struct{ in, want string }{
		{`{"ok":true,"protocol_version":"1","result":{"team_ref":"t","revoked":1}}` + "\n", `{"ok":true,"protocol_version":"1","result":{"team_ref":"t","revoked":1,"url":"https://old.example.test:8743"}}` + "\n"},
		{`{"ok":true,"result":{},"protocol_version":"1"}`, `{"ok":true,"result":{"url":"https://old.example.test:8743"},"protocol_version":"1"}`},
		{`{"ok":true,"protocol_version":"1","result":{"nested":{"a":"}"},"n":2}}`, `{"ok":true,"protocol_version":"1","result":{"nested":{"a":"}"},"n":2,"url":"https://old.example.test:8743"}}`},
		{`{"ok":true,"protocol_version":"1","result":{"url":"its own"}}`, `{"ok":true,"protocol_version":"1","result":{"url":"its own"}}`},
		{`{"ok":false,"protocol_version":"1","error":{"code":"unauthorized","message":"no"}}`, `{"ok":false,"protocol_version":"1","error":{"code":"unauthorized","message":"no"}}`},
		{`{"ok":true,"protocol_version":"1","result":[1,2]}`, `{"ok":true,"protocol_version":"1","result":[1,2]}`},
		{"not an envelope\n", "not an envelope\n"},
		{"", ""},
	} {
		if got := string(withBackend([]byte(tc.in), backend)); got != tc.want {
			t.Errorf("withBackend(%q)\n got %q\nwant %q", tc.in, got, tc.want)
		}
	}
}

// TestOnTellsTwoBackendsOfOneTeamOnOneHostApart (work item 341): two
// servers behind one host and port, under two paths, both joined for one
// team. `--on <host>` names both. Until this the refusal said to add
// --team, which names the team and was already given: what tells the two
// apart is the whole url, and the refusal names both. A membership this
// machine left is no backend to act on, and --team is matched as every
// other team command matches it.
func TestOnTellsTwoBackendsOfOneTeamOnOneHostApart(t *testing.T) {
	t.Parallel()
	const one, two = "https://shared.example.test:8743/one", "https://shared.example.test:8743/two"
	f, dump := passThroughFixture(t, fakeadapter.Script{
		Responses: map[string][]fakeadapter.Response{
			"team revoke-member": {{Result: []byte(`{"team_ref":"t_bob","revoked":1}`)}},
		},
	})
	for name, url := range map[string]string{"bob-one": one, "bob-two": two} {
		if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, name, &adapterkit.Profile{
			Version: adapterkit.ProfileVersion, Adapter: "fake", URL: url, TeamRef: "t_bob", TeamName: "bobteam",
		}); err != nil {
			t.Fatal(err)
		}
	}
	// A membership the machine left: a principal, and no team.
	if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, "bob-left", &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "fake", URL: "https://left.example.test", PrincipalRef: "p-left",
	}); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) error {
		iv := f.inv(f.terminalEnv(), "", append([]string{"revoke-member"}, args...)...)
		iv.Deps.Getwd = func() (string, error) { return f.dirs.Root, nil }
		return Team(iv)
	}
	perr := wantCodeErr(t, run("--team", "bobteam", "--on", "shared.example.test:8743", "--max-version", "1"), protocol.CodeConfig, "team_ambiguous")
	if !strings.Contains(perr.Message, one) || !strings.Contains(perr.Message, two) || strings.Contains(perr.Message, "add --team") {
		t.Fatalf("the refusal of an --on that names two backends of one team: %s", perr.Message)
	}
	wantCode(t, run("--on", "left.example.test", "--max-version", "1"), protocol.CodeConfig, "team_unknown")
	if _, err := osLstat(dump); err == nil {
		t.Fatalf("a refused --on reached the adapter")
	}
	// The whole url names one of the two.
	if err := run("--team", "bobteam", "--on", two, "--max-version", "1"); err != nil {
		t.Fatal(err)
	}
	if invs := readDump(t, dump); len(invs) != 1 || invs[0].Env["BRIGADE_PROFILE"] != "bob-two" {
		t.Fatalf("the adapter was asked %+v, want one call under bob-two", invs)
	}
	// And the line that says which backend the command acts on names that one:
	// the host both are behind would not say which.
	if said := `brigade: team revoke-member acts on team "bobteam" (t_bob) at ` + two + "\n"; !strings.Contains(f.errb.String(), said) {
		t.Fatalf("stderr = %q, want %q: the whole address, where the host names two backends of the team", f.errb.String(), said)
	}
}

// TestABackendIsNamedByItsWholeAddressWhereItsHostNamesTwoOfTheTeam (work
// item 341): the one rule for the line that says which backend a team
// command acts on and for the `--from` of a command a line prints. A host
// names a backend as long as it names one backend of the team; where it
// names another too, the whole address does. Another team's backend on the
// host changes nothing, and neither does the backend's own membership.
func TestABackendIsNamedByItsWholeAddressWhereItsHostNamesTwoOfTheTeam(t *testing.T) {
	t.Parallel()
	joined := func(team, url string) *adapterkit.Profile {
		return &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: "rest", URL: url, TeamRef: team}
	}
	const one, two = "https://one.example.test:8743", "https://two.example.test"
	bindings := []*adapterkit.Profile{
		joined("t_a", one+"/a"), joined("t_a", one+"/b"), joined("t_a", two), joined("t_b", two+"/another-team"),
	}
	for _, tc := range []struct{ why, team, url, want string }{
		{"two of the team behind one host", "t_a", one + "/a", one + "/a"},
		{"the other of the two", "t_a", one + "/b", one + "/b"},
		{"a server not joined yet, on a host that names two of the team", "t_a", one + "/c", one + "/c"},
		{"the only one of the team on its host, beside another team's", "t_a", two, "two.example.test"},
		{"another team's, the only one of that team on the host", "t_b", two + "/another-team", "two.example.test"},
		{"a host that names nothing else", "t_a", "https://three.example.test:8743", "three.example.test:8743"},
	} {
		if got := backendName(bindings, tc.team, tc.url); got != tc.want {
			t.Errorf("%s: backendName(%q) = %q, want %q", tc.why, tc.url, got, tc.want)
		}
	}
}
