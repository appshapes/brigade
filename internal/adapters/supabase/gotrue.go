package supabase

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/protocol"
)

// The three GoTrue calls of 5.1 — anonymous sign-up, refresh and global
// sign-out — ported from scripts/experiments/E0-1 and E0-6, where every
// shape below was exchanged with GoTrue v2.196.0.

// apiVersionHeader asks GoTrue for the 2024-01-01 error-body shape. The
// decoder accepts both shapes regardless (parseAuthError), because a
// gateway or an older server may answer without it.
const apiVersionHeader = "2024-01-01"

// A session is the GoTrue session response as the adapter persists it in
// session.json (5.1): the two tokens, the expiry, the user, and — this
// adapter's own member — last_team_ref, which lets a repeated `team leave`
// still answer a non-empty team_ref after the profile is unbound (C-08).
type session struct {
	AccessToken  string      `json:"access_token"`
	TokenType    string      `json:"token_type,omitzero"`
	ExpiresIn    int         `json:"expires_in,omitzero"`
	ExpiresAt    int64       `json:"expires_at,omitzero"`
	RefreshToken string      `json:"refresh_token"`
	User         sessionUser `json:"user"`
	LastTeamRef  string      `json:"last_team_ref,omitzero"`
}

// sessionUser is the subset of the GoTrue user object the adapter keeps.
type sessionUser struct {
	ID          string `json:"id"`
	Aud         string `json:"aud,omitzero"`
	Role        string `json:"role,omitzero"`
	IsAnonymous bool   `json:"is_anonymous,omitzero"`
}

// expiry reads `exp` from the access token's payload without verifying
// the signature (5.1: user tokens are ES256, the legacy anon key is
// HS256, so nothing may assume an algorithm). ok is false for a token
// that is not a JWT or carries no exp.
func (s *session) expiry() (time.Time, bool) {
	return jwtExpiry(s.AccessToken)
}

// usable reports whether the file carries what every command needs: both
// tokens, a user id and a readable expiry.
func (s *session) usable() bool {
	if s == nil || s.AccessToken == "" || s.RefreshToken == "" {
		return false
	}
	_, ok := s.expiry()
	return ok
}

// principalRef is the opaque principal reference of this credential: the
// user id (the JWT `sub`), which the backend stamps as principal_ref.
func (s *session) principalRef() string {
	if s == nil {
		return ""
	}
	if s.User.ID != "" {
		return s.User.ID
	}
	return jwtSubject(s.AccessToken)
}

// jwtClaims decodes the payload of a JWT without verification.
func jwtClaims(token string) (map[string]any, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, false
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return nil, false
	}
	return claims, true
}

// jwtExpiry reads the numeric `exp` claim.
func jwtExpiry(token string) (time.Time, bool) {
	claims, ok := jwtClaims(token)
	if !ok {
		return time.Time{}, false
	}
	exp, ok := claims["exp"].(float64)
	if !ok || exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(exp), 0), true
}

// jwtSubject reads the `sub` claim, or "".
func jwtSubject(token string) string {
	claims, ok := jwtClaims(token)
	if !ok {
		return ""
	}
	sub, _ := claims["sub"].(string)
	return sub
}

// An authError is a decoded GoTrue error body in either shape: with
// X-Supabase-Api-Version it is {"code":"<snake>","message":"…"}; without
// it {"code":<http>,"error_code":"<snake>","msg":"…"}; the oldest servers
// answer {"error":"…","error_description":"…"}. code is the snake_case
// token when any shape carried one.
type authError struct {
	status  int
	code    string
	message string
}

// parseAuthError decodes body loosely, so a numeric `code` and a string
// `code` are both accepted.
func parseAuthError(status int, body []byte) authError {
	ae := authError{status: status}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return ae
	}
	if s, ok := raw["error_code"].(string); ok && s != "" {
		ae.code = s
	} else if s, ok := raw["code"].(string); ok && s != "" {
		ae.code = s
	} else if s, ok := raw["error"].(string); ok && s != "" {
		ae.code = s
	}
	for _, key := range []string{"msg", "message", "error_description"} {
		if s, ok := raw[key].(string); ok && s != "" {
			ae.message = s
			break
		}
	}
	return ae
}

// The GoTrue error codes the credential state machine keys on (5.1,
// corrected by E0-6).
const (
	authRefreshTokenAlreadyUsed = "refresh_token_already_used"
	authRefreshTokenNotFound    = "refresh_token_not_found"
	authSessionNotFound         = "session_not_found"
	authSessionExpired          = "session_expired"
	authUserNotFound            = "user_not_found"
	authSignupDisabled          = "signup_disabled"
)

// terminal reports whether the code ends the credential at once: the
// refresh-token family is gone, so session.json is deleted and the user
// runs `team join` again (5.1).
func (ae authError) terminal() bool {
	switch ae.code {
	case authRefreshTokenNotFound, authSessionNotFound, authSessionExpired, authUserNotFound:
		return true
	}
	return false
}

// mapAuthError maps a GoTrue failure that is neither terminal nor the
// already-used retry to the 4.6 taxonomy.
func mapAuthError(ae authError) *protocol.Error {
	switch {
	case ae.terminal():
		return errCredentialRevoked()
	case ae.code == authSignupDisabled:
		return errConfig("the backend does not allow anonymous sign-ups; enable them in the project's auth settings", reasonSignupDisabled)
	case ae.status == http.StatusTooManyRequests:
		return errRateLimited(reasonAuthRateLimit, 60_000)
	case ae.status >= 500 || ae.status == 0:
		return errUnavailable("the authentication service is unavailable; try again later", reasonBackendError)
	case ae.status == http.StatusUnauthorized || ae.status == http.StatusForbidden:
		return errBackendUnauthenticated()
	case ae.status == http.StatusBadRequest || ae.status == http.StatusUnprocessableEntity:
		return errBackendUnauthenticated()
	}
	return errUnexpectedResponse("the authentication service answered with an unexpected response")
}

// authHeaders are the headers every GoTrue request carries beyond the
// client's own.
func authHeaders() map[string]string {
	return map[string]string{
		"X-Supabase-Api-Version": apiVersionHeader,
		"Content-Type":           "application/json;charset=UTF-8",
	}
}

// decodeSession parses a 200 sign-up or refresh body.
func decodeSession(body []byte) (*session, error) {
	var s session
	if err := json.Unmarshal(body, &s); err != nil || !s.usable() {
		return nil, errUnexpectedResponse("the authentication service answered without a usable session")
	}
	return &s, nil
}

// signUpAnonymous is POST /auth/v1/signup with the body auth-js's
// signInAnonymously() sends (5.1). Only `team create` and `team join`
// call it (T6: routine commands never mint principals).
func (cl *client) signUpAnonymous(ctx context.Context) (*session, error) {
	body := []byte(`{"data":{},"gotrue_meta_security":{}}`)
	resp, err := cl.post(ctx, "/auth/v1/signup", authHeaders(), body)
	if err != nil {
		return nil, err
	}
	if resp.status != http.StatusOK {
		ae := parseAuthError(resp.status, resp.body)
		cl.logAuth("sign-up refused", ae)
		return nil, mapAuthError(ae)
	}
	return decodeSession(resp.body)
}

// refresh is POST /auth/v1/token?grant_type=refresh_token. A 200 answers
// the session; a 4xx answers the decoded authError with a nil session and
// a nil error, so the caller applies the 5.1 state machine; a transport
// failure is the mapped error.
func (cl *client) refresh(ctx context.Context, refreshToken string) (*session, *authError, error) {
	body, err := json.Marshal(map[string]string{"refresh_token": refreshToken})
	if err != nil {
		return nil, nil, errInternal("the refresh request could not be built")
	}
	resp, err := cl.post(ctx, "/auth/v1/token?grant_type=refresh_token", authHeaders(), body)
	if err != nil {
		return nil, nil, err
	}
	if resp.status != http.StatusOK {
		ae := parseAuthError(resp.status, resp.body)
		cl.logAuth("refresh refused", ae)
		if ae.status >= 500 {
			return nil, nil, mapAuthError(ae)
		}
		return nil, &ae, nil
	}
	s, err := decodeSession(resp.body)
	if err != nil {
		return nil, nil, err
	}
	return s, nil, nil
}

// signOutGlobal is POST /auth/v1/logout?scope=global with the access
// token (5.1): afterwards every refresh token of the family answers
// refresh_token_not_found. A 401/403 means the token was already dead,
// which is the outcome the caller wanted, so it is reported as success.
func (cl *client) signOutGlobal(ctx context.Context, accessToken string) error {
	headers := authHeaders()
	headers["Authorization"] = "Bearer " + accessToken
	resp, err := cl.post(ctx, "/auth/v1/logout?scope=global", headers, nil)
	if err != nil {
		return err
	}
	switch resp.status {
	case http.StatusNoContent, http.StatusOK, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound:
		return nil
	}
	ae := parseAuthError(resp.status, resp.body)
	cl.logAuth("sign-out refused", ae)
	return mapAuthError(ae)
}

// logAuth records a GoTrue refusal at debug: the status and the code are
// scalars, and the message goes through the redacting handler.
func (cl *client) logAuth(what string, ae authError) {
	cl.log.Debug(what, slog.Int("status", ae.status), slog.String("code", ae.code), slog.String("server_message", ae.message))
}
