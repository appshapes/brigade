package cases

import (
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c27Sizes: a body of max_body_bytes + 1 BYTES (é×8192 + "a") is
// `invalid_input` naming `body` with `unit bytes`; é×8192 (exactly the
// cap in bytes, half of it in code points) passes, and so does an ASCII
// body at the cap; a summary of max_summary_chars + 1 code points is
// `invalid_input` naming `summary`, one at the cap passes; an empty body
// is `invalid_input` (JSON convention 5, 4.3.1, 4.4.6, 4.5.11). B sends
// to A.
func c27Sizes() conformance.Case {
	return conformance.Case{
		ID:    "C-27",
		Rule:  "4.5.11 body and summary caps",
		Title: "body 16,385 bytes → invalid_input (unit bytes), 16,384 pass; summary 201 code points → invalid_input, 200 pass; empty body → invalid_input",
		Tags:  []string{conformance.TagCore},
		Run:   runC27,
	}
}

func runC27(t *conformance.T) {
	a, b := t.A(), t.B()
	limits := t.Describe().Limits
	_, sb := t.Register(b, "c27-b-"+t.RunID(), nil)
	_, sa := t.Register(a, "c27-a-"+t.RunID(), nil)
	send := func(body, summary string) *conformance.Result {
		req := protocol.SendRequest{SenderSessionID: sb, RecipientSessionID: sa, Body: body, Summary: summary}
		return t.SendRaw(b, mustMarshal(t, &req))
	}

	twoByte := strings.Repeat("é", limits.MaxBodyBytes/2)
	e := t.Fail(send(twoByte+"a", ""), protocol.CodeInvalidInput)
	if e.Details["field"] != "body" {
		t.Errorf("body over the cap: details.field %q, want body (4.3.1)", e.Details["field"])
	}
	if e.Details["unit"] != "bytes" {
		t.Errorf("body over the cap: details.unit %q, want bytes (JSON convention 5)", e.Details["unit"])
	}
	var res protocol.SendResponse
	t.OK(send(twoByte, ""), &res)
	t.OK(send(strings.Repeat("a", limits.MaxBodyBytes), ""), &res)

	e = t.Fail(send("c27: summary", strings.Repeat("é", limits.MaxSummaryChars+1)), protocol.CodeInvalidInput)
	if e.Details["field"] != "summary" {
		t.Errorf("summary over the cap: details.field %q, want summary (4.3.1)", e.Details["field"])
	}
	t.OK(send("c27: summary", strings.Repeat("é", limits.MaxSummaryChars)), &res)

	e = t.Fail(send("", ""), protocol.CodeInvalidInput)
	if e.Details["field"] != "body" {
		t.Errorf("empty body: details.field %q, want body (4.3.1)", e.Details["field"])
	}
}
