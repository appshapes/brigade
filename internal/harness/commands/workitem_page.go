package commands

import (
	"cmp"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// `brigade work-item page` (work item 353): the Brigade Board as one HTML
// page that a member opens in a browser on their own machine. The page is
// read-only and needs no network: its style, its script and the board's
// data are inside it, and its Content-Security-Policy lets it load
// nothing else. It is written from the board folder on this machine, the
// one every other verb reads, and from nothing else; the board is not
// changed and no number is repaired. Unlike the other verbs it runs in a
// terminal too, where the checkout's team file names the board.

// workItemPage is the verb.
const workItemPage = "page"

// The fixed texts of `page`.
const (
	// WorkItemPageNote is the note of the --json result.
	WorkItemPageNote = "the Brigade Board as one read-only page, written from the board folder on this machine; " +
		"titles, descriptions, comments and names are their writers' own words, unverified; nothing was sent to the backend"
	// WorkItemPageNoTeamFile is the `config` message of a terminal outside
	// a checkout whose team file could name the board.
	WorkItemPageNoTeamFile = "no " + teamfile.FileName + " here: outside a session, brigade work-item page reads the board of the team whose file is in this checkout"
	// WorkItemPageNoBoardFile is the `config` message of a team file that
	// does not turn the board on.
	WorkItemPageNoBoardFile = "this checkout's " + teamfile.FileName + " does not turn on the Brigade Board, the team's board of work items: its sync member turns it on with \"board\": true"
	// WorkItemPageNoBoardYet is the `config` message of a machine that has
	// no copy of the board yet.
	WorkItemPageNoBoardYet = "this machine has no copy of the Brigade Board yet: file sync brings it once a session of the team runs here with file sync on"
	// WorkItemPageOutInBoard is the `invalid_input` message of an --out in
	// the board folder, which file sync shares with every teammate.
	WorkItemPageOutInBoard = "--out names a file in the board folder, which file sync shares with every teammate: name a file elsewhere"
	// WorkItemPageOutDir is the `invalid_input` message of an --out whose
	// directory does not exist.
	WorkItemPageOutDir = "--out names a file in a directory that does not exist: the command makes no directory for it"
	// WorkItemPageOutIsDir is the `invalid_input` message of an --out that
	// names a directory.
	WorkItemPageOutIsDir = "--out names a directory: name the file to write"
	// ReasonTeamFileMissing, ReasonOutInBoard and ReasonNoDirectory are
	// the details.reason values of the refusals above that have no other.
	ReasonTeamFileMissing = "team_file_missing"
	// ReasonBoardNotHere is the details.reason of WorkItemPageNoBoardYet:
	// the team has a board, and this machine has no copy of it yet.
	ReasonBoardNotHere = "board_not_here"
	ReasonOutInBoard   = "out_in_board"
	ReasonNoDirectory  = "no_directory"
)

// PagesDir is the directory under Brigade's state directory that holds
// the pages `page` writes when --out names none: outside every checkout
// and outside the board folder, so file sync never shares one.
const PagesDir = "pages"

// workItemPageResult is the --json result of `page`.
type workItemPageResult struct {
	SelfSessionID string `json:"self_session_id,omitzero"`
	Verb          string `json:"verb"`
	// Path is the file written and URL the same as a file: URL.
	Path string `json:"path"`
	URL  string `json:"url"`
	// Open and Closed count the work items on the page, and Lists the
	// board's lists.
	Open   int    `json:"open"`
	Closed int    `json:"closed"`
	Lists  int    `json:"lists"`
	Note   string `json:"note"`
}

// pageBoard is the board a page shows: the folder, the team's name, and
// the session's id, which a terminal has none of.
type pageBoard struct {
	folder, team, sessionID string
}

// workItemPageRun is `page`: read the board, write the page, say where.
func (inv Invocation) workItemPageRun(ask *workItemAsk) error {
	where, err := inv.pageBoardFolder()
	if err != nil {
		return err
	}
	board, err := workitem.Read(where.folder)
	if err != nil {
		return workItemReadError(inv, err)
	}
	if board.Skipped > 0 {
		inv.logger().Debug("work-item page: files in the board folder that are no changes were left out", "skipped", board.Skipped)
	}
	path := ask.out
	if path == "" {
		stateDir, err := config.BrigadeStateDir(inv.Environ)
		if err != nil {
			return err
		}
		dir := filepath.Join(stateDir, PagesDir)
		// Chmod too: MkdirAll leaves the mode of a directory that was there.
		//nolint:gosec // G302: a directory needs its search bit; 0700 is its owner alone
		if err := cmp.Or(os.MkdirAll(dir, 0o700), os.Chmod(dir, 0o700)); err != nil {
			inv.logger().Warn("work-item page: the pages directory could not be made", log.Err(err))
			return &protocol.Error{Code: protocol.CodeInternal, Message: "the directory for the page could not be made; nothing was written"}
		}
		path = filepath.Join(dir, "board-"+filepath.Base(where.folder)+".html")
	} else if err := pageOutAllowed(path, where.folder); err != nil {
		return err
	}
	data := boardPageFrom(board, where.team, inv.Deps.now())
	page, err := renderBoardPage(data)
	if err != nil {
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the page could not be made; nothing was written"}
	}
	if err := adapterkit.WriteAtomic(path, page); err != nil {
		inv.logger().Warn("work-item page: the page could not be written", log.Err(err))
		return &protocol.Error{Code: protocol.CodeInternal, Message: "the page could not be written"}
	}
	res := workItemPageResult{SelfSessionID: where.sessionID, Verb: workItemPage, Path: path, URL: fileURL(path), Lists: len(board.Lists), Note: WorkItemPageNote}
	for _, it := range data.Items {
		if it.Closed {
			res.Closed++
		} else {
			res.Open++
		}
	}
	if inv.JSON {
		return writeJSON(inv.Out, res)
	}
	return writeLines(inv.Out,
		"wrote: "+res.URL,
		"the Brigade Board as one read-only page: "+itoa(res.Open)+" open and "+itoa(res.Closed)+" closed work items in "+itoa(res.Lists)+" lists; "+
			"open it in a browser, and run brigade work-item page again for later changes")
}

// pageBoardFolder is the board this call's page shows. Inside a session
// it is the one the session's map names, as for every other verb.
// Outside one it is the board of the team whose file is in this checkout:
// its folder under Brigade's state directory, where the session-start
// hook puts it (foldersync.BoardPath), which must exist already — a
// terminal never makes a board.
func (inv Invocation) pageBoardFolder() (pageBoard, error) {
	if inv.inSession() {
		m, _, err := inv.sessionMap()
		if err != nil {
			return pageBoard{}, err
		}
		if m.BoardFolder == "" {
			return pageBoard{}, &protocol.Error{Code: protocol.CodeConfig, Message: WorkItemNoBoard, Details: map[string]string{"reason": ReasonNoBoard}}
		}
		return pageBoard{folder: m.BoardFolder, team: m.TeamName, sessionID: m.BrigadeSessionID}, nil
	}
	path, ok := teamfile.Discover(inv.mustGetwd())
	if !ok {
		return pageBoard{}, &protocol.Error{Code: protocol.CodeConfig, Message: WorkItemPageNoTeamFile, Details: map[string]string{"reason": ReasonTeamFileMissing}}
	}
	f, err := teamfile.Parse(path)
	if err != nil {
		return pageBoard{}, err
	}
	if f.Sync == nil || !f.Sync.Board {
		return pageBoard{}, &protocol.Error{Code: protocol.CodeConfig, Message: WorkItemPageNoBoardFile, Details: map[string]string{"reason": ReasonNoBoard}}
	}
	stateDir, err := config.BrigadeStateDir(inv.Environ)
	if err != nil {
		return pageBoard{}, err
	}
	folder := foldersync.BoardPath(stateDir, f.TeamRef)
	if fi, err := os.Lstat(folder); err != nil || !fi.IsDir() {
		return pageBoard{}, &protocol.Error{Code: protocol.CodeConfig, Message: WorkItemPageNoBoardYet, Details: map[string]string{"reason": ReasonBoardNotHere}}
	}
	return pageBoard{folder: folder, team: f.TeamName}, nil
}

// pageOutAllowed refuses an --out the command must not write: one in the
// board folder, by any spelling of its path or through a symlink, which
// file sync would hand every teammate as a stray file; one whose
// directory does not exist; and a directory. path is absolute.
func pageOutAllowed(path, board string) error {
	refuse := func(message, reason string) error {
		return &protocol.Error{Code: protocol.CodeInvalidInput, Message: message, Details: map[string]string{"field": "out", "reason": reason}}
	}
	dir, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return refuse(WorkItemPageOutDir, ReasonNoDirectory)
		}
		return refuse("--out names a file in a directory that cannot be read", ReasonNoDirectory)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return refuse(WorkItemPageOutDir, ReasonNoDirectory)
	}
	// The file the rename replaces is the path in its resolved directory,
	// a symlink there included; where a symlink points is checked too.
	target := filepath.Join(dir, filepath.Base(path))
	for _, t := range []string{target, resolvedOr(target)} {
		for _, b := range []string{board, resolvedOr(board)} {
			if within(b, t) {
				return refuse(WorkItemPageOutInBoard, ReasonOutInBoard)
			}
		}
	}
	if fi, err := os.Stat(target); err == nil && fi.IsDir() {
		return refuse(WorkItemPageOutIsDir, ReasonNoDirectory)
	}
	return nil
}

// resolvedOr is path with its symlinks resolved, or path itself when it
// cannot be.
func resolvedOr(path string) string {
	if r, err := filepath.EvalSymlinks(path); err == nil {
		return r
	}
	return path
}

// within says whether path is dir or under it.
func within(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// fileURL is path as a file: URL a terminal can open: forward slashes,
// a leading one before a Windows drive letter, and a UNC path's server as
// the URL's host.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if rest, ok := strings.CutPrefix(p, "//"); ok {
		host, share, _ := strings.Cut(rest, "/")
		return (&url.URL{Scheme: "file", Host: host, Path: "/" + share}).String()
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return (&url.URL{Scheme: "file", Path: p}).String()
}

// boardPageData is what the page shows: the board, folded and cleaned,
// and the choices of its filters. It is the page's one JSON value.
type boardPageData struct {
	Team      string `json:"team"`
	Generated string `json:"generated"`
	// Latest is the time of the newest change this machine's copy of the
	// board holds, "" for a board with no work item: a copy that file sync
	// has not brought up to date shows an old one.
	Latest string `json:"latest"`
	// Lists is the board's lists in order, then a list the board no
	// longer has but a work item still names, by its key (Gone).
	Lists []boardPageList `json:"lists"`
	// Items is every work item, open and closed, in the order of Lists,
	// then by number.
	Items []boardPageItem `json:"items"`
	// Assignees and Tags are the filters' choices: each one once, by its
	// fold key, sorted by it.
	Assignees []boardPageChoice `json:"assignees"`
	Tags      []boardPageChoice `json:"tags"`
}

// boardPageChoice is one choice of a filter: the name as first seen on
// the board, and the fold key every spelling of it shares.
type boardPageChoice struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

// boardPageList is one column of the page.
type boardPageList struct {
	Key    string `json:"key"`
	Name   string `json:"name"`
	Hidden bool   `json:"hidden"`
	Gone   bool   `json:"gone"`
}

// boardPageItem is one card of the page.
type boardPageItem struct {
	Number      int                `json:"number"`
	Title       string             `json:"title"`
	List        string             `json:"list"`
	Assignee    string             `json:"assignee"`
	AssigneeKey string             `json:"assignee_key"`
	Tags        []string           `json:"tags"`
	TagKeys     []string           `json:"tag_keys"`
	Closed      bool               `json:"closed"`
	Description string             `json:"description"`
	Comments    []boardPageComment `json:"comments"`
	Updated     string             `json:"updated"`
}

// boardPageComment is one comment on a card.
type boardPageComment struct {
	At      string `json:"at"`
	By      string `json:"by"`
	Session string `json:"session"`
	Text    string `json:"text"`
}

// pageStamp is how a time reads on the page.
const pageStamp = workItemStamp

// boardPageFrom folds the board into what the page shows, every text
// cleaned as the human form of `show` cleans it.
func boardPageFrom(board *workitem.Board, team string, now time.Time) boardPageData {
	data := boardPageData{
		Team:      oneLine(protocol.Sanitize(team)),
		Generated: now.UTC().Format(pageStamp),
		Lists:     make([]boardPageList, 0, len(board.Lists)),
		Items:     make([]boardPageItem, 0, len(board.Items)),
		Assignees: []boardPageChoice{},
		Tags:      []boardPageChoice{},
	}
	for _, l := range board.Lists {
		data.Lists = append(data.Lists, boardPageList{Key: l.Key, Name: workItemText(l.Name), Hidden: l.Hidden})
	}
	rank := func(key string) int {
		if i := slices.IndexFunc(board.Lists, func(l workitem.List) bool { return l.Key == key }); i >= 0 {
			return i
		}
		return len(board.Lists)
	}
	var gone []string
	for _, it := range board.Items {
		if rank(it.List) == len(board.Lists) && !slices.Contains(gone, it.List) {
			gone = append(gone, it.List)
		}
	}
	slices.Sort(gone)
	for _, key := range gone {
		data.Lists = append(data.Lists, boardPageList{Key: key, Name: workItemText(key), Gone: true})
	}
	items := slices.Clone(board.Items)
	slices.SortStableFunc(items, func(x, y *workitem.Item) int {
		return cmp.Or(cmp.Compare(rank(x.List), rank(y.List)), strings.Compare(x.List, y.List), cmp.Compare(x.Number, y.Number))
	})
	var latest time.Time
	for _, it := range items {
		if it.UpdatedAt.After(latest) {
			latest = it.UpdatedAt
		}
		card := boardPageItem{
			Number:      it.Number,
			Title:       workItemText(it.Title),
			List:        it.List,
			Assignee:    labelLine(it.Assignee),
			Tags:        make([]string, 0, len(it.Tags)),
			TagKeys:     make([]string, 0, len(it.Tags)),
			Closed:      it.Closed,
			Description: protocol.Sanitize(it.Description),
			Comments:    make([]boardPageComment, 0, len(it.Comments)),
			Updated:     it.UpdatedAt.UTC().Format(pageStamp),
		}
		card.AssigneeKey = foldKey(card.Assignee)
		for _, t := range it.Tags {
			card.Tags = append(card.Tags, workItemText(t))
			card.TagKeys = append(card.TagKeys, foldKey(workItemText(t)))
		}
		for _, c := range it.Comments {
			card.Comments = append(card.Comments, boardPageComment{
				At: c.At.UTC().Format(pageStamp), By: labelLine(c.By), Session: nameLine(c.Session), Text: protocol.Sanitize(c.Text),
			})
		}
		data.Items = append(data.Items, card)
		data.Assignees = addFolded(data.Assignees, card.Assignee)
		for _, t := range card.Tags {
			data.Tags = addFolded(data.Tags, t)
		}
	}
	if !latest.IsZero() {
		data.Latest = latest.UTC().Format(pageStamp)
	}
	byKey := func(x, y boardPageChoice) int {
		return cmp.Or(cmp.Compare(strings.ToLower(x.Name), strings.ToLower(y.Name)), strings.Compare(x.Key, y.Key))
	}
	slices.SortFunc(data.Assignees, byKey)
	slices.SortFunc(data.Tags, byKey)
	return data
}

// addFolded adds s to the choices unless it is empty or a choice has its
// fold key already.
func addFolded(choices []boardPageChoice, s string) []boardPageChoice {
	key := foldKey(s)
	if s == "" || slices.ContainsFunc(choices, func(c boardPageChoice) bool { return c.Key == key }) {
		return choices
	}
	return append(choices, boardPageChoice{Name: s, Key: key})
}

// foldKey is s with each character replaced by one character of its set
// of simple case folds, the same for the whole set: the lower case of the
// least, when that is in the set, else the least. So two strings have one
// key exactly when strings.EqualFold, the listing's own comparison, says
// they are equal (Σ, σ and ς share σ; S, s and ſ share s; İ, whose lower
// case i is in another set, keeps İ).
func foldKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		least, set := r, []rune{r}
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			least, set = min(least, f), append(set, f)
		}
		if lower := unicode.ToLower(least); slices.Contains(set, lower) {
			least = lower
		}
		b.WriteRune(least)
	}
	return b.String()
}

// pageJSON is the page's data as JSON that is safe inside a script
// element: no `<`, `>` or `&` (so no `</script>` and no comment opener)
// and no line or paragraph separator, each written as its \u escape.
// Those characters stand only inside JSON strings, where the escape is
// the same character.
func pageJSON(data boardPageData) ([]byte, error) {
	raw, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	return []byte(pageJSONEscaper.Replace(string(raw))), nil
}

var pageJSONEscaper = strings.NewReplacer(
	"<", `\u003c`, ">", `\u003e`, "&", `\u0026`, "\u2028", `\u2028`, "\u2029", `\u2029`,
)

// cspHash is the Content-Security-Policy source of an inline element's
// exact text.
func cspHash(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// pagePolicy lets the page run its own style and script and nothing
// else: no fetch, no image, no font, no frame, no form, no other script.
var pagePolicy = "default-src 'none'; style-src " + cspHash(pageStyle) + "; script-src " + cspHash(pageScript) +
	"; base-uri 'none'; form-action 'none'"

// renderBoardPage is the whole page. The markup is fixed: the board's
// words travel only in the JSON element, and the script puts each one in
// the page as text, never as markup.
func renderBoardPage(data boardPageData) ([]byte, error) {
	body, err := pageJSON(data)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(pageHead)
	b.WriteString(pagePolicy)
	b.WriteString(pageAfterPolicy)
	b.WriteString(pageStyle)
	b.WriteString(pageBody)
	b.Write(body)
	b.WriteString("</script>\n<script>")
	b.WriteString(pageScript)
	b.WriteString("</script>\n</body>\n</html>\n")
	return []byte(b.String()), nil
}

const pageHead = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="referrer" content="no-referrer">
<meta http-equiv="Content-Security-Policy" content="`

const pageAfterPolicy = `">
<title>Brigade Board</title>
<style>`

const pageBody = `</style>
</head>
<body>
<header>
<h1 id="title">Brigade Board</h1>
<div id="filters" class="filters">
<label>Assigned <select id="f-assignee"><option value="">anyone</option><option value="-">nobody</option></select></label>
<label>Tag <select id="f-tag"><option value="">any</option></select></label>
<label>List <select id="f-list"><option value="">every shown list</option></select></label>
<label class="check"><input type="checkbox" id="f-hidden"> hidden lists</label>
<label class="check"><input type="checkbox" id="f-closed"> closed work items</label>
</div>
<p id="summary" class="summary"></p>
</header>
<main id="board" class="board"></main>
<footer><p id="generated"></p><p>This page is read-only. Change a work item with <code>brigade work-item</code>, then run <code>brigade work-item page</code> again and reload.</p></footer>
<script id="board-data" type="application/json">`

const pageStyle = `
:root { color-scheme: light dark; --bg: #f6f7f9; --fg: #1d2330; --muted: #5d6676; --card: #ffffff; --line: #d9dde4; --chip: #e8ecf3; --accent: #2f5bd3; }
@media (prefers-color-scheme: dark) { :root { --bg: #14171c; --fg: #e4e8ef; --muted: #9aa3b2; --card: #1d2128; --line: #303642; --chip: #2a303b; --accent: #8fb0ff; } }
* { box-sizing: border-box; }
body { margin: 0; background: var(--bg); color: var(--fg); font: 14px/1.45 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; }
header, footer { padding: 12px 16px; }
h1 { margin: 0 0 8px; font-size: 20px; }
.filters { display: flex; flex-wrap: wrap; gap: 8px 16px; align-items: center; }
.filters label { color: var(--muted); }
.filters select { margin-left: 4px; max-width: 16em; }
.summary, footer p { margin: 8px 0 0; color: var(--muted); }
.board { display: flex; gap: 12px; padding: 0 16px 16px; overflow-x: auto; align-items: flex-start; }
.column { flex: 0 0 300px; background: transparent; }
.column h2 { margin: 0 0 8px; font-size: 14px; text-transform: none; }
.column h2 .count, .column h2 .flag { color: var(--muted); font-weight: normal; }
.column.hidden h2, .column.gone h2 { font-style: italic; }
.cards { display: flex; flex-direction: column; gap: 8px; }
.card { background: var(--card); border: 1px solid var(--line); border-radius: 6px; padding: 8px 10px; }
.card.closed { opacity: 0.6; }
.card summary { cursor: pointer; list-style: none; }
.card summary::-webkit-details-marker { display: none; }
.num { color: var(--muted); margin-right: 4px; }
.title { font-weight: 600; overflow-wrap: anywhere; }
.meta { margin-top: 4px; display: flex; flex-wrap: wrap; gap: 4px; color: var(--muted); font-size: 12px; }
.chip { background: var(--chip); border-radius: 10px; padding: 0 8px; }
.who { margin-right: 4px; }
.closed-flag { color: var(--accent); }
.detail { margin-top: 8px; border-top: 1px solid var(--line); padding-top: 8px; }
.text { white-space: pre-wrap; overflow-wrap: anywhere; margin: 0 0 8px; }
.comment { margin-top: 8px; }
.comment .by { color: var(--muted); font-size: 12px; }
.empty { color: var(--muted); font-style: italic; }
code { font-size: 13px; }
`

const pageScript = `
(function (root) {
  "use strict";
  // The filters, as pure functions of the data: the page uses them, and so
  // does the test that runs them under Node (workitem_page_test.go).
  // Assignees and tags compare by the fold key the command gives each one,
  // the same equality as the command's own listing.
  function match(it, f) {
    if (it.closed && !f.closed) { return false; }
    if (f.assignee === "-" && it.assignee !== "") { return false; }
    if (f.assignee.charAt(0) === "=" && it.assignee_key !== f.assignee.slice(1)) { return false; }
    if (f.tag !== "" && it.tag_keys.indexOf(f.tag.slice(1)) < 0) { return false; }
    return true;
  }
  // shown: whether a list is a column. A list named in the filter is
  // shown, a hidden one too; a list the board no longer has, only while a
  // work item in it passes the filters.
  function shown(l, f, count) {
    if (f.list !== "") { return l.key === f.list; }
    if (l.gone) { return count > 0; }
    return !l.hidden || f.hidden;
  }
  // view: the columns the filters show, each with its work items, and the
  // hidden lists they leave out that hold a work item that passes them.
  function view(data, f) {
    var columns = [], left = [];
    data.lists.forEach(function (l) {
      var items = data.items.filter(function (it) { return it.list === l.key && match(it, f); });
      if (!shown(l, f, items.length)) {
        if (f.list === "" && l.hidden && items.length > 0) { left.push(l.name + " (" + items.length + ")"); }
        return;
      }
      columns.push({ list: l, items: items });
    });
    return { columns: columns, left: left };
  }
  function none() { return { assignee: "", tag: "", list: "", hidden: false, closed: false }; }
  // parseHash: the filters a page address names. A value the page has no
  // choice for, such as a tag that left the board, is dropped.
  function parseHash(hash, data) {
    var f = none();
    String(hash).replace(/^#/, "").split("&").forEach(function (pair) {
      var at = pair.indexOf("="), k, v;
      if (at < 0) { return; }
      k = pair.slice(0, at);
      try { v = decodeURIComponent(pair.slice(at + 1)); } catch (e) { return; }
      if (k === "assignee" && (v === "-" || data.assignees.some(function (a) { return "=" + a.key === v; }))) { f.assignee = v; }
      if (k === "tag" && data.tags.some(function (t) { return "=" + t.key === v; })) { f.tag = v; }
      if (k === "list" && data.lists.some(function (l) { return l.key === v; })) { f.list = v; }
      if (k === "hidden") { f.hidden = v === "1"; }
      if (k === "closed") { f.closed = v === "1"; }
    });
    return f;
  }
  // toHash: the address that names the filters, "" for none.
  function toHash(f) {
    var h = [];
    if (f.assignee) { h.push("assignee=" + encodeURIComponent(f.assignee)); }
    if (f.tag) { h.push("tag=" + encodeURIComponent(f.tag)); }
    if (f.list) { h.push("list=" + encodeURIComponent(f.list)); }
    if (f.hidden) { h.push("hidden=1"); }
    if (f.closed) { h.push("closed=1"); }
    return h.length ? "#" + h.join("&") : "";
  }
  root.brigadeBoardPage = { match: match, shown: shown, view: view, parseHash: parseHash, toHash: toHash };
  if (typeof document === "undefined") { return; }

  var data = JSON.parse(document.getElementById("board-data").textContent);
  function byId(id) { return document.getElementById(id); }
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) { e.className = cls; }
    if (text !== undefined) { e.textContent = text; }
    return e;
  }
  function addOption(select, value, label) {
    var o = el("option", null, label);
    o.value = value;
    select.appendChild(o);
  }
  var fAssignee = byId("f-assignee"), fTag = byId("f-tag"), fList = byId("f-list"), fHidden = byId("f-hidden"), fClosed = byId("f-closed");
  data.assignees.forEach(function (a) { addOption(fAssignee, "=" + a.key, a.name); });
  data.tags.forEach(function (t) { addOption(fTag, "=" + t.key, t.name); });
  data.lists.forEach(function (l) { addOption(fList, l.key, l.name + (l.hidden ? " (hidden)" : "") + (l.gone ? " (no longer a list)" : "")); });
  if (data.team) {
    byId("title").textContent = "Brigade Board: " + data.team;
    document.title = "Brigade Board: " + data.team;
  }
  byId("generated").textContent = "Written " + data.generated + " from the board folder on this machine." +
    (data.latest ? " Its newest change is from " + data.latest + "." : "");

  function filters() {
    return { assignee: fAssignee.value, tag: fTag.value, list: fList.value, hidden: fHidden.checked, closed: fClosed.checked };
  }
  function card(it) {
    var c = el("article", "card" + (it.closed ? " closed" : ""));
    var head = el("div");
    var line = el("div");
    line.appendChild(el("span", "num", "#" + it.number));
    line.appendChild(el("span", "title", it.title));
    head.appendChild(line);
    var meta = el("div", "meta");
    meta.appendChild(el("span", "who", it.assignee !== "" ? it.assignee : "nobody"));
    it.tags.forEach(function (t) { meta.appendChild(el("span", "chip", t)); });
    if (it.closed) { meta.appendChild(el("span", "closed-flag", "closed")); }
    head.appendChild(meta);
    if (it.description === "" && it.comments.length === 0) {
      c.appendChild(head);
      return c;
    }
    var d = el("details");
    var s = el("summary");
    s.appendChild(head);
    d.appendChild(s);
    var body = el("div", "detail");
    if (it.description !== "") { body.appendChild(el("p", "text", it.description)); }
    it.comments.forEach(function (cm) {
      var w = el("div", "comment");
      w.appendChild(el("div", "by", cm.at + (cm.by ? " by " + cm.by : "") + (cm.session ? " (session " + cm.session + ")" : "")));
      w.appendChild(el("p", "text", cm.text));
      body.appendChild(w);
    });
    body.appendChild(el("div", "by", "updated " + it.updated));
    d.appendChild(body);
    c.appendChild(d);
    return c;
  }
  function render() {
    var f = filters();
    var v = view(data, f);
    var board = byId("board");
    board.textContent = "";
    var total = 0;
    v.columns.forEach(function (column) {
      var l = column.list, items = column.items;
      total += items.length;
      var col = el("section", "column" + (l.hidden ? " hidden" : "") + (l.gone ? " gone" : ""));
      var h = el("h2");
      h.appendChild(document.createTextNode(l.name + " "));
      h.appendChild(el("span", "count", "(" + items.length + ")"));
      if (l.hidden) { h.appendChild(el("span", "flag", " hidden")); }
      if (l.gone) { h.appendChild(el("span", "flag", " no longer a list")); }
      col.appendChild(h);
      var cards = el("div", "cards");
      if (items.length === 0) { cards.appendChild(el("p", "empty", "no work items")); }
      items.forEach(function (it) { cards.appendChild(card(it)); });
      col.appendChild(cards);
      board.appendChild(col);
    });
    var text = total + (total === 1 ? " work item shown" : " work items shown");
    if (v.left.length > 0) { text += "; in hidden lists not shown: " + v.left.join(", "); }
    byId("summary").textContent = text;
    var hash = toHash(f);
    try { history.replaceState(null, "", hash || location.pathname); } catch (e) { /* a browser that refuses it for a file: page keeps the filters until a reload */ }
  }
  function restore() {
    var f = parseHash(location.hash, data);
    fAssignee.value = f.assignee; fTag.value = f.tag; fList.value = f.list; fHidden.checked = f.hidden; fClosed.checked = f.closed;
  }
  byId("filters").addEventListener("change", render);
  window.addEventListener("hashchange", function () { restore(); render(); });
  restore();
  render();
})(this);
`
