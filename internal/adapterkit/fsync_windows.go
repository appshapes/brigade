//go:build windows

package adapterkit

// syncDir is a no-op on Windows: a directory handle opened for reading
// cannot be flushed (FlushFileBuffers wants write access and answers
// ERROR_ACCESS_DENIED), and NTFS journals the rename itself. The file's
// own fsync before the rename stands.
func syncDir(string) error { return nil }
