package watch

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
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
