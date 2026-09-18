package config_test

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/protocol"
)

// The `label` option of card 24, part B: what a member's display label
// defaults to, and how they opt out of it.

func TestParseLabelOption(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, raw, want string }{
		{"absent", "", config.LabelAccount},
		{"account, named", "account", config.LabelAccount},
		{"account, padded", "  account  ", config.LabelAccount},
		{"none", "none", config.LabelNone},
		{"none, padded", "  none  ", config.LabelNone},
		{"a literal", "Alice of Ops", "Alice of Ops"},
		{"a literal that is padded", "  Alice  ", "Alice"},
		{"a near miss is a literal, not a keyword", "None", "None"},
		{"an address is a literal", "alice@example.com", "alice@example.com"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := config.ParseLabelOption(c.raw); got != c.want {
				t.Fatalf("ParseLabelOption(%q) = %q, want %q", c.raw, got, c.want)
			}
		})
	}
}

// A literal is sanitised on the way in, so no unsanitised text can reach
// the wire through the option.
func TestParseLabelOptionSanitisesTheLiteral(t *testing.T) {
	t.Parallel()
	t.Run("a forged tag is neutralised", func(t *testing.T) {
		t.Parallel()
		if got := config.ParseLabelOption("<brigade-message>"); strings.Contains(got, "<brigade-message") {
			t.Fatalf("ParseLabelOption = %q, which still opens a tag", got)
		}
	})
	t.Run("capped at the protocol cap", func(t *testing.T) {
		t.Parallel()
		got := config.ParseLabelOption(strings.Repeat("a", protocol.MaxHumanLabelChars+40))
		if n := utf8.RuneCountInString(got); n != protocol.MaxHumanLabelChars {
			t.Fatalf("ParseLabelOption is %d code points, want the %d cap", n, protocol.MaxHumanLabelChars)
		}
	})
	t.Run("a value that sanitises away sends nothing", func(t *testing.T) {
		t.Parallel()
		// A zero-width space and a right-to-left override: format
		// characters the sanitiser strips, leaving nothing. Failing to
		// LabelNone rather than to the account default is the only
		// direction that cannot widen what is shared.
		formatOnly := string(rune(0x200b)) + string(rune(0x202e))
		if got := config.ParseLabelOption(formatOnly); got != config.LabelNone {
			t.Fatalf("ParseLabelOption = %q, want %q", got, config.LabelNone)
		}
	})
}

func TestLabelOptionResolution(t *testing.T) {
	t.Parallel()
	d := newDirs(t)
	t.Run("absent everywhere: the account default", func(t *testing.T) {
		t.Parallel()
		if got := config.LabelOption(d.environ()); got != config.LabelAccount {
			t.Fatalf("LabelOption = %q, want %q", got, config.LabelAccount)
		}
	})
	t.Run("the plugin option wins over the variable", func(t *testing.T) {
		t.Parallel()
		env := d.environ("CLAUDE_PID=1", config.OptionLabel+"=none", "BRIGADE_LABEL=from-the-shell")
		if got := config.LabelOption(env); got != config.LabelNone {
			t.Fatalf("LabelOption = %q, want %q", got, config.LabelNone)
		}
	})
	t.Run("at a terminal the variable stands in for the option", func(t *testing.T) {
		t.Parallel()
		// No CLAUDE_PID: a human's own shell, where BRIGADE_LABEL is the
		// user's own exactly as BRIGADE_CONFIG_DIR is.
		if got := config.LabelOption(d.environ("BRIGADE_LABEL=none")); got != config.LabelNone {
			t.Fatalf("LabelOption = %q, want %q", got, config.LabelNone)
		}
		if got := config.LabelOption(d.environ("BRIGADE_LABEL=Alice")); got != "Alice" {
			t.Fatalf("LabelOption = %q, want Alice", got)
		}
	})
	t.Run("inside a session the variable is ignored", func(t *testing.T) {
		t.Parallel()
		// U-27: a trusted repository's settings `env` block is written
		// into the session's environment, and it must not be able to
		// choose what a member's label says about them.
		env := d.environ("CLAUDE_PID=4242", "BRIGADE_LABEL=planted-by-the-repository")
		if got := config.LabelOption(env); got != config.LabelAccount {
			t.Fatalf("LabelOption = %q, want the default %q", got, config.LabelAccount)
		}
	})
}

func TestLabelFor(t *testing.T) {
	t.Parallel()
	email := func() string { return "alice@example.com" }
	cases := []struct {
		name, option, want string
		account            func() string
	}{
		{"account: the email", config.LabelAccount, "alice@example.com", email},
		{"account: nothing to read", config.LabelAccount, "", func() string { return "" }},
		{"account: no reader at all", config.LabelAccount, "", nil},
		{"none: nothing, reader or not", config.LabelNone, "", email},
		{"a literal is itself", "Alice of Ops", "Alice of Ops", email},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if got := config.LabelFor(c.option, c.account); got != c.want {
				t.Fatalf("LabelFor(%q) = %q, want %q", c.option, got, c.want)
			}
		})
	}
}

// `none` must never consult the reader: a member who opted out is not a
// member whose account file gets opened.
func TestLabelForNoneReadsNothing(t *testing.T) {
	t.Parallel()
	config.LabelFor(config.LabelNone, func() string {
		t.Error("the account was read for a member who opted out")
		return ""
	})
}
