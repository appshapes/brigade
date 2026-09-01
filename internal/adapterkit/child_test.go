package adapterkit_test

import (
	"os"
	"testing"

	"github.com/appshapes/brigade/internal/adapterkit"
)

// TestHelperChildEcho is not a test: it is the child half of the
// process-level printer tests. When ADAPTERKIT_CHILD=echo is set, the
// re-executed test binary runs a minimal adapter command — read the stdin
// document, print the envelope — against its REAL stdin and stdout, which
// is what lets the parent assert the stream separation of 7.3 across a
// process boundary.
func TestHelperChildEcho(t *testing.T) {
	if os.Getenv("ADAPTERKIT_CHILD") != "echo" {
		t.Skip("child-process helper; run by the process-level tests")
	}
	doc, err := adapterkit.ReadInput(os.Stdin)
	if err != nil {
		adapterkit.PrintError(err)
		return
	}
	adapterkit.PrintResult(map[string]int{"bytes": len(doc)})
}
