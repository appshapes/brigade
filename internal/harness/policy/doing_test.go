package policy

import (
	"bytes"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit/log"
)

// evilMarker is planted in every settings fixture: the scan answers one
// of three words, and no byte of a settings file may travel with it.
const evilMarker = "EVILMARKER-settings"

// rules builds one settings document with the given permissions arrays.
func rules(allow, ask, deny []string) string {
	q := func(list []string) string {
		if list == nil {
			return "[]"
		}
		var b strings.Builder
		b.WriteString("[")
		for i, s := range list {
			if i > 0 {
				b.WriteString(",")
			}
			b.WriteString(`"` + s + `"`)
		}
		b.WriteString("]")
		return b.String()
	}
	return `{"note":"` + evilMarker + `","permissions":{"allow":` + q(allow) + `,"ask":` + q(ask) + `,"deny":` + q(deny) + `}}`
}

func TestDoingRuleFilesOrderAndDedupe(t *testing.T) {
	t.Parallel()
	got := DoingRuleFiles(cfgDir, []string{cwd, cwd, "", "/other/dir"})
	want := []string{
		userFile(),
		projectFile(), localFile(),
		filepath.Join("/other/dir", ".claude", "settings.json"), filepath.Join("/other/dir", ".claude", "settings.local.json"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("DoingRuleFiles = %q, want %q", got, want)
	}
	if got := DoingRuleFiles("", []string{cwd}); len(got) != 2 {
		t.Fatalf("no config dir: %q", got)
	}
}

// TestDoingRuleFilesAddTheRepositoryToplevel: a session started in a
// subdirectory of a checkout reads the toplevel's project files too —
// Claude Code reads its project settings from the project root — and the
// toplevel is found the way team-file discovery finds it (a `.git`
// entry), on a real directory tree.
func TestDoingRuleFilesAddTheRepositoryToplevel(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "internal", "harness")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0o700); err != nil {
		t.Fatal(err)
	}
	got := DoingRuleFiles(cfgDir, []string{sub})
	want := []string{
		userFile(),
		filepath.Join(sub, ".claude", "settings.json"), filepath.Join(sub, ".claude", "settings.local.json"),
		filepath.Join(repo, ".claude", "settings.json"), filepath.Join(repo, ".claude", "settings.local.json"),
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("DoingRuleFiles = %q, want %q", got, want)
	}
	// Outside any repository the directory stands alone.
	if got := DoingRuleFiles(cfgDir, []string{root}); len(got) != 3 {
		t.Fatalf("no toplevel: %q", got)
	}
}

// TestScanDoingRulesTable is the rule table of plan 5.2 and row P16-3:
// which ask and deny entries close the feature, which allow spellings
// open it, and which entries are none of Brigade's business.
func TestScanDoingRulesTable(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		m    map[string]string
		want Verdict
	}{
		{"no files", map[string]string{}, VerdictNone},
		{"empty object", map[string]string{userFile(): `{}`}, VerdictNone},
		{"permissions with nothing relevant", map[string]string{userFile(): rules([]string{"Read(/x/**)"}, []string{"Bash(rm:*)"}, []string{"Bash(git push:*)"})}, VerdictNone},
		{"permissions null", map[string]string{userFile(): `{"permissions": null}`}, VerdictNone},
		// Ruling 4: the documented send gate does not cover the new verb.
		{"ask Bash(brigade send*)", map[string]string{userFile(): rules(nil, []string{"Bash(brigade send*)"}, nil)}, VerdictNone},
		{"deny Bash(brigade send*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(brigade send*)"})}, VerdictNone},
		{"deny Bash(brigade send:*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(brigade send:*)"})}, VerdictNone},
		// Closing entries.
		{"deny Bash(brigade:*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(brigade:*)"})}, VerdictBlocked},
		{"ask Bash(brigade:*)", map[string]string{userFile(): rules(nil, []string{"Bash(brigade:*)"}, nil)}, VerdictBlocked},
		{"deny Bash(*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(*)"})}, VerdictBlocked},
		{"deny Bash(b*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(b*)"})}, VerdictBlocked},
		{"deny bare Bash", map[string]string{userFile(): rules(nil, nil, []string{"Bash"})}, VerdictBlocked},
		{"deny Bash()", map[string]string{userFile(): rules(nil, nil, []string{"Bash()"})}, VerdictBlocked},
		{"deny Bash(brigade doing --clear)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(brigade doing --clear)"})}, VerdictBlocked},
		{"ask Bash(brigade doing*)", map[string]string{userFile(): rules(nil, []string{"Bash(brigade doing*)"}, nil)}, VerdictBlocked},
		{"deny Bash(brigade doing:*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(brigade doing:*)"})}, VerdictBlocked},
		{"deny Bash(* doing*)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(* doing*)"})}, VerdictBlocked},
		{"deny with a wildcard tool", map[string]string{userFile(): rules(nil, nil, []string{"*"})}, VerdictBlocked},
		{"deny B*", map[string]string{userFile(): rules(nil, nil, []string{"B*(brigade:*)"})}, VerdictBlocked},
		// Irrelevant entries.
		{"deny Read(/x/brigade/**)", map[string]string{userFile(): rules(nil, nil, []string{"Read(/x/brigade/**)"})}, VerdictNone},
		{"deny WebFetch", map[string]string{userFile(): rules(nil, nil, []string{"WebFetch"})}, VerdictNone},
		{"deny Bash(brigade doingx)", map[string]string{userFile(): rules(nil, nil, []string{"Bash(brigade doingx)"})}, VerdictNone},
		// Opening entries, exactly.
		{"allow bare Bash", map[string]string{userFile(): rules([]string{"Bash"}, nil, nil)}, VerdictAllowed},
		{"allow Bash(brigade:*)", map[string]string{userFile(): rules([]string{"Bash(brigade:*)"}, nil, nil)}, VerdictAllowed},
		{"allow Bash(brigade *)", map[string]string{userFile(): rules([]string{"Bash(brigade *)"}, nil, nil)}, VerdictAllowed},
		{"allow Bash(brigade*)", map[string]string{userFile(): rules([]string{"Bash(brigade*)"}, nil, nil)}, VerdictAllowed},
		{"allow Bash(brigade doing:*)", map[string]string{userFile(): rules([]string{"Bash(brigade doing:*)"}, nil, nil)}, VerdictAllowed},
		{"allow Bash(brigade doing *)", map[string]string{userFile(): rules([]string{"Bash(brigade doing *)"}, nil, nil)}, VerdictAllowed},
		{"allow Bash(brigade doing*)", map[string]string{userFile(): rules([]string{"Bash(brigade doing*)"}, nil, nil)}, VerdictAllowed},
		// An allow that is not one of the spellings is not consent.
		{"allow Bash(brig*) is not a spelling", map[string]string{userFile(): rules([]string{"Bash(brig*)"}, nil, nil)}, VerdictNone},
		{"allow Bash(*) is not a spelling", map[string]string{userFile(): rules([]string{"Bash(*)"}, nil, nil)}, VerdictNone},
		{"allow Bash(brigade send*) is not a spelling", map[string]string{userFile(): rules([]string{"Bash(brigade send*)"}, nil, nil)}, VerdictNone},
		// The union: a closing entry anywhere beats an allow anywhere.
		{"allow in the user file, deny in the project file", map[string]string{userFile(): rules([]string{"Bash(brigade:*)"}, nil, nil), projectFile(): rules(nil, nil, []string{"Bash(brigade:*)"})}, VerdictBlocked},
		{"allow in the local file, ask in the user file", map[string]string{localFile(): rules([]string{"Bash"}, nil, nil), userFile(): rules(nil, []string{"Bash(brigade doing*)"}, nil)}, VerdictBlocked},
		{"allow in the project file alone", map[string]string{projectFile(): rules([]string{"Bash(brigade:*)"}, nil, nil)}, VerdictAllowed},
		{"allow in the local file, unrelated deny in the user file", map[string]string{localFile(): rules([]string{"Bash(brigade:*)"}, nil, nil), userFile(): rules(nil, nil, []string{"Bash(rm:*)"})}, VerdictAllowed},
		// Fail closed on anything that cannot be read as rules.
		{"unparseable user file", map[string]string{userFile(): `{"permissions": {"deny": [` + evilMarker}, VerdictBlocked},
		{"not json", map[string]string{userFile(): `permissions: ` + evilMarker}, VerdictBlocked},
		{"empty file", map[string]string{userFile(): ``}, VerdictBlocked},
		{"a duplicated JSON member", map[string]string{userFile(): `{"permissions": {"allow": ["Bash(brigade:*)"]}, "permissions": {"deny": ["Bash(brigade:*)"]}}`}, VerdictBlocked},
		{"permissions is a string", map[string]string{userFile(): `{"permissions": "` + evilMarker + `"}`}, VerdictBlocked},
		{"an array member that is not a string", map[string]string{userFile(): `{"permissions": {"deny": [1]}}`}, VerdictBlocked},
		{"over the size cap", map[string]string{userFile(): `{"pad": "` + strings.Repeat("x", MaxSettingsBytes) + `"}`}, VerdictBlocked},
		{"an unparseable local file beside a clean user file", map[string]string{userFile(): rules([]string{"Bash"}, nil, nil), localFile(): `{`}, VerdictBlocked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ScanDoingRules(cfgDir, []string{cwd}, files(tc.m))
			if got != tc.want {
				t.Fatalf("ScanDoingRules = %q, want %q", got, tc.want)
			}
			if strings.Contains(string(got), evilMarker) {
				t.Fatalf("the verdict carries settings bytes: %q", got)
			}
		})
	}
}

// TestScanDoingRulesFailsClosedOnReadErrorsAndNoConfigDir: an unresolved
// Claude config directory is Blocked before a file is read; a read error
// other than "missing" is Blocked; a reader that returns content AND an
// error is an error.
func TestScanDoingRulesFailsClosedOnReadErrorsAndNoConfigDir(t *testing.T) {
	t.Parallel()
	calls := 0
	if got := ScanDoingRules("", []string{cwd}, func(string) ([]byte, error) { calls++; return nil, os.ErrNotExist }); got != VerdictBlocked || calls != 0 {
		t.Fatalf("no config dir: %q after %d reads, want blocked after none", got, calls)
	}
	if got := ScanDoingRules(cfgDir, []string{cwd}, func(string) ([]byte, error) { return nil, errors.New("permission denied") }); got != VerdictBlocked {
		t.Fatalf("read error: %q, want blocked", got)
	}
	if got := ScanDoingRules(cfgDir, []string{cwd}, func(string) ([]byte, error) { return []byte(`{}`), errors.New("short read") }); got != VerdictBlocked {
		t.Fatalf("content with an error: %q, want blocked", got)
	}
	// Missing files everywhere is the one "no rules" answer.
	if got := ScanDoingRules(cfgDir, []string{cwd}, func(string) ([]byte, error) { return nil, os.ErrNotExist }); got != VerdictNone {
		t.Fatalf("all missing: %q, want none", got)
	}
}

// TestScanDoingRulesDefaultReaderIsTheFilesystem: nil readFile → os.ReadFile
// over a directory this test owns, including the toplevel's project file
// for a session started in a subdirectory.
func TestScanDoingRulesDefaultReaderIsTheFilesystem(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cfg := filepath.Join(root, "claude-config")
	repo := filepath.Join(root, "repo")
	sub := filepath.Join(repo, "sub")
	for _, d := range []string{cfg, filepath.Join(repo, ".git"), filepath.Join(repo, ".claude"), sub} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if got := ScanDoingRules(cfg, []string{sub}, nil); got != VerdictNone {
		t.Fatalf("no files: %q", got)
	}
	if err := os.WriteFile(filepath.Join(repo, ".claude", "settings.json"), []byte(rules([]string{"Bash(brigade:*)"}, nil, nil)), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ScanDoingRules(cfg, []string{sub}, nil); got != VerdictAllowed {
		t.Fatalf("toplevel allow from a subdirectory: %q, want allowed", got)
	}
	if err := os.WriteFile(filepath.Join(cfg, "settings.json"), []byte(rules(nil, nil, []string{"Bash(brigade doing:*)"})), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := ScanDoingRules(cfg, []string{sub}, nil); got != VerdictBlocked {
		t.Fatalf("user deny over a project allow: %q, want blocked", got)
	}
}

// TestScanDoingRulesEchoesNothing: with the process's default logger
// capturing everything at debug (the scan takes no logger of its own, so
// the default is the only one it could reach) and a marker planted in
// every fixture — a rule, a note, an unparseable file — no byte of any
// settings file reaches the verdict or the log. Not parallel: it swaps the
// default logger for its duration.
func TestScanDoingRulesEchoesNothing(t *testing.T) {
	var captured bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(log.New(&captured, slog.LevelDebug, nil))
	t.Cleanup(func() { slog.SetDefault(previous) })
	for name, m := range map[string]map[string]string{
		"a matching deny carrying the marker":     {userFile(): rules(nil, nil, []string{"Bash(brigade:*) " + evilMarker, "Bash(brigade:*)"})},
		"an allow carrying the marker":            {userFile(): rules([]string{"Bash(brigade:*)"}, nil, nil)},
		"an unparseable file carrying the marker": {userFile(): `{"permissions": ` + evilMarker},
	} {
		got := ScanDoingRules(cfgDir, []string{cwd}, files(m))
		if strings.Contains(string(got), evilMarker) {
			t.Errorf("%s: the verdict carries settings bytes: %q", name, got)
		}
	}
	if strings.Contains(captured.String(), evilMarker) {
		t.Fatalf("a settings byte reached the log:\n%s", captured.String())
	}
}

func TestGlob(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		pattern, s string
		want       bool
	}{
		{"Bash", "Bash", true}, {"Bash", "bash", false}, {"*", "Bash", true}, {"*", "", true},
		{"B*", "Bash", true}, {"B*h", "Bash", true}, {"B*x", "Bash", false}, {"*sh", "Bash", true},
		{"brigade doing*", "brigade doing <<'EOF'", true}, {"brigade send*", "brigade doing <<'EOF'", false},
		{"* doing*", "brigade doing --clear", true}, {"*doing*clear", "brigade doing --clear", true},
		{"b?sh", "bash", false}, {"[Bb]ash", "Bash", false},
	} {
		if got := glob(tc.pattern, tc.s); got != tc.want {
			t.Errorf("glob(%q, %q) = %v, want %v", tc.pattern, tc.s, got, tc.want)
		}
	}
}
