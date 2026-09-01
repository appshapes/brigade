package log

import (
	"cmp"
	"regexp"
	"slices"
	"strings"
	"sync"

	"github.com/appshapes/brigade/internal/protocol"
)

// Redacted is the marker every redaction layer writes in place of the
// text it removed. Lowercase to match protocol.JoinSecret's own String
// and LogValue redaction.
const Redacted = "[redacted]"

// Suppressed is the marker written in place of a non-scalar (non-error)
// slog.Any value. ReplaceAttr cannot redact inside struct, map or slice
// values, so they are never printed at all (the scalar-only policy of
// plan 7.3; forbidigo's slog.Any ban is the static half of the same
// rule).
const Suppressed = "[suppressed non-scalar value: log scalars, or adapterkit/log helpers (plan 7.3)]"

// secretKeyWords is the key list: an attribute key that contains any of
// these (case-insensitive, '-' folded to '_') is a secret whatever its
// value. Containment makes this a superset of plan 1315's literal list —
// access_token, refresh_token and messaging_token are caught by "token",
// join_secret and client_secret by "secret" — and over-redaction of a
// diagnostic key costs legibility, never safety.
var secretKeyWords = []string{
	"token",
	"secret",
	"authorization",
	"apikey",
	"api_key",
	"password",
}

// isSecretKey reports whether an attribute key names a secret.
func isSecretKey(key string) bool {
	k := strings.ReplaceAll(strings.ToLower(key), "-", "_")
	for _, w := range secretKeyWords {
		if strings.Contains(k, w) {
			return true
		}
	}
	return false
}

// prefixPatterns are the secret formats that start with a fixed literal.
// They are matched by scanning for every occurrence of the literal and
// applying the ^-anchored pattern there, NOT with FindAllStringIndex:
// leftmost non-overlapping matching would let junk that happens to parse
// as the same format directly before a real token consume the token's
// prefix and walk away, leaving its tail exposed (e.g. "eyJXX.YY." glued
// in front of a JWT makes the leftmost JWT match end at the header,
// leaving payload and signature outside every match). Anchoring at each
// literal occurrence and merging spans closes that class entirely.
var prefixPatterns = []struct {
	literal string
	re      *regexp.Regexp // must be anchored with ^literal
}{
	// A JWT: base64url header starting eyJ ({"…), payload, and a
	// signature that may be empty (alg "none"). No \b before eyJ: a
	// token glued to other text still redacts.
	{"eyJ", regexp.MustCompile(`^eyJ[A-Za-z0-9_=-]{2,}\.[A-Za-z0-9_=-]+\.[A-Za-z0-9_=-]*`)},
	// The join secret (D5). Its components can contain any non-space
	// character (protocol.ParseJoinSecret rejects only whitespace,
	// control and format characters), so the span runs to the next
	// whitespace: over-redacting a trailing "&x=1" is safe, stopping at
	// a '"' inside the secret would not be.
	{protocol.JoinSecretPrefix, regexp.MustCompile(`^` + regexp.QuoteMeta(protocol.JoinSecretPrefix) + `\S+`)},
	// Supabase secret API keys.
	{"sb_secret_", regexp.MustCompile(`^sb_secret_\S+`)},
}

// bearerPattern redacts the credential of an RFC 6750 Authorization
// header, opaque tokens included. The token span is \S+ rather than the
// b64token alphabet so a nonconforming token cannot half-survive. The
// scheme word is only recognised at a word boundary; text glued directly
// onto "Bearer" forms a different word, which is not a Bearer header.
var bearerPattern = regexp.MustCompile(`(?i)\bbearer[ \t]+\S+`)

// A Redactor holds the exact secret values registered at run time and
// applies every value-level redaction layer. The zero value and nil are
// usable (patterns only); NewRedactor adds exact tokens. One Redactor is
// shared between a logger and the code that learns secrets later (Add is
// safe under concurrent logging).
type Redactor struct {
	mu    sync.RWMutex
	exact []string
}

// NewRedactor returns a Redactor with the given exact secret values
// registered. Empty strings are ignored.
func NewRedactor(secrets ...string) *Redactor {
	r := &Redactor{}
	r.Add(secrets...)
	return r
}

// Add registers exact secret values to be replaced wherever they appear,
// including in the middle of longer strings. Empty strings and
// duplicates are ignored. Safe to call while the logger is in use.
func (r *Redactor) Add(secrets ...string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range secrets {
		if s == "" || slices.Contains(r.exact, s) {
			continue
		}
		r.exact = append(r.exact, s)
	}
}

// Redact replaces every secret found in s — format patterns and
// registered exact tokens — with the Redacted marker. All matches are
// collected as spans on the original string and merged, so overlapping
// matches cannot split a secret and leave a fragment. Nil-safe.
func (r *Redactor) Redact(s string) string {
	if s == "" {
		return s
	}
	var spans [][2]int
	for _, p := range prefixPatterns {
		for from := 0; ; {
			i := strings.Index(s[from:], p.literal)
			if i < 0 {
				break
			}
			at := from + i
			if m := p.re.FindStringIndex(s[at:]); m != nil {
				spans = append(spans, [2]int{at + m[0], at + m[1]})
			}
			from = at + 1
		}
	}
	for _, m := range bearerPattern.FindAllStringIndex(s, -1) {
		spans = append(spans, [2]int{m[0], m[1]})
	}
	if r != nil {
		r.mu.RLock()
		for _, tok := range r.exact {
			for from := 0; ; {
				i := strings.Index(s[from:], tok)
				if i < 0 {
					break
				}
				at := from + i
				spans = append(spans, [2]int{at, at + len(tok)})
				from = at + 1
			}
		}
		r.mu.RUnlock()
	}
	if len(spans) == 0 {
		return s
	}
	slices.SortFunc(spans, func(a, b [2]int) int {
		if c := cmp.Compare(a[0], b[0]); c != 0 {
			return c
		}
		return cmp.Compare(b[1], a[1]) // longer span first at equal start
	})
	var b strings.Builder
	b.Grow(len(s))
	pos := 0
	start, end := spans[0][0], spans[0][1]
	for _, sp := range spans[1:] {
		if sp[0] <= end {
			if sp[1] > end {
				end = sp[1]
			}
			continue
		}
		b.WriteString(s[pos:start])
		b.WriteString(Redacted)
		pos = end
		start, end = sp[0], sp[1]
	}
	b.WriteString(s[pos:start])
	b.WriteString(Redacted)
	b.WriteString(s[end:])
	return b.String()
}
