// roadmap.go is the board's roadmap (work item 404, ADR 0027): the pieces
// of intent larger than one work item, in an order, each with the work
// items that carry it.
//
// A roadmap item is a work item of another kind. It has a number of the
// board's one sequence, a title, a description, a list, comments and a
// history, and every change of it is a change of a work item: only its
// Kind says what it is. A work item is under one roadmap item at most, by
// its Roadmap field, which holds that item's id. Roadmap items do not
// nest: one that carries a Roadmap of its own is read as under none.
//
// The order is kept as the lists' order is. One change under `roadmap/`
// names the roadmap items, all of them and in order, and the last such
// change is the order. So two machines that reorder at once end with the
// later of the two orders, and the other is still in the folder. A
// roadmap item the order does not name follows the ones it names, by
// number: one added on another machine meanwhile is never lost. An id in
// the order that is no open roadmap item of the board is passed over.
//
// A reader from before all this knows neither field and never reads the
// directory: it shows a roadmap item as one more work item in its list.

package workitem

import "slices"

// KindRoadmap is the Kind of a roadmap item.
const KindRoadmap = "roadmap"

// MaxRoadmap caps the roadmap items one change of the order names.
const MaxRoadmap = 500

// IsRoadmap reports whether the item is a roadmap item.
func (it *Item) IsRoadmap() bool { return it.Kind == KindRoadmap }

// cleanOrder is the ids a change of the roadmap read from the folder
// names: each one that can be an id, a repeat dropped, at most MaxRoadmap.
func cleanOrder(ids []string) []string {
	if len(ids) == 0 {
		return nil
	}
	out := make([]string, 0, min(len(ids), MaxRoadmap))
	for _, id := range ids {
		if !ValidID(id) || len(out) >= MaxRoadmap || slices.Contains(out, id) {
			continue
		}
		out = append(out, id)
	}
	return out
}

// Order is the ids the latest change of the roadmap names, in its order,
// as the folder holds them: what a carried board must still read as.
func (b *Board) Order() []string { return slices.Clone(b.order) }

// ClosedRoadmapItems is the board's closed roadmap items, by number: off
// the roadmap until one is reopened.
func (b *Board) ClosedRoadmapItems() []*Item {
	var out []*Item
	for _, it := range b.Items {
		if it.IsRoadmap() && it.Closed {
			out = append(out, it)
		}
	}
	return out
}

// RoadmapItems is the board's open roadmap items in order: those the
// latest order names, as it names them, then every other by number.
func (b *Board) RoadmapItems() []*Item {
	var out []*Item
	for _, id := range b.order {
		if it := b.ByID(id); it != nil && it.IsRoadmap() && !it.Closed {
			out = append(out, it)
		}
	}
	named := len(out)
	// Items is by number already.
	for _, it := range b.Items {
		if it.IsRoadmap() && !it.Closed && !slices.Contains(out[:named], it) {
			out = append(out, it)
		}
	}
	return out
}

// RoadmapOf is the roadmap item the work item is under: nil for a work
// item under none, for one whose roadmap item this board does not hold or
// holds as something else, and for a roadmap item, which is under none.
func (b *Board) RoadmapOf(it *Item) *Item {
	if it.IsRoadmap() || it.Roadmap == "" {
		return nil
	}
	if r := b.ByID(it.Roadmap); r != nil && r.IsRoadmap() {
		return r
	}
	return nil
}

// Under is the work items under the roadmap item with that id, by number,
// the closed ones too.
func (b *Board) Under(id string) []*Item {
	var out []*Item
	for _, it := range b.Items {
		if !it.IsRoadmap() && it.Roadmap == id {
			out = append(out, it)
		}
	}
	return out
}

// FinishedList is the board's list for finished work: the one whose key
// is `done`, or else the last list the board shows, which is what the
// package's skills tell a session to move finished work to. A list the
// team hid holds work set aside, never finished work, so a board whose
// lists are all hidden and none keyed `done` has no such list.
func (b *Board) FinishedList() (List, bool) {
	if l, ok := byKey(b.Lists, "done"); ok {
		return l, true
	}
	for i := len(b.Lists) - 1; i >= 0; i-- {
		if !b.Lists[i].Hidden {
			return b.Lists[i], true
		}
	}
	return List{}, false
}

// Finished reports whether the work item is finished: closed, or in the
// board's list for finished work.
func (b *Board) Finished(it *Item) bool {
	if it.Closed {
		return true
	}
	l, ok := b.FinishedList()
	return ok && it.List == l.Key
}
