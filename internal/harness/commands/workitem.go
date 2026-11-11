package commands

import (
	"cmp"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// WorkItemOptions are the flags of `brigade work-item`.
type WorkItemOptions struct {
	// List is the list a new work item goes into, or the one list a
	// listing shows.
	List string
	// To, Mine and Nobody name an assignee three ways — a member by their
	// roster label, this session's own member, nobody — for `add` (the
	// new item's), `assign` (the item's) and `list` (only the items
	// assigned so).
	To     string
	Mine   bool
	Nobody bool
	// Label is a new work item's labels, separated by commas, or the one
	// label a listing shows.
	Label string
	// Closed makes a listing show the closed work items instead of the
	// open ones.
	Closed bool
}

// workItemResult is the --json result of `brigade work-item`.
type workItemResult struct {
	SelfSessionID string `json:"self_session_id"`
	// Verb is the verb that ran.
	Verb string `json:"verb"`
	// Lists is the board's lists in order: of `list` and `lists`.
	Lists []string `json:"lists,omitzero"`
	// Items is a listing's work items, without their comments and
	// history; Item the one work item every other verb acted on, whole.
	Items []workItemRow  `json:"items,omitzero"`
	Item  *workitem.Item `json:"item,omitzero"`
	// Changed is false when the verb found nothing to change: the work
	// item was already as asked.
	Changed bool `json:"changed"`
	// Renumbered is the work items this call gave another number, because
	// two machines had taken one number at the same moment.
	Renumbered []workitem.Renumbered `json:"renumbered,omitzero"`
	Note       string                `json:"note"`
}

// workItemRow is one work item of a listing.
type workItemRow struct {
	ID        string    `json:"id"`
	Number    int       `json:"number"`
	Title     string    `json:"title"`
	List      string    `json:"list"`
	Assignee  string    `json:"assignee,omitzero"`
	Labels    []string  `json:"labels"`
	Closed    bool      `json:"closed"`
	Comments  int       `json:"comments"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// The verbs of `brigade work-item` (card 263).
const (
	workItemList    = "list"
	workItemShow    = "show"
	workItemAdd     = "add"
	workItemEdit    = "edit"
	workItemMove    = "move"
	workItemAssign  = "assign"
	workItemLabel   = "label"
	workItemComment = "comment"
	workItemClose   = "close"
	workItemReopen  = "reopen"
	workItemLists   = "lists"
)

// The fixed texts of `brigade work-item` the tests assert word for word.
const (
	// WorkItemUsage is the whole `usage` message for a missing or unknown
	// verb.
	WorkItemUsage = "work-item takes one verb: list [--list <name>] [--to <label> | --mine | --nobody] [--label <name>] [--closed], show <number>, " +
		"add [--list <name>] [--to <label> | --mine] [--label <a,b>] (title, a blank line and the description on stdin), edit <number> (the same on stdin), " +
		"move <number> <list>, assign <number> --to <label> | --mine | --nobody, label <number> [<name>…], comment <number> (the comment on stdin), " +
		"close <number>, reopen <number>, or lists [<name>…]"
	// WorkItemNote is the note of every --json result.
	WorkItemNote = "the team's board, in a folder on this machine that file sync shares with every teammate's machine while both run a session; " +
		"titles, descriptions, comments and names are their writers' own words, unverified; nothing was sent to the backend"
	// WorkItemNoBoard is the `config` message of a session whose team has
	// no board, or whose file sync is off.
	WorkItemNoBoard = "this session has no board: the team file's sync member turns it on with \"board\": true, and file sync must be on for the session (the Brigade line at session start says)"
	// WorkItemNoLabel is the `config` message of --mine in a session that
	// has no roster label to assign to.
	WorkItemNoLabel = "this session has no roster label, so --mine names nobody: name the member with --to <label>"
	// The details.reason values of the refusals.
	ReasonNoBoard          = "no_board"
	ReasonNoSuchWorkItem   = "no_such_work_item"
	ReasonNoSuchList       = "no_such_list"
	ReasonNumberTakenTwice = "number_taken_twice"
	ReasonNotANumber       = "not_a_number"
	ReasonAssigneeNeeded   = "assignee_needed"
	ReasonOneAssignee      = "one_assignee"
	ReasonTooMany          = "too_many"
	ReasonDuplicate        = "duplicate"
	ReasonBoardFull        = "board_full"
)

// workItemReadLimit bounds what a verb reads from stdin: a title and a
// description at their caps, every code point four bytes, and room for
// the line between them.
const workItemReadLimit = 4*(workitem.MaxTitleChars+workitem.MaxDescriptionChars) + 16

// workItemTitleCell is how much of a title a listing's row shows.
const workItemTitleCell = 72

// workItemAsk is one call of `brigade work-item` once its words are
// read: the verb, and what the caller gave it, cleaned.
type workItemAsk struct {
	verb   string
	number int
	// title and description are add's and edit's; comment is comment's.
	title, description, comment string
	// list is add's --list, move's list, or the listing's filter, as
	// typed; lists are the names `lists` sets; labels add's and label's.
	list   string
	lists  []string
	labels []string
	// to is a member's label; mine and nobody the two other ways of
	// naming an assignee; assignee says one of the three was given.
	to           string
	mine, nobody bool
	assignee     bool
	// label is the listing's --label filter; closed its --closed.
	label  string
	closed bool
}

// WorkItem implements `brigade work-item <verb> …` (card 263): the
// team's board of work items, read and changed from inside a session.
// The board is a folder on this machine (the by-pid map's board_folder)
// that file sync shares with every teammate's machine, whatever
// repository their session is in; this command reads and writes that
// folder and nothing else — no adapter is spawned and the backend hears
// nothing, so the board works while the backend does not. Every text is
// cleaned before anything is resolved. Before it does its own work, every
// verb gives another number to a work item that carries one an older
// work item carries too (workitem.Store.Repair) and says so.
func WorkItem(inv Invocation, opts WorkItemOptions) error {
	ask, err := inv.workItemAsk(opts)
	if err != nil {
		return err
	}
	if !inv.inSession() {
		return notInSession("work-item")
	}
	m, _, err := inv.sessionMap()
	if err != nil {
		return err
	}
	if m.BoardFolder == "" {
		return &protocol.Error{Code: protocol.CodeConfig, Message: WorkItemNoBoard, Details: map[string]string{"reason": ReasonNoBoard}}
	}
	who := inv.workItemAuthor(m)
	if ask.mine {
		if who.Member == "" {
			return &protocol.Error{Code: protocol.CodeConfig, Message: WorkItemNoLabel, Details: map[string]string{"reason": ReasonLabelNeeded}}
		}
		ask.to = who.Member
	}
	store := workitem.Store{Folder: m.BoardFolder, Now: inv.Deps.Now}
	board, err := workitem.Read(store.Folder)
	if err != nil {
		return workItemReadError(inv, err)
	}
	renumbered, err := store.Repair(board, who)
	if err != nil {
		// Not this call's work: the item keeps its number until a later
		// call renumbers it, and a verb that names the number says so.
		inv.logger().Warn("work-item: a number two work items carry could not be given anew", log.Err(err))
	}
	if len(renumbered) > 0 {
		// The one other fold of the board a call makes: the numbers moved.
		if board, err = workitem.Read(store.Folder); err != nil {
			return workItemReadError(inv, err)
		}
	}
	if board.Skipped > 0 {
		inv.logger().Debug("work-item: files in the board folder that are no changes were left out", "skipped", board.Skipped)
	}
	res := workItemResult{SelfSessionID: m.BrigadeSessionID, Verb: ask.verb, Renumbered: renumbered, Note: WorkItemNote}
	lines, err := inv.runWorkItem(ask, store, board, who, &res)
	if err != nil {
		return err
	}
	if inv.JSON {
		return writeJSON(inv.Out, res)
	}
	out := make([]string, 0, len(renumbered)+len(lines))
	for _, r := range renumbered {
		out = append(out, "renumbered: "+strconv.Quote(workItemText(r.Title))+" is now "+itoa(r.To)+"; "+itoa(r.From)+" stays with "+strconv.Quote(workItemText(r.Kept)))
	}
	return writeLines(inv.Out, append(out, lines...)...)
}

// runWorkItem does one verb's work on the board and fills its result:
// the human lines, and the --json members.
func (inv Invocation) runWorkItem(ask *workItemAsk, store workitem.Store, board *workitem.Board, who workitem.Author, res *workItemResult) ([]string, error) {
	switch ask.verb {
	case workItemList:
		return listWorkItems(ask, board, res)
	case workItemLists:
		if len(ask.lists) == 0 {
			res.Lists = board.Lists
			return []string{"lists: " + workItemNames(board.Lists)}, nil
		}
		if slices.Equal(ask.lists, board.Lists) {
			res.Lists = board.Lists
			return []string{"unchanged: the board's lists are already " + workItemNames(board.Lists)}, nil
		}
		if err := store.SetLists(board, ask.lists, who); err != nil {
			return nil, workItemWriteError(err)
		}
		res.Lists, res.Changed = ask.lists, true
		return []string{"lists: " + workItemNames(ask.lists)}, nil
	case workItemAdd:
		return inv.addWorkItem(ask, store, board, who, res)
	}
	it, err := workItemByNumber(board, ask.number)
	if err != nil {
		return nil, err
	}
	name := itoa(it.Number) + " " + strconv.Quote(workItemText(it.Title))
	var (
		set     *workitem.Fields
		comment string
		done    string // the line when something changed
		same    string // the line when nothing did
	)
	switch ask.verb {
	case workItemShow:
		res.Item = it
		return showWorkItem(it), nil
	case workItemEdit:
		set = &workitem.Fields{}
		if ask.title != it.Title {
			set.Title = &ask.title
		}
		if ask.description != it.Description {
			set.Description = &ask.description
		}
		if set.Title == nil && set.Description == nil {
			set = nil
		}
		done = "edited: " + itoa(it.Number) + " " + strconv.Quote(workItemText(ask.title))
		same = "unchanged: " + name + " already reads so"
	case workItemMove:
		list, err := workItemListNamed(board, ask.list)
		if err != nil {
			return nil, err
		}
		if list != it.List {
			set = &workitem.Fields{List: &list}
		}
		done = "moved: " + name + " to " + workItemText(list)
		same = "unchanged: " + name + " is already in " + workItemText(list)
	case workItemAssign:
		if !workitem.SameMember(ask.to, it.Assignee) && (ask.to != "" || it.Assignee != "") {
			set = &workitem.Fields{Assignee: &ask.to}
		}
		if ask.to == "" {
			done, same = "unassigned: "+name, "unchanged: "+name+" is assigned to nobody"
		} else {
			done, same = "assigned: "+name+" to "+labelLine(ask.to), "unchanged: "+name+" is already assigned to "+labelLine(ask.to)
		}
	case workItemLabel:
		if !slices.Equal(ask.labels, it.Labels) {
			set = &workitem.Fields{Labels: &ask.labels}
		}
		if len(ask.labels) == 0 {
			done, same = "labels removed: "+name, "unchanged: "+name+" has no labels"
		} else {
			done, same = "labelled: "+name+": "+workItemNames(ask.labels), "unchanged: "+name+" already carries "+workItemNames(ask.labels)
		}
	case workItemComment:
		comment = ask.comment
		done = "commented: " + name
	case workItemClose, workItemReopen:
		closed := ask.verb == workItemClose
		if closed != it.Closed {
			set = &workitem.Fields{Closed: &closed}
		}
		if closed {
			done, same = "closed: "+name, "unchanged: "+name+" is already closed"
		} else {
			done, same = "reopened: "+name+" in "+workItemText(it.List), "unchanged: "+name+" is already open"
		}
	}
	if set == nil && comment == "" {
		res.Item = it
		return []string{same}, nil
	}
	if err := store.Change(it, set, comment, who); err != nil {
		return nil, workItemWriteError(err)
	}
	res.Changed = true
	// The item as the folder holds it now — this change and any that
	// arrived while the command ran — read alone, not with the board.
	if after, err := workitem.ReadItem(store.Folder, it.ID); err == nil && after != nil {
		it = after
	}
	res.Item = it
	return []string{done}, nil
}

// addWorkItem is `add`: a new work item in the list --list names, the
// board's first when it names none.
func (inv Invocation) addWorkItem(ask *workItemAsk, store workitem.Store, board *workitem.Board, who workitem.Author, res *workItemResult) ([]string, error) {
	list := board.Lists[0]
	if ask.list != "" {
		var err error
		if list, err = workItemListNamed(board, ask.list); err != nil {
			return nil, err
		}
	}
	set := workitem.Fields{Title: &ask.title, List: &list}
	if ask.description != "" {
		set.Description = &ask.description
	}
	if ask.to != "" {
		set.Assignee = &ask.to
	}
	if len(ask.labels) > 0 {
		set.Labels = &ask.labels
	}
	id, err := store.Add(board, set, who)
	if err != nil {
		return nil, workItemWriteError(err)
	}
	it, err := workitem.ReadItem(store.Folder, id)
	if err != nil || it == nil {
		return nil, &protocol.Error{Code: protocol.CodeInternal, Message: "the work item was written and could not be read back"}
	}
	res.Item, res.Changed = it, true
	line := "added: " + itoa(it.Number) + " " + strconv.Quote(workItemText(it.Title)) + " in " + workItemText(it.List)
	if it.Assignee != "" {
		line += ", assigned to " + labelLine(it.Assignee)
	}
	return []string{line}, nil
}

// listWorkItems is `list`: the open work items — or, with --closed, the
// closed ones — that pass the filters, as one table in the order of the
// board's lists, then by number; and one line that counts every list of
// the board, so an empty list is still seen. A work item in a list the
// board no longer names follows the board's, under its list's own name.
func listWorkItems(ask *workItemAsk, board *workitem.Board, res *workItemResult) ([]string, error) {
	only := ""
	if ask.list != "" {
		var err error
		if only, err = workItemListNamed(board, ask.list); err != nil {
			return nil, err
		}
	}
	rank := func(list string) int {
		if i := slices.IndexFunc(board.Lists, func(l string) bool { return strings.EqualFold(l, list) }); i >= 0 {
			return i
		}
		return len(board.Lists)
	}
	var shown []*workitem.Item
	counts := make([]int, len(board.Lists))
	elsewhere := map[string]int{}
	for _, it := range board.Items {
		if it.Closed != ask.closed {
			continue
		}
		if i := rank(it.List); i < len(counts) {
			counts[i]++
		} else {
			elsewhere[it.List]++
		}
		switch {
		case only != "" && !strings.EqualFold(it.List, only):
		case ask.nobody && it.Assignee != "":
		case ask.to != "" && !workitem.SameMember(ask.to, it.Assignee):
		case ask.label != "" && !slices.ContainsFunc(it.Labels, func(l string) bool { return strings.EqualFold(l, ask.label) }):
		default:
			shown = append(shown, it)
		}
	}
	slices.SortStableFunc(shown, func(x, y *workitem.Item) int {
		return cmp.Or(cmp.Compare(rank(x.List), rank(y.List)), strings.Compare(x.List, y.List), cmp.Compare(x.Number, y.Number))
	})
	res.Lists, res.Items = board.Lists, make([]workItemRow, 0, len(shown))
	rows := [][]string{{"LIST", "NUMBER", "TITLE", "ASSIGNED", "LABELS"}}
	for _, it := range shown {
		res.Items = append(res.Items, workItemRow{
			ID: it.ID, Number: it.Number, Title: it.Title, List: it.List, Assignee: it.Assignee, Labels: it.Labels,
			Closed: it.Closed, Comments: len(it.Comments), CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt,
		})
		assigned, labels := "-", "-"
		if it.Assignee != "" {
			assigned = labelLine(it.Assignee)
		}
		if len(it.Labels) > 0 {
			labels = workItemNames(it.Labels)
		}
		rows = append(rows, []string{workItemText(it.List), itoa(it.Number), protocol.TruncateRunes(workItemText(it.Title), workItemTitleCell), assigned, labels})
	}
	kind := "open"
	if ask.closed {
		kind = "closed"
	}
	var out []string
	if len(shown) == 0 {
		out = append(out, "no "+kind+" work items match")
		if only == "" && !ask.nobody && ask.to == "" && ask.label == "" {
			out[0] = "no " + kind + " work items"
		}
	} else {
		for _, row := range padTable(rows) {
			out = append(out, tableRow(row))
		}
	}
	tally := make([]string, 0, len(board.Lists))
	for i, l := range board.Lists {
		tally = append(tally, workItemText(l)+" ("+itoa(counts[i])+")")
	}
	line := kind + " by list: " + strings.Join(tally, ", ")
	if len(elsewhere) > 0 {
		var names []string
		for l := range elsewhere {
			names = append(names, l)
		}
		slices.Sort(names)
		var others []string
		for _, l := range names {
			others = append(others, workItemText(l)+" ("+itoa(elsewhere[l])+")")
		}
		line += "; in lists the board no longer names: " + strings.Join(others, ", ")
	}
	return append(out, line), nil
}

// workItemStamp is how a time reads in `show`.
const workItemStamp = "2006-01-02 15:04 UTC"

// showWorkItem is `show`: the work item whole — its fields, its
// description as written, its comments and its history.
func showWorkItem(it *workitem.Item) []string {
	out := []string{"work item " + itoa(it.Number) + ": " + workItemText(it.Title)}
	state := workItemText(it.List)
	if it.Closed {
		state += " (closed)"
	}
	out = append(out, "list: "+state)
	if it.Assignee != "" {
		out = append(out, "assigned: "+labelLine(it.Assignee))
	} else {
		out = append(out, "assigned: nobody")
	}
	if len(it.Labels) > 0 {
		out = append(out, "labels: "+workItemNames(it.Labels))
	}
	out = append(out, "created: "+it.CreatedAt.UTC().Format(workItemStamp)+workItemBy(it.CreatedBy, ""))
	out = append(out, "updated: "+it.UpdatedAt.UTC().Format(workItemStamp))
	if it.Description != "" {
		out = append(out, "", "description:")
		out = append(out, workItemBlock(it.Description)...)
	}
	if len(it.Comments) > 0 {
		out = append(out, "", "comments:")
		for _, c := range it.Comments {
			out = append(out, "  "+c.At.UTC().Format(workItemStamp)+workItemBy(c.By, c.Session)+":")
			out = append(out, workItemBlock(c.Text)...)
		}
	}
	out = append(out, "", "history:")
	for _, e := range it.History {
		out = append(out, "  "+e.At.UTC().Format(workItemStamp)+workItemBy(e.By, e.Session)+": "+workItemText(e.What))
	}
	return out
}

// workItemBy names a change's writer after its time: ` by <label>
// (session <name>)`, either part left out when it is empty.
func workItemBy(member, session string) string {
	member, session = labelLine(member), nameLine(session)
	switch {
	case member != "" && session != "":
		return " by " + member + " (session " + session + ")"
	case member != "":
		return " by " + member
	case session != "":
		return " by session " + session
	}
	return ""
}

// workItemBlock is a description's or a comment's lines for the human
// form, each indented four spaces: the indent is the layout's, so a line
// of a teammate's text can never stand where a field's label would.
func workItemBlock(text string) []string {
	lines := strings.Split(protocol.Sanitize(text), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("    "+strings.ReplaceAll(l, "\t", "    "), " ")
	}
	return lines
}

// workItemText is a title, a list's name or a history line on one
// sanitised line of the human form.
func workItemText(s string) string { return oneLine(protocol.Sanitize(s)) }

// workItemNames joins lists' or labels' names for the human form.
func workItemNames(names []string) string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, workItemText(n))
	}
	return strings.Join(out, ", ")
}

// workItemByNumber is the work item the number names: `not_found` when
// none does, `conflict` when two still do — a clash this call could not
// repair.
func workItemByNumber(board *workitem.Board, number int) (*workitem.Item, error) {
	found := board.ByNumber(number)
	switch len(found) {
	case 0:
		return nil, &protocol.Error{
			Code:    protocol.CodeNotFound,
			Message: "the board has no work item " + itoa(number) + ": brigade work-item list shows the open ones, and --closed the closed",
			Details: map[string]string{"reason": ReasonNoSuchWorkItem},
		}
	case 1:
		return found[0], nil
	}
	return nil, &protocol.Error{
		Code:    protocol.CodeConflict,
		Message: itoa(len(found)) + " work items carry the number " + itoa(number) + ", taken on two machines at the same moment, and giving one of them another number failed just now; run this again",
		Details: map[string]string{"reason": ReasonNumberTakenTwice},
	}
}

// workItemListNamed is the board's list the caller named, in the board's
// spelling, or the `invalid_input` refusal that names the lists there
// are: a list is made by `lists`, never by a typing slip in a move.
func workItemListNamed(board *workitem.Board, name string) (string, error) {
	if list, ok := board.List(name); ok {
		return list, nil
	}
	return "", &protocol.Error{
		Code:    protocol.CodeInvalidInput,
		Message: "the board has no list by that name; its lists are: " + workItemNames(board.Lists) + " (brigade work-item lists <name>… names them anew)",
		Details: map[string]string{"field": "list", "reason": ReasonNoSuchList},
	}
}

// workItemWriteError is the failure of a change that could not be
// written: `conflict` for a work item, or a board, at one of its caps,
// else `internal`. Fixed text; an error's own words may carry a path.
func workItemWriteError(err error) *protocol.Error {
	if err == workitem.ErrFull { //nolint:errorlint // a sentinel returned as it is
		return &protocol.Error{
			Code:    protocol.CodeConflict,
			Message: "this work item, or the board, is at its limit and takes no further change: a work item holds " + itoa(workitem.MaxChanges) + " changes and a board " + itoa(workitem.MaxItems) + " work items",
			Details: map[string]string{"reason": ReasonBoardFull},
		}
	}
	return &protocol.Error{Code: protocol.CodeInternal, Message: "the change could not be written to the board folder"}
}

// workItemReadError is the failure of a board folder that could not be
// read: never an empty board, which would hand out numbers the board
// already has. Fixed text; the error's own words go to the log.
func workItemReadError(inv Invocation, err error) *protocol.Error {
	inv.logger().Warn("work-item: the board folder could not be read", log.Err(err))
	return &protocol.Error{Code: protocol.CodeInternal, Message: "the board folder could not be read; nothing was changed"}
}

// workItemWordError is textError for a text that travels on the command
// line — a list's name, a label, a member — where "put the text on
// stdin" would send the caller the wrong way.
func workItemWordError(field, reason string, limit int) *protocol.Error {
	perr := textError(field, reason, limit)
	if reason == doing.ReasonEmpty {
		perr.Message = "the " + field + " is empty; name it on the command line"
	}
	return perr
}

// workItemAuthor is who this session's changes are by, read from this
// machine alone: the session's name from the map, and the member's label
// as the roster shows it — the label the membership was joined with, the
// team store's, else what the `label` option resolves to, as the watcher
// registers it (a pointed host has no account to read one from). Either
// may be empty; a change then says less about its writer.
func (inv Invocation) workItemAuthor(m *sessionmap.ByPID) workitem.Author {
	who := workitem.Author{Session: protocol.OneLineName(m.SessionName)}
	if b, err := teamstore.LoadBinding(m.ConfigDir, m.TeamKey); err == nil {
		who.Member = labelLine(b.HumanLabel)
	} else {
		inv.logger().Debug("work-item: team binding unreadable; the label is the option's", log.Err(err))
	}
	if who.Member != "" {
		return who
	}
	option := config.ParseLabelOption(m.LabelOption)
	if option == config.LabelAccount && config.Host(m.Harness).Pointed() {
		return who
	}
	who.Member = labelLine(config.LabelFor(option, func() string {
		email, err := account.Email(inv.Environ)
		if err != nil {
			inv.logger().Debug("work-item: claude account email unavailable", log.Err(err))
		}
		return email
	}))
	return who
}

// workItemAsk reads one call's words — the verb, its arguments, its
// flags and, for the verbs that take a text, stdin — and cleans every
// one of them, before anything is resolved: a refused text spawns and
// reads nothing.
func (inv Invocation) workItemAsk(opts WorkItemOptions) (*workItemAsk, error) {
	if len(inv.Args) == 0 {
		return nil, usage(WorkItemUsage)
	}
	ask := &workItemAsk{verb: inv.Args[0], closed: opts.Closed, mine: opts.Mine, nobody: opts.Nobody}
	args := inv.Args[1:]
	secret := adapterkit.Getenv(inv.Environ, envMessagingToken)
	uses := func(flags ...string) error {
		// In the flags' own order, so the refusal names the same flag
		// every time.
		given := []struct {
			name string
			on   bool
		}{
			{"list", opts.List != ""}, {"to", opts.To != ""}, {"mine", opts.Mine}, {"nobody", opts.Nobody},
			{"label", opts.Label != ""}, {"closed", opts.Closed},
		}
		for _, g := range given {
			if g.on && !slices.Contains(flags, g.name) {
				return usage("work-item " + ask.verb + " takes no --" + g.name + "; " + WorkItemUsage)
			}
		}
		return nil
	}
	numbered := func(extra bool) error {
		if len(args) == 0 || (!extra && len(args) != 1) {
			return usage("work-item " + ask.verb + " names one work item by its number; " + WorkItemUsage)
		}
		n, err := workItemNumber(args[0])
		if err != nil {
			return err
		}
		ask.number, args = n, args[1:]
		return nil
	}
	assignee := func() error {
		count := 0
		for _, on := range []bool{opts.To != "", opts.Mine, opts.Nobody} {
			if on {
				count++
			}
		}
		if count > 1 {
			return &protocol.Error{Code: protocol.CodeInvalidInput, Message: "--to, --mine and --nobody name an assignee three ways: give one", Details: map[string]string{"field": "to", "reason": ReasonOneAssignee}}
		}
		ask.assignee = count == 1
		if opts.To == "" {
			return nil
		}
		to, reason := workitem.CleanLine(opts.To, protocol.MaxHumanLabelChars, secret)
		if reason != "" {
			return workItemWordError("to", reason, protocol.MaxHumanLabelChars)
		}
		ask.to = protocol.SanitizeLabel(to)
		return nil
	}
	list := func() error {
		if opts.List == "" {
			return nil
		}
		name, reason := workitem.CleanLine(opts.List, workitem.MaxListChars, secret)
		if reason != "" {
			return workItemWordError("list", reason, workitem.MaxListChars)
		}
		ask.list = name
		return nil
	}

	switch ask.verb {
	case workItemList:
		if len(args) != 0 {
			return nil, usage("work-item list takes flags only; " + WorkItemUsage)
		}
		if err := cmp.Or(uses("list", "to", "mine", "nobody", "label", "closed"), assignee(), list()); err != nil {
			return nil, err
		}
		if opts.Label != "" {
			label, reason := workitem.CleanLine(opts.Label, workitem.MaxLabelChars, secret)
			if reason != "" {
				return nil, workItemWordError("label", reason, workitem.MaxLabelChars)
			}
			ask.label = label
		}
	case workItemShow, workItemClose, workItemReopen:
		if err := cmp.Or(uses(), numbered(false)); err != nil {
			return nil, err
		}
	case workItemAdd:
		if len(args) != 0 {
			return nil, usage("work-item add takes flags only: the title, a blank line and the description travel on stdin; " + WorkItemUsage)
		}
		if err := cmp.Or(uses("list", "to", "mine", "label"), assignee(), list()); err != nil {
			return nil, err
		}
		labels, err := workItemLabels(strings.Split(opts.Label, ","), secret)
		if err != nil {
			return nil, err
		}
		ask.labels = labels
		if ask.title, ask.description, err = inv.readWorkItemText(secret); err != nil {
			return nil, err
		}
	case workItemEdit:
		if err := cmp.Or(uses(), numbered(false)); err != nil {
			return nil, err
		}
		var err error
		if ask.title, ask.description, err = inv.readWorkItemText(secret); err != nil {
			return nil, err
		}
	case workItemMove:
		if err := cmp.Or(uses(), numbered(true)); err != nil {
			return nil, err
		}
		if len(args) == 0 {
			return nil, usage("work-item move names a work item by its number and then one of the board's lists; " + WorkItemUsage)
		}
		// The list's name is the rest of the words, so one of two words
		// needs no quoting.
		name, reason := workitem.CleanLine(strings.Join(args, " "), workitem.MaxListChars, secret)
		if reason != "" {
			return nil, workItemWordError("list", reason, workitem.MaxListChars)
		}
		ask.list = name
	case workItemAssign:
		if err := cmp.Or(uses("to", "mine", "nobody"), numbered(false), assignee()); err != nil {
			return nil, err
		}
		if !ask.assignee {
			return nil, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "name the assignee: --to <label>, --mine for this session's member, or --nobody", Details: map[string]string{"field": "to", "reason": ReasonAssigneeNeeded}}
		}
	case workItemLabel:
		if err := cmp.Or(uses(), numbered(true)); err != nil {
			return nil, err
		}
		labels, err := workItemLabels(args, secret)
		if err != nil {
			return nil, err
		}
		ask.labels = labels
	case workItemComment:
		if err := cmp.Or(uses(), numbered(false)); err != nil {
			return nil, err
		}
		raw, err := inv.readWorkItemStdin("comment", "comment", workitem.MaxCommentChars)
		if err != nil {
			return nil, err
		}
		comment, reason := workitem.CleanBlock(raw, workitem.MaxCommentChars, secret)
		if reason != "" {
			return nil, textError("comment", reason, workitem.MaxCommentChars)
		}
		ask.comment = comment
	case workItemLists:
		if err := uses(); err != nil {
			return nil, err
		}
		if len(args) > workitem.MaxLists {
			return nil, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "a board has at most " + itoa(workitem.MaxLists) + " lists", Details: map[string]string{"field": "lists", "reason": ReasonTooMany}}
		}
		for _, a := range args {
			name, reason := workitem.CleanLine(a, workitem.MaxListChars, secret)
			if reason != "" {
				return nil, workItemWordError("list", reason, workitem.MaxListChars)
			}
			if slices.ContainsFunc(ask.lists, func(l string) bool { return strings.EqualFold(l, name) }) {
				return nil, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "a list is named twice", Details: map[string]string{"field": "lists", "reason": ReasonDuplicate}}
			}
			ask.lists = append(ask.lists, name)
		}
	default:
		return nil, usage(WorkItemUsage)
	}
	return ask, nil
}

// workItemNumber reads a work item's number as a person writes it: `41`,
// or `#41`.
func workItemNumber(word string) (int, error) {
	n, err := strconv.Atoi(strings.TrimPrefix(word, "#"))
	if err != nil || n < 1 || n > workitem.MaxNumber {
		return 0, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "a work item is named by its number, as brigade work-item list shows it", Details: map[string]string{"field": "number", "reason": ReasonNotANumber}}
	}
	return n, nil
}

// workItemLabels cleans a work item's labels: each on one line within
// its cap, the empty ones dropped, none twice (compared without case), at
// most workitem.MaxLabels. Never nil.
func workItemLabels(words []string, secret string) ([]string, error) {
	labels := []string{}
	for _, w := range words {
		if strings.TrimSpace(w) == "" {
			continue
		}
		label, reason := workitem.CleanLine(w, workitem.MaxLabelChars, secret)
		if reason != "" {
			return nil, workItemWordError("label", reason, workitem.MaxLabelChars)
		}
		if slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, label) }) {
			continue
		}
		if len(labels) == workitem.MaxLabels {
			return nil, &protocol.Error{Code: protocol.CodeInvalidInput, Message: "a work item carries at most " + itoa(workitem.MaxLabels) + " labels", Details: map[string]string{"field": "label", "reason": ReasonTooMany}}
		}
		labels = append(labels, label)
	}
	return labels, nil
}

// readWorkItemText reads add's and edit's stdin: the title on the first
// line and, after it, the description, as a commit message is written.
// The title is required; the description may be absent.
func (inv Invocation) readWorkItemText(secret string) (title, description string, err error) {
	// A stdin past the read limit is past it because of its description:
	// a title at its cap is a fortieth of the limit.
	raw, err := inv.readWorkItemStdin("title", "description", workitem.MaxDescriptionChars)
	if err != nil {
		return "", "", err
	}
	first, rest, _ := strings.Cut(strings.TrimLeft(raw, "\r\n"), "\n")
	title, reason := workitem.CleanLine(first, workitem.MaxTitleChars, secret)
	if reason != "" {
		return "", "", textError("title", reason, workitem.MaxTitleChars)
	}
	if strings.TrimSpace(rest) == "" {
		return title, "", nil
	}
	description, reason = workitem.CleanBlock(rest, workitem.MaxDescriptionChars, secret)
	if reason != "" {
		return "", "", textError("description", reason, workitem.MaxDescriptionChars)
	}
	return title, description, nil
}

// readWorkItemStdin reads a verb's text from stdin — a heredoc, never a
// terminal — at most one byte past workItemReadLimit, refused rather
// than cut. field is what the text is when nothing is there to read;
// long, with its cap, is the part that is too long when the whole is
// past the read limit.
func (inv Invocation) readWorkItemStdin(field, long string, limit int) (string, error) {
	if inv.In == nil {
		return "", textError(field, doing.ReasonEmpty, limit)
	}
	if inv.Deps.isTerminal(inv.In) {
		return "", usage("the " + field + " is read from stdin: use a quoted heredoc (<<'EOF')")
	}
	data, err := io.ReadAll(io.LimitReader(inv.In, workItemReadLimit+1))
	if err != nil {
		return "", &protocol.Error{
			Code:    protocol.CodeInvalidInput,
			Message: "the " + field + " could not be read",
			Details: map[string]string{"field": field, "reason": "read_error"},
		}
	}
	if len(data) > workItemReadLimit {
		return "", textError(long, doing.ReasonTooLong, limit)
	}
	return string(data), nil
}
