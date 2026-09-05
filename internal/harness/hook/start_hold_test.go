package hook

import (
	"encoding/json/v2"
	"os"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/policy"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestScanStillForcesRefuseOverAHoldOption is 3.6's regression test: a
// cwd settings.json with crossSessionInbound "hold" AND a team_inbound
// option of hold gives a map of refuse, and the context line is
// Scan.Warning() with its P5-9 clause. Brigade's release path ends in a
// socket post; under a native hold or refuse that post is lost while
// Brigade would acknowledge it, so the scan wins over the option.
func TestScanStillForcesRefuseOverAHoldOption(t *testing.T) {
	t.Parallel()
	settings := "/work/project/.claude/settings.json"
	for _, native := range []string{"hold", "refuse"} {
		t.Run("native "+native, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			f.deps.ReadFile = func(p string) ([]byte, error) {
				if p == settings {
					return []byte(`{"crossSessionInbound": "` + native + `"}`), nil
				}
				return nil, os.ErrNotExist
			}
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
			exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"), config.OptionTeamInbound+"=hold")
			if exit != 0 {
				t.Fatal(exit)
			}
			got := lines(out)
			want := policy.Scan{Found: true, Value: native, File: settings}.Warning()
			if len(got) != 2 || !strings.Contains(got[0], "; inbound: refuse;") || got[1] != want {
				t.Fatalf("lines %q, want the start line with refuse and the scan warning %q", got, want)
			}
			if !strings.Contains(got[1], `team_inbound "hold"`) || !strings.Contains(got[1], "`brigade inbox`") {
				t.Fatalf("the warning lacks the P5-9 clause: %q", got[1])
			}
			if m := f.mustMap(); m.Inbound != protocol.InboundRefuse {
				t.Fatalf("map inbound %q, want refuse", m.Inbound)
			}
			var reg protocol.SessionRegistration
			if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
				t.Fatal(err)
			}
			if reg.Inbound != protocol.InboundRefuse {
				t.Fatalf("registered inbound %q", reg.Inbound)
			}
			if spec := f.spawner.last(t); envValue(spec.Env, "BRIGADE_TEAM_INBOUND") != "refuse" {
				t.Fatalf("watcher BRIGADE_TEAM_INBOUND=%q", envValue(spec.Env, "BRIGADE_TEAM_INBOUND"))
			}
		})
	}
}

// TestHoldOptionWritesHoldToTheMap: with no scan hit the hold option is
// the map's policy, the registration's `inbound`, the watcher's
// BRIGADE_TEAM_INBOUND, and the context line — with no warning.
func TestHoldOptionWritesHoldToTheMap(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
	exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"), config.OptionTeamInbound+"=hold")
	if exit != 0 {
		t.Fatal(exit)
	}
	got := lines(out)
	if len(got) != 1 || !strings.Contains(got[0], "; inbound: hold;") {
		t.Fatalf("lines %q, want the start line with hold and nothing else", got)
	}
	if m := f.mustMap(); m.Inbound != protocol.InboundHold {
		t.Fatalf("map inbound %q", m.Inbound)
	}
	var reg protocol.SessionRegistration
	if err := json.Unmarshal(seam.callsFor("session register")[0].Stdin, &reg); err != nil {
		t.Fatal(err)
	}
	if reg.Inbound != protocol.InboundHold {
		t.Fatalf("registered inbound %q", reg.Inbound)
	}
	if spec := f.spawner.last(t); envValue(spec.Env, "BRIGADE_TEAM_INBOUND") != "hold" {
		t.Fatalf("watcher BRIGADE_TEAM_INBOUND=%q", envValue(spec.Env, "BRIGADE_TEAM_INBOUND"))
	}
}
