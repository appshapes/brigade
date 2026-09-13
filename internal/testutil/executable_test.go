package testutil

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestWriteExecutableRunsAndIsOwnerOnly(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "seven")
	WriteExecutable(t, path, []byte("#!/bin/sh\nexit 7\n"))
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Fatalf("mode = %#o, want 0700", fi.Mode().Perm())
	}
	err = exec.CommandContext(t.Context(), path).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Fatalf("exec: err = %v, want exit 7", err)
	}
}

// TestWriteExecutableAlwaysMakesANewInode is the property the testscript
// Setup hook relies on: an fd that outlives the rewrite — the leaked one,
// in the failure — still refers to the OLD file, so an exec of the new one
// cannot see it as a writer. os.SameFile against a held fd is that
// property; a bare inode number is not, because with nothing holding the
// old inode ext4 hands the number straight back (3 of 3 runs, written that
// way first).
func TestWriteExecutableAlwaysMakesANewInode(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "twice")
	WriteExecutable(t, path, []byte("#!/bin/sh\nexit 1\n"))
	held, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	WriteExecutable(t, path, []byte("#!/bin/sh\nexit 0\n"))
	old, err := held.Stat()
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(old, fresh) {
		t.Fatal("the rewrite reused the file a held fd refers to")
	}
	if err := exec.CommandContext(t.Context(), path).Run(); err != nil {
		t.Fatalf("the rewritten file did not run the new body: %v", err)
	}
}

// TestNoTestWritesAnExecutableAnyOtherWay parses every *_test.go in the
// repository and fails on an os.WriteFile or os.OpenFile whose mode
// literal carries an execute bit: the shape WriteExecutable replaces, and
// the one a future fixture reaches for first. It parses rather than greps
// so that a call split over lines or a mode spelled 0755 is still seen. A
// mode held in a variable is not (release_prep_test.go's writeAbs routes
// those to WriteExecutable itself), and neither is a data write followed
// by chmod +x; that shape exists once, in cmd/brigade's testscript Setup,
// which re-materialises the extracted scripts through the helper.
func TestNoTestWritesAnExecutableAnyOtherWay(t *testing.T) {
	t.Parallel()
	root := RepoRoot(t)
	fset := token.NewFileSet()
	var offenders []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root && (strings.HasPrefix(d.Name(), ".") || d.Name() == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) < 3 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "os" || (sel.Sel.Name != "WriteFile" && sel.Sel.Name != "OpenFile") {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok || lit.Kind != token.INT {
				return true
			}
			if mode, err := strconv.ParseUint(lit.Value, 0, 32); err == nil && mode&0o111 != 0 {
				offenders = append(offenders, fset.Position(call.Pos()).String())
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range offenders {
		t.Errorf("%s: an executable written by os.WriteFile/os.OpenFile; use testutil.WriteExecutable", at)
	}
}
