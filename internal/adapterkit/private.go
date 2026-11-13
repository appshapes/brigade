package adapterkit

import "io/fs"

// The privacy facts the harness checks before it trusts a file it reads or a directory it writes under
// (docs/security.md U-10, U-17, U-19; E0-7): on macOS and Linux, a mode that grants nothing to group or
// other and an owner uid equal to the process's own; a team file must not be world-writable; an executable
// has an execute bit. Each fact is one function with a unix implementation (private_unix.go, today's checks,
// unchanged) and a windows one (private_windows.go: Go reports every writable Windows file as 0666 and
// os.Getuid as -1, so the bit checks cannot run there; Rjae's ruling of 2026-10-27, card 133, trusts the
// user's profile directory as Windows sets it up, and an ACL verification is its own card). No caller reads a
// mode bit itself.

// InsecureMode reports whether fi's mode grants group or other any access.
func InsecureMode(fi fs.FileInfo) bool { return insecureMode(fi) }

// ForeignOwner reports whether fi is owned by someone other than the current user, or ownership cannot be
// proven (a FileInfo from a filesystem the platform cannot describe), which the callers refuse.
func ForeignOwner(fi fs.FileInfo) bool { return foreignOwner(fi) }

// IsPrivate reports whether fi is a regular file (or a directory, when the caller allows one) that only this
// user can read: neither InsecureMode nor ForeignOwner.
func IsPrivate(fi fs.FileInfo) bool { return !insecureMode(fi) && !foreignOwner(fi) }

// OwnerReadWriteOnly reports whether fi's mode is exactly 0600: its owner reads and writes it and nobody does
// anything else, the mode WriteAtomic gives every private file and 4.4.10 asks of a --secret-file. It is the
// stricter fact beside InsecureMode, for a caller that checks what was written and not only who may read it.
func OwnerReadWriteOnly(fi fs.FileInfo) bool { return ownerReadWriteOnly(fi) }

// WorldWritable reports whether anyone on the machine could write fi.
func WorldWritable(fi fs.FileInfo) bool { return worldWritable(fi) }

// IsExecutable reports whether fi is a regular file a shell would run.
func IsExecutable(fi fs.FileInfo) bool { return fi.Mode().IsRegular() && executable(fi) }
