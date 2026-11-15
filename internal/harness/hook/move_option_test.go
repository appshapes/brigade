//go:build !windows

package hook

import (
	"encoding/json/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/testutil"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

// TestStartFactsCarryTheTeamMoveOptionFromEveryHostsSource: the option that
// keeps `brigade team move` out of a session (work item 328, ADR 0007)
// reaches the start facts from each harness's own source of options: the
// plugin option in Claude Code, and the host's options file in Muse Code,
// Codex CLI and Grok Build. In each of them `on`, an empty value and no
// value at all leave the facts without the member, which a command reads as
// on; `off` writes the word a command refuses on; and any other value, a
// word in another case and a JSON `false` included, writes that word too
// and is said at the session's start, without the value. The facts are read
// the way a command in that session's shell reads them: by CLAUDE_PID, or
// through the start pointer from the host's native id.
func TestStartFactsCarryTheTeamMoveOptionFromEveryHostsSource(t *testing.T) {
	t.Parallel()
	type value struct {
		name string
		// text is the option as Claude Code hands it, and doc as an options
		// file spells it; skip leaves a spelling out where a source has none.
		text, doc string
		set       bool
		on, said  bool
	}
	values := []value{
		{name: "no value", on: true},
		{name: "on", text: "on", doc: `"on"`, set: true, on: true},
		{name: "empty", text: "", doc: `""`, set: true, on: true},
		{name: "off", text: "off", doc: `"off"`, set: true},
		{name: "another word", text: "sometimes", doc: `"sometimes"`, set: true, said: true},
		{name: "off in another case", text: "OFF", doc: `"OFF"`, set: true, said: true},
		{name: "false", text: "false", doc: `false`, set: true, said: true},
	}
	for _, host := range []config.Host{config.HostClaude, config.HostMuse, config.HostCodex, config.HostGrok} {
		for _, v := range values {
			t.Run(string(host)+"/"+v.name, func(t *testing.T) {
				t.Parallel()
				var (
					f     *fixture
					start func() (int, string, string)
					shell []string
				)
				// optionsFile writes the host's options file: the adapter the
				// fixture runs, and the option as this case spells it.
				optionsFile := func(f *fixture, name string) {
					t.Helper()
					f.deps.ReadFile = os.ReadFile
					adapter, err := json.Marshal(f.adapterOption())
					if err != nil {
						t.Fatal(err)
					}
					body := `{"adapter_command": ` + string(adapter) + `, "sync": "off"`
					if v.set {
						body += `, "team_move_in_session": ` + v.doc
					}
					if err := os.MkdirAll(f.configDir, 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(f.configDir, name), []byte(body+"}"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				switch host {
				case config.HostClaude:
					f = newFixture(t)
					f.useSeam(map[string][]fakeadapter.Response{"session register": {okResp(registerDoc("brigade-sess-1", "payments-api", false))}})
					var extra []string
					if v.set {
						extra = append(extra, config.OptionTeamMoveInSession+"="+v.text)
					}
					start = func() (int, string, string) { return f.run(SubSessionStart, f.startDoc("startup"), extra...) }
					shell = []string{"HOME=" + f.dirs.Home, "CLAUDE_PID=" + strconv.Itoa(f.pid)}
				case config.HostMuse, config.HostCodex:
					f = buildFixture(t, host)
					f.spawner.watcherPID = testutil.NewSleeper(t)
					f.noAdapterOption = true
					optionsFile(f, config.OptionsFile(host))
					doc := f.museStartDoc("default")
					if host == config.HostCodex {
						doc = f.codexDoc("SessionStart", "startup")
					}
					start = func() (int, string, string) { return f.run(SubSessionStart, doc) }
					shell = []string{"HOME=" + f.dirs.Home, host.NativeIDVar() + "=" + f.nativeID}
				case config.HostGrok:
					f = grokFixture(t)
					optionsFile(f, config.GrokOptionsFile)
					start = func() (int, string, string) {
						// Grok prints one JSON document: its systemMessage is what the session is told.
						exit, out, errOut := grokRun(f, SubSessionStart, grokDoc(f, "session_start", "new", nil))
						var ui map[string]string
						if err := json.Unmarshal([]byte(out), &ui); err != nil {
							t.Fatalf("the start's document: %v\n%s", err, out)
						}
						return exit, ui["systemMessage"], errOut
					}
					shell = []string{"HOME=" + f.dirs.Home, host.NativeIDVar() + "=" + f.nativeID, "GROK_WORKSPACE_ROOT=" + f.cwd}
				}
				exit, out, errOut := start()
				if exit != 0 {
					t.Fatalf("exit %d: %s", exit, errOut)
				}
				key, err := config.StateKey(shell, f.store())
				if err != nil {
					t.Fatalf("the key of the session's facts, from its shell: %v", err)
				}
				facts, err := f.store().ReadStart(key)
				if err != nil {
					t.Fatalf("the start facts: %v", err)
				}
				want := ""
				if !v.on {
					want = config.SyncOff
				}
				if facts.TeamMoveInSession != want {
					t.Fatalf("start facts team_move_in_session = %q, want %q", facts.TeamMoveInSession, want)
				}
				if on, _ := config.ParseTeamMoveInSession(facts.TeamMoveInSession); on != v.on {
					t.Fatalf("a command in this session reads the option as on=%v, want %v", on, v.on)
				}
				if said := strings.Contains(out, config.WarnTeamMoveInSessionInvalid); said != v.said {
					t.Fatalf("the start said the option's warning: %v, want %v\n%s", said, v.said, out)
				}
			})
		}
	}
}
