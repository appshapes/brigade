//go:build darwin || linux

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

// versionedGrokComm reports whether command is the kernel field of
// grok-<version>-<platform>-<arch>, cut to width. The field must still show
// at least three letters of the platform, so a longer version that crowds
// the tag out fails closed. Both architectures share that prefix, which is
// the same acceptance the unversioned names already give.
func versionedGrokComm(command, platform string, width int) bool {
	if len(command) != width || !strings.HasPrefix(command, "grok-") {
		return false
	}
	rest := strings.TrimPrefix(command, "grok-")
	dash := strings.IndexByte(rest, '-')
	if dash <= 0 || dash >= len(rest)-1 {
		return false
	}
	version, tail := rest[:dash], rest[dash+1:]
	if len(tail) < 3 || !strings.HasPrefix(platform, tail) || !dottedVersion(version) {
		return false
	}
	asset := "grok-" + version + "-" + platform
	return len(asset) > len(command) && strings.HasPrefix(asset, command)
}

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
