//go:build !windows

// The mail gateway installer's Resend inbox step (card 175): scripts/gateway-install.sh, step 3.
//
// A fresh team's first `make gateway-install` creates the team's Resend inbox and needs that inbox's receiving
// address for everything after it: the gateways row, the function secrets, the team file. Resend's answer to
// the create carries the new inbox's id and no address, and its list carries none either; only an inbox read
// by its id does. Until card 175 the installer read the address from the create answer, so every first run
// stopped after it had created the inbox, with an error naming two causes that were not the cause.
//
// Where the fake's shapes come from: the list and the inbox read by id were measured on a real account on
// 2026-11-10 (key names only: a list item has no receiving_address, an inbox read by id has one), and so was
// the refusal a plan with no inbox left gives the create (HTTP 403 with statusCode, message and name). The
// create's success answer could not be measured that day, for that refusal, so it is the one Resend's API
// reference documents: object and id.
//
// Every case runs the real script to its end on a restricted PATH. Its `curl` records argv, sends the three
// hosts the installer talks to (Resend, the Management API, the project) to one httptest.Server and refuses any
// other; the Supabase CLI is a stand-in that records what it was asked. There is no live case: a real run
// creates an inbox, deploys functions and rewrites a project's secrets.
package ci_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	gatewayInstallScriptRel = "scripts/gateway-install.sh"
	gatewayInstallRef       = "abcdefghijklmnopqrst"
	gatewayInstallTeamRef   = "0b9d6c1e-4f6a-4e0b-9d0a-6a1f1c2d3e4f"
	gatewayInstallFrom      = "team@example.invalid"
	// The address a forwarding inbox receives at: what the installer has to end with.
	gatewayInstallReceiving   = "f3a91c@inbound.example.invalid"
	gatewayInstallNewID       = "b3e2b2b6-3f0e-4c8e-9ad3-2f43a1e2c7f1"
	gatewayInstallPlaceholder = "<a Resend-managed receiving address, created on the real run>"

	// Fakes shaped so that scripts/ci/no-secrets.sh, which scans this file too, reads none of them as a key.
	gatewayInstallFakeToken   = "tok_fake_gateway_install_access_token"
	gatewayInstallFakeKey     = "tok_fake_gateway_install_resend_key"
	gatewayInstallFakeSigning = "tok_fake_gateway_install_signing_secret"

	// Resend's answer to a create on a plan with no inbox left, as measured on 2026-11-10 (HTTP 403).
	gatewayInstallPlanRefusal = `{"statusCode":403,"message":"You have reached the inboxes included in your plan. Upgrade your Transactional plan to unlock pay-as-you-go for inboxes.","name":"validation_error"}`
	// The error line the installer printed for every failure of this step before card 175.
	gatewayInstallOldGuess = "API key may lack full access"
)

// ---------------------------------------------------------------------------------------------------------
// the fake: Resend, the Management API and the project, behind one server

type gatewayInstallInbox struct {
	id, email string
	// receiving is the inbox's receiving address; empty reads back as null, as an inbox without forwarding does.
	receiving string
}

type gatewayInstallRequest struct{ method, path, body string }

type gatewayInstallServer struct {
	mu       sync.Mutex
	url      string
	inboxes  []gatewayInstallInbox
	requests []gatewayInstallRequest
	// createStatus and createBody, when set, are Resend's whole answer to the create, and no inbox is made
	// unless createStillMakes says the inbox was made all the same.
	createStatus     int
	createBody       string
	createStillMakes bool
	// installed makes the team one whose gateway is installed: its gateways row carries the receiving address
	// and its gateway member, as a run that got to the end leaves them.
	installed bool
	// createdReceiving is the receiving address of the inbox a create makes.
	createdReceiving string
}

func newGatewayInstallServer(t *testing.T) *gatewayInstallServer {
	t.Helper()
	gs := &gatewayInstallServer{createdReceiving: gatewayInstallReceiving}
	srv := httptest.NewServer(http.HandlerFunc(gs.serve))
	t.Cleanup(srv.Close)
	gs.url = srv.URL
	return gs
}

func (gs *gatewayInstallServer) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	gs.mu.Lock()
	gs.requests = append(gs.requests, gatewayInstallRequest{method: r.Method, path: r.URL.Path, body: string(body)})
	status, out := gs.answer(r.Method, r.URL.Path, string(body))
	gs.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

// answer is called with the lock held.
func (gs *gatewayInstallServer) answer(method, path, body string) (int, string) {
	const stamp = "2026-11-10T09:12:44.512Z"
	switch {
	// Resend. A list item carries fewer fields than an inbox: no receiving_address, no domain_id, no drafts.
	case method == http.MethodGet && path == "/resend/inboxes":
		items := make([]map[string]any, 0, len(gs.inboxes))
		for _, in := range gs.inboxes {
			items = append(items, map[string]any{
				"id": in.id, "name": "Brigade mail gateway", "email_address": in.email, "from_name": "Brigade",
				"unread": 0, "last_received": nil, "created_at": stamp, "updated_at": stamp,
			})
		}
		return http.StatusOK, gatewayInstallJSON(map[string]any{"object": "list", "has_more": false, "data": items})
	case method == http.MethodGet && strings.HasPrefix(path, "/resend/inboxes/"):
		for _, in := range gs.inboxes {
			if in.id != strings.TrimPrefix(path, "/resend/inboxes/") {
				continue
			}
			var receiving any
			if in.receiving != "" {
				receiving = in.receiving
			}
			return http.StatusOK, gatewayInstallJSON(map[string]any{
				"object": "inbox", "id": in.id, "name": "Brigade mail gateway", "email_address": in.email,
				"domain_id": "d91cd9bd-1176-453e-8fc1-35364d380206", "receiving_address": receiving,
				"from_name": "Brigade", "unread": 0, "drafts": 0, "last_received": nil,
				"created_at": stamp, "updated_at": stamp,
			})
		}
		return http.StatusNotFound, `{"statusCode":404,"message":"Inbox not found","name":"not_found"}`
	case method == http.MethodPost && path == "/resend/inboxes":
		if gs.createBody != "" && !gs.createStillMakes {
			return gs.createStatus, gs.createBody
		}
		var req struct {
			EmailAddress string `json:"email_address"`
		}
		_ = json.Unmarshal([]byte(body), &req)
		gs.inboxes = append(gs.inboxes, gatewayInstallInbox{id: gatewayInstallNewID, email: req.EmailAddress, receiving: gs.createdReceiving})
		if gs.createBody != "" {
			return gs.createStatus, gs.createBody
		}
		return http.StatusCreated, `{"object":"inbox","id":"` + gatewayInstallNewID + `"}`
	case method == http.MethodGet && path == "/resend/webhooks":
		return http.StatusOK, `{"object":"list","has_more":false,"data":[]}`
	case method == http.MethodPost && path == "/resend/webhooks":
		return http.StatusCreated, `{"object":"webhook","id":"4dd369bc-aa82-4ff3-97de-514ae3000ee0","signing_secret":"` + gatewayInstallFakeSigning + `"}`

	// The Management API: a project with no function secrets and a team with no gateway yet.
	case method == http.MethodGet && path == "/mapi/v1/projects/"+gatewayInstallRef+"/secrets":
		return http.StatusOK, `[]`
	case method == http.MethodPost && path == "/mapi/v1/projects/"+gatewayInstallRef+"/database/query":
		if gs.installed && strings.Contains(body, "select inbox, public_address from brigade_gateway.gateways") {
			return http.StatusCreated, gatewayInstallJSON([]map[string]any{{"inbox": gatewayInstallReceiving, "public_address": gatewayInstallReceiving}})
		}
		if gs.installed && strings.Contains(body, "select user_id::text as user_id") {
			return http.StatusCreated, `[{"user_id":"5e0a7c0e-0d0b-4a53-9a0e-0f6a3f0c9d11"}]`
		}
		if strings.Contains(body, "select session_id::text as s") {
			return http.StatusCreated, `[{"s":"7c1f0f3a-52c7-4b3e-8a57-3f1f8f5f2a10"}]`
		}
		return http.StatusCreated, `[]`

	// The project: the anonymous sign-up of the gateway principal, and the first tick.
	case method == http.MethodPost && path == "/project/auth/v1/signup":
		return http.StatusOK, `{"user":{"id":"5e0a7c0e-0d0b-4a53-9a0e-0f6a3f0c9d11"}}`
	case method == http.MethodPost && path == "/project/functions/v1/mail-out":
		return http.StatusOK, `{"heartbeat":true,"delivered":0,"undeliverable":0,"retry":0}`
	}
	return http.StatusNotFound, `{"message":"the fake has no such path"}`
}

func gatewayInstallJSON(v any) string {
	out, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(out)
}

func (gs *gatewayInstallServer) seen() []gatewayInstallRequest {
	gs.mu.Lock()
	defer gs.mu.Unlock()
	return append([]gatewayInstallRequest(nil), gs.requests...)
}

// gatewayInstallIndexes returns the positions, in the order the fake saw them, of the requests with that method
// and path.
func gatewayInstallIndexes(reqs []gatewayInstallRequest, method, path string) []int {
	var out []int
	for i, r := range reqs {
		if r.method == method && r.path == path {
			out = append(out, i)
		}
	}
	return out
}

// ---------------------------------------------------------------------------------------------------------
// running the script

type gatewayInstallRun struct {
	result
	// argv is every curl invocation's arguments, as the stand-in recorded them.
	argv string
	// supabase is every invocation of the Supabase CLI stand-in, and the inbox line of a secrets file it was given.
	supabase string
	// team is the team file as the run left it.
	team map[string]any
}

// runGatewayInstall runs the repository's installer for a fresh team against one fake, with the extra arguments.
func runGatewayInstall(t *testing.T, gs *gatewayInstallServer, args ...string) gatewayInstallRun {
	t.Helper()
	root := t.TempDir()
	if strings.ContainsAny(root, " \t\n") {
		// The installer splits $SUPABASE into words, as it must for `npx --yes supabase@<version>`.
		t.Skipf("the temp directory %q has white space in it: the Supabase CLI stand-in cannot be named in $SUPABASE", root)
	}
	// The installer hashes with the first of these it finds; the restricted PATH carries that one.
	hash := ""
	for _, tool := range []string{"openssl", "shasum", "sha256sum"} {
		if _, err := exec.LookPath(tool); err == nil {
			hash = tool
			break
		}
	}
	if hash == "" {
		t.Skip("none of openssl, shasum and sha256sum is on PATH: the installer cannot hash")
	}
	dir := restrictedPath(t, root, "jq", "od", "sed", "head", "mktemp", "rm", "tr", "cat", "cut", hash)
	realCurl, err := exec.LookPath("curl")
	if err != nil {
		t.Skipf("curl is not on PATH: cannot run the installer cases (%v)", err)
	}

	argvRecord := filepath.Join(root, "curl-argv.log")
	curl := "#!/bin/sh\n" +
		"# The card 175 curl stand-in: record argv, send each host the installer talks to to the fake, refuse any\n" +
		"# other so that no case reaches a real service. Absolute paths only: it runs on the restricted PATH.\n" +
		"{\n" +
		"  printf 'ARGV'\n" +
		"  for a in \"$@\"; do printf '\\t%s' \"$a\"; done\n" +
		"  printf '\\n'\n" +
		"} >> \"" + argvRecord + "\"\n" +
		"n=$#\n" +
		"for a in \"$@\"; do\n" +
		"  case $a in\n" +
		"    https://api.resend.com/*) a=\"" + gs.url + "/resend/${a#https://api.resend.com/}\" ;;\n" +
		"    https://api.supabase.com/*) a=\"" + gs.url + "/mapi/${a#https://api.supabase.com/}\" ;;\n" +
		"    https://" + gatewayInstallRef + ".supabase.co/*) a=\"" + gs.url + "/project/${a#https://" + gatewayInstallRef + ".supabase.co/}\" ;;\n" +
		"    http://*|https://*) echo \"curl stand-in: no case may reach $a\" >&2; exit 97 ;;\n" +
		"  esac\n" +
		"  set -- \"$@\" \"$a\"\n" +
		"done\n" +
		"shift \"$n\"\n" +
		"exec \"" + realCurl + "\" \"$@\"\n"
	testutil.WriteExecutable(t, filepath.Join(dir, "curl"), []byte(curl))

	// The Supabase CLI stand-in is launched as `/bin/sh <file>` (invariants.md: a fixture that is a shell script
	// is read, not exec'd).
	supabaseRecord := filepath.Join(root, "supabase.log")
	stub := filepath.Join(root, "supabase-stand-in.sh")
	stubBody := "# The Supabase CLI stand-in: record what was asked, and the inbox line of a secrets file it is handed.\n" +
		"{\n" +
		"  printf 'SUPABASE'\n" +
		"  for a in \"$@\"; do printf '\\t%s' \"$a\"; done\n" +
		"  printf '\\n'\n" +
		"} >> \"" + supabaseRecord + "\"\n" +
		"prev=''\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$prev\" = --env-file ]; then sed -n 's/^BRIGADE_MAIL_INBOX=/INBOX=/p' \"$a\" >> \"" + supabaseRecord + "\"; fi\n" +
		"  prev=$a\n" +
		"done\n"
	if err := os.WriteFile(stub, []byte(stubBody), 0o600); err != nil {
		t.Fatalf("writing the Supabase CLI stand-in: %v", err)
	}

	teamFile := filepath.Join(root, ".brigade.json")
	team := gatewayInstallJSON(map[string]any{
		"team_ref": gatewayInstallTeamRef, "team_name": "fixture",
		"url": "https://" + gatewayInstallRef + ".supabase.co", "publishable_key": "tok_fake_gateway_install_publishable",
	})
	if err := os.WriteFile(teamFile, []byte(team), 0o600); err != nil {
		t.Fatalf("writing the team file: %v", err)
	}
	home := filepath.Join(root, ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	env := baseEnv(t, home,
		"PATH="+dir,
		"TMPDIR="+root,
		"SUPABASE=/bin/sh "+stub,
		"SUPABASE_ACCESS_TOKEN="+gatewayInstallFakeToken,
		"RESEND_API_KEY="+gatewayInstallFakeKey,
	)
	script := filepath.Join(testutil.RepoRoot(t), gatewayInstallScriptRel)
	full := append([]string{gatewayInstallRef, "--from", "Fixture <" + gatewayInstallFrom + ">", "--team-file", teamFile}, args...)
	run := gatewayInstallRun{result: runScriptAtWithArgs(t, script, root, env, full...)}

	for path, into := range map[string]*string{argvRecord: &run.argv, supabaseRecord: &run.supabase} {
		data, err := os.ReadFile(path)
		if err != nil && !os.IsNotExist(err) {
			t.Fatalf("reading %s: %v", path, err)
		}
		*into = string(data)
	}
	data, err := os.ReadFile(teamFile)
	if err != nil {
		t.Fatalf("reading the team file back: %v", err)
	}
	if err := json.Unmarshal(data, &run.team); err != nil {
		t.Fatalf("the team file is not JSON after the run: %v\n%s", err, data)
	}
	return run
}

// ---------------------------------------------------------------------------------------------------------
// assertions

// gatewayInstallNoCredentials is asserted by every case: a failing run is the one most likely to echo an answer,
// and the installer now quotes Resend's own words in its error line.
func gatewayInstallNoCredentials(t *testing.T, run gatewayInstallRun) {
	t.Helper()
	for what, text := range map[string]string{"the installer's output": run.all(), "curl's argv": run.argv, "the Supabase CLI's argv": run.supabase} {
		for _, secret := range []string{gatewayInstallFakeToken, gatewayInstallFakeKey, gatewayInstallFakeSigning} {
			if strings.Contains(text, secret) {
				t.Errorf("%s contains %q, which is a credential and must never appear there\n%s", what, secret, text)
			}
		}
	}
}

// gatewayInstallStoppedAtTheInbox asserts a failed inbox step left everything after it untouched: no gateway
// principal signed up, no function secret set, no gateway address written into the team file.
func gatewayInstallStoppedAtTheInbox(t *testing.T, gs *gatewayInstallServer, run gatewayInstallRun) {
	t.Helper()
	if run.code != 1 {
		t.Errorf("exit %d, want 1\n%s", run.code, run.all())
	}
	if strings.Contains(run.all(), gatewayInstallOldGuess) {
		t.Errorf("the error still guesses at the API key and the dashboard instead of saying what was checked:\n%s", run.stderr)
	}
	if n := len(gatewayInstallIndexes(gs.seen(), http.MethodPost, "/project/auth/v1/signup")); n != 0 {
		t.Errorf("%d gateway principal(s) signed up after the inbox step failed", n)
	}
	if strings.Contains(run.supabase, "\tsecrets\t") {
		t.Errorf("function secrets were set after the inbox step failed:\n%s", run.supabase)
	}
	if _, ok := run.team["gateway"]; ok {
		t.Errorf("the team file gained a gateway after the inbox step failed: %v", run.team)
	}
}

// ---------------------------------------------------------------------------------------------------------
// the cases

func TestGatewayInstallResendInbox(t *testing.T) {
	t.Parallel()

	t.Run("a fresh team's first run reads the new inbox back and completes", func(t *testing.T) {
		t.Parallel()
		gs := newGatewayInstallServer(t)
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantPass(t, run.result, "receiving address:    "+gatewayInstallReceiving)

		reqs := gs.seen()
		creates := gatewayInstallIndexes(reqs, http.MethodPost, "/resend/inboxes")
		if len(creates) != 1 {
			t.Fatalf("%d creates of an inbox, want exactly 1", len(creates))
		}
		var body struct {
			EmailAddress string `json:"email_address"`
			Forwarding   bool   `json:"forwarding"`
		}
		if err := json.Unmarshal([]byte(reqs[creates[0]].body), &body); err != nil || body.EmailAddress != gatewayInstallFrom || !body.Forwarding {
			t.Errorf("the create asked for %q (err %v), want a forwarding inbox for %s", reqs[creates[0]].body, err, gatewayInstallFrom)
		}
		reads := gatewayInstallIndexes(reqs, http.MethodGet, "/resend/inboxes/"+gatewayInstallNewID)
		if len(reads) != 1 || reads[0] < creates[0] {
			t.Errorf("the new inbox was read back by its id at %v, want once and after the create at %d", reads, creates[0])
		}

		// The address read back is the one everything after the inbox step is given.
		row := false
		for _, r := range reqs {
			if strings.Contains(r.body, "insert into brigade_gateway.gateways") && strings.Contains(r.body, "'"+gatewayInstallReceiving+"'") {
				row = true
			}
		}
		if !row {
			t.Errorf("no gateways row was written with the receiving address %s", gatewayInstallReceiving)
		}
		if !strings.Contains(run.supabase, "INBOX="+gatewayInstallReceiving+"\n") {
			t.Errorf("the function secrets do not carry the receiving address:\n%s", run.supabase)
		}
		if gw, _ := run.team["gateway"].(map[string]any); gw["email"] != gatewayInstallReceiving {
			t.Errorf("the team file's gateway is %v, want email %s", run.team["gateway"], gatewayInstallReceiving)
		}
	})

	t.Run("a create whose answer carries no id is settled by the list", func(t *testing.T) {
		t.Parallel()
		// The create's success answer is the one shape that could not be measured: the installer must not
		// depend on it to find an inbox Resend did make.
		gs := newGatewayInstallServer(t)
		gs.createStatus, gs.createBody, gs.createStillMakes = http.StatusAccepted, `{"object":"inbox"}`, true
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantPass(t, run.result, "receiving address:    "+gatewayInstallReceiving)
		reqs := gs.seen()
		creates := gatewayInstallIndexes(reqs, http.MethodPost, "/resend/inboxes")
		lists := gatewayInstallIndexes(reqs, http.MethodGet, "/resend/inboxes")
		if len(creates) != 1 || len(lists) != 2 || lists[1] < creates[0] {
			t.Errorf("creates at %v and lists at %v, want one create and a list after it", creates, lists)
		}
	})

	t.Run("a create Resend refuses stops with Resend's own reason", func(t *testing.T) {
		t.Parallel()
		gs := newGatewayInstallServer(t)
		gs.createStatus, gs.createBody = http.StatusForbidden, gatewayInstallPlanRefusal
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantFail(t, run.result,
			"Resend did not create an inbox for "+gatewayInstallFrom,
			"carries no id",
			"its list has no inbox for that address",
			"Resend said: You have reached the inboxes included in your plan.")
		gatewayInstallStoppedAtTheInbox(t, gs, run)
	})

	t.Run("Resend's reason stays on the one error line", func(t *testing.T) {
		t.Parallel()
		// A message with a line break and a backslash escape in it: `echo` would act on the escape in dash and
		// the break would split the line the administrator reads.
		gs := newGatewayInstallServer(t)
		gs.createStatus = http.StatusUnprocessableEntity
		gs.createBody = `{"statusCode":422,"message":"line one\nline two \\c tail","name":"validation_error"}`
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantFail(t, run.result, `; Resend said: line one line two \c tail (or pass --inbox <address>)`)
		gatewayInstallStoppedAtTheInbox(t, gs, run)
		if n := strings.Count(run.stderr, "\n"); n != 1 {
			t.Errorf("the error is %d lines, want 1:\n%s", n, run.stderr)
		}
	})

	t.Run("a create answer that is not JSON stops with what was checked", func(t *testing.T) {
		t.Parallel()
		gs := newGatewayInstallServer(t)
		gs.createStatus, gs.createBody = http.StatusBadGateway, "<html><body>502 Bad Gateway</body></html>"
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantFail(t, run.result, "Resend did not create an inbox for "+gatewayInstallFrom, "carries no id")
		gatewayInstallStoppedAtTheInbox(t, gs, run)
		if strings.Contains(run.stderr, "jq:") {
			t.Errorf("the run died in jq instead of reporting the step:\n%s", run.stderr)
		}
	})

	t.Run("a new inbox read back without a receiving address stops with what was checked", func(t *testing.T) {
		t.Parallel()
		gs := newGatewayInstallServer(t)
		gs.createdReceiving = ""
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantFail(t, run.result, "the Resend inbox for "+gatewayInstallFrom, "no receiving address", "read by its id")
		gatewayInstallStoppedAtTheInbox(t, gs, run)
		if n := len(gatewayInstallIndexes(gs.seen(), http.MethodGet, "/resend/inboxes/"+gatewayInstallNewID)); n != 1 {
			t.Errorf("the new inbox was read back %d times, want 1", n)
		}
	})

	t.Run("an inbox for the from address without a receiving address is never created again", func(t *testing.T) {
		t.Parallel()
		// The run the error above recommends: the inbox is there, and still has no address. A second create
		// would answer with a duplicate, or make one.
		gs := newGatewayInstallServer(t)
		gs.inboxes = []gatewayInstallInbox{{id: "7c9e6679-7425-40de-944b-e07fc1f90ae7", email: gatewayInstallFrom}}
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantFail(t, run.result, "the Resend inbox for "+gatewayInstallFrom, "no receiving address", "read by its id")
		gatewayInstallStoppedAtTheInbox(t, gs, run)
		if n := len(gatewayInstallIndexes(gs.seen(), http.MethodPost, "/resend/inboxes")); n != 0 {
			t.Errorf("%d creates of an inbox that already exists, want 0", n)
		}
	})

	t.Run("the second run of card 175 reads the inbox the first one made and creates none", func(t *testing.T) {
		t.Parallel()
		// What the card saw complete: no gateways row yet, and the inbox for the from address in Resend's list.
		gs := newGatewayInstallServer(t)
		gs.inboxes = []gatewayInstallInbox{
			{id: "0f8fad5b-d9cb-469f-a165-70867728950e", email: "other@example.invalid", receiving: "9d02aa@inbound.example.invalid"},
			{id: "7c9e6679-7425-40de-944b-e07fc1f90ae7", email: gatewayInstallFrom, receiving: gatewayInstallReceiving},
		}
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantPass(t, run.result, "receiving address:    "+gatewayInstallReceiving)
		if n := len(gatewayInstallIndexes(gs.seen(), http.MethodPost, "/resend/inboxes")); n != 0 {
			t.Errorf("%d creates of an inbox on a second run, want 0", n)
		}
	})

	t.Run("a re-run of an installed gateway keeps its address and asks Resend for no inbox", func(t *testing.T) {
		t.Parallel()
		gs := newGatewayInstallServer(t)
		gs.installed = true
		run := runGatewayInstall(t, gs)
		gatewayInstallNoCredentials(t, run)
		wantPass(t, run.result,
			"keeping the receiving address the installed gateway already uses",
			"the team already has a gateway member; keeping it",
			"receiving address:    "+gatewayInstallReceiving)
		for _, r := range gs.seen() {
			if strings.HasPrefix(r.path, "/resend/inboxes") || r.path == "/project/auth/v1/signup" {
				t.Errorf("a re-run of an installed gateway sent %s %s", r.method, r.path)
			}
		}
	})

	t.Run("a dry run creates nothing and names the address as still to come", func(t *testing.T) {
		t.Parallel()
		gs := newGatewayInstallServer(t)
		run := runGatewayInstall(t, gs, "--dry-run")
		gatewayInstallNoCredentials(t, run)
		wantPass(t, run.result, "receiving address:    "+gatewayInstallPlaceholder)
		for _, r := range gs.seen() {
			switch {
			case strings.HasPrefix(r.path, "/project/"), strings.HasPrefix(r.path, "/resend/") && r.method != http.MethodGet:
				t.Errorf("a dry run sent %s %s", r.method, r.path)
			case strings.Contains(r.body, "insert into brigade_gateway.gateways"), strings.Contains(r.body, "cron.schedule"), strings.Contains(r.body, "create extension"):
				t.Errorf("a dry run sent the Management API a statement that writes: %s", r.body)
			}
		}
		// The Supabase CLI is asked for the migrations' dry run and nothing else: no deploy, no secret.
		if want := "SUPABASE\tdb\tpush\t--dry-run\t--project-ref\t" + gatewayInstallRef + "\n"; run.supabase != want {
			t.Errorf("a dry run asked the Supabase CLI for\n%s\nwant\n%s", run.supabase, want)
		}
		if _, ok := run.team["gateway"]; ok {
			t.Errorf("a dry run wrote a gateway into the team file: %v", run.team)
		}
	})
}
