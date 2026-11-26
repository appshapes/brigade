package watch

// Card 117: host history is a best-effort LOCAL source, never a hook input
// or an adapter input. Grok's title and model are the exception the hook
// also reads, so a registration carries the name and model the watcher
// will keep publishing (cards 132 and 135). See
// docs/research/host-session-facts.md for the measured formats and limits.
// Only a sanitised name, Grok's sanitised model id and a token count escape
// this reader; neither paths, native IDs nor raw errors are logged.

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
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/usage"
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
	// model is Grok's current_model_id. Codex and Muse leave it empty:
	// their model still comes from the hook-written map.
	model string
	// use is the usage the host's record gives (work item 347): the
	// tokens, the context window, the cost, the limits and the extra
	// usage, each set where the record has it. It stays on this machine
	// (usage.Record); nothing of it rides a heartbeat.
	use hostUse
}

// hostUse is the part of a usage record a host's own record gives.
type hostUse struct {
	Total, Last                usage.Tokens
	Calls                      usage.Value[int64]
	ContextWindow, ContextUsed usage.Value[int64]
	Cost                       usage.Value[usage.Cost]
	Limits                     usage.Value[usage.Limits]
	LimitsAt                   time.Time
	Extra                      usage.Value[usage.Extra]
}

type hostFactsReader struct {
	host config.Host
	// kind names the history to read where the caller knows it and no
	// host: "" means the host's own (config.Host.History).
	kind                         config.HistoryKind
	root, id, path, cwd          string
	facts                        localHostFacts
	rootInfo, logInfo, indexInfo os.FileInfo
	// The last run configuration is enough for the sequential foreground
	// run. A completion from another run needs its explicit prompt_tokens.
	runID, provider string
	// The token sums of a host whose record gives each call alone (Muse
	// Code's journal; work item 347): the offset counted up to, the file
	// it was in, and the sums so far. refreshLog reads windows again, so
	// sums are made here, each complete line once.
	sumOffset int64
	sumInfo   os.FileInfo
	sums      hostSums
	// usageInfo and signalsInfo are Grok's usage.json and signals.json as
	// last read, and grokOwned whether the summary beside them names this
	// session.
	usageInfo, signalsInfo os.FileInfo
	grokOwned              bool
	// codexRecords: the rollout has token_usage_record lines, whose
	// running totals stand over token_count's.
	codexRecords bool
}

// hostSums are the token sums of a record read once, line by line.
type hostSums struct {
	total, last usage.Tokens
	calls       int64
}

// A history is how one host's own record of a session is read (work item
// 381): one implementation for each way a host keeps it, in a
// file of its own, chosen from the host table by its kind
// (config.Host.History). The reader around it is shared: the bounded
// reads, the root that is pinned across polls, and what may leave it.
type history interface {
	// refresh brings r.facts up to date from the host's record and
	// returns them. r.root is set and r.id is a session id.
	refresh(r *hostFactsReader) localHostFacts
	// filedUnder is the directory the watcher's session is filed under
	// in the record, "" for a history that files by the id alone.
	filedUnder(w *watcher) string
}

// histories holds the implementations, each under the kind the table
// names it by. A host whose kind is not here has no local facts.
var histories = map[config.HistoryKind]history{
	config.HistoryDatedStore: datedStore{},
	config.HistoryRollouts:   rollouts{},
	config.HistorySummaries:  summaries{},
}

// history is the reader's: its own kind where the caller named one, else
// its host's. Nil for a host that keeps no record Brigade reads.
func (r *hostFactsReader) history() history {
	kind := r.kind
	if kind == "" {
		kind = r.host.History()
	}
	return histories[kind]
}

func (w *watcher) pointedFacts(id string) localHostFacts {
	w.transcriptMu.Lock()
	defer w.transcriptMu.Unlock()
	host := config.Host(w.harness)
	root := hostHistoryRoot(host, w.environ)
	cwd := ""
	if h := histories[host.History()]; h != nil {
		cwd = h.filedUnder(w)
	}
	r := &w.hostFacts
	if r.host != host || r.root != root || r.id != id || r.cwd != cwd {
		*r = hostFactsReader{host: host, root: root, id: id, cwd: cwd}
	}
	return r.refresh()
}

// hostHistoryRoot is where the host keeps its own record of its sessions:
// the table's (config.Host.HistoryRoot).
func hostHistoryRoot(host config.Host, environ []string) string {
	return host.HistoryRoot(environ)
}

func (r *hostFactsReader) refresh() localHostFacts {
	if r.root == "" || !hostSessionID(r.id) {
		return localHostFacts{}
	}
	h := r.history()
	if h == nil {
		return localHostFacts{}
	}
	return h.refresh(r)
}

// refreshPinned opens the history's root, holds it to the directory it
// was at the first poll, and has read bring the facts up to date from it.
// A root that cannot be opened, or that was replaced, leaves the last
// facts as they are.
func (r *hostFactsReader) refreshPinned(read func(root *os.File)) localHostFacts {
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
	read(root)
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
//
// A history files a session's log in one of two ways under the day:
// direct, as <day>/<id>/session.jsonl, or as a rollout file among the
// day's entries whose name ends with the id.
func findDatedLog(root *os.File, id string, direct bool) string {
	budget := hostDiscoveryEntries
	var visit func(string, int) string
	visit = func(dir string, depth int) string {
		if budget <= 0 {
			return ""
		}
		if depth == 3 && direct {
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

// refreshLog reads the session's log where it changed since the last
// poll: head is given each line of its first bytes and tail each line of
// its last, in that order. A log that was replaced or cut resets the
// facts before either is called.
func (r *hostFactsReader) refreshLog(root *os.File, head, tail func(line []byte)) {
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
		r.codexRecords = false
		r.logInfo = nil
	}
	readHostLines(f, 0, min(fi.Size(), hostHeadBytes), head)
	start := max(0, fi.Size()-hostTailBytes)
	readHostLines(f, start, min(fi.Size(), hostTailBytes), tail)
	r.logInfo = fi
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
