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
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
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

// A restBackend is a running server for the REST contract: its base url,
// when it serves TLS the pin of its certificate, and when it keeps files
// the directory they are in (the server's data and its log; the stand-in
// keeps none).
type restBackend struct {
	url         string
	certificate string
	dir         string
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
	return startServerOn(t, bin, t.TempDir())
}

// startServerOn is startServer on a directory the caller made: the server's data are in its `data`, so a team
// taken in there before the start is served by it.
func startServerOn(t *testing.T, bin, dir string) restBackend {
	t.Helper()
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
	return restBackend{url: url, certificate: pin, dir: dir}
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
// `rest` adapter. bob's sessions read bob's own store, and a session that
// carries an environment of its own (a third member's store) has it last.
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
	if s.stateHome != "" {
		env = append(env, "XDG_STATE_HOME="+s.stateHome)
	}
	return append(env, s.extraEnv...)
}

// terminalEnv is a member's own terminal: no session, and that member's
// store. Team administration is run there: `team revoke-member` and `team
// transfer` refuse inside a session by design.
func (r *rig) terminalEnv(configDir string) []string {
	return append(r.baseEnv(), "BRIGADE_CONFIG_DIR="+configDir)
}

// roster is what `brigade team members --json` answers, as far as the
// administration below reads it.
type roster struct {
	Members []struct {
		PrincipalRef        string `json:"principal_ref"`
		HumanLabel          string `json:"human_label"`
		JoinedSecretVersion int    `json:"joined_secret_version"`
	} `json:"members"`
}

// rosterOf reads the team's roster from a terminal of the member whose
// store configDir is, keyed by human_label.
func (r *rig) rosterOf(configDir string) map[string]struct {
	ref     string
	version int
} {
	r.t.Helper()
	res := r.runIn(r.checkout, r.terminalEnv(configDir), "", "team", "members", "--json")
	if res.exit != 0 {
		r.t.Fatalf("team members --json: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	out := map[string]struct {
		ref     string
		version int
	}{}
	for _, m := range jsonResult[roster](r.t, res.stdout).Members {
		out[m.HumanLabel] = struct {
			ref     string
			version int
		}{m.PrincipalRef, m.JoinedSecretVersion}
	}
	return out
}

// administerTheTeam is the team's administration through the built `brigade`
// and its bundled `rest` adapter (work item 274): the owner rotates the
// join secret to a file, a third member joins with that file and cannot
// with the one the team was created with, the owner revokes that member,
// and the owner hands the team to bob, who then administers it and the
// first owner no longer does. Every administration command is run from a
// terminal, and the two that refuse inside a session are shown to refuse
// there.
func (r *rig) administerTheTeam(alice session) {
	t := r.t
	t.Helper()
	const carolLabel = "carol@example.com"
	created, err := os.ReadFile(r.secretFile)
	if err != nil {
		t.Fatal(err)
	}

	// --- the owner rotates the secret to a file -------------------------
	rotatedFile := filepath.Join(r.dirs.Root, "rotated.secret")
	res := r.runIn(r.checkout, r.terminalEnv(r.configDir), "", "team", "rotate-secret", "--secret-file", rotatedFile)
	if res.exit != 0 {
		t.Fatalf("team rotate-secret: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	rotation := jsonResult[struct {
		TeamRef       string `json:"team_ref"`
		SecretVersion int    `json:"secret_version"`
		SecretFile    string `json:"secret_file"`
	}](t, res.stdout)
	if rotation.TeamRef != r.teamRef || rotation.SecretVersion != 2 || rotation.SecretFile != rotatedFile {
		t.Errorf("team rotate-secret: %+v, want this team, version 2 and the file named", rotation)
	}
	rotated, err := os.ReadFile(rotatedFile)
	if err != nil {
		t.Fatalf("the rotated secret's file: %v", err)
	}
	if fi, err := os.Stat(rotatedFile); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the rotated secret's file: %v %v, want mode 0600", fi, err)
	}
	secret := strings.TrimSpace(string(rotated))
	if secret == "" || secret == strings.TrimSpace(string(created)) || !strings.Contains(secret, "."+r.teamRef+".") {
		t.Errorf("the rotated secret's file does not hold a new secret of this team")
	}
	if strings.Contains(res.stdout+res.stderr, secret) {
		t.Errorf("team rotate-secret printed the new secret")
	}

	// --- a third member joins: not with the first file, with the new one --
	carolDir := filepath.Join(r.dirs.Root, "config-carol")
	if err := os.MkdirAll(carolDir, 0o700); err != nil {
		t.Fatal(err)
	}
	carol := r.newSession("carol", "carol-session", "", "", "")
	carol.extraEnv = []string{config.OptionConfigDir + "=" + carolDir}
	r.restHook(carol, "session-start", carol.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	join := func(file string) result {
		return r.runIn(r.checkout, r.restEnv(carol), "", "team", "join", "--secret-file", file, "--label", carolLabel)
	}
	if res := join(r.secretFile); res.exit == 0 {
		t.Fatalf("a join with the secret the team had before the rotation succeeded\nstdout: %s", res.stdout)
	}
	if res := join(rotatedFile); res.exit != 0 {
		t.Fatalf("team join with the rotated secret: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	members := r.rosterOf(r.configDir)
	if len(members) != 3 || members[aliceLabel].version != 1 || members[bobLabel].version != 1 || members[carolLabel].version != 2 {
		t.Fatalf("the roster after the rotation and the join: %+v, want alice and bob at version 1 and carol at 2", members)
	}
	carolRef, bobRef := members[carolLabel].ref, members[bobLabel].ref

	// --- the owner revokes that member -----------------------------------
	if res := r.runIn(r.checkout, r.restEnv(alice), "", "team", "revoke-member", "--principal", carolRef); res.exit != 2 {
		t.Errorf("team revoke-member inside a session: exit %d, want the usage refusal\nstdout: %s", res.exit, res.stdout)
	}
	res = r.runIn(r.checkout, r.terminalEnv(r.configDir), "", "team", "revoke-member", "--principal", carolRef)
	if res.exit != 0 {
		t.Fatalf("team revoke-member: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	revoked := jsonResult[struct {
		PrincipalRef string `json:"principal_ref"`
		Status       string `json:"status"`
		Changed      bool   `json:"changed"`
	}](t, res.stdout)
	if revoked.PrincipalRef != carolRef || revoked.Status != "revoked" || !revoked.Changed {
		t.Errorf("team revoke-member: %+v, want carol revoked", revoked)
	}
	if left := r.rosterOf(r.configDir); len(left) != 2 || left[carolLabel].ref != "" {
		t.Errorf("the roster after the revocation: %+v, want alice and bob", left)
	}
	if res := r.runIn(r.checkout, r.terminalEnv(carolDir), "", "team", "members"); res.exit != 5 {
		t.Errorf("a revoked member's team members: exit %d, want unauthorized\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}

	// --- the owner hands the team to bob ---------------------------------
	if res := r.runIn(r.checkout, r.restEnv(alice), "", "team", "transfer", "--principal", bobRef); res.exit != 2 {
		t.Errorf("team transfer inside a session: exit %d, want the usage refusal\nstdout: %s", res.exit, res.stdout)
	}
	// The request as a document on stdin this time, the form a script uses:
	// the pass-through hands the adapter the terminal's own stdin.
	res = r.runIn(r.checkout, r.terminalEnv(r.configDir), `{"principal_ref":"`+bobRef+`"}`, "team", "transfer")
	if res.exit != 0 {
		t.Fatalf("team transfer: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	moved := jsonResult[struct {
		PrincipalRef string `json:"principal_ref"`
		Transferred  bool   `json:"transferred"`
	}](t, res.stdout)
	if moved.PrincipalRef != bobRef || !moved.Transferred {
		t.Errorf("team transfer: %+v, want the team with bob", moved)
	}
	// The first owner is a member still, and no longer administers.
	refused := filepath.Join(r.dirs.Root, "refused.secret")
	if res := r.runIn(r.checkout, r.terminalEnv(r.configDir), "", "team", "rotate-secret", "--secret-file", refused); res.exit != 5 {
		t.Errorf("team rotate-secret by the previous owner: exit %d, want unauthorized\nstdout: %s", res.exit, res.stdout)
	}
	if _, err := os.Stat(refused); err == nil {
		t.Errorf("a refused rotation wrote its secret file")
	}
	if still := r.rosterOf(r.configDir); len(still) != 2 {
		t.Errorf("the previous owner's roster: %+v, want alice and bob", still)
	}
	// bob does.
	bobFile := filepath.Join(r.dirs.Root, "bob.secret")
	res = r.runIn(r.checkout, r.terminalEnv(r.configDirB), "", "team", "rotate-secret", "--secret-file", bobFile)
	if res.exit != 0 {
		t.Fatalf("team rotate-secret by the new owner: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if again := jsonResult[struct {
		SecretVersion int `json:"secret_version"`
	}](t, res.stdout); again.SecretVersion != 3 {
		t.Errorf("the new owner's rotation: version %d, want 3", again.SecretVersion)
	}
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

// restJoin is a member's join from inside a session, in the checkout, with the rig's secret file: what
// `/brigade:join <path>` runs.
func (r *rig) restJoin(s session, label string) result {
	r.t.Helper()
	return r.runIn(r.checkout, r.restEnv(s), "", "team", "join", "--secret-file", r.secretFile, "--label", label)
}

// createTeamOnServer is the admin's `team create --adapter rest` in the
// rig's checkout, outside any session, with the server's pin when it has
// one: the team file it writes names the server and the team, the secret
// is in the rig's secret file, and alice is the team's first member.
func (r *rig) createTeamOnServer(backend restBackend) {
	t := r.t
	t.Helper()
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
}

// TestATeamOnAServerThroughTheRealBinary is the path a team on a Brigade
// server takes, with the built `brigade` binary and its bundled `rest`
// adapter as real processes (work item 89, phase 3): the admin's `team
// create --adapter rest`, the committed team file, a member's join from
// inside a session, both sessions' SessionStart, the real watcher long
// polling the server, and a message from one member's session arriving in
// the other's inbox socket. Then the team's administration (work item
// 274): a rotated secret, a member revoked, the team handed on.
func TestATeamOnAServerThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	backend := startRestBackend(t)
	r := newRig(t)
	r.secretFile = filepath.Join(r.dirs.Root, "team.secret")

	// --- 1. the admin creates the team, outside any session ---------------
	r.createTeamOnServer(backend)

	// --- 2. the admin's session starts: the bundled adapter, the watcher ---
	sock := fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, sock.Path(), token)
	bob := r.newSession("bob", bobName, "", "", "")
	start := func(s session) result {
		return r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	}
	res := start(alice)
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
	res = r.restJoin(bob, bobLabel)
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

	// --- 7. the team is administered, from a terminal ---------------------
	r.administerTheTeam(alice)

	// The session's messaging token is in no file the run left behind.
	if hits := tokenHits(t, token, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the messaging token is in %v", hits)
	}
}

// TestATeamMovesToAServerWithItsSecretThroughTheRealBinary is what a team that lives on another backend goes
// through when it moves to a Brigade server and keeps its join secret (work item 89, the plan's section 4.7):
// the operator takes the team in with the secret it already has, the repository's team file is pointed at the
// server with the team's reference unchanged, and each member's machine, which was joined to the team on the
// old backend, joins the server once with the secret file it already holds. It needs the server itself, so it
// runs only where `make server-test` names one.
func TestATeamMovesToAServerWithItsSecretThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	bin := os.Getenv(serverBinVar)
	if bin == "" {
		t.Skip("needs a built brigade-server: make server-test names it in " + serverBinVar)
	}
	r := newRig(t)

	// --- 1. the team as it is on the old backend: a reference, a name, a secret every member has ---------
	// The reference has a Supabase project's form, a UUID, and the secret is put together here: no file of
	// the repository holds the shape of one.
	const teamRef = "6f0f2b41-0f0f-4f0f-8f0f-0f0f0f0f0f0f"
	const oldURL = "https://old.supabase.example"
	secret := strings.Join([]string{"brg1", teamRef, strings.Repeat("ab", 16)}, ".")
	r.secretFile = filepath.Join(r.dirs.Root, "team.secret")
	if err := os.WriteFile(r.secretFile, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// --- 2. the operator takes the team in, with the server stopped, then starts it ---------------------
	dir := t.TempDir()
	//nolint:gosec // G204: the server binary the caller named; no shell is involved
	taken, err := exec.CommandContext(t.Context(), bin, "--data", filepath.Join(dir, "data"), "--log", "file",
		"--import-team", teamName, "--join-secret-file", r.secretFile).CombinedOutput()
	if err != nil {
		t.Fatalf("taking the team in: %v\n%s", err, taken)
	}
	if !strings.Contains(string(taken), `took in team "`+teamName+`" (`+teamRef+`)`) || strings.Contains(string(taken), secret) {
		t.Fatalf("what the import said: %s", taken)
	}
	backend := startServerOn(t, bin, dir)

	// --- 3. each member's machine is joined to the team on the old backend ------------------------------
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	oldKey := teamstore.Key("supabase", oldURL, teamRef)
	for _, configDir := range []string{r.configDir, r.configDirB} {
		if err := write.EnsureBinding(configDir, oldKey, "supabase", oldURL, "sb_publishable_x", "", teamRef, teamName, "p-old"); err != nil {
			t.Fatal(err)
		}
		if err := write.Pin(configDir, canon, teamstore.Pin{
			Adapter: "supabase", URL: oldURL, PublishableKey: "sb_publishable_x", TeamRef: teamRef,
		}); err != nil {
			t.Fatal(err)
		}
	}

	// --- 4. the repository's team file is pointed at the server; the reference stays -------------------
	file, err := json.Marshal(map[string]any{
		"version": 1, "adapter": "rest", "url": backend.url, "certificate": backend.certificate,
		"team_ref": teamRef, "team_name": teamName,
	})
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // G306: a committed team file is 0644
	if err := os.WriteFile(filepath.Join(r.checkout, teamfile.FileName), append(file, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	r.teamRef, r.teamKey = teamRef, teamstore.Key("rest", backend.url, teamRef)

	// --- 5. a session attaches to nothing until its member joins once, with the file they have ---------
	sock := fakesock.New(t)
	token := "cc-messaging-secret-MUST-NOT-LEAK-" + testutil.RunID()
	alice := r.newSession("alice", "", aliceName, sock.Path(), token)
	bob := r.newSession("bob", bobName, "", "", "")
	start := func(s session) result {
		return r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	}
	for _, member := range []struct {
		session session
		label   string
	}{{alice, aliceLabel}, {bob, bobLabel}} {
		res := start(member.session)
		if r.mapExists(member.session) || !strings.Contains(res.stdout, "does not match the team you joined here") {
			t.Fatalf("a session of a member who has not moved: stdout %q, map %v", res.stdout, r.mapExists(member.session))
		}
		res = r.restJoin(member.session, member.label)
		if res.exit != 0 {
			t.Fatalf("team join: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
		if !strings.Contains(res.stdout, `join team "`+teamName+`" (`+teamRef+`)`) || !strings.Contains(res.stdout, `via adapter "rest"`) ||
			strings.Contains(res.stdout+res.stderr, secret) {
			t.Errorf("the join's consent line: %q", res.stdout)
		}
		start(member.session)
		m := r.mustMap(member.session)
		if m.TeamRef != teamRef || m.TeamName != teamName || m.TeamKey != r.teamKey || !sessionmap.IsBundledRest(m.AdapterCommand) {
			t.Fatalf("the member's map after the join: %+v", m)
		}
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(alice, "watch ready") })

	// --- 6. the team works as it did: a message from one member's session reaches the other's ----------
	aliceMap, bobMap := r.mustMap(alice), r.mustMap(bob)
	res := r.mustRun(r.restEnv(bob), "we are on the server now", "send", aliceMap.BrigadeSessionID, "--summary", "moved", "--json")
	sent := jsonResult[sendResult](t, res.stdout)
	frames := sock.WaitFrames(1, waitLong)
	p, err := frame.Parse(frames[0])
	if err != nil {
		t.Fatalf("the injected frame does not parse: %v\n%s", err, frames[0])
	}
	if p.MessageID != sent.MessageID || p.ReplyToSessionID != bobMap.BrigadeSessionID || p.Team != teamName ||
		p.FromLabel != bobLabel+" (unverified)" || p.Body != "we are on the server now" {
		t.Errorf("frame: %+v", p)
	}

	// --- 7. the member's other checkouts of the team, on the same machine (work item 290) --------------
	// One was joined to the team on the old backend, so its pin names that backend. The machine holds the
	// server's credential by now, and the join there is a move to another backend all the same: it shows
	// what changed and asks for the secret's file, and with the file the member joins as who they are. The
	// other was never joined and needs no file. `docs/project-owner/server/take-in-a-team.md` says both.
	member, err := teamstore.LoadBinding(r.configDir, r.teamKey)
	if err != nil {
		t.Fatal(err)
	}
	second, fresh := filepath.Join(r.dirs.Root, "checkout-second"), filepath.Join(r.dirs.Root, "checkout-fresh")
	for _, dir := range []string{second, fresh} {
		if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o700); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // G306: a committed team file is 0644
		if err := os.WriteFile(filepath.Join(dir, teamfile.FileName), append(file, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	canonSecond, err := teamfile.Canonicalize(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(r.configDir, canonSecond, teamstore.Pin{
		Adapter: "supabase", URL: oldURL, PublishableKey: "sb_publishable_x", TeamRef: teamRef,
	}); err != nil {
		t.Fatal(err)
	}
	res = r.runIn(second, r.restEnv(alice), "", "team", "join")
	if res.exit == 0 || !strings.Contains(res.stderr, "team join inside a session needs --secret-file <path>") ||
		!strings.Contains(res.stdout, "adapter: supabase → rest") || strings.Contains(res.stdout, "consented") {
		t.Fatalf("a join with no path in a checkout pinned to the old backend: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if pin, ok, _ := teamstore.LookupPin(r.configDir, canonSecond); !ok || pin.Adapter != "supabase" {
		t.Fatalf("the pin moved without the secret's file: %+v", pin)
	}
	res = r.runIn(second, r.restEnv(alice), "", "team", "join", "--secret-file", r.secretFile)
	if res.exit != 0 || strings.Count(res.stdout, "consented by this invocation") != 1 || strings.Contains(res.stdout+res.stderr, secret) {
		t.Fatalf("the join with the path: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if pin, ok, _ := teamstore.LookupPin(r.configDir, canonSecond); !ok || pin.Adapter != "rest" || pin.URL != backend.url || pin.TeamRef != teamRef {
		t.Fatalf("the pin after the join with the path: %+v", pin)
	}
	if again, err := teamstore.LoadBinding(r.configDir, r.teamKey); err != nil || again.PrincipalRef != member.PrincipalRef {
		t.Fatalf("the member after the second checkout's join: %+v, %v: want principal %s", again, err, member.PrincipalRef)
	}
	res = r.runIn(fresh, r.restEnv(alice), "", "team", "join")
	if res.exit != 0 || !strings.Contains(res.stdout, `re-consented team "`+teamName+`" (`+teamRef+`)`) {
		t.Fatalf("a join with no path in a checkout that was never joined: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	canonFresh, err := teamfile.Canonicalize(fresh)
	if err != nil {
		t.Fatal(err)
	}
	if pin, ok, _ := teamstore.LookupPin(r.configDir, canonFresh); !ok || pin.Adapter != "rest" || pin.URL != backend.url {
		t.Fatalf("the pin of the checkout that was never joined: %+v, %v", pin, ok)
	}

	entry := r.liveWatcher(alice)
	for _, s := range []session{alice, bob} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(entry.PID) })
	// Neither the join secret nor the session's messaging token is in any file the run left behind but the
	// secret's own.
	for _, hidden := range []string{secret, token} {
		for _, hit := range tokenHits(t, hidden, r.dirs.Root) {
			if hit != r.secretFile {
				t.Errorf("a secret of the run is in %s", hit)
			}
		}
	}
}
