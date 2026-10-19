package watch_test

import (
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/harness/sessionmap"
	"github.com/appshapes/brigade/internal/protocol"
	"github.com/appshapes/brigade/internal/testutil/fakeadapter"
)

func TestGrokMapCannotStartAWatcherThroughASink(t *testing.T) {
	t.Parallel()
	fx := newFixture(t, fixtureOptions{sink: true})
	fx.useFake(fakeadapter.Script{})
	fx.writeMapWith(func(m *sessionmap.ByPID) { m.Harness = "grok" })
	code, _, errOut := runOnce(t, fx.environ(), fx.args(), fx.deps())
	if code != protocol.CodeConfig.Exit() {
		t.Fatalf("exit %d stderr %q", code, errOut)
	}
	log, err := os.ReadFile(fx.logPath())
	if err != nil || !strings.Contains(string(log), "participation is gated") {
		t.Fatalf("guard diagnostic %q %v", log, err)
	}
	if _, err := os.Stat(fx.sink); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gated watcher wrote a sink: %v", err)
	}
}
