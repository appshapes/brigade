// Package transcript reads Claude Code's own session transcript — the
// append-only NDJSON file whose path the hooks receive as `transcript_path`
// — for the two facts `brigade sessions` shows beside a session's name and
// activity: the model it runs and how much of its context window is in
// use. It is read LOCALLY by the detached watcher (never by a hook, whose
// budget is seconds; never by an adapter) and only the two derived facts
// ever leave the machine, as the `model` and `context_used_tokens`
// heartbeat members (4.4.4); the transcript's contents, its path and the
// native session id stay where they are (T10, U-22).
//
// The shape is Claude Code's to change (measured on 2.1.267): records of
// type `attachment` with attachment.type "model" carry the FULL model id
// in attachment.identity.modelId ("claude-opus-5[1m]"), written at session
// start and on every /model switch; records of type `assistant` carry the
// BARE id in message.model ("claude-opus-5") and the response's usage in
// message.usage — input_tokens, cache_creation_input_tokens and
// cache_read_input_tokens, whose sum is the context occupancy. Every
// record has isSidechain; a sidechain (a subagent's turn) says nothing
// about this session's context, and neither does the `<synthetic>`
// assistant record Claude Code writes for a turn the API refused (its
// usage is all zeros). Anything unexpected — a line that is not JSON, a
// member of another type, an unknown record type, a usage with a
// negative count — is skipped, never a failure: the facts simply stay
// what they were. Two things are decoded leniently rather than refused,
// because Claude Code legitimately writes them: invalid UTF-8 and a lone
// surrogate escape (JSON.stringify's rendering of an unpaired surrogate)
// become U+FFFD, and a duplicated member takes its last value, as the
// JSON.parse that wrote the file reads it back. The count the reader
// yields is never negative and never wraps (a sum that would overflow
// saturates); the wire's upper bound on it is the caller's (4.4.4).
//
// The reader is incremental: it keeps a byte offset and the facts so far,
// and each Refresh reads only what was appended, complete lines only. A
// line can be hundreds of KB (a tool result) and a file tens of MB, so one
// line is capped at MaxLineBytes and a longer one is skipped whole; a file
// shorter than the offset (truncated, or replaced by another) starts the
// reader over. Nothing here can block the watcher: the path must name a
// regular file (a FIFO, a device or a directory is refused before any
// read), and the errors are diagnostic only — fixed text, never the path.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"strings"
	"syscall"
)

// MaxLineBytes caps one transcript line, newline excluded. A real line is
// at most hundreds of KB; a longer one is skipped whole and the offset
// advanced past it, so a runaway record can cost at most this much memory
// and never stalls the facts behind it.
const MaxLineBytes = 64 << 20

// readBufferBytes is the bufio buffer one ReadSlice fills; a line longer
// than it is assembled across slices up to MaxLineBytes.
const readBufferBytes = 64 << 10

// The record types and the attachment type the reader keys on.
const (
	typeAttachment  = "attachment"
	typeAssistant   = "assistant"
	attachmentModel = "model"
	// syntheticModel is the message.model Claude Code writes on an
	// assistant record the API never answered — a 529, a usage limit, "not
	// logged in" (measured on 2.1.267: five such records, every usage
	// count 0, the content the error text). Such a record says nothing
	// about the model or the context and is skipped whole.
	syntheticModel = "<synthetic>"
)

// lenient is how a line is decoded: invalid UTF-8 and a lone surrogate
// escape become U+FFFD instead of refusing the record, and a duplicated
// member takes its last value — the JSON.parse semantics of the program
// that wrote the file. Nothing decoded here is trusted: the model is
// sanitised by the caller and the counts are checked in apply.
var lenient = json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))

// The errors Refresh returns. They are diagnostic only: fixed text, no
// path, no underlying error — the watcher logs them at debug level and the
// last facts stand.
var (
	// ErrMissing: the transcript does not exist (yet, or any more).
	ErrMissing = errors.New("transcript does not exist")
	// ErrNotRegular: the path names something other than a regular file
	// (a FIFO, a device, a directory); nothing was opened for reading.
	ErrNotRegular = errors.New("transcript is not a regular file")
	// ErrUnreadable: a stat, open, seek or read failure other than
	// "missing".
	ErrUnreadable = errors.New("transcript cannot be read")
)

// Facts are the two facts the transcript yields.
type Facts struct {
	// Model is the model identity as Claude Code wrote it — the full id
	// from the latest model attachment ("claude-opus-5[1m]") unless an
	// assistant record since then names a bare id the attachment does not
	// begin with (a switch the attachment missed), or the bare id alone
	// when no attachment was seen; "" when neither was. It is RAW: the
	// caller sanitises (protocol.SanitizeModel) before it travels.
	Model string
	// ContextUsedTokens is input_tokens + cache_creation_input_tokens +
	// cache_read_input_tokens of the latest non-sidechain assistant record
	// that carries a usage with non-negative counts; meaningful only when
	// HasContext. It is never negative and never a wrapped sum (a corrupt
	// usage saturates at math.MaxInt); the wire's upper bound is the
	// caller's to apply.
	ContextUsedTokens int
	// HasContext reports whether any usage was seen.
	HasContext bool
}

// A record is the minimal shape the reader decodes from one line: every
// other member of the line is skipped by the decoder without being kept.
// message and attachment are pointers so their absence is distinguishable
// from an empty object, and usage likewise.
type record struct {
	Type        string `json:"type"`
	IsSidechain bool   `json:"isSidechain"`
	Message     *struct {
		Model string `json:"model"`
		Usage *struct {
			InputTokens              int `json:"input_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
	Attachment *struct {
		Type     string `json:"type"`
		Identity struct {
			ModelID string `json:"modelId"`
		} `json:"identity"`
	} `json:"attachment"`
}

// A Reader is the incremental reader of one transcript file. It is not
// safe for concurrent use: the watcher drives it from one goroutine.
type Reader struct {
	path string
	// offset is the file position of the first byte not yet consumed:
	// the start of a trailing partial line, or the end of the file.
	offset int64
	// skipping is true while the reader is inside a line that exceeded
	// MaxLineBytes: bytes are discarded up to and including its newline.
	skipping bool

	// The model facts (see Facts.Model): attModel is the latest model
	// attachment's full id, asstModel the latest assistant record's bare
	// id, asstAfterAtt whether an assistant record naming a model came
	// after the latest attachment.
	attModel     string
	asstModel    string
	asstAfterAtt bool
	// The context facts: the latest usage sum and whether one was seen.
	tokens     int
	hasContext bool
}

// NewReader returns a reader positioned at the start of path. Nothing is
// opened until Refresh.
func NewReader(path string) *Reader {
	return &Reader{path: path}
}

// Path is the transcript path this reader follows.
func (r *Reader) Path() string { return r.path }

// Refresh reads whatever was appended since the last call and returns the
// facts so far. A file shorter than the offset is treated as truncated:
// the offset and the facts start over. On an error the facts returned are
// the last known ones (a transcript that vanished keeps reporting what it
// said), and the error is one of ErrMissing, ErrNotRegular and
// ErrUnreadable — diagnostic only, never the path.
func (r *Reader) Refresh() (Facts, error) {
	fi, err := os.Stat(r.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return r.facts(), ErrMissing
	case err != nil:
		return r.facts(), ErrUnreadable
	case !fi.Mode().IsRegular():
		return r.facts(), ErrNotRegular
	}
	if fi.Size() < r.offset {
		r.reset()
	}
	if fi.Size() == r.offset {
		return r.facts(), nil
	}
	// O_NONBLOCK closes the check-then-open race: a FIFO swapped in
	// between the stat above and this open returns at once instead of
	// blocking for a writer, and the fstat below refuses it; it has no
	// effect on a regular file (the same idiom as sessionmap's reader).
	f, err := os.OpenFile(r.path, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return r.facts(), ErrMissing
		}
		return r.facts(), ErrUnreadable
	}
	defer func() { _ = f.Close() }()
	if fi, err = f.Stat(); err != nil {
		return r.facts(), ErrUnreadable
	}
	if !fi.Mode().IsRegular() {
		return r.facts(), ErrNotRegular
	}
	if _, err := f.Seek(r.offset, io.SeekStart); err != nil {
		return r.facts(), ErrUnreadable
	}
	if err := r.consume(bufio.NewReaderSize(f, readBufferBytes)); err != nil {
		return r.facts(), ErrUnreadable
	}
	return r.facts(), nil
}

// consume applies every complete line from the current offset to the end
// of the file, advancing the offset past each; a trailing line without a
// newline is left unread for the next call. A line longer than
// MaxLineBytes is skipped: the bytes seen so far are dropped, the offset
// advanced past them, and skipping stays set until its newline arrives —
// in this call or a later one, so the over-long line is read exactly once.
func (r *Reader) consume(br *bufio.Reader) error {
	var line []byte // the current line's bytes so far, not yet counted in offset
	for {
		chunk, err := br.ReadSlice('\n')
		switch {
		case err == nil:
			// A complete line: chunk ends with the newline.
			n := int64(len(line) + len(chunk))
			switch {
			case r.skipping:
				r.skipping = false
			case len(line)+len(chunk)-1 > MaxLineBytes:
				// Over the cap: dropped whole.
			case len(line) == 0:
				r.apply(chunk)
			default:
				r.apply(append(line, chunk...))
			}
			r.offset += n
			line = line[:0]
		case errors.Is(err, bufio.ErrBufferFull):
			// The line goes on past the buffer: keep the piece, unless
			// that would pass the cap, in which case everything seen so
			// far is discarded and the rest of the line skipped as it
			// arrives.
			if r.skipping {
				r.offset += int64(len(chunk))
				continue
			}
			if len(line)+len(chunk) > MaxLineBytes {
				r.offset += int64(len(line) + len(chunk))
				line = line[:0]
				r.skipping = true
				continue
			}
			line = append(line, chunk...)
		case errors.Is(err, io.EOF):
			// chunk is a trailing partial line (possibly empty): left
			// for the next call, unless it belongs to a line already
			// being skipped, whose bytes are discarded now.
			if r.skipping {
				r.offset += int64(len(chunk))
			}
			return nil
		default:
			return err
		}
	}
}

// apply folds one complete line into the facts. A line that is not JSON
// of the expected shape is skipped; a sidechain record and a synthetic
// assistant record are ignored; a
// model attachment resets the "assistant after attachment" flag; an
// assistant record naming a model sets it, and one carrying a usage
// whose three counts are non-negative replaces the context sum (a
// negative count is corruption: the previous sum stands).
func (r *Reader) apply(line []byte) {
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return
	}
	var rec record
	if err := json.Unmarshal(line, &rec, lenient); err != nil {
		return
	}
	if rec.IsSidechain {
		return
	}
	switch rec.Type {
	case typeAttachment:
		if rec.Attachment != nil && rec.Attachment.Type == attachmentModel && rec.Attachment.Identity.ModelID != "" {
			r.attModel = rec.Attachment.Identity.ModelID
			r.asstAfterAtt = false
		}
	case typeAssistant:
		if rec.Message == nil || rec.Message.Model == syntheticModel {
			return
		}
		if rec.Message.Model != "" {
			r.asstModel = rec.Message.Model
			r.asstAfterAtt = true
		}
		if u := rec.Message.Usage; u != nil && u.InputTokens >= 0 && u.CacheCreationInputTokens >= 0 && u.CacheReadInputTokens >= 0 {
			r.tokens = addSaturating(addSaturating(u.InputTokens, u.CacheCreationInputTokens), u.CacheReadInputTokens)
			r.hasContext = true
		}
	}
}

// addSaturating adds two non-negative counts and saturates at math.MaxInt
// instead of wrapping, so a corrupt usage can make the sum meaningless
// but never negative — a heartbeat carrying a negative count would be
// refused whole, lease renewal included.
func addSaturating(a, b int) int {
	if s := a + b; s >= a {
		return s
	}
	return math.MaxInt
}

// facts applies the model rule to the state so far: the bare assistant id
// when no attachment was seen; the assistant id when one came after the
// latest attachment and the attachment's full id does not begin with it
// (a switch the attachment missed); else the attachment's full id, "[1m]"
// suffix and all.
func (r *Reader) facts() Facts {
	f := Facts{ContextUsedTokens: r.tokens, HasContext: r.hasContext}
	switch {
	case r.attModel == "":
		f.Model = r.asstModel
	case r.asstAfterAtt && !strings.HasPrefix(r.attModel, r.asstModel):
		f.Model = r.asstModel
	default:
		f.Model = r.attModel
	}
	return f
}

// reset starts the reader over: the file was truncated or replaced.
func (r *Reader) reset() {
	*r = Reader{path: r.path}
}
