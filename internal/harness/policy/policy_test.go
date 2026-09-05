package policy

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// The diagnostics D18 says the policy ignores, crossed with every option
// value and both scan outcomes (plan 9.5: "every permission_mode ×
// entrypoint × option value").
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

func TestDecideTableIgnoresModeAndEntrypoint(t *testing.T) {
	t.Parallel()
	scans := []struct {
		name string
		scan Scan
	}{
		{"none", Scan{}},
		{"hold", Scan{Found: true, Value: NativeHold, File: "/x/settings.json"}},
		{"refuse", Scan{Found: true, Value: NativeRefuse, File: "/x/.claude/settings.local.json"}},
	}
	rows := 0
	for _, oc := range optionCases(t) {
		for _, sc := range scans {
			want := oc.want
			if sc.scan.Found {
				want = Refuse
			}
			for _, mode := range permissionModes {
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
					if sc.scan.Found {
						wantWarnings++
					}
					if len(d.Warnings) != wantWarnings {
						t.Errorf("option=%s scan=%s: %d warnings %q, want %d", oc.name, sc.name, len(d.Warnings), d.Warnings, wantWarnings)
					}
				}
			}
		}
	}
	// 5 options × 3 scans × 7 modes × 4 entrypoints.
	if rows != 5*3*7*4 {
		t.Fatalf("table covered %d rows, want %d", rows, 5*3*7*4)
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
	p, warnings := Effective(config.InboundAccept, "", Scan{})
	if p != Accept || len(warnings) != 0 {
		t.Fatalf("accept/no scan: (%q, %q), want (accept, none)", p, warnings)
	}
	// Positive control: the same call with a native hit flips to Refuse,
	// so the accept above is not the function ignoring its inputs.
	p, warnings = Effective(config.InboundAccept, "", Scan{Found: true, Value: NativeRefuse, File: "/f"})
	if p != Refuse || len(warnings) != 1 {
		t.Fatalf("accept/native refuse: (%q, %q), want (refuse, one warning)", p, warnings)
	}
}

func TestEffectiveWarningsInOrder(t *testing.T) {
	t.Parallel()
	scan := Scan{Found: true, Value: NativeHold, File: "/cfg/settings.json"}
	p, warnings := Effective(config.InboundRefuse, config.WarnInboundInvalid, scan)
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
		p, warnings := Effective(config.Inbound(raw), "", Scan{})
		if p != Refuse {
			t.Errorf("option %q: policy %q, want refuse (fail closed)", raw, p)
		}
		if len(warnings) != 1 || warnings[0] != WarnOptionUnknown {
			t.Errorf("option %q: warnings %q, want [WarnOptionUnknown]", raw, warnings)
		}
	}
	// With a warning already supplied the caller's text is kept, not
	// replaced.
	_, warnings := Effective(config.Inbound("weird"), "custom", Scan{})
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
