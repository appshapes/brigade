package supabase

// The fixtures of plan 9.4 that only direct SQL can build: a session
// whose lease has already expired, a message already outside the
// implicit-reply window, and an acknowledged message old enough for
// brigade.gc_expired() to reclaim. Every one of them is a state the
// verbs reach only by waiting — 32 s for a lease, 10 minutes for the
// reply window, 24 h for retention — so the suite backdates the row with
// `database/sql` + jackc/pgx/v5/stdlib against SUPABASE_DB_URL as
// `postgres` and then asserts through the ADAPTER, exactly as 9.4
// prescribes: this file is the only place in the test tree that talks to
// Postgres, and it never asserts on SQL where a verb can answer.
//
// Everything self-skips through testutil.RequireSupabaseDB: no
// .env.test, no SUPABASE_DB_URL or no answering stack and `make test`
// stays Docker-free.

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver "pgx"

	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
)

// liveDB opens the local stack's database as `postgres`, or skips. The
// DSN is never logged: it carries the stack's postgres password.
func liveDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := testutil.RequireSupabaseDB(t)
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Skipf("the local stack's database cannot be opened: %v", redactDSN(err, dsn))
	}
	t.Cleanup(func() { _ = db.Close() })
	// One connection is enough and a pool that outlives the test would
	// hold the stack open across a restart test.
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Skipf("the local stack's database does not answer: %v", redactDSN(err, dsn))
	}
	return db
}

// redactDSN keeps the postgres password out of a skip or failure
// message: pgx puts the whole connection string into some of its errors.
func redactDSN(err error, dsn string) string {
	if err == nil {
		return ""
	}
	return strings.ReplaceAll(err.Error(), dsn, "[dsn redacted]")
}

// execSQL runs one fixture statement and fails the test on error.
func execSQL(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	res, err := db.ExecContext(ctx, query, args...)
	if err != nil {
		t.Fatalf("fixture %q: %v", query, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		t.Fatalf("fixture %q: rows affected: %v", query, err)
	}
	return n
}

// queryRow reads one row into dest.
func queryRow(t *testing.T, db *sql.DB, query string, args []any, dest ...any) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	return db.QueryRowContext(ctx, query, args...).Scan(dest...)
}

// TestIntegrationFixtureExpiredLeaseListsOfflineAndResumes (C-19, 4.5.8,
// the state column of I-04/I-05): a session whose last_seen_at is
// backdated past its own lease drops out of `session list`, appears
// under --include-offline as `offline`, and RESUMES in place — the
// register path's "live" refusal is keyed on the lease, not on
// closed_at. Nothing here waits: the suite would otherwise have to sleep
// a whole 32 s lease to reach this state, which is why 9.4 puts it on
// the SQL fixture.
func TestIntegrationFixtureExpiredLeaseListsOfflineAndResumes(t *testing.T) {
	db := liveDB(t)
	r, _ := liveTeam(t, liveName(t, "p2-11-lease"))
	name := liveName(t, "s")
	result, id := registerLive(t, r, regDoc(name))
	if state, _ := result["state"].(string); state != protocol.SessionStateActive {
		t.Fatalf("a fresh busy session is %v, want active", result["state"])
	}
	if !listHasSession(r.ok("session", "list"), id) {
		t.Fatalf("the fresh session is not listed")
	}

	// One second past the lease: still open, no heartbeat since.
	if n := execSQL(t, db, `update brigade.sessions
	     set last_seen_at = now() - make_interval(secs => lease_seconds + 1)
	   where id = $1`, id); n != 1 {
		t.Fatalf("backdating last_seen_at touched %d rows, want 1", n)
	}

	if listHasSession(r.ok("session", "list"), id) {
		t.Errorf("a session past its lease is still listed without --include-offline (4.5.8)")
	}
	offline := r.ok("session", "list", "--include-offline")
	if !listHasSession(offline, id) {
		t.Fatalf("a session past its lease is absent from --include-offline")
	}
	for _, item := range offline["sessions"].([]any) {
		rec, _ := item.(map[string]any)
		if rec["session_id"] == id && rec["state"] != protocol.SessionStateOffline {
			t.Errorf("state %v, want offline for a session past its lease", rec["state"])
		}
	}

	// The same id resumes: no session_live conflict, resumed true, the id
	// unchanged, and the row is active again.
	resumeDoc := `{"harness":"h","harness_version":"1","session_name":"` + name +
		`","activity":"busy","inbound":"accept","resume":{"session_id":"` + id + `"}}`
	resumed, sameID := registerLive(t, r, resumeDoc)
	if sameID != id {
		t.Errorf("resume: session_id %s, want %s", sameID, id)
	}
	if ok, _ := resumed["resumed"].(bool); !ok {
		t.Errorf("resume of an expired lease: resumed %v, want true", resumed["resumed"])
	}
	if state, _ := resumed["state"].(string); state != protocol.SessionStateActive {
		t.Errorf("resume: state %v, want active", resumed["state"])
	}
	if !listHasSession(r.ok("session", "list"), id) {
		t.Errorf("the resumed session is not listed as live again")
	}
}

// TestIntegrationFixtureImplicitReplyWindow is C-29b's arm the
// conformance suite cannot run (4.5.12): with no `reply_to`, the hop
// chain is computed from the most recent message the recipient sent this
// sender WITHIN the implicit-reply window (600 s). Backdating that
// message past the window starts a fresh chain at 0, and the in-window
// control on the same pair still counts. Only a fixture can show it: the
// suite would have to hold a pair of sessions idle for ten minutes.
func TestIntegrationFixtureImplicitReplyWindow(t *testing.T) {
	db := liveDB(t)
	a, secret := liveTeam(t, liveName(t, "p2-11-hops"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	first := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"one"}`)
	if hop, _ := first["hop_count"].(float64); hop != 0 {
		t.Fatalf("the first message of a chain has hop_count %v, want 0", first["hop_count"])
	}
	firstID, _ := first["message_id"].(string)

	// In-window control: b answers a with no reply_to and inherits the
	// chain (hop 1).
	inWindow := sendLive(t, b, `{"sender_session_id":"`+sb+`","recipient_session_id":"`+sa+`","body":"in window"}`)
	if hop, _ := inWindow["hop_count"].(float64); hop != 1 {
		t.Fatalf("an implicit reply inside the window has hop_count %v, want 1 (4.5.12)", inWindow["hop_count"])
	}

	// Past the window: both messages on this pair move eleven minutes
	// back, so nothing the next send finds is inside the 600 s window.
	if n := execSQL(t, db, `update brigade.messages set created_at = now() - interval '11 minutes'
	   where id = any($1::uuid[])`, []string{firstID, str(t, inWindow, "message_id")}); n != 2 {
		t.Fatalf("backdating the chain touched %d rows, want 2", n)
	}
	fresh := sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"past the window"}`)
	if hop, _ := fresh["hop_count"].(float64); hop != 0 {
		t.Fatalf("a send whose only predecessor is past the implicit-reply window has hop_count %v, want 0 (C-29b)", fresh["hop_count"])
	}
	// And the chain restarts from there: the next implicit reply is 1
	// again, so the window resets the count rather than disabling it.
	next := sendLive(t, b, `{"sender_session_id":"`+sb+`","recipient_session_id":"`+sa+`","body":"a new chain"}`)
	if hop, _ := next["hop_count"].(float64); hop != 1 {
		t.Fatalf("the reply that starts the new chain has hop_count %v, want 1", next["hop_count"])
	}
}

// TestIntegrationFixtureGCReclaimsAckedMessages is D13's 24 h rule
// (I-31's Go-side half; the pgTAP `retention.sql` owns the time-shifted
// assertions): brigade.gc_expired() deletes a message whose injected_at
// is over 24 h old and leaves an unacknowledged one, which the adapter
// then still receives. The function is server-only — revoked from
// public, anon and authenticated — so it is called as `postgres`, which
// is also why no verb can reach this.
func TestIntegrationFixtureGCReclaimsAckedMessages(t *testing.T) {
	db := liveDB(t)
	a, secret := liveTeam(t, liveName(t, "p2-11-gc"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	_, sb := registerLive(t, b, regDoc(liveName(t, "sb")))

	acked := str(t, sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"acked"}`), "message_id")
	kept := str(t, sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+sb+`","body":"unacked"}`), "message_id")
	if got := b.exec(`{"message_ids":["`+acked+`"]}`, "message", "ack", "--session", sb); got.code != 0 {
		t.Fatalf("ack: exit %d, %s", got.code, got.stdout)
	}

	var injected sql.NullTime
	if err := queryRow(t, db, `select injected_at from brigade.messages where id = $1`, []any{acked}, &injected); err != nil {
		t.Fatalf("reading injected_at: %v", err)
	}
	if !injected.Valid {
		t.Fatalf("ack left injected_at null; gc_expired keys retention on it")
	}
	if n := execSQL(t, db, `update brigade.messages set injected_at = now() - interval '25 hours' where id = $1`, acked); n != 1 {
		t.Fatalf("backdating injected_at touched %d rows, want 1", n)
	}
	execSQL(t, db, `select brigade.gc_expired()`)

	var gone, survived int
	if err := queryRow(t, db, `select count(*) from brigade.messages where id = $1`, []any{acked}, &gone); err != nil {
		t.Fatal(err)
	}
	if gone != 0 {
		t.Errorf("gc_expired left an acknowledged message injected 25 h ago (D13: 24 h)")
	}
	if err := queryRow(t, db, `select count(*) from brigade.messages where id = $1`, []any{kept}, &survived); err != nil {
		t.Fatal(err)
	}
	if survived != 1 {
		t.Errorf("gc_expired deleted an unacknowledged message of the same run (D13: 7 days)")
	}
	// The verb agrees with the table: the inbox still holds exactly the
	// message that was never acknowledged.
	inbox := receiveLive(t, b, sb)
	if len(inbox) != 1 || inbox[0]["message_id"] != kept {
		t.Errorf("inbox after gc = %v, want only %s", inbox, kept)
	}
}

// TestIntegrationPrincipalsAreDistinctAnonymousUsers is I-23 through the
// row auth.users actually holds: one profile keeps ONE principal across
// runs (two `profile status` runs answer the same principal_ref), two
// profiles are two principals, and each is an anonymous user with the
// `authenticated` role — the claims of 5.1 as the server stored them,
// not only as the JWT asserts them. Reading auth.users is the point:
// nothing on the wire can show that the two refs are two rows.
func TestIntegrationPrincipalsAreDistinctAnonymousUsers(t *testing.T) {
	db := liveDB(t)
	first := liveRig(t)
	c := first.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatalf("sign-up: %v", err)
	}
	one := str(t, first.ok("profile", "status"), "principal_ref")
	if again := str(t, first.ok("profile", "status"), "principal_ref"); again != one {
		t.Errorf("one profile answered two principal_refs across runs: %s and %s (I-23)", one, again)
	}

	second := liveRig(t)
	c2 := second.command("session", "list")
	if err := c2.ensureIdentity(t.Context()); err != nil {
		t.Fatalf("sign-up: %v", err)
	}
	other := str(t, second.ok("profile", "status"), "principal_ref")
	if other == one {
		t.Fatalf("two profiles share one principal_ref %s (I-23: no shared credential)", one)
	}

	for _, ref := range []string{one, other} {
		var anonymous bool
		var role, aud string
		if err := queryRow(t, db, `select is_anonymous, role, aud from auth.users where id = $1`, []any{ref}, &anonymous, &role, &aud); err != nil {
			t.Fatalf("auth.users row for %s: %v", ref, err)
		}
		if !anonymous || role != "authenticated" || aud != "authenticated" {
			t.Errorf("auth.users %s: is_anonymous %v, role %q, aud %q; want an anonymous authenticated principal (5.1)", ref, anonymous, role, aud)
		}
	}
}
