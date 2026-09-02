package supabase

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/appshapes/brigade/internal/protocol"
)

func base64URL(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

// TestOfflineDialerFailsLoudly: with BRIGADE_TEST_OFFLINE=1 every request
// and every websocket dial fails with unavailable/offline before any
// connection is attempted (5.11, C-01).
func TestOfflineDialerFailsLoudly(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	env := append(append([]string{}, r.env...), "BRIGADE_TEST_OFFLINE=1")
	cl, err := newClient(r.be.srv.URL, testKey, env, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cl.signUpAnonymous(t.Context())
	if perr := asProtocolError(err); perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != reasonOffline {
		t.Fatalf("offline sign-up = %+v", perr)
	}
	_, err = cl.dialRealtime(t.Context(), env)
	if perr := asProtocolError(err); perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != reasonOffline {
		t.Fatalf("offline dial = %+v", perr)
	}
	if r.be.total() != 0 {
		t.Fatalf("the backend was reached offline")
	}
	// The transport itself refuses, not only the client's flag.
	transport, err := newTransport(env)
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, r.be.srv.URL+"/auth/v1/health", nil)
	resp, err := transport.RoundTrip(req)
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "BRIGADE_TEST_OFFLINE") {
		t.Fatalf("offline transport dialled: %v", err)
	}
}

// TestTLSVerificationFailureIsUnavailableTLS: a backend whose certificate
// is signed by an unknown authority is unavailable/tls with the
// SSL_CERT_FILE hint, and SSL_CERT_FILE naming that certificate makes the
// same backend reachable (5.1, 5.11).
func TestTLSVerificationFailureIsUnavailableTLS(t *testing.T) {
	t.Parallel()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, `{}`)
	}))
	t.Cleanup(srv.Close)
	env := []string{"HOME=" + t.TempDir()}
	cl, err := newClient(srv.URL, testKey, env, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cl.callRPC(t.Context(), "tok", "list_sessions", nil)
	perr := asProtocolError(err)
	if perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != reasonTLS || !strings.Contains(perr.Message, "SSL_CERT_FILE") {
		t.Fatalf("unknown authority = %+v", perr)
	}

	certFile := filepath.Join(t.TempDir(), "ca.pem")
	pem := "-----BEGIN CERTIFICATE-----\n" + base64.StdEncoding.EncodeToString(srv.Certificate().Raw) + "\n-----END CERTIFICATE-----\n"
	if err := os.WriteFile(certFile, []byte(pem), 0o600); err != nil {
		t.Fatal(err)
	}
	cl, err = newClient(srv.URL, testKey, append(env, "SSL_CERT_FILE="+certFile), testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cl.callRPC(t.Context(), "tok", "list_sessions", nil)
	if err != nil || resp.status != http.StatusOK {
		t.Fatalf("with SSL_CERT_FILE: %v %+v", err, resp)
	}
	if _, err := newClient(srv.URL, testKey, append(env, "SSL_CERT_FILE="+filepath.Join(t.TempDir(), "missing.pem")), testLogger(t)); asProtocolError(err).Code != protocol.CodeConfig {
		t.Fatalf("missing SSL_CERT_FILE = %v", err)
	}
}

// TestRequestBudget: a backend that never answers is unavailable/timeout
// within the request budget, not a hang. The budget is 20 s in
// production; the test shortens the client's own timeout.
func TestRequestBudget(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		writeJSON(w, http.StatusOK, `{}`)
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	cl, err := newClient(srv.URL, testKey, []string{"HOME=" + t.TempDir()}, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	_, err = cl.callRPC(ctx, "tok", "list_sessions", nil)
	if perr := asProtocolError(err); perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != reasonTimeout {
		t.Fatalf("timeout = %+v", perr)
	}
}

// TestRealtimeURL derives the websocket endpoint of 5.6 from the backend
// url: wss for https, ws for http, the publishable key and the version in
// the query, and the test-only version switch.
func TestRealtimeURL(t *testing.T) {
	t.Parallel()
	for base, want := range map[string]string{
		"https://abc.supabase.co":  "wss://abc.supabase.co/realtime/v1/websocket?apikey=" + testKey + "&vsn=1.0.0",
		"http://127.0.0.1:54321":   "ws://127.0.0.1:54321/realtime/v1/websocket?apikey=" + testKey + "&vsn=1.0.0",
		"https://abc.supabase.co/": "wss://abc.supabase.co/realtime/v1/websocket?apikey=" + testKey + "&vsn=1.0.0",
	} {
		cl, err := newClient(base, testKey, nil, testLogger(t))
		if err != nil {
			t.Fatal(err)
		}
		got, err := cl.realtimeURL(nil)
		if err != nil || got != want {
			t.Errorf("realtimeURL(%q) = %q %v, want %q", base, got, err, want)
		}
	}
	cl, _ := newClient("https://abc.supabase.co", testKey, nil, testLogger(t))
	got, _ := cl.realtimeURL([]string{realtimeVersionVar + "=2.0.0"})
	if !strings.HasSuffix(got, "&vsn=2.0.0") {
		t.Fatalf("version switch ignored: %q", got)
	}
	cl, _ = newClient("ftp://x", testKey, nil, testLogger(t))
	if _, err := cl.realtimeURL(nil); asProtocolError(err).Code != protocol.CodeConfig {
		t.Fatalf("ftp scheme = %v", err)
	}
}

// TestDialRealtime opens a websocket against a fake Phoenix endpoint
// through the client's transport: the apikey and version arrive in the
// query, X-Client-Info in the headers, the read limit is set, and a
// refused upgrade is mapped.
func TestDialRealtime(t *testing.T) {
	t.Parallel()
	type seen struct {
		query  string
		client string
	}
	got := make(chan seen, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/realtime/v1/websocket" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if r.URL.Query().Get("apikey") == "bad" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		got <- seen{query: r.URL.RawQuery, client: r.Header.Get("X-Client-Info")}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		_, data, err := conn.Read(r.Context())
		if err != nil {
			return
		}
		var frame map[string]any
		_ = json.Unmarshal(data, &frame)
		reply, _ := json.Marshal(map[string]any{
			"topic": frame["topic"], "event": "phx_reply", "ref": frame["ref"],
			"payload": map[string]any{"status": "ok", "response": map[string]any{}},
		})
		_ = conn.Write(r.Context(), websocket.MessageText, reply)
	}))
	t.Cleanup(srv.Close)
	env := []string{"HOME=" + t.TempDir()}
	cl, err := newClient(srv.URL, testKey, env, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	conn, err := cl.dialRealtime(t.Context(), env)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.CloseNow() }()
	s := <-got
	if !strings.Contains(s.query, "apikey="+testKey) || !strings.Contains(s.query, "vsn=1.0.0") || !strings.HasPrefix(s.client, "brigade-adapter-supabase/") {
		t.Fatalf("upgrade request: %+v", s)
	}
	join, _ := json.Marshal(map[string]any{"topic": "realtime:brigade:session:x", "event": "phx_join", "ref": "1", "payload": map[string]any{}})
	if err := conn.Write(t.Context(), websocket.MessageText, join); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"phx_reply"`) {
		t.Fatalf("reply = %s", data)
	}

	cl, _ = newClient(srv.URL, "bad", env, testLogger(t))
	_, err = cl.dialRealtime(t.Context(), env)
	if perr := asProtocolError(err); perr.Code != protocol.CodeConfig || perr.Details["reason"] != reasonAPIKeyRejected {
		t.Fatalf("refused upgrade = %+v", perr)
	}
}

// TestSignOutGlobal: the sign-out carries the bearer token and the global
// scope, 204 and an already-dead token both count as done, and a 5xx is
// unavailable.
func TestSignOutGlobal(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	cl, err := newClient(r.be.srv.URL, testKey, r.env, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := cl.signOutGlobal(t.Context(), "tok"); err != nil {
		t.Fatal(err)
	}
	last := r.be.last("/auth/v1/logout")
	if last.header.Get("Authorization") != "Bearer tok" || !strings.Contains(last.path, "scope=global") {
		t.Fatalf("sign-out = %+v", last)
	}
	r.be.onLogout = func(w http.ResponseWriter, _ *http.Request) {
		authErrorNew(w, http.StatusUnauthorized, "session_not_found")
	}
	if err := cl.signOutGlobal(t.Context(), "tok"); err != nil {
		t.Fatalf("dead token = %v", err)
	}
	r.be.onLogout = func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusBadGateway, `bad`) }
	if err := cl.signOutGlobal(t.Context(), "tok"); asProtocolError(err).Code != protocol.CodeUnavailable {
		t.Fatalf("5xx = %v", err)
	}
}

// TestOversizeResponseIsInternal: a body past maxResponseBytes is
// refused rather than buffered without bound.
func TestOversizeResponseIsInternal(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		chunk := strings.Repeat("x", 1<<16)
		for written := 0; written <= maxResponseBytes; written += len(chunk) {
			if _, err := w.Write([]byte(chunk)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	cl, err := newClient(srv.URL, testKey, nil, testLogger(t))
	if err != nil {
		t.Fatal(err)
	}
	_, err = cl.callRPC(t.Context(), "tok", "fetch_inbox", nil)
	if perr := asProtocolError(err); perr.Code != protocol.CodeInternal || perr.Details["reason"] != reasonBackendResponse {
		t.Fatalf("oversize = %+v", perr)
	}
}
