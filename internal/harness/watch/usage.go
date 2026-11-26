package watch

import (
	"context"
	"encoding/json/v2"
	"log/slog"
	"regexp"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	adlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/transcript"
	"github.com/appshapes/brigade/internal/harness/usage"
	"github.com/appshapes/brigade/internal/protocol"
)

// The usage of a session (work item 347, ADR 0036): at each heartbeat the
// watcher puts what the harness's own records give into one usage.Record
// and writes it to this machine's state directory. Nothing of it rides
// the heartbeat: whether it is shared is work item 348.

// usageRewrite is how old a record may get before it is written again
// although nothing in it changed, so its read_at says the watcher is
// still reading.
const usageRewrite = 5 * time.Minute

// usageLimitsEvery is how often a harness's own limits command runs, and
// usageLimitsBudget how long it may take. Muse Code allows usage/refresh
// once a minute for each host; `muse usage` reads what the host has.
const (
	usageLimitsEvery  = 5 * time.Minute
	usageLimitsBudget = 5 * time.Second
	usageOutputBytes  = 64 << 10
)

// usageState is what recordUsage keeps between heartbeats.
type usageState struct {
	last      []byte // the last record written, read_at aside
	written   time.Time
	limitsRun time.Time
	limits    usage.Value[usage.Limits]
	limitsAt  time.Time
}

// runUsageCommand is the production Deps.UsageCommand.
func runUsageCommand(ctx context.Context, argv, env []string) ([]byte, error) {
	return adapterkit.RunOutput(ctx, adapterkit.QuietSpec{Argv: argv, Env: env}, usageOutputBytes)
}

// recordUsage builds the session's record and writes it when it changed,
// or when it was last written usageRewrite ago. A failure is a debug line:
// the heartbeat goes on.
func (w *watcher) recordUsage(snap sharedSnapshot) {
	now := w.deps.Clock()
	// The limits command runs outside the lock the heartbeat's readers
	// share: it may take its whole budget.
	w.hostLimits(now)
	w.transcriptMu.Lock()
	defer w.transcriptMu.Unlock()
	r := w.usageRecord(snap)
	r.ReadAt = time.Time{}
	body, err := json.Marshal(r)
	if err != nil {
		return
	}
	if string(body) == string(w.usage.last) && now.Sub(w.usage.written) < usageRewrite {
		return
	}
	r.ReadAt = now.UTC()
	if w.usage.written.IsZero() {
		// The first write of this watcher prunes what no watcher has
		// written for a long time.
		if n := usage.Prune(w.rc.env.StateDir, now.Add(-usageKeep)); n > 0 {
			w.log.Debug("usage: old records removed", slog.Int("count", n))
		}
	}
	if err := usage.Write(w.rc.env.StateDir, r); err != nil {
		w.log.Debug("usage: the session's record was not written", adlog.Err(err))
		return
	}
	w.usage.last, w.usage.written = body, now
}

// usageKeep is how long a record no watcher wrote again is kept.
const usageKeep = 30 * 24 * time.Hour

// usageRecord is the session's usage as its harness's records give it now.
// Every value that is not there carries its reason. The caller holds
// transcriptMu.
func (w *watcher) usageRecord(snap sharedSnapshot) *usage.Record {
	host := config.Host(w.harness).OrClaude()
	r := &usage.Record{SessionID: w.sessionID, Session: protocol.OneLineName(snap.name), Harness: string(host)}
	if !host.Pointed() {
		w.claudeUsage(r, snap)
	} else {
		f := w.hostFacts.facts
		use := f.use
		r.Total, r.Last, r.Calls = use.Total, use.Last, use.Calls
		r.ContextWindow, r.ContextUsed, r.Cost = use.ContextWindow, use.ContextUsed, use.Cost
		r.Limits, r.LimitsAt, r.Extra = use.Limits, use.LimitsAt, use.Extra
		if !r.ContextUsed.Known() && f.hasContext {
			r.ContextUsed = usage.Of(int64(f.tokens))
		}
		if f.model != "" {
			r.Model = usage.Of(f.model)
		}
		if len(host.Usage().LimitsCommand) > 0 {
			r.Limits, r.LimitsAt = w.usage.limits, w.usage.limitsAt
		}
	}
	gaps(r, host.Usage())
	if !r.Model.Known() && w.lastModel != "" {
		r.Model = usage.Of(w.lastModel)
	}
	if !r.Model.Known() && w.model != "" {
		r.Model = usage.Of(oneLineModel(w.model))
	}
	fillReasons(r)
	return r
}

// gaps marks each value the host gives nowhere Brigade reads with its
// reason, whatever a reader found.
func gaps(r *usage.Record, g config.UsageGaps) {
	if g.Calls != "" {
		r.Calls = usage.Missing[int64](g.Calls)
	}
	if g.ContextWindow != "" {
		r.ContextWindow = usage.Missing[int64](g.ContextWindow)
	}
	if g.Cost != "" {
		r.Cost = usage.Missing[usage.Cost](g.Cost)
	}
	if g.Limits != "" {
		r.Limits, r.LimitsAt = usage.Missing[usage.Limits](g.Limits), time.Time{}
	}
	if g.Extra != "" {
		r.Extra = usage.Missing[usage.Extra](g.Extra)
	}
	if g.Reasoning != "" {
		for _, t := range []*usage.Tokens{&r.Total, &r.Last} {
			if !t.Reasoning.Known() {
				t.Reasoning = usage.Missing[int64](g.Reasoning)
			}
		}
	}
}

// claudeUsage fills a Claude Code session's record: the tokens and the
// context from its transcript, and the window, the cost and the limits
// from what its status line gave `brigade statusline`.
func (w *watcher) claudeUsage(r *usage.Record, snap sharedSnapshot) {
	f := w.transcriptUsage
	if m := oneLineModel(f.Model); m != "" {
		r.Model = usage.Of(m)
	}
	if f.HasContext {
		r.ContextUsed = usage.Of(int64(f.ContextUsedTokens))
	}
	if f.Calls > 0 {
		r.Calls = usage.Of(f.Calls)
		r.Total, r.Last = claudeTokens(f.Total), claudeTokens(f.Last)
	}
	st, err := usage.ReadStatus(w.rc.env.StateDir, snap.nativeID)
	if err != nil {
		w.log.Debug("usage: the status line's record is unreadable", adlog.Err(err))
	}
	if st == nil {
		r.ContextWindow = usage.Missing[int64](usage.StatusLineOnly)
		r.Cost = usage.Missing[usage.Cost](usage.StatusLineOnly)
		r.Limits = usage.Missing[usage.Limits](usage.StatusLineOnly)
		return
	}
	if st.ContextWindow != nil {
		r.ContextWindow = usage.Of(*st.ContextWindow)
	}
	if st.CostUSD != nil {
		r.Cost = usage.Of(usage.Cost{USD: *st.CostUSD, Kind: usage.KindEstimate})
	}
	if st.HasLimits {
		r.Limits, r.LimitsAt = usage.Of(usage.Limits{Windows: st.Limits}), st.At
	} else {
		r.Limits = usage.Missing[usage.Limits](usage.NoLimitsYet)
	}
}

// claudeTokens is a transcript's counts; the host table says why there
// is no reasoning count.
func claudeTokens(t transcript.Tokens) usage.Tokens {
	return usage.Tokens{
		Input: usage.Of(t.Input), Output: usage.Of(t.Output),
		CacheRead: usage.Of(t.CacheRead), CacheWrite: usage.Of(t.CacheWrite),
	}
}

// hostLimits runs the host's limits command (`muse usage --json`) at most
// once in usageLimitsEvery and keeps what it gave: the plan's 5-hour-class
// window and weekly block, or the reason it gave none. It takes
// transcriptMu only to read and keep its state, never while the command
// runs.
func (w *watcher) hostLimits(now time.Time) {
	cmd := config.Host(w.harness).OrClaude().Usage().LimitsCommand
	if len(cmd) == 0 {
		return
	}
	w.transcriptMu.Lock()
	due := w.usage.limitsRun.IsZero() || now.Sub(w.usage.limitsRun) >= usageLimitsEvery
	if due {
		w.usage.limitsRun = now
	}
	w.transcriptMu.Unlock()
	if !due {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), usageLimitsBudget)
	defer cancel()
	limits, at := usage.Missing[usage.Limits](usage.CommandFailed), time.Time{}
	if out, err := w.deps.UsageCommand(ctx, cmd, adapterkit.ChildEnv(w.environ)); err != nil {
		w.log.Debug("usage: the harness's limits command did not answer", adlog.Err(err))
	} else {
		limits, at = museLimitsOf(out)
	}
	w.transcriptMu.Lock()
	w.usage.limits, w.usage.limitsAt = limits, at
	w.transcriptMu.Unlock()
}

// museReason is the word Muse gives for no usage: lower-case letters and
// underscores, as `server_sent_no_usage`.
var museReason = regexp.MustCompile(`^[a-z_]{1,48}$`)

// museLimitsOf reads `muse usage --json`: schemaVersion 1, an allowance
// that is Muse's SubscriptionUsage (tier, window, weekly, observedAtMs),
// or a null allowance with the reason Muse gave.
func museLimitsOf(out []byte) (usage.Value[usage.Limits], time.Time) {
	var doc struct {
		Schema    int `json:"schemaVersion"`
		Allowance *struct {
			ObservedAt *int64  `json:"observedAtMs"`
			Tier       *string `json:"tier"`
			Window     *struct {
				Used     *float64 `json:"usedPercent"`
				ResetsAt *int64   `json:"resetsAtMs"`
				Minutes  *int64   `json:"windowDurationMins"`
			} `json:"window"`
			Weekly *struct {
				Used     *float64 `json:"usedPercent"`
				ResetsAt *int64   `json:"resetsAtMs"`
			} `json:"weekly"`
		} `json:"allowance"`
		Reason string `json:"reason"`
	}
	if json.Unmarshal(out, &doc, hostJSON) != nil || doc.Schema != 1 {
		return usage.Missing[usage.Limits](usage.CommandFailed), time.Time{}
	}
	a := doc.Allowance
	if a == nil {
		reason := usage.NotYet
		if museReason.MatchString(doc.Reason) {
			reason = "Muse Code has none: " + doc.Reason
		}
		return usage.Missing[usage.Limits](reason), time.Time{}
	}
	limits := usage.Limits{Windows: []usage.Limit{}}
	if a.Tier != nil {
		limits.Plan = oneLineName(*a.Tier)
	}
	if win := a.Window; win != nil && win.Used != nil && *win.Used >= 0 {
		l := usage.Limit{Name: usage.LimitShort, UsedPercent: *win.Used}
		if win.Minutes != nil && *win.Minutes > 0 {
			l.WindowMinutes = int(min(*win.Minutes, 1<<31-1))
		}
		if win.ResetsAt != nil && *win.ResetsAt > 0 {
			l.ResetsAt = time.UnixMilli(*win.ResetsAt).UTC()
		}
		limits.Windows = append(limits.Windows, l)
	}
	if wk := a.Weekly; wk != nil && wk.Used != nil && *wk.Used >= 0 {
		l := usage.Limit{Name: usage.LimitWeekly, UsedPercent: *wk.Used, WindowMinutes: 10080}
		if wk.ResetsAt != nil && *wk.ResetsAt > 0 {
			l.ResetsAt = time.UnixMilli(*wk.ResetsAt).UTC()
		}
		limits.Windows = append(limits.Windows, l)
	}
	var at time.Time
	if a.ObservedAt != nil && *a.ObservedAt > 0 {
		at = time.UnixMilli(*a.ObservedAt).UTC()
	}
	return usage.Of(limits), at
}

// fillReasons gives every value that is neither there nor explained the
// reason NotYet: the harness gives it, and this session has not reached
// it yet. A value is never left blank and never written as zero.
func fillReasons(r *usage.Record) {
	notYet := func(v interface{ Known() bool }, reason *string) {
		if !v.Known() && *reason == "" {
			*reason = usage.NotYet
		}
	}
	notYet(r.Model, &r.Model.Unavailable)
	notYet(r.ContextUsed, &r.ContextUsed.Unavailable)
	notYet(r.ContextWindow, &r.ContextWindow.Unavailable)
	notYet(r.Calls, &r.Calls.Unavailable)
	notYet(r.Cost, &r.Cost.Unavailable)
	notYet(r.Limits, &r.Limits.Unavailable)
	notYet(r.Extra, &r.Extra.Unavailable)
	for _, t := range []*usage.Tokens{&r.Total, &r.Last} {
		for _, v := range []*usage.Value[int64]{&t.Input, &t.Output, &t.CacheRead, &t.CacheWrite, &t.Reasoning} {
			notYet(*v, &v.Unavailable)
		}
	}
}
