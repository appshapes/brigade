// Package workitem is the team's board (card 263,
// .context/plans/work-items.md): the work items a team keeps in Brigade
// itself, in lists, the way a kanban board keeps cards in columns. The
// board is one folder per team per machine, under Brigade's state
// directory and outside every checkout, which the sync adapter shares
// with every teammate's machine whatever repository their session is in
// (foldersync.BoardFolder). Nothing here reaches the backend.
//
// The folder holds CHANGES, never a work item as a file. A change is one
// small JSON file, written once under a name no other writer can pick
// and never rewritten: `items/<id>/<seq>-<millis>-<random>.json` says
// what one session set on one work item — its number, title, description,
// list, assignee, tags, whether it is closed — or the comment it added,
// and `lists/<seq>-<millis>-<random>.json` names the board's lists in
// order. A list is a state a work item can be in: it has a key that never
// changes, which is what a work item carries, and a name the team calls
// it by, which can change without a work item noticing. A work item is the fold of its changes: ordered by sequence,
// then time, then file name, the last change that sets a field wins.
// Two machines therefore never write one file, the sync engine never
// makes a conflict copy, and two members who change one work item at the
// same moment both keep what they wrote.
//
// A sequence is the highest one the writer has seen for the work item,
// plus one, so a change made after reading another always follows it,
// whatever the two machines' clocks say.
//
// A work item's number is the highest number the writer knows plus one.
// With no server to hand numbers out, two machines that add a work item
// before either has the other's can take the same number: then the one
// created first keeps it, and Store.Repair gives every other the next
// free number, as a change of its own that the work item's history shows.
//
// Every file is a teammate's text (docs/security.md: any teammate's
// machine can write anything into a synced folder). The readers here
// take only files whose name, size and shape are a change's, follow no
// symlink, cap and sanitise every string, and skip the rest.
package workitem

import (
	"cmp"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/protocol"
)

// Version is the change shape this package writes and reads.
const Version = 1

// The folder's layout.
const (
	ItemsDir = "items"
	ListsDir = "lists"
)

// The caps a change keeps to, whoever wrote it. The text caps are in code
// points.
const (
	// MaxChangeBytes caps a file a reader will decode: a description at
	// its cap, every code point four bytes and escaped, fits.
	MaxChangeBytes = 128 << 10
	// MaxTitleChars caps a title, MaxDescriptionChars a description and
	// MaxCommentChars a comment.
	MaxTitleChars       = 200
	MaxDescriptionChars = 8000
	MaxCommentChars     = 4000
	// MaxLists caps the board's lists, MaxListChars a list's name and
	// MaxListKeyChars its key; MaxTags a work item's tags and MaxTagChars
	// a tag.
	MaxLists        = 24
	MaxListChars    = 64
	MaxListKeyChars = 32
	MaxTags         = 12
	MaxTagChars     = 48
	// MaxItems caps the work items of a board and MaxChanges the changes
	// of one work item (and of the lists). The writer refuses past either
	// (ErrFull), so a change that is written is a change that is read; a
	// reader folds no more, the first by name, and looks at no more than
	// twice as many names in a directory, so a folder a teammate's machine
	// filled cannot cost a reader its memory or its time. Read counts what
	// it left out.
	MaxItems   = 20000
	MaxChanges = 2000
	// MaxSeq is the highest sequence, and MaxNumber the highest number, a
	// reader takes: the nine digits a file name gives a sequence.
	MaxSeq    = 999_999_999
	MaxNumber = 999_999_999
)

// idPattern is a work item's id, its directory's name: 16 hex digits.
// changePattern is a change's file name: the sequence in nine digits, the
// writer's time in milliseconds in thirteen, and eight random hex digits.
// A sync engine's own files — a conflict copy, a temporary, its marker —
// match neither.
var (
	idPattern     = regexp.MustCompile(`^[0-9a-f]{16}$`)
	changePattern = regexp.MustCompile(`^[0-9]{9}-[0-9]{13}-[0-9a-f]{8}\.json$`)
)

// ValidID reports whether id can be a work item's id.
func ValidID(id string) bool { return idPattern.MatchString(id) }

// A Change is one file of the folder: what one session set on one work
// item, or the comment it added, or the board's lists as it named them.
type Change struct {
	Version int `json:"v"`
	// Item is the work item's id, the name of the directory the file is
	// in; absent in a change of the lists.
	Item string `json:"item,omitzero"`
	// Seq orders the changes of one work item (or of the lists): the
	// highest the writer had seen, plus one.
	Seq int `json:"seq"`
	// At is the writer's clock.
	At time.Time `json:"at"`
	// By is the writer's label as the roster shows it and Session its
	// session's name: the writer's own words, like every string here.
	By      string `json:"by,omitzero"`
	Session string `json:"session,omitzero"`
	// Set is the fields this change sets; Comment the comment it adds;
	// Lists, in a change of the lists, the board's lists in order; and
	// Rename, in another, the new name of the one list whose key it
	// carries (lists.go says how the two fold).
	Set     *Fields `json:"set,omitzero"`
	Comment string  `json:"comment,omitzero"`
	Lists   []List  `json:"lists,omitzero"`
	Rename  *List   `json:"rename,omitzero"`

	// name is the file's name, the last key of the fold's order.
	name string
}

// Fields are the fields of a work item a change can set: a nil member is
// left as it was. List is a list's key. An empty Assignee is nobody; an
// empty Tags is none.
type Fields struct {
	Number      *int      `json:"number,omitzero"`
	Title       *string   `json:"title,omitzero"`
	Description *string   `json:"description,omitzero"`
	List        *string   `json:"list,omitzero"`
	Assignee    *string   `json:"assignee,omitzero"`
	Tags        *[]string `json:"tags,omitzero"`
	Closed      *bool     `json:"closed,omitzero"`
}

// An Item is one work item, folded from its changes.
type Item struct {
	// ID is the id the item's changes are kept under; Number the number
	// people and commits call it by.
	ID     string `json:"id"`
	Number int    `json:"number"`
	Title  string `json:"title"`
	// Description is free text, line breaks kept.
	Description string `json:"description,omitzero"`
	// List is the key of the list the item is in, as its last move
	// wrote it; ListName that list's name on the board now, "" for a list
	// the board no longer has.
	List     string `json:"list"`
	ListName string `json:"list_name,omitzero"`
	// Assignee is the roster label of the member the item is assigned
	// to, "" for nobody.
	Assignee string   `json:"assignee,omitzero"`
	Tags     []string `json:"tags"`
	// Closed is true for an item that is finished with: off the board's
	// lists until it is reopened.
	Closed bool `json:"closed"`
	// CreatedAt and CreatedBy are the first change's time and writer;
	// UpdatedAt the latest change's time.
	CreatedAt time.Time `json:"created_at"`
	CreatedBy string    `json:"created_by,omitzero"`
	UpdatedAt time.Time `json:"updated_at"`
	// Comments are the comments, oldest first; History one line per
	// change, oldest first.
	Comments []Comment `json:"comments"`
	History  []Event   `json:"history"`

	// seq is the highest sequence among the item's changes, and changes
	// how many of them were read.
	seq     int
	changes int
}

// A Comment is one comment on a work item.
type Comment struct {
	At      time.Time `json:"at"`
	By      string    `json:"by,omitzero"`
	Session string    `json:"session,omitzero"`
	Text    string    `json:"text"`
}

// An Event is one change of a work item, said in a few fixed words.
type Event struct {
	At      time.Time `json:"at"`
	By      string    `json:"by,omitzero"`
	Session string    `json:"session,omitzero"`
	What    string    `json:"what"`
}

// A Board is the team's board as one machine holds it now.
type Board struct {
	// Lists is the board's lists in order: those of the latest change
	// that named them, or DefaultLists when nobody named any, each under
	// the name it has now.
	Lists []List
	// Items is every work item, open and closed, by number.
	Items []*Item
	// Skipped counts what Read left out: a file or a directory in the
	// folder that is not a change or a work item, or is past a cap.
	Skipped int

	// past is the lists the board had once and has no longer, each
	// under its key and the last name it had: naming one again gives it
	// its key back, and with the key the work items left in it.
	past []List
	// listsSeq is the highest sequence among the changes of the lists,
	// and listsChanges how many of them were read.
	listsSeq     int
	listsChanges int
}

// Read folds the board in folder. A folder that does not exist yet — a
// board nobody has written to — is an empty board with the default lists.
// A directory that cannot be read is an error, never an empty board: a
// caller that took one for the other would hand out a number the board
// already has.
func Read(folder string) (*Board, error) {
	b := &Board{Items: []*Item{}}
	lists, skipped, err := readChanges(filepath.Join(folder, ListsDir), "")
	if err != nil {
		return nil, err
	}
	b.Skipped += skipped
	b.Lists, b.past = foldLists(lists)
	b.listsChanges = len(lists)
	for _, c := range lists {
		b.listsSeq = max(b.listsSeq, c.Seq)
	}
	dir := filepath.Join(folder, ItemsDir)
	entries, skipped, err := readDir(dir)
	if err != nil {
		return nil, err
	}
	b.Skipped += skipped
	for i, d := range entries {
		if i >= 2*MaxItems || len(b.Items) >= MaxItems {
			b.Skipped += len(entries) - i
			break
		}
		// A symlink is not a directory to DirEntry, so none is followed.
		if !d.IsDir() || !ValidID(d.Name()) {
			b.Skipped++
			continue
		}
		changes, skipped, err := readChanges(filepath.Join(dir, d.Name()), d.Name())
		if err != nil {
			return nil, err
		}
		b.Skipped += skipped
		if it := fold(d.Name(), changes, b.Lists); it != nil {
			b.Items = append(b.Items, it)
		}
	}
	slices.SortFunc(b.Items, func(x, y *Item) int {
		return cmp.Or(cmp.Compare(x.Number, y.Number), older(x, y))
	})
	return b, nil
}

// ReadItem folds the one work item with that id as the folder holds it
// now, without reading the rest of the board: nil when the folder holds
// no such work item. lists is the board's lists, for the names the item's
// list and its history read with.
func ReadItem(folder, id string, lists []List) (*Item, error) {
	if !ValidID(id) {
		return nil, nil
	}
	items := filepath.Join(folder, ItemsDir)
	if _, skipped, err := readDir(items); err != nil || skipped > 0 {
		return nil, err
	}
	changes, _, err := readChanges(filepath.Join(items, id), id)
	if err != nil {
		return nil, err
	}
	return fold(id, changes, lists), nil
}

// readDir lists a directory of the board, which must be a real
// directory. One that does not exist yet has no entries. A symlink, or
// anything else that is not a directory, is a teammate's file like any
// other in a synced folder: it is not followed, and counts as one thing
// skipped. Any other failure is the caller's to report.
func readDir(dir string) (entries []os.DirEntry, skipped int, err error) {
	fi, err := os.Lstat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, 0, nil
	case err != nil:
		return nil, 0, err
	case !fi.IsDir():
		return nil, 1, nil
	}
	entries, err = os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, nil
	}
	return entries, 0, err
}

// older orders two work items by when they were created, then by id: the
// order that decides which of two keeps a number both took.
func older(x, y *Item) int {
	return cmp.Or(x.CreatedAt.Compare(y.CreatedAt), strings.Compare(x.ID, y.ID))
}

// readChanges reads the changes in dir, the first MaxChanges by name,
// each sanitised: a file whose name is not a change's, that is not a
// regular file, is past MaxChangeBytes, does not decode, is of another
// version, names another work item than id, or carries no usable
// sequence or time, is counted and skipped. A dir that is no real
// directory holds no change (readDir); one that cannot be listed is an
// error.
func readChanges(dir, id string) (changes []*Change, skipped int, err error) {
	entries, skipped, err := readDir(dir)
	if err != nil {
		return nil, 0, err
	}
	for i, d := range entries {
		if i >= 2*MaxChanges || len(changes) >= MaxChanges {
			skipped += len(entries) - i
			break
		}
		if d.IsDir() || !changePattern.MatchString(d.Name()) {
			skipped++
			continue
		}
		c, err := readChange(filepath.Join(dir, d.Name()))
		if err != nil || c.Item != id {
			skipped++
			continue
		}
		c.name = d.Name()
		c.sanitize()
		changes = append(changes, c)
	}
	slices.SortStableFunc(changes, func(x, y *Change) int {
		return cmp.Or(cmp.Compare(x.Seq, y.Seq), x.At.Compare(y.At), strings.Compare(x.name, y.name))
	})
	return changes, skipped, nil
}

// readChange reads one file as a change, within the rules every read of
// a synced folder keeps: a regular file, no symlink followed, at most
// MaxChangeBytes.
func readChange(path string) (*Change, error) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() || fi.Size() > MaxChangeBytes {
		return nil, errors.New("workitem: not a change file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxChangeBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxChangeBytes {
		return nil, errors.New("workitem: the change is larger than a change can be")
	}
	var c Change
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, errors.New("workitem: the change is not a JSON document of the expected shape")
	}
	if c.Version != Version || c.Seq < 1 || c.Seq > MaxSeq || c.At.IsZero() {
		return nil, errors.New("workitem: the change is of another version, or carries no sequence or time")
	}
	return &c, nil
}

// sanitize caps and sanitises every string of a change read from the
// folder, and drops a member that is not usable: a number out of range,
// an empty title or list.
func (c *Change) sanitize() {
	c.By = protocol.SanitizeLabel(Line(c.By))
	c.Session = protocol.OneLineName(c.Session)
	c.Comment = protocol.TruncateRunes(Block(c.Comment), MaxCommentChars)
	c.Lists = cleanLists(c.Lists)
	if r := c.Rename; r != nil {
		r.Name = protocol.TruncateRunes(Line(r.Name), MaxListChars)
		if !ValidListKey(r.Key) || r.Name == "" {
			c.Rename = nil
		}
	}
	s := c.Set
	if s == nil {
		return
	}
	if s.Number != nil && (*s.Number < 1 || *s.Number > MaxNumber) {
		s.Number = nil
	}
	if s.Title != nil {
		if t := protocol.TruncateRunes(Line(*s.Title), MaxTitleChars); t != "" {
			s.Title = &t
		} else {
			s.Title = nil
		}
	}
	if s.Description != nil {
		d := protocol.TruncateRunes(Block(*s.Description), MaxDescriptionChars)
		s.Description = &d
	}
	if s.List != nil && !ValidListKey(*s.List) {
		s.List = nil
	}
	if s.Assignee != nil {
		a := protocol.SanitizeLabel(Line(*s.Assignee))
		s.Assignee = &a
	}
	if s.Tags != nil {
		l := cleanNames(*s.Tags, MaxTags, MaxTagChars)
		s.Tags = &l
	}
}

// Line folds s onto one sanitised line: protocol.Sanitize (repair, NFC,
// control characters and the frame's tags), then every run of whitespace
// to one space.
func Line(s string) string {
	return strings.Join(strings.Fields(protocol.Sanitize(s)), " ")
}

// Block sanitises a text that keeps its line breaks (a description, a
// comment): protocol.Sanitize, which also removes a carriage return, then
// the blank lines and spaces around the whole.
func Block(s string) string {
	return strings.TrimSpace(protocol.Sanitize(s))
}

// cleanNames is a list of names — a work item's tags — each on one
// sanitised line of at most chars code points, the
// empty ones and the repeats (compared without case) dropped, at most
// limit of them. Never nil.
func cleanNames(names []string, limit, chars int) []string {
	out := make([]string, 0, min(len(names), limit))
	for _, n := range names {
		n = protocol.TruncateRunes(Line(n), chars)
		if n == "" || len(out) >= limit || slices.ContainsFunc(out, func(o string) bool { return strings.EqualFold(o, n) }) {
			continue
		}
		out = append(out, n)
	}
	return out
}

// fold is the work item its changes make, in the order readChanges gave
// them, or nil while they make none: an item has a number, a title and a
// list, which its first change sets, so one whose first change has not
// reached this machine yet is not shown until it has. lists is the
// board's lists: the item's list and its history read with the names
// they have now, and with the key for a list the board no longer has.
func fold(id string, changes []*Change, lists []List) *Item {
	if len(changes) == 0 {
		return nil
	}
	shown := func(key string) string {
		if l, ok := byKey(lists, key); ok {
			return l.Name
		}
		return key
	}
	it := &Item{ID: id, Tags: []string{}, Comments: []Comment{}, History: []Event{}, changes: len(changes)}
	it.CreatedAt, it.CreatedBy = changes[0].At, changes[0].By
	for i, c := range changes {
		it.seq = max(it.seq, c.Seq)
		if c.At.After(it.UpdatedAt) {
			it.UpdatedAt = c.At
		}
		what := it.apply(c.Set, shown)
		if i == 0 {
			// The first change is the item's creation, whatever it set.
			what = []string{"created"}
			if it.List != "" {
				what = []string{"created in " + shown(it.List)}
			}
			if it.Assignee != "" {
				what = append(what, "assigned to "+it.Assignee)
			}
		}
		if c.Comment != "" {
			it.Comments = append(it.Comments, Comment{At: c.At, By: c.By, Session: c.Session, Text: c.Comment})
			what = append(what, "commented")
		}
		if len(what) > 0 {
			it.History = append(it.History, Event{At: c.At, By: c.By, Session: c.Session, What: strings.Join(what, "; ")})
		}
	}
	if it.Number == 0 || it.Title == "" || it.List == "" {
		return nil
	}
	if l, ok := byKey(lists, it.List); ok {
		it.ListName = l.Name
	}
	return it
}

// apply sets on the item the fields s sets, and says what changed, each
// in a few fixed words; a field set to the value it has says nothing.
// shown is a list's name, or its key when the board has no such list.
func (it *Item) apply(s *Fields, shown func(key string) string) []string {
	if s == nil {
		return nil
	}
	var what []string
	if s.Number != nil && *s.Number != it.Number {
		if it.Number != 0 {
			what = append(what, "renumbered from "+strconv.Itoa(it.Number)+" to "+strconv.Itoa(*s.Number))
		}
		it.Number = *s.Number
	}
	if s.Title != nil && *s.Title != it.Title {
		if it.Title != "" {
			what = append(what, "retitled")
		}
		it.Title = *s.Title
	}
	if s.Description != nil && *s.Description != it.Description {
		what = append(what, "description changed")
		it.Description = *s.Description
	}
	if s.List != nil && *s.List != it.List {
		what = append(what, "moved to "+shown(*s.List))
		it.List = *s.List
	}
	if s.Assignee != nil && *s.Assignee != it.Assignee {
		if *s.Assignee == "" {
			what = append(what, "unassigned")
		} else {
			what = append(what, "assigned to "+*s.Assignee)
		}
		it.Assignee = *s.Assignee
	}
	if s.Tags != nil && !slices.Equal(*s.Tags, it.Tags) {
		if len(*s.Tags) == 0 {
			what = append(what, "tags removed")
		} else {
			what = append(what, "tagged "+strings.Join(*s.Tags, ", "))
		}
		it.Tags = *s.Tags
	}
	if s.Closed != nil && *s.Closed != it.Closed {
		if *s.Closed {
			what = append(what, "closed")
		} else {
			what = append(what, "reopened")
		}
		it.Closed = *s.Closed
	}
	return what
}

// ListShown is the list a reader is shown the item in: the list's name,
// or its key for a list the board no longer has.
func (it *Item) ListShown() string {
	if it.ListName != "" {
		return it.ListName
	}
	return it.List
}

// ByID is the work item with that id, or nil.
func (b *Board) ByID(id string) *Item {
	for _, it := range b.Items {
		if it.ID == id {
			return it
		}
	}
	return nil
}

// ByNumber is every work item that carries the number: one, or several
// between two machines taking it at the same moment and Store.Repair.
func (b *Board) ByNumber(number int) []*Item {
	var out []*Item
	for _, it := range b.Items {
		if it.Number == number {
			out = append(out, it)
		}
	}
	return out
}

// NextNumber is the number a new work item takes: the highest on the
// board, closed items included, plus one.
func (b *Board) NextNumber() int {
	highest := 0
	for _, it := range b.Items {
		highest = max(highest, it.Number)
	}
	return highest + 1
}

// Clashes is the work items that carry a number an older work item
// carries too — every item of a number but the one created first — in
// the order Store.Repair renumbers them: by number, then by creation.
// Items is already in that order.
func (b *Board) Clashes() []*Item {
	var out []*Item
	for i, it := range b.Items {
		if i > 0 && b.Items[i-1].Number == it.Number {
			out = append(out, it)
		}
	}
	return out
}

// SameMember reports whether two roster labels name one member: equal
// without case, and not empty.
func SameMember(a, b string) bool {
	return a != "" && strings.EqualFold(a, b)
}
