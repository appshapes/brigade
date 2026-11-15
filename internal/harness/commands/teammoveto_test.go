//go:build !windows

package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
	"github.com/appshapes/brigade/internal/harness/teamstore/write"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// `brigade team move --to` (work item 328): the fixture's team t_9 is on a
// Supabase project, the checkout's team file names that project, and its
// administrator moves it to a Brigade server with one command.

// moveToLabel is the label the administrator has on the backend the team
// leaves. It is not the address of the Claude account the terminal's
// profile is signed in to (accountEmail), which is the point of it.
const moveToLabel = "ops-admin@example.org"

// moveToSecret is the team's join secret, and moveToTicket the ticket the
// server answers when the move is opened: both put together here.
var (
	moveToSecret = strings.Join([]string{"brg1", setupTeamRef, strings.Repeat("cd", 16)}, ".")
	moveToTicket = strings.Join([]string{"brg1", setupTeamRef, strings.Repeat("ef", 16)}, ".")
)

// moveToRig is the administrator's machine before the move.
type moveToRig struct {
	t          *testing.T
	f          *fixture
	top        string
	file       string // the checkout's team file
	original   string // its bytes before the move
	oldURL     string
	oldHost    string
	newURL     string
	newHost    string
	oldKey     string
	newKey     string
	secretFile string
	tokenFile  string
	ticketFile string // where the command keeps the ticket
	prompts    []string
	// ticket is what the server's adapter writes to --ticket-file; a test
	// empties it to make an adapter that writes nothing.
	ticket string
}

// newMoveToRig joins the team on the Supabase project it leaves, with the
// label a member chose, writes the team file as `team create` and a person
// left it, and the secret's file.
func newMoveToRig(t *testing.T) *moveToRig {
	t.Helper()
	f := newFixture(t)
	sum := sha256.Sum256([]byte(t.Name()))
	id := hex.EncodeToString(sum[:6])
	r := &moveToRig{
		t: t, f: f, top: mkCheckout(t, f.dirs.Root),
		oldHost: "old" + id + ".supabase.co", newHost: "new" + id + ".example.test:8743",
		secretFile: filepath.Join(f.dirs.Root, "ops.secret"), tokenFile: filepath.Join(f.dirs.Root, "create.token"),
		ticket: moveToTicket,
	}
	r.oldURL, r.newURL = "https://"+r.oldHost, "https://"+r.newHost
	r.oldKey, r.newKey = teamstore.Key("supabase", r.oldURL, setupTeamRef), teamstore.Key("rest", r.newURL, setupTeamRef)
	r.file = filepath.Join(r.top, teamfile.FileName)
	r.ticketFile = filepath.Join(f.dirs.BrigadeState, moveTicketDir, r.newKey+".ticket")
	r.original = "{\n" +
		"  \"version\": 1,\n" +
		"  \"adapter\": \"supabase\",\n" +
		"  \"url\": \"" + r.oldURL + "\",\n" +
		"  \"publishable_key\": \"sb_publishable_x\",\n" +
		"  \"team_ref\": \"" + setupTeamRef + "\",\n" +
		"  \"team_name\": \"devs\",\n" +
		"  \"sync\": { \"folders\": [\".brigade\"] },\n" +
		"  \"later\": true\n" +
		"}\n"
	r.writeFile(r.original)
	if err := write.EnsureBinding(f.dirs.BrigadeConfig, r.oldKey, "supabase", r.oldURL, "sb_publishable_x", "", setupTeamRef, "devs", "p_old"); err != nil {
		t.Fatal(err)
	}
	r.setLabel(moveToLabel)
	canon, err := teamfile.Canonicalize(r.top)
	if err != nil {
		t.Fatal(err)
	}
	if err := write.Pin(f.dirs.BrigadeConfig, canon, teamstore.Pin{
		Adapter: "supabase", URL: r.oldURL, PublishableKey: "sb_publishable_x", TeamRef: setupTeamRef, ConsentedAt: fixtureNow,
	}); err != nil {
		t.Fatal(err)
	}
	writeSecretFile(t, r.secretFile, moveToSecret)
	if err := os.WriteFile(r.tokenFile, []byte("creation-token-value-MUST-NOT-LEAK\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Both adapters answer the recorder's one describe: the backend the team
	// leaves keeps a record of moves, and the server's adapter takes a team in.
	f.rec.on("describe", okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.move", "team.take_in"))))
	f.rec.on("team moved", okAnswer(r.notMoved()))
	// The probe's clear, then the record.
	f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(r.recorded()))
	f.rec.on("session list", okAnswer(r.sessions()))
	f.rec.on("team take-in", okAnswer(r.takenIn(true, true)))
	return r
}

// writeFile writes the checkout's team file.
func (r *moveToRig) writeFile(doc string) {
	r.t.Helper()
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(r.file, []byte(doc), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

// setLabel gives the membership on the old backend its label, as the
// adapter that joined it wrote it.
func (r *moveToRig) setLabel(label string) {
	r.t.Helper()
	p, err := adapterkit.LoadProfile(r.f.dirs.BrigadeConfig, r.oldKey)
	if err != nil {
		r.t.Fatal(err)
	}
	p.HumanLabel = label
	if err := adapterkit.SaveProfile(r.f.dirs.BrigadeConfig, r.oldKey, p); err != nil {
		r.t.Fatal(err)
	}
}

func (r *moveToRig) notMoved() string {
	return `{"team_ref":"` + setupTeamRef + `","moved":false}`
}

// recorded is the old backend's answer to a record of the rig's move.
func (r *moveToRig) recorded() string {
	return `{"team_ref":"` + setupTeamRef + `","moved":true,"adapter":"rest","url":"` + r.newURL + `","certificate":"` + moveNewPin + `","recorded_at":"2026-11-14T12:00:00Z"}`
}

// takenIn is the server's answer to the take-in.
func (r *moveToRig) takenIn(taken, owner bool) string {
	b, err := json.Marshal(map[string]any{
		"team_ref": setupTeamRef, "team_name": "devs", "principal_ref": "p_new", "taken_in": taken, "owner": owner,
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// sessions is the old backend's roster: one session on each version the
// rule reads, and three it does not.
func (r *moveToRig) sessions() string {
	rec := func(id, name, label string, version any) map[string]any {
		m := map[string]any{
			"session_id": id, "session_name": name, "human_label": label, "principal_ref": id[:8] + "-principal",
			"state": "active", "activity": "busy", "inbound": "accept",
			"last_seen_at": fixtureNow, "lease_until": fixtureNow.Add(time.Minute), "created_at": fixtureNow.Add(-time.Hour), "is_self": false,
		}
		if version != nil {
			m["brigade_version"] = version
		}
		return m
	}
	b, err := json.Marshal(map[string]any{
		"team_ref": setupTeamRef, "team_name": "devs", "server_time": fixtureNow, "truncated": false,
		"sessions": []any{
			rec("bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "billing", "bob@example.com", "0.54.9"),
			rec("cccccccccccccccccccccccccccccccc", "ci", "", "0.9.1"),
			rec("dddddddddddddddddddddddddddddddd", "current", "dana@example.com", "0.55.0"),
			rec("eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee", "dev-build", "erin@example.com", "0.55.0-dev"),
			rec("ffffffffffffffffffffffffffffffff", "future", "finn@example.com", "1.0.0"),
			rec("11111111111111111111111111111111", "silent", "gil@example.com", nil),
			rec("22222222222222222222222222222222", "odd", "hal@example.com", "unknown"),
		},
	})
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// spawn is the rig's adapter: the recorder, and for `team open-move` what
// the server's adapter does beside its answer, which is to write the ticket
// to the file it was told.
func (r *moveToRig) spawn(ctx context.Context, spec adapterkit.SpawnSpec) (*adapterkit.SpawnResult, error) {
	if verbKey(spec.Argv) == "team open-move" {
		path := ""
		if i := slices.Index(spec.Argv, "--ticket-file"); i >= 0 && i+1 < len(spec.Argv) {
			path = spec.Argv[i+1]
		}
		if r.ticket != "" {
			if err := os.WriteFile(path, []byte(r.ticket+"\n"), 0o600); err != nil {
				r.t.Errorf("the ticket's file could not be written where the command named it: %v", err)
			}
		}
		if !r.f.rec.scripted("team open-move") {
			r.f.rec.on("team open-move", okAnswer(`{"team_ref":"`+setupTeamRef+`","team_name":"devs","ends_at":"2026-12-14T12:00:00Z","replaced":false,"ticket_file":"`+path+`"}`))
		}
	}
	return r.f.rec.spawn(ctx, spec)
}

// scripted reports whether a verb has answers.
func (r *spawnRecorder) scripted(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.answers[key]) > 0
}

// args is the command line of the move the rig makes.
func (r *moveToRig) args(extra ...string) []string {
	return append([]string{"--to", r.newURL, "--secret-file", r.secretFile, "--certificate", opensslSpelling(moveNewPin)}, extra...)
}

// inv is the administrator's terminal running `brigade team move …`, with
// the confirmation answered as told. The terminal's Claude profile is
// signed in to accountEmail and its shell names a label of its own: neither
// is the member's label on the old backend.
func (r *moveToRig) inv(confirm bool, args ...string) Invocation {
	iv := r.f.inv(r.f.terminalEnv("CLAUDE_CONFIG_DIR="+r.f.dirs.ClaudeConfig, "BRIGADE_LABEL=terminal-label"), "", append([]string{"move"}, args...)...)
	iv.Deps.Getwd = func() (string, error) { return r.top, nil }
	iv.Deps.IsTerminal = func(io.Reader) bool { return true }
	iv.Deps.Spawn = r.spawn
	iv.Deps.Confirm = func(prompt string) (bool, error) {
		r.prompts = append(r.prompts, prompt)
		return confirm, nil
	}
	return iv
}

// asked lists the adapter commands the recorder saw, in order, but for
// describe.
func (r *moveToRig) asked() []string {
	var out []string
	for _, verb := range r.f.rec.verbs() {
		if verb != "describe" {
			out = append(out, verb)
		}
	}
	return out
}

// pin is the checkout's pin.
func (r *moveToRig) pin() teamstore.Pin {
	r.t.Helper()
	canon, err := teamfile.Canonicalize(r.top)
	if err != nil {
		r.t.Fatal(err)
	}
	pin, ok, err := teamstore.LookupPin(r.f.dirs.BrigadeConfig, canon)
	if err != nil || !ok {
		r.t.Fatalf("the checkout's pin: %v %v", ok, err)
	}
	return *pin
}

// fileNow is the team file's bytes.
func (r *moveToRig) fileNow() string {
	r.t.Helper()
	b, err := os.ReadFile(r.file)
	if err != nil {
		r.t.Fatal(err)
	}
	return string(b)
}

// moved is the team file after the move.
func (r *moveToRig) moved() string {
	return "{\n" +
		"  \"version\": 1,\n" +
		"  \"adapter\": \"rest\",\n" +
		"  \"url\": \"" + r.newURL + "\",\n" +
		"  \"certificate\": \"" + moveNewPin + "\",\n" +
		"  \"team_ref\": \"" + setupTeamRef + "\",\n" +
		"  \"team_name\": \"devs\",\n" +
		"  \"sync\": { \"folders\": [\".brigade\"] },\n" +
		"  \"later\": true\n" +
		"}\n"
}

// unwritten fails the test when a refused move left a trace: the team
// file, the pin and the store are as they were, the ticket's file is not
// there, and neither the open nor a record was asked.
func (r *moveToRig) unwritten(storeBefore string) {
	r.t.Helper()
	if got := r.fileNow(); got != r.original {
		r.t.Errorf("a refused move rewrote the team file:\n%s", got)
	}
	if pin := r.pin(); pin.Adapter != "supabase" || pin.URL != r.oldURL {
		r.t.Errorf("a refused move rewrote the pin: %+v", pin)
	}
	if got := storeHash(r.t, r.f.dirs.BrigadeConfig); got != storeBefore {
		r.t.Errorf("a refused move wrote the store")
	}
	if _, err := os.Lstat(r.ticketFile); err == nil {
		r.t.Errorf("a refused move left a ticket file")
	}
	for _, spec := range r.f.rec.specsOf("team move") {
		if slices.Contains(spec.Argv, "--ticket-file") {
			r.t.Errorf("a refused move recorded it")
		}
	}
	if slices.Contains(r.asked(), "team open-move") {
		r.t.Errorf("a refused move opened it: %v", r.asked())
	}
}

// noSecrets fails the test when the join secret, the ticket or the
// creation token is in anything the command said, asked or put on an
// adapter's argv.
func (r *moveToRig) noSecrets() {
	r.t.Helper()
	var all strings.Builder
	all.WriteString(r.f.out.String() + r.f.errb.String() + strings.Join(r.prompts, "\n"))
	for i := range r.f.rec.count() {
		all.WriteString("\n" + argvOf(r.f.rec.spec(r.t, i)))
	}
	said := all.String()
	for name, hidden := range map[string]string{
		"the join secret": moveToSecret, "its secret part": strings.Repeat("cd", 16),
		"the ticket": moveToTicket, "its secret part ": strings.Repeat("ef", 16),
		"the creation token": "creation-token-value-MUST-NOT-LEAK",
	} {
		if strings.Contains(said, hidden) {
			r.t.Errorf("%s is in an output, a question or an adapter's argv", name)
		}
	}
}

// TestTeamMoveToMovesTheTeam: the whole move in the design's order. The
// old backend is asked, and changed by nothing, before the administrator is;
// the server takes the team in under the team's key there, with the secret
// in the stdin document and the destination as its backend; the ticket
// travels in a file of Brigade's own state directory and is gone at the
// end; the record names what was typed; and only then are the team file,
// the binding and the pin written, the file with every other byte kept.
func TestTeamMoveToMovesTheTeam(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	// Where the label came from is recorded beside the old membership.
	if err := write.LabelOrigin(r.f.dirs.BrigadeConfig, r.oldKey, teamstore.LabelOrigin{
		Source: teamstore.LabelFromFlag, SentAt: time.Date(2026, 9, 6, 18, 0, 0, 0, time.UTC), LabelSHA256: teamstore.LabelDigest(moveToLabel),
	}); err != nil {
		t.Fatal(err)
	}
	if err := Team(r.inv(true, r.args("--create-token-file", r.tokenFile, "--ticket-days", "7")...)); err != nil {
		t.Fatal(err)
	}
	want := []string{"team moved", "team move", "session list", "team take-in", "team open-move", "team move"}
	if got := r.asked(); !slices.Equal(got, want) {
		t.Fatalf("adapter commands: %v, want %v", got, want)
	}
	if len(r.prompts) != 1 {
		t.Fatalf("%d confirmations, want one", len(r.prompts))
	}
	for _, need := range []string{`move team "devs" (` + setupTeamRef + `) from ` + r.oldHost + ` to ` + r.newHost, `via adapter "rest"`, moveNewPin, "no command of theirs", "[y/N]"} {
		if !strings.Contains(r.prompts[0], need) {
			t.Errorf("the confirmation lacks %q: %s", need, r.prompts[0])
		}
	}

	// The probe: a clear, alone, under the old backend's profile.
	moves := r.f.rec.specsOf("team move")
	if !slices.Contains(moves[0].Argv, "--clear") || slices.Contains(moves[0].Argv, "--ticket-file") || len(moves[0].Stdin) != 0 {
		t.Fatalf("the probe: %s", argvOf(moves[0]))
	}
	profile := func(spec adapterkit.SpawnSpec) string {
		i := slices.Index(spec.Argv, "--profile")
		if i < 0 {
			return ""
		}
		return spec.Argv[i+1]
	}
	for _, verb := range []string{"team moved", "team move", "session list"} {
		for _, spec := range r.f.rec.specsOf(verb) {
			if profile(spec) != r.oldKey {
				t.Errorf("%s ran under %s, want the profile of the backend the team leaves", verb, profile(spec))
			}
		}
	}
	// The roster that is read is the sessions that are online: one that is
	// gone says nothing about the plugin its member runs today.
	if list := r.f.rec.specsOf("session list"); len(list) != 1 || slices.Contains(list[0].Argv, "--include-offline") {
		t.Errorf("the roster was read with the offline sessions: %v", r.asked())
	}

	// The take-in: the server's adapter, the team's key there, the secret and
	// the destination in the document, the token by its file's path.
	take := r.f.rec.specsOf("team take-in")[0]
	if i := slices.Index(take.Argv, "adapter"); i < 0 || take.Argv[i+1] != "rest" || profile(take) != r.newKey {
		t.Fatalf("the take-in's argv: %s", argvOf(take))
	}
	if i := slices.Index(take.Argv, "--create-token-file"); i < 0 || take.Argv[i+1] != r.tokenFile {
		t.Fatalf("the take-in does not name the creation token's file: %s", argvOf(take))
	}
	var sent struct {
		JoinSecret string            `json:"join_secret"`
		TeamName   string            `json:"team_name"`
		HumanLabel string            `json:"human_label"`
		Backend    map[string]string `json:"backend"`
	}
	if err := json.Unmarshal(take.Stdin, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.JoinSecret != moveToSecret || sent.TeamName != "devs" || sent.HumanLabel != moveToLabel ||
		len(sent.Backend) != 2 || sent.Backend["url"] != r.newURL || sent.Backend["certificate"] != moveNewPin {
		t.Fatalf("the take-in's document: name %q, label %q, backend %v", sent.TeamName, sent.HumanLabel, sent.Backend)
	}

	// The open and the record: one file, under Brigade's state directory.
	open := r.f.rec.specsOf("team open-move")[0]
	if profile(open) != r.newKey || !strings.Contains(argvOf(open), "--ticket-file "+r.ticketFile+" --ticket-days 7") || len(open.Stdin) != 0 {
		t.Fatalf("the open's argv: %s", argvOf(open))
	}
	record := moves[1]
	if profile(record) != r.oldKey || !strings.Contains(argvOf(record), "--ticket-file "+r.ticketFile) || slices.Contains(record.Argv, "--clear") {
		t.Fatalf("the record's argv: %s", argvOf(record))
	}
	if got := string(record.Stdin); got != `{"adapter":"rest","url":"`+r.newURL+`","certificate":"`+moveNewPin+`"}` {
		t.Fatalf("the destination recorded: %s", got)
	}
	if _, err := os.Lstat(r.ticketFile); err == nil {
		t.Fatalf("the ticket's file is still there")
	}
	if info, err := os.Stat(filepath.Dir(r.ticketFile)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("the ticket's directory: %v %v", info, err)
	}

	// What was written, and only then.
	if got := r.fileNow(); got != r.moved() {
		t.Fatalf("the team file:\n%s\nwant:\n%s", got, r.moved())
	}
	f, err := teamfile.Parse(r.file)
	if err != nil {
		t.Fatal(err)
	}
	pin := r.pin()
	if !pin.Matches(f) || pin.MovedFrom != "" || !pin.ConsentedAt.Equal(fixtureNow) {
		t.Fatalf("the pin: %+v", pin)
	}
	b, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, r.newKey)
	if err != nil || teamstore.VerifyBinding(b, f) != nil || b.PrincipalRef != "p_new" || b.TeamName != "devs" {
		t.Fatalf("the binding at the server: %+v, %v", b, err)
	}
	if old, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, r.oldKey); err != nil || old.TeamRef != setupTeamRef {
		t.Fatalf("the membership on the old backend: %+v, %v", old, err)
	}
	// The label is the same text from the same source, and its record goes
	// with it, dated by this command.
	if origin, err := teamstore.LoadLabelOrigin(r.f.dirs.BrigadeConfig, r.newKey); err != nil || origin.Source != teamstore.LabelFromFlag ||
		!origin.Describes(moveToLabel) || !origin.SentAt.Equal(fixtureNow) {
		t.Fatalf("the label's record at the server: %+v, %v", origin, err)
	}

	out := r.f.out.String()
	for _, need := range []string{
		`took team "devs" (` + setupTeamRef + `) in on ` + r.newHost + `: you own it there`,
		"sent your display label: " + moveToLabel,
		"opened its move: the ticket admits the team's members until 2026-12-14",
		"recorded on " + r.oldHost + `: team "devs" (` + setupTeamRef + `) moved to ` + r.newHost,
		"wrote " + teamfile.FileName + ": it names " + r.newHost,
		"left to do:",
		"commit and push " + teamfile.FileName + " in every repository of the team",
		"2 sessions that are online run a plugin older than 0.55.0",
		"billing (bob@example.com, 0.54.9), ci ([cccccccc], 0.9.1)",
		"to undo: `brigade team move --clear`",
	} {
		if !strings.Contains(out, need) {
			t.Errorf("the lines lack %q:\n%s", need, out)
		}
	}
	for _, absent := range []string{"current", "dev-build", "future", "silent", "odd", "gateway", accountEmail, "terminal-label"} {
		if strings.Contains(out, absent) {
			t.Errorf("the lines carry %q:\n%s", absent, out)
		}
	}
	r.noSecrets()
}

// TestTeamMoveToSendsTheLabelOfTheOldBackend: the server is told the label
// this member has on the backend the team leaves, as a session that follows
// tells it, and never one the terminal offers: not the address of the
// Claude account its profile is signed in to, not the shell's
// BRIGADE_LABEL (work item 326). A member with no label there sends none.
func TestTeamMoveToSendsTheLabelOfTheOldBackend(t *testing.T) {
	t.Parallel()
	for name, label := range map[string]string{"a label the member chose": moveToLabel, "no label": ""} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.setLabel(label)
			// The profile of the terminal is signed in to another account.
			writeAccountFile(t, r.f.dirs.ClaudeConfig)
			if err := Team(r.inv(true, r.args()...)); err != nil {
				t.Fatal(err)
			}
			if got := sentLabel(t, r.f, "team take-in"); got != label {
				t.Fatalf("the take-in sent the label %q, want the old backend's %q", got, label)
			}
			if stdin := string(r.f.rec.specsOf("team take-in")[0].Stdin); strings.Contains(stdin, accountEmail) || strings.Contains(stdin, "terminal-label") {
				t.Fatalf("the take-in carries a label of the terminal's")
			}
			out := r.f.out.String()
			if label == "" && !strings.Contains(out, "sent no display label") {
				t.Fatalf("the lines do not say that no label was sent:\n%s", out)
			}
			if strings.Contains(out, accountEmail) {
				t.Fatalf("the lines name the terminal's account:\n%s", out)
			}
		})
	}
}

// TestTeamMoveToRefusesBeforeAnythingIsAsked: each refusal the command
// makes from argv, the team file, the store and the secret's file spawns no
// adapter, asks nothing and leaves everything as it was.
func TestTeamMoveToRefusesBeforeAnythingIsAsked(t *testing.T) {
	t.Parallel()
	// says is what a `usage` refusal has to say, where another rule would
	// refuse the same argv with the same code.
	says := map[string]string{ //nolint:gosec // G101: the names of test cases and the refusals' fixed texts, not credentials
		"no secret file":                                      "team move --to needs --secret-file <path>",
		"a secret on the command line":                        "join secrets must never be passed on the command line",
		"a relative secret file":                              "--secret-file must be an absolute path",
		"a secret file inside the checkout":                   "--secret-file must not be inside this project",
		"a secret file inside the checkout that is not there": "--secret-file must not be inside this project",
		"a secret file with a .. in it":                       "--secret-file must not contain a `..` component",
		"a server over plain http":                            "--to must use https",
		"a url with a query":                                  "with no credentials, query or fragment",
		"a url with credentials":                              "with no credentials, query or fragment",
		"another scheme":                                      "--to must use https",
		"a pin that is not one":                               "--certificate must be the SHA-256 fingerprint",
		"a pin for a server over http":                        "--certificate goes with an https --to",
		"no days":                                             "--ticket-days must be between 1 and 365",
		"too many days":                                       "--ticket-days must be between 1 and 365",
		"a relative creation token file":                      "--create-token-file must be an absolute path",
		"a creation token file inside the checkout":           "--create-token-file must be a plainly spelled path outside this project",
		"the secret of another team":                          "name the file `team create` wrote for this team",
	}
	for _, tc := range []struct {
		name   string
		args   func(r *moveToRig) []string
		code   protocol.Code
		reason string
	}{
		{"no secret file", func(r *moveToRig) []string { return []string{"--to", r.newURL} }, protocol.CodeUsage, ""},
		{"a secret on the command line", func(r *moveToRig) []string { return []string{"--to", r.newURL, "--secret-file", moveToSecret} }, protocol.CodeUsage, ""},
		{"a relative secret file", func(r *moveToRig) []string { return []string{"--to", r.newURL, "--secret-file", "ops.secret"} }, protocol.CodeUsage, ""},
		{"a secret file inside the checkout", func(r *moveToRig) []string {
			inside := filepath.Join(r.top, "ops.secret")
			writeSecretFile(r.t, inside, moveToSecret)
			return []string{"--to", r.newURL, "--secret-file", inside}
		}, protocol.CodeUsage, ""},
		{"a secret file inside the checkout that is not there", func(r *moveToRig) []string {
			return []string{"--to", r.newURL, "--secret-file", filepath.Join(r.top, "no-such.secret")}
		}, protocol.CodeUsage, ""},
		{"a secret file with a .. in it", func(r *moveToRig) []string {
			return []string{"--to", r.newURL, "--secret-file", r.f.dirs.Root + "/x/../ops.secret"}
		}, protocol.CodeUsage, ""},
		{"a secret file that is not there", func(r *moveToRig) []string {
			return []string{"--to", r.newURL, "--secret-file", filepath.Join(r.f.dirs.Root, "no-such.secret")}
		}, protocol.CodeConfig, "secret_file_missing"},
		{"a file that holds no secret", func(r *moveToRig) []string {
			writeSecretFile(r.t, r.secretFile, "not a secret")
			return r.args()
		}, protocol.CodeInvalidInput, ""},
		{"the secret of another team", func(r *moveToRig) []string {
			writeSecretFile(r.t, r.secretFile, strings.Replace(moveToSecret, setupTeamRef, "t_8", 1))
			return r.args()
		}, protocol.CodeInvalidInput, "secret_team_mismatch"},
		{"a server over plain http", func(r *moveToRig) []string {
			return []string{"--to", "http://" + r.newHost, "--secret-file", r.secretFile}
		}, protocol.CodeUsage, ""},
		{"a url with a query", func(r *moveToRig) []string {
			return []string{"--to", r.newURL + "?x=1", "--secret-file", r.secretFile}
		}, protocol.CodeUsage, ""},
		{"a url with credentials", func(r *moveToRig) []string {
			return []string{"--to", "https://user:pw@" + r.newHost, "--secret-file", r.secretFile}
		}, protocol.CodeUsage, ""},
		{"a url with no host", func(r *moveToRig) []string { return []string{"--to", "https://", "--secret-file", r.secretFile} }, protocol.CodeUsage, ""},
		{"another scheme", func(r *moveToRig) []string {
			return []string{"--to", "ftp://" + r.newHost, "--secret-file", r.secretFile}
		}, protocol.CodeUsage, ""},
		{"a pin that is not one", func(r *moveToRig) []string {
			return []string{"--to", r.newURL, "--secret-file", r.secretFile, "--certificate", "sha256:abc"}
		}, protocol.CodeUsage, ""},
		{"a pin for a server over http", func(r *moveToRig) []string {
			return []string{"--to", "http://127.0.0.1:8743", "--secret-file", r.secretFile, "--certificate", moveNewPin}
		}, protocol.CodeUsage, ""},
		{"no days", func(r *moveToRig) []string { return r.args("--ticket-days", "0") }, protocol.CodeUsage, ""},
		{"too many days", func(r *moveToRig) []string { return r.args("--ticket-days", "366") }, protocol.CodeUsage, ""},
		{"a relative creation token file", func(r *moveToRig) []string { return r.args("--create-token-file", "create.token") }, protocol.CodeUsage, ""},
		{"a creation token file inside the checkout", func(r *moveToRig) []string {
			return r.args("--create-token-file", filepath.Join(r.top, "create.token"))
		}, protocol.CodeUsage, ""},
		{"a creation token file that is not there", func(r *moveToRig) []string {
			return r.args("--create-token-file", filepath.Join(r.f.dirs.Root, "no-such.token"))
		}, protocol.CodeConfig, "create_token_file"},
		{"two forms", func(r *moveToRig) []string { return r.args("--clear") }, protocol.CodeUsage, ""},
		{"the secret file with another form", func(r *moveToRig) []string { return []string{"--show", "--secret-file", r.secretFile} }, protocol.CodeUsage, ""},
		{"the days with another form", func(*moveToRig) []string { return []string{"--clear", "--ticket-days", "7"} }, protocol.CodeUsage, ""},
		{"the pin with another form", func(*moveToRig) []string { return []string{"--show", "--certificate", moveNewPin} }, protocol.CodeUsage, ""},
		{"no team file", func(r *moveToRig) []string {
			if err := os.Remove(r.file); err != nil {
				r.t.Fatal(err)
			}
			return r.args()
		}, protocol.CodeConfig, "team_file_missing"},
		{"the machine never joined the team", func(r *moveToRig) []string {
			if err := os.RemoveAll(filepath.Join(r.f.dirs.BrigadeConfig, "teams")); err != nil {
				r.t.Fatal(err)
			}
			return r.args()
		}, protocol.CodeConfig, reasonNotJoinedAtSource},
		{"the membership is not the one the file describes", func(r *moveToRig) []string {
			if err := write.PatchBindingBackend(r.f.dirs.BrigadeConfig, r.oldKey, "supabase", r.oldURL, "sb_publishable_other", ""); err != nil {
				r.t.Fatal(err)
			}
			return r.args()
		}, protocol.CodeConfig, reasonNotJoinedAtSource},
		{"a --from that names another backend", func(r *moveToRig) []string { return r.args("--from", "elsewhere.supabase.co") }, protocol.CodeConfig, "team_unknown"},
		{"a file that names the server, and no other membership", func(r *moveToRig) []string {
			r.writeFile(r.moved())
			if err := os.RemoveAll(filepath.Join(r.f.dirs.BrigadeConfig, "teams")); err != nil {
				r.t.Fatal(err)
			}
			return r.args()
		}, protocol.CodeConfig, "team_unknown"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			args := tc.args(r)
			fileBefore, storeBefore := "", storeHash(t, r.f.dirs.BrigadeConfig)
			if b, err := os.ReadFile(r.file); err == nil {
				fileBefore = string(b)
			}
			perr := wantCodeErr(t, Team(r.inv(true, args...)), tc.code, tc.reason)
			if need := says[tc.name]; !strings.Contains(perr.Message, need) {
				t.Fatalf("the refusal lacks %q: %s", need, perr.Message)
			}
			for _, hidden := range []string{strings.Repeat("cd", 16), "MUST-NOT-LEAK", "user:pw"} {
				if strings.Contains(perr.Message, hidden) {
					t.Fatalf("the refusal carries %q", hidden)
				}
			}
			if r.f.rec.count() != 0 || len(r.prompts) != 0 {
				t.Fatalf("a refusal spawned %v and asked %d times", r.f.rec.verbs(), len(r.prompts))
			}
			if b, err := os.ReadFile(r.file); err == nil && string(b) != fileBefore {
				t.Fatalf("a refusal rewrote the team file")
			}
			if storeHash(t, r.f.dirs.BrigadeConfig) != storeBefore {
				t.Fatalf("a refusal wrote the store")
			}
		})
	}
}

// TestTeamMoveToTicketDays: the days a move stays open are 1 to 365, both
// ends included, and go to the server's adapter as they were typed.
func TestTeamMoveToTicketDays(t *testing.T) {
	t.Parallel()
	for _, days := range []string{"1", "365"} {
		t.Run(days, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			if err := Team(r.inv(true, r.args("--ticket-days", days)...)); err != nil {
				t.Fatal(err)
			}
			if open := r.f.rec.specsOf("team open-move"); len(open) != 1 || !strings.HasSuffix(argvOf(open[0]), "--ticket-days "+days) {
				t.Fatalf("the open: %v", r.asked())
			}
		})
	}
}

// TestTeamMoveToRefusesASecretFileLinkedIntoTheCheckout: a path outside
// the checkout that is a link to a file inside it is inside it.
func TestTeamMoveToRefusesASecretFileLinkedIntoTheCheckout(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	inside := filepath.Join(r.top, "ops.secret")
	writeSecretFile(t, inside, moveToSecret)
	link := filepath.Join(r.f.dirs.Root, "linked.secret")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	perr := wantCodeErr(t, Team(r.inv(true, "--to", r.newURL, "--secret-file", link)), protocol.CodeUsage, "")
	if !strings.Contains(perr.Message, "--secret-file must not be inside this project") || r.f.rec.count() != 0 {
		t.Fatalf("a secret file linked into the checkout: %s, spawned %v", perr.Message, r.f.rec.verbs())
	}
}

// TestTeamMoveToStopsBeforeAnythingIsWritten: each refusal that comes back
// from an adapter stops the move before the server holds a move, before the
// old backend holds a record and before the checkout is touched, and says
// what to do about it.
func TestTeamMoveToStopsBeforeAnythingIsWritten(t *testing.T) {
	t.Parallel()
	type refusal struct {
		name    string
		setup   func(r *moveToRig)
		confirm bool
		args    func(r *moveToRig) []string
		code    protocol.Code
		reason  string
		says    []string
		// taken says whether the server was asked to take the team in.
		taken bool
	}
	plain := func(r *moveToRig) []string { return r.args() }
	for _, tc := range []refusal{
		{name: "the old backend keeps no record of moves", confirm: true, args: plain,
			setup: func(r *moveToRig) {
				r.f.rec.on("describe", okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.take_in"))))
			},
			code: protocol.CodeConfig, reason: "no_team_move", says: []string{"team.move"}},
		{name: "the server's adapter takes no team in", confirm: true, args: plain,
			setup: func(r *moveToRig) {
				r.f.rec.on("describe",
					okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.move"))),
					okAnswer(string(fakeadapter.DescribeJSON(protocol.ProtocolVersion, "team.roster", "team.admin"))))
			},
			code: protocol.CodeConfig, reason: reasonNoTeamTakeIn, says: []string{"team.take_in", "--ticket-file"}},
		{name: "a project without the migration", confirm: true, args: plain,
			setup: func(r *moveToRig) {
				r.f.rec.on("team move", answer{errObj: &protocol.ErrorObject{
					Code: protocol.CodeConfig, Message: "this backend cannot record a team's move",
					Details: map[string]string{"reason": "migration_drift"},
				}})
			},
			code: protocol.CodeConfig, reason: reasonMigrationDrift,
			says: []string{"20261114120000_team_move", "The project's owner applies it with `make backend-install project=", "Nothing was changed"}},
		{name: "a member who did not create the team", confirm: true, args: plain,
			setup: func(r *moveToRig) {
				r.f.rec.on("team move", failAnswer(protocol.CodeUnauthorized, "not an active member of this team"))
			},
			code: protocol.CodeUnauthorized, reason: reasonNotTheCreator, says: []string{"only the member who created this team", "Nothing was changed"}},
		{name: "the old backend cannot be reached", confirm: true, args: plain,
			setup: func(r *moveToRig) {
				r.f.rec.on("team moved", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
			},
			code: protocol.CodeUnavailable},
		{name: "the roster cannot be read", confirm: true, args: plain,
			setup: func(r *moveToRig) {
				r.f.rec.on("session list", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
			},
			code: protocol.CodeUnavailable},
		{name: "the administrator says no", confirm: false, args: plain, code: protocol.CodeUsage, reason: "declined", says: []string{"not moved; nothing was changed"}},
		{name: "a server that takes no team in by request", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) {
				r.f.rec.on("team take-in", answer{errObj: &protocol.ErrorObject{
					Code: protocol.CodeConfig, Message: "the server does not offer taking a team in by request",
					Details: map[string]string{"reason": "server_lacks_team_take_in"},
				}})
				r.f.rec.on("profile status", okAnswer(`{"name":"x","state":"unauthenticated"}`))
				r.f.rec.on("profile reset", okAnswer(`{"name":"x","state":"unconfigured"}`))
			},
			code: protocol.CodeConfig, reason: reasonServerLacksTakeIn,
			says: []string{"older than 0.7.0", "brigade-server --import-team", "brigade-server --open-move " + setupTeamRef + " --ticket-file", "brigade team move --ticket-file <path>", "Nothing was recorded"}},
		{name: "a team of that reference under another secret", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) {
				r.f.rec.on("team take-in", answer{errObj: &protocol.ErrorObject{
					Code: protocol.CodeConflict, Message: "a team of that reference is here under another join secret",
					Details: map[string]string{"reason": "other_join_secret"},
				}})
				r.f.rec.on("profile status", okAnswer(`{"name":"x","state":"unauthenticated"}`))
				r.f.rec.on("profile reset", okAnswer(`{"name":"x","state":"unconfigured"}`))
			},
			code: protocol.CodeConflict, reason: reasonOtherJoinSecret, says: []string{"under another join secret", "tell the server's operator", "Nothing was recorded"}},
		{name: "a server that asks for a creation token", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) {
				r.f.rec.on("team take-in", failAnswer(protocol.CodeUnauthorized, "this server does not let this caller create a team"))
				r.f.rec.on("profile status", okAnswer(`{"name":"x","state":"unauthenticated"}`))
				r.f.rec.on("profile reset", okAnswer(`{"name":"x","state":"unconfigured"}`))
			},
			code: protocol.CodeUnauthorized, reason: reasonCreateToken, says: []string{"it asks for a creation token", "--create-token-file <path>"}},
		{name: "a creation token the server refuses", confirm: true, taken: true,
			args: func(r *moveToRig) []string { return r.args("--create-token-file", r.tokenFile) },
			setup: func(r *moveToRig) {
				r.f.rec.on("team take-in", failAnswer(protocol.CodeUnauthorized, "this server does not let this caller create a team"))
				r.f.rec.on("profile status", okAnswer(`{"name":"x","state":"unauthenticated"}`))
				r.f.rec.on("profile reset", okAnswer(`{"name":"x","state":"unconfigured"}`))
			},
			code: protocol.CodeUnauthorized, reason: reasonCreateToken, says: []string{"it refused the creation token", "ask its operator for the current one"}},
		{name: "a server that cannot be reached", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) {
				r.f.rec.on("team take-in", failAnswer(protocol.CodeUnavailable, "the backend could not be reached"))
				r.f.rec.on("profile status", okAnswer(`{"name":"x","state":"unauthenticated"}`))
				r.f.rec.on("profile reset", okAnswer(`{"name":"x","state":"unconfigured"}`))
			},
			code: protocol.CodeUnavailable, says: []string{"did not take the team in: the backend could not be reached", "Nothing was recorded"}},
		{name: "a team that someone else owns on the server", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) { r.f.rec.on("team take-in", okAnswer(r.takenIn(false, false))) },
			code:  protocol.CodeConflict, reason: reasonNotOwnerThere,
			says: []string{"is held on ", "and owned by someone else", "the server's operator can name its owner", "brigade-server --set-owner " + setupTeamRef + " --principal p_new", "Nothing was recorded"}},
		{name: "a server that answers another team", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) {
				r.f.rec.on("team take-in", okAnswer(strings.Replace(r.takenIn(true, true), setupTeamRef, "t_8", 1)))
			},
			code: protocol.CodeProtocolMismatch, reason: "bad_result"},
		{name: "a server that says it took the team in for someone else", confirm: true, args: plain, taken: true,
			setup: func(r *moveToRig) { r.f.rec.on("team take-in", okAnswer(r.takenIn(true, false))) },
			code:  protocol.CodeProtocolMismatch, reason: "bad_result"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			if tc.setup != nil {
				tc.setup(r)
			}
			storeBefore := storeHash(t, r.f.dirs.BrigadeConfig)
			perr := wantCodeErr(t, Team(r.inv(tc.confirm, tc.args(r)...)), tc.code, tc.reason)
			for _, need := range tc.says {
				if !strings.Contains(perr.Message, need) {
					t.Errorf("the refusal lacks %q: %s", need, perr.Message)
				}
			}
			if got := slices.Contains(r.asked(), "team take-in"); got != tc.taken {
				t.Errorf("the server asked to take the team in: %v, want %v (%v)", got, tc.taken, r.asked())
			}
			r.unwritten(storeBefore)
			if out := r.f.out.String(); strings.Contains(out, "took") || strings.Contains(out, "recorded") || strings.Contains(out, "wrote") {
				t.Errorf("a refused move printed a success line:\n%s", out)
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToNamesTheProjectToMigrate: the refusal of a project without
// the migration names the command with the project's own reference, read
// off its host, and with a placeholder for a host that is no hosted
// project's.
func TestTeamMoveToNamesTheProjectToMigrate(t *testing.T) {
	t.Parallel()
	drift := &protocol.Error{Code: protocol.CodeConfig, Message: "x", Details: map[string]string{"reason": "migration_drift"}}
	for url, want := range map[string]string{
		"https://abcdefghijklmnopqrst.supabase.co": "`make backend-install project=abcdefghijklmnopqrst`",
		"http://127.0.0.1:54321":                   "`make backend-install project=<the project's ref>`",
		"https://supabase.example.com":             "`make backend-install project=<the project's ref>`",
	} {
		m := &serverMove{source: &adapterkit.Profile{URL: url}}
		perr := wantCodeErr(t, m.cannotRecord(drift), protocol.CodeConfig, reasonMigrationDrift)
		if !strings.Contains(perr.Message, want) {
			t.Errorf("%s: the refusal lacks %s: %s", url, want, perr.Message)
		}
	}
}

// TestTeamMoveToKeepsAProfileThatHoldsACredential: a take-in that is
// refused removes the profile its adapter wrote only when the server issued
// it nothing. A profile that holds a credential is kept, because the server
// may have taken the team in for it before the answer was lost, and one
// that was there before the command is never touched.
func TestTeamMoveToKeepsAProfileThatHoldsACredential(t *testing.T) {
	t.Parallel()
	refuse := failAnswer(protocol.CodeUnavailable, "the backend could not be reached")
	t.Run("a credential was issued", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		r.f.rec.on("team take-in", refuse)
		r.f.rec.on("profile status", okAnswer(`{"name":"x","state":"not_member","principal_ref":"p_new"}`))
		wantCode(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if slices.Contains(r.asked(), "profile reset") {
			t.Fatalf("a profile that holds a credential was reset: %v", r.asked())
		}
	})
	t.Run("the profile was there before", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, r.newKey, "rest", r.newURL, "", moveNewPin, "", "", "p_left"); err != nil {
			t.Fatal(err)
		}
		r.f.rec.on("team take-in", refuse)
		wantCode(t, Team(r.inv(true, r.args()...)), protocol.CodeUnavailable, "")
		if asked := r.asked(); slices.Contains(asked, "profile status") || slices.Contains(asked, "profile reset") {
			t.Fatalf("a profile that was there before was looked at: %v", asked)
		}
	})
	t.Run("the profile is moved to the certificate that was typed", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		if err := write.EnsureBinding(r.f.dirs.BrigadeConfig, r.newKey, "rest", r.newURL, "", otherPin, "", "", "p_left"); err != nil {
			t.Fatal(err)
		}
		r.f.rec.on("profile init", okAnswer(`{"name":"x","state":"not_member"}`))
		if err := Team(r.inv(true, r.args()...)); err != nil {
			t.Fatal(err)
		}
		init := r.f.rec.specsOf("profile init")
		if len(init) != 1 || !strings.Contains(argvOf(init[0]), "--force --url "+r.newURL+" --certificate "+moveNewPin) {
			t.Fatalf("the profile was not moved to the certificate that was typed: %v", r.asked())
		}
		if i, j := slices.Index(r.asked(), "profile init"), slices.Index(r.asked(), "team take-in"); i < 0 || i > j {
			t.Fatalf("the take-in went before the profile was moved: %v", r.asked())
		}
	})
}

// TestTeamMoveToRunAgainEndsInTheSameState: stopped after any step, the
// same command run again finishes the move, and the checkout, the pin and
// the binding are what a move that was never stopped leaves.
func TestTeamMoveToRunAgainEndsInTheSameState(t *testing.T) {
	t.Parallel()
	unavailable := failAnswer(protocol.CodeUnavailable, "the backend could not be reached")
	// What a move that was never stopped leaves.
	whole := newMoveToRig(t)
	if err := Team(whole.inv(true, whole.args()...)); err != nil {
		t.Fatal(err)
	}
	same := func(t *testing.T, r *moveToRig) {
		t.Helper()
		if got := r.fileNow(); got != r.moved() {
			t.Errorf("the team file after the second run:\n%s", got)
		}
		pin, wantPin := r.pin(), whole.pin()
		if pin.Adapter != wantPin.Adapter || pin.URL != r.newURL || pin.Certificate != wantPin.Certificate || pin.TeamRef != wantPin.TeamRef ||
			pin.PublishableKey != "" || pin.MovedFrom != "" {
			t.Errorf("the pin after the second run: %+v", pin)
		}
		b, err := teamstore.LoadBinding(r.f.dirs.BrigadeConfig, r.newKey)
		if err != nil || b.Adapter != "rest" || b.URL != r.newURL || b.Certificate != moveNewPin || b.TeamRef != setupTeamRef || b.PrincipalRef != "p_new" {
			t.Errorf("the binding after the second run: %+v, %v", b, err)
		}
		if _, err := os.Lstat(r.ticketFile); err == nil {
			t.Errorf("the ticket's file is still there after the second run")
		}
		r.noSecrets()
	}
	for _, tc := range []struct {
		name  string
		stop  func(r *moveToRig)
		code  protocol.Code
		says  string
		after func(r *moveToRig) // what the stopped run left, checked before the second
		mend  func(r *moveToRig)
	}{
		{name: "after the take-in",
			stop: func(r *moveToRig) { r.f.rec.on("team open-move", unavailable) },
			code: protocol.CodeUnavailable, says: "is taken in on",
			after: func(r *moveToRig) {
				if got := r.fileNow(); got != r.original {
					r.t.Errorf("a move that was not opened rewrote the team file")
				}
				if pin := r.pin(); pin.Adapter != "supabase" {
					r.t.Errorf("a move that was not opened rewrote the pin: %+v", pin)
				}
			},
			mend: func(r *moveToRig) {
				r.f.rec.on("team open-move")
				// The second take-in is a join that still owns, and the second
				// record is the first one made.
				r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
				r.f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(r.recorded()))
			}},
		{name: "after the open, with a ticket file left behind",
			stop: func(r *moveToRig) { r.f.rec.on("team move", okAnswer(r.notMoved()), unavailable) },
			code: protocol.CodeUnavailable, says: "its move is open, and",
			after: func(r *moveToRig) {
				if got := r.fileNow(); got != r.original {
					r.t.Errorf("a move that was not recorded rewrote the team file")
				}
				// A run that was killed here leaves its ticket: the next one
				// writes over it and removes it.
				if err := os.WriteFile(r.ticketFile, []byte("a ticket of a run that was killed\n"), 0o600); err != nil {
					r.t.Fatal(err)
				}
			},
			mend: func(r *moveToRig) {
				r.f.rec.on("team open-move")
				r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
				r.f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(r.recorded()))
			}},
		{name: "after the record",
			stop: func(r *moveToRig) {
				// The checkout cannot be written: the team file's directory is
				// read-only for the first run.
				if err := os.Chmod(r.top, 0o500); err != nil { //nolint:gosec // G302: a directory the test makes read-only
					r.t.Fatal(err)
				}
				r.t.Cleanup(func() { _ = os.Chmod(r.top, 0o700) }) //nolint:gosec // G302: restored for the temp dir's removal
			},
			code: protocol.CodeInternal, says: "this checkout was not written",
			after: func(r *moveToRig) {
				if got := r.fileNow(); got != r.original {
					r.t.Errorf("the team file changed in a directory that cannot be written")
				}
				if pin := r.pin(); pin.Adapter != "supabase" {
					r.t.Errorf("the pin was written before the team file: %+v", pin)
				}
			},
			mend: func(r *moveToRig) {
				if err := os.Chmod(r.top, 0o700); err != nil { //nolint:gosec // G302: the directory is the test's own checkout
					r.t.Fatal(err)
				}
				r.f.rec.on("team open-move")
				r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
				// The record is there now: the probe's clear is not made.
				r.f.rec.on("team moved", okAnswer(r.recorded()))
				r.f.rec.on("team move", okAnswer(r.recorded()))
			}},
		{name: "after the team file, before the pin",
			stop: func(r *moveToRig) {
				// What such a run leaves, laid out by hand: the file names the
				// server, the pin still names the old backend.
				r.writeFile(r.moved())
				r.f.rec.on("team moved", unavailable)
			},
			code: protocol.CodeUnavailable,
			after: func(r *moveToRig) {
				if pin := r.pin(); pin.Adapter != "supabase" {
					r.t.Errorf("the pin moved: %+v", pin)
				}
			},
			mend: func(r *moveToRig) {
				r.f.rec.on("team take-in", okAnswer(r.takenIn(false, true)))
				r.f.rec.on("team moved", okAnswer(r.recorded()))
				r.f.rec.on("team move", okAnswer(r.recorded()))
			}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			tc.stop(r)
			perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), tc.code, "")
			if !strings.Contains(perr.Message, tc.says) || (tc.says != "" && !strings.Contains(perr.Message, "Run the same command again")) {
				t.Fatalf("the stopped run said: %s", perr.Message)
			}
			tc.after(r)
			tc.mend(r)
			r.f.out.Reset()
			if err := Team(r.inv(true, r.args()...)); err != nil {
				t.Fatalf("the second run: %v", err)
			}
			same(t, r)
			// The second run asked the old backend under its own profile,
			// whichever backend the file named by then.
			records := 0
			for _, spec := range r.f.rec.specsOf("team move") {
				if slices.Contains(spec.Argv, "--ticket-file") {
					records++
					if i := slices.Index(spec.Argv, "--profile"); spec.Argv[i+1] != r.oldKey {
						t.Errorf("a record was made under %s", spec.Argv[i+1])
					}
				}
			}
			if records == 0 {
				t.Errorf("the second run recorded nothing: %v", r.asked())
			}
		})
	}
	t.Run("a move that is finished", func(t *testing.T) {
		// Not parallel with the runs above: it is the rig they compare with.
		whole.f.rec.on("team take-in", okAnswer(whole.takenIn(false, true)))
		whole.f.rec.on("team moved", okAnswer(whole.recorded()))
		whole.f.rec.on("team move", okAnswer(whole.recorded()))
		whole.f.rec.on("team open-move", okAnswer(`{"team_ref":"`+setupTeamRef+`","team_name":"devs","ends_at":"2026-12-14T12:00:00Z","replaced":true,"ticket_file":"`+whole.ticketFile+`"}`))
		whole.f.out.Reset()
		if err := Team(whole.inv(true, whole.args()...)); err != nil {
			t.Fatal(err)
		}
		same(t, whole)
		out := whole.f.out.String()
		for _, need := range []string{"was on " + whole.newHost + " already, and you own it there", "opened its move again, which replaced the ticket it had", teamfile.FileName + " named " + whole.newHost + " already"} {
			if !strings.Contains(out, need) {
				t.Errorf("the lines of a move that was run again lack %q:\n%s", need, out)
			}
		}
	})
}

// TestTeamMoveToSaysWhatIsLeft: the gateways are named as the operator's
// when the team file has a gateway member, with the page that says how; a
// record of a move somewhere else is named before the administrator
// answers; and one session on an older plugin is one session.
func TestTeamMoveToSaysWhatIsLeft(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	r.original = strings.Replace(r.original, "  \"later\": true\n", "  \"gateway\": {\"email\": \"ops@example.com\"},\n  \"later\": true\n", 1)
	r.writeFile(r.original)
	r.f.rec.on("team moved", okAnswer(strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)))
	r.f.rec.on("team move", okAnswer(r.recorded()))
	one := strings.Replace(r.sessions(), `"0.9.1"`, `"0.55.3"`, 1)
	r.f.rec.on("session list", okAnswer(one))
	if err := Team(r.inv(true, r.args()...)); err != nil {
		t.Fatal(err)
	}
	if len(r.prompts) != 1 || !strings.Contains(r.prompts[0], "This replaces the record "+r.oldHost+" holds of a move to elsewhere.example.test") ||
		!strings.Contains(r.prompts[0], "1 session that is online runs a plugin older than 0.55.0") {
		t.Fatalf("the confirmation: %v", r.prompts)
	}
	// A record is there, so the probe's clear is not made: clearing it would
	// change something.
	for _, spec := range r.f.rec.specsOf("team move") {
		if slices.Contains(spec.Argv, "--clear") {
			t.Fatalf("a record that was there was cleared")
		}
	}
	out := r.f.out.String()
	for _, need := range []string{
		"has a gateway member: the mail and Slack gateways are the server operator's to configure on " + r.newHost,
		"docs/project-owner/server/gateways.md",
		"1 session that is online runs a plugin older than 0.55.0 and will not follow until its member updates it: billing (bob@example.com, 0.54.9)",
	} {
		if !strings.Contains(out, need) {
			t.Errorf("the lines lack %q:\n%s", need, out)
		}
	}
	if !strings.Contains(r.fileNow(), `"gateway": {"email": "ops@example.com"}`) {
		t.Errorf("the gateway member did not stay in the team file:\n%s", r.fileNow())
	}
}

// TestTeamMoveToJSON: with --json stdout is one envelope and nothing else.
func TestTeamMoveToJSON(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	iv := r.inv(true, r.args()...)
	iv.JSON = true
	if err := Team(iv); err != nil {
		t.Fatal(err)
	}
	if strings.Count(strings.TrimSpace(r.f.out.String()), "\n") != 0 {
		t.Fatalf("stdout is more than one line:\n%s", r.f.out.String())
	}
	ok, result := envelopeOf(t, r.f.out.String())
	to, _ := result["to"].(map[string]any)
	from, _ := result["from"].(map[string]any)
	older, _ := result["older_sessions"].([]any)
	if !ok || result["team_ref"] != setupTeamRef || result["taken_in"] != true || result["principal_ref"] != "p_new" ||
		to["url"] != r.newURL || to["adapter"] != "rest" || to["certificate"] != moveNewPin || from["url"] != r.oldURL ||
		result["team_file"] != r.file || len(older) != 2 || result["gateway"] != false || result["move_ends_at"] != "2026-12-14T12:00:00Z" {
		t.Fatalf("the result: %v", result)
	}
	r.noSecrets()
}

// moveSessionInv is `brigade team move …` run by the model inside a
// session of the administrator's: nothing can be asked, so the seams that
// ask fail the test.
func (r *moveToRig) moveSessionInv(args ...string) Invocation {
	iv := sessionInv(r.t, r.f, r.top, append([]string{"move"}, args...)...)
	iv.Deps.Spawn = r.spawn
	return iv
}

// startFactsWith writes the session's start facts with the option as the
// hook resolved it.
func (r *moveToRig) startFactsWith(option string) {
	r.t.Helper()
	if err := (sessionmap.Store{StateDir: r.f.stateDir}).WriteStart(&sessionmap.StartFacts{
		ClaudePID: fixturePID, ClaudeSessionID: "native-1", ConfigDir: r.f.dirs.BrigadeConfig,
		TeamMoveInSession: option, WrittenAt: fixtureNow,
	}); err != nil {
		r.t.Fatal(err)
	}
}

// TestTeamMoveToInsideASession: with the option on, which is its default,
// the whole move runs from a session. Nothing is asked: the line a terminal
// asks about is printed as a statement, and the move proceeds. The ticket
// is kept under the session's own state directory.
func TestTeamMoveToInsideASession(t *testing.T) {
	t.Parallel()
	for name, option := range map[string]string{"the option was never set": "", "the option is on": config.SyncOn} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			unattach(t, r.f)
			r.startFactsWith(option)
			r.ticketFile = filepath.Join(r.f.stateDir, moveTicketDir, r.newKey+".ticket")
			if err := Team(r.moveSessionInv(r.args()...)); err != nil {
				t.Fatal(err)
			}
			out := r.f.out.String()
			first, _, _ := strings.Cut(out, "\n")
			if !strings.HasPrefix(first, `move team "devs" (`+setupTeamRef+`) from `+r.oldHost+` to `+r.newHost) ||
				!strings.Contains(first, "no command of theirs") || !strings.HasSuffix(first, "Consented by this invocation.") || strings.Contains(first, "[y/N]") {
				t.Fatalf("the first line of a move from a session: %s", first)
			}
			if got := r.fileNow(); got != r.moved() {
				t.Fatalf("the team file:\n%s", got)
			}
			if pin := r.pin(); pin.Adapter != "rest" || pin.URL != r.newURL {
				t.Fatalf("the pin: %+v", pin)
			}
			open := r.f.rec.specsOf("team open-move")
			if len(open) != 1 || !strings.Contains(argvOf(open[0]), "--ticket-file "+r.ticketFile) {
				t.Fatalf("the open from a session: %v", r.asked())
			}
			if got := sentLabel(t, r.f, "team take-in"); got != moveToLabel {
				t.Fatalf("the take-in from a session sent the label %q", got)
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveInsideASessionFollowsTheOption: the three writing forms are
// refused inside a session whose member set team_move_in_session to off, or
// to a value that is neither word, before the team file is read and with
// nothing spawned; --show reads all the same. With the option on, the
// record and the clear run from a session too. A session with no start
// facts cannot read the option and is refused for that.
func TestTeamMoveInsideASessionFollowsTheOption(t *testing.T) {
	t.Parallel()
	forms := func(r *moveToRig) map[string][]string {
		return map[string][]string{"move": r.args(), "record": {"--ticket-file", r.secretFile}, "clear": {"--clear"}}
	}
	for name, option := range map[string]string{"off": config.SyncOff, "neither word": "sometimes"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			r.startFactsWith(option)
			storeBefore := storeHash(t, r.f.dirs.BrigadeConfig)
			for form, args := range forms(r) {
				perr := wantCodeErr(t, Team(r.moveSessionInv(args...)), protocol.CodeUsage, "in_session")
				if perr.Message != RefusalMoveInSession || !strings.Contains(perr.Message, "team_move_in_session") {
					t.Fatalf("%s in a session with the option %s: %q", form, name, perr.Message)
				}
			}
			if r.f.rec.count() != 0 {
				t.Fatalf("a refusal in a session spawned %v", r.f.rec.verbs())
			}
			r.unwritten(storeBefore)
			// Reading the record is no move.
			if err := Team(r.moveSessionInv("--show", "--from", r.oldHost)); err != nil || !strings.Contains(r.f.out.String(), "no move recorded on "+r.oldHost) {
				t.Fatalf("--show in a session with the option %s: %v\n%s", name, err, r.f.out.String())
			}
		})
	}
	t.Run("no start facts", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		for form, args := range forms(r) {
			wantCode(t, Team(r.moveSessionInv(args...)), protocol.CodeConfig, config.ReasonNotRegistered)
			if r.f.rec.count() != 0 {
				t.Fatalf("%s in a session with no start facts spawned %v", form, r.f.rec.verbs())
			}
		}
	})
	t.Run("on: the record and the clear run from a session", func(t *testing.T) {
		t.Parallel()
		r := newMoveRig(t, true)
		writeStartFacts(t, r.f, r.f.dirs.BrigadeConfig)
		r.f.rec.on("team move", okAnswer(r.recorded()), okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":false}`))
		iv := sessionInv(t, r.f, r.top, "move", "--ticket-file", r.ticket)
		if err := Team(iv); err != nil {
			t.Fatal(err)
		}
		out := r.f.out.String()
		first, _, _ := strings.Cut(out, "\n")
		if !strings.HasPrefix(first, `record that team "devs" (`+setupTeamRef+`) moved from `+r.oldHost+` to `+r.newHost) ||
			!strings.HasSuffix(first, "Consented by this invocation.") || !strings.Contains(out, "recorded on "+r.oldHost) {
			t.Fatalf("a record from a session:\n%s", out)
		}
		if err := Team(sessionInv(t, r.f, r.top, "move", "--clear")); err != nil || !strings.Contains(r.f.out.String(), "cleared: "+r.oldHost) {
			t.Fatalf("a clear from a session: %v\n%s", err, r.f.out.String())
		}
		if strings.Contains(r.f.out.String()+r.f.errb.String(), moveTicket) {
			t.Fatalf("the ticket reached a stream")
		}
	})
	t.Run("on, with --json: the statement goes to stderr", func(t *testing.T) {
		t.Parallel()
		r := newMoveToRig(t)
		unattach(t, r.f)
		r.startFactsWith("")
		r.ticketFile = filepath.Join(r.f.stateDir, moveTicketDir, r.newKey+".ticket")
		iv := r.moveSessionInv(r.args()...)
		iv.JSON = true
		if err := Team(iv); err != nil {
			t.Fatal(err)
		}
		if ok, _ := envelopeOf(t, r.f.out.String()); !ok || strings.Contains(r.f.out.String(), "Consented") ||
			!strings.Contains(r.f.errb.String(), "Consented by this invocation.") {
			t.Fatalf("stdout:\n%s\nstderr:\n%s", r.f.out.String(), r.f.errb.String())
		}
	})
}

// TestTeamMoveToRefusesAnAnswerItCannotUse: an open that answers another
// team, another file or no ticket, and a record that is not the one that
// was given, are protocol mismatches. The team is taken in by then, the
// refusal says so, and the checkout is not written.
func TestTeamMoveToRefusesAnAnswerItCannotUse(t *testing.T) {
	t.Parallel()
	opened := func(r *moveToRig) string {
		return `{"team_ref":"` + setupTeamRef + `","team_name":"devs","ends_at":"2026-12-14T12:00:00Z","replaced":false,"ticket_file":"` + r.ticketFile + `"}`
	}
	for name, tc := range map[string]struct {
		spoil func(r *moveToRig)
		code  protocol.Code
	}{
		"an open for another team": {func(r *moveToRig) {
			r.f.rec.on("team open-move", okAnswer(strings.Replace(opened(r), setupTeamRef, "t_8", 1)))
		}, protocol.CodeProtocolMismatch},
		"an open that names another file": {func(r *moveToRig) {
			r.f.rec.on("team open-move", okAnswer(strings.Replace(opened(r), r.ticketFile, "/elsewhere/ticket", 1)))
		}, protocol.CodeProtocolMismatch},
		"an open with no end": {func(r *moveToRig) {
			r.f.rec.on("team open-move", okAnswer(strings.Replace(opened(r), `"ends_at":"2026-12-14T12:00:00Z",`, "", 1)))
		}, protocol.CodeProtocolMismatch},
		"an open that wrote no ticket": {func(r *moveToRig) { r.ticket = "" }, protocol.CodeConfig},
		"an open that wrote the ticket of another team": {func(r *moveToRig) {
			r.ticket = strings.Replace(moveToTicket, setupTeamRef, "t_8", 1)
		}, protocol.CodeInvalidInput},
		"a record of another url": {func(r *moveToRig) {
			r.f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(strings.Replace(r.recorded(), r.newURL, "https://elsewhere.example.test", 1)))
		}, protocol.CodeProtocolMismatch},
		"a record with no certificate": {func(r *moveToRig) {
			r.f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(strings.Replace(r.recorded(), `,"certificate":"`+moveNewPin+`"`, "", 1)))
		}, protocol.CodeProtocolMismatch},
		"a record that says the team has not moved": {func(r *moveToRig) {
			r.f.rec.on("team move", okAnswer(r.notMoved()))
		}, protocol.CodeProtocolMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			r := newMoveToRig(t)
			tc.spoil(r)
			perr := wantCodeErr(t, Team(r.inv(true, r.args()...)), tc.code, "")
			if !strings.Contains(perr.Message, "the team is taken in on "+r.newHost) || !strings.Contains(perr.Message, "Run the same command again") {
				t.Fatalf("the refusal: %s", perr.Message)
			}
			if got := r.fileNow(); got != r.original {
				t.Fatalf("the team file was rewritten")
			}
			if pin := r.pin(); pin.Adapter != "supabase" {
				t.Fatalf("the pin was rewritten: %+v", pin)
			}
			if _, err := os.Lstat(r.ticketFile); err == nil {
				t.Fatalf("the ticket's file is still there")
			}
			if out := r.f.out.String(); strings.Contains(out, "wrote") || strings.Contains(out, "left to do") {
				t.Fatalf("a success line was printed:\n%s", out)
			}
			r.noSecrets()
		})
	}
}

// TestTeamMoveToFromAFileOnOneLine: the file `team create` writes is one
// line with its members in the encoder's order, and it stays one line; a
// server with no pin gets none; and a loopback server over http is a
// server.
func TestTeamMoveToFromAFileOnOneLine(t *testing.T) {
	t.Parallel()
	r := newMoveToRig(t)
	r.original = `{"adapter":"supabase","publishable_key":"sb_publishable_x","team_name":"devs","team_ref":"` + setupTeamRef + `","url":"` + r.oldURL + `","version":1}` + "\n"
	r.writeFile(r.original)
	const local = "http://127.0.0.1:8743"
	localKey := teamstore.Key("rest", local, setupTeamRef)
	r.f.rec.on("team move", okAnswer(r.notMoved()), okAnswer(`{"team_ref":"`+setupTeamRef+`","moved":true,"adapter":"rest","url":"`+local+`","recorded_at":"2026-11-14T12:00:00Z"}`))
	if err := Team(r.inv(true, "--to", local+"/", "--secret-file", r.secretFile)); err != nil {
		t.Fatal(err)
	}
	want := `{"adapter":"rest","team_name":"devs","team_ref":"` + setupTeamRef + `","url":"` + local + `","version":1}` + "\n"
	if got := r.fileNow(); got != want {
		t.Fatalf("the team file:\n%s\nwant:\n%s", got, want)
	}
	take := r.f.rec.specsOf("team take-in")[0]
	if i := slices.Index(take.Argv, "--profile"); take.Argv[i+1] != localKey || !strings.Contains(string(take.Stdin), `"backend":{"url":"`+local+`"}`) ||
		strings.Contains(argvOf(take), "--create-token-file") {
		t.Fatalf("the take-in: %s", argvOf(take))
	}
	if open := r.f.rec.specsOf("team open-move")[0]; strings.Contains(argvOf(open), "--ticket-days") {
		t.Fatalf("the open names days nobody asked for: %s", argvOf(open))
	}
	if got := string(r.f.rec.specsOf("team move")[1].Stdin); got != `{"adapter":"rest","url":"`+local+`"}` {
		t.Fatalf("the destination recorded: %s", got)
	}
	if strings.Contains(r.prompts[0], "certificate") || strings.Contains(r.f.out.String(), "certificate") {
		t.Fatalf("a server with no pin is shown with one")
	}
}
