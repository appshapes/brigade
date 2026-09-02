package cases

import (
	"strings"

	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/protocol"
)

// c02Usage: an unknown verb and an unknown flag on a core command are
// `usage` (exit 2, 4.1, 4.6); malformed, oversize (1 MiB + 1) and empty
// stdin on `session register` are `invalid_input` (exit 3, 4.1); every
// failing envelope carries `retryable` (B-2, through Fail). The argv checks
// run on a scratch principal. The stdin checks run on fixture principal A:
// on an unconfigured profile 4.6's "unbound profile → 4 or 11" (C-06) and
// 4.1's "malformed stdin → invalid_input" both apply and the spec does not
// order them (the fs adapter checks the profile first, measured), so only
// a joined principal isolates the stdin rule.
func c02Usage() conformance.Case {
	return conformance.Case{
		ID:    "C-02",
		Rule:  "4.6 usage and invalid_input",
		Title: "unknown verb/flag → usage; malformed, oversize and empty stdin → invalid_input",
		Tags:  []string{conformance.TagCore},
		Run:   runC02,
	}
}

func runC02(t *conformance.T) {
	scratch := t.Scratch("c02")
	t.Fail(t.Exec(scratch, nil, "session", "frobnicate"), protocol.CodeUsage)
	t.Fail(t.Exec(scratch, nil, "describe", "--bogus"), protocol.CodeUsage)

	a := t.A()
	e := t.Fail(t.Exec(a, []byte("{"), "session", "register"), protocol.CodeInvalidInput)
	if e.Details["reason"] == "" {
		t.Errorf("session register with malformed stdin: details.reason absent (4.3.1)")
	}

	// 1 MiB + 1 byte: {"a":"…"} padded so the whole document is one byte
	// over the cap; a valid JSON document, refused for its size alone.
	const overCap = 1<<20 + 1
	prefix, suffix := `{"a":"`, `"}`
	big := prefix + strings.Repeat("x", overCap-len(prefix)-len(suffix)) + suffix
	t.Fail(t.Exec(a, []byte(big), "session", "register"), protocol.CodeInvalidInput)

	t.Fail(t.Exec(a, []byte{}, "session", "register"), protocol.CodeInvalidInput)
}
