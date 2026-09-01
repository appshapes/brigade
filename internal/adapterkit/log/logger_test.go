// Tests for the redacting handler (U-09, U-23). Positive controls were
// run against every layer: with the redactor made a no-op, and with each
// rule removed one at a time (key list, JWT, brg1., sb_secret_, Bearer,
// exact tokens, group walking, non-scalar suppression, message
// redaction, span anchoring), a specific test here goes red — a suite
// where a no-op redactor passes proves nothing (the P1-2 lesson).
package log_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"testing"
	"time"

	aklog "github.com/appshapes/brigade/internal/adapterkit/log"
)

// Canary secrets. Every one carries a distinctive fragment that appears
// nowhere else, so "the fragment is absent from the output" is exactly
// "the secret did not leak".
var (
	jwtHeader     = "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"
	jwtPayload    = base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"canary-sub-1"}`))
	jwtSig        = "sigCanary123456"
	jwtToken      = jwtHeader + "." + jwtPayload + "." + jwtSig
	brgSecretHalf = "deadbeefdeadbeefdeadbeefdeadbeef"
	brgSecret     = "brg1.team-7." + brgSecretHalf
	sbSecretHalf  = "canaryAAAABBBBCCCC"
	sbSecret      = "sb_secret_" + sbSecretHalf
	bearerTok     = "opaque-canary-token-99" //nolint:gosec // planted canary, not a credential
	// The exact token contains whitespace and punctuation no format
	// pattern can recognise: only exact-token redaction catches it.
	exactTok = "mtok canary~4242 with spaces"
)

func newLogger(secrets ...string) (*bytes.Buffer, *slog.Logger, *aklog.Redactor) {
	buf := &bytes.Buffer{}
	r := aklog.NewRedactor(secrets...)
	return buf, aklog.New(buf, slog.LevelDebug, r), r
}

func mustNotLeak(t *testing.T, out string, frags ...string) {
	t.Helper()
	for _, f := range frags {
		if strings.Contains(out, f) {
			t.Errorf("output leaks %q:\n%s", f, out)
		}
	}
}

func mustContain(t *testing.T, out string, frags ...string) {
	t.Helper()
	for _, f := range frags {
		if !strings.Contains(out, f) {
			t.Errorf("output does not contain %q:\n%s", f, out)
		}
	}
}

func parseLine(t *testing.T, line string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		t.Fatalf("handler output is not one valid JSON object: %v\n%s", err, line)
	}
	return m
}

func TestKeyListRedactsWhateverTheValue(t *testing.T) {
	t.Parallel()
	// hunter2-key-canary matches no format pattern and is not registered
	// as an exact token: ONLY the key list can catch it, so this test is
	// the discriminating one for the key-list rule.
	const val = "hunter2-key-canary"
	keys := []string{
		"token", "secret", "authorization", "apikey", "api_key",
		"access_token", "refresh_token", "join_secret", "password",
		"Authorization", "API-Key", "messaging_token", "client_secret",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			buf, lg, _ := newLogger()
			lg.Info("m", slog.String(key, val))
			mustNotLeak(t, buf.String(), val)
			mustContain(t, buf.String(), aklog.Redacted)
		})
	}
	t.Run("non-string value", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.Int64("token", 987654321987))
		mustNotLeak(t, buf.String(), "987654321987")
		mustContain(t, buf.String(), aklog.Redacted)
	})
	t.Run("innocent keys survive", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.String("event", "connected"), slog.String("comp", "watcher"))
		mustContain(t, buf.String(), "connected", "watcher")
		mustNotLeak(t, buf.String(), aklog.Redacted)
	})
}

func TestPatternJWT(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"whole value":     jwtToken,
		"in a url":        "https://h.example.com/cb?jwt=" + jwtToken + "&next=%2Fhome",
		"mid error text":  "PGRST301: refresh with " + jwtToken + " was rejected upstream",
		"empty signature": jwtHeader + "." + jwtPayload + ".",
		"in json":         `{"access":"` + jwtToken + `","ok":true}`,
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf, lg, _ := newLogger()
			lg.Info("m", slog.String("detail", val))
			mustNotLeak(t, buf.String(), jwtHeader, jwtPayload, jwtSig)
			mustContain(t, buf.String(), aklog.Redacted)
		})
	}
}

func TestPatternJoinSecret(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"whole value":    brgSecret,
		"in a url":       "https://h/join?s=" + brgSecret + "&x=1",
		"mid error text": "join_team failed: secret " + brgSecret + " rejected",
	}
	for name, val := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf, lg, _ := newLogger()
			lg.Info("m", slog.String("detail", val))
			mustNotLeak(t, buf.String(), brgSecretHalf, brgSecret)
			mustContain(t, buf.String(), aklog.Redacted)
		})
	}
}

func TestPatternSBSecret(t *testing.T) {
	t.Parallel()
	buf, lg, _ := newLogger()
	lg.Info("m", slog.String("detail", "key "+sbSecret+" refused by kong"))
	mustNotLeak(t, buf.String(), sbSecretHalf)
	mustContain(t, buf.String(), aklog.Redacted)
}

func TestPatternBearer(t *testing.T) {
	t.Parallel()
	for name, hdr := range map[string]string{
		"canonical": "GET /rest/v1/x HTTP/1.1\nAuthorization: Bearer " + bearerTok + "\nHost: h",
		"lowercase": "authorization: bearer " + bearerTok,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			buf, lg, _ := newLogger()
			lg.Info("m", slog.String("hdr", hdr))
			mustNotLeak(t, buf.String(), bearerTok)
			mustContain(t, buf.String(), aklog.Redacted)
		})
	}
}

func TestExactTokenMidString(t *testing.T) {
	t.Parallel()
	t.Run("registered at construction", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger(exactTok)
		lg.Info("m", slog.String("detail", "auth line with "+exactTok+" rejected by socket"))
		lg.Info("m2", slog.String("u", "https://cb?x="+exactTok+"&y=1"))
		mustNotLeak(t, buf.String(), exactTok)
		mustContain(t, buf.String(), aklog.Redacted)
	})
	t.Run("registered after construction", func(t *testing.T) {
		t.Parallel()
		buf, lg, r := newLogger()
		r.Add("late-canary-secret 5555")
		lg.Info("m", slog.String("detail", "wrapped: late-canary-secret 5555 leaked?"))
		mustNotLeak(t, buf.String(), "late-canary-secret 5555")
		mustContain(t, buf.String(), aklog.Redacted)
	})
}

// TestSecretInAttrKeyIsRedacted pins the key-redaction half of
// replaceAttr: a secret can arrive as the attribute KEY itself (a map
// key or URL fragment used as a per-entry attr name), where none of the
// value-side layers see it. The P1-3 adversarial pass found the mutant
// that drops key redaction survived the original suite; these cases are
// its kill.
func TestSecretInAttrKeyIsRedacted(t *testing.T) {
	t.Parallel()
	t.Run("jwt as the key", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.String("ctx_"+jwtToken, "v"))
		mustNotLeak(t, buf.String(), jwtPayload, jwtSig)
		mustContain(t, buf.String(), aklog.Redacted)
	})
	t.Run("exact token as the key inside a group", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger(exactTok)
		lg.Info("m", slog.Group("g", slog.String("k-"+exactTok, "v")))
		mustNotLeak(t, buf.String(), exactTok)
	})
	t.Run("secret-list key carrying a jwt in the key text", func(t *testing.T) {
		t.Parallel()
		// isSecretKey hits (the key contains "token"), and the KEY must
		// still be scrubbed of its embedded secret, not merely have its
		// value replaced.
		buf, lg, _ := newLogger()
		lg.Info("m", slog.String("token_"+jwtToken, "v"))
		mustNotLeak(t, buf.String(), jwtPayload, jwtSig)
	})
}

func TestGroupAttrsAreWalked(t *testing.T) {
	t.Parallel()
	t.Run("inline groups", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m",
			slog.Group("req",
				slog.String("authorization", "Basic dXNlcjpwYXNz"),
				slog.String("url", "https://h/cb?jwt="+jwtToken),
				slog.Group("inner", slog.String("note", "joined with "+brgSecret)),
			),
		)
		out := buf.String()
		mustNotLeak(t, out, jwtPayload, jwtSig, brgSecretHalf, "dXNlcjpwYXNz")
		m := parseLine(t, out)
		req, ok := m["req"].(map[string]any)
		if !ok {
			t.Fatalf("group req is not an object: %v", m)
		}
		if req["authorization"] != aklog.Redacted {
			t.Errorf("authorization inside a group = %q, want %q", req["authorization"], aklog.Redacted)
		}
		inner, ok := req["inner"].(map[string]any)
		if !ok {
			t.Fatalf("nested group inner is not an object: %v", m)
		}
		note, _ := inner["note"].(string)
		if !strings.Contains(note, aklog.Redacted) {
			t.Errorf("nested group attr not redacted: %q", note)
		}
	})
	t.Run("WithGroup and With", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger(exactTok)
		lg.WithGroup("g").
			With(slog.String("apikey", sbSecret), slog.String("u", "x "+exactTok+" y")).
			Info("preformatted")
		out := buf.String()
		mustNotLeak(t, out, sbSecretHalf, exactTok)
		m := parseLine(t, out)
		g, ok := m["g"].(map[string]any)
		if !ok {
			t.Fatalf("group g is not an object: %v", m)
		}
		if g["apikey"] != aklog.Redacted {
			t.Errorf("apikey in WithGroup/With = %q, want %q", g["apikey"], aklog.Redacted)
		}
	})
}

func TestMessageIsRedacted(t *testing.T) {
	t.Parallel()
	buf, lg, _ := newLogger(exactTok)
	lg.Error("refresh with " + jwtToken + " failed")
	lg.Error("socket refused auth line " + exactTok + " (5s)")
	out := buf.String()
	mustNotLeak(t, out, jwtPayload, jwtSig, exactTok)
	mustContain(t, out, aklog.Redacted)
}

type panickyErr struct{}

func (p *panickyErr) Error() string {
	if p == nil {
		panic("Error() on nil receiver")
	}
	return "panicky"
}

func TestErrHelper(t *testing.T) {
	t.Parallel()
	t.Run("wrapped secret", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		err := fmt.Errorf("wrap: %w", errors.New("refresh with "+jwtToken+" failed"))
		lg.Error("boom", aklog.Err(err))
		out := buf.String()
		mustNotLeak(t, out, jwtPayload, jwtSig)
		mustContain(t, out, "wrap: refresh with "+aklog.Redacted+" failed")
	})
	t.Run("nil error", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", aklog.Err(nil))
		mustContain(t, buf.String(), "<nil>")
	})
	t.Run("typed-nil error via slog.Any does not crash", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		var pe *panickyErr
		lg.Info("m", slog.Any("e", pe))
		mustContain(t, buf.String(), "panicked in Error()")
	})
}

func TestNonScalarAnySuppressed(t *testing.T) {
	t.Parallel()
	// None of these canaries match a pattern or the key list from
	// inside the value: only the scalar-only suppression keeps them out
	// of the log. forbidigo is the static half of the same rule; this is
	// the runtime half, for code paths lint cannot see (slog.AnyValue,
	// third-party helpers).
	type cfg struct {
		JWT  string
		Note string
	}
	t.Run("struct", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.Any("cfg", cfg{JWT: jwtToken, Note: "struct-canary-1"}))
		mustNotLeak(t, buf.String(), jwtPayload, jwtSig, "struct-canary-1")
		mustContain(t, buf.String(), aklog.Suppressed)
	})
	t.Run("map", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.Any("m", map[string]string{"note": "map-canary-2"}))
		mustNotLeak(t, buf.String(), "map-canary-2")
		mustContain(t, buf.String(), aklog.Suppressed)
	})
	t.Run("slice via AnyValue", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.Attr{Key: "s", Value: slog.AnyValue([]string{"slice-canary-3"})})
		mustNotLeak(t, buf.String(), "slice-canary-3")
		mustContain(t, buf.String(), aklog.Suppressed)
	})
	t.Run("nil", func(t *testing.T) {
		t.Parallel()
		buf, lg, _ := newLogger()
		lg.Info("m", slog.Any("n", nil))
		mustContain(t, buf.String(), aklog.Suppressed)
	})
}

func TestScalarsPassThrough(t *testing.T) {
	t.Parallel()
	buf, lg, _ := newLogger()
	at := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	lg.Info("m",
		slog.Int("count", 42),
		slog.Bool("ok", true),
		slog.Duration("d", 1500*time.Millisecond),
		slog.Float64("f", 2.5),
		slog.Uint64("u", 7),
		slog.Time("at", at),
	)
	m := parseLine(t, buf.String())
	if m["count"] != float64(42) || m["ok"] != true || m["d"] != float64(1500000000) ||
		m["f"] != 2.5 || m["u"] != float64(7) {
		t.Errorf("scalar attrs were altered: %v", m)
	}
	if s, _ := m["at"].(string); !strings.HasPrefix(s, "2026-09-01T12:00:00") {
		t.Errorf("time attr was altered: %v", m["at"])
	}
}

// TestOverlapComposition pins the span-merge and anchored-occurrence
// design: junk that parses as a secret format directly before a real
// token must not consume the token's prefix and leave its tail exposed
// (naive leftmost non-overlapping matching does exactly that).
func TestOverlapComposition(t *testing.T) {
	t.Parallel()
	r := aklog.NewRedactor()
	t.Run("jwt-shaped junk before a jwt", func(t *testing.T) {
		t.Parallel()
		out := r.Redact("eyJXX.YY." + jwtToken)
		mustNotLeak(t, out, jwtHeader, jwtPayload, jwtSig)
	})
	t.Run("jwt directly abutting a join secret", func(t *testing.T) {
		t.Parallel()
		out := r.Redact(jwtToken + brgSecret)
		mustNotLeak(t, out, jwtPayload, jwtSig, brgSecretHalf)
	})
	t.Run("sb_secret junk before a join secret", func(t *testing.T) {
		t.Parallel()
		out := r.Redact("sb_secret_X" + brgSecret + " tail")
		mustNotLeak(t, out, brgSecretHalf)
		mustContain(t, out, "tail")
	})
}

func TestRedactorNilSafe(t *testing.T) {
	t.Parallel()
	var r *aklog.Redactor
	out := r.Redact("with " + jwtToken + " inside")
	mustNotLeak(t, out, jwtPayload, jwtSig)
	buf, lg, _ := func() (*bytes.Buffer, *slog.Logger, *aklog.Redactor) {
		b := &bytes.Buffer{}
		return b, aklog.New(b, nil, nil), nil
	}()
	lg.Info("m", slog.String("d", brgSecret))
	mustNotLeak(t, buf.String(), brgSecretHalf)
}

// TestAddIgnoresEmptyAndDuplicate pins the Add guards: an empty exact
// token must be dropped (it would match at every position and mangle —
// or panic on — every string), and a duplicate must not be stored twice.
func TestAddIgnoresEmptyAndDuplicate(t *testing.T) {
	t.Parallel()
	buf, lg, r := newLogger("", "x-canary-tok", "x-canary-tok")
	r.Add("")
	lg.Info("m", slog.String("d", "safe text survives"))
	lg.Info("m2", slog.String("d", "with x-canary-tok inside"))
	out := buf.String()
	mustContain(t, out, "safe text survives")
	mustNotLeak(t, out, "x-canary-tok")
}

// TestAddConcurrentWithLogging exists for the race detector: Add is
// documented safe under concurrent logging.
func TestAddConcurrentWithLogging(t *testing.T) {
	t.Parallel()
	buf, lg, r := newLogger()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := range 100 {
			r.Add("concurrent-canary-" + strconv.Itoa(i))
		}
	}()
	for i := range 100 {
		lg.Info("m", slog.Int("i", i))
	}
	<-done
	buf.Reset()
	lg.Info("m", slog.String("d", "leaked concurrent-canary-99 here"))
	mustNotLeak(t, buf.String(), "concurrent-canary-99")
}

func TestParseLevel(t *testing.T) {
	t.Parallel()
	for name, want := range map[string]slog.Level{
		"error": slog.LevelError,
		"warn":  slog.LevelWarn,
		"info":  slog.LevelInfo,
		"debug": slog.LevelDebug,
	} {
		got, err := aklog.ParseLevel(name)
		if err != nil || got != want {
			t.Errorf("ParseLevel(%q) = %v, %v; want %v, nil", name, got, err, want)
		}
	}
	for _, bad := range []string{"", "INFO", "trace", "debug "} {
		if _, err := aklog.ParseLevel(bad); err == nil {
			t.Errorf("ParseLevel(%q) succeeded, want error", bad)
		} else if bad != "" && strings.Contains(err.Error(), bad) {
			t.Errorf("ParseLevel error echoes its input: %v", err)
		}
	}
}

func TestLevelFilter(t *testing.T) {
	t.Parallel()
	buf, lg := func() (*bytes.Buffer, *slog.Logger) {
		b := &bytes.Buffer{}
		return b, aklog.New(b, slog.LevelWarn, nil)
	}()
	lg.Debug("quiet")
	lg.Info("quiet")
	lg.Warn("loud")
	if strings.Contains(buf.String(), "quiet") || !strings.Contains(buf.String(), "loud") {
		t.Errorf("level filtering broken:\n%s", buf.String())
	}
}

// stripFragments removes every canary fragment from a fuzz-controlled
// string, looping until none remains (a single ReplaceAll pass can
// splice a new occurrence out of the pieces around a removed one). The
// fuzzer sees the fragments as comparison operands and would otherwise
// eventually plant one in the surrounding context itself, making the
// leak assertion fire on input that was never a secret.
func stripFragments(s string, frags []string) string {
	for changed := true; changed; {
		changed = false
		for _, f := range frags {
			if strings.Contains(s, f) {
				s = strings.ReplaceAll(s, f, "")
				changed = true
			}
		}
	}
	return s
}

// FuzzRedact drives the full logger pipeline — planted secrets inside
// fuzz-mutated JSON documents, URLs, stack traces, the message, and
// slog.Group attributes — and fails if any distinctive fragment of any
// planted secret survives into the output (U-23). Positive control: with
// the redactor made a no-op, the very first seed fails.
func FuzzRedact(f *testing.F) {
	f.Add("", "", "k")
	f.Add("eyJXX.YY.", "", "authorization") // jwt-shaped junk glued to the jwt (overlap class)
	f.Add("eyJAB.", ".tail", "eyJ")
	f.Add("prefix ", " suffix", "note")
	f.Add("brg1.decoy.", "&next=1", "url2")
	f.Add("sb_secret_decoy ", "\n", "hdr2")
	f.Add("Bearer decoy ", " Bearer tail", "auth_note")
	f.Add("\x00\xff", "\u2028", "k2")
	f.Add(`"}{"`, `\`, "quote")
	frags := []string{jwtPayload, jwtSig, brgSecretHalf, sbSecretHalf, bearerTok, exactTok}
	f.Fuzz(func(t *testing.T, prefix, suffix, key string) {
		prefix = stripFragments(prefix, frags)
		suffix = stripFragments(suffix, frags)
		key = stripFragments(key, frags)
		if key == "" || key == "inner" || key == "doc" {
			key = "k"
		}
		red := aklog.NewRedactor(exactTok)
		var buf bytes.Buffer
		lg := aklog.New(&buf, slog.LevelDebug, red)
		for _, sec := range []string{jwtToken, brgSecret, sbSecret, exactTok} {
			jsonDoc := `{"p":` + strconv.Quote(prefix) + `,"v":` + strconv.Quote(prefix+sec+suffix) + `}`
			url := "https://api.example.com/cb?state=" + prefix + "&code=" + sec + "&next=" + suffix
			stack := "goroutine 7 [running]:\nmain.post(0x1)\n\t/src/post.go:42 +0x2f\n" + prefix + sec + "\n" + suffix
			lg.Info(prefix+sec+suffix,
				slog.String("json", jsonDoc),
				slog.String("url", url),
				slog.String("stack", stack),
				slog.String("k_"+prefix+sec+suffix, "key-planted"),
				slog.Group("grp",
					slog.String(key, url),
					slog.Group("inner", slog.String("doc", jsonDoc)),
				),
			)
		}
		// The Bearer scheme is recognised at a word boundary, so the
		// context guarantees one: text glued directly onto "Bearer"
		// forms a different word, which is not a Bearer header.
		hdr := prefix + "\nAuthorization: Bearer " + bearerTok + "\n" + suffix
		lg.Info("hdr", slog.String("hdr", hdr), slog.Group("grp", slog.String(key, hdr)))
		lg.Info("kv", slog.String("apikey", prefix+suffix), slog.String("password", prefix))

		out := buf.String()
		for _, frag := range frags {
			if strings.Contains(out, frag) {
				t.Fatalf("leak of %q with prefix=%q suffix=%q key=%q:\n%s", frag, prefix, suffix, key, out)
			}
		}
		for line := range strings.Lines(out) {
			line = strings.TrimSuffix(line, "\n")
			if line == "" {
				continue
			}
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil {
				t.Fatalf("handler emitted a non-JSON line: %v\n%s", err, line)
			}
		}
	})
}
