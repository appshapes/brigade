package watch

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"os"
)

// rollouts is Codex CLI's history (config.HistoryRollouts; card 117):
// one rollout file a session under sessions/YYYY/MM/DD, whose name ends
// with the thread id, and an index of thread names beside them.
type rollouts struct{}

func (rollouts) filedUnder(*watcher) string { return "" }

func (rollouts) findLog(root *os.File, id string) string { return findDatedLog(root, id, false) }

func (h rollouts) refresh(r *hostFactsReader) localHostFacts {
	return r.refreshPinned(func(root *os.File) {
		if r.path == "" {
			r.path = h.findLog(root, r.id)
		}
		if r.path != "" {
			// Verify a Codex rollout's own session_meta.id before trusting any
			// token_count record (which does not repeat the ID).
			verified := false
			r.refreshLog(root, func(line []byte) {
				var meta struct {
					Type    string `json:"type"`
					Payload struct {
						ID string `json:"id"`
					} `json:"payload"`
				}
				if json.Unmarshal(line, &meta, hostJSON) == nil && meta.Type == "session_meta" {
					verified = meta.Payload.ID == r.id
				}
			}, func(line []byte) {
				if verified {
					r.applyCodex(line)
				}
			})
		}
		r.refreshIndex(root)
	})
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
