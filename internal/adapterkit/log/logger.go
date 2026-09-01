package log

import (
	"errors"
	"io"
	"log/slog"
)

// New returns a JSON slog logger writing to w through the redacting
// handler. level nil means slog.LevelInfo (the HandlerOptions default);
// pass a *slog.LevelVar to change it later. r nil means a fresh Redactor
// with no exact tokens — pass your own to register secrets (the
// messaging token, a refresh token) before or after construction.
//
// The handler never writes to os.Stdout by this package's choice of w:
// stdout is protocol output only (plan 7.3); diagnostics go to stderr or
// a 0600 log file, both of which the harness may capture at debug level,
// which is exactly why everything is redacted.
func New(w io.Writer, level slog.Leveler, r *Redactor) *slog.Logger {
	if r == nil {
		r = NewRedactor()
	}
	return slog.New(slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level:       level,
		ReplaceAttr: r.replaceAttr,
	}))
}

// replaceAttr is the slog.HandlerOptions.ReplaceAttr hook. It sees every
// leaf attribute — the JSON handler resolves LogValuers and recurses
// into Groups before calling it (verified, plan A.7; pinned by the group
// tests) — plus the built-in msg, level and time attributes at the top
// level.
func (r *Redactor) replaceAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == slog.LevelKey) {
		return a
	}
	if isSecretKey(a.Key) {
		// Key-list hit: the value is replaced whatever its kind, so a
		// secret under a well-named key can never leak through a kind
		// this switch mishandles.
		return slog.Attr{Key: r.Redact(a.Key), Value: slog.StringValue(Redacted)}
	}
	a.Key = r.Redact(a.Key)
	switch a.Value.Kind() {
	case slog.KindString:
		// The message arrives here too (key "msg", top level).
		a.Value = slog.StringValue(r.Redact(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok && err != nil {
			a.Value = slog.StringValue(r.Redact(errText(err)))
		} else {
			// The scalar-only policy (doc.go, layer 4): a struct, map,
			// slice or nil is never printed, because neither this hook
			// nor the handler would redact inside it.
			a.Value = slog.StringValue(Suppressed)
		}
	case slog.KindBool, slog.KindDuration, slog.KindFloat64, slog.KindInt64,
		slog.KindTime, slog.KindUint64:
		// Carry no redactable text.
	case slog.KindGroup, slog.KindLogValuer:
		// Never reach ReplaceAttr: the handler recurses into groups
		// (tested) and resolves LogValuers first.
	default:
		// A slog.Kind this handler does not know cannot be trusted to
		// carry no text.
		a.Value = slog.StringValue(Suppressed)
	}
	return a
}

// Err is the typed replacement for slog.Any("err", err), which forbidigo
// bans outside this package: an error is logged as its message string,
// which the handler then redacts like any other string. A nil error logs
// as "<nil>".
func Err(err error) slog.Attr {
	if err == nil {
		return slog.String("err", "<nil>")
	}
	return slog.String("err", errText(err))
}

// errText returns err.Error(), surviving an Error method that panics
// (a typed-nil receiver inside a non-nil error interface is the classic
// case). A logging call must never take down a watcher.
func errText(err error) (s string) {
	defer func() {
		if recover() != nil {
			s = "[error value panicked in Error()]"
		}
	}()
	return err.Error()
}

// ParseLevel parses the wire grammar of --log-level and
// BRIGADE_LOG_LEVEL: exactly one of "error", "warn", "info", "debug"
// (plan 4.1). Anything else is an error the caller maps to its own
// taxonomy (usage for a flag, config for the environment); the message
// never echoes the input, which could be a mis-pasted secret.
func ParseLevel(s string) (slog.Level, error) {
	switch s {
	case "error":
		return slog.LevelError, nil
	case "warn":
		return slog.LevelWarn, nil
	case "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	default:
		return 0, errors.New(`log level must be one of "error", "warn", "info", "debug"`)
	}
}
