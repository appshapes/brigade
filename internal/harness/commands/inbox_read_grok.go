package commands

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
)

const grokReadBudget = 2048

// Cursors select a location, never grant access. Every call resolves the exact
// receiver again and verifies the complete stored record before returning data.
type grokCursor struct {
	Version  int    `json:"v"`
	Receiver string `json:"receiver"`
	File     string `json:"file"`
	Record   string `json:"record"`
	Offset   int    `json:"offset"`
}
type grokReadPage struct {
	Untrusted     bool   `json:"untrusted_external_content"`
	MessageID     string `json:"message_id"`
	Digest        string `json:"frame_sha256"`
	Length        int    `json:"frame_bytes"`
	Offset        int    `json:"offset"`
	Next          int    `json:"next_offset"`
	FrameComplete bool   `json:"frame_complete"`
	CaughtUp      bool   `json:"caught_up"`
	Cursor        string `json:"next_cursor"`
	Chunk         string `json:"frame_chunk"`
	PageComplete  bool   `json:"page_complete"`
}

func cursorEncode(c grokCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}
func digestBytes(b []byte) string { sum := sha256.Sum256(b); return hex.EncodeToString(sum[:]) }
func receiverDigest(f *follower) string {
	m := f.binding
	b, _ := json.Marshal([]any{m.ClaudePID, m.ClaudeSessionID, m.BrigadeSessionID, m.Grok.Cwd, m.Grok.ProcessStart, m.Grok.Incarnation})
	return digestBytes(b)
}

// Only locally injected records are readable. Never backend receive, held
// messages, arbitrary file paths, histories, policy changes or ACKs.
func inboxReadGrok(inv Invocation, args []string, token string) error {
	if config.HostOf(inv.Environ) != config.HostGrok || len(args) != 0 {
		return usage("Grok inbox read takes only --cursor from its previous response")
	}
	state, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	m, err := config.Session(inv.Environ, state)
	if err != nil {
		return err
	}
	f := &follower{inv: inv, stateDir: state, sessionID: m.BrigadeSessionID, binding: m}
	defer f.closeFile()
	if !f.receiverValid() {
		return notRegisteredFollow()
	}
	binding := receiverDigest(f)
	cursor := grokCursor{Version: 1, Receiver: binding}
	if token != "" {
		if len(token) > 1024 {
			return usage("invalid inbox cursor")
		}
		b, e := base64.RawURLEncoding.DecodeString(token)
		if e != nil || json.Unmarshal(b, &cursor) != nil || cursor.Version != 1 || cursor.Receiver != binding || cursor.Offset < 0 || cursor.Offset > followMaxLineBytes || filepath.Base(cursor.File) != cursor.File || !strings.HasSuffix(cursor.File, ".ndjson") || len(cursor.Record) != 64 {
			return usage("invalid or stale inbox cursor")
		}
	}
	dir := inbound.GrokSinkDir(state, m.BrigadeSessionID, m.Grok.Incarnation)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return errors.New("local inbox directory unavailable")
	}
	if len(entries) > 4096 {
		return errors.New("local inbox generation limit exceeded; no records discarded")
	}
	found := token == ""
	total := 0
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !strings.HasSuffix(entry.Name(), ".ndjson") {
			continue
		}
		if !found && entry.Name() < cursor.File {
			continue
		}
		if err := f.open(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
		reader := bufio.NewReaderSize(f.file, followMaxLineBytes+1)
		for {
			line, e := reader.ReadSlice('\n')
			total += len(line)
			if total > 64<<20 {
				return errors.New("local inbox scan limit exceeded; no records discarded")
			}
			if errors.Is(e, io.EOF) {
				if len(line) > 0 {
					return errors.New("incomplete local inbox record; reader paused")
				}
				break
			}
			if e != nil || len(line) > followMaxLineBytes {
				return errors.New("local inbox record exceeds reader limit")
			}
			var rec inbound.SinkRecord
			if json.Unmarshal(line, &rec) != nil || !utf8.ValidString(rec.Frame) {
				return errors.New("corrupt local inbox record; reader paused")
			}
			digest := digestBytes(line)
			offset := 0
			if !found {
				if entry.Name() != cursor.File || digest != cursor.Record {
					continue
				}
				found = true
				offset = cursor.Offset
				if offset > len(rec.Frame) || !utf8.ValidString(rec.Frame[:offset]) {
					return usage("invalid frame cursor boundary")
				}
				if offset == len(rec.Frame) {
					continue
				}
			}
			current := grokCursor{Version: 1, Receiver: binding, File: entry.Name(), Record: digest, Offset: offset}
			return emitGrokPage(f, current, rec)
		}
	}
	if !found {
		return errors.New("inbox cursor record is missing or changed; no cursor advanced")
	}
	if !f.receiverValid() {
		return notRegisteredFollow()
	}
	return boundedGrokPage(inv, grokReadPage{Untrusted: true, CaughtUp: true, Cursor: token, PageComplete: true})
}

func emitGrokPage(f *follower, c grokCursor, rec inbound.SinkRecord) error {
	// Shrink by code-point boundaries until the entire protocol envelope fits.
	end := min(c.Offset+1024, len(rec.Frame))
	for end > c.Offset && end < len(rec.Frame) && !utf8.RuneStart(rec.Frame[end]) {
		end--
	}
	for {
		next := c
		next.Offset = end
		page := grokReadPage{Untrusted: true, MessageID: rec.MessageID, Digest: digestBytes([]byte(rec.Frame)), Length: len(rec.Frame), Offset: c.Offset, Next: end, FrameComplete: end == len(rec.Frame), Cursor: cursorEncode(next), Chunk: rec.Frame[c.Offset:end], PageComplete: true}
		b, err := grokPageBytes(page)
		if err != nil {
			return err
		}
		if len(b) <= grokReadBudget {
			if !f.receiverValid() {
				return notRegisteredFollow()
			}
			return writeGrokBytes(f.inv.Out, b)
		}
		if end == c.Offset {
			return errors.New("inbox page metadata exceeds output budget")
		}
		_, size := utf8.DecodeLastRuneInString(rec.Frame[c.Offset:end])
		end -= size
	}
}
func grokPageBytes(page grokReadPage) ([]byte, error) {
	var out strings.Builder
	if err := writeJSON(&out, page); err != nil {
		return nil, err
	}
	return []byte(out.String()), nil
}
func boundedGrokPage(inv Invocation, page grokReadPage) error {
	b, err := grokPageBytes(page)
	if err != nil {
		return err
	}
	if len(b) > grokReadBudget {
		return errors.New("inbox page metadata exceeds output budget")
	}
	return writeGrokBytes(inv.Out, b)
}
func writeGrokBytes(out io.Writer, b []byte) error {
	if out == nil {
		return nil
	}
	n, err := out.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}
