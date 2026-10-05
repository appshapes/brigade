package policy

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// Every permission mode and entrypoint, crossed with every option value
// and the scan outcomes (plan 9.5: "every permission_mode × entrypoint ×
// option value"). The entrypoint is the diagnostic D18 says the policy
// ignores; the mode is read for exactly one rule since card 50, and the
// table pins which rows it touches.
var (
	permissionModes = []string{"default", "plan", "acceptEdits", "auto", "dontAsk", "bypassPermissions", ""}
	entrypoints     = []struct {
		name           string
		entrypoint     string
		nonInteractive bool
	}{
		{"cli", "cli", false},
		{"sdk-cli", "sdk-cli", true},
		{"unknown", "something-new", false},
		{"unset", "", false},
	}
)

// optionCase is one team_inbound option as config.ParseInbound resolves it,
// with the policy it must produce absent any native hit.
type optionCase struct {
	name    string
	raw     string
	want    Policy
	warning string // the exact config warning expected, "" for none
}

func optionCases(t *testing.T) []optionCase {
	t.Helper()
	cases := []optionCase{
		{name: "unset", raw: "", want: Accept},
		{name: "accept", raw: "accept", want: Accept},
		{name: "refuse", raw: "refuse", want: Refuse},
		{name: "hold", raw: "hold", want: Hold},
		{name: "invalid", raw: "sometimes", want: Refuse, warning: config.WarnInboundInvalid},
	}
	// Positive control on the fixture itself: ParseInbound must agree with
	// the table, or the table proves nothing about the real inputs.
	for _, c := range cases {
		got, warn := config.ParseInbound(c.raw)
		if Policy(got) != c.want || warn != c.warning {
			t.Fatalf("fixture %s: ParseInbound(%q) = (%q, %q), table expects (%q, %q)", c.name, c.raw, got, warn, c.want, c.warning)
		}
	}
	return cases
}

func TestDecideTableModeRuleAndEntrypoint(t *testing.T) {
	t.Parallel()
	scans := []struct {
		name string
		scan Scan
	}{
		{"none", Scan{UserFile: "/x/settings.json"}},
		{"user accept", Scan{UserFile: "/x/settings.json", UserAccept: true}},
		{"repo accept only", Scan{UserFile: "/x/settings.json", RepoAccept: "/w/.claude/settings.json"}},
		{"hold", Scan{Found: true, Value: NativeHold, File: "/x/settings.json", UserFile: "/x/settings.json"}},
		{"refuse", Scan{Found: true, Value: NativeRefuse, File: "/x/.claude/settings.local.json", UserFile: "/x/settings.json"}},
	}
	rows := 0
	for _, oc := range optionCases(t) {
		for _, sc := range scans {
			for _, mode := range permissionModes {
				want := oc.want
				parity := false
				switch {
				case sc.scan.Found:
					want = Refuse
				case want != Refuse && mode == PermissionBypass && !sc.scan.UserAccept:
					// The card 50 rule: the one row set the mode decides.
					want, parity = Refuse, true
				}
				for _, ep := range entrypoints {
					rows++
					in := Inputs{
						Option:         config.Inbound(mustPolicy(oc.raw)),
						OptionWarning:  oc.warning,
						Native:         sc.scan,
						PermissionMode: mode,
						NonInteractive: ep.nonInteractive,
						Entrypoint:     ep.entrypoint,
					}
					d := Decide(in)
					if d.Policy != want {
						t.Errorf("option=%s scan=%s mode=%q entrypoint=%s: policy %q, want %q", oc.name, sc.name, mode, ep.name, d.Policy, want)
					}
					if d.PermissionMode != mode || d.NonInteractive != ep.nonInteractive || d.Entrypoint != ep.entrypoint {
						t.Errorf("option=%s scan=%s: diagnostics not echoed: %+v", oc.name, sc.name, d)
					}
					wantWarnings := 0
					if oc.warning != "" {
						wantWarnings++
					}
					if sc.scan.Found || parity {
						wantWarnings++
					}
					if len(d.Warnings) != wantWarnings {
						t.Errorf("option=%s scan=%s mode=%q: %d warnings %q, want %d", oc.name, sc.name, mode, len(d.Warnings), d.Warnings, wantWarnings)
					}
					if parity && d.Warnings[len(d.Warnings)-1] != sc.scan.ParityWarning() {
						t.Errorf("option=%s scan=%s mode=%q: last warning %q, want the parity warning", oc.name, sc.name, mode, d.Warnings[len(d.Warnings)-1])
					}
				}
			}
		}
	}
	// 5 options × 5 scans × 7 modes × 4 entrypoints.
	if rows != 5*5*7*4 {
		t.Fatalf("table covered %d rows, want %d", rows, 5*5*7*4)
	}
}

// mustPolicy resolves a raw option through the real parser, so the table
// exercises the same values the hook would pass.
func mustPolicy(raw string) string {
	p, _ := config.ParseInbound(raw)
	return string(p)
}

func TestEffectiveAcceptIsTheDefaultEverywhere(t *testing.T) {
	t.Parallel()
	for _, mode := range permissionModes {
		if mode == PermissionBypass {
			continue // the one mode with a rule of its own, below
		}
		p, warnings := Effective(config.InboundAccept, "", Scan{}, mode)
		if p != Accept || len(warnings) != 0 {
			t.Fatalf("accept/no scan/mode %q: (%q, %q), want (accept, none)", mode, p, warnings)
		}
	}
	// Positive control: the same call with a native hit flips to Refuse,
	// so the accept above is not the function ignoring its inputs.
	p, warnings := Effective(config.InboundAccept, "", Scan{Found: true, Value: NativeRefuse, File: "/f"}, "default")
	if p != Refuse || len(warnings) != 1 {
		t.Fatalf("accept/native refuse: (%q, %q), want (refuse, one warning)", p, warnings)
	}
}

// TestEffectiveBypassNeedsTheUserAccept is the card 50 rule in isolation:
// in bypassPermissions the policy is Refuse with the parity warning unless
// the user file carries the accept; a repository accept changes nothing
// but the warning's text; a native hold or refuse wins over the user
// accept with its own warning, never both; an option of refuse warns
// nothing (the user asked for refuse); and the mode "" decides nothing.
func TestEffectiveBypassNeedsTheUserAccept(t *testing.T) {
	t.Parallel()
	none := Scan{UserFile: "/cfg/settings.json"}
	repo := Scan{UserFile: "/cfg/settings.json", RepoAccept: "/w/.claude/settings.json"}
	user := Scan{UserFile: "/cfg/settings.json", UserAccept: true}
	for _, tc := range []struct {
		name   string
		option config.Inbound
		scan   Scan
		mode   string
		want   Policy
		warns  []string
	}{
		{"accept, nothing set", config.InboundAccept, none, PermissionBypass, Refuse, []string{none.ParityWarning()}},
		{"hold, nothing set", config.InboundHold, none, PermissionBypass, Refuse, []string{none.ParityWarning()}},
		{"refuse, nothing set", config.InboundRefuse, none, PermissionBypass, Refuse, nil},
		{"accept, repo accept only", config.InboundAccept, repo, PermissionBypass, Refuse, []string{repo.ParityWarning()}},
		{"accept, user accept", config.InboundAccept, user, PermissionBypass, Accept, nil},
		{"hold, user accept", config.InboundHold, user, PermissionBypass, Hold, nil},
		{"user accept under a native hold", config.InboundAccept, Scan{Found: true, Value: NativeHold, File: "/w/.claude/settings.json", UserFile: "/cfg/settings.json", UserAccept: true}, PermissionBypass, Refuse, []string{Scan{Found: true, Value: NativeHold, File: "/w/.claude/settings.json", UserFile: "/cfg/settings.json", UserAccept: true}.Warning()}},
		{"mode unknown decides nothing", config.InboundAccept, none, "", Accept, nil},
		{"plan is read as prompting", config.InboundAccept, none, "plan", Accept, nil},
		{"auto is prompting", config.InboundAccept, none, "auto", Accept, nil},
	} {
		p, warnings := Effective(tc.option, "", tc.scan, tc.mode)
		if p != tc.want || strings.Join(warnings, "\x00") != strings.Join(tc.warns, "\x00") {
			t.Errorf("%s: (%q, %q), want (%q, %q)", tc.name, p, warnings, tc.want, tc.warns)
		}
	}
	if !BypassesPrompts(PermissionBypass) || BypassesPrompts("plan") || BypassesPrompts("") || BypassesPrompts("BypassPermissions") {
		t.Fatal("BypassesPrompts must be exactly the bypassPermissions mode")
	}
}

// TestParityWarningText pins Scan.ParityWarning character for character,
// in its three shapes: a user file with no repository accept, a repository
// accept to explain away, and no known user file.
func TestParityWarningText(t *testing.T) {
	t.Parallel()
	const tail = ` Brigade cannot see managed settings or --settings; an "accept" there needs this line too.`
	got := Scan{UserFile: "/home/u/.claude-work/settings.json"}.ParityWarning()
	want := `Brigade: this session bypasses permission prompts and /home/u/.claude-work/settings.json has no "crossSessionInbound": "accept", ` +
		`so Claude Code would hold every Brigade message for your approval in a dialog that expires (and drop it unseen in a headless session); ` +
		`Brigade's inbound policy is refuse instead (nothing is acknowledged blind; messages wait on the server). ` +
		`To receive team messages, add "crossSessionInbound": "accept" to /home/u/.claude-work/settings.json — Brigade applies it at your next prompt.` + tail
	if got != want {
		t.Fatalf("ParityWarning():\n got %q\nwant %q", got, want)
	}
	got = Scan{UserFile: "/home/u/.claude-work/settings.json", RepoAccept: "/work/repo/.claude/settings.json"}.ParityWarning()
	if !strings.HasPrefix(got, want[:len(want)-len(tail)]) || !strings.HasSuffix(got, tail) ||
		!strings.Contains(got, ` The "accept" in /work/repo/.claude/settings.json does not count: Claude Code lets a repository only tighten this setting.`) {
		t.Fatalf("with a repository accept: %q", got)
	}
	got = Scan{}.ParityWarning()
	if !strings.Contains(got, "your Claude Code user settings.json has no") || !strings.Contains(got, `to your Claude Code user settings.json —`) {
		t.Fatalf("with no user file: %q", got)
	}
	for _, w := range []string{want, got} {
		if strings.Contains(w, "\n") {
			t.Errorf("warning is not one line: %q", w)
		}
	}
}

func TestEffectiveWarningsInOrder(t *testing.T) {
	t.Parallel()
	scan := Scan{Found: true, Value: NativeHold, File: "/cfg/settings.json"}
	p, warnings := Effective(config.InboundRefuse, config.WarnInboundInvalid, scan, "default")
	if p != Refuse {
		t.Fatalf("policy %q, want refuse", p)
	}
	if len(warnings) != 2 || warnings[0] != config.WarnInboundInvalid || warnings[1] != scan.Warning() {
		t.Fatalf("warnings = %q, want [option warning, scan warning]", warnings)
	}
}

func TestEffectiveUnknownOptionFailsClosed(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"", "HOLD", "ACCEPT", "yes"} {
		p, warnings := Effective(config.Inbound(raw), "", Scan{}, "default")
		if p != Refuse {
			t.Errorf("option %q: policy %q, want refuse (fail closed)", raw, p)
		}
		if len(warnings) != 1 || warnings[0] != WarnOptionUnknown {
			t.Errorf("option %q: warnings %q, want [WarnOptionUnknown]", raw, warnings)
		}
	}
	// With a warning already supplied the caller's text is kept, not
	// replaced.
	_, warnings := Effective(config.Inbound("weird"), "custom", Scan{}, "default")
	if len(warnings) != 1 || warnings[0] != "custom" {
		t.Fatalf("warnings %q, want [custom]", warnings)
	}
}

func TestPolicyStringAndValid(t *testing.T) {
	t.Parallel()
	if Accept.String() != "accept" || Hold.String() != "hold" || Refuse.String() != "refuse" {
		t.Fatalf("String: %q %q %q", Accept, Hold, Refuse)
	}
	if !Accept.Valid() || !Hold.Valid() || !Refuse.Valid() {
		t.Fatal("accept, hold and refuse must be valid")
	}
	for _, bad := range []Policy{"", "HOLD", "Accept", "auto"} {
		if bad.Valid() {
			t.Errorf("%q must not be valid", bad)
		}
	}
}

// TestHoldIsAPolicyAndTheScanStillForcesRefuse is D18's value set after
// P5-9: the option yields Hold with no warning, and a native hold or
// refuse forces Refuse over it (3.6) — the scan wins, the option's hold is
// overridden, and both facts are in the warnings.
func TestHoldIsAPolicyAndTheScanStillForcesRefuse(t *testing.T) {
	t.Parallel()
	if d := Decide(Inputs{Option: config.InboundHold}); d.Policy != Hold || len(d.Warnings) != 0 {
		t.Fatalf("hold option, no scan: %+v, want hold and no warning", d)
	}
	for _, scan := range []Scan{
		{Found: true, Value: NativeHold, File: "/f/settings.json"},
		{Found: true, Value: NativeRefuse, File: "/f/.claude/settings.local.json"},
	} {
		d := Decide(Inputs{Option: config.InboundHold, Native: scan})
		if d.Policy != Refuse || len(d.Warnings) != 1 || d.Warnings[0] != scan.Warning() {
			t.Errorf("hold option over native %s: %+v, want refuse with the scan's warning", scan.Value, d)
		}
	}
	for _, w := range []string{WarnOptionUnknown, config.WarnInboundInvalid, Scan{Found: true, Value: NativeHold, File: "/f"}.Warning()} {
		if strings.Contains(w, "\n") {
			t.Errorf("warning is not one line: %q", w)
		}
	}
}

// TestScanWarningText pins Scan.Warning character for character, the P5-9
// clause included: it names hold as Brigade's own review option, says the
// native setting must still be accept, and points at `brigade inbox`.
func TestScanWarningText(t *testing.T) {
	t.Parallel()
	got := Scan{Found: true, Value: NativeRefuse, File: "/home/u/.claude/settings.json"}.Warning()
	want := `Brigade: your Claude Code settings set "crossSessionInbound": "refuse" in /home/u/.claude/settings.json; ` +
		`Claude Code would not deliver Brigade messages to this session, so Brigade's inbound policy is refuse ` +
		`(nothing is acknowledged blind; messages wait on the server). Remove that setting, or set it to "accept", to receive team messages; ` +
		`Brigade's own team_inbound "hold" reviews messages in a terminal before delivery, but it still needs Claude Code's setting to be "accept". ` +
		"Run `brigade inbox` in a terminal to read what is waiting."
	if got != want {
		t.Fatalf("Warning():\n got %q\nwant %q", got, want)
	}
	if (Scan{}).Warning() != "" {
		t.Fatal("a scan that found nothing must warn nothing")
	}
}
