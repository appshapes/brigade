//go:build !windows

package watch

import (
	"os"

	"github.com/appshapes/brigade/internal/harness/config"
)

// findHostLog is the path of the session's log under root in the host's
// history, "" when there is none or the host keeps no log of this form.
// The tests of the two histories that keep a log ask by host, as they did
// when one function found both (work item 381); the watcher asks the
// history itself.
func findHostLog(root *os.File, host config.Host, id string) string {
	if h, ok := histories[host.History()].(interface {
		findLog(root *os.File, id string) string
	}); ok {
		return h.findLog(root, id)
	}
	return ""
}
