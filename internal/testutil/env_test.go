package testutil

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// nameOf returns the variable name of a KEY=VALUE entry.
func nameOf(entry string) string {
	name, _, _ := strings.Cut(entry, "=")
	return name
}

// valueOf returns the value the environment resolves for name: the last
// entry wins. That is os/exec's dedupEnv rule, which Env is built to match --
// it is NOT execve's (execve resolves nothing; it hands the block through) and
// it is NOT os.Getenv's (Go and libc both take the FIRST occurrence and Go
// blanks later duplicates).
func valueOf(env []string, name string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if key, value, ok := strings.Cut(env[i], "="); ok && key == name {
			return value, true
		}
	}
	return "", false
}

// TestEnvDoesNotInheritTheProcessEnvironmentSerial is the whole point of
// Env, so it is the one test here that cannot be parallel: it has to put
// hostile values in the test process's own environment to prove they do not
// come out the other side. t.Setenv panics in a parallel test.
//
// The scenario is real. A developer running the suite has CLAUDE_CONFIG_DIR
// pointing at a live Claude Code config directory, and may have BRIGADE_*
// set from a session. A child that inherited either would read and write
// the developer's own state.
func TestEnvDoesNotInheritTheProcessEnvironmentSerial(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/definitely/the/developers/own/config")
	t.Setenv("BRIGADE_STATE_DIR", "/definitely/the/developers/own/state")
	t.Setenv("BRIGADE_PROFILE", "the-developers-profile")
	t.Setenv("BRIGADE_TESTUTIL_CANARY", "leaked")

	env := Env(t)

	for _, name := range []string{"BRIGADE_PROFILE", "BRIGADE_TESTUTIL_CANARY"} {
		if value, ok := valueOf(env, name); ok {
			t.Errorf("Env() carried %s=%q over from the test process", name, value)
		}
	}
	for _, name := range []string{"CLAUDE_CONFIG_DIR", "BRIGADE_STATE_DIR"} {
		value, ok := valueOf(env, name)
		if !ok {
			t.Errorf("Env() did not set %s at all", name)
			continue
		}
		if strings.HasPrefix(value, "/definitely/") {
			t.Errorf("Env() left %s at the test process's value %q", name, value)
		}
	}
}

func TestEnvPointsEveryDirectoryAtTheTestsOwnTempTree(t *testing.T) {
	t.Parallel()

	env := Env(t)

	// PATH is the single documented exception: without it nothing can be
	// executed by name. Everything else must be a directory this test owns.
	for _, entry := range env {
		name := nameOf(entry)
		if name == "PATH" {
			continue
		}
		value, _ := valueOf(env, name)
		if !filepath.IsAbs(value) {
			t.Errorf("%s=%q is not an absolute path", name, value)
			continue
		}
		info, err := os.Stat(value)
		if err != nil {
			t.Errorf("%s=%q does not exist: %v", name, value, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s=%q is not a directory", name, value)
		}
	}

	if _, ok := valueOf(env, "PATH"); !ok {
		t.Error("Env() did not set PATH; nothing could be executed by name")
	}
	if home, ok := valueOf(env, "HOME"); !ok {
		t.Error("Env() did not set HOME")
	} else if home == os.Getenv("HOME") {
		t.Errorf("Env() set HOME to the developer's own home %q", home)
	}
}

func TestEnvAppliesExtraLast(t *testing.T) {
	t.Parallel()

	env := Env(t, "BRIGADE_FS_ROOT=/overridden", "BRIGADE_EXTRA=added")
	if value, _ := valueOf(env, "BRIGADE_FS_ROOT"); value != "/overridden" {
		t.Errorf("BRIGADE_FS_ROOT = %q, want the override to win", value)
	}
	if value, _ := valueOf(env, "BRIGADE_EXTRA"); value != "added" {
		t.Errorf("BRIGADE_EXTRA = %q, want %q", value, "added")
	}
}

// TestDirsVarsLeavesHomeAlone guards a contract testscript depends on: each
// script runs with HOME=/no-home so that a stray ~ fails loudly, and a
// Setup that appended a HOME of its own would quietly undo that.
func TestDirsVarsLeavesHomeAlone(t *testing.T) {
	t.Parallel()

	for _, entry := range NewDirs(t.TempDir()).Vars() {
		if nameOf(entry) == "HOME" {
			t.Errorf("Dirs.Vars() set %q; HOME belongs to Env and to testscript", entry)
		}
	}
}

func TestDirsVarsNamesEveryDirectoryOnce(t *testing.T) {
	t.Parallel()

	dirs := NewDirs(t.TempDir())
	var names, values []string
	for _, entry := range dirs.Vars() {
		name, value, _ := strings.Cut(entry, "=")
		names = append(names, name)
		values = append(values, value)
	}
	if slices.Contains(names, "") {
		t.Errorf("Dirs.Vars() = %q, which has a malformed entry", dirs.Vars())
	}
	for _, name := range names {
		if n := strings.Count(strings.Join(names, " "), name); n != 1 {
			t.Errorf("Dirs.Vars() names %s %d times", name, n)
		}
	}
	// Every directory in the layout except HOME should be reachable through
	// a variable; a path created but never exported is a path nothing can
	// be redirected to.
	for _, path := range dirs.paths() {
		if path == dirs.Home {
			continue
		}
		if !slices.Contains(values, path) {
			t.Errorf("Dirs.Vars() exports no variable for %s", path)
		}
	}
}

func TestDirsMkdirCreatesEveryDirectory0700(t *testing.T) {
	t.Parallel()

	dirs := NewDirs(filepath.Join(t.TempDir(), "root"))
	if err := dirs.Mkdir(); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	for _, path := range dirs.paths() {
		info, err := os.Stat(path)
		if err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", path)
		}
		if mode := info.Mode().Perm(); mode != 0o700 {
			t.Errorf("%s has mode %04o, want 0700", path, mode)
		}
	}
}
