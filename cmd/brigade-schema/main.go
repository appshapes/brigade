// Command brigade-schema writes the Brigade Adapter Protocol JSON Schema
// document to stdout. `make schema` redirects it into
// docs/protocol-v1.schema.json and `make schema-check` diffs the two, so
// its output must be byte-identical between runs and between machines;
// internal/protocol/schema owns that guarantee (sorted $defs, ordered
// members) and TestDocumentStable pins it.
//
// The command is DEV-ONLY and never shipped: it may link
// invopop/jsonschema, which must stay out of bin/brigade (`make
// deps-check` asserts the shipped binary's module set).
package main

import (
	"io"
	"os"

	"github.com/appshapes/brigade/internal/protocol/schema"
)

func main() { os.Exit(run(os.Stdout, os.Stderr)) }

func run(stdout, stderr io.Writer) int {
	b, err := schema.Document()
	if err != nil {
		_, _ = io.WriteString(stderr, "brigade-schema failed (internal): "+err.Error()+"\n")
		return 1
	}
	if _, err := stdout.Write(b); err != nil {
		_, _ = io.WriteString(stderr, "brigade-schema failed (internal): "+err.Error()+"\n")
		return 1
	}
	return 0
}
