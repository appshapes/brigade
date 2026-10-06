package teamfile_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamfile"
)

// withPolling is validDoc with a `polling` member of the given raw JSON.
func withPolling(member string) string {
	return strings.TrimSuffix(validDoc, "}") + `,"polling":` + member + `}`
}

// TestPollingRules runs every rule of the `polling` member (card 53) both
// ways, each reason token at least once. A rejected member is NEVER a
// refusal: the file parses, Polling is nil (the session runs on the
// defaults), the token says why, and the team the file names is
// untouched. The bounds are inclusive.
func TestPollingRules(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, member string
		reason       string // "" means usable
		want         teamfile.PollingConfig
		ignored      []string
	}{
		{"both values", `{"heartbeat_seconds":100,"roster_seconds":300}`, "", teamfile.PollingConfig{HeartbeatSeconds: 100, RosterSeconds: 300}, nil},
		{"an empty member is the defaults", `{}`, "", teamfile.PollingConfig{}, nil},
		{"the heartbeat alone", `{"heartbeat_seconds":200}`, "", teamfile.PollingConfig{HeartbeatSeconds: 200}, nil},
		{"the roster alone", `{"roster_seconds":15}`, "", teamfile.PollingConfig{RosterSeconds: 15}, nil},
		{"the lowest bounds", `{"heartbeat_seconds":10,"roster_seconds":15}`, "", teamfile.PollingConfig{HeartbeatSeconds: 10, RosterSeconds: 15}, nil},
		{"the highest bounds", `{"heartbeat_seconds":200,"roster_seconds":3600}`, "", teamfile.PollingConfig{HeartbeatSeconds: 200, RosterSeconds: 3600}, nil},
		{"an unknown inner member is ignored and named", `{"heartbeat_seconds":60,"drain_seconds":30}`, "", teamfile.PollingConfig{HeartbeatSeconds: 60}, []string{"polling.drain_seconds"}},
		{"not an object", `100`, teamfile.PollingNotObject, teamfile.PollingConfig{}, nil},
		{"an array", `[100,300]`, teamfile.PollingNotObject, teamfile.PollingConfig{}, nil},
		{"the heartbeat as a string", `{"heartbeat_seconds":"100"}`, teamfile.PollingHeartbeatInvalid, teamfile.PollingConfig{}, nil},
		{"a fractional heartbeat", `{"heartbeat_seconds":100.5}`, teamfile.PollingHeartbeatInvalid, teamfile.PollingConfig{}, nil},
		{"a null heartbeat", `{"heartbeat_seconds":null}`, teamfile.PollingHeartbeatInvalid, teamfile.PollingConfig{}, nil},
		{"a heartbeat under the bound", `{"heartbeat_seconds":9}`, teamfile.PollingHeartbeatOutOfRange, teamfile.PollingConfig{}, nil},
		{"a heartbeat over the bound", `{"heartbeat_seconds":201}`, teamfile.PollingHeartbeatOutOfRange, teamfile.PollingConfig{}, nil},
		{"a zero heartbeat", `{"heartbeat_seconds":0}`, teamfile.PollingHeartbeatOutOfRange, teamfile.PollingConfig{}, nil},
		{"the roster as a boolean", `{"roster_seconds":true}`, teamfile.PollingRosterInvalid, teamfile.PollingConfig{}, nil},
		{"a roster under the bound", `{"roster_seconds":14}`, teamfile.PollingRosterOutOfRange, teamfile.PollingConfig{}, nil},
		{"a roster over the bound", `{"roster_seconds":3601}`, teamfile.PollingRosterOutOfRange, teamfile.PollingConfig{}, nil},
		{"the heartbeat's rule is reported first", `{"heartbeat_seconds":1,"roster_seconds":1}`, teamfile.PollingHeartbeatOutOfRange, teamfile.PollingConfig{}, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, err := teamfile.Parse(write(t, withPolling(c.member)))
			if err != nil {
				t.Fatalf("a polling member must never refuse the file: %v", err)
			}
			if f.TeamRef != "t_4f9c" {
				t.Fatalf("the team the file names changed: %+v", f)
			}
			if f.PollingUnusable != c.reason {
				t.Fatalf("reason = %q, want %q", f.PollingUnusable, c.reason)
			}
			if c.reason != "" && f.Polling != nil {
				t.Fatalf("an unusable member yielded a config: %+v", f.Polling)
			}
			if c.reason == "" && (f.Polling == nil || *f.Polling != c.want) {
				t.Fatalf("polling = %+v, want %+v", f.Polling, c.want)
			}
			if len(f.Ignored)+len(c.ignored) > 0 && !slices.Equal(f.Ignored, c.ignored) {
				t.Fatalf("Ignored = %q, want %q", f.Ignored, c.ignored)
			}
		})
	}
}

// TestNoPollingMemberIsTheDefaults: a file without the member parses with
// no config and no reason, which every reader maps to the defaults.
func TestNoPollingMemberIsTheDefaults(t *testing.T) {
	t.Parallel()
	f, err := teamfile.Parse(write(t, validDoc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Polling != nil || f.PollingUnusable != "" {
		t.Fatalf("polling = %+v (%q), want none", f.Polling, f.PollingUnusable)
	}
}

func TestPollingReasonsListIsClosed(t *testing.T) {
	t.Parallel()
	want := []string{
		teamfile.PollingNotObject, teamfile.PollingHeartbeatInvalid, teamfile.PollingHeartbeatOutOfRange,
		teamfile.PollingRosterInvalid, teamfile.PollingRosterOutOfRange,
	}
	if !slices.Equal(teamfile.PollingReasons(), want) {
		t.Fatalf("PollingReasons() = %q", teamfile.PollingReasons())
	}
}

func TestCarriedPolling(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		doc           string // "" means no file at all
		worldWritable bool
		want          *teamfile.PollingConfig
		cause         string
	}{
		"no file":                {"", false, nil, ""},
		"a file without polling": {validDoc, false, nil, ""},
		"a usable member": {
			withPolling(`{"heartbeat_seconds":150,"roster_seconds":900,"future":true}`), false,
			&teamfile.PollingConfig{HeartbeatSeconds: 150, RosterSeconds: 900}, "",
		},
		"a refused file with no polling": {"not json", false, nil, ""},
		"an unusable member":             {withPolling(`{"heartbeat_seconds":5}`), false, nil, teamfile.PollingHeartbeatOutOfRange},
		"a refused file that declares polling": {
			strings.Replace(withPolling(`{"heartbeat_seconds":100}`), `"version":1`, `"version":2`, 1), false, nil, teamfile.ReasonUnsupportedVersion,
		},
		"a file read refuses unread": {withPolling(`{"heartbeat_seconds":100}`), true, nil, teamfile.ReasonWorldWritable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), teamfile.FileName)
			if c.doc != "" {
				p = write(t, c.doc)
			}
			if c.worldWritable {
				//nolint:gosec // G302: the world-writable mode is the precondition under test
				if err := os.Chmod(p, 0o646); err != nil {
					t.Fatal(err)
				}
			}
			got, cause := teamfile.CarriedPolling(p)
			if !reflect.DeepEqual(got, c.want) || cause != c.cause {
				t.Fatalf("= %+v, %q; want %+v, %q", got, cause, c.want, c.cause)
			}
		})
	}
}
