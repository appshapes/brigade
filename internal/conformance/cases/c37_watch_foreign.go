package cases

import (
	"bytes"
	"time"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c37WatchExitDeadline is the spec's exit deadline for a watch that has
// emitted a non-retryable `error` event (4.4.9, brief section 6).
const c37WatchExitDeadline = 10 * time.Second

// c37WatchForeign: a watch on a session the principal does not own — C in
// team T2 watching B's fixture session — emits an `error` event with
// not_found, `retryable` present and false (B-2), and exits 6 within 10 s
// (4.5.7, 4.4.9). No `ready` is required before it (the catch-up never
// started); either order is accepted but the error must come.
//
// 4.5.7 does not stop at the code: a session that is not owned is refused
// with "the same answer as for an unknown id (C-13, C-19, C-24, C-37)", so
// the two `error` event LINES must be byte-identical — a foreign id that
// is distinguishable from an id that never existed is an existence oracle
// even when both say not_found. The positive control is a third watch
// with no --session at all: an argument fault on a core command is `usage`
// (4.1) and a watch reports it as an `error` event like any other (4.4.9:
// stdout is NDJSON and nothing else), so its line must NOT match — an
// adapter that printed one fixed error line for everything would pass the
// identity check and fail this one.
func c37WatchForeign() conformance.Case {
	return conformance.Case{
		ID:    "C-37",
		Rule:  "4.5.7 watch not owned",
		Title: "watching a foreign session yields the unknown-id error event, byte for byte, and exit 6",
		Tags:  []string{conformance.TagCore},
		Run:   runC37,
	}
}

func runC37(t *conformance.T) {
	b, c := t.B(), t.C()
	foreign := watchRefusal(t, "a session owned by another principal", c, "--session", t.Session(b))
	unknown := watchRefusal(t, "a session id that was never issued", c, "--session", randomRef())
	for _, r := range []*refusal{foreign, unknown} {
		r.assertCode(t, protocol.CodeNotFound)
		r.assertExit(t, protocol.CodeNotFound.Exit())
	}
	if !bytes.Equal(foreign.line, unknown.line) {
		t.Errorf("the `error` event for %s and for %s differ byte for byte; 4.5.7 requires the same answer:\n  %s\n  %s",
			foreign.what, unknown.what, foreign.line, unknown.line)
	}

	// Positive control: a DIFFERENT refusal must not be byte-identical, or
	// the check above would hold on an adapter that prints one error for
	// everything.
	usage := watchRefusal(t, "a watch with no --session", c)
	usage.assertCode(t, protocol.CodeUsage)
	usage.assertExit(t, protocol.CodeUsage.Exit())
	if bytes.Equal(usage.line, foreign.line) {
		t.Errorf("the `error` event for %s is byte-identical to the one for %s; want different (positive control)",
			usage.what, foreign.what)
	}
}

// A refusal is one watch that was refused: the raw `error` line, the
// decoded event and the process's exit status.
type refusal struct {
	what  string
	line  []byte
	event conformance.Event
	exit  int
	ended bool
}

// watchRefusal starts a watch with args, waits for its `error` event and
// reaps it. `retryable` is asserted present and false here (B-2, 4.4.9):
// every refusal in this case is a fatal one.
func watchRefusal(t *conformance.T, what string, p *conformance.Principal, args ...string) *refusal {
	w := t.WatchArgs(p, args...)
	ev := w.Expect(protocol.EventError, c37WatchExitDeadline)
	r := &refusal{what: what, line: ev.Line, event: ev}
	if ev.Error == nil {
		t.Fatalf("message watch (%s): the error event did not validate (4.4.9)", what)
	}
	errObj, _ := ev.Raw["error"].(map[string]any)
	rv, present := errObj["retryable"]
	if !present {
		t.Errorf("message watch (%s): error.retryable is absent (B-2)", what)
	} else if rb, isBool := rv.(bool); !isBool || rb {
		t.Errorf("message watch (%s): error.retryable is %v, want false (4.4.9)", what, rv)
	}
	r.exit, r.ended = w.Wait(c37WatchExitDeadline)
	return r
}

// assertCode checks the code the error event carries.
func (r *refusal) assertCode(t *conformance.T, want protocol.Code) {
	if r.event.Error != nil && r.event.Error.Error.Code != want {
		t.Errorf("message watch (%s): error event code %s, want %s (4.5.7)", r.what, r.event.Error.Error.Code, want)
	}
}

// assertExit checks the 4.6 status the process left with, within the
// spec's 10 s deadline for a non-retryable error event.
func (r *refusal) assertExit(t *conformance.T, want int) {
	if !r.ended {
		t.Errorf("message watch (%s): still running %s after a non-retryable error event (4.4.9)", r.what, c37WatchExitDeadline)
		return
	}
	if r.exit != want {
		t.Errorf("message watch (%s): exit %d, want %d (4.4.9, 4.6)", r.what, r.exit, want)
	}
}
