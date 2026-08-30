package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"

	"github.com/rogpeppe/go-internal/testscript"
)

// TestMain makes the test binary act as the `mini` command when re-executed by testscript,
// so `exec mini ...` in a script runs a real child process without a separate `go build`.
func TestMain(m *testing.M) {
	testscript.Main(m, map[string]func(){
		"mini":   main,
		"helper": func() { os.Args = append([]string{"helper"}, os.Args[1:]...); main() },
	})
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir:                 "testdata/script",
		RequireExplicitExec: true,
		UpdateScripts:       os.Getenv("UPDATE_SCRIPTS") == "1",
		Cmds: map[string]func(ts *testscript.TestScript, neg bool, args []string){
			"status": cmdStatus, // status <code> <cmd> [args...]: run and assert the exact exit code
			"json":   cmdJSON,   // json <stdout|stderr|file> <.dotted.path> <expected>: assert a JSON value
		},
	})
}

func cmdStatus(ts *testscript.TestScript, neg bool, args []string) {
	if neg || len(args) < 2 {
		ts.Fatalf("usage: status <code> <cmd> [args...]")
	}
	want, err := strconv.Atoi(args[0])
	ts.Check(err)
	err = ts.Exec(args[1], args[2:]...)
	got := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			ts.Fatalf("exec %s: %v", args[1], err)
		}
		got = ee.ExitCode()
	}
	if got != want {
		ts.Fatalf("exit status %d, want %d", got, want)
	}
}

func cmdJSON(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 3 {
		ts.Fatalf("usage: json <stdout|stderr|file> <.path> <expected>")
	}
	var doc any
	ts.Check(json.Unmarshal([]byte(ts.ReadFile(args[0])), &doc))
	cur := doc
	for _, key := range strings.Split(strings.TrimPrefix(args[1], "."), ".") {
		if key == "" {
			continue
		}
		m, ok := cur.(map[string]any)
		if !ok {
			ts.Fatalf("path %s: %q is not an object", args[1], key)
		}
		cur, ok = m[key]
		if !ok {
			if neg {
				return
			}
			ts.Fatalf("path %s: key %q missing", args[1], key)
		}
	}
	got := fmt.Sprint(cur)
	if (got == args[2]) == neg {
		ts.Fatalf("json %s %s = %q, want %q (neg=%v)", args[0], args[1], got, args[2], neg)
	}
}
