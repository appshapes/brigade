package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/workitem"
)

// invoke runs the command and answers its exit code and what it printed.
func invoke(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errb bytes.Buffer
	code = run(args, &out, &errb)
	return code, out.String(), errb.String()
}

// TestTheCommandCarriesABoardAndItsCards is the three verbs in the order
// the carry-over runs them, into a scratch folder: copy, add, list; then
// both again, which write nothing.
func TestTheCommandCarriesABoardAndItsCards(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	from, to, cards := filepath.Join(dir, "earlier"), filepath.Join(dir, "now"), filepath.Join(dir, "cards.json")
	s := workitem.Store{Folder: from}
	b, err := workitem.Read(from)
	if err != nil {
		t.Fatal(err)
	}
	title, list := "The board's own work item", b.Lists[0].Key
	if _, err := s.Add(b, workitem.Fields{Title: &title, List: &list}, workitem.Author{Member: "alice@example.com", Session: "alice-web"}); err != nil {
		t.Fatal(err)
	}
	file := `[{"number": 1, "title": "A card whose number is taken", "description": "What it asks for.", "tags": ["cavalry"], "comments": ["dana, 2026-10-02 (Trello):\nfirst"]},
	          {"number": 7, "title": "A card whose number is free"}]`
	if err := os.WriteFile(cards, []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}

	code, out, errs := invoke(t, "copy", "--from", from, "--to", to)
	if code != 0 || !strings.Contains(out, "1 work items in 1 changes; 1 written, 0 there already") || !strings.Contains(out, "board-carry: ok:") || errs != "" {
		t.Fatalf("copy: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	code, out, errs = invoke(t, "add", "--to", to, "--cards", cards, "--origin", "Trello card", "--by", "bob@example.com")
	if code != 0 || errs != "" ||
		!strings.Contains(out, "Trello card 1 -> work item 8 (written, with 1 comments): A card whose number is taken") ||
		!strings.Contains(out, "Trello card 7 -> work item 7 (written, with 0 comments): A card whose number is free") ||
		!strings.Contains(out, "board-carry: ok: 2 cards are on the board") {
		t.Fatalf("add: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	code, out, errs = invoke(t, "list", "--to", to)
	if code != 0 || errs != "" || !strings.Contains(out, "1 [todo] The board's own work item") || !strings.Contains(out, "7 [todo] A card whose number is free") ||
		!strings.Contains(out, "8 [todo] A card whose number is taken (1 comments") || !strings.Contains(out, "3 work items, 0 sharing a number with an older one, next number 9") {
		t.Fatalf("list: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	// What a card says is on the board, never in what the command prints.
	if strings.Contains(out, "What it asks for.") {
		t.Errorf("list printed a description:\n%s", out)
	}

	// Again: the copy is refused, because the second board has cards the
	// first does not, and the cards are there already.
	if code, out, errs = invoke(t, "copy", "--from", from, "--to", to); code != 1 || !strings.Contains(errs, "a change of its own") {
		t.Errorf("a second copy: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
	code, out, errs = invoke(t, "add", "--to", to, "--cards", cards, "--origin", "Trello card", "--by", "bob@example.com")
	if code != 0 || errs != "" || strings.Contains(out, "written, with") || strings.Count(out, "(there already)") != 2 {
		t.Errorf("a second add: exit %d\nstdout: %s\nstderr: %s", code, out, errs)
	}
}

// TestTheCommandRefusesWhatItCannotDo: a verb with a required flag
// missing, an unknown verb and a stray argument are usage, exit 2, with
// nothing done; a cards file with a member the command does not know, or
// that is no list of cards, is exit 1 with nothing written.
func TestTheCommandRefusesWhatItCannotDo(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	to := filepath.Join(dir, "now")
	for name, args := range map[string][]string{
		"no verb":            nil,
		"an unknown verb":    {"move", "--from", dir, "--to", to},
		"copy with no from":  {"copy", "--to", to},
		"add with no origin": {"add", "--to", to, "--cards", "cards.json", "--by", "bob@example.com"},
		"add with no member": {"add", "--to", to, "--cards", "cards.json", "--origin", "Trello card"},
		"list with no board": {"list"},
		"a stray argument":   {"list", "--to", to, "extra"},
		"an unknown flag":    {"list", "--to", to, "--verbose"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if code, out, errs := invoke(t, args...); code != 2 || out != "" || !strings.HasPrefix(errs, "usage: brigade-board-carry") {
				t.Errorf("exit %d\nstdout: %s\nstderr: %s", code, out, errs)
			}
		})
	}
	for name, file := range map[string]string{
		"a member misspelt":    `[{"number": 7, "title": "x", "coments": ["lost"]}]`,
		"no list of cards":     `{"number": 7, "title": "x"}`,
		"a card with no title": `[{"number": 7}]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			cards := filepath.Join(t.TempDir(), "cards.json")
			if err := os.WriteFile(cards, []byte(file), 0o600); err != nil {
				t.Fatal(err)
			}
			board := filepath.Join(t.TempDir(), "now")
			if code, out, errs := invoke(t, "add", "--to", board, "--cards", cards, "--origin", "Trello card", "--by", "bob@example.com"); code != 1 || out != "" || !strings.HasPrefix(errs, "board-carry: ") {
				t.Errorf("exit %d\nstdout: %s\nstderr: %s", code, out, errs)
			}
			if _, err := os.Stat(board); !os.IsNotExist(err) {
				t.Errorf("the refused run made the board's folder: %v", err)
			}
		})
	}
}
