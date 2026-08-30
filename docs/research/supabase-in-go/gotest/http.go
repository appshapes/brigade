package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Env holds the local stack facts from `supabase status -o env`.
type Env struct {
	APIURL         string
	PublishableKey string
	AnonJWT        string
	SecretKey      string
	ServiceRoleJWT string
	JWTSecret      string
}

func loadEnv(path string) Env {
	b, err := os.ReadFile(path)
	if err != nil {
		fatal("read env: %v", err)
	}
	m := map[string]string{}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "=") {
			continue
		}
		kv := strings.SplitN(line, "=", 2)
		m[kv[0]] = strings.Trim(kv[1], `"`)
	}
	return Env{
		APIURL:         m["API_URL"],
		PublishableKey: m["PUBLISHABLE_KEY"],
		AnonJWT:        m["ANON_KEY"],
		SecretKey:      m["SECRET_KEY"],
		ServiceRoleJWT: m["SERVICE_ROLE_KEY"],
		JWTSecret:      m["JWT_SECRET"],
	}
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "FATAL: "+format+"\n", a...)
	os.Exit(1)
}

var jwtRe = regexp.MustCompile(`eyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)
var keyRe = regexp.MustCompile(`sb_(publishable|secret)_[A-Za-z0-9_-]+`)

// redact shortens JWTs and API keys for the transcript only; requests carry the full values.
func redact(s string) string {
	s = jwtRe.ReplaceAllStringFunc(s, func(t string) string { return t[:16] + "…<jwt>" })
	s = keyRe.ReplaceAllStringFunc(s, func(t string) string { return t[:20] + "…" })
	return s
}

func section(title string) { fmt.Printf("\n### %s\n", title) }

type Resp struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
}

// do performs one HTTP request and prints the exchange verbatim (tokens shortened).
func do(ctx context.Context, label, method, url string, headers map[string]string, body any) Resp {
	var bodyBytes []byte
	if body != nil {
		switch v := body.(type) {
		case []byte:
			bodyBytes = v
		case string:
			bodyBytes = []byte(v)
		default:
			bodyBytes, _ = json.Marshal(v)
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		fatal("new request: %v", err)
	}
	keys := make([]string, 0, len(headers))
	for k := range headers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		req.Header.Set(k, headers[k])
	}
	fmt.Printf("\n--- %s\n", label)
	fmt.Printf("> %s %s\n", method, strings.TrimPrefix(url, "http://127.0.0.1:54321"))
	for _, k := range keys {
		fmt.Printf("> %s: %s\n", k, redact(headers[k]))
	}
	if bodyBytes != nil {
		fmt.Printf("> body: %s\n", redact(string(bodyBytes)))
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Printf("< transport error: %v\n", err)
		return Resp{Status: -1}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	el := time.Since(start)
	fmt.Printf("< HTTP %d %s (%.0f ms)\n", resp.StatusCode, http.StatusText(resp.StatusCode), float64(el.Microseconds())/1000)
	for _, h := range []string{"Content-Type", "WWW-Authenticate", "X-Supabase-Api-Version", "Content-Profile", "Content-Range", "Proxy-Status", "Sb-Gateway-Version", "Preference-Applied"} {
		if v := resp.Header.Get(h); v != "" {
			fmt.Printf("< %s: %s\n", h, v)
		}
	}
	fmt.Printf("< body: %s\n", redact(string(rb)))
	return Resp{Status: resp.StatusCode, Header: resp.Header, Body: rb, Elapsed: el}
}

// doQuiet performs a request without printing (used inside timing loops / races).
func doQuiet(ctx context.Context, method, url string, headers map[string]string, body any) Resp {
	var bodyBytes []byte
	if body != nil {
		bodyBytes, _ = json.Marshal(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(bodyBytes))
	if err != nil {
		fatal("new request: %v", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return Resp{Status: -1, Body: []byte(err.Error())}
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	return Resp{Status: resp.StatusCode, Header: resp.Header, Body: rb, Elapsed: time.Since(start)}
}

func jsonGet[T any](b []byte, path ...string) (T, bool) {
	var zero T
	var cur any
	if err := json.Unmarshal(b, &cur); err != nil {
		return zero, false
	}
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return zero, false
		}
		cur, ok = m[p]
		if !ok {
			return zero, false
		}
	}
	v, ok := cur.(T)
	return v, ok
}
