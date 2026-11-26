package watch

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"math"
	"os"
	"time"

	"github.com/appshapes/brigade/internal/harness/usage"
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

// codexTokens is Codex's TokenUsage as a rollout writes it.
type codexTokens struct {
	Input      jsontext.Value `json:"input_tokens"`
	Cached     jsontext.Value `json:"cached_input_tokens"`
	CacheWrite jsontext.Value `json:"cache_write_input_tokens"`
	Output     jsontext.Value `json:"output_tokens"`
	Reasoning  jsontext.Value `json:"reasoning_output_tokens"`
	Total      jsontext.Value `json:"total_tokens"`
}

// codexWindow is one window of Codex's RateLimitSnapshot.
type codexWindow struct {
	Used     *float64 `json:"used_percent"`
	Minutes  *int64   `json:"window_minutes"`
	ResetsAt *int64   `json:"resets_at"`
}

func (r *hostFactsReader) applyCodex(line []byte) {
	var rec struct {
		Type    string `json:"type"`
		Payload struct {
			Type string `json:"type"`
			// token_count
			Info *struct {
				Total  *codexTokens `json:"total_token_usage"`
				Last   *codexTokens `json:"last_token_usage"`
				Window *int64       `json:"model_context_window"`
			} `json:"info"`
			Limits *struct {
				ID        *string      `json:"limit_id"`
				Primary   *codexWindow `json:"primary"`
				Secondary *codexWindow `json:"secondary"`
				Credits   *struct {
					Has       *bool   `json:"has_credits"`
					Unlimited *bool   `json:"unlimited"`
					Balance   *string `json:"balance"`
				} `json:"credits"`
				Spend *bool   `json:"spend_control_reached"`
				Plan  *string `json:"plan_type"`
			} `json:"rate_limits"`
			// token_usage_record
			Thread string       `json:"thread_id"`
			Usage  *codexTokens `json:"usage"`
			Totals *codexTokens `json:"thread_token_usage"`
		} `json:"payload"`
		At string `json:"timestamp"`
	}
	if json.Unmarshal(line, &rec, hostJSON) != nil {
		return
	}
	// token_usage_record is one completed response's usage and the thread's
	// running total, kept apart from the context Codex can mark full or
	// estimate (work item 347): the totals come from it whenever the
	// rollout has one.
	if rec.Type == "token_usage_record" {
		if rec.Payload.Thread != r.id {
			return
		}
		if t, ok := codexUsage(rec.Payload.Totals); ok {
			r.facts.use.Total, r.codexRecords = t, true
		}
		if l, ok := codexUsage(rec.Payload.Usage); ok {
			r.facts.use.Last = l
		}
		return
	}
	if rec.Type != "event_msg" || rec.Payload.Type != "token_count" {
		return
	}
	if info := rec.Payload.Info; info != nil {
		if info.Last != nil {
			// The context: the last response's input, or, for a context
			// Codex marked full or estimated without a call, its total.
			in, okIn := hostTokenCount(info.Last.Input)
			if total, ok := hostTokenCount(info.Last.Total); (!okIn || in == 0) && ok && total > 0 {
				r.setTokens(info.Last.Total)
			} else {
				r.setTokens(info.Last.Input)
			}
		}
		if info.Window != nil && *info.Window > 0 {
			r.facts.use.ContextWindow = usage.Of(*info.Window)
		}
		// Without token_usage_record, the event's totals: a context Codex
		// marked full carries zeros but its total_tokens
		// (fill_to_context_window), and the totals before it stand.
		if !r.codexRecords {
			if t, ok := codexUsage(info.Total); ok {
				r.facts.use.Total = t
			}
			if l, ok := codexUsage(info.Last); ok {
				r.facts.use.Last = l
			}
		}
	}
	// The plan's own windows: a snapshot of another quota bucket (a
	// limit_id other than codex) is not the plan's.
	if lim := rec.Payload.Limits; lim != nil && (lim.ID == nil || *lim.ID == "" || *lim.ID == "codex") {
		limits := usage.Limits{Windows: []usage.Limit{}}
		if lim.Plan != nil {
			limits.Plan = oneLineName(*lim.Plan)
		}
		for _, w := range []*codexWindow{lim.Primary, lim.Secondary} {
			if l, ok := codexLimit(w); ok {
				limits.Windows = append(limits.Windows, l)
			}
		}
		r.facts.use.Limits = usage.Of(limits)
		if at, err := time.Parse(time.RFC3339Nano, rec.At); err == nil && plausible(at) {
			r.facts.use.LimitsAt = at.UTC()
		}
		if c := lim.Credits; c != nil || lim.Spend != nil {
			var x usage.Extra
			if c != nil {
				x.HasCredits, x.Unlimited = c.Has, c.Unlimited
				if c.Balance != nil {
					x.Balance = oneLineName(*c.Balance)
				}
			}
			x.SpendControlReached = lim.Spend
			r.facts.use.Extra = usage.Of(x)
		}
	}
}

// codexUsage is one TokenUsage as Brigade counts it: input without the
// cached part, which is the cache read; false for a usage with no input
// and no output, which is either none or a context Codex marked full.
func codexUsage(t *codexTokens) (usage.Tokens, bool) {
	if t == nil {
		return usage.Tokens{}, false
	}
	in, ok1 := hostTokenCount(t.Input)
	out, ok2 := hostTokenCount(t.Output)
	if !ok1 || !ok2 || in+out == 0 {
		return usage.Tokens{}, false
	}
	var u usage.Tokens
	// Codex's input includes the cached input; without a cached count
	// that fits in it, the input not read from the cache is not known.
	cached, okc := hostTokenCount(t.Cached)
	if okc && cached <= in {
		u.Input, u.CacheRead = usage.Of(int64(in-cached)), usage.Of(int64(cached))
	} else {
		u.Input, u.CacheRead = usage.Missing[int64](usage.BadShape), usage.Missing[int64](usage.BadShape)
	}
	u.Output = usage.Of(int64(out))
	if n, ok := hostTokenCount(t.CacheWrite); ok {
		u.CacheWrite = usage.Of(int64(n))
	} else {
		u.CacheWrite = usage.Missing[int64](usage.NotGiven)
	}
	if n, ok := hostTokenCount(t.Reasoning); ok {
		u.Reasoning = usage.Of(int64(n))
	} else {
		u.Reasoning = usage.Missing[int64](usage.NotGiven)
	}
	return u, true
}

// codexLimit is one window: Codex names none, so a week (10080 minutes)
// is the weekly window and a shorter one the short window, else its
// length.
func codexLimit(w *codexWindow) (usage.Limit, bool) {
	if w == nil || w.Used == nil || *w.Used < 0 || math.IsNaN(*w.Used) || math.IsInf(*w.Used, 0) {
		return usage.Limit{}, false
	}
	l := usage.Limit{UsedPercent: *w.Used, Name: "window"}
	if w.Minutes != nil && *w.Minutes > 0 {
		l.WindowMinutes = int(min(*w.Minutes, math.MaxInt32))
		switch {
		case *w.Minutes == 10080:
			l.Name = usage.LimitWeekly
		case *w.Minutes <= 720:
			l.Name = usage.LimitShort
		}
	}
	if w.ResetsAt != nil {
		l.ResetsAt = epoch(*w.ResetsAt)
	}
	return l, true
}

// epoch reads a time Codex wrote in seconds since 1970 (RateLimitWindow's
// resets_at); zero for one out of a range a record can hold.
func epoch(n int64) time.Time {
	if t := time.Unix(n, 0).UTC(); plausible(t) {
		return t
	}
	return time.Time{}
}

// plausible reports whether a harness's time is one a record keeps: from
// 2000 to 2199. A time past year 9999 cannot be written as JSON at all.
func plausible(t time.Time) bool {
	return t.Year() >= 2000 && t.Year() < 2200
}
