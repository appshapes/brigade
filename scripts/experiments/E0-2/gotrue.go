package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Session is the subset of the GoTrue session response the adapter would persist.
type Session struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	ExpiresAt    int64  `json:"expires_at"`
	RefreshToken string `json:"refresh_token"`
	User         struct {
		ID          string `json:"id"`
		Aud         string `json:"aud"`
		Role        string `json:"role"`
		IsAnonymous bool   `json:"is_anonymous"`
	} `json:"user"`
}

// AuthError is GoTrue's error body (API version 2024-01-01): {"code":<http>,"error_code":"...","msg":"..."}.
type AuthError struct {
	Code      int    `json:"code"`
	ErrorCode string `json:"error_code"`
	Msg       string `json:"msg"`
	// legacy fields
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func authHeaders(env Env, apikey string) map[string]string {
	return map[string]string{
		"apikey":       apikey,
		"Content-Type": "application/json;charset=UTF-8",
	}
}

// signUpAnonymous: POST /auth/v1/signup with an empty body (auth-js sends {"data":{},"gotrue_meta_security":{}}).
func signUpAnonymous(ctx context.Context, env Env, apikey string, label string, extraHeaders map[string]string, body any) (Session, Resp) {
	h := authHeaders(env, apikey)
	for k, v := range extraHeaders {
		h[k] = v
	}
	if body == nil {
		body = map[string]any{"data": map[string]any{}, "gotrue_meta_security": map[string]any{}}
	}
	r := do(ctx, label, "POST", env.APIURL+"/auth/v1/signup", h, body)
	var s Session
	if r.Status == 200 {
		_ = json.Unmarshal(r.Body, &s)
	}
	return s, r
}

func refresh(ctx context.Context, env Env, apikey, refreshToken, label string) (Session, Resp) {
	r := do(ctx, label, "POST", env.APIURL+"/auth/v1/token?grant_type=refresh_token", authHeaders(env, apikey),
		map[string]string{"refresh_token": refreshToken})
	var s Session
	if r.Status == 200 {
		_ = json.Unmarshal(r.Body, &s)
	}
	return s, r
}

func refreshQuiet(ctx context.Context, env Env, apikey, refreshToken string) (Session, Resp) {
	r := doQuiet(ctx, "POST", env.APIURL+"/auth/v1/token?grant_type=refresh_token", authHeaders(env, apikey),
		map[string]string{"refresh_token": refreshToken})
	var s Session
	if r.Status == 200 {
		_ = json.Unmarshal(r.Body, &s)
	}
	return s, r
}

func logout(ctx context.Context, env Env, apikey, accessToken, scope, label string) Resp {
	h := authHeaders(env, apikey)
	h["Authorization"] = "Bearer " + accessToken
	url := env.APIURL + "/auth/v1/logout"
	if scope != "" {
		url += "?scope=" + scope
	}
	return do(ctx, label, "POST", url, h, nil)
}

// jwtClaims decodes the JWT payload without verification (the adapter only needs exp/sub/role).
func jwtClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil
	}
	b, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil
	}
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

func printClaims(label, token string) {
	c := jwtClaims(token)
	b, _ := json.Marshal(c)
	fmt.Printf("%s claims: %s\n", label, string(b))
}

// mintJWT signs an HS256 token with the local stack's well-known JWT secret. Used ONLY to test
// Realtime's behaviour at token expiry with a short-lived token; never something the adapter does.
func mintJWT(secret string, claims map[string]any) string {
	hdr := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	pb, _ := json.Marshal(claims)
	payload := base64.RawURLEncoding.EncodeToString(pb)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(hdr + "." + payload))
	sig := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return hdr + "." + payload + "." + sig
}

func shortLivedTokenFor(env Env, s Session, ttl time.Duration) string {
	now := time.Now().Unix()
	c := jwtClaims(s.AccessToken)
	claims := map[string]any{
		"aud": "authenticated", "role": "authenticated", "sub": s.User.ID,
		"iss": c["iss"], "iat": now, "exp": now + int64(ttl.Seconds()),
		"is_anonymous": true, "session_id": c["session_id"], "aal": "aal1",
		"amr": []map[string]any{{"method": "anonymous", "timestamp": now}},
		"app_metadata": map[string]any{}, "user_metadata": map[string]any{},
	}
	return mintJWT(env.JWTSecret, claims)
}
