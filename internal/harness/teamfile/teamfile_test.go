package teamfile_test

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/protocol"
)

const validDoc = `{"version":1,"adapter":"supabase","url":"https://abc.supabase.co","publishable_key":"sb_publishable_x","team_ref":"t_4f9c","team_name":"ops"}`

// write puts a team file with the given content into a fresh dir and
// returns its path.
func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), teamfile.FileName)
	//nolint:gosec // G306: a committed team file IS 0644 -- the public mode under test
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// wantReason asserts err is a config refusal carrying exactly reason.
func wantReason(t *testing.T, err error, reason string) {
	t.Helper()
	_ = reasonOf(t, err, reason)
}

// reasonOf is wantReason returning the refusal, for tests that inspect
// its details.
func reasonOf(t *testing.T, err error, reason string) *protocol.Error {
	t.Helper()
	var pe *protocol.Error
	if !errors.As(err, &pe) {
		t.Fatalf("err = %v, want a *protocol.Error", err)
	}
	if pe.Code != protocol.CodeConfig {
		t.Fatalf("code = %q, want config", pe.Code)
	}
	if pe.Details["reason"] != reason {
		t.Fatalf("reason = %q, want %q", pe.Details["reason"], reason)
	}
	return pe
}

func TestParseValid(t *testing.T) {
	t.Parallel()
	f, err := teamfile.Parse(write(t, validDoc))
	if err != nil {
		t.Fatal(err)
	}
	if f.Adapter != "supabase" || f.URL != "https://abc.supabase.co" ||
		f.PublishableKey != "sb_publishable_x" || f.TeamRef != "t_4f9c" || f.TeamName != "ops" {
		t.Fatalf("parsed = %+v", f)
	}
}

func TestParseFsAdapterPlaceholders(t *testing.T) {
	t.Parallel()
	// Correction 6: the fs-adapter dev shape carries a loopback
	// placeholder pair; the closed schema must accept it unchanged.
	doc := `{"version":1,"adapter":"fs","url":"http://127.0.0.1:1","publishable_key":"placeholder","team_ref":"t_dev","team_name":"dev"}`
	if _, err := teamfile.Parse(write(t, doc)); err != nil {
		t.Fatal(err)
	}
}

func TestParseRefusesPoisonFields(t *testing.T) {
	t.Parallel()
	// One case per known poison member: each refuses naming the member,
	// and neither the error nor its details carries the VALUE.
	for _, field := range []string{"adapter_command", "profile", "config_dir", "team_inbound", "frame", "frame_file"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			value := "SENTINEL-VALUE-MUST-NEVER-APPEAR"
			doc := strings.TrimSuffix(validDoc, "}") + fmt.Sprintf(`,%q:%q}`, field, value)
			_, err := teamfile.Parse(write(t, doc))
			pe := reasonOf(t, err, teamfile.ReasonUnknownField)
			if pe.Details["field"] != field {
				t.Fatalf("field = %q, want %q", pe.Details["field"], field)
			}
			all := fmt.Sprintf("%v %v", pe, pe.Details)
			if strings.Contains(all, value) {
				t.Fatalf("the refusal echoed the member's value: %s", all)
			}
		})
	}
}

func TestParseRefusesNonRegularFiles(t *testing.T) {
	t.Parallel()
	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		target := filepath.Join(dir, "real.json")
		//nolint:gosec // G306: the symlink's target mirrors a real committed file
		if err := os.WriteFile(target, []byte(validDoc), 0o644); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(dir, teamfile.FileName)
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		_, err := teamfile.Parse(link)
		wantReason(t, err, teamfile.ReasonNotRegularFile)
	})
	t.Run("directory", func(t *testing.T) {
		t.Parallel()
		dir := filepath.Join(t.TempDir(), teamfile.FileName)
		//nolint:gosec // G301: an ordinary project directory at the path is the case under test
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		_, err := teamfile.Parse(dir)
		wantReason(t, err, teamfile.ReasonNotRegularFile)
	})
	t.Run("fifo", func(t *testing.T) {
		t.Parallel()
		path := filepath.Join(t.TempDir(), teamfile.FileName)
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() {
			_, err := teamfile.Parse(path)
			done <- err
		}()
		select {
		case err := <-done:
			wantReason(t, err, teamfile.ReasonNotRegularFile)
		case <-time.After(5 * time.Second):
			t.Fatal("Parse blocked on a FIFO: the open is not refusing non-regular files")
		}
	})
}

func TestParseRefusesUnixSocket(t *testing.T) {
	t.Parallel()
	// A socket refuses the open itself (EOPNOTSUPP on darwin, ENXIO on
	// linux) and must still carry the not_regular_file token (D3).
	//nolint:usetesting // t.TempDir embeds the long test name; a unix socket path must stay under sockaddr_un's limit
	dir, err := os.MkdirTemp("", "tf")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, teamfile.FileName)
	l, err := new(net.ListenConfig).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Skipf("cannot bind a unix socket here: %v", err)
	}
	t.Cleanup(func() { _ = l.Close() })
	_, perr := teamfile.Parse(path)
	wantReason(t, perr, teamfile.ReasonNotRegularFile)
}

func TestParseRefusesWorldWritable(t *testing.T) {
	t.Parallel()
	path := write(t, validDoc)
	//nolint:gosec // G302: the world-writable mode is the PRECONDITION this test refuses
	if err := os.Chmod(path, 0o646); err != nil {
		t.Fatal(err)
	}
	_, err := teamfile.Parse(path)
	wantReason(t, err, teamfile.ReasonWorldWritable)
}

func TestParseSizeCapBoundary(t *testing.T) {
	t.Parallel()
	exact := validDoc + strings.Repeat(" ", teamfile.MaxBytes-len(validDoc))
	if _, err := teamfile.Parse(write(t, exact)); err != nil {
		t.Fatalf("exactly MaxBytes must parse: %v", err)
	}
	_, err := teamfile.Parse(write(t, exact+" "))
	wantReason(t, err, teamfile.ReasonTooLarge)
}

func TestParseRefusesSecretShapes(t *testing.T) {
	t.Parallel()
	t.Run("a full brg1 join secret anywhere in the file", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"team_name":"ops"`,
			`"team_name":"brg1.t-x.stolen-join-secret-value"`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonSecretShaped)
	})
	t.Run("an sb_secret_ publishable_key", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"publishable_key":"sb_publishable_x"`,
			`"publishable_key":"sb_secret_not_the_publishable_one"`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonSecretKey)
	})
	t.Run("a JSON-escaped secret in a value is caught post-decode (D1)", func(t *testing.T) {
		t.Parallel()
		// The raw-byte scan cannot see through \u002d; the decoded value
		// is a usable secret and must refuse all the same.
		doc := strings.Replace(validDoc, `"team_ref":"t_4f9c"`,
			`"team_ref":"brg1.t-x.stolen\u002djoin1234"`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonSecretShaped)
	})
	t.Run("a JSON-escaped secret in team_name is caught post-decode (D1)", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"team_name":"ops"`,
			`"team_name":"\u0062rg1.t-x.stolenjoin12345"`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonSecretShaped)
	})
	t.Run("a secret smuggled as a member NAME refuses without echoing (D1)", func(t *testing.T) {
		t.Parallel()
		doc := strings.TrimSuffix(validDoc, "}") + `,"\u0062rg1.t-x.stolenjoin12345":"x"}`
		_, err := teamfile.Parse(write(t, doc))
		pe := reasonOf(t, err, teamfile.ReasonSecretShaped)
		all := fmt.Sprintf("%v %v", pe, pe.Details)
		if strings.Contains(all, "stolenjoin12345") {
			t.Fatalf("the refusal echoed the smuggled secret: %s", all)
		}
	})
	t.Run("a dotted-team-ref secret ParseJoinSecret accepts is caught (D2)", func(t *testing.T) {
		t.Parallel()
		// The shape regexp cannot cross the ref's dot; ParseJoinSecret can.
		doc := strings.Replace(validDoc, `"team_ref":"t_4f9c"`,
			`"team_ref":"brg1.t.x.stolenjoin12345"`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonSecretShaped)
	})
	t.Run("the bare prefix and two-part forms stay legitimate", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"team_name":"ops"`,
			`"team_name":"brg1. and brg1.x"`, 1)
		if _, err := teamfile.Parse(write(t, doc)); err != nil {
			t.Fatal(err)
		}
	})
}

func TestParseURLRule(t *testing.T) {
	t.Parallel()
	cases := []struct {
		url    string
		reason string // "" means accepted
	}{
		{"https://abc.supabase.co", ""},
		{"http://127.0.0.1:54321", ""},
		{"http://localhost:54321", ""},
		{"http://127.9.9.9", ""},
		{"http://evil.example.com", teamfile.ReasonURLNotHTTPS},
		{"ftp://abc.supabase.co", teamfile.ReasonURLNotHTTPS},
		{"not a url", teamfile.ReasonMalformed},
	}
	for _, c := range cases {
		t.Run(c.url, func(t *testing.T) {
			t.Parallel()
			doc := strings.Replace(validDoc, `"url":"https://abc.supabase.co"`,
				fmt.Sprintf(`"url":%q`, c.url), 1)
			_, err := teamfile.Parse(write(t, doc))
			if c.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			wantReason(t, err, c.reason)
		})
	}
}

func TestParseAdapterNameRule(t *testing.T) {
	t.Parallel()
	for _, bad := range []string{"Supabase", "-fs", "a b", "a/b", "../x", strings.Repeat("a", 33), "a.b"} {
		t.Run(bad, func(t *testing.T) {
			t.Parallel()
			doc := strings.Replace(validDoc, `"adapter":"supabase"`, fmt.Sprintf(`"adapter":%q`, bad), 1)
			_, err := teamfile.Parse(write(t, doc))
			wantReason(t, err, teamfile.ReasonAdapterUnknown)
		})
	}
}

func TestParseVersionAndShapeRules(t *testing.T) {
	t.Parallel()
	t.Run("version 2 is unsupported", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"version":1`, `"version":2`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonUnsupportedVersion)
	})
	t.Run("a string version is malformed", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"version":1`, `"version":"1"`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonMalformed)
	})
	t.Run("not JSON at all", func(t *testing.T) {
		t.Parallel()
		_, err := teamfile.Parse(write(t, "not json"))
		wantReason(t, err, teamfile.ReasonMalformed)
	})
	t.Run("a missing required member", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"team_ref":"t_4f9c",`, ``, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonMalformed)
	})
	t.Run("an empty url", func(t *testing.T) {
		t.Parallel()
		doc := strings.Replace(validDoc, `"url":"https://abc.supabase.co"`, `"url":""`, 1)
		_, err := teamfile.Parse(write(t, doc))
		wantReason(t, err, teamfile.ReasonMalformed)
	})
}

func TestTeamNameSanitized(t *testing.T) {
	t.Parallel()
	hostile := "evil\u202e\"<name>\n" + strings.Repeat("x", 200)
	doc := strings.Replace(validDoc, `"team_name":"ops"`, fmt.Sprintf(`"team_name":%q`, hostile), 1)
	f, err := teamfile.Parse(write(t, doc))
	if err != nil {
		t.Fatal(err)
	}
	if utf8.RuneCountInString(f.TeamName) > teamfile.TeamNameMaxRunes {
		t.Fatalf("team name is %d runes, cap is %d", utf8.RuneCountInString(f.TeamName), teamfile.TeamNameMaxRunes)
	}
	for _, banned := range []string{"\u202e", `"`, "<", ">", "\n"} {
		if strings.Contains(f.TeamName, banned) {
			t.Fatalf("team name %q still carries %q", f.TeamName, banned)
		}
	}
}

func TestReasonsListIsClosed(t *testing.T) {
	t.Parallel()
	want := []string{
		teamfile.ReasonNotRegularFile, teamfile.ReasonWorldWritable, teamfile.ReasonTooLarge,
		teamfile.ReasonSecretShaped, teamfile.ReasonSecretKey, teamfile.ReasonMalformed,
		teamfile.ReasonUnknownField, teamfile.ReasonUnsupportedVersion, teamfile.ReasonURLNotHTTPS,
		teamfile.ReasonAdapterUnknown,
	}
	got := teamfile.Reasons()
	if len(got) != len(want) {
		t.Fatalf("Reasons() has %d tokens, want %d: the list is closed and every change needs a hook line and a test", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Reasons()[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- discovery -----------------------------------------------------------

// mkRepo creates dir/.git as a directory (plain clone) or a regular file
// (linked worktree).
func mkRepo(t *testing.T, dir string, worktree bool) {
	t.Helper()
	//nolint:gosec // G301: a checkout tree is an ordinary 0755 directory
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	git := filepath.Join(dir, ".git")
	if worktree {
		//nolint:gosec // G306: a worktree's .git file is an ordinary 0644 file
		if err := os.WriteFile(git, []byte("gitdir: /elsewhere/.git/worktrees/x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // G301: as above
	if err := os.Mkdir(git, 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestDiscoverFindsNearestWithinToplevel(t *testing.T) {
	t.Parallel()
	for _, worktree := range []bool{false, true} {
		name := map[bool]string{false: "plain clone", true: "linked worktree"}[worktree]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			top := filepath.Join(root, "repo")
			mkRepo(t, top, worktree)
			// A file ABOVE the toplevel must never be discovered.
			//nolint:gosec // G306: a committed team file IS 0644
			if err := os.WriteFile(filepath.Join(root, teamfile.FileName), []byte(validDoc), 0o644); err != nil {
				t.Fatal(err)
			}
			sub := filepath.Join(top, "a", "b")
			//nolint:gosec // G301: as above
			if err := os.MkdirAll(sub, 0o755); err != nil {
				t.Fatal(err)
			}
			if _, ok := teamfile.Discover(sub); ok {
				t.Fatal("discovered a file above the repository toplevel")
			}
			// The toplevel file is found from a subdirectory…
			topFile := filepath.Join(top, teamfile.FileName)
			//nolint:gosec // G306: as above
			if err := os.WriteFile(topFile, []byte(validDoc), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok := teamfile.Discover(sub)
			if !ok || got != topFile {
				t.Fatalf("Discover = %q, %v; want %q", got, ok, topFile)
			}
			// …and a nearer file wins over the root one.
			nearer := filepath.Join(sub, teamfile.FileName)
			//nolint:gosec // G306: as above
			if err := os.WriteFile(nearer, []byte(validDoc), 0o644); err != nil {
				t.Fatal(err)
			}
			got, ok = teamfile.Discover(sub)
			if !ok || got != nearer {
				t.Fatalf("Discover = %q, %v; want the nearer %q", got, ok, nearer)
			}
		})
	}
}

func TestDiscoverNoRepoMeansNoDiscoveryAndNoOpens(t *testing.T) {
	t.Parallel()
	// The non-repo arm (owner ruling 2): a cwd inside no git repository
	// consults no ancestor. The planted ancestor file is a FIFO whose
	// open would BLOCK forever, so completing at all proves the walk
	// never opened it; the Lstat-only walk must also answer false.
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, teamfile.FileName), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "scratch", "foo")
	//nolint:gosec // G301: as above
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	done := make(chan bool, 1)
	go func() {
		_, ok := teamfile.Discover(sub)
		done <- ok
	}()
	select {
	case ok := <-done:
		if ok {
			t.Fatal("a cwd inside no repository discovered a team file")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Discover blocked: it opened a file it must not even consult")
	}
}

func TestDiscoverFromTheToplevelItself(t *testing.T) {
	t.Parallel()
	top := filepath.Join(t.TempDir(), "repo")
	mkRepo(t, top, false)
	topFile := filepath.Join(top, teamfile.FileName)
	//nolint:gosec // G306: a committed team file IS 0644
	if err := os.WriteFile(topFile, []byte(validDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	got, ok := teamfile.Discover(top)
	if !ok || got != topFile {
		t.Fatalf("Discover from the toplevel = %q, %v; want %q", got, ok, topFile)
	}
}

func TestDiscoverRepoWithNoFileIsNotAnError(t *testing.T) {
	t.Parallel()
	top := filepath.Join(t.TempDir(), "repo")
	mkRepo(t, top, false)
	if _, ok := teamfile.Discover(top); ok {
		t.Fatal("a repo with no team file discovered something")
	}
}

func TestCanonicalizeAgreesAcrossSymlinkedCheckout(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	realDir := filepath.Join(root, "real-checkout")
	//nolint:gosec // G301: as above
	if err := os.MkdirAll(realDir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-checkout")
	if err := os.Symlink(realDir, link); err != nil {
		t.Fatal(err)
	}
	a, err := teamfile.Canonicalize(realDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := teamfile.Canonicalize(link)
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("canonical paths disagree: %q vs %q — a pin written through one would miss through the other", a, b)
	}
}

func FuzzParse(f *testing.F) {
	f.Add([]byte(validDoc))
	f.Add([]byte(`{"version":1,"adapter":"fs","url":"http://127.0.0.1:1","publishable_key":"p","team_ref":"t","team_name":"x","frame":"noop"}`))
	f.Add([]byte(`{"version":1,"adapter":"supabase","url":"https://x.co","publishable_key":"brg1.t-x.forged-join-secret","team_ref":"t"}`))
	f.Add([]byte("not json"))
	f.Add([]byte(`{"version":2}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > teamfile.MaxBytes {
			data = data[:teamfile.MaxBytes]
		}
		path := filepath.Join(t.TempDir(), teamfile.FileName)
		//nolint:gosec // G306: as above
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Skip()
		}
		file, err := teamfile.Parse(path)
		if err != nil {
			var pe *protocol.Error
			if errors.As(err, &pe) {
				if pe.Details["reason"] == "" {
					t.Fatalf("a refusal with no reason token: %v", pe)
				}
			}
			return
		}
		if utf8.RuneCountInString(file.TeamName) > teamfile.TeamNameMaxRunes {
			t.Fatalf("an accepted file's team name escaped the cap: %q", file.TeamName)
		}
		if file.Adapter == "" || file.URL == "" || file.PublishableKey == "" || file.TeamRef == "" {
			t.Fatalf("an accepted file has an empty required member: %+v", file)
		}
	})
}
