//go:build !windows

// The stub runs the fs adapter, whose own suite is not run on Windows.

package main

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// helperVar turns this test binary into the command `run` is asked to
// run: TestMain answers for it instead of running the tests.
const helperVar = "BRIGADE_REST_STUB_TEST_HELPER"

func TestMain(m *testing.M) {
	if os.Getenv(helperVar) != "" {
		os.Exit(helper(os.Args[1:]))
	}
	os.Exit(m.Run())
}

// helper is the child: `probe <url>` (or `probe BRIGADE_REST_URL=<url>`)
// asks the server for /v1/info and exits 0 when it answers as the contract
// says; `exit <n>` exits n; `echo` copies stdin to stdout.
func helper(args []string) int {
	switch {
	case len(args) == 2 && args[0] == "probe":
		base := strings.TrimPrefix(args[1], "BRIGADE_REST_URL=")
		//nolint:gosec // G704: the url is the stand-in server's own, handed to this helper by the test
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/v1/info", strings.NewReader("{}"))
		if err != nil {
			return 3
		}
		resp, err := http.DefaultClient.Do(req) //nolint:gosec // G704: as above
		if err != nil {
			return 4
		}
		defer func() { _ = resp.Body.Close() }()
		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK || !bytes.Contains(body, []byte(`"contract_version":"1"`)) {
			return 5
		}
		return 0
	case len(args) == 2 && args[0] == "exit":
		n, _ := strconv.Atoi(args[1])
		return n
	case len(args) == 1 && args[0] == "echo":
		//nolint:forbidigo // the helper IS a process; its stdout is what the test reads
		_, _ = io.Copy(os.Stdout, os.Stdin)
		return 0
	}
	return 9
}

// invoke runs the binary's run with this test binary as the command.
func invoke(t *testing.T, stdin string, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var out, errs bytes.Buffer
	environ := append(os.Environ(), helperVar+"=1")
	code = run(t.Context(), args, strings.NewReader(stdin), &out, &errs, environ)
	return code, out.String(), errs.String()
}

// TestRunsTheCommandAgainstTheServer: the command after `--` runs with
// {url} replaced by the base url of a server that answers the contract,
// its streams are the caller's and its status is the binary's. The
// temporary directory is gone afterwards.
func TestRunsTheCommandAgainstTheServer(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	self := os.Args[0]

	if code, _, stderr := invoke(t, "", "--", self, "probe", "{url}"); code != 0 {
		t.Errorf("probe {url}: exit %d (stderr %s), want 0: the server did not answer /v1/info at the substituted url", code, stderr)
	}
	// {url} inside a longer argument, as `--env BRIGADE_REST_URL={url}` has it.
	if code, _, stderr := invoke(t, "", "--", self, "probe", "BRIGADE_REST_URL={url}"); code != 0 {
		t.Errorf("probe BRIGADE_REST_URL={url}: exit %d (stderr %s), want 0", code, stderr)
	}
	if code, _, _ := invoke(t, "", "--", self, "exit", "7"); code != 7 {
		t.Errorf("exit 7: the binary exited %d, want the command's status", code)
	}
	if code, stdout, _ := invoke(t, "through the pipe", "--", self, "echo"); code != 0 || stdout != "through the pipe" {
		t.Errorf("echo: exit %d stdout %q, want the command's streams to be the caller's", code, stdout)
	}
	if code, _, stderr := invoke(t, "", "--", tmp+"/no-such-command"); code != exitFailure || !strings.Contains(stderr, progName) {
		t.Errorf("a command that cannot start: exit %d stderr %q", code, stderr)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%d entries are left in the temporary directory, want none", len(entries))
	}
}

// TestUsage: anything but nothing or `-- <command>` is a usage error, and
// nothing is started for it.
func TestUsage(t *testing.T) {
	for _, args := range [][]string{{"--"}, {"serve"}, {"-h"}, {"--port", "1"}} {
		code, stdout, stderr := invoke(t, "", args...)
		if code != exitUsage || stdout != "" || !strings.Contains(stderr, "Usage:") {
			t.Errorf("%v: exit %d stdout %q stderr %q, want usage on stderr and exit %d", args, code, stdout, stderr, exitUsage)
		}
	}
}

// TestServesUntilInterrupted: with no command the base url is printed on
// one line, the server answers there, and an interrupt ends it with exit 0
// and nothing left behind.
func TestServesUntilInterrupted(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)
	ctx, interrupt := context.WithCancel(t.Context())
	reader, writer := io.Pipe()
	done := make(chan int, 1)
	go func() {
		done <- run(ctx, nil, strings.NewReader(""), writer, io.Discard, os.Environ())
		_ = writer.Close()
	}()
	line, err := bufio.NewReader(reader).ReadString('\n')
	if err != nil {
		t.Fatalf("no base url on stdout: %v", err)
	}
	base := strings.TrimSuffix(line, "\n")
	if !strings.HasPrefix(base, "http://127.0.0.1:") {
		t.Fatalf("the printed line is %q, want a loopback base url", line)
	}
	if code := helper([]string{"probe", base}); code != 0 {
		t.Errorf("the server at the printed url did not answer /v1/info (helper status %d)", code)
	}
	interrupt()
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d after the interrupt, want 0", code)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the server did not stop after the interrupt")
	}
	if entries, _ := os.ReadDir(tmp); len(entries) != 0 {
		t.Errorf("%d entries are left in the temporary directory, want none", len(entries))
	}
}
