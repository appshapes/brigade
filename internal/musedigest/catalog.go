package musedigest

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// What the catalog commands read and write, relative to the repository root.
const (
	// PackageDir is the Muse Code package.
	PackageDir = "muse/brigade"
	// ManifestFile marks PackageDir as a Muse package.
	ManifestFile = PackageDir + "/.muse-plugin/plugin.json"
	// VersionFile is the release the package pins.
	VersionFile = PackageDir + "/bin/VERSION"
	// CatalogFile is the Muse marketplace catalog (card 115).
	CatalogFile = "marketplace.json"
	// PluginName is the catalog entry the digest belongs to.
	PluginName = "brigade"
)

// ErrStale is returned by Check when the catalog is not what Write would
// write: another digest, another version.
var ErrStale = errors.New("the catalog is stale")

// The catalog keeps "version" and "digest" each on a line of its own:
// scripts/ci/muse-plugin-check.sh and release.yml read them with anchored
// seds, and Write rewrites exactly those two lines, leaving every other byte
// of the hand-written file alone.
var (
	digestLine  = regexp.MustCompile(`(?m)^([ \t]*"digest":[ \t]*)"[^"\n]*"`)
	versionLine = regexp.MustCompile(`(?m)^([ \t]*"version":[ \t]*)"[^"\n]*"`)
	versionForm = regexp.MustCompile(`^[0-9A-Za-z.+-]+$`)
)

// catalog is the part of marketplace.json the commands read.
type catalog struct {
	Plugins []struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Integrity struct {
			Digest string `json:"digest"`
		} `json:"integrity"`
	} `json:"plugins"`
}

// Package returns the digest of the Muse package under the repository root.
func Package(root string) (string, error) {
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(ManifestFile))); err != nil {
		return "", fmt.Errorf("run this from the repository root: no %s here", ManifestFile)
	}
	digest, err := SumDir(filepath.Join(root, filepath.FromSlash(PackageDir)))
	if err != nil {
		return "", fmt.Errorf("%s: %w", PackageDir, err)
	}
	return digest, nil
}

// Recorded returns the version and the digest the catalog under root carries
// for the brigade entry.
func Recorded(root string) (version, digest string, err error) {
	data, err := os.ReadFile(filepath.Join(root, CatalogFile))
	if err != nil {
		return "", "", fmt.Errorf("missing %s: %w", CatalogFile, err)
	}
	return recorded(data)
}

func recorded(data []byte) (version, digest string, err error) {
	var c catalog
	if err := json.Unmarshal(data, &c); err != nil {
		return "", "", fmt.Errorf("%s is not the catalog Muse reads: %w", CatalogFile, err)
	}
	for _, p := range c.Plugins {
		if p.Name == PluginName {
			return p.Version, p.Integrity.Digest, nil
		}
	}
	return "", "", fmt.Errorf("%s names no plugin %q", CatalogFile, PluginName)
}

// Check holds the catalog against what Write would write for this tree: the
// package's digest and the package's version, on the two lines Write keeps.
// It returns the digest when nothing would change, and an error wrapping
// ErrStale that names what differs when something would.
func Check(root string) (string, error) {
	p, err := prepare(root)
	if err != nil {
		return "", err
	}
	if bytes.Equal(p.want, p.have) {
		return p.digest, nil
	}
	haveVersion, haveDigest, err := recorded(p.have)
	if err != nil {
		return "", err
	}
	var differs []string
	if haveDigest != p.digest {
		differs = append(differs, fmt.Sprintf("it carries the digest %q, and the digest of %s/ is %s", haveDigest, PackageDir, p.digest))
	}
	if haveVersion != p.version {
		differs = append(differs, fmt.Sprintf("it pins version %q, and %s is %s", haveVersion, VersionFile, p.version))
	}
	return "", fmt.Errorf("%w: %s is not what 'make muse-digest' writes for this tree (%s): run 'make muse-digest' and commit the catalog",
		ErrStale, CatalogFile, strings.Join(differs, "; "))
}

// Write rewrites the catalog's digest from the package and its version from
// the package's VERSION, and returns both. Nothing else in the file changes,
// and a catalog that is already right is not touched.
func Write(root string) (version, digest string, err error) {
	p, err := prepare(root)
	if err != nil {
		return "", "", err
	}
	if bytes.Equal(p.want, p.have) {
		return p.version, p.digest, nil
	}
	if err := replaceFile(filepath.Join(root, CatalogFile), p.want, p.mode); err != nil {
		return "", "", err
	}
	return p.version, p.digest, nil
}

// prepared is the catalog as it is and as Write would leave it.
type prepared struct {
	version, digest string
	have, want      []byte
	mode            os.FileMode
}

// prepare computes what Write writes, without writing: the package's digest
// and version set on the catalog's two lines, and read back as JSON.
func prepare(root string) (prepared, error) {
	digest, err := Package(root)
	if err != nil {
		return prepared{}, err
	}
	pin, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(VersionFile)))
	if err != nil {
		return prepared{}, fmt.Errorf("missing %s: %w", VersionFile, err)
	}
	version, _, _ := strings.Cut(string(pin), "\n")
	version = strings.TrimSuffix(version, "\r")
	if !versionForm.MatchString(version) {
		return prepared{}, fmt.Errorf("invalid version in %s: %q", VersionFile, version)
	}
	target := filepath.Join(root, CatalogFile)
	info, err := os.Stat(target)
	if err != nil {
		return prepared{}, fmt.Errorf("missing %s: %w", CatalogFile, err)
	}
	have, err := os.ReadFile(target)
	if err != nil {
		return prepared{}, err
	}
	want, err := replaceLine(have, digestLine, "digest", digest)
	if err != nil {
		return prepared{}, err
	}
	want, err = replaceLine(want, versionLine, "version", version)
	if err != nil {
		return prepared{}, err
	}
	// The textual rewrite is checked against what a JSON reader, Muse among them, would see.
	gotVersion, gotDigest, err := recorded(want)
	if err != nil {
		return prepared{}, err
	}
	if gotVersion != version || gotDigest != digest {
		return prepared{}, fmt.Errorf("the write would not take in %s: the %s entry would read version %q, digest %q", CatalogFile, PluginName, gotVersion, gotDigest)
	}
	return prepared{version: version, digest: digest, have: have, want: want, mode: info.Mode().Perm()}, nil
}

// replaceFile puts data at target through a temporary file beside it and a
// rename, so an interrupted write leaves the catalog as it was.
func replaceFile(target string, data []byte, mode os.FileMode) (err error) {
	tmp, err := os.CreateTemp(filepath.Dir(target), filepath.Base(target)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmp.Name())
		}
	}()
	if _, err = tmp.Write(data); err != nil {
		return err
	}
	if err = tmp.Chmod(mode); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// replaceLine sets the one `"key": "…"` line of the catalog to value.
func replaceLine(data []byte, line *regexp.Regexp, key, value string) ([]byte, error) {
	if n := len(line.FindAllIndex(data, -1)); n != 1 {
		return nil, fmt.Errorf("%s has %d lines starting with %q, want exactly one: the key must start a line of its own, once", CatalogFile, n, `"`+key+`":`)
	}
	return line.ReplaceAll(data, []byte(`${1}"`+value+`"`)), nil
}
