package protocol

import (
	"bufio"
	"bytes"
	"encoding/json/v2"
	"errors"
	"io"
	"sync"
)

// This file is the one NDJSON reader and writer of plan 4.4.9 and 7.3,
// shared by the watcher, the conformance suite and `message watch`'s
// stdin command loop.

// MaxLineBytes is the NDJSON line cap: a line whose content exceeds
// 1 MiB is dropped by the reader with ErrLineTooLong and reading
// continues (4.4.9).
const MaxLineBytes = 1 << 20

// ErrLineTooLong marks one over-long NDJSON line that was discarded to
// its end. It is a per-line condition, not a stream failure: the caller
// logs a warning and calls Next again for the line after it. This is
// exactly why the reader is built on bufio.Reader.ReadSlice rather than
// bufio.Scanner, whose ErrTooLong is permanent (7.3).
var ErrLineTooLong = errors.New("ndjson line exceeds 1 MiB and was dropped")

// errRawNewline guards the one-line invariant of the writer. The json
// codec always escapes \n and \r inside strings and emits no whitespace
// between tokens, so this is defence in depth, not a reachable path for
// protocol shapes.
var errRawNewline = errors.New("ndjson: encoded value contains a raw newline")

// A LineReader delivers NDJSON lines of up to MaxLineBytes. A longer
// line is discarded to its end and reported as ErrLineTooLong, and
// reading continues with the next line.
type LineReader struct {
	r   *bufio.Reader
	buf []byte
}

// NewLineReader wraps r. The internal buffer starts small; lines
// accumulate across bufio.ErrBufferFull chunks up to MaxLineBytes.
func NewLineReader(r io.Reader) *LineReader {
	return &LineReader{r: bufio.NewReaderSize(r, 64*1024)}
}

// Next returns the next line with its terminator (and any preceding
// '\r') removed. The returned slice is valid only until the next call.
//
//   - (line, nil): a line was delivered; a zero-length line is an empty
//     line, which receivers skip (adapters never emit one, 4.4.9).
//   - (nil, ErrLineTooLong): one over-long line was discarded whole;
//     log a warning and call Next again — reading continues.
//   - (nil, io.EOF): end of stream. A final line with no terminator is
//     delivered first.
//   - (nil, err): a transport error from the underlying reader.
func (lr *LineReader) Next() ([]byte, error) {
	lr.buf = lr.buf[:0]
	overlong := false
	for {
		chunk, err := lr.r.ReadSlice('\n')
		if !overlong {
			// The '\n' terminator may ride along in the final chunk,
			// so allow content plus one terminator byte.
			if len(lr.buf)+len(chunk) > MaxLineBytes+1 {
				overlong = true
				lr.buf = lr.buf[:0]
			} else {
				lr.buf = append(lr.buf, chunk...)
			}
		}
		switch {
		case err == nil:
			// The line ended; the next call starts at the next line.
			if overlong {
				return nil, ErrLineTooLong
			}
			return trimLineEnding(lr.buf), nil
		case errors.Is(err, bufio.ErrBufferFull):
			// Mid-line; keep accumulating (or keep discarding).
			continue
		case errors.Is(err, io.EOF):
			if overlong {
				return nil, ErrLineTooLong
			}
			if len(lr.buf) == 0 {
				return nil, io.EOF
			}
			// A final line without a terminator is still a line.
			return trimLineEnding(lr.buf), nil
		default:
			return nil, err
		}
	}
}

// trimLineEnding removes one trailing '\n' and, before it, one
// tolerated '\r'. Compact JSON never ends in a raw control character,
// so this cannot eat content.
func trimLineEnding(b []byte) []byte {
	if n := len(b); n > 0 && b[n-1] == '\n' {
		b = b[:n-1]
	}
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return b
}

// A LineWriter emits one JSON document per line: jsonv2 marshalling
// plus a trailing '\n', written as a single Write call behind a mutex
// so concurrent events never interleave (7.3).
type LineWriter struct {
	mu sync.Mutex
	w  io.Writer
}

// NewLineWriter wraps w, which is typically the watch command's stdout.
func NewLineWriter(w io.Writer) *LineWriter {
	return &LineWriter{w: w}
}

// WriteLine marshals v and writes it as exactly one '\n'-terminated
// line. The codec always escapes \n and \r inside strings, so a body
// containing newlines (or U+2028/U+2029, which are not newline bytes)
// cannot produce a second line (C-39, U-17); a marshalling that
// somehow embedded a raw newline is refused rather than emitted.
func (lw *LineWriter) WriteLine(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if bytes.IndexByte(data, '\n') >= 0 {
		return errRawNewline
	}
	data = append(data, '\n')
	lw.mu.Lock()
	defer lw.mu.Unlock()
	_, err = lw.w.Write(data)
	return err
}
