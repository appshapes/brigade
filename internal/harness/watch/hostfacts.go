package watch

// Card 117: host history is a best-effort LOCAL source, never a hook input
// or an adapter input. Grok's title is the one exception the hook also
// reads, so a registration does not wipe a name the watcher already
// published (card 132). See docs/research/host-session-facts.md for the
// measured formats and limits. Only a sanitised name and a token count
// escape this reader; neither paths, native IDs nor raw errors are logged.

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
)

const (
	hostTailBytes        = 8 << 20
	hostHeadBytes        = 1 << 20
	hostLineBytes        = 1 << 20
	hostDiscoveryEntries = 8192
)

var hostJSON = json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))

type localHostFacts struct {
	name       string
	tokens     int
	hasContext bool
}

type hostFactsReader struct {
	host                         config.Host
	root, id, path, cwd          string
	facts                        localHostFacts
	rootInfo, logInfo, indexInfo os.FileInfo
	// The last run configuration is enough for the sequential foreground
	// run. A completion from another run needs its explicit prompt_tokens.
	runID, provider string
}

func (w *watcher) pointedFacts(id string) localHostFacts {
	w.transcriptMu.Lock()
	defer w.transcriptMu.Unlock()
	host := config.Host(w.harness)
	root := hostHistoryRoot(host, w.environ)
	cwd := ""
	if host == config.HostGrok {
		cwd = grokCwdOf(w.grokReceiver)
	}
	r := &w.hostFacts
	if r.host != host || r.root != root || r.id != id || r.cwd != cwd {
		*r = hostFactsReader{host: host, root: root, id: id, cwd: cwd}
	}
	return r.refresh()
}

func hostHistoryRoot(host config.Host, environ []string) string {
	switch host {
	case config.HostGrok:
		return grokHistoryRoot(environ)
	case config.HostClaude:
		return ""
	case config.HostCodex:
		if dir := adapterkit.Getenv(environ, "CODEX_HOME"); dir != "" {
			if filepath.IsAbs(dir) {
				return filepath.Clean(dir)
			}
			return ""
		}
		home := adapterkit.Getenv(environ, "HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		return filepath.Join(home, ".codex")
	case config.HostMuse:
		// Muse clears XDG_* from plugin hooks. As with Brigade's own
		// pointed-host directories, never trust an inherited XDG override.
		home := adapterkit.Getenv(environ, "HOME")
		if !filepath.IsAbs(home) {
			return ""
		}
		return filepath.Join(home, ".local", "share", "muse")
	default:
		return ""
	}
}

func (r *hostFactsReader) refresh() localHostFacts {
	if r.root == "" || !hostSessionID(r.id) {
		return localHostFacts{}
	}
	if r.host == config.HostGrok {
		return r.refreshGrokFacts()
	}
	if r.host != config.HostCodex && r.host != config.HostMuse {
		return localHostFacts{}
	}
	root, info := openHostRoot(r.root)
	if root == nil {
		return r.facts
	}
	defer func() { _ = root.Close() }()
	// Pin the configured directory's identity across polls. Never adopt a
	// replacement root, even if a cached path is invalidated and rediscovered.
	if r.rootInfo != nil && !os.SameFile(r.rootInfo, info) {
		return r.facts
	}
	r.rootInfo = info
	if r.path == "" {
		r.path = findHostLog(root, r.host, r.id)
	}
	if r.path != "" {
		r.refreshLog(root)
	}
	if r.host == config.HostCodex {
		r.refreshIndex(root)
	}
	return r.facts
}

// Both hosts use UUIDs. In particular, an untrusted native ID must never
// become a path fragment, a glob, or a prefix match for another session.
func hostSessionID(id string) bool {
	if len(id) != 36 {
		return false
	}
	for i, c := range id {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Search ONLY YYYY/MM/DD. Never descend into subagents, tool output,
// archives, arbitrary directories or SQLite stores. Directory entries
// share one budget, including non-matching entries; reads are batched.
func findHostLog(root *os.File, host config.Host, id string) string {
	budget := hostDiscoveryEntries
	var visit func(string, int) string
	visit = func(dir string, depth int) string {
		if budget <= 0 {
			return ""
		}
		if depth == 3 && host == config.HostMuse {
			path := filepath.Join(dir, id, "session.jsonl")
			if f, _ := openHostPath(root, path, false); f != nil {
				_ = f.Close()
				return path
			}
			return ""
		}
		f, _ := openHostPath(root, dir, true)
		if f == nil {
			return ""
		}
		defer func() { _ = f.Close() }()
		var entries []os.DirEntry
		for budget > 0 {
			n := min(128, budget)
			batch, readErr := f.ReadDir(n)
			budget -= len(batch)
			entries = append(entries, batch...)
			if readErr != nil {
				break
			}
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() > entries[j].Name() })
		for _, e := range entries {
			name := e.Name()
			if depth == 3 {
				if !e.IsDir() && strings.HasPrefix(name, "rollout-") && strings.HasSuffix(name, "-"+id+".jsonl") {
					return filepath.Join(dir, name)
				}
			} else if e.IsDir() && datePart(name, depth) {
				if found := visit(filepath.Join(dir, name), depth+1); found != "" {
					return found
				}
			}
		}
		return ""
	}
	return visit("sessions", 0)
}

func datePart(s string, depth int) bool {
	n := 2
	if depth == 0 {
		n = 4
	}
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func unchangedHostFile(old, current os.FileInfo) bool {
	return old != nil && os.SameFile(old, current) && old.Size() == current.Size() && old.ModTime().Equal(current.ModTime())
}

// readHostLines reads a bounded window. A partial first or final line is
// discarded, as are overlong lines; later well-formed lines still count.
// ReadAt bounds work even if the host keeps appending while we read.
func readHostLines(f *os.File, start, size int64, apply func([]byte)) {
	if size <= 0 {
		return
	}
	b := make([]byte, size)
	n, err := f.ReadAt(b, start)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	b = b[:n]
	if start > 0 {
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			b = b[i+1:]
		} else {
			return
		}
	}
	for len(b) > 0 {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			return
		}
		if i <= hostLineBytes {
			apply(b[:i])
		}
		b = b[i+1:]
	}
}

func (r *hostFactsReader) refreshIndex(root *os.File) {
	f, fi := openHostPath(root, "session_index.jsonl", false)
	if f == nil {
		// A restored index may reuse the same inode, size and timestamp.
		// Keep the last facts, but require a fresh read after an open failure.
		r.indexInfo = nil
		return
	}
	defer func() { _ = f.Close() }()
	if unchangedHostFile(r.indexInfo, fi) {
		return
	}
	readHostLines(f, max(0, fi.Size()-hostTailBytes), min(fi.Size(), hostTailBytes), func(line []byte) {
		var entry struct {
			ID   string `json:"id"`
			Name string `json:"thread_name"`
		}
		if json.Unmarshal(line, &entry, hostJSON) == nil && entry.ID == r.id {
			if name := oneLineName(entry.Name); name != "" {
				r.facts.name = name
			}
		}
	})
	r.indexInfo = fi
}

func (r *hostFactsReader) refreshLog(root *os.File) {
	f, fi := openHostPath(root, r.path, false)
	if f == nil {
		r.path = ""
		return
	}
	defer func() { _ = f.Close() }()
	if unchangedHostFile(r.logInfo, fi) {
		return
	}
	if r.logInfo != nil && (!os.SameFile(r.logInfo, fi) || fi.Size() < r.logInfo.Size()) {
		r.facts = localHostFacts{}
		r.indexInfo = nil
		r.runID, r.provider = "", ""
		r.logInfo = nil
	}
	// Verify a Codex rollout's own session_meta.id before trusting any
	// token_count record (which does not repeat the ID). Muse envelopes
	// repeat stream.id, so every fact is checked individually instead.
	verified := false
	readHostLines(f, 0, min(fi.Size(), hostHeadBytes), func(line []byte) {
		if r.host == config.HostCodex {
			var meta struct {
				Type    string `json:"type"`
				Payload struct {
					ID string `json:"id"`
				} `json:"payload"`
			}
			if json.Unmarshal(line, &meta, hostJSON) == nil && meta.Type == "session_meta" {
				verified = meta.Payload.ID == r.id
			}
		} else if r.logInfo == nil {
			r.applyMuse(line)
		}
	})
	start := max(0, fi.Size()-hostTailBytes)
	readHostLines(f, start, min(fi.Size(), hostTailBytes), func(line []byte) {
		if r.host == config.HostMuse {
			r.applyMuse(line)
		} else if verified {
			r.applyCodex(line)
		}
	})
	r.logInfo = fi
}

func (r *hostFactsReader) applyCodex(line []byte) {
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			Info *struct {
				Last *struct {
					Input jsontext.Value `json:"input_tokens"`
				} `json:"last_token_usage"`
			} `json:"info"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec, hostJSON) != nil || rec.Type != "event_msg" || rec.Payload.Type != "token_count" || rec.Payload.Info == nil || rec.Payload.Info.Last == nil {
		return
	}
	r.setTokens(rec.Payload.Info.Last.Input)
}

func (r *hostFactsReader) applyMuse(line []byte) {
	var rec struct {
		Type   string `json:"payload_type"`
		Stream struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"stream"`
		Payload struct {
			Kind      string `json:"kind"`
			RunID     string `json:"run_id"`
			SessionID string `json:"session_id"`
			Name      string `json:"new_name"`
			Record    struct {
				Provider  string `json:"provider_id"`
				RunStream struct {
					ID string `json:"id"`
				} `json:"run_stream"`
			} `json:"record"`
			Event struct {
				Kind  string `json:"kind"`
				Usage *struct {
					Input  jsontext.Value `json:"input_tokens"`
					Prompt jsontext.Value `json:"prompt_tokens"`
				} `json:"usage"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec, hostJSON) != nil || rec.Stream.Kind != "session" || rec.Stream.ID != r.id {
		return
	}
	switch rec.Type {
	case "session.name.changed":
		if rec.Payload.SessionID == r.id {
			if name := oneLineName(rec.Payload.Name); name != "" {
				r.facts.name = name
			}
		}
	case "run.model.configured":
		r.runID, r.provider = rec.Payload.Record.RunStream.ID, rec.Payload.Record.Provider
	case "runtime.session":
		p := rec.Payload
		if p.Kind != "run" || p.Event.Kind != "model_completed" || p.Event.Usage == nil {
			return
		}
		u := p.Event.Usage
		if len(u.Prompt) > 0 && string(u.Prompt) != "null" {
			r.setTokens(u.Prompt)
		} else if p.RunID != "" && p.RunID == r.runID && r.provider == "meta" {
			r.setTokens(u.Input)
		}
	}
}

func (r *hostFactsReader) setTokens(raw jsontext.Value) {
	if n, ok := hostTokenCount(raw); ok {
		r.facts.tokens, r.facts.hasContext = n, true
	}
}

// Accept only non-negative integer counters; saturate even an individual
// corrupt counter larger than a machine integer. The heartbeat omits a
// saturated value above the wire's 2^53-1 cap rather than losing its lease.
func hostTokenCount(raw jsontext.Value) (int, bool) {
	if len(raw) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range raw {
		if c < '0' || c > '9' {
			return 0, false
		}
		d := int(c - '0')
		if n > (math.MaxInt-d)/10 {
			n = math.MaxInt
		} else {
			n = n*10 + d
		}
	}
	return n, true
}
