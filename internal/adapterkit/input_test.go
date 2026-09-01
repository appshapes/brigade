package adapterkit_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// pattern builds n bytes whose value depends on the offset, so that a
// reader that truncated or shifted the document could not still compare
// equal (the P1-2 lesson: boundary defects hide behind uniform payloads).
func pattern(n int) []byte {
	p := make([]byte, n)
	for i := range p {
		p[i] = byte('A' + i%23)
	}
	return p
}

// asProtocol unwraps err into a *protocol.Error or fails the test.
func asProtocol(t *testing.T, err error) *protocol.Error {
	t.Helper()
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("error is %T (%v), want *protocol.Error", err, err)
	}
	return perr
}

func TestReadInputBoundary(t *testing.T) {
	t.Parallel()
	t.Run("one byte under the cap", func(t *testing.T) {
		t.Parallel()
		in := pattern(adapterkit.MaxInputBytes - 1)
		got, err := adapterkit.ReadInput(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("ReadInput(%d bytes): %v", len(in), err)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("ReadInput returned %d bytes not equal to the %d-byte input", len(got), len(in))
		}
	})
	t.Run("exactly the cap", func(t *testing.T) {
		t.Parallel()
		in := pattern(adapterkit.MaxInputBytes)
		got, err := adapterkit.ReadInput(bytes.NewReader(in))
		if err != nil {
			t.Fatalf("ReadInput(%d bytes): %v", len(in), err)
		}
		if !bytes.Equal(got, in) {
			t.Fatalf("ReadInput returned %d bytes not equal to the %d-byte input", len(got), len(in))
		}
	})
	t.Run("one byte over the cap", func(t *testing.T) {
		t.Parallel()
		in := pattern(adapterkit.MaxInputBytes + 1)
		_, err := adapterkit.ReadInput(bytes.NewReader(in))
		perr := asProtocol(t, err)
		if perr.Code != protocol.CodeInvalidInput {
			t.Fatalf("code = %q, want invalid_input", perr.Code)
		}
		if got := perr.Code.Exit(); got != 3 {
			t.Fatalf("exit = %d, want 3", got)
		}
		if perr.Details["limit"] != strconv.Itoa(adapterkit.MaxInputBytes) || perr.Details["unit"] != "bytes" {
			t.Fatalf("details = %v, want limit=%d unit=bytes", perr.Details, adapterkit.MaxInputBytes)
		}
	})
}

func TestReadInputEmpty(t *testing.T) {
	t.Parallel()
	_, err := adapterkit.ReadInput(bytes.NewReader(nil))
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeInvalidInput {
		t.Fatalf("code = %q, want invalid_input", perr.Code)
	}
	if perr.Details["reason"] != "required" {
		t.Fatalf("details = %v, want reason=required", perr.Details)
	}
}

// errReader fails on the first read.
type errReader struct{}

func (errReader) Read([]byte) (int, error) { return 0, errors.New("raw transport text") }

func TestReadInputReadError(t *testing.T) {
	t.Parallel()
	_, err := adapterkit.ReadInput(errReader{})
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeInvalidInput {
		t.Fatalf("code = %q, want invalid_input", perr.Code)
	}
	if perr.Message == "raw transport text" {
		t.Fatalf("the underlying error text was echoed into the protocol message")
	}
}

// countingZeros yields zero bytes forever and counts what was consumed.
type countingZeros struct{ n int64 }

func (c *countingZeros) Read(p []byte) (int, error) {
	c.n += int64(len(p))
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

func TestReadInputBoundedConsumption(t *testing.T) {
	t.Parallel()
	src := &countingZeros{}
	_, err := adapterkit.ReadInput(src)
	perr := asProtocol(t, err)
	if perr.Code != protocol.CodeInvalidInput {
		t.Fatalf("code = %q, want invalid_input", perr.Code)
	}
	// io.ReadAll may OFFER a larger buffer than the limit; the limit
	// reader hands out at most cap+1 bytes, but the last Read call can
	// have been offered a full buffer. Allow one buffer of slack.
	const slack = 512 * 1024
	if src.n > adapterkit.MaxInputBytes+1+slack {
		t.Fatalf("ReadInput consumed %d bytes from an unbounded stream; the cap is %d", src.n, adapterkit.MaxInputBytes)
	}
}

func TestReadInputFromRegularFile(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "doc.json")
	doc := []byte(`{"x":1}`)
	if err := os.WriteFile(path, doc, 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	got, err := adapterkit.ReadInput(f)
	if err != nil {
		t.Fatalf("a regular file must not be refused as a terminal: %v", err)
	}
	if !bytes.Equal(got, doc) {
		t.Fatalf("got %q, want %q", got, doc)
	}
}

func TestReadInputFromPipe(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close() }()
	doc := []byte(`{"x":1}` + "\n")
	go func() {
		_, _ = w.Write(doc)
		_ = w.Close()
	}()
	got, err := adapterkit.ReadInput(r)
	if err != nil {
		t.Fatalf("a pipe must not be refused as a terminal: %v", err)
	}
	if !bytes.Equal(got, doc) {
		t.Fatalf("got %q, want %q", got, doc)
	}
}

// Guard against the reader growing an unexported dependency on the
// concrete type: a plain io.Reader wrapper must behave like the pipe.
func TestReadInputFromWrappedReader(t *testing.T) {
	t.Parallel()
	doc := []byte(`{"x":1}`)
	got, err := adapterkit.ReadInput(io.MultiReader(bytes.NewReader(doc)))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, doc) {
		t.Fatalf("got %q, want %q", got, doc)
	}
}
