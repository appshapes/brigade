// Package socketpost is the inbox socket client of plan 6.7 and A.2: it
// writes one frame into a Claude Code session's `CLAUDE_CODE_MESSAGING_SOCKET`
// as the two NDJSON lines the harness reads — an auth line carrying the
// session's messaging token, then a user line carrying the content — and
// closes.
//
// What success means (4.9, E0-9): EXACTLY "written and closed without
// error". The socket answers nothing, and an explicit native
// `crossSessionInbound: refuse` drops the post with no signal to either
// side, so nothing stronger is observable and nothing stronger is claimed;
// `injected` is this and only this. Note too that a write "succeeds" once
// the kernel has buffered it: a peer that never reads shows up only when
// the buffer is full, which a small frame never is.
//
// What runs before the dial (U-19): the path must be absolute, no longer
// than the platform's sockaddr_un can carry, and Lstat must show a socket
// — not a symlink — owned by this uid with mode 0600. The stat function is
// injectable so tests can exercise the foreign-uid row without a second
// user. A refused path is never dialled.
//
// What is never here: the token in an error, a log line or a file. Post
// takes it in a Target whose String, GoString and LogValue redact it, and
// the only place it is written is the auth line on the socket.
package socketpost

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The socket protocol's fixed strings (A.2).
const (
	// TypeAuth is the `type` of the first line.
	TypeAuth = "auth"
	// TypeUser is the `type` of the second line.
	TypeUser = "user"
	// RoleUser is the `message.role` of the second line.
	RoleUser = "user"
)

// The deadlines of 6.7: connect and write are each bounded at 5 s.
const (
	// DefaultDialTimeout bounds the connect.
	DefaultDialTimeout = 5 * time.Second
	// DefaultWriteTimeout bounds the two writes together.
	DefaultWriteTimeout = 5 * time.Second
)

// Network is the address family of the inbox socket.
const Network = "unix"

// The fixed reason tokens of the dial and write steps.
const (
	reasonContext      = "context"
	reasonDial         = "dial"
	reasonDialTimeout  = "dial_timeout"
	reasonDeadline     = "deadline"
	reasonEncode       = "encode"
	reasonWrite        = "write"
	reasonWriteTimeout = "write_timeout"
	reasonEPIPE        = "epipe"
	reasonReset        = "econnreset"
	reasonClose        = "close"
)

// A Target is one session's inbox: the socket path from the hook's
// environment or the registry, and the messaging token from the hook's
// environment only.
type Target struct {
	// Path is the absolute socket path.
	Path string
	// Token is the session's messaging token. It goes on the auth line
	// and nowhere else.
	Token string
}

// String renders the target with the token redacted, so a stray %v or %s
// cannot leak it.
func (t Target) String() string { return "socketpost.Target{Path:" + t.Path + " Token:[redacted]}" }

// GoString redacts too, so %#v is safe.
func (t Target) GoString() string { return t.String() }

// LogValue renders only the path, so a Target handed to slog never
// carries the token whatever the handler.
func (t Target) LogValue() slog.Value { return slog.StringValue(t.Path) }

// Options are the injectable side effects of Post. The zero value is the
// production configuration.
type Options struct {
	// Stat is the pre-check's stat function; nil means os.Lstat.
	Stat func(string) (fs.FileInfo, error)
	// UID is the uid the socket must be owned by; nil means os.Getuid().
	UID *int
	// DialTimeout bounds the connect; zero means DefaultDialTimeout.
	DialTimeout time.Duration
	// WriteTimeout bounds the writes; zero means DefaultWriteTimeout.
	WriteTimeout time.Duration
	// Logger receives one debug line on success and one warn line on a
	// refusal or failure, scalar attributes only; nil discards them.
	Logger *slog.Logger
}

// The two wire lines. Field order is marshalling order, so line 1 is
// exactly {"type":"auth","token":"…"} and line 2 exactly
// {"type":"user","message":{"role":"user","content":"…"}}.
type authLine struct {
	Type  string `json:"type"`
	Token string `json:"token"`
}

type userLine struct {
	Type    string      `json:"type"`
	Message userMessage `json:"message"`
}

type userMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Post writes content to the target's inbox socket and closes.
//
// Sequence: the pre-checks of precheck (no dial on refusal); a dial with
// net.Dialer{Timeout}.DialContext; one write deadline covering both
// lines (the earlier of now+WriteTimeout and the context's deadline; a
// context cancelled after the dial does not interrupt a write in flight,
// the deadline bounds it); the auth line, then the user line, each
// serialised by encoding/json/v2 through one protocol.LineWriter — the
// codec escapes \n and \r inside strings, so content cannot produce a
// second physical line (U-17), while U+2028/U+2029 stay raw and valid;
// then Close. Nothing is read: the socket answers nothing.
//
// A nil return means exactly "written and closed without error" (4.9).
// Every non-nil return is a *Error classified as ErrPrecheck,
// ErrSocketGone, ErrTimeout or ErrWrite, and never contains the token.
func Post(ctx context.Context, target Target, content string, opts Options) error {
	cfg := opts.resolved()
	logger := cfg.logger

	if err := precheck(target.Path, cfg.stat, cfg.uid); err != nil {
		return logged(logger, target.Path, err)
	}
	if err := ctx.Err(); err != nil {
		return logged(logger, target.Path, newError(KindTimeout, reasonContext, err))
	}

	dialer := net.Dialer{Timeout: cfg.dialTimeout}
	conn, err := dialer.DialContext(ctx, Network, target.Path)
	if err != nil {
		return logged(logger, target.Path, classifyDial(ctx, err))
	}

	deadline := time.Now().Add(cfg.writeTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err := conn.SetWriteDeadline(deadline); err != nil {
		_ = conn.Close()
		return logged(logger, target.Path, newError(KindWrite, reasonDeadline, err))
	}

	w := protocol.NewLineWriter(conn)
	if err := w.WriteLine(authLine{Type: TypeAuth, Token: target.Token}); err != nil {
		_ = conn.Close()
		return logged(logger, target.Path, classifyWrite(ctx, err))
	}
	user := userLine{Type: TypeUser, Message: userMessage{Role: RoleUser, Content: content}}
	if err := w.WriteLine(user); err != nil {
		_ = conn.Close()
		return logged(logger, target.Path, classifyWrite(ctx, err))
	}
	if err := conn.Close(); err != nil {
		return logged(logger, target.Path, newError(KindWrite, reasonClose, err))
	}
	if logger != nil {
		logger.Debug("socket post written",
			slog.String("path", target.Path),
			slog.Int("content_bytes", len(content)))
	}
	return nil
}

// settings are the resolved Options.
type settings struct {
	stat         func(string) (fs.FileInfo, error)
	uid          int
	dialTimeout  time.Duration
	writeTimeout time.Duration
	logger       *slog.Logger
}

// resolved applies the defaults.
func (o Options) resolved() settings {
	cfg := settings{
		stat:         o.Stat,
		uid:          os.Getuid(),
		dialTimeout:  o.DialTimeout,
		writeTimeout: o.WriteTimeout,
		logger:       o.Logger,
	}
	if cfg.stat == nil {
		cfg.stat = lstat
	}
	if o.UID != nil {
		cfg.uid = *o.UID
	}
	if cfg.dialTimeout <= 0 {
		cfg.dialTimeout = DefaultDialTimeout
	}
	if cfg.writeTimeout <= 0 {
		cfg.writeTimeout = DefaultWriteTimeout
	}
	return cfg
}

// classifyDial maps a DialContext failure: the caller's context ending
// is a timeout; ENOENT and ECONNREFUSED mean the socket is gone; a
// dialer timeout is a timeout; anything else is a write-class failure.
func classifyDial(ctx context.Context, err error) *Error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return newError(KindTimeout, reasonContext, err)
	}
	if errors.Is(err, syscall.ENOENT) || errors.Is(err, syscall.ECONNREFUSED) {
		return newError(KindSocketGone, reasonDial, err)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return newError(KindTimeout, reasonDialTimeout, err)
	}
	return newError(KindWrite, reasonDial, err)
}

// classifyWrite maps a WriteLine failure: a deadline is a timeout; a
// context that ended is a timeout; EPIPE and ECONNRESET are named write
// failures; an encoding refusal (the writer's own raw-newline guard,
// unreachable for a string content) and everything else are write
// failures.
func classifyWrite(ctx context.Context, err error) *Error {
	switch {
	case errors.Is(err, os.ErrDeadlineExceeded):
		return newError(KindTimeout, reasonWriteTimeout, err)
	case ctx.Err() != nil:
		return newError(KindTimeout, reasonContext, err)
	case errors.Is(err, syscall.EPIPE):
		return newError(KindWrite, reasonEPIPE, err)
	case errors.Is(err, syscall.ECONNRESET):
		return newError(KindWrite, reasonReset, err)
	}
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return newError(KindWrite, reasonEncode, err)
	}
	return newError(KindWrite, reasonWrite, err)
}

// logged emits the one warn line for a failure and returns it unchanged.
func logged(logger *slog.Logger, path string, err *Error) error {
	if logger != nil {
		logger.Warn("socket post not written",
			slog.String("path", path),
			slog.String("kind", err.Kind.String()),
			slog.String("reason", err.Reason),
			slog.String("err", err.Error()))
	}
	return err
}
