package teammove_test

import (
	"context"
	json "encoding/json/v2"
	"errors"
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
	if c.adapter != "rest" && !c.b.noRecord {
		capabilities = append(capabilities, teammove.Capability)
	}
	return &protocol.DescribeResult{Capabilities: capabilities}, nil
}

func (c fakeCaller) Call(_ context.Context, group, verb string, flags []string, stdin any) (*protocol.Envelope, error) {
	b := c.b
	b.calls = append(b.calls, call{adapter: c.adapter, profile: c.profile, verb: group + " " + verb, flags: slices.Clone(flags), stdin: stdin})
	var result any
	switch group + " " + verb {
	case "profile init":
		result = map[string]string{"state": "configured"}
	case "team moved":
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
}

func newRig(t *testing.T) *rig {
	t.Helper()
	r := &rig{
		t: t, configDir: filepath.Join(t.TempDir(), "config"), checkout: filepath.Join(t.TempDir(), "checkout"),
		oldKey: teamstore.Key("supabase", oldURL, teamRef), newKey: teamstore.Key("rest", newURL, teamRef),
		pin:  teamstore.Pin{Adapter: "supabase", URL: oldURL, PublishableKey: oldKeyP, TeamRef: teamRef, ConsentedAt: time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC)},
		file: &teamfile.File{Version: 1, Adapter: "rest", URL: newURL, Certificate: newPin, TeamRef: teamRef, TeamName: teamName},
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
		Now: func() time.Time { return time.Date(2026, 11, 14, 13, 0, 0, 0, time.UTC) },
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
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved", "rest:team join"}) {
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
		}, teammove.ReasonOldBackendFailed, true},
		{"the old backend answers a shape that is not the protocol's", func(r *rig) { r.b.moved = &protocol.TeamMoveResult{Moved: true} }, teammove.ReasonOldBackendFailed, true},
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
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved"}) {
		t.Fatalf("adapter calls of the second checkout: %v", got)
	}
	if pin := r.currentPin(); !pin.Matches(r.file) || pin.MovedFrom != oldURL {
		t.Fatalf("the second checkout's pin: %+v", pin)
	}
	if left := r.leftBehind(); len(left) != 0 || r.ticketOnDisk() {
		t.Fatalf("the second follow left %v in the store, ticket on disk %v", left, r.ticketOnDisk())
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
	if got := r.b.verbs(); !slices.Equal(got, []string{"supabase:describe", "supabase:team moved", "rest:profile init", "rest:team join"}) {
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
