package hook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/harness/teamstore"
)

// The P7-6 security roster (brief §4/§5): the hook is attach-only, ever.
// Every test here runs the real session-start path against the seam and
// asserts the refusal SHAPE — zero spawns, zero store writes, the fixed
// line — not just the exit code.

// storeFingerprint hashes every file under the config dir, so "the hook
// wrote nothing" is checked against bytes, not beliefs.
func storeFingerprint(t *testing.T, dir string) string {
	t.Helper()
	var b strings.Builder
	//nolint:gosec // G122: a test fingerprint over the test's own temp dir; no adversary races it
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			data, _ := os.ReadFile(p) //nolint:gosec // G304: the walk's own path
			b.WriteString(p)
			b.Write(data)
		}
		return nil
	})
	return b.String()
}

// TestHookAttachOnlyNoPinNoSpawn is the brief's §4 point 3, pinned: a
// valid team file with NO pin gets exactly the not-joined line, zero
// adapter spawns, zero network, zero store writes.
func TestHookAttachOnlyNoPinNoSpawn(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seam := f.useSeam(nil)
	if err := os.Remove(filepath.Join(f.configDir, "projects.json")); err != nil {
		t.Fatal(err)
	}
	before := storeFingerprint(t, f.configDir)
	exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 {
		t.Fatal(exit)
	}
	want := "Brigade: not joined: this project uses team \"ops\" — run `brigade team join` here or in a terminal (a first join on this machine needs `--secret-file <path>`, the join secret saved to a file outside the repository)."
	if got := lines(out); len(got) != 1 || got[0] != want {
		t.Fatalf("lines = %q, want exactly %q", got, want)
	}
	// P7-11: the start facts are down even though nothing attached — the
	// store an in-session join must write is in them — and they live in
	// the state directory, not the store (the fingerprint below holds).
	facts, err := f.store().ReadStart(f.pid)
	if err != nil || facts.ConfigDir != f.configDir {
		t.Fatalf("start facts on the not-joined path: %+v, %v (want config dir %q)", facts, err, f.configDir)
	}
	if n := len(seam.calls); n != 0 {
		t.Fatalf("%d adapter spawns on the not-joined path, want 0", n)
	}
	if storeFingerprint(t, f.configDir) != before {
		t.Fatal("the hook wrote to the store on the not-joined path")
	}
	if f.mapExists() {
		t.Fatal("a by-pid map was written without an attach")
	}
}

// TestHookStartFactsCarryTheConfigDirOption is P7-11's acceptance 2: the
// config_dir OPTION — which never reaches the Bash tool — is what the
// start facts carry, not the XDG default, so an in-session join lands in
// the persona's store. (With the option pointing at an empty store there
// is no pin, so this is the not-joined path again: no map, no spawn.)
func TestHookStartFactsCarryTheConfigDirOption(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seam := f.useSeam(nil)
	persona := filepath.Join(t.TempDir(), "persona-config")
	if exit, _, _ := f.run(SubSessionStart, f.startDoc("startup"), config.OptionConfigDir+"="+persona); exit != 0 {
		t.Fatal(exit)
	}
	facts, err := f.store().ReadStart(f.pid)
	if err != nil || facts.ConfigDir != persona {
		t.Fatalf("start facts = %+v, %v; want config dir %q", facts, err, persona)
	}
	if len(seam.calls) != 0 || f.mapExists() {
		t.Fatalf("spawns %d, map %v on the not-joined path", len(seam.calls), f.mapExists())
	}
}

// TestHookSwapDriftAttachesToNeither is the team-swap defense end to end:
// the checkout is pinned to team A, a PR re-points the file to team B —
// which this user ALSO holds a credential for — and the session attaches
// to NEITHER, with the fixed drift line carrying not one byte of the
// drifted file.
func TestHookSwapDriftAttachesToNeither(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	seam := f.useSeam(nil)
	// The user also has team B's credential.
	const urlB, refB = "https://evil-marker-host.example.com", "t_EVILMARKERREF"
	keyB := teamstore.Key("supabase", urlB, refB)
	if err := adapterkit.SaveProfile(f.configDir, keyB, &adapterkit.Profile{
		Version: adapterkit.ProfileVersion, Adapter: "supabase",
		URL: urlB, PublishableKey: "sb_publishable_b", TeamRef: refB, TeamName: "evilteam",
	}); err != nil {
		t.Fatal(err)
	}
	// The PR: the committed file now names team B; the pin still says A.
	doc := `{"version":1,"adapter":"supabase","url":"` + urlB + `","publishable_key":"sb_publishable_b","team_ref":"` + refB + `","team_name":"evilteam"}`
	if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	before := storeFingerprint(t, f.configDir)
	exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
	if exit != 0 {
		t.Fatal(exit)
	}
	if got := lines(out); len(got) != 1 || got[0] != driftLine {
		t.Fatalf("lines = %q, want exactly the drift line", got)
	}
	// Not one byte of the drifted file is echoed.
	for _, marker := range []string{"evil", "EVILMARKER", refB, urlB, keyB} {
		if strings.Contains(out, marker) {
			t.Fatalf("the drift line echoed %q from the hostile file", marker)
		}
	}
	if len(seam.calls) != 0 || f.mapExists() {
		t.Fatal("the swap attached or spawned something")
	}
	if storeFingerprint(t, f.configDir) != before {
		t.Fatal("the swap path wrote to the store")
	}
}

// TestHookSilentOffBothArms: no team file in the repo, and no repo at
// all with a clean plant in an ancestor — both say NOTHING on stdout and
// spawn nothing (a repo without Brigade must not nag; an ancestor file
// outside a repo must not exist as far as the hook is concerned).
func TestHookSilentOffBothArms(t *testing.T) {
	t.Parallel()
	t.Run("a repo with no team file", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		seam := f.useSeam(nil)
		if err := os.Remove(filepath.Join(f.cwd, teamfile.FileName)); err != nil {
			t.Fatal(err)
		}
		exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
		if exit != 0 || out != "" {
			t.Fatalf("exit %d out %q, want silence", exit, out)
		}
		if len(seam.calls) != 0 || f.mapExists() {
			t.Fatal("a team-less repo spawned or registered something")
		}
	})
	t.Run("no repo at all, a plant in an ancestor", func(t *testing.T) {
		t.Parallel()
		f := newFixture(t)
		seam := f.useSeam(nil)
		root := t.TempDir()
		// The plant would parse clean if it were ever consulted.
		doc := `{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"p","team_ref":"t","team_name":"x"}`
		if err := os.WriteFile(filepath.Join(root, teamfile.FileName), []byte(doc), 0o600); err != nil {
			t.Fatal(err)
		}
		sub := filepath.Join(root, "scratch", "foo")
		//nolint:gosec // G301: an ordinary directory tree outside any store
		if err := os.MkdirAll(sub, 0o750); err != nil {
			t.Fatal(err)
		}
		f.cwd = sub
		exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
		if exit != 0 || out != "" {
			t.Fatalf("exit %d out %q, want silence", exit, out)
		}
		if len(seam.calls) != 0 || f.mapExists() {
			t.Fatal("a repo-less cwd spawned or registered something")
		}
	})
}

// TestHookTeamFileLines renders one fixed line per token of the parser's
// closed list, with the two urgent secret lines taking their own forms.
func TestHookTeamFileLines(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		content string
		mode    os.FileMode
		want    string
	}{
		teamfile.ReasonWorldWritable: {
			content: `{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"p","team_ref":"t"}`,
			mode:    0o646,
			want:    "Brigade: not connected (config: team_file_world_writable): fix .brigade.json.",
		},
		teamfile.ReasonUnknownField: {
			content: `{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"p","team_ref":"t","frame":"noop"}`,
			mode:    0o600,
			want:    "Brigade: not connected (config: team_file_unknown_field): fix .brigade.json.",
		},
		teamfile.ReasonMalformed: {
			content: "not json",
			mode:    0o600,
			want:    "Brigade: not connected (config: team_file_malformed): fix .brigade.json.",
		},
		teamfile.ReasonUnsupportedVersion: {
			content: `{"version":2,"adapter":"supabase","url":"https://x.co","publishable_key":"p","team_ref":"t"}`,
			mode:    0o600,
			want:    "Brigade: not connected (config: team_file_unsupported_version): fix .brigade.json.",
		},
		teamfile.ReasonURLNotHTTPS: {
			content: `{"version":1,"adapter":"supabase","url":"http://evil.example.com","publishable_key":"p","team_ref":"t"}`,
			mode:    0o600,
			want:    "Brigade: not connected (config: team_file_url_not_https): fix .brigade.json.",
		},
		teamfile.ReasonAdapterUnknown: {
			content: `{"version":1,"adapter":"Not A Name","url":"https://x.co","publishable_key":"p","team_ref":"t"}`,
			mode:    0o600,
			want:    "Brigade: not connected (config: team_file_adapter_unknown): fix .brigade.json.",
		},
		teamfile.ReasonSecretShaped: {
			content: `{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"p","team_ref":"brg1.t-x.stolen-join-value1"}`,
			mode:    0o600,
			want:    "Brigade: not connected: .brigade.json contains what looks like a join secret — remove it and rotate now (brigade team rotate-secret).",
		},
		teamfile.ReasonSecretKey: {
			content: `{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"sb_secret_not_publishable","team_ref":"t"}`,
			mode:    0o600,
			want:    "Brigade: not connected: .brigade.json's publishable_key looks like a Supabase secret key — remove it and rotate that key in the Supabase dashboard.",
		},
	}
	// The closed list stays covered: every token has either a case here
	// or an os-level fixture elsewhere (not_regular_file and too_large
	// are teamfile package tests; their hook rendering shares the one
	// generic line this table pins).
	for reason, tc := range cases {
		t.Run(reason, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			seam := f.useSeam(nil)
			path := filepath.Join(f.cwd, teamfile.FileName)
			if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
				t.Fatal(err)
			}
			// chmod explicitly: WriteFile's mode is umask-masked, and the
			// world-writable case needs its bit to survive.
			if err := os.Chmod(path, tc.mode); err != nil {
				t.Fatal(err)
			}
			exit, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
			if exit != 0 {
				t.Fatal(exit)
			}
			if got := lines(out); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("lines = %q\nwant  %q", got, tc.want)
			}
			if len(seam.calls) != 0 || f.mapExists() {
				t.Fatal("a refused team file spawned or registered something")
			}
		})
	}
}

// TestHookNotJoinedLineSanitisesTeamName: the one repo-sourced string
// that ever reaches the model arrives sanitized and capped.
func TestHookNotJoinedLineSanitisesTeamName(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	f.useSeam(nil)
	hostile := "evil\u202e\"<name>" + strings.Repeat("x", 200)
	doc := `{"version":1,"adapter":"supabase","url":"https://abc.supabase.co","publishable_key":"sb_publishable_x","team_ref":"team-1","team_name":` + string(mustJSON(hostile)) + `}`
	if err := os.WriteFile(filepath.Join(f.cwd, teamfile.FileName), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.configDir, "projects.json")); err != nil {
		t.Fatal(err)
	}
	_, out, _ := f.run(SubSessionStart, f.startDoc("startup"))
	for _, banned := range []string{"\u202e", "<name>", strings.Repeat("x", 40)} {
		if strings.Contains(out, banned) {
			t.Fatalf("the not-joined line carries %q unsanitised: %q", banned, out)
		}
	}
	if !strings.Contains(out, "not joined") {
		t.Fatalf("out = %q, want the not-joined line", out)
	}
}
