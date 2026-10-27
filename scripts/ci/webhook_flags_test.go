// The webhook gateway's flag table (card 147). docs/project-owner/configure/webhook-gateway.md carries
// brigade-webhook-gateway's flags in a hand-written table. brigade's own commands and the make targets are
// generated from the code and compared by docs-check; the gateway is a second operator binary with no generator,
// so this test is what keeps its table honest: every flag the binary defines is a row of the table, and every flag
// the table names exists. The binary's list is read the way an operator reads it, from `--help`, so nothing in the
// package is exported for the test's sake.
package ci_test

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/appshapes/brigade/internal/gateway/webhook"
	"github.com/appshapes/brigade/internal/testutil"
)

const webhookGatewayPageRel = "docs/project-owner/configure/webhook-gateway.md"

// webhookHelpFlag matches one definition line of flag.PrintDefaults: two spaces, a dash, the name, then the type
// or the end of the line (a boolean flag prints no type). The usage text above the definitions indents its command
// lines by two spaces too, but none of them starts with a dash.
var webhookHelpFlag = regexp.MustCompile(`(?m)^  -([a-z][a-z0-9-]*)(?: |$)`)

// webhookTableFlag matches every `--name` in a table cell; a row may carry two (`--lease`, `--poll`).
var webhookTableFlag = regexp.MustCompile(`--([a-z][a-z0-9-]*)`)

func TestWebhookGatewayFlagTableMatchesTheBinary(t *testing.T) {
	t.Parallel()
	var out, errb bytes.Buffer
	if code := webhook.Run(t.Context(), []string{"--help"}, strings.NewReader(""), &out, &errb, nil); code != 0 {
		t.Fatalf("brigade-webhook-gateway --help = %d, stderr %q", code, errb.String())
	}
	binary := map[string]bool{}
	for _, m := range webhookHelpFlag.FindAllStringSubmatch(errb.String(), -1) {
		binary[m[1]] = true
	}
	if len(binary) < 10 {
		t.Fatalf("--help lists only %d flags, which cannot be the whole set:\n%s", len(binary), errb.String())
	}
	page, err := os.ReadFile(filepath.Join(testutil.RepoRoot(t), webhookGatewayPageRel))
	if err != nil {
		t.Fatal(err)
	}
	table := webhookFlagTable(t, string(page))
	for name := range binary {
		if !table[name] {
			t.Errorf("--%s is a flag of brigade-webhook-gateway but no row of the flag table in %s names it", name, webhookGatewayPageRel)
		}
	}
	for name := range table {
		if !binary[name] {
			t.Errorf("the flag table in %s names --%s, which brigade-webhook-gateway does not define", webhookGatewayPageRel, name)
		}
	}
}

// webhookFlagTable returns the flags named in the first column of the page's `| flag | default | meaning |` table,
// which ends at the first line that is not a table row.
func webhookFlagTable(t *testing.T, page string) map[string]bool {
	t.Helper()
	lines := strings.Split(page, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "| flag | default | meaning |" {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no `| flag | default | meaning |` table", webhookGatewayPageRel)
	}
	flags := map[string]bool{}
	for _, l := range lines[start+2:] {
		cells := strings.Split(l, "|")
		if !strings.HasPrefix(l, "|") || len(cells) < 2 {
			break
		}
		for _, m := range webhookTableFlag.FindAllStringSubmatch(cells[1], -1) {
			flags[m[1]] = true
		}
	}
	if len(flags) == 0 {
		t.Fatalf("the flag table in %s names no flag", webhookGatewayPageRel)
	}
	return flags
}
