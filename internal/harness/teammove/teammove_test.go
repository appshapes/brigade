package teammove_test

import (
	"context"
	json "encoding/json/v2"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
)

// The team of these tests on the backend it leaves and on the one it goes
// to. The reference is the same on both, which is what makes it a move.
const (
	teamRef  = "6f0f2b41-0f0f-4f0f-8f0f-0f0f0f0f0f0f"
	teamName = "ops"
	oldURL   = "https://old.supabase.example"
	oldKeyP  = "sb_publishable_x"
	newURL   = "https://brigade.example.test"
	newPin   = "sha256:2222222222222222222222222222222222222222222222222222222222222222"
	label    = "alice@example.com"
)

// ticket is the move ticket the old backend holds: a join secret of the
// team, put together here so that no file of the repository holds one.
var ticket = strings.Join([]string{"brg1", teamRef, strings.Repeat("cd", 16)}, ".")

// call is one adapter call a fake backend saw.
type call struct {
	adapter, profile, verb string
	flags                  []string
	stdin                  any
}

// backends stands in for the two adapters of a follow. moved is what the
// old backend's `team moved` answers; when it carries a ticket_file the
// fake writes the ticket there, as an adapter does. join is what the new
// backend's `team join` answers.
type backends struct {
	t        *testing.T
	calls    []call
	moved    *protocol.TeamMoveResult
	movedErr error
	ticket   string
	join     *protocol.TeamJoinResult
	joinErr  error
	// unresolved names an adapter this machine cannot resolve.
	unresolved string
	// noRecord is true for an old backend whose adapter does not carry
	// capability team.move.
	noRecord bool
	// newRecords is true for a new backend whose adapter carries capability
	// team.move, as the `rest` adapter does from 0.57.0 (work item 341): it
	// is then asked `team moved` with the membership this machine holds
	// there. newMoved is what it answers, a team that did not move when nil,
	// and newMovedErrs are the failures of its answers in turn, nil for one
	// that is answered.
	newRecords   bool
	newMoved     *protocol.TeamMoveResult
	newMovedErrs []error
	// newMovedHangs has the new backend's `team moved` wait for as long as
	// its caller does: a server that accepts the connection and says nothing.
	newMovedHangs bool
}

type fakeCaller struct {
	b                *backends
	adapter, profile string
}

// Describe answers the capabilities of the adapter: the old backend's
// carries the record's two commands unless the test says otherwise, and
// the server's adapter never does.
func (c fakeCaller) Describe(context.Context) (*protocol.DescribeResult, error) {
	c.b.calls = append(c.b.calls, call{adapter: c.adapter, profile: c.profile, verb: "describe"})
	capabilities := []string{"team.create", "team.join"}
	if (c.adapter != "rest" && !c.b.noRecord) || (c.adapter == "rest" && c.b.newRecords) {
		capabilities = append(capabilities, teammove.Capability)
	}
	return &protocol.DescribeResult{Capabilities: capabilities}, nil
}

func (c fakeCaller) Call(ctx context.Context, group, verb string, flags []string, stdin any) (*protocol.Envelope, error) {
	b := c.b
	if b.newMovedHangs && c.adapter == "rest" && group+" "+verb == "team moved" {
		b.calls = append(b.calls, call{adapter: c.adapter, profile: c.profile, verb: group + " " + verb})
		<-ctx.Done()
		return nil, ctx.Err()
	}
	b.calls = append(b.calls, call{adapter: c.adapter, profile: c.profile, verb: group + " " + verb, flags: slices.Clone(flags), stdin: stdin})
	var result any
	switch group + " " + verb {
	case "profile init":
		result = map[string]string{"state": "configured"}
	case "team moved":
		if c.adapter == "rest" {
			// The new backend, asked what it records itself.
			if len(b.newMovedErrs) > 0 {
				err := b.newMovedErrs[0]
				b.newMovedErrs = b.newMovedErrs[1:]
				if err != nil {
					return nil, err
				}
			}
			result = &protocol.TeamMoveResult{TeamRef: teamRef}
			if b.newMoved != nil {
				result = b.newMoved
			}
			break
		}
		if b.movedErr != nil {
			return nil, b.movedErr
		}
		answer := *b.moved
		if answer.Moved && len(flags) == 2 && flags[0] == "--ticket-file" && b.ticket != "" {
			if err := adapterkit.WriteTicketFile(flags[1], b.ticket); err != nil {
				b.t.Fatal(err)
			}
			answer.TicketFile = flags[1]
		}
		result = &answer
	case "team join":
		if b.joinErr != nil {
			return nil, b.joinErr
		}
		result = b.join
	default:
		b.t.Fatalf("a follow ran %s %s, which is not one of its commands", group, verb)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		b.t.Fatal(err)
	}
	return &protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: raw}, nil
}

func (b *backends) client(adapter, profile string) (teammove.Caller, error) {
	if adapter == b.unresolved {
		return nil, errors.New("no such adapter on this machine")
	}
	return fakeCaller{b: b, adapter: adapter, profile: profile}, nil
}

func (b *backends) verbs() []string {
	out := make([]string, 0, len(b.calls))
	for _, c := range b.calls {
		out = append(out, c.adapter+":"+c.verb)
	}
	return out
}

// called returns the calls of one command, in order.
func (b *backends) called(verb string) []call {
	var out []call
	for _, c := range b.calls {
		if c.verb == verb {
			out = append(out, c)
		}
	}
	return out
}

// rig is one member's machine: a store with the team joined on the old
// backend in one checkout, and the team file as the administrator pushed
// it, naming the new backend.
type rig struct {
	t         *testing.T
	configDir string
	checkout  string
	oldKey    string
	newKey    string
	pin       teamstore.Pin
	file      *teamfile.File
	b         *backends
	// now is the clock of the rig's follows.
	now time.Time
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t: t, configDir: filepath.Join(t.TempDir(), "config"), checkout: filepath.Join(t.TempDir(), "checkout"),
		oldKey: teamstore.Key("supabase", oldURL, teamRef), newKey: teamstore.Key("rest", newURL, teamRef),
		pin:  teamstore.Pin{Adapter: "supabase", URL: oldURL, PublishableKey: oldKeyP, TeamRef: teamRef, ConsentedAt: time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)},
		file: &teamfile.File{Version: 1, Adapter: "rest", URL: newURL, Certificate: newPin, TeamRef: teamRef, TeamName: teamName},
		now:  time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC),
	}
	if err := adapterkit.MkdirPrivate(r.configDir); err != nil {
		t.Fatal(err)
	}
	if err := write.EnsureBinding(r.configDir, r.oldKey, "supabase", oldURL, oldKeyP, "", teamRef, teamName, "p-old"); err != nil {
		t.Fatal(err)
	}
	old, err := teamstore.LoadBinding(r.configDir, r.oldKey)
	if err != nil {
		t.Fatal(err)
	}
	old.HumanLabel = label
	if err := adapterkit.SaveProfile(r.configDir, r.oldKey, old); err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(r.configDir, r.checkout, r.pin); err != nil {
		t.Fatal(err)
	}
	r.b = &backends{
		t:      t,
		moved:  &protocol.TeamMoveResult{TeamRef: teamRef, Moved: true, Adapter: "rest", URL: newURL, Certificate: newPin, RecordedAt: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)},
		ticket: ticket,
		join:   &protocol.TeamJoinResult{TeamRef: teamRef, TeamName: teamName, PrincipalRef: "p-new"},
	}
	return r
}

func (r *rig) follow() (teammove.Outcome, error) {
	r.t.Helper()
	return teammove.Follow(r.t.Context(), teammove.Inputs{
		ConfigDir: r.configDir, Checkout: r.checkout, File: r.file, Pin: &r.pin, Client: r.b.client,
		Now: func() time.Time { return r.now },
	})
}

// currentPin is the checkout's pin as the store holds it now.
func (r *rig) currentPin() teamstore.Pin {
	r.t.Helper()
	pin, ok, err := teamstore.LookupPin(r.configDir, r.checkout)
	if err != nil || !ok {
		r.t.Fatalf("the checkout's pin: %v, %v", ok, err)
	}
	return *pin
}

// leftBehind lists what a follow left in the store's own directory besides
// the files a store always has: a ticket file would be here.
func (r *rig) leftBehind() []string {
	r.t.Helper()
	entries, err := os.ReadDir(r.configDir)
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		switch e.Name() {
		case "teams", "projects.json", "projects.json.lock", "team-move.lock":
		default:
			out = append(out, e.Name())
		}
	}
	return out
}

// ticketOnDisk reports whether any file of the store holds the ticket.
func (r *rig) ticketOnDisk() bool {
	r.t.Helper()
	found := false
	err := filepath.WalkDir(r.configDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, rerr := os.ReadFile(path) //nolint:gosec // the test's own store
		if rerr == nil && strings.Contains(string(data), ticket) {
			found = true
		}
		return nil
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return found
}

// TestFollowJoinsTheBackendTheRecordAndTheFileBothName is the whole of a
// follow: the old backend is asked once, under the profile of the team the
// checkout joined, with a ticket file inside the store; the new backend is
// joined once with that ticket, the label the member had and the backend
// the file names; the binding and the pin are written; and the ticket is
// in no file afterwards, a file an earlier follow left behind included.
func TestFollowJoinsTheBackendTheRecordAndTheFileBothName(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	// A ticket file an earlier follow left when it was stopped from outside.
	leftover := filepath.Join(r.configDir, ".move-ticket-0123456789abcdef")
	if err := os.WriteFile(leftover, []byte(ticket+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := r.follow()
	if err != nil || !out.Followed || !out.Joined || out.Reason != "" {
		t.Fatalf("follow: %+v, %v", out, err)
	}
	// The new backend's adapter is asked what it offers once this machine is
	// a member there, from its local files; one that keeps no record of a
	// move is asked nothing else (work item 341).
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved", "rest:team join", "rest:describe"}) {
		t.Fatalf("adapter calls: %v", got)
	}
	moved, join := r.b.calls[1], r.b.calls[2]
	if moved.profile != r.oldKey || len(moved.flags) != 2 || moved.flags[0] != "--ticket-file" || filepath.Dir(moved.flags[1]) != r.configDir {
		t.Fatalf("team moved was not asked of the old profile with a ticket file in the store: profile %q", moved.profile)
	}
	req, ok := join.stdin.(*protocol.TeamJoinRequest)
	if !ok || join.profile != r.newKey || req.JoinSecret != ticket || req.HumanLabel != label {
		t.Fatalf("team join was not sent to the new profile with the ticket and the member's label")
	}
	var backend map[string]string
	if err := json.Unmarshal(req.Backend, &backend); err != nil || backend["url"] != newURL || backend["certificate"] != newPin || len(backend) != 2 {
		t.Fatalf("the join's backend: %v, %v", backend, err)
	}
	pin := r.currentPin()
	if !pin.Matches(r.file) || pin.MovedFrom != oldURL || !pin.ConsentedAt.Equal(time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC)) {
		t.Fatalf("the pin after the follow: %+v", pin)
	}
	binding, err := teamstore.LoadBinding(r.configDir, r.newKey)
	if err != nil || teamstore.VerifyBinding(binding, r.file) != nil || binding.PrincipalRef != "p-new" || binding.TeamName != teamName {
		t.Fatalf("the binding after the follow: %+v, %v", binding, err)
	}
	if old, err := teamstore.LoadBinding(r.configDir, r.oldKey); err != nil || old.URL != oldURL {
		t.Fatalf("the old binding was touched: %+v, %v", old, err)
	}
	if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
		t.Fatalf("the follow left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
	}
}

// TestFollowIsRefusedAndChangesNothing: every way a follow does not
// happen leaves the pin as it was, joins nothing and leaves no ticket in
// the store, and names its reason.
func TestFollowIsRefusedAndChangesNothing(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		spoil  func(r *rig)
		reason string
		// asked is whether the old backend is asked whether the team moved.
		asked bool
	}{
		{"the file names another team", func(r *rig) { r.file.TeamRef = "7f0f2b41-0f0f-4f0f-8f0f-0f0f0f0f0f0f" }, teammove.ReasonNotAMove, false},
		{"the file names the same backend with another key", func(r *rig) {
			r.file = &teamfile.File{Version: 1, Adapter: "supabase", URL: oldURL, PublishableKey: "sb_publishable_y", TeamRef: teamRef, TeamName: teamName}
		}, teammove.ReasonNotAMove, false},
		{"the machine holds no membership on the old backend", func(r *rig) {
			if err := os.RemoveAll(filepath.Join(r.configDir, "teams", r.oldKey)); err != nil {
				r.t.Fatal(err)
			}
		}, teammove.ReasonNoOldBinding, false},
		{"the old binding is not the one the pin describes", func(r *rig) { r.pin.PublishableKey = "sb_publishable_other" }, teammove.ReasonOldBindingDrifted, false},
		{"the old adapter cannot be resolved", func(r *rig) { r.b.unresolved = "supabase" }, teammove.ReasonAdapterUnresolved, false},
		{"the new adapter cannot be resolved", func(r *rig) { r.b.unresolved = "rest" }, teammove.ReasonAdapterUnresolved, false},
		{"the old backend's adapter keeps no record of moves", func(r *rig) { r.b.noRecord = true }, teammove.ReasonNoCapability, false},
		{"the old backend refuses", func(r *rig) {
			r.b.movedErr = &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
		}, teammove.ReasonOldBackendRefused, true},
		{"the old backend does not answer", func(r *rig) {
			r.b.movedErr = &protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend could not be reached"}
		}, teammove.ReasonOldBackendFailed, true},
		{"the old backend answers a shape that is not the protocol's", func(r *rig) { r.b.moved = &protocol.TeamMoveResult{Moved: true} }, teammove.ReasonOldBackendRefused, true},
		{"no move is recorded", func(r *rig) { r.b.moved = &protocol.TeamMoveResult{TeamRef: teamRef} }, teammove.ReasonNoRecord, true},
		{"the record names another url", func(r *rig) { r.b.moved.URL = "https://elsewhere.example.test" }, teammove.ReasonRecordElsewhere, true},
		{"the record names another adapter", func(r *rig) { r.b.moved.Adapter = "other" }, teammove.ReasonRecordElsewhere, true},
		{"the record names another certificate", func(r *rig) { r.b.moved.Certificate = strings.Replace(newPin, "2", "3", 1) }, teammove.ReasonRecordElsewhere, true},
		{"the record names no certificate and the file one", func(r *rig) { r.b.moved.Certificate = "" }, teammove.ReasonRecordElsewhere, true},
		{"the file names no certificate and the record one", func(r *rig) { r.file.Certificate = "" }, teammove.ReasonRecordElsewhere, true},
		{"the record is another team's", func(r *rig) { r.b.moved.TeamRef = "7f0f2b41-0f0f-4f0f-8f0f-0f0f0f0f0f0f" }, teammove.ReasonRecordElsewhere, true},
		{"the old backend wrote no ticket", func(r *rig) { r.b.ticket = "" }, teammove.ReasonNoTicket, true},
		{"the ticket is another team's", func(r *rig) { r.b.ticket = strings.Replace(ticket, "6f0f", "7f0f", 1) }, teammove.ReasonNoTicket, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			before := r.currentPin()
			tc.spoil(r)
			out, _ := r.follow()
			if out.Followed || out.Joined || out.Reason != tc.reason {
				t.Fatalf("follow: %+v, want reason %s", out, tc.reason)
			}
			for _, c := range r.b.calls {
				if c.verb == "team join" {
					t.Fatalf("the new backend was sent a join")
				}
			}
			if asked := len(r.b.called("team moved")) > 0; asked != tc.asked {
				t.Fatalf("the old backend was asked whether the team moved: %v, want %v", asked, tc.asked)
			}
			if after := r.currentPin(); after != before {
				t.Fatalf("the pin changed: %+v", after)
			}
			if _, err := teamstore.LoadBinding(r.configDir, r.newKey); err == nil {
				t.Fatalf("a binding for the new backend was written")
			}
			if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
				t.Fatalf("the refusal left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
			}
		})
	}
}

// TestFollowWhoseJoinIsRefusedWritesNoPin: the new backend's refusal of the
// ticket (it ended, or the move was closed), an answer that is no join
// result and an answer about another team each leave the pin and write no
// binding, and the ticket is gone from the store all the same.
func TestFollowWhoseJoinIsRefusedWritesNoPin(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		spoil  func(r *rig)
		reason string
	}{
		"the ticket is refused": {func(r *rig) {
			r.b.joinErr = &protocol.Error{Code: protocol.CodeUnauthorized, Message: "join secret rejected"}
		}, teammove.ReasonJoinRefused},
		"the answer is no join result": {func(r *rig) { r.b.join = &protocol.TeamJoinResult{TeamRef: teamRef} }, teammove.ReasonJoinRefused},
		"the answer is about another team": {func(r *rig) {
			r.b.join = &protocol.TeamJoinResult{TeamRef: "7f0f2b41-0f0f-4f0f-8f0f-0f0f0f0f0f0f", TeamName: teamName, PrincipalRef: "p-new"}
		}, teammove.ReasonJoinedOther},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			before := r.currentPin()
			tc.spoil(r)
			out, err := r.follow()
			if out.Followed || out.Reason != tc.reason || err == nil {
				t.Fatalf("follow: %+v, %v, want reason %s and an error", out, err, tc.reason)
			}
			if after := r.currentPin(); after != before {
				t.Fatalf("the pin changed: %+v", after)
			}
			if _, err := teamstore.LoadBinding(r.configDir, r.newKey); err == nil {
				t.Fatalf("a binding for the new backend was written")
			}
			if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
				t.Fatalf("the refusal left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
			}
		})
	}
}

// TestFollowInAnotherCheckoutJoinsNothing is work item 290's case for a
// team that moved: the machine followed in one checkout, so it holds the
// new backend's membership, and a second checkout that is still pinned to
// the old backend follows by the same rule, the old backend's record, and
// sends no join. Without the record it does not follow, membership or not.
func TestFollowInAnotherCheckoutJoinsNothing(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if out, err := r.follow(); err != nil || !out.Joined {
		t.Fatalf("the first checkout: %+v, %v", out, err)
	}
	second := filepath.Join(t.TempDir(), "second")
	if err := write.Pin(r.configDir, second, r.pin); err != nil {
		t.Fatal(err)
	}
	r.checkout, r.b.calls = second, nil
	r.b.moved = &protocol.TeamMoveResult{TeamRef: teamRef}
	if out, _ := r.follow(); out.Followed || out.Reason != teammove.ReasonNoRecord {
		t.Fatalf("a second checkout with no record on the old backend: %+v", out)
	}
	if pin := r.currentPin(); pin.Adapter != "supabase" {
		t.Fatalf("the second checkout's pin moved without a record: %+v", pin)
	}
	r.b.calls = nil
	r.b.moved = &protocol.TeamMoveResult{TeamRef: teamRef, Moved: true, Adapter: "rest", URL: newURL, Certificate: newPin, RecordedAt: time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)}
	out, err := r.follow()
	if err != nil || !out.Followed || out.Joined {
		t.Fatalf("the second checkout: %+v, %v", out, err)
	}
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved", "rest:describe"}) {
		t.Fatalf("adapter calls of the second checkout: %v", got)
	}
	if pin := r.currentPin(); !pin.Matches(r.file) || pin.MovedFrom != oldURL {
		t.Fatalf("the second checkout's pin: %+v", pin)
	}
	if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
		t.Fatalf("the second follow left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
	}
}

// describing is a Caller whose describe first runs before: a follow's
// describe of the old backend's adapter is the last thing it does before
// it waits for the lock, so what before does happens while the follow that
// called it stands between reading the store and taking the lock.
type describing struct {
	teammove.Caller
	before func()
}

func (d describing) Describe(ctx context.Context) (*protocol.DescribeResult, error) {
	d.before()
	return d.Caller.Describe(ctx)
}

// TestTwoFollowsOfOneTeamAtOnceJoinOnce: two checkouts of one team, both
// still pinned to the old backend, follow in the same instant: two sessions
// that start at once, or two children that two prompts started (work item
// 327: the bound on children is each checkout's). The lock lets one through
// at a time. The one that comes second read the store before it waited,
// when this machine held no membership at the new backend; by the time it
// has the lock the first has joined. It must see that, follow by the old
// backend's record as a second checkout does, and send no join of its own:
// one machine presents the move's ticket once.
func TestTwoFollowsOfOneTeamAtOnceJoinOnce(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	second := filepath.Join(t.TempDir(), "second")
	if err := write.Pin(r.configDir, second, r.pin); err != nil {
		t.Fatal(err)
	}
	follow := func(checkout string, client func(adapter, profile string) (teammove.Caller, error)) (teammove.Outcome, error) {
		return teammove.Follow(t.Context(), teammove.Inputs{
			ConfigDir: r.configDir, Checkout: checkout, File: r.file, Pin: &r.pin, Client: client,
			Now: func() time.Time { return time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC) },
		})
	}
	// The second checkout's follow has read the store and is about to wait
	// for the lock when the first checkout's runs to its end.
	raced := false
	racing := func(adapter, profile string) (teammove.Caller, error) {
		c, err := r.b.client(adapter, profile)
		if err != nil {
			return nil, err
		}
		return describing{Caller: c, before: func() {
			if raced {
				return
			}
			raced = true
			if out, err := follow(r.checkout, r.b.client); err != nil || !out.Followed || !out.Joined {
				t.Errorf("the follow that came first: %+v, %v", out, err)
			}
		}}, nil
	}
	out, err := follow(second, racing)
	if err != nil || !out.Followed || out.Joined || !raced {
		t.Fatalf("the follow that came second: %+v, %v (raced %v)", out, err, raced)
	}
	if joins := r.b.called("team join"); len(joins) != 1 {
		t.Fatalf("%d joins were sent to the new backend, want 1", len(joins))
	}
	for _, checkout := range []string{r.checkout, second} {
		pin, ok, err := teamstore.LookupPin(r.configDir, checkout)
		if err != nil || !ok || !pin.Matches(r.file) || pin.MovedFrom != oldURL {
			t.Fatalf("the pin of %s: %+v, %v", filepath.Base(checkout), pin, err)
		}
	}
	if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
		t.Fatalf("the two follows left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
	}
}

// TestFollowLeavesWordBeforeThePin: a follow that has nobody to tell (the
// child a prompt hook starts, work item 327) leaves word for the next hook
// of the checkout through BeforePin. It is called once, with whether this
// call joined, and before the pin is written: the pin is what says the
// checkout followed, so a hook that finds the pin finds the word too. A
// follow that is refused never calls it. And two follows of one checkout
// can be under way at once, a session's start and a child: the one that
// comes second holds the pin it read before the other wrote the new one,
// finds the checkout followed, asks the old backend nothing and leaves no
// word of its own.
func TestFollowLeavesWordBeforeThePin(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	follow := func(word *[]bool) (teammove.Outcome, error) {
		t.Helper()
		return teammove.Follow(t.Context(), teammove.Inputs{
			ConfigDir: r.configDir, Checkout: r.checkout, File: r.file, Pin: &r.pin, Client: r.b.client,
			Now: func() time.Time { return time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC) },
			BeforePin: func(joined bool) {
				if pin := r.currentPin(); pin.Adapter != "supabase" {
					t.Errorf("the word was left after the pin was written: %+v", pin)
				}
				*word = append(*word, joined)
			},
		})
	}
	var word []bool
	// No record: nothing is followed, and no word is left.
	recorded := r.b.moved
	r.b.moved = &protocol.TeamMoveResult{TeamRef: teamRef}
	if out, _ := follow(&word); out.Followed || len(word) != 0 {
		t.Fatalf("a follow with no record: %+v, word %v", out, word)
	}
	r.b.moved = recorded
	if out, err := follow(&word); err != nil || !out.Followed || !slices.Equal(word, []bool{true}) {
		t.Fatalf("the first follow: %+v, %v, word %v", out, err, word)
	}
	written := r.currentPin()
	r.b.calls = nil
	// r.pin is still the pin the second follow read: the old backend's.
	out, err := follow(&word)
	if err != nil || !out.Followed || out.Joined || len(word) != 1 {
		t.Fatalf("the second follow: %+v, %v, word %v", out, err, word)
	}
	if got := r.b.verbs(); slices.Contains(got, "supabase:team moved") || slices.Contains(got, "rest:team join") {
		t.Fatalf("the second follow asked a backend: %v", got)
	}
	if pin := r.currentPin(); pin != written {
		t.Fatalf("the second follow rewrote the pin: %+v", pin)
	}
}

// TestFollowLeavesAMemberThatLeftTheNewBackendOut: `team leave` is the way
// out the follow's own line names. A machine that was a member at the new
// backend and left has a profile there that names its principal and no
// team; a session in another checkout that is still pinned to the old
// backend does not join it again, and asks no backend anything.
func TestFollowLeavesAMemberThatLeftTheNewBackendOut(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	if out, err := r.follow(); err != nil || !out.Joined {
		t.Fatalf("the follow: %+v, %v", out, err)
	}
	left, err := teamstore.LoadBinding(r.configDir, r.newKey)
	if err != nil {
		t.Fatal(err)
	}
	left.TeamRef, left.TeamName = "", ""
	if err := adapterkit.SaveProfile(r.configDir, r.newKey, left); err != nil {
		t.Fatal(err)
	}
	second := filepath.Join(t.TempDir(), "second")
	if err := write.Pin(r.configDir, second, r.pin); err != nil {
		t.Fatal(err)
	}
	r.checkout, r.b.calls = second, nil
	if out, err := r.follow(); err != nil || out.Followed || out.Reason != teammove.ReasonLeft {
		t.Fatalf("a second checkout after the member left: %+v, %v", out, err)
	}
	if len(r.b.calls) != 0 {
		t.Fatalf("a backend was asked for a member that left: %v", r.b.verbs())
	}
	if pin := r.currentPin(); pin.Adapter != "supabase" {
		t.Fatalf("the second checkout's pin moved: %+v", pin)
	}
}

// TestFollowLeavesOutAMemberThatLeftWhileItWaited: the membership is read
// before the lock, to send a machine that left away without a question, and
// again under it (work item 327). Between the two the member can have left
// the new backend: another checkout's follow joined, and they ran `team
// leave`. The follow that waited must not join the machine again on the
// strength of what it read first: under the lock it finds the profile of a
// member that left, asks the old backend nothing and joins nothing.
func TestFollowLeavesOutAMemberThatLeftWhileItWaited(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	second := filepath.Join(t.TempDir(), "second")
	if err := write.Pin(r.configDir, second, r.pin); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC) }
	// While the second checkout's follow stands between reading the store
	// and taking the lock, the first checkout's follows, and the member
	// leaves the new backend.
	left := false
	leaving := func(adapter, profile string) (teammove.Caller, error) {
		c, err := r.b.client(adapter, profile)
		if err != nil {
			return nil, err
		}
		return describing{Caller: c, before: func() {
			if left {
				return
			}
			left = true
			out, err := teammove.Follow(t.Context(), teammove.Inputs{
				ConfigDir: r.configDir, Checkout: r.checkout, File: r.file, Pin: &r.pin, Client: r.b.client, Now: now,
			})
			if err != nil || !out.Joined {
				t.Errorf("the follow that came first: %+v, %v", out, err)
			}
			binding, err := teamstore.LoadBinding(r.configDir, r.newKey)
			if err != nil {
				t.Error(err)
				return
			}
			binding.TeamRef, binding.TeamName = "", ""
			if err := adapterkit.SaveProfile(r.configDir, r.newKey, binding); err != nil {
				t.Error(err)
			}
			r.b.calls = nil
		}}, nil
	}
	out, err := teammove.Follow(t.Context(), teammove.Inputs{
		ConfigDir: r.configDir, Checkout: second, File: r.file, Pin: &r.pin, Client: leaving, Now: now,
	})
	if err != nil || out.Followed || out.Reason != teammove.ReasonLeft || !left {
		t.Fatalf("the follow that waited while the member left: %+v, %v (left %v)", out, err, left)
	}
	// Its describe is answered from local files; no backend was asked.
	if len(r.b.called("team moved")) != 0 || len(r.b.called("team join")) != 0 {
		t.Fatalf("a backend was asked for a member that left: %v", r.b.verbs())
	}
	if pin, ok, err := teamstore.LookupPin(r.configDir, second); err != nil || !ok || pin.Adapter != "supabase" {
		t.Fatalf("the second checkout's pin moved: %+v, %v", pin, err)
	}
}

// TestFollowMovesAStaleServerProfileBeforeTheJoin: a profile at the new
// backend's key that names another certificate, which a refused join or a
// server's replaced certificate leaves, would make the adapter answer
// `conflict` to the join's backend. The record named this backend and this
// certificate, so the profile is moved to them first, and the join goes
// through.
func TestFollowMovesAStaleServerProfileBeforeTheJoin(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	stale := strings.Replace(newPin, "2", "9", 1)
	if err := adapterkit.SaveProfile(r.configDir, r.newKey, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "rest", URL: newURL, Certificate: stale,
	}); err != nil {
		t.Fatal(err)
	}
	out, err := r.follow()
	if err != nil || !out.Followed || !out.Joined {
		t.Fatalf("follow: %+v, %v", out, err)
	}
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved", "rest:profile init", "rest:team join", "rest:describe"}) {
		t.Fatalf("adapter calls: %v", got)
	}
	if init := r.b.called("profile init")[0]; !slices.Equal(init.flags, []string{"--force", "--url", newURL, "--certificate", newPin}) {
		t.Fatalf("profile init flags: %v", init.flags)
	}
	if binding, err := teamstore.LoadBinding(r.configDir, r.newKey); err != nil || teamstore.VerifyBinding(binding, r.file) != nil {
		t.Fatalf("the binding after the follow: %+v, %v", binding, err)
	}
}

// TestFollowCarriesTheLabelsRecord: the record of where the member's label
// came from goes to the new credential directory when it describes the
// label that was sent, and not when it describes another.
func TestFollowCarriesTheLabelsRecord(t *testing.T) {
	t.Parallel()
	for name, described := range map[string]string{"the label that was sent": label, "another label": "someone-else@example.com"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			if err := write.LabelOrigin(r.configDir, r.oldKey, teamstore.LabelOrigin{
				Source: teamstore.LabelFromFlag, SentAt: time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC), LabelSHA256: teamstore.LabelDigest(described),
			}); err != nil {
				t.Fatal(err)
			}
			if out, err := r.follow(); err != nil || !out.Followed {
				t.Fatalf("follow: %+v, %v", out, err)
			}
			origin, err := teamstore.LoadLabelOrigin(r.configDir, r.newKey)
			if carried := err == nil && origin.Source == teamstore.LabelFromFlag && origin.Describes(label); carried != (described == label) {
				t.Fatalf("the label's record in the new directory: %+v, %v", origin, err)
			}
		})
	}
}

// TestCandidateAndJoinBackendAndHost pin the three small rules the hook
// and the join command share with a follow.
func TestCandidateAndJoinBackendAndHost(t *testing.T) {
	t.Parallel()
	pin := &teamstore.Pin{Adapter: "supabase", URL: oldURL, PublishableKey: oldKeyP, TeamRef: teamRef}
	for name, tc := range map[string]struct {
		file *teamfile.File
		want bool
	}{
		"another adapter and url, the same team": {&teamfile.File{Adapter: "rest", URL: newURL, TeamRef: teamRef}, true},
		"another url alone":                      {&teamfile.File{Adapter: "supabase", URL: "https://other.supabase.example", TeamRef: teamRef}, true},
		"another team":                           {&teamfile.File{Adapter: "rest", URL: newURL, TeamRef: "other"}, false},
		"the same backend, another key":          {&teamfile.File{Adapter: "supabase", URL: oldURL, PublishableKey: "sb_publishable_y", TeamRef: teamRef}, false},
		"the same backend, a certificate added":  {&teamfile.File{Adapter: "supabase", URL: oldURL, Certificate: newPin, TeamRef: teamRef}, false},
	} {
		if got := teammove.Candidate(pin, tc.file); got != tc.want {
			t.Errorf("%s: candidate %v, want %v", name, got, tc.want)
		}
	}
	if teammove.Candidate(nil, &teamfile.File{TeamRef: teamRef}) || teammove.Candidate(pin, nil) {
		t.Errorf("a missing pin or file is a candidate")
	}
	if got := teammove.JoinBackend(&teamfile.File{Adapter: "supabase", URL: oldURL, PublishableKey: oldKeyP}); got["url"] != oldURL || got["publishable_key"] != oldKeyP || len(got) != 2 {
		t.Errorf("a Supabase team's backend: %v", got)
	}
	if got := teammove.JoinBackend(&teamfile.File{Adapter: "rest", URL: newURL}); got["url"] != newURL || len(got) != 1 {
		t.Errorf("a server's backend with no pin: %v", got)
	}
	if got := teammove.JoinBackend(&teamfile.File{Adapter: "rest", URL: newURL, Certificate: newPin}); got["certificate"] != newPin || len(got) != 2 {
		t.Errorf("a server's backend with a pin: %v", got)
	}
	for raw, want := range map[string]string{
		newURL: "brigade.example.test", "https://127.0.0.1:8743": "127.0.0.1:8743", "https://h.example.test/prefix/": "h.example.test",
		"no-scheme.example.test": "no-scheme.example.test", "": "",
	} {
		if got := teammove.Host(raw); got != want {
			t.Errorf("host of %q: %q, want %q", raw, got, want)
		}
	}
}

// TestOlderThanFollow: a session is named as one that will not follow only
// when the version it reports can be read and is before 0.55.0, the first
// that follows a recorded move. A development build's suffix is left out;
// anything that is not three runs of digits says nothing about the session.
func TestOlderThanFollow(t *testing.T) {
	t.Parallel()
	for v, want := range map[string]bool{
		"0.54.9": true, "0.9.1": true, "0.0.0": true, "0.54.99": true, "0.54.0-dev": true, "0.1.0+build.7": true,
		"0.55.0": false, "0.55.0-dev": false, "0.55.1": false, "0.56.0": false, "1.0.0": false, "10.0.0": false,
		"": false, "unknown": false, "0.54": false, "0.54.9.1": false, "v0.54.9": false, "0.54.x": false, "0..9": false,
		"-0.54.9": false, "0.54.9 ": false, "0.5 4.9": false, "0.54.99999999999999999999": false, "٠.٥٤.٩": false,
	} {
		if got := teammove.OlderThanFollow(v); got != want {
			t.Errorf("OlderThanFollow(%q) = %v, want %v", v, got, want)
		}
	}
	if teammove.OlderThanFollow(teammove.FollowsFromVersion) || !teammove.OlderThanFollow("0.54.999") {
		t.Errorf("FollowsFromVersion %q is not the first version that follows", teammove.FollowsFromVersion)
	}
}

// asking is a Caller whose `team moved` first runs before: the question to
// the old backend is the one call of a follow that can hang, and it is
// asked under the follow's lock, so what before does happens while the
// follow that called it holds the lock and waits for its backend.
type asking struct {
	teammove.Caller
	before func()
}

func (a asking) Call(ctx context.Context, group, verb string, flags []string, stdin any) (*protocol.Envelope, error) {
	if group+" "+verb == "team moved" {
		a.before()
	}
	return a.Caller.Call(ctx, group, verb, flags, stdin)
}

// TestAFollowSeesTheJoinThatWentAheadWhileItsBackendHung: a member's own
// `brigade team join` waits ten seconds for the follow's lock and then goes
// on without it (work item 327): a join is the member's command and is not
// refused because a follow's old backend is slow. So a follow can come back
// from its question to find that the machine joined meanwhile. It read the
// machine's membership when it took the lock, before the question; it reads
// it again once the backend has answered, just before it would join, and
// sends no join of its own. When the member joined in another checkout, the
// follow writes this checkout's pin and leaves word that the machine had
// joined already. When the member joined in this very checkout, the pin is
// theirs: the follow writes nothing and leaves no word.
func TestAFollowSeesTheJoinThatWentAheadWhileItsBackendHung(t *testing.T) {
	t.Parallel()
	joinedBy := func(r *rig) {
		r.t.Helper()
		key := teamstore.Key(r.file.Adapter, r.file.URL, r.file.TeamRef)
		if err := write.EnsureBinding(r.configDir, key, r.file.Adapter, r.file.URL, r.file.PublishableKey, r.file.Certificate,
			r.file.TeamRef, r.file.TeamName, "p_the_members_own_join"); err != nil {
			r.t.Fatal(err)
		}
	}
	follow := func(r *rig, before func(), word *[]bool) (teammove.Outcome, error) {
		r.t.Helper()
		return teammove.Follow(r.t.Context(), teammove.Inputs{
			ConfigDir: r.configDir, Checkout: r.checkout, File: r.file, Pin: &r.pin,
			Client: func(adapter, profile string) (teammove.Caller, error) {
				c, err := r.b.client(adapter, profile)
				if err != nil {
					return nil, err
				}
				return asking{Caller: c, before: before}, nil
			},
			Now:       func() time.Time { return time.Date(2026, 11, 15, 9, 0, 0, 0, time.UTC) },
			BeforePin: func(joined bool) { *word = append(*word, joined) },
		})
	}

	t.Run("the member joined in another checkout", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		var word []bool
		out, err := follow(r, func() { joinedBy(r) }, &word)
		if err != nil || !out.Followed || out.Joined {
			t.Fatalf("the follow: %+v, %v", out, err)
		}
		if joins := r.b.called("team join"); len(joins) != 0 {
			t.Fatalf("the follow sent %d join(s) for a machine its member had joined meanwhile", len(joins))
		}
		if pin := r.currentPin(); !pin.Matches(r.file) || pin.MovedFrom != oldURL || !slices.Equal(word, []bool{false}) {
			t.Fatalf("the pin %+v and the word the follow left, %v", pin, word)
		}
	})

	t.Run("the member joined in this checkout", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		consented := time.Date(2026, 11, 15, 8, 59, 0, 0, time.UTC)
		var word []bool
		out, err := follow(r, func() {
			joinedBy(r)
			if err := write.Pin(r.configDir, r.checkout, teamstore.Pin{
				Adapter: r.file.Adapter, URL: r.file.URL, PublishableKey: r.file.PublishableKey, Certificate: r.file.Certificate,
				TeamRef: r.file.TeamRef, ConsentedAt: consented,
			}); err != nil {
				t.Fatal(err)
			}
		}, &word)
		if err != nil || !out.Followed || out.Joined {
			t.Fatalf("the follow: %+v, %v", out, err)
		}
		if joins := r.b.called("team join"); len(joins) != 0 {
			t.Fatalf("the follow sent %d join(s) for a machine its member had joined meanwhile", len(joins))
		}
		if pin := r.currentPin(); !pin.Matches(r.file) || pin.MovedFrom != "" || !pin.ConsentedAt.Equal(consented) || len(word) != 0 {
			t.Fatalf("the member's pin after the follow: %+v; the follow left word: %v", pin, word)
		}
	})

	t.Run("the member left the new backend meanwhile", func(t *testing.T) {
		t.Parallel()
		r := newRig(t)
		var word []bool
		out, err := follow(r, func() {
			key := teamstore.Key(r.file.Adapter, r.file.URL, r.file.TeamRef)
			if err := adapterkit.SaveProfile(r.configDir, key, &adapterkit.Profile{
				Version: adapterkit.ProfileVersion, Adapter: r.file.Adapter, URL: r.file.URL, Certificate: r.file.Certificate, PrincipalRef: "p_left",
			}); err != nil {
				t.Fatal(err)
			}
		}, &word)
		if err != nil || out.Followed || out.Reason != teammove.ReasonLeft || len(r.b.called("team join")) != 0 || len(word) != 0 {
			t.Fatalf("the follow of a machine that left meanwhile: %+v, %v; %d joins", out, err, len(r.b.called("team join")))
		}
	})
}

// TestFollowsFromTheBackendATeamLeaves: a session follows a move that a
// Brigade server recorded from a later plugin than a move any other
// backend recorded, because the record is read by the adapter of the
// backend a team leaves and the `rest` adapter reads one from 0.57.0 on
// (work item 341, ADR 0010).
func TestFollowsFromTheBackendATeamLeaves(t *testing.T) {
	t.Parallel()
	if got := teammove.FollowsFrom("rest"); got != "0.57.0" {
		t.Errorf("FollowsFrom(rest) = %q, want %q", got, "0.57.0")
	}
	if teammove.FollowsFromServerVersion != "0.57.0" {
		t.Errorf("FollowsFromServerVersion = %q, want the release that carries the server's record, 0.57.0", teammove.FollowsFromServerVersion)
	}
	for _, adapter := range []string{"supabase", "fs", "", "an-adapter-of-their-own"} {
		if got := teammove.FollowsFrom(adapter); got != teammove.FollowsFromVersion {
			t.Errorf("FollowsFrom(%q) = %q, want %q", adapter, got, teammove.FollowsFromVersion)
		}
	}
	for v, want := range map[string]struct{ server, other bool }{
		"0.54.9":     {true, true},
		"0.55.0":     {true, false},
		"0.56.0":     {true, false},
		"0.56.9-dev": {true, false},
		"0.57.0":     {false, false},
		"0.57.0-dev": {false, false},
		"1.0.0":      {false, false},
		"unknown":    {false, false},
		"":           {false, false},
		"0.57":       {false, false},
	} {
		if got := teammove.OlderThanFollowFrom("rest", v); got != want.server {
			t.Errorf("OlderThanFollowFrom(rest, %q) = %v, want %v", v, got, want.server)
		}
		if got := teammove.OlderThanFollowFrom("supabase", v); got != want.other || got != teammove.OlderThanFollow(v) {
			t.Errorf("OlderThanFollowFrom(supabase, %q) = %v, want %v", v, got, want.other)
		}
	}
}

// memberAtTheNewBackend gives the rig's machine a membership of the team
// on the new backend, as a machine has that was on it before the team left
// it, and a file in its directory that stands for the credential.
func (r *rig) memberAtTheNewBackend() string {
	r.t.Helper()
	if err := write.EnsureBinding(r.configDir, r.newKey, "rest", newURL, "", newPin, teamRef, teamName, "p-before"); err != nil {
		r.t.Fatal(err)
	}
	credential := filepath.Join(r.configDir, "teams", r.newKey, "credential.json")
	if err := os.WriteFile(credential, []byte(`{"held":"before"}`+"\n"), 0o600); err != nil {
		r.t.Fatal(err)
	}
	return credential
}

// setAside lists the directories of the store's teams that carry a
// create-time name: a membership set aside would be one, and no follow
// leaves one.
func (r *rig) setAside() []string {
	r.t.Helper()
	entries, err := os.ReadDir(filepath.Join(r.configDir, "teams"))
	if err != nil {
		r.t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tmp-") {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestFollowJoinsOnTheProfileItHoldsWhereTheNewBackendNoLongerHonoursIt: a
// team that comes back to a backend its administrator had ended it on
// (work item 341). The machine holds the membership it had there, and the
// backend answers `unauthorized` to it. The follow does not pin the
// checkout on its strength: it joins with the move's ticket, under the
// profile it holds, so the adapter presents the credential that is there
// and the backend acts for the same principal. Nothing is set aside: a
// first form of this did that, and a banned member came back as a
// principal the ban did not know.
func TestFollowJoinsOnTheProfileItHoldsWhereTheNewBackendNoLongerHonoursIt(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	credential := r.memberAtTheNewBackend()
	r.b.newRecords = true
	r.b.newMovedErrs = []error{&protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}}
	// The backend acts for the principal whose credential was presented.
	r.b.join = &protocol.TeamJoinResult{TeamRef: teamRef, TeamName: teamName, PrincipalRef: "p-before", Rejoined: true}
	out, err := r.follow()
	if err != nil || !out.Followed || !out.Joined {
		t.Fatalf("follow: %+v, %v", out, err)
	}
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved", "rest:describe", "rest:team moved", "rest:team join", "rest:describe", "rest:team moved"}) {
		t.Fatalf("adapter calls: %v", got)
	}
	joined := r.b.called("team join")[0]
	if req, ok := joined.stdin.(*protocol.TeamJoinRequest); !ok || joined.profile != r.newKey || req.JoinSecret != ticket || req.HumanLabel != label {
		t.Fatalf("the join was not made under the profile the machine holds, with the ticket and the member's label")
	}
	binding, err := teamstore.LoadBinding(r.configDir, r.newKey)
	if err != nil || binding.PrincipalRef != "p-before" || teamstore.VerifyBinding(binding, r.file) != nil {
		t.Fatalf("the binding after the follow: %+v, %v", binding, err)
	}
	if data, err := os.ReadFile(credential); err != nil || !strings.Contains(string(data), "before") {
		t.Fatalf("the credential the machine held is not where it was: %q, %v", data, err)
	}
	if aside := r.setAside(); len(aside) != 0 {
		t.Fatalf("a membership was set aside: %v", aside)
	}
	if pin := r.currentPin(); !pin.Matches(r.file) || pin.MovedFrom != oldURL {
		t.Fatalf("the pin after the follow: %+v", pin)
	}
}

// TestFollowLeavesOutAMemberTheNewBackendRefusesToLetBackIn: the backend
// answers `unauthorized` to the membership the machine holds and refuses
// the join made with its credential, as it refuses a principal that is
// banned. The checkout is not pinned, nothing of the membership is touched
// or set aside, and the machine joined nothing as anyone else.
func TestFollowLeavesOutAMemberTheNewBackendRefusesToLetBackIn(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	credential := r.memberAtTheNewBackend()
	r.b.newRecords = true
	refused := &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
	r.b.newMovedErrs = []error{refused}
	r.b.joinErr = &protocol.Error{Code: protocol.CodeUnauthorized, Message: "this principal is banned from the team"}
	out, _ := r.follow()
	if out.Followed || out.Reason != teammove.ReasonRejoinRefused {
		t.Fatalf("follow: %+v, want %s", out, teammove.ReasonRejoinRefused)
	}
	if joins := r.b.called("team join"); len(joins) != 1 || joins[0].profile != r.newKey {
		t.Fatalf("the joins of a member who is refused: %v", r.b.verbs())
	}
	binding, err := teamstore.LoadBinding(r.configDir, r.newKey)
	if err != nil || binding.PrincipalRef != "p-before" {
		t.Fatalf("the membership the machine held after a refused join: %+v, %v", binding, err)
	}
	if _, err := os.Stat(credential); err != nil || len(r.setAside()) != 0 {
		t.Fatalf("the credential is gone (%v) or something was set aside: %v", err, r.setAside())
	}
	if pin := r.currentPin(); pin != r.pin {
		t.Fatalf("the pin moved: %+v", pin)
	}
}

// TestFollowPresentsNoTicketForAWhileAfterARejoinIsRefused: the backend
// does not honour the membership the machine holds and refuses the join
// made with the ticket under its credential. A backend counts the joins a
// principal fails, and a follow runs at every session's start: until this
// each one presented the ticket again, and the member's own join with the
// right secret found the count spent. The refusal is put on record, and
// for RejoinPause no follow presents a ticket there, in this checkout or
// another. The first follow after that asks once more: one that is refused
// keeps the time of the first refusal and pauses again, and one that is
// let in, a member whose ban was lifted, follows and removes the record.
func TestFollowPresentsNoTicketForAWhileAfterARejoinIsRefused(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.memberAtTheNewBackend()
	r.b.newRecords = true
	notAMember := &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
	r.b.newMovedErrs = []error{notAMember, notAMember, notAMember, notAMember, notAMember, notAMember}
	r.b.joinErr = &protocol.Error{Code: protocol.CodeUnauthorized, Message: "join secret rejected"}
	joins := func() int { return len(r.b.called("team join")) }
	refused := func(when string, wantJoins int) *teamstore.RejoinRefusal {
		t.Helper()
		out, _ := r.follow()
		if out.Followed || out.Reason != teammove.ReasonRejoinRefused || joins() != wantJoins {
			t.Fatalf("%s: %+v, %d joins; want %s and %d joins", when, out, joins(), teammove.ReasonRejoinRefused, wantJoins)
		}
		if pin := r.currentPin(); pin != r.pin {
			t.Fatalf("%s: the pin moved: %+v", when, pin)
		}
		record, err := teamstore.LoadRejoinRefusal(r.configDir, r.newKey)
		if err != nil {
			t.Fatalf("%s: the record of the refusal: %v", when, err)
		}
		return record
	}
	began := r.now
	if record := refused("the follow that is refused", 1); !record.RefusedAt.Equal(began) || !record.FirstRefusedAt.Equal(began) {
		t.Fatalf("the record of the first refusal: %+v", record)
	}
	// Inside the pause, in this checkout and in another of the team: nothing is presented.
	r.now = began.Add(time.Minute)
	refused("a follow a minute later", 1)
	other := filepath.Join(t.TempDir(), "checkout-2")
	if err := write.Pin(r.configDir, other, r.pin); err != nil {
		t.Fatal(err)
	}
	r.now = began.Add(teammove.RejoinPause - time.Second)
	out, _ := teammove.Follow(t.Context(), teammove.Inputs{
		ConfigDir: r.configDir, Checkout: other, File: r.file, Pin: &r.pin, Client: r.b.client, Now: func() time.Time { return r.now },
	})
	if out.Followed || out.Reason != teammove.ReasonRejoinRefused || joins() != 1 {
		t.Fatalf("a follow of another checkout inside the pause: %+v, %d joins", out, joins())
	}
	// The pause is over: one more join is asked for, refused, and the pause begins again.
	r.now = began.Add(teammove.RejoinPause)
	if record := refused("the first follow after the pause", 2); !record.RefusedAt.Equal(r.now) || !record.FirstRefusedAt.Equal(began) {
		t.Fatalf("the record of the second refusal keeps no time of the first: %+v", record)
	}
	r.now = r.now.Add(time.Minute)
	refused("a follow inside the second pause", 2)
	// The ban is lifted. Inside the pause nothing is asked; after it the member is let in.
	r.b.joinErr = nil
	r.b.join = &protocol.TeamJoinResult{TeamRef: teamRef, TeamName: teamName, PrincipalRef: "p-before", Rejoined: true}
	r.now = began.Add(2 * teammove.RejoinPause)
	out, err := r.follow()
	if err != nil || !out.Followed || !out.Joined || joins() != 3 {
		t.Fatalf("the follow after the ban was lifted and the pause had passed: %+v, %v, %d joins", out, err, joins())
	}
	if _, err := teamstore.LoadRejoinRefusal(r.configDir, r.newKey); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record of the refusal after a join that succeeded: %v", err)
	}
	if binding, err := teamstore.LoadBinding(r.configDir, r.newKey); err != nil || binding.PrincipalRef != "p-before" {
		t.Fatalf("the membership after he was let in: %+v, %v", binding, err)
	}
}

// TestFollowForgetsARefusalOnceTheBackendHonoursTheMembership: a refusal is
// on record, and the backend honours the membership again, as it does after
// a join made by any other road. The follow pins the checkout on it,
// presents nothing, and removes the record.
func TestFollowForgetsARefusalOnceTheBackendHonoursTheMembership(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.memberAtTheNewBackend()
	r.b.newRecords = true
	if err := write.RejoinRefusal(r.configDir, r.newKey, r.now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	out, err := r.follow()
	if err != nil || !out.Followed || out.Joined || len(r.b.called("team join")) != 0 {
		t.Fatalf("follow: %+v, %v; calls %v", out, err, r.b.verbs())
	}
	if _, err := teamstore.LoadRejoinRefusal(r.configDir, r.newKey); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the record of the refusal after the backend honoured the membership: %v", err)
	}
}

// TestFollowKeepsAMembershipTheNewBackendDidNotAnswerFor: any failure of
// the new backend's answer but its `unauthorized` is no word about the
// membership. Nothing is set aside, nothing is joined and no pin is
// written; the next follow asks again, and follows on an answer.
func TestFollowKeepsAMembershipTheNewBackendDidNotAnswerFor(t *testing.T) {
	t.Parallel()
	for name, failure := range map[string]error{
		"unavailable": &protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend could not be reached"},
		"a deadline":  context.DeadlineExceeded,
		"no ticket":   nil,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			credential := r.memberAtTheNewBackend()
			r.b.newRecords = true
			want := teammove.ReasonNewBackendFailed
			if failure == nil {
				// The backend refuses the membership, and the record carries no
				// ticket to join with: the membership is not set aside for nothing.
				failure, want = &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}, teammove.ReasonNoTicket
				r.b.ticket = ""
			}
			r.b.newMovedErrs = []error{failure}
			out, _ := r.follow()
			if out.Followed || out.Reason != want {
				t.Fatalf("follow: %+v, want %s", out, want)
			}
			if len(r.b.called("team join")) != 0 || len(r.setAside()) != 0 {
				t.Fatalf("a follow whose question was not answered joined or set something aside: %v, %v", r.b.verbs(), r.setAside())
			}
			if _, err := os.Stat(credential); err != nil {
				t.Fatalf("the membership the machine held is gone: %v", err)
			}
			if pin := r.currentPin(); pin != r.pin {
				t.Fatalf("the pin moved: %+v", pin)
			}
			if want == teammove.ReasonNewBackendFailed {
				// The next follow is answered, and follows with the membership it held.
				out, err := r.follow()
				if err != nil || !out.Followed || out.Joined {
					t.Fatalf("the follow after that: %+v, %v", out, err)
				}
			}
		})
	}
}

// TestFollowTellsABackendThatRefusesFromOneThatDidNotAnswer: the new
// backend answers the follow's question, and its answer is a refusal that
// is not `unauthorized`: a credential it does not know, above all, which is
// what a server started again from an older copy of its data answers. That
// is not a backend that did not answer, and it is told apart: the caller
// has a member to send to a join, not one to keep waiting. Nothing is
// joined, nothing of the membership is touched and no pin is written.
func TestFollowTellsABackendThatRefusesFromOneThatDidNotAnswer(t *testing.T) {
	t.Parallel()
	for name, refusal := range map[string]error{
		"a credential it does not know": &protocol.Error{Code: protocol.CodeUnauthenticated, Message: "the credential is not known to this server"},
		"no such endpoint":              &protocol.Error{Code: protocol.CodeNotFound, Message: "no such endpoint"},
		"not an envelope":               &protocol.Error{Code: protocol.CodeInternal, Message: "the backend answered with something that is not a result envelope"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			credential := r.memberAtTheNewBackend()
			r.b.newRecords = true
			r.b.newMovedErrs = []error{refusal}
			out, err := r.follow()
			if out.Followed || out.Reason != teammove.ReasonNewBackendRefused || !errors.Is(err, refusal) {
				t.Fatalf("follow: %+v, %v, want %s", out, err, teammove.ReasonNewBackendRefused)
			}
			if len(r.b.called("team join")) != 0 {
				t.Fatalf("a follow that was refused joined: %v", r.b.verbs())
			}
			if _, err := os.Stat(credential); err != nil {
				t.Fatalf("the membership the machine held is gone: %v", err)
			}
			if pin := r.currentPin(); pin != r.pin {
				t.Fatalf("the pin moved: %+v", pin)
			}
		})
	}
}

// TestFollowTellsABackendTheMemberJoinedThatRefusesFromOneThatDidNotAnswer
// (work item 341): the one question a follow asks of the backend the
// member joined, whether the team moved, is refused, or is not answered.
// Until this both were one reason, and its readers took it for a backend
// that did not answer: a member who had been revoked there, over a mark an
// earlier follow left of the server the team moved to, was told at every
// start that that server did not answer and that a session tries again,
// and only his own join got him in. A refusal is an answer, as it is on
// the other backend's side: the two are told apart, and neither sends a
// join or moves the pin.
func TestFollowTellsABackendTheMemberJoinedThatRefusesFromOneThatDidNotAnswer(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		failure error
		// shape is an answer that is no record of the protocol's.
		shape  bool
		reason string
	}{
		"it no longer counts the member one": {failure: &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}, reason: teammove.ReasonOldBackendRefused},
		"a credential it does not know":      {failure: &protocol.Error{Code: protocol.CodeUnauthenticated, Message: "the credential is not known to this server"}, reason: teammove.ReasonOldBackendRefused},
		"not an envelope":                    {failure: &protocol.Error{Code: protocol.CodeInternal, Message: "the backend answered with something that is not a result envelope"}, reason: teammove.ReasonOldBackendRefused},
		"a shape that is not the protocol's": {shape: true, reason: teammove.ReasonOldBackendRefused},
		"it could not be reached":            {failure: &protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend could not be reached"}, reason: teammove.ReasonOldBackendFailed},
		"its answer did not come in time":    {failure: context.DeadlineExceeded, reason: teammove.ReasonOldBackendFailed},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.b.movedErr = tc.failure
			if tc.shape {
				r.b.moved = &protocol.TeamMoveResult{Moved: true}
			}
			out, err := r.follow()
			if out.Followed || out.Reason != tc.reason || err == nil || (tc.failure != nil && !errors.Is(err, tc.failure)) {
				t.Fatalf("follow: %+v, %v, want %s", out, err, tc.reason)
			}
			if len(r.b.called("team moved")) != 1 || len(r.b.called("team join")) != 0 {
				t.Fatalf("the adapter calls of a follow whose question was not answered with a record: %v", r.b.verbs())
			}
			if pin := r.currentPin(); pin != r.pin {
				t.Fatalf("the pin moved: %+v", pin)
			}
		})
	}
}

// clearing is a Caller that answers the clear of a record with err, or
// with a team that has not moved.
type clearing struct{ err error }

func (clearing) Describe(context.Context) (*protocol.DescribeResult, error) {
	return &protocol.DescribeResult{Capabilities: []string{teammove.Capability}}, nil
}

func (c clearing) Call(context.Context, string, string, []string, any) (*protocol.Envelope, error) {
	if c.err != nil {
		return nil, c.err
	}
	raw, err := json.Marshal(&protocol.TeamMoveResult{TeamRef: teamRef})
	if err != nil {
		return nil, err
	}
	return &protocol.Envelope{OK: true, ProtocolVersion: protocol.ProtocolVersion, Result: raw}, nil
}

// TestAClearAnsweredWithAWaitIsARefusal (work item 341): what became of a
// clear. A backend that answers the clear with a wait has answered, and
// the record stands: it is a refusal, as it is of any answer that is not a
// failure of the backend. Told as a clear that was not answered, the
// command said that the answer did not arrive and that the record may be
// cleared all the same, which is untrue. Only a clear that could not be
// reached, a failure of the backend and an answer that is no envelope
// leave it unknown whether the record stands.
func TestAClearAnsweredWithAWaitIsARefusal(t *testing.T) {
	t.Parallel()
	for name, tc := range map[string]struct {
		err  error
		want teammove.ClearOutcome
	}{
		"cleared":                   {nil, teammove.Cleared},
		"it says to wait":           {&protocol.Error{Code: protocol.CodeRateLimited, Message: "slow down", RetryAfterMS: 1000}, teammove.ClearRefused},
		"it could not be reached":   {&protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend could not be reached"}, teammove.ClearUnanswered},
		"its answer is no envelope": {&protocol.Error{Code: protocol.CodeInternal, Message: "the backend answered with something that is not a result envelope"}, teammove.ClearUnanswered},
		"the caller's time ran out": {context.DeadlineExceeded, teammove.ClearUnanswered},
		"the caller may not":        {&protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}, teammove.ClearRefused},
		"no such endpoint":          {&protocol.Error{Code: protocol.CodeNotFound, Message: "no such endpoint"}, teammove.ClearRefused},
	} {
		if got, err := teammove.ClearRecord(t.Context(), clearing{err: tc.err}); got != tc.want || (tc.err != nil && !errors.Is(err, tc.err)) {
			t.Errorf("%s: outcome %d, %v, want %d", name, got, err, tc.want)
		}
	}
}

// undescribed is a Caller whose describe fails with err, as the adapter of
// the backend the member joined does when it is not set up, prints what is
// no envelope, or does not end in time. It is asked nothing else.
type undescribed struct {
	teammove.Caller
	err error
}

func (u undescribed) Describe(context.Context) (*protocol.DescribeResult, error) { return nil, u.err }

// TestADescribeThatFailsIsNoRefusalOfTheBackendTheMemberJoined (work item
// 341): before a follow asks the backend the member joined anything, it
// reads what that backend's adapter offers, which the adapter answers from
// local files. When that fails, no backend was asked: nothing was learned
// of the member, of the team or of the backend the team file names. It
// was told as that backend's refusal when its failure was anything but a
// deadline, which takes back what an earlier follow left and has the
// member read that the team file does not match. Only an answer to the
// question can be a refusal: a describe that fails is a backend that did
// not answer, whatever its code.
func TestADescribeThatFailsIsNoRefusalOfTheBackendTheMemberJoined(t *testing.T) {
	t.Parallel()
	for name, failure := range map[string]error{
		"the adapter is not set up":            &protocol.Error{Code: protocol.CodeConfig, Message: "the adapter is not set up"},
		"what it printed is no envelope":       &protocol.Error{Code: protocol.CodeInternal, Message: "adapter stdout is not a single protocol JSON envelope"},
		"it refuses, as no describe should":    &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"},
		"its executable is gone":               &protocol.Error{Code: protocol.CodeUnavailable, Message: "adapter executable not found"},
		"a failure that is not the protocol's": errors.New("exec: no such file or directory"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			out, err := teammove.Follow(t.Context(), teammove.Inputs{
				ConfigDir: r.configDir, Checkout: r.checkout, File: r.file, Pin: &r.pin,
				Client: func(adapter, profile string) (teammove.Caller, error) {
					c, cerr := r.b.client(adapter, profile)
					if cerr != nil || adapter == "rest" {
						return c, cerr
					}
					return undescribed{Caller: c, err: failure}, nil
				},
				Now: func() time.Time { return r.now },
			})
			if out.Followed || out.Reason != teammove.ReasonOldBackendFailed || !errors.Is(err, failure) {
				t.Fatalf("follow: %+v, %v, want %s", out, err, teammove.ReasonOldBackendFailed)
			}
			if asked := r.b.verbs(); len(asked) != 0 {
				t.Fatalf("a follow whose describe failed asked %v", asked)
			}
			if pin := r.currentPin(); pin != r.pin {
				t.Fatalf("the pin moved: %+v", pin)
			}
		})
	}
}

// TestAnAnswerTheProtocolCallsRetryableIsNoAnswerAboutTheMember (work item
// 341): a backend answers `rate_limited`, with how long to wait. That says
// nothing of the member or of the team: the protocol's own table calls it
// retryable, as it calls a backend that could not be reached. Until this it
// counted as a refusal wherever a follow asks a backend anything: the
// backend the member joined took back what an earlier follow had left and
// the member was told that the team file does not match; the backend the
// team moved to was told as one that refuses the membership; and a join
// was a join the backend refused. It is a backend that did not answer, in
// each place, and nothing is joined, pinned or put on record for it. An
// answer the table does not call retryable stays what it was.
func TestAnAnswerTheProtocolCallsRetryableIsNoAnswerAboutTheMember(t *testing.T) {
	t.Parallel()
	notAMember := &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
	for name, tc := range map[string]struct {
		// asked scripts err as the answer to what the follow asks.
		asked func(r *rig, err error)
		// retryable and other are the reasons of a retryable answer and of one
		// the table does not call retryable.
		retryable, other string
		joins            int
	}{
		"the question to the backend the member joined": {
			asked:     func(r *rig, err error) { r.b.movedErr = err },
			retryable: teammove.ReasonOldBackendFailed, other: teammove.ReasonOldBackendRefused,
		},
		"the question to the backend the team moved to": {
			asked: func(r *rig, err error) {
				r.memberAtTheNewBackend()
				r.b.newRecords, r.b.newMovedErrs = true, []error{err}
			},
			retryable: teammove.ReasonNewBackendFailed, other: teammove.ReasonNewBackendRefused,
		},
		"a machine's first join": {
			asked:     func(r *rig, err error) { r.b.joinErr = err },
			retryable: teammove.ReasonNewBackendFailed, other: teammove.ReasonJoinRefused, joins: 1,
		},
		"a join under the membership it holds": {
			asked: func(r *rig, err error) {
				r.memberAtTheNewBackend()
				r.b.newRecords, r.b.newMovedErrs, r.b.joinErr = true, []error{notAMember}, err
			},
			retryable: teammove.ReasonNewBackendFailed, other: teammove.ReasonJoinRefused, joins: 1,
		},
	} {
		for kind, answer := range map[string]struct {
			err  *protocol.Error
			want string
		}{
			"rate_limited": {&protocol.Error{Code: protocol.CodeRateLimited, Message: "slow down", RetryAfterMS: 1000}, tc.retryable},
			"internal":     {&protocol.Error{Code: protocol.CodeInternal, Message: "the server failed"}, tc.other},
		} {
			t.Run(name+", "+kind, func(t *testing.T) {
				t.Parallel()
				if answer.err.Code.Retryable() != (kind == "rate_limited") {
					t.Fatalf("the protocol's table calls %s retryable: %v; the test holds nothing", kind, answer.err.Code.Retryable())
				}
				r := newRig(t)
				tc.asked(r, answer.err)
				out, err := r.follow()
				if out.Followed || out.Reason != answer.want || !errors.Is(err, answer.err) {
					t.Fatalf("follow: %+v, %v, want %s", out, err, answer.want)
				}
				if joins := len(r.b.called("team join")); joins != tc.joins {
					t.Fatalf("the follow presented the ticket %d times, want %d", joins, tc.joins)
				}
				if _, err := teamstore.LoadRejoinRefusal(r.configDir, r.newKey); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("a refusal is on record: %v", err)
				}
				if pin := r.currentPin(); pin != r.pin {
					t.Fatalf("the pin moved: %+v", pin)
				}
			})
		}
	}
}

// TestAJoinThatWasNotAnsweredIsABackendThatDidNotAnswer (work item 341):
// the join a follow sends to the backend the team moved to gets no answer:
// the backend could not be reached, or the caller's time ran out. Until this
// every failure of that join was a join the backend refused, and a member
// who never held a membership there was told at a session's start that the
// team file does not match and to run `brigade team join`, of a server that
// was only down. A join that was not answered is a backend that did not
// answer, on the first join and on the join made under a membership the
// machine holds alike: nothing is pinned, no binding is written, no refusal
// is put on record or counted, and the next follow presents the ticket
// again and follows.
func TestAJoinThatWasNotAnsweredIsABackendThatDidNotAnswer(t *testing.T) {
	t.Parallel()
	notAMember := &protocol.Error{Code: protocol.CodeUnauthorized, Message: "not an active member of this team"}
	for name, failure := range map[string]error{
		"unavailable": &protocol.Error{Code: protocol.CodeUnavailable, Message: "the backend could not be reached"},
		"a deadline":  context.DeadlineExceeded,
	} {
		for road, held := range map[string]bool{"a first join": false, "a join under the membership it holds": true} {
			t.Run(name+", "+road, func(t *testing.T) {
				t.Parallel()
				r := newRig(t)
				if held {
					// The backend no longer honours the membership the machine holds,
					// so the follow joins with the ticket on that profile.
					r.memberAtTheNewBackend()
					r.b.newRecords = true
					r.b.newMovedErrs = []error{notAMember, notAMember}
					r.b.join = &protocol.TeamJoinResult{TeamRef: teamRef, TeamName: teamName, PrincipalRef: "p-before", Rejoined: true}
				}
				r.b.joinErr = failure
				out, err := r.follow()
				if out.Followed || out.Reason != teammove.ReasonNewBackendFailed || !errors.Is(err, failure) {
					t.Fatalf("a follow whose join was not answered: %+v, %v, want %s", out, err, teammove.ReasonNewBackendFailed)
				}
				if joins := len(r.b.called("team join")); joins != 1 {
					t.Fatalf("the follow presented the ticket %d times, want once", joins)
				}
				if _, err := teamstore.LoadRejoinRefusal(r.configDir, r.newKey); !errors.Is(err, fs.ErrNotExist) {
					t.Fatalf("a join that was not answered is on record as one the backend refused: %v", err)
				}
				if pin := r.currentPin(); pin != r.pin {
					t.Fatalf("the pin moved: %+v", pin)
				}
				binding, berr := teamstore.LoadBinding(r.configDir, r.newKey)
				if held && (berr != nil || binding.PrincipalRef != "p-before") {
					t.Fatalf("the membership the machine held: %+v, %v", binding, berr)
				}
				if !held && berr == nil {
					t.Fatalf("a binding for the new backend was written: %+v", binding)
				}
				if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
					t.Fatalf("the follow left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
				}
				// The backend answers: the next follow presents the ticket again, with
				// no pause, and follows.
				r.b.joinErr = nil
				out, err = r.follow()
				if err != nil || !out.Followed || !out.Joined || len(r.b.called("team join")) != 2 {
					t.Fatalf("the follow after the backend answered: %+v, %v; calls %v", out, err, r.b.verbs())
				}
			})
		}
	}
}

// TestFollowGoesNowhereBetweenTwoBackendsThatNameEachOther: the backend
// the file names itself records that the team moved to the backend the
// checkout is pinned to (work item 341). A session follows neither,
// whether the machine held a membership there or joined with the ticket to
// find out, and the pin stays; a record there that names any other place
// stops nothing.
func TestFollowGoesNowhereBetweenTwoBackendsThatNameEachOther(t *testing.T) {
	t.Parallel()
	back := &protocol.TeamMoveResult{TeamRef: teamRef, Moved: true, Adapter: "supabase", URL: oldURL + "/", RecordedAt: time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)}
	for _, member := range []bool{true, false} {
		r := newRig(t)
		if member {
			r.memberAtTheNewBackend()
		}
		r.b.newRecords, r.b.newMoved = true, back
		out, err := r.follow()
		if err != nil || out.Followed || out.Reason != teammove.ReasonEachOther {
			t.Fatalf("a machine that was a member there (%v): %+v, %v", member, out, err)
		}
		if joins := len(r.b.called("team join")); (joins != 0) == member {
			t.Fatalf("a machine that was a member there (%v) joined %d times", member, joins)
		}
		if pin := r.currentPin(); pin != r.pin {
			t.Fatalf("the pin moved between two backends that name each other: %+v", pin)
		}
	}
	// A record that names a third place is the new backend's own move on,
	// and no reason to stay: a chain is followed one move at a time.
	r := newRig(t)
	r.memberAtTheNewBackend()
	on := *back
	on.Adapter, on.URL = "rest", "https://third.example.test"
	r.b.newRecords, r.b.newMoved = true, &on
	if out, err := r.follow(); err != nil || !out.Followed {
		t.Fatalf("a new backend that records a move to a third: %+v, %v", out, err)
	}
}

// TestSameAddress: what is trivially one server's address, and what is not.
func TestSameAddress(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		a, b string
		same bool
	}{
		{"https://brigade.example.com", "https://brigade.example.com", true},
		{"https://Brigade.Example.com:443/", "https://brigade.example.com", true},
		{"HTTPS://brigade.example.com", "https://brigade.example.com//", true},
		{"http://LOCALHOST:80", "http://localhost", true},
		{"https://brigade.example.com/team/", "https://brigade.example.com/team", true},
		{"https://[::1]:8743", "https://[::1]:8743/", true},
		{"https://brigade.example.com:8743", "https://brigade.example.com", false},
		{"https://brigade.example.com:80", "https://brigade.example.com", false},
		{"http://brigade.example.com", "https://brigade.example.com", false},
		{"https://brigade.example.com/a", "https://brigade.example.com/b", false},
		{"https://brigade.example.com/Team", "https://brigade.example.com/team", false},
		{"http://localhost:8743", "http://127.0.0.1:8743", false},
		{"https://user@brigade.example.com", "https://brigade.example.com", false},
		{"not an address", "not an address", true},
		{"not an address", "another", false},
		{"", "https://brigade.example.com", false},
	} {
		if got := teammove.SameAddress(tc.a, tc.b); got != tc.same || teammove.SameAddress(tc.b, tc.a) != tc.same {
			t.Errorf("SameAddress(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.same)
		}
	}
}

// TestFollowGivesTheNewBackendsAnswerABoundOfItsOwn (work item 341): the
// follow asks the backend the team moved to one question under the lock
// that every follow and every join of the machine takes. A server that
// accepts the connection and says nothing held that lock until the hook's
// own deadline, as the old backend's question can. The question has a
// bound of its own: the follow gives up on it long before its caller would,
// changes nothing, and the next follow asks again. It is not run beside the
// other tests: it measures time.
func TestFollowGivesTheNewBackendsAnswerABoundOfItsOwn(t *testing.T) {
	defer teammove.SetNewBackendWait(200 * time.Millisecond)()
	r := newRig(t)
	r.memberAtTheNewBackend()
	r.b.newRecords, r.b.newMovedHangs = true, true
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Second)
	defer cancel()
	began := time.Now()
	out, _ := teammove.Follow(ctx, teammove.Inputs{
		ConfigDir: r.configDir, Checkout: r.checkout, File: r.file, Pin: &r.pin, Client: r.b.client,
		Now: func() time.Time { return time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC) },
	})
	if took := time.Since(began); out.Followed || out.Reason != teammove.ReasonNewBackendFailed || took > 2*time.Second {
		t.Fatalf("a follow whose new backend says nothing: %+v after %s, want %s well inside the caller's 4s", out, took, teammove.ReasonNewBackendFailed)
	}
	if pin := r.currentPin(); pin != r.pin || len(r.b.called("team join")) != 0 {
		t.Fatalf("the pin moved (%+v) or the machine joined: %v", pin, r.b.verbs())
	}
}
