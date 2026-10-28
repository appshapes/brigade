//go:build windows

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/windows"
)

// The IMAGE_FILE_MACHINE values IsWow64Process2 reports for the native machine.
const (
	machineAMD64 = 0x8664
	machineARM64 = 0xAA64
)

// nativeArch asks the kernel which machine this is, so an amd64 launcher running under x64 emulation on a
// Windows-on-ARM machine downloads the arm64 binary. The environment is the fallback only where the call is
// missing (it exists from Windows 10 1709, and Claude Code requires 1809).
func nativeArch() (string, error) {
	var process, native uint16
	if err := windows.IsWow64Process2(windows.CurrentProcess(), &process, &native); err == nil {
		switch native {
		case machineAMD64:
			return "amd64", nil
		case machineARM64:
			return "arm64", nil
		}
		return "", errors.New("unsupported CPU architecture (native machine " + hexWord(native) + ")")
	}
	for _, name := range []string{"PROCESSOR_ARCHITEW6432", "PROCESSOR_ARCHITECTURE"} {
		switch strings.ToUpper(envValue(name)) {
		case "ARM64":
			return "arm64", nil
		case "AMD64":
			return "amd64", nil
		}
	}
	return "", errors.New("unsupported CPU architecture")
}

func hexWord(v uint16) string {
	const digits = "0123456789abcdef"
	return "0x" + string([]byte{digits[v>>12&0xf], digits[v>>8&0xf], digits[v>>4&0xf], digits[v&0xf]})
}

// envValue is one variable from os.Environ (os.Getenv is forbidden by the repository's rules), matched
// without regard to case as Windows itself matches.
func envValue(name string) string {
	return lookupEnv(os.Environ(), name)
}

// curlFetch downloads with the curl.exe Windows 10 1803 and later ship in System32, named by its absolute
// path so a curl earlier on PATH is never run: an argv array, no shell, the user's curlrc disabled, and only
// the variables curlEnvironment picks out of environ.
func curlFetch(environ []string) func(url, tmp, proto string) error {
	return func(url, tmp, proto string) error {
		root, childEnv := curlEnvironment(environ)
		curl := filepath.Join(root, "System32", "curl.exe")
		argv := []string{curl, "--disable", "--fail", "--silent", "--show-error", "--location",
			"--proto", proto, "--retry", "3", "--retry-delay", "1", "--retry-connrefused",
			"--connect-timeout", "10", "--max-time", "45", "--output", tmp, url}
		null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		defer null.Close()
		r, w, err := os.Pipe()
		if err != nil {
			return err
		}
		p, err := os.StartProcess(curl, argv, &os.ProcAttr{Env: childEnv, Files: []*os.File{null, null, w}}) //nolint:gosec // G702: System32's curl.exe by absolute path, a fixed argv apart from the URL and the temporary file, no shell
		_ = w.Close()
		if err != nil {
			_ = r.Close()
			return errors.New("cannot run " + curl + ": " + err.Error())
		}
		said, _ := io.ReadAll(io.LimitReader(r, 4096))
		_ = r.Close()
		state, err := p.Wait()
		if err != nil {
			return err
		}
		if state.ExitCode() != 0 {
			msg := strings.TrimSpace(string(said))
			if msg == "" {
				msg = "curl exited " + itoa(state.ExitCode())
			}
			return errors.New(msg)
		}
		return nil
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		b = append([]byte{'-'}, b...)
	}
	return string(b)
}

// runWith runs the binary as the bootstrap's exec does, as nearly as Windows allows: the same argv, the same
// environment, the three process streams main.go hands in, and the child's exit status returned. A console
// Ctrl-C reaches both processes, so the launcher needs no relay.
func runWith(stdin, stdout, stderr *os.File) func(path string, args []string, environ []string) (int, error) {
	return func(path string, args []string, environ []string) (int, error) {
		p, err := os.StartProcess(path, append([]string{path}, args...), &os.ProcAttr{ //nolint:gosec // G702: the verified binary in the cache (or the developer pointer's), argv passed through, no shell
			Env:   environ,
			Files: []*os.File{stdin, stdout, stderr},
		})
		if err != nil {
			return exitConfig, err
		}
		state, err := p.Wait()
		if err != nil {
			return exitConfig, err
		}
		return state.ExitCode(), nil
	}
}

// detach starts the download worker the way the bootstrap backgrounds its subshell: a hidden console of its
// own, its own process group, every stream NUL, and no wait, so the SessionStart hook returns in
// milliseconds. CREATE_NO_WINDOW and not DETACHED_PROCESS: Windows ignores the first beside the second, and a
// process with no console at all gives the curl.exe it starts a fresh, visible window (card 133, 0.41.1).
func detach(path string, args []string, environ []string) error {
	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer null.Close()
	p, err := os.StartProcess(path, append([]string{path}, args...), &os.ProcAttr{ //nolint:gosec // G702: this executable again, with one fixed flag, no shell
		Env:   environ,
		Files: []*os.File{null, null, null},
		Sys: &syscall.SysProcAttr{
			CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_NO_WINDOW,
		},
	})
	if err != nil {
		return err
	}
	return p.Release()
}
