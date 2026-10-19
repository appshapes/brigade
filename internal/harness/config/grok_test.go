package config_test

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/sessionmap"
)

const grokID = "02000000-0000-7000-8000-000000000001"

func grokEnv() []string { return []string{"HOME=/h", config.GrokSessionIDVar + "=" + grokID} }

func TestGrokIdentityAndOptionsIsolation(t *testing.T) {
	t.Parallel()
	env := append(grokEnv(), "XDG_CONFIG_HOME=/other", "XDG_STATE_HOME=/other", "BRIGADE_CONFIG_DIR=/other", config.OptionTeamInbound+"=accept", config.OptionConfigDir+"=/other")
	if config.HostOf(env) != config.HostGrok || !config.InSession(env) || !config.HostGrok.Pointed() || config.HostGrok.Name() != "Grok Build" {
		t.Fatal("Grok is not explicitly recognized")
	}
	if got, err := config.NativeID(config.HostGrok, env); err != nil || got != grokID {
		t.Fatalf("native ID %q %v", got, err)
	}
	path, err := config.OptionsPath(env)
	if err != nil || path != "/h/.config/brigade/grok-options.json" {
		t.Fatalf("options path %q %v", path, err)
	}
	if dir, err := config.BrigadeStateDir(env); err != nil || dir != "/h/.local/state/brigade" {
		t.Fatalf("state dir %q %v", dir, err)
	}
	for _, body := range []string{"", `{}`, `{"team_inbound":"hold", "config_dir":"/persona", "label":"none"}`} {
		clean, warn := config.OptionsEnviron(env, path, func(p string) ([]byte, error) {
			if p != path {
				t.Fatalf("read another host's options: %s", p)
			}
			if body == "" {
				return nil, fs.ErrNotExist
			}
			return []byte(body), nil
		})
		if warn != "" {
			t.Fatal(warn)
		}
		opts, err := config.ParseOptions(clean)
		if err != nil {
			t.Fatal(err)
		}
		wantDir := "/h/.config/brigade"
		wantInbound := config.InboundAccept
		if strings.Contains(body, "hold") {
			wantDir = "/persona"
			wantInbound = config.InboundHold
		}
		if opts.ConfigDir != wantDir || opts.TeamInbound != wantInbound {
			t.Fatalf("inherited options leaked: %+v", opts)
		}
	}
	for _, marker := range []string{"CLAUDE_PID=17", "MUSE_SESSION_ID=other", "CODEX_THREAD_ID=other"} {
		mixed := append(grokEnv(), marker)
		if config.HostOf(mixed) != config.HostGrok {
			t.Fatal("Grok fell into another host")
		}
		_, err := config.NativeID(config.HostGrok, mixed)
		assertReason(t, err, config.ReasonAmbiguousHost)
	}
	for _, id := range []string{"../bad", strings.Repeat("a", 81)} {
		_, err := config.NativeID(config.HostGrok, []string{config.GrokSessionIDVar + "=" + id})
		assertReason(t, err, config.ReasonInvalidNativeID)
	}
}

func TestGrokPointersBindFullIdentity(t *testing.T) {
	t.Parallel()
	for _, mutation := range []string{"none", "native", "host", "brigade", "missing", "malformed", "private", "other namespace", "leader process"} {
		t.Run(mutation, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			store := sessionmap.Store{StateDir: dir}
			m := sampleMap(777)
			m.Harness = "grok"
			m.Grok = &sessionmap.GrokReceiver{Cwd: "/synthetic", ProcessStart: "start", Incarnation: strings.Repeat("a", 32), MonitorState: "unarmed"}
			m.ClaudeSessionID = grokID
			m.SocketPath = ""
			ptr := sessionmap.ByHost{HostPID: 777, BrigadeSessionID: m.BrigadeSessionID}
			switch mutation {
			case "native":
				m.ClaudeSessionID = "other"
			case "host":
				m.Harness = "codex"
			case "leader process":
				m.HostProcessPID = 42
			case "brigade":
				m.BrigadeSessionID = "other"
			}
			if mutation != "missing" {
				writeMap(t, dir, m)
			}
			host := "grok"
			if mutation == "other namespace" {
				host = "codex"
			}
			if err := store.WriteByHost(host, grokID, &ptr); err != nil {
				t.Fatal(err)
			}
			// A valid start pointer must not hide a broken attached pointer.
			if err := store.WriteStartPointer("grok", grokID, 999, time.Now()); err != nil {
				t.Fatal(err)
			}
			if mutation == "malformed" || mutation == "private" {
				p, _ := store.ByHostPath("grok", grokID)
				if mutation == "malformed" {
					if err := os.WriteFile(p, []byte("{"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := os.Chmod(p, 0644); err != nil { //nolint:gosec // G302: the insecure pointer is the refusal precondition
						t.Fatal(err)
					}
				}
			}
			got, err := config.Session(grokEnv(), dir)
			if mutation == "none" {
				if err != nil || got.ClaudePID != 777 {
					t.Fatalf("session %+v %v", got, err)
				}
				key, e := config.StateKey(grokEnv(), store)
				if e != nil || key != 777 {
					t.Fatalf("key %d %v", key, e)
				}
				return
			}
			if err == nil {
				t.Fatal("accepted a broken pointer")
			}
			if mutation != "other namespace" {
				if _, err := config.StateKey(grokEnv(), store); err == nil {
					t.Fatal("StateKey hid invalid attachment with start pointer")
				}
			}
		})
	}
}

func TestGrokSiblingPointersAreIndependent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	store := sessionmap.Store{StateDir: dir}
	for i, id := range []string{grokID, "02000000-0000-7000-8000-000000000002"} {
		m := sampleMap(777 + i)
		m.Harness = "grok"
		m.Grok = &sessionmap.GrokReceiver{Cwd: "/synthetic", ProcessStart: "start", Incarnation: strings.Repeat("a", 32), MonitorState: "unarmed"}
		m.ClaudeSessionID = id
		m.HostProcessPID = 0
		m.SocketPath = ""
		m.BrigadeSessionID += id
		writeMap(t, dir, m)
		if err := store.WriteByHost("grok", id, &sessionmap.ByHost{HostPID: m.ClaudePID, BrigadeSessionID: m.BrigadeSessionID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.DeleteByHost("grok", grokID); err != nil {
		t.Fatal(err)
	}
	_, err := store.ReadByHost("grok", grokID)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatal(err)
	}
	env := []string{config.GrokSessionIDVar + "=02000000-0000-7000-8000-000000000002"}
	m, err := config.Session(env, dir)
	if err != nil || m.ClaudePID != 778 || m.HostProcessPID != 0 {
		t.Fatalf("sibling %+v %v", m, err)
	}
}
