package commands

import (
	"strings"
	"testing"
	"time"

	use "github.com/appshapes/brigade/internal/harness/usage"
)

// TestUsageShowsEachValueOrItsReason is `brigade usage` (work item 347):
// this session's block, each value or "not available" with its reason;
// --all every session on this machine; --json the records whole.
func TestUsageShowsEachValueOrItsReason(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	read := fixtureNow.Add(-90 * time.Second)
	mine := &use.Record{
		SessionID: selfSessionID, Session: selfSessionName, Harness: "codex", ReadAt: read,
		Model: use.Of("gpt-6"), ContextUsed: use.Of(int64(73000)), ContextWindow: use.Of(int64(272000)),
		Total: use.Tokens{Input: use.Of(int64(30000)), Output: use.Of(int64(3000)), CacheRead: use.Of(int64(90000)),
			CacheWrite: use.Missing[int64](use.NotGiven), Reasoning: use.Of(int64(1500))},
		Calls: use.Missing[int64](use.NotGiven), Cost: use.Missing[use.Cost](use.NotGiven),
		Limits: use.Of(use.Limits{Plan: "plus", Windows: []use.Limit{
			{Name: use.LimitShort, UsedPercent: 12.5, WindowMinutes: 300, ResetsAt: time.Date(2026, 11, 26, 14, 0, 0, 0, time.UTC)},
			{Name: use.LimitWeekly, UsedPercent: 40, WindowMinutes: 10080},
		}}),
		LimitsAt: read, Extra: use.Of(use.Extra{HasCredits: ptrTo(true), Balance: "17.50"}),
	}
	other := &use.Record{SessionID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Session: "billing", Harness: "claude-code", ReadAt: read,
		Cost: use.Missing[use.Cost](use.StatusLineOnly)}
	for _, r := range []*use.Record{mine, other} {
		if err := use.Write(f.stateDir, r); err != nil {
			t.Fatal(err)
		}
	}
	run := func(opts UsageOptions, json bool) string {
		t.Helper()
		f.out.Reset()
		inv := f.inv(f.sessionEnv(), "")
		inv.JSON = json
		if err := Usage(inv, opts); err != nil {
			t.Fatalf("usage: %v", err)
		}
		return f.out.String()
	}
	want := selfSessionName + " (codex, gpt-6), read 1m ago\n" +
		"  context: 73k of 272k\n" +
		"  tokens: input 30k, output 3k, cache read 90k, reasoning 2k\n" +
		"  cost: not available: " + use.NotGiven + "\n" +
		"  limits: 5-hour 12.5% (resets 2026-11-26 14:00 UTC), weekly 40%; plan plus; as of 1m ago\n" +
		"  extra usage: credits yes, balance 17.50\n"
	if got := run(UsageOptions{}, false); got != want {
		t.Fatalf("usage =\n%s\nwant\n%s", got, want)
	}
	all := run(UsageOptions{All: true}, false)
	if !strings.Contains(all, "billing (claude-code), read 1m ago") || !strings.Contains(all, "cost: not available: "+use.StatusLineOnly) {
		t.Fatalf("usage --all:\n%s", all)
	}
	res := resultOf[usageResult](t, run(UsageOptions{}, true))
	if len(res.Sessions) != 1 || res.Sessions[0].Limits.V.Plan != "plus" || res.SelfSessionID != selfSessionID {
		t.Fatalf("--json: %+v", res)
	}
}

// TestUsageWithNone: no record yet says so.
func TestUsageWithNone(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	if err := Usage(f.inv(f.sessionEnv(), ""), UsageOptions{}); err != nil {
		t.Fatal(err)
	}
	if got := f.out.String(); got != UsageNone+"\n" {
		t.Fatalf("got %q", got)
	}
}

// TestStatuslineKeepsTheStatusAndPrintsNothing is `brigade statusline`:
// it keeps the status for the watcher under Claude Code's own session id,
// prints nothing, and passes over a document it cannot use without an
// error, so a member's status line is never blanked by it.
func TestStatuslineKeepsTheStatusAndPrintsNothing(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	doc := `{"session_id":"native-1","cost":{"total_cost_usd":2.5},"rate_limits":{"seven_day":{"used_percentage":41.2,"resets_at":1738857600}}}`
	if err := Statusline(f.inv(f.sessionEnv(), doc)); err != nil {
		t.Fatal(err)
	}
	if f.out.Len() != 0 {
		t.Fatalf("printed %q", f.out.String())
	}
	s, err := use.ReadStatus(f.stateDir, "native-1")
	if err != nil || s == nil || *s.CostUSD != 2.5 || len(s.Limits) != 1 || s.Limits[0].Name != use.LimitWeekly {
		t.Fatalf("status: %+v %v", s, err)
	}
	for _, bad := range []string{"", "not json", `{"cost":{}}`} {
		if err := Statusline(f.inv(f.sessionEnv(), bad)); err != nil || f.out.Len() != 0 {
			t.Fatalf("%q: %v %q", bad, err, f.out.String())
		}
	}
}

func ptrTo[T any](v T) *T { return &v }

// TestUsageTexts: a window's length, and a count at the boundary of k
// and M.
func TestUsageTexts(t *testing.T) {
	t.Parallel()
	for minutes, want := range map[int]string{300: "5-hour", 30: "30-minute", 90: "90-minute"} {
		if got := windowLength(minutes); got != want {
			t.Fatalf("windowLength(%d) = %q", minutes, got)
		}
	}
	for n, want := range map[int64]string{999: "999", 999_499: "999k", 999_700: "1.0M", 1_400_000: "1.4M"} {
		if got := countText(n); got != want {
			t.Fatalf("countText(%d) = %q, want %q", n, got, want)
		}
	}
}
