package supabase

import (
	"context"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// Schema compatibility (P10-5). The adapter and the backend are updated
// independently — a member updates the plugin when they like, an
// administrator applies a migration when they like — and nothing makes
// the two agree, so a released adapter MUST work against a backend that
// is a migration behind it. The rule this file implements:
//
//   - A migration only ever APPENDS parameters to an RPC, each with a
//     default (20260910193200_session_model_context is the first). An
//     older adapter never names them and matches the new signature through
//     the defaults, which needs nothing here.
//   - A newer adapter names an appended parameter only when it has a value
//     for it. PostgREST matches an RPC by the SET of named arguments it
//     receives, so a call that omits the appended parameters matches the
//     OLD signature on a backend without the migration, and every call that
//     carries nothing new works there unchanged — every registration the
//     harness sends, for one.
//   - A call that does name an appended parameter is 404 PGRST202 on that
//     backend ("could not find the function … in the schema cache", the
//     `migration_drift` error of errors.go). The adapter then sends the same
//     call WITHOUT the parameters of the migration the backend turned out to
//     lack — the values are dropped, not the heartbeat, so a lease is never
//     lost to a missing migration — says so once on stderr, naming that
//     migration, and remembers it in the profile directory for
//     legacyMarkerTTL, so the next calls omit those parameters without a
//     probe. The marker expires, the parameters are tried again, and a
//     migration the administrator has applied in the meantime is picked up:
//     the first success with every appended parameter removes the marker.
//   - The step back is PER MIGRATION, not all-or-nothing. The migrations
//     apply in order, so a backend that has the n'th has every earlier one,
//     and how far behind it is is a single number: sessionAppendedMigrations
//     is that list in apply order and `level` below is the count of leading
//     entries the backend is believed to have. A project that has
//     20260910193200 but not 20260917170000 — which is EVERY deployed
//     project between those two releases — therefore keeps storing `model`
//     and `context_used_tokens` and loses only the registration's
//     `human_label`. One set for all of them would have taken a working
//     feature dark on every such backend, and docs/setup.md promises the
//     opposite ("what you lose until you migrate is only what the migration
//     adds"); this keeps that promise true.
//   - While the marker is fresh, `describe` does not advertise the
//     capabilities the missing migrations' parameters implement (4.7), and
//     only those: an adapter that drops a member is, for that backend, an
//     adapter without the capability, and saying so is the truthful
//     advertisement. It still answers from local files only (C-01).
//
// The marker is a 0600 file beside session.json, read through the strict
// reader; a marker that cannot be read is no marker (the call probes). A
// stale PostgREST schema cache right after a migration looks like a legacy
// backend for one TTL, which is the price of needing no version RPC — a
// version RPC would itself be absent on the one backend that matters.

// sessionAppendedMigration is one appending migration's contribution to
// the session RPCs.
type sessionAppendedMigration struct {
	// File is the migration under supabase/migrations/, which is what the
	// stderr line and the marker name.
	File string
	// Params are the parameters it appended. A parameter a given RPC never
	// carries (p_human_label on a heartbeat) simply never appears in that
	// call's arguments.
	Params []string
	// Caps are the 4.7 capabilities those parameters implement.
	Caps []string
}

// sessionAppendedMigrations are the migrations after the base schema that
// appended parameters to register_session and session_heartbeat, IN APPLY
// ORDER — the order is the mechanism (see the file comment), so a new
// entry is appended, never inserted.
var sessionAppendedMigrations = []sessionAppendedMigration{
	{
		// `p_model` and `p_context_used_tokens`, the members of 4.4.2 and
		// 4.4.4 (C-44), on both RPCs.
		File:   "20260910193200_session_model_context.sql",
		Params: []string{"p_model", "p_context_used_tokens"},
		Caps:   []string{"session.model", "session.context_used_tokens"},
	},
	{
		// `p_human_label`, the member 4.4.2 adds (C-45), on register_session
		// only — a heartbeat never carries a label.
		File:   "20260917170000_session_human_label.sql",
		Params: []string{"p_human_label"},
		Caps:   []string{"session.human_label"},
	},
}

// sessionAppendedParams are every appended parameter, which is what the
// call sites hand rpcAppended: a backend without the migrations exposes
// the older signatures.
var sessionAppendedParams = appendedParamsFrom(0)

// sessionAppendedCapabilities are every 4.7 capability those parameters
// implement.
var sessionAppendedCapabilities = appendedCapsFrom(0)

// appendedParamsFrom is the parameters the migrations from level on
// appended: exactly what a backend believed to be at that level cannot
// accept, and so what a call made at that level omits.
func appendedParamsFrom(level int) []string {
	var out []string
	for _, m := range sessionAppendedMigrations[level:] {
		out = append(out, m.Params...)
	}
	return out
}

// appendedCapsFrom is the same for the capabilities.
func appendedCapsFrom(level int) []string {
	var out []string
	for _, m := range sessionAppendedMigrations[level:] {
		out = append(out, m.Caps...)
	}
	return out
}

const (
	// legacyMarkerName is the profile-directory file that records a
	// confirmed-legacy backend.
	legacyMarkerName = "backend-legacy.json"
	// legacyMarkerTTL is how long a confirmed-legacy backend is taken on
	// trust before the appended parameters are tried again, and so the
	// longest an applied migration goes unnoticed by a running adapter.
	legacyMarkerTTL = 10 * time.Minute
)

// legacyMarker is the marker file's content.
type legacyMarker struct {
	// CheckedAt is the adapter's clock when the backend last answered
	// PGRST202 to an appended parameter.
	CheckedAt time.Time `json:"checked_at"`
	// Migration is the FIRST migration of sessionAppendedMigrations the
	// backend was found to lack; it has none of the ones after it either,
	// because migrations apply in order.
	Migration string `json:"migration"`
}

// rpcAppended is rpc for an RPC with appended parameters: the nil-valued
// ones are omitted, the call is made at the level a fresh marker gives
// (every appended parameter when there is none) and, on migration drift,
// made again one migration further back — dropping that migration's
// values, never the call — until it is accepted or nothing appended is
// left to drop. See the file comment. out and the error are rpc's.
func (c *command) rpcAppended(ctx context.Context, fn string, args rpcArgs, appended []string, out any) error {
	args = withoutNilKeys(args, appended)
	level := c.backendLevel()
	for {
		try := withoutKeys(args, appendedParamsFrom(level))
		if !namesAny(try, appended) {
			// Nothing appended is left for a backend to refuse: this call
			// matches the base signature everywhere, there is nothing further
			// to fall back to, and a failure is its own.
			return c.rpc(ctx, fn, try, out)
		}
		err := c.rpc(ctx, fn, try, out)
		if err == nil {
			if level == len(sessionAppendedMigrations) {
				c.forgetLegacy()
			}
			return nil
		}
		if !isMigrationDrift(err) {
			return err
		}
		// The backend refused a parameter, so it lacks the migration that
		// appended one of them. Step back to the last level that actually
		// changes this call — dropping parameters it never named would only
		// send the identical request again — which the loop above reached
		// naming at least one of, so this always finds one and terminates.
		for level > 0 {
			level--
			if namesAny(args, sessionAppendedMigrations[level].Params) {
				break
			}
		}
		c.log.Warn("the backend predates a migration this adapter knows; the values it adds are not stored until an administrator applies it",
			slog.String("fn", fn), slog.String("migration", sessionAppendedMigrations[level].File))
		c.rememberLegacy(sessionAppendedMigrations[level].File)
	}
}

// withoutNilKeys copies args without the keys of appended whose value is
// nil (JSON null): an absent member means "unchanged" on a heartbeat and
// "none" on a registration, which is exactly what the RPC's default gives.
func withoutNilKeys(args rpcArgs, appended []string) rpcArgs {
	out := make(rpcArgs, len(args))
	for k, v := range args {
		if isNil(v) && slices.Contains(appended, k) {
			continue
		}
		out[k] = v
	}
	return out
}

// isNil reports whether v is nil or a nil pointer held in the interface:
// the request members are *string and *int, and a nil one stored in an
// `any` is not == nil (it carries its type), which is exactly the value
// that used to be sent as JSON null.
func isNil(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	return rv.Kind() == reflect.Pointer && rv.IsNil()
}

// withoutKeys copies args without any of keys.
func withoutKeys(args rpcArgs, keys []string) rpcArgs {
	out := make(rpcArgs, len(args))
	for k, v := range args {
		if !slices.Contains(keys, k) {
			out[k] = v
		}
	}
	return out
}

// namesAny reports whether args carries any of keys.
func namesAny(args rpcArgs, keys []string) bool {
	for _, k := range keys {
		if _, ok := args[k]; ok {
			return true
		}
	}
	return false
}

// isMigrationDrift reports whether err is errors.go's PGRST202/PGRST205
// mapping.
func isMigrationDrift(err error) bool {
	var perr *protocol.Error
	return errors.As(err, &perr) && perr.Code == protocol.CodeInternal && perr.Details["reason"] == reasonMigrationDrift
}

// legacyMarkerPath is ${profileDir}/backend-legacy.json.
func (c *command) legacyMarkerPath() (string, error) {
	dir, err := c.profileDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, legacyMarkerName), nil
}

// freshMarker is the marker when one says the backend lacks a migration:
// written within legacyMarkerTTL by a clock no later than now. A missing,
// unreadable or malformed marker is no marker, and so is one stamped in
// the future.
func (c *command) freshMarker() (legacyMarker, bool) {
	path, err := c.legacyMarkerPath()
	if err != nil {
		return legacyMarker{}, false
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			c.log.Debug("legacy-backend marker unreadable; probing", slog.String("reason", "marker_unreadable"))
		}
		return legacyMarker{}, false
	}
	var m legacyMarker
	if err := json.Unmarshal(data, &m); err != nil || m.CheckedAt.IsZero() {
		return legacyMarker{}, false
	}
	now := c.now()
	if m.CheckedAt.After(now) || now.Sub(m.CheckedAt) >= legacyMarkerTTL {
		return legacyMarker{}, false
	}
	return m, true
}

// backendLevel is how many of sessionAppendedMigrations the backend is
// believed to have, in apply order: all of them unless a fresh marker
// names one it lacks, and then that migration's index — it has every
// earlier migration and none of the later ones. A marker naming a
// migration this adapter does not know (a newer adapter sharing the
// profile directory) is treated as no marker: probing costs one round
// trip, while guessing low would withhold values the backend can store.
func (c *command) backendLevel() int {
	m, ok := c.freshMarker()
	if !ok {
		return len(sessionAppendedMigrations)
	}
	for i, mig := range sessionAppendedMigrations {
		if mig.File == m.Migration {
			return i
		}
	}
	return len(sessionAppendedMigrations)
}

// rememberLegacy writes the marker with the adapter's clock, naming the
// migration the backend was found to lack. A failure is a debug line: the
// fallback still works call by call.
func (c *command) rememberLegacy(migration string) {
	path, err := c.legacyMarkerPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(legacyMarker{CheckedAt: c.now(), Migration: migration})
	if err != nil {
		return
	}
	if err := adapterkit.WriteAtomic(path, append(data, '\n')); err != nil {
		c.log.Debug("legacy-backend marker not written", slog.String("reason", "marker_unwritable"))
	}
}

// forgetLegacy removes the marker after a call that carried every
// appended parameter succeeded: the backend has all the migrations. A
// missing marker is the common case and not an error.
func (c *command) forgetLegacy() {
	path, err := c.legacyMarkerPath()
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Debug("legacy-backend marker not removed", slog.String("reason", "marker_unremovable"))
	}
}

// advertisedCapabilities is capabilities() less the ones the migrations a
// fresh marker says the backend lacks implement — and only those, so a
// backend behind on the newest migration alone still advertises every
// earlier one's capabilities, which it honours (the file comment).
func (c *command) advertisedCapabilities() []string {
	caps := capabilities()
	level := c.backendLevel()
	if level == len(sessionAppendedMigrations) {
		return caps
	}
	withheld := appendedCapsFrom(level)
	kept := caps[:0:0]
	for _, capability := range caps {
		if !slices.Contains(withheld, capability) {
			kept = append(kept, capability)
		}
	}
	return kept
}
