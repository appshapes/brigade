package watch

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"os"

	"github.com/appshapes/brigade/internal/harness/usage"
)

// datedStore is Muse Code's history (config.HistoryDatedStore; card 117):
// sessions/YYYY/MM/DD/<id>/session.jsonl, a log of envelopes that each
// repeat the stream's id, so every fact is checked for itself.
type datedStore struct{}

func (datedStore) filedUnder(*watcher) string { return "" }

func (datedStore) findLog(root *os.File, id string) string { return findDatedLog(root, id, true) }

func (h datedStore) refresh(r *hostFactsReader) localHostFacts {
	return r.refreshPinned(func(root *os.File) {
		if r.path == "" {
			r.path = h.findLog(root, r.id)
		}
		if r.path != "" {
			r.refreshLog(root, func(line []byte) {
				// The head is read at the log's first read alone, and again
				// after a log that was replaced: refreshLog has cleared
				// logInfo by then.
				if r.logInfo == nil {
					r.applyMuse(line)
				}
			}, r.applyMuse)
			// After refreshLog, which clears the facts of a replaced log.
			r.sumMuse(root)
		}
	})
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

// museSumBytes caps what one poll reads of the journal for the sums; the
// next poll goes on from there.
const museSumBytes = 64 << 20

// sumMuse adds the usage of each model completion appended to the journal
// since the last poll (work item 347): every complete line once, from the
// offset it reached. A journal that was replaced or cut is counted again
// from its start.
func (r *hostFactsReader) sumMuse(root *os.File) {
	f, fi := openHostPath(root, r.path, false)
	if f == nil {
		return
	}
	defer func() { _ = f.Close() }()
	if r.sumInfo != nil && (!os.SameFile(r.sumInfo, fi) || fi.Size() < r.sumOffset) {
		r.sumOffset, r.sums = 0, hostSums{}
	}
	r.sumInfo = fi
	size := min(fi.Size()-r.sumOffset, museSumBytes)
	if size <= 0 {
		r.publishSums()
		return
	}
	b := make([]byte, size)
	n, err := f.ReadAt(b, r.sumOffset)
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
	b = b[:n]
	for {
		i := bytes.IndexByte(b, '\n')
		if i < 0 {
			break
		}
		if i <= hostLineBytes {
			r.sumMuseLine(b[:i])
		}
		r.sumOffset += int64(i) + 1
		b = b[i+1:]
	}
	// A line longer than one poll's read is passed over whole.
	if r.sumOffset+museSumBytes <= fi.Size() && len(b) == int(size) {
		r.sumOffset += int64(len(b))
	}
	r.publishSums()
}

// sumMuseLine adds one completion's usage.
func (r *hostFactsReader) sumMuseLine(line []byte) {
	var rec struct {
		Type   string `json:"payload_type"`
		Stream struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"stream"`
		Payload struct {
			Kind  string `json:"kind"`
			Event struct {
				Kind  string `json:"kind"`
				Usage *struct {
					Input      jsontext.Value `json:"input_tokens"`
					Prompt     jsontext.Value `json:"prompt_tokens"`
					Output     jsontext.Value `json:"output_tokens"`
					CacheRead  jsontext.Value `json:"cache_read_tokens"`
					CacheWrite jsontext.Value `json:"cache_write_tokens"`
					Reasoning  jsontext.Value `json:"reasoning_tokens"`
				} `json:"usage"`
			} `json:"event"`
		} `json:"payload"`
	}
	if json.Unmarshal(line, &rec, hostJSON) != nil || rec.Stream.Kind != "session" || rec.Stream.ID != r.id ||
		rec.Type != "runtime.session" || rec.Payload.Kind != "run" || rec.Payload.Event.Kind != "model_completed" || rec.Payload.Event.Usage == nil {
		return
	}
	u := rec.Payload.Event.Usage
	in, ok := hostTokenCount(u.Input)
	if !ok {
		if in, ok = hostTokenCount(u.Prompt); !ok {
			return
		}
	}
	out, ok := hostTokenCount(u.Output)
	if !ok {
		return
	}
	call := usage.Tokens{Output: usage.Of(int64(out))}
	// Muse Code's input includes the cache reads (a session's second call
	// on 2026-11-26: input 44,859, cache read 42,481): the input not read
	// from the cache is what is left, and is not known without the split.
	switch cr, ok := hostTokenCount(u.CacheRead); {
	case ok && cr <= in:
		call.Input, call.CacheRead = usage.Of(int64(in-cr)), usage.Of(int64(cr))
	case ok:
		call.Input, call.CacheRead = usage.Missing[int64](usage.BadShape), usage.Missing[int64](usage.BadShape)
	default:
		call.Input, call.CacheRead = usage.Missing[int64](usage.NotGiven), usage.Missing[int64](usage.NotGiven)
	}
	for _, v := range []struct {
		raw jsontext.Value
		to  *usage.Value[int64]
	}{{u.CacheWrite, &call.CacheWrite}, {u.Reasoning, &call.Reasoning}} {
		if n, ok := hostTokenCount(v.raw); ok {
			*v.to = usage.Of(int64(n))
		} else {
			*v.to = usage.Missing[int64](usage.NotGiven)
		}
	}
	r.sums.total = addTokens(r.sums.total, call)
	r.sums.last = call
	r.sums.calls++
}

// publishSums puts the sums into the facts the watcher reads.
func (r *hostFactsReader) publishSums() {
	if r.sums.calls == 0 {
		return
	}
	r.facts.use.Total, r.facts.use.Last = r.sums.total, r.sums.last
	r.facts.use.Calls = usage.Of(r.sums.calls)
}

// addTokens adds call to total, value by value: a count that either one
// lacks stays as total had it, and the first call's reason stands.
func addTokens(total, call usage.Tokens) usage.Tokens {
	add := func(t, c usage.Value[int64]) usage.Value[int64] {
		switch {
		case !c.Known():
			if t.Known() || t.Unavailable != "" {
				return t
			}
			return c
		case !t.Known():
			if t.Unavailable != "" {
				return t
			}
			return c
		}
		s := *t.V + *c.V
		if s < *t.V {
			s = math.MaxInt64
		}
		return usage.Of(s)
	}
	return usage.Tokens{
		Input: add(total.Input, call.Input), Output: add(total.Output, call.Output),
		CacheRead: add(total.CacheRead, call.CacheRead), CacheWrite: add(total.CacheWrite, call.CacheWrite),
		Reasoning: add(total.Reasoning, call.Reasoning),
	}
}
