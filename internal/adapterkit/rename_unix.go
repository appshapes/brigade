//go:build darwin || linux

package adapterkit

import "os"

// renameInto is rename(2): atomic, and a reader holding the old file open
// keeps reading the old inode.
func renameInto(tmp, path string) error { return os.Rename(tmp, path) }
