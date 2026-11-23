package commands

import (
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

// roadmap runs one `brigade roadmap` call in the fixture's session and
// returns what it printed.
func (f *fixture) roadmap(t *testing.T, opts RoadmapOptions, stdin string, args ...string) string {
	t.Helper()
	f.out.Reset()
	if err := Roadmap(f.inv(f.sessionEnv(), stdin, args...), opts); err != nil {
		t.Fatalf("roadmap %v: %v", args, err)
	}
	return f.out.String()
}

// roadmapRefused runs one call that must be refused and returns the
// refusal.
func (f *fixture) roadmapRefused(t *testing.T, stdin string, args ...string) *protocol.Error {
	t.Helper()
	f.out.Reset()
	err := Roadmap(f.inv(f.sessionEnv(), stdin, args...), RoadmapOptions{})
	perr, ok := err.(*protocol.Error) //nolint:errorlint // the commands return the type itself
	if !ok {
		t.Fatalf("roadmap %v: %v, want a refusal", args, err)
	}
	return perr
}

// TestRoadmapLifecycle walks a roadmap through the board with the human
// output pinned word for word: two roadmap items added, work items put
// under them, the order named, a work item finished, one taken off.
func TestRoadmapLifecycle(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)

	want := "the roadmap is empty: brigade roadmap add adds a roadmap item (its title, a blank line and its description on stdin)\n" +
		"under no roadmap item: 0 open work items that are not finished (brigade work-item list shows the board)\n"
	if got := f.roadmap(t, RoadmapOptions{}, ""); got != want {
		t.Fatalf("an empty roadmap =\n%s", got)
	}
	f.workItem(t, WorkItemOptions{}, "Fix the login redirect", "add")
	f.workItem(t, WorkItemOptions{Mine: true}, "Add the reset mail", "add")
	f.workItem(t, WorkItemOptions{}, "Bump the toolchain", "add")
	got := f.roadmap(t, RoadmapOptions{}, "Sign-in works everywhere\n\nEvery browser, every tenant.\n", "add")
	if got != "added: roadmap item 4 \"Sign-in works everywhere\" in Todo, at place 1 of the roadmap (brigade roadmap order places it; brigade roadmap put <work item> 4 puts a work item under it)\n" {
		t.Fatalf("add = %q", got)
	}
	got = f.roadmap(t, RoadmapOptions{List: "doing"}, "Billing by seat", "add")
	if got != "added: roadmap item 5 \"Billing by seat\" in Doing, at place 2 of the roadmap (brigade roadmap order places it; brigade roadmap put <work item> 5 puts a work item under it)\n" {
		t.Fatalf("add to a list = %q", got)
	}

	for _, step := range []struct {
		args []string
		want string
	}{
		{[]string{"put", "1", "4"}, "put: 1 \"Fix the login redirect\" is under roadmap item 4 \"Sign-in works everywhere\"\n"},
		{[]string{"put", "#1", "#4"}, "unchanged: 1 \"Fix the login redirect\" is already under roadmap item 4 \"Sign-in works everywhere\"\n"},
		{[]string{"put", "2", "4"}, "put: 2 \"Add the reset mail\" is under roadmap item 4 \"Sign-in works everywhere\"\n"},
		{[]string{"put", "3", "none"}, "unchanged: 3 \"Bump the toolchain\" is under no roadmap item\n"},
		{[]string{"order", "5"}, "ordered: the roadmap is now\n1. 5 \"Billing by seat\" (Doing): no work items under it\n2. 4 \"Sign-in works everywhere\" (Todo): 0 of 2 finished\n"},
		{[]string{"order", "5", "4"}, "unchanged: the roadmap is already in this order\n1. 5 \"Billing by seat\" (Doing): no work items under it\n2. 4 \"Sign-in works everywhere\" (Todo): 0 of 2 finished\n"},
	} {
		if got := f.roadmap(t, RoadmapOptions{}, "", step.args...); got != step.want {
			t.Fatalf("roadmap %v =\n%s\nwant\n%s", step.args, got, step.want)
		}
	}
	f.workItem(t, WorkItemOptions{}, "", "move", "1", "done")
	want = "1. 5 \"Billing by seat\" (Doing): no work items under it\n" +
		"2. 4 \"Sign-in works everywhere\" (Todo): 1 of 2 finished\n" +
		"     NUMBER  LIST  ASSIGNED           TITLE\n" +
		"     2       Todo  alice@example.com  Add the reset mail\n" +
		"under no roadmap item: 1 open work item that is not finished (brigade work-item list shows the board)\n"
	if got := f.roadmap(t, RoadmapOptions{}, ""); got != want {
		t.Fatalf("the roadmap =\n%s\nwant\n%s", got, want)
	}

	// A work item goes from one roadmap item to another, and off.
	if got := f.roadmap(t, RoadmapOptions{}, "", "put", "2", "5"); got != "put: 2 \"Add the reset mail\" is under roadmap item 5 \"Billing by seat\"; it was under 4 \"Sign-in works everywhere\"\n" {
		t.Fatalf("a second put = %q", got)
	}
	if got := f.roadmap(t, RoadmapOptions{}, "", "put", "2", "NONE"); got != "taken off: 2 \"Add the reset mail\" is under no roadmap item now; it was under 5 \"Billing by seat\"\n" {
		t.Fatalf("put none = %q", got)
	}
	b := readBoard(t, folder)
	if it := b.ByNumber(2)[0]; it.Roadmap != "" || b.RoadmapOf(it) != nil {
		t.Errorf("the work item taken off is still under %q", it.Roadmap)
	}
	if !b.ByNumber(4)[0].IsRoadmap() || b.ByNumber(1)[0].IsRoadmap() {
		t.Error("the kinds on the board are not a roadmap item's and a work item's")
	}

	// A closed roadmap item is off the roadmap and named in its last line,
	// so the number `reopen` needs is always in reach; what was under it is
	// under none; and a work item in a list the team hid is counted apart.
	f.roadmap(t, RoadmapOptions{}, "", "put", "2", "5")
	f.workItem(t, WorkItemOptions{}, "", "close", "5")
	f.workItem(t, WorkItemOptions{}, "", "lists", "Todo", "Doing", "Done", "Parked")
	f.workItem(t, WorkItemOptions{}, "", "hide-list", "parked")
	f.workItem(t, WorkItemOptions{}, "", "move", "3", "parked")
	want = "1. 4 \"Sign-in works everywhere\" (Todo): 1 of 1 finished\n" +
		"under no roadmap item: 1 open work item that is not finished (brigade work-item list shows the board), and 1 more in the lists the team hid (brigade work-item list --all shows them)\n" +
		"closed, and off the roadmap: 5 \"Billing by seat\"; brigade work-item reopen <number> puts one back, and the work items under it with it\n"
	if got := f.roadmap(t, RoadmapOptions{}, ""); got != want {
		t.Fatalf("the roadmap with a closed roadmap item =\n%s\nwant\n%s", got, want)
	}
	want = "no closed work items\nclosed by list: Todo (0), Doing (0), Done (0), Parked (0); closed roadmap items: 1 (brigade roadmap names them)\n"
	if got := f.workItem(t, WorkItemOptions{Closed: true}, "", "list"); got != want {
		t.Fatalf("the closed listing =\n%s\nwant\n%s", got, want)
	}
	f.workItem(t, WorkItemOptions{}, "", "reopen", "5")
	if got := f.roadmap(t, RoadmapOptions{}, ""); !strings.HasPrefix(got, "1. 5 \"Billing by seat\" (Doing): 0 of 1 finished\n") || strings.Contains(got, "closed, and off") {
		t.Fatalf("after the reopen =\n%s", got)
	}
}

// A listing of work leaves the roadmap items out and says so; show says
// what a roadmap item carries and what a work item is under.
func TestWorkItemKnowsARoadmapItem(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "Fix the login redirect", "add")
	f.workItem(t, WorkItemOptions{}, "Add the reset mail", "add")
	f.roadmap(t, RoadmapOptions{}, "Sign-in works everywhere", "add")
	f.roadmap(t, RoadmapOptions{}, "", "put", "1", "3")
	f.roadmap(t, RoadmapOptions{}, "", "put", "2", "3")
	f.workItem(t, WorkItemOptions{}, "", "close", "2")

	want := "LIST  NUMBER  TITLE                   ASSIGNED  TAGS\n" +
		"Todo  1       Fix the login redirect  -         -\n" +
		"open by list: Todo (1), Doing (0), Done (0); roadmap items, which are no work to pick up: 1 (brigade roadmap shows them)\n"
	if got := f.workItem(t, WorkItemOptions{}, "", "list"); got != want {
		t.Fatalf("list =\n%s\nwant\n%s", got, want)
	}
	// Under a filter the count is of the roadmap items the filter takes.
	if got := f.workItem(t, WorkItemOptions{Tag: "web"}, "", "list"); got != "no open work items match\nopen by list: Todo (1), Doing (0), Done (0)\n" {
		t.Fatalf("a filtered list = %q", got)
	}
	got := f.workItem(t, WorkItemOptions{}, "", "show", "3")
	for _, line := range []string{
		"roadmap item 3: Sign-in works everywhere\n",
		"roadmap: 1 of 2 work items under it finished\n  1 (Todo) Fix the login redirect\n  2 (Todo, closed) Add the reset mail\n",
		": created in Todo as a roadmap item\n",
	} {
		if !strings.Contains(got, line) {
			t.Errorf("show of the roadmap item lacks %q:\n%s", line, got)
		}
	}
	got = f.workItem(t, WorkItemOptions{}, "", "show", "1")
	if !strings.Contains(got, "work item 1: Fix the login redirect\n") || !strings.Contains(got, "\nroadmap: under 3 \"Sign-in works everywhere\"\n") {
		t.Errorf("show of a work item under one:\n%s", got)
	}
	if !strings.Contains(got, ": put under a roadmap item\n") {
		t.Errorf("its history does not say it was put:\n%s", got)
	}

	// The same as data: a row carries its roadmap item's number, the
	// listing its count of roadmap items, show what is under and over.
	asJSON := func(run func(Invocation) error, args ...string) map[string]any {
		t.Helper()
		f.out.Reset()
		inv := f.inv(f.sessionEnv(), "", args...)
		inv.JSON = true
		if err := run(inv); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		ok, result := envelopeOf(t, f.out.String())
		if !ok {
			t.Fatalf("%v: %s", args, f.out.String())
		}
		return result
	}
	list := asJSON(func(inv Invocation) error { return WorkItem(inv, WorkItemOptions{}) }, "list")
	rows := list["items"].([]any)
	if len(rows) != 1 || rows[0].(map[string]any)["roadmap"] != float64(3) || list["roadmap_items"] != float64(1) {
		t.Errorf("list --json = %v", list)
	}
	shown := asJSON(func(inv Invocation) error { return WorkItem(inv, WorkItemOptions{}) }, "show", "1")
	if under := shown["under"].(map[string]any); under["number"] != float64(3) || under["title"] != "Sign-in works everywhere" {
		t.Errorf("show --json of a work item: under = %v", shown["under"])
	}
	shown = asJSON(func(inv Invocation) error { return WorkItem(inv, WorkItemOptions{}) }, "show", "3")
	if carries := shown["carries"].([]any); len(carries) != 2 || shown["item"].(map[string]any)["kind"] != "roadmap" {
		t.Errorf("show --json of a roadmap item = %v", shown)
	}
	road := asJSON(func(inv Invocation) error { return Roadmap(inv, RoadmapOptions{}) })
	if road["verb"] != "show" || road["note"] != RoadmapNote || road["self_session_id"] != selfSessionID || road["unplaced"] != float64(0) {
		t.Errorf("roadmap --json = %v", road)
	}
	entries := road["roadmap"].([]any)
	if len(entries) != 1 {
		t.Fatalf("roadmap --json has %d entries", len(entries))
	}
	e := entries[0].(map[string]any)
	open := e["open"].([]any)
	if e["place"] != float64(1) || e["number"] != float64(3) || e["total"] != float64(2) || e["finished"] != float64(1) ||
		len(open) != 1 || open[0].(map[string]any)["number"] != float64(1) || open[0].(map[string]any)["roadmap"] != float64(3) {
		t.Errorf("the entry = %v", e)
	}
}

func TestRoadmapRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "Fix the login redirect", "add")
	f.roadmap(t, RoadmapOptions{}, "Sign-in works everywhere", "add")
	f.roadmap(t, RoadmapOptions{}, "Billing by seat", "add")
	f.workItem(t, WorkItemOptions{}, "", "close", "3")
	before := len(readBoard(t, folder).Items)

	for _, c := range []struct {
		name   string
		stdin  string
		args   []string
		code   protocol.Code
		reason string
	}{
		{"a verb that is none", "", []string{"reorder"}, protocol.CodeUsage, ""},
		{"show as a word", "", []string{"show"}, protocol.CodeUsage, ""},
		{"a title on the command line", "", []string{"add", "Sign-in"}, protocol.CodeUsage, ""},
		{"no title", "", []string{"add"}, protocol.CodeInvalidInput, "empty"},
		{"put with one word", "", []string{"put", "1"}, protocol.CodeUsage, ""},
		{"put under a work item", "", []string{"put", "1", "1"}, protocol.CodeInvalidInput, ReasonNotARoadmapItem},
		{"a roadmap item under a roadmap item", "", []string{"put", "2", "2"}, protocol.CodeInvalidInput, ReasonIsARoadmapItem},
		{"put under a closed roadmap item", "", []string{"put", "1", "3"}, protocol.CodeInvalidInput, ReasonRoadmapClosed},
		{"put under a number the board has not", "", []string{"put", "1", "99"}, protocol.CodeNotFound, ReasonNoSuchWorkItem},
		{"put of what is no number", "", []string{"put", "one", "2"}, protocol.CodeInvalidInput, ReasonNotANumber},
		{"an order with nothing", "", []string{"order"}, protocol.CodeUsage, ""},
		{"an order that names a work item", "", []string{"order", "1"}, protocol.CodeInvalidInput, ReasonNotARoadmapItem},
		{"an order that names one twice", "", []string{"order", "2", "2"}, protocol.CodeInvalidInput, ReasonDuplicate},
		{"an order that names a closed one", "", []string{"order", "2", "3"}, protocol.CodeInvalidInput, ReasonRoadmapClosed},
	} {
		perr := f.roadmapRefused(t, c.stdin, c.args...)
		if perr.Code != c.code || (c.reason != "" && perr.Details["reason"] != c.reason) {
			t.Errorf("%s: %s %v (%s)", c.name, perr.Code, perr.Details, perr.Message)
		}
	}
	b := readBoard(t, folder)
	if len(b.Items) != before || b.ByNumber(1)[0].Roadmap != "" {
		t.Error("a refused call changed the board")
	}
	// --list belongs to add alone.
	f.out.Reset()
	if err := Roadmap(f.inv(f.sessionEnv(), "", "put", "1", "2"), RoadmapOptions{List: "doing"}); err == nil {
		t.Error("put took --list")
	}
}

func TestRoadmapNeedsASessionWithABoard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	perr := f.roadmapRefused(t, "")
	if perr.Code != protocol.CodeConfig || perr.Details["reason"] != ReasonNoBoard {
		t.Errorf("with no board: %s %v", perr.Code, perr.Details)
	}
}
