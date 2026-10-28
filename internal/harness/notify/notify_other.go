//go:build !windows

package notify

import (
	"context"
	"errors"
)

// RunInProcess is Windows' alone (notify_windows.go): on every other
// platform a descriptor is an error, since Sound and Banner never answer
// one here.
func RunInProcess(_ context.Context, argv []string) error {
	what := "a Windows descriptor"
	if len(argv) > 0 {
		what = argv[0]
	}
	return errors.New("notify: " + what + " runs in process on Windows only")
}

// Close is Windows' alone: nothing to release here.
func Close() {}
