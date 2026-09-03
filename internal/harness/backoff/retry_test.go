package backoff

import (
	"errors"
	"fmt"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

func TestRetryableOverEveryCode(t *testing.T) {
	t.Parallel()
	want := map[protocol.Code]bool{
		protocol.CodeInternal:         false,
		protocol.CodeUsage:            false,
		protocol.CodeInvalidInput:     false,
		protocol.CodeUnauthenticated:  false,
		protocol.CodeUnauthorized:     false,
		protocol.CodeNotFound:         false,
		protocol.CodeConflict:         false,
		protocol.CodeRateLimited:      true,
		protocol.CodeUnavailable:      true,
		protocol.CodeProtocolMismatch: false,
		protocol.CodeConfig:           false,
		protocol.CodeLoopDetected:     false,
	}
	if len(want) != len(allCodes) {
		t.Fatalf("table has %d codes, taxonomy has %d", len(want), len(allCodes))
	}
	for _, code := range allCodes {
		w, ok := want[code]
		if !ok {
			t.Fatalf("code %q missing from the table", code)
		}
		if got := Retryable(code); got != w {
			t.Errorf("Retryable(%q) = %v, want %v", code, got, w)
		}
		// The protocol package's own table is the source of truth (4.3);
		// this predicate must agree with it for every code.
		if got := code.Retryable(); got != w {
			t.Errorf("protocol.Code(%q).Retryable() = %v, table says %v", code, got, w)
		}
	}
	for _, unknown := range []protocol.Code{"", "retry_me", "RATE_LIMITED", "unavailable "} {
		if Retryable(unknown) {
			t.Errorf("unknown code %q must not be retryable", unknown)
		}
	}
}

func TestAllCodesIsTheTaxonomyInExitOrder(t *testing.T) {
	t.Parallel()
	seen := map[int]bool{}
	for i, code := range allCodes {
		if exit := code.Exit(); exit != i+1 || seen[exit] {
			t.Errorf("allCodes[%d] = %q exits %d", i, code, exit)
		}
		seen[code.Exit()] = true
	}
	if len(seen) != 12 {
		t.Fatalf("%d distinct exits, want 12", len(seen))
	}
	// codeForExit is the inverse for exactly those statuses.
	for exit := -2; exit <= 200; exit++ {
		code, ok := codeForExit(exit)
		if ok != (exit >= 1 && exit <= 12) {
			t.Errorf("codeForExit(%d) ok=%v", exit, ok)
		}
		if ok && code.Exit() != exit {
			t.Errorf("codeForExit(%d) = %q which exits %d", exit, code, code.Exit())
		}
	}
}

func TestRetryableError(t *testing.T) {
	t.Parallel()
	perr := func(code protocol.Code) error {
		return &protocol.Error{Code: code, Message: "x"}
	}
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"plain error", errors.New("boom"), false},
		{"unavailable", perr(protocol.CodeUnavailable), true},
		{"rate_limited", perr(protocol.CodeRateLimited), true},
		{"invalid_input", perr(protocol.CodeInvalidInput), false},
		{"unauthorized", perr(protocol.CodeUnauthorized), false},
		{"loop_detected", perr(protocol.CodeLoopDetected), false},
		{"wrapped unavailable", fmt.Errorf("call: %w", perr(protocol.CodeUnavailable)), true},
		{"wrapped config", fmt.Errorf("call: %w", perr(protocol.CodeConfig)), false},
		{"spawn timeout mapping", &protocol.Error{Code: protocol.CodeUnavailable, Details: map[string]string{"reason": "timeout"}}, true},
		{"spawn signal mapping", &protocol.Error{Code: protocol.CodeUnavailable, Details: map[string]string{"signal": "killed"}}, true},
		{"typed nil pointer", func() error { var p *protocol.Error; return p }(), false},
	}
	for _, tc := range cases {
		if got := RetryableError(tc.err); got != tc.want {
			t.Errorf("%s: RetryableError = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestRetryableExit(t *testing.T) {
	t.Parallel()
	want := map[int]bool{
		0:   false, // clean end
		1:   false, // internal
		2:   false, // usage
		3:   false, // invalid_input
		4:   false, // unauthenticated: stop (6.6)
		5:   false, // unauthorized: stop
		6:   false, // not_found
		7:   false, // conflict
		8:   true,  // rate_limited: restart
		9:   true,  // unavailable: restart
		10:  false, // protocol_mismatch: stop
		11:  false, // config: stop
		12:  false, // loop_detected
		13:  true,  // no code owns it: a crash
		126: true,
		127: true,
		137: true, // 128+SIGKILL
		143: true, // 128+SIGTERM
		-1:  true, // exec.ExitError.ExitCode() for a signal death
	}
	for exit, w := range want {
		if got := RetryableExit(exit); got != w {
			t.Errorf("RetryableExit(%d) = %v, want %v", exit, got, w)
		}
	}
}
