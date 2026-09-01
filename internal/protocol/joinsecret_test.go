package protocol

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// Format helpers kept out of the test bodies so the %v/%s/%#v verbs are
// exercised through the exact fmt paths a caller would use. fmt.Sprint*
// is allowed by forbidigo (only fmt.Print/Printf/Println are not).
func sprintV(v any) string     { return fmt.Sprintf("%v", v) }
func sprintS(v any) string     { return fmt.Sprintf("%s", v) }
func sprintHashV(v any) string { return fmt.Sprintf("%#v", v) }

const (
	sampleTeamRef = "3c1a0000-1111-2222-3333-444455556666"
	sampleTail    = "0123456789abcdef0123456789abcdef"
	sampleSecret  = JoinSecretPrefix + sampleTeamRef + "." + sampleTail
)

func TestParseJoinSecretValid(t *testing.T) {
	t.Parallel()
	got, err := ParseJoinSecret(sampleSecret)
	if err != nil {
		t.Fatalf("ParseJoinSecret(valid) error: %v", err)
	}
	if got.TeamRef() != sampleTeamRef {
		t.Errorf("TeamRef = %q, want %q", got.TeamRef(), sampleTeamRef)
	}
	// Secret() returns the FULL brg1.<team_ref>.<tail> string for the
	// team-join backend call.
	if got.Secret() != sampleSecret {
		t.Errorf("Secret() = %q, want the full secret", got.Secret())
	}
}

// TestParseJoinSecretTeamRefWithDots: the team_ref may contain '.' (4.8
// does not freeze its shape), so the split is on the LAST dot.
func TestParseJoinSecretTeamRefWithDots(t *testing.T) {
	t.Parallel()
	s := JoinSecretPrefix + "region.eu.team-7." + sampleTail
	got, err := ParseJoinSecret(s)
	if err != nil {
		t.Fatalf("error: %v", err)
	}
	if got.TeamRef() != "region.eu.team-7" {
		t.Errorf("TeamRef = %q, want region.eu.team-7", got.TeamRef())
	}
}

func TestParseJoinSecretMalformed(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"no prefix", "team.secret"},
		{"wrong prefix case", "BRG1." + sampleTeamRef + "." + sampleTail},
		{"prefix only", JoinSecretPrefix},
		{"no dot after prefix", JoinSecretPrefix + "teamonlysecretnodot"},
		{"empty team_ref", JoinSecretPrefix + "." + sampleTail},
		{"empty secret half", JoinSecretPrefix + sampleTeamRef + "."},
		{"whitespace in tail", JoinSecretPrefix + sampleTeamRef + "." + "abc def"},
		{"newline in tail", JoinSecretPrefix + sampleTeamRef + "." + "abc\ndef"},
		{"control char in team_ref", JoinSecretPrefix + "team\x07x." + sampleTail},
		{"bidi in tail", JoinSecretPrefix + sampleTeamRef + "." + "abc\u202edef"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := ParseJoinSecret(tc.in)
			var perr *Error
			if !errors.As(err, &perr) {
				t.Fatalf("ParseJoinSecret(%q) err = %v, want *Error", tc.in, err)
			}
			if perr.Code != CodeInvalidInput {
				t.Errorf("code = %q, want invalid_input", perr.Code)
			}
			if perr.Code.Exit() != 3 {
				t.Errorf("exit = %d, want 3", perr.Code.Exit())
			}
		})
	}
}

// TestParseJoinSecretErrorNeverCarriesSecret: a malformed secret whose
// tail is a recognisable token must not appear in the error message or
// any details value — 4.5.14, no secret in any error line.
func TestParseJoinSecretErrorNeverCarriesSecret(t *testing.T) {
	t.Parallel()
	marker := "SUPERSECRETTAILVALUE"
	// Malformed because of the trailing space, but it carries the marker.
	bad := JoinSecretPrefix + sampleTeamRef + "." + marker + " x"
	_, err := ParseJoinSecret(bad)
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), marker) {
		t.Errorf("error message leaked the secret: %q", err.Error())
	}
	var perr *Error
	if errors.As(err, &perr) {
		for k, v := range perr.Details {
			if strings.Contains(v, marker) {
				t.Errorf("details[%q] leaked the secret: %q", k, v)
			}
		}
	}
}

// TestJoinSecretRedactsInEveryRendering: String, GoString, LogValue and
// JSON marshalling must all omit the secret tail while the explicit
// Secret() still returns it. The positive control is built in: the same
// test proves Secret() DOES return the full value, so a type that simply
// dropped the secret everywhere would fail the first assertion rather
// than pass the redaction ones vacuously.
func TestJoinSecretRedactsInEveryRendering(t *testing.T) {
	t.Parallel()
	js, err := ParseJoinSecret(sampleSecret)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// Positive control: the secret really is retained and retrievable.
	if js.Secret() != sampleSecret {
		t.Fatalf("Secret() did not return the full secret")
	}

	renderings := map[string]string{
		"String":   js.String(),
		"GoString": js.GoString(),
	}
	for name, r := range renderings {
		if strings.Contains(r, sampleTail) {
			t.Errorf("%s leaked the secret tail: %q", name, r)
		}
		if !strings.Contains(r, sampleTeamRef) {
			t.Errorf("%s dropped the non-secret team_ref: %q", name, r)
		}
		if !strings.Contains(r, "redacted") {
			t.Errorf("%s is not marked redacted: %q", name, r)
		}
	}

	// slog.LogValuer: a JoinSecret logged through slog renders redacted.
	lv := js.LogValue()
	if lv.Kind() != slog.KindString || strings.Contains(lv.String(), sampleTail) {
		t.Errorf("LogValue leaked the secret: %q", lv.String())
	}

	// JSON marshalling of the value directly.
	b, err := json.Marshal(js)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), sampleTail) {
		t.Errorf("MarshalJSON leaked the secret tail: %s", b)
	}

	// JSON marshalling when EMBEDDED in a struct, the realistic leak path
	// (a log record or a result object that happens to hold one).
	type holder struct {
		Secret JoinSecret `json:"secret"`
		Note   string     `json:"note"`
	}
	hb, err := json.Marshal(holder{Secret: js, Note: "n"})
	if err != nil {
		t.Fatalf("marshal holder: %v", err)
	}
	if strings.Contains(string(hb), sampleTail) {
		t.Errorf("embedded MarshalJSON leaked the secret tail: %s", hb)
	}
}

// TestJoinSecretDefaultFormatVerbDoesNotLeak: a stray %v or %s (the
// commonest accidental leak) renders the redacted String, not the raw
// struct with its unexported secret field. This is the reason String
// and GoString exist.
func TestJoinSecretDefaultFormatVerbDoesNotLeak(t *testing.T) {
	t.Parallel()
	js, err := ParseJoinSecret(sampleSecret)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	for _, formatted := range []string{
		strings.TrimSpace(sprintV(js)),
		strings.TrimSpace(sprintS(js)),
		strings.TrimSpace(sprintHashV(js)),
	} {
		if strings.Contains(formatted, sampleTail) {
			t.Errorf("format verb leaked the secret: %q", formatted)
		}
	}
}
