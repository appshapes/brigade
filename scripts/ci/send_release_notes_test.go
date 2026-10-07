// The release-notes email (card 43): scripts/ci/send-release-notes.sh, the three deterministic steps of
// .github/workflows/send-release-notes.yml.
//
// What the script decides cannot be taken back once the mail step has run: which releases the email covers,
// who receives it, and what it says about why. So each decision is measured against a fake GitHub API (an
// httptest.Server that records every request) rather than read off the source, and two properties are held
// on every run: no address reaches the output except on its own `::add-mask::` line, and the job token
// reaches curl through a 0600 header file, never on argv — the curl shim is keepalive_test.go's.
//
// Every address below is made up. No fixture here may be built from a real recipient, a real address or a
// credential of the session that writes it.
package ci_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/testutil"
)

const (
	notesEmailScript      = "send-release-notes.sh"
	notesEmailScriptRel   = "scripts/ci/send-release-notes.sh"
	notesEmailWorkflowRel = ".github/workflows/send-release-notes.yml"
	notesEmailRepo        = "example-owner/example-repo"
	// Not shaped like any real token: scripts/ci/no-secrets.sh scans this file too.
	notesEmailFakeToken = "tok_fake_send_release_notes" //nolint:gosec // G101: a made-up value the fake API expects back
)

// notesEmailChangelog is the changelog the window cases read: three versions, each with one line that names it.
const notesEmailChangelog = "# Changelog\n\n## [Unreleased]\n\n" +
	"## [0.3.0] — 2026-01-03\n\n### Added\n\n- The third thing.\n\n" +
	"## [0.2.0] — 2026-01-02\n\n### Added\n\n- The second thing.\n\n" +
	"## [0.1.0] — 2026-01-01\n\n### Added\n\n- The first thing.\n"

// ---------------------------------------------------------------------------------------------------------
// the fake API

// notesEmailServer plays the five GitHub endpoints the script reads. Every answer is a field, so a case
// changes one and nothing else. A path it does not know is a 404 with GitHub's own shape.
type notesEmailServer struct {
	url string

	repo     string            // GET /repos/<repo>
	releases string            // GET /repos/<repo>/releases
	runs     string            // GET /repos/<repo>/actions/workflows/send-release-notes.yml/runs
	run      map[string]string // GET /repos/<repo>/actions/runs/<id>
	jobs     map[string]string // GET /repos/<repo>/actions/runs/<id>/jobs

	mu       sync.Mutex
	requests []keepaliveRequest
}

func newNotesEmailServer(t *testing.T) *notesEmailServer {
	t.Helper()
	ns := &notesEmailServer{
		repo:     `{"default_branch":"trunk"}`,
		releases: `[]`,
		runs:     `{"total_count":0,"workflow_runs":[]}`,
		run:      map[string]string{},
		jobs:     map[string]string{},
	}
	srv := httptest.NewServer(http.HandlerFunc(ns.serve))
	t.Cleanup(srv.Close)
	ns.url = srv.URL
	return ns
}

func (ns *notesEmailServer) serve(w http.ResponseWriter, r *http.Request) {
	_, _ = io.Copy(io.Discard, r.Body)
	ns.mu.Lock()
	ns.requests = append(ns.requests, keepaliveRequest{method: r.Method, path: r.URL.RequestURI(), headers: r.Header.Clone()})
	status, out := http.StatusNotFound, `{"message":"Not Found"}`
	repo := "/repos/" + notesEmailRepo
	path := r.URL.Path
	firstPage := r.URL.Query().Get("page") == "" || r.URL.Query().Get("page") == "1"
	switch {
	case path == repo:
		status, out = http.StatusOK, ns.repo
	case path == repo+"/releases" && firstPage:
		status, out = http.StatusOK, ns.releases
	case path == repo+"/releases":
		status, out = http.StatusOK, `[]`
	case path == repo+"/actions/workflows/send-release-notes.yml/runs":
		status, out = http.StatusOK, ns.runs
	case strings.HasPrefix(path, repo+"/actions/runs/") && strings.HasSuffix(path, "/jobs"):
		id := strings.TrimSuffix(strings.TrimPrefix(path, repo+"/actions/runs/"), "/jobs")
		if body, ok := ns.jobs[id]; ok {
			status, out = http.StatusOK, body
		}
	case strings.HasPrefix(path, repo+"/actions/runs/"):
		if body, ok := ns.run[strings.TrimPrefix(path, repo+"/actions/runs/")]; ok {
			status, out = http.StatusOK, body
		}
	}
	ns.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, out)
}

func (ns *notesEmailServer) seen() []keepaliveRequest {
	ns.mu.Lock()
	defer ns.mu.Unlock()
	return append([]keepaliveRequest(nil), ns.requests...)
}

// asked reports whether any request's path starts with prefix.
func (ns *notesEmailServer) asked(prefix string) bool {
	for _, req := range ns.seen() {
		if strings.HasPrefix(req.path, prefix) {
			return true
		}
	}
	return false
}

// stamp is a time in GitHub's own form, the one the script compares as a string.
func stamp(at time.Time) string { return at.UTC().Format("2006-01-02T15:04:05Z") }

func notesEmailRelease(tag, published, body string, draft, prerelease bool) string {
	flag := func(b bool) string {
		if b {
			return "true"
		}
		return "false"
	}
	at := `"` + published + `"`
	if published == "" {
		at = "null"
	}
	return `{"tag_name":"` + tag + `","published_at":` + at + `,"draft":` + flag(draft) +
		`,"prerelease":` + flag(prerelease) + `,"body":"` + body + `"}`
}

// threeReleases is the repository every window case starts from: three final releases a day apart, and
// three things that are never announced — a draft, a pre-release and a tag that is not a version.
func threeReleases() string {
	return "[" + strings.Join([]string{
		notesEmailRelease("v0.3.0", "2026-01-03T10:00:00Z", "Notes of the third.", false, false),
		notesEmailRelease("v0.4.0-rc1", "2026-01-03T12:00:00Z", "A rehearsal.", false, true),
		notesEmailRelease("v0.9.0", "", "A draft.", true, false),
		notesEmailRelease("nightly", "2026-01-03T13:00:00Z", "Not a version.", false, false),
		notesEmailRelease("v0.2.0", "2026-01-02T10:00:00Z", "Notes of the second.", false, false),
		notesEmailRelease("v0.1.0", "2026-01-01T10:00:00Z", "Notes of the first.", false, false),
	}, ",") + "]"
}

// ---------------------------------------------------------------------------------------------------------
// running the script

// notesEmailRun is one run: the script's output, the curl shim's record, the working directory the script
// wrote into and the step outputs it set.
type notesEmailRun struct {
	keepaliveRun
	dir     string
	outputs map[string]string
}

func runNotesEmail(t *testing.T, ns *notesEmailServer, mode string, vars ...string) notesEmailRun {
	t.Helper()
	root := t.TempDir()
	return runNotesEmailIn(t, ns, root, mode, vars...)
}

// runNotesEmailIn runs one mode in a root the case prepared (finish reads a draft the case wrote first).
func runNotesEmailIn(t *testing.T, ns *notesEmailServer, root, mode string, vars ...string) notesEmailRun {
	t.Helper()
	path, record := keepaliveShim(t, root)
	restrictedPath(t, root, "awk", "cat", "cmp", "cp", "date", "grep", "head", "mkdir", "paste", "sed", "tr", "wc")
	home := filepath.Join(root, ".home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	changelog := filepath.Join(root, "CHANGELOG.md")
	if err := os.WriteFile(changelog, []byte(notesEmailChangelog), 0o600); err != nil {
		t.Fatalf("writing the changelog: %v", err)
	}
	dir := filepath.Join(root, "email")
	outputs := filepath.Join(root, "outputs.txt")
	env := baseEnv(t, home, append([]string{
		"PATH=" + path,
		"BRIGADE_EMAIL_DIR=" + dir,
		"BRIGADE_EMAIL_CHANGELOG=" + changelog,
		"GITHUB_REPOSITORY=" + notesEmailRepo,
		"GITHUB_API_URL=" + ns.url,
		"GITHUB_SERVER_URL=https://github.example",
		"GITHUB_OUTPUT=" + outputs,
		"GH_TOKEN=" + notesEmailFakeToken,
	}, vars...)...)
	res := runScript(t, notesEmailScript, root, env, mode)
	shim, err := os.ReadFile(record)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the curl shim's record: %v", err)
	}
	run := notesEmailRun{keepaliveRun: keepaliveRun{result: res, shim: string(shim)}, dir: dir, outputs: map[string]string{}}
	data, err := os.ReadFile(outputs)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("reading the step outputs: %v", err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if name, value, ok := strings.Cut(line, "="); ok {
			run.outputs[name] = value
		}
	}
	if strings.Contains(res.all(), notesEmailFakeToken) {
		t.Errorf("the output carries the job token:\n%s", res.all())
	}
	return run
}

func (run notesEmailRun) read(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(run.dir, rel))
	if err != nil {
		t.Fatalf("reading %s: %v", rel, err)
	}
	return string(data)
}

func (run notesEmailRun) wantOutputs(t *testing.T, want map[string]string) {
	t.Helper()
	for name, value := range want {
		if got := run.outputs[name]; got != value {
			t.Errorf("output %s is %q, want %q\n%s", name, got, value, run.all())
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// window

func TestSendReleaseNotesWindowStartsWithTheTagAskedFor(t *testing.T) {
	t.Parallel()
	for _, from := range []string{"v0.2.0", "0.2.0"} {
		t.Run(from, func(t *testing.T) {
			t.Parallel()
			ns := newNotesEmailServer(t)
			ns.releases = threeReleases()
			run := runNotesEmail(t, ns, "window", "BRIGADE_EMAIL_FROM="+from)
			wantPass(t, run.result, "the window starts with v0.2.0, as asked")
			run.wantOutputs(t, map[string]string{
				"count": "2", "first": "v0.2.0", "last": "v0.3.0", "versions": "0.2.0 0.3.0",
				"subject": "Brigade 0.2.0 to 0.3.0: what's new",
			})
			if got := run.read(t, "window.txt"); got != "v0.2.0\nv0.3.0\n" {
				t.Errorf("window.txt is %q", got)
			}
			if got := run.read(t, "sources/v0.2.0.md"); !strings.Contains(got, "Notes of the second.") {
				t.Errorf("sources/v0.2.0.md is %q", got)
			}
			changelog := run.read(t, "sources/changelog.md")
			for _, want := range []string{"## [0.2.0]", "The second thing.", "## [0.3.0]", "The third thing."} {
				if !strings.Contains(changelog, want) {
					t.Errorf("sources/changelog.md lacks %q:\n%s", want, changelog)
				}
			}
			for _, unwanted := range []string{"The first thing.", "Unreleased"} {
				if strings.Contains(changelog, unwanted) {
					t.Errorf("sources/changelog.md carries %q, which is outside the window:\n%s", unwanted, changelog)
				}
			}
			// The draft, the pre-release and the tag that is not a version are never sources.
			entries, err := os.ReadDir(filepath.Join(run.dir, "sources"))
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			if got := strings.Join(names, " "); got != "changelog.md v0.2.0.md v0.3.0.md" {
				t.Errorf("sources/ holds %q", got)
			}
			if ns.asked("/repos/" + notesEmailRepo + "/actions/workflows/") {
				t.Errorf("the run history was read although a tag was asked for")
			}
		})
	}
}

func TestSendReleaseNotesWindowOfOneRelease(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	ns.releases = threeReleases()
	run := runNotesEmail(t, ns, "window", "BRIGADE_EMAIL_FROM=v0.3.0")
	wantPass(t, run.result)
	run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0", "subject": "Brigade 0.3.0: what's new"})
}

func TestSendReleaseNotesWindowRefusesATagThatWasNotReleased(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	ns.releases = threeReleases()
	for _, from := range []string{"v0.7.0", "v0.4.0-rc1", "v0.9.0"} {
		run := runNotesEmail(t, ns, "window", "BRIGADE_EMAIL_FROM="+from)
		wantFail(t, run.result, from+" is not a published release")
	}
}

// The last email is the newest successful run whose SEND STEP succeeded. A newer successful run that only
// previewed (the step skipped) does not move the mark, and neither does this run itself.
func TestSendReleaseNotesWindowFollowsTheLastEmail(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	ns.releases = threeReleases()
	ns.runs = `{"workflow_runs":[` +
		`{"id":30,"created_at":"2026-01-03T09:00:00Z"},` + // this run
		`{"id":20,"created_at":"2026-01-02T15:00:00Z"},` + // a preview: nothing was sent to the recipients
		`{"id":10,"created_at":"2026-01-01T15:00:00Z"}]}` // the last email
	step := func(conclusion string) string {
		return `{"jobs":[{"name":"email","steps":[{"name":"Draft the email","conclusion":"success"},` +
			`{"name":"Send to the recipients","conclusion":"` + conclusion + `"}]}]}`
	}
	ns.jobs["30"] = step("success")
	ns.jobs["20"] = step("skipped")
	ns.jobs["10"] = step("success")
	ns.run["30"] = `{"id":30,"created_at":"2026-01-03T09:00:00Z"}`

	run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=30")
	wantPass(t, run.result, "the last email was sent by the run created at 2026-01-01T15:00:00Z")
	// v0.1.0 came before the last email; v0.3.0 was published an hour after this run started.
	run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.2.0", "last": "v0.2.0"})
	if ns.asked("/repos/" + notesEmailRepo + "/actions/runs/30/jobs") {
		t.Errorf("the run read its own jobs to find the last email")
	}
	if !strings.Contains(run.shim, "status=success") {
		t.Errorf("the run history was not asked for successful runs only:\n%s", run.shim)
	}
}

func TestSendReleaseNotesWindowIsSevenDaysWithoutAnEarlierEmail(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	now := time.Now()
	ns.releases = "[" +
		notesEmailRelease("v0.3.0", stamp(now.Add(-72*time.Hour)), "Three days ago.", false, false) + "," +
		notesEmailRelease("v0.2.0", stamp(now.Add(-240*time.Hour)), "Ten days ago.", false, false) + "]"
	run := runNotesEmail(t, ns, "window")
	wantPass(t, run.result, "no earlier email was found: the window is the last seven days")
	run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0"})
}

func TestSendReleaseNotesWindowWithNothingToSend(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	ns.releases = threeReleases() // all of January 2026: outside any seven days this test will ever run in
	run := runNotesEmail(t, ns, "window")
	wantPass(t, run.result, "::notice::send-release-notes: no release was published since the last email")
	run.wantOutputs(t, map[string]string{"count": "0"})
	if _, ok := run.outputs["subject"]; ok {
		t.Errorf("a subject was set for an email that will not be sent: %q", run.outputs["subject"])
	}
	if got := run.read(t, "window.txt"); got != "" {
		t.Errorf("window.txt is %q", got)
	}
}

func TestSendReleaseNotesFailsWhenTheAPIRefuses(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	run := runNotesEmail(t, ns, "window", "GITHUB_REPOSITORY=example-owner/another-repo")
	wantFail(t, run.result, "::error::send-release-notes: GET /repos/example-owner/another-repo/releases answered HTTP 404: Not Found")
}

// The job token: on every request as a bearer header, from a 0600 file, and on no curl argv.
func TestSendReleaseNotesKeepsTheTokenOffArgv(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	ns.releases = threeReleases()
	run := runNotesEmail(t, ns, "window", "BRIGADE_EMAIL_FROM=v0.1.0")
	wantPass(t, run.result)
	seen := ns.seen()
	if len(seen) == 0 {
		t.Fatal("the API saw no request")
	}
	for _, req := range seen {
		if got := req.headers.Get("Authorization"); got != "Bearer "+notesEmailFakeToken {
			t.Errorf("%s carried Authorization %q", req.path, got)
		}
	}
	if strings.Contains(run.shim, notesEmailFakeToken) {
		t.Errorf("the token is on curl's argv:\n%s", run.shim)
	}
	modes := keepaliveHeaderFileModes(run.shim)
	if len(modes) == 0 {
		t.Fatalf("no header file was passed to curl:\n%s", run.shim)
	}
	for _, mode := range modes {
		if !strings.HasPrefix(mode, "-rw-------") {
			t.Errorf("a header file has mode %s, want -rw-------", mode)
		}
	}
}

func TestSendReleaseNotesRefusesAnAPIThatIsNotHTTPS(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	run := runNotesEmail(t, ns, "window", "GITHUB_API_URL=http://api.example.test")
	wantFail(t, run.result, "GITHUB_API_URL must use https")
	if len(ns.seen()) != 0 || run.shim != "" {
		t.Errorf("a request went out before the refusal:\n%s", run.shim)
	}
}

// ---------------------------------------------------------------------------------------------------------
// adopt

// adoptFixture is what `adopt` reads: this run's window, and the artifact of the earlier run as the workflow
// downloaded it. Each case changes one thing of a fixture every part of which would be adopted.
type adoptFixture struct {
	window       string // this run's window.txt
	draftWindow  string // the earlier run's window.txt; "-" leaves the file out
	notes        string // the earlier run's notes.md; "-" leaves the file out
	review       string // the earlier run's review.md; "-" leaves the file out
	run          string // GET /actions/runs/77
	draftRun     string // BRIGADE_EMAIL_DRAFT_RUN
	thisRun      string // GITHUB_RUN_ID
	wantRequests bool
}

func goodAdoptFixture() adoptFixture {
	return adoptFixture{
		window:       "v0.2.0\nv0.3.0\n",
		draftWindow:  "v0.2.0\nv0.3.0\n",
		notes:        "# Brigade 0.2.0 to 0.3.0\n\nThe second thing and the third.\n",
		review:       "APPROVED\n",
		run:          `{"id":77,"path":".github/workflows/send-release-notes.yml","head_branch":"trunk","conclusion":"success"}`,
		draftRun:     "77",
		thisRun:      "99",
		wantRequests: true,
	}
}

func runAdopt(t *testing.T, fx adoptFixture) (notesEmailRun, *notesEmailServer) {
	t.Helper()
	ns := newNotesEmailServer(t)
	ns.run["77"] = fx.run
	root := t.TempDir()
	draft := filepath.Join(root, "draft")
	for _, dir := range []string{filepath.Join(root, "email"), draft} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for path, text := range map[string]string{
		filepath.Join(root, "email", "window.txt"): fx.window,
		filepath.Join(draft, "window.txt"):         fx.draftWindow,
		filepath.Join(draft, "notes.md"):           fx.notes,
		filepath.Join(draft, "review.md"):          fx.review,
	} {
		if text == "-" {
			continue
		}
		if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	run := runNotesEmailIn(t, ns, root, "adopt",
		"BRIGADE_EMAIL_DRAFT_RUN="+fx.draftRun, "BRIGADE_EMAIL_DRAFT_DIR="+draft, "GITHUB_RUN_ID="+fx.thisRun)
	return run, ns
}

func TestSendReleaseNotesAdoptTakesAnApprovedDraft(t *testing.T) {
	t.Parallel()
	fx := goodAdoptFixture()
	run, ns := runAdopt(t, fx)
	wantPass(t, run.result, "the draft of run 77 is adopted: approved there, 2 release(s), the same as this email covers")
	if got := run.read(t, "notes.md"); got != fx.notes {
		t.Errorf("notes.md is %q", got)
	}
	if got := run.read(t, "review.md"); got != fx.review {
		t.Errorf("review.md is %q", got)
	}
	if !ns.asked("/repos/"+notesEmailRepo+"/actions/runs/77") || ns.asked("/repos/"+notesEmailRepo+"/actions/runs/99") {
		t.Errorf("adopt did not ask about run 77 and run 77 alone")
	}
}

func TestSendReleaseNotesAdoptRefusals(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		change func(*adoptFixture)
		want   string
	}{
		{"an id that is not digits", func(fx *adoptFixture) { fx.draftRun = "77/../78" }, "BRIGADE_EMAIL_DRAFT_RUN is not the id of a run"},
		{"no id", func(fx *adoptFixture) { fx.draftRun = "" }, "BRIGADE_EMAIL_DRAFT_RUN is not the id of a run"},
		{"its own run", func(fx *adoptFixture) { fx.thisRun = "77" }, "a run cannot adopt its own draft"},
		{"a run that does not exist", func(fx *adoptFixture) { fx.draftRun = "78" }, "GET /repos/" + notesEmailRepo + "/actions/runs/78 answered HTTP 404"},
		{"a run of another workflow", func(fx *adoptFixture) {
			fx.run = strings.Replace(fx.run, "send-release-notes.yml", "release-notes.yml", 1)
		}, "run 77 is not a run of send-release-notes.yml"},
		{"a run the gate failed", func(fx *adoptFixture) {
			fx.run = strings.Replace(fx.run, `"success"`, `"failure"`, 1)
		}, "run 77 did not succeed (failure): its gate approved no draft"},
		{"a run on another branch", func(fx *adoptFixture) {
			fx.run = strings.Replace(fx.run, `"trunk"`, `"someone/try"`, 1)
		}, "run 77 ran on someone/try, not on the default branch"},
		{"a review that asks for changes", func(fx *adoptFixture) { fx.review = "CHANGES_REQUESTED\nA finding.\n" }, "the review of run 77 does not say APPROVED"},
		{"a review that says it lower down", func(fx *adoptFixture) { fx.review = "CHANGES_REQUESTED\nAPPROVED\n" }, "the review of run 77 does not say APPROVED"},
		{"no review", func(fx *adoptFixture) { fx.review = "-" }, "run 77 left no review"},
		{"no draft", func(fx *adoptFixture) { fx.notes = "-" }, "run 77 left no draft"},
		{"an empty draft", func(fx *adoptFixture) { fx.notes = "" }, "run 77 left no draft"},
		{"a release published since", func(fx *adoptFixture) { fx.window = "v0.2.0\nv0.3.0\nv0.4.0\n" }, "the draft of run 77 covers 2 release(s) and this email covers 3"},
		{"another window of the same length", func(fx *adoptFixture) { fx.draftWindow = "v0.1.0\nv0.2.0\n" }, "or not the same ones: write a new draft"},
		{"no window in the artifact", func(fx *adoptFixture) { fx.draftWindow = "-" }, "run 77 left no window"},
		{"no window of this run", func(fx *adoptFixture) { fx.window = "-" }, "run window first"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fx := goodAdoptFixture()
			tc.change(&fx)
			run, _ := runAdopt(t, fx)
			wantFail(t, run.result, tc.want)
			for _, name := range []string{"notes.md", "review.md"} {
				if _, err := os.Stat(filepath.Join(run.dir, name)); err == nil {
					t.Errorf("%s was written although the draft was refused", name)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------------------------------------
// recipients

var notesEmailAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+`)

// notesEmailOnlyMasks fails on any line of the output that carries an address and is not an `::add-mask::`
// line: this repository's run logs are public, and the runner consumes the mask lines.
func notesEmailOnlyMasks(t *testing.T, res result) {
	t.Helper()
	for _, line := range strings.Split(res.all(), "\n") {
		if notesEmailAddress.MatchString(line) && !strings.HasPrefix(line, "::add-mask::") {
			t.Errorf("an address is printed outside a mask line: %q", line)
		}
	}
}

func TestSendReleaseNotesRecipients(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	list := "# the list, kept by hand\n" +
		"Ann@Example.com, ben@example.org\n" + // two on a line, one in capitals
		"\n" +
		"cy@example.net   # asked on the first of the month\n" +
		"ann@example.com\n" + // ann again
		"Fay <fay@example.com>\n" + // a display name: two entries, neither of them one plain address
		"gus@example.com;x@example.org\n" + // two addresses glued together
		"dee@users.noreply.github.com\n" // accepts no mail

	run := runNotesEmail(t, ns, "recipients", "BRIGADE_EMAIL_RECIPIENTS="+list)
	wantPass(t, run.result,
		"the list has 8 entries; 3 will receive the email",
		"1 repeat an address above and are counted once",
		"4 were refused",
		"::warning::send-release-notes: entry 5 of the list is not one plain address; not used",
	)
	notesEmailOnlyMasks(t, run.result)

	got := strings.Fields(run.read(t, "recipients.txt"))
	sort.Strings(got)
	want := []string{"ann@example.com", "ben@example.org", "cy@example.net"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("recipients.txt holds %v, want %v", got, want)
	}
	info, err := os.Stat(filepath.Join(run.dir, "recipients.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("recipients.txt has mode %o, want 600", info.Mode().Perm())
	}
	bcc := strings.Split(run.outputs["bcc"], ",")
	sort.Strings(bcc)
	if strings.Join(bcc, " ") != strings.Join(want, " ") {
		t.Errorf("the bcc output is %q, want %v", run.outputs["bcc"], want)
	}
	run.wantOutputs(t, map[string]string{"count": "3"})
	// Every address is a mask, as it was written and as it is sent, and so is an entry that was refused.
	for _, address := range append(want, "Ann@Example.com", "<fay@example.com>", "gus@example.com;x@example.org") {
		if !strings.Contains(run.stdout, "::add-mask::"+address+"\n") {
			t.Errorf("%s was never registered as a mask", address)
		}
	}
	// A word that is no address is not a mask: it would blank that word out of every log line.
	if strings.Contains(run.stdout, "::add-mask::Fay\n") {
		t.Errorf("a word that is not an address was registered as a mask")
	}
	if len(ns.seen()) != 0 || run.shim != "" {
		t.Errorf("reading the list made a request:\n%s", run.shim)
	}
}

func TestSendReleaseNotesRecipientsWhenTheListIsEmpty(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	for _, list := range []string{"", "# nobody yet\n", "not-an-address\n"} {
		run := runNotesEmail(t, ns, "recipients", "BRIGADE_EMAIL_RECIPIENTS="+list)
		wantPass(t, run.result,
			"0 will receive the email",
			"::notice::send-release-notes: the list of recipients is empty",
		)
		notesEmailOnlyMasks(t, run.result)
		run.wantOutputs(t, map[string]string{"count": "0", "bcc": ""})
	}
}

// ---------------------------------------------------------------------------------------------------------
// finish

// finishFixture lays out a root for `finish`: the draft and the window the agents' steps left behind.
func finishFixture(t *testing.T, draft string) (root string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "email"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "email", "notes.md"), []byte(draft), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "email", "window.txt"), []byte("v0.2.0\nv0.3.0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

const finishDraft = "# Brigade 0.2.0 to 0.3.0\n\nThe second thing and the third.\n\n## What's new\n\n### The third thing (0.3.0)\n\nIt does the third thing.\n"

func TestSendReleaseNotesFinishPutsTheFooterUnderTheDraft(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	root := finishFixture(t, finishDraft)
	renderer := testutil.Build(t, "./cmd/brigade-release-email")
	run := runNotesEmailIn(t, ns, root, "finish", "BRIGADE_EMAIL_RENDERER="+renderer, "GITHUB_SHA=0123456789abcdef0123456789abcdef01234567")
	wantPass(t, run.result, "the HTML is")
	draft := finishDraft
	email := run.read(t, "email.md")
	if !strings.HasPrefix(email, draft) {
		t.Errorf("email.md does not start with the draft:\n%s", email)
	}
	footer := strings.TrimPrefix(email, draft)
	for _, want := range []string{
		"\n---\n",
		"Every release and its notes: https://github.example/" + notesEmailRepo + "/releases",
		"You are receiving this because you are on the list for the release notes of [" + notesEmailRepo + "](https://github.example/" + notesEmailRepo + ").",
		"To stop these emails, reply to this one and say so.",
	} {
		if !strings.Contains(footer, want) {
			t.Errorf("the footer lacks %q:\n%s", want, footer)
		}
	}
	if len(ns.seen()) != 0 {
		t.Errorf("finish reached the API")
	}

	// The HTML part (card 73): the same words laid out, the images pinned to the commit, the chip linking to
	// the release on the server the window read from, the footer's words under the card.
	html := run.read(t, "email.html")
	for _, want := range []string{
		"<!doctype html>",
		"<title>Brigade 0.2.0 to 0.3.0</title>",
		">Two releases</p>",
		`src="https://github.example/` + notesEmailRepo + `/raw/0123456789abcdef0123456789abcdef01234567/plugin/.claude-plugin/icon.png"`,
		`<a href="https://github.example/` + notesEmailRepo + `/releases/tag/v0.3.0"`,
		"The third thing",
		"Every release and its notes: <a href=\"https://github.example/" + notesEmailRepo + "/releases\"",
		"To stop these emails, reply to this one and say so.",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("email.html lacks %q:\n%s", want, html)
		}
	}
	if strings.Contains(html, "<style") {
		t.Errorf("email.html carries a <style> block")
	}
	if info, err := os.Stat(filepath.Join(run.dir, "email.html")); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("email.html: %v %v", info, err)
	}
}

func TestSendReleaseNotesFinishRefusesWithoutADraft(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	run := runNotesEmail(t, ns, "finish")
	wantFail(t, run.result, "there is no draft at")
	if _, err := os.Stat(filepath.Join(run.dir, "email.md")); err == nil {
		t.Errorf("email.md was written without a draft")
	}
}

// Without the renderer there is no HTML part, and the step fails rather than sending the Markdown as the
// HTML: the mail step reads email.html by name.
func TestSendReleaseNotesFinishRefusesWithoutTheRenderer(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	run := runNotesEmailIn(t, ns, finishFixture(t, finishDraft), "finish")
	wantFail(t, run.result, "BRIGADE_EMAIL_RENDERER is not set")
	run = runNotesEmailIn(t, ns, finishFixture(t, finishDraft), "finish", "BRIGADE_EMAIL_RENDERER="+filepath.Join(t.TempDir(), "absent"))
	wantFail(t, run.result, "does not exist or is not executable")
}

// A draft the renderer refuses -- the lint ran it on the draft, so this is the footer's or the renderer's
// own doing -- fails the step with the renderer's findings, and no email.html is left for the mail step.
func TestSendReleaseNotesFinishRefusesADraftThatDoesNotRender(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	root := finishFixture(t, finishDraft+"\n| a | b |\n|---|---|\n")
	renderer := testutil.Build(t, "./cmd/brigade-release-email")
	run := runNotesEmailIn(t, ns, root, "finish", "BRIGADE_EMAIL_RENDERER="+renderer)
	wantFail(t, run.result, "the email did not render as HTML", "a table is not supported")
	if _, err := os.Stat(filepath.Join(run.dir, "email.html")); err == nil {
		t.Errorf("email.html was written for a draft that does not render")
	}
}

func TestSendReleaseNotesUsage(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	for _, mode := range []string{"", "send"} {
		run := runNotesEmail(t, ns, mode)
		if run.code != 2 || !strings.Contains(run.stderr, "usage: scripts/ci/send-release-notes.sh window|adopt|recipients|finish") {
			t.Errorf("mode %q: exit %d\n%s", mode, run.code, run.all())
		}
	}
}

// ---------------------------------------------------------------------------------------------------------
// the joins to the workflow

// The script finds the last email by the NAME of the workflow's send step and by the workflow's file name.
// Both are retyped in the script, so both are joined here: a rename on one side only would make every run
// believe no email was ever sent, and send the last seven days again.
func TestSendReleaseNotesJoinsTheWorkflow(t *testing.T) {
	t.Parallel()
	root := testutil.RepoRoot(t)
	script, ok := readText(t, root, notesEmailScriptRel)
	if !ok {
		return
	}
	workflow, ok := readText(t, root, notesEmailWorkflowRel)
	if !ok {
		return
	}
	step := regexp.MustCompile(`(?m)^send_step='([^']+)'$`).FindStringSubmatch(script)
	file := regexp.MustCompile(`(?m)^workflow_file='([^']+)'$`).FindStringSubmatch(script)
	if step == nil || file == nil {
		t.Fatalf("the script does not define send_step and workflow_file as single-quoted literals")
	}
	if file[1] != filepath.Base(notesEmailWorkflowRel) {
		t.Errorf("the script reads the runs of %s; the workflow is %s", file[1], filepath.Base(notesEmailWorkflowRel))
	}
	if n := strings.Count(workflow, "\n      - name: "+step[1]+"\n"); n != 1 {
		t.Errorf("the workflow has %d steps named %q, want exactly 1", n, step[1])
	}

	// The addresses are secrets, never variables: a variable is printed unmasked in a public log.
	for _, name := range []string{"RELEASE_NOTES_RECIPIENTS", "RELEASE_NOTES_PREVIEW_ADDRESS", "SMTP_USERNAME", "SMTP_PASSWORD"} {
		if !strings.Contains(workflow, "secrets."+name) {
			t.Errorf("the workflow never reads secrets.%s", name)
		}
		if strings.Contains(workflow, "vars."+name) {
			t.Errorf("the workflow reads vars.%s: it must be a secret", name)
		}
	}
	// The recipients never leave the job: no artifact carries them.
	if strings.Contains(workflow, "recipients.txt") {
		t.Errorf("the workflow names recipients.txt; the list must not be uploaded or printed")
	}
	// Every mail step hides its recipients from each other.
	if strings.Count(workflow, "dawidd6/action-send-mail@") != 2 {
		t.Errorf("the workflow has %d mail steps, want 2 (the recipients and the preview)", strings.Count(workflow, "dawidd6/action-send-mail@"))
	}
	if !strings.Contains(workflow, "bcc: ${{ steps.recipients.outputs.bcc }}") {
		t.Errorf("the recipients are not addressed as bcc")
	}
	// The draft of an earlier run is found by the name its artifact was uploaded under.
	if !strings.Contains(workflow, "name: release-notes-email-${{ github.run_id }}") ||
		!strings.Contains(workflow, `--name "release-notes-email-$BRIGADE_EMAIL_DRAFT_RUN"`) {
		t.Errorf("the artifact is not uploaded and downloaded under the same name")
	}
	// No agent writes or reviews when a draft is adopted: four agent steps, each behind the same condition.
	if n := strings.Count(workflow, "inputs.draft_run == ''"); n < 4 {
		t.Errorf("%d steps stand aside for an adopted draft, want the four agent steps at least", n)
	}
	// The footer tells the reader to answer the email in order to stop it, and the answer goes to the sender:
	// no mail step sets a Reply-To (owner, 2026-09-29).
	if strings.Contains(workflow, "reply_to:") {
		t.Errorf("a mail step sets a Reply-To; an answer must reach the sender's address")
	}
	// The HTML part is the renderer's (card 73): both mail steps send email.html as it is and email.md as the
	// text part, the renderer is built beside the CLI the lint reads, and the HTML joins the artifact.
	for want, n := range map[string]int{
		"html_body: file:///tmp/brigade-email/email.html": 2,
		"body: file:///tmp/brigade-email/email.md":        2,
		"convert_markdown: false":                         2,
		"convert_markdown: true":                          0,
		`go build -o "$BRIGADE_EMAIL_DIR/brigade-release-email" ./cmd/brigade-release-email`:      1,
		`echo "BRIGADE_EMAIL_RENDERER=$BRIGADE_EMAIL_DIR/brigade-release-email" >> "$GITHUB_ENV"`: 1,
		"            /tmp/brigade-email/email.html\n":                                             1,
	} {
		if got := strings.Count(workflow, want); got != n {
			t.Errorf("the workflow has %d of %q, want %d", got, want, n)
		}
	}
}
