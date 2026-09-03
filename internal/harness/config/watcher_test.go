package config_test

import (
	"slices"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

func completeWatcherEnv() []string {
	return []string{
		"PATH=/usr/bin", "HOME=/h",
		config.WatcherClaudePIDVar + "=4242",
		config.WatcherProfileVar + "=work",
		config.WatcherConfigDirVar + "=/c/brigade/",
		config.WatcherStateDirVar + "=/s//brigade",
		config.WatcherAdapterCommandVar + `=["/opt/adapter","--root","/x"]`,
		config.WatcherTeamInboundVar + "=refuse",
	}
}

func TestFromWatcherEnvComplete(t *testing.T) {
	t.Parallel()
	got, err := config.FromWatcherEnv(completeWatcherEnv())
	if err != nil {
		t.Fatalf("FromWatcherEnv: %v", err)
	}
	if got.ClaudePID != 4242 || got.Profile != "work" || got.ConfigDir != "/c/brigade" || got.StateDir != "/s/brigade" ||
		got.TeamInbound != config.InboundRefuse || got.TeamInboundWarning != "" {
		t.Fatalf("FromWatcherEnv = %+v", got)
	}
	assertAdapter(t, got.Adapter, []string{"/opt/adapter", "--root", "/x"}, false, config.SourceEnv)
}

func TestFromWatcherEnvTable(t *testing.T) {
	t.Parallel()
	replace := func(name, value string) []string {
		env := completeWatcherEnv()
		out := env[:0:0]
		for _, e := range env {
			if len(e) > len(name) && e[:len(name)+1] == name+"=" {
				continue
			}
			out = append(out, e)
		}
		if value != "" {
			out = append(out, name+"="+value)
		}
		return out
	}
	cases := []struct {
		name    string
		environ []string
		reason  string
		check   func(t *testing.T, w config.WatcherEnv)
	}{
		{"missing pid", replace(config.WatcherClaudePIDVar, ""), config.ReasonWatcherEnvIncomplete, nil},
		{"junk pid", replace(config.WatcherClaudePIDVar, evilMarker), config.ReasonInvalidClaudePID, nil},
		{"zero pid", replace(config.WatcherClaudePIDVar, "0"), config.ReasonInvalidClaudePID, nil},
		{"missing config dir", replace(config.WatcherConfigDirVar, ""), config.ReasonWatcherEnvIncomplete, nil},
		{"relative config dir", replace(config.WatcherConfigDirVar, "rel/"+evilMarker), config.ReasonRelativePath, nil},
		{"missing state dir", replace(config.WatcherStateDirVar, ""), config.ReasonWatcherEnvIncomplete, nil},
		{"relative state dir", replace(config.WatcherStateDirVar, "rel/"+evilMarker), config.ReasonRelativePath, nil},
		{"invalid profile", replace(config.WatcherProfileVar, "../"+evilMarker), config.ReasonInvalidProfileName, nil},
		{"adapter name is refused (resolution is the hook's)", replace(config.WatcherAdapterCommandVar, "fs"), config.ReasonAdapterMalformed, nil},
		{"adapter relative in array", replace(config.WatcherAdapterCommandVar, `["bin/`+evilMarker+`"]`), config.ReasonAdapterRelative, nil},
		{"profile defaults", replace(config.WatcherProfileVar, ""), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			if w.Profile != "default" {
				t.Fatalf("Profile = %q", w.Profile)
			}
		}},
		{"adapter empty is bundled", replace(config.WatcherAdapterCommandVar, ""), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			assertAdapter(t, w.Adapter, nil, true, config.SourceEnv)
		}},
		{"adapter [] is bundled", replace(config.WatcherAdapterCommandVar, "[]"), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			assertAdapter(t, w.Adapter, nil, true, config.SourceEnv)
		}},
		{"adapter absolute path", replace(config.WatcherAdapterCommandVar, "/opt/adapter"), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			assertAdapter(t, w.Adapter, []string{"/opt/adapter"}, false, config.SourceEnv)
		}},
		{"inbound empty is accept", replace(config.WatcherTeamInboundVar, ""), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			if w.TeamInbound != config.InboundAccept || w.TeamInboundWarning != "" {
				t.Fatalf("inbound = %q %q", w.TeamInbound, w.TeamInboundWarning)
			}
		}},
		{"inbound hold is refuse with the warning", replace(config.WatcherTeamInboundVar, "hold"), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			if w.TeamInbound != config.InboundRefuse || w.TeamInboundWarning != config.WarnInboundHold {
				t.Fatalf("inbound = %q %q", w.TeamInbound, w.TeamInboundWarning)
			}
		}},
		{"inbound junk is refuse with the warning", replace(config.WatcherTeamInboundVar, evilMarker), "", func(t *testing.T, w config.WatcherEnv) {
			t.Helper()
			if w.TeamInbound != config.InboundRefuse || w.TeamInboundWarning != config.WarnInboundInvalid {
				t.Fatalf("inbound = %q %q", w.TeamInbound, w.TeamInboundWarning)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.FromWatcherEnv(tc.environ)
			if tc.reason != "" {
				assertConfig(t, err, tc.reason)
				return
			}
			if err != nil {
				t.Fatalf("FromWatcherEnv: %v", err)
			}
			tc.check(t, got)
		})
	}
}

func TestFromWatcherEnvReadsExactlyTheSixVariables(t *testing.T) {
	t.Parallel()
	// Decoys that a confused reader might honour: the hook's option
	// variables, a session's CLAUDE_PID, an adapter-specific variable.
	env := append(completeWatcherEnv(),
		config.OptionProfile+"=optionprofile",
		config.OptionTeamInbound+"=accept",
		config.OptionAdapterCommand+"=/opt/option-adapter",
		"CLAUDE_PID=1",
		"BRIGADE_FS_ROOT=/fs",
		"BRIGADE_LOG_LEVEL=debug",
	)
	got, err := config.FromWatcherEnv(env)
	if err != nil {
		t.Fatal(err)
	}
	if got.ClaudePID != 4242 || got.Profile != "work" || got.TeamInbound != config.InboundRefuse {
		t.Fatalf("a decoy was honoured: %+v", got)
	}
	assertAdapter(t, got.Adapter, []string{"/opt/adapter", "--root", "/x"}, false, config.SourceEnv)
}

func TestWatcherEnvVarsRoundTrip(t *testing.T) {
	t.Parallel()
	want, err := config.FromWatcherEnv(completeWatcherEnv())
	if err != nil {
		t.Fatal(err)
	}
	vars, err := want.Vars()
	if err != nil {
		t.Fatal(err)
	}
	if len(vars) != 6 {
		t.Fatalf("Vars = %q, want six entries", vars)
	}
	for _, v := range vars {
		if !slices.ContainsFunc([]string{config.WatcherClaudePIDVar, config.WatcherProfileVar, config.WatcherConfigDirVar, config.WatcherStateDirVar, config.WatcherAdapterCommandVar, config.WatcherTeamInboundVar}, func(name string) bool {
			return len(v) > len(name) && v[:len(name)+1] == name+"="
		}) {
			t.Fatalf("Vars emitted %q, which is not one of the six", v)
		}
	}
	got, err := config.FromWatcherEnv(vars)
	if err != nil {
		t.Fatalf("FromWatcherEnv(Vars): %v", err)
	}
	if got.ClaudePID != want.ClaudePID || got.Profile != want.Profile || got.ConfigDir != want.ConfigDir || got.StateDir != want.StateDir || got.TeamInbound != want.TeamInbound {
		t.Fatalf("round trip:\n got %+v\nwant %+v", got, want)
	}
	assertAdapter(t, got.Adapter, want.Adapter.Argv, false, config.SourceEnv)

	bundled := config.WatcherEnv{ClaudePID: 1, Profile: "default", ConfigDir: "/c", StateDir: "/s", Adapter: config.Adapter{Bundled: true}, TeamInbound: config.InboundAccept}
	vars, err = bundled.Vars()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(vars, config.WatcherAdapterCommandVar+"=[]") {
		t.Fatalf("bundled Vars = %q", vars)
	}
}
