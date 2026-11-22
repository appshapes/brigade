//go:build !windows

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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
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
		if r.URL.Query().Get("status") == "success" {
			out = successfulRunsOf(out)
		}
		if size, err := strconv.Atoi(r.URL.Query().Get("per_page")); err == nil {
			out = newestRunsOf(out, size)
		}
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

// successfulRunsOf is a run history as the API gives it for `status=success`: without the runs whose
// conclusion is another. A run that names no conclusion is kept, as the fixtures that predate the field are.
func successfulRunsOf(history string) string {
	return runsOf(history, func(runs []map[string]any) []map[string]any {
		kept := []map[string]any{}
		for _, run := range runs {
			if conclusion, named := run["conclusion"]; !named || conclusion == "success" {
				kept = append(kept, run)
			}
		}
		return kept
	})
}

// newestRunsOf is one page of a run history, as the API gives it for `per_page`: the first runs listed, which
// are the newest, and no more of them than the page holds.
func newestRunsOf(history string, size int) string {
	return runsOf(history, func(runs []map[string]any) []map[string]any {
		if size < 1 || len(runs) <= size {
			return runs
		}
		return runs[:size]
	})
}

// runsOf is a run history with only the runs that keep answers with. A history it cannot read, and one that
// loses no run, are given as they are.
func runsOf(history string, keep func([]map[string]any) []map[string]any) string {
	var listed struct {
		Runs []map[string]any `json:"workflow_runs"`
	}
	if err := json.Unmarshal([]byte(history), &listed); err != nil {
		return history
	}
	kept := keep(listed.Runs)
	if len(kept) == len(listed.Runs) {
		return history
	}
	out, err := json.Marshal(map[string]any{"workflow_runs": kept})
	if err != nil {
		return history
	}
	return string(out)
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
				"subject": "Brigade 0.3.0",
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
	run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0", "subject": "Brigade 0.3.0"})
}

// The subject names the highest version, not the one published last nor the one that sorts last as text:
// 0.9.1, a fix published after 0.10.0, is both of those.
func TestSendReleaseNotesSubjectIsTheHighestVersion(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	ns.releases = "[" + strings.Join([]string{
		notesEmailRelease("v0.9.0", "2026-01-01T10:00:00Z", "Notes of 0.9.0.", false, false),
		notesEmailRelease("v0.10.0", "2026-01-02T10:00:00Z", "Notes of 0.10.0.", false, false),
		notesEmailRelease("v0.9.1", "2026-01-03T10:00:00Z", "Notes of 0.9.1.", false, false),
	}, ",") + "]"
	run := runNotesEmail(t, ns, "window", "BRIGADE_EMAIL_FROM=v0.10.0")
	wantPass(t, run.result, `the subject is "Brigade 0.10.0"`)
	run.wantOutputs(t, map[string]string{"count": "2", "first": "v0.10.0", "last": "v0.9.1", "subject": "Brigade 0.10.0"})
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

// A run history is not a record that lasts: since 2026-10-01 GitHub removes a run when its repository's
// retention period ends, seven days for this one until 2026-11-22 and 90 since (work item 401). A run that
// sent an email can be gone,
// and the newest one still listed is then an older email: believed, it would mail the list every release
// since that older one again. historyOf is the workflow's history as the API gives it, each run with its
// number, how many hours ago it was made and what its send step concluded. A run whose send step failed
// is a failed run, which the API leaves out when it is asked for the successful ones.
func historyOf(ns *notesEmailServer, now time.Time, runs ...[3]string) {
	var listed []string
	for _, run := range runs {
		hours, _ := time.ParseDuration(run[1] + "h")
		conclusion := "success"
		if run[2] == "failure" {
			conclusion = "failure"
		}
		record := `{"id":` + run[0] + `0,"run_number":` + run[0] + `,"conclusion":"` + conclusion + `","created_at":"` + stamp(now.Add(-hours)) + `"}`
		listed = append(listed, record)
		ns.run[run[0]+"0"] = record
		ns.jobs[run[0]+"0"] = `{"jobs":[{"name":"email","steps":[{"name":"Send to the recipients","conclusion":"` + run[2] + `"}]}]}`
	}
	ns.runs = `{"workflow_runs":[` + strings.Join(listed, ",") + `]}`
}

// A release of three days ago, which no email has, and one of ten days ago, which the email of a week ago
// had. The newest email the history can still have is run 10's, of twenty days ago.
func aWeekAfterAnEmail(now time.Time) string {
	return "[" +
		notesEmailRelease("v0.3.0", stamp(now.Add(-72*time.Hour)), "Three days ago.", false, false) + "," +
		notesEmailRelease("v0.2.0", stamp(now.Add(-240*time.Hour)), "Ten days ago.", false, false) + "]"
}

// A run is not accounted for between the newest email the history still has, run 10's of twenty days ago,
// and this run, 14: the run that sent the last email, or one beside it. The mark is not believed, and the
// window is the last seven days, as when no email was found: the release of ten days ago, which the email
// of a week ago had, is not mailed again. One such run is enough, wherever it is, and it is not accounted
// for when its number is missing, when it is listed without its steps, and when it goes out of the history
// between being listed and being asked for its jobs, which ends no run.
func TestSendReleaseNotesWindowDoesNotBelieveAMarkWithARunNotAccountedForAfterIt(t *testing.T) {
	t.Parallel()
	this, mark := [3]string{"14", "0", "success"}, [3]string{"10", "480", "success"}
	preview := func(number string) [3]string { return [3]string{number, "100", "skipped"} }
	for name, c := range map[string]struct {
		history [][3]string
		lost    func(ns *notesEmailServer)
		doubt   string
	}{
		"the three runs after the mark are gone": {[][3]string{this, mark}, nil, "run 11 of this workflow is not in the run history"},
		"the first run after the mark is gone":   {[][3]string{this, preview("13"), preview("12"), mark}, nil, "run 11 of this workflow is not in the run history"},
		"the run before this one is gone":        {[][3]string{this, preview("12"), preview("11"), mark}, nil, "run 13 of this workflow is not in the run history"},
		"this run is run again, and a later run is gone": {
			[][3]string{{"16", "1", "skipped"}, this, preview("13"), preview("12"), preview("11"), mark}, nil,
			"run 15 of this workflow is not in the run history",
		},
		"a run after the mark has lost its steps": {
			[][3]string{this, preview("13"), preview("12"), preview("11"), mark},
			func(ns *notesEmailServer) { ns.jobs["130"] = `{"jobs":[]}` },
			"run 13 of this workflow is in the run history without its steps",
		},
		// The shape GitHub really leaves: this workflow's runs of September 2026 are listed with their job
		// and no step in it.
		"a run after the mark has a job and no step in it": {
			[][3]string{this, preview("13"), preview("12"), preview("11"), mark},
			func(ns *notesEmailServer) { ns.jobs["130"] = `{"jobs":[{"name":"email","steps":[]}]}` },
			"run 13 of this workflow is in the run history without its steps",
		},
		"a run after the mark goes while it is read": {
			[][3]string{this, preview("13"), preview("12"), preview("11"), mark},
			func(ns *notesEmailServer) { delete(ns.jobs, "130") },
			"run 13 of this workflow went out of the run history while it was being read",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ns := newNotesEmailServer(t)
			now := time.Now()
			ns.releases = aWeekAfterAnEmail(now)
			historyOf(ns, now, c.history...)
			if c.lost != nil {
				c.lost(ns)
			}

			run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=140", "GITHUB_RUN_NUMBER=14")
			wantPass(t, run.result, "::warning::send-release-notes: "+c.doubt, "the window is the last seven days")
			run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
			if strings.Contains(run.all(), "the last email was sent by the run created at") {
				t.Errorf("a mark was believed:\n%s", run.all())
			}
		})
	}
}

// What the newest email that is left had sent is still sent, whatever came after it. So when that email is
// inside the last seven days, the window starts at it and not before it: the release of four days ago,
// which run 13's email of three days ago had, is not mailed again because run 14 is not accounted for. That
// holds for each way a run is not accounted for: under a doubt the reading goes on down the history for
// the newest email that is left, and does not stop at the run that raised the doubt.
func TestSendReleaseNotesWindowStartsAtAMarkThatIsNewerThanSevenDays(t *testing.T) {
	t.Parallel()
	this, preview, mark := [3]string{"15", "0", "success"}, [3]string{"14", "24", "skipped"}, [3]string{"13", "72", "success"}
	for name, c := range map[string]struct {
		history [][3]string
		lost    func(ns *notesEmailServer)
		doubt   string
	}{
		"the run after it is gone": {[][3]string{this, mark}, nil, "run 14 of this workflow is not in the run history"},
		"the run after it goes while it is read": {
			[][3]string{this, preview, mark},
			func(ns *notesEmailServer) { delete(ns.jobs, "140") },
			"run 14 of this workflow went out of the run history while it was being read",
		},
		"the run after it has lost its steps": {
			[][3]string{this, preview, mark},
			func(ns *notesEmailServer) { ns.jobs["140"] = `{"jobs":[{"name":"email","steps":[]}]}` },
			"run 14 of this workflow is in the run history without its steps",
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ns := newNotesEmailServer(t)
			now := time.Now()
			ns.releases = "[" +
				notesEmailRelease("v0.3.0", stamp(now.Add(-24*time.Hour)), "A day ago.", false, false) + "," +
				notesEmailRelease("v0.2.0", stamp(now.Add(-100*time.Hour)), "Four days ago.", false, false) + "]"
			historyOf(ns, now, c.history...)
			if c.lost != nil {
				c.lost(ns)
			}

			run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=150", "GITHUB_RUN_NUMBER=15")
			wantPass(t, run.result, "::warning::send-release-notes: "+c.doubt,
				"of "+stamp(now.Add(-72*time.Hour)), "the window starts at that email")
			run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
		})
	}
}

// The check reads the hundred newest runs of the workflow. When they do not reach back to the mark, the
// runs between are not known to be there, and the mark is not believed; when the hundredth is the run after
// the mark, every run is accounted for and it is.
func TestSendReleaseNotesWindowReadsAHundredRunsAfterTheMark(t *testing.T) {
	t.Parallel()
	// failed is the runs from number `newest` down to `oldest`, each a failure, an hour apart.
	failed := func(newest, oldest int) [][3]string {
		var runs [][3]string
		for number := newest; number >= oldest; number-- {
			runs = append(runs, [3]string{strconv.Itoa(number), strconv.Itoa(newest - number + 1), "failure"})
		}
		return runs
	}
	mark := [3]string{"10", "480", "success"}

	t.Run("the hundred do not reach the mark", func(t *testing.T) {
		t.Parallel()
		ns := newNotesEmailServer(t)
		now := time.Now()
		ns.releases = aWeekAfterAnEmail(now)
		history := append([][3]string{{"120", "0", "success"}}, failed(119, 21)...) // a hundred runs: 120 to 21
		historyOf(ns, now, append(history, mark)...)

		run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=1200", "GITHUB_RUN_NUMBER=120")
		wantPass(t, run.result,
			"::warning::send-release-notes: the run history is longer than this check reads, a hundred runs, and does not reach back to run 11",
			"the window is the last seven days")
		run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
	})

	t.Run("the hundredth is the run after the mark", func(t *testing.T) {
		t.Parallel()
		ns := newNotesEmailServer(t)
		now := time.Now()
		ns.releases = aWeekAfterAnEmail(now)
		history := append([][3]string{{"110", "0", "success"}}, failed(109, 11)...) // a hundred runs: 110 to 11
		historyOf(ns, now, append(history, mark)...)

		run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=1100", "GITHUB_RUN_NUMBER=110")
		wantPass(t, run.result, "the last email was sent by the run created at "+stamp(now.Add(-480*time.Hour)))
		run.wantOutputs(t, map[string]string{"count": "2", "first": "v0.2.0", "last": "v0.3.0"})
		if strings.Contains(run.all(), "::warning::") {
			t.Errorf("a history of a hundred runs that reaches the mark warns:\n%s", run.all())
		}
	})
}

// No run that is left sent an email. That is the first run of a new repository, and it is also a history
// that lost the run of the last email: the numbers tell them apart. With a run of the workflow missing
// below this one, the run says so and does not say that no earlier email was found; the window is the last
// seven days in both.
func TestSendReleaseNotesWindowWithNoEmailLeftSaysWhenARunIsGone(t *testing.T) {
	t.Parallel()

	t.Run("a run is gone", func(t *testing.T) {
		t.Parallel()
		ns := newNotesEmailServer(t)
		now := time.Now()
		ns.releases = aWeekAfterAnEmail(now)
		historyOf(ns, now, [3]string{"14", "0", "success"}, [3]string{"13", "100", "skipped"}, [3]string{"12", "200", "skipped"})

		run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=140", "GITHUB_RUN_NUMBER=14")
		wantPass(t, run.result,
			"::warning::send-release-notes: run 1 of this workflow is not in the run history, and no run that is left sent an email",
			"the window is the last seven days")
		run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
		if strings.Contains(run.all(), "no earlier email was found") {
			t.Errorf("the run says no earlier email was found, with a run gone that may have sent one:\n%s", run.all())
		}
	})

	// The doubt of a run without its steps says it too: the workflow's runs of September 2026 are what is
	// left once the last sending run is gone.
	t.Run("a run has lost its steps", func(t *testing.T) {
		t.Parallel()
		ns := newNotesEmailServer(t)
		now := time.Now()
		ns.releases = aWeekAfterAnEmail(now)
		historyOf(ns, now, [3]string{"14", "0", "success"}, [3]string{"3", "1000", "skipped"})
		ns.jobs["30"] = `{"jobs":[{"name":"email","steps":[]}]}`

		run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=140", "GITHUB_RUN_NUMBER=14")
		wantPass(t, run.result,
			"::warning::send-release-notes: run 3 of this workflow is in the run history without its steps, and no run that is left sent an email",
			"the window is the last seven days")
		run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
	})

	t.Run("every run is there", func(t *testing.T) {
		t.Parallel()
		ns := newNotesEmailServer(t)
		now := time.Now()
		ns.releases = aWeekAfterAnEmail(now)
		historyOf(ns, now, [3]string{"3", "0", "success"}, [3]string{"2", "100", "skipped"}, [3]string{"1", "200", "failure"})

		run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=30", "GITHUB_RUN_NUMBER=3")
		wantPass(t, run.result, "no earlier email was found: the window is the last seven days")
		run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
		if strings.Contains(run.all(), "::warning::") {
			t.Errorf("a history with every run in it and no email warns:\n%s", run.all())
		}
	})
}

// The seven days are counted back from this run's own start, which is the window's upper bound and what
// the next run reads as its mark, and not from the clock: a run that waited two hours in the workflow's
// queue still covers the release published 169 hours ago, and not the one of 171 hours ago.
func TestSendReleaseNotesWindowCountsItsSevenDaysFromTheRunsStart(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	now := time.Now()
	ns.releases = "[" +
		notesEmailRelease("v0.3.0", stamp(now.Add(-169*time.Hour)), "A week and an hour ago.", false, false) + "," +
		notesEmailRelease("v0.2.0", stamp(now.Add(-171*time.Hour)), "A week and three hours ago.", false, false) + "]"
	historyOf(ns, now, [3]string{"1", "2", "success"})

	run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=10", "GITHUB_RUN_NUMBER=1")
	wantPass(t, run.result, "no earlier email was found: the window is the last seven days, from "+stamp(now.Add(-170*time.Hour)))
	run.wantOutputs(t, map[string]string{"count": "1", "first": "v0.3.0", "last": "v0.3.0"})
}

// With nothing published in the window the run says where its window starts, and not that nothing was
// published since the last email, which it has just said it does not know.
func TestSendReleaseNotesWindowWithARunNotAccountedForAndNothingToSend(t *testing.T) {
	t.Parallel()
	ns := newNotesEmailServer(t)
	now := time.Now()
	ns.releases = threeReleases() // all of January 2026
	historyOf(ns, now, [3]string{"14", "0", "success"}, [3]string{"10", "480", "success"})

	run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=140", "GITHUB_RUN_NUMBER=14")
	wantPass(t, run.result, "::notice::send-release-notes: no release was published since ", "where this run's window starts")
	run.wantOutputs(t, map[string]string{"count": "0"})
	if strings.Contains(run.all(), "since the last email") {
		t.Errorf("the run says what was published since an email it does not believe:\n%s", run.all())
	}
}

// The ordinary week: every run between the last email and this one is in the history, whatever it did. The
// mark is taken as it always was, and the email covers everything published since it. A run that failed is
// in the history too, though the API leaves it out when it is asked for the successful runs: the check
// reads every run. And a run that has its steps, none of them of the send step's name, sent nothing the
// script knows of: it is not a run that lost its steps, and it puts no later run in doubt.
func TestSendReleaseNotesWindowBelievesAMarkWithEveryRunAfterItThere(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		history [][3]string
		change  func(ns *notesEmailServer)
	}{
		"the run before this one sent":      {[][3]string{{"14", "0", "success"}, {"13", "480", "success"}}, nil},
		"previews and a failure in between": {[][3]string{{"14", "0", "success"}, {"13", "24", "skipped"}, {"12", "48", "failure"}, {"11", "96", "skipped"}, {"10", "480", "success"}}, nil},
		"a run whose steps name no send step": {
			[][3]string{{"14", "0", "success"}, {"13", "24", "skipped"}, {"12", "48", "skipped"}, {"11", "96", "skipped"}, {"10", "480", "success"}},
			func(ns *notesEmailServer) {
				ns.jobs["130"] = `{"jobs":[{"name":"email","steps":[{"name":"Draft the email","conclusion":"success"}]}]}`
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			ns := newNotesEmailServer(t)
			now := time.Now()
			ns.releases = aWeekAfterAnEmail(now)
			historyOf(ns, now, c.history...)
			if c.change != nil {
				c.change(ns)
			}

			run := runNotesEmail(t, ns, "window", "GITHUB_RUN_ID=140", "GITHUB_RUN_NUMBER=14")
			wantPass(t, run.result, "the last email was sent by the run created at "+stamp(now.Add(-480*time.Hour)))
			run.wantOutputs(t, map[string]string{"count": "2", "first": "v0.2.0", "last": "v0.3.0"})
			if strings.Contains(run.all(), "::warning::") {
				t.Errorf("the ordinary week warns:\n%s", run.all())
			}
		})
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
