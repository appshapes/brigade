package hook

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// teamFileBase is seedTeam's document without its closing brace, so a
// test can append members (a `sync` member above all) and keep the pin
// and the binding matching.
const teamFileBase = `{"version":1,"adapter":"supabase","url":"https://abc.supabase.co","publishable_key":"sb_publishable_x","team_ref":"team-1","team_name":"ops"`

// writeTeamFile rewrites the fixture checkout's team file with members
// appended to seedTeam's own.
func (f *fixture) writeTeamFile(members string) {
	f.t.Helper()
	if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), []byte(teamFileBase+members+"}"), 0o600); err != nil {
		f.t.Fatal(err)
	}
}

// TestSyncLines pins what SessionStart freezes for file sync and the one
// line it prints right after the context line (folder-sync plan §4.3):
// a usable member with folders and the option on freezes the adapter's
// name, the folders as written and the canonical toplevel, and says `file
// sync on`; the option off, or no folders, freezes nothing and says why;
// an unusable member keeps card 32's line and gets no second; a project
// without the member says nothing at all. In every case the session
// connects.
func TestSyncLines(t *testing.T) {
	t.Parallel()
	const unusable = "Brigade: .brigade.json's sync member is not usable (folder_not_relative); file sync is off for this session."
	cases := map[string]struct {
		members string
		option  string // CLAUDE_PLUGIN_OPTION_SYNC; "" leaves it unset
		subdir  string // a cwd below the toplevel
		folders []string
		adapter string
		want    []string
	}{
		"no sync member": {},
		"two folders through the default adapter": {
			members: `,"sync":{"folders":[".context/plans","docs/shared"]}`,
			folders: []string{".context/plans", "docs/shared"}, adapter: "syncthing",
			want: []string{"Brigade: file sync on: 2 folder(s) through syncthing."},
		},
		"another adapter, the option spelled on, from a subdirectory": {
			members: `,"sync":{"adapter":"rsync-ish","folders":["shared"]}`, option: "on", subdir: "src/deep",
			folders: []string{"shared"}, adapter: "rsync-ish",
			want: []string{"Brigade: file sync on: 1 folder(s) through rsync-ish."},
		},
		"the option off": {
			members: `,"sync":{"folders":["shared"]}`, option: "off",
			want: []string{"Brigade: file sync off (the sync option is off)."},
		},
		"the option neither word": {
			members: `,"sync":{"folders":["shared"]}`, option: "sometimes",
			want: []string{"Brigade: file sync off (the sync option is off).", config.WarnSyncInvalid},
		},
		"no folders listed": {
			members: `,"sync":{"folders":[]}`,
			want:    []string{"Brigade: file sync off (no folders listed)."},
		},
		"no folders member": {
			members: `,"sync":{"adapter":"syncthing"}`,
			want:    []string{"Brigade: file sync off (no folders listed)."},
		},
		"an unusable member, one line": {
			members: `,"sync":{"folders":["/abs"]}`,
			want:    []string{unusable},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "x", false))}})
			f.writeTeamFile(tc.members)
			top := f.cwd
			if tc.subdir != "" {
				f.cwd = filepath.Join(top, tc.subdir)
				//nolint:gosec // G301: a checkout tree is an ordinary directory
				if err := os.MkdirAll(f.cwd, 0o750); err != nil {
					t.Fatal(err)
				}
			}
			var extra []string
			if tc.option != "" {
				extra = append(extra, config.OptionSync+"="+tc.option)
			}
			exit, out, errOut := f.run(SubSessionStart, f.startDoc("startup"), extra...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if got := strings.Join(seam.verbs(), ","); got != "describe,session register" {
				t.Fatalf("calls %q: the session must connect whatever sync says", got)
			}
			got := lines(out)
			if len(got) != 1+len(tc.want) || !strings.HasPrefix(got[0], "Brigade: this session is ") {
				t.Fatalf("lines = %q\nwant the context line then %q", got, tc.want)
			}
			for i, w := range tc.want {
				if got[1+i] != w {
					t.Fatalf("line %d = %q\nwant      %q", 1+i, got[1+i], w)
				}
			}
			m := f.mustMap()
			if tc.adapter == "" {
				if m.SyncAdapter != "" || m.SyncFolders != nil || m.SyncRoot != "" {
					t.Fatalf("sync frozen for a session that syncs nothing: %q %v %q", m.SyncAdapter, m.SyncFolders, m.SyncRoot)
				}
				return
			}
			if m.SyncAdapter != tc.adapter || !slices.Equal(m.SyncFolders, tc.folders) || m.SyncRoot != realpath(t, top) {
				t.Fatalf("frozen sync = %q %v %q, want %q %v %q", m.SyncAdapter, m.SyncFolders, m.SyncRoot, tc.adapter, tc.folders, realpath(t, top))
			}
		})
	}
}

// TestSyncChangeRespawnsTheWatcher: the watcher reads the sync members
// once, so on the continue path (/clear) a changed `sync` member or a
// flipped option replaces it, exactly as a changed coordinate does; the
// same member and option keep it. The map carries the new resolution
// either way.
func TestSyncChangeRespawnsTheWatcher(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		members string
		option  string
		respawn bool
		folders []string
	}{
		{"unchanged", `,"sync":{"folders":["docs"]}`, "", false, []string{"docs"}},
		{"a folder added", `,"sync":{"folders":["docs","shared"]}`, "", true, []string{"docs", "shared"}},
		{"another adapter", `,"sync":{"adapter":"other","folders":["docs"]}`, "", true, []string{"docs"}},
		{"the option turned off", `,"sync":{"folders":["docs"]}`, "off", true, nil},
		{"the member removed", ``, "", true, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			old := testutil.NewSleeper(t)
			f.spawner.watcherPID = old
			f.useSeam(map[string][]fakeadapter.Response{
				"session register":  {okResp(registerDoc("brigade-sess-1", "payments-api", false))},
				"session heartbeat": {okResp(heartbeatDoc())},
			})
			f.writeTeamFile(`,"sync":{"folders":["docs"]}`)
			if exit, _, errOut := f.run(SubSessionStart, f.startDoc("startup")); exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			f.writeTeamFile(tc.members)
			replacement := testutil.NewSleeper(t)
			f.spawner.watcherPID = replacement
			var extra []string
			if tc.option != "" {
				extra = append(extra, config.OptionSync+"="+tc.option)
			}
			exit, _, errOut := f.run(SubSessionStart, f.startDoc("clear"), extra...)
			if exit != 0 {
				t.Fatalf("exit %d: %s", exit, errOut)
			}
			if !tc.respawn {
				if f.spawner.count() != 1 || !alive(old) {
					t.Fatalf("the watcher was respawned (%d) or killed (%v) with sync unchanged", f.spawner.count(), alive(old))
				}
			} else {
				testutil.Eventually(t, pollTimeout, pollInterval, func() bool { return !alive(old) })
				if f.spawner.count() != 2 || !strings.Contains(errOut, "sync changed; respawning") {
					t.Fatalf("spawns %d, stderr %q: want a respawn for the sync change", f.spawner.count(), errOut)
				}
			}
			if m := f.mustMap(); !slices.Equal(m.SyncFolders, tc.folders) {
				t.Fatalf("map sync_folders = %v, want %v", m.SyncFolders, tc.folders)
			}
		})
	}
}
