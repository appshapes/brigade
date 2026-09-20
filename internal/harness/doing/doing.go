// Package doing holds what the harness knows about a session's doing line
// (card 25, plan .context/plans/session-doing.md): the one sentence a
// session's own model publishes as `session_description` so teammates can
// route by it. The package is neutral on purpose — the roster's display
// (commands), the writer (`brigade doing`), the watcher's read-back and the
// hook's resolved mode all need the same cap, the same cleaning and the
// same mode words, and `watch` does not import `commands`.
package doing

import (
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// MaxChars is the harness's cap on a doing line, in code points: the cap
// the writer refuses over (never truncates) and the cap the roster's
// continuation line cuts to with the truncation marker. It is
// deliberately below the wire cap, protocol.MaxDescriptionChars (256),
// which stays as it is — conformance requires at-cap values to pass, and
// a value Brigade's own writer sent can never exceed this one — so the
// table's cut only ever reaches text Brigade did not write (plan 5.1,
// 5.5; ruling 11).
const MaxChars = 160

// The resolved doing modes the SessionStart hook freezes into the by-pid
// map's `doing_mode` (plan 5.2, first match wins). The verb reads the word
// to decide whether to publish; the prompt hook (P16-5) reads it to decide
// whether a line may be printed. An absent member — a map written before
// the mode existed — publishes and prints nothing.
const (
	// ModeUnsupported: the adapter does not advertise `session.description`.
	// Nothing is published, `--clear` included.
	ModeUnsupported = "unsupported"
	// ModeOff: the `share_doing` option is false. Nothing is published;
	// `--clear` still works, so opting out can retract at once.
	ModeOff = "off"
	// ModeUnasked: an ask or deny entry in the settings Brigade can read
	// matches `brigade doing`, or a candidate settings file exists but
	// could not be read or parsed, or the Claude config directory is
	// unresolved. The verb publishes — Claude Code's own rule does the
	// asking or denying — and no line is ever printed.
	ModeUnasked = "unasked"
	// ModeAllowed: an allow entry exactly covers the verb, so a line may
	// print in the default and acceptEdits modes as well.
	ModeAllowed = "allowed"
	// ModeQuiet: no rule either way. The verb publishes; a line prints
	// only where nothing prompts anyway.
	ModeQuiet = "quiet"
)

// ValidMode reports whether mode is one of the five words.
func ValidMode(mode string) bool {
	switch mode {
	case ModeUnsupported, ModeOff, ModeUnasked, ModeAllowed, ModeQuiet:
		return true
	}
	return false
}

// The details.reason values Clean answers, and the verb's `invalid_input`
// refusals carry (plan 5.1).
const (
	ReasonEmpty        = "empty"
	ReasonTooLong      = "too_long"
	ReasonNotUTF8      = "not_utf8"
	ReasonSecretShaped = "secret_shaped"
	ReasonLocalPath    = "local_path"
)

// secretPrefixes are the credential formats the redactor's own pattern
// list does not carry, as bare literals: a Supabase personal access token,
// the three GitHub token families, an Anthropic API key, a Slack bot
// token. Containment is the test — a doing line has no business carrying
// any of them, and a false positive here costs one refusal the model can
// reword, where a leak costs a rotation.
var secretPrefixes = []string{"sbp_", "ghp_", "gho_", "github_pat_", "sk-ant-", "xoxb-"}

// localPath matches an absolute or `~/` path of two or more segments at
// the start of the text or after whitespace: `/Users/me/src`, `~/work`.
// A bare `/clear` or `/compact`, a repository-relative path
// (`internal/harness/doing.go`), `and/or` and a URL (whose first `/`
// follows a colon) all pass, on purpose: the rule refuses what would name
// this machine's layout, not every slash (plan 5.1).
var localPath = regexp.MustCompile(`(^|\s)(~|/)[^\s/]*/[^\s/]+`)

// Clean turns the raw text of a doing line into the sentence the harness
// may publish, or answers the details.reason it is refused for. The order
// is the plan's (5.1), and it is load-bearing: the text is sanitised
// BEFORE it is counted, because neutralisation lengthens text — an
// input under the cap that grows past it is refused, never truncated
// (protocol.SanitizeDescription's marker is for display, not for a line
// this harness sends). The steps: valid UTF-8; protocol.Sanitize (rules
// 1-2, no cap); folded onto one line by strings.Fields, which also folds
// U+2028/2029; refuse empty; count code points against MaxChars and
// refuse over; the credential rule — log.SecretShaped's prefix formats,
// the literals in secretPrefixes, and each of secrets (the caller passes
// the exact messaging token when it has one) — refuse; the path rule —
// home when it is longer than one character, then localPath — refuse.
// The text is the model's own words and is never logged by any caller.
func Clean(raw, home string, secrets ...string) (text, reason string) {
	if !utf8.ValidString(raw) {
		return "", ReasonNotUTF8
	}
	text = strings.Join(strings.Fields(protocol.Sanitize(raw)), " ")
	if text == "" {
		return "", ReasonEmpty
	}
	if utf8.RuneCountInString(text) > MaxChars {
		return "", ReasonTooLong
	}
	if log.SecretShaped(text) {
		return "", ReasonSecretShaped
	}
	for _, p := range secretPrefixes {
		if strings.Contains(text, p) {
			return "", ReasonSecretShaped
		}
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(text, s) {
			return "", ReasonSecretShaped
		}
	}
	if len(home) > 1 && strings.Contains(text, home) {
		return "", ReasonLocalPath
	}
	if localPath.MatchString(text) {
		return "", ReasonLocalPath
	}
	return text, ""
}
