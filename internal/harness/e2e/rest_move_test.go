//go:build !windows

package e2e

import (
	"bytes"
	"crypto/tls"
	"encoding/json/v2"
	"errors"
	"io"
	"io/fs"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teammove"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakesock"
)

// A team's move to a Brigade server by ONE command, `brigade team move --to
// <server url> --secret-file <path>` (work item 328; ADR 0005 and ADR
// 0006), through the built `brigade`, a real adapter for the backend the
// team leaves and a real server for the one it goes to. Until this command
// the move was the server operator's two commands on a stopped server, a
// ticket handed back as a file, a team file edited by hand, a join and a
// record: the tests of rest_test.go still hold that road, which stays for a
// server that takes no team in by request.
//
// The server is the stand-in (cmd/brigade-rest-stub) under `make test` and
// the built brigade-server, over TLS with a certificate it made itself,
// where `make server-test` names one. The backend the team leaves is the fs
// adapter, which carries capability team.move as the Supabase adapter does,
// and, under `make test-move-live`, the local Supabase stack.

// terminalAccount is the address of the Claude account the administrator's
// terminal happens to be signed in to. It is not her label on the team: the
// move must never send it (work item 326).
const terminalAccount = "someone-else@example.net"

// A movingTeam is a rig whose team is about to move: what each test of this
// file needs to know of the backend the team leaves.
type movingTeam struct {
	r *rig
	// canon is the checkout's canonical directory, the key of its pin.
	canon string
	// oldKey, oldPin and oldHost are the team on the backend it leaves.
	oldKey  string
	oldPin  teamstore.Pin
	oldHost string
	// secret is the team's join secret, and fileBefore the team file as it
	// was committed.
	secret     string
	fileBefore string
	// terminal is the administrator's own terminal.
	terminal []string
	// oldAdapter runs the adapter of the backend the team leaves, as the
	// administrator: the one way a test reads a record's ticket.
	oldAdapter func(args ...string) result
	// said collects everything a `team move` of the test printed, so that
	// the secret and the ticket can be looked for in all of it at the end.
	said strings.Builder
}

// newMovingTeam reads the rig's team as it is before the move. The
// administrator's terminal is given a Claude profile that is signed in to
// another account than the one her label names.
func newMovingTeam(t *testing.T, r *rig, oldAdapter func(args ...string) result) *movingTeam {
	t.Helper()
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok, err := teamstore.LookupPin(r.configDir, canon)
	if err != nil || !ok || pin.TeamRef != r.teamRef {
		t.Fatalf("the administrator's pin before the move: %+v %v %v", pin, ok, err)
	}
	secret, err := os.ReadFile(r.secretFile)
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.ReadFile(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	doc := `{"oauthAccount":{"accountUuid":"u-terminal","emailAddress":"` + terminalAccount + `"}}`
	if err := os.WriteFile(filepath.Join(r.dirs.ClaudeConfig, account.FileName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	return &movingTeam{
		r: r, canon: canon, oldKey: r.teamKey, oldPin: *pin, oldHost: teammove.Host(pin.URL),
		secret: strings.TrimSpace(string(secret)), fileBefore: string(file),
		terminal: r.terminalEnv(r.configDir), oldAdapter: oldAdapter,
	}
}

// fsAdapterAs runs the rig's fs adapter directly, as the member whose store
// configDir is.
func (r *rig) fsAdapterAs(configDir string) func(args ...string) result {
	return func(args ...string) result {
		r.t.Helper()
		return r.runAdapter(append(r.baseEnv(), "BRIGADE_CONFIG_DIR="+configDir), "", args...)
	}
}

// moveArgs is the one command, for a server.
func moveArgs(backend restBackend, secretFile string, extra ...string) []string {
	args := []string{"team", "move", "--to", backend.url, "--secret-file", secretFile}
	if backend.certificate != "" {
		args = append(args, "--certificate", backend.certificate)
	}
	return append(args, extra...)
}

// move runs `brigade team move …` in the checkout and keeps what it said.
func (m *movingTeam) move(env []string, stdin string, args ...string) result {
	m.r.t.Helper()
	res := m.r.runIn(m.r.checkout, env, stdin, args...)
	m.said.WriteString(res.stdout + res.stderr)
	return res
}

// pinOf is the checkout's pin in a member's store.
func (m *movingTeam) pinOf(configDir string) teamstore.Pin {
	m.r.t.Helper()
	pin, ok, err := teamstore.LookupPin(configDir, m.canon)
	if err != nil || !ok {
		m.r.t.Fatalf("the pin of %s: %v %v", m.canon, ok, err)
	}
	return *pin
}

// fileNow is the checkout's team file.
func (m *movingTeam) fileNow() string {
	m.r.t.Helper()
	text, err := os.ReadFile(filepath.Join(m.r.checkout, teamfile.FileName))
	if err != nil {
		m.r.t.Fatal(err)
	}
	return string(text)
}

// record reads what the backend the team leaves says about a move, as the
// administrator reads it.
func (m *movingTeam) record() string {
	m.r.t.Helper()
	res := m.move(m.terminal, "", "team", "move", "--show", "--from", m.oldHost)
	if res.exit != 0 {
		m.r.t.Fatalf("team move --show: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	return res.stdout
}

// unmoved fails the test when a refused move left a trace: the team file
// and the administrator's pin are as they were, and the backend the team
// leaves records no move.
func (m *movingTeam) unmoved(what string) {
	m.r.t.Helper()
	if got := m.fileNow(); got != m.fileBefore {
		m.r.t.Fatalf("%s rewrote the team file:\n%s", what, got)
	}
	if pin := m.pinOf(m.r.configDir); pin != m.oldPin {
		m.r.t.Fatalf("%s rewrote the administrator's pin: %+v", what, pin)
	}
	if said := m.record(); !strings.Contains(said, "no move recorded on "+m.oldHost) {
		m.r.t.Fatalf("%s left a record on the backend the team leaves: %s", what, said)
	}
}

// notJoined fails the test when the administrator's store holds a profile
// for the team at a server.
func (m *movingTeam) notJoined(what string, backend restBackend) {
	m.r.t.Helper()
	key := teamstore.Key("rest", backend.url, m.r.teamRef)
	if _, err := os.Lstat(filepath.Join(m.r.configDir, "teams", key)); !errors.Is(err, fs.ErrNotExist) {
		m.r.t.Fatalf("%s left a profile for the server in the administrator's store (%v)", what, err)
	}
}

// serverRoster is the team's roster on a server, read by the rest adapter
// under the profile a member's store holds for it, keyed by human_label.
func (m *movingTeam) serverRoster(configDir string, backend restBackend) map[string]string {
	m.r.t.Helper()
	key := teamstore.Key("rest", backend.url, m.r.teamRef)
	res := m.r.run(m.r.terminalEnv(configDir), "", "adapter", "rest", "--profile", key, "team", "members")
	if res.exit != 0 {
		m.r.t.Fatalf("the server's roster: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	out := map[string]string{}
	for _, member := range jsonResult[roster](m.r.t, res.stdout).Members {
		out[member.HumanLabel] = member.PrincipalRef
	}
	return out
}

// ticket reads the ticket of the recorded move from the backend the team
// leaves, into a file of the test's, the way a member's follow reads it.
// It is the only place a test can learn the ticket: the command that made
// it shows it nowhere.
func (m *movingTeam) ticket() (ticket, file string) {
	m.r.t.Helper()
	file = filepath.Join(m.r.dirs.Root, "ticket-read-by-the-test")
	res := m.oldAdapter("--profile", m.oldKey, "team", "moved", "--ticket-file", file)
	if res.exit != 0 {
		m.r.t.Fatalf("team moved --ticket-file: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	text, err := os.ReadFile(file)
	if err != nil {
		m.r.t.Fatalf("the ticket the old backend holds: %v", err)
	}
	return strings.TrimSpace(string(text)), file
}

// noSecrets fails the test when the join secret or the ticket is in
// anything a `team move` printed, on an fs adapter child's argv, or in a
// file the run left behind other than the files they travel in. allowed
// are those files.
func (m *movingTeam) noSecrets(ticket string, allowed ...string) {
	t := m.r.t
	t.Helper()
	if ticket == "" || ticket == m.secret {
		t.Fatalf("the ticket the old backend holds is not a ticket")
	}
	argv, _ := os.ReadFile(m.r.argvLog)
	for name, hidden := range map[string]string{"the join secret": m.secret, "the move's ticket": ticket} {
		if strings.Contains(m.said.String(), hidden) {
			t.Errorf("%s is in what a team move printed", name)
		}
		if bytes.Contains(argv, []byte(hidden)) {
			t.Errorf("%s was on an adapter child's argv", name)
		}
		for _, hit := range tokenHits(t, hidden, m.r.dirs.Root) {
			if !slices.Contains(allowed, hit) {
				t.Errorf("%s is in %s", name, hit)
			}
		}
	}
	if left, _ := filepath.Glob(filepath.Join(m.r.stateDir, "team-move", "*")); len(left) != 0 {
		t.Errorf("a move left its ticket file: %v", left)
	}
}

// olderServer is a server that takes no team in by request, as one before
// 0.7.0: it answers `/v1/info` with no team.take_in and nothing else. It
// records every path it was asked.
type olderServer struct {
	*httptest.Server
	mu    sync.Mutex
	paths []string
}

func startOlderServer(t *testing.T) *olderServer {
	t.Helper()
	s := &olderServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		s.mu.Lock()
		s.paths = append(s.paths, req.URL.Path)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if req.URL.Path != "/v1/info" {
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"ok":false,"protocol_version":"` + protocol.ProtocolVersion + `","error":{"code":"not_found","message":"no such endpoint","retryable":false}}` + "\n"))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"protocol_version":"` + protocol.ProtocolVersion +
			`","result":{"contract_version":"1","server":{"name":"brigade-server","version":"0.6.0"},"capabilities":["team.admin"]}}` + "\n"))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *olderServer) asked() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.paths)
}

// A lossyFront stands between a member's machine and a server and, while it
// is told to, loses the answer of every take-in: it hands the request to the
// server, lets the server carry it out, and closes the connection without a
// word. It is what a network does to a request whose answer never arrives.
// Every other request, and every take-in once it is told to stop, is handed
// on and answered as the server answered it.
type lossyFront struct {
	*httptest.Server
	backend string
	client  *http.Client
	mu      sync.Mutex
	losing  bool
	// lost counts the take-ins the server answered as done whose answer
	// the front dropped.
	lost int
}

// startLossyFront starts a front for backend and answers the server a
// member's machine is told of: the front's own address. A backend that
// serves TLS under a pinned certificate gets a front that serves TLS under a
// certificate of its own, whose pin is the one the member is given, and that
// trusts the backend by the backend's pin and by nothing else, as the
// member's adapter would.
func startLossyFront(t *testing.T, backend restBackend) (*lossyFront, restBackend) {
	t.Helper()
	f := &lossyFront{backend: backend.url, losing: true, client: http.DefaultClient}
	f.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		body, err := io.ReadAll(req.Body)
		if err != nil {
			http.Error(w, "the request could not be read", http.StatusBadGateway)
			return
		}
		//nolint:gosec // G704: the url is the test's own server's, and the path one the built adapter asked this front for
		forward, err := http.NewRequestWithContext(req.Context(), req.Method, f.backend+req.URL.RequestURI(), bytes.NewReader(body))
		if err != nil {
			http.Error(w, "the request could not be forwarded", http.StatusBadGateway)
			return
		}
		forward.Header = req.Header.Clone()
		answer, err := f.client.Do(forward) //nolint:gosec // G704: as above
		if err != nil {
			http.Error(w, "the server did not answer", http.StatusBadGateway)
			return
		}
		defer func() { _ = answer.Body.Close() }()
		answered, err := io.ReadAll(answer.Body)
		if err != nil {
			http.Error(w, "the server's answer could not be read", http.StatusBadGateway)
			return
		}
		f.mu.Lock()
		lose := f.losing && req.URL.Path == "/v1/team/take-in"
		if lose && answer.StatusCode == http.StatusOK {
			f.lost++
		}
		f.mu.Unlock()
		if lose {
			// The answer is lost: the connection ends with nothing on it.
			if hijacker, ok := w.(http.Hijacker); ok {
				if conn, _, herr := hijacker.Hijack(); herr == nil {
					_ = conn.Close()
					return
				}
			}
			panic(http.ErrAbortHandler)
		}
		w.Header().Set("Content-Type", answer.Header.Get("Content-Type"))
		w.WriteHeader(answer.StatusCode)
		_, _ = w.Write(answered)
	}))
	// A request the front drops is a handler that ended without an answer:
	// nothing of it is worth a line in the test's output.
	f.Config.ErrorLog = log.New(io.Discard, "", 0)
	t.Cleanup(f.Close)
	if backend.certificate == "" {
		f.Start()
		return f, restBackend{url: f.URL}
	}
	f.client = &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: true, //nolint:gosec // G402: verified by the server's pinned fingerprint, below
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 || adapterkit.CertificateFingerprint(state.PeerCertificates[0].Raw) != backend.certificate {
				return errors.New("the server behind the front does not present the pinned certificate")
			}
			return nil
		},
	}}}
	f.StartTLS()
	return f, restBackend{url: f.URL, certificate: adapterkit.CertificateFingerprint(f.Certificate().Raw)}
}

// lose tells the front whether a take-in's answer is lost from now on.
func (f *lossyFront) lose(losing bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.losing = losing
}

// answersLost is how many take-ins the server carried out whose answer the
// front lost.
func (f *lossyFront) answersLost() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lost
}

// withFsAdapter runs do with the administrator's `fs` adapter replaced by a
// script that answers refuse for the `team move` whose argv carries flag,
// and is the rig's adapter for everything else: a backend that refuses one
// command. The script is run as `/bin/sh <script>` and uses builtins alone.
func (m *movingTeam) withFsAdapter(flag, refuse string, do func()) {
	t := m.r.t
	t.Helper()
	script := filepath.Join(m.r.dirs.Root, "adapter-refusing"+flag+".sh")
	body := "verb=''; hit=0; prev=''\n" +
		"for a in \"$@\"; do\n" +
		"  case \"$prev $a\" in 'team move') verb=move;; esac\n" +
		"  case \"$a\" in " + flag + ") hit=1;; esac\n" +
		"  prev=\"$a\"\n" +
		"done\n" +
		"if [ \"$verb\" = move ] && [ \"$hit\" = 1 ]; then\n" +
		"  printf '%s\\n' " + shellQuote(refuse) + "\n" +
		"  exit 11\n" +
		"fi\n" +
		"exec " + shellQuote(m.r.wrapper) + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.RegisterAdapter(m.r.configDir, "fs", []string{"/bin/sh", script, "--root", m.r.root}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := config.RegisterAdapter(m.r.configDir, "fs", m.r.adapterCmd); err != nil {
			t.Fatal(err)
		}
	}()
	do()
}

// withoutMigration is the Supabase adapter's answer to a `team move` on a
// project that lacks the team-move migration, word for word.
const withoutMigration = `{"ok":false,"protocol_version":"` + protocol.ProtocolVersion + `","error":{"code":"config","message":"this backend cannot record a team's move: it lacks the migration 20261114120000_team_move.sql, which its Project Owner applies","retryable":false,"details":{"reason":"migration_drift"}}}`

// unreachable is an adapter's answer for a backend it could not reach.
const unreachable = `{"ok":false,"protocol_version":"` + protocol.ProtocolVersion + `","error":{"code":"unavailable","message":"the backend could not be reached","retryable":true}}`

// startOlderSession starts a session of bob's with a `brigade` of version
// 0.54.0, the last that does not follow a recorded move, and leaves it
// online on the backend the team leaves: it has an inbox socket, so a
// watcher keeps its lease for as long as the test runs.
func (m *movingTeam) startOlderSession() session {
	t := m.r.t
	t.Helper()
	old := testutil.BuildStamped(t, "./cmd/brigade", "0.54.0")
	s := m.r.newSession("bob", "billing-old-plugin", "", fakesock.New(t).Path(), "cc-messaging-secret-MUST-NOT-LEAK-"+testutil.RunID())
	//nolint:gosec // G204: a binary this test built; no shell is involved
	cmd := exec.CommandContext(t.Context(), old, "hook", "session-start")
	cmd.Env, cmd.Dir = m.r.restEnv(s), m.r.dirs.Home
	cmd.Stdin = strings.NewReader(s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("the older plugin's session start: %v\n%s", err, out)
	}
	if got := m.r.mustMap(s); got.TeamKey != m.oldKey {
		t.Fatalf("the older plugin's session: %+v", got)
	}
	m.r.liveWatcher(s)
	return s
}

// start runs a session's SessionStart hook.
func (m *movingTeam) start(s session) result {
	m.r.t.Helper()
	return m.r.restHook(s, "session-start", s.hookDoc("SessionStart", map[string]any{"source": "startup"}))
}

// followsAndTalks is what a move is for: bob's next session, in a checkout
// that is pinned to the backend the team left, follows the team to the
// server with no command, under the label he had, and a message from it
// reaches a session of the administrator's on the server.
func (m *movingTeam) followsAndTalks(backend restBackend) {
	t, r := m.r.t, m.r
	t.Helper()
	destKey := teamstore.Key("rest", backend.url, r.teamRef)
	if pin := m.pinOf(r.configDirB); pin != pinInBobsStore(m) {
		t.Fatalf("bob's pin moved before his session followed: %+v", pin)
	}
	bob := r.newSession("bob", bobName, "", "", "")
	res := m.start(bob)
	where := strings.TrimPrefix(strings.TrimPrefix(backend.url, "https://"), "http://")
	if !strings.Contains(res.stdout, `followed team "`+teamName+`" to `+where) || !strings.Contains(res.stdout, "recorded that move on "+m.oldHost) ||
		!strings.Contains(res.stdout, "with the move's ticket, under the label you had") || strings.Contains(res.stdout, bobLabel) {
		t.Fatalf("the session that followed: stdout %q\nstderr %s", res.stdout, res.stderr)
	}
	m.said.WriteString(res.stdout + res.stderr)
	bobMap := r.mustMap(bob)
	if bobMap.TeamRef != r.teamRef || bobMap.TeamKey != destKey || !sessionmap.IsBundledRest(bobMap.AdapterCommand) {
		t.Fatalf("bob's map after the follow: %+v", bobMap)
	}
	if pin := m.pinOf(r.configDirB); pin.Adapter != "rest" || pin.URL != backend.url || pin.Certificate != backend.certificate || pin.MovedFrom != m.oldPin.URL {
		t.Fatalf("bob's pin after the follow: %+v", pin)
	}
	// Two members on the server, each under the label they had on the old
	// backend: the administrator's is not the account of her terminal.
	members := m.serverRoster(r.configDir, backend)
	if len(members) != 2 || members[aliceLabel] == "" || members[bobLabel] == "" || members[terminalAccount] != "" {
		t.Fatalf("the team's roster on the server: %v", members)
	}
	// The administrator owns the team; the member the ticket let in does not.
	if res := r.runIn(r.checkout, r.terminalEnv(r.configDirB), "", "team", "transfer", "--principal", members[bobLabel]); res.exit == 0 {
		t.Fatalf("a member that came in with the ticket administered the team:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	if res := r.runIn(r.checkout, m.terminal, "", "team", "transfer", "--principal", members[aliceLabel]); res.exit != 0 {
		t.Fatalf("the administrator's own command: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}

	sock := fakesock.New(t)
	alice := r.newSession("alice", "", aliceName, sock.Path(), "cc-messaging-secret-MUST-NOT-LEAK-"+testutil.RunID())
	m.start(alice)
	aliceMap := r.mustMap(alice)
	if aliceMap.TeamKey != destKey {
		t.Fatalf("the administrator's map after the move: %+v", aliceMap)
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return r.logHas(alice, "watch ready") })
	sent := jsonResult[sendResult](t, r.mustRun(r.restEnv(bob), "I followed the team here", "send", aliceMap.BrigadeSessionID, "--summary", "moved", "--json").stdout)
	frames := sock.WaitFrames(1, waitLong)
	p, err := frame.Parse(frames[0])
	if err != nil {
		t.Fatalf("the injected frame does not parse: %v\n%s", err, frames[0])
	}
	if p.MessageID != sent.MessageID || p.ReplyToSessionID != bobMap.BrigadeSessionID || p.FromLabel != bobLabel+" (unverified)" || p.Body != "I followed the team here" {
		t.Errorf("frame: %+v", p)
	}
	entry := r.liveWatcher(alice)
	for _, s := range []session{alice, bob} {
		r.restHook(s, "session-end", s.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))
	}
	testutil.Eventually(t, waitLong, pollEvery, func() bool { return !alive(entry.PID) })
}

// pinInBobsStore is bob's pin as it was when the team was on the backend it
// leaves: the same backend the administrator's names, the pin's own
// consent time aside.
func pinInBobsStore(m *movingTeam) teamstore.Pin {
	pin := m.pinOf(m.r.configDirB)
	if pin.Adapter != m.oldPin.Adapter || pin.URL != m.oldPin.URL || pin.TeamRef != m.oldPin.TeamRef {
		m.r.t.Fatalf("bob's pin before the follow: %+v", pin)
	}
	return pin
}

// movedFile fails the test unless the team file names the server and the
// same team, with no key and with every other member it had.
func (m *movingTeam) movedFile(backend restBackend) {
	t := m.r.t
	t.Helper()
	file, err := teamfile.Parse(filepath.Join(m.r.checkout, teamfile.FileName))
	if err != nil {
		t.Fatal(err)
	}
	if file.Adapter != "rest" || file.URL != backend.url || file.Certificate != backend.certificate || file.PublishableKey != "" ||
		file.TeamRef != m.r.teamRef || file.TeamName != teamName || len(file.Ignored) != 0 {
		t.Fatalf("the team file after the move: %+v", file)
	}
	now := m.fileNow()
	if strings.Contains(now, "publishable_key") || strings.Count(now, "\n") != strings.Count(m.fileBefore, "\n") {
		t.Fatalf("the team file after the move:\n%s\nbefore:\n%s", now, m.fileBefore)
	}
	if pin := m.pinOf(m.r.configDir); !pin.Matches(file) || pin.MovedFrom != "" {
		t.Fatalf("the administrator's pin after the move: %+v", pin)
	}
	binding, err := teamstore.LoadBinding(m.r.configDir, teamstore.Key("rest", backend.url, m.r.teamRef))
	if err != nil || teamstore.VerifyBinding(binding, file) != nil {
		t.Fatalf("the administrator's membership at the server: %+v, %v", binding, err)
	}
	if said := m.record(); !strings.Contains(said, "recorded on "+m.oldHost) || !strings.Contains(said, "names the same backend") {
		t.Fatalf("the record after the move: %s", said)
	}
}

// TestATeamMovesToAServerByOneCommandThroughTheRealBinary is the move from
// the fs adapter. Four things stop it before anything is written, each with
// the sentence that says what to do: a secret file of another team, a
// server that takes no team in by request, a project without the migration
// that keeps the record, and a team that someone else already owns on the
// server. Then the administrator declines, then moves the team with the one
// command, and runs it again; a member's next session follows with no
// command, and a message is delivered; and the record is cleared, which is
// the way back the command names.
func TestATeamMovesToAServerByOneCommandThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	teamMovesByOneCommand(t, m, true)
}

// TestATeamMovesFromSupabaseToAServerByOneCommandThroughTheRealBinary is
// the same move from the backend the feature was built for: a team on the
// local Supabase stack, to the built server. It needs the stack and the
// server both, which no CI job has together, so it runs where a developer
// asks for it: `make test-move-live`.
func TestATeamMovesFromSupabaseToAServerByOneCommandThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	if os.Getenv(serverBinVar) == "" {
		t.Skip("needs a built brigade-server, named in " + serverBinVar)
	}
	stack := testutil.RequireSupabase(t)
	r := newRig(t)
	res := r.runIn(r.checkout, r.baseEnv(), "", "team", "create", "--url", stack.URL, "--key", stack.PublishableKey,
		"--name", teamName, "--label", aliceLabel, "--secret-file", r.secretFile)
	if res.exit != 0 {
		t.Fatalf("team create on the stack: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	canon, err := teamfile.Canonicalize(r.checkout)
	if err != nil {
		t.Fatal(err)
	}
	pin, ok, err := teamstore.LookupPin(r.configDir, canon)
	if err != nil || !ok || pin.Adapter != "supabase" {
		t.Fatalf("alice's pin after the create: %+v %v %v", pin, ok, err)
	}
	r.teamRef, r.teamKey = pin.TeamRef, teamstore.Key(pin.Adapter, pin.URL, pin.TeamRef)
	joiner := r.newSession("bob", "billing-join", "", "", "")
	r.restHook(joiner, "session-start", joiner.hookDoc("SessionStart", map[string]any{"source": "startup"}))
	if res := r.restJoin(joiner, bobLabel); res.exit != 0 {
		t.Fatalf("bob's join on the stack: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m := newMovingTeam(t, r, func(args ...string) result {
		return r.run(r.terminalEnv(r.configDir), "", append([]string{"adapter", "supabase"}, args...)...)
	})
	teamMovesByOneCommand(t, m, false)
}

// teamMovesByOneCommand is the move of the rig's team, whatever backend it
// is on: alice is its administrator in r.configDir, bob a member in
// r.configDirB, and r.secretFile holds the team's join secret. fsBackend
// says that the backend is the fs adapter, whose answers a test can
// replace: the refusal of a project without the migration is shown there.
func teamMovesByOneCommand(t *testing.T, m *movingTeam, fsBackend bool) {
	t.Helper()
	r := m.r
	older := m.startOlderSession()

	// --- 1. a secret file of another team: nothing is sent anywhere ------------------------------------
	backend := startRestBackend(t)
	otherSecret := filepath.Join(r.dirs.Root, "other.secret")
	other := strings.Join([]string{"brg1", "another-team", strings.Repeat("ab", 16)}, ".")
	if err := os.WriteFile(otherSecret, []byte(other+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := m.move(m.terminal, "y\n", moveArgs(backend, otherSecret)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "holds the join secret of another team") || !strings.Contains(res.stderr, "name the file `team create` wrote for this team") ||
		strings.Contains(res.stdout+res.stderr, other) {
		t.Fatalf("a secret file of another team: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a secret file of another team")
	m.notJoined("a secret file of another team", backend)

	// --- 2. a server that takes no team in by request: it is asked what it offers, and nothing else -----
	old := startOlderServer(t)
	res = m.move(m.terminal, "y\n", moveArgs(restBackend{url: old.URL}, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "takes no team in by request: it is older than 0.7.0") ||
		!strings.Contains(res.stderr, "brigade-server --import-team") || !strings.Contains(res.stderr, "brigade-server --open-move "+r.teamRef+" --ticket-file") ||
		!strings.Contains(res.stderr, "brigade team move --ticket-file <path>") {
		t.Fatalf("a server that takes no team in: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if asked := old.asked(); len(asked) == 0 || slices.ContainsFunc(asked, func(p string) bool { return p != "/v1/info" }) {
		t.Fatalf("the older server was asked %v, want /v1/info alone", asked)
	}
	m.unmoved("a server that takes no team in")
	m.notJoined("a server that takes no team in", restBackend{url: old.URL})

	// --- 3. a project without the migration that keeps the record: the server is asked nothing ----------
	if fsBackend {
		m.withFsAdapter("--clear", withoutMigration, func() {
			res := m.move(m.terminal, "y\n", moveArgs(backend, r.secretFile)...)
			if res.exit == 0 || !strings.Contains(res.stderr, "lacks the migration 20261114120000_team_move") ||
				!strings.Contains(res.stderr, "The project's owner applies it with `make backend-install project=") || !strings.Contains(res.stderr, "Nothing was changed") {
				t.Fatalf("a project without the migration: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
			}
		})
		m.unmoved("a project without the migration")
		m.notJoined("a project without the migration", backend)
	}

	// --- 4. a team that someone else already owns on the server -----------------------------------------
	// Whoever holds the join secret and may create a team on a server can take the team in there (ADR 0005).
	// A stranger did, on a server of its own for this step, before the administrator moved the team.
	taken := startRestBackend(t)
	stranger := filepath.Join(r.dirs.Root, "config-stranger")
	if err := os.MkdirAll(stranger, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{"join_secret": m.secret, "team_name": teamName, "human_label": "stranger", "backend": map[string]string{"url": taken.url}}
	if taken.certificate != "" {
		doc["backend"] = map[string]string{"url": taken.url, "certificate": taken.certificate}
	}
	request, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if res := r.run(r.terminalEnv(stranger), string(request), "adapter", "rest", "--profile", "stranger", "team", "take-in"); res.exit != 0 || !strings.Contains(res.stdout, `"owner":true`) {
		t.Fatalf("the stranger's take-in: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	res = m.move(m.terminal, "y\n", moveArgs(taken, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "is held on ") || !strings.Contains(res.stderr, "and owned by someone else") ||
		!strings.Contains(res.stderr, "the server's operator can name its owner") ||
		!strings.Contains(res.stderr, "brigade-server --set-owner "+r.teamRef+" --principal ") || !strings.Contains(res.stderr, "Nothing was recorded") {
		t.Fatalf("a team that someone else owns on the server: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a team that someone else owns on the server")

	// --- 5. the administrator is shown the move and says no ---------------------------------------------
	res = m.move(m.terminal, "n\n", moveArgs(backend, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "not moved; nothing was changed") ||
		!strings.Contains(res.stderr, `move team "`+teamName+`" (`+r.teamRef+`) from `+m.oldHost+` to `) || !strings.Contains(res.stderr, `via adapter "rest"`) ||
		!strings.Contains(res.stderr, "1 session that is online runs a plugin older than 0.55.0") || !strings.Contains(res.stderr, "billing-old-plugin ("+bobLabel+", 0.54.0)") {
		t.Fatalf("a move she declined: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if backend.certificate != "" && !strings.Contains(res.stderr, "server certificate "+backend.certificate) {
		t.Fatalf("the question does not show the certificate: %s", res.stderr)
	}
	m.unmoved("a move she declined")
	m.notJoined("a move she declined", backend)

	// --- 6. the move, by the one command ------------------------------------------------------------------
	res = m.move(m.terminal, "y\n", moveArgs(backend, r.secretFile, "--ticket-days", "7")...)
	if res.exit != 0 {
		t.Fatalf("the move: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, need := range []string{
		`took team "` + teamName + `" (` + r.teamRef + `) in on `, ": you own it there",
		"sent your display label: " + aliceLabel,
		"opened its move: the ticket admits the team's members until ",
		"recorded on " + m.oldHost + `: team "` + teamName + `" (` + r.teamRef + `) moved to `,
		"wrote " + teamfile.FileName + ": it names ",
		"left to do:",
		"commit and push " + teamfile.FileName + " in every repository of the team",
		"1 session that is online runs a plugin older than 0.55.0 and will not follow until its member updates it: billing-old-plugin (" + bobLabel + ", 0.54.0)",
		"to undo: `brigade team move --clear` stops " + m.oldHost + " from saying where the team went",
	} {
		if !strings.Contains(res.stdout, need) {
			t.Errorf("the move's lines lack %q:\n%s", need, res.stdout)
		}
	}
	if strings.Contains(res.stdout+res.stderr, terminalAccount) {
		t.Errorf("the move names the account of the terminal:\n%s\n%s", res.stdout, res.stderr)
	}
	m.movedFile(backend)
	if members := m.serverRoster(r.configDir, backend); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the team's roster on the server after the move: %v", members)
	}
	first, ticketRead := m.ticket()

	// --- 7. the same command again changes nothing but the ticket ----------------------------------------
	// The team file names the server by now, so the backend the team left is the other membership of the team
	// this machine holds. Step 4 left it one more, on the server where a stranger owns the team: with two, the
	// command asks which one is meant, and --from says.
	res = m.move(m.terminal, "y\n", moveArgs(backend, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "name the one it is leaving with --from <url>") {
		t.Fatalf("the move run again with two other memberships: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	res = m.move(m.terminal, "y\n", moveArgs(backend, r.secretFile, "--from", m.oldHost)...)
	if res.exit != 0 || !strings.Contains(res.stdout, "already, and you own it there") ||
		!strings.Contains(res.stdout, "opened its move again, which replaced the ticket it had") || !strings.Contains(res.stdout, teamfile.FileName+" named ") {
		t.Fatalf("the move, run again: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(backend)
	if members := m.serverRoster(r.configDir, backend); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the team's roster on the server after the second run: %v", members)
	}
	ticket, _ := m.ticket()
	if ticket == first {
		t.Fatalf("a second open did not replace the ticket the old backend holds")
	}

	// --- 8. a member's next session follows, and the team works -------------------------------------------
	m.followsAndTalks(backend)
	r.restHook(older, "session-end", older.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))

	// --- 9. neither the join secret nor a ticket is anywhere it should not be -----------------------------
	// The record holds the ticket, which for the fs adapter is a file of its store.
	allowed := []string{r.secretFile, ticketRead}
	if fsBackend {
		allowed = append(allowed, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
	}
	m.noSecrets(ticket, allowed...)
	if hits := tokenHits(t, first, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the ticket a second open replaced is still in %v", hits)
	}

	// --- 10. the way back the command named: the record is cleared, and the old backend says nothing ----
	// It also leaves the backend the team left as the test found it: the local Supabase stack's own tests
	// count the records it holds.
	res = m.move(m.terminal, "", "team", "move", "--clear", "--from", m.oldHost)
	if res.exit != 0 || !strings.Contains(res.stdout, "cleared: "+m.oldHost+" no longer says") {
		t.Fatalf("the clear: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if said := m.record(); !strings.Contains(said, "no move recorded on "+m.oldHost) {
		t.Fatalf("the record after the clear: %s", said)
	}
}

// TestAMoveStoppedAtEachStepEndsTheSameWhenRunAgainThroughTheRealBinary:
// the one command is stopped after the take-in, after the open, after the
// record and after the team file, and run again each time. Every run
// leaves the team where the design says a stop there leaves it, and the
// last one leaves what a move that was never stopped leaves.
func TestAMoveStoppedAtEachStepEndsTheSameWhenRunAgainThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	backend := startRestBackend(t)
	args := moveArgs(backend, r.secretFile)
	again := "Run the same command again to finish the move"

	// --- 1. stopped after the take-in: the ticket has nowhere to go --------------------------------------
	blocked := filepath.Join(r.stateDir, "team-move")
	if err := os.MkdirAll(r.stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blocked, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res := m.move(m.terminal, "y\n", args...)
	if res.exit == 0 || !strings.Contains(res.stderr, "the team is taken in on ") || !strings.Contains(res.stderr, "and its move was not opened") ||
		!strings.Contains(res.stderr, again) {
		t.Fatalf("a move whose ticket has nowhere to go: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.unmoved("a move stopped after the take-in")
	if members := m.serverRoster(r.configDir, backend); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the server's roster after the take-in: %v", members)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}

	// --- 2. stopped after the open: the backend the team leaves does not take the record ----------------
	m.withFsAdapter("--ticket-file", unreachable, func() {
		res := m.move(m.terminal, "y\n", args...)
		if res.exit == 0 || !strings.Contains(res.stderr, "its move is open, and "+m.oldHost+" did not record it") || !strings.Contains(res.stderr, again) {
			t.Fatalf("a move that was not recorded: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
		}
	})
	m.unmoved("a move stopped after the open")

	// --- 3. stopped after the record: the checkout cannot be written ------------------------------------
	//nolint:gosec // G302: the checkout is made read-only for one run
	if err := os.Chmod(r.checkout, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.checkout, 0o700) }) //nolint:gosec // G302: restored for the temp dir's removal
	res = m.move(m.terminal, "y\n", args...)
	if res.exit == 0 || !strings.Contains(res.stderr, "the move is recorded on "+m.oldHost+", and this checkout was not written") || !strings.Contains(res.stderr, again) {
		t.Fatalf("a move whose checkout cannot be written: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	//nolint:gosec // G302: the checkout is the test's own
	if err := os.Chmod(r.checkout, 0o700); err != nil {
		t.Fatal(err)
	}
	if got := m.fileNow(); got != m.fileBefore {
		t.Fatalf("the team file changed in a checkout that cannot be written:\n%s", got)
	}
	if pin := m.pinOf(r.configDir); pin != m.oldPin {
		t.Fatalf("the pin was written before the team file: %+v", pin)
	}
	if said := m.record(); !strings.Contains(said, "recorded on "+m.oldHost) || !strings.Contains(said, "names ANOTHER backend") {
		t.Fatalf("the record of a move whose team file was not written: %s", said)
	}

	// --- 4. stopped after the team file: the pin cannot be written --------------------------------------
	//nolint:gosec // G302: the store's own directory is made read-only for one run; its teams/ stays writable
	if err := os.Chmod(r.configDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(r.configDir, 0o700) }) //nolint:gosec // G302: restored for the temp dir's removal
	res = m.move(m.terminal, "y\n", args...)
	//nolint:gosec // G302: the store is the test's own
	if err := os.Chmod(r.configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if res.exit == 0 || !strings.Contains(res.stderr, "this checkout was not written") {
		t.Fatalf("a move whose pin cannot be written: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	file, err := teamfile.Parse(filepath.Join(r.checkout, teamfile.FileName))
	if err != nil || file.Adapter != "rest" || file.URL != backend.url {
		t.Fatalf("the team file after a run that wrote it: %+v, %v", file, err)
	}
	if pin := m.pinOf(r.configDir); pin != m.oldPin {
		t.Fatalf("the pin after a run that could not write it: %+v", pin)
	}

	// --- 5. run once more, with nothing in its way: the team file names the server already --------------
	res = m.move(m.terminal, "y\n", args...)
	if res.exit != 0 || !strings.Contains(res.stdout, "already, and you own it there") || !strings.Contains(res.stdout, teamfile.FileName+" named ") {
		t.Fatalf("the last run: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	m.movedFile(backend)
	if members := m.serverRoster(r.configDir, backend); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the server's roster after five runs: %v", members)
	}
	ticket, ticketRead := m.ticket()
	m.followsAndTalks(backend)
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
}

// TestATeamIsMovedFromInsideASessionThroughTheRealBinary: the one command
// run by the model, in the Bash tool of a session of the administrator's.
// With the option team_move_in_session off it is refused and nothing is
// asked of anyone; with the option on, which is its default, it says what
// it is about to do, asks nothing and moves the team, after which a
// member's next session follows and a message is delivered.
func TestATeamIsMovedFromInsideASessionThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	backend := startRestBackend(t)
	args := moveArgs(backend, r.secretFile)

	// --- 1. a session whose member turned the option off -------------------------------------------------
	// The option reaches the hooks and never the Bash tool: the command reads it from what the session's
	// start wrote down, so its own environment carries no option at all.
	closed := r.newSession("alice", "", "ops-closed", "", "")
	closed.extraEnv = []string{config.OptionTeamMoveInSession + "=off"}
	m.start(closed)
	shell := closed
	shell.extraEnv = nil
	for _, form := range [][]string{args, {"team", "move", "--clear"}, {"team", "move", "--ticket-file", r.secretFile}} {
		res := m.move(r.restEnv(shell), "", form...)
		if res.exit != 2 || !strings.Contains(res.stderr, "the option team_move_in_session is off here") || res.stdout != "" {
			t.Fatalf("%v in a session with the option off: exit %d\nstdout: %s\nstderr: %s", form, res.exit, res.stdout, res.stderr)
		}
	}
	m.unmoved("a move from a session with the option off")
	m.notJoined("a move from a session with the option off", backend)
	// Reading the record is no move, and runs there all the same.
	if res := m.move(r.restEnv(shell), "", "team", "move", "--show", "--from", m.oldHost); res.exit != 0 || !strings.Contains(res.stdout, "no move recorded on "+m.oldHost) {
		t.Fatalf("--show in a session with the option off: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	r.restHook(closed, "session-end", closed.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))

	// --- 2. a session with the option as it comes: the move, with nobody asked --------------------------
	open := r.newSession("alice", "", "ops-open", "", "")
	m.start(open)
	if got := r.mustMap(open); got.TeamKey != m.oldKey {
		t.Fatalf("the administrator's session before the move: %+v", got)
	}
	res := m.move(r.restEnv(open), "", args...)
	if res.exit != 0 {
		t.Fatalf("the move from a session: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	first, _, _ := strings.Cut(res.stdout, "\n")
	if !strings.HasPrefix(first, `move team "`+teamName+`" (`+r.teamRef+`) from `+m.oldHost+` to `) || !strings.HasSuffix(first, "Consented by this invocation.") ||
		strings.Contains(res.stdout+res.stderr, "[y/N]") || !strings.Contains(res.stdout, "sent your display label: "+aliceLabel) ||
		!strings.Contains(res.stdout, "left to do:") {
		t.Fatalf("the move from a session said:\nstdout: %s\nstderr: %s", res.stdout, res.stderr)
	}
	m.movedFile(backend)
	ticket, ticketRead := m.ticket()
	r.restHook(open, "session-end", open.hookDoc("SessionEnd", map[string]any{"reason": "exit"}))

	// --- 3. a member's next session follows, and the team works ------------------------------------------
	m.followsAndTalks(backend)
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
}

// TestATakeInWhoseAnswerIsLostKeepsItsProfileThroughTheRealBinary: the
// server takes the team in and its answer never arrives. The one command
// stops, having recorded and written nothing, and it leaves the profile the
// server's adapter wrote for the request where it is: that directory holds
// the retry key the adapter minted (ADR 0005), which is what the next run of
// the command is the same caller with, so nothing of it may be cleaned away,
// as it is after a refusal.
//
// Then the answers arrive again and the same command is run again. It is
// the owner the first request made and no second member: the team was on
// the server already, the move is opened and recorded, the checkout is
// written, the retry key is gone, a member's next session follows and a
// message is delivered. The server is the stand-in under `make test` and the
// built server, over TLS, where `make server-test` names one; a front that
// loses the answer stands before either.
func TestATakeInWhoseAnswerIsLostKeepsItsProfileThroughTheRealBinary(t *testing.T) {
	t.Parallel()
	r := newRig(t)
	r.createTeam()
	m := newMovingTeam(t, r, r.fsAdapterAs(r.configDir))
	front, server := startLossyFront(t, startRestBackend(t))
	profile := filepath.Join(r.configDir, "teams", teamstore.Key("rest", server.url, r.teamRef))
	if _, err := os.Lstat(profile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the profile for the server before the move: %v", err)
	}

	// --- 1. the server takes the team in, and its answer is lost ------------------------------------------
	res := m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "did not take the team in") || !strings.Contains(res.stderr, "Nothing was recorded") ||
		strings.Contains(res.stdout, "took") {
		t.Fatalf("a move whose take-in's answer was lost: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	if lost := front.answersLost(); lost != 1 {
		t.Fatalf("the server carried out %d take-ins whose answer was lost, want 1", lost)
	}
	m.unmoved("a move whose take-in's answer was lost")
	// The profile the adapter wrote is where the adapter left it, with what
	// it holds: the server it names, no team yet, no credential, and the
	// retry key of the take-in, in a file that is the member's alone.
	entries, err := os.ReadDir(profile)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the profile's directory after a lost answer: %v, %v", entries, err)
	}
	kept, err := teamstore.LoadBinding(r.configDir, filepath.Base(profile))
	if err != nil || kept.URL != server.url || kept.Certificate != server.certificate || kept.TeamRef != "" {
		t.Fatalf("the profile after a lost answer: %+v, %v", kept, err)
	}
	if _, err := os.Lstat(filepath.Join(profile, "credential.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a take-in whose answer was lost left a credential: %v", err)
	}
	keyFile := filepath.Join(profile, "take-in-key.json")
	if info, err := os.Lstat(keyFile); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("the retry key's file after a lost answer: %v, %v", info, err)
	}
	text, err := os.ReadFile(keyFile) //nolint:gosec // G304: a file of the test's own store
	if err != nil {
		t.Fatal(err)
	}
	var minted struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal(text, &minted); err != nil || len(minted.Key) < 22 {
		t.Fatalf("the retry key's file does not hold a key: %v", err)
	}
	if strings.Contains(m.said.String(), m.secret) {
		t.Fatalf("the join secret is in what the move printed")
	}

	// --- 2. a second answer is lost too: the same key, and still one owner ---------------------------------
	res = m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit == 0 || !strings.Contains(res.stderr, "did not take the team in") || front.answersLost() != 2 {
		t.Fatalf("a second take-in whose answer was lost: exit %d, %d lost\nstdout: %s\nstderr: %s", res.exit, front.answersLost(), res.stdout, res.stderr)
	}
	m.unmoved("a second move whose take-in's answer was lost")
	if again, err := os.ReadFile(keyFile); err != nil || !bytes.Equal(again, text) { //nolint:gosec // G304: as above
		t.Fatalf("the retry key's file changed between two attempts (%v)", err)
	}

	// --- 3. the answers arrive again: the same command finishes the move, as the owner the first run made ---
	front.lose(false)
	res = m.move(m.terminal, "y\n", moveArgs(server, r.secretFile)...)
	if res.exit != 0 {
		t.Fatalf("the move, run again once answers arrive: exit %d\nstdout: %s\nstderr: %s", res.exit, res.stdout, res.stderr)
	}
	for _, need := range []string{
		`team "` + teamName + `" (` + r.teamRef + `) was on `, "already, and you own it there",
		"sent your display label: " + aliceLabel,
		"opened its move: the ticket admits the team's members until ",
		"recorded on " + m.oldHost + `: team "` + teamName + `" (` + r.teamRef + `) moved to `,
		"wrote " + teamfile.FileName + ": it names ",
	} {
		if !strings.Contains(res.stdout, need) {
			t.Errorf("the move's lines lack %q:\n%s", need, res.stdout)
		}
	}
	if strings.Contains(res.stdout, "took team") {
		t.Errorf("the run that found the team on the server says it took it in:\n%s", res.stdout)
	}
	m.movedFile(server)
	// One member, the administrator: the two requests whose answers were lost
	// and the one that was answered made one principal between them.
	if members := m.serverRoster(r.configDir, server); len(members) != 1 || members[aliceLabel] == "" {
		t.Fatalf("the team's roster on the server after three take-ins: %v", members)
	}
	if _, err := os.Lstat(keyFile); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the retry key's file after the take-in was answered: %v", err)
	}
	if front.answersLost() != 2 {
		t.Fatalf("the front lost %d answers, want the two it was told to lose", front.answersLost())
	}

	// --- 4. a member's next session follows, and the team works -------------------------------------------
	ticket, ticketRead := m.ticket()
	m.followsAndTalks(server)
	m.noSecrets(ticket, r.secretFile, ticketRead, filepath.Join(r.root, "teams", r.teamRef, "move.json"))
	// The retry key was in one file for as long as it was needed, and nowhere else.
	argv, _ := os.ReadFile(r.argvLog)
	if strings.Contains(m.said.String(), minted.Key) || bytes.Contains(argv, []byte(minted.Key)) {
		t.Errorf("the retry key is in what a team move printed or on an adapter child's argv")
	}
	if hits := tokenHits(t, minted.Key, r.dirs.Root); len(hits) != 0 {
		t.Errorf("the retry key is still in %v", hits)
	}
}
