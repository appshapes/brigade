package commands

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/harness/doing"
	"github.com/appshapes/brigade/internal/protocol"
)

// This file is the human-output layer of 6.4: every remote string is
// sanitised with the 6.7 sanitiser before it is printed and folded onto
// ONE line (the layouts are one item per line, and a name carrying a
// newline must not become two items). In the LINE layouts — `team
// members`, `team join`, `inbox` — a human_label always carries the
// unverified suffix (B-3); card 27 took it out of the roster's table,
// where it cost every row width and said the same thing every time, and
// `sessions --json`'s own `note` carries the warning instead. The --json
// forms carry the sanitised strings unfolded: a JSON string escapes its
// newlines itself.

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

// shortPrincipalChars is how much of a principal ref a member line prints
// beside a label (card 24), and how much the roster prints for a session
// that has NO label (card 27): eight characters tell two principals apart
// at a glance without crowding a line that already carries six columns.
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

// shortSessionChars is how much of a session id the SESSION column shows:
// enough to tell two sessions in the same table apart at a glance, without
// the full id — a UUID-length column — widening the whole table past a
// terminal or a chat window. `brigade send` still needs the id byte for
// byte, so this is a display shortening only; a reader who has to address
// a session reaches for `--json`, which always carries it in full.
const shortSessionChars = 5

// shortSession renders the trailing characters of a sanitised session id
// for the human form. An id shorter than the cap is taken whole, and one
// that sanitises away to nothing renders as "?" — the convention
// rosterMember uses for a principal ref that does the same.
//
// The "?" is not cosmetic. The wire requires a non-empty session_id, but
// an id of `"` or `<` satisfies that and still sanitises to "" (rules 1-2
// plus the attribute breakers), and with card 27's borders gone a row
// whose first cell were blank would begin with whitespace — exactly like
// the doing line doingRow indents under it. A session_name is free to
// start with the arrow, so that row could then pass for a continuation
// line of the row above it. A non-blank first cell is what keeps every
// row a row: the border used to do this work.
func shortSession(s string) string {
	id := idLine(s)
	if id == "" {
		return "?"
	}
	if r := []rune(id); len(r) > shortSessionChars {
		return string(r[len(r)-shortSessionChars:])
	}
	return id
}

// tableNameChars is how much of a session name the roster's NAME column
// shows (card 27). padTable pads a column to its widest cell, so a single
// session carrying a name at the protocol's 64-code-point cap widened the
// column for every row — one of the two things that pushed the table past
// a terminal. `--json` always carries the name in full.
const tableNameChars = 50

// tableName renders a session name for the roster's NAME column: the
// human form nameLine produces, cut to the column's cap with the marker
// inside it, exactly as descriptionLine cuts a doing line to the
// harness's shorter cap. Folding runs first (inside nameLine), so the cap
// counts the characters the cell will show.
func tableName(s string) string {
	return protocol.TruncateRunes(nameLine(s), tableNameChars)
}

// memberLine renders a member line: the label with the unverified suffix
// (B-3) and a short principal beside it, so a reader sees who a session
// belongs to and still has the stable anchor — the label is never an
// identity, and two members may pick the same one. An empty label renders
// as "", and the caller falls back to the identity it printed before this
// existed: `team members` — one line per member — its `principal=<ref>`
// field. A ref that sanitises away renders as "[?]": a labelled line
// always carries the bracket, so a missing anchor is visible rather than
// silently absent.
//
// The roster no longer calls this; card 27 gave it rosterMember below,
// which drops the suffix and the anchor a TABLE has no width for. The
// callers left are `team members` and `team join`, whose one-line-per-
// member layout has room for both.
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

// rosterMember renders the roster's MEMBER column (card 27): memberLine's
// shape without the two things a reader of a TABLE does not need beside a
// label — the unverified suffix, which the roster no longer carries in any
// cell, and the short principal, which `--json` carries in full for a
// reader who has to tell two principals apart.
//
// A session with no label has no other identity, so it keeps the short
// principal alone — "[b49eac08]", or "[?]" for a ref that sanitises away.
// The cell is therefore never blank, which is what retires the roster's
// LABEL and PRINCIPAL fallback columns: the identity of an unlabelled
// session used to live in those, and now lives here.
//
// `team members` and `team join` keep memberLine: their layout is one line
// per member, not a table, and it has room for the suffix and the anchor.
func rosterMember(label, principalRef string) string {
	if l := oneLine(protocol.SanitizeLabel(label)); l != "" {
		return l
	}
	short := shortPrincipal(principalRef)
	if short == "" {
		short = "?"
	}
	return "[" + short + "]"
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

// descriptionLine renders a session's doing line — its
// session_description, the one sentence its own model published (card
// 25) — for the continuation line doingRow prints under that session's
// row (card 27; it was a DOING column until then): rules 1-3 at the wire cap,
// folded onto one line (so it can neither start a row nor, through
// U+2028/U+2029, which are whitespace to strings.Fields, hide one), then
// cut to the harness's shorter table cap with the marker inside it
// (plan 5.5; ruling 11). Folding runs before THIS cut so the cap counts
// the characters the cell will show, not whitespace the fold removes;
// SanitizeDescription's own cut at the wire cap still runs before the
// fold, as it does for every consumer, so a whitespace-heavy value over
// 256 can carry the wire's marker into a cell folded well under 160.
// It is unverified text like the name beside it, published from the
// session by whoever holds it — its model, once Brigade publishes one;
// "" — nil, the empty string, or anything that sanitises to it — renders
// as "", which the caller treats as "no line".
func descriptionLine(s string) string {
	return protocol.TruncateRunes(oneLine(protocol.SanitizeDescription(s)), doing.MaxChars)
}

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
	// A description with nothing left once sanitised and folded is no
	// description: the wire form of "none" is "" (plan 5.3), so the member
	// is dropped rather than carried empty — --json says "absent" the one
	// way it has, and agrees with the roster, which prints no continuation
	// line for it. The kept value stays unfolded like every other --json string.
	if r.SessionDescription != nil {
		d := protocol.SanitizeDescription(*r.SessionDescription)
		if oneLine(d) == "" {
			r.SessionDescription = nil
		} else {
			r.SessionDescription = &d
		}
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

// borderBar is the box-drawing vertical bar. The roster stopped drawing
// its borders with it in card 27, but a cell carrying a literal one would
// still forge what looks like a column boundary in a padded plain-text
// table — there is no escape convention for plain text the way `\|` is
// one for a Markdown pipe table, so neutralizeCell replaces it outright.
const borderBar = "│"

// cellNeutraliser is what an unverified remote string — a session_name,
// human_label, model, workspace_label or session_description — may not
// carry into a cell.
//
// The box-drawing bar is there for the reason borderBar gives. The five
// after it are the code points that RENDER blank but are not
// unicode.IsSpace, so neither oneLine's fold (strings.Fields splits on
// unicode.IsSpace) nor the sanitiser's stripping (Cc and Cf only) touches
// them. Under the bordered table they were odd spacing inside a cell the
// border still closed, and card 25 deliberately let them through. Card 27
// made the column boundary a RUN OF TWO SPACES, and two blank glyphs in a
// row are indistinguishable from it: that is enough to forge a cell
// boundary, and enough to land a forged " (this session)" under the SEEN
// header of a row that is not this session. Each becomes a visible "?"
// for the same reason the bar becomes "|" — a reader has to be able to
// see that it is not the table's own spacing.
var cellNeutraliser = strings.NewReplacer(
	borderBar, "|",
	"\u115f", "?", // HANGUL CHOSEONG FILLER
	"\u1160", "?", // HANGUL JUNGSEONG FILLER
	"\u2800", "?", // BRAILLE PATTERN BLANK
	"\u3164", "?", // HANGUL FILLER
	"\uffa0", "?", // HALFWIDTH HANGUL FILLER
)

// neutralizeCell applies cellNeutraliser to one cell. It runs once per
// cell in padTable, and once more in doingRow for the continuation line
// padTable never sees.
func neutralizeCell(s string) string {
	return cellNeutraliser.Replace(s)
}

// padTable neutralises every cell of a table and pads each one with
// trailing spaces to its column's widest cell (in runes), so the table
// stays aligned as plain text — which is how `/brigade:sessions` shows
// it, inside a fenced code block, and how any terminal shows it. rows[0]
// is the header; every row must carry the same number of cells. It is the
// alignment alone: since card 27 the roster draws no borders, so the
// padding is all that holds a column together.
func padTable(rows [][]string) [][]string {
	widths := make([]int, len(rows[0]))
	padded := make([][]string, len(rows))
	for r, row := range rows {
		padded[r] = make([]string, len(row))
		for c, cell := range row {
			n := neutralizeCell(cell)
			padded[r][c] = n
			if w := utf8.RuneCountInString(n); w > widths[c] {
				widths[c] = w
			}
		}
	}
	for _, row := range padded {
		for c, cell := range row {
			if pad := widths[c] - utf8.RuneCountInString(cell); pad > 0 {
				row[c] = cell + strings.Repeat(" ", pad)
			}
		}
	}
	return padded
}

// tableRow renders one row of the roster from cells padTable has already
// neutralised and padded: the two-space separator of the documented
// layouts (card 27 dropped the borders it used to draw instead), with the
// last cell's padding trimmed so no row carries invisible trailing width.
func tableRow(cells []string) string {
	return strings.TrimRight(columns(cells...), " ")
}

// doingArrow marks the roster's doing line as a continuation of the row
// above it rather than a row of its own (card 27).
const doingArrow = "↳ "

// doingRow renders a session's doing line under its row: indented to the
// NAME column — the caller passes that row's padded SESSION cell, whose
// width plus the two-space separator IS that offset — marked with the
// arrow, and neutralised like every other cell, because this one never
// went through padTable. descriptionLine has already folded it onto one
// line and cut it to the harness's cap, so it can open no second line and
// forge no row. The parameter is `text`, not `doing`: the package of that
// name is imported here.
func doingRow(sessionCell, text string) string {
	return strings.Repeat(" ", utf8.RuneCountInString(sessionCell)+2) + doingArrow + neutralizeCell(text)
}

// seenAgoCell is seenAgo without its "seen " word or its trailing "ago",
// for a column that is already labelled by its own table header: "2s",
// not "seen 2s ago"; "never", not "seen never".
func seenAgoCell(at, server, now time.Time) string {
	return strings.TrimSuffix(strings.TrimPrefix(seenAgo(at, server, now), "seen "), " ago")
}
