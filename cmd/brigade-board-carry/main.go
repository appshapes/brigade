// Command brigade-board-carry carries a Brigade Board over to another
// team's board folder (work item 272), and adds a tracker's open cards to
// it under the numbers they had:
//
//	brigade-board-carry copy --from <board folder> --to <board folder>
//	brigade-board-carry add  --to <board folder> --cards <file.json> --origin "Trello card" --by <member label> [--session <name>]
//	brigade-board-carry list --to <board folder>
//
// A board folder is the one `brigade sync status --json` names for the
// team's board, under `boards/` of Brigade's state directory. `copy`
// writes nothing unless the second folder is empty or holds only what a
// run before copied, and reads the board back against the first before it
// says it is done. `add` reads a JSON array of cards (`number`, `title`,
// `description`, `list`, `tags`, `comments`), checks every one, and then
// writes them: a number the board has free is kept, and a card whose
// number is taken gets the next free number above both sequences, with
// "<origin> <number>" as the first line of its description; a card a run
// before left without some of its comments is given them. `list` prints
// the board in a folder, every list and the closed work items too.
//
// Run it into a scratch folder first: that is the dry run. The command is
// DEV-ONLY and never shipped; `make build` does not build it.
package main

import (
	"encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/appshapes/brigade/internal/boardcarry"
	"github.com/appshapes/brigade/internal/harness/workitem"
)

const usage = `usage: brigade-board-carry copy --from <board folder> --to <board folder>
       brigade-board-carry add  --to <board folder> --cards <file.json> --origin "Trello card" --by <member label> [--session <name>]
       brigade-board-carry list --to <board folder>
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the command with its streams handed in.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	from := flags.String("from", "", "")
	to := flags.String("to", "", "")
	cards := flags.String("cards", "", "")
	origin := flags.String("origin", "", "")
	by := flags.String("by", "", "")
	session := flags.String("session", "board-carry-over", "")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	switch {
	case args[0] == "copy" && *from != "" && *to != "":
		done, err := boardcarry.Copy(*from, *to)
		if done != nil {
			_, _ = fmt.Fprintf(stdout, "board-carry: %d work items in %d changes; %d written, %d there already; %d files of the first folder are no change and were left\n",
				done.Items, done.Changes, done.Written, done.Changes-done.Written, done.Left)
		}
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = io.WriteString(stdout, "board-carry: ok: the board carried over reads as the board it was copied from\n")
	case args[0] == "add" && *to != "" && *cards != "" && *origin != "" && *by != "":
		raw, err := os.ReadFile(*cards) //nolint:gosec // G703: the cards file the person running the command named
		if err != nil {
			return fail(stderr, err)
		}
		var list []boardcarry.Card
		// A member this command does not know is a mistake in the file: a
		// misspelt `comments` would carry none and say nothing.
		if err := json.Unmarshal(raw, &list, json.RejectUnknownMembers(true)); err != nil {
			return fail(stderr, fmt.Errorf("%s is not a list of cards: %w", *cards, err))
		}
		done, err := boardcarry.Add(*to, list, *origin, workitem.Author{Member: *by, Session: *session})
		for _, c := range done {
			how := "there already"
			switch {
			case c.Written:
				how = fmt.Sprintf("written, with %d comments", c.Comments)
			case c.Comments > 0:
				how = fmt.Sprintf("there already, %d comments it lacked written", c.Comments)
			}
			_, _ = fmt.Fprintf(stdout, "board-carry: %s %d -> work item %d (%s): %s\n", *origin, c.From, c.Number, how, c.Title)
		}
		if err != nil {
			return fail(stderr, err)
		}
		_, _ = fmt.Fprintf(stdout, "board-carry: ok: %d cards are on the board\n", len(done))
	case args[0] == "list" && *to != "":
		b, err := workitem.Read(*to)
		if err != nil {
			return fail(stderr, err)
		}
		for _, l := range b.Lists {
			shown := ""
			if l.Hidden {
				shown = " (hidden)"
			}
			_, _ = fmt.Fprintf(stdout, "list %s: %s%s\n", l.Key, l.Name, shown)
		}
		for _, it := range b.Items {
			state := it.List
			if it.Closed {
				state += ", closed"
			}
			_, _ = fmt.Fprintf(stdout, "%d [%s] %s (%d comments, %d changes in its history)\n", it.Number, state, it.Title, len(it.Comments), len(it.History))
		}
		_, _ = fmt.Fprintf(stdout, "board-carry: %d work items, %d sharing a number with an older one, next number %d, %d files that are no change\n",
			len(b.Items), len(b.Clashes()), b.NextNumber(), b.Skipped)
	default:
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	return 0
}

// fail prints the error and returns exit code 1.
func fail(stderr io.Writer, err error) int {
	_, _ = io.WriteString(stderr, "board-carry: "+err.Error()+"\n")
	return 1
}
