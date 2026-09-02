package testutil

import (
	"os"
	"path/filepath"
	"testing"
)

// Dirs are the directories a child process started by a test is confined
// to. Every path Brigade or Claude Code would otherwise take from the
// developer's own account has one here instead, so a test — or a detached
// watcher a test leaked — cannot reach the real ~/.claude* tree, the real
// XDG directories or the real home.
//
// The zero Dirs is not usable; build one with [NewDirs].
type Dirs struct {
	// Root is the directory the others live under.
	Root string
	// Home backs $HOME.
	Home string
	// ClaudeConfig backs $CLAUDE_CONFIG_DIR. Never hardcode ~/.claude
	// anywhere: this is the only config dir a test may touch.
	ClaudeConfig string
	// PluginRoot backs $CLAUDE_PLUGIN_ROOT.
	PluginRoot string
	// XDGConfig, XDGState and XDGCache back the XDG base directories the
	// adapter kit resolves its files from.
	XDGConfig string
	XDGState  string
	XDGCache  string
	// BrigadeConfig and BrigadeState back $BRIGADE_CONFIG_DIR and
	// $BRIGADE_STATE_DIR.
	BrigadeConfig string
	BrigadeState  string
	// FSRoot backs $BRIGADE_FS_ROOT, the filesystem adapter's store.
	FSRoot string
}

// NewDirs lays out a [Dirs] under root. It creates nothing; call
// [Dirs.Mkdir] for that.
func NewDirs(root string) Dirs {
	return Dirs{
		Root:          root,
		Home:          filepath.Join(root, "home"),
		ClaudeConfig:  filepath.Join(root, "claude-config"),
		PluginRoot:    filepath.Join(root, "plugin-root"),
		XDGConfig:     filepath.Join(root, "xdg", "config"),
		XDGState:      filepath.Join(root, "xdg", "state"),
		XDGCache:      filepath.Join(root, "xdg", "cache"),
		BrigadeConfig: filepath.Join(root, "brigade-config"),
		BrigadeState:  filepath.Join(root, "brigade-state"),
		FSRoot:        filepath.Join(root, "fs-root"),
	}
}

// paths returns every directory in the layout.
func (d Dirs) paths() []string {
	return []string{
		d.Home, d.ClaudeConfig, d.PluginRoot,
		d.XDGConfig, d.XDGState, d.XDGCache,
		d.BrigadeConfig, d.BrigadeState, d.FSRoot,
	}
}

// Mkdir creates every directory in the layout, 0700 as the plan's file
// modes require (T7, T14).
func (d Dirs) Mkdir() error {
	for _, p := range d.paths() {
		if err := os.MkdirAll(p, 0o700); err != nil {
			return err
		}
	}
	return nil
}

// Vars returns the directory half of a child environment, in os.Environ
// form.
//
// $HOME is deliberately absent. [Env] sets it to [Dirs.Home]; testscript
// sets its own HOME=/no-home so that any ~ resolution inside a script fails
// loudly, and a caller building script variables must not overwrite that.
func (d Dirs) Vars() []string {
	return []string{
		"CLAUDE_CONFIG_DIR=" + d.ClaudeConfig,
		"CLAUDE_PLUGIN_ROOT=" + d.PluginRoot,
		"XDG_CONFIG_HOME=" + d.XDGConfig,
		"XDG_STATE_HOME=" + d.XDGState,
		"XDG_CACHE_HOME=" + d.XDGCache,
		"BRIGADE_CONFIG_DIR=" + d.BrigadeConfig,
		"BRIGADE_STATE_DIR=" + d.BrigadeState,
		"BRIGADE_FS_ROOT=" + d.FSRoot,
	}
}

// Env builds the environment for a child process from scratch and returns
// it in os.Environ form, ready for exec.Cmd.Env.
//
// It does NOT copy the test process's environment. That is the point: the
// developer running the suite has a real CLAUDE_CONFIG_DIR (never ~/.claude
// on this project's machines) and real BRIGADE_* values, and a child that
// inherited them would read and write the developer's own state. Exactly
// one variable is carried over — PATH, without which nothing can be
// executed by name — and it is copied explicitly rather than by inheritance
// so that the exception is visible here and nowhere else.
//
// TMPDIR is deliberately not redirected: unix sockets have to stay under
// /tmp, because macOS caps sun_path at 103 bytes and a t.TempDir path is
// already about 91 of them (plan 7.3).
//
// extra is appended last, so a caller can override any variable above or
// add its own. Env creates the directories and fails the test if it cannot.
func Env(tb testing.TB, extra ...string) []string {
	tb.Helper()
	d := NewDirs(tb.TempDir())
	if err := d.Mkdir(); err != nil {
		tb.Fatalf("testutil.Env: %v", err)
	}
	vars := []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + d.Home,
	}
	vars = append(vars, d.Vars()...)
	// GOCOVERDIR travels when the ambient run has one. A child built by
	// [Build] under BRIGADE_COVER=1 is instrumented, and an instrumented
	// binary whose environment carries no GOCOVERDIR writes nothing and
	// WARNS on stderr — which several tests here assert is empty. Passing
	// it through is what makes coverage across the process boundary (plan
	// 9.4) work with a curated environment; with no GOCOVERDIR in the
	// ambient run, nothing is added and the environment is unchanged.
	if dir := os.Getenv("GOCOVERDIR"); dir != "" {
		vars = append(vars, "GOCOVERDIR="+dir)
	}
	return append(vars, extra...)
}
