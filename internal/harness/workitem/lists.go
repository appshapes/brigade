// lists.go is the board's lists: the states a work item can be in. They
// are the team's own. A list has a key, which is the state and never
// changes, and a name, which is what the team calls it and can change.
//
// Two kinds of change make them, both files under `lists/`. One names
// the board's lists, all of them and in order: the last such change says
// which lists the board has. The other renames one list, by its key. A
// name is folded apart from the membership: a list is called what its
// latest rename says, or else what the change that first named it called
// it. So a rename made on one machine while another names the lists anew
// is not undone by it, and two machines that rename two lists both keep
// their rename.
//
// A list answers to two words, its key and its name, and a reference —
// what a person types for `move`, `--list` or `rename-list` — is looked
// up among the keys first and the names next. No two lists of a board
// may answer to one word: Named and Renamed refuse what would make them.

package workitem

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// A List is one of the board's lists: a state a work item can be in. Key
// is the state itself — set once, when the list is made, from the name it
// was given then (ListKey), and never changed; a work item carries it.
// Name is what the team calls the state, and renaming it moves no work
// item.
type List struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// DefaultLists is the lists of a board nobody has named lists for.
func DefaultLists() []List {
	return []List{{Key: "todo", Name: "Todo"}, {Key: "doing", Name: "Doing"}, {Key: "done", Name: "Done"}}
}

// listKeyPattern is a list's key: lower-case letters, digits and hyphens,
// starting with a letter or a digit.
var listKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidListKey reports whether key can be a list's key.
func ValidListKey(key string) bool { return listKeyPattern.MatchString(key) }

// ListKey is the key a list named name is made with: the name in lower
// case, every run of anything but a letter of a to z or a digit a hyphen,
// at most MaxListKeyChars. A name that leaves nothing — one in another
// script — gets "list-" and eight hex digits of its SHA-256.
func ListKey(name string) string {
	var b strings.Builder
	hyphen := false
	for _, r := range strings.ToLower(Line(name)) {
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			hyphen = true
			continue
		}
		if hyphen && b.Len() > 0 {
			b.WriteByte('-')
		}
		hyphen = false
		b.WriteRune(r)
	}
	key := b.String()
	if len(key) > MaxListKeyChars {
		key = strings.TrimRight(key[:MaxListKeyChars], "-")
	}
	if key == "" {
		sum := sha256.Sum256([]byte(Line(name)))
		key = "list-" + hex.EncodeToString(sum[:])[:8]
	}
	return key
}

// byKey is the list of lists with that key.
func byKey(lists []List, key string) (List, bool) {
	i := slices.IndexFunc(lists, func(l List) bool { return l.Key == key })
	if i < 0 {
		return List{}, false
	}
	return lists[i], true
}

// find is the list of lists that ref names: by its key, or — when no
// list has ref as its key — by its name, compared without case.
func find(lists []List, ref string) (List, bool) {
	ref = Line(ref)
	if l, ok := byKey(lists, strings.ToLower(ref)); ok {
		return l, true
	}
	i := slices.IndexFunc(lists, func(l List) bool { return strings.EqualFold(l.Name, ref) })
	if i < 0 {
		return List{}, false
	}
	return lists[i], true
}

// answers reports whether l answers to a word a list of lists answers
// to already: the same key or the same name, or a name that is the
// other's key.
func answers(lists []List, l List) bool {
	return slices.ContainsFunc(lists, func(o List) bool {
		return o.Key == l.Key || strings.EqualFold(o.Name, l.Name) ||
			o.Key == strings.ToLower(l.Name) || l.Key == strings.ToLower(o.Name)
	})
}

// cleanLists is the lists a change read from the folder names: each with
// a key that can be one and a name on one sanitised line of at most
// MaxListChars code points, a key already taken dropped, at most
// MaxLists of them.
func cleanLists(lists []List) []List {
	out := make([]List, 0, min(len(lists), MaxLists))
	for _, l := range lists {
		l.Name = protocol.TruncateRunes(Line(l.Name), MaxListChars)
		if _, taken := byKey(out, l.Key); !ValidListKey(l.Key) || l.Name == "" || taken || len(out) >= MaxLists {
			continue
		}
		out = append(out, l)
	}
	return out
}

// foldLists is the board's lists as its changes make them, in the order
// readChanges gave them: the lists of the last change that named any
// (DefaultLists when none did), each under the name its latest rename
// gave it, or else the one it was first named with; and past, the lists
// the board had before and has no longer, the same way.
func foldLists(changes []*Change) (lists, past []List) {
	first := map[string]string{}   // the name a key was first given
	renamed := map[string]string{} // the name its latest rename gave it
	var seen []string              // every key, in the order it was first named
	current := make([]string, 0, MaxLists)
	note := func(l List) {
		if _, ok := first[l.Key]; !ok {
			first[l.Key] = l.Name
			seen = append(seen, l.Key)
		}
	}
	for _, l := range DefaultLists() {
		note(l)
		current = append(current, l.Key)
	}
	for _, c := range changes {
		if len(c.Lists) > 0 {
			current = current[:0]
			for _, l := range c.Lists {
				note(l)
				current = append(current, l.Key)
			}
		}
		if c.Rename != nil {
			renamed[c.Rename.Key] = c.Rename.Name
		}
	}
	named := func(key string) List {
		if n, ok := renamed[key]; ok {
			return List{Key: key, Name: n}
		}
		return List{Key: key, Name: first[key]}
	}
	for _, key := range current {
		lists = append(lists, named(key))
	}
	for _, key := range seen {
		if !slices.Contains(current, key) {
			past = append(past, named(key))
		}
	}
	return lists, past
}

// List is the board's list that ref names, by its key or by its name;
// false when the board has no such list.
func (b *Board) List(ref string) (List, bool) { return find(b.Lists, ref) }

// Named is the board's lists as names asks for them, in that order. A
// name that is a list the board has, by its key or its name, is that
// list, as it is. One that is a list the board had before is that list
// again, with the key it had — and so with the work items left in it.
// Any other name is a new list, whose key is ListKey of the name, with a
// number after it when a list of the board, now or before, or of the new
// set answers to that word. ok is false, and clash the name that did it,
// when two of the names are one list, or when two of the lists would
// answer to one word.
func (b *Board) Named(names []string) (lists []List, clash string, ok bool) {
	lists = make([]List, 0, len(names))
	for _, n := range names {
		n = Line(n)
		l, found := find(b.Lists, n)
		if !found {
			l, found = find(b.past, n)
		}
		if !found {
			l = List{Key: b.newKey(n, lists), Name: n}
		}
		if answers(lists, l) {
			return nil, n, false
		}
		lists = append(lists, l)
	}
	return lists, "", true
}

// newKey is the key of a new list named name: ListKey of the name, or
// that and a number, so that it is no key and no name a list of the
// board, now or before, or of set already answers to. A new list never
// takes a key work items may still carry.
func (b *Board) newKey(name string, set []List) string {
	base := ListKey(name)
	taken := func(key string) bool {
		for _, lists := range [][]List{b.Lists, b.past, set} {
			if slices.ContainsFunc(lists, func(o List) bool { return o.Key == key || strings.ToLower(o.Name) == key }) {
				return true
			}
		}
		return false
	}
	key := base
	for i := 2; taken(key); i++ {
		suffix := "-" + strconv.Itoa(i)
		stem := base
		if len(stem)+len(suffix) > MaxListKeyChars {
			stem = strings.TrimRight(stem[:MaxListKeyChars-len(suffix)], "-")
		}
		key = stem + suffix
	}
	return key
}

// Renamed is the list ref names as it was and as it would be called
// name: its key, its place and the work items in it stay. found is false
// when the board has no such list, and taken true when another list of
// the board already answers to name, as its name or as its key.
func (b *Board) Renamed(ref, name string) (was, renamed List, found, taken bool) {
	was, found = find(b.Lists, ref)
	if !found {
		return List{}, List{}, false, false
	}
	renamed = List{Key: was.Key, Name: Line(name)}
	others := slices.DeleteFunc(slices.Clone(b.Lists), func(o List) bool { return o.Key == was.Key })
	if slices.ContainsFunc(others, func(o List) bool {
		return strings.EqualFold(o.Name, renamed.Name) || o.Key == strings.ToLower(renamed.Name)
	}) {
		return was, List{}, true, true
	}
	return was, renamed, true, false
}
