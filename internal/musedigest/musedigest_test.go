package musedigest_test

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/appshapes/brigade/internal/musedigest"
)

// The smallest package Muse Code installs: a manifest and the one skill it names. Muse 1.4.14 reported every
// digest in this file for exactly these bytes (`muse plugins install <dir> --scope user --json`, field
// `package_sha256`, in a throwaway XDG root, 2026-11-09), so a case here is a measurement, not a derivation.
const (
	probeManifest = `{"schemaVersion":1,"name":"x","version":"1.0.0","description":"d","compat":{"manifestDir":".muse-plugin"},"capabilities":{"skills":[{"id":"a","path":"skills/a/SKILL.md"}]}}`
	probeSkill    = "---\nname: a\ndescription: a skill\n---\nbody\n"
	probeDigest   = "sha256:62201f5b491e865c4b8a5a37448b17258b2b7aa118fa022e1e8880aa0ff983cd"
)

// probe is the smallest package plus the files of one case. A value ending in "/" is a directory.
func probe(extra map[string]string) fstest.MapFS {
	fsys := fstest.MapFS{
		".muse-plugin/plugin.json": {Data: []byte(probeManifest), Mode: 0o644},
		"skills/a/SKILL.md":        {Data: []byte(probeSkill), Mode: 0o644},
	}
	for name, body := range extra {
		if dir, ok := strings.CutSuffix(name, "/"); ok {
			fsys[dir] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
			continue
		}
		fsys[name] = &fstest.MapFile{Data: []byte(body), Mode: 0o644}
	}
	return fsys
}

func TestSumIsWhatMuseComputes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		extra map[string]string
		want  string
	}{
		{"the smallest package", nil, probeDigest},

		// A path is bound to its own bytes, and the two are delimited.
		{"two files", map[string]string{"b.txt": "1", "c.txt": "2"}, "sha256:a84995ca0ce366c503613e54090f24058452d759e0c5f00c154c40efa1e4bd14"},
		{"the same two files with their bytes swapped", map[string]string{"b.txt": "2", "c.txt": "1"}, "sha256:30fa4f8911940086928100951d43ddb5b2c5a6718d387a31c5aa3c875f959f69"},
		{"a name and its bytes do not run together (ab, c)", map[string]string{"ab": "c"}, "sha256:6a797defaf25faae24d07611a59b806d0489a6560b4b6c171f4cf71544bbd358"},
		{"a name and its bytes do not run together (a, bc)", map[string]string{"a": "bc"}, "sha256:43abf13ad22bc3f4368a222f6bca558cd0d000a0848e714ccca8af9184ddf93b"},
		{"an empty file", map[string]string{"e.txt": ""}, "sha256:46a24be6e4935a66d0bb17b6f67ec2083751d2c2c59bb44a81ce5e5966425551"},
		{"a file three directories down", map[string]string{"d/e/f.txt": "1"}, "sha256:1f73944943dcbacdef0e497c3b0c4e653f478619be0c8490bed6cd5edfeedc2b"},

		// Bytes are taken as they are.
		{"CRLF line endings", map[string]string{"t.txt": "a\r\nb\r\n"}, "sha256:31c32fd94237bfe4b34a2212911d274516b59f4b7c6f29ef3269fe19f884cfe6"},
		{"LF line endings", map[string]string{"t.txt": "a\nb\n"}, "sha256:92bb5685e5d0b23c203838cbcd5176005d2344b8d4d66912cde3e26213ebbd21"},
		{"bytes that are not text, 0xFF among them", map[string]string{"t.bin": "\x00\x01\xff"}, "sha256:48672cf72c3c100c2a40701e4af35f79126d3c2c5912d07e898d4b210aa79522"},

		// The order is the byte order of the whole path, not a directory walk's: "a b" < "a.b" < "a/b" < "a0",
		// and "skills/a-b" < "skills/a.txt" < "skills/a/SKILL.md".
		{"byte order of the whole path", map[string]string{
			"a/b": "1", "a.b": "2", "a0": "3", "a b": "4", "B": "5", "skills/a/z/q": "6", "skills/a.txt": "7", "skills/a-b": "8",
		}, "sha256:f21c0b8f607bacadad5e87e4de4a00691ec4524ca426efcdbb20782f288f7eb0"},

		// Dot files count; version-control DIRECTORIES do not, at any depth; an empty directory is nothing.
		{"a .DS_Store counts", map[string]string{".DS_Store": "x"}, "sha256:179da59d2f0659d2fb1440d5b47e06e2e4eecd06d559a229bf67953de6e3e3dc"},
		{"a dot file counts", map[string]string{".hidden": "x"}, "sha256:a7b3ced0a0624b7c0f117e10fceb5dfeb856f1da71b91d94aa5a5ff3261c839d"},
		{"a FILE named .git counts", map[string]string{".git": "x"}, "sha256:266983806b893e40d5aa81ea7f1adac21231445ec28d44eab50ae16b2f453f0f"},
		{"FILES named .hg and .svn count", map[string]string{".hg": "1", ".svn": "2"}, "sha256:471dbeb436036e6967a91d2c7aad7dba12f9551c63cdaccc1bda384871d1b55d"},
		{"a .git directory is left out", map[string]string{".git/HEAD": "hi\n"}, probeDigest},
		{"a .git directory further down is left out", map[string]string{"skills/.git/HEAD": "x"}, probeDigest},
		{".hg and .svn directories are left out at any depth", map[string]string{
			".hg/x": "1", "d/.svn/y": "2", "d/.hg/z": "3", "d/keep": "4",
		}, "sha256:20340a17c7ca86a4aeb6e376f92b0b80e0de4e4359c07f6b8fd9c52c7fba13ff"},
		{"empty directories change nothing", map[string]string{"zz/": "", "zz/yy/": ""}, probeDigest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := musedigest.Sum(probe(tc.extra))
			if err != nil {
				t.Fatalf("Sum: %v", err)
			}
			if got != tc.want {
				t.Fatalf("Sum = %s, and Muse reported %s", got, tc.want)
			}
		})
	}
}

// Muse reported the smallest package's digest whatever the modes of its files and directories were (0755, 0600,
// a 0700 directory) and whatever their times.
func TestSumIgnoresFileModes(t *testing.T) {
	t.Parallel()
	fsys := probe(nil)
	fsys["skills/a/SKILL.md"].Mode = 0o755
	fsys[".muse-plugin/plugin.json"].Mode = 0o600
	fsys["skills"] = &fstest.MapFile{Mode: fs.ModeDir | 0o700}
	got, err := musedigest.Sum(fsys)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if got != probeDigest {
		t.Fatalf("Sum = %s, want the digest of the same files at any mode, %s", got, probeDigest)
	}
}

// The done criterion of card 114: the 0.31.0 package (muse/brigade/ at f5e2c3e, 15 files), for which Muse
// 1.4.10 reported this digest on 2026-10-17, from a local-path install and from a git marketplace clone alike.
// The fixture is `git archive f5e2c3e:muse/brigade | gzip`, an archive so that no check of this repository
// takes the tree for a second Muse package.
func TestSumReproducesTheDigestOfRelease0310(t *testing.T) {
	t.Parallel()
	const want = "sha256:092dabcd7481280b89f0a1f6990a1dfb5b219ccef454707a5526670840285420"
	fsys := untar(t, filepath.Join("testdata", "brigade-0.31.0.tar.gz"))
	files := 0
	for _, f := range fsys {
		if f.Mode.IsRegular() {
			files++
		}
	}
	if files != 15 {
		t.Fatalf("the fixture holds %d files, want the 15 of the 0.31.0 package", files)
	}
	got, err := musedigest.Sum(fsys)
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if got != want {
		t.Fatalf("Sum = %s, and Muse 1.4.10 reported %s for the 0.31.0 package", got, want)
	}
}

// untar reads a gzipped tar into memory, regular files and directories only.
func untar(t *testing.T, archive string) fstest.MapFS {
	t.Helper()
	f, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{}
	for tr := tar.NewReader(zr); ; {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return fsys
		}
		if err != nil {
			t.Fatal(err)
		}
		name := strings.TrimSuffix(h.Name, "/")
		switch h.Typeflag {
		case tar.TypeDir:
			fsys[name] = &fstest.MapFile{Mode: fs.ModeDir | 0o755}
		case tar.TypeReg:
			body, err := io.ReadAll(tr)
			if err != nil {
				t.Fatal(err)
			}
			fsys[name] = &fstest.MapFile{Data: body, Mode: 0o644}
		case tar.TypeXGlobalHeader: // git archive's comment carrying the commit id
		default:
			t.Fatalf("%s: unexpected entry %q of type %q", archive, h.Name, h.Typeflag)
		}
	}
}

func TestSumRefusesWhatMuseRefuses(t *testing.T) {
	t.Parallel()

	t.Run("a symlink", func(t *testing.T) {
		t.Parallel()
		fsys := probe(nil)
		fsys["link.md"] = &fstest.MapFile{Data: []byte("skills/a/SKILL.md"), Mode: fs.ModeSymlink | 0o777}
		_, err := musedigest.Sum(fsys)
		if !errors.Is(err, musedigest.ErrNotPackage) || !strings.Contains(err.Error(), "link.md") {
			t.Fatalf("Sum = %v, want ErrNotPackage naming link.md", err)
		}
	})

	t.Run("a tree with no file", func(t *testing.T) {
		t.Parallel()
		_, err := musedigest.Sum(fstest.MapFS{"empty": &fstest.MapFile{Mode: fs.ModeDir | 0o755}})
		if !errors.Is(err, musedigest.ErrNotPackage) {
			t.Fatalf("Sum = %v, want ErrNotPackage", err)
		}
	})
}

// writeTree writes files under root, creating their directories.
func writeTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSumDir(t *testing.T) {
	t.Parallel()

	t.Run("a directory on disk hashes as the same files in memory", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{
			".muse-plugin/plugin.json": probeManifest, "skills/a/SKILL.md": probeSkill, ".git/HEAD": "ref: refs/heads/master\n",
		})
		if err := os.MkdirAll(filepath.Join(dir, "zz", "yy"), 0o700); err != nil {
			t.Fatal(err)
		}
		got, err := musedigest.SumDir(dir)
		if err != nil {
			t.Fatalf("SumDir: %v", err)
		}
		if got != probeDigest {
			t.Fatalf("SumDir = %s, want %s", got, probeDigest)
		}
	})

	t.Run("a symlink on disk is refused", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{".muse-plugin/plugin.json": probeManifest, "skills/a/SKILL.md": probeSkill})
		if err := os.Symlink(filepath.Join("skills", "a", "SKILL.md"), filepath.Join(dir, "link.md")); err != nil {
			t.Skipf("this machine does not create symlinks: %v", err)
		}
		_, err := musedigest.SumDir(dir)
		if !errors.Is(err, musedigest.ErrNotPackage) || !strings.Contains(err.Error(), "link.md") {
			t.Fatalf("SumDir = %v, want ErrNotPackage naming link.md", err)
		}
	})

	t.Run("a file is not a package", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		writeTree(t, dir, map[string]string{"plain": "x"})
		if _, err := musedigest.SumDir(filepath.Join(dir, "plain")); !errors.Is(err, musedigest.ErrNotPackage) {
			t.Fatalf("SumDir = %v, want ErrNotPackage", err)
		}
		if _, err := musedigest.SumDir(filepath.Join(dir, "absent")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("SumDir = %v, want fs.ErrNotExist", err)
		}
	})
}

// catalogJSON is marketplace.json as the repository writes it: one entry, "version" and "digest" each on a line
// of its own.
func catalogJSON(version, digest string) string {
	return `{
  "schemaVersion": 1,
  "source": "local",
  "plugins": [
    {
      "name": "brigade",
      "version": "` + version + `",
      "install": {
        "transport": "local-path",
        "source": "muse/brigade"
      },
      "integrity": {
        "digest": "` + digest + `"
      },
      "availability": {
        "status": "available"
      }
    }
  ]
}
`
}

const (
	fixtureVersion = "1.2.3"
	digestZero     = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
)

// catalogRoot lays out what the catalog commands read under a fresh root: the smallest package at muse/brigade,
// its VERSION and a catalog at 0.0.0 with a digest of zeros, so that Write has both lines to change. The
// package's digest is returned: it is not probeDigest, because bin/VERSION is one more file.
func catalogRoot(t *testing.T, overrides map[string]string) (root, digest string) {
	t.Helper()
	root = t.TempDir()
	files := map[string]string{
		"muse/brigade/.muse-plugin/plugin.json": probeManifest,
		"muse/brigade/skills/a/SKILL.md":        probeSkill,
		"muse/brigade/bin/VERSION":              fixtureVersion + "\n",
		"marketplace.json":                      catalogJSON("0.0.0", digestZero),
	}
	maps.Copy(files, overrides)
	writeTree(t, root, files)
	digest, err := musedigest.SumDir(filepath.Join(root, "muse", "brigade"))
	if err != nil {
		t.Fatalf("SumDir: %v", err)
	}
	return root, digest
}

func readFile(t *testing.T, root, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestWriteAndCheck(t *testing.T) {
	t.Parallel()

	t.Run("write rewrites the catalog's digest and version and nothing else", func(t *testing.T) {
		t.Parallel()
		root, want := catalogRoot(t, nil)
		version, digest, err := musedigest.Write(root)
		if err != nil {
			t.Fatalf("Write: %v", err)
		}
		if version != fixtureVersion || digest != want {
			t.Fatalf("Write = %s, %s; want %s, %s", version, digest, fixtureVersion, want)
		}
		if got := readFile(t, root, "marketplace.json"); got != catalogJSON(fixtureVersion, want) {
			t.Fatalf("the catalog after Write:\n%s", got)
		}
		// A second write changes nothing, and the two agree.
		if _, _, err := musedigest.Write(root); err != nil {
			t.Fatalf("the second Write: %v", err)
		}
		if got := readFile(t, root, "marketplace.json"); got != catalogJSON(fixtureVersion, want) {
			t.Fatalf("the catalog after a second Write:\n%s", got)
		}
		if got, err := musedigest.Check(root); err != nil || got != want {
			t.Fatalf("Check after Write = %s, %v; want %s", got, err, want)
		}
		entries, err := os.ReadDir(root)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 2 {
			t.Fatalf("Write left something beside muse/ and marketplace.json: %v", entries)
		}
	})

	t.Run("check fails on a catalog whose digest is not the package's, and says what to run", func(t *testing.T) {
		t.Parallel()
		root, want := catalogRoot(t, nil)
		_, err := musedigest.Check(root)
		if !errors.Is(err, musedigest.ErrStale) {
			t.Fatalf("Check = %v, want ErrStale", err)
		}
		for _, mention := range []string{digestZero, want, "make muse-digest"} {
			if !strings.Contains(err.Error(), mention) {
				t.Errorf("the error does not mention %q: %v", mention, err)
			}
		}
	})

	t.Run("one changed byte in the package makes a written catalog stale", func(t *testing.T) {
		t.Parallel()
		root, _ := catalogRoot(t, nil)
		if _, _, err := musedigest.Write(root); err != nil {
			t.Fatalf("Write: %v", err)
		}
		writeTree(t, root, map[string]string{"muse/brigade/skills/a/SKILL.md": probeSkill + "\n"})
		if _, err := musedigest.Check(root); !errors.Is(err, musedigest.ErrStale) {
			t.Fatalf("Check = %v, want ErrStale", err)
		}
	})

	t.Run("check fails on a catalog whose version is not the package's, the digest being right", func(t *testing.T) {
		t.Parallel()
		root, want := catalogRoot(t, nil)
		writeTree(t, root, map[string]string{"marketplace.json": catalogJSON("9.9.9", want)})
		_, err := musedigest.Check(root)
		if !errors.Is(err, musedigest.ErrStale) {
			t.Fatalf("Check = %v, want ErrStale", err)
		}
		if !strings.Contains(err.Error(), `pins version "9.9.9"`) || strings.Contains(err.Error(), "carries the digest") {
			t.Fatalf("the error should name the version and only the version: %v", err)
		}
	})

	t.Run("check refuses a catalog write could not rewrite", func(t *testing.T) {
		t.Parallel()
		root, want := catalogRoot(t, nil)
		oneLine := strings.Replace(catalogJSON(fixtureVersion, want), "{\n        \"digest\"", "{ \"digest\"", 1)
		writeTree(t, root, map[string]string{"marketplace.json": oneLine})
		if _, err := musedigest.Check(root); err == nil || errors.Is(err, musedigest.ErrStale) || !strings.Contains(err.Error(), "want exactly one") {
			t.Fatalf("Check = %v, want the refusal Write gives, although the digest and the version are right", err)
		}
	})

	t.Run("recorded reads the brigade entry", func(t *testing.T) {
		t.Parallel()
		root, _ := catalogRoot(t, nil)
		version, digest, err := musedigest.Recorded(root)
		if err != nil || version != "0.0.0" || digest != digestZero {
			t.Fatalf("Recorded = %s, %s, %v", version, digest, err)
		}
	})
}

func TestWriteRefuses(t *testing.T) {
	t.Parallel()
	twoEntries := strings.Replace(catalogJSON("0.0.0", digestZero), "    {\n", "    {\n      \"name\": \"other\",\n      \"version\": \"9.9.9\",\n      \"integrity\": {\n        \"digest\": \""+digestZero+"\"\n      }\n    },\n    {\n", 1)
	for _, tc := range []struct {
		name      string
		overrides map[string]string
		remove    string
		mention   string
	}{
		{"a directory that is not the repository root", nil, "muse/brigade/.muse-plugin/plugin.json", "run this from the repository root"},
		{"a missing VERSION", nil, "muse/brigade/bin/VERSION", "missing muse/brigade/bin/VERSION"},
		{"a VERSION that is no version", map[string]string{"muse/brigade/bin/VERSION": "1.2.3 beta\n"}, "", "invalid version"},
		{"an empty VERSION", map[string]string{"muse/brigade/bin/VERSION": "\n"}, "", "invalid version"},
		{"a missing catalog", nil, "marketplace.json", "missing marketplace.json"},
		{"a catalog that is not JSON", map[string]string{"marketplace.json": catalogJSON("0.0.0", digestZero) + "}"}, "", "not the catalog Muse reads"},
		{"a catalog with no digest line", map[string]string{"marketplace.json": strings.Replace(catalogJSON("0.0.0", digestZero), `"digest"`, `"sha"`, 1)}, "", `0 lines starting with "\"digest\":"`},
		{"a catalog whose digest shares its line", map[string]string{"marketplace.json": strings.Replace(catalogJSON("0.0.0", digestZero), "{\n        \"digest\"", "{ \"digest\"", 1)}, "", `0 lines starting with "\"digest\":"`},
		{"a catalog with two entries", map[string]string{"marketplace.json": twoEntries}, "", "2 lines starting with"},
		{"a catalog that names no brigade", map[string]string{"marketplace.json": strings.Replace(catalogJSON("0.0.0", digestZero), `"name": "brigade"`, `"name": "cavalry"`, 1)}, "", `names no plugin "brigade"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root, _ := catalogRoot(t, tc.overrides)
			if tc.remove != "" {
				if err := os.Remove(filepath.Join(root, filepath.FromSlash(tc.remove))); err != nil {
					t.Fatal(err)
				}
			}
			before, readErr := os.ReadFile(filepath.Join(root, "marketplace.json"))
			_, _, err := musedigest.Write(root)
			if err == nil || !strings.Contains(err.Error(), tc.mention) {
				t.Fatalf("Write = %v, want an error mentioning %q", err, tc.mention)
			}
			if readErr != nil {
				return
			}
			if after := readFile(t, root, "marketplace.json"); after != string(before) {
				t.Fatalf("a refused Write changed the catalog:\n%s", after)
			}
		})
	}
}
