package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	use "github.com/appshapes/brigade/internal/harness/usage"
	"github.com/appshapes/brigade/internal/harness/workitem"
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
	if !strings.Contains(script, "Plan limits") {
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

// TestWorkItemPageShowsSharedUsage is work item 348 on the page: a
// teammate's session that shared its usage through the board shows on the
// card with the teammate's label, and its limits under the teammate's
// name; a session this machine has a record of shows that record.
func TestWorkItemPageShowsSharedUsage(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "Shared", "add")
	id := readBoard(t, folder).ByNumber(1)[0].ID
	store := workitem.Store{Folder: folder}
	share := func(sid, member string, cost float64) {
		t.Helper()
		u := &workitem.Usage{SessionID: sid, Harness: "codex", Model: "gpt-6.2-astra", ReadAt: fixtureNow, LimitsAt: fixtureNow,
			Limits: &use.Limits{Plan: "pro", Windows: []use.Limit{{Name: use.LimitWeekly, UsedPercent: 90, WindowMinutes: 10080}}},
			Items:  []workitem.UsageItem{{Item: id, ItemUsage: use.ItemUsage{Number: 1, Tokens: use.Counts{Input: 1000}, CostUSD: cost, Priced: 1}}}}
		if err := store.ShareUsage(u, workitem.Author{Member: member, Session: "teammate-session"}); err != nil {
			t.Fatal(err)
		}
	}
	share("cccccccccccccccccccccccccccccccc", "carol@example.com", 1.25)
	share(selfSessionID, "alice@example.com", 9.99)
	// This machine's own record of its session: it wins over its share.
	if err := use.Write(f.stateDir, &use.Record{SessionID: selfSessionID, Session: selfSessionName, Harness: "claude-code", ReadAt: fixtureNow,
		WorkItems: map[string]*use.ItemUsage{id: {Number: 1, CostUSD: 0.5, Priced: 1}}}); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(f.dirs.Root, "board.html")
	f.workItem(t, WorkItemOptions{Out: out}, "", "page")
	page, _ := os.ReadFile(out)
	data, _, _ := pageParts(t, string(page))
	agents := data.Items[0].Agents
	if len(agents) != 2 {
		t.Fatalf("agents: %+v", agents)
	}
	var carol, mine *boardPageAgent
	for i := range agents {
		switch agents[i].Member {
		case "carol@example.com":
			carol = &agents[i]
		case "":
			mine = &agents[i]
		}
	}
	if carol == nil || carol.Session != "teammate-session" || carol.Cost != "$1.25 estimated at list price" {
		t.Fatalf("carol's share: %+v", agents)
	}
	if mine == nil || mine.Cost != "$0.50 estimated at list price" {
		t.Fatalf("this machine's own record: %+v", agents)
	}
	if len(data.Limits) != 1 || data.Limits[0].Harness != "carol@example.com, codex" || !data.Limits[0].Near {
		t.Fatalf("limits: %+v", data.Limits)
	}
}

// TestWorkItemPageSharedUsageWithNoNameAndOld: a share that names no
// member is still a teammate's, not this machine's; a share no watcher
// wrote in 30 days shows nowhere.
func TestWorkItemPageSharedUsageWithNoNameAndOld(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "Shared", "add")
	id := readBoard(t, folder).ByNumber(1)[0].ID
	store := workitem.Store{Folder: folder}
	for sid, at := range map[string]time.Time{"dddddddddddddddddddddddddddddddd": fixtureNow, "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee": fixtureNow.Add(-40 * 24 * time.Hour)} {
		u := &workitem.Usage{SessionID: sid, Harness: "grok", ReadAt: at,
			Items: []workitem.UsageItem{{Item: id, ItemUsage: use.ItemUsage{Number: 1, Tokens: use.Counts{Input: 10}}}}}
		if err := store.ShareUsage(u, workitem.Author{}); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(f.dirs.Root, "board.html")
	f.workItem(t, WorkItemOptions{Out: out}, "", "page")
	page, _ := os.ReadFile(out)
	data, _, _ := pageParts(t, string(page))
	if a := data.Items[0].Agents; len(a) != 1 || a[0].Member != "a teammate" {
		t.Fatalf("agents: %+v", a)
	}
}
