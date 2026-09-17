package commands

import (
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// This file is the human-output layer of 6.4: every remote string is
// sanitised with the 6.7 sanitiser before it is printed, folded onto ONE
// line (the layouts are one item per line, and a name carrying a newline
// must not become two items), and human_label always carries the
// unverified suffix (B-3). The --json forms carry the sanitised strings
// unfolded: a JSON string escapes its newlines itself.

// oneLine folds every run of whitespace — newlines and tabs included,
// which the sanitiser deliberately keeps — onto one space and trims the
// ends, so a value can never break a one-item-per-line layout.
func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// dropBreakers removes the four characters the frame's attribute rule
// drops from an opaque id: an id is printed in full (the model copies it
// into `brigade send`), so it gets no cap, but it must not be able to
// quote, close a tag or break a line.
func dropBreakers(s string) string {
	return strings.Map(func(r rune) rune {
		switch r {
		case '"', '<', '>', '\n':
			return -1
		default:
			return r
		}
	}, s)
}

// sanitizeID sanitises an opaque id (session, message, principal, team
// ref) for both output forms: rules 1-2 plus the attribute breakers, no
// cap.
func sanitizeID(s string) string {
	return dropBreakers(protocol.Sanitize(s))
}

// idLine is sanitizeID folded onto one line, for the human form.
func idLine(s string) string { return oneLine(sanitizeID(s)) }

// nameLine sanitises a session or team name for the human form.
func nameLine(s string) string { return oneLine(protocol.SanitizeName(s)) }

// pathLine sanitises a local path the hook resolved (the plugin binary in
// `whoami`) for the human form: no cap, because the path is printed to be
// copied into a symlink, and the attribute breakers dropped so it can
// neither quote nor break its line.
func pathLine(s string) string { return oneLine(sanitizeID(s)) }

// labelLine sanitises a human label for the human form and appends the
// unverified suffix; an empty label renders as the suffix alone, so the
// column is never silently absent.
func labelLine(s string) string {
	label := oneLine(protocol.SanitizeLabel(s))
	if label == "" {
		return strings.TrimSpace(UnverifiedSuffix)
	}
	return label + UnverifiedSuffix
}

// shortPrincipalChars is how much of a principal ref the roster prints
// beside a label (card 24): eight characters tell two principals apart at
// a glance without crowding a line that already carries six columns.
const shortPrincipalChars = 8

// shortPrincipal renders the leading characters of a sanitised principal
// ref for the human form. A ref shorter than the cap is taken whole, and
// an empty one stays empty so the caller can drop the column.
func shortPrincipal(s string) string {
	id := idLine(s)
	if r := []rune(id); len(r) > shortPrincipalChars {
		return string(r[:shortPrincipalChars])
	}
	return id
}

// memberLine renders the roster's member column: the label with the
// unverified suffix (B-3) and a short principal beside it, so a reader
// sees who a session belongs to and still has the stable anchor — the
// label is never an identity, and two members may pick the same one.
// An empty label renders as "", and the caller keeps the
// `principal=<ref>` column it has always printed, so an unlabelled
// member's line does not change shape. A ref that sanitises away renders
// as "[?]": a labelled line always carries the bracket, so a missing
// anchor is visible rather than silently absent.
func memberLine(label, principalRef string) string {
	l := oneLine(protocol.SanitizeLabel(label))
	if l == "" {
		return ""
	}
	short := shortPrincipal(principalRef)
	if short == "" {
		short = "?"
	}
	return l + UnverifiedSuffix + " [" + short + "]"
}

// workspaceLine sanitises a registered workspace label for the human
// form with the label rules; "" stays "" so the caller omits the column.
func workspaceLine(s string) string { return oneLine(protocol.SanitizeLabel(s)) }

// attrLine sanitises a short free-text value (an adapter name or version,
// a harness name) with the attribute rules: 64 code points, no breakers.
func attrLine(s string) string { return oneLine(protocol.SanitizeAttribute(s)) }

// enumLine keeps a wire enumeration value printable: the adapters
// validated it against a closed set, so this is belt and braces.
func enumLine(s string) string { return oneLine(protocol.SanitizeAttribute(s)) }

// modelLine sanitises a harness-reported model identity for the human
// form exactly as nameLine does a session name: the 6.7 rules 1-3 with
// the 128-code-point cap of 4.5.11 (every tag-like `<` neutralised, so the
// value can open no tag), folded onto one line. It is text a remote
// harness derived from its own transcript — unverified, like the name
// beside it — and it gets the same treatment, no more and no less.
func modelLine(s string) string { return oneLine(protocol.SanitizeModel(s)) }

// tokensLine renders a context occupancy for the human form: exact below
// a thousand, otherwise thousands rounded to the nearest (189681 ->
// "190k", 1500 -> "2k", 999 -> "999"). The column is an at-a-glance
// figure in a line that already carries six others; the --json form
// carries the integer the adapter returned.
func tokensLine(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return strconv.Itoa((n+500)/1000) + "k"
}

// seenAgo renders the "seen <n>s ago" column from the adapter's own clock
// (server_time), so the age is stable for a given result and never depends
// on the local clock; a zero server time falls back to now.
func seenAgo(at, server, now time.Time) string {
	if at.IsZero() {
		return "seen never"
	}
	ref := server
	if ref.IsZero() {
		ref = now
	}
	secs := int(ref.Sub(at).Seconds())
	if secs < 0 {
		secs = 0
	}
	return "seen " + strconv.Itoa(secs) + "s ago"
}

// sanitizeRecord sanitises every remote string of a session record in
// place, for the --json form (U-06: the corpus name comes back sanitised
// in both forms).
func sanitizeRecord(r *protocol.SessionRecord) {
	r.SessionID = sanitizeID(r.SessionID)
	r.SessionName = protocol.SanitizeName(r.SessionName)
	if r.SessionDescription != nil {
		d := protocol.SanitizeDescription(*r.SessionDescription)
		r.SessionDescription = &d
	}
	r.PrincipalRef = sanitizeID(r.PrincipalRef)
	r.HumanLabel = protocol.SanitizeLabel(r.HumanLabel)
	r.State = protocol.SanitizeAttribute(r.State)
	r.Activity = protocol.SanitizeAttribute(r.Activity)
	r.Inbound = protocol.SanitizeAttribute(r.Inbound)
	r.Harness = protocol.SanitizeAttribute(r.Harness)
	r.HarnessVersion = protocol.SanitizeAttribute(r.HarnessVersion)
	if r.WorkspaceLabel != nil {
		l := protocol.SanitizeLabel(*r.WorkspaceLabel)
		r.WorkspaceLabel = &l
	}
	// model is unverified harness-reported text like human_label (4.5.11),
	// so it is sanitised in both forms; context_used_tokens is an integer
	// the wire validation already bounded, so there is nothing to sanitise.
	if r.Model != nil {
		m := protocol.SanitizeModel(*r.Model)
		r.Model = &m
	}
}

// itoa is strconv.Itoa under a shorter name for the message builders.
func itoa(n int) string { return strconv.Itoa(n) }

// columns joins the fields of one output line with the two-space
// separator of the documented layouts.
func columns(fields ...string) string {
	return strings.Join(fields, "  ")
}

// tableRow renders one Markdown pipe-table row from already-sanitised
// cells, escaping any literal "|" so a cell can never split a column
// (session names, labels and models are unverified remote text and may
// carry one).
//
// The backslash is escaped FIRST, and that order is the whole of the
// guarantee: the 6.7 sanitiser keeps backslashes, so escaping the pipe
// alone would turn a cell's own `\|` into `\\|` — an escaped backslash
// followed by a LIVE delimiter — and a session named `x\|idle\|accept`
// would shift every cell to its right and forge the columns a reader
// uses to decide who they are messaging.
func tableRow(cells ...string) string {
	escaped := make([]string, len(cells))
	for i, c := range cells {
		c = strings.ReplaceAll(c, "\\", "\\\\")
		escaped[i] = strings.ReplaceAll(c, "|", "\\|")
	}
	return "| " + strings.Join(escaped, " | ") + " |"
}

// tableDivider renders the Markdown header/body divider row for n columns.
func tableDivider(n int) string {
	cells := make([]string, n)
	for i := range cells {
		cells[i] = "---"
	}
	return "| " + strings.Join(cells, " | ") + " |"
}

// seenAgoCell is seenAgo without its "seen " word, for a column that is
// already labelled by its own table header.
func seenAgoCell(at, server, now time.Time) string {
	return strings.TrimPrefix(seenAgo(at, server, now), "seen ")
}
