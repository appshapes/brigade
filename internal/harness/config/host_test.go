package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/frame"
)

// TestTheHostTable reads the table through what the tree asks of it (work
// item 380, ADR 0019): one row a host, one column a fact. A harness added
// later adds a row here, and its name to the lint rule's pattern in
// .golangci.yml, which spells the names out; a fact that comes to differ
// adds a column. Nothing else in the tree names a host for either. The
// plugin root and the history root have tests of their own below, and so
// have the three columns only this package asks. The receiver column is
// held where Grok Build's receiver is, in grok_test.go.
func TestTheHostTable(t *testing.T) {
	t.Parallel()
	type row struct {
		name, nativeIDVar, optionsFile, frame string
		history                               config.HistoryKind
		delivery                              config.DeliveryKind
		dialect                               config.DialectKind
		hookNamed, receiver                   bool
		historyVar                            string
		pointed, rename, stop                 bool
		discardsPrompt, staysOnMove, inWhoami bool
	}
	for host, want := range map[config.Host]row{
		config.HostClaude: {name: "Claude Code", rename: true, stop: true},
		config.HostMuse: {name: "Muse Code", nativeIDVar: "MUSE_SESSION_ID", optionsFile: "muse-options.json", frame: frame.HostMuse,
			history: config.HistoryDatedStore, delivery: config.DeliverFollowedSink, dialect: config.DialectMonitor,
			pointed: true, inWhoami: true},
		config.HostCodex: {name: "Codex CLI", nativeIDVar: "CODEX_THREAD_ID", optionsFile: "codex-options.json", frame: frame.HostCodex,
			history: config.HistoryRollouts, delivery: config.DeliverQueue, dialect: config.DialectThread, hookNamed: true,
			historyVar: "CODEX_HOME", pointed: true, inWhoami: true},
		config.HostGrok: {name: "Grok Build", nativeIDVar: "GROK_SESSION_ID", optionsFile: "grok-options.json", frame: frame.HostGrok,
			history: config.HistorySummaries, delivery: config.DeliverPagedSink, dialect: config.DialectNative, hookNamed: true,
			receiver: true, historyVar: "GROK_HOME",
			pointed: true, stop: true, discardsPrompt: true, staysOnMove: true, inWhoami: true},
		config.HostAntigravity: {name: "Antigravity CLI", nativeIDVar: "ANTIGRAVITY_CONVERSATION_ID",
			optionsFile: "antigravity-options.json", frame: frame.HostAntigravity,
			history: config.HistoryAnnotations, historyVar: "ANTIGRAVITY_APP_DATA_DIR", delivery: config.DeliverNextCall,
			dialect: config.DialectInjectSteps, hookNamed: true, pointed: true, stop: true, inWhoami: true},
	} {
		got := row{
			name: host.Name(), nativeIDVar: host.NativeIDVar(), optionsFile: config.OptionsFile(host), frame: host.Frame(),
			history: host.History(), delivery: host.Delivery(), historyVar: host.HistoryVar(),
			dialect: host.Dialect(), hookNamed: host.NamedByHook(), receiver: host.BindsReceiver(),
			pointed: host.Pointed(), rename: host.CanRename(), stop: host.HasStop(),
			discardsPrompt: host.DiscardsPromptOutput(), staysOnMove: host.StaysOnMove(), inWhoami: host.NamedInWhoami(),
		}
		if got != want {
			t.Errorf("%s:\n got %+v\nwant %+v", host, got, want)
		}
	}
}

// TestAHostTheTableDoesNotHoldGetsNo: every yes or no is worded so that a
// host with no row gets what it got when each was a condition on a host's
// name, and its name is its own word.
func TestAHostTheTableDoesNotHoldGetsNo(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{"", "copilot"} {
		if host.Name() != string(host) || host.NativeIDVar() != "" || config.OptionsFile(host) != "" || host.Frame() != "" ||
			host.History() != config.HistoryNone || host.Delivery() != config.DeliverSocket || host.HistoryVar() != "" ||
			host.Dialect() != config.DialectPlugin || host.NamedByHook() || host.BindsReceiver() ||
			host.Pointed() || host.CanRename() || host.HasStop() || host.DiscardsPromptOutput() || host.StaysOnMove() ||
			host.PluginRoot([]string{"CLAUDE_PLUGIN_ROOT=/c", "MUSE_PLUGIN_ROOT=/m", "GROK_PLUGIN_ROOT=/g"}, "/flag") != "" ||
			host.HistoryRoot([]string{adapterkit.HomeVar + "=" + t.TempDir(), "CODEX_HOME=" + t.TempDir(), "GROK_HOME=" + t.TempDir()}) != "" {
			t.Errorf("host %q is given a fact of some row", host)
		}
		// The one fact whose "no" is a line: a session of a host that is
		// not Claude Code says which.
		if !host.NamedInWhoami() {
			t.Errorf("host %q is left out of whoami as Claude Code is", host)
		}
	}
}

// TestThePluginRootIsNamedAsEachHostNamesIt: a variable of the hook's
// environment, or the hook's --plugin-root where the host passes one, the
// argument first.
func TestThePluginRootIsNamedAsEachHostNamesIt(t *testing.T) {
	t.Parallel()
	env := []string{"CLAUDE_PLUGIN_ROOT=/claude", "MUSE_PLUGIN_ROOT=/muse", "GROK_PLUGIN_ROOT=/grok"}
	for _, tc := range []struct {
		host       config.Host
		flag, want string
	}{
		{config.HostClaude, "", "/claude"},
		{config.HostClaude, "/flag", "/claude"},
		{config.HostMuse, "", "/muse"},
		{config.HostMuse, "/flag", "/muse"},
		{config.HostCodex, "/flag", "/flag"},
		{config.HostCodex, "", ""},
		{config.HostGrok, "/flag", "/flag"},
		{config.HostGrok, "", "/grok"},
		{config.HostAntigravity, "/flag", "/flag"},
		{config.HostAntigravity, "", ""},
	} {
		if got := tc.host.PluginRoot(env, tc.flag); got != tc.want {
			t.Errorf("%s with --plugin-root %q: %q, want %q", tc.host, tc.flag, got, tc.want)
		}
	}
	for _, host := range config.Hosts() {
		if got := host.PluginRootIsWorkingDir(); got != (host == config.HostAntigravity) {
			t.Errorf("%s: a relative --plugin-root is the working directory %v", host, got)
		}
	}
}

// TestTheHistoryRootIsTheHostsOwn: the host's variable when it is set and
// absolute, never a relative one joined onto the home directory, else its
// directory under the home directory; none for Claude Code, and none
// without a home. The home is named as the platform names it
// (adapterkit.HomeVar: HOME, USERPROFILE on Windows), and the paths are
// real directories, which are absolute on every platform.
func TestTheHistoryRootIsTheHostsOwn(t *testing.T) {
	t.Parallel()
	home, abs := t.TempDir(), t.TempDir()
	at := func(extra ...string) []string { return append([]string{adapterkit.HomeVar + "=" + home}, extra...) }
	for _, tc := range []struct {
		name    string
		host    config.Host
		environ []string
		want    string
	}{
		{"Claude Code keeps none Brigade reads", config.HostClaude, at(), ""},
		{"Muse Code under the home", config.HostMuse, at(), filepath.Join(home, ".local", "share", "muse")},
		{"Muse Code never by XDG", config.HostMuse, at("XDG_DATA_HOME=" + abs), filepath.Join(home, ".local", "share", "muse")},
		{"Codex CLI under the home", config.HostCodex, at(), filepath.Join(home, ".codex")},
		{"Codex CLI by CODEX_HOME", config.HostCodex, at("CODEX_HOME=" + abs), abs},
		{"Codex CLI, a relative CODEX_HOME", config.HostCodex, at("CODEX_HOME=store"), ""},
		{"Grok Build under the home", config.HostGrok, at(), filepath.Join(home, ".grok")},
		{"Grok Build by GROK_HOME", config.HostGrok, at("GROK_HOME=" + abs), abs},
		{"Grok Build, a relative GROK_HOME", config.HostGrok, at("GROK_HOME=store"), ""},
		{"Grok Build is not asked CODEX_HOME", config.HostGrok, at("CODEX_HOME=" + abs), filepath.Join(home, ".grok")},
		{"no home", config.HostCodex, nil, ""},
	} {
		if got := tc.host.HistoryRoot(tc.environ); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestAKindOfHistoryKnowsItsRoot: a reader that knows its kind of history
// and no host asks the kind where that history is kept, and is told the
// root of the host that keeps it so.
func TestAKindOfHistoryKnowsItsRoot(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	environ := []string{adapterkit.HomeVar + "=" + home}
	for kind, host := range map[config.HistoryKind]config.Host{
		config.HistoryDatedStore: config.HostMuse, config.HistoryRollouts: config.HostCodex, config.HistorySummaries: config.HostGrok,
		config.HistoryAnnotations: config.HostAntigravity,
	} {
		if got, want := kind.Root(environ), host.HistoryRoot(environ); got == "" || got != want {
			t.Errorf("%s: root %q, want %s's, %q", kind, got, host, want)
		}
	}
	for _, kind := range []config.HistoryKind{config.HistoryNone, "a-kind-no-host-has"} {
		if got := kind.Root(environ); got != "" {
			t.Errorf("%q: root %q, want none", kind, got)
		}
	}
	// A kind's root is one host's. Two hosts that keep one kind make the
	// question a host's, and Root's comment says what then.
	keeps := map[config.HistoryKind]config.Host{}
	for _, host := range config.Hosts() {
		kind := host.History()
		if kind == config.HistoryNone {
			continue
		}
		if other, taken := keeps[kind]; taken {
			t.Errorf("%s and %s both keep a %q history: HistoryKind.Root answers for the first alone", other, host, kind)
		}
		keeps[kind] = host
	}
}

// TestTheColumnsOnlyThisPackageAsks: directories from HOME alone (Trusted
// drops XDG_* in a pointed host's session and keeps it in Claude Code's),
// another host's marker refused and not resolved (Grok Build), and
// inherited plugin options that are no defaults (Grok Build).
func TestTheColumnsOnlyThisPackageAsks(t *testing.T) {
	t.Parallel()
	id := "0199c0de-0000-7000-8000-000000000042"
	marker := map[config.Host]string{
		config.HostClaude: "CLAUDE_PID=4242", config.HostMuse: "MUSE_SESSION_ID=" + id,
		config.HostCodex: "CODEX_THREAD_ID=" + id, config.HostGrok: "GROK_SESSION_ID=" + id,
		config.HostAntigravity: "ANTIGRAVITY_CONVERSATION_ID=" + id,
	}
	has := func(environ []string, entry string) bool {
		for _, e := range environ {
			if e == entry {
				return true
			}
		}
		return false
	}
	for _, tc := range []struct {
		host                            config.Host
		homeOnly, exclusive, ownOptions bool
	}{
		{config.HostClaude, false, false, false},
		{config.HostMuse, true, false, false},
		{config.HostCodex, true, false, false},
		{config.HostGrok, true, true, true},
		{config.HostAntigravity, true, true, true},
	} {
		environ := []string{marker[tc.host], "HOME=/home/x", "XDG_STATE_HOME=/elsewhere", "BRIGADE_PROFILE=other"}
		trusted := config.Trusted(environ)
		if has(trusted, "BRIGADE_PROFILE=other") {
			t.Errorf("%s: an inherited BRIGADE_* is trusted in a session", tc.host)
		}
		if got := !has(trusted, "XDG_STATE_HOME=/elsewhere"); got != tc.homeOnly {
			t.Errorf("%s: XDG_* dropped %v, want %v", tc.host, got, tc.homeOnly)
		}
		if tc.host.Pointed() {
			// Another host's marker beside this one's own. Claude Code's
			// would make it a Claude Code session, so a pointed host's is used.
			other := marker[config.HostMuse]
			if tc.host == config.HostMuse {
				other = marker[config.HostCodex]
			}
			_, err := config.NativeID(tc.host, []string{marker[tc.host], other})
			if got := err != nil; got != tc.exclusive {
				t.Errorf("%s: a competing marker refused %v (%v), want %v", tc.host, got, err, tc.exclusive)
			}
		}
		inherited := "CLAUDE_PLUGIN_OPTION_LABEL=inherited"
		missing := func(string) ([]byte, error) { return nil, os.ErrNotExist }
		out, _ := config.OptionsEnviron([]string{marker[tc.host], inherited}, "/nowhere/options.json", missing)
		if got := !has(out, inherited); got != tc.ownOptions {
			t.Errorf("%s: inherited plugin options dropped %v, want %v", tc.host, got, tc.ownOptions)
		}
	}
}

// TestHostOfAsksInTheTablesOrder: Claude Code first and Grok Build last,
// whatever else the environment carries, and no host in a terminal.
func TestHostOfAsksInTheTablesOrder(t *testing.T) {
	t.Parallel()
	all := []string{"GROK_SESSION_ID=g", "ANTIGRAVITY_CONVERSATION_ID=a", "CODEX_THREAD_ID=c", "MUSE_SESSION_ID=m", "CLAUDE_PID=4242"}
	for i, want := range []config.Host{config.HostGrok, config.HostAntigravity, config.HostCodex, config.HostMuse, config.HostClaude} {
		if got := config.HostOf(all[:i+1]); got != want {
			t.Errorf("with %v: %q, want %q", all[:i+1], got, want)
		}
	}
	if got := config.HostOf([]string{"HOME=/home/x", "CLAUDE_PLUGIN_ROOT=/p"}); got != "" {
		t.Errorf("a terminal is taken for %q", got)
	}
}
