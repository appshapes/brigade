package watch

import (
	"testing"

	"github.com/appshapes/brigade/internal/harness/config"
)

// TestEveryKindOfHistoryTheTableNamesHasAReader: the host table says how a
// host keeps its own record of a session, and this package reads it (work
// item 381). A kind the table names for a host and no reader is registered
// under would give that host's sessions no name, model or context size,
// and say nothing.
func TestEveryKindOfHistoryTheTableNamesHasAReader(t *testing.T) {
	t.Parallel()
	for _, host := range config.Hosts() {
		kind := host.History()
		if kind == config.HistoryNone {
			continue
		}
		if histories[kind] == nil {
			t.Errorf("%s keeps a %q history, and no reader is registered for that kind", host, kind)
		}
	}
}
