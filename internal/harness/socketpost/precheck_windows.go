//go:build windows

package socketpost

import (
	"io/fs"
	"strings"
)

// MaxSocketPath is the longest name a named pipe can have: 256
// characters after the `\\.\pipe\` prefix, which the prefix's own length
// is added to here so the comparison is on the whole value.
const MaxSocketPath = 256 + len(pipePrefix)

// pipePrefix is where every named pipe lives; Claude Code's inbox is
// `\\.\pipe\LOCAL\cc-msg-<hex>` (E11), the LOCAL namespace being the one
// only this logon session's processes can open.
const pipePrefix = `\\.\pipe\`

// precheck on Windows is the pipe name's shape alone: a stat of a named
// pipe would open a client connection to it (which Claude Code closes
// after 30 s with no line), and a pipe has no mode or owner to read. The
// session-local namespace and the token on the first line are what
// authenticate the post (A.2: the auth line is required on Windows). A
// name outside the pipe namespace is refused as not a pipe; a missing
// pipe shows at the dial, as KindSocketGone.
func precheck(path string, _ func(string) (fs.FileInfo, error), _ int) *Error {
	switch {
	case path == "":
		return newError(KindPrecheck, ReasonEmpty, nil)
	case len(path) <= len(pipePrefix) || !strings.EqualFold(path[:len(pipePrefix)], pipePrefix):
		return newError(KindPrecheck, ReasonNotPipe, nil)
	case len(path) > MaxSocketPath:
		return newError(KindPrecheck, ReasonPathTooLong, nil)
	case strings.ContainsAny(path[len(pipePrefix):], "/"):
		return newError(KindPrecheck, ReasonNotPipe, nil)
	}
	return nil
}
