package socketpost

import "errors"

// The classification P3-5's watcher acts on (plan 6.7, U-20). Every
// failure Post returns is a *Error whose Kind selects one of the four
// sentinels below, so callers switch with errors.Is and never parse text:
//
//   - ErrPrecheck: the path was refused before any dial (U-19). Nothing
//     was connected; the socket is not this user's 0600 socket, or the
//     path cannot be dialled at all. Log it and stop posting to that path.
//   - ErrSocketGone: nothing is there — ENOENT at the pre-check or on
//     dial, or ECONNREFUSED on dial. Re-read the registry for a new path
//     (E0-5: `claude` never re-creates a socket it lost, so this is not a
//     liveness signal, only "this path is dead").
//   - ErrTimeout: the dial or the write hit its deadline, or the caller's
//     context ended. Not injected; back off and retry.
//   - ErrWrite: the connection or a write failed some other way (EPIPE,
//     ECONNRESET, a refused non-gone dial, a close error). Not injected;
//     back off and retry.
//
// The wrapped error is the transport's own (a *net.OpError carrying the
// path) and never the content or the token: nothing this package returns
// or logs contains the token, and the tests grep for it.
var (
	ErrPrecheck   = errors.New("socketpost: pre-check refused the socket")
	ErrSocketGone = errors.New("socketpost: socket is gone")
	ErrTimeout    = errors.New("socketpost: timed out")
	ErrWrite      = errors.New("socketpost: connection or write failed")
)

// A Kind is the classification of one failure; each maps to one sentinel.
type Kind int

// The four kinds, in the order the post proceeds.
const (
	KindPrecheck Kind = iota + 1
	KindSocketGone
	KindTimeout
	KindWrite
)

// Sentinel returns the sentinel error errors.Is matches for the kind.
func (k Kind) Sentinel() error {
	switch k {
	case KindPrecheck:
		return ErrPrecheck
	case KindSocketGone:
		return ErrSocketGone
	case KindTimeout:
		return ErrTimeout
	case KindWrite:
		return ErrWrite
	default:
		return ErrWrite
	}
}

// String names the kind for logs.
func (k Kind) String() string {
	switch k {
	case KindPrecheck:
		return "precheck"
	case KindSocketGone:
		return "socket_gone"
	case KindTimeout:
		return "timeout"
	case KindWrite:
		return "write"
	default:
		return "unknown"
	}
}

// An Error is one classified failure of Post.
type Error struct {
	// Kind selects the sentinel.
	Kind Kind
	// Reason is a fixed, value-free token naming the check or step that
	// failed (e.g. "symlink", "mode", "foreign_uid", "path_too_long",
	// "missing", "dial", "write_timeout", "epipe", "close").
	Reason string
	// Err is the underlying error, if any; never carries the token.
	Err error
}

// Error implements error: the sentinel's text, the reason, then the
// underlying error.
func (e *Error) Error() string {
	s := e.Kind.Sentinel().Error() + " (" + e.Reason + ")"
	if e.Err != nil {
		s += ": " + e.Err.Error()
	}
	return s
}

// Unwrap exposes the underlying error, so errors.Is(err, syscall.ENOENT)
// and friends see through the classification.
func (e *Error) Unwrap() error { return e.Err }

// Is matches the sentinel of the error's kind.
func (e *Error) Is(target error) bool {
	return target == e.Kind.Sentinel() //nolint:errorlint // sentinel identity is the point of Is
}

// newError builds a classified error.
func newError(kind Kind, reason string, err error) *Error {
	return &Error{Kind: kind, Reason: reason, Err: err}
}
