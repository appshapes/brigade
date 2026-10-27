package commands

import (
	"bufio"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/procutil"
	"github.com/appshapes/brigade/internal/protocol"
)

// `brigade inbox follow` (card 88) is the monitor command of a Muse Code
// session: the model arms Muse's `monitor` tool on it once, persistent, with
// an immediate wake, and from then on every team message the session's
// watcher appends to the inbox sink is printed here — the frame between two
// lines of Brigade's own text — and the monitor wakes the idle session 0.2 s
// later (measured). It reads the newest sink of the session from its
// beginning, so nothing that landed between the first prompt and the arming
// is missed, follows it, switches to a newer sink when the watcher is
// replaced, and ends when the session is no longer registered. It writes
// nothing anywhere: under Muse's default profile the shell it runs in may
// write only to the workspace, and the state directory is not that.
const (
	// FollowPoll is how often the follower looks for new bytes, a new sink
	// and the session's map.
	FollowPoll = 250 * time.Millisecond
	// followMapCheck is how often the session's registration is re-read.
	followMapCheck = 5 * time.Second
	// FollowEndedLine is the one line printed when the session's map is
	// gone: the session ended, and the follower with it.
	FollowEndedLine = "Brigade: this session is no longer registered; the follower stops."
	// followMaxLineBytes bounds one sink record; a real record is well
	// under the wire's body cap plus the frame.
	followMaxLineBytes = 1 << 20
)

// inboxFollow implements `brigade inbox follow`.
func inboxFollow(inv Invocation, args []string) error {
	if len(args) > 0 && config.HostOf(inv.Environ) != config.HostGrok {
		return usage("inbox follow takes no arguments")
	}
	if !config.InSession(inv.Environ) {
		return notInSession("inbox follow")
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	m, err := config.Session(inv.Environ, stateDir)
	if err != nil {
		return err
	}
	f := &follower{inv: inv, stateDir: stateDir, sessionID: m.BrigadeSessionID, binding: m}
	if m.Harness == string(config.HostGrok) {
		if len(args) != 2 || args[0] != "--incarnation" || !m.ValidGrokReceiver() || args[1] != m.Grok.Incarnation {
			return usage("Grok inbox follow requires the exact --incarnation from this session's monitor command")
		}
		if !f.receiverValid() {
			return notRegisteredFollow()
		}
	}
	err = f.run()
	if errors.Is(err, errFollowConsumerClosed) {
		return nil
	}
	return err
}

// A follower tails one session's inbox sinks.
type follower struct {
	inv       Invocation
	stateDir  string
	sessionID string
	binding   *sessionmap.ByPID

	path   string   // the sink being followed, "" before the first
	file   *os.File // open on path
	reader *bufio.Reader
	// partial holds an incomplete last line between polls: the watcher
	// writes a record in one call, but a reader can still see a prefix.
	partial []byte
}

// run is the follower's life: find the sink, replay it, follow it, switch
// to a newer one, stop when the session is gone or stdout is closed.
func (f *follower) run() error {
	if f.grok() {
		return f.runGrokFollow()
	}
	defer f.closeFile()
	lastMapCheck := time.Now()
	for {
		if newest := inbound.NewestSink(f.stateDir, f.sessionID); newest != "" && newest != f.path {
			// A first sink, or a newer one (the watcher was replaced):
			// drain what the old one still holds, then read the new one
			// from its beginning.
			if f.file != nil {
				if err := f.drain(); err != nil {
					return err
				}
			}
			if err := f.open(newest); err != nil {
				return err
			}
		}
		if f.file != nil {
			if err := f.drain(); err != nil {
				return err
			}
		}
		if time.Since(lastMapCheck) >= followMapCheck {
			lastMapCheck = time.Now()
			if current, err := config.Session(f.inv.Environ, f.stateDir); err != nil || !sessionmap.SameReceiver(f.binding, current) {
				if err == nil {
					return f.write(FollowEndedLine + "\n")
				}
				var perr *protocol.Error
				if errors.As(err, &perr) && perr.Details["reason"] == config.ReasonNotRegistered {
					return f.write(FollowEndedLine + "\n")
				}
				// Any other refusal (a map that cannot be trusted) is a
				// reason to stop too, with the refusal as the answer.
				return err
			}
		}
		time.Sleep(FollowPoll)
	}
}

// open starts following path from its beginning.
func (f *follower) open(path string) error {
	f.closeFile()
	var file *os.File
	var err error
	if f.grok() {
		file, err = adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	} else {
		file, err = os.OpenFile(path, os.O_RDONLY, 0) //nolint:gosec // the path is Brigade's own, under its state directory
	}
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return &protocol.Error{Code: protocol.CodeConfig, Message: "the inbox sink could not be opened", Details: map[string]string{"reason": "sink_unreadable"}}
	}
	if f.grok() {
		info, err := file.Stat()
		if err != nil {
			_ = file.Close()
			return errors.New("inbox sink metadata could not be read")
		}
		if !info.Mode().IsRegular() || !adapterkit.IsPrivate(info) {
			_ = file.Close()
			return errors.New("inbox sink is not private")
		}
	}
	f.path, f.file, f.reader, f.partial = path, file, bufio.NewReaderSize(file, 64<<10), nil
	return nil
}

func (f *follower) closeFile() {
	if f.file != nil {
		_ = f.file.Close()
		f.file, f.reader = nil, nil
	}
}

// drain prints every complete record the sink holds beyond what was read.
func (f *follower) drain() error {
	for {
		chunk, err := f.reader.ReadBytes('\n')
		if len(chunk) > 0 {
			f.partial = append(f.partial, chunk...)
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return &protocol.Error{Code: protocol.CodeConfig, Message: "the inbox sink could not be read", Details: map[string]string{"reason": "sink_unreadable"}}
		}
		line := f.partial
		f.partial = nil
		if len(line) > followMaxLineBytes {
			continue
		}
		if werr := f.write(renderRecord(line)); werr != nil {
			return werr
		}
	}
}

// renderRecord turns one sink line into what the model reads: a message as
// its frame between a `team message … follows` line (with the line count,
// so a split delivery is visible) and an `end of message` line; a notice as
// one line of Brigade's own text; an unreadable line as nothing but a
// one-line note. Every id is sanitised as an attribute would be.
func renderRecord(line []byte) string {
	var rec inbound.SinkRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return "Brigade: an inbox record could not be read; it was skipped.\n"
	}
	frame := strings.TrimRight(rec.Frame, "\n")
	if rec.MessageID == "" {
		return "Brigade: " + oneLineText(frame) + "\n"
	}
	id := sanitizeID(rec.MessageID)
	lines := strings.Count(frame, "\n") + 1
	var b strings.Builder
	b.Grow(len(frame) + 160)
	b.WriteString("Brigade: team message " + id + " follows (" + strconv.Itoa(lines) + " lines)\n")
	b.WriteString(frame)
	b.WriteString("\nBrigade: end of message " + id + "\n")
	return b.String()
}

// oneLineText folds a notice onto one sanitised line.
func oneLineText(s string) string {
	s = protocol.Sanitize(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '\n', '\r', '\t':
			return ' '
		}
		return r
	}, s)
	return strings.TrimSpace(s)
}

// write puts text on stdout in one call; a closed stdout ends the follower
// quietly, since whoever read it is gone.
func (f *follower) write(text string) error {
	if f.inv.Out == nil {
		return nil
	}
	if n, err := io.WriteString(f.inv.Out, text); err != nil || n != len(text) {
		return errFollowConsumerClosed
	}
	return nil
}

var errFollowConsumerClosed = errors.New("inbox consumer closed")

func (f *follower) grok() bool {
	return f.binding != nil && f.binding.Harness == string(config.HostGrok)
}
func (f *follower) receiverValid() bool {
	current, err := config.Session(f.inv.Environ, f.stateDir)
	if err != nil || !sessionmap.SameReceiver(f.binding, current) {
		return false
	}
	lookup := f.inv.Deps.Lookup
	if lookup == nil {
		lookup = procutil.Lookup
	}
	if life, _ := current.GrokLiveness(lookup); life == sessionmap.GrokDead {
		return false
	}
	ppid := os.Getppid()
	if f.inv.Deps.Getppid != nil {
		ppid = f.inv.Deps.Getppid()
	}
	seen := map[int]bool{}
	for range 32 {
		if ppid == current.ClaudePID {
			return true
		}
		if ppid <= 1 || seen[ppid] {
			return false
		}
		seen[ppid] = true
		info, err := lookup(ppid)
		if err != nil || !info.Exists || info.Foreign || info.Zombie {
			return false
		}
		ppid = info.ParentPID
	}
	return false
}
func notRegisteredFollow() error {
	return &protocol.Error{Code: protocol.CodeConfig, Message: "Grok monitor receiver is no longer live or bound", Details: map[string]string{"reason": config.ReasonNotRegistered}}
}

// Native monitor output is a body-free, bounded coalescible hint only.
func renderGrokRecord(_ []byte) string {
	return "{\"brigade_inbox_changed\":true,\"action\":\"drain the local inbox using /brigade:team-messaging\"}\n"
}

// Observe only this incarnation's sink metadata. A dropped hint cannot consume
// records: a later hint/read drains the stream using a validated cursor.
func (f *follower) runGrokFollow() error {
	previous := ""
	last := time.Time{}
	for {
		if !f.receiverValid() {
			return f.write("{\"brigade_status\":\"receiver_ended\"}\n")
		}
		dir := inbound.GrokSinkDir(f.stateDir, f.sessionID, f.binding.Grok.Incarnation)
		entries, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return errors.New("inbox directory could not be read; follower paused")
		}
		if len(entries) > 4096 {
			return errors.New("inbox generation limit exceeded; follower paused")
		}
		var snapshot strings.Builder
		for _, e := range entries {
			if !e.Type().IsRegular() || !strings.HasSuffix(e.Name(), ".ndjson") {
				continue
			}
			info, err := e.Info()
			if err != nil {
				return errors.New("inbox metadata unavailable; follower paused")
			}
			if info.Size() == 0 {
				continue
			}
			snapshot.WriteString(e.Name())
			snapshot.WriteByte(':')
			snapshot.WriteString(strconv.FormatInt(info.Size(), 10))
			snapshot.WriteByte(':')
			snapshot.WriteString(strconv.FormatInt(info.ModTime().UnixNano(), 10))
			snapshot.WriteByte('\n')
		}
		current := snapshot.String()
		if current != previous && (last.IsZero() || time.Since(last) >= 5*time.Second) {
			if !f.receiverValid() {
				continue
			}
			if err := f.write(renderGrokRecord(nil)); err != nil {
				return err
			}
			previous = current
			last = time.Now()
		}
		time.Sleep(FollowPoll)
	}
}
