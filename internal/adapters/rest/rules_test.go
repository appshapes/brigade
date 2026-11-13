package rest

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"

	adapterlog "github.com/appshapes/brigade/internal/adapterkit/log"
	"github.com/appshapes/brigade/internal/protocol"
)

// The tests in this file touch neither a file nor the network, so they run
// on every platform; the ones that drive the adapter against the stand-in
// server are in the files tagged !windows.

// TestBackendURLRule pins the url rule of the contract's section 1:
// https, or http for the three loopback hosts it names, and nothing that
// could carry more than a base (credentials, a query, a fragment).
func TestBackendURLRule(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		url    string
		reason string // "" means accepted
	}{
		{"https://brigade.example.com", ""},
		{"https://brigade.example.com:8443", ""},
		{"https://brigade.example.com/prefix", ""},
		{"https://brigade.example.com/prefix/", ""},
		{"https://10.0.0.7:8443", ""},
		{"http://127.0.0.1:8080", ""},
		{"http://127.0.0.1", ""},
		{"http://[::1]:8080", ""},
		{"http://[0:0:0:0:0:0:0:1]:8080", ""},
		{"http://localhost:8080/prefix", ""},
		{"http://LOCALHOST:8080", ""},

		{"http://brigade.example.com", "http_not_loopback"},
		{"http://10.0.0.7:8080", "http_not_loopback"},
		{"http://127.0.0.2:8080", "http_not_loopback"},
		{"http://localhost.example.com", "http_not_loopback"},
		{"http://0.0.0.0:8080", "http_not_loopback"},
		{"ftp://brigade.example.com", "scheme_not_https"},
		{"ws://127.0.0.1:8080", "scheme_not_https"},
		{"brigade.example.com", "malformed_url"},
		{"localhost:8080", "malformed_url"},
		{"https://", "malformed_url"},
		{"https://:8443", "malformed_url"},
		{"https://user:pass@brigade.example.com", "malformed_url"},
		{"https://brigade.example.com?x=1", "malformed_url"},
		{"https://brigade.example.com/#frag", "malformed_url"},
		{"", "malformed_url"},
		{"https://brigade.example.com/\n", "malformed_url"},
	} {
		err := checkBackendURL(tc.url)
		if tc.reason == "" {
			if err != nil {
				t.Errorf("checkBackendURL(%q) = %v, want accepted", tc.url, err)
			}
			continue
		}
		var perr *protocol.Error
		if !errors.As(err, &perr) {
			t.Errorf("checkBackendURL(%q) = %v, want a refusal", tc.url, err)
			continue
		}
		if perr.Code != protocol.CodeInvalidInput || perr.Details["field"] != "url" || perr.Details["reason"] != tc.reason {
			t.Errorf("checkBackendURL(%q) = %s %v, want invalid_input, field url, reason %s", tc.url, perr.Code, perr.Details, tc.reason)
		}
		if tc.url != "" && strings.Contains(perr.Message, tc.url) {
			t.Errorf("checkBackendURL(%q): the message echoes the value", tc.url)
		}
	}
}

// TestCredentialShape pins the contract's bounds on a credential: 16 to
// 512 characters from its alphabet.
func TestCredentialShape(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		value string
		want  bool
	}{
		{"sixteen", strings.Repeat("a", 16), true},
		{"the whole alphabet", "AZaz09._~+/=-AZaz09", true},
		{"the longest", strings.Repeat("x", 512), true},
		{"fifteen", strings.Repeat("a", 15), false},
		{"too long", strings.Repeat("x", 513), false},
		{"empty", "", false},
		{"a space", "aaaaaaaa aaaaaaaa", false},
		{"a newline", "aaaaaaaaaaaaaaaa\n", false},
		{"a quote", `aaaaaaaaaaaaaaaa"`, false},
		{"not ascii", "aaaaaaaaaaaaaaaé", false},
	} {
		if got := validCredential(tc.value); got != tc.want {
			t.Errorf("validCredential(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestReadEnvelope pins what the adapter does with a response (the
// contract's section 1), one row per rule: a valid envelope is the answer
// whatever the status; anything else is `unavailable` for a 5xx and
// `internal` otherwise. "Valid" includes what the contract says a server
// never answers: the adapter's own three codes, a rate_limited with no
// retry_after_ms, another protocol version.
func TestReadEnvelope(t *testing.T) {
	t.Parallel()
	const okBody = `{"ok":true,"protocol_version":"1","result":{"a":1}}`
	failing := func(code, extra string) string {
		return `{"ok":false,"protocol_version":"1","error":{"code":"` + code + `","message":"m","retryable":false` + extra + `}}`
	}
	for _, tc := range []struct {
		name   string
		status int
		body   string
		want   protocol.Code // "" means the result is returned
		reason string
	}{
		{"ok with 200", 200, okBody, "", ""},
		{"ok with 500: the envelope is the answer", 500, okBody, "", ""},
		{"ok with 404: the envelope is the answer", 404, okBody, "", ""},
		{"an error envelope with its own status", 404, failing("not_found", ""), protocol.CodeNotFound, ""},
		{"an error envelope with status 200", 200, failing("conflict", `,"details":{"reason":"session_live"}`), protocol.CodeConflict, "session_live"},
		{"an error envelope with status 500", 500, failing("unauthorized", ""), protocol.CodeUnauthorized, ""},
		{"rate_limited with its retry", 429, failing("rate_limited", `,"retry_after_ms":1200`), protocol.CodeRateLimited, ""},
		{"unavailable from the server", 503, failing("unavailable", ""), protocol.CodeUnavailable, ""},
		{"internal from the server", 500, failing("internal", ""), protocol.CodeInternal, ""},
		{"loop_detected", 422, failing("loop_detected", ""), protocol.CodeLoopDetected, ""},

		{"a gateway page with 502", 502, "<html>bad gateway</html>", protocol.CodeUnavailable, reasonBackendError},
		{"an empty body with 503", 503, "", protocol.CodeUnavailable, reasonBackendError},
		{"JSON that is not an envelope with 500", 500, `{"message":"boom"}`, protocol.CodeUnavailable, reasonBackendError},
		{"a page with 404", 404, "not found", protocol.CodeInternal, reasonBackendResponse},
		{"an empty body with 200", 200, "", protocol.CodeInternal, reasonBackendResponse},
		{"JSON that is not an envelope with 400", 400, `{"error":"bad request"}`, protocol.CodeInternal, reasonBackendResponse},
		{"a redirect", 302, "", protocol.CodeInternal, reasonBackendResponse},
		{"two documents", 200, okBody + okBody, protocol.CodeInternal, reasonBackendResponse},
		{"ok with no result", 200, `{"ok":true,"protocol_version":"1"}`, protocol.CodeInternal, reasonBackendResponse},
		{"ok with a result that is not an object", 200, `{"ok":true,"protocol_version":"1","result":[1]}`, protocol.CodeInternal, reasonBackendResponse},
		{"ok and an error together", 200, `{"ok":true,"protocol_version":"1","result":{},"error":{"code":"internal","message":"m","retryable":false}}`, protocol.CodeInternal, reasonBackendResponse},
		{"another protocol version", 200, `{"ok":true,"protocol_version":"2","result":{}}`, protocol.CodeInternal, reasonBackendResponse},
		{"an error with no message", 400, `{"ok":false,"protocol_version":"1","error":{"code":"invalid_input","message":"","retryable":false}}`, protocol.CodeInternal, reasonBackendResponse},
		{"usage is the adapter's own", 400, failing("usage", ""), protocol.CodeInternal, reasonBackendResponse},
		{"config is the adapter's own", 400, failing("config", ""), protocol.CodeInternal, reasonBackendResponse},
		{"protocol_mismatch is the adapter's own", 400, failing("protocol_mismatch", ""), protocol.CodeInternal, reasonBackendResponse},
		{"a code outside the taxonomy", 418, failing("teapot", ""), protocol.CodeInternal, reasonBackendResponse},
		{"a code outside the taxonomy with 500", 500, failing("teapot", ""), protocol.CodeUnavailable, reasonBackendError},
		{"rate_limited with no retry_after_ms", 429, failing("rate_limited", ""), protocol.CodeInternal, reasonBackendResponse},
	} {
		result, err := readEnvelope(&response{status: tc.status, body: []byte(tc.body)}, adapterlog.NewRedactor())
		if tc.want == "" {
			if err != nil || string(result) != `{"a":1}` {
				t.Errorf("%s: result %q, err %v; want the result", tc.name, result, err)
			}
			continue
		}
		var perr *protocol.Error
		if !errors.As(err, &perr) {
			t.Errorf("%s: err %v, want %s", tc.name, err, tc.want)
			continue
		}
		if perr.Code != tc.want || (tc.reason != "" && perr.Details["reason"] != tc.reason) {
			t.Errorf("%s: %s %v, want %s with reason %q", tc.name, perr.Code, perr.Details, tc.want, tc.reason)
		}
	}
}

// TestReadEnvelopePassesTheServersErrorOn pins that a failing envelope is
// passed on member for member, and that its text goes through the redactor
// on the way: a server that broke the contract and echoed a credential or
// a join secret does not get it printed.
func TestReadEnvelopePassesTheServersErrorOn(t *testing.T) {
	t.Parallel()
	const held = "held-credential-0123456789"
	redactor := adapterlog.NewRedactor(held)
	body := `{"ok":false,"protocol_version":"1","error":{"code":"rate_limited","message":"slow down","retryable":true,` +
		`"retry_after_ms":1500,"details":{"reason":"send_per_minute"}}}`
	_, err := readEnvelope(&response{status: 429, body: []byte(body)}, redactor)
	var perr *protocol.Error
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want the server's error", err)
	}
	if perr.Code != protocol.CodeRateLimited || perr.Message != "slow down" || perr.RetryAfterMS != 1500 || perr.Details["reason"] != "send_per_minute" {
		t.Errorf("the server's error was not passed on as it was: %+v", perr)
	}

	leaky := `{"ok":false,"protocol_version":"1","error":{"code":"unauthenticated","message":"no such credential ` + held +
		`","retryable":false,"details":{"hint":"tried brg1.team.NOTASECRET"}}}`
	_, err = readEnvelope(&response{status: 401, body: []byte(leaky)}, redactor)
	if !errors.As(err, &perr) {
		t.Fatalf("err = %v, want the server's error", err)
	}
	if strings.Contains(perr.Message, held) || strings.Contains(perr.Details["hint"], "NOTASECRET") {
		t.Errorf("a credential or a join secret in the server's text was not redacted: %q %v", perr.Message, perr.Details)
	}
}

// TestCapabilitiesAreTheContracts pins the list `describe` advertises:
// what the contract's section 5 makes every version 1 server implement,
// plus message.receive, the two the adapter earns itself, and team.admin,
// the administration endpoints of section 3a, which a server may leave
// out and the adapter then says so (TestAServerWithoutTeamAdministration).
func TestCapabilitiesAreTheContracts(t *testing.T) {
	t.Parallel()
	want := []string{
		"team.create", "team.join", "team.roster", "team.admin",
		"message.receive", "message.watch.push", "message.watch.stdin_commands",
		"session.description", "session.resume", "session.workspace_label", "session.inbound",
		"session.model", "session.context_used_tokens", "session.human_label",
		"session.brigade_version", "session.sync_peer",
	}
	got := capabilities()
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("capabilities() = %v, want %v", got, want)
	}
}

// TestTransportFailuresAreUnavailable pins the third row of the contract's
// section 1 by cause: whatever kept a request from being answered is
// `unavailable`, with a reason that says which kind it was.
func TestTransportFailuresAreUnavailable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		err    error
		reason string
	}{
		{"the offline switch", errOffline, reasonOffline},
		{"a deadline", context.DeadlineExceeded, reasonTimeout},
		{"a wrapped deadline", fmt.Errorf("Post %q: %w", "https://brigade.example.com/v1/x", context.DeadlineExceeded), reasonTimeout},
		{"a network timeout", &net.DNSError{Err: "i/o timeout", IsTimeout: true}, reasonTimeout},
		{"an unknown authority", x509.UnknownAuthorityError{}, reasonTLS},
		{"a certificate for another host", x509.HostnameError{Certificate: &x509.Certificate{}, Host: "brigade.example.com"}, reasonTLS},
		{"an expired certificate", x509.CertificateInvalidError{Reason: x509.Expired}, reasonTLS},
		{"a TLS alert in text", errors.New("remote error: tls: handshake failure"), reasonTLS},
		{"a refused connection", errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), reasonUnreachable},
		{"a name that does not resolve", &net.DNSError{Err: "no such host", IsNotFound: true}, reasonUnreachable},
	} {
		perr := mapTransportError(tc.err)
		if perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != tc.reason {
			t.Errorf("%s: %s %v, want unavailable with reason %s", tc.name, perr.Code, perr.Details, tc.reason)
		}
		if !perr.Code.Retryable() {
			t.Errorf("%s: not retryable", tc.name)
		}
		if strings.Contains(perr.Message, "brigade.example.com") || strings.Contains(perr.Message, "127.0.0.1:1") {
			t.Errorf("%s: the message carries the underlying error's text: %q", tc.name, perr.Message)
		}
	}
	// An error that is already the protocol's passes through as it is.
	own := errConfig("x", "y")
	if got := mapTransportError(fmt.Errorf("wrapped: %w", own)); got != own {
		t.Errorf("a wrapped protocol error was reclassified: %+v", got)
	}
}
