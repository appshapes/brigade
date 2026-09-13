package teamfile

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/appshapes/brigade/internal/protocol"
)

// maxGitFileBytes bounds every read RepoName makes of git's own metadata:
// a `.git` pointer file, a `commondir` file and a `config` are a few
// hundred bytes in practice, and a bigger one is read no further.
const maxGitFileBytes = 64 << 10

// RepoName is the name a team knows the repository at top by: the last
// path segment of the `origin` remote's URL (else of the first remote in
// git's config) without any `.git` suffix — `thinktech-api` for
// `git@github.com:appshapes/thinktech-api.git` — and, when no remote is
// configured or git's metadata cannot be read, the toplevel directory's
// own name. It is a session's default workspace label (P11-5): the one
// value that tells a session in one of a team's repositories from a
// session in the next with nobody typing anything, and unlike the
// session name it survives a /rename. Only git's own files are read —
// `.git/config`, or for a linked worktree the `gitdir:` pointer and its
// `commondir` — each capped at maxGitFileBytes. Never a path: a URL's
// last segment or a directory's base name, sanitised as a label.
func RepoName(top string) string {
	name := ""
	if url, ok := originURL(top); ok {
		name = nameFromURL(url)
	}
	if name == "" {
		name = dirName(top)
	}
	return protocol.SanitizeLabel(name)
}

// originURL reads the `origin` remote's URL from git's config, else the
// first remote's; false when there is no readable config or no remote.
func originURL(top string) (string, bool) {
	gitDir, ok := resolveGitDir(top)
	if !ok {
		return "", false
	}
	data, err := readGitFile(filepath.Join(gitDir, "config"))
	if err != nil {
		return "", false
	}
	remotes := remoteURLs(data)
	for _, r := range remotes {
		if r.name == "origin" && r.url != "" {
			return r.url, true
		}
	}
	for _, r := range remotes {
		if r.url != "" {
			return r.url, true
		}
	}
	return "", false
}

// resolveGitDir finds the directory holding git's config for top: `.git`
// itself in a plain clone; in a linked worktree the directory the `.git`
// file's `gitdir:` line names, then the main repository's `.git` that its
// `commondir` file names (the config lives there). Lstat, as Toplevel
// does, so a symlinked `.git` is not followed.
func resolveGitDir(top string) (string, bool) {
	entry := filepath.Join(top, ".git")
	fi, err := os.Lstat(entry)
	if err != nil {
		return "", false
	}
	dir := entry
	switch {
	case fi.Mode().IsRegular():
		data, err := readGitFile(entry)
		if err != nil {
			return "", false
		}
		first, _, _ := strings.Cut(string(data), "\n")
		p, ok := strings.CutPrefix(strings.TrimSpace(first), "gitdir:")
		if !ok || strings.TrimSpace(p) == "" {
			return "", false
		}
		dir = relativeTo(top, strings.TrimSpace(p))
	case !fi.IsDir():
		return "", false
	}
	if data, err := readGitFile(filepath.Join(dir, "commondir")); err == nil {
		if c := strings.TrimSpace(string(data)); c != "" {
			dir = relativeTo(dir, c)
		}
	}
	return dir, true
}

// relativeTo resolves p against base unless p is already absolute.
func relativeTo(base, p string) string {
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(base, p))
}

// readGitFile reads at most maxGitFileBytes of one of git's own files.
func readGitFile(path string) ([]byte, error) {
	f, err := os.Open(path) //nolint:gosec // G304: git's own metadata under the discovered toplevel
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	return io.ReadAll(io.LimitReader(f, maxGitFileBytes))
}

type remote struct{ name, url string }

// remoteURLs collects the remotes of a git config in file order, each
// with its first `url` value. It reads only what it needs: `[remote
// "<name>"]` section headers and the `url = ...` lines under them;
// every other line is skipped, comments included.
func remoteURLs(data []byte) []remote {
	var out []remote
	current := -1
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 4096), maxGitFileBytes+1)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		if line[0] == '[' {
			current = -1
			if name, ok := remoteSection(line); ok {
				out = append(out, remote{name: name})
				current = len(out) - 1
			}
			continue
		}
		if current < 0 || out[current].url != "" {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok && strings.TrimSpace(k) == "url" {
			out[current].url = strings.TrimSpace(v)
		}
	}
	return out
}

// remoteSection parses a `[remote "<name>"]` header; anything else,
// `[core]` or `[branch "main"]` included, is false.
func remoteSection(line string) (string, bool) {
	inner, ok := strings.CutSuffix(strings.TrimPrefix(line, "["), "]")
	if !ok {
		return "", false
	}
	rest, ok := strings.CutPrefix(inner, "remote ")
	if !ok {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if len(rest) < 3 || rest[0] != '"' || rest[len(rest)-1] != '"' {
		return "", false
	}
	return rest[1 : len(rest)-1], true
}

// nameFromURL is the last path segment of a remote URL in any of git's
// spellings — scp-like `host:org/name.git`, `ssh://`, `https://`,
// `file://` or a plain path — without a `.git` suffix; "" when the URL
// yields no name.
func nameFromURL(url string) string {
	s := strings.TrimRight(strings.TrimSpace(url), "/")
	s = strings.TrimRight(strings.TrimSuffix(s, ".git"), "/")
	if i := strings.LastIndexAny(s, "/:"); i >= 0 {
		s = s[i+1:]
	}
	if s == "." || s == ".." {
		return ""
	}
	return s
}

// dirName is the toplevel directory's own name, "" for a root.
func dirName(top string) string {
	base := filepath.Base(filepath.Clean(top))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}
