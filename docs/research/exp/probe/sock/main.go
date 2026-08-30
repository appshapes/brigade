package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Tries unix socket paths of increasing length under /tmp to find the sun_path limit on this OS.
func main() {
	base, _ := os.MkdirTemp("/tmp", "sk")
	defer os.RemoveAll(base)
	for _, want := range []int{90, 100, 103, 104, 105, 108, 110, 120} {
		dir := base
		// pad the directory so that the final path has exactly `want` bytes
		name := "s.sock"
		pad := want - len(filepath.Join(dir, "x", name))
		if pad < 1 {
			pad = 1
		}
		sub := filepath.Join(dir, "x"+strings.Repeat("y", pad-1))
		_ = os.MkdirAll(sub, 0o700)
		p := filepath.Join(sub, name)
		l, err := net.Listen("unix", p)
		if err != nil {
			fmt.Printf("len=%d listen: ERR %v\n", len(p), err)
			continue
		}
		fmt.Printf("len=%d listen: ok\n", len(p))
		l.Close()
	}
	// what does t.TempDir look like on this machine? (approximate: os.TempDir)
	fmt.Println("os.TempDir():", os.TempDir(), "len", len(os.TempDir()))
}
