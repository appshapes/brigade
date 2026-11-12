//go:build !windows

package e2e

import (
	"bufio"
	"context"
	"crypto/x509"
	"encoding/json/v2"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// serverBinVar names a built `brigade-server` (work item 89, server/). With
// it set, the test below runs against that server, over TLS, with the
// certificate the server makes for itself and its pin in the team file;
// `make server-test` sets it. Without it the backend is the stand-in server
// (cmd/brigade-rest-stub) over loopback HTTP, which is what `make test`
// runs: no Rust, no certificate.
const serverBinVar = "BRIGADE_E2E_SERVER"

// A restBackend is a running server for the REST contract: its base url
// and, when it serves TLS, the pin of its certificate.
type restBackend struct {
	url         string
	certificate string
}

// startRestBackend starts the backend the environment asks for and stops
// it when the test ends. The server's context is not the test's: the test's
// is cancelled before its cleanups run, and the cleanup here is what stops
// the server, with the signal an operator would send.
func startRestBackend(t *testing.T) restBackend {
	t.Helper()
	if bin := os.Getenv(serverBinVar); bin != "" {
		return startServer(t, bin)
	}
	return startStub(t)
}

// startStub runs the stand-in server, which prints its base url on one
// line and serves until it is interrupted.
func startStub(t *testing.T) restBackend {
	t.Helper()
	//nolint:gosec // G204: a binary this test built; no shell is involved
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), testutil.Build(t, "./cmd/brigade-rest-stub"))
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "TMPDIR=" + t.TempDir()}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	stopOnCleanup(t, cmd)
	line := make(chan string, 1)
	go func() {
		text, _ := bufio.NewReader(stdout).ReadString('\n')
		line <- strings.TrimSpace(text)
	}()
	select {
	case url := <-line:
		if !strings.HasPrefix(url, "http://127.0.0.1:") {
			t.Fatalf("the stand-in server printed %q, want its loopback base url", url)
		}
		return restBackend{url: url}
	case <-time.After(waitLong):
		t.Fatal("the stand-in server printed no base url")
		return restBackend{}
	}
}

// startServer runs a built brigade-server on a data directory of its own,
// serving TLS with the certificate it makes for itself (--tls-self-signed):
// one that names no address and that no authority signed, so the only
// thing that can make a member trust it is its pin. This is the start an
// operator with no certificate of their own makes.
func startServer(t *testing.T, bin string) restBackend {
	t.Helper()
	dir := t.TempDir()
	data, addressFile := filepath.Join(dir, "data"), filepath.Join(dir, "address")
	log, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = log.Close() })
	// The file log: what is under test here is the member's side and the
	// listener, and the server's own tests hold the Cavalry feed.
	//nolint:gosec // G204: the server binary the caller named; no shell is involved
	cmd := exec.CommandContext(context.WithoutCancel(t.Context()), bin, "--data", data, "--listen", "127.0.0.1:0", "--log", "file",
		"--tls-self-signed", "--address-file", addressFile)
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatalf("%s: %v", bin, err)
	}
	stopOnCleanup(t, cmd)
	said := func() string {
		text, _ := os.ReadFile(log.Name())
		return string(text)
	}
	var url string
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		text, err := os.ReadFile(addressFile)
		url = strings.TrimSpace(string(text))
		return err == nil && url != ""
	})
	if !strings.HasPrefix(url, "https://127.0.0.1:") {
		t.Fatalf("the server's address file holds %q, want an https loopback url\n%s", url, said())
	}
	// The pin is the fingerprint of the certificate the server made, and the
	// server's start line says the same one: that line is where an operator
	// copies it from.
	text, err := os.ReadFile(filepath.Join(data, "tls", "certificate.pem"))
	if err != nil {
		t.Fatalf("the server's own certificate: %v\n%s", err, said())
	}
	block, _ := pem.Decode(text)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatalf("the server's own certificate is not a PEM certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		t.Fatalf("the server's own certificate does not parse: %v", err)
	}
	pin := adapterkit.CertificateFingerprint(block.Bytes)
	testutil.Eventually(t, waitLong, pollEvery, func() bool {
		return strings.Contains(said(), "(certificate "+pin+")")
	})
	return restBackend{url: url, certificate: pin}
}

// stopOnCleanup interrupts a server process when the test ends and waits
// for it.
func stopOnCleanup(t *testing.T, cmd *exec.Cmd) {
	t.Helper()
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(waitLong):
			_ = cmd.Process.Kill()
			<-done
		}
	})
}

// restEnv is the hook environment of a session of a team on a server: the
// rig's base environment and the session facts, and no adapter_command
// option, because the team file's adapter name is what selects the bundled
// `rest` adapter. bob's sessions read bob's own store.
func (r *rig) restEnv(s session) []string {
	env := append(r.baseEnv(),
		"CLAUDE_PID="+strconv.Itoa(s.pid),
		"CLAUDE_CODE_SESSION_ID="+s.nativeID,
		"CLAUDECODE=1",
		"CLAUDE_CODE_ENTRYPOINT=cli",
	)
	if s.profile == "bob" {
		env = append(env, config.OptionConfigDir+"="+r.configDirB)
	}
	if s.socket != "" {
		env = append(env, "CLAUDE_CODE_MESSAGING_SOCKET="+s.socket, "CLAUDE_CODE_MESSAGING_TOKEN="+s.token)
	}
	return env
}

// restHook runs one lifecycle hook for a session of a team on a server.
func (r *rig) restHook(s session, sub, doc string) result {
	r.t.Helper()
	res := r.run(r.restEnv(s), doc, "hook", sub)
	r.mu.Lock()
	r.lastHook[s.pid] = res
	r.mu.Unlock()
	if res.exit != 0 {
		r.t.Fatalf("hook %s: exit %d\nstdout: %s\nstderr: %s", sub, res.exit, res.stdout, res.stderr)
	}
	return res
}

// TestATeamOnAServerThroughTheRealBinary is the path a team on a Brigade
// server takes, with the built `brigade` binary and its bundled `rest`
// adapter as real processes (work item 89, phase 3): the admin's `team
// create --adapter rest`, the committed team file, a member's join from
// inside a session, both sessions' SessionStart, the real watcher long
// polling the server, and a message from one member's session arriving in
// the other's inbox socket.
func TestATeamOnAServerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	backend := startRestBackend(t)
	r := newRig(t)
	r.secretFile = filepath.Join(r.dirs.Root, "team.secret")

	// --- 1. the admin creates the team, outside any session ---------------
	create := []string{"team", "create", "--adapter", "rest", "--url", backend.url,
		"--name", teamName, "--label", aliceLabel, "--secret-file", r.secretFile}
	if backend.certificate != "" {
		// As an operator copies it from openssl: uppercase, a colon between bytes.
		digits := strings.ToUpper(strings.TrimPrefix(backend.certificate, "sha256:"))
		var pairs []string
		for i := 0; i < len(digits); i += 2 {
			pairs = append(pairs, digits[i:i+2])
		}
		create = append(create, "--certificate", strings.Join(pairs, ":"))
	}
	res := r.runIn(r.checkout, r.baseEnv(), "", create...)
	if res.exit != 0 {
		t.Fatalf("team create: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	file, err := teamfile.Parse(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if file.Adapter != "rest" || file.URL != backend.url || file.Certificate != backend.certificate ||
		file.PublishableKey != "" || file.TeamName != teamName || len(file.Ignored) != 0 {
		t.Fatalf("the team file: %+v", file)
	}
	r.teamRef, r.teamKey = file.TeamRef, teamstore.Key("rest", backend.url, file.TeamRef)
	if fi, err := os.Stat(r.secretFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("the join secret's file: %v %v", fi, err)
	}

	// --- 2. the admin's session starts: the bundled adapter, the watcher ---
	sock := fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, sock.Path(), token)
	bob := r.newSession("bob", bobName, "", "", "")
	start := func(s session) result {
		return r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	}
	res = start(alice)
	m := r.mustMap(alice)
	if m.BrigadeSessionID == "" || m.TeamRef != r.teamRef || m.TeamName != teamName || m.SessionName != aliceName ||
		m.TeamKey != r.teamKey || !sessionmap.IsBundledRest(m.AdapterCommand) {
		t.Fatalf("alice's map: %+v", m)
	}
	if !strings.HasPrefix(res.stdout, "Brigade: this session is \""+aliceName+"\" ("+m.BrigadeSessionID+") in team \""+teamName+"\"") {
		t.Errorf("session-start stdout: %q", res.stdout)
	}
	entry := r.liveWatcher(alice)
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(alice, "watch ready") })

	// --- 3. a member joins from inside a session, then is attached --------
	res = start(bob)
	if r.mapExists(bob) || !strings.Contains(res.stdout, "team join") {
		t.Fatalf("a session of a member who has not joined: stdout %q, map %v", res.stdout, r.mapExists(bob))
	}
	res = r.runIn(r.checkout, r.restEnv(bob), "", "team", "join", "--secret-file", r.secretFile, "--label", bobLabel)
	if res.exit != 0 {
		t.Fatalf("team join: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	consent := "join team \"" + teamName + "\" (" + r.teamRef + ") at " + strings.TrimPrefix(strings.TrimPrefix(backend.url, "https://"), "http://") + " via adapter \"rest\""
	if backend.certificate != "" {
		consent += ", trusting the server certificate " + backend.certificate
	}
	if !strings.Contains(res.stdout, consent+" — consented by this invocation") {
		t.Errorf("the join's consent line: %q, want %q", res.stdout, consent)
	}
	binding, err := teamstore.LoadBinding(r.configDirB, r.teamKey)
	if err != nil || binding.Adapter != "rest" || binding.URL != backend.url || binding.Certificate != backend.certificate || binding.TeamRef != r.teamRef {
		t.Fatalf("bob's binding: %+v %v", binding, err)
	}
	start(bob)
	bobMap := r.mustMap(bob)
	if bobMap.SessionName != bobName || bobMap.TeamKey != r.teamKey || !sessionmap.IsBundledRest(bobMap.AdapterCommand) {
		t.Fatalf("bob's map: %+v", bobMap)
	}

	// --- 4. bob's session sends; the frame reaches alice's socket ---------
	body := "the tenant_id migration has landed\nsecond line"
	res = r.mustRun(r.restEnv(bob), body, "send", m.BrigadeSessionID, "--summary", "migration completed", "--json")
	sent := jsonResult[sendResult](t, res.stdout)
	if sent.MessageID == "" || sent.RecipientSessionID != m.BrigadeSessionID || sent.HopCount != 0 || sent.Duplicate {
		t.Fatalf("send: %+v", sent)
	}
	frames := sock.WaitFrames(1, waitLong)
	p, err := frame.Parse(frames[0])
	if err != nil {
		t.Fatalf("the injected frame does not parse: %v\n%s", err, frames[0])
	}
	if !p.Wrapped || p.MessageID != sent.MessageID || p.ReplyToSessionID != bobMap.BrigadeSessionID || p.Team != teamName ||
		p.FromName != bobName || p.FromLabel != bobLabel+" (unverified)" || p.Hops != "0" ||
		p.Summary != "migration completed" || p.Body != body {
		t.Errorf("frame: %+v\n%s", p, frames[0])
	}

	// --- 5. the reply, the roster and whoami, through the same adapter ----
	short := bobMap.BrigadeSessionID[len(bobMap.BrigadeSessionID)-5:]
	res = r.mustRun(r.restEnv(alice), "on it", "send", short, "--reply-to", sent.MessageID, "--json")
	if reply := jsonResult[sendResult](t, res.stdout); reply.HopCount != 1 || reply.RecipientSessionID != bobMap.BrigadeSessionID {
		t.Errorf("reply: %+v, want hop 1 to bob's id in full", reply)
	}
	res = r.mustRun(r.restEnv(alice), "", "sessions", "--json")
	var roster struct {
		Result struct {
			Sessions []struct {
				SessionName string `json:"session_name"`
			} `json:"sessions"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(res.stdout), &roster); err != nil {
		t.Fatalf("sessions --json: %v\n%s", err, res.stdout)
	}
	var names []string
	for _, s := range roster.Result.Sessions {
		names = append(names, s.SessionName)
	}
	if got := strings.Join(names, ","); !strings.Contains(got, aliceName) || !strings.Contains(got, bobName) {
		t.Errorf("the roster names %q, want both sessions", got)
	}
	res = r.mustRun(r.restEnv(alice), "", "whoami")
	if !strings.Contains(res.stdout, "(adapter brigade-adapter-rest ") {
		t.Errorf("whoami does not name the rest adapter:\n%s", res.stdout)
	}
	// The watcher that delivered is the one the hook started.
	if again := r.liveWatcher(alice); again.PID != entry.PID {
		t.Errorf("the watcher changed from %d to %d", entry.PID, again.PID)
	}

	// --- 6. the sessions end ----------------------------------------------
	for _, s := range []session{alice, bob} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(entry.PID) })
	// The session's messaging token is in no file the run left behind.
	if hits := tokenHits(t, token, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the messaging token is in %v", hits)
	}
}
