package watch

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
	"github.com/appshapes/brigade/internal/harness/inbound"
)

// Even a synthetic sink cannot establish Grok-native admission. Keep this
// guard until the installed host contract supplies a defensible boundary.
func TestGrokCannotAcknowledgeAnUnprovenInjection(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sink")
	w := &watcher{rc: runConfig{host: string(config.HostGrok), sink: path}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	in := newInjector(w)
	if err := in.inject(context.Background(), inbound.Item{Content: "synthetic", MessageID: "m"}); !errors.Is(err, errGrokAdmissionUnproven) {
		t.Fatalf("injection %v", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("gated injection wrote a sink: %v", err)
	}
	if got := in.takeAcks(); len(got) != 0 {
		t.Fatalf("acked: %v", got)
	}
}
