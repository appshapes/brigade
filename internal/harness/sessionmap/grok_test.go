package sessionmap

import (
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/procutil"
)

func TestGrokAlivePackagedKernelNameKeepsExactPIDAndStart(t *testing.T) {
	t.Parallel()
	m := &ByPID{ClaudePID: 4242, Harness: "grok", Grok: &GrokReceiver{Cwd: "/synthetic", ProcessStart: "owned-start", Incarnation: strings.Repeat("a", 32)}}
	command := "grok-linux-x86_"
	if runtime.GOOS == "darwin" {
		command = "grok-macos-aarch"
	}
	info := procutil.Info{Exists: true, Command: command, StartToken: "owned-start"}
	lookup := func(pid int) (procutil.Info, error) {
		if pid != 4242 {
			t.Fatalf("looked up %d instead of the actual TUI", pid)
		}
		return info, nil
	}
	if !m.GrokAlive(lookup) {
		t.Fatal("packaged kernel name rejected")
	}
	info.StartToken = "reused-pid"
	if m.GrokAlive(lookup) {
		t.Fatal("PID reuse accepted")
	}
	info.StartToken = "owned-start"
	info.Command = command + "-other"
	if m.GrokAlive(lookup) {
		t.Fatal("arbitrary prefix accepted")
	}
}

func TestGrokLivenessLookupErrorIsUnknown(t *testing.T) {
	t.Parallel()
	m := &ByPID{ClaudePID: 4242, Harness: "grok", Grok: &GrokReceiver{Cwd: "/synthetic", ProcessStart: "owned-start", Incarnation: strings.Repeat("a", 32)}}
	lookup := func(int) (procutil.Info, error) { return procutil.Info{}, errors.New("lookup failed") }
	life, err := m.GrokLiveness(lookup)
	if life != GrokUnknown || err == nil || m.GrokAlive(lookup) {
		t.Fatalf("life %v err %v", life, err)
	}
	lookup = func(int) (procutil.Info, error) { return procutil.Info{}, nil }
	life, err = m.GrokLiveness(lookup)
	if life != GrokDead || err != nil || m.GrokAlive(lookup) {
		t.Fatalf("missing process life %v err %v", life, err)
	}
}
