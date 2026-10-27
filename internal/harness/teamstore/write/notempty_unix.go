//go:build darwin || linux

package write

// dirNotEmpty is the platform's other spelling of ENOTEMPTY: none on unix.
func dirNotEmpty(error) bool { return false }
