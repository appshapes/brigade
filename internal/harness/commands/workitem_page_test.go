package commands

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/appshapes/brigade/internal/harness/foldersync"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/workitem"
	"github.com/appshapes/brigade/internal/protocol"
)

// The tests of `brigade work-item page` (work item 353). The filters
// themselves run in the browser; these tests pin what the page carries:
// the board's data, the filters' choices, the fixed markup with its
// policy, and the board folder it is read from, in a session and in a
// terminal.

// pageParts splits a written page into its JSON data, its style and its
// script, and fails the test when the page is not the fixed markup.
func pageParts(t *testing.T, page string) (data boardPageData, style, script string) {
	t.Helper()
	cut := func(s, from, to string) (string, string) {
		_, rest, ok := strings.Cut(s, from)
		if !ok {
			t.Fatalf("the page has no %q", from)
		}
		in, after, ok := strings.Cut(rest, to)
		if !ok {
			t.Fatalf("the page has no %q after %q", to, from)
		}
		return in, after
	}
	style, _ = cut(page, "<style>", "</style>")
	raw, rest := cut(page, `<script id="board-data" type="application/json">`, "</script>")
	script, _ = cut(rest, "<script>", "</script>")
	if err := json.Unmarshal([]byte(raw), &data); err != nil {
		t.Fatalf("the page's data is no JSON: %v", err)
	}
	return data, style, script
}

// boardFiles lists every file under the board folder, for a check that a
// page wrote nothing there.
func boardFiles(t *testing.T, folder string) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(folder, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, p)
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	return files
}

// TestWorkItemPageCarriesTheBoard writes the page of a board with a
// hidden list, a list the board no longer has, a closed work item, a
// comment, and assignees and tags that differ only in case, and reads
// the page's data back: the lists in the board's order then the one it
// no longer has, the cards in that order then by number, the closed one
// too, and each filter's choices once, sorted without case. The board
// folder is read and not written, and no adapter is spawned.
func TestWorkItemPageCarriesTheBoard(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)

	f.workItem(t, WorkItemOptions{Mine: true, Tag: "bug,web"}, "Fix the login redirect\n\nSteps:\n1. Sign out\n", "add")
	f.workItem(t, WorkItemOptions{List: "doing", To: "Bob@example.com", Tag: "API"}, "Add a board", "add")
	f.workItem(t, WorkItemOptions{List: "doing", To: "bob@example.com", Tag: "api"}, "Old work", "add")
	f.workItem(t, WorkItemOptions{}, "Done on staging.", "comment", "3")
	f.workItem(t, WorkItemOptions{}, "", "close", "3")
	f.workItem(t, WorkItemOptions{}, "Parked idea", "add")
	f.workItem(t, WorkItemOptions{}, "", "lists", "Todo", "Done", "Review")
	f.workItem(t, WorkItemOptions{}, "", "move", "4", "review")
	f.workItem(t, WorkItemOptions{}, "", "hide-list", "review")

	before := boardFiles(t, folder)
	out := filepath.Join(f.dirs.Root, "board.html")
	got := f.workItem(t, WorkItemOptions{Out: out}, "", "page")
	want := "wrote: " + fileURL(out) + "\n" +
		"the Brigade Board as one read-only page: 3 open and 1 closed work items in 3 lists; open it in a browser, and run brigade work-item page again for later changes\n"
	if got != want {
		t.Fatalf("page =\n%s\nwant\n%s", got, want)
	}
	if after := boardFiles(t, folder); !slices.Equal(before, after) {
		t.Fatalf("the page changed the board folder:\nbefore %v\nafter  %v", before, after)
	}
	if n := f.rec.count(); n != 0 {
		t.Fatalf("%d adapter spawns, want none", n)
	}
	page, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(out); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("the page's mode: %v %v, want 0600", fi.Mode(), err)
		}
	}
	data, _, _ := pageParts(t, string(page))

	if data.Team != fixtureTeamName || data.Generated != fixtureNow.UTC().Format(pageStamp) || data.Latest != fixtureNow.UTC().Format(pageStamp) {
		t.Fatalf("team %q generated %q latest %q", data.Team, data.Generated, data.Latest)
	}
	wantLists := []boardPageList{
		{Key: "todo", Name: "Todo"}, {Key: "done", Name: "Done"}, {Key: "review", Name: "Review", Hidden: true}, {Key: "doing", Name: "doing", Gone: true},
	}
	if !slices.Equal(data.Lists, wantLists) {
		t.Fatalf("lists = %+v\nwant %+v", data.Lists, wantLists)
	}
	var order []int
	for _, it := range data.Items {
		order = append(order, it.Number)
	}
	if !slices.Equal(order, []int{1, 4, 2, 3}) {
		t.Fatalf("the cards' order = %v, want 1 (Todo), 4 (Review), 2 and 3 (the list the board no longer has)", order)
	}
	first, closed := data.Items[0], data.Items[3]
	if first.Title != "Fix the login redirect" || first.Assignee != "alice@example.com" || !slices.Equal(first.Tags, []string{"bug", "web"}) ||
		first.Description != "Steps:\n1. Sign out" || first.Closed || first.List != "todo" {
		t.Fatalf("card 1 = %+v", first)
	}
	if !closed.Closed || len(closed.Comments) != 1 || closed.Comments[0].Text != "Done on staging." ||
		closed.Comments[0].By != "alice@example.com" || closed.Comments[0].Session != selfSessionName {
		t.Fatalf("card 3 = %+v", closed)
	}
	if !slices.Equal(data.Assignees, []boardPageChoice{{"alice@example.com", foldKey("alice@example.com")}, {"Bob@example.com", foldKey("bob@example.com")}}) {
		t.Fatalf("the assignee choices = %q", data.Assignees)
	}
	if !slices.Equal(data.Tags, []boardPageChoice{{"API", foldKey("api")}, {"bug", foldKey("bug")}, {"web", foldKey("web")}}) {
		t.Fatalf("the tag choices = %q", data.Tags)
	}
	if card := data.Items[2]; card.AssigneeKey != foldKey("bob@example.com") || !slices.Equal(card.TagKeys, []string{foldKey("api")}) {
		t.Fatalf("card 2's keys = %q %q", card.AssigneeKey, card.TagKeys)
	}
}

// TestWorkItemPageDefaultPathAndJSON: with no --out the page goes to
// Brigade's state directory, under pages/, named after the board folder;
// --json says where, as a path and as a file: URL, with the counts.
func TestWorkItemPageDefaultPathAndJSON(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "One", "add")

	inv := f.inv(f.sessionEnv(), "", "page")
	inv.JSON = true
	f.out.Reset()
	if err := WorkItem(inv, WorkItemOptions{}); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Result workItemPageResult `json:"result"`
	}
	if err := json.Unmarshal(f.out.Bytes(), &env); err != nil {
		t.Fatalf("--json: %v: %s", err, f.out.String())
	}
	want := filepath.Join(f.stateDir, PagesDir, "board-"+filepath.Base(folder)+".html")
	res := env.Result
	if res.Path != want || res.URL != fileURL(want) || res.Open != 1 || res.Closed != 0 || res.Lists != 3 ||
		res.Verb != "page" || res.SelfSessionID != selfSessionID || res.Note != WorkItemPageNote {
		t.Fatalf("result = %+v\nwant the path %s", res, want)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("the page was not written: %v", err)
	}
	if runtime.GOOS != "windows" {
		// A pages directory made open before is made private again.
		//nolint:gosec // G302: the test opens the directory on purpose
		if err := os.Chmod(filepath.Dir(want), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := WorkItem(f.inv(f.sessionEnv(), "", "page"), WorkItemOptions{}); err != nil {
			t.Fatal(err)
		}
		if fi, err := os.Stat(filepath.Dir(want)); err != nil || fi.Mode().Perm() != 0o700 {
			t.Fatalf("the pages directory's mode: %v %v, want 0700", fi.Mode(), err)
		}
	}
}

// TestWorkItemPageFileURL: the URL a terminal opens, for a POSIX path, a
// Windows drive path and a Windows UNC path, as filepath.ToSlash gives
// them.
func TestWorkItemPageFileURL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		"/home/a/b c.html":      "file:///home/a/b%20c.html",
		"C:/Users/a/b.html":     "file:///C:/Users/a/b.html",
		"//server/share/b.html": "file://server/share/b.html",
	} {
		if got := fileURL(filepath.FromSlash(in)); got != want {
			t.Errorf("fileURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWorkItemPageMarkupIsFixed: a board's words never become markup. A
// title that tries to end the data element and a description with the
// other characters that matter in a script element travel as \u escapes
// inside the JSON and come back whole; the page carries only its own two
// script elements, and its policy names the exact hashes of its style
// and its script and lets nothing load.
func TestWorkItemPageMarkupIsFixed(t *testing.T) {
	t.Parallel()
	hostile := `</script><script>alert(1)</script><!-- <img src=x onerror=alert(2)>`
	data := boardPageData{
		Generated: "2026-11-16 12:00 UTC",
		Lists:     []boardPageList{{Key: "todo", Name: "Todo"}},
		Items: []boardPageItem{{
			Number: 1, Title: hostile, List: "todo", Tags: []string{"<b>"}, Description: "a & b\u2028c\u2029d >", Comments: []boardPageComment{},
		}},
		Assignees: []boardPageChoice{},
		Tags:      []boardPageChoice{{Name: "<b>", Key: "<b>"}},
	}
	raw, err := renderBoardPage(data)
	if err != nil {
		t.Fatal(err)
	}
	page := string(raw)
	if n := strings.Count(page, "<script"); n != 2 {
		t.Fatalf("%d script elements, want the page's own two", n)
	}
	for _, bad := range []string{"<img", "<!--", "alert(1)</script>", "\u2028", "\u2029", "<b>"} {
		if strings.Contains(page, bad) {
			t.Fatalf("the page carries %q outside the escaped data", bad)
		}
	}
	back, style, script := pageParts(t, page)
	if back.Items[0].Title != hostile || back.Items[0].Description != data.Items[0].Description || back.Tags[0].Name != "<b>" {
		t.Fatalf("the data did not come back whole: %+v", back.Items[0])
	}
	if style != pageStyle || script != pageScript {
		t.Fatalf("the page's style or script is not the fixed text")
	}
	policy := "default-src 'none'; style-src " + cspHash(pageStyle) + "; script-src " + cspHash(pageScript) + "; base-uri 'none'; form-action 'none'"
	if !strings.Contains(page, `http-equiv="Content-Security-Policy" content="`+policy+`"`) {
		t.Fatalf("the page's policy is not %q", policy)
	}
	if strings.Contains(pageScript, "innerHTML") || strings.Contains(pageScript, "insertAdjacentHTML") || strings.Contains(pageScript, "document.write") {
		t.Fatalf("the script puts text in the page as markup")
	}
}

// terminalBoard lays out a checkout whose team file carries the given
// sync member and returns its toplevel and the board folder a terminal
// resolves for it.
func terminalBoard(t *testing.T, f *fixture, sync string) (top, folder string) {
	t.Helper()
	top = filepath.Join(f.dirs.Root, "page-checkout")
	//nolint:gosec // G301: an ordinary checkout tree
	if err := os.MkdirAll(filepath.Join(top, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	doc := `{"version":1,"adapter":"supabase","url":"https://abc.supabase.co","publishable_key":"sb_publishable_x","team_ref":"` + pageTeamRef + `","team_name":"devs"` + sync + `}`
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(filepath.Join(top, teamfile.FileName), []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return top, foldersync.BoardPath(f.dirs.BrigadeState, pageTeamRef)
}

// pageTeamRef is the team of the terminal's checkout.
const pageTeamRef = "t_page"

// reasonOf is a refusal's details.reason, "" for any other error.
func reasonOf(err error) string {
	var perr *protocol.Error
	if errors.As(err, &perr) {
		return perr.Details["reason"]
	}
	return ""
}

// pageInTerminal runs `work-item page` from a human's shell in dir.
func (f *fixture) pageInTerminal(dir string) error {
	inv := f.inv(f.terminalEnv(), "", "page")
	inv.Deps.Getwd = func() (string, error) { return dir, nil }
	f.out.Reset()
	return WorkItem(inv, WorkItemOptions{})
}

// TestWorkItemPageInATerminal: outside a session the page is the board
// of the team whose file is in the checkout, at the folder the
// session-start hook gives it; each way that comes to nothing is refused
// with its own reason, and a terminal never makes a board folder.
func TestWorkItemPageInATerminal(t *testing.T) {
	t.Parallel()
	f := newFixture(t)

	if err := f.pageInTerminal(f.dirs.Root); reasonOf(err) != ReasonTeamFileMissing {
		t.Fatalf("outside a checkout: %v, want %s", err, ReasonTeamFileMissing)
	}
	top, _ := terminalBoard(t, f, "")
	if err := f.pageInTerminal(top); reasonOf(err) != ReasonNoBoard || !strings.Contains(err.Error(), "does not turn on") {
		t.Fatalf("a team file with no board: %v", err)
	}
	top, folder := terminalBoard(t, f, `,"sync":{"board":true}`)
	if err := f.pageInTerminal(top); reasonOf(err) != ReasonBoardNotHere || !strings.Contains(err.Error(), "no copy") {
		t.Fatalf("a board this machine has no copy of: %v", err)
	}
	if _, err := os.Stat(folder); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the refused call made the board folder: %v", err)
	}

	store := workitem.Store{Folder: folder, Now: func() time.Time { return fixtureNow }}
	board, err := workitem.Read(folder)
	if err != nil {
		t.Fatal(err)
	}
	title := "From a terminal"
	if _, err := store.Add(board, workitem.Fields{Title: &title, List: new("todo")}, workitem.Author{Member: "carol@example.com"}); err != nil {
		t.Fatal(err)
	}
	if err := f.pageInTerminal(top); err != nil {
		t.Fatalf("the page from a terminal: %v", err)
	}
	want := filepath.Join(f.dirs.BrigadeState, PagesDir, "board-"+filepath.Base(folder)+".html")
	if !strings.HasPrefix(f.out.String(), "wrote: "+fileURL(want)+"\n") {
		t.Fatalf("page = %q, want it written to %s", f.out.String(), want)
	}
	page, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	data, _, _ := pageParts(t, string(page))
	if data.Team != "devs" || len(data.Items) != 1 || data.Items[0].Title != title {
		t.Fatalf("the terminal's page = %+v", data)
	}
}

// TestWorkItemPageRefusals: page takes no words and no flag but --out,
// and --out may name neither a file in the board folder, which file sync
// would share, nor one in a directory that does not exist. A relative
// --out is the caller's directory's.
func TestWorkItemPageRefusals(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	folder := f.withBoard(t)
	f.workItem(t, WorkItemOptions{}, "One", "add")
	run := func(opts WorkItemOptions, args ...string) error {
		f.out.Reset()
		inv := f.inv(f.sessionEnv(), "", append([]string{"page"}, args...)...)
		inv.Deps.Getwd = func() (string, error) { return f.dirs.Root, nil }
		return WorkItem(inv, opts)
	}
	for _, c := range []struct {
		name   string
		opts   WorkItemOptions
		args   []string
		code   protocol.Code
		reason string
	}{
		{name: "a word", args: []string{"now"}, code: protocol.CodeUsage},
		{name: "--all", opts: WorkItemOptions{All: true}, code: protocol.CodeUsage},
		{name: "--out in the board folder", opts: WorkItemOptions{Out: filepath.Join(folder, "board.html")}, code: protocol.CodeInvalidInput, reason: ReasonOutInBoard},
		{name: "--out the board folder itself", opts: WorkItemOptions{Out: folder}, code: protocol.CodeInvalidInput, reason: ReasonOutInBoard},
		{name: "--out in no directory", opts: WorkItemOptions{Out: filepath.Join(f.dirs.Root, "nowhere", "board.html")}, code: protocol.CodeInvalidInput, reason: ReasonNoDirectory},
		{name: "--out a directory", opts: WorkItemOptions{Out: f.dirs.Root}, code: protocol.CodeInvalidInput, reason: ReasonNoDirectory},
	} {
		err := run(c.opts, c.args...)
		var perr *protocol.Error
		if !errors.As(err, &perr) || perr.Code != c.code || (c.reason != "" && perr.Details["reason"] != c.reason) {
			t.Errorf("%s: %v, want %s %s", c.name, err, c.code, c.reason)
		}
	}
	if runtime.GOOS != "windows" {
		// A symlink to the board folder is the board folder.
		link := filepath.Join(f.dirs.Root, "board-link")
		if err := os.Symlink(folder, link); err != nil {
			t.Fatal(err)
		}
		if err := run(WorkItemOptions{Out: filepath.Join(link, "board.html")}); reasonOf(err) != ReasonOutInBoard {
			t.Fatalf("--out through a symlink to the board folder: %v, want %s", err, ReasonOutInBoard)
		}
		// A symlink in the board folder that points out of it: the rename
		// would replace the symlink with the page, in the board folder.
		outside := filepath.Join(f.dirs.Root, "outside.html")
		if err := os.WriteFile(outside, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		inside := filepath.Join(folder, "link.html")
		if err := os.Symlink(outside, inside); err != nil {
			t.Fatal(err)
		}
		if err := run(WorkItemOptions{Out: inside}); reasonOf(err) != ReasonOutInBoard {
			t.Fatalf("--out a symlink in the board folder that points out of it: %v, want %s", err, ReasonOutInBoard)
		}
		if err := os.Remove(inside); err != nil {
			t.Fatal(err)
		}
	}
	if err := run(WorkItemOptions{Out: "relative.html"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.dirs.Root, "relative.html")); err != nil {
		t.Fatalf("a relative --out was not written in the caller's directory: %v", err)
	}
}

// TestWorkItemPageFoldKey: one key for the spellings strings.EqualFold
// takes as one, the listing's own comparison, and two keys otherwise.
func TestWorkItemPageFoldKey(t *testing.T) {
	t.Parallel()
	for _, pair := range [][2]string{{"Σigma", "σigma"}, {"Σigma", "ςigma"}, {"σigma", "ςigma"}, {"Bob@Example.com", "bob@example.COM"}, {"K", "\u212a"}} {
		if !strings.EqualFold(pair[0], pair[1]) || foldKey(pair[0]) != foldKey(pair[1]) {
			t.Errorf("%q and %q: EqualFold %v, keys %q and %q", pair[0], pair[1], strings.EqualFold(pair[0], pair[1]), foldKey(pair[0]), foldKey(pair[1]))
		}
	}
	// A key reads as the lower case of an ASCII name, so a page address
	// carries "server", not some other fold of it.
	for in, want := range map[string]string{"Server": "server", "KS": "ks", "Σ": "σ", "ς": "σ"} {
		if got := foldKey(in); got != want {
			t.Errorf("foldKey(%q) = %q, want %q", in, got, want)
		}
	}
	// Every code point: its key is in its own set of simple folds, and so
	// is every member's. The sets do not overlap, so one key for two
	// strings is one set for each pair of their characters, which is what
	// EqualFold compares.
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		key := []rune(foldKey(string(r)))[0]
		inSet := key == r
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			inSet = inSet || key == f
			if other := []rune(foldKey(string(f)))[0]; other != key {
				t.Fatalf("U+%04X and U+%04X fold together and have the keys U+%04X and U+%04X", r, f, key, other)
			}
		}
		if !inSet {
			t.Fatalf("the key of U+%04X is U+%04X, outside its set of folds", r, key)
		}
	}
	// Two names EqualFold tells apart stay two choices.
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	board := &workitem.Board{
		Lists: []workitem.List{{Key: "todo", Name: "Todo"}},
		Items: []*workitem.Item{
			{ID: "a", Number: 1, Title: "A", List: "todo", Tags: []string{"İ"}, CreatedAt: at, UpdatedAt: at},
			{ID: "b", Number: 2, Title: "B", List: "todo", Tags: []string{"i"}, CreatedAt: at, UpdatedAt: at},
		},
	}
	if data := boardPageFrom(board, "", at); len(data.Tags) != 2 || data.Items[0].TagKeys[0] == data.Items[1].TagKeys[0] {
		t.Errorf("İ and i: choices %v, keys %q and %q, want two of each", data.Tags, data.Items[0].TagKeys, data.Items[1].TagKeys)
	}
	for _, pair := range [][2]string{{"İ", "i"}, {"ı", "i"}, {"web", "webs"}, {"ss", "\u00df"}, {"api", "apı"}} {
		if strings.EqualFold(pair[0], pair[1]) || foldKey(pair[0]) == foldKey(pair[1]) {
			t.Errorf("%q and %q share a key", pair[0], pair[1])
		}
	}
}

// pageHarness runs the page's script under Node with no document, as a
// browser would run it but for the part that builds the page, and applies
// its pure functions to the cases on stdin: each filter set's view, each
// address's filters, and each address back from its filters.
const pageHarness = `
const vm = require("vm");
const fs = require("fs");
const script = fs.readFileSync(process.argv[2], "utf8");
const input = JSON.parse(fs.readFileSync(0, "utf8"));
const sandbox = {};
vm.runInNewContext(script, sandbox);
const p = sandbox.brigadeBoardPage;
const views = input.filters.map(function (f) {
  const v = p.view(input.data, f);
  return { columns: v.columns.map(function (c) { return { list: c.list.key, numbers: c.items.map(function (it) { return it.number; }) }; }), left: v.left };
});
const parsed = input.hashes.map(function (h) { return p.parseHash(h, input.data); });
const back = parsed.map(function (f) { return p.toHash(f); });
process.stdout.write(JSON.stringify({ views: views, parsed: parsed, back: back }));
`

// pageColumn and pageView are what the harness reports of one view.
type pageColumn struct {
	List    string `json:"list"`
	Numbers []int  `json:"numbers"`
}

type pageView struct {
	Columns []pageColumn `json:"columns"`
	Left    []string     `json:"left"`
}

// pageFilters is the script's filter set.
type pageFilters struct {
	Assignee string `json:"assignee"`
	Tag      string `json:"tag"`
	List     string `json:"list"`
	Hidden   bool   `json:"hidden"`
	Closed   bool   `json:"closed"`
}

// TestWorkItemPageScriptFilters runs the page's own filter functions under
// Node, on a board with a hidden list, a list the board no longer has, a
// closed work item, and an assignee and a tag spelt with three forms of
// sigma: each filter alone and together, the hidden lists' switch, a list
// named in the filter, and addresses that name choices the page has, has
// not, or cannot read. Node is needed in CI on Linux; elsewhere the test
// is skipped without it.
func TestWorkItemPageScriptFilters(t *testing.T) {
	t.Parallel()
	node, err := exec.LookPath("node")
	if err != nil {
		if os.Getenv("CI") != "" && runtime.GOOS == "linux" {
			t.Fatalf("node is not on PATH, and CI needs it to run the page's script: %v", err)
		}
		t.Skip("node is not on PATH")
	}
	at := time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	item := func(n int, list, assignee string, closed bool, tags ...string) *workitem.Item {
		return &workitem.Item{ID: "i" + strconv.Itoa(n), Number: n, Title: "Item " + strconv.Itoa(n), List: list, Assignee: assignee,
			Tags: tags, Closed: closed, CreatedAt: at, UpdatedAt: at}
	}
	board := &workitem.Board{
		Lists: []workitem.List{{Key: "todo", Name: "Todo"}, {Key: "parked", Name: "Parked", Hidden: true}, {Key: "done", Name: "Done"}},
		Items: []*workitem.Item{
			item(1, "todo", "Σam@x", false, "Σigma"),
			item(2, "todo", "ςam@x", false, "σigma", "web"),
			item(3, "parked", "bob", false, "web"),
			item(4, "done", "", true),
			item(5, "old", "bob", false),
			item(6, "done", "", false, "WEB"),
		},
	}
	data := boardPageFrom(board, "ops", at)
	sam, web, bob := "="+foldKey("σam@x"), "="+foldKey("web"), "="+foldKey("bob")
	if len(data.Assignees) != 2 || len(data.Tags) != 2 {
		t.Fatalf("the choices: %v %v, want two assignees and two tags", data.Assignees, data.Tags)
	}
	col := func(list string, numbers ...int) pageColumn {
		return pageColumn{List: list, Numbers: append([]int{}, numbers...)}
	}
	cases := []struct {
		name string
		f    pageFilters
		want pageView
	}{
		{"no filter", pageFilters{}, pageView{[]pageColumn{col("todo", 1, 2), col("done", 6), col("old", 5)}, []string{"Parked (1)"}}},
		{"an assignee spelt two ways", pageFilters{Assignee: sam}, pageView{[]pageColumn{col("todo", 1, 2), col("done")}, []string{}}},
		{"nobody", pageFilters{Assignee: "-"}, pageView{[]pageColumn{col("todo"), col("done", 6)}, []string{}}},
		{"a tag spelt three ways", pageFilters{Tag: "=" + foldKey("Σigma")}, pageView{[]pageColumn{col("todo", 1, 2), col("done")}, []string{}}},
		{"a tag in two cases, one in a hidden list", pageFilters{Tag: web}, pageView{[]pageColumn{col("todo", 2), col("done", 6)}, []string{"Parked (1)"}}},
		{"the hidden lists", pageFilters{Hidden: true}, pageView{[]pageColumn{col("todo", 1, 2), col("parked", 3), col("done", 6), col("old", 5)}, []string{}}},
		{"a hidden list named", pageFilters{List: "parked"}, pageView{[]pageColumn{col("parked", 3)}, []string{}}},
		{"another list named, the hidden ones on", pageFilters{List: "done", Hidden: true}, pageView{[]pageColumn{col("done", 6)}, []string{}}},
		{"the closed ones", pageFilters{List: "done", Closed: true}, pageView{[]pageColumn{col("done", 4, 6)}, []string{}}},
		{"an assignee and the hidden lists", pageFilters{Assignee: bob, Hidden: true}, pageView{[]pageColumn{col("todo"), col("parked", 3), col("done"), col("old", 5)}, []string{}}},
		{"an assignee and a tag", pageFilters{Assignee: sam, Tag: web}, pageView{[]pageColumn{col("todo", 2), col("done")}, []string{}}},
	}
	whole := "#assignee=" + url.QueryEscape(sam) + "&tag=" + url.QueryEscape(web) + "&list=parked&hidden=1&closed=1"
	whole = strings.ReplaceAll(whole, "+", "%20")
	hashes := []string{whole, "#tag=%3Dgone&list=nolist&assignee=%3Dnobody%40x&hidden=yes&closed=0", "#tag=%E0%A4%A&assignee=-", "", "#junk&=1&list"}
	wantParsed := []pageFilters{{Assignee: sam, Tag: web, List: "parked", Hidden: true, Closed: true}, {}, {Assignee: "-"}, {}, {}}

	filters := make([]pageFilters, 0, len(cases))
	for _, c := range cases {
		filters = append(filters, c.f)
	}
	input, err := json.Marshal(map[string]any{"data": data, "filters": filters, "hashes": hashes})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	scriptFile, harnessFile := filepath.Join(dir, "page.js"), filepath.Join(dir, "harness.js")
	if err := os.WriteFile(scriptFile, []byte(pageScript), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(harnessFile, []byte(pageHarness), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(t.Context(), node, harnessFile, scriptFile) //nolint:gosec // G204: the test's own node and files
	cmd.Stdin = bytes.NewReader(input)
	raw, err := cmd.Output()
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			t.Fatalf("node: %v: %s", err, exit.Stderr)
		}
		t.Fatalf("node: %v", err)
	}
	var got struct {
		Views  []pageView    `json:"views"`
		Parsed []pageFilters `json:"parsed"`
		Back   []string      `json:"back"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("the harness's output: %v: %s", err, raw)
	}
	for i, c := range cases {
		if !reflect.DeepEqual(got.Views[i], c.want) {
			t.Errorf("%s: %+v\nwant %+v", c.name, got.Views[i], c.want)
		}
	}
	for i, want := range wantParsed {
		if got.Parsed[i] != want {
			t.Errorf("parseHash(%q) = %+v, want %+v", hashes[i], got.Parsed[i], want)
		}
	}
	if got.Back[0] != whole {
		t.Errorf("toHash(parseHash(%q)) = %q", whole, got.Back[0])
	}
}
