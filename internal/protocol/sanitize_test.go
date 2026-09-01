package protocol

import (
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// rawForgeableTagRE is the INDEPENDENT raw-tag scanner used by the
// corpus invariant check below — deliberately not the package's own
// startsForgeableTag, so a positive control can catch a no-op sanitiser.
var rawForgeableTagRE = regexp.MustCompile(`(?i)<[\s\p{Z}]*/?[\s\p{Z}]*(brigade-message|cross-session-message|teammate-message|channel|system-reminder)`)

func TestSanitizeTable(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain text untouched", "hello world", "hello world"},
		{"newline and tab kept", "a\nb\tc", "a\nb\tc"},
		{"open brigade tag neutralised", "<brigade-message>", "&lt;brigade-message>"},
		{"close brigade tag neutralised", "</brigade-message>", "&lt;/brigade-message>"},
		{"uppercase tag neutralised", "<BRIGADE-MESSAGE>", "&lt;BRIGADE-MESSAGE>"},
		{"mixed case neutralised", "<Cross-Session-Message>", "&lt;Cross-Session-Message>"},
		{"space before name neutralised", "< brigade-message>", "&lt; brigade-message>"},
		{"tab before name neutralised", "<\tbrigade-message>", "&lt;\tbrigade-message>"},
		{"newline before name neutralised", "<\nsystem-reminder>", "&lt;\nsystem-reminder>"},
		{"close with space neutralised", "< / teammate-message>", "&lt; / teammate-message>"},
		{"channel neutralised", "<channel>", "&lt;channel>"},
		{"system-reminder neutralised", "<system-reminder>", "&lt;system-reminder>"},
		{"prefix over-match neutralised", "<brigade-messages>", "&lt;brigade-messages>"},
		{"unlisted tag survives", "<important_instructions>", "<important_instructions>"},
		{"bare angle survives", "1 < 2 and 3 > 2", "1 < 2 and 3 > 2"},
		{"non-family angle survives", "<div>hi</div>", "<div>hi</div>"},
		{"split close not matched", "</brigade-\nmessage>", "</brigade-\nmessage>"},
		{"pre-encoded not re-encoded", "&lt;brigade-message>", "&lt;brigade-message>"},
		// U+017F LATIN SMALL LETTER LONG S simple-folds to 's', so a
		// case-insensitive consumer (Go regexp (?i), and Brigade's own
		// frame parser) would read "ſystem-reminder" as the ASCII family
		// name. The matcher folds RUNE-wise for exactly this input; an
		// ASCII-only fold passes every other case in this table and
		// misses this one (mutation M2, P1-2 adversarial pass).
		{"unicode simple fold neutralised", "<ſystem-reminder>", "&lt;ſystem-reminder>"},
		{"unicode fold in close tag", "</ſYSTEM-REMINDER>", "&lt;/ſYSTEM-REMINDER>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := Sanitize(tc.in); got != tc.want {
				t.Errorf("Sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestSanitizeStripsControlCharactersExactly asserts on the exact code
// points removed, not on a rendered string that would look the same
// either way (a no-op sanitiser would pass a visual check).
func TestSanitizeStripsControlCharactersExactly(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		r    rune
		gone bool
	}{
		{"NUL", '\u0000', true},
		{"BEL", '\u0007', true},
		{"ESC", '\u001b', true},
		{"DEL", '\u007f', true},
		{"C1 control U+0085", '\u0085', true},
		{"C1 control U+009f", '\u009f', true},
		{"LRE bidi", '\u202a', true},
		{"RLO bidi", '\u202e', true},
		{"LRI bidi", '\u2066', true},
		{"PDI bidi", '\u2069', true},
		{"ZWJ", '\u200d', true},
		{"ZWNJ", '\u200c', true},
		{"carriage return", '\r', true},
		{"soft hyphen", '\u00ad', true},
		{"BOM/ZWNBSP", '\ufeff', true},
		{"newline kept", '\n', false},
		{"tab kept", '\t', false},
		{"ordinary space kept", ' ', false},
		{"letter kept", 'a', false},
		{"emoji kept", '😀', false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			in := "A" + string(tc.r) + "B"
			got := Sanitize(in)
			present := strings.ContainsRune(got, tc.r)
			if tc.gone && present {
				t.Errorf("Sanitize kept U+%04X, want stripped (got %q)", tc.r, got)
			}
			if !tc.gone && !present {
				t.Errorf("Sanitize stripped U+%04X, want kept (got %q)", tc.r, got)
			}
		})
	}
}

// TestSanitizeNormalisesToNFC pins the NFC step on exact code points: a
// decomposed sequence (e + combining acute) must become the single
// precomposed rune é (U+00E9).
func TestSanitizeNormalisesToNFC(t *testing.T) {
	t.Parallel()
	decomposed := "cafe\u0301" // "café" with a combining acute
	got := Sanitize(decomposed)
	want := "caf\u00e9"
	if got != want {
		t.Fatalf("Sanitize(%q) = %q (%v), want %q", decomposed, got, []rune(got), want)
	}
	if strings.ContainsRune(got, '\u0301') {
		t.Errorf("combining acute U+0301 survived NFC: %q", got)
	}
}

// TestSanitizeFormatCharCannotHideTag proves the strip-then-scan order:
// a zero-width joiner wedged inside "<brigade-message" is removed first,
// so the re-joined tag is still neutralised.
func TestSanitizeFormatCharCannotHideTag(t *testing.T) {
	t.Parallel()
	in := "<brigade\u200d-message team=\"ops\">"
	got := Sanitize(in)
	if strings.HasPrefix(got, "<") {
		t.Fatalf("format char hid the tag from the matcher: %q", got)
	}
	if !strings.HasPrefix(got, "&lt;") {
		t.Errorf("Sanitize(%q) = %q, want a neutralised opener", in, got)
	}
}

// TestSanitizeIsIdempotent: sanitising twice equals sanitising once, for
// every corpus item plus a handful of pointed inputs. Idempotence is
// what lets a value pass through the watcher and then a CLI rendering
// without degrading.
func TestSanitizeIsIdempotent(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"<brigade-message>", "&lt;brigade-message>", "cafe\u0301",
		"<\tsystem-reminder>", "a\u200db", "plain",
		// A format character BETWEEN a base letter and its combining
		// mark: the first NFC leaves the sequence decomposed (the ZWJ
		// blocks composition), stripping removes the ZWJ, and only the
		// SECOND NFC restores normal form. Dropping that second pass
		// (mutation M7) breaks idempotence exactly here.
		"e\u200d\u0301",
	}
	inputs = append(inputs, loadCorpusBodies(t)...)
	for _, in := range inputs {
		once := Sanitize(in)
		twice := Sanitize(once)
		if once != twice {
			t.Errorf("Sanitize not idempotent for %q:\n once=%q\ntwice=%q", in, once, twice)
		}
	}
}

func TestSanitizeBodyTruncatesOnBoundaryWithMarker(t *testing.T) {
	t.Parallel()
	// A body one byte over the cap must be cut on a rune boundary, and
	// the result INCLUDING the marker must be within the byte cap and
	// still valid UTF-8.
	over := strings.Repeat("a", MaxBodyBytes+100)
	got := SanitizeBody(over)
	if len(got) > MaxBodyBytes {
		t.Fatalf("SanitizeBody len = %d, want <= %d", len(got), MaxBodyBytes)
	}
	if !strings.HasSuffix(got, TruncationMarker) {
		t.Errorf("SanitizeBody did not append the truncation marker")
	}
	if !utf8.ValidString(got) {
		t.Errorf("SanitizeBody produced invalid UTF-8")
	}
	// A multi-byte rune straddling the cut point must not be split.
	multi := strings.Repeat("界", MaxBodyBytes) // 3 bytes each
	g2 := SanitizeBody(multi)
	if !utf8.ValidString(g2) {
		t.Errorf("SanitizeBody split a multi-byte rune: invalid UTF-8")
	}
	if len(g2) > MaxBodyBytes {
		t.Errorf("SanitizeBody len = %d, want <= %d", len(g2), MaxBodyBytes)
	}
}

// TestSanitizeRepairsInvalidUTF8 pins rule 1's repair step
// deterministically (the fuzz target checks the same property, but only
// when fuzzing happens to generate invalid bytes): invalid input comes
// back valid, with U+FFFD standing in for the bad bytes, and text around
// them intact.
func TestSanitizeRepairsInvalidUTF8(t *testing.T) {
	t.Parallel()
	got := Sanitize("a\xffb\x80c")
	if !utf8.ValidString(got) {
		t.Fatalf("Sanitize returned invalid UTF-8: %q", got)
	}
	if !strings.ContainsRune(got, '�') {
		t.Errorf("invalid bytes were not replaced with U+FFFD: %q", got)
	}
	for _, keep := range []string{"a", "b", "c"} {
		if !strings.Contains(got, keep) {
			t.Errorf("repair lost adjacent text %q: %q", keep, got)
		}
	}
}

// TestSanitizeBodyTruncationRunsLast pins rule 3's ordering from the
// observable side: neutralisation INFLATES ('<' becomes the four-byte
// "&lt;"), so on a tag-dense body only truncate-after-scan can keep the
// result within the byte cap. A pipeline that truncates first and scans
// second (mutation M16) inflates past the cap and fails validation here.
// The raw-tag scan below also proves the cut cannot resurrect a tag.
func TestSanitizeBodyTruncationRunsLast(t *testing.T) {
	t.Parallel()
	dense := strings.Repeat("<brigade-message>", MaxBodyBytes/len("<brigade-message>")+2)
	got := SanitizeBody(dense)
	if len(got) > MaxBodyBytes {
		t.Fatalf("SanitizeBody len = %d, want <= %d (truncation must run after tag neutralisation)", len(got), MaxBodyBytes)
	}
	if !strings.HasSuffix(got, TruncationMarker) {
		t.Errorf("over-cap tag-dense body was not marked truncated")
	}
	if rawForgeableTagRE.MatchString(got) {
		t.Errorf("a raw forgeable tag survived body sanitisation: %q", got[:64])
	}
	if !utf8.ValidString(got) {
		t.Errorf("invalid UTF-8 out of SanitizeBody")
	}
}

func TestSanitizeBodyLeavesUndersizedBodyUnmarked(t *testing.T) {
	t.Parallel()
	in := "a short body"
	if got := SanitizeBody(in); got != in {
		t.Errorf("SanitizeBody(%q) = %q, want unchanged", in, got)
	}
}

func TestSanitizeSummaryTruncatesInCodePoints(t *testing.T) {
	t.Parallel()
	// A summary of code points that are 3 bytes each: the cap is in code
	// points, so a body-style byte cap would cut far too early.
	over := strings.Repeat("界", MaxSummaryChars+50)
	got := SanitizeSummary(over)
	if n := utf8.RuneCountInString(got); n > MaxSummaryChars {
		t.Fatalf("SanitizeSummary runes = %d, want <= %d", n, MaxSummaryChars)
	}
	if !strings.HasSuffix(got, TruncationMarker) {
		t.Errorf("SanitizeSummary did not append the marker")
	}
}

// TestSanitizeSummaryAtCapIsUntouched is the other side of the same
// boundary, with multi-byte input on purpose: exactly MaxSummaryChars
// code points is 3x the cap in BYTES, so a truncator that pre-checks
// len(s) (mutation M9) truncates a legal summary that the code-point
// rule must leave alone.
func TestSanitizeSummaryAtCapIsUntouched(t *testing.T) {
	t.Parallel()
	exact := strings.Repeat("界", MaxSummaryChars)
	if got := SanitizeSummary(exact); got != exact {
		t.Fatalf("SanitizeSummary changed a summary of exactly %d code points:\n got %d runes, marker=%v",
			MaxSummaryChars, utf8.RuneCountInString(got), strings.HasSuffix(got, TruncationMarker))
	}
	exactName := strings.Repeat("界", MaxSessionNameCodepoints)
	if got := SanitizeName(exactName); got != exactName {
		t.Fatalf("SanitizeName changed a name of exactly %d code points", MaxSessionNameCodepoints)
	}
}

// TestSanitizeResultsPassValidation: a sanitised value at the cap must
// satisfy the wire validation, so a value that survived the watcher can
// be re-serialised without a spurious invalid_input.
func TestSanitizeResultsPassValidation(t *testing.T) {
	t.Parallel()
	body := SanitizeBody(strings.Repeat("界", MaxBodyBytes))
	if err := capBytes("body", body, MaxBodyBytes); err != nil {
		t.Errorf("sanitised body failed the byte cap: %v", err)
	}
	summary := SanitizeSummary(strings.Repeat("界", MaxSummaryChars+50))
	if err := capRunes("summary", summary, MaxSummaryChars); err != nil {
		t.Errorf("sanitised summary failed the code-point cap: %v", err)
	}
}

func TestSanitizeAttribute(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"drops double quote", `alice"bob`, "alicebob"},
		{"drops angle brackets", "a<b>c", "abc"},
		{"drops newline", "a\nb", "ab"},
		{"keeps tab as-is is dropped only newline", "a\tb", "a\tb"},
		{"neutralised tag then bracket drop leaves entity", "<brigade-message>", "&lt;brigade-message"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := SanitizeAttribute(tc.in); got != tc.want {
				t.Errorf("SanitizeAttribute(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestSanitizeAttributeCapsAt64CodePoints(t *testing.T) {
	t.Parallel()
	in := strings.Repeat("界", 100)
	got := SanitizeAttribute(in)
	if n := utf8.RuneCountInString(got); n != 64 {
		t.Fatalf("SanitizeAttribute runes = %d, want 64", n)
	}
	if strings.Contains(got, TruncationMarker) {
		t.Errorf("attribute must not carry the truncation marker")
	}
	if !utf8.ValidString(got) {
		t.Errorf("SanitizeAttribute produced invalid UTF-8")
	}
}

// TestSanitizeCorpusLeavesNoRawForgeableTag runs the sanitiser over every
// P0-1 corpus body and asserts, with an INDEPENDENT scanner, that no raw
// ASCII forgeable-tag opener survives. It also pins the two documented
// design outcomes: the homoglyph tag (item 22) and the unlisted tag
// (item 24) survive verbatim, because they are outside the five ASCII
// families on purpose.
func TestSanitizeCorpusLeavesNoRawForgeableTag(t *testing.T) {
	t.Parallel()
	for _, in := range loadCorpusBodies(t) {
		out := Sanitize(in)
		if rawForgeableTagRE.MatchString(out) {
			t.Errorf("sanitiser left a raw forgeable tag in output:\n%q", out)
		}
	}
}

func TestSanitizeBoundaryOutcomes(t *testing.T) {
	t.Parallel()
	// Item 22: homoglyph system-reminder must survive (Cyrillic look-
	// alikes are not the ASCII family), which is why the item is graded
	// "ignore" as a surviving-near-miss fixture.
	homoglyph := "<\u0455y\u0455t\u0435m-r\u0435m\u0456nd\u0435r>"
	if !strings.HasPrefix(Sanitize(homoglyph), "<") {
		t.Errorf("homoglyph tag was neutralised; item 22 expects it to survive")
	}
	// Item 24: an unlisted tag proves the five-family list is the actual
	// boundary — it must reach the model verbatim.
	unlisted := "<important_instructions>"
	if Sanitize(unlisted) != unlisted {
		t.Errorf("unlisted tag was altered; item 24 expects it verbatim")
	}
	// Item 23's ASCII wrapper tags, by contrast, MUST be neutralised.
	for _, tag := range []string{"<cross-session-message>", "<teammate-message>", "<channel name=\"ops\">"} {
		if strings.HasPrefix(Sanitize(tag), "<") {
			t.Errorf("ASCII wrapper tag %q was not neutralised", tag)
		}
	}
}
