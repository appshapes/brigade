// store.go is the board's writer: every act — adding a work item,
// changing one, commenting, naming the lists, renumbering — is one new
// change file, and nothing is ever rewritten or removed.

package workitem

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/doing"
)

// An Author is who a change is by: the member's label as the roster
// shows it and the session's name. Either may be empty; the writer's own
// words to every reader.
type Author struct {
	Member  string
	Session string
}

// A Store writes changes into one board folder. Now and Rand are the
// clock and the randomness of a file's name; nil is the real thing.
type Store struct {
	Folder string
	Now    func() time.Time
	Rand   io.Reader
}

// ErrFull is a board at a cap: a work item, or the lists, with MaxChanges
// changes or at MaxSeq, a board with MaxItems work items, or one whose
// highest number is MaxNumber. The writer refuses there, because a reader
// would not read what it wrote.
var ErrFull = errors.New("workitem: the board is at a cap; no further change can follow")

// errNotADirectory is a directory of the board that is something else:
// a symlink a teammate's machine put where `items`, `lists` or a work
// item's directory belongs. Nothing is written through it.
var errNotADirectory = errors.New("workitem: a directory of the board is not a directory")

// Add writes a new work item's first change: a fresh id, the board's
// next number, sequence one, and the fields set gives it — a title and a
// list at least, which the caller checked. It returns the id.
func (s Store) Add(b *Board, set Fields, who Author) (string, error) {
	if len(b.Items) >= MaxItems || b.NextNumber() > MaxNumber {
		return "", ErrFull
	}
	var raw [8]byte
	if _, err := io.ReadFull(s.random(), raw[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(raw[:])
	number := b.NextNumber()
	set.Number = &number
	c := &Change{Item: id, Seq: 1, By: who.Member, Session: who.Session, Set: &set}
	return id, s.write(c, ItemsDir, id)
}

// Change writes one change of an existing work item, after every change
// of it this machine holds: the fields set gives it, the comment, or
// both.
func (s Store) Change(it *Item, set *Fields, comment string, who Author) error {
	if it.seq >= MaxSeq || it.changes >= MaxChanges {
		return ErrFull
	}
	c := &Change{Item: it.ID, Seq: it.seq + 1, By: who.Member, Session: who.Session, Set: set, Comment: comment}
	return s.write(c, ItemsDir, it.ID)
}

// SetLists writes the board's lists, in order: the whole list, which
// replaces the one before. A work item in a list the new ones leave out
// stays where it is, and shows under that list's name.
func (s Store) SetLists(b *Board, lists []string, who Author) error {
	if b.listsSeq >= MaxSeq || b.listsChanges >= MaxChanges {
		return ErrFull
	}
	c := &Change{Seq: b.listsSeq + 1, By: who.Member, Session: who.Session, Lists: lists}
	return s.write(c, ListsDir)
}

// A Renumbered is one work item Repair gave another number: the item as
// it was, the number it has now, and the title of the item that kept the
// number both had taken.
type Renumbered struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	From  int    `json:"from"`
	To    int    `json:"to"`
	Kept  string `json:"kept"`
}

// Repair gives every work item of b.Clashes the next free number, one
// change each, and reports them. Two machines that repair the same board
// write the same numbers; two that hold different boards may not, and the
// later change wins until the next repair finds what is left. The first
// failure stops it and is returned with what was done.
func (s Store) Repair(b *Board, who Author) ([]Renumbered, error) {
	next := b.NextNumber()
	var done []Renumbered
	for _, it := range b.Clashes() {
		if next > MaxNumber {
			return done, ErrFull
		}
		number := next
		if err := s.Change(it, &Fields{Number: &number}, "", who); err != nil {
			return done, err
		}
		kept := ""
		if first := b.ByNumber(it.Number); len(first) > 0 {
			kept = first[0].Title
		}
		done = append(done, Renumbered{ID: it.ID, Title: it.Title, From: it.Number, To: number, Kept: kept})
		next++
	}
	return done, nil
}

// write stamps a change and writes it once into the directory parts
// name under the folder, under a name no other writer picks: the
// sequence, this machine's clock in milliseconds and eight random hex
// digits. The directories are Brigade's own, 0700 like the state
// directory they are under, and each one that is already there must be a
// real directory: a symlink where one belongs is a teammate's, and
// nothing is written through it. The file is written whole and renamed
// into place, so a reader — the sync engine included — never sees half
// of one.
func (s Store) write(c *Change, parts ...string) error {
	if !filepath.IsAbs(s.Folder) {
		return errors.New("workitem: the board folder must be an absolute path")
	}
	dir := s.Folder
	for _, part := range parts {
		dir = filepath.Join(dir, part)
		fi, err := os.Lstat(dir)
		if err == nil && !fi.IsDir() {
			return errNotADirectory
		}
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	c.Version, c.At = Version, now().UTC()
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if len(data) > MaxChangeBytes {
		return errors.New("workitem: the change is larger than a change can be")
	}
	var raw [4]byte
	if _, err := io.ReadFull(s.random(), raw[:]); err != nil {
		return err
	}
	name := fmt.Sprintf("%09d-%013d-%s.json", c.Seq, c.At.UnixMilli(), hex.EncodeToString(raw[:]))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return adapterkit.WriteAtomic(filepath.Join(dir, name), data)
}

func (s Store) random() io.Reader {
	if s.Rand != nil {
		return s.Rand
	}
	return rand.Reader
}

// CleanLine turns what a caller typed for a one-line field — a title, a
// list's name, a label — into the text a change may carry, or answers
// the reason it is refused for, in package doing's words: not valid
// UTF-8, empty once sanitised and folded onto one line, longer than
// maxChars code points, or carrying a credential (doing.CarriesCredential,
// with secrets). Nothing is cut: a text past its cap is refused. Unlike a
// doing line, a work item may name a path.
func CleanLine(raw string, maxChars int, secrets ...string) (text, reason string) {
	if !utf8.ValidString(raw) {
		return "", doing.ReasonNotUTF8
	}
	return checked(Line(raw), maxChars, secrets)
}

// CleanBlock is CleanLine for a text that keeps its line breaks: a
// description, a comment.
func CleanBlock(raw string, maxChars int, secrets ...string) (text, reason string) {
	if !utf8.ValidString(raw) {
		return "", doing.ReasonNotUTF8
	}
	return checked(Block(raw), maxChars, secrets)
}

// checked applies the rules after the sanitising: not empty, within the
// cap, no credential.
func checked(text string, maxChars int, secrets []string) (string, string) {
	switch {
	case text == "":
		return "", doing.ReasonEmpty
	case utf8.RuneCountInString(text) > maxChars:
		return "", doing.ReasonTooLong
	case doing.CarriesCredential(text, secrets...):
		return "", doing.ReasonSecretShaped
	}
	return text, ""
}
