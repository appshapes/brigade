package commands

import (
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	use "github.com/appshapes/brigade/internal/harness/usage"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// UsageOptions are the flags of `brigade usage`.
type UsageOptions struct {
	// All shows every session's usage on this machine, in place of this
	// session's alone.
	All bool
}

// UsageNote is the note of the --json result.
const UsageNote = "each session's usage as its harness's own records gave it, read by the session's watcher on this machine; " +
	"a value that is not there carries the reason; nothing of it was sent to the backend or to a teammate"

// UsageNone is the line of a machine, or a session, with no usage read.
const UsageNone = "no usage read yet: the watcher of a running session reads it at each heartbeat"

// usageResult is the --json result.
type usageResult struct {
	SelfSessionID string        `json:"self_session_id,omitzero"`
	Sessions      []*use.Record `json:"sessions"`
	Note          string        `json:"note"`
}

// Usage implements `brigade usage [--all]` (work item 347, ADR 0036): the
// usage of this session, or with --all of every session whose watcher ran
// on this machine, as the watcher last read it from the harness's own
// records. It reads files in this machine's state directory and sends
// nothing. Outside a session it shows every session, as --all.
func Usage(inv Invocation, opts UsageOptions) error {
	if len(inv.Args) > 0 {
		return usage("usage takes no arguments")
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return err
	}
	self := ""
	if inv.inSession() {
		// In a session, its own usage: a map that cannot be read is said,
		// never answered with every session's.
		m, _, err := inv.sessionMap()
		if err != nil {
			return err
		}
		self = m.BrigadeSessionID
	}
	records, err := usageList(stateDir)
	if err != nil {
		inv.logger().Warn("usage: the usage folder could not be read", log.Err(err))
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the usage folder in the state directory could not be read"}
	}
	if self != "" && !opts.All {
		var mine []*use.Record
		for _, r := range records {
			if r.SessionID == self {
				mine = append(mine, r)
			}
		}
		records = mine
	}
	if inv.JSON {
		if records == nil {
			records = []*use.Record{}
		}
		return writeJSON(inv.Out, usageResult{SelfSessionID: self, Sessions: records, Note: UsageNote})
	}
	if len(records) == 0 {
		return writeLines(inv.Out, UsageNone)
	}
	now := inv.Deps.now()
	var lines []string
	for i, r := range records {
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, usageBlock(r, now)...)
	}
	return writeLines(inv.Out, lines...)
}

// usageList is the records, each one's text cleaned for a terminal.
func usageList(stateDir string) ([]*use.Record, error) {
	records, err := use.List(stateDir)
	if err != nil {
		return nil, err
	}
	for _, r := range records {
		r.Session = protocol.OneLineName(r.Session)
		r.Harness = attrLine(r.Harness)
		if r.Model.V != nil {
			m := modelLine(*r.Model.V)
			r.Model.V = &m
		}
	}
	return records, nil
}

// usageBlock is one session's usage as lines: a heading, then one line for
// each value, "not available" with the reason where the value is not
// there.
func usageBlock(r *use.Record, now time.Time) []string {
	name := r.Session
	if name == "" {
		name = shortSession(r.SessionID)
	}
	head := tableName(name) + " (" + r.Harness
	if r.Model.Known() {
		head += ", " + *r.Model.V
	}
	head += "), read " + usageAgo(r.ReadAt, now)
	lines := []string{head}
	add := func(label, text string) { lines = append(lines, "  "+label+": "+text) }

	ctx := "not available: " + reasonText(r.ContextUsed.Unavailable)
	if r.ContextUsed.Known() {
		ctx = countText(*r.ContextUsed.V)
		if r.ContextWindow.Known() {
			ctx += " of " + countText(*r.ContextWindow.V)
		}
	}
	add("context", ctx)
	add("tokens", tokensText(r.Total, r.Calls))
	switch {
	case r.Cost.Known():
		c := *r.Cost.V
		text := "$" + strconv.FormatFloat(c.USD, 'f', 2, 64)
		switch c.Kind {
		case use.KindEstimate:
			text += ", estimated at list price"
		default:
			text += "; the harness does not say whether it is a list price or a bill"
		}
		if c.Partial {
			text += " (some calls had no price)"
		}
		add("cost", text)
	default:
		add("cost", "not available: "+reasonText(r.Cost.Unavailable))
	}
	switch {
	case r.Limits.Known():
		add("limits", limitsText(*r.Limits.V, r.LimitsAt, now))
	default:
		add("limits", "not available: "+reasonText(r.Limits.Unavailable))
	}
	switch {
	case r.Extra.Known():
		add("extra usage", extraText(*r.Extra.V))
	default:
		add("extra usage", "not available: "+reasonText(r.Extra.Unavailable))
	}
	return lines
}

// reasonText is a reason as a line shows it: the record's own text,
// cleaned, which the watcher wrote from fixed text.
func reasonText(reason string) string {
	if reason = protocol.TruncateRunes(workitem.Line(reason), 200); reason == "" {
		return use.NotYet
	}
	return reason
}

// tokensText is the session's tokens: each count that is there, and the
// calls.
func tokensText(t use.Tokens, calls use.Value[int64]) string {
	var parts []string
	for _, v := range []struct {
		label string
		v     use.Value[int64]
	}{{"input", t.Input}, {"output", t.Output}, {"cache read", t.CacheRead}, {"cache write", t.CacheWrite}, {"reasoning", t.Reasoning}} {
		if v.v.Known() {
			parts = append(parts, v.label+" "+countText(*v.v.V))
		}
	}
	if len(parts) == 0 {
		return "not available: " + reasonText(t.Input.Unavailable)
	}
	text := strings.Join(parts, ", ")
	if calls.Known() {
		text += "; " + strconv.FormatInt(*calls.V, 10) + " model calls"
	}
	return text
}

// limitsText is the plan's windows.
func limitsText(l use.Limits, at, now time.Time) string {
	if len(l.Windows) == 0 {
		return "none given"
	}
	var parts []string
	for _, w := range l.Windows {
		var name string
		switch w.Name {
		case use.LimitWeekly:
			name = "weekly"
		case use.LimitShort:
			name = "short window"
		default:
			name = attrLine(w.Name)
		}
		if w.Name != use.LimitWeekly && w.WindowMinutes > 0 {
			name = windowLength(w.WindowMinutes)
		}
		text := name + " " + strconv.FormatFloat(w.UsedPercent, 'f', -1, 64) + "%"
		if !w.ResetsAt.IsZero() {
			text += " (resets " + w.ResetsAt.UTC().Format("2006-01-02 15:04 UTC") + ")"
		}
		parts = append(parts, text)
	}
	text := strings.Join(parts, ", ")
	if p := attrLine(l.Plan); p != "" {
		text += "; plan " + p
	}
	if !at.IsZero() {
		text += "; as of " + usageAgo(at, now)
	}
	return text
}

// windowLength names a window by its length: "5-hour", or "90-minute"
// for one that is not whole hours.
func windowLength(minutes int) string {
	if minutes%60 == 0 {
		return strconv.Itoa(minutes/60) + "-hour"
	}
	return strconv.Itoa(minutes) + "-minute"
}

// extraText is the paid usage beyond the plan, as Codex CLI gives it.
func extraText(x use.Extra) string {
	var parts []string
	if x.HasCredits != nil {
		parts = append(parts, "credits "+yesNo(*x.HasCredits))
	}
	if x.Unlimited != nil && *x.Unlimited {
		parts = append(parts, "unlimited")
	}
	if b := attrLine(x.Balance); b != "" {
		parts = append(parts, "balance "+b)
	}
	if x.SpendControlReached != nil && *x.SpendControlReached {
		parts = append(parts, "spend control reached")
	}
	if len(parts) == 0 {
		return "none given"
	}
	return strings.Join(parts, ", ")
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// countText is a count as a person reads it: 970, 325k, 1.4M.
func countText(n int64) string {
	switch {
	case n < 1000:
		return strconv.FormatInt(n, 10)
	case n < 999_500:
		return strconv.FormatInt((n+500)/1000, 10) + "k"
	default:
		return strconv.FormatFloat(float64(n)/1e6, 'f', 1, 64) + "M"
	}
}

// usageAgo is how long ago a time was, in whole units.
func usageAgo(at, now time.Time) string {
	if at.IsZero() {
		return "never"
	}
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return strconv.Itoa(max(0, int(d.Seconds()))) + "s ago"
	case d < time.Hour:
		return strconv.Itoa(int(d.Minutes())) + "m ago"
	case d < 48*time.Hour:
		return strconv.Itoa(int(d.Hours())) + "h ago"
	}
	return strconv.Itoa(int(d.Hours()/24)) + "d ago"
}

// Statusline implements `brigade statusline` (work item 347, ADR 0036): a
// step a member adds to their own Claude Code status line script, which
// reads the status line document on stdin and keeps the cost, the context
// window and the plan's limits for the session's watcher, the one place
// Claude Code gives them (a plugin cannot set the status line). It prints
// nothing, so the member's own line stays as it is, and it never fails:
// a document it cannot use is passed over, and the status line is never
// blanked by it.
func Statusline(inv Invocation) error {
	if len(inv.Args) > 0 {
		return usage("statusline takes no arguments; the status line document travels on stdin")
	}
	data, err := io.ReadAll(io.LimitReader(inv.In, use.MaxStatusBytes+1))
	if err != nil || len(data) > use.MaxStatusBytes {
		inv.logger().Debug("statusline: the document was not read whole")
		return nil //nolint:nilerr // the step never fails: an error would blank the member's status line
	}
	s, err := use.ParseStatus(data, inv.Deps.now())
	if err != nil {
		inv.logger().Debug("statusline: the document was passed over", log.Err(err))
		return nil
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		inv.logger().Debug("statusline: no state directory", log.Err(err))
		return nil
	}
	if err := use.WriteStatus(stateDir, s); err != nil {
		inv.logger().Debug("statusline: the status was not written", log.Err(err))
	}
	return nil
}
