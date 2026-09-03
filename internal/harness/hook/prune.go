package hook

import (
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/adapterkit/log"
)

// The weekly cache prune (the P3-4 row; 6.2, 3.2): the bootstrap caches one
// release binary per pinned version under ${XDG_DATA_HOME:-~/.local/share}/
// brigade/bin/brigade-<version>-<os>-<arch>, and nothing else ever removes
// an old one. SessionStart removes, at most once every pruneInterval (a
// 0600 stamp file under the state dir), the cached binaries whose version
// differs from ${CLAUDE_PLUGIN_ROOT}/bin/VERSION and whose mtime is older
// than pruneAge. Never the current version, never any other file, best
// effort, logged.
const (
	pruneInterval = 7 * 24 * time.Hour
	pruneAge      = 7 * 24 * time.Hour
	cachePrefix   = "brigade-"
	pruneStamp    = "cache-prune.stamp"
)

// pruneCache runs the weekly prune from SessionStart.
func (r *run) pruneCache(f facts, now time.Time) {
	stamp := filepath.Join(f.stateDir, "state", pruneStamp)
	if last, ok := lastPrune(stamp); ok && now.Sub(last) < pruneInterval {
		return
	}
	root := adapterkit.Getenv(r.environ, envPluginRoot)
	if root == "" || !filepath.IsAbs(root) {
		return
	}
	current := currentVersion(root)
	if current == "" {
		r.log.Debug("cache prune skipped: the plugin's VERSION is unreadable")
		return
	}
	cacheDir, ok := cacheBinDir(r.environ)
	if !ok {
		return
	}
	removed := r.pruneDir(cacheDir, current, now)
	if err := adapterkit.MkdirPrivate(filepath.Dir(stamp)); err == nil {
		if werr := adapterkit.WriteAtomic(stamp, []byte(now.UTC().Format(time.RFC3339)+"\n")); werr != nil {
			r.log.Debug("cache prune stamp not written", log.Err(werr))
		}
	}
	if removed > 0 {
		r.log.Info("cache prune removed old binaries", slog.Int("removed", removed))
	}
}

// lastPrune reads the stamp: the RFC 3339 time of the last prune, else
// the file's mtime for a stamp whose content is unreadable.
func lastPrune(stamp string) (time.Time, bool) {
	fi, err := os.Lstat(stamp)
	if err != nil || !fi.Mode().IsRegular() {
		return time.Time{}, false
	}
	if data, rerr := adapterkit.ReadStrict(stamp); rerr == nil {
		if t, perr := time.Parse(time.RFC3339, strings.TrimSpace(string(data))); perr == nil {
			return t, true
		}
	}
	return fi.ModTime(), true
}

// currentVersion reads ${CLAUDE_PLUGIN_ROOT}/bin/VERSION, one line.
func currentVersion(root string) string {
	data, err := os.ReadFile(filepath.Join(root, "bin", "VERSION"))
	if err != nil {
		return ""
	}
	first, _, _ := strings.Cut(string(data), "\n")
	return strings.TrimSpace(first)
}

// cacheBinDir is the bootstrap's cache directory: an absolute
// XDG_DATA_HOME, else HOME/.local/share, then brigade/bin (6.2 applies the
// same absolute-only rule).
func cacheBinDir(environ []string) (string, bool) {
	base := adapterkit.Getenv(environ, "XDG_DATA_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home := adapterkit.Getenv(environ, "HOME")
		if home == "" || !filepath.IsAbs(home) {
			return "", false
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "brigade", "bin"), true
}

// pruneDir removes the eligible binaries in dir and reports how many.
func (r *run) pruneDir(dir, current string, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			r.log.Debug("cache prune: directory unreadable", log.Err(err))
		}
		return 0
	}
	removed := 0
	for _, e := range entries {
		version, ok := cachedVersion(e.Name())
		if !ok || version == current {
			continue
		}
		path := filepath.Join(dir, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || now.Sub(fi.ModTime()) < pruneAge {
			continue
		}
		if err := os.Remove(path); err != nil {
			r.log.Debug("cache prune: not removed", log.Err(err))
			continue
		}
		r.log.Info("cache prune: removed", slog.String("version", version))
		removed++
	}
	return removed
}

// cachedVersion parses `brigade-<version>-<os>-<arch>` and returns the
// version. The version may itself contain hyphens (a pre-release), so the
// os and arch are the LAST two components. Anything else — the bootstrap's
// temporary files (`.brigade-…`), a stray file, a name with fewer parts —
// is not a cached binary.
func cachedVersion(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, cachePrefix)
	if !ok || rest == "" {
		return "", false
	}
	parts := strings.Split(rest, "-")
	if len(parts) < 3 {
		return "", false
	}
	for _, p := range parts {
		if p == "" {
			return "", false
		}
	}
	return strings.Join(parts[:len(parts)-2], "-"), true
}
