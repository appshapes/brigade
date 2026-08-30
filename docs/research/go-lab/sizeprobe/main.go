// Size probe: hello world + net/http client + github.com/coder/websocket,
// the network stack the shipped brigade binary needs.
package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/coder/websocket"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	fmt.Println("hello from brigade size probe", version)
	if len(os.Args) > 2 && os.Args[1] == "--probe" {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, os.Args[2], nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		fmt.Println("http", resp.StatusCode)
		if len(os.Args) > 3 {
			c, _, err := websocket.Dial(ctx, os.Args[3], nil)
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
				os.Exit(1)
			}
			_ = c.Close(websocket.StatusNormalClosure, "bye")
		}
	}
}
