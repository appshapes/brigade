package fs

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// newTestStore opens a store on a root of the test's own, with a clock the
// test controls. It is the seam the rate-limit and sweep tests use: the
// six budgets of 4.5.12 need hundreds of messages, and driving them
// through Run would test the clock, not the rule.
func newTestStore(t *testing.T, now *time.Time) *store {
	t.Helper()
	s, err := openStore(filepath.Join(t.TempDir(), "root"), func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.close)
	return s
}

// seed writes one message straight into a recipient's inbox. It bypasses
// the send path on purpose: the rules under test are counting rules.
func seed(t *testing.T, s *store, team, recipient, senderSession, senderPrincipal string, created time.Time, n int) {
	t.Helper()
	dir := s.inboxDir(team, recipient)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range n {
		id := senderSession + "x" + strconv.Itoa(i) + strconv.FormatInt(created.UnixNano(), 10)
		message := storedMessage{MessageEnvelope: protocol.MessageEnvelope{
			ProtocolVersion: protocol.ProtocolVersion, Kind: protocol.KindText,
			MessageID: id, TeamRef: team,
			Sender: protocol.Sender{
				PrincipalRef: senderPrincipal, SessionID: senderSession, SessionName: "seed",
			},
			RecipientSessionID: recipient, Body: "seeded", HopCount: 0,
			CreatedAt: created, DeliveryState: protocol.DeliveryStateAccepted,
		}}
		data, err := json.Marshal(&message)
		if err != nil {
			t.Fatal(err)
		}
		name := filepath.Join(dir, strconv.FormatInt(created.UnixNano()+int64(i), 10)+"."+id+jsonExt)
		if err := os.WriteFile(name, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// TestSendBudgetsInTheirFixedOrder covers C-28 and 4.5.12: all six
// budgets, each with its own reason token, checked in the order the spec
// fixes — the two session windows, the two principal windows, then the
// per-pair cap BEFORE the recipient-wide one.
func TestSendBudgetsInTheirFixedOrder(t *testing.T) {
	t.Parallel()
	limits := protocol.DefaultLimits()
	const team, recipient, principal = "t1", "recipient", "p1"
	sender := &sessionFile{SessionID: "s1", PrincipalRef: principal}

	for _, tc := range []struct {
		name       string
		reason     string
		windowHint bool
		seedInto   func(t *testing.T, s *store, now time.Time)
	}{
		{
			name: "session per minute", reason: reasonSendPerMinute, windowHint: true,
			seedInto: func(t *testing.T, s *store, now time.Time) {
				t.Helper()
				seed(t, s, team, recipient, "s1", principal, now.Add(-time.Second), limits.SendRate.PerMinute)
			},
		},
		{
			name: "session per hour", reason: reasonSendPerHour, windowHint: true,
			seedInto: func(t *testing.T, s *store, now time.Time) {
				t.Helper()
				seed(t, s, team, "old", "s1", principal, now.Add(-30*time.Minute), limits.SendRate.PerHour)
			},
		},
		{
			name: "principal per minute", reason: reasonPrincipalPerMinute, windowHint: true,
			seedInto: func(t *testing.T, s *store, now time.Time) {
				t.Helper()
				seed(t, s, team, "other", "s2", principal, now.Add(-time.Second),
					limits.PrincipalSendRate.PerMinute)
			},
		},
		{
			name: "principal per hour", reason: reasonPrincipalPerHour, windowHint: true,
			seedInto: func(t *testing.T, s *store, now time.Time) {
				t.Helper()
				seed(t, s, team, "other", "s2", principal, now.Add(-30*time.Minute),
					limits.PrincipalSendRate.PerHour)
			},
		},
		{
			name: "per sender-recipient pair", reason: reasonSenderQuotaForRecipient,
			seedInto: func(t *testing.T, s *store, now time.Time) {
				t.Helper()
				seed(t, s, team, recipient, "s1", principal, now.Add(-2*time.Hour),
					limits.MaxUnackedPerSenderRecipient)
			},
		},
		{
			name: "recipient inbox", reason: reasonRecipientInboxFull,
			seedInto: func(t *testing.T, s *store, now time.Time) {
				t.Helper()
				for i := range 6 {
					seed(t, s, team, recipient, "other"+strconv.Itoa(i), "p"+strconv.Itoa(i),
						now.Add(-2*time.Hour), 10)
				}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := fixedStart
			s := newTestStore(t, &now)
			tc.seedInto(t, s, now)
			err := s.checkSendRate(team, sender, recipient)
			var perr *protocol.Error
			if !asError(err, &perr) {
				t.Fatalf("checkSendRate = %v, want a rate_limited error", err)
			}
			if perr.Code != protocol.CodeRateLimited || perr.Details["reason"] != tc.reason {
				t.Fatalf("code %q reason %q, want rate_limited/%s", perr.Code, perr.Details["reason"], tc.reason)
			}
			if perr.RetryAfterMS <= 0 {
				t.Fatalf("retry_after_ms = %d, want > 0", perr.RetryAfterMS)
			}
			if tc.windowHint && perr.RetryAfterMS > int(time.Hour/time.Millisecond) {
				t.Fatalf("retry_after_ms = %d, longer than the widest window", perr.RetryAfterMS)
			}
		})
	}
}

// TestBudgetsAreCheckedInOrder: when the session window and the per-pair
// cap would both trip, the window is reported. The order is normative
// (4.5.12), not an implementation detail.
func TestBudgetsAreCheckedInOrder(t *testing.T) {
	t.Parallel()
	now := fixedStart
	s := newTestStore(t, &now)
	limits := protocol.DefaultLimits()
	seed(t, s, "t1", "recipient", "s1", "p1", now.Add(-time.Second), limits.SendRate.PerMinute)
	err := s.checkSendRate("t1", &sessionFile{SessionID: "s1", PrincipalRef: "p1"}, "recipient")
	var perr *protocol.Error
	if !asError(err, &perr) || perr.Details["reason"] != reasonSendPerMinute {
		t.Fatalf("err = %v, want send_per_minute first", err)
	}
}

// TestPerPairCapIsCheckedBeforeTheRecipientCap pins the ORDER 4.5.12
// fixes between the two unacknowledged caps: when a recipient's inbox is
// full AND one sender already holds its whole per-pair quota, the answer
// is sender_quota_for_recipient, because that cap is "checked before the
// recipient-wide cap so that one sender cannot exhaust a recipient's
// inbox for everyone else". Every other test in this file trips exactly
// one of the two, so without this one the two checks can be swapped and
// the whole suite stays green (C-28).
func TestPerPairCapIsCheckedBeforeTheRecipientCap(t *testing.T) {
	t.Parallel()
	now := fixedStart
	s := newTestStore(t, &now)
	limits := protocol.DefaultLimits()
	// The sender holds exactly its per-pair quota …
	seed(t, s, "t1", "recipient", "s1", "p1", now.Add(-2*time.Hour), limits.MaxUnackedPerSenderRecipient)
	// … and other senders fill the recipient's inbox to its own cap.
	seed(t, s, "t1", "recipient", "s2", "p2", now.Add(-2*time.Hour),
		limits.MaxUnackedPerRecipient-limits.MaxUnackedPerSenderRecipient)
	err := s.checkSendRate("t1", &sessionFile{SessionID: "s1", PrincipalRef: "p1"}, "recipient")
	var perr *protocol.Error
	if !asError(err, &perr) {
		t.Fatalf("checkSendRate = %v, want a rate_limited error", err)
	}
	if perr.Details["reason"] != reasonSenderQuotaForRecipient {
		t.Fatalf("reason = %q, want %q: the per-pair cap is checked BEFORE the recipient-wide one (4.5.12)",
			perr.Details["reason"], reasonSenderQuotaForRecipient)
	}
}

// TestRateLimitRetryHintIsAlwaysPositive: C-28 requires retry_after_ms >
// 0, and the time left in a window rounds down to zero milliseconds when
// the oldest counted message is less than a millisecond from leaving it.
func TestRateLimitRetryHintIsAlwaysPositive(t *testing.T) {
	t.Parallel()
	now := fixedStart
	s := newTestStore(t, &now)
	limits := protocol.DefaultLimits()
	seed(t, s, "t1", "recipient", "s1", "p1",
		now.Add(-time.Minute+100*time.Nanosecond), limits.SendRate.PerMinute)
	err := s.checkSendRate("t1", &sessionFile{SessionID: "s1", PrincipalRef: "p1"}, "recipient")
	var perr *protocol.Error
	if !asError(err, &perr) || perr.Details["reason"] != reasonSendPerMinute {
		t.Fatalf("checkSendRate = %v, want send_per_minute", err)
	}
	if perr.RetryAfterMS < 1 {
		t.Fatalf("retry_after_ms = %d; C-28 requires a positive hint", perr.RetryAfterMS)
	}
}

// TestSendBudgetsAllowTheLastPermittedMessage: one below every cap passes,
// so the checks cannot be off by one in the refusing direction.
func TestSendBudgetsAllowTheLastPermittedMessage(t *testing.T) {
	t.Parallel()
	now := fixedStart
	s := newTestStore(t, &now)
	limits := protocol.DefaultLimits()
	// Another team's traffic never counts: every budget is scoped to the
	// profile's team (4.5.6).
	seed(t, s, "t2", "recipient", "s1", "p1", now.Add(-time.Second), limits.SendRate.PerHour)
	// One below the session's minute budget, and one below the per-pair
	// cap, in the same call.
	seed(t, s, "t1", "elsewhere", "s1", "p1", now.Add(-time.Second),
		limits.SendRate.PerMinute-limits.MaxUnackedPerSenderRecipient)
	seed(t, s, "t1", "recipient", "s1", "p1", now.Add(-time.Second),
		limits.MaxUnackedPerSenderRecipient-1)
	if err := s.checkSendRate("t1", &sessionFile{SessionID: "s1", PrincipalRef: "p1"}, "recipient"); err != nil {
		t.Fatalf("the last permitted send was refused: %v", err)
	}
}

// TestSequenceNumbersAreMonotonic: lexical order equals send order, and an
// acknowledgement — which moves a file out of the inbox — never lets a
// later message reuse an earlier number.
func TestSequenceNumbersAreMonotonic(t *testing.T) {
	t.Parallel()
	now := fixedStart
	s := newTestStore(t, &now)
	previous := ""
	for range 5 {
		seq, err := s.nextSeq("t1", "r1")
		if err != nil {
			t.Fatal(err)
		}
		if len(seq) != seqWidth {
			t.Fatalf("seq %q is %d digits, want %d", seq, len(seq), seqWidth)
		}
		if seq <= previous {
			t.Fatalf("seq %q is not after %q", seq, previous)
		}
		previous = seq
		if err := writeJSON(filepath.Join(s.inboxDir("t1", "r1"), seq+".id"+seq+jsonExt),
			&storedMessage{}); err != nil {
			t.Fatal(err)
		}
	}
	// The clock going backwards must not reuse a number.
	now = fixedStart.Add(-time.Hour)
	seq, err := s.nextSeq("t1", "r1")
	if err != nil {
		t.Fatal(err)
	}
	if seq <= previous {
		t.Fatalf("a backwards clock reused a sequence number: %q after %q", seq, previous)
	}
}

// TestSafeRefRejectsPathTraversal: identifiers are opaque (4.8) and become
// path components, so anything that could escape the store is refused and
// simply never found.
func TestSafeRefRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	for _, ref := range []string{"", ".", "..", "../escape", "a/b", "a\x00b", "a.b", string(make([]byte, 65))} {
		if safeRef(ref) {
			t.Fatalf("safeRef(%q) = true", ref)
		}
	}
	for _, ref := range []string{"a", "0123456789abcdef", "with-dash", "with_underscore"} {
		if !safeRef(ref) {
			t.Fatalf("safeRef(%q) = false", ref)
		}
	}
}

// TestSessionStateIsComputedAtReadTime covers 4.5.8: closed or expired is
// offline, a valid lease with activity busy is active, everything else is
// idle. Nothing ever writes `state`.
func TestSessionStateIsComputedAtReadTime(t *testing.T) {
	t.Parallel()
	now := fixedStart
	closed := now.Add(-time.Minute)
	for name, tc := range map[string]struct {
		file *sessionFile
		want string
	}{
		"busy with a valid lease": {
			file: &sessionFile{Activity: protocol.ActivityBusy, LeaseUntil: now.Add(time.Minute)},
			want: protocol.SessionStateActive,
		},
		"idle with a valid lease": {
			file: &sessionFile{Activity: protocol.ActivityIdle, LeaseUntil: now.Add(time.Minute)},
			want: protocol.SessionStateIdle,
		},
		"expired lease": {
			file: &sessionFile{Activity: protocol.ActivityBusy, LeaseUntil: now.Add(-time.Second)},
			want: protocol.SessionStateOffline,
		},
		"closed with a valid lease": {
			file: &sessionFile{Activity: protocol.ActivityBusy, LeaseUntil: now.Add(time.Minute), ClosedAt: &closed},
			want: protocol.SessionStateOffline,
		},
	} {
		if got := sessionState(tc.file, now); got != tc.want {
			t.Fatalf("%s: state = %q, want %q", name, got, tc.want)
		}
	}
}

// TestRetentionSweep covers 4.5.9: an acknowledged message goes after
// retention.acked_message_seconds, and a session offline for longer than
// retention.closed_session_seconds goes with its inbox, its acked messages
// and its idempotency records.
func TestRetentionSweep(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	teamRef, secret := r.team("alice", "ops")
	r.join("bob", secret)
	alice := r.register("alice", "alice-1")
	bob := r.register("bob", "bob-1")
	acked := str(t, r.send("alice", alice, bob, "will be acked"), "message_id")
	r.ok(`{"message_ids":["`+acked+`"]}`, "--profile", "bob", "message", "ack", "--session", bob)
	r.send("alice", alice, bob, "will linger")

	retention := protocol.DefaultRetention()
	ackedDir := filepath.Join(r.root, teamsDirName, teamRef, ackedDirName, bob)
	inboxPath := filepath.Join(r.root, teamsDirName, teamRef, inboxDirName, bob)
	if names, _ := listNames(ackedDir); len(names) != 1 {
		t.Fatalf("the acknowledged message was not stored: %v", names)
	}

	// Past the acked floor only the acknowledged message goes.
	r.now = r.now.Add(time.Duration(retention.AckedMessageSeconds+1) * time.Second)
	r.ok("", "--profile", "alice", "session", "list", "--include-offline")
	if names, _ := listNames(ackedDir); len(names) != 0 {
		t.Fatalf("an acknowledged message survived its floor: %v", names)
	}
	if names, _ := listNames(inboxPath); len(names) != 1 {
		t.Fatalf("an unacknowledged message was swept early: %v", names)
	}

	// Past the closed-session floor the sessions and their boxes go.
	r.now = r.now.Add(time.Duration(retention.ClosedSessionSeconds+1) * time.Second)
	r.ok("", "--profile", "alice", "session", "list", "--include-offline")
	absent(t, inboxPath)
	absent(t, filepath.Join(r.root, teamsDirName, teamRef, sessionsDirName, bob+jsonExt))
	if got := mustSessions(t, r.ok("", "--profile", "alice", "session", "list", "--include-offline")); len(got) != 0 {
		t.Fatalf("sessions survived the sweep: %v", got)
	}
}

// TestSweepDoesNothingWithoutTeams: the sweep must be a no-op on a store
// that has never held a team, so a first command cannot fail on an empty
// tree (4.5.9).
func TestSweepDoesNothingWithoutTeams(t *testing.T) {
	t.Parallel()
	now := fixedStart
	s := newTestStore(t, &now)
	if err := s.sweep(protocol.DefaultRetention()); err != nil {
		t.Fatalf("sweep on an empty store: %v", err)
	}
}
