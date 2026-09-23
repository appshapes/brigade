package teamfile_test

import (
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/teamfile"
)

// withSync is validDoc with a `sync` member of the given raw JSON.
func withSync(member string) string {
	return strings.TrimSuffix(validDoc, "}") + `,"sync":` + member + `}`
}

// TestSyncRules runs every §4.1 rule both ways. A rejected member is
// NEVER a refusal: the file parses, Sync is nil, the token says why, and
// the team the file names is untouched — which is what lets the session
// connect.
func TestSyncRules(t *testing.T) {
	t.Parallel()
	base, err := teamfile.Parse(write(t, validDoc))
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("a", teamfile.MaxSyncFolderBytes)
	many := make([]string, teamfile.MaxSyncFolders)
	for i := range many {
		many[i] = fmt.Sprintf("%q", fmt.Sprintf("d%02d", i))
	}
	cases := []struct {
		name, member string
		reason       string   // "" means usable
		adapter      string   // want, when usable
		folders      []string // want, when usable
	}{
		// Usable.
		{"the plan's example", `{"adapter":"syncthing","folders":[".context/plans","docs/shared"]}`, "", "syncthing", []string{".context/plans", "docs/shared"}},
		{"adapter defaults to syncthing", `{"folders":["notes"]}`, "", "syncthing", []string{"notes"}},
		{"another adapter name", `{"adapter":"rsync-2","folders":["notes"]}`, "", "rsync-2", []string{"notes"}},
		{"no folders member syncs nothing", `{}`, "", "syncthing", []string{}},
		{"an empty folders array", `{"folders":[]}`, "", "syncthing", []string{}},
		{"exactly the folder cap", `{"folders":[` + strings.Join(many, ",") + `]}`, "", "syncthing", nil},
		{"exactly the folder byte cap", `{"folders":["` + long + `"]}`, "", "syncthing", []string{long}},
		// Open by default (§4.1): everything below names a clean relative
		// path, so the parser takes it; whether the engine can use it is
		// the engine's to report.
		{"dot-directories, .git included", `{"folders":[".github/x",".git","a/.git/hooks"]}`, "", "syncthing", []string{".github/x", ".git", "a/.git/hooks"}},
		{"the team file itself", `{"folders":[".brigade.json"]}`, "", "syncthing", []string{".brigade.json"}},
		{"a path above the checkout", `{"folders":["..","../elsewhere","../../b"]}`, "", "syncthing", []string{"..", "../elsewhere", "../../b"}},
		{"duplicates and nesting", `{"folders":["docs","docs","Docs","docs/shared"]}`, "", "syncthing", []string{"docs", "docs", "Docs", "docs/shared"}},
		{"unicode letters are fine", `{"folders":["notas/café"]}`, "", "syncthing", []string{"notas/café"}},
		{"backslash, quote, control and format characters", `{"folders":["a\\b","a\"b","a\nb","a\u202eb"]}`, "", "syncthing", []string{"a\\b", "a\"b", "a\nb", "a\u202eb"}},
		// Unusable, one per token.
		{"sync is an array", `["docs"]`, teamfile.SyncNotObject, "", nil},
		{"sync is a string", `"docs"`, teamfile.SyncNotObject, "", nil},
		{"sync is null", `null`, teamfile.SyncNotObject, "", nil},
		{"adapter is a path", `{"adapter":"/usr/bin/evil","folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"adapter is a command", `{"adapter":"sh -c x","folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"adapter is uppercase", `{"adapter":"Syncthing","folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"adapter is empty", `{"adapter":"","folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"adapter is too long", `{"adapter":"` + strings.Repeat("a", 33) + `","folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"adapter is a number", `{"adapter":1,"folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"adapter is null", `{"adapter":null,"folders":["a"]}`, teamfile.SyncAdapterInvalid, "", nil},
		{"folders is a string", `{"folders":"docs"}`, teamfile.SyncFoldersInvalid, "", nil},
		{"folders holds a number", `{"folders":["a",1]}`, teamfile.SyncFoldersInvalid, "", nil},
		{"folders is null", `{"folders":null}`, teamfile.SyncFoldersInvalid, "", nil},
		{"one folder over the cap", `{"folders":[` + strings.Join(many, ",") + `,"one-more"]}`, teamfile.SyncTooManyFolders, "", nil},
		{"one byte over the byte cap", `{"folders":["` + long + `b"]}`, teamfile.SyncFolderTooLong, "", nil},
		{"absolute", `{"folders":["/etc"]}`, teamfile.SyncFolderNotRelative, "", nil},
		{"an empty folder", `{"folders":[""]}`, teamfile.SyncFolderNotClean, "", nil},
		{"a trailing slash", `{"folders":["docs/"]}`, teamfile.SyncFolderNotClean, "", nil},
		{"a leading ./", `{"folders":["./docs"]}`, teamfile.SyncFolderNotClean, "", nil},
		{"a double slash", `{"folders":["a//b"]}`, teamfile.SyncFolderNotClean, "", nil},
		{"an inner .", `{"folders":["a/./b"]}`, teamfile.SyncFolderNotClean, "", nil},
		{"a .. that cleans away", `{"folders":["a/../b"]}`, teamfile.SyncFolderNotClean, "", nil},
		{"the whole checkout", `{"folders":["."]}`, teamfile.SyncFolderRoot, "", nil},
		{"one bad entry spoils the member", `{"folders":["good","/bad"]}`, teamfile.SyncFolderNotRelative, "", nil},
	}
	covered := map[string]bool{}
	for _, c := range cases {
		covered[c.reason] = true
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f, err := teamfile.Parse(write(t, withSync(c.member)))
			if err != nil {
				t.Fatalf("an unusable sync member refused the file: %v", err)
			}
			assertSyncInvariants(t, f)
			if c.reason != "" {
				if f.Sync != nil || f.SyncUnusable != c.reason {
					t.Fatalf("sync = %+v, unusable = %q; want nil and %q", f.Sync, f.SyncUnusable, c.reason)
				}
			} else {
				if f.Sync == nil || f.SyncUnusable != "" {
					t.Fatalf("sync = %+v, unusable = %q; want a usable member", f.Sync, f.SyncUnusable)
				}
				if f.Sync.Adapter != c.adapter || (c.folders != nil && !slices.Equal(f.Sync.Folders, c.folders)) {
					t.Fatalf("sync = %+v, want adapter %q folders %q", f.Sync, c.adapter, c.folders)
				}
			}
			// The team is the team, sync or no sync.
			f.Sync, f.SyncUnusable, f.Ignored = nil, "", nil
			if !reflect.DeepEqual(f, base) {
				t.Fatalf("a sync member changed the team: %+v, want %+v", f, base)
			}
		})
	}
	for _, token := range teamfile.SyncReasons() {
		if !covered[token] {
			t.Errorf("no case yields %q", token)
		}
	}
}

// TestSyncUnusableEchoesNoValue: the token is all an unusable member
// leaves behind; not one byte of the folder travels.
func TestSyncUnusableEchoesNoValue(t *testing.T) {
	t.Parallel()
	const marker = "SENTINEL-FOLDER-VALUE"
	f, err := teamfile.Parse(write(t, withSync(`{"folders":["/`+marker+`"]}`)))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%+v", f); strings.Contains(got, marker) {
		t.Fatalf("the parse carries the rejected folder: %s", got)
	}
}

func TestSyncReasonsListIsClosed(t *testing.T) {
	t.Parallel()
	want := []string{
		"not_object", "adapter_invalid", "folders_invalid", "too_many_folders",
		"folder_too_long", "folder_not_relative", "folder_not_clean", "folder_root",
	}
	if got := teamfile.SyncReasons(); !slices.Equal(got, want) {
		t.Fatalf("SyncReasons() = %q\nwant %q: the list is closed and every change needs a test", got, want)
	}
}

// TestParseLargestLegalFile builds the largest file the members this
// version reads can make — every bounded member at its bound, the other
// values generously long, hand-indented — and shows it parses within
// MaxBytes, with the whole sync member usable.
func TestParseLargestLegalFile(t *testing.T) {
	t.Parallel()
	folders := make([]string, teamfile.MaxSyncFolders)
	for i := range folders {
		// Each exactly at the byte cap, of the character with the longest
		// JSON spelling a folder may now hold: a control character, which
		// JSON must escape as six bytes (\u0001).
		prefix := fmt.Sprintf("f%02d/", i)
		folders[i] = prefix + strings.Repeat("\x01", teamfile.MaxSyncFolderBytes-len(prefix))
	}
	doc := map[string]any{
		"version":         1,
		"adapter":         strings.Repeat("a", 32),
		"url":             "https://" + strings.Repeat("r", 63) + ".supabase.co",
		"publishable_key": "sb_publishable_" + strings.Repeat("k", 64),
		"team_ref":        "0a465da2-da2f-468e-abb6-2a6e2fd0460d",
		"team_name":       strings.Repeat("\U0001F680", 64), // 64 four-byte runes
		"sync":            map[string]any{"adapter": strings.Repeat("s", 32), "folders": folders},
	}
	data, err := json.Marshal(doc, jsontext.WithIndent("  "))
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("the largest legal file, indented: %d of %d bytes", len(data), teamfile.MaxBytes)
	if len(data) > teamfile.MaxBytes {
		t.Fatalf("the largest legal file is %d bytes, over MaxBytes %d", len(data), teamfile.MaxBytes)
	}
	f, err := teamfile.Parse(write(t, string(data)))
	if err != nil {
		t.Fatal(err)
	}
	if f.Sync == nil || !slices.Equal(f.Sync.Folders, folders) {
		t.Fatalf("the maximal sync member did not survive: %+v (%q)", f.Sync, f.SyncUnusable)
	}
}

// TestParse010FilesUnchanged: the files 0.10.0 wrote and read parse to
// exactly what they parsed to then — no sync, nothing ignored — so an
// existing team sees no change from opening the file.
func TestParse010FilesUnchanged(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		doc  string
		want teamfile.File
	}{
		"the supabase shape": {validDoc, teamfile.File{
			Version: 1, Adapter: "supabase", URL: "https://abc.supabase.co",
			PublishableKey: "sb_publishable_x", TeamRef: "t_4f9c", TeamName: "ops",
		}},
		"the fs placeholders": {
			`{"version":1,"adapter":"fs","url":"http://127.0.0.1:1","publishable_key":"placeholder","team_ref":"t_dev","team_name":"dev"}`,
			teamfile.File{Version: 1, Adapter: "fs", URL: "http://127.0.0.1:1", PublishableKey: "placeholder", TeamRef: "t_dev", TeamName: "dev"},
		},
		// The member order `team create` produces (this repository's own
		// file has it), placeholder values.
		"create's own member order": {
			`{"adapter":"supabase","url":"https://ref.supabase.co","publishable_key":"sb_publishable_p","team_ref":"0a465da2-da2f-468e-abb6-2a6e2fd0460d","team_name":"brigade","version":1}` + "\n",
			teamfile.File{Version: 1, Adapter: "supabase", URL: "https://ref.supabase.co", PublishableKey: "sb_publishable_p", TeamRef: "0a465da2-da2f-468e-abb6-2a6e2fd0460d", TeamName: "brigade"},
		},
		"no team_name": {
			`{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"p","team_ref":"t"}`,
			teamfile.File{Version: 1, Adapter: "supabase", URL: "https://x.co", PublishableKey: "p", TeamRef: "t"},
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f, err := teamfile.Parse(write(t, c.doc))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*f, c.want) {
				t.Fatalf("parsed %+v\nwant   %+v", *f, c.want)
			}
		})
	}
}

// TestCarriedSync is what `team create --force` carries from the file it
// replaces: a usable member as parsed; nothing and no cause when there is
// none; and nothing plus a fixed cause token — never an error, since the
// replacement always goes ahead — when a member may be there but cannot
// be carried.
func TestCarriedSync(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		doc           string // "" means no file at all
		worldWritable bool
		want          *teamfile.SyncConfig
		cause         string
	}{
		"no file":             {"", false, nil, ""},
		"a file without sync": {validDoc, false, nil, ""},
		"a usable member": {
			withSync(`{"folders":["b","a"],"future":true}`), false,
			&teamfile.SyncConfig{Adapter: "syncthing", Folders: []string{"b", "a"}}, "",
		},
		"a refused file with no sync": {"not json", false, nil, ""},
		"an unusable member":          {withSync(`{"folders":["/x"]}`), false, nil, teamfile.SyncFolderNotRelative},
		"a refused file that declares sync": {
			strings.Replace(withSync(`{"folders":["a"]}`), `"version":1`, `"version":2`, 1), false, nil, teamfile.ReasonUnsupportedVersion,
		},
		"a file read refuses unread": {withSync(`{"folders":["a"]}`), true, nil, teamfile.ReasonWorldWritable},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			p := filepath.Join(t.TempDir(), teamfile.FileName)
			if c.doc != "" {
				p = write(t, c.doc)
			}
			if c.worldWritable {
				//nolint:gosec // G302: the world-writable mode is the precondition under test
				if err := os.Chmod(p, 0o646); err != nil {
					t.Fatal(err)
				}
			}
			got, cause := teamfile.CarriedSync(p)
			if !reflect.DeepEqual(got, c.want) || cause != c.cause {
				t.Fatalf("= %+v, %q; want %+v, %q", got, cause, c.want, c.cause)
			}
		})
	}
}

// assertSyncInvariants is what every parse must hold, fuzzed or not:
// never both a member and a reason; a usable member within every bound
// and every §4.1 folder rule; every reason from the closed list; every
// ignored name plain.
func assertSyncInvariants(t *testing.T, f *teamfile.File) {
	t.Helper()
	if f.Sync != nil && f.SyncUnusable != "" {
		t.Fatalf("both a usable sync and a reason: %+v", f)
	}
	if f.SyncUnusable != "" && !slices.Contains(teamfile.SyncReasons(), f.SyncUnusable) {
		t.Fatalf("an unlisted sync reason %q", f.SyncUnusable)
	}
	for _, name := range f.Ignored {
		if strings.ContainsFunc(name, func(r rune) bool {
			return (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && !strings.ContainsRune("_-.?", r)
		}) {
			t.Fatalf("ignored name %q is not plain", name)
		}
	}
	if f.Sync == nil {
		return
	}
	if f.Sync.Adapter == "" || len(f.Sync.Folders) > teamfile.MaxSyncFolders {
		t.Fatalf("an accepted sync member escaped a bound: %+v", f.Sync)
	}
	for _, folder := range f.Sync.Folders {
		if folder == "" || len(folder) > teamfile.MaxSyncFolderBytes || path.IsAbs(folder) ||
			path.Clean(folder) != folder || folder == "." {
			t.Fatalf("an accepted folder %q breaks a rule", folder)
		}
	}
}
