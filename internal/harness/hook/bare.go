package hook

import (
	"encoding/json/jsontext"
	"io"
	"os"
	"time"

	"golang.org/x/term"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// bareCallWait is how long a `brigade` with no arguments waits for a
// hook's document on its stdin before it is taken for no hook's. A host
// writes a hook's document at once, so the wait is only ever spent on a
// stdin that nobody writes to, which must not hold the usage error back
// for good. It stays under the shortest timeout the hooks have, five
// seconds, with room for the launcher.
const bareCallWait = 3 * time.Second

// maxBareDocument bounds what is read of stdin to find a hook's event.
// It is far above maxStdinBytes on purpose: `hook prompt` exits 0 on a
// document it finds too long, and the bare call must not answer the same
// document with a usage error.
const maxBareDocument = 64 << 20

// BareCall reports whether a `brigade` started with no arguments at all
// was started as a hook (work item 402, GitHub issue 59, ADR 0017).
//
// The Claude Code package's hooks are in the exec form, the binary in
// `command` and its words in `args`, on purpose (plan 6.3;
// `make plugin-check` holds it). A host that loads Claude Code's
// plugins and drops `args` runs the bare binary for every hook. The bare
// binary's usage error exits 2, which is the status a hook blocks with,
// so every prompt of such a host was blocked. A bare call that carries a
// hook's marks does nothing and exits 0 instead, as every hook does on
// every failure (6.3): Brigade is inactive in that host, which is all it
// can be where its words do not arrive.
//
// The marks are two, and either is enough, since a host may hand over one
// and not the other: the plugin's root in the environment, which Claude
// Code gives a plugin's hook and neither a terminal nor a session's own
// shell tool has (measured in a session of 2026-11-22: CLAUDE_PID and no
// CLAUDE_PLUGIN_ROOT); or a hook's document on stdin, a JSON object whose
// `hook_event_name` names an event. The other three packages word their
// hooks as one string or a wrapper script, which no host can cut in two,
// so their roots are no marks. With neither mark the call is a person's or
// a model's slip, and it keeps its usage error.
func BareCall(in io.Reader, environ []string) bool {
	return bareCall(in, environ, bareCallWait)
}

// bareCall is BareCall with the wait for a document as the caller's.
func bareCall(in io.Reader, environ []string, wait time.Duration) bool {
	if adapterkit.Getenv(environ, envPluginRoot) != "" {
		return true
	}
	return hookDocument(in, wait)
}

// hookDocument reports whether in begins as a hook's document does. A
// terminal is never read: a person at one is waiting for the usage line.
// It is decided from the document's start, member by member, and not by
// decoding the whole of it: a host that writes the document and leaves
// the pipe open is answered as one that closes it, and a document that
// `hook prompt` would refuse (too long, text that is no UTF-8, a name
// twice) is still a hook's, since that hook exits 0 on it too.
func hookDocument(in io.Reader, wait time.Duration) bool {
	if in == nil {
		return false
	}
	if f, ok := in.(*os.File); ok && term.IsTerminal(int(f.Fd())) { //nolint:gosec // G115: a file descriptor fits an int
		return false
	}
	answer := make(chan bool, 1)
	go func() { answer <- namesAHookEvent(in) }()
	select {
	case is := <-answer:
		return is
	case <-time.After(wait):
		// The reader is left to the process's exit, which follows.
		return false
	}
}

// namesAHookEvent reads in as far as the top-level `hook_event_name` of
// the JSON object it begins with, and reports whether that names an event.
func namesAHookEvent(in io.Reader) bool {
	dec := jsontext.NewDecoder(io.LimitReader(in, maxBareDocument),
		jsontext.AllowDuplicateNames(true), jsontext.AllowInvalidUTF8(true))
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return false
	}
	for dec.PeekKind() == '"' {
		name, err := dec.ReadToken()
		if err != nil {
			return false
		}
		if name.String() != "hook_event_name" {
			if dec.SkipValue() != nil {
				return false
			}
			continue
		}
		event, err := dec.ReadToken()
		return err == nil && event.Kind() == '"' && event.String() != ""
	}
	return false
}
