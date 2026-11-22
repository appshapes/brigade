//go:build !windows

package commands

import (
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/harness/account"
	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/teamstore"
)

// labelHostEnvs is the environment of a session of each harness, and of a
// terminal, on a machine whose home directory is home.
func labelHostEnvs(home string) map[string][]string {
	envs := map[string][]string{
		string(config.HostClaude): {config.ClaudePIDVar + "=4242"},
		string(config.HostMuse):   {config.MuseSessionIDVar + "=muse-1"},
		string(config.HostCodex):  {config.CodexThreadIDVar + "=codex-1"},
		string(config.HostGrok):   {config.GrokSessionIDVar + "=grok-1"},
		"a terminal":              {},
	}
	for name, env := range envs {
		envs[name] = append(env, "PATH=/usr/bin:/bin", "HOME="+home)
	}
	return envs
}

// TestTeamJoinSendsTheAccountLabelOnEveryHost is ADR 0014 at `team join`
// (work item 379): with no --label, a join from a session of each of the
// four harnesses, and from a terminal, sends the email of the Claude
// account on the machine and records the file it was read from. This is
// the one of the three senders that had no host rule of its own: the
// session-start hook and the watcher now send what it sends
// (TestSessionStartRegistersTheAccountLabelOnEveryHost in the hook,
// TestTheWatcherSendsTheAccountLabelOnEveryHost in the watcher).
func TestTeamJoinSendsTheAccountLabelOnEveryHost(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	writeAccountFile(t, f.dirs.Home)
	for name, env := range labelHostEnvs(f.dirs.Home) {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			got := f.inv(env, "").defaultLabel()
			want := labelChoice{text: accountEmail, source: teamstore.LabelFromAccount, accountFile: filepath.Join(f.dirs.Home, account.FileName)}
			if got != want {
				t.Fatalf("the join's default label = %+v, want %+v", got, want)
			}
		})
	}
}

// TestABoardChangeIsByTheAccountLabelOnEveryHost: a change of the Brigade
// Board is by the member's label as the session registers it, which is
// the account's email where the membership on this machine carries none,
// on each of the four harnesses. Until work item 379 a Muse Code, Codex
// CLI or Grok Build session's change named no member.
func TestABoardChangeIsByTheAccountLabelOnEveryHost(t *testing.T) {
	t.Parallel()
	for _, host := range []config.Host{config.HostClaude, config.HostMuse, config.HostCodex, config.HostGrok} {
		t.Run(string(host), func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			writeAccountFile(t, f.dirs.Home)
			m := f.byPID()
			m.Harness = string(host)
			for option, want := range map[string]string{"": accountEmail, config.LabelNone: "", "Rae on the laptop": "Rae on the laptop"} {
				m.LabelOption = option
				if who := f.inv(labelHostEnvs(f.dirs.Home)[string(host)], "").workItemAuthor(m); who.Member != want {
					t.Errorf("label option %q: a change is by %q, want %q", option, who.Member, want)
				}
			}
		})
	}
}
