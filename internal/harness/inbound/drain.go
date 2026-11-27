package inbound

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// The sink read at the next model call (config.DeliverNextCall, work item
// 532): the watcher appends frames to the session's sink as it does for a
// followed sink, and the host's own hook, run before each model call,
// takes what is new and puts it in front of the model. A cursor file
// beside the sinks keeps, for each sink, the offset of the first byte not
// yet handed over, so each record is handed over once. A drain reads every
// sink of the session, oldest first, each from its own offset: a watcher
// that still writes to an older sink after a newer one exists, a record
// that was half written at the last drain, and a sink whose name sorts
// before another's (a clock set back) are all read in their turn. The
// sinks are the session's alone, and a session of this kind is one
// conversation.
//
// A drain is two steps, under a lock on the session's cursor: TakeNew reads
// a batch, and the hook moves the cursor past it (Batch.Commit) only once
// its answer is written, or leaves it (Batch.Release). Two hooks of one
// session at once therefore never hand over the same batch, and an answer
// that was not written is handed over again at the next call. A hook that
// is ended between its write and the commit hands that batch over twice.
// A line that is no record (empty, unreadable, or over maxRecordBytes) is
// passed over, and the cursor moves past it like past any other.
const (
	// drainCursorName is the cursor file in the session's sink directory,
	// and drainLockName the file its lock is taken on.
	drainCursorName = "next-call.json"
	drainLockName   = "next-call.lock"
	// drainLockWait is how long a drain waits for another hook's.
	drainLockWait = 2 * time.Second
	// DrainMaxBytes caps what one drain hands over, in bytes of the sink
	// read: what is left waits for the next model call. One record is
	// handed over whole even when it alone is larger.
	DrainMaxBytes = 64 << 10
	// maxRecordBytes bounds one sink record; a longer line is passed over,
	// as the follower skips it.
	maxRecordBytes = 1 << 20
	// maxCursorBytes bounds the cursor file a drain reads.
	maxCursorBytes = 64 << 10
)

// drainCursor is what the cursor file holds: for each sink, by its file
// name, the offset of the first byte not yet handed over. A sink it does
// not name is read from its beginning.
type drainCursor struct {
	Offsets map[string]int64 `json:"offsets"`
}

// A Batch is what one drain read: the rendered text, "" when nothing was
// new, and the cursor past it. Exactly one of Commit and Release ends it.
type Batch struct {
	// Text is the rendered records, in the order the sinks hold them.
	Text  string
	dir   string
	next  drainCursor
	moved bool
	lock  *adapterkit.FileLock
}

// Commit moves the cursor past the batch, and lets the next drain in.
func (b *Batch) Commit() error {
	defer b.Release()
	if !b.moved {
		return nil
	}
	return writeDrainCursor(b.dir, b.next)
}

// Release lets the next drain in and leaves the cursor where it was: the
// batch is handed over again.
func (b *Batch) Release() {
	if b.lock != nil {
		_ = b.lock.Unlock()
		b.lock = nil
	}
}

// Moved reports whether committing the batch moves the cursor: it read new
// bytes, a record or a line that is none.
func (b *Batch) Moved() bool { return b.moved }

// TakeNew reads every complete record the session's sinks hold beyond the
// cursor, oldest sink first, at most about DrainMaxBytes of them, under the
// cursor's lock. A sink that cannot be read is an error, and the lock is
// let go.
func TakeNew(stateDir, sessionID string) (*Batch, error) {
	dir := SinkDir(stateDir, sessionID)
	names := sinkNames(dir)
	if len(names) == 0 {
		return &Batch{}, nil
	}
	lock, err := adapterkit.LockFile(filepath.Join(dir, drainLockName), drainLockWait)
	if err != nil {
		return nil, err
	}
	b := &Batch{dir: dir, lock: lock}
	cur := readDrainCursor(dir)
	// The next cursor names the sinks there are now: a sink that is gone
	// has nothing left to hand over.
	b.next = drainCursor{Offsets: map[string]int64{}}
	for _, name := range names {
		if off, ok := cur.Offsets[name]; ok {
			b.next.Offsets[name] = off
		}
	}
	var out strings.Builder
	budget := int64(DrainMaxBytes)
	for _, name := range names {
		if budget <= 0 {
			break
		}
		from := b.next.Offsets[name]
		next, err := drainFile(filepath.Join(dir, name), from, &budget, &out)
		if errors.Is(err, fs.ErrNotExist) {
			delete(b.next.Offsets, name)
			continue
		}
		if err != nil {
			b.Release()
			return nil, err
		}
		b.next.Offsets[name] = next
	}
	b.Text, b.moved = out.String(), !maps.Equal(b.next.Offsets, cur.Offsets)
	// A cursor that cannot be written (a full disk, a directory made read
	// only) would hand the same messages over at every call: prove it can
	// be, by writing it as it stands, before any is handed over.
	if b.Text != "" {
		if err := writeDrainCursor(dir, cur); err != nil {
			b.Release()
			return nil, err
		}
	}
	return b, nil
}

// DrainNew is TakeNew and Commit in one: the text handed over, with the
// cursor moved past it.
func DrainNew(stateDir, sessionID string) (string, error) {
	b, err := TakeNew(stateDir, sessionID)
	if err != nil {
		return "", err
	}
	return b.Text, b.Commit()
}

// HasNew reports whether the session's sinks hold a complete line beyond
// the cursor, and moves nothing: what a hook that may not hand a message
// over itself asks before it says that one waits.
func HasNew(stateDir, sessionID string) bool {
	dir := SinkDir(stateDir, sessionID)
	cur := readDrainCursor(dir)
	for _, name := range sinkNames(dir) {
		if completeBeyond(filepath.Join(dir, name), cur.Offsets[name]) {
			return true
		}
	}
	return false
}

// sinkNames are the names of the session's sink files, oldest first.
func sinkNames(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if e.Type().IsRegular() && validSinkName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names
}

// drainFile renders the complete records of path from offset on into out,
// until budget is spent, and returns the offset after the last line read.
// A partial last line is left for the next drain. A line over
// maxRecordBytes is passed over whole, however long. An offset past the
// end (a file that is not the one the cursor read) starts it again from 0.
func drainFile(path string, offset int64, budget *int64, out *strings.Builder) (int64, error) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return offset, err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return offset, errors.New("inbound: the sink is not a regular file")
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return offset, err
	}
	r := bufio.NewReaderSize(io.LimitReader(f, info.Size()-offset), 64<<10)
	pos := offset
	for *budget > 0 {
		line, whole, n, err := readLine(r)
		if !whole {
			// A partial last line waits for the writer to finish it. A
			// read error leaves the cursor where the last whole line ended.
			if err != nil && !errors.Is(err, io.EOF) {
				return pos, err
			}
			break
		}
		pos += n
		*budget -= n
		if len(line) == 0 {
			continue
		}
		out.WriteString(RenderRecord(line))
	}
	return pos, nil
}

// readLine reads one line and its newline: the line, nil when it is over
// maxRecordBytes (it is passed over), whether it was whole, and the bytes
// it took.
func readLine(r *bufio.Reader) (line []byte, whole bool, n int64, err error) {
	var buf bytes.Buffer
	over := false
	for {
		chunk, err := r.ReadSlice('\n')
		n += int64(len(chunk))
		if !over {
			if buf.Len()+len(chunk) > maxRecordBytes+1 {
				over = true
				buf.Reset()
			} else {
				buf.Write(chunk)
			}
		}
		switch {
		case err == nil:
			if over {
				return nil, true, n, nil
			}
			return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), true, n, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		default:
			return nil, false, n, err
		}
	}
}

// completeBeyond reports whether path holds a whole line after offset.
func completeBeyond(path string, offset int64) bool {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	if offset < 0 || offset > info.Size() {
		offset = 0
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return false
	}
	_, err = bufio.NewReader(io.LimitReader(f, info.Size()-offset)).ReadSlice('\n')
	return err == nil || errors.Is(err, bufio.ErrBufferFull)
}

// readDrainCursor is the cursor in dir, the empty cursor when there is
// none or it cannot be read. An offset for a name that is no sink's is
// dropped.
func readDrainCursor(dir string) drainCursor {
	c := drainCursor{Offsets: map[string]int64{}}
	f, err := adapterkit.OpenNoFollow(filepath.Join(dir, drainCursorName), os.O_RDONLY, 0)
	if err != nil {
		return c
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxCursorBytes))
	if err != nil {
		return c
	}
	var read drainCursor
	if json.Unmarshal(data, &read) != nil {
		return c
	}
	for name, off := range read.Offsets {
		if validSinkName(name) && off >= 0 {
			c.Offsets[name] = off
		}
	}
	return c
}

// writeDrainCursor writes the cursor, whole and atomically, mode 0600.
func writeDrainCursor(dir string, c drainCursor) error {
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return adapterkit.WriteAtomicMode(filepath.Join(dir, drainCursorName), append(data, '\n'), 0o600)
}

// validSinkName is a sink's file name as NewSinkPath makes it: digits and
// the suffix, so a cursor can name no other file.
func validSinkName(name string) bool {
	stamp, ok := strings.CutSuffix(name, sinkSuffix)
	if !ok || stamp == "" {
		return false
	}
	_, err := strconv.ParseUint(stamp, 10, 64)
	return err == nil
}

// RenderRecord turns one sink line into what the model reads: a message as
// its frame between a `team message … follows` line (with the line count,
// so a split delivery is visible) and an `end of message` line; a notice as
// one line of Brigade's own text; an unreadable line as nothing but a
// one-line note. Every id is sanitised as an attribute would be. `brigade
// inbox follow` prints it, and a host's hook hands it over at the next
// model call.
func RenderRecord(line []byte) string {
	var rec SinkRecord
	if err := json.Unmarshal(line, &rec); err != nil {
		return "Brigade: an inbox record could not be read; it was skipped.\n"
	}
	frame := strings.TrimRight(rec.Frame, "\n")
	if rec.MessageID == "" {
		return "Brigade: " + oneLineText(frame) + "\n"
	}
	id := sanitizeRecordID(rec.MessageID)
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

// sanitizeRecordID sanitises an opaque id: the protocol's rules, and no
// character that could quote an attribute or break a line.
func sanitizeRecordID(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '<', '>', '\n':
			return -1
		default:
			return r
		}
	}, protocol.Sanitize(s))
}
