// Package boardcarry carries a Brigade Board over to another team's board
// folder (work item 272: the brigade repository's own team onto the board
// of the team its repositories join), and adds to it the open cards of a
// tracker the team leaves, each under the number it had there.
//
// A board is a folder of changes (internal/harness/workitem) and no
// change names its team. Copy therefore copies every change of one board
// folder into another: each work item arrives with its id, its number,
// its comments and its history, and the lists with their names and what
// is hidden, exactly as a reader of the first board sees them. It writes
// nothing unless the second folder is empty or holds only changes the
// first holds too, and it reads the second board back and holds it
// against the first before it reports. Run it before any session of the
// new team writes to its board: a change that arrives in the second
// folder while it runs, from a teammate's machine, is one the first does
// not hold, and the run after it is refused.
//
// Add writes cards through the store, as a session's `brigade work-item
// add` would, but under the number each card had: a number the board has
// free is kept, and a card whose number is taken gets the next free
// number above both sequences, with where it came from as the first line
// of its description.
//
// DEV-ONLY: cmd/brigade-board-carry runs it and nothing ships it.
package boardcarry

import (
	"bytes"
	"cmp"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/workitem"
)

// changeName is a change's file name as the store writes it and the
// reader takes it (workitem.changePattern).
var changeName = regexp.MustCompile(`^[0-9]{9}-[0-9]{13}-[0-9a-f]{8}\.json$`)

// A Copied is what Copy did: the work items and the changes the first
// board holds, how many of those changes it wrote (the rest were there
// already, from a run before), and what the first folder holds that is
// no change and was left behind.
type Copied struct {
	Items   int
	Changes int
	Written int
	Left    int
}

// Copy copies the board in the folder from into the folder to. Both are
// absolute paths of board folders, as `brigade sync status --json` names
// them; to may not exist yet.
func Copy(from, to string) (*Copied, error) {
	if !filepath.IsAbs(from) || !filepath.IsAbs(to) {
		return nil, errors.New("both board folders must be absolute paths")
	}
	if filepath.Clean(from) == filepath.Clean(to) {
		return nil, errors.New("the two board folders are one folder")
	}
	src, err := workitem.Read(from)
	if err != nil {
		return nil, fmt.Errorf("read the board to carry over: %w", err)
	}
	if len(src.Items) == 0 {
		return nil, errors.New("the board to carry over holds no work item")
	}
	if clashes := src.Clashes(); len(clashes) > 0 {
		return nil, fmt.Errorf("%d work items of the board to carry over share a number with an older one: run any `brigade work-item` command on its team first, which repairs it", len(clashes))
	}
	files, err := changes(from)
	if err != nil {
		return nil, err
	}
	// Nothing is written unless everything can be: the second folder is a
	// board of real directories with nothing in them that is no change,
	// and may hold only what the first holds, byte for byte.
	dst, err := workitem.Read(to)
	if err != nil {
		return nil, fmt.Errorf("read the board to carry over to: %w", err)
	}
	if dst.Skipped != 0 {
		return nil, fmt.Errorf("the board to carry over to holds %d files or directories that are no change", dst.Skipped)
	}
	there, err := changes(to)
	if err != nil {
		return nil, err
	}
	for rel, data := range there {
		if want, ok := files[rel]; !ok || !bytes.Equal(want, data) {
			return nil, fmt.Errorf("the board to carry over to holds a change of its own (%s): it must be empty, or hold only what a run before copied", rel)
		}
	}
	out := &Copied{Items: len(src.Items), Changes: len(files), Left: src.Skipped}
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		if _, ok := there[rel]; ok {
			continue
		}
		if err := write(to, rel, files[rel]); err != nil {
			return out, err
		}
		out.Written++
	}
	got, err := workitem.Read(to)
	if err != nil {
		return out, fmt.Errorf("read the board carried over: %w", err)
	}
	if got.Skipped != 0 {
		return out, fmt.Errorf("the board carried over holds %d files or directories that are no change", got.Skipped)
	}
	if a, b := folded(src), folded(got); !bytes.Equal(a, b) {
		return out, errors.New("the board carried over does not read as the board it was copied from")
	}
	return out, nil
}

// folded is a board as its readers see it: the lists and every work item
// with its comments and its history.
func folded(b *workitem.Board) []byte {
	raw, err := json.Marshal(struct {
		Lists []workitem.List
		Items []*workitem.Item
	}{b.Lists, b.Items})
	if err != nil {
		return []byte(err.Error())
	}
	return raw
}

// changes is every change of the board in folder, by its path under the
// folder: what a reader of the board takes (workitem.Read), in `lists`
// and in each work item's directory under `items`. Whatever else is
// there — a sync engine's marker, a conflict copy, a file with a change's
// name that is none — is not read. A directory of the board that is
// something else, a link above all, is an error: nothing is read through
// it. A folder that does not exist yet holds none.
func changes(folder string) (map[string][]byte, error) {
	out := map[string][]byte{}
	// list is the entries of a directory of the board, nil when it does
	// not exist yet.
	list := func(dir string) ([]os.DirEntry, error) {
		info, err := os.Lstat(dir)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("%s is not a directory", dir)
		}
		return os.ReadDir(dir)
	}
	take := func(item string, parts ...string) error {
		dir := filepath.Join(append([]string{folder}, parts...)...)
		entries, err := list(dir)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if !e.Type().IsRegular() || !changeName.MatchString(e.Name()) {
				continue
			}
			if data, ok := change(filepath.Join(dir, e.Name()), item); ok {
				out[filepath.Join(append(parts, e.Name())...)] = data
			}
		}
		return nil
	}
	if err := take("", workitem.ListsDir); err != nil {
		return nil, err
	}
	items, err := list(filepath.Join(folder, workitem.ItemsDir))
	if err != nil {
		return nil, err
	}
	for _, d := range items {
		// A symlink is not a directory to DirEntry, so none is followed.
		if !d.IsDir() || !workitem.ValidID(d.Name()) {
			continue
		}
		if err := take(d.Name(), workitem.ItemsDir, d.Name()); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// change is the bytes of the file at path when a reader of the board
// takes it for a change of item ("" for a change of the lists), by the
// reader's own rules (workitem.readChange): a regular file opened without
// following a link, of a change's size, that decodes as a change of this
// version with a sequence and a time, and names its work item. Copy's
// reading back of the second board is what holds this to the reader.
func change(path, item string) ([]byte, bool) {
	f, err := adapterkit.OpenNoFollow(path, os.O_RDONLY, 0)
	if err != nil {
		return nil, false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > workitem.MaxChangeBytes {
		return nil, false
	}
	data := make([]byte, info.Size())
	if _, err := io.ReadFull(f, data); err != nil {
		return nil, false
	}
	var c workitem.Change
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, false
	}
	if c.Version != workitem.Version || c.Seq < 1 || c.Seq > workitem.MaxSeq || c.At.IsZero() || c.Item != item {
		return nil, false
	}
	return data, true
}

// write writes one change under folder as the store does: every
// directory on the way is Brigade's own, 0700, and one that is already
// there must be a real directory; the file is written whole, 0600, and
// renamed into place.
func write(folder, rel string, data []byte) error {
	dir := folder
	for part := range strings.SplitSeq(filepath.Dir(rel), string(filepath.Separator)) {
		dir = filepath.Join(dir, part)
		if info, err := os.Lstat(dir); err == nil && !info.IsDir() {
			return fmt.Errorf("%s is not a directory", dir)
		} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	if err := adapterkit.MkdirPrivate(dir); err != nil {
		return err
	}
	return adapterkit.WriteAtomic(filepath.Join(folder, rel), data)
}

// A Card is one card of the tracker a team leaves: its number there, its
// title and description, the board's list it goes into (a key or a name;
// "" is the board's first list that is shown), its tags, and its
// comments, oldest first, each already saying who wrote it and when.
type Card struct {
	Number      int      `json:"number"`
	Title       string   `json:"title"`
	Description string   `json:"description"`
	List        string   `json:"list"`
	Tags        []string `json:"tags"`
	Comments    []string `json:"comments"`
}

// A Carried is one card on the board: the number it had, the number it
// has, whether this run wrote it or found it there from a run before, and
// how many of its comments this run wrote. A card a run before left with
// some of its comments missing is found there and given the rest.
type Carried struct {
	From     int
	Number   int
	Title    string
	Written  bool
	Comments int
}

// Add writes cards onto the board in folder, by who, under the numbering
// rule of the package's comment. origin is what a card is called where it
// came from ("Trello card"): a card whose number was taken says "<origin>
// <number>" on the first line of its description. A description or a
// comment longer than the board holds in one goes on as further
// comments, cut between lines. Every card is checked before any is
// written: a title, a text, a tag or a list the board would refuse stops
// the run with nothing written. It answers the cards that are on the
// board whole; an error names the one that is not.
func Add(folder string, cards []Card, origin string, who workitem.Author) ([]Carried, error) {
	b, err := workitem.Read(folder)
	if err != nil {
		return nil, err
	}
	if clashes := b.Clashes(); len(clashes) > 0 {
		return nil, fmt.Errorf("%d work items of the board share a number with an older one", len(clashes))
	}
	cards = slices.Clone(cards)
	slices.SortStableFunc(cards, func(x, y Card) int { return cmp.Compare(x.Number, y.Number) })
	// The next free number above both sequences: the board's and the
	// tracker's.
	above := b.NextNumber()
	for i, c := range cards {
		if c.Number < 1 || c.Number > workitem.MaxNumber {
			return nil, fmt.Errorf("card %q has the number %d, which no work item can have", c.Title, c.Number)
		}
		if i > 0 && cards[i-1].Number == c.Number {
			return nil, fmt.Errorf("two cards have the number %d", c.Number)
		}
		above = max(above, c.Number+1)
	}
	type planned struct {
		carried  Carried
		there    *workitem.Item
		set      workitem.Fields
		comments []string
	}
	var plan []planned
	for _, c := range cards {
		title, reason := workitem.CleanLine(c.Title, workitem.MaxTitleChars)
		if reason != "" {
			return nil, fmt.Errorf("card %d: its title is refused (%s)", c.Number, reason)
		}
		list, ok := listOf(b, c.List)
		if !ok {
			return nil, fmt.Errorf("card %d: the board has no list %q", c.Number, c.List)
		}
		tags := make([]string, 0, len(c.Tags))
		for i, raw := range c.Tags {
			tag, reason := workitem.CleanLine(raw, workitem.MaxTagChars)
			if reason != "" {
				return nil, fmt.Errorf("card %d: its tag %d is refused (%s)", c.Number, i+1, reason)
			}
			tags = append(tags, tag)
		}
		if len(tags) > workitem.MaxTags {
			return nil, fmt.Errorf("card %d has %d tags; a work item has at most %d", c.Number, len(tags), workitem.MaxTags)
		}
		// Where the card is, or goes: under its number when the board has
		// it free or has this very card there; under the number a run
		// before gave it; else under the next free number, saying where
		// it came from.
		p := planned{carried: Carried{From: c.Number, Number: c.Number, Title: title, Written: true}}
		description := c.Description
		from := fmt.Sprintf("%s %d", origin, c.Number)
		switch holders := b.ByNumber(c.Number); {
		case len(holders) == 0:
		case holders[0].Title == title:
			p.there = holders[0]
		default:
			description = strings.TrimSpace(from + "\n\n" + description)
			if p.there = carriedBefore(b, title, from); p.there == nil {
				p.carried.Number, above = above, above+1
			}
		}
		// What of a description is past what one holds follows it as
		// comments, before the card's own.
		var rest []string
		parts, reason := pieces(description, workitem.MaxDescriptionChars)
		if reason != "" {
			return nil, fmt.Errorf("card %d: its description is refused (%s)", c.Number, reason)
		}
		p.set = workitem.Fields{Title: &title, List: &list.Key, Tags: &tags}
		if len(parts) > 0 {
			p.set.Description, rest = &parts[0], parts[1:]
		}
		for _, raw := range append(rest, c.Comments...) {
			texts, reason := pieces(raw, workitem.MaxCommentChars)
			if reason != "" {
				return nil, fmt.Errorf("card %d: a comment of it is refused (%s)", c.Number, reason)
			}
			p.comments = append(p.comments, texts...)
		}
		if p.there != nil {
			// A run before wrote it: only the comments it did not get to
			// are written now.
			p.carried.Number, p.carried.Written = p.there.Number, false
			p.comments = slices.DeleteFunc(p.comments, func(text string) bool {
				return slices.ContainsFunc(p.there.Comments, func(had workitem.Comment) bool { return had.Text == text })
			})
		}
		plan = append(plan, p)
	}
	s := workitem.Store{Folder: folder}
	out := make([]Carried, 0, len(plan))
	for _, p := range plan {
		id := ""
		if p.there != nil {
			id = p.there.ID
		} else {
			if b, err = workitem.Read(folder); err != nil {
				return out, err
			}
			if id, err = s.AddNumbered(b, p.carried.Number, p.set, who); err != nil {
				return out, fmt.Errorf("card %d as work item %d: %w", p.carried.From, p.carried.Number, err)
			}
		}
		for _, comment := range p.comments {
			it, err := workitem.ReadItem(folder, id, b.Lists)
			if err != nil {
				return out, fmt.Errorf("card %d: work item %d as written: %w", p.carried.From, p.carried.Number, err)
			}
			if it == nil {
				return out, fmt.Errorf("card %d: work item %d is not on the board it was written to", p.carried.From, p.carried.Number)
			}
			if err := s.Change(it, nil, comment, who); err != nil {
				return out, fmt.Errorf("card %d: a comment of work item %d: %w; run it again to write the rest", p.carried.From, p.carried.Number, err)
			}
			p.carried.Comments++
		}
		out = append(out, p.carried)
	}
	return out, nil
}

// listOf is the board's list ref names, or its first list that is shown
// when ref is "".
func listOf(b *workitem.Board, ref string) (workitem.List, bool) {
	if ref != "" {
		return b.List(ref)
	}
	for _, l := range b.Lists {
		if !l.Hidden {
			return l, true
		}
	}
	return workitem.List{}, false
}

// carriedBefore is the work item a run before wrote for a card whose
// number was taken: the one with its title whose description begins
// with where it came from.
func carriedBefore(b *workitem.Board, title, from string) *workitem.Item {
	for _, it := range b.Items {
		if it.Title != title {
			continue
		}
		if first, _, _ := strings.Cut(it.Description, "\n"); strings.TrimSpace(first) == from {
			return it
		}
	}
	return nil
}

// pieces is text as the board keeps it, in pieces of at most limit
// characters: sanitised first, because that is what the cap is held
// against and sanitising can lengthen a text, then cut between lines
// where a line fits and inside one where it does not. An empty text has
// none. The reason is the board's own for a text it refuses: not UTF-8,
// or carrying a credential.
func pieces(text string, limit int) (out []string, reason string) {
	if !utf8.ValidString(text) {
		return nil, doing.ReasonNotUTF8
	}
	var lines []string
	count := 0
	flush := func() {
		if piece := strings.TrimSpace(strings.Join(lines, "\n")); piece != "" {
			out = append(out, piece)
		}
		lines, count = nil, 0
	}
	for line := range strings.SplitSeq(strings.TrimSpace(workitem.Block(text)), "\n") {
		for utf8.RuneCountInString(line) > limit {
			flush()
			runes := []rune(line)
			out, line = append(out, string(runes[:limit])), string(runes[limit:])
		}
		// A line costs its characters, and the break before it when it
		// is not a piece's first.
		cost := utf8.RuneCountInString(line)
		if len(lines) > 0 {
			cost++
		}
		if count+cost > limit {
			flush()
			cost = utf8.RuneCountInString(line)
		}
		lines, count = append(lines, line), count+cost
	}
	flush()
	for _, piece := range out {
		if _, reason := workitem.CleanBlock(piece, limit); reason != "" {
			return nil, reason
		}
	}
	return out, ""
}
