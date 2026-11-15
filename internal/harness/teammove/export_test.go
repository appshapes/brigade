package teammove

import "time"

// SetNewBackendWait shortens the bound of the question a follow asks of
// the backend a team moved to, for a test that measures it, and returns
// what puts it back. A test that calls it does not run beside others.
func SetNewBackendWait(d time.Duration) (restore func()) {
	before := newBackendWait
	newBackendWait = d
	return func() { newBackendWait = before }
}
