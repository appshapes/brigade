//go:build !windows

package rest

import (
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
	"github.com/appshapes/brigade/internal/protocol"
)

// TestGrammar pins the argv rules of 4.1 that every adapter shares: an
// unknown group, verb or flag is `usage`, help is `usage` with the text on
// stderr, the leading flags are --profile and --log-level only, and no
// refusal echoes the argument.
func TestGrammar(t *testing.T) {
	t.Parallel()
	r := newRig(t, "")
	for _, args := range [][]string{
		{},
		{"frobnicate"},
		{"session"},
		{"session", "--session", "x"},
		{"session", "frobnicate"},
		{"message", "frobnicate"},
		{"team", "frobnicate"},
		{"profile", "frobnicate"},
		{"describe", "--bogus"},
		{"describe", "extra"},
		{"session", "list", "--root", "/x"},
		{"--root", "/x", "describe"},
		{"--profile"},
		{"--profile=", "describe"},
		{"--log-level", "loud", "describe"},
		{"describe", "--log-level", "loud"},
		{"message", "watch-me"},
		{"team", "join", "--join-secret", "brg1.x.PLANTED"},
		{"--join_secret=brg1.x.PLANTED", "describe"},
	} {
		got := r.fails(protocol.CodeUsage, "", args...)
		if strings.Contains(got.stdout+got.stderr, "PLANTED") || strings.Contains(got.stdout+got.stderr, "loud") {
			t.Errorf("%v: the refused argument is echoed: %s %s", args, got.stdout, got.stderr)
		}
	}
	for _, args := range [][]string{{"--help"}, {"-h"}} {
		got := r.fails(protocol.CodeUsage, "", args...)
		if !strings.Contains(got.stderr, "brigade adapter rest") || !strings.Contains(got.stderr, "docs/backend-rest-v1.md") {
			t.Errorf("%v: stderr %q, want the usage text", args, got.stderr)
		}
	}

	// The leading and the trailing form of the two common flags.
	for _, args := range [][]string{
		{"--profile", "work", "--log-level=debug", "describe"},
		{"describe", "--profile", "work", "--log-level", "error"},
	} {
		profile, _ := r.ok("", args...)["profile"].(map[string]any)
		if profile["name"] != "work" {
			t.Errorf("%v: profile %v, want the profile named work", args, profile)
		}
	}
	// A profile name that would leave the profiles directory, and a log
	// level the environment got wrong, are `config`.
	r.fails(protocol.CodeConfig, "", "describe", "--profile", "../elsewhere")
	got := r.execEnv([]string{"BRIGADE_LOG_LEVEL=loud"}, "", "describe")
	if got.code != protocol.CodeConfig.Exit() {
		t.Errorf("BRIGADE_LOG_LEVEL=loud: exit %d, want config", got.code)
	}
	got = r.execEnv([]string{"BRIGADE_CONFIG_DIR=relative/dir"}, "", "describe")
	if got.code != protocol.CodeConfig.Exit() {
		t.Errorf("a relative BRIGADE_CONFIG_DIR: exit %d, want config", got.code)
	}
}

// TestProfilesAreSeparate: --profile selects another directory, with its
// own backend, credential and binding.
func TestProfilesAreSeparate(t *testing.T) {
	t.Parallel()
	b := newBackend(t)
	r := newRig(t, b.url())
	created := r.ok(`{"team_name":"ops"}`, "team", "create")
	other := r.ok(`{"team_name":"other"}`, "team", "create", "--profile", "second")
	if created["principal_ref"] == other["principal_ref"] || created["team_ref"] == other["team_ref"] {
		t.Errorf("two profiles share a principal or a team: %v %v", created, other)
	}
	if _, err := os.Stat(filepath.Join(r.cfg, "teams", "second", credentialFileName)); err != nil {
		t.Errorf("the second profile has no credential file of its own: %v", err)
	}
	if status := r.ok("", "--profile", "second", "profile", "status"); status["team_name"] != "other" {
		t.Errorf("profile status --profile second: %v", status)
	}
	if status := r.ok("", "profile", "status"); status["team_name"] != "ops" {
		t.Errorf("profile status: %v", status)
	}
}

// TestHTTPS: an https backend whose certificate the verifier does not know
// is `unavailable` with reason tls; with the CA named in SSL_CERT_FILE the
// same backend works; a file that cannot be read or holds no certificate
// is `config`.
func TestHTTPS(t *testing.T) {
	t.Parallel()
	server, err := stub.New(t.TempDir(), stub.Options{})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewTLSServer(server)
	t.Cleanup(srv.Close)
	r := newRig(t, srv.URL)

	got := r.fails(protocol.CodeUnavailable, `{"team_name":"ops"}`, "team", "create")
	if details(t, got.stdout)["reason"] != reasonTLS {
		t.Errorf("an unknown certificate: details %v, want reason tls", details(t, got.stdout))
	}

	dir := t.TempDir()
	ca, empty := filepath.Join(dir, "ca.pem"), filepath.Join(dir, "empty.pem")
	block := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw})
	if err := os.WriteFile(ca, block, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(empty, []byte("not a certificate\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{empty, filepath.Join(dir, "missing.pem")} {
		refused := r.execEnv([]string{"SSL_CERT_FILE=" + bad}, `{"team_name":"ops"}`, "team", "create")
		if refused.code != protocol.CodeConfig.Exit() {
			t.Errorf("SSL_CERT_FILE=%s: exit %d, want config", filepath.Base(bad), refused.code)
		}
	}
	created := r.execEnv([]string{"SSL_CERT_FILE=" + ca}, `{"team_name":"ops"}`, "team", "create")
	if created.code != 0 {
		t.Fatalf("team create with the CA in SSL_CERT_FILE: exit %d (%s)", created.code, created.stdout)
	}
	listed := r.execEnv([]string{"SSL_CERT_DIR=" + dir}, "", "session", "list")
	if listed.code != 0 {
		t.Errorf("session list with the CA in SSL_CERT_DIR: exit %d (%s)", listed.code, listed.stdout)
	}
}

// TestASlowBackendIsATimeout: a request that outlives its deadline is
// `unavailable` with reason timeout. The command's own deadline is 20 s,
// so the test drives the request seam with a short one.
func TestASlowBackendIsATimeout(t *testing.T) {
	t.Parallel()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })
	r := newRig(t, "")
	r.ok("", "profile", "init", "--url", srv.URL)

	c := &command{environ: r.env, cfgDir: r.cfg, profileName: "default", stderr: &syncBuffer{}}
	if err := c.applyLevel(); err != nil {
		t.Fatal(err)
	}
	p, _, err := c.loadProfile()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.connect(p); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = c.do(ctx, &request{path: pathSessionList})
	perr := asProtocolError(err)
	if perr.Code != protocol.CodeUnavailable || perr.Details["reason"] != reasonTimeout {
		t.Errorf("a request past its deadline: %s %v, want unavailable with reason timeout", perr.Code, perr.Details)
	}
}
