package workitem

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// addRoadmap adds a roadmap item titled title to the first list and
// returns its id, as `brigade roadmap add` does.
func addRoadmap(t *testing.T, s Store, title string, who Author) string {
	t.Helper()
	b := read(t, s.Folder)
	id, err := s.Add(b, Fields{Title: &title, List: &b.Lists[0].Key, Kind: ptr(KindRoadmap)}, who)
	if err != nil {
		t.Fatalf("add roadmap item %q: %v", title, err)
	}
	return id
}

// order writes the roadmap's order as the folder holds the board now.
func order(t *testing.T, s Store, who Author, ids ...string) {
	t.Helper()
	if err := s.SetOrder(read(t, s.Folder), ids, who); err != nil {
		t.Fatalf("order: %v", err)
	}
}

// titles is the items' titles, comma separated.
func titles(items []*Item) string {
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, it.Title)
	}
	return strings.Join(out, ",")
}

func TestARoadmapItemIsAWorkItemOfAnotherKind(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	work := add(t, s, "Fix the login redirect", alice)
	goal := addRoadmap(t, s, "Sign-in works everywhere", alice)

	b := read(t, s.Folder)
	g := b.ByID(goal)
	if g == nil || !g.IsRoadmap() || g.Number != 2 || g.List != "todo" {
		t.Fatalf("the roadmap item = %+v", g)
	}
	if got := g.History[0].What; got != "created in Todo as a roadmap item" {
		t.Errorf("its first event = %q", got)
	}
	if b.ByID(work).IsRoadmap() {
		t.Error("a work item reads as a roadmap item")
	}
	// Every change of a work item is a change of a roadmap item.
	change(t, s, goal, &Fields{List: ptr("doing"), Title: ptr("Sign-in works in every browser")}, "Started.", bob)
	g = read(t, s.Folder).ByID(goal)
	if g.List != "doing" || g.Title != "Sign-in works in every browser" || len(g.Comments) != 1 || !g.IsRoadmap() {
		t.Errorf("after a move, a retitle and a comment: %+v", g)
	}
}

func TestAWorkItemIsUnderOneRoadmapItemAtMost(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	first := addRoadmap(t, s, "Sign-in", alice)
	second := addRoadmap(t, s, "Billing", alice)
	work := add(t, s, "Fix the login redirect", alice)
	other := add(t, s, "Bump the toolchain", alice)

	change(t, s, work, &Fields{Roadmap: &first}, "", alice)
	b := read(t, s.Folder)
	if r := b.RoadmapOf(b.ByID(work)); r == nil || r.ID != first {
		t.Fatalf("the work item is under %+v", r)
	}
	if got := titles(b.Under(first)); got != "Fix the login redirect" {
		t.Errorf("under the first = %q", got)
	}
	if b.RoadmapOf(b.ByID(other)) != nil {
		t.Error("a work item nobody put anywhere is under a roadmap item")
	}
	events := b.ByID(work).History
	if got := events[len(events)-1].What; got != "put under a roadmap item" {
		t.Errorf("the event = %q", got)
	}

	// Put under another, it is under that one alone.
	change(t, s, work, &Fields{Roadmap: &second}, "", bob)
	b = read(t, s.Folder)
	if len(b.Under(first)) != 0 || titles(b.Under(second)) != "Fix the login redirect" {
		t.Errorf("after the second put: first %q, second %q", titles(b.Under(first)), titles(b.Under(second)))
	}
	// Taken off, it is under none.
	change(t, s, work, &Fields{Roadmap: ptr("")}, "", bob)
	b = read(t, s.Folder)
	if b.RoadmapOf(b.ByID(work)) != nil || len(b.Under(second)) != 0 {
		t.Error("a work item taken off its roadmap item is still under one")
	}
	events = b.ByID(work).History
	if got := events[len(events)-1].What; got != "taken off its roadmap item" {
		t.Errorf("the event = %q", got)
	}
}

// Roadmap items do not nest, and a field that names what is no roadmap
// item of the board places a work item nowhere.
func TestWhatIsNoRoadmapItemHoldsNothing(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	goal := addRoadmap(t, s, "Sign-in", alice)
	inner := addRoadmap(t, s, "Sign-in on mobile", alice)
	plain := add(t, s, "Fix the login redirect", alice)
	stray := add(t, s, "Bump the toolchain", alice)

	change(t, s, inner, &Fields{Roadmap: &goal}, "", alice)
	change(t, s, stray, &Fields{Roadmap: &plain}, "", alice)
	change(t, s, plain, &Fields{Roadmap: ptr("0123456789abcdef")}, "", alice)
	b := read(t, s.Folder)
	if b.RoadmapOf(b.ByID(inner)) != nil || len(b.Under(goal)) != 0 {
		t.Error("a roadmap item is under a roadmap item")
	}
	if b.RoadmapOf(b.ByID(stray)) != nil {
		t.Error("a work item is under a work item")
	}
	if b.RoadmapOf(b.ByID(plain)) != nil {
		t.Error("a work item is under an id the board does not hold")
	}
}

func TestTheRoadmapsOrderIsTheLastOneNamed(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	a := addRoadmap(t, s, "A", alice)
	b := addRoadmap(t, s, "B", alice)
	d := addRoadmap(t, s, "C", alice)
	add(t, s, "a work item", alice)
	if got := titles(read(t, s.Folder).RoadmapItems()); got != "A,B,C" {
		t.Fatalf("with no order named = %q, want them by number", got)
	}
	order(t, s, alice, d, a, b)
	if got := titles(read(t, s.Folder).RoadmapItems()); got != "C,A,B" {
		t.Fatalf("after the first order = %q", got)
	}
	order(t, s, bob, b, d)
	// One the order leaves out follows the ones it names.
	if got := titles(read(t, s.Folder).RoadmapItems()); got != "B,C,A" {
		t.Fatalf("after the second order = %q", got)
	}
	// One added since follows too, and a closed one is not on the roadmap.
	e := addRoadmap(t, s, "D", alice)
	change(t, s, d, &Fields{Closed: ptr(true)}, "", alice)
	if got := titles(read(t, s.Folder).RoadmapItems()); got != "B,A,D" {
		t.Fatalf("with one added and one closed = %q", got)
	}
	// Reopened, it has the place the order gave it.
	change(t, s, d, &Fields{Closed: ptr(false)}, "", alice)
	if got := titles(read(t, s.Folder).RoadmapItems()); got != "B,C,A,D" {
		t.Fatalf("with the closed one reopened = %q", got)
	}
	_ = e
}

// Two machines reorder at once: both end with the later order, and a
// roadmap item one of them added meanwhile is on both.
func TestTwoMachinesReorderingAgree(t *testing.T) {
	c := newClock()
	one := Store{Folder: t.TempDir(), Now: c.now}
	a := addRoadmap(t, one, "A", alice)
	b := addRoadmap(t, one, "B", alice)
	two := Store{Folder: t.TempDir(), Now: c.now}
	merge(t, one.Folder, two.Folder)

	order(t, one, alice, b, a)
	addRoadmap(t, two, "C", bob)
	order(t, two, bob, a, b)
	merge(t, one.Folder, two.Folder)
	merge(t, two.Folder, one.Folder)
	for _, folder := range []string{one.Folder, two.Folder} {
		if got := titles(read(t, folder).RoadmapItems()); got != "A,B,C" {
			t.Errorf("%s: the roadmap = %q, want the later order and the added item after it", folder, got)
		}
	}
	entries, err := os.ReadDir(filepath.Join(one.Folder, RoadmapDir))
	if err != nil || len(entries) != 2 {
		t.Errorf("the folder holds %d orders (%v), want both", len(entries), err)
	}
}

func TestAnOrderReadFromTheFolderIsCleaned(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	a := addRoadmap(t, s, "A", alice)
	b := addRoadmap(t, s, "B", alice)
	work := add(t, s, "a work item", alice)
	// A teammate's machine can write anything: an id twice, what is no id,
	// a work item's id, an id the board does not hold.
	order(t, s, alice, b, "../../etc", b, work, "0123456789abcdef", a)
	got := read(t, s.Folder)
	if titles(got.RoadmapItems()) != "B,A" {
		t.Errorf("the roadmap = %q", titles(got.RoadmapItems()))
	}
	if len(got.order) != 4 {
		t.Errorf("the order kept %d ids, want the four that can be ids, once each: %v", len(got.order), got.order)
	}
	// A change in the roadmap's directory that names a work item is no
	// change of the order, and a kind that is no kind is dropped.
	odd := add(t, s, "an odd one", alice)
	change(t, s, odd, &Fields{Kind: ptr("epic"), Roadmap: ptr("not-an-id")}, "", alice)
	if it := read(t, s.Folder).ByID(odd); it.Kind != "" || it.Roadmap != "" {
		t.Errorf("a kind and a roadmap that are neither were kept: %+v", it)
	}
}

func TestFinishedIsClosedOrInTheListForFinishedWork(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	open := add(t, s, "open", alice)
	done := add(t, s, "done", alice)
	closed := add(t, s, "closed", alice)
	change(t, s, done, &Fields{List: ptr("done")}, "", alice)
	change(t, s, closed, &Fields{Closed: ptr(true)}, "", alice)
	b := read(t, s.Folder)
	if b.Finished(b.ByID(open)) || !b.Finished(b.ByID(done)) || !b.Finished(b.ByID(closed)) {
		t.Error("with the default lists: open, done, closed did not read as not finished, finished, finished")
	}
	// A board with lists of its own and none keyed `done`: the last list.
	setLists(t, s, alice, "Backlog", "Building", "Shipped")
	change(t, s, open, &Fields{List: ptr("shipped")}, "", alice)
	b = read(t, s.Folder)
	if l, ok := b.FinishedList(); !ok || l.Key != "shipped" {
		t.Errorf("the list for finished work = %+v", l)
	}
	if !b.Finished(b.ByID(open)) || b.Finished(b.ByID(done)) {
		t.Error("with lists of the team's own: the last list is not the one for finished work")
	}
	// A hidden list holds work set aside: the last list the board shows.
	setLists(t, s, alice, "Backlog", "Building", "Shipped", "Parked")
	if err := s.HideList(read(t, s.Folder), "parked", true, alice); err != nil {
		t.Fatal(err)
	}
	change(t, s, done, &Fields{List: ptr("parked")}, "", alice)
	b = read(t, s.Folder)
	if l, ok := b.FinishedList(); !ok || l.Key != "shipped" {
		t.Errorf("with the last list hidden, the list for finished work = %+v", l)
	}
	if !b.Finished(b.ByID(open)) || b.Finished(b.ByID(done)) {
		t.Error("a work item in a hidden last list read as finished")
	}
}

func TestTheOrdersWriterRefusesAtACap(t *testing.T) {
	c := newClock()
	s := Store{Folder: t.TempDir(), Now: c.now}
	a := addRoadmap(t, s, "A", alice)
	b := read(t, s.Folder)
	b.orderChanges = MaxChanges
	if err := s.SetOrder(b, []string{a}, alice); err != ErrFull { //nolint:errorlint // the sentinel itself
		t.Errorf("at MaxChanges: %v", err)
	}
}
