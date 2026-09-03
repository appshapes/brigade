package fakeregistry

import "fmt"

// sprintf keeps fmt out of the recorder's import list except here, where
// the Spy has to format a testing.TB Errorf call exactly as the real one
// would.
func sprintf(format string, args ...any) string {
	return fmt.Sprintf(format, args...)
}
