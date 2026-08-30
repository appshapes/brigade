// Size probe "plus": the sizeprobe imports plus everything else the real
// brigade binary will link: encoding/json/v2, log/slog, os/exec, crypto/sha256,
// regexp, bufio, net (unix sockets), flag.
package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	jsonv2 "encoding/json/v2"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"time"

	"github.com/coder/websocket"
)

var version = "dev"

func main() {
	v := flag.Bool("version", false, "print version")
	flag.Parse()
	if *v {
		fmt.Println(version)
		return
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	log.Info("hello", "version", version)
	b, _ := jsonv2.Marshal(map[string]any{"ok": true, "sha": fmt.Sprintf("%x", sha256.Sum256([]byte(version)))})
	fmt.Println(string(b), regexp.MustCompile(`eyJ`).MatchString(string(b)))
	if len(os.Args) > 2 && os.Args[1] == "--probe" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, os.Args[2], nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, bufio.NewReader(resp.Body))
		_ = resp.Body.Close()
		c, _, err := websocket.Dial(ctx, os.Args[3], nil)
		if err == nil {
			_ = c.Close(websocket.StatusNormalClosure, "bye")
		}
		conn, err := net.Dial("unix", os.Args[4])
		if err == nil {
			_ = conn.Close()
		}
		_ = exec.CommandContext(ctx, "true").Run()
	}
}
