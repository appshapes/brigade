// Package doing holds what the harness knows about a session's doing line
// (card 25, plan .context/plans/session-doing.md): the one sentence a
// session's own model publishes as `session_description` so teammates can
// route by it. The package is neutral on purpose — the roster's display
// (commands) and, later, the writer and the watcher's read-back all need
// the same cap, and `watch` does not import `commands`.
package doing

// MaxChars is the harness's cap on a doing line, in code points: the cap
// the writer refuses over (never truncates) and the cap the roster's
// `DOING (unverified)` column cuts to with the truncation marker. It is
// deliberately below the wire cap, protocol.MaxDescriptionChars (256),
// which stays as it is — conformance requires at-cap values to pass, and
// a value Brigade's own writer sent can never exceed this one — so the
// table's cut only ever reaches text Brigade did not write (plan 5.1,
// 5.5; ruling 11).
const MaxChars = 160
