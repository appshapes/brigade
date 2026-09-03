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

// attrLine sanitises a short free-text value (an adapter name or version,
// a harness name) with the attribute rules: 64 code points, no breakers.
func attrLine(s string) string { return oneLine(protocol.SanitizeAttribute(s)) }

// enumLine keeps a wire enumeration value printable: the adapters
// validated it against a closed set, so this is belt and braces.
func enumLine(s string) string { return oneLine(protocol.SanitizeAttribute(s)) }

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
}

// itoa is strconv.Itoa under a shorter name for the message builders.
func itoa(n int) string { return strconv.Itoa(n) }

// columns joins the fields of one output line with the two-space
// separator of the documented layouts.
func columns(fields ...string) string {
	return strings.Join(fields, "  ")
}
