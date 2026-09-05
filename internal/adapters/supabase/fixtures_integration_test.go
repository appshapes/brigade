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
// Everything self-skips through testutil.RequireSupabaseDB: first
// without the BRIGADE_TEST_LIVE opt-in (testutil.LiveTestVar), then
// without .env.test, SUPABASE_DB_URL or an answering stack — so `make
// test` stays Docker-free and stack-free.

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

// TestIntegrationRetentionResumeAfterThreeAndEightDays is P5-3's
// end-to-end retention check (I-31 through the adapter; the acceptance
// sentence of the plan row): a member offline for 3 days resumes its
// session and still receives what was sent to it; after 8 days both the
// session and its message are gone and the adapter answers not_found
// (exit 6) to the resume and to the receive. The 3-day arm is the
// "adapter-specific retention-sweep test" the spec asks for in place of a
// conformance case for B-10 (docs/protocol-v1.md:923): an unacknowledged
// message survives a gc run inside retention.unacked_message_seconds. No
// sleeps: the rows are backdated as postgres, exactly as retention.sql
// does, and gc_expired() is run once per arm. The SQL side of the 8-day
// arm is asserted too, so a green arm cannot mean the adapter answered
// not_found for some other reason.
func TestIntegrationRetentionResumeAfterThreeAndEightDays(t *testing.T) {
	db := liveDB(t)
	a, secret := liveTeam(t, liveName(t, "p5-3-retention"))
	b := liveJoin(t, secret)
	_, sa := registerLive(t, a, regDoc(liveName(t, "sa")))
	name3, name8 := liveName(t, "s3"), liveName(t, "s8")
	_, s3 := registerLive(t, b, regDoc(name3))
	_, s8 := registerLive(t, b, regDoc(name8))
	m3 := str(t, sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+s3+`","body":"sent three days before the resume"}`), "message_id")
	m8 := str(t, sendLive(t, a, `{"sender_session_id":"`+sa+`","recipient_session_id":"`+s8+`","body":"sent eight days before the resume"}`), "message_id")

	resumeDoc := func(name, id string) string {
		return `{"harness":"h","harness_version":"1","session_name":"` + name +
			`","activity":"busy","inbound":"accept","resume":{"session_id":"` + id + `"}}`
	}
	// offline moves one session's last_seen_at and its message's created_at
	// the given number of days into the past: the session's lease expired
	// that long ago and the message has waited unacknowledged since.
	offline := func(session, message string, days int) {
		t.Helper()
		if n := execSQL(t, db, `update brigade.sessions set last_seen_at = now() - make_interval(days => $2) where id = $1`, session, days); n != 1 {
			t.Fatalf("backdating session %s by %d days touched %d rows, want 1", session, days, n)
		}
		if n := execSQL(t, db, `update brigade.messages set created_at = now() - make_interval(days => $2) where id = $1`, message, days); n != 1 {
			t.Fatalf("backdating message %s by %d days touched %d rows, want 1", message, days, n)
		}
	}

	// Arm A: 3 days offline, then a gc run. The session resumes in place
	// and the inbox holds exactly the message that waited.
	offline(s3, m3, 3)
	execSQL(t, db, `select brigade.gc_expired()`)
	resumed, sameID := registerLive(t, b, resumeDoc(name3, s3))
	if sameID != s3 {
		t.Errorf("3 days offline: resume answered session_id %s, want %s", sameID, s3)
	}
	if ok, _ := resumed["resumed"].(bool); !ok {
		t.Errorf("3 days offline: resumed %v, want true (D13: sessions are kept 7 days after lease expiry)", resumed["resumed"])
	}
	inbox := receiveLive(t, b, s3)
	if len(inbox) != 1 || inbox[0]["message_id"] != m3 {
		t.Errorf("3 days offline: inbox after gc = %v, want exactly %s (B-10: an unacknowledged message is retained %d s at least)",
			inbox, m3, protocol.RetentionUnackedMessageSeconds)
	}

	// Arm B: 8 days offline, then a gc run. The row and its message are
	// gone, and the adapter says so with the uniform not_found on both
	// verbs a returning member would try.
	offline(s8, m8, 8)
	execSQL(t, db, `select brigade.gc_expired()`)
	var sessions, messages int
	if err := queryRow(t, db, `select count(*) from brigade.sessions where id = $1`, []any{s8}, &sessions); err != nil {
		t.Fatal(err)
	}
	if err := queryRow(t, db, `select count(*) from brigade.messages where id = $1`, []any{m8}, &messages); err != nil {
		t.Fatal(err)
	}
	if sessions != 0 || messages != 0 {
		t.Fatalf("8 days offline: %d session row(s) and %d message row(s) survived gc_expired(), want 0 and 0 (D13: 7 days)", sessions, messages)
	}
	b.fails("not_found", 6, resumeDoc(name8, s8), "session", "register")
	b.fails("not_found", 6, "", "message", "receive", "--session", s8)
}

// TestIntegrationGCReclaimsAnonymousUsers is I-24 with the keep-alive's
// own input shape (P5-0 adds one anonymous auth.users row per day on the
// hosted project, and Supabase never cleans them up): a principal minted
// by the adapter's sign-up — the same POST /auth/v1/signup body
// scripts/ci/keepalive.sh posts, drift-joined to gotrue.go — that never
// joins a team is reaped by brigade.gc_anonymous_users() once it is older
// than 7 days, with its auth.sessions row and refresh tokens; a creator
// of the same age, holding a membership row, survives. Then through the
// adapter: the reaped principal's refresh family is gone (I-34's shape),
// and its next command that needs a refresh is the terminal credential
// path, exit 4, with session.json deleted. Its still-unexpired access
// token is recorded, not asserted: deleting a user revokes the refresh
// family, not a live JWT.
//
// Blast radius: the function is global, so on a shared stack it reaps
// every developer's stale un-joined principals alongside. Only ids this
// test created are asserted on — never a global count of auth.users.
func TestIntegrationGCReclaimsAnonymousUsers(t *testing.T) {
	db := liveDB(t)
	alone := liveRig(t)
	c := alone.command("session", "list")
	if err := c.ensureIdentity(t.Context()); err != nil {
		t.Fatalf("sign-up: %v", err)
	}
	saved := alone.readSession()
	if saved == nil || !saved.usable() {
		t.Fatalf("sign-up left no usable credential: %+v", saved)
	}
	aloneRef := saved.principalRef()
	creator, secret := liveTeam(t, liveName(t, "p5-3-anon"))
	creatorRef := creator.readSession().principalRef()
	if !validUUID(aloneRef) || !validUUID(creatorRef) || aloneRef == creatorRef {
		t.Fatalf("principal refs %q and %q", aloneRef, creatorRef)
	}

	// Baseline: both are anonymous rows, each with the auth.sessions row
	// its sign-up created, and only the creator holds a membership.
	for _, ref := range []string{aloneRef, creatorRef} {
		var anonymous bool
		var sessions int
		if err := queryRow(t, db, `select u.is_anonymous, (select count(*) from auth.sessions s where s.user_id = u.id) from auth.users u where u.id = $1`, []any{ref}, &anonymous, &sessions); err != nil {
			t.Fatalf("auth.users row for %s: %v", ref, err)
		}
		if !anonymous || sessions < 1 {
			t.Fatalf("principal %s: is_anonymous %v with %d auth.sessions row(s); want an anonymous principal with a session", ref, anonymous, sessions)
		}
	}
	var aloneMemberships, creatorMemberships int
	if err := queryRow(t, db, `select (select count(*) from brigade.memberships where user_id = $1), (select count(*) from brigade.memberships where user_id = $2)`, []any{aloneRef, creatorRef}, &aloneMemberships, &creatorMemberships); err != nil {
		t.Fatal(err)
	}
	if aloneMemberships != 0 || creatorMemberships != 1 {
		t.Fatalf("membership rows: un-joined %d, creator %d; want 0 and 1", aloneMemberships, creatorMemberships)
	}

	// Both past the window; only the un-joined one qualifies.
	if n := execSQL(t, db, `update auth.users set created_at = now() - interval '8 days' where id = any($1::uuid[])`, []string{aloneRef, creatorRef}); n != 2 {
		t.Fatalf("backdating created_at touched %d rows, want 2", n)
	}
	var deleted int
	if err := queryRow(t, db, `select brigade.gc_anonymous_users()`, nil, &deleted); err != nil {
		t.Fatalf("gc_anonymous_users(): %v", err)
	}
	if deleted == -1 {
		t.Fatalf("gc_anonymous_users() answered -1: its exception handler fired and nothing was deleted (a restricted delete reached a creator, or a defect)")
	}
	if deleted < 1 {
		t.Fatalf("gc_anonymous_users() deleted %d user(s), want at least the un-joined principal", deleted)
	}
	t.Logf("gc_anonymous_users() deleted %d user(s) (this run's un-joined principal, plus any other stale ones on a shared stack)", deleted)

	var users, sessions, tokens, creatorRows int
	if err := queryRow(t, db, `select (select count(*) from auth.users where id = $1), (select count(*) from auth.sessions where user_id = $1), (select count(*) from auth.refresh_tokens where user_id = $1::text), (select count(*) from auth.users where id = $2)`,
		[]any{aloneRef, creatorRef}, &users, &sessions, &tokens, &creatorRows); err != nil {
		t.Fatal(err)
	}
	if users != 0 {
		t.Errorf("the 8-day-old un-joined principal survived gc_anonymous_users() (D13: 7 days, no membership row)")
	}
	if sessions != 0 {
		t.Errorf("%d auth.sessions row(s) of the reaped principal survived: the FK cascade did not run", sessions)
	}
	if tokens != 0 {
		t.Errorf("%d auth.refresh_tokens row(s) of the reaped principal survived: auth.refresh_tokens has no FK to auth.users and must cascade through auth.sessions", tokens)
	}
	if creatorRows != 1 {
		t.Errorf("the 8-day-old CREATOR was deleted: it holds a membership row and is created_by of a team, and teams.created_by is on delete restrict (5.10)")
	}

	// I-34's shape: the family is gone at the backend.
	_, ae, err := c.client.refresh(t.Context(), saved.RefreshToken)
	if err != nil {
		t.Fatalf("refresh of the reaped principal's token: %v", err)
	}
	if ae == nil {
		t.Fatalf("the reaped principal's refresh token still rotates: deleting auth.users did not revoke its family")
	}
	if ae.code != authRefreshTokenNotFound || !ae.terminal() {
		t.Errorf("refresh after the user delete: code %q terminal %v, want %q and terminal", ae.code, ae.terminal(), authRefreshTokenNotFound)
	}
	// Recorded, not asserted: PostgREST checks the JWT's signature and
	// expiry, not auth.users, so the access token issued before the delete
	// still passes until it expires (an hour on the local stack).
	if resp, rerr := c.client.callRPC(t.Context(), saved.AccessToken, "my_team_ids", nil); rerr == nil {
		t.Logf("the reaped principal's unexpired access token still answers HTTP %d from PostgREST (recorded, not asserted: a user delete revokes the refresh family, not a live JWT)", resp.status)
	}

	// Through the adapter: bound to a team locally so the command reaches
	// the credential rather than answering `config`, with the clock past
	// the JWT's expiry so it must refresh — the terminal path, exit 4, and
	// session.json is gone.
	alone.bindTeam(teamRefOf(t, secret), "p5-3")
	alone.now = time.Now().Add(2 * time.Hour)
	got := alone.fails("unauthenticated", 4, "", "session", "list")
	if details(t, got.stdout)["reason"] != reasonCredentialRevoked {
		t.Errorf("the reaped principal's next command: details %v, want reason %s", details(t, got.stdout), reasonCredentialRevoked)
	}
	if alone.readSession() != nil {
		t.Errorf("session.json survived the terminal refresh of a reaped principal")
	}
}

// TestIntegrationDescribeRetentionMatchesTheDeployedFunctions is the
// live half of the retention drift join (brief 3.5).
// TestDescribeRetentionMatchesTheMigration proves the REPOSITORY is
// consistent; only reading the function bodies back out of the database
// proves the DEPLOYED gc_expired() and gc_anonymous_users() enforce what
// describe advertises to a harness — the point on a hosted project that
// may be a migration behind. It also pins that the deployed gc_expired()
// still ends in the anonymous-user call: without it the heartbeat path
// would never reap a user and correctness would depend on nothing at all.
func TestIntegrationDescribeRetentionMatchesTheDeployedFunctions(t *testing.T) {
	db := liveDB(t)
	advertised := describeRetention(t)
	var gcExpired, gcAnonymous string
	if err := queryRow(t, db, `select pg_get_functiondef('brigade.gc_expired'::regproc)`, nil, &gcExpired); err != nil {
		t.Fatalf("reading the deployed gc_expired(): %v", err)
	}
	if err := queryRow(t, db, `select pg_get_functiondef('brigade.gc_anonymous_users'::regproc)`, nil, &gcAnonymous); err != nil {
		t.Fatalf("reading the deployed gc_anonymous_users() (is the anonymous_user_gc migration applied?): %v", err)
	}
	if enforced := retentionIn(t, "the deployed brigade.gc_expired()", gcExpired); enforced != advertised {
		t.Errorf("the deployed gc_expired() enforces %+v but describe advertises %+v: the stack is a migration behind, or the two moved apart", enforced, advertised)
	}
	if days := sourceInterval(t, "the deployed brigade.gc_anonymous_users()", gcAnonymous, anonymousUserDaysAnchor); days != anonymousUserDays {
		t.Errorf("the deployed gc_anonymous_users() reaps after %d days, want %d (D13)", days, anonymousUserDays)
	}
	const call = "select brigade.gc_anonymous_users();"
	if idx := strings.LastIndex(gcExpired, call); idx < 0 || strings.Contains(gcExpired[idx+len(call):], "delete from") {
		t.Errorf("the deployed gc_expired() does not call gc_anonymous_users() as its LAST statement (5.8's chain: the abandoned-team delete must run first)")
	}
}
