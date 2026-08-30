// Command mini is a stand-in for the brigade multi-call binary: JSON result envelope on stdout,
// diagnostics on stderr, protocol exit codes.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
)

func main() { os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr)) }

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	enc := json.NewEncoder(stdout)
	if len(args) == 0 {
		enc.Encode(map[string]any{"ok": false, "protocol_version": "1", "error": map[string]any{"code": "usage", "message": "missing command", "retryable": false}})
		return 2
	}
	switch args[0] {
	case "describe":
		enc.Encode(map[string]any{"ok": true, "protocol_version": "1", "result": map[string]any{"adapter": map[string]any{"name": "mini", "version": "0.0.1"}, "profile": map[string]any{"state": "unconfigured"}}})
		return 0
	case "echo":
		raw, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil || !json.Valid(raw) {
			enc.Encode(map[string]any{"ok": false, "protocol_version": "1", "error": map[string]any{"code": "invalid_input", "message": "stdin is not JSON", "retryable": false}})
			return 3
		}
		fmt.Fprint(stdout, string(raw))
		return 0
	case "spawn":
		// the harness spawns the adapter as an argv array; a bare name is resolved on PATH by os/exec
		c := exec.Command("helper", "describe")
		c.Stdout, c.Stderr = stdout, stderr
		if err := c.Run(); err != nil {
			fmt.Fprintln(stderr, "spawn:", err)
			return 9
		}
		return 0
	case "send":
		fmt.Fprintln(stderr, `{"level":"debug","event":"send","note":"diagnostics only on stderr"}`)
		enc.Encode(map[string]any{"ok": false, "protocol_version": "1", "error": map[string]any{"code": "not_found", "message": "no such session", "retryable": false}})
		return 6
	default:
		enc.Encode(map[string]any{"ok": false, "protocol_version": "1", "error": map[string]any{"code": "usage", "message": "unknown command " + args[0], "retryable": false}})
		return 2
	}
}
