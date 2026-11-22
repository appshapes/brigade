package hook

import (
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// TestEveryKindOfDialectTheTableNamesHasAnImplementation: the host table
// says what a host's hook is handed and may print, and this package
// speaks it (work item 381). A kind the table names and none is
// registered under would be spoken as a plugin's hook, and say nothing.
func TestEveryKindOfDialectTheTableNamesHasAnImplementation(t *testing.T) {
	t.Parallel()
	native := 0
	for _, host := range config.Hosts() {
		kind := host.Dialect()
		if dialects[kind] == nil {
			t.Errorf("%s speaks a %q dialect, and none is registered for that kind", host, kind)
			continue
		}
		if dialectOf(host).native() {
			native++
			if !host.NamedByHook() {
				t.Errorf("%s speaks its own protocol and its hooks do not name it: nothing would say whose hook it is", host)
			}
		}
	}
	if native == 0 {
		t.Errorf("no host of the table speaks a protocol of its own; the native dialect is tested by none")
	}
	if _, plain := dialectOf("a-host-the-table-does-not-hold").(plugin); !plain {
		t.Errorf("a host the table does not hold is not given the plugin's dialect")
	}
}

// TestTheKeyIsTheParentUnlessTheDialectSaysOtherwise: a session is keyed
// by its hook's parent process, and by its thread where the parent is a
// daemon the host's sessions share, which is then kept for liveness.
func TestTheKeyIsTheParentUnlessTheDialectSaysOtherwise(t *testing.T) {
	t.Parallel()
	const thread, parent = "0199c0de-0000-7000-8000-000000000042", 4242
	for _, host := range config.Hosts() {
		if !host.Pointed() {
			// Never asked: Claude Code's session is keyed by CLAUDE_PID.
			continue
		}
		pid, hostProcess := dialectOf(host).key(thread, parent)
		if host.Dialect() == config.DialectThread {
			if pid != codexKey(thread) || hostProcess != parent {
				t.Errorf("%s: key %d with process %d, want the thread's key and the parent kept", host, pid, hostProcess)
			}
			continue
		}
		if pid != parent || hostProcess != 0 {
			t.Errorf("%s: key %d with process %d, want the parent %d and none kept", host, pid, hostProcess, parent)
		}
	}
}
