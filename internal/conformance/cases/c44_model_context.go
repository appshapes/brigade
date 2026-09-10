package cases

import (
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c44ModelContext (caps session.model, session.context_used_tokens):
// `model` and `context_used_tokens` set at registration come back exactly
// in the register result and in `session list`; a heartbeat carrying both
// applies the new values; a heartbeat carrying neither leaves them
// standing (absent means unchanged, JSON convention 4 — the harness never
// clears them); a model of max_model_chars + 1 code points made of 2-byte
// runes is `invalid_input` naming `model` while one at the cap passes (so
// an adapter counting bytes fails, as in C-16); a context_used_tokens of
// −1 is `invalid_input` naming `context_used_tokens` (4.4.2, 4.4.3, 4.4.4,
// 4.5.11, 4.7). Both values are harness-reported and unverified: the case
// asserts storage and echo, never meaning.
func c44ModelContext() conformance.Case {
	return conformance.Case{
		ID:    "C-44",
		Rule:  "4.4.2 model and context_used_tokens",
		Title: "model and context_used_tokens round-trip through register, list and heartbeat; absent leaves them unchanged; over the caps → invalid_input",
		Tags: []string{
			conformance.TagCore,
			conformance.TagCap("session.model"),
			conformance.TagCap("session.context_used_tokens"),
		},
		Run: runC44,
	}
}

func runC44(t *conformance.T) {
	a := t.A()

	// Registration: both members, echoed by the register result (the raw
	// composite, so the number is checked as JSON carried it) and by the
	// list (the validated record).
	model, used := "claude-opus-5[1m]", 189681
	rec, s := t.Register(a, nameFor(t, "C-44", "facts"), func(reg *protocol.SessionRegistration) {
		reg.Model, reg.ContextUsedTokens = &model, &used
	})
	if got, _ := rec["model"].(string); got != model {
		t.Errorf("session register: model %q, want %q (4.4.2)", got, model)
	}
	if got, ok := rawInt(rec, "context_used_tokens"); !ok || got != used {
		t.Errorf("session register: context_used_tokens %v, want %d (4.4.2)", rec["context_used_tokens"], used)
	}
	sessions, _ := t.List(a, "", false)
	checkFacts(t, "session list", recordOf(t, sessions, s), model, used)

	// A heartbeat with both applies them (4.4.4).
	newModel, newUsed := "claude-sonnet-5", 2048
	t.Heartbeat(a, s, &protocol.HeartbeatRequest{Model: &newModel, ContextUsedTokens: &newUsed})
	sessions, _ = t.List(a, "", false)
	checkFacts(t, "session list after a heartbeat with both", recordOf(t, sessions, s), newModel, newUsed)

	// A heartbeat with neither is a pure renewal: the stored values stand
	// (JSON convention 4; 4.4.4 "never cleared by a heartbeat").
	t.Heartbeat(a, s, nil)
	sessions, _ = t.List(a, "", false)
	checkFacts(t, "session list after a heartbeat with neither", recordOf(t, sessions, s), newModel, newUsed)

	// The caps (4.5.11): code points, not bytes, for model; the lower bound
	// for context_used_tokens.
	limits := t.Describe().Limits
	over := strings.Repeat("é", limits.MaxModelChars+1)
	e := t.Fail(t.Exec(a, registrationWith(t, nameFor(t, "C-44", "over"), func(r *protocol.SessionRegistration) {
		r.Model = &over
	}), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["field"] != "model" {
		t.Errorf("model over the cap: details.field %q, want model (4.3.1)", e.Details["field"])
	}
	atCap := strings.Repeat("é", limits.MaxModelChars)
	rec, _ = t.Register(a, nameFor(t, "C-44", "cap"), func(r *protocol.SessionRegistration) { r.Model = &atCap })
	if got, _ := rec["model"].(string); got != atCap {
		t.Errorf("model at the cap: the record's model differs from the one registered (4.4.3)")
	}

	negative := -1
	e = t.Fail(t.Exec(a, registrationWith(t, nameFor(t, "C-44", "negative"), func(r *protocol.SessionRegistration) {
		r.ContextUsedTokens = &negative
	}), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["field"] != "context_used_tokens" {
		t.Errorf("context_used_tokens -1: details.field %q, want context_used_tokens (4.3.1)", e.Details["field"])
	}
}

// checkFacts asserts one listed record carries exactly the model and
// context_used_tokens given; an absent member (nil pointer) is reported
// as such rather than as a zero value, because absent and zero differ on
// the wire (a non-nil 0 marshals as `"context_used_tokens":0`).
func checkFacts(t *conformance.T, what string, rec *protocol.SessionRecord, model string, used int) {
	switch {
	case rec.Model == nil:
		t.Errorf("%s: model absent, want %q (4.4.3)", what, model)
	case *rec.Model != model:
		t.Errorf("%s: model %q, want %q (4.4.3)", what, *rec.Model, model)
	}
	switch {
	case rec.ContextUsedTokens == nil:
		t.Errorf("%s: context_used_tokens absent, want %d (4.4.3)", what, used)
	case *rec.ContextUsedTokens != used:
		t.Errorf("%s: context_used_tokens %d, want %d (4.4.3)", what, *rec.ContextUsedTokens, used)
	}
}
