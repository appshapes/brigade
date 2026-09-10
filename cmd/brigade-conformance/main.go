// Command brigade-conformance runs the BAP/1 conformance suite (C-01..C-44,
// plan 9.2) against any adapter. It is a development binary: `make build`
// produces it and `make test` runs it against the fs adapter, but it is
// never shipped. The suite is internal/conformance; the cases are
// internal/conformance/cases.
package main

import (
	"github.com/appshapes/brigade/internal/conformance"
	"github.com/appshapes/brigade/internal/conformance/cases"
)

func main() { conformance.Main(cases.All()) }
