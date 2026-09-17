package hook

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// The FIRST registration of a session — the one SessionStart sends —
// carries the member's default human_label, so a membership with an empty
// label is filled at the next session start after the plugin update (card
// 24, part C). The `label` option decides what is sent, and the option,
// never a label, is what the by-pid map hands the watcher for its own
// re-registrations.

const accountEmail = "member@example.com"

// registerWithAccountLabel runs SessionStart with an account email on disk
// and returns the registration the adapter saw and the `label` option the
// by-pid map carries.
func registerWithAccountLabel(t *testing.T, email string, extraEnv ...string) (protocol.SessionRegistration, string) {
	t.Helper()
	f := newFixture(t)
	f.seedTeam(t)
	if email != "" {
		doc := `{"oauthAccount":{"emailAddress":"` + email + `"},"numStartups":3}`
		if err := os.WriteFile(filepath.Join(f.dirs.ClaudeConfig, ".claude.json"), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
	exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup"), extraEnv...)
	if exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	var reg protocol.SessionRegistration
	if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
		t.Fatal(err)
	}
	return reg, f.mustMap().LabelOption
}

func TestSessionStartRegistersTheAccountLabel(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		email     string
		env       []string
		want      string // "" means the member must be absent from the wire
		wantOptIn string // the option the by-pid map must carry
	}{
		{
			name: "the default is the claude account email", email: accountEmail,
			want: accountEmail, wantOptIn: config.LabelAccount,
		},
		{
			name: "label none sends nothing", email: accountEmail,
			env: []string{config.OptionLabel + "=none"}, want: "", wantOptIn: config.LabelNone,
		},
		{
			name: "a literal label sends that text", email: accountEmail,
			env: []string{config.OptionLabel + "=Rae on the laptop"}, want: "Rae on the laptop", wantOptIn: "Rae on the laptop",
		},
		{
			// Best effort: no account file at all is an empty label and a
			// registration that succeeds, exactly as before this version.
			name: "an unreadable account sends nothing and registers anyway", email: "",
			want: "", wantOptIn: config.LabelAccount,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			reg, option := registerWithAccountLabel(t, tc.email, tc.env...)
			switch {
			case tc.want == "" && reg.HumanLabel != nil:
				t.Errorf("human_label %q on the wire, want the member absent", *reg.HumanLabel)
			case tc.want != "" && reg.HumanLabel == nil:
				t.Errorf("no human_label on the wire, want %q", tc.want)
			case tc.want != "" && *reg.HumanLabel != tc.want:
				t.Errorf("human_label %q, want %q", *reg.HumanLabel, tc.want)
			}
			if option != tc.wantOptIn {
				t.Errorf("by-pid map label_option %q, want %q", option, tc.wantOptIn)
			}
		})
	}
}

// TestTheByPIDMapCarriesTheOptionAndNeverTheEmail is the privacy half of
// the same mechanism: the account email reaches the wire, where the
// membership needs it, and the map on disk carries only the OPTION — so a
// reader of the state directory learns which option was chosen, never the
// member's email (the rule StartFacts already follows).
func TestTheByPIDMapCarriesTheOptionAndNeverTheEmail(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.seedTeam(t)
	doc := `{"oauthAccount":{"emailAddress":"` + accountEmail + `"},"numStartups":3}`
	if err := os.WriteFile(filepath.Join(f.dirs.ClaudeConfig, ".claude.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
	if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
		t.Fatalf("exit %d: %s", exit, errOut)
	}
	p, err := f.store().ByPIDPath(f.pid)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(p) // #nosec G304 -- a path this test just built under its own temp dir
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(accountEmail)) {
		t.Errorf("the by-pid map holds the member's account email; it must carry the label OPTION only:\n%s", raw)
	}
	if got := f.mustMap().LabelOption; got != config.LabelAccount {
		t.Errorf("by-pid map label_option %q, want %q", got, config.LabelAccount)
	}
}
