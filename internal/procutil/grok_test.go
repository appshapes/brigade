//go:build darwin || linux

package procutil

import "testing"

func TestGrokKernelCommandExactPlatformNames(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		goos, arch, binary, short string
		width                     int
	}{
		{"darwin", "arm64", "grok-macos-aarch64", "grok-macos-aarch", 16},
		{"darwin", "amd64", "grok-macos-x86_64", "grok-macos-x86_6", 16},
		{"linux", "arm64", "grok-linux-aarch64", "grok-linux-aarc", 15},
		{"linux", "amd64", "grok-linux-x86_64", "grok-linux-x86_", 15},
	} {
		t.Run(tc.goos+"/"+tc.arch, func(t *testing.T) {
			t.Parallel()
			if tc.binary[:tc.width] != tc.short {
				t.Fatal("fixture does not match the kernel field width")
			}
			for _, name := range []string{"grok", tc.short} {
				if !grokKernelCommand(tc.goos, name) {
					t.Fatalf("rejected exact kernel name %q", name)
				}
			}
			for _, name := range []string{"", "sh", "tmux", "grok-agent", "grok-leader", "grok-malicious", "grok-macos", tc.short + "-evil", tc.short[:len(tc.short)-1], tc.binary, "/owned/grok", "Grok", "grok\x00"} {
				if grokKernelCommand(tc.goos, name) {
					t.Fatalf("accepted unrelated name %q", name)
				}
			}
			for _, other := range []string{"grok-macos-aarch", "grok-macos-x86_6", "grok-linux-aarc", "grok-linux-x86_"} {
				if (tc.goos == "darwin") != (other[:10] == "grok-macos") && grokKernelCommand(tc.goos, other) {
					t.Fatalf("accepted other platform %q", other)
				}
			}
		})
	}
	if grokKernelCommand("windows", "grok") {
		t.Fatal("accepted an unsupported platform")
	}
}

func TestIsGrokProcessRequiresLiveOwnedStartIdentity(t *testing.T) {
	t.Parallel()
	live := Info{Exists: true, Command: "grok", StartToken: "owned-start"}
	if !IsGrokProcess(live) {
		t.Fatal("live Grok refused")
	}
	for _, mutate := range []func(*Info){
		func(i *Info) { i.Exists = false }, func(i *Info) { i.Zombie = true },
		func(i *Info) { i.Foreign = true }, func(i *Info) { i.StartToken = "" },
		func(i *Info) { i.Command = "grok-other" },
	} {
		i := live
		mutate(&i)
		if IsGrokProcess(i) {
			t.Fatalf("unverified process accepted: %+v", i)
		}
	}
}
