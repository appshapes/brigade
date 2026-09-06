package cli

import (
	"bytes"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	harnesscmd "github.com/appshapes/brigade/internal/harness/commands"
	"github.com/appshapes/brigade/internal/protocol"
)

// poisonArgvValue is the value used wherever a test needs to prove that a
// join secret on argv never reaches stdout or stderr. It is nonsense, not a
// credential, and it is deliberately distinctive so a substring search for
// it cannot match anything else this package prints.
const poisonArgvValue = "brg1.ZZZ-NOT-A-REAL-VALUE-4c1f9a-ZZZ"

// result is one Dispatch run captured whole.
type result struct {
	exit   int
	stdout string
	stderr string
}

// dispatch runs one invocation against in-memory streams.
func dispatch(t *testing.T, args ...string) result {
	t.Helper()
	var out, errb bytes.Buffer
	exit := Dispatch(args, Streams{In: strings.NewReader(""), Out: &out, Err: &errb}, nil)
	return result{exit: exit, stdout: out.String(), stderr: errb.String()}
}

func TestVersionHumanOutputIsTheBareVersion(t *testing.T) {
	t.Parallel()
	// The Makefile and CI assert `bin/brigade version` byte-for-byte, so
	// the human form must carry the version and nothing else.
	got := dispatch(t, "version")
	if got.exit != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", got.exit, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	if strings.Count(got.stdout, "\n") != 1 || !strings.HasSuffix(got.stdout, "\n") {
		t.Errorf("stdout = %q, want exactly one newline-terminated line", got.stdout)
	}
	if strings.TrimSpace(got.stdout) == "" {
		t.Error("stdout carried no version")
	}
}

func TestVersionJSONEnvelope(t *testing.T) {
	t.Parallel()
	got := dispatch(t, "version", "--json")
	if got.exit != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", got.exit, got.stderr)
	}
	if got.stderr != "" {
		t.Errorf("stderr = %q, want empty", got.stderr)
	}
	// Every member carries an explicit tag: encoding/json/v2 matches
	// member names case-sensitively, so an untagged Go field would stay
	// silently zero and the assertions below would test nothing (D16).
	var env struct {
		OK              bool   `json:"ok"`
		ProtocolVersion string `json:"protocol_version"`
		Result          struct {
			Version   string `json:"version"`
			GoVersion string `json:"go_version"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(got.stdout), &env); err != nil {
		t.Fatalf("stdout is not a JSON envelope: %v (%q)", err, got.stdout)
	}
	if !env.OK {
		t.Error("envelope ok = false, want true")
	}
	if env.ProtocolVersion != ProtocolVersion {
		t.Errorf("protocol_version = %q, want %q", env.ProtocolVersion, ProtocolVersion)
	}
	if env.Result.Version == "" || env.Result.GoVersion == "" {
		t.Errorf("result = %+v, want version and go_version populated", env.Result)
	}
}

func TestUnknownCommandIsAUsageError(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"bogus"},
		{"bogus", "--json"},
		{"--json", "bogus"},
	} {
		got := dispatch(t, args...)
		if got.exit != 2 {
			t.Errorf("%v: exit = %d, want 2", args, got.exit)
		}
	}
}

// TestErrorStreamSeparation is the rule the whole design turns on: with
// --json the error object is on stdout and stderr is silent; without it,
// stdout stays completely empty.
func TestErrorStreamSeparation(t *testing.T) {
	t.Parallel()

	human := dispatch(t, "bogus")
	if human.stdout != "" {
		t.Errorf("human error wrote %q to stdout; stdout is protocol output only", human.stdout)
	}
	if !strings.Contains(human.stderr, "unknown command") {
		t.Errorf("human stderr = %q, want the unknown-command line", human.stderr)
	}

	for _, args := range [][]string{{"--json", "bogus"}, {"bogus", "--json"}} {
		machine := dispatch(t, args...)
		if machine.stderr != "" {
			t.Errorf("%v: machine error wrote %q to stderr, want everything on stdout", args, machine.stderr)
		}
		var env struct {
			OK  bool `json:"ok"`
			Err struct {
				Code      Code   `json:"code"`
				Message   string `json:"message"`
				Retryable bool   `json:"retryable"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(machine.stdout), &env); err != nil {
			t.Fatalf("%v: stdout is not a JSON envelope: %v (%q)", args, err, machine.stdout)
		}
		if env.OK {
			t.Errorf("%v: envelope ok = true, want false", args)
		}
		if env.Err.Code != CodeUsage {
			t.Errorf("%v: error.code = %q, want %q", args, env.Err.Code, CodeUsage)
		}
		if env.Err.Retryable {
			t.Errorf("%v: usage reported retryable", args)
		}
	}
}

// TestJoinSecretOnArgvNeverReachesAnyStream is the C-05 property. Every
// spelling of the poison flag, in every position, must produce a usage
// error whose output does not contain the value.
func TestJoinSecretOnArgvNeverReachesAnyStream(t *testing.T) {
	t.Parallel()

	cases := [][]string{
		{"--join-secret", poisonArgvValue, "version"},
		{"-join-secret", poisonArgvValue, "version"},
		{"--join-secret=" + poisonArgvValue, "version"},
		{"-join-secret=" + poisonArgvValue},
		{"version", "--join-secret", poisonArgvValue},
		{"version", "--join-secret=" + poisonArgvValue},
		{"team", "join", "--join-secret", poisonArgvValue},
		{"send", "x", "--", "--join-secret", poisonArgvValue},
		{"--json", "--join-secret", poisonArgvValue, "version"},
		{"hook", "session-start", "--join-secret", poisonArgvValue},
	}
	for _, args := range cases {
		got := dispatch(t, args...)
		if got.exit != 2 {
			t.Errorf("%v: exit = %d, want 2 (usage)", args, got.exit)
		}
		if strings.Contains(got.stdout, poisonArgvValue) {
			t.Errorf("%v: the secret appeared on stdout: %q", args, got.stdout)
		}
		if strings.Contains(got.stderr, poisonArgvValue) {
			t.Errorf("%v: the secret appeared on stderr: %q", args, got.stderr)
		}
		if !strings.Contains(got.stdout+got.stderr, "never be passed on the command line") {
			t.Errorf("%v: no poison-flag explanation in %q / %q", args, got.stdout, got.stderr)
		}
	}
}

// TestNotImplementedCommands pins the P1-1 state: a command that exists in
// the table but has no implementation fails loudly with the plan task, and
// does not exit 0.
func TestNotImplementedCommands(t *testing.T) {
	t.Parallel()
	for _, c := range commands {
		if c.Implemented() {
			continue
		}
		got := dispatch(t, c.Name)
		if got.exit != CodeInternal.Exit() {
			t.Errorf("%s: exit = %d, want %d", c.Name, got.exit, CodeInternal.Exit())
		}
		if !strings.Contains(got.stderr, c.Task) {
			t.Errorf("%s: stderr = %q, want the task %q", c.Name, got.stderr, c.Task)
		}
		if got.stdout != "" {
			t.Errorf("%s: stdout = %q, want empty", c.Name, got.stdout)
		}
	}
}

func TestNotImplementedIsNeverASilentSuccess(t *testing.T) {
	t.Parallel()
	for _, c := range commands {
		if c.Implemented() {
			continue
		}
		if got := dispatch(t, c.Name); got.exit == ExitOK {
			t.Errorf("%s exited 0 while unimplemented", c.Name)
		}
	}
}

// TestMultiCallSeam covers the internal/app branch: hook, watch and adapter
// are recognised entrypoints, not unknown commands, and NotImplemented
// honours --json wherever it appears.
func TestMultiCallSeam(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"hook", "watch", "adapter"} {
		cmd, ok := LookupMultiCall(name)
		if !ok {
			t.Fatalf("LookupMultiCall(%q) = false, want the table entry", name)
		}
		var out, errb bytes.Buffer
		exit := NotImplemented(cmd, []string{name, "whatever", "--json"}, Streams{In: strings.NewReader(""), Out: &out, Err: &errb})
		if exit != CodeInternal.Exit() {
			t.Errorf("%s: exit = %d, want %d", name, exit, CodeInternal.Exit())
		}
		if errb.Len() != 0 {
			t.Errorf("%s: --json wrote %q to stderr", name, errb.String())
		}
		if !strings.Contains(out.String(), cmd.Task) {
			t.Errorf("%s: stdout = %q, want the task %q", name, out.String(), cmd.Task)
		}
	}

	for _, name := range []string{"version", "help", "sessions", "nope"} {
		if _, ok := LookupMultiCall(name); ok {
			t.Errorf("LookupMultiCall(%q) = true, want false", name)
		}
	}
}

func TestHelp(t *testing.T) {
	t.Parallel()

	plain := dispatch(t, "help")
	if plain.exit != ExitOK {
		t.Fatalf("exit = %d, want 0", plain.exit)
	}
	if plain.stderr != "" {
		t.Errorf("stderr = %q, want empty", plain.stderr)
	}
	for _, want := range []string{"version", "send", "Global flags"} {
		if !strings.Contains(plain.stdout, want) {
			t.Errorf("`help` output is missing %q:\n%s", want, plain.stdout)
		}
	}
	for _, hidden := range []string{"hook session-start", "watch [--sink"} {
		if strings.Contains(plain.stdout, hidden) {
			t.Errorf("`help` leaked the hidden entry %q", hidden)
		}
	}

	all := dispatch(t, "help", "--all")
	if all.exit != ExitOK {
		t.Fatalf("`help --all` exit = %d, want 0", all.exit)
	}
	for _, want := range []string{"hook session-start", "watch [--sink", "adapter <name>"} {
		if !strings.Contains(all.stdout, want) {
			t.Errorf("`help --all` is missing %q:\n%s", want, all.stdout)
		}
	}

	one := dispatch(t, "help", "version")
	if one.exit != ExitOK || !strings.Contains(one.stdout, "Usage: brigade version") {
		t.Errorf("`help version` = %d %q", one.exit, one.stdout)
	}

	bad := dispatch(t, "help", "nosuch")
	if bad.exit != 2 {
		t.Errorf("`help nosuch` exit = %d, want 2", bad.exit)
	}

	toomany := dispatch(t, "help", "version", "send")
	if toomany.exit != 2 {
		t.Errorf("`help version send` exit = %d, want 2", toomany.exit)
	}
}

// TestHelpFlagPrintsUsageAndExitsZero covers the flag.ErrHelp arm of 7.3.
func TestHelpFlagPrintsUsageAndExitsZero(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"-h"}, {"--help"}, {"version", "-h"}, {"version", "--help"}} {
		got := dispatch(t, args...)
		if got.exit != ExitOK {
			t.Errorf("%v: exit = %d, want 0", args, got.exit)
		}
		if !strings.Contains(got.stdout, "Usage: brigade") {
			t.Errorf("%v: stdout = %q, want a usage block", args, got.stdout)
		}
	}

	// A machine caller must not get raw usage TEXT on stdout -- stdout carries
	// protocol output only. It must not get an EMPTY stdout either, or success
	// and a silent failure are indistinguishable at exit 0. Both hold when the
	// usage block travels inside a 4.3 success envelope, which is what stdout
	// carries here; TestJSONHelpEmitsAnEnvelope asserts the shape.
	machine := dispatch(t, "--json", "-h")
	if machine.exit != ExitOK {
		t.Errorf("--json -h exit = %d, want 0", machine.exit)
	}
	if machine.stderr != "" {
		t.Errorf("--json -h stderr = %q, want empty", machine.stderr)
	}
	if strings.HasPrefix(machine.stdout, "Usage: brigade") {
		t.Errorf("--json -h wrote raw usage text to stdout: %q", machine.stdout)
	}
	var env struct {
		OK     bool `json:"ok"`
		Result struct {
			Usage string `json:"usage"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(machine.stdout), &env); err != nil {
		t.Errorf("--json -h stdout %q is not one JSON document: %v", machine.stdout, err)
	} else if !env.OK || !strings.Contains(env.Result.Usage, "Usage: brigade") {
		t.Errorf("--json -h envelope = %+v, want ok=true with the usage block in result.usage", env)
	}
}

func TestUsageErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string // substring of the combined output
	}{
		{"no command", nil, "no command given"},
		{"unknown command", []string{"bogus"}, "unknown command"},
		{"unknown flag", []string{"version", "--nope"}, "not defined"},
		{"unknown global flag", []string{"--nope", "version"}, "not defined"},
		{"version takes no arguments", []string{"version", "extra"}, "takes no arguments"},
		{"bad log level", []string{"--log-level", "shout", "version"}, "unknown --log-level"},
		{"bad log level after command", []string{"version", "--log-level=shout"}, "unknown --log-level"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := dispatch(t, tc.args...)
			if got.exit != 2 {
				t.Errorf("exit = %d, want 2 (stdout %q, stderr %q)", got.exit, got.stdout, got.stderr)
			}
			if !strings.Contains(got.stdout+got.stderr, tc.want) {
				t.Errorf("output %q / %q does not mention %q", got.stdout, got.stderr, tc.want)
			}
		})
	}
}

func TestValidLogLevelsAreAccepted(t *testing.T) {
	t.Parallel()
	for _, level := range LogLevels {
		if got := dispatch(t, "--log-level", level, "version"); got.exit != ExitOK {
			t.Errorf("--log-level %s: exit = %d, want 0 (stderr %q)", level, got.exit, got.stderr)
		}
	}
}

func TestExitCodeTable(t *testing.T) {
	t.Parallel()
	// Plan 4.6, copied from the table rather than from the code.
	want := map[Code]int{
		CodeInternal: 1, CodeUsage: 2, CodeInvalidInput: 3, CodeUnauthenticated: 4,
		CodeUnauthorized: 5, CodeNotFound: 6, CodeConflict: 7, CodeRateLimited: 8,
		CodeUnavailable: 9, CodeProtocolMismatch: 10, CodeConfig: 11, CodeLoopDetected: 12,
	}
	for code, exit := range want {
		if got := code.Exit(); got != exit {
			t.Errorf("%s.Exit() = %d, want %d", code, got, exit)
		}
	}
	// An unmapped code must never look like success.
	if got := Code("something-new").Exit(); got == 0 {
		t.Error("an unknown code mapped to exit 0")
	}
	// Only rate_limited and unavailable are retryable (4.6).
	for code, exit := range want {
		wantRetry := code == CodeRateLimited || code == CodeUnavailable
		if got := code.Retryable(); got != wantRetry {
			t.Errorf("%s(exit %d).Retryable() = %v, want %v", code, exit, got, wantRetry)
		}
	}
}

func TestGetenvReadsTheGivenEnvironment(t *testing.T) {
	t.Parallel()
	cx := &Context{Environ: []string{"A=1", "B=2", "A=3", "EMPTY="}}
	for _, tc := range []struct{ name, want string }{
		{"A", "3"}, // last occurrence wins (os/exec dedupEnv semantics, NOT os.Getenv, which takes the first)
		{"B", "2"},
		{"EMPTY", ""},
		{"MISSING", ""},
	} {
		if got := cx.Getenv(tc.name); got != tc.want {
			t.Errorf("Getenv(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestParseInterspersed covers the property stdlib flag does not have:
// flags may follow positional arguments (7.3).
func TestParseInterspersed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		args    []string
		wantPos []string
		wantS   string
		wantB   bool
		wantErr bool
	}{
		{"flags first", []string{"-s", "x", "-b", "a", "c"}, []string{"a", "c"}, "x", true, false},
		{"flag after positional", []string{"a", "-s", "x"}, []string{"a"}, "x", false, false},
		{"flags around positionals", []string{"a", "-s", "x", "b", "-b"}, []string{"a", "b"}, "x", true, false},
		{"double dash makes everything positional", []string{"a", "--", "-s", "-b"}, []string{"a", "-s", "-b"}, "", false, false},
		{"no arguments", nil, nil, "", false, false},
		{"unknown flag", []string{"a", "--nope"}, nil, "", false, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fs := newFlagSet("t")
			s := fs.String("s", "", "")
			b := fs.Bool("b", false, "")
			pos, err := parseInterspersed(fs, tc.args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("err = nil, want a parse error")
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v", err)
			}
			if strings.Join(pos, ",") != strings.Join(tc.wantPos, ",") {
				t.Errorf("positional = %v, want %v", pos, tc.wantPos)
			}
			if *s != tc.wantS {
				t.Errorf("-s = %q, want %q", *s, tc.wantS)
			}
			if *b != tc.wantB {
				t.Errorf("-b = %v, want %v", *b, tc.wantB)
			}
		})
	}
}

// TestFlagSetsAreSilent guards the 7.3 rule that no flag set may write to a
// stream of its own: a flag value can be a secret.
func TestFlagSetsAreSilent(t *testing.T) {
	t.Parallel()
	fs := newFlagSet("t")
	if fs.Output() != io.Discard {
		t.Error("flag set output is not io.Discard")
	}
	if _, err := parseInterspersed(fs, []string{"--nope"}); err == nil {
		t.Error("parse of an undefined flag returned no error")
	}
}

// TestCommandTableIsWellFormed catches the table mistakes that would
// otherwise show up only as a confusing runtime failure.
func TestCommandTableIsWellFormed(t *testing.T) {
	t.Parallel()
	seen := map[string]bool{}
	for _, c := range commands {
		if c.Name == "" {
			t.Error("a table entry has no name")
		}
		if seen[c.Name] {
			t.Errorf("%s: duplicate table entry", c.Name)
		}
		seen[c.Name] = true
		if c.Summary == "" {
			t.Errorf("%s: no summary", c.Name)
		}
		if c.Implemented() == (c.Task != "") {
			t.Errorf("%s: Run and Task must be exactly one of the two (Run set: %v, Task %q)",
				c.Name, c.Implemented(), c.Task)
		}
		if c.MultiCall && !c.Hidden {
			t.Errorf("%s: multi-call entrypoints are hidden from `help`", c.Name)
		}
	}
	for _, want := range []string{"version", "help", "sessions", "send", "whoami", "team", "profile", "inbox", "hook", "watch", "adapter"} {
		if !seen[want] {
			t.Errorf("the 6.4 command %q is missing from the table", want)
		}
	}
}

// TestLookupIsExactNotAPrefix pins the front door of the whole binary.
//
// A Lookup that matched by prefix instead of by name — `c.Name[:len(name)]
// == name` in place of `c.Name == name` — passed every other test in the
// repository, and it would silently turn `brigade t` into `brigade team` and
// `brigade s` into whichever of `sessions` and `send` came first in the
// table. Abbreviation is a feature this binary does not have, and a model
// calling a command it only half-typed must be told so.
func TestLookupIsExactNotAPrefix(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"version", "help", "sessions", "send", "team", "hook", "watch", "adapter"} {
		cmd, ok := Lookup(name)
		if !ok {
			t.Errorf("Lookup(%q) = false, want the table entry", name)
			continue
		}
		if cmd.Name != name {
			t.Errorf("Lookup(%q) returned the entry for %q", name, cmd.Name)
		}
	}

	// Prefixes, suffixes and case variants of real command names are not
	// commands.
	for _, name := range []string{
		"v", "ver", "versio", "versions", "version ", " version",
		"s", "se", "sess", "t", "te", "h", "ho", "a", "w",
		"Version", "VERSION", "Help", "",
	} {
		if cmd, ok := Lookup(name); ok {
			t.Errorf("Lookup(%q) resolved to the command %q; the table is matched exactly", name, cmd.Name)
		}
		if got := dispatch(t, name); got.exit != 2 {
			t.Errorf("`brigade %q` exit = %d, want 2 (stdout %q, stderr %q)", name, got.exit, got.stdout, got.stderr)
		}
	}
}

// TestAsErrorClassifiesAnUnknownFailure covers the arm 4.6 turns on: a
// command that returns a plain error has not said what went wrong, and an
// unclassified bug must become `internal` (exit 1) rather than anything a
// caller would read as its own mistake — or, worse, as success.
//
// No table entry returns a bare error today, so this is the only thing
// exercising the branch: with CodeInternal replaced by CodeUsage the whole
// repository stayed green.
func TestAsErrorClassifiesAnUnknownFailure(t *testing.T) {
	t.Parallel()

	plain := asError("send", errors.New("something went wrong"))
	if plain.Code != CodeInternal {
		t.Errorf("asError(plain).Code = %q, want %q", plain.Code, CodeInternal)
	}
	if plain.Code.Exit() == ExitOK {
		t.Error("an unclassified command failure mapped to exit 0")
	}
	if plain.Command != "send" {
		t.Errorf("asError(plain).Command = %q, want %q", plain.Command, "send")
	}
	if plain.Message != "something went wrong" {
		t.Errorf("asError(plain).Message = %q", plain.Message)
	}

	// A command that already knows its code keeps it, and keeps its own
	// command name when it set one.
	classified := asError("send", &Error{Code: CodeNotFound, Message: "no such session"})
	if classified.Code != CodeNotFound {
		t.Errorf("asError(*Error).Code = %q, want %q", classified.Code, CodeNotFound)
	}
	if classified.Command != "send" {
		t.Errorf("asError(*Error).Command = %q, want the dispatching command", classified.Command)
	}
	named := asError("send", &Error{Code: CodeNotFound, Message: "no such session", Command: "team"})
	if named.Command != "team" {
		t.Errorf("asError overwrote the error's own command with %q", named.Command)
	}

	// A wrapped *Error is still found: errors.As, not a type assertion.
	wrapped := asError("send", fmt.Errorf("sending: %w", &Error{Code: CodeRateLimited, Message: "slow down"}))
	if wrapped.Code != CodeRateLimited {
		t.Errorf("asError(wrapped).Code = %q, want %q", wrapped.Code, CodeRateLimited)
	}
}

// probeCommand is a table entry installed for the duration of one test. It
// exists because nothing else can observe the environment reaching a
// command: at P1-1 neither `version` nor `help` reads it, so Dispatch could
// throw its environ away and every other test would pass. 3.2 makes that
// seam load-bearing — a command's configuration comes from the environment
// it was handed, never from os.Getenv — so it is asserted here rather than
// left until a command needs it.
const probeCommandName = "test-probe-environ"

// TestDispatchHandsTheEnvironmentToTheCommandSerial must not run in
// parallel: it installs a table entry, and `commands` is read by the
// parallel tests in this file. Go runs the sequential top-level tests to
// completion before releasing any paused parallel one, which is the same
// guarantee t.Setenv relies on in internal/testutil.
func TestDispatchHandsTheEnvironmentToTheCommandSerial(t *testing.T) {
	var got *Context
	restore := commands
	commands = append(append([]Command{}, commands...), Command{
		Name:    probeCommandName,
		Summary: "test-only: capture the context Dispatch built",
		Hidden:  true,
		Run: func(cx *Context, _ []string) error {
			got = cx
			return nil
		},
	})
	t.Cleanup(func() { commands = restore })

	environ := []string{"BRIGADE_PROFILE=alpha", "CLAUDE_CONFIG_DIR=/somewhere/private", "BRIGADE_PROFILE=beta"}
	var out, errb bytes.Buffer
	exit := Dispatch([]string{probeCommandName}, Streams{In: strings.NewReader(""), Out: &out, Err: &errb}, environ)
	if exit != ExitOK {
		t.Fatalf("exit = %d, want 0 (stderr %q)", exit, errb.String())
	}
	if got == nil {
		t.Fatal("the probe command never ran")
	}
	if len(got.Environ) != len(environ) {
		t.Fatalf("cx.Environ = %q, want the environ Dispatch was given (%q)", got.Environ, environ)
	}
	for i := range environ {
		if got.Environ[i] != environ[i] {
			t.Errorf("cx.Environ[%d] = %q, want %q", i, got.Environ[i], environ[i])
		}
	}
	// Read through the accessor too, so the whole path from Dispatch's
	// argument to the value a command sees is covered.
	if v := got.Getenv("BRIGADE_PROFILE"); v != "beta" {
		t.Errorf("cx.Getenv(\"BRIGADE_PROFILE\") = %q, want %q (last occurrence wins)", v, "beta")
	}
	if v := got.Getenv("CLAUDE_CONFIG_DIR"); v != "/somewhere/private" {
		t.Errorf("cx.Getenv(\"CLAUDE_CONFIG_DIR\") = %q", v)
	}
	if v := got.Getenv("NOT_IN_THE_SLICE"); v != "" {
		t.Errorf("cx.Getenv of an absent name = %q, want empty", v)
	}
}

// TestJSONSurvivesAFlagParseError pins the rule that --json selects the output
// STREAM and must therefore be honoured even when the parse that would have set
// it never runs. stdlib flag aborts at the first offending argument, so in
// `brigade version --bad-flag --json` the flag set never reaches --json; before
// this was fixed a machine caller got a human line on stderr and a completely
// empty stdout instead of the 4.3 envelope it asked for.
func TestJSONSurvivesAFlagParseError(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"version", "--bad-flag", "--json"}, // --json AFTER the offending flag
		{"version", "--json", "--bad-flag"}, // and before it
		{"--json", "version", "--bad-flag"}, // as a global
		{"--bad-global", "--json"},          // pass one, before any command word
		{"no-such-command", "--json"},       // the branch that already worked
	} {
		got := dispatch(t, args...)
		if got.exit != CodeUsage.Exit() {
			t.Errorf("%v: exit = %d, want %d", args, got.exit, CodeUsage.Exit())
		}
		if got.stdout == "" {
			t.Errorf("%v: stdout empty; --json must put the envelope on stdout", args)
			continue
		}
		var env struct {
			OK              bool   `json:"ok"`
			ProtocolVersion string `json:"protocol_version"`
			Err             struct {
				Code string `json:"code"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(got.stdout), &env); err != nil {
			t.Errorf("%v: stdout %q is not one JSON document: %v", args, got.stdout, err)
			continue
		}
		if env.OK || env.Err.Code != string(CodeUsage) || env.ProtocolVersion != ProtocolVersion {
			t.Errorf("%v: envelope = %+v, want ok=false code=%q protocol_version=%q",
				args, env, CodeUsage, ProtocolVersion)
		}
	}
}

// TestJSONAfterDoubleDashIsPositional guards the other direction: everything
// after a bare "--" is an argument, not a flag, so it must NOT switch the
// stream. Without this the scan would be a grep for the string anywhere.
func TestJSONAfterDoubleDashIsPositional(t *testing.T) {
	t.Parallel()
	got := dispatch(t, "version", "--bad-flag", "--", "--json")
	if got.exit != CodeUsage.Exit() {
		t.Fatalf("exit = %d, want %d", got.exit, CodeUsage.Exit())
	}
	if got.stdout != "" {
		t.Errorf("stdout = %q, want empty: --json after -- is positional", got.stdout)
	}
	if got.stderr == "" {
		t.Error("stderr empty; the human error line is the output in this mode")
	}
}

// TestJSONHelpEmitsAnEnvelope pins that a machine caller asking for help gets
// one JSON document on stdout rather than an empty stream. Human mode keeps the
// usage block on stdout (7.3); --json mode must not answer with nothing, or
// success and a silent failure look identical.
func TestJSONHelpEmitsAnEnvelope(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"--json", "help"},
		{"--json", "-h"},
		{"--json", "help", "version"},
	} {
		got := dispatch(t, args...)
		if got.exit != ExitOK {
			t.Errorf("%v: exit = %d, want 0 (stderr %q)", args, got.exit, got.stderr)
		}
		var env struct {
			OK     bool `json:"ok"`
			Result struct {
				Usage string `json:"usage"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(got.stdout), &env); err != nil {
			t.Errorf("%v: stdout %q is not one JSON document: %v", args, got.stdout, err)
			continue
		}
		if !env.OK || !strings.Contains(env.Result.Usage, "Usage:") {
			t.Errorf("%v: envelope = %+v, want ok=true with the usage block in result.usage", args, env)
		}
	}
}

// --- P3-3: the filled table, the raw dispatch and the error mapping ---------

// TestFilledCommandsAreNoLongerPlaceholders pins the table after P5-9:
// every 6.4 human command has a Run — `inbox` was the last placeholder —
// no entry carries a Task, and `help` lists the six under "Commands" with
// no "Not implemented yet" block at all.
func TestFilledCommandsAreNoLongerPlaceholders(t *testing.T) {
	t.Parallel()
	for _, c := range commands {
		if !c.Implemented() || c.Task != "" {
			t.Errorf("%s: still a placeholder (Task %q)", c.Name, c.Task)
		}
	}
	for _, raw := range []string{"team", "profile"} {
		if cmd, _ := Lookup(raw); !cmd.Raw {
			t.Errorf("%s: not Raw; the adapter flags it forwards would be usage errors", raw)
		}
	}
	for _, typed := range []string{"sessions", "send", "whoami", "inbox"} {
		if cmd, _ := Lookup(typed); cmd.Raw {
			t.Errorf("%s: Raw; its flags are the table's", typed)
		}
	}
	got := dispatch(t, "help")
	if strings.Contains(got.stdout, "Not implemented yet") {
		t.Errorf("help still carries a placeholder block:\n%s", got.stdout)
	}
	commandsBlock := got.stdout[strings.Index(got.stdout, "Commands:"):strings.Index(got.stdout, "Global flags:")]
	for _, want := range []string{"sessions [--all]", "send <session_id>", "whoami", "team create|join|leave|members|status|reset|revoke-credentials|list|rotate-secret|revoke-member|transfer", "profile init|status", "inbox [release]"} {
		if !strings.Contains(commandsBlock, want) {
			t.Errorf("help's Commands block lacks %q:\n%s", want, commandsBlock)
		}
	}
}

// TestRawDispatchForwardsEverythingAfterTheCommandWord installs a raw probe
// and proves the raw contract: no second parse, so an unknown flag reaches
// Run instead of being usage; --json after the command word selects the
// error stream; -h as the first raw argument prints usage; the poison scan
// still runs first.
func TestRawDispatchForwardsEverythingAfterTheCommandWordSerial(t *testing.T) {
	var got []string
	var gotJSON bool
	restore := commands
	commands = append(append([]Command{}, commands...), Command{
		Name:    "test-probe-raw",
		Summary: "test-only: capture the raw arguments",
		Hidden:  true,
		Raw:     true,
		Run: func(cx *Context, args []string) error {
			got, gotJSON = args, cx.JSON
			return &protocol.Error{Code: protocol.CodeNotFound, Message: "probe", Details: map[string]string{"reason": "probe"}}
		},
	})
	t.Cleanup(func() { commands = restore })

	res := dispatch(t, "test-probe-raw", "join", "--prompt", "--nope", "--", "--json")
	if res.exit != CodeNotFound.Exit() {
		t.Fatalf("exit = %d, want 6 (stderr %q)", res.exit, res.stderr)
	}
	want := []string{"join", "--prompt", "--nope", "--", "--json"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Errorf("raw args = %v, want %v", got, want)
	}
	if gotJSON {
		t.Error("--json after -- switched the stream; it is positional there")
	}
	if !strings.Contains(res.stderr, "brigade test-probe-raw failed (not_found): probe") || res.stdout != "" {
		t.Errorf("stdout %q stderr %q", res.stdout, res.stderr)
	}

	// --json after the command word is honoured for the error stream, and
	// still reaches Run (the command consumes it itself).
	res = dispatch(t, "test-probe-raw", "join", "--json")
	if !gotJSON || res.stderr != "" || !strings.Contains(res.stdout, `"code":"not_found"`) {
		t.Errorf("--json: cx.JSON %v stdout %q stderr %q", gotJSON, res.stdout, res.stderr)
	}
	if !strings.Contains(res.stdout, `"reason":"probe"`) {
		t.Errorf("the protocol error's details were dropped: %s", res.stdout)
	}

	// -h first prints the command's usage and never reaches Run.
	got = nil
	res = dispatch(t, "test-probe-raw", "-h")
	if res.exit != ExitOK || !strings.Contains(res.stdout, "Usage: brigade test-probe-raw") || got != nil {
		t.Errorf("-h: exit %d stdout %q args %v", res.exit, res.stdout, got)
	}

	// The poison scan runs before the raw hand-off.
	got = nil
	res = dispatch(t, "test-probe-raw", "join", "--join-secret", poisonArgvValue)
	if res.exit != 2 || got != nil || strings.Contains(res.stderr, poisonArgvValue) {
		t.Errorf("poison: exit %d args %v stderr %q", res.exit, got, res.stderr)
	}
}

// TestAsErrorMapsProtocolErrors: a *protocol.Error keeps its code, message,
// retry-after and details, so the harness library's failures reach the
// stderr line and the envelope unchanged.
func TestAsErrorMapsProtocolErrors(t *testing.T) {
	t.Parallel()
	perr := &protocol.Error{Code: protocol.CodeRateLimited, Message: "slow down", RetryAfterMS: 1500, Details: map[string]string{"reason": "x"}}
	got := asError("send", perr)
	if got.Code != CodeRateLimited || got.Message != "slow down" || got.RetryAfterMS != 1500 || got.Details["reason"] != "x" || got.Command != "send" {
		t.Errorf("asError(protocol) = %+v", got)
	}
	wrapped := asError("send", fmt.Errorf("sending: %w", perr))
	if wrapped.Code != CodeRateLimited {
		t.Errorf("a wrapped *protocol.Error lost its code: %+v", wrapped)
	}
	var out, errb bytes.Buffer
	exit := report(Streams{Out: &out, Err: &errb}, true, got)
	if exit != 8 || !strings.Contains(out.String(), `"retry_after_ms":1500`) {
		t.Errorf("report = %d %s", exit, out.String())
	}
}

// TestExitStatusIsForwardedSilently: a pass-through's ExitStatus becomes
// the process exit with nothing printed by the dispatcher.
func TestExitStatusIsForwardedSilentlySerial(t *testing.T) {
	restore := commands
	commands = append(append([]Command{}, commands...), Command{
		Name:    "test-probe-exit",
		Summary: "test-only: forward an adapter exit status",
		Hidden:  true,
		Raw:     true,
		Run:     func(*Context, []string) error { return harnesscmd.ExitStatus(7) },
	})
	t.Cleanup(func() { commands = restore })
	res := dispatch(t, "test-probe-exit", "anything")
	if res.exit != 7 || res.stdout != "" || res.stderr != "" {
		t.Errorf("exit %d stdout %q stderr %q, want 7 and silence", res.exit, res.stdout, res.stderr)
	}
}

// TestSessionBoundCommandsOutsideASessionFailCleanly: with an empty
// environment the five commands report a classified failure — never a
// panic, never exit 0, and never a raw Go error as `internal`.
func TestSessionBoundCommandsOutsideASessionFailCleanly(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"sessions"}, {"send", "x"}, {"whoami"}, {"team", "members"}, {"profile", "status"},
	} {
		got := dispatch(t, args...)
		if got.exit == ExitOK || got.exit == CodeInternal.Exit() {
			t.Errorf("%v: exit = %d (stderr %q)", args, got.exit, got.stderr)
		}
		if got.stdout != "" {
			t.Errorf("%v: stdout = %q, want empty", args, got.stdout)
		}
	}
}
