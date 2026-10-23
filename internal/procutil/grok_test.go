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
			for _, name := range []string{"", "sh", "tmux", "grok-agent", "grok-leader", "grok-malicious", "grok-macos", tc.short + "-evil", tc.short[:len(tc.short)-1], tc.binary, "/owned/grok", "Grok", "grok\x00", "grok-1.0.67-evil", "grok-1.0.67-mac", "grok-1"} {
				if grokKernelCommand(tc.goos, name) {
					t.Fatalf("accepted unrelated name %q", name)
				}
			}
			measured := map[string]string{"darwin": "grok-1.0.67-maco", "linux": "grok-1.0.67-lin"}
			// The boundary: a five-character version cuts the field exactly on
			// the platform tag's last byte, so the tag is whole and the field
			// is one byte shorter than grok-<version>-<platform>-<arch>.
			boundary := map[string]string{"darwin": "grok-1.1.0-macos", "linux": "grok-1.1.0-linu"}
			for _, names := range []map[string]string{measured, boundary} {
				if !grokKernelCommand(tc.goos, names[tc.goos]) {
					t.Fatalf("rejected versioned kernel name %q", names[tc.goos])
				}
				for otherOS, name := range names {
					if otherOS != tc.goos && grokKernelCommand(tc.goos, name) {
						t.Fatalf("accepted other platform %q", name)
					}
				}
			}
			// Wherever the cut lands — inside the platform tag, exactly on its
			// last byte, or past it in the architecture — the kernel field of
			// an installer asset is accepted, and only a version long enough
			// to leave fewer than three letters of the tag is refused.
			platform := tc.binary[5:10]
			for _, version := range []string{"1.0.67", "1.1.0", "1.2", "10.20.30"} {
				for _, arch := range grokInstallerArches {
					asset := "grok-" + version + "-" + platform + "-" + arch
					if len(asset) < tc.width {
						t.Fatalf("fixture %q is shorter than the kernel field", asset)
					}
					field, want := asset[:tc.width], len("grok-"+version+"-")+3 <= tc.width
					if grokKernelCommand(tc.goos, field) != want {
						t.Fatalf("kernel field %q of %q: accepted %v, want %v", field, asset, !want, want)
					}
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
