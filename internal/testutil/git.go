package testutil

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// GitQuietConfig is the configuration a fixture gives its git so that git starts nothing in the background
// (work item 424). A commit, a fetch and the receiving end of a push each end by starting `git maintenance
// run --auto --detach`, which outlives the command. git judges how many loose objects a repository has by
// those whose name begins with 17, and with git 2.55 two of them were enough for a repack (measured
// 2026-11-24). A commit is named after the second it is made in, so now and then a fixture got that repack:
// when it was still writing `.git/info/refs` while the test's temporary directory was removed, the removal
// failed and the test with it (seen once, in scripts/ci's TestThePinIsARelease).
//
// It is configuration in a file and never GIT_CONFIG_COUNT variables or `-c` options: git takes those away
// from the `git receive-pack` a push to a local repository starts. `gc.auto` and `receive.autogc` are the
// same switch for a git from before `git maintenance`.
const GitQuietConfig = "[maintenance]\n\tauto = false\n[gc]\n\tauto = 0\n[receive]\n\tautogc = false\n"

// QuietGitHome makes home a HOME whose git reads GitQuietConfig, as its `.gitconfig`, and returns home: for a
// fixture that runs git with a HOME of its own and no GIT_CONFIG_GLOBAL. A `.gitconfig` the home already has
// is left as it is, so it may be called before every git a fixture runs. A fixture whose home lies inside the
// repository it builds names a file elsewhere in GIT_CONFIG_GLOBAL instead, as scripts/ci does.
func QuietGitHome(tb testing.TB, home string) string {
	tb.Helper()
	file := filepath.Join(home, ".gitconfig")
	if _, err := os.Stat(file); err == nil {
		return home
	}
	if err := os.MkdirAll(home, 0o700); err != nil {
		tb.Fatalf("the fixture's home: %v", err)
	}
	if err := os.WriteFile(file, []byte(GitQuietConfig), 0o600); err != nil {
		tb.Fatalf("the fixture's git configuration: %v", err)
	}
	return home
}

// GitStarted reads a trace git wrote (GIT_TRACE2_EVENT, which a child inherits): ran is every command line
// git ran or started, one to a line, and maintenance the ones that are git's own maintenance, `git
// maintenance` or `git gc` however the program is named (`git-gc`, a path, `.exe`).
func GitStarted(trace []byte) (ran string, maintenance [][]string) {
	var lines []string
	for _, line := range strings.Split(string(trace), "\n") {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if json.Unmarshal([]byte(line), &event) != nil || (event.Event != "start" && event.Event != "child_start") {
			continue
		}
		lines = append(lines, strings.Join(event.Argv, " "))
		if slices.ContainsFunc(event.Argv, isMaintenance) {
			maintenance = append(maintenance, event.Argv)
		}
	}
	return strings.Join(lines, "\n"), maintenance
}

// isMaintenance reports whether a word of a git command line names git's maintenance.
func isMaintenance(word string) bool {
	if at := strings.LastIndexAny(word, `/\\`); at >= 0 {
		word = word[at+1:]
	}
	switch strings.TrimPrefix(strings.TrimSuffix(word, ".exe"), "git-") {
	case "maintenance", "gc":
		return true
	}
	return false
}
