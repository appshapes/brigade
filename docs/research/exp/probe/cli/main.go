package main

import (
	"encoding/json"
	"fmt"
	"os"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	json.NewEncoder(os.Stdout).Encode(map[string]any{"ok": true, "protocol_version": "1", "version": version})
}
