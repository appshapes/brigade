package protocol

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// This file is the sanitiser of plan 6.7, shared by the watcher, the
// prompt-hook poll, every human-readable and --json output of the CLI,
// and every remote string the hook prints as context. Everything that
// crosses from another principal to this user's model goes through here.
//
// The pipeline, in a fixed order that the security argument depends on:
//
//  1. Repair to valid UTF-8, normalise to NFC (golang.org/x/text — the
//     standard library has no NFC), strip C0/C1 controls except \n and
//     \t, strip every Unicode Cf (format) character including the bidi
//     overrides U+202A-U+202E and U+2066-U+2069 and zero-width joiners,
//     then normalise to NFC once more. Stripping runs after the first
//     NFC so a format character can never hide a tag (`<​brigade-…`
//     re-joins and is then caught by rule 2); the second NFC restores
//     the normal form that stripping a composition-blocking character
//     may have broken, which is what makes the whole pipeline
//     idempotent (the fuzz target asserts it).
//  2. Neutralise any '<' that starts — case-insensitively, with
//     optional whitespace, opening or closing — one of the five tag
//     families by replacing the '<' with "&lt;", so a body can never
//     close or forge a frame.
//  3. Truncate to the protocol cap on a UTF-8 boundary and append
//     TruncationMarker when it had to. Truncation runs last: cutting
//     never runs before the tag scan, so it cannot split a tag out of
//     the matcher's sight, and the appended marker is plain text.
//  4. (Attributes only, SanitizeAttribute.) Additionally drop '"', '<',
//     '>' and newlines and cap at 64 code points, mirroring the
//     harness's own from-name normalisation.
//
// Decisions recorded for corpus item 25-tag-matcher-evasion (the
// execution log leaves them to P1-2):
//
//   - A close tag split mid-name ("</brigade-\nmessage>") is NOT
//     neutralised. No consuming parser — Brigade's own frame parser,
//     the harness's wrapper parse, or any XML/HTML tokenizer — joins a
//     tag name across whitespace, so the split form cannot close or
//     forge a frame (TestForgedCloseCannotCloseFrame proves it against
//     the frame-parser stub). Widening the matcher to skip whitespace
//     inside names would neutralise legitimate text without closing any
//     real channel.
//   - A pre-encoded "&lt;brigade-message" is NOT re-encoded. It is
//     byte-identical to this sanitiser's own output for a neutralised
//     tag, contains no '<', and nothing in the pipeline ever
//     entity-decodes (the frame is plain text, not HTML), so it is
//     already inert. Escaping '&' would corrupt legitimate bodies and
//     destroy idempotence: text that passes through the sanitiser twice
//     (the watcher and then a CLI rendering) must not degrade.

// TruncationMarker is appended by rule 3 when a value had to be cut to
// its protocol cap. The truncated value INCLUDING the marker stays
// within the cap, so a sanitised value always passes the corresponding
// wire validation.
const TruncationMarker = "[truncated]"

// attributeMaxCodepoints is rule 4's cap on a tag attribute value,
// mirroring the harness's own from-name normalisation (6.7).
const attributeMaxCodepoints = 64

// forgeableTagFamilies are the five tag families of 6.7 rule 2. The
// list is deliberately closed: corpus item 24-unlisted-forged-tag
// proves an unlisted tag reaches the model verbatim, and widening the
// list is a frame/sanitiser design change (execution log, P0-1), not a
// local edit.
var forgeableTagFamilies = []string{
	"brigade-message",
	"cross-session-message",
	"teammate-message",
	"channel",
	"system-reminder",
}

// Sanitize applies rules 1 and 2 — repair, NFC, control and format
// stripping, tag neutralisation — with no truncation. Use the
// field-specific variants for anything with a protocol cap.
func Sanitize(s string) string {
	s = strings.ToValidUTF8(s, "�")
	s = norm.NFC.String(s)
	s = stripControls(s)
	s = norm.NFC.String(s)
	return neutralizeTags(s)
}

// SanitizeBody sanitises a message body: rules 1-3 with the
// MaxBodyBytes byte cap.
func SanitizeBody(s string) string {
	return truncateBytes(Sanitize(s), MaxBodyBytes)
}

// SanitizeSummary sanitises a sender summary: rules 1-3 with the
// MaxSummaryChars code-point cap.
func SanitizeSummary(s string) string {
	return truncateRunes(Sanitize(s), MaxSummaryChars)
}

// SanitizeName sanitises a session or team name: rules 1-3 with the
// MaxSessionNameCodepoints code-point cap.
func SanitizeName(s string) string {
	return truncateRunes(Sanitize(s), MaxSessionNameCodepoints)
}

// SanitizeLabel sanitises a human label: rules 1-3 with the
// MaxHumanLabelChars code-point cap.
func SanitizeLabel(s string) string {
	return truncateRunes(Sanitize(s), MaxHumanLabelChars)
}

// SanitizeDescription sanitises a session description: rules 1-3 with
// the MaxDescriptionChars code-point cap.
func SanitizeDescription(s string) string {
	return truncateRunes(Sanitize(s), MaxDescriptionChars)
}

// SanitizeAttribute sanitises a value destined for a tag attribute in
// the injected frame (rule 4): rules 1-2, then '"', '<', '>' and
// newlines are dropped so the value can neither escape its quotes nor
// close the tag line, then a 64-code-point cap with no marker (a marker
// would spend 11 of the 64).
func SanitizeAttribute(s string) string {
	s = Sanitize(s)
	s = strings.Map(func(r rune) rune {
		switch r {
		case '"', '<', '>', '\n':
			return -1
		default:
			return r
		}
	}, s)
	if utf8.RuneCountInString(s) <= attributeMaxCodepoints {
		return s
	}
	n := 0
	for i := range s {
		if n == attributeMaxCodepoints {
			return s[:i]
		}
		n++
	}
	return s
}

// stripControls removes every C0/C1 control character except '\n' and
// '\t' (Cc: U+0000-U+001F, U+007F-U+009F) and every Unicode format
// character (Cf), which covers the bidi overrides U+202A-U+202E and
// U+2066-U+2069, zero-width joiners and non-joiners, zero-width spaces,
// soft hyphens and the BOM.
func stripControls(s string) string {
	// Fast path: most strings carry nothing to strip.
	clean := true
	for _, r := range s {
		if isStrippedControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isStrippedControl(r) {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isStrippedControl reports whether rule 1 removes the rune.
func isStrippedControl(r rune) bool {
	if r == '\n' || r == '\t' {
		return false
	}
	return unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r)
}

// neutralizeTags applies rule 2: every '<' that starts — after optional
// whitespace and an optional '/' — one of the five tag families,
// case-insensitively, becomes "&lt;". Only the '<' is replaced; the
// rest of the forged tag stays visible as inert text. Matching is a
// prefix test on purpose: "<brigade-messages" is neutralised too,
// because over-matching renders harmless text while under-matching
// forges a frame.
func neutralizeTags(s string) string {
	if !strings.Contains(s, "<") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 3*len(forgeableTagFamilies))
	for i := range len(s) {
		if s[i] == '<' && startsForgeableTag(s[i+1:]) {
			b.WriteString("&lt;")
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// startsForgeableTag reports whether rest — the text after a '<' —
// begins a listed tag family: optional whitespace, an optional '/'
// (closing form), optional whitespace again, then a family name,
// case-insensitively. Whitespace is unicode.IsSpace, so a space, tab,
// newline or Unicode space between '<' and the name does not hide the
// tag (corpus item 25).
func startsForgeableTag(rest string) bool {
	i := skipTagSpace(rest, 0)
	if i < len(rest) && rest[i] == '/' {
		i = skipTagSpace(rest, i+1)
	}
	rest = rest[i:]
	for _, name := range forgeableTagFamilies {
		if hasFoldPrefix(rest, name) {
			return true
		}
	}
	return false
}

// hasFoldPrefix reports whether s begins with name under Unicode simple
// case folding, compared RUNE-wise. A byte-sliced strings.EqualFold
// would mis-align on multi-byte fold pairs (U+017F LATIN SMALL LETTER
// LONG S folds to 's', so "ſystem-reminder" must match
// "system-reminder" the way a case-insensitive regexp would).
func hasFoldPrefix(s, name string) bool {
	for _, want := range name {
		r, size := utf8.DecodeRuneInString(s)
		if size == 0 || !runesFoldEqual(r, want) {
			return false
		}
		s = s[size:]
	}
	return true
}

// runesFoldEqual reports Unicode simple-fold equality of two runes —
// the same relation Go's regexp uses for (?i).
func runesFoldEqual(a, b rune) bool {
	if a == b {
		return true
	}
	for f := unicode.SimpleFold(a); f != a; f = unicode.SimpleFold(f) {
		if f == b {
			return true
		}
	}
	return false
}

// skipTagSpace advances i past any whitespace runes.
func skipTagSpace(s string, i int) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !unicode.IsSpace(r) {
			break
		}
		i += size
	}
	return i
}

// truncateBytes cuts s to at most limit BYTES on a UTF-8 boundary,
// appending TruncationMarker when it had to. The marker counts against
// the limit so the result always passes the byte-cap validation.
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	keep := limit - len(TruncationMarker)
	if keep < 0 {
		keep = 0
	}
	for keep > 0 && !utf8.RuneStart(s[keep]) {
		keep--
	}
	return s[:keep] + TruncationMarker
}

// truncateRunes cuts s to at most limit CODE POINTS, appending
// TruncationMarker when it had to. The marker counts against the limit
// so the result always passes the code-point-cap validation.
func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	keep := limit - utf8.RuneCountInString(TruncationMarker)
	if keep < 0 {
		keep = 0
	}
	n := 0
	for i := range s {
		if n == keep {
			return s[:i] + TruncationMarker
		}
		n++
	}
	return s
}
