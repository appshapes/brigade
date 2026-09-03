package sessionmap

import (
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// ByNative is the by-native map (plan 3.2, 3.7): keyed by the native
// Claude Code session id, it survives session end so that `claude --resume
// <id>` finds the Brigade session to re-open. It is OVERWRITTEN on every
// write: a native id RECURS within one process (E0-5 item 6: `/clear` then
// `/resume` returns the id to its pre-clear value), so the map tolerates a
// returning id by design rather than assuming ids are monotonic.
type ByNative struct {
	BrigadeSessionID string    `json:"brigade_session_id"`
	TeamRef          string    `json:"team_ref"`
	SessionName      string    `json:"session_name"`
	UpdatedAt        time.Time `json:"updated_at,omitzero"`
}

// Validate requires the one member a resume hint cannot do without.
func (m *ByNative) Validate() error {
	if m.BrigadeSessionID == "" {
		return errInvalid("brigade_session_id")
	}
	return nil
}

// MaxNativeIDLen is the length cap of a native session id used as a file
// name.
const MaxNativeIDLen = 80

// CheckNativeID validates a native session id before it becomes a path
// component: 1–80 characters from [A-Za-z0-9_-]. Claude Code's ids are
// UUIDs, which pass; anything with a separator, a dot or a non-ASCII rune
// is refused, so a hostile id in hook stdin cannot escape the by-native
// directory. The offending value is never echoed.
func CheckNativeID(id string) error {
	bad := func() error {
		return &protocol.Error{
			Code:    protocol.CodeConfig,
			Message: "native session id must be 1-80 characters from letters, digits, dash and underscore",
			Details: map[string]string{"reason": ReasonMapInvalid, "field": "claude_session_id"},
		}
	}
	if id == "" || len(id) > MaxNativeIDLen {
		return bad()
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
		default:
			return bad()
		}
	}
	return nil
}
