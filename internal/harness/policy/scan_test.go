package policy

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	cfgDir = "/home/u/.claude-test"
	cwd    = "/work/repo"
)

// files is an injected reader over an in-memory map of path → content;
// a path not in the map is fs.ErrNotExist, exactly like a missing file.
func files(m map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if s, ok := m[path]; ok {
			return []byte(s), nil
		}
		return nil, os.ErrNotExist
	}
}

func userFile() string    { return filepath.Join(cfgDir, "settings.json") }
func projectFile() string { return filepath.Join(cwd, ".claude", "settings.json") }
func localFile() string   { return filepath.Join(cwd, ".claude", "settings.local.json") }

func TestSettingsFilesOrderAndPaths(t *testing.T) {
	t.Parallel()
	got := SettingsFiles(cfgDir, cwd)
	want := []string{localFile(), projectFile(), userFile()}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("SettingsFiles = %q, want %q", got, want)
	}
	if got := SettingsFiles("", cwd); len(got) != 2 {
		t.Fatalf("no config dir: %q", got)
	}
	if got := SettingsFiles(cfgDir, ""); len(got) != 1 || got[0] != userFile() {
		t.Fatalf("no cwd: %q", got)
	}
}

func TestScanEachFile(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		path string
	}{
		{"user", userFile()},
		{"project", projectFile()},
		{"local", localFile()},
	} {
		for _, v := range []string{NativeHold, NativeRefuse} {
			s := ScanNative(cfgDir, cwd, files(map[string]string{tc.path: `{"crossSessionInbound": "` + v + `"}`}))
			if !s.Found || s.Value != v || s.File != tc.path {
				t.Errorf("%s/%s: %+v", tc.name, v, s)
			}
			if len(s.Checked) != 3 {
				t.Errorf("%s/%s: checked %q, want all three", tc.name, v, s.Checked)
			}
		}
	}
}

func TestScanNoneWhenNothingSet(t *testing.T) {
	t.Parallel()
	for name, m := range map[string]map[string]string{
		"no files":            {},
		"empty objects":       {userFile(): `{}`, projectFile(): `{}`, localFile(): `{}`},
		"explicit accept":     {userFile(): `{"crossSessionInbound": "accept"}`},
		"unknown value":       {userFile(): `{"crossSessionInbound": "sometimes"}`},
		"wrong case value":    {userFile(): `{"crossSessionInbound": "Refuse"}`},
		"wrong case key":      {userFile(): `{"crosssessioninbound": "refuse"}`},
		"non-string":          {userFile(): `{"crossSessionInbound": true}`},
		"null":                {userFile(): `{"crossSessionInbound": null}`},
		"object value":        {userFile(): `{"crossSessionInbound": {"value": "refuse"}}`},
		"nested under key":    {userFile(): `{"permissions": {"crossSessionInbound": "refuse"}}`},
		"nested in array":     {userFile(): `[{"crossSessionInbound": "refuse"}]`},
		"malformed":           {userFile(): `{"crossSessionInbound": "refuse"`},
		"not json":            {userFile(): `crossSessionInbound: refuse`},
		"empty file":          {userFile(): ``},
		"duplicate member":    {userFile(): `{"crossSessionInbound": "accept", "crossSessionInbound": "refuse"}`},
		"over the size cap":   {userFile(): `{"crossSessionInbound": "refuse", "pad": "` + strings.Repeat("x", MaxSettingsBytes) + `"}`},
		"string mentions key": {userFile(): `{"note": "crossSessionInbound refuse"}`},
	} {
		s := ScanNative(cfgDir, cwd, files(m))
		if s.Found || s.Value != "" || s.File != "" {
			t.Errorf("%s: unexpected hit %+v", name, s)
		}
		if s.Warning() != "" {
			t.Errorf("%s: warning on a miss: %q", name, s.Warning())
		}
	}
}

func TestScanPrecedenceAnyHitWins(t *testing.T) {
	t.Parallel()
	// Any hold/refuse in any file wins over an accept elsewhere; when
	// several files carry one, the most specific (local > project > user)
	// is the one reported.
	cases := []struct {
		name      string
		m         map[string]string
		wantValue string
		wantFile  string
	}{
		{"user refuse, project accept", map[string]string{userFile(): `{"crossSessionInbound":"refuse"}`, projectFile(): `{"crossSessionInbound":"accept"}`}, NativeRefuse, userFile()},
		{"user accept, project refuse", map[string]string{userFile(): `{"crossSessionInbound":"accept"}`, projectFile(): `{"crossSessionInbound":"refuse"}`}, NativeRefuse, projectFile()},
		{"user hold, local refuse", map[string]string{userFile(): `{"crossSessionInbound":"hold"}`, localFile(): `{"crossSessionInbound":"refuse"}`}, NativeRefuse, localFile()},
		{"project refuse, local hold", map[string]string{projectFile(): `{"crossSessionInbound":"refuse"}`, localFile(): `{"crossSessionInbound":"hold"}`}, NativeHold, localFile()},
		{"local malformed, user refuse", map[string]string{localFile(): `{`, userFile(): `{"crossSessionInbound":"refuse"}`}, NativeRefuse, userFile()},
		{"local accept, user hold", map[string]string{localFile(): `{"crossSessionInbound":"accept"}`, userFile(): `{"crossSessionInbound":"hold"}`}, NativeHold, userFile()},
	}
	for _, tc := range cases {
		s := ScanNative(cfgDir, cwd, files(tc.m))
		if !s.Found || s.Value != tc.wantValue || s.File != tc.wantFile {
			t.Errorf("%s: %+v, want %s in %s", tc.name, s, tc.wantValue, tc.wantFile)
		}
	}
}

func TestScanReadErrorsAreNone(t *testing.T) {
	t.Parallel()
	calls := 0
	s := ScanNative(cfgDir, cwd, func(string) ([]byte, error) {
		calls++
		return nil, errors.New("permission denied")
	})
	if s.Found {
		t.Fatalf("hit on a read error: %+v", s)
	}
	if calls != 3 {
		t.Fatalf("read %d files, want 3", calls)
	}
	// A reader that returns content AND an error is an error.
	s = ScanNative(cfgDir, cwd, func(string) ([]byte, error) {
		return []byte(`{"crossSessionInbound":"refuse"}`), errors.New("short read")
	})
	if s.Found {
		t.Fatalf("hit despite a read error: %+v", s)
	}
}

func TestScanDefaultReaderIsTheFilesystem(t *testing.T) {
	t.Parallel()
	// nil readFile → os.ReadFile, over a directory this test owns; never
	// the developer's config dir.
	root := t.TempDir()
	cfg := filepath.Join(root, "claude-config")
	work := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(work, ".claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(cfg, 0o700); err != nil {
		t.Fatal(err)
	}
	if s := ScanNative(cfg, work, nil); s.Found {
		t.Fatalf("hit with no files: %+v", s)
	}
	local := filepath.Join(work, ".claude", "settings.local.json")
	if err := os.WriteFile(local, []byte(`{"crossSessionInbound": "refuse"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s := ScanNative(cfg, work, nil)
	if !s.Found || s.Value != NativeRefuse || s.File != local {
		t.Fatalf("real file: %+v", s)
	}
}

func TestScanWarningNamesSettingValueAndFile(t *testing.T) {
	t.Parallel()
	s := Scan{Found: true, Value: NativeHold, File: localFile()}
	w := s.Warning()
	for _, want := range []string{SettingKey, `"hold"`, localFile(), "refuse", "Brigade:"} {
		if !strings.Contains(w, want) {
			t.Errorf("warning %q lacks %q", w, want)
		}
	}
	if strings.Contains(w, "\n") {
		t.Errorf("warning is not one line: %q", w)
	}
	if (Scan{}).Warning() != "" {
		t.Error("a miss must have no warning")
	}
}
