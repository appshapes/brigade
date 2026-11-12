// Command brigade-rest-stub runs the stand-in Brigade backend of
// internal/adapters/rest/stub: a server for the backend REST contract
// (docs/backend-rest-v1.md) that answers every request by running the
// filesystem adapter in-process. It is INSECURE and TEST-ONLY, and it is a
// development binary: `make build` produces it, `make test` runs the
// conformance suite through it against the bundled `rest` adapter, and
// nothing ships it.
//
//	brigade-rest-stub
//	    serves on 127.0.0.1, on a port the system picks, prints the base
//	    url on one line and serves until it is interrupted.
//
//	brigade-rest-stub -- <command> [args…]
//	    serves the same way, runs the command with every `{url}` in its
//	    arguments replaced by the base url, and exits with the command's
//	    status. The command is run as an argument array, never through a
//	    shell.
//
// Either way the server keeps its files in a temporary directory that is
// removed on exit.
package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/appshapes/brigade/internal/adapters/rest/stub"
)

const progName = "brigade-rest-stub"

// urlPlaceholder is what a command's arguments carry where the base url
// belongs.
const urlPlaceholder = "{url}"

const usage = `Usage: ` + progName + ` [-- <command> [args...]]

Without a command: print the stand-in server's base url on one line and
serve until interrupted. With one: run it with every {url} in its
arguments replaced by the base url, and exit with its status.
`

// The exit statuses of the binary's own failures; a command's status is
// passed on as it is.
const (
	exitFailure = 1
	exitUsage   = 2
)

// main is the only place this dev binary names the process streams and the
// only one that exits.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Environ())
	stop()
	os.Exit(code)
}

// run starts the server, then serves until ctx ends or runs the command
// after `--`. It returns the process exit status.
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer, environ []string) int {
	var command []string
	switch {
	case len(args) == 0:
	case args[0] == "--" && len(args) > 1:
		command = args[1:]
	default:
		_, _ = io.WriteString(stderr, usage)
		return exitUsage
	}

	dir, err := os.MkdirTemp("", progName+"-")
	if err != nil {
		return fail(stderr, "temporary directory", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	server, err := stub.New(dir, stub.Options{})
	if err != nil {
		return fail(stderr, "server", err)
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return fail(stderr, "listen", err)
	}
	// No write timeout: a receive of the contract may be held for 30 s.
	httpServer := &http.Server{Handler: server, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- httpServer.Serve(listener) }()
	defer func() { _ = httpServer.Close() }()
	base := "http://" + listener.Addr().String()

	if command == nil {
		_, _ = io.WriteString(stdout, base+"\n")
		select {
		case <-ctx.Done():
			return 0
		case err := <-served:
			return fail(stderr, "serve", err)
		}
	}
	for i := range command {
		command[i] = strings.ReplaceAll(command[i], urlPlaceholder, base)
	}
	//nolint:gosec // G204/G702: the argv after `--` is the developer's own command line, run as an array; no shell is involved
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr, cmd.Env = stdin, stdout, stderr, environ
	cmd.WaitDelay = 5 * time.Second
	err = cmd.Run()
	var exited *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &exited) && exited.ExitCode() >= 0:
		return exited.ExitCode()
	default:
		return fail(stderr, "command", err)
	}
}

// fail reports one of the binary's own failures on stderr.
func fail(stderr io.Writer, what string, err error) int {
	_, _ = io.WriteString(stderr, progName+": "+what+": "+err.Error()+"\n")
	return exitFailure
}
