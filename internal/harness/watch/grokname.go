package watch

import (
	"encoding/json/v2"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

const grokSummaryBytes = 1 << 20

// GrokSessionFacts is the sanitized title and model of one Grok session.
// The title is generated_title, or "" when that session has none yet. The
// model is current_model_id, or "" when the summary names none. Both come
// from that session's own summary.json. Chat history is not opened.
func GrokSessionFacts(environ []string, cwd, id string) (name, model string) {
	r := hostFactsReader{host: config.HostGrok, root: grokHistoryRoot(environ), id: id, cwd: cwd}
	facts := r.refresh()
	return facts.name, facts.model
}

// GrokSessionName is the sanitized title of one Grok session, or "" when
// that session has no title yet.
func GrokSessionName(environ []string, cwd, id string) string {
	name, _ := GrokSessionFacts(environ, cwd, id)
	return name
}

// GrokEncodedCwd is the session-group directory name for a launch cwd whose
// encoding fits in one file name. Empty when the cwd is not a usable path
// or the encoding would exceed 255 bytes; that case is found by .cwd.
func GrokEncodedCwd(cwd string) string {
	if !grokLaunchCwd(cwd) {
		return ""
	}
	enc := grokEncodeCwd(cwd)
	if len(enc) > 255 || strings.Contains(enc, "/") {
		return ""
	}
	return enc
}

func grokHistoryRoot(environ []string) string {
	if dir := adapterkit.Getenv(environ, "GROK_HOME"); dir != "" {
		if filepath.IsAbs(dir) {
			return filepath.Clean(dir)
		}
		// A relative override must not be joined onto HOME and read some
		// other tree. The same rule as CODEX_HOME.
		return ""
	}
	home := adapterkit.HomeDir(environ)
	if home == "" {
		return ""
	}
	return filepath.Join(home, ".grok")
}

func grokCwdOf(m *sessionmap.ByPID) string {
	if m == nil || m.Grok == nil {
		return ""
	}
	if m.Grok.LaunchCwd != "" {
		return m.Grok.LaunchCwd
	}
	return m.Grok.Cwd
}

func grokLaunchCwd(cwd string) bool {
	return filepath.IsAbs(cwd) && !strings.ContainsRune(cwd, 0) && !strings.ContainsAny(cwd, "\r\n")
}

// Percent-encode every byte outside RFC 3986 unreserved, uppercase hex.
// Measured against Grok Build 1.0.67: the group directory is this encoding
// of the launch cwd, so "/" is "%2F". Go's PathEscape leaves ":@+$&=" raw;
// Grok does not.
func grokEncodeCwd(cwd string) string {
	const hexDigits = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(cwd))
	for i := 0; i < len(cwd); i++ {
		c := cwd[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexDigits[c>>4])
		b.WriteByte(hexDigits[c&0x0F])
	}
	return b.String()
}

func (r *hostFactsReader) refreshGrokFacts() localHostFacts {
	if !grokLaunchCwd(r.cwd) {
		return r.facts
	}
	root, info := openHostRoot(r.root)
	if root == nil {
		return r.facts
	}
	defer func() { _ = root.Close() }()
	if r.rootInfo != nil && !os.SameFile(r.rootInfo, info) {
		return r.facts
	}
	r.rootInfo = info
	path := r.grokSummaryPath(root)
	if path == "" {
		return r.facts
	}
	f, fi := openHostPath(root, path, false)
	if f == nil {
		// A restored summary may reuse the same inode, size and timestamp, and
		// the group may have been renamed: require a fresh read and a fresh
		// discovery, exactly as the journal reader does.
		r.logInfo, r.path = nil, ""
		return r.facts
	}
	defer func() { _ = f.Close() }()
	if unchangedHostFile(r.logInfo, fi) {
		return r.facts
	}
	if r.logInfo != nil && (!os.SameFile(r.logInfo, fi) || fi.Size() < r.logInfo.Size()) {
		r.facts = localHostFacts{}
	}
	r.logInfo = fi
	if fi.Size() <= 0 || fi.Size() > grokSummaryBytes {
		return r.facts
	}
	buf := make([]byte, fi.Size())
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return r.facts
	}
	var doc struct {
		Info struct {
			ID string `json:"id"`
		} `json:"info"`
		Title string `json:"generated_title"`
		Model string `json:"current_model_id"`
	}
	if json.Unmarshal(buf[:n], &doc, hostJSON) != nil {
		return r.facts
	}
	if doc.Info.ID != r.id {
		// A summary that names another session is not evidence for this one.
		r.facts = localHostFacts{}
		return r.facts
	}
	if name := oneLineName(doc.Title); name != "" {
		r.facts.name = name
	}
	// An empty model leaves the last one. A summary that omits the field,
	// or sanitises to nothing, is not a change of model.
	if model := oneLineModel(doc.Model); model != "" {
		r.facts.model = model
	}
	return r.facts
}

// grokSummaryPath caches the resolved path in r.path, as the journal reader
// does: the long-cwd arm walks every sessions child, which must not happen
// once per heartbeat. The cache is per (root, id, cwd) and an open failure
// drops it; info.id, not the path, is what proves the title is this session's.
func (r *hostFactsReader) grokSummaryPath(root *os.File) string {
	if r.path != "" {
		return r.path
	}
	if enc := GrokEncodedCwd(r.cwd); enc != "" {
		r.path = filepath.Join("sessions", enc, r.id, "summary.json")
		return r.path
	}
	// An empty encoding here can only be the over-255-byte case: the caller
	// has already established a usable launch cwd, and the encoding never
	// contains "/". So fall through to the slug-plus-hash group, without
	// encoding the path a second time to decide nothing.
	if dir := r.grokLongGroup(root); dir != "" {
		r.path = filepath.Join("sessions", dir, r.id, "summary.json")
	}
	return r.path
}

// grokLongGroup finds the one sessions child whose .cwd file is this cwd.
// Two matches are not a guess. The hash Grok uses for the directory name
// is not reimplemented.
func (r *hostFactsReader) grokLongGroup(root *os.File) string {
	f, _ := openHostPath(root, "sessions", true)
	if f == nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	budget := hostDiscoveryEntries
	var match string
	for budget > 0 {
		n := min(128, budget)
		batch, err := f.ReadDir(n)
		budget -= len(batch)
		for _, e := range batch {
			name := e.Name()
			if name == "." || name == ".." || strings.Contains(name, "/") || !e.IsDir() {
				continue
			}
			body, ok := readGrokCwdFile(root, name)
			if !ok || !grokCwdFileMatches(body, r.cwd) {
				continue
			}
			if match != "" {
				return ""
			}
			match = name
		}
		if err != nil {
			break
		}
	}
	return match
}

func readGrokCwdFile(root *os.File, group string) ([]byte, bool) {
	f, fi := openHostPath(root, filepath.Join("sessions", group, ".cwd"), false)
	if f == nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	if fi.Size() <= 0 || fi.Size() > 4096 {
		return nil, false
	}
	buf := make([]byte, fi.Size())
	n, err := f.ReadAt(buf, 0)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, false
	}
	return buf[:n], true
}

func grokCwdFileMatches(body []byte, cwd string) bool {
	s := string(body)
	return s == cwd || s == cwd+"\n"
}
