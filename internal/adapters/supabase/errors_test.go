package supabase

import (
	"context"
	"crypto/x509"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// TestMappingTable is every row of 5.11's table (U-24): the brigade:
// prefix first, the SQLSTATE second, the HTTP status never — a
// brigade:not_found under 500 (P0002), 404 (PT404) and even 200 is
// not_found; 42501 is unauthorized with a JWT and unauthenticated as a
// 401; PGRST202/205 is migration drift; a non-PostgREST 5xx is
// unavailable.
func TestMappingTable(t *testing.T) {
	t.Parallel()
	pg := func(code, message string) []byte {
		return []byte(`{"code":"` + code + `","details":null,"hint":null,"message":"` + message + `"}`)
	}
	for name, tc := range []struct {
		status int
		body   []byte
		code   protocol.Code
		reason string
		field  string
		retry  int
	}{
		{500, pg("P0002", "brigade:not_found"), protocol.CodeNotFound, "", "", 0},
		{404, pg("PT404", "brigade:not_found"), protocol.CodeNotFound, "", "", 0},
		{200, pg("PT404", "brigade:not_found"), protocol.CodeNotFound, "", "", 0},
		{403, pg("42501", "brigade:unauthorized"), protocol.CodeUnauthorized, "", "", 0},
		{401, pg("42501", "permission denied for schema brigade"), protocol.CodeUnauthenticated, "", "", 0},
		{403, pg("42501", "permission denied for table teams"), protocol.CodeUnauthorized, "", "", 0},
		{403, pg("28000", "brigade:unauthenticated"), protocol.CodeUnauthenticated, "", "", 0},
		{403, pg("28000", "no such user"), protocol.CodeUnauthenticated, "", "", 0},
		{500, pg("PT404", "brigade:not_found"), protocol.CodeNotFound, "", "", 0},
		// The SQLSTATE rows on their own, without the brigade: prefix: a
		// raise from a future migration or PostgreSQL's own no-rows path
		// must still be the uniform not_found, whatever the HTTP status.
		{404, pg("PT404", "no such session"), protocol.CodeNotFound, "", "", 0},
		{500, pg("P0002", "query returned no rows"), protocol.CodeNotFound, "", "", 0},
		{200, pg("P0002", "no data found"), protocol.CodeNotFound, "", "", 0},
		{400, pg("22023", "brigade:invalid_input:harness"), protocol.CodeInvalidInput, "rejected_by_backend", "harness", 0},
		{400, pg("22023", "brigade:invalid_input:session_name"), protocol.CodeInvalidInput, "rejected_by_backend", "session_name", 0},
		{400, pg("P0001", "brigade:conflict:session_live"), protocol.CodeConflict, "session_live", "", 0},
		{400, pg("P0001", "brigade:conflict:session_closed"), protocol.CodeConflict, "session_closed", "", 0},
		{400, pg("P0001", "brigade:conflict:idempotency_key"), protocol.CodeConflict, "idempotency_key", "", 0},
		{400, pg("P0001", "brigade:rate_limited:sender_quota_for_recipient:60"), protocol.CodeRateLimited, "sender_quota_for_recipient", "", 60_000},
		{400, pg("P0001", "brigade:rate_limited:register_session:3600"), protocol.CodeRateLimited, "register_session", "", 3_600_000},
		{400, pg("P0001", "brigade:rate_limited:team_create"), protocol.CodeRateLimited, "team_create", "", 60_000},
		{400, pg("P0001", "brigade:loop_detected:max_hops"), protocol.CodeLoopDetected, "max_hops", "", 0},
		{409, pg("23505", "duplicate key value violates unique constraint"), protocol.CodeConflict, "unique_violation", "", 0},
		{500, pg("57014", "canceling statement due to statement timeout"), protocol.CodeUnavailable, reasonBackendError, "", 0},
		{404, pg("PGRST202", "Could not find the function brigade.nope"), protocol.CodeInternal, reasonMigrationDrift, "", 0},
		{404, pg("PGRST205", "Could not find the table"), protocol.CodeInternal, reasonMigrationDrift, "", 0},
		{401, pg("PGRST301", "JWT expired"), protocol.CodeUnauthenticated, "", "", 0},
		{401, pg("PGRST303", "JWT expired"), protocol.CodeUnauthenticated, "", "", 0},
		{400, pg("22P02", "invalid input syntax for type uuid"), protocol.CodeInternal, reasonBackendResponse, "", 0},
		{500, pg("XX000", "internal error"), protocol.CodeUnavailable, reasonBackendError, "", 0},
		{500, pg("P0001", "brigade:frobnicate"), protocol.CodeInternal, reasonBackendResponse, "", 0},
		{502, []byte(`<html>bad gateway</html>`), protocol.CodeUnavailable, reasonBackendError, "", 0},
		{503, []byte(``), protocol.CodeUnavailable, reasonBackendError, "", 0},
		{401, []byte(`{"message":"Invalid API key"}`), protocol.CodeConfig, reasonAPIKeyRejected, "", 0},
		{404, []byte(`{"message":"no Route matched"}`), protocol.CodeInternal, reasonBackendResponse, "", 0},
	} {
		got := mapRPCFailure(tc.status, tc.body)
		if got.Code != tc.code {
			t.Errorf("row %d (%d %s): code %s, want %s", name, tc.status, tc.body, got.Code, tc.code)
			continue
		}
		if tc.reason != "" && got.Details["reason"] != tc.reason {
			t.Errorf("row %d: reason %q, want %q", name, got.Details["reason"], tc.reason)
		}
		if tc.field != "" && got.Details["field"] != tc.field {
			t.Errorf("row %d: field %q, want %q", name, got.Details["field"], tc.field)
		}
		if got.RetryAfterMS != tc.retry {
			t.Errorf("row %d: retry_after_ms %d, want %d", name, got.RetryAfterMS, tc.retry)
		}
		if got.Code.Exit() != tc.code.Exit() {
			t.Errorf("row %d: exit %d", name, got.Code.Exit())
		}
		for _, leak := range []string{"permission denied", "duplicate key", "Could not find", "bad gateway", "Invalid API key", "canceling"} {
			if contains(got.Message, leak) {
				t.Errorf("row %d: message echoes server text: %q", name, got.Message)
			}
		}
	}
}

// TestUniformErrorsCarryNoDetails pins the byte-identity rule of section
// 3: not_found and unauthorized are fixed strings without details,
// whatever the server said, and the join refusal is one fixed text.
func TestUniformErrorsCarryNoDetails(t *testing.T) {
	t.Parallel()
	pg := func(code, message string) []byte {
		return []byte(`{"code":"` + code + `","details":"some detail","hint":"some hint","message":"` + message + `"}`)
	}
	want := newFailure(t, errNotFound())
	for _, body := range [][]byte{
		pg("PT404", "brigade:not_found"), pg("P0002", "brigade:not_found"), pg("PT404", "brigade:not_found:extra"),
		pg("PT404", "no such session"), pg("P0002", "query returned no rows"),
	} {
		for _, status := range []int{200, 404, 500} {
			if got := newFailure(t, mapRPCFailure(status, body)); got != want {
				t.Errorf("not_found under %d differs: %s vs %s", status, got, want)
			}
		}
	}
	want = newFailure(t, errNotMember())
	for _, body := range [][]byte{pg("42501", "brigade:unauthorized"), pg("42501", "permission denied for table x"), pg("42501", "brigade:unauthorized:revoked")} {
		if got := newFailure(t, mapRPCFailure(403, body)); got != want {
			t.Errorf("unauthorized differs: %s vs %s", got, want)
		}
	}
	if errSecretRejected().Details != nil || errSecretRejected().Code != protocol.CodeUnauthorized {
		t.Fatalf("errSecretRejected = %+v", errSecretRejected())
	}
	if errNotFound().Details != nil || errNotMember().Details != nil {
		t.Fatalf("the uniform errors carry details")
	}
}

// TestTransportMapping: x509 verification failures are unavailable/tls
// with the SSL_CERT_FILE hint, the offline dialer is unavailable/offline,
// a deadline is unavailable/timeout, anything else unavailable/
// unreachable, and a *protocol.Error passes through.
func TestTransportMapping(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		err    error
		reason string
	}{
		"unknown authority": {x509.UnknownAuthorityError{}, reasonTLS},
		"hostname":          {x509.HostnameError{Host: "x"}, reasonTLS},
		"invalid cert":      {x509.CertificateInvalidError{Reason: x509.Expired}, reasonTLS},
		"system roots":      {&x509.SystemRootsError{}, reasonTLS},
		"wrapped x509":      {errors.New("Post \"https://x\": tls: failed to verify certificate: x509: certificate signed by unknown authority"), reasonTLS},
		"offline":           {errOffline, reasonOffline},
		"deadline":          {context.DeadlineExceeded, reasonTimeout},
		"net timeout":       {&net.OpError{Op: "dial", Err: timeoutError{}}, reasonTimeout},
		"refused":           {&net.OpError{Op: "dial", Err: errors.New("connection refused")}, reasonUnreachable},
		"dns":               {&net.DNSError{Err: "no such host", Name: "x"}, reasonUnreachable},
	}
	for name, tc := range cases {
		got := mapTransportError(tc.err)
		if got.Code != protocol.CodeUnavailable || got.Details["reason"] != tc.reason {
			t.Errorf("%s: %+v, want unavailable/%s", name, got, tc.reason)
		}
		if got.Object().Retryable != true {
			t.Errorf("%s: retryable false on unavailable", name)
		}
	}
	if got := mapTransportError(errNotFound()); got != errNotFound() && got.Code != protocol.CodeNotFound {
		t.Fatalf("a protocol error did not pass through: %+v", got)
	}
	if !contains(errTLS().Message, "SSL_CERT_FILE") {
		t.Fatalf("errTLS carries no hint: %s", errTLS().Message)
	}
}

// timeoutError is a net.Error whose Timeout is true.
type timeoutError struct{}

func (timeoutError) Error() string   { return "i/o timeout" }
func (timeoutError) Timeout() bool   { return true }
func (timeoutError) Temporary() bool { return true }

// TestAuthErrorShapes: both GoTrue body shapes decode to the same code,
// the terminal set is exactly the four of 5.1, and every non-terminal
// mapping is a fixed text.
func TestAuthErrorShapes(t *testing.T) {
	t.Parallel()
	newShape := []byte(`{"code":"refresh_token_not_found","message":"Invalid Refresh Token: Refresh Token Not Found"}`)
	legacy := []byte(`{"code":400,"error_code":"refresh_token_not_found","msg":"Invalid Refresh Token"}`)
	oldest := []byte(`{"error":"invalid_grant","error_description":"Invalid Refresh Token"}`)
	if ae := parseAuthError(400, newShape); ae.code != authRefreshTokenNotFound || ae.message == "" || !ae.terminal() {
		t.Fatalf("new shape: %+v", ae)
	}
	if ae := parseAuthError(400, legacy); ae.code != authRefreshTokenNotFound || ae.message == "" || !ae.terminal() {
		t.Fatalf("legacy shape: %+v", ae)
	}
	if ae := parseAuthError(400, oldest); ae.code != "invalid_grant" || ae.terminal() {
		t.Fatalf("oldest shape: %+v", ae)
	}
	if ae := parseAuthError(502, []byte(`<html>`)); ae.code != "" || ae.status != 502 {
		t.Fatalf("html: %+v", ae)
	}
	for _, code := range []string{authRefreshTokenNotFound, authSessionNotFound, authSessionExpired, authUserNotFound} {
		if !(authError{code: code}).terminal() {
			t.Errorf("%s is not terminal", code)
		}
		if got := mapAuthError(authError{status: 400, code: code}); got.Code != protocol.CodeUnauthenticated || got.Details["reason"] != reasonCredentialRevoked {
			t.Errorf("%s maps to %+v", code, got)
		}
	}
	if (authError{code: authRefreshTokenAlreadyUsed}).terminal() {
		t.Fatalf("refresh_token_already_used is terminal at once; E0-6 says the family survives")
	}
	for name, tc := range map[string]struct {
		ae   authError
		code protocol.Code
	}{
		"signup disabled": {authError{status: 422, code: authSignupDisabled}, protocol.CodeConfig},
		"rate limited":    {authError{status: http.StatusTooManyRequests, code: "over_request_rate_limit"}, protocol.CodeRateLimited},
		"5xx":             {authError{status: 503}, protocol.CodeUnavailable},
		"401":             {authError{status: 401, code: "bad_jwt"}, protocol.CodeUnauthenticated},
		"400":             {authError{status: 400, code: "validation_failed"}, protocol.CodeUnauthenticated},
		"teapot":          {authError{status: 418}, protocol.CodeInternal},
	} {
		if got := mapAuthError(tc.ae); got.Code != tc.code {
			t.Errorf("%s: %+v, want %s", name, got, tc.code)
		}
	}
}

// TestBrigadeMessageCoversEveryCode: every one of the twelve codes has a
// row, and an unknown code is internal.
func TestBrigadeMessageCoversEveryCode(t *testing.T) {
	t.Parallel()
	for _, code := range []protocol.Code{
		protocol.CodeInternal, protocol.CodeUsage, protocol.CodeInvalidInput, protocol.CodeUnauthenticated,
		protocol.CodeUnauthorized, protocol.CodeNotFound, protocol.CodeConflict, protocol.CodeRateLimited,
		protocol.CodeUnavailable, protocol.CodeProtocolMismatch, protocol.CodeConfig, protocol.CodeLoopDetected,
	} {
		got := mapBrigadeMessage("brigade:" + string(code) + ":x:5")
		want := code
		if code == protocol.CodeInternal || code == protocol.CodeUsage || code == protocol.CodeProtocolMismatch {
			want = protocol.CodeInternal
		}
		if got.Code != want {
			t.Errorf("%s: %s, want %s", code, got.Code, want)
		}
	}
	if got := mapBrigadeMessage("brigade:nonsense"); got.Code != protocol.CodeInternal {
		t.Fatalf("unknown code: %s", got.Code)
	}
	if got := mapBrigadeMessage("brigade:rate_limited:x:notanumber"); got.RetryAfterMS != 60_000 {
		t.Fatalf("unparsable retry: %d", got.RetryAfterMS)
	}
}

// TestJWTHelpers: exp and sub are read from the payload without any
// verification, and a token that is not a JWT is not usable.
func TestJWTHelpers(t *testing.T) {
	t.Parallel()
	token := mintJWT(testUserID, fixedStart.Add(time.Hour))
	exp, ok := jwtExpiry(token)
	if !ok || !exp.Equal(fixedStart.Add(time.Hour)) {
		t.Fatalf("exp = %v %v", exp, ok)
	}
	if jwtSubject(token) != testUserID {
		t.Fatalf("sub = %q", jwtSubject(token))
	}
	for _, bad := range []string{"", "x", "a.b", "a.!!!.c", "a." + base64URL(`{"exp":"soon"}`) + ".c", "a." + base64URL(`[]`) + ".c"} {
		if _, ok := jwtExpiry(bad); ok {
			t.Errorf("%q parsed as a JWT with exp", bad)
		}
	}
	s := &session{AccessToken: token, RefreshToken: "rt"}
	if !s.usable() || s.principalRef() != testUserID {
		t.Fatalf("session not usable: %+v", s)
	}
	if (&session{AccessToken: token}).usable() || (&session{RefreshToken: "rt"}).usable() || (*session)(nil).usable() {
		t.Fatalf("an incomplete session is usable")
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
