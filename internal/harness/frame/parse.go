package frame

import (
	"errors"
	"strings"
)

// This file is Brigade's own frame parser (plan 6.7 rule 2, U-03): the
// consumer that proves a hostile body cannot close or forge a frame. It
// reads a frame the way a consuming parser would — the FIRST tag line's
// attributes, the FIRST separator line after it, the LAST close tag —
// so a forged close or opener that the sanitiser has neutralised to
// `&lt;…` inside the body stays where it is: inert text below the
// separator, attributed to the true sender.

// Errors Parse returns. Both are sentinels: wrap-compare with errors.Is.
var (
	// ErrNoFrame means the text carries no `<brigade-message` tag.
	ErrNoFrame = errors.New("frame: no <brigade-message> tag")
	// ErrMalformed means a tag was found but the text around it is not
	// the shape Build produces (unterminated tag line, no close tag, no
	// separator, no summary line, an attribute that is not name="value").
	ErrMalformed = errors.New("frame: malformed frame")
)

// A Parsed frame. The typed fields mirror TagAttributes; Attributes
// holds every attribute the tag line carried, typed or not.
type Parsed struct {
	// Attributes is every name="value" pair of the tag line.
	Attributes map[string]string
	// The eight attributes of TagAttributes, "" when absent.
	Team             string
	MessageID        string
	ReplyToSessionID string
	FromPrincipal    string
	FromName         string
	FromLabel        string
	Hops             string
	SentAt           string
	// Preamble is Brigade's text between the tag line and the separator.
	Preamble string
	// Summary is the text after SummaryPrefix on the first line below
	// the separator.
	Summary string
	// Body is everything between the summary line and the close tag,
	// exactly as it appears (the sanitised body Build placed there).
	Body string
	// Frame is the frame text itself, from OpenTag to CloseTag inclusive,
	// byte for byte — for a wrapped frame, the inner frame.
	Frame string
	// Wrapped reports whether a WrapperOpen tag preceded the frame;
	// WrapperFromName is that tag's from-name attribute.
	Wrapped         bool
	WrapperFromName string
}

// Parse reads one frame out of text. It finds the FIRST `<brigade-message`
// tag, reads its attributes up to the tag line's `>`, takes the LAST
// `</brigade-message>` line as the close tag, splits the inside at the
// first `----` line after the tag, and returns the attributes, Brigade's
// preamble, the summary line (SummaryPrefix removed) and the body. Text
// before the tag (a variant-C wrapper) and after the close tag (the
// wrapper's end) is ignored, except that a wrapper's from-name is reported.
//
// A frame built by Build from a hostile body parses as ONE message with
// the true attributes: the sanitiser has neutralised every forged
// `<brigade-message`, `</brigade-message>` and `<cross-session-message`
// in the body to `&lt;…`, so none of them is a tag here.
func Parse(text string) (Parsed, error) {
	start := findOpenTag(text)
	if start < 0 {
		return Parsed{}, ErrNoFrame
	}
	tagBody := text[start+len(OpenTag):]
	tagEnd := strings.IndexByte(tagBody, '>')
	if tagEnd < 0 {
		return Parsed{}, malformed("tag line has no '>'")
	}
	attributes, err := parseAttributes(tagBody[:tagEnd])
	if err != nil {
		return Parsed{}, err
	}
	// rest begins right after the tag line's '>' and must open with the
	// newline that ends the tag line.
	rest := tagBody[tagEnd+1:]
	if !strings.HasPrefix(rest, "\n") {
		return Parsed{}, malformed("tag line is not followed by a newline")
	}
	closeAt := strings.LastIndex(rest, "\n"+CloseTag)
	if closeAt < 0 {
		return Parsed{}, malformed("no close tag")
	}
	if closeAt == 0 {
		return Parsed{}, malformed("nothing between the tag line and the close tag")
	}
	// The close tag must stand alone on its line: nothing but a newline
	// or the end of text may follow it.
	afterClose := rest[closeAt+1+len(CloseTag):]
	if afterClose != "" && !strings.HasPrefix(afterClose, "\n") {
		return Parsed{}, malformed("close tag is not alone on its line")
	}
	inner := rest[1:closeAt]
	preamble, below, ok := splitAtSeparator(inner)
	if !ok {
		return Parsed{}, malformed("no separator line")
	}
	summaryLine, body, ok := strings.Cut(below, "\n")
	if !ok {
		return Parsed{}, malformed("no body line after the summary")
	}
	summary, ok := strings.CutPrefix(summaryLine, SummaryPrefix)
	if !ok {
		return Parsed{}, malformed("first line below the separator is not the sender summary")
	}

	p := Parsed{
		Attributes:       attributes,
		Team:             attributes["team"],
		MessageID:        attributes["message-id"],
		ReplyToSessionID: attributes["reply-to-session-id"],
		FromPrincipal:    attributes["from-principal"],
		FromName:         attributes["from-name"],
		FromLabel:        attributes["from-label"],
		Hops:             attributes["hops"],
		SentAt:           attributes["sent-at"],
		Preamble:         preamble,
		Summary:          summary,
		Body:             body,
		Frame:            text[start : start+len(OpenTag)+tagEnd+1+closeAt+1+len(CloseTag)],
	}
	p.Wrapped, p.WrapperFromName = wrapperName(text[:start])
	return p, nil
}

// findOpenTag returns the index of the first OpenTag that is a tag (the
// name is followed by whitespace or '>'), or -1.
func findOpenTag(text string) int {
	from := 0
	for {
		i := strings.Index(text[from:], OpenTag)
		if i < 0 {
			return -1
		}
		i += from
		after := text[i+len(OpenTag):]
		if after == "" || after[0] == ' ' || after[0] == '>' || after[0] == '\n' || after[0] == '\t' {
			return i
		}
		from = i + len(OpenTag)
	}
}

// splitAtSeparator splits inner at the first line that is exactly
// Separator. The preamble is the text before that line (without the
// newline that ends it); below is the text after it.
func splitAtSeparator(inner string) (preamble, below string, ok bool) {
	if rest, found := strings.CutPrefix(inner, Separator+"\n"); found {
		return "", rest, true
	}
	i := strings.Index(inner, "\n"+Separator+"\n")
	if i < 0 {
		return "", "", false
	}
	return inner[:i], inner[i+1+len(Separator)+1:], true
}

// parseAttributes reads ` name="value"` pairs. Names are
// [A-Za-z][A-Za-z0-9-]*; values end at the next '"' (a sanitised value
// carries none). Anything else, and a repeated name, is ErrMalformed.
func parseAttributes(s string) (map[string]string, error) {
	attributes := map[string]string{}
	for {
		s = strings.TrimLeft(s, " \t")
		if s == "" {
			return attributes, nil
		}
		n := attributeNameLen(s)
		if n == 0 {
			return nil, malformed("attribute name expected")
		}
		name := s[:n]
		s = s[n:]
		if !strings.HasPrefix(s, `="`) {
			return nil, malformed("attribute value expected")
		}
		s = s[2:]
		end := strings.IndexByte(s, '"')
		if end < 0 {
			return nil, malformed("unterminated attribute value")
		}
		if _, dup := attributes[name]; dup {
			return nil, malformed("repeated attribute")
		}
		attributes[name] = s[:end]
		s = s[end+1:]
		if s != "" && s[0] != ' ' && s[0] != '\t' {
			return nil, malformed("attributes must be separated by whitespace")
		}
	}
}

// attributeNameLen returns how many leading bytes of s form an attribute
// name, 0 when none does.
func attributeNameLen(s string) int {
	n := 0
	for n < len(s) {
		c := s[n]
		isLetter := (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		isDigit := c >= '0' && c <= '9'
		if isLetter || (n > 0 && (isDigit || c == '-')) {
			n++
			continue
		}
		break
	}
	return n
}

// wrapperName reports whether before (the text preceding the frame's
// tag) carries a WrapperOpen tag and, if so, its from-name.
func wrapperName(before string) (wrapped bool, fromName string) {
	i := strings.Index(before, WrapperOpen)
	if i < 0 {
		return false, ""
	}
	tag := before[i+len(WrapperOpen):]
	end := strings.IndexByte(tag, '>')
	if end < 0 {
		return true, ""
	}
	attributes, err := parseAttributes(tag[:end])
	if err != nil {
		return true, ""
	}
	return true, attributes["from-name"]
}

// malformed wraps ErrMalformed with a fixed, value-free detail.
func malformed(detail string) error {
	return &malformedError{detail: detail}
}

type malformedError struct{ detail string }

func (e *malformedError) Error() string { return ErrMalformed.Error() + ": " + e.detail }
func (e *malformedError) Is(target error) bool {
	return target == ErrMalformed //nolint:errorlint // sentinel identity is the point of Is
}
