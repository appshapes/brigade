package protocol

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestLineReaderDeliversLines(t *testing.T) {
	t.Parallel()
	in := "one\ntwo\nthree\n"
	lr := NewLineReader(strings.NewReader(in))
	var got []string
	for {
		line, err := lr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		got = append(got, string(line))
	}
	want := []string{"one", "two", "three"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestLineReaderFinalLineWithoutNewline(t *testing.T) {
	t.Parallel()
	lr := NewLineReader(strings.NewReader("a\nb"))
	first, err := lr.Next()
	if err != nil || string(first) != "a" {
		t.Fatalf("first line = %q, %v", first, err)
	}
	second, err := lr.Next()
	if err != nil || string(second) != "b" {
		t.Fatalf("second line = %q, %v", second, err)
	}
	if _, err := lr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF, got %v", err)
	}
}

func TestLineReaderTrimsCarriageReturn(t *testing.T) {
	t.Parallel()
	lr := NewLineReader(strings.NewReader("a\r\nb\r\n"))
	for _, want := range []string{"a", "b"} {
		line, err := lr.Next()
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		if string(line) != want {
			t.Fatalf("line = %q, want %q", line, want)
		}
	}
}

// TestLineReaderDropsOverlongAndContinues is a named acceptance test: a
// 2 MiB line is dropped with ErrLineTooLong and the NEXT line is still
// delivered. Asserting on the delivery of the following line — not
// merely that no fatal error came back — is the positive control: a
// reader that stopped permanently (bufio.Scanner's ErrTooLong) would
// fail here.
func TestLineReaderDropsOverlongAndContinues(t *testing.T) {
	t.Parallel()
	var in bytes.Buffer
	in.WriteString("before\n")
	in.WriteString(strings.Repeat("x", 2<<20)) // 2 MiB, well over 1 MiB
	in.WriteString("\n")
	in.WriteString("after\n")

	lr := NewLineReader(&in)

	first, err := lr.Next()
	if err != nil || string(first) != "before" {
		t.Fatalf("first line = %q, %v; want \"before\"", first, err)
	}

	if _, err := lr.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("over-long line: got err %v, want ErrLineTooLong", err)
	}

	// The whole point: reading CONTINUES.
	third, err := lr.Next()
	if err != nil {
		t.Fatalf("line after over-long: unexpected err %v", err)
	}
	if string(third) != "after" {
		t.Fatalf("line after over-long = %q, want \"after\" (reading did not continue)", third)
	}

	if _, err := lr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("want EOF after last line, got %v", err)
	}
}

// TestLineReaderOverlongAtEOF: an over-long final line with no trailing
// newline is reported as ErrLineTooLong, then EOF — it is not silently
// delivered as content.
func TestLineReaderOverlongAtEOF(t *testing.T) {
	t.Parallel()
	in := strings.NewReader(strings.Repeat("y", 2<<20))
	lr := NewLineReader(in)
	if _, err := lr.Next(); !errors.Is(err, ErrLineTooLong) {
		t.Fatalf("got %v, want ErrLineTooLong", err)
	}
	if _, err := lr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("got %v, want EOF", err)
	}
}

// TestLineReaderAcceptsMaxLengthLine: a line exactly at the 1 MiB cap is
// delivered, not dropped — the boundary is inclusive.
func TestLineReaderAcceptsMaxLengthLine(t *testing.T) {
	t.Parallel()
	line := strings.Repeat("z", MaxLineBytes)
	lr := NewLineReader(strings.NewReader(line + "\n"))
	got, err := lr.Next()
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if len(got) != MaxLineBytes {
		t.Fatalf("delivered %d bytes, want %d", len(got), MaxLineBytes)
	}
}

// TestLineReaderDropsMultipleOverlongInARow proves the discard state is
// reset per line: two consecutive over-long lines each report the error
// and a short line after them is still delivered.
func TestLineReaderDropsMultipleOverlongInARow(t *testing.T) {
	t.Parallel()
	var in bytes.Buffer
	in.WriteString(strings.Repeat("a", 2<<20) + "\n")
	in.WriteString(strings.Repeat("b", 2<<20) + "\n")
	in.WriteString("ok\n")
	lr := NewLineReader(&in)
	for i := 0; i < 2; i++ {
		if _, err := lr.Next(); !errors.Is(err, ErrLineTooLong) {
			t.Fatalf("line %d: got %v, want ErrLineTooLong", i, err)
		}
	}
	got, err := lr.Next()
	if err != nil || string(got) != "ok" {
		t.Fatalf("recovery line = %q, %v; want \"ok\"", got, err)
	}
}

// TestWriterHostileBodyEncodesToOneLine is a named acceptance test
// (C-39, U-17): a body containing \n, \r, U+2028 and U+2029 must not
// produce a second NDJSON line.
func TestWriterHostileBodyEncodesToOneLine(t *testing.T) {
	t.Parallel()
	env := &MessageEnvelope{
		ProtocolVersion:    ProtocolVersion,
		Kind:               KindText,
		MessageID:          "m1",
		TeamRef:            "t1",
		Sender:             Sender{PrincipalRef: "p1", SessionID: "s1", SessionName: "n1"},
		RecipientSessionID: "r1",
		Body:               "line one\nline two\rline three\u2028line four\u2029line five",
		HopCount:           0,
		CreatedAt:          time.Unix(0, 0).UTC(),
		DeliveryState:      DeliveryStateAccepted,
	}
	var buf bytes.Buffer
	w := NewLineWriter(&buf)
	if err := w.WriteLine(&WatchMessage{Event: EventMessage, Message: *env}); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}

	out := buf.Bytes()
	// Exactly one trailing newline, and it is the only newline.
	if n := bytes.Count(out, []byte{'\n'}); n != 1 {
		t.Fatalf("output has %d newlines, want exactly 1: %q", n, out)
	}
	if out[len(out)-1] != '\n' {
		t.Fatalf("output does not end in newline: %q", out)
	}
	if bytes.Contains(out, []byte{'\r'}) {
		t.Fatalf("raw carriage return leaked into output: %q", out)
	}

	// And feeding it back through the reader yields exactly one line
	// whose decoded body is the original, newlines intact.
	lr := NewLineReader(bytes.NewReader(out))
	line, err := lr.Next()
	if err != nil {
		t.Fatalf("reader Next: %v", err)
	}
	var back WatchMessage
	if err := Decode(line, &back); err != nil {
		t.Fatalf("decode round-trip: %v", err)
	}
	if back.Message.Body != env.Body {
		t.Fatalf("body changed across round trip:\n got %q\nwant %q", back.Message.Body, env.Body)
	}
	if _, err := lr.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("hostile body produced a second line: %v", err)
	}
}

func TestWriterMarshalsWatchEvents(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	w := NewLineWriter(&buf)
	if err := w.WriteLine(&WatchReady{Event: EventReady, ProtocolVersion: ProtocolVersion, SessionID: "s1", Mode: "push"}); err != nil {
		t.Fatalf("WriteLine: %v", err)
	}
	if !bytes.HasSuffix(buf.Bytes(), []byte{'\n'}) {
		t.Fatalf("event line not newline-terminated: %q", buf.Bytes())
	}
	var got WatchReady
	if err := Decode(bytes.TrimRight(buf.Bytes(), "\n"), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "push" || got.SessionID != "s1" {
		t.Fatalf("round trip lost data: %+v", got)
	}
}

// TestWriterIsSerialised: concurrent WriteLine calls never interleave —
// every byte sequence between two newlines is a complete, decodable
// event.
func TestWriterIsSerialised(t *testing.T) {
	t.Parallel()
	var buf syncBuffer
	w := NewLineWriter(&buf)
	const n = 50
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = w.WriteLine(&WatchStatus{Event: EventStatus, State: "live", Detail: strings.Repeat("d", 500)})
		}()
	}
	wg.Wait()
	lr := NewLineReader(bytes.NewReader(buf.Bytes()))
	count := 0
	for {
		line, err := lr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatalf("Next: %v", err)
		}
		var ev WatchStatus
		if err := Decode(line, &ev); err != nil {
			t.Fatalf("interleaved write: %v (line %q)", err, line)
		}
		count++
	}
	if count != n {
		t.Fatalf("read %d lines, want %d", count, n)
	}
}

// syncBuffer is a mutex-guarded bytes.Buffer so concurrent writers in
// the test do not race on the buffer itself (the race we test for is in
// LineWriter, not in the sink).
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

// TestLineReaderContentCapBoundary is the matrix the two P1-2 boundary defects
// slipped through. The cap of 4.4.9/7.3 is on the line's CONTENT, so the same
// content must be accepted or rejected identically however the line ends. The
// original reader measured the RAW byte count against a one-byte allowance,
// which spent the allowance twice on "\r\n" (dropping a legal at-cap line) and
// spent it on a terminator that was not there at EOF (delivering a line one
// byte over). Neither was visible to a test that only checked LF at the cap and
// a 2 MiB line far above it.
func TestLineReaderContentCapBoundary(t *testing.T) {
	t.Parallel()

	for _, size := range []struct {
		name string
		n    int
		want bool // true = the line must be delivered
	}{
		{"cap-1", MaxLineBytes - 1, true},
		{"cap", MaxLineBytes, true},
		{"cap+1", MaxLineBytes + 1, false},
	} {
		for _, term := range []struct {
			name string
			end  string
		}{
			{"LF", "\n"},
			{"CRLF", "\r\n"},
			{"EOF", ""},
		} {
			t.Run(size.name+"/"+term.name, func(t *testing.T) {
				t.Parallel()
				content := bytes.Repeat([]byte("x"), size.n)
				lr := NewLineReader(bytes.NewReader(append(append([]byte{}, content...), term.end...)))
				line, err := lr.Next()
				switch {
				case size.want && err != nil:
					t.Fatalf("%d bytes of content terminated by %s: err = %v, want it delivered",
						size.n, term.name, err)
				case size.want && len(line) != size.n:
					t.Fatalf("%d bytes of content terminated by %s: delivered %d bytes",
						size.n, term.name, len(line))
				case !size.want && !errors.Is(err, ErrLineTooLong):
					t.Fatalf("%d bytes of content terminated by %s: err = %v (line %d bytes), want ErrLineTooLong",
						size.n, term.name, err, len(line))
				}
			})
		}
	}
}

// TestLineReaderContinuesAfterEachOverlongTermination pins the property that
// makes ErrLineTooLong a per-LINE condition rather than a stream failure: the
// line after a dropped one must still arrive, for every terminator shape.
func TestLineReaderContinuesAfterEachOverlongTermination(t *testing.T) {
	t.Parallel()

	for _, term := range []struct {
		name string
		end  string
	}{
		{"LF", "\n"},
		{"CRLF", "\r\n"},
	} {
		t.Run(term.name, func(t *testing.T) {
			t.Parallel()
			var in []byte
			in = append(in, bytes.Repeat([]byte("x"), MaxLineBytes+1)...)
			in = append(in, term.end...)
			in = append(in, []byte(`{"after":true}`)...)
			in = append(in, term.end...)

			lr := NewLineReader(bytes.NewReader(in))
			if _, err := lr.Next(); !errors.Is(err, ErrLineTooLong) {
				t.Fatalf("first line: err = %v, want ErrLineTooLong", err)
			}
			line, err := lr.Next()
			if err != nil {
				t.Fatalf("second line: err = %v, want it delivered", err)
			}
			if string(line) != `{"after":true}` {
				t.Fatalf("second line = %q, want the line after the dropped one", line)
			}
		})
	}
}
