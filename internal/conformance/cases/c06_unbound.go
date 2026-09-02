package cases

import (
	"bytes"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/buildinfo"
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c06Unbound: every `session`/`message` command on an unconfigured
// scratch principal answers `unauthenticated` (exit 4) or `config` (exit
// 11) — the code and the exit status matching, `retryable` present and
// false — with one envelope on stdout and never a stack trace on stderr
// (4.6). The documents on stdin are valid, so the profile is the only
// thing wrong.
func c06Unbound() conformance.Case {
	return conformance.Case{
		ID:    "C-06",
		Rule:  "4.6 unbound profile",
		Title: "session and message commands on an unbound profile → exit 4 unauthenticated or 11 config, never a stack trace",
		Tags:  []string{conformance.TagCore},
		Run:   runC06,
	}
}

func runC06(t *conformance.T) {
	p := t.Scratch("c06")
	reg := protocol.SessionRegistration{
		Harness:        conformance.Harness,
		HarnessVersion: buildinfo.String(),
		SessionName:    "c06-" + t.RunID(),
		Activity:       protocol.ActivityBusy,
		Inbound:        protocol.InboundAccept,
	}
	send := protocol.SendRequest{SenderSessionID: randomRef(), RecipientSessionID: randomRef(), Body: "c06"}
	for _, r := range []*conformance.Result{
		t.Exec(p, nil, "session", "list"),
		t.Exec(p, mustMarshal(t, &reg), "session", "register"),
		t.Exec(p, nil, "message", "receive", "--session", randomRef()),
		t.Exec(p, mustMarshal(t, &send), "message", "send"),
	} {
		failOneOf(t, r, protocol.CodeUnauthenticated, protocol.CodeConfig)
		if bytes.Contains(r.Stderr, []byte("goroutine ")) || bytes.Contains(r.Stderr, []byte("panic:")) {
			t.Errorf("%s: stderr carries a stack trace (4.6)", commandOf(r))
		}
	}
}

// failOneOf is Fail for a rule that allows more than one code (4.6's
// "exit 4 or 11"): ok:false, error.code one of codes, exit matching that
// code, retryable present and equal to the code's row. It records and
// continues rather than aborting, so every command is checked.
func failOneOf(t *conformance.T, r *conformance.Result, codes ...protocol.Code) {
	name := commandOf(r)
	if r.Envelope == nil {
		t.Errorf("%s: stdout is not one valid envelope (exit %d)", name, r.Exit)
		return
	}
	if r.Envelope.OK {
		t.Errorf("%s: want ok:false, got ok:true (exit %d)", name, r.Exit)
		return
	}
	e := r.Envelope.Error
	var code protocol.Code
	for _, c := range codes {
		if e.Code == c {
			code = c
		}
	}
	if code == "" {
		t.Errorf("%s: error.code %s is none of %v", name, e.Code, codes)
		return
	}
	if r.Exit != code.Exit() {
		t.Errorf("%s: exit %d, want %d for %s (4.6)", name, r.Exit, code.Exit(), code)
	}
	errObj, _ := r.Raw["error"].(map[string]any)
	rv, present := errObj["retryable"]
	rb, isBool := rv.(bool)
	switch {
	case !present:
		t.Errorf("%s: error.retryable is absent (B-2)", name)
	case !isBool:
		t.Errorf("%s: error.retryable is not a boolean (4.3)", name)
	case rb != code.Retryable():
		t.Errorf("%s: error.retryable is %v, want %v for %s (4.6)", name, rb, code.Retryable(), code)
	}
}

// commandOf names the case's own arguments of r (the launcher prepends
// the adapter and its fixed arguments to Args, so the command is the
// tail after the first argument that is a known group).
func commandOf(r *conformance.Result) string {
	for i, a := range r.Args {
		switch a {
		case "describe", "session", "message", "team", "profile":
			return strings.Join(r.Args[i:], " ")
		}
	}
	return "command (" + strconv.Itoa(len(r.Args)) + " args)"
}
