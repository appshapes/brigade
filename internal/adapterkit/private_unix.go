//go:build darwin || linux

package adapterkit

import (
	"io/fs"
	"os"
	"syscall"
)

func insecureMode(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o077 != 0 }

func foreignOwner(fi fs.FileInfo) bool {
	uid, ok := fileOwnerUID(fi)
	return !ok || uid != os.Getuid()
}

func worldWritable(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o002 != 0 }

func executable(fi fs.FileInfo) bool { return fi.Mode().Perm()&0o111 != 0 }

// fileOwnerUID returns the owner uid recorded in fi. ok is false when fi
// carries no unix stat, which the caller treats as "cannot prove
// ownership" and refuses.
func fileOwnerUID(fi fs.FileInfo) (uid int, ok bool) {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0, false
	}
	return int(st.Uid), true
}
