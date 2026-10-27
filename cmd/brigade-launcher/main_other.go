//go:build !windows

package main

import "os"

// main on any other platform refuses: the launcher exists so that Windows can run the plugin's hooks; macOS
// and Linux run the POSIX bootstrap. The package still builds everywhere so launcher.go's tests run on every
// host.
func main() {
	_, _ = os.Stderr.WriteString("brigade-launcher: Windows only; macOS and Linux run plugin/bin/brigade\n")
	os.Exit(exitConfig)
}
