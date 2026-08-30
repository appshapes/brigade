// Baseline: hello world with stdlib flag only.
package main

import (
	"flag"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	fs := flag.NewFlagSet("brigade", flag.ContinueOnError)
	replyTo := fs.String("reply-to", "", "message id being answered")
	jsonOut := fs.Bool("json", false, "machine-readable output")
	if err := fs.Parse(os.Args[1:]); err != nil {
		os.Exit(2)
	}
	fmt.Println("hello", version, *replyTo, *jsonOut, fs.Args())
}
