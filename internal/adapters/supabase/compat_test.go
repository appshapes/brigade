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
// an appending migration — the case nobody controls, because members
// update the plugin and administrators apply migrations on their own
// clocks. The fake backend below answers exactly as PostgREST does on
// such a project: an RPC that names a parameter a migration the project
// does not have appended is 404 PGRST202, one that omits them matches the
// older signature and succeeds. Its `level` is the number of leading
// sessionAppendedMigrations the project has, so 0 is a project behind on
// all of them, len() one that is fully migrated, and 1 the project every
// deployed one becomes when this release lands: 20260910193200 applied,
// 20260917170000 not yet.

// heartbeatOK is session_heartbeat's answer.
const heartbeatOK = `{"session_id":"` + testSessionID + `","state":"idle",` +
	`"lease_until":"2026-09-02T12:01:30+00:00","server_time":"2026-09-02T12:00:00+00:00"}`

// migratedLevel is the fake backend level of a project with every
// appending migration applied.
var migratedLevel = len(sessionAppendedMigrations)

// backendAt records every RPC and answers the session RPCs as a project
// holding the first *level appending migrations; *level is read on every
// call, so a test can migrate the project between two of them.
func backendAt(r *rig, level *int) *[]map[string]any {
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
		if (fn == "register_session" || fn == "session_heartbeat") && namesAny(rpcArgs(args), appendedParamsFrom(*level)) {
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

// namesParam reports whether a recorded call carried one named parameter.
func namesParam(args map[string]any, name string) bool {
	_, present := args[name]
	return present
}

// markerPath is the rig profile's backend-legacy.json.
func markerPath(r *rig) string { return filepath.Join(r.profileDir(), legacyMarkerName) }

// writeMarker plants a marker stamped at, naming the first migration the
// backend lacks.
func writeMarker(t *testing.T, r *rig, at time.Time, migration string) {
	t.Helper()
	data, err := json.Marshal(legacyMarker{CheckedAt: at, Migration: migration})
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
	level := 0
	seen := backendAt(r, &level)
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
	missing := sessionAppendedMigrations[0].File
	if !strings.Contains(first.stderr, "predates a migration") || !strings.Contains(first.stderr, missing) {
		t.Errorf("stderr does not name the missing migration:\n%s", first.stderr)
	}
	data, err := adapterkit.ReadStrict(markerPath(r))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	var m legacyMarker
	if err := json.Unmarshal(data, &m); err != nil || !m.CheckedAt.Equal(r.now) || m.Migration != missing {
		t.Fatalf("marker = %s (%v), want checked_at %s and migration %s", data, err, r.now.Format(time.RFC3339), missing)
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
	level := 0
	seen := backendAt(r, &level)
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
			writeMarker(t, r, at, sessionAppendedMigrations[0].File)
			level := migratedLevel
			seen := backendAt(r, &level)
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
	writeMarker(t, r, r.now.Add(-legacyMarkerTTL/2), sessionAppendedMigrations[0].File)
	level := 0
	seen := backendAt(r, &level)
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

// TestDescribeWithholdsTheAppendedCapabilitiesWhileLegacy: `describe`
// drops every appended capability while a fresh marker names the first
// appending migration, drops only the ones the named migration adds when
// it names a later one, and advertises them all again once the marker is
// stale — from local files only, creating and removing nothing (C-01).
func TestDescribeWithholdsTheAppendedCapabilitiesWhileLegacy(t *testing.T) {
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
	writeMarker(t, r, r.now, sessionAppendedMigrations[0].File)
	fresh := caps()
	for _, capability := range sessionAppendedCapabilities {
		if has(fresh, capability) {
			t.Errorf("describe advertises %s while the backend is known legacy", capability)
		}
	}
	if !has(fresh, "session.inbound") || len(fresh) != len(capabilities())-len(sessionAppendedCapabilities) {
		t.Errorf("describe dropped more than the appended capabilities: %v", fresh)
	}
	if _, err := os.Stat(markerPath(r)); err != nil {
		t.Fatalf("describe removed the marker: %v", err)
	}

	// A backend behind on the LAST migration only keeps every earlier
	// migration's capabilities, because it honours them.
	last := len(sessionAppendedMigrations) - 1
	writeMarker(t, r, r.now, sessionAppendedMigrations[last].File)
	behindOne := caps()
	for _, capability := range appendedCapsFrom(last) {
		if has(behindOne, capability) {
			t.Errorf("describe advertises %s on a backend that lacks the migration adding it", capability)
		}
	}
	for i := 0; i < last; i++ {
		for _, capability := range sessionAppendedMigrations[i].Caps {
			if !has(behindOne, capability) {
				t.Errorf("describe withholds %s from a backend that has %s", capability, sessionAppendedMigrations[i].File)
			}
		}
	}

	writeMarker(t, r, r.now.Add(-legacyMarkerTTL), sessionAppendedMigrations[0].File)
	for _, capability := range sessionAppendedCapabilities {
		if !has(caps(), capability) {
			t.Errorf("describe withholds %s on a stale marker", capability)
		}
	}
}

// TestBackendBehindOnlyTheLabelMigrationKeepsModelAndContext is the
// upgrade every deployed project makes when this release lands: it has
// 20260910193200 and not 20260917170000. The registration's label is
// dropped, the registration is not, and `model` and `context_used_tokens`
// keep being stored — on that registration and on the heartbeats after
// it, which take no probe at all because the marker names the migration
// they do not depend on.
func TestBackendBehindOnlyTheLabelMigrationKeepsModelAndContext(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	behind := 1 // 20260910193200 applied, 20260917170000 (and so everything after it) not
	seen := backendAt(r, &behind)
	doc := `{"harness":"other","harness_version":"1","session_name":"main","activity":"busy","inbound":"accept",` +
		`"model":"claude-opus-5[1m]","context_used_tokens":1,"human_label":"alice@example.com"}`
	got := r.exec(doc, "session", "register")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if len(*seen) != 2 {
		t.Fatalf("calls = %v, want one probe then one without the label", *seen)
	}
	if !namesParam((*seen)[0], "p_human_label") {
		t.Errorf("the probe did not name p_human_label: %v", (*seen)[0])
	}
	retry := (*seen)[1]
	if namesParam(retry, "p_human_label") {
		t.Errorf("the retry still named p_human_label: %v", retry)
	}
	if !namesParam(retry, "p_model") || !namesParam(retry, "p_context_used_tokens") {
		t.Errorf("the retry dropped the values 20260910193200 added, which this backend stores: %v", retry)
	}
	var m legacyMarker
	data, err := adapterkit.ReadStrict(markerPath(r))
	if err != nil {
		t.Fatalf("marker not written: %v", err)
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Migration != sessionAppendedMigrations[behind].File {
		t.Fatalf("marker = %s (%v), want migration %s", data, err, sessionAppendedMigrations[behind].File)
	}
	if !strings.Contains(got.stderr, sessionAppendedMigrations[behind].File) {
		t.Errorf("stderr does not name the missing migration:\n%s", got.stderr)
	}

	// The heartbeat that follows carries no label, so the marker costs it
	// no probe and it still stores both facts.
	hb := r.exec(`{"model":"claude-opus-5[1m]","context_used_tokens":2}`, "session", "heartbeat", "--session", testSessionID)
	if hb.code != 0 {
		t.Fatalf("heartbeat: exit %d: %s", hb.code, hb.stdout)
	}
	if len(*seen) != 3 {
		t.Fatalf("calls = %v, want exactly one heartbeat RPC", *seen)
	}
	if !namesParam((*seen)[2], "p_model") || !namesParam((*seen)[2], "p_context_used_tokens") {
		t.Errorf("the heartbeat dropped the two facts on a backend that stores them: %v", (*seen)[2])
	}
	if strings.Contains(hb.stderr, "predates a migration") {
		t.Errorf("the heartbeat warned about a migration it does not need:\n%s", hb.stderr)
	}
}

// TestBackendBehindOnlyTheVersionMigrationKeepsEverythingElse is the
// upgrade every deployed project makes when the release that adds
// `brigade_version` lands (C-46): it has 20260910193200 and 20260917170000
// and not 20260920180000. Unlike a label, the version rides EVERY
// registration and heartbeat, so this is the path every session on such a
// backend takes: the version is dropped, the call is not, the model, the
// context and the label are all still stored — and once the marker names
// the migration, the heartbeats after it drop the version without a probe,
// so a lease is never a round trip longer for a migration nobody applied.
func TestBackendBehindOnlyTheVersionMigrationKeepsEverythingElse(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.joined()
	behind := len(sessionAppendedMigrations) - 1 // every migration but the newest
	if sessionAppendedMigrations[behind].File != "20260920180000_session_brigade_version.sql" {
		t.Fatalf("the newest appended migration is %s; this test is about brigade_version's", sessionAppendedMigrations[behind].File)
	}
	seen := backendAt(r, &behind)
	doc := `{"harness":"other","harness_version":"1","session_name":"main","activity":"busy","inbound":"accept",` +
		`"model":"claude-opus-5[1m]","context_used_tokens":1,"human_label":"alice@example.com","brigade_version":"0.10.0"}`
	got := r.exec(doc, "session", "register")
	if got.code != 0 {
		t.Fatalf("exit %d: %s", got.code, got.stdout)
	}
	if len(*seen) != 2 {
		t.Fatalf("calls = %v, want one probe then one without the version", *seen)
	}
	if !namesParam((*seen)[0], "p_brigade_version") {
		t.Errorf("the probe did not name p_brigade_version: %v", (*seen)[0])
	}
	retry := (*seen)[1]
	if namesParam(retry, "p_brigade_version") {
		t.Errorf("the retry still named p_brigade_version: %v", retry)
	}
	for _, kept := range []string{"p_model", "p_context_used_tokens", "p_human_label"} {
		if !namesParam(retry, kept) {
			t.Errorf("the retry dropped %s, which this backend stores: %v", kept, retry)
		}
	}
	if !strings.Contains(got.stderr, sessionAppendedMigrations[behind].File) {
		t.Errorf("stderr does not name the missing migration:\n%s", got.stderr)
	}

	// The heartbeat that follows carries the version too, as every one
	// does. The marker is fresh, so it is dropped WITHOUT a probe.
	hb := r.exec(`{"model":"claude-opus-5[1m]","context_used_tokens":2,"brigade_version":"0.10.0"}`, "session", "heartbeat", "--session", testSessionID)
	if hb.code != 0 {
		t.Fatalf("heartbeat: exit %d: %s", hb.code, hb.stdout)
	}
	if len(*seen) != 3 {
		t.Fatalf("calls = %v, want exactly one heartbeat RPC (no probe while the marker is fresh)", *seen)
	}
	if namesParam((*seen)[2], "p_brigade_version") {
		t.Errorf("the heartbeat named a parameter the marker says this backend lacks: %v", (*seen)[2])
	}
	if !namesParam((*seen)[2], "p_model") || !namesParam((*seen)[2], "p_context_used_tokens") {
		t.Errorf("the heartbeat dropped the two facts on a backend that stores them: %v", (*seen)[2])
	}
}
