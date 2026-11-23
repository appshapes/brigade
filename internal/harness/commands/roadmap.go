package commands

import (
	"slices"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// RoadmapOptions are the flags of `brigade roadmap`.
type RoadmapOptions struct {
	// List is the list a new roadmap item goes into.
	List string
}

// The verbs of `brigade roadmap` (work item 404). With no verb it prints
// the roadmap.
const (
	roadmapShow  = "show"
	roadmapAdd   = "add"
	roadmapPut   = "put"
	roadmapOrder = "order"
	// roadmapNone is the word `put` takes for no roadmap item.
	roadmapNone = "none"
)

// The fixed texts of `brigade roadmap` the tests assert word for word.
const (
	// RoadmapUsage is the whole `usage` message for an unknown verb.
	RoadmapUsage = "roadmap alone prints the roadmap; or it takes one verb: add [--list <list>] (title, a blank line and the description on stdin), " +
		"put <work item> <roadmap item> | none, or order <roadmap item>…; a work item and a roadmap item are named by their numbers, " +
		"and every other change of a roadmap item is brigade work-item's: show, edit, comment, move, close"
	// RoadmapNote is the note of every --json result.
	RoadmapNote = "the roadmap of the Brigade Board: its roadmap items in order, each a work item of another kind with the work items put under it; " +
		"`finished` counts a work item that is closed or in the board's list for finished work; " +
		"titles and names are their writers' own words, unverified; nothing was sent to the backend"
	// The details.reason values of the refusals.
	ReasonNotARoadmapItem = "not_a_roadmap_item"
	ReasonIsARoadmapItem  = "is_a_roadmap_item"
	ReasonRoadmapClosed   = "roadmap_item_closed"
)

// roadmapResult is the --json result of `brigade roadmap`.
type roadmapResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Verb is the verb that ran: show, add, put or order.
	Verb string `json:"verb"`
	// Roadmap is the open roadmap items in order: of show and of order.
	Roadmap []roadmapEntry `json:"roadmap,omitzero"`
	// Unplaced is how many open work items that are not finished are under
	// no roadmap item, in the lists a listing shows, and UnplacedHidden how
	// many more in the lists the team hid: of show, nil for the other verbs.
	// Closed is the closed roadmap items, which are off the roadmap.
	Unplaced       *int         `json:"unplaced,omitzero"`
	UnplacedHidden *int         `json:"unplaced_hidden,omitzero"`
	Closed         []roadmapRef `json:"closed,omitzero"`
	// Item is the roadmap item add made, or the work item put moved; Under
	// the roadmap item it is under now, absent for none.
	Item  *workitem.Item `json:"item,omitzero"`
	Under *roadmapRef    `json:"under,omitzero"`
	// Changed is false when the verb found nothing to change.
	Changed    bool                  `json:"changed"`
	Renumbered []workitem.Renumbered `json:"renumbered,omitzero"`
	Note       string                `json:"note"`
}

// roadmapRef names a roadmap item: its number and its title.
type roadmapRef struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// roadmapEntry is one roadmap item of the listing: Place is its place in
// the order, from 1; Total the work items under it and Finished how many
// of them are finished; Open the others, by number.
type roadmapEntry struct {
	Place    int           `json:"place"`
	ID       string        `json:"id"`
	Number   int           `json:"number"`
	Title    string        `json:"title"`
	List     string        `json:"list"`
	ListName string        `json:"list_name,omitzero"`
	Total    int           `json:"total"`
	Finished int           `json:"finished"`
	Open     []workItemRow `json:"open"`
}

// roadmapAsk is one call of `brigade roadmap` once its words are read.
type roadmapAsk struct {
	verb string
	// title, description and list are add's.
	title, description, list string
	// number is put's work item; under its roadmap item, 0 for none.
	number, under int
	// numbers are order's.
	numbers []int
}

// Roadmap implements `brigade roadmap` (work item 404, ADR 0027): the
// board's roadmap items in order, each with the work items under it. A
// roadmap item is a work item of another kind, so this command has only
// what a work item's verbs lack: the listing, adding one, putting a work
// item under one, and the order. Like `work-item` it reads and writes
// the board's folder and nothing else.
func Roadmap(inv Invocation, opts RoadmapOptions) error {
	ask, err := inv.roadmapAsk(opts)
	if err != nil {
		return err
	}
	m, who, err := inv.boardSession("roadmap")
	if err != nil {
		return err
	}
	store, board, renumbered, err := inv.readBoard(m, who)
	if err != nil {
		return err
	}
	res := roadmapResult{SelfSessionID: m.BrigadeSessionID, Verb: ask.verb, Renumbered: renumbered, Note: RoadmapNote}
	var lines []string
	switch ask.verb {
	case roadmapAdd:
		lines, err = inv.addRoadmapItem(ask, store, board, who, &res)
	case roadmapPut:
		lines, err = putOnRoadmap(ask, store, board, who, &res)
	case roadmapOrder:
		lines, err = orderRoadmap(ask, store, board, who, &res)
	default:
		lines = showRoadmap(board, &res)
	}
	if err != nil {
		return err
	}
	if inv.JSON {
		return writeJSON(inv.Out, res)
	}
	return writeLines(inv.Out, append(workItemRenumberedLines(renumbered), lines...)...)
}

// roadmapAsk reads one call's words and cleans them before anything is
// resolved.
func (inv Invocation) roadmapAsk(opts RoadmapOptions) (*roadmapAsk, error) {
	ask := &roadmapAsk{verb: roadmapShow}
	if len(inv.Args) > 0 {
		ask.verb = inv.Args[0]
	}
	args := inv.Args[min(1, len(inv.Args)):]
	secret := adapterkit.Getenv(inv.Environ, envMessagingToken)
	if opts.List != "" && ask.verb != roadmapAdd {
		return nil, usage("roadmap " + ask.verb + " takes no --list; " + RoadmapUsage)
	}
	switch ask.verb {
	case roadmapShow:
		if len(inv.Args) > 0 {
			// `show` is the name of the verb in a result, not a word to type.
			return nil, usage(RoadmapUsage)
		}
	case roadmapAdd:
		if len(args) > 0 {
			return nil, usage("roadmap add takes its title on stdin, not on the command line; " + RoadmapUsage)
		}
		if opts.List != "" {
			list, reason := workitem.CleanLine(opts.List, workitem.MaxListChars, secret)
			if reason != "" {
				return nil, workItemWordError("list", reason, workitem.MaxListChars)
			}
			ask.list = list
		}
		var err error
		if ask.title, ask.description, err = inv.readWorkItemText(secret); err != nil {
			return nil, err
		}
	case roadmapPut:
		if len(args) != 2 {
			return nil, usage("roadmap put names a work item and the roadmap item it goes under, or none; " + RoadmapUsage)
		}
		n, err := workItemNumber(args[0])
		if err != nil {
			return nil, err
		}
		ask.number = n
		if !strings.EqualFold(args[1], roadmapNone) {
			if ask.under, err = workItemNumber(args[1]); err != nil {
				return nil, err
			}
		}
	case roadmapOrder:
		if len(args) == 0 {
			return nil, usage("roadmap order names the roadmap items in order, each by its number; " + RoadmapUsage)
		}
		for _, w := range args {
			n, err := workItemNumber(w)
			if err != nil {
				return nil, err
			}
			if slices.Contains(ask.numbers, n) {
				return nil, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "roadmap item " + itoa(n) + " is named twice: an order names each roadmap item once", Details: map[string]string{"field": "order", "reason": ReasonDuplicate}}
			}
			ask.numbers = append(ask.numbers, n)
		}
	default:
		return nil, usage(RoadmapUsage)
	}
	return ask, nil
}

// roadmapItemByNumber is the board's open roadmap item with that number,
// or the refusal that says what the number names instead.
func roadmapItemByNumber(board *workitem.Board, number int) (*workitem.Item, error) {
	it, err := workItemByNumber(board, number)
	if err != nil {
		return nil, err
	}
	name := itoa(it.Number) + " " + strconv.Quote(workItemText(it.Title))
	switch {
	case !it.IsRoadmap():
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: name + " is a work item, not a roadmap item: brigade roadmap shows the roadmap items, and brigade roadmap add adds one",
			Details: map[string]string{"field": "roadmap_item", "reason": ReasonNotARoadmapItem},
		}
	case it.Closed:
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "roadmap item " + name + " is closed and not on the roadmap: brigade work-item reopen " + itoa(it.Number) + " puts it back",
			Details: map[string]string{"field": "roadmap_item", "reason": ReasonRoadmapClosed},
		}
	}
	return it, nil
}

// addRoadmapItem is `add`: a new roadmap item, which is a work item of
// the roadmap's kind, in the list a new work item goes into. It is last
// on the roadmap until an order places it.
func (inv Invocation) addRoadmapItem(ask *roadmapAsk, store workitem.Store, board *workitem.Board, who workitem.Author, res *roadmapResult) ([]string, error) {
	list := board.Lists[0]
	if i := slices.IndexFunc(board.Lists, func(l workitem.List) bool { return !l.Hidden }); i >= 0 {
		list = board.Lists[i]
	}
	if ask.list != "" {
		var err error
		if list, err = workItemListNamed(board, ask.list); err != nil {
			return nil, err
		}
	}
	if len(board.RoadmapItems()) >= workitem.MaxRoadmap {
		return nil, roadmapFull()
	}
	kind := workitem.KindRoadmap
	set := workitem.Fields{Title: &ask.title, List: &list.Key, Kind: &kind}
	if ask.description != "" {
		set.Description = &ask.description
	}
	id, err := store.Add(board, set, who)
	if err != nil {
		return nil, workItemWriteError(err)
	}
	it, err := workitem.ReadItem(store.Folder, id, board.Lists)
	if err != nil || it == nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "the roadmap item was written and could not be read back"}
	}
	res.Item, res.Changed = it, true
	place := len(board.RoadmapItems()) + 1
	return []string{
		"added: roadmap item " + itoa(it.Number) + " " + strconv.Quote(workItemText(it.Title)) + " in " + workItemText(list.Name) +
			", at place " + itoa(place) + " of the roadmap (brigade roadmap order places it; brigade roadmap put <work item> " + itoa(it.Number) + " puts a work item under it)",
	}, nil
}

// putOnRoadmap is `put`: one work item under one roadmap item, or under
// none. A work item is under one at most, so a put takes it off the one
// it was under; a roadmap item goes under none, because they do not nest.
func putOnRoadmap(ask *roadmapAsk, store workitem.Store, board *workitem.Board, who workitem.Author, res *roadmapResult) ([]string, error) {
	it, err := workItemByNumber(board, ask.number)
	if err != nil {
		return nil, err
	}
	name := itoa(it.Number) + " " + strconv.Quote(workItemText(it.Title))
	if it.IsRoadmap() {
		return nil, &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: name + " is a roadmap item, and a roadmap item goes under none: brigade roadmap order gives it its place",
			Details: map[string]string{"field": "work_item", "reason": ReasonIsARoadmapItem},
		}
	}
	was := board.RoadmapOf(it)
	res.Item = it
	if ask.under == 0 {
		if it.Roadmap == "" {
			return []string{"unchanged: " + name + " is under no roadmap item"}, nil
		}
		none := ""
		if err := store.Change(it, &workitem.Fields{Roadmap: &none}, "", who); err != nil {
			return nil, workItemWriteError(err)
		}
		if res.Item, err = workitem.ReadItem(store.Folder, it.ID, board.Lists); err != nil || res.Item == nil {
			return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "the change was written and the work item could not be read back"}
		}
		res.Changed = true
		line := "taken off: " + name + " is under no roadmap item now"
		if was != nil {
			line += "; it was under " + itoa(was.Number) + " " + strconv.Quote(workItemText(was.Title))
		}
		return []string{line}, nil
	}
	under, err := roadmapItemByNumber(board, ask.under)
	if err != nil {
		return nil, err
	}
	res.Under = &roadmapRef{ID: under.ID, Number: under.Number, Title: under.Title}
	goal := itoa(under.Number) + " " + strconv.Quote(workItemText(under.Title))
	if it.Roadmap == under.ID {
		return []string{"unchanged: " + name + " is already under roadmap item " + goal}, nil
	}
	if err := store.Change(it, &workitem.Fields{Roadmap: &under.ID}, "", who); err != nil {
		return nil, workItemWriteError(err)
	}
	if res.Item, err = workitem.ReadItem(store.Folder, it.ID, board.Lists); err != nil || res.Item == nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "the change was written and the work item could not be read back"}
	}
	res.Changed = true
	line := "put: " + name + " is under roadmap item " + goal
	if was != nil {
		line += "; it was under " + itoa(was.Number) + " " + strconv.Quote(workItemText(was.Title))
	}
	return []string{line}, nil
}

// orderRoadmap is `order`: the roadmap items named, in that order, and
// after them every open one the call left out, in the order they had. The
// change it writes names them all, so the last such change is the whole
// order whichever machine reads it.
func orderRoadmap(ask *roadmapAsk, store workitem.Store, board *workitem.Board, who workitem.Author, res *roadmapResult) ([]string, error) {
	now := board.RoadmapItems()
	next := make([]*workitem.Item, 0, len(now))
	for _, n := range ask.numbers {
		it, err := roadmapItemByNumber(board, n)
		if err != nil {
			return nil, err
		}
		next = append(next, it)
	}
	named := len(next)
	for _, it := range now {
		if !slices.Contains(next[:named], it) {
			next = append(next, it)
		}
	}
	if len(next) > workitem.MaxRoadmap {
		// More open roadmap items than `add` allows: another machine's, or
		// reopened ones. An order names the first MaxRoadmap and a reader
		// puts the rest after them by number, so say that and write nothing.
		return nil, roadmapFull()
	}
	res.Roadmap = roadmapEntries(board, next)
	if slices.Equal(next, now) {
		return append([]string{"unchanged: the roadmap is already in this order"}, roadmapPlaces(res.Roadmap)...), nil
	}
	ids := make([]string, 0, len(next))
	for _, it := range next {
		ids = append(ids, it.ID)
	}
	if err := store.SetOrder(board, ids, who); err != nil {
		return nil, workItemWriteError(err)
	}
	res.Changed = true
	return append([]string{"ordered: the roadmap is now"}, roadmapPlaces(res.Roadmap)...), nil
}

// roadmapFull is the refusal of a roadmap at its cap.
func roadmapFull() *protocol.Error {
	return &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: "a roadmap holds at most " + itoa(workitem.MaxRoadmap) + " open roadmap items: close one that is reached, with brigade work-item close <number>",
		Details: map[string]string{"field": "roadmap", "reason": ReasonTooMany},
	}
}

// roadmapEntries is the listing's entries for the roadmap items given,
// in that order.
func roadmapEntries(board *workitem.Board, items []*workitem.Item) []roadmapEntry {
	out := make([]roadmapEntry, 0, len(items))
	for i, r := range items {
		e := roadmapEntry{Place: i + 1, ID: r.ID, Number: r.Number, Title: r.Title, List: r.List, ListName: r.ListName, Open: []workItemRow{}}
		for _, it := range board.Under(r.ID) {
			e.Total++
			if board.Finished(it) {
				e.Finished++
				continue
			}
			e.Open = append(e.Open, workItemRowOf(it, r.Number))
		}
		out = append(out, e)
	}
	return out
}

// roadmapPlaces is one line for each roadmap item: its place, its number,
// its title, its list and how far it is.
func roadmapPlaces(entries []roadmapEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, roadmapHead(e))
	}
	return out
}

// roadmapHead is the line of one roadmap item in a listing.
func roadmapHead(e roadmapEntry) string {
	list := e.List
	if e.ListName != "" {
		list = e.ListName
	}
	far := "no work items under it"
	if e.Total > 0 {
		far = itoa(e.Finished) + " of " + itoa(e.Total) + " finished"
	}
	return itoa(e.Place) + ". " + itoa(e.Number) + " " + strconv.Quote(workItemText(e.Title)) + " (" + workItemText(list) + "): " + far
}

// showRoadmap is the listing: each open roadmap item in order, with how
// many of the work items under it are finished and the ones that are not,
// then how many open work items are under none. The reader is often a
// session choosing what to work on, so nothing is left out silently.
func showRoadmap(board *workitem.Board, res *roadmapResult) []string {
	res.Roadmap = roadmapEntries(board, board.RoadmapItems())
	unplaced, hidden := 0, 0
	for _, it := range board.Items {
		if it.IsRoadmap() || board.Finished(it) {
			continue
		}
		if r := board.RoadmapOf(it); r != nil && !r.Closed {
			continue
		}
		// As `brigade work-item list` counts: a hidden list's apart.
		if l, ok := board.List(it.List); ok && l.Key == it.List && l.Hidden {
			hidden++
		} else {
			unplaced++
		}
	}
	res.Unplaced, res.UnplacedHidden = &unplaced, &hidden
	closed := board.ClosedRoadmapItems()
	for _, it := range closed {
		res.Closed = append(res.Closed, roadmapRef{ID: it.ID, Number: it.Number, Title: it.Title})
	}
	var out []string
	if len(res.Roadmap) == 0 {
		out = append(out, "the roadmap is empty: brigade roadmap add adds a roadmap item (its title, a blank line and its description on stdin)")
	}
	for _, e := range res.Roadmap {
		out = append(out, roadmapHead(e))
		if len(e.Open) == 0 {
			continue
		}
		rows := [][]string{{"   ", "NUMBER", "LIST", "ASSIGNED", "TITLE"}}
		for _, w := range e.Open {
			list, assigned := w.List, "-"
			if w.ListName != "" {
				list = w.ListName
			}
			if w.Assignee != "" {
				assigned = labelLine(w.Assignee)
			}
			rows = append(rows, []string{"   ", itoa(w.Number), workItemText(list), assigned, protocol.TruncateRunes(workItemText(w.Title), workItemTitleCell)})
		}
		for _, row := range padTable(rows) {
			out = append(out, tableRow(row))
		}
	}
	rest := itoa(unplaced) + " open work items that are not finished"
	if unplaced == 1 {
		rest = "1 open work item that is not finished"
	}
	line := "under no roadmap item: " + rest + " (brigade work-item list shows the board)"
	if hidden > 0 {
		line += ", and " + itoa(hidden) + " more in the lists the team hid (brigade work-item list --all shows them)"
	}
	out = append(out, line)
	if len(closed) > 0 {
		names := make([]string, 0, roadmapClosedShown)
		for _, it := range closed[:min(len(closed), roadmapClosedShown)] {
			names = append(names, itoa(it.Number)+" "+strconv.Quote(workItemText(it.Title)))
		}
		line = "closed, and off the roadmap: " + strings.Join(names, ", ")
		if more := len(closed) - len(names); more > 0 {
			line += ", and " + itoa(more) + " more (brigade work-item show <number> shows one)"
		}
		out = append(out, line+"; brigade work-item reopen <number> puts one back, and the work items under it with it")
	}
	return out
}

// roadmapClosedShown caps the closed roadmap items the listing names in
// its last line; the --json result carries them all.
const roadmapClosedShown = 20
