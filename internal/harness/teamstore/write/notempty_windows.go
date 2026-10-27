//go:build windows

package write

import (
	"errors"

	"golang.org/x/sys/windows"
)

// dirNotEmpty is ERROR_DIR_NOT_EMPTY, what a rename onto a non-empty
// directory answers on Windows where unix answers ENOTEMPTY.
func dirNotEmpty(err error) bool { return errors.Is(err, windows.ERROR_DIR_NOT_EMPTY) }
