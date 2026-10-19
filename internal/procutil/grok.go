//go:build darwin || linux

package procutil

import "runtime"

// IsGrokProcess checks the bounded kernel-name witness shared by Grok hooks and
// receiver liveness. Callers must still bind the actual PID/start token and,
// for monitor children, ancestry; a process name is not authorization by itself.
func IsGrokProcess(info Info) bool {
	return info.Exists && !info.Foreign && !info.Zombie && info.StartToken != "" && grokKernelCommand(runtime.GOOS, info.Command)
}

func grokKernelCommand(goos, command string) bool {
	// https://x.ai/cli/install.sh links grok to grok-{macos,linux}-{aarch64,x86_64}.
	// Darwin P_comm holds 16 bytes; Linux comm holds 15. These are exact
	// platform/architecture names, never acceptance of an arbitrary grok-* prefix.
	// Accept both installed architectures on each OS: the Go process can be
	// translated independently of the TUI. Coordinator measured grok-macos-aarch
	// on installed macOS arm64 1.0.62;
	// the other entries follow the installer convention, not native acceptance.
	switch goos {
	case "darwin":
		return command == "grok" || command == "grok-macos-aarch" || command == "grok-macos-x86_6"
	case "linux":
		return command == "grok" || command == "grok-linux-aarc" || command == "grok-linux-x86_"
	default:
		return false
	}
}
