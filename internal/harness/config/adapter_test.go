package config_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/config"
)

func writeSidecar(t *testing.T, configDir, profile, content string) string {
	t.Helper()
	path, err := config.SidecarPath(configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.WriteAtomic(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeRegistry(t *testing.T, configDir, content string) string {
	t.Helper()
	if err := adapterkit.MkdirPrivate(configDir); err != nil {
		t.Fatal(err)
	}
	path := config.RegistryPath(configDir)
	if err := adapterkit.WriteAtomic(path, []byte(content)); err != nil {
		t.Fatal(err)
	}
	return path
}

func writeProfile(t *testing.T, configDir, profile, adapter string) {
	t.Helper()
	if err := adapterkit.SaveProfile(configDir, profile, &adapterkit.Profile{Version: adapterkit.ProfileVersion, Adapter: adapter}); err != nil {
		t.Fatal(err)
	}
}

func assertAdapter(t *testing.T, got config.Adapter, argv []string, bundled bool, source string) {
	t.Helper()
	if got.Bundled != bundled || got.Source != source || !slices.Equal(got.Argv, argv) {
		t.Fatalf("adapter = %+v; want argv %q bundled %v source %q", got, argv, bundled, source)
	}
	if bundled && len(got.Argv) != 0 {
		t.Fatalf("a bundled adapter carries an argv: %+v", got)
	}
}

func TestResolveAdapterPrecedence(t *testing.T) {
	t.Parallel()
	// Option beats sidecar beats profile member beats bundled: the
	// sources are removed one at a time and the answer moves down.
	configDir := filepath.Join(t.TempDir(), "config")
	const profile = "work"
	writeRegistry(t, configDir, `{"fs": "/opt/registry-fs"}`)
	writeProfile(t, configDir, profile, "fs")
	sidecar := writeSidecar(t, configDir, profile, "/opt/sidecar-adapter\n")
	opts := config.Options{AdapterCommand: "/opt/option-adapter"}

	got, err := config.ResolveAdapter(opts, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/option-adapter"}, false, config.SourceOption)

	opts.AdapterCommand = ""
	got, err = config.ResolveAdapter(opts, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/sidecar-adapter"}, false, config.SourceSidecar)

	if err := os.Remove(sidecar); err != nil {
		t.Fatal(err)
	}
	got, err = config.ResolveAdapter(opts, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/registry-fs"}, false, config.SourceProfile)

	profilePath, err := adapterkit.ProfilePath(configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(profilePath); err != nil {
		t.Fatal(err)
	}
	got, err = config.ResolveAdapter(opts, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, nil, true, config.SourceBundled)
}

// TestResolveAdapterThreeFormsPerSource drives each of the three accepted
// forms (and the implicit bundled name) through each of the three value
// sources.
func TestResolveAdapterThreeFormsPerSource(t *testing.T) {
	t.Parallel()
	forms := []struct {
		name    string
		value   string
		argv    []string
		bundled bool
	}{
		{"absolute path", "/opt/adapter", []string{"/opt/adapter"}, false},
		{"absolute path is cleaned", "/opt//adapter/../adapter-fs/", []string{"/opt/adapter-fs"}, false},
		{"json array", `["/opt/adapter","--root","/srv/store"]`, []string{"/opt/adapter", "--root", "/srv/store"}, false},
		{"json array with whitespace", " [ \"/opt/adapter\" , \"--flag\" ] ", []string{"/opt/adapter", "--flag"}, false},
		{"json array carrying a path with spaces", `["/opt/my apps/adapter","--root","/srv/a b"]`, []string{"/opt/my apps/adapter", "--root", "/srv/a b"}, false},
		{"registered name (string entry)", "fs", []string{"/opt/registry-fs"}, false},
		{"registered name (array entry)", "fs-rooted", []string{"/opt/registry-fs", "--root", "/srv/r"}, false},
		{"the bundled name", "supabase", nil, true},
	}
	sources := []string{config.SourceOption, config.SourceSidecar, config.SourceProfile}
	for _, src := range sources {
		for _, f := range forms {
			t.Run(src+"/"+f.name, func(t *testing.T) {
				t.Parallel()
				configDir := filepath.Join(t.TempDir(), "config")
				const profile = "default"
				writeRegistry(t, configDir, `{"fs": "/opt/registry-fs", "fs-rooted": ["/opt/registry-fs", "--root", "/srv/r"], "supabase": "/opt/never-used"}`)
				var opts config.Options
				switch src {
				case config.SourceOption:
					opts.AdapterCommand = f.value
				case config.SourceSidecar:
					writeSidecar(t, configDir, profile, f.value+"\n")
				case config.SourceProfile:
					if strings.HasPrefix(strings.TrimSpace(f.value), "[") || strings.HasPrefix(f.value, "/") {
						// adapterkit.Profile.Adapter is a name by design; the
						// parser accepts every form there too, proven by
						// writing the raw file.
						writeRawProfile(t, configDir, profile, f.value)
					} else {
						writeProfile(t, configDir, profile, f.value)
					}
				}
				got, err := config.ResolveAdapter(opts, configDir, profile)
				if err != nil {
					t.Fatalf("ResolveAdapter: %v", err)
				}
				assertAdapter(t, got, f.argv, f.bundled, src)
			})
		}
	}
}

// writeRawProfile writes a team.json with an arbitrary adapter member.
func writeRawProfile(t *testing.T, configDir, profile, adapter string) {
	t.Helper()
	path, err := adapterkit.ProfilePath(configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
		t.Fatal(err)
	}
	quoted := strings.ReplaceAll(adapter, `\`, `\\`)
	quoted = strings.ReplaceAll(quoted, `"`, `\"`)
	if err := adapterkit.WriteAtomic(path, []byte(`{"version":1,"adapter":"`+quoted+`"}`)); err != nil {
		t.Fatal(err)
	}
}

func TestResolveAdapterReasons(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		value  string
		reason string
	}{
		{"relative path", "bin/" + evilMarker, config.ReasonAdapterRelative},
		{"dot-relative path", "./" + evilMarker, config.ReasonAdapterRelative},
		{"parent-relative path", "../" + evilMarker, config.ReasonAdapterRelative},
		{"array with a relative executable", `["bin/` + evilMarker + `"]`, config.ReasonAdapterRelative},
		{"array with a bare name", `["supabase"]`, config.ReasonAdapterRelative},
		{"empty array", "[]", config.ReasonAdapterMalformed},
		{"array of a non-string", "[1]", config.ReasonAdapterMalformed},
		{"array with an empty element", `["/opt/a", ""]`, config.ReasonAdapterMalformed},
		{"unterminated array", `["/opt/` + evilMarker, config.ReasonAdapterMalformed},
		{"a json object", `{"cmd":"/opt/` + evilMarker + `"}`, config.ReasonAdapterMalformed},
		{"a shell command", "/opt/adapter --root " + evilMarker, config.ReasonAdapterMalformed},
		{"an absolute path with a space (use the array form)", "/opt/my " + evilMarker + "/adapter", config.ReasonAdapterMalformed},
		{"a tab", "/opt/adapter\t" + evilMarker, config.ReasonAdapterMalformed},
		{"a quoted path", `"/opt/` + evilMarker + `"`, config.ReasonAdapterMalformed},
		{"a name with a dot", "adapter." + evilMarker, config.ReasonAdapterMalformed},
		{"a name with a space", "my " + evilMarker, config.ReasonAdapterMalformed},
		{"a name too long", strings.Repeat("a", 65), config.ReasonAdapterMalformed},
		{"a path spanning lines", "/opt/a\n/opt/" + evilMarker, config.ReasonAdapterMalformed},
		{"only whitespace", "  \n ", config.ReasonAdapterMalformed},
		{"unregistered name, no registry", "fs" + evilMarker, config.ReasonAdapterUnregistered},
	}
	for _, tc := range cases {
		t.Run("option/"+tc.name, func(t *testing.T) {
			t.Parallel()
			configDir := filepath.Join(t.TempDir(), "config")
			_, err := config.ResolveAdapter(config.Options{AdapterCommand: tc.value}, configDir, "default")
			details := assertConfig(t, err, tc.reason)
			if details["source"] != config.SourceOption {
				t.Fatalf("details.source = %q", details["source"])
			}
		})
		t.Run("sidecar/"+tc.name, func(t *testing.T) {
			t.Parallel()
			configDir := filepath.Join(t.TempDir(), "config")
			writeSidecar(t, configDir, "default", tc.value)
			_, err := config.ResolveAdapter(config.Options{}, configDir, "default")
			details := assertConfig(t, err, tc.reason)
			if details["source"] != config.SourceSidecar {
				t.Fatalf("details.source = %q", details["source"])
			}
		})
	}
	t.Run("an unresolvable value never falls through to the bundled adapter", func(t *testing.T) {
		t.Parallel()
		// The sidecar names an unregistered adapter while the profile
		// member would have resolved: the sidecar's failure stands.
		configDir := filepath.Join(t.TempDir(), "config")
		writeProfile(t, configDir, "default", "supabase")
		writeSidecar(t, configDir, "default", "unregistered-"+evilMarker)
		_, err := config.ResolveAdapter(config.Options{}, configDir, "default")
		assertConfig(t, err, config.ReasonAdapterUnregistered)
	})
}

func TestResolveAdapterRegistryProblems(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		registry string // "" = no file
		lookup   string
		reason   string
	}{
		{"no registry file", "", "fs", config.ReasonAdapterUnregistered},
		{"name absent", `{"other": "/opt/other"}`, "fs", config.ReasonAdapterUnregistered},
		{"registry not json", "{not json " + evilMarker, "fs", config.ReasonRegistryMalformed},
		{"registry an array", `["/opt/fs"]`, "fs", config.ReasonRegistryMalformed},
		{"entry a number", `{"fs": 1}`, "fs", config.ReasonRegistryMalformed},
		{"entry an object", `{"fs": {"path": "/opt/fs"}}`, "fs", config.ReasonRegistryMalformed},
		{"entry an array with a number", `{"fs": ["/opt/fs", 1]}`, "fs", config.ReasonRegistryMalformed},
		{"entry a relative path", `{"fs": "bin/` + evilMarker + `"}`, "fs", config.ReasonAdapterRelative},
		{"entry a bare name", `{"fs": "supabase"}`, "fs", config.ReasonAdapterRelative},
		{"entry an empty string", `{"fs": ""}`, "fs", config.ReasonAdapterRelative},
		{"entry a command line", `{"fs": "/opt/fs --root /` + evilMarker + `"}`, "fs", config.ReasonAdapterMalformed},
		{"entry an array with a relative executable", `{"fs": ["bin/` + evilMarker + `"]}`, "fs", config.ReasonAdapterRelative},
		{"entry an empty array", `{"fs": []}`, "fs", config.ReasonAdapterMalformed},
		{"entry an array with an empty element", `{"fs": ["/opt/fs", ""]}`, "fs", config.ReasonRegistryMalformed},
		{"duplicate names", `{"fs": "/opt/a", "fs": "/opt/b"}`, "fs", config.ReasonRegistryMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			configDir := filepath.Join(t.TempDir(), "config")
			if tc.registry != "" {
				writeRegistry(t, configDir, tc.registry)
			}
			_, err := config.ResolveAdapter(config.Options{AdapterCommand: tc.lookup}, configDir, "default")
			assertConfig(t, err, tc.reason)
		})
	}
	t.Run("the bundled name ignores a registry entry of that name", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		writeRegistry(t, configDir, `{"supabase": "/opt/`+evilMarker+`"}`)
		got, err := config.ResolveAdapter(config.Options{AdapterCommand: "supabase"}, configDir, "default")
		if err != nil {
			t.Fatal(err)
		}
		assertAdapter(t, got, nil, true, config.SourceOption)
	})
}

func TestResolveAdapterRefusesInsecureModes(t *testing.T) {
	t.Parallel()
	t.Run("world-readable registry", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		path := writeRegistry(t, configDir, `{"fs": "/opt/fs"}`)
		//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := config.ResolveAdapter(config.Options{AdapterCommand: "fs"}, configDir, "default")
		assertConfig(t, err, "insecure_mode")
		// Positive control: 0600 resolves.
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := config.ResolveAdapter(config.Options{AdapterCommand: "fs"}, configDir, "default"); err != nil {
			t.Fatalf("control: %v", err)
		}
	})
	t.Run("world-readable sidecar", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		path := writeSidecar(t, configDir, "default", "/opt/adapter")
		//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := config.ResolveAdapter(config.Options{}, configDir, "default")
		assertConfig(t, err, "insecure_mode")
	})
	t.Run("a sidecar that is a directory", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		path, err := config.SidecarPath(configDir, "default")
		if err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.MkdirPrivate(path); err != nil {
			t.Fatal(err)
		}
		_, err = config.ResolveAdapter(config.Options{}, configDir, "default")
		assertConfig(t, err, "not_regular")
	})
}

func TestResolveAdapterProfileMemberIsBestEffort(t *testing.T) {
	t.Parallel()
	t.Run("missing profile", func(t *testing.T) {
		t.Parallel()
		got, err := config.ResolveAdapter(config.Options{}, filepath.Join(t.TempDir(), "config"), "default")
		if err != nil {
			t.Fatal(err)
		}
		assertAdapter(t, got, nil, true, config.SourceBundled)
	})
	t.Run("world-readable profile is skipped here (the adapter refuses it later)", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		writeProfile(t, configDir, "default", "fs")
		path, err := adapterkit.ProfilePath(configDir, "default")
		if err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := config.ResolveAdapter(config.Options{}, configDir, "default")
		if err != nil {
			t.Fatal(err)
		}
		assertAdapter(t, got, nil, true, config.SourceBundled)
	})
	t.Run("malformed profile is skipped", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		path, err := adapterkit.ProfilePath(configDir, "default")
		if err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.MkdirPrivate(filepath.Dir(path)); err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.WriteAtomic(path, []byte("{not json")); err != nil {
			t.Fatal(err)
		}
		got, err := config.ResolveAdapter(config.Options{}, configDir, "default")
		if err != nil {
			t.Fatal(err)
		}
		assertAdapter(t, got, nil, true, config.SourceBundled)
	})
	t.Run("a profile member that names an unregistered adapter is an error, not a fallthrough", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		writeProfile(t, configDir, "default", "fs")
		_, err := config.ResolveAdapter(config.Options{}, configDir, "default")
		details := assertConfig(t, err, config.ReasonAdapterUnregistered)
		if details["source"] != config.SourceProfile {
			t.Fatalf("source = %q", details["source"])
		}
	})
	t.Run("an invalid profile name is refused before any file is touched", func(t *testing.T) {
		t.Parallel()
		_, err := config.ResolveAdapter(config.Options{}, filepath.Join(t.TempDir(), "config"), "../"+evilMarker)
		assertConfig(t, err, "invalid_profile_name")
	})
}

func TestAdapterEncodeDecodeRoundTrip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		adapter config.Adapter
		encoded string
	}{
		{"bundled", config.Adapter{Bundled: true, Source: config.SourceOption}, "[]"},
		{"bundled with a stray argv encodes as bundled", config.Adapter{Bundled: true, Argv: []string{"/x"}}, "[]"},
		{"path", config.Adapter{Argv: []string{"/opt/adapter"}}, `["/opt/adapter"]`},
		{"path with args", config.Adapter{Argv: []string{"/opt/adapter", "--root", "/srv/a b"}}, `["/opt/adapter","--root","/srv/a b"]`},
		{"empty argv is bundled", config.Adapter{}, "[]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			enc, err := tc.adapter.Encode()
			if err != nil || enc != tc.encoded {
				t.Fatalf("Encode = %q, %v; want %q", enc, err, tc.encoded)
			}
			back, err := config.DecodeAdapter(enc)
			if err != nil {
				t.Fatalf("DecodeAdapter(%q): %v", enc, err)
			}
			wantBundled := tc.adapter.Bundled || len(tc.adapter.Argv) == 0
			var wantArgv []string
			if !wantBundled {
				wantArgv = tc.adapter.Argv
			}
			assertAdapter(t, back, wantArgv, wantBundled, config.SourceEnv)
		})
	}
}

func TestDecodeAdapterRefusesUnresolvedForms(t *testing.T) {
	t.Parallel()
	cases := []struct {
		value  string
		reason string
	}{
		{"fs", config.ReasonAdapterMalformed},
		{"supabase", config.ReasonAdapterMalformed},
		{"bin/" + evilMarker, config.ReasonAdapterMalformed},
		{"/opt/adapter --root /" + evilMarker, config.ReasonAdapterMalformed},
		{`["bin/` + evilMarker + `"]`, config.ReasonAdapterRelative},
		{`["/opt/a", ""]`, config.ReasonAdapterMalformed},
		{"[1]", config.ReasonAdapterMalformed},
		{"[", config.ReasonAdapterMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.value, func(t *testing.T) {
			t.Parallel()
			_, err := config.DecodeAdapter(tc.value)
			details := assertConfig(t, err, tc.reason)
			if details["source"] != config.SourceEnv {
				t.Fatalf("source = %q", details["source"])
			}
		})
	}
	t.Run("an absolute path decodes", func(t *testing.T) {
		t.Parallel()
		got, err := config.DecodeAdapter(" /opt/adapter ")
		if err != nil {
			t.Fatal(err)
		}
		assertAdapter(t, got, []string{"/opt/adapter"}, false, config.SourceEnv)
	})
}

func TestAdapterFromArgv(t *testing.T) {
	t.Parallel()
	got, err := config.AdapterFromArgv(nil)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, nil, true, config.SourceMap)
	got, err = config.AdapterFromArgv([]string{})
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, nil, true, config.SourceMap)
	argv := []string{"/opt/adapter", "--root", "/x"}
	got, err = config.AdapterFromArgv(argv)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, argv, false, config.SourceMap)
	argv[0] = "mutated"
	if got.Argv[0] != "/opt/adapter" {
		t.Fatal("AdapterFromArgv aliased the caller's slice")
	}
	_, err = config.AdapterFromArgv([]string{"bin/" + evilMarker})
	assertConfig(t, err, config.ReasonAdapterRelative)
	_, err = config.AdapterFromArgv([]string{"/opt/a", ""})
	assertConfig(t, err, config.ReasonAdapterMalformed)
	_, err = config.AdapterFromArgv([]string{""})
	assertConfig(t, err, config.ReasonAdapterMalformed)
}

func TestCheckAdapterName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		ok   bool
	}{
		{"fs", true}, {"supabase", true}, {"my-adapter_2", true}, {strings.Repeat("a", 64), true},
		{"", false}, {strings.Repeat("a", 65), false}, {"a.b", false}, {"a/b", false}, {"a b", false}, {"é", false}, {"a\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if err := config.CheckAdapterName(tc.name); (err == nil) != tc.ok {
				t.Fatalf("CheckAdapterName(%q) = %v, want ok=%v", tc.name, err, tc.ok)
			}
		})
	}
}

func TestSidecarAndRegistryPaths(t *testing.T) {
	t.Parallel()
	got, err := config.SidecarPath("/c", "work")
	if err != nil || got != "/c/teams/work/adapter" {
		t.Fatalf("SidecarPath = %q, %v", got, err)
	}
	if _, err := config.SidecarPath("/c", "../"+evilMarker); err == nil {
		t.Fatal("SidecarPath accepted a traversing profile")
	}
	if got := config.RegistryPath("/c"); got != "/c/adapters.json" {
		t.Fatalf("RegistryPath = %q", got)
	}
}

// TestRegisterAdapterAndWriteSidecar is the P3-3 seam of D36: `profile init
// --adapter` registers a third-party name in adapters.json and writes the
// profile's sidecar, and what it writes is exactly what ResolveAdapter
// reads back.
func TestRegisterAdapterAndWriteSidecar(t *testing.T) {
	t.Parallel()
	configDir := filepath.Join(t.TempDir(), "config")
	const profile = "work"
	if err := config.RegisterAdapter(configDir, "fs", []string{"/opt/adapter-fs", "--root", "/srv/a b"}); err != nil {
		t.Fatalf("RegisterAdapter: %v", err)
	}
	fi, err := os.Stat(config.RegistryPath(configDir))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("registry mode %o, want 0600", fi.Mode().Perm())
	}
	// A second registration keeps the first entry and replaces its own.
	if err := config.RegisterAdapter(configDir, "other", []string{"/opt/other"}); err != nil {
		t.Fatal(err)
	}
	if err := config.RegisterAdapter(configDir, "other", []string{"/opt/other2"}); err != nil {
		t.Fatal(err)
	}
	got, err := config.ResolveAdapter(config.Options{AdapterCommand: "fs"}, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/adapter-fs", "--root", "/srv/a b"}, false, config.SourceOption)
	got, err = config.ResolveAdapter(config.Options{AdapterCommand: "other"}, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/other2"}, false, config.SourceOption)

	// The sidecar: a registered name resolves and is written as one line;
	// the next SessionStart reads it back with Source sidecar.
	a, err := config.WriteSidecar(configDir, profile, "fs")
	if err != nil {
		t.Fatalf("WriteSidecar: %v", err)
	}
	assertAdapter(t, a, []string{"/opt/adapter-fs", "--root", "/srv/a b"}, false, config.SourceSidecar)
	path, err := config.SidecarPath(configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "fs\n" {
		t.Fatalf("sidecar content %q, want %q", raw, "fs\n")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("sidecar mode: %v %v", fi, err)
	}
	got, err = config.ResolveAdapter(config.Options{}, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/adapter-fs", "--root", "/srv/a b"}, false, config.SourceSidecar)
	// A path and an array form are accepted too, and overwrite.
	if _, err := config.WriteSidecar(configDir, profile, ` ["/opt/x","--flag"] `); err != nil {
		t.Fatal(err)
	}
	got, err = config.ResolveAdapter(config.Options{}, configDir, profile)
	if err != nil {
		t.Fatal(err)
	}
	assertAdapter(t, got, []string{"/opt/x", "--flag"}, false, config.SourceSidecar)
}

func TestRegisterAdapterAndWriteSidecarRefusals(t *testing.T) {
	t.Parallel()
	t.Run("register refusals write nothing", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		for _, tc := range []struct {
			name   string
			reg    string
			argv   []string
			reason string
		}{
			{"bundled name", "supabase", []string{"/opt/" + evilMarker}, config.ReasonAdapterMalformed},
			{"name with a slash", "a/" + evilMarker, []string{"/opt/x"}, config.ReasonAdapterMalformed},
			{"empty name", "", []string{"/opt/x"}, config.ReasonAdapterMalformed},
			{"empty argv", "fs", nil, config.ReasonAdapterMalformed},
			{"relative argv", "fs", []string{"bin/" + evilMarker}, config.ReasonAdapterRelative},
			{"empty element", "fs", []string{"/opt/x", ""}, config.ReasonAdapterMalformed},
		} {
			err := config.RegisterAdapter(configDir, tc.reg, tc.argv)
			details := assertConfig(t, err, tc.reason)
			if details["source"] != config.SourceRegistry {
				t.Fatalf("%s: source = %q", tc.name, details["source"])
			}
		}
		if _, err := os.Stat(config.RegistryPath(configDir)); !os.IsNotExist(err) {
			t.Fatalf("a refused registration created adapters.json (stat err %v)", err)
		}
	})
	t.Run("an insecure or malformed registry is never replaced", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		path := writeRegistry(t, configDir, `{"fs": "/opt/fs"}`)
		//nolint:gosec // G302: a group- or world-readable file is the PRECONDITION this test refuses
		if err := os.Chmod(path, 0o644); err != nil {
			t.Fatal(err)
		}
		assertConfig(t, config.RegisterAdapter(configDir, "other", []string{"/opt/other"}), "insecure_mode")
		if err := os.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := adapterkit.WriteAtomic(path, []byte("{not json")); err != nil {
			t.Fatal(err)
		}
		assertConfig(t, config.RegisterAdapter(configDir, "other", []string{"/opt/other"}), config.ReasonRegistryMalformed)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(raw) != "{not json" {
			t.Fatalf("the malformed registry was replaced: %q", raw)
		}
	})
	t.Run("sidecar refusals write nothing", func(t *testing.T) {
		t.Parallel()
		configDir := filepath.Join(t.TempDir(), "config")
		for _, tc := range []struct {
			value  string
			reason string
		}{
			{"unregistered-" + evilMarker, config.ReasonAdapterUnregistered},
			{"bin/" + evilMarker, config.ReasonAdapterRelative},
			{"/opt/adapter --root " + evilMarker, config.ReasonAdapterMalformed},
			{"", config.ReasonAdapterMalformed},
		} {
			_, err := config.WriteSidecar(configDir, "default", tc.value)
			details := assertConfig(t, err, tc.reason)
			if details["source"] != config.SourceSidecar {
				t.Fatalf("%q: source = %q", tc.value, details["source"])
			}
		}
		if _, err := config.WriteSidecar(configDir, "../"+evilMarker, "/opt/adapter"); err == nil {
			t.Fatal("a traversing profile name was accepted")
		}
		if _, err := os.Stat(configDir); !os.IsNotExist(err) {
			t.Fatalf("a refused sidecar created the config dir (stat err %v)", err)
		}
		// Positive control: the bundled name needs no registry and writes.
		a, err := config.WriteSidecar(configDir, "default", "supabase")
		if err != nil {
			t.Fatal(err)
		}
		assertAdapter(t, a, nil, true, config.SourceSidecar)
	})
}
