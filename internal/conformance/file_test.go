package conformance

import "os"

// writeFile and readFile are the tests' tiny file helpers (0600, as
// every state file in this repository).
func writeFile(path, content string) error { return os.WriteFile(path, []byte(content), 0o600) }

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}
