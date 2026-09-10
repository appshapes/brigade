package supabase

import (
	"encoding/json/v2"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// Schema compatibility (P10-5): the adapter against a backend that lacks
// migration 20260910193200 — the case nobody controls, because members
// update the plugin and administrators apply migrations on their own
// clocks. The fake backend below answers exactly as PostgREST does on
// such a project: an RPC that names an appended parameter is 404
// PGRST202, one that omits them matches the old signature and succeeds.

// heartbeatOK is session_heartbeat's answer.
const heartbeatOK = `{"session_id":"` + testSessionID + `","state":"idle",` +
	`"lease_until":"2026-09-02T12:01:30+00:00","server_time":"2026-09-02T12:00:00+00:00"}`

// legacyBackend records every RPC and answers the session RPCs as a
// backend without the migration while *migrated is false, and as a
// migrated one afterwards.
func legacyBackend(r *rig, migrated *bool) *[]map[string]any {
	seen := &[]map[string]any{}
	r.be.onRPC = func(w http.ResponseWriter, _ *http.Request, fn, _ string, args map[string]any) {
		r.be.mu.Lock()
		copied := make(map[string]any, len(args)+1)
		for k, v := range args {
			copied[k] = v
		}
		copied["__fn"] = fn
		*seen = append(*seen, copied)
		r.be.mu.Unlock()
		if !*migrated && (fn == "register_session" || fn == "session_heartbeat") && namesAny(rpcArgs(args), sessionAppendedParams) {
			// The message is PostgREST's shape with a fixed function name:
			// the adapter keys on the code, and echoing the request's own
			// path back would only teach gosec's taint analysis to object.
			postgrest(w, http.StatusNotFound, "PGRST202",
				"Could not find the function brigade.session_heartbeat(p_activity, p_context_used_tokens, p_model, p_session_id) in the schema cache")
			return
		}
		if fn == "register_session" {
			writeJSON(w, http.StatusOK, registerJSON("main", false))
			return
		}
		writeJSON(w, http.StatusOK, heartbeatOK)
	}
	return seen
}

// names reports whether a recorded call carried any appended parameter.
func names(args map[string]any) bool { return namesAny(rpcArgs(args), sessionAppendedParams) }

// markerPath is the rig profile's backend-legacy.json.
func markerPath(r *rig) string { return filepath.Join(r.profileDir(), legacyMarkerName) }

// writeMarker plants a marker stamped at.
func writeMarker(t *testing.T, r *rig, at time.Time) {
	t.Helper()
	data, err := json.Marshal(legacyMarker{CheckedAt: at, Migration: legacyMigration})
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(markerPath(r), data); err != nil {
		t.Fatal(err)
	}
}

// TestRegistrationWithoutFactsNamesNoAppendedParameter: the harness's
// registration never carries model or context_used_tokens, and the call
// it produces names neither parameter — so it matches the old signature
// on an un-migrated backend with no probe and no fallback at all.
func TestRegistrationWithoutFactsNamesNoAppendedParameter(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	seen := r.be.captureRPC(only(registerJSON("main", false)))
	if got := r.exec(registration("main"), "session", "register"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if len(*seen) != 1 {
		t.Fatalf("%d RPCs, want 1", len(*seen))
	}
	for _, name := range sessionAppendedParams {
		if _, present := (*seen)[0][name]; present {
			t.Errorf("%s was named on a registration that carries no value for it", name)
		}
	}
	absent(t, markerPath(r))
}

// TestLegacyBackendDropsTheFactsAndKeepsTheHeartbeat: against a backend
// without the migration, a heartbeat that carries the two facts is
// refused once, sent again without them and succeeds — the lease is
// renewed, the facts are dropped — with one stderr line naming the
// migration and a marker written; the next heartbeat omits them without
// a probe and says nothing.
func TestLegacyBackendDropsTheFactsAndKeepsTheHeartbeat(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	migrated := false
	seen := legacyBackend(r, &migrated)
	first := r.exec(`{"model":"claude-opus-5[1m]","context_used_tokens":189681}`, "session", "heartbeat", "--session", testSessionID)
	if first.code != 0 {
		t.Fatalf("exit %d: %s", first.code, first.stdout)
	}
	var res protocol.HeartbeatResult
	raw, err := json.Marshal(decode(t, first.stdout)["result"])
	if err != nil {
		t.Fatal(err)
	}
	if err := protocol.Decode(raw, &res); err != nil {
		t.Fatalf("the fallback's result does not validate: %v", err)
	}
	if len(*seen) != 2 || !names((*seen)[0]) || names((*seen)[1]) {
		t.Fatalf("calls = %v, want one probe naming the parameters then one without", *seen)
	}
	if !strings.Contains(first.stderr, "predates a migration") || !strings.Contains(first.stderr, legacyMigration) {
		t.Errorf("stderr does not name the missing migration:\n%s", first.stderr)
	}
	data, err := adapterkit.ReadStrict(markerPath(r))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	var m legacyMarker
	if err := json.Unmarshal(data, &m); err != nil || !m.CheckedAt.Equal(r.now) || m.Migration != legacyMigration {
		t.Fatalf("marker = %s (%v), want checked_at %s and migration %s", data, err, r.now.Format(time.RFC3339), legacyMigration)
	}

	second := r.exec(`{"model":"claude-opus-5[1m]","context_used_tokens":190000}`, "session", "heartbeat", "--session", testSessionID)
	if second.code != 0 {
		t.Fatalf("second heartbeat: exit %d: %s", second.code, second.stdout)
	}
	if len(*seen) != 3 || names((*seen)[2]) {
		t.Fatalf("calls = %v, want the third without the parameters and no probe", *seen)
	}
	if strings.Contains(second.stderr, "predates a migration") {
		t.Errorf("the second heartbeat repeated the warning:\n%s", second.stderr)
	}
}

// TestRegistrationWithFactsFallsBackToo: a registration that does carry
// the facts (another harness's) takes the same path as a heartbeat.
func TestRegistrationWithFactsFallsBackToo(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	migrated := false
	seen := legacyBackend(r, &migrated)
	doc := `{"harness":"other","harness_version":"1","session_name":"main","activity":"busy","inbound":"accept","model":"claude-opus-5[1m]","context_used_tokens":1}`
	if got := r.exec(doc, "session", "register"); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if len(*seen) != 2 || !names((*seen)[0]) || names((*seen)[1]) {
		t.Fatalf("calls = %v, want one probe then one without the parameters", *seen)
	}
}

// TestLegacyMarkerExpiresAndAMigratedBackendClearsIt: a marker older
// than legacyMarkerTTL (or stamped in the future) is no marker — the
// parameters are tried again — and once the backend accepts them the
// marker is removed, so an applied migration is picked up within one TTL.
func TestLegacyMarkerExpiresAndAMigratedBackendClearsIt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		at   time.Time
	}{
		{"expired", time.Time{}}, // set below: now - TTL - 1 s
		{"future", time.Time{}},  // now + 1 h
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newRig(t)
			r.joined()
			at := r.now.Add(-legacyMarkerTTL - time.Second)
			if tc.name == "future" {
				at = r.now.Add(time.Hour)
			}
			writeMarker(t, r, at)
			migrated := true
			seen := legacyBackend(r, &migrated)
			if got := r.exec(`{"model":"claude-opus-5[1m]","context_used_tokens":5}`, "session", "heartbeat", "--session", testSessionID); got.code != 0 {
				t.Fatalf("exit %d: %s", got.code, got.stdout)
			}
			if len(*seen) != 1 || !names((*seen)[0]) {
				t.Fatalf("calls = %v, want exactly one, naming the parameters", *seen)
			}
			absent(t, markerPath(r))
		})
	}
}

// TestFreshMarkerSkipsTheProbe: while the marker is fresh, no call names
// the parameters — one RPC, no PGRST202 round trip — and the marker
// stays.
func TestFreshMarkerSkipsTheProbe(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	writeMarker(t, r, r.now.Add(-legacyMarkerTTL/2))
	migrated := false
	seen := legacyBackend(r, &migrated)
	if got := r.exec(`{"model":"claude-opus-5[1m]","context_used_tokens":5}`, "session", "heartbeat", "--session", testSessionID); got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if len(*seen) != 1 || names((*seen)[0]) {
		t.Fatalf("calls = %v, want exactly one, without the parameters", *seen)
	}
	if _, err := os.Stat(markerPath(r)); err != nil {
		t.Fatalf("the marker was removed by a call that carried nothing: %v", err)
	}
}

// TestDescribeWithholdsTheTwoCapabilitiesWhileLegacy: `describe` drops
// session.model and session.context_used_tokens while the marker is
// fresh and advertises them again once it is stale — from local files
// only, creating and removing nothing (C-01).
func TestDescribeWithholdsTheTwoCapabilitiesWhileLegacy(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	caps := func() []string {
		t.Helper()
		got := r.exec("", "describe")
		if got.code != 0 {
			t.Fatalf("describe: exit %d, %s", got.code, got.stdout)
		}
		var env protocol.Envelope
		if err := protocol.Decode([]byte(got.stdout), &env); err != nil {
			t.Fatal(err)
		}
		var d protocol.DescribeResult
		if err := protocol.Decode(env.Result, &d); err != nil {
			t.Fatal(err)
		}
		return d.Capabilities
	}
	has := func(list []string, capability string) bool {
		for _, c := range list {
			if c == capability {
				return true
			}
		}
		return false
	}
	writeMarker(t, r, r.now)
	fresh := caps()
	for _, capability := range sessionAppendedCapabilities {
		if has(fresh, capability) {
			t.Errorf("describe advertises %s while the backend is known legacy", capability)
		}
	}
	if !has(fresh, "session.inbound") || len(fresh) != len(capabilities())-len(sessionAppendedCapabilities) {
		t.Errorf("describe dropped more than the two capabilities: %v", fresh)
	}
	if _, err := os.Stat(markerPath(r)); err != nil {
		t.Fatalf("describe removed the marker: %v", err)
	}
	writeMarker(t, r, r.now.Add(-legacyMarkerTTL))
	for _, capability := range sessionAppendedCapabilities {
		if !has(caps(), capability) {
			t.Errorf("describe withholds %s on a stale marker", capability)
		}
	}
}
