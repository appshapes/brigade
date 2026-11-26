package commands

import (
	"encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestWorkItemStartAndStop is work item 346: `start` links this session to
// a work item, a second `start` replaces the link and says what it
// stopped, `stop` clears it, a call that changes nothing says so, and
// `show` lists the session with its times.
func TestWorkItemStartAndStop(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "Fix the login redirect", "add")
	f.workItem(t, WorkItemOptions{}, "Add a board", "add")

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"stop"}, "unchanged: this session works on no work item\n"},
		{[]string{"start", "1"}, "started: this session works on 1 \"Fix the login redirect\"\n"},
		{[]string{"start", "#1"}, "unchanged: this session already works on 1 \"Fix the login redirect\"\n"},
		{[]string{"start", "2"}, "stopped: this session no longer works on 1 \"Fix the login redirect\"\nstarted: this session works on 2 \"Add a board\"\n"},
		{[]string{"stop"}, "stopped: this session no longer works on 2 \"Add a board\"\n"},
		{[]string{"stop"}, "unchanged: this session works on no work item\n"},
	} {
		if got := f.workItem(t, WorkItemOptions{}, "", step.args...); got != step.want {
			t.Fatalf("work-item %v = %q\nwant %q", step.args, got, step.want)
		}
	}

	w, err := workitem.ReadSessionWork(folder, selfSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if len(w.Spans) != 2 || w.Current() != nil || w.Spans[0].End != workitem.EndStarted || w.Spans[1].End != workitem.EndStopped {
		t.Fatalf("the spans: %+v", w.Spans)
	}

	stamp := fixtureNow.Format("2006-01-02 15:04 UTC")
	show := f.workItem(t, WorkItemOptions{}, "", "show", "1")
	want := "\nsessions:\n  by alice@example.com (session " + selfSessionName + "), from " + stamp + " to " + stamp + "\n\nhistory:\n"
	if !strings.Contains(show, want) {
		t.Fatalf("show 1 lists no session:\n%s", show)
	}
	f.workItem(t, WorkItemOptions{}, "Untouched", "add")
	if show := f.workItem(t, WorkItemOptions{}, "", "show", "3"); strings.Contains(show, "sessions:") {
		t.Fatalf("a work item no session worked on lists sessions:\n%s", show)
	}
}

// TestWorkItemStartJSON is the --json result of `start` and `show`: the
// span opened, the one closed, and every span on the work item.
func TestWorkItemStartJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "First", "add")
	f.workItem(t, WorkItemOptions{}, "Second", "add")
	f.workItem(t, WorkItemOptions{}, "", "start", "1")

	run := func(args ...string) workItemResult {
		t.Helper()
		f.out.Reset()
		inv := f.inv(f.sessionEnv(), "", args...)
		inv.JSON = true
		if err := WorkItem(inv, WorkItemOptions{}); err != nil {
			t.Fatalf("work-item %v: %v", args, err)
		}
		return resultOf[workItemResult](t, f.out.String())
	}
	res := run("start", "2")
	if !res.Changed || res.Started == nil || res.Started.Number != 2 || res.Started.SessionID != selfSessionID || !res.Started.Open() ||
		res.Stopped == nil || res.Stopped.Number != 1 || res.Stopped.End != workitem.EndStarted {
		t.Fatalf("start --json: %+v started %+v stopped %+v", res, res.Started, res.Stopped)
	}
	if res = run("show", "1"); len(res.Sessions) != 1 || res.Sessions[0].Open() || res.Sessions[0].Session != selfSessionName {
		t.Fatalf("show --json: %+v", res.Sessions)
	}
}

// TestWorkItemStartRefusals: `start` names one work item the board has,
// and `stop` names none.
func TestWorkItemStartRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "First", "add")
	for _, tc := range []struct {
		args []string
		code protocol.Code
	}{
		{[]string{"start"}, protocol.CodeUsage},
		{[]string{"start", "1", "2"}, protocol.CodeUsage},
		{[]string{"start", "x"}, protocol.CodeInvalidInput},
		{[]string{"start", "9"}, protocol.CodeNotFound},
		{[]string{"stop", "1"}, protocol.CodeUsage},
	} {
		f.out.Reset()
		err := WorkItem(f.inv(f.sessionEnv(), "", tc.args...), WorkItemOptions{})
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != tc.code {
			t.Fatalf("work-item %v: want %s, got %v", tc.args, tc.code, err)
		}
	}
	// A closed work item and a roadmap item are refused, each with its reason.
	f.workItem(t, WorkItemOptions{}, "Finished", "add")
	f.workItem(t, WorkItemOptions{}, "", "close", "2")
	folder := f.withBoard(t)
	board := readBoard(t, folder)
	title, list, kind := "A goal", board.Lists[0].Key, workitem.KindRoadmap
	if _, err := (workitem.Store{Folder: folder}).Add(board, workitem.Fields{Title: &title, List: &list, Kind: &kind}, workitem.Author{}); err != nil {
		t.Fatal(err)
	}
	for number, reason := range map[string]string{"2": ReasonWorkItemClosed, "3": ReasonIsARoadmapItem} {
		f.out.Reset()
		err := WorkItem(f.inv(f.sessionEnv(), "", "start", number), WorkItemOptions{})
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != protocol.CodeInvalidInput || perr.Details["reason"] != reason {
			t.Fatalf("start %s: want invalid_input %s, got %v", number, reason, err)
		}
	}
	if w, err := workitem.ReadSessionWork(folder, selfSessionID); err != nil || len(w.Spans) != 0 {
		t.Fatalf("a refused start wrote a link: %+v %v", w, err)
	}

	f.out.Reset()
	err := WorkItem(f.inv(f.sessionEnv(), "", "start", "1"), WorkItemOptions{Mine: true})
	var perr *protocol.Error
	if !errors.As(err, &perr) || perr.Code != protocol.CodeUsage {
		t.Fatalf("start --mine: want usage, got %v", err)
	}
}

// TestSessionsShowsTheWorkItem is `brigade sessions` with the board's
// links (work item 346): the ITEM column carries the number each live
// session works on, under the number the work item has now; an offline
// session shows none though its span is open; --json carries the same.
func TestSessionsShowsTheWorkItem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "First", "add")
	f.workItem(t, WorkItemOptions{}, "Second", "add")
	f.workItem(t, WorkItemOptions{}, "", "start", "2")
	store := workitem.Store{Folder: folder}
	board := readBoard(t, folder)
	for _, id := range []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "dddddddddddddddddddddddddddddddd"} {
		if err := store.StartWork(&workitem.SessionWork{SessionID: id}, board.ByNumber(1)[0], workitem.Author{}); err != nil {
			t.Fatal(err)
		}
	}

	// Work item 2 takes another number after the start: the column shows
	// the number it has now.
	five := 5
	if err := store.Change(readBoard(t, folder).ByNumber(2)[0], &workitem.Fields{Number: &five}, "", workitem.Author{}); err != nil {
		t.Fatal(err)
	}

	f.rec.on("session list", okAnswer(listResult()))
	f.out.Reset()
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{All: true}); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	rows := map[string][]string{}
	for line := range strings.SplitSeq(f.out.String(), "\n") {
		if cells := strings.Fields(line); len(cells) > 2 {
			rows[cells[0]] = cells
		}
	}
	if h := rows["SESSION"]; len(h) < 3 || h[2] != "ITEM" {
		t.Fatalf("no ITEM column:\n%s", f.out.String())
	}
	if r := rows[shortSession(selfSessionID)]; r[2] != "5" {
		t.Fatalf("this session's row: %v", r)
	}
	if r := rows["bbbbb"]; r[2] != "1" {
		t.Fatalf("bob's row: %v", r)
	}
	if r := rows["ddddd"]; r[2] == "1" {
		t.Fatalf("an offline session shows a work item: %v", r)
	}

	f.out.Reset()
	inv := f.inv(f.sessionEnv(), "")
	inv.JSON = true
	if err := Sessions(inv, SessionsOptions{All: true}); err != nil {
		t.Fatalf("sessions --json: %v", err)
	}
	res := resultOf[sessionsResult](t, f.out.String())
	if len(res.WorkItems) != 2 || res.WorkItems[selfSessionID].Number != 5 || res.WorkItems["bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"].Number != 1 {
		t.Fatalf("work_items: %+v", res.WorkItems)
	}
}

// TestSessionsWithoutLinksHasNoItemColumn: a board where no session
// works on anything adds no column, so the roster reads as before.
func TestSessionsWithoutLinksHasNoItemColumn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withBoard(t)
	f.rec.on("session list", okAnswer(listResult()))
	if err := Sessions(f.inv(f.sessionEnv(), ""), SessionsOptions{}); err != nil {
		t.Fatalf("sessions: %v", err)
	}
	if strings.Contains(f.out.String(), "ITEM") {
		t.Fatalf("an ITEM column with no links:\n%s", f.out.String())
	}
}

// resultOf decodes the result of an ok envelope into T.
func resultOf[T any](t *testing.T, out string) T {
	t.Helper()
	ok, result := envelopeOf(t, out)
	if !ok {
		t.Fatalf("envelope not ok: %s", out)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return v
}
