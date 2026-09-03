package socketpost

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

func TestKindSentinelAndString(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		kind     Kind
		sentinel error
		name     string
	}{
		{KindPrecheck, ErrPrecheck, "precheck"},
		{KindSocketGone, ErrSocketGone, "socket_gone"},
		{KindTimeout, ErrTimeout, "timeout"},
		{KindWrite, ErrWrite, "write"},
		{Kind(0), ErrWrite, "unknown"},
		{Kind(99), ErrWrite, "unknown"},
	} {
		if !errors.Is(tc.kind.Sentinel(), tc.sentinel) {
			t.Errorf("%v.Sentinel() = %v, want %v", tc.kind, tc.kind.Sentinel(), tc.sentinel)
		}
		if tc.kind.String() != tc.name {
			t.Errorf("%d.String() = %q, want %q", tc.kind, tc.kind.String(), tc.name)
		}
	}
}

func TestErrorIsExactlyItsKind(t *testing.T) {
	t.Parallel()
	all := []error{ErrPrecheck, ErrSocketGone, ErrTimeout, ErrWrite}
	for _, kind := range []Kind{KindPrecheck, KindSocketGone, KindTimeout, KindWrite} {
		err := error(newError(kind, "r", nil))
		for _, s := range all {
			if got, want := errors.Is(err, s), errors.Is(s, kind.Sentinel()); got != want {
				t.Errorf("kind %v: errors.Is(err, %v) = %v, want %v", kind, s, got, want)
			}
		}
		var e *Error
		if !errors.As(err, &e) || e.Kind != kind {
			t.Errorf("errors.As failed for kind %v", kind)
		}
	}
}

func TestErrorUnwrapsTheTransportError(t *testing.T) {
	t.Parallel()
	err := newError(KindSocketGone, ReasonMissing, syscall.ENOENT)
	if !errors.Is(err, syscall.ENOENT) {
		t.Error("underlying ENOENT not visible through errors.Is")
	}
	if !errors.Is(err, ErrSocketGone) {
		t.Error("sentinel not visible")
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		t.Error("a different errno matched")
	}
	if got := err.Error(); got != "socketpost: socket is gone (missing): no such file or directory" {
		t.Errorf("Error() = %q", got)
	}
	bare := newError(KindPrecheck, ReasonSymlink, nil)
	if got := bare.Error(); got != "socketpost: pre-check refused the socket (symlink)" {
		t.Errorf("Error() = %q", got)
	}
	if bare.Unwrap() != nil {
		t.Error("Unwrap of a bare error is not nil")
	}
}

// TestErrorTextIsFixedAndValueFree: reasons are tokens, never a value the
// caller passed (the content or the token can never enter through them).
func TestErrorTextIsFixedAndValueFree(t *testing.T) {
	t.Parallel()
	for _, r := range []string{
		ReasonEmpty, ReasonRelative, ReasonPathTooLong, ReasonMissing, ReasonStat, ReasonSymlink,
		ReasonNotSocket, ReasonMode, ReasonForeignUID, ReasonOwnerUnknown,
		reasonContext, reasonDial, reasonDialTimeout, reasonDeadline, reasonEncode, reasonWrite,
		reasonWriteTimeout, reasonEPIPE, reasonReset, reasonClose,
	} {
		if r == "" || strings.ContainsAny(r, " \"\n") {
			t.Errorf("reason %q is not a bare token", r)
		}
	}
}
