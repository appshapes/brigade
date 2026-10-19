package inbound

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

// The inbox sink of a host without an inbox socket (card 88). On Muse Code
// the watcher cannot post into the session: it appends each frame as one
// NDJSON record to a sink file, and the session's model reads the file
// through a monitor it armed on `brigade inbox follow`. The sink is PER
// WATCHER: ${stateDir}/inbox/<brigade_session_id>/<spawn-time>.ndjson,
// named by the hook at spawn time, so a follower started after the watcher
// reads the newest file from its beginning — every frame this watcher
// delivered, including the ones that landed between the session's first
// prompt and the moment the model armed the monitor — and never the frames
// an earlier watcher of the same Brigade session delivered to an earlier
// conversation. A respawned watcher (a plugin update, a policy change) gets
// a new file; the follower notices and switches to it.
const (
	inboxDirName = "inbox"
	sinkSuffix   = ".ndjson"
)

// A SinkRecord is one line of a sink file: what a socket post would have
// carried, with the ids a reader keys on. A notice has an empty MessageID.
// The watcher writes it (watch/inject.go); `brigade inbox follow` reads it.
type SinkRecord struct {
	TS              time.Time `json:"ts"`
	Frame           string    `json:"frame"`
	MessageID       string    `json:"message_id"`
	SenderSessionID string    `json:"sender_session_id"`
}

// SinkDir is ${stateDir}/inbox/<brigade_session_id>.
func SinkDir(stateDir, brigadeSessionID string) string {
	return filepath.Join(stateDir, inboxDirName, brigadeSessionID)
}

// NewSinkPath is the sink file a watcher spawned at `now` writes:
// SinkDir/<unix nanoseconds, zero-padded>.ndjson, so the newest sorts last
// by name.
func NewSinkPath(stateDir, brigadeSessionID string, now time.Time) string {
	stamp := strconv.FormatInt(now.UnixNano(), 10)
	if len(stamp) < 20 {
		stamp = strings.Repeat("0", 20-len(stamp)) + stamp
	}
	return filepath.Join(SinkDir(stateDir, brigadeSessionID), stamp+sinkSuffix)
}

// NewestSink is the sink file of SinkDir that sorts last by name, or "" when
// the directory holds none (or does not exist). Only regular `.ndjson`
// entries count.
func NewestSink(stateDir, brigadeSessionID string) string {
	entries, err := os.ReadDir(SinkDir(stateDir, brigadeSessionID))
	if err != nil {
		return ""
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), sinkSuffix) {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return ""
	}
	sort.Strings(names)
	return filepath.Join(SinkDir(stateDir, brigadeSessionID), names[len(names)-1])
}

// GrokSinkDir isolates a direct receiver incarnation's retained local backlog.
func GrokSinkDir(stateDir, sessionID, incarnation string) string {
	return filepath.Join(SinkDir(stateDir, sessionID), "grok-"+incarnation)
}

// GrokSinkPath keeps each replacement watcher's append file separate while
// allowing the incarnation's follower to drain all files in order.
func GrokSinkPath(stateDir, sessionID, incarnation string, now time.Time) string {
	return filepath.Join(GrokSinkDir(stateDir, sessionID, incarnation), filepath.Base(NewSinkPath(stateDir, sessionID, now)))
}
