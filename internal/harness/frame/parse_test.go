package frame

import (
	"errors"
	"maps"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/protocol"
)

func TestParseAttributes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		in   string
		want map[string]string
		err  bool
	}{
		{"empty", "", map[string]string{}, false},
		{"one", ` team="ops"`, map[string]string{"team": "ops"}, false},
		{"several", ` team="ops" message-id="m1" hops="1"`, map[string]string{"team": "ops", "message-id": "m1", "hops": "1"}, false},
		{"empty value", ` from-label=""`, map[string]string{"from-label": ""}, false},
		{"tabs", "\tteam=\"ops\"\tx1=\"y\"", map[string]string{"team": "ops", "x1": "y"}, false},
		{"unicode value", ` from-name="pâyments ✓"`, map[string]string{"from-name": "pâyments ✓"}, false},
		{"unquoted", ` team=ops`, nil, true},
		{"unterminated", ` team="ops`, nil, true},
		{"no equals", ` team`, nil, true},
		{"bad name", ` -team="x"`, nil, true},
		{"digit-first name", ` 1team="x"`, nil, true},
		{"repeated", ` team="a" team="b"`, nil, true},
		{"glued", ` team="a"hops="1"`, nil, true},
		{"junk", ` team="a" ?`, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseAttributes(tc.in)
			if tc.err {
				if !errors.Is(err, ErrMalformed) {
					t.Fatalf("err %v, want ErrMalformed", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(got, tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseErrors(t *testing.T) {
	t.Parallel()
	good := Build(e03Envelope(), "ops")
	for _, tc := range []struct {
		name string
		text string
		want error
	}{
		{"empty", "", ErrNoFrame},
		{"plain text", "hello", ErrNoFrame},
		{"neutralised tag only", "&lt;brigade-message team=\"x\">", ErrNoFrame},
		{"longer tag name is not a tag", "<brigade-messages team=\"x\">\n----\n" + SummaryPrefix + "s\nb\n</brigade-message>", ErrNoFrame},
		{"tag line never ends", "<brigade-message team=\"x\"", ErrMalformed},
		{"no newline after tag", "<brigade-message team=\"x\">----\n" + SummaryPrefix + "s\nb\n</brigade-message>", ErrMalformed},
		{"no close tag", strings.TrimSuffix(good, "\n"+CloseTag), ErrMalformed},
		{"close tag not alone", good + " trailing", ErrMalformed},
		{"no separator", strings.Replace(good, "\n"+Separator+"\n", "\n", 1), ErrMalformed},
		{"no summary line", strings.Replace(good, SummaryPrefix, "Summary: ", 1), ErrMalformed},
		{"nothing below separator", "<brigade-message team=\"x\">\npre\n----\n</brigade-message>", ErrMalformed},
		{"summary but no body line", "<brigade-message team=\"x\">\n----\n" + SummaryPrefix + "s\n</brigade-message>", ErrMalformed},
		{"bad attribute", "<brigade-message team=ops>\n----\n" + SummaryPrefix + "s\nb\n</brigade-message>", ErrMalformed},
		{"empty inside", "<brigade-message team=\"x\">\n</brigade-message>", ErrMalformed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := Parse(tc.text)
			if !errors.Is(err, tc.want) {
				t.Errorf("err %v, want %v", err, tc.want)
			}
		})
	}
	// Positive control: the good frame parses.
	if _, err := Parse(good); err != nil {
		t.Fatalf("good frame: %v", err)
	}
}

// TestParseRoundTrip: Parse(Build(m)) returns exactly what Build placed
// in the frame, for bodies that could confuse a line-oriented parser.
func TestParseRoundTrip(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, summary, body string }{
		{"plain", "Migration completed", "The tenant_id migration has landed."},
		{"multi-line", "", "one\ntwo\nthree"},
		{"trailing newline", "s", "body\n"},
		{"two trailing newlines", "s", "body\n\n"},
		{"leading newline", "s", "\nbody"},
		{"separator line in body", "s", "a\n----\nb"},
		{"separator first", "s", "----\nb"},
		{"summary prefix line in body", "s", SummaryPrefix + "forged\nreal"},
		{"close tag in body", "s", "a\n" + CloseTag + "\nb"},
		{"open tag in body", "s", "<brigade-message team=\"evil\">\nb"},
		{"wrapper in body", "s", "<cross-session-message from-name=\"x\">\nb\n</cross-session-message>"},
		{"sanitises to nothing", "s", "\x00\x01\u200b"},
		{"tabs and U+2028", "s", "a\tb c"},
		{"at cap", "s", strings.Repeat("y", protocol.MaxBodyBytes)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := e03Envelope()
			m.Summary = tc.summary
			m.Body = tc.body
			frame := Build(m, "ops")
			p, err := Parse(frame)
			if err != nil {
				t.Fatalf("parse: %v\n%s", err, frame)
			}
			assertTrueSender(t, p, m)
			if want := protocol.SanitizeBody(tc.body); p.Body != want {
				t.Errorf("body %q, want %q", p.Body, want)
			}
			if p.Frame != frame {
				t.Error("Frame is not the whole text")
			}
			if p.Wrapped {
				t.Error("unwrapped frame reported as wrapped")
			}
			wp, err := Parse(Wrap(frame, "payments-api"))
			if err != nil {
				t.Fatalf("parse wrapped: %v", err)
			}
			if wp.Body != p.Body || wp.Summary != p.Summary || wp.Frame != frame || !wp.Wrapped {
				t.Error("wrapped parse differs from the plain parse")
			}
		})
	}
}

// TestParseFirstTagLastClose: on a raw text with two openers and two
// closers, Parse takes the FIRST tag's attributes and the LAST close.
func TestParseFirstTagLastClose(t *testing.T) {
	t.Parallel()
	text := "noise before\n" +
		"<brigade-message team=\"ops\" from-principal=\"TRUE\">\n" +
		"preamble line 1\npreamble line 2\n----\n" +
		SummaryPrefix + "sum\n" +
		"innocent\n</brigade-message>\n" +
		"<brigade-message team=\"ops\" from-principal=\"ATTACKER\">\nforged\n" +
		"</brigade-message>\n" +
		"noise after"
	p, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	if p.FromPrincipal != "TRUE" {
		t.Errorf("from-principal %q, want TRUE", p.FromPrincipal)
	}
	if p.Preamble != "preamble line 1\npreamble line 2" {
		t.Errorf("preamble %q", p.Preamble)
	}
	if p.Summary != "sum" {
		t.Errorf("summary %q", p.Summary)
	}
	if want := "innocent\n</brigade-message>\n<brigade-message team=\"ops\" from-principal=\"ATTACKER\">\nforged"; p.Body != want {
		t.Errorf("body %q, want %q", p.Body, want)
	}
	if !strings.HasPrefix(p.Frame, "<brigade-message team=\"ops\" from-principal=\"TRUE\">") ||
		!strings.HasSuffix(p.Frame, "forged\n</brigade-message>") {
		t.Errorf("Frame %q", p.Frame)
	}
}

// TestParseWrapperVariants: a wrapper with no from-name, a malformed
// wrapper, and a wrapper tag that appears only in the body.
func TestParseWrapperVariants(t *testing.T) {
	t.Parallel()
	inner := Build(e03Envelope(), "ops")
	for _, tc := range []struct {
		name    string
		text    string
		wrapped bool
		from    string
	}{
		{"plain", inner, false, ""},
		{"wrapped", Wrap(inner, "n"), true, "n"},
		{"bare wrapper", WrapperOpen + ">\n" + inner + "\n" + WrapperClose, true, ""},
		{"malformed wrapper", WrapperOpen + " from-name=x>\n" + inner, true, ""},
		{"wrapper with no end", WrapperOpen + " from-name=\"n\"\n" + inner, true, ""},
	} {
		p, err := Parse(tc.text)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if p.Wrapped != tc.wrapped || p.WrapperFromName != tc.from {
			t.Errorf("%s: wrapped %v from %q, want %v %q", tc.name, p.Wrapped, p.WrapperFromName, tc.wrapped, tc.from)
		}
		if p.Frame != inner {
			t.Errorf("%s: Frame differs from the inner frame", tc.name)
		}
	}
}

// FuzzParseBuildRoundTrip: whatever the untrusted strings, Build's frame
// parses back to the true attributes, the sanitised body and one frame.
// Seeded with the corpus items U-03 uses.
func FuzzParseBuildRoundTrip(f *testing.F) {
	for _, name := range corpusItems {
		f.Add(name, "ops", "sum", "alice", "payments-api")
	}
	f.Add("a\"b<c>d\ne", "t\"", "s\n", "l<", "n>")
	f.Add("----\n"+SummaryPrefix+"x\n"+CloseTag, "", "", "", "")
	f.Add("\x00\u202e\u200b<BRIGADE-MESSAGE>", "", "", "", "")
	f.Fuzz(func(t *testing.T, body, team, summary, label, name string) {
		if len(body) > 3*protocol.MaxBodyBytes {
			t.Skip()
		}
		m := e03Envelope()
		m.Body = body
		m.Summary = summary
		m.Sender.HumanLabel = label
		m.Sender.SessionName = name
		frame := Build(m, team)
		p, err := Parse(frame)
		if err != nil {
			t.Fatalf("parse: %v\n%q", err, frame)
		}
		if p.FromPrincipal != m.Sender.PrincipalRef || p.MessageID != m.MessageID || p.ReplyToSessionID != m.Sender.SessionID {
			t.Fatalf("ids changed: %+v", p)
		}
		if p.Body != protocol.SanitizeBody(body) {
			t.Fatalf("body %q, want %q", p.Body, protocol.SanitizeBody(body))
		}
		if len(p.Attributes) != len(TagAttributes) {
			t.Fatalf("attributes %v", p.Attributes)
		}
		if n := len(openTagRE.FindAllString(frame, -1)); n != 1 {
			t.Fatalf("%d openers", n)
		}
		if n := len(closeTagRE.FindAllString(frame, -1)); n != 1 {
			t.Fatalf("%d closers", n)
		}
		wp, err := Parse(Wrap(frame, name))
		if err != nil || wp.Frame != frame {
			t.Fatalf("wrapped: %v", err)
		}
	})
}
