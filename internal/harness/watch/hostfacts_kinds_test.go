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

// TestEveryKindOfDeliveryTheTableNamesHasAnImplementation: the same for
// how a message reaches a host's model. A kind with no implementation
// falls to the socket, which a host that has none cannot be reached by.
func TestEveryKindOfDeliveryTheTableNamesHasAnImplementation(t *testing.T) {
	t.Parallel()
	for _, host := range config.Hosts() {
		if kind := host.Delivery(); deliveries[kind] == nil {
			t.Errorf("%s is reached by a %q delivery, and none is registered for that kind", host, kind)
		}
	}
	if _, plain := deliveryOf("a-host-the-table-does-not-hold").(socket); !plain {
		t.Errorf("a host the table does not hold is not given the socket")
	}
}
