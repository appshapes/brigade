package releaseemail

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Run is the command: it renders a finished email, or with -check reads a draft and reports every finding.
// Diagnostics go to stderr, one finding a line; nothing goes to stdout. It returns 0 when the Markdown
// renders, 1 when it does not or a file cannot be read or written, 2 for a usage error.
//
//	brigade-release-email -in email.md -window window.txt -out email.html -repo owner/name -server https://github.com -commit <sha> [-date YYYY-MM-DD]
//	brigade-release-email -check -in notes.md [-root <checkout>]
func Run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("brigade-release-email", flag.ContinueOnError)
	fs.SetOutput(stderr)
	in := fs.String("in", "", "the Markdown to render: the finished email (draft and footer), or with -check the draft alone")
	out := fs.String("out", "", "where to write the HTML (0600)")
	window := fs.String("window", "", "the window file: one tag a line, oldest first; a ### heading's version links to its release")
	repo := fs.String("repo", "", "the repository, owner/name")
	server := fs.String("server", "https://github.com", "the GitHub server")
	commit := fs.String("commit", "", "the commit the images are pinned to (GITHUB_SHA)")
	date := fs.String("date", "", "the masthead's date, YYYY-MM-DD; empty leaves it out")
	check := fs.Bool("check", false, "read a draft and report every finding; write nothing")
	root := fs.String("root", ".", "the checkout, where an image's file must exist")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || *in == "" {
		fmt.Fprintln(stderr, "usage: brigade-release-email -in <markdown> -out <html> -window <file> -repo <owner/name> -commit <sha> [-server <url>] [-date <YYYY-MM-DD>]")
		fmt.Fprintln(stderr, "       brigade-release-email -check -in <draft> [-root <checkout>]")
		return 2
	}
	src, err := os.ReadFile(*in)
	if err != nil {
		fmt.Fprintf(stderr, "brigade-release-email: %v\n", err)
		return 1
	}

	if *check {
		if err := Check(string(src), *root); err != nil {
			report(stderr, err)
			return 1
		}
		return 0
	}

	if *out == "" || *repo == "" || *commit == "" {
		fmt.Fprintln(stderr, "brigade-release-email: -out, -repo and -commit are required to render (or pass -check)")
		return 2
	}
	o := Options{Repo: *repo, Server: strings.TrimRight(*server, "/"), Commit: *commit, Root: *root}
	if *date != "" {
		d, err := time.Parse("2006-01-02", *date)
		if err != nil {
			fmt.Fprintf(stderr, "brigade-release-email: -date is not YYYY-MM-DD: %q\n", *date)
			return 2
		}
		o.Date = d
	}
	if *window != "" {
		data, err := os.ReadFile(*window)
		if err != nil {
			fmt.Fprintf(stderr, "brigade-release-email: %v\n", err)
			return 1
		}
		for _, line := range strings.Split(string(data), "\n") {
			if tag := strings.TrimSpace(line); tag != "" {
				o.Window = append(o.Window, tag)
			}
		}
	}
	html, err := Render(string(src), o)
	if err != nil {
		report(stderr, err)
		return 1
	}
	//nolint:gosec // G703: -out is where the workflow step says to write; a command-line tool writes where it is told
	if err := os.WriteFile(*out, html, 0o600); err != nil {
		fmt.Fprintf(stderr, "brigade-release-email: %v\n", err)
		return 1
	}
	return 0
}

// report writes every finding on its own line, so the lint can show them all to the writer's fix cycle.
func report(stderr io.Writer, err error) {
	var fs Findings
	if errors.As(err, &fs) {
		for _, f := range fs {
			fmt.Fprintf(stderr, "brigade-release-email: %s\n", f.Error())
		}
		return
	}
	fmt.Fprintf(stderr, "brigade-release-email: %v\n", err)
}
