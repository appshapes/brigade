// Package tscmd holds the custom testscript commands the Brigade scripts
// use. They close five gaps in testscript's builtin set (plan 9.1):
//
//	status <exit-code> <command> [args...]
//	    Run a command and assert its exact exit status, leaving stdout and
//	    stderr in place for the checks that follow. The builtin `exec`
//	    asserts only success, and `! exec` only failure, so neither can tell
//	    a `usage` (2) from a `not_found` (6) — and the exit code is half of
//	    Brigade's error contract (4.6).
//
//	json <stdout|stderr|file> <.dotted.path> <expected>     (negatable)
//	    Assert one field of a JSON document. The builtin `stdout` is a
//	    regexp over the whole stream, which passes on a document that
//	    happens to contain the text somewhere else.
//
//	jsonenv <stdout|stderr|file> <.dotted.path> <VAR>
//	    Export one field into the script environment, so a value printed by
//	    one command can be an argument to the next.
//
//	expand <in> <out>
//	    Write <in> to <out> with $VAR expanded, so a script can build the
//	    stdin document for the next command out of values it just learned.
//
//	sleeper
//	    Start a `sleep 300` child, reap it, and export $SLEEPER_PID. It
//	    stands in for a live Claude Code process. Reaping matters: kill(pid,
//	    0) keeps succeeding on an unreaped zombie, which is the false
//	    positive E0-5 measured at 27.6 s and 59.0 s.
//
// A note that matters for anything added here: these commands must not call
// ts.Stdout or ts.Stderr. testscript replaces the script's stdout and
// stderr with a builtin command's own buffers the moment either is called,
// so `status` would erase the output of the command it just ran.
package tscmd

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"

	"github.com/rogpeppe/go-internal/testscript"
)

// Commands returns the command table to put in testscript.Params.Cmds.
func Commands() map[string]func(ts *testscript.TestScript, neg bool, args []string) {
	return map[string]func(ts *testscript.TestScript, neg bool, args []string){
		"status":  Status,
		"json":    JSON,
		"jsonenv": JSONEnv,
		"expand":  Expand,
		"sleeper": Sleeper,
	}
}

// Status implements `status <exit-code> <command> [args...]`.
func Status(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("status: `!` is meaningless here; name the exit code you expect")
	}
	if len(args) < 2 {
		ts.Fatalf("usage: status <exit-code> <command> [args...]")
	}
	want, err := strconv.Atoi(args[0])
	if err != nil {
		ts.Fatalf("status: %q is not an exit code", args[0])
	}
	got, ok := exitCode(ts.Exec(args[1], args[2:]...))
	if !ok {
		ts.Fatalf("status: %s did not run to completion: %v", strings.Join(args[1:], " "), err)
	}
	if got != want {
		ts.Fatalf("status: %s exited %d, want %d", strings.Join(args[1:], " "), got, want)
	}
}

// exitCode maps the error of a finished command to its exit status. ok is
// false when the command never ran, or died on a signal, so that neither is
// silently reported as an exit code the script could assert.
func exitCode(err error) (code int, ok bool) {
	if err == nil {
		return 0, true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return exit.ExitCode(), true
	}
	return 0, false
}

// JSON implements `json <stdout|stderr|file> <.dotted.path> <expected>`.
func JSON(ts *testscript.TestScript, neg bool, args []string) {
	if len(args) != 3 {
		ts.Fatalf("usage: json <stdout|stderr|file> <.dotted.path> <expected>")
	}
	got := field(ts, args[0], args[1])
	switch {
	case neg && got == args[2]:
		ts.Fatalf("json: %s%s is %q and the `!` says it must not be", args[0], args[1], got)
	case !neg && got != args[2]:
		ts.Fatalf("json: %s%s is %q, want %q", args[0], args[1], got, args[2])
	}
}

// JSONEnv implements `jsonenv <stdout|stderr|file> <.dotted.path> <VAR>`.
func JSONEnv(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("jsonenv: `!` is meaningless here")
	}
	if len(args) != 3 {
		ts.Fatalf("usage: jsonenv <stdout|stderr|file> <.dotted.path> <VAR>")
	}
	name := args[2]
	if name == "" || strings.ContainsAny(name, "=$") {
		ts.Fatalf("jsonenv: %q is not usable as a variable name", name)
	}
	ts.Setenv(name, field(ts, args[0], args[1]))
}

// field reads src — "stdout", "stderr" or a file relative to the script's
// directory — and returns the scalar at path.
func field(ts *testscript.TestScript, src, path string) string {
	value, err := lookup([]byte(ts.ReadFile(src)), path)
	if err != nil {
		ts.Fatalf("json: %s: %v", src, err)
	}
	return value
}

// lookup parses one JSON document and renders the scalar at a dotted path
// such as ".error.code" or ".results.0.id".
//
// A path that is not present is an error rather than a mismatch, in both
// the plain and the negated form. A renamed or vanished member is the way
// these assertions go quietly wrong, and "no such member" names the real
// problem where "want X, got empty" would not.
func lookup(doc []byte, path string) (string, error) {
	if !strings.HasPrefix(path, ".") {
		return "", fmt.Errorf("path %q must start with a dot", path)
	}
	var value any
	if err := json.Unmarshal(doc, &value); err != nil {
		return "", fmt.Errorf("not one JSON document: %w", err)
	}
	walked := ""
	for _, name := range strings.Split(path[1:], ".") {
		if name == "" {
			return "", fmt.Errorf("path %q has an empty segment", path)
		}
		next, err := step(value, name)
		if err != nil {
			return "", fmt.Errorf("%s%s: %w", walked, "."+name, err)
		}
		value, walked = next, walked+"."+name
	}
	return render(value)
}

// step descends one path segment.
func step(value any, name string) (any, error) {
	switch container := value.(type) {
	case map[string]any:
		member, ok := container[name]
		if !ok {
			return nil, fmt.Errorf("no such member; the object has %s", strings.Join(members(container), ", "))
		}
		return member, nil
	case []any:
		i, err := strconv.Atoi(name)
		if err != nil {
			return nil, fmt.Errorf("an array needs an index, not %q", name)
		}
		if i < 0 || i >= len(container) {
			return nil, fmt.Errorf("index %d is outside the %d-element array", i, len(container))
		}
		return container[i], nil
	default:
		return nil, fmt.Errorf("%s is not an object or an array", kindOf(value))
	}
}

// members lists an object's member names in sorted order.
func members(object map[string]any) []string {
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, strconv.Quote(name))
	}
	slices.Sort(names)
	if len(names) == 0 {
		return []string{"no members"}
	}
	return names
}

// render turns a scalar into the text a script compares against. An object
// or an array is refused: comparing one against a script argument would be
// comparing against a formatting accident.
func render(value any) (string, error) {
	switch v := value.(type) {
	case nil:
		return "null", nil
	case bool:
		return strconv.FormatBool(v), nil
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64), nil
	default:
		return "", fmt.Errorf("the path names %s, which has no single value to compare", kindOf(value))
	}
}

// kindOf names a decoded JSON value for an error message.
func kindOf(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "a boolean"
	case string:
		return "a string"
	case float64:
		return "a number"
	case []any:
		return "an array"
	case map[string]any:
		return "an object"
	default:
		return "an unknown value"
	}
}

// Expand implements `expand <in> <out>`.
func Expand(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("expand: `!` is meaningless here")
	}
	if len(args) != 2 {
		ts.Fatalf("usage: expand <in> <out>")
	}
	out, err := expandVars(ts.ReadFile(args[0]), ts.Getenv)
	if err != nil {
		ts.Fatalf("expand: %s: %v", args[0], err)
	}
	ts.Check(os.WriteFile(ts.MkAbs(args[1]), []byte(out), 0o600))
}

// expandVars is os.Expand with one difference that earns its keep: a
// variable with no value is an error instead of an empty string. Silent
// expansion to nothing is how a fixture ends up asserting nothing.
//
// get is ts.Getenv, which cannot distinguish unset from set-to-empty, so an
// empty value counts as unset.
func expandVars(in string, get func(string) string) (string, error) {
	var missing []string
	out := os.Expand(in, func(name string) string {
		value := get(name)
		if value == "" {
			missing = append(missing, name)
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("$%s is unset or empty", strings.Join(missing, ", $"))
	}
	return out, nil
}

// sleeperCommand is the child `sleeper` starts. 300 seconds outlives any
// script; the process is killed when the script ends, not when it expires.
var sleeperCommand = []string{"sleep", "300"}

// Sleeper implements `sleeper`.
func Sleeper(ts *testscript.TestScript, neg bool, args []string) {
	if neg {
		ts.Fatalf("sleeper: `!` is meaningless here")
	}
	if len(args) != 0 {
		ts.Fatalf("usage: sleeper")
	}
	// testscript hands a builtin no context, so the sleeper gets one whose
	// only cancellation is the script ending: ts.Defer cancels it, which
	// kills the child, and the wait below reaps it.
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, sleeperCommand[0], sleeperCommand[1:]...)
	if err := cmd.Start(); err != nil {
		cancel()
		ts.Fatalf("sleeper: %v", err)
	}
	// Reap it as soon as it dies. An unreaped child stays kill(pid, 0)-alive
	// as a zombie, which is the false positive E0-5 measured at 27.6 s and
	// 59.0 s, and it would make the sleeper useless as a liveness fixture.
	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		_ = cmd.Wait()
	}()
	ts.Defer(func() {
		cancel()
		<-reaped
	})
	ts.Setenv("SLEEPER_PID", strconv.Itoa(cmd.Process.Pid))
}
