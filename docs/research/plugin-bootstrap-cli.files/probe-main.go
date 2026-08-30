package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
)

func main() {
	cwd, _ := os.Getwd()
	envs := map[string]string{}
	for _, kv := range os.Environ() {
		k, v, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(k, "CLAUDE") || k == "PATH" || strings.HasPrefix(k, "BRIGADE") || k == "HOME" || k == "TMPDIR" || k == "XDG_DATA_HOME" {
			if k == "CLAUDE_CODE_MESSAGING_TOKEN" {
				v = fmt.Sprintf("<redacted len=%d>", len(v))
			}
			envs[k] = v
		}
	}
	keys := make([]string, 0, len(envs))
	for k := range envs {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var stdin string
	if len(os.Args) > 1 && os.Args[1] == "hook" {
		done := make(chan string, 1)
		go func() { b, _ := io.ReadAll(io.LimitReader(os.Stdin, 8192)); done <- string(b) }()
		select {
		case stdin = <-done:
		case <-time.After(2 * time.Second):
			stdin = "<stdin read timed out>"
		}
	}
	out := map[string]any{
		"probe": "brigade-probe", "argv": os.Args, "cwd": cwd, "pid": os.Getpid(), "ppid": os.Getppid(),
		"ts": time.Now().Format(time.RFC3339Nano), "stdin": stdin,
	}
	envo := map[string]string{}
	for _, k := range keys {
		envo[k] = envs[k]
	}
	out["env"] = envo
	j, _ := json.MarshalIndent(out, "", " ")
	fmt.Println(string(j))
	// also append to an evidence file so hook runs (whose stdout is context, not shown) are captured
	if p := os.Getenv("BRIGADE_PROBE_LOG"); p != "" {
		f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err == nil {
			f.Write(j)
			f.Write([]byte("\n"))
			f.Close()
		}
	} else {
		// default evidence path next to the binary's plugin root
		if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" {
			f, err := os.OpenFile(root+"/../evidence/probe-runs.ndjson", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
			if err == nil {
				f.Write(j)
				f.Write([]byte("\n"))
				f.Close()
			}
		}
	}
}
