package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
)

// --here and --member of `brigade sessions` (card 34): filters on what is
// shown, with a note that counts what they left out.

// filterRoster is a team across two repositories: three sessions in
// `brigade` (one of them offline, one this session), one in `website`,
// and one that shares no repository name. frank has no label.
func filterRoster() string {
	rec := func(id, name, label, principal, repo, state string) map[string]any {
		r := rosterRec(id, name, state, "accept")
		r["principal_ref"] = principal
		if label != "" {
			r["human_label"] = label
		}
		if repo != "" {
			r["workspace_label"] = repo
		}
		return r
	}
	return rosterJSON(false,
		rec("11111111111111111111111111111111", "reviewer", "erin@example.com", "e1e1e1e1-principal-erin", "brigade", "active"),
		rec("22222222222222222222222222222222", "landing-page", "erin@example.com", "e1e1e1e1-principal-erin", "website", "idle"),
		rec("33333333333333333333333333333333", "quiet", "", "f2f2f2f2-principal-frank", "", "idle"),
		rec("44444444444444444444444444444444", "erin@example.com", "gus@example.com", "a3a3a3a3-principal-gus", "brigade", "offline"),
		rec(selfSessionID, selfSessionName, "alice@example.com", "a4a4a4a4-principal-alice", "brigade", "active"),
	)
}

// hereFixture is a session that registered the repository name `brigade`.
func hereFixture(t *testing.T, workspaceLabel string) *fixture {
	t.Helper()
	f := newFixture(t)
	m := f.byPID()
	m.WorkspaceLabel = workspaceLabel
	f.writeMap(t, m)
	f.rec.on("session list", okAnswer(filterRoster()))
	return f
}

// shown lists the SESSION cells of the rows a run printed, in order.
func shown(out string) []string {
	var ids []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n")[1:] {
		if line == "" || strings.HasPrefix(line, "(") || strings.HasPrefix(line, " ") {
			continue
		}
		ids = append(ids, strings.Fields(line)[0])
	}
	return ids
}

func sameIDs(got []string, want ...string) bool {
	return strings.Join(got, " ") == strings.Join(want, " ")
}

// TestSessionsHere: only the sessions of this session's repository. The
// offline one among them is hidden as offline, not as filtered; the note
// counts what --here left out and says how many of those share no
// repository name — they may be here, and --here cannot know.
func TestSessionsHere(t *testing.T) {
	t.Parallel()
	f := hereFixture(t, "brigade")
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{Here: true}); err != nil {
		t.Fatalf("sessions --here: %v", err)
	}
	out := f.out.String()
	if got := shown(out); !sameIDs(got, "11111", "aaaaa") {
		t.Errorf("rows %v, want the two online sessions of brigade:\n%s", got, out)
	}
	for _, want := range []string{
		"(2 sessions left out by --here; 1 of them share no repository name and may be here)\n",
		"(1 offline sessions hidden; --all shows them)\n",
		RosterUnverifiedNote + "\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}

	f.out.Reset()
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{Here: true, All: true}); err != nil {
		t.Fatalf("sessions --here --all: %v", err)
	}
	if got := shown(f.out.String()); !sameIDs(got, "11111", "aaaaa", "44444") {
		t.Errorf("--all rows %v, want the offline session of brigade too:\n%s", got, f.out.String())
	}

	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "")
	inv.JSON = true
	if err := Sessions(inv, SessionsOptions{Here: true}); err != nil {
		t.Fatalf("sessions --here --json: %v", err)
	}
	_, result := envelopeOf(t, f.out.String())
	if result["filtered_out"] != float64(2) || result["offline_hidden"] != float64(1) || result["here"] != "brigade" ||
		result["filter_note"] != nil || len(result["sessions"].([]any)) != 2 {
		t.Errorf("result = %v", result)
	}
}

// TestSessionsHereThatCannotFilter: a session that shares no repository
// name has nothing to compare, so --here shows the roster whole and says
// so. An empty table would claim that nobody is here.
func TestSessionsHereThatCannotFilter(t *testing.T) {
	t.Parallel()
	f := hereFixture(t, "")
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{Here: true}); err != nil {
		t.Fatalf("sessions --here: %v", err)
	}
	out := f.out.String()
	if got := shown(out); !sameIDs(got, "11111", "aaaaa", "22222", "33333") {
		t.Errorf("rows %v, want every online session:\n%s", got, out)
	}
	if !strings.Contains(out, HereNoLabelNote+"\n") || strings.Contains(out, "left out") {
		t.Errorf("notes:\n%s", out)
	}

	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "")
	inv.JSON = true
	if err := Sessions(inv, SessionsOptions{Here: true}); err != nil {
		t.Fatalf("sessions --here --json: %v", err)
	}
	_, result := envelopeOf(t, f.out.String())
	if result["filtered_out"] != float64(0) || result["filter_note"] != HereNoLabelNote || result["here"] != nil {
		t.Errorf("result = %v", result)
	}
}

// TestSessionsHereInATerminal: with no session "here" is the repository
// the working directory is in, named as the hook names a session's — and
// outside any repository --here says it cannot filter.
func TestSessionsHereInATerminal(t *testing.T) {
	t.Parallel()
	terminal := func(t *testing.T, cwd string) *fixture {
		t.Helper()
		f := newFixture(t)
		if err := config.RegisterAdapter(f.dirs.BrigadeConfig, "betafake", []string{f.adapterPath, "--root", "/y"}); err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.SaveProfile(f.dirs.BrigadeConfig, "beta", &adapterkit.Profile{
			Version: adapterkit.ProfileVersion, Adapter: "betafake",
			URL: "https://example.invalid", PublishableKey: "k",
			TeamRef: "t_beta", TeamName: "betateam",
		}); err != nil {
			t.Fatal(err)
		}
		f.rec.on("session list", okAnswer(filterRoster()))
		inv := f.inv(f.terminalEnv(), "")
		inv.Deps.Getwd = func() (string, error) { return cwd, nil }
		if err := Sessions(inv, SessionsOptions{Here: true, Team: "t_beta"}); err != nil {
			t.Fatalf("sessions --here in a terminal: %v", err)
		}
		return f
	}

	t.Run("inside a repository", func(t *testing.T) {
		t.Parallel()
		repo := filepath.Join(t.TempDir(), "website")
		if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(repo, "docs", "deep"), 0o700); err != nil {
			t.Fatal(err)
		}
		f := terminal(t, filepath.Join(repo, "docs", "deep"))
		if got := shown(f.out.String()); !sameIDs(got, "22222") {
			t.Errorf("rows %v, want the session of website:\n%s", got, f.out.String())
		}
		if !strings.Contains(f.out.String(), "(4 sessions left out by --here; 1 of them share no repository name and may be here)\n") {
			t.Errorf("notes:\n%s", f.out.String())
		}
	})
	t.Run("outside any repository", func(t *testing.T) {
		t.Parallel()
		f := terminal(t, t.TempDir())
		if got := shown(f.out.String()); len(got) != 4 || !strings.Contains(f.out.String(), HereNoRepositoryNote+"\n") {
			t.Errorf("rows %v:\n%s", got, f.out.String())
		}
	})
}

// TestSessionsMember: one member's sessions, named by their label as the
// MEMBER column shows it, or by the beginning of their principal_ref — as
// typed, or as the bracketed cell of a session with no label. A session's
// NAME is never compared, and the value is never printed back.
func TestSessionsMember(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, who string
		all       bool
		want      []string
		leftOut   string
	}{
		{"a label", "erin@example.com", false, []string{"11111", "22222"}, "3"},
		// gus NAMED his session after erin's label: a name is not compared.
		{"a label, offline sessions too", "erin@example.com", true, []string{"11111", "22222"}, "3"},
		{"the first eight characters of a principal", "f2f2f2f2", false, []string{"33333"}, "4"},
		{"more of a principal", "f2f2f2f2-principal", false, []string{"33333"}, "4"},
		{"the MEMBER cell of a session with no label", "[f2f2f2f2]", false, []string{"33333"}, "4"},
		{"surrounded by whitespace", "  erin@example.com\t", false, []string{"11111", "22222"}, "3"},
		{"seven characters of a principal", "f2f2f2f", false, nil, "5"},
		{"the middle of a principal", "principal-frank", false, nil, "5"},
		{"a label in another case", "Erin@example.com", false, nil, "5"},
		{"a session name", "reviewer", false, nil, "5"},
		{"an offline member without --all", "gus@example.com", false, nil, "4"},
		{"an offline member with --all", "gus@example.com", true, []string{"44444"}, "4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := hereFixture(t, "brigade")
			if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{Member: tc.who, All: tc.all}); err != nil {
				t.Fatalf("sessions --member: %v", err)
			}
			out := f.out.String()
			if got := shown(out); !sameIDs(got, tc.want...) {
				t.Errorf("rows %v, want %v:\n%s", got, tc.want, out)
			}
			if want := "(" + tc.leftOut + " sessions left out by --member)\n"; !strings.Contains(out, want) {
				t.Errorf("output lacks %q:\n%s", want, out)
			}
			// The header is printed even when no row is, and the value —
			// argv — is in the output only as a cell of a row it matched.
			if !strings.HasPrefix(out, "SESSION") {
				t.Errorf("no header:\n%s", out)
			}
			if len(tc.want) == 0 && strings.Contains(out, strings.TrimSpace(tc.who)) {
				t.Errorf("the value of --member was printed back:\n%s", out)
			}
		})
	}
}

// TestSessionsHereAndMember: both filters hold at once, one note counts
// what either left out, and a --here that cannot filter leaves --member
// working.
func TestSessionsHereAndMember(t *testing.T) {
	t.Parallel()
	f := hereFixture(t, "brigade")
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{Here: true, Member: "erin@example.com"}); err != nil {
		t.Fatal(err)
	}
	out := f.out.String()
	if got := shown(out); !sameIDs(got, "11111") {
		t.Errorf("rows %v, want erin's session in brigade:\n%s", got, out)
	}
	if !strings.Contains(out, "(4 sessions left out by --here and --member; 1 of them share no repository name and may be here)\n") {
		t.Errorf("notes:\n%s", out)
	}

	g := hereFixture(t, "")
	inv := g.inv(g.sessionEnv(), "")
	inv.JSON = true
	if err := Sessions(inv, SessionsOptions{Here: true, Member: "erin@example.com"}); err != nil {
		t.Fatal(err)
	}
	_, result := envelopeOf(t, g.out.String())
	if result["filtered_out"] != float64(3) || result["filter_note"] != HereNoLabelNote || len(result["sessions"].([]any)) != 2 {
		t.Errorf("result = %v", result)
	}
	g.out.Reset()
	if err := Sessions(g.inv(g.sessionEnv(), ""), SessionsOptions{Here: true, Member: "erin@example.com"}); err != nil {
		t.Fatal(err)
	}
	if out := g.out.String(); !strings.Contains(out, HereNoLabelNote+"\n(3 sessions left out by --member)\n") {
		t.Errorf("notes:\n%s", out)
	}
}

// TestSessionsWithoutFiltersSaysNothingOfThem: the plain roster carries no
// filter note, and --json says zero were left out.
func TestSessionsWithoutFiltersSaysNothingOfThem(t *testing.T) {
	t.Parallel()
	f := hereFixture(t, "brigade")
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{}); err != nil {
		t.Fatal(err)
	}
	if out := f.out.String(); strings.Contains(out, "left out") || strings.Contains(out, "--here") || len(shown(out)) != 4 {
		t.Errorf("plain roster:\n%s", out)
	}
	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "")
	inv.JSON = true
	if err := Sessions(inv, SessionsOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, result := envelopeOf(t, f.out.String()); result["filtered_out"] != float64(0) || result["here"] != nil || result["filter_note"] != nil {
		t.Errorf("result = %v", result)
	}
}
