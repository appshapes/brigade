package config_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// evilMarker is planted in every hostile value so a test can prove the
// value never reaches an error message or its details.
const evilMarker = "EVILMARKER"

// dirs is the benign half of a test environment: HOME and the two XDG
// directories, all absolute and all under the test's own temp dir.
type dirs struct {
	home, xdgConfig, xdgState string
}

func newDirs(t *testing.T) dirs {
	t.Helper()
	root := t.TempDir()
	return dirs{
		home:      filepath.Join(root, "home"),
		xdgConfig: filepath.Join(root, "xdg", "config"),
		xdgState:  filepath.Join(root, "xdg", "state"),
	}
}

// environ returns the benign environment plus extra.
func (d dirs) environ(extra ...string) []string {
	base := []string{"PATH=/usr/bin", "HOME=" + d.home, "XDG_CONFIG_HOME=" + d.xdgConfig, "XDG_STATE_HOME=" + d.xdgState}
	return append(base, extra...)
}

func (d dirs) brigadeConfig() string { return filepath.Join(d.xdgConfig, "brigade") }
func (d dirs) brigadeState() string  { return filepath.Join(d.xdgState, "brigade") }

func asProtocol(t *testing.T, err error) *protocol.Error {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error is %T (%v), want *protocol.Error", err, err)
	}
	return perr
}

// assertConfig checks the shape every refusal shares — the config code,
// exit 11, the reason detail, no echo of the marker anywhere — and returns
// the details for the caller to inspect further.
func assertConfig(t *testing.T, err error, reason string) map[string]string {
	t.Helper()
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeConfig {
		t.Fatalf("code = %q, want config (%v)", perr.Code, err)
	}
	if exit := perr.Code.Exit(); exit != 11 {
		t.Fatalf("exit = %d, want 11", exit)
	}
	if got := perr.Details["reason"]; got != reason {
		t.Fatalf("details.reason = %q, want %q (details %v)", got, reason, perr.Details)
	}
	if strings.Contains(perr.Message, evilMarker) {
		t.Fatalf("message echoes the value: %q", perr.Message)
	}
	for k, v := range perr.Details {
		if strings.Contains(v, evilMarker) {
			t.Fatalf("details[%q] echoes the value: %q", k, v)
		}
	}
	return perr.Details
}

func TestInSession(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		environ []string
		want    bool
	}{
		{"CLAUDE_PID set", []string{"CLAUDE_PID=4242"}, true},
		{"CLAUDE_PID set to junk still counts (the strip rule must not depend on parsing)", []string{"CLAUDE_PID=junk"}, true},
		{"CLAUDE_PID empty is unset", []string{"CLAUDE_PID="}, false},
		{"unset", []string{"HOME=/h"}, false},
		{"nil environ", nil, false},
		// adapterkit.Getenv skips an empty entry rather than letting it
		// clear an earlier one, so a later "CLAUDE_PID=" leaves the session
		// detected: the strip rule fails closed.
		{"a later empty entry does not clear an earlier value", []string{"CLAUDE_PID=1", "CLAUDE_PID="}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := config.InSession(tc.environ); got != tc.want {
				t.Fatalf("InSession(%q) = %v, want %v", tc.environ, got, tc.want)
			}
		})
	}
}

func TestStripRemovesEveryBrigadeEntryAndNothingElse(t *testing.T) {
	t.Parallel()
	in := []string{
		"PATH=/usr/bin", "BRIGADE_PROFILE=evil", "HOME=/h", "BRIGADE_CONFIG_DIR=/tmp/evil", "BRIGADE_",
		"BRIGADE_STATE_DIR=rel", "BRIGADEFOO=kept", "brigade_lower=kept", "CLAUDE_PID=1", "BRIGADE_TEAM_INBOUND=accept",
		"BRIGADE_ADAPTER_COMMAND=/x", "BRIGADE_LOG_LEVEL=debug", "BRIGADE_FS_ROOT=/y", "XBRIGADE_=kept",
	}
	want := []string{"PATH=/usr/bin", "HOME=/h", "BRIGADEFOO=kept", "brigade_lower=kept", "CLAUDE_PID=1", "XBRIGADE_=kept"}
	got := config.Strip(in)
	if !slices.Equal(got, want) {
		t.Fatalf("Strip = %q, want %q", got, want)
	}
	if got := config.Strip(nil); len(got) != 0 {
		t.Fatalf("Strip(nil) = %q", got)
	}
	// Trusted is Strip inside a session and the identity outside.
	if got := config.Trusted(in); !slices.Equal(got, want) {
		t.Fatalf("Trusted(in session) = %q, want %q", got, want)
	}
	outside := slices.DeleteFunc(slices.Clone(in), func(e string) bool { return e == "CLAUDE_PID=1" })
	if got := config.Trusted(outside); !slices.Equal(got, outside) {
		t.Fatalf("Trusted(outside) = %q, want the input unchanged", got)
	}
}

// TestHostileInheritedValuesU27 is U-27's unit half for the directory and
// profile resolvers: every hostile BRIGADE_* value is ignored with
// CLAUDE_PID set and honoured with it unset (exactly as adapterkit does).
func TestHostileInheritedValuesU27(t *testing.T) {
	t.Parallel()
	d := newDirs(t)
	const evilAbs = "/tmp/" + evilMarker
	cases := []struct {
		name     string
		hostile  []string
		inConfig string // ConfigDir inside a session
		inState  string
		inProf   string
		outConf  string // ConfigDir outside a session; "" = expect a config error
		outState string
		outProf  string
	}{
		{
			name:     "BRIGADE_CONFIG_DIR absolute",
			hostile:  []string{"BRIGADE_CONFIG_DIR=" + evilAbs},
			inConfig: d.brigadeConfig(), inState: d.brigadeState(), inProf: "default",
			outConf: evilAbs, outState: d.brigadeState(), outProf: "default",
		},
		{
			name:     "BRIGADE_PROFILE",
			hostile:  []string{"BRIGADE_PROFILE=" + evilMarker},
			inConfig: d.brigadeConfig(), inState: d.brigadeState(), inProf: "default",
			outConf: d.brigadeConfig(), outState: d.brigadeState(), outProf: evilMarker,
		},
		{
			name:     "BRIGADE_STATE_DIR relative",
			hostile:  []string{"BRIGADE_STATE_DIR=rel/" + evilMarker},
			inConfig: d.brigadeConfig(), inState: d.brigadeState(), inProf: "default",
			outConf: d.brigadeConfig(), outState: "", outProf: "default",
		},
		{
			name:     "BRIGADE_STATE_DIR absolute",
			hostile:  []string{"BRIGADE_STATE_DIR=" + evilAbs},
			inConfig: d.brigadeConfig(), inState: d.brigadeState(), inProf: "default",
			outConf: d.brigadeConfig(), outState: evilAbs, outProf: "default",
		},
		{
			name:     "all at once",
			hostile:  []string{"BRIGADE_CONFIG_DIR=" + evilAbs, "BRIGADE_PROFILE=" + evilMarker, "BRIGADE_STATE_DIR=" + evilAbs},
			inConfig: d.brigadeConfig(), inState: d.brigadeState(), inProf: "default",
			outConf: evilAbs, outState: evilAbs, outProf: evilMarker,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name+" in session", func(t *testing.T) {
			t.Parallel()
			env := d.environ(append([]string{"CLAUDE_PID=4242"}, tc.hostile...)...)
			if got, err := config.BrigadeConfigDir(env); err != nil || got != tc.inConfig {
				t.Fatalf("ConfigDir = %q, %v; want %q", got, err, tc.inConfig)
			}
			if got, err := config.BrigadeStateDir(env); err != nil || got != tc.inState {
				t.Fatalf("StateDir = %q, %v; want %q", got, err, tc.inState)
			}
			if got := config.ProfileName(env); got != tc.inProf {
				t.Fatalf("ProfileName = %q, want %q", got, tc.inProf)
			}
		})
		t.Run(tc.name+" outside a session", func(t *testing.T) {
			t.Parallel()
			env := d.environ(tc.hostile...)
			if got, err := config.BrigadeConfigDir(env); err != nil || got != tc.outConf {
				t.Fatalf("ConfigDir = %q, %v; want %q", got, err, tc.outConf)
			}
			got, err := config.BrigadeStateDir(env)
			if tc.outState == "" {
				assertConfig(t, err, "relative_path")
			} else if err != nil || got != tc.outState {
				t.Fatalf("StateDir = %q, %v; want %q", got, err, tc.outState)
			}
			if got := config.ProfileName(env); got != tc.outProf {
				t.Fatalf("ProfileName = %q, want %q", got, tc.outProf)
			}
		})
	}
}

func TestClaudePID(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		value  string // "" = unset
		want   int
		reason string
	}{
		{"positive", "4242", 4242, ""},
		{"unset", "", 0, config.ReasonNotInSession},
		{"zero", "0", 0, config.ReasonInvalidClaudePID},
		{"negative", "-4242", 0, config.ReasonInvalidClaudePID},
		{"junk", "abc" + evilMarker, 0, config.ReasonInvalidClaudePID},
		{"float", "42.0", 0, config.ReasonInvalidClaudePID},
		{"spaces", " 42", 0, config.ReasonInvalidClaudePID},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			env := []string{"HOME=/h"}
			if tc.value != "" {
				env = append(env, "CLAUDE_PID="+tc.value)
			}
			got, err := config.ClaudePID(env)
			if tc.reason == "" {
				if err != nil || got != tc.want {
					t.Fatalf("ClaudePID = %d, %v; want %d", got, err, tc.want)
				}
				return
			}
			assertConfig(t, err, tc.reason)
		})
	}
}

func TestClaudeConfigDir(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		environ []string
		want    string
		reason  string
	}{
		{"CLAUDE_CONFIG_DIR absolute", []string{"HOME=/h", "CLAUDE_CONFIG_DIR=/Users/x/.claude-other"}, "/Users/x/.claude-other", ""},
		{"CLAUDE_CONFIG_DIR is cleaned", []string{"CLAUDE_CONFIG_DIR=/Users/x//.claude-other/"}, "/Users/x/.claude-other", ""},
		{"CLAUDE_CONFIG_DIR relative is refused", []string{"HOME=/h", "CLAUDE_CONFIG_DIR=rel/" + evilMarker}, "", config.ReasonRelativePath},
		{"default under HOME", []string{"HOME=/h"}, "/h/.claude", ""},
		{"no HOME", []string{}, "", config.ReasonUnresolvable},
		{"relative HOME", []string{"HOME=h"}, "", config.ReasonUnresolvable},
		{"a session does not strip it (not BRIGADE_)", []string{"CLAUDE_PID=1", "HOME=/h", "CLAUDE_CONFIG_DIR=/c"}, "/c", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := config.ClaudeConfigDir(tc.environ)
			if tc.reason != "" {
				assertConfig(t, err, tc.reason)
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ClaudeConfigDir = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
}
