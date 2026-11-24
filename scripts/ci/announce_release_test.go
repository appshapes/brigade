//go:build !windows

// The release's announcement (work item 403): scripts/ci/announce-release.sh, the "Announce, once" step of
// .github/workflows/release-notes.yml.
//
// The script writes to a public repository's issues and cannot be tried there: what it opens is read by every
// member, and what it closes is closed. So its two decisions, whether to open the announcement and which
// earlier ones to close, are measured against a `gh` that answers the listing from a file and records every
// write, and held from both sides: what must be closed is, and what must never be is not.
package ci_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/testutil"
)

const announceRepo = "example-owner/example-repo"

// announceTitle is the announcement's title for a version.
func announceTitle(version string) string { return version + " is out — run /brigade:update" }

// announceIssue is one issue of the listing as the issues endpoint gives it.
func announceIssue(number, state, title string, pullRequest bool) string {
	pr := ""
	if pullRequest {
		pr = `,"pull_request":{"url":"x"}`
	}
	return `{"number":` + number + `,"state":"` + state + `","title":"` + title + `"` + pr + `}`
}

// announceFixture is a `gh` and a `sleep` on a PATH of their own before the real one (jq is the real one), the
// listing `gh api` answers with and the file the writes are recorded in.
type announceFixture struct {
	dir string
	env []string
}

func newAnnounceFixture(t *testing.T, tag string, issues ...string) *announceFixture {
	t.Helper()
	dir := t.TempDir()
	// Two pages, as `gh api --paginate` prints them: one array a page.
	half := len(issues) / 2
	listing := "[" + strings.Join(issues[:half], ",") + "]\n[" + strings.Join(issues[half:], ",") + "]\n"
	if err := os.WriteFile(filepath.Join(dir, "listing.json"), []byte(listing), 0o600); err != nil {
		t.Fatal(err)
	}
	// The listing is asked for with the bot as its creator, open and closed, and nothing else is listed. Every
	// other call is a write: recorded with its arguments, one a line.
	writeExec(t, filepath.Join(dir, "bin", "gh"), `#!/bin/sh
if [ "$1" = api ]; then
  case "$*" in
    *"repos/`+announceRepo+`/issues?state=all&creator=github-actions%5Bbot%5D&per_page=100"*) ;;
    *) echo "gh: unexpected listing: $*" >&2; exit 1 ;;
  esac
  if [ -n "${FAKE_GH_LIST_FAILS:-}" ]; then echo "gh: HTTP 502" >&2; exit 1; fi
  if [ -f "$FAKE_GH_DIR/created" ] && [ -f "$FAKE_GH_DIR/listing-after.json" ]; then
    cat "$FAKE_GH_DIR/listing-after.json"
  else
    cat "$FAKE_GH_DIR/listing.json"
  fi
  exit 0
fi
printf '%s\n' "$*" >> "$FAKE_GH_DIR/writes.log"
if [ "$1 $2" = "issue close" ] && [ "$3" = "${FAKE_GH_CLOSE_FAILS:-}" ]; then echo "gh: HTTP 403" >&2; exit 1; fi
if [ "$1 $2" = "issue create" ]; then : > "$FAKE_GH_DIR/created"; echo "https://github.com/`+announceRepo+`/issues/999"; fi
`)
	writeExec(t, filepath.Join(dir, "bin", "sleep"), "#!/bin/sh\nexit 0\n")
	return &announceFixture{dir: dir, env: []string{
		"PATH=" + filepath.Join(dir, "bin") + string(os.PathListSeparator) + os.Getenv("PATH"),
		"FAKE_GH_DIR=" + dir, "RELEASE_TAG=" + tag, "GITHUB_REPOSITORY=" + announceRepo,
	}}
}

// writes is every write the script made, one a line, in order.
func (f *announceFixture) writes(t *testing.T) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(f.dir, "writes.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

// closed is the numbers the script closed, in order, and created whether it opened an issue.
func (f *announceFixture) closed(t *testing.T) (numbers []string, created bool) {
	t.Helper()
	for _, w := range f.writes(t) {
		switch {
		case strings.HasPrefix(w, "issue close "):
			numbers = append(numbers, strings.Fields(w)[2])
		case strings.HasPrefix(w, "issue create "):
			created = true
		}
	}
	return numbers, created
}

// announceBoard is a repository's issues as the bot's listing gives them: three open announcements, one that
// was closed by hand long ago, and everything that must never be closed.
func announceBoard() []string {
	return []string{
		announceIssue("117", "open", announceTitle("0.57.0"), false),
		announceIssue("116", "open", announceTitle("0.56.0"), false),
		announceIssue("60", "open", announceTitle("0.9.0"), false),
		announceIssue("11", "closed", announceTitle("0.6.1"), false),
		// A work order the bot opened, and one that names a version in passing.
		announceIssue("120", "open", "Refresh the structural documentation", false),
		announceIssue("90", "open", "0.40.0 is out — run /brigade:update, and it broke my hooks", false),
		announceIssue("91", "open", "Why 0.41.0 is out — run /brigade:update", false),
		// A pull request under the announcement's title: the endpoint lists pull requests too.
		announceIssue("95", "open", announceTitle("0.42.0"), true),
	}
}

func TestAnnounceRelease(t *testing.T) {
	t.Parallel()

	t.Run("a release opens its announcement and closes the earlier ones", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "v0.58.0", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r, `closed: #117 "`+announceTitle("0.57.0")+`"`, `closed: #116`, `closed: #60`)
		numbers, created := f.closed(t)
		if !created || strings.Join(numbers, " ") != "117 116 60" {
			t.Fatalf("created %v, closed %v; want the announcement opened and 117, 116 and 60 closed\n%s", created, numbers, r.all())
		}
		// The announcement as it always read, with no label. The body
		// has line ends, so the call is read whole, up to the first close.
		all := strings.Join(f.writes(t), "\n")
		create, _, _ := strings.Cut(all, "\nissue close ")
		for _, want := range []string{"--title " + announceTitle("0.58.0"), "Brigade 0.58.0 is published.", "/brigade:update", "https://github.com/" + announceRepo + "/releases/tag/v0.58.0"} {
			if !strings.Contains(create, want) {
				t.Errorf("the announcement lacks %q:\n%s", want, create)
			}
		}
		if !strings.HasPrefix(create, "issue create ") || strings.Contains(create, "--label") {
			t.Errorf("the first write is not the announcement with no label:\n%s", create)
		}
	})

	t.Run("a dispatch for the same tag again opens nothing and closes nothing more", func(t *testing.T) {
		t.Parallel()
		issues := append([]string{announceIssue("130", "open", announceTitle("0.58.0"), false)},
			announceIssue("117", "closed", announceTitle("0.57.0"), false), announceIssue("120", "open", "Refresh the structural documentation", false))
		f := newAnnounceFixture(t, "v0.58.0", issues...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r, "announcement exists (#130", "no earlier announcement is open")
		if w := f.writes(t); len(w) != 0 {
			t.Fatalf("writes on a second run: %v", w)
		}
	})

	t.Run("an announcement that exists is not opened again, and the earlier ones are still closed", func(t *testing.T) {
		t.Parallel()
		// The run before this one opened the announcement and died before it closed anything.
		f := newAnnounceFixture(t, "v0.57.0", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r, "announcement exists (#117")
		if numbers, created := f.closed(t); created || strings.Join(numbers, " ") != "116 60" {
			t.Fatalf("created %v, closed %v; want nothing opened and 116 and 60 closed", created, numbers)
		}
	})

	t.Run("a dispatch for an older tag never closes the newest announcement", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "v0.6.1", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r, "announcement exists (#11")
		if numbers, created := f.closed(t); created || strings.Join(numbers, " ") != "116 60" {
			t.Fatalf("created %v, closed %v; want 116 and 60 closed and 117, the newest, left open", created, numbers)
		}
	})

	t.Run("an older tag with no announcement opens none beside the newest", func(t *testing.T) {
		t.Parallel()
		// A dispatch by hand for a tag that was never announced: it would tell the members to update to a
		// release behind the one they were told of.
		f := newAnnounceFixture(t, "v0.4.0", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r, "0.4.0 is below 0.57.0, which is announced; no announcement is opened for it")
		if numbers, created := f.closed(t); created || strings.Join(numbers, " ") != "116 60" {
			t.Fatalf("created %v, closed %v; want nothing opened, 116 and 60 closed and 117 left open", created, numbers)
		}
	})

	t.Run("what is closed is decided on the issues as they are after the opening", func(t *testing.T) {
		t.Parallel()
		// A run for another tag opened its announcement between this run's listing and its opening.
		f := newAnnounceFixture(t, "v0.58.1", announceIssue("117", "open", announceTitle("0.57.0"), false), announceIssue("120", "open", "Refresh the structural documentation", false))
		after := "[" + strings.Join([]string{
			announceIssue("131", "open", announceTitle("0.58.1"), false),
			announceIssue("130", "open", announceTitle("0.58.0"), false),
			announceIssue("117", "open", announceTitle("0.57.0"), false),
			announceIssue("120", "open", "Refresh the structural documentation", false),
		}, ",") + "]\n"
		if err := os.WriteFile(filepath.Join(f.dir, "listing-after.json"), []byte(after), 0o600); err != nil {
			t.Fatal(err)
		}
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r)
		if numbers, created := f.closed(t); !created || strings.Join(numbers, " ") != "130 117" {
			t.Fatalf("created %v, closed %v; want the announcement opened and 130 and 117 closed, never 131, its own", created, numbers)
		}
	})

	t.Run("a release closes the announcement of its own release candidate", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "v0.58.0",
			announceIssue("131", "open", announceTitle("0.59.0-rc1"), false),
			announceIssue("130", "open", announceTitle("0.58.0-rc1"), false),
			announceIssue("117", "open", announceTitle("0.57.0"), false))
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r)
		if numbers, created := f.closed(t); !created || strings.Join(numbers, " ") != "130 117" {
			t.Fatalf("created %v, closed %v; want 130 (0.58.0-rc1) and 117 closed, and 131 (0.59.0-rc1), which is above, left open", created, numbers)
		}
	})

	t.Run("versions are compared as numbers", func(t *testing.T) {
		t.Parallel()
		// 0.9.0 is below 0.10.0, and as text it is above.
		f := newAnnounceFixture(t, "v0.10.0", announceIssue("60", "open", announceTitle("0.9.0"), false), announceIssue("61", "open", announceTitle("0.10.0"), false))
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r)
		if numbers, _ := f.closed(t); strings.Join(numbers, " ") != "60" {
			t.Fatalf("closed %v, want 60 (0.9.0) and never 61 (0.10.0)", numbers)
		}
	})

	t.Run("plan writes nothing and says what would be", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "v0.58.0", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env, "plan")
		wantPass(t, r, `would open: "`+announceTitle("0.58.0")+`"`, `would close: #117`, `would close: #116`, `would close: #60`)
		if w := f.writes(t); len(w) != 0 {
			t.Fatalf("plan wrote: %v", w)
		}
		for _, never := range []string{"#120", "#90", "#91", "#95", "#11"} {
			if strings.Contains(r.stdout, "would close: "+never+" ") {
				t.Errorf("the plan closes %s:\n%s", never, r.stdout)
			}
		}
	})

	t.Run("a tag that is no plain version announces itself and closes nothing", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "v0.58.0-rc1", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantPass(t, r, "0.58.0-rc1 is not a plain version; no earlier announcement is closed")
		if numbers, created := f.closed(t); !created || len(numbers) != 0 {
			t.Fatalf("created %v, closed %v; want the announcement opened and nothing closed", created, numbers)
		}
	})

	t.Run("a listing that fails opens nothing", func(t *testing.T) {
		t.Parallel()
		// An empty listing would read as "no announcement yet", and a second one would be opened.
		f := newAnnounceFixture(t, "v0.57.0", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, append(f.env, "FAKE_GH_LIST_FAILS=1"))
		wantFail(t, r)
		if w := f.writes(t); len(w) != 0 {
			t.Fatalf("writes after a listing that failed: %v", w)
		}
	})

	t.Run("a close that fails fails the step", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "v0.58.0", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, append(f.env, "FAKE_GH_CLOSE_FAILS=116"))
		wantFail(t, r)
		if numbers, _ := f.closed(t); strings.Join(numbers, " ") != "117 116" {
			t.Fatalf("closes attempted: %v, want 117 and then 116, where it stops", numbers)
		}
	})

	t.Run("without its tag it does nothing", func(t *testing.T) {
		t.Parallel()
		f := newAnnounceFixture(t, "", announceBoard()...)
		r := runScript(t, "announce-release.sh", f.dir, f.env)
		wantFail(t, r, "RELEASE_TAG")
		if w := f.writes(t); len(w) != 0 {
			t.Fatalf("writes with no tag: %v", w)
		}
	})
}

// TestTheNotesWorkflowAnnouncesThroughTheScript: the step of release-notes.yml runs the script, with the job's
// token and the right to write issues, and opens no issue of its own beside it.
func TestTheNotesWorkflowAnnouncesThroughTheScript(t *testing.T) {
	t.Parallel()
	data, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), ".github", "workflows", "release-notes.yml"))
	if err != nil {
		t.Fatal(err)
	}
	workflow := string(data)
	at := strings.Index(workflow, "      - name: Announce, once\n")
	if at < 0 {
		t.Fatal("release-notes.yml has no step named \"Announce, once\"")
	}
	step := workflow[at:]
	for _, want := range []string{"        run: scripts/ci/announce-release.sh\n", `        env: {GH_TOKEN: "${{ github.token }}"}`} {
		if !strings.Contains(step, want) {
			t.Errorf("the step lacks %q:\n%s", want, step)
		}
	}
	if !strings.Contains(workflow, "      issues: write") {
		t.Error("the job may not write issues: the announcement is opened and the earlier ones closed with the job's token")
	}
	// Nothing else in the workflow opens an issue: the one other mention is the sentence that tells the
	// writer agent not to.
	for i, line := range strings.Split(workflow, "\n") {
		if strings.Contains(line, "gh issue create") && !strings.Contains(line, "Do not run") {
			t.Errorf("release-notes.yml line %d opens an issue outside the script: %s", i+1, strings.TrimSpace(line))
		}
	}
}
