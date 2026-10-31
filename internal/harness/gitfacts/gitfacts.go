// Package gitfacts reads the few facts about a checkout the manager
// digest's state entry carries (card 162, manager-digest plan §5.3): the
// commit HEAD names and the branch it is on, read from git's own files
// with no process started; the commits a session made since it started,
// which only git itself can list, so `git log` is run — an argument
// array, the allow-listed child environment, never a shell, a short
// deadline, and nothing but its stdout read; and whether a path is
// ignored, which `git check-ignore` answers. Every failure leaves the
// caller's facts as they were: a checkout without git, a repository with
// no commit yet and a `.git` nobody can read all answer "nothing known",
// never an error a hook would print.
package gitfacts

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/appshapes/brigade/internal/adapterkit"
	"github.com/appshapes/brigade/internal/harness/teamfile"
	"github.com/appshapes/brigade/internal/protocol"
)

// The bounds of what one git call may hand back.
const (
	// MaxLogCommits caps one `git log`: a session that made more commits
	// since it started keeps the newest ones, which the digest's own cap
	// (40 lines) never exceeds anyway.
	MaxLogCommits = 100
	// maxOutputBytes caps a child's stdout; a hundred log lines are a few
	// KiB, and anything past 1 MiB is not an answer.
	maxOutputBytes = 1 << 20
	// MaxSubjectChars caps a commit subject the entry carries, in code
	// points, with protocol.TruncationMarker when cut.
	MaxSubjectChars = 200
	// maxRefBytes bounds a HEAD or ref file read; a ref is 41 bytes.
	maxRefBytes = 4096
	// waitDelay is how long a cancelled git may outlive its SIGTERM.
	waitDelay = 2 * time.Second
)

// A Commit is one commit `git log` listed: the full hash, the subject
// (sanitised and capped), the card number the subject starts with (the
// repository's `<card>: <summary>` convention, 0 when it starts with
// none) and the commit time.
type Commit struct {
	Hash    string
	Subject string
	Card    int
	At      time.Time
}

// cardPrefix is the commit-message convention: a card number, a colon.
var cardPrefix = regexp.MustCompile(`^([0-9]{1,6}):`)

// hexHash is a full SHA-1 or SHA-256 object name.
var hexHash = regexp.MustCompile(`^[0-9a-f]{40}([0-9a-f]{24})?$`)

// Head reads what HEAD names in the checkout at top, from git's own
// files: the commit hash, and the branch when HEAD is symbolic (""
// when detached). A repository whose HEAD names a branch with no commit
// yet answers the branch and no hash; anything unreadable answers
// nothing at all. No process is started: this runs at every prompt.
func Head(top string) (hash, branch string) {
	gitDir, common, ok := teamfile.GitDirs(top)
	if !ok {
		return "", ""
	}
	head, err := readSmall(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return "", ""
	}
	line := strings.TrimSpace(head)
	ref, symbolic := strings.CutPrefix(line, "ref:")
	if !symbolic {
		if hexHash.MatchString(line) {
			return line, ""
		}
		return "", ""
	}
	ref = strings.TrimSpace(ref)
	branch, _ = strings.CutPrefix(ref, "refs/heads/")
	if branch == ref {
		branch = ""
	}
	if !cleanRef(ref) {
		return "", branch
	}
	if h, err := readSmall(filepath.Join(common, filepath.FromSlash(ref))); err == nil {
		if h = strings.TrimSpace(h); hexHash.MatchString(h) {
			return h, branch
		}
	}
	return packedRef(filepath.Join(common, "packed-refs"), ref), branch
}

// cleanRef accepts a ref name that is a clean relative path of plain
// components, so a HEAD file cannot point the read outside the common
// directory.
func cleanRef(ref string) bool {
	if ref == "" || strings.HasPrefix(ref, "/") || strings.Contains(ref, "\\") {
		return false
	}
	for part := range strings.SplitSeq(ref, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

// packedRef finds ref in a packed-refs file: `<hash> <ref>` lines, the
// peeled `^<hash>` lines and `#` comments skipped.
func packedRef(path, ref string) string {
	f, err := os.Open(path) //nolint:gosec // G304: git's own metadata under the discovered toplevel
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxOutputBytes))
	if err != nil {
		return ""
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		hash, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		if !ok || name != ref || !hexHash.MatchString(hash) {
			continue
		}
		return hash
	}
	return ""
}

// readSmall reads one of git's small files, capped.
func readSmall(path string) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: git's own metadata under the discovered toplevel
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	data, err := io.ReadAll(io.LimitReader(f, maxRefBytes))
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// Made is the commits HEAD's reflog in the checkout shows were made there
// at or after since (card 172), read from git's own file with no process
// started: the new side of each entry a commit, an amend, a cherry-pick,
// a revert, `am`, a rebase step that writes a commit, or a merge that
// made a merge commit (a `pull` or `merge` entry that is not a
// fast-forward). A fast-forward, a checkout and a reset only move HEAD to
// commits made elsewhere. Nil when the reflog cannot be read (git writes
// none when core.logAllRefUpdates is off): nothing is then known made.
// Only the newest maxOutputBytes of the file are read; a session that
// moved HEAD more often than that keeps the newest moves.
func Made(top string, since time.Time) map[string]bool {
	gitDir, _, ok := teamfile.GitDirs(top)
	if !ok {
		return nil
	}
	data, err := readTail(filepath.Join(gitDir, "logs", "HEAD"), maxOutputBytes)
	if err != nil {
		return nil
	}
	made := map[string]bool{}
	cutoff := since.Unix()
	for line := range strings.SplitSeq(data, "\n") {
		head, msg, ok := strings.Cut(line, "\t")
		fields := strings.Fields(head)
		if !ok || len(fields) < 4 || !hexHash.MatchString(fields[1]) {
			continue
		}
		secs, err := strconv.ParseInt(fields[len(fields)-2], 10, 64)
		if err != nil || secs < cutoff {
			continue
		}
		action, text, _ := strings.Cut(msg, ": ")
		if madeBy(action, text) {
			made[fields[1]] = true
		}
	}
	return made
}

// madeBy reports whether a reflog entry with this action (the
// GIT_REFLOG_ACTION part before the first ": ") and text wrote the commit
// it moved HEAD to.
func madeBy(action, text string) bool {
	switch {
	case action == "commit" || strings.HasPrefix(action, "commit ("):
		return true // commit, (initial), (amend), (merge)
	case action == "cherry-pick" || action == "revert" || action == "am":
		return true
	}
	for _, step := range []string{"(pick)", "(reword)", "(edit)", "(squash)", "(fixup)", "(continue)"} {
		if strings.HasSuffix(action, step) {
			return true
		}
	}
	if strings.Contains(action, "(") {
		return false // a rebase's start, finish or abort
	}
	if action == "pull" || strings.HasPrefix(action, "pull ") || strings.HasPrefix(action, "merge ") {
		return strings.TrimSpace(text) != "Fast-forward"
	}
	return false
}

// readTail reads the last limit bytes of a file, dropping the first line
// when the read starts inside one.
func readTail(path string, limit int64) (string, error) {
	f, err := os.Open(path) //nolint:gosec // G304: git's own metadata under the discovered toplevel
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	start := max(info.Size()-limit, 0)
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return "", err
	}
	data, err := io.ReadAll(io.LimitReader(f, limit))
	if err != nil {
		return "", err
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		}
	}
	return string(data), nil
}

// A Runner runs git in one checkout: the toplevel, the environment the
// child's allow-listed one is built from (the hook's or the command's
// own, in os.Environ form) and the deadline each call gets. Git is found
// on that environment's PATH, never on this process's.
type Runner struct {
	Top     string
	Environ []string
	Timeout time.Duration
}

// ErrNoGit is the error of a Runner whose environment's PATH has no git.
var ErrNoGit = errors.New("gitfacts: no git on PATH")

// Log lists the commits reachable from HEAD and not from since (the
// `since..HEAD` range), newest first as git lists them, at most
// MaxLogCommits; with author set, only those whose author line contains
// it, matched as fixed text. An empty since lists nothing: the entry's
// start head is what makes a range, and a session with none made no
// commit Brigade could attribute.
func (r Runner) Log(ctx context.Context, since, author string) ([]Commit, error) {
	if since == "" || !hexHash.MatchString(since) {
		return nil, nil
	}
	args := []string{"log", "--no-color", "--max-count=" + strconv.Itoa(MaxLogCommits), "--format=%H%x1f%ct%x1f%s"}
	if author != "" {
		args = append(args, "--fixed-strings", "--author="+author)
	}
	args = append(args, since+"..HEAD", "--")
	out, err := r.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for line := range strings.SplitSeq(out, "\n") {
		parts := strings.SplitN(line, "\x1f", 3)
		if len(parts) != 3 || !hexHash.MatchString(parts[0]) {
			continue
		}
		secs, perr := strconv.ParseInt(parts[1], 10, 64)
		if perr != nil {
			continue
		}
		subject := protocol.TruncateRunes(strings.Join(strings.Fields(protocol.Sanitize(parts[2])), " "), MaxSubjectChars)
		card := 0
		if m := cardPrefix.FindStringSubmatch(subject); m != nil {
			card, _ = strconv.Atoi(m[1])
		}
		commits = append(commits, Commit{Hash: parts[0], Subject: subject, Card: card, At: time.Unix(secs, 0).UTC()})
	}
	return commits, nil
}

// AuthorEmail is `user.email` as git resolves it for the checkout: the
// value the commits a session makes there carry, so Log can keep to
// them. "" when none is configured.
func (r Runner) AuthorEmail(ctx context.Context) (string, error) {
	out, err := r.run(ctx, "config", "--get", "user.email")
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1 {
			return "", nil // unset
		}
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// Ignored reports whether git ignores path in the checkout — the answer
// `git check-ignore` gives (exit 0 ignored, 1 not), with the path given
// after `--` so it is never read as an option. When path lies beyond a
// symbolic link inside the checkout (card 172: a second checkout on one
// machine links its synced folder to the first one's), git refuses the
// path itself (`beyond a symbolic link`, exit 128) and would commit only
// the link, so the link is the path asked about.
func (r Runner) Ignored(ctx context.Context, path string) (bool, error) {
	path = firstLink(r.Top, path)
	_, err := r.run(ctx, "check-ignore", "--quiet", "--", path)
	if err == nil {
		return true, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

// firstLink is the first symbolic link on the way from top down to path,
// spelled as path is (absolute, or relative to top), or path itself when
// there is none, when a step does not exist yet, or when path is not
// under top. Only the steps below top are looked at: top's own spelling
// (macOS's /var is a link) is the checkout's.
func firstLink(top, path string) string {
	rel := path
	if filepath.IsAbs(path) {
		r, err := filepath.Rel(top, path)
		if err != nil {
			return path
		}
		rel = r
	}
	rel = filepath.Clean(rel)
	if rel == "." || rel == ".." || filepath.IsAbs(rel) || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := top
	for i, part := range parts {
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		if err != nil {
			return path
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if filepath.IsAbs(path) {
				return cur
			}
			return filepath.Join(parts[:i+1]...)
		}
	}
	return path
}

// run starts git with args in the checkout and returns its stdout. The
// child gets the allow-listed environment and nothing else, the null
// device for stdin, a discarded stderr and the Runner's deadline; a
// deadline of zero means five seconds.
func (r Runner) run(ctx context.Context, args ...string) (string, error) {
	git, ok := lookPath(adapterkit.Getenv(r.Environ, "PATH"), "git")
	if !ok {
		return "", ErrNoGit
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	argv := append([]string{"-C", r.Top}, args...)
	//nolint:forbidigo,gosec // gitfacts IS the one git spawn of card 162: an argv array of fixed words plus a path, the allow-listed environment, no shell
	cmd := exec.CommandContext(ctx, git, argv...)
	cmd.Env = adapterkit.ChildEnv(r.Environ)
	cmd.Stdin = nil
	var out capped
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	cmd.Cancel = func() error { return adapterkit.CancelProcess(cmd.Process) }
	cmd.WaitDelay = waitDelay
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", err
	}
	return out.String(), nil
}

// capped is a bytes.Buffer that keeps at most maxOutputBytes.
type capped struct{ bytes.Buffer }

func (c *capped) Write(p []byte) (int, error) {
	room := maxOutputBytes - c.Len()
	if room <= 0 {
		return len(p), nil
	}
	if len(p) > room {
		_, _ = c.Buffer.Write(p[:room])
		return len(p), nil
	}
	return c.Buffer.Write(p)
}

// lookPath searches pathVar (a PATH value) for an executable regular
// file named name, as the hook's shadowing check does, and returns the
// first hit; on Windows the name takes `.exe`.
func lookPath(pathVar, name string) (string, bool) {
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	for _, dir := range filepath.SplitList(pathVar) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		fi, err := os.Stat(p)
		if err != nil || !adapterkit.IsExecutable(fi) {
			continue
		}
		if abs, aerr := filepath.Abs(p); aerr == nil {
			p = abs
		}
		return p, true
	}
	return "", false
}
