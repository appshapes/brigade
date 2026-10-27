//go:build darwin || linux

package adapterkit

import (
	"fmt"
	"os"
)

// syncDir fsyncs the directory so a rename into it is durable (5.1).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("adapterkit: atomic write open dir: %w", err)
	}
	serr := d.Sync()
	cerr := d.Close()
	if serr != nil {
		return fmt.Errorf("adapterkit: atomic write fsync dir: %w", serr)
	}
	if cerr != nil {
		return fmt.Errorf("adapterkit: atomic write close dir: %w", cerr)
	}
	return nil
}
