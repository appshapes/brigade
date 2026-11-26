package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	use "github.com/appshapes/brigade/internal/harness/usage"
)

// TestWorkItemPageShowsTheAgents is work item 349 on the local page: a
// card shows each session of this machine that worked on it, with its
// tokens and estimated cost, the context of one that works on it now, and
// the total; the plan limits stand apart, a window at 80 percent or more
// marked near; a card no session worked on shows none.
func TestWorkItemPageShowsTheAgents(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "Show the usage", "add")
	f.workItem(t, WorkItemOptions{}, "Untouched", "add")
	f.workItem(t, WorkItemOptions{}, "", "start", "1")
	id := readBoard(t, folder).ByNumber(1)[0].ID

	now := fixtureNow.Add(-time.Minute)
	records := []*use.Record{
		{
			SessionID: selfSessionID, Session: selfSessionName, Harness: "claude-code", ReadAt: now,
			Model: use.Of("claude-opus-5-5"), ContextUsed: use.Of(int64(620000)), ContextWindow: use.Of(int64(1000000)),
			Limits: use.Of(use.Limits{Windows: []use.Limit{
				{Name: use.LimitShort, UsedPercent: 85, WindowMinutes: 300},
				// Reset an hour ago: not near any more.
				{Name: use.LimitWeekly, UsedPercent: 95, WindowMinutes: 10080, ResetsAt: fixtureNow.Add(-time.Hour)},
			}}), LimitsAt: now,
			WorkItems: map[string]*use.ItemUsage{id: {Number: 1, Tokens: use.Counts{Input: 1000, Output: 2000}, CostUSD: 0.044, Priced: 1, Model: "claude-opus-5-5"}},
		},
		{
			SessionID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Session: "codex-helper", Harness: "codex", ReadAt: now.Add(-time.Minute),
			Model:     use.Of("gpt-6.2-astra"),
			Limits:    use.Of(use.Limits{Plan: "pro", Windows: []use.Limit{{Name: use.LimitWeekly, UsedPercent: 2, WindowMinutes: 10080}}}),
			Extra:     use.Of(use.Extra{HasCredits: ptrTo(true), Balance: "17.50"}),
			WorkItems: map[string]*use.ItemUsage{id: {Number: 1, Tokens: use.Counts{Input: 500}, Unpriced: 1, Model: "gpt-6.2-astra"}},
		},
	}
	for _, r := range records {
		if err := use.Write(f.stateDir, r); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(f.dirs.Root, "board.html")
	f.workItem(t, WorkItemOptions{Out: out}, "", "page")
	page, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	data, _, script := pageParts(t, string(page))

	card := data.Items[0]
	if len(card.Agents) != 2 || card.Spent != "2 agents, input 2k, output 2k, cache read 0, cache write 0, $0.04 estimated, part not priced" {
		t.Fatalf("card: %+v", card)
	}
	mine, helper := card.Agents[0], card.Agents[1]
	if mine.Session != selfSessionName || !mine.Current || mine.Context != "620k of 1.0M" || mine.Cost != "$0.04 estimated at list price" {
		t.Fatalf("this session: %+v", mine)
	}
	if helper.Current || helper.Context != "" || helper.Cost != "cost not available: no price for gpt-6.2-astra" {
		t.Fatalf("the helper: %+v", helper)
	}
	if other := data.Items[1]; len(other.Agents) != 0 || other.Spent != "" {
		t.Fatalf("an untouched card: %+v", other)
	}
	// The near window decides; the weekly one at 95 percent has reset.
	if len(data.Limits) != 2 || !data.Limits[0].Near || data.Limits[1].Near || data.Limits[1].Extra != "credits yes, balance 17.50" {
		t.Fatalf("limits: %+v", data.Limits)
	}
	if !strings.Contains(script, "Plan limits of the sessions on this machine") {
		t.Fatal("the script shows no limits")
	}
}

// TestWorkItemPageLimitsResetAreNotNear: a window at 80 percent or more
// that has reset since is not marked near.
func TestWorkItemPageLimitsResetAreNotNear(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "One", "add")
	r := &use.Record{SessionID: selfSessionID, Harness: "claude-code", ReadAt: fixtureNow,
		Limits: use.Of(use.Limits{Windows: []use.Limit{{Name: use.LimitShort, UsedPercent: 99, ResetsAt: fixtureNow.Add(-time.Minute)}}}), LimitsAt: fixtureNow.Add(-time.Hour)}
	if err := use.Write(f.stateDir, r); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.dirs.Root, "board.html")
	f.workItem(t, WorkItemOptions{Out: out}, "", "page")
	page, _ := os.ReadFile(out)
	data, _, _ := pageParts(t, string(page))
	if len(data.Limits) != 1 || data.Limits[0].Near {
		t.Fatalf("a reset window is near: %+v", data.Limits)
	}
}
