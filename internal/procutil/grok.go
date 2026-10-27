package procutil

import (
	"runtime"
	"strings"
)

// IsGrokProcess checks the bounded kernel-name witness shared by Grok hooks and
// receiver liveness. Callers must still bind the actual PID/start token and,
// for monitor children, ancestry; a process name is not authorization by itself.
func IsGrokProcess(info Info) bool {
	return info.Exists && !info.Foreign && !info.Zombie && info.StartToken != "" && grokKernelCommand(runtime.GOOS, info.Command)
}

func grokKernelCommand(goos, command string) bool {
	// https://x.ai/cli/install.sh links grok to the downloaded binary.
	// Darwin P_comm holds 16 bytes; Linux comm holds 15. These are exact
	// platform names, never acceptance of an arbitrary grok-* prefix.
	// Accept both installed architectures on each OS: the Go process can be
	// translated independently of the TUI. Coordinator measured grok-macos-aarch
	// on installed macOS arm64 1.0.62. Grok 1.0.67 names the binary
	// grok-1.0.67-macos-aarch64, whose P_comm is grok-1.0.67-maco (measured
	// 2026-10-23). The Linux truncation of that asset name is not a native
	// measurement.
	switch goos {
	case "darwin":
		return command == "grok" || command == "grok-macos-aarch" || command == "grok-macos-x86_6" || versionedGrokComm(command, "macos", 16)
	case "linux":
		return command == "grok" || command == "grok-linux-aarc" || command == "grok-linux-x86_" || versionedGrokComm(command, "linux", 15)
	default:
		return false
	}
}

// versionedGrokComm reports whether command is the first width bytes of
// grok-<version>-<platform>-<arch> for either installed architecture. One
// comparison covers both ends of the cut: wherever the kernel field ends —
// inside the platform tag, exactly on its last byte, or past it in the
// architecture — the whole name must still be that asset's prefix. The field
// must also show at least three letters of the platform, so a longer version
// that crowds the tag out fails closed. Both architectures are accepted, which
// is the same acceptance the unversioned names already give.
func versionedGrokComm(command, platform string, width int) bool {
	if len(command) != width || !strings.HasPrefix(command, "grok-") {
		return false
	}
	rest := strings.TrimPrefix(command, "grok-")
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 || !dottedVersion(rest[:dash]) {
		return false
	}
	head := "grok-" + rest[:dash] + "-"
	if width < len(head)+3 {
		return false
	}
	for _, arch := range grokInstallerArches {
		if strings.HasPrefix(head+platform+"-"+arch, command) {
			return true
		}
	}
	return false
}

// The architectures https://x.ai/cli/install.sh downloads.
var grokInstallerArches = [...]string{"aarch64", "x86_64"}

func dottedVersion(version string) bool {
	if !strings.Contains(version, ".") || strings.HasPrefix(version, ".") || strings.HasSuffix(version, ".") || strings.Contains(version, "..") {
		return false
	}
	for _, r := range version {
		if r != '.' && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}
