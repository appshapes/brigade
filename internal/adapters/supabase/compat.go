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
//     call WITHOUT the appended parameters — the values are dropped, not the
//     heartbeat, so a lease is never lost to a missing migration — says so
//     once on stderr, naming the migration, and remembers the backend as
//     legacy in the profile directory for legacyMarkerTTL, so the next
//     calls omit the parameters without a probe. The marker expires, the
//     parameters are tried again, and a migration the administrator has
//     applied in the meantime is picked up: the first success with the
//     parameters removes the marker.
//   - While the marker is fresh, `describe` does not advertise the
//     capabilities those parameters implement (4.7): an adapter that drops a
//     member is, for that backend, an adapter without the capability, and
//     saying so is the truthful advertisement. It still answers from local
//     files only (C-01).
//
// The marker is a 0600 file beside session.json, read through the strict
// reader; a marker that cannot be read is no marker (the call probes). A
// stale PostgREST schema cache right after a migration looks like a legacy
// backend for one TTL, which is the price of needing no version RPC — a
// version RPC would itself be absent on the one backend that matters.

// sessionAppendedParams are the parameters migration
// 20260910193200_session_model_context appended to register_session and
// session_heartbeat (the `model` and `context_used_tokens` members of
// 4.4.2 and 4.4.4). A backend without it exposes the older signatures.
var sessionAppendedParams = []string{"p_model", "p_context_used_tokens"}

// sessionAppendedCapabilities are the 4.7 capabilities those parameters
// implement, withheld from `describe` while the backend is known legacy.
var sessionAppendedCapabilities = []string{"session.model", "session.context_used_tokens"}

const (
	// legacyMarkerName is the profile-directory file that records a
	// confirmed-legacy backend.
	legacyMarkerName = "backend-legacy.json"
	// legacyMarkerTTL is how long a confirmed-legacy backend is taken on
	// trust before the appended parameters are tried again, and so the
	// longest an applied migration goes unnoticed by a running adapter.
	legacyMarkerTTL = 10 * time.Minute
	// legacyMigration names the migration the stderr line asks for.
	legacyMigration = "20260910193200_session_model_context.sql"
)

// legacyMarker is the marker file's content.
type legacyMarker struct {
	// CheckedAt is the adapter's clock when the backend last answered
	// PGRST202 to an appended parameter.
	CheckedAt time.Time `json:"checked_at"`
	// Migration is the migration the backend was found to lack.
	Migration string `json:"migration"`
}

// rpcAppended is rpc for an RPC with appended parameters: the nil-valued
// ones are omitted; a backend known legacy gets none of them; otherwise
// the call is made as given and, on migration drift, made again without
// them (see the file comment). out and the error are rpc's.
func (c *command) rpcAppended(ctx context.Context, fn string, args rpcArgs, appended []string, out any) error {
	args = withoutNilKeys(args, appended)
	if !namesAny(args, appended) {
		return c.rpc(ctx, fn, args, out)
	}
	if c.backendIsLegacy() {
		return c.rpc(ctx, fn, withoutKeys(args, appended), out)
	}
	err := c.rpc(ctx, fn, args, out)
	if err == nil {
		c.forgetLegacy()
		return nil
	}
	if !isMigrationDrift(err) {
		return err
	}
	c.log.Warn("the backend predates a migration this adapter knows; model and context_used_tokens are not stored until an administrator applies it",
		slog.String("fn", fn), slog.String("migration", legacyMigration))
	c.rememberLegacy()
	return c.rpc(ctx, fn, withoutKeys(args, appended), out)
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

// backendIsLegacy reports whether a fresh marker says the backend lacks
// the migration: one written within legacyMarkerTTL by a clock no later
// than now. A missing, unreadable or malformed marker is no marker, and
// so is one stamped in the future.
func (c *command) backendIsLegacy() bool {
	path, err := c.legacyMarkerPath()
	if err != nil {
		return false
	}
	data, err := adapterkit.ReadStrict(path)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			c.log.Debug("legacy-backend marker unreadable; probing", slog.String("reason", "marker_unreadable"))
		}
		return false
	}
	var m legacyMarker
	if err := json.Unmarshal(data, &m); err != nil || m.CheckedAt.IsZero() {
		return false
	}
	now := c.now()
	return !m.CheckedAt.After(now) && now.Sub(m.CheckedAt) < legacyMarkerTTL
}

// rememberLegacy writes the marker with the adapter's clock. A failure is
// a debug line: the fallback still works call by call.
func (c *command) rememberLegacy() {
	path, err := c.legacyMarkerPath()
	if err != nil {
		return
	}
	data, err := json.Marshal(legacyMarker{CheckedAt: c.now(), Migration: legacyMigration})
	if err != nil {
		return
	}
	if err := adapterkit.WriteAtomic(path, append(data, '\n')); err != nil {
		c.log.Debug("legacy-backend marker not written", slog.String("reason", "marker_unwritable"))
	}
}

// forgetLegacy removes the marker after a call that carried an appended
// parameter succeeded: the backend has the migration. A missing marker is
// the common case and not an error.
func (c *command) forgetLegacy() {
	path, err := c.legacyMarkerPath()
	if err != nil {
		return
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		c.log.Debug("legacy-backend marker not removed", slog.String("reason", "marker_unremovable"))
	}
}

// advertisedCapabilities is capabilities() less the ones a legacy backend
// cannot honour while its marker is fresh (the file comment).
func (c *command) advertisedCapabilities() []string {
	caps := capabilities()
	if !c.backendIsLegacy() {
		return caps
	}
	kept := caps[:0:0]
	for _, capability := range caps {
		if !slices.Contains(sessionAppendedCapabilities, capability) {
			kept = append(kept, capability)
		}
	}
	return kept
}
